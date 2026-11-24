package backtest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"qs/internal/trading"
)

// Bar 一根 1 分钟 K 线。
type Bar struct {
	T          int64
	O, H, L, C float64
	V          float64 // 成交量（币）
}

// Source 历史数据从哪来。正式用币安的公开接口（Binance）；测试用造的数据。
type Source interface {
	// Bars 1 分钟 K 线，mark = 标记价 K 线（强平、按标记价触发的止损止盈看它）
	Bars(ctx context.Context, symbol string, from, to int64, mark bool) ([]Bar, error)
	Funding(ctx context.Context, symbol string, from, to int64) ([]trading.FundingSettle, error)
}

// Binance 币安 U 本位合约的公开接口（不要密钥）。整天的数据记在内存里，同一段回测跑第二次不用再下载。
type Binance struct {
	Base string // 空 = https://fapi.binance.com
	HTTP *http.Client

	mu    sync.Mutex
	cache map[string][]Bar // symbol|mark|日期 → 那一天的 1440 根
}

const day = 24 * 60 * 60 * 1000

func (b *Binance) base() string {
	if b.Base != "" {
		return b.Base
	}
	return "https://fapi.binance.com"
}

func (b *Binance) Bars(ctx context.Context, symbol string, from, to int64, mark bool) ([]Bar, error) {
	var out []Bar
	for d := from / day * day; d < to; d += day {
		bars, err := b.day(ctx, symbol, d, mark)
		if err != nil {
			return nil, err
		}
		for _, x := range bars {
			if x.T >= from && x.T < to {
				out = append(out, x)
			}
		}
	}
	return out, nil
}

// day 一整天（UTC）的 1 分钟线：分两页取（一页最多 1500 根）。今天的不记（还没走完）。
func (b *Binance) day(ctx context.Context, symbol string, d int64, mark bool) ([]Bar, error) {
	key := fmt.Sprintf("%s|%v|%d", symbol, mark, d)
	b.mu.Lock()
	if v, ok := b.cache[key]; ok {
		b.mu.Unlock()
		return v, nil
	}
	b.mu.Unlock()
	var all []Bar
	start := d
	for start < d+day {
		page, err := b.klines(ctx, symbol, start, d+day-1, mark)
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			break
		}
		all = append(all, page...)
		start = page[len(page)-1].T + 60_000
	}
	if d+day <= time.Now().UnixMilli() {
		b.mu.Lock()
		if b.cache == nil {
			b.cache = map[string][]Bar{}
		}
		if len(b.cache) > 2000 { // 最多记约 2000 天·品种，多了整个清掉
			b.cache = map[string][]Bar{}
		}
		b.cache[key] = all
		b.mu.Unlock()
	}
	return all, nil
}

func (b *Binance) client() *http.Client {
	if b.HTTP != nil {
		return b.HTTP
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func (b *Binance) klines(ctx context.Context, symbol string, start, end int64, mark bool) ([]Bar, error) {
	path := "/fapi/v1/klines"
	if mark {
		path = "/fapi/v1/markPriceKlines"
	}
	q := url.Values{"symbol": {symbol}, "interval": {"1m"}, "startTime": {strconv.FormatInt(start, 10)}, "endTime": {strconv.FormatInt(end, 10)}, "limit": {"1500"}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, b.base()+path+"?"+q.Encode(), nil)
	res, err := b.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("klines %s: HTTP %d", symbol, res.StatusCode)
	}
	var rows [][]any
	if err := json.NewDecoder(res.Body).Decode(&rows); err != nil {
		return nil, err
	}
	out := make([]Bar, 0, len(rows))
	f := func(v any) float64 {
		s, _ := v.(string)
		x, _ := strconv.ParseFloat(s, 64)
		return x
	}
	for _, r := range rows {
		if len(r) < 6 {
			continue
		}
		t, _ := r[0].(float64)
		out = append(out, Bar{T: int64(t), O: f(r[1]), H: f(r[2]), L: f(r[3]), C: f(r[4]), V: f(r[5])})
	}
	return out, nil
}

func (b *Binance) Funding(ctx context.Context, symbol string, from, to int64) ([]trading.FundingSettle, error) {
	var out []trading.FundingSettle
	start := from
	for start < to {
		page, err := trading.FetchFundingHistory(ctx, b.Base, symbol, start, to)
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			break
		}
		out = append(out, page...)
		start = page[len(page)-1].Time + 1
		if len(page) < 1000 {
			break
		}
	}
	return out, nil
}
