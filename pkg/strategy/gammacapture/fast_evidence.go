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
	TradeCount                       int
	TradeCount5m                     int
	BBOCount                         int
	BBOCount5m                       int
	SignedTradeImbalance             float64
	SignedTradeImbalance5m           float64
	QueueImbalance                   float64
	MidReturnBps                     float64
	MidReturn1mBps                   float64
	MidReturn5mBps                   float64
	MidDrawdownBps                   float64
	MidDrawdownWindow                time.Duration
	MidDrawdown5mBps                 float64 // legacy diagnostic and acquisition-reset input
	MidRebound30sBps                 float64
	MidLow30s                        float64
	OrderFlowImbalance30s            float64
	MicropriceDisplacement           float64
	MidVolatilityPerSqrtSecond5mBps  float64 // fair-price diagnostic
	BuyVolatilityPerSqrtSecond5mBps  float64 // ask path
	SellVolatilityPerSqrtSecond5mBps float64 // bid path
	MidVolatilitySamples5m           int
	MidVolatilityObservedSeconds5m   float64
	RealizedVolatilityBps            float64
	Observed                         time.Duration
	Age                              time.Duration
	Health                           ModelHealth
	VolumeBalance                    VolumeBalanceSnapshot
}

func (s FastEvidenceSnapshot) BuyExecutionVolatility5mBps() float64 {
	if s.BuyVolatilityPerSqrtSecond5mBps > 0 {
		return s.BuyVolatilityPerSqrtSecond5mBps
	}
	return s.MidVolatilityPerSqrtSecond5mBps
}

func (s FastEvidenceSnapshot) SellExecutionVolatility5mBps() float64 {
	if s.SellVolatilityPerSqrtSecond5mBps > 0 {
		return s.SellVolatilityPerSqrtSecond5mBps
	}
	return s.MidVolatilityPerSqrtSecond5mBps
}

type fastEvidenceTrade struct {
	at       time.Time
	notional float64
	signed   float64
	id       uint64
}

