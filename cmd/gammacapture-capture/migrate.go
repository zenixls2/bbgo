package main

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var (
	tradeCaptureHeader = []string{"event_time", "received_at", "id", "price", "quantity", "side", "aggregate_id", "first_trade_id", "last_trade_id", "gap_before_ms", "source"}
	bookCaptureHeader  = []string{"received_at", "bid", "bid_quantity", "ask", "ask_quantity", "gap_before_ms"}
)

type captureMigrationResult struct {
	InputFiles int
	Rows       int64
	DailyFiles int
}

func migrateLegacyCaptureFiles(output, symbol string) (captureMigrationResult, error) {
	temporary, err := os.MkdirTemp(output, ".daily-migration-*")
	if err != nil {
		return captureMigrationResult{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(temporary)
		}
	}()

	result := captureMigrationResult{}
	streams := []struct {
		name           string
		header         []string
		timestampIndex int
	}{
		{name: "trades", header: tradeCaptureHeader, timestampIndex: 1},
		{name: "bookticker", header: bookCaptureHeader, timestampIndex: 0},
	}
	for _, stream := range streams {
		files, globErr := filepath.Glob(filepath.Join(output, symbol+"-"+stream.name+"-*.csv"))
		if globErr != nil {
			return result, globErr
		}
		sort.Strings(files)
		writer, writerErr := newDailyCSV(temporary, symbol, stream.name, stream.header)
		if writerErr != nil {
			return result, writerErr
		}
		for _, filename := range files {
			if isDailyCaptureFilename(filename, symbol, stream.name) {
				continue
			}
			rows, readErr := migrateLegacyCaptureFile(filename, writer, stream.timestampIndex, len(stream.header))
			if readErr != nil {
				_ = writer.close()
				return result, readErr
			}
			result.InputFiles++
			result.Rows += rows
		}
		if closeErr := writer.close(); closeErr != nil {
			return result, closeErr
		}
	}
	if result.InputFiles == 0 || result.Rows == 0 {
		return result, fmt.Errorf("no legacy capture rows found for %s under %s", symbol, output)
	}

	artifacts, err := filepath.Glob(filepath.Join(temporary, "*"))
	if err != nil {
		return result, err
	}
	sort.Strings(artifacts)
	for _, artifact := range artifacts {
		target := filepath.Join(output, filepath.Base(artifact))
		if _, statErr := os.Stat(target); statErr == nil {
			return result, fmt.Errorf("daily migration target already exists: %s", target)
		} else if !os.IsNotExist(statErr) {
			return result, statErr
		}
		if strings.HasSuffix(artifact, ".csv") && !strings.HasSuffix(artifact, ".index.csv") {
			result.DailyFiles++
		}
	}
	verifiedRows, err := countMigratedCaptureRows(temporary, symbol)
	if err != nil {
		return result, err
	}
	if verifiedRows != result.Rows {
		return result, fmt.Errorf("daily migration verification mismatch: wrote=%d read=%d", result.Rows, verifiedRows)
	}

	moved := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		target := filepath.Join(output, filepath.Base(artifact))
		if err := os.Rename(artifact, target); err != nil {
			for index := len(moved) - 1; index >= 0; index-- {
				_ = os.Rename(filepath.Join(output, filepath.Base(moved[index])), moved[index])
			}
			return result, err
		}
		moved = append(moved, artifact)
	}
	if err := os.Remove(temporary); err != nil {
		return result, err
	}
	committed = true
	return result, nil
}

func migrateLegacyCaptureFile(filename string, writer *dailyCSV, timestampIndex, width int) (int64, error) {
	file, err := os.Open(filename)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	reader := csv.NewReader(file)
	reader.FieldsPerRecord = -1
	if _, err := reader.Read(); err != nil {
		return 0, fmt.Errorf("read legacy capture header %s: %w", filename, err)
	}
	var rows int64
	for {
		record, readErr := reader.Read()
		if readErr == io.EOF {
			return rows, nil
		}
		if readErr != nil {
			return rows, fmt.Errorf("read legacy capture %s: %w", filename, readErr)
		}
		if timestampIndex >= len(record) {
			return rows, fmt.Errorf("legacy capture %s has a short record", filename)
		}
		at, parseErr := time.Parse(time.RFC3339Nano, record[timestampIndex])
		if parseErr != nil {
			return rows, fmt.Errorf("legacy capture %s has invalid timestamp %q: %w", filename, record[timestampIndex], parseErr)
		}
		if len(record) < width {
			padded := make([]string, width)
			copy(padded, record)
			record = padded
		}
		if err := writer.write(at, record); err != nil {
			return rows, fmt.Errorf("write migrated capture row from %s: %w", filename, err)
		}
		rows++
	}
}

func isDailyCaptureFilename(filename, symbol, stream string) bool {
	base := filepath.Base(filename)
	prefix := symbol + "-" + stream + "-"
	if !strings.HasPrefix(base, prefix) || !strings.HasSuffix(base, ".csv") {
		return false
	}
	stamp := strings.TrimSuffix(strings.TrimPrefix(base, prefix), ".csv")
	if len(stamp) != len(time.DateOnly) {
		return false
	}
	_, err := time.Parse(time.DateOnly, stamp)
	return err == nil
}

func countMigratedCaptureRows(root, symbol string) (int64, error) {
	files, err := filepath.Glob(filepath.Join(root, symbol+"-*.csv"))
	if err != nil {
		return 0, err
	}
	var rows int64
	for _, filename := range files {
		if strings.HasSuffix(filename, ".index.csv") {
			continue
		}
		file, openErr := os.Open(filename)
		if openErr != nil {
			return rows, openErr
		}
		reader := csv.NewReader(file)
		reader.FieldsPerRecord = -1
		if _, readErr := reader.Read(); readErr != nil {
			_ = file.Close()
			return rows, readErr
		}
		for {
			_, readErr := reader.Read()
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				_ = file.Close()
				return rows, readErr
			}
			rows++
		}
		if closeErr := file.Close(); closeErr != nil {
			return rows, closeErr
		}
	}
	return rows, nil
}
