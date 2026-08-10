package gammacapture

import (
	"fmt"
	"math"
	"strings"
	"time"
)

type MacroReversalInput struct {
	Now                             time.Time
	BaselineTargetRatio             float64
	CurrentRiskyWeight              float64
	PolicyMinRatio                  float64
	PolicyMaxRatio                  float64
	RoundTripCostBps                float64
	ConfidenceZScore                float64
	RiskAversion                    float64
	FallbackVolatilityBpsPerSqrtSec float64
}

type MacroReversalHorizonDecision struct {
	Early                      bool
	PosteriorBars              int
	SignalReliability          float64
	Horizon                    time.Duration
	Direction                  int
	Applied                    bool
	Bars                       int
	ChangeAt                   time.Time
	ForecastHorizon            time.Duration
	PriorSlopeBpsPerHour       float64
	PosteriorSlopeBpsPerHour   float64
	PosteriorSlopeSEBpsPerHour float64
	ReversalProbability        float64
	ForecastMeanBps            float64
	ForecastMeanSEBps          float64
	NetLowerEdgeBps            float64
	DownsideLossBps            float64
	RawSamples                 int
	EffectiveSamples           float64
	RobustTargetRatio          float64
	TargetShiftRatio           float64
	Reason                     string
}

type MacroReversalDecision struct {
	Enabled                  bool
	Direction                int
	Healthy                  bool
	Applied                  bool
	Reason                   string
	BaselineTargetRatio      float64
	TargetRatio              float64
	CurrentRiskyWeight       float64
	InventoryAdjustmentRatio float64
	AdditionalHeadroomRatio  float64
	AggregateProbability     float64
	AggregateNetEdgeBps      float64
	HealthyHorizons          int
	ActiveHorizons           int
	EarlyHorizons            int
	SignalChangeAt           time.Time
	SignalForecastHorizon    time.Duration
	LeaseApplied             bool
	LeaseAge                 time.Duration
	LeaseSurvivalProbability float64
	HorizonSummary           string
	Horizons                 []MacroReversalHorizonDecision
}

type macroReversalCacheEntry struct {
	BarCount              int
	LastClosed            time.Time
	BarInterval           time.Duration
	EarlyDetection        bool
	EarlyPosteriorBars    int
	EarlyMinimumPriorBars int
	Lookback              time.Duration
	MinimumSamples        int
	DriftPriorSamples     float64
	DownsideZScore        float64
	Decision              MacroReversalHorizonDecision
}

type macroLinearFit struct {
	Slope   float64
	SlopeSE float64
	SSE     float64
	N       int
}

func macroExecutablePrice(point macroInventoryBar, direction int) float64 {
	if direction < 0 {
		if point.Bid > 0 {
			return point.Bid
		}
	} else if point.Ask > 0 {
		return point.Ask
	}
	return point.Mid
}

func fitMacroLogExecutable(points []macroInventoryBar, direction int) (macroLinearFit, bool) {
	fit := macroLinearFit{N: len(points)}
	if len(points) < 3 || direction == 0 {
		return fit, false
	}
	origin := points[0].At
	meanX, meanY := 0.0, 0.0
	for _, point := range points {
		price := macroExecutablePrice(point, direction)
		if price <= 0 {
			return fit, false
		}
		meanX += point.At.Sub(origin).Hours()
		meanY += math.Log(price)
	}
	meanX /= float64(len(points))
	meanY /= float64(len(points))
	sxx, sxy := 0.0, 0.0
	for _, point := range points {
		price := macroExecutablePrice(point, direction)
		x := point.At.Sub(origin).Hours() - meanX
		y := math.Log(price) - meanY
		sxx += x * x
		sxy += x * y
	}
	if sxx <= 0 {
		return fit, false
	}
	fit.Slope = sxy / sxx
	for _, point := range points {
		price := macroExecutablePrice(point, direction)
		x := point.At.Sub(origin).Hours() - meanX
		residual := math.Log(price) - meanY - fit.Slope*x
		fit.SSE += residual * residual
	}
	if len(points) > 2 {
		residualVariance := fit.SSE / float64(len(points)-2)
		fit.SlopeSE = math.Sqrt(math.Max(0, residualVariance/sxx))
	}
	return fit, true
}

