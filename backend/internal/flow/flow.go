// Package flow 订单流（12 的 V.12–V.14）：持仓量变化拆解、多空比、实时爆仓流。足迹图的数据在 heat 包。
//
// 持仓量和多空比是币安公开的统计数据，隔 5 分钟一个点，币安只留最近 30 天。
// "新开多、新开空、空头回补、多头止损"是按价格方向和持仓量方向推断的，币安没有直接给这个数。
package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 四种变化。
const (
	NewLong    = "new_long"    // 价格涨、持仓量增：新开多
	NewShort   = "new_short"   // 价格跌、持仓量增：新开空
	ShortCover = "short_cover" // 价格涨、持仓量减：空头回补
	LongClose  = "long_close"  // 价格跌、持仓量减：多头止损
)

// OIRaw 币安的一个持仓量统计点。T 是统计时刻。
type OIRaw struct {
	T    int64
	Coin float64
	USD  float64
}

// Close 一根 K 线的开始时间和收盘价。
type Close struct {
	T int64
	C float64
}

// OIPoint 拆解后的一个周期。T 是这个周期的开始时间，和 K 线对齐。
type OIPoint struct {
	T     int64   `json:"t"`
	USD   float64 `json:"usd"`   // 周期结束时的持仓量（美元）
	Delta float64 `json:"delta"` // 持仓量变化（币的变化 × 价格，美元），排除价格本身涨跌的影响
	DP    float64 `json:"dp"`    // 价格变化（百分比）
	Kind  string  `json:"kind"`
}

// Decompose 拆解。raw 按时间升序，closes 是同周期的 K 线。
// 币安的统计时刻是周期结束的时刻，所以价格取"开始时间 = 统计时刻 - 周期"那根 K 线的收盘价。
func Decompose(raw []OIRaw, closes []Close, periodMs int64) []OIPoint {
	price := make(map[int64]float64, len(closes))
	for _, c := range closes {
		price[c.T] = c.C
	}
	out := make([]OIPoint, 0, len(raw))
	for i := 1; i < len(raw); i++ {
		a, b := raw[i-1], raw[i]
		pa, ok1 := price[a.T-periodMs]
		pb, ok2 := price[b.T-periodMs]
		if !ok1 || !ok2 || pa <= 0 || b.T-a.T != periodMs {
			continue
		}
		dCoin := b.Coin - a.Coin
		p := OIPoint{T: b.T - periodMs, USD: b.USD, Delta: dCoin * pb, DP: math.Round((pb-pa)/pa*1e6) / 1e4}
		up := pb >= pa
		switch {
		case dCoin > 0 && up:
			p.Kind = NewLong
		case dCoin > 0:
			p.Kind = NewShort
		case dCoin < 0 && up:
			p.Kind = ShortCover
		case dCoin < 0:
			p.Kind = LongClose
		}
		out = append(out, p)
	}
	return out
}

// periodOf K 线周期对应币安支持的统计周期。比 5 分钟短的用 5 分钟。
func periodOf(intervalMs int64) (string, int64) {
	table := []struct {
		s string
		m int64
	}{{"5m", 5}, {"15m", 15}, {"30m", 30}, {"1h", 60}, {"2h", 120}, {"4h", 240}, {"6h", 360}, {"12h", 720}, {"1d", 1440}}
	for _, x := range table {
		if intervalMs <= x.m*60000 {
			return x.s, x.m * 60000
		}
	}
	return "1d", 1440 * 60000
}

// Ratio 多空比。
type Ratio struct {
	Ratio float64 `json:"ratio"`
	Long  float64 `json:"long"`
	Short float64 `json:"short"`
	T     int64   `json:"t"`
}

// Ratios 账户多空比和头部账户持仓多空比。
type Ratios struct {
	Account Ratio `json:"account"`
	Top     Ratio `json:"top"`
}

