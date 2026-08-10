package main

import (
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
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

// loadExactReplayDataset is used by the ordinary event replay and preserves
// every captured BBO/trade row in the interval. It shares the same checkpoint
// keying and invalidation rules as the Macro warm-up cache.
func loadExactReplayDataset(path, symbol string, from, to time.Time, configFingerprint, cacheDir string) ([]bboSnapshot, []tick, bool) {
	return loadReplayDataset(path, symbol, from, to, time.Time{}, configFingerprint, cacheDir, "exact")
}

func loadReplayDataset(path, symbol string, from, to, exactFrom time.Time, configFingerprint, cacheDir, mode string) ([]bboSnapshot, []tick, bool) {
	bboFiles := replayCaptureFilesOverlapping(replayCaptureFiles(path, symbol, "bookticker"), symbol, "bookticker", from, to)
	tradeFiles := replayCaptureFilesOverlapping(replayCaptureFiles(path, symbol, "trades"), symbol, "trades", from, to)
	files := append(append([]string(nil), bboFiles...), tradeFiles...)
	header := replayDatasetCacheHeader{
		Version: replayDatasetCacheVersion, Mode: mode, Symbol: symbol,
		From: from, To: to, ExactFrom: exactFrom, ConfigFingerprint: configFingerprint,
		Files: replayFileFingerprints(files),
	}
	cachePath := replayDatasetCachePath(cacheDir, symbol, header.Mode, header)
	if cachePath != "" {
		if cached, ok := readReplayDatasetCache(cachePath, header); ok {
			return cachedBooks(cached.Books), cachedTrades(cached.Trades), true
		}
	}
	books := readBBOFiles(bboFiles, from, to)
	if mode == "macro-1s" {
		books = compactMacroReplayBBO(books, exactFrom)
	}
	trades := readLiveTradesFiles(tradeFiles, from, to)
	if cachePath != "" {
		_ = writeReplayDatasetCache(cachePath, replayDatasetCache{
			Header: header, Books: encodeCachedBBO(books), Trades: encodeCachedTrades(trades),
		})
	}
	return books, trades, false
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
