package gammacapture

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"time"

	"github.com/c9s/bbgo/pkg/fixedpoint"
	"github.com/c9s/bbgo/pkg/types"
)

type makerStartupWarmupStats struct {
	Files                    int
	TradeFiles               int
	BBOUpdates               int
	TradeUpdates             int
	PendingTrades            int
	PrivateCalibrationEvents int
	RelativeHoldPrivateFills int
	RelativeHoldLabels       int
	RelativeHoldPreloadUsed  bool
	First                    time.Time
	Last                     time.Time
}

type makerStartupTrade struct {
	when  time.Time
	trade types.Trade
}

const makerStartupWarmupBBOInterval = time.Second

// makerStartupBBOAccumulator keeps only the final observable BBO in each
// warm-up second.  Model state is causal at the second close, so intermediate
// quote churn cannot improve the rolling 10/15/30-minute estimators but does
// multiply parsing and model-update work.  The gap flag is OR-ed across the
// bucket so a reconnect marker is never lost by compaction.
type makerStartupBBOAccumulator struct {
	pending   bool
	bucket    time.Time
	at        time.Time
	ticker    types.BookTicker
	gapBefore bool
	emit      func(time.Time, types.BookTicker, bool)
}

func (a *makerStartupBBOAccumulator) add(at time.Time, ticker types.BookTicker, gapBefore bool) {
	bucket := at.Truncate(makerStartupWarmupBBOInterval)
	if !a.pending {
		a.pending = true
		a.bucket = bucket
		a.at = at
		a.ticker = ticker
		a.gapBefore = gapBefore
		return
	}
	if !bucket.Equal(a.bucket) {
		a.flush()
		a.pending = true
		a.bucket = bucket
		a.at = at
		a.ticker = ticker
		a.gapBefore = gapBefore
		return
	}
	a.at = at
	a.ticker = ticker
	a.gapBefore = a.gapBefore || gapBefore
}

func (a *makerStartupBBOAccumulator) flush() {
	if !a.pending {
		return
	}
	if a.emit != nil {
		a.emit(a.at, a.ticker, a.gapBefore)
	}
	a.pending = false
	a.gapBefore = false
}

