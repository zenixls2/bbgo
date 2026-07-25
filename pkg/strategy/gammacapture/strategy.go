package gammacapture

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/c9s/bbgo/pkg/bbgo"
	"github.com/c9s/bbgo/pkg/exchange/retry"
	"github.com/c9s/bbgo/pkg/fixedpoint"
	"github.com/c9s/bbgo/pkg/types"
)

var log = logrus.WithField("strategy", ID)

const (
	marketMakerClientOrderPrefix         = "gcmm-"
	legacyBinanceBrokerClientOrderPrefix = "x-NSUYEBKM"
)

func init() { bbgo.RegisterStrategy(ID, &Strategy{}) }

type RuntimeState string

const (
	StateInitializing RuntimeState = "INITIALIZING"
	StateWarmingUp    RuntimeState = "WARMING_UP"
	StateDisarmed     RuntimeState = "DISARMED"
	StateArmedLong    RuntimeState = "ARMED_LONG"
	StateEntryPending RuntimeState = "ENTRY_PENDING"
	StateLong         RuntimeState = "LONG"
	StateWeakening    RuntimeState = "WEAKENING"
	StateCooldown     RuntimeState = "COOLDOWN"
	StateSuspended    RuntimeState = "SUSPENDED"
	StateHalted       RuntimeState = "HALTED"
)

type State struct {
	Engine *CrossingEngine `json:"engine"`
	// RawSignals retains all probability transitions for diagnostics. Signals
	// is the health-scoped state machine used for actual trading decisions.
	RawSignals         SignalState `json:"rawSignals"`
	Signals            SignalState `json:"signals"`
	SignalHealthActive bool        `json:"signalHealthActive"`
	EntryGrid          int64       `json:"entryGrid"`
	HighWaterGrid      int64       `json:"highWaterGrid"`
	// EntryPrice and HighWaterPrice keep trade exits independent of the
	// crossing-engine origin.  The engine may reset after an ambiguous candle;
	// a reset must never turn into a synthetic stop or take-profit.
	EntryPrice     fixedpoint.Value `json:"entryPrice"`
	HighWaterPrice fixedpoint.Value `json:"highWaterPrice"`
	// Entry barrier values are selected once at entry and remain immutable for
	// the position, so adaptive policy cannot move a stop retrospectively.
	EntryTargetBarriers   int           `json:"entryTargetBarriers"`
	EntryHardStopBarriers int           `json:"entryHardStopBarriers"`
	EntrySoftStopBarriers int           `json:"entrySoftStopBarriers"`
	EntryPredictedTP      float64       `json:"entryPredictedTP"`
	Feedback              FeedbackState `json:"feedback"`
	PendingExitReason     string        `json:"pendingExitReason"`
	EnteredAt             time.Time     `json:"enteredAt"`
	// EntryEligibleUntil preserves a completed confidence upcrossing long
	// enough to require a stable confirmation candle instead of buying the
	// stretched impulse candle that created the signal.
	EntryEligibleUntil time.Time        `json:"entryEligibleUntil"`
	EntrySignalPrice   fixedpoint.Value `json:"entrySignalPrice"`
	EntryNeedsRetrace  bool             `json:"entryNeedsRetrace"`
	CooldownUntil      time.Time        `json:"cooldownUntil"`
	LastDecision       string           `json:"lastDecision"`
	Runtime            RuntimeState     `json:"runtime"`
	TrendSamples       []TrendSample    `json:"trendSamples,omitempty"`
	LastReferenceTime  time.Time        `json:"lastReferenceTime,omitempty"`
	LastMarketTradeID  uint64           `json:"lastMarketTradeID,omitempty"`
}

// FeedbackState is a persisted, causal scorecard. It records outcomes only
// after an exit decision, so it can be used for later calibration without
// leaking future prices into the current signal.
type FeedbackState struct {
	ResolvedExits     int     `json:"resolvedExits"`
	PositiveNetExits  int     `json:"positiveNetExits"`
	NegativeNetExits  int     `json:"negativeNetExits"`
	PredictedTPSum    float64 `json:"predictedTPSum"`
	RealizedNetBpsSum float64 `json:"realizedNetBpsSum"`
	LastOutcome       string  `json:"lastOutcome"`
}

type BarrierPlan struct {
	Target   int
	HardStop int
	SoftStop int
	Passage  FirstPassage
	EdgeBps  float64
}

type TrendSample struct {
	Time  time.Time        `json:"time"`
	Price fixedpoint.Value `json:"price"`
}

// Strategy deliberately owns one symbol per instance. BBGO subscribes before its
// streams start, so dynamic JPY discovery is a preflight responsibility; a generator
// must emit one validated allowlist instance for each selected market.
type Strategy struct {
	EnvironmentRef *bbgo.Environment `json:"-" yaml:"-"`
	Market         types.Market      `json:"-" yaml:"-"`
	Position       *types.Position   `persistence:"position"`
	State          *State            `persistence:"state"`
	GateStats      *GateStats        `persistence:"gate_stats"`

	Config

	session                    *bbgo.ExchangeSession
	executor                   *bbgo.GeneralOrderExecutor
	model                      *IntensityModel
	fastModel                  *IntensityModel
	fastEvidence               *FastEvidenceModel
	referenceMu                sync.Mutex
	bookMu                     sync.RWMutex
	bestBid                    fixedpoint.Value
	bestAsk                    fixedpoint.Value
	bestBookAt                 time.Time
	marketMakerMu              sync.Mutex
	lastMakerQuoteAt           time.Time
	lastMakerDiagnosticAt      time.Time
	lastMakerBid, lastMakerAsk fixedpoint.Value
	// Best bid/ask observed when the current quote window was submitted. These
	// are used for adverse-reprice detection; comparing the live BBO directly
	// with our intentionally distant quote would trigger a false reprice.
	lastMakerBestBid, lastMakerBestAsk float64
	lastMakerMid                       float64
	lastMakerImbalance                 float64
	lastMakerDirection                 float64
	makerHorizonModel                  MarketMakerHorizonModel
	makerHorizonDecision               MarketMakerHorizonDecision
	makerInventoryBand                 InventoryBand
	makerSideAllocationBias            float64
	makerSideAllocationReady           bool
	makerSideDistanceBias              float64
	makerSideDistanceReady             bool
	makerBuyQuoteNotional              fixedpoint.Value
	makerSellQuoteNotional             fixedpoint.Value
	makerTradingWindowStartedAt        time.Time
	makerTradingWindowEndsAt           time.Time
	makerAskSince                      time.Time // age of the currently active passive ask
	makerAskAnchorMid                  float64   // mid when the currently active ask was submitted
	makerInventoryExposureSince        time.Time // age of continuous non-zero base inventory
	makerInventoryAnchorMid            float64
	makerResetCooldownUntil            time.Time
	bbgo.StrategyController
}

func (s *Strategy) ID() string                       { return ID }
func (s *Strategy) InstanceID() string               { return ID + ":" + s.Symbol }
func (s *Strategy) CurrentPosition() *types.Position { return s.Position }

func (s *Strategy) Subscribe(session *bbgo.ExchangeSession) {
	s.setDefaults()
	if s.MarketMaker.Enabled {
		session.Subscribe(types.BookTickerChannel, s.Symbol, types.SubscribeOptions{})
		session.Subscribe(types.MarketTradeChannel, s.Symbol, types.SubscribeOptions{})
		return
	}
	if s.usesRealtimeReference() {
		if s.usesMarketTradeReference() {
			session.Subscribe(types.MarketTradeChannel, s.Symbol, types.SubscribeOptions{})
		}
		session.Subscribe(types.BookTickerChannel, s.Symbol, types.SubscribeOptions{})
		return
	}
	session.Subscribe(types.KLineChannel, s.Symbol, types.SubscribeOptions{Interval: types.Interval(s.Interval)})
}

func (s *Strategy) ClosePosition(ctx context.Context, percentage fixedpoint.Value) error {
	if s.Position == nil || s.executor == nil {
		return fmt.Errorf("strategy is not initialized")
	}
	base := s.Position.GetBase()
	if base.Sign() <= 0 {
		return nil
	}
	quantity := s.Market.TruncateQuantity(base.Mul(percentage))
	if s.Market.IsDustQuantity(quantity, s.lastPrice()) {
		return nil
	}
	_, err := s.executor.SubmitOrders(ctx, types.SubmitOrder{Symbol: s.Symbol, Market: s.Market, Side: types.SideTypeSell, Type: types.OrderTypeMarket, Quantity: quantity, Tag: "gammacapture-exit"})
	return err
}

