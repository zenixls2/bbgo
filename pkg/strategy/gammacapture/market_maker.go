package gammacapture

import (
	"math"
	"sort"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

type ProbabilityCenteredQuantityConfig struct {
	Enabled    bool `json:"enabled" yaml:"enabled"`
	ShadowOnly bool `json:"shadowOnly" yaml:"shadowOnly"`
}

// JointDistanceQuantityConfig enables the final one-order-per-side optimizer.
// CandidateCount is a numerical resolution, not a risk weight: the optimizer
// evaluates the ordinary outward price ladder and symmetric passive inward
// BUY/SELL ladders. All candidates are selected by the same conditional posterior
// terminal-wealth objective and inventory chance constraint.
type JointDistanceQuantityConfig struct {
	Enabled        bool `json:"enabled" yaml:"enabled"`
	ShadowOnly     bool `json:"shadowOnly" yaml:"shadowOnly"`
	CandidateCount int  `json:"candidateCount" yaml:"candidateCount"`
	// PreserveTwoSidedQuotes requests an executable minimum bid and ask when the
	// terminal-path optimizer rejects the ordinary pair. It is only a conditional
	// continuity floor, not a guarantee or a positive-PnL override: the BBO
	// crossing edge, balances, hard inventory headroom, and exchange minimums
	// must still pass. The corrective side may be larger than one venue cell only
	// through the cost-adjusted target-restoring Bellman quantity.
	PreserveTwoSidedQuotes bool `json:"preserveTwoSidedQuotes" yaml:"preserveTwoSidedQuotes"`
	// TwoStageContinuation values the option created by a first maker fill.
	// The opening order must still touch within the selected Fast horizon H;
	// only the opposite completion order receives a fresh H after that touch.
	// Completed empirical BBO paths, rather than a Poisson arrival model, price
	// this continuation. The switch is explicit so research replay can compare
	// the old and new estimands on identical market data.
	TwoStageContinuation bool `json:"twoStageContinuation" yaml:"twoStageContinuation"`
	// CrossHorizonContinuation separates entry timing from cycle completion.
	// The opening quote must still touch inside its selected Fast horizon, while
	// an unmatched fill may use the longest configured Fast horizon to complete
	// the opposite leg. This is derived from the live model's own horizon set,
	// rather than introducing a symbol-fitted holding period.
	CrossHorizonContinuation bool `json:"crossHorizonContinuation" yaml:"crossHorizonContinuation"`
	// RobustAdmission requires both a complementary cycle and each standalone
	// fallback to survive the configured posterior lower confidence bound. It
	// prevents a low-confidence point estimate from authorizing a leg whose
	// modeled completion may fail on the realized path.
	RobustAdmission bool `json:"robustAdmission" yaml:"robustAdmission"`
	// PathUtilityHorizonSelection makes the Fast window selector maximize the
	// best inventory-aware action in {NO_ORDER, BUY, SELL, BOTH} under the same
	// confidence-adjusted terminal-wealth objective used by final price and
	// quantity selection. This removes both the two-objective mismatch and the
	// previous BUY-only horizon option asymmetry.
	PathUtilityHorizonSelection bool `json:"pathUtilityHorizonSelection" yaml:"pathUtilityHorizonSelection"`
	// JointHorizonSelection makes the final optimizer choose the statistical
	// horizon together with executable distance and quantity.  Each feasible
	// action is compared on posterior terminal-wealth utility per unit time;
	// sparse horizons are continuously shrunk toward the zero-utility no-order
	// action instead of being admitted or rejected by a health label.
	JointHorizonSelection bool `json:"jointHorizonSelection" yaml:"jointHorizonSelection"`
	// PairedDistanceImprovement prevents an outward ladder level from winning
	// only because it was the largest of many noisy point estimates. The level
	// must improve balanced Fast-cycle payoff over the ordinary quote on the
	// same completed paths, under a simultaneous confidence bound.
	PairedDistanceImprovement bool `json:"pairedDistanceImprovement" yaml:"pairedDistanceImprovement"`
	// PathMaturityMaxRelativeHalfWidth is the largest allowed posterior
	// confidence half-width relative to the larger of the fee/edge scale and
	// the observed terminal-payoff mean.  It is a precision condition, not a
	// fixed raw-sample gate: overlapping path windows are counted through their
	// effective sample size and their weighted variance.
	PathMaturityMaxRelativeHalfWidth float64 `json:"pathMaturityMaxRelativeHalfWidth" yaml:"pathMaturityMaxRelativeHalfWidth"`
	// AdaptivePathDecay estimates the path-persistence decay factor from
	// matured same-symbol executable-BBO paths.  When absent it defaults on;
	// the old sqrt(horizon*lookback) scale is retained only as a cold-start
	// prior until enough lagged path observations exist.
	AdaptivePathDecay bool `json:"adaptivePathDecay" yaml:"adaptivePathDecay"`
	// LegacyStackedTargetContinuation is an in-memory research control for
	// paired replay of the retired behavior. It is intentionally excluded from
	// JSON/YAML so production configuration cannot re-enable target stacking.
	LegacyStackedTargetContinuation bool `json:"-" yaml:"-"`
}

// PostFillUtilityConfig controls causal opposite-side repricing immediately
// after an execution. The previous fill price is model state and diagnostics,
// not a hard boundary: continuation or inventory risk may justify paying back
// part of a completed edge.
type PostFillUtilityConfig struct {
	Enabled          bool    `json:"enabled" yaml:"enabled"`
	MinimumSamples   int     `json:"minimumSamples" yaml:"minimumSamples"`
	ConfidenceZScore float64 `json:"confidenceZScore" yaml:"confidenceZScore"`
	// CandidateCount is numerical search resolution only. It is independent of
	// inventory sizing and must not reuse a portfolio-risk parameter.
	CandidateCount int `json:"candidateCount" yaml:"candidateCount"`
}

// PrivateOrderFillLedgerConfig controls the append-only private execution
// ledger. It is observation-only: ledger errors are surfaced and logged, but
// the writer never changes a quote, quantity, or gate decision.
type PrivateOrderFillLedgerConfig struct {
	Enabled           bool   `json:"enabled" yaml:"enabled"`
	Path              string `json:"path" yaml:"path"`
	SyncEachEvent     bool   `json:"syncEachEvent" yaml:"syncEachEvent"`
	ProductionVersion string `json:"productionVersion,omitempty" yaml:"productionVersion,omitempty"`
}

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
	StartupCancelStaleOrders bool `json:"startupCancelStaleOrders" yaml:"startupCancelStaleOrders"`
	// AccountSyncInterval periodically refreshes the authenticated account and
	// reconciles external balance/position changes before quoting.
	AccountSyncInterval types.Duration `json:"accountSyncInterval" yaml:"accountSyncInterval"`
	// PrivateOrderFillLedger records private order updates and fills with the
	// contemporaneous executable BBO. It is required for calibrated queue and
	// adverse-selection studies; it does not participate in production policy.
	PrivateOrderFillLedger PrivateOrderFillLedgerConfig `json:"privateOrderFillLedger" yaml:"privateOrderFillLedger"`
	// PrivateFillCalibration consumes only private fills/order lifetimes from the
	// ledger and live callbacks. It remains a non-blocking, warming estimator
	// until its own causal labels reach the configured effective sample size.
	PrivateFillCalibration PrivateFillCalibrationConfig `json:"privateFillCalibration" yaml:"privateFillCalibration"`
	MakerFeeBps            float64                      `json:"makerFeeBps" yaml:"makerFeeBps"`
	TakerFeeBps            float64                      `json:"takerFeeBps" yaml:"takerFeeBps"`
	MinimumNetEdgeBps      float64                      `json:"minimumNetEdgeBps" yaml:"minimumNetEdgeBps"`
	AdverseSelectionBps    float64                      `json:"adverseSelectionBps" yaml:"adverseSelectionBps"`
	MinimumHalfSpreadBps   float64                      `json:"minimumHalfSpreadBps" yaml:"minimumHalfSpreadBps"`
	MaximumHalfSpreadBps   float64                      `json:"maximumHalfSpreadBps" yaml:"maximumHalfSpreadBps"`
	VolatilityMultiplier   float64                      `json:"volatilityMultiplier" yaml:"volatilityMultiplier"`
	InventoryTarget        float64                      `json:"inventoryTarget" yaml:"inventoryTarget"`
	InventoryLimit         float64                      `json:"inventoryLimit" yaml:"inventoryLimit"`
	AutoInventoryLimit     bool                         `json:"autoInventoryLimit" yaml:"autoInventoryLimit"`
	InventoryRiskBudgetJPY float64                      `json:"inventoryRiskBudgetJPY" yaml:"inventoryRiskBudgetJPY"`
	// InventoryRiskBudgetRatio scales the adverse-move risk budget with the
	// current quote-equivalent equity of the symbol. InventoryRiskBudgetJPY
	// remains the absolute floor for small accounts or unavailable balances.
	InventoryRiskBudgetRatio float64 `json:"inventoryRiskBudgetRatio" yaml:"inventoryRiskBudgetRatio"`
	InventoryRiskZScore      float64 `json:"inventoryRiskZScore" yaml:"inventoryRiskZScore"`
	// FastRiskAversion belongs to the executable Fast terminal-wealth model.
	// It is deliberately independent from MacroInventory.RiskAversion so that
	// disabling or retuning the long-horizon controller cannot silently change
	// Fast quote sizing, target switching, or IOC decisions.
	FastRiskAversion         float64 `json:"fastRiskAversion" yaml:"fastRiskAversion"`
	PosteriorInventoryTarget bool    `json:"posteriorInventoryTarget" yaml:"posteriorInventoryTarget"`
	// CausalKlinePivot is a delayed-label 3-minute pivot learner. It may affect
	// only the inventory target through a bounded overlay; it never gates,
	// prices, sizes, cancels, or blocks orders. The production ETHJPY profile
	// disables it while the causal pivot-regime CE owner is active.
	CausalKlinePivot CausalKlinePivotConfig `json:"causalKlinePivot" yaml:"causalKlinePivot"`
	// DynamicInventoryAim is retained for YAML/checkpoint compatibility and
	// isolated research. Its causal pivot-regime CE sub-owner is evaluated
	// independently when explicitly enabled; the legacy actuator remains off.
	DynamicInventoryAim DynamicInventoryAimConfig `json:"dynamicInventoryAim" yaml:"dynamicInventoryAim"`
	// FastTargetExecution consumes evidence from the active inventory-target
	// owner; it is not gated by PosteriorInventoryTarget.
	FastTargetExecution       FastTargetExecutionConfig       `json:"fastTargetExecution" yaml:"fastTargetExecution"`
	FastTargetSwitching       FastTargetSwitchingConfig       `json:"fastTargetSwitching" yaml:"fastTargetSwitching"`
	FastDrift                 FastDriftConfig                 `json:"fastDrift" yaml:"fastDrift"`
	AsymmetricOscillationRisk AsymmetricOscillationRiskConfig `json:"asymmetricOscillationRisk" yaml:"asymmetricOscillationRisk"`
	BOCPD45                   BOCPD45Config                   `json:"bocpd45" yaml:"bocpd45"`
	// MultiscaleRegime is a one-minute, jump/noise-robust transition posterior.
	// It is a direction fallback only: a ready calibrated BOCPD45 posterior
	// remains authoritative, so the same change-point evidence is not counted
	// twice in price, quantity, and admission gates.
	MultiscaleRegime MultiscaleRegimeConfig `json:"multiscaleRegime" yaml:"multiscaleRegime"`
	// VolumeProfile is used only as a causal conditioning feature of the
	// unified Fast terminal-payoff model. It is intentionally not an
	// independent quote, quantity, or hard-gate controller.
	VolumeProfile        VolumeProfileConfig `json:"volumeProfile" yaml:"volumeProfile"`
	InventoryTargetRatio float64             `json:"inventoryTargetRatio" yaml:"inventoryTargetRatio"`
	// InventoryCapitalMinRatio, InventoryCapitalTargetRatio, and
	// InventoryCapitalMaxRatio express a target-centered inventory band as
	// fractions of pair equity. The legacy InventoryTargetRatio remains the
	// fallback ratio of target to cap when pair equity is unavailable.
	InventoryCapitalMinRatio    float64                           `json:"inventoryCapitalMinRatio" yaml:"inventoryCapitalMinRatio"`
	InventoryCapitalTargetRatio float64                           `json:"inventoryCapitalTargetRatio" yaml:"inventoryCapitalTargetRatio"`
	InventoryCapitalMaxRatio    float64                           `json:"inventoryCapitalMaxRatio" yaml:"inventoryCapitalMaxRatio"`
	MacroInventory              MacroInventoryConfig              `json:"macroInventory" yaml:"macroInventory"`
	ProbabilityCenteredQuantity ProbabilityCenteredQuantityConfig `json:"probabilityCenteredQuantity" yaml:"probabilityCenteredQuantity"`
	JointDistanceQuantity       JointDistanceQuantityConfig       `json:"jointDistanceQuantity" yaml:"jointDistanceQuantity"`
	// RelativeHoldRisk is a single scalar utility adjustment inside the Fast
	// joint distance/quantity optimizer. It remains disabled unless a same-
	// symbol matured replay explicitly enables it; it is never a side gate.
	RelativeHoldRisk     RelativeHoldRiskConfig     `json:"relativeHoldRisk" yaml:"relativeHoldRisk"`
	ConditionalExecution ConditionalExecutionConfig `json:"conditionalExecution" yaml:"conditionalExecution"`
	PostFillUtility      PostFillUtilityConfig      `json:"postFillUtility" yaml:"postFillUtility"`
	// QuoteLifecycleAction is a Bellman KEEP/REPLACE/CANCEL review at the
	// modeled window boundary. It is disabled by default until a paired-cycle
	// replay has sufficient action diversity and confidence.
	QuoteLifecycleAction QuoteLifecycleActionConfig `json:"quoteLifecycleAction" yaml:"quoteLifecycleAction"`
	InventorySkewBps     float64                    `json:"inventorySkewBps" yaml:"inventorySkewBps"`
	QuoteNotional        float64                    `json:"quoteNotionalJPY" yaml:"quoteNotionalJPY"`
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
	// OnlineArrival is retained only so older YAML/checkpoint files continue
	// to decode. The runtime no longer trains or consults this estimator.
	OnlineArrival     OnlineArrivalConfig     `json:"onlineArrival" yaml:"onlineArrival"`
	HorizonTouchModel HorizonTouchModelConfig `json:"horizonTouchModel" yaml:"horizonTouchModel"`
	FastWindow        types.Duration          `json:"fastWindow" yaml:"fastWindow"`
	// FastWindows enables parallel live crossing/evidence models. When present,
	// the same set is also used as the selectable trading horizons so model
	// health, arrival statistics, and order lifetime refer to coherent windows.
	FastWindows []types.Duration `json:"fastWindows" yaml:"fastWindows"`
	// FastEvidenceWindow is a separate raw-data coverage window. It may be
	// longer than FastWindow because sparse JPY markets need more time to
	// accumulate public trades without slowing the short-horizon signal model.
	FastEvidenceWindow types.Duration `json:"fastEvidenceWindow" yaml:"fastEvidenceWindow"`
	// FastEvidenceMinTrades and FastEvidenceMinBBOUpdates are observational
	// coverage thresholds for the raw trade/BBO evidence layer. They do not
	// override crossing-model health or prove fee-adjusted profitability.
	FastEvidenceMinTrades     int                      `json:"fastEvidenceMinTrades" yaml:"fastEvidenceMinTrades"`
	FastEvidenceMinBBOUpdates int                      `json:"fastEvidenceMinBBOUpdates" yaml:"fastEvidenceMinBBOUpdates"`
	OFIVolumeAgreement        OFIVolumeAgreementConfig `json:"ofiVolumeAgreement" yaml:"ofiVolumeAgreement"`
	// NormalFlowPressure is a bounded fallback for ordinary signed public flow
	// when the high-volume VolumeBalance shock path is inactive.
	NormalFlowPressure NormalFlowPressureConfig `json:"normalFlowPressure" yaml:"normalFlowPressure"`
	AcquisitionQuote   AcquisitionQuoteConfig   `json:"acquisitionQuote" yaml:"acquisitionQuote"`
	// The following fields remain decode-compatible with older YAML files. The
	// active price policy derives soft effects once through joint evidence/hazard
	// pressure; final quantity uses the probability-centered Macro projection.
	DirectionSkewBps             float64                `json:"directionSkewBps" yaml:"directionSkewBps"`
	ImbalanceSkewBps             float64                `json:"imbalanceSkewBps" yaml:"imbalanceSkewBps"`
	SideAllocationSensitivity    float64                `json:"sideAllocationSensitivity" yaml:"sideAllocationSensitivity"`
	SideAllocationFillRateWeight float64                `json:"sideAllocationFillRateWeight" yaml:"sideAllocationFillRateWeight"`
	SideAllocationSmoothing      float64                `json:"sideAllocationSmoothing" yaml:"sideAllocationSmoothing"`
	SideDistanceSensitivity      float64                `json:"sideDistanceSensitivity" yaml:"sideDistanceSensitivity"`
	InventoryReset               InventoryResetConfig   `json:"inventoryReset" yaml:"inventoryReset"`
	AcquisitionReset             AcquisitionResetConfig `json:"acquisitionReset" yaml:"acquisitionReset"`
	EarlyBump                    EarlyBumpConfig        `json:"earlyBump" yaml:"earlyBump"`
}

