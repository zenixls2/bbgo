package main

import (
	"encoding/json"
	"math"
	"os"
	"sort"
	"time"

	"github.com/c9s/bbgo/pkg/strategy/gammacapture"
)

type pivotRegimeStudyInput struct {
	DataPath       string
	Symbol         string
	From           time.Time
	TrainTo        time.Time
	ValidationTo   time.Time
	To             time.Time
	SampleInterval time.Duration
	Horizon        time.Duration
	CostBps        float64
	RiskPenaltyBps float64
	MaxGap         time.Duration
}

type pivotRegimeStudyReport struct {
	Mode           string                 `json:"mode"`
	Symbol         string                 `json:"symbol"`
	From           time.Time              `json:"from"`
	To             time.Time              `json:"to"`
	LoadedFrom     time.Time              `json:"loadedFrom"`
	LoadedTo       time.Time              `json:"loadedTo"`
	SampleInterval string                 `json:"sampleInterval"`
	Horizon        string                 `json:"horizon"`
	CostBps        float64                `json:"costBps"`
	RiskPenaltyBps float64                `json:"riskPenaltyBps"`
	BBOEvents      int                    `json:"bboEvents"`
	SampledAnchors int                    `json:"sampledAnchors"`
	Candidates     []float64              `json:"reversalCandidatesBps"`
	Train          pivotRegimeSplitReport `json:"train"`
	Validation     pivotRegimeSplitReport `json:"validation"`
	Holdout        pivotRegimeSplitReport `json:"holdout"`
	Warnings       []string               `json:"warnings"`
}

type pivotRegimeSplitReport struct {
	From     time.Time                  `json:"from"`
	To       time.Time                  `json:"to"`
	Anchors  int                        `json:"anchors"`
	Variants []pivotRegimeVariantReport `json:"variants"`
	Raw35    pivotRegimeRawReport       `json:"raw35"`
}

type pivotRegimeVariantReport struct {
	ReversalBps              float64 `json:"reversalBps"`
	PivotConfirmations       int     `json:"pivotConfirmations"`
	ReadyAnchors             int     `json:"readyAnchors"`
	ActiveAnchors            int     `json:"activeAnchors"`
	Transitions              int     `json:"transitions"`
	MeanSignedQuantityScale  float64 `json:"meanSignedQuantityScale"`
	MeanQuantityScale        float64 `json:"meanQuantityScale"`
	MeanRemainingBps         float64 `json:"meanRemainingBps"`
	MeanActionValueBps       float64 `json:"meanActionValueBps"`
	ActiveMeanActionValueBps float64 `json:"activeMeanActionValueBps"`
	PositiveBlocks           int     `json:"positiveBlocks"`
	TotalBlocks              int     `json:"totalBlocks"`
}

type pivotRegimeRawReport struct {
	Signals                  int     `json:"signals"`
	MeanActionValueBps       float64 `json:"meanActionValueBps"`
	ActiveMeanActionValueBps float64 `json:"activeMeanActionValueBps"`
	Transitions              int     `json:"transitions"`
}

type pivotRegimeAnchor struct {
	at       time.Time
	bid, ask float64
	rawTag   float64
	decision gammacapture.PivotRegimeDecision
}

