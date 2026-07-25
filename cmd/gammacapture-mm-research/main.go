// gammacapture-mm-research trains and evaluates the fee-aware quote policy.
//
// The archive currently contains aggregate trades but no historical BBO or
// queue position.  Consequently fills are a conservative synthetic-BBO
// experiment, not an exchange-fidelity backtest. The report says this
// explicitly so a profitable parameter set cannot be mistaken for production
// evidence.
package main

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/c9s/bbgo/pkg/datasource/csvsource"
	"github.com/c9s/bbgo/pkg/strategy/gammacapture"
	"github.com/c9s/bbgo/pkg/types"
)

type tick struct {
	time  time.Time
	price float64
	size  float64
	side  types.SideType
}

type minuteBar struct {
	time       time.Time
	open, high float64
	low, close float64
}

type result struct {
	HalfSpreadBps              float64 `json:"halfSpreadBps"`
	InventorySkewBps           float64 `json:"inventorySkewBps"`
	VolatilityMult             float64 `json:"volatilityMultiplier"`
	Observations               int     `json:"observations"`
	Fills                      int     `json:"fills"`
	BuyFills                   int     `json:"buyFills"`
	SellFills                  int     `json:"sellFills"`
	MakerFeesJPY               float64 `json:"makerFeesJPY"`
	FinalEquityJPY             float64 `json:"finalEquityJPY"`
	NetPnLJPY                  float64 `json:"netPnLJPY"`
	MaxAbsInventory            float64 `json:"maxAbsInventory"`
	QuoteUptimePct             float64 `json:"quoteUptimePct"`
	FillsPerDay                float64 `json:"fillsPerDay"`
	MeetsTurnoverGoal          bool    `json:"meetsTurnoverGoal"`
	SyntheticFillModel         string  `json:"syntheticFillModel"`
	DataQuality                string  `json:"dataQuality"`
	QuoteRefreshes             int     `json:"quoteRefreshes"`
	AverageQuoteLifeSeconds    float64 `json:"averageQuoteLifeSeconds"`
	AverageQuotedHalfSpreadBps float64 `json:"averageQuotedHalfSpreadBps"`
	MaxQuotedHalfSpreadBps     float64 `json:"maxQuotedHalfSpreadBps"`
}

type report struct {
	Mode            string      `json:"mode"`
	Symbol          string      `json:"symbol"`
	TrainFrom       string      `json:"trainFrom"`
	TrainTo         string      `json:"trainTo"`
	HoldoutFrom     string      `json:"holdoutFrom"`
	HoldoutTo       string      `json:"holdoutTo"`
	HistoricalBBO   bool        `json:"historicalBBOAvailable"`
	BBOEvents       int         `json:"bboEvents"`
	AggTradeEvents  int         `json:"aggTradeEvents"`
	SampleHours     float64     `json:"sampleHours"`
	Warning         string      `json:"warning"`
	Selected        result      `json:"selected"`
	TrainCandidates []result    `json:"trainCandidates"`
	Holdout         result      `json:"holdout"`
	TickerStats     tickerStats `json:"tickerStats"`
}