// restoreAndWarmMakerModelsFromBinanceCapture restores the bounded live
// checkpoint and advances it through only the capture delta. It is deliberately
// independent of the retired online-arrival learner: crossing, horizon,
// side-volatility, Macro and fast-evidence models are all production state and
// must survive a live restart even when aggTradeWarmup is disabled.
//
// If a compatible checkpoint is unavailable, the same causal BBO pipeline is
// rebuilt from a bounded local-capture interval. When Binance private trade
// history is available, the Relative-Hold equity labels are reconstructed in
// that same interval before the first live quote. Orders are reconciled only
// after this method returns, so a restart never cancels the previous quotes and
// then waits cold for model evidence. Relative-Hold uses the same
// one-second causal BBO accumulator as the research preload, not a coarser
// 5-minute aggregate.
func (s *Strategy) restoreAndWarmMakerModelsFromBinanceCapture(now time.Time) error {
	if !s.MarketMaker.Enabled {
		return nil
	}
	if now.IsZero() || s.State == nil || s.model == nil || s.State.Engine == nil {
		return fmt.Errorf("market-maker startup replay is not initialized")
	}

	lookback := s.makerStartupWarmupLookback()
	cursor, restored, restoreErr := s.restoreModelCheckpoint(now)
	tradeCursor := s.makerLastPublicTradeAt
	if restoreErr != nil {
		log.WithError(restoreErr).WithField("symbol", s.Symbol).
			Warn("gamma-capture model checkpoint unavailable; rebuilding bounded capture state")
	}
	// A very old checkpoint would turn startup into an unbounded raw-event
	// replay. Rebuild the bounded sufficient window instead; older observations
	// cannot affect the rolling crossing/horizon estimators.
	if restored && now.Sub(cursor) > lookback {
		log.WithFields(map[string]interface{}{
			"symbol": s.Symbol, "checkpointCursor": cursor,
			"checkpointAge": now.Sub(cursor), "boundedLookback": lookback,
		}).Warn("gamma-capture model checkpoint is older than bounded replay window; rebuilding capture state")
		restored = false
	}
	if !restored {
		s.resetMakerLearningState()
		cursor = now.Add(-lookback)
		tradeCursor = cursor
	}
	relativeHoldPreload := s.prepareRelativeHoldRiskLivePreload(cursor, now, restored)

	stats, err := s.replayMakerCapture(cursor, tradeCursor, now, restored)
	if err != nil {
		return err
	}
	stats.RelativeHoldPrivateFills = relativeHoldPreload.PrivateTrades
	stats.RelativeHoldPreloadUsed = relativeHoldPreload.Used
	if s.makerRelativeHoldRisk != nil {
		stats.RelativeHoldLabels = s.makerRelativeHoldRisk.SnapshotAt(now).MaturedLabels
	}
	if !restored && stats.BBOUpdates == 0 {
		return fmt.Errorf("no valid Binance BBO observations in bounded startup window %s to %s", cursor, now)
	}
	last := stats.Last
	if last.IsZero() {
		last = cursor
	}
	if maxAge := time.Duration(s.AggTradeWarmup.MaxAge); maxAge > 0 && now.Sub(last) > maxAge {
		return fmt.Errorf("Binance BBO startup history is stale: last=%s age=%s maxAge=%s", last, now.Sub(last), maxAge)
	}

	s.makerCheckpointReplayAfter = last
	if err := s.prepareModelCheckpoint(now); err != nil {
		log.WithError(err).WithField("symbol", s.Symbol).
			Warn("prepare post-warmup gamma-capture model checkpoint failed")
	}
	snapshot := s.model.Snapshot(now)
	fast := s.adaptiveFastSnapshot(now)
	relativeHold := RelativeHoldRiskState{Reason: "relative-hold risk disabled"}
	if s.makerRelativeHoldRisk != nil {
		relativeHold = s.makerRelativeHoldRisk.SnapshotAt(now)
	}
	bocpd45 := BOCPD45Snapshot{}
	if s.makerBOCPD45 != nil {
		bocpd45 = s.makerBOCPD45.SnapshotAt(now)
	}
	privateFill := PrivateFillCalibrationSnapshot{Reason: "private-fill calibration disabled"}
	if s.makerPrivateFillCalibration != nil {
		privateFill = s.makerPrivateFillCalibration.SnapshotAt(now)
	}
	causalKlineDecision := s.makerCausalKlineDecision
	causalKlineSnapshot := CausalKlinePivotSnapshot{}
	if s.makerCausalKlinePivot != nil {
		causalKlineSnapshot = s.makerCausalKlinePivot.Snapshot()
	}
	pivotTarget := s.MarketMaker.DynamicInventoryAim.PivotRegimeTarget
	log.WithFields(map[string]interface{}{
		"symbol":                       s.Symbol,
		"checkpointRestored":           restored,
		"replayAfter":                  cursor,
		"captureFiles":                 stats.Files,
		"tradeFiles":                   stats.TradeFiles,
		"bboUpdates":                   stats.BBOUpdates,
		"tradeUpdates":                 stats.TradeUpdates,
		"pendingTrades":                stats.PendingTrades,
		"privateFillCalibrationEvents": stats.PrivateCalibrationEvents,
		"relativeHoldPreloadSource":    relativeHoldPreload.Source,
		"relativeHoldPreloadFrom":      relativeHoldPreload.From,
		"relativeHoldPreloadTo":        relativeHoldPreload.To,
		"relativeHoldPrivateFills":     stats.RelativeHoldPrivateFills,
		"relativeHoldPreloadUsed":      stats.RelativeHoldPreloadUsed,
		"relativeHoldPreloadReason":    relativeHoldPreload.Reason,
		"relativeHoldPreloadLabels":    stats.RelativeHoldLabels,
		"firstBBO":                     stats.First,
		"lastBBO":                      stats.Last,
		"slowHealth":                   snapshot.Health,
		"slowUp":                       snapshot.Up,
		"slowDown":                     snapshot.Down,
		"selectedFastWindow":           fast.Window,
		"fastHealth":                   fast.Model.Health,
		"fastUp":                       fast.Model.Up,
		"fastDown":                     fast.Model.Down,
		"fastEvidenceHealth":           fast.Evidence.Health,
		"relativeHoldEnabled":          s.MarketMaker.RelativeHoldRisk.Enabled,
		"relativeHoldReady":            relativeHold.Ready,
		"relativeHoldMaturedLabels":    relativeHold.MaturedLabels,
		"relativeHoldEffectiveSamples": relativeHold.EffectiveSamples,
		"relativeHoldReason":           relativeHold.Reason,
		"bocpd45Enabled":               bocpd45.Enabled, "bocpd45Ready": bocpd45.Ready,
		"bocpd45Calibration":                   bocpd45.Calibration,
		"bocpd45CalibrationReady":              bocpd45.CalibrationReady,
		"bocpd45CalibrationSamples":            bocpd45.CalibrationSamples,
		"bocpd45MaturedLabels":                 bocpd45.MaturedLabels,
		"privateFillCalibrationEnabled":        privateFill.Enabled,
		"privateFillCalibrationReady":          privateFill.Ready,
		"privateFillCalibrationTouchReady":     privateFill.TouchReady,
		"privateFillCalibrationStale":          privateFill.Stale,
		"privateFillCalibrationFills":          privateFill.Fills,
		"privateFillCalibrationEffectiveFills": privateFill.EffectiveFills,
		"privateFillCalibrationReason":         privateFill.Reason,
		"causalKlinePivotEnabled":              s.MarketMaker.CausalKlinePivot.Enabled,
		"causalKlinePivotBars":                 len(causalKlineSnapshot.Bars),
		"causalKlinePivotMaturedLabels":        causalKlineSnapshot.MaturedLabels,
		"causalKlinePivotPredictionReady":      causalKlineDecision.PredictionReady,
		"causalKlinePivotModelReady":           causalKlineDecision.ModelReady,
		"causalKlinePivotReason":               causalKlineDecision.Reason,
		"pivotRegimeEnabled":                   pivotTarget.Enabled || pivotTarget.CausalCEEnabled,
		"pivotRegimeCausalCEEnabled":           pivotTarget.CausalCEEnabled,
		"pivotRegimeReady":                     s.makerPivotRegimeDecision.Ready,
		"pivotRegimeHealthy":                   s.makerPivotRegimeDecision.Healthy,
		"pivotRegimeDirection":                 s.makerPivotRegimeDecision.Direction,
		"pivotRegimeCompletedLegSamples":       s.makerPivotRegimeDecision.CompletedLegSamples,
		"pivotRegimeRemainingBps":              s.makerPivotRegimeDecision.RemainingAmplitudeBps,
		"pivotRegimeExpectedBps":               s.makerPivotRegimeDecision.ExpectedLegAmplitudeBps,
		"pivotRegimeReason":                    s.makerPivotRegimeDecision.Reason,
	}).Info("restored and warmed gamma-capture models from Binance capture")
	return nil
}

