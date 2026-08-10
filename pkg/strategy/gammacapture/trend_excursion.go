package gammacapture

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// TrendExcursionDecision is an online, causal estimate of the return remaining
// before the next long-window pivot. Features are executable ask and bid
// returns over every configured Macro horizon. Labels use only forward windows
// that have completely elapsed before the current closed bar.
type TrendExcursionDecision struct {
	Enabled bool
	Healthy bool
	Reason  string

	ForecastHorizon time.Duration
	Samples         int
	Neighbors       int

	AskExpectedReturn      float64
	BidExpectedReturn      float64
	TerminalExpectedReturn float64
	ExpectedReturn         float64
	ReturnVariance         float64
	MeanSE                 float64

	PosteriorUpProbability float64
	ProfitableProbability  float64
	ModelProbability       float64
	Direction              int
	StructuralDirection    int
	StructuralProbability  float64
	StructuralExcursion    float64

	UpRemainingExcursion   float64
	DownRemainingExcursion float64
	UpProfitProbability    float64
	DownProfitProbability  float64
	UpMeanSE               float64
	DownMeanSE             float64
	UpExpectedPivot        time.Duration
	DownExpectedPivot      time.Duration
	RemainingExcursion     float64
	ExpectedPivot          time.Duration
	Continuation           TrendContinuationDecision
}

// TrendContinuationDecision is the posterior predictive competing-risk law
// for the next executable move after the current short-window path shape.
// Up means buy at the current ask and later sell at a bid above full round-trip
// cost. Down means sell at the current bid and later buy at an ask below full
// round-trip cost. Neither event inside the forecast horizon is right-censored.
// A Dirichlet(1,1,1) posterior keeps all three outcomes in one probability
// simplex instead of treating two correlated binary classifiers as evidence.
type TrendContinuationDecision struct {
	Healthy bool
	Reason  string

	Window             time.Duration
	ForecastHorizon    time.Duration
	RecentDirection    int
	ConsolidationScore float64

	Samples                  int
	UpFirst                  int
	DownFirst                int
	Censored                 int
	UpProbability            float64
	DownProbability          float64
	CensorProbability        float64
	DownGivenMoveProbability float64
	DownGivenMoveLower       float64

	ExpectedReturn   float64
	ReturnVariance   float64
	MeanSE           float64
	ExpectedPassage  time.Duration
	Direction        int
	ModelProbability float64
}

type trendExcursionCacheEntry struct {
	BarCount       int
	LastClosed     time.Time
	Enabled        bool
	RoundTripCost  float64
	BarInterval    time.Duration
	Lookback       time.Duration
	MinimumSamples int
	ConfidenceZ    float64
	Horizons       string
	Decision       TrendExcursionDecision
}

type trendExcursionSample struct {
	features                  []float64
	askTerminal               float64
	bidTerminal               float64
	askMaximum                float64
	bidMaximum                float64
	askMinimum                float64
	bidMinimum                float64
	askMaxStep                int
	bidMaxStep                int
	askMinStep                int
	bidMinStep                int
	distance                  float64
	continuationOutcome       int
	continuationUpExcursion   float64
	continuationDownExcursion float64
	continuationFirstStep     int
}

type trendConsolidationState struct {
	window             time.Duration
	recentDirection    int
	consolidationScore float64
	features           []float64
}

type trendSideSummary struct {
	mean            float64
	variance        float64
	upProbability   float64
	upProfit        float64
	downProfit      float64
	maximum         float64
	minimum         float64
	maximumVariance float64
	minimumVariance float64
	maxStep         float64
	minStep         float64
}

