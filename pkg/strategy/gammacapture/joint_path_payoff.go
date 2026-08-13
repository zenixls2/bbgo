package gammacapture

import (
	"math"
	"time"
)

type jointPathPayoffMoments struct {
	BuyMeanBps           float64
	SellMeanBps          float64
	InventoryMeanBps     float64
	BuyVarBps2           float64
	SellVarBps2          float64
	InventoryVarBps2     float64
	CovBps2              float64
	InventoryBuyCovBps2  float64
	InventorySellCovBps2 float64
}

type JointPathPayoffStats struct {
	EffectiveSamples float64
	BuyDominant      jointPathPayoffMoments
	SellDominant     jointPathPayoffMoments
}

type JointPathPayoffDecision struct {
	ExpectedPnLJPY                  float64
	StdErrorJPY                     float64
	LowerPnLJPY                     float64
	ExistingInventoryExpectedPnLJPY float64
	BaselineVarianceJPY2            float64
	WholePositionVarianceJPY2       float64
	MarginalVarianceJPY2            float64
	InventoryOrderCovarianceJPY2    float64
	KellyPenaltyJPY                 float64
	CertaintyEquivalent             float64
	RiskReducing                    bool
}

type weightedJointMoments struct {
	weight, weightSquared float64
	buy, sell, inventory  float64
	buySquared            float64
	sellSquared           float64
	inventorySquared      float64
	buySell               float64
	inventoryBuy          float64
	inventorySell         float64
}

func (m *weightedJointMoments) add(weight, buy, sell, inventory float64) {
	if weight <= 0 {
		return
	}
	m.weight += weight
	m.weightSquared += weight * weight
	m.buy += weight * buy
	m.sell += weight * sell
	m.inventory += weight * inventory
	m.buySquared += weight * buy * buy
	m.sellSquared += weight * sell * sell
	m.inventorySquared += weight * inventory * inventory
	m.buySell += weight * buy * sell
	m.inventoryBuy += weight * inventory * buy
	m.inventorySell += weight * inventory * sell
}

func (m weightedJointMoments) result() (jointPathPayoffMoments, float64) {
	if m.weight <= 0 {
		return jointPathPayoffMoments{}, 0
	}
	buyMean, sellMean, inventoryMean := m.buy/m.weight, m.sell/m.weight, m.inventory/m.weight
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
		BuyMeanBps:       buyMean,
		SellMeanBps:      sellMean,
		InventoryMeanBps: inventoryMean,
		BuyVarBps2: math.Max(0,
			(m.buySquared/m.weight-buyMean*buyMean)*varianceCorrection),
		SellVarBps2: math.Max(0,
			(m.sellSquared/m.weight-sellMean*sellMean)*varianceCorrection),
		InventoryVarBps2: math.Max(0,
			(m.inventorySquared/m.weight-inventoryMean*inventoryMean)*varianceCorrection),
		CovBps2:              (m.buySell/m.weight - buyMean*sellMean) * varianceCorrection,
		InventoryBuyCovBps2:  (m.inventoryBuy/m.weight - inventoryMean*buyMean) * varianceCorrection,
		InventorySellCovBps2: (m.inventorySell/m.weight - inventoryMean*sellMean) * varianceCorrection,
	}
	return out, effective
}

