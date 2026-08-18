package main

import (
	"encoding/json"
	"math"
	"os"
	"sort"
	"time"

	"github.com/c9s/bbgo/pkg/strategy/gammacapture"
)

// quoteLifecycleReplayInput is deliberately research-only.  It replays one
// side of the passive quote lifecycle against public executable BBO and does
// not pretend to observe private queue fills.
type quoteLifecycleReplayInput struct {
	DataPath, Symbol string
	From, To         time.Time
	ConfigPath       string
	ReplayCacheDir   string
	Horizon          time.Duration
	DistanceBps      float64
	ReplacementCost  float64
}

type lifecycleArmStats struct {
	Windows    float64
	Touches    float64
	MarkoutSum float64
}

func (s *lifecycleArmStats) snapshot() (probability, markout, effective float64) {
	if s == nil {
		return 0.5, 0, 0
	}
	probability = (s.Touches + 0.5) / (s.Windows + 1)
	if s.Touches > 0 {
		markout = s.MarkoutSum / s.Touches
		// Empirical-Bayes shrinkage keeps a sparse distance bucket from
		// manufacturing a large continuation value from one path.
		markout *= s.Touches / (s.Touches + 1)
	}
	return probability, markout, s.Windows
}

func (s *lifecycleArmStats) update(touched bool, markout float64) {
	if s == nil || !finiteLifecycleReplay(markout) {
		return
	}
	s.Windows++
	if touched {
		s.Touches++
		s.MarkoutSum += markout
	}
}