func (s *Strategy) Run(ctx context.Context, _ bbgo.OrderExecutor, session *bbgo.ExchangeSession) error {
	if err := s.Validate(); err != nil {
		return err
	}
	market, ok := session.Market(s.Symbol)
	if !ok {
		return fmt.Errorf("market %s not found", s.Symbol)
	}
	s.Market = market
	s.session = session
	newState := s.resetDeterministicRuntimeState(market)
	if s.Position == nil {
		s.Position = types.NewPositionFromMarket(market)
	}
	if !newState {
		newState = s.State == nil || s.State.Engine == nil
	}
	if newState {
		s.State = &State{Engine: NewCrossingEngine(s.Barrier.Width, time.Duration(s.Barrier.MinDwell), s.Barrier.MaxCrossingsPerEvent), Runtime: StateInitializing}
	}
	s.model = NewIntensityModel(s.Intensity)
	if s.MarketMaker.Enabled {
		s.fastModel = NewIntensityModel(s.MarketMaker.fastIntensityConfig())
		s.fastEvidence = NewFastEvidenceModel(FastEvidenceConfig{
			Window:        time.Duration(s.MarketMaker.FastEvidenceWindow),
			MinTrades:     s.MarketMaker.FastEvidenceMinTrades,
			MinBBOUpdates: s.MarketMaker.FastEvidenceMinBBOUpdates,
		})
	} else {
		s.fastModel = nil
		s.fastEvidence = nil
	}
	if s.GateStats == nil {
		s.GateStats = &GateStats{}
	}
	// A backtest service has already loaded the full requested date range into
	// MarketDataStore before strategies are started.  Warming from that store
	// here would seed the model and hysteresis state with future candles.  Let
	// deterministic replays warm one closed candle at a time instead.
	if s.Environment != "backtest" && s.Environment != "replay" {
		if s.AggTradeWarmup.Enabled {
			if err := s.warmModelFromAggTrades(time.Now()); err != nil {
				return err
			}
			if s.fastEvidence != nil {
				if err := s.warmFastEvidenceFromCapture(time.Now()); err != nil {
					// The archive warmup is an acceleration path only. A live
					// stream remains authoritative when the capture file is
					// absent or is being rotated by the collector.
					log.WithError(err).WithField("symbol", s.Symbol).Warn("fast evidence archive warmup unavailable")
				}
			}
		} else if newState {
			s.warmModelFromSession(session)
		}
	}
	s.executor = bbgo.NewGeneralOrderExecutor(session, s.Symbol, ID, s.InstanceID(), s.Position)
	s.executor.OnProfit(func(_ types.Trade, profit *types.Profit) {
		s.recordExecutionFeedback(profit)
	})
	s.executor.Bind()
	if s.MarketMaker.Enabled && s.MarketMaker.StartupCancelStaleOrders {
		if err := s.reconcileMarketMakerOrders(ctx); err != nil {
			return err
		}
	}
	s.Status = types.StrategyStatusRunning
	s.OnSuspend(func() { s.State.Runtime = StateSuspended; bbgo.Sync(ctx, s) })
	s.OnResume(func() { s.State.Runtime = StateWarmingUp; bbgo.Sync(ctx, s) })
	s.OnEmergencyStop(func() {
		s.State.Runtime = StateHalted
		_ = s.executor.GracefulCancel(ctx)
		_ = s.ClosePosition(ctx, fixedpoint.One)
		bbgo.Sync(ctx, s)
	})

	if s.usesRealtimeReference() {
		session.MarketDataStream.OnBookTickerUpdate(func(ticker types.BookTicker) {
			s.onBookTicker(ticker)
			if s.MarketMaker.Enabled {
				s.onMarketMakerBook(ctx, ticker)
				return
			}
			if s.usesMicropriceReference() {
				s.onMicropriceReference(ctx, ticker)
				return
			}
		})
		if s.MarketMaker.Enabled {
			session.MarketDataStream.OnMarketTrade(types.TradeWith(s.Symbol, func(trade types.Trade) {
				s.onMarketMakerTrade(trade)
			}))
		} else if s.usesMarketTradeReference() {
			session.MarketDataStream.OnMarketTrade(types.TradeWith(s.Symbol, func(trade types.Trade) {
				s.onMarketTradeReference(ctx, trade)
			}))
		}
	} else {
		if s.MarketMaker.Enabled {
			session.MarketDataStream.OnBookTickerUpdate(func(ticker types.BookTicker) {
				s.onBookTicker(ticker)
				s.onMarketMakerBook(ctx, ticker)
			})
			session.MarketDataStream.OnMarketTrade(types.TradeWith(s.Symbol, func(trade types.Trade) {
				s.onMarketMakerTrade(trade)
			}))
			return nil
		}
		session.MarketDataStream.OnKLineClosed(types.KLineWith(s.Symbol, types.Interval(s.Interval), func(k types.KLine) {
			s.onKLineReference(ctx, k)
		}))
	}
	return nil
}

// marketMakerClientOrderID makes maker orders attributable to this strategy
// when they are recovered through the exchange REST API after a restart. It
// intentionally stays short enough for Binance's client-order-id limit.
func marketMakerClientOrderID(side types.SideType) string {
	return fmt.Sprintf("%s%s-%x", marketMakerClientOrderPrefix, strings.ToLower(string(side)), time.Now().UnixNano())
}

func isOwnedMarketMakerOrder(order types.Order) bool {
	// Binance's REST order converter intentionally normalizes LIMIT_MAKER to
	// the generic LIMIT type (the maker-only constraint is not represented in
	// the shared order type).  Therefore ownership must accept both forms;
	// the client-order-id prefix remains the authoritative strategy marker.
	if order.Type != types.OrderTypeLimitMaker && order.Type != types.OrderTypeLimit {
		return false
	}
	return strings.HasPrefix(order.ClientOrderID, marketMakerClientOrderPrefix) ||
		strings.HasPrefix(order.ClientOrderID, legacyBinanceBrokerClientOrderPrefix)
}

// reconcileMarketMakerOrders fails closed: if the exchange cannot confirm
// that stale maker orders are gone, the strategy does not start quoting. This
// prevents a restart from leaving an old quote unmanaged while a new quote
// window is submitted on top of it.
func (s *Strategy) reconcileMarketMakerOrders(ctx context.Context) error {
	reconcileCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	openOrders, err := retry.QueryOpenOrdersUntilSuccessfulLite(reconcileCtx, s.session.Exchange, s.Symbol)
	if err != nil {
		return fmt.Errorf("market-maker startup order reconciliation query failed: %w", err)
	}

	stale := make([]types.Order, 0, len(openOrders))
	for _, order := range openOrders {
		if isOwnedMarketMakerOrder(order) {
			stale = append(stale, order)
		}
	}
	if len(stale) == 0 {
		log.WithField("symbol", s.Symbol).Info("market-maker startup order reconciliation complete")
		return nil
	}

	log.WithFields(logrus.Fields{
		"symbol": s.Symbol,
		"orders": len(stale),
	}).Warn("market-maker cancelling stale startup orders")
	if err := s.session.Exchange.CancelOrders(reconcileCtx, stale...); err != nil {
		return fmt.Errorf("market-maker startup stale order cancellation failed: %w", err)
	}

	remaining, err := retry.QueryOpenOrdersUntilSuccessfulLite(reconcileCtx, s.session.Exchange, s.Symbol)
	if err != nil {
		return fmt.Errorf("market-maker startup order reconciliation verification failed: %w", err)
	}
	for _, order := range remaining {
		if isOwnedMarketMakerOrder(order) {
			return fmt.Errorf("market-maker startup reconciliation left order #%d open", order.OrderID)
		}
	}

	log.WithFields(logrus.Fields{
		"symbol":    s.Symbol,
		"cancelled": len(stale),
	}).Info("market-maker startup stale order reconciliation complete")
	return nil
}

func (s *Strategy) usesMarketTradeReference() bool {
	return s.ReferencePrice.Mode == "lastTrade"
}

func (s *Strategy) usesMicropriceReference() bool {
	return s.ReferencePrice.Mode == "microprice"
}

func (s *Strategy) usesRealtimeReference() bool {
	return s.usesMarketTradeReference() || s.usesMicropriceReference()
}

func (s *Strategy) onBookTicker(ticker types.BookTicker) {
	if ticker.Symbol != s.Symbol || ticker.Buy.Sign() <= 0 || ticker.Sell.Sign() <= 0 || ticker.Buy.Compare(ticker.Sell) > 0 {
		return
	}
	s.bookMu.Lock()
	s.bestBid = ticker.Buy
	s.bestAsk = ticker.Sell
	s.bestBookAt = time.Now()
	s.bookMu.Unlock()
}

// updateMarketMakerModel advances only the causal crossing/intensity model.
// Market-maker mode deliberately does not run the directional entry/exit
// state machine, but its inventory reset policy still needs a live model rather
// than the snapshot captured during startup warmup.
func (s *Strategy) updateMarketMakerModel(now time.Time, ticker types.BookTicker) ModelSnapshot {
	if s.model == nil || s.State == nil || s.State.Engine == nil || now.IsZero() {
		return ModelSnapshot{}
	}
	price := (ticker.Buy.Float64() + ticker.Sell.Float64()) / 2
	if micro, ok := microprice(ticker); ok {
		price = micro.Float64()
	}
	if price <= 0 {
		return s.model.Snapshot(now)
	}

	s.referenceMu.Lock()
	defer s.referenceMu.Unlock()
	if !s.State.LastReferenceTime.IsZero() && now.Before(s.State.LastReferenceTime) {
		return s.model.Snapshot(now)
	}
	s.State.LastReferenceTime = now
	events := s.State.Engine.Update(s.Symbol, fixedpoint.NewFromFloat(price), now, now, 0)
	for _, event := range events {
		s.model.Update(event)
		if s.fastModel != nil {
			s.fastModel.Update(event)
		}
	}
	return s.model.Snapshot(now)
}

func (s *Strategy) onMicropriceReference(ctx context.Context, ticker types.BookTicker) {
	s.onBookTicker(ticker)
	if ticker.Symbol != s.Symbol {
		return
	}
	price, ok := microprice(ticker)
	if !ok {
		return
	}
	s.referenceMu.Lock()
	defer s.referenceMu.Unlock()
	s.processReference(ctx, time.Now(), price, true)
}

func microprice(ticker types.BookTicker) (fixedpoint.Value, bool) {
	if ticker.Buy.Sign() <= 0 || ticker.Sell.Sign() <= 0 || ticker.BuySize.Sign() <= 0 || ticker.SellSize.Sign() <= 0 || ticker.Buy.Compare(ticker.Sell) > 0 {
		return fixedpoint.Zero, false
	}
	total := ticker.BuySize.Add(ticker.SellSize)
	if total.Sign() <= 0 {
		return fixedpoint.Zero, false
	}
	return ticker.Sell.Mul(ticker.BuySize).Add(ticker.Buy.Mul(ticker.SellSize)).Div(total), true
}

func bookImbalance(ticker types.BookTicker) float64 {
	buySize, sellSize := ticker.BuySize.Float64(), ticker.SellSize.Float64()
	denom := buySize + sellSize
	if denom <= 0 {
		return 0
	}
	return math.Max(-1, math.Min(1, (buySize-sellSize)/denom))
}

func directionSignal(snapshot ModelSnapshot) float64 {
	total := snapshot.LambdaUp + snapshot.LambdaDown
	if total <= 0 {
		return 0
	}
	return math.Max(-1, math.Min(1, (snapshot.LambdaUp-snapshot.LambdaDown)/total))
}

// resetDeterministicRuntimeState prevents persisted paper/replay state from
// leaking into a new deterministic backtest. Reusing a prior crossing window,
// cooldown, position, or gate counter would contaminate the evaluation and can
// introduce look-ahead across parameter trials.
func (s *Strategy) resetDeterministicRuntimeState(market types.Market) bool {
	if s.Environment != "backtest" && s.Environment != "replay" {
		return false
	}
	s.Position = types.NewPositionFromMarket(market)
	s.State = &State{
		Engine:  NewCrossingEngine(s.Barrier.Width, time.Duration(s.Barrier.MinDwell), s.Barrier.MaxCrossingsPerEvent),
		Runtime: StateInitializing,
	}
	s.GateStats = &GateStats{}
	return true
}

