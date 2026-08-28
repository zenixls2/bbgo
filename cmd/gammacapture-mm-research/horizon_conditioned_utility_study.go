package main

import (
	"encoding/json"
	"math"
	"os"
	"sort"
	"time"

	"github.com/c9s/bbgo/pkg/strategy/gammacapture"
)

// This study is deliberately standalone. It compares the current short
// terminal CE with a continuation-conditioned quantity utility while keeping
// direction (causal pivot regime) and price distance fixed. Public BBO touches
// are an arrival proxy only; they cannot pass the private-fill gate.
type horizonConditionedUtilityStudyInput struct {
	ConfigPath          string
	DataPath            string
	Symbol              string
	From, To            time.Time
	ShortHorizon        time.Duration
	ContinuationHorizon time.Duration
	AnchorStep          time.Duration
	PivotReversalBps    float64
	PivotMaxGap         time.Duration
	QuoteDistanceBps    float64
	PairEquityJPY       float64
	QuoteNotionalJPY    float64
	BBOInterval         time.Duration
	HalfLife            time.Duration
}

type horizonConditionedUtilityStudyReport struct {
	Mode                   string                                   `json:"mode"`
	Symbol                 string                                   `json:"symbol"`
	From                   time.Time                                `json:"from"`
	To                     time.Time                                `json:"to"`
	LoadedFrom             time.Time                                `json:"loadedFrom"`
	LoadedTo               time.Time                                `json:"loadedTo"`
	ShortHorizon           string                                   `json:"shortHorizon"`
	ContinuationHorizon    string                                   `json:"continuationHorizon"`
	AnchorStep             string                                   `json:"anchorStep"`
	PivotReversalBps       float64                                  `json:"pivotReversalBps"`
	QuoteDistanceBps       float64                                  `json:"quoteDistanceBps"`
	EntryCostBps           float64                                  `json:"entryCostBps"`
	PairEquityJPY          float64                                  `json:"pairEquityJPY"`
	QuoteNotionalJPY       float64                                  `json:"quoteNotionalJPY"`
	BBOEvents              int                                      `json:"bboEvents"`
	Warnings               []string                                 `json:"warnings"`
	Train                  horizonConditionedUtilitySplitReport     `json:"train"`
	Validation             horizonConditionedUtilitySplitReport     `json:"validation"`
	Holdout                horizonConditionedUtilitySplitReport     `json:"holdout"`
	PrivateFillCalibration horizonConditionedUtilityFillCalibration `json:"privateFillCalibration"`
}

type horizonConditionedUtilitySplitReport struct {
	From                           time.Time `json:"from"`
	To                             time.Time `json:"to"`
	Anchors                        int       `json:"anchors"`
	PivotReady                     int       `json:"pivotReady"`
	Scored                         int       `json:"scored"`
	LegacyAccepted                 int       `json:"legacyAccepted"`
	ContinuationAccepted           int       `json:"continuationAccepted"`
	LegacyFilled                   int       `json:"legacyFilled"`
	ContinuationFilled             int       `json:"continuationFilled"`
	EffectiveSamplesMean           float64   `json:"effectiveSamplesMean"`
	FillProbabilityMean            float64   `json:"fillProbabilityMean"`
	ContinuationForecastMeanBps    float64   `json:"continuationForecastMeanBps"`
	LegacyCEBpsMean                float64   `json:"legacyCEBpsMean"`
	ContinuationCEBpsMean          float64   `json:"continuationCEBpsMean"`
	LegacyRealizedBpsMean          float64   `json:"legacyRealizedBpsMean"`
	ContinuationRealizedBpsMean    float64   `json:"continuationRealizedBpsMean"`
	LegacyIncrementalVsBaselineBps float64   `json:"legacyIncrementalVsBaselineBps"`
	ContinuationIncrementalBps     float64   `json:"continuationIncrementalBps"`
	FillBrier                      float64   `json:"fillBrier"`
	FillBrierSamples               int       `json:"fillBrierSamples"`
	EffectiveBlocks                int       `json:"effectiveBlocks"`
	LegacyPositiveBlocks           int       `json:"legacyPositiveBlocks"`
	ContinuationPositiveBlocks     int       `json:"continuationPositiveBlocks"`
	LegacyBlockMeanSEBps           float64   `json:"legacyBlockMeanSEBps"`
	ContinuationBlockMeanSEBps     float64   `json:"continuationBlockMeanSEBps"`
}

