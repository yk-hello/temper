package scan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Source 外部数据。正式用 Public（都是公开接口）；模拟行情时不设，界面写"模拟行情下没有"。
type Source interface {
	Funding(ctx context.Context) (map[string]float64, error)
	OpenInterest(ctx context.Context, symbol string) (now, h1, h24 float64, err error) // 持仓量（USDT）
	Days(ctx context.Context, symbol string, n int) ([]Day, error)
	FearGreed(ctx context.Context) (*FearGreed, error)
	Global(ctx context.Context) (*Global, error)
	LongShort(ctx context.Context, symbol string) (*LongShort, error)
	// Minutes 最近 n 根 1 分钟 K 线的收盘价（时间是收盘那一刻）。开机时补 5 分钟、1 小时涨跌用
	Minutes(ctx context.Context, symbol string, n int) ([]sample, error)
}

// Day 一根日线（UTC）。
type Day struct {
	T          int64
	O, H, L, C float64
}

// FearGreed 恐惧贪婪指数（alternative.me，每天更新一次）。
type FearGreed struct {
	Value   int    `json:"value"`
	Label   string `json:"label"` // extreme_fear | fear | neutral | greed | extreme_greed
	Prev    int    `json:"prev"`  // 昨天
	Week    int    `json:"week"`  // 一周前
	Updated int64  `json:"updated"`
	Source  string `json:"source"`
}

// Global 全市场市值（CoinGecko）。
type Global struct {
	MCap    float64 `json:"mcap"`
	MCapChg float64 `json:"mcap_chg"` // 24 小时（%）
	BTC     float64 `json:"btc"`      // BTC 占比（%）
	ETH     float64 `json:"eth"`
	Source  string  `json:"source"`
}

// LongShort 币安合约全市场账户多空比（BTC）。
type LongShort struct {
	Ratio float64 `json:"ratio"`
	Long  float64 `json:"long"` // 做多账户占比（0–1）
	Prev  float64 `json:"prev"` // 24 小时前的多空比
	T     int64   `json:"t"`
}

// Public 公开接口。
type Public struct {
	FAPI string // 空 = https://fapi.binance.com
	HTTP *http.Client
	// 场外数据（extra.go）：空 = 官方地址；测试时指到本机
	Spot, Coinbase, Deribit, Llama, FRED string
	// FREDKey 部署的人的 FRED 密钥：有了它显示美元指数、美债、标普
	FREDKey string
	keyMu   sync.RWMutex // 管理后台随时能换（2026-11-15）
}

const userAgent = "qs/1 (+self-hosted trading workstation)"

func (p *Public) fapi() string {
	if p.FAPI != "" {
		return p.FAPI
	}
	return "https://fapi.binance.com"
}

func (p *Public) get(ctx context.Context, url string, v any) error {
	c := p.HTTP
	if c == nil {
		c = &http.Client{Timeout: 15 * time.Second}
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent) // Coinbase 不认没有 User-Agent 的请求
	res, err := c.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("%s: HTTP %d", url, res.StatusCode)
	}
	return json.NewDecoder(res.Body).Decode(v)
}

func num(s string) float64 { v, _ := strconv.ParseFloat(s, 64); return v }

func (p *Public) Funding(ctx context.Context) (map[string]float64, error) {
	var raw []struct {
		Symbol string `json:"symbol"`
		Rate   string `json:"lastFundingRate"`
	}
	if err := p.get(ctx, p.fapi()+"/fapi/v1/premiumIndex", &raw); err != nil {
		return nil, err
	}
	out := make(map[string]float64, len(raw))
	for _, r := range raw {
		out[r.Symbol] = num(r.Rate)
	}
	return out, nil
}

func (p *Public) OpenInterest(ctx context.Context, symbol string) (now, h1, h24 float64, err error) {
	var raw []struct {
		Value string `json:"sumOpenInterestValue"`
		T     int64  `json:"timestamp"`
	}
	if err = p.get(ctx, p.fapi()+"/futures/data/openInterestHist?symbol="+symbol+"&period=1h&limit=25", &raw); err != nil {
		return
	}
	if len(raw) < 2 {
		return 0, 0, 0, errors.New("no data")
	}
	now = num(raw[len(raw)-1].Value)
	h1 = num(raw[len(raw)-2].Value)
	if len(raw) >= 25 {
		h24 = num(raw[0].Value)
	}
	return
}