func macroSlopeSignProbability(slope, standardError float64, positive bool) float64 {
	if standardError <= 0 {
		if (positive && slope > 0) || (!positive && slope < 0) {
			return 1
		}
		return 0
	}
	z := slope / standardError
	if !positive {
		z = -z
	}
	return 0.5 * math.Erfc(-z/math.Sqrt2)
}

func macroLogistic(value float64) float64 {
	if value >= 40 {
		return 1
	}
	if value <= -40 {
		return 0
	}
	return 1 / (1 + math.Exp(-value))
}

func macroTacticalTarget(baseline, netConfidenceEdge, denominator, minimum, maximum float64) float64 {
	if denominator <= 0 {
		return math.Max(minimum, math.Min(maximum, baseline))
	}
	return math.Max(minimum, math.Min(maximum, baseline+netConfidenceEdge/denominator))
}

func macroMeanVariance(values []float64) (mean, variance float64, ok bool) {
	if len(values) < 2 {
		return 0, 0, false
	}
	for _, value := range values {
		mean += value
	}
	mean /= float64(len(values))
	for _, value := range values {
		delta := value - mean
		variance += delta * delta
	}
	variance /= float64(len(values) - 1)
	return mean, math.Max(variance, 1e-18), true
}

func macroExecutableLogReturns(points []macroInventoryBar, direction int) ([]float64, bool) {
	if len(points) < 2 {
		return nil, false
	}
	returns := make([]float64, 0, len(points)-1)
	previous := macroExecutablePrice(points[0], direction)
	if previous <= 0 {
		return nil, false
	}
	for _, point := range points[1:] {
		price := macroExecutablePrice(point, direction)
		if price <= 0 {
			return nil, false
		}
		returns = append(returns, math.Log(price/previous))
		previous = price
	}
	return returns, true
}

// earlyMacroReversalAtHorizon is a causal fixed-endpoint sequential test.  It
// compares the newest executable-price returns with a longer pre-change sample
// whose variance supplies the small-sample uncertainty.  Unlike the full BIC
// scan it does not search over historical split points, so two closed posterior
// bars are enough without paying a multiple-candidate penalty.
func earlyMacroReversalAtHorizon(points []macroInventoryBar, direction int, interval time.Duration, c MacroInventoryConfig) (MacroReversalHorizonDecision, bool) {
	d := MacroReversalHorizonDecision{Direction: direction, Bars: len(points), Early: true}
	cfg := c.ReversalAccumulation
	posteriorBars := cfg.EarlyPosteriorBars
	minimumPriorBars := cfg.EarlyMinimumPriorBars
	if !cfg.EarlyDetection || direction == 0 || interval <= 0 ||
		posteriorBars < 2 || len(points) < minimumPriorBars+posteriorBars {
		return d, false
	}
	split := len(points) - posteriorBars
	returns, ok := macroExecutableLogReturns(points, direction)
	if !ok || split < 2 {
		return d, false
	}
	priorReturns := returns[:split-1]
	posteriorReturns := returns[split-1:]
	priorMean, priorVariance, ok := macroMeanVariance(priorReturns)
	if !ok || len(posteriorReturns) != posteriorBars {
		return d, false
	}
	posteriorMean := 0.0
	posteriorBarsConfirm := true
	for _, value := range posteriorReturns {
		posteriorMean += value
		if direction > 0 && value <= 0 || direction < 0 && value >= 0 {
			posteriorBarsConfirm = false
		}
	}
	posteriorMean /= float64(len(posteriorReturns))
	priorOpposes := direction > 0 && priorMean < 0 || direction < 0 && priorMean > 0
	posteriorConfirms := direction > 0 && posteriorMean > 0 || direction < 0 && posteriorMean < 0
	if !priorOpposes || !posteriorConfirms || !posteriorBarsConfirm {
		return d, false
	}
	sigma := math.Sqrt(priorVariance)
	posteriorSE := sigma / math.Sqrt(float64(len(posteriorReturns)))
	changeSE := sigma * math.Sqrt(1/float64(len(priorReturns))+1/float64(len(posteriorReturns)))
	signedChangeZ := float64(direction) * (posteriorMean - priorMean) / changeSE
	// The fixed split adds one posterior-mean parameter. Its BIC approximation
	// supplies one coherent posterior instead of multiplying three correlated
	// sign tests that reuse the same two posterior returns.
	totalReturns := float64(len(priorReturns) + len(posteriorReturns))
	logBayes := 0.5*signedChangeZ*signedChangeZ - 0.5*math.Log(totalReturns)
	d.ReversalProbability = macroLogistic(logBayes)
	if d.ReversalProbability <= 0.5 {
		return d, false
	}
	d.PosteriorBars = len(posteriorReturns)
	d.SignalReliability = float64(d.PosteriorBars) /
		(float64(d.PosteriorBars) + float64(c.MinimumSamples))
	d.ChangeAt = points[split-1].At
	d.ForecastHorizon = points[len(points)-1].At.Sub(d.ChangeAt)
	if d.ForecastHorizon < interval {
		d.ForecastHorizon = interval
	}
	d.PriorSlopeBpsPerHour = priorMean / interval.Hours() * 10_000
	d.PosteriorSlopeBpsPerHour = posteriorMean / interval.Hours() * 10_000
	d.PosteriorSlopeSEBpsPerHour = posteriorSE / interval.Hours() * 10_000
	d.Reason = "sequential early executable-price reversal candidate"
	return d, true
}