// fastRiskAversionOrDefault resolves a risk-aversion value for the executable
// Fast quote model.  Fast callers normally pass the value selected by the
// live strategy; the config fallback exists for focused research helpers that
// call an optimizer directly.  It must never fall back to MacroInventory,
// because Macro is an optional long-horizon controller and changing it must
// not silently retune Fast pricing or quantity.
func fastRiskAversionOrDefault(config MarketMakerConfig, riskAversion float64) float64 {
	if riskAversion > 0 && !math.IsNaN(riskAversion) && !math.IsInf(riskAversion, 0) {
		return riskAversion
	}
	if config.FastRiskAversion > 0 && !math.IsNaN(config.FastRiskAversion) && !math.IsInf(config.FastRiskAversion, 0) {
		return config.FastRiskAversion
	}
	return 1
}

// preserveActiveTwoSidedQuotesAfterFastRejection is a continuity floor for an
// already resting bilateral maker pair.  A terminal-path rejection should not
// create an empty book merely because a new candidate was rejected, but the
// floor is deliberately conditional: any event that can make the old pair
// unsafe (crossing, expiry, adverse/material movement, inventory headroom,
// repricing, lifecycle, or fill-rebalance work) still cancels it.
func preserveActiveTwoSidedQuotesAfterFastRejection(
	config JointDistanceQuantityConfig,
	activeBid, activeAsk bool,
	fillRebalancePending bool,
	quoteCrossed, adverseMove, materialMove, materialImbalance bool,
	windowExpired, inventoryHeadroomExceeded, fastEdgeLeaseExpired bool,
	statisticalRealignment, oneSidedTargetRealignment bool,
	macroTargetRealignment, fastTargetRealignment, reservationRiskRealignment bool,
	earlyBumpRefresh, lifecycleReplace bool,
) bool {
	return config.PreserveTwoSidedQuotes && activeBid && activeAsk &&
		!fillRebalancePending && !quoteCrossed && !adverseMove &&
		!materialMove && !materialImbalance && !windowExpired &&
		!inventoryHeadroomExceeded && !fastEdgeLeaseExpired &&
		!statisticalRealignment && !oneSidedTargetRealignment &&
		!macroTargetRealignment && !fastTargetRealignment &&
		!reservationRiskRealignment && !earlyBumpRefresh && !lifecycleReplace
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
	// DriftContinuationWeight shrinks the observed adverse move before it is
	// used as a future-return forecast. Zero is the martingale baseline selected
	// by the SOLJPY holdout; one would repeat the full observed move.
	DriftContinuationWeight float64 `json:"driftContinuationWeight" yaml:"driftContinuationWeight"`
	// A reset is an accelerated inventory exit, not an unbounded stop-loss. It
	// must remain profitable after the fee-adjusted position cost and must beat
	// the passive alternative by a non-trivial margin.
	MinimumRoundTripValueBps float64 `json:"minimumRoundTripValueBps" yaml:"minimumRoundTripValueBps"`
	MinimumImprovementBps    float64 `json:"minimumImprovementBps" yaml:"minimumImprovementBps"`
}

// AcquisitionResetConfig controls a statistically gated transition from a
// stale passive bid to a small IOC BUY. Unlike InventoryReset, it is an
// optional capital-deployment mechanism rather than an inventory safety exit.
type AcquisitionResetConfig struct {
	Enabled            bool `json:"enabled" yaml:"enabled"`
	ShadowStartEnabled bool `json:"shadowStartEnabled" yaml:"shadowStartEnabled"`
	// StartReturn1mMinBps and StartReturn5mMinBps are legacy fallback
	// thresholds for callers without live variance calibration.
	StartReturn1mMinBps float64 `json:"startReturn1mMinBps" yaml:"startReturn1mMinBps"`
	StartReturn5mMinBps float64 `json:"startReturn5mMinBps" yaml:"startReturn5mMinBps"`
	// StartReturnTailProbability converts live midpoint variance into causal
	// one- and five-minute return thresholds.
	StartReturnTailProbability      float64 `json:"startReturnTailProbability" yaml:"startReturnTailProbability"`
	StartMinimumTrades5m            int     `json:"startMinimumTrades5m" yaml:"startMinimumTrades5m"`
	StartMinimumBBO5m               int     `json:"startMinimumBBO5m" yaml:"startMinimumBBO5m"`
	StartMinimumVolatilitySamples5m int     `json:"startMinimumVolatilitySamples5m" yaml:"startMinimumVolatilitySamples5m"`
	// StartDrawdownTailProbability is converted to a live BPS limit from the
	// trailing five-minute midpoint variance via the Brownian reflection
	// principle. It is a probability policy, not a fixed price threshold.
	StartDrawdownTailProbability float64 `json:"startDrawdownTailProbability" yaml:"startDrawdownTailProbability"`
	// StartMaxDrawdown5mBps is a legacy optional hard cap. Zero disables it;
	// the live profile uses only the statistically calibrated dynamic limit.
	StartMaxDrawdown5mBps   float64        `json:"startMaxDrawdown5mBps" yaml:"startMaxDrawdown5mBps"`
	MinDeficitAge           types.Duration `json:"minDeficitAge" yaml:"minDeficitAge"`
	AdverseMoveBps          float64        `json:"adverseMoveBps" yaml:"adverseMoveBps"`
	MaxSlippageBps          float64        `json:"maxSlippageBps" yaml:"maxSlippageBps"`
	Cooldown                types.Duration `json:"cooldown" yaml:"cooldown"`
	MinSamples              int            `json:"minSamples" yaml:"minSamples"`
	ConfidenceZScore        float64        `json:"confidenceZScore" yaml:"confidenceZScore"`
	FillIntensityHaircut    float64        `json:"fillIntensityHaircut" yaml:"fillIntensityHaircut"`
	RiskZScore              float64        `json:"riskZScore" yaml:"riskZScore"`
	MinimumExpectedValueBps float64        `json:"minimumExpectedValueBps" yaml:"minimumExpectedValueBps"`
	MinimumImprovementBps   float64        `json:"minimumImprovementBps" yaml:"minimumImprovementBps"`
}

// AcquisitionQuoteConfig controls bounded one-sided momentum accommodation.
type AcquisitionQuoteConfig struct {
	Enabled               bool    `json:"enabled" yaml:"enabled"`
	ShadowOnly            bool    `json:"shadowOnly" yaml:"shadowOnly"`
	MaxDeltaBps           float64 `json:"maxDeltaBps" yaml:"maxDeltaBps"`
	MinDriftBps           float64 `json:"minDriftBps" yaml:"minDriftBps"`
	DriftConfidenceZScore float64 `json:"driftConfidenceZScore" yaml:"driftConfidenceZScore"`
	MinDirection          float64 `json:"minDirection" yaml:"minDirection"`
}

