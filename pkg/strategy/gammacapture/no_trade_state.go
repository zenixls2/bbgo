package gammacapture

import (
	"math"
	"time"
)

// ApplyNoTradeState turns the instantaneous QV-time aim into a causal Macro
// control. The scalar Kalman covariance is expressed in risky-weight squared.
// Its process variance is the fraction of the rolling crossing observation
// replaced by the newest closed Macro bar, so no hand-tuned smoothing weight or
// clock-time cooldown is required.
//
// After filtering, the cost-derived no-trade boundaries are applied directly.
// Direction is intentionally stateless: posterior filtering owns temporal
// stability, while execution retains the original single-boundary policy.
func (s *MacroInventoryState) ApplyNoTradeState(
	now, closedBarAt time.Time,
	in NoTradeInventoryInput,
	d NoTradeInventoryDecision,
) (NoTradeInventoryDecision, bool) {
	if s == nil || !d.Enabled || closedBarAt.IsZero() {
		return d, false
	}
	minimum := math.Max(0, math.Min(1, in.PolicyMinRatio))
	maximum := math.Max(minimum, math.Min(1, in.PolicyMaxRatio))
	prior := clampRatio(in.PriorTargetRatio, minimum, maximum)
	current := clampRatio(in.CurrentRiskyWeight, minimum, maximum)
	changed := false
	modeChanged := s.NoTradeContinuationMixture != d.ContinuationMixtureApplied &&
		(!s.NoTradeAimUpdatedAt.IsZero() || !s.NoTradeAimClosedBarAt.IsZero())
	if modeChanged {
		s.NoTradeFilteredAimRatio = 0
		s.NoTradeAimVariance = 0
		s.NoTradeAimUpdatedAt = time.Time{}
		s.NoTradeAimClosedBarAt = time.Time{}
		changed = true
	}
	s.NoTradeContinuationMixture = d.ContinuationMixtureApplied

	if !d.Healthy {
		resetAim := prior
		if d.FastRiskReentryCapApplied && current < resetAim {
			resetAim = current
		}
		changed = changed || s.NoTradeFilteredAimRatio != resetAim || s.NoTradeAimVariance != 0 ||
			!s.NoTradeAimClosedBarAt.Equal(closedBarAt)
		s.NoTradeFilteredAimRatio = resetAim
		s.NoTradeAimVariance = 0
		s.NoTradeAimUpdatedAt = now
		s.NoTradeAimClosedBarAt = closedBarAt
		d.AimRatio = resetAim
		d.AimFilterVariance = 0
		d.AimKalmanGain = 0
		d.AimUpdatedAt = now
		materializeNoTradeInventory(&d, in, minimum, maximum, current)
		return d, changed
	}

	rawAim := clampRatio(d.RawAimRatio, minimum, maximum)
	measurementVariance := math.Max(0, d.AimMeasurementVariance)
	if s.NoTradeAimUpdatedAt.IsZero() || s.NoTradeAimClosedBarAt.IsZero() {
		s.NoTradeFilteredAimRatio = rawAim
		s.NoTradeAimVariance = measurementVariance
		s.NoTradeAimUpdatedAt = now
		s.NoTradeAimClosedBarAt = closedBarAt
		d.AimKalmanGain = 1
		changed = true
	} else if s.NoTradeAimVariance <= 0 && measurementVariance > 0 {
		// Versions before the covariance-preserving hold projection persisted an
		// exact zero here. A healthy noisy posterior cannot have zero covariance;
		// restore uncertainty without moving the persisted mean or pretending a
		// second measurement arrived inside the same closed bar.
		s.NoTradeAimVariance = measurementVariance
		changed = true
	} else if closedBarAt.After(s.NoTradeAimClosedBarAt) {
		elapsed := closedBarAt.Sub(s.NoTradeAimClosedBarAt)
		observation := d.ForecastObservation
		if observation <= 0 {
			observation = in.Observed
		}
		replacedFraction := 1.0
		if observation > 0 {
			replacedFraction = clampRatio(elapsed.Seconds()/observation.Seconds(), 0, 1)
		}
		priorVariance := math.Max(0, s.NoTradeAimVariance)
		processVariance := replacedFraction * math.Max(priorVariance, measurementVariance)
		trendDirection := d.TrendExcursion.Direction
		trendProbability := clampRatio(d.TrendExcursion.ModelProbability, 0, 1)
		if trendDirection != 0 && trendDirection != s.NoTradeTrendDirection {
			// A posterior regime change invalidates the random-walk continuity
			// assumption. The innovation supplies the missing change-point process
			// variance, so strong early entries and terminal exits update quickly
			// without globally shortening the smoothing horizon.
			innovation := rawAim - s.NoTradeFilteredAimRatio
			processVariance += trendProbability * innovation * innovation
		}
		predictedVariance := priorVariance + processVariance
		gain := 1.0
		if totalVariance := predictedVariance + measurementVariance; totalVariance > 0 {
			gain = predictedVariance / totalVariance
		}
		s.NoTradeFilteredAimRatio = clampRatio(
			s.NoTradeFilteredAimRatio+gain*(rawAim-s.NoTradeFilteredAimRatio),
			minimum, maximum)
		s.NoTradeAimVariance = math.Max(0, (1-gain)*predictedVariance)
		s.NoTradeAimUpdatedAt = now
		s.NoTradeAimClosedBarAt = closedBarAt
		s.NoTradeTrendDirection = trendDirection
		s.NoTradeTrendProbability = trendProbability
		d.AimKalmanGain = gain
		changed = true
	}

	filteredAim := clampRatio(s.NoTradeFilteredAimRatio, minimum, maximum)
	if d.HoldProtectionApplied {
		// A rejected directional target must not leak through the persisted
		// Kalman mean. Project the mean to current inventory immediately, but
		// preserve the posterior covariance: declining to trade is a control
		// decision, not a zero-noise observation. Clearing the covariance here
		// made the filterVariance diagnostic falsely report zero and made the
		// next closed-bar update overconfident. Hard volatility and capital-risk
		// reductions are applied below.
		protectedAim := clampRatio(in.CurrentRiskyWeight, minimum, maximum)
		if filteredAim != protectedAim {
			filteredAim = protectedAim
			s.NoTradeFilteredAimRatio = protectedAim
			changed = true
		}
	}
	if d.FastRiskDenominatorScale > 0 && d.FastRiskDenominatorScale < 1 {
		// Kalman state contains the base-QV aim. Apply the independently
		// forecast HAR risk denominator after filtering so a volatility shock
		// de-risks immediately and cannot compound into the persistent state.
		filteredAim = clampRatio(filteredAim*d.FastRiskDenominatorScale, minimum, maximum)
	}
	if d.FastRiskReentryCapApplied {
		capRatio := clampRatio(in.CurrentRiskyWeight, minimum, maximum)
		if filteredAim > capRatio {
			filteredAim = capRatio
			s.NoTradeFilteredAimRatio = capRatio
			changed = true
		}
	}
	if d.ContinuationCapApplied {
		capRatio := clampRatio(d.ContinuationCapRatio, minimum, maximum)
		if filteredAim > capRatio {
			// Euclidean projection is the MAP update for the scalar Gaussian
			// state under the convex inequality w <= cap. Keep covariance
			// conservative; only the mean is projected.
			filteredAim = capRatio
			s.NoTradeFilteredAimRatio = capRatio
			changed = true
		}
	}
	if d.FastRiskDenominatorScale == 0 && filteredAim != s.NoTradeFilteredAimRatio {
		s.NoTradeFilteredAimRatio = filteredAim
		changed = true
	}
	d.AimRatio = filteredAim
	d.AimFilterVariance = math.Max(0, s.NoTradeAimVariance)
	d.AimUpdatedAt = s.NoTradeAimUpdatedAt
	materializeNoTradeInventory(&d, in, minimum, maximum, current)

	return d, changed
}