func (m *MacroInventoryModel) rollingExecutableBars(now time.Time, horizon, interval time.Duration) []macroInventoryBar {
	if now.IsZero() || horizon <= 0 || interval <= 0 || len(m.bars) == 0 {
		return nil
	}
	cutoff := now.Add(-horizon)
	lastSegment := m.bars[len(m.bars)-1].Segment
	points := make([]macroInventoryBar, 0, int(horizon/interval)+1)
	for _, point := range m.bars {
		if point.At.After(now) || point.At.Before(cutoff) || point.Segment != lastSegment {
			continue
		}
		if len(points) > 0 && point.At.Sub(points[len(points)-1].At) != interval {
			points = points[:0]
		}
		points = append(points, point)
	}
	return points
}

// reversalShapeAtHorizon reuses the expensive BIC change-point scan until a
// causally closed macro bar arrives. Per-tick fees, fallback volatility, and
// risk budgets are evaluated after this structural cache.
func (m *MacroInventoryModel) reversalShapeAtHorizon(now time.Time, horizon time.Duration, c MacroInventoryConfig) MacroReversalHorizonDecision {
	c.setDefaults()
	lastClosed := time.Time{}
	if len(m.bars) > 0 {
		lastClosed = m.bars[len(m.bars)-1].At
	}
	if entry, ok := m.reversalCache[horizon]; ok &&
		entry.BarCount == len(m.bars) && entry.LastClosed.Equal(lastClosed) &&
		entry.BarInterval == time.Duration(c.BarInterval) &&
		entry.Lookback == time.Duration(c.Lookback) &&
		entry.MinimumSamples == c.MinimumSamples &&
		entry.DriftPriorSamples == c.DriftPriorSamples &&
		entry.EarlyDetection == c.ReversalAccumulation.EarlyDetection &&
		entry.EarlyPosteriorBars == c.ReversalAccumulation.EarlyPosteriorBars &&
		entry.EarlyMinimumPriorBars == c.ReversalAccumulation.EarlyMinimumPriorBars &&
		entry.DownsideZScore == c.DownsideZScore {
		return entry.Decision
	}
	asOf := now
	if !lastClosed.IsZero() {
		asOf = lastClosed
	}
	decision := m.reversalShapeAtHorizonUncached(asOf, horizon, c)
	if m.reversalCache == nil {
		m.reversalCache = make(map[time.Duration]macroReversalCacheEntry)
	}
	m.reversalCache[horizon] = macroReversalCacheEntry{
		BarCount: len(m.bars), LastClosed: lastClosed,
		BarInterval: time.Duration(c.BarInterval), Lookback: time.Duration(c.Lookback),
		MinimumSamples: c.MinimumSamples, DriftPriorSamples: c.DriftPriorSamples,
		EarlyDetection:        c.ReversalAccumulation.EarlyDetection,
		EarlyPosteriorBars:    c.ReversalAccumulation.EarlyPosteriorBars,
		EarlyMinimumPriorBars: c.ReversalAccumulation.EarlyMinimumPriorBars,
		DownsideZScore:        c.DownsideZScore, Decision: decision,
	}
	m.reversalCacheBuilds++
	return decision
}

