package gammacapture

import (
	"math"
	"testing"
)

func TestContinuationPosteriorMomentsIncludeCensorOutcomeOnce(t *testing.T) {
	samples := make([]trendExcursionSample, 9)
	for i := 0; i < 8; i++ {
		samples[i] = trendExcursionSample{
			continuationOutcome: -1, continuationDownExcursion: 0.004,
		}
	}
	samples[8] = trendExcursionSample{
		continuationOutcome: 1, continuationUpExcursion: 0.003,
	}
	// Dirichlet(1,1,1): p_up=2/12, p_down=9/12, p_censor=1/12.
	mean, variance, meanSE := continuationPosteriorMoments(samples, 2.0/12, 9.0/12)
	if math.Abs(mean-(-0.0025)) > 1e-12 {
		t.Fatalf("unexpected posterior mean: %.12g", mean)
	}
	if math.Abs(variance-0.00000725) > 1e-12 {
		t.Fatalf("unexpected posterior variance: %.12g", variance)
	}
	if meanSE <= 0 {
		t.Fatalf("Dirichlet probability uncertainty was lost: %.12g", meanSE)
	}
}

func TestContinuationPosteriorMomentsUseSymmetricSparseMagnitudePrior(t *testing.T) {
	samples := make([]trendExcursionSample, 9)
	samples[0] = trendExcursionSample{
		continuationOutcome: 1, continuationUpExcursion: 0.01,
	}
	// Eight samples are censored and there is no observed down magnitude. The
	// symmetric Dirichlet down pseudo-event receives the pooled 1% magnitude,
	// so it cannot silently become a zero-loss outcome.
	mean, variance, _ := continuationPosteriorMoments(samples, 2.0/12, 1.0/12)
	if math.Abs(mean-(0.01/12)) > 1e-12 {
		t.Fatalf("sparse-side magnitude prior is asymmetric: %.12g", mean)
	}
	if variance <= 0 {
		t.Fatalf("censor and directional uncertainty were lost: %.12g", variance)
	}
}

func TestContinuationPosteriorMomentsAllCensoredAreNeutral(t *testing.T) {
	samples := make([]trendExcursionSample, 9)
	mean, variance, meanSE := continuationPosteriorMoments(samples, 1.0/12, 1.0/12)
	if mean != 0 || variance != 0 || meanSE != 0 {
		t.Fatalf("all-censored posterior must be neutral: mean=%g variance=%g se=%g", mean, variance, meanSE)
	}
}
