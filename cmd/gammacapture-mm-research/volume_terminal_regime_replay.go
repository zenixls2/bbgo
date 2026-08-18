package main

import (
	"encoding/json"
	"math"
	"os"
	"sort"
	"time"

	"github.com/c9s/bbgo/pkg/strategy/gammacapture"
)

// volumeTerminalTargetPoint is a causal prediction available at At. The
// terminal-only target uses the executable post-fill mean model; CombinedBps
// adds only the calibrated Volume Profile residual.
type volumeTerminalTargetPoint struct {
	At                    time.Time
	BaselineBps           float64
	CombinedBps           float64
	RisingBps             float64
	RisingSpecialistReady bool
}

type volumeTerminalTargetSeries struct {
	Horizon      time.Duration
	MaximumAge   time.Duration
	Latency      fillLatencyCoverageEstimate
	Points       []volumeTerminalTargetPoint
	Observations int
}

type volumeTerminalTargetProvider struct {
	Policy                    volumeTerminalTargetPolicy
	Series                    map[time.Duration]volumeTerminalTargetSeries
	Queries                   int
	VolumeProfileApplications int
}

type volumeTerminalTargetPolicy uint8

const (
	volumeTerminalOnly volumeTerminalTargetPolicy = iota
	volumeTerminalAlways
	volumeTerminalRisingOnly
)

func risingConditionedTerminalTarget(baseline, specialist float64, specialistReady bool) (float64, bool) {
	if baseline > 0 && specialistReady {
		return specialist, true
	}
	return baseline, false
}

func (p *volumeTerminalTargetProvider) MeanAt(at time.Time, horizon time.Duration) (float64, bool) {
	if p == nil || at.IsZero() {
		return 0, false
	}
	series, ok := p.Series[horizon]
	if !ok || len(series.Points) == 0 {
		return 0, false
	}
	index := sort.Search(len(series.Points), func(i int) bool {
		return series.Points[i].At.After(at)
	}) - 1
	if index < 0 || at.Sub(series.Points[index].At) > series.MaximumAge {
		return 0, false
	}
	p.Queries++
	switch p.Policy {
	case volumeTerminalAlways:
		p.VolumeProfileApplications++
		return series.Points[index].CombinedBps, true
	case volumeTerminalRisingOnly:
		value, applied := risingConditionedTerminalTarget(
			series.Points[index].BaselineBps, series.Points[index].RisingBps,
			series.Points[index].RisingSpecialistReady)
		if applied {
			p.VolumeProfileApplications++
		}
		return value, true
	}
	return series.Points[index].BaselineBps, true
}

