package main

import (
	"encoding/json"
	"math"
	"os"
	"time"

	"github.com/c9s/bbgo/pkg/strategy/gammacapture"
)

type competingPathStudyInput struct {
	DataPath, Symbol, ConfigPath, ReplayCacheDir string
	From, To                                     time.Time
}

type competingPathHorizonReport struct {
	Horizon                       string  `json:"horizon"`
	DistanceBps                   float64 `json:"distanceBps"`
	EligibleSamples               int     `json:"eligibleSamples"`
	EffectiveSamples              float64 `json:"effectiveSamples"`
	CompetingBrier                float64 `json:"competingBrier"`
	BaselineBrier                 float64 `json:"baselineBrier"`
	CompetingLogLoss              float64 `json:"competingLogLoss"`
	BaselineLogLoss               float64 `json:"baselineLogLoss"`
	MeanAbsoluteValueErrorBps     float64 `json:"meanAbsoluteValueErrorBps"`
	BaselineAbsoluteValueErrorBps float64 `json:"baselineAbsoluteValueErrorBps"`
	IncrementalMeanBps            float64 `json:"incrementalMeanBps"`
	IncrementalStandardErrorBps   float64 `json:"incrementalStandardErrorBps"`
	SimultaneousLowerBoundBps     float64 `json:"simultaneousLowerBoundBps"`
	PositiveBlocks                int     `json:"positiveBlocks"`
	TotalBlocks                   int     `json:"totalBlocks"`
}

type competingPathStudyReport struct {
	Name           string                       `json:"name"`
	Symbol         string                       `json:"symbol"`
	From           time.Time                    `json:"from"`
	To             time.Time                    `json:"to"`
	Causal         bool                         `json:"causal"`
	FeeBpsPerFill  float64                      `json:"feeBpsPerFill"`
	CandidateTests int                          `json:"candidateTests"`
	Results        []competingPathHorizonReport `json:"results"`
}

type conditionalPayoffHorizonReport struct {
	Horizon                     string  `json:"horizon"`
	DistanceBps                 float64 `json:"distanceBps"`
	HalfLife                    string  `json:"halfLife"`
	EligibleOneSidedSamples     int     `json:"eligibleOneSidedSamples"`
	EffectiveSamples            float64 `json:"effectiveSamples"`
	BaselineAbsoluteErrorBps    float64 `json:"baselineAbsoluteErrorBps"`
	ShrunkAbsoluteErrorBps      float64 `json:"shrunkAbsoluteErrorBps"`
	IncrementalMeanBps          float64 `json:"incrementalMeanBps"`
	IncrementalStandardErrorBps float64 `json:"incrementalStandardErrorBps"`
	SimultaneousLowerBoundBps   float64 `json:"simultaneousLowerBoundBps"`
	PositiveBlocks              int     `json:"positiveBlocks"`
	TotalBlocks                 int     `json:"totalBlocks"`
}

type conditionalPayoffStudyReport struct {
	Name           string                           `json:"name"`
	Symbol         string                           `json:"symbol"`
	From           time.Time                        `json:"from"`
	To             time.Time                        `json:"to"`
	Causal         bool                             `json:"causal"`
	FeeBpsPerFill  float64                          `json:"feeBpsPerFill"`
	CandidateTests int                              `json:"candidateTests"`
	Results        []conditionalPayoffHorizonReport `json:"results"`
}

