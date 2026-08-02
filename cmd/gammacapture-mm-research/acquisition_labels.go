package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"time"
)

type acquisitionLabelInput struct {
	DataPath, Symbol                                  string
	From, To                                          time.Time
	TestFrom                                          time.Time
	Horizon                                           time.Duration
	MakerFeeBps, TakerFeeBps, SlippageBps, AdverseBps float64
	MinimumNetEdgeBps                                 float64
}

type chaseObservation struct {
	At                time.Time `json:"at"`
	Hit               bool      `json:"hit"`
	HitLatencyMinutes float64   `json:"hitLatencyMinutes,omitempty"`
	FutureMaxEdgeBps  float64   `json:"futureMaxEdgeBps"`
	Return1mBps       float64   `json:"return1mBps"`
	Return5mBps       float64   `json:"return5mBps"`
	Return10mBps      float64   `json:"return10mBps"`
	TradeImbalance5m  float64   `json:"tradeImbalance5m"`
	TradeCount5m      int       `json:"tradeCount5m"`
	NearHigh10mBps    float64   `json:"nearHigh10mBps"`
	CurrentGate       bool      `json:"currentGate"`
}

type chaseRule struct {
	Return1mMinBps    float64 `json:"return1mMinBps"`
	Return5mMinBps    float64 `json:"return5mMinBps"`
	TradeImbalanceMin float64 `json:"tradeImbalanceMin"`
	MinimumTrades5m   int     `json:"minimumTrades5m"`
	NearHighMinBps    float64 `json:"nearHighMinBps"`
}

type labelRuleStats struct {
	Samples          int     `json:"samples"`
	Hits             int     `json:"hits"`
	HitRate          float64 `json:"hitRate"`
	WilsonLower95    float64 `json:"wilsonLower95"`
	WilsonUpper95    float64 `json:"wilsonUpper95"`
	PositiveCoverage float64 `json:"positiveCoverage"`
}

type acquisitionLabelReport struct {
	Symbol                  string                 `json:"symbol"`
	From                    time.Time              `json:"from"`
	To                      time.Time              `json:"to"`
	Horizon                 string                 `json:"horizon"`
	LabelThresholdBps       float64                `json:"labelThresholdBps"`
	Sampling                string                 `json:"sampling"`
	Observations            int                    `json:"observations"`
	PositiveLabels          int                    `json:"positiveLabels"`
	TrainBaseline           labelRuleStats         `json:"trainBaseline"`
	HoldoutBaseline         labelRuleStats         `json:"holdoutBaseline"`
	Current20BpsGateTrain   labelRuleStats         `json:"current20BpsGateTrain"`
	Current20BpsGateHoldout labelRuleStats         `json:"current20BpsGateHoldout"`
	SelectedRule            chaseRule              `json:"selectedRule"`
	SelectedRuleTrain       labelRuleStats         `json:"selectedRuleTrain"`
	SelectedRuleHoldout     labelRuleStats         `json:"selectedRuleHoldout"`
	CombinedGateTrain       labelRuleStats         `json:"combinedGateTrain"`
	CombinedGateHoldout     labelRuleStats         `json:"combinedGateHoldout"`
	StableSelection         *stableSelectionReport `json:"stableSelection,omitempty"`
	MissedPositiveExamples  []chaseObservation     `json:"missedPositiveExamples,omitempty"`
	Conclusion              string                 `json:"conclusion"`
	Limitations             []string               `json:"limitations"`
}

