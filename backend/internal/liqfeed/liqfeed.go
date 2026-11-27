// Package liqfeed 别家交易所的强平（18 第 10.2 条，D102）。币安的强平推送每个币每秒只推最大的一笔，看不全；
// Bybit（allLiquidation）和 OKX（liquidation-orders）是官方公开的全量推送，合起来更接近真实的爆仓量。
// 只要 USDT 本位永续、只要我们在看的那些币。断了自己重连。
package liqfeed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Liq 一笔强平。Side：long = 多单被强平，short = 空单被强平。
type Liq struct {
	T          int64
	Venue      string // bybit | okx
	Instrument string
	Side       string
	P, Q, USD  float64
}

// Feed 两家的连接。Inst：交易对（BTCUSDT）→ 我们的品种（BTC-PERP），只收这些。
type Feed struct {
	Inst  map[string]string
	OnLiq func(Liq)
	Log   *slog.Logger
	// 测试用；空 = 官方地址
	BybitURL, OKXURL, OKXREST string
	HTTP                      *http.Client

	mu    sync.Mutex
	ctVal map[string]float64 // OKX 每张合约多少个币
	stat  map[string]bool
}

func (f *Feed) set(venue string, on bool) {
	f.mu.Lock()
	if f.stat == nil {
		f.stat = map[string]bool{}
	}
	f.stat[venue] = on
	f.mu.Unlock()
}

// Run 一直连着，直到 ctx 结束。
func (f *Feed) Run(ctx context.Context) {
	go f.loop(ctx, "bybit", f.bybit)
	f.loop(ctx, "okx", f.okx)
}

func (f *Feed) loop(ctx context.Context, venue string, session func(context.Context) error) {
	wait := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		err := session(ctx)
		f.set(venue, false)
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) > time.Minute {
			wait = time.Second
		}
		if f.Log != nil {
			f.Log.Warn(venue+" 强平推送断了，重连", "err", err, "wait", wait)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(wait*2, time.Minute)
	}
}

func dial(ctx context.Context, u string) (*websocket.Conn, error) {
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(dctx, u, nil)
	if err != nil {
		return nil, err
	}
	c.SetReadLimit(4 << 20)
	return c, nil
}

// ping 每隔一段时间发一条心跳（两家都要求客户端主动发）。
// readIdle 读一条消息。太久一条都没有（连 ping 的回应都没有）就当连接已经死了：返回错误，外面重连。
// 11-28 前不设时限，连接要是悄悄断掉（不报错、只是不来数据）就一直等下去
func readIdle(ctx context.Context, c *websocket.Conn, idle time.Duration) ([]byte, error) {
	rctx, cancel := context.WithTimeout(ctx, idle)
	defer cancel()
	_, raw, err := c.Read(rctx)
	if err != nil && ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
		return nil, fmt.Errorf("%v 没有收到消息", idle)
	}
	return raw, err
}

func ping(ctx context.Context, c *websocket.Conn, every time.Duration, msg string) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := c.Write(wctx, websocket.MessageText, []byte(msg))
			cancel()
			if err != nil {
				return
			}
		}
	}
}

func num(s string) float64 { v, _ := strconv.ParseFloat(s, 64); return v }

// ---------- Bybit ----------

func (f *Feed) bybit(ctx context.Context) error {
	u := f.BybitURL
	if u == "" {
		u = "wss://stream.bybit.com/v5/public/linear"
	}
	c, err := dial(ctx, u)
	if err != nil {
		return err
	}
	defer c.CloseNow()
	var topics []string
	for sym := range f.Inst {
		topics = append(topics, "allLiquidation."+sym)
	}
	for i := 0; i < len(topics); i += 10 { // 一次最多订 10 个
		msg, _ := json.Marshal(map[string]any{"op": "subscribe", "args": topics[i:min(i+10, len(topics))]})
		if err := c.Write(ctx, websocket.MessageText, msg); err != nil {
			return err
		}
	}
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go ping(sctx, c, 20*time.Second, `{"op":"ping"}`)
	f.set("bybit", true)
	for {
		raw, err := readIdle(ctx, c, 50*time.Second) // 每 20 秒 ping 一次，对方会回

		if err != nil {
			return err
		}
		for _, l := range ParseBybit(raw, f.Inst) {
			f.OnLiq(l)
		}
	}
}

