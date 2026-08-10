package main

import (
	"encoding/csv"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/c9s/bbgo/pkg/strategy/gammacapture"
	"github.com/c9s/bbgo/pkg/types"
)

type bboSnapshot struct {
	time         time.Time
	bid, bidSize float64
	ask, askSize float64
}

type replayOrder struct {
	active     bool
	side       types.SideType
	price      float64
	quantity   float64
	queueAhead float64
}

type replayInventoryBand struct {
	min, target, max float64
}

func prepareReplayInventory(cfg gammacapture.MarketMakerConfig) (gammacapture.MarketMakerConfig, replayInventoryBand) {
	band := replayInventoryBand{min: 0, target: cfg.InventoryTarget, max: cfg.InventoryLimit}
	if band.max <= band.min {
		band.max = band.min + 1
	}
	band.target = math.Max(band.min, math.Min(band.max, band.target))
	quoteCfg := cfg
	quoteCfg.InventoryTarget = band.target
	quoteCfg.InventoryLimit = math.Max(band.target-band.min, band.max-band.target)
	if quoteCfg.InventoryLimit <= 0 {
		quoteCfg.InventoryLimit = band.max - band.min
	}
	return quoteCfg, band
}

func prepareReplayOrder(side types.SideType, plan gammacapture.MarketMakerQuotePlan, book bboSnapshot, inventory, quote, minOrderNotional float64, band replayInventoryBand) replayOrder {
	var price, plannedNotional, availableNotional, queueAhead float64
	switch side {
	case types.SideTypeBuy:
		price = plan.BidPrice
		plannedNotional = plan.BidQuoteNotional
		availableNotional = math.Min(quote, math.Max(0, band.max-inventory)*price)
		queueAhead = book.bidSize
	case types.SideTypeSell:
		price = plan.AskPrice
		plannedNotional = plan.AskQuoteNotional
		availableNotional = math.Max(0, inventory-band.min) * price
		queueAhead = book.askSize
	default:
		return replayOrder{}
	}
	notional := math.Min(plannedNotional, availableNotional)
	if price <= 0 || notional < minOrderNotional {
		return replayOrder{}
	}
	return replayOrder{active: true, side: side, price: price, quantity: notional / price, queueAhead: queueAhead}
}

type tickerStats struct {
	Symbol                         string                   `json:"symbol"`
	TradeEvents                    int                      `json:"tradeEvents"`
	BBOEvents                      int                      `json:"bboEvents"`
	TradeCoverageHours             float64                  `json:"tradeCoverageHours"`
	BBOCoverageHours               float64                  `json:"bboCoverageHours"`
	TradesPerHour                  float64                  `json:"tradesPerHour"`
	BuyTradeFraction               float64                  `json:"buyTradeFraction"`
	MedianSpreadBps                float64                  `json:"medianSpreadBps"`
	P95SpreadBps                   float64                  `json:"p95SpreadBps"`
	MedianBidDepth                 float64                  `json:"medianBidDepth"`
	MedianAskDepth                 float64                  `json:"medianAskDepth"`
	RealizedOneMinuteVolatilityBps float64                  `json:"realizedOneMinuteVolatilityBps"`
	HorizonVolatility              []horizonVolatilityStats `json:"horizonVolatility"`
	MakerFeeBps                    float64                  `json:"makerFeeBps"`
	RoundTripFeeBps                float64                  `json:"roundTripFeeBps"`
	P95GrossBBOEdgeAfterFeesBps    float64                  `json:"p95GrossBBOEdgeAfterFeesBps"`
	BBOAboveRoundTripFeeFraction   float64                  `json:"bboAboveRoundTripFeeFraction"`
	UpCrosses                      int                      `json:"upCrosses"`
	DownCrosses                    int                      `json:"downCrosses"`
	UpCrossesPerHour               float64                  `json:"upCrossesPerHour"`
	DownCrossesPerHour             float64                  `json:"downCrossesPerHour"`
	TwoSidedOpportunityPerHour     float64                  `json:"twoSidedOpportunityPerHour"`
	StatisticallyUsable            bool                     `json:"statisticallyUsable"`
	UsabilityReason                string                   `json:"usabilityReason"`
	HorizonExcursions              []horizonExcursionStats  `json:"horizonExcursions"`
	SelectedHorizonMinutes         int                      `json:"selectedHorizonMinutes"`
	SelectedHorizonScoreBpsPerHour float64                  `json:"selectedHorizonScoreBpsPerHour"`
}

