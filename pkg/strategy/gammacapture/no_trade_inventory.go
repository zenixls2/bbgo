package gammacapture

import (
	"math"
	"time"
)

// NoTradeInventoryConfig replaces the independently weighted Macro target and
// reversal overlays with one QV-time inventory aim and a proportional-cost
// no-trade region. The existing Macro return horizons remain available only
// for hard carrying-loss and drawdown constraints.
type NoTradeInventoryConfig struct {
	Enabled bool `json:"enabled" yaml:"enabled"`
	// DownsideRiskControlEnabled keeps the risk-adjusted inventory aim
	// independent from current holdings. The proportional-cost no-trade region
	// then owns reductions in long spot exposure; hold protection continues to
	// guard uncertain increases but cannot turn a risk target into w*=w.
	DownsideRiskControlEnabled bool `json:"downsideRiskControlEnabled" yaml:"downsideRiskControlEnabled"`
	// HoldProtectionEnabled blocks discretionary Macro movement unless the one-sided executable forecast lower bound clears round-trip cost.
	HoldProtectionEnabled      bool    `json:"holdProtectionEnabled" yaml:"holdProtectionEnabled"`
	HoldProtectionZScore       float64 `json:"holdProtectionZScore" yaml:"holdProtectionZScore"`
	HoldProtectionMinEdgeBps   float64 `json:"holdProtectionMinEdgeBps" yaml:"holdProtectionMinEdgeBps"`
	TrendExcursionEnabled      bool    `json:"trendExcursionEnabled" yaml:"trendExcursionEnabled"`
	ContinuationEnabled        bool    `json:"continuationEnabled" yaml:"continuationEnabled"`
	ContinuationMixtureEnabled bool    `json:"continuationMixtureEnabled" yaml:"continuationMixtureEnabled"`
	// FastVarianceRiskEnabled lets the online side-specific HAR model replace
	// only the risk variance in the unified Merton/no-trade controller. Crossing
	// evidence remains the sole source of expected return.
	FastVarianceRiskEnabled bool `json:"fastVarianceRiskEnabled" yaml:"fastVarianceRiskEnabled"`
}

// NoTradeInventoryInput contains the sufficient statistics for the one-state
// controller. Crossing signs estimate drift per unit quadratic variation;
// executable ask/bid volatility supplies the side-specific QV rates.
type NoTradeInventoryInput struct {
	CurrentRiskyWeight float64
	PriorTargetRatio   float64
	PolicyMinRatio     float64
	PolicyMaxRatio     float64

	CrossingUp                        int
	CrossingDown                      int
	CrossingHealth                    ModelHealth
	BarrierWidth                      float64
	Observed                          time.Duration
	DriftPriorSamples                 float64
	CrossingQVRatePerSecond           float64
	ExecutableCrossingUp              int
	ExecutableCrossingDown            int
	ExecutableCrossingHealth          ModelHealth
	ExecutableObserved                time.Duration
	ExecutableCrossingQVRatePerSecond float64
	FastRiskVarianceRatePerSecond     float64
	FastRiskBaselineRatePerSecond     float64
	FastRiskHorizon                   time.Duration
	FastRiskHealthy                   bool
	FastRiskElevated                  bool

	BuyVolatilityBpsPerSqrtSec  float64
	SellVolatilityBpsPerSqrtSec float64
	RiskAversion                float64
	PriorStrength               float64
	OneWayCostBps               float64

	PairEquityJPY                float64
	MinimumExecutableNotionalJPY float64
	TrendExcursion               TrendExcursionDecision
	TrendContinuation            TrendContinuationDecision
}

