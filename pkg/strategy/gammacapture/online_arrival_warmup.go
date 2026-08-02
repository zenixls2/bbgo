package gammacapture

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/c9s/bbgo/pkg/fixedpoint"
	"github.com/c9s/bbgo/pkg/types"
)

type onlineArrivalWarmupStats struct {
	Files               int
	BBOUpdates          int
	First               time.Time
	Last                time.Time
	MinimumWindows      float64
	ModelHealth         ModelHealth
	CrossingUp          int
	CrossingDown        int
	DirectionSamples    float64
	FastActivity        FastCrossingActivity
	FastRateUsable      bool
	DirectionConfidence float64
}

// warmOnlineArrivalFromBinanceBBO reconstructs every in-memory maker learner
// from the local Binance book-ticker capture before order handling starts. The
// persisted arrival state has a per-horizon resolution cursor, so replaying an
// overlapping archive advances only newly completed windows.
func (s *Strategy) warmOnlineArrivalFromBinanceBBO(now time.Time) error {
	if !s.MarketMaker.Enabled || !s.MarketMaker.OnlineArrival.Enabled {
		return nil
	}
	if now.IsZero() || s.model == nil || (s.fastModel == nil && len(s.fastModels) == 0) ||
		s.State == nil || s.State.OnlineArrival == nil {
		return fmt.Errorf("online arrival startup replay is not initialized")
	}
	config := s.MarketMaker
	config.setDefaults()
	lookback := time.Duration(config.OnlineArrival.StartupLookback)
	maxAge := time.Duration(config.OnlineArrival.StartupMaxAge)
	if lookback <= 0 || maxAge <= 0 {
		return fmt.Errorf("online arrival startup lookback and max age must be positive")
	}
	files, err := s.binanceBBOCaptureFiles()
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("Binance BBO capture is empty for %s under %s and %s", s.Symbol, s.AggTradeWarmup.LivePath, filepath.Join(s.AggTradeWarmup.Path, s.Symbol))
	}

	cutoff := now.Add(-lookback)
	engine := NewCrossingEngine(s.Barrier.Width, time.Duration(s.Barrier.MinDwell), s.Barrier.MaxCrossingsPerEvent)
	stats := onlineArrivalWarmupStats{Files: len(files)}
	for _, filename := range files {
		if err := s.replayBinanceBBOFile(filename, cutoff, now, engine, config, &stats); err != nil {
			return err
		}
	}
	if stats.BBOUpdates == 0 {
		return fmt.Errorf("no valid Binance BBO observations in startup window %s to %s", cutoff, now)
	}
	if age := now.Sub(stats.Last); age > maxAge {
		return fmt.Errorf("Binance BBO startup history is stale: last=%s age=%s maxAge=%s", stats.Last, age, maxAge)
	}

	s.State.Engine = engine
	s.State.LastReferenceTime = stats.Last
	snapshot := s.model.Snapshot(stats.Last)
	fast := s.adaptiveFastSnapshot(stats.Last)
	fastInference := inferFastCrossing(fast.Window, fast.Model, fast.Evidence, snapshot)
	stats.ModelHealth = snapshot.Health
	stats.CrossingUp = snapshot.Up
	stats.CrossingDown = snapshot.Down
	stats.DirectionSamples = float64(fast.Model.Up + fast.Model.Down)
	stats.FastActivity = fastInference.Activity
	stats.FastRateUsable = fastInference.RateUsable
	stats.DirectionConfidence = fastInference.DirectionConfidence

	minimumWindows, ready := s.onlineArrivalStartupCoverage(config)
	stats.MinimumWindows = minimumWindows
	if !ready {
		return fmt.Errorf("Binance BBO startup history has insufficient completed horizon windows: effective=%.2f required=%d", minimumWindows, config.HorizonMinSamples)
	}
	log.WithFields(map[string]interface{}{
		"symbol":                   s.Symbol,
		"captureFiles":             stats.Files,
		"bboUpdates":               stats.BBOUpdates,
		"firstBBO":                 stats.First,
		"lastBBO":                  stats.Last,
		"minimumEffectiveWindows":  stats.MinimumWindows,
		"requiredEffectiveWindows": config.HorizonMinSamples,
		"modelHealth":              stats.ModelHealth,
		"crossingUp":               stats.CrossingUp,
		"crossingDown":             stats.CrossingDown,
		"directionSamples":         stats.DirectionSamples,
		"fastActivity":             stats.FastActivity,
		"fastRateUsable":           stats.FastRateUsable,
		"directionConfidence":      stats.DirectionConfidence,
	}).Info("warmed gamma-capture online models from Binance BBO capture")
	return nil
}