func (m *MacroInventoryModel) reversalShapeAtHorizonUncached(now time.Time, horizon time.Duration, c MacroInventoryConfig) MacroReversalHorizonDecision {
	d := MacroReversalHorizonDecision{Horizon: horizon, Reason: "insufficient rolling executable bars"}
	interval := time.Duration(c.BarInterval)
	points := m.rollingExecutableBars(now, horizon, interval)
	d.Bars = len(points)
	var earlyDecision MacroReversalHorizonDecision
	earlyFound := false
	horizons := c.horizons()
	if len(horizons) > 0 && horizon == horizons[0] {
		for _, direction := range []int{1, -1} {
			candidate, ok := earlyMacroReversalAtHorizon(points, direction, interval, c)
			if !ok || earlyFound && candidate.ReversalProbability <= earlyDecision.ReversalProbability {
				continue
			}
			candidate.Horizon = horizon
			earlyDecision = candidate
			earlyFound = true
		}
	}
	if len(points) < 8 {
		if earlyFound {
			return earlyDecision
		}
		return d
	}
	minimumSegment := 4
	candidateCount := len(points) - 2*minimumSegment + 1
	bestLogBayes := math.Inf(-1)
	var bestPrior, bestPosterior macroLinearFit
	bestChangeAt := time.Time{}
	bestDirection := 0
	for _, direction := range []int{1, -1} {
		nullFit, ok := fitMacroLogExecutable(points, direction)
		if !ok {
			continue
		}
		for split := minimumSegment; split <= len(points)-minimumSegment; split++ {
			prior, priorOK := fitMacroLogExecutable(points[:split], direction)
			posterior, posteriorOK := fitMacroLogExecutable(points[split:], direction)
			if !priorOK || !posteriorOK {
				continue
			}
			bullish := direction > 0 && prior.Slope < 0 && posterior.Slope > 0
			bearish := direction < 0 && prior.Slope > 0 && posterior.Slope < 0
			if !bullish && !bearish {
				continue
			}
			n := float64(len(points))
			nullSSE := math.Max(nullFit.SSE, 1e-18)
			segmentedSSE := math.Max(prior.SSE+posterior.SSE, 1e-18)
			nullBIC := n*math.Log(nullSSE/n) + 2*math.Log(n)
			segmentedBIC := n*math.Log(segmentedSSE/n) + 5*math.Log(n)
			logBayes := 0.5*(nullBIC-segmentedBIC) - math.Log(float64(candidateCount))
			if logBayes > bestLogBayes {
				bestLogBayes = logBayes
				bestPrior = prior
				bestPosterior = posterior
				bestChangeAt = points[split].At
				bestDirection = direction
			}
		}
	}
	if bestChangeAt.IsZero() {
		if earlyFound {
			return earlyDecision
		}
		d.Reason = "no bid/ask rolling change point"
		return d
	}
	priorProbability := macroSlopeSignProbability(
		bestPrior.Slope, bestPrior.SlopeSE, bestDirection < 0)
	posteriorProbability := macroSlopeSignProbability(
		bestPosterior.Slope, bestPosterior.SlopeSE, bestDirection > 0)
	d.ReversalProbability = macroLogistic(bestLogBayes) * priorProbability * posteriorProbability
	d.Direction = bestDirection
	d.ChangeAt = bestChangeAt
	d.PriorSlopeBpsPerHour = bestPrior.Slope * 10_000
	d.PosteriorSlopeBpsPerHour = bestPosterior.Slope * 10_000
	d.PosteriorSlopeSEBpsPerHour = bestPosterior.SlopeSE * 10_000
	forecastHorizon := points[len(points)-1].At.Sub(bestChangeAt)
	if forecastHorizon < interval {
		forecastHorizon = interval
	}
	if forecastHorizon > horizon {
		forecastHorizon = horizon
	}
	d.ForecastHorizon = forecastHorizon
	if bestDirection > 0 {
		d.Reason = "rolling bullish reversal candidate"
	} else {
		d.Reason = "rolling bearish reversal candidate"
	}
	if earlyFound && earlyDecision.ChangeAt.After(d.ChangeAt) {
		// A recent fee-gated opposite turn may reduce risk before the older BIC
		// regime has accumulated four posterior bars. A same-direction turn stays
		// staged as well, rather than borrowing certainty from an older episode.
		return earlyDecision
	}
	return d
}