type sideImbalancePayoffHorizonReport struct {
	Horizon                     string  `json:"horizon"`
	DistanceBps                 float64 `json:"distanceBps"`
	HalfLife                    string  `json:"halfLife"`
	EligibleSamples             int     `json:"eligibleSamples"`
	EffectiveSamples            float64 `json:"effectiveSamples"`
	DepthReadyPct               float64 `json:"depthReadyPct"`
	BaselineAbsoluteErrorBps    float64 `json:"baselineAbsoluteErrorBps"`
	ConditionalAbsoluteErrorBps float64 `json:"conditionalAbsoluteErrorBps"`
	IncrementalMeanBps          float64 `json:"incrementalMeanBps"`
	IncrementalStandardErrorBps float64 `json:"incrementalStandardErrorBps"`
	SimultaneousLowerBoundBps   float64 `json:"simultaneousLowerBoundBps"`
	PositiveBlocks              int     `json:"positiveBlocks"`
	TotalBlocks                 int     `json:"totalBlocks"`
	BaselineActions             int     `json:"baselineActions"`
	ConditionalActions          int     `json:"conditionalActions"`
	ActionDisagreements         int     `json:"actionDisagreements"`
	BaselineActionValueBps      float64 `json:"baselineActionValueBps"`
	ConditionalActionValueBps   float64 `json:"conditionalActionValueBps"`
	ComponentIncrementalBps     float64 `json:"componentIncrementalBps"`
	ComponentMeanBps            float64 `json:"componentMeanBps"`
	ComponentStandardErrorBps   float64 `json:"componentStandardErrorBps"`
	ComponentLowerBoundBps      float64 `json:"componentLowerBoundBps"`
	IdentificationFloorBps      float64 `json:"identificationFloorBps"`
}

type sideImbalancePayoffStudyReport struct {
	Name           string                             `json:"name"`
	Symbol         string                             `json:"symbol"`
	From           time.Time                          `json:"from"`
	To             time.Time                          `json:"to"`
	Causal         bool                               `json:"causal"`
	FeeBpsPerFill  float64                            `json:"feeBpsPerFill"`
	CandidateTests int                                `json:"candidateTests"`
	Results        []sideImbalancePayoffHorizonReport `json:"results"`
}

type competingMarginalBaseline struct {
	total, buy, sell, both float64
}

func (b competingMarginalBaseline) probabilities() [4]float64 {
	denominator := b.total + 1
	buy := (b.buy + 0.5) / denominator
	sell := (b.sell + 0.5) / denominator
	both := (b.both + 0.5) / denominator
	both = math.Max(math.Max(0, buy+sell-1), math.Min(math.Min(buy, sell), both))
	return [4]float64{
		math.Max(0, 1-buy-sell+both),
		math.Max(0, buy-both),
		math.Max(0, sell-both),
		both,
	}
}

func (b *competingMarginalBaseline) update(outcome gammacapture.CompetingPathOutcome) {
	b.updateWeighted(outcome, 1)
}

func (b *competingMarginalBaseline) updateWeighted(outcome gammacapture.CompetingPathOutcome, weight float64) {
	if weight <= 0 {
		return
	}
	b.total += weight
	switch outcome {
	case gammacapture.CompetingPathBoth:
		b.buy += weight
		b.sell += weight
		b.both += weight
	case gammacapture.CompetingPathBuyOnly:
		b.buy += weight
	case gammacapture.CompetingPathSellOnly:
		b.sell += weight
	}
}

type competingPathObservation struct {
	at, maturity time.Time
	firstEvent   time.Time
	outcome      gammacapture.CompetingPathOutcome
	valueBps     float64
	bothValueBps float64
	imbalance    float64
	depthReady   bool
}

func competingPathObservations(books []bboSnapshot, from, to time.Time, horizon time.Duration, distanceBps, feeBps float64) []competingPathObservation {
	return competingPathObservationsAtStep(books, from, to, horizon, horizon, distanceBps, feeBps)
}