func (s *Strategy) binanceBBOCaptureFiles() ([]string, error) {
	roots := []string{
		s.AggTradeWarmup.LivePath,
		filepath.Join(s.AggTradeWarmup.Path, s.Symbol),
	}
	seen := make(map[string]struct{})
	var files []string
	for _, root := range roots {
		matches, err := filepath.Glob(filepath.Join(root, s.Symbol+"-bookticker-*.csv"))
		if err != nil {
			return nil, fmt.Errorf("list Binance BBO capture files under %s: %w", root, err)
		}
		for _, filename := range matches {
			if _, ok := seen[filename]; ok {
				continue
			}
			seen[filename] = struct{}{}
			files = append(files, filename)
		}
	}
	sort.Strings(files)
	return files, nil
}

func (s *Strategy) replayBinanceBBOFile(
	filename string,
	cutoff, now time.Time,
	engine *CrossingEngine,
	config MarketMakerConfig,
	stats *onlineArrivalWarmupStats,
) error {
	file, err := os.Open(filename)
	if err != nil {
		return fmt.Errorf("open Binance BBO capture %s: %w", filename, err)
	}
	defer file.Close()
	reader := csv.NewReader(file)
	if _, err := reader.Read(); err != nil {
		return fmt.Errorf("read Binance BBO capture header %s: %w", filename, err)
	}
	for {
		record, readErr := reader.Read()
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil || len(record) < 5 {
			// The collector can append while startup is reading. A partial
			// trailing record is ignored; the live stream is authoritative.
			continue
		}
		when, parseErr := time.Parse(time.RFC3339Nano, record[0])
		if parseErr != nil || when.Before(cutoff) || when.After(now) ||
			(!stats.Last.IsZero() && !when.After(stats.Last)) {
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
		gapBefore := !stats.Last.IsZero() && when.Sub(stats.Last) >= marketMakerHorizonGapThreshold
		if len(record) >= 6 {
			if milliseconds, gapErr := strconv.ParseInt(record[5], 10, 64); gapErr == nil &&
				time.Duration(milliseconds)*time.Millisecond >= marketMakerHorizonGapThreshold {
				gapBefore = true
			}
		}
		ticker := types.BookTicker{
			Symbol: s.Symbol,
			Buy:    bid, BuySize: bidSize,
			Sell: ask, SellSize: askSize,
		}
		mid := (bid.Float64() + ask.Float64()) / 2
		s.makerHorizonModel.ObserveBookWithGap(when, bid.Float64(), ask.Float64(), config, gapBefore)

		price := mid
		if value, ok := microprice(ticker); ok {
			price = value.Float64()
		}
		s.model.Observe(when, gapBefore)
		s.observeFastModelExposure(when, gapBefore)
		if gapBefore {
			engine.Reset(price)
		} else {
			for _, event := range engine.Update(s.Symbol, fixedpoint.NewFromFloat(price), when, when, 0) {
				s.model.Update(event)
				s.updateFastModels(event)
				s.updateMakerDirectionModels(event)
			}
		}
		if stats.First.IsZero() {
			stats.First = when
		}
		stats.Last = when
		stats.BBOUpdates++
	}
}

func (s *Strategy) onlineArrivalStartupCoverage(config MarketMakerConfig) (float64, bool) {
	if s.State == nil || s.State.OnlineArrival == nil {
		return 0, false
	}
	config.setDefaults()
	buckets := onlineArrivalDistanceBuckets(config)
	if len(buckets) == 0 {
		return 0, false
	}
	minimum := 0.0
	s.State.OnlineArrival.mu.RLock()
	defer s.State.OnlineArrival.mu.RUnlock()
	for index, horizon := range config.TradingHorizons() {
		cell := s.State.OnlineArrival.Cells[onlineArrivalCellKey(horizon, buckets[0])]
		if cell == nil {
			return 0, false
		}
		windows := cell.SlowWindows
		if index == 0 || windows < minimum {
			minimum = windows
		}
		if windows < float64(config.HorizonMinSamples) {
			return minimum, false
		}
	}
	return minimum, true
}
