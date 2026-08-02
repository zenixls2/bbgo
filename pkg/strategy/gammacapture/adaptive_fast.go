package gammacapture

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

type adaptiveFastSnapshot struct {
	Window        time.Duration
	Model         ModelSnapshot
	Evidence      FastEvidenceSnapshot
	HealthSummary string
}

type FastCrossingActivity string

const (
	FastCrossingUnobserved FastCrossingActivity = "UNOBSERVED"
	FastCrossingQuiet      FastCrossingActivity = "QUIET"
	FastCrossingSparse     FastCrossingActivity = "SPARSE"
	FastCrossingActive     FastCrossingActivity = "ACTIVE"
)

// fastCrossingInference separates observable data coverage from crossing
// activity. In a sparse market, a fully observed window with no crossings is
// information about a low event rate, not missing data.
type fastCrossingInference struct {
	Activity            FastCrossingActivity
	DataHealth          ModelHealth
	RateUsable          bool
	DirectionalActions  bool
	LambdaUp            float64
	LambdaDown          float64
	Total               float64
	Direction           float64
	DirectionConfidence float64
	Observed            time.Duration
	PriorExposure       time.Duration
	RateSource          string
}

// inferFastCrossing applies an online empirical-Bayes update to the selected
// fast window. The slow live rate supplies exactly one pseudo-crossing, making
// its strength data-derived (one divided by the slow total intensity). Up/down
// direction keeps the symmetric Beta(1,1) prior, so a quiet window is neutral
// and old crossings outside the selected window cannot leak into the quote.
func inferFastCrossing(window time.Duration, fast ModelSnapshot, evidence FastEvidenceSnapshot, slow ModelSnapshot) fastCrossingInference {
	result := fastCrossingInference{
		Activity:   FastCrossingUnobserved,
		DataHealth: evidence.Health,
		Observed:   evidence.Observed,
	}
	if evidence.Health != HealthHealthy {
		return result
	}

	events := fast.Up + fast.Down
	switch {
	case events == 0:
		result.Activity = FastCrossingQuiet
	case fast.Health == HealthHealthy:
		result.Activity = FastCrossingActive
	default:
		result.Activity = FastCrossingSparse
	}
	result.DirectionalActions = result.Activity == FastCrossingActive
	result.DirectionConfidence = float64(events) / float64(events+2)
	result.Direction = float64(fast.Up-fast.Down) / float64(events+2)

	observed := evidence.Observed
	if window > 0 && observed > window {
		observed = window
	}
	if observed < time.Second {
		observed = time.Second
	}
	result.Observed = observed

	// Prefer a live slow posterior backed by explicit market-data exposure. One
	// expected slow crossing becomes the conjugate Gamma-Poisson prior mass.
	if slow.Health != HealthInvalid && slow.Observed > 0 &&
		slow.Total > 0 && !math.IsNaN(slow.Total) && !math.IsInf(slow.Total, 0) {
		priorSeconds := 1 / slow.Total
		if slow.Observed > 0 && priorSeconds > slow.Observed.Seconds() {
			priorSeconds = slow.Observed.Seconds()
		}
		if priorSeconds < 1 {
			priorSeconds = 1
		}
		result.PriorExposure = time.Duration(priorSeconds * float64(time.Second))
		result.Total = (1 + float64(events)) / (priorSeconds + observed.Seconds())
		result.RateSource = "slow-empirical-bayes"
		result.RateUsable = true
	} else if events > 0 {
		// Until the slow posterior is available, observed fast events still have
		// a causal MLE. Zero-event windows remain rate-unusable in this branch.
		result.Total = float64(events) / observed.Seconds()
		result.RateSource = "fast-mle"
		result.RateUsable = true
	}
	if result.RateUsable {
		posteriorUp := (1 + float64(fast.Up)) / float64(events+2)
		result.LambdaUp = result.Total * posteriorUp
		result.LambdaDown = result.Total - result.LambdaUp
	}
	return result
}

func (s *Strategy) initializeAdaptiveFastModels() {
	windows := s.MarketMaker.FastModelWindows()
	s.fastModels = make(map[time.Duration]*IntensityModel, len(windows))
	s.fastEvidenceModels = make(map[time.Duration]*FastEvidenceModel, len(windows))
	s.makerDirectionModels = make(map[time.Duration]*DecayedDirectionModel, len(windows))
	for _, window := range windows {
		s.fastModels[window] = NewIntensityModel(s.MarketMaker.fastIntensityConfigFor(window))
		s.makerDirectionModels[window] = NewDecayedDirectionModel(window)
		evidenceWindow := window
		if len(s.MarketMaker.FastWindows) == 0 {
			evidenceWindow = time.Duration(s.MarketMaker.FastEvidenceWindow)
		}
		s.fastEvidenceModels[window] = NewFastEvidenceModel(FastEvidenceConfig{
			Window:        evidenceWindow,
			MinTrades:     s.MarketMaker.FastEvidenceMinTrades,
			MinBBOUpdates: s.MarketMaker.FastEvidenceMinBBOUpdates,
		})
	}
	primary := time.Duration(s.MarketMaker.FastWindow)
	s.fastModel = s.fastModels[primary]
	s.fastEvidence = s.fastEvidenceModels[primary]
	s.makerDirectionModel = s.makerDirectionModels[primary]
	if s.fastModel == nil && len(windows) > 0 {
		primary = windows[0]
		s.fastModel = s.fastModels[primary]
		s.fastEvidence = s.fastEvidenceModels[primary]
		s.makerDirectionModel = s.makerDirectionModels[primary]
	}
}

