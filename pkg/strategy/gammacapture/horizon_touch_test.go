package gammacapture

import (
	"math"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

func testHorizonTouchArtifact() *HorizonTouchArtifact {
	cells := make([]HorizonTouchCell, 0, 8)
	for _, side := range []string{string(types.SideTypeBuy), string(types.SideTypeSell)} {
		for _, horizon := range []int{10, 30} {
			for _, distance := range []float64{20, 50} {
				cells = append(cells, HorizonTouchCell{
					HorizonMinutes: horizon,
					DistanceBps:    distance,
					Side:           side,
					Intercept:      float64(horizon)/10 - distance/100,
					Coefficients:   make([]float64, len(HorizonTouchFeatureNames)),
					TrainingRate:   0.1,
				})
			}
		}
	}
	return &HorizonTouchArtifact{
		Version:          HorizonTouchModelVersion,
		PriceBasis:       "executable-bbo",
		Symbol:           "SOLJPY",
		FeatureNames:     append([]string(nil), HorizonTouchFeatureNames...),
		FeatureMeans:     make([]float64, len(HorizonTouchFeatureNames)),
		FeatureScales:    []float64{1, 1, 1, 1, 1, 1, 1, 1},
		HistoricalWeight: 0.75,
		Accepted:         true,
		BBOHoldout:       HorizonTouchMetrics{BrierImprovementPct: 6.7},
		Cells:            cells,
	}
}

func TestHorizonTouchArtifactValidatesAndInterpolatesLogits(t *testing.T) {
	artifact := testHorizonTouchArtifact()
	if err := artifact.Validate("SOLJPY", 5); err != nil {
		t.Fatalf("valid artifact rejected: %v", err)
	}
	features := make([]float64, len(HorizonTouchFeatureNames))
	probability, ok := artifact.Predict(types.SideTypeBuy, 20*time.Minute, 35, features)
	if !ok {
		t.Fatal("interpolated prediction was unavailable")
	}
	// All four surrounding cells interpolate to logit 1.65.
	want := sigmoid(1.65)
	if math.Abs(probability-want) > 1e-12 {
		t.Fatalf("unexpected interpolated probability: got %.12f want %.12f", probability, want)
	}
}

func TestHorizonTouchArtifactRejectsUnacceptedOrWeakModel(t *testing.T) {
	artifact := testHorizonTouchArtifact()
	artifact.Accepted = false
	artifact.AcceptanceReason = "holdout failed"
	if err := artifact.Validate("SOLJPY", 5); err == nil {
		t.Fatal("unaccepted model should be rejected")
	}
	artifact.Accepted = true
	if err := artifact.Validate("SOLJPY", 7); err == nil {
		t.Fatal("model below the configured Brier improvement should be rejected")
	}
}

func TestBlendTouchProbabilityAndRateAreConservative(t *testing.T) {
	historical, recent := 0.20, 0.10
	blended := BlendTouchProbability(historical, recent, 0.75)
	if blended <= recent || blended >= historical {
		t.Fatalf("blended probability should remain between inputs: %.6f", blended)
	}
	rate := TouchProbabilityToRate(blended, 10*time.Minute, 0.25)
	unhaircut := -math.Log1p(-blended) / (10 * time.Minute).Hours()
	if math.Abs(rate-unhaircut*0.25) > 1e-12 {
		t.Fatalf("unexpected haircut rate: got %.12f want %.12f", rate, unhaircut*0.25)
	}
}

func TestHorizonTouchFeaturesUseMultiMinuteCausalHistory(t *testing.T) {
	var model MarketMakerHorizonModel
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	config := MarketMakerConfig{HorizonLookback: types.Duration(6 * time.Hour)}
	for minute := 0; minute <= 250; minute++ {
		price := 10_000 * math.Exp(float64(minute)/10_000)
		model.Observe(start.Add(time.Duration(minute)*time.Minute), price, config)
	}
	features, ok := model.HorizonTouchFeatures(start.Add(250 * time.Minute))
	if !ok {
		t.Fatal("expected complete multi-minute feature vector")
	}
	for i, want := range []float64{1, 5, 15, 60, 240} {
		if math.Abs(features[i]-want) > 1e-8 {
			t.Fatalf("return feature %d: got %.12f want %.12f", i, features[i], want)
		}
	}
	for i := 5; i < len(features); i++ {
		if math.Abs(features[i]-1) > 1e-8 {
			t.Fatalf("volatility feature %d: got %.12f want 1", i, features[i])
		}
	}
}
