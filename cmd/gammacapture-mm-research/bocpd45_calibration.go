package main

import (
	"fmt"
	"math"
	"time"
)

const (
	bocpd45CalibrationWindow     = 480 // 6 hours at one mature label per 45 seconds.
	bocpd45CalibrationMinSamples = 32
	bocpd45CalibrationRefitEvery = 8
)

type bocpd45CalibrationMethod string

const (
	bocpd45CalibrationRaw      bocpd45CalibrationMethod = "raw"
	bocpd45CalibrationPlatt    bocpd45CalibrationMethod = "platt"
	bocpd45CalibrationBeta     bocpd45CalibrationMethod = "beta"
	bocpd45CalibrationIsotonic bocpd45CalibrationMethod = "isotonic"
)

func parseBOCPD45CalibrationMethod(value string) bocpd45CalibrationMethod {
	method := bocpd45CalibrationMethod(value)
	switch method {
	case "":
		return bocpd45CalibrationPlatt
	case bocpd45CalibrationRaw, bocpd45CalibrationPlatt, bocpd45CalibrationBeta, bocpd45CalibrationIsotonic:
		return method
	default:
		panic(fmt.Sprintf("unsupported BOCPD45 calibration method %q", value))
	}
}

type bocpd45CalibrationSample struct {
	probability float64
	label       float64
}

// bocpd45RollingCalibrator is fitted only from labels which have already
// matured.  It deliberately retains a bounded six-hour window so that a
// calibration error from an old regime cannot permanently dominate a live
// session. Refit batching bounds replay CPU without changing label ordering.
type bocpd45RollingCalibrator struct {
	method  bocpd45CalibrationMethod
	samples []bocpd45CalibrationSample
	updates int
	ready   bool
	theta   []float64
	blocks  []bocpd45IsotonicBlock
}

type bocpd45IsotonicBlock struct {
	low, high float64
	mean      float64
	weight    float64
}

func newBOCPD45RollingCalibrator(method bocpd45CalibrationMethod) *bocpd45RollingCalibrator {
	c := &bocpd45RollingCalibrator{method: method}
	switch method {
	case bocpd45CalibrationPlatt:
		c.theta = []float64{0, 1}
	case bocpd45CalibrationBeta:
		c.theta = []float64{0, 1, 1}
	}
	return c
}

func (c *bocpd45RollingCalibrator) sampleCount() int { return len(c.samples) }

func (c *bocpd45RollingCalibrator) predict(probability float64) float64 {
	p := clampProbability(probability)
	if c == nil || c.method == bocpd45CalibrationRaw || !c.ready {
		return p
	}
	switch c.method {
	case bocpd45CalibrationPlatt, bocpd45CalibrationBeta:
		return clampProbability(logistic(dot(c.features(p), c.theta)))
	case bocpd45CalibrationIsotonic:
		for _, block := range c.blocks {
			if p <= block.high {
				return clampProbability(block.mean)
			}
		}
		if len(c.blocks) > 0 {
			return clampProbability(c.blocks[len(c.blocks)-1].mean)
		}
	}
	return p
}

func (c *bocpd45RollingCalibrator) update(probability, label float64) {
	if c == nil || c.method == bocpd45CalibrationRaw || (label != 0 && label != 1) {
		return
	}
	c.samples = append(c.samples, bocpd45CalibrationSample{probability: clampProbability(probability), label: label})
	if len(c.samples) > bocpd45CalibrationWindow {
		copy(c.samples, c.samples[len(c.samples)-bocpd45CalibrationWindow:])
		c.samples = c.samples[:bocpd45CalibrationWindow]
	}
	c.updates++
	if len(c.samples) < bocpd45CalibrationMinSamples {
		return
	}
	if !c.ready || c.updates%bocpd45CalibrationRefitEvery == 0 {
		c.refit()
	}
}

func (c *bocpd45RollingCalibrator) features(probability float64) []float64 {
	p := math.Max(1e-4, math.Min(1-1e-4, probability))
	if c.method == bocpd45CalibrationBeta {
		return []float64{1, math.Log(p), -math.Log1p(-p)}
	}
	return []float64{1, math.Log(p / (1 - p))}
}

func (c *bocpd45RollingCalibrator) refit() {
	if c.method == bocpd45CalibrationIsotonic {
		c.refitIsotonic()
		return
	}
	prior := []float64{0, 1}
	if c.method == bocpd45CalibrationBeta {
		prior = []float64{0, 1, 1}
	}
	theta := append([]float64(nil), prior...)
	// Eight prior-equivalent observations shrink the small online fit toward
	// identity while allowing the 480-label rolling window to dominate.
	const ridge = 8.0
	for iteration := 0; iteration < 12; iteration++ {
		gradient := make([]float64, len(theta))
		hessian := make([][]float64, len(theta))
		for i := range hessian {
			hessian[i] = make([]float64, len(theta))
			gradient[i] = ridge * (theta[i] - prior[i])
			hessian[i][i] = ridge
		}
		for _, sample := range c.samples {
			x := c.features(sample.probability)
			q := logistic(dot(x, theta))
			weight := math.Max(1e-6, q*(1-q))
			for i := range theta {
				gradient[i] += (q - sample.label) * x[i]
				for j := range theta {
					hessian[i][j] += weight * x[i] * x[j]
				}
			}
		}
		step, ok := solveSmallLinearSystem(hessian, gradient)
		if !ok {
			return
		}
		largest := 0.0
		for i := range theta {
			theta[i] -= step[i]
			largest = math.Max(largest, math.Abs(step[i]))
		}
		if largest < 1e-8 {
			break
		}
	}
	for _, value := range theta {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return
		}
	}
	c.theta, c.ready = theta, true
}

