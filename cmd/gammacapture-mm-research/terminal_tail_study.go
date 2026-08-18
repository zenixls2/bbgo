package main

import (
	"encoding/json"
	"math"
	"os"
	"sort"
	"time"

	"github.com/c9s/bbgo/pkg/strategy/gammacapture"
)

type terminalTailObservation struct {
	at, maturity                    time.Time
	feature, fullReturn, tailReturn float64
}

type terminalTailHorizonReport struct {
	Horizon                         string  `json:"horizon"`
	Tail                            string  `json:"tail"`
	HalfLife                        string  `json:"halfLife"`
	EligibleSamples                 int     `json:"eligibleSamples"`
	EffectiveSamples                float64 `json:"effectiveSamples"`
	FullWindowMAEBps                float64 `json:"fullWindowMAEBps"`
	TailBaselineMAEBps              float64 `json:"tailBaselineMAEBps"`
	ConditionalTailMAEBps           float64 `json:"conditionalTailMAEBps"`
	LabelOnlyIncrementalMeanBps     float64 `json:"labelOnlyIncrementalMeanBps"`
	ConditionalIncrementalMeanBps   float64 `json:"conditionalIncrementalMeanBps"`
	ConditionalStandardErrorBps     float64 `json:"conditionalStandardErrorBps"`
	ConditionalSimultaneousLowerBps float64 `json:"conditionalSimultaneousLowerBps"`
	PositiveBlocks                  int     `json:"positiveBlocks"`
	TotalBlocks                     int     `json:"totalBlocks"`
}

type terminalTailStudyReport struct {
	Name           string                      `json:"name"`
	Symbol         string                      `json:"symbol"`
	From           time.Time                   `json:"from"`
	To             time.Time                   `json:"to"`
	Causal         bool                        `json:"causal"`
	CandidateTests int                         `json:"candidateTests"`
	Results        []terminalTailHorizonReport `json:"results"`
}

type terminalTailEWMean struct {
	halfLife          time.Duration
	last              time.Time
	weight, weightedY float64
}

func (m *terminalTailEWMean) snapshot(at time.Time) float64 {
	if m.weight <= 0 {
		return 0
	}
	return m.weightedY / m.weight
}

func (m *terminalTailEWMean) update(at time.Time, value, weight float64) {
	if weight <= 0 || (!m.last.IsZero() && at.Before(m.last)) {
		return
	}
	decay := 1.0
	if m.halfLife > 0 && !m.last.IsZero() && at.After(m.last) {
		decay = math.Exp(-math.Ln2 * at.Sub(m.last).Seconds() / m.halfLife.Seconds())
	}
	m.weight = m.weight*decay + weight
	m.weightedY = m.weightedY*decay + weight*value
	m.last = at
}

type terminalTailEWStandardizer struct {
	halfLife                time.Duration
	last                    time.Time
	weight, sum, sumSquared float64
}

func (s *terminalTailEWStandardizer) feature(at time.Time, value float64) float64 {
	if s.weight <= 1 {
		return 0
	}
	mean := s.sum / s.weight
	variance := math.Max(0, s.sumSquared/s.weight-mean*mean)
	if variance <= 1e-16 {
		return 0
	}
	return math.Max(-5, math.Min(5, (value-mean)/math.Sqrt(variance)))
}

func (s *terminalTailEWStandardizer) observe(at time.Time, value float64) {
	if (!s.last.IsZero() && !at.After(s.last)) || math.IsNaN(value) || math.IsInf(value, 0) {
		return
	}
	decay := 1.0
	if s.halfLife > 0 && !s.last.IsZero() {
		decay = math.Exp(-math.Ln2 * at.Sub(s.last).Seconds() / s.halfLife.Seconds())
	}
	s.weight = s.weight*decay + 1
	s.sum = s.sum*decay + value
	s.sumSquared = s.sumSquared*decay + value*value
	s.last = at
}

