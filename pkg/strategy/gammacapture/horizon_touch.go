package gammacapture

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

const HorizonTouchModelVersion = "gammacapture-horizon-touch-v2"

var HorizonTouchFeatureNames = []string{
	"return1mBps",
	"return5mBps",
	"return15mBps",
	"return60mBps",
	"return240mBps",
	"realizedVol15mBps",
	"realizedVol60mBps",
	"realizedVol240mBps",
}

// HorizonTouchModelConfig controls a trained, side-specific first-passage
// model. The model does not replace the common market reference price unless a
// separate fair-price forecast passes its own out-of-sample gate. Version two requires executable-BBO touch labels (ask for BUY, bid for SELL).
// Midpoint-labeled v1 artifacts are rejected because their distance likelihood is
// biased when the observed spread is material.
type HorizonTouchModelConfig struct {
	Enabled                    bool    `json:"enabled" yaml:"enabled"`
	Path                       string  `json:"path" yaml:"path"`
	HistoricalWeight           float64 `json:"historicalWeight" yaml:"historicalWeight"`
	TouchToFillHaircut         float64 `json:"touchToFillHaircut" yaml:"touchToFillHaircut"`
	MinimumBrierImprovementPct float64 `json:"minimumBrierImprovementPct" yaml:"minimumBrierImprovementPct"`
}

func (c *HorizonTouchModelConfig) setDefaults() {
	if c.HistoricalWeight <= 0 || c.HistoricalWeight > 1 {
		c.HistoricalWeight = 0.75
	}
	if c.TouchToFillHaircut <= 0 || c.TouchToFillHaircut > 1 {
		c.TouchToFillHaircut = 0.25
	}
	if c.MinimumBrierImprovementPct <= 0 {
		c.MinimumBrierImprovementPct = 5
	}
}

// HorizonTouchMetrics records the chronological holdout evidence bundled with
// a trained artifact. DB metrics measure long-history one-minute path labels;
// BBO metrics measure a separate event-capture holdout.
type HorizonTouchMetrics struct {
	Samples               int     `json:"samples"`
	BrierBaseline         float64 `json:"brierBaseline"`
	BrierModel            float64 `json:"brierModel"`
	BrierImprovementPct   float64 `json:"brierImprovementPct"`
	LogLossBaseline       float64 `json:"logLossBaseline"`
	LogLossModel          float64 `json:"logLossModel"`
	FairPriceMAEBaseline  float64 `json:"fairPriceMAEBaselineBps,omitempty"`
	FairPriceMAEModel     float64 `json:"fairPriceMAEModelBps,omitempty"`
	FairPriceImprovement  float64 `json:"fairPriceImprovementPct,omitempty"`
	FairPriceShiftEnabled bool    `json:"fairPriceShiftEnabled"`
}

// HorizonTouchCell is a standardized logistic first-passage model at one
// horizon, quote distance, and side. Predict interpolates logits across cells,
// so distance remains continuous in the live quote path.
type HorizonTouchCell struct {
	HorizonMinutes int       `json:"horizonMinutes"`
	DistanceBps    float64   `json:"distanceBps"`
	Side           string    `json:"side"`
	Intercept      float64   `json:"intercept"`
	Coefficients   []float64 `json:"coefficients"`
	TrainingRate   float64   `json:"trainingRate"`
}

// HorizonTouchArtifact is produced by the read-only SQLite/BBO research
// command. It contains no credentials, account state, orders, or private fills.
type HorizonTouchArtifact struct {
	Version          string              `json:"version"`
	PriceBasis       string              `json:"priceBasis"`
	Symbol           string              `json:"symbol"`
	GeneratedAt      time.Time           `json:"generatedAt"`
	TrainFrom        string              `json:"trainFrom"`
	TrainTo          string              `json:"trainTo"`
	FeatureNames     []string            `json:"featureNames"`
	FeatureMeans     []float64           `json:"featureMeans"`
	FeatureScales    []float64           `json:"featureScales"`
	HistoricalWeight float64             `json:"historicalWeight"`
	Accepted         bool                `json:"accepted"`
	AcceptanceReason string              `json:"acceptanceReason"`
	DBHoldout        HorizonTouchMetrics `json:"dbHoldout"`
	BBOHoldout       HorizonTouchMetrics `json:"bboHoldout"`
	Cells            []HorizonTouchCell  `json:"cells"`
}

func LoadHorizonTouchArtifact(path, symbol string, minimumImprovementPct float64) (*HorizonTouchArtifact, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open horizon touch model: %w", err)
	}
	defer file.Close()
	var artifact HorizonTouchArtifact
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&artifact); err != nil {
		return nil, fmt.Errorf("decode horizon touch model: %w", err)
	}
	if err := artifact.Validate(symbol, minimumImprovementPct); err != nil {
		return nil, err
	}
	return &artifact, nil
}