func (c *AcquisitionQuoteConfig) setDefaults() {
	if c.MaxDeltaBps <= 0 {
		c.MaxDeltaBps = 15
	}
	if c.MinDriftBps < 0 {
		c.MinDriftBps = 0
	}
	if c.DriftConfidenceZScore <= 0 {
		c.DriftConfidenceZScore = 1.645
	}
	if c.MinDirection <= 0 {
		c.MinDirection = 0.25
	}
	if c.MinDirection > 1 {
		c.MinDirection = 1
	}
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
	if c.FastRiskAversion <= 0 {
		c.FastRiskAversion = 1
	}
	c.CausalKlinePivot = c.CausalKlinePivot.withDefaults()
	if c.JointDistanceQuantity.CandidateCount <= 0 {
		c.JointDistanceQuantity.CandidateCount = 5
	}
	if c.JointDistanceQuantity.PathMaturityMaxRelativeHalfWidth <= 0 ||
		math.IsNaN(c.JointDistanceQuantity.PathMaturityMaxRelativeHalfWidth) ||
		math.IsInf(c.JointDistanceQuantity.PathMaturityMaxRelativeHalfWidth, 0) {
		// A one-sided 95% interval may be at most one economic scale wide on
		// either side before a path is considered identified.  This avoids the
		// old fixed-six gate while still rejecting a noisy 2-3 effective-sample
		// estimate whose sign/variance cannot support an action.
		c.JointDistanceQuantity.PathMaturityMaxRelativeHalfWidth = 1
	}
	if c.PostFillUtility.CandidateCount <= 0 {
		c.PostFillUtility.CandidateCount = 8
	}
	if c.InventoryTargetRatio <= 0 || c.InventoryTargetRatio >= 1 {
		c.InventoryTargetRatio = 0.5
	}
	if c.InventoryCapitalMaxRatio <= 0 || c.InventoryCapitalMaxRatio > 1 {
		c.InventoryCapitalMaxRatio = 0.50
	}
	if c.InventoryCapitalTargetRatio <= 0 || c.InventoryCapitalTargetRatio >= c.InventoryCapitalMaxRatio {
		c.InventoryCapitalTargetRatio = c.InventoryCapitalMaxRatio * c.InventoryTargetRatio
	}
	c.MacroInventory.setDefaults()
	c.BOCPD45.setDefaults()
	c.MultiscaleRegime = c.MultiscaleRegime.withDefaults()
	c.AcquisitionQuote.setDefaults()
	if c.InventoryCapitalMinRatio < 0 || c.InventoryCapitalMinRatio >= c.InventoryCapitalTargetRatio {
		c.InventoryCapitalMinRatio = math.Max(0, 2*c.InventoryCapitalTargetRatio-c.InventoryCapitalMaxRatio)
	}
	if c.RefreshInterval <= 0 {
		c.RefreshInterval = types.Duration(30 * time.Second)
	}
	if c.AccountSyncInterval <= 0 {
		c.AccountSyncInterval = types.Duration(time.Minute)
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
		c.HorizonUpdateInterval = types.Duration(5 * time.Minute)
	}
	if c.HorizonMinSamples <= 0 {
		c.HorizonMinSamples = 6
	}
	c.DynamicInventoryAim.setDefaults(c.HorizonMinSamples)
	if c.PostFillUtility.MinimumSamples <= 0 {
		c.PostFillUtility.MinimumSamples = c.HorizonMinSamples
	}
	if c.PostFillUtility.ConfidenceZScore <= 0 {
		c.PostFillUtility.ConfidenceZScore = c.InventoryRiskZScore
	}
	c.HorizonTouchModel.setDefaults()
	c.PrivateFillCalibration.setDefaults()
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
	c.EarlyBump.setDefaults()
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
// turnover. A Gamma-Poisson bootstrap rate of one crossing per lookback is
// applied independently to each missing side. This keeps the transition from
// cold-start to one-sided observations continuous. The result is data-derived;
// a fixed configured level count must not silently divide every live quote.
func (c MarketMakerConfig) EffectiveOrderLevels(horizon time.Duration, upCrossesPerHour, downCrossesPerHour float64) float64 {
	c.setDefaults()
	if horizon <= 0 {
		return 1
	}
	lookbackHours := time.Duration(c.HorizonLookback).Hours()
	priorRate := 0.0
	if lookbackHours > 0 {
		priorRate = 1 / lookbackHours
	}
	upRate := math.Max(priorRate, math.Max(0, upCrossesPerHour))
	downRate := math.Max(priorRate, math.Max(0, downCrossesPerHour))
	twoSidedRate := math.Min(upRate, downRate)
	expectedCrossings := twoSidedRate * horizon.Hours()
	return math.Max(1, expectedCrossings)
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
	InventoryMin    float64
	InventoryTarget float64
	InventoryMax    float64
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

// Deprecated: SideAllocationBias is retained for historical/offline comparisons.
// The active strategy uses the joint Quote policy instead.
// SideAllocationBias returns a bounded, continuous pressure score. Positive
// values mean the bid is the riskier side and should be reduced; negative
// values mean the ask should be reduced. Quantity allocation deliberately uses
// only inventory and side-specific fill intensity. Direction and book
// imbalance already alter quote prices; applying them again to quantity would
// multiply one noisy signal across two independent controls.
func (c MarketMakerConfig) SideAllocationBias(in SideQuoteAllocationInput) float64 {
	c.setDefaults()
	limit := in.InventoryLimit
	if in.Inventory >= in.InventoryTarget && in.InventoryMax > in.InventoryTarget {
		limit = in.InventoryMax - in.InventoryTarget
	} else if in.Inventory < in.InventoryTarget && in.InventoryMin < in.InventoryTarget {
		limit = in.InventoryTarget - in.InventoryMin
	}
	if limit <= 0 {
		limit = c.InventoryLimit
	}
	inventoryBias := 0.0
	if limit > 0 {
		inventoryBias = clampSideAllocation((in.Inventory - in.InventoryTarget) / limit)
	}
	buyRate := math.Max(0, in.BuyFillRate)
	sellRate := math.Max(0, in.SellFillRate)
	// log1p plus tanh makes a sparse-rate estimate useful without allowing one
	// noisy observation to dominate the inventory signal.
	rateBias := math.Tanh(math.Log1p(buyRate) - math.Log1p(sellRate))
	pressure := inventoryBias + c.SideAllocationFillRateWeight*rateBias
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

// Deprecated: SideQuoteDistanceBias is retained for historical/offline comparisons.
// The active strategy uses the joint Quote policy instead.
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

// MarketMakerHorizonPoint is a one-second executable BBO sample used by the
// horizon optimizer. Bid and Ask are retained separately because a passive
// buy can only be conservatively observed from the ask path, while a passive
// sell can only be conservatively observed from the bid path. Mid is retained
// as a mark/fair-price feature and for backwards-compatible trade-only warmup.
// Keeping one point per second avoids letting a burst of BBO updates dominate
// the crossing count.
const marketMakerHorizonGapThreshold = 2 * time.Minute

type MarketMakerHorizonPoint struct {
	At               time.Time
	Bid              float64
	Ask              float64
	Mid              float64
	BBOWeightedPrice float64
	BookImbalance    float64
	BookDepthReady   bool
	GapBefore        bool
	volumeProfiles   [maxVolumeProfileSnapshots]volumeProfileSnapshot
	volumeProfileN   uint8
}

func (p MarketMakerHorizonPoint) volumeProfileState(horizon time.Duration) VolumeProfileState {
	for index := 0; index < int(p.volumeProfileN) && index < len(p.volumeProfiles); index++ {
		if p.volumeProfiles[index].Horizon == horizon {
			return p.volumeProfiles[index].State
		}
	}
	return VolumeProfileState{}
}

func (p MarketMakerHorizonPoint) bidPrice() float64 {
	if p.Bid > 0 {
		return p.Bid
	}
	return p.Mid
}

func (p MarketMakerHorizonPoint) askPrice() float64 {
	if p.Ask > 0 {
		return p.Ask
	}
	return p.Mid
}

func (p MarketMakerHorizonPoint) midPrice() float64 {
	if p.Mid > 0 {
		return p.Mid
	}
	if p.Bid > 0 && p.Ask >= p.Bid {
		return (p.Bid + p.Ask) / 2
	}
	return 0
}

func (p MarketMakerHorizonPoint) bboWeightedPrice() float64 {
	if p.BBOWeightedPrice > 0 {
		return p.BBOWeightedPrice
	}
	return p.midPrice()
}

// bboDepthWeightedPrice is the top-of-book microprice. Opposite-side depth
// weights each executable price: more bid depth moves the estimate toward the
// ask and more ask depth moves it toward the bid. Equal or unavailable depth
// falls back to the ordinary BBO midpoint for legacy/trade-only callers.
func bboDepthWeightedPrice(bid, bidSize, ask, askSize float64) float64 {
	if bid <= 0 || ask < bid {
		return 0
	}
	if bidSize > 0 && askSize > 0 && bidSize+askSize > 0 {
		return (ask*bidSize + bid*askSize) / (bidSize + askSize)
	}
	return (bid + ask) / 2
}

func bboDepthImbalance(bidSize, askSize float64) (float64, bool) {
	if bidSize <= 0 || askSize <= 0 || bidSize+askSize <= 0 {
		return 0, false
	}
	return clampBookImbalance((bidSize - askSize) / (bidSize + askSize)), true
}

// MarketMakerSideVolatilityEstimate reports executable-price volatility in
// bps/sqrt(second). Buy uses ask returns and Sell uses bid returns.
type MarketMakerSideVolatilityEstimate struct {
	BuyBps      float64
	SellBps     float64
	BuySamples  int
	SellSamples int
}

func (e MarketMakerSideVolatilityEstimate) MaxBps() float64 {
	return math.Max(e.BuyBps, e.SellBps)
}

func (e MarketMakerSideVolatilityEstimate) MinSamples() int {
	if e.BuySamples < e.SellSamples {
		return e.BuySamples
	}
	return e.SellSamples
}

// MarketMakerHorizonDecision is the currently selected trading window. The
// score is the estimated fee-adjusted two-sided edge per hour, based on the
// observed crossing frequency and the average spacing between crossings.
type MarketMakerHorizonDecision struct {
	Horizon              time.Duration
	HorizonSeconds       int64
	QuoteDistanceBps     float64 // legacy neutral mid-to-quote distance
	BuyTouchDistanceBps  float64 // current ask to passive bid
	SellTouchDistanceBps float64 // passive ask to current bid
	UpCrosses            int
	DownCrosses          int
	UpCrossesPerHour     float64
	DownCrossesPerHour   float64
	MeanUpSpacing        time.Duration
	MeanDownSpacing      time.Duration
	ObservedHours        float64
	EffectiveSamples     float64
	BuyTouchProbability  float64 // Jeffreys-posterior probability of an ask-path touch within Horizon
	SellTouchProbability float64 // Jeffreys-posterior probability of a bid-path touch within Horizon
	BothTouchProbability float64 // coherent probability that both executable sides touch within Horizon
	TouchCovariance      float64 // Cov(1_buy_touch, 1_sell_touch) on paired completed BBO paths
	BuyTouchStdError     float64
	SellTouchStdError    float64
	BothTouchStdError    float64
	OnlineFastWeight     float64
	EstimatorSource      string
	NetRoundTripEdgeBps  float64
	ScoreBpsPerHour      float64
	ScoreStdErrorBpsHour float64
	// SelectionScoreBpsPerHour preserves the crossing score above and adds the
	// positive option value of the next executable marginal BUY, expressed in
	// the same bps/hour unit. The strategy can always decline a negative-utility
	// BUY, so its value is max(0, Delta CE), not a forced negative payoff. The
	// extra term is evaluated only while current inventory is below its target.
	SelectionScoreBpsPerHour          float64
	MarginalBuyEvaluated              bool
	MarginalBuyNotionalJPY            float64
	MarginalBuyTargetNotionalJPY      float64
	MarginalBuyTargetUpProbability    float64
	MarginalBuyCertaintyEquivalentJPY float64
	MarginalBuyUtilityBpsPerHour      float64
	PathUtilityEvaluated              bool
	PathUtilityAction                 FastHorizonAction
	PathUtilityBuyNotionalJPY         float64
	PathUtilitySellNotionalJPY        float64
	PathUtilityCertaintyEquivalentJPY float64
	PathUtilityBpsPerHour             float64
	HorizonUncertaintyPenaltyBpsHour  float64
	HorizonReplacementCostBpsHour     float64
	DistanceOptimized                 bool
	UpdatedAt                         time.Time
	Reason                            string
}

// applyLifecycleAwareHorizonUtility turns a horizon's fee-net score into the
// single lifecycle objective used for selection:
//
//	U_H = S_H - z*SE(S_H) - C_replace/H.
//
// S_H remains the existing crossing/path utility. The uncertainty term is a
// confidence penalty, while replacement cost is queue/opportunity loss only;
// maker fees are already included in S_H and are never charged twice here.
func applyLifecycleAwareHorizonUtility(decision MarketMakerHorizonDecision, config MarketMakerConfig) MarketMakerHorizonDecision {
	if decision.Horizon <= 0 {
		return decision
	}
	z := config.InventoryRiskZScore
	if z <= 0 || math.IsNaN(z) || math.IsInf(z, 0) {
		z = 1.645
	}
	uncertainty := z * math.Max(0, decision.ScoreStdErrorBpsHour)
	replacement := math.Max(0, config.QuoteLifecycleAction.ReplacementCostBps)
	if hours := decision.Horizon.Hours(); hours > 0 {
		replacement /= hours
	} else {
		replacement = 0
	}
	decision.HorizonUncertaintyPenaltyBpsHour = uncertainty
	decision.HorizonReplacementCostBpsHour = replacement
	decision.SelectionScoreBpsPerHour -= uncertainty + replacement
	return decision
}

// FastHorizonMarginalBuyInput is the causal account state needed to return
// marginal acquisition to the Fast horizon objective.  MarginalBuyNotionalJPY
// is one executable venue cell, not the entire target deficit: horizon choice
// asks whether the *next* BUY improves terminal wealth before the downstream
// unified quantity model decides how many cells to expose.
type FastHorizonMarginalBuyInput struct {
	CurrentInventoryNotionalJPY float64
	TargetInventoryNotionalJPY  float64
	HardMinInventoryNotionalJPY float64
	HardMaxInventoryNotionalJPY float64
	PosteriorInventoryTarget    bool
	PairEquityJPY               float64
	MarginalBuyNotionalJPY      float64
	AvailableBuyCapitalJPY      float64
	MarginalSellNotionalJPY     float64
	AvailableSellInventoryJPY   float64
	RiskAversion                float64
	ConfidenceZScore            float64
}

func scoreFastHorizonWithPathUtility(
	decision MarketMakerHorizonDecision,
	stats JointPathPayoffStats,
	in FastHorizonMarginalBuyInput,
) MarketMakerHorizonDecision {
	value := evaluateSymmetricFastHorizonAction(decision, stats, in)
	decision.SelectionScoreBpsPerHour = value.ScoreBpsPerHour
	decision.PathUtilityEvaluated = value.Evaluated
	decision.PathUtilityAction = value.Action
	decision.PathUtilityBuyNotionalJPY = value.BuyNotionalJPY
	decision.PathUtilitySellNotionalJPY = value.SellNotionalJPY
	decision.PathUtilityCertaintyEquivalentJPY = value.CertaintyEquivalentJPY
	decision.PathUtilityBpsPerHour = value.ScoreBpsPerHour
	decision.MarginalBuyTargetNotionalJPY = value.TargetInventoryNotionalJPY
	return decision
}

func scoreFastHorizonWithMarginalBuy(
	decision MarketMakerHorizonDecision,
	stats JointPathPayoffStats,
	in FastHorizonMarginalBuyInput,
) MarketMakerHorizonDecision {
	decision.SelectionScoreBpsPerHour = decision.ScoreBpsPerHour
	target := in.TargetInventoryNotionalJPY
	if in.PosteriorInventoryTarget {
		posterior := PosteriorInventoryRiskTarget(
			in.TargetInventoryNotionalJPY,
			in.HardMinInventoryNotionalJPY,
			in.HardMaxInventoryNotionalJPY,
			stats)
		if posterior.Enabled {
			target = posterior.TargetBase
			decision.MarginalBuyTargetUpProbability = posterior.UpProbability
		}
	}
	decision.MarginalBuyTargetNotionalJPY = target
	deficit := target - in.CurrentInventoryNotionalJPY
	unit := in.MarginalBuyNotionalJPY
	if decision.Horizon <= 0 || in.PairEquityJPY <= 0 || unit <= 0 ||
		deficit+1e-9 < unit || in.AvailableBuyCapitalJPY+1e-9 < unit ||
		stats.EffectiveSamples <= 1 {
		return decision
	}
	z := in.ConfidenceZScore
	if z <= 0 {
		z = 1.645
	}
	payoff := stats.EvaluateTargetRelativePosition(
		in.CurrentInventoryNotionalJPY, target,
		unit, 0,
		in.PairEquityJPY, in.RiskAversion, z)
	hours := decision.Horizon.Hours()
	if hours <= 0 {
		return decision
	}
	utilityBpsPerHour := math.Max(0, payoff.CertaintyEquivalent) /
		in.PairEquityJPY * 10_000 / hours
	decision.MarginalBuyEvaluated = true
	decision.MarginalBuyNotionalJPY = unit
	decision.MarginalBuyCertaintyEquivalentJPY = payoff.CertaintyEquivalent
	decision.MarginalBuyUtilityBpsPerHour = utilityBpsPerHour
	decision.SelectionScoreBpsPerHour += utilityBpsPerHour
	return decision
}

// HasSufficientCrossings reports whether this decision contains enough
// completed quote-distance exposure for the selected horizon. A side is not
// required to have crossed: zero touches over valid completed windows are
// statistical evidence, represented by the Jeffreys posterior above.
// Directional Gamma barrier events are intentionally not accepted as a
// substitute because their barrier width may differ from the maker quote.
func (d MarketMakerHorizonDecision) HasSufficientCrossings(minSamples int) bool {
	if minSamples <= 0 {
		minSamples = 1
	}
	validReason := d.Reason == "max fee-adjusted two-sided edge per hour" ||
		d.Reason == "online dual-timescale fee-adjusted two-sided edge per hour" ||
		d.Reason == "conditional same-symbol fee-adjusted two-sided edge per hour"
	samples := float64(d.UpCrosses + d.DownCrosses)
	if d.EffectiveSamples > 0 {
		samples = d.EffectiveSamples
	}
	return validReason &&
		d.Horizon > 0 &&
		samples >= float64(minSamples)
}

// BuyTouchRatePerHour and SellTouchRatePerHour expose a discrete-window
// renewal rate p/H. They deliberately do not apply 1-exp(-lambda*H), which
// would impose a homogeneous Poisson arrival model on sparse BBO crossings.
func (d MarketMakerHorizonDecision) BuyTouchRatePerHour() float64 {
	if d.Horizon > 0 && d.BuyTouchProbability > 0 {
		return d.BuyTouchProbability / d.Horizon.Hours()
	}
	return d.DownCrossesPerHour
}

func (d MarketMakerHorizonDecision) SellTouchRatePerHour() float64 {
	if d.Horizon > 0 && d.SellTouchProbability > 0 {
		return d.SellTouchProbability / d.Horizon.Hours()
	}
	return d.UpCrossesPerHour
}

// MarketMakerHorizonModel updates at most once per configured interval. It
// uses completed historical windows, so a newly selected horizon is only a
// reference for the next trading window; callers must not cancel an existing
// quote merely because this decision changed.
type MarketMakerHorizonModel struct {
	points                 []MarketMakerHorizonPoint
	lastSecond             time.Time
	lastTrimSecond         time.Time
	lastUpdate             time.Time
	decision               MarketMakerHorizonDecision
	sideHARVarianceRisk    map[time.Duration]*OnlineSideHARVarianceRisk
	crossingExposureCaches map[time.Duration]*marketMakerHorizonExposureCache
	bboRangeIndex          *marketMakerBBORangeIndex
	fastDrift              map[time.Duration]*fastDriftRegression
	fastDriftBBOStateTags  map[time.Duration]fastDriftBBOStateTagCache
	asymmetricRiskFeatures map[time.Duration]asymmetricOscillationRiskFeatureCache
	conditionalStates      map[time.Duration]conditionalExecutionState
	volumeProfiles         map[time.Duration]*RollingVolumeProfile
	volumeProfileWindows   []time.Duration
	volumeProfileConfig    VolumeProfileConfig
	// pathDecay is causal model state keyed by the selected Fast horizon. It
	// contains only matured path-persistence and Neff summaries; raw paths
	// remain in crossingExposureCaches and are still valued by the exact BBO
	// first-passage estimator.
	pathDecay                map[time.Duration]*adaptivePathDecayState
	downsideEProcess         *DrawdownEProcess
	downsideDecision         DrawdownEProcessDecision
	downsidePublished        DrawdownEProcessDecision
	downsidePublishedAt      time.Time
	downsidePublishedHorizon time.Duration
	upsideEProcess           *DrawdownEProcess
	upsideDecision           DrawdownEProcessDecision
	upsidePublished          DrawdownEProcessDecision
	upsidePublishedAt        time.Time
	upsidePublishedHorizon   time.Duration
	// crossingDecisionCache is an ephemeral same-observation cache. A single
	// quote pass asks for the same completed-path statistics from several
	// downstream optimizers; reusing an exact query avoids rescanning the
	// horizon exposure slice without changing the statistical clock. It is
	// intentionally discarded whenever a new BBO observation arrives and is
	// never checkpointed.
	crossingDecisionCacheAt time.Time
	crossingDecisionCache   map[crossingDecisionCacheKey]MarketMakerHorizonDecision
}

type crossingDecisionCacheKey struct {
	horizon, lookback                time.Duration
	buyDistance, sellDistance        uint64
	grossEdge, makerFee              uint64
	adverseSelection, minimumNetEdge uint64
}

func makeCrossingDecisionCacheKey(c MarketMakerConfig, horizon time.Duration, buyDistanceBps, sellDistanceBps, grossQuoteEdgeBps float64) crossingDecisionCacheKey {
	return crossingDecisionCacheKey{
		horizon: horizon, lookback: time.Duration(c.HorizonLookback),
		buyDistance: math.Float64bits(buyDistanceBps), sellDistance: math.Float64bits(sellDistanceBps),
		grossEdge: math.Float64bits(grossQuoteEdgeBps), makerFee: math.Float64bits(c.MakerFeeBps),
		adverseSelection: math.Float64bits(c.AdverseSelectionBps), minimumNetEdge: math.Float64bits(c.MinimumNetEdgeBps),
	}
}

func (m *MarketMakerHorizonModel) clearCrossingDecisionCache() {
	if m == nil {
		return
	}
	m.crossingDecisionCacheAt = time.Time{}
	m.crossingDecisionCache = nil
}

func (m *MarketMakerHorizonModel) cachedCrossingDecision(now time.Time, key crossingDecisionCacheKey) (MarketMakerHorizonDecision, bool) {
	if m == nil || m.crossingDecisionCacheAt.IsZero() || !m.crossingDecisionCacheAt.Equal(now) {
		return MarketMakerHorizonDecision{}, false
	}
	d, ok := m.crossingDecisionCache[key]
	return d, ok
}

func (m *MarketMakerHorizonModel) storeCrossingDecision(now time.Time, key crossingDecisionCacheKey, decision MarketMakerHorizonDecision) {
	if m == nil {
		return
	}
	if !m.crossingDecisionCacheAt.Equal(now) {
		m.crossingDecisionCacheAt = now
		m.crossingDecisionCache = make(map[crossingDecisionCacheKey]MarketMakerHorizonDecision)
	}
	m.crossingDecisionCache[key] = decision
}

type asymmetricOscillationRiskFeatureCache struct {
	Bucket   time.Time
	Features AsymmetricOscillationRiskFeatures
	Valid    bool
}

// EmpiricalSideVolatilityEstimate returns executable-price realized
// volatility for both maker sides. Buy volatility is estimated from ask log
// returns; sell volatility is estimated from bid log returns. For irregular,
// change-driven BBO samples it uses the quadratic-variation rate
// sqrt(sum(return^2)/sum(elapsed seconds)). Zero moves and quiet elapsed time
// remain in the denominator; conditioning on non-zero returns would
// systematically overstate volatility on sparse symbols.
func (m MarketMakerHorizonModel) EmpiricalSideVolatilityEstimate(now time.Time, lookback time.Duration) MarketMakerSideVolatilityEstimate {
	if now.IsZero() || lookback <= 0 || len(m.points) < 2 {
		return MarketMakerSideVolatilityEstimate{}
	}
	cutoff := now.Add(-lookback)
	var buySquares, sellSquares, buySeconds, sellSeconds float64
	buySamples, sellSamples := 0, 0
	start := sort.Search(len(m.points), func(i int) bool { return !m.points[i].At.Before(cutoff) })
	if start < 1 {
		start = 1
	}
	for i := start; i < len(m.points); i++ {
		previous, current := m.points[i-1], m.points[i]
		if current.GapBefore || current.At.Before(cutoff) || previous.At.Before(cutoff) {
			continue
		}
		seconds := current.At.Sub(previous.At).Seconds()
		if seconds <= 0 || seconds > 120 {
			continue
		}
		if currentAsk, previousAsk := current.askPrice(), previous.askPrice(); currentAsk > 0 && previousAsk > 0 {
			ret := math.Log(currentAsk / previousAsk)
			buySquares += ret * ret
			buySeconds += seconds
			buySamples++
		}
		if currentBid, previousBid := current.bidPrice(), previous.bidPrice(); currentBid > 0 && previousBid > 0 {
			ret := math.Log(currentBid / previousBid)
			sellSquares += ret * ret
			sellSeconds += seconds
			sellSamples++
		}
	}
	estimate := MarketMakerSideVolatilityEstimate{BuySamples: buySamples, SellSamples: sellSamples}
	if buySamples >= 8 && buySeconds > 0 {
		estimate.BuyBps = math.Sqrt(buySquares/buySeconds) * 10_000
	}
	if sellSamples >= 8 && sellSeconds > 0 {
		estimate.SellBps = math.Sqrt(sellSquares/sellSeconds) * 10_000
	}
	return estimate
}

// EmpiricalVolatilityEstimate is the conservative common-risk compatibility
// view. New quote code should consume EmpiricalSideVolatilityEstimate so the
// two execution sides are not collapsed prematurely.
func (m MarketMakerHorizonModel) EmpiricalVolatilityEstimate(now time.Time, lookback time.Duration) (float64, int) {
	estimate := m.EmpiricalSideVolatilityEstimate(now, lookback)
	return estimate.MaxBps(), estimate.MinSamples()
}

// EmpiricalVolatilityFloor is retained for offline callers and backwards
// compatibility. Live quote sizing uses EmpiricalVolatilityEstimate together
// with ShrinkVolatility.
func (m MarketMakerHorizonModel) EmpiricalVolatilityFloor(now time.Time, lookback time.Duration) float64 {
	estimate, _ := m.EmpiricalVolatilityEstimate(now, lookback)
	return estimate
}

// ShrinkVolatility combines live and long-history estimates in variance space.
// liveSamples controls how much evidence the short estimator contributes;
// priorStrength is the equivalent sample size assigned to the empirical prior.
func ShrinkVolatility(liveBps, priorBps float64, liveSamples, priorStrength int) (float64, float64) {
	liveBps = math.Max(0, liveBps)
	priorBps = math.Max(0, priorBps)
	if liveBps <= 0 {
		if priorBps > 0 {
			return priorBps, 0
		}
		return 0, 0
	}
	if priorBps <= 0 || priorStrength <= 0 {
		return liveBps, 1
	}
	if liveSamples < 0 {
		liveSamples = 0
	}
	weight := float64(liveSamples) / float64(liveSamples+priorStrength)
	variance := weight*liveBps*liveBps + (1-weight)*priorBps*priorBps
	return math.Sqrt(math.Max(0, variance)), weight
}

func (m *MarketMakerHorizonModel) Observe(at time.Time, mid float64, c MarketMakerConfig) {
	m.ObserveBook(at, mid, mid, c)
}

// ObserveBook records the executable BBO. This is the production observation
// path; Observe remains only for trade-only archives that do not contain a
// spread and therefore cannot identify side-specific execution prices.
func (m *MarketMakerHorizonModel) ObserveBook(at time.Time, bid, ask float64, c MarketMakerConfig) {
	m.ObserveBookWithSizes(at, bid, 0, ask, 0, c)
}

// ObserveBookWithSizes records prices and visible top-of-book depth so future
// horizon averages can use the BBO-weighted price rather than an event-counted
// midpoint. The observation remains one point per second.
func (m *MarketMakerHorizonModel) ObserveBookWithSizes(
	at time.Time, bid, bidSize, ask, askSize float64, c MarketMakerConfig,
) {
	// Binance book-ticker is change-driven: a short silence means the last BBO
	// remained valid, not that the price path is unknown. Reserve gap markers
	// for connection-scale outages; aggregate-trade warmup keeps its stricter
	// explicit five-second capture-gap rule.
	gapBefore := !m.lastSecond.IsZero() && at.Truncate(time.Second).Sub(m.lastSecond) >= marketMakerHorizonGapThreshold
	m.ObserveBookWithSizesAndGap(at, bid, bidSize, ask, askSize, c, gapBefore)
}

// ObserveWithGap appends a trade-derived synthetic BBO and is retained for
// archives that have no spread. Production BBO paths use ObserveBookWithGap.
func (m *MarketMakerHorizonModel) ObserveWithGap(at time.Time, mid float64, c MarketMakerConfig, gapBefore bool) {
	m.ObserveBookWithGap(at, mid, mid, c, gapBefore)
}

// ObserveBookWithGap appends an executable BBO sample and marks whether the
// interval before it contained a capture outage. Horizon crossing statistics
// must not treat an outage as a continuous path; the marker is also respected
// by the realized-volatility estimator.
func (m *MarketMakerHorizonModel) ObserveBookWithGap(at time.Time, bid, ask float64, c MarketMakerConfig, gapBefore bool) {
	m.ObserveBookWithSizesAndGap(at, bid, 0, ask, 0, c, gapBefore)
}

// ObserveBookWithSizesAndGap is the authoritative executable-BBO observation
// path. It retains the depth-weighted BBO price used by completed-window target
// estimation while bid and ask remain separate for execution payoffs.
func (m *MarketMakerHorizonModel) ObserveBookWithSizesAndGap(
	at time.Time, bid, bidSize, ask, askSize float64,
	c MarketMakerConfig, gapBefore bool,
) {
	if at.IsZero() || bid <= 0 || ask <= 0 || ask < bid {
		return
	}
	// Any accepted BBO can change completed-window availability or the most
	// recent same-second replacement. Invalidate only the ephemeral query
	// cache; distance-independent exposure caches remain incrementally valid.
	m.clearCrossingDecisionCache()
	m.conditionalStates = nil
	if gapBefore {
		m.fastDriftBBOStateTags = nil
		m.asymmetricRiskFeatures = nil
		if m.pathDecay != nil {
			for _, state := range m.pathDecay {
				if state != nil {
					state.resetSegment()
				}
			}
		}
	}
	// Normal live/replay configs are normalized before the stream starts.
	// Retain zero-value compatibility for unit tests and legacy callers, but
	// avoid rebuilding the full default config on every BBO event.
	if c.HorizonLookback <= 0 || c.MaxTradingWindow <= 0 {
		c.setDefaults()
	}
	m.configureVolumeProfiles(c)
	if gapBefore {
		m.resetVolumeProfiles()
	}
	m.observeExecutableDownside(at, bid, ask, c, gapBefore)
	m.observeSideHARVarianceRisk(at, bid, ask, c, gapBefore)
	mid := (bid + ask) / 2
	weightedPrice := bboDepthWeightedPrice(bid, bidSize, ask, askSize)
	imbalance, depthReady := bboDepthImbalance(bidSize, askSize)
	second := at.Truncate(time.Second)
	volumeProfiles, volumeProfileN := m.volumeProfileSnapshots(weightedPrice, at)
	if !m.lastSecond.IsZero() && second.Equal(m.lastSecond) {
		if n := len(m.points); n > 0 {
			m.points[n-1] = MarketMakerHorizonPoint{
				At: second, Bid: bid, Ask: ask, Mid: mid,
				BBOWeightedPrice: weightedPrice,
				BookImbalance:    imbalance, BookDepthReady: depthReady,
				GapBefore:      m.points[n-1].GapBefore || gapBefore,
				volumeProfiles: volumeProfiles, volumeProfileN: volumeProfileN,
			}
		}
		return
	}
	m.lastSecond = second
	m.points = append(m.points, MarketMakerHorizonPoint{
		At: second, Bid: bid, Ask: ask, Mid: mid,
		BBOWeightedPrice: weightedPrice,
		BookImbalance:    imbalance, BookDepthReady: depthReady,
		GapBefore: gapBefore, volumeProfiles: volumeProfiles, volumeProfileN: volumeProfileN,
	})
	cutoff := second.Add(-time.Duration(c.HorizonLookback) - time.Duration(c.MaxTradingWindow) - time.Minute)
	first := sort.Search(len(m.points), func(i int) bool { return !m.points[i].At.Before(cutoff) })
	// Retain a bounded history, but do not copy the entire horizon slice on
	// every second once it crosses the cutoff. A minute/1024-point cadence is
	// observationally equivalent for all lookback calculations and avoids an
	// O(history) copy in the hot path.
	if first > 0 && (first >= 1024 || m.lastTrimSecond.IsZero() || second.Sub(m.lastTrimSecond) >= time.Minute) {
		m.points = append([]MarketMakerHorizonPoint(nil), m.points[first:]...)
		m.lastTrimSecond = second
	}
}

func (m *MarketMakerHorizonModel) observeExecutableDownside(
	at time.Time, bid, ask float64, c MarketMakerConfig, gapBefore bool,
) {
	if m == nil {
		return
	}
	if m.downsideEProcess == nil {
		m.downsideEProcess = NewDrawdownEProcess(DrawdownEProcessConfig{
			Windows: c.FastModelWindows(), ConfidenceZ: c.InventoryRiskZScore,
		})
	}
	if m.upsideEProcess == nil {
		m.upsideEProcess = NewDrawdownEProcess(DrawdownEProcessConfig{
			Windows: c.FastModelWindows(), ConfidenceZ: c.InventoryRiskZScore,
		})
	}
	if gapBefore {
		m.downsideEProcess.Reset()
		m.upsideEProcess.Reset()
	}
	next := m.downsideEProcess.ObserveMinute(at, bid, ask)
	if next.Reason != "same-minute executable update retained" {
		m.downsideDecision = next
	}
	// Reciprocal executable prices preserve ordering (1/ask < 1/bid) and
	// transform an ask-side rise into a drawdown.  This reuses the identical
	// QV/jump/multiscale test while keeping BUY volatility on executable asks.
	upside := m.upsideEProcess.ObserveMinute(at, 1/ask, 1/bid)
	if upside.Reason != "same-minute executable update retained" {
		m.upsideDecision = upside
	}
}

// ExecutableDownsideDecision returns the latest time-uniform bid/ask agreement
// state and a SELL-side forecast expressed on the selected Fast horizon.
func (m *MarketMakerHorizonModel) ExecutableDownsideDecision(horizon time.Duration) DrawdownEProcessDecision {
	if m == nil {
		return DrawdownEProcessDecision{Reason: "executable downside model unavailable"}
	}
	d := m.downsidePublished
	// The e-process state is published only on the fixed model clock. Its
	// forecast is linear in future quadratic variation and therefore in the
	// requested horizon, so rescaling the frozen forecast is exact and does not
	// leak intra-bucket observations into the executable decision.
	if d.Active && horizon > 0 && m.downsidePublishedHorizon > 0 {
		d.BidForecastBps *= float64(horizon) / float64(m.downsidePublishedHorizon)
	}
	return d
}

// ExecutableUpsideDecision returns the fixed-clock BUY opportunity snapshot.
// It is produced from reciprocal BBO, so BidForecastBps represents the
// same-horizon rise of the original executable ask.
func (m *MarketMakerHorizonModel) ExecutableUpsideDecision(horizon time.Duration) DrawdownEProcessDecision {
	if m == nil {
		return DrawdownEProcessDecision{Reason: "executable upside model unavailable"}
	}
	u := m.upsidePublished
	if u.Active && horizon > 0 && m.upsidePublishedHorizon > 0 {
		u.BidForecastBps *= float64(horizon) / float64(m.upsidePublishedHorizon)
	}
	return u
}

func (m *MarketMakerHorizonModel) publishExecutableDownside(at time.Time, horizon time.Duration) {
	if m == nil {
		return
	}
	d := m.downsideDecision
	if m.downsideEProcess != nil && d.Active {
		d.BidForecastBps = m.downsideEProcess.DownsideForecastBps(horizon)
	}
	m.downsidePublished = d
	m.downsidePublishedAt = at
	m.downsidePublishedHorizon = horizon
	u := m.upsideDecision
	if m.upsideEProcess != nil && u.Active {
		u.BidForecastBps = m.upsideEProcess.DownsideForecastBps(horizon)
	}
	m.upsidePublished = u
	m.upsidePublishedAt = at
	m.upsidePublishedHorizon = horizon
}

// rebuildExecutableDownside restores sequential downside state from the
// bounded checkpoint BBO history.  It does not require an offline artifact.
func (m *MarketMakerHorizonModel) rebuildExecutableDownside(c MarketMakerConfig) {
	if m == nil {
		return
	}
	m.downsideEProcess = nil
	m.downsideDecision = DrawdownEProcessDecision{}
	m.downsidePublished = DrawdownEProcessDecision{}
	m.downsidePublishedAt = time.Time{}
	m.downsidePublishedHorizon = 0
	m.upsideEProcess = nil
	m.upsideDecision = DrawdownEProcessDecision{}
	m.upsidePublished = DrawdownEProcessDecision{}
	m.upsidePublishedAt = time.Time{}
	m.upsidePublishedHorizon = 0
	for _, point := range m.points {
		m.observeExecutableDownside(point.At, point.bidPrice(), point.askPrice(), c, point.GapBefore)
	}
}

func (c MarketMakerConfig) FastModelWindows() []time.Duration {
	c.setDefaults()
	if len(c.FastWindows) == 0 {
		return []time.Duration{time.Duration(c.FastWindow)}
	}
	seen := make(map[time.Duration]struct{}, len(c.FastWindows))
	out := make([]time.Duration, 0, len(c.FastWindows))
	for _, configured := range c.FastWindows {
		window := time.Duration(configured)
		if window <= 0 {
			continue
		}
		if _, ok := seen[window]; ok {
			continue
		}
		seen[window] = struct{}{}
		out = append(out, window)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	if len(out) == 0 {
		out = append(out, time.Duration(c.FastWindow))
	}
	return out
}

func (c MarketMakerConfig) TradingHorizons() []time.Duration {
	c.setDefaults()
	minHorizon := time.Duration(c.MinTradingWindow)
	maxHorizon := time.Duration(c.MaxTradingWindow)
	if len(c.FastWindows) > 0 {
		configured := c.FastModelWindows()
		out := make([]time.Duration, 0, len(configured))
		for _, horizon := range configured {
			if horizon >= minHorizon && horizon <= maxHorizon {
				out = append(out, horizon)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	all := []time.Duration{time.Minute, 3 * time.Minute, 5 * time.Minute, 10 * time.Minute, 15 * time.Minute, 20 * time.Minute, 30 * time.Minute}
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

// InventoryBand is an automatically calculated target-centered band. The
// risk-budget and quote-level capacity determine a half-width around the capital
// expectation; configured outer capital min/max ratios remain hard portfolio
// guardrails.
type InventoryBand struct {
	MinInventory                 float64
	Target                       float64
	Limit                        float64 // legacy symmetric normalization width
	MaxInventory                 float64
	TargetRatio                  float64
	OrderSize                    float64
	RiskMoveBps                  float64
	RiskBudgetJPY                float64
	PairEquityJPY                float64
	RiskCapNotionalJPY           float64
	RiskBandHalfWidthNotionalJPY float64
	CapitalMinNotionalJPY        float64
	CapitalTargetNotionalJPY     float64
	CapitalCapNotionalJPY        float64
}

type InventoryControlDecision struct {
	Band                  InventoryBand
	FastTradingZone       bool
	LongHorizonAdjustment bool
	Reason                string
}

// SelectInventoryControl integrates a long-horizon no-trade correction into
// Fast without installing a second execution controller. Direction is non-zero
// only after current inventory exits the Macro no-trade region. The Macro
// boundary can then strengthen the correction, but never weaken an already more
// conservative Fast target. Fast keeps ownership of price, quantity, fill
// rebalancing, and order lifetime.
func SelectInventoryControl(
	fastBand, macroBand InventoryBand,
	longHorizonEnabled bool, correctionDirection int,
) InventoryControlDecision {
	validBand := func(b InventoryBand) bool {
		return b.MaxInventory > b.MinInventory &&
			b.Target >= b.MinInventory && b.Target <= b.MaxInventory
	}
	if !validBand(fastBand) {
		return InventoryControlDecision{Band: fastBand, Reason: "invalid Fast inventory band"}
	}
	if !longHorizonEnabled || correctionDirection == 0 || !validBand(macroBand) {
		return InventoryControlDecision{
			Band: fastBand, FastTradingZone: true,
			Reason: "no long-horizon boundary correction required",
		}
	}
	projectedTarget := fastBand.Target
	switch {
	case correctionDirection > 0 && macroBand.Target > projectedTarget:
		projectedTarget = macroBand.Target
	case correctionDirection < 0 && macroBand.Target < projectedTarget:
		projectedTarget = macroBand.Target
	default:
		return InventoryControlDecision{
			Band: fastBand, FastTradingZone: true,
			Reason: "Fast target already provides at least the long-horizon correction",
		}
	}
	lowerWidth := math.Max(0, fastBand.Target-fastBand.MinInventory)
	upperWidth := math.Max(0, fastBand.MaxInventory-fastBand.Target)
	band := fastBand
	band.Target = projectedTarget
	band.MinInventory = projectedTarget - lowerWidth
	band.MaxInventory = projectedTarget + upperWidth
	band.Limit = math.Max(lowerWidth, upperWidth)
	if band.PairEquityJPY > 0 {
		mid := 0.0
		switch {
		case fastBand.Target != 0 && fastBand.CapitalTargetNotionalJPY != 0:
			mid = fastBand.CapitalTargetNotionalJPY / fastBand.Target
		case fastBand.MinInventory != 0 && fastBand.CapitalMinNotionalJPY != 0:
			mid = fastBand.CapitalMinNotionalJPY / fastBand.MinInventory
		case fastBand.MaxInventory != 0 && fastBand.CapitalCapNotionalJPY != 0:
			mid = fastBand.CapitalCapNotionalJPY / fastBand.MaxInventory
		}
		if mid > 0 {
			band.TargetRatio = projectedTarget * mid / band.PairEquityJPY
			band.CapitalMinNotionalJPY = band.MinInventory * mid
			band.CapitalTargetNotionalJPY = band.Target * mid
			band.CapitalCapNotionalJPY = band.MaxInventory * mid
		}
	}
	return InventoryControlDecision{
		Band: band, LongHorizonAdjustment: true,
		Reason: "Fast target strengthened to the active long-horizon boundary",
	}
}

// HardInventoryBand returns the configured absolute capital guardrails while
// retaining the live Macro target and stochastic-band diagnostics. Macro's
// lower/upper variation is an expected-position policy, not permission to
// remove the opposite Fast quote. Only these outer bounds may hard-gate a side.
func (c MarketMakerConfig) HardInventoryBand(soft InventoryBand, midPrice, pairEquityJPY float64) InventoryBand {
	c.setDefaults()
	hard := soft
	if midPrice <= 0 || pairEquityJPY <= 0 {
		return hard
	}
	minNotional := pairEquityJPY * c.InventoryCapitalMinRatio
	maxNotional := pairEquityJPY * c.InventoryCapitalMaxRatio
	hard.MinInventory = minNotional / midPrice
	hard.MaxInventory = maxNotional / midPrice
	hard.CapitalMinNotionalJPY = minNotional
	hard.CapitalCapNotionalJPY = maxNotional
	return hard
}

// InventoryBandFromPolicyRatios materializes an already-solved no-trade
// region. Unlike DynamicInventoryBandWithCapitalPolicyBounds it does not apply
// a configured order-level divisor or a second volatility width; those effects
// are already present in the free-boundary solution.
func (c MarketMakerConfig) InventoryBandFromPolicyRatios(
	midPrice, pairEquityJPY, lowerRatio, executionTargetRatio, upperRatio float64,
) InventoryBand {
	c.setDefaults()
	minimum := math.Max(c.InventoryCapitalMinRatio, math.Min(c.InventoryCapitalMaxRatio, lowerRatio))
	maximum := math.Max(minimum, math.Min(c.InventoryCapitalMaxRatio, upperRatio))
	target := math.Max(minimum, math.Min(maximum, executionTargetRatio))
	if midPrice <= 0 || pairEquityJPY <= 0 {
		return InventoryBand{TargetRatio: target, PairEquityJPY: pairEquityJPY}
	}
	minNotional := pairEquityJPY * minimum
	targetNotional := pairEquityJPY * target
	maxNotional := pairEquityJPY * maximum
	lowerWidth := math.Max(0, targetNotional-minNotional)
	upperWidth := math.Max(0, maxNotional-targetNotional)
	return InventoryBand{
		MinInventory:                 minNotional / midPrice,
		Target:                       targetNotional / midPrice,
		Limit:                        math.Max(lowerWidth, upperWidth) / midPrice,
		MaxInventory:                 maxNotional / midPrice,
		TargetRatio:                  target,
		OrderSize:                    c.QuoteNotional / midPrice,
		RiskBudgetJPY:                c.EffectiveInventoryRiskBudgetJPY(pairEquityJPY),
		PairEquityJPY:                pairEquityJPY,
		RiskBandHalfWidthNotionalJPY: math.Max(lowerWidth, upperWidth),
		CapitalMinNotionalJPY:        minNotional,
		CapitalTargetNotionalJPY:     targetNotional,
		CapitalCapNotionalJPY:        maxNotional,
	}
}

// TargetCenteredOrderCaps separates the hard inventory band from one
// executable ticket. It is shared by live submission and production replay so
// simulations cannot bypass the same correction and exploration sizing limits.
type TargetCenteredOrderCaps struct {
	TrancheNotional float64
	BuyNotional     float64
	SellQuantity    float64
}

// TargetCenteredInventoryOrderCaps stages both exploration and correction over
// the configured order levels. A target error must not bypass the tranche and
// turn one maker fill into a full portfolio rebalance; repeated fills converge
// geometrically while every ticket remains inside the hard inventory band.
func TargetCenteredInventoryOrderCaps(band InventoryBand, inventory, price, maxLevels float64) TargetCenteredOrderCaps {
	if price <= 0 || band.MaxInventory <= band.MinInventory ||
		band.Target < band.MinInventory || band.Target > band.MaxInventory {
		return TargetCenteredOrderCaps{}
	}
	lowerNotional := math.Max(0, band.Target-band.MinInventory) * price
	upperNotional := math.Max(0, band.MaxInventory-band.Target) * price
	halfWidthNotional := math.Min(lowerNotional, upperNotional)
	if halfWidthNotional <= 0 {
		halfWidthNotional = math.Max(lowerNotional, upperNotional)
	}
	if band.RiskBandHalfWidthNotionalJPY > 0 {
		halfWidthNotional = math.Min(halfWidthNotional, band.RiskBandHalfWidthNotionalJPY)
	}
	if halfWidthNotional <= 0 {
		return TargetCenteredOrderCaps{}
	}
	levels := math.Max(1, maxLevels)
	trancheNotional := halfWidthNotional / levels
	buyCorrection := math.Max(0, band.Target-inventory) * price
	sellCorrectionNotional := math.Max(0, inventory-band.Target) * price
	return TargetCenteredOrderCaps{
		TrancheNotional: trancheNotional,
		BuyNotional:     math.Max(trancheNotional, buyCorrection/levels),
		SellQuantity:    math.Max(trancheNotional, sellCorrectionNotional/levels) / price,
	}
}

// InventoryVariationDecision converts the observed maker-fill process into a
// symmetric inventory variation band around a macro expected position. Maker
// fills are modeled as independent compound-Poisson jumps. If an executable
// order has quote notional q and the two side intensities are lambda+/- then
// Var[dInventoryJPY] = q^2 (lambdaBuy + lambdaSell) T.
type InventoryVariationDecision struct {
	Enabled                    bool
	ExpectedTargetRatio        float64
	LowerRatio                 float64
	UpperRatio                 float64
	ExpectedFillEvents         float64
	InventoryStdDevJPY         float64
	HalfWidthJPY               float64
	ExecutableOrderNotionalJPY float64
}

// InventoryVariationHorizon keeps the inventory distribution on the same
// statistical clock as the adaptive fast model currently influencing quotes.
// The maker horizon is only a fallback for legacy/single-model callers; the
// configured minimum is the final defensive fallback.
func (c MarketMakerConfig) InventoryVariationHorizon(selectedFastWindow, makerHorizon time.Duration) (time.Duration, string) {
	c.setDefaults()
	if selectedFastWindow > 0 {
		return selectedFastWindow, "adaptive-fast"
	}
	if makerHorizon > 0 {
		return makerHorizon, "maker-horizon-fallback"
	}
	return time.Duration(c.MinTradingWindow), "configured-minimum-fallback"
}

// InventoryActuationHorizon is the causal clock used to choose the size of a
// staged Macro correction. A Macro regime can remain valid for hours, but a
// maker order is only allowed to wait for the shorter of the selected Fast
// window and that regime forecast. Using the shortest positive clock prevents a
// long-lived regime lease from making every individual correction needlessly
// small, while still preserving the configured maximum order-level cap.
func (c MarketMakerConfig) InventoryActuationHorizon(selectedFastWindow, regimeForecast time.Duration) (time.Duration, string) {
	c.setDefaults()
	fastValid := selectedFastWindow > 0
	regimeValid := regimeForecast > 0
	switch {
	case fastValid && regimeValid:
		if selectedFastWindow <= regimeForecast {
			return selectedFastWindow, "shortest-fast-regime"
		}
		return regimeForecast, "shortest-regime-fast"
	case fastValid:
		return selectedFastWindow, "fast-only"
	case regimeValid:
		return regimeForecast, "regime-only"
	default:
		fallback := time.Duration(c.MinTradingWindow)
		if fallback <= 0 {
			fallback = time.Minute
		}
		return fallback, "configured-minimum-fallback"
	}
}

// ProbabilisticInventoryVariation treats expectedTargetRatio as E[inventory]
// instead of an instantaneous hard ceiling. A continuous target generally lies
// between executable inventory lattice states, whose centering error is at most
// half a fill. The band therefore admits one executable jump plus that half-cell
// error when the outer portfolio policy has room, and otherwise uses a normal
// approximation to the compound-Poisson variance. Configured capital min/max
// ratios remain absolute outer guardrails.
func (c MarketMakerConfig) ProbabilisticInventoryVariation(
	pairEquityJPY, expectedTargetRatio float64,
	horizon time.Duration,
	buyFillRatePerHour, sellFillRatePerHour, executableOrderNotionalJPY float64,
) InventoryVariationDecision {
	c.setDefaults()
	policyMin := math.Max(0, math.Min(1, c.InventoryCapitalMinRatio))
	policyMax := math.Max(policyMin, math.Min(1, c.InventoryCapitalMaxRatio))
	target := math.Max(policyMin, math.Min(policyMax, expectedTargetRatio))
	d := InventoryVariationDecision{
		ExpectedTargetRatio: target,
		LowerRatio:          target,
		UpperRatio:          target,
	}
	if pairEquityJPY <= 0 || horizon <= 0 || executableOrderNotionalJPY <= 0 {
		return d
	}
	d.ExecutableOrderNotionalJPY = executableOrderNotionalJPY
	d.ExpectedFillEvents = (math.Max(0, buyFillRatePerHour) + math.Max(0, sellFillRatePerHour)) * horizon.Hours()
	d.InventoryStdDevJPY = executableOrderNotionalJPY * math.Sqrt(d.ExpectedFillEvents)
	latticeCenteredOneFillWidthJPY := 1.5 * executableOrderNotionalJPY
	d.HalfWidthJPY = math.Max(latticeCenteredOneFillWidthJPY, c.InventoryRiskZScore*d.InventoryStdDevJPY)

	// Keep the stochastic region symmetric so the controlled process is centered
	// on the macro expectation. At an outer policy boundary the admissible width
	// correctly collapses to zero rather than shifting the expectation.
	lowerRoomJPY := (target - policyMin) * pairEquityJPY
	upperRoomJPY := (policyMax - target) * pairEquityJPY
	d.HalfWidthJPY = math.Min(d.HalfWidthJPY, math.Min(lowerRoomJPY, upperRoomJPY))
	if d.HalfWidthJPY <= 0 || math.IsNaN(d.HalfWidthJPY) || math.IsInf(d.HalfWidthJPY, 0) {
		d.HalfWidthJPY = 0
		return d
	}
	d.LowerRatio = target - d.HalfWidthJPY/pairEquityJPY
	d.UpperRatio = target + d.HalfWidthJPY/pairEquityJPY
	d.Enabled = true
	return d
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
	return c.DynamicInventoryBandWithCapitalPolicy(
		midPrice, volatilityBpsPerSqrtSec, horizon, pairEquityJPY,
		c.InventoryCapitalTargetRatio, c.InventoryCapitalMaxRatio)
}

// DynamicInventoryBandWithCapitalPolicy is the backwards-compatible asymmetric
// runtime policy entry point. New macro callers use the explicit-bounds method
// below so a carrying-risk limit can constrain expected inventory without also
// becoming an instantaneous one-sided ceiling.
func (c MarketMakerConfig) DynamicInventoryBandWithCapitalPolicy(midPrice, volatilityBpsPerSqrtSec float64, horizon time.Duration, pairEquityJPY, capitalTargetRatio, capitalMaxRatio float64) InventoryBand {
	return c.DynamicInventoryBandWithCapitalPolicyBounds(
		midPrice, volatilityBpsPerSqrtSec, horizon, pairEquityJPY,
		c.InventoryCapitalMinRatio, capitalTargetRatio, capitalMaxRatio)
}

// DynamicInventoryBandWithCapitalPolicyBounds applies runtime lower, expected,
// and upper inventory ratios. It lets the macro controller express an expected
// position together with a stochastic no-trade region, while the configured
// capital min/max ratios remain absolute outer portfolio guardrails.
func (c MarketMakerConfig) DynamicInventoryBandWithCapitalPolicyBounds(midPrice, volatilityBpsPerSqrtSec float64, horizon time.Duration, pairEquityJPY, capitalMinRatio, capitalTargetRatio, capitalMaxRatio float64) InventoryBand {
	c.setDefaults()
	capitalMinRatio = math.Max(c.InventoryCapitalMinRatio, math.Min(c.InventoryCapitalMaxRatio, capitalMinRatio))
	capitalMaxRatio = math.Max(capitalMinRatio, math.Min(c.InventoryCapitalMaxRatio, capitalMaxRatio))
	capitalTargetRatio = math.Max(capitalMinRatio, math.Min(capitalMaxRatio, capitalTargetRatio))
	targetRatio := c.EffectiveInventoryTargetRatio()
	if capitalMaxRatio > 0 {
		targetRatio = capitalTargetRatio / capitalMaxRatio
	}
	riskBudgetJPY := c.EffectiveInventoryRiskBudgetJPY(pairEquityJPY)
	if midPrice <= 0 {
		return InventoryBand{Target: c.InventoryTarget, Limit: c.InventoryLimit, TargetRatio: targetRatio, RiskBudgetJPY: riskBudgetJPY, PairEquityJPY: pairEquityJPY}
	}
	orderSize := c.QuoteNotional / midPrice
	riskMoveBps := c.InventoryRiskZScore * math.Max(0, volatilityBpsPerSqrtSec) * math.Sqrt(math.Max(0, horizon.Seconds()))
	riskHalfWidth := math.Inf(1)
	if riskMoveBps > 0 {
		riskHalfWidth = riskBudgetJPY / (riskMoveBps / 10_000)
	}
	capitalMinNotional := 0.0
	capitalTargetNotional := 0.0
	capitalMaxNotional := math.Inf(1)
	if pairEquityJPY > 0 {
		capitalMinNotional = pairEquityJPY * capitalMinRatio
		capitalTargetNotional = pairEquityJPY * capitalTargetRatio
		capitalMaxNotional = pairEquityJPY * capitalMaxRatio
	}
	if pairEquityJPY > 0 && capitalTargetNotional >= capitalMinNotional && capitalMaxNotional >= capitalTargetNotional {
		widthCap := riskHalfWidth
		lowerWidth := math.Min(math.Max(0, capitalTargetNotional-capitalMinNotional), widthCap)
		upperWidth := math.Min(math.Max(0, capitalMaxNotional-capitalTargetNotional), widthCap)
		if (lowerWidth > 0 || upperWidth > 0) && !math.IsInf(math.Max(lowerWidth, upperWidth), 0) {
			minNotional := capitalTargetNotional - lowerWidth
			maxNotional := capitalTargetNotional + upperWidth
			return InventoryBand{
				MinInventory: minNotional / midPrice, Target: capitalTargetNotional / midPrice, Limit: math.Max(lowerWidth, upperWidth) / midPrice, MaxInventory: maxNotional / midPrice,
				TargetRatio: targetRatio, OrderSize: orderSize, RiskMoveBps: riskMoveBps, RiskBudgetJPY: riskBudgetJPY, PairEquityJPY: pairEquityJPY,
				RiskCapNotionalJPY: riskHalfWidth, RiskBandHalfWidthNotionalJPY: math.Max(lowerWidth, upperWidth),
				CapitalMinNotionalJPY: capitalMinNotional, CapitalTargetNotionalJPY: capitalTargetNotional, CapitalCapNotionalJPY: capitalMaxNotional,
			}
		}
		// A zero-width policy is a valid hard carrying cap, not a reason to
		// fall back to the legacy absolute inventory values.
		return InventoryBand{
			MinInventory: capitalTargetNotional / midPrice, Target: capitalTargetNotional / midPrice, MaxInventory: capitalTargetNotional / midPrice,
			TargetRatio: targetRatio, OrderSize: orderSize, RiskMoveBps: riskMoveBps, RiskBudgetJPY: riskBudgetJPY, PairEquityJPY: pairEquityJPY,
			RiskCapNotionalJPY:    riskHalfWidth,
			CapitalMinNotionalJPY: capitalMinNotional, CapitalTargetNotionalJPY: capitalTargetNotional, CapitalCapNotionalJPY: capitalMaxNotional,
		}
	}
	if pairEquityJPY <= 0 {
		maxNotional := riskHalfWidth
		if maxNotional > 0 && !math.IsInf(maxNotional, 0) {
			maxInventory := maxNotional / midPrice
			target := maxInventory * targetRatio
			return InventoryBand{MinInventory: 0, Target: target, Limit: maxInventory - target, MaxInventory: maxInventory, TargetRatio: targetRatio, OrderSize: orderSize, RiskMoveBps: riskMoveBps, RiskBudgetJPY: riskBudgetJPY, RiskCapNotionalJPY: riskHalfWidth, RiskBandHalfWidthNotionalJPY: maxInventory * midPrice}
		}
	}
	return InventoryBand{MinInventory: 0, Target: c.InventoryTarget, Limit: c.InventoryLimit, MaxInventory: c.InventoryTarget + c.InventoryLimit, TargetRatio: targetRatio, OrderSize: orderSize, RiskMoveBps: riskMoveBps, RiskBudgetJPY: riskBudgetJPY, PairEquityJPY: pairEquityJPY}
}

// MakerTouchDistances returns the distances that public BBO data must move
// before a resting maker quote is executable. A buy is observed only when the
// ask reaches the bid quote; a sell is observed only when the bid reaches the
// ask quote. GrossQuoteEdgeBps is the quote-to-quote edge and deliberately does
// not count the market spread as strategy profit.
func MakerTouchDistances(bestBid, bestAsk, bidQuote, askQuote float64) (buyDistanceBps, sellDistanceBps, grossQuoteEdgeBps float64) {
	if bestBid > 0 && bestAsk >= bestBid && bidQuote > 0 && askQuote > bidQuote {
		buyDistanceBps = math.Max(0, math.Log(bestAsk/bidQuote)*10_000)
		sellDistanceBps = math.Max(0, math.Log(askQuote/bestBid)*10_000)
		grossQuoteEdgeBps = math.Max(0, math.Log(askQuote/bidQuote)*10_000)
	}
	return
}

func neutralMakerTouchDistances(bestBid, bestAsk, halfSpreadBps float64) (buyDistanceBps, sellDistanceBps, grossQuoteEdgeBps float64) {
	if bestBid <= 0 || bestAsk < bestBid {
		return halfSpreadBps, halfSpreadBps, 2 * halfSpreadBps
	}
	mid := (bestBid + bestAsk) / 2
	bidQuote := mid * math.Exp(-halfSpreadBps/10_000)
	askQuote := mid * math.Exp(halfSpreadBps/10_000)
	return MakerTouchDistances(bestBid, bestAsk, bidQuote, askQuote)
}

func (m *MarketMakerHorizonModel) CrossingDecisionAtDistance(now time.Time, c MarketMakerConfig, horizon time.Duration, distanceBps float64) MarketMakerHorizonDecision {
	return m.CrossingDecisionAtSideDistances(now, c, horizon, distanceBps, distanceBps, 2*distanceBps)
}

// CrossingDecisionAtSideDistances estimates each side from its executable BBO
// path. Up/sell events use best bid; down/buy events use best ask.
func (m *MarketMakerHorizonModel) CrossingDecisionAtSideDistances(
	now time.Time,
	c MarketMakerConfig,
	horizon time.Duration,
	buyDistanceBps, sellDistanceBps, grossQuoteEdgeBps float64,
) MarketMakerHorizonDecision {
	c.setDefaults()
	cacheKey := makeCrossingDecisionCacheKey(c, horizon, buyDistanceBps, sellDistanceBps, grossQuoteEdgeBps)
	if cached, ok := m.cachedCrossingDecision(now, cacheKey); ok {
		return cached
	}
	neutralDistance := grossQuoteEdgeBps / 2
	d := MarketMakerHorizonDecision{
		Horizon: horizon, HorizonSeconds: int64(horizon.Seconds()), QuoteDistanceBps: neutralDistance,
		BuyTouchDistanceBps: buyDistanceBps, SellTouchDistanceBps: sellDistanceBps,
		Reason: "insufficient completed horizon samples", EstimatorSource: "bbo-side", UpdatedAt: now,
	}
	if horizon <= 0 || buyDistanceBps <= 0 || sellDistanceBps <= 0 {
		m.storeCrossingDecision(now, cacheKey, d)
		return d
	}
	cutoff := now.Add(-time.Duration(c.HorizonLookback))
	var up, down int
	var first, last, lastUp, lastDown, lastExposure time.Time
	var effectiveSamples, weightedUpTouches, weightedDownTouches, weightedBothTouches float64
	var upSpacing, downSpacing []time.Duration
	exposures := m.crossingExposures(horizon)
	for index := firstHorizonExposureAtOrAfter(exposures, cutoff); index < len(exposures); {
		exposure := exposures[index]
		if exposure.EndAt.After(now) {
			break
		}
		sellTouched := exposure.SellExcursionBps >= sellDistanceBps
		buyTouched := exposure.BuyExcursionBps >= buyDistanceBps
		weight := 1.0
		if !lastExposure.IsZero() {
			weight = math.Min(1, exposure.At.Sub(lastExposure).Seconds()/horizon.Seconds())
		}
		if weight <= 0 {
			if exposure.NextMinute <= index {
				break
			}
			index = exposure.NextMinute
			continue
		}
		effectiveSamples += weight
		if sellTouched {
			weightedUpTouches += weight
		}
		if buyTouched {
			weightedDownTouches += weight
		}
		if buyTouched && sellTouched {
			weightedBothTouches += weight
		}
		lastExposure = exposure.At
		sellEvent := sellTouched &&
			(lastUp.IsZero() || exposure.At.Sub(lastUp) >= horizon)
		buyEvent := buyTouched &&
			(lastDown.IsZero() || exposure.At.Sub(lastDown) >= horizon)
		if sellEvent {
			up++
			if !lastUp.IsZero() {
				upSpacing = append(upSpacing, exposure.At.Sub(lastUp))
			}
			lastUp = exposure.At
		}
		if buyEvent {
			down++
			if !lastDown.IsZero() {
				downSpacing = append(downSpacing, exposure.At.Sub(lastDown))
			}
			lastDown = exposure.At
		}
		if first.IsZero() {
			first = exposure.At
		}
		last = exposure.At
		if exposure.NextMinute <= index {
			break
		}
		index = exposure.NextMinute
	}
	if first.IsZero() || !last.After(first) {
		m.storeCrossingDecision(now, cacheKey, d)
		return d
	}
	d.ObservedHours = last.Sub(first).Hours()
	d.UpCrosses, d.DownCrosses = up, down
	d.UpCrossesPerHour, d.DownCrossesPerHour = float64(up)/d.ObservedHours, float64(down)/d.ObservedHours
	d.MeanUpSpacing, d.MeanDownSpacing = meanDuration(upSpacing), meanDuration(downSpacing)
	d.EffectiveSamples = effectiveSamples
	// Jeffreys' Beta(1/2,1/2) prior makes completed zero-touch windows valid
	// evidence without pretending that arrivals are Poisson. Fractional touch
	// counts are the overlap weights of the rolling completed windows.
	d.BuyTouchProbability, d.BuyTouchStdError = jeffreysBernoulliPosterior(weightedDownTouches, effectiveSamples)
	d.SellTouchProbability, d.SellTouchStdError = jeffreysBernoulliPosterior(weightedUpTouches, effectiveSamples)
	d.BothTouchProbability, d.BothTouchStdError = jeffreysBernoulliPosterior(weightedBothTouches, effectiveSamples)
	// Separate Jeffreys marginals and the joint posterior can differ by one
	// pseudo-count. Project the joint mean onto the Frechet bounds so the
	// Bernoulli covariance remains realizable without changing the established
	// per-side estimators.
	lowerJoint := math.Max(0, d.BuyTouchProbability+d.SellTouchProbability-1)
	upperJoint := math.Min(d.BuyTouchProbability, d.SellTouchProbability)
	d.BothTouchProbability = math.Max(lowerJoint, math.Min(upperJoint, d.BothTouchProbability))
	d.TouchCovariance = d.BothTouchProbability - d.BuyTouchProbability*d.SellTouchProbability
	d.NetRoundTripEdgeBps = grossQuoteEdgeBps - 2*c.MakerFeeBps - 2*c.AdverseSelectionBps - c.MinimumNetEdgeBps
	edge := math.Max(0, d.NetRoundTripEdgeBps)
	if horizonHours := horizon.Hours(); horizonHours > 0 {
		d.ScoreBpsPerHour = math.Min(d.BuyTouchProbability, d.SellTouchProbability) / horizonHours * edge
		d.ScoreStdErrorBpsHour = math.Max(d.BuyTouchStdError, d.SellTouchStdError) / horizonHours * edge
	}
	d.Reason = "max fee-adjusted two-sided edge per hour"
	m.storeCrossingDecision(now, cacheKey, d)
	return d
}

func jeffreysBernoulliPosterior(weightedSuccesses, effectiveSamples float64) (mean, standardError float64) {
	if effectiveSamples < 0 {
		effectiveSamples = 0
	}
	weightedSuccesses = math.Max(0, math.Min(effectiveSamples, weightedSuccesses))
	alpha := 0.5 + weightedSuccesses
	beta := 0.5 + effectiveSamples - weightedSuccesses
	total := alpha + beta
	mean = alpha / total
	variance := alpha * beta / (total * total * (total + 1))
	return mean, math.Sqrt(math.Max(0, variance))
}

func (m *MarketMakerHorizonModel) Update(now time.Time, c MarketMakerConfig, volatilityBpsPerSqrtSec float64) MarketMakerHorizonDecision {
	return m.UpdateForBook(now, c, volatilityBpsPerSqrtSec, 0, 0)
}

// UpdateForBook selects a horizon with side-specific executable distances. The
// legacy Update wrapper remains for trade-only replay callers without a BBO.
func (m *MarketMakerHorizonModel) UpdateForBook(
	now time.Time,
	c MarketMakerConfig,
	volatilityBpsPerSqrtSec, bestBid, bestAsk float64,
) MarketMakerHorizonDecision {
	c.setDefaults()
	updateBucket := now.UTC().Truncate(time.Duration(c.HorizonUpdateInterval))
	if m.lastUpdate.Equal(updateBucket) && m.decision.Horizon > 0 {
		return m.decision
	}
	m.lastUpdate = updateBucket
	best := MarketMakerHorizonDecision{Reason: "insufficient completed horizon samples", UpdatedAt: now}
	for _, horizon := range c.TradingHorizons() {
		decision := m.DecisionForHorizon(now, c, volatilityBpsPerSqrtSec, bestBid, bestAsk, horizon)
		decision.SelectionScoreBpsPerHour = decision.ScoreBpsPerHour
		decision = applyLifecycleAwareHorizonUtility(decision, c)
		if decision.HasSufficientCrossings(c.HorizonMinSamples) &&
			(best.Horizon == 0 || decision.SelectionScoreBpsPerHour > best.SelectionScoreBpsPerHour) {
			best = decision
		}
	}
	if best.Horizon == 0 {
		horizon := time.Duration(c.MinTradingWindow)
		halfSpread := c.HalfSpreadForHorizon(horizon, volatilityBpsPerSqrtSec)
		buyDistance, sellDistance, grossEdge := neutralMakerTouchDistances(bestBid, bestAsk, halfSpread)
		best = m.CrossingDecisionAtSideDistances(now, c, horizon, buyDistance, sellDistance, grossEdge)
		best.SelectionScoreBpsPerHour = best.ScoreBpsPerHour
		best = applyLifecycleAwareHorizonUtility(best, c)
	}
	m.decision = best
	m.publishExecutableDownside(updateBucket, best.Horizon)
	return best
}

// UpdateForBookAdaptiveVolatility selects among trading horizons using each
// horizon's own executable ask/bid volatility estimate.  This avoids choosing
// the quote horizon with volatility inherited from a separately selected Fast
// direction window.  Health remains an admissibility condition; the objective
// remains fee-adjusted two-sided crossing edge per unit time.
func (m *MarketMakerHorizonModel) UpdateForBookAdaptiveVolatility(
	now time.Time,
	c MarketMakerConfig,
	bestBid, bestAsk float64,
) MarketMakerHorizonDecision {
	return m.updateForBookAdaptiveVolatility(now, c, bestBid, bestAsk, nil)
}

// UpdateForBookAdaptiveVolatilityWithMarginalBuy selects the horizon using
// both the original two-sided crossing edge and the conservative positive
// option value of one executable BUY. Because CE is normalized by pair equity and
// horizon length, both terms are bps/hour and require no hand-tuned mixing
// weight. Price and final quantity remain owned by the downstream unified
// Fast optimizer.
func (m *MarketMakerHorizonModel) UpdateForBookAdaptiveVolatilityWithMarginalBuy(
	now time.Time,
	c MarketMakerConfig,
	bestBid, bestAsk float64,
	in FastHorizonMarginalBuyInput,
) MarketMakerHorizonDecision {
	return m.updateForBookAdaptiveVolatility(now, c, bestBid, bestAsk, &in)
}

func (m *MarketMakerHorizonModel) updateForBookAdaptiveVolatility(
	now time.Time,
	c MarketMakerConfig,
	bestBid, bestAsk float64,
	marginalBuy *FastHorizonMarginalBuyInput,
) MarketMakerHorizonDecision {
	c.setDefaults()
	updateBucket := now.UTC().Truncate(time.Duration(c.HorizonUpdateInterval))
	if m.lastUpdate.Equal(updateBucket) && m.decision.Horizon > 0 {
		return m.decision
	}
	m.lastUpdate = updateBucket
	prior := m.EmpiricalSideVolatilityEstimate(now, time.Duration(c.HorizonLookback))
	volatilityFor := func(horizon time.Duration) float64 {
		live := m.EmpiricalSideVolatilityEstimate(now, horizon)
		buy, _ := ShrinkVolatility(
			live.BuyBps, prior.BuyBps, live.BuySamples, c.HorizonMinSamples)
		sell, _ := ShrinkVolatility(
			live.SellBps, prior.SellBps, live.SellSamples, c.HorizonMinSamples)
		return math.Max(buy, sell)
	}
	best := MarketMakerHorizonDecision{Reason: "insufficient completed horizon samples", UpdatedAt: now}
	for _, horizon := range c.TradingHorizons() {
		decision := m.DecisionForHorizon(
			now, c, volatilityFor(horizon), bestBid, bestAsk, horizon)
		decision.SelectionScoreBpsPerHour = decision.ScoreBpsPerHour
		if marginalBuy != nil && decision.HasSufficientCrossings(c.HorizonMinSamples) {
			stats := m.JointPathPayoffStatistics(
				now, c, horizon,
				decision.BuyTouchDistanceBps, decision.SellTouchDistanceBps)
			if c.JointDistanceQuantity.PathUtilityHorizonSelection {
				decision = scoreFastHorizonWithPathUtility(decision, stats, *marginalBuy)
			} else {
				decision = scoreFastHorizonWithMarginalBuy(decision, stats, *marginalBuy)
			}
		}
		decision = applyLifecycleAwareHorizonUtility(decision, c)
		if decision.HasSufficientCrossings(c.HorizonMinSamples) &&
			(best.Horizon == 0 || decision.SelectionScoreBpsPerHour > best.SelectionScoreBpsPerHour) {
			best = decision
		}
	}
	if best.Horizon == 0 {
		horizon := time.Duration(c.MinTradingWindow)
		volatility := volatilityFor(horizon)
		halfSpread := c.HalfSpreadForHorizon(horizon, volatility)
		buyDistance, sellDistance, grossEdge := neutralMakerTouchDistances(
			bestBid, bestAsk, halfSpread)
		best = m.CrossingDecisionAtSideDistances(
			now, c, horizon, buyDistance, sellDistance, grossEdge)
		best.SelectionScoreBpsPerHour = best.ScoreBpsPerHour
		if marginalBuy != nil && best.HasSufficientCrossings(c.HorizonMinSamples) {
			stats := m.JointPathPayoffStatistics(
				now, c, horizon, best.BuyTouchDistanceBps, best.SellTouchDistanceBps)
			if c.JointDistanceQuantity.PathUtilityHorizonSelection {
				best = scoreFastHorizonWithPathUtility(best, stats, *marginalBuy)
			} else {
				best = scoreFastHorizonWithMarginalBuy(best, stats, *marginalBuy)
			}
		}
		best = applyLifecycleAwareHorizonUtility(best, c)
	}
	m.decision = best
	m.publishExecutableDownside(updateBucket, best.Horizon)
	return best
}

// DecisionForHorizon selects the statistically best executable distance for a
// fixed resting horizon. Quote lifetime remains that exact reference horizon;
// the first-passage characteristic time is diagnostic only.
func (m *MarketMakerHorizonModel) DecisionForHorizon(
	now time.Time,
	c MarketMakerConfig,
	volatilityBpsPerSqrtSec, bestBid, bestAsk float64,
	horizon time.Duration,
) MarketMakerHorizonDecision {
	c.setDefaults()
	halfSpread := c.HalfSpreadForHorizon(horizon, volatilityBpsPerSqrtSec)
	buyDistance, sellDistance, grossEdge := neutralMakerTouchDistances(bestBid, bestAsk, halfSpread)
	return m.CrossingDecisionAtSideDistances(now, c, horizon, buyDistance, sellDistance, grossEdge)
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
// volatility. The lower bound prevents ordinary cancel churn; the upper bound
// keeps a stale quote from resting indefinitely. A separate configured
// transport floor is used only by confidence-supported one-sided target
// realignment. Volatility is expressed in bps per square-root second, matching
// GammaCaptureVolatility*10_000.
func (c MarketMakerConfig) RefreshIntervals(halfSpreadBps, volatilityBpsPerSqrtSec float64) (time.Duration, time.Duration) {
	c.setDefaults()
	minRefresh := time.Duration(c.MinRefreshInterval)
	maxRefresh := time.Duration(c.RefreshInterval)
	if maxRefresh <= 0 {
		maxRefresh = time.Minute
	}
	if halfSpreadBps > 0 && volatilityBpsPerSqrtSec > 0 {
		expectedSeconds := math.Pow(halfSpreadBps/volatilityBpsPerSqrtSec, 2)
		// Reprice ordinary quotes no more often than roughly one quarter of the
		// expected crossing time, and force a refresh no later than that time.
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

// MarketMakerOrderKeepDecision maps the continuous first-passage clock to a
// configured horizon for which the strategy can estimate crossing arrivals.
// Duration is never shorter than SelectedHorizon: selecting a 15-minute
// arrival model and cancelling its order after five minutes would invalidate
// the estimated touch probability.
type MarketMakerOrderKeepDecision struct {
	Duration                       time.Duration
	SelectedHorizon                time.Duration
	CharacteristicFirstPassageTime time.Duration
	QuoteDistanceBps               float64
	VolatilityBpsPerSqrtSec        float64
	Reason                         string
}

// OrderKeepDistanceBps validates the actual executable-BBO-to-order distance.
// MaximumHalfSpreadBps is a quote construction cap, not a minimum lifecycle
// distance; using it here inflated ordinary 15-30 bps quotes to 80 bps and
// produced meaningless multi-day first-passage diagnostics.
func (c MarketMakerConfig) OrderKeepDistanceBps(selectedDistanceBps float64) float64 {
	c.setDefaults()
	if selectedDistanceBps > 0 && !math.IsNaN(selectedDistanceBps) && !math.IsInf(selectedDistanceBps, 0) {
		return selectedDistanceBps
	}
	return c.MinimumHalfSpreadBps
}

// DynamicOrderKeepDecision gives an order exactly the horizon whose empirical
// crossing probability priced it.  The characteristic first-passage time is
// retained only as a diagnostic: changing a 10-minute Bernoulli experiment
// into a 15/30-minute lease would mix different probability models.
func (c MarketMakerConfig) DynamicOrderKeepDecision(selectedHorizon time.Duration, quoteDistanceBps, volatilityBpsPerSqrtSec float64) MarketMakerOrderKeepDecision {
	c.setDefaults()
	if selectedHorizon <= 0 {
		selectedHorizon = time.Duration(c.MinTradingWindow)
	}
	decision := MarketMakerOrderKeepDecision{
		Duration: selectedHorizon, SelectedHorizon: selectedHorizon,
		QuoteDistanceBps: quoteDistanceBps, VolatilityBpsPerSqrtSec: volatilityBpsPerSqrtSec,
		Reason: "selected statistical horizon",
	}
	if quoteDistanceBps > 0 && volatilityBpsPerSqrtSec > 0 {
		seconds := math.Pow(quoteDistanceBps/volatilityBpsPerSqrtSec, 2)
		if !math.IsNaN(seconds) && !math.IsInf(seconds, 0) && seconds > 0 {
			firstPassage := time.Duration(seconds * float64(time.Second))
			decision.CharacteristicFirstPassageTime = firstPassage
		}
	}
	return decision
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
	MidPrice                           float64
	BestBid                            float64
	BestAsk                            float64
	VolatilityBps                      float64
	VolatilityPerSqrtSec               float64 // conservative common-risk fallback
	BuyVolatilityPerSqrtSec            float64 // ask-path execution volatility
	SellVolatilityPerSqrtSec           float64 // bid-path execution volatility
	TradingHorizonSeconds              float64
	ArrivalBuyTouchDistanceBps         float64
	ArrivalSellTouchDistanceBps        float64
	Inventory                          float64
	InventoryMin                       float64
	InventoryMax                       float64
	HardInventoryMin                   float64
	HardInventoryMax                   float64
	DirectionSignal                    float64
	VolumeSignal                       float64
	BookImbalance                      float64
	BuyFillRate                        float64
	SellFillRate                       float64
	QuoteNotionalBase                  float64
	SideDistanceBias                   float64
	FastDrift                          FastDriftDecision
	AcquisitionDriftBps                float64
	AcquisitionVolatilityPerSqrtSecBps float64
	AcquisitionHorizonSeconds          float64
	AcquisitionDirection               float64
	InventoryActuationDirection        int
	InventoryActuationStrength         float64
	CanBuy                             bool
	CanSell                            bool
}

type MarketMakerQuotePlan struct {
	BidPrice                       float64
	AskPrice                       float64
	BidDistanceBps                 float64 // midpoint to bid quote
	AskDistanceBps                 float64 // midpoint to ask quote
	BidTouchDistanceBps            float64 // current ask to bid quote
	AskTouchDistanceBps            float64 // ask quote to current bid
	BidHalfSpreadBps               float64
	AskHalfSpreadBps               float64
	HalfSpreadBps                  float64 // conservative maximum of both sides
	InventorySkew                  float64
	ReservationShiftBps            float64
	FastDriftApplied               bool
	FastDriftMeanBps               float64
	FastDriftRawMeanBps            float64
	FastDriftStrength              float64
	FastDriftValidationProbability float64
	FastDriftVarianceBps2          float64
	FastDriftSamples               int
	FastDriftValidationSamples     int
	FastDriftPrequentialSkill      float64
	FastDriftReason                string
	SidePressure                   float64
	BidQuoteNotional               float64
	AskQuoteNotional               float64
	BidQuoteFactor                 float64
	AskQuoteFactor                 float64
	AskEquityFloor                 float64
	AskNetMarkEdgeBps              float64
	EquityProtected                bool
	AcquisitionDeltaBps            float64
	AcquisitionTouchProbability    float64
	AcquisitionApplied             bool
	InventoryActuationDirection    int
	InventoryActuationStrength     float64
	InventoryActuationInwardBps    float64
	InventoryActuationFloorBps     float64
	AllowBid                       bool
	AllowAsk                       bool
	Reason                         string
}

func jointEvidenceSignal(direction, imbalance float64) float64 {
	direction = math.Max(-0.999, math.Min(0.999, direction))
	imbalance = math.Max(-0.999, math.Min(0.999, imbalance))
	return math.Tanh(math.Atanh(direction) + math.Atanh(imbalance))
}

func jointFillSignal(buyRate, sellRate float64) float64 {
	buyRate = math.Max(0, buyRate)
	sellRate = math.Max(0, sellRate)
	return math.Tanh(math.Log1p(buyRate) - math.Log1p(sellRate))
}

func standardNormalCDF(x float64) float64 {
	return 0.5 * math.Erfc(-x/math.Sqrt2)
}

// brownianLowerBarrierTouchProbability is the first-passage probability for
// X(t)=mu*t+sigma*W(t) to reach a lower barrier a within T. Distances and
// drift are bps; sigma is bps/sqrt(second).
func brownianLowerBarrierTouchProbability(distanceBps, driftBps, volatilityPerSqrtSecBps, horizonSeconds float64) float64 {
	if distanceBps <= 0 {
		return 1
	}
	if volatilityPerSqrtSecBps <= 0 || horizonSeconds <= 0 {
		if driftBps <= 0 {
			return 1
		}
		return 0
	}
	sigmaT := volatilityPerSqrtSecBps * math.Sqrt(horizonSeconds)
	first := standardNormalCDF((-distanceBps - driftBps) / sigmaT)
	exponent := -2 * driftBps * distanceBps / (volatilityPerSqrtSecBps * volatilityPerSqrtSecBps * horizonSeconds)
	secondWeight := 0.0
	if exponent > 700 {
		if driftBps < 0 {
			return 1
		}
		secondWeight = math.Inf(1)
	} else {
		secondWeight = math.Exp(exponent)
	}
	second := secondWeight * standardNormalCDF((-distanceBps+driftBps)/sigmaT)
	probability := first + second
	if math.IsNaN(probability) {
		return 0
	}
	return math.Max(0, math.Min(1, probability))
}

// acquisitionQuoteDeltaBps returns a confidence-bounded bid improvement and
// the resulting lower-barrier touch probability.
func acquisitionQuoteDeltaBps(distanceBps, maxAllowedDeltaBps, driftBps, volatilityPerSqrtSecBps, horizonSeconds float64, cfg AcquisitionQuoteConfig) (float64, float64) {
	cfg.setDefaults()
	if distanceBps <= 0 || maxAllowedDeltaBps <= 0 || driftBps <= 0 || volatilityPerSqrtSecBps <= 0 || horizonSeconds <= 0 {
		return 0, brownianLowerBarrierTouchProbability(distanceBps, driftBps, volatilityPerSqrtSecBps, horizonSeconds)
	}
	confidenceMove := driftBps - cfg.DriftConfidenceZScore*volatilityPerSqrtSecBps*math.Sqrt(horizonSeconds)
	if confidenceMove < cfg.MinDriftBps {
		return 0, brownianLowerBarrierTouchProbability(distanceBps, driftBps, volatilityPerSqrtSecBps, horizonSeconds)
	}
	delta := math.Min(cfg.MaxDeltaBps, math.Min(maxAllowedDeltaBps, confidenceMove))
	if delta <= 0 {
		return 0, brownianLowerBarrierTouchProbability(distanceBps, driftBps, volatilityPerSqrtSecBps, horizonSeconds)
	}
	return delta, brownianLowerBarrierTouchProbability(distanceBps-delta, driftBps, volatilityPerSqrtSecBps, horizonSeconds)
}

// acquisitionDriftForecast converts causal fast evidence into a horizon-scaled
// drift forecast. The two rates use the selected evidence window and its 5m
// subwindow; direction acts as a posterior-confidence attenuation.
func acquisitionDriftForecast(evidence FastEvidenceSnapshot, direction float64, horizon time.Duration) (float64, float64) {
	if evidence.Health != HealthHealthy || direction <= 0 || horizon <= 0 {
		return 0, 0
	}
	windowSeconds := evidence.Observed.Seconds()
	if windowSeconds <= 0 {
		windowSeconds = (5 * time.Minute).Seconds()
	}
	if windowSeconds < 1 {
		windowSeconds = 1
	}
	rateWindow := evidence.MidReturnBps / windowSeconds
	rateFiveMinute := evidence.MidReturn5mBps / (5 * time.Minute).Seconds()
	rate := 0.5 * (rateWindow + rateFiveMinute)
	confidence := math.Max(0, math.Min(1, direction))
	drift := math.Max(0, rate*float64(horizon.Seconds())*confidence)
	return drift, evidence.BuyExecutionVolatility5mBps()
}

// equityProtectedAskFloor returns the minimum maker sell price whose proceeds
// increase mark-to-market equity after fees and the configured adverse-selection
// allowance. Unlike an accounting-cost floor, it is observable before the first
// fill and follows the live mark downward as inventory risk changes.
func equityProtectedAskFloor(markPrice, makerFeeBps, adverseSelectionBps, minimumRoundTripEdgeBps float64) float64 {
	if markPrice <= 0 {
		return 0
	}
	feeRate := math.Max(0, makerFeeBps) / 10_000
	if feeRate >= 1 {
		return 0
	}
	requiredNetMarkEdgeBps := math.Max(0, adverseSelectionBps) + math.Max(0, minimumRoundTripEdgeBps)/2
	return markPrice * math.Exp(requiredNetMarkEdgeBps/10_000) / (1 - feeRate)
}

// Quote computes a conservative two-sided maker-only quote. A positive
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
	buyVolatility := in.BuyVolatilityPerSqrtSec
	if buyVolatility <= 0 {
		buyVolatility = in.VolatilityPerSqrtSec
	}
	sellVolatility := in.SellVolatilityPerSqrtSec
	if sellVolatility <= 0 {
		sellVolatility = in.VolatilityPerSqrtSec
	}
	halfForVolatility := func(sideVolatility float64) float64 {
		vol := math.Max(0, in.VolatilityBps) * c.VolatilityMultiplier
		half := math.Max(c.MinimumHalfSpreadBps, baseEdge+vol)
		if in.TradingHorizonSeconds > 0 && sideVolatility > 0 {
			horizon := time.Duration(in.TradingHorizonSeconds * float64(time.Second))
			half = c.HalfSpreadForHorizon(horizon, sideVolatility)
		} else if sideVolatility > 0 {
			// Solve the quote-distance/time-scale fixed point per executable
			// side; ask-path and bid-path volatility need not be equal.
			half = math.Max(c.MinimumHalfSpreadBps, baseEdge)
			for i := 0; i < 8; i++ {
				_, horizon := c.RefreshIntervals(half, sideVolatility)
				move := sideVolatility * math.Sqrt(horizon.Seconds()) * c.VolatilityMultiplier
				candidate := math.Max(c.MinimumHalfSpreadBps, baseEdge+move)
				candidate = math.Min(candidate, c.MaximumHalfSpreadBps)
				if math.Abs(candidate-half) < 1e-6 {
					half = candidate
					break
				}
				half = candidate
			}
		}
		return math.Min(half, c.MaximumHalfSpreadBps)
	}
	bidHalf := halfForVolatility(buyVolatility)
	askHalf := halfForVolatility(sellVolatility)
	// When every distance bucket has sufficient online exposure, the arrival
	// model selects the common fee-adjusted edge optimum. Convert its executable
	// ask-to-bid and bid-to-ask distances back to midpoint half-spreads before
	// applying the unified inventory/direction reservation shift.
	if in.ArrivalBuyTouchDistanceBps > 0 {
		arrivalBid := in.BestAsk * math.Exp(-in.ArrivalBuyTouchDistanceBps/10_000)
		if arrivalBid > 0 && arrivalBid < in.MidPrice {
			bidHalf = math.Max(sideFloor, math.Min(c.MaximumHalfSpreadBps, math.Log(in.MidPrice/arrivalBid)*10_000))
		}
	}
	if in.ArrivalSellTouchDistanceBps > 0 {
		arrivalAsk := in.BestBid * math.Exp(in.ArrivalSellTouchDistanceBps/10_000)
		if arrivalAsk > in.MidPrice {
			askHalf = math.Max(sideFloor, math.Min(c.MaximumHalfSpreadBps, math.Log(arrivalAsk/in.MidPrice)*10_000))
		}
	}
	half := math.Max(bidHalf, askHalf)
	lowerInventory := c.InventoryTarget - c.InventoryLimit
	upperInventory := c.InventoryTarget + c.InventoryLimit
	if in.InventoryMax > in.InventoryMin {
		lowerInventory = in.InventoryMin
		upperInventory = in.InventoryMax
	}
	hardLowerInventory, hardUpperInventory := lowerInventory, upperInventory
	if in.HardInventoryMax > in.HardInventoryMin {
		hardLowerInventory = in.HardInventoryMin
		hardUpperInventory = in.HardInventoryMax
	}
	limit := c.InventoryLimit
	if in.Inventory >= c.InventoryTarget && upperInventory > c.InventoryTarget {
		limit = upperInventory - c.InventoryTarget
	} else if in.Inventory < c.InventoryTarget && lowerInventory < c.InventoryTarget {
		limit = c.InventoryTarget - lowerInventory
	}
	if limit <= 0 {
		limit = 1
	}
	ratio := (in.Inventory - c.InventoryTarget) / limit
	ratio = math.Max(-1, math.Min(1, ratio))
	// Inventory, directional evidence, book imbalance, and side hazards are
	// converted once into one signed pressure for reservation-price and spread
	// placement. Quantity is projected later from fill probabilities and Macro risk.
	evidenceSignal := jointEvidenceSignal(in.DirectionSignal, in.BookImbalance)
	if math.Abs(in.VolumeSignal) > 1e-9 {
		evidenceSignal = jointEvidenceSignal(evidenceSignal, in.VolumeSignal)
	}
	fillSignal := jointFillSignal(in.BuyFillRate, in.SellFillRate)
	sidePressure := ratio - evidenceSignal + fillSignal
	moveScaleBps := math.Max(0, in.VolatilityBps)
	if in.TradingHorizonSeconds > 0 {
		moveScaleBps = math.Max(buyVolatility, sellVolatility) * math.Sqrt(in.TradingHorizonSeconds)
	}
	if moveScaleBps <= 0 {
		moveScaleBps = half
	}
	fastDriftApplied := c.FastDrift.Enabled && !c.FastDrift.ShadowOnly &&
		in.FastDrift.Enabled && in.FastDrift.Healthy
	if fastDriftApplied {
		// The learned conditional mean replaces the heuristic evidence shift;
		// applying both would use the same Fast crossing/book state twice. The
		// conditional-mean uncertainty conservatively replaces only a smaller
		// horizon risk scale. Return innovation variance remains owned by side QV.
		evidenceSignal = 0
		sidePressure = ratio + fillSignal
		moveScaleBps = math.Sqrt(math.Max(
			moveScaleBps*moveScaleBps, in.FastDrift.CenterVarianceBps2))
	}
	// Project the unified pressure into both side-specific admissible intervals.
	// The intersection preserves the risk signal without forcing either side
	// beyond its configured floor/cap.
	rawShiftBps := sidePressure * moveScaleBps
	if fastDriftApplied {
		rawShiftBps -= in.FastDrift.CenterMeanBps
	}
	// Clamp each projection independently. At a fee floor one side can remain
	// fixed while the opposite side widens; forcing a symmetric intersection
	// would erase directional risk precisely when the other side hits the floor.
	normalBidBps := math.Max(sideFloor, math.Min(c.MaximumHalfSpreadBps, bidHalf+rawShiftBps))
	normalAskBps := math.Max(sideFloor, math.Min(c.MaximumHalfSpreadBps, askHalf-rawShiftBps))
	bidFloor, askFloor := sideFloor, sideFloor
	bidInwardDeltaBps, askInwardDeltaBps := 0.0, 0.0
	actuationStrength := math.Max(0, math.Min(1, in.InventoryActuationStrength))
	roundTripFloor := 2*c.MakerFeeBps + 2*c.AdverseSelectionBps + c.MinimumNetEdgeBps
	executionFloor := math.Max(0, c.MakerFeeBps+c.AdverseSelectionBps)
	if actuationStrength > 0 {
		switch {
		case in.InventoryActuationDirection > 0:
			// Spend only edge already carried by the opposite quote. The target
			// bid retains its own fee plus adverse-selection execution floor.
			economicFloor := math.Max(executionFloor, roundTripFloor-normalAskBps)
			economicFloor = math.Min(sideFloor, economicFloor)
			bidFloor = sideFloor - actuationStrength*(sideFloor-economicFloor)
			bidInwardDeltaBps = sideFloor - bidFloor
		case in.InventoryActuationDirection < 0:
			economicFloor := math.Max(executionFloor, roundTripFloor-normalBidBps)
			economicFloor = math.Min(sideFloor, economicFloor)
			askFloor = sideFloor - actuationStrength*(sideFloor-economicFloor)
			askInwardDeltaBps = sideFloor - askFloor
		}
	}
	bidBps := math.Max(bidFloor, math.Min(c.MaximumHalfSpreadBps, bidHalf+rawShiftBps-bidInwardDeltaBps))
	askBps := math.Max(askFloor, math.Min(c.MaximumHalfSpreadBps, askHalf-rawShiftBps-askInwardDeltaBps))
	if deficit := roundTripFloor - bidBps - askBps; deficit > 0 {
		// Preserve the full round-trip economics by widening the non-target
		// side instead of undoing the statistically requested inward move.
		if in.InventoryActuationDirection > 0 {
			askBps = math.Min(c.MaximumHalfSpreadBps, askBps+deficit)
		} else {
			bidBps = math.Min(c.MaximumHalfSpreadBps, bidBps+deficit)
		}
	}
	shiftBps := ((bidBps - bidHalf) + (askHalf - askBps)) / 2
	plan.BidPrice = in.MidPrice * math.Exp(-bidBps/10_000)
	plan.AskPrice = in.MidPrice * math.Exp(askBps/10_000)
	if in.CanSell && in.Inventory > 0 {
		plan.AskEquityFloor = equityProtectedAskFloor(
			in.MidPrice, c.MakerFeeBps, c.AdverseSelectionBps, c.MinimumNetEdgeBps)
		if plan.AskPrice < plan.AskEquityFloor {
			plan.AskPrice = plan.AskEquityFloor
			askBps = math.Log(plan.AskPrice/in.MidPrice) * 10_000
			plan.EquityProtected = true
		}
		feeRate := math.Max(0, c.MakerFeeBps) / 10_000
		if feeRate < 1 {
			plan.AskNetMarkEdgeBps = math.Log(plan.AskPrice*(1-feeRate)/in.MidPrice)*10_000 - c.AdverseSelectionBps
		}
	}
	// Never submit a quote that could execute immediately against the observed
	// BBO.  The one-tick adjustment belongs to the exchange formatter.
	if plan.BidPrice >= in.BestAsk {
		plan.BidPrice = math.Nextafter(in.BestAsk, 0)
	}
	if plan.AskPrice <= in.BestBid {
		plan.AskPrice = math.Nextafter(in.BestBid, math.Inf(1))
	}
	acquisitionCfg := c.AcquisitionQuote
	if acquisitionCfg.Enabled && in.CanBuy && in.Inventory < upperInventory && in.AcquisitionDirection >= acquisitionCfg.MinDirection &&
		in.AcquisitionDriftBps > 0 && in.AcquisitionVolatilityPerSqrtSecBps > 0 && in.AcquisitionHorizonSeconds > 0 {
		touchDistance := math.Log(in.BestAsk/plan.BidPrice) * 10_000
		currentBidDistance := math.Log(in.MidPrice/plan.BidPrice) * 10_000
		maxDelta := math.Max(0, currentBidDistance-sideFloor)
		delta, probability := acquisitionQuoteDeltaBps(touchDistance, maxDelta, in.AcquisitionDriftBps,
			in.AcquisitionVolatilityPerSqrtSecBps, in.AcquisitionHorizonSeconds, acquisitionCfg)
		plan.AcquisitionDeltaBps = delta
		plan.AcquisitionTouchProbability = probability
		if delta > 0 && !acquisitionCfg.ShadowOnly {
			plan.BidPrice *= math.Exp(delta / 10_000)
			if plan.BidPrice >= in.BestAsk {
				plan.BidPrice = math.Nextafter(in.BestAsk, 0)
			}
			plan.AcquisitionApplied = plan.BidPrice > in.MidPrice*math.Exp(-bidBps/10_000)
			if plan.AcquisitionApplied {
				bidBps = math.Log(in.MidPrice/plan.BidPrice) * 10_000
			}
		}
	}
	plan.BidDistanceBps = math.Log(in.MidPrice/plan.BidPrice) * 10_000
	plan.AskDistanceBps = math.Log(plan.AskPrice/in.MidPrice) * 10_000
	plan.BidTouchDistanceBps, plan.AskTouchDistanceBps, _ = MakerTouchDistances(
		in.BestBid, in.BestAsk, plan.BidPrice, plan.AskPrice)
	plan.BidHalfSpreadBps = bidBps
	plan.AskHalfSpreadBps = askBps
	plan.HalfSpreadBps = math.Max(bidBps, askBps)
	plan.InventorySkew = -ratio * moveScaleBps
	plan.ReservationShiftBps = -shiftBps
	plan.FastDriftApplied = fastDriftApplied
	plan.FastDriftMeanBps = in.FastDrift.CenterMeanBps
	plan.FastDriftRawMeanBps = in.FastDrift.RawCenterMeanBps
	plan.FastDriftStrength = in.FastDrift.Strength
	plan.FastDriftValidationProbability = in.FastDrift.ValidationProbability
	plan.FastDriftVarianceBps2 = in.FastDrift.CenterVarianceBps2
	plan.FastDriftSamples = in.FastDrift.Samples
	plan.FastDriftValidationSamples = in.FastDrift.ValidationSamples
	plan.FastDriftPrequentialSkill = in.FastDrift.PrequentialSkill
	plan.FastDriftReason = in.FastDrift.Reason
	plan.SidePressure = sidePressure
	plan.InventoryActuationDirection = in.InventoryActuationDirection
	plan.InventoryActuationStrength = actuationStrength
	if in.InventoryActuationDirection > 0 {
		plan.InventoryActuationInwardBps = math.Max(0, normalBidBps-bidBps)
		plan.InventoryActuationFloorBps = bidFloor
	} else if in.InventoryActuationDirection < 0 {
		plan.InventoryActuationInwardBps = math.Max(0, normalAskBps-askBps)
		plan.InventoryActuationFloorBps = askFloor
	}
	plan.BidQuoteFactor = 1
	plan.AskQuoteFactor = 1
	if in.QuoteNotionalBase > 0 {
		plan.BidQuoteNotional = in.QuoteNotionalBase
		plan.AskQuoteNotional = in.QuoteNotionalBase
	}
	plan.AllowBid = in.CanBuy && in.Inventory < hardUpperInventory
	plan.AllowAsk = in.CanSell && in.Inventory > hardLowerInventory
	plan.Reason = "quoted"
	return plan
}

func (c MarketMakerConfig) fastIntensityConfig() IntensityConfig {
	return c.fastIntensityConfigFor(time.Duration(c.FastWindow))
}

func (c MarketMakerConfig) fastIntensityConfigFor(window time.Duration) IntensityConfig {
	if window <= 0 {
		window = time.Minute
	}
	configuredWindow := types.Duration(window)
	return IntensityConfig{
		Window:           configuredWindow,
		VolatilityWindow: configuredWindow,
		PriorAlphaUp:     1,
		PriorBetaUp:      10,
		PriorAlphaDown:   1,
		PriorBetaDown:    10,
		MinEvents:        1,
	}
}
