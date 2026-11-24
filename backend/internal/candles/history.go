package candles

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 交易所常用的 K 线周期（D59：图表按周期选，K 线宽度固定，拖动看历史）。
var Intervals = map[string]int64{
	"1s": 1000, "1m": 60_000, "3m": 180_000, "5m": 300_000, "15m": 900_000, "30m": 1_800_000,
	"1h": 3_600_000, "2h": 7_200_000, "4h": 14_400_000, "1d": 86_400_000,
}

// aggregate 把 1 分钟线合成更粗的周期（按 UTC 整点对齐，和币安一致）。
func aggregate(bars []Candle, iv int64) []Candle {
	var out []Candle
	for _, b := range bars {
		t := b.T - b.T%iv
		if n := len(out); n > 0 && out[n-1].T == t {
			x := &out[n-1]
			x.H, x.L, x.C = max(x.H, b.H), min(x.L, b.L), b.C
			x.VB += b.VB
			x.VS += b.VS
			x.N += b.N
			continue
		}
		b.T = t
		out = append(out, b)
	}
	return out
}

// Query 按周期取 K 线：
//
//	since > 0：从 since 所在的那根开始到最新（实时刷新用）
//	before > 0：before 之前的最近 limit 根（往左拖看历史用）
//	都为 0：最新的 limit 根
//
// 内存里的数据不够时，用 Remote 从交易所补（只补更早的部分）。
func (s *Store) Query(ctx context.Context, instrument string, iv, since, before int64, limit int) []Candle {
	if limit <= 0 || limit > 1000 {
		limit = 300
	}
	base := int64(60_000)
	if iv == 1000 {
		base = 1000
	}
	s.mu.RLock()
	var mem []Candle
	if ser := s.m[instrument][base]; ser != nil {
		mem = append([]Candle(nil), ser.bars...)
	}
	s.mu.RUnlock()
	partial := iv != base && len(mem) > 0 && mem[0].T%iv != 0
	if iv != base {
		mem = aggregate(mem, iv)
	}
	// 内存里最早那根 1 分钟线不在周期边界上：合成的第一根只有后半段数据，高低点和成交量都不对。
	// 查历史时丢掉它，下面从交易所取完整的。
	if partial && since == 0 && s.Remote != nil {
		mem = mem[1:]
	}
	if since > 0 {
		from := since - since%iv
		i := len(mem)
		for i > 0 && mem[i-1].T >= from {
			i--
		}
		return nonNil(mem[i:])
	}
	if before > 0 {
		i := len(mem)
		for i > 0 && mem[i-1].T >= before {
			i--
		}
		mem = mem[:i]
	}
	if len(mem) > limit {
		return mem[len(mem)-limit:]
	}
	// 内存里的不够：从交易所补更早的（1 秒线交易所没有）
	if iv != 1000 && s.Remote != nil {
		end := before
		if len(mem) > 0 {
			end = mem[0].T
		}
		if end == 0 {
			end = time.Now().UnixMilli() + iv
		}
		if old, err := s.Remote.Get(ctx, instrument, iv, end, limit-len(mem)); err == nil {
			mem = append(old, mem...)
		}
	}
	return nonNil(mem)
}

func nonNil(c []Candle) []Candle {
	if c == nil {
		return []Candle{}
	}
	return c
}

// Remote 从币安取历史 K 线，带缓存（同一段历史多人同时看只请求一次）。
type Remote struct {
	Symbols map[string]string // 品种 → 交易所代码，如 BTC-PERP → BTCUSDT
	BaseURL string            // 为空时用币安正式地址（测试时换成假服务器）
	mu      sync.Mutex
	cache   map[string]cached
}

// Add 运行中加一个品种。
func (r *Remote) Add(instrument, symbol string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Symbols == nil {
		r.Symbols = map[string]string{}
	}
	r.Symbols[instrument] = symbol
}

func (r *Remote) symbol(instrument string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.Symbols[instrument]
	return s, ok
}

type cached struct {
	at   time.Time
	bars []Candle
}