type horizonConditionedUtilityFillCalibration struct {
	Status            string `json:"status"`
	PrivateFills      int    `json:"privateFills"`
	PublicTouchLabels int    `json:"publicTouchLabels"`
	Reason            string `json:"reason"`
}

type horizonUtilityWeightedStats struct {
	LastAt       time.Time
	Weight       float64
	WeightSquare float64
	Sum          float64
	SumSquare    float64
}

func (s *horizonUtilityWeightedStats) decay(at time.Time, halfLife time.Duration) {
	if s == nil || halfLife <= 0 || s.LastAt.IsZero() || !at.After(s.LastAt) {
		return
	}
	factor := math.Exp(-math.Ln2 * at.Sub(s.LastAt).Seconds() / halfLife.Seconds())
	s.Weight *= factor
	s.WeightSquare *= factor * factor
	s.Sum *= factor
	s.SumSquare *= factor
	s.LastAt = at
}

func (s *horizonUtilityWeightedStats) add(at time.Time, value, weight float64, halfLife time.Duration) {
	if s == nil || weight <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return
	}
	s.decay(at, halfLife)
	s.Weight += weight
	s.WeightSquare += weight * weight
	s.Sum += weight * value
	s.SumSquare += weight * value * value
	s.LastAt = at
}

func (s horizonUtilityWeightedStats) mean() float64 {
	if s.Weight <= 0 {
		return 0
	}
	return s.Sum / s.Weight
}

func (s horizonUtilityWeightedStats) variance() float64 {
	if s.Weight <= 0 {
		return 0
	}
	variance := s.SumSquare/s.Weight - s.mean()*s.mean()
	if s.Weight > 1 {
		variance *= s.Weight / (s.Weight - 1)
	}
	return math.Max(0, variance)
}

func (s horizonUtilityWeightedStats) effectiveSamples() float64 {
	if s.WeightSquare <= 0 {
		return 0
	}
	return s.Weight * s.Weight / s.WeightSquare
}

type horizonUtilitySideEstimator struct {
	Exposure, Touches float64
	ExposureSquare    float64
	LastAt            time.Time
	Execution         horizonUtilityWeightedStats
	Continuation      horizonUtilityWeightedStats
	Total             horizonUtilityWeightedStats
	HalfLife          time.Duration
}

func (e *horizonUtilitySideEstimator) decay(at time.Time) {
	if e == nil || e.HalfLife <= 0 || e.LastAt.IsZero() || !at.After(e.LastAt) {
		return
	}
	factor := math.Exp(-math.Ln2 * at.Sub(e.LastAt).Seconds() / e.HalfLife.Seconds())
	e.Exposure *= factor
	e.Touches *= factor
	e.ExposureSquare *= factor * factor
	e.LastAt = at
	e.Execution.decay(at, e.HalfLife)
	e.Continuation.decay(at, e.HalfLife)
	e.Total.decay(at, e.HalfLife)
}

func (e *horizonUtilitySideEstimator) observeExposure(at time.Time, touched bool) {
	if e == nil {
		return
	}
	e.decay(at)
	e.Exposure++
	e.ExposureSquare++
	if touched {
		e.Touches++
	}
	e.LastAt = at
}

func (e *horizonUtilitySideEstimator) observeExecution(at time.Time, execution float64) {
	if e == nil || math.IsNaN(execution) || math.IsInf(execution, 0) {
		return
	}
	e.decay(at)
	e.Execution.add(at, execution, 1, e.HalfLife)
	e.LastAt = at
}