func (s *Strategy) onKLineReference(ctx context.Context, k types.KLine) {
	s.referenceMu.Lock()
	defer s.referenceMu.Unlock()
	s.processReference(ctx, k.EndTime.Time(), k.GetClose(), s.entryBarIsStable(k))
}

func (s *Strategy) onMarketMakerTrade(trade types.Trade) {
	if s.fastEvidence == nil || trade.Symbol != s.Symbol {
		return
	}
	at := trade.Time.Time()
	if at.IsZero() {
		at = time.Now()
	}
	s.fastEvidence.ObserveTrade(at, trade)
}

func (s *Strategy) onMarketTradeReference(ctx context.Context, trade types.Trade) {
	if trade.Symbol != s.Symbol || trade.Price.Sign() <= 0 {
		return
	}
	now := trade.Time.Time()
	if now.IsZero() {
		return
	}
	s.referenceMu.Lock()
	defer s.referenceMu.Unlock()
	if !s.State.LastReferenceTime.IsZero() && now.Before(s.State.LastReferenceTime) {
		return
	}
	if trade.ID != 0 && trade.ID <= s.State.LastMarketTradeID {
		return
	}
	s.processReference(ctx, now, trade.Price, true)
	if trade.ID != 0 {
		s.State.LastMarketTradeID = trade.ID
	}
}

func (s *Strategy) processReference(ctx context.Context, now time.Time, price fixedpoint.Value, entryStable bool) {
	if s.Status != types.StrategyStatusRunning {
		return
	}
	if s.MarketMaker.Enabled {
		return
	}
	if !s.State.LastReferenceTime.IsZero() && now.Before(s.State.LastReferenceTime) {
		return
	}
	s.State.LastReferenceTime = now
	_, _, snapshot, passage, rawSignal, signal := s.updateModelAt(now, price)
	s.observeTrend(now, price)

	if !s.Position.IsDust(price) {
		if s.State.EntryTargetBarriers > 0 && s.State.EntryHardStopBarriers > 0 {
			passage = s.passageFor(snapshot, s.State.EntryTargetBarriers, s.State.EntryHardStopBarriers)
		}
		s.managePosition(ctx, now, price, passage, signal)
		bbgo.Sync(ctx, s)
		return
	}
	if signal == SignalUp {
		s.State.EntryEligibleUntil = now.Add(time.Duration(s.Signal.EntryConfirmationWindow))
		s.State.EntrySignalPrice = price
		s.State.EntryNeedsRetrace = !entryStable
	} else if signal == SignalDown || passage.TP <= s.Signal.LowerThreshold {
		s.clearEntryEligibility()
	}
	s.GateStats.Time = now
	s.GateStats.FlatObservations++
	if rawSignal == SignalUp {
		s.GateStats.RawSignalUp++
	}
	if passage.TP > s.GateStats.MaxTPProbability {
		s.GateStats.MaxTPProbability = passage.TP
	}
	if edgeBps := s.expectedNetEdgeBps(passage); edgeBps > s.GateStats.MaxRawNetEdgeBps {
		s.GateStats.MaxRawNetEdgeBps = edgeBps
	}
	if now.Before(s.State.CooldownUntil) {
		s.GateStats.Cooldown++
		s.State.Runtime = StateCooldown
		return
	}
	entryPlan := s.defaultBarrierPlan(passage)
	if !s.ResearchForceEntry {
		if snapshot.Health != HealthHealthy {
			s.State.Runtime = StateWarmingUp
			return
		}
		s.GateStats.Healthy++
		if passage.TP >= s.Signal.EntryProbability {
			s.GateStats.ProbabilityReady++
		}
		if signal == SignalUp {
			s.GateStats.SignalUp++
		}
		if s.State.EntryEligibleUntil.IsZero() || now.After(s.State.EntryEligibleUntil) {
			s.State.Runtime = StateWarmingUp
			return
		}
		s.GateStats.SignalWindow++
		if passage.TP < s.Signal.EntryProbability {
			s.State.Runtime = StateWarmingUp
			return
		}
		s.GateStats.Probability++
		// First-passage expectancy explicitly values TP and SL outcomes. Unresolved
		// paths receive no credit, which is conservative because their eventual
		// time/probability exit is not known at entry.
		entryPlan = s.selectBarrierPlan(snapshot, passage)
		rangeBps := s.targetRangeBps(entryPlan.Target)
		if rangeBps > s.GateStats.MaxRangeBps {
			s.GateStats.MaxRangeBps = rangeBps
		}
		if !s.rangePasses(entryPlan.Target) {
			s.State.LastDecision = "blocked: target range does not cover fees and edge"
			return
		}
		s.GateStats.Range++
		edgeBps := entryPlan.EdgeBps
		if edgeBps > s.GateStats.MaxExpectancyBps {
			s.GateStats.MaxExpectancyBps = edgeBps
		}
		if edgeBps < s.Risk.MinimumNetEdgeBps {
			s.State.LastDecision = "blocked: insufficient first-passage net expectancy"
			return
		}
		s.GateStats.Expectancy++
		if !entryStable {
			s.State.LastDecision = "blocked: entry bar is stretched"
			return
		}
		s.GateStats.BarQuality++
		if !s.trendPasses(now, price) {
			s.State.LastDecision = "blocked: trend regime"
			return
		}
		s.GateStats.Trend++
		if !s.entryRetraceConfirmed(price) {
			s.State.LastDecision = "blocked: stretched signal has not retraced"
			return
		}
		s.GateStats.Retrace++
		if !s.entryBookPasses() {
			s.State.LastDecision = "blocked: stale or wide best-bid/best-ask"
			return
		}
		s.GateStats.Book++
	} else {
		s.GateStats.ResearchForced++
	}
	if s.availableQuoteBalance().Sign() > 0 {
		s.GateStats.Balance++
	}
	quantity := s.entryQuantity(price, entryPlan.HardStop)
	if quantity.IsZero() {
		s.State.LastDecision = "blocked: balance or market minimum"
		return
	}
	s.GateStats.Quantity++
	s.State.Runtime = StateEntryPending
	if _, err := s.executor.SubmitOrders(ctx, types.SubmitOrder{Symbol: s.Symbol, Market: s.Market, Side: types.SideTypeBuy, Type: types.OrderTypeMarket, Quantity: quantity, Tag: "gammacapture-entry"}); err != nil {
		s.State.LastDecision = "entry rejected: " + err.Error()
		s.State.Runtime = StateDisarmed
		return
	}
	s.State.EntryGrid = s.State.Engine.State
	s.State.Signals.ResetCycle()
	s.State.HighWaterGrid = s.State.EntryGrid
	s.State.EntryPrice = price
	s.State.HighWaterPrice = price
	s.State.EntryTargetBarriers = entryPlan.Target
	s.State.EntryHardStopBarriers = entryPlan.HardStop
	s.State.EntrySoftStopBarriers = entryPlan.SoftStop
	s.State.EntryPredictedTP = entryPlan.Passage.TP
	s.State.EnteredAt = now
	if s.ResearchForceEntry {
		s.State.LastDecision = "research forced entry"
	} else {
		s.State.LastDecision = "entered after completed confidence upcrossing"
	}
	s.State.Runtime = StateLong
	s.GateStats.Entries++
	bbgo.Sync(ctx, s)
}