func runPivotRegimeStudy(input pivotRegimeStudyInput) {
	if input.From.IsZero() || !input.From.Before(input.To) || input.SampleInterval <= 0 || input.Horizon <= 0 || input.CostBps < 0 || input.RiskPenaltyBps < 0 {
		fatalf("invalid pivot-regime study configuration")
	}
	if input.MaxGap <= 0 {
		input.MaxGap = 15 * time.Minute
	}
	loadedFrom := input.From.Add(-24 * time.Hour)
	loadedTo := input.To.Add(input.Horizon + 2*input.SampleInterval)
	rawBooks := readBBO(input.DataPath, input.Symbol, loadedFrom, loadedTo)
	rawBooks = compactBBO(rawBooks)
	if len(rawBooks) < 10 {
		fatalf("insufficient BBO events for pivot-regime study: %d", len(rawBooks))
	}
	sampledBooks := compactBBOAtInterval(append([]bboSnapshot(nil), rawBooks...), input.SampleInterval)
	candidates := []float64{20, 26, 35, 50, 75}
	decisions := make([]map[time.Time]gammacapture.PivotRegimeDecision, len(candidates))
	pivotEvents := make([]map[time.Time]int, len(candidates))
	filters := make([]*gammacapture.PivotRegimeFilter, len(candidates))
	for i, reversal := range candidates {
		filters[i] = gammacapture.NewPivotRegimeFilter(gammacapture.PivotRegimeConfig{
			ReversalBps: reversal, MaxGap: input.MaxGap, MinLegSamples: 2, PriorLegSamples: 2,
		})
		decisions[i] = make(map[time.Time]gammacapture.PivotRegimeDecision)
		pivotEvents[i] = make(map[time.Time]int)
	}
	for _, book := range rawBooks {
		bucket := book.time.Truncate(input.SampleInterval)
		for i, filter := range filters {
			decision := filter.Observe(gammacapture.PivotRegimeInput{At: book.time, ReferencePrice: book.midPrice()})
			decisions[i][bucket] = decision
			if decision.PivotChanged {
				pivotEvents[i][bucket]++
			}
		}
	}
	anchors := make([][]pivotRegimeAnchor, len(candidates))
	for sampleIndex, book := range sampledBooks {
		if book.time.Before(input.From) || !book.time.Before(input.To) {
			continue
		}
		index := sort.Search(len(rawBooks), func(i int) bool { return !rawBooks[i].time.Before(book.time) })
		if index >= len(rawBooks) {
			continue
		}
		rawTag, ok := causalRegimeRawTag(sampledBooks, sampleIndex, book.time, 30*time.Minute, 30*time.Minute)
		if !ok {
			continue
		}
		bucket := book.time.Truncate(input.SampleInterval)
		for i := range candidates {
			decision, exists := decisions[i][bucket]
			if !exists {
				continue
			}
			decision.PivotChanged = pivotEvents[i][bucket] > 0
			anchors[i] = append(anchors[i], pivotRegimeAnchor{at: book.time, bid: book.bid, ask: book.ask, rawTag: rawTag, decision: decision})
		}
	}
	trainTo := input.TrainTo
	validationTo := input.ValidationTo
	if trainTo.IsZero() || validationTo.IsZero() {
		trainTo = input.From.Add((input.To.Sub(input.From)) / 2)
		validationTo = input.From.Add(3 * (input.To.Sub(input.From)) / 4)
	}
	if !input.From.Before(trainTo) || !trainTo.Before(validationTo) || !validationTo.Before(input.To) {
		fatalf("pivot-regime study requires from < train-to < validation-to < to")
	}
	report := pivotRegimeStudyReport{
		Mode: "standalone-causal-pivot-regime-study", Symbol: input.Symbol,
		From: input.From, To: input.To, LoadedFrom: loadedFrom, LoadedTo: loadedTo,
		SampleInterval: input.SampleInterval.String(), Horizon: input.Horizon.String(),
		CostBps: input.CostBps, RiskPenaltyBps: input.RiskPenaltyBps,
		BBOEvents: len(rawBooks), Candidates: candidates,
		Warnings: []string{
			"Pivot state is updated on every compacted BBO event; sampleInterval is used only for evaluation anchors and does not define regime state.",
			"The target scale uses empirical same-direction completed-leg amplitude and is not a fixed raw-score threshold.",
		},
	}
	for _, values := range anchors {
		report.SampledAnchors = maxInt(report.SampledAnchors, len(values))
	}
	report.Train = summarizePivotRegimeSplit(anchors, candidates, input, input.From, trainTo, rawBooks)
	report.Validation = summarizePivotRegimeSplit(anchors, candidates, input, trainTo, validationTo, rawBooks)
	report.Holdout = summarizePivotRegimeSplit(anchors, candidates, input, validationTo, input.To, rawBooks)
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fatalf("encode pivot-regime study: %v", err)
	}
}

