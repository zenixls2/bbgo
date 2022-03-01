package signals

import (
	"github.com/c9s/bbgo/pkg/signals/miningHamster"
	"github.com/c9s/bbgo/pkg/types"
)

func New() *types.Signals {
	var out types.Signals = types.Signals{
		miningHamster.New(),
	}
	return &out
}
