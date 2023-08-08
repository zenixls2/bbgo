package bybit

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/c9s/bbgo/pkg/fixedpoint"
	"github.com/c9s/bbgo/pkg/testutil"
	"github.com/c9s/bbgo/pkg/types"
)

func getTestClientOrSkip(t *testing.T) *Stream {
	if b, _ := strconv.ParseBool(os.Getenv("CI")); b {
		t.Skip("skip test for CI")
	}

	key, secret, ok := testutil.IntegrationTestConfigured(t, "BYBIT")
	if !ok {
		t.Skip("BYBIT_* env vars are not configured")
		return nil
	}

	return NewStream(key, secret)
}

func TestStream(t *testing.T) {
	s := getTestClientOrSkip(t)

	t.Run("Auth test", func(t *testing.T) {
		s.Connect(context.Background())
		c := make(chan struct{})
		<-c
	})

	t.Run("book test", func(t *testing.T) {
		s.Subscribe(types.BookChannel, "BTCUSDT", types.SubscribeOptions{
			Depth: types.DepthLevel50,
		})
		s.SetPublicOnly()
		err := s.Connect(context.Background())
		assert.NoError(t, err)

		s.OnBookSnapshot(func(book types.SliceOrderBook) {
			t.Log("got snapshot", book)
		})
		s.OnBookUpdate(func(book types.SliceOrderBook) {
			t.Log("got update", book)
		})
		c := make(chan struct{})
		<-c
	})

	t.Run("wallet test", func(t *testing.T) {
		err := s.Connect(context.Background())
		assert.NoError(t, err)

		s.OnBalanceSnapshot(func(balances types.BalanceMap) {
			t.Log("got snapshot", balances)
		})
		c := make(chan struct{})
		<-c
	})
}

func TestStream_parseWebSocketEvent(t *testing.T) {
	s := Stream{}

	t.Run("op", func(t *testing.T) {
		input := `{
		   "success":true,
		   "ret_msg":"subscribe",
		   "conn_id":"a403c8e5-e2b6-4edd-a8f0-1a64fa7227a5",
		   "op":"subscribe"
		}`
		res, err := s.parseWebSocketEvent([]byte(input))
		assert.NoError(t, err)
		opEvent, ok := res.(*WebSocketOpEvent)
		assert.True(t, ok)
		expSucceeds := true
		expRetMsg := "subscribe"
		assert.Equal(t, WebSocketOpEvent{
			Success: expSucceeds,
			RetMsg:  expRetMsg,
			ReqId:   "",
			ConnId:  "a403c8e5-e2b6-4edd-a8f0-1a64fa7227a5",
			Op:      WsOpTypeSubscribe,
			Args:    nil,
		}, *opEvent)
	})
	t.Run("TopicTypeOrderBook with delta", func(t *testing.T) {
		input := `{
			   "topic":"orderbook.50.BTCUSDT",
			   "ts":1691130685111,
			   "type":"delta",
			   "data":{
			      "s":"BTCUSDT",
			      "b":[

			      ],
			      "a":[
			         [
			            "29239.37",
			            "0.082356"
			         ],
			         [
			            "29236.1",
			            "0"
			         ]
			      ],
			      "u":1854104,
			      "seq":10559247733
			   }
			}`

		res, err := s.parseWebSocketEvent([]byte(input))
		assert.NoError(t, err)
		book, ok := res.(*BookEvent)
		assert.True(t, ok)
		assert.Equal(t, BookEvent{
			Symbol: "BTCUSDT",
			Bids:   nil,
			Asks: types.PriceVolumeSlice{
				{
					fixedpoint.NewFromFloat(29239.37),
					fixedpoint.NewFromFloat(0.082356),
				},
				{
					fixedpoint.NewFromFloat(29236.1),
					fixedpoint.NewFromFloat(0),
				},
			},
			UpdateId:   fixedpoint.NewFromFloat(1854104),
			SequenceId: fixedpoint.NewFromFloat(10559247733),
			Type:       DataTypeDelta,
		}, *book)
	})

	t.Run("Parse fails", func(t *testing.T) {
		input := `{
			   "topic":"orderbook.50.BTCUSDT",
			   "ts":1691130685111,
			   "type":"delta",
			   "data":{
					"GG": "test",
			   }
			}`

		res, err := s.parseWebSocketEvent([]byte(input))
		assert.Error(t, err)
		assert.Equal(t, nil, res)
	})
}

func Test_convertSubscription(t *testing.T) {
	t.Run("BookChannel.DepthLevel1", func(t *testing.T) {
		res, err := convertSubscription(types.Subscription{
			Symbol:  "BTCUSDT",
			Channel: types.BookChannel,
			Options: types.SubscribeOptions{
				Depth: types.DepthLevel1,
			},
		})
		assert.NoError(t, err)
		assert.Equal(t, genTopic(TopicTypeOrderBook, types.DepthLevel1, "BTCUSDT"), res)
	})
	t.Run("BookChannel. with default depth", func(t *testing.T) {
		res, err := convertSubscription(types.Subscription{
			Symbol:  "BTCUSDT",
			Channel: types.BookChannel,
		})
		assert.NoError(t, err)
		assert.Equal(t, genTopic(TopicTypeOrderBook, types.DepthLevel1, "BTCUSDT"), res)
	})
	t.Run("BookChannel.DepthLevel50", func(t *testing.T) {
		res, err := convertSubscription(types.Subscription{
			Symbol:  "BTCUSDT",
			Channel: types.BookChannel,
			Options: types.SubscribeOptions{
				Depth: types.DepthLevel50,
			},
		})
		assert.NoError(t, err)
		assert.Equal(t, genTopic(TopicTypeOrderBook, types.DepthLevel50, "BTCUSDT"), res)
	})
	t.Run("BookChannel. not support depth, use default level 1", func(t *testing.T) {
		res, err := convertSubscription(types.Subscription{
			Symbol:  "BTCUSDT",
			Channel: types.BookChannel,
			Options: types.SubscribeOptions{
				Depth: "20",
			},
		})
		assert.NoError(t, err)
		assert.Equal(t, genTopic(TopicTypeOrderBook, types.DepthLevel1, "BTCUSDT"), res)
	})

	t.Run("unsupported channel", func(t *testing.T) {
		res, err := convertSubscription(types.Subscription{
			Symbol:  "BTCUSDT",
			Channel: "unsupported",
		})
		assert.Equal(t, fmt.Errorf("unsupported stream channel: %s", "unsupported"), err)
		assert.Equal(t, "", res)
	})
}