package main

import (
	"bufio"
	"encoding/json"
	"io"
	"math"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/c9s/bbgo/pkg/strategy/gammacapture"
	"github.com/c9s/bbgo/pkg/types"
)

const (
	minimumLifecycleFills                = 30
	minimumLifecycleSideFills            = 10
	legacyBinanceBrokerClientOrderPrefix = "x-NSUYEBKM"
)

type journalMakerOrder struct {
	OrderID       uint64         `json:"orderID"`
	ClientOrderID string         `json:"clientOrderID"`
	Side          types.SideType `json:"side"`
	Price         float64        `json:"price"`
	Quantity      float64        `json:"quantity"`
	CreatedAt     time.Time      `json:"createdAt"`
	EndedAt       time.Time      `json:"endedAt"`
	FilledAt      time.Time      `json:"filledAt,omitempty"`
	Filled        bool           `json:"filled"`
	EndReason     string         `json:"endReason"`
}

type lifecycleQueueCandidate struct {
	QueueMultiple       float64 `json:"queueMultipleOfOrderQuantity"`
	PredictedBuyFills   int     `json:"predictedBuyFills"`
	PredictedSellFills  int     `json:"predictedSellFills"`
	TruePositive        int     `json:"truePositive"`
	FalsePositive       int     `json:"falsePositive"`
	FalseNegative       int     `json:"falseNegative"`
	TrueNegative        int     `json:"trueNegative"`
	Precision           float64 `json:"precision"`
	Recall              float64 `json:"recall"`
	F1                  float64 `json:"f1"`
	DirectionalAbsError int     `json:"directionalAbsoluteError"`
}

type lifecycleOrderDiagnostic struct {
	OrderID             uint64         `json:"orderID"`
	Side                types.SideType `json:"side"`
	Price               float64        `json:"price"`
	Quantity            float64        `json:"quantity"`
	CreatedAt           time.Time      `json:"createdAt"`
	EndedAt             time.Time      `json:"endedAt"`
	EndReason           string         `json:"endReason"`
	ActualFilled        bool           `json:"actualFilled"`
	PredictedFilled     bool           `json:"predictedFilled"`
	PublicCrossVolume   float64        `json:"publicCrossVolume"`
	CrossVolumeQtyRatio float64        `json:"crossVolumeQuantityRatio"`
	FirstCrossAt        time.Time      `json:"firstCrossAt,omitempty"`
}

type orderLifecycleReport struct {
	JournalPath                 string                     `json:"journalPath"`
	MakerOrders                 int                        `json:"makerOrders"`
	MakerBuyOrders              int                        `json:"makerBuyOrders"`
	MakerSellOrders             int                        `json:"makerSellOrders"`
	ActualMakerFills            int                        `json:"actualMakerFills"`
	ActualMakerBuyFills         int                        `json:"actualMakerBuyFills"`
	ActualMakerSellFills        int                        `json:"actualMakerSellFills"`
	ExcludedImmediateOrNonMaker int                        `json:"excludedImmediateOrNonMakerOrders"`
	MeanOrderLifeSeconds        float64                    `json:"meanOrderLifeSeconds"`
	ZeroQueueTouchedOrders      int                        `json:"zeroQueueTouchedOrders"`
	SelectedQueueMultiple       float64                    `json:"selectedQueueMultipleOfOrderQuantity"`
	Selected                    lifecycleQueueCandidate    `json:"selected"`
	Candidates                  []lifecycleQueueCandidate  `json:"candidates"`
	OrderDiagnostics            []lifecycleOrderDiagnostic `json:"actualOrPredictedFillOrders"`
	CountMatched                bool                       `json:"countMatched"`
	StatisticallySufficient     bool                       `json:"statisticallySufficient"`
	CalibrationPassed           bool                       `json:"calibrationPassed"`
	Decision                    string                     `json:"decision"`
	Limitations                 []string                   `json:"limitations"`
}

type lifecycleJournalEvent struct {
	at       time.Time
	kind     int
	order    *journalMakerOrder
	orderID  uint64
	excluded bool
}

