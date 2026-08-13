//go:build ignore

package gammacapture

import (
	"math"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/fixedpoint"
	"github.com/c9s/bbgo/pkg/types"
)

func TestHawkesDirectionUsesSubcriticalCausalExcitation(t *testing.T) {
	start := time.Unix(0, 0)
	m := NewHawkesDirectionModel(HawkesDirectionConfig{
		Enabled: true, HalfLife: types.Duration(time.Minute),
		SelfExcitation: 0.35, CrossExcitation: 0.05, MinEvents: 2,
	})
	for i := 0; i < 3; i++ {
		m.ObserveTrade(start.Add(time.Duration(i)*time.Second), types.Trade{
			Price: fixedpoint.NewFromFloat(100), Quantity: fixedpoint.NewFromFloat(1),
			QuoteQuantity: fixedpoint.NewFromFloat(100), Side: types.SideTypeBuy,
		})
	}
	got := m.Snapshot(start.Add(3 * time.Second))
	if !got.Ready || got.Direction <= 0 || got.LambdaUp <= got.LambdaDown {
		t.Fatalf("expected ready upward Hawkes direction, got %+v", got)
	}
	if got.Confidence <= 0 || got.Confidence > 1 || got.Direction > 1 {
		t.Fatalf("invalid bounded Hawkes confidence/direction: %+v", got)
	}
	decayed := m.Snapshot(start.Add(20 * time.Minute))
	if math.Abs(decayed.Direction) >= math.Abs(got.Direction) {
		t.Fatalf("excitation did not decay causally: near=%+v far=%+v", got, decayed)
	}
}

func TestHawkesDirectionCrossExcitationDoesNotCreatePermanentBias(t *testing.T) {
	start := time.Unix(0, 0)
	m := NewHawkesDirectionModel(HawkesDirectionConfig{Enabled: true, HalfLife: types.Duration(time.Minute), MinEvents: 1})
	m.ObserveDirection(start, true, 100)
	up := m.Snapshot(start.Add(time.Second))
	if up.Direction == 0 {
		t.Fatal("single upward event should produce a signed intensity")
	}
	balanced := m.Snapshot(start.Add(30 * time.Minute))
	if balanced.Total >= up.Total || balanced.Confidence >= up.Confidence {
		t.Fatalf("stale Hawkes burst retained too much intensity: near=%+v far=%+v", up, balanced)
	}
}
