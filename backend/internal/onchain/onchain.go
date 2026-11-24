// Package onchain 扫描 Hyperliquid 上大户的真实持仓（D33、D35 第 2、3 项：链上爆仓地图、大户追踪）。
//
// Hyperliquid 的每个地址的持仓、开仓均价、强平价都是公开的。做法：
//  1. 每 6 小时从排行榜取地址：账户价值最大的前 N 个 + 历史总盈利最多的前 300 个（"长期赚钱的地址"）
//  2. 按限速逐个查 clearinghouseState（官方限额约每分钟 600 次，这里用每秒 6 次）
//  3. 和上一轮比较，产生"开仓、加仓、减仓、平仓、反手"事件
//
// 爆仓地图用的是链上真实的强平价，不是估算（Coinglass 是模型估算）。
// 但只覆盖扫描到的地址，所以接口里同时给出覆盖率，界面上要如实标出。
// 这里的数字只用于展示和分析，不进账本，所以用 float64。
package onchain

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Pos struct {
	Coin   string  `json:"coin"`
	Size   float64 `json:"size"` // 带符号：正多负空（币的数量）
	Entry  float64 `json:"entry"`
	Value  float64 `json:"value"` // 名义价值（美元）
	UPnL   float64 `json:"upnl"`
	LiqPx  float64 `json:"liq_px"` // 0 = 没有强平价（保证金很足）
	Lev    int     `json:"leverage"`
	Cross  bool    `json:"cross"`
	Funded float64 `json:"funding_since_open"` // 开仓以来付出的资金费（正 = 付出）
}

type Account struct {
	Address     string  `json:"address"`
	Value       float64 `json:"account_value"`
	AllTimePnL  float64 `json:"all_time_pnl"`
	MonthPnL    float64 `json:"month_pnl"`
	Smart       bool    `json:"smart_money"` // 历史总盈利排前列
	RankByValue int     `json:"rank_by_value"`
}

type Event struct {
	Time    int64   `json:"time"`
	Address string  `json:"address"`
	Coin    string  `json:"coin"`
	Kind    string  `json:"kind"` // open | add | reduce | close | flip
	From    float64 `json:"from_size"`
	To      float64 `json:"to_size"`
	Price   float64 `json:"price"` // 新的开仓均价（平仓时为原均价）
	Value   float64 `json:"value"`
	Smart   bool    `json:"smart_money"`
	Lag     int64   `json:"lag_ms"` // 数据延迟：两次扫描的间隔，这个动作发生在这段时间里的某一刻
}

type Scanner struct {
	InfoURL        string // 默认 https://api.hyperliquid.xyz/info
	LeaderboardURL string // 默认 https://stats-data.hyperliquid.xyz/Mainnet/leaderboard
	TopByValue     int    // 默认 1500
	TopByPnL       int    // 默认 300
	PerSecond      float64
	Log            *slog.Logger
	HTTP           *http.Client

	mu       sync.RWMutex
	accounts map[string]Account
	pos      map[string][]Pos // 地址 → 持仓
	scanned  map[string]int64 // 地址 → 上次扫描时间
	events   []Event
	cycle    int
	cycleEnd int64
	oi       map[string]float64 // 每个币的总持仓量（币），算覆盖率用
	PrioN    int                // 刷新频率提高的地址数
	pool     *pgxpool.Pool
}

func (s *Scanner) defaults() {
	if s.InfoURL == "" {
		s.InfoURL = "https://api.hyperliquid.xyz/info"
	}
	if s.LeaderboardURL == "" {
		s.LeaderboardURL = "https://stats-data.hyperliquid.xyz/Mainnet/leaderboard"
	}
	if s.TopByValue == 0 {
		s.TopByValue = 1500
	}
	if s.TopByPnL == 0 {
		s.TopByPnL = 300
	}
	if s.PerSecond == 0 {
		s.PerSecond = 6
	}
	if s.HTTP == nil {
		s.HTTP = &http.Client{Timeout: 60 * time.Second}
	}
	if s.accounts == nil {
		s.accounts, s.pos, s.scanned = map[string]Account{}, map[string][]Pos{}, map[string]int64{}
	}
}

