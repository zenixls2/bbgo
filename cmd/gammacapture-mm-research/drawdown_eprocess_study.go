package main

import (
	"encoding/json"
	"math"
	"os"
	"sort"
	"time"

	"github.com/c9s/bbgo/pkg/strategy/gammacapture"
)

type drawdownEProcessStudyInput struct {
	DataPath         string
	Symbol           string
	From, To         time.Time
	Horizon          time.Duration
	RoundTripCostBps float64
	BarrierWidth     float64
	ConfidenceZ      float64
	Windows          []time.Duration
}

type drawdownEProcessSignal struct {
	At                  time.Time `json:"at"`
	EValue              float64   `json:"eValue"`
	PosteriorDiagnostic float64   `json:"posteriorDiagnostic"`
	AskScoreBps         float64   `json:"askScoreBps"`
	BidScoreBps         float64   `json:"bidScoreBps"`
	Outcome             string    `json:"outcome"`
	PassageMinutes      int       `json:"passageMinutes"`
}

type drawdownEProcessDay struct {
	Day        string `json:"day"`
	Signals    int    `json:"signals"`
	DownFirst  int    `json:"downFirst"`
	UpFirst    int    `json:"upFirst"`
	Censored   int    `json:"censored"`
	Recoveries int    `json:"recoveries"`
}

type drawdownEProcessStudyReport struct {
	Mode                string                   `json:"mode"`
	Symbol              string                   `json:"symbol"`
	From                time.Time                `json:"from"`
	To                  time.Time                `json:"to"`
	HorizonMinutes      int                      `json:"horizonMinutes"`
	RoundTripCostBps    float64                  `json:"roundTripCostBps"`
	Threshold           float64                  `json:"eValueThreshold"`
	Signals             int                      `json:"signals"`
	Recoveries          int                      `json:"recoveries"`
	DownFirst           int                      `json:"downFirst"`
	UpFirst             int                      `json:"upFirst"`
	Censored            int                      `json:"censored"`
	ResolvedDownRate    float64                  `json:"resolvedDownRate"`
	DownRateWilsonLower float64                  `json:"downRateWilsonLower95"`
	BaselineResolved    int                      `json:"baselineResolved"`
	BaselineDownRate    float64                  `json:"baselineDownRate"`
	BaselineWilsonLower float64                  `json:"baselineWilsonLower95"`
	MeanPassageMinutes  float64                  `json:"meanPassageMinutes"`
	SignalsDetail       []drawdownEProcessSignal `json:"signalsDetail"`
	Daily               []drawdownEProcessDay    `json:"daily"`
	Acceptance          string                   `json:"acceptance"`
}

func runDrawdownEProcessStudy(input drawdownEProcessStudyInput) {
	books := compactBBO(readBBO(input.DataPath, input.Symbol, input.From, input.To))
	closes := minuteRegimeCloses(books)
	report := evaluateDrawdownEProcess(closes, input)
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fatalf("encode drawdown e-process report: %v", err)
	}
}

