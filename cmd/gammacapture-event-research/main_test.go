package main

import (
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/strategy/gammacapture"
	"github.com/stretchr/testify/require"
)

func TestDateFromFilename(t *testing.T) {
	got, err := dateFromFilename("data/BTCJPY-2025-07-01.csv")
	require.NoError(t, err)
	require.Equal(t, time.Date(2025, time.July, 1, 0, 0, 0, 0, time.UTC), got)
}

func TestExpectedNetEdgeBps(t *testing.T) {
	got := expectedNetEdgeBps(.001, 8, 2, 25, gammacapture.FirstPassage{TP: .75, SL: .2})
	require.InDelta(t, 31, got, 1e-12)
}

func TestCalibrationBucketsRecordPredictionAndOutcome(t *testing.T) {
	r := report{Calibration: makeCalibrationBuckets(10)}
	r.recordCalibration(&trade{PredictedTP: .62, Outcome: "target", NetJPY: 10})
	r.recordCalibration(&trade{PredictedTP: .64, Outcome: "stop", NetJPY: -5})
	bucket := r.Calibration[6]
	require.Equal(t, 2, bucket.Count)
	require.Equal(t, 1, bucket.TargetHits)
	require.Equal(t, 1, bucket.StopHits)
	require.Equal(t, 1, bucket.PositiveNet)
	require.InDelta(t, 1.26, bucket.PredictedTPSum, 1e-12)
	require.InDelta(t, 5, bucket.NetSumJPY, 1e-12)
}
