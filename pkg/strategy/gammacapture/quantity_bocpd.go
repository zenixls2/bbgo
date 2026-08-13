//go:build ignore

package gammacapture

import (
	"math"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

// QuantityBOCPDConfig controls a bounded Beta-Bernoulli Bayesian online
// changepoint detector for the executable ask and bid price directions. The
// detector owns no extra side multiplier: its posterior target is passed to the
// existing probability-centered Fast quantity solver.
type QuantityBOCPDConfig struct {
	Enabled                bool           `json:"enabled" yaml:"enabled"`
	ShadowOnly             bool           `json:"shadowOnly" yaml:"shadowOnly"`
	ExpectedRunLength      types.Duration `json:"expectedRunLength" yaml:"expectedRunLength"`
	MinimumPriceChanges    int            `json:"minimumPriceChanges" yaml:"minimumPriceChanges"`
	MaximumRunLengthStates int            `json:"maximumRunLengthStates" yaml:"maximumRunLengthStates"`
}

func (c *QuantityBOCPDConfig) setDefaults(fastWindow time.Duration) {
	if c.ExpectedRunLength <= 0 {
		c.ExpectedRunLength = types.Duration(fastWindow)
	}
	if c.MinimumPriceChanges <= 0 {
		c.MinimumPriceChanges = 8
	}
	if c.MaximumRunLengthStates <= 0 {
		c.MaximumRunLengthStates = 128
	}
}

type quantityBOCPDRunState struct {
	RunLength   int
	Probability float64
	Up          float64
	Down        float64
}

type quantityBOCPDSide struct {
	States     []quantityBOCPDRunState
	LastChange time.Time
	Changes    int
}

func (s *quantityBOCPDSide) reset() {
	s.States = nil
	s.LastChange = time.Time{}
	s.Changes = 0
}

// observe performs the Adams-MacKay BOCPD message update for a Bernoulli sign
// observation. Beta(1,1) is the neutral conjugate prior. The hazard is based
// on elapsed wall time rather than event count, which avoids treating a busy
// BBO feed as a faster-changing economic regime.
func (s *quantityBOCPDSide) observe(at time.Time, up bool, expectedRunLength time.Duration, maxStates int) {
	if at.IsZero() || expectedRunLength <= 0 || maxStates <= 0 {
		return
	}
	if !s.LastChange.IsZero() && !at.After(s.LastChange) {
		return
	}
	if len(s.States) == 0 {
		s.States = []quantityBOCPDRunState{{
			RunLength: 1, Probability: 1,
			Up: 1 + boolFloat(up), Down: 1 + boolFloat(!up),
		}}
		s.LastChange = at
		s.Changes = 1
		return
	}
	delta := at.Sub(s.LastChange)
	hazard := 1 - math.Exp(-float64(delta)/float64(expectedRunLength))
	hazard = math.Max(1e-9, math.Min(1-1e-9, hazard))
	const priorPredictive = 0.5
	next := make([]quantityBOCPDRunState, 1, minBOCPDInt(maxStates, len(s.States)+1))
	changeMass := 0.0
	for _, state := range s.States {
		predictiveUp := state.Up / (state.Up + state.Down)
		predictive := predictiveUp
		if !up {
			predictive = 1 - predictiveUp
		}
		changeMass += state.Probability * hazard * priorPredictive
		if len(next) < maxStates {
			next = append(next, quantityBOCPDRunState{
				RunLength:   state.RunLength + 1,
				Probability: state.Probability * (1 - hazard) * predictive,
				Up:          state.Up + boolFloat(up), Down: state.Down + boolFloat(!up),
			})
		}
	}
	next[0] = quantityBOCPDRunState{
		RunLength: 1, Probability: changeMass,
		Up: 1 + boolFloat(up), Down: 1 + boolFloat(!up),
	}
	total := 0.0
	for _, state := range next {
		total += state.Probability
	}
	if total <= 0 || math.IsNaN(total) || math.IsInf(total, 0) {
		s.reset()
		return
	}
	for index := range next {
		next[index].Probability /= total
	}
	s.States = next
	s.LastChange = at
	s.Changes++
}

func boolFloat(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

func minBOCPDInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

type quantityBOCPDSideDecision struct {
	Ready             bool
	UpProbability     float64
	ChangeProbability float64
	ExpectedRunLength float64
	Changes           int
}

func (s *quantityBOCPDSide) decision(minimumChanges int) quantityBOCPDSideDecision {
	d := quantityBOCPDSideDecision{UpProbability: 0.5, Changes: s.Changes}
	if len(s.States) == 0 {
		return d
	}
	for _, state := range s.States {
		d.UpProbability += state.Probability * (state.Up/(state.Up+state.Down) - 0.5)
		d.ExpectedRunLength += state.Probability * float64(state.RunLength)
		if state.RunLength == 1 {
			d.ChangeProbability += state.Probability
		}
	}
	d.UpProbability = math.Max(0, math.Min(1, d.UpProbability))
	d.Ready = s.Changes >= minimumChanges
	return d
}

// QuantityBOCPDModel keeps separate executable-side posteriors. Ask changes
// describe the price a BUY must eventually pay; bid changes describe the price
// at which a SELL can execute.
type QuantityBOCPDModel struct {
	LastBid float64
	LastAsk float64
	Bid     quantityBOCPDSide
	Ask     quantityBOCPDSide
}

func (m *QuantityBOCPDModel) Reset() {
	*m = QuantityBOCPDModel{}
}

func (m *QuantityBOCPDModel) Observe(at time.Time, bid, ask float64, gapBefore bool, config QuantityBOCPDConfig) {
	if m == nil || !config.Enabled || at.IsZero() || bid <= 0 || ask < bid {
		return
	}
	config.setDefaults(10 * time.Minute)
	if gapBefore {
		m.Reset()
	}
	if m.LastBid > 0 && bid != m.LastBid {
		m.Bid.observe(at, bid > m.LastBid, time.Duration(config.ExpectedRunLength), config.MaximumRunLengthStates)
	}
	if m.LastAsk > 0 && ask != m.LastAsk {
		m.Ask.observe(at, ask > m.LastAsk, time.Duration(config.ExpectedRunLength), config.MaximumRunLengthStates)
	}
	m.LastBid, m.LastAsk = bid, ask
}

type QuantityBOCPDDecision struct {
	Enabled              bool
	Ready                bool
	Applied              bool
	Reason               string
	UpProbability        float64
	AskUpProbability     float64
	BidUpProbability     float64
	AskChangeProbability float64
	BidChangeProbability float64
	AskExpectedRunLength float64
	BidExpectedRunLength float64
	AskChanges           int
	BidChanges           int
	OriginalTargetBase   float64
	PosteriorTargetBase  float64
}

func (m *QuantityBOCPDModel) Decision(config QuantityBOCPDConfig) QuantityBOCPDDecision {
	d := QuantityBOCPDDecision{Reason: "disabled", UpProbability: 0.5,
		AskUpProbability: 0.5, BidUpProbability: 0.5}
	if m == nil || !config.Enabled {
		return d
	}
	config.setDefaults(10 * time.Minute)
	d.Enabled = true
	ask := m.Ask.decision(config.MinimumPriceChanges)
	bid := m.Bid.decision(config.MinimumPriceChanges)
	d.AskUpProbability, d.BidUpProbability = ask.UpProbability, bid.UpProbability
	d.AskChangeProbability, d.BidChangeProbability = ask.ChangeProbability, bid.ChangeProbability
	d.AskExpectedRunLength, d.BidExpectedRunLength = ask.ExpectedRunLength, bid.ExpectedRunLength
	d.AskChanges, d.BidChanges = ask.Changes, bid.Changes
	if !ask.Ready || !bid.Ready {
		d.Reason = "insufficient executable-side price changes"
		return d
	}
	// Equal side weight is intentional: the ask and bid are two executable
	// observations of the same latent direction, and neither side is allowed to
	// dominate merely because it updates more often.
	d.UpProbability = 0.5 * (ask.UpProbability + bid.UpProbability)
	d.Ready = true
	d.Reason = "executable-side changepoint posterior"
	return d
}

// ApplyQuantityBOCPDTarget maps the directional posterior to the already
// computed Fast inventory interval. This is Bayesian model averaging over the
// lower and upper inventory states; no second quantity multiplier is applied.
func ApplyQuantityBOCPDTarget(
	decision QuantityBOCPDDecision,
	currentTarget, lower, upper float64,
	shadowOnly bool,
) (float64, QuantityBOCPDDecision) {
	decision.OriginalTargetBase = currentTarget
	decision.PosteriorTargetBase = currentTarget
	if !decision.Ready || upper < lower || !inventoryProjectionFinite(lower) ||
		!inventoryProjectionFinite(upper) || !inventoryProjectionFinite(currentTarget) {
		return currentTarget, decision
	}
	probability := math.Max(0, math.Min(1, decision.UpProbability))
	decision.PosteriorTargetBase = lower + probability*(upper-lower)
	if shadowOnly {
		decision.Reason += " (shadow)"
		return currentTarget, decision
	}
	decision.Applied = true
	return decision.PosteriorTargetBase, decision
}
func checkpointQuantityBOCPDSide(side quantityBOCPDSide) quantityBOCPDSideCheckpoint {
	checkpoint := quantityBOCPDSideCheckpoint{
		LastChange: side.LastChange,
		Changes:    side.Changes,
		States:     make([]quantityBOCPDRunCheckpoint, 0, len(side.States)),
	}
	for _, state := range side.States {
		checkpoint.States = append(checkpoint.States, quantityBOCPDRunCheckpoint{
			RunLength: state.RunLength, Probability: state.Probability,
			Up: state.Up, Down: state.Down,
		})
	}
	return checkpoint
}

func checkpointQuantityBOCPD(model QuantityBOCPDModel) quantityBOCPDCheckpoint {
	return quantityBOCPDCheckpoint{
		LastBid: model.LastBid, LastAsk: model.LastAsk,
		Bid: checkpointQuantityBOCPDSide(model.Bid),
		Ask: checkpointQuantityBOCPDSide(model.Ask),
	}
}

func restoreQuantityBOCPDSide(checkpoint quantityBOCPDSideCheckpoint) quantityBOCPDSide {
	side := quantityBOCPDSide{
		LastChange: checkpoint.LastChange,
		Changes:    checkpoint.Changes,
		States:     make([]quantityBOCPDRunState, 0, len(checkpoint.States)),
	}
	for _, state := range checkpoint.States {
		if state.RunLength <= 0 || state.Probability < 0 || state.Up <= 0 || state.Down <= 0 {
			continue
		}
		side.States = append(side.States, quantityBOCPDRunState{
			RunLength: state.RunLength, Probability: state.Probability,
			Up: state.Up, Down: state.Down,
		})
	}
	return side
}

func restoreQuantityBOCPD(checkpoint quantityBOCPDCheckpoint) QuantityBOCPDModel {
	return QuantityBOCPDModel{
		LastBid: checkpoint.LastBid, LastAsk: checkpoint.LastAsk,
		Bid: restoreQuantityBOCPDSide(checkpoint.Bid),
		Ask: restoreQuantityBOCPDSide(checkpoint.Ask),
	}
}
func quantityPosteriorLogit(probability float64) float64 {
	const epsilon = 1e-6
	probability = math.Max(epsilon, math.Min(1-epsilon, probability))
	return math.Log(probability / (1 - probability))
}

func quantityPosteriorLogistic(logOdds float64) float64 {
	if logOdds >= 0 {
		exponential := math.Exp(-logOdds)
		return 1 / (1 + exponential)
	}
	exponential := math.Exp(logOdds)
	return exponential / (1 + exponential)
}

// FuseQuantityBOCPDWithHawkes treats the Hawkes up/down intensity odds as a
// tempered prior and the BOCPD sign posterior odds (whose prior is 1:1) as the
// run-length likelihood ratio. Hawkes Confidence is entirely data-derived and
// prevents a low-intensity burst from receiving full prior strength.
func FuseQuantityBOCPDWithHawkes(
	decision QuantityBOCPDDecision,
	hawkes HawkesDirectionSnapshot,
) QuantityBOCPDDecision {
	if !decision.Ready || !hawkes.Ready || hawkes.Total <= 0 ||
		hawkes.LambdaUp < 0 || hawkes.LambdaDown < 0 {
		return decision
	}
	hawkesUpProbability := hawkes.LambdaUp / hawkes.Total
	confidence := math.Max(0, math.Min(1, hawkes.Confidence))
	logOdds := quantityPosteriorLogit(decision.UpProbability) +
		confidence*quantityPosteriorLogit(hawkesUpProbability)
	decision.UpProbability = quantityPosteriorLogistic(logOdds)
	return decision
}

// QuantityPosteriorFromHawkes exposes the Hawkes intensity posterior for
// research ablation while preserving the same target-mapping interface.
func QuantityPosteriorFromHawkes(hawkes HawkesDirectionSnapshot) QuantityBOCPDDecision {
	decision := QuantityBOCPDDecision{
		Enabled: true, Reason: "Hawkes intensity posterior",
		UpProbability: 0.5, AskUpProbability: 0.5, BidUpProbability: 0.5,
	}
	if !hawkes.Ready || hawkes.Total <= 0 || hawkes.LambdaUp < 0 || hawkes.LambdaDown < 0 {
		return decision
	}
	decision.Ready = true
	decision.UpProbability = math.Max(0, math.Min(1, hawkes.LambdaUp/hawkes.Total))
	return decision
}
