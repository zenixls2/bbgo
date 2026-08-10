package gammacapture

import (
	"math"
	"testing"
	"time"
)

func TestInventoryActuationUsesExpectedRegimeFillsAsEffectiveLevels(t *testing.T) {
	d := InventoryActuation(InventoryActuationInput{
		CurrentInventoryNotionalJPY: 3_000, TargetInventoryNotionalJPY: 4_500,
		ExpectedFillNotionalJPY: 100, BuyFillRatePerHour: 0.25,
		RegimeHorizon: 3 * time.Hour, MaximumOrderLevels: 6,
	})
	if !d.Enabled || d.Direction != 1 || d.EffectiveOrderLevels != 1 || d.TargetContraction != 1 {
		t.Fatalf("sparse regime should use one reachable tranche: %+v", d)
	}
	if d.InwardStrength <= 0 || d.InwardStrength >= 1 {
		t.Fatalf("neutral momentum should apply its posterior 50%% weight: %+v", d)
	}

	d = InventoryActuation(InventoryActuationInput{
		CurrentInventoryNotionalJPY: 3_000, TargetInventoryNotionalJPY: 4_500,
		ExpectedFillNotionalJPY: 100, BuyFillRatePerHour: 4,
		RegimeHorizon: 3 * time.Hour, MaximumOrderLevels: 6, MomentumSignal: 1,
	})
	if math.Abs(d.EffectiveOrderLevels-6) > 1e-12 || math.Abs(d.TargetContraction-1.0/6) > 1e-12 {
		t.Fatalf("liquid regime must retain configured risk cap: %+v", d)
	}
}

func TestInventoryActuationConvergesInExpectedRegimeFills(t *testing.T) {
	d := InventoryActuation(InventoryActuationInput{
		CurrentInventoryNotionalJPY: 3_000, TargetInventoryNotionalJPY: 4_000,
		ExpectedFillNotionalJPY: 100, BuyFillRatePerHour: 1,
		RegimeHorizon: 3 * time.Hour, MaximumOrderLevels: 6,
	})
	if !d.Enabled || math.Abs(d.EffectiveOrderLevels-3) > 1e-12 {
		t.Fatalf("expected three reachable correction tranches: %+v", d)
	}
	// K expected fills, each removing 1/K of the target error, produce one
	// target error of expected drift over the regime horizon.
	if math.Abs(d.ExpectedCorrectiveFills*d.TargetContraction-1) > 1e-12 {
		t.Fatalf("expected regime correction must equal one target error: %+v", d)
	}
}

func TestInventoryActuationFailsClosedWithoutCorrectiveArrivalRate(t *testing.T) {
	d := InventoryActuation(InventoryActuationInput{
		CurrentInventoryNotionalJPY: 4_000, TargetInventoryNotionalJPY: 3_000,
		ExpectedFillNotionalJPY: 100, BuyFillRatePerHour: 10, SellFillRatePerHour: 0,
		RegimeHorizon: time.Hour, MaximumOrderLevels: 6, MomentumSignal: -1,
	})
	if d.Enabled || d.Direction != -1 || d.Reason != "corrective arrival rate unavailable" {
		t.Fatalf("sell correction must not borrow the unrelated buy arrival rate: %+v", d)
	}
}

func TestInventoryActuationFailsClosedOnNonFiniteInputs(t *testing.T) {
	cases := []InventoryActuationInput{
		{CurrentInventoryNotionalJPY: 3_000, TargetInventoryNotionalJPY: 4_000,
			ExpectedFillNotionalJPY: 100, BuyFillRatePerHour: math.NaN(),
			RegimeHorizon: time.Hour, MaximumOrderLevels: 6},
		{CurrentInventoryNotionalJPY: 3_000, TargetInventoryNotionalJPY: 4_000,
			ExpectedFillNotionalJPY: 100, BuyFillRatePerHour: math.Inf(1),
			RegimeHorizon: time.Hour, MaximumOrderLevels: 6},
		{CurrentInventoryNotionalJPY: 3_000, TargetInventoryNotionalJPY: 4_000,
			ExpectedFillNotionalJPY: math.NaN(), BuyFillRatePerHour: 1,
			RegimeHorizon: time.Hour, MaximumOrderLevels: 6},
		{CurrentInventoryNotionalJPY: 3_000, TargetInventoryNotionalJPY: 4_000,
			ExpectedFillNotionalJPY: 100, BuyFillRatePerHour: 1,
			RegimeHorizon: time.Hour, MaximumOrderLevels: math.NaN()},
	}
	for i, in := range cases {
		d := InventoryActuation(in)
		if d.Enabled || d.TargetContraction != 0 || d.EffectiveOrderLevels != 0 {
			t.Fatalf("case %d must fail closed without a staged correction: %+v", i, d)
		}
	}
}

