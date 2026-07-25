// gammacapture-event-research evaluates the gamma-capture signal against
// chronological aggregate-trade events. It is a research tool: fills are
// assumed at the observed trade price and it does not represent BBO depth.
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
	"strings"
	"time"

	"github.com/c9s/bbgo/pkg/datasource/csvsource"
	"github.com/c9s/bbgo/pkg/strategy/gammacapture"
	"github.com/c9s/bbgo/pkg/types"
)

type report struct {
	Mode                  string              `json:"mode"`
	Symbol                string              `json:"symbol"`
	From                  string              `json:"from"`
	To                    string              `json:"to"`
	Observations          int                 `json:"observations"`
	CleanEvents           int                 `json:"cleanEvents"`
	Healthy               int                 `json:"healthy"`
	RawSignalUp           int                 `json:"rawSignalUp"`
	RawSignalDown         int                 `json:"rawSignalDown"`
	HealthySignalUp       int                 `json:"healthySignalUp"`
	HealthySignalDown     int                 `json:"healthySignalDown"`
	SignalWindow          int                 `json:"signalWindow"`
	Probability           int                 `json:"probability"`
	ExpectedEdge          int                 `json:"expectedEdge"`
	OrderFlowPass         int                 `json:"orderFlowPass"`
	ReclaimPass           int                 `json:"reclaimPass"`
	Entries               int                 `json:"entries"`
	TargetHits            int                 `json:"targetHits"`
	StopHits              int                 `json:"stopHits"`
	TimedExits            int                 `json:"timedExits"`
	OpenAtEnd             int                 `json:"openAtEnd"`
	GrossJPY              float64             `json:"grossJPY"`
	FeesJPY               float64             `json:"feesJPY"`
	NetJPY                float64             `json:"netJPY"`
	MaxTP                 float64             `json:"maxTP"`
	MaxExpectedNetEdgeBps float64             `json:"maxExpectedNetEdgeBps"`
	OutOfOrderTicks       int                 `json:"outOfOrderTicks"`
	Calibration           []calibrationBucket `json:"calibration"`
	Trades                []trade             `json:"trades"`
}

// calibrationBucket compares the model's finite-horizon TP probability with
// the observed first-passage outcome and fee-inclusive realized result. The
// bucket is deliberately emitted even when no trade falls into it so reports
// can be compared across runs.
type calibrationBucket struct {
	LowerProbability float64 `json:"lowerProbability"`
	UpperProbability float64 `json:"upperProbability"`
	Count            int     `json:"count"`
	TargetHits       int     `json:"targetHits"`
	StopHits         int     `json:"stopHits"`
	OtherExits       int     `json:"otherExits"`
	PositiveNet      int     `json:"positiveNet"`
	PredictedTPSum   float64 `json:"predictedTPSum"`
	NetSumJPY        float64 `json:"netSumJPY"`
}

type openTrade struct {
	entry       float64
	qty         float64
	opened      time.Time
	reportIndex int
}

type flowSample struct {
	time time.Time
	buy  float64
	sell float64
}

type trade struct {
	Opened             time.Time `json:"opened"`
	Closed             time.Time `json:"closed"`
	Entry              float64   `json:"entry"`
	Exit               float64   `json:"exit"`
	PredictedTP        float64   `json:"predictedTP"`
	ExpectedNetEdgeBps float64   `json:"expectedNetEdgeBps"`
	Up                 int       `json:"up"`
	Down               int       `json:"down"`
	OrderFlowImbalance float64   `json:"orderFlowImbalance"`
	Outcome            string    `json:"outcome"`
	NetJPY             float64   `json:"netJPY"`
}

