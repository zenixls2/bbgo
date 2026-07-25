// Package gammacapture implements a long-only, paper-first barrier-crossing strategy.
package gammacapture

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

const (
	ID                = "gammacapture"
	BBGOCommit        = "8585410a3"
	BBGORelease       = "v1.63.0-1713-g8585410a3"
	defaultWindow     = 30 * time.Minute
	defaultHorizon    = 15 * time.Minute
	defaultMaxHolding = 30 * time.Minute
)

type SymbolSelection struct {
	Mode       string   `json:"mode" yaml:"mode"`
	QuoteAsset string   `json:"quoteAsset" yaml:"quoteAsset"`
	Symbols    []string `json:"symbols" yaml:"symbols"`
	Allowlist  []string `json:"allowlist" yaml:"allowlist"`
	Denylist   []string `json:"denylist" yaml:"denylist"`
}

type BarrierConfig struct {
	Width                float64        `json:"width" yaml:"width"`
	EpochDuration        types.Duration `json:"epochDuration" yaml:"epochDuration"`
	UpdateOnlyWhenFlat   bool           `json:"updateOnlyWhenFlat" yaml:"updateOnlyWhenFlat"`
	MinDwell             types.Duration `json:"minDwell" yaml:"minDwell"`
	ReversalBuffer       float64        `json:"reversalBuffer" yaml:"reversalBuffer"`
	MaxCrossingsPerEvent int            `json:"maxCrossingsPerEvent" yaml:"maxCrossingsPerEvent"`
}

type IntensityConfig struct {
	Window types.Duration `json:"window" yaml:"window"`
	// VolatilityWindow controls the denominator used for volatility only. A
	// zero value preserves the observation-duration behavior used by the slow
	// model; fast models can use their full configured window to regularize
	// sparse observations.
	VolatilityWindow types.Duration `json:"volatilityWindow" yaml:"volatilityWindow"`
	PriorAlphaUp     float64        `json:"priorAlphaUp" yaml:"priorAlphaUp"`
	PriorBetaUp      float64        `json:"priorBetaUp" yaml:"priorBetaUp"`
	PriorAlphaDown   float64        `json:"priorAlphaDown" yaml:"priorAlphaDown"`
	PriorBetaDown    float64        `json:"priorBetaDown" yaml:"priorBetaDown"`
	MinEvents        int            `json:"minEvents" yaml:"minEvents"`
}

type HorizonConfig struct {
	Prediction     types.Duration `json:"prediction" yaml:"prediction"`
	MaximumHolding types.Duration `json:"maximumHolding" yaml:"maximumHolding"`
}

// ReferencePriceConfig selects the causal stream used by the crossing engine.
// Backtests/replays must use closed candles because BBGO's built-in service does
// not replay market trades; paper mode defaults to individual market trades so
// that ordered barrier crossings are not inferred from a candle endpoint.
type ReferencePriceConfig struct {
	Mode string `json:"mode" yaml:"mode"`
}

// TrendConfig optionally requires a causal close-to-close return before a
// long entry. Leaving Lookback at zero disables the regime gate.
type TrendConfig struct {
	Lookback     types.Duration `json:"lookback" yaml:"lookback"`
	MinReturnBps float64        `json:"minReturnBps" yaml:"minReturnBps"`
}

type SignalConfig struct {
	LowerThreshold          float64        `json:"lowerThreshold" yaml:"lowerThreshold"`
	UpperThreshold          float64        `json:"upperThreshold" yaml:"upperThreshold"`
	EntryProbability        float64        `json:"entryProbability" yaml:"entryProbability"`
	ExitProbability         float64        `json:"exitProbability" yaml:"exitProbability"`
	MaxChurn                int            `json:"maxChurn" yaml:"maxChurn"`
	EntryConfirmationWindow types.Duration `json:"entryConfirmationWindow" yaml:"entryConfirmationWindow"`
	// StretchedSignalRetraceBps requires a pullback from a stretched signal
	// candle before it can be confirmed. Zero explicitly disables this optional
	// research guard; deployed configurations must opt in deliberately.
	StretchedSignalRetraceBps float64 `json:"stretchedSignalRetraceBps" yaml:"stretchedSignalRetraceBps"`
}

type TakeProfitConfig struct {
	ActivationBarriers    int `json:"activationBarriers" yaml:"activationBarriers"`
	InitialTargetBarriers int `json:"initialTargetBarriers" yaml:"initialTargetBarriers"`
	MinTrailingBarriers   int `json:"minTrailingBarriers" yaml:"minTrailingBarriers"`
	MaxTrailingBarriers   int `json:"maxTrailingBarriers" yaml:"maxTrailingBarriers"`
}

