package main

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type rangeRegimeStudyInput struct {
	DataPath, Symbol string
	From, To         time.Time
	Window, Step     time.Duration
	MaximumWindows   int
}

type rangeRegimePoint struct {
	At  time.Time
	Mid float64
}

type rangeRegimeWindow struct {
	From             time.Time `json:"from"`
	To               time.Time `json:"to"`
	Samples          int       `json:"samples"`
	OpeningMidJPY    float64   `json:"openingMidJPY"`
	NetReturnBps     float64   `json:"netReturnBps"`
	PathVariationBps float64   `json:"pathVariationBps"`
	RangeBps         float64   `json:"rangeBps"`
	EfficiencyRatio  float64   `json:"efficiencyRatio"`
	CenterCrossings  int       `json:"centerCrossings"`
	ReturnReversals  int       `json:"returnReversals"`
	OscillationScore float64   `json:"oscillationScore"`
}

type rangeRegimeReport struct {
	Symbol         string              `json:"symbol"`
	From           time.Time           `json:"from"`
	To             time.Time           `json:"to"`
	SampleInterval time.Duration       `json:"sampleInterval"`
	Window         time.Duration       `json:"window"`
	Step           time.Duration       `json:"step"`
	Selected       []rangeRegimeWindow `json:"selected"`
}

func indexedBookFiles(dataPath, symbol string) ([]string, error) {
	patterns := []string{
		filepath.Join(dataPath, symbol, symbol+"-bookticker-*.csv.index.csv"),
		filepath.Join(dataPath, symbol+"-bookticker-*.csv.index.csv"),
	}
	seen := make(map[string]struct{})
	var files []string
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return nil, err
		}
		for _, match := range matches {
			if _, ok := seen[match]; ok {
				continue
			}
			seen[match] = struct{}{}
			files = append(files, match)
		}
	}
	sort.Strings(files)
	return files, nil
}

func readIndexedRangePoints(dataPath, symbol string, from, to time.Time, sampleInterval time.Duration) ([]rangeRegimePoint, error) {
	files, err := indexedBookFiles(dataPath, symbol)
	if err != nil {
		return nil, err
	}
	var points []rangeRegimePoint
	for _, indexPath := range files {
		indexFile, err := os.Open(indexPath)
		if err != nil {
			return nil, err
		}
		dataFile, err := os.Open(strings.TrimSuffix(indexPath, ".index.csv"))
		if err != nil {
			indexFile.Close()
			return nil, err
		}
		reader := csv.NewReader(indexFile)
		_, _ = reader.Read()
		for {
			record, readErr := reader.Read()
			if readErr == io.EOF {
				break
			}
			if readErr != nil || len(record) < 2 {
				continue
			}
			minute, parseErr := time.Parse(time.RFC3339, record[0])
			if parseErr != nil || minute.Before(from) || !minute.Before(to) ||
				minute.UnixNano()%sampleInterval.Nanoseconds() != 0 {
				continue
			}
			offset, parseErr := strconv.ParseInt(record[1], 10, 64)
			if parseErr != nil {
				continue
			}
			if _, seekErr := dataFile.Seek(offset, io.SeekStart); seekErr != nil {
				continue
			}
			line, lineErr := bufio.NewReader(dataFile).ReadString('\n')
			if lineErr != nil && lineErr != io.EOF {
				continue
			}
			row, parseErr := csv.NewReader(strings.NewReader(line)).Read()
			if parseErr != nil || len(row) < 4 {
				continue
			}
			at, timeErr := time.Parse(time.RFC3339Nano, row[0])
			bid, bidErr := strconv.ParseFloat(row[1], 64)
			ask, askErr := strconv.ParseFloat(row[3], 64)
			if timeErr == nil && bidErr == nil && askErr == nil && bid > 0 && ask >= bid {
				points = append(points, rangeRegimePoint{At: at, Mid: (bid + ask) / 2})
			}
		}
		dataFile.Close()
		indexFile.Close()
	}
	sort.Slice(points, func(i, j int) bool { return points[i].At.Before(points[j].At) })
	return points, nil
}

