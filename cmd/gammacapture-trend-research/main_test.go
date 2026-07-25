package main

import "testing"

func TestSignalTriggered(t *testing.T) {
	tests := []struct {
		name                string
		momentum, threshold float64
		side                string
		want                bool
	}{
		{name: "continuation meets positive threshold", momentum: 100, threshold: 100, side: "continuation", want: true},
		{name: "continuation rejects drawdown", momentum: -100, threshold: 100, side: "continuation", want: false},
		{name: "reversal meets negative threshold", momentum: -100, threshold: 100, side: "reversal", want: true},
		{name: "reversal rejects rally", momentum: 100, threshold: 100, side: "reversal", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := signalTriggered(tt.momentum, tt.threshold, tt.side); got != tt.want {
				t.Fatalf("signalTriggered(%v, %v, %q) = %v, want %v", tt.momentum, tt.threshold, tt.side, got, tt.want)
			}
		})
	}
}