// NoTradeInventoryDecision separates the frictionless/QV-time aim from the
// execution target. Inside the no-trade region the execution target equals the
// current inventory, so the portfolio controller adds no turnover. Outside it,
// the target is the nearest boundary, which is the discrete maker-order
// approximation of boundary-local-time reflection.
type NoTradeInventoryDecision struct {
	Enabled                    bool
	DownsideRiskControlEnabled bool
	Healthy                    bool
	Reason                     string

	PosteriorUpProbability       float64
	MicroSignedDirection         float64
	ExecutableSignedDirection    float64
	SignedDirection              float64
	DriftPerQV                   float64
	QVRatePerSecond              float64
	ForecastVariance             float64
	BaseForecastVariance         float64
	FastRiskForecastVariance     float64
	FastRiskBaselineVariance     float64
	FastRiskApplied              bool
	FastRiskDenominatorScale     float64
	FastRiskReentryCapApplied    bool
	BaseAimRatio                 float64
	RiskAdjustedAimRatio         float64
	ForecastObservation          time.Duration
	ForecastReturn               float64
	EffectivePriorStrength       float64
	ContinuationMixtureApplied   bool
	ContinuationCapApplied       bool
	ContinuationCapRatio         float64
	ForecastReturnSE             float64
	ForecastEdgeLowerBps         float64
	HoldProtectionApplied        bool
	HoldProtectionPreservesAim   bool
	HoldProtectionEnabled        bool
	HoldProtectionThresholdBps   float64
	RiskReductionGrossUtilityBps float64
	RiskReductionNetUtilityBps   float64

	// RawAimRatio is the instantaneous Merton/QV-time observation. AimRatio is
	// the causal, posterior-variance-filtered center used by live execution.
	RawAimRatio            float64
	AimRatio               float64
	AimMeasurementVariance float64
	AimFilterVariance      float64
	AimKalmanGain          float64
	AimUpdatedAt           time.Time
	LowerRatio             float64
	UpperRatio             float64
	ExecutionTargetRatio   float64
	Direction              int
	BuyHalfWidthRatio      float64
	SellHalfWidthRatio     float64
	TrendExcursion         TrendExcursionDecision
	TrendContinuation      TrendContinuationDecision
}

// FastReservationDecision projects the no-trade return posterior onto the
// Fast quote horizon. It adds only signed long-horizon drift to Fast's unified
// reservation price; it never changes the inventory target, gross risk budget,
// quote quantity, or side gates.
type FastReservationDecision struct {
	Enabled                bool
	Reason                 string
	Direction              int
	ForecastReturnBps      float64
	ForecastReturnSEBps    float64
	AdverseProbability     float64
	DirectionalProbability float64
	ActivationProbability  float64
	Strength               float64
	PathEfficiency         float64
	ReservationShiftBps    float64
}

// FastReservation projects the signed executable-return posterior onto the
// Fast horizon and adds it to Fast's existing reservation price,
//
//	log(r/m) = eta*mu_H - gamma*sigma_H^2*(w-w*).
//
// Quote already owns the second term, including current inventory and target.
// This function supplies only eta*mu_H, preventing inventory risk from being
// counted twice. eta is path efficiency (net log displacement over total log
// variation), which tends to zero in a range without a fitted regime threshold.
// Posterior confidence remains diagnostic rather than becoming a hard side gate.
func (d NoTradeInventoryDecision) FastReservation(horizon time.Duration, confidenceZScore float64) FastReservationDecision {
	r := FastReservationDecision{Reason: "posterior unavailable"}
	if d.Direction != 0 {
		r.Reason = "active no-trade boundary correction owns inventory adjustment"
		return r
	}
	if !d.Enabled || !d.Healthy || horizon <= 0 || d.ForecastObservation <= 0 ||
		d.ForecastReturnSE <= 0 || math.IsNaN(d.ForecastReturn) || math.IsInf(d.ForecastReturn, 0) ||
		math.IsNaN(d.ForecastReturnSE) || math.IsInf(d.ForecastReturnSE, 0) {
		return r
	}
	scale := horizon.Seconds() / d.ForecastObservation.Seconds()
	mean := d.ForecastReturn * scale
	se := d.ForecastReturnSE * scale
	if scale <= 0 || se <= 0 || math.IsNaN(se) || math.IsInf(se, 0) {
		return r
	}
	adverseProbability := 0.5 * math.Erfc(mean/(se*math.Sqrt2))
	adverseProbability = clampRatio(adverseProbability, 0, 1)
	directionalProbability := math.Max(adverseProbability, 1-adverseProbability)
	zScore := confidenceZScore
	if zScore <= 0 {
		zScore = 1.6448536269514722
	}
	activationProbability := 0.5 * math.Erfc(-zScore/math.Sqrt2)
	activationProbability = clampRatio(activationProbability, 0.5, 1-1e-9)
	strength := math.Max(0, math.Min(1,
		(directionalProbability-activationProbability)/(1-activationProbability)))
	pathEfficiency := 1.0
	if d.TrendContinuation.Healthy {
		pathEfficiency = 1 - clampRatio(d.TrendContinuation.ConsolidationScore, 0, 1)
	}
	r.Enabled = mean != 0 && pathEfficiency > 0
	if mean > 0 {
		r.Direction = 1
	} else if mean < 0 {
		r.Direction = -1
	}
	r.ForecastReturnBps = mean * 10_000
	r.ForecastReturnSEBps = se * 10_000
	r.AdverseProbability = adverseProbability
	r.DirectionalProbability = directionalProbability
	r.ActivationProbability = activationProbability
	r.Strength = strength
	r.PathEfficiency = pathEfficiency
	if r.Enabled {
		r.ReservationShiftBps = r.ForecastReturnBps * pathEfficiency
		r.Reason = "signed posterior drift shifts the unified Fast reservation price"
	} else {
		r.Reason = "posterior mean is neutral or path has no directional efficiency"
	}
	return r
}

