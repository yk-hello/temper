package market

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Binance 接入币安 U 本位永续的公开行情。
//
// 注意：币安在 2026-04-23 停用了旧地址 wss://fstream.binance.com/ws 和 /stream。
// 新地址按频率分两条线路：
//   - /public：高频（最优买卖价 bookTicker、深度 depth）
//   - /market：常规（成交 aggTrade、标记价格 markPrice、24h 统计 ticker）
//
// 2026-11-04 实测：6 个品种的 bookTicker 每秒 2000 多条（约 480 KB/s），后端处理不过来，
// 盘口积压到落后十几秒、被币安断开重连（一个多小时 84 次），模拟成交按过期盘口成交。
// 所以 /public 只订 depth20@100ms（每品种每秒 10 条，前 20 档里已经有最优买卖价），负载降到约 3%。
//
// 文档：https://developers.binance.com/docs/derivatives/usds-margined-futures/websocket-market-streams/Important-WebSocket-Change-Notice
type Binance struct {
	Symbols []string
	BaseURL string // 默认 wss://fstream.binance.com；现货默认 wss://stream.binance.com:443（BINANCE_SPOT_WS_URL 可改）
	Spot    bool   // 现货：一条线路，地址和格式都不同
	Log     *slog.Logger
	// Idle 多久一条消息都没收到就当连接已经死了、断开重连（0 = idleDefault）。测试时调短
	Idle time.Duration

	mu    sync.Mutex
	extra []string                   // 运行中加进来的品种（用户点开了别的币）
	conns map[string]*websocket.Conn // 线路名 → 当前连接
	subID int
}

// 两条线路各订什么
var binanceKinds = map[string][]string{
	"public": {"depth20@100ms"},
	"market": {"aggTrade", "markPrice@1s", "ticker", "forceOrder"},
}

// Add 运行中加一个品种：发 SUBSCRIBE，不用断线。重连时也会订上。
func (b *Binance) Add(symbol string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, x := range b.Symbols {
		if x == symbol {
			return true
		}
	}
	for _, x := range b.extra {
		if x == symbol {
			return true
		}
	}
	b.extra = append(b.extra, symbol)
	for name, c := range b.conns {
		b.subscribeLocked(c, name, []string{symbol})
	}
	return true
}

func (b *Binance) subscribeLocked(c *websocket.Conn, line string, symbols []string) {
	var params []string
	for _, sym := range symbols {
		for _, k := range binanceKinds[line] {
			params = append(params, strings.ToLower(sym)+"@"+k)
		}
	}
	b.subID++
	msg, _ := json.Marshal(map[string]any{"method": "SUBSCRIBE", "params": params, "id": b.subID})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := c.Write(ctx, websocket.MessageText, msg); err != nil && b.Log != nil {
			b.Log.Warn("binance subscribe", "line", line, "err", err)
		}
	}()
}

const binanceVenue = "binance-usdm"

// idleDefault 每条线路每秒都有好几条消息（标记价格、24 小时统计每秒一条，盘口每 100 毫秒），10 秒一条都没有就是连接死了。
// 11-28 前是 60 秒：电脑的网络断一下（不报错、只是没数据），价格在页面上停一分钟才重连
const idleDefault = 10 * time.Second

func (b *Binance) Name() string { return binanceVenue }

func (b *Binance) base() string {
	if b.BaseURL != "" {
		return strings.TrimRight(b.BaseURL, "/")
	}
	if b.Spot {
		// 现货走 443（11-27 外部反馈：有的网络拦 9443 这种不常见的端口，TLS 握手就被断开）。
		// 币安官方 9443、443 两个都给；只要行情的话 wss://data-stream.binance.vision:443 也行
		return "wss://stream.binance.com:443"
	}
	return "wss://fstream.binance.com"
}

func (b *Binance) streams(kinds ...string) string {
	var parts []string
	for _, s := range b.Symbols {
		for _, k := range kinds {
			parts = append(parts, strings.ToLower(s)+"@"+k)
		}
	}
	return strings.Join(parts, "/")
}

// URLs 返回两条线路的连接地址（导出供测试检查）。
func (b *Binance) URLs() map[string]string {
	if b.Spot {
		return map[string]string{"spot": b.base() + "/stream?streams=" + b.streams("aggTrade", "depth20@100ms", "ticker")}
	}
	return map[string]string{
		"public": b.base() + "/public/stream?streams=" + b.streams(binanceKinds["public"]...),
		"market": b.base() + "/market/stream?streams=" + b.streams(binanceKinds["market"]...),
	}
}

