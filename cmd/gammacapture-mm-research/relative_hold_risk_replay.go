package main

import (
	"encoding/json"
	"math"
	"os"
	"sort"
	"time"

	"github.com/c9s/bbgo/pkg/strategy/gammacapture"
)

// relativeHoldRiskReplayInput is deliberately a study-only input. It runs a
// causal production shadow preload, then scores the requested interval with
// the delayed RelativeHoldRiskModel. The integrated arm reads one scalar only
// after scoreFrom; no live order, inventory, or service state is changed.
type relativeHoldRiskReplayInput struct {
	ConfigPath, DataPath, Symbol string
	From, To                     time.Time
	PreloadFrom                  time.Time
	PairEquityJPY, StartingBase  float64
	QueueMultiplier              float64
	ReplayCacheDir               string
	BBOInterval                  time.Duration
	Horizon                      time.Duration
	MaxDrawdownStopPct           float64
	CheckpointPath               string
}

type relativeHoldRiskReplayReport struct {
	Symbol                   string                        `json:"symbol"`
	From                     time.Time                     `json:"from"`
	To                       time.Time                     `json:"to"`
	WarmupFrom               time.Time                     `json:"warmupFrom"`
	PreloadFrom              time.Time                     `json:"preloadFrom"`
	RelativeHoldWarmupLabels int                           `json:"relativeHoldWarmupLabels,omitempty"`
	RelativeHoldWarmupNeff   float64                       `json:"relativeHoldWarmupEffectiveSamples,omitempty"`
	RelativeHoldWarmup       time.Duration                 `json:"relativeHoldWarmup,omitempty"`
	Horizon                  time.Duration                 `json:"horizon"`
	ReplayBBOInterval        time.Duration                 `json:"replayBBOInterval"`
	ReplayCacheHit           bool                          `json:"replayCacheHit"`
	CheckpointPath           string                        `json:"checkpointPath,omitempty"`
	CheckpointLoaded         bool                          `json:"checkpointLoaded"`
	CheckpointSaved          bool                          `json:"checkpointSaved"`
	PairEquityJPY            float64                       `json:"pairEquityJPY"`
	StartingBase             float64                       `json:"startingBase"`
	QueueMultiplier          float64                       `json:"queueMultiplier"`
	BBOEvents                int                           `json:"bboEvents"`
	AggTradeEvents           int                           `json:"aggTradeEvents"`
	ReplayNetPnLJPY          float64                       `json:"replayNetPnLJPY"`
	ReplayHoldPnLJPY         float64                       `json:"replayHoldPnLJPY"`
	ReplayMaximumDrawdownPct float64                       `json:"replayMaximumDrawdownPct"`
	ReplayFills              int                           `json:"replayFills"`
	ReplayStoppedEarly       bool                          `json:"replayStoppedEarly"`
	EligibleLabels           int                           `json:"eligibleLabels"`
	MaturedLabels            int                           `json:"maturedLabels"`
	EffectiveSamples         float64                       `json:"effectiveSamples"`
	DownsideEffectiveSamples float64                       `json:"downsideEffectiveSamples"`
	PrecisionStdErrorBps     float64                       `json:"precisionStdErrorBps"`
	PrecisionLower95Bps      float64                       `json:"precisionOneSidedLower95Bps"`
	RequiredEffectiveSamples float64                       `json:"requiredEffectiveSamples"`
	StateReady               bool                          `json:"stateReady"`
	DownsideReady            bool                          `json:"downsideReady"`
	StateReason              string                        `json:"stateReason"`
	MeanExcessReturnBps      float64                       `json:"meanExcessReturnBps"`
	TrackingErrorBps         float64                       `json:"trackingErrorBps"`
	DownsideBeta             float64                       `json:"downsideBeta"`
	DownsideBetaUpper        float64                       `json:"downsideBetaUpper"`
	DownsideCVaRBps          float64                       `json:"downsideCVaRBps"`
	StrategySharpeAnnualized float64                       `json:"strategySharpeAnnualized"`
	StrategyHoldCorrelation  float64                       `json:"strategyHoldCorrelation"`
	StrategyHoldBeta         float64                       `json:"strategyHoldBeta"`
	StrategyReturnVolBps     float64                       `json:"strategyReturnVolatilityBps"`
	HoldReturnVolBps         float64                       `json:"holdReturnVolatilityBps"`
	PairedMeanExcessBps      float64                       `json:"pairedMeanExcessBps"`
	PairedStdErrorBps        float64                       `json:"pairedStdErrorBps"`
	PairedLower95Bps         float64                       `json:"pairedOneSidedLower95Bps"`
	PairedTStatistic         float64                       `json:"pairedTStatistic"`
	PairedMeanExcessJPY      float64                       `json:"pairedMeanExcessJPY"`
	PairedStdErrorJPY        float64                       `json:"pairedStdErrorJPY"`
	PairedLower95JPY         float64                       `json:"pairedOneSidedLower95JPY"`
	PositiveBlocks           int                           `json:"positiveBlocks"`
	TotalBlocks              int                           `json:"totalBlocks"`
	Gate                     string                        `json:"gate"`
	Limitations              []string                      `json:"limitations"`
	Baseline                 relativeHoldRiskReplaySummary `json:"baseline"`
	Integrated               relativeHoldRiskReplaySummary `json:"integrated"`
	IntegratedDeltaNetPnLJPY float64                       `json:"integratedDeltaNetPnLJPY"`
	IntegratedDeltaExcessJPY float64                       `json:"integratedDeltaExcessJPY"`
}