type StopLossConfig struct {
	HardStopBarriers int `json:"hardStopBarriers" yaml:"hardStopBarriers"`
	// SoftStopBarriers requires adverse grid movement before a model-only
	// soft exit can realize a loss. It must remain tighter than the hard stop.
	SoftStopBarriers int `json:"softStopBarriers" yaml:"softStopBarriers"`
}

// BarrierSelectionConfig enables entry-time model selection over a bounded
// TP/SL grid. The selected pair is persisted with the position; it is not
// recomputed while the position is open.
type BarrierSelectionConfig struct {
	Enabled           bool `json:"enabled" yaml:"enabled"`
	MinTargetBarriers int  `json:"minTargetBarriers" yaml:"minTargetBarriers"`
	MaxTargetBarriers int  `json:"maxTargetBarriers" yaml:"maxTargetBarriers"`
	MinStopBarriers   int  `json:"minStopBarriers" yaml:"minStopBarriers"`
	MaxStopBarriers   int  `json:"maxStopBarriers" yaml:"maxStopBarriers"`
}

type RiskConfig struct {
	MaxRiskPerTrade   float64 `json:"maxRiskPerTradeJPY" yaml:"maxRiskPerTradeJPY"`
	MaxSymbolNotional float64 `json:"maxSymbolNotionalJPY" yaml:"maxSymbolNotionalJPY"`
	// MaxSpreadBps limits the current best-ask/best-bid log spread before a
	// paper-mode market buy. It is not evaluated in candle-only replay.
	MaxSpreadBps float64 `json:"maxSpreadBps" yaml:"maxSpreadBps"`
	// MaxBookAge requires a recent BBO update alongside the spread cap.
	MaxBookAge        types.Duration `json:"maxBookAge" yaml:"maxBookAge"`
	CooldownAfterStop types.Duration `json:"cooldownAfterStop" yaml:"cooldownAfterStop"`
	// SoftExitMinHolding prevents model-confidence noise from immediately
	// round-tripping a newly opened spot position. Hard stops ignore it.
	SoftExitMinHolding types.Duration `json:"softExitMinHolding" yaml:"softExitMinHolding"`
	// EstimatedCostBps is the complete round-trip cost budget: both trading fees,
	// expected spread/slippage, and a conservative execution allowance.
	EstimatedCostBps float64 `json:"estimatedCostBps" yaml:"estimatedCostBps"`
	// MinimumNetEdgeBps is the additional edge required after EstimatedCostBps.
	// It is deliberately not a profit guarantee.
	MinimumNetEdgeBps float64 `json:"minimumNetEdgeBps" yaml:"minimumNetEdgeBps"`
	// MinimumRangeBps requires the selected gross TP excursion to cover the
	// configured round-trip cost plus edge. Zero derives that floor from
	// EstimatedCostBps + MinimumNetEdgeBps.
	MinimumRangeBps float64 `json:"minimumRangeBps" yaml:"minimumRangeBps"`
	// MaxEntryBarRangeBps and MaxEntryBarReturnBps avoid buying the close of a
	// candle that has already made an unusually large move. They are a candle
	// replay safeguard, not a replacement for a live BBO/market-impact check.
	MaxEntryBarRangeBps  float64 `json:"maxEntryBarRangeBps" yaml:"maxEntryBarRangeBps"`
	MaxEntryBarReturnBps float64 `json:"maxEntryBarReturnBps" yaml:"maxEntryBarReturnBps"`
}

// AggTradeWarmupConfig controls the causal startup preload used by paper/live
// runs. The archive contains market executions, not this bot's private orders;
// it is sufficient to seed the crossing/intensity model before live callbacks.
type AggTradeWarmupConfig struct {
	Enabled        bool           `json:"enabled" yaml:"enabled"`
	Path           string         `json:"path" yaml:"path"`
	LivePath       string         `json:"livePath" yaml:"livePath"`
	Lookback       types.Duration `json:"lookback" yaml:"lookback"`
	MaxAge         types.Duration `json:"maxAge" yaml:"maxAge"`
	RequireHealthy bool           `json:"requireHealthy" yaml:"requireHealthy"`
}