// FastReservationRiskHorizon uses the causal horizon of the return posterior
// that moves the reservation price. Public crossing intensity is deliberately
// excluded: it is neither private fill intensity nor an identified liquidation
// time, and shortening holding risk by its inverse would be a Poisson-style
// assumption unsupported by the capture. The Fast horizon remains the fallback.
func FastReservationRiskHorizon(fastHorizon, forecastHorizon time.Duration) time.Duration {
	if forecastHorizon > 0 {
		return forecastHorizon
	}
	if fastHorizon > 0 {
		return fastHorizon
	}
	return 0
}

// ApplyFastReservationShift moves both Fast quotes by the signed posterior
// drift. The inward target-side coordinate is projected onto current maker
// touch; the opposite coordinate continues to express the common reservation.
// Side gates, notionals and order lifetime remain exactly those chosen by Fast.
func ApplyFastReservationShift(
	plan MarketMakerQuotePlan, decision FastReservationDecision,
	mid, bestBid, bestAsk float64,
) MarketMakerQuotePlan {
	if !decision.Enabled || decision.ReservationShiftBps == 0 ||
		plan.BidPrice <= 0 || plan.AskPrice <= plan.BidPrice || mid <= 0 ||
		bestBid <= 0 || bestAsk < bestBid {
		return plan
	}
	scale := math.Exp(decision.ReservationShiftBps / 10_000)
	adjustedBid := plan.BidPrice * scale
	adjustedAsk := plan.AskPrice * scale
	if decision.ReservationShiftBps > 0 {
		adjustedBid = math.Min(bestBid, adjustedBid)
	} else {
		adjustedAsk = math.Max(bestAsk, adjustedAsk)
	}
	if adjustedBid <= 0 || adjustedAsk <= adjustedBid {
		return plan
	}
	plan.BidPrice = adjustedBid
	plan.AskPrice = adjustedAsk
	plan.BidDistanceBps = math.Max(0, math.Log(mid/adjustedBid)*10_000)
	plan.AskDistanceBps = math.Max(0, math.Log(adjustedAsk/mid)*10_000)
	plan.BidHalfSpreadBps = plan.BidDistanceBps
	plan.AskHalfSpreadBps = plan.AskDistanceBps
	plan.HalfSpreadBps = math.Max(plan.BidHalfSpreadBps, plan.AskHalfSpreadBps)
	plan.BidTouchDistanceBps, plan.AskTouchDistanceBps, _ =
		MakerTouchDistances(bestBid, bestAsk, plan.BidPrice, plan.AskPrice)
	return plan
}

type FastReservationUtilityDecision struct {
	Applied             bool
	Reason              string
	EffectiveSamples    float64
	ExpectedPnLJPY      float64
	StdErrorJPY         float64
	CertaintyEquivalent float64
}

// SelectFastReservationPlan applies signed drift only when one exchange-minimum
// fill on its target side has positive mean and risk-adjusted terminal wealth.
// Insufficient or rejected evidence returns base unchanged.
func SelectFastReservationPlan(
	model *MarketMakerHorizonModel,
	config MarketMakerConfig,
	now time.Time,
	horizon time.Duration,
	base MarketMakerQuotePlan,
	decision FastReservationDecision,
	mid, bestBid, bestAsk, executableUnitJPY, pairEquityJPY, riskAversion float64,
) (MarketMakerQuotePlan, FastReservationUtilityDecision) {
	u := FastReservationUtilityDecision{Reason: "signed reservation candidate unavailable"}
	if !decision.Enabled || decision.ReservationShiftBps == 0 {
		return base, u
	}
	candidate := ApplyFastReservationShift(base, decision, mid, bestBid, bestAsk)
	if candidate.BidPrice == base.BidPrice && candidate.AskPrice == base.AskPrice {
		u.Reason = "signed reservation candidate unchanged"
		return base, u
	}
	if model == nil || now.IsZero() || horizon <= 0 || executableUnitJPY <= 0 || pairEquityJPY <= 0 {
		u.Reason = "terminal path utility unavailable"
		return base, u
	}
	stats := model.JointPathPayoffStatistics(
		now, config, horizon, candidate.BidTouchDistanceBps, candidate.AskTouchDistanceBps)
	u.EffectiveSamples = stats.EffectiveSamples
	if stats.EffectiveSamples <= 1 {
		u.Reason = "terminal path dispersion not identifiable"
		return base, u
	}
	var payoff JointPathPayoffDecision
	if decision.ReservationShiftBps > 0 {
		payoff = stats.Evaluate(executableUnitJPY, 0, pairEquityJPY, riskAversion, 0)
	} else {
		payoff = stats.Evaluate(0, executableUnitJPY, pairEquityJPY, riskAversion, 0)
	}
	u.ExpectedPnLJPY = payoff.ExpectedPnLJPY
	u.StdErrorJPY = payoff.StdErrorJPY
	u.CertaintyEquivalent = payoff.CertaintyEquivalent
	if payoff.ExpectedPnLJPY <= 0 || payoff.CertaintyEquivalent <= 0 {
		u.Reason = "target-side terminal path utility is not positive"
		return base, u
	}
	u.Applied = true
	u.Reason = "signed reservation candidate has positive terminal path utility"
	return candidate, u
}

