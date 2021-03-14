package cmd

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/c9s/bbgo/pkg/exchange/ftx"
	"github.com/c9s/bbgo/pkg/types"
	"github.com/c9s/bbgo/pkg/util"
)

// go run ./cmd/bbgo listorders [open|closed] --session=ftx --symbol=BTC/USDT
var listOrdersCmd = &cobra.Command{
	Use:  "listorders [status]",
	Args: cobra.OnlyValidArgs,
	// default is open which means we query open orders if you haven't provided args.
	ValidArgs:    []string{"", "open", "closed"},
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		session, err := cmd.Flags().GetString("session")
		if err != nil {
			return fmt.Errorf("can't get session from flags: %w", err)
		}
		ex, err := newExchange(session)
		if err != nil {
			return err
		}
		symbol, err := cmd.Flags().GetString("symbol")
		if err != nil {
			return fmt.Errorf("can't get the symbol from flags: %w", err)
		}
		if symbol == "" {
			return fmt.Errorf("symbol is not found")
		}

		status := "open"
		if len(args) != 0 {
			status = args[0]
		}

		var os []types.Order
		switch status {
		case "open":
			os, err = ex.QueryOpenOrders(ctx, symbol)
			if err != nil {
				return err
			}
		case "closed":
			panic("not implemented")
		default:
			return fmt.Errorf("invalid status %s", status)
		}

		for _, o := range os {
			log.Infof("%s orders: %+v", status, o)
		}

		return nil
	},
}

// go run ./cmd/bbgo scheduleorder --session=ftx --symbol=sol/usd
var scheduleOrderCmd = &cobra.Command{
	Use:          "scheduleorder",
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		timeLayout := "2006-01-02 15:04:05 -0700"
		type Pair struct {
			clientID string
			side     string
			price    float64
			quantity float64
		}
		// ---------------
		targetTime, err := time.Parse(timeLayout, "2021-03-16 22:00:00 +0800")
		if err != nil {
			return err
		}

		pairs := []Pair{
			{
				clientID: "small-one",
				side:     "buy",
				price:    0.25,
				quantity: 600,
			},
			{
				clientID: "small-two",
				side:     "buy",
				price:    0.375,
				quantity: 400,
			},
		}
		// ---------------
		ctx := context.Background()

		session, err := cmd.Flags().GetString("session")
		if err != nil {
			return fmt.Errorf("can't get session from flags: %w", err)
		}
		symbol, err := cmd.Flags().GetString("symbol")
		if err != nil {
			return fmt.Errorf("can't get the symbol from flags: %w", err)
		}
		if symbol == "" {
			return fmt.Errorf("symbol is not found")
		}

		log.Infof("will place %d orders at %+v", len(pairs), targetTime)
		ex, err := newExchange(session)
		if err != nil {
			panic(err)
		}
		timer := time.AfterFunc(targetTime.Add(-10*time.Millisecond).Sub(time.Now()), func() {
			for i := 0; i < 30; i++ {
				for _, p := range pairs {
					go func(pair Pair) {
						o := types.SubmitOrder{
							ClientOrderID: pair.clientID,
							Symbol:        symbol,
							Side:          types.SideType(ftx.TrimUpperString(pair.side)),
							Type:          types.OrderTypeLimit,
							Quantity:      pair.quantity,
							Price:         pair.price,
							TimeInForce:   "GTC",
						}
						for {
							timeoutCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
							log.Infof("place order: %+v", o)
							if _, err := ex.SubmitOrders(timeoutCtx, o); err != nil {
								if !strings.Contains(err.Error(), "No such market") {
									log.WithError(err).Warnf("failed to submit order and will try again immediately: %+v", o)
								}
								cancel()
								continue
							}
							log.Infof("order is placed: %+v", o)
							cancel()
							return
						}
					}(p)
				}
			}
		})
		defer timer.Stop()

		wg := sync.WaitGroup{}
		max := 10000
		wg.Add(max)
		timer2 := time.AfterFunc(targetTime.Add(-10*time.Millisecond).Sub(time.Now()), func() {
			for i := 0; i < max; i++ {
				o := types.SubmitOrder{
					Symbol:      symbol,
					Side:        types.SideType(ftx.TrimUpperString("sell")),
					Type:        types.OrderTypeLimit,
					Quantity:    100,
					Price:       0.125 * 10,
					TimeInForce: "GTC",
				}
				timedCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
				if _, err := ex.SubmitOrders(timedCtx, o); err != nil {
					log.WithError(err).Errorf("failed to sell: %+v", o)
				}
				cancel()
				wg.Done()
			}
			log.Infof("terminated bidding goroutine")
		})
		defer timer2.Stop()
		wg.Wait()
		log.Infof("done")
		return nil
	},
}

// go run ./cmd/bbgo placeorder --session=ftx --symbol=BTC/USDT --side=buy --price=<price> --quantity=<quantity>
var placeOrderCmd = &cobra.Command{
	Use:          "placeorder",
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		session, err := cmd.Flags().GetString("session")
		if err != nil {
			return fmt.Errorf("can't get session from flags: %w", err)
		}
		ex, err := newExchange(session)
		if err != nil {
			return err
		}
		symbol, err := cmd.Flags().GetString("symbol")
		if err != nil {
			return fmt.Errorf("can't get the symbol from flags: %w", err)
		}
		if symbol == "" {
			return fmt.Errorf("symbol is not found")
		}

		side, err := cmd.Flags().GetString("side")
		if err != nil {
			return fmt.Errorf("can't get side: %w", err)
		}
		price, err := cmd.Flags().GetString("price")
		if err != nil {
			return fmt.Errorf("can't get price: %w", err)
		}
		quantity, err := cmd.Flags().GetString("quantity")
		if err != nil {
			return fmt.Errorf("can't get quantity: %w", err)
		}

		so := types.SubmitOrder{
			ClientOrderID: uuid.New().String(),
			Symbol:        symbol,
			Side:          types.SideType(ftx.TrimUpperString(side)),
			Type:          types.OrderTypeLimit,
			Quantity:      util.MustParseFloat(quantity),
			Price:         util.MustParseFloat(price),
			Market:        types.Market{Symbol: symbol},
			TimeInForce:   "GTC",
		}
		co, err := ex.SubmitOrders(ctx, so)
		if err != nil {
			return err
		}

		log.Infof("submitted order: %+v\ncreated order: %+v", so, co[0])
		return nil
	},
}

func init() {
	listOrdersCmd.Flags().String("session", "", "the exchange session name for sync")
	listOrdersCmd.Flags().String("symbol", "", "the trading pair, like btcusdt")

	placeOrderCmd.Flags().String("session", "", "the exchange session name for sync")
	placeOrderCmd.Flags().String("symbol", "", "the trading pair, like btcusdt")
	placeOrderCmd.Flags().String("side", "", "the trading side: buy or sell")
	placeOrderCmd.Flags().String("price", "", "the trading price")
	placeOrderCmd.Flags().String("quantity", "", "the trading quantity")

	scheduleOrderCmd.Flags().String("session", "", "the exchange session name for sync")
	scheduleOrderCmd.Flags().String("symbol", "", "the trading pair, like btcusdt")

	RootCmd.AddCommand(listOrdersCmd)
	RootCmd.AddCommand(placeOrderCmd)
	RootCmd.AddCommand(scheduleOrderCmd)
}
