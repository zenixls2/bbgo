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
	EntryEligibleUntil            time.Time           `json:"entryEligibleUntil"`
	EntrySignalPrice              fixedpoint.Value    `json:"entrySignalPrice"`
	EntryNeedsRetrace             bool                `json:"entryNeedsRetrace"`
	CooldownUntil                 time.Time           `json:"cooldownUntil"`
	MakerResetCooldownUntil       time.Time           `json:"makerResetCooldownUntil,omitempty"`
	MakerAcquisitionCooldownUntil time.Time           `json:"makerAcquisitionCooldownUntil,omitempty"`
	LastDecision                  string              `json:"lastDecision"`
	Runtime                       RuntimeState        `json:"runtime"`
	TrendSamples                  []TrendSample       `json:"trendSamples,omitempty"`
	LastReferenceTime             time.Time           `json:"lastReferenceTime,omitempty"`
	LastMarketTradeID             uint64              `json:"lastMarketTradeID,omitempty"`
	OnlineArrival                 *OnlineArrivalState `json:"onlineArrival,omitempty"`
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
	fastModel                  *IntensityModel // primary/legacy alias
	fastModels                 map[time.Duration]*IntensityModel
	fastEvidence               *FastEvidenceModel // primary/legacy alias
	fastEvidenceModels         map[time.Duration]*FastEvidenceModel
	makerDirectionModel        *DecayedDirectionModel // primary/legacy alias
	makerDirectionModels       map[time.Duration]*DecayedDirectionModel
	referenceMu                sync.Mutex
	bookMu                     sync.RWMutex
	bestBid                    fixedpoint.Value
	bestAsk                    fixedpoint.Value
	bestBookAt                 time.Time
	lastBookTicker             types.BookTicker
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
	makerOnlineArrivalLastSync         time.Time
	makerHorizonTouchModel             *HorizonTouchArtifact
	makerInventoryBand                 InventoryBand
	makerBuyQuoteNotional              fixedpoint.Value
	makerSellQuoteNotional             fixedpoint.Value
	makerTradingWindowStartedAt        time.Time
	makerTradingWindowEndsAt           time.Time
	makerAskSince                      time.Time // age of the currently active passive ask
	makerAcquisitionDeficitSince       time.Time // continuous time below the dynamic inventory target
	makerAcquisitionDeficitAnchorMid   float64   // mid when the continuous inventory deficit began
	makerAcquisitionCooldownUntil      time.Time
	makerAskAnchorMid                  float64   // mid when the currently active ask was submitted
	makerInventoryExposureSince        time.Time // age of continuous non-zero base inventory
	makerInventoryAnchorMid            float64
	makerResetCooldownUntil            time.Time
	makerFillRefreshPending            bool
	makerFillRefreshScheduled          bool
	makerFillRefreshSide               types.SideType
	makerFillRefreshAt                 time.Time
	makerFillRefreshGeneration         uint64
	// makerHeadroomCancelAt prevents a delayed ActiveOrderBook cancel update
	// from turning an inventory-headroom correction into a cancel/submit loop.
	// A headroom correction cancels first and waits for the order book to
	// reflect the cancellation before a replacement quote is submitted.
	makerHeadroomCancelAt          time.Time
	makerLastAcquisitionStartLogAt time.Time
	makerLastNoSubmissionLogAt     time.Time
	makerReplacementRetryAfter     time.Time
	makerLastGateLogAt             time.Time
	makerLastBookLogAt             time.Time
	makerLastPipelineLogAt         time.Time
	makerBookEvents                uint64
	// Expected cancellations are tracked so an intentional cancel/requote does
	// not recursively trigger the external-cancel replenisher.
	makerExpectedCancelMu       sync.Mutex
	makerExpectedCancelIDs      map[uint64]string
	makerCancelRefreshMu        sync.Mutex
	makerCancelRefreshScheduled bool
	makerEarlyBumpState         EarlyBumpState
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
		if s.MarketMaker.OnlineArrival.Enabled {
			if s.State.OnlineArrival == nil {
				s.State.OnlineArrival = NewOnlineArrivalState()
			}
			s.makerHorizonModel.bindOnlineArrival(s.State.OnlineArrival)
		} else {
			s.makerHorizonModel.bindOnlineArrival(nil)
		}
		s.makerResetCooldownUntil = s.State.MakerResetCooldownUntil
		s.makerAcquisitionCooldownUntil = s.State.MakerAcquisitionCooldownUntil
		s.initializeAdaptiveFastModels()
		if s.MarketMaker.HorizonTouchModel.Enabled {
			artifact, err := LoadHorizonTouchArtifact(
				s.MarketMaker.HorizonTouchModel.Path,
				s.Symbol,
				s.MarketMaker.HorizonTouchModel.MinimumBrierImprovementPct,
			)
			if err != nil {
				return fmt.Errorf("load market-maker horizon touch model: %w", err)
			}
			s.makerHorizonTouchModel = artifact
			log.WithFields(logrus.Fields{
				"symbol":                 artifact.Symbol,
				"path":                   s.MarketMaker.HorizonTouchModel.Path,
				"dbBrierImprovementPct":  artifact.DBHoldout.BrierImprovementPct,
				"bboBrierImprovementPct": artifact.BBOHoldout.BrierImprovementPct,
				"historicalWeight":       s.MarketMaker.HorizonTouchModel.HistoricalWeight,
			}).Info("loaded accepted market-maker horizon touch model")
		} else {
			s.makerHorizonTouchModel = nil
		}
	} else {
		s.fastModel = nil
		s.fastModels = nil
		s.makerDirectionModel = nil
		s.makerDirectionModels = nil
		s.fastEvidence = nil
		s.fastEvidenceModels = nil
		s.makerHorizonTouchModel = nil
	}
	if s.GateStats == nil {
		s.GateStats = &GateStats{}
	}
	// A backtest service has already loaded the full requested date range into
	// MarketDataStore before strategies are started.  Warming from that store
	// here would seed the model and hysteresis state with future candles.  Let
	// deterministic replays warm one closed candle at a time instead.
	if s.Environment != "backtest" && s.Environment != "replay" {
		now := time.Now()
		if s.MarketMaker.Enabled && s.MarketMaker.OnlineArrival.Enabled {
			if session.Exchange == nil || session.Exchange.Name() != types.ExchangeBinance {
				return fmt.Errorf("marketMaker.onlineArrival startup replay supports Binance only")
			}
			if err := s.warmOnlineArrivalFromBinanceBBO(now); err != nil {
				if s.MarketMaker.OnlineArrival.RequireStartupHistory {
					return fmt.Errorf("required Binance BBO startup replay: %w", err)
				}
				log.WithError(err).WithField("symbol", s.Symbol).Warn("Binance BBO startup replay unavailable; continuing with persisted/online state")
			}
			if s.fastEvidence != nil {
				if err := s.warmFastEvidenceFromCapture(now); err != nil {
					log.WithError(err).WithField("symbol", s.Symbol).Warn("fast evidence capture replay unavailable")
				}
			}
		} else if s.AggTradeWarmup.Enabled {
			if err := s.warmModelFromAggTrades(now); err != nil {
				return err
			}
			if s.fastEvidence != nil {
				if err := s.warmFastEvidenceFromCapture(now); err != nil {
					log.WithError(err).WithField("symbol", s.Symbol).Warn("fast evidence archive warmup unavailable")
				}
			}
		} else if newState {
			s.warmModelFromSession(session)
		}
	}
	s.executor = bbgo.NewGeneralOrderExecutor(session, s.Symbol, ID, s.InstanceID(), s.Position)
	if s.EnvironmentRef != nil {
		s.executor.BindEnvironment(s.EnvironmentRef)
	}
	s.executor.TradeCollector().OnPositionUpdate(func(_ *types.Position) {
		bbgo.Sync(ctx, s)
	})
	if s.MarketMaker.Enabled {
		// A trade callback is emitted for every execution, including partial
		// fills, after TradeCollector has applied it to Position. Order-filled
		// callbacks only cover the terminal fill and are therefore insufficient
		// for balance-aware two-sided risk management.
		s.executor.TradeCollector().OnTrade(func(trade types.Trade, _, _ fixedpoint.Value) {
			order, ok := s.executor.OrderStore().Get(trade.OrderID)
			if !isOwnedMarketMakerTrade(trade, order, ok) {
				return
			}
			go s.onMakerTradeFilled(ctx, trade)
		})
	}
	s.executor.OnProfit(func(_ types.Trade, profit *types.Profit) {
		s.recordExecutionFeedback(profit)
	})
	s.executor.Bind()
	if s.MarketMaker.Enabled {
		s.executor.ActiveMakerOrders().OnCanceled(func(order types.Order) {
			go s.onMakerOrderCanceled(ctx, order)
		})
	}
	if s.MarketMaker.Enabled && s.MarketMaker.StartupCancelStaleOrders {
		if err := s.reconcileMarketMakerOrders(ctx); err != nil {
			return err
		}
	}
	s.Status = types.StrategyStatusRunning
	if s.MarketMaker.Enabled && s.Environment != "backtest" && s.Environment != "replay" {
		go s.runMarketMakerAccountSync(ctx)
	}
	s.OnSuspend(func() { s.State.Runtime = StateSuspended; bbgo.Sync(ctx, s) })
	s.OnResume(func() { s.State.Runtime = StateWarmingUp; bbgo.Sync(ctx, s) })
	s.OnEmergencyStop(func() {
		s.State.Runtime = StateHalted
		_ = s.gracefulCancelMaker(ctx, "emergency-stop")
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

func isOwnedMarketMakerTrade(trade types.Trade, order types.Order, found bool) bool {
	return found && trade.OrderID != 0 && trade.OrderID == order.OrderID &&
		trade.Symbol == order.Symbol && isOwnedMarketMakerOrder(order)
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

	// Cancel completion and REST open-order verification do not update the
	// session's balance snapshot. Refresh it synchronously before market-data
	// callbacks are registered; otherwise the first BBO can see base still
	// locked by the cancelled ask and submit an erroneous acquisition bid.
	if _, err := s.session.UpdateAccount(reconcileCtx); err != nil {
		return fmt.Errorf("market-maker startup account refresh after cancellation failed: %w", err)
	}

	log.WithFields(logrus.Fields{
		"symbol":    s.Symbol,
		"cancelled": len(stale),
	}).Info("market-maker startup stale order reconciliation and account refresh complete")
	return nil
}

const makerAccountSyncTimeout = 15 * time.Second

func (s *Strategy) runMarketMakerAccountSync(ctx context.Context) {
	interval := time.Duration(s.MarketMaker.AccountSyncInterval)
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.syncMarketMakerAccount(ctx, interval)
		}
	}
}

func (s *Strategy) syncMarketMakerAccount(ctx context.Context, interval time.Duration) {
	if !s.refreshMarketMakerAccount(ctx, interval, "periodic") {
		return
	}
	if ticker, ok := s.latestMakerBook(time.Duration(s.Risk.MaxBookAge)); ok {
		s.onMarketMakerBookWithEvidence(ctx, ticker, false, 0)
	}
}

func (s *Strategy) refreshMarketMakerAccount(ctx context.Context, interval time.Duration, reason string) bool {
	if s.session == nil || s.session.GetAccount() == nil {
		return false
	}
	timeout := makerAccountSyncTimeout
	if interval > 0 && interval/2 < timeout {
		timeout = interval / 2
	}
	if timeout < time.Second {
		timeout = time.Second
	}
	syncCtx, cancel := context.WithTimeout(ctx, timeout)
	account, err := s.session.UpdateAccount(syncCtx)
	cancel()
	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{"symbol": s.Symbol, "reason": reason}).Warn("market-maker account sync failed")
		return false
	}
	baseBalance, baseOK := account.Balance(s.Market.BaseCurrency)
	quoteBalance, quoteOK := account.Balance(s.Market.QuoteCurrency)
	if !baseOK || !quoteOK {
		log.WithFields(logrus.Fields{
			"symbol": s.Symbol, "reason": reason,
			"baseCurrency": s.Market.BaseCurrency, "quoteCurrency": s.Market.QuoteCurrency,
		}).Warn("market-maker account sync missing symbol balance")
		return false
	}
	positionDelta, positionChanged, err := reconcileMakerPositionBase(s.Position, baseBalance.Total(), fixedpoint.NewFromFloat(1e-12))
	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{"symbol": s.Symbol, "reason": reason}).Warn("market-maker position sync failed")
		return false
	}
	log.WithFields(logrus.Fields{
		"symbol": s.Symbol, "reason": reason,
		"availableJPY": quoteBalance.Available, "lockedJPY": quoteBalance.Locked, "totalJPY": quoteBalance.Total(),
		"availableBase": baseBalance.Available, "lockedBase": baseBalance.Locked, "totalBase": baseBalance.Total(),
		"positionDeltaBase": positionDelta, "positionChanged": positionChanged,
	}).Info("market-maker account sync complete")
	if positionChanged {
		bbgo.Sync(ctx, s)
	}
	return true
}

