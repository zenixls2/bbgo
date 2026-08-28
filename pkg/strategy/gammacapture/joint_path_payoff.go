package gammacapture

import (
	"math"
	"sort"
	"time"
)

type jointPathPayoffMoments struct {
	BuyMeanBps                  float64
	SellMeanBps                 float64
	InventoryMeanBps            float64
	InventoryDirectionalMeanBps float64
	BuyVarBps2                  float64
	SellVarBps2                 float64
	InventoryVarBps2            float64
	InventoryDirectionalVarBps2 float64
	CovBps2                     float64
	InventoryBuyCovBps2         float64
	InventorySellCovBps2        float64
}

type JointPathPayoffStats struct {
	EffectiveSamples float64
	// EffectiveSamplesBaseline is an online EWMA of previously observed
	// effective path mass. It is a stability reference only; the current
	// EffectiveSamples remains the authoritative causal count.
	EffectiveSamplesBaseline         float64
	EffectiveSamplesBaselineStd      float64
	PathDecayHalfLifeSeconds         float64
	PathDecayAutocorrelation         float64
	PathDecayPersistenceObservations float64
	BuyDominant                      jointPathPayoffMoments
	SellDominant                     jointPathPayoffMoments
	InventoryTarget                  jointPathPayoffMoments
	InventoryTargetEffectiveSamples  float64
	TwoStageContinuation             bool
	ContinuationHorizon              time.Duration
	BuyThenSellSamples               float64
	BuyThenSellCompletions           float64
	SellThenBuySamples               float64
	SellThenBuyCompletions           float64
}

// JointPathMaturityDecision separates statistical incompleteness from an
// economically negative terminal-wealth estimate. EffectiveSamples is the
// weighted information mass of completed overlapping paths, not their raw
// row count.
type JointPathMaturityDecision struct {
	Matured                bool
	Reason                 string
	EffectiveSamples       float64
	ConfidenceHalfWidthBps float64
	ReferenceScaleBps      float64
	RelativeHalfWidth      float64
}

// AssessJointPathMaturity applies one causal precision rule to every joint
// distance candidate. It uses only completed path moments already available at
// quote time. Crossing health and the later terminal-wealth sign/CE test remain
// separate owners; a mature path can therefore still be rejected as negative.
func AssessJointPathMaturity(stats JointPathPayoffStats, config MarketMakerConfig, zScore float64) JointPathMaturityDecision {
	if zScore <= 0 || math.IsNaN(zScore) || math.IsInf(zScore, 0) {
		zScore = 1.645
	}
	decision := JointPathMaturityDecision{
		EffectiveSamples: stats.EffectiveSamples,
		Reason:           "terminal-path effective samples are not non-degenerate",
	}
	effective := stats.EffectiveSamples
	if stats.InventoryTargetEffectiveSamples > 0 {
		effective = math.Min(effective, stats.InventoryTargetEffectiveSamples)
	}
	decision.EffectiveSamples = effective
	if effective <= 1 || math.IsNaN(effective) || math.IsInf(effective, 0) {
		return decision
	}
	buy, sell, target := stats.BuyDominant, stats.SellDominant, stats.InventoryTarget
	variance := math.Max(buy.BuyVarBps2, buy.SellVarBps2)
	variance = math.Max(variance, math.Max(sell.BuyVarBps2, sell.SellVarBps2))
	variance = math.Max(variance, target.InventoryVarBps2)
	if variance < 0 || math.IsNaN(variance) || math.IsInf(variance, 0) {
		decision.Reason = "terminal-path variance is invalid"
		return decision
	}
	decision.ConfidenceHalfWidthBps = zScore * math.Sqrt(variance/effective)
	// When the current path mass falls below its own causal trailing baseline,
	// add only the variance uncertainty implied by that information loss. This
	// is a continuous surcharge, not another sample-count gate. If the current
	// mass is above baseline, no penalty is applied.
	if stats.EffectiveSamplesBaseline > effective && stats.EffectiveSamplesBaseline > 1 {
		baselineTerm := 1/math.Sqrt(effective) - 1/math.Sqrt(stats.EffectiveSamplesBaseline)
		if baselineTerm > 0 {
			decision.ConfidenceHalfWidthBps += zScore * math.Sqrt(variance) * baselineTerm
		}
	}
	meanScale := math.Max(math.Abs(buy.BuyMeanBps), math.Abs(buy.SellMeanBps))
	meanScale = math.Max(meanScale, math.Max(math.Abs(sell.BuyMeanBps), math.Abs(sell.SellMeanBps)))
	meanScale = math.Max(meanScale, math.Abs(target.InventoryMeanBps))
	costScale := config.MakerFeeBps + config.AdverseSelectionBps + config.MinimumNetEdgeBps
	if costScale < 1 {
		costScale = 1
	}
	decision.ReferenceScaleBps = math.Max(costScale, meanScale)
	if math.IsNaN(decision.ReferenceScaleBps) || math.IsInf(decision.ReferenceScaleBps, 0) || decision.ReferenceScaleBps <= 0 {
		decision.Reason = "terminal-path economic scale is invalid"
		return decision
	}
	decision.RelativeHalfWidth = decision.ConfidenceHalfWidthBps / decision.ReferenceScaleBps
	maxRelative := config.JointDistanceQuantity.PathMaturityMaxRelativeHalfWidth
	if maxRelative <= 0 || math.IsNaN(maxRelative) || math.IsInf(maxRelative, 0) {
		maxRelative = 1
	}
	if math.IsNaN(decision.RelativeHalfWidth) || math.IsInf(decision.RelativeHalfWidth, 0) {
		decision.Reason = "terminal-path confidence width is invalid"
		return decision
	}
	if decision.RelativeHalfWidth > maxRelative {
		decision.Reason = "terminal-path confidence width exceeds maturity scale"
		return decision
	}
	decision.Matured = true
	decision.Reason = "terminal-path posterior is mature"
	return decision
}