var (
	journalLogTimeRE    = regexp.MustCompile(`time="([^"]+)"`)
	journalFillRE       = regexp.MustCompile(`\[ActiveOrderBook\] order #(\d+) is filled:`)
	journalTradeOrderRE = regexp.MustCompile(`order_id=(\d+)`)
	journalFieldREs     = map[string]*regexp.Regexp{
		"symbol":   regexp.MustCompile(`Symbol:([^ ]+)`),
		"orderID":  regexp.MustCompile(`OrderID:(\d+)`),
		"clientID": regexp.MustCompile(`ClientOrderID:([^ ]+)`),
		"tx":       regexp.MustCompile(`TransactTime:(\d+)`),
		"price":    regexp.MustCompile(`Price:([0-9.]+)`),
		"qty":      regexp.MustCompile(`OrigQuantity:([0-9.]+)`),
		"status":   regexp.MustCompile(`Status:([^ ]+)`),
		"tif":      regexp.MustCompile(`TimeInForce:([^ ]+)`),
		"type":     regexp.MustCompile(`Type:([^ ]+)`),
		"side":     regexp.MustCompile(`Side:(BUY|SELL)`),
	}
)

func buildOrderLifecycleReport(path, symbol string, from, to time.Time, trades []tick) *orderLifecycleReport {
	orders, excluded := readJournalMakerOrders(path, symbol, from, to)
	report := &orderLifecycleReport{
		JournalPath:                 path,
		MakerOrders:                 len(orders),
		ExcludedImmediateOrNonMaker: excluded,
		Limitations: []string{
			"journal fill receipt time is used because the active-order update does not preserve the exchange fill timestamp",
			"aggregate trades reveal volume through the order price but not same-price depth, cancellations, or private queue priority",
			"effective queue is expressed as a multiple of order quantity; it is a calibrated proxy, not observed L2 depth",
		},
	}
	var lifeSeconds float64
	for _, order := range orders {
		if order.Side == types.SideTypeBuy {
			report.MakerBuyOrders++
		} else {
			report.MakerSellOrders++
		}
		if order.Filled {
			report.ActualMakerFills++
			if order.Side == types.SideTypeBuy {
				report.ActualMakerBuyFills++
			} else {
				report.ActualMakerSellFills++
			}
		}
		if order.EndedAt.After(order.CreatedAt) {
			lifeSeconds += order.EndedAt.Sub(order.CreatedAt).Seconds()
		}
	}
	if len(orders) > 0 {
		report.MeanOrderLifeSeconds = lifeSeconds / float64(len(orders))
	}
	queueGrid := []float64{0, .25, .5, 1, 2, 4, 8, 16, 32}
	best := -1
	bestF1 := -1.0
	bestError := math.MaxInt
	for _, queue := range queueGrid {
		candidate := evaluateJournalOrders(orders, trades, queue)
		report.Candidates = append(report.Candidates, candidate)
		if queue == 0 {
			report.ZeroQueueTouchedOrders = candidate.PredictedBuyFills + candidate.PredictedSellFills
		}
		if candidate.F1 > bestF1 || (candidate.F1 == bestF1 && candidate.DirectionalAbsError < bestError) {
			best, bestF1, bestError = len(report.Candidates)-1, candidate.F1, candidate.DirectionalAbsError
		}
	}
	if best >= 0 {
		report.Selected = report.Candidates[best]
		report.SelectedQueueMultiple = report.Selected.QueueMultiple
	}
	for _, order := range orders {
		volume, first := journalOrderCrossingVolume(order, trades)
		predicted := volume+1e-12 >= order.Quantity*(1+report.SelectedQueueMultiple)
		if !order.Filled && !predicted {
			continue
		}
		ratio := 0.0
		if order.Quantity > 0 {
			ratio = volume / order.Quantity
		}
		report.OrderDiagnostics = append(report.OrderDiagnostics, lifecycleOrderDiagnostic{
			OrderID: order.OrderID, Side: order.Side, Price: order.Price, Quantity: order.Quantity,
			CreatedAt: order.CreatedAt, EndedAt: order.EndedAt, EndReason: order.EndReason,
			ActualFilled: order.Filled, PredictedFilled: predicted, PublicCrossVolume: volume,
			CrossVolumeQtyRatio: ratio, FirstCrossAt: first,
		})
	}
	report.CountMatched = report.Selected.DirectionalAbsError <= 1
	report.StatisticallySufficient = report.ActualMakerFills >= minimumLifecycleFills &&
		report.ActualMakerBuyFills >= minimumLifecycleSideFills &&
		report.ActualMakerSellFills >= minimumLifecycleSideFills
	report.CalibrationPassed = report.CountMatched && report.StatisticallySufficient &&
		report.Selected.Precision >= .7 && report.Selected.Recall >= .7
	if !report.StatisticallySufficient {
		report.Decision = "maker lifecycle sample is statistically insufficient; collect at least 30 fills including 10 per side before policy promotion"
	} else if !report.CalibrationPassed {
		report.Decision = "maker lifecycle queue proxy failed calibration; do not promote or tune live policy"
	} else {
		report.Decision = "maker lifecycle queue proxy passed the minimum calibration gate"
	}
	return report
}