func causalRegimeRawTag(books []bboSnapshot, index int, at time.Time, slowLookback, volatilityWindow time.Duration) (float64, bool) {
	if index < 0 || index >= len(books) || !books[index].time.Equal(at) {
		index = sort.Search(len(books), func(i int) bool { return !books[i].time.Before(at) })
	}
	if index >= len(books) {
		return 0, false
	}
	slowIndex, okSlow := indexAtOrBefore(books, index, at.Add(-slowLookback))
	volIndex, okVol := indexAtOrBefore(books, index, at.Add(-volatilityWindow))
	fastIndex, okFast := indexAtOrBefore(books, index, at.Add(-5*time.Minute))
	if !okSlow || !okVol || !okFast || books[index].midPrice() <= 0 || books[slowIndex].midPrice() <= 0 || books[fastIndex].midPrice() <= 0 {
		return 0, false
	}
	volatility := rollingRegimeVolatility(books, volIndex, index)
	slowReturn := math.Log(books[index].midPrice() / books[slowIndex].midPrice())
	fastReturn := math.Log(books[index].midPrice() / books[fastIndex].midPrice())
	slowScale := math.Max(1e-6, volatility*math.Sqrt(float64(maxInt(1, index-slowIndex))))
	fastScale := math.Max(1e-6, volatility)
	return 0.5 * (math.Tanh(slowReturn/slowScale) + math.Tanh(fastReturn/fastScale)), true
}

func summarizePivotRegimeSplit(anchors [][]pivotRegimeAnchor, candidates []float64, input pivotRegimeStudyInput, from, to time.Time, books []bboSnapshot) pivotRegimeSplitReport {
	report := pivotRegimeSplitReport{From: from, To: to}
	if len(anchors) > 0 {
		for _, anchor := range anchors[0] {
			if !anchor.at.Before(from) && anchor.at.Before(to) {
				report.Anchors++
			}
		}
	}
	report.Variants = make([]pivotRegimeVariantReport, 0, len(candidates))
	for i, reversal := range candidates {
		report.Variants = append(report.Variants, summarizePivotRegimeVariant(anchors[i], reversal, input, from, to, books))
	}
	// Raw tag is the old comparison arm, deliberately reported separately from
	// the pivot source-of-truth variants.
	if len(anchors) > 0 {
		report.Raw35 = summarizePivotRegimeRaw(anchors[0], input, from, to, books)
	}
	return report
}

