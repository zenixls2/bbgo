package main

import (
	"encoding/json"
	"math"
	"os"
	"sort"
	"time"

	"github.com/c9s/bbgo/pkg/fixedpoint"
	"github.com/c9s/bbgo/pkg/strategy/gammacapture"
	"github.com/c9s/bbgo/pkg/types"
	"gopkg.in/yaml.v3"
)

type productionComparisonInput struct {
	ConfigPath, ModelPath, DataPath, Symbol      string
	JournalPath                                  string
	ValidationStage                              string
	From, To                                     time.Time
	WarmupFrom                                   time.Time
	PairEquityJPY, StartingBase, QueueMultiplier float64
	CalibrationFrom, CalibrationTo               time.Time
	ActualBuyFills, ActualSellFills              int
	ReplayCacheDir                               string
	BBOInterval                                  time.Duration
	ComponentOnly                                bool
	TargetActionValueOnly                        bool
	PivotRegimeTargetOnly                        bool
	CausalRegimeInventoryTargetOnly              bool
	EnableAsymmetricRisk                         bool
	AsymmetricRiskOnly                           bool
	NormalFlowPressureOnly                       bool
	NormalFlowPressureRiskBudgetScale            float64
	FeeFreeCounterfactualOnly                    bool
	RegimeExpectedValueSizingOnly                bool
	HorizonConditionedUtilityOnly                bool
	AllowUncalibratedReplay                      bool
}

type productionYAML struct {
	ExchangeStrategies []struct {
		GammaCapture struct {
			Symbol      string                         `yaml:"symbol"`
			Barrier     gammacapture.BarrierConfig     `yaml:"barrier"`
			Intensity   gammacapture.IntensityConfig   `yaml:"intensity"`
			MarketMaker gammacapture.MarketMakerConfig `yaml:"marketMaker"`
		} `yaml:"gammacapture"`
	} `yaml:"exchangeStrategies"`
}

type productionConfigOverrides struct {
	InventoryRiskBudgetRatio      float64
	InventoryRiskZScore           float64
	MinimumHalfSpreadBps          float64
	VolatilityMultiplier          float64
	MinimumNetEdgeBps             float64
	MinimumNetEdgeSet             bool
	MaxTradingWindow              time.Duration
	HorizonLookback               time.Duration
	HorizonMinSamples             int
	MacroRiskAversion             float64
	MacroCarryRiskBudget          float64
	MacroBarInterval              time.Duration
	DisableJointDistanceQuantity  bool
	DisableAdaptivePathDecay      bool
	DisableConditionalExecution   bool
	ActivateJointDistanceQuantity bool
	EnablePathUtilityHorizon      bool
	EnableJointHorizonSelection   bool
	JointDistanceCandidateCount   int
	EnableFastDrift               bool
	EnableDynamicInventoryAim     bool
	EnableBOCPD45Direction        bool
	BOCPD45Calibration            string
	EnableQuoteLifecycleAction    bool
}

var activeProductionConfigOverrides productionConfigOverrides

// activeProductionReplayDirectionalTarget is a research-only, causal target
// provider. It alters exactly InventoryDirectionalMeanBps before the existing
// posterior inventory transform; price, crossing, variance, quantity and gate
// estimators remain owned by the production model. Live strategy code never
// consults this hook.
var activeProductionReplayDirectionalTarget *volumeTerminalTargetProvider

func (o productionConfigOverrides) apply(c gammacapture.MarketMakerConfig) gammacapture.MarketMakerConfig {
	if o.InventoryRiskBudgetRatio > 0 {
		c.InventoryRiskBudgetRatio = o.InventoryRiskBudgetRatio
	}
	if o.InventoryRiskZScore > 0 {
		c.InventoryRiskZScore = o.InventoryRiskZScore
	}
	if o.MinimumHalfSpreadBps > 0 {
		c.MinimumHalfSpreadBps = o.MinimumHalfSpreadBps
	}
	if o.VolatilityMultiplier > 0 {
		c.VolatilityMultiplier = o.VolatilityMultiplier
	}
	if o.MinimumNetEdgeSet {
		c.MinimumNetEdgeBps = o.MinimumNetEdgeBps
	}
	if o.MaxTradingWindow > 0 {
		c.MaxTradingWindow = types.Duration(o.MaxTradingWindow)
	}
	if o.HorizonLookback > 0 {
		c.HorizonLookback = types.Duration(o.HorizonLookback)
	}
	if o.HorizonMinSamples > 0 {
		c.HorizonMinSamples = o.HorizonMinSamples
	}
	if o.MacroRiskAversion > 0 {
		c.MacroInventory.RiskAversion = o.MacroRiskAversion
	}
	if o.MacroCarryRiskBudget > 0 {
		c.MacroInventory.CarryRiskBudgetRatio = o.MacroCarryRiskBudget
	}
	if o.MacroBarInterval > 0 {
		c.MacroInventory.BarInterval = types.Duration(o.MacroBarInterval)
	}
	if o.DisableJointDistanceQuantity {
		c.JointDistanceQuantity.Enabled = false
	}
	if o.DisableAdaptivePathDecay {
		c.JointDistanceQuantity.AdaptivePathDecay = false
	}
	if o.DisableConditionalExecution {
		c.ConditionalExecution.Enabled = false
	}
	if o.ActivateJointDistanceQuantity {
		c.JointDistanceQuantity.Enabled = true
		c.JointDistanceQuantity.ShadowOnly = false
	}
	if o.EnablePathUtilityHorizon {
		c.JointDistanceQuantity.PathUtilityHorizonSelection = true
	}
	if o.EnableJointHorizonSelection {
		c.JointDistanceQuantity.JointHorizonSelection = true
	}
	if o.JointDistanceCandidateCount > 0 {
		c.JointDistanceQuantity.CandidateCount = o.JointDistanceCandidateCount
	}
	if o.EnableFastDrift {
		c.FastDrift.Enabled = true
		c.FastDrift.ShadowOnly = false
	}
	if o.EnableDynamicInventoryAim {
		c.DynamicInventoryAim.Enabled = true
		c.DynamicInventoryAim.ShadowOnly = false
	}
	if o.EnableQuoteLifecycleAction {
		c.QuoteLifecycleAction.Enabled = true
		c.QuoteLifecycleAction.ShadowOnly = false
		// Component replay explicitly exercises the new five-minute hazard
		// and uncertainty paths; live YAML remains untouched.
		c.QuoteLifecycleAction.Hazard.Enabled = true
		if c.QuoteLifecycleAction.ActionMarginBps == 0 {
			c.QuoteLifecycleAction.ActionMarginBps = 0.25
		}
		if c.QuoteLifecycleAction.ConfidenceZScore <= 0 {
			c.QuoteLifecycleAction.ConfidenceZScore = 1.645
		}
		if !c.QuoteLifecycleAction.DynamicReplacementCost {
			c.QuoteLifecycleAction.DynamicReplacementCost = true
			if c.QuoteLifecycleAction.ReplacementCostQueueWeight == 0 && c.QuoteLifecycleAction.ReplacementCostDriftWeight == 0 && c.QuoteLifecycleAction.ReplacementCostVolatilityWeight == 0 {
				c.QuoteLifecycleAction.ReplacementCostQueueWeight = 0.25
				c.QuoteLifecycleAction.ReplacementCostDriftWeight = 0.5
				c.QuoteLifecycleAction.ReplacementCostVolatilityWeight = 0.25
			}
			if c.QuoteLifecycleAction.ReplacementCostLatencySeconds == 0 {
				c.QuoteLifecycleAction.ReplacementCostLatencySeconds = 5
			}
		}
	}
	return c
}

type replayPolicyMode string

const (
	replayLegacy           replayPolicyMode = "legacy-direction-fallback"
	replayHorizonTouch     replayPolicyMode = "horizon-touch"
	replayHorizonTouchOff  replayPolicyMode = "horizon-touch-disabled"
	replayAcquisitionReset replayPolicyMode = "statistical-acquisition-reset"
)

type productionReplayDay struct {
	Day       string `json:"day"`
	BuyFills  int    `json:"buyFills"`
	SellFills int    `json:"sellFills"`
}

type productionEquityPoint struct {
	At                             time.Time     `json:"at"`
	MidJPY                         float64       `json:"midJPY"`
	EquityJPY                      float64       `json:"equityJPY"`
	HoldEquityJPY                  float64       `json:"holdEquityJPY"`
	Inventory                      float64       `json:"inventory"`
	RiskyWeight                    float64       `json:"riskyWeight"`
	TargetRatio                    float64       `json:"targetRatio"`
	ReversalDirection              int           `json:"reversalDirection"`
	EarlyReversal                  bool          `json:"earlyReversal"`
	ReversalApplied                bool          `json:"reversalApplied"`
	TrendDirection                 int           `json:"trendDirection,omitempty"`
	TrendModelProbability          float64       `json:"trendModelProbability,omitempty"`
	TrendTerminalReturnBps         float64       `json:"trendTerminalReturnBps,omitempty"`
	TrendExpectedReturnBps         float64       `json:"trendExpectedReturnBps,omitempty"`
	TrendRemainingExcursionBps     float64       `json:"trendRemainingExcursionBps,omitempty"`
	TrendExpectedPivot             time.Duration `json:"trendExpectedPivot,omitempty"`
	ContinuationRecentDirection    int           `json:"continuationRecentDirection,omitempty"`
	ContinuationDirection          int           `json:"continuationDirection,omitempty"`
	ContinuationConsolidationScore float64       `json:"continuationConsolidationScore,omitempty"`
	ContinuationUpProbability      float64       `json:"continuationUpProbability,omitempty"`
	ContinuationDownProbability    float64       `json:"continuationDownProbability,omitempty"`
	ContinuationCensorProbability  float64       `json:"continuationCensorProbability,omitempty"`
	ContinuationDownGivenMove      float64       `json:"continuationDownGivenMove,omitempty"`
	ContinuationDownLower          float64       `json:"continuationDownLower,omitempty"`
	ContinuationExpectedReturnBps  float64       `json:"continuationExpectedReturnBps,omitempty"`
	ContinuationModelProbability   float64       `json:"continuationModelProbability,omitempty"`
	NoTradeRawAimRatio             float64       `json:"noTradeRawAimRatio,omitempty"`
	NoTradeAimRatio                float64       `json:"noTradeAimRatio,omitempty"`
	NoTradeLowerRatio              float64       `json:"noTradeLowerRatio,omitempty"`
	NoTradeUpperRatio              float64       `json:"noTradeUpperRatio,omitempty"`
	NoTradeKalmanGain              float64       `json:"noTradeKalmanGain,omitempty"`
	NoTradeEffectivePriorStrength  float64       `json:"noTradeEffectivePriorStrength,omitempty"`
	NoTradeForecastReturnBps       float64       `json:"noTradeForecastReturnBps,omitempty"`
	FastReservationEnabled         bool          `json:"fastReservationEnabled,omitempty"`
	FastReservationForecastBps     float64       `json:"fastReservationForecastReturnBps,omitempty"`
	FastReservationForecastSEBps   float64       `json:"fastReservationForecastSEBps,omitempty"`
	FastReservationAdverseProb     float64       `json:"fastReservationAdverseProbability,omitempty"`
	FastReservationStrength        float64       `json:"fastReservationStrength,omitempty"`
	FastReservationPathEfficiency  float64       `json:"fastReservationPathEfficiency,omitempty"`
	FastReservationShift           float64       `json:"fastReservationShiftBps,omitempty"`
	NoTradeContinuationCapApplied  bool          `json:"noTradeContinuationCapApplied,omitempty"`
	NoTradeContinuationCapRatio    float64       `json:"noTradeContinuationCapRatio,omitempty"`
}

type productionReplayMacroDecision struct {
	At                     time.Time `json:"at"`
	ClosedBarAt            time.Time `json:"closedBarAt"`
	Direction              int       `json:"direction"`
	Trigger                bool      `json:"trigger"`
	Reason                 string    `json:"reason"`
	Quantity               float64   `json:"quantity"`
	TargetGapBase          float64   `json:"targetGapBase"`
	TacticalTargetGapBase  float64   `json:"tacticalTargetGapBase"`
	ResidualMakerGapBase   float64   `json:"residualMakerGapBase"`
	PassiveMissProbability float64   `json:"passiveMissProbability"`
	UrgentFraction         float64   `json:"urgentFraction"`
	WaitLossBps            float64   `json:"waitLossBps"`
	CrossingCostBps        float64   `json:"crossingCostBps"`
}

type replayFastTargetDecision struct {
	At                     time.Time `json:"at"`
	ModelUpdatedAt         time.Time `json:"modelUpdatedAt"`
	Direction              int       `json:"direction"`
	Trigger                bool      `json:"trigger"`
	Reason                 string    `json:"reason"`
	Quantity               float64   `json:"quantity"`
	TargetGapBase          float64   `json:"targetGapBase"`
	ResidualMakerGapBase   float64   `json:"residualMakerGapBase"`
	TouchProbability       float64   `json:"touchProbability"`
	TouchProbabilityUpper  float64   `json:"touchProbabilityUpper"`
	MissProbabilityLower   float64   `json:"missProbabilityLower"`
	ExpectedAdverseMoveBps float64   `json:"expectedAdverseMoveBps"`
	WaitLossBps            float64   `json:"waitLossBps"`
	CrossingCostBps        float64   `json:"crossingCostBps"`
	DownsideActive         bool      `json:"downsideActive"`
	DownsideEValue         float64   `json:"downsideEValue"`
	DownsideForecastBps    float64   `json:"downsideForecastBps"`
	UpsideActive           bool      `json:"upsideActive"`
	UpsideEValue           float64   `json:"upsideEValue"`
	UpsideForecastBps      float64   `json:"upsideForecastBps"`
	VariancePenaltyBps     float64   `json:"variancePenaltyBps"`
	ActiveCEBps            float64   `json:"activeCEBps"`
}

type productionReplayResult struct {
	Mode                                replayPolicyMode `json:"mode"`
	From, To                            time.Time
	ActiveHours                         float64                                      `json:"activeHours"`
	BBOEvents                           int                                          `json:"bboEvents"`
	AggTradeEvents                      int                                          `json:"aggTradeEvents"`
	DataGaps                            int                                          `json:"dataGaps"`
	QueueMultiplier                     float64                                      `json:"queueMultiplier"`
	BOCPD45Calibration                  string                                       `json:"bocpd45Calibration,omitempty"`
	BOCPD45MatureCalibrationSamples     int                                          `json:"bocpd45MatureCalibrationSamples,omitempty"`
	QuoteRefreshes                      int                                          `json:"quoteRefreshes"`
	FullFills                           int                                          `json:"fullFills"`
	BuyFills                            int                                          `json:"buyFills"`
	SellFills                           int                                          `json:"sellFills"`
	RoundTrips                          int                                          `json:"roundTrips"`
	FillEvents                          []replayFill                                 `json:"fillEvents,omitempty"`
	FastInventoryZoneDecisions          int                                          `json:"fastInventoryZoneDecisions"`
	LongHorizonAdjustmentDecisions      int                                          `json:"longHorizonAdjustmentDecisions"`
	FastReservationEvaluations          int                                          `json:"fastReservationEvaluations"`
	FastReservationApplied              int                                          `json:"fastReservationApplied"`
	FastReservationReasons              map[string]int                               `json:"fastReservationReasons,omitempty"`
	FastTargetSwitchEvaluations         int                                          `json:"fastTargetSwitchEvaluations"`
	FastTargetSwitchApplied             int                                          `json:"fastTargetSwitchApplied"`
	FastTargetSwitchRetained            int                                          `json:"fastTargetSwitchRetained"`
	FastTargetSwitchReasons             map[string]int                               `json:"fastTargetSwitchReasons,omitempty"`
	FastTargetSwitchNetValueSumJPY      float64                                      `json:"fastTargetSwitchNetValueSumJPY"`
	DynamicInventoryAimEvaluations      int                                          `json:"dynamicInventoryAimEvaluations,omitempty"`
	DynamicInventoryAimPassed           int                                          `json:"dynamicInventoryAimPassed,omitempty"`
	DynamicInventoryAimApplied          int                                          `json:"dynamicInventoryAimApplied,omitempty"`
	DynamicInventoryAimReasons          map[string]int                               `json:"dynamicInventoryAimReasons,omitempty"`
	DynamicInventoryAimAccuracy         dynamicInventoryAimAccuracyReport            `json:"dynamicInventoryAimAccuracy,omitempty"`
	CausalRegimeTargetEvaluations       int                                          `json:"causalRegimeTargetEvaluations,omitempty"`
	CausalRegimeTargetReady             int                                          `json:"causalRegimeTargetReady,omitempty"`
	CausalRegimeTargetApplied           int                                          `json:"causalRegimeTargetApplied,omitempty"`
	CausalRegimeTargetFullLong          int                                          `json:"causalRegimeTargetFullLong,omitempty"`
	CausalRegimeTargetFullFlat          int                                          `json:"causalRegimeTargetFullFlat,omitempty"`
	CausalRegimeTargetMeanWeight        float64                                      `json:"causalRegimeTargetMeanWeight,omitempty"`
	CausalRegimeTargetMeanDelta         float64                                      `json:"causalRegimeTargetMeanDelta,omitempty"`
	CausalRegimeTargetReasons           map[string]int                               `json:"causalRegimeTargetReasons,omitempty"`
	EarlyStatisticalEvaluations         int                                          `json:"earlyStatisticalRealignmentEvaluations"`
	EarlyStatisticalApplied             int                                          `json:"earlyStatisticalRealignmentApplied"`
	JointQuoteEvaluations               int                                          `json:"jointQuoteEvaluations"`
	JointQuoteAccepted                  int                                          `json:"jointQuoteAccepted"`
	JointQuoteApplied                   int                                          `json:"jointQuoteApplied"`
	JointQuoteReasons                   map[string]int                               `json:"jointQuoteReasons,omitempty"`
	AverageJointCapitalUtilization      float64                                      `json:"averageJointCapitalUtilization"`
	AverageJointPairCapitalUtilization  float64                                      `json:"averageJointPairCapitalUtilization"`
	AverageJointCandidateUtilization    float64                                      `json:"averageJointCandidateUtilization"`
	MaximumJointExpectedPnLJPYHour      float64                                      `json:"maximumJointExpectedPnLJPYHour"`
	MaximumJointLowerPnLJPYHour         float64                                      `json:"maximumJointLowerPnLJPYHour"`
	MinimumJointPathEffectiveSamples    float64                                      `json:"minimumJointPathEffectiveSamples"`
	DirectionalTargetOverrides          int                                          `json:"directionalTargetOverrides,omitempty"`
	MeanDirectionalTargetBps            float64                                      `json:"meanDirectionalTargetBps,omitempty"`
	JointQuoteDecisions                 []productionReplayJointDecision              `json:"jointQuoteDecisions,omitempty"`
	HorizonDiagnostics                  map[string]productionReplayHorizonDiagnostic `json:"horizonDiagnostics,omitempty"`
	PostFillUtilityEvaluations          int                                          `json:"postFillUtilityEvaluations"`
	PostFillUtilityApplied              int                                          `json:"postFillUtilityApplied"`
	PostFillUtilityReasons              map[string]int                               `json:"postFillUtilityReasons,omitempty"`
	QuoteLifecycleEvaluations           int                                          `json:"quoteLifecycleEvaluations,omitempty"`
	QuoteLifecycleActions               map[string]int                               `json:"quoteLifecycleActions,omitempty"`
	QuoteLifecycleMeanIncrementalBps    float64                                      `json:"quoteLifecycleMeanIncrementalBps,omitempty"`
	QuoteLifecycleHazardObservations    int                                          `json:"quoteLifecycleHazardObservations,omitempty"`
	QuoteLifecycleHazardReadyReviews    int                                          `json:"quoteLifecycleHazardReadyReviews,omitempty"`
	MaxPostFillIncrementalMeanBps       float64                                      `json:"maxPostFillIncrementalMeanBps"`
	MaxPostFillIncrementalLowerBps      float64                                      `json:"maxPostFillIncrementalLowerBps"`
	AcquisitionResets                   int                                          `json:"acquisitionResets"`
	AcquisitionQuantity                 float64                                      `json:"acquisitionQuantity"`
	MacroActiveAttempts                 int                                          `json:"macroActiveAttempts"`
	MacroActiveFills                    int                                          `json:"macroActiveFills"`
	MacroActiveQuantity                 float64                                      `json:"macroActiveQuantity"`
	MacroActiveDecisions                []productionReplayMacroDecision              `json:"macroActiveDecisions,omitempty"`
	FastTargetActiveAttempts            int                                          `json:"fastTargetActiveAttempts"`
	FastTargetActiveFills               int                                          `json:"fastTargetActiveFills"`
	FastTargetActiveQuantity            float64                                      `json:"fastTargetActiveQuantity"`
	FastTargetActiveDecisions           []replayFastTargetDecision                   `json:"fastTargetActiveDecisions,omitempty"`
	TakerFeesJPY                        float64                                      `json:"takerFeesJPY"`
	AcquisitionEvaluations              int                                          `json:"acquisitionEvaluations"`
	AcquisitionRejections               map[string]int                               `json:"acquisitionRejections,omitempty"`
	AcquisitionDrawdownLimitSamples     int                                          `json:"acquisitionDrawdownLimitSamples"`
	MinimumAcquisitionDrawdownLimitBps  float64                                      `json:"minimumAcquisitionDrawdownLimitBps"`
	MeanAcquisitionDrawdownLimitBps     float64                                      `json:"meanAcquisitionDrawdownLimitBps"`
	MaximumAcquisitionDrawdownLimitBps  float64                                      `json:"maximumAcquisitionDrawdownLimitBps"`
	MaxUpProbabilityLower               float64                                      `json:"maxUpProbabilityLower"`
	MaxAcquisitionIOCValueBps           float64                                      `json:"maxAcquisitionIOCValueBps"`
	MaxAcquisitionImprovementBps        float64                                      `json:"maxAcquisitionImprovementBps"`
	FillsPerHour                        float64                                      `json:"fillsPerHour"`
	FillsPerDay                         float64                                      `json:"fillsPerDay"`
	RoundTripsPerDay                    float64                                      `json:"roundTripsPerDay"`
	QuoteUptimePct                      float64                                      `json:"quoteUptimePct"`
	AverageBidDistanceBps               float64                                      `json:"averageBidDistanceBps"`
	AverageAskDistanceBps               float64                                      `json:"averageAskDistanceBps"`
	AverageQuoteLifeSeconds             float64                                      `json:"averageQuoteLifeSeconds"`
	HorizonTouchFeatureReadyPct         float64                                      `json:"horizonTouchFeatureReadyPct"`
	AsymmetricRiskEnabled               bool                                         `json:"asymmetricRiskEnabled"`
	AsymmetricRiskSamples               int                                          `json:"asymmetricRiskSamples"`
	AsymmetricRiskMeanMultiplier        float64                                      `json:"asymmetricRiskMeanMultiplier"`
	AsymmetricRiskMinMultiplier         float64                                      `json:"asymmetricRiskMinMultiplier"`
	AsymmetricRiskMaxMultiplier         float64                                      `json:"asymmetricRiskMaxMultiplier"`
	AsymmetricRiskReadySamples          int                                          `json:"asymmetricRiskReadySamples"`
	AsymmetricRiskDirectionStrength     float64                                      `json:"asymmetricRiskDirectionStrength"`
	AsymmetricRiskAsymmetryWeight       float64                                      `json:"asymmetricRiskAsymmetryWeight"`
	VolumeProfileEnabled                bool                                         `json:"volumeProfileEnabled"`
	VolumeProfileAsymmetricPOCRisk      bool                                         `json:"volumeProfileAsymmetricPOCRisk"`
	VolumeProfileSamples                int                                          `json:"volumeProfileSamples"`
	VolumeProfileReadySamples           int                                          `json:"volumeProfileReadySamples"`
	RelativeHoldRiskEnabled             bool                                         `json:"relativeHoldRiskEnabled,omitempty"`
	RelativeHoldRiskApplied             bool                                         `json:"relativeHoldRiskApplied,omitempty"`
	RelativeHoldRiskMaturedLabels       int                                          `json:"relativeHoldRiskMaturedLabels,omitempty"`
	RelativeHoldRiskEffectiveSamples    float64                                      `json:"relativeHoldRiskEffectiveSamples,omitempty"`
	RelativeHoldRiskDownsideSamples     float64                                      `json:"relativeHoldRiskDownsideEffectiveSamples,omitempty"`
	RelativeHoldRiskMeanExcessBps       float64                                      `json:"relativeHoldRiskMeanExcessBps,omitempty"`
	RelativeHoldRiskTrackingErrorBps    float64                                      `json:"relativeHoldRiskTrackingErrorBps,omitempty"`
	RelativeHoldRiskDownsideBeta        float64                                      `json:"relativeHoldRiskDownsideBeta,omitempty"`
	RelativeHoldRiskTotalBeta           float64                                      `json:"relativeHoldRiskTotalBeta,omitempty"`
	RelativeHoldRiskTotalBetaUpper      float64                                      `json:"relativeHoldRiskTotalBetaUpper,omitempty"`
	RelativeHoldRiskDownsideCVaRBps     float64                                      `json:"relativeHoldRiskDownsideCVaRBps,omitempty"`
	RelativeHoldRiskUtilitySumJPYHour   float64                                      `json:"relativeHoldRiskUtilitySumJPYHour,omitempty"`
	RelativeHoldRiskUtilityEvaluations  int                                          `json:"relativeHoldRiskUtilityEvaluations,omitempty"`
	RelativeHoldRiskReadyInputs         int                                          `json:"relativeHoldRiskReadyInputs,omitempty"`
	RelativeHoldRiskReadyDecisions      int                                          `json:"relativeHoldRiskReadyDecisions,omitempty"`
	RegimeEVSizingEvaluations           int                                          `json:"regimeExpectedValueSizingEvaluations,omitempty"`
	RegimeEVSizingApplied               int                                          `json:"regimeExpectedValueSizingApplied,omitempty"`
	RegimeEVSizingZeroScale             int                                          `json:"regimeExpectedValueSizingZeroScale,omitempty"`
	RegimeEVSizingMeanScale             float64                                      `json:"regimeExpectedValueSizingMeanScale,omitempty"`
	RegimeEVSizingMeanGrossValueJPY     float64                                      `json:"regimeExpectedValueSizingMeanGrossValueJPY,omitempty"`
	RegimeEVSizingMeanNetValueJPY       float64                                      `json:"regimeExpectedValueSizingMeanNetValueJPY,omitempty"`
	RegimeEVSizingMeanUtilityJPY        float64                                      `json:"regimeExpectedValueSizingMeanUtilityJPY,omitempty"`
	RegimeEVSizingExpectedFeesJPY       float64                                      `json:"regimeExpectedValueSizingExpectedFeesJPY,omitempty"`
	RegimeEVSizingNotReady              int                                          `json:"regimeExpectedValueSizingNotReady,omitempty"`
	RegimeEVSizingRegimeNotReady        int                                          `json:"regimeExpectedValueSizingRegimeNotReady,omitempty"`
	RegimeEVSizingFastDriftFallback     int                                          `json:"regimeExpectedValueSizingFastDriftFallback,omitempty"`
	RegimeEVSizingMeanEffectiveSamples  float64                                      `json:"regimeExpectedValueSizingMeanEffectiveSamples,omitempty"`
	RegimeEVSizingMeanRegimeReliability float64                                      `json:"regimeExpectedValueSizingMeanRegimeReliability,omitempty"`
	RegimeEVSizingReasons               map[string]int                               `json:"regimeExpectedValueSizingReasons,omitempty"`
	HorizonUtilityEvaluations           int                                          `json:"horizonConditionedUtilityEvaluations,omitempty"`
	HorizonUtilityReady                 int                                          `json:"horizonConditionedUtilityReady,omitempty"`
	HorizonUtilityPositive              int                                          `json:"horizonConditionedUtilityPositive,omitempty"`
	HorizonUtilityZeroScale             int                                          `json:"horizonConditionedUtilityZeroScale,omitempty"`
	HorizonUtilityAnchors               int                                          `json:"horizonConditionedUtilityAnchors,omitempty"`
	HorizonUtilityShortLabels           int                                          `json:"horizonConditionedUtilityShortLabels,omitempty"`
	HorizonUtilityContinuationLabels    int                                          `json:"horizonConditionedUtilityContinuationLabels,omitempty"`
	HorizonUtilityTouchedLabels         int                                          `json:"horizonConditionedUtilityTouchedLabels,omitempty"`
	HorizonUtilityMeanScale             float64                                      `json:"horizonConditionedUtilityMeanScale,omitempty"`
	HorizonUtilityMeanEffectiveSamples  float64                                      `json:"horizonConditionedUtilityMeanEffectiveSamples,omitempty"`
	HorizonUtilityMeanFillProbability   float64                                      `json:"horizonConditionedUtilityMeanFillProbability,omitempty"`
	MakerFeesJPY                        float64                                      `json:"makerFeesJPY"`
	NetPnLJPY                           float64                                      `json:"netPnLJPY"`
	HoldPnLJPY                          float64                                      `json:"holdPnLJPY"`
	MeanMarkout1mBps                    float64                                      `json:"meanMarkout1mBps"`
	MeanMarkout5mBps                    float64                                      `json:"meanMarkout5mBps"`
	MeanMarkout10mBps                   float64                                      `json:"meanMarkout10mBps"`
	DayResults                          []productionReplayDay                        `json:"dayResults"`
	EquityCurve                         []productionEquityPoint                      `json:"equityCurve,omitempty"`
	StoppedEarly                        bool                                         `json:"stoppedEarly"`
	StopAt                              time.Time                                    `json:"stopAt,omitempty"`
	StopReason                          string                                       `json:"stopReason,omitempty"`
	MaximumDrawdownPct                  float64                                      `json:"maximumDrawdownPct"`
	Limitations                         []string                                     `json:"limitations"`
	// relativeHoldRiskCheckpoint is intentionally not serialized in the report;
	// the study driver persists it in a separate 0600 checkpoint file together
	// with the replay cursor and configuration fingerprint.
	relativeHoldRiskCheckpoint *gammacapture.RelativeHoldRiskCheckpoint
}

type productionReplayHorizonDiagnostic struct {
	Evaluations                      int     `json:"evaluations"`
	SufficientCrossings              int     `json:"sufficientCrossings"`
	PathReady                        int     `json:"pathReady"`
	PositiveRiskAdjustedPathMean     int     `json:"positiveRiskAdjustedPathMean"`
	Selected                         int     `json:"selected"`
	MeanScoreBpsPerHour              float64 `json:"meanScoreBpsPerHour"`
	MaximumScoreBpsPerHour           float64 `json:"maximumScoreBpsPerHour"`
	MeanScoreStdErrorBpsPerHour      float64 `json:"meanScoreStdErrorBpsPerHour"`
	MeanBuyTouchProbability          float64 `json:"meanBuyTouchProbability"`
	MeanSellTouchProbability         float64 `json:"meanSellTouchProbability"`
	MeanPathEffectiveSamples         float64 `json:"meanPathEffectiveSamples"`
	MeanPathEffectiveSamplesBaseline float64 `json:"meanPathEffectiveSamplesBaseline"`
	MeanPathDecayHalfLifeSeconds     float64 `json:"meanPathDecayHalfLifeSeconds"`
	MeanPathDecayAutocorrelation     float64 `json:"meanPathDecayAutocorrelation"`
	PathDecayPersistenceObservations float64 `json:"pathDecayPersistenceObservations"`
	scoreSum                         float64
	scoreStdErrorSum                 float64
	buyTouchProbabilitySum           float64
	sellTouchProbabilitySum          float64
	pathEffectiveSamplesSum          float64
	pathEffectiveSamplesBaselineSum  float64
	pathDecayHalfLifeSum             float64
	pathDecayAutocorrelationSum      float64
	pathDecayPersistenceSum          float64
}

func (d *productionReplayHorizonDiagnostic) finalize() {
	if d.Evaluations > 0 {
		denominator := float64(d.Evaluations)
		d.MeanScoreBpsPerHour = d.scoreSum / denominator
		d.MeanScoreStdErrorBpsPerHour = d.scoreStdErrorSum / denominator
		d.MeanBuyTouchProbability = d.buyTouchProbabilitySum / denominator
		d.MeanSellTouchProbability = d.sellTouchProbabilitySum / denominator
	}
	if d.PathReady > 0 {
		d.MeanPathEffectiveSamples = d.pathEffectiveSamplesSum / float64(d.PathReady)
		d.MeanPathEffectiveSamplesBaseline = d.pathEffectiveSamplesBaselineSum / float64(d.PathReady)
		d.MeanPathDecayHalfLifeSeconds = d.pathDecayHalfLifeSum / float64(d.PathReady)
		d.MeanPathDecayAutocorrelation = d.pathDecayAutocorrelationSum / float64(d.PathReady)
		d.PathDecayPersistenceObservations = d.pathDecayPersistenceSum / float64(d.PathReady)
	}
}