// RequiredStartupWarmup is the longest causal history plus label/path maturity
// needed by the maker model. A checkpoint normally makes this a one-time cost;
// when a compatible checkpoint does not exist, startup must rebuild the full
// sufficient interval rather than silently quote with immature estimators.
func (c MarketMakerConfig) RequiredStartupWarmup() time.Duration {
	c.setDefaults()
	lookback := time.Duration(c.HorizonLookback)
	maxPathMaturity := time.Duration(0)
	for _, horizon := range c.FastModelWindows() {
		maturity := horizon
		if c.JointDistanceQuantity.TwoStageContinuation {
			maturity += c.JointContinuationHorizon(horizon)
		}
		if maturity > maxPathMaturity {
			maxPathMaturity = maturity
		}
	}
	warmup := lookback + maxPathMaturity
	if c.BOCPD45.Enabled {
		candidate := time.Duration(c.BOCPD45.CalibrationWindow) + time.Duration(c.BOCPD45.Horizon)
		if candidate > warmup {
			warmup = candidate
		}
	}
	if c.MacroInventory.Enabled {
		if candidate := c.MacroInventory.requiredHistory(); candidate > warmup {
			warmup = candidate
		}
	}
	if c.RelativeHoldRisk.Enabled {
		requirement := c.RelativeHoldRisk.WarmupRequirement(time.Duration(c.HorizonUpdateInterval))
		if requirement.Feasible && requirement.RequiredDuration > warmup {
			warmup = requirement.RequiredDuration
		}
	}
	if c.PrivateFillCalibration.Enabled {
		// Include the fill-label horizon so a cold rebuild can mature private
		// fills from the earliest replay point without reading future BBO data.
		if horizon := time.Duration(c.PrivateFillCalibration.Horizon); horizon > warmup {
			warmup = horizon
		}
	}
	pivotTarget := c.DynamicInventoryAim.PivotRegimeTarget
	if pivotTarget.CausalCEEnabled {
		// The CE owner needs completed same-direction legs, not merely a
		// healthy short-horizon quote model. Keep this bounded and explicit;
		// the replay remains O(number of captured BBO rows) and is compacted to
		// one causal observation per second before model updates.
		if pivotWarmup := time.Duration(pivotTarget.StartupWarmup); pivotWarmup > warmup {
			warmup = pivotWarmup
		}
	}
	return warmup
}

