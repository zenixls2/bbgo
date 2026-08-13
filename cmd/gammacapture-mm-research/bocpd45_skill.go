package main

import (
	"encoding/json"
	"math"
	"os"
	"time"
)

type bocpd45SkillReport struct {
	Symbol                     string `json:"symbol"`
	From, To                   time.Time
	Horizon                    string  `json:"horizon"`
	Calibration                string  `json:"calibration"`
	MatureCalibrationSamples   int     `json:"matureCalibrationSamples"`
	Predictions                int     `json:"predictions"`
	UpOutcomes                 int     `json:"upOutcomes"`
	DirectionalAccuracyPct     float64 `json:"directionalAccuracyPct"`
	PrequentialBaseAccuracyPct float64 `json:"prequentialBaseAccuracyPct"`
	BrierScore                 float64 `json:"brierScore"`
	PrequentialBaseBrier       float64 `json:"prequentialBaseBrier"`
	BrierSkillPct              float64 `json:"brierSkillPct"`
	MeanProbability            float64 `json:"meanUpProbability"`
	MeanConfidence             float64 `json:"meanConfidence"`
}

type bocpd45CalibrationStudyReport struct {
	StrictlyPrequential bool                 `json:"strictlyPrequential"`
	CalibrationWindow   string               `json:"calibrationWindow"`
	RefitEvery          int                  `json:"refitEveryMatureLabels"`
	Methods             []bocpd45SkillReport `json:"methods"`
}

func runBOCPD45SkillStudy(dataPath, symbol string, from, to time.Time) {
	books := compactBBO(readBBO(dataPath, symbol, from.Add(-6*time.Hour-10*time.Minute), to))
	report := evaluateBOCPD45CalibrationStudy(books, symbol, from, to)
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fatalf("encode BOCPD45 skill report: %v", err)
	}
}

type bocpd45SkillAccumulator struct {
	report                  bocpd45SkillReport
	prequential             *bocpd45PrequentialCalibration
	upHistory, totalHistory float64
	correct, baseCorrect    int
}

func evaluateBOCPD45CalibrationStudy(books []bboSnapshot, symbol string, from, to time.Time) bocpd45CalibrationStudyReport {
	methods := []bocpd45CalibrationMethod{
		bocpd45CalibrationRaw,
		bocpd45CalibrationPlatt,
		bocpd45CalibrationBeta,
		bocpd45CalibrationIsotonic,
	}
	accumulators := make([]bocpd45SkillAccumulator, 0, len(methods))
	for _, method := range methods {
		accumulators = append(accumulators, bocpd45SkillAccumulator{
			report: bocpd45SkillReport{
				Symbol: symbol, From: from, To: to, Horizon: bocpd45ExpectedRunLength.String(), Calibration: string(method),
			},
			prequential: newBOCPD45PrequentialCalibration(method),
			upHistory:   1, totalHistory: 2,
		})
	}
	model := bocpd45DirectionModel{}
	previous := time.Time{}
	for _, book := range books {
		if book.time.After(to) {
			break
		}
		gap := !previous.IsZero() && book.time.Sub(previous) >= 15*time.Minute
		previous = book.time
		model.observe(book.time, book.bid, book.ask, gap)
		decision := model.snapshot()
		for index := range accumulators {
			accumulator := &accumulators[index]
			pending, label, labeled := accumulator.prequential.observe(
				book, decision, gap, !book.time.Before(from) && !book.time.After(to))
			if !labeled {
				continue
			}
			baseProbability := accumulator.upHistory / accumulator.totalHistory
			if pending.evaluationAnchor && !book.time.After(to) {
				probability := pending.usedProbability
				accumulator.report.BrierScore += (probability - label) * (probability - label)
				accumulator.report.PrequentialBaseBrier += (baseProbability - label) * (baseProbability - label)
				accumulator.report.MeanProbability += probability
				accumulator.report.MeanConfidence += math.Abs(2*probability - 1)
				if label == 1 {
					accumulator.report.UpOutcomes++
				}
				if (probability >= 0.5) == (label == 1) {
					accumulator.correct++
				}
				if (baseProbability >= 0.5) == (label == 1) {
					accumulator.baseCorrect++
				}
				accumulator.report.Predictions++
			}
			accumulator.upHistory += label
			accumulator.totalHistory++
		}
	}
	report := bocpd45CalibrationStudyReport{
		StrictlyPrequential: true,
		CalibrationWindow:   (bocpd45CalibrationWindow * bocpd45ExpectedRunLength).String(),
		RefitEvery:          bocpd45CalibrationRefitEvery,
	}
	for index := range accumulators {
		accumulator := &accumulators[index]
		accumulator.report.MatureCalibrationSamples = accumulator.prequential.matured
		if accumulator.report.Predictions > 0 {
			n := float64(accumulator.report.Predictions)
			accumulator.report.BrierScore /= n
			accumulator.report.PrequentialBaseBrier /= n
			accumulator.report.DirectionalAccuracyPct = 100 * float64(accumulator.correct) / n
			accumulator.report.PrequentialBaseAccuracyPct = 100 * float64(accumulator.baseCorrect) / n
			accumulator.report.MeanProbability /= n
			accumulator.report.MeanConfidence /= n
			if accumulator.report.PrequentialBaseBrier > 0 {
				accumulator.report.BrierSkillPct = 100 * (1 - accumulator.report.BrierScore/accumulator.report.PrequentialBaseBrier)
			}
		}
		report.Methods = append(report.Methods, accumulator.report)
	}
	return report
}

func evaluateBOCPD45Skill(books []bboSnapshot, symbol string, from, to time.Time) bocpd45SkillReport {
	return evaluateBOCPD45CalibrationStudy(books, symbol, from, to).Methods[0]
}