type JointPathPayoffDecision struct {
	ExpectedPnLJPY float64
	// StdErrorJPY is the incremental order-payoff SE retained for path
	// diagnostics and legacy callers. LowerPnLJPY uses the target-relative
	// whole-vs-baseline SE difference below.
	StdErrorJPY                     float64
	BaselineStdErrorJPY             float64
	WholePositionStdErrorJPY        float64
	IncrementalStdErrorJPY          float64
	LowerPnLJPY                     float64
	ExistingInventoryExpectedPnLJPY float64
	TargetInventoryNotionalJPY      float64
	RiskInventoryNotionalJPY        float64
	BaselineVarianceJPY2            float64
	WholePositionVarianceJPY2       float64
	MarginalVarianceJPY2            float64
	InventoryOrderCovarianceJPY2    float64
	KellyPenaltyJPY                 float64
	CertaintyEquivalent             float64
	RiskReducing                    bool
}

type JointPathPayoffDifferenceDecision struct {
	Evaluated        bool
	MeanBps          float64
	StdErrorBps      float64
	EffectiveSamples float64
}

// makerFillTerminalWealthBps is the one-fill change in terminal liquidatable
// wealth relative to leaving the corresponding quote notional in its pre-fill
// asset. Both cases mark terminal base inventory at the executable bid. A SELL
// must not use terminal ask here: doing so inserts a hypothetical repurchase
// that neither occurred nor paid its second fee.
func makerFillTerminalWealthBps(buy bool, quote, terminalBid, entryCostBps float64) float64 {
	if quote <= 0 || terminalBid <= 0 {
		return 0
	}
	if buy {
		return math.Log(terminalBid/quote)*10_000 - entryCostBps
	}
	return math.Log(quote/terminalBid)*10_000 - entryCostBps
}

type weightedJointMoments struct {
	weight, weightSquared                      float64
	buy, sell, inventory, inventoryDirectional float64
	buySquared                                 float64
	sellSquared                                float64
	inventorySquared                           float64
	inventoryDirectionalSquared                float64
	buySell                                    float64
	inventoryBuy                               float64
	inventorySell                              float64
}

func (m *weightedJointMoments) add(weight, buy, sell, inventory, inventoryDirectional float64) {
	if weight <= 0 {
		return
	}
	m.weight += weight
	m.weightSquared += weight * weight
	m.buy += weight * buy
	m.sell += weight * sell
	m.inventory += weight * inventory
	m.inventoryDirectional += weight * inventoryDirectional
	m.buySquared += weight * buy * buy
	m.sellSquared += weight * sell * sell
	m.inventorySquared += weight * inventory * inventory
	m.inventoryDirectionalSquared += weight * inventoryDirectional * inventoryDirectional
	m.buySell += weight * buy * sell
	m.inventoryBuy += weight * inventory * buy
	m.inventorySell += weight * inventory * sell
}

func (m weightedJointMoments) result() (jointPathPayoffMoments, float64) {
	if m.weight <= 0 {
		return jointPathPayoffMoments{}, 0
	}
	buyMean, sellMean := m.buy/m.weight, m.sell/m.weight
	inventoryMean := m.inventory / m.weight
	inventoryDirectionalMean := m.inventoryDirectional / m.weight
	effective := m.weight
	if m.weightSquared > 0 {
		effective = math.Min(effective, m.weight*m.weight/m.weightSquared)
	}
	// Apply the effective-sample Bessel correction. The confidence bound in
	// Evaluate then widens continuously as independent path evidence becomes
	// scarce, so callers do not need a second arbitrary hard sample gate.
	varianceCorrection := 1.0
	if effective > 1 {
		varianceCorrection = effective / (effective - 1)
	}
	out := jointPathPayoffMoments{
		BuyMeanBps:                  buyMean,
		SellMeanBps:                 sellMean,
		InventoryMeanBps:            inventoryMean,
		InventoryDirectionalMeanBps: inventoryDirectionalMean,
		BuyVarBps2: math.Max(0,
			(m.buySquared/m.weight-buyMean*buyMean)*varianceCorrection),
		SellVarBps2: math.Max(0,
			(m.sellSquared/m.weight-sellMean*sellMean)*varianceCorrection),
		InventoryVarBps2: math.Max(0,
			(m.inventorySquared/m.weight-inventoryMean*inventoryMean)*varianceCorrection),
		InventoryDirectionalVarBps2: math.Max(0,
			(m.inventoryDirectionalSquared/m.weight-inventoryDirectionalMean*inventoryDirectionalMean)*varianceCorrection),
		CovBps2:              (m.buySell/m.weight - buyMean*sellMean) * varianceCorrection,
		InventoryBuyCovBps2:  (m.inventoryBuy/m.weight - inventoryMean*buyMean) * varianceCorrection,
		InventorySellCovBps2: (m.inventorySell/m.weight - inventoryMean*sellMean) * varianceCorrection,
	}
	return out, effective
}

func (m *MarketMakerHorizonModel) bookImbalanceAt(now time.Time) (float64, bool) {
	if m == nil || now.IsZero() || len(m.points) == 0 {
		return 0, false
	}
	index := sort.Search(len(m.points), func(index int) bool {
		return m.points[index].At.After(now)
	}) - 1
	if index < 0 || !m.points[index].BookDepthReady {
		return 0, false
	}
	return clampBookImbalance(m.points[index].BookImbalance), true
}

// applySideImbalancePayoffMean changes only the mean contribution of a
// one-sided terminal payoff. The crossing probability, variance, inventory
// target, price ladder, and quantity controller retain their existing owners.
// The multiplier is the empirical probability of the corresponding one-sided
// path under the same weights as the parent payoff moments.
func applySideImbalancePayoffMean(
	mean *float64,
	totalWeight, oneSidedWeight float64,
	regression sideImbalanceSufficientStats,
	imbalance float64,
) {
	if mean == nil || totalWeight <= 0 || oneSidedWeight <= 0 || regression.weight <= 1 {
		return
	}
	baseline, conditional := regression.predict(clampBookImbalance(imbalance))
	*mean += math.Min(1, oneSidedWeight/totalWeight) * (conditional - baseline)
}