// makerStartupWarmupLookback also covers the strategy-level crossing and raw
// evidence windows that live outside MarketMakerHorizonModel.
func (s *Strategy) makerStartupWarmupLookback() time.Duration {
	lookback := time.Duration(s.AggTradeWarmup.Lookback)
	if window := time.Duration(s.Intensity.Window); window > lookback {
		lookback = window
	}
	config := s.MarketMaker
	config.setDefaults()
	if window := config.RequiredStartupWarmup(); window > lookback {
		lookback = window
	}
	if window := s.maxFastEvidenceWindow(); window > lookback {
		lookback = window
	}
	if lookback <= 0 {
		lookback = 6 * time.Hour
	}
	return lookback
}

func (s *Strategy) resetMakerLearningState() {
	s.State.Engine = NewCrossingEngine(s.Barrier.Width, time.Duration(s.Barrier.MinDwell), s.Barrier.MaxCrossingsPerEvent)
	s.State.LastReferenceTime = time.Time{}
	s.model = NewIntensityModel(s.Intensity)
	s.initializeAdaptiveFastModels()
	s.makerHorizonModel = MarketMakerHorizonModel{}
	s.makerMacroInventoryModel = MacroInventoryModel{}
	s.makerExecutableCrossingModel = NewExecutableCrossingModel(s.Symbol, s.Barrier, s.Intensity)
	s.makerCheckpointCaptureFiles = nil
	s.makerLastPublicTradeAt = time.Time{}
	s.makerPrivateLedgerReplayOffset = 0
	s.makerStartupPendingTrades = nil
	s.makerRelativeHoldPrivateTrades = nil
	s.makerRelativeHoldPrivateIndex = 0
	s.makerRelativeHoldShadowBase = 0
	s.makerRelativeHoldShadowQuote = 0
	s.makerRelativeHoldShadowReady = false
	s.makerRelativeHoldPreloadSource = ""
	s.makerRelativeHoldInitialAt = time.Time{}
	s.makerRelativeHoldInitialBase = 0
	s.makerRelativeHoldInitialQuote = 0
	s.makerRelativeHoldAnchor = nil
	if s.MarketMaker.CausalKlinePivot.Enabled {
		s.makerCausalKlineBuilder = NewCausalKlineBuilder(time.Duration(s.MarketMaker.CausalKlinePivot.Interval))
		s.makerCausalKlinePivot = NewCausalKlinePivotLearner(s.MarketMaker.CausalKlinePivot)
		s.makerCausalKlineDecision = CausalKlinePivotDecision{Reason: "causal Kline pivot learner warming"}
	} else {
		s.makerCausalKlineBuilder = nil
		s.makerCausalKlinePivot = nil
		s.makerCausalKlineDecision = CausalKlinePivotDecision{Reason: "causal Kline pivot learner disabled"}
	}
	pivotTarget := s.MarketMaker.DynamicInventoryAim.PivotRegimeTarget
	if pivotTarget.Enabled || pivotTarget.CausalCEEnabled {
		pivotTarget.setDefaults()
		s.makerPivotRegimeFilter = NewPivotRegimeFilter(pivotTarget.filterConfig())
		s.makerPivotRegimeDecision = PivotRegimeDecision{Reason: "pivot regime warming"}
		s.makerCausalRegimeTargetDecision = CausalRegimeInventoryTargetDecision{Reason: "causal regime target warming"}
	} else {
		s.makerPivotRegimeFilter = nil
		s.makerPivotRegimeDecision = PivotRegimeDecision{Reason: "pivot regime target disabled"}
		s.makerCausalRegimeTargetDecision = CausalRegimeInventoryTargetDecision{Reason: "causal regime target disabled"}
	}
}

