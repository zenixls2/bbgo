package gammacapture

import (
	"math"
	"time"
)

// QuoteLifecycleActionConfig controls the lifecycle Bellman layer. It is
// disabled by default until a pair-cycle replay passes the component gate.
// ShadowOnly keeps the production quote path unchanged while exposing the
// action value in diagnostics.
type QuoteLifecycleActionConfig struct {
	Enabled                         bool                       `json:"enabled" yaml:"enabled"`
	ShadowOnly                      bool                       `json:"shadowOnly" yaml:"shadowOnly"`
	DiscountFactor                  float64                    `json:"discountFactor" yaml:"discountFactor"`
	ReplacementCostBps              float64                    `json:"replacementCostBps" yaml:"replacementCostBps"`
	Hazard                          QuoteLifecycleHazardConfig `json:"hazard" yaml:"hazard"`
	DynamicReplacementCost          bool                       `json:"dynamicReplacementCost" yaml:"dynamicReplacementCost"`
	ReplacementCostQueueWeight      float64                    `json:"replacementCostQueueWeight" yaml:"replacementCostQueueWeight"`
	ReplacementCostDriftWeight      float64                    `json:"replacementCostDriftWeight" yaml:"replacementCostDriftWeight"`
	ReplacementCostVolatilityWeight float64                    `json:"replacementCostVolatilityWeight" yaml:"replacementCostVolatilityWeight"`
	ReplacementCostLatencySeconds   float64                    `json:"replacementCostLatencySeconds" yaml:"replacementCostLatencySeconds"`
	ActionMarginBps                 float64                    `json:"actionMarginBps" yaml:"actionMarginBps"`
	ConfidenceZScore                float64                    `json:"confidenceZScore" yaml:"confidenceZScore"`
}

func (c *QuoteLifecycleActionConfig) setDefaults() {
	if c.DiscountFactor <= 0 || c.DiscountFactor > 1 || math.IsNaN(c.DiscountFactor) || math.IsInf(c.DiscountFactor, 0) {
		c.DiscountFactor = 1
	}
	if c.ReplacementCostBps < 0 || math.IsNaN(c.ReplacementCostBps) || math.IsInf(c.ReplacementCostBps, 0) {
		c.ReplacementCostBps = 0
	}
	c.Hazard.setDefaults()
	if c.ReplacementCostQueueWeight < 0 || !finiteLifecycleValue(c.ReplacementCostQueueWeight) {
		c.ReplacementCostQueueWeight = 0
	}
	if c.ReplacementCostDriftWeight < 0 || !finiteLifecycleValue(c.ReplacementCostDriftWeight) {
		c.ReplacementCostDriftWeight = 0
	}
	if c.ReplacementCostVolatilityWeight < 0 || !finiteLifecycleValue(c.ReplacementCostVolatilityWeight) {
		c.ReplacementCostVolatilityWeight = 0
	}
	if c.ReplacementCostLatencySeconds < 0 || !finiteLifecycleValue(c.ReplacementCostLatencySeconds) {
		c.ReplacementCostLatencySeconds = 0
	}
	if c.ActionMarginBps < 0 || !finiteLifecycleValue(c.ActionMarginBps) {
		c.ActionMarginBps = 0
	}
	if c.ConfidenceZScore <= 0 || !finiteLifecycleValue(c.ConfidenceZScore) {
		c.ConfidenceZScore = 1.645
	}
}

type QuoteLifecycleReplacementCostInput struct {
	BaseCostBps               float64
	CurrentFillProbability    float64
	CurrentAge                time.Duration
	Horizon                   time.Duration
	CurrentTerminalMarkoutBps float64
	BBOStalenessBps           float64
	VolatilityBpsPerSqrtSec   float64
}

type QuoteLifecycleReplacementCostEstimate struct {
	CostBps     float64
	StdErrorBps float64
}

