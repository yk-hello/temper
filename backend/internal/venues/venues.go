// Package venues 跨交易所对比（可视化第 5 项、B）：资金费矩阵、价差带。
// 币安和 Hyperliquid 已经在实时行情里；Bybit、OKX 用公开接口每分钟取一次。取不到的写"没有数据"，不猜。
package venues

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"qs/internal/book"
)

// Cell 一个交易所的一个币。
type Cell struct {
	Mark   float64 `json:"mark"`
	Rate   float64 `json:"rate"`   // 这一期的资金费率
	Hours  float64 `json:"hours"`  // 一期几小时
	Rate8h float64 `json:"rate8h"` // 折算成每 8 小时（%）。不算年化：年化数字容易被读成收益承诺（D85）
	Next   int64   `json:"next,omitempty"`
	T      int64   `json:"t"`
}

// Row 矩阵的一行。
type Row struct {
	Coin      string          `json:"coin"`
	Cells     map[string]Cell `json:"cells"`
	SpreadBps float64         `json:"spread_bps"` // 标记价最高和最低差多少
	Gap8h     float64         `json:"gap_8h"`     // 费率最高和最低的差，每 8 小时（%）。只是费率的差，不是收益
	Hi, Lo    string          `json:"-"`
	HiV       string          `json:"hi"`
	LoV       string          `json:"lo"`
}

// Venues 列的顺序。
var Venues = []string{"binance", "hyperliquid", "bybit", "okx"}

// Service 服务。
type Service struct {
	Books   *book.Store
	Primary string
	Coins   []string
	HL      bool
	Sim     func(coin string) (float64, bool) // 模拟行情：别的交易所也按它算
	HTTP    *http.Client
	Now     func() int64

	mu     sync.Mutex
	remote map[string]map[string]Cell // venue → coin → cell
	hist   map[string][]point         // coin → 价差历史
	since  int64
	errs   map[string]string
	polls  map[string]PollStat
}

// PollStat 一个定时取的交易所：上一次花了多久、最后一次成功是什么时候。
type PollStat struct {
	Ms float64
	At int64
	OK bool
}

// Polls Bybit、OKX 的状态（系统控制台）。
func (s *Service) Polls() map[string]PollStat {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]PollStat, len(s.polls))
	for k, v := range s.polls {
		out[k] = v
	}
	return out
}

type point struct {
	T int64
	V map[string]float64 // venue → 相对币安的价差（基点）
}

func (s *Service) now() int64 {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UnixMilli()
}

func per8h(rate, hours float64) float64 {
	if hours <= 0 {
		hours = 8
	}
	return rate * 100 * 8 / hours
}

