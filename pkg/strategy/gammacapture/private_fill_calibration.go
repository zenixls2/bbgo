package gammacapture

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

// PrivateFillCalibrationConfig controls the optional causal calibration of
// private maker fills.  It is deliberately separate from the ledger: a ledger
// can be enabled for audit/research while this estimator is still warming.
// None of the readiness thresholds is an order gate.
type PrivateFillCalibrationConfig struct {
	Enabled                  bool           `json:"enabled" yaml:"enabled"`
	ShadowOnly               bool           `json:"shadowOnly" yaml:"shadowOnly"`
	Horizon                  types.Duration `json:"horizon" yaml:"horizon"`
	HalfLife                 types.Duration `json:"halfLife" yaml:"halfLife"`
	MinimumFills             int            `json:"minimumFills" yaml:"minimumFills"`
	MinimumTouchObservations int            `json:"minimumTouchObservations" yaml:"minimumTouchObservations"`
	ConfidenceZScore         float64        `json:"confidenceZScore" yaml:"confidenceZScore"`
	MaxAdverseSelectionBps   float64        `json:"maxAdverseSelectionBps" yaml:"maxAdverseSelectionBps"`
	MaxPendingFills          int            `json:"maxPendingFills" yaml:"maxPendingFills"`
	MaxTrackedOrders         int            `json:"maxTrackedOrders" yaml:"maxTrackedOrders"`
}

func (c *PrivateFillCalibrationConfig) setDefaults() {
	if c.Horizon <= 0 {
		c.Horizon = types.Duration(5 * time.Minute)
	}
	if c.HalfLife <= 0 {
		c.HalfLife = types.Duration(24 * time.Hour)
	}
	if c.MinimumFills <= 0 {
		c.MinimumFills = 32
	}
	if c.MinimumTouchObservations <= 0 {
		c.MinimumTouchObservations = 32
	}
	if c.ConfidenceZScore <= 0 || math.IsNaN(c.ConfidenceZScore) || math.IsInf(c.ConfidenceZScore, 0) {
		c.ConfidenceZScore = 1.645
	}
	if c.MaxAdverseSelectionBps <= 0 || math.IsNaN(c.MaxAdverseSelectionBps) || math.IsInf(c.MaxAdverseSelectionBps, 0) {
		c.MaxAdverseSelectionBps = 50
	}
	if c.MaxPendingFills <= 0 {
		c.MaxPendingFills = 4096
	}
	if c.MaxTrackedOrders <= 0 {
		c.MaxTrackedOrders = 256
	}
}

type privateFillPendingLabel struct {
	At        time.Time
	MaturesAt time.Time
	Side      types.SideType
	Price     float64
	OrderID   uint64
	TradeID   uint64
}

type privateFillTrackedOrder struct {
	Side      types.SideType
	Price     float64
	CreatedAt time.Time
	Touched   bool
	Filled    bool
}

// PrivateFillCalibrationObservation is the small causal interface used by
// live callbacks and replay.  A fill is labeled only by a later executable
// BBO, never by the BBO captured at the fill itself.
type PrivateFillCalibrationObservation struct {
	At      time.Time
	TradeID uint64
	OrderID uint64
	Side    types.SideType
	Price   float64
}

// PrivateFillCalibrationSnapshot is diagnostic state plus conservative
// recommendations.  Effective counts are EW counts, so a long-lived process
// does not let ancient fills dominate current execution conditions.
type PrivateFillCalibrationSnapshot struct {
	Enabled    bool
	Ready      bool
	Stale      bool
	ShadowOnly bool
	Reason     string

	Fills                          int
	EffectiveFills                 float64
	LastLabelAt                    time.Time
	Age                            time.Duration
	MeanAdverseSelectionBps        float64
	AdverseSelectionUpperBps       float64
	RecommendedAdverseSelectionBps float64

	TouchObservations             float64
	TouchFills                    float64
	TouchToFillProbability        float64
	TouchToFillLowerBound         float64
	RecommendedTouchToFillHaircut float64
	TouchReady                    bool
}

