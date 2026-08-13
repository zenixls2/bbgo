package main

import (
	"encoding/json"
	"math"
	"os"
	"time"

	"github.com/c9s/bbgo/pkg/strategy/gammacapture"
)

type macroReversalComparisonInput struct {
	ConfigPath                  string
	DataPath                    string
	Symbol                      string
	From, To                    time.Time
	PairEquityJPY, StartingBase float64
	QueueMultiplier             float64
	ReplayCacheDir              string
}

type macroReversalComparisonReport struct {
	Symbol                string                 `json:"symbol"`
	From                  time.Time              `json:"from"`
	To                    time.Time              `json:"to"`
	WarmupFrom            time.Time              `json:"warmupFrom"`
	StartingPairEquityJPY float64                `json:"startingPairEquityJPY"`
	StartingBase          float64                `json:"startingBase"`
	QueueMultiplier       float64                `json:"queueMultiplier"`
	ReplayCacheHit        bool                   `json:"replayCacheHit"`
	ConfirmedOnly         productionReplayResult `json:"confirmedOnly"`
	EarlySequential       productionReplayResult `json:"earlySequential"`
}

func replayCappedInventoryCapacity(modelCap, hardCap, minimum float64) float64 {
	// Match live semantics: zero is an authoritative model rejection. A
	// positive sub-minimum allocation may still be rounded to one executable
	// unit when the hard inventory band can absorb it.
	if modelCap <= 0 || hardCap <= 0 {
		return 0
	}
	capacity := math.Min(math.Max(0, modelCap), hardCap)
	if minimum > 0 && hardCap >= minimum && capacity < minimum {
		capacity = minimum
	}
	return math.Min(capacity, hardCap)
}

func (s *productionReplayState) recordEquity(at time.Time, mid, targetRatio float64, direction int, early, applied bool) {
	grossEquity := s.quote + s.inventory*mid
	riskyWeight := 0.0
	if grossEquity > 0 {
		riskyWeight = s.inventory * mid / grossEquity
	}
	point := productionEquityPoint{
		At: at.Truncate(time.Minute), MidJPY: mid,
		EquityJPY:     grossEquity - s.fees,
		HoldEquityJPY: s.initialQuote + s.initialInventory*mid,
		Inventory:     s.inventory, RiskyWeight: riskyWeight, TargetRatio: targetRatio,
		ReversalDirection: direction, EarlyReversal: early, ReversalApplied: applied,
	}
	if point.EquityJPY > s.equityPeak {
		s.equityPeak = point.EquityJPY
	}
	if s.equityPeak > 0 {
		drawdownPct := 100 * (s.equityPeak - point.EquityJPY) / s.equityPeak
		if drawdownPct > s.maximumDrawdownPct {
			s.maximumDrawdownPct = drawdownPct
		}
		if s.maxDrawdownStopPct > 0 && drawdownPct >= s.maxDrawdownStopPct && !s.stopped {
			s.stopped = true
			s.stopAt = at
			s.stopReason = "equity drawdown reached research stop"
		}
	}
	n := len(s.equityCurve)
	if n > 0 && s.equityCurve[n-1].At.Equal(point.At) {
		s.equityCurve[n-1] = point
		return
	}
	s.equityCurve = append(s.equityCurve, point)
}

func macroReplayWarmup(cfg gammacapture.MarketMakerConfig) time.Duration {
	longestMacroHorizon := time.Duration(0)
	for _, horizon := range cfg.MacroInventory.RiskHorizons {
		if value := time.Duration(horizon); value > longestMacroHorizon {
			longestMacroHorizon = value
		}
	}
	macroHistory := time.Duration(cfg.MacroInventory.Lookback) +
		longestMacroHorizon + time.Duration(cfg.MacroInventory.BarInterval)
	warmup := macroHistory
	for _, candidate := range []time.Duration{
		time.Duration(cfg.HorizonLookback) + time.Duration(cfg.MaxTradingWindow),
	} {
		if candidate > warmup {
			warmup = candidate
		}
	}
	return warmup
}

// readMacroReplayBBO keeps the evaluation interval tick-exact but retains only
// the last BBO of each second in the long warm-up. Macro uses closed 10-minute
// bars, while one-second warm-up still preserves the path needed by crossing
// models without holding millions of redundant depth updates in memory.
func readMacroReplayBBO(path, symbol string, from, to, exactFrom time.Time) []bboSnapshot {
	files := replayCaptureFilesOverlapping(replayCaptureFiles(path, symbol, "bookticker"), symbol, "bookticker", from, to)
	return readMacroReplayBBOFiles(files, from, to, exactFrom)
}

func readMacroReplayBBOFiles(files []string, from, to, exactFrom time.Time) []bboSnapshot {
	return compactMacroReplayBBO(readBBOFiles(files, from, to), exactFrom)
}

func compactMacroReplayBBO(raw []bboSnapshot, exactFrom time.Time) []bboSnapshot {
	var out []bboSnapshot
	var pending bboSnapshot
	pendingValid := false
	flushPending := func() {
		if pendingValid {
			out = append(out, pending)
			pendingValid = false
		}
	}
	for _, value := range raw {
		if !value.time.Before(exactFrom) {
			flushPending()
			out = append(out, value)
			continue
		}
		if pendingValid && pending.time.Unix() != value.time.Unix() {
			flushPending()
		}
		pending = value
		pendingValid = true
	}
	flushPending()
	return out
}

func runMacroReversalComparison(in macroReversalComparisonInput) {
	if !in.From.Before(in.To) || in.PairEquityJPY <= 0 || in.StartingBase < 0 || in.QueueMultiplier < 0 {
		fatalf("invalid macro reversal replay interval, balances, or queue multiplier")
	}
	barrier, intensity, cfg := loadProductionConfig(in.ConfigPath, in.Symbol)
	warmupFrom := in.From.Add(-macroReplayWarmup(cfg))
	books, trades, cacheHit := loadMacroReplayDataset(in.DataPath, in.Symbol, warmupFrom, in.To, in.From, replayConfigFingerprint(in.ConfigPath), in.ReplayCacheDir)
	books = compactBBO(books)
	trades = compactTrades(trades)
	if len(books) < 2 || len(trades) == 0 {
		fatalf("insufficient macro reversal replay events: bbo=%d trades=%d", len(books), len(trades))
	}

	confirmedCfg := cfg
	confirmedCfg.MacroInventory.ReversalAccumulation.EarlyDetection = false
	confirmed := simulateProductionPolicy(
		books, trades, confirmedCfg, barrier, intensity, nil, replayLegacy,
		in.Symbol, in.PairEquityJPY, in.StartingBase, in.QueueMultiplier, in.From)
	early := simulateProductionPolicy(
		books, trades, cfg, barrier, intensity, nil, replayLegacy,
		in.Symbol, in.PairEquityJPY, in.StartingBase, in.QueueMultiplier, in.From)

	report := macroReversalComparisonReport{
		Symbol: in.Symbol, From: in.From, To: in.To, WarmupFrom: warmupFrom,
		StartingPairEquityJPY: in.PairEquityJPY, StartingBase: in.StartingBase,
		QueueMultiplier: in.QueueMultiplier, ReplayCacheHit: cacheHit, ConfirmedOnly: confirmed, EarlySequential: early,
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fatalf("encode macro reversal comparison: %v", err)
	}
}