// onMarketMakerBook refreshes a small two-sided maker-only quote. It is kept
// separate from the directional process so enabling the maker mode cannot
// accidentally open a market position from a stale barrier signal.
func (s *Strategy) onMarketMakerBook(ctx context.Context, ticker types.BookTicker) {
	if ticker.Symbol != s.Symbol || s.executor == nil || ticker.Buy.Sign() <= 0 || ticker.Sell.Sign() <= 0 || ticker.Sell.Compare(ticker.Buy) <= 0 {
		return
	}
	s.marketMakerMu.Lock()
	defer s.marketMakerMu.Unlock()
	now := time.Now()
	if s.fastEvidence != nil {
		s.fastEvidence.ObserveBBO(now, ticker)
	}
	base := s.availableBaseBalance()
	availableQuote := s.availableQuoteBalance()
	mid := (ticker.Buy.Float64() + ticker.Sell.Float64()) / 2
	s.makerHorizonModel.Observe(now, mid, s.MarketMaker)
	// Selling should only require enough base to pass the exchange's minimum
	// order filters. The risk-sized quoteNotional caps a normal ask size; it
	// must not suppress an otherwise valid smaller ask and create a one-sided
	// market maker.
	_, canSell := s.Market.GreaterThanMinimalOrderQuantity(types.SideTypeSell, ticker.Buy, base)
	modelSnapshot := s.updateMarketMakerModel(now, ticker)
	fastSnapshot := ModelSnapshot{}
	if s.fastModel != nil {
		fastSnapshot = s.fastModel.Snapshot(now)
	}
	fastEvidence := FastEvidenceSnapshot{Health: HealthInsufficient}
	if s.fastEvidence != nil {
		fastEvidence = s.fastEvidence.Snapshot(now)
	}
	direction := directionSignal(fastSnapshot)
	imbalance := bookImbalance(ticker)
	// A sparse/degraded fast window must not widen quotes from a one-event
	// volatility spike. The slow model remains the risk anchor; fast volatility
	// participates only when both the model and raw fast evidence are healthy.
	slowVolatility := modelSnapshot.GammaCaptureVolatility
	quoteVolatility := quoteRiskVolatility(slowVolatility, fastSnapshot.GammaCaptureVolatility, fastSnapshot.Health, fastEvidence.Health)
	fastQuoteVolatilityUsable := fastSnapshot.Health == HealthHealthy && fastEvidence.Health == HealthHealthy
	// The model volatility is supplemented by a symbol-specific empirical
	// floor. This prevents a temporarily quiet/degraded model from producing
	// an effectively unbounded risk-sized ticket, without imposing an
	// arbitrary JPY minimum or maximum. The floor is derived from recent
	// one-second BBO returns and is zero until enough observations exist.
	volatilityFloorBps := s.makerHorizonModel.EmpiricalVolatilityFloor(now, time.Duration(s.MarketMaker.HorizonLookback))
	effectiveVolatilityBps := math.Max(quoteVolatility*10_000, volatilityFloorBps)
	if effectiveVolatilityBps <= 0 {
		// Keep an already-resting quote alive until its selected trading window
		// ends. This avoids turning a short model/data gap into a cancel/recreate
		// loop that destroys queue priority and prevents fill statistics from
		// accumulating. New windows remain fail-closed until the symbol has
		// enough observed market data.
		if s.retainMakerQuoteDuringDataGap(now, ticker) {
			return
		}
		if !s.lastMakerQuoteAt.IsZero() {
			if err := s.executor.GracefulCancel(ctx); err != nil {
				log.WithError(err).Warn("market-maker missing volatility statistics cancellation failed")
			}
			s.lastMakerQuoteAt = time.Time{}
			s.makerTradingWindowStartedAt = time.Time{}
			s.makerTradingWindowEndsAt = time.Time{}
		}
		return
	}
	quoteVolatility = effectiveVolatilityBps / 10_000
	inventoryVolatility := inventoryRiskVolatility(slowVolatility, fastSnapshot.GammaCaptureVolatility, fastSnapshot.Health)
	inventoryVolatility = math.Max(inventoryVolatility, quoteVolatility)
	horizonDecision := s.makerHorizonModel.Update(now, s.MarketMaker, effectiveVolatilityBps)
	s.makerHorizonDecision = horizonDecision
	quoteConfig := s.MarketMaker
	// Prefer quote-distance crossing statistics. During a healthy warm model,
	// use its observed directional event rates as a conservative fallback so a
	// new symbol does not wait indefinitely for a completed horizon optimizer
	// decision. The fallback is deliberately disabled for degraded data.
	buyFillRate, sellFillRate := horizonDecision.DownCrossesPerHour, horizonDecision.UpCrossesPerHour
	sideDistanceSource := "horizon"
	if horizonDecision.UpCrosses <= 0 || horizonDecision.DownCrosses <= 0 || horizonDecision.UpCrosses+horizonDecision.DownCrosses < quoteConfig.HorizonMinSamples {
		buyFillRate, sellFillRate = 0, 0
		sideDistanceSource = "none"
		if modelSnapshot.Health == HealthHealthy && modelSnapshot.Up > 0 && modelSnapshot.Down > 0 && modelSnapshot.Observed > 0 {
			hours := modelSnapshot.Observed.Hours()
			if hours > 0 {
				buyFillRate = float64(modelSnapshot.Down) / hours
				sellFillRate = float64(modelSnapshot.Up) / hours
				sideDistanceSource = "healthy-direction-fallback"
			}
		}
	}
	// Scale both the quote risk budget and the inventory band from the current
	// quote-equivalent pair equity. Total balances are used for this sizing base
	// so locked maker orders do not make the policy jump on every refresh.
	pairEquityJPY := s.pairEquityQuote(mid)
	effectiveRiskBudgetJPY := quoteConfig.EffectiveInventoryRiskBudgetJPY(pairEquityJPY)
	quoteConfig.InventoryRiskBudgetJPY = effectiveRiskBudgetJPY
	dynamicQuoteNotional := quoteConfig.DynamicQuoteNotionalWithFillRates(
		effectiveVolatilityBps,
		time.Duration(horizonDecision.HorizonSeconds)*time.Second,
		sellFillRate, // upward crossings consume asks
		buyFillRate,  // downward crossings consume bids
	)
	if dynamicQuoteNotional <= 0 {
		// No statistically valid risk-sized ticket is available for a new
		// window. Preserve an existing quote until its window expires so the
		// market-data and fill observations can recover without queue churn.
		if s.retainMakerQuoteDuringDataGap(now, ticker) {
			return
		}
		if !s.lastMakerQuoteAt.IsZero() {
			if err := s.executor.GracefulCancel(ctx); err != nil {
				log.WithError(err).Warn("market-maker dynamic size cancellation failed")
			}
			s.lastMakerQuoteAt = time.Time{}
			s.makerTradingWindowStartedAt = time.Time{}
			s.makerTradingWindowEndsAt = time.Time{}
		}
		return
	}
	// MaxSymbolNotional is a portfolio-level risk control, not a quote-policy
	// bound. Apply it only as a derived cap when the account configuration has
	// explicitly enabled one; actual order quantities are still capped by
	// available balances and the exchange's filters below.
	if s.Risk.MaxSymbolNotional > 0 && dynamicQuoteNotional > s.Risk.MaxSymbolNotional {
		dynamicQuoteNotional = s.Risk.MaxSymbolNotional
	}
	quoteConfig.QuoteNotional = dynamicQuoteNotional
	quoteNotional := fixedpoint.NewFromFloat(dynamicQuoteNotional)
	// The risk-sized notional is a target, not a balance gate. If the account
	// has less free JPY than the target, submit the smaller balance-backed order
	// (subject to exchange filters) instead of suppressing the entire bid.
	canBuy := dynamicQuoteNotional > 0 && availableQuote.Sign() > 0
	inventoryBand := InventoryBand{Target: quoteConfig.InventoryTarget, Limit: quoteConfig.InventoryLimit}
	if quoteConfig.AutoInventoryLimit {
		inventoryBand = quoteConfig.DynamicInventoryBandWithCapital(mid, inventoryVolatility*10_000, time.Duration(horizonDecision.HorizonSeconds)*time.Second, pairEquityJPY)
		// Shrink immediately when volatility expands, but grow by at most 20%
		// per update so a single quiet tick cannot reopen inventory too quickly.
		if s.makerInventoryBand.MaxInventory > 0 && inventoryBand.MaxInventory > s.makerInventoryBand.MaxInventory {
			maxGrowth := s.makerInventoryBand.MaxInventory * 0.20
			if inventoryBand.MaxInventory-s.makerInventoryBand.MaxInventory > maxGrowth {
				inventoryBand.MaxInventory = s.makerInventoryBand.MaxInventory + maxGrowth
				inventoryBand.Target = inventoryBand.MaxInventory * inventoryBand.TargetRatio
				inventoryBand.Limit = inventoryBand.MaxInventory - inventoryBand.Target
			}
		}
		s.makerInventoryBand = inventoryBand
		quoteConfig.InventoryTarget = inventoryBand.Target
		quoteConfig.InventoryLimit = inventoryBand.Limit
	}
	// Keep one shared statistical risk-sized baseline, then de-risk only the
	// side exposed to inventory, short-term direction, imbalance, or unusually
	// high fill intensity. The unaffected side remains at the baseline, so
	// side-specific sizing cannot increase the configured inventory risk budget.
	sideAllocationInput := SideQuoteAllocationInput{
		Inventory:       base.Float64(),
		InventoryTarget: quoteConfig.InventoryTarget,
		InventoryLimit:  quoteConfig.InventoryLimit,
		DirectionSignal: direction,
		BookImbalance:   imbalance,
		// Downward crossings consume bids; upward crossings consume asks.
		BuyFillRate:  buyFillRate,
		SellFillRate: sellFillRate,
	}
	desiredSideBias := quoteConfig.SideAllocationBias(sideAllocationInput)
	if !s.makerSideAllocationReady {
		s.makerSideAllocationBias = desiredSideBias
		s.makerSideAllocationReady = true
	} else {
		alpha := quoteConfig.SideAllocationSmoothing
		s.makerSideAllocationBias += alpha * (desiredSideBias - s.makerSideAllocationBias)
	}
	sideNotionals := quoteConfig.SideQuoteNotionals(dynamicQuoteNotional, s.makerSideAllocationBias)
	buyQuoteNotional := fixedpoint.NewFromFloat(sideNotionals.Buy)
	sellQuoteNotional := fixedpoint.NewFromFloat(sideNotionals.Sell)
	s.makerBuyQuoteNotional = buyQuoteNotional
	s.makerSellQuoteNotional = sellQuoteNotional
	desiredSideDistanceBias := quoteConfig.SideQuoteDistanceBias(buyFillRate, sellFillRate)
	if !s.makerSideDistanceReady {
		s.makerSideDistanceBias = desiredSideDistanceBias
		s.makerSideDistanceReady = true
	} else {
		alpha := quoteConfig.SideAllocationSmoothing
		s.makerSideDistanceBias += alpha * (desiredSideDistanceBias - s.makerSideDistanceBias)
	}
	plan := quoteConfig.Quote(MarketMakerQuoteInput{
		MidPrice: mid, BestBid: ticker.Buy.Float64(), BestAsk: ticker.Sell.Float64(),
		// GammaCaptureVolatility is an instantaneous log-volatility in
		// fraction/sqrt(second). Quote's input is bps/sqrt(second), and it
		// derives the expected move over the selected first-passage horizon.
		VolatilityPerSqrtSec:  quoteVolatility * 10_000,
		TradingHorizonSeconds: float64(horizonDecision.HorizonSeconds),
		Inventory:             base.Float64(), DirectionSignal: direction, BookImbalance: imbalance,
		SideDistanceBias: s.makerSideDistanceBias,
		CanBuy:           canBuy, CanSell: canSell,
	})

	if now.Before(s.makerResetCooldownUntil) {
		return
	}
	if base.Sign() > 0 {
		if s.makerInventoryExposureSince.IsZero() {
			s.makerInventoryExposureSince = now
			s.makerInventoryAnchorMid = mid
		}
	} else {
		s.makerInventoryExposureSince = time.Time{}
		s.makerInventoryAnchorMid = 0
	}

	activeAskPrice := s.lastMakerAsk.Float64()
	activeAsk := false
	for _, order := range s.executor.ActiveMakerOrders().Orders() {
		if order.Side == types.SideTypeSell {
			activeAsk = true
			if order.Price.Sign() > 0 {
				activeAskPrice = order.Price.Float64()
			}
			break
		}
	}
	if activeAsk && s.makerAskSince.IsZero() && !s.lastMakerQuoteAt.IsZero() {
		// Recover the local age after a restart or an asynchronous order update;
		// the active order book is authoritative for existence, while the last
		// successful quote timestamp is the best causal age available locally.
		s.makerAskSince = s.lastMakerQuoteAt
		s.makerAskAnchorMid = s.lastMakerMid
	}
	if !activeAsk {
		s.makerAskSince = time.Time{}
		s.makerAskAnchorMid = 0
	}
	if s.MarketMaker.InventoryReset.Enabled && activeAsk && !s.makerAskSince.IsZero() && s.model != nil && modelSnapshot.Health == HealthHealthy {
		decision := s.MarketMaker.InventoryReset.Evaluate(InventoryResetInput{
			Now: now, AskSince: s.makerAskSince, AnchorMidPrice: s.makerAskAnchorMid,
			MidPrice: mid, BestBid: ticker.Buy.Float64(), AskPrice: activeAskPrice,
			MakerFeeBps: s.MarketMaker.MakerFeeBps, TakerFeeBps: s.MarketMaker.TakerFeeBps,
			MaxSlippageBps: s.MarketMaker.InventoryReset.MaxSlippageBps,
			FillIntensity:  modelSnapshot.LambdaUp, FillIntensityHaircut: s.MarketMaker.InventoryReset.FillIntensityHaircut,
			VolatilityPerSqrtSec: modelSnapshot.GammaCaptureVolatility,
			FastDirectionSignal:  direction,
		})
		if decision.Trigger {
			s.executeInventoryReset(ctx, ticker, base, decision)
			return
		}
	}
	// Do not churn maker orders on every BBO tick. Binance user-data cancel
	// updates can arrive after the local order is removed; refreshing too fast
	// then accumulates pending order updates in ActiveOrderBook.
	windowDuration := time.Duration(horizonDecision.HorizonSeconds) * time.Second
	minRefreshInterval, refreshInterval := s.MarketMaker.RefreshIntervals(plan.HalfSpreadBps, quoteVolatility*10_000)
	if windowDuration > 0 {
		minRefreshInterval, refreshInterval = BoundRefreshIntervals(minRefreshInterval, refreshInterval, windowDuration)
	}
	elapsed := now.Sub(s.lastMakerQuoteAt)
	materialMove := s.lastMakerMid > 0 && math.Abs(math.Log(mid/s.lastMakerMid))*10_000 >= s.MarketMaker.RefreshMoveBps
	materialImbalance := !s.lastMakerQuoteAt.IsZero() && math.Abs(imbalance-s.lastMakerImbalance) >= s.MarketMaker.RefreshImbalanceDelta
	adverseAskMoveBps, adverseBidMoveBps := makerAdverseBBOChangeBps(
		s.lastMakerBestBid, s.lastMakerBestAsk, ticker.Buy.Float64(), ticker.Sell.Float64())
	adverseMove := math.Max(adverseAskMoveBps, adverseBidMoveBps) >= s.MarketMaker.AdverseRepriceBps
	// A mid-price move, imbalance flip, or missing side is only actionable after
	// the minimum resting interval. This preserves queue priority against
	// micro-ticks while allowing the quote to react to a material state change.
	quoteCrossed := (!s.lastMakerBid.IsZero() && s.lastMakerBid.Compare(ticker.Sell) >= 0) ||
		(!s.lastMakerAsk.IsZero() && s.lastMakerAsk.Compare(ticker.Buy) <= 0)
	missingSide := false
	sideMismatch := false
	if !s.lastMakerQuoteAt.IsZero() && elapsed >= minRefreshInterval {
		hasBid, hasAsk := false, false
		for _, order := range s.executor.ActiveMakerOrders().Orders() {
			if order.Side == types.SideTypeBuy {
				hasBid = true
			} else if order.Side == types.SideTypeSell {
				hasAsk = true
			}
		}
		missingSide = (plan.AllowBid && !hasBid) || (plan.AllowAsk && !hasAsk)
		sideMismatch = (hasBid != plan.AllowBid) || (hasAsk != plan.AllowAsk)
	}
	if windowDuration <= 0 {
		windowDuration = refreshInterval
	}
	windowExpired := s.makerTradingWindowEndsAt.IsZero() || !now.Before(s.makerTradingWindowEndsAt)
	if !s.lastMakerQuoteAt.IsZero() {
		nearFillHold := windowExpired && !quoteCrossed && !adverseMove && !materialMove && !materialImbalance && !missingSide && !sideMismatch && plan.Reason == "quoted" && makerQuoteNearFill(
			s.lastMakerBid.Float64(), s.lastMakerAsk.Float64(), ticker.Buy.Float64(), ticker.Sell.Float64(), mid,
			plan, quoteConfig.MinimumHalfSpreadBps)
		if nearFillHold {
			if s.lastMakerDiagnosticAt.IsZero() || now.Sub(s.lastMakerDiagnosticAt) >= 10*time.Second {
				log.WithFields(logrus.Fields{
					"bid": s.lastMakerBid, "ask": s.lastMakerAsk,
					"bestBid": ticker.Buy, "bestAsk": ticker.Sell,
					"selectedHorizon": windowDuration, "refreshMin": minRefreshInterval,
				}).Info("market-maker retaining near-fill quote after window expiry")
				s.lastMakerDiagnosticAt = now
			}
			return
		}
		if !makerQuoteRefreshRequired(elapsed, minRefreshInterval, quoteCrossed, windowExpired, adverseMove, materialMove, materialImbalance, missingSide || sideMismatch) {
			return
		}
	}
	exposureAge := time.Duration(0)
	if !s.makerInventoryExposureSince.IsZero() {
		exposureAge = now.Sub(s.makerInventoryExposureSince)
	}
	if s.lastMakerDiagnosticAt.IsZero() || now.Sub(s.lastMakerDiagnosticAt) >= 10*time.Second {
		evidence := fastEvidence
		log.WithFields(logrus.Fields{
			"bid": ticker.Buy, "ask": ticker.Sell, "availableQuote": availableQuote,
			"base": base, "quoteNotional": quoteNotional, "minNotional": s.Market.MinNotional,
			"buyQuoteNotional": buyQuoteNotional, "sellQuoteNotional": sellQuoteNotional,
			"sideAllocationBias": sideNotionals.Bias, "buyQuoteAllocation": sideNotionals.BuyFactor,
			"sellQuoteAllocation": sideNotionals.SellFactor,
			"minQuantity":         s.Market.MinQuantity, "canBuy": canBuy, "canSell": canSell,
			"plan": plan.Reason, "allowBid": plan.AllowBid, "allowAsk": plan.AllowAsk,
			"modelHealth": modelSnapshot.Health, "modelUp": modelSnapshot.Up,
			"modelDown": modelSnapshot.Down, "modelEventAge": modelSnapshot.Age,
			"fastHealth": fastSnapshot.Health, "fastDirection": direction, "bookImbalance": imbalance,
			"fastEvidenceHealth": evidence.Health, "fastTradeCount": evidence.TradeCount,
			"fastBBOCount": evidence.BBOCount, "fastTradeImbalance": evidence.SignedTradeImbalance,
			"fastQueueImbalance": evidence.QueueImbalance, "fastMidReturnBps": evidence.MidReturnBps,
			"fastRealizedVolatilityBps": evidence.RealizedVolatilityBps, "fastEvidenceAge": evidence.Age,
			"slowModelVolatilityBps":          modelSnapshot.GammaCaptureVolatility * 10_000,
			"fastModelVolatilityBps":          fastSnapshot.GammaCaptureVolatility * 10_000,
			"fastQuoteVolatilityUsable":       fastQuoteVolatilityUsable,
			"fastSlowVolatilityDeltaBps":      (fastSnapshot.GammaCaptureVolatility - modelSnapshot.GammaCaptureVolatility) * 10_000,
			"roundTripMakerFeeBps":            2 * quoteConfig.MakerFeeBps,
			"quoteEdgeAfterFeesBps":           plan.BidDistanceBps + plan.AskDistanceBps - 2*quoteConfig.MakerFeeBps,
			"quoteNetEdgeBps":                 plan.BidDistanceBps + plan.AskDistanceBps - 2*quoteConfig.MakerFeeBps - 2*quoteConfig.AdverseSelectionBps - quoteConfig.MinimumNetEdgeBps,
			"quoteVolatilityBps":              quoteVolatility * 10_000,
			"volatilityFloorBps":              volatilityFloorBps,
			"inventoryVolatilityBps":          inventoryVolatility * 10_000,
			"quoteHalfSpreadBps":              plan.HalfSpreadBps,
			"bidDistanceBps":                  plan.BidDistanceBps,
			"askDistanceBps":                  plan.AskDistanceBps,
			"sideDistanceBias":                s.makerSideDistanceBias,
			"sideDistanceSource":              sideDistanceSource,
			"buyFillRatePerHour":              buyFillRate,
			"sellFillRatePerHour":             sellFillRate,
			"selectedHorizon":                 windowDuration,
			"horizonScoreBpsPerHour":          horizonDecision.ScoreBpsPerHour,
			"horizonUpPerHour":                horizonDecision.UpCrossesPerHour,
			"horizonDownPerHour":              horizonDecision.DownCrossesPerHour,
			"inventoryTarget":                 quoteConfig.InventoryTarget,
			"inventoryLimit":                  quoteConfig.InventoryLimit,
			"inventoryMax":                    inventoryBand.MaxInventory,
			"inventoryTargetRatio":            inventoryBand.TargetRatio,
			"pairEquityJPY":                   pairEquityJPY,
			"inventoryRiskBudgetEffectiveJPY": effectiveRiskBudgetJPY,
			"inventoryCapitalTargetJPY":       inventoryBand.CapitalTargetNotionalJPY,
			"inventoryCapitalCapJPY":          inventoryBand.CapitalCapNotionalJPY,
			"quoteNotionalDynamic":            dynamicQuoteNotional,
			"quoteOrderSize":                  inventoryBand.OrderSize,
			"effectiveOrderLevels":            quoteConfig.EffectiveOrderLevels(windowDuration, horizonDecision.UpCrossesPerHour, horizonDecision.DownCrossesPerHour),
			"inventoryRiskMoveBps":            inventoryBand.RiskMoveBps,
			"missingSide":                     missingSide,
			"sideMismatch":                    sideMismatch,
			"materialMove":                    materialMove, "materialImbalance": materialImbalance,
			"adverseAskMoveBps": adverseAskMoveBps, "adverseBidMoveBps": adverseBidMoveBps,
			"adverseRepriceBps":    s.MarketMaker.AdverseRepriceBps,
			"inventoryExposureAge": exposureAge,
			"refreshMin":           minRefreshInterval, "refreshMax": refreshInterval,
			"modelReferenceAge": now.Sub(s.State.LastReferenceTime),
		}).Info("market-maker quote evaluation")
		s.lastMakerDiagnosticAt = now
	}
	if plan.Reason != "quoted" {
		if err := s.executor.GracefulCancel(ctx); err != nil {
			log.WithError(err).Warn("market-maker cancel after invalid quote failed")
		}
		s.makerTradingWindowStartedAt = time.Time{}
		s.makerTradingWindowEndsAt = time.Time{}
		s.makerAskSince = time.Time{}
		s.makerAskAnchorMid = 0
		return
	}
	// Use cancel-all mode so ActiveOrderBook also clears updates that arrived
	// before the corresponding create callback was processed.
	if err := s.executor.GracefulCancel(ctx); err != nil {
		log.WithError(err).Warn("market-maker cancel existing quotes failed")
	}
	var submits []types.SubmitOrder
	askSubmitted := false
	if plan.AllowBid {
		price := s.Market.TruncatePrice(fixedpoint.NewFromFloat(plan.BidPrice))
		qty, ok := s.Market.GreaterThanMinimalOrderQuantity(types.SideTypeBuy, price, fixedpoint.Min(s.availableQuoteBalance(), buyQuoteNotional))
		if !ok {
			log.WithFields(logrus.Fields{"side": "BUY", "price": price, "availableQuote": availableQuote, "minNotional": s.Market.MinNotional, "minQuantity": s.Market.MinQuantity}).Warn("market-maker bid blocked by exchange quantity filters")
		}
		if ok && price.Compare(ticker.Sell) < 0 {
			// Binance LIMIT_MAKER orders reject an explicit timeInForce.
			submits = append(submits, types.SubmitOrder{Symbol: s.Symbol, Market: s.Market, Side: types.SideTypeBuy, Type: types.OrderTypeLimitMaker, Price: price, Quantity: qty, ClientOrderID: marketMakerClientOrderID(types.SideTypeBuy), Tag: "gammacapture-mm-bid"})
		}
	}
	if plan.AllowAsk {
		price := s.Market.TruncatePrice(fixedpoint.NewFromFloat(plan.AskPrice))
		qty, ok := s.makerAskQuantity(price, base, sellQuoteNotional)
		if !ok {
			log.WithFields(logrus.Fields{"side": "SELL", "price": price, "base": base, "minNotional": s.Market.MinNotional, "minQuantity": s.Market.MinQuantity}).Warn("market-maker ask blocked by exchange quantity filters")
		}
		if ok && price.Compare(ticker.Buy) > 0 {
			askSubmitted = true
			submits = append(submits, types.SubmitOrder{Symbol: s.Symbol, Market: s.Market, Side: types.SideTypeSell, Type: types.OrderTypeLimitMaker, Price: price, Quantity: qty, ClientOrderID: marketMakerClientOrderID(types.SideTypeSell), Tag: "gammacapture-mm-ask"})
		}
	}
	if len(submits) > 0 {
		if _, err := s.executor.SubmitOrders(ctx, submits...); err != nil {
			s.State.LastDecision = "maker quote rejected: " + err.Error()
			log.WithError(err).Error("market-maker quote submission failed")
		} else {
			log.WithField("orders", len(submits)).Info("market-maker quotes submitted")
			s.lastMakerQuoteAt = now
			s.makerTradingWindowStartedAt = now
			s.makerTradingWindowEndsAt = now.Add(windowDuration)
			s.lastMakerBid = fixedpoint.NewFromFloat(plan.BidPrice)
			s.lastMakerAsk = fixedpoint.NewFromFloat(plan.AskPrice)
			s.lastMakerBestBid = ticker.Buy.Float64()
			s.lastMakerBestAsk = ticker.Sell.Float64()
			s.lastMakerMid = mid
			s.lastMakerImbalance = imbalance
			s.lastMakerDirection = direction
			if askSubmitted {
				s.makerAskSince = now
				s.makerAskAnchorMid = mid
			} else {
				s.makerAskSince = time.Time{}
				s.makerAskAnchorMid = 0
			}
		}
	}
}

