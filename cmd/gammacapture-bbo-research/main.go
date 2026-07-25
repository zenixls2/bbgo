// gammacapture-bbo-research summarizes public BBO capture quality and the
// spread/freshness properties relevant to the paper-mode entry gate.
package main

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"time"
)

type bookSample struct {
	received time.Time
	bid, ask float64
}

type report struct {
	BookUpdates           int     `json:"bookUpdates"`
	TradeEvents           int     `json:"tradeEvents"`
	PositiveSpreads       int     `json:"positiveSpreads"`
	SpreadP50Bps          float64 `json:"spreadP50Bps"`
	SpreadP95Bps          float64 `json:"spreadP95Bps"`
	SpreadMaxBps          float64 `json:"spreadMaxBps"`
	WithinMaxSpreadPct    float64 `json:"withinMaxSpreadPct"`
	BookGapP95Seconds     float64 `json:"bookGapP95Seconds"`
	BookGapMaxSeconds     float64 `json:"bookGapMaxSeconds"`
	TradeBookAgeP95Second float64 `json:"tradeBookAgeP95Seconds"`
	TradeBookAgeMaxSecond float64 `json:"tradeBookAgeMaxSeconds"`
	TradesWithoutPriorBBO int     `json:"tradesWithoutPriorBBO"`
	TradesPassingBookAge  int     `json:"tradesPassingBookAge"`
	TradeBookPassRatePct  float64 `json:"tradeBookPassRatePct"`
}

func main() {
	bookPath := flag.String("book", "", "bookticker CSV produced by gammacapture-capture")
	tradePath := flag.String("trades", "", "trade CSV produced by gammacapture-capture")
	maxSpread := flag.Float64("max-spread-bps", 10, "paper entry spread limit")
	maxBookAge := flag.Duration("max-book-age", 5*time.Second, "paper entry BBO freshness limit")
	flag.Parse()
	if *bookPath == "" || *tradePath == "" || *maxSpread < 0 || *maxBookAge < 0 {
		fatalf("book, trades, non-negative max-spread-bps, and non-negative max-book-age are required")
	}
	books, err := readBooks(*bookPath)
	if err != nil {
		fatalf("read books: %v", err)
	}
	trades, err := readTradeTimes(*tradePath)
	if err != nil {
		fatalf("read trades: %v", err)
	}
	r := summarize(books, trades, *maxSpread, *maxBookAge)
	if err := json.NewEncoder(os.Stdout).Encode(r); err != nil {
		fatalf("write report: %v", err)
	}
}

func readBooks(path string) ([]bookSample, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	if _, err := r.Read(); err != nil {
		return nil, err
	}
	var books []bookSample
	for {
		record, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(record) < 4 {
			return nil, fmt.Errorf("book record has %d fields", len(record))
		}
		received, err := time.Parse(time.RFC3339Nano, record[0])
		if err != nil {
			return nil, err
		}
		var bid, ask float64
		if _, err := fmt.Sscan(record[1], &bid); err != nil {
			return nil, err
		}
		if _, err := fmt.Sscan(record[3], &ask); err != nil {
			return nil, err
		}
		if bid > 0 && ask > 0 && ask >= bid {
			books = append(books, bookSample{received: received, bid: bid, ask: ask})
		}
	}
	return books, nil
}

func readTradeTimes(path string) ([]time.Time, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	if _, err := r.Read(); err != nil {
		return nil, err
	}
	var times []time.Time
	for {
		record, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(record) < 2 {
			return nil, fmt.Errorf("trade record has %d fields", len(record))
		}
		received, err := time.Parse(time.RFC3339Nano, record[1])
		if err != nil {
			return nil, err
		}
		times = append(times, received)
	}
	return times, nil
}

func summarize(books []bookSample, trades []time.Time, maxSpreadBps float64, maxBookAge time.Duration) report {
	r := report{BookUpdates: len(books), TradeEvents: len(trades)}
	spreads := make([]float64, 0, len(books))
	bookGaps := make([]float64, 0, len(books)-1)
	within := 0
	for i, book := range books {
		spread := math.Log(book.ask/book.bid) * 10_000
		if spread > 0 {
			spreads = append(spreads, spread)
			if spread <= maxSpreadBps {
				within++
			}
		}
		if i > 0 {
			gap := book.received.Sub(books[i-1].received).Seconds()
			if gap >= 0 {
				bookGaps = append(bookGaps, gap)
			}
		}
	}
	r.PositiveSpreads = len(spreads)
	r.SpreadP50Bps = quantile(spreads, .50)
	r.SpreadP95Bps = quantile(spreads, .95)
	r.SpreadMaxBps = quantile(spreads, 1)
	if r.PositiveSpreads > 0 {
		r.WithinMaxSpreadPct = float64(within) * 100 / float64(r.PositiveSpreads)
	}
	r.BookGapP95Seconds = quantile(bookGaps, .95)
	r.BookGapMaxSeconds = quantile(bookGaps, 1)
	ages := make([]float64, 0, len(trades))
	bookIndex := 0
	for _, tradeTime := range trades {
		for bookIndex+1 < len(books) && !books[bookIndex+1].received.After(tradeTime) {
			bookIndex++
		}
		if len(books) == 0 || books[bookIndex].received.After(tradeTime) {
			r.TradesWithoutPriorBBO++
			continue
		}
		age := tradeTime.Sub(books[bookIndex].received).Seconds()
		if age >= 0 {
			ages = append(ages, age)
			if time.Duration(age*float64(time.Second)) <= maxBookAge {
				r.TradesPassingBookAge++
			}
		}
	}
	if r.TradeEvents > 0 {
		r.TradeBookPassRatePct = float64(r.TradesPassingBookAge) * 100 / float64(r.TradeEvents)
	}
	r.TradeBookAgeP95Second = quantile(ages, .95)
	r.TradeBookAgeMaxSecond = quantile(ages, 1)
	return r
}

func quantile(values []float64, q float64) float64 {
	if len(values) == 0 || q < 0 || q > 1 {
		return 0
	}
	copyValues := append([]float64(nil), values...)
	sort.Float64s(copyValues)
	index := int(math.Ceil(q*float64(len(copyValues)))) - 1
	if index < 0 {
		index = 0
	}
	return copyValues[index]
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(2)
}
