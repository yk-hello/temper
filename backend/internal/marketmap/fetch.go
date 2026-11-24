package marketmap

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"math/rand/v2"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Binance 币安 U 本位合约的公开接口（不用密钥）。
type Binance struct {
	BaseURL string // 默认 https://fapi.binance.com
}

func (b *Binance) base() string {
	if b.BaseURL != "" {
		return b.BaseURL
	}
	return "https://fapi.binance.com"
}

func getJSON(ctx context.Context, url string, v any) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("%s: HTTP %d", url, res.StatusCode)
	}
	return json.NewDecoder(res.Body).Decode(v)
}

func (b *Binance) Tickers(ctx context.Context) ([]Ticker, error) {
	var raw []struct {
		Symbol      string `json:"symbol"`
		LastPrice   string `json:"lastPrice"`
		ChangePct   string `json:"priceChangePercent"`
		QuoteVolume string `json:"quoteVolume"`
	}
	if err := getJSON(ctx, b.base()+"/fapi/v1/ticker/24hr", &raw); err != nil {
		return nil, err
	}
	out := make([]Ticker, 0, len(raw))
	for _, x := range raw {
		last, _ := strconv.ParseFloat(x.LastPrice, 64)
		chg, _ := strconv.ParseFloat(x.ChangePct, 64)
		qv, _ := strconv.ParseFloat(x.QuoteVolume, 64)
		out = append(out, Ticker{Symbol: x.Symbol, Last: last, ChgPct: chg, QuoteVol: qv})
	}
	return out, nil
}

// Closes 最近 n 根 1 小时收盘价（风险页算联动系数用）。
func (b *Binance) Closes(ctx context.Context, symbol string, n int) ([]float64, error) {
	var raw [][]any
	if err := getJSON(ctx, fmt.Sprintf("%s/fapi/v1/klines?symbol=%s&interval=1h&limit=%d", b.base(), symbol, n), &raw); err != nil {
		return nil, err
	}
	out := make([]float64, 0, len(raw))
	for _, k := range raw {
		if len(k) > 4 {
			if s, ok := k[4].(string); ok {
				c, _ := strconv.ParseFloat(s, 64)
				out = append(out, c)
			}
		}
	}
	return out, nil
}

// Coins 币种目录和板块：只要 U 本位永续、正常交易、标的是币的。
// 同一个接口里还有股票、ETF、黄金原油（contractType 是 TRADIFI_PERPETUAL）、指数和盘前合约，都不算币。
func (b *Binance) Coins(ctx context.Context) ([]Coin, error) {
	var raw struct {
		Symbols []struct {
			Symbol       string   `json:"symbol"`
			ContractType string   `json:"contractType"`
			QuoteAsset   string   `json:"quoteAsset"`
			Status       string   `json:"status"`
			Underlying   string   `json:"underlyingType"`
			SubType      []string `json:"underlyingSubType"`
		} `json:"symbols"`
	}
	if err := getJSON(ctx, b.base()+"/fapi/v1/exchangeInfo", &raw); err != nil {
		return nil, err
	}
	out := make([]Coin, 0, len(raw.Symbols))
	for _, x := range raw.Symbols {
		if x.ContractType != "PERPETUAL" || x.QuoteAsset != "USDT" || x.Status != "TRADING" || x.Underlying != "COIN" {
			continue
		}
		c := Coin{Symbol: x.Symbol}
		if len(x.SubType) > 0 {
			c.Sector = x.SubType[0]
		}
		out = append(out, c)
	}
	return out, nil
}

// VolumeRatio 过去 72 根 1 小时线：最新 24 根的成交额 ÷ 前 48 根的日均。不到 72 根回 0。
func (b *Binance) VolumeRatio(ctx context.Context, symbol string) (float64, error) {
	var raw [][]any
	if err := getJSON(ctx, fmt.Sprintf("%s/fapi/v1/klines?symbol=%s&interval=1h&limit=72", b.base(), symbol), &raw); err != nil {
		return 0, err
	}
	if len(raw) < 72 {
		return 0, nil
	}
	var recent, before float64
	for i, k := range raw {
		if len(k) <= 7 {
			return 0, nil
		}
		s, _ := k[7].(string)
		v, _ := strconv.ParseFloat(s, 64)
		if i >= len(raw)-24 {
			recent += v
		} else {
			before += v
		}
	}
	if before <= 0 {
		return 0, nil
	}
	return math.Round(recent/(before/2)*100) / 100, nil
}

// SimPrice 模拟行情里某个代码的基准价：行情源和方块图用同一个价。
func SimPrice(symbol string) float64 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(symbol))
	v := float64(h.Sum32()%100000) / 100000
	return math.Pow(10, v*4-2) // 0.01 到 100
}

// simSectors 模拟行情的板块，和币安标的名字一样。
var simSectors = []string{"PoW", "Layer-1", "DeFi", "Meme", "AI", "Infrastructure", "Layer-2"}

