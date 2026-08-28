//go:build ignore

package main

import (
	"encoding/json"
	"math"
	"os"
	"sort"
	"time"

	"github.com/c9s/bbgo/pkg/strategy/gammacapture"
	"github.com/c9s/bbgo/pkg/types"
)

type multiscaleRegimeStudyInput struct {
	DataPath         string
	Symbol           string
	From             time.Time
	To               time.Time
	Horizon          time.Duration
	AnchorStep       time.Duration
	RoundTripCostBps float64
	HazardMeans      []time.Duration
}

type multiscaleRegimeStudyReport struct {
	Mode              string                    `json:"mode"`
	Symbol            string                    `json:"symbol"`
	From              time.Time                 `json:"from"`
	To                time.Time                 `json:"to"`
	MinuteCloses      int                       `json:"minuteCloses"`
	HorizonMinutes    int                       `json:"horizonMinutes"`
	AnchorStepMinutes int                       `json:"anchorStepMinutes"`
	RoundTripCostBps  float64                   `json:"roundTripCostBps"`
	Variants          []multiscaleRegimeVariant `json:"variants"`
	Blockers          []string                  `json:"blockers"`
}

type multiscaleRegimeVariant struct {
	HazardMeanMinutes             int                   `json:"hazardMeanMinutes"`
	Predictions                   int                   `json:"predictions"`
	Resolved                      int                   `json:"resolved"`
	UpFirst                       int                   `json:"upFirst"`
	DownFirst                     int                   `json:"downFirst"`
	Censored                      int                   `json:"censored"`
	RawBrier                      float64               `json:"rawBrier"`
	CalibratedBrier               float64               `json:"calibratedBrier"`
	ClimatologyBrier              float64               `json:"climatologyBrier"`
	RawBrierSkill                 float64               `json:"rawBrierSkill"`
	CalibratedBrierSkill          float64               `json:"calibratedBrierSkill"`
	RawLogLoss                    float64               `json:"rawLogLoss"`
	CalibratedLogLoss             float64               `json:"calibratedLogLoss"`
	ClimatologyLogLoss            float64               `json:"climatologyLogLoss"`
	RawAccuracy                   float64               `json:"rawAccuracy"`
	CalibratedAccuracy            float64               `json:"calibratedAccuracy"`
	RawAccuracyWilsonLower        float64               `json:"rawAccuracyWilsonLower95"`
	CalibratedAccuracyWilsonLower float64               `json:"calibratedAccuracyWilsonLower95"`
	RawCalibrationError           float64               `json:"rawCalibrationError"`
	CalibratedCalibrationError    float64               `json:"calibratedCalibrationError"`
	MeanChangeProbability         float64               `json:"meanChangeProbability"`
	MeanJumpFraction              float64               `json:"meanJumpVariationFraction"`
	Daily                         []multiscaleRegimeDay `json:"daily"`
}

type multiscaleRegimeDay struct {
	Day                string  `json:"day"`
	Resolved           int     `json:"resolved"`
	RawBrier           float64 `json:"rawBrier"`
	CalibratedBrier    float64 `json:"calibratedBrier"`
	ClimatologyBrier   float64 `json:"climatologyBrier"`
	RawAccuracy        float64 `json:"rawAccuracy"`
	CalibratedAccuracy float64 `json:"calibratedAccuracy"`
}

type minuteRegimeClose struct {
	at       time.Time
	bid, ask float64
}

type regimePendingLabel struct {
	matures int
	bin     int
	outcome int
}

type regimeScore struct {
	p, y        float64
	day         string
	calibrated  float64
	climatology float64
}

type regimeCalibrationBin struct {
	n    int
	p, y float64
}

func runMultiscaleRegimeStudy(input multiscaleRegimeStudyInput) {
	books := compactBBO(readBBO(input.DataPath, input.Symbol, input.From, input.To))
	closes := minuteRegimeCloses(books)
	report := multiscaleRegimeStudyReport{
		Mode: "standalone-multiscale-regime-study", Symbol: input.Symbol,
		From: input.From, To: input.To, MinuteCloses: len(closes),
		HorizonMinutes:    int(input.Horizon / time.Minute),
		AnchorStepMinutes: int(input.AnchorStep / time.Minute),
		RoundTripCostBps:  input.RoundTripCostBps,
		Blockers: []string{
			"the posterior sign of a one-minute regime is not automatically a calibrated first-passage probability",
			"three-hour non-overlapping labels yield at most eight independent anchors per complete UTC day",
			"spot-only inventory cannot guarantee positive P&L across an unanticipated downward jump",
			"public BBO has no private fill or queue-priority observation",
		},
	}
	for _, hazard := range input.HazardMeans {
		report.Variants = append(report.Variants, evaluateMultiscaleRegimeV2(closes, input.Horizon, input.AnchorStep, input.RoundTripCostBps, hazard))
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fatalf("encode multiscale regime report: %v", err)
	}
}