// EstimateQuoteLifecycleReplacementCostBps prices queue loss, observed BBO
// drift, and latency volatility at the review boundary. It is intentionally
// an additive opportunity cost, never a second maker-fee charge.
func EstimateQuoteLifecycleReplacementCostBps(in QuoteLifecycleReplacementCostInput, config QuoteLifecycleActionConfig) QuoteLifecycleReplacementCostEstimate {
	config.setDefaults()
	estimate := QuoteLifecycleReplacementCostEstimate{CostBps: math.Max(0, in.BaseCostBps)}
	if !config.DynamicReplacementCost {
		return estimate
	}
	p := math.Min(1, math.Max(0, in.CurrentFillProbability))
	ageFraction := 0.0
	if in.Horizon > 0 && in.CurrentAge > 0 {
		ageFraction = math.Min(1, float64(in.CurrentAge)/float64(in.Horizon))
	}
	queueLoss := p * ageFraction * math.Max(0, in.CurrentTerminalMarkoutBps)
	drift := math.Abs(in.BBOStalenessBps)
	volatilityLoss := 0.0
	if config.ReplacementCostLatencySeconds > 0 && in.VolatilityBpsPerSqrtSec > 0 {
		volatilityLoss = in.VolatilityBpsPerSqrtSec * math.Sqrt(config.ReplacementCostLatencySeconds)
	}
	estimate.CostBps += config.ReplacementCostQueueWeight*queueLoss + config.ReplacementCostDriftWeight*drift + config.ReplacementCostVolatilityWeight*volatilityLoss
	estimate.StdErrorBps = 0.5 * (config.ReplacementCostDriftWeight*drift + config.ReplacementCostVolatilityWeight*volatilityLoss)
	return estimate
}

// QuoteLifecycleAction is the decision at a review boundary for an already
// resting passive quote.  KEEP preserves queue age, REPLACE cancels the old
// quote and starts a new quote window, and CANCEL leaves no passive quote.
type QuoteLifecycleAction string

const (
	QuoteLifecycleKeep    QuoteLifecycleAction = "KEEP"
	QuoteLifecycleReplace QuoteLifecycleAction = "REPLACE"
	QuoteLifecycleCancel  QuoteLifecycleAction = "CANCEL"
)

// QuoteLifecycleActionInput is a one-step Bellman state.  Markout values are
// conditional on a fill and are already expressed against the correct
// executable terminal side.  Continuation values are the value of revisiting
// the quote state after this window has elapsed when no fill occurs.
//
// ReplacementCostBps is queue-age/opportunity loss from cancel/replace.  It
// must not contain maker fees: a maker fee is charged only in the conditional
// fill term.  CancelImmediateValueBps can represent an inventory-risk benefit
// of removing a quote; it is not forced to zero by this component.
type QuoteLifecycleActionInput struct {
	CurrentActive   bool
	CandidateActive bool

	CurrentFillProbability         float64
	CurrentFillProbabilityStdError float64
	CurrentTerminalMarkoutBps      float64
	CurrentExecutionCostBps        float64
	CurrentRiskPenaltyBps          float64
	CurrentContinuationValueBps    float64

	CandidateFillProbability         float64
	CandidateFillProbabilityStdError float64
	CandidateTerminalMarkoutBps      float64
	CandidateExecutionCostBps        float64
	CandidateRiskPenaltyBps          float64
	CandidateContinuationValueBps    float64

	ReplacementCostBps         float64
	ReplacementCostStdErrorBps float64
	CancelImmediateValueBps    float64
	CancelContinuationValueBps float64
	DiscountFactor             float64
	ActionMarginBps            float64
	ConfidenceZScore           float64
}

type QuoteLifecycleActionValue struct {
	Evaluated bool
	Action    QuoteLifecycleAction
	Reason    string

	KeepValueBps         float64
	ReplaceValueBps      float64
	CancelValueBps       float64
	SelectedValueBps     float64
	IncrementalVsKeepBps float64
	KeepStdErrorBps      float64
	ReplaceStdErrorBps   float64
	CancelStdErrorBps    float64
	KeepLowerBps         float64
	ReplaceLowerBps      float64
	CancelLowerBps       float64
	ActionMarginBps      float64
	ConfidenceZScore     float64
}

// QuoteLifecycleActionInputFromHorizon converts a completed paired horizon
// decision into a fee-net lifecycle input. The horizon score already includes
// the path's fee/adverse-selection admission terms, so execution and risk
// costs are zero here; charging them again would double count costs.
func QuoteLifecycleActionInputFromHorizon(
	decision MarketMakerHorizonDecision,
	active bool,
	config QuoteLifecycleActionConfig,
) QuoteLifecycleActionInput {
	config.setDefaults()
	terminalEdge := decision.NetRoundTripEdgeBps
	if !finiteLifecycleValue(terminalEdge) {
		terminalEdge = 0
	}
	continuation := 0.0
	if decision.Horizon > 0 && finiteLifecycleValue(decision.ScoreBpsPerHour) {
		continuation = math.Max(0, decision.ScoreBpsPerHour*decision.Horizon.Hours())
	}
	probability := math.Max(0, math.Min(1, decision.BothTouchProbability))
	return QuoteLifecycleActionInput{
		CurrentActive: active, CandidateActive: active,
		CurrentFillProbability: probability, CurrentTerminalMarkoutBps: terminalEdge,
		CurrentFillProbabilityStdError: decision.BothTouchStdError,
		CurrentContinuationValueBps:    continuation,
		CandidateFillProbability:       probability, CandidateTerminalMarkoutBps: terminalEdge,
		CandidateFillProbabilityStdError: decision.BothTouchStdError,
		CandidateContinuationValueBps:    continuation,
		ReplacementCostBps:               config.ReplacementCostBps,
		DiscountFactor:                   config.DiscountFactor, ActionMarginBps: config.ActionMarginBps,
		ConfidenceZScore: config.ConfidenceZScore,
	}
}

