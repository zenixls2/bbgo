package gammacapture

import (
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

func TestPrivateFillCalibrationMaturesExecutableAdverseLabel(t *testing.T) {
	config := PrivateFillCalibrationConfig{
		Enabled: true, Horizon: types.Duration(time.Minute), HalfLife: types.Duration(time.Hour),
		MinimumFills: 1, MinimumTouchObservations: 1,
		MaxAdverseSelectionBps: 500,
	}
	model := NewPrivateFillCalibrationModel(config)
	at := time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)
	model.ObserveFill(PrivateFillCalibrationObservation{
		At: at, TradeID: 7, Side: types.SideTypeBuy, Price: 100,
	})
	model.ObserveFill(PrivateFillCalibrationObservation{
		At: at, TradeID: 7, Side: types.SideTypeBuy, Price: 100,
	})
	model.ObserveBBO(at.Add(time.Minute), 99, 101)

	snapshot := model.SnapshotAt(at.Add(time.Minute))
	if !snapshot.Ready || snapshot.Fills != 1 || snapshot.EffectiveFills != 1 {
		t.Fatalf("expected one matured deduplicated fill: %+v", snapshot)
	}
	if snapshot.MeanAdverseSelectionBps <= 0 || snapshot.RecommendedAdverseSelectionBps < snapshot.MeanAdverseSelectionBps {
		t.Fatalf("expected conservative positive BUY adverse-selection estimate: %+v", snapshot)
	}
}

func TestPrivateFillCalibrationTracksTouchToFillAndStaleness(t *testing.T) {
	config := PrivateFillCalibrationConfig{
		Enabled: true, Horizon: types.Duration(time.Minute), HalfLife: types.Duration(time.Hour),
		MinimumFills: 1, MinimumTouchObservations: 1,
	}
	model := NewPrivateFillCalibrationModel(config)
	at := time.Date(2026, 8, 26, 1, 0, 0, 0, time.UTC)
	model.ObserveOrder(11, types.SideTypeSell, 101, at)
	model.ObserveBBO(at.Add(10*time.Second), 101.1, 101.5)
	model.ObserveOrderEnd(11, at.Add(20*time.Second), true)

	snapshot := model.SnapshotAt(at.Add(20 * time.Second))
	if !snapshot.TouchReady || snapshot.TouchObservations != 1 || snapshot.TouchFills != 1 {
		t.Fatalf("expected one touched and filled order: %+v", snapshot)
	}
	if snapshot.TouchToFillProbability <= 0 || snapshot.RecommendedTouchToFillHaircut <= 0 {
		t.Fatalf("expected positive touch-to-fill estimate: %+v", snapshot)
	}
	stale := model.SnapshotAt(at.Add(3 * time.Hour))
	if !stale.Stale || stale.Ready || stale.TouchReady {
		t.Fatalf("expected stale calibration to be unusable: %+v", stale)
	}
}

func TestPrivateFillCalibrationCheckpointRoundTrip(t *testing.T) {
	config := PrivateFillCalibrationConfig{
		Enabled: true, Horizon: types.Duration(time.Minute), HalfLife: types.Duration(time.Hour),
		MinimumFills: 1,
	}
	model := NewPrivateFillCalibrationModel(config)
	at := time.Date(2026, 8, 26, 2, 0, 0, 0, time.UTC)
	model.ObserveFill(PrivateFillCalibrationObservation{
		At: at, TradeID: 19, Side: types.SideTypeSell, Price: 100,
	})
	checkpoint := model.checkpoint()
	restored := NewPrivateFillCalibrationModel(config)
	if err := restored.restore(checkpoint); err != nil {
		t.Fatal(err)
	}
	restored.ObserveBBO(at.Add(time.Minute), 99, 101)
	if got := restored.SnapshotAt(at.Add(time.Minute)); got.Fills != 1 || !got.Ready {
		t.Fatalf("checkpoint must preserve pending private label: %+v", got)
	}
}
