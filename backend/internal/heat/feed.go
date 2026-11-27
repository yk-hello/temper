package heat

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// Feed 盘口增量的来源：真实币安，或测试环境用的模拟盘口。
type Feed interface {
	// Run 一直跑：连上后调 connected（这时之前的增量都作废），每条增量调 diff。断线自动重连。
	Run(ctx context.Context, connected func(), diff func(inst string, d Diff))
	// Snapshot 取一次快照。
	Snapshot(ctx context.Context, inst string) (Snapshot, error)
}

// ---------- 币安 ----------

// BinanceDepth 币安 U 本位永续的完整盘口。自己一条连接，和行情分开：
// 盘口增量量大，不能拖慢模拟成交用的那条线（`market/binance.go` 开头记过那次事故）。
type BinanceDepth struct {
	Instruments []string
	WS, REST    string // 默认币安地址；测试时换掉
	Log         *slog.Logger
	HTTP        *http.Client
}

func symOf(inst string) string { return strings.TrimSuffix(inst, "-PERP") + "USDT" }
func instOf(sym string) string { return strings.TrimSuffix(sym, "USDT") + "-PERP" }

func (b *BinanceDepth) url() string {
	base := b.WS
	if base == "" {
		base = "wss://fstream.binance.com"
	}
	parts := make([]string, len(b.Instruments))
	for i, x := range b.Instruments {
		parts[i] = strings.ToLower(symOf(x)) + "@depth@100ms"
	}
	return strings.TrimRight(base, "/") + "/public/stream?streams=" + strings.Join(parts, "/")
}

// Run 见 Feed。
func (b *BinanceDepth) Run(ctx context.Context, connected func(), diff func(string, Diff)) {
	wait := time.Second
	for ctx.Err() == nil {
		began := time.Now()
		err := b.session(ctx, connected, diff)
		if ctx.Err() != nil {
			return
		}
		if b.Log != nil {
			b.Log.Warn("完整盘口断线，重连", "err", err)
		}
		if time.Since(began) > time.Minute {
			wait = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(wait*2, 30*time.Second)
	}
}

func (b *BinanceDepth) session(ctx context.Context, connected func(), diff func(string, Diff)) error {
	dial, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(dial, b.url(), nil)
	if err != nil {
		return err
	}
	defer c.CloseNow()
	c.SetReadLimit(4 << 20)
	connected()
	for {
		read, cancelRead := context.WithTimeout(ctx, 10*time.Second) // 盘口每 100 毫秒一次，10 秒没有就是连接死了（11-28 前 15 秒）
		_, data, err := c.Read(read)
		cancelRead()
		if err != nil {
			return err
		}
		sym, d, err := parseDepth(data)
		if err != nil {
			continue
		}
		diff(instOf(sym), d)
	}
}

// parseDepth 解析一条增量。比通用 JSON 快不了多少，但每秒只有几十条，够用。
func parseDepth(data []byte) (string, Diff, error) {
	var m struct {
		Data struct {
			Sym  string      `json:"s"`
			U    int64       `json:"U"`
			Last int64       `json:"u"`
			PU   int64       `json:"pu"`
			B    [][2]string `json:"b"`
			A    [][2]string `json:"a"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return "", Diff{}, err
	}
	if m.Data.Sym == "" {
		return "", Diff{}, fmt.Errorf("不是盘口增量")
	}
	return m.Data.Sym, Diff{U: m.Data.U, Last: m.Data.Last, PU: m.Data.PU, Bids: levels(m.Data.B), Asks: levels(m.Data.A)}, nil
}

func levels(in [][2]string) [][2]float64 {
	out := make([][2]float64, 0, len(in))
	for _, x := range in {
		p, err1 := strconv.ParseFloat(x[0], 64)
		q, err2 := strconv.ParseFloat(x[1], 64)
		if err1 == nil && err2 == nil {
			out = append(out, [2]float64{p, q})
		}
	}
	return out
}

// Snapshot 见 Feed。1000 档，权重 20（币安每分钟上限 2400），6 个币断线时一起取也不会超。
func (b *BinanceDepth) Snapshot(ctx context.Context, inst string) (Snapshot, error) {
	base := b.REST
	if base == "" {
		base = "https://fapi.binance.com"
	}
	hc := b.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, "GET", base+"/fapi/v1/depth?limit=1000&symbol="+symOf(inst), nil)
	if err != nil {
		return Snapshot{}, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return Snapshot{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return Snapshot{}, fmt.Errorf("币安返回 %d", resp.StatusCode)
	}
	var m struct {
		Last int64       `json:"lastUpdateId"`
		B    [][2]string `json:"bids"`
		A    [][2]string `json:"asks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return Snapshot{}, err
	}
	return Snapshot{LastID: m.Last, Bids: levels(m.B), Asks: levels(m.A)}, nil
}

// ---------- 模拟（只有测试环境用）----------

// SimDepth 跟着模拟价格走的盘口：现价附近铺一层挂单，再随机放一些大单，有的挂一阵子后撤掉。
// 序号和真实的一样连续，走同一套拼接代码。
type SimDepth struct {
	Instruments []string
	Price       func(inst string) (float64, bool)
	Tick        func(inst string) float64
	Seed        uint64
	rnd         *rand.Rand
	books       map[string]*simBook
	cmds        chan simCmd
}

type simBook struct {
	seq   int64
	bids  map[float64]float64
	asks  map[float64]float64
	walls []*simWall
}

type simWall struct {
	p, q float64
	bid  bool
	ttl  int
}

type simCmd struct {
	inst string
	out  chan Snapshot
}

const simRange = 0.01 // 铺到现价上下 1%

func simStep(mid, tick float64) float64 {
	st := niceWidth(mid, tick)
	if st < tick {
		st = tick
	}
	return st
}

func (s *SimDepth) ensure(inst string) *simBook {
	if s.books == nil {
		s.books = map[string]*simBook{}
	}
	b := s.books[inst]
	if b == nil {
		b = &simBook{seq: 1000, bids: map[float64]float64{}, asks: map[float64]float64{}}
		s.books[inst] = b
	}
	return b
}

// Run 见 Feed。所有状态只在这一个 goroutine 里动。
func (s *SimDepth) Run(ctx context.Context, connected func(), diff func(string, Diff)) {
	s.rnd = rand.New(rand.NewPCG(s.Seed, s.Seed^0x9e3779b97f4a7c15))
	if s.cmds == nil {
		s.cmds = make(chan simCmd, 16)
	}
	connected()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case c := <-s.cmds:
			b := s.ensure(c.inst)
			snap := Snapshot{LastID: b.seq}
			for p, q := range b.bids {
				snap.Bids = append(snap.Bids, [2]float64{p, q})
			}
			for p, q := range b.asks {
				snap.Asks = append(snap.Asks, [2]float64{p, q})
			}
			c.out <- snap
		case <-tick.C:
			for _, inst := range s.Instruments {
				if d, ok := s.step(inst); ok {
					diff(inst, d)
				}
			}
		}
	}
}

