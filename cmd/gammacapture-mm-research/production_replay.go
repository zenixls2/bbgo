package main

import (
	"encoding/json"
	"math"
	"os"
	"sort"
	"time"

	"github.com/c9s/bbgo/pkg/fixedpoint"
	"github.com/c9s/bbgo/pkg/strategy/gammacapture"
	"github.com/c9s/bbgo/pkg/types"
	"gopkg.in/yaml.v3"
)

type productionComparisonInput struct {
	ConfigPath, ModelPath, DataPath, Symbol      string
	JournalPath                                  string
	From, To                                     time.Time
	PairEquityJPY, StartingBase, QueueMultiplier float64
	CalibrationFrom, CalibrationTo               time.Time
	ActualBuyFills, ActualSellFills              int
}

type productionYAML struct {
	ExchangeStrategies []struct {
		GammaCapture struct {
			Symbol      string                         `yaml:"symbol"`
			Barrier     gammacapture.BarrierConfig     `yaml:"barrier"`
			Intensity   gammacapture.IntensityConfig   `yaml:"intensity"`
			MarketMaker gammacapture.MarketMakerConfig `yaml:"marketMaker"`
		} `yaml:"gammacapture"`
	} `yaml:"exchangeStrategies"`
}

type replayPolicyMode string

const (
	replayLegacy           replayPolicyMode = "legacy-direction-fallback"
	replayHorizonTouch     replayPolicyMode = "horizon-touch"
	replayAcquisitionReset replayPolicyMode = "statistical-acquisition-reset"
)

type productionReplayDay struct {
	Day       string `json:"day"`
	BuyFills  int    `json:"buyFills"`
	SellFills int    `json:"sellFills"`
}
type productionReplayResult struct {
	Mode                               replayPolicyMode `json:"mode"`
	From, To                           time.Time
	ActiveHours                        float64               `json:"activeHours"`
	BBOEvents                          int                   `json:"bboEvents"`
	AggTradeEvents                     int                   `json:"aggTradeEvents"`
	DataGaps                           int                   `json:"dataGaps"`
	QueueMultiplier                    float64               `json:"queueMultiplier"`
	QuoteRefreshes                     int                   `json:"quoteRefreshes"`
	FullFills                          int                   `json:"fullFills"`
	BuyFills                           int                   `json:"buyFills"`
	SellFills                          int                   `json:"sellFills"`
	RoundTrips                         int                   `json:"roundTrips"`
	AcquisitionResets                  int                   `json:"acquisitionResets"`
	AcquisitionQuantity                float64               `json:"acquisitionQuantity"`
	TakerFeesJPY                       float64               `json:"takerFeesJPY"`
	AcquisitionEvaluations             int                   `json:"acquisitionEvaluations"`
	AcquisitionRejections              map[string]int        `json:"acquisitionRejections,omitempty"`
	AcquisitionDrawdownLimitSamples    int                   `json:"acquisitionDrawdownLimitSamples"`
	MinimumAcquisitionDrawdownLimitBps float64               `json:"minimumAcquisitionDrawdownLimitBps"`
	MeanAcquisitionDrawdownLimitBps    float64               `json:"meanAcquisitionDrawdownLimitBps"`
	MaximumAcquisitionDrawdownLimitBps float64               `json:"maximumAcquisitionDrawdownLimitBps"`
	MaxUpProbabilityLower              float64               `json:"maxUpProbabilityLower"`
	MaxAcquisitionIOCValueBps          float64               `json:"maxAcquisitionIOCValueBps"`
	MaxAcquisitionImprovementBps       float64               `json:"maxAcquisitionImprovementBps"`
	FillsPerHour                       float64               `json:"fillsPerHour"`
	FillsPerDay                        float64               `json:"fillsPerDay"`
	RoundTripsPerDay                   float64               `json:"roundTripsPerDay"`
	QuoteUptimePct                     float64               `json:"quoteUptimePct"`
	AverageBidDistanceBps              float64               `json:"averageBidDistanceBps"`
	AverageAskDistanceBps              float64               `json:"averageAskDistanceBps"`
	AverageQuoteLifeSeconds            float64               `json:"averageQuoteLifeSeconds"`
	HorizonTouchFeatureReadyPct        float64               `json:"horizonTouchFeatureReadyPct"`
	MakerFeesJPY                       float64               `json:"makerFeesJPY"`
	NetPnLJPY                          float64               `json:"netPnLJPY"`
	MeanMarkout1mBps                   float64               `json:"meanMarkout1mBps"`
	MeanMarkout5mBps                   float64               `json:"meanMarkout5mBps"`
	MeanMarkout10mBps                  float64               `json:"meanMarkout10mBps"`
	DayResults                         []productionReplayDay `json:"dayResults"`
	Limitations                        []string              `json:"limitations"`
}

type productionComparisonReport struct {
	Symbol                  string                   `json:"symbol"`
	CalibrationActualBuy    int                      `json:"calibrationActualBuyFills"`
	CalibrationActualSell   int                      `json:"calibrationActualSellFills"`
	SelectedQueueMultiplier float64                  `json:"selectedQueueMultiplier"`
	CalibrationPassed       bool                     `json:"calibrationPassed"`
	CalibrationAbsError     int                      `json:"calibrationAbsoluteSideError"`
	CalibrationCandidates   []productionReplayResult `json:"calibrationCandidates"`
	CalibrationLegacy       productionReplayResult   `json:"calibrationLegacy"`
	FullLegacy              productionReplayResult   `json:"fullLegacy"`
	FullHorizonTouch        productionReplayResult   `json:"fullHorizonTouch"`
	JournalLifecycle        *orderLifecycleReport    `json:"journalLifecycle,omitempty"`
	FullAcquisitionReset    productionReplayResult   `json:"fullAcquisitionReset"`
	Decision                string                   `json:"decision"`
}

type productionReplayOrder struct {
	active                       bool
	side                         types.SideType
	price, remaining, queueAhead float64
	placedAt                     time.Time
}
type replayFill struct {
	at    time.Time
	side  types.SideType
	price float64
}

