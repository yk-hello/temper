// Package scan 筛选器、异动扫描、今日（盘前准备和市场总览）。
//
// 数据都从现成的地方来：全市场方块图那份 24 小时行情（每 5 秒一次）；币安的全市场资金费（premiumIndex）、
// 持仓量历史、日线；恐惧贪婪指数（alternative.me）、全市场市值和 BTC 占比（CoinGecko）。都是公开接口，不要密钥。
//
// 异动（写进列表；同一个币同一种异动有冷却时间，见 cooldown）：
//   - 5 分钟涨跌 ≥ 2%（BTC、ETH ≥ 1%）
//   - 成交额到了前 2 天日均的 3 倍
//   - 资金费率 ≥ 0.1% 或 ≤ −0.1%（一期）
//   - 持仓量 1 小时变化 ≥ 10%
package scan

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"qs/internal/marketmap"
)

// Row 筛选器的一行。
type Row struct {
	S      string   `json:"s"`
	I      string   `json:"i"`
	Sec    string   `json:"sec,omitempty"`
	Px     float64  `json:"px"`
	Chg5m  *float64 `json:"chg5m"` // 刚启动、样本不够时为空
	Chg1h  *float64 `json:"chg1h"`
	Chg24h float64  `json:"chg24h"`
	QV     float64  `json:"qv"`
	RV     float64  `json:"rv,omitempty"`      // 成交额倍数
	Fund   *float64 `json:"funding,omitempty"` // 当期资金费率（小数）
	OI     *float64 `json:"oi,omitempty"`      // 持仓量（USDT）
	OI1h   *float64 `json:"oi1h,omitempty"`    // 1 小时变化（小数）
	OI24h  *float64 `json:"oi24h,omitempty"`
	F      bool     `json:"f,omitempty"`
}

// Event 一条异动。
type Event struct {
	T    int64   `json:"t"`
	S    string  `json:"s"`
	I    string  `json:"i"`
	Kind string  `json:"kind"` // up | down | volume | funding_hi | funding_lo | oi_up | oi_down
	V    float64 `json:"v"`    // 涨跌、倍数、费率、持仓量变化
	Px   float64 `json:"px"`
}

type sample struct {
	t  int64
	px float64
}

type oi struct {
	v, h1, h24 float64
	at         time.Time
}

// Service 筛选和异动。
type Service struct {
	Map func() marketmap.Snapshot
	Src Source
	Now func() time.Time
	// Pace 取持仓量时每个币之间停多久（默认 150 毫秒，远低于币安的频率限制）
	Pace time.Duration

	mu      sync.Mutex
	hist    map[string][]sample // 品种 → 最近 75 分钟的价格（每 15 秒一个）
	fund    map[string]float64  // 交易所代码 → 当期资金费率
	fundAt  time.Time
	ois     map[string]oi
	oiAt    time.Time
	seeded  bool // 开机后补过最近 75 分钟的价格（backfill）
	events  []Event
	last    map[string]int64 // 品种|种类 → 上次记异动的时间
	rows    []Row
	rowsAt  int64
	cache   map[string]cached
	started time.Time
	// 后台取外部数据、存数据库（warm.go）
	fetching map[string]bool
	tried    map[string]bool
	pool     *pgxpool.Pool
	savedAt  time.Time
}