func (s *Strategy) observeFastModelExposure(at time.Time, gapBefore bool) {
	if len(s.fastModels) > 0 {
		for _, model := range s.fastModels {
			model.Observe(at, gapBefore)
		}
		return
	}
	if s.fastModel != nil {
		s.fastModel.Observe(at, gapBefore)
	}
}

func (s *Strategy) updateFastModels(event CrossingEvent) {
	if len(s.fastModels) > 0 {
		for _, model := range s.fastModels {
			model.Update(event)
		}
		return
	}
	if s.fastModel != nil {
		s.fastModel.Update(event)
	}
}

func (s *Strategy) updateMakerDirectionModels(event CrossingEvent) {
	if len(s.makerDirectionModels) > 0 {
		for _, model := range s.makerDirectionModels {
			model.Update(event)
		}
		return
	}
	if s.makerDirectionModel != nil {
		s.makerDirectionModel.Update(event)
	}
}

func (s *Strategy) makerDirectionSnapshot(window time.Duration, now time.Time) DecayedDirectionSnapshot {
	if model := s.makerDirectionModels[window]; model != nil {
		return model.Snapshot(now)
	}
	if s.makerDirectionModel != nil {
		return s.makerDirectionModel.Snapshot(now)
	}
	return DecayedDirectionSnapshot{PosteriorUp: 0.5}
}

func (s *Strategy) observeFastEvidenceTrade(at time.Time, trade types.Trade) {
	if len(s.fastEvidenceModels) > 0 {
		for _, model := range s.fastEvidenceModels {
			model.ObserveTrade(at, trade)
		}
		return
	}
	if s.fastEvidence != nil {
		s.fastEvidence.ObserveTrade(at, trade)
	}
}

func (s *Strategy) observeFastEvidenceBBO(at time.Time, ticker types.BookTicker) {
	if len(s.fastEvidenceModels) > 0 {
		for _, model := range s.fastEvidenceModels {
			model.ObserveBBO(at, ticker)
		}
		return
	}
	if s.fastEvidence != nil {
		s.fastEvidence.ObserveBBO(at, ticker)
	}
}

func (s *Strategy) maxFastEvidenceWindow() time.Duration {
	maximum := time.Duration(s.MarketMaker.FastEvidenceWindow)
	for _, model := range s.fastEvidenceModels {
		if model != nil && model.window > maximum {
			maximum = model.window
		}
	}
	return maximum
}

func fastHealthRank(health ModelHealth) int {
	switch health {
	case HealthHealthy:
		return 4
	case HealthDegraded:
		return 3
	case HealthInsufficient:
		return 2
	case HealthInvalid:
		return 1
	default:
		return 0
	}
}

func (s *Strategy) adaptiveFastSnapshot(now time.Time) adaptiveFastSnapshot {
	if len(s.fastModels) == 0 {
		model := ModelSnapshot{Health: HealthInsufficient}
		if s.fastModel != nil {
			model = s.fastModel.Snapshot(now)
		}
		evidence := FastEvidenceSnapshot{Health: HealthInsufficient}
		if s.fastEvidence != nil {
			evidence = s.fastEvidence.Snapshot(now)
		}
		window := time.Duration(s.MarketMaker.FastWindow)
		if window <= 0 && s.fastEvidence != nil {
			window = s.fastEvidence.window
		}
		return adaptiveFastSnapshot{
			Window: window, Model: model, Evidence: evidence,
			HealthSummary: fmt.Sprintf("%s=%s/%s", window, model.Health, evidence.Health),
		}
	}

	windows := make([]time.Duration, 0, len(s.fastModels))
	for window := range s.fastModels {
		windows = append(windows, window)
	}
	sort.Slice(windows, func(i, j int) bool { return windows[i] < windows[j] })

	type candidate struct {
		window   time.Duration
		model    ModelSnapshot
		evidence FastEvidenceSnapshot
	}
	candidates := make([]candidate, 0, len(windows))
	summary := make([]string, 0, len(windows))
	selected := -1
	for _, window := range windows {
		model := s.fastModels[window].Snapshot(now)
		evidence := FastEvidenceSnapshot{Health: HealthInsufficient}
		if evidenceModel := s.fastEvidenceModels[window]; evidenceModel != nil {
			evidence = evidenceModel.Snapshot(now)
		}
		candidates = append(candidates, candidate{window: window, model: model, evidence: evidence})
		summary = append(summary, fmt.Sprintf("%s=%s/%s", window, model.Health, evidence.Health))
		if selected < 0 && model.Health == HealthHealthy {
			// The shortest healthy crossing window is the most responsive
			// statistically admissible estimate. Raw evidence health remains a
			// separate gate for volatility and IOC behavior.
			selected = len(candidates) - 1
		}
	}
	if selected < 0 {
		selected = 0
		for index := 1; index < len(candidates); index++ {
			current, best := candidates[index], candidates[selected]
			currentRank, bestRank := fastHealthRank(current.model.Health), fastHealthRank(best.model.Health)
			currentEvents := current.model.Up + current.model.Down
			bestEvents := best.model.Up + best.model.Down
			if currentRank > bestRank ||
				(currentRank == bestRank && currentEvents > bestEvents) ||
				(currentRank == bestRank && currentEvents == bestEvents && current.window > best.window) {
				selected = index
			}
		}
	}
	chosen := candidates[selected]
	return adaptiveFastSnapshot{
		Window: chosen.window, Model: chosen.model, Evidence: chosen.evidence,
		HealthSummary: strings.Join(summary, ","),
	}
}