func sideImbalancePayoffEnabled(horizon time.Duration) bool {
	return horizon == 15*time.Minute
}

// applyVolumeProfileSideTerminalRisk changes only the side-specific terminal
// payoff means. Crossing probabilities, variances, inventory targets, and
// quote distances keep their existing owners; quantity therefore responds to
// the same unified certainty-equivalent model instead of a second hard gate.
// The same side adjustment is applied to both dominant path mixtures because
// either mixture can be selected by EvaluateTargetRelativePosition.
func applyVolumeProfileSideTerminalRisk(stats *JointPathPayoffStats, state VolumeProfileState, zScore float64) {
	if stats == nil || !state.Valid {
		return
	}
	buyPenalty, buyVariance := state.SideTerminalRiskMomentsWithZScore(true, zScore)
	sellPenalty, sellVariance := state.SideTerminalRiskMomentsWithZScore(false, zScore)
	if buyPenalty > 0 {
		stats.BuyDominant.BuyMeanBps -= buyPenalty
		stats.SellDominant.BuyMeanBps -= buyPenalty
	}
	if buyVariance > 0 {
		stats.BuyDominant.BuyVarBps2 += buyVariance
		stats.SellDominant.BuyVarBps2 += buyVariance
	}
	if sellPenalty > 0 {
		stats.BuyDominant.SellMeanBps -= sellPenalty
		stats.SellDominant.SellMeanBps -= sellPenalty
	}
	if sellVariance > 0 {
		stats.BuyDominant.SellVarBps2 += sellVariance
		stats.SellDominant.SellVarBps2 += sellVariance
	}
}

// JointPathPayoffStatistics values a completed maker window in terminal
// executable wealth, rather than assuming that every touch earns the quoted
// spread. A one-sided fill is compared with terminal-bid liquidatable wealth
// and charged its one actual fill cost. When both sides touch, matched notional
// earns the realized quote-to-quote cycle; only the unmatched residual is
// terminal-marked.
// This directly penalizes selling into a continued rise or buying into a
// continued decline while retaining profitable two-sided oscillation.
func (m *MarketMakerHorizonModel) JointPathPayoffStatistics(
	now time.Time,
	config MarketMakerConfig,
	horizon time.Duration,
	buyDistanceBps, sellDistanceBps float64,
) JointPathPayoffStats {
	// Once the public-trade profile is mature, use it at the same historical
	// path-weighting point for every Fast candidate. This keeps VP from being
	// silently limited to the rare inward-distance branch while preserving the
	// exact legacy path whenever the profile is disabled or still warming.
	if config.VolumeProfile.Enabled {
		if current := m.conditionalExecutionState(horizon); current.Valid && current.VolumeProfile.Valid {
			return m.jointPathPayoffStatistics(
				now, config, horizon, buyDistanceBps, sellDistanceBps, &current)
		}
	}
	return m.jointPathPayoffStatistics(
		now, config, horizon, buyDistanceBps, sellDistanceBps, nil)
}

func (m *MarketMakerHorizonModel) conditionalJointPathPayoffStatistics(
	now time.Time,
	config MarketMakerConfig,
	horizon time.Duration,
	buyDistanceBps, sellDistanceBps float64,
	current conditionalExecutionState,
) JointPathPayoffStats {
	if !current.Valid {
		return m.JointPathPayoffStatistics(now, config, horizon, buyDistanceBps, sellDistanceBps)
	}
	return m.jointPathPayoffStatistics(
		now, config, horizon, buyDistanceBps, sellDistanceBps, &current)
}

// adaptivePathDecaySnapshot updates only with completed, distance-independent
// paths.  It is intentionally separate from quote-distance scoring: every
// candidate observes the same decay state and therefore cannot select a decay
// factor after seeing its own payoff.
func (m *MarketMakerHorizonModel) adaptivePathDecaySnapshot(
	now time.Time,
	config MarketMakerConfig,
	horizon time.Duration,
	exposures []marketMakerHorizonExposure,
) adaptivePathDecaySnapshot {
	if m == nil || now.IsZero() || horizon <= 0 || !config.JointDistanceQuantity.AdaptivePathDecay {
		return adaptivePathDecaySnapshot{}
	}
	if m.pathDecay == nil {
		m.pathDecay = make(map[time.Duration]*adaptivePathDecayState)
	}
	state := m.pathDecay[horizon]
	if state == nil {
		state = &adaptivePathDecayState{}
		m.pathDecay[horizon] = state
	}
	completionHorizon := jointContinuationHorizon(config, horizon)
	for _, exposure := range exposures {
		maturity := exposure.EndAt
		if config.JointDistanceQuantity.TwoStageContinuation {
			maturity = exposure.At.Add(horizon + completionHorizon)
		}
		if maturity.After(now) {
			break
		}
		if !state.LastMaturedAt.IsZero() && !exposure.At.After(state.LastMaturedAt) {
			continue
		}
		if exposure.StartBid <= 0 || exposure.StartAsk <= 0 ||
			exposure.TerminalBid <= 0 || exposure.TerminalAsk <= 0 {
			continue
		}
		// Use the two executable sides separately: BUY volatility observes ask
		// and SELL volatility observes bid.  The larger absolute terminal side
		// return is a conservative volatility-persistence label; no midpoint or
		// signed directional information is consumed. Cap only pathological feed
		// values so one bad tick cannot make a six-hour half-life permanent.
		buyValue := math.Abs(math.Log(exposure.TerminalAsk/exposure.StartAsk) * 10_000)
		sellValue := math.Abs(math.Log(exposure.TerminalBid/exposure.StartBid) * 10_000)
		value := math.Max(buyValue, sellValue)
		if value > 10_000 {
			value = 10_000
		}
		state.observePath(exposure.At, value, horizon, time.Duration(config.HorizonLookback))
	}
	return state.snapshot(horizon, time.Duration(config.HorizonLookback))
}