func (m *MacroInventoryModel) reversalAtHorizon(now time.Time, horizon time.Duration, c MacroInventoryConfig, in MacroReversalInput) MacroReversalHorizonDecision {
	d := m.reversalShapeAtHorizon(now, horizon, c)
	// Populate risk sufficiency even when no structural turn is present. Health
	// describes the data, not whether every horizon happens to emit a signal.
	estimate := m.Estimate(now, horizon, in.FallbackVolatilityBpsPerSqrtSec, c)
	d.RawSamples = estimate.RawSamples
	d.EffectiveSamples = estimate.EffectiveSamples
	d.DownsideLossBps = estimate.DownsideLoss * 10_000
	if d.ChangeAt.IsZero() {
		return d
	}
	if !estimate.Sufficient || estimate.DownsideLoss <= 0 || estimate.Variance <= 0 {
		d.Reason = "insufficient overlap-corrected horizon risk"
		return d
	}
	forecastMean := d.PosteriorSlopeBpsPerHour / 10_000 * d.ForecastHorizon.Hours()
	forecastSE := d.PosteriorSlopeSEBpsPerHour / 10_000 * d.ForecastHorizon.Hours()
	nullMean := estimate.ShrunkMean
	p := d.ReversalProbability
	mixtureMean := p*forecastMean + (1-p)*nullMean
	meanVariance := p*forecastSE*forecastSE + p*(1-p)*(forecastMean-nullMean)*(forecastMean-nullMean)
	meanSE := math.Sqrt(math.Max(0, meanVariance))
	cost := math.Max(0, in.RoundTripCostBps) / 10_000
	z := math.Max(0, in.ConfidenceZScore)
	netConfidenceEdge := mixtureMean - z*meanSE - cost
	if d.Direction < 0 {
		// To sell an existing long position, require even the posterior upper
		// confidence bound to remain below zero after a complete sell/re-entry
		// cost. This avoids liquidation churn on statistically ambiguous tops.
		netConfidenceEdge = mixtureMean + z*meanSE + cost
	}
	d.ForecastMeanBps = mixtureMean * 10_000
	d.ForecastMeanSEBps = meanSE * 10_000
	d.NetLowerEdgeBps = netConfidenceEdge * 10_000
	if p <= 0.5 {
		d.Reason = "reversal posterior does not beat equal odds"
		return d
	}
	riskAversion := math.Max(in.RiskAversion, 1e-12)
	denominator := riskAversion * (estimate.Variance + meanVariance)
	if denominator <= 0 {
		d.Reason = "regime allocation variance is unavailable"
		return d
	}
	minimum := math.Max(0, math.Min(in.BaselineTargetRatio, in.PolicyMinRatio))
	maximum := math.Max(in.BaselineTargetRatio, math.Min(1, in.PolicyMaxRatio))
	if !d.Early {
		d.SignalReliability = 1
	}
	switch {
	case d.Direction > 0:
		if netConfidenceEdge <= 0 {
			d.Reason = "fee-adjusted bullish lower edge is non-positive"
			return d
		}
		// The slow carrying allocation is the no-alpha prior. The tactical
		// return therefore moves that prior by a continuous, fee-soft-thresholded
		// mean/variance increment; it is not an absolute Merton allocation. The
		// latter would make an arbitrarily small negative edge jump directly to
		// the long-only lower bound.
		fullTarget := macroTacticalTarget(
			in.BaselineTargetRatio, netConfidenceEdge, denominator, minimum, maximum)
		d.RobustTargetRatio = fullTarget
		if d.Early {
			d.RobustTargetRatio = in.BaselineTargetRatio +
				d.SignalReliability*(fullTarget-in.BaselineTargetRatio)
		}
		d.TargetShiftRatio = d.RobustTargetRatio - in.BaselineTargetRatio
		if d.TargetShiftRatio <= 0 {
			d.Reason = "robust bullish target does not exceed baseline"
			return d
		}
		d.Reason = "fee-positive rolling bullish reversal"
		d.Applied = true
		if d.Early {
			d.Reason = "fee-positive sequential early bullish reversal"
		}
	case d.Direction < 0:
		if netConfidenceEdge >= 0 {
			d.Reason = "fee-adjusted bearish upper edge is non-negative"
			return d
		}
		fullTarget := macroTacticalTarget(
			in.BaselineTargetRatio, netConfidenceEdge, denominator, minimum, in.BaselineTargetRatio)
		d.RobustTargetRatio = fullTarget
		if d.Early {
			d.RobustTargetRatio = in.BaselineTargetRatio +
				d.SignalReliability*(fullTarget-in.BaselineTargetRatio)
		}
		d.TargetShiftRatio = d.RobustTargetRatio - in.BaselineTargetRatio
		if d.TargetShiftRatio >= 0 {
			d.Reason = "robust bearish target does not reduce baseline"
			return d
		}
		d.Applied = true
		d.Reason = "fee-positive rolling bearish reversal"
		if d.Early {
			d.Reason = "fee-positive sequential early bearish reversal"
		}
	}
	return d
}