func inventoryRiskVolatility(slow, fast float64, fastHealth ModelHealth) float64 {
	if fastHealth == HealthHealthy {
		return math.Max(slow, fast)
	}
	return math.Max(0, slow)
}

func quoteRiskVolatility(slow, fast float64, fastHealth, evidenceHealth ModelHealth) float64 {
	volatility := math.Max(0, slow)
	if fastHealth == HealthHealthy && evidenceHealth == HealthHealthy {
		volatility = math.Max(volatility, math.Max(0, fast))
	}
	return volatility
}

// makerQuoteWindowOpen reports whether a previously submitted quote is still
// inside its selected trading window. A zero end time deliberately means that
// no valid window exists and must not be used as a grace period.
func makerQuoteWindowOpen(now, quoteAt, windowEnd time.Time) bool {
	return !now.IsZero() && !quoteAt.IsZero() && !windowEnd.IsZero() && now.Before(windowEnd)
}

// retainMakerQuoteDuringDataGap prevents a transient model/data gap from
// cancelling a live queue position. It only retains an actually active maker
// order and only until the current window expires; a missing or expired quote
// is still cancelled and rebuilt under the latest risk statistics.
func (s *Strategy) retainMakerQuoteDuringDataGap(now time.Time, ticker types.BookTicker) bool {
	if s.executor == nil || !makerQuoteWindowOpen(now, s.lastMakerQuoteAt, s.makerTradingWindowEndsAt) {
		return false
	}
	if len(s.executor.ActiveMakerOrders().Orders()) == 0 {
		return false
	}
	// A data gap is not permission to leave a quote executable. Retain only
	// while both sides remain passive relative to the current BBO and neither
	// side has crossed the configured adverse-reprice threshold.
	if ticker.Buy.Sign() <= 0 || ticker.Sell.Sign() <= 0 ||
		s.lastMakerBid.Sign() <= 0 || s.lastMakerAsk.Sign() <= 0 ||
		s.lastMakerBid.Compare(ticker.Sell) >= 0 || s.lastMakerAsk.Compare(ticker.Buy) <= 0 {
		return false
	}
	adverseAskMoveBps, adverseBidMoveBps := makerAdverseBBOChangeBps(
		s.lastMakerBestBid, s.lastMakerBestAsk, ticker.Buy.Float64(), ticker.Sell.Float64())
	if math.Max(adverseAskMoveBps, adverseBidMoveBps) >= s.MarketMaker.AdverseRepriceBps {
		return false
	}
	log.WithFields(logrus.Fields{
		"windowEndsAt": s.makerTradingWindowEndsAt,
		"remaining":    time.Until(s.makerTradingWindowEndsAt),
	}).Debug("market-maker retaining quote during statistics gap")
	return true
}