func evaluateDrawdownEProcess(closes []minuteRegimeClose, input drawdownEProcessStudyInput) drawdownEProcessStudyReport {
	report := drawdownEProcessStudyReport{
		Mode: "standalone-qv-drawdown-eprocess-study", Symbol: input.Symbol,
		From: input.From, To: input.To, HorizonMinutes: int(input.Horizon / time.Minute),
		RoundTripCostBps: input.RoundTripCostBps,
		Acceptance:       "reject: requires early target-day detection and a resolved down-first rate above the unconditional executable baseline",
	}
	if len(closes) < 2 || input.Horizon <= 0 || input.RoundTripCostBps <= 0 {
		return report
	}
	model := gammacapture.NewDrawdownEProcess(gammacapture.DrawdownEProcessConfig{
		BarrierWidth: input.BarrierWidth, Windows: input.Windows,
		ConfidenceZ: input.ConfidenceZ,
	})
	steps := int(input.Horizon / time.Minute)
	days := make(map[string]*drawdownEProcessDay)
	passageTotal := 0
	for index, close := range closes {
		decision := model.ObserveMinute(close.at, close.bid, close.ask)
		if report.Threshold == 0 && decision.Threshold > 0 {
			report.Threshold = decision.Threshold
		}
		day := days[close.at.Format(time.DateOnly)]
		if day == nil {
			day = &drawdownEProcessDay{Day: close.at.Format(time.DateOnly)}
			days[day.Day] = day
		}
		if decision.RecoveryAlarm {
			report.Recoveries++
			day.Recoveries++
		}
		if !decision.DownAlarm || index+steps >= len(closes) ||
			!closes[index+steps].at.Equal(close.at.Add(input.Horizon)) {
			continue
		}
		outcome, passage := executableFirstPassageWithStep(
			closes, index, steps, input.RoundTripCostBps)
		signal := drawdownEProcessSignal{
			At: close.at, EValue: decision.DownEValue,
			PosteriorDiagnostic: decision.DownProbability,
			AskScoreBps:         decision.AskScoreBps, BidScoreBps: decision.BidScoreBps,
			PassageMinutes: passage,
		}
		report.Signals++
		day.Signals++
		switch outcome {
		case 1:
			signal.Outcome = "down-first"
			report.DownFirst++
			day.DownFirst++
			passageTotal += passage
		case -1:
			signal.Outcome = "up-first"
			report.UpFirst++
			day.UpFirst++
			passageTotal += passage
		default:
			signal.Outcome = "censored"
			report.Censored++
			day.Censored++
		}
		report.SignalsDetail = append(report.SignalsDetail, signal)
	}
	resolved := report.DownFirst + report.UpFirst
	if resolved > 0 {
		report.ResolvedDownRate = float64(report.DownFirst) / float64(resolved)
		report.DownRateWilsonLower = wilsonLower(report.DownFirst, resolved, 1.6448536269514722)
		report.MeanPassageMinutes = float64(passageTotal) / float64(resolved)
	}
	baseDown, baseUp := baselineExecutableFirstPassages(closes, steps, input.RoundTripCostBps)
	report.BaselineResolved = baseDown + baseUp
	if report.BaselineResolved > 0 {
		report.BaselineDownRate = float64(baseDown) / float64(report.BaselineResolved)
		report.BaselineWilsonLower = wilsonLower(baseDown, report.BaselineResolved, 1.6448536269514722)
	}
	keys := make([]string, 0, len(days))
	for key := range days {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if day := days[key]; day.Signals > 0 || day.Recoveries > 0 {
			report.Daily = append(report.Daily, *day)
		}
	}
	if report.Signals > 0 && report.DownRateWilsonLower > report.BaselineDownRate {
		report.Acceptance = "conditional direction gate passes; timing and fee-aware Macro replay still required"
	}
	return report
}

func executableFirstPassageWithStep(closes []minuteRegimeClose, anchor, steps int, costBps float64) (int, int) {
	if anchor < 0 || steps <= 0 || anchor+steps >= len(closes) {
		return 0, 0
	}
	start := closes[anchor]
	cost := costBps / 10_000
	for step := 1; step <= steps; step++ {
		point := closes[anchor+step]
		if !point.at.Equal(start.at.Add(time.Duration(step) * time.Minute)) {
			return 0, 0
		}
		up := math.Log(point.bid/start.ask) >= cost
		down := -math.Log(point.ask/start.bid) >= cost
		if up == down {
			if up {
				return 0, step
			}
			continue
		}
		if down {
			return 1, step
		}
		return -1, step
	}
	return 0, 0
}

func baselineExecutableFirstPassages(closes []minuteRegimeClose, steps int, costBps float64) (int, int) {
	if steps <= 0 {
		return 0, 0
	}
	down, up := 0, 0
	for index := steps; index+steps < len(closes); index += steps {
		if !closes[index+steps].at.Equal(closes[index].at.Add(time.Duration(steps) * time.Minute)) {
			continue
		}
		outcome, _ := executableFirstPassageWithStep(closes, index, steps, costBps)
		if outcome > 0 {
			down++
		} else if outcome < 0 {
			up++
		}
	}
	return down, up
}
