package gammacapture

import "math"

// continuationPosteriorMoments computes moments of the complete competing-risk
// return law: up excursion, down excursion, or a zero return when censored.
// The outcome probabilities are the Dirichlet posterior predictive values.
// A pooled symmetric magnitude prior is used only when one direction has no
// observed magnitude, avoiding the incoherent zero-magnitude pseudo-event that
// would bias a sparse posterior toward the observed side.
func continuationPosteriorMoments(
	samples []trendExcursionSample,
	upProbability, downProbability float64,
) (mean, variance, meanSE float64) {
	type moments struct {
		count          int
		sum, sumSquare float64
	}
	var up, down moments
	for _, sample := range samples {
		switch sample.continuationOutcome {
		case 1:
			value := math.Max(0, sample.continuationUpExcursion)
			up.count++
			up.sum += value
			up.sumSquare += value * value
		case -1:
			value := math.Max(0, sample.continuationDownExcursion)
			down.count++
			down.sum += value
			down.sumSquare += value * value
		}
	}

	resolved := up.count + down.count
	pooledMean, pooledSecond := 0.0, 0.0
	if resolved > 0 {
		pooledMean = (up.sum + down.sum) / float64(resolved)
		pooledSecond = (up.sumSquare + down.sumSquare) / float64(resolved)
	}
	side := func(value moments) (sideMean, sideSecond, meanVariance float64) {
		if value.count == 0 {
			return pooledMean, pooledSecond, 0
		}
		n := float64(value.count)
		sideMean = value.sum / n
		sideSecond = value.sumSquare / n
		if value.count > 1 {
			sampleVariance := math.Max(0,
				(value.sumSquare-value.sum*value.sum/n)/(n-1))
			meanVariance = sampleVariance / n
		}
		return sideMean, sideSecond, meanVariance
	}
	upMean, upSecond, upMeanVariance := side(up)
	downMean, downSecond, downMeanVariance := side(down)
	mean = upProbability*upMean - downProbability*downMean
	secondMoment := upProbability*upSecond + downProbability*downSecond
	variance = math.Max(0, secondMoment-mean*mean)

	// For p~Dirichlet(alpha), Var(c'p) equals the posterior predictive
	// between-outcome variance divided by alpha0+1. Add plug-in conditional
	// magnitude-mean uncertainty without treating the two directions as
	// independent directional classifiers.
	alpha0 := float64(len(samples) + 3)
	probabilityMeanVariance := math.Max(0,
		(upProbability*upMean*upMean+downProbability*downMean*downMean-mean*mean)/
			(alpha0+1))
	magnitudeMeanVariance := upProbability*upProbability*upMeanVariance +
		downProbability*downProbability*downMeanVariance
	meanSE = math.Sqrt(math.Max(0, probabilityMeanVariance+magnitudeMeanVariance))
	return mean, variance, meanSE
}