func reconcileMakerPositionBase(position *types.Position, accountBase, tolerance fixedpoint.Value) (fixedpoint.Value, bool, error) {
	if position == nil {
		return fixedpoint.Zero, false, fmt.Errorf("position is nil")
	}
	if tolerance.Sign() < 0 {
		tolerance = fixedpoint.Zero
	}
	currentBase := position.GetBase()
	delta := accountBase.Sub(currentBase)
	if delta.Abs().Compare(tolerance) <= 0 {
		return delta, false, nil
	}
	if err := position.ModifyBase(accountBase); err != nil {
		return delta, false, err
	}
	return delta, true, nil
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
	now := time.Now()
	s.bookMu.Lock()
	s.bestBid = ticker.Buy
	s.bestAsk = ticker.Sell
	s.bestBookAt = now
	s.lastBookTicker = ticker
	s.makerBookEvents++
	bookEvents := s.makerBookEvents
	logBook := s.MarketMaker.Enabled && (s.makerLastBookLogAt.IsZero() || now.Sub(s.makerLastBookLogAt) >= time.Minute)
	if logBook {
		s.makerLastBookLogAt = now
	}
	s.bookMu.Unlock()
	if logBook {
		log.WithFields(logrus.Fields{
			"symbol": ticker.Symbol, "bid": ticker.Buy, "ask": ticker.Sell,
			"bookEvents": bookEvents,
		}).Info("market-maker BBO callback active")
	}
}

func (s *Strategy) logMakerQuoteGate(now time.Time, reason string, fields logrus.Fields) {
	if !s.makerLastGateLogAt.IsZero() && now.Sub(s.makerLastGateLogAt) < 10*time.Second {
		return
	}
	if fields == nil {
		fields = logrus.Fields{}
	}
	fields["symbol"] = s.Symbol
	fields["reason"] = reason
	log.WithFields(fields).Warn("market-maker quote gated before planning")
	s.makerLastGateLogAt = now
}

const makerFillRebalanceDelay = 500 * time.Millisecond
const makerFillRebalanceRetryInterval = time.Second
const makerCancelReplenishDelay = 500 * time.Millisecond

// gracefulCancelMaker records the current maker order IDs before an intentional
// cancel. The cancel callback can therefore distinguish a normal reprice from
// an external cancel and avoid recursively cancelling/recreating the same quote.
func (s *Strategy) gracefulCancelMaker(ctx context.Context, reason string) error {
	if s.executor == nil {
		return nil
	}
	orders := s.executor.ActiveMakerOrders().Orders()
	if reason == "" {
		reason = "unspecified"
	}
	if len(orders) > 0 {
		s.makerExpectedCancelMu.Lock()
		if s.makerExpectedCancelIDs == nil {
			s.makerExpectedCancelIDs = make(map[uint64]string)
		}
		for _, order := range orders {
			s.makerExpectedCancelIDs[order.OrderID] = reason
		}
		s.makerExpectedCancelMu.Unlock()
		orderIDs := make([]uint64, 0, len(orders))
		for _, order := range orders {
			orderIDs = append(orderIDs, order.OrderID)
		}
		log.WithFields(logrus.Fields{
			"symbol": s.Symbol, "reason": reason, "orders": len(orders), "orderIDs": orderIDs,
		}).Info("market-maker cancel requested")
	}
	startedAt := time.Now()
	err := s.executor.GracefulCancel(ctx)
	if len(orders) > 0 {
		fields := logrus.Fields{
			"symbol": s.Symbol, "reason": reason, "orders": len(orders),
			"remainingActiveOrders": s.executor.ActiveMakerOrders().NumOfOrders(),
			"elapsed":               time.Since(startedAt),
		}
		if err != nil {
			log.WithError(err).WithFields(fields).Warn("market-maker cancel completed with error")
		} else {
			log.WithFields(fields).Info("market-maker cancel complete")
		}
	}
	return err
}

// onMakerOrderCanceled immediately rebuilds a missing side after an external
// cancellation. Intentional policy cancels are ignored via the ID registry.
// The short delay lets account/order callbacks settle, while the scheduled flag
// coalesces simultaneous bid/ask cancellation updates.
func (s *Strategy) onMakerOrderCanceled(ctx context.Context, order types.Order) {
	if !isOwnedMarketMakerOrder(order) {
		return
	}
	s.makerExpectedCancelMu.Lock()
	reason, expected := s.makerExpectedCancelIDs[order.OrderID]
	if expected {
		delete(s.makerExpectedCancelIDs, order.OrderID)
	}
	s.makerExpectedCancelMu.Unlock()
	if expected {
		log.WithFields(logrus.Fields{
			"symbol": s.Symbol, "side": order.Side, "orderID": order.OrderID, "reason": reason,
		}).Info("market-maker expected cancel observed")
		return
	}
	s.makerCancelRefreshMu.Lock()
	if s.makerCancelRefreshScheduled {
		s.makerCancelRefreshMu.Unlock()
		return
	}
	s.makerCancelRefreshScheduled = true
	s.makerCancelRefreshMu.Unlock()
	defer func() {
		s.makerCancelRefreshMu.Lock()
		s.makerCancelRefreshScheduled = false
		s.makerCancelRefreshMu.Unlock()
	}()
	timer := time.NewTimer(makerCancelReplenishDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}
	ticker, ok := s.latestMakerBook(time.Duration(s.Risk.MaxBookAge))
	if !ok {
		log.WithField("symbol", s.Symbol).Warn("maker cancel refresh waiting for a fresh BBO")
		return
	}
	// A canceled order is no longer a valid quote window. Reset the local
	// window so the normal planner rebuilds the surviving side immediately.
	s.marketMakerMu.Lock()
	s.lastMakerQuoteAt = time.Time{}
	s.makerTradingWindowStartedAt = time.Time{}
	s.makerTradingWindowEndsAt = time.Time{}
	s.marketMakerMu.Unlock()
	log.WithFields(logrus.Fields{"symbol": s.Symbol, "side": order.Side, "orderID": order.OrderID}).Info("maker external cancel refresh scheduled")
	s.onMarketMakerBookWithEvidence(ctx, ticker, false, 0)
}

// onMakerTradeFilled coalesces every private maker execution, including partial
// fills. It keeps the surviving quote live while obtaining authoritative
// balances, then forces one complete two-sided plan. Only after that plan exists
// does the planner atomically cancel and replace. Account or fresh-BBO failures
// retain the old quote and retry without submitting from stale balances.
func (s *Strategy) onMakerTradeFilled(ctx context.Context, trade types.Trade) {
	now := time.Now()
	s.marketMakerMu.Lock()
	s.makerFillRefreshGeneration++
	generation := s.makerFillRefreshGeneration
	s.makerFillRefreshPending = true
	s.makerFillRefreshSide = trade.Side
	s.makerFillRefreshAt = now
	if s.makerFillRefreshScheduled {
		s.marketMakerMu.Unlock()
		log.WithFields(logrus.Fields{
			"symbol": s.Symbol, "side": trade.Side, "orderID": trade.OrderID,
			"tradeID": trade.ID, "generation": generation,
		}).Info("maker execution coalesced into pending balance rebalance")
		return
	}
	s.makerFillRefreshScheduled = true
	s.marketMakerMu.Unlock()

	log.WithFields(logrus.Fields{
		"symbol": s.Symbol, "side": trade.Side, "orderID": trade.OrderID,
		"tradeID": trade.ID, "quantity": trade.Quantity, "generation": generation,
	}).Info("maker execution scheduled balance-aware two-sided rebalance")
	s.runMakerFillRebalance(ctx)
}

func (s *Strategy) runMakerFillRebalance(ctx context.Context) {
	if !waitMakerFillRebalance(ctx, makerFillRebalanceDelay) {
		s.finishMakerFillRebalanceWorker()
		return
	}
	for {
		s.marketMakerMu.Lock()
		generation := s.makerFillRefreshGeneration
		side := s.makerFillRefreshSide
		fillAt := s.makerFillRefreshAt
		s.marketMakerMu.Unlock()

		if !s.refreshMarketMakerAccount(ctx, 0, "fill-rebalance") {
			log.WithFields(logrus.Fields{
				"symbol": s.Symbol, "side": side, "generation": generation,
			}).Warn("maker fill rebalance waiting for authoritative balances")
			if !waitMakerFillRebalance(ctx, makerFillRebalanceRetryInterval) {
				s.finishMakerFillRebalanceWorker()
				return
			}
			continue
		}
		ticker, ok := s.latestMakerBook(time.Duration(s.Risk.MaxBookAge))
		if !ok {
			log.WithFields(logrus.Fields{
				"symbol": s.Symbol, "side": side, "generation": generation,
			}).Warn("maker fill rebalance waiting for a fresh BBO")
			if !waitMakerFillRebalance(ctx, makerFillRebalanceRetryInterval) {
				s.finishMakerFillRebalanceWorker()
				return
			}
			continue
		}

		s.marketMakerMu.Lock()
		if generation != s.makerFillRefreshGeneration {
			s.marketMakerMu.Unlock()
			continue
		}
		s.marketMakerMu.Unlock()

		log.WithFields(logrus.Fields{
			"symbol": s.Symbol, "side": side, "generation": generation,
			"fillRefreshAge": time.Since(fillAt),
		}).Info("maker fill balances synchronized; calculating replacement before cancel")
		s.onMarketMakerBookWithEvidence(ctx, ticker, false, generation)

		s.marketMakerMu.Lock()
		if generation == s.makerFillRefreshGeneration {
			s.makerFillRefreshPending = false
			s.makerFillRefreshScheduled = false
			s.makerFillRefreshSide = ""
			s.makerFillRefreshAt = time.Time{}
			s.marketMakerMu.Unlock()
			return
		}
		s.marketMakerMu.Unlock()
		if !waitMakerFillRebalance(ctx, makerFillRebalanceRetryInterval) {
			s.finishMakerFillRebalanceWorker()
			return
		}
	}
}

func (s *Strategy) finishMakerFillRebalanceWorker() {
	s.marketMakerMu.Lock()
	s.makerFillRefreshScheduled = false
	s.marketMakerMu.Unlock()
}