func (m *MarketMakerHorizonModel) jointPathPayoffStatistics(
	now time.Time,
	config MarketMakerConfig,
	horizon time.Duration,
	buyDistanceBps, sellDistanceBps float64,
	current *conditionalExecutionState,
) JointPathPayoffStats {
	if m == nil || now.IsZero() || horizon <= 0 ||
		buyDistanceBps <= 0 || sellDistanceBps <= 0 {
		return JointPathPayoffStats{}
	}
	config.setDefaults()
	completionHorizon := jointContinuationHorizon(config, horizon)
	exposures := m.crossingExposures(horizon)
	decaySnapshot := m.adaptivePathDecaySnapshot(now, config, horizon, exposures)
	var bboIndex *marketMakerBBORangeIndex
	if config.JointDistanceQuantity.TwoStageContinuation {
		bboIndex = m.executableBBORangeIndex()
	}
	lookback := time.Duration(config.HorizonLookback)
	cutoff := now.Add(-lookback)
	// Sparse-market path payoffs are non-stationary. The half-life is estimated
	// from the causal lag-one persistence of matured executable-BBO path
	// volatility; the old sqrt(H*L) scale is used only before the estimator has
	// eight lagged pairs.
	decayHalfLifeSeconds := decaySnapshot.HalfLifeSeconds
	if decayHalfLifeSeconds <= 0 {
		decayHalfLifeSeconds = math.Sqrt(horizon.Seconds() * lookback.Seconds())
	}
	var buyDominant, sellDominant, inventoryTarget weightedJointMoments
	var buyDominantBuyOnly, buyDominantSellOnly sideImbalanceSufficientStats
	var sellDominantBuyOnly, sellDominantSellOnly sideImbalanceSufficientStats
	var buyDominantBuyOnlyWeight, buyDominantSellOnlyWeight float64
	var sellDominantBuyOnlyWeight, sellDominantSellOnlyWeight float64
	var buyThenSellSamples, buyThenSellCompletions float64
	var sellThenBuySamples, sellThenBuyCompletions float64
	var lastExposure time.Time
	entryCostBps := config.MakerFeeBps + config.AdverseSelectionBps
	cycleCostBps := 2*entryCostBps + config.MinimumNetEdgeBps
	globalCount := 0
	if current != nil {
		for index := firstHorizonExposureAtOrAfter(exposures, cutoff); index < len(exposures); {
			maturity := exposures[index].EndAt
			if config.JointDistanceQuantity.TwoStageContinuation {
				maturity = exposures[index].At.Add(horizon + completionHorizon)
			}
			if maturity.After(now) {
				break
			}
			globalCount++
			if exposures[index].NextMinute <= index {
				break
			}
			index = exposures[index].NextMinute
		}
	}
	priorPerPath := 0.0
	if globalCount > 0 {
		priorPerPath = 1 / math.Sqrt(float64(globalCount))
	}
	for index := firstHorizonExposureAtOrAfter(exposures, cutoff); index < len(exposures); {
		exposure := exposures[index]
		maturity := exposure.EndAt
		if config.JointDistanceQuantity.TwoStageContinuation {
			maturity = exposure.At.Add(horizon + completionHorizon)
		}
		if maturity.After(now) {
			break
		}
		if exposure.StartBid <= 0 || exposure.StartAsk < exposure.StartBid ||
			exposure.TerminalBid <= 0 || exposure.TerminalAsk < exposure.TerminalBid {
			if exposure.NextMinute <= index {
				break
			}
			index = exposure.NextMinute
			continue
		}
		weight := 1.0
		if !lastExposure.IsZero() {
			weight = math.Min(1, exposure.At.Sub(lastExposure).Seconds()/horizon.Seconds())
		}
		if decayHalfLifeSeconds > 0 {
			ageSeconds := math.Max(0, now.Sub(maturity).Seconds())
			weight *= math.Exp(-math.Ln2 * ageSeconds / decayHalfLifeSeconds)
		}
		if weight <= 0 {
			if exposure.NextMinute <= index {
				break
			}
			index = exposure.NextMinute
			continue
		}
		buyTouched := exposure.BuyExcursionBps >= buyDistanceBps
		sellTouched := exposure.SellExcursionBps >= sellDistanceBps
		// Retain the intentionally conservative start-mark-to-terminal-bid
		// liquidation return for whole-position covariance and downside stress.
		startMid := math.Sqrt(exposure.StartBid * exposure.StartAsk)
		inventoryReturnBps := math.Log(exposure.TerminalBid/startMid) * 10_000
		// Directional inventory targeting is a different estimand: compare the
		// current depth-weighted BBO price with the time-weighted depth-weighted
		// BBO mean over the completed future window. This neither mistakes one
		// terminal tick for the horizon mean nor embeds the liquidation spread
		// haircut in the target direction.
		startWeightedPrice := exposure.StartBBOWeightedPrice
		if startWeightedPrice <= 0 {
			startWeightedPrice = startMid
		}
		windowWeightedPrice := exposure.WindowBBOWeightedPrice
		if windowWeightedPrice <= 0 {
			// Compatibility for synthetic unit fixtures created before completed
			// exposures retained their BBO-weighted window mean.
			windowWeightedPrice = math.Sqrt(exposure.TerminalBid * exposure.TerminalAsk)
		}
		inventoryDirectionalReturnBps := math.Log(
			windowWeightedPrice/startWeightedPrice) * 10_000
		bidQuote := exposure.StartAsk * math.Exp(-buyDistanceBps/10_000)
		askQuote := exposure.StartBid * math.Exp(sellDistanceBps/10_000)
		pathTerminalBid := exposure.TerminalBid
		completion := false
		if config.JointDistanceQuantity.TwoStageContinuation && buyTouched != sellTouched {
			var valid bool
			pathTerminalBid, completion, valid = m.twoStageCompletionOutcome(
				bboIndex, exposure, completionHorizon, bidQuote, askQuote, buyTouched)
			if !valid {
				if exposure.NextMinute <= index {
					break
				}
				index = exposure.NextMinute
				continue
			}
			if buyTouched {
				buyThenSellSamples += weight
				if completion {
					buyThenSellCompletions += weight
					sellTouched = true
				}
			} else {
				sellThenBuySamples += weight
				if completion {
					sellThenBuyCompletions += weight
					buyTouched = true
				}
			}
			inventoryReturnBps = math.Log(pathTerminalBid/startMid) * 10_000
		}
		buySingleBps, sellSingleBps := 0.0, 0.0
		if buyTouched {
			// Terminal executable wealth contains one completed BUY, hence one
			// maker fee. Charging a hypothetical future exit here double-counts
			// cost and systematically suppresses inventory carried past H.
			buySingleBps = makerFillTerminalWealthBps(
				true, bidQuote, pathTerminalBid, entryCostBps)
		}
		if sellTouched {
			// The unfilled counterfactual still owns base and can liquidate only
			// at terminal bid. Terminal ask belongs solely to an explicitly
			// modeled future repurchase cycle.
			sellSingleBps = makerFillTerminalWealthBps(
				false, askQuote, pathTerminalBid, entryCostBps)
		}
		buyDomBuy, buyDomSell := 0.0, 0.0
		sellDomBuy, sellDomSell := 0.0, 0.0
		switch {
		case buyTouched && sellTouched:
			cycleNetBps := math.Log(askQuote/bidQuote)*10_000 - cycleCostBps
			// If qB >= qS, qS is a completed cycle and qB-qS remains
			// exposed to the terminal bid. The other region is symmetric.
			buyDomBuy = buySingleBps
			buyDomSell = cycleNetBps - buySingleBps
			sellDomBuy = cycleNetBps - sellSingleBps
			sellDomSell = sellSingleBps
		case buyTouched:
			buyDomBuy, sellDomBuy = buySingleBps, buySingleBps
		case sellTouched:
			buyDomSell, sellDomSell = sellSingleBps, sellSingleBps
		}
		buyWeight, sellWeight, targetWeight := weight, weight, weight
		if current != nil {
			buyKernel := conditionalExecutionKernel(
				*current, exposure.ConditionalState, true, horizon)
			sellKernel := conditionalExecutionKernel(
				*current, exposure.ConditionalState, false, horizon)
			jointKernel := math.Sqrt(buyKernel * sellKernel)
			targetWeight *= (priorPerPath + jointKernel) / (1 + priorPerPath)
			buyWeight *= (priorPerPath + buyKernel) / (1 + priorPerPath)
			sellWeight *= (priorPerPath + sellKernel) / (1 + priorPerPath)
		}
		switch {
		case buyTouched && !sellTouched:
			buyDominantBuyOnlyWeight += buyWeight
			sellDominantBuyOnlyWeight += sellWeight
			if exposure.StartBookDepthReady {
				buyDominantBuyOnly.add(exposure.StartBookImbalance, buySingleBps, buyWeight)
				sellDominantBuyOnly.add(exposure.StartBookImbalance, buySingleBps, sellWeight)
			}
		case sellTouched && !buyTouched:
			buyDominantSellOnlyWeight += buyWeight
			sellDominantSellOnlyWeight += sellWeight
			if exposure.StartBookDepthReady {
				reflected := -exposure.StartBookImbalance
				buyDominantSellOnly.add(reflected, sellSingleBps, buyWeight)
				sellDominantSellOnly.add(reflected, sellSingleBps, sellWeight)
			}
		}
		buyDominant.add(buyWeight, buyDomBuy, buyDomSell, inventoryReturnBps, inventoryDirectionalReturnBps)
		sellDominant.add(sellWeight, sellDomBuy, sellDomSell, inventoryReturnBps, inventoryDirectionalReturnBps)
		inventoryTarget.add(targetWeight, 0, 0, inventoryReturnBps, inventoryDirectionalReturnBps)
		lastExposure = exposure.At
		if exposure.NextMinute <= index {
			break
		}
		index = exposure.NextMinute
	}
	buyMoments, buyN := buyDominant.result()
	sellMoments, sellN := sellDominant.result()
	targetMoments, targetN := inventoryTarget.result()
	// The causal study promoted the predeclared 15m primary only. Its 30m
	// sensitivity failed the simultaneous lower bound and 10m was not tested;
	// do not leak the accepted coefficient across statistical clocks.
	if imbalance, ready := m.bookImbalanceAt(now); ready && sideImbalancePayoffEnabled(horizon) {
		applySideImbalancePayoffMean(&buyMoments.BuyMeanBps,
			buyDominant.weight, buyDominantBuyOnlyWeight,
			buyDominantBuyOnly, imbalance)
		applySideImbalancePayoffMean(&buyMoments.SellMeanBps,
			buyDominant.weight, buyDominantSellOnlyWeight,
			buyDominantSellOnly, -imbalance)
		applySideImbalancePayoffMean(&sellMoments.BuyMeanBps,
			sellDominant.weight, sellDominantBuyOnlyWeight,
			sellDominantBuyOnly, imbalance)
		applySideImbalancePayoffMean(&sellMoments.SellMeanBps,
			sellDominant.weight, sellDominantSellOnlyWeight,
			sellDominantSellOnly, -imbalance)
	}
	stats := JointPathPayoffStats{
		EffectiveSamples:                 math.Min(buyN, sellN),
		PathDecayHalfLifeSeconds:         decayHalfLifeSeconds,
		PathDecayAutocorrelation:         decaySnapshot.Autocorrelation,
		PathDecayPersistenceObservations: decaySnapshot.PersistenceObservations,
		BuyDominant:                      buyMoments,
		SellDominant:                     sellMoments,
		InventoryTarget:                  targetMoments,
		InventoryTargetEffectiveSamples:  targetN,
		TwoStageContinuation:             config.JointDistanceQuantity.TwoStageContinuation,
		ContinuationHorizon:              completionHorizon,
		BuyThenSellSamples:               buyThenSellSamples,
		BuyThenSellCompletions:           buyThenSellCompletions,
		SellThenBuySamples:               sellThenBuySamples,
		SellThenBuyCompletions:           sellThenBuyCompletions,
	}
	// Update the Neff reference only after the current path estimate has been
	// formed. Repeated distance queries at the same timestamp are idempotent.
	if config.JointDistanceQuantity.AdaptivePathDecay {
		if m.pathDecay == nil {
			m.pathDecay = make(map[time.Duration]*adaptivePathDecayState)
		}
		state := m.pathDecay[horizon]
		if state == nil {
			state = &adaptivePathDecayState{}
			m.pathDecay[horizon] = state
		}
		state.observeNeff(now, stats.EffectiveSamples, horizon, time.Duration(config.HorizonLookback))
		neffSnapshot := state.snapshot(horizon, time.Duration(config.HorizonLookback))
		stats.EffectiveSamplesBaseline = neffSnapshot.EffectiveSamplesBaseline
		stats.EffectiveSamplesBaselineStd = neffSnapshot.EffectiveSamplesStd
	}
	if config.VolumeProfile.Enabled && config.VolumeProfile.AsymmetricPOCRisk &&
		current != nil {
		applyVolumeProfileSideTerminalRisk(&stats, current.VolumeProfile, config.InventoryRiskZScore)
	}
	return stats
}

