package main

import (
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

const replayDatasetCacheVersion = 1

// A replay cache is deliberately keyed by the complete input description.
// It is not the live model checkpoint: it contains only parsed historical
// events and therefore cannot leak future model state into a deterministic
// backtest.
type replayDatasetCacheHeader struct {
	Version           int                     `json:"version"`
	Mode              string                  `json:"mode"`
	Symbol            string                  `json:"symbol"`
	From              time.Time               `json:"from"`
	To                time.Time               `json:"to"`
	ExactFrom         time.Time               `json:"exactFrom"`
	BBOInterval       time.Duration           `json:"bboInterval"`
	ConfigFingerprint string                  `json:"configFingerprint"`
	Files             []replayFileFingerprint `json:"files"`
}

type replayFileFingerprint struct {
	Path            string `json:"path"`
	Size            int64  `json:"size"`
	ModTimeUnixNano int64  `json:"modTimeUnixNano"`
}

type replayDatasetCache struct {
	Header replayDatasetCacheHeader
	Books  []replayCachedBBO
	Trades []replayCachedTrade
}

type replayCachedBBO struct {
	Time         time.Time
	Bid, BidSize float64
	Ask, AskSize float64
}

type replayCachedTrade struct {
	ID          uint64
	Time        time.Time
	Price, Size float64
	Side        string
}

// replayConfigFingerprint is retained for report metadata and callers, but
// parsed market data is independent of strategy parameters. The cache key
// deliberately ignores this fingerprint so changing one model setting does
// not force a multi-gigabyte BBO/trade reparse.
func replayConfigFingerprint(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		sum := sha256.Sum256([]byte("unreadable:" + path))
		return hex.EncodeToString(sum[:])
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func replayFileFingerprints(files []string) []replayFileFingerprint {
	seen := make(map[string]struct{}, len(files))
	out := make([]replayFileFingerprint, 0, len(files))
	for _, filename := range files {
		absolute, err := filepath.Abs(filename)
		if err != nil {
			absolute = filename
		}
		if _, ok := seen[absolute]; ok {
			continue
		}
		seen[absolute] = struct{}{}
		stat, err := os.Stat(filename)
		if err != nil {
			continue
		}
		out = append(out, replayFileFingerprint{
			Path: absolute, Size: stat.Size(), ModTimeUnixNano: stat.ModTime().UnixNano(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func replayDatasetCacheKey(header replayDatasetCacheHeader) string {
	// Parsed events do not depend on strategy configuration. Keep the field in
	// the header for diagnostics/backward-compatible decoding, but canonicalize
	// it out of the key so a YAML-only policy change reuses the same dataset.
	header.ConfigFingerprint = ""
	data, _ := json.Marshal(header)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func replayDatasetCachePath(cacheDir, symbol, mode string, header replayDatasetCacheHeader) string {
	if cacheDir == "" {
		return ""
	}
	return filepath.Join(cacheDir, symbol+"-"+mode+"-"+replayDatasetCacheKey(header)+".gob")
}

// loadMacroReplayDataset parses (or reuses) the exact requested interval. The
// warm-up is compacted to one BBO per second while the evaluation interval is
// retained tick-exactly, matching readMacroReplayBBO's semantics.
func loadMacroReplayDataset(path, symbol string, from, to, exactFrom time.Time, configFingerprint, cacheDir string) ([]bboSnapshot, []tick, bool) {
	return loadReplayDataset(path, symbol, from, to, exactFrom, configFingerprint, cacheDir, "macro-1s")
}

// loadWarmReplayDataset keeps the evaluation/calibration interval tick-exact
// while retaining one BBO close per second before exactFrom. Public trades are
// never compacted. This is sufficient to seed the production Fast path models
// without making a multi-hour warm-up dominate replay memory and CPU.
func loadWarmReplayDataset(path, symbol string, from, to, exactFrom time.Time, configFingerprint, cacheDir string) ([]bboSnapshot, []tick, bool) {
	return loadWarmReplayDatasetAtInterval(path, symbol, from, to, exactFrom, configFingerprint, cacheDir, 0)
}

// loadWarmReplayDatasetAtInterval applies the requested replay BBO interval
// while parsing the archive.  Keeping the compaction before model replay is
// materially cheaper than loading every raw row and compacting the resulting
// slice afterward, especially for multi-day production comparisons.
func loadWarmReplayDatasetAtInterval(path, symbol string, from, to, exactFrom time.Time, configFingerprint, cacheDir string, bboInterval time.Duration) ([]bboSnapshot, []tick, bool) {
	return loadReplayDatasetWithInterval(path, symbol, from, to, exactFrom, configFingerprint, cacheDir, "warm-1s", bboInterval)
}

// loadWarmReplayDatasetAtIntervals is an explicitly approximate research
// variant. The ordinary loader retains one warmup BBO per second; this helper
// also coarsens warmup only when the caller supplies a positive warmInterval.
func loadWarmReplayDatasetAtIntervals(path, symbol string, from, to, exactFrom time.Time, configFingerprint, cacheDir string, bboInterval, warmInterval time.Duration) ([]bboSnapshot, []tick, bool) {
	return loadReplayDatasetWithIntervalAndWarmInterval(path, symbol, from, to, exactFrom, configFingerprint, cacheDir, "warm-interval", bboInterval, warmInterval)
}

// loadExactReplayDataset is used by the ordinary event replay and preserves
// every captured BBO/trade row in the interval. It shares the same checkpoint
// keying and invalidation rules as the Macro warm-up cache.
func loadExactReplayDataset(path, symbol string, from, to time.Time, configFingerprint, cacheDir string) ([]bboSnapshot, []tick, bool) {
	return loadReplayDataset(path, symbol, from, to, time.Time{}, configFingerprint, cacheDir, "exact")
}

func loadReplayDataset(path, symbol string, from, to, exactFrom time.Time, configFingerprint, cacheDir, mode string) ([]bboSnapshot, []tick, bool) {
	return loadReplayDatasetWithInterval(path, symbol, from, to, exactFrom, configFingerprint, cacheDir, mode, 0)
}

func loadReplayDatasetWithInterval(path, symbol string, from, to, exactFrom time.Time, configFingerprint, cacheDir, mode string, bboInterval time.Duration) ([]bboSnapshot, []tick, bool) {
	return loadReplayDatasetWithIntervalAndWarmInterval(path, symbol, from, to, exactFrom, configFingerprint, cacheDir, mode, bboInterval, 0)
}

func loadReplayDatasetWithIntervalAndWarmInterval(path, symbol string, from, to, exactFrom time.Time, configFingerprint, cacheDir, mode string, bboInterval, warmInterval time.Duration) ([]bboSnapshot, []tick, bool) {
	bboFiles := replayCaptureFilesOverlapping(replayCaptureFiles(path, symbol, "bookticker"), symbol, "bookticker", from, to)
	tradeFiles := replayCaptureFilesOverlapping(replayCaptureFiles(path, symbol, "trades"), symbol, "trades", from, to)
	files := append(append([]string(nil), bboFiles...), tradeFiles...)
	header := replayDatasetCacheHeader{
		Version: replayDatasetCacheVersion, Mode: mode, Symbol: symbol,
		From: from, To: to, ExactFrom: exactFrom, BBOInterval: bboInterval, ConfigFingerprint: configFingerprint,
		Files: replayFileFingerprints(files),
	}
	cachePath := replayDatasetCachePath(cacheDir, symbol, header.Mode, header)
	if cachePath != "" {
		if cached, ok := readReplayDatasetCache(cachePath, header); ok {
			return cachedBooks(cached.Books), cachedTrades(cached.Trades), true
		}
	}
	var books []bboSnapshot
	if mode == "macro-1s" || mode == "warm-1s" || bboInterval > 0 {
		books = readBBOFilesCompactedWithWarmInterval(bboFiles, from, to, exactFrom, bboInterval, warmInterval)
	} else {
		books = readBBOFiles(bboFiles, from, to)
	}
	trades := readLiveTradesFiles(tradeFiles, from, to)
	if cachePath != "" {
		_ = writeReplayDatasetCache(cachePath, replayDatasetCache{
			Header: header, Books: encodeCachedBBO(books), Trades: encodeCachedTrades(trades),
		})
	}
	return books, trades, false
}

// readBBOFilesCompacted performs the warm-up and optional fixed-interval
// reduction in one pass.  Before exactFrom, warm-1s semantics retain the last
// BBO in each second.  At and after exactFrom, a positive interval retains the
// last BBO in each requested bucket; zero keeps the exact event stream.  This
// is equivalent to the previous post-load compaction but avoids a raw-event
// allocation and sort for the discarded rows.
func readBBOFilesCompacted(files []string, from, to, exactFrom time.Time, interval time.Duration) []bboSnapshot {
	return readBBOFilesCompactedWithWarmInterval(files, from, to, exactFrom, interval, 0)
}

func readBBOFilesCompactedWithWarmInterval(files []string, from, to, exactFrom time.Time, interval, warmInterval time.Duration) []bboSnapshot {
	out := make([]bboSnapshot, 0, 4096)
	var pending bboSnapshot
	var pendingBucket time.Time
	var pendingWarm bool
	pendingValid := false
	flush := func() {
		if pendingValid {
			out = append(out, pending)
			pendingValid = false
		}
	}
	for _, filename := range files {
		file, err := os.Open(filename)
		if err != nil {
			continue
		}
		reader := newReplayIndexedCaptureReader(file, filename, from)
		for {
			row, readErr := reader.Read()
			if readErr == io.EOF {
				break
			}
			if readErr != nil || len(row) < 5 {
				continue
			}
			when, parseErr := time.Parse(time.RFC3339Nano, row[0])
			if parseErr != nil || when.Before(from) || !when.Before(to) {
				continue
			}
			bid, e1 := strconv.ParseFloat(row[1], 64)
			bidSize, e2 := strconv.ParseFloat(row[2], 64)
			ask, e3 := strconv.ParseFloat(row[3], 64)
			askSize, e4 := strconv.ParseFloat(row[4], 64)
			if e1 != nil || e2 != nil || e3 != nil || e4 != nil || bid <= 0 || ask <= bid {
				continue
			}
			value := bboSnapshot{time: when, bid: bid, bidSize: bidSize, ask: ask, askSize: askSize}
			warm := !exactFrom.IsZero() && when.Before(exactFrom)
			bucketWidth := interval
			if warm {
				// The default production-compatible preload keeps one BBO per
				// second. A focused research replay may explicitly coarsen warmup;
				// never infer that approximation from the score interval.
				bucketWidth = time.Second
				if warmInterval > 0 {
					bucketWidth = warmInterval
				}
			}
			if bucketWidth <= 0 {
				flush()
				out = append(out, value)
				continue
			}
			bucket := when.Truncate(bucketWidth)
			if !pendingValid || pendingWarm != warm || !pendingBucket.Equal(bucket) {
				flush()
				pending = value
				pendingBucket = bucket
				pendingWarm = warm
				pendingValid = true
				continue
			}
			pending = value
		}
		_ = file.Close()
	}
	flush()
	sort.SliceStable(out, func(i, j int) bool { return out[i].time.Before(out[j].time) })
	return out
}

func readReplayDatasetCache(path string, expected replayDatasetCacheHeader) (replayDatasetCache, bool) {
	file, err := os.Open(path)
	if err != nil {
		return replayDatasetCache{}, false
	}
	defer file.Close()
	var cached replayDatasetCache
	if err := gob.NewDecoder(file).Decode(&cached); err != nil || replayDatasetCacheKey(cached.Header) != replayDatasetCacheKey(expected) {
		return replayDatasetCache{}, false
	}
	// A corrupt local cache must not be able to cause an unbounded allocation
	// during a research command.
	if len(cached.Books) > 20_000_000 || len(cached.Trades) > 20_000_000 {
		return replayDatasetCache{}, false
	}
	return cached, true
}

func writeReplayDatasetCache(path string, cached replayDatasetCache) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".replay-cache-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := gob.NewEncoder(tmp).Encode(cached); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func encodeCachedBBO(values []bboSnapshot) []replayCachedBBO {
	out := make([]replayCachedBBO, len(values))
	for i, value := range values {
		out[i] = replayCachedBBO{Time: value.time, Bid: value.bid, BidSize: value.bidSize, Ask: value.ask, AskSize: value.askSize}
	}
	return out
}

func cachedBooks(values []replayCachedBBO) []bboSnapshot {
	out := make([]bboSnapshot, len(values))
	for i, value := range values {
		out[i] = bboSnapshot{time: value.Time, bid: value.Bid, bidSize: value.BidSize, ask: value.Ask, askSize: value.AskSize}
	}
	return out
}

func encodeCachedTrades(values []tick) []replayCachedTrade {
	out := make([]replayCachedTrade, len(values))
	for i, value := range values {
		out[i] = replayCachedTrade{ID: value.id, Time: value.time, Price: value.price, Size: value.size, Side: string(value.side)}
	}
	return out
}

func cachedTrades(values []replayCachedTrade) []tick {
	out := make([]tick, 0, len(values))
	for _, value := range values {
		side, err := types.StrToSideType(value.Side)
		if err != nil {
			continue
		}
		out = append(out, tick{id: value.ID, time: value.Time, price: value.Price, size: value.Size, side: side})
	}
	return out
}
