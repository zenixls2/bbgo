// gammacapture-capture records public Binance aggregate trades and best-
// bid/best-ask updates for later paper-model calibration. It never creates a
// BBGO session, account, strategy, or order.
package main

import (
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/c9s/bbgo/pkg/exchange"
	"github.com/c9s/bbgo/pkg/types"
)

func main() {
	symbol := flag.String("symbol", "BTCJPY", "Binance spot symbol")
	duration := flag.Duration("duration", 2*time.Hour, "capture duration")
	output := flag.String("output", "data/gammacapture/live", "directory for timestamped CSV files")
	flag.Parse()
	if *symbol == "" || *duration <= 0 {
		fatalf("symbol and positive duration are required")
	}
	if err := os.MkdirAll(*output, 0o755); err != nil {
		fatalf("create output directory: %v", err)
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	trades, err := newCSV(filepath.Join(*output, *symbol+"-trades-"+stamp+".csv"), []string{"event_time", "received_at", "id", "price", "quantity", "side", "aggregate_id", "first_trade_id", "last_trade_id", "gap_before_ms", "source"})
	if err != nil {
		fatalf("open trade output: %v", err)
	}
	defer trades.close()
	books, err := newCSV(filepath.Join(*output, *symbol+"-bookticker-"+stamp+".csv"), []string{"received_at", "bid", "bid_quantity", "ask", "ask_quantity", "gap_before_ms"})
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
		trades.write([]string{
			trade.Time.Time().UTC().Format(time.RFC3339Nano), received.Format(time.RFC3339Nano),
			fmt.Sprintf("%d", trade.ID), trade.Price.String(), trade.Quantity.String(), trade.Side.String(),
			fmt.Sprintf("%d", trade.AggregateTradeID), fmt.Sprintf("%d", trade.FirstTradeID), fmt.Sprintf("%d", trade.LastTradeID),
			fmt.Sprintf("%d", gap.Milliseconds()), "stream",
		})
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
		books.write([]string{received.Format(time.RFC3339Nano), book.Buy.String(), book.BuySize.String(), book.Sell.String(), book.SellSize.String(), fmt.Sprintf("%d", gap.Milliseconds())})
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

type csvFile struct {
	mu sync.Mutex
	f  *os.File
	w  *csv.Writer
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

func newCSV(path string, header []string) (*csvFile, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	w := csv.NewWriter(f)
	if err := w.Write(header); err != nil {
		_ = f.Close()
		return nil, err
	}
	w.Flush()
	if err := w.Error(); err != nil {
		_ = f.Close()
		return nil, err
	}
	return &csvFile{f: f, w: w}, nil
}

func (f *csvFile) write(record []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.w.Write(record); err == nil {
		f.w.Flush()
	}
}

func (f *csvFile) close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.w.Flush()
	_ = f.f.Close()
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(2)
}