func (e *horizonUtilitySideEstimator) observeContinuation(at time.Time, execution, continuation float64) {
	if e == nil || math.IsNaN(execution) || math.IsInf(execution, 0) ||
		math.IsNaN(continuation) || math.IsInf(continuation, 0) {
		return
	}
	e.decay(at)
	e.Continuation.add(at, continuation, 1, e.HalfLife)
	e.Total.add(at, execution+continuation, 1, e.HalfLife)
	e.LastAt = at
}

type horizonUtilitySideSnapshot struct {
	Ready                    bool
	FillProbability          float64
	FillProbabilityStdErr    float64
	ExecutionMeanBps         float64
	ExecutionVarianceBps2    float64
	ContinuationMeanBps      float64
	ContinuationVarianceBps2 float64
	TotalMeanBps             float64
	TotalVarianceBps2        float64
	EffectiveSamples         float64
}

func (e *horizonUtilitySideEstimator) snapshot(at time.Time) horizonUtilitySideSnapshot {
	if e == nil {
		return horizonUtilitySideSnapshot{}
	}
	e.decay(at)
	exposure := math.Max(0, e.Exposure)
	p := (e.Touches + 1) / (exposure + 2)
	pSE := math.Sqrt(math.Max(0, p*(1-p)/(exposure+3)))
	effective := e.Total.effectiveSamples()
	return horizonUtilitySideSnapshot{
		Ready:                    exposure >= 4 && e.Execution.effectiveSamples() >= 4 && effective >= 4,
		FillProbability:          clampHorizonUtilityProbability(p),
		FillProbabilityStdErr:    pSE,
		ExecutionMeanBps:         e.Execution.mean(),
		ExecutionVarianceBps2:    e.Execution.variance(),
		ContinuationMeanBps:      e.Continuation.mean(),
		ContinuationVarianceBps2: e.Continuation.variance(),
		TotalMeanBps:             e.Total.mean(),
		TotalVarianceBps2:        e.Total.variance(),
		EffectiveSamples:         effective,
	}
}

type horizonUtilityPending struct {
	At, ShortAt, PivotMaturesAt time.Time
	Direction                   int
	StartBid, StartAsk          float64
	ShortBid, ShortAsk          float64
	Quote                       float64
	PivotOutcomeBps             float64
	Phase                       int
	Touched                     bool
	ShortUpdated                bool
	PivotUpdated                bool
	Execution                   float64
}

type horizonUtilityPivotEvent struct {
	At, PivotAt time.Time
	Direction   int
}

type horizonUtilityStudyRow struct {
	At                      time.Time
	Block                   int
	Direction               int
	PublicFillProbability   float64
	Touched                 bool
	ShortOutcomeBps         float64
	ContinuationBps         float64
	TotalOutcomeBps         float64
	ContinuationKnown       bool
	EffectiveSamples        float64
	ContinuationForecastBps float64
	Legacy                  gammacapture.HorizonConditionedUtilityDecision
	Enhanced                gammacapture.HorizonConditionedUtilityDecision
}

