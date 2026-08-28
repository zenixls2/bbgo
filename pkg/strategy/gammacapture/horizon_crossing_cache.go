package gammacapture

import (
	"math"
	"sort"
	"time"
)

// marketMakerHorizonExposure is the distance-independent part of one
// completed maker holding window. The quote-distance query only compares its
// barriers with these exact executable-BBO excursions; it does not rebuild
// the future max/min path.
type marketMakerHorizonExposure struct {
	At                     time.Time
	EndAt                  time.Time
	StartBid               float64
	StartAsk               float64
	StartBBOWeightedPrice  float64
	WindowBBOWeightedPrice float64
	StartBookImbalance     float64
	StartBookDepthReady    bool
	TerminalBid            float64
	TerminalAsk            float64
	MinimumAsk             float64
	MaximumBid             float64
	BuyExcursionBps        float64
	SellExcursionBps       float64
	ConditionalState       conditionalExecutionState
	NextMinute             int
}

type marketMakerHorizonExposureCache struct {
	Initialized             bool
	BuiltThrough            time.Time
	LastPointAt             time.Time
	Exposures               []marketMakerHorizonExposure
	ConditionalStateBuilder conditionalExecutionStateBuilder
	BadPrefix               []int
	MaxBidDeque             []int
	MinAskDeque             []int
	NextLinkCursor          int
	NextLinkSearch          int
}

func (c *marketMakerHorizonExposureCache) conditionalStates(
	points []MarketMakerHorizonPoint, horizon time.Duration,
) []conditionalExecutionState {
	if c == nil {
		return nil
	}
	return c.ConditionalStateBuilder.appendThrough(points, horizon)
}