func terminalTailMicroprice(book bboSnapshot) float64 {
	if book.bid <= 0 || book.ask < book.bid {
		return 0
	}
	if book.bidSize > 0 && book.askSize > 0 {
		return (book.ask*book.bidSize + book.bid*book.askSize) / (book.bidSize + book.askSize)
	}
	return (book.bid + book.ask) / 2
}

func terminalTailBookAtOrAfter(books []bboSnapshot, at time.Time) (bboSnapshot, bool) {
	index := sort.Search(len(books), func(index int) bool { return !books[index].time.Before(at) })
	if index >= len(books) || books[index].time.Sub(at) > time.Minute {
		return bboSnapshot{}, false
	}
	return books[index], terminalTailMicroprice(books[index]) > 0
}

func terminalTailObservations(
	books []bboSnapshot, from, to time.Time, horizon time.Duration,
) []terminalTailObservation {
	if horizon <= 0 || len(books) < 2 {
		return nil
	}
	tail := 5 * time.Minute
	if third := horizon / 3; third < tail {
		tail = third
	}
	step := time.Minute
	standardizer := terminalTailEWStandardizer{halfLife: 6 * time.Hour}
	out := make([]terminalTailObservation, 0)
	for anchor := from; anchor.Add(horizon).Before(to) || anchor.Add(horizon).Equal(to); anchor = anchor.Add(step) {
		start, ok := terminalTailBookAtOrAfter(books, anchor)
		if !ok {
			standardizer = terminalTailEWStandardizer{halfLife: 6 * time.Hour}
			continue
		}
		startLog := math.Log(terminalTailMicroprice(start))
		feature := standardizer.feature(start.time, startLog)
		standardizer.observe(start.time, startLog)
		fullSum, tailSum := 0.0, 0.0
		fullCount, tailCount := 0, 0
		valid := true
		for sampleAt := start.time.Add(step); !sampleAt.After(start.time.Add(horizon)); sampleAt = sampleAt.Add(step) {
			book, found := terminalTailBookAtOrAfter(books, sampleAt)
			if !found {
				valid = false
				break
			}
			value := math.Log(terminalTailMicroprice(book))
			fullSum += value
			fullCount++
			if sampleAt.After(start.time.Add(horizon - tail)) {
				tailSum += value
				tailCount++
			}
		}
		if !valid || fullCount == 0 || tailCount == 0 {
			continue
		}
		out = append(out, terminalTailObservation{
			at: start.time, maturity: start.time.Add(horizon), feature: feature,
			fullReturn: (fullSum/float64(fullCount) - startLog) * 10_000,
			tailReturn: (tailSum/float64(tailCount) - startLog) * 10_000,
		})
	}
	return out
}

