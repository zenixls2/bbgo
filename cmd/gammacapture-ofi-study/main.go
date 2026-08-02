package main

// gammacapture-ofi-study is a read-only public-data research command. It
// evaluates depth-normalized order-flow imbalance and signed-volume baselines
// against the next causal 15-minute pivot. It never connects to an exchange,
// reads private fills, or submits orders.

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type trade struct {
	at              time.Time
	price, quantity float64
	side            string
	id              uint64
}
type bbo struct {
	at                         time.Time
	mid                        float64
	bid, ask, bidSize, askSize float64
}
type bucket struct {
	start, lastMidAt                                          time.Time
	notional, signed, ofi, depth, queueSum, spreadSum, midSum float64
	count, midCount                                           int
}
type bar struct {
	start     time.Time
	high, low float64
}
type pivot struct {
	key   int64
	kind  string
	price float64
}
type observation struct {
	At                time.Time `json:"at"`
	BarStart          time.Time `json:"barStart"`
	OFIDepth          float64   `json:"ofiDepth"`
	VolumeZ           float64   `json:"volumeZ"`
	VolumePressure    float64   `json:"volumePressure"`
	QueueImbalance    float64   `json:"queueImbalance"`
	SpreadBps         float64   `json:"spreadBps"`
	ImpactPerNotional float64   `json:"impactPerNotional"`
	ShockAgeSeconds   float64   `json:"shockAgeSeconds"`
	ActualPivot       string    `json:"actualPivot"`
	PivotAt           time.Time `json:"pivotAt"`
	PivotPrice        float64   `json:"pivotPrice"`
	ExecutionAt       time.Time `json:"executionAt,omitempty"`
	ExecutionPrice    float64   `json:"executionPrice,omitempty"`
	GrossOFIBps       float64   `json:"grossOFIBps,omitempty"`
	GrossVolumeBps    float64   `json:"grossVolumeBps,omitempty"`
	GrossAgreementBps float64   `json:"grossAgreementBps,omitempty"`
	Markout30s        float64   `json:"markout30s,omitempty"`
	Markout1m         float64   `json:"markout1m,omitempty"`
	Markout5m         float64   `json:"markout5m,omitempty"`
	Markout10m        float64   `json:"markout10m,omitempty"`
	Censored          bool      `json:"censored"`
}
type metric struct {
	Name            string  `json:"name"`
	Samples         int     `json:"samples"`
	Signals         int     `json:"signals"`
	Hits            int     `json:"hits"`
	FeePositive     int     `json:"feePositive"`
	Coverage        float64 `json:"coverage"`
	HitRate         float64 `json:"hitRate"`
	FeePositiveRate float64 `json:"feePositiveRate"`
	MeanGrossBps    float64 `json:"meanGrossBps"`
	MeanNetBps      float64 `json:"meanNetBps"`
	MeanMarkout30s  float64 `json:"meanMarkout30s"`
	MeanMarkout1m   float64 `json:"meanMarkout1m"`
	MeanMarkout5m   float64 `json:"meanMarkout5m"`
	MeanMarkout10m  float64 `json:"meanMarkout10m"`
}

type report struct {
	Symbol               string    `json:"symbol"`
	From                 time.Time `json:"from"`
	To                   time.Time `json:"to"`
	TestFrom             time.Time `json:"testFrom"`
	BBOEvents            int       `json:"bboEvents"`
	TradeEvents          int       `json:"tradeEvents"`
	Bars                 int       `json:"bars"`
	Observations         int       `json:"observations"`
	TrainObservations    int       `json:"trainObservations"`
	TestObservations     int       `json:"testObservations"`
	MakerRoundTripFeeBps float64   `json:"makerRoundTripFeeBps"`
	TrainMetrics         []metric  `json:"trainMetrics"`
	Metrics              []metric  `json:"testMetrics"`
	Limitations          []string  `json:"limitations"`
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err == nil {
		return t
	}
	t, err = time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}