// horizonVolatilityStats uses non-overlapping, UTC-aligned BBO-mid closes.
// This keeps the volatility clock consistent with the quote horizon and avoids
// treating highly overlapping forward returns as independent observations.
type horizonVolatilityStats struct {
	HorizonMinutes       int     `json:"horizonMinutes"`
	Samples              int     `json:"samples"`
	MeanReturnBps        float64 `json:"meanReturnBps"`
	RMSReturnBps         float64 `json:"rmsReturnBps"`
	StandardDeviationBps float64 `json:"standardDeviationBps"`
	P95AbsoluteReturnBps float64 `json:"p95AbsoluteReturnBps"`
}

// horizonExcursionStats measures how far the BBO mid moves after a quote is
// placed. It is intentionally different from instantaneous spread: a passive
// order can fill after the market travels through its price several minutes
// later, even when the inside spread was narrow at submission time.
type horizonExcursionStats struct {
	HorizonMinutes         int     `json:"horizonMinutes"`
	Samples                int     `json:"samples"`
	UpCrosses              int     `json:"upCrosses"`
	DownCrosses            int     `json:"downCrosses"`
	MedianUpExcursionBps   float64 `json:"medianUpExcursionBps"`
	P95UpExcursionBps      float64 `json:"p95UpExcursionBps"`
	MedianDownExcursionBps float64 `json:"medianDownExcursionBps"`
	P95DownExcursionBps    float64 `json:"p95DownExcursionBps"`
	UpCrossFraction        float64 `json:"upCrossFraction"`
	DownCrossFraction      float64 `json:"downCrossFraction"`
	UpCrossesPerHour       float64 `json:"upCrossesPerHour"`
	DownCrossesPerHour     float64 `json:"downCrossesPerHour"`
	MeanUpSpacingMinutes   float64 `json:"meanUpSpacingMinutes"`
	MeanDownSpacingMinutes float64 `json:"meanDownSpacingMinutes"`
	TwoSidedCrossFraction  float64 `json:"twoSidedCrossFraction"`
}

