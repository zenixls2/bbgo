package gammacapture

import (
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

func executableCrossingTestModel() *ExecutableCrossingModel {
	return NewExecutableCrossingModel("ETHJPY", BarrierConfig{Width: 0.001, MaxCrossingsPerEvent: 8}, IntensityConfig{
		Window: types.Duration(time.Hour), PriorAlphaUp: 1, PriorBetaUp: 60,
		PriorAlphaDown: 1, PriorBetaDown: 60, MinEvents: 1,
	})
}

func TestExecutableCrossingUsesBidUpAndAskDown(t *testing.T) {
	start := time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC)
	model := executableCrossingTestModel()
	model.Observe(start, 100, 100.02, false)
	model.Observe(start.Add(time.Second), 100.11, 100.13, false)
	up := model.Snapshot(start.Add(time.Second))
	if up.Up != 1 || up.Down != 0 {
		t.Fatalf("rising executable book must confirm only bid-up evidence: %+v", up)
	}

	model.Observe(start.Add(2*time.Second), 99.98, 100.00, false)
	both := model.Snapshot(start.Add(2 * time.Second))
	if both.Down == 0 {
		t.Fatalf("falling best ask must confirm buy-side/down evidence: %+v", both)
	}
}

func TestExecutableCrossingRejectsSpreadWideningAsDirection(t *testing.T) {
	start := time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC)
	model := executableCrossingTestModel()
	model.Observe(start, 100, 100.02, false)
	model.Observe(start.Add(time.Second), 99.8, 100.22, false)
	snapshot := model.Snapshot(start.Add(time.Second))
	if snapshot.Up != 0 || snapshot.Down != 0 {
		t.Fatalf("outward spread move is neither executable bid-up nor ask-down evidence: %+v", snapshot)
	}
}

func TestExecutableCrossingGapResetsWithoutInventingPath(t *testing.T) {
	start := time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC)
	model := executableCrossingTestModel()
	model.Observe(start, 100, 100.02, false)
	model.Observe(start.Add(10*time.Minute), 102, 102.02, true)
	snapshot := model.Snapshot(start.Add(10 * time.Minute))
	if snapshot.Up != 0 || snapshot.Down != 0 {
		t.Fatalf("capture gap manufactured executable crossings: %+v", snapshot)
	}
}

func TestConservativeConfirmedDirectionUsesPosteriorIntersection(t *testing.T) {
	micro, executable, confirmed := ConservativeConfirmedDirection(30, 10, 22, 14, 20)
	if micro <= 0 || executable <= 0 || confirmed != executable {
		t.Fatalf("weaker agreeing posterior must bound confirmation: micro=%v executable=%v confirmed=%v", micro, executable, confirmed)
	}
	_, _, disagree := ConservativeConfirmedDirection(30, 10, 8, 28, 20)
	if disagree != 0 {
		t.Fatalf("disagreeing posteriors must identify neutral direction: %v", disagree)
	}
}