type productionReplayState struct {
	cfg                                                                            gammacapture.MarketMakerConfig
	artifact                                                                       *gammacapture.HorizonTouchArtifact
	mode                                                                           replayPolicyMode
	symbol                                                                         string
	queueFactor                                                                    float64
	engine                                                                         *gammacapture.CrossingEngine
	slowModel, fastModel                                                           *gammacapture.IntensityModel
	fastEvidence                                                                   *gammacapture.FastEvidenceModel
	horizonModel                                                                   gammacapture.MarketMakerHorizonModel
	inventory, quote, initialEquity                                                float64
	inventoryBand                                                                  gammacapture.InventoryBand
	sideAllocationBias, sideDistanceBias                                           float64
	sideAllocationReady, sideDistanceReady                                         bool
	bidOrder, askOrder                                                             productionReplayOrder
	lastQuoteAt, windowEndsAt                                                      time.Time
	lastBestBid, lastBestAsk, lastMid, lastImbalance                               float64
	acquisitionCooldownUntil                                                       time.Time
	acquisitionDeficitSince                                                        time.Time
	acquisitionDeficitAnchorMid                                                    float64
	acquisitionResets                                                              int
	books, trades, gaps, refreshes, quoteActive                                    int
	fills, buys, sells, roundTrips, unmatchedBuys, unmatchedSells                  int
	featureChecks, featureReady                                                    int
	tradingFrom                                                                    time.Time
	acquisitionEvaluations                                                         int
	acquisitionRejections                                                          map[string]int
	acquisitionDrawdownLimitSamples                                                int
	minAcquisitionDrawdownLimitBps, acquisitionDrawdownLimitSumBps                 float64
	maxAcquisitionDrawdownLimitBps                                                 float64
	maxUpProbabilityLower, maxAcquisitionIOCValueBps, maxAcquisitionImprovementBps float64
	lastDecisionSecond                                                             time.Time
	volatilityMinute                                                               time.Time
	cachedSideVolatility                                                           gammacapture.MarketMakerSideVolatilityEstimate
	featureMinute                                                                  time.Time
	cachedFeatures                                                                 []float64
	cachedFeaturesReady                                                            bool
	fees, bidDistanceSum, askDistanceSum                                           float64
	distanceSamples                                                                int
	acquisitionQuantity, takerFees                                                 float64
	quoteLifeSum                                                                   float64
	quoteLifeSamples                                                               int
	activeDuration                                                                 time.Duration
	fillsByDay                                                                     map[string]*productionReplayDay
	fillEvents                                                                     []replayFill
}

func runProductionComparison(in productionComparisonInput) {
	barrier, intensity, cfg := loadProductionConfig(in.ConfigPath, in.Symbol)
	artifact, err := gammacapture.LoadHorizonTouchArtifact(in.ModelPath, in.Symbol, cfg.HorizonTouchModel.MinimumBrierImprovementPct)
	if err != nil {
		fatalf("load production horizon-touch model: %v", err)
	}
	books := compactBBO(readBBO(in.DataPath, in.Symbol, in.From, in.To))
	trades := compactTrades(readLiveTrades(in.DataPath, in.Symbol, in.From, in.To))
	if len(books) < 2 || len(trades) == 0 {
		fatalf("insufficient production replay events: bbo=%d trades=%d", len(books), len(trades))
	}
	warmFrom := in.CalibrationFrom.Add(-time.Duration(cfg.HorizonLookback) - time.Duration(cfg.MaxTradingWindow) - time.Hour)
	calBooks := filterBBO(books, warmFrom, in.CalibrationTo)
	calTrades := filterTicks(trades, warmFrom, in.CalibrationTo)
	actualBuyFills, actualSellFills := in.ActualBuyFills, in.ActualSellFills
	var lifecycle *orderLifecycleReport
	if in.JournalPath != "" {
		lifecycle = buildOrderLifecycleReport(in.JournalPath, in.Symbol, in.CalibrationFrom, in.CalibrationTo, calTrades)
		actualBuyFills = lifecycle.ActualMakerBuyFills
		actualSellFills = lifecycle.ActualMakerSellFills
	}
	queueCandidates := []float64{0, .25, .5, 1, 2, 4, 8, 16}
	if in.QueueMultiplier >= 0 {
		queueCandidates = []float64{in.QueueMultiplier}
	}
	candidates := make([]productionReplayResult, 0, len(queueCandidates))
	selected, selectedIndex, bestScore := queueCandidates[0], 0, math.Inf(1)
	for _, queue := range queueCandidates {
		r := simulateProductionPolicy(calBooks, calTrades, cfg, barrier, intensity, nil, replayLegacy, in.Symbol, in.PairEquityJPY, in.StartingBase, queue, in.CalibrationFrom)
		candidates = append(candidates, r)
		score := math.Abs(float64(r.BuyFills-actualBuyFills)) + math.Abs(float64(r.SellFills-actualSellFills)) + .25*math.Abs(float64(r.FullFills-actualBuyFills-actualSellFills))
		if score < bestScore {
			selected, selectedIndex, bestScore = queue, len(candidates)-1, score
		}
	}
	calLegacy := candidates[selectedIndex]
	old := simulateProductionPolicy(books, trades, cfg, barrier, intensity, nil, replayLegacy, in.Symbol, in.PairEquityJPY, in.StartingBase, selected, in.From)
	current := simulateProductionPolicy(books, trades, cfg, barrier, intensity, artifact, replayHorizonTouch, in.Symbol, in.PairEquityJPY, in.StartingBase, selected, in.From)
	acquisition := simulateProductionPolicy(books, trades, cfg, barrier, intensity, nil, replayAcquisitionReset, in.Symbol, in.PairEquityJPY, in.StartingBase, selected, in.From)
	calibrationError := absInt(calLegacy.BuyFills-actualBuyFills) + absInt(calLegacy.SellFills-actualSellFills)
	calibrationPassed := calibrationError <= 1
	if lifecycle != nil && !lifecycle.CalibrationPassed {
		calibrationPassed = false
	}
	report := productionComparisonReport{Symbol: in.Symbol, CalibrationActualBuy: actualBuyFills, CalibrationActualSell: actualSellFills, SelectedQueueMultiplier: selected, CalibrationPassed: calibrationPassed, CalibrationAbsError: calibrationError, CalibrationCandidates: candidates, CalibrationLegacy: calLegacy, FullLegacy: old, FullHorizonTouch: current, FullAcquisitionReset: acquisition, JournalLifecycle: lifecycle, Decision: compareReplayPolicies(old, current, calibrationPassed)}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fatalf("encode production comparison: %v", err)
	}
}