func runAcquisitionLabelStudy(in acquisitionLabelInput) {
	books := compactBBO(readBBO(in.DataPath, in.Symbol, in.From, in.To))
	trades := compactTrades(readLiveTrades(in.DataPath, in.Symbol, in.From, in.To))
	observations := buildChaseObservations(books, trades, in)
	if len(observations) < 8 {
		fatalf("insufficient independent acquisition labels: %d", len(observations))
	}
	split := len(observations) * 60 / 100
	train, holdout := observations[:split], observations[split:]
	baseTrain := ruleStats(train, func(chaseObservation) bool { return true })
	baseHoldout := ruleStats(holdout, func(chaseObservation) bool { return true })
	current := func(o chaseObservation) bool { return o.CurrentGate }
	currentTrain, currentHoldout := ruleStats(train, current), ruleStats(holdout, current)
	rule, selectedTrain := selectChaseRule(train)
	selected := func(o chaseObservation) bool { return rule.matches(o) }
	selectedHoldout := ruleStats(holdout, selected)
	combined := func(o chaseObservation) bool { return current(o) || selected(o) }
	combinedTrain := ruleStats(train, combined)
	combinedHoldout := ruleStats(holdout, combined)
	missed := make([]chaseObservation, 0, 10)
	for _, o := range holdout {
		if o.Hit && !o.CurrentGate && len(missed) < cap(missed) {
			missed = append(missed, o)
		}
	}
	positive := 0
	for _, o := range observations {
		if o.Hit {
			positive++
		}
	}
	conclusion := fmt.Sprintf("holdout has %d fee-positive starts; current 20 bps gate detected %d, selected causal rule detected %d, and their union detected %d", baseHoldout.Hits, currentHoldout.Hits, selectedHoldout.Hits, combinedHoldout.Hits)
	if selectedHoldout.Samples == 0 || selectedHoldout.Hits == 0 {
		conclusion += "; do not enable IOC acquisition from this rule"
	} else if selectedHoldout.WilsonLower95 <= baseHoldout.WilsonUpper95 {
		conclusion += "; nonzero events exist but the 95% intervals do not establish improvement over baseline"
	} else {
		conclusion += "; holdout supports promotion to a shadow-only live classifier"
	}
	report := acquisitionLabelReport{
		Symbol: in.Symbol, From: in.From, To: in.To, Horizon: in.Horizon.String(),
		LabelThresholdBps: in.TakerFeeBps + in.MakerFeeBps + in.SlippageBps + in.AdverseBps + in.MinimumNetEdgeBps,
		Sampling:          "one causal observation per horizon inside contiguous BBO segments (non-overlapping labels)",
		Observations:      len(observations), PositiveLabels: positive,
		TrainBaseline: baseTrain, HoldoutBaseline: baseHoldout,
		Current20BpsGateTrain: currentTrain, Current20BpsGateHoldout: currentHoldout,
		SelectedRule: rule, SelectedRuleTrain: selectedTrain, SelectedRuleHoldout: selectedHoldout,
		CombinedGateTrain: combinedTrain, CombinedGateHoldout: combinedHoldout,
		StableSelection:        buildStableSelectionReport(observations, in.TestFrom),
		MissedPositiveExamples: missed, Conclusion: conclusion,
		Limitations: []string{
			"a future best bid at the target is public executable/touch evidence, not proof of our private maker fill",
			"BBO files contain capture gaps; labels never cross a gap longer than five minutes",
			"the chronological holdout is untouched by rule selection, but the small sample still needs more live shadow data",
		},
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		fatalf("encode acquisition label report: %v", err)
	}
}

func buildChaseObservations(books []bboSnapshot, trades []tick, in acquisitionLabelInput) []chaseObservation {
	const maxGap = 5 * time.Minute
	lookback := 10 * time.Minute
	threshold := in.TakerFeeBps + in.MakerFeeBps + in.SlippageBps + in.AdverseBps + in.MinimumNetEdgeBps
	var out []chaseObservation
	for start := 0; start < len(books); {
		end := start + 1
		for end < len(books) && books[end].time.Sub(books[end-1].time) <= maxGap {
			end++
		}
		segment := books[start:end]
		if len(segment) >= 3 && segment[len(segment)-1].time.Sub(segment[0].time) >= lookback+in.Horizon {
			for sampleAt := segment[0].time.Add(lookback); !sampleAt.Add(in.Horizon).After(segment[len(segment)-1].time); sampleAt = sampleAt.Add(in.Horizon) {
				i := sort.Search(len(segment), func(i int) bool { return !segment[i].time.Before(sampleAt) })
				if i >= len(segment) {
					break
				}
				book := segment[i]
				mid := (book.bid + book.ask) / 2
				midAt := func(d time.Duration) float64 {
					target := book.time.Add(-d)
					j := sort.Search(i+1, func(j int) bool { return segment[j].time.After(target) }) - 1
					if j < 0 {
						return 0
					}
					return (segment[j].bid + segment[j].ask) / 2
				}
				ret := func(d time.Duration) float64 {
					p := midAt(d)
					if p <= 0 {
						return 0
					}
					return math.Log(mid/p) * 10_000
				}
				pastStart := sort.Search(i+1, func(j int) bool { return !segment[j].time.Before(book.time.Add(-lookback)) })
				pastHigh := mid
				for j := pastStart; j <= i; j++ {
					m := (segment[j].bid + segment[j].ask) / 2
					if m > pastHigh {
						pastHigh = m
					}
				}
				futureEnd := sort.Search(len(segment), func(j int) bool { return segment[j].time.After(book.time.Add(in.Horizon)) })
				maxBid, hitAt := book.bid, time.Time{}
				for j := i + 1; j < futureEnd; j++ {
					if segment[j].bid > maxBid {
						maxBid = segment[j].bid
					}
					if hitAt.IsZero() && math.Log(segment[j].bid/book.ask)*10_000 >= threshold {
						hitAt = segment[j].time
					}
				}
				tradeStart := sort.Search(len(trades), func(j int) bool { return !trades[j].time.Before(book.time.Add(-5 * time.Minute)) })
				tradeEnd := sort.Search(len(trades), func(j int) bool { return trades[j].time.After(book.time) })
				buy, sell := 0.0, 0.0
				for j := tradeStart; j < tradeEnd; j++ {
					if string(trades[j].side) == "BUY" {
						buy += trades[j].size
					} else {
						sell += trades[j].size
					}
				}
				imbalance := 0.0
				if buy+sell > 0 {
					imbalance = (buy - sell) / (buy + sell)
				}
				o := chaseObservation{At: book.time, Hit: !hitAt.IsZero(), FutureMaxEdgeBps: math.Log(maxBid/book.ask)*10_000 - threshold,
					Return1mBps: ret(time.Minute), Return5mBps: ret(5 * time.Minute), Return10mBps: ret(10 * time.Minute),
					TradeImbalance5m: imbalance, TradeCount5m: tradeEnd - tradeStart, NearHigh10mBps: math.Log(mid/pastHigh) * 10_000}
				if o.Hit {
					o.HitLatencyMinutes = hitAt.Sub(book.time).Minutes()
				}
				o.CurrentGate = o.Return10mBps >= 20
				out = append(out, o)
			}
		}
		start = end
	}
	return out
}

