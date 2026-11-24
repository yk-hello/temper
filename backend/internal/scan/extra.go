package scan

// 今日页的场外和宏观数字（18 第 10.2 条，D102）。都是官方公开接口，不抓网页：
//   - Coinbase 溢价：Coinbase 的 BTC/USD 和币安现货 BTC/USDT 每小时收盘价的差（%），看美国这边的买盘。USDT 对美元的小偏差也算在里面
//   - 期权隐含波动：Deribit 的 DVOL（BTC、ETH），期权市场预期的年化波动（%）
//   - 稳定币总量：DefiLlama，所有锚定美元的稳定币流通量，和一天前、一周前比
//   - 美元指数、美债 10 年、标普 500：FRED（圣路易斯联储），要部署的人填免费的 FRED_API_KEY；前一个交易日收盘
// 只摆数字，不做买卖判断（D100）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Extras 可选的场外数据源（Public 实现了；测试的假数据源可以不实现）。
type Extras interface {
	Premium(ctx context.Context) (*Premium, error)
	ImpliedVol(ctx context.Context) (*ImpliedVol, error)
	Stable(ctx context.Context) (*Stable, error)
	Macro(ctx context.Context) ([]Macro, error) // 没配密钥时返回 nil, nil
}

// Premium Coinbase 溢价（%）。
type Premium struct {
	Now   float64   `json:"now"`
	Avg24 float64   `json:"avg24"`
	Hours []float64 `json:"hours"` // 最近 24 小时每小时一个，旧的在前
}

// ImpliedVol 期权隐含波动（Deribit DVOL，年化 %）。
type ImpliedVol struct {
	BTC     float64 `json:"btc"`
	BTCPrev float64 `json:"btc_prev"` // 24 小时前
	ETH     float64 `json:"eth"`
	ETHPrev float64 `json:"eth_prev"`
}

// Stable 锚定美元的稳定币流通量（美元）。
type Stable struct {
	Total float64 `json:"total"`
	Day   float64 `json:"day"`  // 比一天前多了多少
	Week  float64 `json:"week"` // 比一周前
}

// Macro 一个宏观数字（前一个交易日收盘）。
type Macro struct {
	ID    string  `json:"id"` // dxy | us10y | spx
	Value float64 `json:"value"`
	Prev  float64 `json:"prev"`
	Date  string  `json:"date"`
}

func (p *Public) base(v, def string) string {
	if v != "" {
		return strings.TrimRight(v, "/")
	}
	return def
}

func (p *Public) Premium(ctx context.Context) (*Premium, error) {
	var cb [][]float64 // [时间(秒), 低, 高, 开, 收, 量]，新的在前
	if err := p.get(ctx, p.base(p.Coinbase, "https://api.exchange.coinbase.com")+"/products/BTC-USD/candles?granularity=3600", &cb); err != nil {
		return nil, err
	}
	var bn [][]any // 币安现货 1 小时线，旧的在前
	if err := p.get(ctx, p.base(p.Spot, "https://api.binance.com")+"/api/v3/klines?symbol=BTCUSDT&interval=1h&limit=30", &bn); err != nil {
		return nil, err
	}
	closeAt := map[int64]float64{}
	for _, k := range bn {
		if len(k) < 5 {
			continue
		}
		t, _ := k[0].(float64)
		c, _ := k[4].(string)
		closeAt[int64(t)/1000] = num(c)
	}
	var hours []float64
	sort.Slice(cb, func(i, j int) bool { return cb[i][0] < cb[j][0] })
	for _, k := range cb {
		if len(k) < 5 {
			continue
		}
		if b := closeAt[int64(k[0])]; b > 0 {
			hours = append(hours, (k[4]-b)/b*100)
		}
	}
	if len(hours) == 0 {
		return nil, errors.New("no data")
	}
	if len(hours) > 24 {
		hours = hours[len(hours)-24:]
	}
	var sum float64
	for _, h := range hours {
		sum += h
	}
	return &Premium{Now: hours[len(hours)-1], Avg24: sum / float64(len(hours)), Hours: hours}, nil
}