func loadProductionConfig(path, symbol string) (gammacapture.BarrierConfig, gammacapture.IntensityConfig, gammacapture.MarketMakerConfig) {
	data, err := os.ReadFile(path)
	if err != nil {
		fatalf("read production config: %v", err)
	}
	var envelope productionYAML
	if err := yaml.Unmarshal(data, &envelope); err != nil {
		fatalf("decode production config: %v", err)
	}
	for _, entry := range envelope.ExchangeStrategies {
		if entry.GammaCapture.Symbol == symbol {
			return entry.GammaCapture.Barrier, entry.GammaCapture.Intensity, entry.GammaCapture.MarketMaker
		}
	}
	fatalf("GammaCapture symbol %s not found in %s", symbol, path)
	return gammacapture.BarrierConfig{}, gammacapture.IntensityConfig{}, gammacapture.MarketMakerConfig{}
}

func newProductionReplayState(cfg gammacapture.MarketMakerConfig, barrier gammacapture.BarrierConfig, intensity gammacapture.IntensityConfig, artifact *gammacapture.HorizonTouchArtifact, mode replayPolicyMode, symbol string, startQuote, startBase, queue float64, tradingFrom time.Time) *productionReplayState {
	fastWindow := time.Duration(cfg.FastWindow)
	if fastWindow <= 0 {
		fastWindow = time.Minute
	}
	return &productionReplayState{
		cfg: cfg, artifact: artifact, mode: mode, symbol: symbol, queueFactor: queue,
		engine:       gammacapture.NewCrossingEngine(barrier.Width, time.Duration(barrier.MinDwell), barrier.MaxCrossingsPerEvent),
		slowModel:    gammacapture.NewIntensityModel(intensity),
		fastModel:    gammacapture.NewIntensityModel(gammacapture.IntensityConfig{Window: types.Duration(fastWindow), VolatilityWindow: types.Duration(fastWindow), PriorAlphaUp: 1, PriorBetaUp: 10, PriorAlphaDown: 1, PriorBetaDown: 10, MinEvents: 1}),
		fastEvidence: gammacapture.NewFastEvidenceModel(gammacapture.FastEvidenceConfig{Window: time.Duration(cfg.FastEvidenceWindow), MinTrades: cfg.FastEvidenceMinTrades, MinBBOUpdates: cfg.FastEvidenceMinBBOUpdates}),
		inventory:    startBase, quote: startQuote, initialEquity: startQuote, tradingFrom: tradingFrom,
		fillsByDay:            make(map[string]*productionReplayDay),
		acquisitionRejections: make(map[string]int),
	}
}

func simulateProductionPolicy(books []bboSnapshot, trades []tick, cfg gammacapture.MarketMakerConfig, barrier gammacapture.BarrierConfig, intensity gammacapture.IntensityConfig, artifact *gammacapture.HorizonTouchArtifact, mode replayPolicyMode, symbol string, pairEquity, startBase, queue float64, tradingFrom time.Time) productionReplayResult {
	if len(books) < 2 {
		return productionReplayResult{Mode: mode}
	}
	startIndex := sort.Search(len(books), func(i int) bool { return !books[i].time.Before(tradingFrom) })
	if startIndex >= len(books) {
		return productionReplayResult{Mode: mode}
	}
	startMid := (books[startIndex].bid + books[startIndex].ask) / 2
	startQuote := math.Max(0, pairEquity-startBase*startMid)
	s := newProductionReplayState(cfg, barrier, intensity, artifact, mode, symbol, startQuote, startBase, queue, tradingFrom)
	s.initialEquity = startQuote + startBase*startMid
	bi, ti := 0, 0
	var previous time.Time
	for bi < len(books) || ti < len(trades) {
		useBook := ti >= len(trades) || (bi < len(books) && !books[bi].time.After(trades[ti].time))
		if useBook {
			book := books[bi]
			bi++
			gap := !previous.IsZero() && book.time.Sub(previous) > 15*time.Minute
			if gap {
				s.cancelQuotes(book.time)
				if !book.time.Before(tradingFrom) {
					s.gaps++
				}
			} else if !previous.IsZero() && !book.time.Before(tradingFrom) {
				activeStart := previous
				if activeStart.Before(tradingFrom) {
					activeStart = tradingFrom
				}
				s.activeDuration += book.time.Sub(activeStart)
			}
			previous = book.time
			s.onBook(book, gap)
		} else {
			s.onTrade(trades[ti])
			ti++
		}
	}
	s.cancelQuotes(books[len(books)-1].time)
	return s.result(books)
}