func (a *HorizonTouchArtifact) Validate(symbol string, minimumImprovementPct float64) error {
	if a == nil {
		return fmt.Errorf("horizon touch model is nil")
	}
	if a.Version != HorizonTouchModelVersion {
		return fmt.Errorf("horizon touch model version %q is not supported", a.Version)
	}
	if a.PriceBasis != "executable-bbo" {
		return fmt.Errorf("horizon touch model price basis %q is not executable-bbo", a.PriceBasis)
	}
	if !strings.EqualFold(a.Symbol, symbol) {
		return fmt.Errorf("horizon touch model symbol %q does not match %q", a.Symbol, symbol)
	}
	if !a.Accepted {
		return fmt.Errorf("horizon touch model was not accepted: %s", a.AcceptanceReason)
	}
	if minimumImprovementPct > 0 && a.BBOHoldout.BrierImprovementPct < minimumImprovementPct {
		return fmt.Errorf("horizon touch model BBO improvement %.3f%% is below %.3f%%", a.BBOHoldout.BrierImprovementPct, minimumImprovementPct)
	}
	if len(a.FeatureNames) != len(HorizonTouchFeatureNames) ||
		len(a.FeatureMeans) != len(HorizonTouchFeatureNames) ||
		len(a.FeatureScales) != len(HorizonTouchFeatureNames) {
		return fmt.Errorf("horizon touch model feature dimensions are invalid")
	}
	for i, name := range HorizonTouchFeatureNames {
		if a.FeatureNames[i] != name || !finite(a.FeatureMeans[i]) || !finite(a.FeatureScales[i]) || a.FeatureScales[i] <= 0 {
			return fmt.Errorf("horizon touch feature %d is invalid", i)
		}
	}
	if len(a.Cells) == 0 {
		return fmt.Errorf("horizon touch model has no cells")
	}
	seen := make(map[string]struct{}, len(a.Cells))
	for _, cell := range a.Cells {
		side := strings.ToUpper(cell.Side)
		if cell.HorizonMinutes <= 0 || cell.DistanceBps <= 0 ||
			(side != string(types.SideTypeBuy) && side != string(types.SideTypeSell)) ||
			!finite(cell.Intercept) || len(cell.Coefficients) != len(HorizonTouchFeatureNames) {
			return fmt.Errorf("invalid horizon touch cell: %+v", cell)
		}
		for _, coefficient := range cell.Coefficients {
			if !finite(coefficient) {
				return fmt.Errorf("horizon touch cell has non-finite coefficient")
			}
		}
		key := touchCellKey(side, cell.HorizonMinutes, cell.DistanceBps)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("duplicate horizon touch cell %s", key)
		}
		seen[key] = struct{}{}
	}
	return nil
}

// Predict returns a causal touch probability within horizon. BUY denotes a
// downward path reaching a passive bid; SELL denotes an upward path reaching a
// passive ask.
func (a *HorizonTouchArtifact) Predict(side types.SideType, horizon time.Duration, distanceBps float64, features []float64) (float64, bool) {
	if a == nil || horizon <= 0 || distanceBps <= 0 || len(features) != len(HorizonTouchFeatureNames) {
		return 0, false
	}
	for _, value := range features {
		if !finite(value) {
			return 0, false
		}
	}
	sideName := strings.ToUpper(string(side))
	if sideName != string(types.SideTypeBuy) && sideName != string(types.SideTypeSell) {
		return 0, false
	}
	horizons, distances := a.axes(sideName)
	if len(horizons) == 0 || len(distances) == 0 {
		return 0, false
	}
	targetH := horizon.Minutes()
	h0, h1, hw := interpolationBoundsInt(horizons, targetH)
	d0, d1, dw := interpolationBoundsFloat(distances, distanceBps)
	logit00, ok00 := a.cellLogit(sideName, h0, d0, features)
	logit01, ok01 := a.cellLogit(sideName, h0, d1, features)
	logit10, ok10 := a.cellLogit(sideName, h1, d0, features)
	logit11, ok11 := a.cellLogit(sideName, h1, d1, features)
	if !ok00 || !ok01 || !ok10 || !ok11 {
		return 0, false
	}
	l0 := logit00 + dw*(logit01-logit00)
	l1 := logit10 + dw*(logit11-logit10)
	return clampProbability(sigmoid(l0 + hw*(l1-l0))), true
}

func (a *HorizonTouchArtifact) axes(side string) ([]int, []float64) {
	horizonSet := make(map[int]struct{})
	distanceSet := make(map[float64]struct{})
	for _, cell := range a.Cells {
		if strings.EqualFold(cell.Side, side) {
			horizonSet[cell.HorizonMinutes] = struct{}{}
			distanceSet[cell.DistanceBps] = struct{}{}
		}
	}
	horizons := make([]int, 0, len(horizonSet))
	for horizon := range horizonSet {
		horizons = append(horizons, horizon)
	}
	sort.Ints(horizons)
	distances := make([]float64, 0, len(distanceSet))
	for distance := range distanceSet {
		distances = append(distances, distance)
	}
	sort.Float64s(distances)
	return horizons, distances
}

