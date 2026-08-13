package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/c9s/bbgo/pkg/strategy/gammacapture"
)

type noTradeIOCComparisonInput struct {
	ConfigPath                  string
	DataPath                    string
	Symbol                      string
	From, To                    time.Time
	PairEquityJPY, StartingBase float64
	QueueMultiplier             float64
	MaxDrawdownStopPct          float64
	ReplayCacheDir              string
	TrendQVOnly                 bool
	ContinuationQVOnly          bool
	FastVarianceQVOnly          bool
	ContinuationMixtureOnly     bool
	ContinuationMixtureQVOnly   bool
	HoldProtectionOnly          bool
	LiveNoTradeToggleOnly       bool
	FastOnlyFixedHalf           bool
}

type noTradeIOCVariant struct {
	Name                       string
	NoTradeEnabled             bool
	TrendExcursionEnabled      bool
	ContinuationEnabled        bool
	ContinuationMixtureEnabled bool
	FastVarianceEnabled        bool
	ActiveIOCEnabled           bool
	MacroInventoryEnabled      bool
	Config                     gammacapture.MarketMakerConfig
}

type noTradeIOCVariantResult struct {
	Name                       string                 `json:"name"`
	NoTradeEnabled             bool                   `json:"noTradeEnabled"`
	TrendExcursionEnabled      bool                   `json:"trendExcursionEnabled"`
	ContinuationEnabled        bool                   `json:"continuationEnabled"`
	ContinuationMixtureEnabled bool                   `json:"continuationMixtureEnabled"`
	HoldProtectionApplied      bool                   `json:"holdProtectionApplied"`
	FastVarianceEnabled        bool                   `json:"fastVarianceEnabled"`
	ActiveIOCEnabled           bool                   `json:"activeIOCEnabled"`
	MacroInventoryEnabled      bool                   `json:"macroInventoryEnabled"`
	Result                     productionReplayResult `json:"result"`
	ExcessVsHoldJPY            float64                `json:"excessVsHoldJPY"`
	ExcessLower95JPY           float64                `json:"excessLower95JPY"`
	ExcessSamples              int                    `json:"excessSamples"`
}

type replayHoldBenchmark struct {
	StartingEquityJPY  float64 `json:"startingEquityJPY"`
	FinalEquityJPY     float64 `json:"finalEquityJPY"`
	NetPnLJPY          float64 `json:"netPnLJPY"`
	ReturnPct          float64 `json:"returnPct"`
	MaximumDrawdownPct float64 `json:"maximumDrawdownPct"`
}

type noTradeIOCComparisonReport struct {
	Symbol                string                    `json:"symbol"`
	From                  time.Time                 `json:"from"`
	To                    time.Time                 `json:"to"`
	WarmupFrom            time.Time                 `json:"warmupFrom"`
	StartingPairEquityJPY float64                   `json:"startingPairEquityJPY"`
	StartingBase          float64                   `json:"startingBase"`
	QueueMultiplier       float64                   `json:"queueMultiplier"`
	ReplayCacheHit        bool                      `json:"replayCacheHit"`
	Hold                  replayHoldBenchmark       `json:"hold"`
	Variants              []noTradeIOCVariantResult `json:"variants"`
}

