package gammacapture

import (
	"math"
	"time"
)

type ModelHealth string

const (
	HealthHealthy      ModelHealth = "HEALTHY"
	HealthDegraded     ModelHealth = "DEGRADED"
	HealthInvalid      ModelHealth = "INVALID"
	HealthInsufficient ModelHealth = "INSUFFICIENT_DATA"
)

type ModelSnapshot struct {
	Up, Down                                            int
	LambdaUp, LambdaDown, Total, DirectionalProbability float64
	GammaCaptureVolatility                              float64
	Health                                              ModelHealth
	Observed                                            time.Duration
	Age                                                 time.Duration
}
type CrossingModel interface {
	Update(CrossingEvent)
	Snapshot(time.Time) ModelSnapshot
	Validate() ModelHealth
}

type IntensityModel struct {
	cfg    IntensityConfig
	events []CrossingEvent
	last   time.Time
}

func NewIntensityModel(cfg IntensityConfig) *IntensityModel { return &IntensityModel{cfg: cfg} }
func (m *IntensityModel) Update(e CrossingEvent) {
	m.events = append(m.events, e)
	m.last = e.ExchangeTime
	m.trim(e.ExchangeTime)
}
func (m *IntensityModel) trim(now time.Time) {
	cut := now.Add(-time.Duration(m.cfg.Window))
	n := 0
	for _, e := range m.events {
		if !e.ExchangeTime.Before(cut) {
			m.events[n] = e
			n++
		}
	}
	m.events = m.events[:n]
}
func (m *IntensityModel) Snapshot(now time.Time) ModelSnapshot {
	m.trim(now)
	var up, down int
	var first time.Time
	for _, e := range m.events {
		if e.GapAffected {
			continue
		}
		if first.IsZero() {
			first = e.ExchangeTime
		}
		if e.Direction == DirectionUp {
			up++
		} else if e.Direction == DirectionDown {
			down++
		}
	}
	d := time.Duration(m.cfg.Window)
	if !first.IsZero() && now.Sub(first) < d {
		d = now.Sub(first)
	}
	seconds := d.Seconds()
	if seconds < 1 {
		seconds = 1
	}
	// Keep intensity rates based on the elapsed observation interval, while
	// regularizing volatility over the configured window when requested. This
	// prevents one sparse event at the start of a fast window from appearing
	// as an instantaneous 10bps shock.
	volatilitySeconds := seconds
	configuredVolatilitySeconds := time.Duration(m.cfg.VolatilityWindow).Seconds()
	if configuredVolatilitySeconds > volatilitySeconds {
		volatilitySeconds = configuredVolatilitySeconds
	}
	lu := (m.cfg.PriorAlphaUp + float64(up)) / (m.cfg.PriorBetaUp + seconds)
	ld := (m.cfg.PriorAlphaDown + float64(down)) / (m.cfg.PriorBetaDown + seconds)
	total := lu + ld
	age := time.Duration(0)
	if !m.last.IsZero() {
		age = now.Sub(m.last)
	}
	s := ModelSnapshot{Up: up, Down: down, LambdaUp: lu, LambdaDown: ld, Total: total, DirectionalProbability: lu / total, GammaCaptureVolatility: math.Sqrt(float64(up+down)/volatilitySeconds) * m.barrierWidth(), Observed: d, Age: age}
	s.Health = m.Validate()
	return s
}
func (m *IntensityModel) barrierWidth() float64 {
	if len(m.events) > 0 {
		return m.events[len(m.events)-1].BarrierWidth
	}
	return 0
}
func (m *IntensityModel) Validate() ModelHealth {
	// Gap-affected events are deliberately excluded from the directional rates.
	// They must not, however, make an otherwise unobserved model appear healthy.
	n := 0
	for _, event := range m.events {
		if !event.GapAffected {
			n++
		}
	}
	if n < m.cfg.MinEvents {
		return HealthInsufficient
	}
	if n < 2*m.cfg.MinEvents {
		return HealthDegraded
	}
	return HealthHealthy
}

type FirstPassage struct{ TP, SL, Unresolved float64 }

// TPBeforeSL uses uniformization of the finite birth-death generator. It is stable
// for short horizons and returns explicit unresolved probability.
func TPBeforeSL(lambdaUp, lambdaDown float64, horizon time.Duration, takeProfit, stopLoss, current int) FirstPassage {
	if takeProfit < 1 || stopLoss < 1 || current >= takeProfit || current <= -stopLoss || horizon <= 0 {
		return FirstPassage{}
	}
	rate := lambdaUp + lambdaDown
	if rate <= 0 {
		return FirstPassage{Unresolved: 1}
	}
	n := takeProfit + stopLoss - 1
	p := make([]float64, n)
	p[current+stopLoss-1] = 1
	upP := lambdaUp / rate
	downP := lambdaDown / rate
	mean := rate * horizon.Seconds()
	weight := math.Exp(-mean) // Poisson probability of zero jumps.
	tp, sl := 0., 0.
	absorbedTP, absorbedSL := 0., 0.
	for jumps := 1; jumps < 10000; jumps++ {
		next := make([]float64, n)
		for i, v := range p {
			if v == 0 {
				continue
			}
			state := i - stopLoss + 1
			if state+1 == takeProfit {
				absorbedTP += v * upP
			} else {
				next[i+1] += v * upP
			}
			if state-1 == -stopLoss {
				absorbedSL += v * downP
			} else {
				next[i-1] += v * downP
			}
		}
		// At each possible Poisson jump count, weight the cumulative absorbing
		// mass. Once reached, an absorbing barrier remains reached for all later
		// jump counts.
		weight *= mean / float64(jumps)
		tp += weight * absorbedTP
		sl += weight * absorbedSL
		p = next
		transient := 0.
		for _, v := range p {
			transient += v
		}
		if weight*(transient+absorbedTP+absorbedSL) < 1e-13 && jumps > int(mean)+20 {
			break
		}
	}
	u := 1 - tp - sl
	if u < 0 && u > -1e-10 {
		u = 0
	}
	return FirstPassage{TP: clamp01(tp), SL: clamp01(sl), Unresolved: clamp01(u)}
}

func SkellamPMF(k int, mu1, mu2 float64) float64 { // Poisson-convolution avoids unstable Bessel calls for modest horizons.
	if mu1 < 0 || mu2 < 0 {
		return math.NaN()
	}
	sum := 0.
	lo := 0
	if k < 0 {
		lo = -k
	}
	for n := lo; n < 10000; n++ {
		a := poissonPMF(n+k, mu1)
		b := poissonPMF(n, mu2)
		sum += a * b
		if a*b < 1e-15 && n > int(mu1+mu2)+20 {
			break
		}
	}
	return sum
}
func poissonPMF(k int, mu float64) float64 {
	if k < 0 {
		return 0
	}
	if mu == 0 {
		if k == 0 {
			return 1
		}
		return 0
	}
	lgamma, _ := math.Lgamma(float64(k) + 1)
	return math.Exp(float64(k)*math.Log(mu) - mu - lgamma)
}
func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