// Fetcher 数据来源。
type Fetcher interface {
	OI(ctx context.Context, symbol, period string, limit int) ([]OIRaw, error)
	Closes(ctx context.Context, symbol, period string, limit int) ([]Close, error)
	Ratios(ctx context.Context, symbol string) (Ratios, error)
}

// ---------- 币安 ----------

// Binance 从币安取。
type Binance struct {
	Base string
	HTTP *http.Client
}

func (b *Binance) get(ctx context.Context, path string, out any) error {
	base := b.Base
	if base == "" {
		base = "https://fapi.binance.com"
	}
	hc := b.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 8 * time.Second}
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

func num(s string) float64 {
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

// OI 见 Fetcher。
func (b *Binance) OI(ctx context.Context, symbol, period string, limit int) ([]OIRaw, error) {
	var raw []struct {
		Coin string `json:"sumOpenInterest"`
		USD  string `json:"sumOpenInterestValue"`
		T    int64  `json:"timestamp"`
	}
	if err := b.get(ctx, fmt.Sprintf("/futures/data/openInterestHist?symbol=%s&period=%s&limit=%d", symbol, period, limit), &raw); err != nil {
		return nil, err
	}
	out := make([]OIRaw, len(raw))
	for i, x := range raw {
		out[i] = OIRaw{T: x.T, Coin: num(x.Coin), USD: num(x.USD)}
	}
	return out, nil
}

// Closes 见 Fetcher。
func (b *Binance) Closes(ctx context.Context, symbol, period string, limit int) ([]Close, error) {
	var raw [][]any
	if err := b.get(ctx, fmt.Sprintf("/fapi/v1/klines?symbol=%s&interval=%s&limit=%d", symbol, period, limit), &raw); err != nil {
		return nil, err
	}
	out := make([]Close, 0, len(raw))
	for _, k := range raw {
		if len(k) < 5 {
			continue
		}
		t, ok1 := k[0].(float64)
		c, ok2 := k[4].(string)
		if ok1 && ok2 {
			out = append(out, Close{T: int64(t), C: num(c)})
		}
	}
	return out, nil
}

// Ratios 见 Fetcher。
func (b *Binance) Ratios(ctx context.Context, symbol string) (Ratios, error) {
	type row struct {
		Ratio string `json:"longShortRatio"`
		Long  string `json:"longAccount"`
		Short string `json:"shortAccount"`
		T     int64  `json:"timestamp"`
	}
	one := func(path string) (Ratio, error) {
		var raw []row
		if err := b.get(ctx, fmt.Sprintf("/futures/data/%s?symbol=%s&period=5m&limit=1", path, symbol), &raw); err != nil {
			return Ratio{}, err
		}
		if len(raw) == 0 {
			return Ratio{}, fmt.Errorf("没有数据")
		}
		return Ratio{Ratio: num(raw[0].Ratio), Long: num(raw[0].Long), Short: num(raw[0].Short), T: raw[0].T}, nil
	}
	a, err := one("globalLongShortAccountRatio")
	if err != nil {
		return Ratios{}, err
	}
	t, err := one("topLongShortPositionRatio")
	if err != nil {
		return Ratios{}, err
	}
	return Ratios{Account: a, Top: t}, nil
}

// ---------- 模拟（只有测试环境用）----------

// Sim 确定性的假数据：同一个品种、同一个时刻每次取到的都一样。
type Sim struct{ Price func(symbol string) float64 }

func (s *Sim) walk(symbol string, t, step int64) (coin, price float64) {
	h := uint64(0)
	for _, c := range symbol {
		h = h*31 + uint64(c)
	}
	base := 70000.0
	if s.Price != nil {
		if p := s.Price(symbol); p > 0 {
			base = p
		}
	}
	x := float64(t / step)
	coin = 80000 + 3000*math.Sin(x/7+float64(h%13)) + 900*math.Sin(x/2.3)
	price = base * (1 + 0.004*math.Sin(x/5+float64(h%7)))
	return
}

// OI 见 Fetcher。
func (s *Sim) OI(_ context.Context, symbol, period string, limit int) ([]OIRaw, error) {
	_, ms := periodByName(period)
	end := time.Now().UnixMilli() / ms * ms
	out := make([]OIRaw, 0, limit)
	for i := limit - 1; i >= 0; i-- {
		t := end - int64(i)*ms
		c, p := s.walk(symbol, t, ms)
		out = append(out, OIRaw{T: t, Coin: c, USD: c * p})
	}
	return out, nil
}

// Closes 见 Fetcher。
func (s *Sim) Closes(_ context.Context, symbol, period string, limit int) ([]Close, error) {
	_, ms := periodByName(period)
	end := time.Now().UnixMilli() / ms * ms
	out := make([]Close, 0, limit+1)
	for i := limit; i >= 0; i-- {
		t := end - int64(i)*ms
		_, p := s.walk(symbol, t+ms, ms)
		out = append(out, Close{T: t, C: p})
	}
	return out, nil
}

// Ratios 见 Fetcher。
func (s *Sim) Ratios(_ context.Context, symbol string) (Ratios, error) {
	t := time.Now().UnixMilli()
	r := 1.2 + 0.3*math.Sin(float64(t/300000))
	long := r / (1 + r)
	return Ratios{Account: Ratio{Ratio: r, Long: long, Short: 1 - long, T: t}, Top: Ratio{Ratio: r * 0.8, Long: r * 0.8 / (1 + r*0.8), Short: 1 / (1 + r*0.8), T: t}}, nil
}

func periodByName(p string) (string, int64) {
	n, _ := strconv.Atoi(p[:len(p)-1])
	switch p[len(p)-1] {
	case 'h':
		return p, int64(n) * 3600_000
	case 'd':
		return p, int64(n) * 86400_000
	}
	return p, int64(n) * 60_000
}

// ---------- 爆仓流 ----------

// Liq 一笔强平。Side 是被强平的方向。
type Liq struct {
	T          int64   `json:"t"`
	Instrument string  `json:"instrument"`
	Side       string  `json:"side"`            // long = 多单被强平，short = 空单被强平
	Venue      string  `json:"venue,omitempty"` // binance | bybit | okx（18 第 10.2 条）
	P          float64 `json:"p"`
	Q          float64 `json:"q"`
	USD        float64 `json:"usd"`
}

// LiqSum 一段时间的合计。
type LiqSum struct {
	Long  float64 `json:"long"`
	Short float64 `json:"short"`
}

const liqKeep = 5000

// Service 持有爆仓流和缓存。
type Service struct {
	F     Fetcher
	mu    sync.Mutex
	liqs  []Liq
	since int64
	cache map[string]cached
}

type cached struct {
	at time.Time
	v  any
}

// New 新建。
func New(f Fetcher) *Service {
	return &Service{F: f, cache: map[string]cached{}, since: time.Now().UnixMilli()}
}

// AddLiquidation 收到一笔强平。
func (s *Service) AddLiquidation(l Liq) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.liqs = append(s.liqs, l)
	if len(s.liqs) > liqKeep {
		s.liqs = append([]Liq(nil), s.liqs[len(s.liqs)-liqKeep:]...)
	}
}

