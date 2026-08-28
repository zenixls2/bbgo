package gammacapture

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"time"
)

const relativeHoldRiskCheckpointVersion = 2

// RelativeHoldRiskConfig controls the causal, same-symbol Hold-relative
// diagnostic. Returns passed to this model are fee-net log returns over one
// fixed label horizon; the model never turns a BBO observation into a label by
// itself. A caller must wait until the label's MaturedAt timestamp before
// calling UpdateLabel.
//
// PriorEffectiveSamples and the two prior variances are explicit Bayesian
// shrinkage inputs. Zero means no prior is imposed, which is useful for a
// shadow study that wants to expose the raw estimator. They are not hidden
// strategy weights and do not gate orders.
type RelativeHoldRiskConfig struct {
	Enabled                         bool          `json:"enabled" yaml:"enabled"`
	ShadowOnly                      bool          `json:"shadowOnly" yaml:"shadowOnly"`
	Horizon                         time.Duration `json:"horizon" yaml:"horizon"`
	HalfLife                        time.Duration `json:"halfLife" yaml:"halfLife"`
	MinimumEffectiveSamples         float64       `json:"minimumEffectiveSamples" yaml:"minimumEffectiveSamples"`
	MinimumDownsideEffectiveSamples float64       `json:"minimumDownsideEffectiveSamples" yaml:"minimumDownsideEffectiveSamples"`
	PriorEffectiveSamples           float64       `json:"priorEffectiveSamples" yaml:"priorEffectiveSamples"`
	PriorVarianceFraction           float64       `json:"priorVarianceFraction" yaml:"priorVarianceFraction"`
	PriorDownsideVarianceFraction   float64       `json:"priorDownsideVarianceFraction" yaml:"priorDownsideVarianceFraction"`
	TargetDownsideBeta              float64       `json:"targetDownsideBeta" yaml:"targetDownsideBeta"`
	// TargetTotalBeta is the desired all-regime strategy-to-Hold beta. A value
	// below one limits mechanical exposure to the symbol price; the active
	// penalty remains opt-in through TotalBetaAversion.
	TargetTotalBeta       float64 `json:"targetTotalBeta" yaml:"targetTotalBeta"`
	ConfidenceZ           float64 `json:"confidenceZ" yaml:"confidenceZ"`
	TailQuantile          float64 `json:"tailQuantile" yaml:"tailQuantile"`
	MaxTailSamples        int     `json:"maxTailSamples" yaml:"maxTailSamples"`
	TrackingErrorAversion float64 `json:"trackingErrorAversion" yaml:"trackingErrorAversion"`
	DownsideBetaAversion  float64 `json:"downsideBetaAversion" yaml:"downsideBetaAversion"`
	TotalBetaAversion     float64 `json:"totalBetaAversion" yaml:"totalBetaAversion"`
}

// UnmarshalJSON accepts both the human-readable duration strings emitted by
// BBGO's YAML-to-JSON config bridge (for example, "6h") and the native JSON
// nanosecond numbers accepted by time.Duration. Most strategy configs use
// types.Duration, but this model deliberately keeps time.Duration in its
// public research API; normalize the two duration fields at this boundary so
// enabling the live canary cannot fail after YAML decoding succeeds.
func (c *RelativeHoldRiskConfig) UnmarshalJSON(data []byte) error {
	type alias RelativeHoldRiskConfig
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for _, key := range []string{"horizon", "halfLife"} {
		raw, ok := fields[key]
		if !ok || len(raw) == 0 || string(raw) == "null" {
			continue
		}
		var durationText string
		if err := json.Unmarshal(raw, &durationText); err != nil {
			// A numeric duration is already in time.Duration's native unit.
			var numeric float64
			if numberErr := json.Unmarshal(raw, &numeric); numberErr != nil {
				return fmt.Errorf("relative-hold %s duration: %w", key, err)
			}
			fields[key] = json.RawMessage(strconv.FormatInt(int64(numeric), 10))
			continue
		}
		parsed, err := time.ParseDuration(durationText)
		if err != nil {
			return fmt.Errorf("relative-hold %s duration %q: %w", key, durationText, err)
		}
		fields[key] = json.RawMessage(strconv.FormatInt(int64(parsed), 10))
	}
	normalized, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	var decoded alias
	if err := json.Unmarshal(normalized, &decoded); err != nil {
		return err
	}
	*c = RelativeHoldRiskConfig(decoded)
	return nil
}

// RelativeHoldRiskWarmupRequirement is the causal preload needed to have a
// ready Relative-Hold estimator before the first scored decision. Labels are
// deliberately treated as non-overlapping one-horizon observations, matching
// UpdateLabel's monotonic maturity contract.
type RelativeHoldRiskWarmupRequirement struct {
	RequiredLabels   int
	EffectiveSamples float64
	RequiredDuration time.Duration
	Feasible         bool
}

