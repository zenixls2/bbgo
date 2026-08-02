package gammacapture

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

const onlineArrivalStateVersion = 2

// OnlineArrivalConfig controls a dual-timescale estimator trained only from
// Binance BBO observations. Startup replays the raw local capture through the
// same causal learner; no frozen or symbol-specific model artifact is loaded.
type OnlineArrivalConfig struct {
	Enabled               bool           `json:"enabled" yaml:"enabled"`
	DistanceStepBps       float64        `json:"distanceStepBps" yaml:"distanceStepBps"`
	FastHalfLife          types.Duration `json:"fastHalfLife" yaml:"fastHalfLife"`
	SlowHalfLife          types.Duration `json:"slowHalfLife" yaml:"slowHalfLife"`
	PersistenceInterval   types.Duration `json:"persistenceInterval" yaml:"persistenceInterval"`
	StartupLookback       types.Duration `json:"startupLookback" yaml:"startupLookback"`
	StartupMaxAge         types.Duration `json:"startupMaxAge" yaml:"startupMaxAge"`
	RequireStartupHistory bool           `json:"requireStartupHistory" yaml:"requireStartupHistory"`
}

func (c *OnlineArrivalConfig) setDefaults() {
	if c.DistanceStepBps <= 0 {
		c.DistanceStepBps = 5
	}
	if c.FastHalfLife <= 0 {
		c.FastHalfLife = types.Duration(6 * time.Hour)
	}
	if c.SlowHalfLife <= 0 {
		c.SlowHalfLife = types.Duration(72 * time.Hour)
	}
	if c.SlowHalfLife < c.FastHalfLife {
		c.SlowHalfLife = c.FastHalfLife
	}
	if c.PersistenceInterval <= 0 {
		c.PersistenceInterval = types.Duration(10 * time.Minute)
	}
	if c.StartupLookback <= 0 {
		c.StartupLookback = c.SlowHalfLife
	}
	if c.StartupMaxAge <= 0 {
		c.StartupMaxAge = types.Duration(15 * time.Minute)
	}
}

// OnlineArrivalState is persisted with the strategy. Cells contain decayed
// event counts and exposure, while the raw Binance capture reconstructs the
// rolling in-memory models and advances only windows newer than the cursor.
type OnlineArrivalState struct {
	mu                    sync.RWMutex
	Version               int                           `json:"version"`
	UpdatedAt             time.Time                     `json:"updatedAt,omitempty"`
	LastResolvedByHorizon map[string]time.Time          `json:"lastResolvedByHorizon,omitempty"`
	Cells                 map[string]*OnlineArrivalCell `json:"cells,omitempty"`
}

type OnlineArrivalCell struct {
	HorizonSeconds int64     `json:"horizonSeconds"`
	DistanceBps    float64   `json:"distanceBps"`
	FastUpEvents   float64   `json:"fastUpEvents"`
	FastDownEvents float64   `json:"fastDownEvents"`
	FastExposure   float64   `json:"fastExposureHours"`
	FastWindows    float64   `json:"fastEffectiveWindows"`
	SlowUpEvents   float64   `json:"slowUpEvents"`
	SlowDownEvents float64   `json:"slowDownEvents"`
	SlowExposure   float64   `json:"slowExposureHours"`
	SlowWindows    float64   `json:"slowEffectiveWindows"`
	FirstObserved  time.Time `json:"firstObserved,omitempty"`
	LastObserved   time.Time `json:"lastObserved,omitempty"`
}

func NewOnlineArrivalState() *OnlineArrivalState {
	return &OnlineArrivalState{
		Version:               onlineArrivalStateVersion,
		LastResolvedByHorizon: make(map[string]time.Time),
		Cells:                 make(map[string]*OnlineArrivalCell),
	}
}

func (s *OnlineArrivalState) ensure() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureUnlocked()
}

func (s *OnlineArrivalState) ensureUnlocked() {
	// Version 2 changes labels from midpoint excursions to executable BBO
	// excursions. Old sufficient statistics cannot be mixed with the new
	// likelihood, so discard them and causally rebuild from the local BBO archive.
	if s.Version != onlineArrivalStateVersion {
		s.Version = onlineArrivalStateVersion
		s.UpdatedAt = time.Time{}
		s.LastResolvedByHorizon = make(map[string]time.Time)
		s.Cells = make(map[string]*OnlineArrivalCell)
		return
	}
	if s.LastResolvedByHorizon == nil {
		s.LastResolvedByHorizon = make(map[string]time.Time)
	}
	if s.Cells == nil {
		s.Cells = make(map[string]*OnlineArrivalCell)
	}
}