func waitMakerFillRebalance(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (s *Strategy) retryMakerFillRebalanceLocked(generation uint64) {
	if generation > 0 && s.makerFillRefreshScheduled && generation == s.makerFillRefreshGeneration {
		s.makerFillRefreshGeneration++
		s.makerFillRefreshPending = true
	}
}

func makerFillRebalanceQuoteAllowed(scheduled bool, currentGeneration, requestedGeneration uint64) bool {
	if requestedGeneration == 0 {
		return true
	}
	return scheduled && requestedGeneration == currentGeneration
}

func (s *Strategy) latestMakerBook(maxAge time.Duration) (types.BookTicker, bool) {
	if maxAge <= 0 {
		maxAge = 5 * time.Second
	}
	s.bookMu.RLock()
	ticker, updated := s.lastBookTicker, s.bestBookAt
	s.bookMu.RUnlock()
	if updated.IsZero() || time.Since(updated) > maxAge || ticker.Buy.Sign() <= 0 || ticker.Sell.Sign() <= 0 || ticker.Sell.Compare(ticker.Buy) <= 0 {
		return types.BookTicker{}, false
	}
	return ticker, true
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
	s.model.Observe(now, false)
	s.observeFastModelExposure(now, false)
	events := s.State.Engine.Update(s.Symbol, fixedpoint.NewFromFloat(price), now, now, 0)
	for _, event := range events {
		s.model.Update(event)
		s.updateFastModels(event)
		s.updateMakerDirectionModels(event)
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

func healthyDirectionSignal(snapshot ModelSnapshot, evidenceHealth ModelHealth) float64 {
	if snapshot.Health != HealthHealthy || evidenceHealth != HealthHealthy {
		return 0
	}
	return directionSignal(snapshot)
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
	if (s.fastEvidence == nil && len(s.fastEvidenceModels) == 0) || trade.Symbol != s.Symbol {
		return
	}
	at := trade.Time.Time()
	if at.IsZero() {
		at = time.Now()
	}
	s.observeFastEvidenceTrade(at, trade)
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
	s.onMarketMakerBookWithEvidence(ctx, ticker, true, 0)
}

func (s *Strategy) onMarketMakerBookWithEvidence(ctx context.Context, ticker types.BookTicker, observeEvidence bool, fillRebalanceGeneration uint64) {
	if ticker.Symbol != s.Symbol || s.executor == nil || ticker.Buy.Sign() <= 0 || ticker.Sell.Sign() <= 0 || ticker.Sell.Compare(ticker.Buy) <= 0 {
		return
	}
	s.marketMakerMu.Lock()
	if !makerFillRebalanceQuoteAllowed(s.makerFillRefreshScheduled, s.makerFillRefreshGeneration, fillRebalanceGeneration) {
		s.marketMakerMu.Unlock()
		return
	}
	defer s.marketMakerMu.Unlock()
	fillRebalanceObservationOnly := fillRebalanceGeneration == 0 && s.makerFillRefreshScheduled
	now := time.Now()
	if s.makerLastPipelineLogAt.IsZero() || now.Sub(s.makerLastPipelineLogAt) >= time.Minute {
		log.WithFields(logrus.Fields{
			"symbol": s.Symbol, "observeEvidence": observeEvidence,
			"activeMakerOrders": s.executor.ActiveMakerOrders().NumOfOrders(),
			"lastQuoteAt":       s.lastMakerQuoteAt, "windowEndsAt": s.makerTradingWindowEndsAt,
		}).Info("market-maker quote pipeline active")
		s.makerLastPipelineLogAt = now
	}
	fillRefreshPending := s.makerFillRefreshPending
	fillRefreshSide := s.makerFillRefreshSide
	fillRefreshAge := time.Duration(0)
	if fillRefreshPending {
		fillRefreshAge = now.Sub(s.makerFillRefreshAt)
	}
	if observeEvidence && (s.fastEvidence != nil || len(s.fastEvidenceModels) > 0) {
		s.observeFastEvidenceBBO(now, ticker)
	}
	quoteBalances := s.makerQuoteBalances()
	// Quote planning happens before the current maker orders are canceled.
	// Include only balances reserved by those replaceable orders; otherwise an
	// active ask makes its own SOL appear unavailable on the next refresh.
	base := quoteBalances.QuoteableBase
	quoteableQuote := quoteBalances.QuoteableQuote
	mid := (ticker.Buy.Float64() + ticker.Sell.Float64()) / 2
	quoteConfig, feeSource := marketMakerConfigWithSessionFees(s.MarketMaker, s.session)
	if observeEvidence {
		onlineUpdatedAt := time.Time{}
		if s.State != nil && s.State.OnlineArrival != nil {
			onlineUpdatedAt = s.State.OnlineArrival.UpdatedAt
		}
		s.makerHorizonModel.ObserveBook(now, ticker.Buy.Float64(), ticker.Sell.Float64(), quoteConfig)
		if s.State != nil && s.State.OnlineArrival != nil &&
			s.State.OnlineArrival.UpdatedAt.After(onlineUpdatedAt) &&
			(s.makerOnlineArrivalLastSync.IsZero() ||
				now.Sub(s.makerOnlineArrivalLastSync) >= time.Duration(quoteConfig.OnlineArrival.PersistenceInterval)) {
			bbgo.Sync(ctx, s)
			s.makerOnlineArrivalLastSync = now
		}
	}
	averageCost := 0.0
	if s.Position != nil {
		averageCost = s.Position.GetAverageCost().Float64()
	}
	// Selling should only require enough base to pass the exchange's minimum
	// order filters. The risk-sized quoteNotional caps a normal ask size; it
	// must not suppress an otherwise valid smaller ask and create a one-sided
	// market maker.
	_, canSell := s.Market.GreaterThanMinimalOrderQuantity(types.SideTypeSell, ticker.Buy, base)
	modelSnapshot := ModelSnapshot{}
	if observeEvidence {
		modelSnapshot = s.updateMarketMakerModel(now, ticker)
	} else if s.model != nil {
		// A fill-triggered refresh reuses the last real BBO. Snapshot the model
		// without advancing crossing counts or their timestamps.
		modelSnapshot = s.model.Snapshot(now)
	}
	adaptiveFast := s.adaptiveFastSnapshot(now)
	selectedFastWindow := adaptiveFast.Window
	fastSnapshot := adaptiveFast.Model
	fastEvidence := adaptiveFast.Evidence
	fastHealthSummary := adaptiveFast.HealthSummary
	fastInference := inferFastCrossing(selectedFastWindow, fastSnapshot, fastEvidence, modelSnapshot)
	fastSignalHealthy := fastInference.DirectionalActions
	rawFastDirection := fastInference.Direction
	directionCoverage := fastEvidenceCoverage(
		fastEvidence,
		s.MarketMaker.FastEvidenceMinTrades,
		s.MarketMaker.FastEvidenceMinBBOUpdates,
	)
	direction := rawFastDirection * directionCoverage
	imbalance := bookImbalance(ticker)
	volumeSignal := fastEvidence.VolumeBalance.Signal
	ofiVolumeAgreement := evaluateOFIVolumeAgreement(s.MarketMaker.OFIVolumeAgreement, fastEvidence.OrderFlowImbalance30s, fastEvidence.SignedTradeImbalance5m)
	if ofiVolumeAgreement.Ready && !ofiVolumeAgreement.Agrees && s.MarketMaker.OFIVolumeAgreement.SuppressOnDisagreement {
		// A conflicting public-flow observation suppresses only the auxiliary
		// volume component. Inventory and directional risk controls remain active.
		volumeSignal = 0
		ofiVolumeAgreement.Applied = true
	}
	// Microprice barrier crossings remain a directional signal only. Execution
	// risk is learned from the observable price on each maker side: ask returns
	// for buys and bid returns for sells. A short side model adapts intraday and
	// is shrunk toward the longer executable-BBO baseline in variance space.
	slowVolatility := modelSnapshot.GammaCaptureVolatility
	fastSideVolatility := s.makerHorizonModel.EmpiricalSideVolatilityEstimate(
		now, selectedFastWindow)
	sideVolatilityPrior := s.makerHorizonModel.EmpiricalSideVolatilityEstimate(
		now, time.Duration(quoteConfig.HorizonLookback))
	fastQuoteVolatilityUsable := fastSideVolatility.BuyBps > 0 && fastSideVolatility.SellBps > 0
	liveVolatilitySamples := fastSideVolatility.MinSamples()
	buyEffectiveVolatilityBps, buyVolatilityLiveWeight := ShrinkVolatility(
		fastSideVolatility.BuyBps, sideVolatilityPrior.BuyBps,
		fastSideVolatility.BuySamples, quoteConfig.HorizonMinSamples)
	sellEffectiveVolatilityBps, sellVolatilityLiveWeight := ShrinkVolatility(
		fastSideVolatility.SellBps, sideVolatilityPrior.SellBps,
		fastSideVolatility.SellSamples, quoteConfig.HorizonMinSamples)
	effectiveVolatilityBps := math.Max(buyEffectiveVolatilityBps, sellEffectiveVolatilityBps)
	volatilityPriorBps := sideVolatilityPrior.MaxBps()
	volatilityPriorSamples := sideVolatilityPrior.MinSamples()
	volatilityLiveWeight := math.Min(buyVolatilityLiveWeight, sellVolatilityLiveWeight)
	if fillRebalanceObservationOnly {
		// Keep ingesting BBO, crossing, and fast evidence while the fill worker
		// synchronizes inventory. The existing orders remain untouched until the
		// forced generation has a complete replacement plan.
		return
	}
	if buyEffectiveVolatilityBps <= 0 || sellEffectiveVolatilityBps <= 0 {
		s.logMakerQuoteGate(now, "missing-volatility-statistics", logrus.Fields{
			"slowHealth": modelSnapshot.Health, "slowUp": modelSnapshot.Up, "slowDown": modelSnapshot.Down,
			"slowVolatilityBps":  slowVolatility * 10_000,
			"fastWindowSelected": selectedFastWindow, "fastHealth": fastSnapshot.Health,
			"fastEvidenceHealth": fastEvidence.Health, "fastVolatilityBps": fastSnapshot.GammaCaptureVolatility * 10_000,
			"volatilityPriorBps": volatilityPriorBps, "volatilityPriorSamples": volatilityPriorSamples,
			"volatilityLiveSamples": liveVolatilitySamples, "fastWindowHealths": fastHealthSummary,
		})
		// Keep an already-resting quote alive until its selected trading window
		// ends. This avoids turning a short model/data gap into a cancel/recreate
		// loop that destroys queue priority and prevents fill statistics from
		// accumulating. New windows remain fail-closed until the symbol has
		// enough observed market data.
		if fillRebalanceGeneration == 0 && s.retainMakerQuoteDuringDataGap(now, ticker) {
			return
		}
		if !s.lastMakerQuoteAt.IsZero() {
			if err := s.gracefulCancelMaker(ctx, "missing-volatility-statistics"); err != nil {
				log.WithError(err).Warn("market-maker missing volatility statistics cancellation failed")
				s.retryMakerFillRebalanceLocked(fillRebalanceGeneration)
				return
			}
			s.lastMakerQuoteAt = time.Time{}
			s.makerTradingWindowStartedAt = time.Time{}
			s.makerTradingWindowEndsAt = time.Time{}
		}
		return
	}
	quoteVolatility := effectiveVolatilityBps / 10_000
	buyQuoteVolatilityBps := buyEffectiveVolatilityBps
	sellQuoteVolatilityBps := sellEffectiveVolatilityBps
	// Inventory risk uses the conservative maximum of the two executable
	// side models; directional microprice crossings must not widen it again.
	inventoryVolatility := quoteVolatility
	selectedHorizonDecision := s.makerHorizonModel.UpdateForBook(now, quoteConfig, effectiveVolatilityBps, ticker.Buy.Float64(), ticker.Sell.Float64())
	selectedHorizon := time.Duration(selectedHorizonDecision.HorizonSeconds) * time.Second
	if selectedHorizon <= 0 {
		selectedHorizon = time.Duration(quoteConfig.MinTradingWindow)
	}
	// Couple queue lifetime to the same distance/volatility clock used by the
	// quote. Iterate over the discrete measured horizons because widening the
	// horizon can widen the volatility-aware quote and therefore its passage
	// time. This converges monotonically at the configured maximum.
	neutralTouchDistance := func(selected time.Duration) (buyDistance, sellDistance, grossEdge float64) {
		halfSpread := quoteConfig.HalfSpreadForHorizon(selected, effectiveVolatilityBps)
		return neutralMakerTouchDistances(ticker.Buy.Float64(), ticker.Sell.Float64(), halfSpread)
	}
	initialBuyDistance, initialSellDistance, _ := neutralTouchDistance(selectedHorizon)
	initialDistance := quoteConfig.OrderKeepDistanceBps(math.Max(initialBuyDistance, initialSellDistance))
	orderKeepDecision := quoteConfig.DynamicOrderKeepDecision(selectedHorizon, initialDistance, effectiveVolatilityBps)
	for i := 0; i < 3; i++ {
		buyDistance, sellDistance, _ := neutralTouchDistance(orderKeepDecision.Duration)
		distance := quoteConfig.OrderKeepDistanceBps(math.Max(buyDistance, sellDistance))
		next := quoteConfig.DynamicOrderKeepDecision(selectedHorizon, distance, effectiveVolatilityBps)
		if next.Duration == orderKeepDecision.Duration {
			orderKeepDecision = next
			break
		}
		orderKeepDecision = next
	}
	horizon := orderKeepDecision.Duration
	actualBuyDistance, actualSellDistance, actualGrossEdge := neutralTouchDistance(horizon)
	horizonDecision := selectedHorizonDecision
	if horizon != selectedHorizon ||
		math.Abs(horizonDecision.BuyTouchDistanceBps-actualBuyDistance) > 1e-9 ||
		math.Abs(horizonDecision.SellTouchDistanceBps-actualSellDistance) > 1e-9 {
		// Arrival rates must match the executable ask-to-bid-quote and
		// bid-to-ask-quote distances for the actual resting horizon.
		horizonDecision = s.makerHorizonModel.CrossingDecisionAtSideDistances(
			now, quoteConfig, horizon, actualBuyDistance, actualSellDistance, actualGrossEdge)
	}
	s.makerHorizonDecision = horizonDecision
	// Only use crossing rates measured at the actual quote distance. The
	// directional Gamma model uses its own fixed barrier width (10 bps in the
	// supplied profile), which is not a valid maker fill-rate estimate when the
	// quote is tens of bps from mid. The accepted horizon-touch model keeps mid
	// as the common martingale reference, predicts the conditional distribution
	// around it, and blends that long-history estimate with resolved recent
	// quote-distance crossings. Public touches receive a queue/fill haircut.
	buyFillRate, sellFillRate := 0.0, 0.0
	buyTouchProbability, sellTouchProbability := 0.0, 0.0
	historicalBuyTouchProbability, historicalSellTouchProbability := 0.0, 0.0
	touchFeaturesReady := false
	sideDistanceSource := "none"
	recentBuyTouchProbability, recentSellTouchProbability := 0.0, 0.0
	if horizonDecision.HasSufficientCrossings(quoteConfig.HorizonMinSamples) && horizon > 0 {
		recentBuyTouchProbability = 1 - math.Exp(-horizonDecision.DownCrossesPerHour*horizon.Hours())
		recentSellTouchProbability = 1 - math.Exp(-horizonDecision.UpCrossesPerHour*horizon.Hours())
		buyFillRate, sellFillRate = horizonDecision.DownCrossesPerHour, horizonDecision.UpCrossesPerHour
		sideDistanceSource = "horizon"
		if horizonDecision.EstimatorSource != "" {
			sideDistanceSource = horizonDecision.EstimatorSource
		}
	}
	// Public historical touch probabilities are deliberately excluded from
	// common risk sizing. They enter the joint side-hazard pressure after the
	// actual quote distances are known; private queue-fill evidence is still
	// required before they may increase total capital at risk.
	riskSizingBuyFillRate, riskSizingSellFillRate := buyFillRate, sellFillRate
	touchModelBidDistanceBps, touchModelAskDistanceBps := 0.0, 0.0
	// Scale both the quote risk budget and the inventory band from the current
	// quote-equivalent pair equity. Total balances are used for this sizing base
	// so locked maker orders do not make the policy jump on every refresh.
	pairEquityJPY := s.pairEquityQuote(mid)
	effectiveRiskBudgetJPY := quoteConfig.EffectiveInventoryRiskBudgetJPY(pairEquityJPY)
	quoteConfig.InventoryRiskBudgetJPY = effectiveRiskBudgetJPY
	dynamicQuoteNotional := quoteConfig.DynamicQuoteNotionalWithFillRates(
		effectiveVolatilityBps,
		horizon,
		sellFillRate, // upward crossings consume asks
		buyFillRate,  // downward crossings consume bids
	)
	if dynamicQuoteNotional <= 0 {
		s.logMakerQuoteGate(now, "dynamic-size-unavailable", logrus.Fields{
			"effectiveVolatilityBps": effectiveVolatilityBps, "horizon": horizon,
			"inventoryRiskBudgetJPY": quoteConfig.InventoryRiskBudgetJPY,
			"upCrossesPerHour":       sellFillRate, "downCrossesPerHour": buyFillRate,
			"horizonEffectiveSamples": horizonDecision.EffectiveSamples,
			"horizonEstimatorSource":  horizonDecision.EstimatorSource,
			"horizonDecisionReason":   horizonDecision.Reason,
		})
		// No statistically valid risk-sized ticket is available for a new
		// window. Preserve an existing quote until its window expires so the
		// market-data and fill observations can recover without queue churn.
		if fillRebalanceGeneration == 0 && s.retainMakerQuoteDuringDataGap(now, ticker) {
			return
		}
		if !s.lastMakerQuoteAt.IsZero() {
			if err := s.gracefulCancelMaker(ctx, "dynamic-size-unavailable"); err != nil {
				log.WithError(err).Warn("market-maker dynamic size cancellation failed")
				s.retryMakerFillRebalanceLocked(fillRebalanceGeneration)
				return
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
	canBuy := makerBidEligible(s.Market, ticker.Sell, quoteableQuote, fixedpoint.NewFromFloat(dynamicQuoteNotional))
	inventoryBand := InventoryBand{Target: quoteConfig.InventoryTarget, Limit: quoteConfig.InventoryLimit}
	if quoteConfig.AutoInventoryLimit {
		inventoryBand = quoteConfig.DynamicInventoryBandWithCapital(mid, inventoryVolatility*10_000, horizon, pairEquityJPY)
		// Shrink immediately when volatility expands, but grow by at most 20%
		// per update so a single quiet tick cannot reopen inventory too quickly.
		if s.makerInventoryBand.MaxInventory > s.makerInventoryBand.Target &&
			s.makerInventoryBand.Target > s.makerInventoryBand.MinInventory {
			previousLowerWidth := s.makerInventoryBand.Target - s.makerInventoryBand.MinInventory
			previousUpperWidth := s.makerInventoryBand.MaxInventory - s.makerInventoryBand.Target
			lowerWidth := math.Min(inventoryBand.Target-inventoryBand.MinInventory, previousLowerWidth*1.20)
			upperWidth := math.Min(inventoryBand.MaxInventory-inventoryBand.Target, previousUpperWidth*1.20)
			inventoryBand.MinInventory = inventoryBand.Target - lowerWidth
			inventoryBand.MaxInventory = inventoryBand.Target + upperWidth
			inventoryBand.Limit = math.Max(lowerWidth, upperWidth)
		}
		s.makerInventoryBand = inventoryBand
		quoteConfig.InventoryTarget = inventoryBand.Target
		quoteConfig.InventoryLimit = inventoryBand.Limit
	}
	if s.makerHorizonTouchModel != nil && horizon > 0 {
		features, ready := s.makerHorizonModel.HorizonTouchFeatures(now)
		touchFeaturesReady = ready
		if ready {
			provisionalPlan := quoteConfig.Quote(MarketMakerQuoteInput{
				MidPrice: mid, BestBid: ticker.Buy.Float64(), BestAsk: ticker.Sell.Float64(),
				VolatilityPerSqrtSec: effectiveVolatilityBps, BuyVolatilityPerSqrtSec: buyQuoteVolatilityBps, SellVolatilityPerSqrtSec: sellQuoteVolatilityBps, TradingHorizonSeconds: horizon.Seconds(),
				Inventory: base.Float64(), InventoryMin: inventoryBand.MinInventory, InventoryMax: inventoryBand.MaxInventory,
				DirectionSignal: direction, VolumeSignal: volumeSignal, BookImbalance: imbalance,
				BuyFillRate: buyFillRate, SellFillRate: sellFillRate, QuoteNotionalBase: dynamicQuoteNotional, CanBuy: canBuy, CanSell: canSell,
			})
			touchModelBidDistanceBps = provisionalPlan.BidTouchDistanceBps
			touchModelAskDistanceBps = provisionalPlan.AskTouchDistanceBps
			historicalBuy, buyOK := s.makerHorizonTouchModel.Predict(types.SideTypeBuy, horizon, provisionalPlan.BidTouchDistanceBps, features)
			historicalSell, sellOK := s.makerHorizonTouchModel.Predict(types.SideTypeSell, horizon, provisionalPlan.AskTouchDistanceBps, features)
			if buyOK && sellOK {
				historicalBuyTouchProbability = historicalBuy
				historicalSellTouchProbability = historicalSell
				buyTouchProbability = BlendTouchProbability(historicalBuy, recentBuyTouchProbability, quoteConfig.HorizonTouchModel.HistoricalWeight)
				sellTouchProbability = BlendTouchProbability(historicalSell, recentSellTouchProbability, quoteConfig.HorizonTouchModel.HistoricalWeight)
				buyFillRate = TouchProbabilityToRate(buyTouchProbability, horizon, quoteConfig.HorizonTouchModel.TouchToFillHaircut)
				sellFillRate = TouchProbabilityToRate(sellTouchProbability, horizon, quoteConfig.HorizonTouchModel.TouchToFillHaircut)
				sideDistanceSource = "horizon-touch"
			}
		}
	}
	plan := quoteConfig.Quote(MarketMakerQuoteInput{
		MidPrice: mid, BestBid: ticker.Buy.Float64(), BestAsk: ticker.Sell.Float64(),
		// GammaCaptureVolatility is an instantaneous log-volatility in
		// fraction/sqrt(second). Quote's input is bps/sqrt(second), and it
		// derives the expected move over the selected first-passage horizon.
		VolatilityPerSqrtSec:     effectiveVolatilityBps,
		BuyVolatilityPerSqrtSec:  buyQuoteVolatilityBps,
		SellVolatilityPerSqrtSec: sellQuoteVolatilityBps,
		TradingHorizonSeconds:    horizon.Seconds(),
		Inventory:                base.Float64(), InventoryMin: inventoryBand.MinInventory, InventoryMax: inventoryBand.MaxInventory,
		DirectionSignal: direction, VolumeSignal: volumeSignal, BookImbalance: imbalance,
		BuyFillRate: buyFillRate, SellFillRate: sellFillRate, QuoteNotionalBase: dynamicQuoteNotional,
		CanBuy: canBuy, CanSell: canSell,
	})
	buyQuoteNotional := fixedpoint.NewFromFloat(plan.BidQuoteNotional)
	sellQuoteNotional := fixedpoint.NewFromFloat(plan.AskQuoteNotional)
	// Joint quantity pressure can shrink an otherwise eligible bid below the
	// exchange minimum. Remove that side from the plan now so its inevitable
	// submission failure is not later interpreted as a missing-side refresh.
	if plan.AllowBid {
		if !makerBidEligible(s.Market, fixedpoint.NewFromFloat(plan.BidPrice), quoteableQuote, buyQuoteNotional) {
			plan.AllowBid = false
			plan.BidQuoteNotional = 0
			buyQuoteNotional = fixedpoint.Zero
		}
	}
	s.makerBuyQuoteNotional = buyQuoteNotional
	s.makerSellQuoteNotional = sellQuoteNotional

	hardBuyInventoryHeadroom := inventoryBuyHeadroomNotional(inventoryBand, base.Float64(), mid)
	hardSellInventoryHeadroom := inventorySellHeadroomQuantity(inventoryBand, base.Float64())
	orderCaps := targetCenteredInventoryOrderCaps(
		inventoryBand, base.Float64(), mid, quoteConfig.InventoryMaxOrderLevels)
	buyInventoryHeadroom := math.Min(hardBuyInventoryHeadroom, orderCaps.BuyNotional)
	sellInventoryHeadroom := math.Min(hardSellInventoryHeadroom, orderCaps.SellQuantity)

	if base.Float64() < inventoryBand.Target {
		if s.makerAcquisitionDeficitSince.IsZero() {
			s.makerAcquisitionDeficitSince = now
			s.makerAcquisitionDeficitAnchorMid = mid
		}
	} else {
		s.makerAcquisitionDeficitSince = time.Time{}
		s.makerAcquisitionDeficitAnchorMid = 0
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

	activeBidPrice := s.lastMakerBid.Float64()
	activeAskPrice := s.lastMakerAsk.Float64()
	activeBid, activeAsk := false, false
	for _, order := range s.executor.ActiveMakerOrders().Orders() {
		switch order.Side {
		case types.SideTypeBuy:
			activeBid = true
			if order.Price.Sign() > 0 {
				activeBidPrice = order.Price.Float64()
			}
		case types.SideTypeSell:
			activeAsk = true
			if order.Price.Sign() > 0 {
				activeAskPrice = order.Price.Float64()
			}
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
	earlyBumpDecision := s.makerEarlyBumpState.Update(quoteConfig.EarlyBump, EarlyBumpInput{
		Now: now, MidPrice: mid,
		BestBid: ticker.Buy.Float64(), BestAsk: ticker.Sell.Float64(),
		BaseBidPrice: plan.BidPrice,
		MinimumBidDistanceBps: math.Max(0, 2*quoteConfig.MakerFeeBps+
			2*quoteConfig.AdverseSelectionBps+quoteConfig.MinimumNetEdgeBps-plan.AskDistanceBps),
		InventoryDeficit: base.Float64() < inventoryBand.Target,
		CanBuy:           plan.AllowBid,
		BuyHeadroomJPY:   buyInventoryHeadroom,
		MinimumNotional:  s.Market.MinNotional.Float64(),
		Evidence:         fastEvidence,
	})
	plan = applyEarlyBumpBid(plan, earlyBumpDecision, mid, ticker.Sell.Float64())
	if earlyBumpDecision.Transition {
		log.WithFields(logrus.Fields{
			"phase": earlyBumpDecision.Phase, "shadowOnly": quoteConfig.EarlyBump.ShadowOnly,
			"reason": earlyBumpDecision.Reason, "applied": earlyBumpDecision.Apply,
			"baseBid": activeBidPrice, "urgencyBid": earlyBumpDecision.BidPrice,
			"deltaBps":                   earlyBumpDecision.DeltaBps,
			"drawdownBps":                fastEvidence.MidDrawdownBps,
			"drawdownWindow":             fastEvidence.MidDrawdownWindow,
			"drawdown5mBpsDiagnostic":    fastEvidence.MidDrawdown5mBps,
			"rebound30sBps":              fastEvidence.MidRebound30sBps,
			"ofi30s":                     fastEvidence.OrderFlowImbalance30s,
			"micropriceDisplacement":     fastEvidence.MicropriceDisplacement,
			"activationProbability":      earlyBumpDecision.ActivationProbability,
			"activationProbabilityLower": earlyBumpDecision.ActivationProbabilityLow,
			"baselineProbabilityUpper":   earlyBumpDecision.BaselineProbabilityHigh,
		}).Info("early-bump urgency transition")
	}
	inventoryResetAskDistanceBps := 0.0
	inventoryResetUpCrosses := 0
	inventoryResetUpRatePerHour := 0.0
	inventoryResetFillIntensityValid := false
	if quoteConfig.InventoryReset.Enabled && !now.Before(s.makerResetCooldownUntil) && activeAsk && !s.makerAskSince.IsZero() && s.model != nil && modelSnapshot.Health == HealthHealthy {
		input := InventoryResetInput{
			Now: now, AskSince: s.makerAskSince, AnchorMidPrice: s.makerAskAnchorMid,
			MidPrice: mid, BestBid: ticker.Buy.Float64(), AskPrice: activeAskPrice,
			MakerFeeBps: quoteConfig.MakerFeeBps, TakerFeeBps: quoteConfig.TakerFeeBps,
			MaxSlippageBps:       quoteConfig.InventoryReset.MaxSlippageBps,
			FillIntensityHaircut: quoteConfig.InventoryReset.FillIntensityHaircut,
			VolatilityPerSqrtSec: sellEffectiveVolatilityBps / 10_000,
			FastDirectionSignal:  rawFastDirection, FastSignalHealthy: fastSignalHealthy,
			AverageCost: averageCost,
		}
		decision := quoteConfig.InventoryReset.Evaluate(input)
		// Scan six-hour history only after the cheap age/adverse gates pass.
		// This avoids an O(history) calculation on every BBO update.
		if decision.Reason == "insufficient ask-distance crossing statistics" && activeAskPrice > ticker.Buy.Float64() && horizon > 0 {
			inventoryResetAskDistanceBps = math.Log(activeAskPrice/ticker.Buy.Float64()) * 10_000
			_, _, grossQuoteEdgeBps := MakerTouchDistances(
				ticker.Buy.Float64(), ticker.Sell.Float64(), plan.BidPrice, activeAskPrice)
			stats := s.makerHorizonModel.CrossingDecisionAtSideDistances(
				now, quoteConfig, horizon, plan.BidTouchDistanceBps,
				inventoryResetAskDistanceBps, grossQuoteEdgeBps)
			inventoryResetUpCrosses = stats.UpCrosses
			inventoryResetUpRatePerHour = stats.UpCrossesPerHour
			minimumSideSamples := quoteConfig.HorizonMinSamples / 2
			if minimumSideSamples < 1 {
				minimumSideSamples = 1
			}
			inventoryResetFillIntensityValid = stats.ObservedHours > 0 && stats.UpCrosses >= minimumSideSamples && stats.UpCrossesPerHour > 0
			input.FillIntensity = inventoryResetUpRatePerHour / 3600
			input.FillIntensityValid = inventoryResetFillIntensityValid
			decision = quoteConfig.InventoryReset.Evaluate(input)
		}
		if fillRebalanceGeneration == 0 && decision.Trigger {
			s.executeInventoryReset(ctx, ticker, base, decision)
			return
		}
	}
	acquisitionCfg := quoteConfig.AcquisitionReset
	acquisitionDrawdownLimit5mBps, _ := acquisitionCfg.CalibratedDrawdownLimit5mBps(fastEvidence)
	acquisitionReturn1mMoveBps, acquisitionReturn1mReady := acquisitionCfg.CalibratedReturnMoveBps(fastEvidence, time.Minute)
	acquisitionReturn5mMoveBps, acquisitionReturn5mReady := acquisitionCfg.CalibratedReturnMoveBps(fastEvidence, 5*time.Minute)
	acquisitionReturnCalibrationReady := acquisitionReturn1mReady && acquisitionReturn5mReady
	acquisitionReturn1mThresholdBps := -acquisitionReturn1mMoveBps
	acquisitionReturn5mThresholdBps := acquisitionReturn5mMoveBps
	acquisitionStart := acquisitionCfg.EvaluateStartShadow(AcquisitionStartInput{
		InventoryDeficit:       base.Float64() < inventoryBand.Target,
		EvidenceHealth:         fastEvidence.Health,
		Return1mBps:            fastEvidence.MidReturn1mBps,
		Return5mBps:            fastEvidence.MidReturn5mBps,
		Return1mThresholdBps:   acquisitionReturn1mThresholdBps,
		Return5mThresholdBps:   acquisitionReturn5mThresholdBps,
		ReturnCalibrationReady: acquisitionReturnCalibrationReady,
		Drawdown5mBps:          fastEvidence.MidDrawdown5mBps,
		DrawdownLimit5mBps:     acquisitionDrawdownLimit5mBps,
		TradeCount5m:           fastEvidence.TradeCount5m,
		BBOCount5m:             fastEvidence.BBOCount5m,
	})
	if acquisitionStart.Signal && (s.makerLastAcquisitionStartLogAt.IsZero() || now.Sub(s.makerLastAcquisitionStartLogAt) >= 10*time.Minute) {
		log.WithFields(logrus.Fields{"symbol": s.Symbol, "return1mBps": fastEvidence.MidReturn1mBps,
			"return5mBps": fastEvidence.MidReturn5mBps, "drawdown5mBps": fastEvidence.MidDrawdown5mBps,
			"return1mThresholdBps": acquisitionReturn1mThresholdBps,
			"return5mThresholdBps": acquisitionReturn5mThresholdBps,
			"drawdownLimit5mBps":   acquisitionDrawdownLimit5mBps,
			"tradeCount5m":         fastEvidence.TradeCount5m, "bboCount5m": fastEvidence.BBOCount5m}).Info("fee-positive acquisition start pattern observed in shadow mode")
		s.makerLastAcquisitionStartLogAt = now
	}
	acquisitionDecision := AcquisitionResetDecision{Reason: "disabled"}
	if acquisitionCfg.Enabled && activeBid && !s.makerAcquisitionDeficitSince.IsZero() &&
		!now.Before(s.makerAcquisitionCooldownUntil) &&
		base.Float64() < inventoryBand.Target && quoteableQuote.Sign() > 0 {
		acquisitionDecision = acquisitionCfg.Evaluate(AcquisitionResetInput{
			Now: now, DeficitSince: s.makerAcquisitionDeficitSince, AnchorMidPrice: s.makerAcquisitionDeficitAnchorMid,
			MidPrice: mid, BestAsk: ticker.Sell.Float64(), BidPrice: activeBidPrice,
			PlannedAskPrice: plan.AskPrice,
			MakerFeeBps:     quoteConfig.MakerFeeBps, TakerFeeBps: quoteConfig.TakerFeeBps,
			AdverseSelectionBps: quoteConfig.AdverseSelectionBps,
			MaxSlippageBps:      acquisitionCfg.MaxSlippageBps,
			UpCrosses:           horizonDecision.UpCrosses, DownCrosses: horizonDecision.DownCrosses,
			UpCrossesPerHour:        horizonDecision.UpCrossesPerHour,
			DownCrossesPerHour:      horizonDecision.DownCrossesPerHour,
			QuoteDistanceBps:        horizonDecision.QuoteDistanceBps,
			Horizon:                 horizon,
			FillIntensityHaircut:    acquisitionCfg.FillIntensityHaircut,
			VolatilityPerSqrtSec:    inventoryVolatility,
			EvidenceHealth:          fastEvidence.Health,
			Return1mBps:             fastEvidence.MidReturn1mBps,
			Return5mBps:             fastEvidence.MidReturn5mBps,
			Return1mThresholdBps:    acquisitionReturn1mThresholdBps,
			Return5mThresholdBps:    acquisitionReturn5mThresholdBps,
			ReturnCalibrationReady:  acquisitionReturnCalibrationReady,
			AdverseMoveThresholdBps: acquisitionReturn5mMoveBps,
			Drawdown5mBps:           fastEvidence.MidDrawdown5mBps,
			DrawdownLimit5mBps:      acquisitionDrawdownLimit5mBps,
			TradeCount5m:            fastEvidence.TradeCount5m,
			BBOCount5m:              fastEvidence.BBOCount5m,
		})
		if fillRebalanceGeneration == 0 && acquisitionDecision.Trigger {
			s.executeAcquisitionReset(ctx, ticker, base, quoteableQuote, inventoryBand.Target, acquisitionDecision)
			return
		}
	}

	// Do not churn maker orders on every BBO tick. Binance user-data cancel
	// updates can arrive after the local order is removed; refreshing too fast
	// then accumulates pending order updates in ActiveOrderBook.
	windowDuration := horizon
	minRefreshInterval, refreshInterval := quoteConfig.RefreshIntervals(plan.HalfSpreadBps, quoteVolatility*10_000)
	if windowDuration > 0 {
		minRefreshInterval, refreshInterval = BoundRefreshIntervals(minRefreshInterval, refreshInterval, windowDuration)
	}
	elapsed := now.Sub(s.lastMakerQuoteAt)
	materialMove := s.lastMakerMid > 0 && math.Abs(math.Log(mid/s.lastMakerMid))*10_000 >= quoteConfig.RefreshMoveBps
	materialImbalanceObserved := !s.lastMakerQuoteAt.IsZero() && math.Abs(imbalance-s.lastMakerImbalance) >= quoteConfig.RefreshImbalanceDelta
	// Thin-book L1 size changes were true on 84.7% of live evaluations and
	// reduced the refresh policy to min-interval cancel/recreate. Retain the
	// observation for diagnostics, but do not sacrifice queue priority for it.
	materialImbalance := false
	adverseAskMoveBps, adverseBidMoveBps := makerAdverseBBOChangeBps(
		s.lastMakerBestBid, s.lastMakerBestAsk, ticker.Buy.Float64(), ticker.Sell.Float64())
	adverseMove := math.Max(adverseAskMoveBps, adverseBidMoveBps) >= quoteConfig.AdverseRepriceBps
	// A mid-price move, imbalance flip, or missing side is only actionable after
	// the minimum resting interval. This preserves queue priority against
	// micro-ticks while allowing the quote to react to a material state change.
	quoteCrossed := (!s.lastMakerBid.IsZero() && s.lastMakerBid.Compare(ticker.Sell) >= 0) ||
		(!s.lastMakerAsk.IsZero() && s.lastMakerAsk.Compare(ticker.Buy) <= 0)
	missingSide := false
	sideMismatch := false
	hasBid, hasAsk := false, false
	activeMakerOrders := s.executor.ActiveMakerOrders().Orders()
	inventoryHeadroomExceeded := makerOrdersExceedInventoryBand(activeMakerOrders, base.Float64(), inventoryBand)
	if !s.lastMakerQuoteAt.IsZero() && (elapsed >= minRefreshInterval || fillRefreshPending) {
		for _, order := range activeMakerOrders {
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
	unexpectedSide := (hasBid && !plan.AllowBid) || (hasAsk && !plan.AllowAsk)
	// An inventory-headroom violation is a cancellation-only transition.
	// Do not cancel and submit in the same callback: the exchange/user-data
	// cancel is asynchronous, so the old order can still be visible on the
	// next BBO event. Repeated callbacks are rate-limited until that state
	// settles, preventing duplicate replacement orders.
	if inventoryHeadroomExceeded {
		s.logMakerQuoteGate(now, "inventory-headroom-exceeded", logrus.Fields{
			"activeMakerOrders": len(activeMakerOrders), "inventory": base,
			"inventoryMin": inventoryBand.MinInventory, "inventoryMax": inventoryBand.MaxInventory,
		})
		if makerHeadroomCancelDue(now, s.makerHeadroomCancelAt, minRefreshInterval) {
			if err := s.gracefulCancelMaker(ctx, "inventory-headroom-exceeded"); err != nil {
				log.WithError(err).Warn("market-maker headroom cancellation failed")
				s.retryMakerFillRebalanceLocked(fillRebalanceGeneration)
				return
			}
			s.makerHeadroomCancelAt = now
			s.lastMakerQuoteAt = time.Time{}
			s.makerTradingWindowStartedAt = time.Time{}
			s.makerTradingWindowEndsAt = time.Time{}
			s.lastMakerBid = fixedpoint.Zero
			s.lastMakerAsk = fixedpoint.Zero
		}
		return
	}
	// A failed exchange-feasibility projection is stable across adjacent BBO
	// callbacks. Keep safety checks above active, but avoid rebuilding the same
	// impossible replacement on every book event.
	if fillRebalanceGeneration == 0 && !s.makerReplacementRetryAfter.IsZero() && now.Before(s.makerReplacementRetryAfter) {
		return
	}
	// Ordinary price/imbalance changes are observations during the modeled
	// passage window, not reasons to destroy queue age before that model has had
	// time to resolve. Hard marketability, inventory/side-policy, and confirmed
	// fill transitions remain independently actionable.
	if !s.lastMakerQuoteAt.IsZero() && fillRebalanceGeneration == 0 {
		lockedBid := s.Market.TruncatePrice(fixedpoint.NewFromFloat(earlyBumpDecision.BidPrice)).Float64()
		tickTolerance := math.Max(1e-12, s.Market.TickSize.Float64()/2)
		earlyBumpLockResting := earlyBumpDecision.Apply && earlyBumpDecision.Phase == EarlyBumpLocked &&
			activeBid && math.Abs(activeBidPrice-lockedBid) <= tickTolerance
		if earlyBumpLockResting && !quoteCrossed && !windowExpired &&
			!missingSide && !sideMismatch && !unexpectedSide {
			// The urgency price is absolute. Do not follow a rising BBO or
			// sacrifice queue age during the lock; only execution and safety
			// conditions can end it.
			return
		}
		// The selected statistical window is a hard maximum quote age. The old
		// near-fill exception never activated in live observations and could keep
		// a stale quote indefinitely after its evidence window expired.
		if !earlyBumpDecision.Refresh &&
			!makerQuoteRefreshRequired(elapsed, minRefreshInterval, windowDuration, quoteCrossed, windowExpired,
				adverseMove, materialMove, materialImbalance, missingSide || sideMismatch) {
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
			"bid": ticker.Buy, "ask": ticker.Sell,
			"availableBase": quoteBalances.AvailableBase, "quoteableBase": base,
			"availableQuote": quoteBalances.AvailableQuote, "quoteableQuote": quoteableQuote,
			"base": base, "quoteNotional": quoteNotional, "minNotional": s.Market.MinNotional,
			"buyQuoteNotional": buyQuoteNotional, "sellQuoteNotional": sellQuoteNotional,
			"jointSidePressure": plan.SidePressure, "jointReservationShiftBps": plan.ReservationShiftBps,
			"buyQuoteFactor": plan.BidQuoteFactor, "sellQuoteFactor": plan.AskQuoteFactor,
			"minQuantity": s.Market.MinQuantity, "canBuy": canBuy, "canSell": canSell,
			"plan": plan.Reason, "allowBid": plan.AllowBid, "allowAsk": plan.AllowAsk,
			"modelHealth": modelSnapshot.Health, "modelUp": modelSnapshot.Up,
			"modelDown": modelSnapshot.Down, "modelEventAge": modelSnapshot.Age,
			"fastHealth": fastSnapshot.Health, "fastWindowSelected": selectedFastWindow,
			"fastWindowHealths": fastHealthSummary, "fastDirection": direction, "rawFastDirection": rawFastDirection,
			"directionPosteriorUpWeight": float64(fastSnapshot.Up), "directionPosteriorDownWeight": float64(fastSnapshot.Down),
			"directionPosteriorEffectiveSamples": float64(fastSnapshot.Up + fastSnapshot.Down), "directionEvidenceCoverage": directionCoverage,
			"fastActivity": fastInference.Activity, "fastDataHealth": fastInference.DataHealth,
			"fastRateUsable": fastInference.RateUsable, "fastDirectionalActions": fastInference.DirectionalActions,
			"fastDirectionConfidence": fastInference.DirectionConfidence, "fastRateSource": fastInference.RateSource,
			"fastPosteriorUpRatePerHour": fastInference.LambdaUp * 3600, "fastPosteriorDownRatePerHour": fastInference.LambdaDown * 3600,
			"fastPosteriorObserved": fastInference.Observed, "fastPosteriorPriorExposure": fastInference.PriorExposure,
			"bookImbalance":      imbalance,
			"fastEvidenceHealth": evidence.Health, "fastTradeCount": evidence.TradeCount,
			"ofiVolumeAgreementEnabled": s.MarketMaker.OFIVolumeAgreement.Enabled,
			"ofiVolumeAgreementReady":   ofiVolumeAgreement.Ready,
			"ofiVolumeAgreement":        ofiVolumeAgreement.Agrees,
			"ofiVolumeAgreementApplied": ofiVolumeAgreement.Applied,
			"ofiVolumeAgreementReason":  ofiVolumeAgreement.Reason,
			"volumeBalanceState":        evidence.VolumeBalance.State, "volumeShockScore": evidence.VolumeBalance.ShockScore,
			"volumeAbsorptionScore": evidence.VolumeBalance.AbsorptionScore, "volumeBalanceProgress": evidence.VolumeBalance.BalanceProgress,
			"volumeSignedPressure": evidence.VolumeBalance.SignedPressure, "volumeBalanceSignal": evidence.VolumeBalance.Signal,
			"volumeBalanceConfidence": evidence.VolumeBalance.Confidence, "volumeZ": evidence.VolumeBalance.VolumeZ,
			"fastBBOCount": evidence.BBOCount, "fastTradeImbalance": evidence.SignedTradeImbalance,
			"fastQueueImbalance": evidence.QueueImbalance, "fastMidReturnBps": evidence.MidReturnBps,
			"fastTradeCount5m":                        evidence.TradeCount5m,
			"fastBBOCount5m":                          evidence.BBOCount5m,
			"fastTradeImbalance5m":                    evidence.SignedTradeImbalance5m,
			"fastMidReturn1mBps":                      evidence.MidReturn1mBps,
			"fastMidReturn5mBps":                      evidence.MidReturn5mBps,
			"fastMidDrawdownBps":                      evidence.MidDrawdownBps,
			"fastMidDrawdownWindow":                   evidence.MidDrawdownWindow,
			"fastMidDrawdown5mBps":                    evidence.MidDrawdown5mBps,
			"fastMidRebound30sBps":                    evidence.MidRebound30sBps,
			"fastMidLow30s":                           evidence.MidLow30s,
			"fastOrderFlowImbalance30s":               evidence.OrderFlowImbalance30s,
			"fastMicropriceDisplacement":              evidence.MicropriceDisplacement,
			"fastMidVolatilityPerSqrtSecond5mBps":     evidence.MidVolatilityPerSqrtSecond5mBps,
			"fastBuyAskVolatilityPerSqrtSecond5mBps":  evidence.BuyVolatilityPerSqrtSecond5mBps,
			"fastSellBidVolatilityPerSqrtSecond5mBps": evidence.SellVolatilityPerSqrtSecond5mBps,
			"fastMidVolatilitySamples5m":              evidence.MidVolatilitySamples5m,
			"acquisitionDrawdownLimit5mBps":           acquisitionDrawdownLimit5mBps,
			"acquisitionReturn1mThresholdBps":         acquisitionReturn1mThresholdBps,
			"acquisitionReturn5mThresholdBps":         acquisitionReturn5mThresholdBps,
			"acquisitionReturnCalibrationReady":       acquisitionReturnCalibrationReady,
			"acquisitionStartShadowSignal":            acquisitionStart.Signal,
			"acquisitionStartShadowReason":            acquisitionStart.Reason,
			"fastRealizedVolatilityBps":               evidence.RealizedVolatilityBps, "fastEvidenceAge": evidence.Age,
			"slowModelVolatilityBps":             modelSnapshot.GammaCaptureVolatility * 10_000,
			"fastModelVolatilityBps":             fastSnapshot.GammaCaptureVolatility * 10_000,
			"fastQuoteVolatilityUsable":          fastQuoteVolatilityUsable,
			"fastSlowVolatilityDeltaBps":         (fastSnapshot.GammaCaptureVolatility - modelSnapshot.GammaCaptureVolatility) * 10_000,
			"makerFeeBpsEffective":               quoteConfig.MakerFeeBps,
			"takerFeeBpsEffective":               quoteConfig.TakerFeeBps,
			"feeSource":                          feeSource,
			"roundTripMakerFeeBps":               2 * quoteConfig.MakerFeeBps,
			"quoteEdgeAfterFeesBps":              plan.BidDistanceBps + plan.AskDistanceBps - 2*quoteConfig.MakerFeeBps,
			"quoteNetEdgeBps":                    plan.BidDistanceBps + plan.AskDistanceBps - 2*quoteConfig.MakerFeeBps - 2*quoteConfig.AdverseSelectionBps - quoteConfig.MinimumNetEdgeBps,
			"quoteVolatilityBps":                 quoteVolatility * 10_000,
			"volatilityPriorBps":                 volatilityPriorBps,
			"buyAskVolatilityPriorBps":           sideVolatilityPrior.BuyBps,
			"sellBidVolatilityPriorBps":          sideVolatilityPrior.SellBps,
			"buyAskVolatilityPriorSamples":       sideVolatilityPrior.BuySamples,
			"sellBidVolatilityPriorSamples":      sideVolatilityPrior.SellSamples,
			"buyEffectiveVolatilityBps":          buyEffectiveVolatilityBps,
			"sellEffectiveVolatilityBps":         sellEffectiveVolatilityBps,
			"buyVolatilityLiveWeight":            buyVolatilityLiveWeight,
			"sellVolatilityLiveWeight":           sellVolatilityLiveWeight,
			"volatilityPriorSamples":             volatilityPriorSamples,
			"volatilityLiveWeight":               volatilityLiveWeight,
			"inventoryVolatilityBps":             inventoryVolatility * 10_000,
			"quoteHalfSpreadBps":                 plan.HalfSpreadBps,
			"bidDistanceBps":                     plan.BidDistanceBps,
			"askDistanceBps":                     plan.AskDistanceBps,
			"bidTouchDistanceBps":                plan.BidTouchDistanceBps,
			"askTouchDistanceBps":                plan.AskTouchDistanceBps,
			"averageCost":                        averageCost,
			"askEquityFloor":                     plan.AskEquityFloor,
			"askNetMarkEdgeBps":                  plan.AskNetMarkEdgeBps,
			"equityProtectionActive":             plan.EquityProtected,
			"sideDistanceBias":                   plan.SidePressure,
			"sideDistanceSource":                 sideDistanceSource,
			"fairPriceSource":                    "mid-martingale-baseline",
			"horizonTouchModelEnabled":           s.makerHorizonTouchModel != nil,
			"horizonTouchFeaturesReady":          touchFeaturesReady,
			"historicalBuyTouchProbability":      historicalBuyTouchProbability,
			"historicalSellTouchProbability":     historicalSellTouchProbability,
			"recentBuyTouchProbability":          recentBuyTouchProbability,
			"recentSellTouchProbability":         recentSellTouchProbability,
			"buyTouchProbability":                buyTouchProbability,
			"sellTouchProbability":               sellTouchProbability,
			"touchToFillHaircut":                 quoteConfig.HorizonTouchModel.TouchToFillHaircut,
			"touchModelBidDistanceBps":           touchModelBidDistanceBps,
			"touchModelAskDistanceBps":           touchModelAskDistanceBps,
			"riskSizingBuyFillRatePerHour":       riskSizingBuyFillRate,
			"riskSizingSellFillRatePerHour":      riskSizingSellFillRate,
			"buyFillRatePerHour":                 buyFillRate,
			"sellFillRatePerHour":                sellFillRate,
			"selectedHorizon":                    selectedHorizon,
			"orderKeepDuration":                  windowDuration,
			"orderKeepFirstPassageTime":          orderKeepDecision.CharacteristicFirstPassageTime,
			"orderKeepDistanceBps":               orderKeepDecision.QuoteDistanceBps,
			"orderKeepReason":                    orderKeepDecision.Reason,
			"horizonScoreBpsPerHour":             horizonDecision.ScoreBpsPerHour,
			"horizonUpPerHour":                   horizonDecision.UpCrossesPerHour,
			"horizonDownPerHour":                 horizonDecision.DownCrossesPerHour,
			"horizonEstimatorSource":             horizonDecision.EstimatorSource,
			"horizonEffectiveSamples":            horizonDecision.EffectiveSamples,
			"horizonOnlineFastWeight":            horizonDecision.OnlineFastWeight,
			"horizonDecisionReason":              horizonDecision.Reason,
			"acquisitionResetEnabled":            acquisitionCfg.Enabled,
			"acquisitionResetReason":             acquisitionDecision.Reason,
			"acquisitionDeficitAge":              acquisitionDecision.Age,
			"acquisitionAdverseMoveBps":          acquisitionDecision.AdverseMoveBps,
			"acquisitionAdverseMoveThresholdBps": acquisitionDecision.AdverseMoveThresholdBps,
			"acquisitionUpProbabilityLower":      acquisitionDecision.UpProbabilityLower,
			"acquisitionUpRateLowerPerHour":      acquisitionDecision.UpRateLowerPerHour,
			"acquisitionDownRateUpperPerHour":    acquisitionDecision.DownRateUpperPerHour,
			"acquisitionPassiveFillProbability":  acquisitionDecision.PassiveBidFillProbability,
			"acquisitionExitFillProbability":     acquisitionDecision.MakerExitFillProbability,
			"acquisitionWaitValueBps":            acquisitionDecision.PassiveWaitValueBps,
			"acquisitionIOCValueBps":             acquisitionDecision.IOCValueBps,
			"acquisitionIOCImprovementBps":       acquisitionDecision.IOCImprovementBps,
			"earlyBumpPhase":                     earlyBumpDecision.Phase,
			"earlyBumpSignal":                    earlyBumpDecision.Signal,
			"earlyBumpApplied":                   earlyBumpDecision.Apply,
			"earlyBumpRefresh":                   earlyBumpDecision.Refresh,
			"earlyBumpReason":                    earlyBumpDecision.Reason,
			"earlyBumpBid":                       earlyBumpDecision.BidPrice,
			"earlyBumpDeltaBps":                  earlyBumpDecision.DeltaBps,
			"earlyBumpDrawdownBps":               fastEvidence.MidDrawdownBps,
			"earlyBumpDrawdownWindow":            fastEvidence.MidDrawdownWindow,
			"earlyBumpProbability":               earlyBumpDecision.ActivationProbability,
			"earlyBumpProbabilityLower":          earlyBumpDecision.ActivationProbabilityLow,
			"earlyBumpBaselineProbabilityUpper":  earlyBumpDecision.BaselineProbabilityHigh,
			"inventoryMin":                       inventoryBand.MinInventory,
			"inventoryTarget":                    quoteConfig.InventoryTarget,
			"inventoryLimit":                     quoteConfig.InventoryLimit,
			"inventoryMax":                       inventoryBand.MaxInventory,
			"inventoryTargetRatio":               inventoryBand.TargetRatio,
			"pairEquityJPY":                      pairEquityJPY,
			"inventoryRiskBudgetEffectiveJPY":    effectiveRiskBudgetJPY,
			"inventoryCapitalMinJPY":             inventoryBand.CapitalMinNotionalJPY,
			"inventoryCapitalTargetJPY":          inventoryBand.CapitalTargetNotionalJPY,
			"inventoryCapitalCapJPY":             inventoryBand.CapitalCapNotionalJPY,
			"inventoryEffectiveMinJPY":           inventoryBand.MinInventory * mid,
			"inventoryEffectiveTargetJPY":        inventoryBand.Target * mid,
			"inventoryEffectiveMaxJPY":           inventoryBand.MaxInventory * mid,
			"inventoryRiskBandHalfWidthJPY":      inventoryBand.RiskBandHalfWidthNotionalJPY,
			"inventoryBuyHeadroomJPY":            buyInventoryHeadroom,
			"inventorySellHeadroomJPY":           sellInventoryHeadroom * mid,
			"inventoryHardBuyHeadroomJPY":        hardBuyInventoryHeadroom,
			"inventoryHardSellHeadroomJPY":       hardSellInventoryHeadroom * mid,
			"inventoryOrderTrancheJPY":           orderCaps.TrancheNotional,
			"inventoryHeadroomExceeded":          inventoryHeadroomExceeded,
			"quoteNotionalDynamic":               dynamicQuoteNotional,
			"quoteOrderSize":                     inventoryBand.OrderSize,
			"effectiveOrderLevels":               quoteConfig.EffectiveOrderLevels(windowDuration, riskSizingSellFillRate, riskSizingBuyFillRate),
			"inventoryRiskMoveBps":               inventoryBand.RiskMoveBps,
			"missingSide":                        missingSide,
			"sideMismatch":                       sideMismatch,
			"fillRefreshPending":                 fillRefreshPending,
			"fillRefreshSide":                    fillRefreshSide,
			"fillRefreshAge":                     fillRefreshAge,
			"materialMove":                       materialMove,
			"materialImbalance":                  materialImbalance,
			"materialImbalanceObserved":          materialImbalanceObserved,
			"inventoryResetAskDistanceBps":       inventoryResetAskDistanceBps,
			"inventoryResetUpCrosses":            inventoryResetUpCrosses,
			"inventoryResetUpRatePerHour":        inventoryResetUpRatePerHour,
			"inventoryResetFillIntensityValid":   inventoryResetFillIntensityValid,
			"adverseAskMoveBps":                  adverseAskMoveBps, "adverseBidMoveBps": adverseBidMoveBps,
			"adverseRepriceBps":    quoteConfig.AdverseRepriceBps,
			"inventoryExposureAge": exposureAge,
			"refreshMin":           minRefreshInterval, "refreshMax": refreshInterval,
			"modelReferenceAge": now.Sub(s.State.LastReferenceTime),
		}).Info("market-maker quote evaluation")
		s.lastMakerDiagnosticAt = now
	}
	if plan.Reason != "quoted" {
		if err := s.gracefulCancelMaker(ctx, "invalid-quote-plan"); err != nil {
			log.WithError(err).Warn("market-maker cancel after invalid quote failed")
			s.retryMakerFillRebalanceLocked(fillRebalanceGeneration)
			return
		}
		s.makerTradingWindowStartedAt = time.Time{}
		s.makerTradingWindowEndsAt = time.Time{}
		s.makerAskSince = time.Time{}
		s.makerAskAnchorMid = 0
		s.makerFillRefreshPending = false
		s.makerFillRefreshSide = ""
		s.makerFillRefreshAt = time.Time{}
		return
	}
	var submits []types.SubmitOrder
	bidSubmitted := false
	askSubmitted := false
	if plan.AllowBid {
		price := s.Market.TruncatePrice(fixedpoint.NewFromFloat(plan.BidPrice))
		buyCapacity := fixedpoint.Min(quoteableQuote, buyQuoteNotional)
		if inventoryBand.MaxInventory > 0 {
			hardHeadroom := inventoryBuyHeadroomNotional(inventoryBand, base.Float64(), price.Float64())
			targetCaps := targetCenteredInventoryOrderCaps(
				inventoryBand, base.Float64(), price.Float64(), quoteConfig.InventoryMaxOrderLevels)
			headroom := makerBuyInventoryCapacity(
				s.Market, price,
				fixedpoint.NewFromFloat(targetCaps.BuyNotional),
				fixedpoint.NewFromFloat(hardHeadroom))
			buyCapacity = fixedpoint.Min(buyCapacity, headroom)
		}
		qty, ok := s.Market.GreaterThanMinimalOrderQuantity(types.SideTypeBuy, price, buyCapacity)
		if !ok {
			log.WithFields(logrus.Fields{"side": "BUY", "price": price, "availableQuote": quoteBalances.AvailableQuote, "quoteableQuote": quoteableQuote, "minNotional": s.Market.MinNotional, "minQuantity": s.Market.MinQuantity}).Warn("market-maker bid blocked by exchange quantity filters")
		}
		if ok && price.Compare(ticker.Sell) < 0 {
			// Binance LIMIT_MAKER orders reject an explicit timeInForce.
			bidSubmitted = true
			submits = append(submits, types.SubmitOrder{Symbol: s.Symbol, Market: s.Market, Side: types.SideTypeBuy, Type: types.OrderTypeLimitMaker, Price: price, Quantity: qty, ClientOrderID: marketMakerClientOrderID(types.SideTypeBuy), Tag: "gammacapture-mm-bid"})
		}
	}
	if plan.AllowAsk {
		price := makerProtectedAskPrice(s.Market, plan.AskPrice, plan.AskEquityFloor)
		eligibleBase := base
		if inventoryBand.MaxInventory > 0 {
			hardHeadroom := inventorySellHeadroomQuantity(inventoryBand, base.Float64())
			targetCaps := targetCenteredInventoryOrderCaps(
				inventoryBand, base.Float64(), price.Float64(), quoteConfig.InventoryMaxOrderLevels)
			eligibleBase = makerSellInventoryCapacity(
				s.Market, price,
				fixedpoint.NewFromFloat(targetCaps.SellQuantity),
				fixedpoint.NewFromFloat(hardHeadroom))
		}
		qty, ok := s.makerAskQuantity(price, eligibleBase, sellQuoteNotional)
		if !ok {
			log.WithFields(logrus.Fields{"side": "SELL", "price": price, "base": base, "minNotional": s.Market.MinNotional, "minQuantity": s.Market.MinQuantity}).Warn("market-maker ask blocked by exchange quantity filters")
		}
		if ok && price.Compare(ticker.Buy) > 0 {
			askSubmitted = true
			submits = append(submits, types.SubmitOrder{Symbol: s.Symbol, Market: s.Market, Side: types.SideTypeSell, Type: types.OrderTypeLimitMaker, Price: price, Quantity: qty, ClientOrderID: marketMakerClientOrderID(types.SideTypeSell), Tag: "gammacapture-mm-ask"})
		}
	}
	// Replacement construction, including exchange quantity/notional filters,
	// must finish before any resting order is cancelled. A model ticket can be
	// temporarily unexecutable near the inventory target; destroying a safe
	// opposite-side quote in that state creates a needless empty-book interval.
	if len(submits) == 0 {
		const noReplacementRetryDelay = 10 * time.Second
		s.makerReplacementRetryAfter = now.Add(noReplacementRetryDelay)
		if s.makerLastNoSubmissionLogAt.IsZero() || now.Sub(s.makerLastNoSubmissionLogAt) >= noReplacementRetryDelay {
			log.WithFields(logrus.Fields{
				"symbol": s.Symbol, "plan": plan.Reason, "allowBid": plan.AllowBid, "allowAsk": plan.AllowAsk,
				"hasBid": hasBid, "hasAsk": hasAsk, "canBuy": canBuy, "canSell": canSell,
				"bidConstructed": bidSubmitted, "askConstructed": askSubmitted,
				"availableBase": quoteBalances.AvailableBase, "quoteableBase": base,
				"availableQuote": quoteBalances.AvailableQuote, "quoteableQuote": quoteableQuote,
				"buyQuoteNotional": buyQuoteNotional, "sellQuoteNotional": sellQuoteNotional,
				"windowDuration": windowDuration, "elapsed": elapsed,
			}).Warn("market-maker replacement produced no orders; preserving active quotes")
			s.makerLastNoSubmissionLogAt = now
		}
		return
	}
	// Every accepted refresh rebuilds the complete risk-sized quote set. The
	// replacement prices and final exchange-valid quantities now exist, so this
	// is the last possible point at which cancellation can safely begin.
	if err := s.gracefulCancelMaker(ctx, "quote-refresh-required"); err != nil {
		log.WithError(err).Warn("market-maker cancel existing quotes failed")
		s.retryMakerFillRebalanceLocked(fillRebalanceGeneration)
		return
	}
	if len(submits) > 0 {
		if _, err := s.executor.SubmitOrders(ctx, submits...); err != nil {
			s.State.LastDecision = "maker quote rejected: " + err.Error()
			log.WithError(err).Error("market-maker quote submission failed")
			s.retryMakerFillRebalanceLocked(fillRebalanceGeneration)
		} else {
			submittedBidPrice, submittedAskPrice := makerSubmittedSidePrices(submits)
			log.WithField("orders", len(submits)).Info("market-maker quotes submitted")
			s.makerReplacementRetryAfter = time.Time{}
			s.lastMakerQuoteAt = now
			s.makerTradingWindowStartedAt = now
			s.makerTradingWindowEndsAt = now.Add(windowDuration)
			// Track only sides that were actually accepted for submission. A
			// theoretical price for a balance- or policy-disabled side must not
			// later trigger quoteCrossed and churn the real resting side.
			s.lastMakerBid = submittedBidPrice
			s.lastMakerAsk = submittedAskPrice
			s.lastMakerBestBid = ticker.Buy.Float64()
			s.lastMakerBestAsk = ticker.Sell.Float64()
			s.lastMakerMid = mid
			s.lastMakerImbalance = imbalance
			s.lastMakerDirection = direction
			s.makerHeadroomCancelAt = time.Time{}
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

func makerSubmittedSidePrices(submits []types.SubmitOrder) (bid, ask fixedpoint.Value) {
	for _, order := range submits {
		switch order.Side {
		case types.SideTypeBuy:
			bid = order.Price
		case types.SideTypeSell:
			ask = order.Price
		}
	}
	return bid, ask
}

func makerBidEligible(market types.Market, price, quoteableQuote, plannedNotional fixedpoint.Value) bool {
	if price.Sign() <= 0 || quoteableQuote.Sign() <= 0 || plannedNotional.Sign() <= 0 {
		return false
	}
	capacity := fixedpoint.Min(quoteableQuote, plannedNotional)
	_, ok := market.GreaterThanMinimalOrderQuantity(types.SideTypeBuy, price, capacity)
	return ok
}

// makerMinimumExecutableQuantity converts Binance's continuous min-notional
// constraint onto the symbol's discrete quantity lattice. The result is the
// smallest base quantity that survives the same truncation used at submission.
func makerMinimumExecutableQuantity(market types.Market, price fixedpoint.Value) (fixedpoint.Value, bool) {
	if price.Sign() <= 0 {
		return fixedpoint.Zero, false
	}
	quantity := market.AdjustQuantityByMinNotional(fixedpoint.Zero, price)
	quantity = market.AdjustQuantityByMinQuantity(quantity)
	quantity = market.RoundUpByStepSize(quantity)
	return market.GreaterThanMinimalOrderQuantity(types.SideTypeSell, price, quantity)
}

// makerMinimumExecutableBuyCapacity returns the smallest quote capacity that
// still produces an exchange-valid BUY after quote-precision and quantity-step
// truncation. Adding one quote tick prevents a valid base quantity from being
// rounded back below min-notional by the BUY-side conversion path.
func makerMinimumExecutableBuyCapacity(market types.Market, price fixedpoint.Value) (fixedpoint.Value, bool) {
	quantity, ok := makerMinimumExecutableQuantity(market, price)
	if !ok {
		return fixedpoint.Zero, false
	}
	capacity := price.Mul(quantity)
	if market.TickSize.Sign() > 0 {
		capacity = capacity.Add(market.TickSize)
	}
	if _, ok := market.GreaterThanMinimalOrderQuantity(types.SideTypeBuy, price, capacity); !ok {
		return fixedpoint.Zero, false
	}
	return capacity, true
}

// makerBuyInventoryCapacity floors a model tranche to the smallest executable
// BUY only when hard inventory headroom can absorb it. Otherwise the hard band
// remains authoritative and the side is omitted.
func makerBuyInventoryCapacity(market types.Market, price, modelCap, hardCap fixedpoint.Value) fixedpoint.Value {
	capacity := fixedpoint.Min(modelCap, hardCap)
	minimum, ok := makerMinimumExecutableBuyCapacity(market, price)
	if ok && hardCap.Compare(minimum) >= 0 && capacity.Compare(minimum) < 0 {
		capacity = minimum
	}
	return fixedpoint.Min(capacity, hardCap)
}

// makerSellInventoryCapacity is the SELL-side equivalent. Its capacity is a
// base quantity because Binance applies the lot-size filter before notional.
func makerSellInventoryCapacity(market types.Market, price, modelCap, hardCap fixedpoint.Value) fixedpoint.Value {
	capacity := fixedpoint.Min(modelCap, hardCap)
	minimum, ok := makerMinimumExecutableQuantity(market, price)
	if ok && hardCap.Compare(minimum) >= 0 && capacity.Compare(minimum) < 0 {
		capacity = minimum
	}
	return fixedpoint.Min(capacity, hardCap)
}

func inventoryBuyHeadroomNotional(band InventoryBand, inventory, price float64) float64 {
	if band.MaxInventory <= 0 || price <= 0 {
		return 0
	}
	return math.Max(0, band.MaxInventory-inventory) * price
}

func inventorySellHeadroomQuantity(band InventoryBand, inventory float64) float64 {
	if band.MaxInventory <= 0 {
		return 0
	}
	return math.Max(0, inventory-band.MinInventory)
}

type targetCenteredOrderCaps struct {
	TrancheNotional float64
	BuyNotional     float64
	SellQuantity    float64
}

// targetCenteredInventoryOrderCaps separates the hard inventory band from a
// single executable ticket. Corrective orders may move inventory to the target
// in one fill but cannot traverse from one band edge to the opposite edge. At
// the target, the symmetric band half-width is divided by the configured level
// capacity, giving the unified model a small, equity-scaled exploration ticket.
func targetCenteredInventoryOrderCaps(band InventoryBand, inventory, price, maxLevels float64) targetCenteredOrderCaps {
	if price <= 0 || band.MaxInventory <= band.MinInventory ||
		band.Target < band.MinInventory || band.Target > band.MaxInventory {
		return targetCenteredOrderCaps{}
	}
	lowerNotional := math.Max(0, band.Target-band.MinInventory) * price
	upperNotional := math.Max(0, band.MaxInventory-band.Target) * price
	halfWidthNotional := math.Min(lowerNotional, upperNotional)
	if halfWidthNotional <= 0 {
		halfWidthNotional = math.Max(lowerNotional, upperNotional)
	}
	if band.RiskBandHalfWidthNotionalJPY > 0 {
		halfWidthNotional = math.Min(halfWidthNotional, band.RiskBandHalfWidthNotionalJPY)
	}
	if halfWidthNotional <= 0 {
		return targetCenteredOrderCaps{}
	}
	levels := math.Max(1, maxLevels)
	trancheNotional := halfWidthNotional / levels
	buyCorrection := math.Max(0, band.Target-inventory) * price
	sellCorrectionNotional := math.Max(0, inventory-band.Target) * price
	return targetCenteredOrderCaps{
		TrancheNotional: trancheNotional,
		BuyNotional:     math.Max(trancheNotional, buyCorrection),
		SellQuantity:    math.Max(trancheNotional, sellCorrectionNotional) / price,
	}
}

func makerOrdersExceedInventoryBand(orders types.OrderSlice, inventory float64, band InventoryBand) bool {
	if band.MaxInventory <= band.MinInventory {
		return false
	}
	var remainingBuy, remainingSell float64
	for _, order := range orders {
		remaining := order.GetRemainingQuantity().Float64()
		if remaining <= 0 {
			continue
		}
		if order.Side == types.SideTypeBuy {
			remainingBuy += remaining
		} else if order.Side == types.SideTypeSell {
			remainingSell += remaining
		}
	}
	const quantityTolerance = 1e-12
	// Existing inventory may already be outside the newly recalculated dynamic
	// band. That condition must not suppress the corrective side when there are
	// no resting orders. Reject only an order reservation that would breach (or
	// further worsen) its corresponding edge; ordinary quote planning below
	// will independently disable the risk-increasing side.
	return (remainingBuy > quantityTolerance && inventory+remainingBuy > band.MaxInventory+quantityTolerance) ||
		(remainingSell > quantityTolerance && inventory-remainingSell < band.MinInventory-quantityTolerance)
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

// marketMakerConfigWithSessionFees uses authenticated exchange/account fees
// without allowing an optimistic session estimate to undercut the configured
// fail-safe. Binance can report a discounted tier even when the actual fill is
// charged in quote/base at the undiscounted rate; sizing must use the larger
// observable rate until executions prove the discount is applied.
func marketMakerConfigWithSessionFees(cfg MarketMakerConfig, session *bbgo.ExchangeSession) (MarketMakerConfig, string) {
	if session == nil {
		return cfg, "yaml-fallback"
	}
	accountRates := session.Account != nil && session.Account.HasFeeRate
	makerAvailable := accountRates || session.MakerFeeRateConfig != nil || !session.MakerFeeRate.IsZero()
	takerAvailable := accountRates || session.TakerFeeRateConfig != nil || !session.TakerFeeRate.IsZero()
	usedYAMLFloor := false
	if makerAvailable {
		sessionMakerBps := session.MakerFeeRate.Float64() * 10_000
		usedYAMLFloor = sessionMakerBps < cfg.MakerFeeBps
		cfg.MakerFeeBps = math.Max(cfg.MakerFeeBps, sessionMakerBps)
	}
	if takerAvailable {
		sessionTakerBps := session.TakerFeeRate.Float64() * 10_000
		usedYAMLFloor = usedYAMLFloor || sessionTakerBps < cfg.TakerFeeBps
		cfg.TakerFeeBps = math.Max(cfg.TakerFeeBps, sessionTakerBps)
	}
	if makerAvailable || takerAvailable {
		if usedYAMLFloor {
			return cfg, "session+yaml-floor"
		}
		return cfg, "session"
	}
	return cfg, "yaml-fallback"
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

// makerHeadroomCancelDue rate-limits cancellation-only headroom corrections while
// asynchronous order updates are still draining from ActiveOrderBook.
func makerHeadroomCancelDue(now, lastCancel time.Time, minInterval time.Duration) bool {
	if lastCancel.IsZero() || minInterval <= 0 {
		return true
	}
	return now.Sub(lastCancel) >= minInterval
}

// makerQuoteRefreshRequired centralizes the quote refresh policy. The minimum
// resting interval is a transport anti-churn floor. Hard lifecycle transitions
// may act after that floor; ordinary market-state changes wait for the modeled
// first-passage keep duration.
func makerQuoteRefreshRequired(elapsed, minRefreshInterval, orderKeepDuration time.Duration, quoteCrossed, windowExpired, adverseMove, materialMove, materialImbalance, missingSide bool) bool {
	if elapsed < minRefreshInterval {
		return false
	}
	// A crossed quote or missing/policy-mismatched side is a hard lifecycle
	// transition. Ordinary market movement is part of the first-passage path
	// and must not cancel the order before its modeled keep duration.
	if quoteCrossed || missingSide {
		return true
	}
	if orderKeepDuration > 0 && elapsed < orderKeepDuration {
		return false
	}
	return windowExpired || adverseMove || materialMove || materialImbalance
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
func makerProtectedAskPrice(market types.Market, planned, costFloor float64) fixedpoint.Value {
	price := market.TruncatePrice(fixedpoint.NewFromFloat(planned))
	if costFloor <= 0 || price.Float64() >= costFloor {
		return price
	}
	tick := market.TickSize.Float64()
	if tick <= 0 {
		return fixedpoint.NewFromFloat(costFloor)
	}
	// Round upward so formatting cannot turn a protected exit fee-negative.
	ceiling := math.Ceil((costFloor-1e-12)/tick) * tick
	return market.TruncatePrice(fixedpoint.NewFromFloat(ceiling))
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
	if err := s.gracefulCancelMaker(ctx, "inventory-reset"); err != nil {
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
	if s.State != nil {
		s.State.MakerResetCooldownUntil = s.makerResetCooldownUntil
		bbgo.Sync(ctx, s)
	}
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
		"iocRoundTripBps": decision.IOCRoundTripBps, "iocImprovementBps": decision.IOCImprovementBps,
		"expectedFutureDriftBps": decision.ExpectedFutureDriftBps,
		"riskBps":                decision.RiskBps, "cooldown": cfg.Cooldown,
	}).Warn("inventory reset IOC sell submitted")
}

func (s *Strategy) executeAcquisitionReset(ctx context.Context, ticker types.BookTicker, base, quoteableQuote fixedpoint.Value, inventoryTarget float64, decision AcquisitionResetDecision) {
	cfg := s.MarketMaker.AcquisitionReset
	if inventoryTarget <= 0 || base.Float64() >= inventoryTarget || quoteableQuote.Sign() <= 0 {
		return
	}

	price := ticker.Sell.Mul(fixedpoint.NewFromFloat(1 + cfg.MaxSlippageBps/10_000))
	price = s.Market.TruncatePrice(price)
	if price.Sign() <= 0 || price.Compare(ticker.Sell) < 0 {
		log.WithFields(logrus.Fields{"price": price, "bestAsk": ticker.Sell}).Warn("acquisition reset blocked by invalid IOC price")
		return
	}
	shortfall := fixedpoint.NewFromFloat(inventoryTarget).Sub(base)
	maxNotional := shortfall.Mul(price)
	maxNotional = fixedpoint.Min(maxNotional, quoteableQuote)
	if s.makerBuyQuoteNotional.Sign() > 0 {
		maxNotional = fixedpoint.Min(maxNotional, s.makerBuyQuoteNotional)
	}
	quantity, ok := s.Market.GreaterThanMinimalOrderQuantity(types.SideTypeBuy, price, maxNotional)
	if !ok {
		log.WithFields(logrus.Fields{
			"base": base, "target": inventoryTarget, "price": price,
			"quoteableQuote": quoteableQuote, "maxNotional": maxNotional,
			"minNotional": s.Market.MinNotional, "minQuantity": s.Market.MinQuantity,
		}).Warn("acquisition reset blocked by exchange quantity filters")
		return
	}

	if err := s.gracefulCancelMaker(ctx, "acquisition-reset"); err != nil {
		log.WithError(err).Warn("acquisition reset cancel failed")
		return
	}
	_, err := s.executor.SubmitOrders(ctx, types.SubmitOrder{
		Symbol: s.Symbol, Market: s.Market, Side: types.SideTypeBuy,
		Type: types.OrderTypeLimit, TimeInForce: types.TimeInForceIOC,
		Price: price, Quantity: quantity, Tag: "gammacapture-acquisition-reset",
	})
	if err != nil {
		log.WithError(err).Error("acquisition reset IOC buy failed")
		return
	}

	now := time.Now()
	s.makerAcquisitionCooldownUntil = now.Add(time.Duration(cfg.Cooldown))
	if s.State != nil {
		s.State.MakerAcquisitionCooldownUntil = s.makerAcquisitionCooldownUntil
		bbgo.Sync(ctx, s)
	}
	s.makerAcquisitionDeficitSince = time.Time{}
	s.makerAcquisitionDeficitAnchorMid = 0
	s.makerAskSince = time.Time{}
	s.makerAskAnchorMid = 0
	s.lastMakerQuoteAt = now
	s.makerTradingWindowStartedAt = time.Time{}
	s.makerTradingWindowEndsAt = time.Time{}
	log.WithFields(logrus.Fields{
		"quantity": quantity, "price": price, "inventoryTarget": inventoryTarget,
		"age": decision.Age, "adverseMoveBps": decision.AdverseMoveBps,
		"upProbabilityLower":        decision.UpProbabilityLower,
		"upRateLowerPerHour":        decision.UpRateLowerPerHour,
		"downRateUpperPerHour":      decision.DownRateUpperPerHour,
		"passiveBidFillProbability": decision.PassiveBidFillProbability,
		"makerExitFillProbability":  decision.MakerExitFillProbability,
		"expectedDriftLowerBps":     decision.ExpectedDriftLowerBps,
		"passiveWaitValueBps":       decision.PassiveWaitValueBps,
		"iocValueBps":               decision.IOCValueBps,
		"iocImprovementBps":         decision.IOCImprovementBps,
		"riskBps":                   decision.RiskBps, "cooldown": cfg.Cooldown,
	}).Warn("acquisition reset IOC buy submitted")
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
	s.model.Observe(now, false)
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

// makerQuoteBalances separates funds available immediately from funds that
// become available when this strategy replaces its own active maker orders.
// Reservations belonging to other strategies or manual orders stay locked.
type makerQuoteBalances struct {
	AvailableBase  fixedpoint.Value
	QuoteableBase  fixedpoint.Value
	AvailableQuote fixedpoint.Value
	QuoteableQuote fixedpoint.Value
}

func calculateMakerQuoteBalances(base, quote types.Balance, activeOrders types.OrderSlice) makerQuoteBalances {
	ownBaseReservation := fixedpoint.Zero
	ownQuoteReservation := fixedpoint.Zero
	for _, order := range activeOrders {
		if !types.IsActiveOrder(order) {
			continue
		}
		remaining := order.GetRemainingQuantity()
		if remaining.Sign() <= 0 {
			continue
		}
		switch order.Side {
		case types.SideTypeSell:
			ownBaseReservation = ownBaseReservation.Add(remaining)
		case types.SideTypeBuy:
			if order.Price.Sign() > 0 {
				ownQuoteReservation = ownQuoteReservation.Add(order.Price.Mul(remaining))
			}
		}
	}

	// The exchange-reported Locked amount is authoritative. This cap prevents
	// a stale local order update from reclaiming more than is actually locked.
	reclaimableBase := fixedpoint.Min(base.Locked, ownBaseReservation)
	if reclaimableBase.Sign() < 0 {
		reclaimableBase = fixedpoint.Zero
	}
	reclaimableQuote := fixedpoint.Min(quote.Locked, ownQuoteReservation)
	if reclaimableQuote.Sign() < 0 {
		reclaimableQuote = fixedpoint.Zero
	}

	return makerQuoteBalances{
		AvailableBase:  base.Available,
		QuoteableBase:  base.Available.Add(reclaimableBase),
		AvailableQuote: quote.Available,
		QuoteableQuote: quote.Available.Add(reclaimableQuote),
	}
}

func (s *Strategy) makerQuoteBalances() makerQuoteBalances {
	if s.session == nil || s.session.GetAccount() == nil {
		return makerQuoteBalances{}
	}
	account := s.session.GetAccount()
	base, _ := account.Balance(s.Market.BaseCurrency)
	quote, _ := account.Balance(s.Market.QuoteCurrency)

	var activeOrders types.OrderSlice
	if s.executor != nil && s.executor.ActiveMakerOrders() != nil {
		activeOrders = s.executor.ActiveMakerOrders().Orders()
	}
	return calculateMakerQuoteBalances(base, quote, activeOrders)
}

// pairEquityQuote returns the symbol's total quote-equivalent capital. It is
// deliberately based on total (available + locked) balances for sizing; maker
// submission separately reclaims only reservations owned by replaceable orders.
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
