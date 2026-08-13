package gammacapture

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

// MacroInventoryConfig controls portfolio-horizon inventory risk. The
// configured capital target remains a strategic prior; this controller derives
// the live target and absolute cap from causal return and wealth state.
type MacroInventoryConfig struct {
	Enabled                bool                   `json:"enabled" yaml:"enabled"`
	NoTradeRegion          NoTradeInventoryConfig `json:"noTradeRegion" yaml:"noTradeRegion"`
	BarInterval            types.Duration         `json:"barInterval" yaml:"barInterval"`
	Lookback               types.Duration         `json:"lookback" yaml:"lookback"`
	RiskHorizons           []types.Duration       `json:"riskHorizons" yaml:"riskHorizons"`
	MinimumSamples         int                    `json:"minimumSamples" yaml:"minimumSamples"`
	RiskAversion           float64                `json:"riskAversion" yaml:"riskAversion"`
	PriorStrength          float64                `json:"priorStrength" yaml:"priorStrength"`
	DriftPriorSamples      float64                `json:"driftPriorSamples" yaml:"driftPriorSamples"`
	DownsideZScore         float64                `json:"downsideZScore" yaml:"downsideZScore"`
	CarryRiskBudgetRatio   float64                `json:"carryRiskBudgetRatio" yaml:"carryRiskBudgetRatio"`
	MaxWealthDrawdownRatio float64                `json:"maxWealthDrawdownRatio" yaml:"maxWealthDrawdownRatio"`
	ReversalAccumulation   MacroReversalConfig    `json:"reversalAccumulation" yaml:"reversalAccumulation"`
}

// MacroReversalConfig enables a tactical, rolling inventory tranche. Every
// configured Macro risk horizon participates; confidence, fees, risk aversion,
// and loss budget are inherited from the existing live policy.
type MacroReversalConfig struct {
	Enabled               bool                       `json:"enabled" yaml:"enabled"`
	EarlyDetection        bool                       `json:"earlyDetection" yaml:"earlyDetection"`
	EarlyPosteriorBars    int                        `json:"earlyPosteriorBars" yaml:"earlyPosteriorBars"`
	EarlyMinimumPriorBars int                        `json:"earlyMinimumPriorBars" yaml:"earlyMinimumPriorBars"`
	ActiveExecution       MacroActiveExecutionConfig `json:"activeExecution" yaml:"activeExecution"`
}

// MacroActiveExecutionConfig enables a bounded taker tranche only when the
// confidence-adjusted cost of waiting for the passive quote exceeds the cost
// of crossing. The threshold, price budget, cadence, and size are learned or
// derived at runtime; Enabled is deliberately the only policy switch.
type MacroActiveExecutionConfig struct {
	Enabled bool `json:"enabled" yaml:"enabled"`
}

func (c *MacroReversalConfig) setDefaults() {
	if c.EarlyPosteriorBars == 0 {
		c.EarlyPosteriorBars = 2
	}
	if c.EarlyMinimumPriorBars == 0 {
		c.EarlyMinimumPriorBars = 6
	}
}
func (c *MacroInventoryConfig) setDefaults() {
	if c.BarInterval == 0 {
		c.BarInterval = types.Duration(15 * time.Minute)
	}
	if c.Lookback == 0 {
		c.Lookback = types.Duration(240 * time.Hour)
	}
	if len(c.RiskHorizons) == 0 {
		c.RiskHorizons = []types.Duration{
			types.Duration(3 * time.Hour),
			types.Duration(6 * time.Hour),
			types.Duration(24 * time.Hour),
		}
	}
	if c.MinimumSamples == 0 {
		c.MinimumSamples = 8
	}
	if c.RiskAversion == 0 {
		c.RiskAversion = 1
	}
	if c.PriorStrength == 0 {
		c.PriorStrength = 0.05
	}
	if c.DriftPriorSamples == 0 {
		c.DriftPriorSamples = 20
	}
	if c.DownsideZScore == 0 {
		c.DownsideZScore = 2.326347874
	}
	if c.CarryRiskBudgetRatio == 0 {
		c.CarryRiskBudgetRatio = 0.01
	}
	if c.MaxWealthDrawdownRatio == 0 {
		c.MaxWealthDrawdownRatio = 0.05
	}
	c.ReversalAccumulation.setDefaults()
}

