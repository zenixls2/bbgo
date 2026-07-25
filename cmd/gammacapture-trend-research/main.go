// gammacapture-trend-research evaluates a causal, low-frequency BTCJPY trend
// hypothesis on closed one-minute prices. It is research only and assumes a
// market fill at the close following a completed signal.
package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"

	_ "github.com/mattn/go-sqlite3"
)

type bar struct {
	time  string
	close float64
}

type report struct {
	From          string  `json:"from"`
	To            string  `json:"to"`
	LookbackMins  int     `json:"lookbackMins"`
	HoldingMins   int     `json:"holdingMins"`
	CooldownMins  int     `json:"cooldownMins"`
	SignalSide    string  `json:"signalSide"`
	ThresholdBps  float64 `json:"thresholdBps"`
	TargetBps     float64 `json:"targetBps"`
	StopBps       float64 `json:"stopBps"`
	Trades        int     `json:"trades"`
	TargetHits    int     `json:"targetHits"`
	StopHits      int     `json:"stopHits"`
	TimeExits     int     `json:"timeExits"`
	GrossBps      float64 `json:"grossBps"`
	NetBps        float64 `json:"netBps"`
	AverageNetBps float64 `json:"averageNetBps"`
	WinRate       float64 `json:"winRate"`
}

func main() {
	database := flag.String("database", "bbgo.sqlite3", "SQLite kline database")
	from := flag.String("from", "2026-01-01", "inclusive date")
	to := flag.String("to", "2026-06-01", "exclusive date")
	lookback := flag.Int("lookback-mins", 60, "closed-price momentum lookback")
	threshold := flag.Float64("threshold-bps", 50, "minimum lookback return")
	target := flag.Float64("target-bps", 100, "profit target from entry")
	stop := flag.Float64("stop-bps", 50, "loss cut from entry")
	holding := flag.Int("holding-mins", 240, "maximum holding time")
	cooldown := flag.Int("cooldown-mins", 30, "post-exit cooldown")
	signalSide := flag.String("signal-side", "continuation", "entry signal: continuation (up move) or reversal (down move)")
	fee := flag.Float64("fee-bps", 15, "round-trip taker fee")
	flag.Parse()
	if *from >= *to || *lookback < 1 || *threshold < 0 || *target <= 0 || *stop <= 0 || *holding < 1 || *cooldown < 0 || *fee < 0 || (*signalSide != "continuation" && *signalSide != "reversal") {
		fatalf("invalid research parameters")
	}
	db, err := sql.Open("sqlite3", "file:"+*database+"?mode=ro")
	if err != nil {
		fatalf("open database: %v", err)
	}
	defer db.Close()
	bars, err := loadBars(db, *from, *to)
	if err != nil {
		fatalf("load bars: %v", err)
	}
	r := report{From: *from, To: *to, LookbackMins: *lookback, HoldingMins: *holding, CooldownMins: *cooldown, SignalSide: *signalSide, ThresholdBps: *threshold, TargetBps: *target, StopBps: *stop}
	for i := *lookback; i+*holding < len(bars); {
		momentum := math.Log(bars[i].close/bars[i-*lookback].close) * 10_000
		if !signalTriggered(momentum, *threshold, *signalSide) {
			i++
			continue
		}
		entry := bars[i].close
		exitIndex := i + *holding
		outcome := "time"
		for j := i + 1; j <= i+*holding; j++ {
			move := math.Log(bars[j].close/entry) * 10_000
			if move >= *target {
				exitIndex, outcome = j, "target"
				break
			}
			if move <= -*stop {
				exitIndex, outcome = j, "stop"
				break
			}
		}
		gross := math.Log(bars[exitIndex].close/entry) * 10_000
		net := gross - *fee
		r.Trades++
		r.GrossBps += gross
		r.NetBps += net
		if net > 0 {
			r.WinRate++
		}
		switch outcome {
		case "target":
			r.TargetHits++
		case "stop":
			r.StopHits++
		default:
			r.TimeExits++
		}
		i = exitIndex + *cooldown
	}
	if r.Trades > 0 {
		r.AverageNetBps = r.NetBps / float64(r.Trades)
		r.WinRate /= float64(r.Trades)
	}
	if err := json.NewEncoder(os.Stdout).Encode(r); err != nil {
		fatalf("encode report: %v", err)
	}
}

func signalTriggered(momentum, threshold float64, side string) bool {
	if side == "reversal" {
		return momentum <= -threshold
	}
	return momentum >= threshold
}

func loadBars(db *sql.DB, from, to string) ([]bar, error) {
	rows, err := db.Query(`SELECT start_time, close FROM binance_klines WHERE symbol = 'BTCJPY' AND interval = '1m' AND start_time >= ? AND start_time < ? ORDER BY start_time`, from, to)
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

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(2)
}