// Sim 模拟的全市场：只在测试环境用（MARKET_SOURCE=sim）。分几个板块，同板块走势相关。
type Sim struct {
	Symbols []string                     // 必须包含的代码
	Price   func(string) (float64, bool) // 真实在跑的模拟品种的现价（和交易页一致）
	N       int

	mu    sync.Mutex
	init  bool
	syms  []string
	px    map[string]float64
	base  map[string]float64
	qv    map[string]float64
	group map[string]int
	seed  map[string]uint64
	rng   *rand.Rand
}

func (s *Sim) setup() {
	if s.init {
		return
	}
	s.init = true
	s.rng = rand.New(rand.NewPCG(7, 11))
	s.px, s.base, s.qv, s.group, s.seed = map[string]float64{}, map[string]float64{}, map[string]float64{}, map[string]int{}, map[string]uint64{}
	n := s.N
	if n <= 0 {
		n = 300
	}
	s.syms = append(s.syms, s.Symbols...)
	for i := 0; len(s.syms) < n; i++ {
		s.syms = append(s.syms, fmt.Sprintf("SIM%03dUSDT", i+1))
	}
	for i, sym := range s.syms {
		p := SimPrice(sym)
		s.px[sym], s.base[sym] = p, p
		s.qv[sym] = math.Exp(s.rng.NormFloat64()*1.4) * 2e7
		s.group[sym] = i % 7
		h := fnv.New64a()
		_, _ = h.Write([]byte(sym))
		s.seed[sym] = h.Sum64()
	}
}

func (s *Sim) Tickers(context.Context) ([]Ticker, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setup()
	factors := make([]float64, 7)
	for i := range factors {
		factors[i] = s.rng.NormFloat64() * 0.0006
	}
	surge := s.syms[s.rng.IntN(len(s.syms))]
	out := make([]Ticker, 0, len(s.syms))
	for _, sym := range s.syms {
		r := factors[s.group[sym]] + s.rng.NormFloat64()*0.0004
		day := s.qv[sym]
		add := day * 5 / 86400
		if sym == surge {
			r = math.Abs(r) + 0.002
			add *= 30
		}
		s.px[sym] *= 1 + r
		s.qv[sym] = day + add
		last := s.px[sym]
		if s.Price != nil {
			if p, ok := s.Price(sym); ok {
				last = p
			}
		}
		out = append(out, Ticker{Symbol: sym, Last: last, ChgPct: (s.px[sym]/s.base[sym] - 1) * 100, QuoteVol: s.qv[sym]})
	}
	return out, nil
}

// Closes 模拟的 1 小时收盘价：同板块共用一个因子，所以走势相关。
func (s *Sim) Closes(_ context.Context, symbol string, n int) ([]float64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setup()
	market := rand.New(rand.NewPCG(99, 1))
	factor := rand.New(rand.NewPCG(uint64(s.group[symbol]+1), 2))
	own := rand.New(rand.NewPCG(s.seed[symbol], 3))
	out := make([]float64, n)
	p := s.base[symbol]
	for i := 0; i < n; i++ {
		p *= math.Exp(0.004*market.NormFloat64() + 0.008*factor.NormFloat64() + 0.005*own.NormFloat64())
		out[i] = p
	}
	return out, nil
}

// Coins 模拟目录：全部算币，板块按分组来。
func (s *Sim) Coins(context.Context) ([]Coin, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setup()
	out := make([]Coin, 0, len(s.syms))
	for _, sym := range s.syms {
		out = append(out, Coin{Symbol: sym, Sector: simSectors[s.group[sym]]})
	}
	return out, nil
}

// VolumeRatio 模拟的成交额倍数：每个代码固定一个值，0.5–3 倍。
func (s *Sim) VolumeRatio(_ context.Context, symbol string) (float64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setup()
	return 0.5 + float64(s.seed[symbol]%250)/100, nil
}

// Universe 模拟全市场的代码列表。
func (s *Sim) Universe() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setup()
	return append([]string(nil), s.syms...)
}

// OpenInterest 持仓量（按币），币安每 15 秒取一次就够。
type OpenInterest struct {
	BaseURL string
	mu      sync.Mutex
	cache   map[string]oiEntry
}

type oiEntry struct {
	at time.Time
	v  float64
}

// Value 持仓量（币数）。缓存 15 秒：所有人共用，不多打交易所。
func (o *OpenInterest) Value(ctx context.Context, symbol string) (float64, error) {
	o.mu.Lock()
	if e, ok := o.cache[symbol]; ok && time.Since(e.at) < 15*time.Second {
		o.mu.Unlock()
		return e.v, nil
	}
	o.mu.Unlock()
	base := o.BaseURL
	if base == "" {
		base = "https://fapi.binance.com"
	}
	var raw struct {
		OpenInterest string `json:"openInterest"`
	}
	if err := getJSON(ctx, base+"/fapi/v1/openInterest?symbol="+symbol, &raw); err != nil {
		return 0, err
	}
	v, err := strconv.ParseFloat(raw.OpenInterest, 64)
	if err != nil {
		return 0, err
	}
	o.mu.Lock()
	if o.cache == nil {
		o.cache = map[string]oiEntry{}
	}
	o.cache[symbol] = oiEntry{time.Now(), v}
	o.mu.Unlock()
	return v, nil
}
