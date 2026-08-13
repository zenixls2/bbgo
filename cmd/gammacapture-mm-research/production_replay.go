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
	From, To                                     time.Time
	PairEquityJPY, StartingBase, QueueMultiplier float64
	CalibrationFrom, CalibrationTo               time.Time
	ActualBuyFills, ActualSellFills              int
	ReplayCacheDir                               string
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
	DisableConditionalExecution   bool
	ActivateJointDistanceQuantity bool
	JointDistanceCandidateCount   int
	EnableFastDrift               bool
	EnableBOCPD45Direction        bool
	BOCPD45Calibration            string
}

var activeProductionConfigOverrides productionConfigOverrides

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
	if o.DisableConditionalExecution {
		c.ConditionalExecution.Enabled = false
	}
	if o.ActivateJointDistanceQuantity {
		c.JointDistanceQuantity.Enabled = true
		c.JointDistanceQuantity.ShadowOnly = false
	}
	if o.JointDistanceCandidateCount > 0 {
		c.JointDistanceQuantity.CandidateCount = o.JointDistanceCandidateCount
	}
	if o.EnableFastDrift {
		c.FastDrift.Enabled = true
		c.FastDrift.ShadowOnly = false
	}
	return c
}

type replayPolicyMode string

const (
	replayLegacy           replayPolicyMode = "legacy-direction-fallback"
	replayHorizonTouch     replayPolicyMode = "horizon-touch"
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

type productionReplayResult struct {
	Mode                               replayPolicyMode `json:"mode"`
	From, To                           time.Time
	ActiveHours                        float64                         `json:"activeHours"`
	BBOEvents                          int                             `json:"bboEvents"`
	AggTradeEvents                     int                             `json:"aggTradeEvents"`
	DataGaps                           int                             `json:"dataGaps"`
	QueueMultiplier                    float64                         `json:"queueMultiplier"`
	BOCPD45Calibration                 string                          `json:"bocpd45Calibration,omitempty"`
	BOCPD45MatureCalibrationSamples    int                             `json:"bocpd45MatureCalibrationSamples,omitempty"`
	QuoteRefreshes                     int                             `json:"quoteRefreshes"`
	FullFills                          int                             `json:"fullFills"`
	BuyFills                           int                             `json:"buyFills"`
	SellFills                          int                             `json:"sellFills"`
	RoundTrips                         int                             `json:"roundTrips"`
	FillEvents                         []replayFill                    `json:"fillEvents,omitempty"`
	FastInventoryZoneDecisions         int                             `json:"fastInventoryZoneDecisions"`
	LongHorizonAdjustmentDecisions     int                             `json:"longHorizonAdjustmentDecisions"`
	FastReservationEvaluations         int                             `json:"fastReservationEvaluations"`
	FastReservationApplied             int                             `json:"fastReservationApplied"`
	FastReservationReasons             map[string]int                  `json:"fastReservationReasons,omitempty"`
	JointQuoteEvaluations              int                             `json:"jointQuoteEvaluations"`
	JointQuoteAccepted                 int                             `json:"jointQuoteAccepted"`
	JointQuoteApplied                  int                             `json:"jointQuoteApplied"`
	JointQuoteReasons                  map[string]int                  `json:"jointQuoteReasons,omitempty"`
	AverageJointCapitalUtilization     float64                         `json:"averageJointCapitalUtilization"`
	AverageJointPairCapitalUtilization float64                         `json:"averageJointPairCapitalUtilization"`
	AverageJointCandidateUtilization   float64                         `json:"averageJointCandidateUtilization"`
	MaximumJointExpectedPnLJPYHour     float64                         `json:"maximumJointExpectedPnLJPYHour"`
	MaximumJointLowerPnLJPYHour        float64                         `json:"maximumJointLowerPnLJPYHour"`
	MinimumJointPathEffectiveSamples   float64                         `json:"minimumJointPathEffectiveSamples"`
	JointQuoteDecisions                []productionReplayJointDecision `json:"jointQuoteDecisions,omitempty"`
	PostFillUtilityEvaluations         int                             `json:"postFillUtilityEvaluations"`
	PostFillUtilityApplied             int                             `json:"postFillUtilityApplied"`
	PostFillUtilityReasons             map[string]int                  `json:"postFillUtilityReasons,omitempty"`
	MaxPostFillIncrementalMeanBps      float64                         `json:"maxPostFillIncrementalMeanBps"`
	MaxPostFillIncrementalLowerBps     float64                         `json:"maxPostFillIncrementalLowerBps"`
	AcquisitionResets                  int                             `json:"acquisitionResets"`
	AcquisitionQuantity                float64                         `json:"acquisitionQuantity"`
	MacroActiveAttempts                int                             `json:"macroActiveAttempts"`
	MacroActiveFills                   int                             `json:"macroActiveFills"`
	MacroActiveQuantity                float64                         `json:"macroActiveQuantity"`
	MacroActiveDecisions               []productionReplayMacroDecision `json:"macroActiveDecisions,omitempty"`
	TakerFeesJPY                       float64                         `json:"takerFeesJPY"`
	AcquisitionEvaluations             int                             `json:"acquisitionEvaluations"`
	AcquisitionRejections              map[string]int                  `json:"acquisitionRejections,omitempty"`
	AcquisitionDrawdownLimitSamples    int                             `json:"acquisitionDrawdownLimitSamples"`
	MinimumAcquisitionDrawdownLimitBps float64                         `json:"minimumAcquisitionDrawdownLimitBps"`
	MeanAcquisitionDrawdownLimitBps    float64                         `json:"meanAcquisitionDrawdownLimitBps"`
	MaximumAcquisitionDrawdownLimitBps float64                         `json:"maximumAcquisitionDrawdownLimitBps"`
	MaxUpProbabilityLower              float64                         `json:"maxUpProbabilityLower"`
	MaxAcquisitionIOCValueBps          float64                         `json:"maxAcquisitionIOCValueBps"`
	MaxAcquisitionImprovementBps       float64                         `json:"maxAcquisitionImprovementBps"`
	FillsPerHour                       float64                         `json:"fillsPerHour"`
	FillsPerDay                        float64                         `json:"fillsPerDay"`
	RoundTripsPerDay                   float64                         `json:"roundTripsPerDay"`
	QuoteUptimePct                     float64                         `json:"quoteUptimePct"`
	AverageBidDistanceBps              float64                         `json:"averageBidDistanceBps"`
	AverageAskDistanceBps              float64                         `json:"averageAskDistanceBps"`
	AverageQuoteLifeSeconds            float64                         `json:"averageQuoteLifeSeconds"`
	HorizonTouchFeatureReadyPct        float64                         `json:"horizonTouchFeatureReadyPct"`
	MakerFeesJPY                       float64                         `json:"makerFeesJPY"`
	NetPnLJPY                          float64                         `json:"netPnLJPY"`
	MeanMarkout1mBps                   float64                         `json:"meanMarkout1mBps"`
	MeanMarkout5mBps                   float64                         `json:"meanMarkout5mBps"`
	MeanMarkout10mBps                  float64                         `json:"meanMarkout10mBps"`
	DayResults                         []productionReplayDay           `json:"dayResults"`
	EquityCurve                        []productionEquityPoint         `json:"equityCurve,omitempty"`
	StoppedEarly                       bool                            `json:"stoppedEarly"`
	StopAt                             time.Time                       `json:"stopAt,omitempty"`
	StopReason                         string                          `json:"stopReason,omitempty"`
	MaximumDrawdownPct                 float64                         `json:"maximumDrawdownPct"`
	Limitations                        []string                        `json:"limitations"`
}

type productionComparisonReport struct {
	Symbol                  string                   `json:"symbol"`
	CalibrationActualBuy    int                      `json:"calibrationActualBuyFills"`
	CalibrationActualSell   int                      `json:"calibrationActualSellFills"`
	SelectedQueueMultiplier float64                  `json:"selectedQueueMultiplier"`
	CalibrationPassed       bool                     `json:"calibrationPassed"`
	CalibrationAbsError     int                      `json:"calibrationAbsoluteSideError"`
	CalibrationCandidates   []productionReplayResult `json:"calibrationCandidates"`
	CalibrationLegacy       productionReplayResult   `json:"calibrationLegacy"`
	FullLegacy              productionReplayResult   `json:"fullLegacy"`
	FullHorizonTouch        productionReplayResult   `json:"fullHorizonTouch"`
	JournalLifecycle        *orderLifecycleReport    `json:"journalLifecycle,omitempty"`
	FullAcquisitionReset    productionReplayResult   `json:"fullAcquisitionReset"`
	Decision                string                   `json:"decision"`
	ReplayCacheHit          bool                     `json:"replayCacheHit"`
}

type productionReplayOrder struct {
	active                                 bool
	eligible                               bool
	side                                   types.SideType
	price, quantity, remaining, queueAhead float64
	filledQuantity, accumulatedFee         float64
	placedAt                               time.Time
}
type replayFill struct {
	At             time.Time      `json:"at"`
	Side           types.SideType `json:"side"`
	Price          float64        `json:"price"`
	Quantity       float64        `json:"quantity"`
	NotionalJPY    float64        `json:"notionalJPY"`
	FeeJPY         float64        `json:"feeJPY"`
	InventoryAfter float64        `json:"inventoryAfter"`
	QuoteAfter     float64        `json:"quoteAfter"`
}

type productionReplayMacroIOC struct {
	active      bool
	direction   int
	quantity    float64
	worstPrice  float64
	closedBarAt time.Time
}

type productionReplayJointDecision struct {
	At                           time.Time     `json:"at"`
	Horizon                      time.Duration `json:"horizon"`
	HorizonCrossingScoreBpsHour  float64       `json:"horizonCrossingScoreBpsHour"`
	HorizonSelectionScoreBpsHour float64       `json:"horizonSelectionScoreBpsHour"`
	HorizonMarginalBuyEvaluated  bool          `json:"horizonMarginalBuyEvaluated"`
	HorizonMarginalBuyNotional   float64       `json:"horizonMarginalBuyNotionalJPY"`
	HorizonMarginalBuyCE         float64       `json:"horizonMarginalBuyCEJPY"`
	HorizonMarginalBuyUtility    float64       `json:"horizonMarginalBuyUtilityBpsHour"`
	Reason                       string        `json:"reason"`
	AuthoritativeRejection       bool          `json:"authoritativeRejection"`
	SideSafeFallback             bool          `json:"sideSafeFallback"`
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

type productionReplayState struct {
	cfg                                                                            gammacapture.MarketMakerConfig
	artifact                                                                       *gammacapture.HorizonTouchArtifact
	mode                                                                           replayPolicyMode
	symbol                                                                         string
	queueFactor                                                                    float64
	barrierWidth                                                                   float64
	engine                                                                         *gammacapture.CrossingEngine
	slowModel                                                                      *gammacapture.IntensityModel
	executableCrossingModel                                                        *gammacapture.ExecutableCrossingModel
	fastModels                                                                     map[time.Duration]*gammacapture.IntensityModel
	fastEvidenceModels                                                             map[time.Duration]*gammacapture.FastEvidenceModel
	bocpd45Direction                                                               bocpd45DirectionModel
	bocpd45DirectionEnabled                                                        bool
	bocpd45Calibration                                                             *bocpd45PrequentialCalibration
	horizonModel                                                                   gammacapture.MarketMakerHorizonModel
	macroInventoryModel                                                            gammacapture.MacroInventoryModel
	macroInventoryState                                                            gammacapture.MacroInventoryState
	inventory, quote, initialEquity, initialInventory, initialQuote                float64
	inventoryBand                                                                  gammacapture.InventoryBand
	sideAllocationBias, sideDistanceBias                                           float64
	sideAllocationReady, sideDistanceReady                                         bool
	useProbabilityProjection                                                       bool
	projectionTargetAnchor                                                         float64
	quotedTargetRatio                                                              float64
	quotedFastReservationBps                                                       float64
	quotedTargetSet                                                                bool
	lastMakerFill                                                                  gammacapture.MakerPostFillState
	postFillUtilityReasons                                                         map[string]int
	maxPostFillIncrementalMeanBps, maxPostFillIncrementalLowerBps                  float64
	fillRefreshPending                                                             bool
	postFillUtilityEvaluations, postFillUtilityApplied                             int
	fastReservationEvaluations, fastReservationApplied                             int
	fastReservationReasons                                                         map[string]int
	jointQuoteEvaluations, jointQuoteAccepted, jointQuoteApplied                   int
	jointQuoteReasons                                                              map[string]int
	jointCapitalUtilizationSum, jointPairCapitalUtilizationSum                     float64
	maximumJointLowerPnLJPYHour                                                    float64
	jointCandidateCount, jointBestObserved                                         int
	jointCandidateUtilizationSum, maximumJointExpectedPnLJPYHour                   float64
	minimumJointPathEffectiveSamples                                               float64
	jointQuoteDecisions                                                            []productionReplayJointDecision
	bidOrder, askOrder                                                             productionReplayOrder
	pendingMacroIOC                                                                productionReplayMacroIOC
	lastQuoteAt, windowEndsAt                                                      time.Time
	lastBestBid, lastBestAsk, lastMid, lastImbalance                               float64
	acquisitionCooldownUntil                                                       time.Time
	acquisitionDeficitSince                                                        time.Time
	acquisitionDeficitAnchorMid                                                    float64
	acquisitionResets                                                              int
	macroActiveAttempts, macroActiveFills                                          int
	macroActiveQuantity                                                            float64
	lastMacroActiveDecisionBarAt                                                   time.Time
	macroActiveDecisions                                                           []productionReplayMacroDecision
	books, trades, gaps, refreshes, quoteActive                                    int
	fastInventoryZoneDecisions, longHorizonAdjustmentDecisions                     int
	fills, buys, sells, roundTrips, unmatchedBuys, unmatchedSells                  int
	featureChecks, featureReady                                                    int
	tradingFrom                                                                    time.Time
	acquisitionEvaluations                                                         int
	acquisitionRejections                                                          map[string]int
	acquisitionDrawdownLimitSamples                                                int
	minAcquisitionDrawdownLimitBps, acquisitionDrawdownLimitSumBps                 float64
	maxAcquisitionDrawdownLimitBps                                                 float64
	maxUpProbabilityLower, maxAcquisitionIOCValueBps, maxAcquisitionImprovementBps float64
	lastDecisionSecond                                                             time.Time
	volatilityMinute                                                               time.Time
	cachedSideVolatility                                                           gammacapture.MarketMakerSideVolatilityEstimate
	featureMinute                                                                  time.Time
	cachedFeatures                                                                 []float64
	cachedFeaturesReady                                                            bool
	fees, bidDistanceSum, askDistanceSum                                           float64
	distanceSamples                                                                int
	acquisitionQuantity, takerFees                                                 float64
	quoteLifeSum                                                                   float64
	quoteLifeSamples                                                               int
	activeDuration                                                                 time.Duration
	fillsByDay                                                                     map[string]*productionReplayDay
	fillEvents                                                                     []replayFill
	equityCurve                                                                    []productionEquityPoint
	maxDrawdownStopPct, equityPeak, maximumDrawdownPct                             float64
	stopped                                                                        bool
	stopAt                                                                         time.Time
	stopReason                                                                     string
}

func runProductionComparison(in productionComparisonInput) {
	barrier, intensity, cfg := loadProductionConfig(in.ConfigPath, in.Symbol)
	artifact, err := gammacapture.LoadHorizonTouchArtifact(in.ModelPath, in.Symbol, cfg.HorizonTouchModel.MinimumBrierImprovementPct)
	if err != nil {
		fatalf("load production horizon-touch model: %v", err)
	}
	books, trades, cacheHit := loadExactReplayDataset(in.DataPath, in.Symbol, in.From, in.To, replayConfigFingerprint(in.ConfigPath), in.ReplayCacheDir)
	books = compactBBO(books)
	trades = compactTrades(trades)
	if len(books) < 2 || len(trades) == 0 {
		fatalf("insufficient production replay events: bbo=%d trades=%d", len(books), len(trades))
	}
	warmFrom := in.CalibrationFrom.Add(-time.Duration(cfg.HorizonLookback) - time.Duration(cfg.MaxTradingWindow) - time.Hour)
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
	for _, queue := range queueCandidates {
		r := simulateProductionPolicy(calBooks, calTrades, cfg, barrier, intensity, nil, replayLegacy, in.Symbol, in.PairEquityJPY, in.StartingBase, queue, in.CalibrationFrom)
		candidates = append(candidates, r)
		score := math.Abs(float64(r.BuyFills-actualBuyFills)) + math.Abs(float64(r.SellFills-actualSellFills)) + .25*math.Abs(float64(r.FullFills-actualBuyFills-actualSellFills))
		if score < bestScore {
			selected, selectedIndex, bestScore = queue, len(candidates)-1, score
		}
	}
	calLegacy := candidates[selectedIndex]
	old := simulateProductionPolicy(books, trades, cfg, barrier, intensity, nil, replayLegacy, in.Symbol, in.PairEquityJPY, in.StartingBase, selected, in.From)
	current := simulateProductionPolicy(books, trades, cfg, barrier, intensity, artifact, replayHorizonTouch, in.Symbol, in.PairEquityJPY, in.StartingBase, selected, in.From)
	acquisition := simulateProductionPolicy(books, trades, cfg, barrier, intensity, nil, replayAcquisitionReset, in.Symbol, in.PairEquityJPY, in.StartingBase, selected, in.From)
	calibrationError := absInt(calLegacy.BuyFills-actualBuyFills) + absInt(calLegacy.SellFills-actualSellFills)
	calibrationPassed := calibrationError <= 1
	if lifecycle != nil && !lifecycle.CalibrationPassed {
		calibrationPassed = false
	}
	report := productionComparisonReport{Symbol: in.Symbol, CalibrationActualBuy: actualBuyFills, CalibrationActualSell: actualSellFills, SelectedQueueMultiplier: selected, CalibrationPassed: calibrationPassed, CalibrationAbsError: calibrationError, CalibrationCandidates: candidates, CalibrationLegacy: calLegacy, FullLegacy: old, FullHorizonTouch: current, FullAcquisitionReset: acquisition, JournalLifecycle: lifecycle, Decision: compareReplayPolicies(old, current, calibrationPassed), ReplayCacheHit: cacheHit}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fatalf("encode production comparison: %v", err)
	}
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

func newProductionReplayState(cfg gammacapture.MarketMakerConfig, barrier gammacapture.BarrierConfig, intensity gammacapture.IntensityConfig, artifact *gammacapture.HorizonTouchArtifact, mode replayPolicyMode, symbol string, startQuote, startBase, queue float64, tradingFrom time.Time, useProbabilityProjection bool) *productionReplayState {
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
	state := &productionReplayState{
		cfg: cfg, artifact: artifact, mode: mode, symbol: symbol, queueFactor: queue, barrierWidth: barrier.Width, useProbabilityProjection: useProbabilityProjection,
		engine:                  gammacapture.NewCrossingEngine(barrier.Width, time.Duration(barrier.MinDwell), barrier.MaxCrossingsPerEvent),
		slowModel:               gammacapture.NewIntensityModel(intensity),
		executableCrossingModel: gammacapture.NewExecutableCrossingModel(symbol, barrier, intensity),
		fastModels:              fastModels, fastEvidenceModels: fastEvidenceModels,
		bocpd45DirectionEnabled: cfg.BOCPD45.Enabled || activeProductionConfigOverrides.EnableBOCPD45Direction,
		postFillUtilityReasons:  make(map[string]int),
		inventory:               startBase, quote: startQuote, initialEquity: startQuote,
		initialInventory: startBase, initialQuote: startQuote, tradingFrom: tradingFrom,
		fillsByDay:            make(map[string]*productionReplayDay),
		acquisitionRejections: make(map[string]int),
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

func simulateProductionPolicyWithQuantityProjection(books []bboSnapshot, trades []tick, cfg gammacapture.MarketMakerConfig, barrier gammacapture.BarrierConfig, intensity gammacapture.IntensityConfig, artifact *gammacapture.HorizonTouchArtifact, mode replayPolicyMode, symbol string, pairEquity, startBase, queue float64, tradingFrom time.Time, useProbabilityProjection bool, maxDrawdownStopPct float64) productionReplayResult {
	if len(books) < 2 {
		return productionReplayResult{Mode: mode}
	}
	startIndex := sort.Search(len(books), func(i int) bool { return !books[i].time.Before(tradingFrom) })
	if startIndex >= len(books) {
		return productionReplayResult{Mode: mode}
	}
	startMid := (books[startIndex].bid + books[startIndex].ask) / 2
	startQuote := math.Max(0, pairEquity-startBase*startMid)
	s := newProductionReplayState(cfg, barrier, intensity, artifact, mode, symbol, startQuote, startBase, queue, tradingFrom, useProbabilityProjection)
	s.initialEquity = startQuote + startBase*startMid
	s.maxDrawdownStopPct = math.Max(0, maxDrawdownStopPct)
	s.equityPeak = s.initialEquity
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
				if !book.time.Before(tradingFrom) {
					s.gaps++
				}
			} else if !previous.IsZero() && !book.time.Before(tradingFrom) {
				activeStart := previous
				if activeStart.Before(tradingFrom) {
					activeStart = tradingFrom
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
	// arrived before the next observable book. Activate it at BBO[t+1]; only
	// subsequent public trades may consume its modeled queue.
	s.activatePendingQuotesOnNextBBO()
	macroIOCFilled := s.executePendingMacroIOC(book)
	mid := (book.bid + book.ask) / 2
	imbalance := replayBookImbalance(book)
	ticker := types.BookTicker{Symbol: s.symbol, Buy: fixedpoint.NewFromFloat(book.bid), Sell: fixedpoint.NewFromFloat(book.ask), BuySize: fixedpoint.NewFromFloat(book.bidSize), SellSize: fixedpoint.NewFromFloat(book.askSize)}
	for _, evidenceModel := range s.fastEvidenceModels {
		evidenceModel.ObserveBBO(book.time, ticker)
	}
	micro := mid
	if book.bidSize+book.askSize > 0 {
		micro = (book.ask*book.bidSize + book.bid*book.askSize) / (book.bidSize + book.askSize)
	}
	s.slowModel.Observe(book.time, gap)
	for _, fastModel := range s.fastModels {
		fastModel.Observe(book.time, gap)
	}
	if gap {
		s.engine.Reset(micro)
	} else {
		events := s.engine.Update(s.symbol, fixedpoint.NewFromFloat(micro), book.time, book.time, 0)
		for _, event := range events {
			s.slowModel.Update(event)
			for _, fastModel := range s.fastModels {
				fastModel.Update(event)
			}
		}
	}
	s.horizonModel.ObserveBookWithGap(book.time, book.bid, book.ask, s.cfg, gap)
	if s.bocpd45DirectionEnabled {
		s.bocpd45Direction.observe(book.time, book.bid, book.ask, gap)
		if s.bocpd45Calibration != nil {
			s.bocpd45Calibration.observe(book, s.bocpd45Direction.snapshot(), gap, false)
		}
	}
	s.macroInventoryModel.ObserveBBO(book.time, mid, book.bid, book.ask, gap, s.cfg.MacroInventory)
	s.executableCrossingModel.Observe(book.time, book.bid, book.ask, gap)
	if s.cfg.FastDrift.Enabled {
		for _, window := range s.cfg.FastModelWindows() {
			model := s.fastModels[window]
			if model == nil {
				continue
			}
			s.horizonModel.ObserveFastDrift(
				book.time, book.bid, book.ask, window, time.Duration(s.cfg.HorizonLookback),
				gammacapture.FastDriftFeatures{
					Direction:     gammacapture.RawFastDirection(model.Snapshot(book.time)),
					BookImbalance: imbalance,
				}, gap)
		}
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
	slow := s.slowModel.Snapshot(book.time)
	preSelectionPairEquity := s.quote + s.inventory*mid
	selectedDecision := s.horizonModel.UpdateForBookAdaptiveVolatilityWithMarginalBuy(
		book.time, s.cfg, book.bid, book.ask,
		gammacapture.FastHorizonMarginalBuyInput{
			CurrentInventoryNotionalJPY: s.inventory * mid,
			TargetInventoryNotionalJPY:  s.cfg.InventoryCapitalTargetRatio * preSelectionPairEquity,
			PairEquityJPY:               preSelectionPairEquity,
			MarginalBuyNotionalJPY:      100,
			AvailableBuyCapitalJPY:      s.quote,
			RiskAversion:                s.cfg.MacroInventory.RiskAversion,
			ConfidenceZScore:            s.cfg.InventoryRiskZScore,
		})
	selectedHorizon := time.Duration(selectedDecision.HorizonSeconds) * time.Second
	if selectedHorizon <= 0 {
		selectedHorizon = time.Duration(s.cfg.MinTradingWindow)
	}
	adaptiveFast := gammacapture.SelectAdaptiveFastSnapshotForWindow(
		book.time, s.fastModels, s.fastEvidenceModels, selectedHorizon)
	fast := adaptiveFast.Model
	evidence := adaptiveFast.Evidence
	fastInference := gammacapture.InferFastCrossing(adaptiveFast.Window, fast, evidence, slow)
	directionCoverage := gammacapture.FastEvidenceCoverage(
		evidence, s.cfg.FastEvidenceMinTrades, s.cfg.FastEvidenceMinBBOUpdates)
	direction := fastInference.Direction * directionCoverage
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
		}
	}
	fastDrift := s.horizonModel.FastDriftDecision(
		adaptiveFast.Window,
		gammacapture.FastDriftFeatures{
			Direction:     gammacapture.RawFastDirection(fast),
			BookImbalance: imbalance,
		})
	volumeSignal := evidence.VolumeBalance.Signal
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
	cfg := s.cfg
	fastBandPreview := cfg.DynamicInventoryBandWithCapital(
		mid, effectiveVolBps, horizon, pairEquity)
	effectiveTargetRatio := cfg.InventoryCapitalTargetRatio
	baselineTargetRatio := effectiveTargetRatio
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
	fastBuyHoldingRiskHorizon := gammacapture.FastReservationRiskHorizon(
		horizon, macroDecision.NoTrade.ForecastObservation)
	fastReservationConfidenceZ := cfg.InventoryRiskZScore
	fastReservation := macroDecision.NoTrade.FastReservation(
		fastBuyHoldingRiskHorizon, fastReservationConfidenceZ)
	s.recordEquity(book.time, mid, effectiveTargetRatio, reversalDirection, earlyReversal, reversalApplied)
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
	if inventoryControl.FastTradingZone {
		s.fastInventoryZoneDecisions++
	} else if inventoryControl.LongHorizonAdjustment {
		s.longHorizonAdjustmentDecisions++
	}
	if pairEquity > 0 {
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
	if s.fillRefreshPending && !s.lastMakerFill.At.IsZero() {
		utility := cfg.ApplyPostFillUtility(&s.horizonModel, gammacapture.PostFillUtilityInput{
			Now: book.time, Fill: s.lastMakerFill, Plan: plan,
			BestBid: book.bid, BestAsk: book.ask, Mid: mid, Horizon: horizon,
			InventoryBase: s.inventory, InventoryTargetBase: band.Target,
			PairEquityJPY: pairEquity, ExpectedFillNotionalJPY: 100,
			VolatilityBpsPerSqrtSec: effectiveVolBps,
			RiskAversion:            cfg.MacroInventory.RiskAversion,
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
			cfg.MacroInventory.RiskAversion)
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

	s.distanceSamples++
	s.bidDistanceSum += plan.BidDistanceBps
	s.askDistanceSum += plan.AskDistanceBps
	minRefresh, maxRefresh := cfg.RefreshIntervals(plan.HalfSpreadBps, effectiveVolBps)
	minRefresh, _ = gammacapture.BoundRefreshIntervals(minRefresh, maxRefresh, orderReviewDuration)
	elapsed := book.time.Sub(s.lastQuoteAt)
	quoteCrossed := (s.bidOrder.active && s.bidOrder.price >= book.ask) || (s.askOrder.active && s.askOrder.price <= book.bid)
	missingSide := (plan.AllowBid && !s.bidOrder.active) || (plan.AllowAsk && !s.askOrder.active)
	sideMismatch := (s.bidOrder.active != plan.AllowBid) || (s.askOrder.active != plan.AllowAsk)
	windowExpired := s.windowEndsAt.IsZero() || !book.time.Before(s.windowEndsAt)
	statisticalRealignment := false
	if (elapsed >= minRefresh || windowExpired) && s.bidOrder.active && s.askOrder.active &&
		decision.HasSufficientCrossings(cfg.HorizonMinSamples) {
		activeHorizon := s.windowEndsAt.Sub(s.lastQuoteAt)
		if activeHorizon <= 0 {
			activeHorizon = horizon
		}
		activeBuyDistance, activeSellDistance, activeGrossEdge := gammacapture.MakerTouchDistances(
			book.bid, book.ask, s.bidOrder.price, s.askOrder.price)
		activeDecision := s.horizonModel.CrossingDecisionAtSideDistances(
			book.time, cfg, activeHorizon, activeBuyDistance, activeSellDistance, activeGrossEdge)
		statisticalRealignment, _, _ = gammacapture.MakerQuoteStatisticalRealignment(
			decision, activeDecision, cfg.InventoryRiskZScore)
	}
	currentRiskyWeight := 0.0
	if pairEquity > 0 {
		currentRiskyWeight = s.inventory * mid / pairEquity
	}
	macroTargetRealignment := s.quotedTargetSet && gammacapture.InventoryTargetRealignmentRequired(
		effectiveTargetRatio, s.quotedTargetRatio, currentRiskyWeight, pairEquity, 100)
	reservationTargetSideActive := (appliedFastReservationBps > 0 && s.bidOrder.active) ||
		(appliedFastReservationBps < 0 && s.askOrder.active)
	reservationRiskRealignment := fastReservationUtility.Applied && reservationTargetSideActive &&
		gammacapture.FastReservationRealignmentRequired(
			appliedFastReservationBps, s.quotedFastReservationBps, 1e-9)
	retainBid, retainAsk := false, false
	cleanWindowExpiry := windowExpired && !quoteCrossed && !missingSide && !sideMismatch &&
		!statisticalRealignment && !macroTargetRealignment && !reservationRiskRealignment && !macroIOCFilled
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
	shouldRefresh := s.lastQuoteAt.IsZero() || macroIOCFilled || s.fillRefreshPending
	if !shouldRefresh && elapsed >= minRefresh {
		hardTransition := quoteCrossed || missingSide || sideMismatch || statisticalRealignment || macroTargetRealignment || reservationRiskRealignment
		ordinaryReprice := windowExpired
		shouldRefresh = hardTransition || ordinaryReprice
	}
	if !shouldRefresh {
		if s.bidOrder.active || s.askOrder.active {
			s.quoteActive++
		}
		return
	}
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
	projectionTargetBase := band.Target
	if (activeProductionReplayPosteriorBaseTarget || cfg.PosteriorInventoryTarget) && !plan.FastDriftApplied {
		buyDistance, sellDistance, _ := gammacapture.MakerTouchDistances(
			book.bid, book.ask, plan.BidPrice, plan.AskPrice)
		pathStats := s.horizonModel.JointPathPayoffStatistics(
			book.time, cfg, horizon, buyDistance, sellDistance)
		posteriorTarget := gammacapture.PosteriorExpectedInventoryTarget(
			s.initialInventory, band.Target, hardBand.MinInventory, hardBand.MaxInventory, pathStats)
		projectionTargetBase = posteriorTarget.TargetBase
	}
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
	if s.useProbabilityProjection {
		projection = gammacapture.ProbabilityCenteredQuoteNotionals(projectionInput)
		if cfg.JointDistanceQuantity.Enabled {
			jointProjectionInput := projectionInput
			jointProjectionInput.MaxBuyNotionalJPY = jointMaxBuyNotionalJPY
			jointProjectionInput.MaxSellNotionalJPY = jointMaxSellNotionalJPY
			joint := gammacapture.OptimizeUnifiedFastQuantity(
				&s.horizonModel, cfg, gammacapture.JointDistanceQuantityInput{
					Now: book.time, Horizon: horizon,
					BestBid: book.bid, BestAsk: book.ask, MidPrice: mid,
					BasePlan: plan, Projection: jointProjectionInput,
					FastDirection:    jointFastDirection,
					ConfidenceZScore: cfg.InventoryRiskZScore,
					PairEquityJPY:    pairEquity,
					RiskAversion:     cfg.MacroInventory.RiskAversion,
				}, projectionInput)
			s.jointQuoteEvaluations++
			if s.jointQuoteReasons == nil {
				s.jointQuoteReasons = make(map[string]int)
			}
			s.jointQuoteReasons[joint.Reason]++
			s.jointQuoteDecisions = append(s.jointQuoteDecisions,
				productionReplayJointDecision{
					At: book.time, Horizon: horizon,
					HorizonCrossingScoreBpsHour:  selectedDecision.ScoreBpsPerHour,
					HorizonSelectionScoreBpsHour: selectedDecision.SelectionScoreBpsPerHour,
					HorizonMarginalBuyEvaluated:  selectedDecision.MarginalBuyEvaluated,
					HorizonMarginalBuyNotional:   selectedDecision.MarginalBuyNotionalJPY,
					HorizonMarginalBuyCE:         selectedDecision.MarginalBuyCertaintyEquivalentJPY,
					HorizonMarginalBuyUtility:    selectedDecision.MarginalBuyUtilityBpsPerHour,
					Reason:                       joint.Reason,
					AuthoritativeRejection:       joint.AuthoritativeRejection,
					SideSafeFallback:             joint.SideSafeFallback,
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
			}
		}
	}
	projectionUsed := projection.Enabled
	buyNotional, sellQty := 0.0, 0.0
	if projectionUsed {
		buyNotional = math.Min(preCancelQuote, projection.BuyNotionalJPY*plan.BidPrice/mid)
		sellQty = math.Min(preCancelBase, projection.SellNotionalJPY/mid)
	} else {
		hardBuyNotional := math.Max(0, hardBand.MaxInventory-s.inventory) * plan.BidPrice
		buyCapacity := replayCappedInventoryCapacity(riskUtilizationSizing.BuyNotionalCapJPY*plan.BidPrice/mid, hardBuyNotional, 100)
		buyNotional = math.Min(preCancelQuote, math.Min(notionals.Buy, buyCapacity))
		hardSellQuantity := math.Max(0, s.inventory-hardBand.MinInventory)
		minimumSellQuantity := 100 / plan.AskPrice
		sellCapacity := replayCappedInventoryCapacity(riskUtilizationSizing.SellNotionalCapJPY/mid, hardSellQuantity, minimumSellQuantity)
		sellQty = math.Min(preCancelBase, math.Min(notionals.Sell/plan.AskPrice, sellCapacity))
	}
	if plan.AllowBid && !retainBid && buyNotional >= 100 {
		quantity := buyNotional / plan.BidPrice
		s.bidOrder = productionReplayOrder{active: true, side: types.SideTypeBuy,
			price: plan.BidPrice, quantity: quantity, remaining: quantity,
			queueAhead: book.bidSize * s.queueFactor, placedAt: book.time}
	}
	if plan.AllowAsk && !retainAsk && sellQty*plan.AskPrice >= 100 {
		s.askOrder = productionReplayOrder{active: true, side: types.SideTypeSell,
			price: plan.AskPrice, quantity: sellQty, remaining: sellQty,
			queueAhead: book.askSize * s.queueFactor, placedAt: book.time}
	}
	if s.bidOrder.active || s.askOrder.active {
		s.refreshes++
		s.lastQuoteAt = book.time
		s.windowEndsAt = book.time.Add(orderReviewDuration)
		s.lastBestBid, s.lastBestAsk, s.lastMid, s.lastImbalance = book.bid, book.ask, mid, imbalance
		s.quotedTargetRatio = effectiveTargetRatio
		s.quotedTargetSet = true
		s.quotedFastReservationBps = appliedFastReservationBps
		s.quoteActive++
		s.fillRefreshPending = false
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
	for _, evidenceModel := range s.fastEvidenceModels {
		evidenceModel.ObserveTrade(trade.time, observed)
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
	s.fillRefreshPending = true

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
	s.fillEvents = append(s.fillEvents, replayFill{
		At: trade.time, Side: order.side, Price: order.price,
		Quantity:       order.filledQuantity,
		NotionalJPY:    order.filledQuantity * order.price,
		FeeJPY:         order.accumulatedFee,
		InventoryAfter: s.inventory,
		QuoteAfter:     s.quote,
	})
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
	evalBooks := filterBBO(books, s.tradingFrom, resultTo.Add(time.Nanosecond))
	if len(evalBooks) == 0 {
		return productionReplayResult{Mode: s.mode}
	}
	books = evalBooks
	lastMid := (books[len(books)-1].bid + books[len(books)-1].ask) / 2
	hours := s.activeDuration.Hours()
	r := productionReplayResult{Mode: s.mode, From: books[0].time, To: books[len(books)-1].time, ActiveHours: hours, BBOEvents: s.books, AggTradeEvents: s.trades, DataGaps: s.gaps, QueueMultiplier: s.queueFactor, QuoteRefreshes: s.refreshes, FullFills: s.fills, BuyFills: s.buys, SellFills: s.sells, RoundTrips: s.roundTrips, FastInventoryZoneDecisions: s.fastInventoryZoneDecisions, LongHorizonAdjustmentDecisions: s.longHorizonAdjustmentDecisions, AcquisitionResets: s.acquisitionResets, AcquisitionQuantity: s.acquisitionQuantity, MacroActiveAttempts: s.macroActiveAttempts, MacroActiveFills: s.macroActiveFills, MacroActiveQuantity: s.macroActiveQuantity, AcquisitionEvaluations: s.acquisitionEvaluations, AcquisitionRejections: s.acquisitionRejections, AcquisitionDrawdownLimitSamples: s.acquisitionDrawdownLimitSamples, MinimumAcquisitionDrawdownLimitBps: s.minAcquisitionDrawdownLimitBps, MaximumAcquisitionDrawdownLimitBps: s.maxAcquisitionDrawdownLimitBps, MaxUpProbabilityLower: s.maxUpProbabilityLower, MaxAcquisitionIOCValueBps: s.maxAcquisitionIOCValueBps, MaxAcquisitionImprovementBps: s.maxAcquisitionImprovementBps, MakerFeesJPY: s.fees - s.takerFees, TakerFeesJPY: s.takerFees, NetPnLJPY: s.quote + s.inventory*lastMid - s.fees - s.initialEquity, StoppedEarly: s.stopped, StopAt: s.stopAt, StopReason: s.stopReason, MaximumDrawdownPct: s.maximumDrawdownPct, Limitations: []string{"no level-2 depth or exchange queue priority", "orders decided at BBO[t] execute no earlier than BBO[t+1]", "queue multiplier is calibrated to aggregate confirmed fills, not per-order queue position", "public aggregate trades cannot identify our private execution"}}
	if s.bocpd45Calibration != nil {
		r.BOCPD45Calibration = string(s.bocpd45Calibration.calibrator.method)
		r.BOCPD45MatureCalibrationSamples = s.bocpd45Calibration.matured
	}
	r.PostFillUtilityEvaluations = s.postFillUtilityEvaluations
	r.FastReservationEvaluations = s.fastReservationEvaluations
	r.FastReservationApplied = s.fastReservationApplied
	r.FastReservationReasons = s.fastReservationReasons
	r.PostFillUtilityApplied = s.postFillUtilityApplied
	r.EquityCurve = s.equityCurve
	r.FillEvents = append([]replayFill(nil), s.fillEvents...)
	r.PostFillUtilityReasons = s.postFillUtilityReasons
	r.JointQuoteEvaluations = s.jointQuoteEvaluations
	r.JointQuoteAccepted = s.jointQuoteAccepted
	r.JointQuoteApplied = s.jointQuoteApplied
	r.JointQuoteReasons = s.jointQuoteReasons
	r.JointQuoteDecisions = s.jointQuoteDecisions
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
	r.MaxPostFillIncrementalMeanBps = s.maxPostFillIncrementalMeanBps
	r.MaxPostFillIncrementalLowerBps = s.maxPostFillIncrementalLowerBps
	r.MacroActiveDecisions = s.macroActiveDecisions
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
	r.MeanMarkout1mBps = meanFillMarkout(s.fillEvents, books, time.Minute)
	r.MeanMarkout5mBps = meanFillMarkout(s.fillEvents, books, 5*time.Minute)
	r.MeanMarkout10mBps = meanFillMarkout(s.fillEvents, books, 10*time.Minute)
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
	if !bid.active || !ask.active || bid.price <= 0 || ask.price <= 0 || book.bid <= 0 || book.ask <= 0 {
		return false, false
	}
	if math.Log(ask.price/bid.price)*10_000+1e-9 < floor {
		return false, false
	}
	retainBid, retainAsk := false, false
	if plan.AllowBid && bid.price < book.ask {
		retainBid = math.Log(book.ask/bid.price)*10_000 <= plan.BidTouchDistanceBps+1e-9
	}
	if plan.AllowAsk && ask.price > book.bid {
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