type productionComparisonReport struct {
	Symbol                        string                          `json:"symbol"`
	ValidationStage               string                          `json:"validationStage,omitempty"`
	ProductionMode                replayPolicyMode                `json:"productionMode"`
	HorizonTouchEnabled           bool                            `json:"horizonTouchEnabled"`
	WarmupFrom                    time.Time                       `json:"warmupFrom"`
	ExactFrom                     time.Time                       `json:"exactFrom"`
	WarmupBBOEvents               int                             `json:"warmupBBOEvents"`
	WarmupTradeEvents             int                             `json:"warmupTradeEvents"`
	CalibrationActualBuy          int                             `json:"calibrationActualBuyFills"`
	CalibrationActualSell         int                             `json:"calibrationActualSellFills"`
	CalibratedQueueMultiplier     float64                         `json:"calibratedQueueMultiplier"`
	SelectedQueueMultiplier       float64                         `json:"selectedQueueMultiplier"`
	ReplayQueueSource             string                          `json:"replayQueueSource,omitempty"`
	CalibrationPassed             bool                            `json:"calibrationPassed"`
	CalibrationAbsError           int                             `json:"calibrationAbsoluteSideError"`
	CalibrationCandidates         []productionReplayResult        `json:"calibrationCandidates"`
	CalibrationLegacy             productionReplayResult          `json:"calibrationLegacy"`
	FullLegacy                    productionReplayResult          `json:"fullLegacy"`
	FullAsymmetricRisk            productionReplayResult          `json:"fullAsymmetricRisk"`
	FullNormalFlowPressure        productionReplayResult          `json:"fullNormalFlowPressure,omitempty"`
	FullVolumeProfile             productionReplayResult          `json:"fullVolumeProfile"`
	FullVolumeProfilePOCRisk      productionReplayResult          `json:"fullVolumeProfilePOCRisk"`
	FullHorizonTouch              productionReplayResult          `json:"fullHorizonTouch"`
	FullQuoteLifecycle            productionReplayResult          `json:"fullQuoteLifecycle,omitempty"`
	JournalLifecycle              *orderLifecycleReport           `json:"journalLifecycle,omitempty"`
	FullAcquisitionReset          productionReplayResult          `json:"fullAcquisitionReset"`
	FullRelativeHold              productionReplayResult          `json:"fullRelativeHold,omitempty"`
	FullStackedTarget             productionReplayResult          `json:"fullStackedTarget,omitempty"`
	FullSingleActionTarget        productionReplayResult          `json:"fullSingleActionTarget,omitempty"`
	FullPivotRegimeTarget         productionReplayResult          `json:"fullPivotRegimeTarget,omitempty"`
	FullCausalRegimeTarget        productionReplayResult          `json:"fullCausalRegimeTarget,omitempty"`
	FullHorizonConditionedUtility productionReplayResult          `json:"fullHorizonConditionedUtility,omitempty"`
	Decision                      string                          `json:"decision"`
	ReplayCacheHit                bool                            `json:"replayCacheHit"`
	ReplayBBOInterval             string                          `json:"replayBBOInterval,omitempty"`
	Preload                       productionReplayPreloadManifest `json:"preload"`
}

type productionReplayScalarSummary struct {
	Mode                                replayPolicyMode `json:"mode"`
	From                                time.Time        `json:"from"`
	To                                  time.Time        `json:"to"`
	ActiveHours                         float64          `json:"activeHours"`
	BBOEvents                           int              `json:"bboEvents"`
	AggTradeEvents                      int              `json:"aggTradeEvents"`
	DataGaps                            int              `json:"dataGaps"`
	QueueMultiplier                     float64          `json:"queueMultiplier"`
	QuoteRefreshes                      int              `json:"quoteRefreshes"`
	FullFills                           int              `json:"fullFills"`
	BuyFills                            int              `json:"buyFills"`
	SellFills                           int              `json:"sellFills"`
	RoundTrips                          int              `json:"roundTrips"`
	MakerFeesJPY                        float64          `json:"makerFeesJPY"`
	NetPnLJPY                           float64          `json:"netPnLJPY"`
	HoldPnLJPY                          float64          `json:"holdPnLJPY"`
	MaximumDrawdownPct                  float64          `json:"maximumDrawdownPct"`
	FillsPerDay                         float64          `json:"fillsPerDay"`
	RoundTripsPerDay                    float64          `json:"roundTripsPerDay"`
	QuoteUptimePct                      float64          `json:"quoteUptimePct"`
	AverageBidDistance                  float64          `json:"averageBidDistanceBps"`
	AverageAskDistance                  float64          `json:"averageAskDistanceBps"`
	AverageQuoteLifeSec                 float64          `json:"averageQuoteLifeSeconds"`
	MeanMarkout1mBps                    float64          `json:"meanMarkout1mBps"`
	MeanMarkout5mBps                    float64          `json:"meanMarkout5mBps"`
	MeanMarkout10mBps                   float64          `json:"meanMarkout10mBps"`
	RegimeEVSizingEvaluations           int              `json:"regimeExpectedValueSizingEvaluations,omitempty"`
	RegimeEVSizingApplied               int              `json:"regimeExpectedValueSizingApplied,omitempty"`
	RegimeEVSizingZeroScale             int              `json:"regimeExpectedValueSizingZeroScale,omitempty"`
	RegimeEVSizingMeanScale             float64          `json:"regimeExpectedValueSizingMeanScale,omitempty"`
	RegimeEVSizingMeanGrossValueJPY     float64          `json:"regimeExpectedValueSizingMeanGrossValueJPY,omitempty"`
	RegimeEVSizingMeanNetValueJPY       float64          `json:"regimeExpectedValueSizingMeanNetValueJPY,omitempty"`
	RegimeEVSizingMeanUtilityJPY        float64          `json:"regimeExpectedValueSizingMeanUtilityJPY,omitempty"`
	RegimeEVSizingExpectedFeesJPY       float64          `json:"regimeExpectedValueSizingExpectedFeesJPY,omitempty"`
	RegimeEVSizingNotReady              int              `json:"regimeExpectedValueSizingNotReady,omitempty"`
	RegimeEVSizingRegimeNotReady        int              `json:"regimeExpectedValueSizingRegimeNotReady,omitempty"`
	RegimeEVSizingFastDriftFallback     int              `json:"regimeExpectedValueSizingFastDriftFallback,omitempty"`
	RegimeEVSizingMeanEffectiveSamples  float64          `json:"regimeExpectedValueSizingMeanEffectiveSamples,omitempty"`
	RegimeEVSizingMeanRegimeReliability float64          `json:"regimeExpectedValueSizingMeanRegimeReliability,omitempty"`
	RegimeEVSizingReasons               map[string]int   `json:"regimeExpectedValueSizingReasons,omitempty"`
	HorizonUtilityEvaluations           int              `json:"horizonConditionedUtilityEvaluations,omitempty"`
	HorizonUtilityReady                 int              `json:"horizonConditionedUtilityReady,omitempty"`
	HorizonUtilityPositive              int              `json:"horizonConditionedUtilityPositive,omitempty"`
	HorizonUtilityZeroScale             int              `json:"horizonConditionedUtilityZeroScale,omitempty"`
	HorizonUtilityAnchors               int              `json:"horizonConditionedUtilityAnchors,omitempty"`
	HorizonUtilityShortLabels           int              `json:"horizonConditionedUtilityShortLabels,omitempty"`
	HorizonUtilityContinuationLabels    int              `json:"horizonConditionedUtilityContinuationLabels,omitempty"`
	HorizonUtilityTouchedLabels         int              `json:"horizonConditionedUtilityTouchedLabels,omitempty"`
	HorizonUtilityMeanScale             float64          `json:"horizonConditionedUtilityMeanScale,omitempty"`
	HorizonUtilityMeanEffectiveSamples  float64          `json:"horizonConditionedUtilityMeanEffectiveSamples,omitempty"`
	HorizonUtilityMeanFillProbability   float64          `json:"horizonConditionedUtilityMeanFillProbability,omitempty"`
}

func summarizeProductionReplayResult(result productionReplayResult) productionReplayScalarSummary {
	return productionReplayScalarSummary{
		Mode: result.Mode, From: result.From, To: result.To, ActiveHours: result.ActiveHours,
		BBOEvents: result.BBOEvents, AggTradeEvents: result.AggTradeEvents, DataGaps: result.DataGaps,
		QueueMultiplier: result.QueueMultiplier, QuoteRefreshes: result.QuoteRefreshes,
		FullFills: result.FullFills, BuyFills: result.BuyFills, SellFills: result.SellFills,
		RoundTrips: result.RoundTrips, MakerFeesJPY: result.MakerFeesJPY, NetPnLJPY: result.NetPnLJPY,
		HoldPnLJPY: result.HoldPnLJPY, MaximumDrawdownPct: result.MaximumDrawdownPct,
		FillsPerDay: result.FillsPerDay, RoundTripsPerDay: result.RoundTripsPerDay,
		QuoteUptimePct: result.QuoteUptimePct, AverageBidDistance: result.AverageBidDistanceBps,
		AverageAskDistance: result.AverageAskDistanceBps, AverageQuoteLifeSec: result.AverageQuoteLifeSeconds,
		MeanMarkout1mBps: result.MeanMarkout1mBps, MeanMarkout5mBps: result.MeanMarkout5mBps,
		MeanMarkout10mBps:                   result.MeanMarkout10mBps,
		RegimeEVSizingEvaluations:           result.RegimeEVSizingEvaluations,
		RegimeEVSizingApplied:               result.RegimeEVSizingApplied,
		RegimeEVSizingZeroScale:             result.RegimeEVSizingZeroScale,
		RegimeEVSizingMeanScale:             result.RegimeEVSizingMeanScale,
		RegimeEVSizingMeanGrossValueJPY:     result.RegimeEVSizingMeanGrossValueJPY,
		RegimeEVSizingMeanNetValueJPY:       result.RegimeEVSizingMeanNetValueJPY,
		RegimeEVSizingMeanUtilityJPY:        result.RegimeEVSizingMeanUtilityJPY,
		RegimeEVSizingExpectedFeesJPY:       result.RegimeEVSizingExpectedFeesJPY,
		RegimeEVSizingNotReady:              result.RegimeEVSizingNotReady,
		RegimeEVSizingRegimeNotReady:        result.RegimeEVSizingRegimeNotReady,
		RegimeEVSizingFastDriftFallback:     result.RegimeEVSizingFastDriftFallback,
		RegimeEVSizingMeanEffectiveSamples:  result.RegimeEVSizingMeanEffectiveSamples,
		RegimeEVSizingMeanRegimeReliability: result.RegimeEVSizingMeanRegimeReliability,
		RegimeEVSizingReasons:               result.RegimeEVSizingReasons,
		HorizonUtilityEvaluations:           result.HorizonUtilityEvaluations,
		HorizonUtilityReady:                 result.HorizonUtilityReady,
		HorizonUtilityPositive:              result.HorizonUtilityPositive,
		HorizonUtilityZeroScale:             result.HorizonUtilityZeroScale,
		HorizonUtilityAnchors:               result.HorizonUtilityAnchors,
		HorizonUtilityShortLabels:           result.HorizonUtilityShortLabels,
		HorizonUtilityContinuationLabels:    result.HorizonUtilityContinuationLabels,
		HorizonUtilityTouchedLabels:         result.HorizonUtilityTouchedLabels,
		HorizonUtilityMeanScale:             result.HorizonUtilityMeanScale,
		HorizonUtilityMeanEffectiveSamples:  result.HorizonUtilityMeanEffectiveSamples,
		HorizonUtilityMeanFillProbability:   result.HorizonUtilityMeanFillProbability,
	}
}

type normalFlowPressureReplaySummary struct {
	Symbol                  string                                    `json:"symbol"`
	WarmupFrom              time.Time                                 `json:"warmupFrom"`
	ExactFrom               time.Time                                 `json:"exactFrom"`
	WarmupBBOEvents         int                                       `json:"warmupBBOEvents"`
	WarmupTradeEvents       int                                       `json:"warmupTradeEvents"`
	CalibrationActualBuy    int                                       `json:"calibrationActualBuyFills"`
	CalibrationActualSell   int                                       `json:"calibrationActualSellFills"`
	SelectedQueueMultiplier float64                                   `json:"selectedQueueMultiplier"`
	CalibrationPassed       bool                                      `json:"calibrationPassed"`
	CalibrationAbsoluteErr  int                                       `json:"calibrationAbsoluteSideError"`
	Baseline                productionReplayScalarSummary             `json:"baseline"`
	NormalFlowPressure      productionReplayScalarSummary             `json:"normalFlowPressure"`
	QuantityCandidates      []normalFlowPressureQuantityReplaySummary `json:"quantityCandidates,omitempty"`
	Decision                string                                    `json:"decision"`
	ReplayCacheHit          bool                                      `json:"replayCacheHit"`
	ReplayBBOInterval       string                                    `json:"replayBBOInterval,omitempty"`
}

type normalFlowPressureQuantityReplaySummary struct {
	RiskBudgetScale float64                       `json:"riskBudgetScale"`
	Replay          productionReplayScalarSummary `json:"replay"`
}

type feeFreeCounterfactualSummary struct {
	Symbol                  string                        `json:"symbol"`
	WarmupFrom              time.Time                     `json:"warmupFrom"`
	ExactFrom               time.Time                     `json:"exactFrom"`
	CalibrationActualBuy    int                           `json:"calibrationActualBuyFills"`
	CalibrationActualSell   int                           `json:"calibrationActualSellFills"`
	CalibrationPassed       bool                          `json:"calibrationPassed"`
	CalibrationAbsoluteErr  int                           `json:"calibrationAbsoluteSideError"`
	Baseline                productionReplayScalarSummary `json:"baseline"`
	AccountingFeeFreeGross  productionReplayScalarSummary `json:"accountingFeeFreeGross"`
	PolicyFeeFree           productionReplayScalarSummary `json:"policyFeeFree"`
	FeesRemovedJPY          float64                       `json:"feesRemovedJPY"`
	AccountingOnlyDDNote    string                        `json:"accountingOnlyDDNote"`
	PolicyFeeFreeDefinition string                        `json:"policyFeeFreeDefinition"`
	Decision                string                        `json:"decision"`
	ReplayCacheHit          bool                          `json:"replayCacheHit"`
	ReplayBBOInterval       string                        `json:"replayBBOInterval,omitempty"`
}

type regimeExpectedValueSizingSummary struct {
	Symbol                 string                        `json:"symbol"`
	WarmupFrom             time.Time                     `json:"warmupFrom"`
	ExactFrom              time.Time                     `json:"exactFrom"`
	CalibrationActualBuy   int                           `json:"calibrationActualBuyFills"`
	CalibrationActualSell  int                           `json:"calibrationActualSellFills"`
	CalibrationPassed      bool                          `json:"calibrationPassed"`
	CalibrationAbsoluteErr int                           `json:"calibrationAbsoluteSideError"`
	Baseline               productionReplayScalarSummary `json:"baseline"`
	RegimeExpectedValue    productionReplayScalarSummary `json:"regimeExpectedValue"`
	Definition             string                        `json:"definition"`
	Decision               string                        `json:"decision"`
	ReplayCacheHit         bool                          `json:"replayCacheHit"`
	ReplayBBOInterval      string                        `json:"replayBBOInterval,omitempty"`
}

// productionReplayWarmup covers the longest causal estimator history plus
// the time needed for a path opened at the oldest retained point to mature.
// Two-stage continuation can consume a second Fast horizon, so maturity is
// derived from the configured continuation contract rather than a fixed
// safety margin.
func productionReplayWarmup(config gammacapture.MarketMakerConfig) time.Duration {
	return config.RequiredStartupWarmup()
}

func productionReplayLoadRange(config gammacapture.MarketMakerConfig, from, calibrationFrom time.Time) (warmFrom, exactFrom time.Time) {
	exactFrom = from
	if !calibrationFrom.IsZero() && calibrationFrom.Before(exactFrom) {
		exactFrom = calibrationFrom
	}
	return exactFrom.Add(-productionReplayWarmup(config)), exactFrom
}

type productionReplayOrder struct {
	active                                 bool
	eligible                               bool
	side                                   types.SideType
	price, quantity, remaining, queueAhead float64
	filledQuantity, accumulatedFee         float64
	placedAt                               time.Time
	origin                                 replayQuoteOrigin
}

// replayQuoteOrigin freezes only information available when a maker order is
// submitted. It lets fill analysis compare causal quote premises without
// accidentally joining against a later model snapshot.
type replayQuoteOrigin struct {
	PlacedAt                       time.Time     `json:"placedAt"`
	Horizon                        time.Duration `json:"horizon"`
	CompletionHorizon              time.Duration `json:"completionHorizon,omitempty"`
	BestBid                        float64       `json:"bestBid"`
	BestAsk                        float64       `json:"bestAsk"`
	BidPrice                       float64       `json:"bidPrice"`
	AskPrice                       float64       `json:"askPrice"`
	BuyNotionalJPY                 float64       `json:"buyNotionalJPY"`
	SellNotionalJPY                float64       `json:"sellNotionalJPY"`
	AdmissionJointComplementary    bool          `json:"admissionJointComplementary"`
	BookImbalance                  float64       `json:"bookImbalance"`
	FastUp                         int           `json:"fastUp"`
	FastDown                       int           `json:"fastDown"`
	FastDirection                  float64       `json:"fastDirection"`
	FastDirectionConfidence        float64       `json:"fastDirectionConfidence"`
	FastEvidenceCoverage           float64       `json:"fastEvidenceCoverage"`
	FastMidReturn1mBps             float64       `json:"fastMidReturn1mBps"`
	FastMidReturn5mBps             float64       `json:"fastMidReturn5mBps"`
	FastMidDrawdownBps             float64       `json:"fastMidDrawdownBps"`
	FastRebound30sBps              float64       `json:"fastRebound30sBps"`
	FastTradeImbalance5m           float64       `json:"fastTradeImbalance5m"`
	FastOFI30s                     float64       `json:"fastOFI30s"`
	FastMicropriceDisplacement     float64       `json:"fastMicropriceDisplacement"`
	CurrentInventoryJPY            float64       `json:"currentInventoryJPY"`
	TargetInventoryJPY             float64       `json:"targetInventoryJPY"`
	PosteriorInventoryReturnBps    float64       `json:"posteriorInventoryReturnBps"`
	PosteriorInventoryPredictiveSD float64       `json:"posteriorInventoryPredictiveSDBps"`
	BuyTouchProbability            float64       `json:"buyTouchProbability"`
	SellTouchProbability           float64       `json:"sellTouchProbability"`
	JointExpectedPnLJPYHour        float64       `json:"jointExpectedPnLJPYHour"`
	JointLowerPnLJPYHour           float64       `json:"jointLowerPnLJPYHour"`
	JointFeeValueNetJPY            float64       `json:"jointFeeValueNetJPY"`
	JointEffectiveSamples          float64       `json:"jointEffectiveSamples"`
	BuyAdmissionUtilityBoundJPY    float64       `json:"buyAdmissionUtilityBoundJPY"`
	SellAdmissionUtilityBoundJPY   float64       `json:"sellAdmissionUtilityBoundJPY"`
}

type replayCompletionContract struct {
	active           bool
	side             types.SideType
	price            float64
	quantity         float64
	referenceHorizon time.Duration
	until            time.Time
}

type replayFill struct {
	At             time.Time          `json:"at"`
	Side           types.SideType     `json:"side"`
	Price          float64            `json:"price"`
	Quantity       float64            `json:"quantity"`
	NotionalJPY    float64            `json:"notionalJPY"`
	FeeJPY         float64            `json:"feeJPY"`
	InventoryAfter float64            `json:"inventoryAfter"`
	QuoteAfter     float64            `json:"quoteAfter"`
	QuoteOrigin    *replayQuoteOrigin `json:"quoteOrigin,omitempty"`
}

type productionReplayMacroIOC struct {
	active      bool
	direction   int
	quantity    float64
	worstPrice  float64
	closedBarAt time.Time
}

type productionReplayFastTargetIOC struct {
	active         bool
	direction      int
	quantity       float64
	worstPrice     float64
	modelUpdatedAt time.Time
}

type productionReplayJointDecision struct {
	At                           time.Time     `json:"at"`
	PreliminaryHorizon           time.Duration `json:"preliminaryHorizon"`
	Horizon                      time.Duration `json:"horizon"`
	HorizonCrossingScoreBpsHour  float64       `json:"horizonCrossingScoreBpsHour"`
	HorizonSelectionScoreBpsHour float64       `json:"horizonSelectionScoreBpsHour"`
	HorizonMarginalBuyEvaluated  bool          `json:"horizonMarginalBuyEvaluated"`
	HorizonMarginalBuyNotional   float64       `json:"horizonMarginalBuyNotionalJPY"`
	HorizonMarginalBuyCE         float64       `json:"horizonMarginalBuyCEJPY"`
	HorizonMarginalBuyUtility    float64       `json:"horizonMarginalBuyUtilityBpsHour"`
	HorizonCandidateCount        int           `json:"horizonCandidateCount"`
	HorizonEffectiveSamples      float64       `json:"horizonEffectiveSamples"`
	HorizonReliability           float64       `json:"horizonReliability"`
	HorizonRawUtilityJPYHour     float64       `json:"horizonRawUtilityJPYHour"`
	HorizonPosteriorJPYHour      float64       `json:"horizonPosteriorUtilityJPYHour"`
	Reason                       string        `json:"reason"`
	AuthoritativeRejection       bool          `json:"authoritativeRejection"`
	SideSafeFallback             bool          `json:"sideSafeFallback"`
	ContinuityFloorApplied       bool          `json:"continuityFloorApplied"`
	ContinuityFloorReason        string        `json:"continuityFloorReason"`
	BasePlanAllowBid             bool          `json:"basePlanAllowBid"`
	BasePlanAllowAsk             bool          `json:"basePlanAllowAsk"`
	CompletionProtected          bool          `json:"completionProtected"`
	FallbackBuySupported         bool          `json:"fallbackBuySupported"`
	FallbackSellSupported        bool          `json:"fallbackSellSupported"`
	DistanceCandidate            int           `json:"distanceCandidate"`
	QuantityCandidate            int           `json:"quantityCandidate"`
	QuantityScale                float64       `json:"quantityScale"`
	CapitalUtilization           float64       `json:"capitalUtilization"`
	PairUtilization              float64       `json:"pairCapitalUtilization"`
	ExpectedJPYHour              float64       `json:"expectedJPYHour"`
	LowerJPYHour                 float64       `json:"lowerJPYHour"`
	StdErrorJPYHour              float64       `json:"stdErrorJPYHour"`
	KellyPenaltyHour             float64       `json:"kellyPenaltyJPYHour"`
	KellyUtilityHour             float64       `json:"kellyUtilityJPYHour"`
	FeeValueMeanJPY              float64       `json:"feeValueMeanJPY"`
	FeeValueDownsideRegretJPY    float64       `json:"feeValueDownsideRegretJPY"`
	FeeValueNetJPY               float64       `json:"feeValueNetJPY"`
	PositiveConfidence           float64       `json:"positiveConfidence"`
	EffectiveSamples             float64       `json:"effectiveSamples"`
	FastDirection                float64       `json:"fastDirection"`
	FastDriftApplied             bool          `json:"fastDriftApplied"`
	FastDriftMeanBps             float64       `json:"fastDriftMeanBps"`
	FastDriftRawMeanBps          float64       `json:"fastDriftRawMeanBps"`
	FastDriftStrength            float64       `json:"fastDriftStrength"`
	FastDriftProbability         float64       `json:"fastDriftValidationProbability"`
	FastDriftVarianceBps2        float64       `json:"fastDriftVarianceBps2"`
	FastDriftSamples             int           `json:"fastDriftSamples"`
	FastDriftValidation          int           `json:"fastDriftValidationSamples"`
	FastDriftSkill               float64       `json:"fastDriftPrequentialSkill"`
	FastDriftReason              string        `json:"fastDriftReason"`
	FastDriftBBOStateTag         float64       `json:"fastDriftBBOStateTag"`
	BestBid                      float64       `json:"bestBid"`
	BestAsk                      float64       `json:"bestAsk"`
	BidPrice                     float64       `json:"bidPrice"`
	AskPrice                     float64       `json:"askPrice"`
	BuyNotionalJPY               float64       `json:"buyNotionalJPY"`
	SellNotionalJPY              float64       `json:"sellNotionalJPY"`
	CurrentInventoryJPY          float64       `json:"currentInventoryJPY"`
	TargetInventoryJPY           float64       `json:"targetInventoryJPY"`
	BuyTouchProbability          float64       `json:"buyTouchProbability"`
	SellTouchProbability         float64       `json:"sellTouchProbability"`
	NetRoundTripEdgeBps          float64       `json:"netRoundTripEdgeBps"`
	DownsideBuyCapApplied        bool          `json:"downsideBuyCapApplied"`
	DownsideReturnMeanBps        float64       `json:"downsideInventoryReturnMeanBps"`
	DownsideReturnUpperBps       float64       `json:"downsideInventoryReturnUpperBps"`
	DownsideSamples              float64       `json:"downsideEffectiveSamples"`
	DownsideMinimumBuyEvaluated  bool          `json:"downsideMinimumBuyEvaluated"`
	DownsideMinimumBuyCEJPY      float64       `json:"downsideMinimumBuyCEJPY"`
	BuyAdmissionEvaluated        bool          `json:"buyAdmissionEvaluated"`
	BuyAdmissionApplied          bool          `json:"buyAdmissionApplied"`
	BuyAdmissionMaxJPY           float64       `json:"buyAdmissionMaximumJPY"`
	BuyAdmissionUtilityBoundJPY  float64       `json:"buyAdmissionUtilityBoundJPY"`
	BuyAdmissionReason           string        `json:"buyAdmissionReason,omitempty"`
	SellAdmissionEvaluated       bool          `json:"sellAdmissionEvaluated"`
	SellAdmissionApplied         bool          `json:"sellAdmissionApplied"`
	SellAdmissionMaxJPY          float64       `json:"sellAdmissionMaximumJPY"`
	SellAdmissionUtilityBoundJPY float64       `json:"sellAdmissionUtilityBoundJPY"`
	SellAdmissionReason          string        `json:"sellAdmissionReason,omitempty"`
	AdmissionJointCEJPY          float64       `json:"admissionJointCEJPY"`
	AdmissionJointComplementary  bool          `json:"admissionJointComplementary"`
	InwardBuyEligible            bool          `json:"inwardBuyEligible"`
	InwardSellEligible           bool          `json:"inwardSellEligible"`
	InwardBuySelected            bool          `json:"inwardBuySelected"`
	InwardSellSelected           bool          `json:"inwardSellSelected"`
	SelectedInwardBuyDeltaBps    float64       `json:"selectedInwardBuyDeltaBps"`
	SelectedInwardSellDeltaBps   float64       `json:"selectedInwardSellDeltaBps"`
	ConditionalBuyDeltaMeanBps   float64       `json:"conditionalBuyDeltaMeanBps"`
	ConditionalSellDeltaMeanBps  float64       `json:"conditionalSellDeltaMeanBps"`
}

var activeProductionReplayPosteriorBaseTarget bool
var activeProductionEarlyStatisticalRealignment bool

type productionReplayState struct {
	cfg                                                                                                      gammacapture.MarketMakerConfig
	artifact                                                                                                 *gammacapture.HorizonTouchArtifact
	mode                                                                                                     replayPolicyMode
	symbol                                                                                                   string
	queueFactor                                                                                              float64
	barrierWidth                                                                                             float64
	engine                                                                                                   *gammacapture.CrossingEngine
	slowModel                                                                                                *gammacapture.IntensityModel
	executableCrossingModel                                                                                  *gammacapture.ExecutableCrossingModel
	fastModels                                                                                               map[time.Duration]*gammacapture.IntensityModel
	fastEvidenceModels                                                                                       map[time.Duration]*gammacapture.FastEvidenceModel
	fastWindows                                                                                              []time.Duration
	bocpd45Direction                                                                                         bocpd45DirectionModel
	bocpd45DirectionEnabled                                                                                  bool
	bocpd45Calibration                                                                                       *bocpd45PrequentialCalibration
	horizonModel                                                                                             gammacapture.MarketMakerHorizonModel
	quoteLifecycleHazard                                                                                     *gammacapture.QuoteLifecycleHazardModel
	asymmetricOscillationRisk                                                                                *gammacapture.AsymmetricOscillationRiskModel
	asymmetricRiskDecision                                                                                   gammacapture.AsymmetricOscillationRiskDecision
	macroInventoryModel                                                                                      gammacapture.MacroInventoryModel
	macroInventoryState                                                                                      gammacapture.MacroInventoryState
	inventory, quote, initialEquity, initialInventory, initialQuote                                          float64
	inventoryBand                                                                                            gammacapture.InventoryBand
	sideAllocationBias, sideDistanceBias                                                                     float64
	sideAllocationReady, sideDistanceReady                                                                   bool
	useProbabilityProjection                                                                                 bool
	preloadOnly                                                                                              bool
	projectionTargetAnchor                                                                                   float64
	quotedTargetRatio                                                                                        float64
	quotedFastTargetRatio                                                                                    float64
	quotedFastReservationBps                                                                                 float64
	quotedTargetSet                                                                                          bool
	pivotRegimeFilter                                                                                        *gammacapture.PivotRegimeFilter
	pivotRegimeDecision                                                                                      gammacapture.PivotRegimeDecision
	causalRegimeTargetStats                                                                                  [2]causalRegimeTargetMagnitudeStats
	causalRegimeTargetEvaluations, causalRegimeTargetReady, causalRegimeTargetApplied                        int
	causalRegimeTargetFullLong, causalRegimeTargetFullFlat                                                   int
	causalRegimeTargetWeightSum, causalRegimeTargetDeltaSum                                                  float64
	causalRegimeTargetReasons                                                                                map[string]int
	regimeTargetBucket                                                                                       time.Time
	regimeTargetDecision                                                                                     gammacapture.RegimeConditionedTargetDecision
	lastMakerFill                                                                                            gammacapture.MakerPostFillState
	completionContract                                                                                       replayCompletionContract
	postFillUtilityReasons                                                                                   map[string]int
	quoteLifecycleEvaluations                                                                                int
	quoteLifecycleActions                                                                                    map[string]int
	quoteLifecycleIncrementalSumBps                                                                          float64
	quoteLifecycleHazardReadyReviews                                                                         int
	maxPostFillIncrementalMeanBps, maxPostFillIncrementalLowerBps                                            float64
	fillRefreshPending                                                                                       bool
	postFillUtilityEvaluations, postFillUtilityApplied                                                       int
	fastReservationEvaluations, fastReservationApplied                                                       int
	fastReservationReasons                                                                                   map[string]int
	fastTargetSwitchEvaluations, fastTargetSwitchApplied, fastTargetSwitchRetained                           int
	fastTargetSwitchReasons                                                                                  map[string]int
	fastTargetSwitchNetValueSumJPY                                                                           float64
	dynamicInventoryAimEvaluations, dynamicInventoryAimPassed, dynamicInventoryAimApplied                    int
	dynamicInventoryAimReasons                                                                               map[string]int
	dynamicInventoryAimObservations                                                                          []replayDynamicInventoryAimObservation
	earlyStatisticalEvaluations, earlyStatisticalApplied                                                     int
	jointQuoteEvaluations, jointQuoteAccepted, jointQuoteApplied                                             int
	jointQuoteReasons                                                                                        map[string]int
	jointCapitalUtilizationSum, jointPairCapitalUtilizationSum                                               float64
	maximumJointLowerPnLJPYHour                                                                              float64
	jointCandidateCount, jointBestObserved                                                                   int
	jointCandidateUtilizationSum, maximumJointExpectedPnLJPYHour                                             float64
	minimumJointPathEffectiveSamples                                                                         float64
	relativeHoldRiskModel                                                                                    *gammacapture.RelativeHoldRiskModel
	relativeHoldRiskHorizon                                                                                  time.Duration
	relativeHoldRiskAnchor                                                                                   *productionEquityPoint
	relativeHoldRiskResumeAfter                                                                              time.Time
	relativeHoldRiskApplied                                                                                  bool
	relativeHoldRiskUtilitySumJPYHour                                                                        float64
	relativeHoldRiskUtilityEvaluations                                                                       int
	relativeHoldRiskReadyInputs                                                                              int
	relativeHoldRiskReadyDecisions                                                                           int
	regimeExpectedValueSizer                                                                                 *gammacapture.RegimeExpectedValueSizingConfig
	regimeEVSizingEvaluations, regimeEVSizingApplied, regimeEVSizingZeroScale                                int
	regimeEVSizingNotReady, regimeEVSizingRegimeNotReady, regimeEVSizingFastDriftFallback                    int
	regimeEVSizingScaleSum, regimeEVSizingGrossValueSum, regimeEVSizingNetValueSum, regimeEVSizingUtilitySum float64
	regimeEVSizingEffectiveSamplesSum, regimeEVSizingRegimeReliabilitySum                                    float64
	regimeEVSizingExpectedFeesSum                                                                            float64
	regimeEVSizingReasons                                                                                    map[string]int
	horizonConditionedUtility                                                                                *horizonConditionedUtilityReplaySizer
	directionalTargetOverrides                                                                               int
	directionalTargetSumBps                                                                                  float64
	jointQuoteDecisions                                                                                      []productionReplayJointDecision
	horizonDiagnostics                                                                                       map[string]*productionReplayHorizonDiagnostic
	lastHorizonDiagnosticBucket                                                                              time.Time
	bidOrder, askOrder                                                                                       productionReplayOrder
	pendingMacroIOC                                                                                          productionReplayMacroIOC
	pendingFastTargetIOC                                                                                     productionReplayFastTargetIOC
	lastQuoteAt, windowEndsAt, noOrderRetryAfter                                                             time.Time
	noOrderReferenceBid, noOrderReferenceAsk                                                                 float64
	lastBestBid, lastBestAsk, lastMid, lastImbalance                                                         float64
	acquisitionCooldownUntil                                                                                 time.Time
	acquisitionDeficitSince                                                                                  time.Time
	acquisitionDeficitAnchorMid                                                                              float64
	acquisitionResets                                                                                        int
	macroActiveAttempts, macroActiveFills                                                                    int
	macroActiveQuantity                                                                                      float64
	lastMacroActiveDecisionBarAt                                                                             time.Time
	macroActiveDecisions                                                                                     []productionReplayMacroDecision
	fastTargetActiveAttempts, fastTargetActiveFills                                                          int
	fastTargetActiveQuantity                                                                                 float64
	lastFastTargetExecutionModelAt, lastFastTargetDecisionModelAt                                            time.Time
	lastFastTargetExecutionDirection                                                                         int
	fastTargetActiveDecisions                                                                                []replayFastTargetDecision
	books, trades, gaps, refreshes, quoteActive                                                              int
	fastInventoryZoneDecisions, longHorizonAdjustmentDecisions                                               int
	fills, buys, sells, roundTrips, unmatchedBuys, unmatchedSells                                            int
	featureChecks, featureReady                                                                              int
	tradingFrom, scoreFrom                                                                                   time.Time
	scoreStarted                                                                                             bool
	scoreAccountReset                                                                                        *productionReplayScoreAccount
	scoreInitialEquity, scoreInitialHoldEquity                                                               float64
	scoreInitialInventory, scoreInitialQuote, scoreInitialFees                                               float64
	acquisitionEvaluations                                                                                   int
	acquisitionRejections                                                                                    map[string]int
	acquisitionDrawdownLimitSamples                                                                          int
	minAcquisitionDrawdownLimitBps, acquisitionDrawdownLimitSumBps                                           float64
	maxAcquisitionDrawdownLimitBps                                                                           float64
	maxUpProbabilityLower, maxAcquisitionIOCValueBps, maxAcquisitionImprovementBps                           float64
	lastDecisionSecond                                                                                       time.Time
	volumeProfileSamples, volumeProfileReadySamples                                                          int
	volatilityMinute                                                                                         time.Time
	cachedSideVolatility                                                                                     gammacapture.MarketMakerSideVolatilityEstimate
	asymmetricRiskMultiplierSum                                                                              float64
	asymmetricRiskSamples                                                                                    int
	asymmetricRiskMinMultiplier, asymmetricRiskMaxMultiplier                                                 float64
	asymmetricRiskReadySamples                                                                               int
	featureMinute                                                                                            time.Time
	cachedFeatures                                                                                           []float64
	cachedFeaturesReady                                                                                      bool
	fees, bidDistanceSum, askDistanceSum                                                                     float64
	distanceSamples                                                                                          int
	acquisitionQuantity, takerFees                                                                           float64
	quoteLifeSum                                                                                             float64
	quoteLifeSamples                                                                                         int
	activeDuration                                                                                           time.Duration
	fillsByDay                                                                                               map[string]*productionReplayDay
	fillEvents                                                                                               []replayFill
	equityCurve                                                                                              []productionEquityPoint
	maxDrawdownStopPct, equityPeak, maximumDrawdownPct                                                       float64
	stopped                                                                                                  bool
	stopAt                                                                                                   time.Time
	stopReason                                                                                               string
}

