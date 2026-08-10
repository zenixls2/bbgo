package gammacapture

import (
	"math"
	"testing"
)

func TestOnlineHARVarianceStartsAtRandomWalkBaseline(t *testing.T) {
	model := NewOnlineHARVarianceModel()
	features := HARVarianceFeatures{ShortRate: 2, MediumRate: 1.5, LongRate: 1, DownsideShare: .7, JumpFraction: .2}
	decision := model.Predict(features, .004)
	if decision.Healthy {
		t.Fatal("fresh model must not report healthy")
	}
	if decision.ForecastVariance != .004 || decision.ForecastLogRatio != 0 {
		t.Fatalf("fresh forecast = %+v, want exact random-walk baseline", decision)
	}
}

func TestOnlineHARVarianceLearnsLogVarianceRatio(t *testing.T) {
	model := NewOnlineHARVarianceModel()
	for index := 0; index < 160; index++ {
		shortRatio := math.Exp(-.8 + 1.6*float64(index%17)/16)
		mediumRatio := math.Exp(-.5 + float64(index%11)/10)
		features := HARVarianceFeatures{
			ShortRate: shortRatio, MediumRate: mediumRatio, LongRate: 1,
			DownsideShare: float64(index%9) / 8,
			JumpFraction:  float64(index%7) / 6,
		}
		vector, _ := features.Vector()
		logRatio := .15 + .45*vector[1] - .25*vector[2] + .20*vector[3] + .10*vector[4]
		model.Update(features, .002, .002*math.Exp(logRatio))
	}
	testFeatures := HARVarianceFeatures{ShortRate: 1.8, MediumRate: .8, LongRate: 1, DownsideShare: .75, JumpFraction: .3}
	vector, _ := testFeatures.Vector()
	wantLogRatio := .15 + .45*vector[1] - .25*vector[2] + .20*vector[3] + .10*vector[4]
	decision := model.Predict(testFeatures, .003)
	if !decision.Healthy {
		t.Fatalf("trained model unhealthy: %+v", decision)
	}
	if math.Abs(decision.ForecastLogRatio-wantLogRatio) > .08 {
		t.Fatalf("forecast log ratio = %.6f, want %.6f", decision.ForecastLogRatio, wantLogRatio)
	}
}

func TestOnlineHARVarianceIsVarianceScaleInvariant(t *testing.T) {
	features := HARVarianceFeatures{ShortRate: 1.2, MediumRate: .9, LongRate: 1, DownsideShare: .4, JumpFraction: .1}
	left, right := NewOnlineHARVarianceModel(), NewOnlineHARVarianceModel()
	for index := 0; index < 50; index++ {
		ratio := math.Exp(.1 * math.Sin(float64(index)))
		left.Update(features, .001, .001*ratio)
		right.Update(features, .1, .1*ratio)
	}
	leftDecision := left.Predict(features, .002)
	rightDecision := right.Predict(features, .2)
	if math.Abs(leftDecision.ForecastLogRatio-rightDecision.ForecastLogRatio) > 1e-12 {
		t.Fatalf("variance scaling changed log-ratio forecast: %.12f vs %.12f", leftDecision.ForecastLogRatio, rightDecision.ForecastLogRatio)
	}
	if math.Abs(rightDecision.ForecastVariance/leftDecision.ForecastVariance-100) > 1e-9 {
		t.Fatalf("forecast variance did not scale by 100: left=%g right=%g", leftDecision.ForecastVariance, rightDecision.ForecastVariance)
	}
}