func main() {
	dataPath := flag.String("data", "data/gammacapture/binance/SOLJPY/aggTrades", "aggregate-trade CSV directory")
	symbol := flag.String("symbol", "SOLJPY", "symbol")
	trainFrom := flag.String("train-from", "2026-01-01", "inclusive training date")
	trainTo := flag.String("train-to", "2026-06-01", "exclusive training date")
	holdoutFrom := flag.String("holdout-from", "2026-07-14", "inclusive holdout date")
	holdoutTo := flag.String("holdout-to", "2026-07-18", "exclusive holdout date")
	bboData := flag.String("bbo-data", "data/gammacapture/live", "captured BBO/trade directory; enables event replay when overlapping data exists")
	fee := flag.Float64("maker-fee-bps", 10, "maker fee per side (Binance default: 10 bps)")
	adverse := flag.Float64("adverse-selection-bps", 2, "one-sided adverse selection allowance")
	minimumEdge := flag.Float64("minimum-net-edge-bps", 2, "round-trip residual edge required")
	quoteNotional := flag.Float64("quote-notional-jpy", 50_000, "notional per quote")
	minOrderNotional := flag.Float64("min-order-notional-jpy", 100, "exchange minimum order notional")
	statsQuoteDistance := flag.Float64("stats-quote-distance-bps", 15, "quote distance used for ticker crossing statistics")
	inventoryLimit := flag.Float64("inventory-limit", 100, "base-unit inventory limit; larger limits are required for high turnover but increase inventory risk")
	startingQuote := flag.Float64("starting-quote-jpy", 1_000_000, "starting quote balance")
	minFillsPerDay := flag.Float64("min-fills-per-day", 200, "minimum training turnover target")
	flag.Parse()
	trainStart, trainEnd := parseDate(*trainFrom), parseDate(*trainTo)
	holdStart, holdEnd := parseDate(*holdoutFrom), parseDate(*holdoutTo)
	if !trainStart.Before(trainEnd) || !holdStart.Before(holdEnd) || *fee < 0 || *adverse < 0 || *inventoryLimit <= 0 || *quoteNotional <= 0 || *minOrderNotional <= 0 || *statsQuoteDistance <= 0 {
		fatalf("invalid date or fee/inventory configuration")
	}
	trainBars := aggregateBars(readTicks(*dataPath, trainStart, trainEnd))
	holdoutTicks := readTicks(*dataPath, holdStart, holdEnd)
	holdoutTicks = append(holdoutTicks, readLiveTrades(*bboData, *symbol, holdStart, holdEnd)...)
	sort.SliceStable(holdoutTicks, func(i, j int) bool { return holdoutTicks[i].time.Before(holdoutTicks[j].time) })
	holdoutBars := aggregateBars(holdoutTicks)
	if len(trainBars) == 0 || len(holdoutBars) == 0 {
		fatalf("no ticks in requested train or holdout range")
	}
	candidates := make([]result, 0, 12)
	for _, half := range []float64{1, 2, 4, 6, 8, 10, 15, 20, 30, 40} {
		for _, skew := range []float64{0, 10, 20} {
			for _, volMult := range []float64{0.05, 0.1, 0.25} {
				candidate := simulate(trainBars, gammacapture.MarketMakerConfig{MakerFeeBps: *fee, AdverseSelectionBps: *adverse, MinimumNetEdgeBps: *minimumEdge, MinimumHalfSpreadBps: half, MaximumHalfSpreadBps: 80, VolatilityMultiplier: volMult, InventoryLimit: *inventoryLimit, InventorySkewBps: skew, QuoteNotional: *quoteNotional}, *startingQuote, *minOrderNotional)
				candidate.VolatilityMult = volMult
				candidate.MeetsTurnoverGoal = candidate.FillsPerDay >= *minFillsPerDay
				candidates = append(candidates, candidate)
			}
		}
	}
	anyTurnoverGoal := false
	for _, candidate := range candidates {
		if candidate.MeetsTurnoverGoal {
			anyTurnoverGoal = true
			break
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if anyTurnoverGoal && candidates[i].MeetsTurnoverGoal != candidates[j].MeetsTurnoverGoal {
			return candidates[i].MeetsTurnoverGoal
		}
		if !anyTurnoverGoal && candidates[i].FillsPerDay != candidates[j].FillsPerDay {
			return candidates[i].FillsPerDay > candidates[j].FillsPerDay
		}
		return candidates[i].NetPnLJPY > candidates[j].NetPnLJPY
	})
	selected := candidates[0]
	holdoutConfig := gammacapture.MarketMakerConfig{MakerFeeBps: *fee, AdverseSelectionBps: *adverse, MinimumNetEdgeBps: *minimumEdge, MinimumHalfSpreadBps: selected.HalfSpreadBps, MaximumHalfSpreadBps: 80, VolatilityMultiplier: selected.VolatilityMult, InventoryLimit: *inventoryLimit, InventorySkewBps: selected.InventorySkewBps, QuoteNotional: *quoteNotional}
	holdout := simulate(holdoutBars, holdoutConfig, *startingQuote, *minOrderNotional)
	bbo := readBBO(*bboData, *symbol, holdStart, holdEnd)
	if len(bbo) > 0 && len(holdoutTicks) > 0 {
		holdout = simulateEventReplay(holdoutTicks, bbo, holdoutConfig, *startingQuote, *minOrderNotional)
	}
	holdout.VolatilityMult = selected.VolatilityMult
	holdout.MeetsTurnoverGoal = holdout.FillsPerDay >= *minFillsPerDay
	historicalBBO := len(bbo) > 0
	warning := "No overlapping BBO archive; holdout uses synthetic 1m high/low crossing and is not live-fill evidence."
	mode := "synthetic_bbo_market_maker_1m"
	if historicalBBO {
		mode = "historical_bbo_aggtrade_market_maker_event_replay"
		warning = "BBO is captured market data, but queue position is estimated from visible BBO size and aggressive trade volume; treat fills as a conservative queue proxy, not exchange-confirmed fills."
	}
	sampleHours := 0.0
	if len(bbo) > 1 {
		sampleHours = bbo[len(bbo)-1].time.Sub(bbo[0].time).Hours()
	}
	if historicalBBO && (sampleHours < 24 || holdout.Fills < 100) {
		warning += " Sample is still too short or has too few fills for a stable-profit claim; continue capture before selecting parameters."
	}
	r := report{Mode: mode, Symbol: *symbol, TrainFrom: trainStart.Format(time.DateOnly), TrainTo: trainEnd.Format(time.DateOnly), HoldoutFrom: holdStart.Format(time.DateOnly), HoldoutTo: holdEnd.Format(time.DateOnly), HistoricalBBO: historicalBBO, BBOEvents: len(bbo), AggTradeEvents: len(holdoutTicks), SampleHours: sampleHours, Warning: warning, Selected: selected, TrainCandidates: candidates, Holdout: holdout}
	r.TickerStats = summarizeTicker(holdoutTicks, bbo, *statsQuoteDistance, *fee)
	r.TickerStats.Symbol = *symbol
	if !r.TickerStats.StatisticallyUsable {
		r.Warning += " Ticker-specific statistical evidence is currently insufficient: " + r.TickerStats.UsabilityReason + "."
	}
	if err := json.NewEncoder(os.Stdout).Encode(r); err != nil {
		fatalf("write report: %v", err)
	}
}