func jointContinuationHorizon(config MarketMakerConfig, openingHorizon time.Duration) time.Duration {
	if openingHorizon <= 0 || !config.JointDistanceQuantity.TwoStageContinuation ||
		!config.JointDistanceQuantity.CrossHorizonContinuation {
		return openingHorizon
	}
	completion := openingHorizon
	for _, candidate := range config.FastModelWindows() {
		if candidate > completion {
			completion = candidate
		}
	}
	return completion
}

// JointContinuationHorizon exposes the completion lease implied by the Fast
// configuration so live execution and deterministic replay can honor the same
// contract that JointPathPayoffStatistics values.
func (c MarketMakerConfig) JointContinuationHorizon(openingHorizon time.Duration) time.Duration {
	c.setDefaults()
	return jointContinuationHorizon(c, openingHorizon)
}

// twoStageCompletionOutcome follows an opening fill that occurred inside its
// selected Fast horizon. The completion side then receives completionHorizon,
// matching the independently re-evaluated post-fill lease. Quotes stay at
// their initially evaluated executable prices here; any later live
// re-optimization is additional option value and is not assumed by this
// conservative admission estimator.
func (m *MarketMakerHorizonModel) twoStageCompletionOutcome(
	index *marketMakerBBORangeIndex,
	exposure marketMakerHorizonExposure,
	completionHorizon time.Duration,
	bidQuote, askQuote float64,
	openingBuy bool,
) (terminalBid float64, completed, valid bool) {
	if m == nil || index == nil || completionHorizon <= 0 || bidQuote <= 0 || askQuote <= bidQuote {
		return 0, false, false
	}
	start := pointIndexAtOrAfter(m.points, exposure.At)
	initialRight := pointIndexAtOrAfter(m.points, exposure.EndAt)
	if start >= len(m.points) || initialRight <= start+1 || !m.points[start].At.Equal(exposure.At) {
		return 0, false, false
	}
	firstTouch := -1
	if openingBuy {
		firstTouch = index.firstAskAtOrBelow(start+1, initialRight, bidQuote)
	} else {
		firstTouch = index.firstBidAtOrAbove(start+1, initialRight, askQuote)
	}
	if firstTouch < 0 {
		return 0, false, false
	}
	completionRight := pointIndexAtOrAfter(m.points, m.points[firstTouch].At.Add(completionHorizon))
	if completionRight <= firstTouch+1 || completionRight > len(m.points) ||
		!index.validRange(firstTouch+1, completionRight) {
		return 0, false, false
	}
	terminalBid = m.points[completionRight-1].bidPrice()
	if terminalBid <= 0 {
		return 0, false, false
	}
	if openingBuy {
		completed = index.firstBidAtOrAbove(firstTouch+1, completionRight, askQuote) >= 0
	} else {
		completed = index.firstAskAtOrBelow(firstTouch+1, completionRight, bidQuote) >= 0
	}
	return terminalBid, completed, true
}

