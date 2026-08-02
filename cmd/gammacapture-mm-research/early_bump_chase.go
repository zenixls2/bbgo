package main

import (
	"math"
	"sort"
	"time"
)

type earlyBumpChasePolicy struct {
	CadenceSeconds     int     `json:"cadenceSeconds"`
	MinimumAmendBps    float64 `json:"minimumAmendBps"`
	ConfidencePenaltyZ float64 `json:"confidencePenaltyZ"`
	BestBidOracle      bool    `json:"bestBidOracle,omitempty"`
}

type earlyBumpChaseMetrics struct {
	Policy                     earlyBumpChasePolicy `json:"policy"`
	Signals                    int                  `json:"signals"`
	EpisodesAmended            int                  `json:"episodesAmended"`
	Amendments                 int                  `json:"amendments"`
	MeanAmendmentsPerSignal    float64              `json:"meanAmendmentsPerSignal"`
	MeanTotalLiftBps           float64              `json:"meanTotalLiftBps"`
	BaselineFills              int                  `json:"baselineFills"`
	Fills                      int                  `json:"fills"`
	BaselineCaughtEscapes      int                  `json:"baselineCaughtEscapes"`
	CaughtEscapes              int                  `json:"caughtEscapes"`
	FeePositiveTouches         int                  `json:"feePositiveTouches"`
	AdverseFills               int                  `json:"adverseFills"`
	MeanNetBpsPerSignal        float64              `json:"meanNetBpsPerSignal"`
	PairedNetDeltaBpsPerSignal float64              `json:"pairedNetDeltaBpsPerSignal"`
	PairedDailyBootstrap95     [2]float64           `json:"pairedDailyBootstrap95"`
}

type earlyBumpChaseCandidate struct {
	Policy         earlyBumpChasePolicy  `json:"policy"`
	DevelopmentMAE float64               `json:"developmentMAE"`
	DevelopmentR2  float64               `json:"developmentR2"`
	EvaluationMAE  float64               `json:"evaluationMAE"`
	EvaluationR2   float64               `json:"evaluationR2"`
	Development    earlyBumpChaseMetrics `json:"development"`
}

type earlyBumpChaseReport struct {
	ControlDefinition string                    `json:"controlDefinition"`
	SelectionRule     string                    `json:"selectionRule"`
	Candidates        []earlyBumpChaseCandidate `json:"developmentCandidates"`
	Selected          earlyBumpChaseCandidate   `json:"selectedDevelopment"`
	Model             earlyBumpMomentumModel    `json:"selectedModel"`
	DevelopmentBounds []earlyBumpChaseMetrics   `json:"developmentBestBidUpperBounds"`
	EvaluationBounds  []earlyBumpChaseMetrics   `json:"evaluationBestBidUpperBounds"`
	Evaluation        earlyBumpChaseMetrics     `json:"evaluation"`
	Decision          string                    `json:"decision"`
	Limitations       []string                  `json:"limitations"`
}

type earlyBumpChaseResult struct {
	At               time.Time
	Amendments       int
	TotalLiftBps     float64
	BaselineFilled   bool
	BaselineCaught   bool
	BaselineNetBps   float64
	Filled           bool
	FillAt           time.Time
	FillPrice        float64
	Caught           bool
	FeePositiveTouch bool
	Adverse          bool
	NetBps           float64
}