type cached struct {
	at time.Time
	v  any
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Run 每 15 秒扫一遍。
func (s *Service) Run(ctx context.Context) {
	s.mu.Lock()
	s.started = s.now()
	s.mu.Unlock()
	go s.refreshLoop(ctx) // 外部数据后台取，打开页面不等（warm.go）
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		// 刚启动时方块图还没好：1 秒后再试，不等满 15 秒（原来重启后筛选要 12 秒才有）
		for !s.Step(ctx) {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
		s.persistScreen(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func sym(base string) string { return base + "USDT" }

// Step 扫一遍：记价格、取资金费和持仓量（各有自己的节奏）、算每一行、找异动。方块图还没好时返回 false。
func (s *Service) Step(ctx context.Context) bool {
	snap := s.Map()
	if snap.State == "loading" || len(snap.Nodes) == 0 {
		return false
	}
	now := s.now()
	nowMs := now.UnixMilli()
	s.mu.Lock()
	needFund := now.Sub(s.fundAt) > time.Minute
	needOI := now.Sub(s.oiAt) > 15*time.Minute
	s.mu.Unlock()
	if needFund && s.Src != nil {
		if f, err := s.Src.Funding(ctx); err == nil {
			s.mu.Lock()
			s.fund, s.fundAt = f, now
			s.mu.Unlock()
		}
	}
	if needOI && s.Src != nil {
		s.mu.Lock()
		s.oiAt = now
		s.mu.Unlock()
		go s.refreshOI(ctx, snap.Nodes)
	}
	s.mu.Lock()
	seed := !s.seeded && s.Src != nil
	s.seeded = true
	s.mu.Unlock()
	if seed {
		go s.backfill(ctx, snap.Nodes)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hist == nil {
		s.hist, s.last = map[string][]sample{}, map[string]int64{}
	}
	rows := make([]Row, 0, len(snap.Nodes))
	for _, n := range snap.Nodes {
		h := append(s.hist[n.I], sample{nowMs, n.Px})
		for len(h) > 0 && h[0].t < nowMs-75*60_000 {
			h = h[1:]
		}
		s.hist[n.I] = h
		r := Row{S: n.S, I: n.I, Sec: n.Sec, Px: n.Px, Chg24h: n.Chg, QV: n.QV, RV: n.RV, F: n.F}
		r.Chg5m = change(h, nowMs-5*60_000, n.Px)
		r.Chg1h = change(h, nowMs-60*60_000, n.Px)
		if f, ok := s.fund[sym(n.S)]; ok {
			r.Fund = &f
		}
		if o, ok := s.ois[sym(n.S)]; ok {
			v := o.v
			r.OI = &v
			if o.h1 > 0 {
				c := o.v/o.h1 - 1
				r.OI1h = &c
			}
			if o.h24 > 0 {
				c := o.v/o.h24 - 1
				r.OI24h = &c
			}
		}
		rows = append(rows, r)
		s.detect(r, nowMs)
	}
	s.rows, s.rowsAt = rows, nowMs
	return true
}

// change 从 since 那一刻（最近的一个样本）到现在的涨跌（%）。样本不够早时为空。
func change(h []sample, since int64, px float64) *float64 {
	if len(h) == 0 || h[0].t > since+20_000 {
		return nil
	}
	i := sort.Search(len(h), func(i int) bool { return h[i].t >= since })
	if i >= len(h) || h[i].px <= 0 {
		return nil
	}
	v := (px/h[i].px - 1) * 100
	return &v
}

// cooldown 同一个币同一种异动多久才再记一次：价格异动 30 分钟；持续的状态（放量、资金费、持仓量）记一次就够管一阵
var cooldown = map[string]int64{"up": 30, "down": 30, "volume": 360, "funding_hi": 480, "funding_lo": 480, "oi_up": 120, "oi_down": 120}

func (s *Service) emit(r Row, kind string, v float64, now int64) {
	k := r.I + "|" + kind
	if now-s.last[k] < cooldown[kind]*60_000 {
		return
	}
	s.last[k] = now
	// 刚启动的两分钟里看到的放量、资金费、持仓量是早就有的状态，不是刚发生的变化：记下来但不算异动
	if kind != "up" && kind != "down" && now-s.started.UnixMilli() < 2*60_000 {
		return
	}
	s.events = append(s.events, Event{T: now, S: r.S, I: r.I, Kind: kind, V: v, Px: r.Px})
	if len(s.events) > 300 {
		s.events = s.events[len(s.events)-300:]
	}
}

func (s *Service) detect(r Row, now int64) {
	lim := 2.0
	if r.S == "BTC" || r.S == "ETH" {
		lim = 1
	}
	if r.Chg5m != nil && *r.Chg5m >= lim {
		s.emit(r, "up", *r.Chg5m, now)
	}
	if r.Chg5m != nil && *r.Chg5m <= -lim {
		s.emit(r, "down", *r.Chg5m, now)
	}
	if r.RV >= 3 {
		s.emit(r, "volume", r.RV, now)
	}
	if r.Fund != nil && *r.Fund >= 0.001 {
		s.emit(r, "funding_hi", *r.Fund, now)
	}
	if r.Fund != nil && *r.Fund <= -0.001 {
		s.emit(r, "funding_lo", *r.Fund, now)
	}
	if r.OI1h != nil && *r.OI1h >= 0.10 {
		s.emit(r, "oi_up", *r.OI1h, now)
	}
	if r.OI1h != nil && *r.OI1h <= -0.10 {
		s.emit(r, "oi_down", *r.OI1h, now)
	}
}

// refreshOI 成交额前 40 的币（加上重点币）取持仓量：现在、1 小时前、24 小时前。
// backfill 开机时从币安补每个币最近 75 分钟的 1 分钟收盘价：5 分钟、1 小时涨跌开机就有，
// 不用等后端自己攒满一小时（11-16 走查：刚部署、每次重启升级后，筛选页"1 小时"那一列空一个小时）。只补还没攒到的那段。
func (s *Service) backfill(ctx context.Context, nodes []marketmap.Node) {
	list := append([]marketmap.Node(nil), nodes...)
	sort.Slice(list, func(i, j int) bool { return list[i].QV > list[j].QV })
	pace := s.Pace
	if pace <= 0 {
		pace = 150 * time.Millisecond
	}
	for _, n := range list {
		got, err := s.Src.Minutes(ctx, sym(n.S), 76)
		if err == nil && len(got) > 0 {
			s.mu.Lock()
			if s.hist == nil {
				s.hist, s.last = map[string][]sample{}, map[string]int64{}
			}
			h := s.hist[n.I]
			first := int64(1 << 62)
			if len(h) > 0 {
				first = h[0].t
			}
			old := make([]sample, 0, len(got)+len(h))
			for _, x := range got {
				if x.t < first-5_000 {
					old = append(old, x)
				}
			}
			s.hist[n.I] = append(old, h...)
			s.mu.Unlock()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(pace):
		}
	}
}

func (s *Service) refreshOI(ctx context.Context, nodes []marketmap.Node) {
	list := append([]marketmap.Node(nil), nodes...)
	sort.Slice(list, func(i, j int) bool { return list[i].QV > list[j].QV })
	got := map[string]oi{}
	pace := s.Pace
	if pace <= 0 {
		pace = 150 * time.Millisecond
	}
	for i, n := range list {
		if i >= 40 && !n.F {
			continue
		}
		v, h1, h24, err := s.Src.OpenInterest(ctx, sym(n.S))
		if err != nil {
			continue
		}
		got[sym(n.S)] = oi{v: v, h1: h1, h24: h24, at: s.now()}
		select {
		case <-ctx.Done():
			return
		case <-time.After(pace):
		}
	}
	s.mu.Lock()
	if s.ois == nil {
		s.ois = map[string]oi{}
	}
	for k, v := range got {
		s.ois[k] = v
	}
	s.mu.Unlock()
}

// Screen 筛选器的全部行和最近的异动（新的在前）。warm = 启动不到 5 分钟，短周期涨跌还没有。
func (s *Service) Screen() (rows []Row, events []Event, at int64, warm bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows = append([]Row(nil), s.rows...)
	events = make([]Event, len(s.events))
	for i, e := range s.events {
		events[len(s.events)-1-i] = e
	}
	return rows, events, s.rowsAt, s.now().Sub(s.started) < 5*time.Minute
}

// ---------- 今日 ----------

// Level 一个重点币今天要看的价位。
type Level struct {
	S         string  `json:"s"`
	I         string  `json:"i"`
	Px        float64 `json:"px"`
	PrevHigh  float64 `json:"prev_high"`
	PrevLow   float64 `json:"prev_low"`
	PrevClose float64 `json:"prev_close"`
	DayOpen   float64 `json:"day_open"`
	WeekOpen  float64 `json:"week_open"`
	WeekHigh  float64 `json:"week_high"`
	WeekLow   float64 `json:"week_low"`
}

// Breadth 全市场涨跌家数（方块图里的那些币）。
type Breadth struct {
	Up     int      `json:"up"`
	Down   int      `json:"down"`
	Flat   int      `json:"flat"`
	Median float64  `json:"median"`              // 24 小时涨跌的中位数（%）
	QV     float64  `json:"qv"`                  // 24 小时总成交额（USDT）
	FundW  *float64 `json:"funding_w,omitempty"` // 按成交额加权的平均资金费率
}

// Today 盘前准备和市场总览：一个接口拿全。拿不到的那一块是空的，界面写"暂时取不到"。
type Today struct {
	T       int64      `json:"t"`
	Fear    *FearGreed `json:"fear,omitempty"`
	Global  *Global    `json:"global,omitempty"`
	LSR     *LongShort `json:"lsr,omitempty"`
	Breadth Breadth    `json:"breadth"`
	Levels  []Level    `json:"levels"`
	Movers  []Row      `json:"movers"` // 24 小时涨跌最多的各 5 个
	Events  []Event    `json:"events"` // 最近 12 小时的异动
	// 场外和宏观（18 第 10.2 条）：取不到的是空的；Macro 没配 FRED 密钥时是空的，MacroOff = true
	Premium  *Premium    `json:"premium,omitempty"`
	IV       *ImpliedVol `json:"iv,omitempty"`
	Stable   *Stable     `json:"stable,omitempty"`
	Macro    []Macro     `json:"macro,omitempty"`
	MacroOff bool        `json:"macro_off,omitempty"`
	Missing  []string    `json:"missing,omitempty"`
}

func (s *Service) Today(ctx context.Context) Today {
	now := s.now()
	out := Today{T: now.UnixMilli(), Levels: []Level{}, Movers: []Row{}, Events: []Event{}}
	rows, events, _, _ := s.Screen()
	var chg []float64
	var fw, w float64
	for _, r := range rows {
		switch {
		case r.Chg24h > 0.05:
			out.Breadth.Up++
		case r.Chg24h < -0.05:
			out.Breadth.Down++
		default:
			out.Breadth.Flat++
		}
		chg = append(chg, r.Chg24h)
		out.Breadth.QV += r.QV
		if r.Fund != nil {
			fw += *r.Fund * r.QV
			w += r.QV
		}
	}
	if len(chg) > 0 {
		sort.Float64s(chg)
		out.Breadth.Median = chg[len(chg)/2]
	}
	if w > 0 {
		v := fw / w
		out.Breadth.FundW = &v
	}
	by := append([]Row(nil), rows...)
	sort.Slice(by, func(i, j int) bool { return by[i].Chg24h > by[j].Chg24h })
	for i := 0; i < len(by) && i < 5; i++ {
		out.Movers = append(out.Movers, by[i])
	}
	for i := len(by) - 1; i >= 0 && i >= len(by)-5 && i >= 5; i-- {
		out.Movers = append(out.Movers, by[i])
	}
	for _, e := range events {
		if e.T >= now.Add(-12*time.Hour).UnixMilli() {
			out.Events = append(out.Events, e)
		}
	}
	if s.Src == nil {
		out.Missing = []string{"fear", "global", "lsr", "levels"}
		return out
	}
	// 只读后台取好的（warm.go）：没取过的不写进 Missing，界面显示"加载中"；取过但没取到的才写"暂时取不到"
	get := func(key string) (any, bool) {
		v, ok, failed := s.peek(key)
		if !ok && failed {
			out.Missing = append(out.Missing, key)
		}
		return v, ok
	}
	if v, ok := get("fear"); ok {
		out.Fear = v.(*FearGreed)
	}
	if v, ok := get("global"); ok {
		out.Global = v.(*Global)
	}
	if v, ok := get("lsr"); ok {
		out.LSR = v.(*LongShort)
	}
	daysTried := false
	for _, r := range rows {
		if !r.F {
			continue
		}
		v, ok, failed := s.peek("days|" + r.S)
		daysTried = daysTried || ok || failed
		if !ok {
			continue
		}
		if l, ok := levels(r, v.([]Day), now); ok {
			out.Levels = append(out.Levels, l)
		}
	}
	if len(out.Levels) == 0 && daysTried {
		out.Missing = append(out.Missing, "levels")
	}
	if _, ok := s.Src.(Extras); ok {
		if v, ok := get("premium"); ok {
			out.Premium = v.(*Premium)
		}
		if v, ok := get("iv"); ok {
			out.IV = v.(*ImpliedVol)
		}
		if v, ok := get("stable"); ok {
			out.Stable = v.(*Stable)
		}
		if v, ok := get("macro"); ok {
			out.Macro, _ = v.([]Macro)
			out.MacroOff = out.Macro == nil
		}
	}
	sort.Slice(out.Levels, func(i, j int) bool { return focusRank(out.Levels[i].S) < focusRank(out.Levels[j].S) })
	return out
}

func focusRank(s string) int {
	for i, x := range []string{"BTC", "ETH", "SOL", "BNB", "XRP", "DOGE"} {
		if x == s {
			return i
		}
	}
	return 99
}

// levels 昨天（UTC）的高低收、今天开盘、这周（周一 UTC）开盘和这周的高低。
func levels(r Row, days []Day, now time.Time) (Level, bool) {
	today := now.UTC().Truncate(24 * time.Hour).UnixMilli()
	l := Level{S: r.S, I: r.I, Px: r.Px}
	wd := (int(now.UTC().Weekday()) + 6) % 7 // 周一 = 0
	monday := today - int64(wd)*86_400_000
	l.WeekLow = math.Inf(1)
	found := false
	for _, d := range days {
		switch {
		case d.T == today-86_400_000:
			l.PrevHigh, l.PrevLow, l.PrevClose = d.H, d.L, d.C
			found = true
		case d.T == today:
			l.DayOpen = d.O
		}
		if d.T == monday {
			l.WeekOpen = d.O
		}
		if d.T >= monday {
			l.WeekHigh = math.Max(l.WeekHigh, d.H)
			l.WeekLow = math.Min(l.WeekLow, d.L)
		}
	}
	if math.IsInf(l.WeekLow, 1) {
		l.WeekLow = 0
	}
	return l, found
}

// ---------- 接口 ----------

func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/scan", func(w http.ResponseWriter, _ *http.Request) {
		rows, events, at, warm := s.Screen()
		if rows == nil {
			rows = []Row{}
		}
		writeJSON(w, map[string]any{"t": at, "warm": warm, "rows": rows, "events": events})
	})
	mux.HandleFunc("GET /api/today", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		writeJSON(w, s.Today(ctx))
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

// Metrics 一个品种此刻的各项数字（自定义提醒条件用，18 第 10.4 条）。单位：px 是价格；chg5m、chg1h、chg24h、funding、oi1h、oi24h 是百分比；rv 是倍数。
// 还没算出来的那项不放（规则里用到它的条件算不满足）。
func (s *Service) Metrics(inst string) map[string]float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.rows {
		if r.I != inst {
			continue
		}
		m := map[string]float64{"px": r.Px, "chg24h": r.Chg24h}
		if r.Chg5m != nil {
			m["chg5m"] = *r.Chg5m
		}
		if r.Chg1h != nil {
			m["chg1h"] = *r.Chg1h
		}
		if r.Fund != nil {
			m["funding"] = *r.Fund * 100
		}
		if r.OI1h != nil {
			m["oi1h"] = *r.OI1h * 100
		}
		if r.OI24h != nil {
			m["oi24h"] = *r.OI24h * 100
		}
		if r.RV > 0 {
			m["rv"] = r.RV
		}
		return m
	}
	return nil
}