func TestInventoryActuationMomentumIsPosteriorAlignmentProbability(t *testing.T) {
	base := InventoryActuationInput{
		CurrentInventoryNotionalJPY: 3_000, TargetInventoryNotionalJPY: 4_000,
		ExpectedFillNotionalJPY: 100, BuyFillRatePerHour: 1,
		RegimeHorizon: time.Hour, MaximumOrderLevels: 6,
	}
	base.MomentumSignal = -1
	adverse := InventoryActuation(base)
	base.MomentumSignal = 0
	neutral := InventoryActuation(base)
	base.MomentumSignal = 1
	aligned := InventoryActuation(base)
	if adverse.InwardStrength != 0 || math.Abs(neutral.InwardStrength-.5) > 1e-12 || aligned.InwardStrength != 1 {
		t.Fatalf("unexpected momentum posterior weights: adverse=%+v neutral=%+v aligned=%+v", adverse, neutral, aligned)
	}
	base.CurrentInventoryNotionalJPY, base.TargetInventoryNotionalJPY = 5_000, 4_000
	base.BuyFillRatePerHour, base.SellFillRatePerHour = 0, 1
	base.MomentumSignal = -1
	alignedSell := InventoryActuation(base)
	if alignedSell.Direction != -1 || alignedSell.MomentumAlignmentProbability != 1 || alignedSell.InwardStrength != 1 {
		t.Fatalf("negative momentum must align with a sell correction: %+v", alignedSell)
	}
}

func TestInventoryTargetRealignmentUsesExecutableCellAndDirection(t *testing.T) {
	if InventoryTargetRealignmentRequired(.51, .50, .40, 10_000, 200) {
		t.Fatal("sub-cell target drift on the same correction side should retain queue")
	}
	if !InventoryTargetRealignmentRequired(.53, .50, .40, 10_000, 200) {
		t.Fatal("one executable target cell should trigger quantity realignment")
	}
	if !InventoryTargetRealignmentRequired(.39, .41, .40, 10_000, 200) {
		t.Fatal("correction-side reversal should trigger even below one cell")
	}
}

func rateForHorizonProbability(probability float64, horizon time.Duration) float64 {
	return -math.Log(1-probability) / horizon.Hours()
}

func TestProbabilityCenteredQuotePreservesFastGrossAndMacroExpectation(t *testing.T) {
	horizon := 30 * time.Minute
	d := ProbabilityCenteredQuoteNotionals(ProbabilityCenteredQuoteInput{
		CurrentInventoryNotionalJPY: 500,
		TargetInventoryNotionalJPY:  500,
		LowerInventoryNotionalJPY:   0,
		UpperInventoryNotionalJPY:   1_000,
		FastBuyNotionalJPY:          200,
		FastSellNotionalJPY:         200,
		MaxBuyNotionalJPY:           400,
		MaxSellNotionalJPY:          400,
		BuyFillRatePerHour:          rateForHorizonProbability(0.40, horizon),
		SellFillRatePerHour:         rateForHorizonProbability(0.20, horizon),
		Horizon:                     horizon,
		ConfidenceZScore:            1.645,
	})
	if !d.Enabled || math.Abs(d.ProjectedGrossNotionalJPY-400) > 1e-7 {
		t.Fatalf("expected full Fast gross budget: %+v", d)
	}
	if math.Abs(d.BuyNotionalJPY-400.0/3.0) > 1e-7 ||
		math.Abs(d.SellNotionalJPY-800.0/3.0) > 1e-7 {
		t.Fatalf("unexpected probability-centered split: %+v", d)
	}
	if math.Abs(d.ExpectedInventoryNotionalJPY-500) > 1e-7 {
		t.Fatalf("fill-weighted inventory must remain at Macro expectation: %+v", d)
	}
}