func (s *productionReplayState) onBook(book bboSnapshot, gap bool) {
	mid := (book.bid + book.ask) / 2
	imbalance := replayBookImbalance(book)
	ticker := types.BookTicker{Symbol: s.symbol, Buy: fixedpoint.NewFromFloat(book.bid), Sell: fixedpoint.NewFromFloat(book.ask), BuySize: fixedpoint.NewFromFloat(book.bidSize), SellSize: fixedpoint.NewFromFloat(book.askSize)}
	s.fastEvidence.ObserveBBO(book.time, ticker)
	micro := mid
	if book.bidSize+book.askSize > 0 {
		micro = (book.ask*book.bidSize + book.bid*book.askSize) / (book.bidSize + book.askSize)
	}
	if gap {
		s.engine.Reset(micro)
	} else {
		events := s.engine.Update(s.symbol, fixedpoint.NewFromFloat(micro), book.time, book.time, 0)
		for _, event := range events {
			s.slowModel.Update(event)
			s.fastModel.Update(event)
		}
	}
	s.horizonModel.ObserveBookWithGap(book.time, book.bid, book.ask, s.cfg, gap)
	if book.time.Before(s.tradingFrom) {
		return
	}
	s.books++
	decisionSecond := book.time.Truncate(time.Second)
	if !gap && decisionSecond.Equal(s.lastDecisionSecond) {
		if s.bidOrder.active || s.askOrder.active {
			s.quoteActive++
		}
		return
	}
	s.lastDecisionSecond = decisionSecond
	slow := s.slowModel.Snapshot(book.time)
	fast := s.fastModel.Snapshot(book.time)
	evidence := s.fastEvidence.Snapshot(book.time)
	fastSideVolatility := s.horizonModel.EmpiricalSideVolatilityEstimate(
		book.time, time.Duration(s.cfg.FastWindow))
	minute := book.time.Truncate(time.Minute)
	if !minute.Equal(s.volatilityMinute) {
		s.cachedSideVolatility = s.horizonModel.EmpiricalSideVolatilityEstimate(
			book.time, time.Duration(s.cfg.HorizonLookback))
		s.volatilityMinute = minute
	}
	buyEffectiveVolBps, _ := gammacapture.ShrinkVolatility(
		fastSideVolatility.BuyBps, s.cachedSideVolatility.BuyBps,
		fastSideVolatility.BuySamples, s.cfg.HorizonMinSamples)
	sellEffectiveVolBps, _ := gammacapture.ShrinkVolatility(
		fastSideVolatility.SellBps, s.cachedSideVolatility.SellBps,
		fastSideVolatility.SellSamples, s.cfg.HorizonMinSamples)
	effectiveVolBps := math.Max(buyEffectiveVolBps, sellEffectiveVolBps)
	if buyEffectiveVolBps <= 0 || sellEffectiveVolBps <= 0 {
		return
	}
	selectedDecision := s.horizonModel.UpdateForBook(book.time, s.cfg, effectiveVolBps, book.bid, book.ask)
	selectedHorizon := time.Duration(selectedDecision.HorizonSeconds) * time.Second
	if selectedHorizon <= 0 {
		selectedHorizon = time.Duration(s.cfg.MinTradingWindow)
	}
	neutralTouchDistance := func(selected time.Duration) (buyDistance, sellDistance, grossEdge float64) {
		halfSpread := s.cfg.HalfSpreadForHorizon(selected, effectiveVolBps)
		bidQuote := mid * math.Exp(-halfSpread/10_000)
		askQuote := mid * math.Exp(halfSpread/10_000)
		return gammacapture.MakerTouchDistances(book.bid, book.ask, bidQuote, askQuote)
	}
	initialBuyDistance, initialSellDistance, _ := neutralTouchDistance(selectedHorizon)
	keepDecision := s.cfg.DynamicOrderKeepDecision(selectedHorizon,
		s.cfg.OrderKeepDistanceBps(math.Max(initialBuyDistance, initialSellDistance)), effectiveVolBps)
	for i := 0; i < 3; i++ {
		buyDistance, sellDistance, _ := neutralTouchDistance(keepDecision.Duration)
		distance := s.cfg.OrderKeepDistanceBps(math.Max(buyDistance, sellDistance))
		next := s.cfg.DynamicOrderKeepDecision(selectedHorizon, distance, effectiveVolBps)
		if next.Duration == keepDecision.Duration {
			keepDecision = next
			break
		}
		keepDecision = next
	}
	horizon := keepDecision.Duration
	decision := selectedDecision
	actualBuyDistance, actualSellDistance, actualGrossEdge := neutralTouchDistance(horizon)
	if horizon != selectedHorizon ||
		math.Abs(decision.BuyTouchDistanceBps-actualBuyDistance) > 1e-9 ||
		math.Abs(decision.SellTouchDistanceBps-actualSellDistance) > 1e-9 {
		decision = s.horizonModel.CrossingDecisionAtSideDistances(
			book.time, s.cfg, horizon, actualBuyDistance, actualSellDistance, actualGrossEdge)
	}
	buyRate, sellRate := 0.0, 0.0
	if decision.HasSufficientCrossings(s.cfg.HorizonMinSamples) {
		buyRate, sellRate = decision.DownCrossesPerHour, decision.UpCrossesPerHour
	} else if s.mode != replayHorizonTouch && slow.Health == gammacapture.HealthHealthy && slow.Up > 0 && slow.Down > 0 && slow.Observed > 0 {
		hours := slow.Observed.Hours()
		if hours > 0 {
			buyRate, sellRate = float64(slow.Down)/hours, float64(slow.Up)/hours
		}
	}
	riskBuyRate, riskSellRate := buyRate, sellRate
	pairEquity := s.quote + s.inventory*mid
	cfg := s.cfg
	cfg.InventoryRiskBudgetJPY = cfg.EffectiveInventoryRiskBudgetJPY(pairEquity)
	dynamicNotional := cfg.DynamicQuoteNotionalWithFillRates(effectiveVolBps, horizon, sellRate, buyRate)
	if dynamicNotional <= 0 {
		return
	}
	cfg.QuoteNotional = dynamicNotional
	band := cfg.DynamicInventoryBandWithCapital(mid, effectiveVolBps, horizon, pairEquity)
	if s.inventoryBand.MaxInventory > 0 && band.MaxInventory > 1.2*s.inventoryBand.MaxInventory {
		band.MaxInventory = 1.2 * s.inventoryBand.MaxInventory
		band.Target = band.MaxInventory * band.TargetRatio
		band.Limit = band.MaxInventory - band.Target
	}
	s.inventoryBand = band
	if s.inventory < band.Target {
		if s.acquisitionDeficitSince.IsZero() {
			s.acquisitionDeficitSince = book.time
			s.acquisitionDeficitAnchorMid = mid
		}
	} else {
		s.acquisitionDeficitSince = time.Time{}
		s.acquisitionDeficitAnchorMid = 0
	}
	cfg.InventoryTarget, cfg.InventoryLimit = band.Target, band.Limit
	freeBase, freeQuote := s.freeBalances()
	canBuy, canSell := freeQuote > 0, freeBase*mid >= 100
	direction := replayDirection(fast)
	if s.mode == replayHorizonTouch && s.artifact != nil {
		s.featureChecks++
		if !minute.Equal(s.featureMinute) {
			s.cachedFeatures, s.cachedFeaturesReady = s.horizonModel.HorizonTouchFeatures(book.time)
			s.featureMinute = minute
		}
		features, ready := s.cachedFeatures, s.cachedFeaturesReady
		if ready {
			s.featureReady++
			provisional := cfg.Quote(gammacapture.MarketMakerQuoteInput{MidPrice: mid, BestBid: book.bid, BestAsk: book.ask, VolatilityPerSqrtSec: effectiveVolBps, BuyVolatilityPerSqrtSec: buyEffectiveVolBps, SellVolatilityPerSqrtSec: sellEffectiveVolBps, TradingHorizonSeconds: horizon.Seconds(), Inventory: freeBase, DirectionSignal: direction, BookImbalance: imbalance, SideDistanceBias: s.sideDistanceBias, CanBuy: canBuy, CanSell: canSell})
			hpBuy, okBuy := s.artifact.Predict(types.SideTypeBuy, horizon, provisional.BidTouchDistanceBps, features)
			hpSell, okSell := s.artifact.Predict(types.SideTypeSell, horizon, provisional.AskTouchDistanceBps, features)
			if okBuy && okSell {
				recentBuy, recentSell := 0.0, 0.0
				if decision.HasSufficientCrossings(s.cfg.HorizonMinSamples) {
					recentBuy = 1 - math.Exp(-decision.DownCrossesPerHour*horizon.Hours())
					recentSell = 1 - math.Exp(-decision.UpCrossesPerHour*horizon.Hours())
				}
				pBuy := gammacapture.BlendTouchProbability(hpBuy, recentBuy, cfg.HorizonTouchModel.HistoricalWeight)
				pSell := gammacapture.BlendTouchProbability(hpSell, recentSell, cfg.HorizonTouchModel.HistoricalWeight)
				buyRate = gammacapture.TouchProbabilityToRate(pBuy, horizon, cfg.HorizonTouchModel.TouchToFillHaircut)
				sellRate = gammacapture.TouchProbabilityToRate(pSell, horizon, cfg.HorizonTouchModel.TouchToFillHaircut)
			}
		}
	}
	desiredAllocation := cfg.SideAllocationBias(gammacapture.SideQuoteAllocationInput{Inventory: freeBase, InventoryTarget: cfg.InventoryTarget, InventoryLimit: cfg.InventoryLimit, DirectionSignal: direction, BookImbalance: imbalance, BuyFillRate: buyRate, SellFillRate: sellRate})
	alpha := cfg.SideAllocationSmoothing
	if alpha <= 0 || alpha > 1 {
		alpha = .25
	}
	if !s.sideAllocationReady {
		s.sideAllocationBias, s.sideAllocationReady = desiredAllocation, true
	} else {
		s.sideAllocationBias += alpha * (desiredAllocation - s.sideAllocationBias)
	}
	desiredDistance := cfg.SideQuoteDistanceBias(buyRate, sellRate)
	if !s.sideDistanceReady {
		s.sideDistanceBias, s.sideDistanceReady = desiredDistance, true
	} else {
		s.sideDistanceBias += alpha * (desiredDistance - s.sideDistanceBias)
	}
	notionals := cfg.SideQuoteNotionals(dynamicNotional, s.sideAllocationBias)
	plan := cfg.Quote(gammacapture.MarketMakerQuoteInput{MidPrice: mid, BestBid: book.bid, BestAsk: book.ask, VolatilityPerSqrtSec: effectiveVolBps, BuyVolatilityPerSqrtSec: buyEffectiveVolBps, SellVolatilityPerSqrtSec: sellEffectiveVolBps, TradingHorizonSeconds: horizon.Seconds(), Inventory: freeBase, DirectionSignal: direction, BookImbalance: imbalance, SideDistanceBias: s.sideDistanceBias, CanBuy: canBuy, CanSell: canSell})
	_ = riskBuyRate
	_ = riskSellRate
	if plan.Reason != "quoted" {
		s.cancelQuotes(book.time)
		return
	}
	if s.mode == replayAcquisitionReset && s.tryAcquisitionReset(book, decision, horizon, effectiveVolBps, plan, notionals, evidence) {
		return
	}

	s.distanceSamples++
	s.bidDistanceSum += plan.BidDistanceBps
	s.askDistanceSum += plan.AskDistanceBps
	minRefresh, maxRefresh := cfg.RefreshIntervals(plan.HalfSpreadBps, effectiveVolBps)
	minRefresh, _ = gammacapture.BoundRefreshIntervals(minRefresh, maxRefresh, horizon)
	elapsed := book.time.Sub(s.lastQuoteAt)
	quoteCrossed := (s.bidOrder.active && s.bidOrder.price >= book.ask) || (s.askOrder.active && s.askOrder.price <= book.bid)
	missingSide := (plan.AllowBid && !s.bidOrder.active) || (plan.AllowAsk && !s.askOrder.active)
	sideMismatch := (s.bidOrder.active != plan.AllowBid) || (s.askOrder.active != plan.AllowAsk)
	windowExpired := s.windowEndsAt.IsZero() || !book.time.Before(s.windowEndsAt)
	shouldRefresh := s.lastQuoteAt.IsZero()
	if !shouldRefresh && elapsed >= minRefresh {
		hardTransition := quoteCrossed || missingSide || sideMismatch
		ordinaryReprice := windowExpired
		shouldRefresh = hardTransition || ordinaryReprice
	}
	if !shouldRefresh {
		if s.bidOrder.active || s.askOrder.active {
			s.quoteActive++
		}
		return
	}
	// Live computes these balances before canceling currently locked orders.
	preCancelBase, preCancelQuote := freeBase, freeQuote
	s.cancelQuotes(book.time)
	buyNotional := math.Min(preCancelQuote, notionals.Buy)
	sellQty := math.Min(preCancelBase, notionals.Sell/plan.AskPrice)
	if plan.AllowBid && buyNotional >= 100 {
		s.bidOrder = productionReplayOrder{active: true, side: types.SideTypeBuy, price: plan.BidPrice, remaining: buyNotional / plan.BidPrice, queueAhead: book.bidSize * s.queueFactor, placedAt: book.time}
	}
	if plan.AllowAsk && sellQty*plan.AskPrice >= 100 {
		s.askOrder = productionReplayOrder{active: true, side: types.SideTypeSell, price: plan.AskPrice, remaining: sellQty, queueAhead: book.askSize * s.queueFactor, placedAt: book.time}
	}
	if s.bidOrder.active || s.askOrder.active {
		s.refreshes++
		s.lastQuoteAt = book.time
		s.windowEndsAt = book.time.Add(horizon)
		s.lastBestBid, s.lastBestAsk, s.lastMid, s.lastImbalance = book.bid, book.ask, mid, imbalance
		s.quoteActive++
	}
}