func finiteLifecycleReplay(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

type lifecyclePendingLabel struct {
	At, MaturesAt time.Time
	Side          lifecycleReplaySide
	Quote         float64
	StartIndex    int
	EndIndex      int
	Bucket        int
}

type lifecycleReplaySide string

const (
	lifecycleBuy  lifecycleReplaySide = "BUY"
	lifecycleSell lifecycleReplaySide = "SELL"
)

type quoteLifecycleReplayReport struct {
	Name                  string    `json:"name"`
	Symbol                string    `json:"symbol"`
	From                  time.Time `json:"from"`
	To                    time.Time `json:"to"`
	Horizon               string    `json:"horizon"`
	DistanceBps           float64   `json:"distanceBps"`
	ReplacementCostBps    float64   `json:"replacementCostBps"`
	EligibleDecisions     int       `json:"eligibleDecisions"`
	ScoredDecisions       int       `json:"scoredDecisions"`
	EffectiveSamples      float64   `json:"effectiveSamples"`
	KeepActions           int       `json:"keepActions"`
	ReplaceActions        int       `json:"replaceActions"`
	CancelActions         int       `json:"cancelActions"`
	SelectedTouches       int       `json:"selectedTouches"`
	AlwaysReplaceTouches  int       `json:"alwaysReplaceTouches"`
	SelectedNetBps        float64   `json:"selectedNetBps"`
	AlwaysReplaceNetBps   float64   `json:"alwaysReplaceNetBps"`
	IncrementalMeanBps    float64   `json:"incrementalMeanBps"`
	IncrementalSEBps      float64   `json:"incrementalStandardErrorBps"`
	IncrementalLower95Bps float64   `json:"incrementalLower95Bps"`
	PositiveBlocks        int       `json:"positiveBlocks"`
	TotalBlocks           int       `json:"totalBlocks"`
	Gate                  string    `json:"gate"`
	DataNote              string    `json:"dataNote"`
}

type lifecycleReplayDecision struct {
	At              time.Time
	Side            lifecycleReplaySide
	Action          gammacapture.QuoteLifecycleAction
	CurrentQuote    float64
	CandidateQuote  float64
	CurrentBucket   int
	CandidateBucket int
	CurrentLabel    lifecyclePendingLabel
	CandidateLabel  lifecyclePendingLabel
	CurrentActive   bool
	CandidateActive bool
}

func lifecycleDistanceBucket(distance float64) int {
	if distance <= 0 || !finiteLifecycleReplay(distance) {
		return 0
	}
	return int(math.Round(distance/5)) * 5
}

func lifecycleQuote(side lifecycleReplaySide, book bboSnapshot, distanceBps float64) float64 {
	if side == lifecycleBuy && book.ask > 0 {
		return book.ask * math.Exp(-distanceBps/10_000)
	}
	if side == lifecycleSell && book.bid > 0 {
		return book.bid * math.Exp(distanceBps/10_000)
	}
	return 0
}

func lifecycleDistance(side lifecycleReplaySide, book bboSnapshot, quote float64) float64 {
	if quote <= 0 {
		return 0
	}
	if side == lifecycleBuy && book.ask > 0 {
		return math.Max(0, math.Log(book.ask/quote)*10_000)
	}
	if side == lifecycleSell && book.bid > 0 {
		return math.Max(0, math.Log(quote/book.bid)*10_000)
	}
	return 0
}

func lifecycleLabel(books []bboSnapshot, side lifecycleReplaySide, quote float64, start, end int) (bool, float64) {
	if start < 0 || end <= start || end > len(books) || quote <= 0 {
		return false, 0
	}
	terminalBid := books[end-1].bid
	if terminalBid <= 0 {
		return false, 0
	}
	touched := false
	for i := start; i < end; i++ {
		if side == lifecycleBuy && books[i].ask > 0 && books[i].ask <= quote {
			touched = true
			break
		}
		if side == lifecycleSell && books[i].bid > 0 && books[i].bid >= quote {
			touched = true
			break
		}
	}
	if !touched {
		return false, 0
	}
	return true, gammacapture.OneFillTerminalMarkoutBps(side == lifecycleBuy, quote, terminalBid)
}

func lifecycleIndexAtOrAfter(books []bboSnapshot, at time.Time) int {
	return sort.Search(len(books), func(i int) bool { return !books[i].time.Before(at) })
}

func lifecycleUpdatePending(
	books []bboSnapshot,
	pending []lifecyclePendingLabel,
	stats map[lifecycleReplaySide]map[int]*lifecycleArmStats,
	now time.Time,
) []lifecyclePendingLabel {
	cut := 0
	for cut < len(pending) && !pending[cut].MaturesAt.After(now) {
		p := pending[cut]
		byBucket := stats[p.Side]
		if byBucket == nil {
			byBucket = make(map[int]*lifecycleArmStats)
			stats[p.Side] = byBucket
		}
		arm := byBucket[p.Bucket]
		if arm == nil {
			arm = &lifecycleArmStats{}
			byBucket[p.Bucket] = arm
		}
		touched, markout := lifecycleLabel(books, p.Side, p.Quote, p.StartIndex, p.EndIndex)
		arm.update(touched, markout)
		cut++
	}
	return pending[cut:]
}

func lifecycleArmValue(
	arm *lifecycleArmStats,
	feeBps, adverseBps, discount float64,
) (probability, markout, continuation float64) {
	probability, markout, _ = arm.snapshot()
	continuation = math.Max(0, markout-feeBps-adverseBps)
	return
}

func runQuoteLifecycleReplay(in quoteLifecycleReplayInput) {
	if in.Horizon <= 0 {
		fatalf("quote lifecycle horizon must be positive")
	}
	_, _, cfg := loadProductionConfig(in.ConfigPath, in.Symbol)
	if in.DistanceBps <= 0 {
		in.DistanceBps = math.Max(1, cfg.MinimumHalfSpreadBps)
	}
	books, _, _ := loadExactReplayDataset(in.DataPath, in.Symbol, in.From, in.To, "quote-lifecycle-v1", in.ReplayCacheDir)
	books = compactBBO(books)
	if len(books) < 4 {
		fatalf("quote lifecycle replay requires at least four BBO events")
	}

	feeBps := cfg.MakerFeeBps
	adverseBps := cfg.AdverseSelectionBps
	discount := 1.0 // zero short-rate convention; no arbitrary time decay is fitted.
	stats := map[lifecycleReplaySide]map[int]*lifecycleArmStats{
		lifecycleBuy:  make(map[int]*lifecycleArmStats),
		lifecycleSell: make(map[int]*lifecycleArmStats),
	}
	pending := make([]lifecyclePendingLabel, 0, 2*len(books)/20)
	var current [2]float64
	var currentActive [2]bool
	report := quoteLifecycleReplayReport{
		Name: "gamma-capture-quote-lifecycle-bellman-v1", Symbol: in.Symbol,
		From: in.From, To: in.To, Horizon: in.Horizon.String(), DistanceBps: in.DistanceBps,
		ReplacementCostBps: in.ReplacementCost,
		DataNote:           "same-symbol public executable-BBO touch proxy; no private fills or queue position",
	}
	blockSum := map[string]float64{}
	blockCount := map[string]int{}

	for start := 0; ; {
		decisionAt := books[start].time
		endAt := decisionAt.Add(in.Horizon)
		end := lifecycleIndexAtOrAfter(books, endAt)
		if end <= start+1 || end >= len(books) {
			break
		}
		pending = lifecycleUpdatePending(books, pending, stats, decisionAt)
		report.EligibleDecisions += 2
		for sideIndex, side := range []lifecycleReplaySide{lifecycleBuy, lifecycleSell} {
			candidate := lifecycleQuote(side, books[start], in.DistanceBps)
			if candidate <= 0 {
				continue
			}
			currentQuote := current[sideIndex]
			currentIsActive := currentActive[sideIndex] && currentQuote > 0
			currentDistance := lifecycleDistance(side, books[start], currentQuote)
			candidateDistance := lifecycleDistance(side, books[start], candidate)
			currentBucket, candidateBucket := lifecycleDistanceBucket(currentDistance), lifecycleDistanceBucket(candidateDistance)
			currentArm := stats[side][currentBucket]
			if currentArm == nil {
				currentArm = &lifecycleArmStats{}
			}
			candidateArm := stats[side][candidateBucket]
			if candidateArm == nil {
				candidateArm = &lifecycleArmStats{}
			}
			cp, cm, cc := lifecycleArmValue(currentArm, feeBps, adverseBps, discount)
			np, nm, nc := lifecycleArmValue(candidateArm, feeBps, adverseBps, discount)
			decision := gammacapture.EvaluateQuoteLifecycleAction(gammacapture.QuoteLifecycleActionInput{
				CurrentActive: currentIsActive, CandidateActive: true,
				CurrentFillProbability: cp, CurrentTerminalMarkoutBps: cm,
				CurrentExecutionCostBps: feeBps, CurrentRiskPenaltyBps: adverseBps,
				CurrentContinuationValueBps: cc,
				CandidateFillProbability:    np, CandidateTerminalMarkoutBps: nm,
				CandidateExecutionCostBps: feeBps, CandidateRiskPenaltyBps: adverseBps,
				CandidateContinuationValueBps: nc,
				ReplacementCostBps:            in.ReplacementCost, DiscountFactor: discount,
			})
			if !decision.Evaluated {
				continue
			}
			report.ScoredDecisions++
			switch decision.Action {
			case gammacapture.QuoteLifecycleKeep:
				report.KeepActions++
			case gammacapture.QuoteLifecycleReplace:
				report.ReplaceActions++
			case gammacapture.QuoteLifecycleCancel:
				report.CancelActions++
			}
			currentForAction := currentQuote
			if !currentIsActive {
				currentForAction = 0
			}
			selectedQuote := currentForAction
			if decision.Action == gammacapture.QuoteLifecycleReplace {
				selectedQuote = candidate
			}
			selectedTouched, selectedMarkout := lifecycleLabel(books, side, selectedQuote, start+1, end)
			if decision.Action == gammacapture.QuoteLifecycleCancel {
				selectedTouched, selectedMarkout = false, 0
			}
			selectedNet := 0.0
			if selectedTouched {
				selectedNet = selectedMarkout - feeBps - adverseBps
				report.SelectedTouches++
			}
			_, replaceMarkout := lifecycleLabel(books, side, candidate, start+1, end)
			replaceTouched, _ := lifecycleLabel(books, side, candidate, start+1, end)
			alwaysReplaceNet := 0.0
			if replaceTouched {
				alwaysReplaceNet = replaceMarkout - feeBps - adverseBps
				report.AlwaysReplaceTouches++
			}
			report.SelectedNetBps += selectedNet
			report.AlwaysReplaceNetBps += alwaysReplaceNet
			increment := selectedNet - alwaysReplaceNet
			block := decisionAt.UTC().Format(time.DateOnly)
			blockSum[block] += increment
			blockCount[block]++
			// Both counterfactual arm labels are delayed until end. This is a
			// public-BBO markout label, not evidence of private execution.
			if currentIsActive {
				pending = append(pending, lifecyclePendingLabel{At: decisionAt, MaturesAt: endAt,
					Side: side, Quote: currentQuote, StartIndex: start + 1, EndIndex: end,
					Bucket: currentBucket})
			}
			pending = append(pending, lifecyclePendingLabel{At: decisionAt, MaturesAt: endAt,
				Side: side, Quote: candidate, StartIndex: start + 1, EndIndex: end,
				Bucket: candidateBucket})
			if decision.Action == gammacapture.QuoteLifecycleCancel {
				current[sideIndex], currentActive[sideIndex] = 0, false
			} else if decision.Action == gammacapture.QuoteLifecycleReplace {
				current[sideIndex], currentActive[sideIndex] = candidate, true
			}
		}
		start = end
	}
	if report.ScoredDecisions > 0 {
		report.SelectedNetBps /= float64(report.ScoredDecisions)
		report.AlwaysReplaceNetBps /= float64(report.ScoredDecisions)
	}
	report.EffectiveSamples = float64(report.ScoredDecisions)
	blocks := make([]float64, 0, len(blockSum))
	for day, sum := range blockSum {
		if blockCount[day] <= 0 {
			continue
		}
		value := sum / float64(blockCount[day])
		blocks = append(blocks, value)
		if value > 0 {
			report.PositiveBlocks++
		}
	}
	report.TotalBlocks = len(blocks)
	if len(blocks) > 0 {
		mean := 0.0
		for _, value := range blocks {
			mean += value
		}
		mean /= float64(len(blocks))
		report.IncrementalMeanBps = mean
		if len(blocks) > 1 {
			variance := 0.0
			for _, value := range blocks {
				variance += (value - mean) * (value - mean)
			}
			report.IncrementalSEBps = math.Sqrt(variance / float64(len(blocks)-1) / float64(len(blocks)))
			report.IncrementalLower95Bps = mean - 1.959963984540054*report.IncrementalSEBps
		}
	}
	report.Gate = "INCONCLUSIVE_SAMPLES"
	if report.ScoredDecisions >= 24 && report.TotalBlocks >= 5 {
		// A one-sided terminal model that selects CANCEL on nearly every
		// window has not identified a lifecycle alpha; it has only rediscovered
		// that an isolated maker leg is fee-negative.  Require observable
		// KEEP/REPLACE decisions before a positive paired result can promote.
		activeActions := report.KeepActions + report.ReplaceActions
		if activeActions < 5 || float64(activeActions) < 0.05*float64(report.ScoredDecisions) {
			report.Gate = "INCONCLUSIVE_NO_ACTION_DIVERSITY"
		} else if report.IncrementalLower95Bps > 0 {
			report.Gate = "PROMOTE_COMPONENT_REPLAY"
		} else {
			report.Gate = "REJECT_NO_INCREMENTAL_VALUE"
		}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		fatalf("encode quote lifecycle replay report: %v", err)
	}
}