// crossingExposures incrementally materializes the exact distance-independent
// first-passage path. Initial warmup is O(N); afterwards each immutable
// completed start window is appended once. A same-second BBO replacement is
// pending: a completed window ending at that second excludes the current point
// (At.Before(endAt)), so it is consumed only after the next point arrives.
func (m *MarketMakerHorizonModel) crossingExposures(horizon time.Duration) []marketMakerHorizonExposure {
	if m == nil || horizon <= 0 || len(m.points) == 0 {
		return nil
	}
	if m.crossingExposureCaches == nil {
		m.crossingExposureCaches = make(map[time.Duration]*marketMakerHorizonExposureCache)
	}
	cache := m.crossingExposureCaches[horizon]
	if cache == nil {
		cache = &marketMakerHorizonExposureCache{}
		m.crossingExposureCaches[horizon] = cache
	}
	lastPointAt := m.points[len(m.points)-1].At
	if cache.Initialized {
		switch {
		case lastPointAt.Before(cache.LastPointAt):
			cache.Initialized = false
		case lastPointAt.Equal(cache.LastPointAt):
			return cache.Exposures
		default:
			m.appendCompletedHorizonExposures(cache, horizon, lastPointAt)
			cache.LastPointAt = lastPointAt
			m.trimHorizonExposures(cache)
			refreshHorizonExposureMinuteLinks(cache)
			return cache.Exposures
		}
	}

	pointCount := len(m.points)
	if cap(cache.BadPrefix) < pointCount+1 {
		cache.BadPrefix = make([]int, pointCount+1)
	} else {
		cache.BadPrefix = cache.BadPrefix[:pointCount+1]
		clear(cache.BadPrefix)
	}
	for index := range m.points {
		cache.BadPrefix[index+1] = cache.BadPrefix[index]
		bid, ask := m.points[index].bidPrice(), m.points[index].askPrice()
		if m.points[index].GapBefore || bid <= 0 || ask < bid {
			cache.BadPrefix[index+1]++
		}
	}
	// Integral prefix for the piecewise-constant BBO-weighted price. Binance
	// book-ticker is change-driven, so event-count averaging would overweight
	// busy intervals. Prefix integration makes every initial completed-window
	// mean O(1) after this O(N) build.
	weightedAreaPrefix := make([]float64, pointCount)
	for index := 1; index < pointCount; index++ {
		seconds := m.points[index].At.Sub(m.points[index-1].At).Seconds()
		if seconds > 0 {
			weightedAreaPrefix[index] = weightedAreaPrefix[index-1] +
				m.points[index-1].bboWeightedPrice()*seconds
		} else {
			weightedAreaPrefix[index] = weightedAreaPrefix[index-1]
		}
	}

	cache.Exposures = cache.Exposures[:0]
	cache.MaxBidDeque = cache.MaxBidDeque[:0]
	cache.MinAskDeque = cache.MinAskDeque[:0]
	cache.BuiltThrough = time.Time{}
	conditionalStates := cache.conditionalStates(m.points, horizon)
	right := 1
	pushWindow := func(index int) {
		bid, ask := m.points[index].bidPrice(), m.points[index].askPrice()
		for len(cache.MaxBidDeque) > 0 {
			lastIndex := cache.MaxBidDeque[len(cache.MaxBidDeque)-1]
			if m.points[lastIndex].bidPrice() > bid {
				break
			}
			cache.MaxBidDeque = cache.MaxBidDeque[:len(cache.MaxBidDeque)-1]
		}
		cache.MaxBidDeque = append(cache.MaxBidDeque, index)
		for len(cache.MinAskDeque) > 0 {
			lastIndex := cache.MinAskDeque[len(cache.MinAskDeque)-1]
			if m.points[lastIndex].askPrice() < ask {
				break
			}
			cache.MinAskDeque = cache.MinAskDeque[:len(cache.MinAskDeque)-1]
		}
		cache.MinAskDeque = append(cache.MinAskDeque, index)
	}

	for index, start := range m.points {
		startBid, startAsk := start.bidPrice(), start.askPrice()
		endAt := start.At.Add(horizon)
		if endAt.After(lastPointAt) {
			break
		}
		cache.BuiltThrough = start.At
		if right < index+1 {
			right = index + 1
		}
		for right < pointCount && m.points[right].At.Before(endAt) {
			pushWindow(right)
			right++
		}
		for len(cache.MaxBidDeque) > 0 && cache.MaxBidDeque[0] <= index {
			cache.MaxBidDeque = cache.MaxBidDeque[1:]
		}
		for len(cache.MinAskDeque) > 0 && cache.MinAskDeque[0] <= index {
			cache.MinAskDeque = cache.MinAskDeque[1:]
		}
		if start.GapBefore || startBid <= 0 || startAsk < startBid || right <= index+1 ||
			cache.BadPrefix[right]-cache.BadPrefix[index+1] > 0 ||
			len(cache.MaxBidDeque) == 0 || len(cache.MinAskDeque) == 0 {
			continue
		}
		maxBid := m.points[cache.MaxBidDeque[0]].bidPrice()
		minAsk := m.points[cache.MinAskDeque[0]].askPrice()
		terminalBid := m.points[right-1].bidPrice()
		terminalAsk := m.points[right-1].askPrice()
		startWeightedPrice := start.bboWeightedPrice()
		terminalWeightedPrice := m.points[right-1].bboWeightedPrice()
		weightedArea := weightedAreaPrefix[right-1] - weightedAreaPrefix[index] +
			terminalWeightedPrice*endAt.Sub(m.points[right-1].At).Seconds()
		windowWeightedPrice := weightedArea / horizon.Seconds()
		if startWeightedPrice <= 0 || windowWeightedPrice <= 0 {
			continue
		}
		cache.Exposures = append(cache.Exposures, marketMakerHorizonExposure{
			At:                     start.At,
			EndAt:                  endAt,
			StartBid:               startBid,
			StartAsk:               startAsk,
			StartBBOWeightedPrice:  startWeightedPrice,
			WindowBBOWeightedPrice: windowWeightedPrice,
			StartBookImbalance:     start.BookImbalance,
			StartBookDepthReady:    start.BookDepthReady,
			TerminalBid:            terminalBid,
			TerminalAsk:            terminalAsk,
			MinimumAsk:             minAsk,
			MaximumBid:             maxBid,
			BuyExcursionBps:        math.Log(startAsk/minAsk) * 10_000,
			SellExcursionBps:       math.Log(maxBid/startBid) * 10_000,
			ConditionalState:       conditionalStates[index],
		})
	}

	cache.Initialized = true
	cache.LastPointAt = lastPointAt
	resetHorizonExposureMinuteLinks(cache)
	refreshHorizonExposureMinuteLinks(cache)
	return cache.Exposures
}

