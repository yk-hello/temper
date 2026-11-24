package risk

// 历史情景和"最坏的一天"（18 第 10.3 条，D102，借高盛 gs-quant 的情景分析思路）。
//
//   - 历史情景：真实发生过的大行情那一天，每个持仓的币当天的真实日线（币安合约公开接口）。
//     每个仓位按对它最不利的价算一次（多单看当天最低、空单看最高）：会不会被强平、最差的时候亏多少；
//     再按收盘价算一次：这一天过完亏多少。那天这个币还没上市的，按它和 BTC 的联动系数乘 BTC 当天的涨跌估，界面上标"估算"。
//   - 最坏的一天：过去一年每天的涨跌，按现在的仓位大小重演一遍，排一排：100 天里最差的那 1 天、20 天里最差的 1 天、一年里最差的一天。
//     按名义价值直接乘，没算强平和保证金（界面写明）。
// 只看现有仓位，不考虑止损、止盈和挂单。只摆数字，不做买卖判断。

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"qs/internal/auth"
	"qs/internal/trading"
)

// ScenarioDef 一个历史情景。名字在界面的词条里（rk.sc.<ID>）。
type ScenarioDef struct {
	ID   string `json:"id"`
	Date string `json:"date"` // UTC 那一天
	Up   bool   `json:"up"`   // 暴涨（对空单不利）
}

// Scenarios 2026-11-13 对着币安的日线核对过：BTC 当天最低（或最高）相对开盘。
var Scenarios = []ScenarioDef{
	{"covid", "2020-03-12", false},   // 新冠恐慌：最低 −45%
	{"may19", "2021-05-19", false},   // 519：最低 −33%
	{"celsius", "2022-06-13", false}, // 三箭资本、Celsius：最低 −18%
	{"ftx", "2022-11-09", false},     // FTX 倒闭：最低 −17%
	{"yen", "2024-08-05", false},     // 日元套息平仓：最低 −16%
	{"oct10", "2025-10-10", false},   // 关税消息、连环爆仓：BTC 最低 −16.5%，SOL −36%，DOGE −66%
	{"tesla", "2021-02-08", true},    // 特斯拉买入：最高 +21%
	{"election", "2024-11-06", true}, // 美国大选：最高 +10%
}

// DayBar 一根日线。
type DayBar struct{ O, H, L, C float64 }

// History 历史日线（币安合约公开接口），算过的留着：情景那几天的不会变，一年的 6 小时更新一次。
type History struct {
	FAPI string
	HTTP *http.Client

	mu   sync.Mutex
	days map[string]*DayBar // symbol|date → nil 表示那天还没上市
	year map[string]yearEntry
}

type yearEntry struct {
	at  time.Time
	ret map[string]float64 // 日期 → 当天涨跌（收盘比前一天收盘）
}

func (h *History) get(ctx context.Context, path string, v any) error {
	base := h.FAPI
	if base == "" {
		base = "https://fapi.binance.com"
	}
	hc := h.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+path, nil)
	if err != nil {
		return err
	}
	res, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", res.StatusCode)
	}
	return json.NewDecoder(res.Body).Decode(v)
}

func parseBars(raw [][]any) (ts []int64, bars []DayBar) {
	f := func(x any) float64 {
		s, _ := x.(string)
		var v float64
		_, _ = fmt.Sscan(s, &v)
		return v
	}
	for _, k := range raw {
		if len(k) < 5 {
			continue
		}
		t, _ := k[0].(float64)
		ts = append(ts, int64(t))
		bars = append(bars, DayBar{O: f(k[1]), H: f(k[2]), L: f(k[3]), C: f(k[4])})
	}
	return
}

// Day 某个币某一天（UTC）的日线；那天还没上市返回 nil。
func (h *History) Day(ctx context.Context, symbol, date string) (*DayBar, error) {
	key := symbol + "|" + date
	h.mu.Lock()
	if b, ok := h.days[key]; ok {
		h.mu.Unlock()
		return b, nil
	}
	h.mu.Unlock()
	d, err := time.Parse("2006-01-02", date)
	if err != nil {
		return nil, err
	}
	var raw [][]any
	if err := h.get(ctx, fmt.Sprintf("/fapi/v1/klines?symbol=%s&interval=1d&startTime=%d&limit=1", symbol, d.UnixMilli()), &raw); err != nil {
		return nil, err
	}
	ts, bars := parseBars(raw)
	var b *DayBar
	if len(bars) == 1 && ts[0] == d.UnixMilli() && bars[0].O > 0 { // 第一根不是那天 = 那天还没上市
		b = &bars[0]
	}
	h.mu.Lock()
	if h.days == nil {
		h.days = map[string]*DayBar{}
	}
	h.days[key] = b
	h.mu.Unlock()
	return b, nil
}