func (s *Service) cellOf(venue, coin string) (Cell, bool) {
	if venue == "binance" || (venue == "hyperliquid" && s.HL) {
		v, h := s.Primary, 8.0
		if venue == "hyperliquid" {
			v, h = "hyperliquid", 1
		}
		t, ok := s.Books.Ticker(v, coin+"-PERP")
		if !ok || !t.Mark.IsPositive() {
			return Cell{}, false
		}
		rate := t.FundingRate.InexactFloat64()
		r8 := per8h(rate, h)
		return Cell{Mark: t.Mark.InexactFloat64(), Rate: rate, Hours: h, Rate8h: r8, Next: t.NextFunding, T: s.now()}, true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.remote[venue][coin]
	return c, ok
}

// Matrix 现在的矩阵。
func (s *Service) Matrix() []Row {
	out := make([]Row, 0, len(s.Coins))
	for _, coin := range s.Coins {
		r := Row{Coin: coin, Cells: map[string]Cell{}}
		lo, hi := 0.0, 0.0
		rlo, rhi := 0.0, 0.0
		first := true
		for _, v := range Venues {
			c, ok := s.cellOf(v, coin)
			if !ok {
				continue
			}
			r.Cells[v] = c
			if first || c.Mark < lo {
				lo = c.Mark
			}
			if first || c.Mark > hi {
				hi = c.Mark
			}
			if first || c.Rate8h < rlo {
				rlo, r.LoV = c.Rate8h, v
			}
			if first || c.Rate8h > rhi {
				rhi, r.HiV = c.Rate8h, v
			}
			first = false
		}
		if lo > 0 {
			r.SpreadBps = (hi - lo) / lo * 1e4
		}
		r.Gap8h = rhi - rlo
		out = append(out, r)
	}
	return out
}

// Spread 这个币的价差带。
func (s *Service) Spread(coin string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.hist[coin]
	ts := make([]int64, len(h))
	series := map[string][]*float64{}
	for _, v := range Venues[1:] {
		series[v] = make([]*float64, len(h))
	}
	for i, p := range h {
		ts[i] = p.T
		for v, x := range p.V {
			x := x
			if series[v] != nil {
				series[v][i] = &x
			}
		}
	}
	return map[string]any{"t": ts, "series": series, "since": s.since}
}

func (s *Service) sample() {
	now := s.now()
	type row struct {
		coin string
		v    map[string]float64
	}
	var rows []row
	for _, coin := range s.Coins {
		base, ok := s.cellOf("binance", coin)
		if !ok || base.Mark <= 0 {
			continue
		}
		r := row{coin, map[string]float64{}}
		for _, v := range Venues[1:] {
			if c, ok := s.cellOf(v, coin); ok && c.Mark > 0 {
				r.v[v] = (c.Mark - base.Mark) / base.Mark * 1e4
			}
		}
		rows = append(rows, r)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hist == nil {
		s.hist = map[string][]point{}
	}
	if s.since == 0 {
		s.since = now
	}
	for _, r := range rows {
		h := append(s.hist[r.coin], point{T: now, V: r.v})
		if len(h) > 2160 { // 10 秒一个点，留 6 小时
			h = h[len(h)-2160:]
		}
		s.hist[r.coin] = h
	}
}

func (s *Service) get(ctx context.Context, url string, out any) error {
	hc := s.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func f64(s string) float64 { v, _ := strconv.ParseFloat(s, 64); return v }

// pollBybit 一次取全部。
func (s *Service) pollBybit(ctx context.Context) (map[string]Cell, error) {
	var r struct {
		Result struct {
			List []struct {
				Symbol       string `json:"symbol"`
				MarkPrice    string `json:"markPrice"`
				FundingRate  string `json:"fundingRate"`
				NextFunding  string `json:"nextFundingTime"`
				FundingHours string `json:"fundingIntervalHour"`
			} `json:"list"`
		} `json:"result"`
	}
	if err := s.get(ctx, "https://api.bybit.com/v5/market/tickers?category=linear", &r); err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, c := range s.Coins {
		want[c] = true
	}
	out := map[string]Cell{}
	now := s.now()
	for _, x := range r.Result.List {
		coin := strings.TrimSuffix(x.Symbol, "USDT")
		if !want[coin] || coin == x.Symbol {
			continue
		}
		h := f64(x.FundingHours)
		if h <= 0 {
			h = 8
		}
		rate := f64(x.FundingRate)
		r8 := per8h(rate, h)
		next, _ := strconv.ParseInt(x.NextFunding, 10, 64)
		out[coin] = Cell{Mark: f64(x.MarkPrice), Rate: rate, Hours: h, Rate8h: r8, Next: next, T: now}
	}
	return out, nil
}

// pollOKX 标记价一次取全部，资金费一个币一次。
func (s *Service) pollOKX(ctx context.Context) (map[string]Cell, error) {
	var mk struct {
		Data []struct {
			InstID string `json:"instId"`
			MarkPx string `json:"markPx"`
		} `json:"data"`
	}
	if err := s.get(ctx, "https://www.okx.com/api/v5/public/mark-price?instType=SWAP", &mk); err != nil {
		return nil, err
	}
	out := map[string]Cell{}
	now := s.now()
	marks := map[string]float64{}
	for _, x := range mk.Data {
		marks[x.InstID] = f64(x.MarkPx)
	}
	for _, coin := range s.Coins {
		id := coin + "-USDT-SWAP"
		if marks[id] <= 0 {
			continue
		}
		var fr struct {
			Data []struct {
				Rate string `json:"fundingRate"`
				Next string `json:"nextFundingTime"`
				Time string `json:"fundingTime"`
			} `json:"data"`
		}
		if err := s.get(ctx, "https://www.okx.com/api/v5/public/funding-rate?instId="+id, &fr); err != nil || len(fr.Data) == 0 {
			out[coin] = Cell{Mark: marks[id], Hours: 8, T: now}
			continue
		}
		h := 8.0
		t0, _ := strconv.ParseInt(fr.Data[0].Time, 10, 64)
		t1, _ := strconv.ParseInt(fr.Data[0].Next, 10, 64)
		if t1 > t0 && t0 > 0 {
			h = float64(t1-t0) / 3_600_000
		}
		rate := f64(fr.Data[0].Rate)
		r8 := per8h(rate, h)
		out[coin] = Cell{Mark: marks[id], Rate: rate, Hours: h, Rate8h: r8, Next: t0, T: now}
	}
	return out, nil
}

func (s *Service) simCells() {
	now := s.now()
	m := map[string]map[string]Cell{"bybit": {}, "okx": {}, "hyperliquid": {}}
	for i, coin := range s.Coins {
		p, ok := s.Sim(coin + "USDT")
		if !ok {
			continue
		}
		w := float64(now/120000%7) - 3
		for j, v := range []string{"bybit", "okx", "hyperliquid"} {
			rate := 0.0001 + float64(j+1)*w*0.00001 + float64(i)*0.00001
			r8 := per8h(rate, 8)
			m[v][coin] = Cell{Mark: p * (1 + (float64(j)-0.5)*0.0006 + w*0.00004), Rate: rate, Hours: 8, Rate8h: r8, T: now}
		}
	}
	s.mu.Lock()
	s.remote = m
	s.mu.Unlock()
}

// closes 一个交易所最近 6 小时的 1 分钟标记价收盘（Hyperliquid 只有成交价 K 线）。
func (s *Service) closes(ctx context.Context, venue, coin string) map[int64]float64 {
	out := map[int64]float64{}
	now := s.now()
	var rows [][]json.RawMessage
	switch venue {
	case "binance":
		if s.get(ctx, "https://fapi.binance.com/fapi/v1/markPriceKlines?interval=1m&limit=360&symbol="+coin+"USDT", &rows) != nil {
			return out
		}
	case "bybit":
		var r struct {
			Result struct {
				List [][]json.RawMessage `json:"list"`
			} `json:"result"`
		}
		if s.get(ctx, "https://api.bybit.com/v5/market/mark-price-kline?category=linear&interval=1&limit=360&symbol="+coin+"USDT", &r) != nil {
			return out
		}
		rows = r.Result.List
	case "okx":
		var r struct {
			Data [][]json.RawMessage `json:"data"`
		}
		if s.get(ctx, "https://www.okx.com/api/v5/market/mark-price-candles?bar=1m&limit=300&instId="+coin+"-USDT-SWAP", &r) != nil {
			return out
		}
		rows = r.Data
	case "hyperliquid":
		hc := s.HTTP
		if hc == nil {
			hc = &http.Client{Timeout: 10 * time.Second}
		}
		body := fmt.Sprintf(`{"type":"candleSnapshot","req":{"coin":%q,"interval":"1m","startTime":%d,"endTime":%d}}`, coin, now-6*3_600_000, now)
		req, err := http.NewRequestWithContext(ctx, "POST", "https://api.hyperliquid.xyz/info", strings.NewReader(body))
		if err != nil {
			return out
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := hc.Do(req)
		if err != nil {
			return out
		}
		defer resp.Body.Close()
		// 返回里同时有 t（开始）和 T（结束），两个都要声明，不然 T 会盖掉 t
		var list []struct {
			T   int64  `json:"t"`
			End int64  `json:"T"`
			C   string `json:"c"`
		}
		if json.NewDecoder(resp.Body).Decode(&list) == nil {
			for _, x := range list {
				out[x.T] = f64(x.C)
			}
		}
		return out
	}
	for _, r := range rows {
		if len(r) < 5 {
			continue
		}
		var t int64
		if json.Unmarshal(r[0], &t) != nil { // Bybit、OKX 的时间是字符串
			var ts string
			if json.Unmarshal(r[0], &ts) != nil {
				continue
			}
			t, _ = strconv.ParseInt(ts, 10, 64)
		}
		var c string
		if json.Unmarshal(r[4], &c) == nil {
			out[t] = f64(c)
		}
	}
	return out
}

// backfill 开机时从公开 K 线补最近 6 小时，图不用从空的开始攒。
func (s *Service) backfill(ctx context.Context) {
	for _, coin := range s.Coins {
		base := s.closes(ctx, "binance", coin)
		if len(base) == 0 {
			continue
		}
		rows := map[int64]map[string]float64{}
		for _, v := range Venues[1:] {
			if v == "hyperliquid" && !s.HL {
				continue
			}
			for t, c := range s.closes(ctx, v, coin) {
				b := base[t]
				if b <= 0 || c <= 0 {
					continue
				}
				if rows[t] == nil {
					rows[t] = map[string]float64{}
				}
				rows[t][v] = (c - b) / b * 1e4
			}
		}
		ts := make([]int64, 0, len(rows))
		for t := range rows {
			ts = append(ts, t)
		}
		sort.Slice(ts, func(i, j int) bool { return ts[i] < ts[j] })
		pts := make([]point, len(ts))
		for i, t := range ts {
			pts[i] = point{T: t, V: rows[t]}
		}
		s.mu.Lock()
		// 补的放前面，已经攒下的实时点放后面
		var live []point
		if h := s.hist[coin]; len(h) > 0 && len(ts) > 0 {
			for _, p := range h {
				if p.T > ts[len(ts)-1] {
					live = append(live, p)
				}
			}
		}
		if s.hist == nil {
			s.hist = map[string][]point{}
		}
		s.hist[coin] = append(pts, live...)
		if len(ts) > 0 && (s.since == 0 || ts[0] < s.since) {
			s.since = ts[0]
		}
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// Run 一直跑。
func (s *Service) Run(ctx context.Context) {
	sample := time.NewTicker(10 * time.Second)
	poll := time.NewTicker(time.Minute)
	defer sample.Stop()
	defer poll.Stop()
	s.mu.Lock()
	s.remote = map[string]map[string]Cell{}
	s.errs = map[string]string{}
	s.mu.Unlock()
	fetch := func() {
		if s.Sim != nil {
			s.simCells()
			return
		}
		for name, fn := range map[string]func(context.Context) (map[string]Cell, error){"bybit": s.pollBybit, "okx": s.pollOKX} {
			c, cancel := context.WithTimeout(ctx, 30*time.Second)
			t0 := time.Now()
			cells, err := fn(c)
			ms := float64(time.Since(t0).Microseconds()) / 1000
			cancel()
			s.mu.Lock()
			if s.polls == nil {
				s.polls = map[string]PollStat{}
			}
			st := s.polls[name]
			st.Ms, st.OK = ms, err == nil
			if err != nil {
				s.errs[name] = err.Error()
			} else {
				s.remote[name] = cells
				delete(s.errs, name)
				st.At = s.now()
			}
			s.polls[name] = st
			s.mu.Unlock()
		}
	}
	fetch()
	s.sample()
	if s.Sim == nil {
		go s.backfill(ctx)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-sample.C:
			if s.Sim != nil { // 模拟行情开机时还没有价格，等它有了马上算
				s.simCells()
			}
			s.sample()
		case <-poll.C:
			fetch()
		}
	}
}

// Routes 挂接口。
func (s *Service) Routes(mux *http.ServeMux) {
	wj := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("GET /api/venues/matrix", func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		missing := []string{}
		for k := range s.errs {
			missing = append(missing, k)
		}
		s.mu.Unlock()
		sort.Strings(missing)
		wj(w, map[string]any{"venues": Venues, "rows": s.Matrix(), "missing": missing})
	})
	mux.HandleFunc("GET /api/venues/spread", func(w http.ResponseWriter, q *http.Request) {
		wj(w, s.Spread(strings.ToUpper(q.URL.Query().Get("coin"))))
	})
}