func main() {
	dataPath := flag.String("data", "data/gammacapture/binance/BTCJPY/aggTrades", "directory containing chronological aggregate-trade CSV files")
	symbol := flag.String("symbol", "BTCJPY", "symbol represented by the aggregate-trade archive")
	fromText := flag.String("from", "2026-01-01", "inclusive UTC date (YYYY-MM-DD)")
	toText := flag.String("to", "2026-06-01", "exclusive UTC date (YYYY-MM-DD)")
	width := flag.Float64("width", .001, "log-price barrier width")
	window := flag.Duration("window", 2*time.Hour, "intensity lookback")
	horizon := flag.Duration("horizon", 2*time.Hour, "first-passage horizon")
	maxHolding := flag.Duration("max-holding", 3*time.Hour, "forced close horizon")
	maxCrossings := flag.Int("max-crossings", 1, "maximum crossings accepted from one event")
	minEvents := flag.Int("min-events", 10, "minimum clean events before health")
	low := flag.Float64("low", .42, "hysteresis lower probability")
	high := flag.Float64("high", .58, "hysteresis upper probability")
	entryProbability := flag.Float64("entry-probability", .60, "minimum TP probability")
	target := flag.Int("target", 8, "target barrier count")
	stop := flag.Int("stop", 2, "hard-stop barrier count")
	costBps := flag.Float64("cost-bps", 25, "all-in model cost budget")
	minimumEdge := flag.Float64("minimum-edge-bps", 30, "required residual expected edge")
	actualFeeRate := flag.Float64("actual-fee-rate", .0015, "realized round-trip fee rate")
	notional := flag.Float64("notional-jpy", 50000, "per-trade JPY notional")
	cooldown := flag.Duration("cooldown", 30*time.Minute, "cooldown after every close")
	flowWindow := flag.Duration("flow-window", 5*time.Minute, "aggressor-volume lookback used for diagnostics")
	minFlowImbalance := flag.Float64("min-flow-imbalance", -1, "minimum buy aggressor imbalance required for an entry")
	maxFlowImbalance := flag.Float64("max-flow-imbalance", 1, "maximum buy aggressor imbalance allowed for an entry")
	mode := flag.String("mode", "momentum", "entry hypothesis: momentum or reclaim")
	reclaimBars := flag.Int("reclaim-bars", 1, "upward barriers required after a down signal in reclaim mode")
	reclaimWindow := flag.Duration("reclaim-window", 30*time.Minute, "maximum wait for a reclaim entry")
	includeTrades := flag.Bool("include-trades", false, "include individual trades in JSON output")
	flag.Parse()

	from := parseDate(*fromText)
	to := parseDate(*toText)
	if !from.Before(to) || *width <= 0 || *target < 1 || *stop < 1 || *maxCrossings < 1 || *minFlowImbalance < -1 || *maxFlowImbalance > 1 || *minFlowImbalance > *maxFlowImbalance || (*mode != "momentum" && *mode != "reclaim") || *reclaimBars < 1 || *reclaimWindow <= 0 {
		fatalf("invalid range or model parameters")
	}

	r := report{Mode: *mode, Symbol: *symbol, From: from.Format(time.DateOnly), To: to.Format(time.DateOnly), Calibration: makeCalibrationBuckets(10)}
	engine := gammacapture.NewCrossingEngine(*width, 0, *maxCrossings)
	model := gammacapture.NewIntensityModel(gammacapture.IntensityConfig{
		Window:         types.Duration(*window),
		PriorAlphaUp:   1,
		PriorBetaUp:    60,
		PriorAlphaDown: 1,
		PriorBetaDown:  60,
		MinEvents:      *minEvents,
	})
	var rawSignals, signals gammacapture.SignalState
	var signalHealthActive bool
	var position *openTrade
	var eligibleUntil, cooldownUntil time.Time
	var lastTime time.Time
	var lastPrice float64
	var reclaimUntil time.Time
	var reclaimReference float64
	var flow []flowSample
	var flowHead int
	var buyFlow, sellFlow float64

	files, err := filepath.Glob(filepath.Join(*dataPath, "*.csv"))
	if err != nil {
		fatalf("list files: %v", err)
	}
	for _, filename := range files {
		// Binance Vision files are daily; skip whole files outside the requested
		// interval rather than parsing an archive that cannot contribute a tick.
		fileDay, dayErr := dateFromFilename(filename)
		if dayErr == nil && (fileDay.Before(from) || !fileDay.Before(to)) {
			continue
		}
		file, err := os.Open(filename)
		if err != nil {
			fatalf("open %s: %v", filename, err)
		}
		reader := csvsource.NewCSVTickReader(csv.NewReader(file))
		for {
			tick, readErr := reader.Read()
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				_ = file.Close()
				fatalf("read %s: %v", filename, readErr)
			}
			if tick == nil {
				continue
			}
			now := tick.Timestamp.Time()
			if now.Before(from) || !now.Before(to) {
				continue
			}
			price := tick.Price.Float64()
			if price <= 0 {
				continue
			}
			if !lastTime.IsZero() && now.Before(lastTime) {
				r.OutOfOrderTicks++
				continue
			}
			lastTime, lastPrice = now, price
			r.Observations++
			quote := price * tick.Size.Float64()
			sample := flowSample{time: now}
			if tick.Side == types.SideTypeBuy {
				sample.buy = quote
				buyFlow += quote
			} else {
				sample.sell = quote
				sellFlow += quote
			}
			flow = append(flow, sample)
			flowCut := now.Add(-*flowWindow)
			for flowHead < len(flow) && flow[flowHead].time.Before(flowCut) {
				buyFlow -= flow[flowHead].buy
				sellFlow -= flow[flowHead].sell
				flowHead++
			}
			if flowHead > 4096 {
				flow = append([]flowSample(nil), flow[flowHead:]...)
				flowHead = 0
			}
			imbalance := 0.0
			if total := buyFlow + sellFlow; total > 0 {
				imbalance = (buyFlow - sellFlow) / total
			}

			if position != nil {
				targetPrice := position.entry * math.Exp(float64(*target)*(*width))
				stopPrice := position.entry * math.Exp(-float64(*stop)*(*width))
				if price >= targetPrice {
					closeTrade(&r, position, price, now, "target", *actualFeeRate)
					r.TargetHits++
					position = nil
					cooldownUntil = now.Add(*cooldown)
				} else if price <= stopPrice {
					closeTrade(&r, position, price, now, "stop", *actualFeeRate)
					r.StopHits++
					position = nil
					cooldownUntil = now.Add(*cooldown)
				} else if now.Sub(position.opened) >= *maxHolding {
					closeTrade(&r, position, price, now, "time", *actualFeeRate)
					r.TimedExits++
					position = nil
					cooldownUntil = now.Add(*cooldown)
				}
			}

			for _, event := range engine.Update(*symbol, tick.Price, now, now, 0) {
				if !event.GapAffected {
					r.CleanEvents++
				}
				model.Update(event)
			}
			snapshot := model.Snapshot(now)
			passage := gammacapture.TPBeforeSL(snapshot.LambdaUp, snapshot.LambdaDown, *horizon, *target, *stop, 0)
			rawSignal := rawSignals.Update(passage.TP, *low, *high)
			var signal gammacapture.SignalCrossing
			if snapshot.Health != gammacapture.HealthHealthy {
				if signalHealthActive {
					signals.ResetHysteresis()
					signalHealthActive = false
				}
			} else {
				if !signalHealthActive {
					signals.ResetHysteresis()
					signalHealthActive = true
				}
				signal = signals.Update(passage.TP, *low, *high)
			}
			if passage.TP > r.MaxTP {
				r.MaxTP = passage.TP
			}
			edge := expectedNetEdgeBps(*width, *target, *stop, *costBps, passage)
			if edge > r.MaxExpectedNetEdgeBps {
				r.MaxExpectedNetEdgeBps = edge
			}
			if rawSignal == gammacapture.SignalUp {
				r.RawSignalUp++
			}
			if rawSignal == gammacapture.SignalDown {
				r.RawSignalDown++
			}
			if snapshot.Health != gammacapture.HealthHealthy {
				continue
			}
			r.Healthy++
			if *mode == "momentum" {
				if signal == gammacapture.SignalUp {
					r.HealthySignalUp++
					eligibleUntil = now.Add(5 * time.Minute)
				}
			} else if signal == gammacapture.SignalDown {
				r.HealthySignalDown++
				reclaimUntil = now.Add(*reclaimWindow)
				reclaimReference = price
			}
			if position != nil || now.Before(cooldownUntil) {
				continue
			}
			if *mode == "momentum" && (eligibleUntil.IsZero() || now.After(eligibleUntil)) {
				continue
			}
			if *mode == "reclaim" && (reclaimUntil.IsZero() || now.After(reclaimUntil)) {
				continue
			}
			r.SignalWindow++
			if *mode == "momentum" {
				if passage.TP < *entryProbability {
					continue
				}
				r.Probability++
				if edge < *minimumEdge {
					continue
				}
				r.ExpectedEdge++
			} else {
				if price < reclaimReference*math.Exp(float64(*reclaimBars)*(*width)) {
					continue
				}
				r.ReclaimPass++
			}
			if imbalance < *minFlowImbalance || imbalance > *maxFlowImbalance {
				continue
			}
			r.OrderFlowPass++
			r.Trades = append(r.Trades, trade{
				Opened:             now,
				Entry:              price,
				PredictedTP:        passage.TP,
				ExpectedNetEdgeBps: edge,
				Up:                 snapshot.Up,
				Down:               snapshot.Down,
				OrderFlowImbalance: imbalance,
			})
			position = &openTrade{entry: price, qty: *notional / price, opened: now, reportIndex: len(r.Trades) - 1}
			r.Entries++
		}
		if err := file.Close(); err != nil {
			fatalf("close %s: %v", filename, err)
		}
	}
	if position != nil {
		closeTrade(&r, position, lastPrice, lastTime, "end", *actualFeeRate)
		r.OpenAtEnd++
	}
	if !*includeTrades {
		r.Trades = nil
	}
	if err := json.NewEncoder(os.Stdout).Encode(r); err != nil {
		fatalf("write report: %v", err)
	}
}

