// Package marketmap 给全市场方块图（10 的第 8 项、12 的 3.15、D79）算数据。
//
//   - 范围：币安 USDT 永续里真正的币。股票、ETF、黄金原油这些传统资产合约不算。
//   - 门槛：24 小时成交额不到 MinVol 的不显示，页面上写明总数和门槛。
//   - 板块：币安自己给每个币标的类别（underlyingSubType）。
//   - 面积用 24 小时成交额，颜色用 24 小时涨跌（界面算）。
//   - 成交额倍数：近 24 小时成交额 ÷ 之前 2 天的日均，每 30 分钟算一次。
package marketmap

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"qs/internal/binance"
)

// Ticker 一个币种的 24 小时行情。Symbol 是交易所代码，如 BTCUSDT。
type Ticker struct {
	Symbol   string
	Last     float64
	ChgPct   float64
	QuoteVol float64
}

// Coin 目录里的一个币：Sector 是交易所标的板块，没标时为空。
type Coin struct {
	Symbol string
	Sector string
}

// Fetcher 行情来源（币安；测试环境是模拟的）。
type Fetcher interface {
	Tickers(ctx context.Context) ([]Ticker, error)
	Coins(ctx context.Context) ([]Coin, error)
	// VolumeRatio 近 24 小时成交额 ÷ 之前 2 天的日均。上市不到 3 天的币回 0。
	VolumeRatio(ctx context.Context, symbol string) (float64, error)
}

// Node 方块图里的一块。
type Node struct {
	S   string  `json:"s"` // 币种，如 BTC
	I   string  `json:"i"` // 平台品种代码，如 BTC-PERP
	Px  float64 `json:"px"`
	Chg float64 `json:"chg"`          // 24 小时涨跌（%）
	QV  float64 `json:"qv"`           // 24 小时成交额（USDT）
	Sec string  `json:"sec"`          // 板块
	RV  float64 `json:"rv,omitempty"` // 成交额倍数，还没算出来时没有这个字段
	F   bool    `json:"f,omitempty"`  // 重点币种：行情常开，功能优先（D79）
}

// Snapshot 接口返回的内容。
type Snapshot struct {
	T      int64   `json:"t"`
	State  string  `json:"state"` // loading：目录或行情还没到；ok；stale：行情断了，显示最后一次的数据
	Source string  `json:"source"`
	Total  int     `json:"total"`   // 目录里一共多少个币
	MinVol float64 `json:"min_vol"` // 显示门槛（USDT）
	Nodes  []Node  `json:"nodes"`
	// 取不到时的原因（binance.Problem）：throttled / restricted / unreachable；没好的时候界面直接写出来，不再一直"正在加载"
	Problem string `json:"problem,omitempty"`
	WaitMin int    `json:"wait_min,omitempty"`
}

const (
	defaultMinVol = 10_000_000
	defaultTop    = 300
	tickerEvery   = 5 * time.Second
	coinsEvery    = time.Hour
	ratioEvery    = 30 * time.Minute
	staleAfter    = 30 * time.Second
)

type Service struct {
	F      Fetcher
	Source string
	MinVol float64                  // 24 小时成交额门槛，默认 1000 万 USDT
	Top    int                      // 最多显示多少个，默认 300
	Known  func(symbol string) bool // 只留能交易的品种；为空不过滤
	Focus  []string                 // 重点币种的交易所代码：不受成交额门槛限制
	Log    *slog.Logger
	Now    func() time.Time
	Pace   time.Duration // 取 K 线的间隔，默认 120 毫秒（一次 1 权重，远低于币安每分钟 2400）

	mu        sync.Mutex
	snap      Snapshot
	coins     map[string]string // 交易所代码 → 板块
	coinsAt   time.Time
	coinsBusy bool
	ratios    map[string]float64
	ratiosAt  time.Time
	ratioBusy bool
	lastOK    time.Time
	raw       []byte
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) minVol() float64 {
	if s.MinVol > 0 {
		return s.MinVol
	}
	return defaultMinVol
}

func baseOf(symbol string) string { return strings.TrimSuffix(symbol, "USDT") }

// Run 一直跑到 ctx 结束。
func (s *Service) Run(ctx context.Context) {
	s.mu.Lock()
	s.snap = Snapshot{State: "loading", Source: s.Source, MinVol: s.minVol(), Nodes: []Node{}}
	s.mu.Unlock()
	t := time.NewTicker(tickerEvery)
	defer t.Stop()
	s.step(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.step(ctx)
		}
	}
}