func (c MacroInventoryConfig) DecideReversal(model *MacroInventoryModel, in MacroReversalInput) MacroReversalDecision {
	c.setDefaults()
	baseline := math.Max(0, math.Min(1, in.BaselineTargetRatio))
	minimum := math.Max(0, math.Min(baseline, in.PolicyMinRatio))
	maximum := math.Max(baseline, math.Min(1, in.PolicyMaxRatio))
	d := MacroReversalDecision{
		Enabled: c.ReversalAccumulation.Enabled, Reason: "disabled",
		BaselineTargetRatio: baseline, TargetRatio: baseline,
		CurrentRiskyWeight: in.CurrentRiskyWeight,
	}
	if !d.Enabled {
		return d
	}
	weightedTarget, weightedProbability, weightedEdge, weightSum := 0.0, 0.0, 0.0, 0.0
	summaries := make([]string, 0, len(c.RiskHorizons))
	allHealthy := true
	for _, horizon := range c.horizons() {
		horizonDecision := model.reversalAtHorizon(in.Now, horizon, c, in)
		d.Horizons = append(d.Horizons, horizonDecision)
		summaries = append(summaries, fmt.Sprintf("%s:dir=%+d p=%.3f edge=%.2fbps target=%.4f %s",
			horizon, horizonDecision.Direction, horizonDecision.ReversalProbability,
			horizonDecision.NetLowerEdgeBps, horizonDecision.RobustTargetRatio, horizonDecision.Reason))
		healthy := horizonDecision.Bars >= 8 &&
			horizonDecision.EffectiveSamples >= float64(c.MinimumSamples)
		if !healthy {
			allHealthy = false
			continue
		}
		d.HealthyHorizons++
		weight := horizonDecision.EffectiveSamples /
			(horizonDecision.EffectiveSamples + c.DriftPriorSamples)
		// Model-average every statistically healthy horizon. A healthy horizon
		// that has no fee-positive turn votes for the no-alpha baseline and equal
		// odds, instead of being dropped after selection. This prevents one noisy
		// significant window from receiving 100% of the aggregate weight.
		target := baseline
		probability := 0.5
		edge := 0.0
		if horizonDecision.Applied {
			target = horizonDecision.RobustTargetRatio
			probability = horizonDecision.ReversalProbability
			edge = horizonDecision.NetLowerEdgeBps
			d.ActiveHorizons++
			if horizonDecision.Early {
				d.EarlyHorizons++
			}
		}
		weightedTarget += weight * target
		weightedProbability += weight * probability
		weightedEdge += weight * edge
		weightSum += weight
	}
	d.Healthy = allHealthy
	d.HorizonSummary = strings.Join(summaries, "; ")
	if weightSum <= 0 || d.ActiveHorizons == 0 {
		d.Reason = "no fee-positive rolling reversal horizon"
		return d
	}
	d.AggregateProbability = weightedProbability / weightSum
	d.AggregateNetEdgeBps = weightedEdge / weightSum
	regimeTarget := math.Max(minimum, math.Min(maximum, weightedTarget/weightSum))
	d.TargetRatio = regimeTarget
	switch {
	case regimeTarget > baseline+1e-12:
		d.Direction = 1
		d.Reason = "multi-horizon rolling bullish reversal target"
	case regimeTarget < baseline-1e-12:
		d.Direction = -1
		d.Reason = "multi-horizon rolling bearish reversal target"
	default:
		d.Reason = "opposing reversal horizons net to baseline"
		return d
	}
	dominantWeight := -1.0
	for _, horizonDecision := range d.Horizons {
		if !horizonDecision.Applied || horizonDecision.Direction != d.Direction {
			continue
		}
		weight := horizonDecision.EffectiveSamples /
			(horizonDecision.EffectiveSamples + c.DriftPriorSamples)
		if weight > dominantWeight {
			dominantWeight = weight
			d.SignalChangeAt = horizonDecision.ChangeAt
			d.SignalForecastHorizon = horizonDecision.ForecastHorizon
		}
	}
	d.InventoryAdjustmentRatio = d.TargetRatio - in.CurrentRiskyWeight
	d.AdditionalHeadroomRatio = math.Max(0, d.InventoryAdjustmentRatio)
	d.Applied = !d.SignalChangeAt.IsZero() && d.SignalForecastHorizon > 0
	return d
}