func competingPathObservationsAtStep(books []bboSnapshot, from, to time.Time, horizon, step time.Duration, distanceBps, feeBps float64) []competingPathObservation {
	if horizon <= 0 || step <= 0 || distanceBps <= 0 || len(books) < 2 {
		return nil
	}
	observations := make([]competingPathObservation, 0)
	left := 0
	for anchor := from; anchor.Before(to); {
		for left < len(books) && books[left].time.Before(anchor) {
			left++
		}
		if left >= len(books) {
			break
		}
		if books[left].time.Sub(anchor) > time.Minute ||
			books[left].bid <= 0 || books[left].ask < books[left].bid {
			anchor = anchor.Add(step)
			continue
		}
		predictionAt := books[left].time
		maturity := predictionAt.Add(horizon)
		if maturity.After(to) {
			break
		}
		right := left + 1
		buyQuote := books[left].ask * math.Exp(-distanceBps/10_000)
		sellQuote := books[left].bid * math.Exp(distanceBps/10_000)
		buyTouched, sellTouched := false, false
		firstEvent := time.Time{}
		for right < len(books) && !books[right].time.After(maturity) {
			buyCrossed := books[right].ask <= buyQuote
			sellCrossed := books[right].bid >= sellQuote
			if firstEvent.IsZero() && (buyCrossed || sellCrossed) {
				firstEvent = books[right].time
			}
			buyTouched = buyTouched || buyCrossed
			sellTouched = sellTouched || sellCrossed
			right++
		}
		terminal := right - 1
		if terminal <= left || maturity.Sub(books[terminal].time) > time.Minute ||
			books[terminal].bid <= 0 || books[terminal].ask < books[terminal].bid {
			anchor = predictionAt.Add(step)
			continue
		}
		outcome := gammacapture.CompetingPathOutcomeFromTouches(buyTouched, sellTouched)
		bothValue := math.Log(sellQuote/buyQuote)*10_000 - 2*feeBps
		value := 0.0
		switch outcome {
		case gammacapture.CompetingPathBoth:
			value = bothValue
		case gammacapture.CompetingPathBuyOnly:
			value = math.Log(books[terminal].bid/buyQuote)*10_000 - feeBps
		case gammacapture.CompetingPathSellOnly:
			value = math.Log(sellQuote/books[terminal].ask)*10_000 - feeBps
		}
		observations = append(observations, competingPathObservation{
			at: predictionAt, maturity: maturity, firstEvent: firstEvent, outcome: outcome,
			valueBps: value, bothValueBps: bothValue,
			imbalance: func() float64 {
				denominator := books[left].bidSize + books[left].askSize
				if denominator <= 0 {
					return 0
				}
				return math.Max(-1, math.Min(1, (books[left].bidSize-books[left].askSize)/denominator))
			}(),
			depthReady: books[left].bidSize > 0 && books[left].askSize > 0,
		})
		anchor = predictionAt.Add(step)
	}
	return observations
}

// eventClockScores identifies the observations at which the strategy could
// actually replace a quote.  A quote is re-based only after its selected Fast
// horizon expires or after the first observable crossing event.  The event
// BBO belongs to the old order, so the next observation strictly after it is
// the first eligible decision for the replacement order.
func eventClockScores(observations []competingPathObservation, from time.Time) map[time.Time]bool {
	scores := make(map[time.Time]bool)
	nextEligible := from
	for _, observation := range observations {
		if observation.at.Before(from) || observation.at.Before(nextEligible) {
			continue
		}
		scores[observation.at] = true
		nextEligible = observation.maturity
		if !observation.firstEvent.IsZero() && observation.firstEvent.Before(nextEligible) {
			nextEligible = observation.firstEvent.Add(time.Nanosecond)
		}
	}
	return scores
}

func competingExpectedValue(probabilities [4]float64, means [4]float64) float64 {
	value := 0.0
	for i := range probabilities {
		value += probabilities[i] * means[i]
	}
	return value
}

// equalDayMeanSE treats UTC days, rather than correlated quote evaluations, as
// the independent units for the paired alpha gate.
func equalDayMeanSE(sums map[string]float64, counts map[string]int) (mean, standardError float64, positive, total int) {
	dayMeans := make([]float64, 0, len(sums))
	for day, sum := range sums {
		if counts[day] <= 0 {
			continue
		}
		value := sum / float64(counts[day])
		dayMeans = append(dayMeans, value)
		if value > 0 {
			positive++
		}
	}
	total = len(dayMeans)
	if total == 0 {
		return 0, 0, positive, total
	}
	for _, value := range dayMeans {
		mean += value
	}
	mean /= float64(total)
	if total == 1 {
		return mean, 0, positive, total
	}
	variance := 0.0
	for _, value := range dayMeans {
		variance += (value - mean) * (value - mean)
	}
	variance /= float64(total - 1)
	return mean, math.Sqrt(variance / float64(total)), positive, total
}