func (c RelativeHoldRiskConfig) normalized() RelativeHoldRiskConfig {
	if c.Horizon <= 0 {
		c.Horizon = time.Hour
	}
	if c.HalfLife <= 0 {
		// This is a neutral online default: one label horizon is one half-life.
		// Production tuning must be done by same-symbol walk-forward evidence.
		c.HalfLife = c.Horizon
	}
	if c.MinimumEffectiveSamples <= 0 || math.IsNaN(c.MinimumEffectiveSamples) || math.IsInf(c.MinimumEffectiveSamples, 0) {
		c.MinimumEffectiveSamples = 4
	}
	if c.MinimumDownsideEffectiveSamples <= 0 || math.IsNaN(c.MinimumDownsideEffectiveSamples) || math.IsInf(c.MinimumDownsideEffectiveSamples, 0) {
		c.MinimumDownsideEffectiveSamples = 4
	}
	if c.PriorEffectiveSamples < 0 || math.IsNaN(c.PriorEffectiveSamples) || math.IsInf(c.PriorEffectiveSamples, 0) {
		c.PriorEffectiveSamples = 0
	}
	if c.PriorVarianceFraction < 0 || math.IsNaN(c.PriorVarianceFraction) || math.IsInf(c.PriorVarianceFraction, 0) {
		c.PriorVarianceFraction = 0
	}
	if c.PriorDownsideVarianceFraction < 0 || math.IsNaN(c.PriorDownsideVarianceFraction) || math.IsInf(c.PriorDownsideVarianceFraction, 0) {
		c.PriorDownsideVarianceFraction = 0
	}
	if c.TargetDownsideBeta <= 0 || math.IsNaN(c.TargetDownsideBeta) || math.IsInf(c.TargetDownsideBeta, 0) {
		c.TargetDownsideBeta = 1
	}
	if c.TargetTotalBeta <= 0 || math.IsNaN(c.TargetTotalBeta) || math.IsInf(c.TargetTotalBeta, 0) {
		c.TargetTotalBeta = 1
	}
	if c.ConfidenceZ <= 0 || math.IsNaN(c.ConfidenceZ) || math.IsInf(c.ConfidenceZ, 0) {
		c.ConfidenceZ = 1.645
	}
	if c.TailQuantile <= 0 || c.TailQuantile >= 1 || math.IsNaN(c.TailQuantile) || math.IsInf(c.TailQuantile, 0) {
		c.TailQuantile = .95
	}
	if c.MaxTailSamples <= 0 {
		c.MaxTailSamples = 256
	}
	return c
}

// WarmupRequirement derives, rather than hard-codes, the Relative-Hold
// preload. For equally spaced matured labels with decay factor
// f=exp(-ln(2)H/halfLife),
//
//	N_eff(n) = ((1+f)/(1-f)) ((1-f^n)/(1+f^n)).
//
// The extra updateInterval is a causal boundary margin: the last label must
// mature and be ingested before scoreFrom's first quote decision. A false
// Feasible result means the configured minimum N_eff exceeds the asymptotic
// information mass of this EWMA and no finite preload can make the model
// ready without changing the sampling clock or half-life.
func (c RelativeHoldRiskConfig) WarmupRequirement(updateInterval time.Duration) RelativeHoldRiskWarmupRequirement {
	c = c.normalized()
	if !c.Enabled {
		return RelativeHoldRiskWarmupRequirement{Feasible: true}
	}
	if updateInterval <= 0 {
		updateInterval = time.Minute
	}
	decay := math.Exp(-math.Ln2 * c.Horizon.Seconds() / c.HalfLife.Seconds())
	if !relativeFinite(decay) || decay <= 0 || decay >= 1 {
		return RelativeHoldRiskWarmupRequirement{}
	}
	asymptotic := (1 + decay) / (1 - decay)
	// The same causal label clock feeds both the all-label tracking state and
	// the conditional downside state.  Use the larger structural target so a
	// preload cannot satisfy the total N_eff requirement while making the
	// downside estimator structurally impossible to warm.  This is not a
	// guarantee that enough negative-Hold labels will occur; that path-dependent
	// condition is reported by DownsideReady and must remain online.
	target := math.Max(2, math.Max(c.MinimumEffectiveSamples, c.MinimumDownsideEffectiveSamples))
	if target > asymptotic*(1+1e-12) {
		return RelativeHoldRiskWarmupRequirement{Feasible: false}
	}
	labels := 2
	for ; labels < 1_000_000; labels++ {
		neff := asymptotic * (1 - math.Pow(decay, float64(labels))) /
			(1 + math.Pow(decay, float64(labels)))
		if neff >= target {
			return RelativeHoldRiskWarmupRequirement{
				RequiredLabels: labels, EffectiveSamples: neff,
				RequiredDuration: time.Duration(labels)*c.Horizon + updateInterval,
				Feasible:         true,
			}
		}
	}
	return RelativeHoldRiskWarmupRequirement{Feasible: false}
}

// RelativeHoldRiskLabel is the only update input. StrategyReturn and
// HoldReturn are same-symbol fee-net log returns over Config.Horizon. The
// strategy return must use the same next-executable-BBO convention as the
// Hold return; mixing midpoint or future-known fills would invalidate beta and
// tracking error.
type RelativeHoldRiskLabel struct {
	DecisionAt     time.Time
	MaturedAt      time.Time
	StrategyReturn float64
	HoldReturn     float64
}

// RelativeHoldRiskEquityPoint is the compact causal observation used by the
// live strategy bridge. Both values are marked at the executable bid because
// the position must be liquidated there; no midpoint is used for the label.
type RelativeHoldRiskEquityPoint struct {
	At             time.Time `json:"at"`
	StrategyEquity float64   `json:"strategyEquity"`
	HoldEquity     float64   `json:"holdEquity"`
}

