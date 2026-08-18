package gammacapture

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
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
	EntryEligibleUntil               time.Time            `json:"entryEligibleUntil"`
	EntrySignalPrice                 fixedpoint.Value     `json:"entrySignalPrice"`
	EntryNeedsRetrace                bool                 `json:"entryNeedsRetrace"`
	CooldownUntil                    time.Time            `json:"cooldownUntil"`
	MakerResetCooldownUntil          time.Time            `json:"makerResetCooldownUntil,omitempty"`
	MakerAcquisitionCooldownUntil    time.Time            `json:"makerAcquisitionCooldownUntil,omitempty"`
	LastDecision                     string               `json:"lastDecision"`
	Runtime                          RuntimeState         `json:"runtime"`
	TrendSamples                     []TrendSample        `json:"trendSamples,omitempty"`
	LastReferenceTime                time.Time            `json:"lastReferenceTime,omitempty"`
	LastMarketTradeID                uint64               `json:"lastMarketTradeID,omitempty"`
	OnlineArrival                    *OnlineArrivalState  `json:"onlineArrival,omitempty"`
	MacroInventory                   *MacroInventoryState `json:"macroInventory,omitempty"`
	ModelCheckpoint                  *ModelCheckpoint     `json:"modelCheckpoint,omitempty"`
	MakerPostFill                    *MakerPostFillState  `json:"makerPostFill,omitempty"`
	FastInventoryAnchorSet           bool                 `json:"fastInventoryAnchorSet,omitempty"`
	FastInventoryAnchorBase          float64              `json:"fastInventoryAnchorBase,omitempty"`
	LastFastTargetExecutionModelAt   time.Time            `json:"lastFastTargetExecutionModelAt,omitempty"`
	LastFastTargetExecutionDirection int                  `json:"lastFastTargetExecutionDirection,omitempty"`
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

	session                        *bbgo.ExchangeSession
	executor                       *bbgo.GeneralOrderExecutor
	model                          *IntensityModel
	fastModel                      *IntensityModel // primary/legacy alias
	fastModels                     map[time.Duration]*IntensityModel
	fastEvidence                   *FastEvidenceModel // primary/legacy alias
	fastEvidenceModels             map[time.Duration]*FastEvidenceModel
	makerBOCPD45                   *BOCPD45Model
	makerAsymmetricOscillationRisk *AsymmetricOscillationRiskModel
	makerDirectionModel            *DecayedDirectionModel // primary/legacy alias
	makerDirectionModels           map[time.Duration]*DecayedDirectionModel
	referenceMu                    sync.Mutex
	bookMu                         sync.RWMutex
	bestBid                        fixedpoint.Value
	bestAsk                        fixedpoint.Value
	bestBookAt                     time.Time
	lastBookTicker                 types.BookTicker
	marketMakerMu                  sync.Mutex
	lastMakerQuoteAt               time.Time
	lastMakerDiagnosticAt          time.Time
	lastMakerBid, lastMakerAsk     fixedpoint.Value
	// Best bid/ask observed when the current quote window was submitted. These
	// are used for adverse-reprice detection; comparing the live BBO directly
	// with our intentionally distant quote would trigger a false reprice.
	lastMakerBestBid, lastMakerBestAsk float64
	lastMakerMid                       float64
	lastMakerImbalance                 float64
	makerQuotedTargetRatio             float64
	makerQuotedFastTargetRatio         float64
	makerQuotedFastReservationBps      float64
	makerQuotedTargetSet               bool
	makerHorizonModel                  MarketMakerHorizonModel
	makerQuoteLifecycleHazard          *QuoteLifecycleHazardModel
	makerMacroInventoryModel           MacroInventoryModel
	makerExecutableCrossingModel       *ExecutableCrossingModel
	makerHorizonDecision               MarketMakerHorizonDecision
	makerLastCheckpointSync            time.Time
	makerCheckpointReplayAfter         time.Time
	makerLastPublicTradeAt             time.Time
	makerStartupPendingTrades          []makerStartupTrade
	makerCheckpointCaptureFiles        map[string]captureFileCheckpoint
	makerMacroInventorySyncPending     bool
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
	makerTerminalFillSequence          atomic.Uint64
	makerTerminalFillObservedAt        atomic.Int64
	// makerHeadroomCancelAt prevents a delayed ActiveOrderBook cancel update
	// from turning an inventory-headroom correction into a cancel/submit loop.
	// A headroom correction cancels first and waits for the order book to
	// reflect the cancellation before a replacement quote is submitted.
	makerHeadroomCancelAt          time.Time
	makerLastAcquisitionStartLogAt time.Time
	makerLastNoSubmissionLogAt     time.Time
	makerReplacementRetryAfter     time.Time
	makerNoOrderReferenceBid       float64
	makerNoOrderReferenceAsk       float64
	makerLastGateLogAt             time.Time
	makerLastBookLogAt             time.Time
	makerLastPipelineLogAt         time.Time
	makerLastBOCPD45LogAt          time.Time
	makerLastAsymmetricRiskLogAt   time.Time
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
		s.makerExecutableCrossingModel = NewExecutableCrossingModel(s.Symbol, s.Barrier, s.Intensity)
		s.makerQuoteLifecycleHazard = NewQuoteLifecycleHazardModel(s.MarketMaker.QuoteLifecycleAction.Hazard)
		if s.MarketMaker.MacroInventory.Enabled && s.State.MacroInventory == nil {
			s.State.MacroInventory = &MacroInventoryState{}
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
		s.makerBOCPD45 = nil
		s.makerDirectionModel = nil
		s.makerDirectionModels = nil
		s.fastEvidence = nil
		s.fastEvidenceModels = nil
		s.makerHorizonTouchModel = nil
		s.makerQuoteLifecycleHazard = nil
		s.makerExecutableCrossingModel = nil
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
		if s.MarketMaker.Enabled && session.Exchange != nil && session.Exchange.Name() == types.ExchangeBinance {
			if err := s.restoreAndWarmMakerModelsFromBinanceCapture(now); err != nil {
				return fmt.Errorf("restore/warm market-maker models: %w", err)
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
	if s.MarketMaker.Enabled {
		s.executor.ActiveMakerOrders().OnFilled(func(order types.Order) {
			if !isOwnedMarketMakerOrder(order) {
				return
			}
			s.makerTerminalFillObservedAt.Store(time.Now().UnixNano())
			s.makerTerminalFillSequence.Add(1)
		})
	}
	s.executor.Bind()
	if s.MarketMaker.Enabled && s.Environment != "backtest" && s.Environment != "replay" &&
		!s.makerCheckpointReplayAfter.IsZero() && s.State.ModelCheckpoint != nil {
		// Persist the freshly rebuilt/advanced checkpoint before stale orders are
		// reconciled. A crash during order recovery must not force the next start
		// to repeat the complete historical warmup.
		bbgo.Sync(ctx, s)
		s.makerLastCheckpointSync = time.Now()
	}
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

func macroReversalPolicyMinRatio(config MarketMakerConfig, decision MacroInventoryDecision) float64 {
	return math.Max(config.InventoryCapitalMinRatio, decision.CapitalFloorRatio)
}

func macroReversalPolicyMaxRatio(config MarketMakerConfig, decision MacroInventoryDecision) float64 {
	return math.Min(config.InventoryCapitalMaxRatio, math.Min(decision.DrawdownCapRatio, decision.CapitalCapRatio))
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

// Checkpoint cadence is independent of the retired online-arrival learner.
const makerCheckpointSyncInterval = 10 * time.Minute

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
		if s.State != nil && s.State.FastInventoryAnchorSet {
			s.State.FastInventoryAnchorBase = math.Max(0,
				s.State.FastInventoryAnchorBase+positionDelta.Float64())
		}
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
const makerTerminalFillDeferralWindow = 2 * time.Second

func makerTerminalFillDefersReplacement(requestedGeneration, initialSequence, currentSequence uint64, observedAt, now time.Time) bool {
	if requestedGeneration > 0 {
		return false
	}
	if currentSequence != initialSequence {
		return true
	}
	if observedAt.IsZero() {
		return false
	}
	return now.Before(observedAt) || now.Sub(observedAt) <= makerTerminalFillDeferralWindow
}

// gracefulCancelMaker records the current maker order IDs before an intentional
// cancel. The cancel callback can therefore distinguish a normal reprice from
// an external cancel and avoid recursively cancelling/recreating the same quote.
func (s *Strategy) gracefulCancelMaker(ctx context.Context, reason string) error {
	if s.executor == nil {
		return nil
	}
	orders := s.executor.ActiveMakerOrders().Orders()
	return s.gracefulCancelMakerOrders(ctx, reason, orders...)
}

// gracefulCancelMakerOrders is the selective counterpart used when one side
// has approached its executable BBO and must retain queue priority while only
// the stranded side is replaced. An empty slice is a no-op: passing no orders
// to GeneralOrderExecutor.GracefulCancel would otherwise cancel every side.
func (s *Strategy) gracefulCancelMakerOrders(ctx context.Context, reason string, orders ...types.Order) error {
	if s.executor == nil || len(orders) == 0 {
		return nil
	}
	if reason == "" {
		reason = "unspecified"
	}
	s.makerExpectedCancelMu.Lock()
	if s.makerExpectedCancelIDs == nil {
		s.makerExpectedCancelIDs = make(map[uint64]string)
	}
	for _, order := range orders {
		s.makerExpectedCancelIDs[order.OrderID] = reason
	}
	s.makerExpectedCancelMu.Unlock()
	if len(orders) > 0 {
		orderIDs := make([]uint64, 0, len(orders))
		for _, order := range orders {
			orderIDs = append(orderIDs, order.OrderID)
		}
		log.WithFields(logrus.Fields{
			"symbol": s.Symbol, "reason": reason, "orders": len(orders), "orderIDs": orderIDs,
		}).Info("market-maker cancel requested")
	}
	startedAt := time.Now()
	err := s.executor.GracefulCancel(ctx, orders...)
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
	if s.State != nil {
		s.State.MakerPostFill = &MakerPostFillState{
			Side: trade.Side, Price: trade.Price.Float64(),
			Quantity: trade.Quantity.Float64(), At: now,
		}
	}
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
	gapBefore := !s.State.LastReferenceTime.IsZero() && now.Sub(s.State.LastReferenceTime) >= marketMakerHorizonGapThreshold
	s.State.LastReferenceTime = now
	s.model.Observe(now, gapBefore)
	s.observeFastModelExposure(now, gapBefore)
	if gapBefore {
		// A websocket/startup gap is missing path data, not evidence that price
		// crossed every intervening barrier. Restart the reference at this BBO.
		s.State.Engine.Reset(price)
		return s.model.Snapshot(now)
	}
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
	if (s.fastEvidence == nil && len(s.fastEvidenceModels) == 0 && !s.MarketMaker.VolumeProfile.Enabled) || trade.Symbol != s.Symbol {
		return
	}
	at := trade.Time.Time()
	if s.Environment != "backtest" && s.Environment != "replay" {
		// BBO has only a local receive timestamp. Use the same causal arrival
		// clock for public trades so live and capture warmup cannot disagree due
		// to exchange/local clock skew.
		at = time.Now()
	} else if at.IsZero() {
		return
	}
	if at.After(s.makerLastPublicTradeAt) {
		s.makerLastPublicTradeAt = at
	}
	if s.fastEvidence != nil || len(s.fastEvidenceModels) > 0 {
		s.observeFastEvidenceTrade(at, trade)
	}
	if s.MarketMaker.VolumeProfile.Enabled {
		s.makerHorizonModel.ObservePublicTrade(
			at, trade.Price.Float64(), trade.Quantity.Float64(),
			trade.Side != types.SideTypeSell && (trade.Side == types.SideTypeBuy || trade.IsBuyer),
			s.MarketMaker)
	}
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

func shouldLogBOCPD45Status(last, now time.Time) bool {
	return last.IsZero() || now.Before(last) || now.Sub(last) >= time.Minute
}

func (s *Strategy) logBOCPD45Status(now time.Time) {
	if s.makerBOCPD45 == nil || !shouldLogBOCPD45Status(s.makerLastBOCPD45LogAt, now) {
		return
	}
	snapshot := s.makerBOCPD45.Snapshot()
	log.WithFields(logrus.Fields{
		"symbol":                  s.Symbol,
		"calibration":             snapshot.Calibration,
		"ready":                   snapshot.Ready,
		"calibrationReady":        snapshot.CalibrationReady,
		"calibrationSamples":      snapshot.CalibrationSamples,
		"maturedLabels":           snapshot.MaturedLabels,
		"rawUpProbability":        snapshot.RawUpProbability,
		"calibratedUpProbability": snapshot.UpProbability,
		"direction":               snapshot.Direction,
		"confidence":              snapshot.Confidence,
		"changeProbability":       snapshot.ChangeProbability,
		"posteriorVariance":       snapshot.PosteriorVariance,
		"bidChanges":              snapshot.BidChanges,
		"askChanges":              snapshot.AskChanges,
		"pendingMaturesAt":        snapshot.PendingMaturesAt,
	}).Info("BOCPD45 calibration status")
	s.makerLastBOCPD45LogAt = now
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
	if observeEvidence {
		s.drainMakerStartupTrades(now, s.MarketMaker)
	}
	terminalFillSequenceAtStart := s.makerTerminalFillSequence.Load()
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
	inventoryBase := quoteBalances.TotalBase.Float64()
	quoteableQuote := quoteBalances.QuoteableQuote
	mid := (ticker.Buy.Float64() + ticker.Sell.Float64()) / 2
	quoteConfig, feeSource := marketMakerConfigWithSessionFees(s.MarketMaker, s.session)
	fastRiskAversion := fastRiskAversionOrDefault(quoteConfig, quoteConfig.FastRiskAversion)
	if observeEvidence {
		s.makerHorizonModel.ObserveBookWithSizes(
			now,
			ticker.Buy.Float64(), ticker.BuySize.Float64(),
			ticker.Sell.Float64(), ticker.SellSize.Float64(),
			quoteConfig)
		if quoteConfig.MacroInventory.Enabled {
			s.makerMacroInventoryModel.ObserveBBO(now, mid, ticker.Buy.Float64(), ticker.Sell.Float64(), false, quoteConfig.MacroInventory)
			s.makerExecutableCrossingModel.Observe(now, ticker.Buy.Float64(), ticker.Sell.Float64(), false)
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
		if s.makerBOCPD45 != nil {
			s.makerBOCPD45.Observe(now, ticker.Buy.Float64(), ticker.Sell.Float64(), false)
			s.logBOCPD45Status(now)
		}
		s.observeFastDriftModels(now, ticker, false, quoteConfig)
	} else if s.model != nil {
		// A fill-triggered refresh reuses the last real BBO. Snapshot the model
		// without advancing crossing counts or their timestamps.
		modelSnapshot = s.model.Snapshot(now)
	}
	if observeEvidence && (s.makerLastCheckpointSync.IsZero() ||
		now.Sub(s.makerLastCheckpointSync) >= makerCheckpointSyncInterval) {
		if err := s.prepareModelCheckpoint(now); err != nil {
			log.WithError(err).WithField("symbol", s.Symbol).Warn("prepare gamma-capture model checkpoint failed")
		}
		bbgo.Sync(ctx, s)
		s.makerLastCheckpointSync = now
	}
	// A completed-path no-order action is leased until the next model update.
	// Continue ingesting BBO/trade evidence above, but do not rebuild the same
	// expensive optimizer state on every book tick while the exchange book is
	// empty. Fill generations and resting-order safety paths remain immediate.
	if makerEmptyBookRetryPending(
		now, s.makerReplacementRetryAfter,
		s.executor.ActiveMakerOrders().NumOfOrders(), fillRebalanceGeneration,
		s.makerNoOrderReferenceBid, s.makerNoOrderReferenceAsk,
		ticker.Buy.Float64(), ticker.Sell.Float64(), quoteConfig.RefreshMoveBps,
	) {
		return
	}
	preSelectionPairEquityJPY := s.pairEquityQuote(mid)
	preSelectionExecutableNotionalJPY := makerMinimumExecutableNotional(
		s.Market, ticker.Buy, ticker.Sell)
	selectedHorizonDecision := s.makerHorizonModel.UpdateForBookAdaptiveVolatilityWithMarginalBuy(
		now, quoteConfig, ticker.Buy.Float64(), ticker.Sell.Float64(),
		FastHorizonMarginalBuyInput{
			CurrentInventoryNotionalJPY: inventoryBase * mid,
			TargetInventoryNotionalJPY:  quoteConfig.InventoryCapitalTargetRatio * preSelectionPairEquityJPY,
			HardMinInventoryNotionalJPY: quoteConfig.InventoryCapitalMinRatio * preSelectionPairEquityJPY,
			HardMaxInventoryNotionalJPY: quoteConfig.InventoryCapitalMaxRatio * preSelectionPairEquityJPY,
			PosteriorInventoryTarget:    quoteConfig.PosteriorInventoryTarget,
			PairEquityJPY:               preSelectionPairEquityJPY,
			MarginalBuyNotionalJPY:      preSelectionExecutableNotionalJPY,
			AvailableBuyCapitalJPY:      quoteableQuote.Float64(),
			MarginalSellNotionalJPY:     preSelectionExecutableNotionalJPY,
			AvailableSellInventoryJPY:   base.Float64() * mid,
			RiskAversion:                fastRiskAversion,
			ConfidenceZScore:            quoteConfig.InventoryRiskZScore,
		})
	selectedHorizon := time.Duration(selectedHorizonDecision.HorizonSeconds) * time.Second
	if selectedHorizon <= 0 {
		selectedHorizon = time.Duration(quoteConfig.MinTradingWindow)
	}
	preliminarySelectedHorizon := selectedHorizon
	asymmetricRiskDecision := s.observeAsymmetricOscillationRisk(
		now, ticker.Buy.Float64(), ticker.Sell.Float64(), selectedHorizon, false)
	if quoteConfig.AsymmetricOscillationRisk.Enabled &&
		!quoteConfig.AsymmetricOscillationRisk.ShadowOnly &&
		asymmetricRiskDecision.Enabled && asymmetricRiskDecision.RiskMultiplier > 0 {
		fastRiskAversion *= asymmetricRiskDecision.RiskMultiplier
	}
	adaptiveFast := s.adaptiveFastSnapshotForWindow(now, selectedHorizon)
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
	bocpd45 := BOCPD45Snapshot{}
	if s.makerBOCPD45 != nil {
		bocpd45 = s.makerBOCPD45.Snapshot()
		if bocpd45.Ready {
			fastConfidence := math.Max(0, math.Min(1,
				fastInference.DirectionConfidence*directionCoverage))
			combinedConfidence := fastConfidence + bocpd45.Confidence
			if combinedConfidence > 0 {
				direction = (direction*fastConfidence +
					bocpd45.Direction*bocpd45.Confidence) / combinedConfidence
			}
		}
	}
	imbalance := bookImbalance(ticker)
	fastDriftBBOStateTag, _ := s.makerHorizonModel.FastDriftBBOStateTag(selectedFastWindow)
	fastDrift := s.makerHorizonModel.FastDriftDecision(
		selectedFastWindow,
		FastDriftFeatures{
			Direction: rawFastDirection, BookImbalance: imbalance,
			BBOStateTag: fastDriftBBOStateTag,
		})
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
	// Statistical horizon selection and exchange-order lifetime are separate.
	// The model horizon determines the quote distribution; the final executable
	// side prices below determine when each resting order is reviewed. A review
	// boundary is not itself permission to cancel an otherwise valid quote.
	horizonDistance := func(selected time.Duration) (MarketMakerHorizonDecision, float64) {
		decision := s.makerHorizonModel.DecisionForHorizon(
			now, quoteConfig, effectiveVolatilityBps, ticker.Buy.Float64(), ticker.Sell.Float64(), selected)
		distance := math.Max(decision.BuyTouchDistanceBps, decision.SellTouchDistanceBps)
		if distance <= 0 {
			halfSpread := quoteConfig.HalfSpreadForHorizon(selected, effectiveVolatilityBps)
			buyDistance, sellDistance, _ := neutralMakerTouchDistances(ticker.Buy.Float64(), ticker.Sell.Float64(), halfSpread)
			distance = math.Max(buyDistance, sellDistance)
		}
		return decision, distance
	}
	horizon := selectedHorizon
	horizonDecision, _ := horizonDistance(horizon)
	orderKeepDecision := quoteConfig.DynamicOrderKeepDecision(horizon, horizonDecision.QuoteDistanceBps, effectiveVolatilityBps)
	buyOrderKeepDecision, sellOrderKeepDecision := MarketMakerOrderKeepDecision{}, MarketMakerOrderKeepDecision{}
	orderReviewDuration := horizon
	s.makerHorizonDecision = horizonDecision
	arrivalBuyTouchDistanceBps, arrivalSellTouchDistanceBps := 0.0, 0.0
	if horizonDecision.DistanceOptimized && horizonDecision.HasSufficientCrossings(quoteConfig.HorizonMinSamples) {
		arrivalBuyTouchDistanceBps = horizonDecision.BuyTouchDistanceBps
		arrivalSellTouchDistanceBps = horizonDecision.SellTouchDistanceBps
	}
	acquisitionDriftBps, acquisitionVolatilityPerSqrtSecBps := acquisitionDriftForecast(fastEvidence, direction, horizon)
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
		recentBuyTouchProbability = horizonDecision.BuyTouchProbability
		recentSellTouchProbability = horizonDecision.SellTouchProbability
		buyFillRate, sellFillRate = horizonDecision.BuyTouchRatePerHour(), horizonDecision.SellTouchRatePerHour()
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
	executableOrderNotionalJPY := makerMinimumExecutableNotional(
		s.Market, ticker.Buy, ticker.Sell)
	if quoteConfig.MacroInventory.Enabled && s.State.MacroInventory == nil {
		s.State.MacroInventory = &MacroInventoryState{}
	}
	wealthPeakJPY := pairEquityJPY
	if s.State != nil && s.State.MacroInventory != nil {
		previousWealthPeakJPY := s.State.MacroInventory.WealthPeakJPY
		s.State.MacroInventory.ObserveWealth(now, pairEquityJPY)
		if s.State.MacroInventory.WealthPeakJPY > previousWealthPeakJPY {
			s.makerMacroInventorySyncPending = true
		}
		wealthPeakJPY = s.State.MacroInventory.WealthPeakJPY
	}
	executableCrossingSnapshot := s.makerExecutableCrossingModel.Snapshot(now)
	macroDecision := quoteConfig.MacroInventory.Decide(&s.makerMacroInventoryModel, MacroInventoryInput{
		Now: now, WealthJPY: pairEquityJPY, WealthPeakJPY: wealthPeakJPY,
		RiskyNotionalJPY:                inventoryBase * mid,
		PriorTargetRatio:                quoteConfig.InventoryCapitalTargetRatio,
		PolicyMinRatio:                  quoteConfig.InventoryCapitalMinRatio,
		PolicyMaxRatio:                  quoteConfig.InventoryCapitalMaxRatio,
		FallbackVolatilityBpsPerSqrtSec: inventoryVolatility * 10_000,
		CrossingSnapshot:                modelSnapshot,
		ExecutableCrossingSnapshot:      executableCrossingSnapshot,
		BarrierWidth:                    s.Barrier.Width,
		CrossingQVRatePerSecond:         math.Pow(modelSnapshot.GammaCaptureVolatility, 2),
		FastVarianceRisk:                s.makerHorizonModel.SideHARVarianceRisk(selectedFastWindow),
		BuyVolatilityBpsPerSqrtSec:      buyEffectiveVolatilityBps,
		SellVolatilityBpsPerSqrtSec:     sellEffectiveVolatilityBps,
		OneWayCostBps:                   quoteConfig.MakerFeeBps + quoteConfig.AdverseSelectionBps,
		ConfidenceZScore:                quoteConfig.InventoryRiskZScore,
		MinimumExecutableNotionalJPY:    executableOrderNotionalJPY,
		State:                           s.State.MacroInventory,
		LatestClosedBarAt:               s.makerMacroInventoryModel.LatestClosedBarAt(),
	})
	if macroDecision.StateChanged {
		s.makerMacroInventorySyncPending = true
	}
	noTradeInventoryEnabled := macroDecision.Enabled && quoteConfig.MacroInventory.NoTradeRegion.Enabled
	reversalDecision := MacroReversalDecision{
		Reason:                "superseded by QV-time no-trade region",
		BaselineTargetRatio:   macroDecision.NoTrade.AimRatio,
		TargetRatio:           macroDecision.TargetRatio,
		CurrentRiskyWeight:    macroDecision.CurrentRiskyWeight,
		Direction:             macroDecision.NoTrade.Direction,
		AggregateNetEdgeBps:   macroDecision.NoTrade.ExecutionEdgeBps(selectedFastWindow),
		SignalForecastHorizon: selectedFastWindow,
	}
	if quoteConfig.MacroInventory.Enabled && !noTradeInventoryEnabled {
		reversalPolicyMinRatio := macroReversalPolicyMinRatio(quoteConfig, macroDecision)
		reversalPolicyMaxRatio := macroReversalPolicyMaxRatio(quoteConfig, macroDecision)
		reversalDecision = quoteConfig.MacroInventory.DecideReversal(&s.makerMacroInventoryModel, MacroReversalInput{
			Now: now, BaselineTargetRatio: macroDecision.TargetRatio,
			CurrentRiskyWeight: macroDecision.CurrentRiskyWeight,
			PolicyMinRatio:     reversalPolicyMinRatio,
			PolicyMaxRatio:     reversalPolicyMaxRatio,
			RoundTripCostBps: 2*quoteConfig.MakerFeeBps +
				2*quoteConfig.AdverseSelectionBps + quoteConfig.MinimumNetEdgeBps,
			ConfidenceZScore:                quoteConfig.InventoryRiskZScore,
			RiskAversion:                    quoteConfig.MacroInventory.RiskAversion,
			FallbackVolatilityBpsPerSqrtSec: inventoryVolatility * 10_000,
		})
		if s.State != nil && s.State.MacroInventory != nil {
			var regimeStateChanged bool
			reversalDecision, regimeStateChanged = s.State.MacroInventory.ApplyRegimeLease(
				now, reversalPolicyMinRatio, reversalPolicyMaxRatio, reversalDecision)
			if regimeStateChanged {
				s.makerMacroInventorySyncPending = true
			}
		}
	}
	effectiveInventoryTargetRatio := reversalDecision.TargetRatio
	macroLatestClosedBarAt := s.makerMacroInventoryModel.LatestClosedBarAt()
	if s.makerMacroInventorySyncPending &&
		(s.makerLastCheckpointSync.IsZero() || now.Sub(s.makerLastCheckpointSync) >= makerCheckpointSyncInterval) {
		bbgo.Sync(ctx, s)
		s.makerLastCheckpointSync = now
		s.makerMacroInventorySyncPending = false
	}
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
	inventoryVariationHorizon, inventoryVariationHorizonSource := quoteConfig.InventoryVariationHorizon(
		selectedFastWindow, horizon)
	fastInventoryBand := InventoryBand{
		MinInventory: quoteConfig.InventoryTarget - quoteConfig.InventoryLimit,
		Target:       quoteConfig.InventoryTarget,
		Limit:        quoteConfig.InventoryLimit,
		MaxInventory: quoteConfig.InventoryTarget + quoteConfig.InventoryLimit,
	}
	macroInventoryBand := fastInventoryBand
	inventoryVariation := InventoryVariationDecision{
		ExpectedTargetRatio: quoteConfig.InventoryCapitalTargetRatio,
		LowerRatio:          quoteConfig.InventoryCapitalMinRatio,
		UpperRatio:          quoteConfig.InventoryCapitalMaxRatio,
	}
	if quoteConfig.AutoInventoryLimit {
		fastInventoryBand = quoteConfig.DynamicInventoryBandWithCapital(
			mid, inventoryVolatility*10_000, inventoryVariationHorizon, pairEquityJPY)
		if macroDecision.Enabled {
			if noTradeInventoryEnabled {
				inventoryVariation = InventoryVariationDecision{
					Enabled:             true,
					ExpectedTargetRatio: macroDecision.NoTrade.AimRatio,
					LowerRatio:          macroDecision.NoTrade.LowerRatio,
					UpperRatio:          macroDecision.NoTrade.UpperRatio,
					HalfWidthJPY: math.Max(macroDecision.NoTrade.BuyHalfWidthRatio,
						macroDecision.NoTrade.SellHalfWidthRatio) * pairEquityJPY,
					ExecutableOrderNotionalJPY: executableOrderNotionalJPY,
				}
				macroInventoryBand = quoteConfig.InventoryBandFromPolicyRatios(
					mid, pairEquityJPY, macroDecision.NoTrade.LowerRatio,
					macroDecision.NoTrade.ExecutionTargetRatio, macroDecision.NoTrade.UpperRatio)
			} else {
				// Near the expected target, target-centered level allocation is below
				// the risk-sized quote in sparse JPY books and the exchange lattice
				// floors it to one executable order. Use that realized jump scale for
				// compound-Poisson variance; the unconstrained risk notional is not an
				// order fill and would create a self-inflating inventory band.
				inventoryVariation = quoteConfig.ProbabilisticInventoryVariation(
					pairEquityJPY, effectiveInventoryTargetRatio, inventoryVariationHorizon,
					riskSizingBuyFillRate, riskSizingSellFillRate,
					executableOrderNotionalJPY)
				macroInventoryBand = quoteConfig.DynamicInventoryBandWithCapitalPolicyBounds(
					mid, inventoryVolatility*10_000, inventoryVariationHorizon, pairEquityJPY,
					inventoryVariation.LowerRatio,
					inventoryVariation.ExpectedTargetRatio,
					inventoryVariation.UpperRatio)
			}
		} else {
			macroInventoryBand = quoteConfig.DynamicInventoryBandWithCapitalPolicy(
				mid, inventoryVolatility*10_000, inventoryVariationHorizon, pairEquityJPY,
				macroDecision.TargetRatio, macroDecision.CapitalCapRatio)
		}
	}
	inventoryControl := SelectInventoryControl(
		fastInventoryBand, macroInventoryBand,
		noTradeInventoryEnabled && macroDecision.Enabled, macroDecision.NoTrade.Direction)
	inventoryBand := inventoryControl.Band
	longHorizonInventoryAdjustment := inventoryControl.LongHorizonAdjustment
	if pairEquityJPY > 0 {
		effectiveInventoryTargetRatio = inventoryBand.Target * mid / pairEquityJPY
		inventoryVariation.ExpectedTargetRatio = effectiveInventoryTargetRatio
		inventoryVariation.LowerRatio = inventoryBand.MinInventory * mid / pairEquityJPY
		inventoryVariation.UpperRatio = inventoryBand.MaxInventory * mid / pairEquityJPY
	}
	s.makerInventoryBand = inventoryBand
	quoteConfig.InventoryTarget = inventoryBand.Target
	quoteConfig.InventoryLimit = inventoryBand.Limit
	// InventoryActuation is part of the executable Fast quote, not merely a
	// quantity diagnostic.  The previous path initialized the decision to its
	// neutral value and never evaluated the already implemented controller;
	// consequently an over-target account kept the normal 15-bps ask even when
	// the causal arrival posterior said a SELL correction was reachable.  Use
	// the same shortest causal clock as inventory variation and the currently
	// observed side-specific touch rates.  A missing rate fails closed; it must
	// not invent an arrival process during warmup.
	actuationHorizon, actuationHorizonSource := quoteConfig.InventoryActuationHorizon(
		selectedFastWindow, macroDecision.NoTrade.ForecastObservation)
	actuation := InventoryActuation(InventoryActuationInput{
		CurrentInventoryNotionalJPY: inventoryBase * mid,
		TargetInventoryNotionalJPY:  inventoryBand.Target * mid,
		ExpectedFillNotionalJPY:     executableOrderNotionalJPY,
		BuyFillRatePerHour:          buyFillRate,
		SellFillRatePerHour:         sellFillRate,
		RegimeHorizon:               actuationHorizon,
		MomentumSignal:              direction,
	})
	if !actuation.Enabled {
		// Keep this reason distinct from an enabled neutral correction so logs can
		// separate data starvation from a genuinely balanced inventory state.
		if actuation.Reason == "invalid input" {
			actuation.Reason = "long-horizon target integrated into Fast inventory control"
		}
	}
	actuationLevels := 1.0
	targetContraction := 1.0
	// Soft boundaries control expected inventory without deleting the opposite
	// Fast quote. Hard gates normally use configured outer capital ratios; the
	// no-trade path additionally intersects the retained Macro carrying-loss and
	// drawdown bounds.
	hardInventoryBand := quoteConfig.HardInventoryBand(inventoryBand, mid, pairEquityJPY)
	if noTradeInventoryEnabled {
		hardInventoryBand = quoteConfig.InventoryBandFromPolicyRatios(
			mid, pairEquityJPY, macroDecision.CapitalFloorRatio,
			inventoryBand.TargetRatio, macroDecision.CapitalCapRatio)
	}
	fastBuyHoldingRiskHorizon := FastReservationRiskHorizon(
		inventoryVariationHorizon, macroDecision.NoTrade.ForecastObservation)
	fastReservationConfidenceZ := quoteConfig.InventoryRiskZScore
	fastReservation := macroDecision.NoTrade.FastReservation(
		fastBuyHoldingRiskHorizon, fastReservationConfidenceZ)
	if s.makerHorizonTouchModel != nil && horizon > 0 {
		features, ready := s.makerHorizonModel.HorizonTouchFeatures(now)
		touchFeaturesReady = ready
		if ready {
			provisionalPlan := quoteConfig.Quote(MarketMakerQuoteInput{
				MidPrice: mid, BestBid: ticker.Buy.Float64(), BestAsk: ticker.Sell.Float64(),
				VolatilityPerSqrtSec: effectiveVolatilityBps, BuyVolatilityPerSqrtSec: buyQuoteVolatilityBps, SellVolatilityPerSqrtSec: sellQuoteVolatilityBps, TradingHorizonSeconds: horizon.Seconds(),
				ArrivalBuyTouchDistanceBps: arrivalBuyTouchDistanceBps, ArrivalSellTouchDistanceBps: arrivalSellTouchDistanceBps,
				Inventory: inventoryBase, InventoryMin: inventoryBand.MinInventory, InventoryMax: inventoryBand.MaxInventory,
				HardInventoryMin: hardInventoryBand.MinInventory, HardInventoryMax: hardInventoryBand.MaxInventory,
				DirectionSignal: direction, VolumeSignal: volumeSignal, BookImbalance: imbalance,
				FastDrift:                   fastDrift,
				InventoryActuationDirection: actuation.Direction, InventoryActuationStrength: actuation.InwardStrength,
				BuyFillRate: buyFillRate, SellFillRate: sellFillRate, QuoteNotionalBase: dynamicQuoteNotional, AcquisitionDriftBps: acquisitionDriftBps, AcquisitionVolatilityPerSqrtSecBps: acquisitionVolatilityPerSqrtSecBps, AcquisitionHorizonSeconds: horizon.Seconds(), AcquisitionDirection: direction, CanBuy: canBuy, CanSell: canSell,
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
	// Horizon-touch estimation can replace the preliminary crossing rates after
	// its causal feature lookup. Re-run the same controller once, before the
	// final quote is built, so price urgency and quantity projection consume one
	// consistent rate posterior rather than the pre-touch fallback.
	actuation = InventoryActuation(InventoryActuationInput{
		CurrentInventoryNotionalJPY: inventoryBase * mid,
		TargetInventoryNotionalJPY:  inventoryBand.Target * mid,
		ExpectedFillNotionalJPY:     executableOrderNotionalJPY,
		BuyFillRatePerHour:          buyFillRate,
		SellFillRatePerHour:         sellFillRate,
		RegimeHorizon:               actuationHorizon,
		MomentumSignal:              direction,
	})
	if !actuation.Enabled && actuation.Reason == "invalid input" {
		actuation.Reason = "long-horizon target integrated into Fast inventory control"
	}
	if actuation.Enabled {
		actuationLevels = actuation.EffectiveOrderLevels
		targetContraction = actuation.TargetContraction
	}
	plan := quoteConfig.Quote(MarketMakerQuoteInput{
		MidPrice: mid, BestBid: ticker.Buy.Float64(), BestAsk: ticker.Sell.Float64(),
		// GammaCaptureVolatility is an instantaneous log-volatility in
		// fraction/sqrt(second). Quote's input is bps/sqrt(second), and it
		// derives the expected move over the selected first-passage horizon.
		VolatilityPerSqrtSec:        effectiveVolatilityBps,
		BuyVolatilityPerSqrtSec:     buyQuoteVolatilityBps,
		SellVolatilityPerSqrtSec:    sellQuoteVolatilityBps,
		TradingHorizonSeconds:       horizon.Seconds(),
		ArrivalBuyTouchDistanceBps:  arrivalBuyTouchDistanceBps,
		ArrivalSellTouchDistanceBps: arrivalSellTouchDistanceBps,
		Inventory:                   inventoryBase, InventoryMin: inventoryBand.MinInventory, InventoryMax: inventoryBand.MaxInventory,
		HardInventoryMin: hardInventoryBand.MinInventory, HardInventoryMax: hardInventoryBand.MaxInventory,
		DirectionSignal: direction, VolumeSignal: volumeSignal, BookImbalance: imbalance,
		FastDrift:                   fastDrift,
		InventoryActuationDirection: actuation.Direction, InventoryActuationStrength: actuation.InwardStrength,
		BuyFillRate: buyFillRate, SellFillRate: sellFillRate, QuoteNotionalBase: dynamicQuoteNotional,
		AcquisitionDriftBps: acquisitionDriftBps, AcquisitionVolatilityPerSqrtSecBps: acquisitionVolatilityPerSqrtSecBps, AcquisitionHorizonSeconds: horizon.Seconds(), AcquisitionDirection: direction,
		CanBuy: canBuy, CanSell: canSell,
	})
	postFillUtilityDecision := PostFillUtilityDecision{Reason: "no pending fill", Plan: plan}
	postFillStateActive := s.State != nil && s.State.MakerPostFill != nil &&
		!now.Before(s.State.MakerPostFill.At) &&
		now.Sub(s.State.MakerPostFill.At) <= horizon
	if postFillStateActive {
		postFillUtilityDecision = quoteConfig.ApplyPostFillUtility(&s.makerHorizonModel, PostFillUtilityInput{
			Now: now, Fill: *s.State.MakerPostFill, Plan: plan,
			BestBid: ticker.Buy.Float64(), BestAsk: ticker.Sell.Float64(), Mid: mid,
			Horizon: horizon, InventoryBase: inventoryBase,
			InventoryTargetBase: inventoryBand.Target, PairEquityJPY: pairEquityJPY,
			// Price the marginal inventory change of the next executable fill,
			// not the entire dynamic risk budget. The latter can exceed pair
			// equity and incorrectly reverse the sign of a target-restoring fill.
			ExpectedFillNotionalJPY: executableOrderNotionalJPY,
			VolatilityBpsPerSqrtSec: effectiveVolatilityBps,
			RiskAversion:            fastRiskAversion,
		})
		if postFillUtilityDecision.Applied {
			plan = postFillUtilityDecision.Plan
		}
	}

	fastReservationUtility := FastReservationUtilityDecision{
		Reason: "endogenous Fast drift owns reservation center",
	}
	if !plan.FastDriftApplied {
		plan, fastReservationUtility = SelectFastReservationPlan(
			&s.makerHorizonModel, quoteConfig, now, fastBuyHoldingRiskHorizon,
			plan, fastReservation, mid, ticker.Buy.Float64(), ticker.Sell.Float64(),
			executableOrderNotionalJPY, pairEquityJPY, fastRiskAversion)
	}
	appliedFastReservationBps := 0.0
	if fastReservationUtility.Applied {
		appliedFastReservationBps = fastReservation.ReservationShiftBps
	}

	// Use the final asymmetric executable prices and the matching executable
	// side volatility. Buy touch risk follows the ask path; sell touch risk
	// follows the bid path. Review the pair when the earlier side clock resolves,
	// then revalidate in place instead of automatically cancelling it.
	orderReviewDuration = 0
	if plan.AllowBid {
		buyOrderKeepDecision = quoteConfig.DynamicOrderKeepDecision(
			horizon, quoteConfig.OrderKeepDistanceBps(plan.BidTouchDistanceBps), buyQuoteVolatilityBps)
		orderKeepDecision = buyOrderKeepDecision
		orderReviewDuration = buyOrderKeepDecision.Duration
	}
	if plan.AllowAsk {
		sellOrderKeepDecision = quoteConfig.DynamicOrderKeepDecision(
			horizon, quoteConfig.OrderKeepDistanceBps(plan.AskTouchDistanceBps), sellQuoteVolatilityBps)
		if orderReviewDuration <= 0 || sellOrderKeepDecision.Duration < orderReviewDuration {
			orderKeepDecision = sellOrderKeepDecision
			orderReviewDuration = sellOrderKeepDecision.Duration
		}
	}
	if orderReviewDuration <= 0 {
		orderReviewDuration = horizon
	}
	// Re-evaluate the final asymmetric quote selected by the unified reservation
	// shift. The common distance optimizer supplies the spread; inventory and
	// direction redistribute it, so quantity projection must use arrival rates
	// measured at the actual executable prices.
	finalHorizonDecision := s.makerHorizonModel.CrossingDecisionAtSideDistances(
		now, quoteConfig, horizon, plan.BidTouchDistanceBps, plan.AskTouchDistanceBps,
		math.Max(0, math.Log(plan.AskPrice/plan.BidPrice)*10_000))
	if finalHorizonDecision.HasSufficientCrossings(quoteConfig.HorizonMinSamples) {
		finalHorizonDecision.DistanceOptimized = horizonDecision.DistanceOptimized
		horizonDecision = finalHorizonDecision
		s.makerHorizonDecision = finalHorizonDecision
		buyFillRate = finalHorizonDecision.BuyTouchRatePerHour()
		sellFillRate = finalHorizonDecision.SellTouchRatePerHour()
		recentBuyTouchProbability = finalHorizonDecision.BuyTouchProbability
		recentSellTouchProbability = finalHorizonDecision.SellTouchProbability
		sideDistanceSource = finalHorizonDecision.EstimatorSource
	}
	// Price urgency uses the causal rates available before constructing the
	// quote. Quantity can use the final side distances without another price
	// iteration, avoiding a circular quote/rate fixed point on every BBO event.
	quantityActuation := actuation
	hardBuyInventoryHeadroom := inventoryBuyHeadroomNotional(hardInventoryBand, inventoryBase, mid)
	hardSellInventoryHeadroom := inventorySellHeadroomQuantity(hardInventoryBand, inventoryBase)
	fastQuantityCapacity := FastQuantityCapacity(ExposureUtilizationSizingInput{
		ExecutableUnitJPY:                 executableOrderNotionalJPY,
		RiskSizedNotionalJPY:              dynamicQuoteNotional,
		PairEquityJPY:                     pairEquityJPY,
		CurrentInventoryNotionalJPY:       inventoryBase * mid,
		HardLowerInventoryNotionalJPY:     hardInventoryBand.MinInventory * mid,
		HardUpperInventoryNotionalJPY:     hardInventoryBand.MaxInventory * mid,
		AvailableBuyCapitalJPY:            quoteableQuote.Float64(),
		AvailableSellInventoryNotionalJPY: base.Float64() * mid,
	})
	riskUtilizationSizing := fastQuantityCapacity.Exposure
	projectionTargetBase := inventoryBand.Target
	posteriorInventoryTarget := PosteriorInventoryTargetDecision{
		Reason: "disabled", TargetBase: projectionTargetBase, UpProbability: 0.5,
	}
	dynamicInventoryAim := DynamicInventoryAimDecision{
		Reason: "disabled", GateReason: "disabled",
		CurrentInventoryRatio: inventoryBase * mid / math.Max(1, pairEquityJPY),
		PolicyTargetRatio:     inventoryBand.Target * mid / math.Max(1, pairEquityJPY),
		HardMinimumRatio:      hardInventoryBand.MinInventory * mid / math.Max(1, pairEquityJPY),
		HardMaximumRatio:      hardInventoryBand.MaxInventory * mid / math.Max(1, pairEquityJPY),
		AimTargetRatio:        projectionTargetBase,
		AdjustedTargetRatio:   projectionTargetBase,
	}
	needInventoryPathStats := quoteConfig.PosteriorInventoryTarget || quoteConfig.DynamicInventoryAim.Enabled
	if needInventoryPathStats {
		pathStats := s.makerHorizonModel.JointPathPayoffStatistics(
			now, quoteConfig, horizon, plan.BidTouchDistanceBps, plan.AskTouchDistanceBps)
		if quoteConfig.PosteriorInventoryTarget {
			posteriorInventoryTarget = PosteriorInventoryRiskTarget(
				inventoryBand.Target, hardInventoryBand.MinInventory,
				hardInventoryBand.MaxInventory, pathStats)
			projectionTargetBase = posteriorInventoryTarget.TargetBase
		}
		if quoteConfig.DynamicInventoryAim.Enabled {
			predictiveVariance := pathStats.InventoryTarget.InventoryDirectionalVarBps2
			inventorySamples := pathStats.InventoryTargetEffectiveSamples
			if inventorySamples <= 0 {
				inventorySamples = pathStats.EffectiveSamples
			}
			if inventorySamples > 0 {
				predictiveVariance += predictiveVariance / inventorySamples
			}
			dynamicInventoryAim = EvaluateDynamicInventoryAim(
				quoteConfig.DynamicInventoryAim,
				DynamicInventoryAimInput{
					CurrentInventoryRatio:   inventoryBase * mid / math.Max(1, pairEquityJPY),
					PolicyTargetRatio:       inventoryBand.Target * mid / math.Max(1, pairEquityJPY),
					HardMinimumRatio:        hardInventoryBand.MinInventory * mid / math.Max(1, pairEquityJPY),
					HardMaximumRatio:        hardInventoryBand.MaxInventory * mid / math.Max(1, pairEquityJPY),
					GrossInventoryReturnBps: pathStats.InventoryTarget.InventoryDirectionalMeanBps,
					PredictiveVarianceBps2:  predictiveVariance,
					EffectiveSamples:        inventorySamples,
					ForecastHorizon:         horizon,
					ExecutionHorizon:        horizon,
					AdjustmentPeriod:        time.Duration(quoteConfig.HorizonUpdateInterval),
					RiskAversion:            math.Max(fastRiskAversion, 1e-6),
					OneWayExecutionCostBps:  quoteConfig.MakerFeeBps + quoteConfig.AdverseSelectionBps,
					EvidencePriorSamples:    quoteConfig.DynamicInventoryAim.EvidencePriorSamples,
				},
			)
			// Keep the existing Fast execution/lifecycle structures as consumers of
			// one target. A dynamic aim never creates an independent quantity or
			// price gate; it only replaces the candidate inventory target.
			if dynamicInventoryAim.GatePassed && !quoteConfig.DynamicInventoryAim.ShadowOnly {
				projectionTargetBase = dynamicInventoryAim.AdjustedTargetRatio * pairEquityJPY / mid
			}
			if dynamicInventoryAim.GatePassed && !quoteConfig.DynamicInventoryAim.ShadowOnly {
				posteriorInventoryTarget.Enabled = true
				posteriorInventoryTarget.TargetBase = projectionTargetBase
				posteriorInventoryTarget.InventoryReturnMean = dynamicInventoryAim.NetReturnBps
				posteriorInventoryTarget.InventoryPredictiveSD = dynamicInventoryAim.PredictiveStdDevBps
				posteriorInventoryTarget.DirectionConfidence = dynamicInventoryAim.SignalStrength
			}
		}
	}
	fastTargetSwitching := FastTargetSwitchingDecision{
		Reason:              "Fast target switching has no previous quoted target",
		CandidateTargetBase: projectionTargetBase, SelectedTargetBase: projectionTargetBase,
	}
	if quoteConfig.DynamicInventoryAim.Enabled && dynamicInventoryAim.GatePassed && !quoteConfig.DynamicInventoryAim.ShadowOnly {
		// DynamicInventoryAim already applies the single fee/risk partial-
		// adjustment gate. Running the older target-switching impulse gate after
		// it would charge the same inventory transition twice and could strand the
		// target at the previous quote.
		fastTargetSwitching.Reason = "superseded by dynamic inventory aim"
	} else if quoteConfig.FastTargetSwitching.Enabled && s.makerQuotedTargetSet {
		previousTargetBase := s.makerQuotedFastTargetRatio * pairEquityJPY / mid
		fastTargetSwitching = EvaluateFastTargetSwitching(
			quoteConfig.FastTargetSwitching,
			FastTargetSwitchingInput{
				CandidateTargetBase:  projectionTargetBase,
				PreviousTargetBase:   previousTargetBase,
				CurrentInventoryBase: inventoryBase,
				HardMinimumBase:      hardInventoryBand.MinInventory,
				HardMaximumBase:      hardInventoryBand.MaxInventory,
				MidPrice:             mid, PairEquityJPY: pairEquityJPY,
				InventoryReturnMeanBps:      posteriorInventoryTarget.InventoryReturnMean,
				InventoryReturnPredictiveSD: posteriorInventoryTarget.InventoryPredictiveSD,
				RiskAversion:                fastRiskAversion,
				OneWayExecutionCostBps:      quoteConfig.MakerFeeBps + quoteConfig.AdverseSelectionBps,
			})
		if !quoteConfig.FastTargetSwitching.ShadowOnly {
			projectionTargetBase = fastTargetSwitching.SelectedTargetBase
		}
	}
	projectionLowerNotionalJPY, projectionUpperNotionalJPY :=
		SymmetricInventoryProjectionBounds(
			projectionTargetBase*mid,
			hardInventoryBand.MinInventory*mid,
			hardInventoryBand.MaxInventory*mid)
	// Fast supplies the gross risk budget and the sole side allocator. Choose
	// bid/ask notionals whose fill-weighted expected inventory is centered on
	// the one-sided unified target and whose z-score interval remains inside the
	// global hard inventory band. Work in mid-marked inventory notionals so different bid and
	// ask execution prices do not bias the expected-position equation.
	projectionInput := ProbabilityCenteredQuoteInput{
		CurrentInventoryNotionalJPY: inventoryBase * mid,
		TargetInventoryNotionalJPY:  projectionTargetBase * mid,
		LowerInventoryNotionalJPY:   projectionLowerNotionalJPY,
		UpperInventoryNotionalJPY:   projectionUpperNotionalJPY,
		BuyFillRatePerHour:          buyFillRate,
		SellFillRatePerHour:         sellFillRate,
		Horizon:                     inventoryVariationHorizon,
		ConfidenceZScore:            quoteConfig.InventoryRiskZScore,
		FastBuyRestraint:            math.Max(0, -direction),
		FastSellRestraint:           math.Max(0, direction),
		TargetContraction:           targetContraction,
	}
	jointFastDirection := direction
	if plan.FastDriftApplied {
		// Direction and BBO imbalance already determine the learned reservation
		// center. Quantity reacts to the resulting final touch probabilities and
		// must not apply the raw direction as a second restraint.
		projectionInput.FastBuyRestraint = 0
		projectionInput.FastSellRestraint = 0
		jointFastDirection = 0
	}
	if minimum, ok := makerMinimumExecutableBuyCapacity(
		s.Market, fixedpoint.NewFromFloat(plan.BidPrice)); ok && plan.BidPrice > 0 {
		projectionInput.MinBuyNotionalJPY = minimum.Float64() * mid / plan.BidPrice
	}
	if minimum, ok := makerMinimumExecutableQuantity(
		s.Market, fixedpoint.NewFromFloat(plan.AskPrice)); ok {
		projectionInput.MinSellNotionalJPY = minimum.Float64() * mid
	}
	if plan.AllowBid && plan.BidPrice > 0 {
		projectionInput.FastBuyNotionalJPY = plan.BidQuoteNotional * mid / plan.BidPrice
		bidPrice := fixedpoint.NewFromFloat(plan.BidPrice)
		modelCap := fixedpoint.NewFromFloat(fastQuantityCapacity.BaselineBuyCapJPY * plan.BidPrice / mid)
		hardCap := fixedpoint.NewFromFloat(
			inventoryBuyHeadroomNotional(hardInventoryBand, inventoryBase, plan.BidPrice))
		executableCap := makerBuyInventoryCapacity(s.Market, bidPrice, modelCap, hardCap)
		maxBuyNotionalJPY := math.Min(
			quoteableQuote.Float64()*mid/plan.BidPrice,
			executableCap.Float64()*mid/plan.BidPrice)
		// Min and max originate from the same fixed-point venue capacity, but
		// independent quote-to-mid conversions can differ by a few ulps. Once
		// both the balance and the capped capacity pass the exchange BUY filter,
		// align them to the same lattice point instead of treating Max < Min as
		// an authoritative zero.
		if makerBidEligible(s.Market, bidPrice, quoteableQuote, executableCap) {
			maxBuyNotionalJPY = math.Max(maxBuyNotionalJPY, projectionInput.MinBuyNotionalJPY)
		}
		projectionInput.MaxBuyNotionalJPY = maxBuyNotionalJPY
	}
	if plan.AllowAsk && plan.AskPrice > 0 {
		projectionInput.FastSellNotionalJPY = plan.AskQuoteNotional * mid / plan.AskPrice
		modelSellCap := fastQuantityCapacity.BaselineSellCapJPY / mid
		executableCap := makerSellInventoryCapacity(
			s.Market, fixedpoint.NewFromFloat(plan.AskPrice),
			fixedpoint.NewFromFloat(modelSellCap),
			fixedpoint.NewFromFloat(hardSellInventoryHeadroom))
		maxSellNotionalJPY := math.Min(
			base.Float64()*mid, executableCap.Float64()*mid)
		if _, capacityOK := s.Market.GreaterThanMinimalOrderQuantity(
			types.SideTypeSell, fixedpoint.NewFromFloat(plan.AskPrice), executableCap,
		); capacityOK && base.Compare(executableCap) >= 0 {
			maxSellNotionalJPY = math.Max(
				maxSellNotionalJPY, projectionInput.MinSellNotionalJPY)
		}
		projectionInput.MaxSellNotionalJPY = maxSellNotionalJPY
	}
	jointMaxBuyNotionalJPY := math.Min(
		quoteableQuote.Float64()*mid/plan.BidPrice,
		fastQuantityCapacity.PathModelBuyCapJPY)
	jointMaxSellNotionalJPY := math.Min(
		base.Float64()*mid, fastQuantityCapacity.PathModelSellCapJPY)
	probabilityProjection := ProbabilityCenteredQuoteDecision{Reason: "disabled"}
	jointQuoteDecision := JointDistanceQuantityDecision{Reason: "disabled", Plan: plan}
	fastValueRejected := false
	if quoteConfig.ProbabilityCenteredQuantity.Enabled {
		probabilityProjection = ProbabilityCenteredQuoteNotionals(projectionInput)
		if quoteConfig.JointDistanceQuantity.Enabled {
			jointProjectionInput := projectionInput
			jointProjectionInput.MaxBuyNotionalJPY = jointMaxBuyNotionalJPY
			jointProjectionInput.MaxSellNotionalJPY = jointMaxSellNotionalJPY
			completionSide := types.SideType("")
			if postFillUtilityDecision.Applied {
				completionSide = postFillUtilityDecision.Side
			}
			jointHorizonCandidates := []time.Duration(nil)
			if quoteConfig.JointDistanceQuantity.JointHorizonSelection {
				jointHorizonCandidates = quoteConfig.TradingHorizons()
			}
			jointQuoteDecision = OptimizeUnifiedFastQuantity(
				&s.makerHorizonModel, quoteConfig, JointDistanceQuantityInput{
					Now: now, Horizon: inventoryVariationHorizon,
					HorizonCandidates: jointHorizonCandidates,
					BestBid:           ticker.Buy.Float64(), BestAsk: ticker.Sell.Float64(),
					MidPrice: mid, BasePlan: plan, Projection: jointProjectionInput,
					FastDirection:                     jointFastDirection,
					ConfidenceZScore:                  quoteConfig.InventoryRiskZScore,
					PairEquityJPY:                     pairEquityJPY,
					RiskAversion:                      fastRiskAversion,
					AvailableBuyCapitalJPY:            quoteableQuote.Float64(),
					AvailableSellInventoryNotionalJPY: base.Float64() * mid,
					CompletionSide:                    completionSide,
				}, projectionInput)
			if jointQuoteDecision.Applied {
				if selected := jointQuoteDecision.Crossing.Horizon; selected > 0 {
					// The preliminary selector already compared complete, horizon-
					// specific evidence bundles. The joint optimizer owns distance and
					// quantity only, so this must remain the same statistical clock.
					horizon = selected
					selectedHorizon = selected
					inventoryVariationHorizon = selected
					inventoryVariationHorizonSource = "joint-posterior-terminal-wealth"
				}
				plan = jointQuoteDecision.Plan
				probabilityProjection = jointQuoteDecision.Projection
				finalHorizonDecision = jointQuoteDecision.Crossing
				horizonDecision = jointQuoteDecision.Crossing
				s.makerHorizonDecision = jointQuoteDecision.Crossing
				buyFillRate = jointQuoteDecision.Crossing.BuyTouchRatePerHour()
				sellFillRate = jointQuoteDecision.Crossing.SellTouchRatePerHour()
				// A farther selected level needs its own first-passage lifetime;
				// retaining the base quote's shorter clock would invalidate the
				// selected completed-window probability.
				orderReviewDuration = 0
				if plan.AllowBid {
					buyOrderKeepDecision = quoteConfig.DynamicOrderKeepDecision(
						inventoryVariationHorizon,
						quoteConfig.OrderKeepDistanceBps(plan.BidTouchDistanceBps),
						buyQuoteVolatilityBps)
					orderKeepDecision = buyOrderKeepDecision
					orderReviewDuration = buyOrderKeepDecision.Duration
				}
				if plan.AllowAsk {
					sellOrderKeepDecision = quoteConfig.DynamicOrderKeepDecision(
						inventoryVariationHorizon,
						quoteConfig.OrderKeepDistanceBps(plan.AskTouchDistanceBps),
						sellQuoteVolatilityBps)
					if orderReviewDuration <= 0 ||
						sellOrderKeepDecision.Duration < orderReviewDuration {
						orderKeepDecision = sellOrderKeepDecision
						orderReviewDuration = sellOrderKeepDecision.Duration
					}
				}
			} else if jointQuoteDecision.AuthoritativeRejection &&
				!quoteConfig.JointDistanceQuantity.ShadowOnly {
				// A completed-path Fast rejection is the optimizer's no-order
				// action. Do not let the probability-only quantity fallback recreate
				// the fee-negative candidate that Fast just removed.
				plan.AllowBid = false
				plan.AllowAsk = false
				plan.BidQuoteNotional = 0
				plan.AskQuoteNotional = 0
				probabilityProjection = ProbabilityCenteredQuoteDecision{
					Enabled: true,
					Reason:  jointQuoteDecision.Reason,
				}
				fastValueRejected = true
			}
		}
	}
	probabilityProjectionUsed := probabilityProjection.Enabled &&
		!quoteConfig.ProbabilityCenteredQuantity.ShadowOnly
	fastQuantityFallbackUsed := quoteConfig.ProbabilityCenteredQuantity.Enabled &&
		!quoteConfig.ProbabilityCenteredQuantity.ShadowOnly && !probabilityProjectionUsed
	unrestrainedFastBuyNotionalJPY := 0.0
	unrestrainedFastSellNotionalJPY := 0.0
	if plan.BidPrice > 0 {
		unrestrainedFastBuyNotionalJPY = plan.BidQuoteNotional * mid / plan.BidPrice
	}
	if plan.AskPrice > 0 {
		unrestrainedFastSellNotionalJPY = plan.AskQuoteNotional * mid / plan.AskPrice
	}
	if probabilityProjectionUsed {
		plan.BidQuoteNotional = probabilityProjection.BuyNotionalJPY * plan.BidPrice / mid
		plan.AskQuoteNotional = probabilityProjection.SellNotionalJPY * plan.AskPrice / mid
		plan.AllowBid = plan.AllowBid && plan.BidQuoteNotional > 0
		plan.AllowAsk = plan.AllowAsk && plan.AskQuoteNotional > 0
	} else if fastQuantityFallbackUsed {
		if plan.BidPrice > 0 {
			plan.BidQuoteNotional = math.Min(plan.BidQuoteNotional,
				riskUtilizationSizing.BuyNotionalCapJPY*plan.BidPrice/mid)
		}
		if plan.AskPrice > 0 {
			plan.AskQuoteNotional = math.Min(plan.AskQuoteNotional,
				riskUtilizationSizing.SellNotionalCapJPY*plan.AskPrice/mid)
		}
		plan.AllowBid = plan.AllowBid && plan.BidQuoteNotional > 0
		plan.AllowAsk = plan.AllowAsk && plan.AskQuoteNotional > 0
	}

	buyQuoteNotional := fixedpoint.NewFromFloat(plan.BidQuoteNotional)
	sellQuoteNotional := fixedpoint.NewFromFloat(plan.AskQuoteNotional)
	if plan.AllowBid && !makerBidEligible(
		s.Market, fixedpoint.NewFromFloat(plan.BidPrice), quoteableQuote, buyQuoteNotional) {
		plan.AllowBid = false
		plan.BidQuoteNotional = 0
		buyQuoteNotional = fixedpoint.Zero
	}
	s.makerBuyQuoteNotional = buyQuoteNotional
	s.makerSellQuoteNotional = sellQuoteNotional

	buyInventoryHeadroom := hardBuyInventoryHeadroom
	sellInventoryHeadroom := hardSellInventoryHeadroom
	if !probabilityProjectionUsed {
		buyInventoryHeadroom = math.Min(hardBuyInventoryHeadroom, riskUtilizationSizing.BuyNotionalCapJPY)
		sellInventoryHeadroom = math.Min(hardSellInventoryHeadroom, riskUtilizationSizing.SellNotionalCapJPY/mid)
	}

	if inventoryBase < inventoryBand.Target {
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
		InventoryDeficit: inventoryBase < inventoryBand.Target,
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
	macroActiveExecution := MacroActiveExecutionDecision{
		Reason: "long-horizon target integrated into Fast inventory control",
	}
	fastTargetExecution := FastTargetExecutionDecision{Reason: "disabled"}
	// FastTargetExecution is an execution actuator for the already-selected
	// inventory target; it must not invent a target when the posterior/dynamic
	// target evidence is unavailable. Keep the readiness predicate explicit so
	// a disabled IOC path is observable instead of looking like a silent quote
	// failure.
	fastTargetExecutionModelReady := posteriorInventoryTarget.Enabled
	if quoteConfig.FastTargetExecution.Enabled && fastTargetExecutionModelReady {
		fastTargetHorizonDecision := horizonDecision
		if earlyBumpDecision.Apply {
			buyDistance, sellDistance, grossEdge := MakerTouchDistances(
				ticker.Buy.Float64(), ticker.Sell.Float64(), plan.BidPrice, plan.AskPrice)
			aligned := s.makerHorizonModel.CrossingDecisionAtSideDistances(
				now, quoteConfig, horizon, buyDistance, sellDistance, grossEdge)
			if aligned.HasSufficientCrossings(quoteConfig.HorizonMinSamples) {
				fastTargetHorizonDecision = aligned
			}
		}
		fastTargetDirection := 0
		if projectionTargetBase > inventoryBase {
			fastTargetDirection = 1
		} else if projectionTargetBase < inventoryBase {
			fastTargetDirection = -1
		}
		passiveQuotePrice := plan.BidPrice
		passiveAvailable := !fastValueRejected && plan.AllowBid && passiveQuotePrice > 0
		touchProbability := fastTargetHorizonDecision.BuyTouchProbability
		touchStdError := fastTargetHorizonDecision.BuyTouchStdError
		if fastTargetDirection < 0 {
			passiveQuotePrice = plan.AskPrice
			passiveAvailable = !fastValueRejected && plan.AllowAsk && passiveQuotePrice > 0
			touchProbability = fastTargetHorizonDecision.SellTouchProbability
			touchStdError = fastTargetHorizonDecision.SellTouchStdError
		}
		if !passiveAvailable {
			passiveQuotePrice, touchProbability, touchStdError = 0, 0, 0
		}
		modelUpdateInterval := time.Duration(quoteConfig.HorizonUpdateInterval)
		if modelUpdateInterval <= 0 {
			modelUpdateInterval = 5 * time.Minute
		}
		modelUpdatedAt := now.Truncate(modelUpdateInterval)
		lastExecutionModelAt := time.Time{}
		lastExecutionDirection := 0
		if s.State != nil {
			lastExecutionModelAt = s.State.LastFastTargetExecutionModelAt
			lastExecutionDirection = s.State.LastFastTargetExecutionDirection
		}
		downside := s.makerHorizonModel.ExecutableDownsideDecision(horizon)
		upside := s.makerHorizonModel.ExecutableUpsideDecision(horizon)
		fastTargetExecution = EvaluateFastTargetExecution(
			quoteConfig.FastTargetExecution,
			FastTargetExecutionInput{
				Now: now, ModelUpdatedAt: modelUpdatedAt,
				LastExecutionModelAt:   lastExecutionModelAt,
				LastExecutionDirection: lastExecutionDirection, Horizon: horizon,
				Direction:            fastTargetDirection,
				CurrentInventoryBase: inventoryBase, TargetInventoryBase: projectionTargetBase,
				AvailableBase: base.Float64(), AvailableQuote: quoteableQuote.Float64(),
				BestBid: ticker.Buy.Float64(), BestBidSize: ticker.BuySize.Float64(),
				BestAsk: ticker.Sell.Float64(), BestAskSize: ticker.SellSize.Float64(),
				PassiveQuotePrice: passiveQuotePrice, PassiveAvailable: passiveAvailable,
				TouchProbability: touchProbability, TouchStdError: touchStdError,
				InventoryReturnMeanBps:        posteriorInventoryTarget.InventoryReturnMean,
				InventoryPredictiveSDBps:      posteriorInventoryTarget.InventoryPredictiveSD,
				DirectionConfidence:           posteriorInventoryTarget.DirectionConfidence,
				PersistentDownsideActive:      downside.Active,
				PersistentDownsideEValue:      downside.DownEValue,
				PersistentDownsideForecastBps: downside.BidForecastBps,
				PersistentUpsideActive:        upside.Active,
				PersistentUpsideEValue:        upside.DownEValue,
				PersistentUpsideForecastBps:   upside.BidForecastBps,
				PairEquityJPY:                 pairEquityJPY,
				RiskAversion:                  fastRiskAversion,
				ConfidenceZScore:              quoteConfig.InventoryRiskZScore,
				MakerFeeBps:                   quoteConfig.MakerFeeBps, TakerFeeBps: quoteConfig.TakerFeeBps,
				MinimumQuantityBase: s.Market.MinQuantity.Float64(),
				MinimumNotionalJPY:  s.Market.MinNotional.Float64(),
			})
		if fillRebalanceGeneration == 0 && fastTargetExecution.Trigger {
			if s.executeFastTargetIOC(ctx, ticker, modelUpdatedAt, fastTargetExecution) {
				return
			}
		}
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
		InventoryDeficit:       inventoryBase < inventoryBand.Target,
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
		inventoryBase < inventoryBand.Target && quoteableQuote.Sign() > 0 {
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
			s.executeAcquisitionReset(ctx, ticker, fixedpoint.NewFromFloat(inventoryBase), quoteableQuote, inventoryBand.Target, acquisitionDecision)
			return
		}
	}

	// Do not churn maker orders on every BBO tick. Binance user-data cancel
	// updates can arrive after the local order is removed; refreshing too fast
	// then accumulates pending order updates in ActiveOrderBook.
	windowDuration := orderReviewDuration
	minRefreshInterval, refreshInterval := quoteConfig.RefreshIntervals(plan.HalfSpreadBps, quoteVolatility*10_000)
	transportMinRefreshInterval, _ := quoteConfig.RefreshIntervals(0, 0)
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
	inventoryHeadroomExceeded := makerOrdersExceedInventoryBand(activeMakerOrders, inventoryBase, hardInventoryBand)
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
	fastEdgeImprovementBps := 0.0
	if fastSignalHealthy && selectedFastWindow > 0 && elapsed >= selectedFastWindow {
		if rawFastDirection > 0 && activeBid && plan.BidPrice > activeBidPrice {
			fastEdgeImprovementBps = math.Log(plan.BidPrice/activeBidPrice) * 10_000
		} else if rawFastDirection < 0 && activeAsk && plan.AskPrice < activeAskPrice {
			fastEdgeImprovementBps = math.Log(activeAskPrice/plan.AskPrice) * 10_000
		}
	}
	fastEdgeLeaseExpired := fastEdgeImprovementBps >= quoteConfig.RefreshMoveBps
	statisticalRealignment := false
	oneSidedTargetRealignment := false
	oneSidedTargetRiskRealignment := false
	oneSidedCandidateCEJPY, oneSidedActiveCEJPY := 0.0, 0.0
	statisticalScoreImprovement := 0.0
	statisticalScoreThreshold := 0.0
	activeHorizonDecision := MarketMakerHorizonDecision{}
	if activeBid && activeAsk && activeBidPrice > 0 && activeAskPrice > activeBidPrice &&
		horizonDecision.HasSufficientCrossings(quoteConfig.HorizonMinSamples) {
		activeHorizon := s.makerTradingWindowEndsAt.Sub(s.makerTradingWindowStartedAt)
		if activeHorizon <= 0 {
			activeHorizon = horizon
		}
		activeBuyDistance, activeSellDistance, activeGrossEdge := MakerTouchDistances(
			ticker.Buy.Float64(), ticker.Sell.Float64(), activeBidPrice, activeAskPrice)
		activeHorizonDecision = s.makerHorizonModel.CrossingDecisionAtSideDistances(
			now, quoteConfig, activeHorizon, activeBuyDistance, activeSellDistance, activeGrossEdge)
		statisticalRealignment, statisticalScoreImprovement, statisticalScoreThreshold =
			makerQuoteStatisticalRealignment(horizonDecision, activeHorizonDecision, quoteConfig.InventoryRiskZScore)
	}
	// Public-BBO hazard observations are coalesced to the configured
	// five-minute cadence. They condition the lifecycle probability on quote
	// age without changing the Fast crossing estimator or its window selector.
	lifecycleCurrentProbability := activeHorizonDecision.BothTouchProbability
	lifecycleCurrentProbabilitySE := activeHorizonDecision.BothTouchStdError
	lifecycleCandidateProbability := horizonDecision.BothTouchProbability
	lifecycleCandidateProbabilitySE := horizonDecision.BothTouchStdError
	lifecycleReplacementCost := quoteConfig.QuoteLifecycleAction.ReplacementCostBps
	lifecycleReplacementCostSE := 0.0
	if s.makerQuoteLifecycleHazard != nil && quoteConfig.QuoteLifecycleAction.Hazard.Enabled && activeBid && activeAsk &&
		activeBidPrice > 0 && activeAskPrice > activeBidPrice {
		activeBuyDistance, activeSellDistance, _ := MakerTouchDistances(
			ticker.Buy.Float64(), ticker.Sell.Float64(), activeBidPrice, activeAskPrice)
		age := elapsed
		if !s.makerTradingWindowStartedAt.IsZero() {
			age = now.Sub(s.makerTradingWindowStartedAt)
		}
		if age < 0 {
			age = 0
		}
		s.makerQuoteLifecycleHazard.Observe(now, QuoteLifecycleHazardBuy, activeBuyDistance, age, ticker.Sell.Float64() <= activeBidPrice)
		s.makerQuoteLifecycleHazard.Observe(now, QuoteLifecycleHazardSell, activeSellDistance, age, ticker.Buy.Float64() >= activeAskPrice)
		candidateBuyDistance, candidateSellDistance, _ := MakerTouchDistances(
			ticker.Buy.Float64(), ticker.Sell.Float64(), plan.BidPrice, plan.AskPrice)
		// The candidate is a counterfactual public-BBO exposure. Recording it at
		// age zero gives the replacement arm its own five-minute hazard instead
		// of forcing every new quote to borrow the current quote's aged cell.
		s.makerQuoteLifecycleHazard.Observe(now, QuoteLifecycleHazardBuy, candidateBuyDistance, 0, plan.BidPrice > 0 && ticker.Sell.Float64() <= plan.BidPrice)
		s.makerQuoteLifecycleHazard.Observe(now, QuoteLifecycleHazardSell, candidateSellDistance, 0, plan.AskPrice > 0 && ticker.Buy.Float64() >= plan.AskPrice)
		activeLifecycleHorizon := activeHorizonDecision.Horizon
		if activeLifecycleHorizon <= 0 {
			activeLifecycleHorizon = horizon
		}
		currentHazard := s.makerQuoteLifecycleHazard.PairSnapshot(activeLifecycleHorizon, activeBuyDistance, activeSellDistance, age, age)
		candidateHazard := s.makerQuoteLifecycleHazard.PairSnapshot(horizon, candidateBuyDistance, candidateSellDistance, 0, 0)
		if currentHazard.Ready {
			lifecycleCurrentProbability, lifecycleCurrentProbabilitySE = currentHazard.BothProbability, currentHazard.BothStdError
		}
		if candidateHazard.Ready {
			lifecycleCandidateProbability, lifecycleCandidateProbabilitySE = candidateHazard.BothProbability, candidateHazard.BothStdError
		}
		cost := EstimateQuoteLifecycleReplacementCostBps(QuoteLifecycleReplacementCostInput{
			BaseCostBps:            quoteConfig.QuoteLifecycleAction.ReplacementCostBps,
			CurrentFillProbability: lifecycleCurrentProbability, CurrentAge: age, Horizon: activeLifecycleHorizon,
			CurrentTerminalMarkoutBps: activeHorizonDecision.NetRoundTripEdgeBps,
			BBOStalenessBps:           math.Max(adverseAskMoveBps, adverseBidMoveBps),
			VolatilityBpsPerSqrtSec:   quoteVolatility * 10_000,
		}, quoteConfig.QuoteLifecycleAction)
		lifecycleReplacementCost, lifecycleReplacementCostSE = cost.CostBps, cost.StdErrorBps
	}
	// A balance-boundary quote has only the target-restoring side on exchange.
	// The old bilateral guard above made its statistical renewal impossible, so
	// a falling market could leave an overweight SELL behind until the complete
	// 30-minute lease expired. Compare the resting side with the new candidate
	// using the same hypothetical completion leg and the same posterior score.
	// This changes only the quoted side and only when the replacement moves
	// inward toward execution and beats the uncertainty-adjusted active score.
	if !statisticalRealignment && elapsed >= transportMinRefreshInterval &&
		horizonDecision.HasSufficientCrossings(quoteConfig.HorizonMinSamples) {
		activeHorizon := s.makerTradingWindowEndsAt.Sub(s.makerTradingWindowStartedAt)
		if activeHorizon <= 0 {
			activeHorizon = horizon
		}
		activeReferenceBid, activeReferenceAsk := activeBidPrice, activeAskPrice
		targetRestoring := false
		if activeAsk && !activeBid && inventoryBase > projectionTargetBase+1e-12 &&
			plan.AllowAsk && plan.AskPrice+1e-12 < activeAskPrice {
			activeReferenceBid = plan.BidPrice
			targetRestoring = activeReferenceBid > 0 && activeReferenceBid < activeReferenceAsk
		} else if activeBid && !activeAsk && inventoryBase+1e-12 < projectionTargetBase &&
			plan.AllowBid && plan.BidPrice > activeBidPrice+1e-12 {
			activeReferenceAsk = plan.AskPrice
			targetRestoring = activeReferenceAsk > activeReferenceBid
		}
		if targetRestoring {
			activeBuyDistance, activeSellDistance, activeGrossEdge := MakerTouchDistances(
				ticker.Buy.Float64(), ticker.Sell.Float64(), activeReferenceBid, activeReferenceAsk)
			activeHorizonDecision = s.makerHorizonModel.CrossingDecisionAtSideDistances(
				now, quoteConfig, activeHorizon, activeBuyDistance, activeSellDistance, activeGrossEdge)
			oneSidedTargetRealignment, statisticalScoreImprovement, statisticalScoreThreshold =
				makerQuoteStatisticalRealignment(horizonDecision, activeHorizonDecision, quoteConfig.InventoryRiskZScore)
			buyBoundary := activeBid && !activeAsk &&
				quoteableQuote.Float64()+1e-9 >= projectionInput.MinBuyNotionalJPY &&
				base.Float64()*mid+1e-9 < projectionInput.MinSellNotionalJPY
			sellBoundary := activeAsk && !activeBid &&
				quoteableQuote.Float64()+1e-9 < projectionInput.MinBuyNotionalJPY &&
				base.Float64()*mid+1e-9 >= projectionInput.MinSellNotionalJPY
			orderNotionalJPY := probabilityProjection.SellNotionalJPY
			if buyBoundary {
				orderNotionalJPY = probabilityProjection.BuyNotionalJPY
			}
			if buyBoundary || sellBoundary {
				oneSidedTargetRiskRealignment, oneSidedCandidateCEJPY, oneSidedActiveCEJPY =
					TargetRestoringSideRealignment(
						&s.makerHorizonModel, quoteConfig, now, activeHorizon,
						ticker.Buy.Float64(), ticker.Sell.Float64(),
						plan.BidPrice, plan.AskPrice, activeReferenceBid, activeReferenceAsk,
						buyBoundary, inventoryBase*mid, projectionTargetBase*mid,
						orderNotionalJPY, pairEquityJPY,
						fastRiskAversion, quoteConfig.InventoryRiskZScore)
			}
			oneSidedTargetRealignment = oneSidedTargetRealignment || oneSidedTargetRiskRealignment
			statisticalRealignment = oneSidedTargetRealignment
		}
	}
	currentRiskyWeight := 0.0
	selectedProjectionTargetRatio := effectiveInventoryTargetRatio
	// Keep the legacy mid-marked ratio for model continuity, but expose an
	// executable liquidation mark separately.  The former is an exposure ratio,
	// not a complete risk measure: it ignores the bid-side exit price and the
	// fee paid if the position must be reduced immediately.
	liquidationMarkPrice := ticker.Buy.Float64()
	liquidationFeeRate := math.Max(0, quoteConfig.TakerFeeBps) / 10_000
	if liquidationMarkPrice > 0 {
		liquidationMarkPrice *= math.Max(0, 1-liquidationFeeRate)
	}
	totalBase := quoteBalances.TotalBase.Float64()
	totalQuote := quoteBalances.TotalQuote.Float64()
	liquidationNotionalJPY := totalBase * liquidationMarkPrice
	liquidationEquityJPY := totalQuote + liquidationNotionalJPY
	liquidationRiskyWeight := 0.0
	if liquidationEquityJPY > 0 {
		liquidationRiskyWeight = liquidationNotionalJPY / liquidationEquityJPY
	}
	currentRiskyNotionalJPY := inventoryBase * mid
	if totalBase > 0 && mid > 0 {
		currentRiskyNotionalJPY = totalBase * mid
	}
	inventoryTargetGapJPY := currentRiskyNotionalJPY - projectionTargetBase*mid
	inventoryRiskUsedJPY := currentRiskyNotionalJPY * math.Max(0, inventoryBand.RiskMoveBps) / 10_000
	inventoryRiskUtilization := 0.0
	if effectiveRiskBudgetJPY > 0 {
		inventoryRiskUtilization = inventoryRiskUsedJPY / effectiveRiskBudgetJPY
	}
	if pairEquityJPY > 0 {
		currentRiskyWeight = inventoryBase * mid / pairEquityJPY
		selectedProjectionTargetRatio = projectionTargetBase * mid / pairEquityJPY
	}
	macroTargetRealignment := s.makerQuotedTargetSet && InventoryTargetRealignmentRequired(
		effectiveInventoryTargetRatio, s.makerQuotedTargetRatio, currentRiskyWeight,
		pairEquityJPY, executableOrderNotionalJPY)
	fastTargetRealignment := s.makerQuotedTargetSet && InventoryTargetRealignmentRequired(
		selectedProjectionTargetRatio, s.makerQuotedFastTargetRatio, currentRiskyWeight,
		pairEquityJPY, executableOrderNotionalJPY)
	reservationRiskTickBps := s.Market.TickSize.Float64() / mid * 10_000
	reservationTargetSideActive := (appliedFastReservationBps > 0 && activeBid) ||
		(appliedFastReservationBps < 0 && activeAsk)
	reservationRiskRealignment := fastReservationUtility.Applied && reservationTargetSideActive &&
		FastReservationRealignmentRequired(
			appliedFastReservationBps, s.makerQuotedFastReservationBps, reservationRiskTickBps)
	unexpectedSide := (hasBid && !plan.AllowBid) || (hasAsk && !plan.AllowAsk)
	// Bellman lifecycle review is deliberately downstream of all hard safety
	// and inventory-policy gates. It is only allowed at an expired paired
	// window, where KEEP preserves queue age, REPLACE enters the existing
	// cancel/requote path, and CANCEL removes both passive legs. Shadow mode
	// evaluates the same value without changing order flow.
	lifecycleActionDecision := QuoteLifecycleActionValue{Action: QuoteLifecycleCancel, Reason: "disabled"}
	lifecycleReplace := false
	if quoteConfig.QuoteLifecycleAction.Enabled && plan.Reason == "quoted" &&
		windowExpired && fillRebalanceGeneration == 0 &&
		activeBid && activeAsk && plan.AllowBid && plan.AllowAsk &&
		!quoteCrossed && !adverseMove && !missingSide && !sideMismatch && !unexpectedSide &&
		!statisticalRealignment && !oneSidedTargetRealignment &&
		!macroTargetRealignment && !fastTargetRealignment && !reservationRiskRealignment &&
		!earlyBumpDecision.Refresh &&
		horizonDecision.HasSufficientCrossings(quoteConfig.HorizonMinSamples) &&
		activeHorizonDecision.HasSufficientCrossings(quoteConfig.HorizonMinSamples) {
		lifecycleActionDecision = EvaluateQuoteLifecycleFromHorizonsWithEstimates(
			activeHorizonDecision, horizonDecision, true, true,
			quoteConfig.QuoteLifecycleAction,
			lifecycleCurrentProbability, lifecycleCurrentProbabilitySE,
			lifecycleCandidateProbability, lifecycleCandidateProbabilitySE,
			lifecycleReplacementCost, lifecycleReplacementCostSE)
		if lifecycleActionDecision.Evaluated && !quoteConfig.QuoteLifecycleAction.ShadowOnly {
			switch lifecycleActionDecision.Action {
			case QuoteLifecycleKeep:
				s.makerTradingWindowStartedAt = now
				s.makerTradingWindowEndsAt = now.Add(windowDuration)
				log.WithFields(logrus.Fields{
					"symbol": s.Symbol, "action": lifecycleActionDecision.Action,
					"keepValueBps":    lifecycleActionDecision.KeepValueBps,
					"replaceValueBps": lifecycleActionDecision.ReplaceValueBps,
					"cancelValueBps":  lifecycleActionDecision.CancelValueBps,
				}).Info("market-maker Bellman quote lifecycle kept resting pair")
				return
			case QuoteLifecycleCancel:
				if err := s.gracefulCancelMaker(ctx, "quote-lifecycle-cancel"); err != nil {
					log.WithError(err).Warn("market-maker Bellman lifecycle cancellation failed")
					s.retryMakerFillRebalanceLocked(fillRebalanceGeneration)
					return
				}
				s.lastMakerQuoteAt = now
				s.makerTradingWindowStartedAt = now
				s.makerTradingWindowEndsAt = now.Add(windowDuration)
				s.lastMakerBid = fixedpoint.Zero
				s.lastMakerAsk = fixedpoint.Zero
				s.makerReplacementRetryAfter = now.Add(windowDuration)
				s.makerNoOrderReferenceBid = ticker.Buy.Float64()
				s.makerNoOrderReferenceAsk = ticker.Sell.Float64()
				s.State.LastDecision = "Quote lifecycle CANCEL"
				log.WithFields(logrus.Fields{
					"symbol": s.Symbol, "action": lifecycleActionDecision.Action,
					"keepValueBps":    lifecycleActionDecision.KeepValueBps,
					"replaceValueBps": lifecycleActionDecision.ReplaceValueBps,
					"cancelValueBps":  lifecycleActionDecision.CancelValueBps,
				}).Info("market-maker Bellman quote lifecycle cancelled resting pair")
				return
			case QuoteLifecycleReplace:
				lifecycleReplace = true
			}
		}
	}
	// An inventory-headroom violation is a cancellation-only transition.
	// Do not cancel and submit in the same callback: the exchange/user-data
	// cancel is asynchronous, so the old order can still be visible on the
	// next BBO event. Repeated callbacks are rate-limited until that state
	// settles, preventing duplicate replacement orders.
	if inventoryHeadroomExceeded {
		s.logMakerQuoteGate(now, "inventory-headroom-exceeded", logrus.Fields{
			"activeMakerOrders": len(activeMakerOrders), "inventory": base,
			"inventoryMin": hardInventoryBand.MinInventory, "inventoryMax": hardInventoryBand.MaxInventory,
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
	if makerEmptyBookRetryPending(
		now, s.makerReplacementRetryAfter, len(activeMakerOrders), fillRebalanceGeneration,
		s.makerNoOrderReferenceBid, s.makerNoOrderReferenceAsk,
		ticker.Buy.Float64(), ticker.Sell.Float64(), quoteConfig.RefreshMoveBps,
	) {
		return
	}
	// Ordinary price/imbalance changes are observations during the modeled
	// passage window, not reasons to destroy queue age before that model has had
	// time to resolve. Hard marketability, inventory/side-policy, and confirmed
	// fill transitions remain independently actionable.
	retainBidOnRefresh, retainAskOnRefresh := false, false
	if !s.lastMakerQuoteAt.IsZero() && fillRebalanceGeneration == 0 {
		lockedBid := s.Market.TruncatePrice(fixedpoint.NewFromFloat(earlyBumpDecision.BidPrice)).Float64()
		tickTolerance := math.Max(1e-12, s.Market.TickSize.Float64()/2)
		earlyBumpLockResting := earlyBumpDecision.Apply && earlyBumpDecision.Phase == EarlyBumpLocked &&
			activeBid && math.Abs(activeBidPrice-lockedBid) <= tickTolerance
		if earlyBumpLockResting && !quoteCrossed && !windowExpired &&
			!macroTargetRealignment && !fastTargetRealignment && !reservationRiskRealignment &&
			!missingSide && !sideMismatch && !unexpectedSide {
			// The urgency price is absolute. Do not follow a rising BBO or
			// sacrifice queue age during the lock; only execution and safety
			// conditions can end it.
			return
		}
		// A target-restoring Fast action can intentionally quote one side only.
		// Use the freshly evaluated opposite-side completion price solely to
		// verify that the complete cycle still pays its fee floor; otherwise the
		// near-fill lease would be unavailable exactly for these one-sided orders.
		retentionBidPrice, retentionAskPrice := activeBidPrice, activeAskPrice
		if activeBid && !activeAsk && plan.AllowBid {
			retentionAskPrice = plan.AskPrice
		}
		if activeAsk && !activeBid && plan.AllowAsk {
			retentionBidPrice = plan.BidPrice
		}
		retainBidOnRefresh, retainAskOnRefresh = makerQuoteNearFillSides(
			retentionBidPrice, retentionAskPrice, ticker.Buy.Float64(), ticker.Sell.Float64(), plan,
			2*quoteConfig.MakerFeeBps+2*quoteConfig.AdverseSelectionBps+quoteConfig.MinimumNetEdgeBps)
		feeSafeNearFill := retainBidOnRefresh || retainAskOnRefresh
		allActiveNearFill := (!activeBid || retainBidOnRefresh) && (!activeAsk || retainAskOnRefresh)
		expiryNearFillRetention := windowExpired && plan.Reason == "quoted" && feeSafeNearFill && !earlyBumpDecision.Refresh &&
			!quoteCrossed && !missingSide && !sideMismatch && !unexpectedSide &&
			!fastEdgeLeaseExpired && !statisticalRealignment &&
			!macroTargetRealignment && !fastTargetRealignment && !reservationRiskRealignment &&
			!lifecycleReplace
		if expiryNearFillRetention && allActiveNearFill {
			// Expiry is a model review boundary, not an exchange-order TTL. At
			// least one fixed-price side is now closer than its replacement and
			// the resting pair still pays fees/adverse selection. Preserve both
			// queue positions and schedule the next evidence review in place.
			s.makerTradingWindowStartedAt = now
			s.makerTradingWindowEndsAt = now.Add(windowDuration)
			log.WithFields(logrus.Fields{
				"symbol": s.Symbol, "activeBid": activeBidPrice, "activeAsk": activeAskPrice,
				"nextReviewAt": s.makerTradingWindowEndsAt,
			}).Info("market-maker quote review retained fee-safe near-fill orders")
			return
		}
		if !expiryNearFillRetention {
			retainBidOnRefresh, retainAskOnRefresh = false, false
		}
		// Quotes that are no longer fee-safe/near-fill, or whose statistical,
		// Fast, Macro, policy, or safety state changed, use the replacement path.
		if !makerQuoteRefreshRequired(elapsed, minRefreshInterval, windowDuration, quoteCrossed, windowExpired,
			adverseMove, materialMove, materialImbalance,
			missingSide || sideMismatch || fastTargetRealignment, len(activeMakerOrders) == 0, fastEdgeLeaseExpired,
			statisticalRealignment || macroTargetRealignment || reservationRiskRealignment || earlyBumpDecision.Refresh || lifecycleReplace,
			oneSidedTargetRealignment) {
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
			"posteriorInventoryTargetEnabled": posteriorInventoryTarget.Enabled, "posteriorInventoryTargetReason": posteriorInventoryTarget.Reason,
			"fastTargetExecutionModelReady": fastTargetExecutionModelReady,
			"posteriorInventoryTargetBase":  posteriorInventoryTarget.TargetBase, "posteriorInventoryUpProbability": posteriorInventoryTarget.UpProbability,
			"posteriorInventoryDirectionConfidence": posteriorInventoryTarget.DirectionConfidence,
			"posteriorInventoryReturnMeanBps":       posteriorInventoryTarget.InventoryReturnMean, "posteriorInventoryReturnSEBps": posteriorInventoryTarget.InventoryReturnSE,
			"posteriorInventoryPredictiveSDBps":     posteriorInventoryTarget.InventoryPredictiveSD,
			"dynamicInventoryAimEnabled":            quoteConfig.DynamicInventoryAim.Enabled,
			"dynamicInventoryAimShadowOnly":         quoteConfig.DynamicInventoryAim.ShadowOnly,
			"dynamicInventoryAimApplied":            dynamicInventoryAim.Applied,
			"dynamicInventoryAimGatePassed":         dynamicInventoryAim.GatePassed,
			"dynamicInventoryAimReason":             dynamicInventoryAim.Reason,
			"dynamicInventoryAimGateReason":         dynamicInventoryAim.GateReason,
			"dynamicInventoryAimCurrentRatio":       dynamicInventoryAim.CurrentInventoryRatio,
			"dynamicInventoryAimPolicyRatio":        dynamicInventoryAim.PolicyTargetRatio,
			"dynamicInventoryAimEffectiveSamples":   dynamicInventoryAim.EffectiveSamples,
			"dynamicInventoryAimSamplesGate":        "effectiveSamples>1",
			"dynamicInventoryAimRatio":              dynamicInventoryAim.AimTargetRatio,
			"dynamicInventoryAdjustedTargetRatio":   dynamicInventoryAim.AdjustedTargetRatio,
			"dynamicInventoryAimGrossReturnBps":     dynamicInventoryAim.GrossReturnBps,
			"dynamicInventoryAimShrunkReturnBps":    dynamicInventoryAim.ShrunkReturnBps,
			"dynamicInventoryAimNetReturnBps":       dynamicInventoryAim.NetReturnBps,
			"dynamicInventoryAimPredictiveSDBps":    dynamicInventoryAim.PredictiveStdDevBps,
			"dynamicInventoryAimPositiveProb":       dynamicInventoryAim.PositiveReturnProb,
			"dynamicInventoryAimSignalStrength":     dynamicInventoryAim.SignalStrength,
			"dynamicInventoryAimAlphaPersistence":   dynamicInventoryAim.AlphaPersistence,
			"dynamicInventoryAimAdjustmentFraction": dynamicInventoryAim.AdjustmentFraction,
			"fastTargetSwitchingEnabled":            quoteConfig.FastTargetSwitching.Enabled,
			"fastTargetSwitchingShadowOnly":         quoteConfig.FastTargetSwitching.ShadowOnly,
			"fastTargetSwitchingApplied":            fastTargetSwitching.Applied,
			"fastTargetSwitchingReason":             fastTargetSwitching.Reason,
			"fastTargetSwitchingCandidateBase":      fastTargetSwitching.CandidateTargetBase,
			"fastTargetSwitchingPreviousBase":       fastTargetSwitching.PreviousTargetBase,
			"fastTargetSwitchingSelectedBase":       fastTargetSwitching.SelectedTargetBase,
			"fastTargetSwitchingIncrementalCEJPY":   fastTargetSwitching.IncrementalCertaintyEquivalentJPY,
			"fastTargetSwitchingPreviousNetCEJPY":   fastTargetSwitching.PreviousNetCertaintyEquivalentJPY,
			"fastTargetSwitchingSelectedNetCEJPY":   fastTargetSwitching.SelectedNetCertaintyEquivalentJPY,
			"fastTargetSwitchingCostJPY":            fastTargetSwitching.SwitchingCostJPY,
			"fastTargetSwitchingNetValueJPY":        fastTargetSwitching.NetSwitchValueJPY,
			"jointSidePressure":                     plan.SidePressure, "jointReservationShiftBps": plan.ReservationShiftBps,
			"fastDriftConfigured": quoteConfig.FastDrift.Enabled, "fastDriftShadowOnly": quoteConfig.FastDrift.ShadowOnly,
			"fastDriftApplied": plan.FastDriftApplied, "fastDriftHealthy": fastDrift.Healthy,
			"fastDriftReason": fastDrift.Reason, "fastDriftSamples": fastDrift.Samples,
			"fastDriftValidationSamples": fastDrift.ValidationSamples, "fastDriftPrequentialSkill": fastDrift.PrequentialSkill,
			"fastDriftValidationGainBps2": fastDrift.ValidationGainBps2, "fastDriftValidationGainSEBps2": fastDrift.ValidationGainSEBps2,
			"fastDriftValidationProbability": fastDrift.ValidationProbability, "fastDriftStrength": fastDrift.Strength,
			"fastDriftBBOStateTag":                fastDriftBBOStateTag,
			"asymmetricOscillationRiskEnabled":    quoteConfig.AsymmetricOscillationRisk.Enabled,
			"asymmetricOscillationRiskShadowOnly": quoteConfig.AsymmetricOscillationRisk.ShadowOnly,
			"asymmetricOscillationRiskMultiplier": asymmetricRiskDecision.RiskMultiplier,
			"asymmetricOscillationRiskScore":      asymmetricRiskDecision.OscillationScore,
			"asymmetricOscillationRiskAsymmetry":  asymmetricRiskDecision.AsymmetryScore,
			"asymmetricOscillationRiskReason":     asymmetricRiskDecision.Reason,
			"fastDriftAskMeanBps":                 fastDrift.AskMeanBps, "fastDriftBidMeanBps": fastDrift.BidMeanBps,
			"fastDriftRawCenterMeanBps": fastDrift.RawCenterMeanBps, "fastDriftCenterMeanBps": fastDrift.CenterMeanBps,
			"fastDriftCenterVarianceBps2": fastDrift.CenterVarianceBps2,
			"buyQuoteFactor":              plan.BidQuoteFactor, "sellQuoteFactor": plan.AskQuoteFactor,
			"minQuantity": s.Market.MinQuantity, "canBuy": canBuy, "canSell": canSell,
			"plan": plan.Reason, "allowBid": plan.AllowBid, "allowAsk": plan.AllowAsk,
			"modelHealth": modelSnapshot.Health, "modelUp": modelSnapshot.Up,
			"modelDown": modelSnapshot.Down, "modelEventAge": modelSnapshot.Age,
			"fastHealth": fastSnapshot.Health, "fastWindowSelected": selectedFastWindow,
			"fastWindowHealths": fastHealthSummary, "fastDirection": direction, "rawFastDirection": rawFastDirection,
			"bocpd45CalibrationReady": bocpd45.CalibrationReady,
			"bocpd45MaturedLabels":    bocpd45.MaturedLabels,
			"bocpd45Direction":        bocpd45.Direction,
			"fastEdgeLeaseExpired":    fastEdgeLeaseExpired, "fastEdgeImprovementBps": fastEdgeImprovementBps,
			"statisticalRealignment":                       statisticalRealignment,
			"oneSidedTargetRealignment":                    oneSidedTargetRealignment,
			"oneSidedTargetRiskRealignment":                oneSidedTargetRiskRealignment,
			"oneSidedTargetCandidateCEJPY":                 oneSidedCandidateCEJPY,
			"oneSidedTargetActiveCEJPY":                    oneSidedActiveCEJPY,
			"macroTargetRealignment":                       macroTargetRealignment,
			"fastTargetRealignment":                        fastTargetRealignment,
			"reservationRiskRealignment":                   reservationRiskRealignment,
			"reservationRiskQuotedBps":                     s.makerQuotedFastReservationBps,
			"reservationRiskCandidateBps":                  fastReservation.ReservationShiftBps,
			"macroQuotedTargetRatio":                       s.makerQuotedTargetRatio,
			"inventoryActuationCurrentRiskyWeight":         currentRiskyWeight,
			"statisticalScoreImprovementBpsPerHour":        statisticalScoreImprovement,
			"statisticalScoreThresholdBpsPerHour":          statisticalScoreThreshold,
			"activeQuoteScoreBpsPerHour":                   activeHorizonDecision.ScoreBpsPerHour,
			"candidateQuoteScoreBpsPerHour":                horizonDecision.ScoreBpsPerHour,
			"quoteLifecycleActionEnabled":                  quoteConfig.QuoteLifecycleAction.Enabled,
			"quoteLifecycleActionShadowOnly":               quoteConfig.QuoteLifecycleAction.ShadowOnly,
			"quoteLifecycleAction":                         lifecycleActionDecision.Action,
			"quoteLifecycleActionReason":                   lifecycleActionDecision.Reason,
			"quoteLifecycleKeepValueBps":                   lifecycleActionDecision.KeepValueBps,
			"quoteLifecycleReplaceValueBps":                lifecycleActionDecision.ReplaceValueBps,
			"quoteLifecycleCancelValueBps":                 lifecycleActionDecision.CancelValueBps,
			"quoteLifecycleSelectedValueBps":               lifecycleActionDecision.SelectedValueBps,
			"quoteLifecycleIncrementalVsKeepBps":           lifecycleActionDecision.IncrementalVsKeepBps,
			"quoteLifecycleKeepLowerBps":                   lifecycleActionDecision.KeepLowerBps,
			"quoteLifecycleReplaceLowerBps":                lifecycleActionDecision.ReplaceLowerBps,
			"quoteLifecycleActionMarginBps":                lifecycleActionDecision.ActionMarginBps,
			"quoteLifecycleConfidenceZ":                    lifecycleActionDecision.ConfidenceZScore,
			"candidateQuoteScoreStdErrorBpsPerHour":        horizonDecision.ScoreStdErrorBpsHour,
			"distanceOptimized":                            horizonDecision.DistanceOptimized,
			"inventoryActuationEnabled":                    quantityActuation.Enabled,
			"inventoryActuationReason":                     quantityActuation.Reason,
			"inventoryActuationHorizon":                    actuationHorizon,
			"inventoryActuationHorizonSource":              actuationHorizonSource,
			"inventoryActuationDirection":                  quantityActuation.Direction,
			"inventoryActuationCorrectiveRatePerHour":      quantityActuation.CorrectiveFillRatePerHour,
			"inventoryActuationExpectedFills":              quantityActuation.ExpectedCorrectiveFills,
			"inventoryActuationRequiredFills":              quantityActuation.RequiredCorrectionFills,
			"inventoryActuationEffectiveLevels":            actuationLevels,
			"inventoryActuationQuantityReachabilityLoad":   quantityActuation.ReachabilityShortfall,
			"inventoryActuationPriceCorrectiveRatePerHour": actuation.CorrectiveFillRatePerHour,
			"inventoryActuationPriceReachabilityLoad":      actuation.ReachabilityShortfall,
			"inventoryActuationPriceMomentumProbability":   actuation.MomentumAlignmentProbability,
			"inventoryActuationPriceInwardStrength":        actuation.InwardStrength,
			"inventoryActuationPriceInwardBps":             plan.InventoryActuationInwardBps,
			"inventoryActuationPriceFloorBps":              plan.InventoryActuationFloorBps,
			"directionPosteriorUpWeight":                   float64(fastSnapshot.Up), "directionPosteriorDownWeight": float64(fastSnapshot.Down),
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
			"fastTradeCount5m":                           evidence.TradeCount5m,
			"fastBBOCount5m":                             evidence.BBOCount5m,
			"fastTradeImbalance5m":                       evidence.SignedTradeImbalance5m,
			"fastMidReturn1mBps":                         evidence.MidReturn1mBps,
			"fastMidReturn5mBps":                         evidence.MidReturn5mBps,
			"fastMidDrawdownBps":                         evidence.MidDrawdownBps,
			"fastMidDrawdownWindow":                      evidence.MidDrawdownWindow,
			"fastMidDrawdown5mBps":                       evidence.MidDrawdown5mBps,
			"fastMidRebound30sBps":                       evidence.MidRebound30sBps,
			"fastMidLow30s":                              evidence.MidLow30s,
			"fastOrderFlowImbalance30s":                  evidence.OrderFlowImbalance30s,
			"fastMicropriceDisplacement":                 evidence.MicropriceDisplacement,
			"fastMidVolatilityPerSqrtSecond5mBps":        evidence.MidVolatilityPerSqrtSecond5mBps,
			"fastBuyAskVolatilityPerSqrtSecond5mBps":     evidence.BuyVolatilityPerSqrtSecond5mBps,
			"fastSellBidVolatilityPerSqrtSecond5mBps":    evidence.SellVolatilityPerSqrtSecond5mBps,
			"fastMidVolatilitySamples5m":                 evidence.MidVolatilitySamples5m,
			"acquisitionDrawdownLimit5mBps":              acquisitionDrawdownLimit5mBps,
			"acquisitionReturn1mThresholdBps":            acquisitionReturn1mThresholdBps,
			"acquisitionReturn5mThresholdBps":            acquisitionReturn5mThresholdBps,
			"acquisitionReturnCalibrationReady":          acquisitionReturnCalibrationReady,
			"acquisitionStartShadowSignal":               acquisitionStart.Signal,
			"acquisitionStartShadowReason":               acquisitionStart.Reason,
			"acquisitionQuoteEnabled":                    quoteConfig.AcquisitionQuote.Enabled,
			"acquisitionQuoteShadowOnly":                 quoteConfig.AcquisitionQuote.ShadowOnly,
			"acquisitionQuoteDriftBps":                   acquisitionDriftBps,
			"acquisitionQuoteVolatilityPerSqrtSecondBps": acquisitionVolatilityPerSqrtSecBps,
			"acquisitionQuoteDeltaBps":                   plan.AcquisitionDeltaBps,
			"acquisitionQuoteTouchProbability":           plan.AcquisitionTouchProbability,
			"acquisitionQuoteApplied":                    plan.AcquisitionApplied,
			"fastRealizedVolatilityBps":                  evidence.RealizedVolatilityBps, "fastEvidenceAge": evidence.Age,
			"slowModelVolatilityBps":                    modelSnapshot.GammaCaptureVolatility * 10_000,
			"fastModelVolatilityBps":                    fastSnapshot.GammaCaptureVolatility * 10_000,
			"fastQuoteVolatilityUsable":                 fastQuoteVolatilityUsable,
			"fastSlowVolatilityDeltaBps":                (fastSnapshot.GammaCaptureVolatility - modelSnapshot.GammaCaptureVolatility) * 10_000,
			"makerFeeBpsEffective":                      quoteConfig.MakerFeeBps,
			"takerFeeBpsEffective":                      quoteConfig.TakerFeeBps,
			"feeSource":                                 feeSource,
			"roundTripMakerFeeBps":                      2 * quoteConfig.MakerFeeBps,
			"quoteEdgeAfterFeesBps":                     plan.BidDistanceBps + plan.AskDistanceBps - 2*quoteConfig.MakerFeeBps,
			"quoteNetEdgeBps":                           plan.BidDistanceBps + plan.AskDistanceBps - 2*quoteConfig.MakerFeeBps - 2*quoteConfig.AdverseSelectionBps - quoteConfig.MinimumNetEdgeBps,
			"quoteVolatilityBps":                        quoteVolatility * 10_000,
			"volatilityPriorBps":                        volatilityPriorBps,
			"buyAskVolatilityPriorBps":                  sideVolatilityPrior.BuyBps,
			"sellBidVolatilityPriorBps":                 sideVolatilityPrior.SellBps,
			"buyAskVolatilityPriorSamples":              sideVolatilityPrior.BuySamples,
			"sellBidVolatilityPriorSamples":             sideVolatilityPrior.SellSamples,
			"buyEffectiveVolatilityBps":                 buyEffectiveVolatilityBps,
			"sellEffectiveVolatilityBps":                sellEffectiveVolatilityBps,
			"buyVolatilityLiveWeight":                   buyVolatilityLiveWeight,
			"sellVolatilityLiveWeight":                  sellVolatilityLiveWeight,
			"volatilityPriorSamples":                    volatilityPriorSamples,
			"volatilityLiveWeight":                      volatilityLiveWeight,
			"inventoryVolatilityBps":                    inventoryVolatility * 10_000,
			"quoteHalfSpreadBps":                        plan.HalfSpreadBps,
			"bidDistanceBps":                            plan.BidDistanceBps,
			"askDistanceBps":                            plan.AskDistanceBps,
			"bidTouchDistanceBps":                       plan.BidTouchDistanceBps,
			"askTouchDistanceBps":                       plan.AskTouchDistanceBps,
			"averageCost":                               averageCost,
			"askEquityFloor":                            plan.AskEquityFloor,
			"askNetMarkEdgeBps":                         plan.AskNetMarkEdgeBps,
			"equityProtectionActive":                    plan.EquityProtected,
			"sideDistanceBias":                          plan.SidePressure,
			"sideDistanceSource":                        sideDistanceSource,
			"fairPriceSource":                           "mid-martingale-baseline",
			"horizonTouchModelEnabled":                  s.makerHorizonTouchModel != nil,
			"horizonTouchFeaturesReady":                 touchFeaturesReady,
			"historicalBuyTouchProbability":             historicalBuyTouchProbability,
			"historicalSellTouchProbability":            historicalSellTouchProbability,
			"recentBuyTouchProbability":                 recentBuyTouchProbability,
			"recentSellTouchProbability":                recentSellTouchProbability,
			"buyTouchProbability":                       buyTouchProbability,
			"sellTouchProbability":                      sellTouchProbability,
			"touchToFillHaircut":                        quoteConfig.HorizonTouchModel.TouchToFillHaircut,
			"touchModelBidDistanceBps":                  touchModelBidDistanceBps,
			"touchModelAskDistanceBps":                  touchModelAskDistanceBps,
			"riskSizingBuyFillRatePerHour":              riskSizingBuyFillRate,
			"riskSizingSellFillRatePerHour":             riskSizingSellFillRate,
			"buyFillRatePerHour":                        buyFillRate,
			"sellFillRatePerHour":                       sellFillRate,
			"selectedHorizon":                           selectedHorizon,
			"orderKeepDuration":                         windowDuration,
			"orderKeepFirstPassageTime":                 orderKeepDecision.CharacteristicFirstPassageTime,
			"orderKeepDistanceBps":                      orderKeepDecision.QuoteDistanceBps,
			"orderKeepReason":                           orderKeepDecision.Reason,
			"buyOrderKeepDistanceBps":                   buyOrderKeepDecision.QuoteDistanceBps,
			"buyOrderKeepFirstPassageTime":              buyOrderKeepDecision.CharacteristicFirstPassageTime,
			"sellOrderKeepDistanceBps":                  sellOrderKeepDecision.QuoteDistanceBps,
			"sellOrderKeepFirstPassageTime":             sellOrderKeepDecision.CharacteristicFirstPassageTime,
			"horizonBuyTouchPosterior":                  horizonDecision.BuyTouchProbability,
			"horizonSellTouchPosterior":                 horizonDecision.SellTouchProbability,
			"horizonScoreStdErrorBpsPerHour":            horizonDecision.ScoreStdErrorBpsHour,
			"horizonScoreBpsPerHour":                    horizonDecision.ScoreBpsPerHour,
			"horizonSelectionScoreBpsPerHour":           selectedHorizonDecision.SelectionScoreBpsPerHour,
			"horizonPreliminarySelected":                preliminarySelectedHorizon,
			"horizonJointCandidateCount":                jointQuoteDecision.HorizonCandidateCount,
			"horizonJointEffectiveSamples":              jointQuoteDecision.HorizonEffectiveSamples,
			"horizonJointReliability":                   jointQuoteDecision.HorizonReliability,
			"horizonJointRawUtilityJPYHour":             jointQuoteDecision.HorizonRawUtilityJPYHour,
			"horizonJointSelectionUtilityJPYHour":       jointQuoteDecision.HorizonSelectionUtilityJPYHour,
			"horizonMarginalBuyEvaluated":               selectedHorizonDecision.MarginalBuyEvaluated,
			"horizonMarginalBuyNotionalJPY":             selectedHorizonDecision.MarginalBuyNotionalJPY,
			"horizonMarginalBuyTargetNotionalJPY":       selectedHorizonDecision.MarginalBuyTargetNotionalJPY,
			"horizonMarginalBuyTargetUpProbability":     selectedHorizonDecision.MarginalBuyTargetUpProbability,
			"horizonMarginalBuyCEJPY":                   selectedHorizonDecision.MarginalBuyCertaintyEquivalentJPY,
			"horizonMarginalBuyUtilityBpsPerHour":       selectedHorizonDecision.MarginalBuyUtilityBpsPerHour,
			"horizonUpPerHour":                          horizonDecision.UpCrossesPerHour,
			"horizonDownPerHour":                        horizonDecision.DownCrossesPerHour,
			"horizonEstimatorSource":                    horizonDecision.EstimatorSource,
			"horizonEffectiveSamples":                   horizonDecision.EffectiveSamples,
			"horizonOnlineFastWeight":                   horizonDecision.OnlineFastWeight,
			"horizonDecisionReason":                     horizonDecision.Reason,
			"acquisitionResetEnabled":                   acquisitionCfg.Enabled,
			"acquisitionResetReason":                    acquisitionDecision.Reason,
			"acquisitionDeficitAge":                     acquisitionDecision.Age,
			"acquisitionAdverseMoveBps":                 acquisitionDecision.AdverseMoveBps,
			"acquisitionAdverseMoveThresholdBps":        acquisitionDecision.AdverseMoveThresholdBps,
			"acquisitionUpProbabilityLower":             acquisitionDecision.UpProbabilityLower,
			"acquisitionUpRateLowerPerHour":             acquisitionDecision.UpRateLowerPerHour,
			"acquisitionDownRateUpperPerHour":           acquisitionDecision.DownRateUpperPerHour,
			"acquisitionPassiveFillProbability":         acquisitionDecision.PassiveBidFillProbability,
			"acquisitionExitFillProbability":            acquisitionDecision.MakerExitFillProbability,
			"acquisitionWaitValueBps":                   acquisitionDecision.PassiveWaitValueBps,
			"acquisitionIOCValueBps":                    acquisitionDecision.IOCValueBps,
			"acquisitionIOCImprovementBps":              acquisitionDecision.IOCImprovementBps,
			"earlyBumpPhase":                            earlyBumpDecision.Phase,
			"earlyBumpSignal":                           earlyBumpDecision.Signal,
			"earlyBumpApplied":                          earlyBumpDecision.Apply,
			"earlyBumpRefresh":                          earlyBumpDecision.Refresh,
			"earlyBumpReason":                           earlyBumpDecision.Reason,
			"earlyBumpBid":                              earlyBumpDecision.BidPrice,
			"earlyBumpDeltaBps":                         earlyBumpDecision.DeltaBps,
			"earlyBumpDrawdownBps":                      fastEvidence.MidDrawdownBps,
			"earlyBumpDrawdownWindow":                   fastEvidence.MidDrawdownWindow,
			"earlyBumpProbability":                      earlyBumpDecision.ActivationProbability,
			"earlyBumpProbabilityLower":                 earlyBumpDecision.ActivationProbabilityLow,
			"earlyBumpBaselineProbabilityUpper":         earlyBumpDecision.BaselineProbabilityHigh,
			"inventoryMin":                              inventoryBand.MinInventory,
			"inventoryTarget":                           quoteConfig.InventoryTarget,
			"inventoryLimit":                            quoteConfig.InventoryLimit,
			"inventoryMax":                              inventoryBand.MaxInventory,
			"inventoryTargetRatio":                      inventoryBand.TargetRatio,
			"inventoryControlReason":                    inventoryControl.Reason,
			"fastInventoryTradingZone":                  inventoryControl.FastTradingZone,
			"longHorizonInventoryAdjustment":            longHorizonInventoryAdjustment,
			"fastInventoryMin":                          fastInventoryBand.MinInventory,
			"fastInventoryTarget":                       fastInventoryBand.Target,
			"fastInventoryMax":                          fastInventoryBand.MaxInventory,
			"pairEquityJPY":                             pairEquityJPY,
			"macroInventoryEnabled":                     macroDecision.Enabled,
			"macroInventoryHealthy":                     macroDecision.Healthy,
			"macroInventoryReason":                      macroDecision.Reason,
			"macroBarInterval":                          time.Duration(quoteConfig.MacroInventory.BarInterval),
			"macroLatestClosedBarAt":                    macroLatestClosedBarAt,
			"macroEstimateCacheBuilds":                  s.makerMacroInventoryModel.estimateCacheBuilds,
			"macroReversalCacheBuilds":                  s.makerMacroInventoryModel.reversalCacheBuilds,
			"macroWealthPeakJPY":                        macroDecision.WealthPeakJPY,
			"macroWealthDrawdownRatio":                  macroDecision.DrawdownRatio,
			"macroCurrentRiskyWeight":                   macroDecision.CurrentRiskyWeight,
			"macroPriorTargetRatio":                     macroDecision.PriorTargetRatio,
			"macroUtilityTargetRatio":                   macroDecision.UtilityTargetRatio,
			"macroUtilityWeightSum":                     macroDecision.UtilityWeightSum,
			"macroUtilityHorizons":                      macroDecision.UtilityHorizons,
			"macroTargetRatio":                          macroDecision.TargetRatio,
			"macroNoTradeEnabled":                       macroDecision.NoTrade.Enabled,
			"macroNoTradeHealthy":                       macroDecision.NoTrade.Healthy,
			"macroNoTradeReason":                        macroDecision.NoTrade.Reason,
			"macroNoTradeContinuationMixtureApplied":    macroDecision.NoTrade.ContinuationMixtureApplied,
			"macroNoTradePosteriorUp":                   macroDecision.NoTrade.PosteriorUpProbability,
			"macroNoTradeMicroCrossingUp":               modelSnapshot.Up,
			"macroNoTradeMicroCrossingDown":             modelSnapshot.Down,
			"macroNoTradeExecutableCrossingUp":          executableCrossingSnapshot.Up,
			"macroNoTradeExecutableCrossingDown":        executableCrossingSnapshot.Down,
			"macroNoTradeExecutableCrossingHealth":      executableCrossingSnapshot.Health,
			"macroNoTradeMicroSignedDirection":          macroDecision.NoTrade.MicroSignedDirection,
			"macroNoTradeExecutableSignedDirection":     macroDecision.NoTrade.ExecutableSignedDirection,
			"macroNoTradeSignedDirection":               macroDecision.NoTrade.SignedDirection,
			"macroNoTradeDriftPerQV":                    macroDecision.NoTrade.DriftPerQV,
			"macroNoTradeQVRatePerSecond":               macroDecision.NoTrade.QVRatePerSecond,
			"macroNoTradeForecastVariance":              macroDecision.NoTrade.ForecastVariance,
			"macroNoTradeForecastReturnBps":             macroDecision.NoTrade.ForecastReturn * 10_000,
			"macroNoTradeForecastReturnSE":              macroDecision.NoTrade.ForecastReturnSE,
			"macroNoTradeForecastEdgeLowerBps":          macroDecision.NoTrade.ForecastEdgeLowerBps,
			"macroNoTradeHoldProtectionApplied":         macroDecision.NoTrade.HoldProtectionApplied,
			"macroNoTradeRiskReductionGrossUtilityBps":  macroDecision.NoTrade.RiskReductionGrossUtilityBps,
			"macroNoTradeRiskReductionNetUtilityBps":    macroDecision.NoTrade.RiskReductionNetUtilityBps,
			"macroNoTradeRawAimRatio":                   macroDecision.NoTrade.RawAimRatio,
			"macroNoTradeAimRatio":                      macroDecision.NoTrade.AimRatio,
			"macroNoTradeAimMeasurementVariance":        macroDecision.NoTrade.AimMeasurementVariance,
			"macroNoTradeAimFilterVariance":             macroDecision.NoTrade.AimFilterVariance,
			"macroNoTradeAimKalmanGain":                 macroDecision.NoTrade.AimKalmanGain,
			"macroNoTradeAimUpdatedAt":                  macroDecision.NoTrade.AimUpdatedAt,
			"macroNoTradeLowerRatio":                    macroDecision.NoTrade.LowerRatio,
			"macroNoTradeUpperRatio":                    macroDecision.NoTrade.UpperRatio,
			"macroNoTradeExecutionTargetRatio":          macroDecision.NoTrade.ExecutionTargetRatio,
			"macroNoTradeDirection":                     macroDecision.NoTrade.Direction,
			"macroNoTradeBuyHalfWidthRatio":             macroDecision.NoTrade.BuyHalfWidthRatio,
			"macroNoTradeSellHalfWidthRatio":            macroDecision.NoTrade.SellHalfWidthRatio,
			"macroTrendEnabled":                         macroDecision.NoTrade.TrendExcursion.Enabled,
			"macroTrendHealthy":                         macroDecision.NoTrade.TrendExcursion.Healthy,
			"macroTrendReason":                          macroDecision.NoTrade.TrendExcursion.Reason,
			"macroTrendSamples":                         macroDecision.NoTrade.TrendExcursion.Samples,
			"macroTrendNeighbors":                       macroDecision.NoTrade.TrendExcursion.Neighbors,
			"macroTrendDirection":                       macroDecision.NoTrade.TrendExcursion.Direction,
			"macroTrendPosteriorUp":                     macroDecision.NoTrade.TrendExcursion.PosteriorUpProbability,
			"macroTrendProfitableProbability":           macroDecision.NoTrade.TrendExcursion.ProfitableProbability,
			"macroTrendModelProbability":                macroDecision.NoTrade.TrendExcursion.ModelProbability,
			"macroTrendTerminalReturnBps":               macroDecision.NoTrade.TrendExcursion.TerminalExpectedReturn * 10_000,
			"macroTrendStructuralDirection":             macroDecision.NoTrade.TrendExcursion.StructuralDirection,
			"macroTrendStructuralProbability":           macroDecision.NoTrade.TrendExcursion.StructuralProbability,
			"macroTrendStructuralExcursionBps":          macroDecision.NoTrade.TrendExcursion.StructuralExcursion * 10_000,
			"macroTrendExpectedReturnBps":               macroDecision.NoTrade.TrendExcursion.ExpectedReturn * 10_000,
			"macroTrendRemainingExcursionBps":           macroDecision.NoTrade.TrendExcursion.RemainingExcursion * 10_000,
			"macroTrendExpectedPivot":                   macroDecision.NoTrade.TrendExcursion.ExpectedPivot,
			"macroRollingRawSamples":                    macroDecision.RawSamples,
			"macroRollingEffectiveSamples":              macroDecision.EffectiveSamples,
			"macroRollingLatestReturnBps":               macroDecision.LatestReturn * 10_000,
			"macroReversalEnabled":                      reversalDecision.Enabled,
			"macroReversalDirection":                    reversalDecision.Direction,
			"macroReversalInventoryAdjustmentRatio":     reversalDecision.InventoryAdjustmentRatio,
			"macroReversalHealthy":                      reversalDecision.Healthy,
			"macroReversalApplied":                      reversalDecision.Applied,
			"macroReversalReason":                       reversalDecision.Reason,
			"macroReversalBaselineTargetRatio":          reversalDecision.BaselineTargetRatio,
			"macroReversalTargetRatio":                  reversalDecision.TargetRatio,
			"macroReversalProbability":                  reversalDecision.AggregateProbability,
			"macroReversalNetEdgeBps":                   reversalDecision.AggregateNetEdgeBps,
			"macroReversalHealthyHorizons":              reversalDecision.HealthyHorizons,
			"macroReversalActiveHorizons":               reversalDecision.ActiveHorizons,
			"macroReversalEarlyHorizons":                reversalDecision.EarlyHorizons,
			"macroReversalAdditionalHeadroomRatio":      reversalDecision.AdditionalHeadroomRatio,
			"macroReversalHorizons":                     reversalDecision.HorizonSummary,
			"macroRegimeSignalChangeAt":                 reversalDecision.SignalChangeAt,
			"macroRegimeSignalForecastHorizon":          reversalDecision.SignalForecastHorizon,
			"macroRegimeLeaseApplied":                   reversalDecision.LeaseApplied,
			"macroRegimeLeaseAge":                       reversalDecision.LeaseAge,
			"macroRegimeLeaseSurvivalProbability":       reversalDecision.LeaseSurvivalProbability,
			"macroActiveExecutionEnabled":               false,
			"macroActiveExecutionTrigger":               macroActiveExecution.Trigger,
			"macroActiveExecutionReason":                macroActiveExecution.Reason,
			"macroActiveExecutionTargetGapBase":         macroActiveExecution.TargetGapBase,
			"macroActiveExecutionDepthCapBase":          macroActiveExecution.DepthCapBase,
			"macroActiveExecutionTouchRateUpper":        macroActiveExecution.PassiveTouchRateUpperPerHour,
			"macroActiveExecutionExpectedWait":          macroActiveExecution.ExpectedPassiveWait,
			"macroActiveExecutionWaitLossBps":           macroActiveExecution.WaitLossBps,
			"macroActiveExecutionCrossCostBps":          macroActiveExecution.PassiveToTouchCostBps + macroActiveExecution.FeeIncrementBps,
			"macroActiveExecutionMaximumImpactBps":      macroActiveExecution.MaximumImpactBps,
			"macroActiveExecutionWorstPrice":            macroActiveExecution.WorstPrice,
			"fastTargetExecutionEnabled":                quoteConfig.FastTargetExecution.Enabled,
			"fastTargetExecutionTrigger":                fastTargetExecution.Trigger,
			"fastTargetExecutionReason":                 fastTargetExecution.Reason,
			"fastTargetExecutionDirection":              fastTargetExecution.Direction,
			"fastTargetExecutionTargetGapBase":          fastTargetExecution.TargetGapBase,
			"fastTargetExecutionResidualMakerGapBase":   fastTargetExecution.ResidualMakerGapBase,
			"fastTargetExecutionTouchProbability":       fastTargetExecution.PassiveTouchProbability,
			"fastTargetExecutionTouchProbabilityUpper":  fastTargetExecution.PassiveTouchProbabilityUpper,
			"fastTargetExecutionMissProbabilityLower":   fastTargetExecution.PassiveMissProbabilityLower,
			"fastTargetExecutionExpectedAdverseMoveBps": fastTargetExecution.ExpectedAdverseMoveBps,
			"fastTargetExecutionWaitLossBps":            fastTargetExecution.WaitLossBps,
			"fastTargetExecutionCrossCostBps":           fastTargetExecution.ExecutionCostBps,
			"fastTargetExecutionRawPassiveDistanceBps":  fastTargetExecution.PassiveToTouchCostBps,
			"fastTargetExecutionWeightedPassiveBps":     fastTargetExecution.ProbabilityWeightedPassiveCostBps,
			"fastTargetExecutionExpectedFeeBps":         fastTargetExecution.ExpectedExecutionFeeBps,
			"fastTargetExecutionDownsideActive":         fastTargetExecution.PersistentDownsideActive,
			"fastTargetExecutionDownsideEValue":         fastTargetExecution.PersistentDownsideEValue,
			"fastTargetExecutionDownsideForecastBps":    fastTargetExecution.PersistentDownsideForecastBps,
			"fastTargetExecutionUpsideActive":           fastTargetExecution.PersistentUpsideActive,
			"fastTargetExecutionUpsideEValue":           fastTargetExecution.PersistentUpsideEValue,
			"fastTargetExecutionUpsideForecastBps":      fastTargetExecution.PersistentUpsideForecastBps,
			"fastTargetExecutionVariancePenaltyBps":     fastTargetExecution.InventoryVariancePenaltyBps,
			"fastTargetExecutionActiveCEBps":            fastTargetExecution.ActiveCertaintyEquivalentBps,
			"fastTargetExecutionMaximumImpactBps":       fastTargetExecution.MaximumImpactBps,
			"fastTargetExecutionQuantity":               fastTargetExecution.Quantity,
			"fastTargetExecutionWorstPrice":             fastTargetExecution.WorstPrice,
			"macroCapitalCapRatio":                      macroDecision.CapitalCapRatio,
			"macroCapitalFloorRatio":                    macroDecision.CapitalFloorRatio,
			"macroCarryCapRatio":                        macroDecision.CarryCapRatio,
			"macroCarryFloorRatio":                      macroDecision.CarryFloorRatio,
			"macroDrawdownCapRatio":                     macroDecision.DrawdownCapRatio,
			"macroLimitingHorizon":                      macroDecision.LimitingHorizon,
			"macroReturnMeanBps":                        macroDecision.ReturnMean * 10_000,
			"macroReturnShrunkMeanBps":                  macroDecision.ReturnShrunkMean * 10_000,
			"macroReturnStdDevBps":                      macroDecision.ReturnStdDev * 10_000,
			"macroDownsideLossBps":                      macroDecision.DownsideLoss * 10_000,
			"macroReturnSamples":                        macroDecision.Samples,
			"macroUsedFallback":                         macroDecision.UsedFallback,
			"macroFallbackVarianceWeight":               macroDecision.FallbackVarianceWeight,
			"inventoryRiskBudgetEffectiveJPY":           effectiveRiskBudgetJPY,
			"inventoryCapitalMinJPY":                    inventoryBand.CapitalMinNotionalJPY,
			"inventoryCapitalTargetJPY":                 inventoryBand.CapitalTargetNotionalJPY,
			"inventoryCapitalCapJPY":                    inventoryBand.CapitalCapNotionalJPY,
			"inventoryExpectedTargetRatio":              inventoryVariation.ExpectedTargetRatio,
			"inventoryVariationLowerRatio":              inventoryVariation.LowerRatio,
			"inventoryVariationUpperRatio":              inventoryVariation.UpperRatio,
			"inventoryVariationExpectedFills":           inventoryVariation.ExpectedFillEvents,
			"inventoryVariationWindow":                  inventoryVariationHorizon,
			"inventoryVariationWindowSource":            inventoryVariationHorizonSource,
			"inventoryVariationStdDevJPY":               inventoryVariation.InventoryStdDevJPY,
			"inventoryVariationHalfWidthJPY":            inventoryVariation.HalfWidthJPY,
			"inventoryExecutableOrderNotionalJPY":       inventoryVariation.ExecutableOrderNotionalJPY,
			"inventoryEffectiveMinJPY":                  inventoryBand.MinInventory * mid,
			"inventoryEffectiveTargetJPY":               inventoryBand.Target * mid,
			"inventoryEffectiveMaxJPY":                  inventoryBand.MaxInventory * mid,
			"inventoryRiskBandHalfWidthJPY":             inventoryBand.RiskBandHalfWidthNotionalJPY,
			"inventoryBuyHeadroomJPY":                   buyInventoryHeadroom,
			"inventorySellHeadroomJPY":                  sellInventoryHeadroom * mid,
			"inventoryHardBuyHeadroomJPY":               hardBuyInventoryHeadroom,
			"inventoryHardSellHeadroomJPY":              hardSellInventoryHeadroom * mid,
			"inventoryHardMinJPY":                       hardInventoryBand.MinInventory * mid,
			"inventoryHardMaxJPY":                       hardInventoryBand.MaxInventory * mid,
			"quantityProjectionEnabled":                 quoteConfig.ProbabilityCenteredQuantity.Enabled,
			"quantityProjectionShadowOnly":              quoteConfig.ProbabilityCenteredQuantity.ShadowOnly,
			"fastQuantityOwner":                         "probability-centered-fast",
			"fastQuantityFallbackUsed":                  fastQuantityFallbackUsed,
			"fastQuantityBaselineBuyCapJPY":             fastQuantityCapacity.BaselineBuyCapJPY,
			"fastQuantityBaselineSellCapJPY":            fastQuantityCapacity.BaselineSellCapJPY,
			"fastQuantityPathModelBuyCapJPY":            fastQuantityCapacity.PathModelBuyCapJPY,
			"fastQuantityPathModelSellCapJPY":           fastQuantityCapacity.PathModelSellCapJPY,
			"fastQuantityMultiCellPromoted":             probabilityProjection.BuyNotionalJPY > executableOrderNotionalJPY+1e-9 || probabilityProjection.SellNotionalJPY > executableOrderNotionalJPY+1e-9,
			"riskUtilizationSizingEnabled":              riskUtilizationSizing.Enabled,
			"riskUtilizationSizingReason":               riskUtilizationSizing.Reason,
			"riskUtilizationRiskMultiplier":             riskUtilizationSizing.RiskMultiplier,
			"riskUtilizationBuyMultiplier":              riskUtilizationSizing.BuyMultiplier,
			"riskUtilizationSellMultiplier":             riskUtilizationSizing.SellMultiplier,
			"riskUtilizationBuyCapJPY":                  riskUtilizationSizing.BuyNotionalCapJPY,
			"riskUtilizationSellCapJPY":                 riskUtilizationSizing.SellNotionalCapJPY,
			"riskUtilizationExposureRatio":              riskUtilizationSizing.CurrentExposureRatio,
			"riskUtilizationBuyHeadroomRatio":           riskUtilizationSizing.BuyHeadroomRatio,
			"riskUtilizationSellHeadroomRatio":          riskUtilizationSizing.SellHeadroomRatio,
			"riskUtilizationGrossCapitalRatio":          riskUtilizationSizing.GrossUtilizationRatio,
			"inventoryHeadroomExceeded":                 inventoryHeadroomExceeded,
			"quoteNotionalDynamic":                      dynamicQuoteNotional,
			"quantityProjectionActive":                  probabilityProjectionUsed,
			"quantityProjectionReason":                  probabilityProjection.Reason,
			"quantityProjectionBuyProbability":          probabilityProjection.BuyFillProbability,
			"quantityProjectionSellProbability":         probabilityProjection.SellFillProbability,
			"quantityProjectionBothProbability":         probabilityProjection.BothFillProbability,
			"quantityProjectionFillCovariance":          probabilityProjection.FillCovariance,
			"quantityProjectionFastGrossJPY":            probabilityProjection.FastGrossNotionalJPY,
			"quantityProjectionGrossJPY":                probabilityProjection.ProjectedGrossNotionalJPY,
			"quantityProjectionCycleBuyJPY":             probabilityProjection.CycleBuyNotionalJPY,
			"quantityProjectionCycleSellJPY":            probabilityProjection.CycleSellNotionalJPY,
			"quantityProjectionTargetRestoringBuyJPY":   probabilityProjection.TargetRestoringBuyJPY,
			"quantityProjectionTargetRestoringSellJPY":  probabilityProjection.TargetRestoringSellJPY,
			"quantityProjectionExpectedInventoryJPY":    probabilityProjection.ExpectedInventoryNotionalJPY,
			"quantityProjectionStdDevJPY":               probabilityProjection.InventoryStdDevJPY,
			"quantityProjectionConfidenceLowerJPY":      probabilityProjection.ConfidenceLowerNotionalJPY,
			"quantityProjectionConfidenceUpperJPY":      probabilityProjection.ConfidenceUpperNotionalJPY,
			"quantityProjectionTargetErrorJPY":          probabilityProjection.TargetErrorJPY,
			"quantityProjectionDesiredInventoryJPY":     probabilityProjection.DesiredInventoryNotionalJPY,
			"quantityProjectionMinBuyJPY":               projectionInput.MinBuyNotionalJPY,
			"quantityProjectionMaxBuyJPY":               projectionInput.MaxBuyNotionalJPY,
			"quantityProjectionMinSellJPY":              projectionInput.MinSellNotionalJPY,
			"quantityProjectionMaxSellJPY":              projectionInput.MaxSellNotionalJPY,
			"jointQuoteEnabled":                         quoteConfig.JointDistanceQuantity.Enabled,
			"jointQuoteShadowOnly":                      quoteConfig.JointDistanceQuantity.ShadowOnly,
			"jointQuoteActive":                          jointQuoteDecision.Applied,
			"jointQuoteAuthoritativeRejection":          jointQuoteDecision.AuthoritativeRejection,
			"jointQuoteSideSafeFallback":                jointQuoteDecision.SideSafeFallback,
			"jointQuoteContinuityFloorApplied":          jointQuoteDecision.ContinuityFloorApplied,
			"jointQuoteContinuityFloorReason":           jointQuoteDecision.ContinuityFloorReason,
			"jointQuotePreserveTwoSidedQuotes":          quoteConfig.JointDistanceQuantity.PreserveTwoSidedQuotes,
			"jointQuoteCompletionProtected":             jointQuoteDecision.CompletionProtected,
			"jointQuoteFallbackBuySupported":            jointQuoteDecision.FallbackBuySupported,
			"jointQuoteFallbackSellSupported":           jointQuoteDecision.FallbackSellSupported,
			"jointQuoteFallbackBuyScore":                jointQuoteDecision.FallbackBuyScore,
			"jointQuoteFallbackSellScore":               jointQuoteDecision.FallbackSellScore,
			"jointQuoteReason":                          jointQuoteDecision.Reason,
			"jointQuoteCandidateCount":                  jointQuoteDecision.CandidateCount,
			"jointQuoteSelectedCandidate":               jointQuoteDecision.SelectedCandidate,
			"jointQuoteSelectedQuantityCandidate":       jointQuoteDecision.SelectedQuantityCandidate,
			"jointQuoteConditionalEnabled":              quoteConfig.ConditionalExecution.Enabled,
			"jointQuoteInwardBuyEligible":               jointQuoteDecision.InwardBuyEligible,
			"jointQuoteInwardSellEligible":              jointQuoteDecision.InwardSellEligible,
			"jointQuoteInwardBuySelected":               jointQuoteDecision.InwardBuySelected,
			"jointQuoteInwardSellSelected":              jointQuoteDecision.InwardSellSelected,
			"jointQuoteSelectedInwardBuyDeltaBps":       jointQuoteDecision.SelectedInwardBuyDeltaBps,
			"jointQuoteSelectedInwardSellDeltaBps":      jointQuoteDecision.SelectedInwardSellDeltaBps,
			"jointQuoteConditionalBuyDeltaMeanBps":      jointQuoteDecision.ConditionalBuy.ExpectedPairedDeltaBps,
			"jointQuoteConditionalSellDeltaMeanBps":     jointQuoteDecision.ConditionalSell.ExpectedPairedDeltaBps,
			"jointQuoteConditionalBuySamples":           jointQuoteDecision.ConditionalBuy.EffectiveSamples,
			"jointQuoteConditionalSellSamples":          jointQuoteDecision.ConditionalSell.EffectiveSamples,
			"jointQuoteQuantityScale":                   jointQuoteDecision.QuantityScale,
			"jointQuoteCycleBuyJPY":                     jointQuoteDecision.Projection.CycleBuyNotionalJPY,
			"jointQuoteCycleSellJPY":                    jointQuoteDecision.Projection.CycleSellNotionalJPY,
			"jointQuoteTargetRestoringBuyJPY":           jointQuoteDecision.Projection.TargetRestoringBuyJPY,
			"jointQuoteTargetRestoringSellJPY":          jointQuoteDecision.Projection.TargetRestoringSellJPY,
			"jointQuoteExpectedCycleJPY":                jointQuoteDecision.ExpectedCycleJPY,
			"jointQuoteExpectedPnLJPYHour":              jointQuoteDecision.ExpectedPnLJPYHour,
			"jointQuoteLowerPnLJPYHour":                 jointQuoteDecision.LowerPnLJPYHour,
			"jointQuotePathStdErrorJPYHour":             jointQuoteDecision.PathStdErrorJPYHour,
			"jointQuoteKellyPenaltyJPYHour":             jointQuoteDecision.KellyPenaltyJPYHour,
			"jointQuoteKellyUtilityJPYHour":             jointQuoteDecision.KellyUtilityJPYHour,
			"jointQuoteFeeValueMeanJPY":                 jointQuoteDecision.FeeValueMeanJPY,
			"jointQuoteFeeValueDownsideRegretJPY":       jointQuoteDecision.FeeValueDownsideRegretJPY,
			"jointQuoteFeeValueNetJPY":                  jointQuoteDecision.FeeValueNetJPY,
			"jointQuotePathPositiveConfidence":          jointQuoteDecision.PathPositiveConfidence,
			"jointQuotePathEffectiveSamples":            jointQuoteDecision.PathEffectiveSamples,
			"jointQuoteCapitalUtilization":              jointQuoteDecision.CapitalUtilization,
			"jointQuotePairCapitalUtilization":          jointQuoteDecision.PairCapitalUtilization,
			"postFillUtilityEnabled":                    postFillUtilityDecision.Enabled,
			"postFillUtilityApplied":                    postFillUtilityDecision.Applied,
			"postFillUtilityReason":                     postFillUtilityDecision.Reason,
			"postFillUtilitySide":                       postFillUtilityDecision.Side,
			"postFillUtilityBaseDistanceBps":            postFillUtilityDecision.BaseDistanceBps,
			"postFillUtilitySelectedDistanceBps":        postFillUtilityDecision.SelectedDistanceBps,
			"postFillUtilityIncrementalMeanBps":         postFillUtilityDecision.IncrementalMeanBps,
			"postFillUtilityExpectedMeanBps":            postFillUtilityDecision.ExpectedMeanBps,
			"jointQuoteExistingInventoryExpectedPnLJPY": jointQuoteDecision.ExistingInventoryExpectedPnLJPY,
			"jointQuoteBaselineVarianceJPY2":            jointQuoteDecision.BaselineVarianceJPY2,
			"jointQuoteWholePositionVarianceJPY2":       jointQuoteDecision.WholePositionVarianceJPY2,
			"jointQuoteMarginalVarianceJPY2":            jointQuoteDecision.MarginalVarianceJPY2,
			"jointQuoteInventoryOrderCovarianceJPY2":    jointQuoteDecision.InventoryOrderCovarianceJPY2,
			"jointQuoteRiskReducing":                    jointQuoteDecision.RiskReducing,
			"jointQuoteRiskTargetNotionalJPY":           projectionInput.TargetInventoryNotionalJPY,
			"jointQuoteRiskInventoryDeviationJPY":       projectionInput.CurrentInventoryNotionalJPY - projectionInput.TargetInventoryNotionalJPY,
			"fastDownsideBuyCapApplied":                 jointQuoteDecision.DownsideBuyCapApplied,
			"fastDownsideInventoryReturnMeanBps":        jointQuoteDecision.DownsideInventoryReturnMeanBps,
			"fastDownsideInventoryReturnSEBps":          jointQuoteDecision.DownsideInventoryReturnSEBps,
			"fastDownsideInventoryReturnUpperBps":       jointQuoteDecision.DownsideInventoryReturnUpperBps,
			"fastDownsideEffectiveSamples":              jointQuoteDecision.DownsideEffectiveSamples,
			"fastDownsideOriginalMaxBuyJPY":             jointQuoteDecision.DownsideOriginalMaxBuyJPY,
			"fastBuyAdmissionEvaluated":                 jointQuoteDecision.BuyAdmissionEvaluated,
			"fastBuyAdmissionApplied":                   jointQuoteDecision.BuyAdmissionApplied,
			"fastBuyAdmissionMaximumJPY":                jointQuoteDecision.BuyAdmissionMaximumJPY,
			"fastBuyAdmissionUtilityBoundJPY":           jointQuoteDecision.BuyAdmissionUtilityBoundJPY,
			"fastBuyAdmissionReason":                    jointQuoteDecision.BuyAdmissionReason,
			"fastSellAdmissionEvaluated":                jointQuoteDecision.SellAdmissionEvaluated,
			"fastSellAdmissionApplied":                  jointQuoteDecision.SellAdmissionApplied,
			"fastSellAdmissionMaximumJPY":               jointQuoteDecision.SellAdmissionMaximumJPY,
			"fastSellAdmissionUtilityBoundJPY":          jointQuoteDecision.SellAdmissionUtilityBoundJPY,
			"fastSellAdmissionReason":                   jointQuoteDecision.SellAdmissionReason,
			"fastAdmissionJointCEJPY":                   jointQuoteDecision.AdmissionJointCEJPY,
			"fastAdmissionJointComplementary":           jointQuoteDecision.AdmissionJointComplementary,
			"wholePositionNotionalJPY":                  inventoryBase * mid,
			"wholePositionRiskyWeight":                  currentRiskyWeight,
			"wholePositionLiquidationMarkPrice":         liquidationMarkPrice,
			"wholePositionLiquidationNotionalJPY":       liquidationNotionalJPY,
			"wholePositionLiquidationEquityJPY":         liquidationEquityJPY,
			"wholePositionLiquidationRiskyWeight":       liquidationRiskyWeight,
			"wholePositionTargetWeight":                 effectiveInventoryTargetRatio,
			"wholePositionTargetGapJPY":                 inventoryTargetGapJPY,
			"wholePositionRiskUsedJPY":                  inventoryRiskUsedJPY,
			"wholePositionRiskBudgetUtilization":        inventoryRiskUtilization,
			"wholePositionUnrealizedPnLJPY":             inventoryBase * (mid - averageCost),
			"wholePositionAverageCost":                  averageCost,
			"postFillUtilityIncrementalSEBps":           postFillUtilityDecision.IncrementalStdErrorBps,
			"postFillUtilityIncrementalLowerBps":        postFillUtilityDecision.IncrementalLowerBps,
			"postFillUtilityExpectedSEBps":              postFillUtilityDecision.ExpectedStdErrorBps,
			"postFillUtilityExpectedLowerBps":           postFillUtilityDecision.ExpectedLowerBps,
			"postFillUtilityFillProbability":            postFillUtilityDecision.FillProbability,
			"postFillUtilityEffectiveSamples":           postFillUtilityDecision.EffectiveSamples,
			"postFillUtilityCycleEdgeBps":               postFillUtilityDecision.CycleEdgeBps,
			"postFillUtilityInventoryRiskBenefitBps":    postFillUtilityDecision.InventoryRiskBenefitBps,
			"postFillUtilityMarginalNotionalJPY":        executableOrderNotionalJPY,

			"quantityProjectionTargetContraction":   probabilityProjection.TargetContraction,
			"fastReservationEnabled":                fastReservation.Enabled,
			"fastReservationReason":                 fastReservation.Reason,
			"fastReservationForecastReturnBps":      fastReservation.ForecastReturnBps,
			"fastReservationForecastSEBps":          fastReservation.ForecastReturnSEBps,
			"fastReservationAdverseProbability":     fastReservation.AdverseProbability,
			"fastReservationStrength":               fastReservation.Strength,
			"fastReservationPathEfficiency":         fastReservation.PathEfficiency,
			"fastReservationShiftBps":               fastReservation.ReservationShiftBps,
			"fastReservationApplied":                fastReservationUtility.Applied,
			"fastReservationUtilityReason":          fastReservationUtility.Reason,
			"fastReservationUtilitySamples":         fastReservationUtility.EffectiveSamples,
			"fastReservationExpectedPnLJPY":         fastReservationUtility.ExpectedPnLJPY,
			"fastReservationCertaintyEquivalentJPY": fastReservationUtility.CertaintyEquivalent,
			"quantityProjectionFastBuyRestraint":    probabilityProjection.FastBuyRestraint,
			"quantityProjectionFastSellRestraint":   probabilityProjection.FastSellRestraint,
			"quantityProjectionBuyRetention":        probabilityProjection.BuyRetention,
			"quantityProjectionSellRetention":       probabilityProjection.SellRetention,
			"quantityProjectionUnrestrainedBuyJPY":  unrestrainedFastBuyNotionalJPY,
			"quantityProjectionUnrestrainedSellJPY": unrestrainedFastSellNotionalJPY,
			// inventoryBandOrderSize is a reference-policy quantity, not the
			// exchange quantity. Actual submitted quantities are logged below.
			"inventoryBandOrderSize":           inventoryBand.OrderSize,
			"plannedBidNotionalJPY":            buyQuoteNotional.Float64(),
			"plannedAskNotionalJPY":            sellQuoteNotional.Float64(),
			"effectiveOrderLevels":             quoteConfig.EffectiveOrderLevels(windowDuration, riskSizingSellFillRate, riskSizingBuyFillRate),
			"inventoryRiskMoveBps":             inventoryBand.RiskMoveBps,
			"missingSide":                      missingSide,
			"sideMismatch":                     sideMismatch,
			"fillRefreshPending":               fillRefreshPending,
			"fillRefreshSide":                  fillRefreshSide,
			"fillRefreshAge":                   fillRefreshAge,
			"materialMove":                     materialMove,
			"materialImbalance":                materialImbalance,
			"materialImbalanceObserved":        materialImbalanceObserved,
			"inventoryResetAskDistanceBps":     inventoryResetAskDistanceBps,
			"inventoryResetUpCrosses":          inventoryResetUpCrosses,
			"inventoryResetUpRatePerHour":      inventoryResetUpRatePerHour,
			"inventoryResetFillIntensityValid": inventoryResetFillIntensityValid,
			"adverseAskMoveBps":                adverseAskMoveBps, "adverseBidMoveBps": adverseBidMoveBps,
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
	if plan.AllowBid && !retainBidOnRefresh {
		price := s.Market.TruncatePrice(fixedpoint.NewFromFloat(plan.BidPrice))
		buyCapacity := fixedpoint.Min(quoteableQuote, buyQuoteNotional)
		if hardInventoryBand.MaxInventory > 0 {
			hardHeadroom := inventoryBuyHeadroomNotional(hardInventoryBand, inventoryBase, price.Float64())
			modelCap := hardHeadroom
			if !probabilityProjectionUsed {
				modelCap = riskUtilizationSizing.BuyNotionalCapJPY * price.Float64() / mid
			}
			headroom := makerBuyInventoryCapacity(
				s.Market, price, fixedpoint.NewFromFloat(modelCap),
				fixedpoint.NewFromFloat(hardHeadroom))
			buyCapacity = fixedpoint.Min(buyCapacity, headroom)
		}
		qty, ok := s.Market.GreaterThanMinimalOrderQuantity(types.SideTypeBuy, price, buyCapacity)
		if !ok {
			log.WithFields(logrus.Fields{"side": "BUY", "price": price, "availableQuote": quoteBalances.AvailableQuote, "quoteableQuote": quoteableQuote, "minNotional": s.Market.MinNotional, "minQuantity": s.Market.MinQuantity}).Warn("market-maker bid blocked by exchange quantity filters")
		}
		retainedAskSafe := !retainAskOnRefresh || activeAskPrice <= 0 || price.Float64() < activeAskPrice
		if ok && price.Compare(ticker.Sell) < 0 && retainedAskSafe {
			// Binance LIMIT_MAKER orders reject an explicit timeInForce.
			bidSubmitted = true
			submits = append(submits, types.SubmitOrder{Symbol: s.Symbol, Market: s.Market, Side: types.SideTypeBuy, Type: types.OrderTypeLimitMaker, Price: price, Quantity: qty, ClientOrderID: marketMakerClientOrderID(types.SideTypeBuy), Tag: "gammacapture-mm-bid"})
		}
	}
	if plan.AllowAsk && !retainAskOnRefresh {
		price := makerProtectedAskPrice(s.Market, plan.AskPrice, plan.AskEquityFloor)
		eligibleBase := base
		if hardInventoryBand.MaxInventory > 0 {
			hardHeadroom := inventorySellHeadroomQuantity(hardInventoryBand, inventoryBase)
			modelCap := hardHeadroom
			if !probabilityProjectionUsed {
				modelCap = riskUtilizationSizing.SellNotionalCapJPY / mid
			}
			inventoryCapacity := makerSellInventoryCapacity(
				s.Market, price, fixedpoint.NewFromFloat(modelCap),
				fixedpoint.NewFromFloat(hardHeadroom))
			eligibleBase = fixedpoint.Min(eligibleBase, inventoryCapacity)
		}
		qty, ok := s.makerAskQuantity(price, eligibleBase, sellQuoteNotional)
		if !ok {
			log.WithFields(logrus.Fields{"side": "SELL", "price": price, "base": base, "minNotional": s.Market.MinNotional, "minQuantity": s.Market.MinQuantity}).Warn("market-maker ask blocked by exchange quantity filters")
		}
		retainedBidSafe := !retainBidOnRefresh || activeBidPrice <= 0 || price.Float64() > activeBidPrice
		if ok && price.Compare(ticker.Buy) > 0 && retainedBidSafe {
			askSubmitted = true
			submits = append(submits, types.SubmitOrder{Symbol: s.Symbol, Market: s.Market, Side: types.SideTypeSell, Type: types.OrderTypeLimitMaker, Price: price, Quantity: qty, ClientOrderID: marketMakerClientOrderID(types.SideTypeSell), Tag: "gammacapture-mm-ask"})
		}
	}
	if fastValueRejected {
		// A no-order action is different from an exchange-infeasible replacement.
		// Preserve an already-resting bilateral pair only when every independent
		// safety/refresh condition says that the old quotes are still valid. This
		// avoids an empty-book interval caused solely by rejecting a new candidate,
		// while crossed, expired, adverse, inventory, lifecycle, and fill-refresh
		// states still fail closed below.
		if preserveActiveTwoSidedQuotesAfterFastRejection(
			quoteConfig.JointDistanceQuantity,
			activeBid, activeAsk, fillRefreshPending,
			quoteCrossed, adverseMove, materialMove, materialImbalance,
			windowExpired, inventoryHeadroomExceeded, fastEdgeLeaseExpired,
			statisticalRealignment, oneSidedTargetRealignment,
			macroTargetRealignment, fastTargetRealignment, reservationRiskRealignment,
			earlyBumpDecision.Refresh, lifecycleReplace,
		) {
			modelUpdateInterval := time.Duration(quoteConfig.HorizonUpdateInterval)
			if modelUpdateInterval <= 0 {
				modelUpdateInterval = windowDuration
			}
			if modelUpdateInterval <= 0 {
				modelUpdateInterval = time.Minute
			}
			s.lastMakerQuoteAt = now
			s.lastMakerMid = mid
			s.lastMakerBestBid = ticker.Buy.Float64()
			s.lastMakerBestAsk = ticker.Sell.Float64()
			s.lastMakerImbalance = imbalance
			s.lastMakerBid = fixedpoint.NewFromFloat(activeBidPrice)
			s.lastMakerAsk = fixedpoint.NewFromFloat(activeAskPrice)
			s.makerTradingWindowStartedAt = now
			s.makerTradingWindowEndsAt = now.Add(modelUpdateInterval)
			s.makerReplacementRetryAfter = now.Add(modelUpdateInterval)
			s.makerNoOrderReferenceBid = 0
			s.makerNoOrderReferenceAsk = 0
			s.State.LastDecision = "Fast rejection: retained safe bilateral maker quotes"
			log.WithFields(logrus.Fields{
				"symbol": s.Symbol, "reason": jointQuoteDecision.Reason,
				"activeBid": activeBidPrice, "activeAsk": activeAskPrice,
				"window": modelUpdateInterval,
			}).Info("market-maker Fast rejection retained safe bilateral quotes")
			return
		}
		// No safe continuity floor is available. A no-order action is different
		// from an exchange-infeasible replacement: keeping the old maker orders
		// would execute the opportunity that the Fast value model just rejected.
		// Cancel only; do not manufacture a replacement or invoke the ordinary
		// ten-second retry loop.
		if len(activeMakerOrders) > 0 {
			if err := s.gracefulCancelMakerOrders(
				ctx, "Fast fee-value rejection", activeMakerOrders...); err != nil {
				log.WithError(err).Warn("market-maker Fast value-rejection cancellation failed")
				return
			}
		}
		modelUpdateInterval := time.Duration(quoteConfig.HorizonUpdateInterval)
		if modelUpdateInterval <= 0 {
			modelUpdateInterval = time.Minute
		}
		s.lastMakerQuoteAt = now
		s.makerTradingWindowStartedAt = now
		s.makerTradingWindowEndsAt = now.Add(modelUpdateInterval)
		s.lastMakerBid = fixedpoint.Zero
		s.lastMakerAsk = fixedpoint.Zero
		s.makerReplacementRetryAfter = now.Add(modelUpdateInterval)
		s.makerNoOrderReferenceBid = ticker.Buy.Float64()
		s.makerNoOrderReferenceAsk = ticker.Sell.Float64()
		s.State.LastDecision = "Fast no-order: " + jointQuoteDecision.Reason
		return
	}
	// Replacement construction, including exchange quantity/notional filters,
	// must finish before any resting order is cancelled. A model ticket can be
	// temporarily unexecutable near the inventory target; destroying a safe
	// opposite-side quote in that state creates a needless empty-book interval.
	if len(submits) == 0 {
		const noReplacementRetryDelay = 10 * time.Second
		s.makerReplacementRetryAfter = now.Add(noReplacementRetryDelay)
		s.makerNoOrderReferenceBid = 0
		s.makerNoOrderReferenceAsk = 0
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
	// A terminal fill can arrive while a normal BBO refresh is planning. Its
	// trade callback is serialized behind marketMakerMu, so defer this stale
	// plan to the balance-aware fill worker instead of submitting twice.
	terminalFillObservedAt := time.Time{}
	if observedAt := s.makerTerminalFillObservedAt.Load(); observedAt > 0 {
		terminalFillObservedAt = time.Unix(0, observedAt)
	}
	if makerTerminalFillDefersReplacement(
		fillRebalanceGeneration, terminalFillSequenceAtStart,
		s.makerTerminalFillSequence.Load(), terminalFillObservedAt, time.Now()) {
		log.WithFields(logrus.Fields{
			"symbol": s.Symbol, "fillSequence": s.makerTerminalFillSequence.Load(),
		}).Info("market-maker normal refresh deferred to terminal-fill rebalance")
		return
	}
	// Cancel only sides that have an executable replacement. A fixed-price side
	// approaching its BBO keeps queue priority across the review boundary.
	ordersToCancel := makerOrdersToCancelForReplacement(activeMakerOrders, retainBidOnRefresh, retainAskOnRefresh)
	if err := s.gracefulCancelMakerOrders(ctx, "quote-refresh-required", ordersToCancel...); err != nil {
		log.WithError(err).Warn("market-maker cancel existing quotes failed")
		s.retryMakerFillRebalanceLocked(fillRebalanceGeneration)
		return
	}
	terminalFillObservedAt = time.Time{}
	if observedAt := s.makerTerminalFillObservedAt.Load(); observedAt > 0 {
		terminalFillObservedAt = time.Unix(0, observedAt)
	}
	if makerTerminalFillDefersReplacement(
		fillRebalanceGeneration, terminalFillSequenceAtStart,
		s.makerTerminalFillSequence.Load(), terminalFillObservedAt, time.Now()) {
		log.WithFields(logrus.Fields{
			"symbol": s.Symbol, "fillSequence": s.makerTerminalFillSequence.Load(),
		}).Info("market-maker post-cancel submission deferred to terminal-fill rebalance")
		return
	}
	if len(submits) > 0 {
		if _, err := s.executor.SubmitOrders(ctx, submits...); err != nil {
			s.State.LastDecision = "maker quote rejected: " + err.Error()
			log.WithError(err).Error("market-maker quote submission failed")
			s.retryMakerFillRebalanceLocked(fillRebalanceGeneration)
		} else {
			submittedBidPrice, submittedAskPrice := makerSubmittedSidePrices(submits)
			submittedBidQuantity, submittedAskQuantity := makerSubmittedSideQuantities(submits)
			if retainBidOnRefresh {
				submittedBidPrice = fixedpoint.NewFromFloat(activeBidPrice)
			}
			if retainAskOnRefresh {
				submittedAskPrice = fixedpoint.NewFromFloat(activeAskPrice)
			}
			log.WithFields(logrus.Fields{
				"orders":                  len(submits),
				"submittedBidQuantity":    submittedBidQuantity,
				"submittedAskQuantity":    submittedAskQuantity,
				"submittedBidNotionalJPY": submittedBidQuantity.Mul(submittedBidPrice),
				"submittedAskNotionalJPY": submittedAskQuantity.Mul(submittedAskPrice),
				"inventoryBandOrderSize":  inventoryBand.OrderSize,
			}).Info("market-maker quotes submitted")
			s.makerReplacementRetryAfter = time.Time{}
			s.makerNoOrderReferenceBid = 0
			s.makerNoOrderReferenceAsk = 0
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
			s.makerQuotedTargetRatio = effectiveInventoryTargetRatio
			s.makerQuotedFastTargetRatio = selectedProjectionTargetRatio
			s.makerQuotedTargetSet = true
			s.makerQuotedFastReservationBps = appliedFastReservationBps
			s.makerHeadroomCancelAt = time.Time{}
			if askSubmitted {
				s.makerAskSince = now
				s.makerAskAnchorMid = mid
			} else if !retainAskOnRefresh {
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

func makerSubmittedSideQuantities(submits []types.SubmitOrder) (bid, ask fixedpoint.Value) {
	for _, order := range submits {
		switch order.Side {
		case types.SideTypeBuy:
			bid = order.Quantity
		case types.SideTypeSell:
			ask = order.Quantity
		}
	}
	return bid, ask
}

func makerOrdersToCancelForReplacement(active []types.Order, retainBid, retainAsk bool) []types.Order {
	orders := make([]types.Order, 0, len(active))
	for _, order := range active {
		if order.Side == types.SideTypeBuy && retainBid {
			continue
		}
		if order.Side == types.SideTypeSell && retainAsk {
			continue
		}
		orders = append(orders, order)
	}
	return orders
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

// makerMinimumExecutableNotional maps both Binance side filters onto quote
// notional and returns the more conservative executable amount. This lets the
// stochastic inventory band absorb at least one real order rather than a
// continuous quantity that the exchange would reject.
func makerMinimumExecutableNotional(market types.Market, bidPrice, askPrice fixedpoint.Value) float64 {
	minimum := market.MinNotional.Float64()
	if capacity, ok := makerMinimumExecutableBuyCapacity(market, bidPrice); ok {
		minimum = math.Max(minimum, capacity.Float64())
	}
	if quantity, ok := makerMinimumExecutableQuantity(market, askPrice); ok {
		minimum = math.Max(minimum, askPrice.Mul(quantity).Float64())
	}
	return minimum
}

// makerBuyInventoryCapacity floors a model tranche to the smallest executable
// BUY only when hard inventory headroom can absorb it. Otherwise the hard band
// remains authoritative and the side is omitted.
func makerBuyInventoryCapacity(market types.Market, price, modelCap, hardCap fixedpoint.Value) fixedpoint.Value {
	// Zero is an authoritative statistical rejection. Only a positive
	// continuous allocation may be rounded up to the exchange lattice.
	if modelCap.Sign() <= 0 {
		return fixedpoint.Zero
	}
	capacity := fixedpoint.Min(modelCap, hardCap)
	if _, capacityOK := market.GreaterThanMinimalOrderQuantity(types.SideTypeBuy, price, capacity); !capacityOK {
		// The padded minimum protects against quote-to-base truncation, but the
		// remaining hard band can be slightly smaller than that padding and still
		// produce an exchange-valid quantity. Validate the hard capacity directly;
		// if valid, use the smaller of it and the padded minimum so the filter
		// reserve cannot incorrectly suppress the BUY side.
		if _, hardOK := market.GreaterThanMinimalOrderQuantity(types.SideTypeBuy, price, hardCap); hardOK {
			minimum, minimumOK := makerMinimumExecutableBuyCapacity(market, price)
			if minimumOK {
				capacity = fixedpoint.Min(minimum, hardCap)
			} else {
				capacity = hardCap
			}
		}
	}
	return fixedpoint.Min(capacity, hardCap)
}

// makerSellInventoryCapacity is the SELL-side equivalent. Its capacity is a
// base quantity because Binance applies the lot-size filter before notional.
func makerSellInventoryCapacity(market types.Market, price, modelCap, hardCap fixedpoint.Value) fixedpoint.Value {
	// Preserve a model-side zero instead of resurrecting a rejected side as an
	// exchange-minimum exploratory order.
	if modelCap.Sign() <= 0 {
		return fixedpoint.Zero
	}
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

type targetCenteredOrderCaps = TargetCenteredOrderCaps

// targetCenteredInventoryOrderCaps separates the hard inventory band from a
// single executable ticket. Corrective orders may move inventory to the target
// in one fill but cannot traverse from one band edge to the opposite edge. At
// the target, the symmetric band half-width is divided by the configured level
// capacity, giving the unified model a small, equity-scaled exploration ticket.
func targetCenteredInventoryOrderCaps(band InventoryBand, inventory, price, maxLevels float64) targetCenteredOrderCaps {
	return TargetCenteredInventoryOrderCaps(band, inventory, price, maxLevels)
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

func makerEmptyBookRetryPending(
	now, retryAfter time.Time,
	activeOrders int,
	fillGeneration uint64,
	referenceBid, referenceAsk, currentBid, currentAsk, moveThresholdBps float64,
) bool {
	if fillGeneration != 0 || activeOrders != 0 || retryAfter.IsZero() ||
		!now.Before(retryAfter) {
		return false
	}
	// A model rejection is conditional on its executable BBO state. Re-open the
	// decision when either side moves materially from that rejection anchor;
	// the next rejection installs a fresh anchor, bounding optimizer work by
	// market-state changes instead of BBO event frequency. Zero references are
	// used by exchange-feasibility retries and retain their fixed retry clock.
	if referenceBid > 0 && referenceAsk >= referenceBid &&
		currentBid > 0 && currentAsk >= currentBid && moveThresholdBps > 0 {
		askMove := math.Abs(math.Log(currentAsk/referenceAsk)) * 10_000
		bidMove := math.Abs(math.Log(currentBid/referenceBid)) * 10_000
		if math.Max(askMove, bidMove) >= moveThresholdBps {
			return false
		}
	}
	return true
}

// makerQuoteRefreshRequired centralizes the quote refresh policy. The minimum
// resting interval is a transport anti-churn floor. Hard lifecycle transitions
// may act after that floor; ordinary market-state changes wait for the modeled
// first-passage keep duration.
func makerQuoteRefreshRequired(elapsed, minRefreshInterval, orderKeepDuration time.Duration, quoteCrossed, windowExpired, adverseMove, materialMove, materialImbalance, missingSide, noActiveOrders, fastEdgeLeaseExpired, statisticalRealignment, earlyTargetRealignment bool) bool {
	return MakerQuoteRefreshRequired(
		elapsed, minRefreshInterval, orderKeepDuration,
		quoteCrossed, windowExpired, adverseMove, materialMove, materialImbalance,
		missingSide, noActiveOrders, fastEdgeLeaseExpired, statisticalRealignment,
		earlyTargetRealignment)
}

// MakerQuoteRefreshRequired is shared with production replay so an empty book
// cannot accidentally inherit the modeled lease that exists only to preserve
// a resting exchange queue position.
func MakerQuoteRefreshRequired(elapsed, minRefreshInterval, orderKeepDuration time.Duration, quoteCrossed, windowExpired, adverseMove, materialMove, materialImbalance, missingSide, noActiveOrders, fastEdgeLeaseExpired, statisticalRealignment, earlyTargetRealignment bool) bool {
	// The caller computes earlyTargetRealignment only after the configured
	// transport floor and an uncertainty-adjusted active-vs-candidate test. It
	// is therefore the sole signal allowed to break the longer first-passage
	// minimum used by ordinary Fast quotes.
	if earlyTargetRealignment {
		return true
	}
	// The no-order retry deadline is the only throttle when there is nothing on
	// the exchange. There is no cancel transport or queue age for the ordinary
	// resting-order minimum interval to protect.
	if noActiveOrders && windowExpired {
		return true
	}
	if elapsed < minRefreshInterval {
		return false
	}
	// A crossed quote or missing/policy-mismatched side is a hard lifecycle
	// transition. An empty book has no queue age to preserve, so its explicit
	// no-order review lease may also wake before a longer modeled keep duration.
	// A newer five-minute model snapshot, Fast edge, or statistical realignment
	// alone does not rewrite the reference window of an actually resting order.
	if quoteCrossed || missingSide {
		return true
	}
	if orderKeepDuration > 0 && elapsed < orderKeepDuration {
		return false
	}
	return windowExpired || adverseMove || materialMove || materialImbalance ||
		fastEdgeLeaseExpired || statisticalRealignment
}

// makerQuoteStatisticalRealignment compares fee-adjusted edge/hour using the
// Jeffreys-posterior uncertainty produced by side-specific BBO observations.
// Repricing is allowed only when the candidate improvement exceeds the
// configured z-score times the conservative independent-error bound.
func makerQuoteStatisticalRealignment(candidate, active MarketMakerHorizonDecision, zScore float64) (bool, float64, float64) {
	return MakerQuoteStatisticalRealignment(candidate, active, zScore)
}

// MakerQuoteStatisticalRealignment is exported for the production-policy
// replay, which must use the same queue-preserving refresh test as live.
func MakerQuoteStatisticalRealignment(candidate, active MarketMakerHorizonDecision, zScore float64) (bool, float64, float64) {
	if !makerBBOEstimatorSource(candidate.EstimatorSource) || !makerBBOEstimatorSource(active.EstimatorSource) ||
		candidate.ScoreStdErrorBpsHour <= 0 || active.ScoreStdErrorBpsHour < 0 {
		return false, 0, 0
	}
	improvement := candidate.ScoreBpsPerHour - active.ScoreBpsPerHour
	if improvement <= 0 {
		return false, improvement, 0
	}
	if zScore <= 0 {
		zScore = 1.645
	}
	threshold := zScore * math.Hypot(candidate.ScoreStdErrorBpsHour, active.ScoreStdErrorBpsHour)
	return improvement > threshold, improvement, threshold
}

func makerBBOEstimatorSource(source string) bool {
	return source == "bbo-side" || source == "online-bbo"
}

// makerQuoteNearFill reports whether at least one fixed-price side is now as
// close to its executable BBO as its proposed replacement while the complete
// resting pair still pays the configured round-trip fee/adverse-selection
// floor. Buy distance is measured from best ask; sell distance from best bid.
func makerQuoteNearFill(lastBid, lastAsk, bestBid, bestAsk float64, plan MarketMakerQuotePlan, minimumRoundTripFloorBps float64) bool {
	retainBid, retainAsk := makerQuoteNearFillSides(
		lastBid, lastAsk, bestBid, bestAsk, plan, minimumRoundTripFloorBps)
	return retainBid || retainAsk
}

func makerQuoteNearFillSides(lastBid, lastAsk, bestBid, bestAsk float64, plan MarketMakerQuotePlan, minimumRoundTripFloorBps float64) (retainBid, retainAsk bool) {
	if lastBid <= 0 || lastAsk <= lastBid || bestBid <= 0 || bestAsk <= bestBid {
		return false, false
	}
	grossEdgeBps := math.Log(lastAsk/lastBid) * 10_000
	if grossEdgeBps+1e-9 < math.Max(0, minimumRoundTripFloorBps) {
		return false, false
	}
	if plan.AllowBid {
		if lastBid >= bestAsk {
			return false, false
		}
		bidTouchDistance := math.Log(bestAsk/lastBid) * 10_000
		retainBid = bidTouchDistance <= plan.BidTouchDistanceBps+1e-9
	}
	if plan.AllowAsk {
		if lastAsk <= bestBid {
			return false, false
		}
		askTouchDistance := math.Log(lastAsk/bestBid) * 10_000
		retainAsk = askTouchDistance <= plan.AskTouchDistanceBps+1e-9
	}
	return retainBid, retainAsk
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

func macroMarketableIOCPrice(market types.Market, side types.SideType, touch fixedpoint.Value, worstPrice float64) (fixedpoint.Value, bool) {
	if touch.Sign() <= 0 || worstPrice <= 0 {
		return fixedpoint.Zero, false
	}
	raw := fixedpoint.NewFromFloat(worstPrice)
	price := market.TruncatePrice(raw)
	switch side {
	case types.SideTypeBuy:
		if raw.Compare(touch) < 0 {
			return fixedpoint.Zero, false
		}
		// Floor-to-tick preserves the maximum BUY price budget.
		if price.Compare(touch) < 0 {
			price = touch
		}
		return price, price.Compare(touch) >= 0 && price.Compare(raw) <= 0
	case types.SideTypeSell:
		if raw.Compare(touch) > 0 {
			return fixedpoint.Zero, false
		}
		// Ceil-to-tick preserves the minimum SELL price budget.
		if price.Compare(raw) < 0 && market.TickSize.Sign() > 0 {
			price = price.Add(market.TickSize)
		}
		if price.Compare(touch) > 0 {
			price = touch
		}
		return price, price.Compare(touch) <= 0 && price.Compare(raw) >= 0
	default:
		return fixedpoint.Zero, false
	}
}

func (s *Strategy) executeFastTargetIOC(ctx context.Context, ticker types.BookTicker, modelUpdatedAt time.Time, decision FastTargetExecutionDecision) bool {
	if !decision.Trigger || decision.Direction == 0 || decision.Quantity <= 0 || decision.WorstPrice <= 0 {
		return false
	}
	side := types.SideTypeBuy
	tag := "gammacapture-fast-target-ioc-buy"
	touch := ticker.Sell
	if decision.Direction < 0 {
		side = types.SideTypeSell
		tag = "gammacapture-fast-target-ioc-sell"
		touch = ticker.Buy
	}
	price, priceOK := macroMarketableIOCPrice(s.Market, side, touch, decision.WorstPrice)
	if !priceOK {
		log.WithFields(logrus.Fields{
			"side": side, "touch": touch, "rawWorstPrice": decision.WorstPrice,
		}).Warn("Fast target execution blocked by invalid IOC worst price")
		return false
	}
	quantity := s.Market.TruncateQuantity(fixedpoint.NewFromFloat(decision.Quantity))
	if price.Sign() <= 0 || quantity.Sign() <= 0 ||
		quantity.Compare(s.Market.MinQuantity) < 0 ||
		quantity.Mul(price).Compare(s.Market.MinNotional) < 0 {
		log.WithFields(logrus.Fields{
			"side": side, "price": price, "quantity": quantity,
			"minNotional": s.Market.MinNotional, "minQuantity": s.Market.MinQuantity,
			"decision": decision.Reason,
		}).Warn("Fast target execution blocked by exchange quantity filters")
		return false
	}
	// Existing maker orders lock the balances included in QuoteableBase and
	// QuoteableQuote, and an opposing order could self-cross the IOC. Validate
	// first, then cancel the complete old quote window before taking liquidity.
	// The next BBO rebuilds the residual maker target from synchronized account
	// state through the ordinary Fast pipeline.
	if err := s.gracefulCancelMaker(ctx, "fast-target-active-execution"); err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"side": side, "price": price, "quantity": quantity,
		}).Warn("Fast target execution maker cancellation failed")
		return false
	}
	// Cancellation has ended the old quote window. Force the next BBO through a
	// complete Fast rebuild even if the IOC is rejected or expires unfilled.
	s.lastMakerQuoteAt = time.Time{}
	s.makerTradingWindowStartedAt = time.Time{}
	s.makerTradingWindowEndsAt = time.Time{}
	s.makerAskSince = time.Time{}
	s.makerAskAnchorMid = 0
	if s.State != nil {
		// Record an attempted model epoch in memory before the API call so a
		// transient submit error cannot create a BBO-rate cancel/retry loop.
		s.State.LastFastTargetExecutionModelAt = modelUpdatedAt
		s.State.LastFastTargetExecutionDirection = decision.Direction
	}
	_, err := s.executor.SubmitOrders(ctx, types.SubmitOrder{
		Symbol: s.Symbol, Market: s.Market, Side: side,
		Type: types.OrderTypeLimit, TimeInForce: types.TimeInForceIOC,
		Price: price, Quantity: quantity,
		ClientOrderID: marketMakerClientOrderID(side), Tag: tag,
	})
	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"side": side, "price": price, "quantity": quantity,
		}).Error("Fast target IOC submission failed")
		return false
	}
	if s.State != nil {
		bbgo.Sync(ctx, s)
	}
	log.WithFields(logrus.Fields{
		"side": side, "quantity": quantity, "price": price,
		"targetGapBase":                     decision.TargetGapBase,
		"residualMakerGapBase":              decision.ResidualMakerGapBase,
		"urgentFraction":                    decision.UrgentFraction,
		"passiveTouchProbability":           decision.PassiveTouchProbability,
		"passiveTouchProbabilityUpper":      decision.PassiveTouchProbabilityUpper,
		"passiveMissProbabilityLower":       decision.PassiveMissProbabilityLower,
		"expectedAdverseMoveBps":            decision.ExpectedAdverseMoveBps,
		"waitLossBps":                       decision.WaitLossBps,
		"passiveToTouchCostBps":             decision.PassiveToTouchCostBps,
		"probabilityWeightedPassiveCostBps": decision.ProbabilityWeightedPassiveCostBps,
		"expectedExecutionFeeBps":           decision.ExpectedExecutionFeeBps,
		"executionCostBps":                  decision.ExecutionCostBps,
		"persistentDownsideActive":          decision.PersistentDownsideActive,
		"persistentDownsideEValue":          decision.PersistentDownsideEValue,
		"persistentDownsideForecastBps":     decision.PersistentDownsideForecastBps,
		"persistentUpsideActive":            decision.PersistentUpsideActive,
		"persistentUpsideEValue":            decision.PersistentUpsideEValue,
		"persistentUpsideForecastBps":       decision.PersistentUpsideForecastBps,
		"inventoryVariancePenaltyBps":       decision.InventoryVariancePenaltyBps,
		"activeCertaintyEquivalentBps":      decision.ActiveCertaintyEquivalentBps,
		"maximumImpactBps":                  decision.MaximumImpactBps,
		"rawWorstPrice":                     decision.WorstPrice,
		"modelUpdatedAt":                    modelUpdatedAt,
	}).Warn("Fast expected-wait depth-capped marketable IOC submitted")
	return true
}

func (s *Strategy) executeMacroActiveIOC(ctx context.Context, ticker types.BookTicker, closedBarAt time.Time, decision MacroActiveExecutionDecision) bool {
	if !decision.Trigger || decision.Direction == 0 || decision.Quantity <= 0 || decision.WorstPrice <= 0 {
		return false
	}
	side := types.SideTypeBuy
	tag := "gammacapture-macro-ioc-buy"
	touch := ticker.Sell
	if decision.Direction < 0 {
		side = types.SideTypeSell
		tag = "gammacapture-macro-ioc-sell"
		touch = ticker.Buy
	}
	price, priceOK := macroMarketableIOCPrice(s.Market, side, touch, decision.WorstPrice)
	if !priceOK {
		log.WithFields(logrus.Fields{
			"side": side, "touch": touch, "rawWorstPrice": decision.WorstPrice,
		}).Warn("macro active execution blocked by invalid IOC worst price")
		return false
	}
	quantity := s.Market.TruncateQuantity(fixedpoint.NewFromFloat(decision.Quantity))
	if price.Sign() <= 0 || quantity.Sign() <= 0 ||
		quantity.Compare(s.Market.MinQuantity) < 0 ||
		quantity.Mul(price).Compare(s.Market.MinNotional) < 0 {
		log.WithFields(logrus.Fields{
			"side": side, "price": price, "quantity": quantity,
			"minNotional": s.Market.MinNotional, "minQuantity": s.Market.MinQuantity,
			"decision": decision.Reason,
		}).Warn("macro active execution blocked by exchange quantity filters")
		return false
	}
	for _, order := range s.executor.ActiveMakerOrders().Orders() {
		selfCross := side == types.SideTypeBuy &&
			order.Side == types.SideTypeSell && order.Price.Compare(price) <= 0
		if side == types.SideTypeSell {
			selfCross = order.Side == types.SideTypeBuy && order.Price.Compare(price) >= 0
		}
		if selfCross {
			log.WithFields(logrus.Fields{
				"side": side, "price": price, "oppositeOrderID": order.OrderID,
				"oppositePrice": order.Price,
			}).Warn("macro active execution retained maker quotes and blocked a self-cross")
			return false
		}
	}
	_, err := s.executor.SubmitOrders(ctx, types.SubmitOrder{
		Symbol: s.Symbol, Market: s.Market, Side: side,
		Type: types.OrderTypeLimit, TimeInForce: types.TimeInForceIOC,
		Price: price, Quantity: quantity,
		ClientOrderID: marketMakerClientOrderID(side), Tag: tag,
	})
	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"side": side, "price": price, "quantity": quantity,
		}).Error("macro active IOC submission failed")
		return false
	}
	now := time.Now()
	if s.State != nil && s.State.MacroInventory != nil {
		s.State.MacroInventory.LastActiveExecutionAt = now
		s.State.MacroInventory.LastActiveExecutionBarAt = closedBarAt
		bbgo.Sync(ctx, s)
	}
	log.WithFields(logrus.Fields{
		"side": side, "quantity": quantity, "price": price,
		"targetGapBase":                decision.TargetGapBase,
		"tacticalTargetGapBase":        decision.TacticalTargetGapBase,
		"residualMakerGapBase":         decision.ResidualMakerGapBase,
		"urgentFraction":               decision.UrgentFraction,
		"passiveMissProbability":       decision.PassiveMissProbability,
		"visibleDepthBase":             decision.DepthCapBase,
		"passiveTouchRateUpperPerHour": decision.PassiveTouchRateUpperPerHour,
		"expectedPassiveWait":          decision.ExpectedPassiveWait,
		"directionalDriftBpsPerHour":   decision.DirectionalDriftBpsPerHour,
		"waitLossBps":                  decision.WaitLossBps,
		"passiveToTouchCostBps":        decision.PassiveToTouchCostBps,
		"feeIncrementBps":              decision.FeeIncrementBps,
		"maximumImpactBps":             decision.MaximumImpactBps,
		"rawWorstPrice":                decision.WorstPrice, "closedBarAt": closedBarAt,
	}).Warn("macro depth-capped marketable IOC submitted")
	return true
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
	TotalBase      fixedpoint.Value
	TotalQuote     fixedpoint.Value
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
		TotalBase:      base.Total(),
		TotalQuote:     quote.Total(),
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