// Run 一直运行到 ctx 结束。
func (s *Scanner) Run(ctx context.Context) {
	s.defaults()
	var lbAt, oiAt time.Time
	var lbBusy, lbFail atomic.Bool
	tick := time.NewTicker(time.Duration(float64(time.Second) / s.PerSecond))
	defer tick.Stop()
	for ctx.Err() == nil {
		if f := lbFail.Swap(false); f { // 后台刷新失败：半小时后再试
			lbAt = time.Now().Add(-6*time.Hour + 30*time.Minute)
		}
		if time.Since(lbAt) > 6*time.Hour && !lbBusy.Load() {
			s.mu.RLock()
			have := len(s.accounts) > 0 // 上次存盘恢复的也算
			s.mu.RUnlock()
			if have {
				// 已经有一份名单：新名单放到后台下载，扫描不停（11-18：40MB 的名单网慢时要四五分钟甚至超时，
				// 原来在这里同步下载，这段时间一个地址都不扫；下载失败时原来还会一直卡着重试，大户提醒一条都发不出来）
				lbAt = time.Now()
				lbBusy.Store(true)
				go func() {
					defer lbBusy.Store(false)
					if err := s.loadLeaderboard(ctx); err != nil && ctx.Err() == nil {
						s.Log.Warn("onchain leaderboard", "err", err)
						lbFail.Store(true)
					}
				}()
			} else if err := s.loadLeaderboard(ctx); err != nil { // 还没有名单，没东西可扫：一分钟后再试
				s.Log.Warn("onchain leaderboard", "err", err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Minute):
				}
				continue
			} else {
				lbAt = time.Now()
			}
		}
		// 持仓金额最大的一批每 3 次查询占 1 次，大约一分钟一轮；其余的扫完一遍算一轮
		prio, rest := s.queues()
		pi := 0
		for i, ri := 0, 0; ri < len(rest) && ctx.Err() == nil; i++ {
			if time.Since(oiAt) > 5*time.Minute {
				if err := s.loadOI(ctx); err != nil {
					s.Log.Warn("onchain oi", "err", err)
				}
				oiAt = time.Now()
			}
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			var a string
			if i%3 == 0 && len(prio) > 0 {
				a = prio[pi%len(prio)]
				pi++
			} else {
				a = rest[ri]
				ri++
			}
			ps, err := s.fetch(ctx, a)
			if err != nil {
				continue
			}
			s.update(a, ps)
		}
		s.mu.Lock()
		s.cycle++
		s.cycleEnd = time.Now().UnixMilli()
		s.mu.Unlock()
		s.Log.Info("onchain cycle", "addresses", len(prio)+len(rest), "cycle", s.cycle)
	}
}