// EvaluateQuoteLifecycleFromHorizons compares the currently resting paired
// quote with the newly computed paired candidate.  NetRoundTripEdgeBps is the
// conditional completed-cycle payoff; ScoreBpsPerHour contributes only to the
// no-fill continuation term.  This separation avoids treating a fill-rate
// score as if it were an executable terminal price.
func EvaluateQuoteLifecycleFromHorizons(
	current, candidate MarketMakerHorizonDecision,
	currentActive, candidateActive bool,
	config QuoteLifecycleActionConfig,
) QuoteLifecycleActionValue {
	config.setDefaults()
	currentInput := QuoteLifecycleActionInputFromHorizon(current, currentActive, config)
	candidateInput := QuoteLifecycleActionInputFromHorizon(candidate, candidateActive, config)
	input := QuoteLifecycleActionInput{
		CurrentActive:                    currentInput.CurrentActive,
		CurrentFillProbability:           currentInput.CurrentFillProbability,
		CurrentFillProbabilityStdError:   currentInput.CurrentFillProbabilityStdError,
		CurrentTerminalMarkoutBps:        currentInput.CurrentTerminalMarkoutBps,
		CurrentExecutionCostBps:          currentInput.CurrentExecutionCostBps,
		CurrentRiskPenaltyBps:            currentInput.CurrentRiskPenaltyBps,
		CurrentContinuationValueBps:      currentInput.CurrentContinuationValueBps,
		CandidateActive:                  candidateInput.CandidateActive,
		CandidateFillProbability:         candidateInput.CurrentFillProbability,
		CandidateFillProbabilityStdError: candidateInput.CurrentFillProbabilityStdError,
		CandidateTerminalMarkoutBps:      candidateInput.CurrentTerminalMarkoutBps,
		CandidateExecutionCostBps:        candidateInput.CurrentExecutionCostBps,
		CandidateRiskPenaltyBps:          candidateInput.CurrentRiskPenaltyBps,
		CandidateContinuationValueBps:    candidateInput.CurrentContinuationValueBps,
		ReplacementCostBps:               config.ReplacementCostBps,
		DiscountFactor:                   config.DiscountFactor,
		ActionMarginBps:                  config.ActionMarginBps,
		ConfidenceZScore:                 config.ConfidenceZScore,
	}
	return EvaluateQuoteLifecycleAction(input)
}

// EvaluateQuoteLifecycleFromHorizonsWithEstimates is used when an
// age-conditioned hazard probability and dynamic replacement-cost estimate
// are available. The legacy helper above remains the point-estimate path.
func EvaluateQuoteLifecycleFromHorizonsWithEstimates(
	current, candidate MarketMakerHorizonDecision,
	currentActive, candidateActive bool,
	config QuoteLifecycleActionConfig,
	currentProbability, currentProbabilitySE, candidateProbability, candidateProbabilitySE,
	replacementCostBps, replacementCostSE float64,
) QuoteLifecycleActionValue {
	config.setDefaults()
	ci := QuoteLifecycleActionInputFromHorizon(current, currentActive, config)
	ni := QuoteLifecycleActionInputFromHorizon(candidate, candidateActive, config)
	return EvaluateQuoteLifecycleAction(QuoteLifecycleActionInput{
		CurrentActive: ci.CurrentActive, CurrentFillProbability: currentProbability,
		CurrentFillProbabilityStdError: currentProbabilitySE,
		CurrentTerminalMarkoutBps:      ci.CurrentTerminalMarkoutBps,
		CurrentExecutionCostBps:        ci.CurrentExecutionCostBps, CurrentRiskPenaltyBps: ci.CurrentRiskPenaltyBps,
		CurrentContinuationValueBps: ci.CurrentContinuationValueBps,
		CandidateActive:             ni.CandidateActive, CandidateFillProbability: candidateProbability,
		CandidateFillProbabilityStdError: candidateProbabilitySE,
		CandidateTerminalMarkoutBps:      ni.CurrentTerminalMarkoutBps,
		CandidateExecutionCostBps:        ni.CurrentExecutionCostBps, CandidateRiskPenaltyBps: ni.CurrentRiskPenaltyBps,
		CandidateContinuationValueBps: ni.CurrentContinuationValueBps,
		ReplacementCostBps:            replacementCostBps, ReplacementCostStdErrorBps: replacementCostSE,
		DiscountFactor: config.DiscountFactor, ActionMarginBps: config.ActionMarginBps,
		ConfidenceZScore: config.ConfidenceZScore,
	})
}

