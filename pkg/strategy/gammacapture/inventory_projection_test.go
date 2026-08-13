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
		RegimeHorizon: 3 * time.Hour,
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
		RegimeHorizon: 3 * time.Hour, MomentumSignal: 1,
	})
	if math.Abs(d.EffectiveOrderLevels-12) > 1e-12 || math.Abs(d.TargetContraction-1.0/12) > 1e-12 {
		t.Fatalf("liquid regime must use its reachable executable fills: %+v", d)
	}
}

func TestInventoryActuationCannotCreateSubMinimumCorrectionTranches(t *testing.T) {
	d := InventoryActuation(InventoryActuationInput{
		CurrentInventoryNotionalJPY: 3_000, TargetInventoryNotionalJPY: 3_250,
		ExpectedFillNotionalJPY: 100, BuyFillRatePerHour: 4,
		RegimeHorizon: 3 * time.Hour,
	})
	if !d.Enabled || math.Abs(d.RequiredCorrectionFills-2.5) > 1e-12 || math.Abs(d.EffectiveOrderLevels-2.5) > 1e-12 {
		t.Fatalf("exchange-sized correction gap must cap reachable levels: %+v", d)
	}
	if perFillCorrection := 250 * d.TargetContraction; perFillCorrection+1e-12 < 100 {
		t.Fatalf("actuation produced a sub-minimum correction tranche: %.6f", perFillCorrection)
	}
}

func TestInventoryActuationConvergesInExpectedRegimeFills(t *testing.T) {
	d := InventoryActuation(InventoryActuationInput{
		CurrentInventoryNotionalJPY: 3_000, TargetInventoryNotionalJPY: 4_000,
		ExpectedFillNotionalJPY: 100, BuyFillRatePerHour: 1,
		RegimeHorizon: 3 * time.Hour,
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
		RegimeHorizon: time.Hour, MomentumSignal: -1,
	})
	if d.Enabled || d.Direction != -1 || d.Reason != "corrective arrival rate unavailable" {
		t.Fatalf("sell correction must not borrow the unrelated buy arrival rate: %+v", d)
	}
}