func scoreCompetingPathHorizon(books []bboSnapshot, from, to time.Time, horizon time.Duration, distanceBps, feeBps float64) competingPathHorizonReport {
	trainingStep := time.Minute
	observations := competingPathObservationsAtStep(books, from, to, horizon, trainingStep, distanceBps, feeBps)
	scoreAt := eventClockScores(observations, from)
	model := &gammacapture.CompetingPathValueModel{}
	baseline := competingMarginalBaseline{}
	type pendingScore struct {
		id                            uint64
		observation                   competingPathObservation
		competingProb, baselineProb   [4]float64
		competingValue, baselineValue float64
		score                         bool
	}
	pending := make([]pendingScore, 0, len(observations))
	increments := make([]float64, 0, len(observations))
	blockSums, blockCounts := map[string]float64{}, map[string]int{}
	competingBrier, baselineBrier := 0.0, 0.0
	competingLogLoss, baselineLogLoss := 0.0, 0.0
	competingAbs, baselineAbs := 0.0, 0.0
	mature := func(p pendingScore) {
		if !p.score {
			if !model.UpdateLabel(p.observation.maturity, p.id, p.observation.outcome, p.observation.valueBps, trainingStep.Seconds()/horizon.Seconds()) {
				fatalf("competing path label update failed at %s", p.observation.maturity)
			}
			baseline.updateWeighted(p.observation.outcome, trainingStep.Seconds()/horizon.Seconds())
			return
		}
		index := int(p.observation.outcome)
		for category := 0; category < 4; category++ {
			y := 0.0
			if category == index {
				y = 1
			}
			competingBrier += (p.competingProb[category] - y) * (p.competingProb[category] - y)
			baselineBrier += (p.baselineProb[category] - y) * (p.baselineProb[category] - y)
		}
		competingLogLoss -= math.Log(math.Max(1e-12, p.competingProb[index]))
		baselineLogLoss -= math.Log(math.Max(1e-12, p.baselineProb[index]))
		competingError := math.Abs(p.competingValue - p.observation.valueBps)
		baselineError := math.Abs(p.baselineValue - p.observation.valueBps)
		increment := baselineError - competingError
		competingAbs += competingError
		baselineAbs += baselineError
		increments = append(increments, increment)
		block := p.observation.at.UTC().Format("2006-01-02")
		blockSums[block] += increment
		blockCounts[block]++
		if !model.UpdateLabel(p.observation.maturity, p.id, p.observation.outcome, p.observation.valueBps, trainingStep.Seconds()/horizon.Seconds()) {
			fatalf("competing path label update failed at %s", p.observation.maturity)
		}
		baseline.updateWeighted(p.observation.outcome, trainingStep.Seconds()/horizon.Seconds())
	}
	for _, observation := range observations {
		for len(pending) > 0 && !pending[0].observation.maturity.After(observation.at) {
			p := pending[0]
			pending = pending[1:]
			mature(p)
		}
		id, snapshot := model.Predict(observation.at, observation.maturity)
		pending = append(pending, pendingScore{
			id: id, observation: observation,
			competingProb:  snapshot.Probabilities,
			baselineProb:   baseline.probabilities(),
			competingValue: snapshot.ExpectedValueBps,
			baselineValue:  competingExpectedValue(baseline.probabilities(), snapshot.ConditionalMeanBps),
			score:          scoreAt[observation.at],
		})
	}
	for _, p := range pending {
		mature(p)
	}
	n := float64(len(increments))
	report := competingPathHorizonReport{Horizon: horizon.String(), DistanceBps: distanceBps, EligibleSamples: len(increments), EffectiveSamples: n}
	if n <= 0 {
		return report
	}
	mean, se, positiveBlocks, totalBlocks := equalDayMeanSE(blockSums, blockCounts)
	report.CompetingBrier = competingBrier / n
	report.BaselineBrier = baselineBrier / n
	report.CompetingLogLoss = competingLogLoss / n
	report.BaselineLogLoss = baselineLogLoss / n
	report.MeanAbsoluteValueErrorBps = competingAbs / n
	report.BaselineAbsoluteValueErrorBps = baselineAbs / n
	report.IncrementalMeanBps = mean
	report.IncrementalStandardErrorBps = se
	report.EffectiveSamples = float64(totalBlocks)
	// One-sided Bonferroni 5% bound for the two predeclared horizons.
	report.SimultaneousLowerBoundBps = mean - 1.959963984540054*se
	report.PositiveBlocks = positiveBlocks
	report.TotalBlocks = totalBlocks
	return report
}

