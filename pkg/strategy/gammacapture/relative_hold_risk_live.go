package gammacapture

import (
	"fmt"
	"math"
	"time"
)

// relativeHoldRiskBaselineCheckpoint persists the account baseline and the
// current non-overlapping label anchor. The RelativeHoldRisk model checkpoint
// alone is insufficient: after restart, recomputing Hold wealth from a new
// baseline would silently change every subsequent excess-return label.
type relativeHoldRiskBaselineCheckpoint struct {
	InitialAt    time.Time                    `json:"initialAt,omitempty"`
	InitialBase  float64                      `json:"initialBase,omitempty"`
	InitialQuote float64                      `json:"initialQuote,omitempty"`
	Anchor       *RelativeHoldRiskEquityPoint `json:"anchor,omitempty"`
}

// observeRelativeHoldRiskEquity records one executable-BBO equity point and
// matures at most one non-overlapping same-symbol label. It is called from the
// maker quote loop even when no order is currently resting, so the label clock
// cannot be starved by a temporary empty order book. Account balances already
// include realized fees; marking both paths at the bid therefore produces the
// fee-net strategy-vs-Hold log return required by RelativeHoldRiskModel.
func (s *Strategy) observeRelativeHoldRiskEquity(now time.Time, bid, base, quote float64) {
	s.observeRelativeHoldRiskEquityAt(now, bid, base, quote)
}

// observeRelativeHoldRiskEquityAt is shared by the live quote loop and the
// startup private-fill replay. Both paths use the same executable-bid mark and
// non-overlapping label clock; only the balance source differs.
func (s *Strategy) observeRelativeHoldRiskEquityAt(now time.Time, bid, base, quote float64) {
	model := s.makerRelativeHoldRisk
	if model == nil || !s.MarketMaker.RelativeHoldRisk.Enabled || now.IsZero() || bid <= 0 ||
		!relativeFinite(base) || !relativeFinite(quote) || base < 0 || quote < 0 {
		return
	}
	strategyEquity := quote + base*bid
	if strategyEquity <= 0 || !relativeFinite(strategyEquity) {
		return
	}
	if s.makerRelativeHoldInitialAt.IsZero() {
		s.makerRelativeHoldInitialAt = now
		s.makerRelativeHoldInitialBase = base
		s.makerRelativeHoldInitialQuote = quote
	}
	holdEquity := s.makerRelativeHoldInitialQuote + s.makerRelativeHoldInitialBase*bid
	if holdEquity <= 0 || !relativeFinite(holdEquity) {
		return
	}
	point := RelativeHoldRiskEquityPoint{At: now, StrategyEquity: strategyEquity, HoldEquity: holdEquity}
	if s.makerRelativeHoldAnchor == nil {
		s.makerRelativeHoldAnchor = &point
		return
	}
	horizon := model.config.Horizon
	if horizon <= 0 || point.At.Before(s.makerRelativeHoldAnchor.At.Add(horizon)) {
		return
	}
	anchor := s.makerRelativeHoldAnchor
	if anchor.StrategyEquity <= 0 || anchor.HoldEquity <= 0 ||
		!relativeFinite(anchor.StrategyEquity) || !relativeFinite(anchor.HoldEquity) {
		s.makerRelativeHoldAnchor = &point
		return
	}
	strategyReturn := math.Log(point.StrategyEquity / anchor.StrategyEquity)
	holdReturn := math.Log(point.HoldEquity / anchor.HoldEquity)
	if !relativeFinite(strategyReturn) || !relativeFinite(holdReturn) {
		s.makerRelativeHoldAnchor = &point
		return
	}
	if model.UpdateLabel(RelativeHoldRiskLabel{
		DecisionAt: anchor.At, MaturedAt: point.At,
		StrategyReturn: strategyReturn, HoldReturn: holdReturn,
	}) {
		s.makerRelativeHoldAnchor = &point
	}
}

func (s *Strategy) relativeHoldRiskInput(now time.Time) RelativeHoldRiskInput {
	if s == nil || s.makerRelativeHoldRisk == nil || !s.MarketMaker.RelativeHoldRisk.Enabled {
		return RelativeHoldRiskInput{}
	}
	state := s.makerRelativeHoldRisk.SnapshotAt(now)
	return RelativeHoldRiskInput{
		Enabled:               true,
		ShadowOnly:            s.MarketMaker.RelativeHoldRisk.ShadowOnly,
		State:                 state,
		TrackingErrorAversion: s.MarketMaker.RelativeHoldRisk.TrackingErrorAversion,
		DownsideBetaAversion:  s.MarketMaker.RelativeHoldRisk.DownsideBetaAversion,
		TotalBetaAversion:     s.MarketMaker.RelativeHoldRisk.TotalBetaAversion,
	}
}

func (s *Strategy) relativeHoldRiskBaselineCheckpoint() *relativeHoldRiskBaselineCheckpoint {
	if s == nil || (s.makerRelativeHoldInitialAt.IsZero() && s.makerRelativeHoldAnchor == nil) {
		return nil
	}
	checkpoint := &relativeHoldRiskBaselineCheckpoint{
		InitialAt:    s.makerRelativeHoldInitialAt,
		InitialBase:  s.makerRelativeHoldInitialBase,
		InitialQuote: s.makerRelativeHoldInitialQuote,
	}
	if s.makerRelativeHoldAnchor != nil {
		anchor := *s.makerRelativeHoldAnchor
		checkpoint.Anchor = &anchor
	}
	return checkpoint
}

func (s *Strategy) restoreRelativeHoldRiskBaseline(checkpoint *relativeHoldRiskBaselineCheckpoint) error {
	if checkpoint == nil {
		s.makerRelativeHoldInitialAt = time.Time{}
		s.makerRelativeHoldInitialBase = 0
		s.makerRelativeHoldInitialQuote = 0
		s.makerRelativeHoldAnchor = nil
		return nil
	}
	if checkpoint.InitialAt.IsZero() || checkpoint.InitialBase < 0 || checkpoint.InitialQuote < 0 ||
		!relativeFinite(checkpoint.InitialBase) || !relativeFinite(checkpoint.InitialQuote) {
		return fmt.Errorf("relative-hold baseline checkpoint is invalid")
	}
	if checkpoint.Anchor != nil {
		anchor := *checkpoint.Anchor
		if anchor.At.IsZero() || anchor.StrategyEquity <= 0 || anchor.HoldEquity <= 0 ||
			!relativeFinite(anchor.StrategyEquity) || !relativeFinite(anchor.HoldEquity) {
			return fmt.Errorf("relative-hold anchor checkpoint is invalid")
		}
		s.makerRelativeHoldAnchor = &anchor
	} else {
		s.makerRelativeHoldAnchor = nil
	}
	s.makerRelativeHoldInitialAt = checkpoint.InitialAt
	s.makerRelativeHoldInitialBase = checkpoint.InitialBase
	s.makerRelativeHoldInitialQuote = checkpoint.InitialQuote
	return nil
}