func trendConsolidationStateAt(
	bars []macroInventoryBar,
	anchor int,
	horizons []time.Duration,
	interval time.Duration,
) (trendConsolidationState, bool) {
	var state trendConsolidationState
	if anchor < 0 || anchor >= len(bars) || len(horizons) == 0 || interval <= 0 {
		return state, false
	}
	longSteps := int(horizons[0] / interval)
	if longSteps <= 0 || anchor-longSteps < 0 {
		return state, false
	}
	// The short state length is the square-root temporal scale of the forecast.
	// This is the usual diffusion scaling and avoids introducing another fitted
	// 20/30/60-minute switch. For 3h on 10-minute bars it is five bars.
	shortSteps := int(math.Ceil(math.Sqrt(float64(longSteps))))
	if shortSteps < 2 {
		shortSteps = 2
	}
	if shortSteps > longSteps {
		shortSteps = longSteps
	}
	shortStart := anchor - shortSteps
	longStart := anchor - longSteps
	if shortStart < 0 {
		return state, false
	}
	current := bars[anchor]
	first := bars[shortStart]
	longFirst := bars[longStart]
	if current.Ask <= 0 || current.Bid <= 0 || first.Ask <= 0 || first.Bid <= 0 ||
		longFirst.Ask <= 0 || longFirst.Bid <= 0 ||
		current.Segment != first.Segment || current.Segment != longFirst.Segment {
		return state, false
	}
	netAsk := math.Log(current.Ask / first.Ask)
	netBid := math.Log(current.Bid / first.Bid)
	minAsk, maxAsk := 0.0, 0.0
	minBid, maxBid := 0.0, 0.0
	qvAsk, qvBid := 0.0, 0.0
	variationAsk, variationBid := 0.0, 0.0
	for i := shortStart + 1; i <= anchor; i++ {
		previous, point := bars[i-1], bars[i]
		if point.Segment != current.Segment || point.Ask <= 0 || point.Bid <= 0 ||
			previous.Ask <= 0 || previous.Bid <= 0 {
			return trendConsolidationState{}, false
		}
		askStep := math.Log(point.Ask / previous.Ask)
		bidStep := math.Log(point.Bid / previous.Bid)
		qvAsk += askStep * askStep
		qvBid += bidStep * bidStep
		variationAsk += math.Abs(askStep)
		variationBid += math.Abs(bidStep)
		askLevel := math.Log(point.Ask / first.Ask)
		bidLevel := math.Log(point.Bid / first.Bid)
		minAsk, maxAsk = math.Min(minAsk, askLevel), math.Max(maxAsk, askLevel)
		minBid, maxBid = math.Min(minBid, bidLevel), math.Max(maxBid, bidLevel)
	}
	efficiencyAsk := 0.0
	if variationAsk > 0 {
		efficiencyAsk = math.Abs(netAsk) / variationAsk
	}
	efficiencyBid := 0.0
	if variationBid > 0 {
		efficiencyBid = math.Abs(netBid) / variationBid
	}
	locationAsk := 0.5
	if maxAsk > minAsk {
		locationAsk = (netAsk - minAsk) / (maxAsk - minAsk)
	}
	locationBid := 0.5
	if maxBid > minBid {
		locationBid = (netBid - minBid) / (maxBid - minBid)
	}
	longAsk := math.Log(current.Ask / longFirst.Ask)
	longBid := math.Log(current.Bid / longFirst.Bid)
	longMean := conservativeTrendMean(longAsk, longBid)
	switch {
	case longMean > 0:
		state.recentDirection = 1
	case longMean < 0:
		state.recentDirection = -1
	}
	state.window = time.Duration(shortSteps) * interval
	state.consolidationScore = clampRatio(
		1-math.Max(efficiencyAsk, efficiencyBid), 0, 1)
	state.features = []float64{
		netAsk, netBid,
		math.Sqrt(qvAsk), math.Sqrt(qvBid),
		efficiencyAsk, efficiencyBid,
		locationAsk, locationBid,
		maxAsk - minAsk, maxBid - minBid,
	}
	return state, true
}

func trendBarFeatures(bars []macroInventoryBar, anchor int, horizons []time.Duration, interval time.Duration) ([]float64, bool) {
	if anchor < 0 || anchor >= len(bars) || interval <= 0 {
		return nil, false
	}
	current := bars[anchor]
	if current.Ask <= 0 || current.Bid <= 0 {
		return nil, false
	}
	features := make([]float64, 0, 2*len(horizons))
	for _, horizon := range horizons {
		steps := int(horizon / interval)
		previousIndex := anchor - steps
		if steps <= 0 || previousIndex < 0 {
			return nil, false
		}
		previous := bars[previousIndex]
		if previous.Segment != current.Segment ||
			!previous.At.Add(horizon).Equal(current.At) ||
			previous.Ask <= 0 || previous.Bid <= 0 {
			return nil, false
		}
		features = append(features,
			math.Log(current.Ask/previous.Ask),
			math.Log(current.Bid/previous.Bid))
	}
	state, ok := trendConsolidationStateAt(bars, anchor, horizons, interval)
	if !ok {
		return nil, false
	}
	features = append(features, state.features...)
	return features, true
}