func runCompetingPathStudy(in competingPathStudyInput) {
	if !in.From.Before(in.To) || in.Symbol == "" {
		fatalf("invalid competing path study input")
	}
	_, _, cfg := loadProductionConfig(in.ConfigPath, in.Symbol)
	books, _, _ := loadExactReplayDataset(in.DataPath, in.Symbol, in.From, in.To, "", in.ReplayCacheDir)
	if len(books) < 2 {
		fatalf("insufficient competing path BBO events: %d", len(books))
	}
	distance := cfg.MinimumHalfSpreadBps
	if distance <= 0 {
		distance = 15
	}
	report := competingPathStudyReport{
		Name: "competing-fast-path-value", Symbol: in.Symbol, From: in.From, To: in.To,
		Causal: true, FeeBpsPerFill: cfg.MakerFeeBps, CandidateTests: 2,
		Results: []competingPathHorizonReport{
			scoreCompetingPathHorizon(books, in.From, in.To, 15*time.Minute, distance, cfg.MakerFeeBps),
			scoreCompetingPathHorizon(books, in.From, in.To, 30*time.Minute, distance, cfg.MakerFeeBps),
		},
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fatalf("encode competing path study: %v", err)
	}
}

func scoreConditionalPayoffHorizon(books []bboSnapshot, from, to time.Time, horizon time.Duration, distanceBps, feeBps float64) conditionalPayoffHorizonReport {
	trainingStep := time.Minute
	observations := competingPathObservationsAtStep(books, from, to, horizon, trainingStep, distanceBps, feeBps)
	scoreAt := eventClockScores(observations, from)
	halfLife := time.Duration(math.Sqrt(horizon.Seconds()*(6*time.Hour).Seconds())) * time.Second
	model := gammacapture.NewConditionalPathPayoffModel(halfLife)
	type pendingPayoff struct {
		id          uint64
		observation competingPathObservation
		baseline    float64
		shrunk      float64
		score       bool
	}
	pending := make([]pendingPayoff, 0, len(observations))
	increments := make([]float64, 0, len(observations))
	blockSums, blockCounts := map[string]float64{}, map[string]int{}
	baselineAbs, shrunkAbs := 0.0, 0.0
	mature := func(p pendingPayoff) {
		if p.score && (p.observation.outcome == gammacapture.CompetingPathBuyOnly ||
			p.observation.outcome == gammacapture.CompetingPathSellOnly) {
			baselineError := math.Abs(p.baseline - p.observation.valueBps)
			shrunkError := math.Abs(p.shrunk - p.observation.valueBps)
			increment := baselineError - shrunkError
			baselineAbs += baselineError
			shrunkAbs += shrunkError
			increments = append(increments, increment)
			block := p.observation.at.UTC().Format("2006-01-02")
			blockSums[block] += increment
			blockCounts[block]++
		}
		if !model.UpdateWeightedLabel(
			p.observation.maturity, p.id, p.observation.outcome,
			p.observation.valueBps, trainingStep.Seconds()/horizon.Seconds()) {
			fatalf("conditional payoff label update failed at %s", p.observation.maturity)
		}
	}
	for _, observation := range observations {
		for len(pending) > 0 && !pending[0].observation.maturity.After(observation.at) {
			p := pending[0]
			pending = pending[1:]
			mature(p)
		}
		id, snapshot := model.Predict(observation.at, observation.maturity)
		score := scoreAt[observation.at]
		pending = append(pending, pendingPayoff{
			id: id, observation: observation,
			baseline: snapshot.BaselineMeanBps[observation.outcome],
			shrunk:   snapshot.ShrunkMeanBps[observation.outcome],
			score:    score,
		})
	}
	for _, p := range pending {
		mature(p)
	}
	n := float64(len(increments))
	report := conditionalPayoffHorizonReport{
		Horizon: horizon.String(), DistanceBps: distanceBps, HalfLife: halfLife.String(),
		EligibleOneSidedSamples: len(increments), EffectiveSamples: n,
	}
	if n <= 0 {
		return report
	}
	mean, se, positiveBlocks, totalBlocks := equalDayMeanSE(blockSums, blockCounts)
	report.BaselineAbsoluteErrorBps = baselineAbs / n
	report.ShrunkAbsoluteErrorBps = shrunkAbs / n
	report.IncrementalMeanBps = mean
	report.IncrementalStandardErrorBps = se
	report.EffectiveSamples = float64(totalBlocks)
	report.SimultaneousLowerBoundBps = mean - 1.959963984540054*se
	report.PositiveBlocks = positiveBlocks
	report.TotalBlocks = totalBlocks
	return report
}