func (s *productionReplayState) tryAcquisitionReset(book bboSnapshot, horizonDecision gammacapture.MarketMakerHorizonDecision, horizon time.Duration, effectiveVolBps float64, plan gammacapture.MarketMakerQuotePlan, notionals gammacapture.SideQuoteNotionals, evidence gammacapture.FastEvidenceSnapshot) bool {
	cfg := s.cfg.AcquisitionReset
	// This function is called only by the explicit acquisition research mode.
	cfg.Enabled = true
	if !s.bidOrder.active || book.time.Before(s.acquisitionCooldownUntil) ||
		s.inventory >= s.inventoryBand.Target || s.quote <= 0 || book.askSize <= 0 {
		return false
	}
	drawdownLimit5mBps, calibrated := cfg.CalibratedDrawdownLimit5mBps(evidence)
	return1mMoveBps, return1mReady := cfg.CalibratedReturnMoveBps(evidence, time.Minute)
	return5mMoveBps, return5mReady := cfg.CalibratedReturnMoveBps(evidence, 5*time.Minute)
	returnCalibrationReady := return1mReady && return5mReady
	if calibrated {
		s.acquisitionDrawdownLimitSamples++
		s.acquisitionDrawdownLimitSumBps += drawdownLimit5mBps
		s.maxAcquisitionDrawdownLimitBps = math.Max(s.maxAcquisitionDrawdownLimitBps, drawdownLimit5mBps)
		if s.minAcquisitionDrawdownLimitBps == 0 || drawdownLimit5mBps < s.minAcquisitionDrawdownLimitBps {
			s.minAcquisitionDrawdownLimitBps = drawdownLimit5mBps
		}
	}
	decision := cfg.Evaluate(gammacapture.AcquisitionResetInput{
		Now: book.time, DeficitSince: s.acquisitionDeficitSince, AnchorMidPrice: s.acquisitionDeficitAnchorMid,
		MidPrice: (book.bid + book.ask) / 2, BestAsk: book.ask,
		BidPrice: s.bidOrder.price, PlannedAskPrice: plan.AskPrice,
		MakerFeeBps: s.cfg.MakerFeeBps, TakerFeeBps: s.cfg.TakerFeeBps,
		AdverseSelectionBps: s.cfg.AdverseSelectionBps,
		MaxSlippageBps:      cfg.MaxSlippageBps,
		UpCrosses:           horizonDecision.UpCrosses, DownCrosses: horizonDecision.DownCrosses,
		UpCrossesPerHour:   horizonDecision.UpCrossesPerHour,
		DownCrossesPerHour: horizonDecision.DownCrossesPerHour,
		QuoteDistanceBps:   horizonDecision.QuoteDistanceBps,
		Horizon:            horizon, FillIntensityHaircut: cfg.FillIntensityHaircut,
		VolatilityPerSqrtSec:    effectiveVolBps / 10_000,
		EvidenceHealth:          evidence.Health,
		Return1mBps:             evidence.MidReturn1mBps,
		Return5mBps:             evidence.MidReturn5mBps,
		Return1mThresholdBps:    -return1mMoveBps,
		Return5mThresholdBps:    return5mMoveBps,
		ReturnCalibrationReady:  returnCalibrationReady,
		AdverseMoveThresholdBps: return5mMoveBps,
		Drawdown5mBps:           evidence.MidDrawdown5mBps,
		DrawdownLimit5mBps:      drawdownLimit5mBps,
		TradeCount5m:            evidence.TradeCount5m,
		BBOCount5m:              evidence.BBOCount5m,
	})
	s.acquisitionEvaluations++
	s.maxUpProbabilityLower = math.Max(s.maxUpProbabilityLower, decision.UpProbabilityLower)
	s.maxAcquisitionIOCValueBps = math.Max(s.maxAcquisitionIOCValueBps, decision.IOCValueBps)
	s.maxAcquisitionImprovementBps = math.Max(s.maxAcquisitionImprovementBps, decision.IOCImprovementBps)
	if !decision.Trigger {
		s.acquisitionRejections[decision.Reason]++
		return false
	}

	price := book.ask * (1 + cfg.MaxSlippageBps/10_000)
	quantity := math.Min(s.inventoryBand.Target-s.inventory, notionals.Buy/price)
	quantity = math.Min(quantity, s.quote/price)
	// BBO replay has no deeper book. Limiting the IOC to displayed best-ask
	// quantity avoids assuming unavailable depth while pricing at the full cap.
	quantity = math.Min(quantity, book.askSize)
	if quantity <= 0 || quantity*price < 100 {
		return false
	}
	s.acquisitionRejections["replay IOC below exchange minimum"]++

	s.cancelQuotes(book.time)
	notional := quantity * price
	fee := notional * s.cfg.TakerFeeBps / 10_000
	s.inventory += quantity
	s.quote -= notional
	s.fees += fee
	s.takerFees += fee
	s.acquisitionResets++
	s.acquisitionQuantity += quantity
	s.acquisitionCooldownUntil = book.time.Add(time.Duration(cfg.Cooldown))
	if s.unmatchedSells > 0 {
		s.acquisitionDeficitSince = time.Time{}
		s.acquisitionDeficitAnchorMid = 0
		s.unmatchedSells--
		s.roundTrips++
	} else {
		s.unmatchedBuys++
	}
	return true
}