func buildEarlyBumpChaseReport(books []bboSnapshot, trades []tick, events []earlyBumpEvent, in earlyBumpStudyInput) *earlyBumpChaseReport {
	if in.TestFrom.IsZero() {
		return nil
	}
	var developmentEvents, evaluationEvents []earlyBumpEvent
	for _, event := range events {
		if event.At.Before(in.TestFrom) {
			developmentEvents = append(developmentEvents, event)
		} else {
			evaluationEvents = append(evaluationEvents, event)
		}
	}
	if len(developmentEvents) < 80 || len(evaluationEvents) < 40 {
		return &earlyBumpChaseReport{Decision: "insufficient chronological development or evaluation episodes"}
	}

	report := &earlyBumpChaseReport{
		ControlDefinition: "30-second urgency episode; recompute on BBO snapshots but amend only at 5/10/20-second candidate cadences; each amend is monotonic, forecast-sized, and capped at current best bid",
		SelectionRule:     "prefer a policy with at least one incremental caught escape, then maximize paired fee-adjusted value per signal on pre-test development only; if none catches an escape, retain the least-harmful active policy for diagnostic evaluation and fail closed",
		Limitations: []string{
			"public aggressive SELL trade-through is an optimistic private-fill proxy and does not model queue ahead",
			"queue priority lost on each amend is counted but not assigned an invented bps penalty; positive results still require private fill calibration",
			"the ridge forecast is deliberately low-capacity, but cadence and minimum-amend selection still consume development data",
		},
	}

	models := make(map[int]fittedEarlyBumpMomentum)
	developmentSamples := make(map[int][]earlyBumpEvent)
	evaluationSamples := make(map[int][]earlyBumpEvent)
	for _, cadence := range []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second} {
		devSamples := buildEarlyBumpChaseSamples(books, developmentEvents, in, cadence)
		evalSamples := buildEarlyBumpChaseSamples(books, evaluationEvents, in, cadence)
		if len(devSamples) < 80 || len(evalSamples) < 40 {
			continue
		}
		lambda, cvMAE := selectEarlyBumpLambda(devSamples)
		model := fitEarlyBumpMomentum(devSamples, lambda)
		model.lambda, model.cvMAE = lambda, cvMAE
		seconds := int(cadence / time.Second)
		models[seconds] = model
		developmentSamples[seconds] = devSamples
		evaluationSamples[seconds] = evalSamples
		boundPolicy := earlyBumpChasePolicy{CadenceSeconds: seconds, BestBidOracle: true}
		report.DevelopmentBounds = append(report.DevelopmentBounds, summarizeEarlyBumpChase(books, trades, developmentEvents, in, model, boundPolicy))
		report.EvaluationBounds = append(report.EvaluationBounds, summarizeEarlyBumpChase(books, trades, evaluationEvents, in, model, boundPolicy))
		devMAE, devR2 := momentumFitStats(devSamples, model)
		evalMAE, evalR2 := momentumFitStats(evalSamples, model)
		for _, minimumAmend := range []float64{1, 3, 5} {
			for _, z := range []float64{0, 0.5, 1} {
				policy := earlyBumpChasePolicy{CadenceSeconds: seconds, MinimumAmendBps: minimumAmend, ConfidencePenaltyZ: z}
				metrics := summarizeEarlyBumpChase(books, trades, developmentEvents, in, model, policy)
				report.Candidates = append(report.Candidates, earlyBumpChaseCandidate{
					Policy: policy, DevelopmentMAE: devMAE, DevelopmentR2: devR2,
					EvaluationMAE: evalMAE, EvaluationR2: evalR2, Development: metrics,
				})
			}
		}
	}
	if len(report.Candidates) == 0 {
		report.Decision = "insufficient cadence-aligned forecast samples"
		return report
	}

	selected := -1
	for i, candidate := range report.Candidates {
		metrics := candidate.Development
		if metrics.EpisodesAmended == 0 || metrics.CaughtEscapes <= metrics.BaselineCaughtEscapes {
			continue
		}
		if selected < 0 || chaseCandidateBetter(candidate, report.Candidates[selected]) {
			selected = i
		}
	}
	if selected < 0 {
		for i, candidate := range report.Candidates {
			if candidate.Development.EpisodesAmended == 0 {
				continue
			}
			if selected < 0 || chaseCandidateBetter(candidate, report.Candidates[selected]) {
				selected = i
			}
		}
	}
	if selected < 0 {
		report.Decision = "all cadence-aligned policies fail closed without an amendment"
		return report
	}
	report.Selected = report.Candidates[selected]
	policy := report.Selected.Policy
	model := models[policy.CadenceSeconds]
	report.Model = earlyBumpMomentumModel{
		FeatureNames: earlyBumpMomentumFeatureNames, Means: model.means, Scales: model.scales,
		Coefficients: model.coefficients, Lambda: model.lambda, CrossValidatedMAE: model.cvMAE,
		ResidualStdBps: model.residualStd,
	}
	report.Model.DevelopmentMAE, report.Model.DevelopmentR2 = momentumFitStats(developmentSamples[policy.CadenceSeconds], model)
	report.Model.EvaluationMAE, report.Model.EvaluationR2 = momentumFitStats(evaluationSamples[policy.CadenceSeconds], model)
	report.Evaluation = summarizeEarlyBumpChase(books, trades, evaluationEvents, in, model, policy)
	report.Decision = "reject live promotion: cadence-aligned evaluation must catch more escapes than baseline with a strictly positive paired daily-bootstrap lower bound and positive forecast R2"
	if report.Evaluation.CaughtEscapes > report.Evaluation.BaselineCaughtEscapes &&
		report.Evaluation.PairedDailyBootstrap95[0] > 0 && report.Model.EvaluationR2 > 0 {
		report.Decision = "evaluation supports shadow-only lifecycle promotion; private queue fills remain required"
	}
	return report
}