func (s *Strategy) replayMakerCapture(bboCutoff, tradeCutoff, now time.Time, deltaOnly bool) (makerStartupWarmupStats, error) {
	trades, tradeFiles, err := s.loadMakerStartupTrades(tradeCutoff, now, deltaOnly)
	if err != nil {
		return makerStartupWarmupStats{}, err
	}
	files, err := s.binanceBBOCaptureFiles()
	if err != nil {
		return makerStartupWarmupStats{}, err
	}
	if len(files) == 0 {
		return makerStartupWarmupStats{}, fmt.Errorf("Binance BBO capture is empty for %s", s.Symbol)
	}
	files = captureFilesOverlapping(files, s.Symbol, "bookticker", bboCutoff, now)
	if deltaOnly {
		files = captureFilesChangedSinceCheckpoint(files, s.makerCheckpointCaptureFiles)
	}
	stats := makerStartupWarmupStats{Files: len(files), TradeFiles: tradeFiles}
	config := s.MarketMaker
	config.setDefaults()
	productionVersion := config.PrivateOrderFillLedger.ProductionVersion
	if productionVersion == "" && s.EnvironmentRef != nil {
		productionVersion = s.EnvironmentRef.ProductionVersion()
	}
	privateEvents, err := loadPrivateFillCalibrationLedger(
		config.PrivateOrderFillLedger, s.Symbol, productionVersion, bboCutoff, now, deltaOnly,
		s.makerPrivateLedgerReplayOffset)
	if err != nil {
		return makerStartupWarmupStats{}, err
	}
	tradeIndex := 0
	privateEventIndex := 0
	observeTradesBefore := func(before time.Time) {
		for tradeIndex < len(trades) && trades[tradeIndex].when.Before(before) {
			s.observeMakerReplayTrade(trades[tradeIndex], config)
			stats.TradeUpdates++
			tradeIndex++
		}
	}
	observePrivateEventsBefore := func(before time.Time) {
		for privateEventIndex < len(privateEvents) && privateEvents[privateEventIndex].At.Before(before) {
			s.observePrivateFillCalibrationLedgerEvent(privateEvents[privateEventIndex])
			stats.PrivateCalibrationEvents++
			privateEventIndex++
		}
	}
	for _, filename := range files {
		if err := s.replayMakerBBOFile(filename, bboCutoff, now, deltaOnly, config, &stats, observeTradesBefore, observePrivateEventsBefore); err != nil {
			return stats, err
		}
	}
	// Consume private events that arrived after the last captured BBO. Their
	// adverse labels remain pending until the next live BBO; using a future
	// price here would violate causality.
	observePrivateEventsBefore(now.Add(time.Nanosecond))
	if size := privateFillLedgerSize(config.PrivateOrderFillLedger, s.Symbol); size >= 0 {
		s.makerPrivateLedgerReplayOffset = size
	}
	if tradeIndex < len(trades) {
		s.makerStartupPendingTrades = append(
			s.makerStartupPendingTrades[:0], trades[tradeIndex:]...)
		stats.PendingTrades = len(s.makerStartupPendingTrades)
	}
	return stats, nil
}

func (s *Strategy) loadMakerStartupTrades(cutoff, now time.Time, deltaOnly bool) ([]makerStartupTrade, int, error) {
	files, err := s.fastEvidenceCaptureFiles("trades")
	if err != nil {
		return nil, 0, err
	}
	files = captureFilesOverlapping(files, s.Symbol, "trades", cutoff, now)
	if deltaOnly {
		files = captureFilesChangedSinceCheckpoint(files, s.makerCheckpointCaptureFiles)
	}
	trades := make([]makerStartupTrade, 0, 4096)
	seenTradeIDs := make(map[uint64]struct{})
	for _, filename := range files {
		file, openErr := os.Open(filename)
		if openErr != nil {
			return nil, len(files), fmt.Errorf("open Binance trade capture %s: %w", filename, openErr)
		}
		reader, readErr := newIndexedCaptureReader(file, filename, cutoff)
		if readErr != nil {
			_ = file.Close()
			return nil, len(files), fmt.Errorf("read Binance trade capture header %s: %w", filename, readErr)
		}
		for {
			record, rowErr := reader.Read()
			if rowErr == io.EOF {
				break
			}
			if rowErr != nil || len(record) < 6 {
				continue
			}
			when, parseErr := time.Parse(time.RFC3339Nano, record[0])
			if parseErr != nil || when.Before(cutoff) || deltaOnly && !when.After(cutoff) || when.After(now) {
				continue
			}
			id, idErr := strconv.ParseUint(record[2], 10, 64)
			price, priceErr := fixedpoint.NewFromString(record[3])
			quantity, quantityErr := fixedpoint.NewFromString(record[4])
			side, sideErr := types.StrToSideType(record[5])
			if idErr != nil || priceErr != nil || quantityErr != nil || sideErr != nil ||
				price.Sign() <= 0 || quantity.Sign() <= 0 {
				continue
			}
			if id != 0 {
				if _, found := seenTradeIDs[id]; found {
					continue
				}
				seenTradeIDs[id] = struct{}{}
			}
			trades = append(trades, makerStartupTrade{when: when, trade: types.Trade{
				ID: id, Symbol: s.Symbol, Price: price, Quantity: quantity,
				QuoteQuantity: price.Mul(quantity), Side: side, Time: types.Time(when),
			}})
		}
		_ = file.Close()
	}
	sort.SliceStable(trades, func(i, j int) bool { return trades[i].when.Before(trades[j].when) })
	return trades, len(files), nil
}