func runHorizonConditionedUtilityStudy(input horizonConditionedUtilityStudyInput) {
	if input.From.IsZero() || !input.From.Before(input.To) || input.ShortHorizon <= 0 ||
		input.ContinuationHorizon <= input.ShortHorizon || input.AnchorStep <= 0 ||
		input.PivotReversalBps <= 0 || input.PivotMaxGap <= 0 || input.QuoteDistanceBps <= 0 {
		fatalf("invalid horizon-conditioned utility study configuration")
	}
	if input.PairEquityJPY <= 0 {
		input.PairEquityJPY = 7_255
	}
	if input.QuoteNotionalJPY <= 0 {
		input.QuoteNotionalJPY = 120
	}
	if input.BBOInterval <= 0 {
		input.BBOInterval = time.Second
	}
	_, _, cfg := loadProductionConfig(input.ConfigPath, input.Symbol)
	entryCost := math.Max(0, cfg.MakerFeeBps+cfg.AdverseSelectionBps)
	if input.HalfLife <= 0 {
		input.HalfLife = time.Duration(cfg.HorizonLookback)
	}
	if input.HalfLife <= 0 {
		input.HalfLife = 6 * time.Hour
	}
	loadedFrom := input.From.Add(-input.HalfLife - input.ContinuationHorizon - time.Hour)
	loadedTo := input.To.Add(input.ContinuationHorizon + input.AnchorStep + time.Hour)
	books := compactBBOAtInterval(compactBBO(readBBO(input.DataPath, input.Symbol, loadedFrom, loadedTo)), input.BBOInterval)
	if len(books) < 10 {
		fatalf("insufficient BBO events for horizon-conditioned utility study: %d", len(books))
	}

	decisions := make([]gammacapture.PivotRegimeDecision, len(books))
	pivotEvents := make([]horizonUtilityPivotEvent, 0)
	filter := gammacapture.NewPivotRegimeFilter(gammacapture.PivotRegimeConfig{
		ReversalBps: input.PivotReversalBps, MaxGap: input.PivotMaxGap, MinLegSamples: 2, PriorLegSamples: 2,
	})
	for i, book := range books {
		decisions[i] = filter.Observe(gammacapture.PivotRegimeInput{At: book.time, ReferencePrice: book.midPrice()})
		if decisions[i].PivotChanged {
			pivotEvents = append(pivotEvents, horizonUtilityPivotEvent{
				At: book.time, PivotAt: decisions[i].LastPivot.PivotAt,
				Direction: decisions[i].LastPivot.Direction,
			})
		}
	}

	shortIndices := make([]int, 0)
	for at := input.From; at.Before(input.To); at = at.Add(input.AnchorStep) {
		index := firstBookAtOrAfter(books, at)
		if index >= 0 && index < len(books) && !books[index].time.Before(input.From) && books[index].time.Before(input.To) {
			shortIndices = append(shortIndices, index)
		}
	}
	rows, model := scoreHorizonUtilityAnchors(
		books, decisions, pivotEvents, shortIndices, input, entryCost, cfg.FastRiskAversion, cfg.InventoryRiskZScore)
	report := horizonConditionedUtilityStudyReport{
		Mode: "standalone-causal-horizon-conditioned-utility-study", Symbol: input.Symbol,
		From: input.From, To: input.To, LoadedFrom: loadedFrom, LoadedTo: loadedTo,
		ShortHorizon: input.ShortHorizon.String(), ContinuationHorizon: "next-confirmed-pivot (max " + input.ContinuationHorizon.String() + ")",
		AnchorStep: input.AnchorStep.String(), PivotReversalBps: input.PivotReversalBps,
		QuoteDistanceBps: input.QuoteDistanceBps, EntryCostBps: entryCost,
		PairEquityJPY: input.PairEquityJPY, QuoteNotionalJPY: input.QuoteNotionalJPY,
		BBOEvents: len(books), Warnings: []string{
			"The pivot direction is causal, but this study uses public-BBO touch as an arrival proxy; it does not observe our private queue position.",
			"Short execution value charges maker fee plus adverse-selection allowance once; continuation is the markout from the short executable state to the next confirmed pivot and charges no second entry fee.",
			"Legacy and continuation decisions are quantity-only component scores. No quote price, direction, cancellation, or live inventory target is changed.",
		},
		PrivateFillCalibration: horizonConditionedUtilityFillCalibration{
			Status: "FAILED_NOT_AVAILABLE", Reason: "historical private order/queue labels were not supplied; public touches cannot authorize production",
		},
	}
	trainTo := input.From.Add(input.To.Sub(input.From) / 2)
	validationTo := input.From.Add(3 * input.To.Sub(input.From) / 4)
	report.Train = summarizeHorizonUtilityRows(rows, input.From, trainTo)
	report.Validation = summarizeHorizonUtilityRows(rows, trainTo, validationTo)
	report.Holdout = summarizeHorizonUtilityRows(rows, validationTo, input.To)
	_ = model

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fatalf("encode horizon-conditioned utility study: %v", err)
	}
}

