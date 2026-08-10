package main

import (
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestDailyCSVRotatesAtUTCMidnight(t *testing.T) {
	dir := t.TempDir()
	f, err := newDailyCSV(dir, "ETHJPY", "bookticker", []string{"received_at", "bid"})
	if err != nil {
		t.Fatal(err)
	}
	first := time.Date(2026, 8, 5, 23, 59, 59, 0, time.UTC)
	second := first.Add(2 * time.Second)
	if err := f.write(first, []string{first.Format(time.RFC3339Nano), "100"}); err != nil {
		t.Fatal(err)
	}
	if err := f.write(second, []string{second.Format(time.RFC3339Nano), "101"}); err != nil {
		t.Fatal(err)
	}
	if err := f.close(); err != nil {
		t.Fatal(err)
	}

	for _, date := range []string{"2026-08-05", "2026-08-06"} {
		path := filepath.Join(dir, "ETHJPY-bookticker-"+date+".csv")
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		records, err := csv.NewReader(file).ReadAll()
		_ = file.Close()
		if err != nil {
			t.Fatal(err)
		}
		if len(records) != 2 {
			t.Fatalf("%s records=%d, want header plus one row", date, len(records))
		}
		var metadata captureFileMetadata
		data, err := os.ReadFile(path + ".meta.json")
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &metadata); err != nil {
			t.Fatal(err)
		}
		if metadata.Rows != 1 || metadata.DateUTC != date {
			t.Fatalf("unexpected metadata: %+v", metadata)
		}
	}
}

func TestDailyCSVAppendsWithoutDuplicateHeader(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 2; i++ {
		f, err := newDailyCSV(dir, "ETHJPY", "trades", []string{"received_at", "price"})
		if err != nil {
			t.Fatal(err)
		}
		when := at.Add(time.Duration(i) * time.Second)
		if err := f.write(when, []string{when.Format(time.RFC3339Nano), "100"}); err != nil {
			t.Fatal(err)
		}
		if err := f.close(); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "ETHJPY-trades-2026-08-05.csv")
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	records, err := csv.NewReader(file).ReadAll()
	_ = file.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 {
		t.Fatalf("records=%d, want one header and two rows", len(records))
	}
	metadataBytes, err := os.ReadFile(path + ".meta.json")
	if err != nil {
		t.Fatal(err)
	}
	var metadata captureFileMetadata
	if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.Rows != 2 || !metadata.First.Equal(at) || !metadata.Last.Equal(at.Add(time.Second)) {
		t.Fatalf("metadata did not survive append restart: %+v", metadata)
	}
	indexFile, err := os.Open(path + ".index.csv")
	if err != nil {
		t.Fatal(err)
	}
	indexRecords, err := csv.NewReader(indexFile).ReadAll()
	_ = indexFile.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(indexRecords) != 2 {
		t.Fatalf("index records=%d, want one header and one minute", len(indexRecords))
	}
}

func TestDailyCSVRestartIndexesNewMinuteAtFileEnd(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	write := func(when time.Time) {
		t.Helper()
		f, err := newDailyCSV(dir, "ETHJPY", "bookticker", []string{"received_at", "bid"})
		if err != nil {
			t.Fatal(err)
		}
		if err := f.write(when, []string{when.Format(time.RFC3339Nano), "100"}); err != nil {
			t.Fatal(err)
		}
		if err := f.close(); err != nil {
			t.Fatal(err)
		}
	}
	write(at)
	path := filepath.Join(dir, "ETHJPY-bookticker-2026-08-05.csv")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	write(at.Add(time.Minute))
	indexFile, err := os.Open(path + ".index.csv")
	if err != nil {
		t.Fatal(err)
	}
	records, err := csv.NewReader(indexFile).ReadAll()
	_ = indexFile.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 {
		t.Fatalf("index records=%d, want header and two minutes", len(records))
	}
	offset, err := strconv.ParseInt(records[2][1], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	if offset != before.Size() {
		t.Fatalf("restart minute offset=%d, want previous file size=%d", offset, before.Size())
	}
}
