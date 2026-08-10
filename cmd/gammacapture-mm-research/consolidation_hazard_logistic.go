package main

import "math"

// logisticHazardVector contains only dimensionless, predeclared path
// statistics.  The last term is the Daniel--Moskowitz-style interaction:
// directional momentum is allowed to change with the volatility state rather
// than being treated as a constant-premium signal.
func logisticHazardVector(feature consolidationHazardFeature, means, scales [4]float64) [6]float64 {
	values := hazardFeatureVector(feature)
	standardized := [4]float64{}
	for i, value := range values {
		if scales[i] > 0 {
			standardized[i] = (value - means[i]) / scales[i]
		}
	}
	return [6]float64{
		1,
		standardized[0],
		standardized[1],
		standardized[2],
		standardized[3],
		standardized[0] * standardized[3],
	}
}

func consolidationFeatureMoments(samples []consolidationHazardSample) (means, scales [4]float64) {
	if len(samples) == 0 {
		return means, scales
	}
	for _, sample := range samples {
		values := hazardFeatureVector(sample.feature)
		for i, value := range values {
			means[i] += value
		}
	}
	for i := range means {
		means[i] /= float64(len(samples))
	}
	for _, sample := range samples {
		values := hazardFeatureVector(sample.feature)
		for i, value := range values {
			scales[i] += math.Pow(value-means[i], 2)
		}
	}
	for i := range scales {
		scales[i] = math.Sqrt(scales[i] / math.Max(1, float64(len(samples)-1)))
	}
	return means, scales
}

func logisticProbability(value float64) float64 {
	if value >= 0 {
		return 1 / (1 + math.Exp(-math.Min(value, 40)))
	}
	exponential := math.Exp(math.Max(value, -40))
	return exponential / (1 + exponential)
}

func solveHazardLinear(matrix [6][6]float64, vector [6]float64) ([6]float64, bool) {
	var augmented [6][7]float64
	for row := 0; row < 6; row++ {
		for column := 0; column < 6; column++ {
			augmented[row][column] = matrix[row][column]
		}
		augmented[row][6] = vector[row]
	}
	for column := 0; column < 6; column++ {
		pivot := column
		for row := column + 1; row < 6; row++ {
			if math.Abs(augmented[row][column]) > math.Abs(augmented[pivot][column]) {
				pivot = row
			}
		}
		if math.Abs(augmented[pivot][column]) < 1e-12 {
			return [6]float64{}, false
		}
		augmented[column], augmented[pivot] = augmented[pivot], augmented[column]
		divisor := augmented[column][column]
		for j := column; j <= 6; j++ {
			augmented[column][j] /= divisor
		}
		for row := 0; row < 6; row++ {
			if row == column {
				continue
			}
			factor := augmented[row][column]
			for j := column; j <= 6; j++ {
				augmented[row][j] -= factor * augmented[column][j]
			}
		}
	}
	var solution [6]float64
	for i := range solution {
		solution[i] = augmented[i][6]
	}
	return solution, true
}

func dotHazard(a, b [6]float64) float64 {
	value := 0.0
	for i := range a {
		value += a[i] * b[i]
	}
	return value
}

// bayesianLogisticConsolidationHazard uses the Laplace approximation to a
// logistic posterior with an isotropic N(0, I) coefficient prior.  Recomputing
// the MAP from causally matured observations avoids a learning-rate or a
// pretrained coefficient artifact.  The posterior precision supplies a lower
// credible probability for the downside event.
func bayesianLogisticConsolidationHazard(samples []consolidationHazardSample, current consolidationHazardFeature, confidenceZ float64) (consolidationHazardPrediction, bool) {
	prediction := consolidationHazardPrediction{Samples: len(samples), Neighbors: len(samples)}
	resolved := 0
	for _, sample := range samples {
		if sample.outcome != 0 {
			resolved++
		}
	}
	if resolved < 6 {
		return prediction, false
	}
	means, scales := consolidationFeatureMoments(samples)
	weights := [6]float64{}
	precision := [6][6]float64{}
	for iteration := 0; iteration < 12; iteration++ {
		precision = [6][6]float64{}
		gradient := [6]float64{}
		for i := range weights {
			precision[i][i] = 1
			gradient[i] = -weights[i]
		}
		for _, sample := range samples {
			if sample.outcome == 0 {
				continue
			}
			x := logisticHazardVector(sample.feature, means, scales)
			probability := logisticProbability(dotHazard(weights, x))
			y := 0.0
			if sample.outcome == 1 {
				y = 1
			}
			for row := range weights {
				gradient[row] += x[row] * (y - probability)
				for column := range weights {
					precision[row][column] += probability * (1 - probability) * x[row] * x[column]
				}
			}
		}
		delta, ok := solveHazardLinear(precision, gradient)
		if !ok {
			return prediction, false
		}
		maximumDelta := 0.0
		for i := range weights {
			weights[i] += delta[i]
			maximumDelta = math.Max(maximumDelta, math.Abs(delta[i]))
		}
		if maximumDelta < 1e-8 {
			break
		}
	}
	x := logisticHazardVector(current, means, scales)
	logOdds := dotHazard(weights, x)
	prediction.DownGivenMoveProbability = logisticProbability(logOdds)
	// x' P^-1 x is the Laplace posterior variance of the current log odds.
	posteriorDirection, ok := solveHazardLinear(precision, x)
	if !ok {
		return prediction, false
	}
	logOddsVariance := math.Max(0, dotHazard(x, posteriorDirection))
	prediction.DownGivenMoveLower = logisticProbability(logOdds - math.Max(0, confidenceZ)*math.Sqrt(logOddsVariance))
	down, up, censored := 0, 0, 0
	downMagnitude, upMagnitude := 0.0, 0.0
	for _, sample := range samples {
		switch sample.outcome {
		case 1:
			down++
			downMagnitude += sample.downNetExcursion
		case -1:
			up++
			upMagnitude += sample.upNetExcursion
		default:
			censored++
		}
	}
	moveProbability := float64(down+up+2) / float64(len(samples)+3)
	prediction.DownProbability = moveProbability * prediction.DownGivenMoveProbability
	prediction.UpProbability = moveProbability * (1 - prediction.DownGivenMoveProbability)
	prediction.CensorProbability = 1 - moveProbability
	meanDown, meanUp := 0.0, 0.0
	if down > 0 {
		meanDown = downMagnitude / float64(down)
	}
	if up > 0 {
		meanUp = upMagnitude / float64(up)
	}
	p := prediction.DownGivenMoveProbability
	pLower := prediction.DownGivenMoveLower
	prediction.ExpectedNetReturnBps = moveProbability * ((1-p)*meanUp - p*meanDown) * 10_000
	// The upper economic bound uses the downside-probability lower bound.  It
	// asks whether even the optimistic direction compatible with coefficient
	// uncertainty remains loss-making for new long exposure.
	prediction.ExpectedUpperBps = moveProbability * ((1-pLower)*meanUp - pLower*meanDown) * 10_000
	return prediction, true
}