func summarizeTicker(trades []tick, bbo []bboSnapshot, quoteDistanceBps, makerFeeBps float64) tickerStats {
	s := tickerStats{TradeEvents: len(trades), BBOEvents: len(bbo)}
	s.MakerFeeBps = makerFeeBps
	s.RoundTripFeeBps = 2 * makerFeeBps
	if len(trades) > 1 {
		s.TradeCoverageHours = trades[len(trades)-1].time.Sub(trades[0].time).Hours()
		if s.TradeCoverageHours > 0 {
			s.TradesPerHour = float64(len(trades)) / s.TradeCoverageHours
		}
	}
	if len(bbo) > 1 {
		s.BBOCoverageHours = bbo[len(bbo)-1].time.Sub(bbo[0].time).Hours()
	}
	var buyCount int
	for _, trade := range trades {
		if trade.side == types.SideTypeBuy {
			buyCount++
		}
	}
	if len(trades) > 0 {
		s.BuyTradeFraction = float64(buyCount) / float64(len(trades))
	}
	spreads := make([]float64, 0, len(bbo))
	bidDepths := make([]float64, 0, len(bbo))
	askDepths := make([]float64, 0, len(bbo))
	for _, book := range bbo {
		spreads = append(spreads, math.Log(book.ask/book.bid)*10_000)
		bidDepths = append(bidDepths, book.bidSize)
		askDepths = append(askDepths, book.askSize)
	}
	s.MedianSpreadBps = percentile(spreads, 0.50)
	s.P95SpreadBps = percentile(spreads, 0.95)
	s.P95GrossBBOEdgeAfterFeesBps = s.P95SpreadBps - s.RoundTripFeeBps
	if len(spreads) > 0 {
		var above int
		for _, spread := range spreads {
			if spread > s.RoundTripFeeBps {
				above++
			}
		}
		s.BBOAboveRoundTripFeeFraction = float64(above) / float64(len(spreads))
	}
	s.MedianBidDepth = percentile(bidDepths, 0.50)
	s.MedianAskDepth = percentile(askDepths, 0.50)
	s.HorizonVolatility = summarizeHorizonVolatility(bbo, []int{15, 30})

	// Keep one-minute volatility as a short-scale diagnostic only. Quote-model
	// calibration and comparisons use the horizon-matched BBO values above.
	// Use one-minute closes from the captured aggregate trades for a causal
	// realized-volatility estimate, rather than treating every trade as an
	// independent return.
	minutePrices := make(map[time.Time]float64)
	var minutes []time.Time
	for _, trade := range trades {
		minute := trade.time.Truncate(time.Minute)
		if _, exists := minutePrices[minute]; !exists {
			minutes = append(minutes, minute)
		}
		minutePrices[minute] = trade.price
	}
	sort.Slice(minutes, func(i, j int) bool { return minutes[i].Before(minutes[j]) })
	if len(minutes) > 1 {
		var sumSquares float64
		var n int
		for i := 1; i < len(minutes); i++ {
			if minutePrices[minutes[i-1]] > 0 && minutePrices[minutes[i]] > 0 {
				r := math.Log(minutePrices[minutes[i]]/minutePrices[minutes[i-1]]) * 10_000
				sumSquares += r * r
				n++
			}
		}
		if n > 0 {
			s.RealizedOneMinuteVolatilityBps = math.Sqrt(sumSquares / float64(n))
		}
	}

	// Crossing health is a path property over the quote horizon. Comparing an
	// aggressive trade with the contemporaneous mid only measures an anomalous
	// execution price and misses ordinary BBO travel to a resting quote.
	s.HorizonExcursions = summarizeHorizonExcursions(bbo, quoteDistanceBps)
	s.SelectedHorizonMinutes, s.SelectedHorizonScoreBpsPerHour = selectBestHorizon(s.HorizonExcursions, quoteDistanceBps, s.RoundTripFeeBps)
	for _, horizon := range s.HorizonExcursions {
		if horizon.HorizonMinutes != s.SelectedHorizonMinutes {
			continue
		}
		s.UpCrosses = horizon.UpCrosses
		s.DownCrosses = horizon.DownCrosses
		s.UpCrossesPerHour = horizon.UpCrossesPerHour
		s.DownCrossesPerHour = horizon.DownCrossesPerHour
		s.TwoSidedOpportunityPerHour = math.Min(horizon.UpCrossesPerHour, horizon.DownCrossesPerHour)
		break
	}
	s.StatisticallyUsable = s.BBOCoverageHours >= 24 && s.TradeEvents >= 10_000 && s.UpCrosses >= 50 && s.DownCrosses >= 50
	if s.StatisticallyUsable {
		s.UsabilityReason = "at least 24h BBO, 10k trades, and 50 horizon-path crossings per side"
	} else {
		s.UsabilityReason = "need >=24h BBO, >=10k trades, and >=50 horizon-path crossings per side"
	}
	return s
}

func selectBestHorizon(stats []horizonExcursionStats, quoteDistanceBps, roundTripFeeBps float64) (int, float64) {
	bestMinutes := 0
	bestScore := 0.0
	netEdge := math.Max(0, 2*quoteDistanceBps-roundTripFeeBps)
	for _, stat := range stats {
		if stat.HorizonMinutes < 10 {
			continue
		}
		score := math.Min(stat.UpCrossesPerHour, stat.DownCrossesPerHour) * netEdge
		if bestMinutes == 0 || score > bestScore {
			bestMinutes = stat.HorizonMinutes
			bestScore = score
		}
	}
	return bestMinutes, bestScore
}