func (c *bocpd45RollingCalibrator) refitIsotonic() {
	const bins = 12
	counts := [bins]float64{}
	sums := [bins]float64{}
	for _, sample := range c.samples {
		index := minInt(bins-1, int(sample.probability*bins))
		counts[index]++
		sums[index] += sample.label
	}
	blocks := make([]bocpd45IsotonicBlock, 0, bins)
	for i := 0; i < bins; i++ {
		if counts[i] == 0 {
			continue
		}
		low, high := float64(i)/bins, float64(i+1)/bins
		// Two identity-centred pseudo observations prevent sparse tail bins
		// from becoming exactly zero or one.
		weight := counts[i] + 2
		mean := (sums[i] + low + high) / weight
		blocks = append(blocks, bocpd45IsotonicBlock{low: low, high: high, mean: mean, weight: weight})
		for len(blocks) >= 2 && blocks[len(blocks)-2].mean > blocks[len(blocks)-1].mean {
			right, left := blocks[len(blocks)-1], blocks[len(blocks)-2]
			left.high = right.high
			left.mean = (left.mean*left.weight + right.mean*right.weight) / (left.weight + right.weight)
			left.weight += right.weight
			blocks = append(blocks[:len(blocks)-2], left)
		}
	}
	if len(blocks) > 0 {
		c.blocks, c.ready = blocks, true
	}
}

func solveSmallLinearSystem(matrix [][]float64, rhs []float64) ([]float64, bool) {
	n := len(rhs)
	a := make([][]float64, n)
	for i := range a {
		a[i] = append(append([]float64(nil), matrix[i]...), rhs[i])
	}
	for column := 0; column < n; column++ {
		pivot := column
		for row := column + 1; row < n; row++ {
			if math.Abs(a[row][column]) > math.Abs(a[pivot][column]) {
				pivot = row
			}
		}
		if math.Abs(a[pivot][column]) < 1e-12 {
			return nil, false
		}
		a[column], a[pivot] = a[pivot], a[column]
		for row := column + 1; row < n; row++ {
			factor := a[row][column] / a[column][column]
			for j := column; j <= n; j++ {
				a[row][j] -= factor * a[column][j]
			}
		}
	}
	result := make([]float64, n)
	for row := n - 1; row >= 0; row-- {
		value := a[row][n]
		for j := row + 1; j < n; j++ {
			value -= a[row][j] * result[j]
		}
		result[row] = value / a[row][row]
	}
	return result, true
}

func dot(a, b []float64) float64 {
	total := 0.0
	for i := range a {
		total += a[i] * b[i]
	}
	return total
}

func logistic(value float64) float64 {
	if value >= 0 {
		z := math.Exp(-value)
		return 1 / (1 + z)
	}
	z := math.Exp(value)
	return z / (1 + z)
}

func clampProbability(value float64) float64 {
	return math.Max(1e-9, math.Min(1-1e-9, value))
}

type bocpd45PendingLabel struct {
	maturesAt        time.Time
	startBid         float64
	startAsk         float64
	rawProbability   float64
	usedProbability  float64
	evaluationAnchor bool
}

type bocpd45PrequentialCalibration struct {
	calibrator *bocpd45RollingCalibrator
	pending    *bocpd45PendingLabel
	nextAnchor time.Time
	matured    int
}

func newBOCPD45PrequentialCalibration(method bocpd45CalibrationMethod) *bocpd45PrequentialCalibration {
	return &bocpd45PrequentialCalibration{calibrator: newBOCPD45RollingCalibrator(method)}
}

// observe first matures an older label against the current executable BBO and
// only then snapshots a new prediction. This ordering is strictly prequential:
// a prediction can never train on its own future label.
func (c *bocpd45PrequentialCalibration) observe(book bboSnapshot, raw bocpd45DirectionSnapshot, gap bool, evaluate bool) (matured *bocpd45PendingLabel, label float64, labeled bool) {
	if gap {
		c.pending = nil
		c.nextAnchor = book.time
	}
	if c.pending != nil && !book.time.Before(c.pending.maturesAt) {
		pending := c.pending
		c.pending = nil
		move := 0.5 * (math.Log(book.ask/pending.startAsk) + math.Log(book.bid/pending.startBid))
		if move != 0 {
			label = 0
			if move > 0 {
				label = 1
			}
			matured, labeled = pending, true
			c.calibrator.update(pending.rawProbability, label)
			c.matured++
		}
	}
	if c.pending == nil && raw.ready && (c.nextAnchor.IsZero() || !book.time.Before(c.nextAnchor)) {
		c.pending = &bocpd45PendingLabel{
			maturesAt: book.time.Add(bocpd45ExpectedRunLength), startBid: book.bid, startAsk: book.ask,
			rawProbability: raw.upProbability, usedProbability: c.calibrator.predict(raw.upProbability),
			evaluationAnchor: evaluate,
		}
		c.nextAnchor = book.time.Add(bocpd45ExpectedRunLength)
	}
	return matured, label, labeled
}

func (c *bocpd45PrequentialCalibration) predict(rawProbability float64) float64 {
	if c == nil {
		return clampProbability(rawProbability)
	}
	return c.calibrator.predict(rawProbability)
}