// PrivateFillCalibrationModel is intentionally a bounded online estimator.
// ObserveBBO is O(number of currently tracked orders), while fill labels and
// touch/fill sufficient statistics are O(1) amortized.  It never scans the
// historical ledger in the quote hot path; startup replay is the only ledger
// scan and is bounded by the configured capture window.
type PrivateFillCalibrationModel struct {
	mu     sync.Mutex
	config PrivateFillCalibrationConfig

	pending []privateFillPendingLabel
	orders  map[uint64]privateFillTrackedOrder

	// EW first and second moments are more stable than repeatedly rebuilding a
	// sample slice, and are enough for a normal-approximation upper bound.
	statsAt       time.Time
	lastLabelAt   time.Time
	lastTouchAt   time.Time
	adverseWeight float64
	adverseSum    float64
	adverseSumSq  float64
	touchWeight   float64
	touchFills    float64
	fillCount     int

	seenTradeIDs []uint64
	seenTradeSet map[uint64]struct{}
}

func NewPrivateFillCalibrationModel(config PrivateFillCalibrationConfig) *PrivateFillCalibrationModel {
	config.setDefaults()
	return &PrivateFillCalibrationModel{
		config:       config,
		orders:       make(map[uint64]privateFillTrackedOrder),
		seenTradeSet: make(map[uint64]struct{}),
	}
}