func (s *SimDepth) step(inst string) (Diff, bool) {
	mid, ok := s.Price(inst)
	if !ok || mid <= 0 {
		return Diff{}, false
	}
	b := s.ensure(inst)
	tick := s.Tick(inst)
	st := simStep(mid, tick)
	up := func(m map[float64]float64, p, q float64, out *[][2]float64) {
		p = math.Round(p/st) * st
		if q <= 0 {
			if _, had := m[p]; !had {
				return
			}
			delete(m, p)
		} else {
			m[p] = q
		}
		*out = append(*out, [2]float64{p, q})
	}
	var d Diff
	base := 20_000 / mid // 每档大约两万美元
	// 穿过现价的挂单消失
	for p := range b.bids {
		if p >= mid || p < mid*(1-simRange) {
			up(b.bids, p, 0, &d.Bids)
		}
	}
	for p := range b.asks {
		if p <= mid || p > mid*(1+simRange) {
			up(b.asks, p, 0, &d.Asks)
		}
	}
	// 补一批：离现价越近越厚
	n := int(mid * simRange / st)
	for range 60 {
		k := 1 + s.rnd.IntN(max(1, n))
		q := base * (0.2 + s.rnd.Float64()*1.6) * (1 + 2/(1+float64(k)/20))
		up(b.bids, mid-float64(k)*st, q, &d.Bids)
		up(b.asks, mid+float64(k)*st, q, &d.Asks)
	}
	// 大单：平均每 8 秒一个，挂 5～120 秒，消失时一次撤完
	if s.rnd.Float64() < 0.0125 {
		bid := s.rnd.IntN(2) == 0
		dist := float64(3+s.rnd.IntN(max(1, n/4))) * st
		p := mid + dist
		if bid {
			p = mid - dist
		}
		b.walls = append(b.walls, &simWall{p: math.Round(p/st) * st, q: base * (40 + s.rnd.Float64()*200), bid: bid, ttl: 50 + s.rnd.IntN(1150)})
	}
	alive := b.walls[:0]
	for _, w := range b.walls {
		w.ttl--
		m, out := b.asks, &d.Asks
		if w.bid {
			m, out = b.bids, &d.Bids
		}
		if w.ttl <= 0 || (w.bid && w.p >= mid) || (!w.bid && w.p <= mid) {
			up(m, w.p, 0, out)
			continue
		}
		up(m, w.p, w.q, out)
		alive = append(alive, w)
	}
	b.walls = alive
	d.PU = b.seq
	d.U = b.seq + 1
	d.Last = b.seq + 1
	b.seq++
	return d, true
}