func chaseCandidateBetter(a, b earlyBumpChaseCandidate) bool {
	if a.Development.PairedNetDeltaBpsPerSignal != b.Development.PairedNetDeltaBpsPerSignal {
		return a.Development.PairedNetDeltaBpsPerSignal > b.Development.PairedNetDeltaBpsPerSignal
	}
	if a.Development.CaughtEscapes != b.Development.CaughtEscapes {
		return a.Development.CaughtEscapes > b.Development.CaughtEscapes
	}
	return a.Development.Amendments < b.Development.Amendments
}

func buildEarlyBumpChaseSamples(books []bboSnapshot, events []earlyBumpEvent, in earlyBumpStudyInput, cadence time.Duration) []earlyBumpEvent {
	var samples []earlyBumpEvent
	for _, episode := range events {
		end := episode.At.Add(in.LockDuration)
		for at := episode.At; at.Before(end); at = at.Add(cadence) {
			index := chaseBookIndexAt(books, at)
			if index < 0 || at.Sub(books[index].time) > 2*time.Second {
				continue
			}
			drawdown, rebound := chaseDrawdownRebound(books, index)
			sample := earlyBumpEventFeatures(books, index, in, drawdown, rebound)
			targetAt := at.Add(cadence)
			if targetAt.After(end) {
				targetAt = end
			}
			futureIndex := chaseBookIndexAt(books, targetAt)
			if futureIndex <= index || targetAt.Sub(books[futureIndex].time) > 2*time.Second {
				continue
			}
			sample.FutureReturn30sBps = math.Log(books[futureIndex].bid/books[index].bid) * 10_000
			samples = append(samples, sample)
		}
	}
	return samples
}

func summarizeEarlyBumpChase(books []bboSnapshot, trades []tick, events []earlyBumpEvent, in earlyBumpStudyInput, model fittedEarlyBumpMomentum, policy earlyBumpChasePolicy) earlyBumpChaseMetrics {
	metrics := earlyBumpChaseMetrics{Policy: policy, Signals: len(events)}
	results := make([]earlyBumpChaseResult, 0, len(events))
	for _, event := range events {
		result := simulateEarlyBumpChase(books, trades, event, in, model, policy)
		results = append(results, result)
		if result.Amendments > 0 {
			metrics.EpisodesAmended++
		}
		metrics.Amendments += result.Amendments
		metrics.MeanTotalLiftBps += result.TotalLiftBps
		if result.BaselineFilled {
			metrics.BaselineFills++
		}
		if result.BaselineCaught {
			metrics.BaselineCaughtEscapes++
		}
		if result.Filled {
			metrics.Fills++
			metrics.MeanNetBpsPerSignal += result.NetBps
		}
		if result.Caught {
			metrics.CaughtEscapes++
		}
		if result.FeePositiveTouch {
			metrics.FeePositiveTouches++
		}
		if result.Adverse {
			metrics.AdverseFills++
		}
		metrics.PairedNetDeltaBpsPerSignal += result.NetBps - result.BaselineNetBps
	}
	if metrics.Signals > 0 {
		n := float64(metrics.Signals)
		metrics.MeanAmendmentsPerSignal = float64(metrics.Amendments) / n
		metrics.MeanTotalLiftBps /= n
		metrics.MeanNetBpsPerSignal /= n
		metrics.PairedNetDeltaBpsPerSignal /= n
	}
	metrics.PairedDailyBootstrap95 = bootstrapEarlyBumpChaseDaily(results)
	return metrics
}