// productionReplayScoreAccount separates causal model preload from the live
// account snapshot at the scoring boundary. Synthetic preload fills are
// useful for maturing delayed labels, but they must not replace the inventory
// and cash that the live process actually had when it began quoting.
type productionReplayScoreAccount struct {
	PairEquityJPY float64
	Base          float64
}

type regimeExpectedValueSizingReplayResult struct {
	Plan       gammacapture.MarketMakerQuotePlan
	Projection gammacapture.ProbabilityCenteredQuoteDecision
	Decision   gammacapture.MarketMakerHorizonDecision
	Scale      float64
}

// applyRegimeExpectedValueSizing is deliberately a replay-only component
// boundary. It is called only after the ordinary joint terminal-wealth arm has
// rejected a quote. The base price, horizon, fill probabilities, balances and
// hard inventory limits are retained; only the binary no-order result is
// replaced by a continuous quantity scale.
func (s *productionReplayState) applyRegimeExpectedValueSizing(
	book bboSnapshot,
	horizon time.Duration,
	riskAversion float64,
	basePlan gammacapture.MarketMakerQuotePlan,
	baseProjection gammacapture.ProbabilityCenteredQuoteDecision,
	baseDecision gammacapture.MarketMakerHorizonDecision,
	fastDrift gammacapture.FastDriftDecision,
	currentInventory, targetInventory float64,
	pairEquity float64,
) (regimeExpectedValueSizingReplayResult, bool) {
	result := regimeExpectedValueSizingReplayResult{
		Plan: basePlan, Projection: baseProjection, Decision: baseDecision,
	}
	config := s.regimeExpectedValueSizer
	if config == nil || !config.Enabled || !baseProjection.Enabled ||
		baseProjection.ProjectedGrossNotionalJPY <= 0 || horizon <= 0 || pairEquity <= 0 {
		return result, false
	}
	buyDistance, sellDistance, _ := gammacapture.MakerTouchDistances(
		book.bid, book.ask, basePlan.BidPrice, basePlan.AskPrice)
	if buyDistance <= 0 || sellDistance <= 0 {
		return result, false
	}
	// Re-evaluate the same causal terminal paths with all exchange and
	// turnover costs removed. The sizing function then adds maker fee, adverse
	// selection and the turnover buffer exactly once from expected touches.
	grossConfig := s.cfg
	// MarketMakerConfig.setDefaults treats zero fees as "unset" and restores
	// the venue defaults (10/2 bps). Use a positive machine epsilon so this
	// gross counterfactual is genuinely cost-free; the sizing function below
	// adds the real fee/adverse/turnover costs exactly once.
	const replayZeroCostBps = 1e-9
	grossConfig.MakerFeeBps = replayZeroCostBps
	grossConfig.TakerFeeBps = replayZeroCostBps
	grossConfig.AdverseSelectionBps = replayZeroCostBps
	grossConfig.MinimumNetEdgeBps = 0
	stats := s.horizonModel.JointPathPayoffStatistics(
		book.time, grossConfig, horizon, buyDistance, sellDistance)
	if stats.EffectiveSamples <= 0 {
		return result, false
	}
	payoff := stats.EvaluateTargetRelativePosition(
		currentInventory,
		targetInventory,
		baseProjection.BuyNotionalJPY,
		baseProjection.SellNotionalJPY,
		pairEquity, riskAversion, 0)

	regimeReliability := s.regimeTargetDecision.Reliability
	regimeDirection := s.regimeTargetDecision.DirectionScore
	regimeSamples := s.regimeTargetDecision.EffectiveSamples
	regimeDecisionReady := s.regimeTargetDecision.Ready
	usedFastDriftFallback := false
	if !s.regimeTargetDecision.Ready && fastDrift.Healthy && fastDrift.ValidationSamples > 0 {
		regimeReliability = fastDrift.Strength
		regimeSamples = float64(fastDrift.ValidationSamples)
		variance := math.Max(1, fastDrift.CenterPredictiveBps2)
		regimeDirection = math.Tanh(fastDrift.CenterMeanBps / math.Sqrt(variance))
		usedFastDriftFallback = true
	}
	if !regimeDecisionReady {
		s.regimeEVSizingRegimeNotReady++
	}
	if usedFastDriftFallback {
		s.regimeEVSizingFastDriftFallback++
	}
	effectiveSamples := math.Min(stats.EffectiveSamples, regimeSamples)
	s.regimeEVSizingEffectiveSamplesSum += effectiveSamples
	s.regimeEVSizingRegimeReliabilitySum += regimeReliability
	decision := gammacapture.EvaluateRegimeExpectedValueSizing(
		*config,
		gammacapture.RegimeExpectedValueSizingInput{
			GrossExpectedValueJPY: payoff.ExpectedPnLJPY,
			GrossStdErrorJPY:      payoff.StdErrorJPY,
			EffectiveSamples:      effectiveSamples,
			RegimeReliability:     regimeReliability,
			RegimeDirectionScore:  regimeDirection,
			PairEquityJPY:         pairEquity,
			BuyNotionalJPY:        baseProjection.BuyNotionalJPY,
			SellNotionalJPY:       baseProjection.SellNotionalJPY,
			BuyFillProbability:    baseDecision.BuyTouchProbability,
			SellFillProbability:   baseDecision.SellTouchProbability,
			BothFillProbability:   baseDecision.BothTouchProbability,
		})
	result.Scale = decision.Scale
	s.regimeEVSizingEvaluations++
	if !decision.Ready {
		s.regimeEVSizingNotReady++
	}
	if s.regimeEVSizingReasons == nil {
		s.regimeEVSizingReasons = make(map[string]int)
	}
	s.regimeEVSizingReasons[decision.Reason]++
	s.regimeEVSizingScaleSum += decision.Scale
	s.regimeEVSizingGrossValueSum += decision.GrossExpectedValueJPY
	s.regimeEVSizingNetValueSum += decision.NetExpectedValueJPY
	s.regimeEVSizingUtilitySum += decision.ExpectedUtilityJPY
	s.regimeEVSizingExpectedFeesSum += decision.ExpectedFeeJPY
	if decision.Scale <= 0 {
		s.regimeEVSizingZeroScale++
		return result, true
	}
	s.regimeEVSizingApplied++
	result.Projection.BuyNotionalJPY *= decision.Scale
	result.Projection.SellNotionalJPY *= decision.Scale
	result.Projection.ProjectedGrossNotionalJPY =
		result.Projection.BuyNotionalJPY + result.Projection.SellNotionalJPY
	result.Projection.Reason = "regime expected-value quantity scale after fee-net rejection"
	result.Plan.AllowBid = result.Plan.AllowBid && result.Projection.BuyNotionalJPY > 0
	result.Plan.AllowAsk = result.Plan.AllowAsk && result.Projection.SellNotionalJPY > 0
	result.Plan.BidQuoteNotional = result.Projection.BuyNotionalJPY
	result.Plan.AskQuoteNotional = result.Projection.SellNotionalJPY
	return result, true
}

func runProductionComparison(in productionComparisonInput) {
	validationStage, err := normalizeProductionReplayValidationStage(in.ValidationStage)
	if err != nil {
		fatalf("production replay validation stage: %v", err)
	}
	barrier, intensity, cfg := loadProductionConfig(in.ConfigPath, in.Symbol)
	if in.EnableAsymmetricRisk {
		// Research-only override: keep the live YAML and replay-cache
		// fingerprint unchanged while enabling the candidate arm in memory.
		cfg.AsymmetricOscillationRisk.Enabled = true
		cfg.AsymmetricOscillationRisk.ShadowOnly = false
	}
	lifecycleEnabled := cfg.QuoteLifecycleAction.Enabled
	// The lifecycle flag is a dedicated research comparison. Keep every
	// existing arm of this report on the prior quote policy so the incremental
	// result cannot be hidden by changing the baseline itself.
	cfg.QuoteLifecycleAction.Enabled = false
	cfg.QuoteLifecycleAction.ShadowOnly = false
	baselineCfg := cfg
	// The comparison baseline is the identical quote policy with only the new
	// one-dimensional risk multiplier removed. This keeps fees, horizons,
	// balances, queue calibration, and execution semantics paired.
	baselineCfg.AsymmetricOscillationRisk.Enabled = false
	comparisonBaselineCfg := baselineCfg
	if in.TargetActionValueOnly {
		comparisonBaselineCfg, cfg = targetActionValueComparisonArms(cfg)
	} else if in.PivotRegimeTargetOnly {
		comparisonBaselineCfg, cfg = pivotRegimeTargetComparisonArms(cfg)
	} else if in.CausalRegimeInventoryTargetOnly {
		comparisonBaselineCfg, cfg = causalRegimeInventoryTargetComparisonArms(cfg)
	}
	var artifact *gammacapture.HorizonTouchArtifact
	if cfg.HorizonTouchModel.Enabled {
		var err error
		artifact, err = gammacapture.LoadHorizonTouchArtifact(
			in.ModelPath, in.Symbol, cfg.HorizonTouchModel.MinimumBrierImprovementPct)
		if err != nil {
			fatalf("load production horizon-touch model: %v", err)
		}
	}
	productionMode := replayLegacy
	if cfg.HorizonTouchModel.Enabled && artifact != nil {
		productionMode = replayHorizonTouch
	}
	warmFrom, exactFrom := productionReplayLoadRange(cfg, in.From, in.CalibrationFrom)
	if !in.WarmupFrom.IsZero() {
		if in.WarmupFrom.After(exactFrom) {
			fatalf("production warmup start must not be after exact replay start")
		}
		warmFrom = in.WarmupFrom
	}
	books, trades, cacheHit := loadWarmReplayDatasetAtInterval(
		in.DataPath, in.Symbol, warmFrom, in.To, exactFrom,
		replayConfigFingerprint(in.ConfigPath), in.ReplayCacheDir, in.BBOInterval)
	books = compactBBO(books)
	trades = compactTrades(trades)
	if len(books) < 2 || len(trades) == 0 {
		fatalf("insufficient production replay events: bbo=%d trades=%d", len(books), len(trades))
	}
	preloadManifest, err := buildProductionReplayPreloadManifest(
		books, trades, warmFrom, exactFrom, in.From, in.To, in.BBOInterval, cacheHit)
	if err != nil {
		fatalf("production replay preload validation: %v", err)
	}
	calBooks := filterBBO(books, warmFrom, in.CalibrationTo)
	calTrades := filterTicks(trades, warmFrom, in.CalibrationTo)
	actualBuyFills, actualSellFills := in.ActualBuyFills, in.ActualSellFills
	var lifecycle *orderLifecycleReport
	if in.JournalPath != "" {
		lifecycle = buildOrderLifecycleReport(in.JournalPath, in.Symbol, in.CalibrationFrom, in.CalibrationTo, calTrades)
		actualBuyFills = lifecycle.ActualMakerBuyFills
		actualSellFills = lifecycle.ActualMakerSellFills
	}
	queueCandidates := []float64{0, .25, .5, 1, 2, 4, 8, 16}
	if in.QueueMultiplier >= 0 {
		queueCandidates = []float64{in.QueueMultiplier}
	}
	candidates := make([]productionReplayResult, 0, len(queueCandidates))
	selected, selectedIndex, bestScore := queueCandidates[0], 0, math.Inf(1)
	useWarmedBaselineAsCalibration := (in.TargetActionValueOnly || in.PivotRegimeTargetOnly || in.CausalRegimeInventoryTargetOnly) && in.QueueMultiplier >= 0
	if !useWarmedBaselineAsCalibration {
		for _, queue := range queueCandidates {
			r := simulateProductionPolicy(calBooks, calTrades, comparisonBaselineCfg, barrier, intensity, nil, replayLegacy, in.Symbol, in.PairEquityJPY, in.StartingBase, queue, in.CalibrationFrom)
			candidates = append(candidates, r)
			score := math.Abs(float64(r.BuyFills-actualBuyFills)) + math.Abs(float64(r.SellFills-actualSellFills)) + .25*math.Abs(float64(r.FullFills-actualBuyFills-actualSellFills))
			if score < bestScore {
				selected, selectedIndex, bestScore = queue, len(candidates)-1, score
			}
		}
	}
	calLegacy := productionReplayResult{}
	if len(candidates) > 0 {
		calLegacy = candidates[selectedIndex]
	}
	calibrationError := absInt(calLegacy.BuyFills-actualBuyFills) + absInt(calLegacy.SellFills-actualSellFills)
	calibrationPassed := calibrationError <= 1
	if shouldStopAfterCalibration(validationStage, calibrationPassed, in.AllowUncalibratedReplay) {
		report := productionComparisonReport{
			Symbol: in.Symbol, ValidationStage: string(validationStage), ProductionMode: productionMode,
			HorizonTouchEnabled: cfg.HorizonTouchModel.Enabled && artifact != nil,
			WarmupFrom:          warmFrom, ExactFrom: exactFrom,
			WarmupBBOEvents:      sort.Search(len(books), func(index int) bool { return !books[index].time.Before(exactFrom) }),
			WarmupTradeEvents:    sort.Search(len(trades), func(index int) bool { return !trades[index].time.Before(exactFrom) }),
			CalibrationActualBuy: actualBuyFills, CalibrationActualSell: actualSellFills,
			SelectedQueueMultiplier: selected, CalibrationPassed: calibrationPassed,
			CalibrationAbsError: calibrationError, CalibrationCandidates: candidates,
			CalibrationLegacy: calLegacy, Decision: "calibration stage complete",
			ReplayCacheHit:    cacheHit,
			ReplayBBOInterval: in.BBOInterval.String(), Preload: preloadManifest,
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			fatalf("encode calibration-only production replay: %v", err)
		}
		return
	}
	// A failed fit does not justify using the fitted queue multiplier. When no
	// explicit queue assumption was supplied, use a neutral prior for every
	// full arm. The replay still tests quote decisions and relative P&L, while
	// the report remains explicitly uncalibrated and cannot promote or tune
	// live policy.
	calibratedQueue := selected
	var replayQueueSource string
	selected, replayQueueSource = selectProductionReplayQueue(
		calibratedQueue, calibrationPassed, in.QueueMultiplier >= 0)
	if in.TargetActionValueOnly {
		// Both arms consume immutable market data and own independent model state.
		// Run their identical causal preload concurrently: this reduces wall time
		// without sharing learned state or changing either event order.
		type armResult struct {
			name   string
			replay productionReplayResult
		}
		armResults := make(chan armResult, 2)
		go func() {
			armResults <- armResult{name: "old", replay: simulateProductionPolicyForComparisonWithScoreAccountReset(
				books, trades, comparisonBaselineCfg, barrier, intensity, nil, replayLegacy,
				in.Symbol, in.PairEquityJPY, in.StartingBase, selected, warmFrom, in.From)}
		}()
		go func() {
			armResults <- armResult{name: "single", replay: simulateProductionPolicyForComparisonWithScoreAccountReset(
				books, trades, cfg, barrier, intensity, nil, replayLegacy,
				in.Symbol, in.PairEquityJPY, in.StartingBase, selected, warmFrom, in.From)}
		}()
		var old, single productionReplayResult
		for range 2 {
			result := <-armResults
			if result.name == "old" {
				old = result.replay
			} else {
				single = result.replay
			}
		}
		if useWarmedBaselineAsCalibration {
			calLegacy = old
			candidates = []productionReplayResult{old}
		}
		calibrationError := absInt(calLegacy.BuyFills-actualBuyFills) + absInt(calLegacy.SellFills-actualSellFills)
		calibrationPassed := calibrationError <= 1
		report := productionComparisonReport{
			Symbol: in.Symbol, ValidationStage: string(validationStage), ProductionMode: productionMode,
			HorizonTouchEnabled: cfg.HorizonTouchModel.Enabled && artifact != nil,
			WarmupFrom:          warmFrom, ExactFrom: exactFrom,
			WarmupBBOEvents:      sort.Search(len(books), func(index int) bool { return !books[index].time.Before(exactFrom) }),
			WarmupTradeEvents:    sort.Search(len(trades), func(index int) bool { return !trades[index].time.Before(exactFrom) }),
			CalibrationActualBuy: actualBuyFills, CalibrationActualSell: actualSellFills,
			SelectedQueueMultiplier: selected, CalibrationPassed: calibrationPassed,
			CalibrationAbsError: calibrationError, CalibrationCandidates: candidates,
			CalibrationLegacy: calLegacy, FullLegacy: old,
			FullStackedTarget: old, FullSingleActionTarget: single,
			Decision:       compareReplayPolicies(old, single, calibrationPassed),
			ReplayCacheHit: cacheHit, ReplayBBOInterval: in.BBOInterval.String(), Preload: preloadManifest,
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			fatalf("encode target action-value comparison: %v", err)
		}
		return
	}
	if in.PivotRegimeTargetOnly || in.CausalRegimeInventoryTargetOnly {
		// Both arms consume the same immutable BBO/trade stream and perform their
		// own causal pivot filtering. Only the target source differs: baseline is
		// the current posterior/50% policy, candidate is pivot-first.
		type pivotArmResult struct {
			name   string
			replay productionReplayResult
		}
		armResults := make(chan pivotArmResult, 2)
		go func() {
			armResults <- pivotArmResult{name: "baseline", replay: simulateProductionPolicyForComparisonWithScoreAccountReset(
				books, trades, comparisonBaselineCfg, barrier, intensity, nil, replayLegacy,
				in.Symbol, in.PairEquityJPY, in.StartingBase, selected, warmFrom, in.From)}
		}()
		go func() {
			armResults <- pivotArmResult{name: "pivot", replay: simulateProductionPolicyForComparisonWithScoreAccountReset(
				books, trades, cfg, barrier, intensity, nil, replayLegacy,
				in.Symbol, in.PairEquityJPY, in.StartingBase, selected, warmFrom, in.From)}
		}()
		var baseline, pivot productionReplayResult
		for range 2 {
			result := <-armResults
			if result.name == "baseline" {
				baseline = result.replay
			} else {
				pivot = result.replay
			}
		}
		if useWarmedBaselineAsCalibration {
			calLegacy = baseline
			candidates = []productionReplayResult{baseline}
		}
		calibrationError := absInt(calLegacy.BuyFills-actualBuyFills) + absInt(calLegacy.SellFills-actualSellFills)
		calibrationPassed := calibrationError <= 1
		report := productionComparisonReport{
			Symbol: in.Symbol, ValidationStage: string(validationStage), ProductionMode: productionMode,
			HorizonTouchEnabled: cfg.HorizonTouchModel.Enabled && artifact != nil,
			WarmupFrom:          warmFrom, ExactFrom: exactFrom,
			WarmupBBOEvents:      sort.Search(len(books), func(index int) bool { return !books[index].time.Before(exactFrom) }),
			WarmupTradeEvents:    sort.Search(len(trades), func(index int) bool { return !trades[index].time.Before(exactFrom) }),
			CalibrationActualBuy: actualBuyFills, CalibrationActualSell: actualSellFills,
			SelectedQueueMultiplier: selected, ReplayQueueSource: replayQueueSource,
			CalibrationPassed: calibrationPassed, CalibrationAbsError: calibrationError,
			CalibrationCandidates: candidates, CalibrationLegacy: calLegacy,
			FullLegacy:     baseline,
			Decision:       compareReplayPolicies(baseline, pivot, calibrationPassed),
			ReplayCacheHit: cacheHit, ReplayBBOInterval: in.BBOInterval.String(), Preload: preloadManifest,
		}
		if in.CausalRegimeInventoryTargetOnly {
			report.FullCausalRegimeTarget = pivot
		} else {
			report.FullPivotRegimeTarget = pivot
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			fatalf("encode pivot-regime target comparison: %v", err)
		}
		return
	}
	if in.NormalFlowPressureOnly {
		// Isolate exactly one change: the bounded ordinary-flow fallback. The
		// selected quantity scale is a replay-only sizing experiment and never
		// mutates the YAML strategy. Run scales separately because the production
		// state kernel is intentionally not a shared concurrent simulator.
		type normalFlowArmResult struct {
			name   string
			replay productionReplayResult
		}
		scale := in.NormalFlowPressureRiskBudgetScale
		if scale <= 0 || scale > 1 {
			scale = 1
		}
		armResults := make(chan normalFlowArmResult, 2)
		go func() {
			armResults <- normalFlowArmResult{name: "legacy", replay: simulateProductionPolicyForComparison(
				books, trades, comparisonBaselineCfg, barrier, intensity, nil, replayLegacy,
				in.Symbol, in.PairEquityJPY, in.StartingBase, selected, warmFrom, in.From)}
		}()
		go func() {
			candidateCfg := comparisonBaselineCfg
			candidateCfg.NormalFlowPressure.Enabled = true
			candidateCfg.InventoryRiskBudgetJPY *= scale
			candidateCfg.InventoryRiskBudgetRatio *= scale
			armResults <- normalFlowArmResult{name: "normal-flow", replay: simulateProductionPolicyForComparison(
				books, trades, candidateCfg, barrier, intensity, nil, replayLegacy,
				in.Symbol, in.PairEquityJPY, in.StartingBase, selected, warmFrom, in.From)}
		}()
		var old, normalFlow productionReplayResult
		for range 2 {
			result := <-armResults
			if result.name == "legacy" {
				old = result.replay
				continue
			}
			normalFlow = result.replay
		}
		quantityCandidates := []normalFlowPressureQuantityReplaySummary{{
			RiskBudgetScale: scale, Replay: summarizeProductionReplayResult(normalFlow),
		}}
		componentCalibrationError := absInt(calLegacy.BuyFills-actualBuyFills) + absInt(calLegacy.SellFills-actualSellFills)
		componentCalibrationPassed := componentCalibrationError <= 1
		report := normalFlowPressureReplaySummary{
			Symbol:     in.Symbol,
			WarmupFrom: warmFrom, ExactFrom: exactFrom,
			WarmupBBOEvents:      sort.Search(len(books), func(index int) bool { return !books[index].time.Before(exactFrom) }),
			WarmupTradeEvents:    sort.Search(len(trades), func(index int) bool { return !trades[index].time.Before(exactFrom) }),
			CalibrationActualBuy: actualBuyFills, CalibrationActualSell: actualSellFills,
			SelectedQueueMultiplier: selected, CalibrationPassed: componentCalibrationPassed,
			CalibrationAbsoluteErr: componentCalibrationError,
			Baseline:               summarizeProductionReplayResult(old),
			NormalFlowPressure:     summarizeProductionReplayResult(normalFlow),
			QuantityCandidates:     quantityCandidates,
			Decision:               compareReplayPolicies(old, normalFlow, componentCalibrationPassed), ReplayCacheHit: cacheHit,
			ReplayBBOInterval: in.BBOInterval.String(),
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			fatalf("encode normal-flow-pressure component replay: %v", err)
		}
		return
	}
	if in.FeeFreeCounterfactualOnly {
		// The accounting-only counterfactual is exact for P&L attribution: it
		// removes recorded maker/taker fees from the same realized path without
		// changing quotes or fills. Drawdown is intentionally not relabeled here,
		// because a fee-free equity path needs a separate state replay.
		old := simulateProductionPolicyForComparisonWithScoreAccountReset(
			books, trades, comparisonBaselineCfg, barrier, intensity, nil, replayLegacy,
			in.Symbol, in.PairEquityJPY, in.StartingBase, selected, warmFrom, in.From)
		accountingFree := summarizeProductionReplayResult(old)
		feesRemoved := old.MakerFeesJPY + old.TakerFeesJPY
		accountingFree.NetPnLJPY += feesRemoved
		accountingFree.MakerFeesJPY = 0

		// Policy-free means explicit maker/taker fees are set to an epsilon and
		// the fee component of the minimum quote floor is removed. Adverse
		// selection and the configured minimum net edge remain, so this is not a
		// frictionless omniscient market; it is a zero-exchange-fee market-maker
		// counterfactual with the same risk model.
		feeFreeCfg := comparisonBaselineCfg
		feeFreeCfg.MakerFeeBps = 1e-9
		feeFreeCfg.TakerFeeBps = 1e-9
		feeFreeCfg.MinimumHalfSpreadBps = math.Max(
			1e-9, feeFreeCfg.AdverseSelectionBps+feeFreeCfg.MinimumNetEdgeBps/2)
		feeFree := simulateProductionPolicyForComparisonWithScoreAccountReset(
			books, trades, feeFreeCfg, barrier, intensity, nil, replayLegacy,
			in.Symbol, in.PairEquityJPY, in.StartingBase, selected, warmFrom, in.From)
		calibrationError := absInt(calLegacy.BuyFills-actualBuyFills) + absInt(calLegacy.SellFills-actualSellFills)
		report := feeFreeCounterfactualSummary{
			Symbol: in.Symbol, WarmupFrom: warmFrom, ExactFrom: exactFrom,
			CalibrationActualBuy: actualBuyFills, CalibrationActualSell: actualSellFills,
			CalibrationPassed: calibrationError <= 1, CalibrationAbsoluteErr: calibrationError,
			Baseline: summarizeProductionReplayResult(old), AccountingFeeFreeGross: accountingFree,
			PolicyFeeFree: summarizeProductionReplayResult(feeFree), FeesRemovedJPY: feesRemoved,
			AccountingOnlyDDNote:    "accounting-only gross PnL removes fees algebraically; drawdown remains the fee-aware realized path",
			PolicyFeeFreeDefinition: "maker/taker fees set to epsilon; explicit maker-fee quote floor removed; adverse selection and minimum net edge retained",
			Decision:                "diagnostic only: private-fill calibration is required before interpreting fee-free replay as executable evidence",
			ReplayCacheHit:          cacheHit, ReplayBBOInterval: in.BBOInterval.String(),
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			fatalf("encode fee-free counterfactual: %v", err)
		}
		return
	}
	if in.HorizonConditionedUtilityOnly {
		// The candidate is a paired strategy replay arm. It keeps the ordinary
		// quote price, direction, inventory bounds, and next-BBO execution engine;
		// only the pivot-aligned quantity is scaled by the HCU decision. Its
		// training labels are public-touch shadow labels, not private fills.
		old := simulateProductionPolicyForComparisonWithScoreAccountReset(
			books, trades, comparisonBaselineCfg, barrier, intensity, nil, replayLegacy,
			in.Symbol, in.PairEquityJPY, in.StartingBase, selected, warmFrom, in.From)
		candidate := simulateProductionPolicyForComparisonWithHorizonConditionedUtility(
			books, trades, comparisonBaselineCfg, barrier, intensity, nil, replayLegacy,
			in.Symbol, in.PairEquityJPY, in.StartingBase, selected, warmFrom, in.From)
		calibrationError := absInt(calLegacy.BuyFills-actualBuyFills) + absInt(calLegacy.SellFills-actualSellFills)
		report := productionComparisonReport{
			Symbol: in.Symbol, ValidationStage: string(validationStage), ProductionMode: productionMode,
			HorizonTouchEnabled: cfg.HorizonTouchModel.Enabled && artifact != nil,
			WarmupFrom:          warmFrom, ExactFrom: exactFrom,
			WarmupBBOEvents:      sort.Search(len(books), func(index int) bool { return !books[index].time.Before(exactFrom) }),
			WarmupTradeEvents:    sort.Search(len(trades), func(index int) bool { return !trades[index].time.Before(exactFrom) }),
			CalibrationActualBuy: actualBuyFills, CalibrationActualSell: actualSellFills,
			SelectedQueueMultiplier: selected, CalibrationPassed: calibrationError <= 1,
			CalibrationAbsError: calibrationError, CalibrationCandidates: candidates,
			CalibrationLegacy: calLegacy, FullLegacy: old,
			FullHorizonConditionedUtility: candidate,
			Decision:                      "diagnostic strategy backtest only: HCU uses public-touch shadow labels; standalone alpha gate was rejected and private-fill calibration is unavailable",
			ReplayCacheHit:                cacheHit, ReplayBBOInterval: in.BBOInterval.String(), Preload: preloadManifest,
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			fatalf("encode horizon-conditioned utility strategy replay: %v", err)
		}
		return
	}
	if in.RegimeExpectedValueSizingOnly {
		// The candidate uses the same fee-aware configuration and the same
		// scoring-boundary account. It only replaces an authoritative terminal
		// value rejection with a replay-only continuous quantity decision.
		old := simulateProductionPolicyForComparisonWithScoreAccountReset(
			books, trades, comparisonBaselineCfg, barrier, intensity, nil, replayLegacy,
			in.Symbol, in.PairEquityJPY, in.StartingBase, selected, warmFrom, in.From)
		candidate := simulateProductionPolicyForComparisonWithRegimeExpectedValueSizing(
			books, trades, comparisonBaselineCfg, barrier, intensity, nil, replayLegacy,
			in.Symbol, in.PairEquityJPY, in.StartingBase, selected, warmFrom, in.From)
		calibrationError := absInt(calLegacy.BuyFills-actualBuyFills) + absInt(calLegacy.SellFills-actualSellFills)
		definition := "quantity-only replay alpha: preserve the base price/horizon and hard balance constraints; after the existing terminal-value rejection, evaluate zero-fee causal path gross value, subtract expected maker fee + adverse selection + minimum-net-edge/2 per touched side, apply regime/effective-sample shrinkage and quadratic uncertainty stress, then scale both candidate notionals continuously in [0,1]. Scale 0 remains no order; venue minimum-notional is the final exchange constraint."
		report := regimeExpectedValueSizingSummary{
			Symbol:     in.Symbol,
			WarmupFrom: warmFrom, ExactFrom: exactFrom,
			CalibrationActualBuy: actualBuyFills, CalibrationActualSell: actualSellFills,
			CalibrationPassed: calibrationError <= 1, CalibrationAbsoluteErr: calibrationError,
			Baseline:            summarizeProductionReplayResult(old),
			RegimeExpectedValue: summarizeProductionReplayResult(candidate),
			Definition:          definition,
			Decision:            "diagnostic component replay only: private-fill calibration and untouched chronological blocks are required before any live integration",
			ReplayCacheHit:      cacheHit, ReplayBBOInterval: in.BBOInterval.String(),
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			fatalf("encode regime expected-value sizing component replay: %v", err)
		}
		return
	}
	old := simulateProductionPolicyForComparison(books, trades, comparisonBaselineCfg, barrier, intensity, nil, replayLegacy, in.Symbol, in.PairEquityJPY, in.StartingBase, selected, warmFrom, in.From)
	if in.ComponentOnly {
		lifecycleCfg := cfg
		lifecycleCfg.QuoteLifecycleAction.Enabled = lifecycleEnabled
		lifecycleCfg.QuoteLifecycleAction.ShadowOnly = false
		lifecycleReplay := productionReplayResult{Mode: productionMode}
		if lifecycleEnabled {
			lifecycleReplay = simulateProductionPolicyForComparison(books, trades, lifecycleCfg, barrier, intensity, artifact, productionMode, in.Symbol, in.PairEquityJPY, in.StartingBase, selected, warmFrom, in.From)
		}
		calibrationError := absInt(calLegacy.BuyFills-actualBuyFills) + absInt(calLegacy.SellFills-actualSellFills)
		report := productionComparisonReport{
			Symbol: in.Symbol, ProductionMode: productionMode,
			HorizonTouchEnabled: cfg.HorizonTouchModel.Enabled && artifact != nil,
			WarmupFrom:          warmFrom, ExactFrom: exactFrom,
			WarmupBBOEvents:      sort.Search(len(books), func(index int) bool { return !books[index].time.Before(exactFrom) }),
			WarmupTradeEvents:    sort.Search(len(trades), func(index int) bool { return !trades[index].time.Before(exactFrom) }),
			CalibrationActualBuy: actualBuyFills, CalibrationActualSell: actualSellFills,
			SelectedQueueMultiplier: selected, CalibrationPassed: calibrationError <= 1,
			CalibrationAbsError: calibrationError, CalibrationCandidates: candidates,
			CalibrationLegacy: calLegacy, FullLegacy: old, FullQuoteLifecycle: lifecycleReplay,
			Decision: compareReplayPolicies(old, lifecycleReplay, calibrationError <= 1), ReplayCacheHit: cacheHit,
			ReplayBBOInterval: in.BBOInterval.String(), Preload: preloadManifest,
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			fatalf("encode lifecycle component replay: %v", err)
		}
		return
	}
	asymmetric := simulateProductionPolicyForComparison(books, trades, cfg, barrier, intensity, nil, replayLegacy, in.Symbol, in.PairEquityJPY, in.StartingBase, selected, warmFrom, in.From)
	if in.AsymmetricRiskOnly {
		componentCalibrationError := absInt(calLegacy.BuyFills-actualBuyFills) + absInt(calLegacy.SellFills-actualSellFills)
		componentCalibrationPassed := componentCalibrationError <= 1
		componentWarmupBBOEvents := sort.Search(len(books), func(index int) bool { return !books[index].time.Before(exactFrom) })
		componentWarmupTradeEvents := sort.Search(len(trades), func(index int) bool { return !trades[index].time.Before(exactFrom) })
		report := productionComparisonReport{
			Symbol: in.Symbol, ValidationStage: string(validationStage), WarmupFrom: warmFrom, ExactFrom: exactFrom,
			WarmupBBOEvents: componentWarmupBBOEvents, WarmupTradeEvents: componentWarmupTradeEvents,
			CalibrationActualBuy: actualBuyFills, CalibrationActualSell: actualSellFills,
			SelectedQueueMultiplier: selected, CalibrationPassed: componentCalibrationPassed,
			CalibrationAbsError: componentCalibrationError, CalibrationCandidates: candidates,
			CalibrationLegacy: calLegacy, FullLegacy: old, FullAsymmetricRisk: asymmetric,
			Decision: compareReplayPolicies(old, asymmetric, componentCalibrationPassed), ReplayCacheHit: cacheHit,
			ReplayBBOInterval: in.BBOInterval.String(), Preload: preloadManifest,
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			fatalf("encode asymmetric-risk component replay: %v", err)
		}
		return
	}
	volumeProfileCfg := cfg
	// The candidate changes exactly one integration point: public-trade volume
	// profile conditioning inside the existing conditional execution kernel.
	// It does not add a quote/quantity gate or alter the exchange simulator.
	volumeProfileCfg.VolumeProfile.Enabled = true
	volumeProfileCfg.VolumeProfile.ShadowOnly = false
	volumeProfile := simulateProductionPolicyForComparison(books, trades, volumeProfileCfg, barrier, intensity, nil, replayLegacy, in.Symbol, in.PairEquityJPY, in.StartingBase, selected, warmFrom, in.From)
	volumeProfilePOCRiskCfg := volumeProfileCfg
	volumeProfilePOCRiskCfg.VolumeProfile.AsymmetricPOCRisk = true
	volumeProfilePOCRisk := simulateProductionPolicyForComparison(books, trades, volumeProfilePOCRiskCfg, barrier, intensity, nil, replayLegacy, in.Symbol, in.PairEquityJPY, in.StartingBase, selected, warmFrom, in.From)
	current := simulateProductionPolicyForComparison(books, trades, cfg, barrier, intensity, artifact, productionMode, in.Symbol, in.PairEquityJPY, in.StartingBase, selected, warmFrom, in.From)
	lifecycleCfg := cfg
	lifecycleCfg.QuoteLifecycleAction.Enabled = lifecycleEnabled
	lifecycleCfg.QuoteLifecycleAction.ShadowOnly = false
	lifecycleReplay := productionReplayResult{Mode: productionMode}
	if lifecycleEnabled {
		lifecycleReplay = simulateProductionPolicyForComparison(books, trades, lifecycleCfg, barrier, intensity, artifact, productionMode, in.Symbol, in.PairEquityJPY, in.StartingBase, selected, warmFrom, in.From)
	}
	acquisition := simulateProductionPolicyForComparison(books, trades, baselineCfg, barrier, intensity, nil, replayAcquisitionReset, in.Symbol, in.PairEquityJPY, in.StartingBase, selected, warmFrom, in.From)
	// Relative-Hold is evaluated as one additional Fast joint utility scalar.
	// The arm uses the same horizon-touch policy, execution simulator, queue
	// calibration and causal preload as current; only matured same-symbol
	// strategy-vs-Hold labels are enabled.
	relativeHoldCfg := cfg
	relativeHoldCfg.RelativeHoldRisk = relativeHoldRiskStudyConfig(time.Hour)
	relativeHold := simulateProductionPolicyForComparison(
		books, trades, relativeHoldCfg, barrier, intensity, artifact,
		productionMode, in.Symbol, in.PairEquityJPY, in.StartingBase, selected, warmFrom, in.From)
	calibrationError = absInt(calLegacy.BuyFills-actualBuyFills) + absInt(calLegacy.SellFills-actualSellFills)
	calibrationPassed = calibrationError <= 1
	if lifecycle != nil && !lifecycle.CalibrationPassed {
		calibrationPassed = false
	}
	warmupBBOEvents := sort.Search(len(books), func(index int) bool {
		return !books[index].time.Before(exactFrom)
	})
	warmupTradeEvents := sort.Search(len(trades), func(index int) bool {
		return !trades[index].time.Before(exactFrom)
	})
	fullHorizonTouch := productionReplayResult{Mode: replayHorizonTouchOff}
	if cfg.HorizonTouchModel.Enabled && artifact != nil {
		fullHorizonTouch = current
	}
	report := productionComparisonReport{Symbol: in.Symbol, ValidationStage: string(validationStage), ProductionMode: productionMode, HorizonTouchEnabled: cfg.HorizonTouchModel.Enabled && artifact != nil, WarmupFrom: warmFrom, ExactFrom: exactFrom, WarmupBBOEvents: warmupBBOEvents, WarmupTradeEvents: warmupTradeEvents, CalibrationActualBuy: actualBuyFills, CalibrationActualSell: actualSellFills, CalibratedQueueMultiplier: calibratedQueue, SelectedQueueMultiplier: selected, ReplayQueueSource: replayQueueSource, CalibrationPassed: calibrationPassed, CalibrationAbsError: calibrationError, CalibrationCandidates: candidates, CalibrationLegacy: calLegacy, FullLegacy: old, FullAsymmetricRisk: asymmetric, FullVolumeProfile: volumeProfile, FullVolumeProfilePOCRisk: volumeProfilePOCRisk, FullHorizonTouch: fullHorizonTouch, FullQuoteLifecycle: lifecycleReplay, FullAcquisitionReset: acquisition, FullRelativeHold: relativeHold, JournalLifecycle: lifecycle, Decision: compareReplayPolicies(old, current, calibrationPassed), ReplayCacheHit: cacheHit, ReplayBBOInterval: in.BBOInterval.String(), Preload: preloadManifest}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fatalf("encode production comparison: %v", err)
	}
}