type fastEvidenceBBO struct {
	at               time.Time
	bid, ask         float64
	bidSize, askSize float64
	mid              float64
	imbalance        float64
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
	m.bbo = append(m.bbo, fastEvidenceBBO{
		at: at, bid: ticker.Buy.Float64(), ask: ticker.Sell.Float64(),
		bidSize: ticker.BuySize.Float64(), askSize: ticker.SellSize.Float64(),
		mid: mid, imbalance: math.Max(-1, math.Min(1, imbalance)),
	})
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
	s := FastEvidenceSnapshot{
		TradeCount: len(m.trades), BBOCount: len(m.bbo),
		MidDrawdownWindow: m.window, Health: HealthInsufficient,
	}
	var first, last time.Time
	var total, signed, total5m, signed5m float64
	for _, trade := range m.trades {
		if first.IsZero() || trade.at.Before(first) {
			first = trade.at
		}
		if trade.at.After(last) {
			last = trade.at
		}
		total += trade.notional
		signed += trade.signed
		if !trade.at.Before(now.Add(-5 * time.Minute)) {
			s.TradeCount5m++
			total5m += trade.notional
			signed5m += trade.signed
		}
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
	if total5m > 0 {
		s.SignedTradeImbalance5m = math.Max(-1, math.Min(1, signed5m/total5m))
	}
	if len(m.bbo) > 0 {
		s.QueueImbalance = m.bbo[len(m.bbo)-1].imbalance
		if len(m.bbo) > 1 {
			firstMid := m.bbo[0].mid
			lastMid := m.bbo[len(m.bbo)-1].mid
			if firstMid > 0 && lastMid > 0 {
				s.MidReturnBps = math.Log(lastMid/firstMid) * 10_000
			}
			returnAt := func(duration time.Duration) float64 {
				cutoff := now.Add(-duration)
				i := sort.Search(len(m.bbo), func(i int) bool { return m.bbo[i].at.After(cutoff) }) - 1
				if i < 0 || m.bbo[i].mid <= 0 || lastMid <= 0 {
					return 0
				}
				return math.Log(lastMid/m.bbo[i].mid) * 10_000
			}
			s.MidReturn1mBps = returnAt(time.Minute)
			s.MidReturn5mBps = returnAt(5 * time.Minute)
			highWindow := 0.0
			for _, point := range m.bbo {
				if point.mid > highWindow {
					highWindow = point.mid
				}
			}
			if highWindow > 0 && lastMid > 0 {
				s.MidDrawdownBps = math.Max(0, math.Log(highWindow/lastMid)*10_000)
			}
			cutoff30s := now.Add(-30 * time.Second)
			low30s := lastMid
			ofi30s, depth30s := 0.0, 0.0
			start30s := sort.Search(len(m.bbo), func(i int) bool {
				return !m.bbo[i].at.Before(cutoff30s)
			})
			if start30s > 0 {
				start30s--
			}
			for i := start30s; i < len(m.bbo); i++ {
				point := m.bbo[i]
				if !point.at.Before(cutoff30s) && point.mid > 0 && point.mid < low30s {
					low30s = point.mid
				}
				if i == 0 || point.at.Before(cutoff30s) {
					continue
				}
				previous := m.bbo[i-1]
				event := 0.0
				if point.bid >= previous.bid {
					event += point.bidSize
				}
				if point.bid <= previous.bid {
					event -= previous.bidSize
				}
				if point.ask <= previous.ask {
					event -= point.askSize
				}
				if point.ask >= previous.ask {
					event += previous.askSize
				}
				ofi30s += event
				depth30s += point.bidSize + point.askSize + previous.bidSize + previous.askSize
			}
			s.MidLow30s = low30s
			if low30s > 0 {
				s.MidRebound30sBps = math.Max(0, math.Log(lastMid/low30s)*10_000)
			}
			if depth30s > 0 {
				s.OrderFlowImbalance30s = math.Max(-1, math.Min(1, 2*ofi30s/depth30s))
			}
			lastBook := m.bbo[len(m.bbo)-1]
			depth := lastBook.bidSize + lastBook.askSize
			spread := lastBook.ask - lastBook.bid
			if depth > 0 && spread > 0 {
				microprice := (lastBook.ask*lastBook.bidSize + lastBook.bid*lastBook.askSize) / depth
				s.MicropriceDisplacement = math.Max(-1, math.Min(1, (microprice-lastMid)/(spread/2)))
			}
			cutoff5m := now.Add(-5 * time.Minute)
			high5m := 0.0
			secondMids := make([]fastEvidenceBBO, 0, 300)
			for _, point := range m.bbo {
				if point.at.Before(cutoff5m) {
					continue
				}
				s.BBOCount5m++
				if point.mid > high5m {
					high5m = point.mid
				}
				last := len(secondMids) - 1
				if last >= 0 && secondMids[last].at.Unix() == point.at.Unix() {
					secondMids[last] = point
				} else {
					secondMids = append(secondMids, point)
				}
			}
			if high5m > 0 && lastMid > 0 {
				s.MidDrawdown5mBps = math.Max(0, math.Log(high5m/lastMid)*10_000)
			}
			midVarianceNumerator, buyVarianceNumerator, sellVarianceNumerator := 0.0, 0.0, 0.0
			for i := 1; i < len(secondMids); i++ {
				previous, current := secondMids[i-1], secondMids[i]
				dt := current.at.Sub(previous.at).Seconds()
				if dt <= 0 || previous.mid <= 0 || current.mid <= 0 ||
					previous.ask <= 0 || current.ask <= 0 || previous.bid <= 0 || current.bid <= 0 {
					continue
				}
				midReturn := math.Log(current.mid / previous.mid)
				buyReturn := math.Log(current.ask / previous.ask)
				sellReturn := math.Log(current.bid / previous.bid)
				midVarianceNumerator += midReturn * midReturn
				buyVarianceNumerator += buyReturn * buyReturn
				sellVarianceNumerator += sellReturn * sellReturn
				s.MidVolatilityObservedSeconds5m += dt
				s.MidVolatilitySamples5m++
			}
			if s.MidVolatilityObservedSeconds5m > 0 {
				s.MidVolatilityPerSqrtSecond5mBps = math.Sqrt(
					midVarianceNumerator/s.MidVolatilityObservedSeconds5m) * 10_000
				s.BuyVolatilityPerSqrtSecond5mBps = math.Sqrt(
					buyVarianceNumerator/s.MidVolatilityObservedSeconds5m) * 10_000
				s.SellVolatilityPerSqrtSecond5mBps = math.Sqrt(
					sellVarianceNumerator/s.MidVolatilityObservedSeconds5m) * 10_000
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
	s.VolumeBalance = ComputeVolumeBalance(m.trades, m.bbo, now, m.window)
	if s.TradeCount > 0 && s.BBOCount > 0 {
		s.Health = HealthDegraded
		if s.TradeCount >= m.minTrades && s.BBOCount >= m.minBBOUpdates {
			s.Health = HealthHealthy
		}
	}
	return s
}