func (s *OnlineArrivalState) MarshalJSON() ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	type persisted OnlineArrivalState
	return json.Marshal((*persisted)(s))
}

func (s *OnlineArrivalState) UnmarshalJSON(data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	type persisted OnlineArrivalState
	var decoded persisted
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	s.Version = decoded.Version
	s.UpdatedAt = decoded.UpdatedAt
	s.LastResolvedByHorizon = decoded.LastResolvedByHorizon
	s.Cells = decoded.Cells
	s.ensureUnlocked()
	return nil
}

func onlineArrivalHorizonKey(horizon time.Duration) string {
	return strconv.FormatInt(int64(horizon/time.Second), 10)
}

func onlineArrivalCellKey(horizon time.Duration, distanceBps float64) string {
	return fmt.Sprintf("%d:%.6f", int64(horizon/time.Second), distanceBps)
}

func onlineArrivalDistanceBuckets(c MarketMakerConfig) []float64 {
	c.setDefaults()
	step := c.OnlineArrival.DistanceStepBps
	minimum := math.Max(step, c.MinimumHalfSpreadBps)
	maximum := math.Max(minimum, 2*c.MaximumHalfSpreadBps)
	first := math.Ceil((minimum-1e-9)/step) * step
	out := make([]float64, 0, int(math.Ceil((maximum-first)/step))+1)
	for distance := first; distance <= maximum+1e-9; distance += step {
		out = append(out, distance)
	}
	return out
}

func onlineArrivalBucket(c MarketMakerConfig, distanceBps float64) float64 {
	buckets := onlineArrivalDistanceBuckets(c)
	if len(buckets) == 0 {
		return distanceBps
	}
	index := sort.SearchFloat64s(buckets, distanceBps-1e-9)
	if index >= len(buckets) {
		return buckets[len(buckets)-1]
	}
	return buckets[index]
}

func decayOnlineValue(value float64, elapsed, halfLife time.Duration) float64 {
	if value <= 0 || elapsed <= 0 || halfLife <= 0 {
		return value
	}
	return value * math.Exp2(-elapsed.Seconds()/halfLife.Seconds())
}

func (c *OnlineArrivalCell) observe(
	at time.Time,
	horizon time.Duration,
	up, down bool,
	fastHalfLife, slowHalfLife time.Duration,
) {
	if !c.LastObserved.IsZero() {
		elapsed := at.Sub(c.LastObserved)
		c.FastUpEvents = decayOnlineValue(c.FastUpEvents, elapsed, fastHalfLife)
		c.FastDownEvents = decayOnlineValue(c.FastDownEvents, elapsed, fastHalfLife)
		c.FastExposure = decayOnlineValue(c.FastExposure, elapsed, fastHalfLife)
		c.FastWindows = decayOnlineValue(c.FastWindows, elapsed, fastHalfLife)
		c.SlowUpEvents = decayOnlineValue(c.SlowUpEvents, elapsed, slowHalfLife)
		c.SlowDownEvents = decayOnlineValue(c.SlowDownEvents, elapsed, slowHalfLife)
		c.SlowExposure = decayOnlineValue(c.SlowExposure, elapsed, slowHalfLife)
		c.SlowWindows = decayOnlineValue(c.SlowWindows, elapsed, slowHalfLife)
	}
	if c.FirstObserved.IsZero() {
		c.FirstObserved = at
	}
	exposure := horizon.Hours()
	c.FastExposure += exposure
	c.SlowExposure += exposure
	c.FastWindows++
	c.SlowWindows++
	if up {
		c.FastUpEvents++
		c.SlowUpEvents++
	}
	if down {
		c.FastDownEvents++
		c.SlowDownEvents++
	}
	c.LastObserved = at
}

func (m *MarketMakerHorizonModel) bindOnlineArrival(state *OnlineArrivalState) {
	m.onlineArrival = state
	if m.onlineArrival != nil {
		m.onlineArrival.ensure()
	}
}