func trendForwardSample(
	bars []macroInventoryBar,
	anchor, forecastSteps int,
	features []float64,
	roundTripCost float64,
) (trendExcursionSample, bool) {
	sample := trendExcursionSample{features: features}
	if anchor < 0 || forecastSteps <= 0 || anchor+forecastSteps >= len(bars) {
		return sample, false
	}
	start := bars[anchor]
	end := bars[anchor+forecastSteps]
	if start.Ask <= 0 || start.Bid <= 0 || end.Ask <= 0 || end.Bid <= 0 ||
		start.Segment != end.Segment {
		return sample, false
	}
	sample.askMaximum, sample.bidMaximum = math.Inf(-1), math.Inf(-1)
	sample.askMinimum, sample.bidMinimum = math.Inf(1), math.Inf(1)
	for step := 1; step <= forecastSteps; step++ {
		point := bars[anchor+step]
		if point.Segment != start.Segment || point.Ask <= 0 || point.Bid <= 0 {
			return trendExcursionSample{}, false
		}
		askReturn := math.Log(point.Ask / start.Ask)
		bidReturn := math.Log(point.Bid / start.Bid)
		if askReturn > sample.askMaximum {
			sample.askMaximum, sample.askMaxStep = askReturn, step
		}
		if bidReturn > sample.bidMaximum {
			sample.bidMaximum, sample.bidMaxStep = bidReturn, step
		}
		if askReturn < sample.askMinimum {
			sample.askMinimum, sample.askMinStep = askReturn, step
		}
		if bidReturn < sample.bidMinimum {
			sample.bidMinimum, sample.bidMinStep = bidReturn, step
		}
		upExcursion := math.Log(point.Bid/start.Ask) - roundTripCost
		downExcursion := -math.Log(point.Ask/start.Bid) - roundTripCost
		sample.continuationUpExcursion = math.Max(
			sample.continuationUpExcursion, upExcursion)
		sample.continuationDownExcursion = math.Max(
			sample.continuationDownExcursion, downExcursion)
		if sample.continuationOutcome == 0 {
			switch {
			case upExcursion >= 0 && downExcursion < 0:
				sample.continuationOutcome = 1
				sample.continuationFirstStep = step
			case downExcursion >= 0 && upExcursion < 0:
				sample.continuationOutcome = -1
				sample.continuationFirstStep = step
			}
		}
	}
	sample.askTerminal = math.Log(end.Ask / start.Ask)
	sample.bidTerminal = math.Log(end.Bid / start.Bid)
	return sample, true
}

func summarizeTrendSide(samples []trendExcursionSample, ask bool, cost float64) trendSideSummary {
	var out trendSideSummary
	if len(samples) == 0 {
		return out
	}
	positive, upProfit, downProfit := 0, 0, 0
	for _, sample := range samples {
		terminal, maximum, minimum := sample.bidTerminal, sample.bidMaximum, sample.bidMinimum
		maxStep, minStep := sample.bidMaxStep, sample.bidMinStep
		if ask {
			terminal, maximum, minimum = sample.askTerminal, sample.askMaximum, sample.askMinimum
			maxStep, minStep = sample.askMaxStep, sample.askMinStep
		}
		out.mean += terminal
		out.maximum += maximum
		out.minimum += minimum
		out.maxStep += float64(maxStep)
		out.minStep += float64(minStep)
		if terminal > 0 {
			positive++
		}
		if maximum > cost {
			upProfit++
		}
		if minimum < -cost {
			downProfit++
		}
	}
	n := float64(len(samples))
	out.mean /= n
	out.maximum /= n
	out.minimum /= n
	out.maxStep /= n
	out.minStep /= n
	if len(samples) > 1 {
		for _, sample := range samples {
			terminal, maximum, minimum := sample.bidTerminal, sample.bidMaximum, sample.bidMinimum
			if ask {
				terminal, maximum, minimum = sample.askTerminal, sample.askMaximum, sample.askMinimum
			}
			out.variance += math.Pow(terminal-out.mean, 2)
			out.maximumVariance += math.Pow(maximum-out.maximum, 2)
			out.minimumVariance += math.Pow(minimum-out.minimum, 2)
		}
		out.variance /= n - 1
		out.maximumVariance /= n - 1
		out.minimumVariance /= n - 1
	}
	// A symmetric Beta(1,1) prior prevents a small neighbor set from reporting
	// certainty. Non-overlapping labels make n the effective observation count.
	out.upProbability = (1 + float64(positive)) / (2 + n)
	out.upProfit = (1 + float64(upProfit)) / (2 + n)
	out.downProfit = (1 + float64(downProfit)) / (2 + n)
	return out
}

