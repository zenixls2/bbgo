package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/c9s/bbgo/pkg/strategy/gammacapture"
)

const relativeHoldRiskReplayCheckpointVersion = 1

// relativeHoldRiskReplayCheckpoint is deliberately narrower than the live
// ModelCheckpoint: it accelerates the causal strategy-vs-Hold label model,
// not order or balance restoration. The production replay still replays the
// same-symbol market data from its normal core-model warmup boundary, while
// this checkpoint skips already-matured RelativeHoldRisk labels.
type relativeHoldRiskReplayCheckpoint struct {
	Version     int                                     `json:"version"`
	Symbol      string                                  `json:"symbol"`
	ConfigHash  string                                  `json:"configHash"`
	ReplayAfter time.Time                               `json:"replayAfter"`
	ScoreFrom   time.Time                               `json:"scoreFrom"`
	Model       gammacapture.RelativeHoldRiskCheckpoint `json:"model"`
}

func relativeHoldRiskCheckpointHash(symbol string, config gammacapture.RelativeHoldRiskConfig) string {
	payload := struct {
		Symbol string
		Config gammacapture.RelativeHoldRiskConfig
	}{symbol, config}
	data, _ := json.Marshal(payload)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func loadRelativeHoldRiskReplayCheckpoint(path, symbol string, config gammacapture.RelativeHoldRiskConfig, scoreFrom time.Time) (*relativeHoldRiskReplayCheckpoint, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var checkpoint relativeHoldRiskReplayCheckpoint
	if err := json.Unmarshal(data, &checkpoint); err != nil {
		return nil, fmt.Errorf("decode relative-hold checkpoint: %w", err)
	}
	if checkpoint.Version != relativeHoldRiskReplayCheckpointVersion {
		return nil, fmt.Errorf("relative-hold replay checkpoint version %d is unsupported", checkpoint.Version)
	}
	if checkpoint.Symbol != symbol || checkpoint.ReplayAfter.IsZero() {
		return nil, fmt.Errorf("relative-hold replay checkpoint cursor is incompatible with scoreFrom")
	}
	// A checkpoint after the requested score boundary belongs to a
	// different/future replay and cannot be used to warm this run. Treat it as
	// a cache miss so a fresh causal preload is performed and the file can be
	// atomically replaced with the new cursor.
	if checkpoint.ReplayAfter.After(scoreFrom) {
		return nil, nil
	}
	if checkpoint.ConfigHash != relativeHoldRiskCheckpointHash(symbol, config) {
		return nil, fmt.Errorf("relative-hold replay checkpoint configuration fingerprint changed")
	}
	return &checkpoint, nil
}

func saveRelativeHoldRiskReplayCheckpoint(path, symbol string, scoreFrom, replayAfter time.Time, config gammacapture.RelativeHoldRiskConfig, model *gammacapture.RelativeHoldRiskCheckpoint) error {
	if path == "" || model == nil {
		return nil
	}
	if replayAfter.IsZero() {
		return fmt.Errorf("relative-hold replay checkpoint has no replay cursor")
	}
	checkpoint := relativeHoldRiskReplayCheckpoint{
		Version: relativeHoldRiskReplayCheckpointVersion, Symbol: symbol,
		ConfigHash:  relativeHoldRiskCheckpointHash(symbol, config),
		ReplayAfter: replayAfter.UTC(), ScoreFrom: scoreFrom.UTC(), Model: *model,
	}
	data, err := json.MarshalIndent(checkpoint, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".relative-hold-checkpoint-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
