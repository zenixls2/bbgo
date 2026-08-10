package gammacapture

import (
	"reflect"
	"testing"
)

func TestContinuationModeWithoutHealthyPosteriorIsExactQVFallback(t *testing.T) {
	in := noTradeTestInput()
	qv := EvaluateNoTradeInventory(NoTradeInventoryConfig{Enabled: true}, in)
	fallback := EvaluateNoTradeInventory(
		NoTradeInventoryConfig{Enabled: true, ContinuationMixtureEnabled: true}, in)
	if fallback.ContinuationMixtureApplied {
		t.Fatalf("unavailable continuation posterior was marked applied: %+v", fallback)
	}
	if !reflect.DeepEqual(qv, fallback) {
		t.Fatalf("feature flag changed QV fallback without a healthy posterior:\nqv=%+v\nfallback=%+v", qv, fallback)
	}
}