func (b *Binance) Run(ctx context.Context, sink Sink) {
	sink.UpdateStatus(func(s *SourceStatus) {
		s.Name = binanceVenue
		for name := range b.URLs() {
			s.Connections[name] = false
		}
	})
	done := make(chan struct{})
	urls := b.URLs()
	for name, url := range urls {
		go func(name, url string) {
			defer func() { done <- struct{}{} }()
			b.loop(ctx, name, url, sink)
		}(name, url)
	}
	for range urls {
		<-done
	}
}

// loop 保持一条连接：断了就按 1s、2s、4s… 最多 30s 的间隔重连。
func (b *Binance) loop(ctx context.Context, name, url string, sink Sink) {
	backoff := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		err := b.session(ctx, name, url, sink)
		sink.UpdateStatus(func(s *SourceStatus) {
			s.Connections[name] = false
			if err != nil && ctx.Err() == nil {
				s.LastError = fmt.Sprintf("%s: %v", name, err)
				s.Reconnects++
			}
		})
		if ctx.Err() != nil {
			return
		}
		if b.Log != nil {
			b.Log.Warn("binance connection lost, reconnecting", "line", name, "err", err, "wait", backoff)
		}
		if time.Since(start) > time.Minute {
			backoff = time.Second // 连了很久才断（比如币安每 24 小时主动断开），立刻重连
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (b *Binance) session(ctx context.Context, name, url string, sink Sink) error {
	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(dialCtx, url, nil)
	if err != nil {
		return err
	}
	defer c.CloseNow()
	c.SetReadLimit(1 << 20)
	// 运行中加进来的品种：地址里没有，连上后再订
	b.mu.Lock()
	if b.conns == nil {
		b.conns = map[string]*websocket.Conn{}
	}
	b.conns[name] = c
	if len(b.extra) > 0 {
		b.subscribeLocked(c, name, b.extra)
	}
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		if b.conns[name] == c {
			delete(b.conns, name)
		}
		b.mu.Unlock()
	}()
	sink.UpdateStatus(func(s *SourceStatus) { s.Connections[name] = true; s.LastError = "" })
	if b.Log != nil {
		b.Log.Info("binance connected", "line", name)
	}
	lag := &lagMeter{}
	defer sink.UpdateStatus(func(s *SourceStatus) { delete(s.Lag, name) })
	for {
		// 太久一条消息都没有：视为假死，主动重连
		idle := b.Idle
		if idle <= 0 {
			idle = idleDefault
		}
		readCtx, cancelRead := context.WithTimeout(ctx, idle)
		_, data, err := c.Read(readCtx)
		cancelRead()
		if err != nil {
			if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
				return fmt.Errorf("%v 没有收到消息", idle)
			}
			return err
		}
		if ev, ok := b.parse(data); ok {
			sink.Publish(ev)
			// 只用实时推送的成交和深度估计积压：标记价、24 小时统计每 1 秒才推一次，时间戳天然会落后，算进去会误报"延迟"
			if ev.Kind != KindTrade && ev.Kind != KindDepth && ev.Kind != KindBook {
				continue
			}
			if v, report := lag.observe(time.Now().UnixMilli(), ev.Time); report {
				sink.UpdateStatus(func(s *SourceStatus) {
					if s.Lag == nil {
						s.Lag = map[string]int64{}
					}
					s.Lag[name] = v
				})
				// 积压超过 30 秒：丢掉积压，重新连接（比慢慢追上快）
				if v > 30_000 {
					return fmt.Errorf("行情积压 %d 秒，重新连接", v/1000)
				}
			}
		}
	}
}

// lagMeter 估计一条线路的积压：每条消息算 (本地收到时间 − 交易所时间)，减去最近一分钟的最小值。
// 减最小值是为了扣掉本机和交易所的时钟偏差（Windows 上的 Docker 时钟常常不准）和网络基础延迟。
// 每秒最多报告一次。
type lagMeter struct {
	minCur, minPrev int64
	winStart        int64
	lastReport      int64
	worst           int64
}