func closeTrade(r *report, position *openTrade, exitPrice float64, closed time.Time, outcome string, feeRate float64) {
	gross := position.qty * (exitPrice - position.entry)
	fees := position.qty * (exitPrice + position.entry) * feeRate / 2
	r.GrossJPY += gross
	r.FeesJPY += fees
	r.NetJPY += gross - fees
	trade := &r.Trades[position.reportIndex]
	trade.Closed = closed
	trade.Exit = exitPrice
	trade.Outcome = outcome
	trade.NetJPY = gross - fees
	r.recordCalibration(trade)
}

func makeCalibrationBuckets(count int) []calibrationBucket {
	if count <= 0 {
		return nil
	}
	buckets := make([]calibrationBucket, count)
	for i := range buckets {
		buckets[i].LowerProbability = float64(i) / float64(count)
		buckets[i].UpperProbability = float64(i+1) / float64(count)
	}
	return buckets
}

func (r *report) recordCalibration(t *trade) {
	if t == nil || len(r.Calibration) == 0 {
		return
	}
	index := int(math.Floor(t.PredictedTP * float64(len(r.Calibration))))
	if index < 0 {
		index = 0
	}
	if index >= len(r.Calibration) {
		index = len(r.Calibration) - 1
	}
	bucket := &r.Calibration[index]
	bucket.Count++
	bucket.PredictedTPSum += t.PredictedTP
	bucket.NetSumJPY += t.NetJPY
	if t.NetJPY > 0 {
		bucket.PositiveNet++
	}
	switch t.Outcome {
	case "target":
		bucket.TargetHits++
	case "stop":
		bucket.StopHits++
	default:
		bucket.OtherExits++
	}
}

func expectedNetEdgeBps(width float64, target, stop int, costBps float64, passage gammacapture.FirstPassage) float64 {
	bps := width * 10_000
	return passage.TP*float64(target)*bps - passage.SL*float64(stop)*bps - costBps
}

func parseDate(value string) time.Time {
	parsed, err := time.Parse(time.DateOnly, value)
	if err != nil {
		fatalf("parse date %q: %v", value, err)
	}
	return parsed
}

func dateFromFilename(filename string) (time.Time, error) {
	base := strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename))
	if len(base) < len(time.DateOnly) {
		return time.Time{}, fmt.Errorf("filename has no ISO date: %s", filename)
	}
	return time.Parse(time.DateOnly, base[len(base)-len(time.DateOnly):])
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(2)
}
