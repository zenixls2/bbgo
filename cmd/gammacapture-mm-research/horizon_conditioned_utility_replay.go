package main

import (
	"math"
	"time"

	"github.com/c9s/bbgo/pkg/strategy/gammacapture"
)

// horizonConditionedUtilityReplaySizer is a replay-only adapter for the
// standalone horizon-conditioned utility. It observes the same BBO stream as
// the production simulator, but its labels use a public first-passage touch
// of the base quote plan. This keeps the paired PnL replay deterministic while
// making the missing private queue calibration explicit in the result.
type horizonConditionedUtilityReplaySizer struct {
	shortHorizon        time.Duration
	continuationHorizon time.Duration
	anchorStep          time.Duration
	halfLife            time.Duration
	entryCostBps        float64
	confidenceZ         float64
	riskAversion        float64

	filter       *gammacapture.PivotRegimeFilter
	models       [2]*horizonUtilitySideEstimator
	phaseModels  [2][3]*horizonUtilitySideEstimator
	lastDecision gammacapture.PivotRegimeDecision
	lastAnchorAt time.Time
	books        map[int64]bboSnapshot
	bookTimes    []int64
	bookHead     int
	pending      []*horizonConditionedUtilityReplayPending

	evaluations        int
	ready              int
	positive           int
	zeroScale          int
	anchors            int
	shortLabels        int
	continuationLabels int
	touchedLabels      int
	scaleSum           float64
	effectiveSum       float64
	fillProbabilitySum float64
}

type horizonConditionedUtilityReplayPending struct {
	at, shortAt, pivotMaturesAt time.Time
	direction                   int
	phase                       int
	quote                       float64
	startBid, startAsk          float64
	shortBid, shortAsk          float64
	pivotOutcomeBps             float64
	touched                     bool
	shortUpdated                bool
	pivotUpdated                bool
	executionBps                float64
}

type horizonConditionedUtilityReplayDecision struct {
	Evaluated bool
	Ready     bool
	Positive  bool
	BuyScale  float64
	SellScale float64
}

func newHorizonConditionedUtilityReplaySizer(cfg gammacapture.MarketMakerConfig) *horizonConditionedUtilityReplaySizer {
	shortHorizon := 15 * time.Minute
	continuationHorizon := 6 * time.Hour
	anchorStep := 15 * time.Minute
	halfLife := time.Duration(cfg.HorizonLookback)
	if halfLife <= 0 {
		halfLife = 6 * time.Hour
	}
	entryCost := math.Max(0, cfg.MakerFeeBps+cfg.AdverseSelectionBps)
	confidenceZ := cfg.InventoryRiskZScore
	if confidenceZ < 0 || math.IsNaN(confidenceZ) || math.IsInf(confidenceZ, 0) {
		confidenceZ = 0
	}
	riskAversion := cfg.FastRiskAversion
	if riskAversion <= 0 || math.IsNaN(riskAversion) || math.IsInf(riskAversion, 0) {
		riskAversion = 1
	}
	s := &horizonConditionedUtilityReplaySizer{
		shortHorizon: shortHorizon, continuationHorizon: continuationHorizon,
		anchorStep: anchorStep, halfLife: halfLife, entryCostBps: entryCost,
		confidenceZ: confidenceZ, riskAversion: riskAversion,
		filter: gammacapture.NewPivotRegimeFilter(gammacapture.PivotRegimeConfig{
			ReversalBps: 26, MaxGap: 15 * time.Minute, MinLegSamples: 2, PriorLegSamples: 2,
		}),
		books: make(map[int64]bboSnapshot),
	}
	for side := range s.models {
		s.models[side] = &horizonUtilitySideEstimator{HalfLife: halfLife}
		for phase := range s.phaseModels[side] {
			s.phaseModels[side][phase] = &horizonUtilitySideEstimator{HalfLife: halfLife}
		}
	}
	return s
}

func (s *horizonConditionedUtilityReplaySizer) observeBook(book bboSnapshot, gap bool) {
	if s == nil {
		return
	}
	if gap {
		s.filter.Reset()
		s.lastDecision = gammacapture.PivotRegimeDecision{}
		s.pending = nil
		s.books = make(map[int64]bboSnapshot)
		s.bookTimes = nil
		s.bookHead = 0
	}
	bookKey := book.time.UnixNano()
	if _, exists := s.books[bookKey]; !exists {
		s.bookTimes = append(s.bookTimes, bookKey)
	}
	s.books[bookKey] = book
	// Pivot confirmation is capped at six hours, so BBOs older than the
	// continuation lease cannot be needed by any still-pending shadow anchor.
	// Keep the lookup O(1) while bounding replay memory instead of retaining
	// every sampled BBO for the entire archive.
	cutoff := book.time.Add(-(s.continuationHorizon + s.shortHorizon)).UnixNano()
	for s.bookHead < len(s.bookTimes) && s.bookTimes[s.bookHead] < cutoff {
		delete(s.books, s.bookTimes[s.bookHead])
		s.bookHead++
	}
	if s.bookHead >= 1024 && s.bookHead*2 >= len(s.bookTimes) {
		copy(s.bookTimes, s.bookTimes[s.bookHead:])
		s.bookTimes = s.bookTimes[:len(s.bookTimes)-s.bookHead]
		s.bookHead = 0
	}
	for _, item := range s.pending {
		if item.shortUpdated || book.time.After(item.shortAt) {
			continue
		}
		if item.direction > 0 && book.ask <= item.quote {
			item.touched = true
		}
		if item.direction < 0 && book.bid >= item.quote {
			item.touched = true
		}
	}

	decision := s.filter.Observe(gammacapture.PivotRegimeInput{
		At: book.time, ReferencePrice: book.midPrice(),
	})
	s.lastDecision = decision
	if decision.PivotChanged {
		s.assignPivot(decision.LastPivot, book.time)
	}
	s.mature(book)
}

