package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReplayCaptureFilesPreferDailyLayout(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "ETHJPY-bookticker-20260801T010203Z.csv")
	daily := filepath.Join(dir, "ETHJPY-bookticker-2026-08-01.csv")
	for _, filename := range []string{legacy, daily} {
		if err := os.WriteFile(filename, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := replayCaptureFiles(dir, "ETHJPY", "bookticker")
	if len(got) != 1 || got[0] != daily {
		t.Fatalf("expected only daily capture, got %v", got)
	}
}

func TestReplayCaptureFilesLegacyFallback(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "ETHJPY-trades-20260801T010203Z.csv")
	if err := os.WriteFile(legacy, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	got := replayCaptureFiles(dir, "ETHJPY", "trades")
	if len(got) != 1 || got[0] != legacy {
		t.Fatalf("expected legacy fallback, got %v", got)
	}
}

func TestReplayCaptureFilesFindsSymbolSubdirectory(t *testing.T) {
	dir := t.TempDir()
	symbolDir := filepath.Join(dir, "ETHJPY")
	if err := os.Mkdir(symbolDir, 0o755); err != nil {
		t.Fatal(err)
	}
	daily := filepath.Join(symbolDir, "ETHJPY-bookticker-2026-08-01.csv")
	if err := os.WriteFile(daily, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	got := replayCaptureFiles(dir, "ETHJPY", "bookticker")
	if len(got) != 1 || got[0] != daily {
		t.Fatalf("expected daily capture in symbol subdirectory, got %v", got)
	}
}

func TestReplayCaptureFilesOverlappingPrunesDailyFiles(t *testing.T) {
	dir := t.TempDir()
	files := make([]string, 0, 2)
	for _, day := range []string{"2026-08-01", "2026-08-02"} {
		filename := filepath.Join(dir, "ETHJPY-bookticker-"+day+".csv")
		if err := os.WriteFile(filename, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		files = append(files, filename)
	}
	from := parseTime("2026-08-02T12:00:00Z")
	to := parseTime("2026-08-02T13:00:00Z")
	got := replayCaptureFilesOverlapping(files, "ETHJPY", "bookticker", from, to)
	if len(got) != 1 || got[0] != files[1] {
		t.Fatalf("expected only overlapping day, got %v", got)
	}
}
