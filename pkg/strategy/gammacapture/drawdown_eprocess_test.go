package gammacapture

import (
	"math"
	"testing"
	"time"
)

func TestHalfNormalDriftMixtureEStartsAtOneAndPenalizesJumpQV(t *testing.T) {
	if got := halfNormalDriftMixtureE(0, 0, 1000); math.Abs(got-1) > 1e-12 {
		t.Fatalf("initial e-value = %.12f, want 1", got)
	}
	continuous := halfNormalDriftMixtureE(.004, 40*.0001*.0001, 1000)
	jump := halfNormalDriftMixtureE(.004, .004*.004, 1000)
	if continuous <= 20 || jump >= continuous {
		t.Fatalf("QV clock did not distinguish persistent drift from one jump: continuous=%g jump=%g", continuous, jump)
	}
}

func TestDrawdownEProcessDetectsPersistentFallAndRecovery(t *testing.T) {
	model := NewDrawdownEProcess(DrawdownEProcessConfig{BarrierWidth: .001, ConfidenceZ: 1.645})
	at := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	price := 300_000.0
	model.ObserveMinute(at, price-5, price+5)
	downDetected := false
	detectedMinute := 0
	for minute := 1; minute <= 60; minute++ {
		price *= math.Exp(-.00008)
		decision := model.ObserveMinute(at.Add(time.Duration(minute)*time.Minute), price-5, price+5)
		if decision.DownAlarm {
			downDetected = true
			detectedMinute = minute
			break
		}
	}
	if !downDetected {
		t.Fatal("persistent executable decline did not cross the e-process threshold")
	}
	recoveryDetected := false
	for minute := detectedMinute + 1; minute <= detectedMinute+80; minute++ {
		price *= math.Exp(.00010)
		decision := model.ObserveMinute(at.Add(time.Duration(minute)*time.Minute), price-5, price+5)
		if decision.RecoveryAlarm {
			recoveryDetected = true
			break
		}
	}
	if !recoveryDetected {
		t.Fatal("persistent executable recovery did not release the drawdown state")
	}
}

func TestDrawdownEProcessRejectsSpreadOnlyDirection(t *testing.T) {
	model := NewDrawdownEProcess(DrawdownEProcessConfig{BarrierWidth: .001, ConfidenceZ: 1.645})
	at := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	bid, ask := 299_990.0, 300_010.0
	model.ObserveMinute(at, bid, ask)
	for minute := 1; minute <= 100; minute++ {
		bid *= math.Exp(-.0001)
		ask *= math.Exp(.0001)
		decision := model.ObserveMinute(at.Add(time.Duration(minute)*time.Minute), bid, ask)
		if decision.DownAlarm {
			t.Fatalf("one-sided spread widening manufactured a drawdown alarm: %+v", decision)
		}
	}
}

func TestDrawdownEProcessIsPriceScaleInvariant(t *testing.T) {
	left := NewDrawdownEProcess(DrawdownEProcessConfig{})
	right := NewDrawdownEProcess(DrawdownEProcessConfig{})
	at := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	for minute := 0; minute < 30; minute++ {
		price := 300_000 * math.Exp(-.00007*float64(minute))
		ld := left.ObserveMinute(at.Add(time.Duration(minute)*time.Minute), price-5, price+5)
		rd := right.ObserveMinute(at.Add(time.Duration(minute)*time.Minute), (price-5)*10, (price+5)*10)
		if math.Abs(ld.DownEValue-rd.DownEValue) > 1e-9 || ld.Active != rd.Active {
			t.Fatalf("price scaling changed evidence at minute %d: left=%+v right=%+v", minute, ld, rd)
		}
	}
}

func TestDrawdownEProcessGapResetsEvidence(t *testing.T) {
	model := NewDrawdownEProcess(DrawdownEProcessConfig{})
	at := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	model.ObserveMinute(at, 99, 101)
	model.ObserveMinute(at.Add(time.Minute), 98, 100)
	d := model.ObserveMinute(at.Add(3*time.Minute), 97, 99)
	if d.Healthy || d.Active || d.DownEValue != 0 || d.RecoveryEValue != 0 {
		t.Fatalf("gap failed to reset sequential evidence: %+v", d)
	}
}

func TestDrawdownEProcessSameMinuteDoesNotResetAndForecastsExecutableBid(t *testing.T) {
	model := NewDrawdownEProcess(DrawdownEProcessConfig{
		BarrierWidth: .001, ConfidenceZ: .1, MinimumMinutes: 2,
	})
	start := time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC)
	model.ObserveMinute(start, 100, 100.1)
	model.ObserveMinute(start.Add(20*time.Second), 99.9, 100)
	var d DrawdownEProcessDecision
	for i := 1; i <= 8; i++ {
		price := 100 * math.Exp(-float64(i)*.001)
		d = model.ObserveMinute(start.Add(time.Duration(i)*time.Minute), price, price+.1)
		if d.Active {
			break
		}
	}
	if !d.Active {
		t.Fatalf("same-minute book event reset sequential evidence: %+v", d)
	}
	forecast := model.DownsideForecastBps(15 * time.Minute)
	if !(forecast > 0) || math.IsNaN(forecast) || math.IsInf(forecast, 0) {
		t.Fatalf("expected finite positive executable-bid forecast, got %.8f", forecast)
	}
}