// updateOnlineArrival resolves non-overlapping horizon windows. Every resolved
// path updates every configured distance bucket, which prevents the strategy's
// current quote choice from censoring training data for alternative quotes.
func (m *MarketMakerHorizonModel) updateOnlineArrival(now time.Time, c MarketMakerConfig) bool {
	if m.onlineArrival == nil || !c.OnlineArrival.Enabled || now.IsZero() {
		return false
	}
	c.setDefaults()
	m.onlineArrival.mu.Lock()
	defer m.onlineArrival.mu.Unlock()
	m.onlineArrival.ensureUnlocked()
	updated := false
	for _, horizon := range c.TradingHorizons() {
		horizonKey := onlineArrivalHorizonKey(horizon)
		lastResolved := m.onlineArrival.LastResolvedByHorizon[horizonKey]
		if !lastResolved.IsZero() && now.Sub(lastResolved) < horizon {
			continue
		}
		startAt := now.Add(-horizon)
		// Anchor at the last observation at or before the requested start and
		// forward-fill it only within the outage tolerance. Using the first
		// observation after start would backfill a cold model with a price that
		// was not yet known.
		startIndex := sort.Search(len(m.points), func(i int) bool {
			return !m.points[i].At.Before(startAt)
		})
		if startIndex >= len(m.points) || m.points[startIndex].At.After(startAt) {
			startIndex--
		}
		if startIndex < 0 || startAt.Sub(m.points[startIndex].At) >= marketMakerHorizonGapThreshold {
			continue
		}
		start := m.points[startIndex]
		startBid, startAsk := start.bidPrice(), start.askPrice()
		if startBid <= 0 || startAsk < startBid || start.GapBefore {
			continue
		}
		maxBid, minAsk := startBid, startAsk
		continuous := true
		for i := startIndex + 1; i < len(m.points) && !m.points[i].At.After(now); i++ {
			point := m.points[i]
			if point.GapBefore {
				continuous = false
				break
			}
			bid, ask := point.bidPrice(), point.askPrice()
			if bid <= 0 || ask < bid {
				continuous = false
				break
			}
			maxBid = math.Max(maxBid, bid)
			minAsk = math.Min(minAsk, ask)
		}
		if !continuous || minAsk <= 0 {
			continue
		}
		// Up means a sell quote became executable through best bid; down means
		// a buy quote became executable through best ask.
		upMoveBps := math.Log(maxBid/startBid) * 10_000
		downMoveBps := math.Log(startAsk/minAsk) * 10_000
		for _, distance := range onlineArrivalDistanceBuckets(c) {
			key := onlineArrivalCellKey(horizon, distance)
			cell := m.onlineArrival.Cells[key]
			if cell == nil {
				cell = &OnlineArrivalCell{
					HorizonSeconds: int64(horizon / time.Second),
					DistanceBps:    distance,
				}
				m.onlineArrival.Cells[key] = cell
			}
			cell.observe(
				now,
				horizon,
				upMoveBps >= distance,
				downMoveBps >= distance,
				time.Duration(c.OnlineArrival.FastHalfLife),
				time.Duration(c.OnlineArrival.SlowHalfLife),
			)
		}
		m.onlineArrival.LastResolvedByHorizon[horizonKey] = now
		m.onlineArrival.UpdatedAt = now
		updated = true
	}
	return updated
}

func (m MarketMakerHorizonModel) onlineArrivalDecision(
	now time.Time,
	c MarketMakerConfig,
	horizon time.Duration,
	distanceBps float64,
) (MarketMakerHorizonDecision, bool) {
	return m.onlineArrivalDecisionAtSideDistances(now, c, horizon, distanceBps, distanceBps, 2*distanceBps)
}