func noTradeIOCVariants(cfg gammacapture.MarketMakerConfig) []noTradeIOCVariant {
	specs := []struct {
		name                                                                                                string
		noTrade, trendExcursion, continuation, continuationMixture, fastVariance, holdProtection, activeIOC bool
	}{
		{name: "trend-excursion+ioc", noTrade: true, trendExcursion: true, activeIOC: true},
		{name: "qv-continuation-mixture+ioc", noTrade: true, continuationMixture: true, activeIOC: true},
		{name: "qv-continuation+ioc", noTrade: true, continuation: true, activeIOC: true},
		{name: "qv-fast-variance+ioc", noTrade: true, fastVariance: true, activeIOC: true},
		{name: "qv-only+ioc", noTrade: true, trendExcursion: false, activeIOC: true},
		{name: "trend-excursion+maker", noTrade: true, trendExcursion: true, activeIOC: false},
		{name: "qv-only+maker", noTrade: true, trendExcursion: false, activeIOC: false},
		{name: "qv-hold-protected+ioc", noTrade: true, holdProtection: true, activeIOC: true},
		{name: "qv-hold-protected+maker", noTrade: true, holdProtection: true, activeIOC: false},
		{name: "legacy-macro+ioc", noTrade: false, activeIOC: true},
		{name: "legacy-macro+maker", noTrade: false, activeIOC: false},
	}
	out := make([]noTradeIOCVariant, len(specs))
	for i, spec := range specs {
		variantCfg := cfg
		variantCfg.MacroInventory.NoTradeRegion.Enabled = spec.noTrade
		variantCfg.MacroInventory.NoTradeRegion.TrendExcursionEnabled = spec.trendExcursion
		variantCfg.MacroInventory.NoTradeRegion.ContinuationEnabled = spec.continuation
		variantCfg.MacroInventory.NoTradeRegion.ContinuationMixtureEnabled = spec.continuationMixture
		variantCfg.MacroInventory.NoTradeRegion.FastVarianceRiskEnabled = spec.fastVariance
		variantCfg.MacroInventory.NoTradeRegion.HoldProtectionEnabled = spec.holdProtection
		if spec.holdProtection {
			variantCfg.MacroInventory.NoTradeRegion.HoldProtectionZScore = 1.6448536269514722
		}
		variantCfg.MacroInventory.ReversalAccumulation.ActiveExecution.Enabled = spec.activeIOC
		out[i] = noTradeIOCVariant{
			Name: spec.name, NoTradeEnabled: spec.noTrade,
			TrendExcursionEnabled: spec.trendExcursion,
			ContinuationEnabled:   spec.continuation, ContinuationMixtureEnabled: spec.continuationMixture, FastVarianceEnabled: spec.fastVariance,
			ActiveIOCEnabled: spec.activeIOC, MacroInventoryEnabled: variantCfg.MacroInventory.Enabled, Config: variantCfg,
		}
	}
	fastOnlyCfg := cfg
	fastOnlyCfg.MacroInventory.Enabled = false
	fastOnlyCfg.MacroInventory.NoTradeRegion.Enabled = false
	fastOnlyCfg.MacroInventory.ReversalAccumulation.ActiveExecution.Enabled = false
	fastOnlyCfg.InventoryTargetRatio = 0.5
	fastOnlyCfg.InventoryCapitalMinRatio = 0
	fastOnlyCfg.InventoryCapitalTargetRatio = 0.5
	fastOnlyCfg.InventoryCapitalMaxRatio = 1
	out = append(out, noTradeIOCVariant{
		Name: "fast-only-fixed-half", MacroInventoryEnabled: false,
		Config: fastOnlyCfg,
	})
	return out
}

func selectNoTradeIOCVariants(
	variants []noTradeIOCVariant,
	names ...string,
) []noTradeIOCVariant {
	selected := make([]noTradeIOCVariant, 0, len(names))
	for _, name := range names {
		for _, variant := range variants {
			if variant.Name == name {
				selected = append(selected, variant)
				break
			}
		}
	}
	return selected
}

func validateSpotReplayStartingBalance(books []bboSnapshot, from time.Time, pairEquityJPY, startingBase float64) error {
	start := sort.Search(len(books), func(i int) bool { return !books[i].time.Before(from) })
	if start >= len(books) {
		return fmt.Errorf("no opening BBO at or after %s", from.Format(time.RFC3339))
	}
	startMid := (books[start].bid + books[start].ask) / 2
	baseValue := startingBase * startMid
	if baseValue > pairEquityJPY+1e-6 {
		return fmt.Errorf("starting base value %.8f JPY exceeds pair equity %.8f JPY", baseValue, pairEquityJPY)
	}
	return nil
}

func holdBenchmark(books []bboSnapshot, from time.Time, pairEquityJPY, startingBase float64) replayHoldBenchmark {
	benchmark := replayHoldBenchmark{StartingEquityJPY: pairEquityJPY}
	start := sort.Search(len(books), func(i int) bool { return !books[i].time.Before(from) })
	if start >= len(books) || pairEquityJPY <= 0 || startingBase < 0 {
		return benchmark
	}
	startMid := (books[start].bid + books[start].ask) / 2
	quote := pairEquityJPY - startingBase*startMid
	peak := pairEquityJPY
	for _, book := range books[start:] {
		mid := (book.bid + book.ask) / 2
		equity := quote + startingBase*mid
		benchmark.FinalEquityJPY = equity
		if equity > peak {
			peak = equity
		}
		if peak > 0 {
			benchmark.MaximumDrawdownPct = math.Max(
				benchmark.MaximumDrawdownPct, 100*(peak-equity)/peak)
		}
	}
	benchmark.NetPnLJPY = benchmark.FinalEquityJPY - pairEquityJPY
	benchmark.ReturnPct = 100 * benchmark.NetPnLJPY / pairEquityJPY
	return benchmark
}

func pairedExcessLower95(result productionReplayResult) (mean, lower float64, samples int) {
	curve := result.EquityCurve
	if len(curve) < 2 {
		return 0, 0, 0
	}
	start := curve[0].At
	previousExcess := 0.0
	nextBoundary := start.Add(time.Hour)
	values := make([]float64, 0)
	for _, point := range curve[1:] {
		if point.At.Before(nextBoundary) {
			continue
		}
		excess := point.EquityJPY - point.HoldEquityJPY
		values = append(values, excess-previousExcess)
		previousExcess = excess
		nextBoundary = nextBoundary.Add(time.Hour)
	}
	if len(values) == 0 {
		return 0, 0, 0
	}
	for _, value := range values {
		mean += value
	}
	mean /= float64(len(values))
	if len(values) == 1 {
		return mean, mean, 1
	}
	var sumSquares float64
	for _, value := range values {
		sumSquares += (value - mean) * (value - mean)
	}
	standardError := math.Sqrt(sumSquares / float64(len(values)-1) / float64(len(values)))
	return mean, mean - 1.6448536269514722*standardError, len(values)
}

