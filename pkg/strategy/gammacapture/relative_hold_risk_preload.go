package gammacapture

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

// relativeHoldRiskLivePreloadSource identifies labels reconstructed from the
// same-symbol private Binance fills and the one-second causal BBO accumulator.
// Research checkpoints made from coarser aggregated BBO are intentionally
// never accepted as live state.
const relativeHoldRiskLivePreloadSource = "live-private-fills/1s-causal-bbo"

const relativeHoldPrivateTradePageLimit int64 = 1000

type relativeHoldRiskPreloadStats struct {
	Source        string
	From          time.Time
	To            time.Time
	PrivateTrades int
	LabelsBefore  int
	Used          bool
	Reason        string
}

// prepareRelativeHoldRiskLivePreload reconstructs the strategy equity path
// between the BBO replay cursor and startup. It uses exchange-confirmed,
// same-symbol private fills rather than public-trade or aggregated-BBO fill
// proxies. The current account is rolled backwards through those fills to get
// the causal starting balances, then the fills are replayed forward and marked
// on each raw BBO observation.
//
// A missing history service or a balance reconciliation mismatch is a safe
// cache miss: the strategy keeps the model immature and learns from live
// balances after startup. It must never invent a label from market data alone.
func (s *Strategy) prepareRelativeHoldRiskLivePreload(cursor, now time.Time, restored bool) relativeHoldRiskPreloadStats {
	stats := relativeHoldRiskPreloadStats{
		Source: relativeHoldRiskLivePreloadSource, From: cursor, To: now,
		Reason: "relative-hold live preload unavailable",
	}
	previousSource := s.makerRelativeHoldPreloadSource
	s.makerRelativeHoldPrivateTrades = nil
	s.makerRelativeHoldPrivateIndex = 0
	s.makerRelativeHoldShadowReady = false
	// The live capture pipeline is raw BBO even when the private history API
	// has no rows. Mark the provenance now so labels accumulated after startup
	// remain restartable instead of being downgraded to an unknown legacy state.
	s.makerRelativeHoldPreloadSource = relativeHoldRiskLivePreloadSource
	if s.makerRelativeHoldRisk == nil || !s.MarketMaker.RelativeHoldRisk.Enabled {
		stats.Reason = "relative-hold disabled"
		return stats
	}
	if cursor.IsZero() || now.IsZero() || !cursor.Before(now) {
		stats.Reason = "relative-hold preload interval is empty"
		return stats
	}
	if s.session == nil || s.session.Exchange == nil {
		stats.Reason = "exchange unavailable for private-fill preload"
		return stats
	}
	trades, err := s.queryRelativeHoldPrivateTrades(cursor, now)
	if err != nil {
		stats.Reason = err.Error()
		return stats
	}
	stats.PrivateTrades = len(trades)
	if len(trades) == 0 && !restored {
		stats.Reason = "no same-symbol private fills in preload interval"
		return stats
	}
	if len(trades) == 0 && restored && previousSource == relativeHoldRiskLivePreloadSource {
		// A compatible full live checkpoint already contains the matured labels;
		// there may simply be no private fills in the delta interval. Preserve
		// its provenance while the raw BBO models catch up.
		s.makerRelativeHoldPreloadSource = previousSource
		stats.Used = true
		stats.Reason = "restored live private-fill checkpoint; no private-fill delta"
		return stats
	}
	account := s.session.GetAccount()
	if account == nil {
		stats.Reason = "account unavailable for private-fill preload"
		return stats
	}
	baseBalance, baseOK := account.Balance(s.Market.BaseCurrency)
	quoteBalance, quoteOK := account.Balance(s.Market.QuoteCurrency)
	if !baseOK || !quoteOK {
		stats.Reason = "base/quote balance unavailable for private-fill preload"
		return stats
	}
	baseNow, quoteNow := baseBalance.Total().Float64(), quoteBalance.Total().Float64()
	baseStart, quoteStart, ok := reverseRelativeHoldPrivateTrades(
		baseNow, quoteNow, trades, s.Market.BaseCurrency, s.Market.QuoteCurrency)
	if !ok {
		stats.Reason = "private-fill replay produced invalid starting balances"
		return stats
	}
	// Forward reconciliation prevents a partial API page, a manual transfer,
	// or another strategy on the same symbol from contaminating the label path.
	baseCheck, quoteCheck := applyRelativeHoldPrivateTrades(
		baseStart, quoteStart, trades, s.Market.BaseCurrency, s.Market.QuoteCurrency)
	if !relativeHoldBalancesClose(baseCheck, baseNow) || !relativeHoldBalancesClose(quoteCheck, quoteNow) {
		stats.Reason = fmt.Sprintf(
			"private-fill replay does not reconcile account (base %.12g/%.12g quote %.12g/%.12g)",
			baseCheck, baseNow, quoteCheck, quoteNow)
		return stats
	}
	s.makerRelativeHoldPrivateTrades = trades
	s.makerRelativeHoldPrivateIndex = 0
	s.makerRelativeHoldShadowBase = baseStart
	s.makerRelativeHoldShadowQuote = quoteStart
	s.makerRelativeHoldShadowReady = true
	s.makerRelativeHoldPreloadSource = relativeHoldRiskLivePreloadSource
	stats.Used = true
	stats.LabelsBefore = s.makerRelativeHoldRisk.Snapshot().MaturedLabels
	stats.Reason = "same-symbol private fills replayed on one-second causal BBO"
	return stats
}