// FastReservationRealignmentRequired reprices immediately only when an accepted
// drift changes sign or grows by at least one exchange tick. A shrinking signal
// keeps queue priority and waits for Fast's normal review window.
func FastReservationRealignmentRequired(candidateBps, quotedBps, tickBps float64) bool {
	if candidateBps == 0 || math.IsNaN(candidateBps) || math.IsInf(candidateBps, 0) {
		return false
	}
	threshold := math.Max(1e-9, tickBps)
	if candidateBps*quotedBps < 0 {
		return math.Abs(candidateBps-quotedBps) > threshold
	}
	return math.Abs(candidateBps) > math.Abs(quotedBps)+threshold
}

func clampRatio(value, minimum, maximum float64) float64 {
	return math.Max(minimum, math.Min(maximum, value))
}

// ExecutionEdgeBps projects the QV-time forecast onto a shorter execution
// horizon for the bounded active-correction test.
func (d NoTradeInventoryDecision) ExecutionEdgeBps(horizon time.Duration) float64 {
	if horizon <= 0 || d.ForecastObservation <= 0 || d.ForecastReturn == 0 {
		return 0
	}
	return d.ForecastReturn * horizon.Seconds() / d.ForecastObservation.Seconds() * 10_000
}

// EvaluateNoTradeInventory derives a horizon-free Merton direction from a
// signed first-passage posterior, then expresses the existing strategic prior
// over the amount of QV actually observed. No 3h/6h/24h target weights enter.
//
// For dX = theta dA + dB_A and symmetric barriers +/-h,
// P(+h first) = logistic(2 theta h). Hence theta = atanh(2p-1)/h.
// The no-trade half widths use the reflected-Brownian tracking approximation
// delta^3 = 3 c a_w / (2 g), with side QV in a_w and utility curvature g.
func EvaluateNoTradeInventory(c NoTradeInventoryConfig, in NoTradeInventoryInput) NoTradeInventoryDecision {
	minimum := math.Max(0, math.Min(1, in.PolicyMinRatio))
	maximum := math.Max(minimum, math.Min(1, in.PolicyMaxRatio))
	prior := clampRatio(in.PriorTargetRatio, minimum, maximum)
	current := clampRatio(in.CurrentRiskyWeight, minimum, maximum)
	d := NoTradeInventoryDecision{
		Enabled: c.Enabled, Reason: "disabled",
		DownsideRiskControlEnabled: c.DownsideRiskControlEnabled,
		PosteriorUpProbability:     0.5,
		AimRatio:                   prior, LowerRatio: prior, UpperRatio: prior,
		ExecutionTargetRatio:   current,
		EffectivePriorStrength: in.PriorStrength,
	}
	if !c.Enabled {
		return d
	}
	if in.RiskAversion <= 0 || in.PriorStrength <= 0 || in.Observed <= 0 ||
		in.BuyVolatilityBpsPerSqrtSec <= 0 || in.SellVolatilityBpsPerSqrtSec <= 0 {
		d.Reason = "insufficient QV-time inputs"
		return d
	}

	events := in.CrossingUp + in.CrossingDown
	executableEvents := in.ExecutableCrossingUp + in.ExecutableCrossingDown
	priorSamples := math.Max(2, in.DriftPriorSamples)
	// Microprice is a common latent-price signal, while bid-up/ask-down events
	// certify that the same direction exists in the executable opportunity set.
	// They are correlated views, so do not add their counts as if independent.
	// The conservative posterior intersection becomes neutral on disagreement
	// and otherwise retains only the weaker signed deviation.
	d.MicroSignedDirection, d.ExecutableSignedDirection, d.SignedDirection =
		ConservativeConfirmedDirection(
			in.CrossingUp, in.CrossingDown,
			in.ExecutableCrossingUp, in.ExecutableCrossingDown,
			priorSamples)
	d.PosteriorUpProbability = clampRatio((1+d.SignedDirection)/2, 1e-9, 1-1e-9)
	if in.BarrierWidth > 0 {
		d.DriftPerQV = math.Atanh(d.SignedDirection) / in.BarrierWidth
	}

	d.QVRatePerSecond = math.Min(
		math.Max(0, in.CrossingQVRatePerSecond),
		math.Max(0, in.ExecutableCrossingQVRatePerSecond))
	observed := in.Observed
	if in.ExecutableObserved > 0 && (observed <= 0 || in.ExecutableObserved < observed) {
		observed = in.ExecutableObserved
	}
	d.BaseForecastVariance = d.QVRatePerSecond * observed.Seconds()
	d.ForecastVariance = d.BaseForecastVariance
	d.ForecastReturn = d.DriftPerQV * d.BaseForecastVariance
	d.ForecastObservation = observed
	if c.FastVarianceRiskEnabled && in.FastRiskHealthy &&
		in.FastRiskVarianceRatePerSecond > 0 && in.FastRiskHorizon > 0 {
		// Convert both forecasts to the same observation horizon before taking
		// the conservative envelope. Do not multiply the QV drift by the HAR
		// variance: doing so would manufacture expected return from risk.
		d.FastRiskForecastVariance = in.FastRiskVarianceRatePerSecond * observed.Seconds()
		d.FastRiskBaselineVariance = math.Max(0, in.FastRiskBaselineRatePerSecond) * observed.Seconds()
		if d.FastRiskForecastVariance > d.ForecastVariance {
			d.ForecastVariance = d.FastRiskForecastVariance
			d.FastRiskApplied = true
		}
	}
	baseDenominator := in.RiskAversion*d.BaseForecastVariance + in.PriorStrength
	denominator := in.RiskAversion*d.ForecastVariance + in.PriorStrength
	d.BaseAimRatio = prior
	if d.SignedDirection != 0 && baseDenominator > 0 {
		d.BaseAimRatio = clampRatio(
			(d.ForecastReturn+in.PriorStrength*prior)/baseDenominator,
			minimum, maximum)
	}
	if d.SignedDirection == 0 && !c.DownsideRiskControlEnabled {
		// The robust posterior identified set contains zero. Without a confirmed
		// directional premium, variance alone must widen the no-trade region, not
		// silently drag the strategic allocation below its configured prior.
		d.AimRatio = prior
		if c.FastVarianceRiskEnabled && in.FastRiskHealthy && in.FastRiskElevated && current < prior {
			// A statistically confirmed high-risk consolidation carries no signed
			// premium. Keep existing exposure, but do not prematurely refill toward
			// the strategic prior while the next move is unresolved.
			d.AimRatio = current
			d.FastRiskReentryCapApplied = true
		}
	} else if denominator > 0 {
		// This is the regularized Merton target. In downside-risk mode it also
		// applies when the signed mean is zero: uncertainty lowers the optimal
		// long-only exposure instead of being silently discarded. The
		// proportional-cost boundary prevents variance noise from creating turnover.
		d.AimRatio = clampRatio(
			(d.ForecastReturn+in.PriorStrength*prior)/denominator,
			minimum, maximum)
	}
	qvHealthy := in.CrossingHealth == HealthHealthy && in.ExecutableCrossingHealth == HealthHealthy &&
		events > 0 && executableEvents > 0 && in.BarrierWidth > 0 && d.QVRatePerSecond > 0
	d.Healthy = qvHealthy
	if !qvHealthy {
		// Insufficient signed evidence cannot move the strategic center. Reset
		// before solving the boundaries so stale drift cannot alter either the
		// center or the state-dependent diffusion scale.
		d.DriftPerQV = 0
		d.ForecastReturn = 0
		d.BaseAimRatio = prior
		d.AimRatio = prior
		if c.FastVarianceRiskEnabled && in.FastRiskHealthy && in.FastRiskElevated && current < prior {
			d.AimRatio = current
			d.FastRiskReentryCapApplied = true
		}
	}
	d.TrendExcursion = in.TrendExcursion
	d.TrendContinuation = in.TrendContinuation
	if in.TrendExcursion.Healthy {
		// The structural trend mixture owns both mean and variance. The HAR/QV
		// denominator separation is currently validated only for QV-only mode.
		d.FastRiskApplied = false
		d.FastRiskDenominatorScale = 0
		trend := in.TrendExcursion
		forecastSeconds := trend.ForecastHorizon.Seconds()
		qvMean := 0.0
		qvVariance := 0.0
		if qvHealthy && forecastSeconds > 0 {
			qvVariance = d.QVRatePerSecond * forecastSeconds
			qvMean = d.DriftPerQV * qvVariance
		}
		probability := clampRatio(trend.ModelProbability, 0, 1)
		trendMean := trend.ExpectedReturn
		trendVariance := math.Max(0, trend.ReturnVariance)
		mixtureMean := (1-probability)*qvMean + probability*trendMean
		mixtureVariance := (1-probability)*qvVariance + probability*trendVariance +
			probability*(1-probability)*math.Pow(trendMean-qvMean, 2)
		d.ForecastObservation = trend.ForecastHorizon
		d.ForecastReturn = mixtureMean
		d.ForecastVariance = mixtureVariance
		denominator = in.RiskAversion*mixtureVariance + in.PriorStrength
		if denominator > 0 {
			d.AimRatio = clampRatio(
				(mixtureMean+in.PriorStrength*prior)/denominator,
				minimum, maximum)
		}
		// The model probability is itself uncertain. Treat model disagreement as
		// measurement uncertainty instead of adding a second target controller.
		qvMeanVariance := 0.0
		if qvHealthy {
			qvDenominator := in.RiskAversion*qvVariance + in.PriorStrength
			qvAimVariance := noTradeAimMeasurementVariance(
				in, d.SignedDirection, qvVariance,
				qvDenominator, minimum, maximum)
			qvMeanVariance = qvAimVariance * qvDenominator * qvDenominator
		}
		meanMeasurementVariance := math.Pow(1-probability, 2)*qvMeanVariance +
			math.Pow(probability, 2)*math.Pow(trend.MeanSE, 2) +
			probability*(1-probability)*math.Pow(trendMean-qvMean, 2)
		d.AimMeasurementVariance = math.Min(
			math.Pow(maximum-minimum, 2), meanMeasurementVariance/math.Pow(denominator, 2))
		d.BaseAimRatio = d.AimRatio
		d.Healthy = true
	}
	d = applyContinuationMixture(c, in, d, minimum, maximum)
	continuation := in.TrendContinuation
	if continuation.Healthy && !c.ContinuationMixtureEnabled && continuation.RecentDirection < 0 &&
		continuation.ExpectedReturn < 0 {
		// This is a constrained Kalman/Merton state, not another inventory
		// target. A negative posterior-predictive executable return means that
		// increasing long exposure is dominated by waiting in cash. Project the
		// aim onto the convex set w <= current until the continuation posterior
		// turns non-negative. Existing QV/structural evidence may still reduce
		// inventory, and the no-trade region still prices that turnover.
		d.ContinuationCapApplied = true
		d.ContinuationCapRatio = current
		d.AimRatio = math.Min(d.AimRatio, current)
	}
	d.RiskAdjustedAimRatio = d.AimRatio
	d.RawAimRatio = d.AimRatio
	if d.FastRiskApplied && qvHealthy && !in.TrendExcursion.Healthy &&
		!d.ContinuationCapApplied && d.SignedDirection != 0 && denominator > baseDenominator && baseDenominator > 0 {
		d.FastRiskDenominatorScale = baseDenominator / denominator
		d.RawAimRatio = d.BaseAimRatio
	}
	if qvHealthy && !in.TrendExcursion.Healthy && !d.ContinuationMixtureApplied {
		d.AimMeasurementVariance = noTradeAimMeasurementVariance(
			in, d.SignedDirection, d.BaseForecastVariance, baseDenominator, minimum, maximum)
	}
	if denominator := in.RiskAversion*d.ForecastVariance + in.PriorStrength; denominator > 0 && d.AimMeasurementVariance > 0 {
		d.ForecastReturnSE = math.Sqrt(d.AimMeasurementVariance) * denominator
	}
	applyNoTradeHoldProtection(&d, c, in, minimum, maximum, current)
	materializeNoTradeInventory(&d, in, minimum, maximum, current)
	return d
}

