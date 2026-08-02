package gammacapture

import (
	"testing"
	"time"
)

func TestComputeVolumeBalanceDetectsShockAbsorption(t *testing.T) {
	now := time.Unix(600, 0)
	trades := make([]fastEvidenceTrade, 0, 10)
	bbo := make([]fastEvidenceBBO, 0, 20)
	for i := 0; i < 10; i++ {
		at := time.Unix(int64(i*30+15), 0)
		n := 100.0
		if i == 9 {
			n = 1200
		}
		trades = append(trades, fastEvidenceTrade{at: at, notional: n, signed: n})
		bbo = append(bbo, fastEvidenceBBO{at: at, mid: 12000})
	}
	got := ComputeVolumeBalance(trades, bbo, now, 10*time.Minute)
	if got.State != VolumeShockAbsorption {
		t.Fatalf("state=%s", got.State)
	}
	if got.ShockScore < 0.5 || got.AbsorptionScore < 0.5 {
		t.Fatalf("shock=%g absorption=%g", got.ShockScore, got.AbsorptionScore)
	}
	if got.Signal != 0 {
		t.Fatalf("absorption should not emit a directional signal: %g", got.Signal)
	}
}

func TestComputeVolumeBalanceDetectsRebalancingReversal(t *testing.T) {
	now := time.Unix(600, 0)
	trades := make([]fastEvidenceTrade, 0, 10)
	bbo := make([]fastEvidenceBBO, 0, 20)
	for i := 0; i < 10; i++ {
		at := time.Unix(int64(i*30+15), 0)
		n := 100.0
		signed := 0.0
		if i == 7 {
			n, signed = 1200, 1200
		}
		if i == 9 {
			n, signed = 140, -100
		}
		trades = append(trades, fastEvidenceTrade{at: at, notional: n, signed: signed})
		bbo = append(bbo, fastEvidenceBBO{at: at, mid: 12000})
	}
	got := ComputeVolumeBalance(trades, bbo, now, 10*time.Minute)
	if got.State != VolumeBalanceRebalancing {
		t.Fatalf("state=%s", got.State)
	}
	if got.Signal >= 0 {
		t.Fatalf("expected reversal signal, got %g", got.Signal)
	}
}