// ApplyRegimeLease filters a fee-positive structural signal through a
// memoryless regime-survival prior. The model-derived forecast horizon is the
// expected regime lifetime, so persistence has no fixed timeout or fitted
// decay coefficient. A missing signal decays continuously instead of forcing
// an instantaneous return to the no-alpha carrying cap.
func (s *MacroInventoryState) ApplyRegimeLease(now time.Time, policyMinRatio, policyMaxRatio float64, d MacroReversalDecision) (MacroReversalDecision, bool) {
	if s == nil || now.IsZero() {
		return d, false
	}
	minimum := math.Max(0, math.Min(d.BaselineTargetRatio, policyMinRatio))
	maximum := math.Max(d.BaselineTargetRatio, math.Min(1, policyMaxRatio))
	changed := false
	if d.Applied && d.Direction != 0 && d.SignalForecastHorizon > 0 && !d.SignalChangeAt.IsZero() {
		newEpisode := s.RegimeDirection != d.Direction || !s.RegimeChangeAt.Equal(d.SignalChangeAt)
		if newEpisode || s.RegimeActivatedAt.IsZero() {
			s.RegimeActivatedAt = now
			changed = true
		}
		if s.RegimeDirection != d.Direction || s.RegimeTargetRatio != d.TargetRatio ||
			s.RegimeForecastHorizon != d.SignalForecastHorizon ||
			!s.RegimeChangeAt.Equal(d.SignalChangeAt) || s.RegimeProbability != d.AggregateProbability ||
			s.RegimeNetEdgeBps != d.AggregateNetEdgeBps {
			changed = true
		}
		s.RegimeDirection = d.Direction
		s.RegimeTargetRatio = d.TargetRatio
		s.RegimeForecastHorizon = d.SignalForecastHorizon
		s.RegimeChangeAt = d.SignalChangeAt
		s.RegimeProbability = d.AggregateProbability
		s.RegimeNetEdgeBps = d.AggregateNetEdgeBps
		d.LeaseSurvivalProbability = 1
		return d, changed
	}
	if s.RegimeActivatedAt.IsZero() || s.RegimeForecastHorizon <= 0 ||
		math.Abs(s.RegimeTargetRatio-d.BaselineTargetRatio) <= 1e-12 {
		return d, false
	}
	age := now.Sub(s.RegimeActivatedAt)
	if age < 0 {
		age = 0
	}
	survival := math.Exp(-age.Seconds() / s.RegimeForecastHorizon.Seconds())
	target := d.BaselineTargetRatio + survival*(s.RegimeTargetRatio-d.BaselineTargetRatio)
	target = math.Max(minimum, math.Min(maximum, target))
	if math.Abs(target-d.BaselineTargetRatio) <= 1e-9 {
		return d, false
	}
	d.Direction = s.RegimeDirection
	d.TargetRatio = target
	d.InventoryAdjustmentRatio = target - d.CurrentRiskyWeight
	d.AdditionalHeadroomRatio = math.Max(0, d.InventoryAdjustmentRatio)
	d.AggregateProbability = survival * s.RegimeProbability
	d.AggregateNetEdgeBps = survival * s.RegimeNetEdgeBps
	d.Applied = true
	d.LeaseApplied = true
	d.LeaseAge = age
	d.LeaseSurvivalProbability = survival
	d.SignalChangeAt = s.RegimeChangeAt
	d.SignalForecastHorizon = s.RegimeForecastHorizon
	d.Reason = "posterior regime-survival lease"
	return d, false
}
