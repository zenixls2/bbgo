// gammacapture-horizon-research trains and evaluates a horizon-dependent,
// side-specific first-passage model. It reads SQLite and captured BBO files
// only; it never opens an exchange session or submits orders.
package main

import (
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/c9s/bbgo/pkg/strategy/gammacapture"
	"github.com/c9s/bbgo/pkg/types"
	_ "github.com/mattn/go-sqlite3"
)

const (
	featureCount       = 8
	evaluationStep     = 5
	recentLookbackMins = 360
	logisticPenalty    = 0.0002
	ridgePenalty       = 0.0002
)

var (
	horizons  = []int{10, 15, 30}
	distances = []float64{20, 30, 40, 50, 60, 70, 80}
)

type minuteBar struct {
	at               time.Time
	high, low, close float64
}

type featureRow struct {
	values [featureCount]float64
	valid  bool
}

type standardizedFeatures struct {
	rows   []featureRow
	means  []float64
	scales []float64
}

type logisticModel struct {
	intercept    float64
	coefficients []float64
	trainingRate float64
}

type metricAccumulator struct {
	samples                   int
	brierBaseline, brierModel float64
	logBaseline, logModel     float64
}

type report struct {
	Symbol           string                           `json:"symbol"`
	Database         string                           `json:"database"`
	BBO              string                           `json:"bbo"`
	TrainFrom        string                           `json:"trainFrom"`
	ValidationFrom   string                           `json:"validationFrom"`
	TestFrom         string                           `json:"testFrom"`
	To               string                           `json:"to"`
	HistoricalWeight float64                          `json:"historicalWeight"`
	DBHoldout        gammacapture.HorizonTouchMetrics `json:"dbHoldout"`
	BBOHoldout       gammacapture.HorizonTouchMetrics `json:"bboHoldout"`
	ArtifactOutput   string                           `json:"artifactOutput"`
	Accepted         bool                             `json:"accepted"`
	AcceptanceReason string                           `json:"acceptanceReason"`
}

