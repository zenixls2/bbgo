package main

import (
	"encoding/csv"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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

type tickerStats struct {
	Symbol                         string                  `json:"symbol"`
	TradeEvents                    int                     `json:"tradeEvents"`
	BBOEvents                      int                     `json:"bboEvents"`
	TradeCoverageHours             float64                 `json:"tradeCoverageHours"`
	BBOCoverageHours               float64                 `json:"bboCoverageHours"`
	TradesPerHour                  float64                 `json:"tradesPerHour"`
	BuyTradeFraction               float64                 `json:"buyTradeFraction"`
	MedianSpreadBps                float64                 `json:"medianSpreadBps"`
	P95SpreadBps                   float64                 `json:"p95SpreadBps"`
	MedianBidDepth                 float64                 `json:"medianBidDepth"`
	MedianAskDepth                 float64                 `json:"medianAskDepth"`
	RealizedOneMinuteVolatilityBps float64                 `json:"realizedOneMinuteVolatilityBps"`
	MakerFeeBps                    float64                 `json:"makerFeeBps"`
	RoundTripFeeBps                float64                 `json:"roundTripFeeBps"`
	P95GrossBBOEdgeAfterFeesBps    float64                 `json:"p95GrossBBOEdgeAfterFeesBps"`
	BBOAboveRoundTripFeeFraction   float64                 `json:"bboAboveRoundTripFeeFraction"`
	UpCrosses                      int                     `json:"upCrosses"`
	DownCrosses                    int                     `json:"downCrosses"`
	UpCrossesPerHour               float64                 `json:"upCrossesPerHour"`
	DownCrossesPerHour             float64                 `json:"downCrossesPerHour"`
	TwoSidedOpportunityPerHour     float64                 `json:"twoSidedOpportunityPerHour"`
	StatisticallyUsable            bool                    `json:"statisticallyUsable"`
	UsabilityReason                string                  `json:"usabilityReason"`
	HorizonExcursions              []horizonExcursionStats `json:"horizonExcursions"`
	SelectedHorizonMinutes         int                     `json:"selectedHorizonMinutes"`
	SelectedHorizonScoreBpsPerHour float64                 `json:"selectedHorizonScoreBpsPerHour"`
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

	// Count aggressive trade crossings relative to the selected quote distance
	// using the latest BBO snapshot. This is a ticker-specific opportunity rate,
	// not a claim that our order would have filled at that price.
	bi := 0
	for _, trade := range trades {
		for bi+1 < len(bbo) && !bbo[bi+1].time.After(trade.time) {
			bi++
		}
		if len(bbo) == 0 || bbo[bi].time.After(trade.time) {
			continue
		}
		mid := (bbo[bi].bid + bbo[bi].ask) / 2
		if trade.side == types.SideTypeBuy && trade.price >= mid*math.Exp(quoteDistanceBps/10_000) {
			s.UpCrosses++
		} else if trade.side == types.SideTypeSell && trade.price <= mid*math.Exp(-quoteDistanceBps/10_000) {
			s.DownCrosses++
		}
	}
	overlapHours := s.BBOCoverageHours
	if overlapHours <= 0 {
		overlapHours = s.TradeCoverageHours
	}
	if overlapHours > 0 {
		s.UpCrossesPerHour = float64(s.UpCrosses) / overlapHours
		s.DownCrossesPerHour = float64(s.DownCrosses) / overlapHours
		s.TwoSidedOpportunityPerHour = math.Min(s.UpCrossesPerHour, s.DownCrossesPerHour)
	}
	s.StatisticallyUsable = s.BBOCoverageHours >= 24 && s.TradeEvents >= 10_000 && s.UpCrosses >= 50 && s.DownCrosses >= 50
	if s.StatisticallyUsable {
		s.UsabilityReason = "at least 24h BBO, 10k trades, and 50 crossings per side"
	} else {
		s.UsabilityReason = "need >=24h BBO, >=10k trades, and >=50 up/down crossings"
	}
	s.HorizonExcursions = summarizeHorizonExcursions(bbo, quoteDistanceBps)
	s.SelectedHorizonMinutes, s.SelectedHorizonScoreBpsPerHour = selectBestHorizon(s.HorizonExcursions, quoteDistanceBps, s.RoundTripFeeBps)
	return s
}

func selectBestHorizon(stats []horizonExcursionStats, quoteDistanceBps, roundTripFeeBps float64) (int, float64) {
	bestMinutes := 0
	bestScore := 0.0
	netEdge := math.Max(0, 2*quoteDistanceBps-roundTripFeeBps)
	for _, stat := range stats {
		if stat.HorizonMinutes < 5 {
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
	horizons := []int{1, 3, 5, 10, 15}
	out := make([]horizonExcursionStats, 0, len(horizons))
	for _, minutes := range horizons {
		window := time.Duration(minutes) * time.Minute
		ups := make([]float64, 0, len(bbo))
		downs := make([]float64, 0, len(bbo))
		var upTimes, downTimes []time.Time
		upCrosses, downCrosses, twoSided := 0, 0, 0
		var lastUpEvent, lastDownEvent time.Time
		j := 1
		for i := 0; i < len(bbo); i++ {
			if j <= i {
				j = i + 1
			}
			for j < len(bbo) && bbo[j].time.Sub(bbo[i].time) <= window {
				j++
			}
			if j <= i+1 {
				continue
			}
			mid := (bbo[i].bid + bbo[i].ask) / 2
			if mid <= 0 {
				continue
			}
			maxMid, minMid := mid, mid
			for k := i + 1; k < j; k++ {
				futureMid := (bbo[k].bid + bbo[k].ask) / 2
				if futureMid > maxMid {
					maxMid = futureMid
				}
				if futureMid < minMid {
					minMid = futureMid
				}
			}
			up := math.Log(maxMid/mid) * 10_000
			down := math.Log(mid/minMid) * 10_000
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
	files, _ := filepath.Glob(filepath.Join(path, symbol+"-bookticker-*.csv"))
	var out []bboSnapshot
	for _, filename := range files {
		file, err := os.Open(filename)
		if err != nil {
			continue
		}
		reader := csv.NewReader(file)
		_, _ = reader.Read() // header
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
	files, _ := filepath.Glob(filepath.Join(path, symbol+"-trades-*.csv"))
	var out []tick
	for _, filename := range files {
		file, err := os.Open(filename)
		if err != nil {
			continue
		}
		reader := csv.NewReader(file)
		_, _ = reader.Read() // header
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
			price, e1 := strconv.ParseFloat(row[3], 64)
			quantity, e2 := strconv.ParseFloat(row[4], 64)
			if e1 != nil || e2 != nil || price <= 0 || quantity <= 0 {
				continue
			}
			side, sideErr := types.StrToSideType(row[5])
			if sideErr != nil {
				continue
			}
			out = append(out, tick{time: when, price: price, size: quantity, side: side})
		}
		_ = file.Close()
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].time.Before(out[j].time) })
	return out
}

// simulateEventReplay uses captured BBO and aggressive trades. A quote starts
// behind the visible top-of-book size; aggressive volume consumes that queue
// proxy before the order can fill. This is materially less optimistic than one
// fill per OHLC bar while remaining explicit about the missing true queue data.
func simulateEventReplay(trades []tick, bbo []bboSnapshot, cfg gammacapture.MarketMakerConfig, startingQuote, minOrderNotional float64) result {
	if len(trades) == 0 || len(bbo) == 0 {
		return result{SyntheticFillModel: "historical_bbo_aggtrade_market_maker_event_replay", DataQuality: "no-overlap"}
	}
	quote := startingQuote
	inventory := cfg.InventoryLimit / 2
	initialEquity := quote + inventory*bbo[0].bid
	var fees, maxInventory float64
	var fills, buys, sells, observations, quoteActive, quoteRefreshes int
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
			plan := cfg.Quote(gammacapture.MarketMakerQuoteInput{
				MidPrice: mid, BestBid: current.bid, BestAsk: current.ask,
				// volatility is bps/sqrt(second); Quote converts it into an
				// expected move over the distance-dependent quote lifetime.
				VolatilityPerSqrtSec: volatility,
				Inventory:            inventory,
				CanBuy:               quote >= cfg.QuoteNotional,
				CanSell:              inventory*mid >= minOrderNotional,
			})
			if plan.Reason == "quoted" {
				quotedHalfSpreadSum += plan.HalfSpreadBps
				quotedHalfSpreadCount++
				if plan.HalfSpreadBps > maxQuotedHalfSpread {
					maxQuotedHalfSpread = plan.HalfSpreadBps
				}
			}
			minRefresh, maxRefresh := cfg.RefreshIntervals(plan.HalfSpreadBps, volatility)
			quoteCrossed := (bidOrder.active && bidOrder.price >= current.ask) || (askOrder.active && askOrder.price <= current.bid)
			shouldRefresh := lastQuote.IsZero()
			if !shouldRefresh {
				elapsed := current.time.Sub(lastQuote)
				// Do not model every mid move as a cancel/recreate. A resting
				// quote earns its queue priority until it crosses the BBO or
				// reaches its distance-derived horizon.
				missingSide := (plan.AllowBid && !bidOrder.active) || (plan.AllowAsk && !askOrder.active)
				shouldRefresh = elapsed >= minRefresh && (quoteCrossed || missingSide || elapsed >= maxRefresh)
			}
			if shouldRefresh {
				if !lastQuote.IsZero() {
					quoteLifetimes = append(quoteLifetimes, current.time.Sub(lastQuote).Seconds())
				}
				quoteRefreshes++
				bidOrder = replayOrder{}
				askOrder = replayOrder{}
				if plan.AllowBid {
					bidOrder = replayOrder{active: true, side: types.SideTypeBuy, price: plan.BidPrice, quantity: cfg.QuoteNotional / plan.BidPrice, queueAhead: current.bidSize}
				}
				if plan.AllowAsk {
					askOrder = replayOrder{active: true, side: types.SideTypeSell, price: plan.AskPrice, quantity: math.Min(cfg.QuoteNotional/plan.AskPrice, inventory), queueAhead: current.askSize}
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
			fill := consumeQueue(&askOrder, trade.size)
			if fill > 0 {
				askOrder.active = false
				inventory -= fill
				quote += fill * askOrder.price
				fees += fill * askOrder.price * cfg.MakerFeeBps / 10_000
				fills++
				sells++
			}
		}
		if bidOrder.active && trade.side == types.SideTypeSell && trade.price <= bidOrder.price {
			fill := consumeQueue(&bidOrder, trade.size)
			if fill > 0 && quote >= fill*bidOrder.price {
				bidOrder.active = false
				inventory += fill
				quote -= fill * bidOrder.price
				fees += fill * bidOrder.price * cfg.MakerFeeBps / 10_000
				fills++
				buys++
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
		MakerFeesJPY: fees, FinalEquityJPY: finalEquity, NetPnLJPY: finalEquity - initialEquity,
		MaxAbsInventory: maxInventory, QuoteUptimePct: float64(quoteActive) * 100 / float64(max(1, observations)),
		FillsPerDay: float64(fills) / days, SyntheticFillModel: "historical_bbo_aggtrade_market_maker_event_replay",
		QuoteRefreshes: quoteRefreshes, AverageQuoteLifeSeconds: averageQuoteLife,
		AverageQuotedHalfSpreadBps: averageQuotedHalfSpread, MaxQuotedHalfSpreadBps: maxQuotedHalfSpread,
		DataQuality: "BBO plus aggressive trades; visible queue proxy, no true queue position",
	}
}

func consumeQueue(order *replayOrder, volume float64) float64 {
	if volume <= 0 || !order.active {
		return 0
	}
	if order.queueAhead >= volume {
		order.queueAhead -= volume
		return 0
	}
	fill := volume - order.queueAhead
	order.queueAhead = 0
	if fill > order.quantity {
		fill = order.quantity
	}
	return fill
}
