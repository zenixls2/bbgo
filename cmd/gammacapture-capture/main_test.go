package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCSVFileWritesHeaderAndRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.csv")
	f, err := newCSV(path, []string{"time", "price"})
	require.NoError(t, err)
	f.write([]string{"2026-01-01T00:00:00Z", "100"})
	f.close()

	content, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "time,price\n2026-01-01T00:00:00Z,100\n", string(content))
}