// buildVolumeTerminalTargetPoints repeats the exact prequential update order
// used by the standalone Volume Profile study. BUY is terminal executable bid
// wealth after a passive fill; SELL is terminal executable ask repurchase cost.
// Their antisymmetric component removes common quote edge and fees:
//
//	mu_inventory = (E[value_BUY] - E[value_SELL]) / 2.
//
// This scalar replaces only InventoryDirectionalMeanBps in research replay.
func buildVolumeTerminalTargetPoints(
	observations []volumeProfileObservation,
	horizon, lookback time.Duration,
) []volumeTerminalTargetPoint {
	if len(observations) == 0 || horizon <= 0 || lookback <= 0 {
		return nil
	}
	halfLife := time.Duration(math.Sqrt(horizon.Seconds()*lookback.Seconds())) * time.Second
	baseline := smallEWRegression{dim: 3, halfLife: halfLife}
	residual := smallEWRegression{dim: 12, halfLife: halfLife}
	calibration := residualCalibration{halfLife: halfLife}
	risingResidual := smallEWRegression{dim: 12, halfLife: halfLife}
	risingCalibration := residualCalibration{halfLife: halfLife}
	type pendingPrediction struct {
		observation                                                    volumeProfileObservation
		base, rawResidual, residual, risingRawResidual, risingResidual [3]float64
		baseReady, rawResidualReady, risingRawReady, rising            bool
	}
	pending := make([]pendingPrediction, 0, 128)
	points := make([]volumeTerminalTargetPoint, 0, len(observations))
	mature := func(p pendingPrediction) {
		o := p.observation
		baseline.update(o.maturity, o.baseline,
			o.valueBps, o.buyMeanReturnBps, o.sellMeanReturnBps)
		residual.update(o.maturity, o.volume,
			o.valueBps-p.base[0], o.buyMeanReturnBps-p.base[1], o.sellMeanReturnBps-p.base[2])
		if p.baseReady && p.rawResidualReady {
			calibration.update(o.maturity, p.rawResidual, [3]float64{
				o.valueBps - p.base[0],
				o.buyMeanReturnBps - p.base[1],
				o.sellMeanReturnBps - p.base[2],
			})
		}
		if p.rising && p.baseReady {
			risingResidual.update(o.maturity, o.volume,
				o.valueBps-p.base[0], o.buyMeanReturnBps-p.base[1], o.sellMeanReturnBps-p.base[2])
			if p.risingRawReady {
				risingCalibration.update(o.maturity, p.risingRawResidual, [3]float64{
					o.valueBps - p.base[0],
					o.buyMeanReturnBps - p.base[1],
					o.sellMeanReturnBps - p.base[2],
				})
			}
		}
	}
	for _, observation := range observations {
		for len(pending) > 0 && !pending[0].observation.maturity.After(observation.at) {
			mature(pending[0])
			pending = pending[1:]
		}
		basePrediction, baseReady := baseline.predict(observation.baseline)
		rawResidual, rawResidualReady := residual.predict(observation.volume)
		residualPrediction, calibrationReady := calibration.predict(rawResidual)
		risingRawResidual, risingRawReady := risingResidual.predict(observation.volume)
		risingResidualPrediction, risingCalibrationReady := risingCalibration.predict(risingRawResidual)
		rising := baseReady && 0.5*(basePrediction[1]-basePrediction[2]) > 0
		if baseReady && rawResidualReady && calibrationReady {
			baselineBps := 0.5 * (basePrediction[1] - basePrediction[2])
			risingBps := baselineBps
			risingReady := rising && risingRawReady && risingCalibrationReady
			if risingReady {
				risingBps = 0.5 * ((basePrediction[1] + risingResidualPrediction[1]) -
					(basePrediction[2] + risingResidualPrediction[2]))
			}
			points = append(points, volumeTerminalTargetPoint{
				At:          observation.at,
				BaselineBps: baselineBps,
				CombinedBps: 0.5 * ((basePrediction[1] + residualPrediction[1]) -
					(basePrediction[2] + residualPrediction[2])),
				RisingBps: risingBps, RisingSpecialistReady: risingReady,
			})
		}
		pending = append(pending, pendingPrediction{
			observation: observation, base: basePrediction, rawResidual: rawResidual,
			residual: residualPrediction, risingRawResidual: risingRawResidual,
			risingResidual: risingResidualPrediction, baseReady: baseReady,
			rawResidualReady: rawResidualReady, risingRawReady: risingRawReady, rising: rising,
		})
	}
	return points
}

func buildVolumeTerminalTargetProvider(
	books []bboSnapshot,
	trades []tick,
	cfg gammacapture.MarketMakerConfig,
	calibrationTo time.Time,
	coverage float64,
) *volumeTerminalTargetProvider {
	provider := &volumeTerminalTargetProvider{Series: make(map[time.Duration]volumeTerminalTargetSeries)}
	distance := math.Max(1, cfg.MinimumHalfSpreadBps)
	lookback := time.Duration(cfg.HorizonLookback)
	if lookback <= 0 {
		lookback = 6 * time.Hour
	}
	for _, horizon := range cfg.FastModelWindows() {
		provisional := buildVolumeProfileMinutes(books, trades, cfg, horizon, horizon, coverage)
		latency := estimateFillLatencyCoverage(
			provisional, calibrationTo, horizon, lookback, distance, coverage)
		if !latency.Sufficient {
			provider.Series[horizon] = volumeTerminalTargetSeries{Horizon: horizon, Latency: latency}
			continue
		}
		minutes := buildVolumeProfileMinutes(books, trades, cfg, horizon, latency.ProfileRange, coverage)
		observations := selectEventClockObservations(minutes,
			volumeProfileObservations(minutes, horizon, distance, cfg.MakerFeeBps, latency),
			horizon, distance)
		points := buildVolumeTerminalTargetPoints(observations, horizon, lookback)
		provider.Series[horizon] = volumeTerminalTargetSeries{
			Horizon: horizon, MaximumAge: latency.ProfileRange,
			Latency: latency, Points: points, Observations: len(observations),
		}
	}
	return provider
}

type volumeTerminalRegimeSpec struct {
	Name     string    `json:"name"`
	From, To time.Time `json:"from"`
}

type volumeTerminalHorizonDiagnostic struct {
	Horizon          string `json:"horizon"`
	Observations     int    `json:"observations"`
	PredictionPoints int    `json:"predictionPoints"`
	LatencyEligible  int    `json:"latencyEligible"`
	LatencyReady     bool   `json:"latencyReady"`
	BuyQ             string `json:"buyQ"`
	SellQ            string `json:"sellQ"`
	ProfileRange     string `json:"profileRange"`
}