func summarizeTrendContinuation(
	samples []trendExcursionSample,
	state trendConsolidationState,
	forecast, interval time.Duration,
	confidenceZ float64,
) TrendContinuationDecision {
	d := TrendContinuationDecision{
		Window: state.window, ForecastHorizon: forecast,
		RecentDirection:    state.recentDirection,
		ConsolidationScore: state.consolidationScore,
		Samples:            len(samples), Reason: "insufficient executable first-passage analogs",
	}
	if len(samples) == 0 || interval <= 0 || forecast <= 0 {
		return d
	}
	passageSteps := 0.0
	for _, sample := range samples {
		switch sample.continuationOutcome {
		case 1:
			d.UpFirst++
			passageSteps += float64(sample.continuationFirstStep)
		case -1:
			d.DownFirst++
			passageSteps += float64(sample.continuationFirstStep)
		default:
			d.Censored++
		}
	}
	// Competing outcomes and right-censoring share one Dirichlet posterior.
	// This preserves probability mass for quiet periods instead of forcing the
	// two directional probabilities to sum to one.
	denominator := float64(len(samples) + 3)
	d.UpProbability = float64(d.UpFirst+1) / denominator
	d.DownProbability = float64(d.DownFirst+1) / denominator
	d.CensorProbability = float64(d.Censored+1) / denominator
	resolved := d.UpFirst + d.DownFirst
	a := float64(d.DownFirst + 1)
	b := float64(d.UpFirst + 1)
	d.DownGivenMoveProbability = a / (a + b)
	posteriorVariance := a * b / (math.Pow(a+b, 2) * (a + b + 1))
	d.DownGivenMoveLower = clampRatio(
		d.DownGivenMoveProbability-math.Max(0, confidenceZ)*math.Sqrt(posteriorVariance),
		0, 1)
	d.ExpectedReturn, d.ReturnVariance, d.MeanSE = continuationPosteriorMoments(
		samples, d.UpProbability, d.DownProbability)
	if resolved > 0 {
		d.ExpectedPassage = time.Duration(passageSteps/float64(resolved)) * interval
	}
	// Directional uncertainty is already present in ExpectedReturn through the
	// complete Dirichlet posterior. Reusing |P(up)-P(down)| as a model weight
	// would shrink the same uncertainty twice. ModelProbability retains the resolved-event mass for telemetry; it must not
	// be reused as a second weight on ExpectedReturn.
	d.ModelProbability = clampRatio(1-d.CensorProbability, 0, 1)
	switch {
	case d.ExpectedReturn > 0:
		d.Direction = 1
	case d.ExpectedReturn < 0:
		d.Direction = -1
	}
	d.Healthy = true
	if d.RecentDirection != 0 && d.Direction == d.RecentDirection {
		d.Reason = "same-direction executable continuation posterior"
	} else if d.Direction != 0 {
		d.Reason = "opposite-direction executable first-passage posterior"
	} else {
		d.Reason = "executable first-passage posterior is balanced"
	}
	return d
}

func conservativeTrendMean(ask, bid float64) float64 {
	switch {
	case ask > 0 && bid > 0:
		return math.Min(ask, bid)
	case ask < 0 && bid < 0:
		return math.Max(ask, bid)
	default:
		return 0
	}
}

func conservativeTrendProbability(ask, bid float64) float64 {
	switch {
	case ask > 0.5 && bid > 0.5:
		return math.Min(ask, bid)
	case ask < 0.5 && bid < 0.5:
		return math.Max(ask, bid)
	default:
		return 0.5
	}
}

