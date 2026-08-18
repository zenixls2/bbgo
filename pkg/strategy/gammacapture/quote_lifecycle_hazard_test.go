package gammacapture

import (
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

func TestQuoteLifecycleHazardCoalescesToFiveMinuteReviews(t *testing.T) {
	at := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	m := NewQuoteLifecycleHazardModel(QuoteLifecycleHazardConfig{
		Enabled: true, UpdateInterval: types.Duration(5 * time.Minute), AgeBucket: types.Duration(5 * time.Minute),
	})
	if !m.Observe(at, QuoteLifecycleHazardBuy, 10, 0, true) {
		t.Fatal("first observation was rejected")
	}
	if m.Observe(at.Add(time.Minute), QuoteLifecycleHazardBuy, 10, 0, false) {
		t.Fatal("sub-five-minute observation was not coalesced")
	}
	if !m.Observe(at.Add(5*time.Minute), QuoteLifecycleHazardBuy, 10, 0, false) {
		t.Fatal("five-minute observation was rejected")
	}
	s := m.Snapshot(QuoteLifecycleHazardBuy, 10, 0, 5*time.Minute)
	if !s.Ready || s.EffectiveSamples != 2 || s.Probability <= 0 || s.Probability >= 1 {
		t.Fatalf("unexpected hazard snapshot: %+v", s)
	}
}

func TestQuoteLifecycleHazardConditionsOnAgeBucket(t *testing.T) {
	at := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	m := NewQuoteLifecycleHazardModel(QuoteLifecycleHazardConfig{
		Enabled: true, UpdateInterval: types.Duration(5 * time.Minute), AgeBucket: types.Duration(5 * time.Minute),
	})
	for i := 0; i < 8; i++ {
		m.Observe(at.Add(time.Duration(i)*5*time.Minute), QuoteLifecycleHazardBuy, 10, 0, true)
		m.Observe(at.Add(time.Duration(i)*5*time.Minute), QuoteLifecycleHazardSell, 10, 10*time.Minute, false)
	}
	fresh := m.Snapshot(QuoteLifecycleHazardBuy, 10, 0, 5*time.Minute)
	aged := m.Snapshot(QuoteLifecycleHazardSell, 10, 10*time.Minute, 5*time.Minute)
	if fresh.Probability <= aged.Probability {
		t.Fatalf("age-conditioned hazard did not distinguish buckets: fresh=%+v aged=%+v", fresh, aged)
	}
}