type volumeTerminalRegimeResult struct {
	Regime   volumeTerminalRegimeSpec          `json:"regime"`
	Horizons []volumeTerminalHorizonDiagnostic `json:"horizons"`
	Control  volumeTerminalReplaySummary       `json:"control"`
	Terminal volumeTerminalReplaySummary       `json:"terminalOnly"`
	Combined volumeTerminalReplaySummary       `json:"volumeProfileTerminal"`
	Rising   volumeTerminalReplaySummary       `json:"volumeProfileRisingOnly"`
}

type volumeTerminalReplaySummary struct {
	NetPnLJPY                    float64 `json:"netPnLJPY"`
	HoldPnLJPY                   float64 `json:"holdPnLJPY"`
	ExcessVsHoldJPY              float64 `json:"excessVsHoldJPY"`
	MakerFeesJPY                 float64 `json:"makerFeesJPY"`
	TakerFeesJPY                 float64 `json:"takerFeesJPY"`
	BuyFills                     int     `json:"buyFills"`
	SellFills                    int     `json:"sellFills"`
	RoundTrips                   int     `json:"roundTrips"`
	QuoteRefreshes               int     `json:"quoteRefreshes"`
	MaximumDrawdownPct           float64 `json:"maximumDrawdownPct"`
	DirectionalTargetOverrides   int     `json:"directionalTargetOverrides"`
	MeanDirectionalTargetBps     float64 `json:"meanDirectionalTargetBps"`
	StoppedEarly                 bool    `json:"stoppedEarly"`
	VolumeProfileApplications    int     `json:"volumeProfileApplications"`
	VolumeProfileApplicationRate float64 `json:"volumeProfileApplicationRate"`
}

func addVolumeTerminalProviderDiagnostics(
	summary volumeTerminalReplaySummary,
	provider *volumeTerminalTargetProvider,
) volumeTerminalReplaySummary {
	if provider == nil {
		return summary
	}
	summary.VolumeProfileApplications = provider.VolumeProfileApplications
	if provider.Queries > 0 {
		summary.VolumeProfileApplicationRate = float64(provider.VolumeProfileApplications) / float64(provider.Queries)
	}
	return summary
}

func summarizeVolumeTerminalReplay(result productionReplayResult) volumeTerminalReplaySummary {
	return volumeTerminalReplaySummary{
		NetPnLJPY: result.NetPnLJPY, HoldPnLJPY: result.HoldPnLJPY,
		ExcessVsHoldJPY: result.NetPnLJPY - result.HoldPnLJPY,
		MakerFeesJPY:    result.MakerFeesJPY, TakerFeesJPY: result.TakerFeesJPY,
		BuyFills: result.BuyFills, SellFills: result.SellFills,
		RoundTrips: result.RoundTrips, QuoteRefreshes: result.QuoteRefreshes,
		MaximumDrawdownPct:         result.MaximumDrawdownPct,
		DirectionalTargetOverrides: result.DirectionalTargetOverrides,
		MeanDirectionalTargetBps:   result.MeanDirectionalTargetBps,
		StoppedEarly:               result.StoppedEarly,
	}
}

type volumeTerminalRegimeReport struct {
	Name              string                       `json:"name"`
	Symbol            string                       `json:"symbol"`
	Causal            bool                         `json:"causal"`
	QueueMultiplier   float64                      `json:"queueMultiplier"`
	FillCoverage      float64                      `json:"fillCoverage"`
	StartingEquityJPY float64                      `json:"startingEquityJPY"`
	StartingBase      float64                      `json:"startingBase"`
	Results           []volumeTerminalRegimeResult `json:"results"`
	Warning           string                       `json:"warning"`
}

type volumeTerminalRegimeInput struct {
	ConfigPath, DataPath, Symbol, ReplayCacheDir string
	PairEquityJPY, StartingBase, QueueMultiplier float64
	FillCoverage                                 float64
}

func canonicalVolumeTerminalRegimes() []volumeTerminalRegimeSpec {
	return []volumeTerminalRegimeSpec{
		{Name: "decline", From: parseTime("2026-08-03T00:00:00Z"), To: parseTime("2026-08-03T08:00:00Z")},
		{Name: "rise", From: parseTime("2026-08-05T15:00:00Z"), To: parseTime("2026-08-05T21:00:00Z")},
		{Name: "range", From: parseTime("2026-08-08T00:00:00Z"), To: parseTime("2026-08-08T12:00:00Z")},
		{Name: "mixed", From: parseTime("2026-08-10T00:00:00Z"), To: parseTime("2026-08-10T18:00:00Z")},
	}
}