// makerAdverseBBOChangeBps measures BBO movement since the current quote
// window was submitted. Positive ask movement means the ask BBO moved down;
// positive bid movement means the bid BBO moved up. It deliberately compares
// BBO-to-BBO rather than quote-to-BBO: a passive quote is expected to be below
// the bid or above the ask by its spread, and that static distance is not a
// market move that should cancel the order.
func makerAdverseBBOChangeBps(referenceBid, referenceAsk, currentBid, currentAsk float64) (askBps, bidBps float64) {
	if referenceAsk > 0 && currentAsk > 0 {
		askBps = math.Log(referenceAsk/currentAsk) * 10_000
	}
	if referenceBid > 0 && currentBid > 0 {
		bidBps = math.Log(currentBid/referenceBid) * 10_000
	}
	return askBps, bidBps
}

// makerQuoteRefreshRequired centralizes the quote refresh policy. The minimum
// resting interval is an anti-churn floor; once it has elapsed, any material
// market-state signal (or a safety condition) permits a re-quote.
func makerQuoteRefreshRequired(elapsed, minRefreshInterval time.Duration, quoteCrossed, windowExpired, adverseMove, materialMove, materialImbalance, missingSide bool) bool {
	if elapsed < minRefreshInterval {
		return false
	}
	return quoteCrossed || windowExpired || adverseMove || materialMove || materialImbalance || missingSide
}

// makerQuoteNearFill reports whether the existing quote is at least as close
// to the current BBO as the newly computed quote, while still retaining the
// fee/adverse-selection floor. It is used only as a window-expiry hold: a
// material move, imbalance flip, missing side, or side-policy change always
// takes precedence and can reprice the order.
func makerQuoteNearFill(lastBid, lastAsk, bestBid, bestAsk, mid float64, plan MarketMakerQuotePlan, minimumFloorBps float64) bool {
	if lastBid <= 0 || lastAsk <= 0 || bestBid <= 0 || bestAsk <= 0 || mid <= 0 || bestAsk <= bestBid {
		return false
	}
	if plan.AllowBid {
		bidDistance := math.Log(mid/lastBid) * 10_000
		if lastBid >= bestAsk || bidDistance < minimumFloorBps || bidDistance > plan.BidDistanceBps+1e-9 {
			return false
		}
	}
	if plan.AllowAsk {
		askDistance := math.Log(lastAsk/mid) * 10_000
		if lastAsk <= bestBid || askDistance < minimumFloorBps || askDistance > plan.AskDistanceBps+1e-9 {
			return false
		}
	}
	return plan.AllowBid || plan.AllowAsk
}

// makerAskQuantity caps a passive sell to one quote notional while avoiding a
// leftover balance that is itself untradeable dust. If the capped order would
// strand dust, sell the whole eligible balance instead.
func (s *Strategy) makerAskQuantity(price, base, quoteNotional fixedpoint.Value) (fixedpoint.Value, bool) {
	if price.Sign() <= 0 || base.Sign() <= 0 {
		return fixedpoint.Zero, false
	}
	maxQuantity := fixedpoint.Min(base, quoteNotional.Div(price))
	quantity, ok := s.Market.GreaterThanMinimalOrderQuantity(types.SideTypeSell, price, maxQuantity)
	if !ok {
		return fixedpoint.Zero, false
	}
	residual := base.Sub(quantity)
	if residual.Sign() > 0 && s.Market.IsDustQuantity(residual, price) {
		if whole, wholeOK := s.Market.GreaterThanMinimalOrderQuantity(types.SideTypeSell, price, base); wholeOK {
			return whole, true
		}
	}
	return quantity, true
}