func compactProductionReplayResultForSummary(result productionReplayResult) productionReplayResult {
	result.FillEvents = nil
	result.JointQuoteDecisions = nil
	result.MacroActiveDecisions = nil
	result.FastTargetActiveDecisions = nil
	result.EquityCurve = nil
	result.DayResults = nil
	return result
}

// targetActionValueComparisonArms constructs the two policies explicitly.
// Target-action replay must not inherit whether a production YAML happens to
// enable DynamicInventoryAim: the retired arm is the legacy stacked target,
// while the candidate arm is the single bounded regime-conditioned target.
// Keeping this construction in one function makes the comparison contract
// testable and prevents an omitted YAML field from silently disabling the
// candidate under test.
func targetActionValueComparisonArms(cfg gammacapture.MarketMakerConfig) (baseline, candidate gammacapture.MarketMakerConfig) {
	baseline = cfg
	baseline.DynamicInventoryAim.Enabled = false
	baseline.DynamicInventoryAim.ShadowOnly = false
	baseline.DynamicInventoryAim.RegimeConditionedTarget.Enabled = false
	baseline.JointDistanceQuantity.LegacyStackedTargetContinuation = true

	candidate = cfg
	candidate.DynamicInventoryAim.Enabled = true
	candidate.DynamicInventoryAim.ShadowOnly = false
	candidate.DynamicInventoryAim.RegimeConditionedTarget.Enabled = true
	candidate.JointDistanceQuantity.LegacyStackedTargetContinuation = false
	return baseline, candidate
}

// pivotRegimeTargetComparisonArms compares the current posterior/50% target
// with the causal pivot-first actuator. The candidate changes only the
// inventory target source; prices, Fast quantities, crossing evidence, fees,
// queue assumptions, and the terminal-wealth admission path remain paired.
func pivotRegimeTargetComparisonArms(cfg gammacapture.MarketMakerConfig) (baseline, candidate gammacapture.MarketMakerConfig) {
	baseline = cfg
	baseline.DynamicInventoryAim.Enabled = false
	baseline.DynamicInventoryAim.ShadowOnly = false
	baseline.DynamicInventoryAim.RegimeConditionedTarget.Enabled = false
	baseline.DynamicInventoryAim.PivotRegimeTarget.Enabled = false

	candidate = baseline
	candidate.DynamicInventoryAim.Enabled = true
	candidate.DynamicInventoryAim.ShadowOnly = false
	candidate.DynamicInventoryAim.RegimeConditionedTarget.Enabled = false
	candidate.DynamicInventoryAim.PivotRegimeTarget.Enabled = true
	return baseline, candidate
}

// causalRegimeInventoryTargetComparisonArms compares the current posterior
// target with the causal pivot CE target. The candidate disables the existing
// posterior target so two inventory target owners cannot stack; all quote,
// price, quantity, fill and lifecycle settings remain inherited and paired.
func causalRegimeInventoryTargetComparisonArms(cfg gammacapture.MarketMakerConfig) (baseline, candidate gammacapture.MarketMakerConfig) {
	baseline = cfg
	baseline.DynamicInventoryAim.Enabled = false
	baseline.DynamicInventoryAim.ShadowOnly = false
	baseline.DynamicInventoryAim.RegimeConditionedTarget.Enabled = false
	baseline.DynamicInventoryAim.PivotRegimeTarget.Enabled = false

	candidate = baseline
	candidate.PosteriorInventoryTarget = false
	candidate.DynamicInventoryAim.PivotRegimeTarget.CausalCEEnabled = true
	return baseline, candidate
}

func loadProductionConfig(path, symbol string) (gammacapture.BarrierConfig, gammacapture.IntensityConfig, gammacapture.MarketMakerConfig) {
	data, err := os.ReadFile(path)
	if err != nil {
		fatalf("read production config: %v", err)
	}
	var envelope productionYAML
	if err := yaml.Unmarshal(data, &envelope); err != nil {
		fatalf("decode production config: %v", err)
	}
	for _, entry := range envelope.ExchangeStrategies {
		if entry.GammaCapture.Symbol == symbol {
			return entry.GammaCapture.Barrier, entry.GammaCapture.Intensity, activeProductionConfigOverrides.apply(entry.GammaCapture.MarketMaker)
		}
	}
	fatalf("GammaCapture symbol %s not found in %s", symbol, path)
	return gammacapture.BarrierConfig{}, gammacapture.IntensityConfig{}, gammacapture.MarketMakerConfig{}
}

func newProductionReplayState(cfg gammacapture.MarketMakerConfig, barrier gammacapture.BarrierConfig, intensity gammacapture.IntensityConfig, artifact *gammacapture.HorizonTouchArtifact, mode replayPolicyMode, symbol string, startQuote, startBase, queue float64, tradingFrom time.Time, useProbabilityProjection bool, regimeExpectedValueSizer ...*gammacapture.RegimeExpectedValueSizingConfig) *productionReplayState {
	return newProductionReplayStateWithOptions(cfg, barrier, intensity, artifact, mode, symbol, startQuote, startBase, queue, tradingFrom, useProbabilityProjection, regimeExpectedValueSizer, false)
}

func newProductionReplayStateWithOptions(cfg gammacapture.MarketMakerConfig, barrier gammacapture.BarrierConfig, intensity gammacapture.IntensityConfig, artifact *gammacapture.HorizonTouchArtifact, mode replayPolicyMode, symbol string, startQuote, startBase, queue float64, tradingFrom time.Time, useProbabilityProjection bool, regimeExpectedValueSizer []*gammacapture.RegimeExpectedValueSizingConfig, preloadOnly bool) *productionReplayState {
	fastWindows := cfg.FastModelWindows()
	fastModels := make(map[time.Duration]*gammacapture.IntensityModel, len(fastWindows))
	fastEvidenceModels := make(map[time.Duration]*gammacapture.FastEvidenceModel, len(fastWindows))
	for _, fastWindow := range fastWindows {
		fastModels[fastWindow] = gammacapture.NewIntensityModel(gammacapture.IntensityConfig{
			Window: types.Duration(fastWindow), VolatilityWindow: types.Duration(fastWindow),
			PriorAlphaUp: 1, PriorBetaUp: 10, PriorAlphaDown: 1, PriorBetaDown: 10, MinEvents: 1,
		})
		evidenceWindow := fastWindow
		if len(cfg.FastWindows) == 0 {
			evidenceWindow = time.Duration(cfg.FastEvidenceWindow)
		}
		fastEvidenceModels[fastWindow] = gammacapture.NewFastEvidenceModel(gammacapture.FastEvidenceConfig{
			Window: evidenceWindow, MinTrades: cfg.FastEvidenceMinTrades, MinBBOUpdates: cfg.FastEvidenceMinBBOUpdates,
		})
	}
	var regimeSizer *gammacapture.RegimeExpectedValueSizingConfig
	if len(regimeExpectedValueSizer) > 0 {
		regimeSizer = regimeExpectedValueSizer[0]
	}
	state := &productionReplayState{
		cfg: cfg, artifact: artifact, mode: mode, symbol: symbol, queueFactor: queue, barrierWidth: barrier.Width, useProbabilityProjection: useProbabilityProjection, regimeExpectedValueSizer: regimeSizer,
		engine:                  gammacapture.NewCrossingEngine(barrier.Width, time.Duration(barrier.MinDwell), barrier.MaxCrossingsPerEvent),
		slowModel:               gammacapture.NewIntensityModel(intensity),
		executableCrossingModel: gammacapture.NewExecutableCrossingModel(symbol, barrier, intensity),
		fastModels:              fastModels, fastEvidenceModels: fastEvidenceModels, fastWindows: append([]time.Duration(nil), fastWindows...),
		quoteLifecycleHazard:       gammacapture.NewQuoteLifecycleHazardModel(cfg.QuoteLifecycleAction.Hazard),
		bocpd45DirectionEnabled:    cfg.BOCPD45.Enabled || activeProductionConfigOverrides.EnableBOCPD45Direction,
		postFillUtilityReasons:     make(map[string]int),
		quoteLifecycleActions:      make(map[string]int),
		dynamicInventoryAimReasons: make(map[string]int),
		causalRegimeTargetReasons:  make(map[string]int),
		horizonDiagnostics:         make(map[string]*productionReplayHorizonDiagnostic),
		inventory:                  startBase, quote: startQuote, initialEquity: startQuote,
		initialInventory: startBase, initialQuote: startQuote, tradingFrom: tradingFrom,
		scoreFrom:             tradingFrom,
		fillsByDay:            make(map[string]*productionReplayDay),
		acquisitionRejections: make(map[string]int),
	}
	state.preloadOnly = preloadOnly
	if (cfg.DynamicInventoryAim.Enabled && cfg.DynamicInventoryAim.PivotRegimeTarget.Enabled) ||
		cfg.DynamicInventoryAim.PivotRegimeTarget.CausalCEEnabled {
		pivotConfig := cfg.DynamicInventoryAim.PivotRegimeTarget
		state.pivotRegimeFilter = gammacapture.NewPivotRegimeFilter(gammacapture.PivotRegimeConfig{
			ReversalBps:     pivotConfig.ReversalBps,
			MaxGap:          time.Duration(pivotConfig.MaxGap),
			MinLegSamples:   pivotConfig.MinLegSamples,
			PriorLegSamples: pivotConfig.PriorLegSamples,
		})
		state.pivotRegimeDecision = gammacapture.PivotRegimeDecision{Reason: "pivot regime warming"}
	}
	if cfg.AsymmetricOscillationRisk.Enabled {
		state.asymmetricOscillationRisk = gammacapture.NewAsymmetricOscillationRiskModel(
			cfg.AsymmetricOscillationRisk)
		state.asymmetricRiskDecision = gammacapture.AsymmetricOscillationRiskDecision{
			Enabled: true, RiskMultiplier: 1, Reason: "asymmetric oscillation risk warming",
		}
	}
	if cfg.RelativeHoldRisk.Enabled {
		state.relativeHoldRiskModel = gammacapture.NewRelativeHoldRiskModel(cfg.RelativeHoldRisk)
		state.relativeHoldRiskHorizon = cfg.RelativeHoldRisk.Horizon
		if state.relativeHoldRiskHorizon <= 0 {
			state.relativeHoldRiskHorizon = time.Hour
		}
	}
	if state.bocpd45DirectionEnabled {
		calibration := cfg.BOCPD45.Calibration
		if activeProductionConfigOverrides.EnableBOCPD45Direction {
			calibration = activeProductionConfigOverrides.BOCPD45Calibration
		}
		state.bocpd45Calibration = newBOCPD45PrequentialCalibration(
			parseBOCPD45CalibrationMethod(calibration))
	}
	return state
}

func simulateProductionPolicy(books []bboSnapshot, trades []tick, cfg gammacapture.MarketMakerConfig, barrier gammacapture.BarrierConfig, intensity gammacapture.IntensityConfig, artifact *gammacapture.HorizonTouchArtifact, mode replayPolicyMode, symbol string, pairEquity, startBase, queue float64, tradingFrom time.Time) productionReplayResult {
	return simulateProductionPolicyWithQuantityProjection(books, trades, cfg, barrier, intensity, artifact, mode, symbol, pairEquity, startBase, queue, tradingFrom, true, 0)
}

// simulateProductionPolicyForComparison keeps full factor arms on the same
// causal lifecycle as the production replay: estimators and delayed labels
// warm from warmFrom, while P&L/fill counters are scored only from scoreFrom.
// The queue-calibration arm intentionally remains a separate short replay.
func simulateProductionPolicyForComparison(books []bboSnapshot, trades []tick, cfg gammacapture.MarketMakerConfig, barrier gammacapture.BarrierConfig, intensity gammacapture.IntensityConfig, artifact *gammacapture.HorizonTouchArtifact, mode replayPolicyMode, symbol string, pairEquity, startBase, queue float64, warmFrom, scoreFrom time.Time) productionReplayResult {
	return simulateProductionPolicyWithQuantityProjectionFrom(
		books, trades, cfg, barrier, intensity, artifact, mode, symbol,
		pairEquity, startBase, queue, warmFrom, scoreFrom, true, 0, nil, nil, nil)
}

func simulateProductionPolicyForComparisonWithScoreAccountReset(books []bboSnapshot, trades []tick, cfg gammacapture.MarketMakerConfig, barrier gammacapture.BarrierConfig, intensity gammacapture.IntensityConfig, artifact *gammacapture.HorizonTouchArtifact, mode replayPolicyMode, symbol string, pairEquity, startBase, queue float64, warmFrom, scoreFrom time.Time) productionReplayResult {
	reset := &productionReplayScoreAccount{PairEquityJPY: pairEquity, Base: startBase}
	return simulateProductionPolicyWithQuantityProjectionFrom(
		books, trades, cfg, barrier, intensity, artifact, mode, symbol,
		pairEquity, startBase, queue, warmFrom, scoreFrom, true, 0, nil, reset, nil)
}

func simulateProductionPolicyForComparisonWithHorizonConditionedUtility(
	books []bboSnapshot, trades []tick, cfg gammacapture.MarketMakerConfig,
	barrier gammacapture.BarrierConfig, intensity gammacapture.IntensityConfig,
	artifact *gammacapture.HorizonTouchArtifact, mode replayPolicyMode, symbol string,
	pairEquity, startBase, queue float64, warmFrom, scoreFrom time.Time,
) productionReplayResult {
	reset := &productionReplayScoreAccount{PairEquityJPY: pairEquity, Base: startBase}
	return simulateProductionPolicyWithQuantityProjectionFromOptions(
		books, trades, cfg, barrier, intensity, artifact, mode, symbol,
		pairEquity, startBase, queue, warmFrom, scoreFrom, true, 0, nil,
		reset, nil, true, true)
}

func simulateProductionPolicyForComparisonWithRegimeExpectedValueSizing(
	books []bboSnapshot, trades []tick, cfg gammacapture.MarketMakerConfig,
	barrier gammacapture.BarrierConfig, intensity gammacapture.IntensityConfig,
	artifact *gammacapture.HorizonTouchArtifact, mode replayPolicyMode, symbol string,
	pairEquity, startBase, queue float64, warmFrom, scoreFrom time.Time,
) productionReplayResult {
	reset := &productionReplayScoreAccount{PairEquityJPY: pairEquity, Base: startBase}
	sizer := &gammacapture.RegimeExpectedValueSizingConfig{
		Enabled: true, PriorEffectiveSamples: 8,
		RiskAversion: math.Max(cfg.FastRiskAversion, 1), DirectionalStressWeight: 0.5,
		MakerFeeBps: cfg.MakerFeeBps, AdverseSelectionBps: cfg.AdverseSelectionBps,
		TurnoverBufferBps: cfg.MinimumNetEdgeBps, MaxScale: 1,
	}
	return simulateProductionPolicyWithQuantityProjectionFrom(
		books, trades, cfg, barrier, intensity, artifact, mode, symbol,
		pairEquity, startBase, queue, warmFrom, scoreFrom, true, 0, nil, reset, sizer)
}

func simulateProductionPolicyWithQuantityProjection(books []bboSnapshot, trades []tick, cfg gammacapture.MarketMakerConfig, barrier gammacapture.BarrierConfig, intensity gammacapture.IntensityConfig, artifact *gammacapture.HorizonTouchArtifact, mode replayPolicyMode, symbol string, pairEquity, startBase, queue float64, tradingFrom time.Time, useProbabilityProjection bool, maxDrawdownStopPct float64) productionReplayResult {
	return simulateProductionPolicyWithQuantityProjectionFrom(
		books, trades, cfg, barrier, intensity, artifact, mode, symbol,
		pairEquity, startBase, queue, tradingFrom, tradingFrom,
		useProbabilityProjection, maxDrawdownStopPct, nil, nil, nil)
}

// simulateProductionPolicyWithQuantityProjectionFrom keeps the existing
// replay API unchanged for all non-HCU arms. When simulationFrom precedes
// scoreFrom, the default is preload-only: causal model observations and
// matured labels continue, while synthetic orders and the expensive quote
// optimizer begin only at the scoring boundary.
func simulateProductionPolicyWithQuantityProjectionFrom(books []bboSnapshot, trades []tick, cfg gammacapture.MarketMakerConfig, barrier gammacapture.BarrierConfig, intensity gammacapture.IntensityConfig, artifact *gammacapture.HorizonTouchArtifact, mode replayPolicyMode, symbol string, pairEquity, startBase, queue float64, simulationFrom, scoreFrom time.Time, useProbabilityProjection bool, maxDrawdownStopPct float64, relativeHoldCheckpoint *gammacapture.RelativeHoldRiskCheckpoint, scoreAccountReset *productionReplayScoreAccount, regimeExpectedValueSizer *gammacapture.RegimeExpectedValueSizingConfig) productionReplayResult {
	return simulateProductionPolicyWithQuantityProjectionFromOptions(
		books, trades, cfg, barrier, intensity, artifact, mode, symbol,
		pairEquity, startBase, queue, simulationFrom, scoreFrom,
		useProbabilityProjection, maxDrawdownStopPct, relativeHoldCheckpoint,
		scoreAccountReset, regimeExpectedValueSizer, false, true)
}

// simulateProductionPolicyForComparisonWithPreloadOnly keeps all causal
// estimator observations during warmup but does not run the expensive quote,
// quantity, and lifecycle optimizer until scoreFrom. It is used only by the
// long continuation WFO, where a coarse explicit BBO interval is already part
// of the research contract.
func simulateProductionPolicyForComparisonWithPreloadOnly(books []bboSnapshot, trades []tick, cfg gammacapture.MarketMakerConfig, barrier gammacapture.BarrierConfig, intensity gammacapture.IntensityConfig, artifact *gammacapture.HorizonTouchArtifact, mode replayPolicyMode, symbol string, pairEquity, startBase, queue float64, warmFrom, scoreFrom time.Time) productionReplayResult {
	reset := &productionReplayScoreAccount{PairEquityJPY: pairEquity, Base: startBase}
	return simulateProductionPolicyWithQuantityProjectionFromOptions(
		books, trades, cfg, barrier, intensity, artifact, mode, symbol,
		pairEquity, startBase, queue, warmFrom, scoreFrom, true, 0, nil,
		reset, nil, false, true)
}

// simulateProductionPolicyWithQuantityProjectionFromOptions replays a causal
// shadow/preload interval before scoreFrom. With preloadOnly enabled, the
// policy observes BBO and public trades and matures delayed labels, but does
// not generate synthetic orders or fills before scoreFrom. At scoreFrom the
// score account and counters are reset, while learned model state continues.
// This is the replay equivalent of a live warmup followed by normal quoting.
func simulateProductionPolicyWithQuantityProjectionFromOptions(books []bboSnapshot, trades []tick, cfg gammacapture.MarketMakerConfig, barrier gammacapture.BarrierConfig, intensity gammacapture.IntensityConfig, artifact *gammacapture.HorizonTouchArtifact, mode replayPolicyMode, symbol string, pairEquity, startBase, queue float64, simulationFrom, scoreFrom time.Time, useProbabilityProjection bool, maxDrawdownStopPct float64, relativeHoldCheckpoint *gammacapture.RelativeHoldRiskCheckpoint, scoreAccountReset *productionReplayScoreAccount, regimeExpectedValueSizer *gammacapture.RegimeExpectedValueSizingConfig, horizonConditionedUtility, preloadOnly bool) productionReplayResult {
	if len(books) < 2 {
		return productionReplayResult{Mode: mode}
	}
	if scoreFrom.IsZero() {
		scoreFrom = simulationFrom
	}
	if scoreFrom.Before(simulationFrom) {
		return productionReplayResult{Mode: mode}
	}
	startIndex := sort.Search(len(books), func(i int) bool { return !books[i].time.Before(simulationFrom) })
	if startIndex >= len(books) {
		return productionReplayResult{Mode: mode}
	}
	startMid := (books[startIndex].bid + books[startIndex].ask) / 2
	startQuote := math.Max(0, pairEquity-startBase*startMid)
	s := newProductionReplayStateWithOptions(cfg, barrier, intensity, artifact, mode, symbol, startQuote, startBase, queue, simulationFrom, useProbabilityProjection, []*gammacapture.RegimeExpectedValueSizingConfig{regimeExpectedValueSizer}, preloadOnly)
	if horizonConditionedUtility {
		s.horizonConditionedUtility = newHorizonConditionedUtilityReplaySizer(cfg)
	}
	s.scoreFrom = scoreFrom
	s.scoreAccountReset = scoreAccountReset
	s.initialEquity = startQuote + startBase*startMid
	s.maxDrawdownStopPct = math.Max(0, maxDrawdownStopPct)
	s.equityPeak = s.initialEquity
	if relativeHoldCheckpoint != nil && s.relativeHoldRiskModel != nil {
		if err := s.relativeHoldRiskModel.Restore(*relativeHoldCheckpoint); err != nil {
			return productionReplayResult{Mode: mode}
		}
		s.relativeHoldRiskResumeAfter = relativeHoldCheckpoint.LastMaturedAt
	}
	bi, ti := 0, 0
	var previous time.Time
	for bi < len(books) || ti < len(trades) {
		useBook := ti >= len(trades) || (bi < len(books) && !books[bi].time.After(trades[ti].time))
		if useBook {
			book := books[bi]
			bi++
			gap := !previous.IsZero() && book.time.Sub(previous) >= 15*time.Minute
			if gap {
				s.cancelQuotes(book.time)
				if !book.time.Before(s.scoreFrom) {
					s.gaps++
				}
			} else if !previous.IsZero() && !book.time.Before(s.scoreFrom) {
				activeStart := previous
				if activeStart.Before(s.scoreFrom) {
					activeStart = s.scoreFrom
				}
				s.activeDuration += book.time.Sub(activeStart)
			}
			previous = book.time
			s.onBook(book, gap)
			if s.stopped {
				break
			}
		} else {
			s.onTrade(trades[ti])
			ti++
		}
	}
	s.cancelQuotes(books[len(books)-1].time)
	return s.result(books)
}

