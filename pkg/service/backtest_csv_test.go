package service

import (
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/types"
	"github.com/stretchr/testify/require"
)

func TestBacktestServiceCSVQueryKLinesChFiltersRequestedRange(t *testing.T) {
	start := time.Date(2026, time.July, 9, 0, 0, 0, 0, time.UTC)
	service := &BacktestServiceCSV{kLines: map[types.Interval][]types.KLine{
		types.Interval1m: {
			{StartTime: types.Time(start.Add(-time.Minute))},
			{StartTime: types.Time(start)},
			{StartTime: types.Time(start.Add(time.Minute))},
			{StartTime: types.Time(start.Add(2 * time.Minute))},
		},
	}}

	ch, errCh := service.QueryKLinesCh(start, start.Add(2*time.Minute), nil, []string{"BTCJPY"}, []types.Interval{types.Interval1m})
	require.Nil(t, errCh)

	var got []types.KLine
	for k := range ch {
		got = append(got, k)
	}
	require.Len(t, got, 2)
	require.Equal(t, start, got[0].StartTime.Time())
	require.Equal(t, start.Add(time.Minute), got[1].StartTime.Time())
}

func TestBacktestServiceCSVQueryKLinesChUsesSmallestInterval(t *testing.T) {
	start := time.Date(2026, time.July, 9, 0, 0, 0, 0, time.UTC)
	service := &BacktestServiceCSV{kLines: map[types.Interval][]types.KLine{
		types.Interval1s: {{StartTime: types.Time(start), Interval: types.Interval1s}},
		types.Interval1m: {{StartTime: types.Time(start), Interval: types.Interval1m}},
	}}
	ch, errCh := service.QueryKLinesCh(start, start.Add(time.Minute), nil, []string{"BTCJPY"}, []types.Interval{types.Interval1m, types.Interval1s})
	require.Nil(t, errCh)
	got := <-ch
	require.Equal(t, types.Interval1s, got.Interval)
	_, open := <-ch
	require.False(t, open)
}