// Strategy configuration deliberately defaults to paper. Live mode is rejected in
// Validate: enabling it requires the acceptance gates documented in docs/gammacapture.
type Config struct {
	Environment string `json:"environment" yaml:"environment"`
	Symbol      string `json:"symbol" yaml:"symbol"`
	Interval    string `json:"interval" yaml:"interval"`

	// ResearchForceEntry bypasses model/signal/edge entry gates only in deterministic
	// replay or backtest. It exists to verify the BBGO order/matching path and is
	// rejected in paper or live modes.
	ResearchForceEntry bool `json:"researchForceEntry" yaml:"researchForceEntry"`

	SymbolSelection  SymbolSelection        `json:"symbolSelection" yaml:"symbolSelection"`
	Barrier          BarrierConfig          `json:"barrier" yaml:"barrier"`
	Intensity        IntensityConfig        `json:"intensity" yaml:"intensity"`
	Horizon          HorizonConfig          `json:"horizon" yaml:"horizon"`
	ReferencePrice   ReferencePriceConfig   `json:"referencePrice" yaml:"referencePrice"`
	Trend            TrendConfig            `json:"trend" yaml:"trend"`
	Signal           SignalConfig           `json:"signal" yaml:"signal"`
	TakeProfit       TakeProfitConfig       `json:"takeProfit" yaml:"takeProfit"`
	StopLoss         StopLossConfig         `json:"stopLoss" yaml:"stopLoss"`
	BarrierSelection BarrierSelectionConfig `json:"barrierSelection" yaml:"barrierSelection"`
	Risk             RiskConfig             `json:"risk" yaml:"risk"`
	AggTradeWarmup   AggTradeWarmupConfig   `json:"aggTradeWarmup" yaml:"aggTradeWarmup"`
	// MarketMaker is the fee-aware quote policy used by the market-making
	// research runner. The existing barrier strategy remains the default until
	// a live configuration explicitly selects a maker execution path.
	MarketMaker MarketMakerConfig `json:"marketMaker" yaml:"marketMaker"`
}