func TestProbabilityCenteredQuoteChanceConstraintReducesGross(t *testing.T) {
	horizon := 30 * time.Minute
	d := ProbabilityCenteredQuoteNotionals(ProbabilityCenteredQuoteInput{
		CurrentInventoryNotionalJPY: 500,
		TargetInventoryNotionalJPY:  500,
		LowerInventoryNotionalJPY:   400,
		UpperInventoryNotionalJPY:   600,
		FastBuyNotionalJPY:          1_000,
		FastSellNotionalJPY:         1_000,
		MaxBuyNotionalJPY:           1_000,
		MaxSellNotionalJPY:          1_000,
		BuyFillRatePerHour:          rateForHorizonProbability(0.25, horizon),
		SellFillRatePerHour:         rateForHorizonProbability(0.25, horizon),
		Horizon:                     horizon,
		ConfidenceZScore:            1.645,
	})
	if !d.Enabled || d.ProjectedGrossNotionalJPY <= 0 || d.ProjectedGrossNotionalJPY >= 2_000 {
		t.Fatalf("confidence interval should reduce but not eliminate Fast gross: %+v", d)
	}
	if d.ConfidenceLowerNotionalJPY < 400-1e-6 || d.ConfidenceUpperNotionalJPY > 600+1e-6 {
		t.Fatalf("projected order distribution escaped Macro band: %+v", d)
	}
	if math.Abs(d.BuyNotionalJPY-d.SellNotionalJPY) > 1e-7 {
		t.Fatalf("symmetric fill hazards at target require symmetric notionals: %+v", d)
	}
}

func TestProbabilityCenteredQuoteUsesOppositeSideToRetainGross(t *testing.T) {
	horizon := 30 * time.Minute
	d := ProbabilityCenteredQuoteNotionals(ProbabilityCenteredQuoteInput{
		CurrentInventoryNotionalJPY: 400,
		TargetInventoryNotionalJPY:  500,
		LowerInventoryNotionalJPY:   0,
		UpperInventoryNotionalJPY:   1_000,
		FastBuyNotionalJPY:          300,
		FastSellNotionalJPY:         300,
		MaxBuyNotionalJPY:           600,
		MaxSellNotionalJPY:          600,
		BuyFillRatePerHour:          rateForHorizonProbability(0.50, horizon),
		SellFillRatePerHour:         rateForHorizonProbability(0.25, horizon),
		Horizon:                     horizon,
		ConfidenceZScore:            1,
	})
	if !d.Enabled || math.Abs(d.ProjectedGrossNotionalJPY-600) > 1e-7 {
		t.Fatalf("expected Fast gross to remain available: %+v", d)
	}
	if d.BuyNotionalJPY <= d.SellNotionalJPY {
		t.Fatalf("inventory deficit must allocate more expected flow to BUY: %+v", d)
	}
	if math.Abs(d.ExpectedInventoryNotionalJPY-500) > 1e-7 {
		t.Fatalf("opposite-side quote should offset risk around Macro expectation: %+v", d)
	}
}

func TestProbabilityCenteredQuoteFallsBackWithoutTwoSidedStatistics(t *testing.T) {
	d := ProbabilityCenteredQuoteNotionals(ProbabilityCenteredQuoteInput{
		CurrentInventoryNotionalJPY: 500,
		TargetInventoryNotionalJPY:  500,
		LowerInventoryNotionalJPY:   400,
		UpperInventoryNotionalJPY:   600,
		FastBuyNotionalJPY:          100,
		FastSellNotionalJPY:         100,
		MaxBuyNotionalJPY:           100,
		MaxSellNotionalJPY:          100,
		BuyFillRatePerHour:          1,
		Horizon:                     30 * time.Minute,
	})
	if d.Enabled || d.Reason != "two-sided fill probabilities unavailable" {
		t.Fatalf("one-sided statistics must retain the staged fallback: %+v", d)
	}
}

func TestProbabilityCenteredQuoteFailsClosedOnNonFiniteStatistics(t *testing.T) {
	base := ProbabilityCenteredQuoteInput{
		CurrentInventoryNotionalJPY: 500,
		TargetInventoryNotionalJPY:  500,
		LowerInventoryNotionalJPY:   400,
		UpperInventoryNotionalJPY:   600,
		FastBuyNotionalJPY:          100,
		FastSellNotionalJPY:         100,
		MaxBuyNotionalJPY:           100,
		MaxSellNotionalJPY:          100,
		BuyFillRatePerHour:          1,
		SellFillRatePerHour:         1,
		Horizon:                     30 * time.Minute,
		ConfidenceZScore:            1.645,
	}
	for name, mutate := range map[string]func(*ProbabilityCenteredQuoteInput){
		"current":     func(in *ProbabilityCenteredQuoteInput) { in.CurrentInventoryNotionalJPY = math.NaN() },
		"buy rate":    func(in *ProbabilityCenteredQuoteInput) { in.BuyFillRatePerHour = math.Inf(1) },
		"confidence":  func(in *ProbabilityCenteredQuoteInput) { in.ConfidenceZScore = math.NaN() },
		"contraction": func(in *ProbabilityCenteredQuoteInput) { in.TargetContraction = math.Inf(1) },
	} {
		in := base
		mutate(&in)
		d := ProbabilityCenteredQuoteNotionals(in)
		if d.Enabled {
			t.Fatalf("%s corruption must not produce an enabled projection: %+v", name, d)
		}
	}
}