// RelativeHoldRiskState is a causal snapshot. Variances and returns are in
// fractional log-return units; the Bps fields are diagnostics only.
type RelativeHoldRiskState struct {
	Ready                    bool
	DownsideReady            bool
	Stale                    bool
	Reason                   string
	LastMaturedAt            time.Time
	Age                      time.Duration
	MaturedLabels            int
	EffectiveSamples         float64
	DownsideEffectiveSamples float64
	Shrinkage                float64
	DownsideShrinkage        float64
	MeanExcessReturn         float64
	MeanExcessReturnBps      float64
	TrackingVariance         float64
	TrackingError            float64
	TrackingErrorBps         float64
	DownsideBeta             float64
	TargetDownsideBeta       float64
	DownsideHoldVariance     float64
	DownsideStrategyVariance float64
	DownsideCovariance       float64
	DownsideBetaUpper        float64
	TotalBetaReady           bool
	TotalBeta                float64
	TargetTotalBeta          float64
	TotalHoldVariance        float64
	TotalStrategyVariance    float64
	TotalCovariance          float64
	TotalBetaUpper           float64
	DownsideCVaR             float64
	DownsideCVaRBps          float64
}

// RelativeHoldRiskPrecision is the current precision requirement implied by
// the observed excess-return variance. It deliberately avoids a universal
// effective-sample cutoff: the required information mass grows when the
// observed signal is weak or noisy and shrinks when the signal is strong.
// The structural model minimum (MinimumEffectiveSamples) remains separate
// and only prevents degenerate moments from being used.
type RelativeHoldRiskPrecision struct {
	StandardError            float64
	StandardErrorBps         float64
	OneSidedLower            float64
	OneSidedLowerBps         float64
	RequiredEffectiveSamples float64
}

// RelativeHoldRiskScalarCheckpoint is the exact EWMA sufficient statistic for
// the all-label excess-return stream.  Keeping weight and squared weight (in
// addition to the first two moments) preserves the effective sample size across
// a restart; restoring only Snapshot would lose the decay clock and produce a
// different posterior after the next label.
type RelativeHoldRiskScalarCheckpoint struct {
	Weight   float64 `json:"weight"`
	WeightSq float64 `json:"weightSq"`
	Sum      float64 `json:"sum"`
	SumSq    float64 `json:"sumSq"`
}

// RelativeHoldRiskBivariateCheckpoint is the exact conditional downside
// sufficient statistic.  It is intentionally public so the research replay
// package can persist it without exposing raw market data.
type RelativeHoldRiskBivariateCheckpoint struct {
	Weight   float64 `json:"weight"`
	WeightSq float64 `json:"weightSq"`
	SumX     float64 `json:"sumX"`
	SumY     float64 `json:"sumY"`
	SumXX    float64 `json:"sumXX"`
	SumYY    float64 `json:"sumYY"`
	SumXY    float64 `json:"sumXY"`
}

type RelativeHoldRiskTailCheckpoint struct {
	At   time.Time `json:"at"`
	Loss float64   `json:"loss"`
}

// RelativeHoldRiskCheckpoint is restart-safe online state.  It contains no
// orders, balances, or future labels: only the model's causal sufficient
// statistics and the last matured label cursor are persisted.
type RelativeHoldRiskCheckpoint struct {
	Version       int                                 `json:"version"`
	Config        RelativeHoldRiskConfig              `json:"config"`
	AllExcess     RelativeHoldRiskScalarCheckpoint    `json:"allExcess"`
	AllReturns    RelativeHoldRiskBivariateCheckpoint `json:"allReturns"`
	Downside      RelativeHoldRiskBivariateCheckpoint `json:"downside"`
	Tail          []RelativeHoldRiskTailCheckpoint    `json:"tail,omitempty"`
	LastMaturedAt time.Time                           `json:"lastMaturedAt,omitempty"`
	MaturedLabels int                                 `json:"maturedLabels"`
	State         RelativeHoldRiskState               `json:"state"`
}

// Precision computes the one-sided normal precision bound for the current
// relative-Hold mean. With z as the predeclared confidence quantile,
//
//	SE = sqrt(Var(e) / N_eff),
//	N_req = z^2 Var(e) / mean(e)^2,
//
// where N_req is +Inf for a non-positive mean. This is an evidence diagnostic
// and promotion gate; it does not alter the action utility or create a side
// gate in the Fast optimizer.
func (s RelativeHoldRiskState) Precision(z float64) RelativeHoldRiskPrecision {
	if z <= 0 || math.IsNaN(z) || math.IsInf(z, 0) {
		z = 1.645
	}
	precision := RelativeHoldRiskPrecision{RequiredEffectiveSamples: math.Inf(1)}
	if s.EffectiveSamples <= 0 || s.TrackingVariance < 0 ||
		!relativeFinite(s.EffectiveSamples) || !relativeFinite(s.TrackingVariance) ||
		!relativeFinite(s.MeanExcessReturn) {
		return precision
	}
	precision.StandardError = math.Sqrt(s.TrackingVariance / s.EffectiveSamples)
	precision.StandardErrorBps = precision.StandardError * 10_000
	precision.OneSidedLower = s.MeanExcessReturn - z*precision.StandardError
	precision.OneSidedLowerBps = precision.OneSidedLower * 10_000
	if s.MeanExcessReturn > 0 {
		precision.RequiredEffectiveSamples = z * z * s.TrackingVariance /
			(s.MeanExcessReturn * s.MeanExcessReturn)
	}
	return precision
}