func (s *Strategy) queryRelativeHoldPrivateTrades(start, end time.Time) ([]types.Trade, error) {
	history, ok := s.session.Exchange.(types.ExchangeTradeHistoryService)
	if !ok {
		return nil, fmt.Errorf("Binance trade-history service unavailable")
	}
	all := make([]types.Trade, 0, 128)
	seen := make(map[uint64]struct{})
	lastTradeID := uint64(0)
	for page := 0; page < 32; page++ {
		options := &types.TradeQueryOptions{
			StartTime: &start, EndTime: &end,
			Limit: relativeHoldPrivateTradePageLimit, LastTradeID: lastTradeID,
		}
		batch, err := history.QueryTrades(context.Background(), s.Symbol, options)
		if err != nil {
			return nil, fmt.Errorf("query same-symbol private fills: %w", err)
		}
		if len(batch) == 0 {
			break
		}
		maxID := lastTradeID
		for _, trade := range batch {
			if trade.Symbol != "" && trade.Symbol != s.Symbol {
				continue
			}
			at := trade.Time.Time()
			if at.Before(start) || !at.Before(end) {
				continue
			}
			key := trade.ID
			if key == 0 {
				key = trade.OrderID
			}
			if key != 0 {
				if _, exists := seen[key]; exists {
					continue
				}
				seen[key] = struct{}{}
			}
			all = append(all, trade)
			if trade.ID > maxID {
				maxID = trade.ID
			}
		}
		if len(batch) < int(relativeHoldPrivateTradePageLimit) || maxID <= lastTradeID {
			break
		}
		lastTradeID = maxID
	}
	sort.SliceStable(all, func(i, j int) bool {
		left, right := all[i].Time.Time(), all[j].Time.Time()
		if left.Equal(right) {
			return all[i].ID < all[j].ID
		}
		return left.Before(right)
	})
	return all, nil
}