func (s *Config) setDefaults() {
	if s.Environment == "" {
		s.Environment = "paper"
	}
	if s.Interval == "" {
		s.Interval = "1m"
	}
	if s.Barrier.Width == 0 {
		s.Barrier.Width = 0.001
	}
	if s.Barrier.EpochDuration == 0 {
		s.Barrier.EpochDuration = types.Duration(30 * time.Minute)
	}
	if s.Barrier.MaxCrossingsPerEvent == 0 {
		s.Barrier.MaxCrossingsPerEvent = 8
	}
	if s.Intensity.Window == 0 {
		s.Intensity.Window = types.Duration(defaultWindow)
	}
	if s.Intensity.PriorAlphaUp == 0 {
		s.Intensity.PriorAlphaUp = 1
	}
	if s.Intensity.PriorAlphaDown == 0 {
		s.Intensity.PriorAlphaDown = 1
	}
	if s.Intensity.PriorBetaUp == 0 {
		s.Intensity.PriorBetaUp = 60
	}
	if s.Intensity.PriorBetaDown == 0 {
		s.Intensity.PriorBetaDown = 60
	}
	if s.Intensity.MinEvents == 0 {
		s.Intensity.MinEvents = 20
	}
	if s.Horizon.Prediction == 0 {
		s.Horizon.Prediction = types.Duration(defaultHorizon)
	}
	if s.Horizon.MaximumHolding == 0 {
		s.Horizon.MaximumHolding = types.Duration(defaultMaxHolding)
	}
	if s.ReferencePrice.Mode == "" {
		if s.Environment == "backtest" || s.Environment == "replay" {
			s.ReferencePrice.Mode = "klineClose"
		} else {
			s.ReferencePrice.Mode = "lastTrade"
		}
	}
	if s.Signal.LowerThreshold == 0 {
		s.Signal.LowerThreshold = .40
	}
	if s.Signal.UpperThreshold == 0 {
		s.Signal.UpperThreshold = .60
	}
	if s.Signal.EntryProbability == 0 {
		s.Signal.EntryProbability = .62
	}
	if s.Signal.ExitProbability == 0 {
		s.Signal.ExitProbability = .42
	}
	if s.Signal.MaxChurn == 0 {
		s.Signal.MaxChurn = 4
	}
	if s.Signal.EntryConfirmationWindow == 0 {
		s.Signal.EntryConfirmationWindow = types.Duration(5 * time.Minute)
	}
	if s.TakeProfit.ActivationBarriers == 0 {
		s.TakeProfit.ActivationBarriers = 2
	}
	if s.TakeProfit.InitialTargetBarriers == 0 {
		s.TakeProfit.InitialTargetBarriers = 5
	}
	if s.TakeProfit.MinTrailingBarriers == 0 {
		s.TakeProfit.MinTrailingBarriers = 1
	}
	if s.TakeProfit.MaxTrailingBarriers == 0 {
		s.TakeProfit.MaxTrailingBarriers = 4
	}
	if s.StopLoss.HardStopBarriers == 0 {
		s.StopLoss.HardStopBarriers = 3
	}
	if s.StopLoss.SoftStopBarriers == 0 {
		s.StopLoss.SoftStopBarriers = 2
	}
	if s.BarrierSelection.Enabled {
		if s.BarrierSelection.MinTargetBarriers == 0 {
			s.BarrierSelection.MinTargetBarriers = s.TakeProfit.InitialTargetBarriers
		}
		if s.BarrierSelection.MaxTargetBarriers == 0 {
			s.BarrierSelection.MaxTargetBarriers = s.TakeProfit.InitialTargetBarriers
		}
		if s.BarrierSelection.MinStopBarriers == 0 {
			s.BarrierSelection.MinStopBarriers = s.StopLoss.HardStopBarriers
		}
		if s.BarrierSelection.MaxStopBarriers == 0 {
			s.BarrierSelection.MaxStopBarriers = s.StopLoss.HardStopBarriers
		}
	}
	if s.Risk.CooldownAfterStop == 0 {
		s.Risk.CooldownAfterStop = types.Duration(30 * time.Minute)
	}
	if s.Risk.SoftExitMinHolding == 0 {
		s.Risk.SoftExitMinHolding = types.Duration(5 * time.Minute)
	}
	if s.Risk.EstimatedCostBps == 0 {
		s.Risk.EstimatedCostBps = 25
	}
	if s.Risk.MaxSpreadBps == 0 {
		s.Risk.MaxSpreadBps = 10
	}
	if s.Risk.MaxBookAge == 0 {
		s.Risk.MaxBookAge = types.Duration(5 * time.Second)
	}
	if s.Risk.MaxEntryBarRangeBps == 0 {
		s.Risk.MaxEntryBarRangeBps = 25
	}
	if s.Risk.MaxEntryBarReturnBps == 0 {
		s.Risk.MaxEntryBarReturnBps = 15
	}
	if s.AggTradeWarmup.Path == "" {
		s.AggTradeWarmup.Path = "data/gammacapture"
	}
	if s.AggTradeWarmup.LivePath == "" {
		s.AggTradeWarmup.LivePath = filepath.Join(s.AggTradeWarmup.Path, "live")
	}
	if s.AggTradeWarmup.Lookback == 0 {
		s.AggTradeWarmup.Lookback = types.Duration(6 * time.Hour)
	}
	if s.AggTradeWarmup.MaxAge == 0 {
		s.AggTradeWarmup.MaxAge = types.Duration(72 * time.Hour)
	}
	s.MarketMaker.setDefaults()
	if s.SymbolSelection.QuoteAsset == "" {
		s.SymbolSelection.QuoteAsset = "JPY"
	}
	if s.SymbolSelection.Mode == "" {
		s.SymbolSelection.Mode = "allowlist"
	}
}