// jointBalancedPathPayoffDifference compares an outward candidate with the
// ordinary Fast quote path by path. Pairing removes common terminal moves and
// makes the test about the actual distance concession: extra edge on paths
// where both levels fill versus lost fills where only the base level fills.
// It deliberately uses one balanced unit, leaving inventory and quantity to
// the unified whole-position optimizer after the price has been identified.
func (m *MarketMakerHorizonModel) jointBalancedPathPayoffDifference(
	now time.Time,
	config MarketMakerConfig,
	horizon time.Duration,
	baseBuyDistanceBps, baseSellDistanceBps,
	candidateBuyDistanceBps, candidateSellDistanceBps float64,
	current conditionalExecutionState,
) JointPathPayoffDifferenceDecision {
	d := JointPathPayoffDifferenceDecision{}
	if m == nil || now.IsZero() || horizon <= 0 ||
		baseBuyDistanceBps <= 0 || baseSellDistanceBps <= 0 ||
		candidateBuyDistanceBps < baseBuyDistanceBps ||
		candidateSellDistanceBps < baseSellDistanceBps {
		return d
	}
	config.setDefaults()
	completionHorizon := jointContinuationHorizon(config, horizon)
	exposures := m.crossingExposures(horizon)
	decaySnapshot := m.adaptivePathDecaySnapshot(now, config, horizon, exposures)
	lookback := time.Duration(config.HorizonLookback)
	cutoff := now.Add(-lookback)
	decayHalfLifeSeconds := decaySnapshot.HalfLifeSeconds
	if decayHalfLifeSeconds <= 0 {
		decayHalfLifeSeconds = math.Sqrt(horizon.Seconds() * lookback.Seconds())
	}
	entryCostBps := config.MakerFeeBps + config.AdverseSelectionBps
	cycleCostBps := 2*entryCostBps + config.MinimumNetEdgeBps
	var bboIndex *marketMakerBBORangeIndex
	if config.JointDistanceQuantity.TwoStageContinuation {
		bboIndex = m.executableBBORangeIndex()
	}
	globalCount := 0
	for index := firstHorizonExposureAtOrAfter(exposures, cutoff); index < len(exposures); {
		maturity := exposures[index].EndAt
		if config.JointDistanceQuantity.TwoStageContinuation {
			maturity = exposures[index].At.Add(horizon + completionHorizon)
		}
		if maturity.After(now) {
			break
		}
		globalCount++
		if exposures[index].NextMinute <= index {
			break
		}
		index = exposures[index].NextMinute
	}
	priorPerPath := 0.0
	if current.Valid && globalCount > 0 {
		priorPerPath = 1 / math.Sqrt(float64(globalCount))
	}
	var differences weightedScalarMoments
	var lastExposure time.Time
	for index := firstHorizonExposureAtOrAfter(exposures, cutoff); index < len(exposures); {
		exposure := exposures[index]
		maturity := exposure.EndAt
		if config.JointDistanceQuantity.TwoStageContinuation {
			maturity = exposure.At.Add(horizon + completionHorizon)
		}
		if maturity.After(now) {
			break
		}
		weight := 1.0
		if !lastExposure.IsZero() {
			weight = math.Min(1, exposure.At.Sub(lastExposure).Seconds()/horizon.Seconds())
		}
		if decayHalfLifeSeconds > 0 {
			weight *= math.Exp(-math.Ln2 * math.Max(0, now.Sub(maturity).Seconds()) /
				decayHalfLifeSeconds)
		}
		if current.Valid {
			buyKernel := conditionalExecutionKernel(current, exposure.ConditionalState, true, horizon)
			sellKernel := conditionalExecutionKernel(current, exposure.ConditionalState, false, horizon)
			weight *= (priorPerPath + math.Sqrt(buyKernel*sellKernel)) / (1 + priorPerPath)
		}
		base, baseValid := m.balancedPathPayoffBps(
			bboIndex, exposure, completionHorizon,
			baseBuyDistanceBps, baseSellDistanceBps,
			entryCostBps, cycleCostBps,
			config.JointDistanceQuantity.TwoStageContinuation)
		candidate, candidateValid := m.balancedPathPayoffBps(
			bboIndex, exposure, completionHorizon,
			candidateBuyDistanceBps, candidateSellDistanceBps,
			entryCostBps, cycleCostBps,
			config.JointDistanceQuantity.TwoStageContinuation)
		if weight > 0 && baseValid && candidateValid {
			differences.add(weight, candidate-base)
			lastExposure = exposure.At
		}
		if exposure.NextMinute <= index {
			break
		}
		index = exposure.NextMinute
	}
	mean, variance, effective := differences.result()
	if effective <= 1 {
		return d
	}
	d.Evaluated = true
	d.MeanBps = mean
	d.EffectiveSamples = effective
	d.StdErrorBps = math.Sqrt(math.Max(0, variance) / effective)
	return d
}

