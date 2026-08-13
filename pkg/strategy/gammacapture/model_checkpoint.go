package gammacapture

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"time"
)

const modelCheckpointVersion = 3

// ModelCheckpoint is the bounded, causal state required to continue live
// learning from a capture delta. It contains no orders, balances, or fills.
// Deterministic backtest/replay environments deliberately never restore it.
type ModelCheckpoint struct {
	Version      int            `json:"version"`
	Symbol       string         `json:"symbol"`
	ModelHash    string         `json:"modelHash"`
	SavedAt      time.Time      `json:"savedAt"`
	ReplayAfter  time.Time      `json:"replayAfter"`
	Engine       CrossingEngine `json:"engine"`
	CaptureFiles map[string]captureFileCheckpoint
	Slow         intensityCheckpoint               `json:"slow"`
	Fast         map[string]intensityCheckpoint    `json:"fast,omitempty"`
	BOCPD45      *bocpd45Checkpoint                `json:"bocpd45,omitempty"`
	Direction    map[string]directionCheckpoint    `json:"direction,omitempty"`
	Evidence     map[string]fastEvidenceCheckpoint `json:"evidence,omitempty"`
	FastDrift    map[string]fastDriftCheckpoint    `json:"fastDrift,omitempty"`
	Horizon      horizonCheckpoint                 `json:"horizon"`
	Macro        macroInventoryCheckpoint          `json:"macro"`
}

type intensityCheckpoint struct {
	Events          []CrossingEvent `json:"events,omitempty"`
	Last            time.Time       `json:"last,omitempty"`
	Observed        time.Duration   `json:"observed"`
	LastObservation time.Time       `json:"lastObservation,omitempty"`
}

type directionCheckpointEvent struct {
	At        time.Time `json:"at"`
	Direction Direction `json:"direction"`
}

type directionCheckpoint struct {
	Events []directionCheckpointEvent `json:"events,omitempty"`
	Last   time.Time                  `json:"last,omitempty"`
}

type fastEvidenceCheckpointTrade struct {
	At       time.Time `json:"at"`
	Notional float64   `json:"notional"`
	Signed   float64   `json:"signed"`
	ID       uint64    `json:"id,omitempty"`
}

type fastEvidenceCheckpointBBO struct {
	At        time.Time `json:"at"`
	Bid       float64   `json:"bid"`
	Ask       float64   `json:"ask"`
	BidSize   float64   `json:"bidSize"`
	AskSize   float64   `json:"askSize"`
	Mid       float64   `json:"mid"`
	Imbalance float64   `json:"imbalance"`
}

type fastEvidenceCheckpoint struct {
	Trades      []fastEvidenceCheckpointTrade `json:"trades,omitempty"`
	BBO         []fastEvidenceCheckpointBBO   `json:"bbo,omitempty"`
	LastTradeID uint64                        `json:"lastTradeID,omitempty"`
}

type fastDriftSampleCheckpoint struct {
	At              time.Time                      `json:"at"`
	Features        [fastDriftFeatureCount]float64 `json:"features"`
	AskReturnBps    float64                        `json:"askReturnBps"`
	BidReturnBps    float64                        `json:"bidReturnBps"`
	CenterReturnBps float64                        `json:"centerReturnBps"`
	PredictedCenter float64                        `json:"predictedCenter"`
	PredictionReady bool                           `json:"predictionReady"`
}

type fastDriftAnchorCheckpoint struct {
	At              time.Time                      `json:"at"`
	MaturesAt       time.Time                      `json:"maturesAt"`
	StartBid        float64                        `json:"startBid"`
	StartAsk        float64                        `json:"startAsk"`
	Features        [fastDriftFeatureCount]float64 `json:"features"`
	PredictedCenter float64                        `json:"predictedCenter"`
	PredictionReady bool                           `json:"predictionReady"`
}

type fastDriftCheckpoint struct {
	Samples []fastDriftSampleCheckpoint `json:"samples,omitempty"`
	Anchor  *fastDriftAnchorCheckpoint  `json:"anchor,omitempty"`
}

type horizonCheckpoint struct {
	Points     []MarketMakerHorizonPoint  `json:"points,omitempty"`
	LastSecond time.Time                  `json:"lastSecond,omitempty"`
	LastUpdate time.Time                  `json:"lastUpdate,omitempty"`
	Decision   MarketMakerHorizonDecision `json:"decision"`
}

