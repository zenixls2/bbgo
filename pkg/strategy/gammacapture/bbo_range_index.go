package gammacapture

import (
	"math"
	"sort"
	"time"
)

const marketMakerBBOBlockSize = 64

type marketMakerBBORangePoint struct {
	at       time.Time
	bid, ask float64
	bad      bool
}

type marketMakerBBORangeBlock struct {
	minimumAsk float64
	maximumBid float64
	badCount   int
}

// marketMakerBBORangeIndex answers exact first-passage queries over executable
// BBO history. It owns append-only blocks and advances a logical head when the
// horizon model trims its slice. Thus a new BBO costs O(1) amortized instead of
// rebuilding an O(lookback) segment tree at every five-minute model update.
// Queries scan at most two partial 64-point blocks plus block extrema.
type marketMakerBBORangeIndex struct {
	points []marketMakerBBORangePoint
	blocks []marketMakerBBORangeBlock
	head   int
}

func (m *MarketMakerHorizonModel) executableBBORangeIndex() *marketMakerBBORangeIndex {
	if m == nil || len(m.points) == 0 {
		return nil
	}
	if m.bboRangeIndex == nil {
		m.bboRangeIndex = &marketMakerBBORangeIndex{}
	}
	index := m.bboRangeIndex
	if index.pointCount() == len(m.points) && index.head < len(index.points) {
		first, last := m.points[0], m.points[len(m.points)-1]
		indexedLast := index.points[len(index.points)-1]
		lastBid, lastAsk := last.bidPrice(), last.askPrice()
		lastBad := last.GapBefore || lastBid <= 0 || lastAsk < lastBid
		if index.points[index.head].at.Equal(first.At) && indexedLast.at.Equal(last.At) &&
			indexedLast.bid == lastBid && indexedLast.ask == lastAsk && indexedLast.bad == lastBad {
			return index
		}
	}
	if !index.sync(m.points) {
		return nil
	}
	return index
}

func (i *marketMakerBBORangeIndex) sync(points []MarketMakerHorizonPoint) bool {
	if i == nil || len(points) == 0 {
		return false
	}
	if len(i.points) == 0 {
		i.rebuild(points)
		return true
	}

	firstAt := points[0].At
	active := i.points[i.head:]
	first := sort.Search(len(active), func(index int) bool {
		return !active[index].at.Before(firstAt)
	})
	if first >= len(active) || !active[first].at.Equal(firstAt) {
		i.rebuild(points)
		return true
	}
	i.head += first
	active = i.points[i.head:]
	lastAt := active[len(active)-1].at
	if points[len(points)-1].At.Before(lastAt) {
		i.rebuild(points)
		return true
	}

	start := sort.Search(len(points), func(index int) bool {
		return !points[index].At.Before(lastAt)
	})
	if start >= len(points) || !points[start].At.Equal(lastAt) {
		i.rebuild(points)
		return true
	}
	// The current second is mutable until the following sampled second. Replace
	// it in place, then append only genuinely new immutable points.
	i.points[len(i.points)-1] = rangePoint(points[start])
	i.recomputeBlock((len(i.points) - 1) / marketMakerBBOBlockSize)
	for pointIndex := start + 1; pointIndex < len(points); pointIndex++ {
		i.append(rangePoint(points[pointIndex]))
	}

	// Compact only after a large fraction has expired. Normal minute-by-minute
	// retention advances head without copying or rebuilding historical blocks.
	if i.head >= 65_536 && i.head*2 >= len(i.points) {
		retained := append([]marketMakerBBORangePoint(nil), i.points[i.head:]...)
		i.points, i.head = retained, 0
		i.rebuildBlocks()
	}
	return i.pointCount() == len(points) && i.points[i.head].at.Equal(points[0].At)
}

func rangePoint(point MarketMakerHorizonPoint) marketMakerBBORangePoint {
	bid, ask := point.bidPrice(), point.askPrice()
	return marketMakerBBORangePoint{
		at: point.At, bid: bid, ask: ask,
		bad: point.GapBefore || bid <= 0 || ask < bid,
	}
}

func (i *marketMakerBBORangeIndex) rebuild(points []MarketMakerHorizonPoint) {
	i.points = make([]marketMakerBBORangePoint, len(points))
	i.head = 0
	for index := range points {
		i.points[index] = rangePoint(points[index])
	}
	i.rebuildBlocks()
}

func (i *marketMakerBBORangeIndex) rebuildBlocks() {
	count := (len(i.points) + marketMakerBBOBlockSize - 1) / marketMakerBBOBlockSize
	i.blocks = make([]marketMakerBBORangeBlock, count)
	for block := range i.blocks {
		i.recomputeBlock(block)
	}
}