func (s *productionReplayState) onBook(book bboSnapshot, gap bool) {
	// A quote decided from BBO[t] cannot execute against information that
	// arrived before the next observable book. Activate it at BBO[t+1]. A later
	// executable BBO that moves through the resting limit is direct first-passage
	// evidence and fills against visible contra depth; aggregate trades remain a
	// second fill path while the quote rests inside the spread.
	if !s.scoreStarted && !book.time.Before(s.scoreFrom) {
		s.beginScore(book)
	}
	s.activatePendingQuotesOnNextBBO()
	s.executeCrossedMakerOrdersAtBBO(book)
	macroIOCFilled := s.executePendingMacroIOC(book)
	fastTargetIOCFilled := s.executePendingFastTargetIOC(book)
	mid := (book.bid + book.ask) / 2
	if s.pivotRegimeFilter != nil {
		s.pivotRegimeDecision = s.pivotRegimeFilter.Observe(
			gammacapture.PivotRegimeInput{At: book.time, ReferencePrice: mid})
		if s.pivotRegimeDecision.PivotChanged {
			event := s.pivotRegimeDecision.LastPivot
			index := 0
			if event.Direction < 0 {
				index = 1
			}
			s.causalRegimeTargetStats[index].add(event.LegAmplitudeBps)
		}
	}
	imbalance := replayBookImbalance(book)
	ticker := types.BookTicker{Symbol: s.symbol, Buy: fixedpoint.NewFromFloat(book.bid), Sell: fixedpoint.NewFromFloat(book.ask), BuySize: fixedpoint.NewFromFloat(book.bidSize), SellSize: fixedpoint.NewFromFloat(book.askSize)}
	for _, window := range s.fastWindows {
		if evidenceModel := s.fastEvidenceModels[window]; evidenceModel != nil {
			evidenceModel.ObserveBBO(book.time, ticker)
		}
	}
	micro := mid
	if book.bidSize+book.askSize > 0 {
		micro = (book.ask*book.bidSize + book.bid*book.askSize) / (book.bidSize + book.askSize)
	}
	s.slowModel.Observe(book.time, gap)
	for _, window := range s.fastWindows {
		if fastModel := s.fastModels[window]; fastModel != nil {
			fastModel.Observe(book.time, gap)
		}
	}
	if gap {
		s.engine.Reset(micro)
	} else {
		events := s.engine.Update(s.symbol, fixedpoint.NewFromFloat(micro), book.time, book.time, 0)
		for _, event := range events {
			s.slowModel.Update(event)
			for _, window := range s.fastWindows {
				if fastModel := s.fastModels[window]; fastModel != nil {
					fastModel.Update(event)
				}
			}
		}
	}
	s.horizonModel.ObserveBookWithSizesAndGap(
		book.time, book.bid, book.bidSize, book.ask, book.askSize, s.cfg, gap)
	if s.horizonConditionedUtility != nil {
		s.horizonConditionedUtility.observeBook(book, gap)
	}
	if s.cfg.VolumeProfile.Enabled {
		for _, window := range s.cfg.FastModelWindows() {
			s.volumeProfileSamples++
			if _, ready := s.horizonModel.VolumeProfileState(window); ready {
				s.volumeProfileReadySamples++
			}
		}
	}
	if s.bocpd45DirectionEnabled {
		s.bocpd45Direction.observe(book.time, book.bid, book.ask, gap)
		if s.bocpd45Calibration != nil {
			s.bocpd45Calibration.observe(book, s.bocpd45Direction.snapshot(), gap, false)
		}
	}
	s.macroInventoryModel.ObserveBBO(book.time, mid, book.bid, book.ask, gap, s.cfg.MacroInventory)
	s.executableCrossingModel.Observe(book.time, book.bid, book.ask, gap)
	if s.quoteLifecycleHazard != nil && s.cfg.QuoteLifecycleAction.Hazard.Enabled &&
		s.bidOrder.active && s.askOrder.active && s.bidOrder.price > 0 && s.askOrder.price > s.bidOrder.price {
		age := book.time.Sub(s.lastQuoteAt)
		if age < 0 {
			age = 0
		}
		buyDistance, sellDistance, _ := gammacapture.MakerTouchDistances(book.bid, book.ask, s.bidOrder.price, s.askOrder.price)
		s.quoteLifecycleHazard.Observe(book.time, gammacapture.QuoteLifecycleHazardBuy, buyDistance, age, book.ask <= s.bidOrder.price)
		s.quoteLifecycleHazard.Observe(book.time, gammacapture.QuoteLifecycleHazardSell, sellDistance, age, book.bid >= s.askOrder.price)
	}
	if s.cfg.FastDrift.Enabled {
		for _, window := range s.cfg.FastModelWindows() {
			model := s.fastModels[window]
			if model == nil {
				continue
			}
			bboStateTag, _ := s.horizonModel.FastDriftBBOStateTag(window)
			s.horizonModel.ObserveFastDrift(
				book.time, book.bid, book.ask, window, time.Duration(s.cfg.HorizonLookback),
				gammacapture.FastDriftFeatures{
					Direction:     gammacapture.RawFastDirection(model.Snapshot(book.time)),
					BookImbalance: imbalance,
					BBOStateTag:   bboStateTag,
				}, gap)
		}
	}
	if s.preloadOnly && !s.scoreStarted {
		// All causal price/trade estimators and macro bars have been updated.
		// Do not create synthetic warmup quotes or run the expensive optimizer;
		// scoreFrom will reset the account and begin the actual paired replay.
		return
	}
	if book.time.Before(s.tradingFrom) {
		return
	}
	s.books++
	decisionSecond := book.time.Truncate(time.Second)
	if !gap && decisionSecond.Equal(s.lastDecisionSecond) {
		if s.bidOrder.active || s.askOrder.active {
			s.quoteActive++
		}
		return
	}
	s.lastDecisionSecond = decisionSecond
	noOrderMoved := replayNoOrderReferenceMoved(
		s.noOrderReferenceBid, s.noOrderReferenceAsk,
		book.bid, book.ask, s.cfg.RefreshMoveBps)
	if !s.noOrderRetryAfter.IsZero() && book.time.Before(s.noOrderRetryAfter) &&
		!s.bidOrder.active && !s.askOrder.active &&
		!macroIOCFilled && !fastTargetIOCFilled && !s.fillRefreshPending &&
		!noOrderMoved {
		return
	}
	slow := s.slowModel.Snapshot(book.time)
	preSelectionPairEquity := s.quote + s.inventory*mid
	// Keep the research replay on the same executable risk path as live:
	// FastRiskAversion drives horizon selection and quote/quantity decisions;
	// MacroInventory.RiskAversion belongs only to the optional Macro controller.
	fastRiskAversion := s.cfg.FastRiskAversion
	if fastRiskAversion <= 0 || math.IsNaN(fastRiskAversion) || math.IsInf(fastRiskAversion, 0) {
		fastRiskAversion = 1
	}
	selectedDecision := s.horizonModel.UpdateForBookAdaptiveVolatilityWithMarginalBuy(
		book.time, s.cfg, book.bid, book.ask,
		gammacapture.FastHorizonMarginalBuyInput{
			CurrentInventoryNotionalJPY: s.inventory * mid,
			TargetInventoryNotionalJPY:  s.cfg.InventoryCapitalTargetRatio * preSelectionPairEquity,
			HardMinInventoryNotionalJPY: s.cfg.InventoryCapitalMinRatio * preSelectionPairEquity,
			HardMaxInventoryNotionalJPY: s.cfg.InventoryCapitalMaxRatio * preSelectionPairEquity,
			PosteriorInventoryTarget:    s.cfg.PosteriorInventoryTarget,
			PairEquityJPY:               preSelectionPairEquity,
			MarginalBuyNotionalJPY:      100,
			AvailableBuyCapitalJPY:      s.quote,
			MarginalSellNotionalJPY:     100,
			AvailableSellInventoryJPY:   s.inventory * mid,
			RiskAversion:                fastRiskAversion,
			ConfidenceZScore:            s.cfg.InventoryRiskZScore,
		})
	selectedHorizon := time.Duration(selectedDecision.HorizonSeconds) * time.Second
	if selectedHorizon <= 0 {
		selectedHorizon = time.Duration(s.cfg.MinTradingWindow)
	}
	cfg := s.cfg
	if s.asymmetricOscillationRisk != nil {
		if gap {
			s.asymmetricOscillationRisk.ResetPending()
		}
		s.asymmetricOscillationRisk.UpdateLabel(book.time, book.bid)
		features, ready := s.horizonModel.AsymmetricOscillationRiskFeatures(selectedHorizon)
		if ready {
			s.asymmetricRiskDecision = s.asymmetricOscillationRisk.Predict(
				book.time, book.bid, selectedHorizon, features)
		} else {
			s.asymmetricRiskDecision = gammacapture.AsymmetricOscillationRiskDecision{
				Enabled: true, RiskMultiplier: 1, Reason: "asymmetric oscillation risk window not ready",
			}
		}
		if cfg.AsymmetricOscillationRisk.Enabled &&
			!cfg.AsymmetricOscillationRisk.ShadowOnly &&
			s.asymmetricRiskDecision.Enabled && s.asymmetricRiskDecision.RiskMultiplier > 0 {
			fastRiskAversion *= s.asymmetricRiskDecision.RiskMultiplier
		}
		if s.asymmetricOscillationRisk != nil && s.asymmetricRiskDecision.Enabled {
			s.asymmetricRiskMultiplierSum += s.asymmetricRiskDecision.RiskMultiplier
			s.asymmetricRiskSamples++
			if s.asymmetricRiskSamples == 1 {
				s.asymmetricRiskMinMultiplier = s.asymmetricRiskDecision.RiskMultiplier
				s.asymmetricRiskMaxMultiplier = s.asymmetricRiskDecision.RiskMultiplier
			} else {
				s.asymmetricRiskMinMultiplier = math.Min(s.asymmetricRiskMinMultiplier, s.asymmetricRiskDecision.RiskMultiplier)
				s.asymmetricRiskMaxMultiplier = math.Max(s.asymmetricRiskMaxMultiplier, s.asymmetricRiskDecision.RiskMultiplier)
			}
			if s.asymmetricRiskDecision.OscillationScore != 0 {
				s.asymmetricRiskReadySamples++
			}
		}
	}
	// Quote() and all subsequent Fast-side optimizers read this field when the
	// caller does not pass an explicit risk aversion. Keep the copied config in
	// sync with the live Fast path without mutating the Macro controller.
	cfg.FastRiskAversion = fastRiskAversion
	s.recordHorizonDiagnostics(book, selectedHorizon, preSelectionPairEquity)
	adaptiveFast := gammacapture.SelectAdaptiveFastSnapshotForWindow(
		book.time, s.fastModels, s.fastEvidenceModels, selectedHorizon)
	fast := adaptiveFast.Model
	evidence := adaptiveFast.Evidence
	fastInference := gammacapture.InferFastCrossing(adaptiveFast.Window, fast, evidence, slow)
	directionCoverage := gammacapture.FastEvidenceCoverage(
		evidence, s.cfg.FastEvidenceMinTrades, s.cfg.FastEvidenceMinBBOUpdates)
	direction := fastInference.Direction * directionCoverage
	bocpdRegimePosterior, bocpdRegimeConfidence, bocpdRegimeSamples := 0.5, 0.0, 0.0
	if s.bocpd45DirectionEnabled {
		bocpd45 := s.bocpd45Direction.snapshot()
		if bocpd45.ready {
			if s.bocpd45Calibration != nil {
				bocpd45.upProbability = s.bocpd45Calibration.predict(bocpd45.upProbability)
				bocpd45.direction = math.Max(-1, math.Min(1, 2*bocpd45.upProbability-1))
			}
			fastConfidence := math.Max(0, math.Min(1,
				fastInference.DirectionConfidence*directionCoverage))
			combinedConfidence := fastConfidence + bocpd45.confidence
			if combinedConfidence > 0 {
				direction = (direction*fastConfidence +
					bocpd45.direction*bocpd45.confidence) / combinedConfidence
			}
			bocpdRegimePosterior = math.Max(0, math.Min(1, bocpd45.upProbability))
			bocpdRegimeConfidence = math.Max(0, math.Min(1, bocpd45.confidence))
			if s.bocpd45Calibration != nil {
				bocpdRegimeSamples = float64(s.bocpd45Calibration.matured)
			}
		}
	}
	fastDriftBBOStateTag, _ := s.horizonModel.FastDriftBBOStateTag(adaptiveFast.Window)
	fastDrift := s.horizonModel.FastDriftDecision(
		adaptiveFast.Window,
		gammacapture.FastDriftFeatures{
			Direction:     gammacapture.RawFastDirection(fast),
			BookImbalance: imbalance,
			BBOStateTag:   fastDriftBBOStateTag,
		})
	volumeSignal := evidence.VolumeBalance.Signal
	normalFlowPressure := gammacapture.EvaluateNormalFlowPressure(
		s.cfg.NormalFlowPressure,
		gammacapture.NormalFlowPressureInput{
			SignedTradeImbalance: evidence.SignedTradeImbalance5m,
			TradeCount:           evidence.TradeCount5m,
		})
	if math.Abs(volumeSignal) <= 1e-12 && normalFlowPressure.Applied {
		volumeSignal = normalFlowPressure.Signal
	}
	agreement := gammacapture.EvaluateOFIVolumeAgreement(
		s.cfg.OFIVolumeAgreement, evidence.OrderFlowImbalance30s, evidence.SignedTradeImbalance5m)
	if agreement.Ready && !agreement.Agrees && s.cfg.OFIVolumeAgreement.SuppressOnDisagreement {
		volumeSignal = 0
	}
	fastSideVolatility := s.horizonModel.EmpiricalSideVolatilityEstimate(
		book.time, adaptiveFast.Window)
	minute := book.time.Truncate(time.Minute)
	if !minute.Equal(s.volatilityMinute) {
		s.cachedSideVolatility = s.horizonModel.EmpiricalSideVolatilityEstimate(
			book.time, time.Duration(s.cfg.HorizonLookback))
		s.volatilityMinute = minute
	}
	buyEffectiveVolBps, _ := gammacapture.ShrinkVolatility(
		fastSideVolatility.BuyBps, s.cachedSideVolatility.BuyBps,
		fastSideVolatility.BuySamples, s.cfg.HorizonMinSamples)
	sellEffectiveVolBps, _ := gammacapture.ShrinkVolatility(
		fastSideVolatility.SellBps, s.cachedSideVolatility.SellBps,
		fastSideVolatility.SellSamples, s.cfg.HorizonMinSamples)
	effectiveVolBps := math.Max(buyEffectiveVolBps, sellEffectiveVolBps)
	if buyEffectiveVolBps <= 0 || sellEffectiveVolBps <= 0 {
		return
	}
	horizonDistance := func(selected time.Duration) (gammacapture.MarketMakerHorizonDecision, float64) {
		candidate := s.horizonModel.DecisionForHorizon(book.time, s.cfg, effectiveVolBps, book.bid, book.ask, selected)
		distance := math.Max(candidate.BuyTouchDistanceBps, candidate.SellTouchDistanceBps)
		if distance <= 0 {
			halfSpread := s.cfg.HalfSpreadForHorizon(selected, effectiveVolBps)
			bidQuote := mid * math.Exp(-halfSpread/10_000)
			askQuote := mid * math.Exp(halfSpread/10_000)
			buyDistance, sellDistance, _ := gammacapture.MakerTouchDistances(book.bid, book.ask, bidQuote, askQuote)
			distance = math.Max(buyDistance, sellDistance)
		}
		return candidate, distance
	}
	// Match live: the selected statistical model window is not silently
	// replaced by the exchange-order review clock.
	horizon := selectedHorizon
	decision, _ := horizonDistance(horizon)
	arrivalBuyDistance, arrivalSellDistance := 0.0, 0.0
	if decision.DistanceOptimized && decision.HasSufficientCrossings(s.cfg.HorizonMinSamples) {
		arrivalBuyDistance, arrivalSellDistance = decision.BuyTouchDistanceBps, decision.SellTouchDistanceBps
	}
	buyRate, sellRate := 0.0, 0.0
	if decision.HasSufficientCrossings(s.cfg.HorizonMinSamples) {
		buyRate, sellRate = decision.BuyTouchRatePerHour(), decision.SellTouchRatePerHour()
	} else if s.mode != replayHorizonTouch && slow.Health == gammacapture.HealthHealthy && slow.Up > 0 && slow.Down > 0 && slow.Observed > 0 {
		hours := slow.Observed.Hours()
		if hours > 0 {
			buyRate, sellRate = float64(slow.Down)/hours, float64(slow.Up)/hours
		}
	}
	riskBuyRate, riskSellRate := buyRate, sellRate
	pairEquity := s.quote + s.inventory*mid
	fastBandPreview := cfg.DynamicInventoryBandWithCapital(
		mid, effectiveVolBps, horizon, pairEquity)
	effectiveTargetRatio := cfg.InventoryCapitalTargetRatio
	baselineTargetRatio := effectiveTargetRatio
	causalRegimeTargetApplied := false
	reversalDirection := 0
	earlyReversal := false
	reversalApplied := false
	reversal := gammacapture.MacroReversalDecision{}
	macroDecision := gammacapture.MacroInventoryDecision{
		TargetRatio:       effectiveTargetRatio,
		CapitalFloorRatio: cfg.InventoryCapitalMinRatio,
		CapitalCapRatio:   cfg.InventoryCapitalMaxRatio,
	}
	noTradeInventoryEnabled := cfg.MacroInventory.Enabled && cfg.MacroInventory.NoTradeRegion.Enabled
	if cfg.MacroInventory.Enabled {
		s.macroInventoryState.ObserveWealth(book.time, pairEquity)
		executableCrossing := s.executableCrossingModel.Snapshot(book.time)
		macroDecision = cfg.MacroInventory.Decide(&s.macroInventoryModel, gammacapture.MacroInventoryInput{
			Now: book.time, WealthJPY: pairEquity, WealthPeakJPY: s.macroInventoryState.WealthPeakJPY,
			RiskyNotionalJPY: s.inventory * mid,
			PriorTargetRatio: cfg.InventoryCapitalTargetRatio,
			PolicyMinRatio:   cfg.InventoryCapitalMinRatio, PolicyMaxRatio: cfg.InventoryCapitalMaxRatio,
			FallbackVolatilityBpsPerSqrtSec: effectiveVolBps,
			CrossingSnapshot:                slow, ExecutableCrossingSnapshot: executableCrossing, BarrierWidth: s.barrierWidth,
			CrossingQVRatePerSecond:      math.Pow(slow.GammaCaptureVolatility, 2),
			FastVarianceRisk:             s.horizonModel.SideHARVarianceRisk(selectedHorizon),
			BuyVolatilityBpsPerSqrtSec:   buyEffectiveVolBps,
			SellVolatilityBpsPerSqrtSec:  sellEffectiveVolBps,
			OneWayCostBps:                cfg.MakerFeeBps + cfg.AdverseSelectionBps,
			ConfidenceZScore:             cfg.InventoryRiskZScore,
			MinimumExecutableNotionalJPY: 100,
			State:                        &s.macroInventoryState,
			LatestClosedBarAt:            s.macroInventoryModel.LatestClosedBarAt(),
		})
		if noTradeInventoryEnabled {
			baselineTargetRatio = macroDecision.NoTrade.AimRatio
			reversal = gammacapture.MacroReversalDecision{
				Reason:                "superseded by QV-time no-trade region",
				BaselineTargetRatio:   baselineTargetRatio,
				TargetRatio:           effectiveTargetRatio,
				Direction:             macroDecision.NoTrade.Direction,
				AggregateNetEdgeBps:   macroDecision.NoTrade.ExecutionEdgeBps(selectedHorizon),
				SignalForecastHorizon: selectedHorizon,
				CurrentRiskyWeight:    macroDecision.CurrentRiskyWeight,
			}
		} else {
			policyMin := math.Max(cfg.InventoryCapitalMinRatio, macroDecision.CapitalFloorRatio)
			policyMax := math.Min(cfg.InventoryCapitalMaxRatio,
				math.Min(macroDecision.DrawdownCapRatio, macroDecision.CapitalCapRatio))
			reversal = cfg.MacroInventory.DecideReversal(&s.macroInventoryModel, gammacapture.MacroReversalInput{
				Now: book.time, BaselineTargetRatio: macroDecision.TargetRatio,
				CurrentRiskyWeight: macroDecision.CurrentRiskyWeight,
				PolicyMinRatio:     policyMin, PolicyMaxRatio: policyMax,
				RoundTripCostBps:                2*cfg.MakerFeeBps + 2*cfg.AdverseSelectionBps + cfg.MinimumNetEdgeBps,
				ConfidenceZScore:                cfg.InventoryRiskZScore,
				RiskAversion:                    cfg.MacroInventory.RiskAversion,
				FallbackVolatilityBpsPerSqrtSec: effectiveVolBps,
			})
			baselineTargetRatio = macroDecision.TargetRatio
			reversal, _ = s.macroInventoryState.ApplyRegimeLease(
				book.time, policyMin, policyMax, reversal)
			effectiveTargetRatio = reversal.TargetRatio
			reversalDirection = reversal.Direction
			earlyReversal = reversal.EarlyHorizons > 0
			reversalApplied = reversal.Applied || reversal.LeaseApplied
		}
	}
	if noTradeInventoryEnabled && macroDecision.Enabled {
		macroBandPreview := cfg.InventoryBandFromPolicyRatios(
			mid, pairEquity, macroDecision.NoTrade.LowerRatio,
			macroDecision.NoTrade.ExecutionTargetRatio, macroDecision.NoTrade.UpperRatio)
		previewControl := gammacapture.SelectInventoryControl(
			fastBandPreview, macroBandPreview, true, macroDecision.NoTrade.Direction)
		if pairEquity > 0 {
			effectiveTargetRatio = previewControl.Band.Target * mid / pairEquity
		}
		if previewControl.LongHorizonAdjustment {
			reversalDirection = macroDecision.NoTrade.Direction
		} else {
			reversalDirection = 0
			earlyReversal = false
			reversalApplied = false
		}
	}
	if cfg.DynamicInventoryAim.PivotRegimeTarget.CausalCEEnabled &&
		s.pivotRegimeFilter != nil && pairEquity > 0 {
		causalConfig := cfg.DynamicInventoryAim.PivotRegimeTarget
		causalTargetConfig := gammacapture.CausalRegimeInventoryTargetConfig{
			RiskAversion:     causalConfig.CausalRiskAversion,
			PriorStrengthBps: causalConfig.CausalPriorStrengthBps,
		}
		currentRiskyWeight := s.inventory * mid / pairEquity
		index := 0
		if s.pivotRegimeDecision.Direction < 0 {
			index = 1
		}
		pivotInput, ready := gammacapture.BuildCausalRegimeInventoryTargetInputFromPivot(
			s.pivotRegimeDecision,
			currentRiskyWeight, cfg.InventoryCapitalTargetRatio,
			cfg.InventoryCapitalMinRatio, cfg.InventoryCapitalMaxRatio,
			s.causalRegimeTargetStats[index].variance(),
			cfg.MakerFeeBps+cfg.AdverseSelectionBps,
		)
		if ready {
			decision := gammacapture.EvaluateCausalRegimeInventoryTarget(causalTargetConfig, pivotInput)
			s.causalRegimeTargetReady++
			s.causalRegimeTargetWeightSum += decision.TargetWeight
			s.causalRegimeTargetDeltaSum += decision.TargetDeltaWeight
			if decision.TargetWeight >= 1-1e-9 {
				s.causalRegimeTargetFullLong++
			}
			if decision.TargetWeight <= 1e-9 {
				s.causalRegimeTargetFullFlat++
			}
			if decision.Ready {
				effectiveTargetRatio = decision.TargetWeight
				causalRegimeTargetApplied = true
				s.causalRegimeTargetApplied++
			}
			if s.causalRegimeTargetReasons == nil {
				s.causalRegimeTargetReasons = make(map[string]int)
			}
			s.causalRegimeTargetReasons[decision.Reason]++
		} else {
			s.causalRegimeTargetReasons[s.pivotRegimeDecision.Reason]++
		}
		s.causalRegimeTargetEvaluations++
	}
	fastBuyHoldingRiskHorizon := gammacapture.FastReservationRiskHorizon(
		horizon, macroDecision.NoTrade.ForecastObservation)
	fastReservationConfidenceZ := cfg.InventoryRiskZScore
	fastReservation := macroDecision.NoTrade.FastReservation(
		fastBuyHoldingRiskHorizon, fastReservationConfidenceZ)
	s.recordEquity(book.time, mid, effectiveTargetRatio, reversalDirection, earlyReversal, reversalApplied)
	s.updateRelativeHoldRiskFromLatestEquity()
	if n := len(s.equityCurve); n > 0 {
		trend := macroDecision.NoTrade.TrendExcursion
		point := &s.equityCurve[n-1]
		point.TrendDirection = trend.Direction
		point.TrendModelProbability = trend.ModelProbability
		point.TrendTerminalReturnBps = trend.TerminalExpectedReturn * 10_000
		point.TrendExpectedReturnBps = trend.ExpectedReturn * 10_000
		point.TrendRemainingExcursionBps = trend.RemainingExcursion * 10_000
		point.TrendExpectedPivot = trend.ExpectedPivot
		continuation := macroDecision.NoTrade.TrendContinuation
		point.ContinuationRecentDirection = continuation.RecentDirection
		point.ContinuationDirection = continuation.Direction
		point.ContinuationConsolidationScore = continuation.ConsolidationScore
		point.ContinuationUpProbability = continuation.UpProbability
		point.ContinuationDownProbability = continuation.DownProbability
		point.ContinuationCensorProbability = continuation.CensorProbability
		point.ContinuationDownGivenMove = continuation.DownGivenMoveProbability
		point.ContinuationDownLower = continuation.DownGivenMoveLower
		point.ContinuationExpectedReturnBps = continuation.ExpectedReturn * 10_000
		point.ContinuationModelProbability = continuation.ModelProbability
		noTrade := macroDecision.NoTrade
		point.NoTradeRawAimRatio = noTrade.RawAimRatio
		point.NoTradeAimRatio = noTrade.AimRatio
		point.NoTradeLowerRatio = noTrade.LowerRatio
		point.NoTradeUpperRatio = noTrade.UpperRatio
		point.NoTradeKalmanGain = noTrade.AimKalmanGain
		point.NoTradeEffectivePriorStrength = noTrade.EffectivePriorStrength
		point.NoTradeForecastReturnBps = noTrade.ForecastReturn * 10_000
		point.FastReservationEnabled = fastReservation.Enabled
		point.FastReservationForecastBps = fastReservation.ForecastReturnBps
		point.FastReservationForecastSEBps = fastReservation.ForecastReturnSEBps
		point.FastReservationAdverseProb = fastReservation.AdverseProbability
		point.FastReservationStrength = fastReservation.Strength
		point.FastReservationPathEfficiency = fastReservation.PathEfficiency
		point.FastReservationShift = fastReservation.ReservationShiftBps
		point.NoTradeContinuationCapApplied = noTrade.ContinuationCapApplied
		point.NoTradeContinuationCapRatio = noTrade.ContinuationCapRatio
	}
	cfg.InventoryRiskBudgetJPY = cfg.EffectiveInventoryRiskBudgetJPY(pairEquity)
	dynamicNotional := cfg.DynamicQuoteNotionalWithFillRates(effectiveVolBps, horizon, sellRate, buyRate)
	if dynamicNotional <= 0 {
		return
	}
	cfg.QuoteNotional = dynamicNotional
	fastBand := cfg.DynamicInventoryBandWithCapital(
		mid, effectiveVolBps, horizon, pairEquity)
	macroBand := fastBand
	if cfg.MacroInventory.Enabled {
		if noTradeInventoryEnabled {
			macroBand = cfg.InventoryBandFromPolicyRatios(
				mid, pairEquity, macroDecision.NoTrade.LowerRatio,
				macroDecision.NoTrade.ExecutionTargetRatio, macroDecision.NoTrade.UpperRatio)
		} else {
			variation := cfg.ProbabilisticInventoryVariation(
				pairEquity, effectiveTargetRatio, horizon,
				riskBuyRate, riskSellRate, 100)
			macroBand = cfg.DynamicInventoryBandWithCapitalPolicyBounds(
				mid, effectiveVolBps, horizon, pairEquity,
				variation.LowerRatio, variation.ExpectedTargetRatio, variation.UpperRatio)
		}
	}
	inventoryControl := gammacapture.SelectInventoryControl(
		fastBand, macroBand, noTradeInventoryEnabled && macroDecision.Enabled, macroDecision.NoTrade.Direction)
	band := inventoryControl.Band
	if causalRegimeTargetApplied && pairEquity > 0 {
		// The causal target owns only the center of the inventory policy. Build
		// the executable band from the configured outer capital bounds so a
		// 0..100% target does not bypass account or exchange headroom checks.
		band = cfg.InventoryBandFromPolicyRatios(
			mid, pairEquity, cfg.InventoryCapitalMinRatio,
			effectiveTargetRatio, cfg.InventoryCapitalMaxRatio)
		inventoryControl.Reason = "causal pivot-regime CE target owns inventory center"
	}
	if inventoryControl.FastTradingZone {
		s.fastInventoryZoneDecisions++
	} else if inventoryControl.LongHorizonAdjustment {
		s.longHorizonAdjustmentDecisions++
	}
	if pairEquity > 0 && !causalRegimeTargetApplied {
		effectiveTargetRatio = band.Target * mid / pairEquity
	}
	s.inventoryBand = band
	hardBand := cfg.HardInventoryBand(band, mid, pairEquity)
	if noTradeInventoryEnabled {
		hardBand = cfg.InventoryBandFromPolicyRatios(
			mid, pairEquity, macroDecision.CapitalFloorRatio,
			band.TargetRatio, macroDecision.CapitalCapRatio)
	}
	actuation := gammacapture.InventoryActuationDecision{
		Reason: "long-horizon target integrated into Fast inventory control",
	}
	targetContraction := 1.0
	if s.inventory < band.Target {
		if s.acquisitionDeficitSince.IsZero() {
			s.acquisitionDeficitSince = book.time
			s.acquisitionDeficitAnchorMid = mid
		}
	} else {
		s.acquisitionDeficitSince = time.Time{}
		s.acquisitionDeficitAnchorMid = 0
	}
	cfg.InventoryTarget, cfg.InventoryLimit = band.Target, band.Limit
	freeBase, freeQuote := s.freeBalances()
	canBuy, canSell := freeQuote > 0, freeBase*mid >= 100
	if s.mode == replayHorizonTouch && s.artifact != nil {
		s.featureChecks++
		if !minute.Equal(s.featureMinute) {
			s.cachedFeatures, s.cachedFeaturesReady = s.horizonModel.HorizonTouchFeatures(book.time)
			s.featureMinute = minute
		}
		features, ready := s.cachedFeatures, s.cachedFeaturesReady
		if ready {
			s.featureReady++
			provisional := cfg.Quote(gammacapture.MarketMakerQuoteInput{MidPrice: mid, BestBid: book.bid, BestAsk: book.ask, VolatilityPerSqrtSec: effectiveVolBps, BuyVolatilityPerSqrtSec: buyEffectiveVolBps, SellVolatilityPerSqrtSec: sellEffectiveVolBps, TradingHorizonSeconds: horizon.Seconds(), ArrivalBuyTouchDistanceBps: arrivalBuyDistance, ArrivalSellTouchDistanceBps: arrivalSellDistance, Inventory: freeBase, DirectionSignal: direction, VolumeSignal: volumeSignal, BookImbalance: imbalance, FastDrift: fastDrift, SideDistanceBias: s.sideDistanceBias, InventoryActuationDirection: actuation.Direction, InventoryActuationStrength: actuation.InwardStrength, CanBuy: canBuy, CanSell: canSell})
			hpBuy, okBuy := s.artifact.Predict(types.SideTypeBuy, horizon, provisional.BidTouchDistanceBps, features)
			hpSell, okSell := s.artifact.Predict(types.SideTypeSell, horizon, provisional.AskTouchDistanceBps, features)
			if okBuy && okSell {
				recentBuy, recentSell := 0.0, 0.0
				if decision.HasSufficientCrossings(s.cfg.HorizonMinSamples) {
					recentBuy = decision.BuyTouchProbability
					recentSell = decision.SellTouchProbability
				}
				pBuy := gammacapture.BlendTouchProbability(hpBuy, recentBuy, cfg.HorizonTouchModel.HistoricalWeight)
				pSell := gammacapture.BlendTouchProbability(hpSell, recentSell, cfg.HorizonTouchModel.HistoricalWeight)
				buyRate = gammacapture.TouchProbabilityToRate(pBuy, horizon, cfg.HorizonTouchModel.TouchToFillHaircut)
				sellRate = gammacapture.TouchProbabilityToRate(pSell, horizon, cfg.HorizonTouchModel.TouchToFillHaircut)
			}
		}
	}
	plan := cfg.Quote(gammacapture.MarketMakerQuoteInput{
		MidPrice: mid, BestBid: book.bid, BestAsk: book.ask,
		VolatilityPerSqrtSec: effectiveVolBps, BuyVolatilityPerSqrtSec: buyEffectiveVolBps, SellVolatilityPerSqrtSec: sellEffectiveVolBps,
		TradingHorizonSeconds: horizon.Seconds(), ArrivalBuyTouchDistanceBps: arrivalBuyDistance, ArrivalSellTouchDistanceBps: arrivalSellDistance,
		Inventory: s.inventory, InventoryMin: band.MinInventory, InventoryMax: band.MaxInventory,
		HardInventoryMin: hardBand.MinInventory, HardInventoryMax: hardBand.MaxInventory,
		DirectionSignal: direction, VolumeSignal: volumeSignal, BookImbalance: imbalance, BuyFillRate: buyRate, SellFillRate: sellRate,
		FastDrift:                   fastDrift,
		InventoryActuationDirection: actuation.Direction, InventoryActuationStrength: actuation.InwardStrength,
		QuoteNotionalBase: dynamicNotional, CanBuy: canBuy, CanSell: canSell,
	})
	postFillUtilityDecision := gammacapture.PostFillUtilityDecision{
		Reason: "no pending fill", Plan: plan,
	}
	postFillStateActive := !s.lastMakerFill.At.IsZero() &&
		!book.time.Before(s.lastMakerFill.At) &&
		book.time.Sub(s.lastMakerFill.At) <= horizon
	if postFillStateActive {
		utility := cfg.ApplyPostFillUtility(&s.horizonModel, gammacapture.PostFillUtilityInput{
			Now: book.time, Fill: s.lastMakerFill, Plan: plan,
			BestBid: book.bid, BestAsk: book.ask, Mid: mid, Horizon: horizon,
			InventoryBase: s.inventory, InventoryTargetBase: band.Target,
			PairEquityJPY: pairEquity, ExpectedFillNotionalJPY: 100,
			VolatilityBpsPerSqrtSec: effectiveVolBps,
			RiskAversion:            fastRiskAversion,
		})
		if utility.Enabled {
			s.postFillUtilityEvaluations++
			s.postFillUtilityReasons[utility.Reason]++
			if s.postFillUtilityEvaluations == 1 || utility.IncrementalMeanBps > s.maxPostFillIncrementalMeanBps {
				s.maxPostFillIncrementalMeanBps = utility.IncrementalMeanBps
			}
			if s.postFillUtilityEvaluations == 1 || utility.IncrementalLowerBps > s.maxPostFillIncrementalLowerBps {
				s.maxPostFillIncrementalLowerBps = utility.IncrementalLowerBps
			}
		}
		if utility.Applied {
			plan = utility.Plan
			postFillUtilityDecision = utility
			s.postFillUtilityApplied++
		}
	}
	fastReservationUtility := gammacapture.FastReservationUtilityDecision{
		Reason: "endogenous Fast drift owns reservation center",
	}
	if !plan.FastDriftApplied {
		plan, fastReservationUtility = gammacapture.SelectFastReservationPlan(
			&s.horizonModel, cfg, book.time, fastBuyHoldingRiskHorizon,
			plan, fastReservation, mid, book.bid, book.ask, 100, pairEquity,
			fastRiskAversion)
	}
	appliedFastReservationBps := 0.0
	if fastReservationUtility.Applied {
		appliedFastReservationBps = fastReservation.ReservationShiftBps
	}
	s.fastReservationEvaluations++
	if s.fastReservationReasons == nil {
		s.fastReservationReasons = make(map[string]int)
	}
	s.fastReservationReasons[fastReservationUtility.Reason]++
	if fastReservationUtility.Applied {
		s.fastReservationApplied++
	}
	orderReviewDuration := time.Duration(0)
	if plan.AllowBid {
		buyReview := cfg.DynamicOrderKeepDecision(
			horizon, cfg.OrderKeepDistanceBps(plan.BidTouchDistanceBps), buyEffectiveVolBps)
		orderReviewDuration = buyReview.Duration
	}
	if plan.AllowAsk {
		sellReview := cfg.DynamicOrderKeepDecision(
			horizon, cfg.OrderKeepDistanceBps(plan.AskTouchDistanceBps), sellEffectiveVolBps)
		if orderReviewDuration <= 0 || (sellReview.Duration > 0 && sellReview.Duration < orderReviewDuration) {
			orderReviewDuration = sellReview.Duration
		}
	}
	if orderReviewDuration <= 0 {
		orderReviewDuration = horizon
	}
	finalDecision := s.horizonModel.CrossingDecisionAtSideDistances(
		book.time, cfg, horizon, plan.BidTouchDistanceBps, plan.AskTouchDistanceBps,
		math.Max(0, math.Log(plan.AskPrice/plan.BidPrice)*10_000))
	if finalDecision.HasSufficientCrossings(cfg.HorizonMinSamples) {
		finalDecision.DistanceOptimized = decision.DistanceOptimized
		decision = finalDecision
		buyRate, sellRate = decision.BuyTouchRatePerHour(), decision.SellTouchRatePerHour()
	}
	notionals := gammacapture.SideQuoteNotionals{Buy: plan.BidQuoteNotional, Sell: plan.AskQuoteNotional}
	_ = riskBuyRate
	_ = riskSellRate
	if plan.Reason != "quoted" {
		s.cancelQuotes(book.time)
		return
	}
	macroActive := gammacapture.MacroActiveExecutionDecision{
		Reason: "long-horizon target integrated into Fast inventory control",
	}
	s.recordMacroActiveDecision(
		book.time, s.macroInventoryModel.LatestClosedBarAt(), macroActive)
	if s.mode == replayAcquisitionReset && s.tryAcquisitionReset(book, decision, horizon, effectiveVolBps, plan, notionals, evidence) {
		return
	}
	projectionTargetBase := band.Target
	posteriorTarget := gammacapture.PosteriorInventoryTargetDecision{
		Reason: "disabled", TargetBase: projectionTargetBase, UpProbability: 0.5,
	}
	dynamicAimApplied := false
	if activeProductionReplayPosteriorBaseTarget || cfg.PosteriorInventoryTarget || cfg.DynamicInventoryAim.Enabled {
		buyDistance, sellDistance, _ := gammacapture.MakerTouchDistances(
			book.bid, book.ask, plan.BidPrice, plan.AskPrice)
		pathStats := s.horizonModel.JointPathPayoffStatistics(
			book.time, cfg, horizon, buyDistance, sellDistance)
		if activeProductionReplayDirectionalTarget != nil {
			if targetMeanBps, ready := activeProductionReplayDirectionalTarget.MeanAt(book.time, horizon); ready {
				pathStats.InventoryTarget.InventoryDirectionalMeanBps = targetMeanBps
				s.directionalTargetOverrides++
				s.directionalTargetSumBps += targetMeanBps
			}
		}
		if activeProductionReplayPosteriorBaseTarget || cfg.PosteriorInventoryTarget {
			posteriorTarget = gammacapture.PosteriorInventoryRiskTarget(
				band.Target, hardBand.MinInventory, hardBand.MaxInventory, pathStats)
			projectionTargetBase = posteriorTarget.TargetBase
		}
		if cfg.DynamicInventoryAim.Enabled {
			inventorySamples := pathStats.InventoryTargetEffectiveSamples
			if inventorySamples <= 0 {
				inventorySamples = pathStats.EffectiveSamples
			}
			predictiveVariance := pathStats.InventoryTarget.InventoryDirectionalVarBps2
			if inventorySamples > 0 {
				predictiveVariance += predictiveVariance / inventorySamples
			}
			regimeInput := gammacapture.BuildRegimeConditionedTargetInputFromTerminalDrift(
				fastDrift, bocpdRegimePosterior, bocpdRegimeConfidence, bocpdRegimeSamples)
			regimeDecision, regimeBucket, _ := gammacapture.RefreshRegimeConditionedTargetDecision(
				book.time, time.Duration(cfg.HorizonUpdateInterval),
				cfg.DynamicInventoryAim.RegimeConditionedTarget,
				regimeInput, s.regimeTargetBucket, s.regimeTargetDecision)
			s.regimeTargetBucket = regimeBucket
			s.regimeTargetDecision = regimeDecision
			dynamicAim := gammacapture.EvaluateDynamicInventoryAim(
				cfg.DynamicInventoryAim,
				gammacapture.DynamicInventoryAimInput{
					CurrentInventoryRatio:             s.inventory * mid / math.Max(1, pairEquity),
					PolicyTargetRatio:                 band.Target * mid / math.Max(1, pairEquity),
					HardMinimumRatio:                  hardBand.MinInventory * mid / math.Max(1, pairEquity),
					HardMaximumRatio:                  hardBand.MaxInventory * mid / math.Max(1, pairEquity),
					GrossInventoryReturnBps:           pathStats.InventoryTarget.InventoryDirectionalMeanBps,
					PredictiveVarianceBps2:            predictiveVariance,
					EffectiveSamples:                  inventorySamples,
					ForecastHorizon:                   horizon,
					ExecutionHorizon:                  horizon,
					AdjustmentPeriod:                  time.Duration(cfg.HorizonUpdateInterval),
					RiskAversion:                      math.Max(fastRiskAversion, 1e-6),
					OneWayExecutionCostBps:            cfg.MakerFeeBps + cfg.AdverseSelectionBps,
					EvidencePriorSamples:              cfg.DynamicInventoryAim.EvidencePriorSamples,
					RegimeConditioned:                 regimeInput,
					RegimeConditionedDecision:         regimeDecision,
					RegimeConditionedDecisionSupplied: true,
					PivotRegimeDecision:               s.pivotRegimeDecision,
					PivotRegimeDecisionSupplied:       s.pivotRegimeFilter != nil,
				},
			)
			s.dynamicInventoryAimEvaluations++
			s.dynamicInventoryAimObservations = append(s.dynamicInventoryAimObservations, replayDynamicInventoryAimObservation{
				At: book.time, Horizon: horizon,
				GrossForecastBps: dynamicAim.GrossReturnBps, ExecutionForecastBps: dynamicAim.ExecutionReturnBps,
				CurrentRatio: dynamicAim.CurrentInventoryRatio, AdjustedTargetRatio: dynamicAim.AdjustedTargetRatio,
				GatePassed: dynamicAim.GatePassed, Applied: dynamicAim.Applied, EffectiveSamples: dynamicAim.EffectiveSamples,
			})
			if s.dynamicInventoryAimReasons == nil {
				s.dynamicInventoryAimReasons = make(map[string]int)
			}
			s.dynamicInventoryAimReasons[dynamicAim.GateReason]++
			if dynamicAim.GatePassed {
				s.dynamicInventoryAimPassed++
			}
			if dynamicAim.GatePassed && !cfg.DynamicInventoryAim.ShadowOnly {
				projectionTargetBase = dynamicAim.AdjustedTargetRatio * pairEquity / mid
				dynamicAimApplied = true
				s.dynamicInventoryAimApplied++
			}
			if dynamicAim.GatePassed && !cfg.DynamicInventoryAim.ShadowOnly {
				posteriorTarget.Enabled = true
				posteriorTarget.TargetBase = projectionTargetBase
				// The inventory-risk gradient selects the target but is not an
				// executable price forecast for Fast IOC decisions.
				posteriorTarget.InventoryReturnMean = dynamicAim.ExecutionReturnBps
				posteriorTarget.InventoryPredictiveSD = dynamicAim.PredictiveStdDevBps
				posteriorTarget.DirectionConfidence = dynamicAim.SignalStrength
			}
		}
	}
	if !dynamicAimApplied && cfg.FastTargetSwitching.Enabled && s.quotedTargetSet {
		previousTargetBase := s.quotedFastTargetRatio * pairEquity / mid
		targetSwitch := gammacapture.EvaluateFastTargetSwitching(
			cfg.FastTargetSwitching,
			gammacapture.FastTargetSwitchingInput{
				CandidateTargetBase:  projectionTargetBase,
				PreviousTargetBase:   previousTargetBase,
				CurrentInventoryBase: s.inventory,
				HardMinimumBase:      hardBand.MinInventory,
				HardMaximumBase:      hardBand.MaxInventory,
				MidPrice:             mid, PairEquityJPY: pairEquity,
				InventoryReturnMeanBps:      posteriorTarget.InventoryReturnMean,
				InventoryReturnPredictiveSD: posteriorTarget.InventoryPredictiveSD,
				RiskAversion:                fastRiskAversion,
				OneWayExecutionCostBps:      cfg.MakerFeeBps + cfg.AdverseSelectionBps,
			})
		s.fastTargetSwitchEvaluations++
		if s.fastTargetSwitchReasons == nil {
			s.fastTargetSwitchReasons = make(map[string]int)
		}
		s.fastTargetSwitchReasons[targetSwitch.Reason]++
		s.fastTargetSwitchNetValueSumJPY += targetSwitch.NetSwitchValueJPY
		if targetSwitch.Applied {
			s.fastTargetSwitchApplied++
		} else {
			s.fastTargetSwitchRetained++
		}
		if !cfg.FastTargetSwitching.ShadowOnly {
			projectionTargetBase = targetSwitch.SelectedTargetBase
		}
	}
	selectedProjectionTargetRatio := effectiveTargetRatio
	if pairEquity > 0 {
		selectedProjectionTargetRatio = projectionTargetBase * mid / pairEquity
	}

	s.distanceSamples++
	s.bidDistanceSum += plan.BidDistanceBps
	s.askDistanceSum += plan.AskDistanceBps
	minRefresh, maxRefresh := cfg.RefreshIntervals(plan.HalfSpreadBps, effectiveVolBps)
	transportMinRefresh, _ := cfg.RefreshIntervals(0, 0)
	minRefresh, _ = gammacapture.BoundRefreshIntervals(minRefresh, maxRefresh, orderReviewDuration)
	elapsed := book.time.Sub(s.lastQuoteAt)
	quoteCrossed := (s.bidOrder.active && s.bidOrder.price >= book.ask) || (s.askOrder.active && s.askOrder.price <= book.bid)
	missingSide := (plan.AllowBid && !s.bidOrder.active) || (plan.AllowAsk && !s.askOrder.active)
	sideMismatch := (s.bidOrder.active != plan.AllowBid) || (s.askOrder.active != plan.AllowAsk)
	windowExpired := s.windowEndsAt.IsZero() || !book.time.Before(s.windowEndsAt)
	statisticalRealignment := false
	oneSidedTargetRealignment := false
	ordinaryStatisticalReview := elapsed >= minRefresh || windowExpired
	earlyStatisticalReview := activeProductionEarlyStatisticalRealignment &&
		elapsed >= transportMinRefresh && !ordinaryStatisticalReview
	if earlyStatisticalReview {
		s.earlyStatisticalEvaluations++
	}
	activeHorizonDecision := gammacapture.MarketMakerHorizonDecision{}
	if (ordinaryStatisticalReview || earlyStatisticalReview) && s.bidOrder.active && s.askOrder.active &&
		decision.HasSufficientCrossings(cfg.HorizonMinSamples) {
		activeHorizon := s.windowEndsAt.Sub(s.lastQuoteAt)
		if activeHorizon <= 0 {
			activeHorizon = horizon
		}
		activeBuyDistance, activeSellDistance, activeGrossEdge := gammacapture.MakerTouchDistances(
			book.bid, book.ask, s.bidOrder.price, s.askOrder.price)
		activeHorizonDecision = s.horizonModel.CrossingDecisionAtSideDistances(
			book.time, cfg, activeHorizon, activeBuyDistance, activeSellDistance, activeGrossEdge)
		statisticalRealignment, _, _ = gammacapture.MakerQuoteStatisticalRealignment(
			decision, activeHorizonDecision, cfg.InventoryRiskZScore)
		if earlyStatisticalReview && statisticalRealignment {
			s.earlyStatisticalApplied++
		}
	}
	if !statisticalRealignment && elapsed >= transportMinRefresh &&
		decision.HasSufficientCrossings(cfg.HorizonMinSamples) {
		activeHorizon := s.windowEndsAt.Sub(s.lastQuoteAt)
		if activeHorizon <= 0 {
			activeHorizon = horizon
		}
		activeBid, activeAsk := s.bidOrder.price, s.askOrder.price
		targetRestoring := false
		if s.askOrder.active && !s.bidOrder.active && s.inventory > projectionTargetBase+1e-12 &&
			plan.AllowAsk && plan.AskPrice+1e-12 < s.askOrder.price {
			activeBid = plan.BidPrice
			targetRestoring = activeBid > 0 && activeBid < activeAsk
		} else if s.bidOrder.active && !s.askOrder.active && s.inventory+1e-12 < projectionTargetBase &&
			plan.AllowBid && plan.BidPrice > s.bidOrder.price+1e-12 {
			activeAsk = plan.AskPrice
			targetRestoring = activeAsk > activeBid
		}
		if targetRestoring {
			activeBuyDistance, activeSellDistance, activeGrossEdge := gammacapture.MakerTouchDistances(
				book.bid, book.ask, activeBid, activeAsk)
			activeDecision := s.horizonModel.CrossingDecisionAtSideDistances(
				book.time, cfg, activeHorizon, activeBuyDistance, activeSellDistance, activeGrossEdge)
			oneSidedTargetRealignment, _, _ = gammacapture.MakerQuoteStatisticalRealignment(
				decision, activeDecision, cfg.InventoryRiskZScore)
			buyBoundary := s.bidOrder.active && !s.askOrder.active &&
				s.quote+1e-9 >= 100 && s.inventory*mid+1e-9 < 100
			sellBoundary := s.askOrder.active && !s.bidOrder.active &&
				s.quote+1e-9 < 100 && s.inventory*mid+1e-9 >= 100
			if buyBoundary || sellBoundary {
				gapJPY := math.Abs(s.inventory*mid - projectionTargetBase*mid)
				orderNotionalJPY := math.Max(100, gapJPY*math.Min(1, gapJPY/pairEquity))
				if buyBoundary {
					orderNotionalJPY = math.Min(orderNotionalJPY, s.quote)
				} else {
					orderNotionalJPY = math.Min(orderNotionalJPY, s.inventory*mid)
				}
				riskRealignment, _, _ := gammacapture.TargetRestoringSideRealignment(
					&s.horizonModel, cfg, book.time, activeHorizon,
					book.bid, book.ask, plan.BidPrice, plan.AskPrice, activeBid, activeAsk,
					buyBoundary, s.inventory*mid, projectionTargetBase*mid,
					orderNotionalJPY, pairEquity, cfg.MacroInventory.RiskAversion, cfg.InventoryRiskZScore)
				oneSidedTargetRealignment = oneSidedTargetRealignment || riskRealignment
			}
			statisticalRealignment = oneSidedTargetRealignment
		}
	}
	currentRiskyWeight := 0.0
	if pairEquity > 0 {
		currentRiskyWeight = s.inventory * mid / pairEquity
	}
	macroTargetRealignment := s.quotedTargetSet && gammacapture.InventoryTargetRealignmentRequired(
		effectiveTargetRatio, s.quotedTargetRatio, currentRiskyWeight, pairEquity, 100)
	fastTargetRealignment := s.quotedTargetSet && gammacapture.InventoryTargetRealignmentRequired(
		selectedProjectionTargetRatio, s.quotedFastTargetRatio, currentRiskyWeight, pairEquity, 100)
	reservationTargetSideActive := (appliedFastReservationBps > 0 && s.bidOrder.active) ||
		(appliedFastReservationBps < 0 && s.askOrder.active)
	reservationRiskRealignment := fastReservationUtility.Applied && reservationTargetSideActive &&
		gammacapture.FastReservationRealignmentRequired(
			appliedFastReservationBps, s.quotedFastReservationBps, 1e-9)
	lifecycleActionDecision := gammacapture.QuoteLifecycleActionValue{
		Action: gammacapture.QuoteLifecycleCancel, Reason: "disabled",
	}
	lifecycleReplace := false
	lifecycleCurrentProbability := activeHorizonDecision.BothTouchProbability
	lifecycleCurrentProbabilitySE := activeHorizonDecision.BothTouchStdError
	lifecycleCandidateProbability := decision.BothTouchProbability
	lifecycleCandidateProbabilitySE := decision.BothTouchStdError
	lifecycleReplacementCost := cfg.QuoteLifecycleAction.ReplacementCostBps
	lifecycleReplacementCostSE := 0.0
	if s.quoteLifecycleHazard != nil && cfg.QuoteLifecycleAction.Hazard.Enabled &&
		s.bidOrder.active && s.askOrder.active && activeHorizonDecision.Horizon > 0 {
		activeBuyDistance, activeSellDistance, _ := gammacapture.MakerTouchDistances(book.bid, book.ask, s.bidOrder.price, s.askOrder.price)
		candidateBuyDistance, candidateSellDistance, _ := gammacapture.MakerTouchDistances(book.bid, book.ask, plan.BidPrice, plan.AskPrice)
		s.quoteLifecycleHazard.Observe(book.time, gammacapture.QuoteLifecycleHazardBuy, candidateBuyDistance, 0, plan.BidPrice > 0 && book.ask <= plan.BidPrice)
		s.quoteLifecycleHazard.Observe(book.time, gammacapture.QuoteLifecycleHazardSell, candidateSellDistance, 0, plan.AskPrice > 0 && book.bid >= plan.AskPrice)
		age := elapsed
		if age < 0 {
			age = 0
		}
		currentHazard := s.quoteLifecycleHazard.PairSnapshot(activeHorizonDecision.Horizon, activeBuyDistance, activeSellDistance, age, age)
		candidateHazard := s.quoteLifecycleHazard.PairSnapshot(horizon, candidateBuyDistance, candidateSellDistance, 0, 0)
		if currentHazard.Ready && candidateHazard.Ready {
			s.quoteLifecycleHazardReadyReviews++
		}
		if currentHazard.Ready {
			lifecycleCurrentProbability, lifecycleCurrentProbabilitySE = currentHazard.BothProbability, currentHazard.BothStdError
		}
		if candidateHazard.Ready {
			lifecycleCandidateProbability, lifecycleCandidateProbabilitySE = candidateHazard.BothProbability, candidateHazard.BothStdError
		}
		staleness := 0.0
		if s.lastBestBid > 0 {
			staleness = math.Max(staleness, math.Abs(math.Log(book.bid/s.lastBestBid))*10_000)
		}
		if s.lastBestAsk > 0 {
			staleness = math.Max(staleness, math.Abs(math.Log(book.ask/s.lastBestAsk))*10_000)
		}
		cost := gammacapture.EstimateQuoteLifecycleReplacementCostBps(gammacapture.QuoteLifecycleReplacementCostInput{
			BaseCostBps:            cfg.QuoteLifecycleAction.ReplacementCostBps,
			CurrentFillProbability: lifecycleCurrentProbability, CurrentAge: age, Horizon: activeHorizonDecision.Horizon,
			CurrentTerminalMarkoutBps: activeHorizonDecision.NetRoundTripEdgeBps,
			BBOStalenessBps:           staleness, VolatilityBpsPerSqrtSec: effectiveVolBps,
		}, cfg.QuoteLifecycleAction)
		lifecycleReplacementCost, lifecycleReplacementCostSE = cost.CostBps, cost.StdErrorBps
	}
	if cfg.QuoteLifecycleAction.Enabled &&
		plan.Reason == "quoted" && windowExpired && !s.fillRefreshPending &&
		s.bidOrder.active && s.askOrder.active && plan.AllowBid && plan.AllowAsk &&
		!quoteCrossed && !missingSide && !sideMismatch && !statisticalRealignment &&
		!oneSidedTargetRealignment && !macroTargetRealignment &&
		!fastTargetRealignment && !reservationRiskRealignment &&
		decision.HasSufficientCrossings(cfg.HorizonMinSamples) &&
		activeHorizonDecision.HasSufficientCrossings(cfg.HorizonMinSamples) {
		lifecycleActionDecision = gammacapture.EvaluateQuoteLifecycleFromHorizonsWithEstimates(
			activeHorizonDecision, decision, true, true, cfg.QuoteLifecycleAction,
			lifecycleCurrentProbability, lifecycleCurrentProbabilitySE,
			lifecycleCandidateProbability, lifecycleCandidateProbabilitySE,
			lifecycleReplacementCost, lifecycleReplacementCostSE)
		if lifecycleActionDecision.Evaluated {
			s.quoteLifecycleEvaluations++
			s.quoteLifecycleIncrementalSumBps += lifecycleActionDecision.IncrementalVsKeepBps
			s.quoteLifecycleActions[string(lifecycleActionDecision.Action)]++
		}
		if lifecycleActionDecision.Evaluated && !cfg.QuoteLifecycleAction.ShadowOnly {
			switch lifecycleActionDecision.Action {
			case gammacapture.QuoteLifecycleKeep:
				s.windowEndsAt = book.time.Add(orderReviewDuration)
				s.quoteActive++
				return
			case gammacapture.QuoteLifecycleCancel:
				s.cancelQuotes(book.time)
				s.lastQuoteAt = book.time
				s.windowEndsAt = book.time.Add(orderReviewDuration)
				s.noOrderRetryAfter = s.windowEndsAt
				s.noOrderReferenceBid, s.noOrderReferenceAsk = book.bid, book.ask
				return
			case gammacapture.QuoteLifecycleReplace:
				lifecycleReplace = true
			}
		}
	}
	retainBid, retainAsk := false, false
	cleanWindowExpiry := windowExpired && !quoteCrossed && !missingSide && !sideMismatch &&
		!statisticalRealignment && !macroTargetRealignment && !fastTargetRealignment &&
		!reservationRiskRealignment && !lifecycleReplace &&
		!macroIOCFilled && !fastTargetIOCFilled
	if cleanWindowExpiry {
		retainBid, retainAsk = replayNearFillSides(
			s.bidOrder, s.askOrder, book, plan,
			2*cfg.MakerFeeBps+2*cfg.AdverseSelectionBps+cfg.MinimumNetEdgeBps)
		allActiveSidesRetained := (!s.bidOrder.active || retainBid) && (!s.askOrder.active || retainAsk)
		if (retainBid || retainAsk) && allActiveSidesRetained {
			s.windowEndsAt = book.time.Add(orderReviewDuration)
			s.quoteActive++
			return
		}
	}
	shouldRefresh := s.lastQuoteAt.IsZero() || macroIOCFilled || fastTargetIOCFilled || s.fillRefreshPending || lifecycleReplace
	if !shouldRefresh && (elapsed >= minRefresh || oneSidedTargetRealignment ||
		(earlyStatisticalReview && statisticalRealignment)) {
		materialMove := s.lastMid > 0 && math.Abs(math.Log(mid/s.lastMid))*10_000 >= cfg.RefreshMoveBps
		adverseAskMoveBps, adverseBidMoveBps := 0.0, 0.0
		if s.lastBestAsk > 0 && book.ask > 0 {
			adverseAskMoveBps = math.Log(s.lastBestAsk/book.ask) * 10_000
		}
		if s.lastBestBid > 0 && book.bid > 0 {
			adverseBidMoveBps = math.Log(book.bid/s.lastBestBid) * 10_000
		}
		adverseMove := math.Max(adverseAskMoveBps, adverseBidMoveBps) >= cfg.AdverseRepriceBps
		noActiveOrders := !s.bidOrder.active && !s.askOrder.active
		shouldRefresh = gammacapture.MakerQuoteRefreshRequired(
			elapsed, minRefresh, orderReviewDuration,
			quoteCrossed, windowExpired, adverseMove, materialMove, false,
			missingSide || sideMismatch || fastTargetRealignment,
			noActiveOrders, false,
			statisticalRealignment || macroTargetRealignment || reservationRiskRealignment,
			oneSidedTargetRealignment || (earlyStatisticalReview && statisticalRealignment))
	}
	if !shouldRefresh {
		if s.bidOrder.active || s.askOrder.active {
			s.quoteActive++
		}
		return
	}
	// The live fill-rebalance worker clears its pending generation after this
	// single complete planning call, regardless of whether the value model
	// submits a replacement. Keeping it set until a replay order happens turns
	// a legitimate no-order action into a BBO-rate refresh loop and is a
	// training/holdout simulator mismatch.
	s.fillRefreshPending = false
	// Replacement sizing uses total balances because canceling the old quotes
	// releases their locks before the new orders are submitted.
	preCancelBase, preCancelQuote := s.inventory, s.quote
	if !retainBid {
		s.bidOrder = productionReplayOrder{}
	}
	if !retainAsk {
		s.askOrder = productionReplayOrder{}
	}
	hardBuyMarkNotional := math.Max(0, hardBand.MaxInventory-s.inventory) * mid
	hardSellMarkNotional := math.Max(0, s.inventory-hardBand.MinInventory) * mid
	fastQuantityCapacity := gammacapture.FastQuantityCapacity(
		gammacapture.ExposureUtilizationSizingInput{
			ExecutableUnitJPY:                 100,
			RiskSizedNotionalJPY:              dynamicNotional,
			PairEquityJPY:                     pairEquity,
			CurrentInventoryNotionalJPY:       s.inventory * mid,
			HardLowerInventoryNotionalJPY:     hardBand.MinInventory * mid,
			HardUpperInventoryNotionalJPY:     hardBand.MaxInventory * mid,
			AvailableBuyCapitalJPY:            preCancelQuote,
			AvailableSellInventoryNotionalJPY: preCancelBase * mid,
		})
	riskUtilizationSizing := fastQuantityCapacity.Exposure
	projectionLowerNotionalJPY, projectionUpperNotionalJPY :=
		gammacapture.SymmetricInventoryProjectionBounds(
			projectionTargetBase*mid, hardBand.MinInventory*mid, hardBand.MaxInventory*mid)
	projectionInput := gammacapture.ProbabilityCenteredQuoteInput{
		CurrentInventoryNotionalJPY: s.inventory * mid,
		TargetInventoryNotionalJPY:  projectionTargetBase * mid,
		LowerInventoryNotionalJPY:   projectionLowerNotionalJPY,
		UpperInventoryNotionalJPY:   projectionUpperNotionalJPY,
		BuyFillRatePerHour:          buyRate,
		SellFillRatePerHour:         sellRate,
		Horizon:                     horizon,
		ConfidenceZScore:            cfg.InventoryRiskZScore,
		FastBuyRestraint:            math.Max(0, -direction),
		FastSellRestraint:           math.Max(0, direction),
		TargetContraction:           targetContraction,
		MinBuyNotionalJPY:           100 * mid / plan.BidPrice,
		MinSellNotionalJPY:          100 * mid / plan.AskPrice,
	}
	jointFastDirection := direction
	if plan.FastDriftApplied {
		projectionInput.FastBuyRestraint = 0
		projectionInput.FastSellRestraint = 0
		jointFastDirection = 0
	}
	if plan.AllowBid && plan.BidPrice > 0 {
		projectionInput.FastBuyNotionalJPY = plan.BidQuoteNotional * mid / plan.BidPrice
		modelBuyCap := fastQuantityCapacity.BaselineBuyCapJPY * plan.BidPrice / mid
		buyExecutionCap := replayCappedInventoryCapacity(
			modelBuyCap,
			hardBuyMarkNotional*plan.BidPrice/mid, 100)
		projectionInput.MaxBuyNotionalJPY = math.Min(
			preCancelQuote*mid/plan.BidPrice, buyExecutionCap*mid/plan.BidPrice)
	}
	if plan.AllowAsk && plan.AskPrice > 0 {
		projectionInput.FastSellNotionalJPY = plan.AskQuoteNotional * mid / plan.AskPrice
		modelSellCap := fastQuantityCapacity.BaselineSellCapJPY / mid
		sellQuantityCap := replayCappedInventoryCapacity(
			modelSellCap, hardSellMarkNotional/mid, 100/plan.AskPrice)
		projectionInput.MaxSellNotionalJPY = math.Min(
			preCancelBase*mid, sellQuantityCap*mid)
	}
	jointMaxBuyNotionalJPY := math.Min(
		preCancelQuote*mid/plan.BidPrice, fastQuantityCapacity.PathModelBuyCapJPY)
	jointMaxSellNotionalJPY := math.Min(
		preCancelBase*mid, fastQuantityCapacity.PathModelSellCapJPY)
	projection := gammacapture.ProbabilityCenteredQuoteDecision{Reason: "staged replay baseline"}
	selectedJoint := gammacapture.JointDistanceQuantityDecision{}
	fastValueRejected := false
	regimeSoftBasePlan := plan
	regimeSoftBaseProjection := projection
	regimeSoftBaseDecision := decision
	if s.useProbabilityProjection {
		projection = gammacapture.ProbabilityCenteredQuoteNotionals(projectionInput)
		regimeSoftBasePlan = plan
		regimeSoftBaseProjection = projection
		regimeSoftBaseDecision = decision
		if cfg.JointDistanceQuantity.Enabled {
			if s.relativeHoldRiskModel != nil && s.relativeHoldRiskModel.Snapshot().Ready {
				s.relativeHoldRiskReadyInputs++
			}
			jointProjectionInput := projectionInput
			jointProjectionInput.MaxBuyNotionalJPY = jointMaxBuyNotionalJPY
			jointProjectionInput.MaxSellNotionalJPY = jointMaxSellNotionalJPY
			completionSide := types.SideTypeNone
			if postFillUtilityDecision.Applied {
				completionSide = postFillUtilityDecision.Side
			}
			jointHorizonCandidates := []time.Duration(nil)
			if cfg.JointDistanceQuantity.JointHorizonSelection {
				jointHorizonCandidates = cfg.TradingHorizons()
			}
			joint := gammacapture.OptimizeUnifiedFastQuantity(
				&s.horizonModel, cfg, gammacapture.JointDistanceQuantityInput{
					Now: book.time, Horizon: horizon, HorizonCandidates: jointHorizonCandidates,
					BestBid: book.bid, BestAsk: book.ask, MidPrice: mid,
					BasePlan: plan, Projection: jointProjectionInput,
					FastDirection:                     jointFastDirection,
					ConfidenceZScore:                  cfg.InventoryRiskZScore,
					PairEquityJPY:                     pairEquity,
					RiskAversion:                      fastRiskAversion,
					AvailableBuyCapitalJPY:            preCancelQuote,
					AvailableSellInventoryNotionalJPY: preCancelBase * mid,
					CompletionSide:                    completionSide,
					RelativeHoldRisk:                  s.relativeHoldRiskInput(),
				}, projectionInput)
			selectedJoint = joint
			if joint.RelativeHoldStateReady {
				s.relativeHoldRiskReadyDecisions++
			}
			if s.relativeHoldRiskModel != nil && joint.RelativeHoldStateReady {
				s.relativeHoldRiskUtilityEvaluations++
				s.relativeHoldRiskUtilitySumJPYHour += joint.RelativeHoldUtilityJPYHour
				if !cfg.RelativeHoldRisk.ShadowOnly &&
					math.Abs(joint.RelativeHoldUtilityJPYHour) > 1e-12 {
					s.relativeHoldRiskApplied = true
				}
			}
			s.jointQuoteEvaluations++
			if s.jointQuoteReasons == nil {
				s.jointQuoteReasons = make(map[string]int)
			}
			s.jointQuoteReasons[joint.Reason]++
			s.jointQuoteDecisions = append(s.jointQuoteDecisions,
				productionReplayJointDecision{
					At: book.time, PreliminaryHorizon: horizon,
					Horizon:                      joint.Crossing.Horizon,
					HorizonCrossingScoreBpsHour:  selectedDecision.ScoreBpsPerHour,
					HorizonSelectionScoreBpsHour: selectedDecision.SelectionScoreBpsPerHour,
					HorizonMarginalBuyEvaluated:  selectedDecision.MarginalBuyEvaluated,
					HorizonMarginalBuyNotional:   selectedDecision.MarginalBuyNotionalJPY,
					HorizonMarginalBuyCE:         selectedDecision.MarginalBuyCertaintyEquivalentJPY,
					HorizonMarginalBuyUtility:    selectedDecision.MarginalBuyUtilityBpsPerHour,
					HorizonCandidateCount:        joint.HorizonCandidateCount,
					HorizonEffectiveSamples:      joint.HorizonEffectiveSamples,
					HorizonReliability:           joint.HorizonReliability,
					HorizonRawUtilityJPYHour:     joint.HorizonRawUtilityJPYHour,
					HorizonPosteriorJPYHour:      joint.HorizonSelectionUtilityJPYHour,
					Reason:                       joint.Reason,
					AuthoritativeRejection:       joint.AuthoritativeRejection,
					SideSafeFallback:             joint.SideSafeFallback,
					ContinuityFloorApplied:       joint.ContinuityFloorApplied,
					ContinuityFloorReason:        joint.ContinuityFloorReason,
					BasePlanAllowBid:             plan.AllowBid,
					BasePlanAllowAsk:             plan.AllowAsk,
					CompletionProtected:          joint.CompletionProtected,
					FallbackBuySupported:         joint.FallbackBuySupported,
					FallbackSellSupported:        joint.FallbackSellSupported,
					DistanceCandidate:            joint.SelectedCandidate,
					QuantityCandidate:            joint.SelectedQuantityCandidate,
					QuantityScale:                joint.QuantityScale,
					CapitalUtilization:           joint.CapitalUtilization,
					PairUtilization:              joint.PairCapitalUtilization,
					ExpectedJPYHour:              joint.ExpectedPnLJPYHour,
					LowerJPYHour:                 joint.LowerPnLJPYHour,
					StdErrorJPYHour:              joint.PathStdErrorJPYHour,
					KellyPenaltyHour:             joint.KellyPenaltyJPYHour,
					KellyUtilityHour:             joint.KellyUtilityJPYHour,
					FeeValueMeanJPY:              joint.FeeValueMeanJPY,
					FeeValueDownsideRegretJPY:    joint.FeeValueDownsideRegretJPY,
					FeeValueNetJPY:               joint.FeeValueNetJPY,
					PositiveConfidence:           joint.PathPositiveConfidence,
					EffectiveSamples:             joint.PathEffectiveSamples,
					FastDirection:                direction,
					FastDriftApplied:             joint.Plan.FastDriftApplied,
					FastDriftMeanBps:             joint.Plan.FastDriftMeanBps,
					FastDriftRawMeanBps:          joint.Plan.FastDriftRawMeanBps,
					FastDriftStrength:            joint.Plan.FastDriftStrength,
					FastDriftProbability:         joint.Plan.FastDriftValidationProbability,
					FastDriftVarianceBps2:        joint.Plan.FastDriftVarianceBps2,
					FastDriftSamples:             joint.Plan.FastDriftSamples,
					FastDriftValidation:          joint.Plan.FastDriftValidationSamples,
					FastDriftSkill:               joint.Plan.FastDriftPrequentialSkill,
					FastDriftReason:              joint.Plan.FastDriftReason,
					FastDriftBBOStateTag:         fastDriftBBOStateTag,
					BestBid:                      book.bid,
					BestAsk:                      book.ask,
					BidPrice:                     joint.Plan.BidPrice,
					AskPrice:                     joint.Plan.AskPrice,
					BuyNotionalJPY:               joint.Projection.BuyNotionalJPY,
					SellNotionalJPY:              joint.Projection.SellNotionalJPY,
					CurrentInventoryJPY:          projectionInput.CurrentInventoryNotionalJPY,
					TargetInventoryJPY:           projectionInput.TargetInventoryNotionalJPY,
					BuyTouchProbability:          joint.Crossing.BuyTouchProbability,
					SellTouchProbability:         joint.Crossing.SellTouchProbability,
					NetRoundTripEdgeBps:          joint.Crossing.NetRoundTripEdgeBps,
					DownsideBuyCapApplied:        joint.DownsideBuyCapApplied,
					DownsideReturnMeanBps:        joint.DownsideInventoryReturnMeanBps,
					DownsideReturnUpperBps:       joint.DownsideInventoryReturnUpperBps,
					DownsideSamples:              joint.DownsideEffectiveSamples,
					DownsideMinimumBuyEvaluated:  joint.DownsideMinimumBuyEvaluated,
					DownsideMinimumBuyCEJPY:      joint.DownsideMinimumBuyCEJPY,
					BuyAdmissionEvaluated:        joint.BuyAdmissionEvaluated,
					BuyAdmissionApplied:          joint.BuyAdmissionApplied,
					BuyAdmissionMaxJPY:           joint.BuyAdmissionMaximumJPY,
					BuyAdmissionUtilityBoundJPY:  joint.BuyAdmissionUtilityBoundJPY,
					BuyAdmissionReason:           joint.BuyAdmissionReason,
					SellAdmissionEvaluated:       joint.SellAdmissionEvaluated,
					SellAdmissionApplied:         joint.SellAdmissionApplied,
					SellAdmissionMaxJPY:          joint.SellAdmissionMaximumJPY,
					SellAdmissionUtilityBoundJPY: joint.SellAdmissionUtilityBoundJPY,
					SellAdmissionReason:          joint.SellAdmissionReason,
					AdmissionJointCEJPY:          joint.AdmissionJointCEJPY,
					AdmissionJointComplementary:  joint.AdmissionJointComplementary,
					InwardBuyEligible:            joint.InwardBuyEligible,
					InwardSellEligible:           joint.InwardSellEligible,
					InwardBuySelected:            joint.InwardBuySelected,
					InwardSellSelected:           joint.InwardSellSelected,
					SelectedInwardBuyDeltaBps:    joint.SelectedInwardBuyDeltaBps,
					SelectedInwardSellDeltaBps:   joint.SelectedInwardSellDeltaBps,
					ConditionalBuyDeltaMeanBps:   joint.ConditionalBuy.ExpectedPairedDeltaBps,
					ConditionalSellDeltaMeanBps:  joint.ConditionalSell.ExpectedPairedDeltaBps,
				})
			if joint.PathEffectiveSamples > 0 {
				s.jointCandidateCount++
				s.jointCandidateUtilizationSum += joint.CapitalUtilization
				if s.jointBestObserved == 0 ||
					joint.ExpectedPnLJPYHour > s.maximumJointExpectedPnLJPYHour {
					s.maximumJointExpectedPnLJPYHour = joint.ExpectedPnLJPYHour
				}
				if s.jointBestObserved == 0 ||
					joint.LowerPnLJPYHour > s.maximumJointLowerPnLJPYHour {
					s.maximumJointLowerPnLJPYHour = joint.LowerPnLJPYHour
				}
				if s.jointBestObserved == 0 ||
					joint.PathEffectiveSamples < s.minimumJointPathEffectiveSamples {
					s.minimumJointPathEffectiveSamples = joint.PathEffectiveSamples
				}
				s.jointBestObserved++
			}
			if joint.Enabled {
				s.jointQuoteAccepted++
				s.jointCapitalUtilizationSum += joint.CapitalUtilization
				s.jointPairCapitalUtilizationSum += joint.PairCapitalUtilization
			}
			if joint.Applied {
				s.jointQuoteApplied++
				if selected := joint.Crossing.Horizon; selected > 0 {
					horizon = selected
				}
				plan = joint.Plan
				projection = joint.Projection
				decision = joint.Crossing
				buyRate = joint.Crossing.BuyTouchRatePerHour()
				sellRate = joint.Crossing.SellTouchRatePerHour()
				orderReviewDuration = 0
				if plan.AllowBid {
					buyReview := cfg.DynamicOrderKeepDecision(
						horizon, cfg.OrderKeepDistanceBps(plan.BidTouchDistanceBps),
						buyEffectiveVolBps)
					orderReviewDuration = buyReview.Duration
				}
				if plan.AllowAsk {
					sellReview := cfg.DynamicOrderKeepDecision(
						horizon, cfg.OrderKeepDistanceBps(plan.AskTouchDistanceBps),
						sellEffectiveVolBps)
					if orderReviewDuration <= 0 ||
						(sellReview.Duration > 0 && sellReview.Duration < orderReviewDuration) {
						orderReviewDuration = sellReview.Duration
					}
				}
			} else if joint.AuthoritativeRejection && !cfg.JointDistanceQuantity.ShadowOnly {
				plan.AllowBid = false
				plan.AllowAsk = false
				plan.BidQuoteNotional = 0
				plan.AskQuoteNotional = 0
				projection = gammacapture.ProbabilityCenteredQuoteDecision{
					Enabled: true,
					Reason:  joint.Reason,
				}
				fastValueRejected = true
			}
		}
	}
	if s.regimeExpectedValueSizer != nil {
		sizingPlan, sizingProjection, sizingDecision := plan, projection, decision
		wasRejected := fastValueRejected
		if wasRejected {
			sizingPlan, sizingProjection, sizingDecision =
				regimeSoftBasePlan, regimeSoftBaseProjection, regimeSoftBaseDecision
		}
		if sizing, evaluated := s.applyRegimeExpectedValueSizing(
			book, horizon, fastRiskAversion, sizingPlan, sizingProjection,
			sizingDecision, fastDrift, projectionInput.CurrentInventoryNotionalJPY,
			projectionInput.TargetInventoryNotionalJPY, pairEquity); evaluated {
			plan = sizing.Plan
			projection = sizing.Projection
			decision = sizing.Decision
			if sizing.Scale > 0 {
				fastValueRejected = false
				if wasRejected {
					selectedJoint = gammacapture.JointDistanceQuantityDecision{
						Reason: "regime expected-value quantity sizing after fee-net rejection",
					}
				}
			} else {
				fastValueRejected = true
			}
		}
	}
	if s.horizonConditionedUtility != nil {
		utilityPlan, utilityProjection := plan, projection
		wasRejected := fastValueRejected
		if wasRejected {
			utilityPlan, utilityProjection = regimeSoftBasePlan, regimeSoftBaseProjection
		}
		utility := s.horizonConditionedUtility.predictAndRecord(
			book, utilityPlan, utilityProjection, pairEquity, fastRiskAversion)
		if utility.Evaluated {
			plan, projection = utilityPlan, utilityProjection
			projection.BuyNotionalJPY *= utility.BuyScale
			projection.SellNotionalJPY *= utility.SellScale
			projection.ProjectedGrossNotionalJPY = projection.BuyNotionalJPY + projection.SellNotionalJPY
			plan.BidQuoteNotional *= utility.BuyScale
			plan.AskQuoteNotional *= utility.SellScale
			plan.AllowBid = plan.AllowBid && utility.BuyScale > 0
			plan.AllowAsk = plan.AllowAsk && utility.SellScale > 0
			if utility.Positive {
				fastValueRejected = false
				if wasRejected {
					selectedJoint = gammacapture.JointDistanceQuantityDecision{
						Reason: "horizon-conditioned continuation utility after fee-net rejection",
					}
				}
			} else {
				fastValueRejected = wasRejected
			}
		}
	}
	completionContractActive := s.applyCompletionContract(
		book, mid, preCancelBase, preCancelQuote,
		&plan, &projection, &decision, &selectedJoint, &orderReviewDuration)
	if completionContractActive {
		fastValueRejected = false
	}
	if fastValueRejected {
		s.cancelQuotes(book.time)
	}
	if !completionContractActive && cfg.FastTargetExecution.Enabled && posteriorTarget.Enabled {
		fastTargetDirection := 0
		if projectionTargetBase > s.inventory {
			fastTargetDirection = 1
		} else if projectionTargetBase < s.inventory {
			fastTargetDirection = -1
		}
		passiveQuotePrice := plan.BidPrice
		passiveAvailable := !fastValueRejected && plan.AllowBid && passiveQuotePrice > 0
		touchProbability := decision.BuyTouchProbability
		touchStdError := decision.BuyTouchStdError
		if fastTargetDirection < 0 {
			passiveQuotePrice = plan.AskPrice
			passiveAvailable = !fastValueRejected && plan.AllowAsk && passiveQuotePrice > 0
			touchProbability = decision.SellTouchProbability
			touchStdError = decision.SellTouchStdError
		}
		if !passiveAvailable {
			passiveQuotePrice, touchProbability, touchStdError = 0, 0, 0
		}
		modelUpdateInterval := time.Duration(cfg.HorizonUpdateInterval)
		if modelUpdateInterval <= 0 {
			modelUpdateInterval = 5 * time.Minute
		}
		modelUpdatedAt := book.time.Truncate(modelUpdateInterval)
		downside := s.horizonModel.ExecutableDownsideDecision(horizon)
		upside := s.horizonModel.ExecutableUpsideDecision(horizon)
		fastTarget := gammacapture.EvaluateFastTargetExecution(
			cfg.FastTargetExecution,
			gammacapture.FastTargetExecutionInput{
				Now: book.time, ModelUpdatedAt: modelUpdatedAt,
				LastExecutionModelAt:   s.lastFastTargetExecutionModelAt,
				LastExecutionDirection: s.lastFastTargetExecutionDirection,
				Horizon:                horizon, Direction: fastTargetDirection,
				CurrentInventoryBase: s.inventory, TargetInventoryBase: projectionTargetBase,
				AvailableBase: preCancelBase, AvailableQuote: preCancelQuote,
				BestBid: book.bid, BestBidSize: book.bidSize,
				BestAsk: book.ask, BestAskSize: book.askSize,
				PassiveQuotePrice: passiveQuotePrice, PassiveAvailable: passiveAvailable,
				TouchProbability: touchProbability, TouchStdError: touchStdError,
				InventoryReturnMeanBps:        posteriorTarget.InventoryReturnMean,
				InventoryReturnSEBps:          posteriorTarget.InventoryReturnSE,
				InventoryPredictiveSDBps:      posteriorTarget.InventoryPredictiveSD,
				DirectionConfidence:           posteriorTarget.DirectionConfidence,
				PersistentDownsideActive:      downside.Active,
				PersistentDownsideEValue:      downside.DownEValue,
				PersistentDownsideForecastBps: downside.BidForecastBps,
				PersistentUpsideActive:        upside.Active,
				PersistentUpsideEValue:        upside.DownEValue,
				PersistentUpsideForecastBps:   upside.BidForecastBps,
				PairEquityJPY:                 pairEquity,
				RiskAversion:                  fastRiskAversion,
				ConfidenceZScore:              cfg.InventoryRiskZScore,
				MakerFeeBps:                   cfg.MakerFeeBps, TakerFeeBps: cfg.TakerFeeBps,
				MinimumQuantityBase: 100 / book.ask, MinimumNotionalJPY: 100,
			})
		s.recordFastTargetActiveDecision(book.time, modelUpdatedAt, fastTarget)
		if fastTarget.Trigger {
			s.cancelQuotes(book.time)
			s.scheduleFastTargetIOC(fastTarget, modelUpdatedAt)
			return
		}
	}
	if fastValueRejected {
		modelUpdateInterval := time.Duration(cfg.HorizonUpdateInterval)
		if modelUpdateInterval <= 0 {
			modelUpdateInterval = time.Minute
		}
		s.lastQuoteAt = book.time
		s.windowEndsAt = book.time.Add(modelUpdateInterval)
		s.noOrderRetryAfter = s.windowEndsAt
		s.noOrderReferenceBid = book.bid
		s.noOrderReferenceAsk = book.ask
		return
	}
	projectionUsed := projection.Enabled
	buyNotional, sellQty := 0.0, 0.0
	if projectionUsed {
		buyNotional = math.Min(preCancelQuote, projection.BuyNotionalJPY*plan.BidPrice/mid)
		sellQty = math.Min(preCancelBase, projection.SellNotionalJPY/mid)
	} else {
		fallback := gammacapture.TargetAwareFallbackQuoteNotionals(
			gammacapture.TargetAwareFallbackQuoteInput{
				CurrentInventoryNotionalJPY:   s.inventory * mid,
				TargetInventoryNotionalJPY:    projectionTargetBase * mid,
				HardLowerInventoryNotionalJPY: hardBand.MinInventory * mid,
				HardUpperInventoryNotionalJPY: hardBand.MaxInventory * mid,
				BuyNotionalCapJPY: math.Min(
					riskUtilizationSizing.BuyNotionalCapJPY,
					math.Max(0, notionals.Buy)*mid/plan.BidPrice),
				SellNotionalCapJPY: math.Min(
					riskUtilizationSizing.SellNotionalCapJPY,
					math.Max(0, notionals.Sell/plan.AskPrice)*mid),
				MinBuyNotionalJPY:  projectionInput.MinBuyNotionalJPY,
				MinSellNotionalJPY: projectionInput.MinSellNotionalJPY,
			})
		buyNotional = math.Min(preCancelQuote, fallback.BuyNotionalJPY*plan.BidPrice/mid)
		sellQty = math.Min(preCancelBase, fallback.SellNotionalJPY/mid)
	}
	originCrossing := decision
	if selectedJoint.Applied {
		originCrossing = selectedJoint.Crossing
	}
	origin := replayQuoteOrigin{
		PlacedAt: book.time, Horizon: horizon,
		CompletionHorizon: cfg.JointContinuationHorizon(horizon),
		BestBid:           book.bid, BestAsk: book.ask, BookImbalance: imbalance,
		BidPrice: plan.BidPrice, AskPrice: plan.AskPrice,
		BuyNotionalJPY: projection.BuyNotionalJPY, SellNotionalJPY: projection.SellNotionalJPY,
		AdmissionJointComplementary: selectedJoint.Applied &&
			selectedJoint.AdmissionJointComplementary && plan.AllowBid && plan.AllowAsk &&
			projection.BuyNotionalJPY > 0 && projection.SellNotionalJPY > 0,
		FastUp: fast.Up, FastDown: fast.Down, FastDirection: direction,
		FastDirectionConfidence:        fastInference.DirectionConfidence,
		FastEvidenceCoverage:           directionCoverage,
		FastMidReturn1mBps:             evidence.MidReturn1mBps,
		FastMidReturn5mBps:             evidence.MidReturn5mBps,
		FastMidDrawdownBps:             evidence.MidDrawdownBps,
		FastRebound30sBps:              evidence.MidRebound30sBps,
		FastTradeImbalance5m:           evidence.SignedTradeImbalance5m,
		FastOFI30s:                     evidence.OrderFlowImbalance30s,
		FastMicropriceDisplacement:     evidence.MicropriceDisplacement,
		CurrentInventoryJPY:            s.inventory * mid,
		TargetInventoryJPY:             projectionTargetBase * mid,
		PosteriorInventoryReturnBps:    posteriorTarget.InventoryReturnMean,
		PosteriorInventoryPredictiveSD: posteriorTarget.InventoryPredictiveSD,
		BuyTouchProbability:            originCrossing.BuyTouchProbability,
		SellTouchProbability:           originCrossing.SellTouchProbability,
		JointExpectedPnLJPYHour:        selectedJoint.ExpectedPnLJPYHour,
		JointLowerPnLJPYHour:           selectedJoint.LowerPnLJPYHour,
		JointFeeValueNetJPY:            selectedJoint.FeeValueNetJPY,
		JointEffectiveSamples:          selectedJoint.PathEffectiveSamples,
		BuyAdmissionUtilityBoundJPY:    selectedJoint.BuyAdmissionUtilityBoundJPY,
		SellAdmissionUtilityBoundJPY:   selectedJoint.SellAdmissionUtilityBoundJPY,
	}
	if plan.AllowBid && !retainBid && buyNotional >= 100 {
		quantity := buyNotional / plan.BidPrice
		s.bidOrder = productionReplayOrder{active: true, side: types.SideTypeBuy,
			price: plan.BidPrice, quantity: quantity, remaining: quantity,
			queueAhead: book.bidSize * s.queueFactor, placedAt: book.time, origin: origin}
	}
	if plan.AllowAsk && !retainAsk && sellQty*plan.AskPrice >= 100 {
		s.askOrder = productionReplayOrder{active: true, side: types.SideTypeSell,
			price: plan.AskPrice, quantity: sellQty, remaining: sellQty,
			queueAhead: book.askSize * s.queueFactor, placedAt: book.time, origin: origin}
	}
	if s.bidOrder.active || s.askOrder.active {
		s.refreshes++
		s.noOrderRetryAfter = time.Time{}
		s.noOrderReferenceBid = 0
		s.noOrderReferenceAsk = 0
		s.lastQuoteAt = book.time
		s.windowEndsAt = book.time.Add(orderReviewDuration)
		s.lastBestBid, s.lastBestAsk, s.lastMid, s.lastImbalance = book.bid, book.ask, mid, imbalance
		s.quotedTargetRatio = effectiveTargetRatio
		s.quotedFastTargetRatio = selectedProjectionTargetRatio
		s.quotedTargetSet = true
		s.quotedFastReservationBps = appliedFastReservationBps
		s.quoteActive++
	}
}

