package csvsource

import (
	"testing"

	"github.com/c9s/bbgo/pkg/fixedpoint"
	"github.com/c9s/bbgo/pkg/types"
	"github.com/stretchr/testify/require"
)

func TestCSVTickConverterStartsNewCandleAtExactBoundary(t *testing.T) {
	converter := NewCSVTickConverter([]types.Interval{types.Interval1m})
	converter.CsvTickToKLine(&CsvTick{
		Exchange: types.ExchangeBinance, Symbol: "BTCJPY", Price: fixedpoint.NewFromInt(100), HomeNotional: fixedpoint.One, ForeignNotional: fixedpoint.NewFromInt(100),
		Timestamp: types.NewMillisecondTimestampFromInt(1_700_000_000_000),
	})
	converter.CsvTickToKLine(&CsvTick{
		Exchange: types.ExchangeBinance, Symbol: "BTCJPY", Price: fixedpoint.NewFromInt(101), HomeNotional: fixedpoint.One, ForeignNotional: fixedpoint.NewFromInt(101),
		Timestamp: types.NewMillisecondTimestampFromInt(1_700_000_040_000),
	})

	klines := converter.GetKLineResults()[types.Interval1m]
	require.Len(t, klines, 2)
	require.Equal(t, fixedpoint.NewFromInt(100), klines[0].Close)
	require.Equal(t, fixedpoint.NewFromInt(101), klines[1].Open)
}

func TestCSVTickConverterFillsGapToTheIncomingTickTime(t *testing.T) {
	converter := NewCSVTickConverter([]types.Interval{types.Interval1m})
	converter.CsvTickToKLine(&CsvTick{
		Exchange: types.ExchangeBinance, Symbol: "BTCJPY", Price: fixedpoint.NewFromInt(100), HomeNotional: fixedpoint.One, ForeignNotional: fixedpoint.NewFromInt(100),
		Timestamp: types.NewMillisecondTimestampFromInt(1_700_000_040_000),
	})
	converter.CsvTickToKLine(&CsvTick{
		Exchange: types.ExchangeBinance, Symbol: "BTCJPY", Price: fixedpoint.NewFromInt(101), HomeNotional: fixedpoint.One, ForeignNotional: fixedpoint.NewFromInt(101),
		Timestamp: types.NewMillisecondTimestampFromInt(1_700_000_160_000),
	})

	klines := converter.GetKLineResults()[types.Interval1m]
	require.Len(t, klines, 3)
	require.Equal(t, int64(1_700_000_100), klines[1].StartTime.Unix())
	require.Equal(t, int64(1_700_000_160), klines[2].StartTime.Unix())
	require.Equal(t, fixedpoint.NewFromInt(101), klines[2].Open)
}