// applyNoTradeHoldProtection is a one-sided posterior safety gate. Let m be
// the executable log-return forecast and s its standard error. A discretionary
// inventory increase requires m-zs >= 2c+e; a decrease requires -m-zs >= 2c+e,
// where c is one-way fee/adverse-selection cost and e is the residual edge.
// The gate prevents an unconfident directional adjustment; it cannot promise an
// unknowable future path.
func applyNoTradeHoldProtection(d *NoTradeInventoryDecision, c NoTradeInventoryConfig, in NoTradeInventoryInput, minimum, maximum, current float64) {
	if d == nil || !c.HoldProtectionEnabled || !d.Healthy {
		return
	}
	d.HoldProtectionEnabled = true
	d.HoldProtectionPreservesAim = c.DownsideRiskControlEnabled
	z := c.HoldProtectionZScore
	if z <= 0 {
		z = 1.6448536269514722
	}
	thresholdBps := 2*math.Max(0, in.OneWayCostBps) + math.Max(0, c.HoldProtectionMinEdgeBps)
	d.HoldProtectionThresholdBps = thresholdBps
	if d.ForecastReturnSE <= 0 {
		// Missing uncertainty is not evidence of certainty.
		d.ForecastEdgeLowerBps = -math.Inf(1)
	} else {
		d.ForecastEdgeLowerBps = (math.Abs(d.ForecastReturn) - z*d.ForecastReturnSE) * 10_000
	}
	if c.DownsideRiskControlEnabled {
		applyRiskAwareHoldProtection(d, in, current)
		return
	}
	needIncrease := d.AimRatio > current+1e-12
	needDecrease := d.AimRatio < current-1e-12
	passes := (needIncrease && d.ForecastReturn > 0 && d.ForecastEdgeLowerBps >= thresholdBps) ||
		(needDecrease && d.ForecastReturn < 0 && d.ForecastEdgeLowerBps >= thresholdBps)
	if (!needIncrease && !needDecrease) || passes {
		return
	}
	d.HoldProtectionApplied = true
	d.AimRatio = clampRatio(current, minimum, maximum)
	d.RawAimRatio = d.AimRatio
	d.RiskAdjustedAimRatio = d.AimRatio
	d.Reason = "hold-protection: executable forecast lower bound below round-trip cost"
}