func (s *Strategy) replayMakerBBOFile(
	filename string,
	cutoff, now time.Time,
	deltaOnly bool,
	config MarketMakerConfig,
	stats *makerStartupWarmupStats,
	observeTradesBefore func(time.Time),
	observePrivateEventsBefore func(time.Time),
) error {
	file, err := os.Open(filename)
	if err != nil {
		return fmt.Errorf("open Binance BBO capture %s: %w", filename, err)
	}
	defer file.Close()
	reader, err := newIndexedCaptureReader(file, filename, cutoff)
	if err != nil {
		return fmt.Errorf("read Binance BBO capture header %s: %w", filename, err)
	}
	compactWarmup := !deltaOnly
	var compacted makerStartupBBOAccumulator
	if compactWarmup {
		compacted.emit = func(at time.Time, ticker types.BookTicker, gapBefore bool) {
			observeTradesBefore(at)
			observePrivateEventsBefore(at)
			s.observeRelativeHoldRiskPreloadBBO(at, ticker)
			s.observeMakerReplayBBO(at, ticker, gapBefore, config)
			if stats.First.IsZero() {
				stats.First = at
			}
			stats.Last = at
			stats.BBOUpdates++
		}
	}
	for {
		record, readErr := reader.Read()
		if readErr == io.EOF {
			if compactWarmup {
				compacted.flush()
			}
			return nil
		}
		if readErr != nil || len(record) < 5 {
			// A rotating collector can be appending the trailing row.
			continue
		}
		when, parseErr := time.Parse(time.RFC3339Nano, record[0])
		if parseErr != nil || when.Before(cutoff) || deltaOnly && !when.After(cutoff) || when.After(now) ||
			(!s.State.LastReferenceTime.IsZero() && !when.After(s.State.LastReferenceTime)) {
			continue
		}
		bid, bidErr := fixedpoint.NewFromString(record[1])
		bidSize, bidSizeErr := fixedpoint.NewFromString(record[2])
		ask, askErr := fixedpoint.NewFromString(record[3])
		askSize, askSizeErr := fixedpoint.NewFromString(record[4])
		if bidErr != nil || bidSizeErr != nil || askErr != nil || askSizeErr != nil ||
			bid.Sign() <= 0 || ask.Sign() <= 0 || ask.Compare(bid) <= 0 {
			continue
		}
		gapBefore := !s.State.LastReferenceTime.IsZero() && when.Sub(s.State.LastReferenceTime) >= marketMakerHorizonGapThreshold
		if len(record) >= 6 {
			if milliseconds, gapErr := strconv.ParseInt(record[5], 10, 64); gapErr == nil &&
				time.Duration(milliseconds)*time.Millisecond >= marketMakerHorizonGapThreshold {
				gapBefore = true
			}
		}
		ticker := types.BookTicker{Symbol: s.Symbol, Buy: bid, BuySize: bidSize, Sell: ask, SellSize: askSize}
		if compactWarmup {
			compacted.add(when, ticker, gapBefore)
			continue
		}
		observeTradesBefore(when)
		observePrivateEventsBefore(when)
		s.observeRelativeHoldRiskPreloadBBO(when, ticker)
		s.observeMakerReplayBBO(when, ticker, gapBefore, config)
		if stats.First.IsZero() {
			stats.First = when
		}
		stats.Last = when
		stats.BBOUpdates++
	}
}