func (m *MarketMakerHorizonModel) balancedPathPayoffBps(
	index *marketMakerBBORangeIndex,
	exposure marketMakerHorizonExposure,
	completionHorizon time.Duration,
	buyDistanceBps, sellDistanceBps,
	entryCostBps, cycleCostBps float64,
	twoStage bool,
) (float64, bool) {
	if exposure.StartBid <= 0 || exposure.StartAsk < exposure.StartBid ||
		exposure.TerminalBid <= 0 || buyDistanceBps <= 0 || sellDistanceBps <= 0 {
		return 0, false
	}
	buyTouched := exposure.BuyExcursionBps >= buyDistanceBps
	sellTouched := exposure.SellExcursionBps >= sellDistanceBps
	bidQuote := exposure.StartAsk * math.Exp(-buyDistanceBps/10_000)
	askQuote := exposure.StartBid * math.Exp(sellDistanceBps/10_000)
	terminalBid := exposure.TerminalBid
	if twoStage && buyTouched != sellTouched {
		var completed, valid bool
		terminalBid, completed, valid = m.twoStageCompletionOutcome(
			index, exposure, completionHorizon, bidQuote, askQuote, buyTouched)
		if !valid {
			return 0, false
		}
		if completed {
			buyTouched, sellTouched = true, true
		}
	}
	switch {
	case buyTouched && sellTouched:
		return math.Log(askQuote/bidQuote)*10_000 - cycleCostBps, true
	case buyTouched:
		return makerFillTerminalWealthBps(
			true, bidQuote, terminalBid, entryCostBps), true
	case sellTouched:
		return makerFillTerminalWealthBps(
			false, askQuote, terminalBid, entryCostBps), true
	default:
		return 0, true
	}
}

func simultaneousOneSidedZ(baseZ float64, comparisons int) float64 {
	if baseZ <= 0 {
		baseZ = 1.645
	}
	if comparisons <= 1 {
		return baseZ
	}
	alpha := 0.5 * math.Erfc(baseZ/math.Sqrt2)
	adjustedAlpha := math.Max(
		math.SmallestNonzeroFloat64, alpha/float64(comparisons))
	return math.Sqrt2 * math.Erfinv(1-2*adjustedAlpha)
}

