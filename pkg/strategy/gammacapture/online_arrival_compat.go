package gammacapture

import (
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

// OnlineArrivalConfig is retained for decoding older YAML and checkpoints.
// The online arrival learner was retired; these values are no longer used by
// quote selection or startup replay.
type OnlineArrivalConfig struct {
	Enabled               bool           `json:"enabled" yaml:"enabled"`
	DistanceStepBps       float64        `json:"distanceStepBps" yaml:"distanceStepBps"`
	FastHalfLife          types.Duration `json:"fastHalfLife" yaml:"fastHalfLife"`
	SlowHalfLife          types.Duration `json:"slowHalfLife" yaml:"slowHalfLife"`
	PersistenceInterval   types.Duration `json:"persistenceInterval" yaml:"persistenceInterval"`
	StartupLookback       types.Duration `json:"startupLookback" yaml:"startupLookback"`
	StartupMaxAge         types.Duration `json:"startupMaxAge" yaml:"startupMaxAge"`
	RequireStartupHistory bool           `json:"requireStartupHistory" yaml:"requireStartupHistory"`
}

// OnlineArrivalState and OnlineArrivalCell preserve the JSON shape of old
// persisted strategy state. They are intentionally passive compatibility
// records; no runtime code allocates, updates, or reads them.
type OnlineArrivalState struct {
	Version               int                           `json:"version"`
	UpdatedAt             time.Time                     `json:"updatedAt,omitempty"`
	LastResolvedByHorizon map[string]time.Time          `json:"lastResolvedByHorizon,omitempty"`
	Cells                 map[string]*OnlineArrivalCell `json:"cells,omitempty"`
}

type OnlineArrivalCell struct {
	HorizonSeconds int64     `json:"horizonSeconds"`
	DistanceBps    float64   `json:"distanceBps"`
	FastUpEvents   float64   `json:"fastUpEvents"`
	FastDownEvents float64   `json:"fastDownEvents"`
	FastExposure   float64   `json:"fastExposureHours"`
	FastWindows    float64   `json:"fastEffectiveWindows"`
	SlowUpEvents   float64   `json:"slowUpEvents"`
	SlowDownEvents float64   `json:"slowDownEvents"`
	SlowExposure   float64   `json:"slowExposureHours"`
	SlowWindows    float64   `json:"slowEffectiveWindows"`
	FirstObserved  time.Time `json:"firstObserved,omitempty"`
	LastObserved   time.Time `json:"lastObserved,omitempty"`
}