// applyRiskAwareHoldProtection compares the regularized Merton objective at
// current holdings and the independent latent aim. A risk-reducing SELL may
// bypass the posterior-mean fee gate only when its utility gain also clears the
// one-way execution cost; the cost-derived no-trade boundary remains the final
// turnover control.
func applyRiskAwareHoldProtection(d *NoTradeInventoryDecision, in NoTradeInventoryInput, current float64) {
	d.HoldProtectionApplied = false
	needIncrease := d.AimRatio > current+1e-12
	needDecrease := d.AimRatio < current-1e-12
	if !needIncrease && !needDecrease {
		return
	}
	if needDecrease {
		prior := in.PriorTargetRatio
		utility := func(weight float64) float64 {
			return weight*d.ForecastReturn -
				0.5*in.RiskAversion*d.ForecastVariance*weight*weight -
				0.5*in.PriorStrength*(weight-prior)*(weight-prior)
		}
		gross := utility(d.AimRatio) - utility(current)
		cost := math.Max(0, in.OneWayCostBps) / 10_000 * math.Abs(d.AimRatio-current)
		d.RiskReductionGrossUtilityBps = gross * 10_000
		d.RiskReductionNetUtilityBps = (gross - cost) * 10_000
		if gross > cost {
			return
		}
	}
	passes := (needIncrease && d.ForecastReturn > 0 &&
		d.ForecastEdgeLowerBps >= d.HoldProtectionThresholdBps) ||
		(needDecrease && d.ForecastReturn < 0 &&
			d.ForecastEdgeLowerBps >= d.HoldProtectionThresholdBps)
	if passes {
		return
	}
	d.HoldProtectionApplied = true
	if needIncrease {
		d.Reason = "hold-protection: inventory increase lower bound below round-trip cost"
	} else {
		d.Reason = "hold-protection: risk-reduction utility below execution cost"
	}
}

