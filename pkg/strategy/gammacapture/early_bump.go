package gammacapture

import (
	"math"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

// EarlyBumpConfig controls the maker-only response to a statistically
// supported rebound after a drawdown. The counts and thresholds are model
// artifact values produced by chronological research; they are not learned
// from the same live episode that they authorize.
type EarlyBumpConfig struct {
	Enabled    bool `json:"enabled" yaml:"enabled"`
	ShadowOnly bool `json:"shadowOnly" yaml:"shadowOnly"`

	MinimumDrawdownBps float64 `json:"minimumDrawdownBps" yaml:"minimumDrawdownBps"`
	// MinimumDrawdown5mBps is a decode-compatible fallback for older profiles.
	// New profiles apply MinimumDrawdownBps to the selected 10/15/30m evidence window.
	MinimumDrawdown5mBps float64 `json:"minimumDrawdown5mBps,omitempty" yaml:"minimumDrawdown5mBps,omitempty"`
	MinimumRebound30sBps float64 `json:"minimumRebound30sBps" yaml:"minimumRebound30sBps"`
	ExitRebound30sBps    float64 `json:"exitRebound30sBps" yaml:"exitRebound30sBps"`
	SelectedDeltaBps     float64 `json:"selectedDeltaBps" yaml:"selectedDeltaBps"`

	ActivationSuccesses int `json:"activationSuccesses" yaml:"activationSuccesses"`
	ActivationSamples   int `json:"activationSamples" yaml:"activationSamples"`
	BaselineSuccesses   int `json:"baselineSuccesses" yaml:"baselineSuccesses"`
	BaselineSamples     int `json:"baselineSamples" yaml:"baselineSamples"`
	MinimumSamples      int `json:"minimumSamples" yaml:"minimumSamples"`

	ConfidenceZScore       float64 `json:"confidenceZScore" yaml:"confidenceZScore"`
	MinimumProbabilityLift float64 `json:"minimumProbabilityLift" yaml:"minimumProbabilityLift"`
	MinimumBBO             int     `json:"minimumBBO" yaml:"minimumBBO"`
	// MinimumBBO5m is retained only for older YAML profiles.
	MinimumBBO5m int `json:"minimumBBO5m,omitempty" yaml:"minimumBBO5m,omitempty"`

	LockDuration types.Duration `json:"lockDuration" yaml:"lockDuration"`
	Cooldown     types.Duration `json:"cooldown" yaml:"cooldown"`
}

func (c *EarlyBumpConfig) setDefaults() {
	if c.MinimumDrawdownBps <= 0 {
		c.MinimumDrawdownBps = c.MinimumDrawdown5mBps
		if c.MinimumDrawdownBps <= 0 {
			c.MinimumDrawdownBps = 15
		}
	}
	if c.MinimumBBO <= 0 {
		c.MinimumBBO = c.MinimumBBO5m
		if c.MinimumBBO <= 0 {
			c.MinimumBBO = 20
		}
	}
	if c.ConfidenceZScore <= 0 {
		c.ConfidenceZScore = 1.959963984540054
	}
	if c.MinimumSamples <= 0 {
		c.MinimumSamples = 30
	}
	if c.LockDuration <= 0 {
		c.LockDuration = types.Duration(30 * time.Second)
	}
	if c.Cooldown <= 0 {
		c.Cooldown = types.Duration(2 * time.Minute)
	}
	if c.ExitRebound30sBps <= 0 && c.MinimumRebound30sBps > 0 {
		c.ExitRebound30sBps = c.MinimumRebound30sBps * 0.5
	}
}

type EarlyBumpPhase string

const (
	EarlyBumpIdle     EarlyBumpPhase = "IDLE"
	EarlyBumpLocked   EarlyBumpPhase = "LOCKED"
	EarlyBumpCooldown EarlyBumpPhase = "COOLDOWN"
)

type EarlyBumpState struct {
	Phase         EarlyBumpPhase
	LockedBid     float64
	EpisodeLowMid float64
	ActivatedAt   time.Time
	LockUntil     time.Time
	CooldownUntil time.Time
}

type EarlyBumpInput struct {
	Now                   time.Time
	MidPrice              float64
	BestBid               float64
	BestAsk               float64
	BaseBidPrice          float64
	MinimumBidDistanceBps float64
	InventoryDeficit      bool
	CanBuy                bool
	BuyHeadroomJPY        float64
	MinimumNotional       float64
	Evidence              FastEvidenceSnapshot
}

type EarlyBumpDecision struct {
	Phase                    EarlyBumpPhase
	Signal                   bool
	Apply                    bool
	Refresh                  bool
	Transition               bool
	BidPrice                 float64
	DeltaBps                 float64
	ActivationProbability    float64
	ActivationProbabilityLow float64
	BaselineProbability      float64
	BaselineProbabilityHigh  float64
	Reason                   string
}

func wilsonBounds(successes, samples int, z float64) (float64, float64) {
	if samples <= 0 || successes < 0 || successes > samples || z <= 0 {
		return 0, 1
	}
	n := float64(samples)
	p := float64(successes) / n
	z2 := z * z
	denominator := 1 + z2/n
	center := (p + z2/(2*n)) / denominator
	half := z * math.Sqrt((p*(1-p)+z2/(4*n))/n) / denominator
	return math.Max(0, center-half), math.Min(1, center+half)
}

func (c EarlyBumpConfig) statisticalSupport() (probability, lower, baseline, baselineUpper float64, ok bool) {
	c.setDefaults()
	if c.ActivationSamples < c.MinimumSamples || c.BaselineSamples < c.MinimumSamples ||
		c.ActivationSuccesses < 0 || c.ActivationSuccesses > c.ActivationSamples ||
		c.BaselineSuccesses < 0 || c.BaselineSuccesses > c.BaselineSamples {
		return 0, 0, 0, 1, false
	}
	probability = float64(c.ActivationSuccesses) / float64(c.ActivationSamples)
	baseline = float64(c.BaselineSuccesses) / float64(c.BaselineSamples)
	lower, _ = wilsonBounds(c.ActivationSuccesses, c.ActivationSamples, c.ConfidenceZScore)
	_, baselineUpper = wilsonBounds(c.BaselineSuccesses, c.BaselineSamples, c.ConfidenceZScore)
	ok = lower > baselineUpper+c.MinimumProbabilityLift
	return
}

func selectedWindowDrawdown(evidence FastEvidenceSnapshot) (float64, time.Duration) {
	if evidence.MidDrawdownWindow > 0 {
		return evidence.MidDrawdownBps, evidence.MidDrawdownWindow
	}
	return evidence.MidDrawdown5mBps, 5 * time.Minute
}

func (c EarlyBumpConfig) signal(in EarlyBumpInput) EarlyBumpDecision {
	c.setDefaults()
	d := EarlyBumpDecision{Phase: EarlyBumpIdle, Reason: "disabled"}
	if !c.Enabled {
		return d
	}
	p, low, base, baseHigh, supported := c.statisticalSupport()
	d.ActivationProbability = p
	d.ActivationProbabilityLow = low
	d.BaselineProbability = base
	d.BaselineProbabilityHigh = baseHigh
	if !supported {
		d.Reason = "activation lift lacks confidence-bounded support"
		return d
	}
	if in.Evidence.Health != HealthHealthy || in.Evidence.BBOCount < c.MinimumBBO {
		d.Reason = "selected-window BBO evidence not healthy"
		return d
	}
	if !in.InventoryDeficit {
		d.Reason = "inventory is not below target"
		return d
	}
	if !in.CanBuy {
		d.Reason = "normal bid is not eligible"
		return d
	}
	if in.BuyHeadroomJPY < in.MinimumNotional {
		d.Reason = "insufficient inventory headroom"
		return d
	}
	drawdownBps, _ := selectedWindowDrawdown(in.Evidence)
	if drawdownBps < c.MinimumDrawdownBps {
		d.Reason = "selected-window drawdown below model threshold"
		return d
	}
	if in.Evidence.MidRebound30sBps < c.MinimumRebound30sBps {
		d.Reason = "thirty-second rebound below model threshold"
		return d
	}
	if in.BaseBidPrice <= 0 || in.BestBid <= 0 || in.BestAsk <= in.BestBid {
		d.Reason = "invalid passive quote"
		return d
	}
	d.Signal = true
	d.DeltaBps = math.Max(0, c.SelectedDeltaBps)
	d.BidPrice = math.Min(in.BestBid, in.BaseBidPrice*math.Exp(d.DeltaBps/10_000))
	if in.MidPrice > 0 && in.MinimumBidDistanceBps > 0 {
		feeSafeBid := in.MidPrice * math.Exp(-in.MinimumBidDistanceBps/10_000)
		d.BidPrice = math.Min(d.BidPrice, feeSafeBid)
	}
	if d.BidPrice >= in.BestAsk {
		d.BidPrice = in.BestBid
	}
	if d.BidPrice <= in.BaseBidPrice {
		d.Signal = false
		d.Reason = "selected delta cannot improve passive bid"
		return d
	}
	d.Reason = "confidence-bounded escape lift"
	return d
}

// Update implements one amend per bump episode. Once active, the absolute bid
// is locked; subsequent upward BBO moves do not lift it again. A new low or
// loss of rebound confirmation aborts the urgency quote and starts cooldown.
func (s *EarlyBumpState) Update(c EarlyBumpConfig, in EarlyBumpInput) EarlyBumpDecision {
	c.setDefaults()
	if s.Phase == "" {
		s.Phase = EarlyBumpIdle
	}
	if !c.Enabled {
		*s = EarlyBumpState{Phase: EarlyBumpIdle}
		return EarlyBumpDecision{Phase: EarlyBumpIdle, Reason: "disabled"}
	}
	if s.Phase == EarlyBumpCooldown {
		if !in.Now.Before(s.CooldownUntil) {
			*s = EarlyBumpState{Phase: EarlyBumpIdle}
		} else {
			return EarlyBumpDecision{Phase: s.Phase, Reason: "cooldown"}
		}
	}
	if s.Phase == EarlyBumpLocked {
		eligibilityLost := !in.InventoryDeficit || !in.CanBuy ||
			in.BuyHeadroomJPY < in.MinimumNotional
		renewedDownside := in.MidPrice <= 0 || in.BestAsk <= s.LockedBid ||
			(s.EpisodeLowMid > 0 && in.MidPrice < s.EpisodeLowMid) ||
			in.Evidence.MidRebound30sBps < c.ExitRebound30sBps
		abort := eligibilityLost || renewedDownside
		expired := !in.Now.Before(s.LockUntil)
		if abort || expired {
			reason := "urgency lock expired"
			if renewedDownside {
				reason = "urgency invalidated by renewed downside"
			} else if eligibilityLost {
				reason = "urgency eligibility lost"
			}
			s.Phase = EarlyBumpCooldown
			s.LockedBid = 0
			s.CooldownUntil = in.Now.Add(time.Duration(c.Cooldown))
			return EarlyBumpDecision{Phase: s.Phase, Refresh: !c.ShadowOnly, Transition: true, Reason: reason}
		}
		return EarlyBumpDecision{
			Phase: s.Phase, Signal: true, Apply: !c.ShadowOnly,
			BidPrice: s.LockedBid, DeltaBps: c.SelectedDeltaBps,
			Reason: "holding one-shot urgency bid",
		}
	}
	d := c.signal(in)
	d.Phase = s.Phase
	if !d.Signal {
		return d
	}
	s.Phase = EarlyBumpLocked
	s.LockedBid = d.BidPrice
	s.EpisodeLowMid = in.Evidence.MidLow30s
	s.ActivatedAt = in.Now
	s.LockUntil = in.Now.Add(time.Duration(c.LockDuration))
	d.Phase = s.Phase
	d.Transition = true
	d.Apply = !c.ShadowOnly
	d.Refresh = !c.ShadowOnly
	if c.ShadowOnly {
		d.Reason = "shadow confidence-bounded escape lift"
	}
	return d
}

func applyEarlyBumpBid(plan MarketMakerQuotePlan, decision EarlyBumpDecision, mid, bestAsk float64) MarketMakerQuotePlan {
	if !decision.Apply || !plan.AllowBid || decision.BidPrice <= plan.BidPrice ||
		decision.BidPrice >= bestAsk || mid <= 0 {
		return plan
	}
	plan.BidPrice = decision.BidPrice
	plan.BidDistanceBps = math.Max(0, math.Log(mid/decision.BidPrice)*10_000)
	plan.BidTouchDistanceBps = math.Max(0, math.Log(bestAsk/decision.BidPrice)*10_000)
	plan.BidHalfSpreadBps = plan.BidDistanceBps
	plan.HalfSpreadBps = math.Max(plan.BidHalfSpreadBps, plan.AskHalfSpreadBps)
	return plan
}