func TestProbabilityCenteredQuoteContractsOutsideSoftBandAndKeepsBothSides(t *testing.T) {
	horizon := 30 * time.Minute
	d := ProbabilityCenteredQuoteNotionals(ProbabilityCenteredQuoteInput{
		CurrentInventoryNotionalJPY: 700,
		TargetInventoryNotionalJPY:  500,
		LowerInventoryNotionalJPY:   400,
		UpperInventoryNotionalJPY:   600,
		FastBuyNotionalJPY:          200,
		FastSellNotionalJPY:         200,
		MinBuyNotionalJPY:           100,
		MinSellNotionalJPY:          100,
		MaxBuyNotionalJPY:           100,
		MaxSellNotionalJPY:          200,
		BuyFillRatePerHour:          rateForHorizonProbability(0.05, horizon),
		SellFillRatePerHour:         rateForHorizonProbability(0.05, horizon),
		Horizon:                     horizon,
		ConfidenceZScore:            1.645,
		TargetContraction:           1.0 / 6,
	})
	if !d.Enabled {
		t.Fatalf("inventory outside the Macro target must still be projectable: %+v", d)
	}
	if d.BuyNotionalJPY < 100 || d.SellNotionalJPY < 100 {
		t.Fatalf("both Fast sides should remain exchange-executable: %+v", d)
	}
	if d.BuyNotionalJPY > 100+1e-7 || d.SellNotionalJPY > 200+1e-7 {
		t.Fatalf("one fill must remain inside its staged Macro correction cap: %+v", d)
	}
	if d.ExpectedInventoryNotionalJPY >= 700 || d.ExpectedInventoryNotionalJPY < 500 {
		t.Fatalf("expected inventory must contract toward, not jump through, the Macro target: %+v", d)
	}
	want := 700 + 0.05*(500-700)/6.0
	if math.Abs(d.DesiredInventoryNotionalJPY-want) > 1e-7 {
		t.Fatalf("unexpected staged Macro target: got %.9f want %.9f", d.DesiredInventoryNotionalJPY, want)
	}
}

func TestProbabilityCenteredQuoteBearishRestraintDoesNotForceSell(t *testing.T) {
	horizon := 30 * time.Minute
	base := ProbabilityCenteredQuoteInput{
		CurrentInventoryNotionalJPY: 500,
		TargetInventoryNotionalJPY:  500,
		LowerInventoryNotionalJPY:   0,
		UpperInventoryNotionalJPY:   1_000,
		FastBuyNotionalJPY:          200,
		FastSellNotionalJPY:         200,
		MaxBuyNotionalJPY:           400,
		MaxSellNotionalJPY:          400,
		BuyFillRatePerHour:          rateForHorizonProbability(0.25, horizon),
		SellFillRatePerHour:         rateForHorizonProbability(0.25, horizon),
		Horizon:                     horizon,
		ConfidenceZScore:            1.645,
	}
	unrestrained := ProbabilityCenteredQuoteNotionals(base)
	base.FastBuyRestraint = 0.5
	restrained := ProbabilityCenteredQuoteNotionals(base)
	if !unrestrained.Enabled || !restrained.Enabled {
		t.Fatalf("expected enabled projections: unrestrained=%+v restrained=%+v", unrestrained, restrained)
	}
	if math.Abs(restrained.BuyNotionalJPY-unrestrained.BuyNotionalJPY*0.5) > 1e-7 {
		t.Fatalf("BUY was not attenuated continuously: unrestrained=%+v restrained=%+v", unrestrained, restrained)
	}
	if math.Abs(restrained.SellNotionalJPY-unrestrained.SellNotionalJPY) > 1e-7 {
		t.Fatalf("bearish BUY restraint must not enlarge SELL or force liquidation: unrestrained=%+v restrained=%+v", unrestrained, restrained)
	}
	if restrained.DesiredInventoryNotionalJPY != unrestrained.DesiredInventoryNotionalJPY ||
		restrained.TargetContraction != unrestrained.TargetContraction {
		t.Fatalf("bearish BUY restraint must not rewrite Macro target: unrestrained=%+v restrained=%+v", unrestrained, restrained)
	}
}
