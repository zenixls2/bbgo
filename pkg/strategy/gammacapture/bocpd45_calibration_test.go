package gammacapture

import (
	"math"
	"testing"
	"time"
)

func testBOCPD45Config() BOCPD45Config {
	config := BOCPD45Config{Enabled: true, Calibration: "platt"}
	config.setDefaults()
	return config
}

func TestBOCPD45WaitsForMatureExecutableBBOLabel(t *testing.T) {
	model := NewBOCPD45Model(testBOCPD45Config())
	for i := 0; i < model.config.MinimumSamples-1; i++ {
		model.updateCalibration(0.8, 0)
	}
	start := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	model.pending = &bocpd45PendingLabel{
		MaturesAt: start.Add(45 * time.Second), StartBid: 100, StartAsk: 101, RawProbability: 0.8,
	}
	model.Observe(start.Add(44*time.Second), 99, 100, false)
	if got := len(model.samples); got != model.config.MinimumSamples-1 {
		t.Fatalf("44-second label must not update calibration: samples=%d", got)
	}
	model.Observe(start.Add(45*time.Second), 98, 99, false)
	if got := len(model.samples); got != model.config.MinimumSamples {
		t.Fatalf("mature label must update calibration: samples=%d", got)
	}
	if !model.calibrationReady || !(model.predictCalibration(0.8) < 0.8) {
		t.Fatalf("mature down label should calibrate only subsequent predictions: %+v", model.Snapshot())
	}
}

func TestBOCPD45RawDirectionIsExecutableSideSymmetric(t *testing.T) {
	start := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	up := NewBOCPD45Model(testBOCPD45Config())
	down := NewBOCPD45Model(testBOCPD45Config())
	bidUp, askUp := 100.0, 100.1
	bidDown, askDown := 100.0, 100.1
	up.Observe(start, bidUp, askUp, false)
	down.Observe(start, bidDown, askDown, false)
	for i := 1; i <= 16; i++ {
		at := start.Add(time.Duration(i) * time.Second)
		bidUp, askUp = bidUp+0.1, askUp+0.1
		bidDown, askDown = bidDown-0.1, askDown-0.1
		up.Observe(at, bidUp, askUp, false)
		down.Observe(at, bidDown, askDown, false)
	}
	bull, bear := up.Snapshot(), down.Snapshot()
	if !bull.Ready || !bear.Ready || bull.Direction <= 0 || bear.Direction >= 0 {
		t.Fatalf("expected ready opposite directions: bull=%+v bear=%+v", bull, bear)
	}
	if math.Abs(bull.Direction+bear.Direction) > 1e-12 || math.Abs(bull.Confidence-bear.Confidence) > 1e-12 {
		t.Fatalf("inverted bid/ask paths must be symmetric: bull=%+v bear=%+v", bull, bear)
	}
}

func TestBOCPD45GapDiscardsPendingLabelButKeepsCalibration(t *testing.T) {
	model := NewBOCPD45Model(testBOCPD45Config())
	model.updateCalibration(0.8, 0)
	start := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	model.pending = &bocpd45PendingLabel{
		MaturesAt: start.Add(45 * time.Second), StartBid: 100, StartAsk: 101, RawProbability: 0.8,
	}
	model.Observe(start.Add(time.Hour), 90, 91, true)
	if model.pending != nil {
		t.Fatalf("gap-spanning pending label must be discarded: %+v", model.pending)
	}
	if got := len(model.samples); got != 1 {
		t.Fatalf("mature calibration history should survive path gap: samples=%d", got)
	}
}

func TestBOCPD45CheckpointRestoresRollingCalibration(t *testing.T) {
	config := testBOCPD45Config()
	original := NewBOCPD45Model(config)
	for i := 0; i < 80; i++ {
		label := 0.0
		if i%20 < 11 {
			label = 1
		}
		original.updateCalibration(0.8, label)
	}
	original.pending = &bocpd45PendingLabel{
		MaturesAt: time.Date(2026, 8, 13, 1, 0, 0, 0, time.UTC), StartBid: 100, StartAsk: 101, RawProbability: 0.8,
	}
	restored := NewBOCPD45Model(config)
	if err := restored.restore(original.checkpoint()); err != nil {
		t.Fatal(err)
	}
	if got, want := restored.predictCalibration(0.8), original.predictCalibration(0.8); math.Abs(got-want) > 1e-12 {
		t.Fatalf("restored Platt probability=%v want=%v", got, want)
	}
	if restored.pending == nil || !restored.pending.MaturesAt.Equal(original.pending.MaturesAt) {
		t.Fatalf("pending maturity was not restored: %+v", restored.pending)
	}
}

func TestBOCPD45ConfigRejectsUnsupportedCalibration(t *testing.T) {
	config := testBOCPD45Config()
	config.Calibration = "isotonic"
	if err := config.validate(); err == nil {
		t.Fatal("live BOCPD45 must reject an unselected calibration method")
	}
}