func runConditionalPayoffStudy(in competingPathStudyInput) {
	if !in.From.Before(in.To) || in.Symbol == "" {
		fatalf("invalid conditional payoff study input")
	}
	_, _, cfg := loadProductionConfig(in.ConfigPath, in.Symbol)
	books, _, _ := loadExactReplayDataset(in.DataPath, in.Symbol, in.From, in.To, "", in.ReplayCacheDir)
	if len(books) < 2 {
		fatalf("insufficient conditional payoff BBO events: %d", len(books))
	}
	distance := cfg.MinimumHalfSpreadBps
	if distance <= 0 {
		distance = 15
	}
	report := conditionalPayoffStudyReport{
		Name: "shrunken-one-sided-terminal-payoff", Symbol: in.Symbol,
		From: in.From, To: in.To, Causal: true,
		FeeBpsPerFill: cfg.MakerFeeBps, CandidateTests: 2,
		Results: []conditionalPayoffHorizonReport{
			scoreConditionalPayoffHorizon(books, in.From, in.To, 15*time.Minute, distance, cfg.MakerFeeBps),
			scoreConditionalPayoffHorizon(books, in.From, in.To, 30*time.Minute, distance, cfg.MakerFeeBps),
		},
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fatalf("encode conditional payoff study: %v", err)
	}
}

type sideImbalancePathBaseline struct {
	halfLife                         time.Duration
	lastUpdate                       time.Time
	total, buy, sell, both, valueSum float64
}

func (s *sideImbalancePathBaseline) decay(at time.Time) float64 {
	if s == nil || s.halfLife <= 0 || s.lastUpdate.IsZero() || !at.After(s.lastUpdate) {
		return 1
	}
	return math.Exp(-math.Ln2 * at.Sub(s.lastUpdate).Seconds() / s.halfLife.Seconds())
}

func (s *sideImbalancePathBaseline) snapshot(at time.Time) (float64, [4]float64) {
	decay := s.decay(at)
	total := s.total * decay
	value := 0.0
	if total > 0 {
		value = s.valueSum * decay / total
	}
	denominator := total + 1
	buy := (s.buy*decay + 0.5) / denominator
	sell := (s.sell*decay + 0.5) / denominator
	both := (s.both*decay + 0.5) / denominator
	both = math.Max(math.Max(0, buy+sell-1), math.Min(math.Min(buy, sell), both))
	return value, [4]float64{
		math.Max(0, 1-buy-sell+both),
		math.Max(0, buy-both),
		math.Max(0, sell-both),
		both,
	}
}

func (s *sideImbalancePathBaseline) update(at time.Time, outcome gammacapture.CompetingPathOutcome, value, weight float64) {
	decay := s.decay(at)
	s.total *= decay
	s.buy *= decay
	s.sell *= decay
	s.both *= decay
	s.valueSum *= decay
	s.total += weight
	s.valueSum += weight * value
	switch outcome {
	case gammacapture.CompetingPathBoth:
		s.buy += weight
		s.sell += weight
		s.both += weight
	case gammacapture.CompetingPathBuyOnly:
		s.buy += weight
	case gammacapture.CompetingPathSellOnly:
		s.sell += weight
	}
	s.lastUpdate = at
}