// Year 过去一年每天的涨跌（日期 → 收盘比前一天收盘）。
func (h *History) Year(ctx context.Context, symbol string) (map[string]float64, error) {
	h.mu.Lock()
	if e, ok := h.year[symbol]; ok && time.Since(e.at) < 6*time.Hour {
		h.mu.Unlock()
		return e.ret, nil
	}
	h.mu.Unlock()
	var raw [][]any
	if err := h.get(ctx, "/fapi/v1/klines?symbol="+symbol+"&interval=1d&limit=367", &raw); err != nil {
		return nil, err
	}
	ts, bars := parseBars(raw)
	ret := map[string]float64{}
	today := time.Now().UTC().Format("2006-01-02")
	for i := 1; i < len(bars); i++ {
		day := time.UnixMilli(ts[i]).UTC().Format("2006-01-02")
		if day == today || bars[i-1].C <= 0 { // 今天还没收盘
			continue
		}
		ret[day] = bars[i].C/bars[i-1].C - 1
	}
	h.mu.Lock()
	if h.year == nil {
		h.year = map[string]yearEntry{}
	}
	h.year[symbol] = yearEntry{time.Now(), ret}
	h.mu.Unlock()
	return ret, nil
}

// ScenarioOut 一个情景的结果。
type ScenarioOut struct {
	ScenarioDef
	BTC       *DayBar                 `json:"btc,omitempty"`
	Worst     *trading.ScenarioResult `json:"worst"` // 每个仓位按当天对它最不利的价
	Close     *trading.ScenarioResult `json:"close"` // 按收盘价
	Estimated []string                `json:"estimated,omitempty"`
}

// WorstDay 过去一年每天的涨跌按现在的仓位重演。PnL 是亏损为负的金额。
type WorstDay struct {
	Days      int     `json:"days"`
	P99       float64 `json:"p99"` // 100 天里最差的那 1 天
	P95       float64 `json:"p95"` // 20 天里最差的那 1 天
	Worst     float64 `json:"worst"`
	WorstDate string  `json:"worst_date"`
	Equity    float64 `json:"equity"`
}

// AccountScenarios 一个账户的历史情景和最坏的一天。
type AccountScenarios struct {
	ID        int64         `json:"id"`
	Scenarios []ScenarioOut `json:"scenarios"`
	WorstDay  *WorstDay     `json:"worst_day,omitempty"`
}

// Quantile 排好序（从小到大）的第 q 分位：第 ⌈q·N⌉ 差的那个。q = 0.01 时，100 天里取最差的 1 天，365 天里取第 4 差的。
func Quantile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(math.Ceil(q*float64(len(sorted)))) - 1
	i = max(0, min(i, len(sorted)-1))
	return sorted[i]
}

// worstDay notional：品种 → 带方向的名义价值（空单为负）。没有某天数据的币按联动系数乘 BTC 那天的涨跌。
func worstDay(notional map[string]float64, rets map[string]map[string]float64, beta map[string]float64, equity float64) *WorstDay {
	btc := rets["BTC-PERP"]
	if len(btc) == 0 || len(notional) == 0 {
		return nil
	}
	type day struct {
		date string
		pnl  float64
	}
	var days []day
	for date, br := range btc {
		var pnl float64
		for inst, n := range notional {
			r, ok := rets[inst][date]
			if !ok {
				r = br * beta[inst]
			}
			pnl += n * r
		}
		days = append(days, day{date, pnl})
	}
	sort.Slice(days, func(i, j int) bool {
		return days[i].pnl < days[j].pnl || (days[i].pnl == days[j].pnl && days[i].date < days[j].date)
	})
	vals := make([]float64, len(days))
	for i, d := range days {
		vals[i] = d.pnl
	}
	r2 := func(v float64) float64 { return math.Round(v*100) / 100 }
	return &WorstDay{Days: len(days), P99: r2(Quantile(vals, 0.01)), P95: r2(Quantile(vals, 0.05)), Worst: r2(days[0].pnl), WorstDate: days[0].date, Equity: r2(equity)}
}