type relativeHoldRiskReplaySummary struct {
	Policy                             string  `json:"policy"`
	RelativeHoldRiskEnabled            bool    `json:"relativeHoldRiskEnabled"`
	RelativeHoldRiskApplied            bool    `json:"relativeHoldRiskApplied"`
	RelativeHoldRiskUtilityEvaluations int     `json:"relativeHoldRiskUtilityEvaluations"`
	RelativeHoldRiskUtilitySumJPYHour  float64 `json:"relativeHoldRiskUtilitySumJPYHour"`
	RelativeHoldRiskReadyInputs        int     `json:"relativeHoldRiskReadyInputs"`
	RelativeHoldRiskReadyDecisions     int     `json:"relativeHoldRiskReadyDecisions"`
	JointQuoteEvaluations              int     `json:"jointQuoteEvaluations"`
	JointQuoteAccepted                 int     `json:"jointQuoteAccepted"`
	JointQuoteApplied                  int     `json:"jointQuoteApplied"`
	ReplayRelativeHoldMaturedLabels    int     `json:"replayRelativeHoldMaturedLabels"`
	ReplayRelativeHoldEffectiveSamples float64 `json:"replayRelativeHoldEffectiveSamples"`
	ReplayRelativeHoldStateReady       bool    `json:"replayRelativeHoldStateReady"`
	NetPnLJPY                          float64 `json:"netPnLJPY"`
	HoldPnLJPY                         float64 `json:"holdPnLJPY"`
	ExcessVsHoldJPY                    float64 `json:"excessVsHoldJPY"`
	MaximumDrawdownPct                 float64 `json:"maximumDrawdownPct"`
	Fills                              int     `json:"fills"`
	EligibleLabels                     int     `json:"eligibleLabels"`
	MaturedLabels                      int     `json:"maturedLabels"`
	EffectiveSamples                   float64 `json:"effectiveSamples"`
	DownsideEffectiveSamples           float64 `json:"downsideEffectiveSamples"`
	PrecisionStdErrorBps               float64 `json:"precisionStdErrorBps"`
	PrecisionLower95Bps                float64 `json:"precisionOneSidedLower95Bps"`
	RequiredEffectiveSamples           float64 `json:"requiredEffectiveSamples"`
	StateReady                         bool    `json:"stateReady"`
	DownsideReady                      bool    `json:"downsideReady"`
	MeanExcessReturnBps                float64 `json:"meanExcessReturnBps"`
	TrackingErrorBps                   float64 `json:"trackingErrorBps"`
	DownsideBeta                       float64 `json:"downsideBeta"`
	DownsideBetaUpper                  float64 `json:"downsideBetaUpper"`
	TotalBeta                          float64 `json:"totalBeta"`
	TotalBetaUpper                     float64 `json:"totalBetaUpper"`
	TotalBetaReady                     bool    `json:"totalBetaReady"`
	DownsideCVaRBps                    float64 `json:"downsideCVaRBps"`
	StrategySharpeAnnualized           float64 `json:"strategySharpeAnnualized"`
	StrategyHoldCorrelation            float64 `json:"strategyHoldCorrelation"`
	StrategyHoldBeta                   float64 `json:"strategyHoldBeta"`
	StrategyReturnVolBps               float64 `json:"strategyReturnVolatilityBps"`
	HoldReturnVolBps                   float64 `json:"holdReturnVolatilityBps"`
	PairedMeanExcessBps                float64 `json:"pairedMeanExcessBps"`
	PairedStdErrorBps                  float64 `json:"pairedStdErrorBps"`
	PairedLower95Bps                   float64 `json:"pairedOneSidedLower95Bps"`
	PairedTStatistic                   float64 `json:"pairedTStatistic"`
	PositiveBlocks                     int     `json:"positiveBlocks"`
	TotalBlocks                        int     `json:"totalBlocks"`
	Gate                               string  `json:"gate"`
}

type relativeHoldReplayBlock struct {
	Label     gammacapture.RelativeHoldRiskLabel
	ExcessBps float64
	ExcessJPY float64
}

