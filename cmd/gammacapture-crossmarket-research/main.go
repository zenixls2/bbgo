// gammacapture-crossmarket-research tests whether a closed BTCUSDT move leads
// a later BTCJPY move. It is read-only and intentionally reports only simple,
// non-overlapping fee-inclusive reference trades; it does not place orders.
package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	_ "github.com/mattn/go-sqlite3"
)

type bar struct {
	time  string
	close float64
}

type result struct {
	ThresholdBps  int     `json:"thresholdBps"`
	HoldingMins   int     `json:"holdingMins"`
	Trades        int     `json:"trades"`
	WinRate       float64 `json:"winRate"`
	GrossBps      float64 `json:"grossBps"`
	NetBps        float64 `json:"netBps"`
	AverageNetBps float64 `json:"averageNetBps"`
}

type report struct {
	From    string   `json:"from"`
	To      string   `json:"to"`
	Results []result `json:"results"`
}

func main() {
	database := flag.String("database", "bbgo.sqlite3", "SQLite kline database")
	from := flag.String("from", "2025-07-01", "inclusive date")
	to := flag.String("to", "2026-01-01", "exclusive date")
	feeBps := flag.Float64("fee-bps", 15, "round-trip taker fee in bps")
	flag.Parse()
	if *from >= *to || *feeBps < 0 {
		fatalf("invalid date range or fee")
	}
	db, err := sql.Open("sqlite3", "file:"+*database+"?mode=ro")
	if err != nil {
		fatalf("open database: %v", err)
	}
	defer db.Close()
	jpy, err := loadBars(db, "BTCJPY", *from, *to)
	if err != nil {
		fatalf("load BTCJPY: %v", err)
	}
	usdt, err := loadBars(db, "BTCUSDT", *from, *to)
	if err != nil {
		fatalf("load BTCUSDT: %v", err)
	}
	ref := make(map[string]float64, len(usdt))
	for _, b := range usdt {
		ref[b.time] = b.close
	}
	r := report{From: *from, To: *to}
	for _, threshold := range []int{5, 10, 20, 30} {
		for _, holding := range []int{1, 5, 15} {
			r.Results = append(r.Results, evaluate(jpy, ref, threshold, holding, *feeBps))
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(r); err != nil {
		fatalf("encode report: %v", err)
	}
}

func loadBars(db *sql.DB, symbol, from, to string) ([]bar, error) {
	rows, err := db.Query(`SELECT start_time, close FROM binance_klines WHERE symbol = ? AND interval = '1m' AND start_time >= ? AND start_time < ? ORDER BY start_time`, symbol, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var bars []bar
	for rows.Next() {
		var b bar
		if err := rows.Scan(&b.time, &b.close); err != nil {
			return nil, err
		}
		if b.close > 0 {
			bars = append(bars, b)
		}
	}
	return bars, rows.Err()
}

func evaluate(target []bar, reference map[string]float64, threshold, holding int, feeBps float64) result {
	r := result{ThresholdBps: threshold, HoldingMins: holding}
	for i := 1; i+holding < len(target); {
		currentRef, ok := reference[target[i].time]
		previousRef, previousOK := reference[target[i-1].time]
		if !ok || !previousOK || previousRef <= 0 {
			i++
			continue
		}
		leadBps := (currentRef/previousRef - 1) * 10_000
		if leadBps < float64(threshold) {
			i++
			continue
		}
		gross := (target[i+holding].close/target[i].close - 1) * 10_000
		net := gross - feeBps
		r.Trades++
		r.GrossBps += gross
		r.NetBps += net
		if net > 0 {
			r.WinRate++
		}
		i += holding
	}
	if r.Trades > 0 {
		r.WinRate /= float64(r.Trades)
		r.AverageNetBps = r.NetBps / float64(r.Trades)
	}
	return r
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(2)
}