func (s *productionReplayState) applyCompletionContract(
	book bboSnapshot,
	mid, availableBase, availableQuote float64,
	plan *gammacapture.MarketMakerQuotePlan,
	projection *gammacapture.ProbabilityCenteredQuoteDecision,
	crossing *gammacapture.MarketMakerHorizonDecision,
	joint *gammacapture.JointDistanceQuantityDecision,
	orderReviewDuration *time.Duration,
) bool {
	contract := s.completionContract
	if !contract.active {
		return false
	}
	if !book.time.Before(contract.until) || contract.price <= 0 || contract.quantity <= 0 ||
		mid <= 0 || book.bid <= 0 || book.ask <= book.bid {
		s.completionContract = replayCompletionContract{}
		return false
	}

	quantity, price := contract.quantity, contract.price
	plan.AllowBid, plan.AllowAsk = false, false
	plan.BidQuoteNotional, plan.AskQuoteNotional = 0, 0
	if contract.side == types.SideTypeBuy {
		price = math.Min(price, math.Nextafter(book.ask, 0))
		quantity = math.Min(quantity, availableQuote/price)
		if quantity*price < 100 {
			s.completionContract = replayCompletionContract{}
			return false
		}
		plan.AllowBid = true
		plan.BidPrice = price
		plan.BidQuoteNotional = quantity * price
	} else if contract.side == types.SideTypeSell {
		price = math.Max(price, math.Nextafter(book.bid, math.Inf(1)))
		quantity = math.Min(quantity, availableBase)
		if quantity*price < 100 {
			s.completionContract = replayCompletionContract{}
			return false
		}
		plan.AllowAsk = true
		plan.AskPrice = price
		plan.AskQuoteNotional = quantity * price
	} else {
		s.completionContract = replayCompletionContract{}
		return false
	}

	buyDistance, sellDistance, grossEdge := gammacapture.MakerTouchDistances(
		book.bid, book.ask, plan.BidPrice, plan.AskPrice)
	remaining := contract.until.Sub(book.time)
	referenceHorizon := contract.referenceHorizon
	if referenceHorizon <= 0 {
		// Compatibility for checkpoints produced before the statistical clock
		// became explicit. New contracts always retain their admitted horizon.
		referenceHorizon = remaining
	}
	*crossing = s.horizonModel.CrossingDecisionAtSideDistances(
		book.time, s.cfg, referenceHorizon, buyDistance, sellDistance, grossEdge)
	projected := gammacapture.ProbabilityCenteredQuoteDecision{
		Enabled:             true,
		Reason:              "matched joint-cycle completion contract",
		BuyFillProbability:  crossing.BuyTouchProbability,
		SellFillProbability: crossing.SellTouchProbability,
	}
	if contract.side == types.SideTypeBuy {
		projected.BuyNotionalJPY = quantity * mid
	} else {
		projected.SellNotionalJPY = quantity * mid
	}
	projected.ProjectedGrossNotionalJPY =
		projected.BuyNotionalJPY + projected.SellNotionalJPY
	*projection = projected
	*joint = gammacapture.JointDistanceQuantityDecision{
		Enabled: true, Applied: true,
		Reason:                      "matched joint-cycle completion contract",
		Plan:                        *plan,
		Projection:                  projected,
		Crossing:                    *crossing,
		CompletionProtected:         true,
		AdmissionJointComplementary: true,
	}
	*orderReviewDuration = remaining
	return true
}

