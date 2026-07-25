// gammacapture-data downloads and normalizes public aggregate-trade archives for replay.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/c9s/bbgo/pkg/datasource/csvsource"
	"github.com/c9s/bbgo/pkg/types"
)

func main() {
	var output, symbol, from, to string
	flag.StringVar(&output, "output", "data/gammacapture", "CSV source root")
	flag.StringVar(&symbol, "symbol", "BTCJPY", "Binance spot symbol")
	flag.StringVar(&from, "from", "", "inclusive UTC date (YYYY-MM-DD)")
	flag.StringVar(&to, "to", "", "inclusive UTC date (YYYY-MM-DD)")
	flag.Parse()
	if from == "" || to == "" {
		fmt.Fprintln(os.Stderr, "-from and -to are required")
		os.Exit(2)
	}
	start, err := time.Parse(time.DateOnly, from)
	if err != nil {
		fail(err)
	}
	end, err := time.Parse(time.DateOnly, to)
	if err != nil {
		fail(err)
	}
	if end.Before(start) {
		fail(fmt.Errorf("-to must not precede -from"))
	}
	path := output + "/binance/" + symbol
	if err := csvsource.Download(path, symbol, types.ExchangeBinance, csvsource.SPOT, csvsource.AGGTRADES, start, end); err != nil {
		fail(err)
	}
	fmt.Printf("normalized Binance aggregate trades: %s/aggTrades\n", path)
}

func fail(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