// Evaluate preserves the incremental-order API for callers that do not own inventory.
func (s JointPathPayoffStats) Evaluate(
	buyNotionalJPY, sellNotionalJPY, pairEquityJPY, riskAversion, zScore float64,
) JointPathPayoffDecision {
	return s.EvaluateWholePosition(
		0, buyNotionalJPY, sellNotionalJPY,
		pairEquityJPY, riskAversion, zScore)
}

// EvaluateWholePosition compares a candidate quote with leaving the current
// inventory unchanged over the same completed terminal paths, using zero risky
// inventory as the legacy risk anchor. New strategy code should call
// EvaluateTargetRelativePosition with the same-horizon inventory target.
func (s JointPathPayoffStats) EvaluateWholePosition(
	currentInventoryNotionalJPY, buyNotionalJPY, sellNotionalJPY,
	pairEquityJPY, riskAversion, zScore float64,
) JointPathPayoffDecision {
	return s.EvaluateTargetRelativePosition(
		currentInventoryNotionalJPY, 0,
		buyNotionalJPY, sellNotionalJPY,
		pairEquityJPY, riskAversion, zScore)
}

// EvaluateTargetRelativePosition compares a candidate quote with submitting no
// new order on the same completed executable-BBO paths. Existing PnL is common
// to both choices and is not counted as new alpha, but the covariance of the
// order payoff with the inventory deviation from the same-horizon target is
// decision-relevant:
//
//	Delta R = Var(Delta W) + 2 Cov(W_inventory-target, Delta W).
//
// A BUY below target (or SELL above target) may therefore receive a negative
// Kelly penalty when it reduces target-relative terminal risk. Crossing the
// target reverses that credit automatically. Average cost is intentionally
// absent: it is an accounting state, not a return forecast.
func (s JointPathPayoffStats) EvaluateTargetRelativePosition(
	currentInventoryNotionalJPY, targetInventoryNotionalJPY,
	buyNotionalJPY, sellNotionalJPY,
	pairEquityJPY, riskAversion, zScore float64,
) JointPathPayoffDecision {
	if currentInventoryNotionalJPY < 0 || targetInventoryNotionalJPY < 0 ||
		buyNotionalJPY < 0 || sellNotionalJPY < 0 ||
		pairEquityJPY <= 0 || s.EffectiveSamples <= 0 {
		return JointPathPayoffDecision{}
	}
	moments := s.SellDominant
	if buyNotionalJPY >= sellNotionalJPY {
		moments = s.BuyDominant
	}
	meanJPY := (buyNotionalJPY*moments.BuyMeanBps +
		sellNotionalJPY*moments.SellMeanBps) / 10_000
	incrementalVarianceJPY2 := (buyNotionalJPY*buyNotionalJPY*moments.BuyVarBps2 +
		sellNotionalJPY*sellNotionalJPY*moments.SellVarBps2 +
		2*buyNotionalJPY*sellNotionalJPY*moments.CovBps2) / 100_000_000
	incrementalVarianceJPY2 = math.Max(0, incrementalVarianceJPY2)
	riskInventoryNotionalJPY := currentInventoryNotionalJPY - targetInventoryNotionalJPY
	baselineVarianceJPY2 := riskInventoryNotionalJPY * riskInventoryNotionalJPY *
		moments.InventoryVarBps2 / 100_000_000
	inventoryOrderCovarianceJPY2 := riskInventoryNotionalJPY *
		(buyNotionalJPY*moments.InventoryBuyCovBps2 +
			sellNotionalJPY*moments.InventorySellCovBps2) / 100_000_000
	wholeVarianceJPY2 := math.Max(0, baselineVarianceJPY2+incrementalVarianceJPY2+
		2*inventoryOrderCovarianceJPY2)
	marginalVarianceJPY2 := wholeVarianceJPY2 - baselineVarianceJPY2
	effectiveSamples := math.Max(1, s.EffectiveSamples)
	baselineStdErrorJPY := math.Sqrt(math.Max(0, baselineVarianceJPY2) / effectiveSamples)
	wholePositionStdErrorJPY := math.Sqrt(wholeVarianceJPY2 / effectiveSamples)
	incrementalStdErrorJPY := math.Sqrt(incrementalVarianceJPY2 / effectiveSamples)
	// The lower bound is a paired comparison against submitting no new order.
	// Using the incremental order-payoff SE here double-counts uncertainty when
	// the existing inventory is already risky, and fails to credit uncertainty
	// reduced by a target-restoring action. The consistent bound is the
	// difference between the candidate and baseline whole-position bounds.
	lower := meanJPY - math.Max(0, zScore)*
		(wholePositionStdErrorJPY-baselineStdErrorJPY)
	kellyPenalty := math.Max(0, riskAversion) * marginalVarianceJPY2 / (2 * pairEquityJPY)
	return JointPathPayoffDecision{
		ExpectedPnLJPY:                  meanJPY,
		StdErrorJPY:                     incrementalStdErrorJPY,
		BaselineStdErrorJPY:             baselineStdErrorJPY,
		WholePositionStdErrorJPY:        wholePositionStdErrorJPY,
		IncrementalStdErrorJPY:          incrementalStdErrorJPY,
		LowerPnLJPY:                     lower,
		ExistingInventoryExpectedPnLJPY: currentInventoryNotionalJPY * moments.InventoryMeanBps / 10_000,
		TargetInventoryNotionalJPY:      targetInventoryNotionalJPY,
		RiskInventoryNotionalJPY:        riskInventoryNotionalJPY,
		BaselineVarianceJPY2:            baselineVarianceJPY2,
		WholePositionVarianceJPY2:       wholeVarianceJPY2,
		MarginalVarianceJPY2:            marginalVarianceJPY2,
		InventoryOrderCovarianceJPY2:    inventoryOrderCovarianceJPY2,
		KellyPenaltyJPY:                 kellyPenalty,
		CertaintyEquivalent:             lower - kellyPenalty,
		RiskReducing:                    marginalVarianceJPY2 < -1e-12,
	}
}
