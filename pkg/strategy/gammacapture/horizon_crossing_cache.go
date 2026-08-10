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
	At               time.Time
	EndAt            time.Time
	StartBid         float64
	StartAsk         float64
	TerminalBid      float64
	TerminalAsk      float64
	BuyExcursionBps  float64
	SellExcursionBps float64
	NextMinute       int
}

type marketMakerHorizonExposureCache struct {
	Initialized    bool
	BuiltThrough   time.Time
	LastPointAt    time.Time
	Exposures      []marketMakerHorizonExposure
	BadPrefix      []int
	MaxBidDeque    []int
	MinAskDeque    []int
	NextLinkCursor int
	NextLinkSearch int
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

	cache.Exposures = cache.Exposures[:0]
	cache.MaxBidDeque = cache.MaxBidDeque[:0]
	cache.MinAskDeque = cache.MinAskDeque[:0]
	cache.BuiltThrough = time.Time{}
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
		cache.Exposures = append(cache.Exposures, marketMakerHorizonExposure{
			At:               start.At,
			EndAt:            endAt,
			StartBid:         startBid,
			StartAsk:         startAsk,
			TerminalBid:      terminalBid,
			TerminalAsk:      terminalAsk,
			BuyExcursionBps:  math.Log(startAsk/minAsk) * 10_000,
			SellExcursionBps: math.Log(maxBid/startBid) * 10_000,
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
	for index := first; index < len(m.points); index++ {
		start := m.points[index]
		if start.At.Add(horizon).After(lastPointAt) {
			break
		}
		cache.BuiltThrough = start.At
		if exposure, ok := horizonExposureAtIndex(m.points, index, horizon); ok {
			cache.Exposures = append(cache.Exposures, exposure)
		}
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
	maxBid, minAsk, terminalBid, terminalAsk, futurePoints := 0.0, math.Inf(1), 0.0, 0.0, 0
	for future := index + 1; future < len(points) && points[future].At.Before(endAt); future++ {
		point := points[future]
		bid, ask := point.bidPrice(), point.askPrice()
		if point.GapBefore || bid <= 0 || ask < bid {
			return marketMakerHorizonExposure{}, false
		}
		futurePoints++
		maxBid = math.Max(maxBid, bid)
		minAsk = math.Min(minAsk, ask)
		terminalBid, terminalAsk = bid, ask
	}
	if futurePoints == 0 || maxBid <= 0 || math.IsInf(minAsk, 1) {
		return marketMakerHorizonExposure{}, false
	}
	return marketMakerHorizonExposure{
		At:               start.At,
		EndAt:            endAt,
		StartBid:         startBid,
		StartAsk:         startAsk,
		TerminalBid:      terminalBid,
		TerminalAsk:      terminalAsk,
		BuyExcursionBps:  math.Log(startAsk/minAsk) * 10_000,
		SellExcursionBps: math.Log(maxBid/startBid) * 10_000,
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
