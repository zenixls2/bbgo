package gammacapture

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGateStatsIncludesSequentialRates(t *testing.T) {
	stats := GateStats{
		Time:             time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		FlatObservations: 100,
		Cooldown:         20,
		Healthy:          40,
		RawSignalUp:      5,
		ProbabilityReady: 10,
		SignalUp:         4,
		SignalWindow:     8,
		Probability:      6,
		Expectancy:       3,
		BarQuality:       2,
		Trend:            2,
		Retrace:          1,
		Book:             1,
		Balance:          1,
		Quantity:         1,
		Entries:          1,
	}

	header := stats.CsvHeader()
	record := stats.CsvRecords()[0]
	require.Len(t, record, len(header))
	require.Equal(t, "eligible_after_cooldown", header[3])
	require.Equal(t, "80", record[3])
	require.Equal(t, "50.0000", record[indexOf(t, header, "healthy_rate_pct")])
	require.Equal(t, "5.0000", record[indexOf(t, header, "raw_signal_rate_from_flat_pct")])
	require.Equal(t, "75.0000", record[indexOf(t, header, "probability_rate_from_signal_window_pct")])
	require.Equal(t, "100.0000", record[indexOf(t, header, "book_rate_from_retrace_pct")])
	require.Equal(t, "100.0000", record[indexOf(t, header, "entry_rate_from_quantity_pct")])
}

func TestFormatGateRateWithoutDenominator(t *testing.T) {
	require.Empty(t, formatGateRate(1, 0))
}

func indexOf(t *testing.T, values []string, want string) int {
	t.Helper()
	for i, value := range values {
		if value == want {
			return i
		}
	}
	t.Fatalf("%q not present", want)
	return -1
}