func runNoTradeIOCComparison(in noTradeIOCComparisonInput) {
	if !in.From.Before(in.To) || in.PairEquityJPY <= 0 || in.StartingBase < 0 || in.QueueMultiplier < 0 {
		fatalf("invalid no-trade/IOC comparison interval, balances, or queue multiplier")
	}
	barrier, intensity, cfg := loadProductionConfig(in.ConfigPath, in.Symbol)
	warmupFrom := in.From.Add(-macroReplayWarmup(cfg))
	books, trades, cacheHit := loadMacroReplayDataset(
		in.DataPath, in.Symbol, warmupFrom, in.To, in.From,
		replayConfigFingerprint(in.ConfigPath), in.ReplayCacheDir)
	books = compactBBO(books)
	trades = compactTrades(trades)
	if len(books) < 2 || len(trades) == 0 {
		fatalf("insufficient no-trade/IOC replay events: bbo=%d trades=%d", len(books), len(trades))
	}
	if err := validateSpotReplayStartingBalance(books, in.From, in.PairEquityJPY, in.StartingBase); err != nil {
		fatalf("invalid no-trade/IOC starting balance: %v", err)
	}

	variants := noTradeIOCVariants(cfg)
	if in.TrendQVOnly {
		variants = selectNoTradeIOCVariants(
			variants, "trend-excursion+ioc", "qv-only+ioc")
	}
	if in.ContinuationQVOnly {
		variants = selectNoTradeIOCVariants(
			variants, "qv-continuation+ioc", "qv-only+ioc")
	}
	if in.FastVarianceQVOnly {
		variants = selectNoTradeIOCVariants(
			variants, "qv-fast-variance+ioc", "qv-only+ioc")
	}
	if in.ContinuationMixtureOnly {
		variants = selectNoTradeIOCVariants(variants, "qv-continuation-mixture+ioc")
	}
	if in.ContinuationMixtureQVOnly {
		variants = selectNoTradeIOCVariants(variants, "qv-continuation-mixture+ioc", "qv-only+ioc")
	}
	if in.HoldProtectionOnly {
		variants = selectNoTradeIOCVariants(variants, "qv-hold-protected+ioc", "qv-only+ioc")
	}
	if in.LiveNoTradeToggleOnly {
		variants = selectNoTradeIOCVariants(variants, "qv-hold-protected+maker", "legacy-macro+maker")
	}
	if in.FastOnlyFixedHalf {
		variants = selectNoTradeIOCVariants(variants, "fast-only-fixed-half")
	}
	results := make([]noTradeIOCVariantResult, len(variants))
	var wg sync.WaitGroup
	for i, variant := range variants {
		wg.Add(1)
		go func(index int, candidate noTradeIOCVariant) {
			defer wg.Done()
			result := simulateProductionPolicyWithQuantityProjection(
				books, trades, candidate.Config, barrier, intensity, nil, replayLegacy,
				in.Symbol, in.PairEquityJPY, in.StartingBase, in.QueueMultiplier,
				in.From, candidate.Config.ProbabilityCenteredQuantity.Enabled && !candidate.Config.ProbabilityCenteredQuantity.ShadowOnly, in.MaxDrawdownStopPct)
			results[index] = noTradeIOCVariantResult{
				Name: candidate.Name, NoTradeEnabled: candidate.NoTradeEnabled,
				TrendExcursionEnabled: candidate.TrendExcursionEnabled,
				ContinuationEnabled:   candidate.ContinuationEnabled, ContinuationMixtureEnabled: candidate.ContinuationMixtureEnabled, FastVarianceEnabled: candidate.FastVarianceEnabled,
				ActiveIOCEnabled: candidate.ActiveIOCEnabled, MacroInventoryEnabled: candidate.MacroInventoryEnabled, Result: result,
			}
		}(i, variant)
	}
	wg.Wait()

	report := noTradeIOCComparisonReport{
		Symbol: in.Symbol, From: in.From, To: in.To, WarmupFrom: warmupFrom,
		StartingPairEquityJPY: in.PairEquityJPY, StartingBase: in.StartingBase,
		QueueMultiplier: in.QueueMultiplier, ReplayCacheHit: cacheHit,
		Hold:     holdBenchmark(books, in.From, in.PairEquityJPY, in.StartingBase),
		Variants: results,
	}
	for index := range report.Variants {
		mean, lower, samples := pairedExcessLower95(report.Variants[index].Result)
		report.Variants[index].ExcessVsHoldJPY = report.Variants[index].Result.NetPnLJPY - report.Hold.NetPnLJPY
		report.Variants[index].ExcessLower95JPY = lower
		report.Variants[index].ExcessSamples = samples
		_ = mean
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fatalf("encode no-trade/IOC comparison: %v", err)
	}
}
