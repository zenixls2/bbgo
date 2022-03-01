package types

import (
	"errors"
	"strings"
	"time"
)

type Signals []Signal

type Signal interface {
	Init() error
	GetName() string
	GetOrderType() OrderType
	GetRateLimit() time.Duration
	GetValidity() (time.Duration, error)
}

func (signals *Signals) FindByName(name string) (Signal, error) {
	for _, signal := range *signals {
		if strings.EqualFold(signal.GetName(), name) {
			err := signal.Init()
			return signal, err
		}
	}
	return nil, errors.New("Cannot find siganl with name: " + name)
}
