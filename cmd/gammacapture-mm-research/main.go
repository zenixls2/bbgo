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
	"runtime/pprof"
	"sort"
	"time"

	"github.com/c9s/bbgo/pkg/datasource/csvsource"
	"github.com/c9s/bbgo/pkg/strategy/gammacapture"
	"github.com/c9s/bbgo/pkg/types"
)

type tick struct {
	id    uint64
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
	ExecutionEvents            int     `json:"executionEvents"`
	PartialFillEvents          int     `json:"partialFillEvents"`
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
	Mode                  string      `json:"mode"`
	Symbol                string      `json:"symbol"`
	TrainFrom             string      `json:"trainFrom"`
	TrainTo               string      `json:"trainTo"`
	HoldoutFrom           string      `json:"holdoutFrom"`
	HoldoutTo             string      `json:"holdoutTo"`
	TrainHistoricalBBO    bool        `json:"trainHistoricalBBOAvailable"`
	HistoricalBBO         bool        `json:"historicalBBOAvailable"`
	BBOEvents             int         `json:"bboEvents"`
	AggTradeEvents        int         `json:"aggTradeEvents"`
	TrainReplayCacheHit   bool        `json:"trainReplayCacheHit"`
	HoldoutReplayCacheHit bool        `json:"holdoutReplayCacheHit"`
	SampleHours           float64     `json:"sampleHours"`
	Warning               string      `json:"warning"`
	Selected              result      `json:"selected"`
	TrainCandidates       []result    `json:"trainCandidates"`
	Holdout               result      `json:"holdout"`
	TickerStats           tickerStats `json:"tickerStats"`
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
	takerFee := flag.Float64("taker-fee-bps", 10, "taker fee for acquisition entry (Binance default: 10 bps)")
	acquisitionSlippage := flag.Float64("acquisition-slippage-bps", 5, "IOC acquisition slippage allowance")
	adverse := flag.Float64("adverse-selection-bps", 2, "one-sided adverse selection allowance")
	minimumEdge := flag.Float64("minimum-net-edge-bps", 2, "round-trip residual edge required")
	acquisitionLabels := flag.Bool("acquisition-labels", false, "report independent fee-positive chase-start labels and causal holdout coverage")
	earlyBumpStudy := flag.Bool("early-bump-study", false, "paired replay of baseline and urgency bids around fixed early-bump signals")
	earlyBumpTestFrom := flag.String("early-bump-test-from", "", "chronological evaluation boundary for the fixed early-bump artifact")
	earlyBumpBaseDistance := flag.Float64("early-bump-base-distance-bps", 30, "baseline passive bid distance used by paired early-bump replay")
	earlyBumpRebound := flag.Float64("early-bump-rebound-bps", 7, "minimum trailing-30s rebound for early-bump sensitivity")
	acquisitionTestFrom := flag.String("acquisition-test-from", "", "fixed untouched-test boundary for stable acquisition selection (RFC3339 or date)")
	quoteNotional := flag.Float64("quote-notional-jpy", 50_000, "notional per quote")
	minOrderNotional := flag.Float64("min-order-notional-jpy", 100, "exchange minimum order notional")
	statsQuoteDistance := flag.Float64("stats-quote-distance-bps", 15, "quote distance used for ticker crossing statistics")
	inventoryLimit := flag.Float64("inventory-limit", 100, "base-unit inventory limit; larger limits are required for high turnover but increase inventory risk")
	inventoryTargetRatio := flag.Float64("inventory-target-ratio", 0.5, "spot inventory target as a fraction of the hard base-unit inventory limit")
	startingQuote := flag.Float64("starting-quote-jpy", 1_000_000, "starting quote balance")
	minFillsPerDay := flag.Float64("min-fills-per-day", 200, "minimum training turnover target")
	minTradingWindow := flag.Duration("min-trading-window", 10*time.Minute, "minimum order-retention/statistical horizon")
	maxTradingWindow := flag.Duration("max-trading-window", 30*time.Minute, "maximum order-retention exposure horizon")
	fixedHalfSpread := flag.Float64("fixed-half-spread-bps", 0, "evaluate only this half spread; zero keeps the training grid")
	fixedInventorySkew := flag.Float64("fixed-inventory-skew-bps", -1, "evaluate only this inventory skew; negative keeps the training grid")
	fixedVolatilityMult := flag.Float64("fixed-volatility-multiplier", 0, "evaluate only this volatility multiplier; zero keeps the training grid")
	productionCompare := flag.Bool("production-compare", false, "compare legacy and horizon-touch policies with the production-state event replay")
	macroReversalCompare := flag.Bool("macro-reversal-compare", false, "compare confirmed-only and early-sequential Macro reversal paths")
	quantityProjectionCompare := flag.Bool("quantity-projection-compare", false, "compare staged and probability-centered quantity policies")
	quantityProjectionCurrentOnly := flag.Bool("quantity-projection-current-only", false, "run only the current probability-centered policy")
	postFillUtilityCompare := flag.Bool("post-fill-utility-compare", false, "compare current policy with confidence-adjusted post-fill utility")
	noTradeIOCCompare := flag.Bool("no-trade-ioc-compare", false, "comparison of trend/QV no-trade, legacy Macro, and bounded IOC")
	trendQVOnly := flag.Bool("trend-qv-only", false, "with --no-trade-ioc-compare, run only trend-excursion+ioc and qv-only+ioc")
	continuationQVOnly := flag.Bool("continuation-qv-only", false, "with --no-trade-ioc-compare, run only qv-continuation+ioc and qv-only+ioc")
	fastVarianceQVOnly := flag.Bool("fast-variance-qv-only", false, "with --no-trade-ioc-compare, run only qv-fast-variance+ioc and qv-only+ioc")
	continuationMixtureOnly := flag.Bool("continuation-mixture-only", false, "with --no-trade-ioc-compare, run only the single-target QV+continuation predictive mixture")
	continuationMixtureQVOnly := flag.Bool("continuation-mixture-qv-only", false, "with --no-trade-ioc-compare, compare continuation posterior with QV fallback")
	holdProtectionOnly := flag.Bool("hold-protection-only", false, "with --no-trade-ioc-compare, compare QV no-trade with and without hold protection")
	liveNoTradeToggleOnly := flag.Bool("live-no-trade-toggle-only", false, "with --no-trade-ioc-compare, compare current legacy maker with hold-protected no-trade maker")
	fastOnlyFixedHalf := flag.Bool("fast-only-fixed-half", false, "with --no-trade-ioc-compare, run only Fast quoting around a fixed 50/50 inventory target with Macro and IOC disabled")
	fastOnlyFixedHalfCompare := flag.Bool("fast-only-fixed-half-compare", false, "with --no-trade-ioc-compare, compare fixed-half fast with and without Hawkes")
	multiscaleRegimeStudy := flag.Bool("multiscale-regime-study", false, "evaluate the standalone one-minute Bayesian regime model without Macro or order simulation")
	multiscaleVarianceStudy := flag.Bool("multiscale-variance-study", false, "evaluate standalone side-specific online HAR variance forecasts without Macro or orders")
	drawdownEProcessStudy := flag.Bool("drawdown-eprocess-study", false, "evaluate the standalone QV-time drawdown/recovery e-process without Macro or orders")
	consolidationHazardStudy := flag.Bool("consolidation-hazard-study", false, "evaluate causal short-consolidation down/up competing risks without Macro or orders")
	rangeRegimeStudy := flag.Bool("range-regime-study", false, "select non-overlapping ETHJPY ranging windows without using strategy P&L")
	rangeWindow := flag.Duration("range-window", 12*time.Hour, "window length for blind ranging-regime selection")
	rangeStep := flag.Duration("range-step", 6*time.Hour, "candidate step for blind ranging-regime selection")
	rangeCount := flag.Int("range-count", 6, "maximum non-overlapping ranging windows to report")
	regimeHorizon := flag.Duration("regime-horizon", 3*time.Hour, "executable first-passage horizon for the standalone regime study")
	regimeAnchorStep := flag.Duration("regime-anchor-step", 3*time.Hour, "non-overlapping prediction anchor spacing for the standalone regime study")
	maxDrawdownStopPct := flag.Float64("max-drawdown-stop-pct", 0, "stop production replay when equity drawdown reaches this percentage; zero disables")
	replayCacheDir := flag.String("replay-cache-dir", "data/gammacapture/state/replay-cache", "deterministic parsed replay cache; empty disables")
	cpuProfilePath := flag.String("cpu-profile", "", "write a Go CPU profile for replay performance analysis")
	overrideRiskBudgetRatio := flag.Float64("override-inventory-risk-budget-ratio", -1, "research-only inventory risk budget ratio")
	overrideRiskZScore := flag.Float64("override-inventory-risk-z-score", -1, "research-only inventory confidence z-score")
	overrideMinimumHalfSpread := flag.Float64("override-minimum-half-spread-bps", -1, "research-only minimum half spread")
	overrideVolatilityMultiplier := flag.Float64("override-volatility-multiplier", -1, "research-only quote volatility multiplier")
	overrideMinimumNetEdge := flag.Float64("override-minimum-net-edge-bps", -1, "research-only minimum residual edge")
	overrideMaxTradingWindow := flag.Duration("override-max-trading-window", -1, "research-only maximum trading window")
	overrideHorizonLookback := flag.Duration("override-horizon-lookback", -1, "research-only horizon lookback")
	overrideHorizonMinSamples := flag.Int("override-horizon-min-samples", -1, "research-only horizon minimum effective samples")
	overrideMacroRiskAversion := flag.Float64("override-macro-risk-aversion", -1, "research-only Macro risk aversion")
	overrideMacroCarryBudget := flag.Float64("override-macro-carry-risk-budget-ratio", -1, "research-only Macro carry risk budget ratio")
	overrideMacroBarInterval := flag.Duration("override-macro-bar-interval", -1, "research-only Macro bar interval")
	disableJointDistanceQuantity := flag.Bool("disable-joint-distance-quantity", false, "research-only disable joint distance/quantity optimizer")
	overrideJointDistanceCandidates := flag.Int("override-joint-distance-candidates", -1, "research-only joint distance ladder candidate count")
	replayFrom := flag.String("replay-from", "", "exact Macro replay start (RFC3339)")
	replayTo := flag.String("replay-to", "", "exact Macro replay end (RFC3339)")
	configPath := flag.String("config", "config/gammacapture.yaml", "GammaCapture YAML configuration used by production replay")
	horizonTouchPath := flag.String("horizon-touch-model", "config/gammacapture-horizon-touch-soljpy.json", "accepted horizon-touch artifact used by production replay")
	pairEquity := flag.Float64("pair-equity-jpy", 7_255, "starting SOLJPY quote-equivalent equity for production replay")
	startingBase := flag.Float64("starting-base", 0.287, "starting total base balance for production replay")
	queueMultiplier := flag.Float64("queue-multiplier", -1, "visible-BBO queue multiplier; negative calibrates from confirmed fills")
	calibrationFrom := flag.String("calibration-from", "2026-07-23T07:54:00Z", "production replay calibration start (RFC3339 or date)")
	calibrationTo := flag.String("calibration-to", "2026-07-25T15:51:30Z", "production replay calibration end (RFC3339 or date)")
	actualBuyFills := flag.Int("actual-buy-fills", 2, "confirmed maker BUY fills in the calibration interval (overridden by --journal-data)")
	actualSellFills := flag.Int("actual-sell-fills", 1, "confirmed maker SELL fills in the calibration interval (overridden by --journal-data)")
	journalData := flag.String("journal-data", "", "exported userspace journal JSONL; reconstructs actual gcmm LIMIT_MAKER lifecycles")
	lifecycleOnly := flag.Bool("lifecycle-only", false, "report journal maker lifecycles without replaying either quote policy")
	flag.Parse()
	activeProductionConfigOverrides = productionConfigOverrides{
		InventoryRiskBudgetRatio:     *overrideRiskBudgetRatio,
		InventoryRiskZScore:          *overrideRiskZScore,
		MinimumHalfSpreadBps:         *overrideMinimumHalfSpread,
		VolatilityMultiplier:         *overrideVolatilityMultiplier,
		MinimumNetEdgeBps:            *overrideMinimumNetEdge,
		MinimumNetEdgeSet:            *overrideMinimumNetEdge >= 0,
		MaxTradingWindow:             *overrideMaxTradingWindow,
		HorizonLookback:              *overrideHorizonLookback,
		HorizonMinSamples:            *overrideHorizonMinSamples,
		MacroRiskAversion:            *overrideMacroRiskAversion,
		MacroCarryRiskBudget:         *overrideMacroCarryBudget,
		MacroBarInterval:             *overrideMacroBarInterval,
		DisableJointDistanceQuantity: *disableJointDistanceQuantity,
		JointDistanceCandidateCount:  *overrideJointDistanceCandidates,
	}
	if *cpuProfilePath != "" {
		profileFile, err := os.Create(*cpuProfilePath)
		if err != nil {
			fatalf("create CPU profile: %v", err)
		}
		if err := pprof.StartCPUProfile(profileFile); err != nil {
			_ = profileFile.Close()
			fatalf("start CPU profile: %v", err)
		}
		defer func() {
			pprof.StopCPUProfile()
			_ = profileFile.Close()
		}()
	}
	trainStart, trainEnd := parseDate(*trainFrom), parseDate(*trainTo)
	holdStart, holdEnd := parseDate(*holdoutFrom), parseDate(*holdoutTo)
	if !trainStart.Before(trainEnd) || !holdStart.Before(holdEnd) || *fee < 0 || *takerFee < 0 || *acquisitionSlippage < 0 || *adverse < 0 || *inventoryLimit <= 0 || *inventoryTargetRatio < 0 || *inventoryTargetRatio > 1 || *quoteNotional <= 0 || *minOrderNotional <= 0 || *statsQuoteDistance <= 0 || *minTradingWindow <= 0 || *maxTradingWindow < *minTradingWindow {
		fatalf("invalid date or fee/inventory configuration")
	}
	if *rangeRegimeStudy {
		if *replayFrom == "" || *replayTo == "" || *rangeWindow <= 0 || *rangeStep <= 0 || *rangeCount <= 0 {
			fatalf("--range-regime-study requires replay bounds and positive range parameters")
		}
		runRangeRegimeStudy(rangeRegimeStudyInput{
			DataPath: *bboData, Symbol: *symbol, From: parseTime(*replayFrom), To: parseTime(*replayTo),
			Window: *rangeWindow, Step: *rangeStep, MaximumWindows: *rangeCount,
		})
		return
	}
	if *consolidationHazardStudy {
		if *regimeHorizon <= 0 {
			fatalf("consolidation hazard horizon must be positive")
		}
		_, _, maker := loadProductionConfig(*configPath, *symbol)
		windows := maker.FastModelWindows()
		if len(windows) < 2 {
			fatalf("consolidation hazard requires at least two fast-model windows")
		}
		runConsolidationHazardStudy(consolidationHazardStudyInput{
			DataPath: *bboData, Symbol: *symbol, From: holdStart, To: holdEnd,
			Horizon: *regimeHorizon, ShortWindow: windows[0], LongWindow: windows[len(windows)-1],
			DecisionStep:     10 * time.Minute,
			RoundTripCostBps: 2*maker.MakerFeeBps + 2*maker.AdverseSelectionBps + maker.MinimumNetEdgeBps,
			ConfidenceZ:      maker.InventoryRiskZScore, MinimumSamples: 8,
		})
		return
	}
	if *drawdownEProcessStudy {
		if *regimeHorizon <= 0 {
			fatalf("drawdown e-process horizon must be positive")
		}
		barrier, _, maker := loadProductionConfig(*configPath, *symbol)
		runDrawdownEProcessStudy(drawdownEProcessStudyInput{
			DataPath: *bboData, Symbol: *symbol, From: holdStart, To: holdEnd,
			Horizon:          *regimeHorizon,
			RoundTripCostBps: 2*maker.MakerFeeBps + 2*maker.AdverseSelectionBps + maker.MinimumNetEdgeBps,
			BarrierWidth:     barrier.Width, ConfidenceZ: maker.InventoryRiskZScore, Windows: maker.FastModelWindows(),
		})
		return
	}
	if *multiscaleVarianceStudy {
		if *regimeHorizon <= 0 || *regimeAnchorStep < *regimeHorizon {
			fatalf("variance horizon must be positive and anchor step must be at least the horizon")
		}
		runMultiscaleVarianceStudy(multiscaleVarianceStudyInput{
			DataPath: *bboData, Symbol: *symbol, From: holdStart, To: holdEnd,
			Horizon: *regimeHorizon, AnchorStep: *regimeAnchorStep,
		})
		return
	}
	if *multiscaleRegimeStudy {
		if *regimeHorizon <= 0 || *regimeAnchorStep < *regimeHorizon {
			fatalf("regime horizon must be positive and anchor step must be at least the horizon")
		}
		runMultiscaleRegimeStudy(multiscaleRegimeStudyInput{
			DataPath: *bboData, Symbol: *symbol, From: holdStart, To: holdEnd,
			Horizon: *regimeHorizon, AnchorStep: *regimeAnchorStep,
			RoundTripCostBps: 2 * *fee,
			HazardMeans:      []time.Duration{time.Hour, 3 * time.Hour, 6 * time.Hour},
		})
		return
	}
	if *earlyBumpStudy {
		testFrom := time.Time{}
		if *earlyBumpTestFrom != "" {
			testFrom = parseTime(*earlyBumpTestFrom)
		}
		if *earlyBumpBaseDistance <= 0 || *earlyBumpRebound < 0 {
			fatalf("early-bump base distance must be positive")
		}
		runEarlyBumpStudy(earlyBumpStudyInput{
			DataPath: *bboData, Symbol: *symbol, From: holdStart, To: holdEnd, TestFrom: testFrom,
			BaseDistanceBps: *earlyBumpBaseDistance,
			MakerFeeBps:     *fee, AdverseSelectionBps: *adverse, MinimumNetEdgeBps: *minimumEdge,
			Drawdown5mBps: 15, Rebound30sBps: *earlyBumpRebound,
			ExitRebound30sBps: *earlyBumpRebound / 2,
			LockDuration:      30 * time.Second, Cooldown: 2 * time.Minute,
			EscapeBarrierBps: 10, AdverseBarrierBps: 10, EscapeHorizon: 2 * time.Minute,
			MarkoutHorizon: 10 * time.Minute, DeltasBps: []float64{0, 3, 5, 7, 10, 15, 20, 25, 30},
		})
		return
	}
	if *acquisitionLabels {
		testFrom := time.Time{}
		if *acquisitionTestFrom != "" {
			testFrom = parseTime(*acquisitionTestFrom)
		}
		runAcquisitionLabelStudy(acquisitionLabelInput{DataPath: *bboData, Symbol: *symbol, From: holdStart, To: holdEnd, Horizon: 10 * time.Minute,
			TestFrom: testFrom, MakerFeeBps: *fee, TakerFeeBps: *takerFee, SlippageBps: *acquisitionSlippage, AdverseBps: *adverse, MinimumNetEdgeBps: *minimumEdge})
		return
	}
	if *lifecycleOnly {
		if *journalData == "" {
			fatalf("--lifecycle-only requires --journal-data")
		}
		from, to := parseTime(*calibrationFrom), parseTime(*calibrationTo)
		trades := compactTrades(readLiveTrades(*bboData, *symbol, from, to))
		lifecycle := buildOrderLifecycleReport(*journalData, *symbol, from, to, trades)
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(lifecycle); err != nil {
			fatalf("encode lifecycle report: %v", err)
		}
		return
	}
	if *noTradeIOCCompare {
		if *replayFrom == "" || *replayTo == "" {
			fatalf("--no-trade-ioc-compare requires --replay-from and --replay-to")
		}
		runNoTradeIOCComparison(noTradeIOCComparisonInput{
			ConfigPath: *configPath, DataPath: *bboData, Symbol: *symbol,
			From: parseTime(*replayFrom), To: parseTime(*replayTo),
			PairEquityJPY: *pairEquity, StartingBase: *startingBase,
			QueueMultiplier: *queueMultiplier, MaxDrawdownStopPct: *maxDrawdownStopPct,
			ReplayCacheDir: *replayCacheDir, TrendQVOnly: *trendQVOnly,
			ContinuationQVOnly: *continuationQVOnly, FastVarianceQVOnly: *fastVarianceQVOnly, ContinuationMixtureOnly: *continuationMixtureOnly, ContinuationMixtureQVOnly: *continuationMixtureQVOnly, HoldProtectionOnly: *holdProtectionOnly, LiveNoTradeToggleOnly: *liveNoTradeToggleOnly, FastOnlyFixedHalf: *fastOnlyFixedHalf, FastOnlyFixedHalfCompare: *fastOnlyFixedHalfCompare,
		})
		return
	}
	if *postFillUtilityCompare {
		if *replayFrom == "" || *replayTo == "" {
			fatalf("--post-fill-utility-compare requires --replay-from and --replay-to")
		}
		runPostFillUtilityComparison(postFillUtilityComparisonInput{
			ConfigPath: *configPath, DataPath: *bboData, Symbol: *symbol,
			From: parseTime(*replayFrom), To: parseTime(*replayTo),
			PairEquityJPY: *pairEquity, StartingBase: *startingBase,
			QueueMultiplier: *queueMultiplier, ReplayCacheDir: *replayCacheDir,
			MaxDrawdownStopPct: *maxDrawdownStopPct,
		})
		return
	}

	if *quantityProjectionCompare {
		if *replayFrom == "" || *replayTo == "" {
			fatalf("--quantity-projection-compare requires --replay-from and --replay-to")
		}
		runQuantityProjectionComparison(quantityProjectionComparisonInput{
			ConfigPath: *configPath, DataPath: *bboData, Symbol: *symbol,
			From: parseTime(*replayFrom), To: parseTime(*replayTo),
			PairEquityJPY: *pairEquity, StartingBase: *startingBase,
			QueueMultiplier: *queueMultiplier,
			ReplayCacheDir:  *replayCacheDir,
			CurrentOnly:     *quantityProjectionCurrentOnly, MaxDrawdownStopPct: *maxDrawdownStopPct,
		})
		return
	}
	if *macroReversalCompare {
		if *replayFrom == "" || *replayTo == "" {
			fatalf("--macro-reversal-compare requires --replay-from and --replay-to")
		}
		runMacroReversalComparison(macroReversalComparisonInput{
			ConfigPath: *configPath, DataPath: *bboData, Symbol: *symbol,
			From: parseTime(*replayFrom), To: parseTime(*replayTo),
			PairEquityJPY: *pairEquity, StartingBase: *startingBase,
			QueueMultiplier: *queueMultiplier,
			ReplayCacheDir:  *replayCacheDir,
		})
		return
	}
	if *productionCompare {
		if *pairEquity <= 0 || *startingBase < 0 || *actualBuyFills < 0 || *actualSellFills < 0 {
			fatalf("invalid production replay balance or fill calibration")
		}
		runProductionComparison(productionComparisonInput{
			ConfigPath: *configPath, ModelPath: *horizonTouchPath, DataPath: *bboData,
			Symbol: *symbol, From: holdStart, To: holdEnd,
			PairEquityJPY: *pairEquity, StartingBase: *startingBase,
			QueueMultiplier: *queueMultiplier,
			CalibrationFrom: parseTime(*calibrationFrom), CalibrationTo: parseTime(*calibrationTo),
			ActualBuyFills: *actualBuyFills, ActualSellFills: *actualSellFills,
			JournalPath:    *journalData,
			ReplayCacheDir: *replayCacheDir,
		})
		return
	}
	trainBBO, trainCapturedTrades, trainReplayCacheHit := loadExactReplayDataset(*bboData, *symbol, trainStart, trainEnd, "", *replayCacheDir)
	holdoutBBO, holdoutCapturedTrades, holdoutReplayCacheHit := loadExactReplayDataset(*bboData, *symbol, holdStart, holdEnd, "", *replayCacheDir)
	trainTicks := mergeReplayTrades(readTicks(*dataPath, trainStart, trainEnd), trainCapturedTrades)
	holdoutTicks := mergeReplayTrades(readTicks(*dataPath, holdStart, holdEnd), holdoutCapturedTrades)
	trainBBO = compactBBO(trainBBO)
	holdoutBBO = compactBBO(holdoutBBO)
	trainHistoricalBBO := len(trainBBO) > 1
	historicalBBO := len(holdoutBBO) > 1
	if trainHistoricalBBO != historicalBBO {
		fatalf("training and holdout must use the same simulator: train historical BBO=%t holdout historical BBO=%t", trainHistoricalBBO, historicalBBO)
	}
	if len(trainTicks) == 0 || len(holdoutTicks) == 0 {
		fatalf("no trades in requested train or holdout range")
	}
	trainBars := aggregateBars(trainTicks)
	holdoutBars := aggregateBars(holdoutTicks)
	if !historicalBBO && (len(trainBars) == 0 || len(holdoutBars) == 0) {
		fatalf("no synthetic bars in requested train or holdout range")
	}

	inventoryTarget := *inventoryLimit * *inventoryTargetRatio
	newCandidateConfig := func(half, skew, volMult float64) gammacapture.MarketMakerConfig {
		return gammacapture.MarketMakerConfig{
			MakerFeeBps: *fee, AdverseSelectionBps: *adverse, MinimumNetEdgeBps: *minimumEdge,
			MinimumHalfSpreadBps: half, MaximumHalfSpreadBps: 80, VolatilityMultiplier: volMult,
			InventoryTarget: inventoryTarget, InventoryLimit: *inventoryLimit,
			InventorySkewBps: skew, QuoteNotional: *quoteNotional,
			MinTradingWindow: types.Duration(*minTradingWindow), MaxTradingWindow: types.Duration(*maxTradingWindow),
			MaxRefreshInterval: types.Duration(*maxTradingWindow),
		}
	}
	simulatePeriod := func(ticks []tick, books []bboSnapshot, bars []minuteBar, cfg gammacapture.MarketMakerConfig) result {
		if historicalBBO {
			return simulateEventReplay(ticks, books, cfg, *startingQuote, *minOrderNotional)
		}
		return simulate(bars, cfg, *startingQuote, *minOrderNotional)
	}

	halfSpreads := []float64{1, 2, 4, 6, 8, 10, 15, 20, 30, 40}
	inventorySkews := []float64{0, 10, 20}
	volatilityMultipliers := []float64{0.05, 0.1, 0.25}
	if *fixedHalfSpread > 0 {
		halfSpreads = []float64{*fixedHalfSpread}
	}
	if *fixedInventorySkew >= 0 {
		inventorySkews = []float64{*fixedInventorySkew}
	}
	if *fixedVolatilityMult > 0 {
		volatilityMultipliers = []float64{*fixedVolatilityMult}
	}
	candidates := make([]result, 0, len(halfSpreads)*len(inventorySkews)*len(volatilityMultipliers))
	for _, half := range halfSpreads {
		for _, skew := range inventorySkews {
			for _, volMult := range volatilityMultipliers {
				candidate := simulatePeriod(trainTicks, trainBBO, trainBars, newCandidateConfig(half, skew, volMult))
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
	holdoutConfig := newCandidateConfig(selected.HalfSpreadBps, selected.InventorySkewBps, selected.VolatilityMult)
	holdout := simulatePeriod(holdoutTicks, holdoutBBO, holdoutBars, holdoutConfig)
	holdout.VolatilityMult = selected.VolatilityMult
	holdout.MeetsTurnoverGoal = holdout.FillsPerDay >= *minFillsPerDay

	warning := "Train and holdout both use synthetic 1m high/low crossing; no BBO or queue position is available."
	mode := "synthetic_bbo_market_maker_1m"
	if historicalBBO {
		mode = "historical_bbo_aggtrade_market_maker_event_replay"
		warning = "Train and holdout both use captured BBO and deduplicated aggressive trades; queue position is estimated from visible BBO size, not exchange-confirmed fills."
	}
	sampleHours := 0.0
	if len(holdoutBBO) > 1 {
		sampleHours = holdoutBBO[len(holdoutBBO)-1].time.Sub(holdoutBBO[0].time).Hours()
	}
	if historicalBBO && (sampleHours < 24 || holdout.Fills < 100) {
		warning += " Sample is still too short or has too few full fills for a stable-profit claim; continue capture before selecting parameters."
	}
	r := report{
		Mode: mode, Symbol: *symbol, TrainFrom: trainStart.Format(time.DateOnly), TrainTo: trainEnd.Format(time.DateOnly),
		HoldoutFrom: holdStart.Format(time.DateOnly), HoldoutTo: holdEnd.Format(time.DateOnly),
		TrainHistoricalBBO: trainHistoricalBBO, HistoricalBBO: historicalBBO,
		BBOEvents: len(holdoutBBO), AggTradeEvents: len(holdoutTicks), SampleHours: sampleHours,
		TrainReplayCacheHit: trainReplayCacheHit, HoldoutReplayCacheHit: holdoutReplayCacheHit,
		Warning: warning, Selected: selected, TrainCandidates: candidates, Holdout: holdout,
	}
	r.TickerStats = summarizeTicker(holdoutTicks, holdoutBBO, *statsQuoteDistance, *fee)
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
	quoteCfg, inventoryBand := prepareReplayInventory(cfg)
	base := inventoryBand.target
	quote := startingQuote
	initialEquity := quote + base*start
	inventory := base
	fees, maxInventory := 0.0, math.Abs(inventory)
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
		plan := quoteCfg.Quote(gammacapture.MarketMakerQuoteInput{
			MidPrice: bar.open, BestBid: bookBid, BestAsk: bookAsk,
			VolatilityBps: rollingVolatilityBps(recentReturns),
			Inventory:     inventory, InventoryMin: inventoryBand.min, InventoryMax: inventoryBand.max,
			QuoteNotionalBase: cfg.QuoteNotional,
			CanBuy:            quote >= minOrderNotional && inventory < inventoryBand.max,
			CanSell:           (inventory-inventoryBand.min)*bar.open >= minOrderNotional,
		})
		if plan.Reason != "quoted" {
			continue
		}
		book := bboSnapshot{time: bar.time, bid: bookBid, ask: bookAsk}
		quoteActive++
		// Ask is our sell order: a bar high crossing it sells base. Bid is our
		// buy order: a bar low crossing it buys base. Each side can fill once.
		if plan.AllowAsk && bar.high >= plan.AskPrice {
			order := prepareReplayOrder(types.SideTypeSell, plan, book, inventory, quote, minOrderNotional, inventoryBand)
			if order.active {
				qty := order.quantity
				inventory -= qty
				quote += qty * order.price
				fees += qty * order.price * cfg.MakerFeeBps / 10_000
				fills++
				sells++
			}
		}
		if plan.AllowBid && bar.low <= plan.BidPrice {
			order := prepareReplayOrder(types.SideTypeBuy, plan, book, inventory, quote, minOrderNotional, inventoryBand)
			if order.active {
				qty := order.quantity
				inventory += qty
				quote -= qty * order.price
				fees += qty * order.price * cfg.MakerFeeBps / 10_000
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
	return result{
		HalfSpreadBps: cfg.MinimumHalfSpreadBps, InventorySkewBps: cfg.InventorySkewBps,
		Observations: observations, Fills: fills, BuyFills: buys, SellFills: sells, ExecutionEvents: fills,
		MakerFeesJPY: fees, FinalEquityJPY: finalEquity, NetPnLJPY: finalEquity - initialEquity,
		MaxAbsInventory: maxInventory, QuoteUptimePct: float64(quoteActive) * 100 / float64(max(1, observations)),
		FillsPerDay: float64(fills) / days, SyntheticFillModel: "one full fill per side per 1m bar when bar high/low crosses synthetic BBO; no queue model",
		DataQuality: "synthetic OHLC; no BBO or queue position",
	}
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
			ticks = append(ticks, tick{id: x.TradeID, time: now, price: x.Price.Float64(), size: x.Size.Float64(), side: x.Side})
		}
		if err := f.Close(); err != nil {
			fatalf("close %s: %v", filename, err)
		}
	}
	sort.SliceStable(ticks, func(i, j int) bool { return ticks[i].time.Before(ticks[j].time) })
	return ticks
}

func mergeReplayTrades(groups ...[]tick) []tick {
	var merged []tick
	for _, group := range groups {
		merged = append(merged, group...)
	}
	return compactTrades(merged)
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

func parseTime(v string) time.Time {
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t
	}
	return parseDate(v)
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