func simulateEarlyBumpChase(books []bboSnapshot, trades []tick, event earlyBumpEvent, in earlyBumpStudyInput, model fittedEarlyBumpMomentum, policy earlyBumpChasePolicy) earlyBumpChaseResult {
	result := earlyBumpChaseResult{At: event.At}
	startIndex := chaseBookIndexAt(books, event.At)
	if startIndex < 0 {
		return result
	}
	startBook := books[startIndex]
	mid := (startBook.bid + startBook.ask) / 2
	baseBid := math.Min(startBook.bid, mid*math.Exp(-in.BaseDistanceBps/10_000))
	end := event.At.Add(in.LockDuration)
	costBps := 2*in.MakerFeeBps + 2*in.AdverseSelectionBps + in.MinimumNetEdgeBps
	upAt := chaseUpEscapeAt(books, startIndex, event.At.Add(in.EscapeHorizon), in.EscapeBarrierBps)
	if !event.EscapedUp {
		upAt = time.Time{}
	}
	baselineFillAt, baselineFilled := chaseFirstSellFill(trades, event.At, end, baseBid)
	result.BaselineFilled = baselineFilled
	result.BaselineCaught = baselineFilled && !upAt.IsZero() && !baselineFillAt.After(upAt)
	finalBid := chaseFinalBid(books, event.At.Add(in.MarkoutHorizon), startBook.bid)
	if baselineFilled {
		result.BaselineNetBps = math.Log(finalBid/baseBid)*10_000 - costBps
	}

	restingBid := baseBid
	activeFrom := event.At
	cadence := time.Duration(policy.CadenceSeconds) * time.Second
	for at := event.At; at.Before(end); at = at.Add(cadence) {
		if fillAt, filled := chaseFirstSellFill(trades, activeFrom, at, restingBid); filled {
			result.Filled, result.FillAt, result.FillPrice = true, fillAt, restingBid
			break
		}
		index := chaseBookIndexAt(books, at)
		if index < 0 || at.Sub(books[index].time) > 2*time.Second {
			continue
		}
		drawdown, rebound := chaseDrawdownRebound(books, index)
		observation := earlyBumpEventFeatures(books, index, in, drawdown, rebound)
		proposed := books[index].bid
		if !policy.BestBidOracle {
			forecast := math.Max(0, model.predict(observation)-policy.ConfidencePenaltyZ*model.residualStd)
			proposed = math.Min(books[index].bid, restingBid*math.Exp(forecast/10_000))
		}
		lift := math.Max(0, math.Log(proposed/restingBid)*10_000)
		if lift+1e-9 < policy.MinimumAmendBps {
			continue
		}
		restingBid = proposed
		activeFrom = at
		result.Amendments++
	}
	if !result.Filled {
		if fillAt, filled := chaseFirstSellFill(trades, activeFrom, end, restingBid); filled {
			result.Filled, result.FillAt, result.FillPrice = true, fillAt, restingBid
		}
	}
	result.TotalLiftBps = math.Max(0, math.Log(restingBid/baseBid)*10_000)
	if !result.Filled {
		return result
	}
	result.Caught = !upAt.IsZero() && !result.FillAt.After(upAt)
	result.NetBps = math.Log(finalBid/result.FillPrice)*10_000 - costBps
	targetBid := result.FillPrice * math.Exp(costBps/10_000)
	adverseAsk := result.FillPrice * math.Exp(-in.AdverseBarrierBps/10_000)
	for i := startIndex + 1; i < len(books) && !books[i].time.After(event.At.Add(in.MarkoutHorizon)); i++ {
		if !books[i].time.Before(result.FillAt) && books[i].bid >= targetBid {
			result.FeePositiveTouch = true
		}
		if !books[i].time.Before(result.FillAt) && !books[i].time.After(result.FillAt.Add(in.EscapeHorizon)) && books[i].ask <= adverseAsk {
			result.Adverse = true
		}
	}
	return result
}