func scoreHorizonUtilityAnchors(
	books []bboSnapshot,
	decisions []gammacapture.PivotRegimeDecision,
	pivotEvents []horizonUtilityPivotEvent,
	anchors []int,
	input horizonConditionedUtilityStudyInput,
	entryCostBps, riskAversion, confidenceZ float64,
) ([]horizonUtilityStudyRow, [2]*horizonUtilitySideEstimator) {
	var models [2]*horizonUtilitySideEstimator
	var phaseModels [2][3]*horizonUtilitySideEstimator
	for i := range models {
		models[i] = &horizonUtilitySideEstimator{HalfLife: input.HalfLife}
		for phase := range phaseModels[i] {
			phaseModels[i][phase] = &horizonUtilitySideEstimator{HalfLife: input.HalfLife}
		}
	}
	pending := make([]*horizonUtilityPending, 0)
	rows := make([]horizonUtilityStudyRow, 0, len(anchors))
	for _, index := range anchors {
		now := books[index].time
		for len(pending) > 0 {
			item := pending[0]
			if !item.ShortUpdated && !item.ShortAt.After(now) {
				side := horizonUtilitySideIndex(item.Direction)
				models[side].observeExposure(item.ShortAt, item.Touched)
				if item.Touched {
					execution := directionalHorizonBps(item.StartBid, item.StartAsk, item.Quote, item.ShortBid, item.ShortAsk, item.Direction, entryCostBps)
					models[side].observeExecution(item.ShortAt, execution)
					phaseModels[side][item.Phase].observeExecution(item.ShortAt, execution)
					item.Execution = execution
				}
				item.ShortUpdated = true
			}
			if item.ShortUpdated && !item.PivotUpdated && !item.PivotMaturesAt.After(now) {
				if item.Touched {
					side := horizonUtilitySideIndex(item.Direction)
					continuation := item.PivotOutcomeBps - item.Execution
					models[side].observeContinuation(item.PivotMaturesAt, item.Execution, continuation)
					phaseModels[side][item.Phase].observeContinuation(item.PivotMaturesAt, item.Execution, continuation)
				}
				item.PivotUpdated = true
			}
			if item.PivotUpdated {
				pending = pending[1:]
				continue
			}
			break
		}
		decision := decisions[index]
		if decision.Ready && decision.Direction != 0 {
			side := horizonUtilitySideIndex(decision.Direction)
			phase := horizonUtilityPhase(decision)
			snapshot := phaseModels[side][phase].snapshot(now)
			if !snapshot.Ready {
				snapshot = models[side].snapshot(now)
			}
			if snapshot.Ready {
				rows = append(rows, makeHorizonUtilityRow(
					books, pivotEvents, index, decision.Direction, snapshot, input,
					entryCostBps, riskAversion, confidenceZ))
			}
		}
		shortIndex := firstBookAtOrAfter(books, now.Add(input.ShortHorizon))
		if shortIndex < 0 || shortIndex >= len(books) {
			continue
		}
		if decision.Ready && decision.Direction != 0 {
			start := books[index]
			short := books[shortIndex]
			quote := start.ask
			if decision.Direction < 0 {
				quote = start.bid
			}
			if decision.Direction > 0 {
				quote *= math.Exp(-input.QuoteDistanceBps / 10_000)
			} else {
				quote *= math.Exp(input.QuoteDistanceBps / 10_000)
			}
			pivotAt, pivotMaturesAt, pivotOutcome, pivotOK := nextHorizonUtilityPivotLabel(
				books, pivotEvents, now, decision.Direction, quote, start.bid, start.ask,
				entryCostBps, input.ContinuationHorizon)
			if !pivotOK {
				continue
			}
			_ = pivotAt
			pending = append(pending, &horizonUtilityPending{
				At: now, ShortAt: short.time, PivotMaturesAt: pivotMaturesAt, Direction: decision.Direction,
				Phase:    horizonUtilityPhase(decision),
				StartBid: start.bid, StartAsk: start.ask, ShortBid: short.bid, ShortAsk: short.ask,
				Quote: quote, PivotOutcomeBps: pivotOutcome,
				Touched: publicTouchBetween(books, index, shortIndex, quote, decision.Direction),
			})
		}
	}
	return rows, models
}