// RelativeHoldAction describes the single joint Fast action to which a
// relative utility adjustment may be applied after component promotion. The
// risk weight is projected notional divided by pair equity; it is not an
// independent side gate.
type RelativeHoldAction struct {
	RiskWeight            float64
	PairEquityJPY         float64
	TrackingErrorAversion float64
	DownsideBetaAversion  float64
	TotalBetaAversion     float64
	// CurrentInventoryBeta and ProjectedInventoryBeta are optional causal
	// portfolio sensitivities. When supplied, the total-beta term is the
	// incremental change in quadratic beta risk, so a de-risking SELL can be
	// rewarded instead of merely suppressing both sides.
	CurrentInventoryBeta   float64
	ProjectedInventoryBeta float64
	InventoryBetaSupplied  bool
}

// RelativeHoldUtility is deliberately a scalar. It is intended to be added
// once to the joint price/quantity objective, never separately to price,
// quantity, and allowBid/allowAsk. CVaR is reported as shadow loss and is not
// included in NetJPYPerHour until a causal component replay promotes it.
type RelativeHoldUtility struct {
	Ready                          bool
	MeanExcessJPYPerHour           float64
	TrackingErrorPenaltyJPYPerHour float64
	DownsideBetaPenaltyJPYPerHour  float64
	TotalBetaPenaltyJPYPerHour     float64
	NetJPYPerHour                  float64
	CVaRShadowLossJPY              float64
	Reason                         string
}

// EvaluateAction computes the relative-Hold contribution per one label hour:
//
//	U = W w m_delta - 1/2 W w^2 lambda_TE Var(e_delta)
//	    - 1/2 W w^2 lambda_beta Var(H^-)(beta^- - beta_0)_+^2.
//
// The expression is a risk adjustment, not an order admission rule. A state
// that is not mature returns zero contribution and an explicit reason.
func (s RelativeHoldRiskState) EvaluateAction(action RelativeHoldAction) RelativeHoldUtility {
	u := RelativeHoldUtility{Reason: "relative-hold state is immature"}
	if !s.Ready || action.PairEquityJPY <= 0 || action.RiskWeight <= 0 ||
		math.IsNaN(action.PairEquityJPY) || math.IsInf(action.PairEquityJPY, 0) ||
		math.IsNaN(action.RiskWeight) || math.IsInf(action.RiskWeight, 0) {
		return u
	}
	if action.TrackingErrorAversion < 0 || math.IsNaN(action.TrackingErrorAversion) || math.IsInf(action.TrackingErrorAversion, 0) {
		action.TrackingErrorAversion = 0
	}
	if action.DownsideBetaAversion < 0 || math.IsNaN(action.DownsideBetaAversion) || math.IsInf(action.DownsideBetaAversion, 0) {
		action.DownsideBetaAversion = 0
	}
	if action.TotalBetaAversion < 0 || math.IsNaN(action.TotalBetaAversion) || math.IsInf(action.TotalBetaAversion, 0) {
		action.TotalBetaAversion = 0
	}
	mean := action.PairEquityJPY * action.RiskWeight * s.MeanExcessReturn
	tePenalty := .5 * action.PairEquityJPY * action.RiskWeight * action.RiskWeight *
		action.TrackingErrorAversion * s.TrackingVariance
	u.Ready = true
	u.MeanExcessJPYPerHour = mean
	u.TrackingErrorPenaltyJPYPerHour = tePenalty
	u.NetJPYPerHour = mean - tePenalty
	u.Reason = "relative-hold tracking-error utility"
	if s.DownsideReady && action.DownsideBetaAversion > 0 {
		betaExcess := math.Max(0, s.DownsideBeta-s.TargetDownsideBeta)
		downsidePenalty := .5 * action.PairEquityJPY * action.RiskWeight * action.RiskWeight *
			action.DownsideBetaAversion * s.DownsideHoldVariance * betaExcess * betaExcess
		u.DownsideBetaPenaltyJPYPerHour = downsidePenalty
		u.NetJPYPerHour -= downsidePenalty
		u.Reason += "; downside-beta penalty"
	}
	if s.TotalBetaReady && action.TotalBetaAversion > 0 {
		totalPenalty := 0.0
		if action.InventoryBetaSupplied && relativeFinite(action.CurrentInventoryBeta) && relativeFinite(action.ProjectedInventoryBeta) {
			currentExcess := math.Max(0, action.CurrentInventoryBeta-s.TargetTotalBeta)
			projectedExcess := math.Max(0, action.ProjectedInventoryBeta-s.TargetTotalBeta)
			totalPenalty = .5 * action.PairEquityJPY * action.TotalBetaAversion *
				s.TotalHoldVariance * (projectedExcess*projectedExcess - currentExcess*currentExcess)
		} else {
			// Preserve a safe scalar for standalone callers that do not provide
			// portfolio beta projections.
			betaExcess := math.Max(0, s.TotalBeta-s.TargetTotalBeta)
			totalPenalty = .5 * action.PairEquityJPY * action.RiskWeight * action.RiskWeight *
				action.TotalBetaAversion * s.TotalHoldVariance * betaExcess * betaExcess
		}
		u.TotalBetaPenaltyJPYPerHour = totalPenalty
		u.NetJPYPerHour -= totalPenalty
		u.Reason += "; total-beta penalty"
	}
	u.CVaRShadowLossJPY = action.PairEquityJPY * action.RiskWeight * math.Max(0, s.DownsideCVaR)
	return u
}