func (i *marketMakerBBORangeIndex) append(point marketMakerBBORangePoint) {
	index := len(i.points)
	i.points = append(i.points, point)
	block := index / marketMakerBBOBlockSize
	if block == len(i.blocks) {
		i.blocks = append(i.blocks, marketMakerBBORangeBlock{
			minimumAsk: math.Inf(1), maximumBid: math.Inf(-1),
		})
	}
	b := &i.blocks[block]
	if point.bad {
		b.badCount++
		return
	}
	b.minimumAsk = math.Min(b.minimumAsk, point.ask)
	b.maximumBid = math.Max(b.maximumBid, point.bid)
}

func (i *marketMakerBBORangeIndex) recomputeBlock(block int) {
	if block < 0 || block >= len(i.blocks) {
		return
	}
	left := block * marketMakerBBOBlockSize
	right := left + marketMakerBBOBlockSize
	if right > len(i.points) {
		right = len(i.points)
	}
	b := marketMakerBBORangeBlock{minimumAsk: math.Inf(1), maximumBid: math.Inf(-1)}
	for index := left; index < right; index++ {
		point := i.points[index]
		if point.bad {
			b.badCount++
			continue
		}
		b.minimumAsk = math.Min(b.minimumAsk, point.ask)
		b.maximumBid = math.Max(b.maximumBid, point.bid)
	}
	i.blocks[block] = b
}

func (i *marketMakerBBORangeIndex) pointCount() int {
	if i == nil {
		return 0
	}
	return len(i.points) - i.head
}

func (i *marketMakerBBORangeIndex) bounds(left, right int) (int, int, bool) {
	if i == nil || left < 0 || left >= right || right > i.pointCount() {
		return 0, 0, false
	}
	return i.head + left, i.head + right, true
}

func (i *marketMakerBBORangeIndex) validRange(left, right int) bool {
	left, right, ok := i.bounds(left, right)
	if !ok {
		return false
	}
	for left < right && left%marketMakerBBOBlockSize != 0 {
		if i.points[left].bad {
			return false
		}
		left++
	}
	for left+marketMakerBBOBlockSize <= right {
		if i.blocks[left/marketMakerBBOBlockSize].badCount > 0 {
			return false
		}
		left += marketMakerBBOBlockSize
	}
	for left < right {
		if i.points[left].bad {
			return false
		}
		left++
	}
	return true
}

func (i *marketMakerBBORangeIndex) firstAskAtOrBelow(left, right int, quote float64) int {
	base := i.head
	left, right, ok := i.bounds(left, right)
	if !ok || quote <= 0 {
		return -1
	}
	for left < right && left%marketMakerBBOBlockSize != 0 {
		if point := i.points[left]; !point.bad && point.ask <= quote {
			return left - base
		}
		left++
	}
	for left+marketMakerBBOBlockSize <= right {
		block := i.blocks[left/marketMakerBBOBlockSize]
		if block.minimumAsk <= quote {
			blockRight := left + marketMakerBBOBlockSize
			for left < blockRight {
				if point := i.points[left]; !point.bad && point.ask <= quote {
					return left - base
				}
				left++
			}
			continue
		}
		left += marketMakerBBOBlockSize
	}
	for left < right {
		if point := i.points[left]; !point.bad && point.ask <= quote {
			return left - base
		}
		left++
	}
	return -1
}

func (i *marketMakerBBORangeIndex) firstBidAtOrAbove(left, right int, quote float64) int {
	base := i.head
	left, right, ok := i.bounds(left, right)
	if !ok || quote <= 0 {
		return -1
	}
	for left < right && left%marketMakerBBOBlockSize != 0 {
		if point := i.points[left]; !point.bad && point.bid >= quote {
			return left - base
		}
		left++
	}
	for left+marketMakerBBOBlockSize <= right {
		block := i.blocks[left/marketMakerBBOBlockSize]
		if block.maximumBid >= quote {
			blockRight := left + marketMakerBBOBlockSize
			for left < blockRight {
				if point := i.points[left]; !point.bad && point.bid >= quote {
					return left - base
				}
				left++
			}
			continue
		}
		left += marketMakerBBOBlockSize
	}
	for left < right {
		if point := i.points[left]; !point.bad && point.bid >= quote {
			return left - base
		}
		left++
	}
	return -1
}

func pointIndexAtOrAfter(points []MarketMakerHorizonPoint, at time.Time) int {
	return sort.Search(len(points), func(index int) bool {
		return !points[index].At.Before(at)
	})
}