func makeHorizonUtilityRow(
	books []bboSnapshot,
	pivotEvents []horizonUtilityPivotEvent,
	index, direction int,
	snapshot horizonUtilitySideSnapshot,
	input horizonConditionedUtilityStudyInput,
	entryCostBps, riskAversion, confidenceZ float64,
) horizonUtilityStudyRow {
	start := books[index]
	shortIndex := firstBookAtOrAfter(books, start.time.Add(input.ShortHorizon))
	short := books[shortIndex]
	quote := start.ask
	if direction < 0 {
		quote = start.bid
	}
	if direction > 0 {
		quote *= math.Exp(-input.QuoteDistanceBps / 10_000)
	} else {
		quote *= math.Exp(input.QuoteDistanceBps / 10_000)
	}
	touched := publicTouchBetween(books, index, shortIndex, quote, direction)
	shortOutcome := directionalHorizonBps(start.bid, start.ask, quote, short.bid, short.ask, direction, entryCostBps)
	_, _, pivotOutcome, pivotKnown := nextHorizonUtilityPivotLabel(
		books, pivotEvents, start.time, direction, quote, start.bid, start.ask,
		entryCostBps, input.ContinuationHorizon)
	continuation := pivotOutcome - shortOutcome
	if !pivotKnown {
		continuation = 0
	}
	qRatios := []float64{0.25, 0.5, 1, 1.5}
	legacyCandidates := make([]gammacapture.HorizonConditionedUtilityCandidate, 0, len(qRatios))
	enhancedCandidates := make([]gammacapture.HorizonConditionedUtilityCandidate, 0, len(qRatios))
	for _, ratio := range qRatios {
		q := input.QuoteNotionalJPY * ratio
		conversion := q / 10_000
		legacyCandidates = append(legacyCandidates, gammacapture.HorizonConditionedUtilityCandidate{
			NotionalJPY: q, FillProbability: snapshot.FillProbability,
			FillProbabilityStdError:      snapshot.FillProbabilityStdErr,
			ConditionalExecutionValueJPY: snapshot.ExecutionMeanBps * conversion,
			ConditionalMeanStdErrorJPY:   math.Sqrt(snapshot.ExecutionVarianceBps2/math.Max(1, snapshot.EffectiveSamples)) * conversion,
			BaselineVarianceJPY2:         0, AfterFillVarianceJPY2: snapshot.ExecutionVarianceBps2 * conversion * conversion,
			EffectiveSamples: snapshot.EffectiveSamples, PairEquityJPY: input.PairEquityJPY,
			RiskAversion: riskAversion, ConfidenceZ: confidenceZ,
		})
		enhancedCandidates = append(enhancedCandidates, gammacapture.HorizonConditionedUtilityCandidate{
			NotionalJPY: q, FillProbability: snapshot.FillProbability,
			FillProbabilityStdError:         snapshot.FillProbabilityStdErr,
			ConditionalExecutionValueJPY:    snapshot.ExecutionMeanBps * conversion,
			ConditionalContinuationValueJPY: snapshot.ContinuationMeanBps * conversion,
			ConditionalMeanStdErrorJPY:      math.Sqrt(snapshot.TotalVarianceBps2/math.Max(1, snapshot.EffectiveSamples)) * conversion,
			BaselineVarianceJPY2:            0, AfterFillVarianceJPY2: snapshot.TotalVarianceBps2 * conversion * conversion,
			EffectiveSamples: snapshot.EffectiveSamples, PairEquityJPY: input.PairEquityJPY,
			RiskAversion: riskAversion, ConfidenceZ: confidenceZ,
		})
	}
	legacy := gammacapture.SelectHorizonConditionedUtility(legacyCandidates)
	enhanced := gammacapture.SelectHorizonConditionedUtility(enhancedCandidates)
	return horizonUtilityStudyRow{
		At: start.time, Block: int(start.time.Sub(input.From) / (6 * time.Hour)), Direction: direction,
		PublicFillProbability: snapshot.FillProbability, Touched: touched,
		ShortOutcomeBps: shortOutcome, ContinuationBps: continuation, TotalOutcomeBps: shortOutcome + continuation,
		ContinuationKnown: pivotKnown, EffectiveSamples: snapshot.EffectiveSamples,
		ContinuationForecastBps: snapshot.ContinuationMeanBps,
		Legacy:                  legacy, Enhanced: enhanced,
	}
}

