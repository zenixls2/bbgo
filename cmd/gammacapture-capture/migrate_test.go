package main

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMigrateLegacyCaptureFilesPreservesOriginalsAndSplitsUTC(t *testing.T) {
	root := t.TempDir()
	symbol := "ETHJPY"
	tradeLegacy := filepath.Join(root, symbol+"-trades-20260805T010000Z.csv")
	bookLegacy := filepath.Join(root, symbol+"-bookticker-20260805T010000Z.csv")
	writeTestCSV(t, tradeLegacy,
		[]string{"event_time", "received_at", "id", "price", "quantity", "side"},
		[][]string{
			{"2026-08-05T23:59:58Z", "2026-08-05T23:59:59Z", "1", "100", "1", "BUY"},
			{"2026-08-06T00:00:00Z", "2026-08-06T00:00:01Z", "2", "101", "1", "SELL"},
		})
	writeTestCSV(t, bookLegacy,
		[]string{"received_at", "bid", "bid_quantity", "ask", "ask_quantity"},
		[][]string{{"2026-08-05T23:59:59Z", "99", "1", "100", "1"}})

	result, err := migrateLegacyCaptureFiles(root, symbol)
	if err != nil {
		t.Fatal(err)
	}
	if result.InputFiles != 2 || result.Rows != 3 || result.DailyFiles != 3 {
		t.Fatalf("unexpected migration result: %+v", result)
	}
	for _, original := range []string{tradeLegacy, bookLegacy} {
		if _, err := os.Stat(original); err != nil {
			t.Fatalf("original was not preserved: %s: %v", original, err)
		}
	}
	for _, daily := range []string{
		symbol + "-trades-2026-08-05.csv",
		symbol + "-trades-2026-08-06.csv",
		symbol + "-bookticker-2026-08-05.csv",
	} {
		path := filepath.Join(root, daily)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("missing daily file %s: %v", daily, err)
		}
		if _, err := os.Stat(path + ".index.csv"); err != nil {
			t.Fatalf("missing daily index %s: %v", daily, err)
		}
		if _, err := os.Stat(path + ".meta.json"); err != nil {
			t.Fatalf("missing daily metadata %s: %v", daily, err)
		}
	}
	trade, err := os.Open(filepath.Join(root, symbol+"-trades-2026-08-05.csv"))
	if err != nil {
		t.Fatal(err)
	}
	records, err := csv.NewReader(trade).ReadAll()
	_ = trade.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || len(records[0]) != len(tradeCaptureHeader) || len(records[1]) != len(tradeCaptureHeader) {
		t.Fatalf("legacy trade schema was not normalized: %v", records)
	}
	if _, err := migrateLegacyCaptureFiles(root, symbol); err == nil {
		t.Fatal("second migration unexpectedly overwrote existing daily files")
	}
}

func writeTestCSV(t *testing.T, path string, header []string, rows [][]string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := csv.NewWriter(file)
	if err := writer.Write(header); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if err := writer.Write(row); err != nil {
			t.Fatal(err)
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestIsDailyCaptureFilename(t *testing.T) {
	if !isDailyCaptureFilename("ETHJPY-bookticker-2026-08-05.csv", "ETHJPY", "bookticker") {
		t.Fatal("daily capture filename was not recognized")
	}
	if isDailyCaptureFilename("ETHJPY-bookticker-20260805T010000Z.csv", "ETHJPY", "bookticker") {
		t.Fatal("legacy capture filename was misclassified as daily")
	}
	_ = time.UTC
}