func summarizeHorizonExcursions(bbo []bboSnapshot, quoteDistanceBps float64) []horizonExcursionStats {
	if len(bbo) < 2 || quoteDistanceBps <= 0 {
		return nil
	}
	horizons := []int{1, 3, 5, 10, 15, 30}
	mids := make([]float64, len(bbo))
	for i, book := range bbo {
		mids[i] = (book.bid + book.ask) / 2
	}
	out := make([]horizonExcursionStats, 0, len(horizons))
	for _, minutes := range horizons {
		window := time.Duration(minutes) * time.Minute
		ups := make([]float64, 0, len(bbo))
		downs := make([]float64, 0, len(bbo))
		var upTimes, downTimes []time.Time
		upCrosses, downCrosses, twoSided := 0, 0, 0
		var lastUpEvent, lastDownEvent time.Time

		// Maintain the maximum and minimum future midpoint in O(n) for each
		// horizon. The previous nested scan was O(n * events-in-window), which
		// becomes prohibitive when adding a 30-minute BBO window.
		maxDeque := make([]int, 0, len(bbo))
		minDeque := make([]int, 0, len(bbo))
		right := 0
		for i := 0; i < len(bbo); i++ {
			end := bbo[i].time.Add(window)
			for right < len(bbo) && !bbo[right].time.After(end) {
				for len(maxDeque) > 0 && mids[maxDeque[len(maxDeque)-1]] <= mids[right] {
					maxDeque = maxDeque[:len(maxDeque)-1]
				}
				maxDeque = append(maxDeque, right)
				for len(minDeque) > 0 && mids[minDeque[len(minDeque)-1]] >= mids[right] {
					minDeque = minDeque[:len(minDeque)-1]
				}
				minDeque = append(minDeque, right)
				right++
			}
			if right > i+1 && mids[i] > 0 {
				up := math.Log(mids[maxDeque[0]]/mids[i]) * 10_000
				down := math.Log(mids[i]/mids[minDeque[0]]) * 10_000
				ups = append(ups, up)
				downs = append(downs, down)
				upHit, downHit := up >= quoteDistanceBps, down >= quoteDistanceBps
				upEvent := upHit && (lastUpEvent.IsZero() || bbo[i].time.Sub(lastUpEvent) >= window)
				downEvent := downHit && (lastDownEvent.IsZero() || bbo[i].time.Sub(lastDownEvent) >= window)
				if upEvent {
					upCrosses++
					upTimes = append(upTimes, bbo[i].time)
					lastUpEvent = bbo[i].time
				}
				if downEvent {
					downCrosses++
					downTimes = append(downTimes, bbo[i].time)
					lastDownEvent = bbo[i].time
				}
				if upEvent && downEvent {
					twoSided++
				}
			}
			if len(maxDeque) > 0 && maxDeque[0] == i {
				maxDeque = maxDeque[1:]
			}
			if len(minDeque) > 0 && minDeque[0] == i {
				minDeque = minDeque[1:]
			}
		}
		s := horizonExcursionStats{HorizonMinutes: minutes, Samples: len(ups), UpCrosses: upCrosses, DownCrosses: downCrosses}
		if len(ups) > 0 {
			s.MedianUpExcursionBps = percentile(ups, 0.50)
			s.P95UpExcursionBps = percentile(ups, 0.95)
			s.MedianDownExcursionBps = percentile(downs, 0.50)
			s.P95DownExcursionBps = percentile(downs, 0.95)
			s.UpCrossFraction = float64(upCrosses) / float64(len(ups))
			s.DownCrossFraction = float64(downCrosses) / float64(len(ups))
			if hours := bbo[len(bbo)-1].time.Sub(bbo[0].time).Hours(); hours > 0 {
				s.UpCrossesPerHour = float64(upCrosses) / hours
				s.DownCrossesPerHour = float64(downCrosses) / hours
			}
			s.MeanUpSpacingMinutes = meanTimeSpacingMinutes(upTimes)
			s.MeanDownSpacingMinutes = meanTimeSpacingMinutes(downTimes)
			s.TwoSidedCrossFraction = float64(twoSided) / float64(len(ups))
		}
		out = append(out, s)
	}
	return out
}

func summarizeHorizonVolatility(bbo []bboSnapshot, horizons []int) []horizonVolatilityStats {
	out := make([]horizonVolatilityStats, 0, len(horizons))
	for _, minutes := range horizons {
		stat := horizonVolatilityStats{HorizonMinutes: minutes}
		if minutes <= 0 || len(bbo) < 2 {
			out = append(out, stat)
			continue
		}
		window := time.Duration(minutes) * time.Minute
		closes := make(map[time.Time]float64)
		var buckets []time.Time
		for _, book := range bbo {
			mid := (book.bid + book.ask) / 2
			if mid <= 0 {
				continue
			}
			bucket := book.time.Truncate(window)
			if _, exists := closes[bucket]; !exists {
				buckets = append(buckets, bucket)
			}
			closes[bucket] = mid
		}
		sort.Slice(buckets, func(i, j int) bool { return buckets[i].Before(buckets[j]) })
		returns := make([]float64, 0, len(buckets)-1)
		for i := 1; i < len(buckets); i++ {
			if buckets[i].Sub(buckets[i-1]) != window {
				continue
			}
			returns = append(returns, math.Log(closes[buckets[i]]/closes[buckets[i-1]])*10_000)
		}
		stat.Samples = len(returns)
		if len(returns) > 0 {
			absolute := make([]float64, 0, len(returns))
			var sum, sumSquares float64
			for _, r := range returns {
				sum += r
				sumSquares += r * r
				absolute = append(absolute, math.Abs(r))
			}
			stat.MeanReturnBps = sum / float64(len(returns))
			stat.RMSReturnBps = math.Sqrt(sumSquares / float64(len(returns)))
			if len(returns) > 1 {
				var squaredDeviations float64
				for _, r := range returns {
					delta := r - stat.MeanReturnBps
					squaredDeviations += delta * delta
				}
				stat.StandardDeviationBps = math.Sqrt(squaredDeviations / float64(len(returns)-1))
			}
			stat.P95AbsoluteReturnBps = percentile(absolute, 0.95)
		}
		out = append(out, stat)
	}
	return out
}