func TestInventoryActuationFailsClosedOnNonFiniteInputs(t *testing.T) {
	cases := []InventoryActuationInput{
		{CurrentInventoryNotionalJPY: 3_000, TargetInventoryNotionalJPY: 4_000,
			ExpectedFillNotionalJPY: 100, BuyFillRatePerHour: math.NaN(),
			RegimeHorizon: time.Hour},
		{CurrentInventoryNotionalJPY: 3_000, TargetInventoryNotionalJPY: 4_000,
			ExpectedFillNotionalJPY: 100, BuyFillRatePerHour: math.Inf(1),
			RegimeHorizon: time.Hour},
		{CurrentInventoryNotionalJPY: 3_000, TargetInventoryNotionalJPY: 4_000,
			ExpectedFillNotionalJPY: math.NaN(), BuyFillRatePerHour: 1,
			RegimeHorizon: time.Hour},
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
		RegimeHorizon: time.Hour,
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

func TestProbabilityCenteredQuoteFindsNarrowMinimumOrderFeasibleRegion(t *testing.T) {
	horizon := 30 * time.Minute
	d := ProbabilityCenteredQuoteNotionals(ProbabilityCenteredQuoteInput{
		CurrentInventoryNotionalJPY: 500,
		TargetInventoryNotionalJPY:  500,
		LowerInventoryNotionalJPY:   390,
		UpperInventoryNotionalJPY:   610,
		FastBuyNotionalJPY:          5_000,
		FastSellNotionalJPY:         5_000,
		MinBuyNotionalJPY:           100,
		MinSellNotionalJPY:          100,
		MaxBuyNotionalJPY:           5_000,
		MaxSellNotionalJPY:          5_000,
		BuyFillRatePerHour:          rateForHorizonProbability(0.25, horizon),
		SellFillRatePerHour:         rateForHorizonProbability(0.25, horizon),
		Horizon:                     horizon,
		ConfidenceZScore:            1.645,
	})
	if !d.Enabled {
		t.Fatalf("narrow executable region near exchange minimum was skipped: %+v", d)
	}
	if d.BuyNotionalJPY < 100 || d.SellNotionalJPY < 100 {
		t.Fatalf("projection must keep both exchange-executable sides: %+v", d)
	}
	if d.ProjectedGrossNotionalJPY < 200 || d.ProjectedGrossNotionalJPY > 230 {
		t.Fatalf("unexpected narrow-region upper boundary: %+v", d)
	}
}

func TestProbabilityCenteredTargetGrossIsNonMinimumAndNonAllIn(t *testing.T) {
	gross := probabilityCenteredTargetGross(2_400, 2_650, 0.25, 0.20, 100, 100, 3_000, 2_400)
	if math.Abs(gross-1_180) > 1e-9 {
		t.Fatalf("target correction should use 1,080 BUY + 100 SELL, got gross %v", gross)
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
		t.Fatalf("bearish BUY restraint must not rewrite unified target: unrestrained=%+v restrained=%+v", unrestrained, restrained)
	}

	base.FastBuyRestraint = 0
	base.FastSellRestraint = 0.5
	bullish := ProbabilityCenteredQuoteNotionals(base)
	if !bullish.Enabled {
		t.Fatalf("expected enabled bullish projection: %+v", bullish)
	}
	if math.Abs(bullish.SellNotionalJPY-unrestrained.SellNotionalJPY*0.5) > 1e-7 {
		t.Fatalf("SELL was not attenuated continuously: unrestrained=%+v bullish=%+v", unrestrained, bullish)
	}
	if math.Abs(bullish.BuyNotionalJPY-unrestrained.BuyNotionalJPY) > 1e-7 {
		t.Fatalf("bullish SELL restraint must not enlarge BUY: unrestrained=%+v bullish=%+v", unrestrained, bullish)
	}
}

func TestProbabilityCenteredQuoteAllowsAsymmetricMinimumOrdersAtTarget(t *testing.T) {
	horizon := 30 * time.Minute
	d := ProbabilityCenteredQuoteNotionals(ProbabilityCenteredQuoteInput{
		CurrentInventoryNotionalJPY: 500,
		TargetInventoryNotionalJPY:  500,
		LowerInventoryNotionalJPY:   390,
		UpperInventoryNotionalJPY:   610,
		FastBuyNotionalJPY:          5_000,
		FastSellNotionalJPY:         5_000,
		MinBuyNotionalJPY:           100,
		MinSellNotionalJPY:          100,
		MaxBuyNotionalJPY:           100,
		MaxSellNotionalJPY:          100,
		BuyFillRatePerHour:          rateForHorizonProbability(0.162, horizon),
		SellFillRatePerHour:         rateForHorizonProbability(0.067, horizon),
		Horizon:                     horizon,
		ConfidenceZScore:            1.645,
	})
	if !d.Enabled || math.Abs(d.ProjectedGrossNotionalJPY-200) > 1e-7 {
		t.Fatalf("asymmetric minimum orders should fit the joint second-moment budget: %+v", d)
	}
	if d.ExpectedInventoryNotionalJPY <= 500 {
		t.Fatalf("test must exercise unavoidable nonzero expected drift: %+v", d)
	}
	if d.ConfidenceLowerNotionalJPY < 390 || d.ConfidenceUpperNotionalJPY > 610 {
		t.Fatalf("asymmetric minimum orders escaped the configured confidence band: %+v", d)
	}
}

func TestProbabilityCenteredQuoteUsesDirectNonPoissonProbabilities(t *testing.T) {
	d := ProbabilityCenteredQuoteNotionals(ProbabilityCenteredQuoteInput{
		CurrentInventoryNotionalJPY: 500,
		TargetInventoryNotionalJPY:  500,
		LowerInventoryNotionalJPY:   0,
		UpperInventoryNotionalJPY:   1000,
		FastBuyNotionalJPY:          100,
		FastSellNotionalJPY:         100,
		MaxBuyNotionalJPY:           100,
		MaxSellNotionalJPY:          100,
		BuyFillRatePerHour:          99,
		SellFillRatePerHour:         99,
		DirectFillProbabilities:     true,
		BuyFillProbability:          0.6,
		SellFillProbability:         0.4,
		BothFillProbability:         0.25,
		Horizon:                     15 * time.Minute,
		ConfidenceZScore:            1,
	})
	if !d.Enabled {
		t.Fatalf("expected direct-probability projection, got %q", d.Reason)
	}
	if math.Abs(d.BuyFillProbability-0.6) > 1e-12 ||
		math.Abs(d.SellFillProbability-0.4) > 1e-12 {
		t.Fatalf("direct probabilities were transformed: buy=%v sell=%v", d.BuyFillProbability, d.SellFillProbability)
	}
	if math.Abs(d.BothFillProbability-0.25) > 1e-12 ||
		math.Abs(d.FillCovariance-0.01) > 1e-12 {
		t.Fatalf("unexpected joint probability/covariance: both=%v cov=%v", d.BothFillProbability, d.FillCovariance)
	}
}

func TestProbabilityCenteredQuoteJointCovarianceChangesRiskCapacity(t *testing.T) {
	base := ProbabilityCenteredQuoteInput{
		CurrentInventoryNotionalJPY: 500,
		TargetInventoryNotionalJPY:  500,
		LowerInventoryNotionalJPY:   250,
		UpperInventoryNotionalJPY:   750,
		FastBuyNotionalJPY:          1000,
		FastSellNotionalJPY:         1000,
		MaxBuyNotionalJPY:           1000,
		MaxSellNotionalJPY:          1000,
		DirectFillProbabilities:     true,
		BuyFillProbability:          0.5,
		SellFillProbability:         0.5,
		Horizon:                     15 * time.Minute,
		ConfidenceZScore:            1,
	}
	independent := base
	independent.BothFillProbability = 0.25
	positive := base
	positive.BothFillProbability = 0.5
	negative := base
	negative.BothFillProbability = 0
	independentDecision := ProbabilityCenteredQuoteNotionals(independent)
	positiveDecision := ProbabilityCenteredQuoteNotionals(positive)
	negativeDecision := ProbabilityCenteredQuoteNotionals(negative)
	if !independentDecision.Enabled || !positiveDecision.Enabled || !negativeDecision.Enabled {
		t.Fatalf("expected all covariance cases feasible: independent=%q positive=%q negative=%q",
			independentDecision.Reason, positiveDecision.Reason, negativeDecision.Reason)
	}
	if !(positiveDecision.ProjectedGrossNotionalJPY > independentDecision.ProjectedGrossNotionalJPY &&
		independentDecision.ProjectedGrossNotionalJPY > negativeDecision.ProjectedGrossNotionalJPY) {
		t.Fatalf("gross capacity must decrease with inventory-difference variance: positive=%v independent=%v negative=%v",
			positiveDecision.ProjectedGrossNotionalJPY,
			independentDecision.ProjectedGrossNotionalJPY,
			negativeDecision.ProjectedGrossNotionalJPY)
	}
}
func TestExposureUtilizationQuoteSizingUsesRiskMultiplier(t *testing.T) {
	d := ExposureUtilizationQuoteSizing(ExposureUtilizationSizingInput{
		ExecutableUnitJPY:                 100,
		RiskSizedNotionalJPY:              1_000,
		PairEquityJPY:                     10_000,
		CurrentInventoryNotionalJPY:       4_000,
		HardLowerInventoryNotionalJPY:     1_000,
		HardUpperInventoryNotionalJPY:     8_000,
		AvailableBuyCapitalJPY:            6_000,
		AvailableSellInventoryNotionalJPY: 4_000,
	})
	if !d.Enabled || math.Abs(d.RiskMultiplier-10) > 1e-12 ||
		math.Abs(d.BuyMultiplier-10) > 1e-12 ||
		math.Abs(d.SellMultiplier-10) > 1e-12 {
		t.Fatalf("risk capacity should scale the executable unit on both sides: %+v", d)
	}
	if math.Abs(d.GrossUtilizationRatio-0.2) > 1e-12 {
		t.Fatalf("unexpected gross capital utilization: %+v", d)
	}
}

func TestExposureUtilizationQuoteSizingUsesSideExposureHeadroom(t *testing.T) {
	d := ExposureUtilizationQuoteSizing(ExposureUtilizationSizingInput{
		ExecutableUnitJPY:                 100,
		RiskSizedNotionalJPY:              1_000,
		PairEquityJPY:                     10_000,
		CurrentInventoryNotionalJPY:       7_900,
		HardLowerInventoryNotionalJPY:     1_000,
		HardUpperInventoryNotionalJPY:     8_000,
		AvailableBuyCapitalJPY:            2_100,
		AvailableSellInventoryNotionalJPY: 7_900,
	})
	if math.Abs(d.BuyMultiplier-1) > 1e-12 || math.Abs(d.SellMultiplier-10) > 1e-12 {
		t.Fatalf("upper exposure must reduce only BUY capacity: %+v", d)
	}
	if math.Abs(d.CurrentExposureRatio-0.79) > 1e-12 ||
		math.Abs(d.BuyHeadroomRatio-0.01) > 1e-12 {
		t.Fatalf("unexpected exposure diagnostics: %+v", d)
	}
}

func TestExposureUtilizationQuoteSizingUsesAvailableCapital(t *testing.T) {
	d := ExposureUtilizationQuoteSizing(ExposureUtilizationSizingInput{
		ExecutableUnitJPY:                 100,
		RiskSizedNotionalJPY:              1_000,
		PairEquityJPY:                     10_000,
		CurrentInventoryNotionalJPY:       4_000,
		HardLowerInventoryNotionalJPY:     1_000,
		HardUpperInventoryNotionalJPY:     8_000,
		AvailableBuyCapitalJPY:            250,
		AvailableSellInventoryNotionalJPY: 600,
	})
	if math.Abs(d.BuyMultiplier-2.5) > 1e-12 ||
		math.Abs(d.SellMultiplier-6) > 1e-12 {
		t.Fatalf("available capital must cap each side independently: %+v", d)
	}
}

// Fast retains its stochastic risk capacity without a completed-path posterior;
// the Bernoulli quantity model, not an exchange-minimum fallback, allocates it.
func TestFastQuantityCapacityKeepsUnifiedRiskSizingWithoutPathEvidence(t *testing.T) {
	d := FastQuantityCapacity(ExposureUtilizationSizingInput{
		ExecutableUnitJPY:                 153.40,
		RiskSizedNotionalJPY:              2_733.20,
		PairEquityJPY:                     6_855,
		CurrentInventoryNotionalJPY:       3_500,
		HardLowerInventoryNotionalJPY:     0,
		HardUpperInventoryNotionalJPY:     6_855,
		AvailableBuyCapitalJPY:            3_355,
		AvailableSellInventoryNotionalJPY: 3_500,
	})
	if !d.Exposure.Enabled {
		t.Fatalf("expected risk capacity, got %+v", d)
	}
	if math.Abs(d.BaselineBuyCapJPY-2_733.20) > 1e-9 ||
		math.Abs(d.BaselineSellCapJPY-2_733.20) > 1e-9 {
		t.Fatalf("Fast baseline must retain the whole-position risk feasible set: %+v", d)
	}
	if math.Abs(d.PathModelBuyCapJPY-2_733.20) > 1e-9 ||
		math.Abs(d.PathModelSellCapJPY-2_733.20) > 1e-9 {
		t.Fatalf("joint Fast posterior must retain the full risk feasible set: %+v", d)
	}
}

func TestFastQuantityCapacityPreservesSideSpecificHardLimits(t *testing.T) {
	d := FastQuantityCapacity(ExposureUtilizationSizingInput{
		ExecutableUnitJPY:                 153.40,
		RiskSizedNotionalJPY:              2_733.20,
		PairEquityJPY:                     6_855,
		CurrentInventoryNotionalJPY:       6_800,
		HardLowerInventoryNotionalJPY:     0,
		HardUpperInventoryNotionalJPY:     6_855,
		AvailableBuyCapitalJPY:            55,
		AvailableSellInventoryNotionalJPY: 6_800,
	})
	if math.Abs(d.BaselineBuyCapJPY-55) > 1e-9 ||
		math.Abs(d.PathModelBuyCapJPY-55) > 1e-9 {
		t.Fatalf("neither Fast feasible set may bypass BUY headroom: %+v", d)
	}
	if math.Abs(d.BaselineSellCapJPY-2_733.20) > 1e-9 ||
		math.Abs(d.PathModelSellCapJPY-2_733.20) > 1e-9 {
		t.Fatalf("both SELL paths must retain independently bounded risk capacity: %+v", d)
	}
}

func TestSymmetricInventoryProjectionBoundsUsesNarrowerHardHeadroom(t *testing.T) {
	lower, upper := SymmetricInventoryProjectionBounds(4_000, 1_000, 8_000)
	if lower != 1_000 || upper != 7_000 {
		t.Fatalf("asymmetric hard band must not lend upper capacity to the lower tail: lower=%f upper=%f", lower, upper)
	}
	lower, upper = SymmetricInventoryProjectionBounds(9_000, 1_000, 8_000)
	if lower != 8_000 || upper != 8_000 {
		t.Fatalf("out-of-band target must collapse at the nearest hard boundary: lower=%f upper=%f", lower, upper)
	}
}