func measureRangeRegime(points []rangeRegimePoint, from, to time.Time) (rangeRegimeWindow, bool) {
	start := sort.Search(len(points), func(i int) bool { return !points[i].At.Before(from) })
	end := sort.Search(len(points), func(i int) bool { return !points[i].At.Before(to) })
	if end-start < 3 {
		return rangeRegimeWindow{}, false
	}
	segment := points[start:end]
	logs := make([]float64, len(segment))
	meanLog, minimum, maximum := 0.0, math.Inf(1), math.Inf(-1)
	for i, point := range segment {
		logs[i] = math.Log(point.Mid)
		meanLog += logs[i]
		minimum = math.Min(minimum, logs[i])
		maximum = math.Max(maximum, logs[i])
	}
	meanLog /= float64(len(logs))
	path, reversals, previousReturnSign := 0.0, 0, 0
	for i := 1; i < len(logs); i++ {
		change := logs[i] - logs[i-1]
		path += math.Abs(change)
		sign := 0
		if math.Abs(change)*10_000 >= 3 {
			if change > 0 {
				sign = 1
			} else {
				sign = -1
			}
		}
		if sign != 0 {
			if previousReturnSign != 0 && sign != previousReturnSign {
				reversals++
			}
			previousReturnSign = sign
		}
	}
	crossings, previousCenterSign := 0, 0
	for _, value := range logs {
		deviationBps := (value - meanLog) * 10_000
		sign := 0
		if math.Abs(deviationBps) >= 2 {
			if deviationBps > 0 {
				sign = 1
			} else {
				sign = -1
			}
		}
		if sign != 0 {
			if previousCenterSign != 0 && sign != previousCenterSign {
				crossings++
			}
			previousCenterSign = sign
		}
	}
	net := logs[len(logs)-1] - logs[0]
	rangeBps := (maximum - minimum) * 10_000
	pathBps := path * 10_000
	efficiency := 1.0
	if path > 0 {
		efficiency = math.Abs(net) / path
	}
	window := rangeRegimeWindow{
		From: from, To: to, Samples: len(segment),
		OpeningMidJPY: segment[0].Mid,
		NetReturnBps:  net * 10_000, PathVariationBps: pathBps,
		RangeBps: rangeBps, EfficiencyRatio: efficiency,
		CenterCrossings: crossings, ReturnReversals: reversals,
	}
	window.OscillationScore = math.Max(0, pathBps-math.Abs(window.NetReturnBps)) *
		(1 + 0.10*float64(crossings) + 0.05*float64(reversals))
	valid := math.Abs(window.NetReturnBps) <= 0.25*rangeBps &&
		pathBps >= 1.5*rangeBps && crossings >= 2 && reversals >= 2
	return window, valid
}

func selectNonOverlappingRangeWindows(candidates []rangeRegimeWindow, maximum int) []rangeRegimeWindow {
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].OscillationScore > candidates[j].OscillationScore
	})
	selected := make([]rangeRegimeWindow, 0, maximum)
	for _, candidate := range candidates {
		overlaps := false
		for _, existing := range selected {
			if candidate.From.Before(existing.To) && existing.From.Before(candidate.To) {
				overlaps = true
				break
			}
		}
		if !overlaps {
			selected = append(selected, candidate)
			if len(selected) >= maximum {
				break
			}
		}
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].From.Before(selected[j].From) })
	return selected
}

func runRangeRegimeStudy(in rangeRegimeStudyInput) {
	const sampleInterval = 15 * time.Minute
	points, err := readIndexedRangePoints(in.DataPath, in.Symbol, in.From, in.To, sampleInterval)
	if err != nil {
		fatalf("read indexed range points: %v", err)
	}
	var candidates []rangeRegimeWindow
	for start := in.From.Truncate(in.Step); !start.Add(in.Window).After(in.To); start = start.Add(in.Step) {
		window, ok := measureRangeRegime(points, start, start.Add(in.Window))
		if ok {
			candidates = append(candidates, window)
		}
	}
	report := rangeRegimeReport{
		Symbol: in.Symbol, From: in.From, To: in.To,
		SampleInterval: sampleInterval, Window: in.Window, Step: in.Step,
		Selected: selectNonOverlappingRangeWindows(candidates, in.MaximumWindows),
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fatalf("encode range-regime report: %v", err)
	}
}
