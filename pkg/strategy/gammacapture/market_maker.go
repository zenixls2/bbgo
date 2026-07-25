package gammacapture

import (
	"math"
	"sort"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

// MarketMakerConfig contains only quote-policy parameters.  It is deliberately
// independent of the exchange adapter so the policy can be trained and tested
// against historical events without pretending that historical fills are known.
type MarketMakerConfig struct {
	Enabled bool `json:"enabled" yaml:"enabled"`
	// StartupCancelStaleOrders removes maker orders left by a previous
	// gammacapture process before a new quote window is opened. Only orders
	// carrying the gammacapture client-id prefix (or the legacy BBGO broker
	// prefix) are eligible; Binance UI orders without those prefixes are left
	// untouched.
	StartupCancelStaleOrders bool    `json:"startupCancelStaleOrders" yaml:"startupCancelStaleOrders"`
	MakerFeeBps              float64 `json:"makerFeeBps" yaml:"makerFeeBps"`
	TakerFeeBps              float64 `json:"takerFeeBps" yaml:"takerFeeBps"`
	MinimumNetEdgeBps        float64 `json:"minimumNetEdgeBps" yaml:"minimumNetEdgeBps"`
	AdverseSelectionBps      float64 `json:"adverseSelectionBps" yaml:"adverseSelectionBps"`
	MinimumHalfSpreadBps     float64 `json:"minimumHalfSpreadBps" yaml:"minimumHalfSpreadBps"`
	MaximumHalfSpreadBps     float64 `json:"maximumHalfSpreadBps" yaml:"maximumHalfSpreadBps"`
	VolatilityMultiplier     float64 `json:"volatilityMultiplier" yaml:"volatilityMultiplier"`
	InventoryTarget          float64 `json:"inventoryTarget" yaml:"inventoryTarget"`
	InventoryLimit           float64 `json:"inventoryLimit" yaml:"inventoryLimit"`
	AutoInventoryLimit       bool    `json:"autoInventoryLimit" yaml:"autoInventoryLimit"`
	InventoryRiskBudgetJPY   float64 `json:"inventoryRiskBudgetJPY" yaml:"inventoryRiskBudgetJPY"`
	// InventoryRiskBudgetRatio scales the adverse-move risk budget with the
	// current quote-equivalent equity of the symbol. InventoryRiskBudgetJPY
	// remains the absolute floor for small accounts or unavailable balances.
	InventoryRiskBudgetRatio float64 `json:"inventoryRiskBudgetRatio" yaml:"inventoryRiskBudgetRatio"`
	InventoryRiskZScore      float64 `json:"inventoryRiskZScore" yaml:"inventoryRiskZScore"`
	InventoryMaxOrderLevels  float64 `json:"inventoryMaxOrderLevels" yaml:"inventoryMaxOrderLevels"`
	InventoryTargetRatio     float64 `json:"inventoryTargetRatio" yaml:"inventoryTargetRatio"`
	// InventoryCapitalTargetRatio and InventoryCapitalMaxRatio express the
	// target and hard inventory cap as fractions of pair equity. The legacy
	// InventoryTargetRatio remains the fallback ratio of target to cap.
	InventoryCapitalTargetRatio float64 `json:"inventoryCapitalTargetRatio" yaml:"inventoryCapitalTargetRatio"`
	InventoryCapitalMaxRatio    float64 `json:"inventoryCapitalMaxRatio" yaml:"inventoryCapitalMaxRatio"`
	InventorySkewBps            float64 `json:"inventorySkewBps" yaml:"inventorySkewBps"`
	QuoteNotional               float64 `json:"quoteNotionalJPY" yaml:"quoteNotionalJPY"`
	// MinimumQuoteNotional and MaximumQuoteNotional are retained for backwards
	// compatible config decoding. They are no longer policy bounds: quote size
	// is determined by the observed volatility, fill load, risk budget, and
	// account/exchange constraints at the time an order is submitted.
	MinimumQuoteNotional  float64        `json:"minimumQuoteNotionalJPY" yaml:"minimumQuoteNotionalJPY"`
	MaximumQuoteNotional  float64        `json:"maximumQuoteNotionalJPY" yaml:"maximumQuoteNotionalJPY"`
	RefreshInterval       types.Duration `json:"refreshInterval" yaml:"refreshInterval"`
	MinRefreshInterval    types.Duration `json:"minRefreshInterval" yaml:"minRefreshInterval"`
	MaxRefreshInterval    types.Duration `json:"maxRefreshInterval" yaml:"maxRefreshInterval"`
	RefreshMoveBps        float64        `json:"refreshMoveBps" yaml:"refreshMoveBps"`
	AdverseRepriceBps     float64        `json:"adverseRepriceBps" yaml:"adverseRepriceBps"`
	RefreshImbalanceDelta float64        `json:"refreshImbalanceDelta" yaml:"refreshImbalanceDelta"`
	MinTradingWindow      types.Duration `json:"minTradingWindow" yaml:"minTradingWindow"`
	MaxTradingWindow      types.Duration `json:"maxTradingWindow" yaml:"maxTradingWindow"`
	HorizonLookback       types.Duration `json:"horizonLookback" yaml:"horizonLookback"`
	HorizonUpdateInterval types.Duration `json:"horizonUpdateInterval" yaml:"horizonUpdateInterval"`
	HorizonMinSamples     int            `json:"horizonMinSamples" yaml:"horizonMinSamples"`
	FastWindow            types.Duration `json:"fastWindow" yaml:"fastWindow"`
	// FastEvidenceWindow is a separate raw-data coverage window. It may be
	// longer than FastWindow because sparse JPY markets need more time to
	// accumulate public trades without slowing the short-horizon signal model.
	FastEvidenceWindow types.Duration `json:"fastEvidenceWindow" yaml:"fastEvidenceWindow"`
	// FastEvidenceMinTrades and FastEvidenceMinBBOUpdates are observational
	// coverage thresholds for the raw trade/BBO evidence layer. They do not
	// override crossing-model health or prove fee-adjusted profitability.
	FastEvidenceMinTrades     int     `json:"fastEvidenceMinTrades" yaml:"fastEvidenceMinTrades"`
	FastEvidenceMinBBOUpdates int     `json:"fastEvidenceMinBBOUpdates" yaml:"fastEvidenceMinBBOUpdates"`
	DirectionSkewBps          float64 `json:"directionSkewBps" yaml:"directionSkewBps"`
	ImbalanceSkewBps          float64 `json:"imbalanceSkewBps" yaml:"imbalanceSkewBps"`
	// SideAllocationSensitivity controls how strongly inventory and short-term
	// pressure de-risk one side of the book. It never increases either side
	// above the common risk-sized notional; the unaffected side remains at the
	// baseline while the riskier side is reduced smoothly.
	SideAllocationSensitivity float64 `json:"sideAllocationSensitivity" yaml:"sideAllocationSensitivity"`
	// SideAllocationFillRateWeight adds a damped side-specific crossing-rate
	// signal to the allocation. A side that is filling more often is reduced so
	// one-sided flow cannot consume the full risk budget repeatedly.
	SideAllocationFillRateWeight float64 `json:"sideAllocationFillRateWeight" yaml:"sideAllocationFillRateWeight"`
	// SideAllocationSmoothing is the EMA update fraction applied at quote-window
	// transitions. It prevents noisy one-minute estimates from flipping sizes.
	SideAllocationSmoothing float64 `json:"sideAllocationSmoothing" yaml:"sideAllocationSmoothing"`
	// SideDistanceSensitivity controls how much an observed side-specific
	// crossing-rate imbalance can move quote distance away from the common
	// volatility estimate. The fee/adverse-selection floor is always respected.
	SideDistanceSensitivity float64              `json:"sideDistanceSensitivity" yaml:"sideDistanceSensitivity"`
	InventoryReset          InventoryResetConfig `json:"inventoryReset" yaml:"inventoryReset"`
}

// InventoryResetConfig controls the mathematically justified transition from
// a passive ask to a small, slippage-capped IOC reduction. It is separate from
// Quote so the normal maker policy remains deterministic and testable.
type InventoryResetConfig struct {
	Enabled                bool           `json:"enabled" yaml:"enabled"`
	MaxAskAge              types.Duration `json:"maxAskAge" yaml:"maxAskAge"`
	FastAskAge             types.Duration `json:"fastAskAge" yaml:"fastAskAge"`
	AdverseMoveBps         float64        `json:"adverseMoveBps" yaml:"adverseMoveBps"`
	FastAdverseMoveBps     float64        `json:"fastAdverseMoveBps" yaml:"fastAdverseMoveBps"`
	FastDirectionThreshold float64        `json:"fastDirectionThreshold" yaml:"fastDirectionThreshold"`
	MaxSlippageBps         float64        `json:"maxSlippageBps" yaml:"maxSlippageBps"`
	ReductionNotional      float64        `json:"reductionNotionalJPY" yaml:"reductionNotionalJPY"`
	Cooldown               types.Duration `json:"cooldown" yaml:"cooldown"`
	FillIntensityHaircut   float64        `json:"fillIntensityHaircut" yaml:"fillIntensityHaircut"`
	RiskZScore             float64        `json:"riskZScore" yaml:"riskZScore"`
}

func (c *MarketMakerConfig) setDefaults() {
	if c.MakerFeeBps <= 0 {
		c.MakerFeeBps = 10
	}
	if c.TakerFeeBps <= 0 {
		c.TakerFeeBps = c.MakerFeeBps
	}
	if c.AdverseSelectionBps <= 0 {
		c.AdverseSelectionBps = 2
	}
	if c.MinimumNetEdgeBps < 0 {
		c.MinimumNetEdgeBps = 0
	}
	if c.MinimumHalfSpreadBps <= 0 {
		c.MinimumHalfSpreadBps = c.MakerFeeBps + c.AdverseSelectionBps + c.MinimumNetEdgeBps/2
	}
	if c.MaximumHalfSpreadBps <= 0 {
		c.MaximumHalfSpreadBps = 80
	}
	if c.VolatilityMultiplier < 0 {
		c.VolatilityMultiplier = 0
	} else if c.VolatilityMultiplier == 0 {
		c.VolatilityMultiplier = 0.75
	}
	if c.InventoryLimit <= 0 {
		c.InventoryLimit = 1
	}
	if c.InventorySkewBps <= 0 {
		c.InventorySkewBps = 20
	}
	if c.QuoteNotional <= 0 {
		c.QuoteNotional = 50_000
	}
	if c.InventoryRiskBudgetJPY <= 0 {
		c.InventoryRiskBudgetJPY = c.QuoteNotional * 0.08
	}
	if c.InventoryRiskBudgetRatio <= 0 {
		c.InventoryRiskBudgetRatio = 0.0025
	}
	if c.InventoryRiskZScore <= 0 {
		c.InventoryRiskZScore = 1.645
	}
	if c.InventoryMaxOrderLevels <= 0 {
		c.InventoryMaxOrderLevels = 32
	}
	if c.InventoryTargetRatio <= 0 || c.InventoryTargetRatio >= 1 {
		c.InventoryTargetRatio = 0.5
	}
	if c.InventoryCapitalMaxRatio <= 0 || c.InventoryCapitalMaxRatio >= 1 {
		c.InventoryCapitalMaxRatio = 0.50
	}
	if c.InventoryCapitalTargetRatio <= 0 || c.InventoryCapitalTargetRatio >= c.InventoryCapitalMaxRatio {
		c.InventoryCapitalTargetRatio = c.InventoryCapitalMaxRatio * c.InventoryTargetRatio
	}
	if c.RefreshInterval <= 0 {
		c.RefreshInterval = types.Duration(30 * time.Second)
	}
	if c.MinRefreshInterval <= 0 {
		c.MinRefreshInterval = types.Duration(10 * time.Second)
	}
	if c.MaxRefreshInterval <= 0 {
		c.MaxRefreshInterval = types.Duration(15 * time.Minute)
	}
	if c.RefreshMoveBps <= 0 {
		c.RefreshMoveBps = 8
	}
	if c.AdverseRepriceBps <= 0 {
		c.AdverseRepriceBps = math.Max(20, 2*c.RefreshMoveBps)
	}
	if c.RefreshImbalanceDelta <= 0 {
		c.RefreshImbalanceDelta = 0.25
	}
	if c.MinTradingWindow <= 0 {
		c.MinTradingWindow = types.Duration(5 * time.Minute)
	}
	if c.MaxTradingWindow <= 0 {
		c.MaxTradingWindow = types.Duration(15 * time.Minute)
	}
	if c.MaxTradingWindow < c.MinTradingWindow {
		c.MaxTradingWindow = c.MinTradingWindow
	}
	if c.HorizonLookback <= 0 {
		c.HorizonLookback = types.Duration(6 * time.Hour)
	}
	if c.HorizonUpdateInterval <= 0 {
		c.HorizonUpdateInterval = types.Duration(time.Minute)
	}
	if c.HorizonMinSamples <= 0 {
		c.HorizonMinSamples = 20
	}
	if c.FastWindow <= 0 {
		c.FastWindow = types.Duration(60 * time.Second)
	}
	if c.FastEvidenceWindow <= 0 {
		// Preserve the old behavior for configs that do not opt into a
		// sparse-market coverage window explicitly.
		c.FastEvidenceWindow = c.FastWindow
	}
	if c.FastEvidenceMinTrades <= 0 {
		c.FastEvidenceMinTrades = 20
	}
	if c.FastEvidenceMinBBOUpdates <= 0 {
		c.FastEvidenceMinBBOUpdates = 20
	}
	if c.DirectionSkewBps <= 0 {
		c.DirectionSkewBps = 10
	}
	if c.ImbalanceSkewBps <= 0 {
		c.ImbalanceSkewBps = 10
	}
	if c.SideAllocationSensitivity <= 0 {
		c.SideAllocationSensitivity = 0.75
	}
	if c.SideAllocationFillRateWeight <= 0 {
		c.SideAllocationFillRateWeight = 0.25
	}
	if c.SideAllocationSmoothing <= 0 || c.SideAllocationSmoothing > 1 {
		c.SideAllocationSmoothing = 0.25
	}
	if c.SideDistanceSensitivity <= 0 {
		c.SideDistanceSensitivity = 0.75
	}
	c.InventoryReset.setDefaults(c.QuoteNotional)
}

// DynamicQuoteNotional allocates the configured inventory risk budget across
// the expected number of simultaneous quote tickets. The denominator is the
// z-score adverse move over the selected horizon, so a larger observed
// volatility or a longer holding window automatically reduces each ticket.
// There is deliberately no strategy minimum or maximum here. Exchange
// quantity/notional filters and account/inventory risk limits are enforced at
// order construction time; adding a fixed policy clamp would make the sizing
// discontinuous and hide the statistical risk calculation.
func (c MarketMakerConfig) DynamicQuoteNotional(volatilityBpsPerSqrtSec float64, horizon time.Duration) float64 {
	return c.DynamicQuoteNotionalWithFillRates(volatilityBpsPerSqrtSec, horizon, 0, 0)
}

// EffectiveOrderLevels estimates how many quote tickets are likely to be
// consumed during the selected horizon. It uses the slower of the up/down
// crossing rates so a one-sided market cannot be mistaken for healthy capital
// turnover. During cold start, when neither side has an observed crossing,
// it applies a Gamma-Poisson prior of one two-sided crossing per horizon
// lookback. That prior produces one conservative expected ticket (rather than
// a fixed quote size) and lets a new symbol gather observations. A one-sided
// observation still falls back to the configured worst-case ticket count.
func (c MarketMakerConfig) EffectiveOrderLevels(horizon time.Duration, upCrossesPerHour, downCrossesPerHour float64) float64 {
	c.setDefaults()
	maxLevels := math.Max(1, c.InventoryMaxOrderLevels)
	twoSidedRate := math.Min(math.Max(0, upCrossesPerHour), math.Max(0, downCrossesPerHour))
	if twoSidedRate <= 0 || horizon <= 0 {
		if upCrossesPerHour <= 0 && downCrossesPerHour <= 0 && horizon > 0 && time.Duration(c.HorizonLookback).Hours() > 0 {
			// alpha=1 pseudo-crossing and beta=lookback-hours. The expected
			// count over a shorter horizon is below one, so retain one ticket
			// as the minimum actionable exposure for bootstrap.
			priorRate := 1 / time.Duration(c.HorizonLookback).Hours()
			return math.Min(maxLevels, math.Max(1, priorRate*horizon.Hours()))
		}
		return maxLevels
	}
	expectedCrossings := twoSidedRate * horizon.Hours()
	return math.Min(maxLevels, math.Max(1, expectedCrossings))
}

// DynamicQuoteNotionalWithFillRates extends the risk-sized ticket with the
// expected two-sided fill load. The risk budget is allocated across expected
// tickets, not blindly across the maximum number of inventory levels; this
// improves capital efficiency when the observed market is sparse while
// remaining conservative when the flow is one-sided or unobserved.
func (c MarketMakerConfig) DynamicQuoteNotionalWithFillRates(volatilityBpsPerSqrtSec float64, horizon time.Duration, upCrossesPerHour, downCrossesPerHour float64) float64 {
	c.setDefaults()
	if volatilityBpsPerSqrtSec <= 0 || horizon <= 0 || c.InventoryRiskBudgetJPY <= 0 {
		return 0
	}
	riskMoveBps := c.InventoryRiskZScore * volatilityBpsPerSqrtSec * math.Sqrt(horizon.Seconds())
	if riskMoveBps <= 0 {
		return 0
	}
	levels := c.EffectiveOrderLevels(horizon, upCrossesPerHour, downCrossesPerHour)
	riskNotional := c.InventoryRiskBudgetJPY / ((riskMoveBps / 10_000) * levels)
	if !math.IsNaN(riskNotional) && !math.IsInf(riskNotional, 0) && riskNotional > 0 {
		return riskNotional
	}
	return 0
}

// SideQuoteAllocationInput contains the state used to split a common
// risk-sized quote notional between the two sides. BuyFillRate corresponds to
// downward crossings (bid fills), while SellFillRate corresponds to upward
// crossings (ask fills).
type SideQuoteAllocationInput struct {
	Inventory       float64
	InventoryTarget float64
	InventoryLimit  float64
	DirectionSignal float64
	BookImbalance   float64
	BuyFillRate     float64
	SellFillRate    float64
}

// SideQuoteNotionals is the side-specific result of the risk allocation.
// Factors are <= 1 by design: side allocation can de-risk the side exposed to
// inventory/adverse-selection pressure, but cannot silently exceed the common
// inventory risk budget.
type SideQuoteNotionals struct {
	Buy        float64
	Sell       float64
	Bias       float64
	BuyFactor  float64
	SellFactor float64
}

func clampSideAllocation(value float64) float64 {
	return math.Max(-1, math.Min(1, value))
}

// SideAllocationBias returns a bounded, continuous pressure score. Positive
// values mean the bid is the riskier side and should be reduced; negative
// values mean the ask should be reduced. Inventory is the primary signal,
// while direction, book imbalance, and relative fill intensity provide
// smaller, damped adjustments.
func (c MarketMakerConfig) SideAllocationBias(in SideQuoteAllocationInput) float64 {
	c.setDefaults()
	limit := in.InventoryLimit
	if limit <= 0 {
		limit = c.InventoryLimit
	}
	inventoryBias := 0.0
	if limit > 0 {
		inventoryBias = clampSideAllocation((in.Inventory - in.InventoryTarget) / limit)
	}
	direction := clampSideAllocation(in.DirectionSignal)
	imbalance := clampSideAllocation(in.BookImbalance)
	buyRate := math.Max(0, in.BuyFillRate)
	sellRate := math.Max(0, in.SellFillRate)
	// log1p plus tanh makes a sparse-rate estimate useful without allowing one
	// noisy observation to dominate the inventory signal.
	rateBias := math.Tanh(math.Log1p(buyRate) - math.Log1p(sellRate))
	pressure := inventoryBias - 0.5*(direction+imbalance) + c.SideAllocationFillRateWeight*rateBias
	return clampSideAllocation(c.SideAllocationSensitivity * pressure)
}

// SideQuoteNotionals allocates a common per-side risk-sized notional. The
// baseline side remains unchanged and only the side with greater estimated
// inventory/adverse-selection risk is reduced. This preserves the existing
// neutral sizing while enforcing max(buyRisk, sellRisk) <= baseline risk.
func (c MarketMakerConfig) SideQuoteNotionals(baseNotional, bias float64) SideQuoteNotionals {
	if baseNotional <= 0 {
		return SideQuoteNotionals{}
	}
	bias = clampSideAllocation(bias)
	// A one-unit score halves the exposed side. Exponential scaling is smooth,
	// positive, and has no discontinuous policy floor.
	const halfLife = math.Ln2
	buyFactor, sellFactor := 1.0, 1.0
	if bias > 0 {
		buyFactor = math.Exp(-halfLife * bias)
	} else if bias < 0 {
		sellFactor = math.Exp(halfLife * bias)
	}
	return SideQuoteNotionals{
		Buy:        baseNotional * buyFactor,
		Sell:       baseNotional * sellFactor,
		Bias:       bias,
		BuyFactor:  buyFactor,
		SellFactor: sellFactor,
	}
}

// SideQuoteDistanceBias returns a bounded directional distance adjustment from
// observed side-specific crossing rates. Positive values mean bids are already
// filling faster and may rest farther away; negative values mean bids are
// under-filling and may be brought closer. A log-rate ratio with tanh keeps a
// sparse observation from producing an unbounded price move.
func (c MarketMakerConfig) SideQuoteDistanceBias(buyFillRate, sellFillRate float64) float64 {
	buyFillRate = math.Max(0, buyFillRate)
	sellFillRate = math.Max(0, sellFillRate)
	if buyFillRate <= 0 && sellFillRate <= 0 {
		return 0
	}
	return clampSideAllocation(math.Tanh(math.Log1p(buyFillRate) - math.Log1p(sellFillRate)))
}

// MarketMakerHorizonPoint is a one-second mid-price sample used by the
// horizon optimizer. Keeping one point per second avoids letting a burst of
// BBO updates dominate the crossing count.
type MarketMakerHorizonPoint struct {
	At        time.Time
	Mid       float64
	GapBefore bool
}

// MarketMakerHorizonDecision is the currently selected trading window. The
// score is the estimated fee-adjusted two-sided edge per hour, based on the
// observed crossing frequency and the average spacing between crossings.
type MarketMakerHorizonDecision struct {
	Horizon             time.Duration
	HorizonSeconds      int64
	QuoteDistanceBps    float64
	UpCrosses           int
	DownCrosses         int
	UpCrossesPerHour    float64
	DownCrossesPerHour  float64
	MeanUpSpacing       time.Duration
	MeanDownSpacing     time.Duration
	NetRoundTripEdgeBps float64
	ScoreBpsPerHour     float64
	UpdatedAt           time.Time
	Reason              string
}

// MarketMakerHorizonModel updates at most once per configured interval. It
// uses completed historical windows, so a newly selected horizon is only a
// reference for the next trading window; callers must not cancel an existing
// quote merely because this decision changed.
type MarketMakerHorizonModel struct {
	points     []MarketMakerHorizonPoint
	lastSecond time.Time
	lastUpdate time.Time
	decision   MarketMakerHorizonDecision
}

// EmpiricalVolatilityFloor returns a robust lower estimate of the symbol's
// realized one-second volatility. It uses the 25th percentile of non-zero
// absolute log returns, normalized by sqrt(elapsed seconds), and converts that
// percentile to a normal-equivalent sigma (q25(|N(0,1)|)=0.3186). Using a
// lower quantile prevents a temporarily quiet sample from making risk-sized
// orders explode, while requiring enough observations avoids treating a single
// stale BBO update as a regime estimate. A zero result means there is not yet
// enough reliable history; callers should then fail closed rather than guess.
func (m MarketMakerHorizonModel) EmpiricalVolatilityFloor(now time.Time, lookback time.Duration) float64 {
	if now.IsZero() || lookback <= 0 || len(m.points) < 2 {
		return 0
	}
	cutoff := now.Add(-lookback)
	returns := make([]float64, 0, len(m.points))
	for i := 1; i < len(m.points); i++ {
		previous, current := m.points[i-1], m.points[i]
		if current.GapBefore || current.At.Before(cutoff) || previous.At.Before(cutoff) || current.Mid <= 0 || previous.Mid <= 0 {
			continue
		}
		seconds := current.At.Sub(previous.At).Seconds()
		if seconds <= 0 || seconds > 120 {
			continue
		}
		absoluteBps := math.Abs(math.Log(current.Mid/previous.Mid)) * 10_000
		if absoluteBps > 0 {
			returns = append(returns, absoluteBps/math.Sqrt(seconds))
		}
	}
	if len(returns) < 8 {
		return 0
	}
	sort.Float64s(returns)
	index := int(math.Floor(0.25 * float64(len(returns)-1)))
	return returns[index] / 0.31863936396437514
}

func (m *MarketMakerHorizonModel) Observe(at time.Time, mid float64, c MarketMakerConfig) {
	gapBefore := !m.lastSecond.IsZero() && at.Truncate(time.Second).Sub(m.lastSecond) >= warmupGapThreshold
	m.ObserveWithGap(at, mid, c, gapBefore)
}

// ObserveWithGap appends a public market-price sample and marks whether the
// interval before it contained a capture outage. Horizon crossing statistics
// must not treat an outage as a continuous path; the marker is also respected
// by the realized-volatility estimator.
func (m *MarketMakerHorizonModel) ObserveWithGap(at time.Time, mid float64, c MarketMakerConfig, gapBefore bool) {
	if at.IsZero() || mid <= 0 {
		return
	}
	c.setDefaults()
	second := at.Truncate(time.Second)
	if !m.lastSecond.IsZero() && second.Equal(m.lastSecond) {
		if n := len(m.points); n > 0 {
			m.points[n-1] = MarketMakerHorizonPoint{At: second, Mid: mid, GapBefore: m.points[n-1].GapBefore || gapBefore}
		}
		return
	}
	m.lastSecond = second
	m.points = append(m.points, MarketMakerHorizonPoint{At: second, Mid: mid, GapBefore: gapBefore})
	cutoff := second.Add(-time.Duration(c.HorizonLookback) - time.Duration(c.MaxTradingWindow) - time.Minute)
	first := sort.Search(len(m.points), func(i int) bool { return !m.points[i].At.Before(cutoff) })
	if first > 0 {
		m.points = append([]MarketMakerHorizonPoint(nil), m.points[first:]...)
	}
}

func (c MarketMakerConfig) TradingHorizons() []time.Duration {
	c.setDefaults()
	all := []time.Duration{time.Minute, 3 * time.Minute, 5 * time.Minute, 10 * time.Minute, 15 * time.Minute, 20 * time.Minute, 30 * time.Minute}
	minHorizon := time.Duration(c.MinTradingWindow)
	maxHorizon := time.Duration(c.MaxTradingWindow)
	out := make([]time.Duration, 0, len(all))
	for _, horizon := range all {
		if horizon >= minHorizon && horizon <= maxHorizon {
			out = append(out, horizon)
		}
	}
	if len(out) == 0 {
		out = append(out, minHorizon)
	}
	return out
}

func (c MarketMakerConfig) HalfSpreadForHorizon(horizon time.Duration, volatilityBpsPerSqrtSec float64) float64 {
	c.setDefaults()
	baseEdge := c.MakerFeeBps + c.AdverseSelectionBps + c.MinimumNetEdgeBps/2
	move := math.Max(0, volatilityBpsPerSqrtSec) * math.Sqrt(math.Max(0, horizon.Seconds())) * c.VolatilityMultiplier
	half := math.Max(c.MinimumHalfSpreadBps, baseEdge+move)
	return math.Min(half, c.MaximumHalfSpreadBps)
}

// InventoryBand is an automatically calculated target and deviation band. Its
// upper edge is the smallest of a risk-budget cap, a finite number of quote
// tickets, and a fraction of the symbol's quote-equivalent pair equity. This
// keeps the band capital-efficient without allowing a larger account balance to
// bypass the volatility and order-level controls.
type InventoryBand struct {
	Target                   float64
	Limit                    float64
	MaxInventory             float64
	TargetRatio              float64
	OrderSize                float64
	RiskMoveBps              float64
	RiskBudgetJPY            float64
	PairEquityJPY            float64
	RiskCapNotionalJPY       float64
	LevelCapNotionalJPY      float64
	CapitalTargetNotionalJPY float64
	CapitalCapNotionalJPY    float64
}

// EffectiveInventoryRiskBudgetJPY preserves the configured absolute floor while
// scaling the risk budget with current quote-equivalent pair equity. Pair equity
// is supplied by the live strategy as total quote plus total base*mid, so locked
// maker orders do not make the risk budget oscillate with every refresh.
func (c MarketMakerConfig) EffectiveInventoryRiskBudgetJPY(pairEquityJPY float64) float64 {
	c.setDefaults()
	budget := c.InventoryRiskBudgetJPY
	if pairEquityJPY > 0 && c.InventoryRiskBudgetRatio > 0 {
		budget = math.Max(budget, pairEquityJPY*c.InventoryRiskBudgetRatio)
	}
	return budget
}

// EffectiveInventoryTargetRatio is the target fraction of the dynamic maximum.
// CapitalTargetRatio/CapitalMaxRatio is the preferred capital-based definition;
// InventoryTargetRatio remains a backwards-compatible fallback.
func (c MarketMakerConfig) EffectiveInventoryTargetRatio() float64 {
	c.setDefaults()
	if c.InventoryCapitalMaxRatio > 0 && c.InventoryCapitalTargetRatio > 0 {
		ratio := c.InventoryCapitalTargetRatio / c.InventoryCapitalMaxRatio
		if ratio > 0 && ratio < 1 {
			return ratio
		}
	}
	return c.InventoryTargetRatio
}

func (c MarketMakerConfig) DynamicInventoryBand(midPrice, volatilityBpsPerSqrtSec float64, horizon time.Duration) InventoryBand {
	return c.DynamicInventoryBandWithCapital(midPrice, volatilityBpsPerSqrtSec, horizon, 0)
}

// DynamicInventoryBandWithCapital derives absolute target/limit quantities from
// volatility, quote-ticket load, and current pair equity. The old method above
// remains for offline callers that do not have account balances.
func (c MarketMakerConfig) DynamicInventoryBandWithCapital(midPrice, volatilityBpsPerSqrtSec float64, horizon time.Duration, pairEquityJPY float64) InventoryBand {
	c.setDefaults()
	targetRatio := c.EffectiveInventoryTargetRatio()
	riskBudgetJPY := c.EffectiveInventoryRiskBudgetJPY(pairEquityJPY)
	if midPrice <= 0 {
		return InventoryBand{Target: c.InventoryTarget, Limit: c.InventoryLimit, TargetRatio: targetRatio, RiskBudgetJPY: riskBudgetJPY, PairEquityJPY: pairEquityJPY}
	}
	orderSize := c.QuoteNotional / midPrice
	riskMoveBps := c.InventoryRiskZScore * math.Max(0, volatilityBpsPerSqrtSec) * math.Sqrt(math.Max(0, horizon.Seconds()))
	riskCapNotional := math.Inf(1)
	if riskMoveBps > 0 {
		riskCapNotional = riskBudgetJPY / (riskMoveBps / 10_000)
	}
	levelCapNotional := orderSize * c.InventoryMaxOrderLevels * midPrice
	capitalCapNotional := math.Inf(1)
	capitalTargetNotional := 0.0
	if pairEquityJPY > 0 {
		capitalCapNotional = pairEquityJPY * c.InventoryCapitalMaxRatio
		capitalTargetNotional = pairEquityJPY * c.InventoryCapitalTargetRatio
	}
	maxNotional := math.Min(riskCapNotional, levelCapNotional)
	maxNotional = math.Min(maxNotional, capitalCapNotional)
	if !math.IsInf(maxNotional, 0) && maxNotional > 0 {
		maxInventory := maxNotional / midPrice
		target := maxInventory * targetRatio
		return InventoryBand{
			Target: target, Limit: maxInventory - target, MaxInventory: maxInventory,
			TargetRatio: targetRatio, OrderSize: orderSize, RiskMoveBps: riskMoveBps,
			RiskBudgetJPY: riskBudgetJPY, PairEquityJPY: pairEquityJPY,
			RiskCapNotionalJPY: riskCapNotional, LevelCapNotionalJPY: levelCapNotional,
			CapitalTargetNotionalJPY: capitalTargetNotional, CapitalCapNotionalJPY: capitalCapNotional,
		}
	}
	return InventoryBand{
		Target: c.InventoryTarget, Limit: c.InventoryLimit, TargetRatio: targetRatio,
		OrderSize: orderSize, RiskMoveBps: riskMoveBps, RiskBudgetJPY: riskBudgetJPY,
		PairEquityJPY: pairEquityJPY, RiskCapNotionalJPY: riskCapNotional,
		LevelCapNotionalJPY: levelCapNotional, CapitalTargetNotionalJPY: capitalTargetNotional,
		CapitalCapNotionalJPY: capitalCapNotional,
	}
}

func (m *MarketMakerHorizonModel) Update(now time.Time, c MarketMakerConfig, volatilityBpsPerSqrtSec float64) MarketMakerHorizonDecision {
	c.setDefaults()
	if !m.lastUpdate.IsZero() && now.Sub(m.lastUpdate) < time.Duration(c.HorizonUpdateInterval) && m.decision.Horizon > 0 {
		return m.decision
	}
	m.lastUpdate = now
	best := MarketMakerHorizonDecision{Reason: "insufficient completed horizon samples", UpdatedAt: now}
	for _, horizon := range c.TradingHorizons() {
		distance := c.HalfSpreadForHorizon(horizon, volatilityBpsPerSqrtSec)
		var upCrosses, downCrosses int
		var firstStart time.Time
		var lastUpStart, lastDownStart time.Time
		var upSpacing, downSpacing []time.Duration
		lastStartSample := time.Time{}
		cutoff := now.Add(-time.Duration(c.HorizonLookback))
		for i := 0; i < len(m.points); i++ {
			start := m.points[i]
			if start.At.Before(cutoff) || start.GapBefore {
				continue
			}
			endAt := start.At.Add(horizon)
			if endAt.After(now) || (!lastStartSample.IsZero() && start.At.Sub(lastStartSample) < time.Minute) {
				continue
			}
			j := sort.Search(len(m.points), func(k int) bool { return !m.points[k].At.Before(endAt) })
			if j <= i+1 {
				continue
			}
			maxMid, minMid := start.Mid, start.Mid
			continuous := true
			for k := i + 1; k < j; k++ {
				if m.points[k].GapBefore {
					continuous = false
					break
				}
				if m.points[k].Mid > maxMid {
					maxMid = m.points[k].Mid
				}
				if m.points[k].Mid < minMid {
					minMid = m.points[k].Mid
				}
			}
			if !continuous {
				continue
			}
			up := math.Log(maxMid/start.Mid) * 10_000
			down := math.Log(start.Mid/minMid) * 10_000
			upEvent := up >= distance && (lastUpStart.IsZero() || start.At.Sub(lastUpStart) >= horizon)
			downEvent := down >= distance && (lastDownStart.IsZero() || start.At.Sub(lastDownStart) >= horizon)
			if upEvent {
				upCrosses++
				if !lastUpStart.IsZero() {
					upSpacing = append(upSpacing, start.At.Sub(lastUpStart))
				}
				lastUpStart = start.At
			}
			if downEvent {
				downCrosses++
				if !lastDownStart.IsZero() {
					downSpacing = append(downSpacing, start.At.Sub(lastDownStart))
				}
				lastDownStart = start.At
			}
			if firstStart.IsZero() {
				firstStart = start.At
			}
			lastStartSample = start.At
		}
		if firstStart.IsZero() || lastStartSample.Sub(firstStart) <= 0 {
			continue
		}
		sampleHours := lastStartSample.Sub(firstStart).Hours()
		if sampleHours <= 0 || upCrosses+downCrosses < c.HorizonMinSamples {
			continue
		}
		upRate := float64(upCrosses) / sampleHours
		downRate := float64(downCrosses) / sampleHours
		netEdge := 2*distance - 2*c.MakerFeeBps - 2*c.AdverseSelectionBps - c.MinimumNetEdgeBps
		score := math.Min(upRate, downRate) * math.Max(0, netEdge)
		if best.Horizon == 0 || score > best.ScoreBpsPerHour {
			best = MarketMakerHorizonDecision{
				Horizon: horizon, HorizonSeconds: int64(horizon.Seconds()), QuoteDistanceBps: distance,
				UpCrosses: upCrosses, DownCrosses: downCrosses,
				UpCrossesPerHour: upRate, DownCrossesPerHour: downRate,
				MeanUpSpacing: meanDuration(upSpacing), MeanDownSpacing: meanDuration(downSpacing),
				NetRoundTripEdgeBps: netEdge, ScoreBpsPerHour: score,
				UpdatedAt: now, Reason: "max fee-adjusted two-sided edge per hour",
			}
		}
	}
	if best.Horizon == 0 {
		horizon := time.Duration(c.MinTradingWindow)
		best.Horizon = horizon
		best.HorizonSeconds = int64(horizon.Seconds())
		best.QuoteDistanceBps = c.HalfSpreadForHorizon(horizon, volatilityBpsPerSqrtSec)
		best.UpdatedAt = now
	}
	m.decision = best
	return best
}

func meanDuration(values []time.Duration) time.Duration {
	if len(values) == 0 {
		return 0
	}
	var total time.Duration
	for _, value := range values {
		total += value
	}
	return total / time.Duration(len(values))
}

// RefreshIntervals estimates a quote's first-passage time from the observed
// volatility. The lower bound prevents cancel churn; the upper bound keeps a
// stale quote from resting indefinitely. Volatility is expressed in bps per
// square-root second, matching GammaCaptureVolatility*10_000.
func (c MarketMakerConfig) RefreshIntervals(halfSpreadBps, volatilityBpsPerSqrtSec float64) (time.Duration, time.Duration) {
	c.setDefaults()
	minRefresh := time.Duration(c.MinRefreshInterval)
	maxRefresh := time.Duration(c.RefreshInterval)
	if maxRefresh <= 0 {
		maxRefresh = time.Minute
	}
	if halfSpreadBps > 0 && volatilityBpsPerSqrtSec > 0 {
		expectedSeconds := math.Pow(halfSpreadBps/volatilityBpsPerSqrtSec, 2)
		// Reprice no more often than roughly one quarter of the expected
		// crossing time, and force a refresh no later than that time.
		adaptiveMin := time.Duration(expectedSeconds * 0.25 * float64(time.Second))
		adaptiveMax := time.Duration(expectedSeconds * float64(time.Second))
		if adaptiveMin > minRefresh {
			minRefresh = adaptiveMin
		}
		if adaptiveMax > maxRefresh {
			maxRefresh = adaptiveMax
		}
	}
	if cap := time.Duration(c.MaxRefreshInterval); cap > 0 {
		if maxRefresh > cap {
			maxRefresh = cap
		}
		if minRefresh > cap {
			minRefresh = cap
		}
	}
	if maxRefresh < minRefresh {
		maxRefresh = minRefresh
	}
	return minRefresh, maxRefresh
}

// BoundRefreshIntervals prevents the adaptive first-passage estimate from
// silently extending a quote beyond the selected trading window. The window
// is the maximum age of a quote; MinRefreshInterval remains the anti-churn
// floor inside that window.
func BoundRefreshIntervals(minRefresh, maxRefresh, window time.Duration) (time.Duration, time.Duration) {
	if window <= 0 {
		return minRefresh, maxRefresh
	}
	if minRefresh > window {
		minRefresh = window
	}
	if maxRefresh > window {
		maxRefresh = window
	}
	if maxRefresh < minRefresh {
		maxRefresh = minRefresh
	}
	return minRefresh, maxRefresh
}

func (c *InventoryResetConfig) setDefaults(quoteNotional float64) {
	if c.MaxAskAge <= 0 {
		c.MaxAskAge = types.Duration(3 * time.Minute)
	}
	if c.FastAskAge <= 0 {
		c.FastAskAge = types.Duration(1 * time.Minute)
	}
	if c.AdverseMoveBps <= 0 {
		c.AdverseMoveBps = 40
	}
	if c.FastAdverseMoveBps <= 0 {
		c.FastAdverseMoveBps = 20
	}
	if c.FastDirectionThreshold <= 0 {
		c.FastDirectionThreshold = 0.25
	}
	if c.MaxSlippageBps <= 0 {
		c.MaxSlippageBps = 25
	}
	if c.ReductionNotional <= 0 {
		c.ReductionNotional = quoteNotional
	}
	if c.Cooldown <= 0 {
		c.Cooldown = types.Duration(15 * time.Minute)
	}
	if c.FillIntensityHaircut <= 0 || c.FillIntensityHaircut > 1 {
		c.FillIntensityHaircut = 0.25
	}
	if c.RiskZScore <= 0 {
		c.RiskZScore = 1.645
	}
}

type MarketMakerQuoteInput struct {
	MidPrice              float64
	BestBid               float64
	BestAsk               float64
	VolatilityBps         float64 // expected one-sided move over the quote lifetime
	VolatilityPerSqrtSec  float64 // instantaneous volatility in bps / sqrt(second)
	TradingHorizonSeconds float64 // selected trading window; zero uses fixed-point fallback
	Inventory             float64
	DirectionSignal       float64 // [-1,1], positive means short-term upward pressure
	BookImbalance         float64 // [-1,1], positive means more bid than ask size
	SideDistanceBias      float64 // [-1,1], negative brings bid closer; positive brings ask closer
	CanBuy                bool
	CanSell               bool
}

type MarketMakerQuotePlan struct {
	BidPrice       float64
	AskPrice       float64
	BidDistanceBps float64
	AskDistanceBps float64
	HalfSpreadBps  float64
	InventorySkew  float64
	AllowBid       bool
	AllowAsk       bool
	Reason         string
}

// Quote computes a conservative two-sided maker-only quote.  A positive
// inventory shifts both prices down: the bid is less attractive and the ask is
// closer, encouraging inventory reduction without crossing the book.
func (c MarketMakerConfig) Quote(in MarketMakerQuoteInput) MarketMakerQuotePlan {
	plan := MarketMakerQuotePlan{}
	if in.MidPrice <= 0 || in.BestBid <= 0 || in.BestAsk <= 0 || in.BestAsk <= in.BestBid {
		plan.Reason = "invalid book"
		return plan
	}
	c.setDefaults()
	// The exchange BBO can be narrower than the fee floor. That is not itself
	// a reason to stop quoting: a maker can rest outside the BBO and wait for a
	// larger move. The fee/adverse-selection floor is applied to our quote
	// distance below, not incorrectly to the observed inside spread.
	baseEdge := c.MakerFeeBps + c.AdverseSelectionBps + c.MinimumNetEdgeBps/2
	sideFloor := math.Max(c.MinimumHalfSpreadBps, baseEdge)
	vol := math.Max(0, in.VolatilityBps) * c.VolatilityMultiplier
	half := math.Max(c.MinimumHalfSpreadBps, baseEdge+vol)
	if in.TradingHorizonSeconds > 0 && in.VolatilityPerSqrtSec > 0 {
		horizon := time.Duration(in.TradingHorizonSeconds * float64(time.Second))
		half = c.HalfSpreadForHorizon(horizon, in.VolatilityPerSqrtSec)
	} else if in.VolatilityPerSqrtSec > 0 {
		// Solve the quote-distance/time-scale fixed point. A wider quote is
		// allowed to live longer, so its volatility buffer must be measured over
		// that longer horizon rather than over one minute or one second.
		half = math.Max(c.MinimumHalfSpreadBps, baseEdge)
		for i := 0; i < 8; i++ {
			_, horizon := c.RefreshIntervals(half, in.VolatilityPerSqrtSec)
			move := in.VolatilityPerSqrtSec * math.Sqrt(horizon.Seconds()) * c.VolatilityMultiplier
			candidate := math.Max(c.MinimumHalfSpreadBps, baseEdge+move)
			if candidate > c.MaximumHalfSpreadBps {
				candidate = c.MaximumHalfSpreadBps
			}
			if math.Abs(candidate-half) < 1e-6 {
				half = candidate
				break
			}
			half = candidate
		}
	}
	half = math.Min(half, c.MaximumHalfSpreadBps)
	limit := c.InventoryLimit
	if limit <= 0 {
		limit = 1
	}
	ratio := (in.Inventory - c.InventoryTarget) / limit
	ratio = math.Max(-1, math.Min(1, ratio))
	skew := ratio * c.InventorySkewBps
	directionSignal := math.Max(-1, math.Min(1, in.DirectionSignal))
	bookImbalance := math.Max(-1, math.Min(1, in.BookImbalance))
	adaptiveSkew := directionSignal*c.DirectionSkewBps + bookImbalance*c.ImbalanceSkewBps
	adaptiveSkew = math.Max(-c.MaximumHalfSpreadBps, math.Min(c.MaximumHalfSpreadBps, adaptiveSkew))
	sideDistanceBias := clampSideAllocation(in.SideDistanceBias)
	distanceRoom := math.Max(0, half-sideFloor)
	distanceSkew := sideDistanceBias * c.SideDistanceSensitivity * distanceRoom
	// Positive skew (long inventory) lowers the bid distance and raises the ask
	// distance in price terms: bid farther away, ask closer to mid.
	bidBps := half + skew - adaptiveSkew + distanceSkew
	askBps := half - skew + adaptiveSkew - distanceSkew
	// Skew may move one side closer to mid, but never below the fee,
	// adverse-selection, and minimum-net-edge floor. The volatility buffer can
	// be relaxed asymmetrically only when side-specific statistics support it;
	// sparse data therefore leaves the original symmetric distance unchanged.
	bidBps = math.Min(c.MaximumHalfSpreadBps, math.Max(sideFloor, bidBps))
	askBps = math.Min(c.MaximumHalfSpreadBps, math.Max(sideFloor, askBps))
	plan.BidPrice = in.MidPrice * math.Exp(-bidBps/10_000)
	plan.AskPrice = in.MidPrice * math.Exp(askBps/10_000)
	// Never submit a quote that could execute immediately against the observed
	// BBO.  The one-tick adjustment belongs to the exchange formatter.
	if plan.BidPrice >= in.BestAsk {
		plan.BidPrice = math.Nextafter(in.BestAsk, 0)
	}
	if plan.AskPrice <= in.BestBid {
		plan.AskPrice = math.Nextafter(in.BestBid, math.Inf(1))
	}
	plan.BidDistanceBps = math.Log(in.MidPrice/plan.BidPrice) * 10_000
	plan.AskDistanceBps = math.Log(plan.AskPrice/in.MidPrice) * 10_000
	plan.HalfSpreadBps = half
	plan.InventorySkew = skew
	plan.AllowBid = in.CanBuy && in.Inventory < c.InventoryTarget+limit
	plan.AllowAsk = in.CanSell && in.Inventory > c.InventoryTarget-limit
	plan.Reason = "quoted"
	return plan
}

func (c MarketMakerConfig) fastIntensityConfig() IntensityConfig {
	window := c.FastWindow
	if window <= 0 {
		window = types.Duration(time.Minute)
	}
	return IntensityConfig{
		Window:           window,
		VolatilityWindow: window,
		PriorAlphaUp:     1,
		PriorBetaUp:      10,
		PriorAlphaDown:   1,
		PriorBetaDown:    10,
		MinEvents:        1,
	}
}