// JointPathPayoffStatistics values a completed maker window in terminal
// executable wealth, rather than assuming that every touch earns the quoted
// spread. A one-sided fill is marked at terminal bid/ask and charged a
// conservative round-trip cost. When both sides touch, matched notional earns
// the realized quote-to-quote cycle; only the unmatched residual is marked.
// This directly penalizes selling into a continued rise or buying into a
// continued decline while retaining profitable two-sided oscillation.
func (m *MarketMakerHorizonModel) JointPathPayoffStatistics(
	now time.Time,
	config MarketMakerConfig,
	horizon time.Duration,
	buyDistanceBps, sellDistanceBps float64,
) JointPathPayoffStats {
	if m == nil || now.IsZero() || horizon <= 0 ||
		buyDistanceBps <= 0 || sellDistanceBps <= 0 {
		return JointPathPayoffStats{}
	}
	config.setDefaults()
	exposures := m.crossingExposures(horizon)
	lookback := time.Duration(config.HorizonLookback)
	cutoff := now.Add(-lookback)
	// Sparse-market path payoffs are non-stationary. Use the geometric mean of
	// the execution horizon and the available lookback as a scale-free
	// exponential half-life: it reacts much faster than the full lookback while
	// retaining several non-overlapping holding windows. No fitted decay
	// coefficient or symbol-specific constant is introduced.
	decayHalfLifeSeconds := math.Sqrt(horizon.Seconds() * lookback.Seconds())
	var buyDominant, sellDominant weightedJointMoments
	var lastExposure time.Time
	entryCostBps := config.MakerFeeBps + config.AdverseSelectionBps
	cycleCostBps := 2*entryCostBps + config.MinimumNetEdgeBps
	for index := firstHorizonExposureAtOrAfter(exposures, cutoff); index < len(exposures); {
		exposure := exposures[index]
		if exposure.EndAt.After(now) {
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
			ageSeconds := math.Max(0, now.Sub(exposure.EndAt).Seconds())
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
		startMid := math.Sqrt(exposure.StartBid * exposure.StartAsk)
		inventoryReturnBps := math.Log(exposure.TerminalBid/startMid) * 10_000
		bidQuote := exposure.StartAsk * math.Exp(-buyDistanceBps/10_000)
		askQuote := exposure.StartBid * math.Exp(sellDistanceBps/10_000)
		buySingleBps, sellSingleBps := 0.0, 0.0
		if buyTouched {
			// Terminal executable wealth contains one completed BUY, hence one
			// maker fee. Charging a hypothetical future exit here double-counts
			// cost and systematically suppresses inventory carried past H.
			buySingleBps = math.Log(exposure.TerminalBid/bidQuote)*10_000 - entryCostBps
		}
		if sellTouched {
			sellSingleBps = math.Log(askQuote/exposure.TerminalAsk)*10_000 - entryCostBps
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
		buyDominant.add(weight, buyDomBuy, buyDomSell, inventoryReturnBps)
		sellDominant.add(weight, sellDomBuy, sellDomSell, inventoryReturnBps)
		lastExposure = exposure.At
		if exposure.NextMinute <= index {
			break
		}
		index = exposure.NextMinute
	}
	buyMoments, buyN := buyDominant.result()
	sellMoments, sellN := sellDominant.result()
	return JointPathPayoffStats{
		EffectiveSamples: math.Min(buyN, sellN),
		BuyDominant:      buyMoments,
		SellDominant:     sellMoments,
	}
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
// inventory unchanged over the same completed terminal paths. Existing PnL is
// common to both choices and is not counted as new alpha, but its covariance
// with the candidate is decision-relevant:
//
//	Delta Var(W) = Var(Delta W) + 2 Cov(W_inventory, Delta W).
//
// A risk-reducing SELL may therefore receive a negative Kelly penalty, while
// a BUY that compounds downside exposure is restrained. Average cost is
// intentionally absent: it is an accounting state, not a return forecast.
func (s JointPathPayoffStats) EvaluateWholePosition(
	currentInventoryNotionalJPY, buyNotionalJPY, sellNotionalJPY,
	pairEquityJPY, riskAversion, zScore float64,
) JointPathPayoffDecision {
	if currentInventoryNotionalJPY < 0 || buyNotionalJPY < 0 || sellNotionalJPY < 0 ||
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
	baselineVarianceJPY2 := currentInventoryNotionalJPY * currentInventoryNotionalJPY *
		moments.InventoryVarBps2 / 100_000_000
	inventoryOrderCovarianceJPY2 := currentInventoryNotionalJPY *
		(buyNotionalJPY*moments.InventoryBuyCovBps2 +
			sellNotionalJPY*moments.InventorySellCovBps2) / 100_000_000
	wholeVarianceJPY2 := math.Max(0, baselineVarianceJPY2+incrementalVarianceJPY2+
		2*inventoryOrderCovarianceJPY2)
	marginalVarianceJPY2 := wholeVarianceJPY2 - baselineVarianceJPY2
	standardErrorJPY := math.Sqrt(incrementalVarianceJPY2 / math.Max(1, s.EffectiveSamples))
	lower := meanJPY - math.Max(0, zScore)*standardErrorJPY
	kellyPenalty := math.Max(0, riskAversion) * marginalVarianceJPY2 / (2 * pairEquityJPY)
	return JointPathPayoffDecision{
		ExpectedPnLJPY:                  meanJPY,
		StdErrorJPY:                     standardErrorJPY,
		LowerPnLJPY:                     lower,
		ExistingInventoryExpectedPnLJPY: currentInventoryNotionalJPY * moments.InventoryMeanBps / 10_000,
		BaselineVarianceJPY2:            baselineVarianceJPY2,
		WholePositionVarianceJPY2:       wholeVarianceJPY2,
		MarginalVarianceJPY2:            marginalVarianceJPY2,
		InventoryOrderCovarianceJPY2:    inventoryOrderCovarianceJPY2,
		KellyPenaltyJPY:                 kellyPenalty,
		CertaintyEquivalent:             lower - kellyPenalty,
		RiskReducing:                    marginalVarianceJPY2 < -1e-12,
	}
}