type relativeWeightedScalar struct {
	weight, weightSq float64
	sum, sumSq       float64
}

func (s *relativeWeightedScalar) decay(factor float64) {
	s.weight *= factor
	s.weightSq *= factor * factor
	s.sum *= factor
	s.sumSq *= factor
}

func (s *relativeWeightedScalar) update(value, factor float64) {
	s.decay(factor)
	s.weight++
	s.weightSq++
	s.sum += value
	s.sumSq += value * value
}

func (s relativeWeightedScalar) effectiveSamples() float64 {
	if s.weight <= 0 || s.weightSq <= 0 {
		return 0
	}
	return s.weight * s.weight / s.weightSq
}

func (s relativeWeightedScalar) moments(priorSamples, priorVariance float64) (mean, variance, shrinkage float64) {
	if s.weight <= 0 {
		return 0, math.Max(0, priorVariance), 0
	}
	mean = s.sum / s.weight
	denominator := s.weight - s.weightSq/s.weight
	if denominator > 0 {
		variance = (s.sumSq - s.sum*s.sum/s.weight) / denominator
	}
	variance = math.Max(0, variance)
	if priorSamples > 0 {
		shrinkage = s.effectiveSamples() / (s.effectiveSamples() + priorSamples)
		mean *= shrinkage
		variance = shrinkage*variance + (1-shrinkage)*math.Max(0, priorVariance)
	} else {
		shrinkage = 1
	}
	return mean, variance, shrinkage
}

type relativeWeightedBivariate struct {
	weight, weightSq    float64
	sumX, sumY          float64
	sumXX, sumYY, sumXY float64
}

func (b *relativeWeightedBivariate) decay(factor float64) {
	b.weight *= factor
	b.weightSq *= factor * factor
	b.sumX *= factor
	b.sumY *= factor
	b.sumXX *= factor
	b.sumYY *= factor
	b.sumXY *= factor
}

func (b *relativeWeightedBivariate) update(x, y, factor float64) {
	b.decay(factor)
	b.weight++
	b.weightSq++
	b.sumX += x
	b.sumY += y
	b.sumXX += x * x
	b.sumYY += y * y
	b.sumXY += x * y
}

func (b relativeWeightedBivariate) effectiveSamples() float64 {
	if b.weight <= 0 || b.weightSq <= 0 {
		return 0
	}
	return b.weight * b.weight / b.weightSq
}

func (b relativeWeightedBivariate) moments(priorSamples, priorVariance float64) (covariance, holdVariance, strategyVariance, shrinkage float64) {
	if b.weight <= 0 {
		return 0, math.Max(0, priorVariance), math.Max(0, priorVariance), 0
	}
	denominator := b.weight - b.weightSq/b.weight
	if denominator > 0 {
		covariance = (b.sumXY - b.sumX*b.sumY/b.weight) / denominator
		holdVariance = (b.sumYY - b.sumY*b.sumY/b.weight) / denominator
		strategyVariance = (b.sumXX - b.sumX*b.sumX/b.weight) / denominator
	}
	covariance = finiteOrZero(covariance)
	holdVariance = math.Max(0, finiteOrZero(holdVariance))
	strategyVariance = math.Max(0, finiteOrZero(strategyVariance))
	if priorSamples > 0 {
		shrinkage = b.effectiveSamples() / (b.effectiveSamples() + priorSamples)
		covariance *= shrinkage // zero covariance is the explicit prior.
		holdVariance = shrinkage*holdVariance + (1-shrinkage)*math.Max(0, priorVariance)
		strategyVariance = shrinkage*strategyVariance + (1-shrinkage)*math.Max(0, priorVariance)
	} else {
		shrinkage = 1
	}
	return covariance, holdVariance, strategyVariance, shrinkage
}

type relativeTailSample struct {
	at   time.Time
	loss float64
}

// RelativeHoldRiskModel is an online EWMA sufficient-statistics model. It
// stores no raw market data and therefore keeps replay/live CPU and memory
// bounded. Conditional downside moments are updated only for Hold<0 labels.
type RelativeHoldRiskModel struct {
	config        RelativeHoldRiskConfig
	allExcess     relativeWeightedScalar
	allReturns    relativeWeightedBivariate
	downside      relativeWeightedBivariate
	tail          []relativeTailSample
	lastMaturedAt time.Time
	maturedLabels int
	state         RelativeHoldRiskState
}

func NewRelativeHoldRiskModel(config RelativeHoldRiskConfig) *RelativeHoldRiskModel {
	return &RelativeHoldRiskModel{config: config.normalized(), state: RelativeHoldRiskState{Reason: "relative-hold state is immature"}}
}