func f64(s string) float64 {
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

func (s *Scanner) loadLeaderboard(ctx context.Context) error {
	req, _ := http.NewRequestWithContext(ctx, "GET", s.LeaderboardURL, nil)
	// 榜单有 40MB（不压缩），网慢时（150KB/s）要四五分钟；共用的 60 秒超时永远下不完（11-17）
	c := *s.HTTP
	c.Timeout = 15 * time.Minute
	res, err := c.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	var lb struct {
		Rows []struct {
			Addr  string               `json:"ethAddress"`
			Value string               `json:"accountValue"`
			Perf  [][2]json.RawMessage `json:"windowPerformances"`
		} `json:"leaderboardRows"`
	}
	if err := json.NewDecoder(res.Body).Decode(&lb); err != nil {
		return err
	}
	accs := make([]Account, 0, len(lb.Rows))
	for _, r := range lb.Rows {
		a := Account{Address: strings.ToLower(r.Addr), Value: f64(r.Value)}
		for _, w := range r.Perf {
			var name string
			var p struct {
				PnL string `json:"pnl"`
			}
			if json.Unmarshal(w[0], &name) != nil || json.Unmarshal(w[1], &p) != nil {
				continue
			}
			switch name {
			case "allTime":
				a.AllTimePnL = f64(p.PnL)
			case "month":
				a.MonthPnL = f64(p.PnL)
			}
		}
		accs = append(accs, a)
	}
	pick := map[string]Account{}
	sort.Slice(accs, func(i, j int) bool { return accs[i].Value > accs[j].Value })
	for i := 0; i < len(accs) && i < s.TopByValue; i++ {
		accs[i].RankByValue = i + 1
		pick[accs[i].Address] = accs[i]
	}
	sort.Slice(accs, func(i, j int) bool { return accs[i].AllTimePnL > accs[j].AllTimePnL })
	for i := 0; i < len(accs) && i < s.TopByPnL; i++ {
		a := accs[i]
		if old, ok := pick[a.Address]; ok {
			a.RankByValue = old.RankByValue
		}
		a.Smart = true
		pick[a.Address] = a
	}
	s.mu.Lock()
	s.accounts = pick
	s.dropLeft()
	s.mu.Unlock()
	s.Log.Info("onchain leaderboard", "rows", len(lb.Rows), "tracking", len(pick))
	return nil
}

// dropLeft 跌出排行榜的地址不再扫了，它们的旧仓位也不能再算进强平地图（调用时持锁）。
func (s *Scanner) dropLeft() {
	for addr := range s.pos {
		if _, ok := s.accounts[addr]; !ok {
			delete(s.pos, addr)
			delete(s.scanned, addr)
		}
	}
}

func (s *Scanner) fetch(ctx context.Context, addr string) ([]Pos, error) {
	body, _ := json.Marshal(map[string]string{"type": "clearinghouseState", "user": addr})
	req, _ := http.NewRequestWithContext(ctx, "POST", s.InfoURL, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res, err := s.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("http %d", res.StatusCode)
	}
	var st struct {
		AssetPositions []struct {
			Position struct {
				Coin     string `json:"coin"`
				Szi      string `json:"szi"`
				EntryPx  string `json:"entryPx"`
				Value    string `json:"positionValue"`
				UPnL     string `json:"unrealizedPnl"`
				LiqPx    string `json:"liquidationPx"`
				Leverage struct {
					Type  string `json:"type"`
					Value int    `json:"value"`
				} `json:"leverage"`
				CumFunding struct {
					SinceOpen string `json:"sinceOpen"`
				} `json:"cumFunding"`
			} `json:"position"`
		} `json:"assetPositions"`
	}
	if err := json.NewDecoder(res.Body).Decode(&st); err != nil {
		return nil, err
	}
	out := make([]Pos, 0, len(st.AssetPositions))
	for _, ap := range st.AssetPositions {
		p := ap.Position
		out = append(out, Pos{Coin: p.Coin, Size: f64(p.Szi), Entry: f64(p.EntryPx), Value: f64(p.Value), UPnL: f64(p.UPnL),
			LiqPx: f64(p.LiqPx), Lev: p.Leverage.Value, Cross: p.Leverage.Type == "cross", Funded: f64(p.CumFunding.SinceOpen)})
	}
	return out, nil
}

// update 记录新持仓，并和上一轮比较产生事件（第一次扫描某个地址时不产生事件）。
func (s *Scanner) update(addr string, ps []Pos) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UnixMilli()
	prev, seen := s.scanned[addr]
	lag := now - prev
	if seen && lag <= maxEventGap {
		old := map[string]Pos{}
		for _, p := range s.pos[addr] {
			old[p.Coin] = p
		}
		smart := s.accounts[addr].Smart
		for _, p := range ps {
			o, had := old[p.Coin]
			delete(old, p.Coin)
			ev := Event{Time: now, Address: addr, Coin: p.Coin, From: o.Size, To: p.Size, Price: p.Entry, Value: p.Value, Smart: smart, Lag: lag}
			switch {
			case !had || o.Size == 0:
				ev.Kind = "open"
			case math.Signbit(o.Size) != math.Signbit(p.Size):
				ev.Kind = "flip"
			case math.Abs(p.Size) > math.Abs(o.Size)*1.1:
				ev.Kind = "add"
			case math.Abs(p.Size) < math.Abs(o.Size)*0.9:
				ev.Kind = "reduce"
			default:
				continue
			}
			s.events = append(s.events, ev)
		}
		for _, o := range old {
			s.events = append(s.events, Event{Time: now, Address: addr, Coin: o.Coin, Kind: "close", From: o.Size, Price: o.Entry, Value: o.Value, Smart: s.accounts[addr].Smart, Lag: lag})
		}
		if len(s.events) > 5000 {
			s.events = s.events[len(s.events)-5000:]
		}
	}
	s.pos[addr] = ps
	s.scanned[addr] = now
}