func (m *PrivateFillCalibrationModel) configSnapshot() PrivateFillCalibrationConfig {
	if m == nil {
		return PrivateFillCalibrationConfig{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.config
}

func privateFillDecayFactor(delta time.Duration, halfLife time.Duration) float64 {
	if delta <= 0 || halfLife <= 0 {
		return 1
	}
	return math.Exp2(-delta.Seconds() / halfLife.Seconds())
}

func (m *PrivateFillCalibrationModel) decayToLocked(at time.Time) {
	if m == nil || at.IsZero() {
		return
	}
	if m.statsAt.IsZero() {
		m.statsAt = at
		return
	}
	if !at.After(m.statsAt) {
		return
	}
	factor := privateFillDecayFactor(at.Sub(m.statsAt), time.Duration(m.config.HalfLife))
	m.adverseWeight *= factor
	m.adverseSum *= factor
	m.adverseSumSq *= factor
	m.touchWeight *= factor
	m.touchFills *= factor
	m.statsAt = at
}

func (m *PrivateFillCalibrationModel) rememberTradeIDLocked(id uint64) bool {
	if id == 0 {
		return true
	}
	if _, exists := m.seenTradeSet[id]; exists {
		return false
	}
	m.seenTradeSet[id] = struct{}{}
	m.seenTradeIDs = append(m.seenTradeIDs, id)
	// This is only a duplicate-boundary cache, not a history store.
	const maxSeenTradeIDs = 8192
	if len(m.seenTradeIDs) > maxSeenTradeIDs {
		oldest := m.seenTradeIDs[0]
		delete(m.seenTradeSet, oldest)
		m.seenTradeIDs = m.seenTradeIDs[1:]
	}
	return true
}

// ObserveOrder starts a bounded order-lifetime observation.  Accepted maker
// orders only are sent here, so rejected submit intents cannot create fake
// queue exposure.
func (m *PrivateFillCalibrationModel) ObserveOrder(orderID uint64, side types.SideType, price float64, at time.Time) {
	if m == nil || orderID == 0 || price <= 0 || math.IsNaN(price) || math.IsInf(price, 0) {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.orders) >= m.config.MaxTrackedOrders {
		// The live strategy normally has two orders. If a venue/API burst ever
		// exceeds the bound, evict one arbitrary old observation rather than
		// allowing unbounded memory growth. Calibration remains conservative.
		for id := range m.orders {
			delete(m.orders, id)
			break
		}
	}
	current := m.orders[orderID]
	if !current.CreatedAt.IsZero() && current.Touched {
		// Preserve a touch already observed across a partial-fill update.
		current.Side, current.Price = side, price
		m.orders[orderID] = current
		return
	}
	m.orders[orderID] = privateFillTrackedOrder{Side: side, Price: price, CreatedAt: at}
}

// ObserveOrderEnd closes one order-lifetime observation.  Touch observations
// are counted once per order, avoiding a bias from repeated status callbacks.
func (m *PrivateFillCalibrationModel) ObserveOrderEnd(orderID uint64, at time.Time, filled bool) {
	if m == nil || orderID == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.decayToLocked(at)
	order, ok := m.orders[orderID]
	if !ok {
		return
	}
	if filled {
		order.Filled = true
		order.Touched = true
	}
	if order.Touched {
		m.touchWeight++
		if order.Filled {
			m.touchFills++
		}
		m.lastTouchAt = at
	}
	delete(m.orders, orderID)
}

// ObserveFill queues a fill for a future executable-BBO adverse-selection
// label.  Duplicate trade IDs are ignored, which makes checkpoint delta
// replay safe at the cursor boundary.
func (m *PrivateFillCalibrationModel) ObserveFill(observation PrivateFillCalibrationObservation) {
	if m == nil || observation.Price <= 0 || math.IsNaN(observation.Price) || math.IsInf(observation.Price, 0) || observation.At.IsZero() {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.rememberTradeIDLocked(observation.TradeID) {
		return
	}
	m.decayToLocked(observation.At)
	if observation.OrderID != 0 {
		if order, ok := m.orders[observation.OrderID]; ok {
			order.Touched = true
			order.Filled = true
			m.orders[observation.OrderID] = order
		}
	}
	pending := privateFillPendingLabel{
		At: observation.At, MaturesAt: observation.At.Add(time.Duration(m.config.Horizon)),
		Side: observation.Side, Price: observation.Price,
		OrderID: observation.OrderID, TradeID: observation.TradeID,
	}
	index := sort.Search(len(m.pending), func(i int) bool {
		return !m.pending[i].MaturesAt.Before(pending.MaturesAt)
	})
	m.pending = append(m.pending, privateFillPendingLabel{})
	copy(m.pending[index+1:], m.pending[index:])
	m.pending[index] = pending
	if len(m.pending) > m.config.MaxPendingFills {
		// Drop the oldest pending label when the explicit memory bound is hit.
		// It is preferable to report a lower effective sample count than to
		// manufacture a label from an unknown future BBO.
		m.pending = m.pending[1:]
	}
}

// ObserveBBO advances pending fill labels and records whether current maker
// orders were touched.  The adverse-selection label uses executable prices:
// BUY is marked against the future bid, SELL against the future ask.
func (m *PrivateFillCalibrationModel) ObserveBBO(at time.Time, bid, ask float64) {
	if m == nil || at.IsZero() || bid <= 0 || ask <= 0 || ask <= bid {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.decayToLocked(at)
	for index := range m.pending {
		if m.pending[index].MaturesAt.After(at) {
			break
		}
		label := m.pending[index]
		adverse := 0.0
		switch label.Side {
		case types.SideTypeBuy:
			adverse = math.Max(0, math.Log(label.Price/bid)*10_000)
		case types.SideTypeSell:
			adverse = math.Max(0, math.Log(ask/label.Price)*10_000)
		default:
			adverse = math.Max(0, math.Abs(math.Log((bid+ask)/(2*label.Price)))*10_000)
		}
		if !math.IsNaN(adverse) && !math.IsInf(adverse, 0) {
			m.adverseWeight++
			m.adverseSum += adverse
			m.adverseSumSq += adverse * adverse
			m.fillCount++
			m.lastLabelAt = at
		}
	}
	if matured := sort.Search(len(m.pending), func(i int) bool { return m.pending[i].MaturesAt.After(at) }); matured > 0 {
		copy(m.pending, m.pending[matured:])
		m.pending = m.pending[:len(m.pending)-matured]
	}
	for id, order := range m.orders {
		if order.Touched {
			continue
		}
		switch order.Side {
		case types.SideTypeBuy:
			if ask <= order.Price {
				order.Touched = true
			}
		case types.SideTypeSell:
			if bid >= order.Price {
				order.Touched = true
			}
		}
		m.orders[id] = order
	}
}

func (m *PrivateFillCalibrationModel) staleAge(now time.Time) time.Duration {
	if m == nil || now.IsZero() {
		return 0
	}
	latest := m.lastLabelAt
	if m.lastTouchAt.After(latest) {
		latest = m.lastTouchAt
	}
	if latest.IsZero() || now.Before(latest) {
		return 0
	}
	return now.Sub(latest)
}

func (m *PrivateFillCalibrationModel) SnapshotAt(now time.Time) PrivateFillCalibrationSnapshot {
	if m == nil {
		return PrivateFillCalibrationSnapshot{Reason: "private-fill calibration disabled"}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.decayToLocked(now)
	config := m.config
	out := PrivateFillCalibrationSnapshot{
		Enabled: config.Enabled, ShadowOnly: config.ShadowOnly, Fills: m.fillCount,
		EffectiveFills: m.adverseWeight, LastLabelAt: m.lastLabelAt,
		TouchObservations: m.touchWeight, TouchFills: m.touchFills,
	}
	out.Age = m.staleAge(now)
	maxAge := time.Duration(config.HalfLife) * 2
	if horizonAge := time.Duration(config.Horizon) * 2; horizonAge > maxAge {
		maxAge = horizonAge
	}
	latestEvidenceAt := m.lastLabelAt
	if m.lastTouchAt.After(latestEvidenceAt) {
		latestEvidenceAt = m.lastTouchAt
	}
	out.Stale = !latestEvidenceAt.IsZero() && out.Age > maxAge
	if m.adverseWeight > 0 {
		out.MeanAdverseSelectionBps = m.adverseSum / m.adverseWeight
		variance := math.Max(0, m.adverseSumSq/m.adverseWeight-out.MeanAdverseSelectionBps*out.MeanAdverseSelectionBps)
		se := math.Sqrt(variance / math.Max(1, m.adverseWeight))
		out.AdverseSelectionUpperBps = math.Min(config.MaxAdverseSelectionBps,
			math.Max(0, out.MeanAdverseSelectionBps+config.ConfidenceZScore*se))
		out.RecommendedAdverseSelectionBps = out.AdverseSelectionUpperBps
	}
	if m.touchWeight > 0 {
		out.TouchToFillProbability = math.Max(0, math.Min(1, (m.touchFills+1)/(m.touchWeight+2)))
		se := math.Sqrt(out.TouchToFillProbability * (1 - out.TouchToFillProbability) / math.Max(1, m.touchWeight+3))
		out.TouchToFillLowerBound = math.Max(0, math.Min(1,
			out.TouchToFillProbability-config.ConfidenceZScore*se))
		out.RecommendedTouchToFillHaircut = math.Max(0.05, out.TouchToFillLowerBound)
	}
	out.Ready = !out.Stale && m.adverseWeight >= float64(config.MinimumFills)
	out.TouchReady = !out.Stale && m.touchWeight >= float64(config.MinimumTouchObservations)
	switch {
	case out.Stale:
		out.Reason = "private-fill calibration stale; waiting for fresh matured labels"
	case out.Ready:
		out.Reason = "private-fill calibration ready"
	case m.adverseWeight <= 0:
		out.Reason = "warming: no matured private fills"
	default:
		out.Reason = "warming: insufficient effective matured private fills"
	}
	return out
}

type privateFillCalibrationCheckpoint struct {
	StatsAt       time.Time                      `json:"statsAt,omitempty"`
	LastLabelAt   time.Time                      `json:"lastLabelAt,omitempty"`
	LastTouchAt   time.Time                      `json:"lastTouchAt,omitempty"`
	AdverseWeight float64                        `json:"adverseWeight"`
	AdverseSum    float64                        `json:"adverseSum"`
	AdverseSumSq  float64                        `json:"adverseSumSq"`
	TouchWeight   float64                        `json:"touchWeight"`
	TouchFills    float64                        `json:"touchFills"`
	FillCount     int                            `json:"fillCount"`
	Pending       []privateFillPendingCheckpoint `json:"pending,omitempty"`
	SeenTradeIDs  []uint64                       `json:"seenTradeIDs,omitempty"`
}

type privateFillPendingCheckpoint struct {
	At        time.Time      `json:"at"`
	MaturesAt time.Time      `json:"maturesAt"`
	Side      types.SideType `json:"side"`
	Price     float64        `json:"price"`
	OrderID   uint64         `json:"orderID,omitempty"`
	TradeID   uint64         `json:"tradeID,omitempty"`
}

func (m *PrivateFillCalibrationModel) checkpoint() *privateFillCalibrationCheckpoint {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	checkpoint := &privateFillCalibrationCheckpoint{
		StatsAt: m.statsAt, LastLabelAt: m.lastLabelAt, LastTouchAt: m.lastTouchAt,
		AdverseWeight: m.adverseWeight, AdverseSum: m.adverseSum, AdverseSumSq: m.adverseSumSq,
		TouchWeight: m.touchWeight, TouchFills: m.touchFills, FillCount: m.fillCount,
		SeenTradeIDs: append([]uint64(nil), m.seenTradeIDs...),
		Pending:      make([]privateFillPendingCheckpoint, 0, len(m.pending)),
	}
	for _, pending := range m.pending {
		checkpoint.Pending = append(checkpoint.Pending, privateFillPendingCheckpoint{
			At: pending.At, MaturesAt: pending.MaturesAt, Side: pending.Side,
			Price: pending.Price, OrderID: pending.OrderID, TradeID: pending.TradeID,
		})
	}
	return checkpoint
}

func (m *PrivateFillCalibrationModel) restore(checkpoint *privateFillCalibrationCheckpoint) error {
	if m == nil || checkpoint == nil {
		return fmt.Errorf("private-fill calibration checkpoint is missing")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if checkpoint.AdverseWeight < 0 || checkpoint.TouchWeight < 0 || checkpoint.TouchFills < 0 || checkpoint.TouchFills > checkpoint.TouchWeight {
		return fmt.Errorf("private-fill calibration checkpoint statistics are invalid")
	}
	m.statsAt, m.lastLabelAt, m.lastTouchAt = checkpoint.StatsAt, checkpoint.LastLabelAt, checkpoint.LastTouchAt
	m.adverseWeight, m.adverseSum, m.adverseSumSq = checkpoint.AdverseWeight, checkpoint.AdverseSum, checkpoint.AdverseSumSq
	m.touchWeight, m.touchFills, m.fillCount = checkpoint.TouchWeight, checkpoint.TouchFills, checkpoint.FillCount
	m.pending = m.pending[:0]
	for _, pending := range checkpoint.Pending {
		if len(m.pending) >= m.config.MaxPendingFills || pending.Price <= 0 || pending.MaturesAt.IsZero() {
			break
		}
		m.pending = append(m.pending, privateFillPendingLabel{
			At: pending.At, MaturesAt: pending.MaturesAt, Side: pending.Side,
			Price: pending.Price, OrderID: pending.OrderID, TradeID: pending.TradeID,
		})
	}
	sort.SliceStable(m.pending, func(i, j int) bool { return m.pending[i].MaturesAt.Before(m.pending[j].MaturesAt) })
	m.seenTradeIDs = append(m.seenTradeIDs[:0], checkpoint.SeenTradeIDs...)
	if len(m.seenTradeIDs) > 8192 {
		m.seenTradeIDs = m.seenTradeIDs[len(m.seenTradeIDs)-8192:]
	}
	m.seenTradeSet = make(map[uint64]struct{}, len(m.seenTradeIDs))
	for _, id := range m.seenTradeIDs {
		m.seenTradeSet[id] = struct{}{}
	}
	m.orders = make(map[uint64]privateFillTrackedOrder)
	return nil
}

// privateFillCalibrationLedgerEvents loads only the configured bounded replay
// interval. Missing ledgers are normal on a fresh deployment and are treated
// as an empty calibration source.
type privateFillCalibrationLedgerEvent struct {
	At    time.Time
	Event PrivateOrderFillLedgerEvent
}

func privateFillLedgerPath(config PrivateOrderFillLedgerConfig, symbol string) string {
	path := strings.TrimSpace(config.Path)
	if path == "" {
		path = filepath.Join("data", "gammacapture", "state", "private-ledger", symbol+".jsonl")
	}
	return strings.ReplaceAll(path, "{symbol}", symbol)
}

func privateFillLedgerSize(config PrivateOrderFillLedgerConfig, symbol string) int64 {
	info, err := os.Stat(privateFillLedgerPath(config, symbol))
	if err != nil {
		return 0
	}
	return info.Size()
}

func loadPrivateFillCalibrationLedger(config PrivateOrderFillLedgerConfig, symbol, productionVersion string, cutoff, now time.Time, deltaOnly bool, replayOffset int64) ([]privateFillCalibrationLedgerEvent, error) {
	path := privateFillLedgerPath(config, symbol)
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open private fill ledger %s: %w", path, err)
	}
	defer file.Close()
	if replayOffset > 0 {
		info, statErr := file.Stat()
		if statErr != nil {
			return nil, fmt.Errorf("stat private fill ledger %s: %w", path, statErr)
		}
		if replayOffset < info.Size() {
			if _, seekErr := file.Seek(replayOffset, io.SeekStart); seekErr != nil {
				return nil, fmt.Errorf("seek private fill ledger %s: %w", path, seekErr)
			}
		} else if replayOffset == info.Size() {
			return nil, nil
		} else {
			// Rotation/truncation invalidates the byte cursor. Rebuild from the
			// causal time cutoff rather than skipping the new file.
			replayOffset = 0
		}
	}
	result := make([]privateFillCalibrationLedgerEvent, 0, 256)
	partialRecord := false
	if replayOffset > 0 {
		var previous [1]byte
		if _, seekErr := file.Seek(replayOffset-1, io.SeekStart); seekErr != nil {
			return nil, fmt.Errorf("seek private fill ledger boundary %s: %w", path, seekErr)
		}
		if _, readErr := file.Read(previous[:]); readErr != nil {
			return nil, fmt.Errorf("read private fill ledger boundary %s: %w", path, readErr)
		}
		partialRecord = previous[0] != '\n'
		if _, seekErr := file.Seek(replayOffset, io.SeekStart); seekErr != nil {
			return nil, fmt.Errorf("seek private fill ledger start %s: %w", path, seekErr)
		}
	}
	reader := bufio.NewReader(file)
	if partialRecord {
		// A checkpoint can end in the middle of a JSONL record only if the
		// process crashed during append. Discard that partial line and start
		// at the next complete event.
		if _, readErr := reader.ReadString('\n'); readErr != nil && readErr != io.EOF {
			return nil, fmt.Errorf("advance private fill ledger %s: %w", path, readErr)
		}
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 32*1024), 2*1024*1024)
	for scanner.Scan() {
		var event PrivateOrderFillLedgerEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		// BBOAt is the local executable-book clock used by the capture files
		// and is therefore the preferred join key. ExchangeAt can be on a
		// venue clock and must not be compared directly with local BBO time.
		at := event.BBOAt
		if at.IsZero() {
			at = event.ObservedAt
		}
		if at.IsZero() {
			at = event.ExchangeAt
		}
		if at.IsZero() || at.Before(cutoff) || deltaOnly && !at.After(cutoff) || !now.IsZero() && at.After(now) {
			continue
		}
		if event.Symbol != "" && event.Symbol != symbol {
			continue
		}
		if event.Strategy != "" && event.Strategy != ID {
			continue
		}
		if productionVersion != "" && event.ProductionVersion != productionVersion {
			continue
		}
		result = append(result, privateFillCalibrationLedgerEvent{At: at, Event: event})
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		return nil, fmt.Errorf("read private fill ledger %s: %w", path, err)
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].At.Equal(result[j].At) {
			return result[i].Event.Sequence < result[j].Event.Sequence
		}
		return result[i].At.Before(result[j].At)
	})
	return result, nil
}

func (s *Strategy) observePrivateFillCalibrationLedgerEvent(event privateFillCalibrationLedgerEvent) {
	model := s.makerPrivateFillCalibration
	if model == nil {
		return
	}
	e := event.Event
	at := event.At
	switch e.EventType {
	case PrivateLedgerEventOrderSubmitResult, PrivateLedgerEventOrderUpdate:
		if e.OrderID != 0 && e.Price.Float64() > 0 && e.Side != "" {
			model.ObserveOrder(e.OrderID, e.Side, e.Price.Float64(), at)
			if e.Status.Closed() || !e.IsWorking && e.EventType == PrivateLedgerEventOrderSubmitResult {
				model.ObserveOrderEnd(e.OrderID, at, e.ExecutedQuantity.Sign() > 0)
			}
		}
	case PrivateLedgerEventFill:
		model.ObserveFill(PrivateFillCalibrationObservation{
			At: at, TradeID: e.TradeID, OrderID: e.OrderID, Side: e.Side,
			Price: e.TradePrice.Float64(),
		})
		if e.OrderID != 0 && e.Status.Closed() {
			model.ObserveOrderEnd(e.OrderID, at, true)
		}
	case PrivateLedgerEventCancelResult:
		if e.OrderID != 0 {
			model.ObserveOrderEnd(e.OrderID, at, e.ExecutedQuantity.Sign() > 0)
		}
	}
}