func meanTimeSpacingMinutes(values []time.Time) float64 {
	if len(values) < 2 {
		return 0
	}
	var total time.Duration
	for i := 1; i < len(values); i++ {
		total += values[i].Sub(values[i-1])
	}
	return total.Minutes() / float64(len(values)-1)
}

func percentile(values []float64, q float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	index := int(math.Round(q * float64(len(sorted)-1)))
	return sorted[index]
}

func readBBO(path, symbol string, from, to time.Time) []bboSnapshot {
	files := replayCaptureFilesOverlapping(replayCaptureFiles(path, symbol, "bookticker"), symbol, "bookticker", from, to)
	return readBBOFiles(files, from, to)
}

func readBBOFiles(files []string, from, to time.Time) []bboSnapshot {
	var out []bboSnapshot
	for _, filename := range files {
		file, err := os.Open(filename)
		if err != nil {
			continue
		}
		reader := newReplayIndexedCaptureReader(file, filename, from)
		for {
			row, readErr := reader.Read()
			if readErr == io.EOF {
				break
			}
			if readErr != nil || len(row) < 5 {
				continue
			}
			when, parseErr := time.Parse(time.RFC3339Nano, row[0])
			if parseErr != nil || when.Before(from) || !when.Before(to) {
				continue
			}
			bid, e1 := strconv.ParseFloat(row[1], 64)
			bidSize, e2 := strconv.ParseFloat(row[2], 64)
			ask, e3 := strconv.ParseFloat(row[3], 64)
			askSize, e4 := strconv.ParseFloat(row[4], 64)
			if e1 != nil || e2 != nil || e3 != nil || e4 != nil || bid <= 0 || ask <= bid {
				continue
			}
			out = append(out, bboSnapshot{time: when, bid: bid, bidSize: bidSize, ask: ask, askSize: askSize})
		}
		_ = file.Close()
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].time.Before(out[j].time) })
	return out
}

// readLiveTrades reads the capture format and turns it into the same tick type
// used by the archive reader. Binance BUY is aggressive buyer flow and SELL is
// aggressive seller flow, which is exactly what a passive quote needs to test.
func readLiveTrades(path, symbol string, from, to time.Time) []tick {
	files := replayCaptureFilesOverlapping(replayCaptureFiles(path, symbol, "trades"), symbol, "trades", from, to)
	return readLiveTradesFiles(files, from, to)
}

func readLiveTradesFiles(files []string, from, to time.Time) []tick {
	var out []tick
	for _, filename := range files {
		file, err := os.Open(filename)
		if err != nil {
			continue
		}
		reader := newReplayIndexedCaptureReader(file, filename, from)
		for {
			row, readErr := reader.Read()
			if readErr == io.EOF {
				break
			}
			if readErr != nil || len(row) < 6 {
				continue
			}
			when, parseErr := time.Parse(time.RFC3339Nano, row[0])
			if parseErr != nil || when.Before(from) || !when.Before(to) {
				continue
			}
			tradeID, _ := strconv.ParseUint(row[2], 10, 64)
			price, e1 := strconv.ParseFloat(row[3], 64)
			quantity, e2 := strconv.ParseFloat(row[4], 64)
			if e1 != nil || e2 != nil || price <= 0 || quantity <= 0 {
				continue
			}
			side, sideErr := types.StrToSideType(row[5])
			if sideErr != nil {
				continue
			}
			out = append(out, tick{id: tradeID, time: when, price: price, size: quantity, side: side})
		}
		_ = file.Close()
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].time.Before(out[j].time) })
	return out
}