func (c MacroInventoryConfig) validate() error {
	c.setDefaults()
	bar := time.Duration(c.BarInterval)
	lookback := time.Duration(c.Lookback)
	if bar <= 0 || lookback <= 0 || c.MinimumSamples < 2 || c.RiskAversion <= 0 ||
		c.PriorStrength <= 0 || c.DriftPriorSamples < 0 || c.DownsideZScore <= 0 ||
		c.CarryRiskBudgetRatio <= 0 || c.CarryRiskBudgetRatio >= 1 ||
		c.MaxWealthDrawdownRatio <= 0 || c.MaxWealthDrawdownRatio >= 1 {
		return fmt.Errorf("macro inventory parameters are outside their admissible ranges")
	}
	for _, configured := range c.RiskHorizons {
		horizon := time.Duration(configured)
		if horizon < bar || horizon%bar != 0 {
			return fmt.Errorf("macro inventory horizon %s must be a positive multiple of bar interval %s", horizon, bar)
		}
	}
	if c.ReversalAccumulation.EarlyDetection &&
		(c.ReversalAccumulation.EarlyPosteriorBars < 2 ||
			c.ReversalAccumulation.EarlyMinimumPriorBars < 3) {
		return fmt.Errorf("macro early reversal requires at least two posterior bars and three prior bars")
	}
	return nil
}

