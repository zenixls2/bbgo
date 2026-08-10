package main

import "testing"

func TestBayesianLogisticHazardLearnsVolatilityConditionedDownside(t *testing.T) {
	var samples []consolidationHazardSample
	for i := 0; i < 30; i++ {
		downFeature := consolidationHazardFeature{
			longZ: -1.5 - 0.01*float64(i), shortZ: -0.2,
			efficiencyRatio: 0.5, qvAccelerationLog: 0.8,
		}
		upFeature := consolidationHazardFeature{
			longZ: -0.3 - 0.005*float64(i), shortZ: 0.3,
			efficiencyRatio: 0.7, qvAccelerationLog: -0.5,
		}
		samples = append(samples,
			consolidationHazardSample{feature: downFeature, outcome: 1, downNetExcursion: 0.004},
			consolidationHazardSample{feature: upFeature, outcome: -1, upNetExcursion: 0.004})
	}
	current := consolidationHazardFeature{
		longZ: -1.8, shortZ: -0.2, efficiencyRatio: 0.5, qvAccelerationLog: 0.8,
	}
	prediction, ok := bayesianLogisticConsolidationHazard(samples, current, 1.645)
	if !ok {
		t.Fatal("Bayesian logistic hazard was unavailable")
	}
	if prediction.DownGivenMoveProbability <= 0.5 || prediction.DownGivenMoveLower <= 0.5 {
		t.Fatalf("known downside relation was not learned with uncertainty: %+v", prediction)
	}
	if prediction.ExpectedUpperBps >= 0 {
		t.Fatalf("known downside relation did not produce negative long-entry value: %+v", prediction)
	}
}

func TestBayesianLogisticHazardRetainsCensorMass(t *testing.T) {
	feature := consolidationHazardFeature{longZ: -1, efficiencyRatio: 0.5}
	var samples []consolidationHazardSample
	for i := 0; i < 10; i++ {
		samples = append(samples,
			consolidationHazardSample{feature: feature, outcome: 1, downNetExcursion: 0.003},
			consolidationHazardSample{feature: feature, outcome: -1, upNetExcursion: 0.003},
			consolidationHazardSample{feature: feature, outcome: 0})
	}
	prediction, ok := bayesianLogisticConsolidationHazard(samples, feature, 1.645)
	if !ok {
		t.Fatal("Bayesian logistic hazard was unavailable")
	}
	if prediction.CensorProbability <= 0 || prediction.DownProbability+prediction.UpProbability+prediction.CensorProbability < 0.999999999 {
		t.Fatalf("posterior lost censor mass: %+v", prediction)
	}
}