func scoreTerminalTailHorizon(
	books []bboSnapshot, trainingFrom, scoreFrom, to time.Time, horizon time.Duration, distanceBps float64,
) terminalTailHorizonReport {
	observations := terminalTailObservations(books, trainingFrom, to, horizon)
	lifecycleObservations := competingPathObservationsAtStep(
		books, trainingFrom, to, horizon, time.Minute, distanceBps, 0,
	)
	scoreAt := eventClockScores(lifecycleObservations, scoreFrom)
	halfLife := time.Duration(math.Sqrt(horizon.Seconds()*(6*time.Hour).Seconds())) * time.Second
	model := gammacapture.NewTerminalTailTargetModel(halfLife)
	fullBaseline := terminalTailEWMean{halfLife: halfLife}
	type pending struct {
		id                                                    uint64
		observation                                           terminalTailObservation
		fullPrediction, tailPrediction, conditionalPrediction float64
		score                                                 bool
	}
	pendingLabels := make([]pending, 0, len(observations))
	increments := make([]float64, 0)
	blockSums, blockCounts := map[string]float64{}, map[string]int{}
	fullAbs, tailAbs, conditionalAbs := 0.0, 0.0, 0.0
	mature := func(item pending) {
		if item.score {
			fullError := math.Abs(item.fullPrediction - item.observation.tailReturn)
			tailError := math.Abs(item.tailPrediction - item.observation.tailReturn)
			conditionalError := math.Abs(item.conditionalPrediction - item.observation.tailReturn)
			increment := fullError - conditionalError
			fullAbs += fullError
			tailAbs += tailError
			conditionalAbs += conditionalError
			increments = append(increments, increment)
			block := item.observation.at.UTC().Format("2006-01-02")
			blockSums[block] += increment
			blockCounts[block]++
		}
		weight := time.Minute.Seconds() / horizon.Seconds()
		// The candidate is a correction to the existing full-window target, not
		// an independent replacement for it.  Training the absolute tail return
		// would discard a baseline which can be more accurate than the noisier
		// terminal-local label and would confound label choice with conditioning.
		residual := item.observation.tailReturn - item.fullPrediction
		if !model.UpdateWeightedLabel(item.observation.maturity, item.id, residual, weight) {
			fatalf("terminal-tail label update failed at %s", item.observation.maturity)
		}
		fullBaseline.update(item.observation.maturity, item.observation.fullReturn, weight)
	}
	for _, observation := range observations {
		for len(pendingLabels) > 0 && !pendingLabels[0].observation.maturity.After(observation.at) {
			item := pendingLabels[0]
			pendingLabels = pendingLabels[1:]
			mature(item)
		}
		id, snapshot := model.Predict(observation.at, observation.maturity, observation.feature)
		score := scoreAt[observation.at]
		pendingLabels = append(pendingLabels, pending{
			id: id, observation: observation, score: score,
			fullPrediction:        fullBaseline.snapshot(observation.at),
			tailPrediction:        fullBaseline.snapshot(observation.at) + snapshot.BaselineMeanBps,
			conditionalPrediction: fullBaseline.snapshot(observation.at) + snapshot.ConditionalMeanBps,
		})
	}
	for _, item := range pendingLabels {
		mature(item)
	}
	report := terminalTailHorizonReport{
		Horizon: horizon.String(), Tail: (minDuration(5*time.Minute, horizon/3)).String(),
		HalfLife: halfLife.String(), EligibleSamples: len(increments), EffectiveSamples: float64(len(increments)),
	}
	if len(increments) == 0 {
		return report
	}
	n := float64(len(increments))
	mean, se, positive, totalBlocks := equalDayMeanSE(blockSums, blockCounts)
	report.FullWindowMAEBps = fullAbs / n
	report.TailBaselineMAEBps = tailAbs / n
	report.ConditionalTailMAEBps = conditionalAbs / n
	report.LabelOnlyIncrementalMeanBps = (fullAbs - tailAbs) / n
	report.ConditionalIncrementalMeanBps = mean
	report.ConditionalStandardErrorBps = se
	report.EffectiveSamples = float64(totalBlocks)
	report.ConditionalSimultaneousLowerBps = mean - 1.959963984540054*se
	report.PositiveBlocks, report.TotalBlocks = positive, totalBlocks
	return report
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func runTerminalTailTargetStudy(in competingPathStudyInput) {
	if !in.From.Before(in.To) || in.Symbol == "" {
		fatalf("invalid terminal-tail study input")
	}
	trainingFrom := in.From.Add(-6 * time.Hour)
	_, _, cfg := loadProductionConfig(in.ConfigPath, in.Symbol)
	distance := cfg.MinimumHalfSpreadBps
	if distance <= 0 {
		distance = 15
	}
	books, _, _ := loadExactReplayDataset(in.DataPath, in.Symbol, trainingFrom, in.To, "", in.ReplayCacheDir)
	report := terminalTailStudyReport{
		Name: "terminal-tail-conditional-target", Symbol: in.Symbol,
		From: in.From, To: in.To, Causal: true, CandidateTests: 2,
		Results: []terminalTailHorizonReport{
			scoreTerminalTailHorizon(books, trainingFrom, in.From, in.To, 15*time.Minute, distance),
			scoreTerminalTailHorizon(books, trainingFrom, in.From, in.To, 30*time.Minute, distance),
		},
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fatalf("encode terminal-tail study: %v", err)
	}
}
