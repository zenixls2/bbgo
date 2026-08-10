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

	"github.com/c9s/bbgo/pkg/datasource/csvsource"
	"github.com/c9s/bbgo/pkg/fixedpoint"
	"github.com/c9s/bbgo/pkg/types"
)

type warmupTrade struct {
	id        uint64
	when      time.Time
	price     float64
	gapBefore time.Duration
}

const warmupGapThreshold = 5 * time.Second

// warmModelFromAggTrades seeds the causal crossing/intensity models and the
// market-maker's public-price horizon model. It intentionally does not replay
// signal hysteresis or create orders: those decisions must begin from the live
// stream after the preload boundary. The maker model is seeded from public
// aggregate trades, never from our own fills, so a new symbol can bootstrap
// without an execution history.
func (s *Strategy) warmModelFromAggTrades(now time.Time) error {
	if !s.AggTradeWarmup.Enabled {
		return nil
	}
	if s.AggTradeWarmup.Lookback <= 0 || s.AggTradeWarmup.MaxAge <= 0 {
		return fmt.Errorf("aggTradeWarmup lookback and maxAge must be positive")
	}
	root := filepath.Join(s.AggTradeWarmup.Path, "binance", s.Symbol, "aggTrades")
	directCaptureFiles := false
	files, err := filepath.Glob(filepath.Join(root, "*.csv"))
	if err != nil {
		return fmt.Errorf("list aggregate-trade files: %w", err)
	}
	// The userspace capture service writes the same causal CSV schema to
	// <path>/<symbol> rather than the Vision archive hierarchy. Accept both
	// layouts so a newly selected symbol can warm without a manual copy.
	if len(files) == 0 {
		root = filepath.Join(s.AggTradeWarmup.Path, s.Symbol)
		directCaptureFiles = true
		files, err = filepath.Glob(filepath.Join(root, s.Symbol+"-trades-*.csv"))
		if err != nil {
			return fmt.Errorf("list direct aggregate-trade files: %w", err)
		}
	}
	sort.Strings(files)
	liveFiles, liveErr := filepath.Glob(filepath.Join(s.AggTradeWarmup.LivePath, s.Symbol+"-trades-*.csv"))
	if liveErr != nil {
		return fmt.Errorf("list live aggregate-trade files: %w", liveErr)
	}
	files = append(files, liveFiles...)
	if len(files) == 0 {
		return fmt.Errorf("aggregate-trade warmup archive is empty: %s", root)
	}
	cutoff := now.Add(-time.Duration(s.AggTradeWarmup.Lookback))
	trades := make([]warmupTrade, 0, 4096)
	for _, filename := range files {
		if directCaptureFiles || filepath.Dir(filename) == s.AggTradeWarmup.LivePath {
			if err := readLiveWarmupFile(filename, cutoff, now, &trades); err != nil {
				return err
			}
			continue
		}
		file, openErr := os.Open(filename)
		if openErr != nil {
			return fmt.Errorf("open aggregate-trade file %s: %w", filename, openErr)
		}
		reader := csvsource.NewCSVTickReader(csv.NewReader(file))
		for {
			tick, readErr := reader.Read()
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				_ = file.Close()
				return fmt.Errorf("read aggregate-trade file %s: %w", filename, readErr)
			}
			when := tick.Timestamp.Time()
			if when.Before(cutoff) || when.After(now) || tick.Price.Sign() <= 0 {
				continue
			}
			trades = append(trades, warmupTrade{id: tick.TradeID, when: when, price: tick.Price.Float64()})
		}
		if closeErr := file.Close(); closeErr != nil {
			return fmt.Errorf("close aggregate-trade file %s: %w", filename, closeErr)
		}
	}
	if len(trades) == 0 {
		return fmt.Errorf("no aggregate trades in warmup window %s to %s", cutoff, now)
	}
	sort.SliceStable(trades, func(i, j int) bool { return trades[i].when.Before(trades[j].when) })
	last := time.Time{}
	var lastID uint64
	engine := NewCrossingEngine(s.Barrier.Width, time.Duration(s.Barrier.MinDwell), s.Barrier.MaxCrossingsPerEvent)
	for _, trade := range trades {
		if trade.id != 0 && trade.id == lastID {
			continue
		}
		if !last.IsZero() && trade.when.Before(last) {
			continue
		}
		gapBefore := trade.gapBefore >= warmupGapThreshold
		if s.MarketMaker.Enabled {
			s.makerHorizonModel.ObserveWithGap(trade.when, trade.price, s.MarketMaker, gapBefore)
		}
		s.model.Observe(trade.when, gapBefore)
		s.observeFastModelExposure(trade.when, gapBefore)
		if gapBefore {
			// The path through a capture outage is unknown. Re-anchor and wait
			// for the next observed move instead of counting a synthetic crossing
			// across the missing interval.
			engine.Reset(trade.price)
			last = trade.when
			lastID = trade.id
			continue
		}
		for _, event := range engine.Update(s.Symbol, fixedpoint.NewFromFloat(trade.price), trade.when, trade.when, 0) {
			s.model.Update(event)
			s.updateFastModels(event)
			s.updateMakerDirectionModels(event)
		}
		last = trade.when
		lastID = trade.id
	}
	snapshot := s.model.Snapshot(last)
	if now.Sub(last) > time.Duration(s.AggTradeWarmup.MaxAge) {
		return fmt.Errorf("aggregate-trade warmup is stale: last=%s age=%s maxAge=%v", last, now.Sub(last), s.AggTradeWarmup.MaxAge)
	}
	if s.AggTradeWarmup.RequireHealthy && snapshot.Health != HealthHealthy {
		// Passive market making can start while the directional intensity model
		// is still collecting evidence. It does not open directional positions;
		// the directional process and inventory reset remain gated on HEALTHY.
		if !s.MarketMaker.Enabled {
			return fmt.Errorf("aggregate-trade warmup is not healthy: health=%s up=%d down=%d observed=%s", snapshot.Health, snapshot.Up, snapshot.Down, snapshot.Observed)
		}
		log.WithFields(map[string]interface{}{"symbol": s.Symbol, "health": snapshot.Health, "up": snapshot.Up, "down": snapshot.Down, "observed": snapshot.Observed}).Warn("market-maker continuing with non-healthy aggregate-trade warmup")
	}
	// A flat strategy can safely replace its persisted crossing cursor with the
	// causally reconstructed one. If a position survived a restart, keep its
	// execution cursor and let the next live event advance it; replacing it
	// could alter the position's persisted grid context.
	if s.Position == nil || s.Position.IsDust(fixedpoint.NewFromFloat(trades[len(trades)-1].price)) {
		s.State.Engine = engine
	}
	s.State.LastDecision = fmt.Sprintf("aggTrade warmup complete: trades=%d last=%s health=%s", len(trades), last.UTC().Format(time.RFC3339), snapshot.Health)
	if snapshot.Health == HealthHealthy {
		s.State.Runtime = StateArmedLong
	} else {
		s.State.Runtime = StateWarmingUp
	}
	log.WithFields(map[string]interface{}{
		"symbol":              s.Symbol,
		"warmupTrades":        len(trades),
		"lastTrade":           last,
		"modelHealth":         snapshot.Health,
		"makerHorizonSamples": len(s.makerHorizonModel.points),
	}).Info("warmed gamma-capture model from aggregate trades")
	return nil
}