func minuteRegimeCloses(books []bboSnapshot) []minuteRegimeClose {
	if len(books) == 0 {
		return nil
	}
	out := make([]minuteRegimeClose, 0, len(books)/10)
	for _, book := range books {
		minute := book.time.UTC().Truncate(time.Minute)
		if len(out) == 0 || !out[len(out)-1].at.Equal(minute) {
			out = append(out, minuteRegimeClose{at: minute, bid: book.bid, ask: book.ask})
			continue
		}
		out[len(out)-1].bid = book.bid
		out[len(out)-1].ask = book.ask
	}
	return out
}

func evaluateMultiscaleRegime(closes []minuteRegimeClose, horizon, anchorStep time.Duration, costBps float64, hazard time.Duration) multiscaleRegimeVariant {
	variant := multiscaleRegimeVariant{HazardMeanMinutes: int(hazard / time.Minute)}
	if len(closes) < 2 || horizon <= 0 || anchorStep <= 0 {
		return variant
	}
	model := gammacapture.NewBayesianMultiscaleRegime(gammacapture.MultiscaleRegimeConfig{
		HazardMean: types.Duration(hazard), MaximumRunLength: int((2 * horizon) / time.Minute),
		VolatilityWindow: 30, MinimumSamples: 30,
	})
	horizonSteps := int(horizon / time.Minute)
	stepMinutes := int(anchorStep / time.Minute)
	binDown, binUp := [10]int{}, [10]int{}
	globalDown, globalUp := 0, 0
	var pending []regimePendingLabel
	var scores []regimeScore

	for index, close := range closes {
		decision := model.ObserveMinute(close.at, close.bid, close.ask)
		remaining := pending[:0]
		for _, label := range pending {
			if label.matures > index {
				remaining = append(remaining, label)
				continue
			}
			switch label.outcome {
			case 1:
				binDown[label.bin]++
				globalDown++
			case -1:
				binUp[label.bin]++
				globalUp++
			}
		}
		pending = remaining
		minuteOfDay := close.at.Minute() + 60*close.at.Hour()
		if !decision.Healthy || index+horizonSteps >= len(closes) || minuteOfDay%stepMinutes != 0 {
			continue
		}
		if !closes[index+horizonSteps].at.Equal(close.at.Add(horizon)) {
			continue
		}
		outcome := executableFirstPassage(closes, index, horizonSteps, costBps)
		variant.Predictions++
		switch outcome {
		case 1:
			variant.DownFirst++
		case -1:
			variant.UpFirst++
		default:
			variant.Censored++
		}
		p := clampProbability(decision.DownProbability)
		bin := int(math.Min(9, math.Floor(p*10)))
		calibrated := float64(binDown[bin]+1) / float64(binDown[bin]+binUp[bin]+2)
		climatology := float64(globalDown+1) / float64(globalDown+globalUp+2)
		if binDown[bin]+binUp[bin] < 2 {
			calibrated = climatology
		}
		if outcome != 0 {
			y := 0.0
			if outcome == 1 {
				y = 1
			}
			scores = append(scores, regimeScore{p: p, calibrated: clampProbability(calibrated), climatology: clampProbability(climatology), y: y, day: close.at.Format(time.DateOnly)})
		}
		pending = append(pending, regimePendingLabel{matures: index + horizonSteps, bin: bin, outcome: outcome})
		variant.MeanChangeProbability += decision.ChangeProbability
		variant.MeanJumpFraction += decision.JumpVariationFraction
	}
	if variant.Predictions > 0 {
		variant.MeanChangeProbability /= float64(variant.Predictions)
		variant.MeanJumpFraction /= float64(variant.Predictions)
	}
	summarizeRegimeScores(&variant, scores)
	return variant
}

// executableFirstPassage returns 1 for down-first, -1 for up-first, and zero
// for a censored or same-minute tie. Increasing inventory must pay the ask and
// later liquidate at bid; reducing inventory observes the symmetric bid/ask
// opportunity.
func executableFirstPassage(closes []minuteRegimeClose, anchor, steps int, costBps float64) int {
	start := closes[anchor]
	cost := costBps / 10_000
	for step := 1; step <= steps; step++ {
		point := closes[anchor+step]
		up := math.Log(point.bid/start.ask) >= cost
		down := -math.Log(point.ask/start.bid) >= cost
		if up == down {
			if up {
				return 0
			}
			continue
		}
		if down {
			return 1
		}
		return -1
	}
	return 0
}