func replayNoOrderReferenceMoved(
	referenceBid, referenceAsk, currentBid, currentAsk, thresholdBps float64,
) bool {
	if referenceBid <= 0 || referenceAsk < referenceBid || currentBid <= 0 ||
		currentAsk < currentBid || thresholdBps <= 0 {
		return false
	}
	return math.Max(
		math.Abs(math.Log(currentBid/referenceBid))*10_000,
		math.Abs(math.Log(currentAsk/referenceAsk))*10_000,
	) >= thresholdBps
}

// recordHorizonDiagnostics samples every configured horizon only on the same
// causal update clock used by the live selector. It is deliberately aggregate:
// replay output can identify whether a short horizon disappears at crossing
// health, completed-path evidence, positive terminal value, or final selection
// without emitting another per-BBO trace or changing the strategy decision.
func (s *productionReplayState) recordHorizonDiagnostics(
	book bboSnapshot, selectedHorizon time.Duration, pairEquityJPY float64,
) {
	if book.time.Before(s.tradingFrom) || pairEquityJPY <= 0 {
		return
	}
	updateInterval := time.Duration(s.cfg.HorizonUpdateInterval)
	if updateInterval <= 0 {
		updateInterval = time.Minute
	}
	bucket := book.time.UTC().Truncate(updateInterval)
	if bucket.Equal(s.lastHorizonDiagnosticBucket) {
		return
	}
	s.lastHorizonDiagnosticBucket = bucket

	prior := s.horizonModel.EmpiricalSideVolatilityEstimate(
		book.time, time.Duration(s.cfg.HorizonLookback))
	currentInventoryJPY := s.inventory * (book.bid + book.ask) / 2
	targetInventoryJPY := s.cfg.InventoryCapitalTargetRatio * pairEquityJPY
	for _, horizon := range s.cfg.TradingHorizons() {
		live := s.horizonModel.EmpiricalSideVolatilityEstimate(book.time, horizon)
		buyVolatility, _ := gammacapture.ShrinkVolatility(
			live.BuyBps, prior.BuyBps, live.BuySamples, s.cfg.HorizonMinSamples)
		sellVolatility, _ := gammacapture.ShrinkVolatility(
			live.SellBps, prior.SellBps, live.SellSamples, s.cfg.HorizonMinSamples)
		decision := s.horizonModel.DecisionForHorizon(
			book.time, s.cfg, math.Max(buyVolatility, sellVolatility),
			book.bid, book.ask, horizon)

		key := horizon.String()
		diagnostic := s.horizonDiagnostics[key]
		if diagnostic == nil {
			diagnostic = &productionReplayHorizonDiagnostic{}
			s.horizonDiagnostics[key] = diagnostic
		}
		diagnostic.Evaluations++
		diagnostic.scoreSum += decision.ScoreBpsPerHour
		diagnostic.scoreStdErrorSum += decision.ScoreStdErrorBpsHour
		diagnostic.buyTouchProbabilitySum += decision.BuyTouchProbability
		diagnostic.sellTouchProbabilitySum += decision.SellTouchProbability
		if decision.ScoreBpsPerHour > diagnostic.MaximumScoreBpsPerHour {
			diagnostic.MaximumScoreBpsPerHour = decision.ScoreBpsPerHour
		}
		if horizon == selectedHorizon {
			diagnostic.Selected++
		}
		if !decision.HasSufficientCrossings(s.cfg.HorizonMinSamples) {
			continue
		}
		diagnostic.SufficientCrossings++
		stats := s.horizonModel.JointPathPayoffStatistics(
			book.time, s.cfg, horizon,
			decision.BuyTouchDistanceBps, decision.SellTouchDistanceBps)
		if stats.EffectiveSamples <= 1 {
			continue
		}
		diagnostic.PathReady++
		diagnostic.pathEffectiveSamplesSum += stats.EffectiveSamples
		diagnostic.pathEffectiveSamplesBaselineSum += stats.EffectiveSamplesBaseline
		diagnostic.pathDecayHalfLifeSum += stats.PathDecayHalfLifeSeconds
		diagnostic.pathDecayAutocorrelationSum += stats.PathDecayAutocorrelation
		diagnostic.pathDecayPersistenceSum += stats.PathDecayPersistenceObservations
		payoff := stats.EvaluateTargetRelativePosition(
			currentInventoryJPY, targetInventoryJPY,
			100, 100, pairEquityJPY, s.cfg.MacroInventory.RiskAversion, 0)
		if payoff.ExpectedPnLJPY-payoff.KellyPenaltyJPY > 0 {
			diagnostic.PositiveRiskAdjustedPathMean++
		}
	}
}

func (s *productionReplayState) scheduleMacroIOC(at time.Time, decision gammacapture.MacroActiveExecutionDecision, closedBarAt time.Time) {
	s.pendingMacroIOC = productionReplayMacroIOC{
		active: true, direction: decision.Direction,
		quantity: decision.Quantity, worstPrice: decision.WorstPrice,
		closedBarAt: closedBarAt,
	}
	s.macroInventoryState.LastActiveExecutionAt = at
	s.macroInventoryState.LastActiveExecutionBarAt = closedBarAt
	s.macroActiveAttempts++
}

func (s *productionReplayState) recordMacroActiveDecision(
	at, closedBarAt time.Time,
	decision gammacapture.MacroActiveExecutionDecision,
) {
	if closedBarAt.IsZero() || decision.Direction == 0 ||
		closedBarAt.Equal(s.lastMacroActiveDecisionBarAt) {
		return
	}
	s.lastMacroActiveDecisionBarAt = closedBarAt
	s.macroActiveDecisions = append(s.macroActiveDecisions, productionReplayMacroDecision{
		At: at, ClosedBarAt: closedBarAt,
		Direction: decision.Direction, Trigger: decision.Trigger, Reason: decision.Reason,
		Quantity:               decision.Quantity,
		TargetGapBase:          decision.TargetGapBase,
		TacticalTargetGapBase:  decision.TacticalTargetGapBase,
		ResidualMakerGapBase:   decision.ResidualMakerGapBase,
		PassiveMissProbability: decision.PassiveMissProbability,
		UrgentFraction:         decision.UrgentFraction,
		WaitLossBps:            decision.WaitLossBps,
		CrossingCostBps:        decision.PassiveToTouchCostBps + decision.FeeIncrementBps,
	})
}

func (s *productionReplayState) executePendingMacroIOC(book bboSnapshot) bool {
	if !s.pendingMacroIOC.active {
		return false
	}
	pending := s.pendingMacroIOC
	s.pendingMacroIOC = productionReplayMacroIOC{}
	quantity, price := pending.quantity, 0.0
	side := types.SideTypeBuy
	if pending.direction > 0 {
		if book.ask <= 0 || book.ask > pending.worstPrice {
			return false
		}
		price = book.ask
		quantity = math.Min(quantity, math.Min(book.askSize, s.quote/price))
	} else if pending.direction < 0 {
		side = types.SideTypeSell
		if book.bid <= 0 || book.bid < pending.worstPrice {
			return false
		}
		price = book.bid
		quantity = math.Min(quantity, math.Min(book.bidSize, s.inventory))
	} else {
		return false
	}
	if quantity <= 0 || quantity*price < 100 {
		return false
	}
	notional := quantity * price
	fee := notional * s.cfg.TakerFeeBps / 10_000
	if side == types.SideTypeBuy {
		s.inventory += quantity
		s.quote -= notional
		if s.unmatchedSells > 0 {
			s.unmatchedSells--
			s.roundTrips++
		} else {
			s.unmatchedBuys++
		}
	} else {
		s.inventory -= quantity
		s.quote += notional
		if s.unmatchedBuys > 0 {
			s.unmatchedBuys--
			s.roundTrips++
		} else {
			s.unmatchedSells++
		}
	}
	s.fees += fee
	s.takerFees += fee
	s.macroActiveFills++
	s.macroActiveQuantity += quantity
	s.fillEvents = append(s.fillEvents, replayFill{
		At: book.time, Side: side, Price: price, Quantity: quantity,
		NotionalJPY: notional, FeeJPY: fee,
		InventoryAfter: s.inventory, QuoteAfter: s.quote,
	})
	return true
}

func (s *productionReplayState) scheduleFastTargetIOC(
	decision gammacapture.FastTargetExecutionDecision,
	modelUpdatedAt time.Time,
) {
	s.pendingFastTargetIOC = productionReplayFastTargetIOC{
		active: true, direction: decision.Direction,
		quantity: decision.Quantity, worstPrice: decision.WorstPrice,
		modelUpdatedAt: modelUpdatedAt,
	}
	// An IOC is an execution attempt even if the next BBO escapes its limit.
	// Do not repeatedly chase within the same causal model-update epoch.
	s.lastFastTargetExecutionModelAt = modelUpdatedAt
	s.lastFastTargetExecutionDirection = decision.Direction
	s.fastTargetActiveAttempts++
}

func (s *productionReplayState) recordFastTargetActiveDecision(
	at, modelUpdatedAt time.Time,
	decision gammacapture.FastTargetExecutionDecision,
) {
	if modelUpdatedAt.IsZero() || modelUpdatedAt.Equal(s.lastFastTargetDecisionModelAt) {
		return
	}
	s.lastFastTargetDecisionModelAt = modelUpdatedAt
	s.fastTargetActiveDecisions = append(s.fastTargetActiveDecisions,
		replayFastTargetDecision{
			At: at, ModelUpdatedAt: modelUpdatedAt,
			Direction: decision.Direction, Trigger: decision.Trigger, Reason: decision.Reason,
			Quantity: decision.Quantity, TargetGapBase: decision.TargetGapBase,
			ResidualMakerGapBase:   decision.ResidualMakerGapBase,
			TouchProbability:       decision.PassiveTouchProbability,
			TouchProbabilityUpper:  decision.PassiveTouchProbabilityUpper,
			MissProbabilityLower:   decision.PassiveMissProbabilityLower,
			ExpectedAdverseMoveBps: decision.ExpectedAdverseMoveBps,
			WaitLossBps:            decision.WaitLossBps,
			CrossingCostBps:        decision.ExecutionCostBps,
			DownsideActive:         decision.PersistentDownsideActive,
			DownsideEValue:         decision.PersistentDownsideEValue,
			DownsideForecastBps:    decision.PersistentDownsideForecastBps,
			UpsideActive:           decision.PersistentUpsideActive,
			UpsideEValue:           decision.PersistentUpsideEValue,
			UpsideForecastBps:      decision.PersistentUpsideForecastBps,
			VariancePenaltyBps:     decision.InventoryVariancePenaltyBps,
			ActiveCEBps:            decision.ActiveCertaintyEquivalentBps,
		})
}

func (s *productionReplayState) executePendingFastTargetIOC(book bboSnapshot) bool {
	if !s.pendingFastTargetIOC.active {
		return false
	}
	pending := s.pendingFastTargetIOC
	s.pendingFastTargetIOC = productionReplayFastTargetIOC{}
	quantity, price := pending.quantity, 0.0
	side := types.SideTypeBuy
	if pending.direction > 0 {
		if book.ask <= 0 || book.ask > pending.worstPrice {
			return false
		}
		price = book.ask
		quantity = math.Min(quantity, math.Min(book.askSize, s.quote/price))
	} else if pending.direction < 0 {
		side = types.SideTypeSell
		if book.bid <= 0 || book.bid < pending.worstPrice {
			return false
		}
		price = book.bid
		quantity = math.Min(quantity, math.Min(book.bidSize, s.inventory))
	} else {
		return false
	}
	if quantity <= 0 || quantity*price < 100 {
		return false
	}
	notional := quantity * price
	fee := notional * s.cfg.TakerFeeBps / 10_000
	if side == types.SideTypeBuy {
		s.inventory += quantity
		s.quote -= notional
		s.buys++
		if s.unmatchedSells > 0 {
			s.unmatchedSells--
			s.roundTrips++
		} else {
			s.unmatchedBuys++
		}
	} else {
		s.inventory -= quantity
		s.quote += notional
		s.sells++
		if s.unmatchedBuys > 0 {
			s.unmatchedBuys--
			s.roundTrips++
		} else {
			s.unmatchedSells++
		}
	}
	s.fills++
	s.fees += fee
	s.takerFees += fee
	s.fastTargetActiveFills++
	s.fastTargetActiveQuantity += quantity
	day := book.time.Format("2006-01-02")
	if s.fillsByDay[day] == nil {
		s.fillsByDay[day] = &productionReplayDay{Day: day}
	}
	if side == types.SideTypeBuy {
		s.fillsByDay[day].BuyFills++
	} else {
		s.fillsByDay[day].SellFills++
	}
	s.fillEvents = append(s.fillEvents, replayFill{
		At: book.time, Side: side, Price: price, Quantity: quantity,
		NotionalJPY: notional, FeeJPY: fee,
		InventoryAfter: s.inventory, QuoteAfter: s.quote,
	})
	return true
}