func (s *Strategy) observeMakerReplayTrade(event makerStartupTrade, config MarketMakerConfig) {
	s.observeFastEvidenceTrade(event.when, event.trade)
	if config.VolumeProfile.Enabled {
		s.makerHorizonModel.ObservePublicTrade(
			event.when, event.trade.Price.Float64(), event.trade.Quantity.Float64(),
			event.trade.Side == types.SideTypeBuy, config)
	}
	if event.when.After(s.makerLastPublicTradeAt) {
		s.makerLastPublicTradeAt = event.when
	}
}

func (s *Strategy) drainMakerStartupTrades(before time.Time, config MarketMakerConfig) {
	if len(s.makerStartupPendingTrades) == 0 {
		return
	}
	consumed := 0
	for consumed < len(s.makerStartupPendingTrades) && s.makerStartupPendingTrades[consumed].when.Before(before) {
		s.observeMakerReplayTrade(s.makerStartupPendingTrades[consumed], config)
		consumed++
	}
	if consumed == 0 {
		return
	}
	copy(s.makerStartupPendingTrades, s.makerStartupPendingTrades[consumed:])
	s.makerStartupPendingTrades = s.makerStartupPendingTrades[:len(s.makerStartupPendingTrades)-consumed]
}

func (s *Strategy) observeMakerReplayBBO(at time.Time, ticker types.BookTicker, gapBefore bool, config MarketMakerConfig) {
	bid, ask := ticker.Buy.Float64(), ticker.Sell.Float64()
	mid := (bid + ask) / 2
	// Keep startup replay identical to the live causal source: pivot Klines are
	// built from BBO/2, never from one executable side and never from a future
	// label. This runs before the first live quote is allowed.
	s.observeMakerCausalKlinePivot(at, mid, config)
	// The pivot-regime source of truth is also midpoint based and must be
	// advanced during startup prefill. Otherwise enabling the target after a
	// restart would leave it cold until enough new BBO events arrived.
	if config.DynamicInventoryAim.PivotRegimeTarget.Enabled ||
		config.DynamicInventoryAim.PivotRegimeTarget.CausalCEEnabled {
		s.observeMakerPivotRegime(at, mid, config)
	}
	if s.makerPrivateFillCalibration != nil {
		// Ledger events strictly before this BBO have already been replayed by
		// replayMakerBBOFile. This event is therefore the next causal executable
		// price and can mature labels at or before its timestamp.
		s.makerPrivateFillCalibration.ObserveBBO(at, bid, ask)
	}
	s.makerHorizonModel.ObserveBookWithSizesAndGap(
		at, bid, ticker.BuySize.Float64(), ask, ticker.SellSize.Float64(),
		config, gapBefore)
	if config.AsymmetricOscillationRisk.Enabled {
		windows := config.FastModelWindows()
		if len(windows) == 0 {
			windows = []time.Duration{time.Duration(config.FastWindow)}
		}
		// Warm every causal online alpha used by the live quote loop. The
		// selected Fast horizon is unavailable during raw replay, so each
		// configured window gets its own pending label and variance state.
		for _, riskHorizon := range windows {
			s.observeAsymmetricOscillationRisk(at, bid, ask, riskHorizon, gapBefore)
		}
	}
	if s.makerBOCPD45 != nil {
		s.makerBOCPD45.Observe(at, bid, ask, gapBefore)
	}
	if config.MacroInventory.Enabled {
		s.makerMacroInventoryModel.ObserveBBO(at, mid, bid, ask, gapBefore, config.MacroInventory)
		if s.makerExecutableCrossingModel != nil {
			s.makerExecutableCrossingModel.Observe(at, bid, ask, gapBefore)
		}
	}
	price := mid
	if value, ok := microprice(ticker); ok {
		price = value.Float64()
	}
	s.model.Observe(at, gapBefore)
	s.observeFastModelExposure(at, gapBefore)
	if gapBefore {
		s.State.Engine.Reset(price)
	} else {
		for _, event := range s.State.Engine.Update(s.Symbol, fixedpoint.NewFromFloat(price), at, at, 0) {
			s.model.Update(event)
			s.updateFastModels(event)
			s.updateMakerDirectionModels(event)
		}
	}
	s.observeFastDriftModels(at, ticker, gapBefore, config)
	s.observeFastEvidenceBBO(at, ticker)
	s.State.LastReferenceTime = at
}