func (s *Config) Validate() error {
	s.setDefaults()
	if s.Risk.EstimatedCostBps < 0 || s.Risk.MinimumNetEdgeBps < 0 || s.Risk.MinimumRangeBps < 0 || s.Risk.MaxSpreadBps < 0 || s.Risk.MaxBookAge < 0 || s.Risk.MaxEntryBarRangeBps < 0 || s.Risk.MaxEntryBarReturnBps < 0 || s.Signal.StretchedSignalRetraceBps < 0 || s.Trend.MinReturnBps < 0 || s.MarketMaker.MakerFeeBps < 0 || s.MarketMaker.TakerFeeBps < 0 || s.MarketMaker.AdverseSelectionBps < 0 || s.MarketMaker.MinimumNetEdgeBps < 0 || s.MarketMaker.RefreshInterval < 0 || s.MarketMaker.MinRefreshInterval < 0 || s.MarketMaker.RefreshMoveBps < 0 || s.MarketMaker.AdverseRepriceBps < 0 || s.MarketMaker.RefreshImbalanceDelta < 0 || s.MarketMaker.FastWindow < 0 || s.MarketMaker.FastEvidenceWindow < 0 || s.MarketMaker.DirectionSkewBps < 0 || s.MarketMaker.ImbalanceSkewBps < 0 || s.MarketMaker.InventoryReset.MaxAskAge < 0 || s.MarketMaker.InventoryReset.FastAskAge < 0 || s.MarketMaker.InventoryReset.AdverseMoveBps < 0 || s.MarketMaker.InventoryReset.FastAdverseMoveBps < 0 || s.MarketMaker.InventoryReset.FastDirectionThreshold < 0 || s.MarketMaker.InventoryReset.MaxSlippageBps < 0 || s.MarketMaker.InventoryReset.ReductionNotional < 0 || s.MarketMaker.InventoryReset.Cooldown < 0 || s.MarketMaker.InventoryReset.FillIntensityHaircut < 0 || s.MarketMaker.InventoryReset.RiskZScore < 0 {
		return fmt.Errorf("risk cost, edge, and entry-bar limits must not be negative")
	}
	if s.Environment == "live" {
	} else if s.Environment != "paper" && s.Environment != "replay" && s.Environment != "backtest" {
		return fmt.Errorf("environment must be paper, replay, or backtest")
	}
	if s.ResearchForceEntry && s.Environment != "replay" && s.Environment != "backtest" {
		return fmt.Errorf("researchForceEntry is only allowed in replay or backtest environments")
	}
	if s.Symbol == "" {
		return fmt.Errorf("symbol is required; dynamic selection must be resolved into one strategy instance before BBGO subscriptions start")
	}
	if s.SymbolSelection.Mode != "allowlist" && s.SymbolSelection.Mode != "dynamic" {
		return fmt.Errorf("symbolSelection.mode must be allowlist or dynamic")
	}
	if s.SymbolSelection.Mode == "allowlist" && len(s.SymbolSelection.Symbols) > 0 && !contains(s.SymbolSelection.Symbols, s.Symbol) {
		return fmt.Errorf("symbol %s is not in symbolSelection.symbols", s.Symbol)
	}
	if contains(s.SymbolSelection.Denylist, s.Symbol) {
		return fmt.Errorf("symbol %s is denylisted", s.Symbol)
	}
	if s.Barrier.Width <= 0 || s.Barrier.Width >= 1 {
		return fmt.Errorf("barrier.width must be in (0,1)")
	}
	if s.Barrier.MaxCrossingsPerEvent < 1 {
		return fmt.Errorf("barrier.maxCrossingsPerEvent must be positive")
	}
	if s.Intensity.Window <= 0 || s.Horizon.Prediction <= 0 || s.Horizon.MaximumHolding <= 0 {
		return fmt.Errorf("intensity window and horizons must be positive")
	}
	if s.ReferencePrice.Mode != "klineClose" && s.ReferencePrice.Mode != "lastTrade" && s.ReferencePrice.Mode != "microprice" {
		return fmt.Errorf("referencePrice.mode must be klineClose, lastTrade, or microprice")
	}
	if (s.Environment == "backtest" || s.Environment == "replay") && s.ReferencePrice.Mode != "klineClose" {
		return fmt.Errorf("backtest and replay require referencePrice.mode klineClose because their market-trade stream is not replayed")
	}
	if s.Trend.Lookback < 0 {
		return fmt.Errorf("trend.lookback must not be negative")
	}
	if !(0 < s.Signal.LowerThreshold && s.Signal.LowerThreshold < s.Signal.UpperThreshold && s.Signal.UpperThreshold < 1) {
		return fmt.Errorf("signal thresholds must satisfy 0 < lower < upper < 1")
	}
	if s.Signal.EntryProbability < s.Signal.UpperThreshold || s.Signal.EntryProbability > 1 || s.Signal.ExitProbability < 0 || s.Signal.ExitProbability >= s.Signal.EntryProbability {
		return fmt.Errorf("entry probability must be at least the upper threshold and exit probability must be lower than entry probability")
	}
	if s.Signal.EntryConfirmationWindow <= 0 {
		return fmt.Errorf("signal.entryConfirmationWindow must be positive")
	}
	if s.TakeProfit.InitialTargetBarriers < 1 || s.StopLoss.HardStopBarriers < 1 {
		return fmt.Errorf("TP and hard-stop barrier counts must be positive")
	}
	if s.StopLoss.SoftStopBarriers < 1 || s.StopLoss.SoftStopBarriers >= s.StopLoss.HardStopBarriers {
		return fmt.Errorf("soft stop barriers must be positive and smaller than the hard stop")
	}
	if s.BarrierSelection.Enabled {
		b := s.BarrierSelection
		if b.MinTargetBarriers < 1 || b.MinTargetBarriers > b.MaxTargetBarriers {
			return fmt.Errorf("barrierSelection target range is invalid")
		}
		if b.MinStopBarriers < 2 || b.MinStopBarriers > b.MaxStopBarriers {
			return fmt.Errorf("barrierSelection stop range must be at least two and ordered")
		}
	}
	return nil
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if strings.EqualFold(v, want) {
			return true
		}
	}
	return false
}