// EstimateTrendExcursion performs k-nearest-neighbor regression with k=sqrt(N).
// Training labels are spaced one forecast horizon apart, so overlapping
// forward returns are not counted as independent evidence. Feature scaling is
// learned from the available history and no pretrained artifact is required.
func (m *MacroInventoryModel) EstimateTrendExcursion(
	now time.Time,
	c MacroInventoryConfig,
	enabled bool,
	roundTripCostBps float64,
) TrendExcursionDecision {
	d := TrendExcursionDecision{Enabled: enabled, Reason: "disabled"}
	if m == nil || !enabled {
		return d
	}
	c.setDefaults()
	horizons := c.horizons()
	interval := time.Duration(c.BarInterval)
	lastClosed := m.LatestClosedBarAt()
	signature := fmt.Sprint(horizons)
	cache := m.trendExcursionCache
	if cache.BarCount == len(m.bars) && cache.LastClosed.Equal(lastClosed) &&
		cache.Enabled == enabled && cache.RoundTripCost == roundTripCostBps &&
		cache.BarInterval == interval && cache.Lookback == time.Duration(c.Lookback) &&
		cache.MinimumSamples == c.MinimumSamples && cache.ConfidenceZ == c.DownsideZScore &&
		cache.Horizons == signature {
		return cache.Decision
	}
	d = m.estimateTrendExcursionUncached(now, c, roundTripCostBps)
	m.trendExcursionCache = trendExcursionCacheEntry{
		BarCount: len(m.bars), LastClosed: lastClosed, Enabled: enabled,
		RoundTripCost: roundTripCostBps, BarInterval: interval,
		Lookback: time.Duration(c.Lookback), MinimumSamples: c.MinimumSamples,
		ConfidenceZ: c.DownsideZScore,
		Horizons:    signature, Decision: d,
	}
	return d
}