// ParseBybit 解析一条 allLiquidation 推送。S = Buy 表示多单被强平（官方文档原话），Sell 是空单。
func ParseBybit(raw []byte, inst map[string]string) []Liq {
	var m struct {
		Topic string `json:"topic"`
		Data  []struct {
			T int64  `json:"T"`
			S string `json:"s"`
			D string `json:"S"`
			V string `json:"v"`
			P string `json:"p"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &m) != nil || !strings.HasPrefix(m.Topic, "allLiquidation.") {
		return nil
	}
	var out []Liq
	for _, d := range m.Data {
		in, ok := inst[d.S]
		p, q := num(d.P), num(d.V)
		if !ok || p <= 0 || q <= 0 {
			continue
		}
		side := "short"
		if d.D == "Buy" {
			side = "long"
		}
		out = append(out, Liq{T: d.T, Venue: "bybit", Instrument: in, Side: side, P: p, Q: q, USD: p * q})
	}
	return out
}

// ---------- OKX ----------

// loadCtVal OKX 的数量是"张"，要乘每张多少个币。
func (f *Feed) loadCtVal(ctx context.Context) error {
	base := f.OKXREST
	if base == "" {
		base = "https://www.okx.com"
	}
	hc := f.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/api/v5/public/instruments?instType=SWAP", nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "qs/1 (+self-hosted trading workstation)")
	res, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("OKX 合约列表：HTTP %d", res.StatusCode)
	}
	var r struct {
		Data []struct {
			InstID string `json:"instId"`
			CtVal  string `json:"ctVal"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 16<<20)).Decode(&r); err != nil {
		return err
	}
	m := map[string]float64{}
	for _, d := range r.Data {
		if v := num(d.CtVal); v > 0 {
			m[d.InstID] = v
		}
	}
	f.mu.Lock()
	f.ctVal = m
	f.mu.Unlock()
	return nil
}

func (f *Feed) okx(ctx context.Context) error {
	if err := f.loadCtVal(ctx); err != nil {
		return err
	}
	u := f.OKXURL
	if u == "" {
		u = "wss://ws.okx.com/ws/v5/public" // 8443 端口官方说要停用，用 443
	}
	c, err := dial(ctx, u)
	if err != nil {
		return err
	}
	defer c.CloseNow()
	msg, _ := json.Marshal(map[string]any{"op": "subscribe", "args": []map[string]string{{"channel": "liquidation-orders", "instType": "SWAP"}}})
	if err := c.Write(ctx, websocket.MessageText, msg); err != nil {
		return err
	}
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go ping(sctx, c, 25*time.Second, "ping")
	f.set("okx", true)
	for {
		raw, err := readIdle(ctx, c, 60*time.Second) // 每 25 秒 ping 一次，对方回 pong

		if err != nil {
			return err
		}
		f.mu.Lock()
		cv := f.ctVal
		f.mu.Unlock()
		for _, l := range ParseOKX(raw, f.Inst, cv) {
			f.OnLiq(l)
		}
	}
}

// ParseOKX 解析一条 liquidation-orders 推送。side 是强平单的方向：sell = 多单被强平，buy = 空单。
func ParseOKX(raw []byte, inst map[string]string, ctVal map[string]float64) []Liq {
	var m struct {
		Arg struct {
			Channel string `json:"channel"`
		} `json:"arg"`
		Data []struct {
			InstID  string `json:"instId"`
			Details []struct {
				Side string `json:"side"`
				Px   string `json:"bkPx"`
				Sz   string `json:"sz"`
				Ts   string `json:"ts"`
			} `json:"details"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &m) != nil || m.Arg.Channel != "liquidation-orders" {
		return nil
	}
	var out []Liq
	for _, d := range m.Data {
		if !strings.HasSuffix(d.InstID, "-USDT-SWAP") {
			continue
		}
		in, ok := inst[strings.TrimSuffix(d.InstID, "-USDT-SWAP")+"USDT"]
		cv := ctVal[d.InstID]
		if !ok || cv <= 0 {
			continue
		}
		for _, x := range d.Details {
			p, q := num(x.Px), num(x.Sz)*cv
			if p <= 0 || q <= 0 {
				continue
			}
			side := "short"
			if x.Side == "sell" {
				side = "long"
			}
			ts, _ := strconv.ParseInt(x.Ts, 10, 64)
			out = append(out, Liq{T: ts, Venue: "okx", Instrument: in, Side: side, P: p, Q: q, USD: p * q})
		}
	}
	return out
}
