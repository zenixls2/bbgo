package gammacapture

import "math"

// OFIVolumeAgreementConfig enables a conservative public-data agreement check.
// It is an auxiliary gate: a disagreement removes only the volume component
// from the joint quote signal; inventory, direction and book-risk controls
// remain active.
type OFIVolumeAgreementConfig struct {
	Enabled                bool    `json:"enabled" yaml:"enabled"`
	MinOFI                 float64 `json:"minOFI" yaml:"minOFI"`
	MinVolume              float64 `json:"minVolume" yaml:"minVolume"`
	SuppressOnDisagreement bool    `json:"suppressOnDisagreement" yaml:"suppressOnDisagreement"`
}

type OFIVolumeAgreementSnapshot struct {
	Ready   bool
	Agrees  bool
	Applied bool
	OFI     float64
	Volume  float64
	Reason  string
}

func signOf(v float64) int {
	if v > 0 {
		return 1
	}
	if v < 0 {
		return -1
	}
	return 0
}

func evaluateOFIVolumeAgreement(cfg OFIVolumeAgreementConfig, ofi, volume float64) OFIVolumeAgreementSnapshot {
	s := OFIVolumeAgreementSnapshot{OFI: ofi, Volume: volume, Agrees: true}
	if !cfg.Enabled {
		s.Reason = "disabled"
		return s
	}
	if !math.IsNaN(ofi) && !math.IsInf(ofi, 0) && !math.IsNaN(volume) && !math.IsInf(volume, 0) && math.Abs(ofi) >= cfg.MinOFI && math.Abs(volume) >= cfg.MinVolume {
		s.Ready = true
		s.Agrees = signOf(ofi) == signOf(volume)
		if s.Agrees {
			s.Reason = "agree"
		} else {
			s.Reason = "disagree"
		}
		return s
	}
	s.Reason = "insufficient-evidence"
	return s
}
