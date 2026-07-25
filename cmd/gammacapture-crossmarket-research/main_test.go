package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEvaluateUsesOnlyClosedReferenceMoveAndFees(t *testing.T) {
	target := []bar{{time: "a", close: 100}, {time: "b", close: 100}, {time: "c", close: 101}}
	reference := map[string]float64{"a": 100, "b": 100.2, "c": 100.2}
	r := evaluate(target, reference, 10, 1, 15)
	require.Equal(t, 1, r.Trades)
	require.InDelta(t, 100, r.GrossBps, 1e-12)
	require.InDelta(t, 85, r.NetBps, 1e-12)
	require.Equal(t, 1.0, r.WinRate)
}
