package main

import "math"

// wilsonLower returns the lower Wilson score bound used by independent
// research studies; it has no dependency on a regime classifier.
func wilsonLower(successes, trials int, z float64) float64 {
	if trials <= 0 {
		return 0
	}
	n, probability := float64(trials), float64(successes)/float64(trials)
	denominator := 1 + z*z/n
	return math.Max(0, (probability+z*z/(2*n)-z*math.Sqrt(
		probability*(1-probability)/n+z*z/(4*n*n)))/denominator)
}
