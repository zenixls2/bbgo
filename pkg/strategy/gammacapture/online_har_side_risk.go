package gammacapture

import (
	"math"
	"time"
)

// SideHARVarianceRiskDecision keeps BUY/ask and SELL/bid variance forecasts
// separate. Integrated variances use log-return-squared units over Horizon.
type SideHARVarianceRiskDecision struct {
	Healthy bool
	Reason  string
	Horizon time.Duration
	At      time.Time

	Buy  HARVarianceDecision
	Sell HARVarianceDecision

	ConservativeForecastVariance float64
	ConservativeBaselineVariance float64
	VarianceRatePerSecond        float64
	BaselineRatePerSecond        float64
	ElevatedRisk                 bool
	DeescalatedRisk              bool
}

type sideHARMinuteClose struct {
	at        time.Time
	bid, ask  float64
	segment   uint64
	askReturn float64
	bidReturn float64
}

type sideHARPending struct {
	anchor       int
	matures      int
	buyFeatures  HARVarianceFeatures
	sellFeatures HARVarianceFeatures
	buyCurrent   float64
	sellCurrent  float64
}

// OnlineSideHARVarianceRisk converts change-driven BBO into closed one-minute
// executable prices and trains side-specific online HAR forecasts only when a
// non-overlapping future label matures.
type OnlineSideHARVarianceRisk struct {
	horizon      time.Duration
	horizonSteps int
	shortSteps   int
	mediumSteps  int

	buyModel  *OnlineHARVarianceModel
	sellModel *OnlineHARVarianceModel
	closes    []sideHARMinuteClose
	pending   []sideHARPending

	currentMinute       time.Time
	currentBid          float64
	currentAsk          float64
	currentGap          bool
	lastFinalizedMinute time.Time
	segment             uint64
	decision            SideHARVarianceRiskDecision
}

func NewOnlineSideHARVarianceRisk(horizon time.Duration) *OnlineSideHARVarianceRisk {
	steps := int(horizon / time.Minute)
	if steps <= 0 {
		steps = 15
		horizon = 15 * time.Minute
	}
	return &OnlineSideHARVarianceRisk{
		horizon: horizon, horizonSteps: steps,
		shortSteps:  int(math.Max(1, math.Min(15, float64(steps)))),
		mediumSteps: int(math.Max(1, math.Min(30, float64(steps)))),
		buyModel:    NewOnlineHARVarianceModel(), sellModel: NewOnlineHARVarianceModel(),
		segment: 1,
	}
}

func (m *OnlineSideHARVarianceRisk) ObserveBBO(at time.Time, bid, ask float64, gapBefore bool) {
	if m == nil || at.IsZero() || bid <= 0 || ask < bid {
		return
	}
	minute := at.UTC().Truncate(time.Minute)
	if m.currentMinute.IsZero() {
		m.currentMinute, m.currentBid, m.currentAsk, m.currentGap = minute, bid, ask, gapBefore
		return
	}
	if minute.Equal(m.currentMinute) {
		m.currentBid, m.currentAsk = bid, ask
		m.currentGap = m.currentGap || gapBefore
		return
	}
	if minute.Before(m.currentMinute) {
		return
	}
	m.finalizeCurrentMinute()
	m.currentMinute, m.currentBid, m.currentAsk, m.currentGap = minute, bid, ask, gapBefore
}

// Snapshot finalizes no partial minute. The returned decision is therefore
// causal as of the last completely observed UTC minute.
func (m *OnlineSideHARVarianceRisk) Snapshot() SideHARVarianceRiskDecision {
	if m == nil {
		return SideHARVarianceRiskDecision{Reason: "nil side HAR variance model"}
	}
	return m.decision
}

func (m *OnlineSideHARVarianceRisk) finalizeCurrentMinute() {
	if m.currentMinute.IsZero() {
		return
	}
	if m.currentGap || !m.lastFinalizedMinute.IsZero() && !m.currentMinute.Equal(m.lastFinalizedMinute.Add(time.Minute)) {
		m.segment++
	}
	close := sideHARMinuteClose{at: m.currentMinute, bid: m.currentBid, ask: m.currentAsk, segment: m.segment}
	if len(m.closes) > 0 {
		previous := m.closes[len(m.closes)-1]
		if previous.segment == close.segment && close.at.Equal(previous.at.Add(time.Minute)) {
			close.askReturn = math.Log(close.ask / previous.ask)
			close.bidReturn = math.Log(close.bid / previous.bid)
		}
	}
	m.closes = append(m.closes, close)
	m.lastFinalizedMinute = close.at
	m.processClose(len(m.closes) - 1)
	// Retain enough geometry for pending labels and current features. Learned
	// coefficients remain in the recursive models, so old raw closes can drop.
	retention := 3*m.horizonSteps + 2
	if len(m.closes) > retention {
		drop := len(m.closes) - retention
		m.closes = append([]sideHARMinuteClose(nil), m.closes[drop:]...)
		kept := m.pending[:0]
		for _, pending := range m.pending {
			pending.anchor -= drop
			pending.matures -= drop
			if pending.anchor >= 0 {
				kept = append(kept, pending)
			}
		}
		m.pending = kept
	}
}