func runVolumeTerminalRegimeComparison(in volumeTerminalRegimeInput) {
	barrier, intensity, cfg := loadProductionConfig(in.ConfigPath, in.Symbol)
	coverage := in.FillCoverage
	if coverage <= 0 || coverage >= 1 {
		fatalf("volume-terminal coverage must be in (0,1): %.6f", coverage)
	}
	queue := in.QueueMultiplier
	if queue < 0 {
		queue = 0
	}
	report := volumeTerminalRegimeReport{
		Name:   "volume-profile-post-fill-terminal-regime-comparison",
		Symbol: in.Symbol, Causal: true, QueueMultiplier: queue,
		FillCoverage: coverage, StartingEquityJPY: in.PairEquityJPY,
		StartingBase: in.StartingBase,
		Warning:      "canonical regimes are diagnostics already inspected repeatedly; they are not blind promotion evidence",
	}
	for _, regime := range canonicalVolumeTerminalRegimes() {
		warmFrom := regime.From.Add(-24 * time.Hour)
		books, trades, _ := loadExactReplayDataset(
			in.DataPath, in.Symbol, warmFrom, regime.To,
			"volume-terminal-regime-v1", in.ReplayCacheDir)
		books, trades = compactBBO(books), compactTrades(trades)
		if len(books) < 2 || len(trades) < 8 {
			fatalf("insufficient %s data: books=%d trades=%d", regime.Name, len(books), len(trades))
		}
		provider := buildVolumeTerminalTargetProvider(books, trades, cfg, regime.From, coverage)
		diagnostics := make([]volumeTerminalHorizonDiagnostic, 0, len(provider.Series))
		for _, horizon := range cfg.FastModelWindows() {
			series := provider.Series[horizon]
			diagnostics = append(diagnostics, volumeTerminalHorizonDiagnostic{
				Horizon: horizon.String(), Observations: series.Observations,
				PredictionPoints: len(series.Points), LatencyEligible: series.Latency.Eligible,
				LatencyReady: series.Latency.Sufficient, BuyQ: series.Latency.Buy.String(),
				SellQ: series.Latency.Sell.String(), ProfileRange: series.Latency.ProfileRange.String(),
			})
		}

		activeProductionReplayDirectionalTarget = nil
		control := simulateProductionPolicyWithQuantityProjection(
			books, trades, cfg, barrier, intensity, nil, replayLegacy, in.Symbol,
			in.PairEquityJPY, in.StartingBase, queue, regime.From, true, 0)
		provider.Policy = volumeTerminalOnly
		provider.Queries, provider.VolumeProfileApplications = 0, 0
		activeProductionReplayDirectionalTarget = provider
		terminal := simulateProductionPolicyWithQuantityProjection(
			books, trades, cfg, barrier, intensity, nil, replayLegacy, in.Symbol,
			in.PairEquityJPY, in.StartingBase, queue, regime.From, true, 0)
		terminalSummary := addVolumeTerminalProviderDiagnostics(summarizeVolumeTerminalReplay(terminal), provider)
		provider.Policy = volumeTerminalAlways
		provider.Queries, provider.VolumeProfileApplications = 0, 0
		combined := simulateProductionPolicyWithQuantityProjection(
			books, trades, cfg, barrier, intensity, nil, replayLegacy, in.Symbol,
			in.PairEquityJPY, in.StartingBase, queue, regime.From, true, 0)
		combinedSummary := addVolumeTerminalProviderDiagnostics(summarizeVolumeTerminalReplay(combined), provider)
		provider.Policy = volumeTerminalRisingOnly
		provider.Queries, provider.VolumeProfileApplications = 0, 0
		rising := simulateProductionPolicyWithQuantityProjection(
			books, trades, cfg, barrier, intensity, nil, replayLegacy, in.Symbol,
			in.PairEquityJPY, in.StartingBase, queue, regime.From, true, 0)
		risingSummary := addVolumeTerminalProviderDiagnostics(summarizeVolumeTerminalReplay(rising), provider)
		activeProductionReplayDirectionalTarget = nil
		report.Results = append(report.Results, volumeTerminalRegimeResult{
			Regime: regime, Horizons: diagnostics,
			Control:  summarizeVolumeTerminalReplay(control),
			Terminal: terminalSummary,
			Combined: combinedSummary,
			Rising:   risingSummary,
		})
	}
	activeProductionReplayDirectionalTarget = nil
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fatalf("encode volume-terminal regime comparison: %v", err)
	}
}