func readJournalMakerOrders(path, symbol string, from, to time.Time) ([]*journalMakerOrder, int) {
	if path != "-" && isPrivateOrderFillLedgerFile(path) {
		return readPrivateOrderFillLedgerMakerOrders(path, symbol, from, to)
	}
	var source io.Reader
	var file *os.File
	if path == "-" {
		source = os.Stdin
	} else {
		var err error
		file, err = os.Open(path)
		if err != nil {
			fatalf("open journal lifecycle data: %v", err)
		}
		defer file.Close()
		source = file
	}
	var events []lifecycleJournalEvent
	tradeFills := make(map[uint64]lifecycleJournalEvent)
	activeBookFills := make(map[uint64]lifecycleJournalEvent)
	excluded := 0
	scanner := bufio.NewScanner(source)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var row map[string]json.RawMessage
		if json.Unmarshal(scanner.Bytes(), &row) != nil {
			continue
		}
		message := journalMessage(row["MESSAGE"])
		if message == "" {
			continue
		}
		at := journalMessageTime(message, row["__REALTIME_TIMESTAMP"])
		if strings.Contains(message, "spot order creation response:") {
			order, isMaker, ok := parseJournalCreation(message, symbol)
			if !ok || order.CreatedAt.Before(from) || !order.CreatedAt.Before(to) {
				continue
			}
			if !isMaker {
				excluded++
				// Inventory-reset IOC orders are submitted only after
				// executeInventoryReset calls GracefulCancel on maker quotes.
				events = append(events, lifecycleJournalEvent{at: order.CreatedAt, kind: 0})
				continue
			}
			events = append(events, lifecycleJournalEvent{at: order.CreatedAt, kind: 1, order: order})
			continue
		}
		if strings.Contains(message, `msg="TRADE`) && strings.Contains(message, "liquidity=MAKER") {
			if match := journalTradeOrderRE.FindStringSubmatch(message); len(match) == 2 {
				id, _ := strconv.ParseUint(match[1], 10, 64)
				tradeAt := journalLastMessageTime(message)
				if !tradeAt.Before(from) && tradeAt.Before(to) {
					tradeFills[id] = lifecycleJournalEvent{at: tradeAt, kind: 2, orderID: id}
				}
			}
			continue
		}
		if match := journalFillRE.FindStringSubmatch(message); len(match) == 2 {
			id, _ := strconv.ParseUint(match[1], 10, 64)
			if !at.Before(from) && at.Before(to) {
				activeBookFills[id] = lifecycleJournalEvent{at: at, kind: 2, orderID: id}
			}
			continue
		}
		if (!at.Before(from) && at.Before(to)) && isJournalCloseBoundary(message) {
			events = append(events, lifecycleJournalEvent{at: at, kind: 0})
		}
	}
	if err := scanner.Err(); err != nil {
		fatalf("scan journal lifecycle data: %v", err)
	}
	for id, event := range activeBookFills {
		if _, ok := tradeFills[id]; !ok {
			tradeFills[id] = event
		}
	}
	for _, event := range tradeFills {
		events = append(events, event)
	}
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].at.Equal(events[j].at) {
			return events[i].kind < events[j].kind
		}
		return events[i].at.Before(events[j].at)
	})
	byID := make(map[uint64]*journalMakerOrder)
	active := make(map[uint64]*journalMakerOrder)
	activeSide := make(map[types.SideType]*journalMakerOrder)
	closeOrder := func(order *journalMakerOrder, at time.Time, reason string) {
		if order == nil || !order.EndedAt.IsZero() || at.Before(order.CreatedAt) {
			return
		}
		order.EndedAt, order.EndReason = at, reason
		delete(active, order.OrderID)
		if activeSide[order.Side] == order {
			delete(activeSide, order.Side)
		}
	}
	for _, event := range events {
		switch event.kind {
		case 0:
			for _, order := range active {
				closeOrder(order, event.at, "quote-refresh-or-reconciliation")
			}
		case 1:
			if previous := activeSide[event.order.Side]; previous != nil {
				closeOrder(previous, event.at, "same-side-replacement")
			}
			byID[event.order.OrderID] = event.order
			active[event.order.OrderID] = event.order
			activeSide[event.order.Side] = event.order
		case 2:
			if order := byID[event.orderID]; order != nil {
				order.Filled, order.FilledAt = true, event.at
				closeOrder(order, event.at, "filled")
			}
		}
	}
	for _, order := range active {
		closeOrder(order, to, "end-of-observation")
	}
	orders := make([]*journalMakerOrder, 0, len(byID))
	for _, order := range byID {
		if order.EndedAt.IsZero() {
			order.EndedAt, order.EndReason = to, "end-of-observation"
		}
		orders = append(orders, order)
	}
	sort.Slice(orders, func(i, j int) bool { return orders[i].CreatedAt.Before(orders[j].CreatedAt) })
	return orders, excluded
}