func (p *Public) ImpliedVol(ctx context.Context) (*ImpliedVol, error) {
	end := time.Now().UnixMilli()
	one := func(cur string) (now, prev float64, err error) {
		var r struct {
			Result struct {
				Data [][]float64 `json:"data"` // [时间, 开, 高, 低, 收]
			} `json:"result"`
		}
		u := fmt.Sprintf("%s/api/v2/public/get_volatility_index_data?currency=%s&start_timestamp=%d&end_timestamp=%d&resolution=3600", p.base(p.Deribit, "https://www.deribit.com"), cur, end-26*3600_000, end)
		if err = p.get(ctx, u, &r); err != nil {
			return
		}
		d := r.Result.Data
		if len(d) == 0 || len(d[len(d)-1]) < 5 {
			return 0, 0, errors.New("no data")
		}
		sort.Slice(d, func(i, j int) bool { return d[i][0] < d[j][0] })
		now = d[len(d)-1][4]
		for _, row := range d { // 24 小时前那一根（没有就最早的）
			if len(row) >= 5 && row[0] <= float64(end-24*3600_000) {
				prev = row[4]
			}
		}
		if prev == 0 && len(d[0]) >= 5 {
			prev = d[0][4]
		}
		return
	}
	out := &ImpliedVol{}
	var err error
	if out.BTC, out.BTCPrev, err = one("BTC"); err != nil {
		return nil, err
	}
	out.ETH, out.ETHPrev, _ = one("ETH") // ETH 取不到就只显示 BTC
	return out, nil
}

func (p *Public) Stable(ctx context.Context) (*Stable, error) {
	var r struct {
		Assets []struct {
			PegType  string             `json:"pegType"`
			Now      map[string]float64 `json:"circulating"`
			PrevDay  map[string]float64 `json:"circulatingPrevDay"`
			PrevWeek map[string]float64 `json:"circulatingPrevWeek"`
		} `json:"peggedAssets"`
	}
	if err := p.get(ctx, p.base(p.Llama, "https://stablecoins.llama.fi")+"/stablecoins?includePrices=false", &r); err != nil {
		return nil, err
	}
	var now, day, week float64
	for _, a := range r.Assets {
		if a.PegType != "peggedUSD" {
			continue
		}
		now += a.Now["peggedUSD"]
		day += a.PrevDay["peggedUSD"]
		week += a.PrevWeek["peggedUSD"]
	}
	if now <= 0 {
		return nil, errors.New("no data")
	}
	return &Stable{Total: now, Day: now - day, Week: now - week}, nil
}

// fredSeries 美元指数用美联储的宽口径美元指数（DXY 是 ICE 的，没有免费接口）。
var fredSeries = []struct{ id, series string }{{"dxy", "DTWEXBGS"}, {"us10y", "DGS10"}, {"spx", "SP500"}}

func (p *Public) Macro(ctx context.Context) ([]Macro, error) {
	key := p.fredKey()
	if key == "" {
		return nil, nil
	}
	c := p.HTTP
	if c == nil {
		c = &http.Client{Timeout: 15 * time.Second}
	}
	var out []Macro
	for _, s := range fredSeries {
		q := url.Values{"series_id": {s.series}, "api_key": {key}, "file_type": {"json"}, "sort_order": {"desc"}, "limit": {"10"}}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, p.base(p.FRED, "https://api.stlouisfed.org")+"/fred/series/observations?"+q.Encode(), nil)
		req.Header.Set("User-Agent", userAgent)
		res, err := c.Do(req)
		if err != nil {
			return nil, errors.New(strings.ReplaceAll(err.Error(), key, "***")) // 错误里带着地址，地址里有密钥
		}
		var r struct {
			Obs []struct {
				Date  string `json:"date"`
				Value string `json:"value"`
			} `json:"observations"`
		}
		err = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&r)
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("FRED 回了 %d（检查 FRED_API_KEY）", res.StatusCode)
		}
		if err != nil {
			return nil, err
		}
		var vals []float64
		var date string
		for _, o := range r.Obs { // 新的在前；"." 是那天没数据（假日）
			v := num(o.Value)
			if o.Value == "." || math.IsNaN(v) || v == 0 {
				continue
			}
			if date == "" {
				date = o.Date
			}
			vals = append(vals, v)
			if len(vals) == 2 {
				break
			}
		}
		if len(vals) == 2 {
			out = append(out, Macro{ID: s.id, Value: vals[0], Prev: vals[1], Date: date})
		}
	}
	return out, nil
}
