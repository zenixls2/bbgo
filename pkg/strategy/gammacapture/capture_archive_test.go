package gammacapture

import (
	"encoding/csv"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestCaptureFilesOverlappingDailyAndLegacy(t *testing.T) {
	files := []string{
		"/capture/ETHJPY-bookticker-2026-08-03.csv",
		"/capture/ETHJPY-bookticker-2026-08-04.csv",
		"/capture/ETHJPY-bookticker-20260801T010203Z.csv",
	}
	cutoff := time.Date(2026, 8, 4, 6, 0, 0, 0, time.UTC)
	got := captureFilesOverlapping(files, "ETHJPY", "bookticker", cutoff, cutoff.Add(24*time.Hour))
	if len(got) != 1 || got[0] != files[1] {
		t.Fatalf("unexpected files: %v", got)
	}
}

func TestCaptureFilesOverlappingFallsBackToLegacy(t *testing.T) {
	files := []string{
		"/capture/ETHJPY-bookticker-20260801T010203Z.csv",
		"/capture/ETHJPY-bookticker-20260804T010203Z.csv",
	}
	cutoff := time.Date(2026, 8, 4, 6, 0, 0, 0, time.UTC)
	got := captureFilesOverlapping(files, "ETHJPY", "bookticker", cutoff, cutoff.Add(24*time.Hour))
	if len(got) != len(files) {
		t.Fatalf("legacy fallback was pruned: %v", got)
	}
}

func TestIndexedCaptureReaderStartsAtSafeMinute(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ETHJPY-bookticker-2026-08-05.csv")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := csv.NewWriter(file)
	_ = writer.Write([]string{"received_at", "bid"})
	writer.Flush()
	offset1, _ := file.Seek(0, io.SeekCurrent)
	_ = writer.Write([]string{"2026-08-05T12:00:01Z", "100"})
	writer.Flush()
	offset2, _ := file.Seek(0, io.SeekCurrent)
	_ = writer.Write([]string{"2026-08-05T12:01:01Z", "101"})
	writer.Flush()
	_ = file.Close()

	index, err := os.Create(path + ".index.csv")
	if err != nil {
		t.Fatal(err)
	}
	indexWriter := csv.NewWriter(index)
	_ = indexWriter.Write([]string{"minute_utc", "byte_offset"})
	_ = indexWriter.Write([]string{"2026-08-05T12:00:00Z", strconv.FormatInt(offset1, 10)})
	_ = indexWriter.Write([]string{"2026-08-05T12:01:00Z", strconv.FormatInt(offset2, 10)})
	indexWriter.Flush()
	_ = index.Close()

	opened, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	reader, err := newIndexedCaptureReader(opened, path, time.Date(2026, 8, 5, 12, 1, 30, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	record, err := reader.Read()
	if err != nil {
		t.Fatal(err)
	}
	if record[0] != "2026-08-05T12:01:01Z" {
		t.Fatalf("unexpected first indexed row: %v", record)
	}
}

func TestCompletedCaptureFileIsSkippedOnlyWhileUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ETHJPY-bookticker-legacy.csv")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := csv.NewWriter(file)
	_ = writer.Write([]string{"received_at", "bid"})
	first := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	_ = writer.Write([]string{first.Format(time.RFC3339Nano), "100"})
	writer.Flush()
	_ = file.Close()

	checkpoint, ok := captureFileCheckpointIfComplete(path, first)
	if !ok {
		t.Fatal("fully consumed capture file was not checkpointed")
	}
	completed := map[string]captureFileCheckpoint{path: checkpoint}
	if remaining := captureFilesChangedSinceCheckpoint([]string{path}, completed); len(remaining) != 0 {
		t.Fatalf("unchanged completed file was replayed: %v", remaining)
	}

	file, err = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	writer = csv.NewWriter(file)
	_ = writer.Write([]string{first.Add(time.Minute).Format(time.RFC3339Nano), "101"})
	writer.Flush()
	_ = file.Close()
	if remaining := captureFilesChangedSinceCheckpoint([]string{path}, completed); len(remaining) != 1 {
		t.Fatalf("changed capture file was incorrectly skipped: %v", remaining)
	}
	if _, ok := captureFileCheckpointIfComplete(path, first); ok {
		t.Fatal("capture file with a newer tail was marked complete")
	}
}
