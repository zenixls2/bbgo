package gammacapture

import "math"

// RegimeExpectedValueSizingConfig controls the replay-only quantity alpha
// that turns a regime-conditioned gross value estimate into a continuous
// position scale. It owns neither price selection nor side admission.
//
// The objective for a scale k in [0, MaxScale] is
//
//	U(k) = k * A - k^2 * R,
//
// where A is the regime-shrunk gross expected value less expected execution
// costs and R is a quadratic uncertainty/inventory-risk penalty. The fee is
// therefore part of the expected value function, but it is not a binary
// candidate filter. The venue minimum remains an execution constraint after
// this continuous decision.
type RegimeExpectedValueSizingConfig struct {
	Enabled                 bool
	PriorEffectiveSamples   float64
	RiskAversion            float64
	DirectionalStressWeight float64
	MakerFeeBps             float64
	AdverseSelectionBps     float64
	TurnoverBufferBps       float64
	MaxScale                float64
}

func (c *RegimeExpectedValueSizingConfig) setDefaults() {
	if c.PriorEffectiveSamples <= 0 || math.IsNaN(c.PriorEffectiveSamples) || math.IsInf(c.PriorEffectiveSamples, 0) {
		c.PriorEffectiveSamples = 8
	}
	if c.RiskAversion <= 0 || math.IsNaN(c.RiskAversion) || math.IsInf(c.RiskAversion, 0) {
		c.RiskAversion = 1
	}
	if c.DirectionalStressWeight < 0 || math.IsNaN(c.DirectionalStressWeight) || math.IsInf(c.DirectionalStressWeight, 0) {
		c.DirectionalStressWeight = 0.5
	}
	if c.MaxScale <= 0 || math.IsNaN(c.MaxScale) || math.IsInf(c.MaxScale, 0) {
		c.MaxScale = 1
	}
	c.MaxScale = math.Min(1, c.MaxScale)
	c.MakerFeeBps = math.Max(0, finiteRegimeSizing(c.MakerFeeBps))
	c.AdverseSelectionBps = math.Max(0, finiteRegimeSizing(c.AdverseSelectionBps))
	c.TurnoverBufferBps = math.Max(0, finiteRegimeSizing(c.TurnoverBufferBps))
}

// RegimeExpectedValueSizingInput is frozen at one causal quote timestamp.
// GrossExpectedValueJPY must be evaluated with exchange fees and the
// configured turnover buffer removed; the returned decision adds those costs
// exactly once. Notionals are the full candidate quantities before scaling.
type RegimeExpectedValueSizingInput struct {
	GrossExpectedValueJPY float64
	GrossStdErrorJPY      float64
	EffectiveSamples      float64
	RegimeReliability     float64
	RegimeDirectionScore  float64
	PairEquityJPY         float64
	BuyNotionalJPY        float64
	SellNotionalJPY       float64
	BuyFillProbability    float64
	SellFillProbability   float64
	BothFillProbability   float64
}

type RegimeExpectedValueSizingDecision struct {
	Ready                 bool
	Reason                string
	Scale                 float64
	GrossExpectedValueJPY float64
	RegimeShrink          float64
	ExpectedFeeJPY        float64
	ExpectedAdverseJPY    float64
	ExpectedTurnoverJPY   float64
	NetExpectedValueJPY   float64
	RiskPenaltyJPY        float64
	ExpectedUtilityJPY    float64
	ExpectedTouchedJPY    float64
	ExpectedCycleJPY      float64
	DirectionalStress     float64
}

func finiteRegimeSizing(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return value
}

func clampRegimeSizing(value, lower, upper float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return lower
	}
	return math.Max(lower, math.Min(upper, value))
}