// Liquidations 最近的强平：新的在前，加最近 5 分钟和 1 小时的合计。
func (s *Service) Liquidations(inst string, now int64, limit int) (items []Liq, m5, h1 LiqSum, since int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items = []Liq{}
	for i := len(s.liqs) - 1; i >= 0; i-- {
		l := s.liqs[i]
		if inst != "" && l.Instrument != inst {
			continue
		}
		if len(items) < limit {
			items = append(items, l)
		}
		add := func(x *LiqSum) {
			if l.Side == "long" {
				x.Long += l.USD
			} else {
				x.Short += l.USD
			}
		}
		if now-l.T <= 300_000 {
			add(&m5)
		}
		if now-l.T <= 3600_000 {
			add(&h1)
		} else {
			break
		}
	}
	return items, m5, h1, s.since
}

// memo 取缓存：币安这些统计 5 分钟才更新一个点，没必要每次都去问。
func (s *Service) memo(key string, ttl time.Duration, fn func() (any, error)) (any, error) {
	s.mu.Lock()
	c, ok := s.cache[key]
	s.mu.Unlock()
	if ok && time.Since(c.at) < ttl {
		return c.v, nil
	}
	v, err := fn()
	if err != nil {
		if ok {
			return c.v, nil // 取不到就先用旧的
		}
		return nil, err
	}
	s.mu.Lock()
	s.cache[key] = cached{time.Now(), v}
	s.mu.Unlock()
	return v, nil
}

