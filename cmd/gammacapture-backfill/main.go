// gammacapture-backfill repairs aggregate-trade capture gaps from Binance's
// public REST endpoint. It never touches the input file; the repaired stream is
// written to a separate CSV so the operator can inspect it before promotion.
package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type capturedTrade struct {
	row       []string
	eventTime time.Time
	aggID     uint64
	gapBefore time.Duration
}

type binanceAggTrade struct {
	AggregateID uint64 `json:"a"`
	Price       string `json:"p"`
	Quantity    string `json:"q"`
	FirstID     uint64 `json:"f"`
	LastID      uint64 `json:"l"`
	TradeTime   int64  `json:"T"`
	IsMaker     bool   `json:"m"`
}

func main() {
	input := flag.String("input", "", "captured SOLJPY trades CSV")
	output := flag.String("output", "", "repaired output CSV (default: input + .repaired.csv)")
	symbol := flag.String("symbol", "SOLJPY", "Binance spot symbol")
	baseURL := flag.String("base-url", "https://api.binance.com", "Binance REST base URL")
	maxGap := flag.Duration("max-gap", 5*time.Second, "minimum receive gap to repair")
	maxGaps := flag.Int("max-gaps", 0, "maximum gaps to repair; zero means all")
	flag.Parse()
	if *input == "" || *symbol == "" || *maxGap <= 0 || *maxGaps < 0 {
		fatalf("input, symbol, positive max-gap, and non-negative max-gaps are required")
	}
	if *output == "" {
		*output = *input + ".repaired.csv"
	}
	if err := repair(context.Background(), *input, *output, *symbol, *baseURL, *maxGap, *maxGaps); err != nil {
		fatalf("backfill failed: %v", err)
	}
}

func repair(ctx context.Context, input, output, symbol, baseURL string, maxGap time.Duration, maxGaps int) error {
	file, err := os.Open(input)
	if err != nil {
		return err
	}
	reader := csv.NewReader(file)
	header, err := reader.Read()
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("read header: %w", err)
	}
	indexes := make(map[string]int, len(header))
	for i, name := range header {
		indexes[name] = i
	}
	for _, required := range []string{"event_time", "received_at", "id", "price", "quantity", "side"} {
		if _, ok := indexes[required]; !ok {
			_ = file.Close()
			return fmt.Errorf("missing column %q", required)
		}
	}
	if _, ok := indexes["aggregate_id"]; !ok {
		_ = file.Close()
		return fmt.Errorf("input has no aggregate_id column; use a new gap-aware capture file")
	}
	if _, ok := indexes["gap_before_ms"]; !ok {
		_ = file.Close()
		return fmt.Errorf("input has no gap_before_ms column; use a new gap-aware capture file")
	}
	if _, ok := indexes["source"]; !ok {
		header = append(header, "source")
		indexes["source"] = len(header) - 1
	}

	rows := make([]capturedTrade, 0, 4096)
	existing := make(map[uint64]struct{})
	for {
		row, readErr := reader.Read()
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			_ = file.Close()
			return fmt.Errorf("read row: %w", readErr)
		}
		if len(row) < len(indexes)-1 {
			continue
		}
		eventTime, parseErr := time.Parse(time.RFC3339Nano, row[indexes["event_time"]])
		if parseErr != nil {
			continue
		}
		aggID, _ := strconv.ParseUint(row[indexes["aggregate_id"]], 10, 64)
		gapMillis, _ := strconv.ParseInt(row[indexes["gap_before_ms"]], 10, 64)
		if len(row) <= indexes["source"] {
			row = append(row, "stream")
		}
		rows = append(rows, capturedTrade{row: row, eventTime: eventTime, aggID: aggID, gapBefore: time.Duration(gapMillis) * time.Millisecond})
		if aggID > 0 {
			existing[aggID] = struct{}{}
		}
	}
	if err := file.Close(); err != nil {
		return err
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].eventTime.Before(rows[j].eventTime) })

	client := &http.Client{Timeout: 20 * time.Second}
	backfilled := 0
	processedGaps := 0
	for i := range rows {
		if rows[i].gapBefore < maxGap || i == 0 || (maxGaps > 0 && processedGaps >= maxGaps) {
			continue
		}
		processedGaps++
		from := rows[i-1].eventTime
		to := rows[i].eventTime
		trades, fetchErr := fetchAggTrades(ctx, client, baseURL, symbol, from, to)
		if fetchErr != nil {
			return fmt.Errorf("gap %s to %s: %w", from, to, fetchErr)
		}
		for _, trade := range trades {
			tradeTime := time.UnixMilli(trade.TradeTime)
			if _, ok := existing[trade.AggregateID]; ok || !tradeTime.After(from) || !tradeTime.Before(to) {
				continue
			}
			row := make([]string, len(header))
			row[indexes["event_time"]] = tradeTime.UTC().Format(time.RFC3339Nano)
			row[indexes["received_at"]] = tradeTime.UTC().Format(time.RFC3339Nano)
			row[indexes["id"]] = strconv.FormatUint(trade.LastID, 10)
			row[indexes["price"]] = trade.Price
			row[indexes["quantity"]] = trade.Quantity
			if trade.IsMaker {
				row[indexes["side"]] = "SELL"
			} else {
				row[indexes["side"]] = "BUY"
			}
			row[indexes["aggregate_id"]] = strconv.FormatUint(trade.AggregateID, 10)
			row[indexes["first_trade_id"]] = strconv.FormatUint(trade.FirstID, 10)
			row[indexes["last_trade_id"]] = strconv.FormatUint(trade.LastID, 10)
			row[indexes["gap_before_ms"]] = "0"
			row[indexes["source"]] = "rest_backfill"
			rows = append(rows, capturedTrade{row: row, eventTime: tradeTime, aggID: trade.AggregateID})
			existing[trade.AggregateID] = struct{}{}
			backfilled++
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].eventTime.Equal(rows[j].eventTime) {
			return rows[i].aggID < rows[j].aggID
		}
		return rows[i].eventTime.Before(rows[j].eventTime)
	})

	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return err
	}
	out, err := os.Create(output)
	if err != nil {
		return err
	}
	w := csv.NewWriter(out)
	if err := w.Write(header); err != nil {
		_ = out.Close()
		return err
	}
	for _, trade := range rows {
		if err := w.Write(trade.row); err != nil {
			_ = out.Close()
			return err
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	fmt.Printf("input=%s output=%s gaps=%d backfilled=%d rows=%d\n", input, output, processedGaps, backfilled, len(rows))
	return nil
}

func fetchAggTrades(ctx context.Context, client *http.Client, baseURL, symbol string, from, to time.Time) ([]binanceAggTrade, error) {
	values := url.Values{}
	values.Set("symbol", strings.ToUpper(symbol))
	values.Set("startTime", strconv.FormatInt(from.UnixMilli(), 10))
	values.Set("endTime", strconv.FormatInt(to.UnixMilli(), 10))
	values.Set("limit", "1000")
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/api/v3/aggTrades?"+values.Encode(), nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return nil, fmt.Errorf("HTTP %s: %s", response.Status, body)
	}
	var trades []binanceAggTrade
	if err := json.NewDecoder(response.Body).Decode(&trades); err != nil {
		return nil, err
	}
	return trades, nil
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(2)
}