func (s *horizonConditionedUtilityReplaySizer) assignPivot(event gammacapture.PivotRegimeEvent, confirmedAt time.Time) {
	if s == nil || event.Direction == 0 {
		return
	}
	pivotBook, ok := s.books[event.PivotAt.UnixNano()]
	if !ok {
		return
	}
	for _, item := range s.pending {
		if item.direction != event.Direction || !event.At.After(item.at) ||
			!event.PivotAt.After(item.at) ||
			!item.pivotMaturesAt.IsZero() ||
			confirmedAt.After(item.at.Add(s.continuationHorizon)) ||
			confirmedAt.Before(item.shortAt) {
			continue
		}
		item.pivotMaturesAt = confirmedAt
		item.pivotOutcomeBps = directionalHorizonBps(
			item.startBid, item.startAsk, item.quote,
			pivotBook.bid, pivotBook.ask, item.direction, s.entryCostBps)
	}
}

func (s *horizonConditionedUtilityReplaySizer) mature(book bboSnapshot) {
	if s == nil {
		return
	}
	remaining := s.pending[:0]
	for _, item := range s.pending {
		if !item.shortUpdated && !item.shortAt.After(book.time) {
			item.shortBid, item.shortAsk = book.bid, book.ask
			item.shortUpdated = true
			s.shortLabels++
			side := horizonUtilitySideIndex(item.direction)
			s.models[side].observeExposure(item.shortAt, item.touched)
			s.phaseModels[side][item.phase].observeExposure(item.shortAt, item.touched)
			if item.touched {
				item.executionBps = directionalHorizonBps(
					item.startBid, item.startAsk, item.quote,
					item.shortBid, item.shortAsk, item.direction, s.entryCostBps)
				s.models[side].observeExecution(item.shortAt, item.executionBps)
				s.phaseModels[side][item.phase].observeExecution(item.shortAt, item.executionBps)
				s.touchedLabels++
			}
		}
		if item.shortUpdated && !item.pivotUpdated &&
			!item.pivotMaturesAt.IsZero() && !item.pivotMaturesAt.After(book.time) {
			if item.touched {
				continuation := item.pivotOutcomeBps - item.executionBps
				side := horizonUtilitySideIndex(item.direction)
				s.models[side].observeContinuation(item.pivotMaturesAt, item.executionBps, continuation)
				s.phaseModels[side][item.phase].observeContinuation(item.pivotMaturesAt, item.executionBps, continuation)
				s.continuationLabels++
			}
			item.pivotUpdated = true
		}
		if item.pivotUpdated || book.time.After(item.at.Add(s.continuationHorizon)) {
			continue
		}
		remaining = append(remaining, item)
	}
	s.pending = remaining
}

// predictAndRecord applies only to the side aligned with the currently active
// causal pivot. The shadow anchor uses the unscaled base quote plan so the
// public-touch estimator does not learn from its own quantity decision.
func (s *horizonConditionedUtilityReplaySizer) predictAndRecord(
	book bboSnapshot,
	basePlan gammacapture.MarketMakerQuotePlan,
	baseProjection gammacapture.ProbabilityCenteredQuoteDecision,
	pairEquity, riskAversion float64,
) horizonConditionedUtilityReplayDecision {
	decision := horizonConditionedUtilityReplayDecision{}
	if s == nil || !s.lastDecision.Ready || s.lastDecision.Direction == 0 {
		return decision
	}
	if s.lastAnchorAt.IsZero() || !book.time.Before(s.lastAnchorAt.Add(s.anchorStep)) {
		if s.recordShadowAnchor(book, basePlan) {
			s.lastAnchorAt = book.time
			s.anchors++
		}
	}
	phase := horizonUtilityPhase(s.lastDecision)
	side := horizonUtilitySideIndex(s.lastDecision.Direction)
	snapshot := s.phaseModels[side][phase].snapshot(book.time)
	if !snapshot.Ready {
		snapshot = s.models[side].snapshot(book.time)
	}
	if !snapshot.Ready {
		return decision
	}
	decision.Evaluated, decision.Ready = true, true
	s.evaluations++
	s.ready++
	s.effectiveSum += snapshot.EffectiveSamples
	s.fillProbabilitySum += snapshot.FillProbability
	scale, approved := horizonUtilityReplayScale(
		snapshot, baseProjection, basePlan, pairEquity,
		riskAversion, s.confidenceZ, s.lastDecision.Direction)
	if s.lastDecision.Direction > 0 {
		decision.BuyScale = scale
		decision.SellScale = 1
	} else {
		decision.BuyScale = 1
		decision.SellScale = scale
	}
	if approved {
		decision.Positive = true
		s.positive++
	} else {
		s.zeroScale++
	}
	s.scaleSum += scale
	return decision
}