func (m *MarketMakerHorizonModel) appendCompletedHorizonExposures(
	cache *marketMakerHorizonExposureCache,
	horizon time.Duration,
	lastPointAt time.Time,
) {
	first := 0
	if !cache.BuiltThrough.IsZero() {
		first = sort.Search(len(m.points), func(index int) bool {
			return m.points[index].At.After(cache.BuiltThrough)
		})
	}
	end := sort.Search(len(m.points), func(index int) bool {
		return m.points[index].At.Add(horizon).After(lastPointAt)
	})
	if first >= end {
		return
	}

	// Build every newly matured start as one sliding-window batch. The prior
	// implementation rebuilt the entire conditional lookback for every new
	// start. Monotone extrema, an integral prefix, and the carried conditional
	// state builder make this exact batch O(newStarts) after initialization.
	conditionalStates := cache.conditionalStates(m.points, horizon)

	localCount := len(m.points) - first
	badPrefix := make([]int, localCount+1)
	weightedAreaPrefix := make([]float64, localCount)
	for local := 0; local < localCount; local++ {
		global := first + local
		point := m.points[global]
		badPrefix[local+1] = badPrefix[local]
		bid, ask := point.bidPrice(), point.askPrice()
		if point.GapBefore || bid <= 0 || ask < bid || point.bboWeightedPrice() <= 0 {
			badPrefix[local+1]++
		}
		if local > 0 {
			seconds := point.At.Sub(m.points[global-1].At).Seconds()
			weightedAreaPrefix[local] = weightedAreaPrefix[local-1]
			if seconds > 0 {
				weightedAreaPrefix[local] += m.points[global-1].bboWeightedPrice() * seconds
			}
		}
	}

	maxBidDeque := make([]int, 0, int(horizon.Seconds())+1)
	minAskDeque := make([]int, 0, int(horizon.Seconds())+1)
	right := first + 1
	pushWindow := func(index int) {
		bid, ask := m.points[index].bidPrice(), m.points[index].askPrice()
		for len(maxBidDeque) > 0 && m.points[maxBidDeque[len(maxBidDeque)-1]].bidPrice() <= bid {
			maxBidDeque = maxBidDeque[:len(maxBidDeque)-1]
		}
		maxBidDeque = append(maxBidDeque, index)
		for len(minAskDeque) > 0 && m.points[minAskDeque[len(minAskDeque)-1]].askPrice() >= ask {
			minAskDeque = minAskDeque[:len(minAskDeque)-1]
		}
		minAskDeque = append(minAskDeque, index)
	}

	for index := first; index < end; index++ {
		start := m.points[index]
		endAt := start.At.Add(horizon)
		cache.BuiltThrough = start.At
		if right < index+1 {
			right = index + 1
		}
		for right < len(m.points) && m.points[right].At.Before(endAt) {
			pushWindow(right)
			right++
		}
		for len(maxBidDeque) > 0 && maxBidDeque[0] <= index {
			maxBidDeque = maxBidDeque[1:]
		}
		for len(minAskDeque) > 0 && minAskDeque[0] <= index {
			minAskDeque = minAskDeque[1:]
		}
		startBid, startAsk := start.bidPrice(), start.askPrice()
		leftLocal, rightLocal := index+1-first, right-first
		if start.GapBefore || startBid <= 0 || startAsk < startBid || right <= index+1 ||
			badPrefix[rightLocal]-badPrefix[leftLocal] > 0 ||
			len(maxBidDeque) == 0 || len(minAskDeque) == 0 {
			continue
		}
		terminal := m.points[right-1]
		terminalBid, terminalAsk := terminal.bidPrice(), terminal.askPrice()
		startWeightedPrice := start.bboWeightedPrice()
		terminalWeightedPrice := terminal.bboWeightedPrice()
		weightedArea := weightedAreaPrefix[right-1-first] - weightedAreaPrefix[index-first] +
			terminalWeightedPrice*endAt.Sub(terminal.At).Seconds()
		windowWeightedPrice := weightedArea / horizon.Seconds()
		if terminalBid <= 0 || terminalAsk < terminalBid || startWeightedPrice <= 0 || windowWeightedPrice <= 0 {
			continue
		}
		cache.Exposures = append(cache.Exposures, marketMakerHorizonExposure{
			At:                     start.At,
			EndAt:                  endAt,
			StartBid:               startBid,
			StartAsk:               startAsk,
			StartBBOWeightedPrice:  startWeightedPrice,
			WindowBBOWeightedPrice: windowWeightedPrice,
			StartBookImbalance:     start.BookImbalance,
			StartBookDepthReady:    start.BookDepthReady,
			TerminalBid:            terminalBid,
			TerminalAsk:            terminalAsk,
			MinimumAsk:             m.points[minAskDeque[0]].askPrice(),
			MaximumBid:             m.points[maxBidDeque[0]].bidPrice(),
			BuyExcursionBps:        math.Log(startAsk/m.points[minAskDeque[0]].askPrice()) * 10_000,
			SellExcursionBps:       math.Log(m.points[maxBidDeque[0]].bidPrice()/startBid) * 10_000,
			ConditionalState:       conditionalStates[index],
		})
	}
}

