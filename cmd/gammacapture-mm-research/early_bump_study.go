package main

import (
	"encoding/json"
	"math"
	"math/rand"
	"os"
	"sort"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

type earlyBumpStudyInput struct {
	DataPath, Symbol                                string
	From, To, TestFrom                              time.Time
	BaseDistanceBps, MakerFeeBps                    float64
	AdverseSelectionBps, MinimumNetEdgeBps          float64
	Drawdown5mBps, Rebound30sBps, ExitRebound30sBps float64
	LockDuration, Cooldown                          time.Duration
	EscapeBarrierBps, AdverseBarrierBps             float64
	EscapeHorizon, MarkoutHorizon                   time.Duration
	DeltasBps                                       []float64
}

type earlyBumpOutcome struct {
	DeltaBps              float64   `json:"deltaBps"`
	BidPrice              float64   `json:"bidPrice"`
	Filled                bool      `json:"filled"`
	FillAt                time.Time `json:"fillAt,omitempty"`
	CaughtEscape          bool      `json:"caughtEscape"`
	FeePositiveTouch      bool      `json:"feePositiveTouch"`
	AdverseAfterFill      bool      `json:"adverseAfterFill"`
	TerminalNetMarkoutBps float64   `json:"terminalNetMarkoutBps"`
	MaximumNetEdgeBps     float64   `json:"maximumNetEdgeBps"`
}

type earlyBumpEvent struct {
	At                 time.Time          `json:"at"`
	Drawdown5mBps      float64            `json:"drawdown5mBps"`
	Rebound30sBps      float64            `json:"rebound30sBps"`
	Return5sBps        float64            `json:"return5sBps"`
	Return15sBps       float64            `json:"return15sBps"`
	Return30sBps       float64            `json:"return30sBps"`
	OFI30s             float64            `json:"ofi30s"`
	Microprice         float64            `json:"micropriceDisplacement"`
	SpreadBps          float64            `json:"spreadBps"`
	InsideDeltaBps     float64            `json:"insideDeltaBps"`
	FutureReturn30sBps float64            `json:"futureReturn30sBps"`
	EscapedUp          bool               `json:"escapedUp"`
	Outcomes           []earlyBumpOutcome `json:"outcomes"`
}

type earlyBumpDeltaMetrics struct {
	DeltaBps                       float64    `json:"deltaBps"`
	Signals                        int        `json:"signals"`
	UpEscapeEvents                 int        `json:"upEscapeEvents"`
	Fills                          int        `json:"fills"`
	FillRate                       float64    `json:"fillRate"`
	FillRateWilson95               [2]float64 `json:"fillRateWilson95"`
	CaughtEscapes                  int        `json:"caughtEscapes"`
	CatchRateGivenEscape           float64    `json:"catchRateGivenEscape"`
	CatchRateWilson95              [2]float64 `json:"catchRateWilson95"`
	IncrementalFillsVsBaseline     int        `json:"incrementalFillsVsBaseline"`
	IncrementalCaughtVsBaseline    int        `json:"incrementalCaughtVsBaseline"`
	FeePositiveTouches             int        `json:"feePositiveTouches"`
	FeePositiveRateGivenFill       float64    `json:"feePositiveRateGivenFill"`
	AdverseFills                   int        `json:"adverseFills"`
	AdverseRateGivenFill           float64    `json:"adverseRateGivenFill"`
	MeanTerminalNetBpsPerFill      float64    `json:"meanTerminalNetBpsPerFill"`
	MeanTerminalNetBpsPerSignal    float64    `json:"meanTerminalNetBpsPerSignal"`
	PairedNetDeltaBpsPerSignal     float64    `json:"pairedNetDeltaBpsPerSignal"`
	PairedNetDeltaDailyBootstrap95 [2]float64 `json:"pairedNetDeltaDailyBootstrap95"`
	MeanMaximumNetEdgeBpsPerFill   float64    `json:"meanMaximumNetEdgeBpsPerFill"`
}

type earlyBumpPeriodReport struct {
	Name    string                  `json:"name"`
	From    time.Time               `json:"from,omitempty"`
	To      time.Time               `json:"to,omitempty"`
	Signals int                     `json:"signals"`
	Deltas  []earlyBumpDeltaMetrics `json:"deltas"`
}

type earlyBumpStudyReport struct {
	Symbol               string                   `json:"symbol"`
	From                 time.Time                `json:"from"`
	To                   time.Time                `json:"to"`
	TestFrom             time.Time                `json:"testFrom,omitempty"`
	BBOEvents            int                      `json:"bboEvents"`
	TradeEvents          int                      `json:"tradeEvents"`
	SignalDefinition     string                   `json:"signalDefinition"`
	FillModel            string                   `json:"fillModel"`
	EscapeDefinition     string                   `json:"escapeDefinition"`
	NetMarkoutDefinition string                   `json:"netMarkoutDefinition"`
	All                  earlyBumpPeriodReport    `json:"all"`
	Development          *earlyBumpPeriodReport   `json:"development,omitempty"`
	Evaluation           *earlyBumpPeriodReport   `json:"evaluation,omitempty"`
	Adaptive             *earlyBumpAdaptiveReport `json:"adaptive,omitempty"`
	Chase                *earlyBumpChaseReport    `json:"eventDrivenChase,omitempty"`
	Limitations          []string                 `json:"limitations"`
}

func runEarlyBumpStudy(in earlyBumpStudyInput) {
	books := compactBBOSeconds(readBBO(in.DataPath, in.Symbol, in.From, in.To))
	trades := compactTrades(readLiveTrades(in.DataPath, in.Symbol, in.From, in.To))
	if len(books) < 2 || len(trades) == 0 {
		fatalf("early-bump study requires overlapping BBO and trade data")
	}
	events := buildEarlyBumpEvents(books, trades, in)
	report := earlyBumpStudyReport{
		Symbol: in.Symbol, From: books[0].time, To: books[len(books)-1].time,
		TestFrom: in.TestFrom, BBOEvents: len(books), TradeEvents: len(trades),
		SignalDefinition:     "first armed BBO with trailing-5m drawdown >= threshold and trailing-30s rebound >= threshold; rearm after new low or rebound below exit threshold",
		FillModel:            "first public aggressive SELL trade at or below the passive limit during the 30-second lock; no private queue-priority claim",
		EscapeDefinition:     "+10-bps best-bid barrier before -10-bps best-ask barrier within two minutes",
		NetMarkoutDefinition: "10-minute terminal best-bid log return from limit price minus two maker fees, two adverse-selection allowances, and minimum net edge",
		Limitations: []string{
			"public trade-through is not proof of private fill and ignores queue ahead",
			"the fixed drawdown/rebound thresholds were selected from overlapping July data, so the chronological section is post-selection evaluation rather than an untouched test",
			"each hypothetical fill is evaluated independently; inventory, order size, capital constraints, and interaction with asks are not portfolio-replayed",
			"maximum fee-positive touch is an optimistic executable-price opportunity, while terminal markout is the conservative primary value metric",
		},
	}
	report.All = summarizeEarlyBumpPeriod("all", events, in, func(earlyBumpEvent) bool { return true })
	report.Adaptive = buildEarlyBumpAdaptiveReport(events, in)
	report.Chase = buildEarlyBumpChaseReport(books, trades, events, in)
	if !in.TestFrom.IsZero() {
		dev := summarizeEarlyBumpPeriod("development", events, in, func(e earlyBumpEvent) bool { return e.At.Before(in.TestFrom) })
		eval := summarizeEarlyBumpPeriod("evaluation", events, in, func(e earlyBumpEvent) bool { return !e.At.Before(in.TestFrom) })
		report.Development, report.Evaluation = &dev, &eval
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fatalf("encode early-bump report: %v", err)
	}
}

func compactBBOSeconds(values []bboSnapshot) []bboSnapshot {
	if len(values) == 0 {
		return nil
	}
	sort.SliceStable(values, func(i, j int) bool { return values[i].time.Before(values[j].time) })
	out := make([]bboSnapshot, 0, len(values)/2)
	for _, value := range values {
		last := len(out) - 1
		if last >= 0 && out[last].time.Unix() == value.time.Unix() {
			out[last] = value
		} else {
			out = append(out, value)
		}
	}
	return out
}

func buildEarlyBumpEvents(books []bboSnapshot, trades []tick, in earlyBumpStudyInput) []earlyBumpEvent {
	const maxGap = 5 * time.Minute
	var events []earlyBumpEvent
	for segmentStart := 0; segmentStart < len(books); {
		segmentEnd := segmentStart + 1
		for segmentEnd < len(books) && books[segmentEnd].time.Sub(books[segmentEnd-1].time) <= maxGap {
			segmentEnd++
		}
		segment := books[segmentStart:segmentEnd]
		armed := true
		cooldownUntil := time.Time{}
		episodeLow := 0.0
		for i, book := range segment {
			if i == 0 || book.time.Sub(segment[0].time) < 5*time.Minute ||
				book.time.Add(in.MarkoutHorizon).After(segment[len(segment)-1].time) {
				continue
			}
			mid := (book.bid + book.ask) / 2
			start5m := sort.Search(i+1, func(j int) bool { return !segment[j].time.Before(book.time.Add(-5 * time.Minute)) })
			start30s := sort.Search(i+1, func(j int) bool { return !segment[j].time.Before(book.time.Add(-30 * time.Second)) })
			high5m, low30s := mid, mid
			for j := start5m; j <= i; j++ {
				value := (segment[j].bid + segment[j].ask) / 2
				if value > high5m {
					high5m = value
				}
				if j >= start30s && value < low30s {
					low30s = value
				}
			}
			drawdown := math.Max(0, math.Log(high5m/mid)*10_000)
			rebound := math.Max(0, math.Log(mid/low30s)*10_000)
			if !armed {
				if (episodeLow > 0 && mid < episodeLow) || rebound < in.ExitRebound30sBps {
					armed = true
				}
				continue
			}
			if book.time.Before(cooldownUntil) || drawdown < in.Drawdown5mBps || rebound < in.Rebound30sBps {
				continue
			}
			event := evaluateEarlyBumpEvent(segment, i, trades, in, drawdown, rebound)
			events = append(events, event)
			armed = false
			episodeLow = low30s
			cooldownUntil = book.time.Add(in.LockDuration + in.Cooldown)
		}
		segmentStart = segmentEnd
	}
	return events
}

func earlyBumpEventFeatures(segment []bboSnapshot, index int, in earlyBumpStudyInput, drawdown, rebound float64) earlyBumpEvent {
	book := segment[index]
	mid := (book.bid + book.ask) / 2
	event := earlyBumpEvent{
		At: book.time, Drawdown5mBps: drawdown, Rebound30sBps: rebound,
		SpreadBps: math.Log(book.ask/book.bid) * 10_000,
	}
	returnAt := func(duration time.Duration) float64 {
		target := book.time.Add(-duration)
		j := sort.Search(index+1, func(j int) bool { return segment[j].time.After(target) }) - 1
		if j < 0 {
			return 0
		}
		pastMid := (segment[j].bid + segment[j].ask) / 2
		if pastMid <= 0 {
			return 0
		}
		return math.Log(mid/pastMid) * 10_000
	}
	event.Return5sBps = returnAt(5 * time.Second)
	event.Return15sBps = returnAt(15 * time.Second)
	event.Return30sBps = returnAt(30 * time.Second)

	start30s := sort.Search(index+1, func(j int) bool {
		return !segment[j].time.Before(book.time.Add(-30 * time.Second))
	})
	if start30s > 0 {
		start30s--
	}
	ofi, depth := 0.0, 0.0
	for j := start30s + 1; j <= index; j++ {
		current, previous := segment[j], segment[j-1]
		value := 0.0
		if current.bid >= previous.bid {
			value += current.bidSize
		}
		if current.bid <= previous.bid {
			value -= previous.bidSize
		}
		if current.ask <= previous.ask {
			value -= current.askSize
		}
		if current.ask >= previous.ask {
			value += previous.askSize
		}
		ofi += value
		depth += current.bidSize + current.askSize + previous.bidSize + previous.askSize
	}
	if depth > 0 {
		event.OFI30s = math.Max(-1, math.Min(1, 2*ofi/depth))
	}
	bookDepth := book.bidSize + book.askSize
	if bookDepth > 0 && book.ask > book.bid {
		microprice := (book.ask*book.bidSize + book.bid*book.askSize) / bookDepth
		event.Microprice = math.Max(-1, math.Min(1, (microprice-mid)/((book.ask-book.bid)/2)))
	}
	baseBid := math.Min(book.bid, mid*math.Exp(-in.BaseDistanceBps/10_000))
	event.InsideDeltaBps = math.Max(0, math.Log(book.bid/baseBid)*10_000)
	futureIndex := sort.Search(len(segment), func(j int) bool {
		return segment[j].time.After(book.time.Add(30 * time.Second))
	}) - 1
	if futureIndex > index {
		event.FutureReturn30sBps = math.Log(segment[futureIndex].bid/book.bid) * 10_000
	}
	return event
}

func evaluateEarlyBumpEvent(segment []bboSnapshot, index int, trades []tick, in earlyBumpStudyInput, drawdown, rebound float64) earlyBumpEvent {
	book := segment[index]
	mid := (book.bid + book.ask) / 2
	event := earlyBumpEventFeatures(segment, index, in, drawdown, rebound)
	upBarrier := book.bid * math.Exp(in.EscapeBarrierBps/10_000)
	downBarrier := book.ask * math.Exp(-in.EscapeBarrierBps/10_000)
	escapeEnd := book.time.Add(in.EscapeHorizon)
	markoutEnd := book.time.Add(in.MarkoutHorizon)
	upAt, downAt := time.Time{}, time.Time{}
	finalBid := book.bid
	for j := index + 1; j < len(segment) && !segment[j].time.After(markoutEnd); j++ {
		next := segment[j]
		finalBid = next.bid
		if next.time.After(escapeEnd) {
			continue
		}
		if upAt.IsZero() && next.bid >= upBarrier {
			upAt = next.time
		}
		if downAt.IsZero() && next.ask <= downBarrier {
			downAt = next.time
		}
	}
	event.EscapedUp = !upAt.IsZero() && (downAt.IsZero() || upAt.Before(downAt))
	costBps := 2*in.MakerFeeBps + 2*in.AdverseSelectionBps + in.MinimumNetEdgeBps
	tradeStart := sort.Search(len(trades), func(i int) bool { return !trades[i].time.Before(book.time) })
	tradeEnd := sort.Search(len(trades), func(i int) bool { return trades[i].time.After(book.time.Add(in.LockDuration)) })
	for _, delta := range in.DeltasBps {
		baseBid := math.Min(book.bid, mid*math.Exp(-in.BaseDistanceBps/10_000))
		price := math.Min(book.bid, baseBid*math.Exp(delta/10_000))
		outcome := earlyBumpOutcome{DeltaBps: delta, BidPrice: price}
		for j := tradeStart; j < tradeEnd; j++ {
			if trades[j].side == types.SideTypeSell && trades[j].price <= price {
				outcome.Filled = true
				outcome.FillAt = trades[j].time
				break
			}
		}
		if outcome.Filled {
			outcome.CaughtEscape = event.EscapedUp && !outcome.FillAt.After(upAt)
			targetBid := price * math.Exp(costBps/10_000)
			adverseAsk := price * math.Exp(-in.AdverseBarrierBps/10_000)
			maxBid := price
			for j := index + 1; j < len(segment) && !segment[j].time.After(markoutEnd); j++ {
				next := segment[j]
				if next.bid > maxBid {
					maxBid = next.bid
				}
				if !outcome.FeePositiveTouch && !next.time.Before(outcome.FillAt) && next.bid >= targetBid {
					outcome.FeePositiveTouch = true
				}
				if !outcome.AdverseAfterFill && !next.time.Before(outcome.FillAt) &&
					!next.time.After(outcome.FillAt.Add(in.EscapeHorizon)) && next.ask <= adverseAsk {
					outcome.AdverseAfterFill = true
				}
			}
			outcome.TerminalNetMarkoutBps = math.Log(finalBid/price)*10_000 - costBps
			outcome.MaximumNetEdgeBps = math.Log(maxBid/price)*10_000 - costBps
		}
		event.Outcomes = append(event.Outcomes, outcome)
	}
	return event
}

func summarizeEarlyBumpPeriod(name string, events []earlyBumpEvent, in earlyBumpStudyInput, include func(earlyBumpEvent) bool) earlyBumpPeriodReport {
	selected := make([]earlyBumpEvent, 0, len(events))
	for _, event := range events {
		if include(event) {
			selected = append(selected, event)
		}
	}
	report := earlyBumpPeriodReport{Name: name, Signals: len(selected)}
	if len(selected) > 0 {
		report.From, report.To = selected[0].At, selected[len(selected)-1].At
	}
	for deltaIndex, delta := range in.DeltasBps {
		metric := earlyBumpDeltaMetrics{DeltaBps: delta, Signals: len(selected)}
		netSum, maxEdgeSum, pairedSum := 0.0, 0.0, 0.0
		for _, event := range selected {
			if event.EscapedUp {
				metric.UpEscapeEvents++
			}
			outcome := event.Outcomes[deltaIndex]
			baseline := event.Outcomes[0]
			if outcome.Filled {
				metric.Fills++
				netSum += outcome.TerminalNetMarkoutBps
				maxEdgeSum += outcome.MaximumNetEdgeBps
			}
			if outcome.CaughtEscape {
				metric.CaughtEscapes++
			}
			if outcome.Filled && !baseline.Filled {
				metric.IncrementalFillsVsBaseline++
			}
			if outcome.CaughtEscape && !baseline.CaughtEscape {
				metric.IncrementalCaughtVsBaseline++
			}
			if outcome.FeePositiveTouch {
				metric.FeePositiveTouches++
			}
			if outcome.AdverseAfterFill {
				metric.AdverseFills++
			}
			pairedSum += outcomeValue(outcome) - outcomeValue(baseline)
		}
		if metric.Signals > 0 {
			metric.FillRate = float64(metric.Fills) / float64(metric.Signals)
			metric.MeanTerminalNetBpsPerSignal = netSum / float64(metric.Signals)
			metric.PairedNetDeltaBpsPerSignal = pairedSum / float64(metric.Signals)
			metric.FillRateWilson95[0], metric.FillRateWilson95[1] = wilson95(metric.Fills, metric.Signals)
		}
		if metric.UpEscapeEvents > 0 {
			metric.CatchRateGivenEscape = float64(metric.CaughtEscapes) / float64(metric.UpEscapeEvents)
			metric.CatchRateWilson95[0], metric.CatchRateWilson95[1] = wilson95(metric.CaughtEscapes, metric.UpEscapeEvents)
		}
		if metric.Fills > 0 {
			metric.FeePositiveRateGivenFill = float64(metric.FeePositiveTouches) / float64(metric.Fills)
			metric.AdverseRateGivenFill = float64(metric.AdverseFills) / float64(metric.Fills)
			metric.MeanTerminalNetBpsPerFill = netSum / float64(metric.Fills)
			metric.MeanMaximumNetEdgeBpsPerFill = maxEdgeSum / float64(metric.Fills)
		}
		metric.PairedNetDeltaDailyBootstrap95 = bootstrapEarlyBumpDailyCI(selected, deltaIndex)
		report.Deltas = append(report.Deltas, metric)
	}
	return report
}

func outcomeValue(outcome earlyBumpOutcome) float64 {
	if !outcome.Filled {
		return 0
	}
	return outcome.TerminalNetMarkoutBps
}

func bootstrapEarlyBumpDailyCI(events []earlyBumpEvent, deltaIndex int) [2]float64 {
	type block struct {
		sum float64
		n   int
	}
	byDay := make(map[string]block)
	for _, event := range events {
		key := event.At.UTC().Format(time.DateOnly)
		value := outcomeValue(event.Outcomes[deltaIndex]) - outcomeValue(event.Outcomes[0])
		current := byDay[key]
		current.sum += value
		current.n++
		byDay[key] = current
	}
	if len(byDay) < 2 {
		return [2]float64{}
	}
	blocks := make([]block, 0, len(byDay))
	for _, value := range byDay {
		blocks = append(blocks, value)
	}
	rng := rand.New(rand.NewSource(42))
	values := make([]float64, 5000)
	for sample := range values {
		sum, count := 0.0, 0
		for range blocks {
			chosen := blocks[rng.Intn(len(blocks))]
			sum += chosen.sum
			count += chosen.n
		}
		if count > 0 {
			values[sample] = sum / float64(count)
		}
	}
	sort.Float64s(values)
	return [2]float64{values[int(.025*float64(len(values)-1))], values[int(.975*float64(len(values)-1))]}
}