func simulate(bars []minuteBar, cfg gammacapture.MarketMakerConfig, startingQuote, minOrderNotional float64) result {
	start := bars[0].open
	base := cfg.InventoryLimit / 2
	quote := startingQuote
	initialEquity := quote + base*start
	inventory := base
	var fees, maxInventory float64
	var fills, buys, sells, quoteActive, observations int
	var recentReturns []float64
	for _, bar := range bars {
		observations++
		if bar.open <= 0 {
			continue
		}
		if len(bars) > 1 && observations > 1 {
			previous := bars[observations-2].close
			recentReturns = append(recentReturns, math.Log(bar.open/previous))
			if len(recentReturns) > 256 {
				recentReturns = recentReturns[len(recentReturns)-256:]
			}
		}
		bookHalf := 1.0
		bookBid := bar.open * math.Exp(-bookHalf/10_000)
		bookAsk := bar.open * math.Exp(bookHalf/10_000)
		plan := cfg.Quote(gammacapture.MarketMakerQuoteInput{MidPrice: bar.open, BestBid: bookBid, BestAsk: bookAsk, VolatilityBps: rollingVolatilityBps(recentReturns), Inventory: inventory, CanBuy: quote >= cfg.QuoteNotional, CanSell: inventory*bar.open >= minOrderNotional})
		if plan.Reason != "quoted" {
			continue
		}
		quoteActive++
		// Ask is our sell order: a bar high crossing it sells base. Bid is our
		// buy order: a bar low crossing it buys base. Each side can fill once.
		if plan.AllowAsk && bar.high >= plan.AskPrice {
			qty := math.Min(cfg.QuoteNotional/plan.AskPrice, inventory)
			if qty > 0 {
				inventory -= qty
				quote += qty * plan.AskPrice
				fees += qty * plan.AskPrice * cfg.MakerFeeBps / 10_000
				fills++
				sells++
			}
		}
		if plan.AllowBid && bar.low <= plan.BidPrice {
			qty := math.Min(cfg.QuoteNotional/plan.BidPrice, math.Max(0, cfg.InventoryLimit-inventory))
			if qty > 0 && quote >= qty*plan.BidPrice {
				inventory += qty
				quote -= qty * plan.BidPrice
				fees += qty * plan.BidPrice * cfg.MakerFeeBps / 10_000
				fills++
				buys++
			}
		}
		if math.Abs(inventory) > maxInventory {
			maxInventory = math.Abs(inventory)
		}
	}
	last := bars[len(bars)-1].close
	finalEquity := quote + inventory*last - fees
	days := bars[len(bars)-1].time.Sub(bars[0].time).Hours() / 24
	if days < 1.0/24 {
		days = 1.0 / 24
	}
	return result{HalfSpreadBps: cfg.MinimumHalfSpreadBps, InventorySkewBps: cfg.InventorySkewBps, Observations: observations, Fills: fills, BuyFills: buys, SellFills: sells, MakerFeesJPY: fees, FinalEquityJPY: finalEquity, NetPnLJPY: finalEquity - initialEquity, MaxAbsInventory: maxInventory, QuoteUptimePct: float64(quoteActive) * 100 / float64(max(1, observations)), FillsPerDay: float64(fills) / days, SyntheticFillModel: "one fill per side per 1m bar when bar high/low crosses synthetic BBO; no queue model", DataQuality: "synthetic OHLC; no BBO or queue position"}
}