// EvaluateRegimeExpectedValueSizing solves the bounded quadratic utility.
// Costs use expected touched notional, so a small oscillation that repeatedly
// touches only one side still pays its fee/adverse/turnover burden. The
// minimum-net-edge budget is intentionally represented as TurnoverBufferBps;
// it is not silently treated as extra alpha.
func EvaluateRegimeExpectedValueSizing(
	config RegimeExpectedValueSizingConfig,
	in RegimeExpectedValueSizingInput,
) RegimeExpectedValueSizingDecision {
	config.setDefaults()
	d := RegimeExpectedValueSizingDecision{Reason: "invalid or immature regime expected value"}
	if in.PairEquityJPY <= 0 || math.IsNaN(in.PairEquityJPY) || math.IsInf(in.PairEquityJPY, 0) {
		return d
	}
	if in.BuyNotionalJPY < 0 || in.SellNotionalJPY < 0 {
		return d
	}
	if math.IsNaN(in.GrossExpectedValueJPY) || math.IsInf(in.GrossExpectedValueJPY, 0) ||
		math.IsNaN(in.GrossStdErrorJPY) || math.IsInf(in.GrossStdErrorJPY, 0) ||
		in.EffectiveSamples <= 0 || math.IsNaN(in.EffectiveSamples) || math.IsInf(in.EffectiveSamples, 0) {
		return d
	}
	buyProbability := clampRegimeSizing(in.BuyFillProbability, 0, 1)
	sellProbability := clampRegimeSizing(in.SellFillProbability, 0, 1)
	bothProbability := clampRegimeSizing(in.BothFillProbability, 0, math.Min(buyProbability, sellProbability))
	reliability := clampRegimeSizing(in.RegimeReliability, 0, 1)
	direction := clampRegimeSizing(in.RegimeDirectionScore, -1, 1)
	regimeShrink := reliability * in.EffectiveSamples /
		(in.EffectiveSamples + config.PriorEffectiveSamples)
	if math.IsNaN(regimeShrink) || math.IsInf(regimeShrink, 0) || regimeShrink <= 0 {
		return d
	}

	expectedTouched := buyProbability*math.Max(0, in.BuyNotionalJPY) +
		sellProbability*math.Max(0, in.SellNotionalJPY)
	expectedCycle := bothProbability * math.Min(
		math.Max(0, in.BuyNotionalJPY), math.Max(0, in.SellNotionalJPY))
	expectedFee := expectedTouched * config.MakerFeeBps / 10_000
	expectedAdverse := expectedTouched * config.AdverseSelectionBps / 10_000
	expectedTurnover := expectedTouched * config.TurnoverBufferBps / 2 / 10_000
	regimeGross := in.GrossExpectedValueJPY * regimeShrink
	net := regimeGross - expectedFee - expectedAdverse - expectedTurnover

	directionalStress := 1 + config.DirectionalStressWeight*math.Abs(direction)*reliability
	standardError := math.Max(0, in.GrossStdErrorJPY)
	riskPenalty := config.RiskAversion * directionalStress * standardError * standardError /
		(2 * in.PairEquityJPY)
	if math.IsNaN(riskPenalty) || math.IsInf(riskPenalty, 0) || riskPenalty < 0 {
		return d
	}
	scale := 0.0
	if net > 0 {
		if riskPenalty > 0 {
			scale = net / (2 * riskPenalty)
		} else {
			scale = config.MaxScale
		}
		scale = clampRegimeSizing(scale, 0, config.MaxScale)
	}
	utility := scale*net - scale*scale*riskPenalty
	d.Ready = true
	d.Scale = scale
	d.GrossExpectedValueJPY = regimeGross
	d.RegimeShrink = regimeShrink
	d.ExpectedFeeJPY = expectedFee * scale
	d.ExpectedAdverseJPY = expectedAdverse * scale
	d.ExpectedTurnoverJPY = expectedTurnover * scale
	d.NetExpectedValueJPY = net
	d.RiskPenaltyJPY = riskPenalty * scale * scale
	d.ExpectedUtilityJPY = utility
	d.ExpectedTouchedJPY = expectedTouched * scale
	d.ExpectedCycleJPY = expectedCycle * scale
	d.DirectionalStress = directionalStress
	if scale <= 0 {
		d.Reason = "fee-and-risk-adjusted expected value is non-positive"
	} else if scale >= config.MaxScale-1e-12 {
		d.Reason = "positive regime expected value at maximum scale"
	} else {
		d.Reason = "continuous regime expected-value scale"
	}
	return d
}
