//go:build ignore

package gammacapture

import (
	"math"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

func testQuantityBOCPDConfig() QuantityBOCPDConfig {
	return QuantityBOCPDConfig{
		Enabled: true, ExpectedRunLength: types.Duration(10 * time.Minute),
		MinimumPriceChanges: 8, MaximumRunLengthStates: 64,
	}
}

func observeQuantityBOCPDPath(model *QuantityBOCPDModel, start time.Time, deltas []float64) {
	config := testQuantityBOCPDConfig()
	bid, ask := 100.0, 100.2
	model.Observe(start, bid, ask, false, config)
	for index, delta := range deltas {
		bid += delta
		ask += delta
		model.Observe(start.Add(time.Duration(index+1)*time.Minute), bid, ask, false, config)
	}
}

func TestQuantityBOCPDReactsToPersistentReversalButNotOneTick(t *testing.T) {
	start := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	model := &QuantityBOCPDModel{}
	up := make([]float64, 20)
	for index := range up {
		up[index] = 0.1
	}
	observeQuantityBOCPDPath(model, start, up)
	before := model.Decision(testQuantityBOCPDConfig())
	if !before.Ready || before.UpProbability <= 0.65 {
		t.Fatalf("persistent rise should produce a bullish posterior: %+v", before)
	}

	model.Observe(start.Add(21*time.Minute), model.LastBid-0.1, model.LastAsk-0.1, false, testQuantityBOCPDConfig())
	falseBreak := model.Decision(testQuantityBOCPDConfig())
	if falseBreak.UpProbability <= 0.5 {
		t.Fatalf("one contrary tick must not reverse a mature bullish run: before=%+v after=%+v", before, falseBreak)
	}

	for index := 0; index < 8; index++ {
		model.Observe(
			start.Add(time.Duration(22+index)*time.Minute),
			model.LastBid-0.1, model.LastAsk-0.1, false, testQuantityBOCPDConfig())
	}
	after := model.Decision(testQuantityBOCPDConfig())
	if after.UpProbability >= 0.5 {
		t.Fatalf("persistent downside should reverse the posterior without a completed window: %+v", after)
	}
	if after.AskExpectedRunLength >= before.AskExpectedRunLength {
		t.Fatalf("changepoint posterior should shorten the inferred run after reversal: before=%+v after=%+v", before, after)
	}
}

func TestQuantityBOCPDIsSideSymmetricAndBounded(t *testing.T) {
	start := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	upModel, downModel := &QuantityBOCPDModel{}, &QuantityBOCPDModel{}
	path := []float64{0.1, 0.1, -0.1, 0.1, 0.1, 0.1, -0.1, 0.1, 0.1, 0.1, 0.1, -0.1}
	inverse := make([]float64, len(path))
	for index, value := range path {
		inverse[index] = -value
	}
	observeQuantityBOCPDPath(upModel, start, path)
	observeQuantityBOCPDPath(downModel, start, inverse)
	upDecision := upModel.Decision(testQuantityBOCPDConfig())
	downDecision := downModel.Decision(testQuantityBOCPDConfig())
	if math.Abs(upDecision.UpProbability+downDecision.UpProbability-1) > 1e-12 {
		t.Fatalf("inverting every executable-side move must invert direction: up=%+v down=%+v", upDecision, downDecision)
	}
	if len(upModel.Ask.States) > 64 || len(upModel.Bid.States) > 64 {
		t.Fatalf("online run-length state must remain bounded: ask=%d bid=%d", len(upModel.Ask.States), len(upModel.Bid.States))
	}
}

func TestQuantityBOCPDTargetChangesQBuyAndQSellInsideUnifiedSolver(t *testing.T) {
	baseDecision := QuantityBOCPDDecision{Enabled: true, Ready: true}
	bullish := baseDecision
	bullish.UpProbability = 0.75
	bearish := baseDecision
	bearish.UpProbability = 0.25

	bullTarget, bullDecision := ApplyQuantityBOCPDTarget(bullish, 50, 0, 100, false)
	bearTarget, bearDecision := ApplyQuantityBOCPDTarget(bearish, 50, 0, 100, false)
	if !bullDecision.Applied || !bearDecision.Applied || bullTarget != 75 || bearTarget != 25 {
		t.Fatalf("unexpected posterior targets: bull=%+v target=%.2f bear=%+v target=%.2f",
			bullDecision, bullTarget, bearDecision, bearTarget)
	}

	projection := func(target float64) ProbabilityCenteredQuoteDecision {
		return ProbabilityCenteredQuoteNotionals(ProbabilityCenteredQuoteInput{
			CurrentInventoryNotionalJPY: 50, TargetInventoryNotionalJPY: target,
			LowerInventoryNotionalJPY: 0, UpperInventoryNotionalJPY: 100,
			FastBuyNotionalJPY: 50, FastSellNotionalJPY: 50,
			MaxBuyNotionalJPY: 100, MaxSellNotionalJPY: 100,
			DirectFillProbabilities: true,
			BuyFillProbability:      0.5, SellFillProbability: 0.5, BothFillProbability: 0.25,
			Horizon: 10 * time.Minute, ConfidenceZScore: 0.1,
			TargetContraction: 1, FullRiskPromotion: true,
		})
	}
	bullQuote, bearQuote := projection(bullTarget), projection(bearTarget)
	if !bullQuote.Enabled || bullQuote.BuyNotionalJPY <= bullQuote.SellNotionalJPY {
		t.Fatalf("bullish BOCPD target must allocate more BUY in the unified solver: %+v", bullQuote)
	}
	if !bearQuote.Enabled || bearQuote.SellNotionalJPY <= bearQuote.BuyNotionalJPY {
		t.Fatalf("bearish BOCPD target must allocate more SELL in the unified solver: %+v", bearQuote)
	}

	shadowTarget, shadowDecision := ApplyQuantityBOCPDTarget(bullish, 50, 0, 100, true)
	if shadowTarget != 50 || shadowDecision.Applied || shadowDecision.PosteriorTargetBase != 75 {
		t.Fatalf("shadow mode must report but not apply the posterior target: target=%.2f decision=%+v", shadowTarget, shadowDecision)
	}
}

func TestQuantityBOCPDCheckpointRoundTrip(t *testing.T) {
	start := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	model := &QuantityBOCPDModel{}
	observeQuantityBOCPDPath(model, start, []float64{0.1, -0.1, 0.1, 0.1, -0.1, 0.1, 0.1, 0.1})
	before := model.Decision(testQuantityBOCPDConfig())
	restored := restoreQuantityBOCPD(checkpointQuantityBOCPD(*model))
	after := restored.Decision(testQuantityBOCPDConfig())
	if math.Abs(before.UpProbability-after.UpProbability) > 1e-15 ||
		before.AskChanges != after.AskChanges || before.BidChanges != after.BidChanges {
		t.Fatalf("checkpoint must preserve the online posterior: before=%+v after=%+v", before, after)
	}
}

func TestQuantityBOCPDHawkesFusionUsesIntensityOddsAndIsSymmetric(t *testing.T) {
	bocpd := QuantityBOCPDDecision{Enabled: true, Ready: true, UpProbability: 0.6}
	bullish := HawkesDirectionSnapshot{
		Ready: true, LambdaUp: 3, LambdaDown: 1, Total: 4, Confidence: 0.5,
	}
	bearish := HawkesDirectionSnapshot{
		Ready: true, LambdaUp: 1, LambdaDown: 3, Total: 4, Confidence: 0.5,
	}
	bull := FuseQuantityBOCPDWithHawkes(bocpd, bullish)
	bear := FuseQuantityBOCPDWithHawkes(
		QuantityBOCPDDecision{Enabled: true, Ready: true, UpProbability: 0.4}, bearish)
	if bull.UpProbability <= bocpd.UpProbability {
		t.Fatalf("bullish Hawkes prior must increase posterior up probability: before=%+v after=%+v", bocpd, bull)
	}
	if math.Abs(bull.UpProbability+bear.UpProbability-1) > 1e-12 {
		t.Fatalf("inverted BOCPD and Hawkes evidence must remain symmetric: bull=%+v bear=%+v", bull, bear)
	}
	neutral := bullish
	neutral.Confidence = 0
	unchanged := FuseQuantityBOCPDWithHawkes(bocpd, neutral)
	if math.Abs(unchanged.UpProbability-bocpd.UpProbability) > 1e-15 {
		t.Fatalf("zero-confidence Hawkes prior must be neutral: before=%+v after=%+v", bocpd, unchanged)
	}
}
