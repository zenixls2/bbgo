package main

import (
	"encoding/json"
	"math"
	"os"
	"sort"
	"time"

	"github.com/c9s/bbgo/pkg/strategy/gammacapture"
	"github.com/c9s/bbgo/pkg/types"
)

type normalFlowPressureDistributionStudyInput struct {
	DataPath       string
	Symbol         string
	From           time.Time
	To             time.Time
	Horizon        time.Duration
	SampleInterval time.Duration
	FlowWindow     time.Duration
	ReplayCacheDir string
	BBOInterval    time.Duration
}

type normalFlowPressureDistributionStudyReport struct {
	Study                  string                                        `json:"study"`
	Symbol                 string                                        `json:"symbol"`
	WarmupFrom             time.Time                                     `json:"warmupFrom"`
	From                   time.Time                                     `json:"from"`
	To                     time.Time                                     `json:"to"`
	Horizon                time.Duration                                 `json:"horizon"`
	SampleInterval         time.Duration                                 `json:"sampleInterval"`
	FlowWindow             time.Duration                                 `json:"flowWindow"`
	BBOEvents              int                                           `json:"bboEvents"`
	TradeEvents            int                                           `json:"tradeEvents"`
	Anchors                int                                           `json:"anchors"`
	ReplayCacheHit         bool                                          `json:"replayCacheHit"`
	PrivateFillCalibration bool                                          `json:"privateFillCalibration"`
	CalibrationNote        string                                        `json:"calibrationNote"`
	Variants               []normalFlowPressureDistributionVariantReport `json:"variants"`
	Decision               string                                        `json:"decision"`
}

type normalFlowPressureDistributionVariantReport struct {
	Variant                 string  `json:"variant"`
	Samples                 int     `json:"samples"`
	EligibleSamples         int     `json:"eligibleSamples"`
	ActiveSamples           int     `json:"activeSamples"`
	ActiveFraction          float64 `json:"activeFraction"`
	MeanRawImbalance        float64 `json:"meanRawImbalance"`
	RawStd                  float64 `json:"rawStd"`
	RawP05                  float64 `json:"rawP05"`
	RawP50                  float64 `json:"rawP50"`
	RawP95                  float64 `json:"rawP95"`
	MeanTransformed         float64 `json:"meanTransformed"`
	TransformedStd          float64 `json:"transformedStd"`
	TransformedP05          float64 `json:"transformedP05"`
	TransformedP50          float64 `json:"transformedP50"`
	TransformedP95          float64 `json:"transformedP95"`
	SignFlipFraction        float64 `json:"signFlipFraction"`
	MeanDirectionalMoveBps  float64 `json:"meanDirectionalMoveBps"`
	DirectionalSEBps        float64 `json:"directionalSEBps"`
	DirectionalLower95Bps   float64 `json:"directionalLower95Bps"`
	DirectionalHitRate      float64 `json:"directionalHitRate"`
	DirectionalP10Bps       float64 `json:"directionalP10Bps"`
	DirectionalP50Bps       float64 `json:"directionalP50Bps"`
	SignalReturnCorrelation float64 `json:"signalReturnCorrelation"`
	PositiveDayBlocks       int     `json:"positiveDayBlocks"`
	DayBlocks               int     `json:"dayBlocks"`
	Decision                string  `json:"decision"`
	DecisionReason          string  `json:"decisionReason"`
}

type normalFlowPressureDistributionObservation struct {
	Raw        float64
	TradeCount int
	FutureMove float64
	Day        string
}

type normalFlowPressureDistributionAccumulator struct {
	config             gammacapture.NormalFlowPressureDistributionConfig
	model              *gammacapture.NormalFlowPressureDistributionModel
	raw                []float64
	transformed        []float64
	directional        []float64
	signals            []float64
	returns            []float64
	positiveDirections int
	signFlips          int
	dayValues          map[string][]float64
}

func newNormalFlowPressureDistributionAccumulator(variant gammacapture.NormalFlowPressureDistributionVariant) *normalFlowPressureDistributionAccumulator {
	config := gammacapture.NormalFlowPressureDistributionConfig{
		Variant: variant, MinAbsImbalance: 0.10, MinTrades: 20,
		PriorTrades: 20, MaximumSignalWeight: 0.35,
		EWMAAlpha: 0.10, RankWindow: 96, WinsorCap: 0.25,
		RobustScaleFloor: 0.05,
	}
	return &normalFlowPressureDistributionAccumulator{
		config: config, model: gammacapture.NewNormalFlowPressureDistributionModel(config),
		dayValues: make(map[string][]float64),
	}
}