func scoreSideImbalancePayoffHorizon(books []bboSnapshot, from, to time.Time, horizon time.Duration, distanceBps, feeBps float64) sideImbalancePayoffHorizonReport {
	trainingStep := time.Minute
	observations := competingPathObservationsAtStep(books, from, to, horizon, trainingStep, distanceBps, feeBps)
	scoreAt := eventClockScores(observations, from)
	halfLife := time.Duration(math.Sqrt(horizon.Seconds()*(6*time.Hour).Seconds())) * time.Second
	// The path payoff is already fee-net.  A second fee-scaled floor would
	// double count transaction cost, and dividing a per-fill fee by the number
	// of possible decisions in the lookback has no execution interpretation.
	// The component action therefore uses the same null as the terminal-wealth
	// optimizer: positive posterior-risk value relative to no new order.
	identificationFloorBps := 0.0
	model := gammacapture.NewSideImbalancePayoffModel(halfLife)
	baselineModel := sideImbalancePathBaseline{halfLife: halfLife}
	type pendingPayoff struct {
		id                        uint64
		observation               competingPathObservation
		baselineValue, alphaValue float64
		score                     bool
	}
	pending := make([]pendingPayoff, 0, len(observations))
	increments := make([]float64, 0, len(observations))
	blockSums, blockCounts := map[string]float64{}, map[string]int{}
	baselineAbs, conditionalAbs := 0.0, 0.0
	depthReady := 0
	baselineActions, conditionalActions, actionDisagreements := 0, 0, 0
	baselineActionValue, conditionalActionValue := 0.0, 0.0
	componentIncrements := make([]float64, 0)
	mature := func(p pendingPayoff) {
		if p.score {
			baselineError := math.Abs(p.baselineValue - p.observation.valueBps)
			conditionalError := math.Abs(p.alphaValue - p.observation.valueBps)
			increment := baselineError - conditionalError
			baselineAbs += baselineError
			conditionalAbs += conditionalError
			increments = append(increments, increment)
			if p.observation.depthReady {
				depthReady++
			}
			block := p.observation.at.UTC().Format("2006-01-02")
			blockSums[block] += increment
			blockCounts[block]++
			baselineAction := p.baselineValue > identificationFloorBps
			conditionalAction := p.alphaValue > identificationFloorBps
			if baselineAction {
				baselineActions++
				baselineActionValue += p.observation.valueBps
			}
			if conditionalAction {
				conditionalActions++
				conditionalActionValue += p.observation.valueBps
			}
			if baselineAction != conditionalAction {
				actionDisagreements++
				componentIncrement := 0.0
				if conditionalAction {
					componentIncrement += p.observation.valueBps
				}
				if baselineAction {
					componentIncrement -= p.observation.valueBps
				}
				componentIncrements = append(componentIncrements, componentIncrement)
			}
		}
		if !model.UpdateWeightedLabel(
			p.observation.maturity, p.id, p.observation.outcome,
			p.observation.valueBps, trainingStep.Seconds()/horizon.Seconds()) {
			fatalf("side imbalance payoff label update failed at %s", p.observation.maturity)
		}
		baselineModel.update(
			p.observation.maturity, p.observation.outcome,
			p.observation.valueBps, trainingStep.Seconds()/horizon.Seconds())
	}
	for _, observation := range observations {
		for len(pending) > 0 && !pending[0].observation.maturity.After(observation.at) {
			p := pending[0]
			pending = pending[1:]
			mature(p)
		}
		id, snapshot := model.Predict(observation.at, observation.maturity, observation.imbalance)
		_, probabilities := baselineModel.snapshot(observation.at)
		baselineValue := probabilities[gammacapture.CompetingPathBuyOnly]*snapshot.BaselineBuyBps +
			probabilities[gammacapture.CompetingPathSellOnly]*snapshot.BaselineSellBps +
			probabilities[gammacapture.CompetingPathBoth]*observation.bothValueBps
		alphaValue := probabilities[gammacapture.CompetingPathBuyOnly]*snapshot.ConditionalBuyBps +
			probabilities[gammacapture.CompetingPathSellOnly]*snapshot.ConditionalSellBps +
			probabilities[gammacapture.CompetingPathBoth]*observation.bothValueBps
		score := scoreAt[observation.at]
		pending = append(pending, pendingPayoff{
			id: id, observation: observation, score: score,
			baselineValue: baselineValue, alphaValue: alphaValue,
		})
	}
	for _, p := range pending {
		mature(p)
	}
	n := float64(len(increments))
	report := sideImbalancePayoffHorizonReport{
		Horizon: horizon.String(), DistanceBps: distanceBps, HalfLife: halfLife.String(),
		EligibleSamples: len(increments), EffectiveSamples: n,
	}
	if n <= 0 {
		return report
	}
	mean, se, positiveBlocks, totalBlocks := equalDayMeanSE(blockSums, blockCounts)
	report.DepthReadyPct = float64(depthReady) / n * 100
	report.BaselineAbsoluteErrorBps = baselineAbs / n
	report.ConditionalAbsoluteErrorBps = conditionalAbs / n
	report.IncrementalMeanBps = mean
	report.IncrementalStandardErrorBps = se
	report.EffectiveSamples = float64(totalBlocks)
	report.SimultaneousLowerBoundBps = mean - 1.959963984540054*se
	report.PositiveBlocks = positiveBlocks
	report.TotalBlocks = totalBlocks
	report.IdentificationFloorBps = identificationFloorBps
	report.BaselineActions = baselineActions
	report.ConditionalActions = conditionalActions
	report.ActionDisagreements = actionDisagreements
	report.BaselineActionValueBps = baselineActionValue
	report.ConditionalActionValueBps = conditionalActionValue
	report.ComponentIncrementalBps = conditionalActionValue - baselineActionValue
	if actionDisagreements > 0 {
		count := float64(actionDisagreements)
		meanComponent := report.ComponentIncrementalBps / count
		componentVariance := 0.0
		for _, increment := range componentIncrements {
			componentVariance += (increment - meanComponent) * (increment - meanComponent)
		}
		if actionDisagreements > 1 {
			componentVariance /= count - 1
		}
		componentSE := math.Sqrt(componentVariance / count)
		report.ComponentMeanBps = meanComponent
		report.ComponentStandardErrorBps = componentSE
		report.ComponentLowerBoundBps = meanComponent - 1.6448536269514722*componentSE
	}
	return report
}

