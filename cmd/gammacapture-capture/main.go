// gammacapture-capture records public Binance aggregate trades and best-
// bid/best-ask updates for later paper-model calibration. It never creates a
// BBGO session, account, strategy, or order.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/c9s/bbgo/pkg/exchange"
	"github.com/c9s/bbgo/pkg/types"
)

func main() {
	symbol := flag.String("symbol", "BTCJPY", "Binance spot symbol")
	duration := flag.Duration("duration", 2*time.Hour, "capture duration")
	output := flag.String("output", "data/gammacapture/live", "directory for timestamped CSV files")
	migrateLegacy := flag.Bool("migrate-legacy", false, "copy legacy timestamped CSVs into verified UTC-daily files, then exit")
	flag.Parse()
	if *symbol == "" || *duration <= 0 {
		fatalf("symbol and positive duration are required")
	}
	if err := os.MkdirAll(*output, 0o755); err != nil {
		fatalf("create output directory: %v", err)
	}
	if *migrateLegacy {
		result, err := migrateLegacyCaptureFiles(*output, *symbol)
		if err != nil {
			fatalf("migrate legacy capture: %v", err)
		}
		fmt.Printf("migrated public %s capture: files=%d rows=%d dailyFiles=%d\n", *symbol, result.InputFiles, result.Rows, result.DailyFiles)
		return
	}
	trades, err := newDailyCSV(*output, *symbol, "trades", []string{"event_time", "received_at", "id", "price", "quantity", "side", "aggregate_id", "first_trade_id", "last_trade_id", "gap_before_ms", "source"})
	if err != nil {
		fatalf("open trade output: %v", err)
	}
	defer trades.close()
	books, err := newDailyCSV(*output, *symbol, "bookticker", []string{"received_at", "bid", "bid_quantity", "ask", "ask_quantity", "gap_before_ms"})
	if err != nil {
		fatalf("open book output: %v", err)
	}
	defer books.close()

	ex, err := exchange.NewPublic(types.ExchangeBinance)
	if err != nil {
		fatalf("create Binance client: %v", err)
	}
	stream, ok := ex.(types.Exchange)
	if !ok {
		fatalf("public Binance client does not expose a stream")
	}
	marketStream := stream.NewStream()
	marketStream.SetPublicOnly()
	marketStream.Subscribe(types.AggTradeChannel, *symbol, types.SubscribeOptions{})
	marketStream.Subscribe(types.BookTickerChannel, *symbol, types.SubscribeOptions{})
	var tradeGaps, bookGaps captureGapTracker
	marketStream.OnAggTrade(func(trade types.Trade) {
		if trade.Symbol != *symbol {
			return
		}
		received := time.Now().UTC()
		gap := tradeGaps.Observe(received)
		if gap >= captureGapThreshold {
			log.Printf("WARN aggregate-trade capture gap=%s", gap)
		}
		if err := trades.write(received, []string{
			trade.Time.Time().UTC().Format(time.RFC3339Nano), received.Format(time.RFC3339Nano),
			fmt.Sprintf("%d", trade.ID), trade.Price.String(), trade.Quantity.String(), trade.Side.String(),
			fmt.Sprintf("%d", trade.AggregateTradeID), fmt.Sprintf("%d", trade.FirstTradeID), fmt.Sprintf("%d", trade.LastTradeID),
			fmt.Sprintf("%d", gap.Milliseconds()), "stream",
		}); err != nil {
			log.Printf("ERROR write aggregate-trade capture: %v", err)
		}
	})
	marketStream.OnBookTickerUpdate(func(book types.BookTicker) {
		if book.Symbol != *symbol {
			return
		}
		received := time.Now().UTC()
		gap := bookGaps.Observe(received)
		if gap >= captureGapThreshold {
			log.Printf("WARN bookticker capture gap=%s", gap)
		}
		if err := books.write(received, []string{received.Format(time.RFC3339Nano), book.Buy.String(), book.BuySize.String(), book.Sell.String(), book.SellSize.String(), fmt.Sprintf("%d", gap.Milliseconds())}); err != nil {
			log.Printf("ERROR write bookticker capture: %v", err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), *duration)
	defer cancel()
	if err := marketStream.Connect(ctx); err != nil {
		fatalf("connect public stream: %v", err)
	}
	defer marketStream.Close()
	fmt.Printf("capturing public %s trades and BBO for %s into %s\n", *symbol, *duration, *output)
	<-ctx.Done()
	if ctx.Err() != context.DeadlineExceeded {
		fatalf("capture stopped: %v", ctx.Err())
	}
}

const captureGapThreshold = 5 * time.Second

type captureGapTracker struct {
	mu   sync.Mutex
	last time.Time
}

func (g *captureGapTracker) Observe(now time.Time) time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	var gap time.Duration
	if !g.last.IsZero() {
		gap = now.Sub(g.last)
	}
	g.last = now
	return gap
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(2)
}