func replayCaptureFiles(path, symbol, stream string) []string {
	roots := []string{path}
	if filepath.Base(filepath.Clean(path)) != symbol {
		roots = append(roots, filepath.Join(path, symbol))
	}
	seen := make(map[string]struct{})
	var files []string
	for _, root := range roots {
		matches, _ := filepath.Glob(filepath.Join(root, symbol+"-"+stream+"-*.csv"))
		for _, filename := range matches {
			if strings.HasSuffix(filename, ".index.csv") {
				continue
			}
			if _, ok := seen[filename]; ok {
				continue
			}
			seen[filename] = struct{}{}
			files = append(files, filename)
		}
	}
	daily := make([]string, 0, len(files))
	for _, filename := range files {
		stamp := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(filename), symbol+"-"+stream+"-"), ".csv")
		if len(stamp) == len(time.DateOnly) {
			if _, err := time.Parse(time.DateOnly, stamp); err == nil {
				daily = append(daily, filename)
			}
		}
	}
	if len(daily) > 0 {
		sort.Strings(daily)
		return daily
	}
	sort.Strings(files)
	return files
}

// replayCaptureFilesOverlapping avoids opening every daily file for a bounded
// replay. Legacy timestamped files have no reliable filename date and are kept.
func replayCaptureFilesOverlapping(files []string, symbol, stream string, from, to time.Time) []string {
	if from.IsZero() || !from.Before(to) {
		return files
	}
	out := make([]string, 0, len(files))
	for _, filename := range files {
		stamp := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(filename), symbol+"-"+stream+"-"), ".csv")
		day, err := time.Parse(time.DateOnly, stamp)
		if err != nil {
			out = append(out, filename)
			continue
		}
		start := day.UTC()
		if start.Before(to) && start.Add(24*time.Hour).After(from) {
			out = append(out, filename)
		}
	}
	return out
}

// newReplayIndexedCaptureReader seeks to the sparse minute index when one is
// available. The index stores byte offsets at the beginning of data rows; a
// missing/stale index safely falls back to reading the CSV header.
func newReplayIndexedCaptureReader(file *os.File, filename string, cutoff time.Time) *csv.Reader {
	if offset, ok := replayCaptureIndexOffset(filename+".index.csv", cutoff); ok && offset > 0 {
		if _, err := file.Seek(offset, io.SeekStart); err == nil {
			return csv.NewReader(file)
		}
	}
	_, _ = file.Seek(0, io.SeekStart)
	reader := csv.NewReader(file)
	_, _ = reader.Read()
	return reader
}

func replayCaptureIndexOffset(indexPath string, cutoff time.Time) (int64, bool) {
	index, err := os.Open(indexPath)
	if err != nil {
		return 0, false
	}
	defer index.Close()
	reader := csv.NewReader(index)
	_, _ = reader.Read()
	target := cutoff.UTC().Truncate(time.Minute)
	var best int64
	var bestMinute time.Time
	found := false
	for {
		row, readErr := reader.Read()
		if readErr == io.EOF {
			break
		}
		if readErr != nil || len(row) < 2 {
			continue
		}
		minute, err := time.Parse(time.RFC3339Nano, row[0])
		if err != nil || minute.After(target) {
			continue
		}
		offset, err := strconv.ParseInt(row[1], 10, 64)
		if err == nil && offset >= 0 && (!found || minute.After(bestMinute) || (minute.Equal(bestMinute) && offset < best)) {
			best = offset
			bestMinute = minute
			found = true
		}
	}
	return best, found && best > 0
}

