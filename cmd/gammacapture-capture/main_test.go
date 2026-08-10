package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCSVFileWritesHeaderAndRecords(t *testing.T) {
	dir := t.TempDir()
	f, err := newDailyCSV(dir, "ETHJPY", "trades", []string{"time", "price"})
	require.NoError(t, err)
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, f.write(at, []string{"2026-01-01T00:00:00Z", "100"}))
	require.NoError(t, f.close())

	path := filepath.Join(dir, "ETHJPY-trades-2026-01-01.csv")
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "time,price\n2026-01-01T00:00:00Z,100\n", string(content))
}