func (r chaseRule) matches(o chaseObservation) bool {
	return o.Return1mBps >= r.Return1mMinBps && o.Return5mBps >= r.Return5mMinBps &&
		o.TradeImbalance5m >= r.TradeImbalanceMin && o.TradeCount5m >= r.MinimumTrades5m && o.NearHigh10mBps >= r.NearHighMinBps
}

func selectChaseRule(train []chaseObservation) (chaseRule, labelRuleStats) {
	best := chaseRule{Return1mMinBps: 1e9}
	bestStats := labelRuleStats{}
	for _, r1 := range []float64{-5, 0, 2, 5, 10} {
		for _, r5 := range []float64{-10, -5, 0, 5, 10, 20} {
			for _, imbalance := range []float64{-1, -0.25, 0, 0.25, 0.5} {
				for _, count := range []int{0, 1, 3, 5, 10} {
					for _, nearHigh := range []float64{-1e9, -20, -10, -5} {
						r := chaseRule{r1, r5, imbalance, count, nearHigh}
						stats := ruleStats(train, func(o chaseObservation) bool { return r.matches(o) })
						if stats.Samples < 5 {
							continue
						}
						if stats.WilsonLower95 > bestStats.WilsonLower95 || (stats.WilsonLower95 == bestStats.WilsonLower95 && stats.PositiveCoverage > bestStats.PositiveCoverage) {
							best, bestStats = r, stats
						}
					}
				}
			}
		}
	}
	return best, bestStats
}

func ruleStats(observations []chaseObservation, selected func(chaseObservation) bool) labelRuleStats {
	totalPos, samples, hits := 0, 0, 0
	for _, o := range observations {
		if o.Hit {
			totalPos++
		}
		if selected(o) {
			samples++
			if o.Hit {
				hits++
			}
		}
	}
	lower, upper := wilson95(hits, samples)
	s := labelRuleStats{Samples: samples, Hits: hits, WilsonLower95: lower, WilsonUpper95: upper}
	if samples > 0 {
		s.HitRate = float64(hits) / float64(samples)
	}
	if totalPos > 0 {
		s.PositiveCoverage = float64(hits) / float64(totalPos)
	}
	return s
}

func wilson95(successes, samples int) (float64, float64) {
	if samples == 0 {
		return 0, 1
	}
	z, n, p := 1.959963984540054, float64(samples), float64(successes)/float64(samples)
	denom := 1 + z*z/n
	center := (p + z*z/(2*n)) / denom
	half := z * math.Sqrt((p*(1-p)+z*z/(4*n))/n) / denom
	return math.Max(0, center-half), math.Min(1, center+half)
}