// ---------- 查询 ----------

type Bin struct {
	Lo       float64 `json:"lo"`
	Hi       float64 `json:"hi"`
	LongLiq  float64 `json:"long_value"`  // 这个价格区间里会被强平的多单名义价值（价格跌到这里）
	ShortLiq float64 `json:"short_value"` // 会被强平的空单名义价值（价格涨到这里）
	// 这一格里强平价按名义价值加权的平均值（图上聚集区的价格标签用它，而不是格子中点）
	LongPx    float64 `json:"long_px,omitempty"`
	ShortPx   float64 `json:"short_px,omitempty"`
	Positions int     `json:"positions"`
}

type LiqMap struct {
	Coin      string  `json:"coin"`
	Center    float64 `json:"center"`
	Bins      []Bin   `json:"bins"`
	Addresses int     `json:"addresses_scanned"`
	Positions int     `json:"positions"`
	LongValue float64 `json:"long_value"`
	ShortVal  float64 `json:"short_value"`
	NoLiq     float64 `json:"no_liq_value"` // 保证金足够、没有强平价的持仓
	CycleEnd  int64   `json:"cycle_end"`
	Cycle     int     `json:"cycle"`
	Coverage  float64 `json:"coverage"` // 扫描到的持仓占全部持仓量的比例；0 = 还不知道总量
	OI        float64 `json:"oi"`       // 全部持仓量（币）
}

// LiqMap 统计 center 上下 rangePct（如 0.15 = ±15%）范围里的强平价分布，大约 n 格。
// 格子画在固定网格上（格宽取 1/2/5×10^k 的整数，格边是格宽的整数倍），不跟着请求那一刻的价格挪：
// 否则两个人在不同时刻打开页面，同一批仓位会被切进不同的格子，图上的聚集区价格和金额就对不上。
func (s *Scanner) LiqMap(coin string, center, rangePct float64, n int) LiqMap {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m := LiqMap{Coin: coin, Center: center, Addresses: len(s.scanned), CycleEnd: s.cycleEnd, Cycle: s.cycle}
	m.Coverage, m.OI = s.coverageLocked(coin)
	if !(center > 0) || n <= 0 {
		return m
	}
	w := niceStep(2 * center * rangePct / float64(n))
	lo := math.Floor(center*(1-rangePct)/w) * w
	n = int(math.Ceil((center*(1+rangePct) - lo) / w))
	if n > 1000 {
		n = 1000
	}
	m.Bins = make([]Bin, n)
	for i := range m.Bins {
		m.Bins[i].Lo, m.Bins[i].Hi = lo+w*float64(i), lo+w*float64(i+1)
	}
	for _, ps := range s.pos {
		for _, p := range ps {
			if p.Coin != coin || p.Size == 0 {
				continue
			}
			m.Positions++
			if p.Size > 0 {
				m.LongValue += p.Value
			} else {
				m.ShortVal += p.Value
			}
			if p.LiqPx <= 0 {
				m.NoLiq += p.Value
				continue
			}
			i := int((p.LiqPx - lo) / w)
			if i < 0 || i >= n {
				continue
			}
			if p.Size > 0 {
				m.Bins[i].LongLiq += p.Value
				m.Bins[i].LongPx += p.Value * p.LiqPx
			} else {
				m.Bins[i].ShortLiq += p.Value
				m.Bins[i].ShortPx += p.Value * p.LiqPx
			}
			m.Bins[i].Positions++
		}
	}
	for i := range m.Bins {
		b := &m.Bins[i]
		if b.LongLiq > 0 {
			b.LongPx /= b.LongLiq
		}
		if b.ShortLiq > 0 {
			b.ShortPx /= b.ShortLiq
		}
	}
	return m
}