func (s *productionReplayState) onTrade(trade tick) {
	s.fastEvidence.ObserveTrade(trade.time, types.Trade{Symbol: s.symbol, Price: fixedpoint.NewFromFloat(trade.price), Quantity: fixedpoint.NewFromFloat(trade.size), Side: trade.side})
	if trade.time.Before(s.tradingFrom) {
		return
	}
	s.trades++
	if trade.side == types.SideTypeBuy && s.askOrder.active && trade.price >= s.askOrder.price {
		s.consume(&s.askOrder, trade)
	}
	if trade.side == types.SideTypeSell && s.bidOrder.active && trade.price <= s.bidOrder.price {
		s.consume(&s.bidOrder, trade)
	}
}

func (s *productionReplayState) consume(order *productionReplayOrder, trade tick) {
	volume := trade.size
	if order.queueAhead >= volume {
		order.queueAhead -= volume
		return
	}
	volume -= order.queueAhead
	order.queueAhead = 0
	executed := math.Min(volume, order.remaining)
	if executed <= 0 {
		return
	}
	order.remaining -= executed
	notional := executed * order.price
	s.fees += notional * s.cfg.MakerFeeBps / 10000
	if order.side == types.SideTypeBuy {
		s.inventory += executed
		s.quote -= notional
	} else {
		s.inventory -= executed
		s.quote += notional
	}
	if order.remaining > 1e-12 {
		return
	}
	order.active = false
	s.fills++
	day := trade.time.UTC().Format(time.DateOnly)
	if s.fillsByDay[day] == nil {
		s.fillsByDay[day] = &productionReplayDay{Day: day}
	}
	if order.side == types.SideTypeBuy {
		s.buys++
		s.fillsByDay[day].BuyFills++
		if s.unmatchedSells > 0 {
			s.unmatchedSells--
			s.roundTrips++
		} else {
			s.unmatchedBuys++
		}
	} else {
		s.sells++
		s.fillsByDay[day].SellFills++
		if s.unmatchedBuys > 0 {
			s.unmatchedBuys--
			s.roundTrips++
		} else {
			s.unmatchedSells++
		}
	}
	s.fillEvents = append(s.fillEvents, replayFill{at: trade.time, side: order.side, price: order.price})
}

