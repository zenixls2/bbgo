package gammacapture

import (
	"strconv"
	"time"
)

// GateStats is an auditable sequential entry funnel. Backtest StateRecorder
// writes it alongside the position so a zero-trade result still explains which
// condition rejected the candidate bars.
type GateStats struct {
	Time time.Time `json:"time"`

	FlatObservations int `json:"flatObservations"`
	Cooldown         int `json:"cooldown"`
	Healthy          int `json:"healthy"`
	// RawSignalUp is recorded before the model-health gate.  It distinguishes
	// a missing confidence transition from one that occurred during warm-up.
	RawSignalUp      int     `json:"rawSignalUp"`
	ProbabilityReady int     `json:"probabilityReady"`
	SignalUp         int     `json:"signalUp"`
	SignalWindow     int     `json:"signalWindow"`
	Probability      int     `json:"probability"`
	Expectancy       int     `json:"expectancy"`
	Range            int     `json:"range"`
	BarQuality       int     `json:"barQuality"`
	Trend            int     `json:"trend"`
	Retrace          int     `json:"retrace"`
	Book             int     `json:"book"`
	Balance          int     `json:"balance"`
	Quantity         int     `json:"quantity"`
	Entries          int     `json:"entries"`
	ResearchForced   int     `json:"researchForced"`
	MaxTPProbability float64 `json:"maxTPProbability"`
	MaxRawNetEdgeBps float64 `json:"maxRawNetEdgeBps"`
	MaxExpectancyBps float64 `json:"maxExpectancyBps"`
	MaxRangeBps      float64 `json:"maxRangeBps"`
}

func (s *GateStats) CsvHeader() []string {
	return []string{
		"time", "flat_observations", "cooldown", "eligible_after_cooldown", "healthy", "raw_signal_up", "probability_ready", "signal_up", "signal_window", "probability", "expectancy", "range", "bar_quality", "trend", "retrace", "book", "balance", "quantity", "entries", "research_forced", "max_tp_probability", "max_raw_net_edge_bps", "max_expectancy_bps", "max_range_bps",
		"healthy_rate_pct", "raw_signal_rate_from_flat_pct", "probability_ready_rate_from_healthy_pct", "signal_up_rate_from_healthy_pct", "signal_window_rate_from_healthy_pct", "probability_rate_from_signal_window_pct", "expectancy_rate_from_probability_pct", "range_rate_from_expectancy_pct", "bar_quality_rate_from_range_pct", "trend_rate_from_bar_quality_pct", "retrace_rate_from_trend_pct", "book_rate_from_retrace_pct", "balance_rate_from_book_pct", "quantity_rate_from_balance_pct", "entry_rate_from_quantity_pct",
	}
}

func (s *GateStats) CsvRecords() [][]string {
	eligibleAfterCooldown := s.FlatObservations - s.Cooldown
	if eligibleAfterCooldown < 0 {
		eligibleAfterCooldown = 0
	}
	return [][]string{{
		s.Time.Format(time.RFC3339),
		strconv.Itoa(s.FlatObservations),
		strconv.Itoa(s.Cooldown),
		strconv.Itoa(eligibleAfterCooldown),
		strconv.Itoa(s.Healthy),
		strconv.Itoa(s.RawSignalUp),
		strconv.Itoa(s.ProbabilityReady),
		strconv.Itoa(s.SignalUp),
		strconv.Itoa(s.SignalWindow),
		strconv.Itoa(s.Probability),
		strconv.Itoa(s.Expectancy),
		strconv.Itoa(s.Range),
		strconv.Itoa(s.BarQuality),
		strconv.Itoa(s.Trend),
		strconv.Itoa(s.Retrace),
		strconv.Itoa(s.Book),
		strconv.Itoa(s.Balance),
		strconv.Itoa(s.Quantity),
		strconv.Itoa(s.Entries),
		strconv.Itoa(s.ResearchForced),
		strconv.FormatFloat(s.MaxTPProbability, 'f', 8, 64),
		strconv.FormatFloat(s.MaxRawNetEdgeBps, 'f', 4, 64),
		strconv.FormatFloat(s.MaxExpectancyBps, 'f', 4, 64),
		strconv.FormatFloat(s.MaxRangeBps, 'f', 4, 64),
		formatGateRate(s.Healthy, eligibleAfterCooldown),
		formatGateRate(s.RawSignalUp, s.FlatObservations),
		formatGateRate(s.ProbabilityReady, s.Healthy),
		formatGateRate(s.SignalUp, s.Healthy),
		formatGateRate(s.SignalWindow, s.Healthy),
		formatGateRate(s.Probability, s.SignalWindow),
		formatGateRate(s.Expectancy, s.Probability),
		formatGateRate(s.Range, s.Expectancy),
		formatGateRate(s.BarQuality, s.Range),
		formatGateRate(s.Trend, s.BarQuality),
		formatGateRate(s.Retrace, s.Trend),
		formatGateRate(s.Book, s.Retrace),
		formatGateRate(s.Balance, s.Book),
		formatGateRate(s.Quantity, s.Balance),
		formatGateRate(s.Entries, s.Quantity),
	}}
}

func formatGateRate(numerator, denominator int) string {
	if denominator <= 0 {
		return ""
	}
	return strconv.FormatFloat(float64(numerator)*100/float64(denominator), 'f', 4, 64)
}
