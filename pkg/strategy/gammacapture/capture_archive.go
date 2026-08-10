package gammacapture

import (
	"bytes"
	"encoding/csv"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const captureTailProbeBytes = 64 * 1024

type captureFileCheckpoint struct {
	Size            int64
	ModTimeUnixNano int64
}

func captureFileCheckpointIfComplete(filename string, replayAfter time.Time) (captureFileCheckpoint, bool) {
	if replayAfter.IsZero() {
		return captureFileCheckpoint{}, false
	}
	file, err := os.Open(filename)
	if err != nil {
		return captureFileCheckpoint{}, false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() <= 0 {
		return captureFileCheckpoint{}, false
	}
	probeSize := info.Size()
	if probeSize > captureTailProbeBytes {
		probeSize = captureTailProbeBytes
	}
	buffer := make([]byte, probeSize)
	read, err := file.ReadAt(buffer, info.Size()-probeSize)
	if err != nil && err != io.EOF {
		return captureFileCheckpoint{}, false
	}
	lines := bytes.Split(buffer[:read], []byte{'\n'})
	for index := len(lines) - 1; index >= 0; index-- {
		line := bytes.TrimSpace(lines[index])
		if len(line) == 0 {
			continue
		}
		comma := bytes.IndexByte(line, ',')
		if comma <= 0 {
			continue
		}
		last, parseErr := time.Parse(time.RFC3339Nano, string(line[:comma]))
		if parseErr != nil {
			continue
		}
		if last.After(replayAfter) {
			return captureFileCheckpoint{}, false
		}
		return captureFileCheckpoint{Size: info.Size(), ModTimeUnixNano: info.ModTime().UnixNano()}, true
	}
	return captureFileCheckpoint{}, false
}

// captureFilesChangedSinceCheckpoint omits a fully consumed file only when
// both its length and modification time still match. Any append or replacement
// falls back to replay, so this optimization cannot skip new observations.
func captureFilesChangedSinceCheckpoint(files []string, completed map[string]captureFileCheckpoint) []string {
	if len(completed) == 0 {
		return files
	}
	out := make([]string, 0, len(files))
	for _, filename := range files {
		checkpoint, ok := completed[filename]
		if ok {
			if info, err := os.Stat(filename); err == nil &&
				info.Size() == checkpoint.Size && info.ModTime().UnixNano() == checkpoint.ModTimeUnixNano {
				continue
			}
		}
		out = append(out, filename)
	}
	return out
}

// captureFilesOverlapping prunes the collector's UTC-daily files without
// excluding legacy timestamped files whose end time is unknown.
func captureFilesOverlapping(files []string, symbol, stream string, cutoff, end time.Time) []string {
	daily := make([]string, 0, len(files))
	legacy := make([]string, 0, len(files))
	for _, filename := range files {
		start, ok := dailyCaptureStart(filename, symbol, stream)
		if ok {
			fileEnd := start.Add(24 * time.Hour)
			if !fileEnd.After(cutoff) || start.After(end) {
				continue
			}
			daily = append(daily, filename)
			continue
		}
		legacy = append(legacy, filename)
	}
	// A UTC-daily archive is produced atomically from the complete legacy set
	// before the rotating collector is started. Once present, it is therefore
	// the authoritative representation of this stream. Reading both layouts
	// would scan the old monolithic files again and can duplicate observations
	// in callers that do not timestamp-deduplicate them.
	if len(daily) > 0 {
		return daily
	}
	return legacy
}

func dailyCaptureStart(filename, symbol, stream string) (time.Time, bool) {
	base := filepath.Base(filename)
	prefix := symbol + "-" + stream + "-"
	if !strings.HasPrefix(base, prefix) || !strings.HasSuffix(base, ".csv") {
		return time.Time{}, false
	}
	stamp := strings.TrimSuffix(strings.TrimPrefix(base, prefix), ".csv")
	if len(stamp) != len(time.DateOnly) {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.DateOnly, stamp)
	return parsed, err == nil
}

// newIndexedCaptureReader seeks to the first indexed minute at or before
// cutoff. Readers still apply an exact timestamp predicate, so replaying the
// partial minute is safe and deterministic.
func newIndexedCaptureReader(file *os.File, filename string, cutoff time.Time) (*csv.Reader, error) {
	offset, ok := captureIndexOffset(filename+".index.csv", cutoff)
	if ok && offset > 0 {
		if _, err := file.Seek(offset, io.SeekStart); err != nil {
			return nil, err
		}
		return csv.NewReader(file), nil
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	reader := csv.NewReader(file)
	if _, err := reader.Read(); err != nil {
		return nil, err
	}
	return reader, nil
}

func captureIndexOffset(indexPath string, cutoff time.Time) (int64, bool) {
	file, err := os.Open(indexPath)
	if err != nil {
		return 0, false
	}
	defer file.Close()
	reader := csv.NewReader(file)
	if _, err := reader.Read(); err != nil {
		return 0, false
	}
	target := cutoff.UTC().Truncate(time.Minute)
	var selected int64
	var selectedMinute time.Time
	found := false
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil || len(record) < 2 {
			continue
		}
		minute, timeErr := time.Parse(time.RFC3339, record[0])
		offset, offsetErr := strconv.ParseInt(record[1], 10, 64)
		if timeErr != nil || offsetErr != nil || offset < 0 || minute.After(target) {
			continue
		}
		if !found || minute.After(selectedMinute) || minute.Equal(selectedMinute) && offset < selected {
			selected = offset
			selectedMinute = minute
			found = true
		}
	}
	return selected, found
}
