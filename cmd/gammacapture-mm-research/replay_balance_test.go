package main

import (
	"testing"
	"time"
)

func TestValidateSpotReplayStartingBalanceRejectsImplicitBorrowing(t *testing.T) {
	from := time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC)
	books := []bboSnapshot{{time: from, bid: 307_529, ask: 307_530}}
	if err := validateSpotReplayStartingBalance(books, from, 6_808, 0.022940981226); err == nil {
		t.Fatal("replay accepted base inventory that requires a negative JPY balance")
	}
	exactETHOnly := 6_808 / 307_529.5
	if err := validateSpotReplayStartingBalance(books, from, 6_808, exactETHOnly); err != nil {
		t.Fatalf("replay rejected a feasible 100%% ETH starting balance: %v", err)
	}
}