func (s *productionReplayState) freeBalances() (base, quote float64) {
	base, quote = s.inventory, s.quote
	if s.askOrder.active {
		base -= s.askOrder.remaining
	}
	if s.bidOrder.active {
		quote -= s.bidOrder.remaining * s.bidOrder.price
	}
	return math.Max(0, base), math.Max(0, quote)
}

func (s *productionReplayState) cancelQuotes(at time.Time) {
	if !s.lastQuoteAt.IsZero() && at.After(s.lastQuoteAt) {
		s.quoteLifeSum += at.Sub(s.lastQuoteAt).Seconds()
		s.quoteLifeSamples++
	}
	s.bidOrder, s.askOrder = productionReplayOrder{}, productionReplayOrder{}
	s.lastQuoteAt, s.windowEndsAt = time.Time{}, time.Time{}
}

func (s *productionReplayState) result(books []bboSnapshot) productionReplayResult {
	evalBooks := filterBBO(books, s.tradingFrom, books[len(books)-1].time.Add(time.Nanosecond))
	if len(evalBooks) == 0 {
		return productionReplayResult{Mode: s.mode}
	}
	books = evalBooks
	lastMid := (books[len(books)-1].bid + books[len(books)-1].ask) / 2
	hours := s.activeDuration.Hours()
	r := productionReplayResult{Mode: s.mode, From: books[0].time, To: books[len(books)-1].time, ActiveHours: hours, BBOEvents: s.books, AggTradeEvents: s.trades, DataGaps: s.gaps, QueueMultiplier: s.queueFactor, QuoteRefreshes: s.refreshes, FullFills: s.fills, BuyFills: s.buys, SellFills: s.sells, RoundTrips: s.roundTrips, AcquisitionResets: s.acquisitionResets, AcquisitionQuantity: s.acquisitionQuantity, AcquisitionEvaluations: s.acquisitionEvaluations, AcquisitionRejections: s.acquisitionRejections, AcquisitionDrawdownLimitSamples: s.acquisitionDrawdownLimitSamples, MinimumAcquisitionDrawdownLimitBps: s.minAcquisitionDrawdownLimitBps, MaximumAcquisitionDrawdownLimitBps: s.maxAcquisitionDrawdownLimitBps, MaxUpProbabilityLower: s.maxUpProbabilityLower, MaxAcquisitionIOCValueBps: s.maxAcquisitionIOCValueBps, MaxAcquisitionImprovementBps: s.maxAcquisitionImprovementBps, MakerFeesJPY: s.fees - s.takerFees, TakerFeesJPY: s.takerFees, NetPnLJPY: s.quote + s.inventory*lastMid - s.fees - s.initialEquity, Limitations: []string{"no level-2 depth or exchange queue priority", "IOC acquisition is capped by visible best-ask size but deeper execution and impact are unavailable", "queue multiplier is calibrated to aggregate confirmed fills, not per-order queue position", "public aggregate trades cannot identify our private execution"}}
	if s.acquisitionDrawdownLimitSamples > 0 {
		r.MeanAcquisitionDrawdownLimitBps = s.acquisitionDrawdownLimitSumBps / float64(s.acquisitionDrawdownLimitSamples)
	}
	if hours > 0 {
		r.FillsPerHour = float64(s.fills) / hours
		r.FillsPerDay = 24 * r.FillsPerHour
		r.RoundTripsPerDay = 24 * float64(s.roundTrips) / hours
	}
	if s.books > 0 {
		r.QuoteUptimePct = 100 * float64(s.quoteActive) / float64(s.books)
	}
	if s.distanceSamples > 0 {
		r.AverageBidDistanceBps = s.bidDistanceSum / float64(s.distanceSamples)
		r.AverageAskDistanceBps = s.askDistanceSum / float64(s.distanceSamples)
	}
	if s.quoteLifeSamples > 0 {
		r.AverageQuoteLifeSeconds = s.quoteLifeSum / float64(s.quoteLifeSamples)
	}
	if s.featureChecks > 0 {
		r.HorizonTouchFeatureReadyPct = 100 * float64(s.featureReady) / float64(s.featureChecks)
	}
	r.MeanMarkout1mBps = meanFillMarkout(s.fillEvents, books, time.Minute)
	r.MeanMarkout5mBps = meanFillMarkout(s.fillEvents, books, 5*time.Minute)
	r.MeanMarkout10mBps = meanFillMarkout(s.fillEvents, books, 10*time.Minute)
	for _, day := range s.fillsByDay {
		r.DayResults = append(r.DayResults, *day)
	}
	sort.Slice(r.DayResults, func(i, j int) bool { return r.DayResults[i].Day < r.DayResults[j].Day })
	return r
}