func isPrivateOrderFillLedgerFile(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == "" {
			continue
		}
		var probe struct {
			EventType string `json:"eventType"`
		}
		return json.Unmarshal(scanner.Bytes(), &probe) == nil && probe.EventType != ""
	}
	return false
}

func readPrivateOrderFillLedgerMakerOrders(path, symbol string, from, to time.Time) ([]*journalMakerOrder, int) {
	file, err := os.Open(path)
	if err != nil {
		fatalf("open private order/fill ledger: %v", err)
	}
	defer file.Close()

	type ledgerOrderState struct {
		order         *journalMakerOrder
		lastExecuted  float64
		pendingCancel string
	}
	states := make(map[uint64]*ledgerOrderState)
	var events []gammacapture.PrivateOrderFillLedgerEvent
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == "" {
			continue
		}
		var event gammacapture.PrivateOrderFillLedgerEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		if event.Symbol != symbol || event.OrderID == 0 {
			continue
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		fatalf("scan private order/fill ledger: %v", err)
	}
	sort.SliceStable(events, func(i, j int) bool {
		return events[i].Sequence < events[j].Sequence
	})

	isMaker := func(event gammacapture.PrivateOrderFillLedgerEvent) bool {
		if !strings.HasPrefix(event.ClientOrderID, "gcmm-") && !strings.HasPrefix(event.ClientOrderID, legacyBinanceBrokerClientOrderPrefix) {
			return false
		}
		return event.OrderType == types.OrderTypeLimitMaker ||
			(event.OrderType == types.OrderTypeLimit && event.TimeInForce == types.TimeInForceGTC)
	}
	orderAt := func(event gammacapture.PrivateOrderFillLedgerEvent) time.Time {
		if !event.OrderCreatedAt.IsZero() {
			return event.OrderCreatedAt
		}
		return event.ObservedAt
	}
	eventAt := func(event gammacapture.PrivateOrderFillLedgerEvent) time.Time {
		if !event.ExchangeAt.IsZero() {
			return event.ExchangeAt
		}
		return event.ObservedAt
	}
	statusReason := func(status types.OrderStatus) string {
		switch status {
		case types.OrderStatusFilled:
			return "filled"
		case types.OrderStatusCanceled:
			return "canceled"
		case types.OrderStatusRejected:
			return "rejected"
		case types.OrderStatusExpired:
			return "expired"
		case types.OrderStatusFinished:
			return "finished"
		default:
			return "closed"
		}
	}
	ensure := func(event gammacapture.PrivateOrderFillLedgerEvent) *ledgerOrderState {
		state := states[event.OrderID]
		if state != nil {
			return state
		}
		createdAt := orderAt(event)
		if createdAt.IsZero() || !createdAt.Before(to) || createdAt.Before(from) || !isMaker(event) {
			return nil
		}
		state = &ledgerOrderState{order: &journalMakerOrder{
			OrderID: event.OrderID, ClientOrderID: event.ClientOrderID,
			Side: event.Side, Price: event.Price.Float64(), Quantity: event.Quantity.Float64(),
			CreatedAt: createdAt,
		}}
		states[event.OrderID] = state
		return state
	}
	for _, event := range events {
		state := ensure(event)
		if state == nil {
			continue
		}
		if event.Price.Sign() > 0 {
			state.order.Price = event.Price.Float64()
		}
		if event.Quantity.Sign() > 0 {
			state.order.Quantity = event.Quantity.Float64()
		}
		if event.ExecutedQuantity.Float64() > state.lastExecuted {
			state.lastExecuted = event.ExecutedQuantity.Float64()
			state.order.Filled = true
			state.order.FilledAt = eventAt(event)
		}
		switch event.EventType {
		case gammacapture.PrivateLedgerEventFill:
			state.order.Filled = true
			state.order.FilledAt = eventAt(event)
		case gammacapture.PrivateLedgerEventCancelRequest:
			state.pendingCancel = event.CancelReason
		case gammacapture.PrivateLedgerEventCancelResult:
			if event.CancelAccepted && state.order.EndedAt.IsZero() {
				state.order.EndedAt = eventAt(event)
				state.order.EndReason = "cancel-request:" + event.CancelReason
			}
		case gammacapture.PrivateLedgerEventOrderUpdate:
			if event.Status.Closed() && state.order.EndedAt.IsZero() {
				state.order.EndedAt = eventAt(event)
				if event.CancelReason != "" {
					state.order.EndReason = "cancel-request:" + event.CancelReason
				} else {
					state.order.EndReason = statusReason(event.Status)
				}
			}
		}
	}
	orders := make([]*journalMakerOrder, 0, len(states))
	for _, state := range states {
		if state.order.EndedAt.IsZero() {
			state.order.EndedAt = to
			state.order.EndReason = "end-of-observation"
		}
		orders = append(orders, state.order)
	}
	sort.Slice(orders, func(i, j int) bool { return orders[i].CreatedAt.Before(orders[j].CreatedAt) })
	return orders, 0
}