func (s *horizonConditionedUtilityReplaySizer) recordShadowAnchor(book bboSnapshot, plan gammacapture.MarketMakerQuotePlan) bool {
	direction := s.lastDecision.Direction
	if direction == 0 || !s.lastDecision.Ready {
		return false
	}
	quote := plan.BidPrice
	if direction < 0 {
		quote = plan.AskPrice
	}
	if quote <= 0 {
		return false
	}
	s.pending = append(s.pending, &horizonConditionedUtilityReplayPending{
		at: book.time, shortAt: book.time.Add(s.shortHorizon), direction: direction,
		phase: horizonUtilityPhase(s.lastDecision), quote: quote,
		startBid: book.bid, startAsk: book.ask,
	})
	return true
}

func horizonUtilityReplayScale(
	snapshot horizonUtilitySideSnapshot,
	projection gammacapture.ProbabilityCenteredQuoteDecision,
	plan gammacapture.MarketMakerQuotePlan,
	pairEquity, riskAversion, confidenceZ float64,
	direction int,
) (float64, bool) {
	if pairEquity <= 0 || snapshot.EffectiveSamples <= 0 ||
		!projection.Enabled || direction == 0 {
		return 0, false
	}
	base := projection.BuyNotionalJPY
	if direction < 0 {
		base = projection.SellNotionalJPY
	}
	if base <= 0 {
		return 0, false
	}
	if direction > 0 && (!plan.AllowBid || plan.BidPrice <= 0) {
		return 0, false
	}
	if direction < 0 && (!plan.AllowAsk || plan.AskPrice <= 0) {
		return 0, false
	}
	best := gammacapture.HorizonConditionedUtilityDecision{}
	for _, ratio := range []float64{0.25, 0.5, 0.75, 1} {
		q := base * ratio
		conversion := q / 10_000
		candidate := gammacapture.HorizonConditionedUtilityCandidate{
			NotionalJPY: q, FillProbability: snapshot.FillProbability,
			FillProbabilityStdError:         snapshot.FillProbabilityStdErr,
			ConditionalExecutionValueJPY:    snapshot.ExecutionMeanBps * conversion,
			ConditionalContinuationValueJPY: snapshot.ContinuationMeanBps * conversion,
			ConditionalMeanStdErrorJPY:      math.Sqrt(snapshot.TotalVarianceBps2/math.Max(1, snapshot.EffectiveSamples)) * conversion,
			BaselineVarianceJPY2:            0, AfterFillVarianceJPY2: snapshot.TotalVarianceBps2 * conversion * conversion,
			EffectiveSamples: snapshot.EffectiveSamples, PairEquityJPY: pairEquity,
			RiskAversion: riskAversion, ConfidenceZ: confidenceZ,
		}
		candidateDecision := gammacapture.EvaluateHorizonConditionedUtility(candidate)
		if !candidateDecision.Evaluated ||
			!best.Evaluated || candidateDecision.CertaintyEquivalentJPY > best.CertaintyEquivalentJPY {
			best = candidateDecision
		}
	}
	if !best.Evaluated || !best.Approved {
		return 0, false
	}
	return math.Max(0, math.Min(1, best.NotionalJPY/base)), true
}

func (s *horizonConditionedUtilityReplaySizer) metrics() horizonConditionedUtilityReplayMetrics {
	if s == nil {
		return horizonConditionedUtilityReplayMetrics{}
	}
	m := horizonConditionedUtilityReplayMetrics{
		Evaluations: s.evaluations, Ready: s.ready, Positive: s.positive,
		ZeroScale: s.zeroScale, Anchors: s.anchors, ShortLabels: s.shortLabels,
		ContinuationLabels: s.continuationLabels, TouchedLabels: s.touchedLabels,
	}
	if s.evaluations > 0 {
		m.MeanScale = s.scaleSum / float64(s.evaluations)
		m.MeanEffectiveSamples = s.effectiveSum / float64(s.evaluations)
		m.MeanFillProbability = s.fillProbabilitySum / float64(s.evaluations)
	}
	return m
}

type horizonConditionedUtilityReplayMetrics struct {
	Evaluations, Ready, Positive, ZeroScale                 int
	Anchors, ShortLabels, ContinuationLabels, TouchedLabels int
	MeanScale, MeanEffectiveSamples, MeanFillProbability    float64
}