func main() {
	database := flag.String("database", "bbgo.sqlite3", "read-only BBGO SQLite database")
	symbol := flag.String("symbol", "SOLJPY", "target symbol")
	from := flag.String("from", "2025-12-01", "inclusive training start")
	validationFrom := flag.String("validation-from", "2026-05-01", "inclusive validation start")
	testFrom := flag.String("test-from", "2026-06-01", "inclusive chronological holdout start")
	to := flag.String("to", "2026-07-14", "exclusive database end")
	bboPath := flag.String("bbo", "", "separate captured bookticker CSV holdout")
	output := flag.String("artifact-output", "", "write trained JSON artifact here")
	historicalWeight := flag.Float64("historical-weight", 0.75, "historical-model weight when blending recent resolved touches")
	minDBImprovement := flag.Float64("minimum-db-brier-improvement-pct", 3, "minimum DB holdout Brier improvement")
	minBBOImprovement := flag.Float64("minimum-bbo-brier-improvement-pct", 5, "minimum BBO holdout Brier improvement")
	flag.Parse()

	if *from >= *validationFrom || *validationFrom >= *testFrom || *testFrom >= *to ||
		*historicalWeight <= 0 || *historicalWeight > 1 ||
		*minDBImprovement < 0 || *minBBOImprovement < 0 {
		fatalf("invalid date split, weight, or acceptance threshold")
	}

	db, err := sql.Open("sqlite3", "file:"+*database+"?mode=ro")
	if err != nil {
		fatalf("open database: %v", err)
	}
	defer db.Close()
	bars, err := loadSQLiteBars(db, *symbol, *from, *to)
	if err != nil {
		fatalf("load SQLite bars: %v", err)
	}
	if len(bars) < 10_000 {
		fatalf("insufficient SQLite bars: %d", len(bars))
	}
	features := buildFeatures(bars)
	trainStart := parseDate(*from)
	validationStart := parseDate(*validationFrom)
	testStart := parseDate(*testFrom)
	end := parseDate(*to)

	dbMetrics, fairPriceMetrics := evaluateDatabase(
		bars, features, trainStart, validationStart, testStart, end, *historicalWeight,
	)
	dbMetrics.FairPriceMAEBaseline = fairPriceMetrics.FairPriceMAEBaseline
	dbMetrics.FairPriceMAEModel = fairPriceMetrics.FairPriceMAEModel
	dbMetrics.FairPriceImprovement = fairPriceMetrics.FairPriceImprovement
	// The point forecast did not pass the independent gate: keep the current
	// mid-price martingale center and deploy only the improved distribution.
	dbMetrics.FairPriceShiftEnabled = fairPriceMetrics.FairPriceImprovement >= 5

	finalStandardized := standardize(features, bars, func(i int) bool {
		return !bars[i].at.Before(trainStart) && bars[i].at.Before(end)
	})
	cells := trainCells(bars, finalStandardized, trainStart, end)
	bboMetrics := gammacapture.HorizonTouchMetrics{}
	if *bboPath != "" {
		bboBars, readErr := loadBBOBars(*bboPath)
		if readErr != nil {
			fatalf("load BBO holdout: %v", readErr)
		}
		bboMetrics = evaluateBBO(bboBars, finalStandardized, cells, *historicalWeight)
	}

	accepted := dbMetrics.BrierImprovementPct >= *minDBImprovement &&
		dbMetrics.LogLossModel < dbMetrics.LogLossBaseline &&
		bboMetrics.Samples > 0 &&
		bboMetrics.BrierImprovementPct >= *minBBOImprovement &&
		bboMetrics.LogLossModel < bboMetrics.LogLossBaseline
	reason := fmt.Sprintf(
		"DB Brier %.3f%% and BBO Brier %.3f%%; fair-price point shift %.3f%% remains disabled",
		dbMetrics.BrierImprovementPct, bboMetrics.BrierImprovementPct, dbMetrics.FairPriceImprovement,
	)
	if !accepted {
		reason = "acceptance gate failed: " + reason
	}
	// SQLite OHLC has no executable BBO side, so this legacy research path can
	// report midpoint-proxy metrics but cannot produce a deployable v2 artifact.
	accepted = false
	reason = "not deployable: midpoint proxy labels do not satisfy executable-bbo v2 semantics; " + reason
	artifact := gammacapture.HorizonTouchArtifact{
		Version:          gammacapture.HorizonTouchModelVersion,
		PriceBasis:       "midpoint-proxy",
		Symbol:           strings.ToUpper(*symbol),
		GeneratedAt:      time.Now().UTC(),
		TrainFrom:        *from,
		TrainTo:          *to,
		FeatureNames:     append([]string(nil), gammacapture.HorizonTouchFeatureNames...),
		FeatureMeans:     finalStandardized.means,
		FeatureScales:    finalStandardized.scales,
		HistoricalWeight: *historicalWeight,
		Accepted:         accepted,
		AcceptanceReason: reason,
		DBHoldout:        dbMetrics,
		BBOHoldout:       bboMetrics,
		Cells:            cells,
	}
	if *output != "" {
		if err := writeArtifact(*output, artifact); err != nil {
			fatalf("write artifact: %v", err)
		}
	}
	out := report{
		Symbol: strings.ToUpper(*symbol), Database: *database, BBO: *bboPath,
		TrainFrom: *from, ValidationFrom: *validationFrom, TestFrom: *testFrom, To: *to,
		HistoricalWeight: *historicalWeight, DBHoldout: dbMetrics, BBOHoldout: bboMetrics,
		ArtifactOutput: *output, Accepted: accepted, AcceptanceReason: reason,
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(out); err != nil {
		fatalf("encode report: %v", err)
	}
}

func loadSQLiteBars(db *sql.DB, symbol, from, to string) ([]minuteBar, error) {
	rows, err := db.Query(`
		SELECT start_time, high, low, close
		FROM binance_klines
		WHERE symbol = ? AND interval = '1m' AND start_time >= ? AND start_time < ?
		ORDER BY start_time`, strings.ToUpper(symbol), from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var bars []minuteBar
	for rows.Next() {
		var rawTime string
		var bar minuteBar
		if err := rows.Scan(&rawTime, &bar.high, &bar.low, &bar.close); err != nil {
			return nil, err
		}
		at, err := parseDatabaseTime(rawTime)
		if err != nil {
			return nil, err
		}
		bar.at = at
		if bar.high > 0 && bar.low > 0 && bar.close > 0 {
			bars = append(bars, bar)
		}
	}
	return bars, rows.Err()
}

func loadBBOBars(path string) ([]minuteBar, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader := csv.NewReader(file)
	if _, err := reader.Read(); err != nil {
		return nil, err
	}
	byMinute := make(map[int64]minuteBar)
	for {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil || len(row) < 4 {
			continue
		}
		at, timeErr := time.Parse(time.RFC3339Nano, row[0])
		bid, bidErr := strconv.ParseFloat(row[1], 64)
		ask, askErr := strconv.ParseFloat(row[3], 64)
		if timeErr != nil || bidErr != nil || askErr != nil || bid <= 0 || ask < bid {
			continue
		}
		mid := (bid + ask) / 2
		minute := at.Unix() / 60
		bar, ok := byMinute[minute]
		if !ok {
			bar = minuteBar{at: time.Unix(minute*60, 0).UTC(), high: mid, low: mid, close: mid}
		} else {
			bar.high = math.Max(bar.high, mid)
			bar.low = math.Min(bar.low, mid)
			bar.close = mid
		}
		byMinute[minute] = bar
	}
	if len(byMinute) == 0 {
		return nil, fmt.Errorf("no valid BBO rows")
	}
	minutes := make([]int64, 0, len(byMinute))
	for minute := range byMinute {
		minutes = append(minutes, minute)
	}
	sort.Slice(minutes, func(i, j int) bool { return minutes[i] < minutes[j] })
	first, last := minutes[0], minutes[len(minutes)-1]
	bars := make([]minuteBar, 0, int(last-first+1))
	for minute := first; minute <= last; minute++ {
		if bar, ok := byMinute[minute]; ok {
			bars = append(bars, bar)
		} else {
			bars = append(bars, minuteBar{at: time.Unix(minute*60, 0).UTC()})
		}
	}
	return bars, nil
}

func buildFeatures(bars []minuteBar) []featureRow {
	rows := make([]featureRow, len(bars))
	returns1m := make([]float64, len(bars))
	for i := 1; i < len(bars); i++ {
		if bars[i].close > 0 && bars[i-1].close > 0 {
			returns1m[i] = math.Log(bars[i].close/bars[i-1].close) * 10_000
		} else {
			returns1m[i] = math.NaN()
		}
	}
	for i := 240; i < len(bars); i++ {
		lags := []int{1, 5, 15, 60, 240}
		valid := bars[i].close > 0
		for feature, lag := range lags {
			if bars[i-lag].close <= 0 {
				valid = false
				break
			}
			rows[i].values[feature] = math.Log(bars[i].close/bars[i-lag].close) * 10_000
		}
		if !valid {
			continue
		}
		for feature, window := range []int{15, 60, 240} {
			sumSquares := 0.0
			count := 0
			for j := i - window + 1; j <= i; j++ {
				value := returns1m[j]
				if finite(value) {
					sumSquares += value * value
					count++
				}
			}
			if count < int(math.Ceil(0.8*float64(window))) {
				valid = false
				break
			}
			rows[i].values[5+feature] = math.Sqrt(sumSquares / float64(count))
		}
		rows[i].valid = valid
	}
	return rows
}

func standardize(features []featureRow, bars []minuteBar, include func(int) bool) standardizedFeatures {
	means := make([]float64, featureCount)
	scales := make([]float64, featureCount)
	count := 0
	for i, row := range features {
		if !row.valid || !include(i) {
			continue
		}
		count++
		for j, value := range row.values {
			means[j] += value
		}
	}
	if count == 0 {
		fatalf("no feature rows available for standardization")
	}
	for j := range means {
		means[j] /= float64(count)
	}
	for i, row := range features {
		if !row.valid || !include(i) {
			continue
		}
		for j, value := range row.values {
			delta := value - means[j]
			scales[j] += delta * delta
		}
	}
	for j := range scales {
		scales[j] = math.Sqrt(scales[j] / float64(count))
		if scales[j] <= 1e-12 {
			scales[j] = 1
		}
	}
	standardized := make([]featureRow, len(features))
	for i, row := range features {
		standardized[i].valid = row.valid
		if !row.valid {
			continue
		}
		for j, value := range row.values {
			standardized[i].values[j] = (value - means[j]) / scales[j]
		}
	}
	return standardizedFeatures{rows: standardized, means: means, scales: scales}
}

func evaluateDatabase(
	bars []minuteBar,
	rawFeatures []featureRow,
	trainStart, validationStart, testStart, end time.Time,
	historicalWeight float64,
) (gammacapture.HorizonTouchMetrics, gammacapture.HorizonTouchMetrics) {
	standardized := standardize(rawFeatures, bars, func(i int) bool {
		return !bars[i].at.Before(trainStart) && bars[i].at.Before(testStart)
	})
	var aggregate metricAccumulator
	for _, horizon := range horizons {
		up, down := pathLabels(bars, horizon)
		for _, distance := range distances {
			for _, side := range []types.SideType{types.SideTypeBuy, types.SideTypeSell} {
				labels := binaryLabels(up, down, distance, side)
				model := fitLogistic(standardized.rows, labels, bars, trainStart, testStart, horizon)
				aggregate.add(evaluateCell(
					standardized.rows, labels, bars, model, testStart, end,
					horizon, historicalWeight,
				))
			}
		}
	}
	touchMetrics := aggregate.metrics()
	fairMetrics := evaluateFairPrice(bars, standardized.rows, trainStart, testStart, end)
	_ = validationStart // retained in the report and CLI split contract
	return touchMetrics, fairMetrics
}

func trainCells(
	bars []minuteBar,
	standardized standardizedFeatures,
	from, to time.Time,
) []gammacapture.HorizonTouchCell {
	cells := make([]gammacapture.HorizonTouchCell, 0, len(horizons)*len(distances)*2)
	for _, horizon := range horizons {
		up, down := pathLabels(bars, horizon)
		for _, distance := range distances {
			for _, side := range []types.SideType{types.SideTypeBuy, types.SideTypeSell} {
				labels := binaryLabels(up, down, distance, side)
				model := fitLogistic(standardized.rows, labels, bars, from, to, horizon)
				cells = append(cells, gammacapture.HorizonTouchCell{
					HorizonMinutes: horizon,
					DistanceBps:    distance,
					Side:           string(side),
					Intercept:      model.intercept,
					Coefficients:   append([]float64(nil), model.coefficients...),
					TrainingRate:   model.trainingRate,
				})
			}
		}
	}
	return cells
}

func evaluateBBO(
	bars []minuteBar,
	trainingStandardized standardizedFeatures,
	cells []gammacapture.HorizonTouchCell,
	historicalWeight float64,
) gammacapture.HorizonTouchMetrics {
	raw := buildFeatures(bars)
	standardized := standardizedFeatures{
		rows:   make([]featureRow, len(raw)),
		means:  trainingStandardized.means,
		scales: trainingStandardized.scales,
	}
	for i, row := range raw {
		standardized.rows[i].valid = row.valid
		if !row.valid {
			continue
		}
		for j, value := range row.values {
			standardized.rows[i].values[j] = (value - standardized.means[j]) / standardized.scales[j]
		}
	}
	var aggregate metricAccumulator
	for _, horizon := range horizons {
		up, down := pathLabels(bars, horizon)
		for _, distance := range distances {
			for _, side := range []types.SideType{types.SideTypeBuy, types.SideTypeSell} {
				labels := binaryLabels(up, down, distance, side)
				model, ok := findModel(cells, horizon, distance, side)
				if !ok {
					continue
				}
				aggregate.add(evaluateCell(
					standardized.rows, labels, bars, model,
					bars[0].at, bars[len(bars)-1].at.Add(time.Minute),
					horizon, historicalWeight,
				))
			}
		}
	}
	return aggregate.metrics()
}

func pathLabels(bars []minuteBar, horizon int) ([]float64, []float64) {
	up := make([]float64, len(bars))
	down := make([]float64, len(bars))
	for i := range up {
		up[i], down[i] = math.NaN(), math.NaN()
	}
	for i := 0; i+horizon < len(bars); i++ {
		if bars[i].close <= 0 {
			continue
		}
		maximum, minimum := 0.0, math.Inf(1)
		valid := true
		for j := i + 1; j <= i+horizon; j++ {
			if bars[j].high <= 0 || bars[j].low <= 0 {
				valid = false
				break
			}
			maximum = math.Max(maximum, bars[j].high)
			minimum = math.Min(minimum, bars[j].low)
		}
		if valid {
			up[i] = math.Log(maximum/bars[i].close) * 10_000
			down[i] = math.Log(bars[i].close/minimum) * 10_000
		}
	}
	return up, down
}

func binaryLabels(up, down []float64, distance float64, side types.SideType) []float64 {
	labels := make([]float64, len(up))
	for i := range labels {
		value := up[i]
		if side == types.SideTypeBuy {
			value = down[i]
		}
		if !finite(value) {
			labels[i] = math.NaN()
		} else if value >= distance {
			labels[i] = 1
		}
	}
	return labels
}

func fitLogistic(
	features []featureRow,
	labels []float64,
	bars []minuteBar,
	from, to time.Time,
	purgeMinutes int,
) logisticModel {
	indices := make([]int, 0, len(features)/evaluationStep)
	end := to.Add(-time.Duration(purgeMinutes) * time.Minute)
	positives := 0.0
	for i, row := range features {
		if i%evaluationStep != 0 || !row.valid || !finite(labels[i]) ||
			bars[i].at.Before(from) || !bars[i].at.Before(end) {
			continue
		}
		indices = append(indices, i)
		positives += labels[i]
	}
	if len(indices) == 0 || positives <= 0 || positives >= float64(len(indices)) {
		return logisticModel{}
	}
	parameters := make([]float64, featureCount+1)
	parameters[0] = probabilityLogit(positives / float64(len(indices)))
	for iteration := 0; iteration < 30; iteration++ {
		gradient := make([]float64, len(parameters))
		hessian := make([][]float64, len(parameters))
		for i := range hessian {
			hessian[i] = make([]float64, len(parameters))
		}
		for _, index := range indices {
			x := make([]float64, len(parameters))
			x[0] = 1
			copy(x[1:], features[index].values[:])
			value := parameters[0]
			for j := 1; j < len(parameters); j++ {
				value += parameters[j] * x[j]
			}
			probability := sigmoid(value)
			residual := probability - labels[index]
			weight := math.Max(1e-9, probability*(1-probability))
			for j := range parameters {
				gradient[j] += residual * x[j]
				for k := range parameters {
					hessian[j][k] += weight * x[j] * x[k]
				}
			}
		}
		n := float64(len(indices))
		for j := range parameters {
			gradient[j] /= n
			for k := range parameters {
				hessian[j][k] /= n
			}
			if j > 0 {
				gradient[j] += logisticPenalty * parameters[j]
				hessian[j][j] += logisticPenalty
			}
		}
		step, ok := solveLinear(hessian, gradient)
		if !ok {
			break
		}
		maximumStep := 0.0
		for j := range parameters {
			parameters[j] -= step[j]
			maximumStep = math.Max(maximumStep, math.Abs(step[j]))
		}
		if maximumStep < 1e-7 {
			break
		}
	}
	return logisticModel{
		intercept: parameters[0], coefficients: append([]float64(nil), parameters[1:]...),
		trainingRate: positives / float64(len(indices)),
	}
}

func evaluateCell(
	features []featureRow,
	labels []float64,
	bars []minuteBar,
	model logisticModel,
	from, to time.Time,
	horizon int,
	historicalWeight float64,
) metricAccumulator {
	var metric metricAccumulator
	prefixSum := make([]float64, len(labels)+1)
	prefixCount := make([]int, len(labels)+1)
	for i, label := range labels {
		prefixSum[i+1] = prefixSum[i]
		prefixCount[i+1] = prefixCount[i]
		if finite(label) {
			prefixSum[i+1] += label
			prefixCount[i+1]++
		}
	}
	for i, row := range features {
		if i%evaluationStep != 0 || !row.valid || !finite(labels[i]) ||
			bars[i].at.Before(from) || !bars[i].at.Before(to) {
			continue
		}
		historical := predictLogistic(model, row)
		recent := model.trainingRate
		resolvedEnd := i - horizon
		if resolvedEnd > 0 {
			start := max(0, resolvedEnd-recentLookbackMins)
			count := prefixCount[resolvedEnd] - prefixCount[start]
			if count > 0 {
				recent = (prefixSum[resolvedEnd] - prefixSum[start]) / float64(count)
			}
		}
		recent = clampProbability(recent)
		prediction := blendProbability(historical, recent, historicalWeight)
		label := labels[i]
		metric.samples++
		metric.brierBaseline += square(recent - label)
		metric.brierModel += square(prediction - label)
		metric.logBaseline += binaryLogLoss(label, recent)
		metric.logModel += binaryLogLoss(label, prediction)
	}
	return metric
}

func evaluateFairPrice(
	bars []minuteBar,
	features []featureRow,
	from, testFrom, to time.Time,
) gammacapture.HorizonTouchMetrics {
	var baselineAbsolute, modelAbsolute float64
	samples := 0
	for _, horizon := range horizons {
		target := forwardAverageReturns(bars, horizon)
		coefficients := fitRidge(features, target, bars, from, testFrom, horizon)
		for i, row := range features {
			if i%evaluationStep != 0 || !row.valid || !finite(target[i]) ||
				bars[i].at.Before(testFrom) || !bars[i].at.Before(to) {
				continue
			}
			prediction := coefficients[0]
			for j, value := range row.values {
				prediction += coefficients[j+1] * value
			}
			baselineAbsolute += math.Abs(target[i])
			modelAbsolute += math.Abs(prediction - target[i])
			samples++
		}
	}
	metrics := gammacapture.HorizonTouchMetrics{Samples: samples}
	if samples > 0 {
		metrics.FairPriceMAEBaseline = baselineAbsolute / float64(samples)
		metrics.FairPriceMAEModel = modelAbsolute / float64(samples)
		if metrics.FairPriceMAEBaseline > 0 {
			metrics.FairPriceImprovement = 100 * (metrics.FairPriceMAEBaseline - metrics.FairPriceMAEModel) / metrics.FairPriceMAEBaseline
		}
	}
	return metrics
}

func forwardAverageReturns(bars []minuteBar, horizon int) []float64 {
	out := make([]float64, len(bars))
	for i := range out {
		out[i] = math.NaN()
	}
	for i := 0; i+horizon < len(bars); i++ {
		if bars[i].close <= 0 {
			continue
		}
		sum := 0.0
		valid := true
		for j := i + 1; j <= i+horizon; j++ {
			if bars[j].close <= 0 {
				valid = false
				break
			}
			sum += math.Log(bars[j].close)
		}
		if valid {
			out[i] = (sum/float64(horizon) - math.Log(bars[i].close)) * 10_000
		}
	}
	return out
}

func fitRidge(
	features []featureRow,
	target []float64,
	bars []minuteBar,
	from, to time.Time,
	purgeMinutes int,
) []float64 {
	size := featureCount + 1
	gram := make([][]float64, size)
	rhs := make([]float64, size)
	for i := range gram {
		gram[i] = make([]float64, size)
	}
	count := 0.0
	end := to.Add(-time.Duration(purgeMinutes) * time.Minute)
	for i, row := range features {
		if i%evaluationStep != 0 || !row.valid || !finite(target[i]) ||
			bars[i].at.Before(from) || !bars[i].at.Before(end) {
			continue
		}
		x := make([]float64, size)
		x[0] = 1
		copy(x[1:], row.values[:])
		for j := range x {
			rhs[j] += x[j] * target[i]
			for k := range x {
				gram[j][k] += x[j] * x[k]
			}
		}
		count++
	}
	if count == 0 {
		return make([]float64, size)
	}
	for j := range gram {
		rhs[j] /= count
		for k := range gram[j] {
			gram[j][k] /= count
		}
		if j > 0 {
			gram[j][j] += ridgePenalty
		}
	}
	solution, ok := solveLinear(gram, rhs)
	if !ok {
		return make([]float64, size)
	}
	return solution
}

func findModel(cells []gammacapture.HorizonTouchCell, horizon int, distance float64, side types.SideType) (logisticModel, bool) {
	for _, cell := range cells {
		if cell.HorizonMinutes == horizon && math.Abs(cell.DistanceBps-distance) < 1e-9 && strings.EqualFold(cell.Side, string(side)) {
			return logisticModel{intercept: cell.Intercept, coefficients: cell.Coefficients, trainingRate: cell.TrainingRate}, true
		}
	}
	return logisticModel{}, false
}

func predictLogistic(model logisticModel, row featureRow) float64 {
	value := model.intercept
	for i, coefficient := range model.coefficients {
		value += coefficient * row.values[i]
	}
	return clampProbability(sigmoid(value))
}

func (m *metricAccumulator) add(other metricAccumulator) {
	m.samples += other.samples
	m.brierBaseline += other.brierBaseline
	m.brierModel += other.brierModel
	m.logBaseline += other.logBaseline
	m.logModel += other.logModel
}

func (m metricAccumulator) metrics() gammacapture.HorizonTouchMetrics {
	out := gammacapture.HorizonTouchMetrics{Samples: m.samples}
	if m.samples == 0 {
		return out
	}
	n := float64(m.samples)
	out.BrierBaseline = m.brierBaseline / n
	out.BrierModel = m.brierModel / n
	out.LogLossBaseline = m.logBaseline / n
	out.LogLossModel = m.logModel / n
	if out.BrierBaseline > 0 {
		out.BrierImprovementPct = 100 * (out.BrierBaseline - out.BrierModel) / out.BrierBaseline
	}
	return out
}

func solveLinear(matrix [][]float64, rhs []float64) ([]float64, bool) {
	n := len(rhs)
	augmented := make([][]float64, n)
	for i := 0; i < n; i++ {
		augmented[i] = append(append([]float64(nil), matrix[i]...), rhs[i])
	}
	for column := 0; column < n; column++ {
		pivot := column
		for row := column + 1; row < n; row++ {
			if math.Abs(augmented[row][column]) > math.Abs(augmented[pivot][column]) {
				pivot = row
			}
		}
		if math.Abs(augmented[pivot][column]) < 1e-12 {
			return nil, false
		}
		augmented[column], augmented[pivot] = augmented[pivot], augmented[column]
		scale := augmented[column][column]
		for j := column; j <= n; j++ {
			augmented[column][j] /= scale
		}
		for row := 0; row < n; row++ {
			if row == column {
				continue
			}
			factor := augmented[row][column]
			for j := column; j <= n; j++ {
				augmented[row][j] -= factor * augmented[column][j]
			}
		}
	}
	out := make([]float64, n)
	for i := range out {
		out[i] = augmented[i][n]
	}
	return out, true
}

func writeArtifact(path string, artifact gammacapture.HorizonTouchArtifact) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(artifact); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func parseDatabaseTime(value string) (time.Time, error) {
	for _, layout := range []string{
		"2006-01-02 15:04:05Z07:00",
		"2006-01-02 15:04:05.999Z07:00",
		time.RFC3339Nano,
	} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported database time %q", value)
}

func parseDate(value string) time.Time {
	parsed, err := time.Parse(time.DateOnly, value)
	if err != nil {
		fatalf("parse date %q: %v", value, err)
	}
	return parsed
}

func blendProbability(historical, recent, weight float64) float64 {
	if weight <= 0 || weight > 1 {
		weight = 0.75
	}
	return clampProbability(sigmoid(weight*probabilityLogit(historical) + (1-weight)*probabilityLogit(recent)))
}

func probabilityLogit(value float64) float64 {
	value = clampProbability(value)
	return math.Log(value / (1 - value))
}

func sigmoid(value float64) float64 {
	if value >= 0 {
		z := math.Exp(-value)
		return 1 / (1 + z)
	}
	z := math.Exp(value)
	return z / (1 + z)
}

func clampProbability(value float64) float64 {
	if value < 0.001 {
		return 0.001
	}
	if value > 0.999 {
		return 0.999
	}
	return value
}

func binaryLogLoss(label, probability float64) float64 {
	probability = clampProbability(probability)
	return -(label*math.Log(probability) + (1-label)*math.Log1p(-probability))
}

func square(value float64) float64 { return value * value }

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(2)
}
