package main

import (
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/strategy/gammacapture"
)

func TestRelativeHoldReplayBlocksAreMaturedAndNonOverlapping(t *testing.T) {
	start := time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC)
	curve := make([]productionEquityPoint, 0, 5)
	for i := 0; i < 5; i++ {
		curve = append(curve, productionEquityPoint{
			At:            start.Add(time.Duration(i) * time.Hour),
			EquityJPY:     1000 + float64(i)*10,
			HoldEquityJPY: 1000 + float64(i)*5,
		})
	}
	blocks, eligible := relativeHoldReplayBlocks(curve, time.Hour)
	if eligible != 4 || len(blocks) != 4 {
		t.Fatalf("eligible=%d blocks=%d, want four non-overlapping labels", eligible, len(blocks))
	}
	for i, block := range blocks {
		if block.Label.MaturedAt.Before(block.Label.DecisionAt.Add(time.Hour)) {
			t.Fatalf("block %d matured before horizon: %+v", i, block.Label)
		}
		if i > 0 && !block.Label.DecisionAt.Equal(blocks[i-1].Label.MaturedAt) {
			t.Fatalf("block %d overlaps or skips anchor: previous=%s current=%s", i, blocks[i-1].Label.MaturedAt, block.Label.DecisionAt)
		}
		if block.ExcessBps <= 0 || block.ExcessJPY <= 0 {
			t.Fatalf("block %d expected positive strategy excess: %+v", i, block)
		}
	}
}

func TestRelativeHoldReplayMeanStatsAndLowerBound(t *testing.T) {
	mean, se, lower, tStat := relativeHoldMeanStats([]float64{1, 2, 3, 4})
	if math.Abs(mean-2.5) > 1e-12 || se <= 0 || lower >= mean || tStat <= 0 {
		t.Fatalf("unexpected paired stats mean=%f se=%f lower=%f t=%f", mean, se, lower, tStat)
	}
	mean, se, lower, tStat = relativeHoldMeanStats(nil)
	if mean != 0 || se != 0 || lower != 0 || tStat != 0 {
		t.Fatalf("empty paired stats not neutral: mean=%f se=%f lower=%f t=%f", mean, se, lower, tStat)
	}
}

func TestRelativeHoldPreloadFeedsLabelsAndScoreBoundaryKeepsModel(t *testing.T) {
	start := time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC)
	model := gammacapture.NewRelativeHoldRiskModel(gammacapture.RelativeHoldRiskConfig{
		Horizon: time.Hour, HalfLife: 4 * time.Hour, MinimumEffectiveSamples: 1,
	})
	s := &productionReplayState{
		cfg: gammacapture.MarketMakerConfig{RelativeHoldRisk: gammacapture.RelativeHoldRiskConfig{
			Enabled: true, Horizon: time.Hour, HalfLife: 4 * time.Hour,
		}},
		relativeHoldRiskModel: model, relativeHoldRiskHorizon: time.Hour,
		tradingFrom: start, scoreFrom: start.Add(2 * time.Hour),
		initialQuote: 500, initialInventory: 5, initialEquity: 1_000,
		quote: 500, inventory: 5, fillsByDay: make(map[string]*productionReplayDay),
	}
	for i := 0; i < 4; i++ {
		s.recordEquity(start.Add(time.Duration(i)*time.Hour), 100+float64(i), .5, 0, false, false)
		s.updateRelativeHoldRiskFromLatestEquity()
	}
	if got := model.Snapshot().MaturedLabels; got == 0 {
		t.Fatal("preload equity did not feed a matured Relative-Hold label")
	}
	if got := s.relativeHoldRiskInput(); !got.ShadowOnly {
		t.Fatal("preload input must remain shadow-only before scoreFrom")
	}
	s.beginScore(bboSnapshot{time: start.Add(2 * time.Hour), bid: 101, ask: 102})
	if !s.scoreStarted || s.scoreInitialInventory != 5 || s.fills != 0 {
		t.Fatalf("score boundary did not preserve account/reset counters: %+v", s)
	}
	if got := s.relativeHoldRiskInput(); got.ShadowOnly {
		t.Fatal("scoreFrom should allow the matured model input to reach the optimizer")
	}
}