func (s *Strategy) observeRelativeHoldRiskPreloadBBO(at time.Time, ticker types.BookTicker) {
	if !s.makerRelativeHoldShadowReady || s.makerRelativeHoldRisk == nil {
		return
	}
	for s.makerRelativeHoldPrivateIndex < len(s.makerRelativeHoldPrivateTrades) {
		trade := s.makerRelativeHoldPrivateTrades[s.makerRelativeHoldPrivateIndex]
		if !trade.Time.Time().Before(at) {
			break
		}
		base, quote, ok := applyRelativeHoldPrivateTradeOne(
			s.makerRelativeHoldShadowBase, s.makerRelativeHoldShadowQuote,
			trade, s.Market.BaseCurrency, s.Market.QuoteCurrency, true)
		if !ok {
			// The complete interval was reconciled before replay. A malformed late
			// row is ignored rather than turning the equity stream negative.
			s.makerRelativeHoldPrivateIndex++
			continue
		}
		s.makerRelativeHoldShadowBase, s.makerRelativeHoldShadowQuote = base, quote
		s.makerRelativeHoldPrivateIndex++
	}
	s.observeRelativeHoldRiskEquityAt(
		at, ticker.Buy.Float64(), s.makerRelativeHoldShadowBase, s.makerRelativeHoldShadowQuote)
}

func reverseRelativeHoldPrivateTrades(base, quote float64, trades []types.Trade, baseCurrency, quoteCurrency string) (float64, float64, bool) {
	for i := len(trades) - 1; i >= 0; i-- {
		var ok bool
		base, quote, ok = applyRelativeHoldPrivateTradeOne(base, quote, trades[i], baseCurrency, quoteCurrency, false)
		if !ok {
			return 0, 0, false
		}
	}
	return base, quote, relativeHoldBalancesValid(base, quote)
}

func applyRelativeHoldPrivateTrades(base, quote float64, trades []types.Trade, baseCurrency, quoteCurrency string) (float64, float64) {
	for _, trade := range trades {
		var ok bool
		base, quote, ok = applyRelativeHoldPrivateTradeOne(base, quote, trade, baseCurrency, quoteCurrency, true)
		if !ok {
			return math.NaN(), math.NaN()
		}
	}
	return base, quote
}

func applyRelativeHoldPrivateTradeOne(base, quote float64, trade types.Trade, baseCurrency, quoteCurrency string, forward bool) (float64, float64, bool) {
	quantity := trade.Quantity.Float64()
	quoteQuantity := trade.QuoteQuantity.Float64()
	if quoteQuantity <= 0 && trade.Price.Sign() > 0 {
		quoteQuantity = trade.Price.Float64() * quantity
	}
	fee := math.Max(0, trade.Fee.Float64())
	if quantity < 0 || quoteQuantity < 0 || !relativeHoldBalancesValid(base, quote) {
		return 0, 0, false
	}
	feeCurrency := strings.ToUpper(strings.TrimSpace(trade.FeeCurrency))
	if !forward {
		// Undo the fee first, then undo the asset/quote transfer.
		if feeCurrency == strings.ToUpper(baseCurrency) {
			base += fee
		} else if feeCurrency == strings.ToUpper(quoteCurrency) {
			quote += fee
		}
		if trade.Side == types.SideTypeBuy || trade.IsBuyer {
			base -= quantity
			quote += quoteQuantity
		} else {
			base += quantity
			quote -= quoteQuantity
		}
		return base, quote, relativeHoldBalancesValid(base, quote)
	}
	if trade.Side == types.SideTypeBuy || trade.IsBuyer {
		base += quantity
		quote -= quoteQuantity
	} else {
		base -= quantity
		quote += quoteQuantity
	}
	if feeCurrency == strings.ToUpper(baseCurrency) {
		base -= fee
	} else if feeCurrency == strings.ToUpper(quoteCurrency) {
		quote -= fee
	}
	return base, quote, relativeHoldBalancesValid(base, quote)
}

func relativeHoldBalancesValid(base, quote float64) bool {
	return relativeFinite(base) && relativeFinite(quote) && base >= -1e-9 && quote >= -1e-6
}

func relativeHoldBalancesClose(left, right float64) bool {
	tolerance := math.Max(1e-8, math.Abs(right)*1e-6)
	return relativeFinite(left) && relativeFinite(right) && math.Abs(left-right) <= tolerance
}
