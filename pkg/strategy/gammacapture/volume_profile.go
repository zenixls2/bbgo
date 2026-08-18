package gammacapture

import (
	"math"
	"sort"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

const volumeProfileFeatureCount = 5

// VolumeProfileConfig defines a bounded exponentially weighted public-trade
// profile. BinWidthBps is scale invariant; MaxBins is a hard memory bound.
type VolumeProfileConfig struct {
	Enabled    bool `json:"enabled" yaml:"enabled"`
	ShadowOnly bool `json:"shadowOnly" yaml:"shadowOnly"`
	// AsymmetricPOCRisk applies the bounded, side-specific terminal-payoff
	// adjustment derived from the signed POC distance and local aggressive
	// flow. It is deliberately opt-in so existing live profiles are unchanged
	// until the replay candidate has passed validation.
	AsymmetricPOCRisk  bool           `json:"asymmetricPOCRisk" yaml:"asymmetricPOCRisk"`
	HalfLife           types.Duration `json:"halfLife" yaml:"halfLife"`
	BinWidthBps        float64        `json:"binWidthBps" yaml:"binWidthBps"`
	MaxBins            int            `json:"maxBins" yaml:"maxBins"`
	KernelWeight       float64        `json:"kernelWeight" yaml:"kernelWeight"`
	MinEffectiveTrades float64        `json:"minEffectiveTrades" yaml:"minEffectiveTrades"`
}

func (c VolumeProfileConfig) normalized() VolumeProfileConfig {
	if c.HalfLife <= 0 {
		c.HalfLife = types.Duration(45 * time.Minute)
	}
	if c.BinWidthBps <= 0 {
		c.BinWidthBps = 1
	}
	if c.MaxBins < 16 {
		c.MaxBins = 512
	}
	if c.KernelWeight <= 0 {
		c.KernelWeight = 1
	}
	if c.KernelWeight > 4 {
		c.KernelWeight = 4
	}
	if c.MinEffectiveTrades <= 0 {
		c.MinEffectiveTrades = 8
	}
	return c
}

const maxVolumeProfileSnapshots = 4

type volumeProfileSnapshot struct {
	Horizon time.Duration
	State   VolumeProfileState
}

type volumeProfileBin struct {
	buy, sell float64
	count     float64
}

// VolumeProfileState is a causal compact state. Distances are signed in bps;
// density and flow are bounded. CorridorPosition is -1 at the nearest lower
// volume node and +1 at the nearest upper node.
type VolumeProfileState struct {
	Valid               bool
	POCDistanceBps      float64
	LocalDensityRatio   float64
	LocalFlowImbalance  float64
	CentroidDistanceBps float64
	ProfileScaleBps     float64
	CorridorPosition    float64
	EffectiveTrades     float64
	Bins                int
	KernelWeight        float64
}

// SideTerminalRiskMoments returns the mean and variance of a bounded adverse
// terminal-payoff component for one side. POCDistanceBps is current-price
// minus POC: a negative value means the POC is overhead (resistance), while a
// positive value means it is below current price (support). Only flow that
// agrees with the resistance/support interpretation contributes:
//
//	BUY:  overhead POC × aggressive seller flow
//	SELL: below-price POC × aggressive buyer flow
//
// If L is the bounded loss magnitude and p is the adverse-flow strength, the
// component is a Bernoulli loss: E[Loss]=pL and Var(Loss)=p(1-p)L². This keeps
// the adjustment in the same terminal-payoff units used by the unified Fast
// model and avoids a second hard gate.
func (s VolumeProfileState) SideTerminalRiskMoments(buy bool) (meanBps, varianceBps2 float64) {
	return s.SideTerminalRiskMomentsWithZScore(buy, 1)
}

// SideTerminalRiskMomentsWithZScore uses the existing inventory confidence
// level to cap the profile-implied adverse move. A z-score of one reproduces
// the profile's one-sigma bound; the production risk model can use its
// already-calibrated confidence level without introducing a new coefficient.
func (s VolumeProfileState) SideTerminalRiskMomentsWithZScore(buy bool, zScore float64) (meanBps, varianceBps2 float64) {
	if !s.Valid || s.ProfileScaleBps <= 0 || !finiteVolumeProfileValue(s.POCDistanceBps) ||
		!finiteVolumeProfileValue(s.LocalDensityRatio) || !finiteVolumeProfileValue(s.LocalFlowImbalance) {
		return 0, 0
	}
	distance := 0.0
	flow := 0.0
	if buy {
		// POC above current price is represented by a negative signed distance.
		distance = math.Max(0, -s.POCDistanceBps)
		flow = math.Max(0, -s.LocalFlowImbalance)
	} else {
		// POC below current price is represented by a positive signed distance.
		distance = math.Max(0, s.POCDistanceBps)
		flow = math.Max(0, s.LocalFlowImbalance)
	}
	density := math.Max(0, math.Min(1, s.LocalDensityRatio))
	zScore = math.Max(1, zScore)
	lossMagnitude := math.Min(zScore*s.ProfileScaleBps, distance) * density
	p := math.Min(1, flow)
	return p * lossMagnitude, p * (1 - p) * lossMagnitude * lossMagnitude
}

// SideTerminalRiskPenaltyBps is the expected adverse terminal payoff. It is
// retained as a diagnostic API for callers that only need the mean.
func (s VolumeProfileState) SideTerminalRiskPenaltyBps(buy bool) float64 {
	mean, _ := s.SideTerminalRiskMoments(buy)
	return mean
}

func finiteVolumeProfileValue(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

// Vector returns the exact side-reflected feature vector used by a conditional
// model. BUY is the natural orientation; SELL reflects price and flow.
func (s VolumeProfileState) Vector(buy bool) [volumeProfileFeatureCount]float64 {
	if !s.Valid {
		return [volumeProfileFeatureCount]float64{}
	}
	sign := 1.0
	if !buy {
		sign = -1
	}
	scale := math.Max(1, s.ProfileScaleBps)
	return [volumeProfileFeatureCount]float64{
		math.Tanh(sign * s.POCDistanceBps / scale),
		math.Max(0, math.Min(1, s.LocalDensityRatio)),
		math.Max(-1, math.Min(1, sign*s.LocalFlowImbalance)),
		math.Tanh(sign * s.CentroidDistanceBps / scale),
		math.Max(-1, math.Min(1, sign*s.CorridorPosition)),
	}
}

// RollingVolumeProfile keeps no raw events. A global lazy scale applies time
// decay in O(1); an O(B) renormalization is needed only after extreme decay.
type RollingVolumeProfile struct {
	config    VolumeProfileConfig
	bins      map[int64]volumeProfileBin
	lastAt    time.Time
	scale     float64
	totalRaw  float64
	countRaw  float64
	lastPrice float64
}

func NewRollingVolumeProfile(config VolumeProfileConfig) *RollingVolumeProfile {
	config = config.normalized()
	return &RollingVolumeProfile{config: config, bins: make(map[int64]volumeProfileBin, config.MaxBins), scale: 1}
}

// configureVolumeProfiles creates one bounded profile per Fast reference
// window. Keeping the profiles separate prevents a short-horizon liquidity
// pulse from being silently reused as a long-horizon state.
func (m *MarketMakerHorizonModel) configureVolumeProfiles(c MarketMakerConfig) {
	if m == nil {
		return
	}
	config := c.VolumeProfile
	if !config.Enabled {
		m.volumeProfiles = nil
		m.volumeProfileWindows = nil
		m.volumeProfileConfig = VolumeProfileConfig{}
		return
	}
	config = config.normalized()
	windows := c.FastModelWindows()
	if len(windows) > maxVolumeProfileSnapshots {
		windows = windows[:maxVolumeProfileSnapshots]
	}
	if m.volumeProfiles != nil && m.volumeProfileConfig == config &&
		len(m.volumeProfileWindows) == len(windows) {
		same := true
		for index := range windows {
			if m.volumeProfileWindows[index] != windows[index] {
				same = false
				break
			}
		}
		if same {
			return
		}
	}
	m.volumeProfiles = make(map[time.Duration]*RollingVolumeProfile, len(windows))
	m.volumeProfileWindows = append([]time.Duration(nil), windows...)
	m.volumeProfileConfig = config
	for _, window := range windows {
		m.volumeProfiles[window] = NewRollingVolumeProfile(config)
	}
}

func (m *MarketMakerHorizonModel) resetVolumeProfiles() {
	if m == nil || len(m.volumeProfileWindows) == 0 {
		return
	}
	m.volumeProfiles = make(map[time.Duration]*RollingVolumeProfile, len(m.volumeProfileWindows))
	for _, window := range m.volumeProfileWindows {
		m.volumeProfiles[window] = NewRollingVolumeProfile(m.volumeProfileConfig)
	}
}

// ObservePublicTrade is the only public-trade input to Volume Profile. It is
// deliberately separate from BBO observation so a trade cannot rewrite a
// historical BBO point and introduce same-timestamp look-ahead.
func (m *MarketMakerHorizonModel) ObservePublicTrade(at time.Time, price, quantity float64, buyerInitiated bool, c MarketMakerConfig) {
	if m == nil || !c.VolumeProfile.Enabled || at.IsZero() {
		return
	}
	m.configureVolumeProfiles(c)
	for _, window := range m.volumeProfileWindows {
		if profile := m.volumeProfiles[window]; profile != nil {
			profile.Observe(at, price, quantity, buyerInitiated)
		}
	}
}

func (m *MarketMakerHorizonModel) volumeProfileSnapshots(price float64) ([maxVolumeProfileSnapshots]volumeProfileSnapshot, uint8) {
	var snapshots [maxVolumeProfileSnapshots]volumeProfileSnapshot
	if m == nil || price <= 0 || len(m.volumeProfileWindows) == 0 {
		return snapshots, 0
	}
	var count uint8
	for _, window := range m.volumeProfileWindows {
		if int(count) >= len(snapshots) {
			break
		}
		profile := m.volumeProfiles[window]
		if profile == nil {
			continue
		}
		snapshots[count] = volumeProfileSnapshot{Horizon: window, State: profile.Snapshot(price)}
		count++
	}
	return snapshots, count
}

func (p *RollingVolumeProfile) binIndex(price float64) int64 {
	return int64(math.Round(math.Log(price) * 10_000 / p.config.BinWidthBps))
}

func (p *RollingVolumeProfile) decayTo(at time.Time) {
	if p.lastAt.IsZero() || !at.After(p.lastAt) {
		return
	}
	halfLife := time.Duration(p.config.HalfLife)
	p.scale *= math.Exp(-math.Ln2 * at.Sub(p.lastAt).Seconds() / halfLife.Seconds())
	p.lastAt = at
	if p.scale >= 1e-80 {
		return
	}
	for index, bin := range p.bins {
		bin.buy *= p.scale
		bin.sell *= p.scale
		bin.count *= p.scale
		p.bins[index] = bin
	}
	p.totalRaw *= p.scale
	p.countRaw *= p.scale
	p.scale = 1
}

// Observe adds one public aggregate trade. buyerInitiated denotes aggressive
// buyer flow. Existing-bin updates allocate nothing.
func (p *RollingVolumeProfile) Observe(at time.Time, price, quantity float64, buyerInitiated bool) {
	if p == nil || at.IsZero() || price <= 0 || quantity <= 0 || math.IsNaN(price) || math.IsNaN(quantity) {
		return
	}
	if !p.lastAt.IsZero() && at.Before(p.lastAt) {
		return
	}
	if p.lastAt.IsZero() {
		p.lastAt = at
	} else {
		p.decayTo(at)
	}
	index := p.binIndex(price)
	if _, found := p.bins[index]; !found && len(p.bins) >= p.config.MaxBins {
		p.pruneLightestBin()
	}
	weight := quantity / p.scale
	bin := p.bins[index]
	if buyerInitiated {
		bin.buy += weight
	} else {
		bin.sell += weight
	}
	bin.count += 1 / p.scale
	p.bins[index] = bin
	p.totalRaw += weight
	p.countRaw += 1 / p.scale
	p.lastPrice = price
}

func (p *RollingVolumeProfile) pruneLightestBin() {
	var lightest int64
	minimum := math.Inf(1)
	found := false
	for index, bin := range p.bins {
		mass := bin.buy + bin.sell
		if mass < minimum || (mass == minimum && (!found || index < lightest)) {
			minimum, lightest, found = mass, index, true
		}
	}
	if !found {
		return
	}
	bin := p.bins[lightest]
	p.totalRaw -= bin.buy + bin.sell
	p.countRaw -= bin.count
	delete(p.bins, lightest)
}

func (p *RollingVolumeProfile) smoothedMass(index int64) float64 {
	center := p.bins[index]
	left := p.bins[index-1]
	right := p.bins[index+1]
	return 0.5*(center.buy+center.sell) + 0.25*(left.buy+left.sell+right.buy+right.sell)
}

// Snapshot is O(B) and is intended for the statistical model clock (one
// minute), not every BBO event. It does not allocate and never mutates bins.
func (p *RollingVolumeProfile) Snapshot(price float64) VolumeProfileState {
	if p == nil {
		return VolumeProfileState{}
	}
	state := VolumeProfileState{Bins: len(p.bins), KernelWeight: p.config.KernelWeight}
	if price <= 0 || len(p.bins) == 0 || p.countRaw*p.scale < p.config.MinEffectiveTrades || p.totalRaw <= 0 {
		return state
	}
	current := p.binIndex(price)
	pocIndex := current
	pocMass := 0.0
	centroidNumerator := 0.0
	centroidSquaredNumerator := 0.0
	localBuy, localSell := 0.0, 0.0
	lowerNode, upperNode := int64(0), int64(0)
	lowerFound, upperFound := false, false
	indices := make([]int64, 0, len(p.bins))
	for index := range p.bins {
		indices = append(indices, index)
	}
	sort.Slice(indices, func(i, j int) bool { return indices[i] < indices[j] })
	for _, index := range indices {
		bin := p.bins[index]
		mass := bin.buy + bin.sell
		centroidNumerator += float64(index) * mass
		centroidSquaredNumerator += float64(index*index) * mass
		smoothed := p.smoothedMass(index)
		// Resolve equal-mass POC ties by the lower price bin. Map iteration is
		// intentionally unordered; without this tie-break, paired replays can
		// choose different POC distances and produce different quote paths.
		if smoothed > pocMass || (smoothed == pocMass && index < pocIndex) {
			pocMass, pocIndex = smoothed, index
		}
		if index >= current-1 && index <= current+1 {
			localBuy += bin.buy
			localSell += bin.sell
		}
		if smoothed < p.smoothedMass(index-1) || smoothed < p.smoothedMass(index+1) {
			continue
		}
		if index < current && (!lowerFound || index > lowerNode) {
			lowerNode, lowerFound = index, true
		}
		if index > current && (!upperFound || index < upperNode) {
			upperNode, upperFound = index, true
		}
	}
	if pocMass <= 0 {
		return state
	}
	localMass := p.smoothedMass(current)
	centroid := centroidNumerator / p.totalRaw
	profileVariance := math.Max(0, centroidSquaredNumerator/p.totalRaw-centroid*centroid)
	profileScaleBps := math.Max(p.config.BinWidthBps, math.Sqrt(profileVariance)*p.config.BinWidthBps)
	flowTotal := localBuy + localSell
	corridor := 0.0
	if lowerFound && upperFound && upperNode > lowerNode {
		corridor = 2*(float64(current-lowerNode)/float64(upperNode-lowerNode)) - 1
	} else {
		corridor = math.Tanh(float64(current-pocIndex) * p.config.BinWidthBps / profileScaleBps)
	}
	flow := 0.0
	if flowTotal > 0 {
		flow = (localBuy - localSell) / flowTotal
	}
	state.Valid = true
	state.POCDistanceBps = float64(current-pocIndex) * p.config.BinWidthBps
	state.LocalDensityRatio = math.Max(0, math.Min(1, localMass/pocMass))
	state.LocalFlowImbalance = math.Max(-1, math.Min(1, flow))
	state.CentroidDistanceBps = (float64(current) - centroid) * p.config.BinWidthBps
	state.ProfileScaleBps = profileScaleBps
	state.CorridorPosition = math.Max(-1, math.Min(1, corridor))
	state.EffectiveTrades = p.countRaw * p.scale
	return state
}

func (p *RollingVolumeProfile) BinCount() int {
	if p == nil {
		return 0
	}
	return len(p.bins)
}