// horizonExposureAtIndex handles only newly completed windows. With roughly
// one newly completed start per sampled second, its O(H) work replaces an
// O(N) rebuild of every historical start. Initial history uses the O(N)
// monotone-deque builder above.
func horizonExposureAtIndex(points []MarketMakerHorizonPoint, index int, horizon time.Duration) (marketMakerHorizonExposure, bool) {
	start := points[index]
	startBid, startAsk := start.bidPrice(), start.askPrice()
	if start.GapBefore || startBid <= 0 || startAsk < startBid {
		return marketMakerHorizonExposure{}, false
	}
	endAt := start.At.Add(horizon)
	startWeightedPrice := start.bboWeightedPrice()
	maxBid, minAsk, terminalBid, terminalAsk, futurePoints := 0.0, math.Inf(1), 0.0, 0.0, 0
	weightedArea := 0.0
	weightedAt, weightedPrice := start.At, startWeightedPrice
	for future := index + 1; future < len(points) && points[future].At.Before(endAt); future++ {
		point := points[future]
		bid, ask := point.bidPrice(), point.askPrice()
		pointWeightedPrice := point.bboWeightedPrice()
		if point.GapBefore || bid <= 0 || ask < bid || pointWeightedPrice <= 0 {
			return marketMakerHorizonExposure{}, false
		}
		weightedArea += weightedPrice * point.At.Sub(weightedAt).Seconds()
		weightedAt, weightedPrice = point.At, pointWeightedPrice
		futurePoints++
		maxBid = math.Max(maxBid, bid)
		minAsk = math.Min(minAsk, ask)
		terminalBid, terminalAsk = bid, ask
	}
	if futurePoints == 0 || maxBid <= 0 || math.IsInf(minAsk, 1) {
		return marketMakerHorizonExposure{}, false
	}
	weightedArea += weightedPrice * endAt.Sub(weightedAt).Seconds()
	windowWeightedPrice := weightedArea / horizon.Seconds()
	if startWeightedPrice <= 0 || windowWeightedPrice <= 0 {
		return marketMakerHorizonExposure{}, false
	}
	return marketMakerHorizonExposure{
		At:                     start.At,
		EndAt:                  endAt,
		StartBid:               startBid,
		StartAsk:               startAsk,
		StartBBOWeightedPrice:  startWeightedPrice,
		WindowBBOWeightedPrice: windowWeightedPrice,
		StartBookImbalance:     start.BookImbalance,
		StartBookDepthReady:    start.BookDepthReady,
		TerminalBid:            terminalBid,
		TerminalAsk:            terminalAsk,
		MinimumAsk:             minAsk,
		MaximumBid:             maxBid,
		BuyExcursionBps:        math.Log(startAsk/minAsk) * 10_000,
		SellExcursionBps:       math.Log(maxBid/startBid) * 10_000,
		ConditionalState:       conditionalExecutionStateAtIndex(points, index, horizon),
	}, true
}

func (m *MarketMakerHorizonModel) trimHorizonExposures(cache *marketMakerHorizonExposureCache) {
	if len(cache.Exposures) == 0 || len(m.points) == 0 {
		return
	}
	firstPointAt := m.points[0].At
	first := sort.Search(len(cache.Exposures), func(index int) bool {
		return !cache.Exposures[index].At.Before(firstPointAt)
	})
	if first == 0 {
		return
	}
	copy(cache.Exposures, cache.Exposures[first:])
	cache.Exposures = cache.Exposures[:len(cache.Exposures)-first]
	resetHorizonExposureMinuteLinks(cache)
}

func resetHorizonExposureMinuteLinks(cache *marketMakerHorizonExposureCache) {
	for index := range cache.Exposures {
		cache.Exposures[index].NextMinute = 0
	}
	cache.NextLinkCursor = 0
	cache.NextLinkSearch = 1
}

// refreshHorizonExposureMinuteLinks resolves only links that lacked a future
// record during the previous update. Every resolved link is immutable, so its
// total work is amortized O(number of exposures).
func refreshHorizonExposureMinuteLinks(cache *marketMakerHorizonExposureCache) {
	for cache.NextLinkCursor < len(cache.Exposures) {
		if cache.NextLinkSearch <= cache.NextLinkCursor {
			cache.NextLinkSearch = cache.NextLinkCursor + 1
		}
		minimumNext := cache.Exposures[cache.NextLinkCursor].At.Add(time.Minute)
		for cache.NextLinkSearch < len(cache.Exposures) && cache.Exposures[cache.NextLinkSearch].At.Before(minimumNext) {
			cache.NextLinkSearch++
		}
		if cache.NextLinkSearch >= len(cache.Exposures) {
			return
		}
		cache.Exposures[cache.NextLinkCursor].NextMinute = cache.NextLinkSearch
		cache.NextLinkCursor++
	}
}

func firstHorizonExposureAtOrAfter(exposures []marketMakerHorizonExposure, cutoff time.Time) int {
	return sort.Search(len(exposures), func(index int) bool {
		return !exposures[index].At.Before(cutoff)
	})
}