// relativeHoldBlockRiskMetrics uses the same non-overlapping matured blocks as
// the Relative-Hold estimator. This avoids reporting a deceptively precise
// tick-level Sharpe/correlation from heavily overlapping equity observations.
// Returns are measured over horizon, so Sharpe is annualized from that clock.
func relativeHoldBlockRiskMetrics(blocks []relativeHoldReplayBlock, horizon time.Duration) (sharpe, correlation, beta, strategyVolBps, holdVolBps float64) {
	if len(blocks) < 2 {
		return 0, 0, 0, 0, 0
	}
	strategy := make([]float64, 0, len(blocks))
	hold := make([]float64, 0, len(blocks))
	for _, block := range blocks {
		strategy = append(strategy, block.Label.StrategyReturn)
		hold = append(hold, block.Label.HoldReturn)
	}
	meanStrategy, meanHold := 0.0, 0.0
	for i := range strategy {
		meanStrategy += strategy[i]
		meanHold += hold[i]
	}
	meanStrategy /= float64(len(strategy))
	meanHold /= float64(len(hold))
	varStrategy, varHold, covariance := 0.0, 0.0, 0.0
	for i := range strategy {
		ds, dh := strategy[i]-meanStrategy, hold[i]-meanHold
		varStrategy += ds * ds
		varHold += dh * dh
		covariance += ds * dh
	}
	denominator := float64(len(strategy) - 1)
	varStrategy /= denominator
	varHold /= denominator
	covariance /= denominator
	strategyVol := math.Sqrt(math.Max(0, varStrategy))
	holdVol := math.Sqrt(math.Max(0, varHold))
	strategyVolBps = strategyVol * 10_000
	holdVolBps = holdVol * 10_000
	if strategyVol > 0 {
		periodsPerYear := (365 * 24 * time.Hour).Hours() / horizon.Hours()
		if periodsPerYear <= 0 || math.IsNaN(periodsPerYear) || math.IsInf(periodsPerYear, 0) {
			periodsPerYear = 24 * 365
		}
		sharpe = meanStrategy / strategyVol * math.Sqrt(periodsPerYear)
	}
	if varHold > 0 {
		beta = covariance / varHold
	}
	if strategyVol > 0 && holdVol > 0 {
		correlation = covariance / (strategyVol * holdVol)
		correlation = math.Max(-1, math.Min(1, correlation))
	}
	return
}

// updateRelativeHoldRiskFromLatestEquity advances the replay model only from
// equity points that have reached the fixed label horizon. It is called after
// recordEquity and before the next quote optimizer, so the current decision
// cannot see its own future outcome.
func (s *productionReplayState) updateRelativeHoldRiskFromLatestEquity() {
	if s == nil || s.relativeHoldRiskModel == nil || len(s.equityCurve) == 0 {
		return
	}
	current := s.equityCurve[len(s.equityCurve)-1]
	if !s.relativeHoldRiskResumeAfter.IsZero() {
		if !current.At.After(s.relativeHoldRiskResumeAfter) {
			return
		}
		// A checkpoint already contains every label through its cursor. Start
		// a fresh non-overlapping anchor after that cursor; older equity points
		// are retained for diagnostics but cannot be replayed a second time.
		if s.relativeHoldRiskAnchor == nil || !s.relativeHoldRiskAnchor.At.After(s.relativeHoldRiskResumeAfter) {
			anchor := current
			s.relativeHoldRiskAnchor = &anchor
			return
		}
	}
	if current.EquityJPY <= 0 || current.HoldEquityJPY <= 0 {
		return
	}
	if s.relativeHoldRiskAnchor == nil {
		anchor := current
		s.relativeHoldRiskAnchor = &anchor
		return
	}
	anchor := s.relativeHoldRiskAnchor
	if current.At.Before(anchor.At.Add(s.relativeHoldRiskHorizon)) {
		return
	}
	if anchor.EquityJPY <= 0 || anchor.HoldEquityJPY <= 0 {
		copy := current
		s.relativeHoldRiskAnchor = &copy
		return
	}
	strategyReturn := math.Log(current.EquityJPY / anchor.EquityJPY)
	holdReturn := math.Log(current.HoldEquityJPY / anchor.HoldEquityJPY)
	if !finiteRelativeReplayReturn(strategyReturn) || !finiteRelativeReplayReturn(holdReturn) {
		copy := current
		s.relativeHoldRiskAnchor = &copy
		return
	}
	if s.relativeHoldRiskModel.UpdateLabel(gammacapture.RelativeHoldRiskLabel{
		DecisionAt: anchor.At, MaturedAt: current.At,
		StrategyReturn: strategyReturn, HoldReturn: holdReturn,
	}) {
		copy := current
		s.relativeHoldRiskAnchor = &copy
	}
}

