package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadBBOFilesCompactedCompactsWarmupBeforeReplay(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ETHJPY-bookticker-test.csv")
	data := "received_at,bid,bid_quantity,ask,ask_quantity,gap_before_ms\n" +
		"2026-08-03T00:00:00.100Z,100,1,101,2,0\n" +
		"2026-08-03T00:00:00.700Z,101,1,102,2,0\n" +
		"2026-08-03T00:00:01.200Z,102,1,103,2,0\n" +
		"2026-08-03T00:00:02.100Z,103,1,104,2,0\n" +
		"2026-08-03T00:00:02.700Z,104,1,105,2,0\n" +
		"2026-08-03T00:00:04.100Z,105,1,106,2,0\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	exactFrom := from.Add(2 * time.Second)
	to := from.Add(5 * time.Second)
	books := readBBOFilesCompacted([]string{path}, from, to, exactFrom, 2*time.Second)
	if len(books) != 4 {
		t.Fatalf("expected two one-second warmup closes and two exact buckets, got %d: %+v", len(books), books)
	}
	want := []float64{101, 102, 104, 105}
	for i, bid := range want {
		if books[i].bid != bid {
			t.Fatalf("book %d bid=%v want %v: %+v", i, books[i].bid, bid, books)
		}
	}
}