func (c MacroInventoryConfig) horizons() []time.Duration {
	c.setDefaults()
	seen := make(map[time.Duration]struct{}, len(c.RiskHorizons))
	out := make([]time.Duration, 0, len(c.RiskHorizons))
	for _, configured := range c.RiskHorizons {
		horizon := time.Duration(configured)
		if horizon <= 0 {
			continue
		}
		if _, ok := seen[horizon]; ok {
			continue
		}
		seen[horizon] = struct{}{}
		out = append(out, horizon)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func (c MacroInventoryConfig) requiredHistory() time.Duration {
	c.setDefaults()
	longest := time.Duration(0)
	for _, horizon := range c.horizons() {
		if horizon > longest {
			longest = horizon
		}
	}
	return time.Duration(c.Lookback) + longest + time.Duration(c.BarInterval)
}

type macroInventoryBar struct {
	At      time.Time
	Mid     float64
	Bid     float64
	Ask     float64
	Segment uint64
}

type macroEstimateCacheKey struct {
	Horizon, BarInterval, Lookback time.Duration
	MinimumSamples                 int
	DriftPriorSamples              float64
	DownsideZScore                 float64
}

type macroEstimateCacheEntry struct {
	BarCount   int
	LastClosed time.Time
	Estimate   MacroReturnEstimate
}

// MacroInventoryModel retains only causally closed bars. It is rebuilt from
// the same Binance BBO capture used by the regular evidence warmup.
type MacroInventoryModel struct {
	bars                []macroInventoryBar
	currentStart        time.Time
	currentMid          float64
	currentBid          float64
	currentAsk          float64
	currentSegment      uint64
	lastObservation     time.Time
	estimateCache       map[macroEstimateCacheKey]macroEstimateCacheEntry
	reversalCache       map[time.Duration]macroReversalCacheEntry
	trendExcursionCache trendExcursionCacheEntry
	estimateCacheBuilds uint64
	reversalCacheBuilds uint64
}

func (m *MacroInventoryModel) LatestClosedBarAt() time.Time {
	if m == nil || len(m.bars) == 0 {
		return time.Time{}
	}
	return m.bars[len(m.bars)-1].At
}

func (m *MacroInventoryModel) Observe(at time.Time, mid float64, gapBefore bool, c MacroInventoryConfig) {
	m.ObserveBBO(at, mid, mid, mid, gapBefore, c)
}

// ObserveBook retains the executable BUY-side ask close in addition to mid.
// Slow carrying risk continues to use mid, while reversal accumulation uses
// ask because a BUY cannot be executed at mid in a wide market.
func (m *MacroInventoryModel) ObserveBook(at time.Time, mid, ask float64, gapBefore bool, c MacroInventoryConfig) {
	m.ObserveBBO(at, mid, mid, ask, gapBefore, c)
}

// ObserveBBO retains both executable sides. Bottom reversals are evaluated on
// ask closes because increasing inventory crosses the ask opportunity set;
// top reversals use bid closes because reducing inventory must be executable at
// the bid. A fast-model data gap shorter than one macro bar does not create a
// new macro segment: doing so would discard hours of valid closed bars whenever
// startup replay hands off to the websocket inside the same 10-minute bucket.
func (m *MacroInventoryModel) ObserveBBO(at time.Time, mid, bid, ask float64, gapBefore bool, c MacroInventoryConfig) {
	if at.IsZero() || mid <= 0 || bid <= 0 || ask <= 0 || !c.Enabled {
		return
	}
	// Strategy configs are normalized once at startup. Keep the defaulting
	// fallback for zero-value callers, but do not repeat it for every BBO.
	if c.BarInterval <= 0 || c.Lookback <= 0 || len(c.RiskHorizons) == 0 {
		c.setDefaults()
	}
	interval := time.Duration(c.BarInterval)
	start := at.UTC().Truncate(interval)
	macroGap := !m.lastObservation.IsZero() && at.Sub(m.lastObservation) >= interval
	_ = gapBefore // sub-bar gaps remain valid within-bar sampling gaps for the macro model
	if m.currentStart.IsZero() {
		m.currentSegment = 1
		m.currentStart = start
		m.currentMid = mid
		m.currentBid = bid
		m.currentAsk = ask
		m.lastObservation = at
		return
	}
	if start.Before(m.currentStart) {
		return
	}
	if start.Equal(m.currentStart) {
		m.currentMid = mid
		m.currentBid = bid
		m.currentAsk = ask
		m.lastObservation = at
		return
	}
	m.bars = append(m.bars, macroInventoryBar{
		At: m.currentStart.Add(interval), Mid: m.currentMid, Bid: m.currentBid, Ask: m.currentAsk, Segment: m.currentSegment,
	})
	if macroGap || start.Sub(m.currentStart) > interval {
		m.currentSegment++
	}
	m.currentStart = start
	m.currentMid = mid
	m.currentBid = bid
	m.currentAsk = ask
	m.lastObservation = at
	cutoff := at.Add(-c.requiredHistory())
	first := sort.Search(len(m.bars), func(i int) bool { return !m.bars[i].At.Before(cutoff) })
	if first > 0 {
		m.bars = append([]macroInventoryBar(nil), m.bars[first:]...)
	}
}

// MacroReturnEstimate is a horizon-consistent rolling return distribution.
// Every configured horizon advances on each closed macro bar. Overlapping
// returns estimate the marginal distribution, while EffectiveSamples divides
// raw observations by H/barInterval so drift reliability is not inflated by
// the shared path.
type MacroReturnEstimate struct {
	Horizon                time.Duration
	Mean                   float64
	ShrunkMean             float64
	Variance               float64
	StandardDeviation      float64
	EmpiricalDownsideLoss  float64
	GaussianDownsideLoss   float64
	DownsideLoss           float64
	LatestReturn           float64
	RawSamples             int
	EffectiveSamples       float64
	Samples                int
	Sufficient             bool
	FallbackVarianceWeight float64
	UsedFallback           bool
}

// Estimate caches the O(history) rolling distribution at the latest closed
// bar. Intrabar calls reuse it and apply only the current executable-volatility
// fallback, so BBO frequency does not multiply macro-history work.
func (m *MacroInventoryModel) Estimate(now time.Time, horizon time.Duration, fallbackVolatilityBpsPerSqrtSec float64, c MacroInventoryConfig) MacroReturnEstimate {
	c.setDefaults()
	lastClosed := time.Time{}
	if len(m.bars) > 0 {
		lastClosed = m.bars[len(m.bars)-1].At
	}
	key := macroEstimateCacheKey{
		Horizon: horizon, BarInterval: time.Duration(c.BarInterval),
		Lookback: time.Duration(c.Lookback), MinimumSamples: c.MinimumSamples,
		DriftPriorSamples: c.DriftPriorSamples, DownsideZScore: c.DownsideZScore,
	}
	if entry, ok := m.estimateCache[key]; ok && entry.BarCount == len(m.bars) && entry.LastClosed.Equal(lastClosed) {
		return applyMacroVolatilityFallback(entry.Estimate, fallbackVolatilityBpsPerSqrtSec, c)
	}
	asOf := now
	if !lastClosed.IsZero() {
		asOf = lastClosed
	}
	base := m.estimateUncached(asOf, horizon, 0, c)
	if m.estimateCache == nil {
		m.estimateCache = make(map[macroEstimateCacheKey]macroEstimateCacheEntry)
	}
	m.estimateCache[key] = macroEstimateCacheEntry{BarCount: len(m.bars), LastClosed: lastClosed, Estimate: base}
	m.estimateCacheBuilds++
	return applyMacroVolatilityFallback(base, fallbackVolatilityBpsPerSqrtSec, c)
}

func applyMacroVolatilityFallback(base MacroReturnEstimate, fallbackVolatilityBpsPerSqrtSec float64, c MacroInventoryConfig) MacroReturnEstimate {
	estimate := base
	if !estimate.Sufficient {
		estimate.UsedFallback = true
		estimate.ShrunkMean = 0
		fallbackStdDev := math.Max(0, fallbackVolatilityBpsPerSqrtSec) / 10_000 * math.Sqrt(estimate.Horizon.Seconds())
		fallbackVariance := fallbackStdDev * fallbackStdDev
		estimate.FallbackVarianceWeight = macroFallbackVarianceWeight(estimate.EffectiveSamples, c.MinimumSamples)
		estimate.Variance += estimate.FallbackVarianceWeight * math.Max(0, fallbackVariance-estimate.Variance)
		estimate.StandardDeviation = math.Sqrt(math.Max(0, estimate.Variance))
	}
	estimate.GaussianDownsideLoss = math.Max(0, c.DownsideZScore*estimate.StandardDeviation-estimate.ShrunkMean)
	estimate.DownsideLoss = math.Max(estimate.EmpiricalDownsideLoss, estimate.GaussianDownsideLoss)
	return estimate
}

// macroFallbackVarianceWeight removes the discontinuity at MinimumSamples.
// One effective sample has no usable variance degrees of freedom and therefore
// receives the full live-volatility fallback. The weight then decays linearly
// to zero as the rolling horizon estimate reaches the sufficiency threshold.
func macroFallbackVarianceWeight(effectiveSamples float64, minimumSamples int) float64 {
	if minimumSamples <= 1 {
		return 0
	}
	weight := (float64(minimumSamples) - effectiveSamples) / float64(minimumSamples-1)
	return math.Max(0, math.Min(1, weight))
}

func (m *MacroInventoryModel) estimateUncached(now time.Time, horizon time.Duration, fallbackVolatilityBpsPerSqrtSec float64, c MacroInventoryConfig) MacroReturnEstimate {
	c.setDefaults()
	estimate := MacroReturnEstimate{Horizon: horizon}
	barInterval := time.Duration(c.BarInterval)
	if now.IsZero() || horizon <= 0 || barInterval <= 0 {
		return estimate
	}
	cutoff := now.Add(-time.Duration(c.Lookback))
	returns := make([]float64, 0)
	for index, bar := range m.bars {
		if bar.At.After(now) || bar.At.Before(cutoff) || bar.Mid <= 0 {
			continue
		}
		previousAt := bar.At.Add(-horizon)
		previousIndex := sort.Search(index, func(i int) bool { return !m.bars[i].At.Before(previousAt) })
		if previousIndex >= index {
			continue
		}
		previous := m.bars[previousIndex]
		if !previous.At.Equal(previousAt) || previous.Segment != bar.Segment || previous.Mid <= 0 {
			continue
		}
		value := math.Log(bar.Mid / previous.Mid)
		returns = append(returns, value)
		estimate.LatestReturn = value
	}
	if len(returns) > 0 {
		for _, value := range returns {
			estimate.Mean += value
		}
		estimate.Mean /= float64(len(returns))
		if len(returns) > 1 {
			for _, value := range returns {
				delta := value - estimate.Mean
				estimate.Variance += delta * delta
			}
			estimate.Variance /= float64(len(returns) - 1)
		}
		estimate.StandardDeviation = math.Sqrt(math.Max(0, estimate.Variance))
		estimate.RawSamples = len(returns)
		overlapFactor := math.Max(1, horizon.Seconds()/barInterval.Seconds())
		estimate.EffectiveSamples = float64(estimate.RawSamples) / overlapFactor
		estimate.Samples = int(math.Floor(estimate.EffectiveSamples + 1e-12))
		sorted := append([]float64(nil), returns...)
		sort.Float64s(sorted)
		// Select the empirical tail on the effective independent-sample scale,
		// then map it back to the denser rolling order statistics. This avoids
		// claiming 96 independent daily tail observations per day.
		alpha := normalLowerTailProbability(c.DownsideZScore)
		effectiveIndex := math.Floor(alpha * math.Max(0, estimate.EffectiveSamples-1))
		index := int(math.Floor(effectiveIndex * overlapFactor))
		if index >= len(sorted) {
			index = len(sorted) - 1
		}
		estimate.EmpiricalDownsideLoss = math.Max(0, -sorted[index])
	}
	estimate.Sufficient = estimate.EffectiveSamples >= float64(c.MinimumSamples) && estimate.Variance > 0
	if estimate.Sufficient {
		weight := estimate.EffectiveSamples / (estimate.EffectiveSamples + c.DriftPriorSamples)
		estimate.ShrunkMean = estimate.Mean * weight
	} else {
		estimate.UsedFallback = true
		estimate.ShrunkMean = 0
		fallbackStdDev := math.Max(0, fallbackVolatilityBpsPerSqrtSec) / 10_000 * math.Sqrt(horizon.Seconds())
		if fallbackStdDev > estimate.StandardDeviation {
			estimate.StandardDeviation = fallbackStdDev
			estimate.Variance = fallbackStdDev * fallbackStdDev
		}
	}
	estimate.GaussianDownsideLoss = math.Max(0, c.DownsideZScore*estimate.StandardDeviation-estimate.ShrunkMean)
	estimate.DownsideLoss = math.Max(estimate.EmpiricalDownsideLoss, estimate.GaussianDownsideLoss)
	return estimate
}

// normalLowerTailProbability is used only to choose an empirical order
// statistic corresponding to the configured Gaussian z-score.
func normalLowerTailProbability(z float64) float64 {
	if z <= 0 {
		return 0.5
	}
	return 0.5 * math.Erfc(z/math.Sqrt2)
}

// MacroInventoryState persists the running marked-wealth peak across restarts.
// Deposits naturally establish a new peak. Operators should reset persisted
// state after a deliberate withdrawal because an account withdrawal is not a
// trading drawdown.
type MacroInventoryState struct {
	WealthPeakJPY              float64       `json:"wealthPeakJPY"`
	LastWealthJPY              float64       `json:"lastWealthJPY"`
	LastUpdatedAt              time.Time     `json:"lastUpdatedAt,omitempty"`
	RegimeTargetRatio          float64       `json:"regimeTargetRatio,omitempty"`
	RegimeDirection            int           `json:"regimeDirection,omitempty"`
	RegimeActivatedAt          time.Time     `json:"regimeActivatedAt,omitempty"`
	RegimeChangeAt             time.Time     `json:"regimeChangeAt,omitempty"`
	RegimeForecastHorizon      time.Duration `json:"regimeForecastHorizon,omitempty"`
	RegimeProbability          float64       `json:"regimeProbability,omitempty"`
	RegimeNetEdgeBps           float64       `json:"regimeNetEdgeBps,omitempty"`
	LastActiveExecutionAt      time.Time     `json:"lastActiveExecutionAt,omitempty"`
	LastActiveExecutionBarAt   time.Time     `json:"lastActiveExecutionBarAt,omitempty"`
	NoTradeFilteredAimRatio    float64       `json:"noTradeFilteredAimRatio,omitempty"`
	NoTradeAimVariance         float64       `json:"noTradeAimVariance,omitempty"`
	NoTradeAimUpdatedAt        time.Time     `json:"noTradeAimUpdatedAt,omitempty"`
	NoTradeAimClosedBarAt      time.Time     `json:"noTradeAimClosedBarAt,omitempty"`
	NoTradeTrendDirection      int           `json:"noTradeTrendDirection,omitempty"`
	NoTradeTrendProbability    float64       `json:"noTradeTrendProbability,omitempty"`
	NoTradeContinuationMixture bool          `json:"noTradeContinuationMixture,omitempty"`
	NoTradeDownsideRiskControl bool          `json:"noTradeDownsideRiskControl,omitempty"`
}

func (s *MacroInventoryState) ObserveWealth(now time.Time, wealthJPY float64) {
	if s == nil || wealthJPY <= 0 {
		return
	}
	s.LastWealthJPY = wealthJPY
	s.LastUpdatedAt = now
	if wealthJPY > s.WealthPeakJPY {
		s.WealthPeakJPY = wealthJPY
	}
}

type MacroInventoryDecision struct {
	Enabled                bool
	Healthy                bool
	Reason                 string
	WealthJPY              float64
	WealthPeakJPY          float64
	DrawdownRatio          float64
	CurrentRiskyWeight     float64
	PriorTargetRatio       float64
	UtilityTargetRatio     float64
	UtilityWeightSum       float64
	UtilityHorizons        int
	TargetRatio            float64
	CapitalFloorRatio      float64
	CapitalCapRatio        float64
	CarryFloorRatio        float64
	CarryCapRatio          float64
	DrawdownCapRatio       float64
	LimitingHorizon        time.Duration
	ReturnMean             float64
	ReturnShrunkMean       float64
	ReturnVariance         float64
	ReturnStdDev           float64
	DownsideLoss           float64
	Samples                int
	RawSamples             int
	EffectiveSamples       float64
	LatestReturn           float64
	FallbackVarianceWeight float64
	UsedFallback           bool
	NoTrade                NoTradeInventoryDecision
	StateChanged           bool
}

type MacroInventoryInput struct {
	Now                             time.Time
	WealthJPY                       float64
	WealthPeakJPY                   float64
	RiskyNotionalJPY                float64
	PriorTargetRatio                float64
	PolicyMinRatio                  float64
	PolicyMaxRatio                  float64
	FallbackVolatilityBpsPerSqrtSec float64
	CrossingQVRatePerSecond         float64
	FastVarianceRisk                SideHARVarianceRiskDecision
	CrossingSnapshot                ModelSnapshot
	ExecutableCrossingSnapshot      ModelSnapshot
	BarrierWidth                    float64
	BuyVolatilityBpsPerSqrtSec      float64
	SellVolatilityBpsPerSqrtSec     float64
	OneWayCostBps                   float64
	ConfidenceZScore                float64
	MinimumExecutableNotionalJPY    float64
	State                           *MacroInventoryState
	LatestClosedBarAt               time.Time
}

func (c MacroInventoryConfig) Decide(model *MacroInventoryModel, in MacroInventoryInput) MacroInventoryDecision {
	c.setDefaults()
	minimum := math.Max(0, math.Min(1, in.PolicyMinRatio))
	maximum := math.Max(minimum, math.Min(1, in.PolicyMaxRatio))
	prior := math.Max(minimum, math.Min(maximum, in.PriorTargetRatio))
	d := MacroInventoryDecision{
		Enabled: c.Enabled, Reason: "disabled", WealthJPY: in.WealthJPY,
		WealthPeakJPY: in.WealthPeakJPY, PriorTargetRatio: prior,
		UtilityTargetRatio: prior, TargetRatio: prior,
		CapitalFloorRatio: minimum, CapitalCapRatio: maximum,
		CarryFloorRatio: minimum, CarryCapRatio: maximum, DrawdownCapRatio: maximum,
	}
	if !c.Enabled || in.WealthJPY <= 0 {
		return d
	}
	if d.WealthPeakJPY < in.WealthJPY {
		d.WealthPeakJPY = in.WealthJPY
	}
	if d.WealthPeakJPY > 0 {
		d.DrawdownRatio = math.Max(0, 1-in.WealthJPY/d.WealthPeakJPY)
	}
	d.CurrentRiskyWeight = math.Max(0, in.RiskyNotionalJPY/in.WealthJPY)
	d.Reason = "macro return distribution unavailable"
	found := false
	allSufficient := true
	utilityWeightedSum := 0.0
	utilityWeightSum := 0.0
	fallbackUtility := prior
	fallbackUtilityFound := false
	for _, horizon := range c.horizons() {
		estimate := model.Estimate(in.Now, horizon, in.FallbackVolatilityBpsPerSqrtSec, c)
		if estimate.DownsideLoss <= 0 || c.RiskAversion*estimate.Variance+c.PriorStrength <= 0 {
			allSufficient = false
			continue
		}
		found = true
		allSufficient = allSufficient && estimate.Sufficient
		utility := (estimate.ShrunkMean + c.PriorStrength*prior) /
			(c.RiskAversion*estimate.Variance + c.PriorStrength)
		utility = math.Max(minimum, math.Min(maximum, utility))
		// The legacy controller aggregates per-horizon utility targets. The
		// no-trade controller skips this branch: one signed crossing posterior
		// supplies its QV-time aim, while these horizons retain only their strict
		// carrying-loss and drawdown constraints.
		if !c.NoTradeRegion.Enabled && estimate.Sufficient {
			weight := macroUtilityReliabilityEffective(estimate.EffectiveSamples, c.DriftPriorSamples)
			utilityWeightedSum += weight * utility
			utilityWeightSum += weight
			d.UtilityHorizons++
		} else if !c.NoTradeRegion.Enabled && (!fallbackUtilityFound || utility < fallbackUtility) {
			// If no horizon is statistically sufficient, preserve the former
			// conservative zero-drift fallback rather than inventing confidence.
			fallbackUtility = utility
			fallbackUtilityFound = true
		}
		// Charge the carrying-loss budget to active deviation from the strategic
		// allocation. Charging the entire core position would turn a 1% active
		// risk budget and a 5-8% tail move into a permanent 12-20% ETH cap.
		activeHeadroom := c.CarryRiskBudgetRatio / estimate.DownsideLoss
		carryFloor := math.Max(minimum, prior-activeHeadroom)
		carryCap := math.Min(maximum, prior+activeHeadroom)
		floor := (1 - c.MaxWealthDrawdownRatio) * d.WealthPeakJPY
		cushionRatio := math.Max(0, (in.WealthJPY-floor)/in.WealthJPY)
		drawdownCap := math.Min(maximum, cushionRatio/estimate.DownsideLoss)
		capRatio := math.Max(0, math.Min(carryCap, drawdownCap))
		floorRatio := math.Min(capRatio, carryFloor)
		if d.LimitingHorizon == 0 || capRatio < d.CapitalCapRatio {
			d.LimitingHorizon = horizon
			d.ReturnMean = estimate.Mean
			d.ReturnShrunkMean = estimate.ShrunkMean
			d.ReturnVariance = estimate.Variance
			d.RawSamples = estimate.RawSamples
			d.EffectiveSamples = estimate.EffectiveSamples
			d.LatestReturn = estimate.LatestReturn
			d.ReturnStdDev = estimate.StandardDeviation
			d.DownsideLoss = estimate.DownsideLoss
			d.Samples = estimate.Samples
			d.UsedFallback = estimate.UsedFallback
			d.FallbackVarianceWeight = estimate.FallbackVarianceWeight
		}
		d.CarryCapRatio = math.Min(d.CarryCapRatio, carryCap)
		d.CarryFloorRatio = math.Max(d.CarryFloorRatio, carryFloor)
		d.DrawdownCapRatio = math.Min(d.DrawdownCapRatio, drawdownCap)
		d.CapitalFloorRatio = math.Max(d.CapitalFloorRatio, floorRatio)
		d.CapitalCapRatio = math.Min(d.CapitalCapRatio, capRatio)
	}
	if c.NoTradeRegion.Enabled {
		policyMinimum := math.Max(minimum, d.CapitalFloorRatio)
		policyMaximum := math.Min(maximum, d.CapitalCapRatio)
		if policyMaximum < policyMinimum {
			policyMinimum = policyMaximum
		}
		estimateTrendState := c.NoTradeRegion.TrendExcursionEnabled ||
			c.NoTradeRegion.ContinuationEnabled ||
			c.NoTradeRegion.ContinuationMixtureEnabled
		trend := model.EstimateTrendExcursion(
			in.Now, c, estimateTrendState, 2*in.OneWayCostBps)
		continuation := trend.Continuation
		if !c.NoTradeRegion.ContinuationEnabled && !c.NoTradeRegion.ContinuationMixtureEnabled {
			continuation = TrendContinuationDecision{}
		}
		if trend.Healthy && c.NoTradeRegion.TrendExcursionEnabled {
			confidence := in.ConfidenceZScore
			if confidence <= 0 {
				confidence = c.DownsideZScore
			}
			structural := c.DecideReversal(model, MacroReversalInput{
				Now: in.Now, BaselineTargetRatio: prior,
				CurrentRiskyWeight: d.CurrentRiskyWeight,
				PolicyMinRatio:     policyMinimum, PolicyMaxRatio: policyMaximum,
				RoundTripCostBps: 2 * in.OneWayCostBps,
				ConfidenceZScore: confidence, RiskAversion: c.RiskAversion,
				FallbackVolatilityBpsPerSqrtSec: in.FallbackVolatilityBpsPerSqrtSec,
			})
			trend = ConditionTrendExcursionOnReversal(trend, structural, confidence)
		}
		if !c.NoTradeRegion.TrendExcursionEnabled {
			trend = TrendExcursionDecision{Reason: "long-window trend target disabled"}
		}
		noTradeInput := NoTradeInventoryInput{
			CurrentRiskyWeight:       d.CurrentRiskyWeight,
			PriorTargetRatio:         prior,
			PolicyMinRatio:           policyMinimum,
			PolicyMaxRatio:           policyMaximum,
			CrossingUp:               in.CrossingSnapshot.Up,
			CrossingDown:             in.CrossingSnapshot.Down,
			CrossingQVRatePerSecond:  in.CrossingQVRatePerSecond,
			CrossingHealth:           in.CrossingSnapshot.Health,
			ExecutableCrossingUp:     in.ExecutableCrossingSnapshot.Up,
			ExecutableCrossingDown:   in.ExecutableCrossingSnapshot.Down,
			ExecutableCrossingHealth: in.ExecutableCrossingSnapshot.Health,
			ExecutableObserved:       in.ExecutableCrossingSnapshot.Observed,
			ExecutableCrossingQVRatePerSecond: math.Pow(
				in.ExecutableCrossingSnapshot.GammaCaptureVolatility, 2),
			FastRiskVarianceRatePerSecond: in.FastVarianceRisk.VarianceRatePerSecond,
			FastRiskBaselineRatePerSecond: in.FastVarianceRisk.BaselineRatePerSecond,
			FastRiskHorizon:               in.FastVarianceRisk.Horizon,
			FastRiskHealthy:               in.FastVarianceRisk.Healthy,
			FastRiskElevated:              in.FastVarianceRisk.ElevatedRisk,
			BarrierWidth:                  in.BarrierWidth,
			Observed:                      in.CrossingSnapshot.Observed,
			DriftPriorSamples:             c.DriftPriorSamples,
			BuyVolatilityBpsPerSqrtSec:    in.BuyVolatilityBpsPerSqrtSec,
			SellVolatilityBpsPerSqrtSec:   in.SellVolatilityBpsPerSqrtSec,
			RiskAversion:                  c.RiskAversion,
			PriorStrength:                 c.PriorStrength,
			OneWayCostBps:                 in.OneWayCostBps,
			PairEquityJPY:                 in.WealthJPY,
			MinimumExecutableNotionalJPY:  in.MinimumExecutableNotionalJPY,
			TrendExcursion:                trend,
			TrendContinuation:             continuation,
		}
		d.NoTrade = EvaluateNoTradeInventory(c.NoTradeRegion, noTradeInput)
		if in.State != nil {
			d.NoTrade, d.StateChanged = in.State.ApplyNoTradeState(
				in.Now, in.LatestClosedBarAt, noTradeInput, d.NoTrade)
		}
		d.UtilityTargetRatio = d.NoTrade.AimRatio
		d.TargetRatio = d.NoTrade.ExecutionTargetRatio
		d.Healthy = d.NoTrade.Healthy && found && allSufficient
		if d.NoTrade.Reason != "" {
			d.Reason = "QV-time no-trade region: " + d.NoTrade.Reason
		}
		return d
	}
	if !found {
		return d
	}
	if utilityWeightSum > 0 {
		d.UtilityTargetRatio = math.Max(minimum, math.Min(maximum, utilityWeightedSum/utilityWeightSum))
		d.UtilityWeightSum = utilityWeightSum
	} else if fallbackUtilityFound {
		d.UtilityTargetRatio = fallbackUtility
	}
	d.CapitalFloorRatio = math.Min(d.CapitalFloorRatio, d.CapitalCapRatio)
	d.TargetRatio = math.Max(d.CapitalFloorRatio, math.Min(d.UtilityTargetRatio, d.CapitalCapRatio))
	d.Healthy = allSufficient
	if allSufficient {
		d.Reason = "rolling overlap-corrected macro returns"
	} else {
		d.Reason = "zero-drift executable-volatility fallback"
	}
	return d
}

func macroUtilityReliabilityEffective(samples, driftPriorSamples float64) float64 {
	if samples <= 0 {
		return 0
	}
	if driftPriorSamples <= 0 {
		return 1
	}
	return samples / (samples + driftPriorSamples)
}

func macroUtilityReliability(samples int, driftPriorSamples float64) float64 {
	if samples <= 0 {
		return 0
	}
	if driftPriorSamples <= 0 {
		return 1
	}
	n := float64(samples)
	return n / (n + driftPriorSamples)
}