type macroCheckpointBar struct {
	At      time.Time `json:"at"`
	Mid     float64   `json:"mid"`
	Bid     float64   `json:"bid"`
	Ask     float64   `json:"ask"`
	Segment uint64    `json:"segment"`
}

type macroInventoryCheckpoint struct {
	Bars            []macroCheckpointBar `json:"bars,omitempty"`
	CurrentStart    time.Time            `json:"currentStart,omitempty"`
	CurrentMid      float64              `json:"currentMid"`
	CurrentBid      float64              `json:"currentBid"`
	CurrentAsk      float64              `json:"currentAsk"`
	CurrentSegment  uint64               `json:"currentSegment"`
	LastObservation time.Time            `json:"lastObservation,omitempty"`
}

func checkpointWindowKey(window time.Duration) string {
	return strconv.FormatInt(int64(window/time.Second), 10)
}

func (s *Strategy) modelCheckpointHash() (string, error) {
	marketMaker := s.MarketMaker
	marketMaker.setDefaults()
	macro := marketMaker.MacroInventory
	macro.setDefaults()
	payload := struct {
		Symbol                    string
		Barrier                   BarrierConfig
		Intensity                 IntensityConfig
		MinimumHalfSpreadBps      float64
		MaximumHalfSpreadBps      float64
		MinTradingWindow          time.Duration
		MaxTradingWindow          time.Duration
		HorizonLookback           time.Duration
		FastWindows               []time.Duration
		FastEvidenceWindow        time.Duration
		FastEvidenceMinTrades     int
		FastEvidenceMinBBOUpdates int
		FastDriftEnabled          bool
		BOCPD45                   BOCPD45Config
		MacroBarInterval          time.Duration
		MacroLookback             time.Duration
		MacroRiskHorizons         []time.Duration
	}{
		Symbol: s.Symbol, Barrier: s.Barrier, Intensity: s.Intensity,
		MinimumHalfSpreadBps:      marketMaker.MinimumHalfSpreadBps,
		MaximumHalfSpreadBps:      marketMaker.MaximumHalfSpreadBps,
		MinTradingWindow:          time.Duration(marketMaker.MinTradingWindow),
		MaxTradingWindow:          time.Duration(marketMaker.MaxTradingWindow),
		HorizonLookback:           time.Duration(marketMaker.HorizonLookback),
		FastWindows:               marketMaker.FastModelWindows(),
		FastEvidenceWindow:        time.Duration(marketMaker.FastEvidenceWindow),
		FastEvidenceMinTrades:     marketMaker.FastEvidenceMinTrades,
		FastEvidenceMinBBOUpdates: marketMaker.FastEvidenceMinBBOUpdates,
		FastDriftEnabled:          marketMaker.FastDrift.Enabled,
		BOCPD45:                   marketMaker.BOCPD45,
		MacroBarInterval:          time.Duration(macro.BarInterval),
		MacroLookback:             time.Duration(macro.Lookback),
		MacroRiskHorizons:         macro.horizons(),
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func checkpointIntensity(model *IntensityModel) intensityCheckpoint {
	if model == nil {
		return intensityCheckpoint{}
	}
	return intensityCheckpoint{
		Events: append([]CrossingEvent(nil), model.events...),
		Last:   model.last, Observed: model.observed, LastObservation: model.lastObservation,
	}
}

func restoreIntensity(model *IntensityModel, checkpoint intensityCheckpoint) {
	model.events = append([]CrossingEvent(nil), checkpoint.Events...)
	model.last = checkpoint.Last
	model.observed = checkpoint.Observed
	model.lastObservation = checkpoint.LastObservation
}

func (s *Strategy) completedCaptureFileCheckpoints(replayAfter time.Time) (map[string]captureFileCheckpoint, error) {
	files, err := s.binanceBBOCaptureFiles()
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(files))
	for _, filename := range files {
		seen[filename] = struct{}{}
	}
	for _, root := range []string{s.AggTradeWarmup.LivePath, filepath.Join(s.AggTradeWarmup.Path, s.Symbol)} {
		matches, globErr := filepath.Glob(filepath.Join(root, s.Symbol+"-trades-*.csv"))
		if globErr != nil {
			return nil, globErr
		}
		for _, filename := range matches {
			seen[filename] = struct{}{}
		}
	}
	completed := make(map[string]captureFileCheckpoint)
	for filename := range seen {
		if checkpoint, ok := captureFileCheckpointIfComplete(filename, replayAfter); ok {
			completed[filename] = checkpoint
		}
	}
	return completed, nil
}

func (s *Strategy) prepareModelCheckpoint(now time.Time) error {
	if s.Environment == "backtest" || s.Environment == "replay" || s.State == nil || s.State.Engine == nil ||
		s.model == nil || s.State.LastReferenceTime.IsZero() {
		return nil
	}
	hash, err := s.modelCheckpointHash()
	if err != nil {
		return err
	}
	captureFiles, err := s.completedCaptureFileCheckpoints(s.State.LastReferenceTime)
	if err != nil {
		return err
	}
	checkpoint := &ModelCheckpoint{
		Version: modelCheckpointVersion, Symbol: s.Symbol, ModelHash: hash,
		SavedAt: now.UTC(), ReplayAfter: s.State.LastReferenceTime.UTC(),
		Engine:       *s.State.Engine,
		CaptureFiles: captureFiles,
		Slow:         checkpointIntensity(s.model),
		BOCPD45:      s.makerBOCPD45.checkpoint(),
		Fast:         make(map[string]intensityCheckpoint, len(s.fastModels)),
		Direction:    make(map[string]directionCheckpoint, len(s.makerDirectionModels)),
		Evidence:     make(map[string]fastEvidenceCheckpoint, len(s.fastEvidenceModels)),
		FastDrift:    make(map[string]fastDriftCheckpoint, len(s.makerHorizonModel.fastDrift)),
		Horizon: horizonCheckpoint{
			Points:     append([]MarketMakerHorizonPoint(nil), s.makerHorizonModel.points...),
			LastSecond: s.makerHorizonModel.lastSecond,
			LastUpdate: s.makerHorizonModel.lastUpdate,
			Decision:   s.makerHorizonModel.decision,
		},
		Macro: macroInventoryCheckpoint{
			CurrentStart:    s.makerMacroInventoryModel.currentStart,
			CurrentMid:      s.makerMacroInventoryModel.currentMid,
			CurrentBid:      s.makerMacroInventoryModel.currentBid,
			CurrentAsk:      s.makerMacroInventoryModel.currentAsk,
			CurrentSegment:  s.makerMacroInventoryModel.currentSegment,
			LastObservation: s.makerMacroInventoryModel.lastObservation,
		},
	}
	for window, model := range s.fastModels {
		checkpoint.Fast[checkpointWindowKey(window)] = checkpointIntensity(model)
	}
	for window, model := range s.makerDirectionModels {
		state := directionCheckpoint{Last: model.last, Events: make([]directionCheckpointEvent, 0, len(model.events))}
		for _, event := range model.events {
			state.Events = append(state.Events, directionCheckpointEvent{At: event.at, Direction: event.direction})
		}
		checkpoint.Direction[checkpointWindowKey(window)] = state
	}
	for window, model := range s.fastEvidenceModels {
		model.mu.Lock()
		state := fastEvidenceCheckpoint{LastTradeID: model.lastTradeID}
		for _, trade := range model.trades {
			state.Trades = append(state.Trades, fastEvidenceCheckpointTrade{At: trade.at, Notional: trade.notional, Signed: trade.signed, ID: trade.id})
		}
		for _, point := range model.bbo {
			state.BBO = append(state.BBO, fastEvidenceCheckpointBBO{
				At: point.at, Bid: point.bid, Ask: point.ask, BidSize: point.bidSize,
				AskSize: point.askSize, Mid: point.mid, Imbalance: point.imbalance,
			})
		}
		model.mu.Unlock()
		checkpoint.Evidence[checkpointWindowKey(window)] = state
	}
	for window, model := range s.makerHorizonModel.fastDrift {
		state := fastDriftCheckpoint{Samples: make([]fastDriftSampleCheckpoint, 0, len(model.Samples))}
		for _, sample := range model.Samples {
			state.Samples = append(state.Samples, fastDriftSampleCheckpoint{
				At: sample.At, Features: sample.Features,
				AskReturnBps: sample.AskReturnBps, BidReturnBps: sample.BidReturnBps,
				CenterReturnBps: sample.CenterReturnBps,
				PredictedCenter: sample.PredictedCenter, PredictionReady: sample.PredictionReady,
			})
		}
		if model.Anchor != nil {
			state.Anchor = &fastDriftAnchorCheckpoint{
				At: model.Anchor.At, MaturesAt: model.Anchor.MaturesAt,
				StartBid: model.Anchor.StartBid, StartAsk: model.Anchor.StartAsk,
				Features: model.Anchor.Features, PredictedCenter: model.Anchor.PredictedCenter,
				PredictionReady: model.Anchor.PredictionReady,
			}
		}
		checkpoint.FastDrift[checkpointWindowKey(window)] = state
	}
	for _, bar := range s.makerMacroInventoryModel.bars {
		checkpoint.Macro.Bars = append(checkpoint.Macro.Bars, macroCheckpointBar{
			At: bar.At, Mid: bar.Mid, Bid: bar.Bid, Ask: bar.Ask, Segment: bar.Segment,
		})
	}
	s.State.ModelCheckpoint = checkpoint
	return nil
}

func (s *Strategy) restoreModelCheckpoint(now time.Time) (time.Time, bool, error) {
	if s.Environment == "backtest" || s.Environment == "replay" || s.State == nil || s.State.ModelCheckpoint == nil {
		return time.Time{}, false, nil
	}
	checkpoint := s.State.ModelCheckpoint
	if checkpoint.Version != modelCheckpointVersion {
		return time.Time{}, false, fmt.Errorf("model checkpoint version %d is not supported", checkpoint.Version)
	}
	if checkpoint.Symbol != s.Symbol || checkpoint.ReplayAfter.IsZero() || checkpoint.ReplayAfter.After(now) {
		return time.Time{}, false, fmt.Errorf("model checkpoint identity or replay cursor is invalid")
	}
	hash, err := s.modelCheckpointHash()
	if err != nil {
		return time.Time{}, false, err
	}
	if checkpoint.ModelHash != hash {
		return time.Time{}, false, fmt.Errorf("model checkpoint configuration fingerprint changed")
	}
	if checkpoint.Engine.Width <= 0 {
		return time.Time{}, false, fmt.Errorf("model checkpoint crossing engine is invalid")
	}
	if s.MarketMaker.BOCPD45.Enabled && checkpoint.BOCPD45 == nil {
		return time.Time{}, false, fmt.Errorf("model checkpoint is missing BOCPD45 calibration state")
	}
	for window := range s.fastModels {
		key := checkpointWindowKey(window)
		if _, ok := checkpoint.Fast[key]; !ok {
			return time.Time{}, false, fmt.Errorf("model checkpoint is missing fast window %s", window)
		}
		if _, ok := checkpoint.Direction[key]; !ok {
			return time.Time{}, false, fmt.Errorf("model checkpoint is missing direction window %s", window)
		}
		if _, ok := checkpoint.Evidence[key]; !ok {
			return time.Time{}, false, fmt.Errorf("model checkpoint is missing evidence window %s", window)
		}
		if s.MarketMaker.FastDrift.Enabled {
			if _, ok := checkpoint.FastDrift[key]; !ok {
				return time.Time{}, false, fmt.Errorf("model checkpoint is missing Fast drift window %s", window)
			}
		}
	}
	// The top-level state can be persisted after the most recent bounded model
	// checkpoint. Model state is only complete through ReplayAfter, so delta
	// replay must start from that cursor rather than the newer top-level value.
	s.State.LastReferenceTime = checkpoint.ReplayAfter
	s.State.Engine = &checkpoint.Engine
	s.makerCheckpointCaptureFiles = checkpoint.CaptureFiles
	restoreIntensity(s.model, checkpoint.Slow)
	for window, model := range s.fastModels {
		state, ok := checkpoint.Fast[checkpointWindowKey(window)]
		if !ok {
			return time.Time{}, false, fmt.Errorf("model checkpoint is missing fast window %s", window)
		}
		restoreIntensity(model, state)
	}
	for window, model := range s.makerDirectionModels {
		state, ok := checkpoint.Direction[checkpointWindowKey(window)]
		if !ok {
			return time.Time{}, false, fmt.Errorf("model checkpoint is missing direction window %s", window)
		}
		model.events = make([]decayedDirectionEvent, 0, len(state.Events))
		for _, event := range state.Events {
			model.events = append(model.events, decayedDirectionEvent{at: event.At, direction: event.Direction})
		}
		model.last = state.Last
	}
	if s.MarketMaker.BOCPD45.Enabled {
		if err := s.makerBOCPD45.restore(checkpoint.BOCPD45); err != nil {
			return time.Time{}, false, err
		}
	}
	for window, model := range s.fastEvidenceModels {
		state, ok := checkpoint.Evidence[checkpointWindowKey(window)]
		if !ok {
			return time.Time{}, false, fmt.Errorf("model checkpoint is missing evidence window %s", window)
		}
		model.mu.Lock()
		model.trades = make([]fastEvidenceTrade, 0, len(state.Trades))
		for _, trade := range state.Trades {
			model.trades = append(model.trades, fastEvidenceTrade{at: trade.At, notional: trade.Notional, signed: trade.Signed, id: trade.ID})
		}
		model.bbo = make([]fastEvidenceBBO, 0, len(state.BBO))
		for _, point := range state.BBO {
			model.bbo = append(model.bbo, fastEvidenceBBO{
				at: point.At, bid: point.Bid, ask: point.Ask, bidSize: point.BidSize,
				askSize: point.AskSize, mid: point.Mid, imbalance: point.Imbalance,
			})
		}
		model.lastTradeID = state.LastTradeID
		model.trimLocked(now)
		model.mu.Unlock()
	}
	s.makerHorizonModel.points = append([]MarketMakerHorizonPoint(nil), checkpoint.Horizon.Points...)
	s.makerHorizonModel.lastSecond = checkpoint.Horizon.LastSecond
	// Preserve learned points and the last decision for diagnostics, but force
	// the first live BBO to recompute distance under the current quote algorithm.
	s.makerHorizonModel.lastUpdate = time.Time{}
	s.makerHorizonModel.decision = checkpoint.Horizon.Decision
	if s.MarketMaker.FastDrift.Enabled {
		s.makerHorizonModel.fastDrift = make(map[time.Duration]*fastDriftRegression, len(checkpoint.FastDrift))
		for _, window := range s.MarketMaker.FastModelWindows() {
			state := checkpoint.FastDrift[checkpointWindowKey(window)]
			model := &fastDriftRegression{Samples: make([]fastDriftSample, 0, len(state.Samples))}
			for _, sample := range state.Samples {
				model.Samples = append(model.Samples, fastDriftSample{
					At: sample.At, Features: sample.Features,
					AskReturnBps: sample.AskReturnBps, BidReturnBps: sample.BidReturnBps,
					CenterReturnBps: sample.CenterReturnBps,
					PredictedCenter: sample.PredictedCenter, PredictionReady: sample.PredictionReady,
				})
			}
			if state.Anchor != nil {
				model.Anchor = &fastDriftAnchor{
					At: state.Anchor.At, MaturesAt: state.Anchor.MaturesAt,
					StartBid: state.Anchor.StartBid, StartAsk: state.Anchor.StartAsk,
					Features: state.Anchor.Features, PredictedCenter: state.Anchor.PredictedCenter,
					PredictionReady: state.Anchor.PredictionReady,
				}
			}
			model.trim(now, time.Duration(s.MarketMaker.HorizonLookback))
			s.makerHorizonModel.fastDrift[window] = model
		}
	}
	s.makerHorizonModel.rebuildSideHARVarianceRisk(s.MarketMaker)
	if s.makerExecutableCrossingModel != nil {
		s.makerExecutableCrossingModel.Rebuild(s.makerHorizonModel.points)
	}
	s.makerMacroInventoryModel = MacroInventoryModel{
		currentStart:    checkpoint.Macro.CurrentStart,
		currentMid:      checkpoint.Macro.CurrentMid,
		currentBid:      checkpoint.Macro.CurrentBid,
		currentAsk:      checkpoint.Macro.CurrentAsk,
		currentSegment:  checkpoint.Macro.CurrentSegment,
		lastObservation: checkpoint.Macro.LastObservation,
	}
	for _, bar := range checkpoint.Macro.Bars {
		s.makerMacroInventoryModel.bars = append(s.makerMacroInventoryModel.bars, macroInventoryBar{
			At: bar.At, Mid: bar.Mid, Bid: bar.Bid, Ask: bar.Ask, Segment: bar.Segment,
		})
	}
	return checkpoint.ReplayAfter, true, nil
}