// noTradeAimMeasurementVariance propagates the symmetric Beta posterior for
// the first-passage sign through theta=atanh(d)/h and the Merton aim. The two
// crossing paths are correlated, so the less precise variance is retained
// instead of pretending that their samples are independent.
func noTradeAimMeasurementVariance(in NoTradeInventoryInput, signedDirection, driftVariance, denominator, minimum, maximum float64) float64 {
	if in.BarrierWidth <= 0 || denominator <= 0 || maximum <= minimum {
		return 0
	}
	priorSamples := math.Max(2, in.DriftPriorSamples)
	signedVariance := func(up, down int) float64 {
		if up < 0 {
			up = 0
		}
		if down < 0 {
			down = 0
		}
		a := priorSamples/2 + float64(up)
		b := priorSamples/2 + float64(down)
		total := a + b
		if total <= 0 {
			return 0
		}
		// Var(2p-1)=4 Var(p), p~Beta(a,b).
		return 4 * a * b / (total * total * (total + 1))
	}
	directionVariance := math.Max(
		signedVariance(in.CrossingUp, in.CrossingDown),
		signedVariance(in.ExecutableCrossingUp, in.ExecutableCrossingDown))
	// Only crossing QV creates uncertain expected return. An independent HAR risk
	// forecast belongs in denominator; treating it as direction measurement noise
	// would lower the Kalman gain precisely when inventory must react faster.
	jacobian := driftVariance /
		(in.BarrierWidth * math.Max(1e-9, 1-signedDirection*signedDirection) * denominator)
	variance := jacobian * jacobian * directionVariance
	return math.Min(variance, math.Pow(maximum-minimum, 2))
}