func (a *HorizonTouchArtifact) cellLogit(side string, horizon int, distance float64, features []float64) (float64, bool) {
	for _, cell := range a.Cells {
		if strings.EqualFold(cell.Side, side) && cell.HorizonMinutes == horizon && math.Abs(cell.DistanceBps-distance) < 1e-9 {
			value := cell.Intercept
			for i, coefficient := range cell.Coefficients {
				value += coefficient * ((features[i] - a.FeatureMeans[i]) / a.FeatureScales[i])
			}
			return value, finite(value)
		}
	}
	return 0, false
}

// BlendTouchProbability combines the long-history conditional model with a
// resolved, recent empirical probability in log-odds space. A missing recent
// estimate leaves the historical model unchanged.
func BlendTouchProbability(historical, recent, historicalWeight float64) float64 {
	historical = clampProbability(historical)
	if recent <= 0 || recent >= 1 || !finite(recent) {
		return historical
	}
	if historicalWeight <= 0 || historicalWeight > 1 {
		historicalWeight = 0.75
	}
	return clampProbability(sigmoid(
		historicalWeight*probabilityLogit(historical) +
			(1-historicalWeight)*probabilityLogit(recent),
	))
}

// TouchProbabilityToRate converts a within-horizon first-passage probability
// to an exponential hazard per hour, then applies a conservative queue/fill
// haircut. Public touch is not an exchange-confirmed fill.
func TouchProbabilityToRate(probability float64, horizon time.Duration, haircut float64) float64 {
	if probability <= 0 || probability >= 1 || horizon <= 0 {
		return 0
	}
	if haircut <= 0 || haircut > 1 {
		haircut = 0.25
	}
	return -math.Log1p(-probability) / horizon.Hours() * haircut
}

// HorizonTouchFeatures derives the same eight causal features used by the
// SQLite trainer from the horizon model's public-price history.
func (m MarketMakerHorizonModel) HorizonTouchFeatures(now time.Time) ([]float64, bool) {
	if now.IsZero() || len(m.points) < 2 {
		return nil, false
	}
	minutePrices := make([]float64, 241)
	for offset := 0; offset <= 240; offset++ {
		target := now.Add(-time.Duration(offset) * time.Minute)
		price, ok := m.priceAtOrBefore(target, 2*time.Minute)
		if ok {
			minutePrices[offset] = price
		}
	}
	if minutePrices[0] <= 0 {
		return nil, false
	}
	features := make([]float64, len(HorizonTouchFeatureNames))
	for i, lag := range []int{1, 5, 15, 60, 240} {
		if minutePrices[lag] <= 0 {
			return nil, false
		}
		features[i] = math.Log(minutePrices[0]/minutePrices[lag]) * 10_000
	}
	for i, window := range []int{15, 60, 240} {
		sumSquares := 0.0
		count := 0
		for offset := 0; offset < window; offset++ {
			current, previous := minutePrices[offset], minutePrices[offset+1]
			if current <= 0 || previous <= 0 {
				continue
			}
			value := math.Log(current/previous) * 10_000
			sumSquares += value * value
			count++
		}
		if count < int(math.Ceil(0.8*float64(window))) {
			return nil, false
		}
		features[5+i] = math.Sqrt(sumSquares / float64(count))
	}
	return features, true
}

func (m MarketMakerHorizonModel) priceAtOrBefore(target time.Time, maximumAge time.Duration) (float64, bool) {
	index := sort.Search(len(m.points), func(i int) bool { return m.points[i].At.After(target) }) - 1
	if index < 0 || m.points[index].Mid <= 0 || target.Sub(m.points[index].At) < 0 || target.Sub(m.points[index].At) > maximumAge {
		return 0, false
	}
	return m.points[index].Mid, true
}

func interpolationBoundsInt(values []int, target float64) (int, int, float64) {
	if target <= float64(values[0]) {
		return values[0], values[0], 0
	}
	last := values[len(values)-1]
	if target >= float64(last) {
		return last, last, 0
	}
	index := sort.Search(len(values), func(i int) bool { return float64(values[i]) >= target })
	lower, upper := values[index-1], values[index]
	return lower, upper, (target - float64(lower)) / float64(upper-lower)
}

func interpolationBoundsFloat(values []float64, target float64) (float64, float64, float64) {
	if target <= values[0] {
		return values[0], values[0], 0
	}
	last := values[len(values)-1]
	if target >= last {
		return last, last, 0
	}
	index := sort.SearchFloat64s(values, target)
	lower, upper := values[index-1], values[index]
	return lower, upper, (target - lower) / (upper - lower)
}

func touchCellKey(side string, horizon int, distance float64) string {
	return fmt.Sprintf("%s:%d:%.8f", strings.ToUpper(side), horizon, distance)
}

func sigmoid(value float64) float64 {
	if value >= 0 {
		z := math.Exp(-value)
		return 1 / (1 + z)
	}
	z := math.Exp(value)
	return z / (1 + z)
}

func probabilityLogit(value float64) float64 {
	value = clampProbability(value)
	return math.Log(value / (1 - value))
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

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