func rollingVolatilityBps(returns []float64) float64 {
	if len(returns) < 2 {
		return 0
	}
	var sum float64
	for _, r := range returns {
		sum += r * r
	}
	// Scale the recent per-trade RMS to a short quote lifetime. This is a
	// deliberately simple causal volatility feature; a production model should
	// learn the scale from BBO event time and trade intensity.
	return math.Sqrt(sum/float64(len(returns))) * math.Sqrt(20) * 10_000
}

func readTicks(path string, from, to time.Time) []tick {
	files, err := filepath.Glob(filepath.Join(path, "*.csv"))
	if err != nil {
		fatalf("list files: %v", err)
	}
	sort.Strings(files)
	var ticks []tick
	for _, filename := range files {
		f, err := os.Open(filename)
		if err != nil {
			fatalf("open %s: %v", filename, err)
		}
		r := csvsource.NewCSVTickReader(csv.NewReader(f))
		for {
			x, readErr := r.Read()
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				_ = f.Close()
				fatalf("read %s: %v", filename, readErr)
			}
			now := x.Timestamp.Time()
			if now.Before(from) || !now.Before(to) || x.Price.Sign() <= 0 || x.Size.Sign() <= 0 {
				continue
			}
			ticks = append(ticks, tick{time: now, price: x.Price.Float64(), size: x.Size.Float64(), side: x.Side})
		}
		if err := f.Close(); err != nil {
			fatalf("close %s: %v", filename, err)
		}
	}
	sort.SliceStable(ticks, func(i, j int) bool { return ticks[i].time.Before(ticks[j].time) })
	return ticks
}

func aggregateBars(ticks []tick) []minuteBar {
	if len(ticks) == 0 {
		return nil
	}
	bars := make([]minuteBar, 0, len(ticks)/20)
	for _, t := range ticks {
		if t.price <= 0 {
			continue
		}
		start := t.time.Truncate(time.Minute)
		if len(bars) == 0 || !bars[len(bars)-1].time.Equal(start) {
			bars = append(bars, minuteBar{time: start, open: t.price, high: t.price, low: t.price, close: t.price})
			continue
		}
		bar := &bars[len(bars)-1]
		if t.price > bar.high {
			bar.high = t.price
		}
		if t.price < bar.low {
			bar.low = t.price
		}
		bar.close = t.price
	}
	return bars
}

func parseDate(v string) time.Time {
	t, err := time.Parse(time.DateOnly, v)
	if err != nil {
		fatalf("parse date %q: %v", v, err)
	}
	return t
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(2)
}
