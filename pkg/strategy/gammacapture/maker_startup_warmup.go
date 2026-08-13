package gammacapture

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/c9s/bbgo/pkg/fixedpoint"
	"github.com/c9s/bbgo/pkg/types"
)

type makerStartupWarmupStats struct {
	Files      int
	BBOUpdates int
	First      time.Time
	Last       time.Time
}

// restoreAndWarmMakerModelsFromBinanceCapture restores the bounded live
// checkpoint and advances it through only the capture delta. It is deliberately
// independent of the retired online-arrival learner: crossing, horizon,
// side-volatility, Macro and fast-evidence models are all production state and
// must survive a live restart even when aggTradeWarmup is disabled.
//
// If a compatible checkpoint is unavailable, the same causal BBO pipeline is
// rebuilt from a bounded local-capture interval. Orders are reconciled only
// after this method returns, so a restart never cancels the previous quotes and
// then waits cold for model evidence.
func (s *Strategy) restoreAndWarmMakerModelsFromBinanceCapture(now time.Time) error {
	if !s.MarketMaker.Enabled {
		return nil
	}
	if now.IsZero() || s.State == nil || s.model == nil || s.State.Engine == nil {
		return fmt.Errorf("market-maker startup replay is not initialized")
	}

	lookback := s.makerStartupWarmupLookback()
	cursor, restored, restoreErr := s.restoreModelCheckpoint(now)
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
	}

	stats, err := s.replayMakerBBOCapture(cursor, now, restored)
	if err != nil {
		return err
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

	if s.fastEvidence != nil || len(s.fastEvidenceModels) > 0 {
		var evidenceErr error
		if restored {
			evidenceErr = s.warmFastEvidenceFromCapture(now, cursor)
		} else {
			evidenceErr = s.warmFastEvidenceFromCapture(now)
		}
		if evidenceErr != nil {
			return fmt.Errorf("warm fast evidence from capture: %w", evidenceErr)
		}
	}

	s.makerCheckpointReplayAfter = last
	snapshot := s.model.Snapshot(now)
	fast := s.adaptiveFastSnapshot(now)
	bocpd45 := BOCPD45Snapshot{}
	if s.makerBOCPD45 != nil {
		bocpd45 = s.makerBOCPD45.Snapshot()
	}
	log.WithFields(map[string]interface{}{
		"symbol":             s.Symbol,
		"checkpointRestored": restored,
		"replayAfter":        cursor,
		"captureFiles":       stats.Files,
		"bboUpdates":         stats.BBOUpdates,
		"firstBBO":           stats.First,
		"lastBBO":            stats.Last,
		"slowHealth":         snapshot.Health,
		"slowUp":             snapshot.Up,
		"slowDown":           snapshot.Down,
		"selectedFastWindow": fast.Window,
		"fastHealth":         fast.Model.Health,
		"fastUp":             fast.Model.Up,
		"fastDown":           fast.Model.Down,
		"fastEvidenceHealth": fast.Evidence.Health,
		"bocpd45Enabled":     bocpd45.Enabled, "bocpd45Ready": bocpd45.Ready,
		"bocpd45Calibration":        bocpd45.Calibration,
		"bocpd45CalibrationReady":   bocpd45.CalibrationReady,
		"bocpd45CalibrationSamples": bocpd45.CalibrationSamples,
		"bocpd45MaturedLabels":      bocpd45.MaturedLabels,
	}).Info("restored and warmed gamma-capture models from Binance capture")
	return nil
}

// makerStartupWarmupLookback is the maximum interval that can affect the
// rolling Fast/slow crossing and executable-BBO horizon estimators. Macro's
// long history is normally retained by checkpoint; on first start it grows
// causally from the bounded reconstruction rather than forcing a many-day raw
// scan before quoting.
func (s *Strategy) makerStartupWarmupLookback() time.Duration {
	lookback := time.Duration(s.AggTradeWarmup.Lookback)
	if window := time.Duration(s.Intensity.Window); window > lookback {
		lookback = window
	}
	config := s.MarketMaker
	config.setDefaults()
	if window := time.Duration(config.HorizonLookback) + time.Duration(config.MaxTradingWindow) + time.Minute; window > lookback {
		lookback = window
	}
	if window := s.maxFastEvidenceWindow(); window > lookback {
		lookback = window
	}
	if config.BOCPD45.Enabled {
		window := time.Duration(config.BOCPD45.CalibrationWindow) +
			time.Duration(config.BOCPD45.Horizon)
		if window > lookback {
			lookback = window
		}
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
}

func (s *Strategy) replayMakerBBOCapture(cutoff, now time.Time, deltaOnly bool) (makerStartupWarmupStats, error) {
	files, err := s.binanceBBOCaptureFiles()
	if err != nil {
		return makerStartupWarmupStats{}, err
	}
	if len(files) == 0 {
		return makerStartupWarmupStats{}, fmt.Errorf("Binance BBO capture is empty for %s", s.Symbol)
	}
	files = captureFilesOverlapping(files, s.Symbol, "bookticker", cutoff, now)
	if deltaOnly {
		files = captureFilesChangedSinceCheckpoint(files, s.makerCheckpointCaptureFiles)
	}
	stats := makerStartupWarmupStats{Files: len(files)}
	config := s.MarketMaker
	config.setDefaults()
	for _, filename := range files {
		if err := s.replayMakerBBOFile(filename, cutoff, now, deltaOnly, config, &stats); err != nil {
			return stats, err
		}
	}
	return stats, nil
}

func (s *Strategy) replayMakerBBOFile(
	filename string,
	cutoff, now time.Time,
	deltaOnly bool,
	config MarketMakerConfig,
	stats *makerStartupWarmupStats,
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
	for {
		record, readErr := reader.Read()
		if readErr == io.EOF {
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
		s.observeMakerReplayBBO(when, ticker, gapBefore, config)
		if stats.First.IsZero() {
			stats.First = when
		}
		stats.Last = when
		stats.BBOUpdates++
	}
}

func (s *Strategy) observeMakerReplayBBO(at time.Time, ticker types.BookTicker, gapBefore bool, config MarketMakerConfig) {
	bid, ask := ticker.Buy.Float64(), ticker.Sell.Float64()
	mid := (bid + ask) / 2
	s.makerHorizonModel.ObserveBookWithGap(at, bid, ask, config, gapBefore)
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
	s.State.LastReferenceTime = at
}
