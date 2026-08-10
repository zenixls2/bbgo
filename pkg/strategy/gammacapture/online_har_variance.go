package gammacapture

import "math"

const HARVarianceFeatureCount = 5

// HARVarianceFeatures are dimensionless mixed-frequency statistics. Variance
// rates, rather than integrated variances, make 15/30/long windows comparable.
type HARVarianceFeatures struct {
	ShortRate     float64
	MediumRate    float64
	LongRate      float64
	DownsideShare float64
	JumpFraction  float64
}

func (f HARVarianceFeatures) Vector() ([HARVarianceFeatureCount]float64, bool) {
	var out [HARVarianceFeatureCount]float64
	if f.ShortRate <= 0 || f.MediumRate <= 0 || f.LongRate <= 0 {
		return out, false
	}
	out[0] = 1
	out[1] = clampFinite(math.Log(f.ShortRate/f.LongRate), -4, 4)
	out[2] = clampFinite(math.Log(f.MediumRate/f.LongRate), -4, 4)
	out[3] = clampFinite(2*clampRatio(f.DownsideShare, 0, 1)-1, -1, 1)
	out[4] = clampFinite(clampRatio(f.JumpFraction, 0, 1), 0, 1)
	return out, true
}

// HARVarianceDecision forecasts integrated variance over the same horizon as
// CurrentVariance. The log-ratio representation makes the zero prior equal to
// the causal random-walk baseline instead of an arbitrary volatility level.
type HARVarianceDecision struct {
	Healthy             bool
	Reason              string
	Samples             int
	CurrentVariance     float64
	ForecastVariance    float64
	ForecastLogRatio    float64
	ResidualLogVariance float64
	StressLogRatio      float64
	StressStandardError float64
	StressLowerLogRatio float64
	StressUpperLogRatio float64
	ElevatedRisk        bool
	DeescalatedRisk     bool
}

// OnlineHARVarianceModel is a ridge-regularized recursive Bayesian linear
// model for log(future variance/current variance). It has no pretrained
// artifact; a zero-mean isotropic prior expresses the random-walk forecast.
type OnlineHARVarianceModel struct {
	mean         [HARVarianceFeatureCount]float64
	covariance   [HARVarianceFeatureCount][HARVarianceFeatureCount]float64
	samples      int
	residualMean float64
	residualM2   float64
}

func NewOnlineHARVarianceModel() *OnlineHARVarianceModel {
	m := &OnlineHARVarianceModel{}
	for index := range m.covariance {
		m.covariance[index][index] = 1
	}
	return m
}

func (m *OnlineHARVarianceModel) Samples() int {
	if m == nil {
		return 0
	}
	return m.samples
}

func (m *OnlineHARVarianceModel) Predict(features HARVarianceFeatures, currentVariance float64) HARVarianceDecision {
	d := HARVarianceDecision{Reason: "invalid HAR variance features", CurrentVariance: currentVariance}
	if m == nil || currentVariance <= 0 {
		return d
	}
	vector, ok := features.Vector()
	if !ok {
		return d
	}
	logRatio := harDot(m.mean, vector)
	// The expected variance under a log-normal residual adds half the causal
	// residual variance. Coefficient uncertainty is epistemic and is reported
	// through health/sample count rather than inflated into carrying risk.
	residualVariance := 0.0
	if m.samples > 1 {
		residualVariance = m.residualM2 / float64(m.samples-1)
	}
	logExpectation := clampFinite(logRatio+.5*residualVariance, -4, 4)
	d.Samples = m.samples
	d.ForecastLogRatio = logRatio
	d.ResidualLogVariance = residualVariance
	d.ForecastVariance = currentVariance * math.Exp(logExpectation)
	stressVector := vector
	stressVector[0] = 0
	d.StressLogRatio = harDot(m.mean, stressVector)
	var stressVariance float64
	for row := range m.covariance {
		for column := range m.covariance[row] {
			stressVariance += stressVector[row] * m.covariance[row][column] * stressVector[column]
		}
	}
	d.StressStandardError = math.Sqrt(math.Max(0, stressVariance))
	d.StressLowerLogRatio = d.StressLogRatio - 1.6448536269514722*d.StressStandardError
	d.StressUpperLogRatio = d.StressLogRatio + 1.6448536269514722*d.StressStandardError
	d.ElevatedRisk = d.StressLowerLogRatio > 0
	d.DeescalatedRisk = d.StressUpperLogRatio < 0
	d.Healthy = m.samples >= 4*HARVarianceFeatureCount
	if d.Healthy {
		d.Reason = "online mixed-frequency HAR variance forecast"
	} else {
		d.Reason = "HAR variance posterior warming up; random-walk baseline remains authoritative"
		d.ForecastVariance = currentVariance
		d.ForecastLogRatio = 0
	}
	return d
}

func (m *OnlineHARVarianceModel) Update(features HARVarianceFeatures, currentVariance, futureVariance float64) bool {
	if m == nil || currentVariance <= 0 || futureVariance <= 0 {
		return false
	}
	vector, ok := features.Vector()
	if !ok {
		return false
	}
	target := clampFinite(math.Log(futureVariance/currentVariance), -4, 4)
	prediction := harDot(m.mean, vector)
	residual := target - prediction
	var projected [HARVarianceFeatureCount]float64
	var information float64
	for row := range m.covariance {
		for column := range m.covariance[row] {
			projected[row] += m.covariance[row][column] * vector[column]
		}
		information += vector[row] * projected[row]
	}
	denominator := 1 + math.Max(0, information)
	for index := range m.mean {
		m.mean[index] += projected[index] * residual / denominator
	}
	for row := range m.covariance {
		for column := range m.covariance[row] {
			m.covariance[row][column] -= projected[row] * projected[column] / denominator
		}
	}
	m.samples++
	delta := residual - m.residualMean
	m.residualMean += delta / float64(m.samples)
	m.residualM2 += delta * (residual - m.residualMean)
	return true
}

func harDot(left, right [HARVarianceFeatureCount]float64) float64 {
	var out float64
	for index := range left {
		out += left[index] * right[index]
	}
	return out
}

func clampFinite(value, minimum, maximum float64) float64 {
	if math.IsNaN(value) {
		return 0
	}
	if math.IsInf(value, -1) || value < minimum {
		return minimum
	}
	if math.IsInf(value, 1) || value > maximum {
		return maximum
	}
	return value
}
