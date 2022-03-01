package miningHamster

import (
	"time"
	"github.com/c9s/bbgo/pkg/types"
)

type MiningHamster struct {
	apiKey string
}

func New() types.Signal {
	return &MiningHamster {
		apiKey: "",
	}
}

func (self *MiningHamster) Init() error {
	return nil
}

func (self *MiningHamster) GetName() string {
	return "MiningHamster"
}

func (self *MiningHamster) GetValidity() (time.Duration, error) {
	return 5 * time.Minute, nil
}

func (self *MiningHamster) GetRateLimit() time.Duration {
	return 30 * time.Second
}

func (self *MiningHamster) GetOrderType() types.OrderType {
	return types.OrderTypeMarket
}

func (self *MiningHamster) GetMarkets() {
}

// TODO: process signals
