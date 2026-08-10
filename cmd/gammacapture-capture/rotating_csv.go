package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

const captureMetadataVersion = 1

// dailyCSV keeps one append-only CSV per UTC calendar day. Event callbacks
// pass their received_at timestamp to write, so rotation does not depend on a
// timer firing exactly at midnight.
type dailyCSV struct {
	mu     sync.Mutex
	dir    string
	symbol string
	stream string
	header []string

	date            string
	path            string
	file            *os.File
	writer          *csv.Writer
	indexFile       *os.File
	indexWriter     *csv.Writer
	lastIndexMinute time.Time
	rows            int64
	first           time.Time
	last            time.Time
}

type captureFileMetadata struct {
	Version int       `json:"version"`
	Symbol  string    `json:"symbol"`
	Stream  string    `json:"stream"`
	DateUTC string    `json:"dateUTC"`
	First   time.Time `json:"first"`
	Last    time.Time `json:"last"`
	Rows    int64     `json:"rows"`
	File    string    `json:"file"`
}

func newDailyCSV(dir, symbol, stream string, header []string) (*dailyCSV, error) {
	if dir == "" || symbol == "" || stream == "" || len(header) == 0 {
		return nil, fmt.Errorf("daily CSV directory, symbol, stream, and header are required")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &dailyCSV{dir: dir, symbol: symbol, stream: stream, header: append([]string(nil), header...)}, nil
}

func (f *dailyCSV) write(at time.Time, record []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if at.IsZero() {
		return fmt.Errorf("daily CSV record timestamp is required")
	}
	date := at.UTC().Format(time.DateOnly)
	if f.file == nil || f.date != date {
		if err := f.rotateLocked(date); err != nil {
			return err
		}
	}
	minute := at.UTC().Truncate(time.Minute)
	if f.lastIndexMinute.IsZero() || minute.After(f.lastIndexMinute) {
		f.writer.Flush()
		if err := f.writer.Error(); err != nil {
			return err
		}
		offset, err := f.file.Seek(0, os.SEEK_CUR)
		if err != nil {
			return err
		}
		if err := f.indexWriter.Write([]string{minute.Format(time.RFC3339), strconv.FormatInt(offset, 10)}); err != nil {
			return err
		}
		f.indexWriter.Flush()
		if err := f.indexWriter.Error(); err != nil {
			return err
		}
		f.lastIndexMinute = minute
	}
	if err := f.writer.Write(record); err != nil {
		return err
	}
	// Preserve the collector's prior durability/visibility behavior: warmup may
	// read the active file while capture is still running.
	f.writer.Flush()
	if err := f.writer.Error(); err != nil {
		return err
	}
	f.rows++
	at = at.UTC()
	if f.first.IsZero() || at.Before(f.first) {
		f.first = at
	}
	if f.last.IsZero() || at.After(f.last) {
		f.last = at
	}
	return nil
}

func (f *dailyCSV) rotateLocked(date string) error {
	if err := f.closeCurrentLocked(); err != nil {
		return err
	}
	path := filepath.Join(f.dir, fmt.Sprintf("%s-%s-%s.csv", f.symbol, f.stream, date))
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return err
	}
	writer := csv.NewWriter(file)
	if info.Size() == 0 {
		if err := writer.Write(f.header); err != nil {
			_ = file.Close()
			return err
		}
		writer.Flush()
		if err := writer.Error(); err != nil {
			_ = file.Close()
			return err
		}
	}
	if _, err := file.Seek(0, os.SEEK_END); err != nil {
		_ = file.Close()
		return err
	}
	indexPath := path + ".index.csv"
	indexFile, err := os.OpenFile(indexPath, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0o644)
	if err != nil {
		_ = file.Close()
		return err
	}
	lastIndexMinute, err := initializeCaptureIndex(indexFile)
	if err != nil {
		_ = indexFile.Close()
		_ = file.Close()
		return err
	}
	if _, err := indexFile.Seek(0, os.SEEK_END); err != nil {
		_ = indexFile.Close()
		_ = file.Close()
		return err
	}
	rows, first, last := int64(0), time.Time{}, time.Time{}
	if metadata, err := readCaptureMetadata(path + ".meta.json"); err == nil &&
		metadata.Version == captureMetadataVersion && metadata.File == filepath.Base(path) {
		rows, first, last = metadata.Rows, metadata.First, metadata.Last
	}
	f.date = date
	f.path = path
	f.file = file
	f.writer = writer
	f.indexFile = indexFile
	f.indexWriter = csv.NewWriter(indexFile)
	f.lastIndexMinute = lastIndexMinute
	f.rows = rows
	f.first = first
	f.last = last
	return nil
}

func initializeCaptureIndex(file *os.File) (time.Time, error) {
	info, err := file.Stat()
	if err != nil {
		return time.Time{}, err
	}
	if info.Size() == 0 {
		writer := csv.NewWriter(file)
		if err := writer.Write([]string{"minute_utc", "byte_offset"}); err != nil {
			return time.Time{}, err
		}
		writer.Flush()
		return time.Time{}, writer.Error()
	}
	if _, err := file.Seek(0, os.SEEK_SET); err != nil {
		return time.Time{}, err
	}
	records, err := csv.NewReader(file).ReadAll()
	if err != nil {
		return time.Time{}, err
	}
	var last time.Time
	for _, record := range records[1:] {
		if len(record) < 2 {
			continue
		}
		at, parseErr := time.Parse(time.RFC3339, record[0])
		if parseErr == nil && at.After(last) {
			last = at
		}
	}
	return last, nil
}

func readCaptureMetadata(path string) (captureFileMetadata, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return captureFileMetadata{}, err
	}
	var metadata captureFileMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return captureFileMetadata{}, err
	}
	return metadata, nil
}

func (f *dailyCSV) close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closeCurrentLocked()
}

func (f *dailyCSV) closeCurrentLocked() error {
	if f.file == nil {
		return nil
	}
	f.writer.Flush()
	writeErr := f.writer.Error()
	f.indexWriter.Flush()
	indexWriteErr := f.indexWriter.Error()
	closeErr := f.file.Close()
	indexCloseErr := f.indexFile.Close()
	metadataErr := f.writeMetadataLocked()
	f.file = nil
	f.writer = nil
	f.indexFile = nil
	f.indexWriter = nil
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	if indexWriteErr != nil {
		return indexWriteErr
	}
	if indexCloseErr != nil {
		return indexCloseErr
	}
	return metadataErr
}

func (f *dailyCSV) writeMetadataLocked() error {
	metadata := captureFileMetadata{
		Version: captureMetadataVersion,
		Symbol:  f.symbol,
		Stream:  f.stream,
		DateUTC: f.date,
		First:   f.first,
		Last:    f.last,
		Rows:    f.rows,
		File:    filepath.Base(f.path),
	}
	temporary, err := os.CreateTemp(f.dir, ".capture-meta-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(metadata); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, f.path+".meta.json")
}
