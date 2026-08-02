package gammacapture

import "testing"

func TestEvaluateOFIVolumeAgreement(t *testing.T) {
	cfg := OFIVolumeAgreementConfig{Enabled: true, MinOFI: 0.1, MinVolume: 0.1, SuppressOnDisagreement: true}
	got := evaluateOFIVolumeAgreement(cfg, 0.4, 0.2)
	if !got.Ready || !got.Agrees || got.Reason != "agree" {
		t.Fatalf("expected agreement, got %+v", got)
	}
	got = evaluateOFIVolumeAgreement(cfg, -0.4, 0.2)
	if !got.Ready || got.Agrees || got.Reason != "disagree" {
		t.Fatalf("expected disagreement, got %+v", got)
	}
	got = evaluateOFIVolumeAgreement(cfg, 0.02, 0.8)
	if got.Ready || got.Reason != "insufficient-evidence" {
		t.Fatalf("expected insufficient evidence, got %+v", got)
	}
}
