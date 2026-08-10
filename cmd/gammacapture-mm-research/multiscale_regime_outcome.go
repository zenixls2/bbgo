package main

import (
	"math"
	"time"

	"github.com/c9s/bbgo/pkg/strategy/gammacapture"
)

const multiscaleOutcomeFeatures = 4

type onlineBayesianOutcome struct {
	mean       [multiscaleOutcomeFeatures]float64
	covariance [multiscaleOutcomeFeatures][multiscaleOutcomeFeatures]float64
}

func newOnlineBayesianOutcome() *onlineBayesianOutcome {
	m := &onlineBayesianOutcome{}
	// Isotropic N(0,I) is a symmetric ridge prior: it expresses no assumed
	// continuation, reversal, or manually preferred feature weight.
	for index := range m.covariance {
		m.covariance[index][index] = 1
	}
	return m
}

func (m *onlineBayesianOutcome) predict(features [multiscaleOutcomeFeatures]float64) float64 {
	eta := outcomeDot(m.mean, features)
	var variance float64
	for row := range m.covariance {
		for column := range m.covariance[row] {
			variance += features[row] * m.covariance[row][column] * features[column]
		}
	}
	// Logistic-Gaussian integral approximation propagates coefficient
	// uncertainty instead of reporting sigmoid(E[w]) as certainty.
	return logistic(eta / math.Sqrt(1+math.Pi*math.Max(0, variance)/8))
}

func (m *onlineBayesianOutcome) update(features [multiscaleOutcomeFeatures]float64, outcome float64) {
	prediction := logistic(outcomeDot(m.mean, features))
	curvature := math.Max(1e-6, prediction*(1-prediction))
	var projected [multiscaleOutcomeFeatures]float64
	var information float64
	for row := range m.covariance {
		for column := range m.covariance[row] {
			projected[row] += m.covariance[row][column] * features[column]
		}
		information += features[row] * projected[row]
	}
	denominator := 1 + curvature*math.Max(0, information)
	innovation := (outcome - prediction) / denominator
	for index := range m.mean {
		m.mean[index] += projected[index] * innovation
	}
	for row := range m.covariance {
		for column := range m.covariance[row] {
			m.covariance[row][column] -= curvature * projected[row] * projected[column] / denominator
		}
	}
}

func outcomeDot(left, right [multiscaleOutcomeFeatures]float64) float64 {
	var out float64
	for index := range left {
		out += left[index] * right[index]
	}
	return out
}

func logistic(value float64) float64 {
	if value >= 0 {
		exponent := math.Exp(-value)
		return 1 / (1 + exponent)
	}
	exponent := math.Exp(value)
	return exponent / (1 + exponent)
}

type regimeOutcomePending struct {
	matures  int
	features [multiscaleOutcomeFeatures]float64
	outcome  int
}

func evaluateMultiscaleRegimeV2(closes []minuteRegimeClose, horizon, anchorStep time.Duration, costBps float64, hazard time.Duration) multiscaleRegimeVariant {
	variant := multiscaleRegimeVariant{HazardMeanMinutes: int(hazard / time.Minute)}
	if len(closes) < 2 || horizon <= 0 || anchorStep <= 0 {
		return variant
	}
	model := gammacapture.NewBayesianMultiscaleRegime(gammacapture.MultiscaleRegimeConfig{
		HazardMean: hazard, MaximumRunLength: int((2 * horizon) / time.Minute),
		VolatilityWindow: 30, MinimumSamples: 30,
	})
	outcomeModel := newOnlineBayesianOutcome()
	horizonSteps := int(horizon / time.Minute)
	stepMinutes := int(anchorStep / time.Minute)
	globalDown, globalUp := 0, 0
	var pending []regimeOutcomePending
	var scores []regimeScore
	logVolatilityEWMA := 0.0
	volatilityReady := false
	// A three-hour half-life ties the stress baseline to the forecast horizon;
	// it is a time-scale definition, not a fitted feature coefficient.
	alpha := 1 - math.Exp(-math.Ln2/math.Max(1, horizon.Minutes()))

	for index, close := range closes {
		decision := model.ObserveMinute(close.at, close.bid, close.ask)
		remaining := pending[:0]
		for _, label := range pending {
			if label.matures > index {
				remaining = append(remaining, label)
				continue
			}
			switch label.outcome {
			case 1:
				outcomeModel.update(label.features, 1)
				globalDown++
			case -1:
				outcomeModel.update(label.features, 0)
				globalUp++
			}
		}
		pending = remaining
		if !decision.Healthy || decision.ContinuousVolatilityBps <= 0 {
			continue
		}
		logVolatility := math.Log(decision.ContinuousVolatilityBps)
		if !volatilityReady {
			logVolatilityEWMA = logVolatility
			volatilityReady = true
		} else {
			logVolatilityEWMA += alpha * (logVolatility - logVolatilityEWMA)
		}
		minuteOfDay := close.at.Minute() + 60*close.at.Hour()
		if index+horizonSteps >= len(closes) || minuteOfDay%stepMinutes != 0 {
			continue
		}
		if !closes[index+horizonSteps].at.Equal(close.at.Add(horizon)) {
			continue
		}
		outcome := executableFirstPassage(closes, index, horizonSteps, costBps)
		variant.Predictions++
		switch outcome {
		case 1:
			variant.DownFirst++
		case -1:
			variant.UpFirst++
		default:
			variant.Censored++
		}
		rawProbability := clampProbability(decision.DownProbability)
		logOdds := math.Log(rawProbability / (1 - rawProbability))
		logOdds = math.Max(-4, math.Min(4, logOdds))
		volatilityStress := math.Max(-2, math.Min(2, logVolatility-logVolatilityEWMA))
		panicInteraction := math.Max(0, logOdds) * math.Max(0, volatilityStress)
		panicInteraction = math.Min(4, panicInteraction)
		features := [multiscaleOutcomeFeatures]float64{1, logOdds, volatilityStress, panicInteraction}
		calibrated := clampProbability(outcomeModel.predict(features))
		climatology := float64(globalDown+1) / float64(globalDown+globalUp+2)
		if outcome != 0 {
			y := 0.0
			if outcome == 1 {
				y = 1
			}
			scores = append(scores, regimeScore{p: rawProbability, calibrated: calibrated, climatology: clampProbability(climatology), y: y, day: close.at.Format(time.DateOnly)})
		}
		pending = append(pending, regimeOutcomePending{matures: index + horizonSteps, features: features, outcome: outcome})
		variant.MeanChangeProbability += decision.ChangeProbability
		variant.MeanJumpFraction += decision.JumpVariationFraction
	}
	if variant.Predictions > 0 {
		variant.MeanChangeProbability /= float64(variant.Predictions)
		variant.MeanJumpFraction /= float64(variant.Predictions)
	}
	summarizeRegimeScores(&variant, scores)
	return variant
}
