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
	Enabled bool
	Healthy bool
	Reason  string

	PosteriorUpProbability     float64
	MicroSignedDirection       float64
	ExecutableSignedDirection  float64
	SignedDirection            float64
	DriftPerQV                 float64
	QVRatePerSecond            float64
	ForecastVariance           float64
	BaseForecastVariance       float64
	FastRiskForecastVariance   float64
	FastRiskBaselineVariance   float64
	FastRiskApplied            bool
	FastRiskDenominatorScale   float64
	FastRiskReentryCapApplied  bool
	BaseAimRatio               float64
	RiskAdjustedAimRatio       float64
	ForecastObservation        time.Duration
	ForecastReturn             float64
	EffectivePriorStrength     float64
	ContinuationMixtureApplied bool
	ContinuationCapApplied     bool
	ContinuationCapRatio       float64
	ForecastReturnSE           float64
	ForecastEdgeLowerBps       float64
	HoldProtectionApplied      bool

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

// FastBuyRestraintDecision projects the no-trade return posterior onto the
// Fast quote horizon. It only attenuates new BUY quantity; it never changes the
// inventory target, enlarges SELL quantity, or disables the bid through a
// directional hard gate.
type FastBuyRestraintDecision struct {
	Enabled               bool
	Reason                string
	ForecastReturnBps     float64
	ForecastReturnSEBps   float64
	AdverseProbability    float64
	ActivationProbability float64
	Restraint             float64
	BuyRetention          float64
}

// FastBuyRestraint computes the posterior probability that executable return
// over the Fast horizon is negative. Fast quote construction already charges
// round-trip fees and adverse selection in its price edge; charging those costs
// again here would double-count them. For a normal posterior,
// pAdverse = P(R_H < 0). Restraint begins only when pAdverse exceeds 1/2:
//
//	restraint = max(0, 2*pAdverse-1), retention = 1-restraint.
//
// Thus an uninformative posterior leaves Fast unchanged, while increasingly
// bearish evidence reduces only the proposed BUY notional continuously. The
// posterior-mean uncertainty scales linearly with the requested horizon because
// it is uncertainty in estimated drift, not realized-price innovation.
func (d NoTradeInventoryDecision) FastBuyRestraint(horizon time.Duration, confidenceZScore float64) FastBuyRestraintDecision {
	r := FastBuyRestraintDecision{Reason: "posterior unavailable", BuyRetention: 1}
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
	z := -mean / se
	adverseProbability := 0.5 * math.Erfc(-z/math.Sqrt2)
	adverseProbability = clampRatio(adverseProbability, 0, 1)
	zScore := confidenceZScore
	if zScore <= 0 {
		zScore = 1.6448536269514722
	}
	activationProbability := 0.5 * math.Erfc(-zScore/math.Sqrt2)
	activationProbability = clampRatio(activationProbability, 0.5, 1-1e-9)
	strength := math.Max(0, math.Min(1,
		(adverseProbability-activationProbability)/(1-activationProbability)))
	r.Enabled = strength > 0
	r.ForecastReturnBps = mean * 10_000
	r.ForecastReturnSEBps = se * 10_000
	r.AdverseProbability = adverseProbability
	r.ActivationProbability = activationProbability
	r.Restraint = strength
	r.BuyRetention = 1 - strength
	if r.Enabled {
		r.Reason = "bearish return posterior clears confidence threshold"
	} else {
		r.Reason = "bearish return posterior below confidence threshold"
	}
	return r
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
		PosteriorUpProbability: 0.5,
		AimRatio:               prior, LowerRatio: prior, UpperRatio: prior,
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
	if d.SignedDirection == 0 {
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
	z := c.HoldProtectionZScore
	if z <= 0 {
		z = 1.6448536269514722
	}
	thresholdBps := 2*math.Max(0, in.OneWayCostBps) + math.Max(0, c.HoldProtectionMinEdgeBps)
	if d.ForecastReturnSE <= 0 {
		// Missing uncertainty is not evidence of certainty.
		d.ForecastEdgeLowerBps = -math.Inf(1)
	} else {
		d.ForecastEdgeLowerBps = (math.Abs(d.ForecastReturn) - z*d.ForecastReturnSE) * 10_000
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
	if !d.Healthy {
		d.Reason = "signed crossing posterior unavailable; strategic-prior no-trade region"
	}
}