func (m MarketMakerHorizonModel) onlineArrivalDecisionAtSideDistances(
	now time.Time,
	c MarketMakerConfig,
	horizon time.Duration,
	buyDistanceBps, sellDistanceBps, grossQuoteEdgeBps float64,
) (MarketMakerHorizonDecision, bool) {
	if m.onlineArrival == nil || !c.OnlineArrival.Enabled || horizon <= 0 || buyDistanceBps <= 0 || sellDistanceBps <= 0 {
		return MarketMakerHorizonDecision{}, false
	}
	c.setDefaults()
	m.onlineArrival.mu.RLock()
	defer m.onlineArrival.mu.RUnlock()
	buckets := onlineArrivalDistanceBuckets(c)
	if len(buckets) == 0 || buyDistanceBps > buckets[len(buckets)-1]+1e-9 || sellDistanceBps > buckets[len(buckets)-1]+1e-9 {
		return MarketMakerHorizonDecision{}, false
	}
	buyBucket := onlineArrivalBucket(c, buyDistanceBps)
	sellBucket := onlineArrivalBucket(c, sellDistanceBps)
	buyCell := m.onlineArrival.Cells[onlineArrivalCellKey(horizon, buyBucket)]
	sellCell := m.onlineArrival.Cells[onlineArrivalCellKey(horizon, sellBucket)]
	if buyCell == nil || sellCell == nil || buyCell.SlowExposure <= 0 || sellCell.SlowExposure <= 0 {
		return MarketMakerHorizonDecision{}, false
	}

	type decayedCell struct {
		fastUp, fastDown, fastExposure, fastWindows float64
		slowUp, slowDown, slowExposure, slowWindows float64
	}
	decay := func(cell *OnlineArrivalCell) decayedCell {
		elapsed := now.Sub(cell.LastObserved)
		fastDecay := decayOnlineValue(1, elapsed, time.Duration(c.OnlineArrival.FastHalfLife))
		slowDecay := decayOnlineValue(1, elapsed, time.Duration(c.OnlineArrival.SlowHalfLife))
		return decayedCell{
			fastUp: cell.FastUpEvents * fastDecay, fastDown: cell.FastDownEvents * fastDecay,
			fastExposure: cell.FastExposure * fastDecay, fastWindows: cell.FastWindows * fastDecay,
			slowUp: cell.SlowUpEvents * slowDecay, slowDown: cell.SlowDownEvents * slowDecay,
			slowExposure: cell.SlowExposure * slowDecay, slowWindows: cell.SlowWindows * slowDecay,
		}
	}
	buy, sell := decay(buyCell), decay(sellCell)
	if buy.slowExposure <= 0 || sell.slowExposure <= 0 {
		return MarketMakerHorizonDecision{}, false
	}

	// Sell/up hazard comes from the bid path; buy/down hazard comes from the ask
	// path. Exposure, not event count, determines fast-regime credibility.
	fastSellRate, fastBuyRate := 0.0, 0.0
	if sell.fastExposure > 0 {
		fastSellRate = sell.fastUp / sell.fastExposure
	}
	if buy.fastExposure > 0 {
		fastBuyRate = buy.fastDown / buy.fastExposure
	}
	slowSellRate := sell.slowUp / sell.slowExposure
	slowBuyRate := buy.slowDown / buy.slowExposure
	sellFastWeight := sell.fastWindows / (sell.fastWindows + float64(c.HorizonMinSamples))
	buyFastWeight := buy.fastWindows / (buy.fastWindows + float64(c.HorizonMinSamples))
	sellRate := sellFastWeight*fastSellRate + (1-sellFastWeight)*slowSellRate
	buyRate := buyFastWeight*fastBuyRate + (1-buyFastWeight)*slowBuyRate
	effectiveWindows := math.Min(buy.slowWindows, sell.slowWindows)
	netEdge := grossQuoteEdgeBps - 2*c.MakerFeeBps - 2*c.AdverseSelectionBps - c.MinimumNetEdgeBps
	reason := "insufficient online arrival exposure"
	if effectiveWindows >= float64(c.HorizonMinSamples) && sell.slowUp > 0 && buy.slowDown > 0 {
		reason = "online dual-timescale fee-adjusted two-sided edge per hour"
	}
	return MarketMakerHorizonDecision{
		Horizon: horizon, HorizonSeconds: int64(horizon / time.Second),
		QuoteDistanceBps:    grossQuoteEdgeBps / 2,
		BuyTouchDistanceBps: buyDistanceBps, SellTouchDistanceBps: sellDistanceBps,
		UpCrosses: int(math.Round(sell.slowUp)), DownCrosses: int(math.Round(buy.slowDown)),
		UpCrossesPerHour: sellRate, DownCrossesPerHour: buyRate,
		ObservedHours:       math.Min(buy.slowExposure, sell.slowExposure),
		EffectiveSamples:    effectiveWindows,
		OnlineFastWeight:    (buyFastWeight + sellFastWeight) / 2,
		EstimatorSource:     "online-bbo",
		NetRoundTripEdgeBps: netEdge,
		ScoreBpsPerHour:     math.Min(sellRate, buyRate) * math.Max(0, netEdge),
		UpdatedAt:           now,
		Reason:              reason,
	}, true
}