func (m *OnlineSideHARVarianceRisk) processClose(index int) {
	remaining := m.pending[:0]
	for _, pending := range m.pending {
		if pending.matures > index {
			remaining = append(remaining, pending)
			continue
		}
		buyFuture, buyOK := sideHARWindow(m.closes, pending.anchor+1, pending.matures, true)
		sellFuture, sellOK := sideHARWindow(m.closes, pending.anchor+1, pending.matures, false)
		if buyOK && sellOK {
			m.buyModel.Update(pending.buyFeatures, pending.buyCurrent, buyFuture.realized)
			m.sellModel.Update(pending.sellFeatures, pending.sellCurrent, sellFuture.realized)
		}
	}
	m.pending = remaining
	close := m.closes[index]
	if index < m.horizonSteps {
		return
	}
	buyFeatures, buyCurrent, buyOK := m.featuresAt(index, true)
	sellFeatures, sellCurrent, sellOK := m.featuresAt(index, false)
	if buyOK && sellOK {
		buyDecision := m.buyModel.Predict(buyFeatures, buyCurrent)
		sellDecision := m.sellModel.Predict(sellFeatures, sellCurrent)
		m.decision = combineSideHARDecision(close.at, m.horizon, buyDecision, sellDecision)
	}
	minuteOfDay := close.at.Hour()*60 + close.at.Minute()
	if minuteOfDay%m.horizonSteps != 0 || !buyOK || !sellOK {
		return
	}
	m.pending = append(m.pending, sideHARPending{
		anchor: index, matures: index + m.horizonSteps,
		buyFeatures: buyFeatures, sellFeatures: sellFeatures,
		buyCurrent: buyCurrent, sellCurrent: sellCurrent,
	})
}

func (m *OnlineSideHARVarianceRisk) featuresAt(index int, buy bool) (HARVarianceFeatures, float64, bool) {
	var features HARVarianceFeatures
	short, ok1 := sideHARWindow(m.closes, index-m.shortSteps+1, index, buy)
	medium, ok2 := sideHARWindow(m.closes, index-m.mediumSteps+1, index, buy)
	long, ok3 := sideHARWindow(m.closes, index-m.horizonSteps+1, index, buy)
	if !ok1 || !ok2 || !ok3 || long.realized <= 0 {
		return features, 0, false
	}
	features = HARVarianceFeatures{
		ShortRate: short.rate, MediumRate: medium.rate, LongRate: long.rate,
		DownsideShare: long.downsideShare, JumpFraction: long.jumpFraction,
	}
	return features, long.realized, true
}

type sideHARWindowStats struct {
	realized, rate, downsideShare, jumpFraction float64
}

func sideHARWindow(closes []sideHARMinuteClose, start, end int, buy bool) (sideHARWindowStats, bool) {
	var out sideHARWindowStats
	if start < 1 || end < start || end >= len(closes) {
		return out, false
	}
	segment := closes[start].segment
	previous := 0.0
	var bipower float64
	for index := start; index <= end; index++ {
		if closes[index].segment != segment {
			return sideHARWindowStats{}, false
		}
		value := closes[index].bidReturn
		if buy {
			value = closes[index].askReturn
		}
		out.realized += value * value
		if value < 0 {
			out.downsideShare += value * value
		}
		if index > start {
			bipower += math.Abs(value) * math.Abs(previous)
		}
		previous = value
	}
	count := end - start + 1
	if count < 2 || out.realized <= 0 {
		return sideHARWindowStats{}, false
	}
	out.rate = out.realized / float64(count)
	out.downsideShare /= out.realized
	bipower *= math.Pi / 2
	out.jumpFraction = clampRatio((out.realized-math.Min(out.realized, bipower))/out.realized, 0, 1)
	return out, true
}

func combineSideHARDecision(at time.Time, horizon time.Duration, buy, sell HARVarianceDecision) SideHARVarianceRiskDecision {
	d := SideHARVarianceRiskDecision{At: at, Horizon: horizon, Buy: buy, Sell: sell, Reason: "side HAR variance warming up"}
	d.ConservativeForecastVariance = math.Max(buy.ForecastVariance, sell.ForecastVariance)
	d.ConservativeBaselineVariance = math.Max(buy.CurrentVariance, sell.CurrentVariance)
	if horizon > 0 {
		d.VarianceRatePerSecond = d.ConservativeForecastVariance / horizon.Seconds()
		d.BaselineRatePerSecond = d.ConservativeBaselineVariance / horizon.Seconds()
	}
	d.Healthy = buy.Healthy && sell.Healthy && d.VarianceRatePerSecond > 0
	// Both executable sides must independently reject zero feature-driven
	// stress. This avoids turning spread asymmetry into a one-sided risk alarm.
	d.ElevatedRisk = d.Healthy && buy.ElevatedRisk && sell.ElevatedRisk
	d.DeescalatedRisk = d.Healthy && buy.DeescalatedRisk && sell.DeescalatedRisk
	if d.Healthy {
		d.Reason = "side-specific online HAR variance forecast"
	}
	return d
}