// simulateEventReplay uses captured BBO and aggressive trades. A quote starts
// behind the visible top-of-book size; aggressive volume consumes that queue
// proxy before the order can fill. This is materially less optimistic than one
// fill per OHLC bar while remaining explicit about the missing true queue data.
func simulateEventReplay(trades []tick, bbo []bboSnapshot, cfg gammacapture.MarketMakerConfig, startingQuote, minOrderNotional float64) result {
	if len(trades) == 0 || len(bbo) == 0 {
		return result{SyntheticFillModel: "historical_bbo_aggtrade_market_maker_event_replay", DataQuality: "no-overlap"}
	}
	quoteCfg, inventoryBand := prepareReplayInventory(cfg)
	quote := startingQuote
	inventory := inventoryBand.target
	initialEquity := quote + inventory*bbo[0].bid
	fees, maxInventory := 0.0, math.Abs(inventory)
	var fills, buys, sells, executionEvents, partialFillEvents, observations, quoteActive, quoteRefreshes int
	var bidOrder, askOrder replayOrder
	var current bboSnapshot
	var lastQuote time.Time
	var previousMid float64
	var previousBBOTime time.Time
	var recentReturns, recentSeconds []float64
	var quoteLifetimes []float64
	var quotedHalfSpreadSum, maxQuotedHalfSpread float64
	var quotedHalfSpreadCount int
	bi, ti := 0, 0
	for bi < len(bbo) || ti < len(trades) {
		useBBO := ti >= len(trades) || (bi < len(bbo) && !bbo[bi].time.After(trades[ti].time))
		if useBBO {
			current = bbo[bi]
			bi++
			observations++
			mid := (current.bid + current.ask) / 2
			if !previousBBOTime.IsZero() && previousMid > 0 {
				dt := current.time.Sub(previousBBOTime).Seconds()
				if dt > 0 {
					recentReturns = append(recentReturns, math.Log(mid/previousMid)*10_000)
					recentSeconds = append(recentSeconds, dt)
					if len(recentReturns) > 128 {
						recentReturns = recentReturns[1:]
						recentSeconds = recentSeconds[1:]
					}
				}
			}
			previousMid, previousBBOTime = mid, current.time
			var sumSquares, totalSeconds float64
			for i, r := range recentReturns {
				sumSquares += r * r
				totalSeconds += recentSeconds[i]
			}
			volatility := 0.0
			if totalSeconds > 0 {
				volatility = math.Sqrt(sumSquares / totalSeconds)
			}
			selectedHorizon := time.Duration(quoteCfg.MinTradingWindow)
			keepDecision := quoteCfg.DynamicOrderKeepDecision(selectedHorizon,
				quoteCfg.OrderKeepDistanceBps(quoteCfg.HalfSpreadForHorizon(selectedHorizon, volatility)), volatility)
			for i := 0; i < 3; i++ {
				distance := quoteCfg.OrderKeepDistanceBps(quoteCfg.HalfSpreadForHorizon(keepDecision.Duration, volatility))
				next := quoteCfg.DynamicOrderKeepDecision(selectedHorizon, distance, volatility)
				if next.Duration == keepDecision.Duration {
					keepDecision = next
					break
				}
				keepDecision = next
			}
			plan := quoteCfg.Quote(gammacapture.MarketMakerQuoteInput{
				MidPrice: mid, BestBid: current.bid, BestAsk: current.ask,
				// Volatility and queue lifetime use the same first-passage
				// horizon, matching the live strategy.
				VolatilityPerSqrtSec:  volatility,
				TradingHorizonSeconds: keepDecision.Duration.Seconds(),
				Inventory:             inventory, InventoryMin: inventoryBand.min, InventoryMax: inventoryBand.max,
				QuoteNotionalBase: cfg.QuoteNotional,
				CanBuy:            quote >= minOrderNotional && inventory < inventoryBand.max,
				CanSell:           (inventory-inventoryBand.min)*mid >= minOrderNotional,
			})
			if plan.Reason == "quoted" {
				quotedHalfSpreadSum += plan.HalfSpreadBps
				quotedHalfSpreadCount++
				if plan.HalfSpreadBps > maxQuotedHalfSpread {
					maxQuotedHalfSpread = plan.HalfSpreadBps
				}
			}
			minRefresh, _ := quoteCfg.RefreshIntervals(plan.HalfSpreadBps, volatility)
			orderKeepDuration := keepDecision.Duration
			quoteCrossed := (bidOrder.active && bidOrder.price >= current.ask) || (askOrder.active && askOrder.price <= current.bid)
			shouldRefresh := lastQuote.IsZero()
			if !shouldRefresh {
				elapsed := current.time.Sub(lastQuote)
				// Do not model every mid move as a cancel/recreate. A resting
				// quote earns its queue priority until it crosses the BBO or
				// reaches its distance-derived horizon.
				missingSide := (plan.AllowBid && !bidOrder.active) || (plan.AllowAsk && !askOrder.active)
				shouldRefresh = elapsed >= minRefresh && (quoteCrossed || missingSide || elapsed >= orderKeepDuration)
			}
			if shouldRefresh {
				if !lastQuote.IsZero() {
					quoteLifetimes = append(quoteLifetimes, current.time.Sub(lastQuote).Seconds())
				}
				quoteRefreshes++
				bidOrder = replayOrder{}
				askOrder = replayOrder{}
				if plan.AllowBid {
					bidOrder = prepareReplayOrder(types.SideTypeBuy, plan, current, inventory, quote, minOrderNotional, inventoryBand)
				}
				if plan.AllowAsk {
					askOrder = prepareReplayOrder(types.SideTypeSell, plan, current, inventory, quote, minOrderNotional, inventoryBand)
				}
				lastQuote = current.time
			}
			if bidOrder.active || askOrder.active {
				quoteActive++
			}
			continue
		}

		trade := trades[ti]
		ti++
		if askOrder.active && trade.side == types.SideTypeBuy && trade.price >= askOrder.price {
			fill, completed := consumeQueue(&askOrder, trade.size)
			if fill > 0 {
				inventory -= fill
				quote += fill * askOrder.price
				fees += fill * askOrder.price * cfg.MakerFeeBps / 10_000
				executionEvents++
				if completed {
					fills++
					sells++
				} else {
					partialFillEvents++
				}
			}
		}
		if bidOrder.active && trade.side == types.SideTypeSell && trade.price <= bidOrder.price {
			fill, completed := consumeQueue(&bidOrder, trade.size)
			if fill > 0 && quote >= fill*bidOrder.price {
				inventory += fill
				quote -= fill * bidOrder.price
				fees += fill * bidOrder.price * cfg.MakerFeeBps / 10_000
				executionEvents++
				if completed {
					fills++
					buys++
				} else {
					partialFillEvents++
				}
			}
		}
		if math.Abs(inventory) > maxInventory {
			maxInventory = math.Abs(inventory)
		}
	}
	last := bbo[len(bbo)-1].bid
	if !lastQuote.IsZero() {
		quoteLifetimes = append(quoteLifetimes, bbo[len(bbo)-1].time.Sub(lastQuote).Seconds())
	}
	var averageQuoteLife float64
	for _, lifetime := range quoteLifetimes {
		averageQuoteLife += lifetime
	}
	if len(quoteLifetimes) > 0 {
		averageQuoteLife /= float64(len(quoteLifetimes))
	}
	var averageQuotedHalfSpread float64
	if quotedHalfSpreadCount > 0 {
		averageQuotedHalfSpread = quotedHalfSpreadSum / float64(quotedHalfSpreadCount)
	}
	finalEquity := quote + inventory*last - fees
	days := bbo[len(bbo)-1].time.Sub(bbo[0].time).Hours() / 24
	if days < 1.0/24 {
		days = 1.0 / 24
	}
	return result{
		HalfSpreadBps: cfg.MinimumHalfSpreadBps, InventorySkewBps: cfg.InventorySkewBps,
		Observations: observations, Fills: fills, BuyFills: buys, SellFills: sells,
		ExecutionEvents: executionEvents, PartialFillEvents: partialFillEvents,
		MakerFeesJPY: fees, FinalEquityJPY: finalEquity, NetPnLJPY: finalEquity - initialEquity,
		MaxAbsInventory: maxInventory, QuoteUptimePct: float64(quoteActive) * 100 / float64(max(1, observations)),
		FillsPerDay: float64(fills) / days, SyntheticFillModel: "historical_bbo_aggtrade_market_maker_event_replay",
		QuoteRefreshes: quoteRefreshes, AverageQuoteLifeSeconds: averageQuoteLife,
		AverageQuotedHalfSpreadBps: averageQuotedHalfSpread, MaxQuotedHalfSpreadBps: maxQuotedHalfSpread,
		DataQuality: "BBO plus aggressive trades; visible queue proxy, no true queue position",
	}
}

func consumeQueue(order *replayOrder, volume float64) (float64, bool) {
	if volume <= 0 || !order.active {
		return 0, false
	}
	if order.queueAhead >= volume {
		order.queueAhead -= volume
		return 0, false
	}
	fill := math.Min(volume-order.queueAhead, order.quantity)
	order.queueAhead = 0
	if fill <= 0 {
		return 0, false
	}
	order.quantity -= fill
	if order.quantity <= 1e-12 {
		order.quantity = 0
		order.active = false
		return fill, true
	}
	return fill, false
}
