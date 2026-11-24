package market

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
)

const hlVenue = "hyperliquid"

// Hyperliquid 接入 Hyperliquid 永续（链上订单簿）。每个币订阅三路：
//   - l2Book：盘口前 20 档（模拟成交、频谱）
//   - trades：逐笔成交（带买卖方向）
//   - activeAssetCtx：标记价格、资金费率（每小时结算）、持仓量、24 小时成交额
//
// 文档：https://hyperliquid.gitbook.io/hyperliquid-docs/for-developers/api/websocket
type Hyperliquid struct {
	Coins []string // 如 BTC、ETH
	URL   string   // 默认 wss://api.hyperliquid.xyz/ws
	Log   *slog.Logger
}

func (h *Hyperliquid) Name() string { return hlVenue }

func (h *Hyperliquid) url() string {
	if h.URL != "" {
		return h.URL
	}
	return "wss://api.hyperliquid.xyz/ws"
}

func (h *Hyperliquid) Run(ctx context.Context, sink Sink) {
	wait := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		err := h.session(ctx, sink)
		sink.UpdateStatus(func(s *SourceStatus) {
			s.Connections[hlVenue] = false
			s.Reconnects++
			if err != nil {
				s.LastError = "hyperliquid: " + err.Error()
			}
		})
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) > time.Minute {
			wait = time.Second
		}
		if h.Log != nil {
			h.Log.Warn("hyperliquid reconnect", "err", err, "wait", wait)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(wait*2, 30*time.Second)
	}
}

func (h *Hyperliquid) session(ctx context.Context, sink Sink) error {
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	c, _, err := websocket.Dial(dctx, h.url(), nil)
	cancel()
	if err != nil {
		return err
	}
	defer c.CloseNow()
	c.SetReadLimit(4 << 20)
	for _, coin := range h.Coins {
		for _, typ := range []string{"l2Book", "trades", "activeAssetCtx"} {
			msg, _ := json.Marshal(map[string]any{"method": "subscribe", "subscription": map[string]string{"type": typ, "coin": coin}})
			if err := c.Write(ctx, websocket.MessageText, msg); err != nil {
				return err
			}
		}
	}
	sink.UpdateStatus(func(s *SourceStatus) { s.Connections[hlVenue] = true })
	// Hyperliquid 60 秒没有消息会断开，每 30 秒发一次 ping
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if c.Write(ctx, websocket.MessageText, []byte(`{"method":"ping"}`)) != nil {
					return
				}
			}
		}
	}()
	for {
		rctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		_, data, err := c.Read(rctx)
		cancel()
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return errors.New("90 秒没有消息")
			}
			return err
		}
		evs := ParseHyperliquid(data)
		if len(evs) > 0 {
			now := time.Now().UnixMilli()
			sink.UpdateStatus(func(s *SourceStatus) { s.LastMessageAt = now })
		}
		for _, ev := range evs {
			sink.Publish(ev)
		}
	}
}

// ParseHyperliquid 解析一条推送，可能产生多个事件（trades 是数组）。
func ParseHyperliquid(raw []byte) []Event {
	var msg struct {
		Channel string          `json:"channel"`
		Data    json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &msg) != nil {
		return nil
	}
	inst := func(coin string) string { return strings.ToUpper(coin) + "-PERP" }
	switch msg.Channel {
	case "l2Book":
		var b struct {
			Coin   string `json:"coin"`
			Time   int64  `json:"time"`
			Levels [2][]struct {
				Px string `json:"px"`
				Sz string `json:"sz"`
			} `json:"levels"`
		}
		if json.Unmarshal(msg.Data, &b) != nil || b.Coin == "" {
			return nil
		}
		ev := Event{Kind: KindDepth, Instrument: inst(b.Coin), Symbol: b.Coin, Venue: hlVenue, Time: b.Time}
		for _, l := range b.Levels[0] {
			ev.Bids = append(ev.Bids, [2]string{l.Px, l.Sz})
		}
		for _, l := range b.Levels[1] {
			ev.Asks = append(ev.Asks, [2]string{l.Px, l.Sz})
		}
		out := []Event{ev}
		if len(ev.Bids) > 0 && len(ev.Asks) > 0 {
			out = append(out, Event{Kind: KindBook, Instrument: ev.Instrument, Symbol: b.Coin, Venue: hlVenue, Time: b.Time,
				Bid: ev.Bids[0][0], BidQty: ev.Bids[0][1], Ask: ev.Asks[0][0], AskQty: ev.Asks[0][1]})
		}
		return out
	case "trades":
		var ts []struct {
			Coin string `json:"coin"`
			Side string `json:"side"` // B = 主动买，A = 主动卖
			Px   string `json:"px"`
			Sz   string `json:"sz"`
			Time int64  `json:"time"`
		}
		if json.Unmarshal(msg.Data, &ts) != nil {
			return nil
		}
		out := make([]Event, 0, len(ts))
		for _, t := range ts {
			side := Buy
			if t.Side == "A" {
				side = Sell
			}
			out = append(out, Event{Kind: KindTrade, Instrument: inst(t.Coin), Symbol: t.Coin, Venue: hlVenue, Time: t.Time, Price: t.Px, Qty: t.Sz, Side: side})
		}
		return out
	case "activeAssetCtx":
		var a struct {
			Coin string `json:"coin"`
			Ctx  struct {
				Funding      string `json:"funding"`
				OpenInterest string `json:"openInterest"`
				PrevDayPx    string `json:"prevDayPx"`
				DayNtlVlm    string `json:"dayNtlVlm"`
				MarkPx       string `json:"markPx"`
				MidPx        string `json:"midPx"`
			} `json:"ctx"`
		}
		if json.Unmarshal(msg.Data, &a) != nil || a.Coin == "" {
			return nil
		}
		now := time.Now()
		// Hyperliquid 每小时整点结算资金费
		next := now.Truncate(time.Hour).Add(time.Hour).UnixMilli()
		ev := Event{Kind: KindMark, Instrument: inst(a.Coin), Symbol: a.Coin, Venue: hlVenue, Time: now.UnixMilli(),
			Mark: a.Ctx.MarkPx, FundingRate: a.Ctx.Funding, NextFundingTime: next, OpenInterest: a.Ctx.OpenInterest}
		tk := Event{Kind: KindTicker, Instrument: ev.Instrument, Symbol: a.Coin, Venue: hlVenue, Time: ev.Time, Price: a.Ctx.MidPx, QuoteVolume24h: a.Ctx.DayNtlVlm}
		if p, err1 := strconv.ParseFloat(a.Ctx.MarkPx, 64); err1 == nil {
			if pd, err2 := strconv.ParseFloat(a.Ctx.PrevDayPx, 64); err2 == nil && pd > 0 {
				tk.Change24hPct = strconv.FormatFloat((p/pd-1)*100, 'f', 3, 64)
			}
		}
		return []Event{ev, tk}
	}
	return nil
}