func symOf(inst string) string { return strings.TrimSuffix(inst, "-PERP") + "USDT" }

// OIFlow 持仓量变化拆解。
func (s *Service) OIFlow(ctx context.Context, inst string, intervalMs int64) (any, error) {
	period, ms := periodOf(intervalMs)
	return s.memo("oi:"+inst+":"+period, time.Minute, func() (any, error) {
		sym := symOf(inst)
		raw, err := s.F.OI(ctx, sym, period, 300)
		if err != nil {
			return nil, err
		}
		closes, err := s.F.Closes(ctx, sym, period, 302)
		if err != nil {
			return nil, err
		}
		sort.Slice(raw, func(i, j int) bool { return raw[i].T < raw[j].T })
		return map[string]any{"period_ms": ms, "points": Decompose(raw, closes, ms)}, nil
	})
}

// Routes 挂接口。公开数据，不需要登录。
func (s *Service) Routes(mux *http.ServeMux) {
	js := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	// 持仓量、多空比只有合约有：现货、不认识的品种直接说清楚（400），不去问币安再回 502（11-16 走查）
	perpOnly := func(w http.ResponseWriter, inst string) bool {
		if strings.HasSuffix(inst, "-PERP") {
			return true
		}
		http.Error(w, `{"error":"PERP_ONLY"}`, 400)
		return false
	}
	mux.HandleFunc("GET /api/flow/oi", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if !perpOnly(w, q.Get("instrument")) {
			return
		}
		iv, _ := strconv.ParseInt(q.Get("interval"), 10, 64)
		c, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		v, err := s.OIFlow(c, q.Get("instrument"), iv)
		if err != nil {
			http.Error(w, `{"error":"OI_UNAVAILABLE"}`, 502)
			return
		}
		js(w, v)
	})
	mux.HandleFunc("GET /api/flow/ratio", func(w http.ResponseWriter, r *http.Request) {
		inst := r.URL.Query().Get("instrument")
		if !perpOnly(w, inst) {
			return
		}
		c, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		v, err := s.memo("ratio:"+inst, time.Minute, func() (any, error) { return s.F.Ratios(c, symOf(inst)) })
		if err != nil {
			http.Error(w, `{"error":"RATIO_UNAVAILABLE"}`, 502)
			return
		}
		js(w, v)
	})
	mux.HandleFunc("GET /api/flow/liquidations", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		limit, _ := strconv.Atoi(q.Get("limit"))
		if limit <= 0 || limit > 300 {
			limit = 100
		}
		items, m5, h1, since := s.Liquidations(q.Get("instrument"), time.Now().UnixMilli(), limit)
		js(w, map[string]any{"items": items, "m5": m5, "h1": h1, "since": since})
	})
}

// RunSim 测试环境：隔几秒造一笔强平。
func (s *Service) RunSim(ctx context.Context, instruments []string, price func(inst string) float64) {
	rnd := rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 7))
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(300+rnd.IntN(900)) * time.Millisecond):
		}
		inst := instruments[rnd.IntN(len(instruments))]
		p := price(inst)
		if p <= 0 {
			continue
		}
		side := "long"
		if rnd.IntN(2) == 0 {
			side = "short"
		}
		usd := 2000 + rnd.ExpFloat64()*60000
		s.AddLiquidation(Liq{T: time.Now().UnixMilli(), Instrument: inst, Side: side, P: p, Q: usd / p, USD: usd})
	}
}