// Checkpoint returns a value copy of all state needed for an exact causal
// continuation.  The caller should persist the replay cursor alongside it;
// this method deliberately does not infer a cursor from wall-clock time.
func (m *RelativeHoldRiskModel) Checkpoint() RelativeHoldRiskCheckpoint {
	if m == nil {
		return RelativeHoldRiskCheckpoint{Version: relativeHoldRiskCheckpointVersion}
	}
	cp := RelativeHoldRiskCheckpoint{
		Version: relativeHoldRiskCheckpointVersion, Config: m.config,
		AllExcess: RelativeHoldRiskScalarCheckpoint{
			Weight: m.allExcess.weight, WeightSq: m.allExcess.weightSq,
			Sum: m.allExcess.sum, SumSq: m.allExcess.sumSq,
		},
		AllReturns: RelativeHoldRiskBivariateCheckpoint{
			Weight: m.allReturns.weight, WeightSq: m.allReturns.weightSq,
			SumX: m.allReturns.sumX, SumY: m.allReturns.sumY,
			SumXX: m.allReturns.sumXX, SumYY: m.allReturns.sumYY, SumXY: m.allReturns.sumXY,
		},
		Downside: RelativeHoldRiskBivariateCheckpoint{
			Weight: m.downside.weight, WeightSq: m.downside.weightSq,
			SumX: m.downside.sumX, SumY: m.downside.sumY,
			SumXX: m.downside.sumXX, SumYY: m.downside.sumYY, SumXY: m.downside.sumXY,
		},
		LastMaturedAt: m.lastMaturedAt, MaturedLabels: m.maturedLabels,
		State: m.state,
	}
	if len(m.tail) > 0 {
		cp.Tail = make([]RelativeHoldRiskTailCheckpoint, len(m.tail))
		for i, sample := range m.tail {
			cp.Tail[i] = RelativeHoldRiskTailCheckpoint{At: sample.at, Loss: sample.loss}
		}
	}
	return cp
}

// Restore replaces the model with a checkpoint created by the same effective
// configuration.  Refusing a configuration mismatch is important: a changed
// horizon or half-life would otherwise make the stored EWMA weights
// statistically meaningless while appearing healthy.
func (m *RelativeHoldRiskModel) Restore(cp RelativeHoldRiskCheckpoint) error {
	if m == nil {
		return fmt.Errorf("nil relative-hold model")
	}
	if cp.Version != relativeHoldRiskCheckpointVersion {
		return fmt.Errorf("relative-hold checkpoint version %d is unsupported", cp.Version)
	}
	if !reflect.DeepEqual(m.config.normalized(), cp.Config.normalized()) {
		return fmt.Errorf("relative-hold checkpoint configuration mismatch")
	}
	if cp.MaturedLabels < 0 || !relativeFinite(cp.AllExcess.Weight) ||
		!relativeFinite(cp.AllExcess.WeightSq) || cp.AllExcess.Weight < 0 ||
		cp.AllExcess.WeightSq < 0 || !relativeFinite(cp.AllExcess.Sum) ||
		!relativeFinite(cp.AllExcess.SumSq) || !relativeFinite(cp.Downside.Weight) ||
		!relativeFinite(cp.Downside.WeightSq) || cp.Downside.Weight < 0 ||
		cp.Downside.WeightSq < 0 || !relativeFinite(cp.Downside.SumX) ||
		!relativeFinite(cp.Downside.SumY) || !relativeFinite(cp.Downside.SumXX) ||
		!relativeFinite(cp.Downside.SumYY) || !relativeFinite(cp.Downside.SumXY) {
		return fmt.Errorf("relative-hold checkpoint sufficient statistics are invalid")
	}
	if !relativeFinite(cp.AllReturns.Weight) || !relativeFinite(cp.AllReturns.WeightSq) ||
		cp.AllReturns.Weight < 0 || cp.AllReturns.WeightSq < 0 ||
		!relativeFinite(cp.AllReturns.SumX) || !relativeFinite(cp.AllReturns.SumY) ||
		!relativeFinite(cp.AllReturns.SumXX) || !relativeFinite(cp.AllReturns.SumYY) ||
		!relativeFinite(cp.AllReturns.SumXY) {
		return fmt.Errorf("relative-hold all-return checkpoint sufficient statistics are invalid")
	}
	if len(cp.Tail) > m.config.MaxTailSamples {
		return fmt.Errorf("relative-hold checkpoint tail exceeds configured bound")
	}
	for _, sample := range cp.Tail {
		if sample.At.IsZero() || !relativeFinite(sample.Loss) {
			return fmt.Errorf("relative-hold checkpoint tail sample is invalid")
		}
	}
	m.allExcess = relativeWeightedScalar{
		weight: cp.AllExcess.Weight, weightSq: cp.AllExcess.WeightSq,
		sum: cp.AllExcess.Sum, sumSq: cp.AllExcess.SumSq,
	}
	m.allReturns = relativeWeightedBivariate{
		weight: cp.AllReturns.Weight, weightSq: cp.AllReturns.WeightSq,
		sumX: cp.AllReturns.SumX, sumY: cp.AllReturns.SumY,
		sumXX: cp.AllReturns.SumXX, sumYY: cp.AllReturns.SumYY, sumXY: cp.AllReturns.SumXY,
	}
	m.downside = relativeWeightedBivariate{
		weight: cp.Downside.Weight, weightSq: cp.Downside.WeightSq,
		sumX: cp.Downside.SumX, sumY: cp.Downside.SumY,
		sumXX: cp.Downside.SumXX, sumYY: cp.Downside.SumYY, sumXY: cp.Downside.SumXY,
	}
	m.tail = make([]relativeTailSample, len(cp.Tail))
	for i, sample := range cp.Tail {
		m.tail[i] = relativeTailSample{at: sample.At, loss: sample.Loss}
	}
	m.lastMaturedAt = cp.LastMaturedAt
	m.maturedLabels = cp.MaturedLabels
	// Recompute rather than trust a stale diagnostic snapshot.  This also
	// makes older checkpoints robust to newly added derived fields.
	m.state = m.snapshot()
	return nil
}