func (m *lagMeter) observe(now, exch int64) (int64, bool) {
	if exch <= 0 {
		return 0, false
	}
	d := now - exch
	if m.winStart == 0 {
		m.winStart, m.minCur, m.minPrev = now, d, d
	}
	if now-m.winStart > 60_000 {
		m.minPrev, m.minCur, m.winStart = m.minCur, d, now
	}
	if d < m.minCur {
		m.minCur = d
	}
	base := m.minCur
	if m.minPrev < base {
		base = m.minPrev
	}
	if v := d - base; v > m.worst {
		m.worst = v
	}
	if now-m.lastReport < 1000 {
		return 0, false
	}
	v := m.worst
	m.lastReport, m.worst = now, 0
	return v, true
}

// ParseBinance 解析一条合并流消息：{"stream":"...","data":{...}}。
//
// 币安的字段名大小写敏感（如 "c" 是最新价、"C" 是收盘时间，"q" 和 "Q" 也不同），
// 而 Go 的 JSON 结构体解析不区分大小写，会串字段。所以这里先解成 map，再按精确键名取值。
func ParseBinance(raw []byte) (Event, bool) {
	var wrap struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &wrap); err != nil || len(wrap.Data) == 0 {
		return Event{}, false
	}
	d := wrap.Data
	// 强平单的字段在 "o" 里面
	if jstr(d, "e") == "forceOrder" {
		var o map[string]json.RawMessage
		if json.Unmarshal(d["o"], &o) != nil {
			return Event{}, false
		}
		sym := jstr(o, "s")
		if sym == "" {
			return Event{}, false
		}
		ev := Event{Kind: KindLiquidation, Instrument: CanonicalPerp(sym), Symbol: sym, Venue: binanceVenue, Time: jint(o, "T"),
			Price: jstr(o, "ap"), Qty: jstr(o, "z"), Side: Buy}
		if ev.Price == "" || ev.Price == "0" {
			ev.Price = jstr(o, "p")
		}
		if jstr(o, "S") == "SELL" { // 强平卖单 = 多头被强平
			ev.Side = Sell
		}
		return ev, true
	}
	sym := jstr(d, "s")
	if sym == "" {
		return Event{}, false
	}
	ev := Event{Instrument: CanonicalPerp(sym), Symbol: sym, Venue: binanceVenue, Time: jint(d, "E")}
	switch jstr(d, "e") {
	case "aggTrade":
		ev.Kind, ev.Price, ev.Qty = KindTrade, jstr(d, "p"), jstr(d, "q")
		if t := jint(d, "T"); t != 0 {
			ev.Time = t
		}
		ev.Side = Buy
		if jbool(d, "m") { // 买方是挂单方 → 主动成交的是卖方
			ev.Side = Sell
		}
	case "bookTicker":
		ev.Kind = KindBook
		ev.Bid, ev.BidQty, ev.Ask, ev.AskQty = jstr(d, "b"), jstr(d, "B"), jstr(d, "a"), jstr(d, "A")
		if ev.Time == 0 {
			ev.Time = jint(d, "T")
		}
	case "markPriceUpdate":
		ev.Kind, ev.Mark, ev.FundingRate, ev.NextFundingTime = KindMark, jstr(d, "p"), jstr(d, "r"), jint(d, "T")
		ev.Index = jstr(d, "i")
	case "depthUpdate":
		ev.Kind = KindDepth
		if t := jint(d, "T"); t != 0 {
			ev.Time = t
		}
		ev.Bids, ev.Asks = levels(d["b"]), levels(d["a"])
	case "24hrTicker":
		// "c" 最新价，"P" 涨跌幅（%），"q" 成交额（计价币），"h"/"l" 最高最低
		ev.Kind, ev.Price = KindTicker, jstr(d, "c")
		ev.Change24hPct, ev.QuoteVolume24h = jstr(d, "P"), jstr(d, "q")
		ev.High24h, ev.Low24h = jstr(d, "h"), jstr(d, "l")
	default:
		return Event{}, false
	}
	return ev, true
}

func levels(raw json.RawMessage) [][2]string {
	var out [][2]string
	_ = json.Unmarshal(raw, &out)
	return out
}

func jstr(m map[string]json.RawMessage, k string) string {
	var s string
	if json.Unmarshal(m[k], &s) == nil {
		return s
	}
	return ""
}

func jint(m map[string]json.RawMessage, k string) int64 {
	var n int64
	if json.Unmarshal(m[k], &n) == nil {
		return n
	}
	return 0
}

func jbool(m map[string]json.RawMessage, k string) bool {
	var b bool
	_ = json.Unmarshal(m[k], &b)
	return b
}

func (b *Binance) parse(raw []byte) (Event, bool) {
	if b.Spot {
		return ParseBinanceSpot(raw)
	}
	return ParseBinance(raw)
}