func (s *Strategy) executeInventoryReset(ctx context.Context, ticker types.BookTicker, base fixedpoint.Value, decision InventoryResetDecision) {
	cfg := s.MarketMaker.InventoryReset
	if err := s.executor.GracefulCancel(ctx); err != nil {
		log.WithError(err).Warn("inventory reset cancel failed")
		return
	}

	price := ticker.Buy.Mul(fixedpoint.NewFromFloat(1 - cfg.MaxSlippageBps/10_000))
	price = s.Market.TruncatePrice(price)
	quoteNotional := fixedpoint.NewFromFloat(cfg.ReductionNotional)
	if s.makerInventoryBand.OrderSize > 0 {
		quoteNotional = fixedpoint.NewFromFloat(s.makerInventoryBand.OrderSize * price.Float64())
	}
	if s.makerSellQuoteNotional.Sign() > 0 {
		quoteNotional = s.makerSellQuoteNotional
	}
	quantity, ok := s.makerAskQuantity(price, base, quoteNotional)
	if !ok {
		log.WithFields(logrus.Fields{"base": base, "price": price, "reductionNotional": quoteNotional, "minNotional": s.Market.MinNotional, "minQuantity": s.Market.MinQuantity}).Warn("inventory reset blocked by exchange quantity filters")
		return
	}

	_, err := s.executor.SubmitOrders(ctx, types.SubmitOrder{
		Symbol: s.Symbol, Market: s.Market, Side: types.SideTypeSell,
		Type: types.OrderTypeLimit, TimeInForce: types.TimeInForceIOC,
		Price: price, Quantity: quantity, Tag: "gammacapture-inventory-reset",
	})
	if err != nil {
		log.WithError(err).Error("inventory reset IOC sell failed")
		return
	}

	now := time.Now()
	s.makerResetCooldownUntil = now.Add(time.Duration(cfg.Cooldown))
	s.makerAskSince = time.Time{}
	s.makerAskAnchorMid = 0
	s.makerInventoryExposureSince = time.Time{}
	s.makerInventoryAnchorMid = 0
	s.lastMakerQuoteAt = now
	s.makerTradingWindowStartedAt = time.Time{}
	s.makerTradingWindowEndsAt = time.Time{}
	log.WithFields(logrus.Fields{
		"quantity": quantity, "price": price, "age": decision.Age,
		"adverseMoveBps": decision.AdverseMoveBps, "fillProbability": decision.FillProbability,
		"waitValueBps": decision.WaitValueBps, "iocValueBps": decision.IOCValueBps,
		"riskBps": decision.RiskBps, "cooldown": cfg.Cooldown,
	}).Warn("inventory reset IOC sell submitted")
}

func (s *Strategy) entryBookPasses() bool {
	// The deterministic backtester has no historical BBO stream. Its candle
	// safeguards are evaluated separately; only a market-trade paper stream can
	// make an executable spread decision.
	if !s.usesRealtimeReference() {
		return true
	}
	s.bookMu.RLock()
	bid, ask, updated := s.bestBid, s.bestAsk, s.bestBookAt
	s.bookMu.RUnlock()
	if bid.Sign() <= 0 || ask.Sign() <= 0 || ask.Compare(bid) <= 0 || updated.IsZero() || time.Since(updated) > time.Duration(s.Risk.MaxBookAge) {
		return false
	}
	spreadBps := math.Log(ask.Float64()/bid.Float64()) * 10_000
	return spreadBps >= 0 && spreadBps <= s.Risk.MaxSpreadBps
}

func (s *Strategy) observeTrend(now time.Time, price fixedpoint.Value) {
	if s.Trend.Lookback <= 0 || price.Sign() <= 0 {
		return
	}
	s.State.TrendSamples = append(s.State.TrendSamples, TrendSample{Time: now, Price: price})
	// Keep a little more than one lookback so the latest sample at or before
	// the target time remains available after each trim.
	cut := now.Add(-2 * time.Duration(s.Trend.Lookback))
	first := 0
	for first < len(s.State.TrendSamples)-1 && s.State.TrendSamples[first+1].Time.Before(cut) {
		first++
	}
	if first > 0 {
		s.State.TrendSamples = append([]TrendSample(nil), s.State.TrendSamples[first:]...)
	}
}

func (s *Strategy) trendPasses(now time.Time, price fixedpoint.Value) bool {
	if s.Trend.Lookback <= 0 {
		return true
	}
	target := now.Add(-time.Duration(s.Trend.Lookback))
	var reference fixedpoint.Value
	for _, sample := range s.State.TrendSamples {
		if !sample.Time.After(target) && sample.Price.Sign() > 0 {
			reference = sample.Price
		} else if sample.Time.After(target) {
			break
		}
	}
	if reference.Sign() <= 0 || price.Sign() <= 0 {
		return false
	}
	return math.Log(price.Float64()/reference.Float64())*10_000 >= s.Trend.MinReturnBps
}

// warmModelFromSession uses BBGO's existing per-symbol historical-kline preload.
// The session fetches it from the configured backtest service (CSV/database) or
// from the exchange in a running session, so the strategy never starts cold.
func (s *Strategy) warmModelFromSession(session *bbgo.ExchangeSession) {
	store, ok := session.MarketDataStore(s.Symbol)
	if !ok {
		log.WithField("symbol", s.Symbol).Warn("market-data store unavailable; model starts cold")
		return
	}
	klines, ok := store.KLinesOfInterval(types.Interval(s.Interval))
	if !ok || len(*klines) == 0 {
		log.WithField("symbol", s.Symbol).Warn("no preloaded klines; model starts cold")
		return
	}
	history := append(types.KLineWindow(nil), (*klines)...)
	sort.Slice(history, func(i, j int) bool {
		return history[i].EndTime.Time().Before(history[j].EndTime.Time())
	})
	for _, k := range history {
		s.updateModel(k)
	}
	log.WithFields(logrus.Fields{
		"symbol":          s.Symbol,
		"interval":        s.Interval,
		"preloadedKLines": len(history),
		"modelHealth":     s.model.Snapshot(history[len(history)-1].EndTime.Time()).Health,
	}).Info("warmed gamma-capture model from session history")
}

func (s *Strategy) updateModel(k types.KLine) (time.Time, fixedpoint.Value, ModelSnapshot, FirstPassage, SignalCrossing, SignalCrossing) {
	now := k.EndTime.Time()
	price := k.GetClose()
	return s.updateModelAt(now, price)
}

func (s *Strategy) updateModelAt(now time.Time, price fixedpoint.Value) (time.Time, fixedpoint.Value, ModelSnapshot, FirstPassage, SignalCrossing, SignalCrossing) {
	events := s.State.Engine.Update(s.Symbol, price, now, now, 0)
	for _, e := range events {
		s.model.Update(e)
	}
	snapshot := s.model.Snapshot(now)
	passage := TPBeforeSL(snapshot.LambdaUp, snapshot.LambdaDown, time.Duration(s.Horizon.Prediction), s.TakeProfit.InitialTargetBarriers, s.StopLoss.HardStopBarriers, 0)
	rawSignal, signal := s.updateHealthScopedSignal(snapshot.Health, passage.TP)
	return now, price, snapshot, passage, rawSignal, signal
}

// updateHealthScopedSignal makes a completed confidence upcrossing meaningful
// only inside one continuous HEALTHY epoch.  The raw state is retained solely
// for diagnostics: otherwise a warm-up/degraded upcrossing can disarm the
// trade signal before the model is permitted to enter.
func (s *Strategy) updateHealthScopedSignal(health ModelHealth, probability float64) (SignalCrossing, SignalCrossing) {
	raw := s.State.RawSignals.Update(probability, s.Signal.LowerThreshold, s.Signal.UpperThreshold)
	if health != HealthHealthy {
		if s.State.SignalHealthActive {
			s.State.Signals.ResetHysteresis()
			s.State.SignalHealthActive = false
		}
		return raw, SignalNone
	}
	if !s.State.SignalHealthActive {
		s.State.Signals.ResetHysteresis()
		s.State.SignalHealthActive = true
	}
	return raw, s.State.Signals.Update(probability, s.Signal.LowerThreshold, s.Signal.UpperThreshold)
}

func (s *Strategy) managePosition(ctx context.Context, now time.Time, price fixedpoint.Value, p FirstPassage, signal SignalCrossing) {
	reason := s.positionExitReason(now, price, p, signal)
	if reason == "" {
		s.State.Runtime = StateLong
		return
	}
	s.State.PendingExitReason = reason
	if err := s.ClosePosition(ctx, fixedpoint.One); err != nil {
		s.State.PendingExitReason = ""
		s.State.LastDecision = "exit failed: " + err.Error()
		return
	}
	s.State.LastDecision = reason
	s.State.Runtime = StateCooldown
	s.State.CooldownUntil = now.Add(time.Duration(s.Risk.CooldownAfterStop))
}

func (s *Strategy) positionExitReason(now time.Time, price fixedpoint.Value, p FirstPassage, signal SignalCrossing) string {
	if price.Sign() <= 0 {
		return ""
	}
	if s.State.EntryPrice.Sign() <= 0 {
		s.State.EntryPrice = s.Position.GetAverageCost()
		if s.State.EntryPrice.Sign() <= 0 {
			s.State.EntryPrice = price
		}
	}
	if s.State.HighWaterPrice.Sign() <= 0 || price.Compare(s.State.HighWaterPrice) > 0 {
		s.State.HighWaterPrice = price
	}
	targetBarriers := s.TakeProfit.InitialTargetBarriers
	hardStopBarriers := s.StopLoss.HardStopBarriers
	softStopBarriers := s.StopLoss.SoftStopBarriers
	if s.State.EntryTargetBarriers > 0 {
		targetBarriers = s.State.EntryTargetBarriers
	}
	if s.State.EntryHardStopBarriers > 0 {
		hardStopBarriers = s.State.EntryHardStopBarriers
	}
	if s.State.EntrySoftStopBarriers > 0 {
		softStopBarriers = s.State.EntrySoftStopBarriers
	}
	entryMove := math.Log(price.Float64()/s.State.EntryPrice.Float64()) / s.Barrier.Width
	holding := now.Sub(s.State.EnteredAt)
	if entryMove <= -float64(hardStopBarriers) {
		return "hard stop"
	}
	if holding >= time.Duration(s.Horizon.MaximumHolding) {
		return "time stop"
	}
	if holding < time.Duration(s.Risk.SoftExitMinHolding) {
		return ""
	}
	modelWeak := p.TP <= s.Signal.ExitProbability || signal == SignalDown || s.State.Signals.Churn() >= s.Signal.MaxChurn
	if modelWeak && entryMove <= -float64(softStopBarriers) {
		return "model-confirmed loss cut"
	}
	minimumProfitBarriers := s.minimumTakeProfitBarriers()
	if minimumProfitBarriers > int64(targetBarriers) {
		minimumProfitBarriers = int64(targetBarriers)
	}
	minimumProfit := float64(minimumProfitBarriers)
	if entryMove >= minimumProfit {
		drawdown := math.Log(price.Float64()/s.State.HighWaterPrice.Float64()) / s.Barrier.Width
		if drawdown <= -float64(s.TakeProfit.MinTrailingBarriers) {
			return "trailing take profit"
		}
		if modelWeak {
			return "protected soft exit"
		}
	}
	return ""
}

