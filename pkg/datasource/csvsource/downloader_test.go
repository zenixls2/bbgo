package csvsource

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/c9s/bbgo/pkg/types"
)

type DownloadTester struct {
	Exchange    types.ExchangeName
	Market      MarketType
	Granularity DataType
	Symbols     []string
	Path        string
}

var (
	expectedCandles = []int{864, 144, 72}
	intervals       = []types.Interval{types.Interval5m, types.Interval30m, types.Interval1h}
	symbols         = []string{"FXSUSDT", "BTCUSDT"}
	since           = time.Date(2023, 11, 17, 0, 0, 0, 0, time.UTC)
	until           = time.Date(2023, 11, 19, 0, 0, 0, 0, time.UTC)
)

func Test_CSV_Download(t *testing.T) {
	t.Skip()
	var tests = []DownloadTester{
		{
			Exchange:    types.ExchangeBinance,
			Market:      SPOT,
			Granularity: AGGTRADES,
			Symbols:     symbols,
			Path:        "testdata/binance",
		},
		{
			Exchange:    types.ExchangeBybit,
			Market:      FUTURES,
			Granularity: AGGTRADES,
			Symbols:     symbols,
			Path:        "testdata/bybit",
		},
		{
			Exchange:    types.ExchangeOKEx,
			Market:      SPOT,
			Granularity: AGGTRADES,
			Symbols:     symbols,
			Path:        "testdata/okex",
		},
	}

	for _, tt := range tests {
		for _, symbol := range tt.Symbols {
			path := filepath.Join(tt.Path, symbol)
			err := Download(
				path,
				symbol,
				tt.Exchange,
				tt.Market,
				tt.Granularity,
				since,
				until,
			)
			assert.NoError(t, err)

			klineMap, err := ReadTicksFromCSV(
				filepath.Join(path, string(tt.Granularity)),
				symbol,
				intervals,
			)
			assert.NoError(t, err)

			for i, interval := range intervals {
				klines := klineMap[interval]

				assert.Equal(
					t,
					expectedCandles[i],
					len(klines),
					fmt.Sprintf("%s: %s/%s should have %d kLines",
						tt.Exchange.String(),
						symbol,
						interval.String(),
						expectedCandles[i],
					),
				)

				err = WriteKLines(path, symbol, klines)
				assert.NoError(t, err)
			}
		}
	}
}

func TestNormalizeBinanceTimestampMillis(t *testing.T) {
	tests := []struct {
		name, input, want string
	}{
		{"milliseconds", "1783555200836", "1783555200836"},
		{"microseconds", "1783555200836321", "1783555200836"},
		{"nanoseconds", "1783555200836321000", "1783555200836"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeBinanceTimestampMillis(tt.input)
			assert.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