func parseJournalCreation(message, symbol string) (*journalMakerOrder, bool, bool) {
	fields := make(map[string]string, len(journalFieldREs))
	for name, expression := range journalFieldREs {
		match := expression.FindStringSubmatch(message)
		if len(match) != 2 {
			return nil, false, false
		}
		fields[name] = match[1]
	}
	if fields["symbol"] != symbol {
		return nil, false, false
	}
	id, errID := strconv.ParseUint(fields["orderID"], 10, 64)
	tx, errTX := strconv.ParseInt(fields["tx"], 10, 64)
	price, errPrice := strconv.ParseFloat(fields["price"], 64)
	qty, errQty := strconv.ParseFloat(fields["qty"], 64)
	side, errSide := types.StrToSideType(fields["side"])
	if errID != nil || errTX != nil || errPrice != nil || errQty != nil || errSide != nil || price <= 0 || qty <= 0 {
		return nil, false, false
	}
	maker := strings.HasPrefix(fields["clientID"], "gcmm-") && fields["type"] == "LIMIT_MAKER" &&
		fields["tif"] == "GTC" && fields["status"] == "NEW"
	return &journalMakerOrder{OrderID: id, ClientOrderID: fields["clientID"], Side: side, Price: price, Quantity: qty, CreatedAt: time.UnixMilli(tx)}, maker, true
}