func runNormalFlowPressureDistributionStudy(in normalFlowPressureDistributionStudyInput) {
	if !in.From.Before(in.To) || in.Horizon <= 0 || in.SampleInterval <= 0 || in.FlowWindow <= 0 {
		fatalf("normal-flow pressure distribution study requires positive replay bounds and horizons")
	}
	warmupFrom := in.From.Add(-30 * time.Minute)
	books, trades, cacheHit := loadWarmReplayDatasetAtInterval(
		in.DataPath, in.Symbol, warmupFrom, in.To, in.From, "", in.ReplayCacheDir, in.BBOInterval)
	books = compactBBO(books)
	trades = compactTrades(trades)
	if len(books) < 2 || len(trades) == 0 {
		fatalf("insufficient distribution study data: bbo=%d trades=%d", len(books), len(trades))
	}

	variants := []gammacapture.NormalFlowPressureDistributionVariant{
		gammacapture.NormalFlowPressureDistributionCurrent,
		gammacapture.NormalFlowPressureDistributionWinsorized,
		gammacapture.NormalFlowPressureDistributionRobustTanh,
		gammacapture.NormalFlowPressureDistributionBalancedRank,
	}
	accumulators := make([]*normalFlowPressureDistributionAccumulator, 0, len(variants))
	for _, variant := range variants {
		accumulators = append(accumulators, newNormalFlowPressureDistributionAccumulator(variant))
	}

	// The rolling flow is built from trade notional, matching
	// FastEvidenceModel.SignedTradeImbalance5m. BBO observations never alter
	// the feature state, and future labels are only read after the decision.
	type flowTrade struct {
		at     time.Time
		signed float64
		total  float64
	}
	flow := make([]flowTrade, 0, 256)
	tradeIndex := 0
	flowSigned, flowTotal := 0.0, 0.0
	nextAnchor := in.From
	anchorCount := 0
	for _, book := range books {
		for tradeIndex < len(trades) && !trades[tradeIndex].time.After(book.time) {
			t := trades[tradeIndex]
			notional := t.price * t.size
			if notional > 0 && !math.IsNaN(notional) && !math.IsInf(notional, 0) {
				signed := notional
				if t.side == types.SideTypeSell {
					signed = -signed
				}
				flow = append(flow, flowTrade{at: t.time, signed: signed, total: notional})
				flowSigned += signed
				flowTotal += notional
			}
			tradeIndex++
		}
		cutoff := book.time.Add(-in.FlowWindow)
		first := 0
		for first < len(flow) && flow[first].at.Before(cutoff) {
			flowSigned -= flow[first].signed
			flowTotal -= flow[first].total
			first++
		}
		if first > 0 {
			flow = append([]flowTrade(nil), flow[first:]...)
		}
		if book.time.Before(nextAnchor) {
			continue
		}
		currentMid := (book.bid + book.ask) / 2
		if currentMid <= 0 {
			continue
		}
		futureAt := book.time.Add(in.Horizon)
		futureIndex := sort.Search(len(books), func(i int) bool { return !books[i].time.Before(futureAt) })
		if futureIndex >= len(books) {
			break
		}
		futureMid := (books[futureIndex].bid + books[futureIndex].ask) / 2
		if futureMid <= 0 {
			continue
		}
		raw := 0.0
		if flowTotal > 0 {
			raw = clampDistributionStudy(flowSigned / flowTotal)
		}
		observation := normalFlowPressureDistributionObservation{
			Raw: raw, TradeCount: len(flow),
			FutureMove: math.Log(futureMid/currentMid) * 10_000,
			Day:        book.time.UTC().Format("2006-01-02"),
		}
		for _, accumulator := range accumulators {
			decision := accumulator.model.Evaluate(observation.Raw, observation.TradeCount)
			accumulator.raw = append(accumulator.raw, observation.Raw)
			if observation.TradeCount >= accumulator.config.MinTrades {
				accumulator.transformed = append(accumulator.transformed, decision.Transformed)
			}
			if decision.Applied {
				accumulator.activeDirections(observation, decision.Signal)
			}
			// Distribution calibration is feature-only and happens after the
			// current prediction has been scored, before the next anchor.
			accumulator.model.Observe(observation.Raw)
		}
		anchorCount++
		for !nextAnchor.After(book.time) {
			nextAnchor = nextAnchor.Add(in.SampleInterval)
		}
	}

	report := normalFlowPressureDistributionStudyReport{
		Study:  "normal-flow-pressure-distribution",
		Symbol: in.Symbol, WarmupFrom: warmupFrom, From: in.From, To: in.To,
		Horizon: in.Horizon, SampleInterval: in.SampleInterval, FlowWindow: in.FlowWindow,
		BBOEvents: len(books), TradeEvents: len(trades), Anchors: anchorCount,
		ReplayCacheHit: cacheHit, PrivateFillCalibration: false,
		CalibrationNote: "public BBO/trade replay only; no private maker-fill labels or queue calibration",
		Decision:        "inconclusive: distribution candidates require private-fill calibration before live use",
	}
	for _, accumulator := range accumulators {
		report.Variants = append(report.Variants, accumulator.report())
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fatalf("encode normal-flow pressure distribution study: %v", err)
	}
}