func files(root, symbol, kind string) []string {
	patterns := []string{
		filepath.Join(root, symbol+"-"+kind+"-*.csv"),
		filepath.Join(root, symbol, symbol+"-"+kind+"-*.csv"),
		filepath.Join(root, "live", symbol+"-"+kind+"-*.csv"),
	}
	out := []string{}
	for _, pattern := range patterns {
		matches, _ := filepath.Glob(pattern)
		out = append(out, matches...)
	}
	return out
}
func f(s string) float64 { v, _ := strconv.ParseFloat(s, 64); return v }
func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	x := append([]float64(nil), v...)
	sort.Float64s(x)
	m := len(x) / 2
	if len(x)%2 == 1 {
		return x[m]
	}
	return (x[m-1] + x[m]) / 2
}
func mean(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := 0.
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}
func read(root, symbol string, from, to time.Time) ([]trade, []bbo) {
	trades := []trade{}
	seen := map[uint64]bool{}
	for _, fn := range files(root, symbol, "trades") {
		file, e := os.Open(fn)
		if e != nil {
			continue
		}
		r := csv.NewReader(file)
		_, _ = r.Read()
		for {
			row, e := r.Read()
			if e == io.EOF {
				break
			}
			if e != nil || len(row) < 6 {
				continue
			}
			at, e := time.Parse(time.RFC3339Nano, row[0])
			if e != nil || at.Before(from) || !at.Before(to) {
				continue
			}
			p, q := f(row[3]), f(row[4])
			if p <= 0 || q <= 0 {
				continue
			}
			id, _ := strconv.ParseUint(row[2], 10, 64)
			if id > 0 && seen[id] {
				continue
			}
			if id > 0 {
				seen[id] = true
			}
			trades = append(trades, trade{at, p, q, strings.ToUpper(row[5]), id})
		}
		file.Close()
	}
	books := []bbo{}
	for _, fn := range files(root, symbol, "bookticker") {
		file, e := os.Open(fn)
		if e != nil {
			continue
		}
		r := csv.NewReader(file)
		_, _ = r.Read()
		for {
			row, e := r.Read()
			if e == io.EOF {
				break
			}
			if e != nil || len(row) < 5 {
				continue
			}
			at, e := time.Parse(time.RFC3339Nano, row[0])
			if e != nil || at.Before(from) || !at.Before(to) {
				continue
			}
			bid, ask := f(row[1]), f(row[3])
			bs, as := f(row[2]), f(row[4])
			if bid <= 0 || ask <= bid {
				continue
			}
			books = append(books, bbo{at: at, mid: (bid + ask) / 2, bid: bid, ask: ask, bidSize: bs, askSize: as})
		}
		file.Close()
	}
	sort.Slice(trades, func(i, j int) bool { return trades[i].at.Before(trades[j].at) })
	sort.Slice(books, func(i, j int) bool { return books[i].at.Before(books[j].at) })
	return trades, books
}
func aggregate(trades []trade, books []bbo) (map[int64]*bucket, map[int64]bar, []int64) {
	bm := map[int64]*bucket{}
	bars := map[int64]bar{}
	var prev bbo
	for i, x := range books {
		k := x.at.Unix() / 30
		q := bm[k]
		if q == nil {
			q = &bucket{start: time.Unix(k*30, 0).UTC()}
			bm[k] = q
		}
		q.lastMidAt = x.at
		q.depth += (x.bidSize + x.askSize)
		if q.depth < 0 {
			q.depth = 0
		}
		q.queueSum += (x.bidSize - x.askSize) / math.Max(x.bidSize+x.askSize, 1e-12)
		q.spreadSum += math.Log(x.ask/x.bid) * 10000
		q.midSum += x.mid
		q.midCount++
		if i > 0 {
			ofi := 0.
			if x.bid >= prev.bid {
				ofi += x.bidSize
			}
			if x.bid <= prev.bid {
				ofi -= prev.bidSize
			}
			if x.ask <= prev.ask {
				ofi -= x.askSize
			}
			if x.ask >= prev.ask {
				ofi += prev.askSize
			}
			q.ofi += ofi
		}
		prev = x
		bk := x.at.Unix() / 900
		barx, ok := bars[bk]
		if !ok {
			barx = bar{start: time.Unix(bk*900, 0).UTC(), high: x.mid, low: x.mid}
		} else {
			barx.high = math.Max(barx.high, x.mid)
			barx.low = math.Min(barx.low, x.mid)
		}
		bars[bk] = barx
	}
	for _, x := range trades {
		k := x.at.Unix() / 30
		q := bm[k]
		if q == nil {
			q = &bucket{start: time.Unix(k*30, 0).UTC()}
			bm[k] = q
		}
		n := x.price * x.quantity
		q.notional += n
		if x.side == "SELL" {
			q.signed -= n
		} else {
			q.signed += n
		}
	}
	keys := make([]int64, 0, len(bm))
	for k := range bm {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return bm, bars, keys
}
func pivots(bars map[int64]bar) []pivot {
	keys := make([]int64, 0, len(bars))
	for k := range bars {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	out := []pivot{}
	for i := 1; i < len(keys)-1; i++ {
		p, c, n := bars[keys[i-1]], bars[keys[i]], bars[keys[i+1]]
		if c.high > p.high && c.high >= n.high {
			out = append(out, pivot{keys[i], "HIGH", c.high})
		}
		if c.low < p.low && c.low <= n.low {
			out = append(out, pivot{keys[i], "LOW", c.low})
		}
	}
	return out
}
func bboAt(books []bbo, at time.Time) float64 {
	i := sort.Search(len(books), func(i int) bool { return !books[i].at.Before(at) })
	if i >= len(books) {
		return books[len(books)-1].mid
	}
	return books[i].mid
}
func firstTrade(trades []trade, at, end time.Time, side string) *trade {
	i := sort.Search(len(trades), func(i int) bool { return !trades[i].at.Before(at) })
	j := sort.Search(len(trades), func(i int) bool { return trades[i].at.After(end) })
	for k := i; k < j; k++ {
		if side == "" || trades[k].side == side {
			return &trades[k]
		}
	}
	if i < j {
		return &trades[i]
	}
	return nil
}
func buildObs(trades []trade, books []bbo, bm map[int64]*bucket, bars map[int64]bar, keys []int64, fee float64) []observation {
	piv := pivots(bars)
	pk := make([]int64, len(piv))
	for i, p := range piv {
		pk[i] = p.key
	}
	barKeys := make([]int64, 0, len(bars))
	for k := range bars {
		barKeys = append(barKeys, k)
	}
	sort.Slice(barKeys, func(i, j int) bool { return barKeys[i] < barKeys[j] })
	obs := []observation{}
	for _, bk := range barKeys {
		end := bk*30 + 29
		var cur int64 = -1
		for _, k := range keys {
			if k >= bk*30 && k <= end {
				cur = k
			}
		}
		if cur < 0 {
			continue
		}
		window := []*bucket{}
		for _, k := range keys {
			if k <= cur && k >= cur-19 {
				window = append(window, bm[k])
			}
		}
		if len(window) < 12 {
			continue
		}
		base := []float64{}
		for _, x := range window[:len(window)-1] {
			if x.notional > 0 {
				base = append(base, math.Log1p(x.notional))
			}
		}
		if len(base) < 8 || window[len(window)-1].notional <= 0 {
			continue
		}
		center := median(base)
		scale := 1.4826 * median(func() []float64 {
			v := []float64{}
			for _, x := range base {
				v = append(v, math.Abs(x-center))
			}
			return v
		}())
		if scale < .05 {
			scale = .05
		}
		last := window[len(window)-1]
		volz := (math.Log1p(last.notional) - center) / scale
		depth := mean(func() []float64 {
			v := []float64{}
			for _, x := range window {
				v = append(v, x.depth)
			}
			return v
		}())
		ofiDepth := 0.
		for _, x := range window {
			ofiDepth += x.ofi
		}
		ofiDepth /= math.Max(depth, 1e-9)
		queue := last.queueSum / math.Max(float64(last.midCount), 1)
		spread := last.spreadSum / math.Max(float64(last.midCount), 1)
		impact := 0.
		if len(window) > 10 && window[0].midCount > 0 && last.midCount > 0 {
			m0 := window[0].midSum / float64(window[0].midCount)
			m1 := last.midSum / float64(last.midCount)
			impact = math.Log(m1/m0) * 10000 / math.Max(math.Log1p(last.notional), 1)
		}
		shockAge := 0.
		for i := len(window) - 2; i >= 0; i-- {
			if window[i].notional > 0 {
				shockAge = float64((cur - window[i].start.Unix()/30)) * 30
				break
			}
		}
		pi := sort.Search(len(pk), func(i int) bool { return pk[i] > bk })
		if pi >= len(piv) {
			continue
		}
		p := piv[pi]
		sampleAt := time.Unix(cur*30+29, 0).UTC()
		ex := firstTrade(trades, sampleAt, time.Unix(p.key*900, 0).UTC(), "")
		o := observation{At: sampleAt, BarStart: time.Unix(bk*900, 0).UTC(), OFIDepth: ofiDepth, VolumeZ: volz, VolumePressure: last.signed / math.Max(last.notional, 1e-12), QueueImbalance: queue, SpreadBps: spread, ImpactPerNotional: impact, ShockAgeSeconds: shockAge, ActualPivot: p.kind, PivotAt: time.Unix(p.key*900, 0).UTC(), PivotPrice: p.price, Censored: true}
		if ex != nil {
			o.Censored = false
			o.ExecutionAt = ex.at
			o.ExecutionPrice = ex.price
			sign := 1.
			if ofiDepth < 0 {
				sign = -1
			}
			gross := (p.price/ex.price - 1) * 10000 * sign
			o.GrossOFIBps = gross
			vsign := 1.
			if last.signed < 0 {
				vsign = -1
			}
			o.GrossVolumeBps = (p.price/ex.price - 1) * 10000 * vsign
			agree := 1.
			if (ofiDepth > 0) != (last.signed > 0) {
				agree = -1
			}
			o.GrossAgreementBps = (p.price/ex.price - 1) * 10000 * sign * agree
			o.Markout30s = (bboAt(books, ex.at.Add(30*time.Second))/ex.price - 1) * 10000
			o.Markout1m = (bboAt(books, ex.at.Add(time.Minute))/ex.price - 1) * 10000
			o.Markout5m = (bboAt(books, ex.at.Add(5*time.Minute))/ex.price - 1) * 10000
			o.Markout10m = (bboAt(books, ex.at.Add(10*time.Minute))/ex.price - 1) * 10000
		}
		obs = append(obs, o)
	}
	_ = fee
	return obs
}
func evaluate(name string, rows []observation, from, to time.Time, fee float64) metric {
	var x []observation
	for _, r := range rows {
		if !r.Censored && !r.At.Before(from) && r.At.Before(to) {
			x = append(x, r)
		}
	}
	m := metric{Name: name, Samples: len(x)}
	gross, net, m30, m1, m5, m10 := []float64{}, []float64{}, []float64{}, []float64{}, []float64{}, []float64{}
	for _, r := range x {
		predHigh := r.OFIDepth > 0
		if name == "volume" {
			predHigh = r.VolumePressure > 0
		}
		if name == "agreement" && ((r.OFIDepth > 0) != (r.VolumePressure > 0)) {
			continue
		}
		m.Signals++
		hit := (predHigh && r.ActualPivot == "HIGH") || (!predHigh && r.ActualPivot == "LOW")
		if hit {
			m.Hits++
		}
		grossPred := (r.PivotPrice/r.ExecutionPrice - 1) * 10000
		if !predHigh {
			grossPred = (r.ExecutionPrice/r.PivotPrice - 1) * 10000
		}
		if grossPred >= fee {
			m.FeePositive++
		}
		gross = append(gross, grossPred)
		net = append(net, grossPred-fee)
		dir := 1.0
		if !predHigh {
			dir = -1
		}
		m30 = append(m30, r.Markout30s*dir)
		m1 = append(m1, r.Markout1m*dir)
		m5 = append(m5, r.Markout5m*dir)
		m10 = append(m10, r.Markout10m*dir)
	}
	if m.Samples > 0 {
		m.Coverage = float64(m.Signals) / float64(m.Samples)
	}
	if m.Signals > 0 {
		m.HitRate = float64(m.Hits) / float64(m.Signals)
		m.FeePositiveRate = float64(m.FeePositive) / float64(m.Signals)
		m.MeanGrossBps = mean(gross)
		m.MeanNetBps = mean(net)
		m.MeanMarkout30s = mean(m30)
		m.MeanMarkout1m = mean(m1)
		m.MeanMarkout5m = mean(m5)
		m.MeanMarkout10m = mean(m10)
	}
	return m
}
func main() {
	root := flag.String("data", "data/gammacapture", "capture root")
	symbol := flag.String("symbol", "SOLJPY", "symbol")
	from := flag.String("from", "2026-07-23", "start")
	to := flag.String("to", "2026-07-30", "exclusive end")
	test := flag.String("test-from", "2026-07-27", "test split")
	fee := flag.Float64("maker-round-trip-fee-bps", 15, "round-trip fee floor")
	jsonOut := flag.String("output", "", "JSON output path")
	csvOut := flag.String("csv-output", "", "observation CSV output path")
	flag.Parse()
	fr, tr, te := parseTime(*from), parseTime(*to), parseTime(*test)
	trades, books := read(*root, *symbol, fr, tr)
	if len(trades) == 0 || len(books) == 0 {
		panic("need overlapping public trades and BBO")
	}
	bm, bars, keys := aggregate(trades, books)
	rows := buildObs(trades, books, bm, bars, keys, *fee)
	rep := report{Symbol: *symbol, From: fr, To: tr, TestFrom: te, BBOEvents: len(books), TradeEvents: len(trades), Bars: len(bars), Observations: len(rows), MakerRoundTripFeeBps: *fee, Limitations: []string{"public aggregate trades are execution proxies, not private fills", "BBO is L1 only; no queue position, cancellations, or deeper depth", "rows overlap in time only at one sample per 15-minute bar", "model direction is fixed sign-of-OFI baseline; no parameter search is used"}}
	for _, r := range rows {
		if r.At.Before(te) {
			rep.TrainObservations++
		} else {
			rep.TestObservations++
		}
	}
	rep.TrainMetrics = []metric{evaluate("ofi-depth", rows, fr, te, *fee), evaluate("volume", rows, fr, te, *fee), evaluate("agreement", rows, fr, te, *fee)}
	rep.Metrics = []metric{evaluate("ofi-depth", rows, te, tr, *fee), evaluate("volume", rows, te, tr, *fee), evaluate("agreement", rows, te, tr, *fee)}
	if *jsonOut == "" {
		*jsonOut = filepath.Join("data/gammacapture/research", *symbol+"-ofi-pivot15m-study.json")
	}
	if *csvOut == "" {
		*csvOut = filepath.Join("data/gammacapture/research", *symbol+"-ofi-pivot15m-study.csv")
	}
	os.MkdirAll(filepath.Dir(*jsonOut), 0755)
	jf, _ := os.Create(*jsonOut)
	e := json.NewEncoder(jf)
	e.SetIndent("", "  ")
	_ = e.Encode(rep)
	jf.Close()
	cf, _ := os.Create(*csvOut)
	cw := csv.NewWriter(cf)
	cw.Write([]string{"at", "ofiDepth", "volumeZ", "volumePressure", "queueImbalance", "spreadBps", "impactPerNotional", "shockAgeSeconds", "actualPivot", "pivotAt", "executionAt", "executionPrice", "grossOFIBps", "grossVolumeBps", "grossAgreementBps", "markout30s", "markout1m", "markout5m", "markout10m", "censored"})
	for _, r := range rows {
		cw.Write([]string{r.At.Format(time.RFC3339), fmt.Sprintf("%.8f", r.OFIDepth), fmt.Sprintf("%.8f", r.VolumeZ), fmt.Sprintf("%.8f", r.VolumePressure), fmt.Sprintf("%.8f", r.QueueImbalance), fmt.Sprintf("%.4f", r.SpreadBps), fmt.Sprintf("%.8f", r.ImpactPerNotional), fmt.Sprintf("%.1f", r.ShockAgeSeconds), r.ActualPivot, r.PivotAt.Format(time.RFC3339), r.ExecutionAt.Format(time.RFC3339Nano), fmt.Sprintf("%.8f", r.ExecutionPrice), fmt.Sprintf("%.4f", r.GrossOFIBps), fmt.Sprintf("%.4f", r.GrossVolumeBps), fmt.Sprintf("%.4f", r.GrossAgreementBps), fmt.Sprintf("%.4f", r.Markout30s), fmt.Sprintf("%.4f", r.Markout1m), fmt.Sprintf("%.4f", r.Markout5m), fmt.Sprintf("%.4f", r.Markout10m), strconv.FormatBool(r.Censored)})
	}
	cw.Flush()
	cf.Close()
	out, _ := json.MarshalIndent(rep, "", "  ")
	fmt.Println(string(out))
}