func evaluateJournalOrders(orders []*journalMakerOrder, trades []tick, queueMultiple float64) lifecycleQueueCandidate {
	out := lifecycleQueueCandidate{QueueMultiple: queueMultiple}
	for _, order := range orders {
		volume, _ := journalOrderCrossingVolume(order, trades)
		required := order.Quantity * (1 + queueMultiple)
		predicted := volume+1e-12 >= required
		if predicted {
			if order.Side == types.SideTypeBuy {
				out.PredictedBuyFills++
			} else {
				out.PredictedSellFills++
			}
		}
		switch {
		case predicted && order.Filled:
			out.TruePositive++
		case predicted && !order.Filled:
			out.FalsePositive++
		case !predicted && order.Filled:
			out.FalseNegative++
		default:
			out.TrueNegative++
		}
	}
	if denominator := out.TruePositive + out.FalsePositive; denominator > 0 {
		out.Precision = float64(out.TruePositive) / float64(denominator)
	}
	if denominator := out.TruePositive + out.FalseNegative; denominator > 0 {
		out.Recall = float64(out.TruePositive) / float64(denominator)
	}
	if out.Precision+out.Recall > 0 {
		out.F1 = 2 * out.Precision * out.Recall / (out.Precision + out.Recall)
	}
	actualBuy, actualSell := 0, 0
	for _, order := range orders {
		if !order.Filled {
			continue
		}
		if order.Side == types.SideTypeBuy {
			actualBuy++
		} else {
			actualSell++
		}
	}
	out.DirectionalAbsError = absInt(out.PredictedBuyFills-actualBuy) + absInt(out.PredictedSellFills-actualSell)
	return out
}

func journalOrderCrossingVolume(order *journalMakerOrder, trades []tick) (float64, time.Time) {
	begin := sort.Search(len(trades), func(i int) bool { return !trades[i].time.Before(order.CreatedAt) })
	volume := 0.0
	var first time.Time
	for i := begin; i < len(trades) && !trades[i].time.After(order.EndedAt); i++ {
		trade := trades[i]
		crosses := order.Side == types.SideTypeBuy && trade.side == types.SideTypeSell && trade.price <= order.Price
		crosses = crosses || order.Side == types.SideTypeSell && trade.side == types.SideTypeBuy && trade.price >= order.Price
		if !crosses {
			continue
		}
		if first.IsZero() {
			first = trade.time
		}
		volume += trade.size
	}
	return volume, first
}

func journalMessage(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var values []string
	if json.Unmarshal(raw, &values) == nil {
		return strings.Join(values, " ")
	}
	return ""
}

func journalMessageTime(message string, raw json.RawMessage) time.Time {
	if match := journalLogTimeRE.FindStringSubmatch(message); len(match) == 2 {
		if parsed, err := time.Parse(time.RFC3339Nano, match[1]); err == nil {
			return parsed
		}
	}
	var microsText string
	if json.Unmarshal(raw, &microsText) == nil {
		if micros, err := strconv.ParseInt(microsText, 10, 64); err == nil {
			return time.UnixMicro(micros)
		}
	}
	return time.Time{}
}

func journalLastMessageTime(message string) time.Time {
	matches := journalLogTimeRE.FindAllStringSubmatch(message, -1)
	for i := len(matches) - 1; i >= 0; i-- {
		if parsed, err := time.Parse(time.RFC3339Nano, matches[i][1]); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

func isJournalCloseBoundary(message string) bool {
	return strings.Contains(message, `msg="market-maker quote evaluation"`) ||
		strings.Contains(message, `msg="market-maker cancelling stale startup orders"`) ||
		strings.Contains(message, `msg="market-maker stopping"`)
}