func (a *normalFlowPressureDistributionAccumulator) activeDirections(observation normalFlowPressureDistributionObservation, signal float64) {
	if signal == 0 {
		return
	}
	value := math.Copysign(observation.FutureMove, signal)
	a.directional = append(a.directional, value)
	a.signals = append(a.signals, signal)
	a.returns = append(a.returns, observation.FutureMove)
	a.dayValues[observation.Day] = append(a.dayValues[observation.Day], value)
	if value > 0 {
		a.positiveDirections++
	}
	if signal*observation.FutureMove < 0 {
		a.signFlips++
	}
}

func (a *normalFlowPressureDistributionAccumulator) report() normalFlowPressureDistributionVariantReport {
	report := normalFlowPressureDistributionVariantReport{
		Variant: string(a.config.Variant), Samples: len(a.raw),
		EligibleSamples: len(a.transformed), ActiveSamples: len(a.directional),
		MeanRawImbalance: meanDistributionStudy(a.raw), RawStd: stdDistributionStudy(a.raw),
		RawP05: percentile(a.raw, .05), RawP50: percentile(a.raw, .50), RawP95: percentile(a.raw, .95),
		MeanTransformed: meanDistributionStudy(a.transformed), TransformedStd: stdDistributionStudy(a.transformed),
		TransformedP05: percentile(a.transformed, .05), TransformedP50: percentile(a.transformed, .50), TransformedP95: percentile(a.transformed, .95),
		MeanDirectionalMoveBps: meanDistributionStudy(a.directional),
		DirectionalHitRate:     safeFraction(a.positiveDirections, len(a.directional)),
		DirectionalP10Bps:      percentile(a.directional, .10), DirectionalP50Bps: percentile(a.directional, .50),
		SignalReturnCorrelation: correlationDistributionStudy(a.signals, a.returns),
		SignFlipFraction:        safeFraction(a.signFlips, len(a.directional)),
		PositiveDayBlocks:       0, DayBlocks: len(a.dayValues),
		Decision: "screen only", DecisionReason: "no private-fill calibration",
	}
	if len(a.directional) > 1 {
		variance := 0.0
		mean := report.MeanDirectionalMoveBps
		for _, value := range a.directional {
			variance += (value - mean) * (value - mean)
		}
		report.DirectionalSEBps = math.Sqrt(variance / float64(len(a.directional)-1) / float64(len(a.directional)))
		report.DirectionalLower95Bps = mean - 1.959963984540054*report.DirectionalSEBps
	}
	if len(a.directional) > 0 {
		report.ActiveFraction = float64(len(a.directional)) / float64(len(a.raw))
	}
	for _, values := range a.dayValues {
		if meanDistributionStudy(values) > 0 {
			report.PositiveDayBlocks++
		}
	}
	return report
}

func clampDistributionStudy(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return math.Max(-1, math.Min(1, value))
}

func meanDistributionStudy(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sum := 0.0
	for _, value := range values {
		sum += value
	}
	return sum / float64(len(values))
}

func stdDistributionStudy(values []float64) float64 {
	if len(values) < 2 {
		return 0
	}
	mean := meanDistributionStudy(values)
	variance := 0.0
	for _, value := range values {
		variance += (value - mean) * (value - mean)
	}
	return math.Sqrt(variance / float64(len(values)-1))
}

func safeFraction(numerator, denominator int) float64 {
	if denominator <= 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

func correlationDistributionStudy(x, y []float64) float64 {
	if len(x) != len(y) || len(x) < 2 {
		return 0
	}
	meanX, meanY := meanDistributionStudy(x), meanDistributionStudy(y)
	var numerator, xVar, yVar float64
	for i := range x {
		dx, dy := x[i]-meanX, y[i]-meanY
		numerator += dx * dy
		xVar += dx * dx
		yVar += dy * dy
	}
	if xVar <= 0 || yVar <= 0 {
		return 0
	}
	return numerator / math.Sqrt(xVar*yVar)
}