// Snapshot 见 Feed。
func (s *SimDepth) Snapshot(ctx context.Context, inst string) (Snapshot, error) {
	if s.cmds == nil {
		return Snapshot{}, fmt.Errorf("模拟盘口还没开始")
	}
	out := make(chan Snapshot, 1)
	select {
	case s.cmds <- simCmd{inst: inst, out: out}:
	case <-ctx.Done():
		return Snapshot{}, ctx.Err()
	}
	select {
	case v := <-out:
		return v, nil
	case <-ctx.Done():
		return Snapshot{}, ctx.Err()
	}
}

// restGet 取一个公开接口。
func (b *BinanceDepth) restGet(ctx context.Context, path string, out any) error {
	base := b.REST
	if base == "" {
		base = "https://fapi.binance.com"
	}
	hc := b.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, "GET", base+path, nil)
	if err != nil {
		return err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("币安返回 %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// AggTrades 见 Filler。一页 1000 笔，权重 20，一段空缺最多翻 20 页。
func (b *BinanceDepth) AggTrades(ctx context.Context, inst string, from, to int64) ([]Trade, bool, error) {
	var out []Trade
	start := from
	lastID := int64(-1)
	for page := 0; page < 20; page++ {
		var rows []struct {
			ID    int64  `json:"a"`
			P     string `json:"p"`
			Q     string `json:"q"`
			T     int64  `json:"T"`
			Maker bool   `json:"m"`
		}
		path := fmt.Sprintf("/fapi/v1/aggTrades?symbol=%s&startTime=%d&endTime=%d&limit=1000", symOf(inst), start, to-1)
		if err := b.restGet(ctx, path, &rows); err != nil {
			return nil, false, err
		}
		for _, x := range rows {
			if x.ID <= lastID {
				continue
			}
			lastID = x.ID
			p, err1 := strconv.ParseFloat(x.P, 64)
			q, err2 := strconv.ParseFloat(x.Q, 64)
			if err1 == nil && err2 == nil {
				out = append(out, Trade{T: x.T, P: p, Q: q, Buy: !x.Maker})
			}
		}
		if len(rows) < 1000 {
			return out, true, nil
		}
		start = rows[len(rows)-1].T // 同一毫秒可能翻页：靠 ID 去重
	}
	return out, false, nil
}

// Klines1m 见 Filler。
func (b *BinanceDepth) Klines1m(ctx context.Context, inst string, from, to int64) ([]Bar1m, error) {
	var out []Bar1m
	start := from - from%60_000
	for start < to {
		var rows [][]any
		path := fmt.Sprintf("/fapi/v1/klines?symbol=%s&interval=1m&startTime=%d&endTime=%d&limit=1500", symOf(inst), start, to-1)
		if err := b.restGet(ctx, path, &rows); err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			break
		}
		for _, x := range rows {
			if len(x) < 11 {
				continue
			}
			num := func(i int) float64 {
				s, _ := x[i].(string)
				v, _ := strconv.ParseFloat(s, 64)
				return v
			}
			t, _ := x[0].(float64)
			out = append(out, Bar1m{T: int64(t), O: num(1), C: num(4), QuoteUSD: num(7), TakerBuyUSD: num(10)})
		}
		next := out[len(out)-1].T + 60_000
		if next <= start {
			break
		}
		start = next
	}
	return out, nil
}