func (s *productionReplayState) tryAcquisitionReset(book bboSnapshot, horizonDecision gammacapture.MarketMakerHorizonDecision, horizon time.Duration, effectiveVolBps float64, plan gammacapture.MarketMakerQuotePlan, notionals gammacapture.SideQuoteNotionals, evidence gammacapture.FastEvidenceSnapshot) bool {
	cfg := s.cfg.AcquisitionReset
	// This function is called only by the explicit acquisition research mode.
	cfg.Enabled = true
	if !s.bidOrder.active || book.time.Before(s.acquisitionCooldownUntil) ||
		s.inventory >= s.inventoryBand.Target || s.quote <= 0 || book.askSize <= 0 {
		return false
	}
	drawdownLimit5mBps, calibrated := cfg.CalibratedDrawdownLimit5mBps(evidence)
	return1mMoveBps, return1mReady := cfg.CalibratedReturnMoveBps(evidence, time.Minute)
	return5mMoveBps, return5mReady := cfg.CalibratedReturnMoveBps(evidence, 5*time.Minute)
	returnCalibrationReady := return1mReady && return5mReady
	if calibrated {
		s.acquisitionDrawdownLimitSamples++
		s.acquisitionDrawdownLimitSumBps += drawdownLimit5mBps
		s.maxAcquisitionDrawdownLimitBps = math.Max(s.maxAcquisitionDrawdownLimitBps, drawdownLimit5mBps)
		if s.minAcquisitionDrawdownLimitBps == 0 || drawdownLimit5mBps < s.minAcquisitionDrawdownLimitBps {
			s.minAcquisitionDrawdownLimitBps = drawdownLimit5mBps
		}
	}
	decision := cfg.Evaluate(gammacapture.AcquisitionResetInput{
		Now: book.time, DeficitSince: s.acquisitionDeficitSince, AnchorMidPrice: s.acquisitionDeficitAnchorMid,
		MidPrice: (book.bid + book.ask) / 2, BestAsk: book.ask,
		BidPrice: s.bidOrder.price, PlannedAskPrice: plan.AskPrice,
		MakerFeeBps: s.cfg.MakerFeeBps, TakerFeeBps: s.cfg.TakerFeeBps,
		AdverseSelectionBps: s.cfg.AdverseSelectionBps,
		MaxSlippageBps:      cfg.MaxSlippageBps,
		UpCrosses:           horizonDecision.UpCrosses, DownCrosses: horizonDecision.DownCrosses,
		UpCrossesPerHour:   horizonDecision.UpCrossesPerHour,
		DownCrossesPerHour: horizonDecision.DownCrossesPerHour,
		QuoteDistanceBps:   horizonDecision.QuoteDistanceBps,
		Horizon:            horizon, FillIntensityHaircut: cfg.FillIntensityHaircut,
		VolatilityPerSqrtSec:    effectiveVolBps / 10_000,
		EvidenceHealth:          evidence.Health,
		Return1mBps:             evidence.MidReturn1mBps,
		Return5mBps:             evidence.MidReturn5mBps,
		Return1mThresholdBps:    -return1mMoveBps,
		Return5mThresholdBps:    return5mMoveBps,
		ReturnCalibrationReady:  returnCalibrationReady,
		AdverseMoveThresholdBps: return5mMoveBps,
		Drawdown5mBps:           evidence.MidDrawdown5mBps,
		DrawdownLimit5mBps:      drawdownLimit5mBps,
		TradeCount5m:            evidence.TradeCount5m,
		BBOCount5m:              evidence.BBOCount5m,
	})
	s.acquisitionEvaluations++
	s.maxUpProbabilityLower = math.Max(s.maxUpProbabilityLower, decision.UpProbabilityLower)
	s.maxAcquisitionIOCValueBps = math.Max(s.maxAcquisitionIOCValueBps, decision.IOCValueBps)
	s.maxAcquisitionImprovementBps = math.Max(s.maxAcquisitionImprovementBps, decision.IOCImprovementBps)
	if !decision.Trigger {
		s.acquisitionRejections[decision.Reason]++
		return false
	}

	price := book.ask * (1 + cfg.MaxSlippageBps/10_000)
	quantity := math.Min(s.inventoryBand.Target-s.inventory, notionals.Buy/price)
	quantity = math.Min(quantity, s.quote/price)
	// BBO replay has no deeper book. Limiting the IOC to displayed best-ask
	// quantity avoids assuming unavailable depth while pricing at the full cap.
	quantity = math.Min(quantity, book.askSize)
	if quantity <= 0 || quantity*price < 100 {
		return false
	}
	s.acquisitionRejections["replay IOC below exchange minimum"]++

	s.cancelQuotes(book.time)
	notional := quantity * price
	fee := notional * s.cfg.TakerFeeBps / 10_000
	s.inventory += quantity
	s.quote -= notional
	s.fees += fee
	s.takerFees += fee
	s.acquisitionResets++
	s.acquisitionQuantity += quantity
	s.acquisitionCooldownUntil = book.time.Add(time.Duration(cfg.Cooldown))
	if s.unmatchedSells > 0 {
		s.acquisitionDeficitSince = time.Time{}
		s.acquisitionDeficitAnchorMid = 0
		s.unmatchedSells--
		s.roundTrips++
	} else {
		s.unmatchedBuys++
	}
	return true
}

func (s *productionReplayState) onTrade(trade tick) {
	observed := types.Trade{Symbol: s.symbol, Price: fixedpoint.NewFromFloat(trade.price), Quantity: fixedpoint.NewFromFloat(trade.size), Side: trade.side}
	for _, window := range s.fastWindows {
		if evidenceModel := s.fastEvidenceModels[window]; evidenceModel != nil {
			evidenceModel.ObserveTrade(trade.time, observed)
		}
	}
	if s.cfg.VolumeProfile.Enabled {
		s.horizonModel.ObservePublicTrade(
			trade.time, trade.price, trade.size, trade.side == types.SideTypeBuy, s.cfg)
	}
	if trade.time.Before(s.tradingFrom) {
		return
	}
	s.trades++
	if trade.side == types.SideTypeBuy && s.askOrder.active && s.askOrder.eligible && trade.price >= s.askOrder.price {
		s.consume(&s.askOrder, trade)
	}
	if trade.side == types.SideTypeSell && s.bidOrder.active && s.bidOrder.eligible && trade.price <= s.bidOrder.price {
		s.consume(&s.bidOrder, trade)
	}
}

func (s *productionReplayState) activatePendingQuotesOnNextBBO() {
	if s.bidOrder.active {
		s.bidOrder.eligible = true
	}
	if s.askOrder.active {
		s.askOrder.eligible = true
	}
}

func (s *productionReplayState) executeCrossedMakerOrdersAtBBO(book bboSnapshot) {
	// Once the top of book has moved through our resting price, any modeled
	// queue ahead and our full visible quantity at that price have necessarily
	// cleared: the exchange cannot publish a best ask below a still-resting bid,
	// or a best bid above a still-resting ask. The new L1 quantity is post-event
	// state and must not be reused as a cap on the already-completed execution.
	if s.bidOrder.active && s.bidOrder.eligible &&
		book.ask > 0 && book.ask <= s.bidOrder.price {
		s.bidOrder.queueAhead = 0
		s.consume(&s.bidOrder, tick{
			time: book.time, price: s.bidOrder.price,
			size: s.bidOrder.remaining, side: types.SideTypeSell,
		})
	}
	if s.askOrder.active && s.askOrder.eligible &&
		book.bid > 0 && book.bid >= s.askOrder.price {
		s.askOrder.queueAhead = 0
		s.consume(&s.askOrder, tick{
			time: book.time, price: s.askOrder.price,
			size: s.askOrder.remaining, side: types.SideTypeBuy,
		})
	}
}

func (s *productionReplayState) consume(order *productionReplayOrder, trade tick) {
	volume := trade.size
	if order.queueAhead >= volume {
		order.queueAhead -= volume
		return
	}
	volume -= order.queueAhead
	order.queueAhead = 0
	executed := math.Min(volume, order.remaining)
	if executed <= 0 {
		return
	}
	if order.quantity <= 0 {
		order.quantity = order.remaining
	}
	order.remaining -= executed
	order.filledQuantity += executed
	notional := executed * order.price
	fee := notional * s.cfg.MakerFeeBps / 10000
	order.accumulatedFee += fee
	s.fees += fee
	if order.side == types.SideTypeBuy {
		s.inventory += executed
		s.quote -= notional
	} else {
		s.inventory -= executed
		s.quote += notional
	}
	s.lastMakerFill = gammacapture.MakerPostFillState{
		Side: order.side, Price: order.price, Quantity: executed, At: trade.time,
	}
	// A joint-cycle admission is a contingent contract, not merely a scoring
	// diagnostic. Once its first leg fills, preserve the exact opposite price
	// and matched quantity for the continuation horizon that priced entry.
	// Otherwise the replay (and live policy) can accept a negative standalone
	// leg using completion value, then immediately discard that completion.
	if s.completionContract.active && order.side == s.completionContract.side {
		s.completionContract = replayCompletionContract{}
	} else if s.cfg.JointDistanceQuantity.CrossHorizonContinuation &&
		order.origin.AdmissionJointComplementary && order.origin.CompletionHorizon > 0 {
		contract := replayCompletionContract{
			active: true, quantity: executed,
			referenceHorizon: order.origin.CompletionHorizon,
			until:            trade.time.Add(order.origin.CompletionHorizon),
		}
		if order.side == types.SideTypeBuy {
			contract.side, contract.price = types.SideTypeSell, order.origin.AskPrice
		} else {
			contract.side, contract.price = types.SideTypeBuy, order.origin.BidPrice
		}
		if contract.price > 0 {
			s.completionContract = contract
		}
	}
	s.fillRefreshPending = true
	// Binance emits every partial execution as a private trade, and the live
	// strategy immediately rebuilds from that changed balance. Keep the replay
	// event stream at the same granularity even when the next BBO cancels the
	// unfilled remainder. FullFills/BuyFills/SellFills below deliberately retain
	// their completed-order semantics for queue calibration.
	origin := order.origin
	s.fillEvents = append(s.fillEvents, replayFill{
		At: trade.time, Side: order.side, Price: order.price,
		Quantity:       executed,
		NotionalJPY:    notional,
		FeeJPY:         fee,
		InventoryAfter: s.inventory,
		QuoteAfter:     s.quote,
		QuoteOrigin:    &origin,
	})

	if order.remaining > 1e-12 {
		return
	}
	order.active = false
	s.fills++
	day := trade.time.UTC().Format(time.DateOnly)
	if s.fillsByDay[day] == nil {
		s.fillsByDay[day] = &productionReplayDay{Day: day}
	}
	if order.side == types.SideTypeBuy {
		s.buys++
		s.fillsByDay[day].BuyFills++
		if s.unmatchedSells > 0 {
			s.unmatchedSells--
			s.roundTrips++
		} else {
			s.unmatchedBuys++
		}
	} else {
		s.sells++
		s.fillsByDay[day].SellFills++
		if s.unmatchedBuys > 0 {
			s.unmatchedBuys--
			s.roundTrips++
		} else {
			s.unmatchedSells++
		}
	}
}

func (s *productionReplayState) freeBalances() (base, quote float64) {
	base, quote = s.inventory, s.quote
	if s.askOrder.active {
		base -= s.askOrder.remaining
	}
	if s.bidOrder.active {
		quote -= s.bidOrder.remaining * s.bidOrder.price
	}
	return math.Max(0, base), math.Max(0, quote)
}

func (s *productionReplayState) cancelQuotes(at time.Time) {
	if !s.lastQuoteAt.IsZero() && at.After(s.lastQuoteAt) {
		s.quoteLifeSum += at.Sub(s.lastQuoteAt).Seconds()
		s.quoteLifeSamples++
	}
	s.bidOrder, s.askOrder = productionReplayOrder{}, productionReplayOrder{}
	s.lastQuoteAt, s.windowEndsAt = time.Time{}, time.Time{}
}

func (s *productionReplayState) result(books []bboSnapshot) productionReplayResult {
	resultTo := books[len(books)-1].time
	if s.stopped && !s.stopAt.IsZero() {
		resultTo = s.stopAt
	}
	resultFrom := s.scoreFrom
	if resultFrom.IsZero() {
		resultFrom = s.tradingFrom
	}
	evalBooks := filterBBO(books, resultFrom, resultTo.Add(time.Nanosecond))
	if len(evalBooks) == 0 {
		return productionReplayResult{Mode: s.mode}
	}
	books = evalBooks
	lastMid := (books[len(books)-1].bid + books[len(books)-1].ask) / 2
	hours := s.activeDuration.Hours()
	initialEquity := s.initialEquity
	initialHoldEquity := s.initialEquity
	initialQuote, initialInventory := s.initialQuote, s.initialInventory
	initialFees := 0.0
	if s.scoreStarted {
		initialEquity = s.scoreInitialEquity
		initialHoldEquity = s.scoreInitialHoldEquity
		initialQuote, initialInventory = s.scoreInitialQuote, s.scoreInitialInventory
		initialFees = s.scoreInitialFees
	}
	r := productionReplayResult{Mode: s.mode, From: books[0].time, To: books[len(books)-1].time, ActiveHours: hours, BBOEvents: s.books, AggTradeEvents: s.trades, DataGaps: s.gaps, QueueMultiplier: s.queueFactor, QuoteRefreshes: s.refreshes, FullFills: s.fills, BuyFills: s.buys, SellFills: s.sells, RoundTrips: s.roundTrips, FastInventoryZoneDecisions: s.fastInventoryZoneDecisions, LongHorizonAdjustmentDecisions: s.longHorizonAdjustmentDecisions, AcquisitionResets: s.acquisitionResets, AcquisitionQuantity: s.acquisitionQuantity, MacroActiveAttempts: s.macroActiveAttempts, MacroActiveFills: s.macroActiveFills, MacroActiveQuantity: s.macroActiveQuantity, FastTargetActiveAttempts: s.fastTargetActiveAttempts, FastTargetActiveFills: s.fastTargetActiveFills, FastTargetActiveQuantity: s.fastTargetActiveQuantity, AcquisitionEvaluations: s.acquisitionEvaluations, AcquisitionRejections: s.acquisitionRejections, AcquisitionDrawdownLimitSamples: s.acquisitionDrawdownLimitSamples, MinimumAcquisitionDrawdownLimitBps: s.minAcquisitionDrawdownLimitBps, MaximumAcquisitionDrawdownLimitBps: s.maxAcquisitionDrawdownLimitBps, MaxUpProbabilityLower: s.maxUpProbabilityLower, MaxAcquisitionIOCValueBps: s.maxAcquisitionIOCValueBps, MaxAcquisitionImprovementBps: s.maxAcquisitionImprovementBps, MakerFeesJPY: s.fees - s.takerFees - initialFees, TakerFeesJPY: s.takerFees, NetPnLJPY: s.quote + s.inventory*lastMid - s.fees - initialEquity, HoldPnLJPY: initialQuote + initialInventory*lastMid - initialHoldEquity, StoppedEarly: s.stopped, StopAt: s.stopAt, StopReason: s.stopReason, MaximumDrawdownPct: s.maximumDrawdownPct, Limitations: []string{"no level-2 depth or exchange queue priority", "orders decided at BBO[t] execute no earlier than BBO[t+1]", "queue multiplier is calibrated to aggregate confirmed fills, not per-order queue position", "public aggregate trades cannot identify our private execution"}}
	r.AsymmetricRiskEnabled = s.asymmetricOscillationRisk != nil
	r.VolumeProfileEnabled = s.cfg.VolumeProfile.Enabled
	r.VolumeProfileAsymmetricPOCRisk = s.cfg.VolumeProfile.AsymmetricPOCRisk
	r.VolumeProfileSamples = s.volumeProfileSamples
	r.VolumeProfileReadySamples = s.volumeProfileReadySamples
	r.RelativeHoldRiskEnabled = s.relativeHoldRiskModel != nil
	r.RelativeHoldRiskApplied = s.relativeHoldRiskApplied
	r.RelativeHoldRiskUtilitySumJPYHour = s.relativeHoldRiskUtilitySumJPYHour
	r.RelativeHoldRiskUtilityEvaluations = s.relativeHoldRiskUtilityEvaluations
	r.RelativeHoldRiskReadyInputs = s.relativeHoldRiskReadyInputs
	r.RelativeHoldRiskReadyDecisions = s.relativeHoldRiskReadyDecisions
	if s.relativeHoldRiskModel != nil {
		relative := s.relativeHoldRiskModel.Snapshot()
		r.RelativeHoldRiskMaturedLabels = relative.MaturedLabels
		r.RelativeHoldRiskEffectiveSamples = relative.EffectiveSamples
		r.RelativeHoldRiskDownsideSamples = relative.DownsideEffectiveSamples
		r.RelativeHoldRiskMeanExcessBps = relative.MeanExcessReturnBps
		r.RelativeHoldRiskTrackingErrorBps = relative.TrackingErrorBps
		r.RelativeHoldRiskDownsideBeta = relative.DownsideBeta
		r.RelativeHoldRiskTotalBeta = relative.TotalBeta
		r.RelativeHoldRiskTotalBetaUpper = relative.TotalBetaUpper
		r.RelativeHoldRiskDownsideCVaRBps = relative.DownsideCVaRBps
	}
	r.AsymmetricRiskSamples = s.asymmetricRiskSamples
	r.AsymmetricRiskMinMultiplier = s.asymmetricRiskMinMultiplier
	r.AsymmetricRiskMaxMultiplier = s.asymmetricRiskMaxMultiplier
	r.AsymmetricRiskReadySamples = s.asymmetricRiskReadySamples
	if s.asymmetricOscillationRisk != nil {
		r.AsymmetricRiskDirectionStrength = s.asymmetricOscillationRisk.Config.DirectionStrength
		r.AsymmetricRiskAsymmetryWeight = s.asymmetricOscillationRisk.Config.AsymmetryWeight
	}
	if s.asymmetricRiskSamples > 0 {
		r.AsymmetricRiskMeanMultiplier = s.asymmetricRiskMultiplierSum / float64(s.asymmetricRiskSamples)
	}
	if s.bocpd45Calibration != nil {
		r.BOCPD45Calibration = string(s.bocpd45Calibration.calibrator.method)
		r.BOCPD45MatureCalibrationSamples = s.bocpd45Calibration.matured
	}
	r.PostFillUtilityEvaluations = s.postFillUtilityEvaluations
	r.FastReservationEvaluations = s.fastReservationEvaluations
	r.FastReservationApplied = s.fastReservationApplied
	r.FastReservationReasons = s.fastReservationReasons
	r.FastTargetSwitchEvaluations = s.fastTargetSwitchEvaluations
	r.FastTargetSwitchApplied = s.fastTargetSwitchApplied
	r.FastTargetSwitchRetained = s.fastTargetSwitchRetained
	r.FastTargetSwitchReasons = s.fastTargetSwitchReasons
	r.FastTargetSwitchNetValueSumJPY = s.fastTargetSwitchNetValueSumJPY
	r.DynamicInventoryAimEvaluations = s.dynamicInventoryAimEvaluations
	r.DynamicInventoryAimPassed = s.dynamicInventoryAimPassed
	r.DynamicInventoryAimApplied = s.dynamicInventoryAimApplied
	r.DynamicInventoryAimReasons = s.dynamicInventoryAimReasons
	r.DynamicInventoryAimAccuracy = summarizeDynamicInventoryAimAccuracy(books, s.dynamicInventoryAimObservations, resultFrom)
	r.CausalRegimeTargetEvaluations = s.causalRegimeTargetEvaluations
	r.CausalRegimeTargetReady = s.causalRegimeTargetReady
	r.CausalRegimeTargetApplied = s.causalRegimeTargetApplied
	r.CausalRegimeTargetFullLong = s.causalRegimeTargetFullLong
	r.CausalRegimeTargetFullFlat = s.causalRegimeTargetFullFlat
	r.CausalRegimeTargetReasons = s.causalRegimeTargetReasons
	if s.causalRegimeTargetReady > 0 {
		r.CausalRegimeTargetMeanWeight = s.causalRegimeTargetWeightSum / float64(s.causalRegimeTargetReady)
		r.CausalRegimeTargetMeanDelta = s.causalRegimeTargetDeltaSum / float64(s.causalRegimeTargetReady)
	}
	r.RegimeEVSizingEvaluations = s.regimeEVSizingEvaluations
	r.RegimeEVSizingApplied = s.regimeEVSizingApplied
	r.RegimeEVSizingZeroScale = s.regimeEVSizingZeroScale
	r.RegimeEVSizingNotReady = s.regimeEVSizingNotReady
	r.RegimeEVSizingRegimeNotReady = s.regimeEVSizingRegimeNotReady
	r.RegimeEVSizingFastDriftFallback = s.regimeEVSizingFastDriftFallback
	r.RegimeEVSizingReasons = s.regimeEVSizingReasons
	if s.regimeEVSizingEvaluations > 0 {
		r.RegimeEVSizingMeanScale = s.regimeEVSizingScaleSum / float64(s.regimeEVSizingEvaluations)
		r.RegimeEVSizingMeanGrossValueJPY = s.regimeEVSizingGrossValueSum / float64(s.regimeEVSizingEvaluations)
		r.RegimeEVSizingMeanNetValueJPY = s.regimeEVSizingNetValueSum / float64(s.regimeEVSizingEvaluations)
		r.RegimeEVSizingMeanUtilityJPY = s.regimeEVSizingUtilitySum / float64(s.regimeEVSizingEvaluations)
		r.RegimeEVSizingMeanEffectiveSamples = s.regimeEVSizingEffectiveSamplesSum / float64(s.regimeEVSizingEvaluations)
		r.RegimeEVSizingMeanRegimeReliability = s.regimeEVSizingRegimeReliabilitySum / float64(s.regimeEVSizingEvaluations)
		r.RegimeEVSizingExpectedFeesJPY = s.regimeEVSizingExpectedFeesSum
	}
	if s.horizonConditionedUtility != nil {
		metrics := s.horizonConditionedUtility.metrics()
		r.HorizonUtilityEvaluations = metrics.Evaluations
		r.HorizonUtilityReady = metrics.Ready
		r.HorizonUtilityPositive = metrics.Positive
		r.HorizonUtilityZeroScale = metrics.ZeroScale
		r.HorizonUtilityAnchors = metrics.Anchors
		r.HorizonUtilityShortLabels = metrics.ShortLabels
		r.HorizonUtilityContinuationLabels = metrics.ContinuationLabels
		r.HorizonUtilityTouchedLabels = metrics.TouchedLabels
		r.HorizonUtilityMeanScale = metrics.MeanScale
		r.HorizonUtilityMeanEffectiveSamples = metrics.MeanEffectiveSamples
		r.HorizonUtilityMeanFillProbability = metrics.MeanFillProbability
	}
	r.EarlyStatisticalEvaluations = s.earlyStatisticalEvaluations
	r.EarlyStatisticalApplied = s.earlyStatisticalApplied
	r.PostFillUtilityApplied = s.postFillUtilityApplied
	r.EquityCurve = filterProductionEquityCurve(s.equityCurve, resultFrom)
	r.FillEvents = filterReplayFills(s.fillEvents, resultFrom)
	if s.relativeHoldRiskModel != nil {
		checkpoint := s.relativeHoldRiskModel.Checkpoint()
		r.relativeHoldRiskCheckpoint = &checkpoint
	}
	r.PostFillUtilityReasons = s.postFillUtilityReasons
	r.QuoteLifecycleEvaluations = s.quoteLifecycleEvaluations
	r.QuoteLifecycleActions = s.quoteLifecycleActions
	if s.quoteLifecycleHazard != nil {
		r.QuoteLifecycleHazardObservations = s.quoteLifecycleHazard.ObservationCount()
	}
	r.QuoteLifecycleHazardReadyReviews = s.quoteLifecycleHazardReadyReviews
	if s.quoteLifecycleEvaluations > 0 {
		r.QuoteLifecycleMeanIncrementalBps = s.quoteLifecycleIncrementalSumBps / float64(s.quoteLifecycleEvaluations)
	}
	r.JointQuoteEvaluations = s.jointQuoteEvaluations
	r.JointQuoteAccepted = s.jointQuoteAccepted
	r.JointQuoteApplied = s.jointQuoteApplied
	r.JointQuoteReasons = s.jointQuoteReasons
	r.JointQuoteDecisions = s.jointQuoteDecisions
	if len(s.horizonDiagnostics) > 0 {
		r.HorizonDiagnostics = make(
			map[string]productionReplayHorizonDiagnostic, len(s.horizonDiagnostics))
		for horizon, diagnostic := range s.horizonDiagnostics {
			copy := *diagnostic
			copy.finalize()
			r.HorizonDiagnostics[horizon] = copy
		}
	}
	if s.jointQuoteAccepted > 0 {
		r.AverageJointCapitalUtilization = s.jointCapitalUtilizationSum /
			float64(s.jointQuoteAccepted)
		r.AverageJointPairCapitalUtilization = s.jointPairCapitalUtilizationSum /
			float64(s.jointQuoteAccepted)
	}
	if s.jointCandidateCount > 0 {
		r.AverageJointCandidateUtilization = s.jointCandidateUtilizationSum /
			float64(s.jointCandidateCount)
	}
	r.MaximumJointExpectedPnLJPYHour = s.maximumJointExpectedPnLJPYHour
	r.MaximumJointLowerPnLJPYHour = s.maximumJointLowerPnLJPYHour
	r.MinimumJointPathEffectiveSamples = s.minimumJointPathEffectiveSamples
	r.DirectionalTargetOverrides = s.directionalTargetOverrides
	if s.directionalTargetOverrides > 0 {
		r.MeanDirectionalTargetBps = s.directionalTargetSumBps / float64(s.directionalTargetOverrides)
	}
	r.MaxPostFillIncrementalMeanBps = s.maxPostFillIncrementalMeanBps
	r.MaxPostFillIncrementalLowerBps = s.maxPostFillIncrementalLowerBps
	r.MacroActiveDecisions = s.macroActiveDecisions
	r.FastTargetActiveDecisions = s.fastTargetActiveDecisions
	if s.acquisitionDrawdownLimitSamples > 0 {
		r.MeanAcquisitionDrawdownLimitBps = s.acquisitionDrawdownLimitSumBps / float64(s.acquisitionDrawdownLimitSamples)
	}
	if hours > 0 {
		r.FillsPerHour = float64(s.fills) / hours
		r.FillsPerDay = 24 * r.FillsPerHour
		r.RoundTripsPerDay = 24 * float64(s.roundTrips) / hours
	}
	if s.books > 0 {
		r.QuoteUptimePct = 100 * float64(s.quoteActive) / float64(s.books)
	}
	if s.distanceSamples > 0 {
		r.AverageBidDistanceBps = s.bidDistanceSum / float64(s.distanceSamples)
		r.AverageAskDistanceBps = s.askDistanceSum / float64(s.distanceSamples)
	}
	if s.quoteLifeSamples > 0 {
		r.AverageQuoteLifeSeconds = s.quoteLifeSum / float64(s.quoteLifeSamples)
	}
	if s.featureChecks > 0 {
		r.HorizonTouchFeatureReadyPct = 100 * float64(s.featureReady) / float64(s.featureChecks)
	}
	r.MeanMarkout1mBps = meanFillMarkout(r.FillEvents, books, time.Minute)
	r.MeanMarkout5mBps = meanFillMarkout(r.FillEvents, books, 5*time.Minute)
	r.MeanMarkout10mBps = meanFillMarkout(r.FillEvents, books, 10*time.Minute)
	for _, day := range s.fillsByDay {
		r.DayResults = append(r.DayResults, *day)
	}
	sort.Slice(r.DayResults, func(i, j int) bool { return r.DayResults[i].Day < r.DayResults[j].Day })
	return r
}

func compareReplayPolicies(old, current productionReplayResult, calibrationPassed bool) string {
	if !calibrationPassed {
		return "calibration failed: do not use synthetic fill comparison to promote or tune live policy"
	}
	if current.FullFills == 0 {
		return "reject horizon-touch: no synthetic fills"
	}
	if current.MeanMarkout10mBps < old.MeanMarkout10mBps && current.FillsPerHour < old.FillsPerHour {
		return "do not promote from replay: lower fill rate and worse 10m markout"
	}
	if current.FillsPerHour >= .8*old.FillsPerHour && current.MeanMarkout10mBps >= old.MeanMarkout10mBps {
		return "horizon-touch passes provisional replay gate; retain live canary and collect private fills"
	}
	return "inconclusive: retain canary, do not loosen quotes until more private fills are available"
}

func compactBBO(values []bboSnapshot) []bboSnapshot {
	sort.SliceStable(values, func(i, j int) bool { return values[i].time.Before(values[j].time) })
	out := values[:0]
	for _, value := range values {
		if len(out) > 0 {
			last := out[len(out)-1]
			// Repeated identical BBO states carry no new price, depth, or
			// execution information. Drop them even when capture timestamps
			// differ; the next distinct state still preserves ordering/gaps.
			if value.bid == last.bid && value.ask == last.ask && value.bidSize == last.bidSize && value.askSize == last.askSize {
				continue
			}
		}
		out = append(out, value)
	}
	return out
}

func filterProductionEquityCurve(curve []productionEquityPoint, from time.Time) []productionEquityPoint {
	if from.IsZero() {
		return append([]productionEquityPoint(nil), curve...)
	}
	out := make([]productionEquityPoint, 0, len(curve))
	for _, point := range curve {
		if !point.At.Before(from) {
			out = append(out, point)
		}
	}
	return out
}

func filterReplayFills(fills []replayFill, from time.Time) []replayFill {
	if from.IsZero() {
		return append([]replayFill(nil), fills...)
	}
	out := make([]replayFill, 0, len(fills))
	for _, fill := range fills {
		if !fill.At.Before(from) {
			out = append(out, fill)
		}
	}
	return out
}

// compactBBOAtInterval keeps the last observable BBO in each fixed time
// bucket. It is a replay-only approximation: the simulator still activates a
// quote on the next retained BBO, while sub-bucket queue events are omitted.
// This bounds CrossingDecisionAtSideDistances calls for long studies without
// changing the live collector or the default exact replay (interval <= 0).
func compactBBOAtInterval(values []bboSnapshot, interval time.Duration) []bboSnapshot {
	if interval <= 0 || len(values) < 2 {
		return values
	}
	sort.SliceStable(values, func(i, j int) bool { return values[i].time.Before(values[j].time) })
	out := make([]bboSnapshot, 0, len(values))
	var bucket time.Time
	for _, value := range values {
		currentBucket := value.time.Truncate(interval)
		if bucket.IsZero() || !currentBucket.Equal(bucket) {
			out = append(out, value)
			bucket = currentBucket
			continue
		}
		out[len(out)-1] = value
	}
	return out
}
func compactTrades(values []tick) []tick {
	sort.SliceStable(values, func(i, j int) bool { return values[i].time.Before(values[j].time) })
	out := values[:0]
	seenIDs := make(map[uint64]struct{}, len(values))
	for _, value := range values {
		if value.id != 0 {
			if _, exists := seenIDs[value.id]; exists {
				continue
			}
			seenIDs[value.id] = struct{}{}
		} else if len(out) > 0 {
			last := out[len(out)-1]
			if last.id == 0 && value.time.Equal(last.time) && value.price == last.price && value.size == last.size && value.side == last.side {
				continue
			}
		}
		out = append(out, value)
	}
	return out
}
func filterBBO(values []bboSnapshot, from, to time.Time) []bboSnapshot {
	out := make([]bboSnapshot, 0, len(values))
	for _, v := range values {
		if !v.time.Before(from) && v.time.Before(to) {
			out = append(out, v)
		}
	}
	return out
}
func filterTicks(values []tick, from, to time.Time) []tick {
	out := make([]tick, 0, len(values))
	for _, v := range values {
		if !v.time.Before(from) && v.time.Before(to) {
			out = append(out, v)
		}
	}
	return out
}
func replayBookImbalance(book bboSnapshot) float64 {
	total := book.bidSize + book.askSize
	if total <= 0 {
		return 0
	}
	return math.Max(-1, math.Min(1, (book.bidSize-book.askSize)/total))
}
func replayDirection(snapshot gammacapture.ModelSnapshot) float64 {
	total := snapshot.LambdaUp + snapshot.LambdaDown
	if total <= 0 {
		return 0
	}
	return math.Max(-1, math.Min(1, (snapshot.LambdaUp-snapshot.LambdaDown)/total))
}
func replayNearFillSides(bid, ask productionReplayOrder, book bboSnapshot, plan gammacapture.MarketMakerQuotePlan, floor float64) (bool, bool) {
	if (!bid.active && !ask.active) || book.bid <= 0 || book.ask <= 0 {
		return false, false
	}
	// A one-sided target-restoring quote is valued against the opposite
	// completion price in the same newly evaluated plan.  That reference is not
	// treated as a resting order; it is used only to prove the retained side's
	// complete sequential cycle remains fee-safe.
	cycleBid, cycleAsk := bid.price, ask.price
	if !bid.active {
		cycleBid = plan.BidPrice
	}
	if !ask.active {
		cycleAsk = plan.AskPrice
	}
	if cycleBid <= 0 || cycleAsk <= cycleBid ||
		math.Log(cycleAsk/cycleBid)*10_000+1e-9 < floor {
		return false, false
	}
	retainBid, retainAsk := false, false
	if bid.active && plan.AllowBid && bid.price < book.ask {
		retainBid = math.Log(book.ask/bid.price)*10_000 <= plan.BidTouchDistanceBps+1e-9
	}
	if ask.active && plan.AllowAsk && ask.price > book.bid {
		retainAsk = math.Log(ask.price/book.bid)*10_000 <= plan.AskTouchDistanceBps+1e-9
	}
	return retainBid, retainAsk
}
func meanFillMarkout(fills []replayFill, books []bboSnapshot, horizon time.Duration) float64 {
	total := 0.0
	count := 0
	for _, fill := range fills {
		target := fill.At.Add(horizon)
		i := sort.Search(len(books), func(i int) bool { return !books[i].time.Before(target) })
		if i >= len(books) || books[i].time.Sub(target) > 2*time.Minute {
			continue
		}
		mid := (books[i].bid + books[i].ask) / 2
		sign := 1.0
		if fill.Side == types.SideTypeSell {
			sign = -1
		}
		total += sign * math.Log(mid/fill.Price) * 10000
		count++
	}
	if count == 0 {
		return 0
	}
	return total / float64(count)
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