// UpdateLabel accepts a label only after its configured horizon has matured.
// It also rejects duplicate/out-of-order maturity timestamps, making replay
// deterministic and preventing a late label from changing an earlier state.
func (m *RelativeHoldRiskModel) UpdateLabel(label RelativeHoldRiskLabel) bool {
	if m == nil || label.DecisionAt.IsZero() || label.MaturedAt.IsZero() ||
		label.MaturedAt.Before(label.DecisionAt.Add(m.config.Horizon)) ||
		!label.MaturedAt.After(m.lastMaturedAt) ||
		!relativeFinite(label.StrategyReturn) || !relativeFinite(label.HoldReturn) {
		return false
	}
	dt := m.config.Horizon
	if !m.lastMaturedAt.IsZero() {
		dt = label.MaturedAt.Sub(m.lastMaturedAt)
		if dt <= 0 {
			return false
		}
	}
	factor := math.Exp(-math.Ln2 * dt.Seconds() / m.config.HalfLife.Seconds())
	factor = math.Max(0, math.Min(1, factor))
	if !m.lastMaturedAt.IsZero() && dt > m.config.HalfLife {
		// CVaR is a shadow recent-tail diagnostic. Do not let a stale tail
		// survive a long data gap even though EWMA moments decay smoothly.
		m.tail = m.tail[:0]
	}
	excess := label.StrategyReturn - label.HoldReturn
	m.allExcess.update(excess, factor)
	m.allReturns.update(label.StrategyReturn, label.HoldReturn, factor)
	m.downside.decay(factor)
	if label.HoldReturn < 0 {
		m.downside.update(label.StrategyReturn, label.HoldReturn, 1)
		m.appendTail(relativeTailSample{at: label.MaturedAt, loss: -label.StrategyReturn})
	}
	m.lastMaturedAt = label.MaturedAt
	m.maturedLabels++
	m.state = m.snapshot()
	return true
}

func (m *RelativeHoldRiskModel) appendTail(sample relativeTailSample) {
	if m.config.MaxTailSamples <= 0 {
		return
	}
	if len(m.tail) < m.config.MaxTailSamples {
		m.tail = append(m.tail, sample)
		return
	}
	copy(m.tail, m.tail[1:])
	m.tail[len(m.tail)-1] = sample
}

// Snapshot returns the latest matured state without mutating the model.
func (m *RelativeHoldRiskModel) Snapshot() RelativeHoldRiskState {
	if m == nil {
		return RelativeHoldRiskState{Reason: "nil relative-hold model"}
	}
	return m.state
}

// SnapshotAt returns a wall-clock-decayed view without mutating the online
// sufficient statistics. A global EWMA factor preserves O(1) model work; the
// explicit stale boundary is required because uniform decay leaves
// w^2/sum(w^2) unchanged and therefore cannot by itself invalidate readiness.
func (m *RelativeHoldRiskModel) SnapshotAt(now time.Time) RelativeHoldRiskState {
	if m == nil {
		return RelativeHoldRiskState{Reason: "nil relative-hold model"}
	}
	if now.IsZero() || m.lastMaturedAt.IsZero() || !now.After(m.lastMaturedAt) {
		return m.state
	}
	view := *m
	factor := math.Exp(-math.Ln2 * now.Sub(m.lastMaturedAt).Seconds() / m.config.HalfLife.Seconds())
	factor = math.Max(0, math.Min(1, factor))
	view.allExcess = m.allExcess
	view.allExcess.decay(factor)
	view.allReturns = m.allReturns
	view.allReturns.decay(factor)
	view.downside = m.downside
	view.downside.decay(factor)
	if now.Sub(m.lastMaturedAt) > m.config.HalfLife {
		view.tail = nil
	} else {
		view.tail = append([]relativeTailSample(nil), m.tail...)
	}
	state := view.snapshot()
	state.Age = now.Sub(m.lastMaturedAt)
	if state.Age > m.relativeHoldMaxAge() {
		state.Stale = true
		state.Ready = false
		state.DownsideReady = false
		state.TotalBetaReady = false
		state.Reason = fmt.Sprintf("relative-hold state stale; last matured label age %s", state.Age)
	}
	return state
}