func (m *MacroInventoryModel) estimateTrendExcursionUncached(
	now time.Time,
	c MacroInventoryConfig,
	roundTripCostBps float64,
) TrendExcursionDecision {
	d := TrendExcursionDecision{Enabled: true, Reason: "insufficient completed long-window analogs"}
	horizons := c.horizons()
	interval := time.Duration(c.BarInterval)
	if now.IsZero() || interval <= 0 || len(horizons) == 0 || len(m.bars) < 3 {
		return d
	}
	forecast := horizons[0]
	longest := horizons[len(horizons)-1]
	forecastSteps := int(forecast / interval)
	longestSteps := int(longest / interval)
	if forecastSteps <= 0 || longestSteps <= 0 {
		return d
	}
	end := len(m.bars) - 1
	segment := m.bars[end].Segment
	segmentStart := end
	for segmentStart > 0 && m.bars[segmentStart-1].Segment == segment {
		segmentStart--
	}
	if end-segmentStart < longestSteps+forecastSteps {
		return d
	}
	currentFeatures, ok := trendBarFeatures(m.bars, end, horizons, interval)
	if !ok {
		return d
	}
	currentState, stateOK := trendConsolidationStateAt(
		m.bars, end, horizons, interval)
	if !stateOK {
		return d
	}
	cost := math.Max(0, roundTripCostBps) / 10_000

	candidates := make([]trendExcursionSample, 0)
	firstAnchor := segmentStart + longestSteps
	for anchor := end - forecastSteps; anchor >= firstAnchor; anchor -= forecastSteps {
		if m.bars[anchor+forecastSteps].At.Sub(m.bars[anchor].At) != forecast {
			continue
		}
		features, featureOK := trendBarFeatures(m.bars, anchor, horizons, interval)
		if !featureOK {
			continue
		}
		sample, sampleOK := trendForwardSample(
			m.bars, anchor, forecastSteps, features, cost)
		if sampleOK {
			candidates = append(candidates, sample)
		}
	}
	d.Samples = len(candidates)
	if len(candidates) < c.MinimumSamples {
		return d
	}

	scales := make([]float64, len(currentFeatures))
	means := make([]float64, len(currentFeatures))
	for _, sample := range candidates {
		for i, value := range sample.features {
			means[i] += value
		}
	}
	for i := range means {
		means[i] /= float64(len(candidates))
	}
	for _, sample := range candidates {
		for i, value := range sample.features {
			scales[i] += math.Pow(value-means[i], 2)
		}
	}
	for i := range scales {
		scales[i] = math.Sqrt(scales[i] / math.Max(1, float64(len(candidates)-1)))
	}
	for i := range candidates {
		for feature, value := range candidates[i].features {
			if scales[feature] > 0 {
				candidates[i].distance += math.Pow((value-currentFeatures[feature])/scales[feature], 2)
			}
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].distance < candidates[j].distance
	})
	neighbors := int(math.Ceil(math.Sqrt(float64(len(candidates)))))
	if neighbors < c.MinimumSamples {
		neighbors = c.MinimumSamples
	}
	if neighbors > len(candidates) {
		neighbors = len(candidates)
	}
	selected := candidates[:neighbors]
	d.Neighbors = neighbors
	ask := summarizeTrendSide(selected, true, cost)
	bid := summarizeTrendSide(selected, false, cost)
	d.AskExpectedReturn = ask.mean
	d.BidExpectedReturn = bid.mean
	d.TerminalExpectedReturn = conservativeTrendMean(ask.mean, bid.mean)
	d.ReturnVariance = math.Max(ask.variance, bid.variance)
	d.PosteriorUpProbability = conservativeTrendProbability(
		ask.upProbability, bid.upProbability)
	d.UpRemainingExcursion = math.Max(0, math.Min(ask.maximum, bid.maximum))
	d.DownRemainingExcursion = math.Max(0, math.Min(-ask.minimum, -bid.minimum))
	d.UpProfitProbability = math.Min(ask.upProfit, bid.upProfit)
	d.DownProfitProbability = math.Min(ask.downProfit, bid.downProfit)
	d.UpMeanSE = math.Sqrt(math.Max(ask.maximumVariance, bid.maximumVariance) / float64(neighbors))
	d.DownMeanSE = math.Sqrt(math.Max(ask.minimumVariance, bid.minimumVariance) / float64(neighbors))
	d.UpExpectedPivot = time.Duration(0.5*(ask.maxStep+bid.maxStep)) * interval
	d.DownExpectedPivot = time.Duration(0.5*(ask.minStep+bid.minStep)) * interval

	switch {
	case d.TerminalExpectedReturn > 0 && d.PosteriorUpProbability > 0.5:
		d.Direction = 1
		d.ProfitableProbability = d.UpProfitProbability
		d.RemainingExcursion = d.UpRemainingExcursion
		d.ExpectedReturn = d.RemainingExcursion
		d.MeanSE = d.UpMeanSE
		d.ExpectedPivot = d.UpExpectedPivot
	case d.TerminalExpectedReturn < 0 && d.PosteriorUpProbability < 0.5:
		d.Direction = -1
		d.ProfitableProbability = d.DownProfitProbability
		d.RemainingExcursion = d.DownRemainingExcursion
		d.ExpectedReturn = -d.RemainingExcursion
		d.MeanSE = d.DownMeanSE
		d.ExpectedPivot = d.DownExpectedPivot
	default:
		d.ProfitableProbability = 0.5
		d.MeanSE = math.Sqrt(d.ReturnVariance / float64(neighbors))
	}
	directionConfidence := math.Abs(2*d.PosteriorUpProbability - 1)
	if d.Direction > 0 {
		// New inventory must clear a complete entry/exit cost in the historical
		// pivot distribution. Existing long inventory can be reduced on a
		// negative-return posterior; the no-trade boundary prices its turnover.
		profitConfidence := math.Max(0, 2*d.ProfitableProbability-1)
		d.ModelProbability = clampRatio(directionConfidence*profitConfidence, 0, 1)
	} else if d.Direction < 0 {
		d.ModelProbability = clampRatio(directionConfidence, 0, 1)
	}
	d.Continuation = summarizeTrendContinuation(
		selected, currentState, forecast, interval, c.DownsideZScore)
	d.ForecastHorizon = forecast
	d.Healthy = true
	if d.Direction == 0 || d.ModelProbability == 0 {
		d.Reason = "long-window executable analogs are directionally ambiguous"
	} else {
		d.Reason = "causal long-window executable pivot analog"
	}
	return d
}

