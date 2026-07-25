// gammacapture-market-research lists the currently advertised JPY spot markets
// before a candidate is added to the strategy's allowlist.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/c9s/bbgo/pkg/exchange"
	"github.com/c9s/bbgo/pkg/types"
)

func main() {
	quote := flag.String("quote", "JPY", "quote currency to select")
	flag.Parse()

	ex, err := exchange.NewPublic(types.ExchangeBinance)
	if err != nil {
		fatalf("create Binance client: %v", err)
	}
	service, ok := ex.(types.ExchangeMarketDataService)
	if !ok {
		fatalf("Binance client does not implement market-data service")
	}
	markets, err := service.QueryMarkets(context.Background())
	if err != nil {
		fatalf("query markets: %v", err)
	}

	var selected []types.Market
	for _, market := range markets {
		if market.QuoteCurrency == *quote {
			selected = append(selected, market)
		}
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].Symbol < selected[j].Symbol })
	if err := json.NewEncoder(os.Stdout).Encode(selected); err != nil {
		fatalf("encode markets: %v", err)
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(2)
}