func (m *RelativeHoldRiskModel) relativeHoldMaxAge() time.Duration {
	if m == nil {
		return 0
	}
	if m.config.HalfLife > m.config.Horizon {
		return 2 * m.config.HalfLife
	}
	return 2 * m.config.Horizon
}

func (m *RelativeHoldRiskModel) snapshot() RelativeHoldRiskState {
	mean, trackingVariance, shrinkage := m.allExcess.moments(
		m.config.PriorEffectiveSamples, m.config.PriorVarianceFraction)
	totalCovariance, totalHoldVariance, totalStrategyVariance, _ := m.allReturns.moments(
		m.config.PriorEffectiveSamples, m.config.PriorVarianceFraction)
	downsideCovariance, downsideHoldVariance, downsideStrategyVariance, downsideShrinkage := m.downside.moments(
		m.config.PriorEffectiveSamples, m.config.PriorDownsideVarianceFraction)
	downsideEffective := m.downside.effectiveSamples()
	state := RelativeHoldRiskState{
		LastMaturedAt:            m.lastMaturedAt,
		MaturedLabels:            m.maturedLabels,
		EffectiveSamples:         m.allExcess.effectiveSamples(),
		DownsideEffectiveSamples: downsideEffective,
		Shrinkage:                shrinkage,
		DownsideShrinkage:        downsideShrinkage,
		MeanExcessReturn:         mean,
		MeanExcessReturnBps:      mean * 10_000,
		TrackingVariance:         trackingVariance,
		TrackingError:            math.Sqrt(math.Max(0, trackingVariance)),
		DownsideCovariance:       downsideCovariance,
		DownsideHoldVariance:     downsideHoldVariance,
		DownsideStrategyVariance: downsideStrategyVariance,
		TargetDownsideBeta:       m.config.TargetDownsideBeta,
		TotalCovariance:          totalCovariance,
		TotalHoldVariance:        totalHoldVariance,
		TotalStrategyVariance:    totalStrategyVariance,
		TargetTotalBeta:          m.config.TargetTotalBeta,
	}
	state.TrackingErrorBps = state.TrackingError * 10_000
	if downsideHoldVariance > 1e-15 {
		state.DownsideBeta = downsideCovariance / downsideHoldVariance
		state.DownsideBetaUpper = state.DownsideBeta + m.config.ConfidenceZ*math.Sqrt(
			math.Max(0, downsideStrategyVariance-downsideCovariance*downsideCovariance/downsideHoldVariance)/
				math.Max(downsideEffective, 1))/
			math.Sqrt(downsideHoldVariance)
		state.DownsideBetaUpper = finiteOrZero(state.DownsideBetaUpper)
	}
	totalEffective := m.allReturns.effectiveSamples()
	if totalHoldVariance > 1e-15 {
		state.TotalBeta = totalCovariance / totalHoldVariance
		state.TotalBetaUpper = state.TotalBeta + m.config.ConfidenceZ*math.Sqrt(
			math.Max(0, totalStrategyVariance-totalCovariance*totalCovariance/totalHoldVariance)/
				math.Max(totalEffective, 1))/math.Sqrt(totalHoldVariance)
		state.TotalBetaUpper = finiteOrZero(state.TotalBetaUpper)
	}
	state.TotalBetaReady = totalEffective >= m.config.MinimumEffectiveSamples &&
		totalHoldVariance > 1e-15 && relativeFinite(state.TotalBeta)
	state.DownsideCVaR = relativeTailCVaR(m.tail, m.config.TailQuantile)
	state.DownsideCVaRBps = state.DownsideCVaR * 10_000
	state.Ready = state.EffectiveSamples >= m.config.MinimumEffectiveSamples &&
		state.MaturedLabels >= 2 && relativeFinite(state.TrackingVariance)
	state.DownsideReady = downsideEffective >= m.config.MinimumDownsideEffectiveSamples &&
		downsideHoldVariance > 1e-15 && relativeFinite(state.DownsideBeta)
	switch {
	case !state.Ready:
		state.Reason = "relative-hold tracking state warming up"
	case !state.DownsideReady:
		if !state.TotalBetaReady {
			state.Reason = "relative-hold state ready; total/downside beta warming up"
		} else {
			state.Reason = "relative-hold state ready; downside beta warming up"
		}
	case !state.TotalBetaReady:
		state.Reason = "relative-hold state ready; total beta warming up"
	default:
		state.Reason = "relative-hold tracking, total beta, and downside beta ready"
	}
	return state
}

func relativeTailCVaR(samples []relativeTailSample, quantile float64) float64 {
	if len(samples) == 0 {
		return 0
	}
	losses := make([]float64, 0, len(samples))
	for _, sample := range samples {
		if relativeFinite(sample.loss) {
			losses = append(losses, sample.loss)
		}
	}
	if len(losses) == 0 {
		return 0
	}
	sort.Float64s(losses)
	count := int(math.Ceil((1 - quantile) * float64(len(losses))))
	if count < 1 {
		count = 1
	}
	if count > len(losses) {
		count = len(losses)
	}
	sum := 0.0
	for _, loss := range losses[len(losses)-count:] {
		sum += loss
	}
	return sum / float64(count)
}

func relativeFinite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func finiteOrZero(value float64) float64 {
	if !relativeFinite(value) {
		return 0
	}
	return value
}