func summarizeHorizonUtilityRows(rows []horizonUtilityStudyRow, from, to time.Time) horizonConditionedUtilitySplitReport {
	report := horizonConditionedUtilitySplitReport{From: from, To: to}
	legacyBlocks := make(map[int][]float64)
	enhancedBlocks := make(map[int][]float64)
	for _, row := range rows {
		if row.At.Before(from) || !row.At.Before(to) {
			continue
		}
		if !row.ContinuationKnown {
			continue
		}
		report.Anchors++
		report.PivotReady++
		if !row.Legacy.Evaluated || !row.Enhanced.Evaluated {
			continue
		}
		report.Scored++
		report.FillProbabilityMean += row.PublicFillProbability
		report.EffectiveSamplesMean += row.EffectiveSamples
		report.ContinuationForecastMeanBps += row.ContinuationForecastBps
		report.LegacyCEBpsMean += row.Legacy.CertaintyEquivalentJPY / row.Legacy.NotionalJPY * 10_000
		report.ContinuationCEBpsMean += row.Enhanced.CertaintyEquivalentJPY / row.Enhanced.NotionalJPY * 10_000
		brier := row.PublicFillProbability
		if row.Touched {
			brier -= 1
		}
		report.FillBrier += brier * brier
		report.FillBrierSamples++
		if row.Legacy.Approved && row.Touched {
			report.LegacyFilled++
		}
		if row.Enhanced.Approved && row.Touched {
			report.ContinuationFilled++
		}
		if row.Legacy.Approved {
			report.LegacyAccepted++
			outcome := row.ShortOutcomeBps
			if row.Touched {
				report.LegacyRealizedBpsMean += outcome
			}
			legacyBlocks[row.Block] = append(legacyBlocks[row.Block], outcome)
		} else {
			legacyBlocks[row.Block] = append(legacyBlocks[row.Block], 0)
		}
		if row.Enhanced.Approved {
			report.ContinuationAccepted++
			outcome := row.TotalOutcomeBps
			if row.Touched {
				report.ContinuationRealizedBpsMean += outcome
			}
			enhancedBlocks[row.Block] = append(enhancedBlocks[row.Block], outcome)
		} else {
			enhancedBlocks[row.Block] = append(enhancedBlocks[row.Block], 0)
		}
	}
	if report.Scored > 0 {
		report.FillProbabilityMean /= float64(report.Scored)
		report.EffectiveSamplesMean /= float64(report.Scored)
		report.ContinuationForecastMeanBps /= float64(report.Scored)
		report.LegacyCEBpsMean /= float64(report.Scored)
		report.ContinuationCEBpsMean /= float64(report.Scored)
		report.LegacyRealizedBpsMean /= float64(report.Scored)
		report.ContinuationRealizedBpsMean /= float64(report.Scored)
		// The realized means are already fee/adverse-selection net and are
		// measured against the no-order baseline. Keep the paired fields
		// explicit so a gate cannot accidentally treat the CE forecast as
		// realized incremental value.
		report.LegacyIncrementalVsBaselineBps = report.LegacyRealizedBpsMean
		report.ContinuationIncrementalBps = report.ContinuationRealizedBpsMean -
			report.LegacyRealizedBpsMean
	}
	if report.FillBrierSamples > 0 {
		report.FillBrier /= float64(report.FillBrierSamples)
	}
	report.EffectiveBlocks = len(legacyBlocks)
	report.LegacyPositiveBlocks, report.ContinuationPositiveBlocks = 0, 0
	legacyBlockMeans, enhancedBlockMeans := make([]float64, 0, len(legacyBlocks)), make([]float64, 0, len(enhancedBlocks))
	for block, values := range legacyBlocks {
		mean := meanFloat64(values)
		legacyBlockMeans = append(legacyBlockMeans, mean)
		if mean > 0 {
			report.LegacyPositiveBlocks++
		}
		if values := enhancedBlocks[block]; len(values) > 0 {
			mean = meanFloat64(values)
			enhancedBlockMeans = append(enhancedBlockMeans, mean)
			if mean > 0 {
				report.ContinuationPositiveBlocks++
			}
		}
	}
	report.LegacyBlockMeanSEBps = sampleMeanSE(legacyBlockMeans)
	report.ContinuationBlockMeanSEBps = sampleMeanSE(enhancedBlockMeans)
	return report
}