type fastEvidenceWarmupEvent struct {
	when  time.Time
	trade *types.Trade
	book  *types.BookTicker
}

// warmFastEvidenceFromCapture restores only the recent causal raw trade/BBO
// coverage window from the live capture files. It deliberately does not warm
// the crossing model: that model has its own aggregate-trade warmup and a
// separate gap-aware path. Capture files can be rotated while this runs, so
// malformed or partially written rows are skipped and the live stream remains
// authoritative afterwards.
func (s *Strategy) warmFastEvidenceFromCapture(now time.Time, replayAfter ...time.Time) error {
	if s.fastEvidence == nil && len(s.fastEvidenceModels) == 0 {
		return nil
	}
	window := s.maxFastEvidenceWindow()
	if window <= 0 {
		return fmt.Errorf("fast evidence window must be positive")
	}
	cutoff := now.Add(-window)
	var deltaCursor time.Time
	if len(replayAfter) > 0 && replayAfter[0].After(cutoff) {
		deltaCursor = replayAfter[0]
		cutoff = replayAfter[0]
	}
	tradeFiles, err := s.fastEvidenceCaptureFiles("trades")
	if err != nil {
		return err
	}
	bookFiles, err := s.fastEvidenceCaptureFiles("bookticker")
	if err != nil {
		return err
	}
	if len(tradeFiles) == 0 && len(bookFiles) == 0 {
		return fmt.Errorf("no live capture files for %s under %s", s.Symbol, s.AggTradeWarmup.LivePath)
	}
	tradeFiles = captureFilesOverlapping(tradeFiles, s.Symbol, "trades", cutoff, now)
	tradeFiles = captureFilesChangedSinceCheckpoint(tradeFiles, s.makerCheckpointCaptureFiles)
	bookFiles = captureFilesChangedSinceCheckpoint(bookFiles, s.makerCheckpointCaptureFiles)
	bookFiles = captureFilesOverlapping(bookFiles, s.Symbol, "bookticker", cutoff, now)

	events := make([]fastEvidenceWarmupEvent, 0, 4096)
	seenTradeIDs := make(map[uint64]struct{})
	for _, filename := range tradeFiles {
		file, openErr := os.Open(filename)
		if openErr != nil {
			continue
		}
		reader, readErr := newIndexedCaptureReader(file, filename, cutoff)
		if readErr != nil {
			_ = file.Close()
			continue
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
			if parseErr != nil || when.Before(cutoff) || !deltaCursor.IsZero() && !when.After(deltaCursor) || when.After(now) {
				continue
			}
			id, idErr := strconv.ParseUint(record[2], 10, 64)
			price, priceErr := fixedpoint.NewFromString(record[3])
			quantity, quantityErr := fixedpoint.NewFromString(record[4])
			side, sideErr := types.StrToSideType(record[5])
			if idErr != nil || priceErr != nil || quantityErr != nil || sideErr != nil || price.Sign() <= 0 || quantity.Sign() <= 0 {
				continue
			}
			if id != 0 {
				if _, exists := seenTradeIDs[id]; exists {
					continue
				}
				seenTradeIDs[id] = struct{}{}
			}
			trade := &types.Trade{
				ID:            id,
				Price:         price,
				Quantity:      quantity,
				QuoteQuantity: price.Mul(quantity),
				Symbol:        s.Symbol,
				Side:          side,
				Time:          types.Time(when),
			}
			events = append(events, fastEvidenceWarmupEvent{when: when, trade: trade})
		}
		_ = file.Close()
	}

	seenBooks := make(map[string]struct{})
	for _, filename := range bookFiles {
		file, openErr := os.Open(filename)
		if openErr != nil {
			continue
		}
		reader, readErr := newIndexedCaptureReader(file, filename, cutoff)
		if readErr != nil {
			_ = file.Close()
			continue
		}
		for {
			record, rowErr := reader.Read()
			if rowErr == io.EOF {
				break
			}
			if rowErr != nil || len(record) < 5 {
				continue
			}
			when, parseErr := time.Parse(time.RFC3339Nano, record[0])
			if parseErr != nil || when.Before(cutoff) || !deltaCursor.IsZero() && !when.After(deltaCursor) || when.After(now) {
				continue
			}
			bid, bidErr := fixedpoint.NewFromString(record[1])
			bidSize, bidSizeErr := fixedpoint.NewFromString(record[2])
			ask, askErr := fixedpoint.NewFromString(record[3])
			askSize, askSizeErr := fixedpoint.NewFromString(record[4])
			if bidErr != nil || bidSizeErr != nil || askErr != nil || askSizeErr != nil || bid.Sign() <= 0 || ask.Sign() <= 0 || ask.Compare(bid) <= 0 {
				continue
			}
			key := fmt.Sprintf("%s|%s|%s|%s|%s", when.UTC().Format(time.RFC3339Nano), bid, bidSize, ask, askSize)
			if _, exists := seenBooks[key]; exists {
				continue
			}
			seenBooks[key] = struct{}{}
			book := &types.BookTicker{Symbol: s.Symbol, Buy: bid, BuySize: bidSize, Sell: ask, SellSize: askSize}
			events = append(events, fastEvidenceWarmupEvent{when: when, book: book})
		}
		_ = file.Close()
	}
	if len(events) == 0 {
		if !deltaCursor.IsZero() {
			return nil
		}
		return fmt.Errorf("no recent capture events for %s in %s", s.Symbol, window)
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].when.Before(events[j].when) })
	for _, event := range events {
		if event.trade != nil {
			s.observeFastEvidenceTrade(event.when, *event.trade)
		}
		if event.book != nil {
			s.observeFastEvidenceBBO(event.when, *event.book)
		}
	}
	warmSnapshot := s.adaptiveFastSnapshot(now)
	log.WithFields(map[string]interface{}{
		"symbol":             s.Symbol,
		"window":             window,
		"selectedFastWindow": warmSnapshot.Window,
		"windowHealths":      warmSnapshot.HealthSummary,
		"tradeCount":         warmSnapshot.Evidence.TradeCount,
		"bboCount":           warmSnapshot.Evidence.BBOCount,
		"health":             warmSnapshot.Evidence.Health,
		"observed":           warmSnapshot.Evidence.Observed,
		"tradeFiles":         len(tradeFiles),
		"bookFiles":          len(bookFiles),
	}).Info("warmed fast evidence from capture")
	return nil
}