func (p *Public) Minutes(ctx context.Context, symbol string, n int) ([]sample, error) {
	var raw [][]any
	if err := p.get(ctx, fmt.Sprintf("%s/fapi/v1/klines?symbol=%s&interval=1m&limit=%d", p.fapi(), symbol, n), &raw); err != nil {
		return nil, err
	}
	out := make([]sample, 0, len(raw))
	for _, r := range raw {
		if len(r) < 7 {
			continue
		}
		ct, _ := r[6].(float64)
		c, _ := r[4].(string)
		if px := num(c); px > 0 {
			out = append(out, sample{t: int64(ct) + 1, px: px})
		}
	}
	return out, nil
}

func (p *Public) Days(ctx context.Context, symbol string, n int) ([]Day, error) {
	var raw [][]any
	if err := p.get(ctx, fmt.Sprintf("%s/fapi/v1/klines?symbol=%s&interval=1d&limit=%d", p.fapi(), symbol, n), &raw); err != nil {
		return nil, err
	}
	out := make([]Day, 0, len(raw))
	for _, r := range raw {
		if len(r) < 5 {
			continue
		}
		t, _ := r[0].(float64)
		s := func(i int) float64 { x, _ := r[i].(string); return num(x) }
		out = append(out, Day{T: int64(t), O: s(1), H: s(2), L: s(3), C: s(4)})
	}
	return out, nil
}

func (p *Public) FearGreed(ctx context.Context) (*FearGreed, error) {
	var raw struct {
		Data []struct {
			Value string `json:"value"`
			Class string `json:"value_classification"`
			T     string `json:"timestamp"`
		} `json:"data"`
	}
	if err := p.get(ctx, "https://api.alternative.me/fng/?limit=8", &raw); err != nil {
		return nil, err
	}
	if len(raw.Data) == 0 {
		return nil, errors.New("no data")
	}
	v := func(i int) int {
		if i >= len(raw.Data) {
			return 0
		}
		n, _ := strconv.Atoi(raw.Data[i].Value)
		return n
	}
	t, _ := strconv.ParseInt(raw.Data[0].T, 10, 64)
	return &FearGreed{Value: v(0), Label: fgLabel(v(0)), Prev: v(1), Week: v(7), Updated: t * 1000, Source: "alternative.me"}, nil
}

// fgLabel 按 alternative.me 的分段：0–24 极度恐惧，25–44 恐惧，45–55 中性，56–75 贪婪，76–100 极度贪婪。
func fgLabel(v int) string {
	switch {
	case v < 25:
		return "extreme_fear"
	case v < 45:
		return "fear"
	case v <= 55:
		return "neutral"
	case v <= 75:
		return "greed"
	}
	return "extreme_greed"
}

func (p *Public) Global(ctx context.Context) (*Global, error) {
	var raw struct {
		Data struct {
			Total map[string]float64 `json:"total_market_cap"`
			Pct   map[string]float64 `json:"market_cap_percentage"`
			Chg   float64            `json:"market_cap_change_percentage_24h_usd"`
		} `json:"data"`
	}
	if err := p.get(ctx, "https://api.coingecko.com/api/v3/global", &raw); err != nil {
		return nil, err
	}
	if raw.Data.Total["usd"] <= 0 {
		return nil, errors.New("no data")
	}
	return &Global{MCap: raw.Data.Total["usd"], MCapChg: raw.Data.Chg, BTC: raw.Data.Pct["btc"], ETH: raw.Data.Pct["eth"], Source: "CoinGecko"}, nil
}

func (p *Public) LongShort(ctx context.Context, symbol string) (*LongShort, error) {
	var raw []struct {
		Ratio string `json:"longShortRatio"`
		Long  string `json:"longAccount"`
		T     int64  `json:"timestamp"`
	}
	if err := p.get(ctx, p.fapi()+"/futures/data/globalLongShortAccountRatio?symbol="+symbol+"&period=1h&limit=25", &raw); err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, errors.New("no data")
	}
	last := raw[len(raw)-1]
	return &LongShort{Ratio: num(last.Ratio), Long: num(last.Long), Prev: num(raw[0].Ratio), T: last.T}, nil
}

// SetFREDKey 换 FRED 密钥（管理后台）。
func (p *Public) SetFREDKey(k string) {
	p.keyMu.Lock()
	p.FREDKey = k
	p.keyMu.Unlock()
}

func (p *Public) fredKey() string {
	p.keyMu.RLock()
	defer p.keyMu.RUnlock()
	return p.FREDKey
}