// ConditionTrendExcursionOnReversal uses the fee-gated sequential/BIC
// change-point posterior as the regime selector and retains the analog model
// only for conditional excursion magnitude and pivot timing. It does not add a
// second inventory target; the returned posterior still enters the single
// QV/Merton/no-trade controller exactly once.
func multiplicityCorrectedReversalProbability(reversal MacroReversalDecision) float64 {
	aggregate := clampRatio(reversal.AggregateProbability, 0.5, 1)
	best := 0.5
	for _, horizon := range reversal.Horizons {
		if horizon.Applied && horizon.Direction == reversal.Direction {
			best = math.Max(best, horizon.ReversalProbability)
		}
	}
	if best <= 0.5 {
		return aggregate
	}
	if best >= 1 {
		return 1
	}
	// The candidate horizon has prior mass 1/m relative to the null. Dividing
	// its posterior odds by m is the Bayesian counterpart of the log(m)
	// selection penalty and prevents an uncorrected max over overlapping windows.
	models := math.Max(1, float64(reversal.HealthyHorizons))
	odds := best / (1 - best) / models
	selected := odds / (1 + odds)
	return math.Max(aggregate, selected)
}

func robustStructuralExcursion(
	reversal MacroReversalDecision,
	direction int,
	pivot time.Duration,
	confidenceZ float64,
) float64 {
	if direction == 0 || pivot <= 0 {
		return 0
	}
	weighted, weightSum := 0.0, 0.0
	for _, horizon := range reversal.Horizons {
		if !horizon.Applied || horizon.Direction != direction || horizon.ForecastHorizon <= 0 {
			continue
		}
		projection := pivot
		if projection > horizon.ForecastHorizon {
			projection = horizon.ForecastHorizon
		}
		robustBps := float64(direction)*horizon.ForecastMeanBps -
			math.Max(0, confidenceZ)*horizon.ForecastMeanSEBps
		if robustBps <= 0 {
			continue
		}
		amplitude := robustBps * projection.Seconds() / horizon.ForecastHorizon.Seconds() / 10_000
		weight := math.Max(0.5, horizon.ReversalProbability)
		weighted += weight * amplitude
		weightSum += weight
	}
	if weightSum <= 0 {
		return 0
	}
	return weighted / weightSum
}

func ConditionTrendExcursionOnReversal(
	trend TrendExcursionDecision,
	reversal MacroReversalDecision,
	confidenceZ float64,
) TrendExcursionDecision {
	if !trend.Healthy || !reversal.Applied || reversal.Direction == 0 {
		return trend
	}
	probability := multiplicityCorrectedReversalProbability(reversal)
	trend.StructuralDirection = reversal.Direction
	trend.StructuralProbability = probability
	trend.Direction = reversal.Direction
	trend.ModelProbability = clampRatio(2*probability-1, 0, 1)
	if trend.Direction > 0 {
		trend.PosteriorUpProbability = probability
		trend.ProfitableProbability = trend.UpProfitProbability
		trend.RemainingExcursion = trend.UpRemainingExcursion
		trend.ExpectedReturn = trend.RemainingExcursion
		trend.MeanSE = trend.UpMeanSE
		trend.ExpectedPivot = trend.UpExpectedPivot
		trend.StructuralExcursion = robustStructuralExcursion(
			reversal, trend.Direction, trend.ExpectedPivot, confidenceZ)
		if trend.StructuralExcursion > 0 {
			trend.RemainingExcursion = trend.StructuralExcursion
			trend.ExpectedReturn = trend.StructuralExcursion
		}
	} else {
		trend.PosteriorUpProbability = 1 - probability
		trend.ProfitableProbability = trend.DownProfitProbability
		trend.RemainingExcursion = trend.DownRemainingExcursion
		trend.ExpectedReturn = -trend.RemainingExcursion
		trend.MeanSE = trend.DownMeanSE
		trend.ExpectedPivot = trend.DownExpectedPivot
		trend.StructuralExcursion = robustStructuralExcursion(
			reversal, trend.Direction, trend.ExpectedPivot, confidenceZ)
		if trend.StructuralExcursion > 0 {
			trend.RemainingExcursion = trend.StructuralExcursion
			trend.ExpectedReturn = -trend.StructuralExcursion
		}
	}
	trend.Reason = "fee-gated change point with conditional executable pivot excursion"
	return trend
}