func (s *Strategy) fastEvidenceCaptureFiles(stream string) ([]string, error) {
	roots := []string{
		s.AggTradeWarmup.LivePath,
		filepath.Join(s.AggTradeWarmup.Path, s.Symbol),
	}
	seen := make(map[string]struct{})
	var files []string
	for _, root := range roots {
		matches, err := filepath.Glob(filepath.Join(root, s.Symbol+"-"+stream+"-*.csv"))
		if err != nil {
			return nil, fmt.Errorf("list %s capture files under %s: %w", stream, root, err)
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

func readLiveWarmupFile(filename string, cutoff, now time.Time, trades *[]warmupTrade) error {
	file, err := os.Open(filename)
	if err != nil {
		return fmt.Errorf("open live aggregate-trade file %s: %w", filename, err)
	}
	defer file.Close()
	reader, err := newIndexedCaptureReader(file, filename, cutoff)
	if err != nil {
		return fmt.Errorf("initialize live aggregate-trade reader %s: %w", filename, err)
	}
	for {
		record, readErr := reader.Read()
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return fmt.Errorf("read live aggregate-trade file %s: %w", filename, readErr)
		}
		if len(record) < 6 {
			continue
		}
		when, parseErr := time.Parse(time.RFC3339Nano, record[0])
		if parseErr != nil || when.Before(cutoff) || when.After(now) {
			continue
		}
		id, _ := strconv.ParseUint(record[2], 10, 64)
		price, parseErr := strconv.ParseFloat(record[3], 64)
		if parseErr != nil || price <= 0 {
			continue
		}
		var gapBefore time.Duration
		// New capture files append metadata after the original six columns;
		// older files remain valid and simply have no explicit gap marker.
		if len(record) >= 10 {
			if gapMillis, gapErr := strconv.ParseInt(record[9], 10, 64); gapErr == nil && gapMillis > 0 {
				gapBefore = time.Duration(gapMillis) * time.Millisecond
			}
		}
		*trades = append(*trades, warmupTrade{id: id, when: when, price: price, gapBefore: gapBefore})
	}
}