var ivName = map[int64]string{}

func init() {
	for k, v := range Intervals {
		ivName[v] = k
	}
}

// Get end 之前（不含）的最近 limit 根。
func (r *Remote) Get(ctx context.Context, instrument string, iv, end int64, limit int) ([]Candle, error) {
	sym, ok := r.symbol(instrument)
	name, ok2 := ivName[iv]
	if !ok || !ok2 || limit <= 0 {
		return nil, fmt.Errorf("不支持")
	}
	key := fmt.Sprintf("%s|%d|%d|%d", instrument, iv, end/iv, limit)
	r.mu.Lock()
	if r.cache == nil {
		r.cache = map[string]cached{}
	}
	if c, ok := r.cache[key]; ok && time.Since(c.at) < 5*time.Minute {
		r.mu.Unlock()
		return append([]Candle(nil), c.bars...), nil
	}
	r.mu.Unlock()
	bars, err := fetchFrom(ctx, r.BaseURL, sym, name, end-1, limit)
	if err != nil {
		return nil, err
	}
	// 去掉还没走完、或者不早于 end 的那根
	for len(bars) > 0 && bars[len(bars)-1].T >= end {
		bars = bars[:len(bars)-1]
	}
	r.mu.Lock()
	if len(r.cache) > 500 {
		r.cache = map[string]cached{}
	}
	r.cache[key] = cached{at: time.Now(), bars: bars}
	r.mu.Unlock()
	return append([]Candle(nil), bars...), nil
}

// minutes 取 from 到 to（都含）的 1 分钟线。一次最多 1500 根；窟窿更长时只补靠近 to 的部分，
// 更早的本来也超出了内存保留的范围。
func (r *Remote) minutes(ctx context.Context, instrument string, from, to int64) ([]Candle, error) {
	sym, ok := r.symbol(instrument)
	if !ok {
		return nil, fmt.Errorf("不支持")
	}
	limit := int((to-from)/60_000) + 1
	if limit > 1500 {
		limit = 1500
	}
	bars, err := fetchFrom(ctx, r.BaseURL, sym, "1m", to+59_999, limit)
	if err != nil {
		return nil, err
	}
	for len(bars) > 0 && bars[0].T < from {
		bars = bars[1:]
	}
	return bars, nil
}

func fetch(ctx context.Context, symbol, interval string, endTime int64, limit int) ([]Candle, error) {
	return fetchFrom(ctx, "", symbol, interval, endTime, limit)
}

func fetchFrom(ctx context.Context, base, symbol, interval string, endTime int64, limit int) ([]Candle, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	path := "/fapi/v1/klines"
	if rest, ok := strings.CutPrefix(symbol, "spot:"); ok { // 现货：地址不同，格式一样
		symbol, path = rest, "/api/v3/klines"
		if base == "" {
			base = "https://api.binance.com"
		}
	}
	if base == "" {
		base = "https://fapi.binance.com"
	}
	url := fmt.Sprintf("%s%s?symbol=%s&interval=%s&limit=%d", base, path, symbol, interval, limit)
	if endTime > 0 {
		url += "&endTime=" + strconv.FormatInt(endTime, 10)
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("klines %s %s: HTTP %d", symbol, interval, res.StatusCode)
	}
	var rows [][]any
	if err := json.NewDecoder(res.Body).Decode(&rows); err != nil {
		return nil, err
	}
	return parseKlines(rows), nil
}

func parseKlines(rows [][]any) []Candle {
	out := make([]Candle, 0, len(rows))
	for _, r := range rows {
		if len(r) < 11 {
			continue
		}
		f := func(i int) float64 {
			s, _ := r[i].(string)
			v, _ := strconv.ParseFloat(s, 64)
			return v
		}
		t, _ := r[0].(float64)
		n, _ := r[8].(float64)
		vol, buy := f(5), f(9)
		out = append(out, Candle{T: int64(t), O: f(1), H: f(2), L: f(3), C: f(4), VB: buy, VS: vol - buy, N: int(n)})
	}
	return out
}