func chaseBookIndexAt(books []bboSnapshot, at time.Time) int {
	return sort.Search(len(books), func(i int) bool { return books[i].time.After(at) }) - 1
}

func chaseDrawdownRebound(books []bboSnapshot, index int) (float64, float64) {
	book := books[index]
	mid := (book.bid + book.ask) / 2
	start5m := sort.Search(index+1, func(i int) bool { return !books[i].time.Before(book.time.Add(-5 * time.Minute)) })
	start30s := sort.Search(index+1, func(i int) bool { return !books[i].time.Before(book.time.Add(-30 * time.Second)) })
	high5m, low30s := mid, mid
	for i := start5m; i <= index; i++ {
		value := (books[i].bid + books[i].ask) / 2
		if value > high5m {
			high5m = value
		}
		if i >= start30s && value < low30s {
			low30s = value
		}
	}
	return math.Max(0, math.Log(high5m/mid)*10_000), math.Max(0, math.Log(mid/low30s)*10_000)
}

func chaseFirstSellFill(trades []tick, from, to time.Time, price float64) (time.Time, bool) {
	start := sort.Search(len(trades), func(i int) bool { return !trades[i].time.Before(from) })
	for i := start; i < len(trades) && !trades[i].time.After(to); i++ {
		if trades[i].side == "SELL" && trades[i].price <= price {
			return trades[i].time, true
		}
	}
	return time.Time{}, false
}

func chaseUpEscapeAt(books []bboSnapshot, start int, end time.Time, barrierBps float64) time.Time {
	barrier := books[start].bid * math.Exp(barrierBps/10_000)
	for i := start + 1; i < len(books) && !books[i].time.After(end); i++ {
		if books[i].bid >= barrier {
			return books[i].time
		}
	}
	return time.Time{}
}

func chaseFinalBid(books []bboSnapshot, at time.Time, fallback float64) float64 {
	index := chaseBookIndexAt(books, at)
	if index >= 0 {
		return books[index].bid
	}
	return fallback
}

func bootstrapEarlyBumpChaseDaily(results []earlyBumpChaseResult) [2]float64 {
	type block struct {
		sum float64
		n   int
	}
	byDay := make(map[string]block)
	for _, result := range results {
		key := result.At.UTC().Format("2006-01-02")
		block := byDay[key]
		block.sum += result.NetBps - result.BaselineNetBps
		block.n++
		byDay[key] = block
	}
	if len(byDay) < 2 {
		return [2]float64{}
	}
	blocks := make([]block, 0, len(byDay))
	for _, block := range byDay {
		blocks = append(blocks, block)
	}
	state := uint64(4242)
	values := make([]float64, 5000)
	for sample := range values {
		sum, count := 0.0, 0
		for range blocks {
			state ^= state << 13
			state ^= state >> 7
			state ^= state << 17
			block := blocks[int(state%uint64(len(blocks)))]
			sum += block.sum
			count += block.n
		}
		if count > 0 {
			values[sample] = sum / float64(count)
		}
	}
	sort.Float64s(values)
	return [2]float64{values[int(.025*float64(len(values)-1))], values[int(.975*float64(len(values)-1))]}
}