func finiteLifecycleValue(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func validLifecycleProbability(value float64) bool {
	return finiteLifecycleValue(value) && value >= 0 && value <= 1
}

// lifecycleActionValue is the one-step Bellman expansion
//
//	Q(a) = p_a (markout_a - executionCost_a - riskPenalty_a)
//	       + (1-p_a) gamma V_{next,a} - replacementCost_a.
//
// The terminal markout is conditional on a fill.  The continuation term is
// conditional on no fill, so a future window is not counted twice as an
// immediate trade return.
func lifecycleActionValue(
	fillProbability, terminalMarkoutBps, executionCostBps, riskPenaltyBps,
	continuationValueBps, discountFactor, replacementCostBps float64,
) float64 {
	return fillProbability*(terminalMarkoutBps-executionCostBps-riskPenaltyBps) +
		(1-fillProbability)*discountFactor*continuationValueBps - replacementCostBps
}

// EvaluateQuoteLifecycleAction chooses the highest expected action value.
// Ties prefer KEEP, then REPLACE, then CANCEL, which is the queue-preserving
// ordering.  The evaluator is deliberately independent of order submission,
// balances, and strategy state; callers must provide only causal posterior
// estimates and the executable feasible set.
func EvaluateQuoteLifecycleAction(in QuoteLifecycleActionInput) QuoteLifecycleActionValue {
	d := QuoteLifecycleActionValue{Action: QuoteLifecycleCancel, Reason: "invalid input"}
	values := []float64{
		in.CurrentFillProbability, in.CurrentTerminalMarkoutBps,
		in.CurrentFillProbabilityStdError,
		in.CurrentExecutionCostBps, in.CurrentRiskPenaltyBps,
		in.CurrentContinuationValueBps, in.CandidateFillProbability,
		in.CandidateFillProbabilityStdError,
		in.CandidateTerminalMarkoutBps, in.CandidateExecutionCostBps,
		in.CandidateRiskPenaltyBps, in.CandidateContinuationValueBps,
		in.ReplacementCostBps, in.ReplacementCostStdErrorBps, in.CancelImmediateValueBps,
		in.CancelContinuationValueBps,
	}
	for _, value := range values {
		if !finiteLifecycleValue(value) {
			return d
		}
	}
	if !validLifecycleProbability(in.CurrentFillProbability) ||
		!validLifecycleProbability(in.CandidateFillProbability) ||
		!finiteLifecycleValue(in.DiscountFactor) || in.DiscountFactor < 0 || in.DiscountFactor > 1 ||
		in.ReplacementCostBps < 0 || in.CurrentFillProbabilityStdError < 0 ||
		in.CandidateFillProbabilityStdError < 0 || in.ReplacementCostStdErrorBps < 0 {
		return d
	}
	d.Evaluated = true
	d.Reason = "Bellman action value"
	d.CancelValueBps = in.CancelImmediateValueBps +
		in.DiscountFactor*in.CancelContinuationValueBps
	d.KeepValueBps = math.Inf(-1)
	d.ReplaceValueBps = math.Inf(-1)
	if in.CurrentActive {
		d.KeepValueBps = lifecycleActionValue(
			in.CurrentFillProbability, in.CurrentTerminalMarkoutBps,
			in.CurrentExecutionCostBps, in.CurrentRiskPenaltyBps,
			in.CurrentContinuationValueBps, in.DiscountFactor, 0)
	}
	if in.CandidateActive {
		d.ReplaceValueBps = lifecycleActionValue(
			in.CandidateFillProbability, in.CandidateTerminalMarkoutBps,
			in.CandidateExecutionCostBps, in.CandidateRiskPenaltyBps,
			in.CandidateContinuationValueBps, in.DiscountFactor,
			in.ReplacementCostBps)
	}
	if in.CurrentActive {
		derivative := in.CurrentTerminalMarkoutBps - in.CurrentExecutionCostBps -
			in.CurrentRiskPenaltyBps - in.DiscountFactor*in.CurrentContinuationValueBps
		d.KeepStdErrorBps = math.Abs(derivative) * in.CurrentFillProbabilityStdError
	}
	if in.CandidateActive {
		derivative := in.CandidateTerminalMarkoutBps - in.CandidateExecutionCostBps -
			in.CandidateRiskPenaltyBps - in.DiscountFactor*in.CandidateContinuationValueBps
		d.ReplaceStdErrorBps = math.Sqrt(math.Pow(math.Abs(derivative)*in.CandidateFillProbabilityStdError, 2) + math.Pow(in.ReplacementCostStdErrorBps, 2))
	}
	d.ActionMarginBps = math.Max(0, in.ActionMarginBps)
	d.ConfidenceZScore = in.ConfidenceZScore
	if d.ConfidenceZScore <= 0 || !finiteLifecycleValue(d.ConfidenceZScore) {
		d.ConfidenceZScore = 1.645
	}
	d.KeepLowerBps = d.KeepValueBps - d.ConfidenceZScore*d.KeepStdErrorBps
	d.ReplaceLowerBps = d.ReplaceValueBps - d.ConfidenceZScore*d.ReplaceStdErrorBps
	d.CancelLowerBps = d.CancelValueBps - d.ConfidenceZScore*d.CancelStdErrorBps
	keepUpper := d.KeepValueBps + d.ConfidenceZScore*d.KeepStdErrorBps
	replaceUpper := d.ReplaceValueBps + d.ConfidenceZScore*d.ReplaceStdErrorBps
	cancelUpper := d.CancelValueBps + d.ConfidenceZScore*d.CancelStdErrorBps
	_ = replaceUpper

	// Keep wins exact ties so a review boundary does not destroy queue age for
	// numerically indistinguishable value.  Replace must beat both alternatives
	// by a strict amount; otherwise cancellation remains the safe fallback.
	d.Action = QuoteLifecycleCancel
	d.SelectedValueBps = d.CancelValueBps
	if d.KeepValueBps >= d.SelectedValueBps && d.KeepLowerBps >= math.Max(0, cancelUpper)+d.ActionMarginBps {
		d.Action, d.SelectedValueBps = QuoteLifecycleKeep, d.KeepValueBps
	}
	if d.ReplaceValueBps > d.SelectedValueBps+1e-12 &&
		d.ReplaceLowerBps >= math.Max(0, math.Max(keepUpper, cancelUpper))+d.ActionMarginBps {
		d.Action, d.SelectedValueBps = QuoteLifecycleReplace, d.ReplaceValueBps
	}
	d.IncrementalVsKeepBps = d.SelectedValueBps
	if finiteLifecycleValue(d.KeepValueBps) {
		d.IncrementalVsKeepBps -= d.KeepValueBps
	}
	return d
}

// QuoteCycleGrossBps is the pure quote-to-quote gross return.  It has no
// terminal-price forecast and is valid only for a completed two-leg cycle.
func QuoteCycleGrossBps(bidQuote, askQuote float64) float64 {
	if bidQuote <= 0 || askQuote <= bidQuote {
		return 0
	}
	return math.Log(askQuote/bidQuote) * 10_000
}

// QuoteCycleFeeNetBps applies exact multiplicative maker fees to the gross
// cycle.  Risk hurdles such as adverse selection and minimum edge are not
// included; callers must subtract them once in the admission layer.
func QuoteCycleFeeNetBps(bidQuote, askQuote, buyFeeBps, sellFeeBps float64) float64 {
	gross := QuoteCycleGrossBps(bidQuote, askQuote)
	if gross == 0 || buyFeeBps < 0 || sellFeeBps < 0 || buyFeeBps >= 10_000 || sellFeeBps >= 10_000 {
		return 0
	}
	buyFee, sellFee := buyFeeBps/10_000, sellFeeBps/10_000
	return gross + math.Log1p(-buyFee)*10_000 + math.Log1p(-sellFee)*10_000
}

// OneFillTerminalMarkoutBps is the pure one-fill terminal markout.  A BUY is
// liquidated at the future executable bid; a SELL is compared with the bid at
// which the unfilled counterfactual inventory could be liquidated.  A future
// ask belongs to an explicitly modeled SELL->BUY cycle, not this one-fill
// wealth benchmark.
func OneFillTerminalMarkoutBps(buy bool, quote, terminalBid float64) float64 {
	if quote <= 0 || terminalBid <= 0 {
		return 0
	}
	if buy {
		return math.Log(terminalBid/quote) * 10_000
	}
	return math.Log(quote/terminalBid) * 10_000
}