func summarizeRegimeScores(variant *multiscaleRegimeVariant, scores []regimeScore) {
	variant.Resolved = len(scores)
	if len(scores) == 0 {
		return
	}
	type dayAccumulator struct {
		n, rawCorrect, calibratedCorrect            int
		rawBrier, calibratedBrier, climatologyBrier float64
	}
	days := make(map[string]*dayAccumulator)
	var rawBins, calibratedBins [10]regimeCalibrationBin
	rawCorrect, calibratedCorrect := 0, 0
	for _, score := range scores {
		rawError := score.p - score.y
		calibratedError := score.calibrated - score.y
		climatologyError := score.climatology - score.y
		variant.RawBrier += rawError * rawError
		variant.CalibratedBrier += calibratedError * calibratedError
		variant.ClimatologyBrier += climatologyError * climatologyError
		variant.RawLogLoss += binaryLogLoss(score.p, score.y)
		variant.CalibratedLogLoss += binaryLogLoss(score.calibrated, score.y)
		variant.ClimatologyLogLoss += binaryLogLoss(score.climatology, score.y)
		rawHit := (score.p >= .5) == (score.y == 1)
		calibratedHit := (score.calibrated >= .5) == (score.y == 1)
		if rawHit {
			rawCorrect++
		}
		if calibratedHit {
			calibratedCorrect++
		}
		rawBin := int(math.Min(9, math.Floor(score.p*10)))
		calibratedBin := int(math.Min(9, math.Floor(score.calibrated*10)))
		rawBins[rawBin].n++
		rawBins[rawBin].p += score.p
		rawBins[rawBin].y += score.y
		calibratedBins[calibratedBin].n++
		calibratedBins[calibratedBin].p += score.calibrated
		calibratedBins[calibratedBin].y += score.y
		day := days[score.day]
		if day == nil {
			day = &dayAccumulator{}
			days[score.day] = day
		}
		day.n++
		day.rawBrier += rawError * rawError
		day.calibratedBrier += calibratedError * calibratedError
		day.climatologyBrier += climatologyError * climatologyError
		if rawHit {
			day.rawCorrect++
		}
		if calibratedHit {
			day.calibratedCorrect++
		}
	}
	n := float64(len(scores))
	variant.RawBrier /= n
	variant.CalibratedBrier /= n
	variant.ClimatologyBrier /= n
	variant.RawLogLoss /= n
	variant.CalibratedLogLoss /= n
	variant.ClimatologyLogLoss /= n
	variant.RawAccuracy = float64(rawCorrect) / n
	variant.CalibratedAccuracy = float64(calibratedCorrect) / n
	variant.RawAccuracyWilsonLower = wilsonLower(rawCorrect, len(scores), 1.959963984540054)
	variant.CalibratedAccuracyWilsonLower = wilsonLower(calibratedCorrect, len(scores), 1.959963984540054)
	if variant.ClimatologyBrier > 0 {
		variant.RawBrierSkill = 1 - variant.RawBrier/variant.ClimatologyBrier
		variant.CalibratedBrierSkill = 1 - variant.CalibratedBrier/variant.ClimatologyBrier
	}
	variant.RawCalibrationError = calibrationError(rawBins, len(scores))
	variant.CalibratedCalibrationError = calibrationError(calibratedBins, len(scores))
	keys := make([]string, 0, len(days))
	for day := range days {
		keys = append(keys, day)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := days[key]
		count := float64(value.n)
		variant.Daily = append(variant.Daily, multiscaleRegimeDay{Day: key, Resolved: value.n, RawBrier: value.rawBrier / count, CalibratedBrier: value.calibratedBrier / count, ClimatologyBrier: value.climatologyBrier / count, RawAccuracy: float64(value.rawCorrect) / count, CalibratedAccuracy: float64(value.calibratedCorrect) / count})
	}
}

func clampProbability(value float64) float64 { return math.Max(1e-9, math.Min(1-1e-9, value)) }
func binaryLogLoss(p, y float64) float64 {
	p = clampProbability(p)
	return -(y*math.Log(p) + (1-y)*math.Log(1-p))
}
func wilsonLower(successes, trials int, z float64) float64 {
	if trials <= 0 {
		return 0
	}
	n, p := float64(trials), float64(successes)/float64(trials)
	denominator := 1 + z*z/n
	return math.Max(0, (p+z*z/(2*n)-z*math.Sqrt(p*(1-p)/n+z*z/(4*n*n)))/denominator)
}
func calibrationError(bins [10]regimeCalibrationBin, total int) float64 {
	if total <= 0 {
		return 0
	}
	var out float64
	for _, bin := range bins {
		if bin.n == 0 {
			continue
		}
		out += float64(bin.n) / float64(total) * math.Abs(bin.p/float64(bin.n)-bin.y/float64(bin.n))
	}
	return out
}
