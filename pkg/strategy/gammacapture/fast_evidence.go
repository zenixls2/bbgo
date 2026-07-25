package gammacapture

import (
	"math"
	"sort"
	"sync"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

// FastEvidenceConfig controls the observational raw-market-data layer.  It is
// deliberately separate from the crossing intensity model: enough trades and
// BBO updates establish data coverage, not profitability or crossing health.
type FastEvidenceConfig struct {
	Window        time.Duration
	MinTrades     int
	MinBBOUpdates int
}

// FastEvidenceSnapshot is emitted in maker diagnostics.  Its health is a
// coverage label only; it must not be used as proof that a quote has positive
// expected value after fees and adverse selection.
type FastEvidenceSnapshot struct {
	TradeCount            int
	BBOCount              int
	SignedTradeImbalance  float64
	QueueImbalance        float64
	MidReturnBps          float64
	RealizedVolatilityBps float64
	Observed              time.Duration
	Age                   time.Duration
	Health                ModelHealth
}

type fastEvidenceTrade struct {
	at       time.Time
	notional float64
	signed   float64
	id       uint64
}

type fastEvidenceBBO struct {
	at        time.Time
	mid       float64
	imbalance float64
}

// FastEvidenceModel keeps a bounded, causal window of public trades and BBO
// observations.  It is intentionally small and deterministic so the same
// feature calculations can later be replayed from captured CSV data.
type FastEvidenceModel struct {
	mu            sync.Mutex
	window        time.Duration
	minTrades     int
	minBBOUpdates int
	trades        []fastEvidenceTrade
	bbo           []fastEvidenceBBO
	lastTradeID   uint64
}

func NewFastEvidenceModel(cfg FastEvidenceConfig) *FastEvidenceModel {
	if cfg.Window <= 0 {
		cfg.Window = time.Minute
	}
	if cfg.MinTrades <= 0 {
		cfg.MinTrades = 20
	}
	if cfg.MinBBOUpdates <= 0 {
		cfg.MinBBOUpdates = 20
	}
	return &FastEvidenceModel{
		window:        cfg.Window,
		minTrades:     cfg.MinTrades,
		minBBOUpdates: cfg.MinBBOUpdates,
	}
}

func (m *FastEvidenceModel) ObserveTrade(at time.Time, trade types.Trade) {
	if m == nil || at.IsZero() || trade.Price.Sign() <= 0 || trade.Quantity.Sign() <= 0 {
		return
	}
	notional := trade.QuoteQuantity.Float64()
	if notional <= 0 {
		notional = trade.Price.Float64() * trade.Quantity.Float64()
	}
	if notional <= 0 || math.IsNaN(notional) || math.IsInf(notional, 0) {
		return
	}
	signed := notional
	if trade.Side == types.SideTypeSell || (trade.Side == "" && !trade.IsBuyer) {
		signed = -signed
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if trade.ID != 0 && trade.ID <= m.lastTradeID {
		return
	}
	if trade.ID != 0 {
		m.lastTradeID = trade.ID
	}
	m.trades = append(m.trades, fastEvidenceTrade{at: at, notional: notional, signed: signed, id: trade.ID})
	m.trimLocked(at)
}

func (m *FastEvidenceModel) ObserveBBO(at time.Time, ticker types.BookTicker) {
	if m == nil || at.IsZero() || ticker.Buy.Sign() <= 0 || ticker.Sell.Sign() <= 0 || ticker.Sell.Compare(ticker.Buy) <= 0 {
		return
	}
	mid := (ticker.Buy.Float64() + ticker.Sell.Float64()) / 2
	denom := ticker.BuySize.Float64() + ticker.SellSize.Float64()
	imbalance := 0.0
	if denom > 0 {
		imbalance = (ticker.BuySize.Float64() - ticker.SellSize.Float64()) / denom
	}
	m.mu.Lock()
	m.bbo = append(m.bbo, fastEvidenceBBO{at: at, mid: mid, imbalance: math.Max(-1, math.Min(1, imbalance))})
	m.trimLocked(at)
	m.mu.Unlock()
}

func (m *FastEvidenceModel) trimLocked(now time.Time) {
	cutoff := now.Add(-m.window)
	tradeFirst := sort.Search(len(m.trades), func(i int) bool { return !m.trades[i].at.Before(cutoff) })
	if tradeFirst > 0 {
		m.trades = append([]fastEvidenceTrade(nil), m.trades[tradeFirst:]...)
	}
	bboFirst := sort.Search(len(m.bbo), func(i int) bool { return !m.bbo[i].at.Before(cutoff) })
	if bboFirst > 0 {
		m.bbo = append([]fastEvidenceBBO(nil), m.bbo[bboFirst:]...)
	}
}

func (m *FastEvidenceModel) Snapshot(now time.Time) FastEvidenceSnapshot {
	if m == nil || now.IsZero() {
		return FastEvidenceSnapshot{Health: HealthInsufficient}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.trimLocked(now)
	s := FastEvidenceSnapshot{TradeCount: len(m.trades), BBOCount: len(m.bbo), Health: HealthInsufficient}
	var first, last time.Time
	var total, signed float64
	for _, trade := range m.trades {
		if first.IsZero() || trade.at.Before(first) {
			first = trade.at
		}
		if trade.at.After(last) {
			last = trade.at
		}
		total += trade.notional
		signed += trade.signed
	}
	for _, point := range m.bbo {
		if first.IsZero() || point.at.Before(first) {
			first = point.at
		}
		if point.at.After(last) {
			last = point.at
		}
	}
	if total > 0 {
		s.SignedTradeImbalance = math.Max(-1, math.Min(1, signed/total))
	}
	if len(m.bbo) > 0 {
		s.QueueImbalance = m.bbo[len(m.bbo)-1].imbalance
		if len(m.bbo) > 1 {
			firstMid := m.bbo[0].mid
			lastMid := m.bbo[len(m.bbo)-1].mid
			if firstMid > 0 && lastMid > 0 {
				s.MidReturnBps = math.Log(lastMid/firstMid) * 10_000
			}
			var sumSquares float64
			for i := 1; i < len(m.bbo); i++ {
				if m.bbo[i-1].mid > 0 && m.bbo[i].mid > 0 {
					ret := math.Log(m.bbo[i].mid/m.bbo[i-1].mid) * 10_000
					sumSquares += ret * ret
				}
			}
			s.RealizedVolatilityBps = math.Sqrt(sumSquares)
		}
	}
	if !first.IsZero() {
		s.Observed = now.Sub(first)
	}
	if !last.IsZero() {
		s.Age = now.Sub(last)
	}
	if s.TradeCount > 0 && s.BBOCount > 0 {
		s.Health = HealthDegraded
		if s.TradeCount >= m.minTrades && s.BBOCount >= m.minBBOUpdates {
			s.Health = HealthHealthy
		}
	}
	return s
}