// expectedNetEdgeBps values only resolved first-passage outcomes. It gives no
// positive value to paths that remain unresolved at the prediction horizon,
// because the later time/probability exit is not observable at entry.
func (s *Strategy) expectedNetEdgeBps(p FirstPassage) float64 {
	return s.expectedNetEdgeBpsFor(p, s.TakeProfit.InitialTargetBarriers, s.StopLoss.HardStopBarriers)
}

func (s *Strategy) expectedNetEdgeBpsFor(p FirstPassage, target, stop int) float64 {
	bpsPerBarrier := s.Barrier.Width * 10_000
	if bpsPerBarrier <= 0 {
		return -s.Risk.EstimatedCostBps
	}
	return p.TP*float64(target)*bpsPerBarrier -
		p.SL*float64(stop)*bpsPerBarrier -
		s.Risk.EstimatedCostBps
}

func (s *Strategy) passageFor(snapshot ModelSnapshot, target, stop int) FirstPassage {
	return TPBeforeSL(snapshot.LambdaUp, snapshot.LambdaDown, time.Duration(s.Horizon.Prediction), target, stop, 0)
}

func (s *Strategy) defaultBarrierPlan(p FirstPassage) BarrierPlan {
	return BarrierPlan{
		Target:   s.TakeProfit.InitialTargetBarriers,
		HardStop: s.StopLoss.HardStopBarriers,
		SoftStop: s.StopLoss.SoftStopBarriers,
		Passage:  p,
		EdgeBps:  s.expectedNetEdgeBps(p),
	}
}

// selectBarrierPlan chooses a single bounded TP/SL pair at entry. It is kept
// deterministic and causal: all candidates use the current intensity snapshot
// and the configured finite horizon. The selected values are later persisted in
// State, so exits do not move just because the model changes after entry.
func (s *Strategy) selectBarrierPlan(snapshot ModelSnapshot, baseline FirstPassage) BarrierPlan {
	if !s.BarrierSelection.Enabled {
		return s.defaultBarrierPlan(baseline)
	}
	b := s.BarrierSelection
	best := BarrierPlan{EdgeBps: math.Inf(-1)}
	for target := b.MinTargetBarriers; target <= b.MaxTargetBarriers; target++ {
		for stop := b.MinStopBarriers; stop <= b.MaxStopBarriers; stop++ {
			if stop <= 1 {
				continue
			}
			passage := s.passageFor(snapshot, target, stop)
			if !s.rangePasses(target) {
				continue
			}
			if passage.TP < s.Signal.EntryProbability {
				continue
			}
			edge := s.expectedNetEdgeBpsFor(passage, target, stop)
			if edge > best.EdgeBps || (edge == best.EdgeBps && target > best.Target) {
				soft := s.StopLoss.SoftStopBarriers
				if soft >= stop {
					soft = stop - 1
				}
				best = BarrierPlan{Target: target, HardStop: stop, SoftStop: soft, Passage: passage, EdgeBps: edge}
			}
		}
	}
	return best
}

func (s *Strategy) targetRangeBps(target int) float64 {
	return float64(target) * s.Barrier.Width * 10_000
}

func (s *Strategy) requiredRangeBps() float64 {
	required := s.Risk.EstimatedCostBps + s.Risk.MinimumNetEdgeBps
	if s.Risk.MinimumRangeBps > required {
		required = s.Risk.MinimumRangeBps
	}
	return required
}

func (s *Strategy) rangePasses(target int) bool {
	return target > 0 && s.targetRangeBps(target) >= s.requiredRangeBps()
}

func (s *Strategy) recordFeedback(reason string, exitPrice fixedpoint.Value) {
	if s.State == nil || s.State.EntryPrice.Sign() <= 0 || exitPrice.Sign() <= 0 {
		return
	}
	moveBps := math.Log(exitPrice.Float64()/s.State.EntryPrice.Float64()) * 10_000
	s.recordFeedbackNet(reason, moveBps-s.Risk.EstimatedCostBps)
}

func (s *Strategy) recordExecutionFeedback(profit *types.Profit) {
	if profit == nil || s.State == nil {
		return
	}
	notional := profit.QuoteQuantity
	if notional.Sign() <= 0 && profit.AverageCost.Sign() > 0 && profit.Quantity.Sign() > 0 {
		notional = profit.AverageCost.Mul(profit.Quantity)
	}
	if notional.Sign() <= 0 {
		return
	}
	netBps := profit.NetProfit.Div(notional).Float64() * 10_000
	reason := s.State.PendingExitReason
	if reason == "" {
		reason = "executor profit"
	}
	s.recordFeedbackNet(reason, netBps)
	s.State.PendingExitReason = ""
}

func (s *Strategy) recordFeedbackNet(reason string, netBps float64) {
	s.State.Feedback.ResolvedExits++
	s.State.Feedback.PredictedTPSum += s.State.EntryPredictedTP
	s.State.Feedback.RealizedNetBpsSum += netBps
	if netBps > 0 {
		s.State.Feedback.PositiveNetExits++
	} else {
		s.State.Feedback.NegativeNetExits++
	}
	s.State.Feedback.LastOutcome = reason
}

// entryBarIsStable blocks an entry at the close of a sharp one-minute impulse.
// This is specifically important in candle replay: the bar does not reveal a
// tradable intrabar path or the BBO available at the signal time.
func (s *Strategy) entryBarIsStable(k types.KLine) bool {
	if k.Open.Sign() <= 0 || k.Close.Sign() <= 0 || k.Low.Sign() <= 0 || k.High.Sign() <= 0 {
		return false
	}
	rangeBps := math.Log(k.High.Float64()/k.Low.Float64()) * 10_000
	returnBps := math.Abs(math.Log(k.Close.Float64()/k.Open.Float64())) * 10_000
	return rangeBps <= s.Risk.MaxEntryBarRangeBps && returnBps <= s.Risk.MaxEntryBarReturnBps
}

func (s *Strategy) entryRetraceConfirmed(price fixedpoint.Value) bool {
	if !s.State.EntryNeedsRetrace {
		return true
	}
	if price.Sign() <= 0 || s.State.EntrySignalPrice.Sign() <= 0 {
		return false
	}
	return math.Log(s.State.EntrySignalPrice.Float64()/price.Float64())*10_000 >= s.Signal.StretchedSignalRetraceBps
}

func (s *Strategy) clearEntryEligibility() {
	s.State.EntryEligibleUntil = time.Time{}
	s.State.EntrySignalPrice = fixedpoint.Zero
	s.State.EntryNeedsRetrace = false
}

// minimumTakeProfitBarriers is the smallest favorable grid move that covers
// the configured round-trip cost budget and required residual edge. It makes a
// trailing exit unable to lock in a knowingly fee-negative move.
func (s *Strategy) minimumTakeProfitBarriers() int64 {
	bpsPerBarrier := s.Barrier.Width * 10_000
	if bpsPerBarrier <= 0 {
		return int64(s.TakeProfit.ActivationBarriers)
	}
	feeAware := int64(math.Ceil((s.Risk.EstimatedCostBps + s.Risk.MinimumNetEdgeBps) / bpsPerBarrier))
	if feeAware < int64(s.TakeProfit.ActivationBarriers) {
		return int64(s.TakeProfit.ActivationBarriers)
	}
	return feeAware
}

func (s *Strategy) entryQuantity(price fixedpoint.Value, hardStopBarriers int) fixedpoint.Value {
	quote := s.availableQuoteBalance()
	if quote.Sign() <= 0 {
		return fixedpoint.Zero
	}
	if s.Risk.MaxSymbolNotional > 0 {
		quote = fixedpoint.Min(quote, fixedpoint.NewFromFloat(s.Risk.MaxSymbolNotional))
	}
	if s.Risk.MaxRiskPerTrade > 0 {
		// Risk sizing reserves both the hard grid loss and round-trip execution
		// cost, so the configured cash risk is not understated by fees.
		if hardStopBarriers <= 0 {
			hardStopBarriers = s.StopLoss.HardStopBarriers
		}
		hardLoss := float64(hardStopBarriers)*s.Barrier.Width + s.Risk.EstimatedCostBps/10_000
		if hardLoss > 0 {
			quote = fixedpoint.Min(quote, fixedpoint.NewFromFloat(s.Risk.MaxRiskPerTrade/hardLoss))
		}
	}
	quantity := s.Market.TruncateQuantity(quote.Div(price))
	if s.Market.IsDustQuantity(quantity, price) {
		return fixedpoint.Zero
	}
	return quantity
}

func (s *Strategy) availableQuoteBalance() fixedpoint.Value {
	if s.session == nil || s.session.GetAccount() == nil {
		return fixedpoint.Zero
	}
	balance, ok := s.session.GetAccount().Balance(s.Market.QuoteCurrency)
	if !ok {
		return fixedpoint.Zero
	}
	return balance.Available
}

func (s *Strategy) availableBaseBalance() fixedpoint.Value {
	if s.session == nil || s.session.GetAccount() == nil {
		return fixedpoint.Zero
	}
	balance, ok := s.session.GetAccount().Balance(s.Market.BaseCurrency)
	if !ok {
		return fixedpoint.Zero
	}
	return balance.Available
}

// pairEquityQuote returns the symbol's total quote-equivalent capital. It is
// deliberately based on total (available + locked) balances for sizing, while
// available balances continue to gate whether a new bid or ask may be sent.
func (s *Strategy) pairEquityQuote(midPrice float64) float64 {
	if s.session == nil || s.session.GetAccount() == nil || midPrice <= 0 {
		return 0
	}
	account := s.session.GetAccount()
	equity := 0.0
	if quote, ok := account.Balance(s.Market.QuoteCurrency); ok {
		equity += quote.Total().Float64()
	}
	if base, ok := account.Balance(s.Market.BaseCurrency); ok {
		equity += base.Total().Float64() * midPrice
	}
	return equity
}

func (s *Strategy) lastPrice() fixedpoint.Value {
	if p, ok := s.session.LastPrice(s.Symbol); ok {
		return p
	}
	return fixedpoint.Zero
}