func runSideImbalancePayoffStudy(in competingPathStudyInput) {
	if !in.From.Before(in.To) || in.Symbol == "" {
		fatalf("invalid side imbalance payoff study input")
	}
	_, _, cfg := loadProductionConfig(in.ConfigPath, in.Symbol)
	books, _, _ := loadExactReplayDataset(in.DataPath, in.Symbol, in.From, in.To, "", in.ReplayCacheDir)
	if len(books) < 2 {
		fatalf("insufficient side imbalance payoff BBO events: %d", len(books))
	}
	distance := cfg.MinimumHalfSpreadBps
	if distance <= 0 {
		distance = 15
	}
	report := sideImbalancePayoffStudyReport{
		Name: "side-imbalance-terminal-payoff", Symbol: in.Symbol,
		From: in.From, To: in.To, Causal: true,
		FeeBpsPerFill: cfg.MakerFeeBps, CandidateTests: 2,
		Results: []sideImbalancePayoffHorizonReport{
			scoreSideImbalancePayoffHorizon(books, in.From, in.To, 15*time.Minute, distance, cfg.MakerFeeBps),
			scoreSideImbalancePayoffHorizon(books, in.From, in.To, 30*time.Minute, distance, cfg.MakerFeeBps),
		},
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fatalf("encode side imbalance payoff study: %v", err)
	}
}