// niceStep 把格宽取成 1、2、5 乘 10 的整数次方（80.6 → 100，0.37 → 0.5）
func niceStep(x float64) float64 {
	if !(x > 0) {
		return 1
	}
	e := math.Pow(10, math.Floor(math.Log10(x)))
	switch m := x / e; {
	case m < 1.5:
		return e
	case m < 3.5:
		return 2 * e
	case m < 7.5:
		return 5 * e
	}
	return 10 * e
}

type Whale struct {
	Account
	Pos
}

// Whales 某个币上名义价值最大的 n 个持仓。
func (s *Scanner) Whales(coin string, n int) []Whale {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Whale
	for addr, ps := range s.pos {
		for _, p := range ps {
			if p.Coin == coin && p.Size != 0 {
				acc := s.accounts[addr]
				acc.Address = addr
				out = append(out, Whale{Account: acc, Pos: p})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Pos.Value > out[j].Pos.Value })
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// Events 最近的大户动作（新的在前）。coin 为空表示全部；minValue 过滤小额。
func (s *Scanner) Events(coin string, minValue float64, n int) []Event {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Event
	for i := len(s.events) - 1; i >= 0 && len(out) < n; i-- {
		e := s.events[i]
		if (coin == "" || e.Coin == coin) && e.Value >= minValue {
			out = append(out, e)
		}
	}
	return out
}

// Handler：/api/onchain/liqmap?coin=BTC&center=77000&range=0.15&bins=120
//
//	/api/onchain/whales?coin=BTC&n=50
//	/api/onchain/events?coin=BTC&min=100000&n=100
func (s *Scanner) Handler(price func(coin string) float64) http.Handler {
	mux := http.NewServeMux()
	q := func(r *http.Request, k string, d float64) float64 {
		if v, err := strconv.ParseFloat(r.URL.Query().Get(k), 64); err == nil && v > 0 {
			return v
		}
		return d
	}
	write := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("GET /api/onchain/liqmap", func(w http.ResponseWriter, r *http.Request) {
		coin := strings.ToUpper(r.URL.Query().Get("coin"))
		write(w, s.LiqMap(coin, q(r, "center", price(coin)), math.Min(q(r, "range", 0.15), 0.9), int(math.Min(q(r, "bins", 120), 1000))))
	})
	mux.HandleFunc("GET /api/onchain/whales", func(w http.ResponseWriter, r *http.Request) {
		write(w, s.Whales(strings.ToUpper(r.URL.Query().Get("coin")), int(math.Min(q(r, "n", 50), 500))))
	})
	mux.HandleFunc("GET /api/onchain/pnl", func(w http.ResponseWriter, r *http.Request) {
		write(w, s.PnLDistOf(strings.ToUpper(r.URL.Query().Get("coin"))))
	})
	mux.HandleFunc("GET /api/onchain/events", func(w http.ResponseWriter, r *http.Request) {
		write(w, s.Events(strings.ToUpper(r.URL.Query().Get("coin")), q(r, "min", 0), int(math.Min(q(r, "n", 100), 1000))))
	})
	return mux
}