func horizonUtilitySideIndex(direction int) int {
	if direction < 0 {
		return 1
	}
	return 0
}

// horizonUtilityPhase is a predeclared pivot-leg phase: early, middle, or
// late relative to the causal same-direction expected leg amplitude. Sparse
// cells fall back to the pooled side posterior in scoreHorizonUtilityAnchors.
func horizonUtilityPhase(decision gammacapture.PivotRegimeDecision) int {
	if decision.ExpectedLegAmplitudeBps <= 0 || decision.LegAmplitudeBps <= 0 {
		return 0
	}
	ratio := decision.LegAmplitudeBps / decision.ExpectedLegAmplitudeBps
	switch {
	case ratio >= 0.67:
		return 2
	case ratio >= 0.33:
		return 1
	default:
		return 0
	}
}

func clampHorizonUtilityProbability(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0.5
	}
	return math.Max(0, math.Min(1, value))
}

func firstBookAtOrAfter(books []bboSnapshot, at time.Time) int {
	return sort.Search(len(books), func(i int) bool { return !books[i].time.Before(at) })
}

func nextHorizonUtilityPivotLabel(
	books []bboSnapshot,
	events []horizonUtilityPivotEvent,
	anchorAt time.Time,
	direction int,
	quote, startBid, startAsk, entryCostBps float64,
	maxHorizon time.Duration,
) (pivotAt, maturesAt time.Time, outcomeBps float64, ok bool) {
	for _, event := range events {
		if event.Direction != direction || !event.At.After(anchorAt) ||
			!event.PivotAt.After(anchorAt) ||
			(maxHorizon > 0 && event.At.After(anchorAt.Add(maxHorizon))) {
			continue
		}
		index := firstBookAtOrAfter(books, event.PivotAt)
		if index < 0 || index >= len(books) {
			return time.Time{}, time.Time{}, 0, false
		}
		book := books[index]
		outcome := directionalHorizonBps(startBid, startAsk, quote, book.bid, book.ask, direction, entryCostBps)
		return event.PivotAt, event.At, outcome, true
	}
	return time.Time{}, time.Time{}, 0, false
}

func publicTouchBetween(books []bboSnapshot, start, end int, quote float64, direction int) bool {
	if start < 0 || end <= start || end >= len(books) || quote <= 0 {
		return false
	}
	for i := start + 1; i <= end; i++ {
		if direction > 0 && books[i].ask <= quote {
			return true
		}
		if direction < 0 && books[i].bid >= quote {
			return true
		}
	}
	return false
}

func directionalHorizonBps(startBid, startAsk, entry, endBid, endAsk float64, direction int, costBps float64) float64 {
	if entry <= 0 || endBid <= 0 || endAsk <= 0 || endAsk < endBid {
		return 0
	}
	if direction > 0 && startAsk > 0 {
		return math.Log(endBid/entry)*10_000 - costBps
	}
	if direction < 0 && startBid > 0 {
		return math.Log(entry/endAsk)*10_000 - costBps
	}
	return 0
}