// scenarios 算一个账户。
func (h *API) scenarios(ctx context.Context, p auth.PaperAccount) (*AccountScenarios, error) {
	v, err := h.Trader.View(p.ID)
	if err != nil {
		return nil, err
	}
	out := &AccountScenarios{ID: p.ID, Scenarios: []ScenarioOut{}}
	if len(v.Positions) == 0 || h.History == nil {
		return out, nil
	}
	insts := []string{"BTC-PERP"}
	beta := map[string]float64{"BTC-PERP": 1}
	notional := map[string]float64{}
	for _, pos := range v.Positions {
		n, _ := pos.Notional.Abs().Float64()
		if pos.Side == "short" {
			n = -n
		}
		notional[pos.Instrument] += n
		if _, ok := beta[pos.Instrument]; !ok {
			b, _ := h.Betas.Get(ctx, pos.Instrument).Value.Float64()
			beta[pos.Instrument] = b
			insts = append(insts, pos.Instrument)
		}
	}
	// 历史情景
	for _, sc := range Scenarios {
		bars := map[string]*DayBar{}
		for _, inst := range insts {
			b, err := h.History.Day(ctx, symbolOf(inst), sc.Date)
			if err != nil {
				return nil, err
			}
			bars[inst] = b
		}
		btc := bars["BTC-PERP"]
		if btc == nil {
			continue
		}
		var est []string
		moveOf := func(inst string, worstFor string) decimal.Decimal {
			b := bars[inst]
			var m float64
			if b == nil { // 那天还没上市：按联动系数估
				ref := btc.C
				switch worstFor {
				case "long":
					ref = btc.L
				case "short":
					ref = btc.H
				}
				m = (ref/btc.O - 1) * beta[inst]
			} else {
				ref := b.C
				switch worstFor {
				case "long":
					ref = b.L
				case "short":
					ref = b.H
				}
				m = ref/b.O - 1
			}
			return decimal.NewFromFloat(m).Round(4)
		}
		for _, inst := range insts {
			if bars[inst] == nil {
				est = append(est, inst)
			}
		}
		worst, err := h.Trader.Scenario(p.ID, func(inst, side string) decimal.Decimal { return moveOf(inst, side) })
		if err != nil {
			return nil, err
		}
		cl, err := h.Trader.Scenario(p.ID, func(inst, _ string) decimal.Decimal { return moveOf(inst, "") })
		if err != nil {
			return nil, err
		}
		out.Scenarios = append(out.Scenarios, ScenarioOut{ScenarioDef: sc, BTC: btc, Worst: worst, Close: cl, Estimated: est})
	}
	// 最坏的一天
	rets := map[string]map[string]float64{}
	for _, inst := range insts {
		r, err := h.History.Year(ctx, symbolOf(inst))
		if err != nil {
			return nil, err
		}
		rets[inst] = r
	}
	eq, _ := v.Equity.Float64()
	out.WorstDay = worstDay(notional, rets, beta, eq)
	return out, nil
}

// scenarioRoutes GET /api/risk/scenarios：每个账户的历史情景和最坏的一天。日线第一次要从交易所取，之后都用存着的。
func (h *API) scenarioRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/risk/scenarios", func(w http.ResponseWriter, req *http.Request) {
		if h.Gate != nil && !h.Gate.IsOpen() {
			w.Header().Set("Retry-After", "5")
			writeJSON(w, 503, map[string]string{"code": "STARTING"})
			return
		}
		h.Users.Require(func(w http.ResponseWriter, req *http.Request) {
			u := auth.UserFrom(req.Context())
			papers, err := h.Users.PaperAccounts(req.Context(), u.ID)
			if err != nil {
				writeJSON(w, 500, map[string]string{"code": "INTERNAL_ERROR"})
				return
			}
			ctx, cancel := context.WithTimeout(req.Context(), 25*time.Second)
			defer cancel()
			out := []AccountScenarios{}
			var all *WorstDay
			for _, p := range papers {
				a, err := h.scenarios(ctx, p)
				if err != nil {
					var r *trading.Reject
					if errorsAs(err, &r) {
						continue
					}
					writeJSON(w, 502, map[string]string{"code": "HISTORY_UNAVAILABLE"}) // 交易所的历史日线取不到
					return
				}
				out = append(out, *a)
			}
			if len(out) > 1 { // 全部账户合起来的最坏一天：分位数不能把各账户的加起来，要按合起来的仓位重算
				all = h.combinedWorst(ctx, papers)
			}
			writeJSON(w, 200, map[string]any{"accounts": out, "defs": Scenarios, "all_worst_day": all})
		})(w, req)
	})
}

// combinedWorst 所有账户的仓位合在一起算最坏的一天（日线都已经存着了）。
func (h *API) combinedWorst(ctx context.Context, papers []auth.PaperAccount) *WorstDay {
	notional := map[string]float64{}
	beta := map[string]float64{"BTC-PERP": 1}
	var equity float64
	for _, p := range papers {
		v, err := h.Trader.View(p.ID)
		if err != nil {
			continue
		}
		e, _ := v.Equity.Float64()
		equity += e
		for _, pos := range v.Positions {
			n, _ := pos.Notional.Abs().Float64()
			if pos.Side == "short" {
				n = -n
			}
			notional[pos.Instrument] += n
			if _, ok := beta[pos.Instrument]; !ok {
				bv, _ := h.Betas.Get(ctx, pos.Instrument).Value.Float64()
				beta[pos.Instrument] = bv
			}
		}
	}
	rets := map[string]map[string]float64{}
	for inst := range beta {
		r, err := h.History.Year(ctx, symbolOf(inst))
		if err != nil {
			return nil
		}
		rets[inst] = r
	}
	return worstDay(notional, rets, beta, equity)
}