func TestScoreBoundaryCanRestoreLiveAccountWithoutDiscardingMaturedModel(t *testing.T) {
	start := time.Date(2026, 8, 20, 19, 49, 49, 0, time.FixedZone("JST", 9*60*60))
	model := gammacapture.NewRelativeHoldRiskModel(gammacapture.RelativeHoldRiskConfig{
		Horizon: time.Hour, HalfLife: 4 * time.Hour, MinimumEffectiveSamples: 1,
	})
	if !model.UpdateLabel(gammacapture.RelativeHoldRiskLabel{
		DecisionAt: start.Add(-2 * time.Hour), MaturedAt: start.Add(-time.Hour),
		StrategyReturn: .001, HoldReturn: 0,
	}) {
		t.Fatal("test label rejected")
	}
	s := &productionReplayState{
		relativeHoldRiskModel: model,
		scoreFrom:             start,
		quote:                 2_000,
		inventory:             .01,
		fees:                  12,
		takerFees:             3,
		quotedTargetSet:       true,
		lastMakerFill:         gammacapture.MakerPostFillState{At: start.Add(-time.Minute)},
		scoreAccountReset: &productionReplayScoreAccount{
			PairEquityJPY: 7_437.81601817,
			Base:          .0205444,
		},
		fillsByDay: make(map[string]*productionReplayDay),
	}
	before := model.Snapshot().MaturedLabels
	book := bboSnapshot{time: start, bid: 361_300, ask: 361_377}
	s.beginScore(book)
	mid := (book.bid + book.ask) / 2
	if diff := math.Abs((s.quote + s.inventory*mid) - 7_437.81601817); diff > 1e-9 {
		t.Fatalf("score account equity mismatch: diff=%g quote=%f inventory=%f", diff, s.quote, s.inventory)
	}
	if s.inventory != .0205444 || s.fees != 0 || s.takerFees != 0 {
		t.Fatalf("score account was not restored: inventory=%f fees=%f taker=%f", s.inventory, s.fees, s.takerFees)
	}
	if s.quotedTargetSet || !s.lastMakerFill.At.IsZero() {
		t.Fatal("preload execution lifecycle leaked across score boundary")
	}
	if after := model.Snapshot().MaturedLabels; after != before {
		t.Fatalf("matured model was discarded: before=%d after=%d", before, after)
	}
}

func TestRelativeHoldReplayCheckpointAtomicRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state", "relative-hold.json")
	config := relativeHoldRiskStudyConfig(time.Hour)
	model := gammacapture.NewRelativeHoldRiskModel(config)
	decision := time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC)
	if !model.UpdateLabel(gammacapture.RelativeHoldRiskLabel{
		DecisionAt: decision, MaturedAt: decision.Add(time.Hour), StrategyReturn: .001, HoldReturn: 0,
	}) {
		t.Fatal("test label rejected")
	}
	cp := model.Checkpoint()
	if err := saveRelativeHoldRiskReplayCheckpoint(path, "ETHJPY", decision, decision.Add(2*time.Hour), config, &cp); err != nil {
		t.Fatalf("save checkpoint: %v", err)
	}
	loaded, err := loadRelativeHoldRiskReplayCheckpoint(path, "ETHJPY", config, decision.Add(3*time.Hour))
	if err != nil || loaded == nil {
		t.Fatalf("load checkpoint: loaded=%+v err=%v", loaded, err)
	}
	restored := gammacapture.NewRelativeHoldRiskModel(config)
	if err := restored.Restore(loaded.Model); err != nil {
		t.Fatalf("restore model from replay checkpoint: %v", err)
	}
	if restored.Snapshot() != model.Snapshot() {
		t.Fatalf("replay checkpoint changed model state: got=%+v want=%+v", restored.Snapshot(), model.Snapshot())
	}
}