func compareReplayPolicies(old, current productionReplayResult, calibrationPassed bool) string {
	if !calibrationPassed {
		return "calibration failed: do not use synthetic fill comparison to promote or tune live policy"
	}
	if current.FullFills == 0 {
		return "reject horizon-touch: no synthetic fills"
	}
	if current.MeanMarkout10mBps < old.MeanMarkout10mBps && current.FillsPerHour < old.FillsPerHour {
		return "do not promote from replay: lower fill rate and worse 10m markout"
	}
	if current.FillsPerHour >= .8*old.FillsPerHour && current.MeanMarkout10mBps >= old.MeanMarkout10mBps {
		return "horizon-touch passes provisional replay gate; retain live canary and collect private fills"
	}
	return "inconclusive: retain canary, do not loosen quotes until more private fills are available"
}

func compactBBO(values []bboSnapshot) []bboSnapshot {
	sort.SliceStable(values, func(i, j int) bool { return values[i].time.Before(values[j].time) })
	out := values[:0]
	for _, value := range values {
		if len(out) > 0 {
			last := out[len(out)-1]
			if value.time.Equal(last.time) && value.bid == last.bid && value.ask == last.ask && value.bidSize == last.bidSize && value.askSize == last.askSize {
				continue
			}
		}
		out = append(out, value)
	}
	return out
}
func compactTrades(values []tick) []tick {
	sort.SliceStable(values, func(i, j int) bool { return values[i].time.Before(values[j].time) })
	out := values[:0]
	seenIDs := make(map[uint64]struct{}, len(values))
	for _, value := range values {
		if value.id != 0 {
			if _, exists := seenIDs[value.id]; exists {
				continue
			}
			seenIDs[value.id] = struct{}{}
		} else if len(out) > 0 {
			last := out[len(out)-1]
			if last.id == 0 && value.time.Equal(last.time) && value.price == last.price && value.size == last.size && value.side == last.side {
				continue
			}
		}
		out = append(out, value)
	}
	return out
}
func filterBBO(values []bboSnapshot, from, to time.Time) []bboSnapshot {
	out := make([]bboSnapshot, 0, len(values))
	for _, v := range values {
		if !v.time.Before(from) && v.time.Before(to) {
			out = append(out, v)
		}
	}
	return out
}
func filterTicks(values []tick, from, to time.Time) []tick {
	out := make([]tick, 0, len(values))
	for _, v := range values {
		if !v.time.Before(from) && v.time.Before(to) {
			out = append(out, v)
		}
	}
	return out
}
func replayBookImbalance(book bboSnapshot) float64 {
	total := book.bidSize + book.askSize
	if total <= 0 {
		return 0
	}
	return math.Max(-1, math.Min(1, (book.bidSize-book.askSize)/total))
}
func replayDirection(snapshot gammacapture.ModelSnapshot) float64 {
	total := snapshot.LambdaUp + snapshot.LambdaDown
	if total <= 0 {
		return 0
	}
	return math.Max(-1, math.Min(1, (snapshot.LambdaUp-snapshot.LambdaDown)/total))
}
func replayNearFill(bid, ask productionReplayOrder, book bboSnapshot, mid float64, plan gammacapture.MarketMakerQuotePlan, floor float64) bool {
	if plan.AllowBid {
		d := math.Log(mid/bid.price) * 10000
		if !bid.active || bid.price >= book.ask || d < floor || d > plan.BidDistanceBps+1e-9 {
			return false
		}
	}
	if plan.AllowAsk {
		d := math.Log(ask.price/mid) * 10000
		if !ask.active || ask.price <= book.bid || d < floor || d > plan.AskDistanceBps+1e-9 {
			return false
		}
	}
	return plan.AllowBid || plan.AllowAsk
}
func meanFillMarkout(fills []replayFill, books []bboSnapshot, horizon time.Duration) float64 {
	total := 0.0
	count := 0
	for _, fill := range fills {
		target := fill.at.Add(horizon)
		i := sort.Search(len(books), func(i int) bool { return !books[i].time.Before(target) })
		if i >= len(books) || books[i].time.Sub(target) > 2*time.Minute {
			continue
		}
		mid := (books[i].bid + books[i].ask) / 2
		sign := 1.0
		if fill.side == types.SideTypeSell {
			sign = -1
		}
		total += sign * math.Log(mid/fill.price) * 10000
		count++
	}
	if count == 0 {
		return 0
	}
	return total / float64(count)
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
