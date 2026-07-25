package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSummarizeCalculatesSpreadAndTradeBookAge(t *testing.T) {
	t0 := time.Unix(0, 0)
	books := []bookSample{
		{received: t0, bid: 100, ask: 100.05},
		{received: t0.Add(time.Second), bid: 100, ask: 100.2},
	}
	r := summarize(books, []time.Time{t0.Add(500 * time.Millisecond), t0.Add(1500 * time.Millisecond)}, 10, time.Second)
	require.Equal(t, 2, r.BookUpdates)
	require.Equal(t, 2, r.TradeEvents)
	require.Equal(t, 2, r.PositiveSpreads)
	require.InDelta(t, 50, r.WithinMaxSpreadPct, 1e-9)
	require.InDelta(t, .5, r.TradeBookAgeP95Second, 1e-9)
	require.InDelta(t, 1, r.BookGapMaxSeconds, 1e-9)
	require.Equal(t, 2, r.TradesPassingBookAge)
	require.Equal(t, 100.0, r.TradeBookPassRatePct)
}

func TestQuantileEmpty(t *testing.T) {
	require.Zero(t, quantile(nil, .95))
}