// materializeNoTradeInventory maps a chosen aim directly into the
// cost-derived no-trade boundaries used by live execution, replay, and tests.
func materializeNoTradeInventory(d *NoTradeInventoryDecision, in NoTradeInventoryInput, minimum, maximum, current float64) {
	if d == nil {
		return
	}
	buyVol := in.BuyVolatilityBpsPerSqrtSec / 10_000
	sellVol := in.SellVolatilityBpsPerSqrtSec / 10_000
	buyQVRate := buyVol * buyVol
	sellQVRate := sellVol * sellVol
	seconds := in.Observed.Seconds()
	curvaturePerSecond := d.QVRatePerSecond*in.RiskAversion + in.PriorStrength/seconds
	oneWayCost := math.Max(0, in.OneWayCostBps) / 10_000
	weightDiffusionScale := math.Pow(d.AimRatio*(1-d.AimRatio), 2)
	halfWidth := func(sideQVRate float64) float64 {
		if oneWayCost <= 0 || sideQVRate <= 0 || curvaturePerSecond <= 0 {
			return 0
		}
		return math.Cbrt(3 * oneWayCost * weightDiffusionScale * sideQVRate /
			(2 * curvaturePerSecond))
	}
	d.BuyHalfWidthRatio = halfWidth(buyQVRate)
	d.SellHalfWidthRatio = halfWidth(sellQVRate)
	// One executable fill plus half a quantity cell is the smallest meaningful
	// discrete approximation of a continuous reflecting boundary.
	if in.PairEquityJPY > 0 && in.MinimumExecutableNotionalJPY > 0 {
		minimumWidth := 1.5 * in.MinimumExecutableNotionalJPY / in.PairEquityJPY
		d.BuyHalfWidthRatio = math.Max(d.BuyHalfWidthRatio, minimumWidth)
		d.SellHalfWidthRatio = math.Max(d.SellHalfWidthRatio, minimumWidth)
	}
	d.LowerRatio = math.Max(minimum, d.AimRatio-d.BuyHalfWidthRatio)
	d.UpperRatio = math.Min(maximum, d.AimRatio+d.SellHalfWidthRatio)
	if d.UpperRatio < d.LowerRatio {
		d.LowerRatio, d.UpperRatio = d.AimRatio, d.AimRatio
	}

	switch {
	case current < d.LowerRatio:
		d.Direction = 1
		d.ExecutionTargetRatio = d.LowerRatio
		d.Reason = "below no-trade region"
	case current > d.UpperRatio:
		d.Direction = -1
		d.ExecutionTargetRatio = d.UpperRatio
		d.Reason = "above no-trade region"
	default:
		d.Direction = 0
		d.ExecutionTargetRatio = clampRatio(current, d.LowerRatio, d.UpperRatio)
		d.Reason = "inside no-trade region"
	}
	if d.HoldProtectionApplied && d.HoldProtectionPreservesAim {
		// Keep the execution band internally ordered around current holdings while
		// preserving AimRatio as the independent latent state. Otherwise lower >
		// execution-target makes the Fast allocator buy despite a protected increase.
		d.LowerRatio = math.Max(minimum, current-d.BuyHalfWidthRatio)
		d.UpperRatio = math.Min(maximum, current+d.SellHalfWidthRatio)
		d.Direction = 0
		d.ExecutionTargetRatio = current
		d.Reason = "hold-protection: inventory increase lower bound below round-trip cost"
	}
	if !d.Healthy {
		d.Reason = "signed crossing posterior unavailable; strategic-prior no-trade region"
	}
}
