package main

import "time"

// minuteRegimeClose is the neutral executable-BBO minute close shared by
// variance, drawdown, and consolidation research. It contains no regime model.
type minuteRegimeClose struct {
	at       time.Time
	bid, ask float64
}

func minuteRegimeCloses(books []bboSnapshot) []minuteRegimeClose {
	if len(books) == 0 {
		return nil
	}
	out := make([]minuteRegimeClose, 0, len(books)/10)
	for _, book := range books {
		minute := book.time.UTC().Truncate(time.Minute)
		if len(out) == 0 || !out[len(out)-1].at.Equal(minute) {
			out = append(out, minuteRegimeClose{at: minute, bid: book.bid, ask: book.ask})
			continue
		}
		out[len(out)-1].bid = book.bid
		out[len(out)-1].ask = book.ask
	}
	return out
}
