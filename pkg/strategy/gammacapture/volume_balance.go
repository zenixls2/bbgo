package gammacapture

import (
	"math"
	"sort"
	"time"
)

type VolumeBalanceState string

const (
	VolumeBalanceNormal      VolumeBalanceState = "NORMAL"
	VolumeShockAbsorption    VolumeBalanceState = "SHOCK_ABSORPTION"
	VolumeBalanceRebalancing VolumeBalanceState = "BALANCING"
)

// VolumeBalanceSnapshot is a causal, confidence-weighted auxiliary signal. It
// approaches zero until enough buckets exist to estimate a local baseline.
type VolumeBalanceSnapshot struct {
	State                   VolumeBalanceState
	ShockScore              float64
	AbsorptionScore         float64
	BalanceProgress         float64
	SignedPressure          float64
	Signal                  float64
	Confidence              float64
	VolumeZ                 float64
	VolumeRatio             float64
	PriceImpactBps          float64
	ContinuationProbability float64
	ReversalProbability     float64
	Buckets                 int
	Age                     time.Duration
}

type volumeBalanceBucket struct {
	start    time.Time
	notional float64
	signed   float64
	midSum   float64
	midCount int
}

func clampVolumeBalance(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

func medianVolumeBalance(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	v := append([]float64(nil), values...)
	sort.Float64s(v)
	m := len(v) / 2
	if len(v)%2 == 1 {
		return v[m]
	}
	return (v[m-1] + v[m]) / 2
}

// ComputeVolumeBalance detects abnormal notional bursts with low price impact,
// then follows volume and signed-flow normalization. It uses only observations
// at or before now, so it is safe for live use and chronological replay.
func ComputeVolumeBalance(trades []fastEvidenceTrade, bbo []fastEvidenceBBO, now time.Time, window time.Duration) VolumeBalanceSnapshot {
	result := VolumeBalanceSnapshot{State: VolumeBalanceNormal, ContinuationProbability: 0.5, ReversalProbability: 0.5}
	if now.IsZero() || window <= 0 {
		return result
	}
	const bucketWidth = 30 * time.Second
	cutoff := now.Add(-window)
	bucketsByKey := make(map[int64]*volumeBalanceBucket)
	keyFor := func(at time.Time) int64 { return at.Unix() / int64(bucketWidth/time.Second) }
	getBucket := func(at time.Time) *volumeBalanceBucket {
		key := keyFor(at)
		bucket := bucketsByKey[key]
		if bucket == nil {
			bucket = &volumeBalanceBucket{start: time.Unix(key*int64(bucketWidth/time.Second), 0)}
			bucketsByKey[key] = bucket
		}
		return bucket
	}
	for _, trade := range trades {
		if trade.at.Before(cutoff) || trade.at.After(now) || trade.notional <= 0 {
			continue
		}
		bucket := getBucket(trade.at)
		bucket.notional += trade.notional
		bucket.signed += trade.signed
	}
	for _, point := range bbo {
		if point.at.Before(cutoff) || point.at.After(now) || point.mid <= 0 {
			continue
		}
		bucket := getBucket(point.at)
		bucket.midSum += point.mid
		bucket.midCount++
	}
	buckets := make([]volumeBalanceBucket, 0, len(bucketsByKey))
	for _, bucket := range bucketsByKey {
		if bucket.notional == 0 && bucket.midCount == 0 {
			continue
		}
		buckets = append(buckets, *bucket)
	}
	sort.Slice(buckets, func(i, j int) bool { return buckets[i].start.Before(buckets[j].start) })
	result.Buckets = len(buckets)
	if len(buckets) < 6 {
		return result
	}
	for i := range buckets {
		if buckets[i].midCount > 0 {
			buckets[i].midSum /= float64(buckets[i].midCount)
		}
	}
	latest := buckets[len(buckets)-1]
	baseline := make([]float64, 0, len(buckets)-1)
	for _, bucket := range buckets[:len(buckets)-1] {
		if bucket.notional > 0 {
			baseline = append(baseline, math.Log1p(bucket.notional))
		}
	}
	if len(baseline) < 4 || latest.notional <= 0 {
		return result
	}
	center := medianVolumeBalance(baseline)
	deviations := make([]float64, len(baseline))
	for i, value := range baseline {
		deviations[i] = math.Abs(value - center)
	}
	scale := 1.4826 * medianVolumeBalance(deviations)
	if scale < 0.05 {
		scale = 0.05
	}
	result.VolumeZ = (math.Log1p(latest.notional) - center) / scale
	medianNotional := math.Expm1(center)
	if medianNotional > 0 {
		result.VolumeRatio = latest.notional / medianNotional
	}
	if latest.notional > 0 {
		result.SignedPressure = clampVolumeBalance(latest.signed/latest.notional, -1, 1)
	}
	if latest.midSum > 0 {
		for i := len(buckets) - 2; i >= 0; i-- {
			if buckets[i].midSum > 0 {
				result.PriceImpactBps = math.Log(latest.midSum/buckets[i].midSum) * 10000
				break
			}
		}
	}
	impactSamples := make([]float64, 0, len(buckets)-2)
	for i := 1; i < len(buckets)-1; i++ {
		if buckets[i].midSum > 0 && buckets[i-1].midSum > 0 {
			impactSamples = append(impactSamples, math.Abs(math.Log(buckets[i].midSum/buckets[i-1].midSum)*10000))
		}
	}
	impactScale := medianVolumeBalance(impactSamples)
	if impactScale < 1 {
		impactScale = 1
	}
	lowImpact := math.Exp(-math.Abs(result.PriceImpactBps) / (2 * impactScale))
	shockLikelihood := 1 / (1 + math.Exp(-1.2*(result.VolumeZ-2.5)))
	result.AbsorptionScore = clampVolumeBalance(shockLikelihood*lowImpact, 0, 1)
	result.ShockScore = clampVolumeBalance(shockLikelihood, 0, 1)
	volumeBalance := math.Exp(-math.Abs(math.Log(math.Max(result.VolumeRatio, 1e-9))))
	imbalanceBalance := 1 - math.Abs(result.SignedPressure)
	result.BalanceProgress = clampVolumeBalance(0.5*volumeBalance+0.5*imbalanceBalance, 0, 1)

	peakSign := 0.0
	peakZ := math.Inf(-1)
	for i := 0; i < len(buckets)-1; i++ {
		if buckets[i].notional <= 0 {
			continue
		}
		z := (math.Log1p(buckets[i].notional) - center) / scale
		if z > peakZ {
			peakZ = z
			if buckets[i].signed != 0 {
				peakSign = math.Copysign(1, buckets[i].signed)
			}
		}
	}
	if peakZ >= 2.5 && result.BalanceProgress >= 0.35 && result.VolumeRatio < 2 {
		result.State = VolumeBalanceRebalancing
	} else if result.AbsorptionScore >= 0.5 {
		result.State = VolumeShockAbsorption
	}
	if result.State == VolumeBalanceRebalancing && peakSign != 0 && result.SignedPressure*peakSign < -0.1 {
		result.Signal = -peakSign * result.BalanceProgress * result.ShockScore
	} else if result.State == VolumeBalanceRebalancing {
		result.Signal = result.SignedPressure * result.BalanceProgress * result.ShockScore
	}
	result.Confidence = clampVolumeBalance(float64(len(baseline))/20, 0, 1) * result.ShockScore
	result.Signal *= result.Confidence
	result.ContinuationProbability = clampVolumeBalance(0.5+0.5*result.Signal, 0, 1)
	result.ReversalProbability = 1 - result.ContinuationProbability
	result.Age = now.Sub(latest.start)
	return result
}