func (s *Service) step(ctx context.Context) {
	s.mu.Lock()
	needCoins := !s.coinsBusy && (s.coins == nil || s.now().Sub(s.coinsAt) > coinsEvery)
	if needCoins {
		s.coinsBusy = true
	}
	s.mu.Unlock()
	if needCoins {
		go s.refreshCoins(ctx)
	}
	list, err := s.F.Tickers(ctx)
	if err != nil {
		if s.Log != nil {
			s.Log.Warn("全市场：取行情失败", "err", err)
		}
		s.mu.Lock()
		if !s.lastOK.IsZero() && s.now().Sub(s.lastOK) > staleAfter {
			s.snap.State = "stale"
			s.raw = nil
		}
		s.mu.Unlock()
		return
	}
	s.Update(list)
	s.mu.Lock()
	needRatio := !s.ratioBusy && len(s.snap.Nodes) > 0 && (s.ratios == nil || s.now().Sub(s.ratiosAt) > ratioEvery)
	if needRatio {
		s.ratioBusy = true
	}
	syms := make([]string, 0, len(s.snap.Nodes))
	for _, n := range s.snap.Nodes {
		syms = append(syms, n.S+"USDT")
	}
	s.mu.Unlock()
	if needRatio {
		go s.refreshRatios(ctx, syms)
	}
}

func (s *Service) refreshCoins(ctx context.Context) {
	defer func() {
		s.mu.Lock()
		s.coinsBusy = false
		s.mu.Unlock()
	}()
	list, err := s.F.Coins(ctx)
	if err != nil || len(list) == 0 {
		if s.Log != nil {
			s.Log.Warn("全市场：取币种目录失败", "err", err)
		}
		return
	}
	m := make(map[string]string, len(list))
	for _, c := range list {
		m[c.Symbol] = c.Sector
	}
	s.mu.Lock()
	s.coins = m
	s.coinsAt = s.now()
	s.mu.Unlock()
}

func (s *Service) refreshRatios(ctx context.Context, syms []string) {
	defer func() {
		s.mu.Lock()
		s.ratioBusy = false
		s.mu.Unlock()
	}()
	pace := s.Pace
	if pace <= 0 {
		pace = 120 * time.Millisecond
	}
	m := make(map[string]float64, len(syms))
	for _, sym := range syms {
		select {
		case <-ctx.Done():
			return
		case <-time.After(pace):
		}
		if r, err := s.F.VolumeRatio(ctx, sym); err == nil && r > 0 {
			m[sym] = r
		}
	}
	s.mu.Lock()
	s.ratios = m
	s.ratiosAt = s.now()
	s.mu.Unlock()
}

// Update 收到一轮行情：按目录和门槛筛一遍，成交额大的排前面。目录没到时不显示，免得把股票也当成币。
func (s *Service) Update(list []Ticker) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snap.Source, s.snap.MinVol = s.Source, s.minVol()
	if s.coins == nil {
		s.snap.State = "loading"
		return
	}
	top := s.Top
	if top <= 0 {
		top = defaultTop
	}
	focus := make(map[string]bool, len(s.Focus))
	for _, x := range s.Focus {
		focus[x] = true
	}
	kept := make([]Ticker, 0, len(list))
	for _, t := range list {
		if _, ok := s.coins[t.Symbol]; !ok || t.Last <= 0 || (t.QuoteVol < s.minVol() && !focus[t.Symbol]) {
			continue
		}
		if s.Known != nil && !s.Known(t.Symbol) {
			continue
		}
		kept = append(kept, t)
	}
	sort.Slice(kept, func(i, j int) bool {
		if kept[i].QuoteVol != kept[j].QuoteVol {
			return kept[i].QuoteVol > kept[j].QuoteVol
		}
		return kept[i].Symbol < kept[j].Symbol
	})
	if len(kept) > top {
		kept = kept[:top]
	}
	nodes := make([]Node, len(kept))
	for i, t := range kept {
		b := baseOf(t.Symbol)
		nodes[i] = Node{S: b, I: b + "-PERP", Px: t.Last, Chg: t.ChgPct, QV: t.QuoteVol, Sec: s.coins[t.Symbol], RV: s.ratios[t.Symbol], F: focus[t.Symbol]}
	}
	now := s.now()
	s.snap.Nodes = nodes
	s.snap.Total = len(s.coins)
	s.snap.T = now.UnixMilli()
	s.snap.State = "ok"
	s.lastOK = now
	s.raw = nil
}

// Current 现在的内容。
func (s *Service) Current() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snap
}

// ServeHTTP 公开接口：GET /api/marketmap。行情断了也回最后一份，state 写 stale。
func (s *Service) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	if s.snap.Nodes == nil {
		s.snap = Snapshot{State: "loading", Source: s.Source, MinVol: s.minVol(), Nodes: []Node{}}
	}
	if s.raw == nil {
		s.raw, _ = json.Marshal(s.snap)
	}
	raw := s.raw
	if s.snap.State != "ok" && strings.HasPrefix(s.Source, "binance") {
		if kind, min := binance.Problem(); kind != "" {
			v := s.snap
			v.Problem, v.WaitMin = kind, min
			raw, _ = json.Marshal(v)
		}
	}
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(raw)
}