func summarizePivotRegimeVariant(values []pivotRegimeAnchor, reversal float64, input pivotRegimeStudyInput, from, to time.Time, books []bboSnapshot) pivotRegimeVariantReport {
	report := pivotRegimeVariantReport{ReversalBps: reversal}
	previousDirection := 0
	blockSums := make(map[int]float64)
	blockCounts := make(map[int]int)
	for _, anchor := range values {
		if anchor.at.Before(from) || !anchor.at.Before(to) {
			continue
		}
		if anchor.decision.PivotChanged {
			report.PivotConfirmations++
		}
		if anchor.decision.Direction != previousDirection {
			if previousDirection != 0 {
				report.Transitions++
			}
			previousDirection = anchor.decision.Direction
		}
		if !anchor.decision.Ready {
			continue
		}
		report.ReadyAnchors++
		sizing := gammacapture.EvaluatePivotRegimeSizing(gammacapture.PivotRegimeSizingInput{
			Decision: anchor.decision, CostBps: input.CostBps,
			RiskPenaltyBps: input.RiskPenaltyBps, MaxTargetShift: 0.2,
		})
		if !sizing.Applied || sizing.QuantityScale <= 0 {
			continue
		}
		report.ActiveAnchors++
		report.MeanQuantityScale += sizing.QuantityScale
		report.MeanSignedQuantityScale += sizing.SignedQuantityScale
		report.MeanRemainingBps += anchor.decision.RemainingAmplitudeBps
		future, ok := pivotRegimeFutureBook(books, anchor.at, input.Horizon, input.SampleInterval)
		if !ok {
			continue
		}
		value := pivotRegimeActionValue(anchor.bid, anchor.ask, future.bid, future.ask, sizing.Direction, input.CostBps)
		weighted := sizing.QuantityScale * value
		report.MeanActionValueBps += weighted
		report.ActiveMeanActionValueBps += value
		block := int(anchor.at.Sub(from) / (6 * time.Hour))
		blockSums[block] += weighted
		blockCounts[block]++
	}
	if report.ActiveAnchors > 0 {
		report.MeanQuantityScale /= float64(report.ActiveAnchors)
		report.MeanSignedQuantityScale /= float64(report.ActiveAnchors)
		report.MeanRemainingBps /= float64(report.ActiveAnchors)
		report.ActiveMeanActionValueBps /= float64(report.ActiveAnchors)
	}
	if report.AnchorsForValue(values, from, to) > 0 {
		report.MeanActionValueBps /= float64(report.AnchorsForValue(values, from, to))
	}
	for block, sum := range blockSums {
		if blockCounts[block] == 0 {
			continue
		}
		report.TotalBlocks++
		if sum > 0 {
			report.PositiveBlocks++
		}
	}
	return report
}

func (r pivotRegimeVariantReport) AnchorsForValue(values []pivotRegimeAnchor, from, to time.Time) int {
	count := 0
	for _, value := range values {
		if !value.at.Before(from) && value.at.Before(to) {
			count++
		}
	}
	return count
}

func summarizePivotRegimeRaw(values []pivotRegimeAnchor, input pivotRegimeStudyInput, from, to time.Time, books []bboSnapshot) pivotRegimeRawReport {
	report := pivotRegimeRawReport{}
	previousState := 0
	for _, anchor := range values {
		if anchor.at.Before(from) || !anchor.at.Before(to) {
			continue
		}
		state := thresholdRegimeState(anchor.rawTag, 0.35)
		if state != previousState {
			if previousState != 0 {
				report.Transitions++
			}
			previousState = state
		}
		if state == 0 {
			continue
		}
		report.Signals++
		future, ok := pivotRegimeFutureBook(books, anchor.at, input.Horizon, input.SampleInterval)
		if !ok {
			continue
		}
		value := pivotRegimeActionValue(anchor.bid, anchor.ask, future.bid, future.ask, state, input.CostBps)
		report.MeanActionValueBps += value
		report.ActiveMeanActionValueBps += value
	}
	if report.Signals > 0 {
		report.MeanActionValueBps /= float64(report.Signals)
		report.ActiveMeanActionValueBps /= float64(report.Signals)
	}
	return report
}

func pivotRegimeFutureBook(books []bboSnapshot, at time.Time, horizon, interval time.Duration) (bboSnapshot, bool) {
	maturity := at.Add(horizon)
	index := sort.Search(len(books), func(i int) bool { return !books[i].time.Before(maturity) })
	if index >= len(books) || books[index].time.Sub(maturity) > 2*interval {
		return bboSnapshot{}, false
	}
	return books[index], books[index].bid > 0 && books[index].ask > books[index].bid
}

func pivotRegimeActionValue(startBid, startAsk, futureBid, futureAsk float64, direction int, costBps float64) float64 {
	if direction > 0 {
		return math.Log(futureBid/startAsk)*10_000 - costBps
	}
	if direction < 0 {
		return -math.Log(futureAsk/startBid)*10_000 - costBps
	}
	return 0
}