func finiteRelativeReplayReturn(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func (s *productionReplayState) relativeHoldRiskInput() gammacapture.RelativeHoldRiskInput {
	if s == nil || s.relativeHoldRiskModel == nil {
		return gammacapture.RelativeHoldRiskInput{}
	}
	state := s.relativeHoldRiskModel.Snapshot()
	shadowOnly := s.cfg.RelativeHoldRisk.ShadowOnly || !s.scoreStarted
	return gammacapture.RelativeHoldRiskInput{
		Enabled:               s.cfg.RelativeHoldRisk.Enabled,
		ShadowOnly:            shadowOnly,
		State:                 state,
		TrackingErrorAversion: s.cfg.RelativeHoldRisk.TrackingErrorAversion,
		DownsideBetaAversion:  s.cfg.RelativeHoldRisk.DownsideBetaAversion,
		TotalBetaAversion:     s.cfg.RelativeHoldRisk.TotalBetaAversion,
	}
}

// beginScore closes the synthetic preload lifecycle at the exact scoreFrom
// boundary.  Learned models, delayed labels, and account balances are kept;
// pending orders and performance counters are not allowed to leak from the
// shadow warmup into the scored interval.
func (s *productionReplayState) beginScore(book bboSnapshot) {
	if s == nil || s.scoreStarted {
		return
	}
	s.cancelQuotes(book.time)
	s.pendingMacroIOC = productionReplayMacroIOC{}
	s.pendingFastTargetIOC = productionReplayFastTargetIOC{}
	s.completionContract = replayCompletionContract{}
	s.fillRefreshPending = false
	mid := (book.bid + book.ask) / 2
	if reset := s.scoreAccountReset; reset != nil {
		// Keep causal estimators and matured labels, but restore the account and
		// execution lifecycle to the live score-boundary snapshot. A real
		// startup does not execute the historical observations it preloads.
		s.inventory = math.Max(0, reset.Base)
		s.quote = math.Max(0, reset.PairEquityJPY-s.inventory*mid)
		s.fees, s.takerFees = 0, 0
		s.quotedTargetSet = false
		s.quotedTargetRatio, s.quotedFastTargetRatio = 0, 0
		s.quotedFastReservationBps = 0
		s.lastMakerFill = gammacapture.MakerPostFillState{}
		s.relativeHoldRiskAnchor = nil
		s.acquisitionCooldownUntil = time.Time{}
		s.acquisitionDeficitSince = time.Time{}
		s.acquisitionDeficitAnchorMid = 0
		s.noOrderRetryAfter = time.Time{}
		s.noOrderReferenceBid, s.noOrderReferenceAsk = 0, 0
		s.lastFastTargetExecutionModelAt = time.Time{}
		s.lastFastTargetDecisionModelAt = time.Time{}
		s.lastFastTargetExecutionDirection = 0
	}
	s.scoreInitialQuote = s.quote
	s.scoreInitialInventory = s.inventory
	s.scoreInitialHoldEquity = s.quote + s.inventory*mid
	s.scoreInitialEquity = s.scoreInitialHoldEquity - s.fees
	s.scoreInitialFees = s.fees
	s.scoreStarted = true

	// Reset only replay/report counters. The operational models, account, and
	// cumulative fee ledger intentionally continue across the boundary.
	s.books, s.trades, s.gaps, s.refreshes, s.quoteActive = 0, 0, 0, 0, 0
	s.fills, s.buys, s.sells, s.roundTrips = 0, 0, 0, 0
	s.unmatchedBuys, s.unmatchedSells = 0, 0
	s.fastInventoryZoneDecisions, s.longHorizonAdjustmentDecisions = 0, 0
	s.featureChecks, s.featureReady = 0, 0
	s.fillsByDay = make(map[string]*productionReplayDay)
	s.acquisitionResets, s.macroActiveAttempts, s.macroActiveFills = 0, 0, 0
	s.macroActiveQuantity = 0
	s.fastTargetActiveAttempts, s.fastTargetActiveFills = 0, 0
	s.fastTargetActiveQuantity = 0
	s.acquisitionEvaluations, s.acquisitionQuantity = 0, 0
	s.acquisitionRejections = make(map[string]int)
	s.acquisitionDrawdownLimitSamples, s.acquisitionDrawdownLimitSumBps = 0, 0
	s.minAcquisitionDrawdownLimitBps, s.maxAcquisitionDrawdownLimitBps = 0, 0
	s.maxUpProbabilityLower, s.maxAcquisitionIOCValueBps, s.maxAcquisitionImprovementBps = 0, 0, 0
	s.postFillUtilityEvaluations, s.postFillUtilityApplied = 0, 0
	s.postFillUtilityReasons = make(map[string]int)
	s.fastReservationEvaluations, s.fastReservationApplied = 0, 0
	s.fastReservationReasons = make(map[string]int)
	s.fastTargetSwitchEvaluations, s.fastTargetSwitchApplied, s.fastTargetSwitchRetained = 0, 0, 0
	s.fastTargetSwitchReasons = make(map[string]int)
	s.fastTargetSwitchNetValueSumJPY = 0
	s.dynamicInventoryAimEvaluations, s.dynamicInventoryAimPassed, s.dynamicInventoryAimApplied = 0, 0, 0
	s.dynamicInventoryAimReasons = make(map[string]int)
	s.earlyStatisticalEvaluations, s.earlyStatisticalApplied = 0, 0
	s.jointQuoteEvaluations, s.jointQuoteAccepted, s.jointQuoteApplied = 0, 0, 0
	s.jointQuoteReasons = make(map[string]int)
	s.jointCapitalUtilizationSum, s.jointPairCapitalUtilizationSum = 0, 0
	s.maximumJointLowerPnLJPYHour, s.jointCandidateCount, s.jointBestObserved = 0, 0, 0
	s.jointCandidateUtilizationSum, s.maximumJointExpectedPnLJPYHour = 0, 0
	s.minimumJointPathEffectiveSamples = 0
	s.directionalTargetOverrides, s.directionalTargetSumBps = 0, 0
	s.quoteLifecycleEvaluations, s.quoteLifecycleHazardReadyReviews = 0, 0
	s.quoteLifecycleActions = make(map[string]int)
	s.quoteLifecycleIncrementalSumBps = 0
	s.maxPostFillIncrementalMeanBps, s.maxPostFillIncrementalLowerBps = 0, 0
	s.macroActiveDecisions = nil
	s.fastTargetActiveDecisions = nil
	s.volumeProfileSamples, s.volumeProfileReadySamples = 0, 0
	s.asymmetricRiskMultiplierSum, s.asymmetricRiskSamples = 0, 0
	s.asymmetricRiskMinMultiplier, s.asymmetricRiskMaxMultiplier = 0, 0
	s.asymmetricRiskReadySamples = 0
	s.jointQuoteDecisions = nil
	s.horizonDiagnostics = make(map[string]*productionReplayHorizonDiagnostic)
	s.lastDecisionSecond = time.Time{}
	s.lastHorizonDiagnosticBucket = time.Time{}
	s.distanceSamples, s.bidDistanceSum, s.askDistanceSum = 0, 0, 0
	s.quoteLifeSamples, s.quoteLifeSum = 0, 0
	s.activeDuration = 0
	s.equityPeak = s.scoreInitialEquity
	s.maximumDrawdownPct, s.stopped = 0, false
	s.stopAt, s.stopReason = time.Time{}, ""
	// Taker fees are a report counter. In continuous-account replay, maker fees
	// remain in the cumulative ledger; scoreAccountReset instead restored the
	// exact live account and zeroed both preload fee ledgers above.
	s.takerFees = 0
}

// runRelativeHoldRiskReplay performs paired baseline/integrated production
// replays. The integrated arm feeds only the matured Relative-Hold scalar to
// the Fast joint optimizer; all other policy and execution settings remain
// identical.
func runRelativeHoldRiskReplay(in relativeHoldRiskReplayInput) {
	barrier, intensity, cfg := loadProductionConfig(in.ConfigPath, in.Symbol)
	if in.Horizon <= 0 {
		in.Horizon = time.Hour
	}
	integratedCfg := cfg
	integratedCfg.RelativeHoldRisk = relativeHoldRiskStudyConfig(in.Horizon)
	relativeHoldWarmup := integratedCfg.RelativeHoldRisk.WarmupRequirement(
		time.Duration(integratedCfg.HorizonUpdateInterval))
	// Include the Relative-Hold N_eff requirement when deriving the default
	// preload. The baseline arm still disables the component, but it receives
	// the identical warmup interval so the paired lifecycle stays symmetric.
	warmupCfg := cfg
	warmupCfg.RelativeHoldRisk = integratedCfg.RelativeHoldRisk
	warmFrom, exactFrom := productionReplayLoadRange(warmupCfg, in.From, time.Time{})
	preloadFrom := in.PreloadFrom
	if preloadFrom.IsZero() || preloadFrom.After(in.From) {
		preloadFrom = warmFrom
	}
	if preloadFrom.Before(warmFrom) {
		warmFrom = preloadFrom
	}
	books, trades, cacheHit := loadWarmReplayDatasetAtInterval(
		in.DataPath, in.Symbol, warmFrom, in.To, exactFrom,
		replayConfigFingerprint(in.ConfigPath), in.ReplayCacheDir, in.BBOInterval)
	books = compactBBO(books)
	trades = compactTrades(trades)
	if len(books) < 2 || len(trades) == 0 {
		fatalf("relative-hold replay has insufficient events: bbo=%d trades=%d", len(books), len(trades))
	}
	queue := in.QueueMultiplier
	if queue < 0 {
		queue = 0
	}
	baselineCfg := cfg
	baselineCfg.RelativeHoldRisk = gammacapture.RelativeHoldRiskConfig{}
	checkpoint, checkpointErr := loadRelativeHoldRiskReplayCheckpoint(
		in.CheckpointPath, in.Symbol, integratedCfg.RelativeHoldRisk, in.From)
	if checkpointErr != nil {
		fatalf("load relative-hold replay checkpoint: %v", checkpointErr)
	}
	baselineReplay := simulateProductionPolicyWithQuantityProjectionFrom(
		books, trades, baselineCfg, barrier, intensity, nil, replayLegacy,
		in.Symbol, in.PairEquityJPY, in.StartingBase, queue, preloadFrom, in.From,
		true, in.MaxDrawdownStopPct, nil, nil, nil)
	integratedReplay := simulateProductionPolicyWithQuantityProjectionFrom(
		books, trades, integratedCfg, barrier, intensity, nil, replayLegacy,
		in.Symbol, in.PairEquityJPY, in.StartingBase, queue, preloadFrom, in.From,
		true, in.MaxDrawdownStopPct, func() *gammacapture.RelativeHoldRiskCheckpoint {
			if checkpoint == nil {
				return nil
			}
			model := checkpoint.Model
			return &model
		}(), nil, nil)
	// Both arms execute the same causal shadow preload. Relative-Hold remains
	// shadow-only before scoreFrom, so the paired policy difference starts at
	// the requested scoring boundary.
	baseline := summarizeRelativeHoldRiskReplay("baseline", baselineReplay, in.Horizon)
	integrated := summarizeRelativeHoldRiskReplay("integrated", integratedReplay, in.Horizon)
	checkpointSaved := false
	if in.CheckpointPath != "" && integratedReplay.relativeHoldRiskCheckpoint != nil {
		checkpointAfter := in.To
		if integratedReplay.StoppedEarly && !integratedReplay.To.IsZero() {
			checkpointAfter = integratedReplay.To
		}
		if err := saveRelativeHoldRiskReplayCheckpoint(
			in.CheckpointPath, in.Symbol, in.From, checkpointAfter, integratedCfg.RelativeHoldRisk,
			integratedReplay.relativeHoldRiskCheckpoint); err != nil {
			fatalf("save relative-hold replay checkpoint: %v", err)
		}
		checkpointSaved = true
	}
	report := relativeHoldRiskReplayReport{
		Symbol: in.Symbol, From: in.From, To: in.To, WarmupFrom: warmFrom, PreloadFrom: preloadFrom,
		RelativeHoldWarmupLabels: relativeHoldWarmup.RequiredLabels,
		RelativeHoldWarmupNeff:   relativeHoldWarmup.EffectiveSamples,
		RelativeHoldWarmup:       relativeHoldWarmup.RequiredDuration,
		Horizon:                  in.Horizon, ReplayBBOInterval: in.BBOInterval,
		ReplayCacheHit: cacheHit, CheckpointPath: in.CheckpointPath,
		CheckpointLoaded: checkpoint != nil, CheckpointSaved: checkpointSaved,
		PairEquityJPY: in.PairEquityJPY,
		StartingBase:  in.StartingBase, QueueMultiplier: queue,
		BBOEvents: integratedReplay.BBOEvents, AggTradeEvents: integratedReplay.AggTradeEvents,
		ReplayNetPnLJPY: integratedReplay.NetPnLJPY, ReplayHoldPnLJPY: integratedReplay.HoldPnLJPY,
		ReplayMaximumDrawdownPct: integratedReplay.MaximumDrawdownPct, ReplayFills: integratedReplay.FullFills,
		ReplayStoppedEarly: integratedReplay.StoppedEarly, EligibleLabels: integrated.EligibleLabels,
		MaturedLabels: integrated.MaturedLabels, EffectiveSamples: integrated.EffectiveSamples,
		DownsideEffectiveSamples: integrated.DownsideEffectiveSamples,
		PrecisionStdErrorBps:     integrated.PrecisionStdErrorBps,
		PrecisionLower95Bps:      integrated.PrecisionLower95Bps,
		RequiredEffectiveSamples: integrated.RequiredEffectiveSamples,
		StateReady:               integrated.StateReady,
		DownsideReady:            integrated.DownsideReady, StateReason: "paired baseline/integrated summaries below",
		MeanExcessReturnBps: integrated.MeanExcessReturnBps, TrackingErrorBps: integrated.TrackingErrorBps,
		DownsideBeta: integrated.DownsideBeta, DownsideBetaUpper: integrated.DownsideBetaUpper,
		DownsideCVaRBps: integrated.DownsideCVaRBps, PairedMeanExcessBps: integrated.PairedMeanExcessBps,
		PairedStdErrorBps: integrated.PairedStdErrorBps, PairedLower95Bps: integrated.PairedLower95Bps,
		PairedTStatistic: integrated.PairedTStatistic, PairedMeanExcessJPY: integrated.ExcessVsHoldJPY,
		PositiveBlocks: integrated.PositiveBlocks, TotalBlocks: integrated.TotalBlocks,
		Gate: integrated.Gate, Baseline: baseline, Integrated: integrated,
		IntegratedDeltaNetPnLJPY: integratedReplay.NetPnLJPY - baselineReplay.NetPnLJPY,
		IntegratedDeltaExcessJPY: integrated.ExcessVsHoldJPY - baseline.ExcessVsHoldJPY,
		Limitations: []string{
			"same-symbol ETHJPY only; no cross-ticker borrowing",
			"labels use next observed compacted executable-BBO equity point at or after one hour",
			"strategy equity includes simulated fees; Hold is the same initial inventory marked at replay BBO",
			"queue multiplier is fixed for this study and is not private exchange queue confirmation",
			"Relative-Hold CVaR is shadow-only; tracking/beta utility is the sole integrated scalar",
			"preload is a causal shadow replay; scoreFrom cancels preload orders and resets score counters while retaining labels and model state",
			"the optional checkpoint stores only Relative-Hold sufficient statistics; core Fast models still use their normal same-symbol preload",
		},
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fatalf("encode relative-hold replay: %v", err)
	}
}

func relativeHoldRiskStudyConfig(horizon time.Duration) gammacapture.RelativeHoldRiskConfig {
	return gammacapture.RelativeHoldRiskConfig{
		Enabled: true, ShadowOnly: false, Horizon: horizon, HalfLife: 6 * horizon,
		MinimumEffectiveSamples: 4, MinimumDownsideEffectiveSamples: 4,
		PriorEffectiveSamples: 4, TargetDownsideBeta: 1, ConfidenceZ: 1.645,
		TailQuantile: .95, MaxTailSamples: 256,
		TrackingErrorAversion: 1, DownsideBetaAversion: 1,
		TargetTotalBeta: .35, TotalBetaAversion: 25,
	}
}

func summarizeRelativeHoldRiskReplay(policy string, replay productionReplayResult, horizon time.Duration) relativeHoldRiskReplaySummary {
	blocks, eligible := relativeHoldReplayBlocks(replay.EquityCurve, horizon)
	model := gammacapture.NewRelativeHoldRiskModel(relativeHoldRiskStudyConfig(horizon))
	for _, block := range blocks {
		if !model.UpdateLabel(block.Label) {
			fatalf("relative-hold replay generated non-causal label at %s", block.Label.MaturedAt.Format(time.RFC3339))
		}
	}
	state := model.Snapshot()
	pairedBps := make([]float64, 0, len(blocks))
	positive := 0
	for _, block := range blocks {
		pairedBps = append(pairedBps, block.ExcessBps)
		if block.ExcessBps > 0 {
			positive++
		}
	}
	mean, se, lower, tStat := relativeHoldMeanStats(pairedBps)
	studyConfig := relativeHoldRiskStudyConfig(horizon)
	precision := state.Precision(studyConfig.ConfidenceZ)
	sharpe, correlation, beta, strategyVolBps, holdVolBps := relativeHoldBlockRiskMetrics(blocks, horizon)
	summary := relativeHoldRiskReplaySummary{
		Policy: policy, RelativeHoldRiskEnabled: replay.RelativeHoldRiskEnabled,
		RelativeHoldRiskApplied:            replay.RelativeHoldRiskApplied,
		RelativeHoldRiskUtilityEvaluations: replay.RelativeHoldRiskUtilityEvaluations,
		RelativeHoldRiskUtilitySumJPYHour:  replay.RelativeHoldRiskUtilitySumJPYHour,
		RelativeHoldRiskReadyInputs:        replay.RelativeHoldRiskReadyInputs,
		RelativeHoldRiskReadyDecisions:     replay.RelativeHoldRiskReadyDecisions,
		JointQuoteEvaluations:              replay.JointQuoteEvaluations,
		JointQuoteAccepted:                 replay.JointQuoteAccepted,
		JointQuoteApplied:                  replay.JointQuoteApplied,
		ReplayRelativeHoldMaturedLabels:    replay.RelativeHoldRiskMaturedLabels,
		ReplayRelativeHoldEffectiveSamples: replay.RelativeHoldRiskEffectiveSamples,
		ReplayRelativeHoldStateReady:       replay.RelativeHoldRiskMaturedLabels > 0 && replay.RelativeHoldRiskEffectiveSamples > 0,
		NetPnLJPY:                          replay.NetPnLJPY, HoldPnLJPY: replay.HoldPnLJPY,
		ExcessVsHoldJPY:    replay.NetPnLJPY - replay.HoldPnLJPY,
		MaximumDrawdownPct: replay.MaximumDrawdownPct, Fills: replay.FullFills,
		EligibleLabels: eligible, MaturedLabels: len(blocks),
		EffectiveSamples: state.EffectiveSamples, DownsideEffectiveSamples: state.DownsideEffectiveSamples,
		PrecisionStdErrorBps: precision.StandardErrorBps, PrecisionLower95Bps: precision.OneSidedLowerBps,
		RequiredEffectiveSamples: finiteJSONFloat(precision.RequiredEffectiveSamples),
		StateReady:               state.Ready, DownsideReady: state.DownsideReady,
		MeanExcessReturnBps: state.MeanExcessReturnBps, TrackingErrorBps: state.TrackingErrorBps,
		DownsideBeta: state.DownsideBeta, DownsideBetaUpper: state.DownsideBetaUpper,
		TotalBeta: state.TotalBeta, TotalBetaUpper: state.TotalBetaUpper,
		TotalBetaReady:           state.TotalBetaReady,
		DownsideCVaRBps:          state.DownsideCVaRBps,
		StrategySharpeAnnualized: sharpe, StrategyHoldCorrelation: correlation,
		StrategyHoldBeta: beta, StrategyReturnVolBps: strategyVolBps,
		HoldReturnVolBps: holdVolBps, PairedMeanExcessBps: mean,
		PairedStdErrorBps: se, PairedLower95Bps: lower, PairedTStatistic: tStat,
		PositiveBlocks: positive, TotalBlocks: len(blocks),
	}
	switch {
	case len(blocks) == 0:
		summary.Gate = "INCONCLUSIVE_NO_MATURED_LABELS"
	case !state.Ready || len(blocks) < 2:
		summary.Gate = "INCONCLUSIVE_STATE_WARMING"
	case state.MeanExcessReturn <= 0:
		summary.Gate = "REJECT_NO_POSITIVE_HOLD_RELATIVE_MEAN"
	case precision.OneSidedLower <= 0:
		summary.Gate = "INCONCLUSIVE_PRECISION"
	case lower <= 0:
		summary.Gate = "REJECT_NO_POSITIVE_HOLD_RELATIVE_LOWER_BOUND"
	default:
		summary.Gate = "PROMOTE_COMPONENT_REPLAY_CANDIDATE"
	}
	return summary
}

func finiteJSONFloat(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		// JSON has no representation for +/-Inf. Zero means "not finite / no
		// finite sample requirement" in the report; the gate reason remains
		// explicit in Gate and the full value is still available in-memory.
		return 0
	}
	return value
}

