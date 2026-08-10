package main

import (
	"testing"

	"github.com/c9s/bbgo/pkg/strategy/gammacapture"
)

func TestContinuationPosteriorQVSelectorContainsOnlyTwoComparableTargets(t *testing.T) {
	variants := selectNoTradeIOCVariants(
		noTradeIOCVariants(gammacapture.MarketMakerConfig{}),
		"qv-continuation-mixture+ioc", "qv-only+ioc")
	if len(variants) != 2 || !variants[0].ContinuationMixtureEnabled ||
		variants[1].ContinuationMixtureEnabled || variants[1].TrendExcursionEnabled ||
		variants[1].ContinuationEnabled {
		t.Fatalf("unexpected focused range variants: %+v", variants)
	}
}
