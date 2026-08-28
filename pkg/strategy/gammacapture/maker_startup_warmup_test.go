package gammacapture

import (
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/fixedpoint"
	"github.com/c9s/bbgo/pkg/types"
)

func TestMakerStartupBBOAccumulatorKeepsLastStateAndGap(t *testing.T) {
	start := time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)
	var got []struct {
		at        time.Time
		bid       float64
		gapBefore bool
	}
	accumulator := makerStartupBBOAccumulator{emit: func(at time.Time, ticker types.BookTicker, gapBefore bool) {
		got = append(got, struct {
			at        time.Time
			bid       float64
			gapBefore bool
		}{at: at, bid: ticker.Buy.Float64(), gapBefore: gapBefore})
	}}
	accumulator.add(start.Add(100*time.Millisecond), types.BookTicker{Buy: fixedpoint.NewFromFloat(100)}, false)
	accumulator.add(start.Add(700*time.Millisecond), types.BookTicker{Buy: fixedpoint.NewFromFloat(101)}, true)
	accumulator.add(start.Add(1200*time.Millisecond), types.BookTicker{Buy: fixedpoint.NewFromFloat(102)}, false)
	accumulator.flush()
	if len(got) != 2 {
		t.Fatalf("expected two warmup buckets, got %d: %+v", len(got), got)
	}
	if got[0].bid != 101 || !got[0].gapBefore || got[0].at != start.Add(700*time.Millisecond) {
		t.Fatalf("first bucket did not keep its last state/gap: %+v", got[0])
	}
	if got[1].bid != 102 || got[1].gapBefore {
		t.Fatalf("second bucket unexpected: %+v", got[1])
	}
}