func relativeHoldReplayBlocks(curve []productionEquityPoint, horizon time.Duration) ([]relativeHoldReplayBlock, int) {
	if horizon <= 0 || len(curve) < 2 {
		return nil, len(curve)
	}
	eligible := 0
	blocks := make([]relativeHoldReplayBlock, 0)
	for anchor := 0; anchor < len(curve); {
		start := curve[anchor]
		if start.EquityJPY <= 0 || start.HoldEquityJPY <= 0 {
			anchor++
			continue
		}
		target := start.At.Add(horizon)
		maturity := sort.Search(len(curve), func(index int) bool {
			return !curve[index].At.Before(target)
		})
		if maturity >= len(curve) {
			break
		}
		end := curve[maturity]
		if end.EquityJPY <= 0 || end.HoldEquityJPY <= 0 {
			anchor = maturity + 1
			continue
		}
		eligible++
		strategyReturn := math.Log(end.EquityJPY / start.EquityJPY)
		holdReturn := math.Log(end.HoldEquityJPY / start.HoldEquityJPY)
		if math.IsNaN(strategyReturn) || math.IsInf(strategyReturn, 0) || math.IsNaN(holdReturn) || math.IsInf(holdReturn, 0) {
			anchor = maturity + 1
			continue
		}
		blocks = append(blocks, relativeHoldReplayBlock{
			Label: gammacapture.RelativeHoldRiskLabel{
				DecisionAt: start.At, MaturedAt: end.At,
				StrategyReturn: strategyReturn, HoldReturn: holdReturn,
			},
			ExcessBps: (strategyReturn - holdReturn) * 10_000,
			ExcessJPY: start.EquityJPY * (math.Exp(strategyReturn) - math.Exp(holdReturn)),
		})
		// Move the next anchor to this matured point to avoid overlapping
		// one-hour labels and to make block-level CI conservative.
		anchor = maturity
	}
	return blocks, eligible
}

func relativeHoldMeanStats(values []float64) (mean, standardError, oneSidedLower95, tStatistic float64) {
	if len(values) == 0 {
		return 0, 0, 0, 0
	}
	for _, value := range values {
		mean += value
	}
	mean /= float64(len(values))
	if len(values) < 2 {
		return mean, 0, mean, 0
	}
	variance := 0.0
	for _, value := range values {
		delta := value - mean
		variance += delta * delta
	}
	variance /= float64(len(values) - 1)
	standardError = math.Sqrt(math.Max(0, variance) / float64(len(values)))
	if standardError > 0 {
		tStatistic = mean / standardError
	}
	// This is the predeclared one-sided 95% normal bound used by the existing
	// GammaCapture posterior reports. It is not a multiplicity correction.
	oneSidedLower95 = mean - 1.645*standardError
	return mean, standardError, oneSidedLower95, tStatistic
}
