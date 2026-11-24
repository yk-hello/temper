// Package trading 是手动交易引擎（验收标准模块 2）：下单、撮合、保证金、强平、止盈止损、资金费、账本。
// 计算规则以币安 U 本位永续（单向持仓模式）为准；模拟盘和以后的实盘共用同一套计算。
package trading

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"
)

// Bracket 维持保证金分档。Floor 是这一档的名义价值下限（含），Cum 是速算数（由各档自动算出）。
type Bracket struct {
	Floor  decimal.Decimal `json:"floor"`
	MMR    decimal.Decimal `json:"mmr"`
	MaxLev int             `json:"max_leverage"`
	Cum    decimal.Decimal `json:"cum"`
}

// Spec 品种规格。
type Spec struct {
	Instrument  string          `json:"instrument"`
	Symbol      string          `json:"symbol"`
	Tick        decimal.Decimal `json:"tick"`         // 价格最小单位
	Step        decimal.Decimal `json:"step"`         // 数量最小单位
	MinQty      decimal.Decimal `json:"min_qty"`      // 最小数量
	MinNotional decimal.Decimal `json:"min_notional"` // 最小名义金额（USDT）
	PriceUp     decimal.Decimal `json:"price_up"`     // 限价单价格上限 = 标记价 × PriceUp（币安 PERCENT_PRICE）
	PriceDown   decimal.Decimal `json:"price_down"`
	MinPrice    decimal.Decimal `json:"min_price"` // PRICE_FILTER：价格下限、上限（0 = 不限）
	MaxPrice    decimal.Decimal `json:"max_price"`
	MaxQty      decimal.Decimal `json:"max_qty"`        // LOT_SIZE：限价单一张最多（0 = 不限）
	MarketMax   decimal.Decimal `json:"market_max_qty"` // MARKET_LOT_SIZE：市价单一张最多
	MaxOrders   int             `json:"max_orders"`     // MAX_NUM_ORDERS：这个品种同时挂着的委托（含条件单）
	MaxAlgo     int             `json:"max_algo"`       // MAX_NUM_ALGO_ORDERS：这个品种同时挂着的条件单（币安 2025-12-29 已去掉，改成所有品种合计 200；交易所还给时才用）
	MarketBound decimal.Decimal `json:"market_bound"`   // marketTakeBound：市价单最多吃到标记价上下这么远，剩下的过期
	LiqFee      decimal.Decimal `json:"liq_fee"`        // liquidationFee：强平清算费率，按强平时的名义价值收
	// triggerProtect：条件单开了价格保护时，标记价和最新价差超过这个比例就不触发（D103）
	TriggerProtect decimal.Decimal `json:"trigger_protect"`
	Brackets       []Bracket       `json:"brackets"`
	Source         string          `json:"source"`         // binance：从交易所读取；default：内置默认值
	BracketSrc     string          `json:"bracket_source"` // 分档目前都是内置占位（币安的分档接口要 API 密钥）
}

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// MaxLeverage 第一档的最大杠杆（仓位很小时允许的上限）。
func (s *Spec) MaxLeverage() int {
	if len(s.Brackets) == 0 {
		return 20
	}
	return s.Brackets[0].MaxLev
}

// BracketFor 按名义价值找所在分档。
func (s *Spec) BracketFor(notional decimal.Decimal) Bracket {
	b := s.Brackets[0]
	for _, x := range s.Brackets {
		if notional.GreaterThanOrEqual(x.Floor) {
			b = x
		}
	}
	return b
}

// MaxLevFor 这个名义价值下允许的最大杠杆。
func (s *Spec) MaxLevFor(notional decimal.Decimal) int { return s.BracketFor(notional).MaxLev }

// MaintMargin 维持保证金 = 名义价值 × 维持保证金率 − 速算数。
func (s *Spec) MaintMargin(notional decimal.Decimal) decimal.Decimal {
	b := s.BracketFor(notional)
	return notional.Mul(b.MMR).Sub(b.Cum)
}

// RoundQty 按数量单位向下取整。
func (s *Spec) RoundQty(q decimal.Decimal) decimal.Decimal {
	return q.Div(s.Step).Floor().Mul(s.Step)
}

// OnTick 价格是否是最小单位的整数倍。
func (s *Spec) OnTick(p decimal.Decimal) bool {
	return p.Mod(s.Tick).IsZero()
}

func withCum(bs []Bracket) []Bracket {
	cum := decimal.Zero
	for i := range bs {
		if i > 0 {
			cum = cum.Add(bs[i].Floor.Mul(bs[i].MMR.Sub(bs[i-1].MMR)))
		}
		bs[i].Cum = cum
	}
	return bs
}

func tiers(rows ...[3]string) []Bracket {
	out := make([]Bracket, len(rows))
	for i, r := range rows {
		var lev int
		fmt.Sscan(r[2], &lev)
		out[i] = Bracket{Floor: d(r[0]), MMR: d(r[1]), MaxLev: lev}
	}
	return withCum(out)
}

// 内置分档。分档表的正式接口（/fapi/v1/leverageBracket）要 API 密钥；配了密钥时启动后用真实分档覆盖（BracketSrc = "exchange"，见 FetchBrackets）。
//   - BTC：币安官网"杠杆与保证金"页的完整 12 档（规则更新时间 2025/08/19，2026-11-09 读取）。
//   - ETH 和其他品种：没有密钥时拿不到官方现值，用保守的值，界面上写明是估计（BracketSrc = "default"）。
var (
	btcTiers = tiers([3]string{"0", "0.004", "150"}, [3]string{"300000", "0.005", "100"}, [3]string{"800000", "0.0065", "75"},
		[3]string{"3000000", "0.01", "50"}, [3]string{"12000000", "0.02", "25"}, [3]string{"70000000", "0.025", "20"},
		[3]string{"100000000", "0.05", "10"}, [3]string{"230000000", "0.1", "5"}, [3]string{"480000000", "0.125", "4"},
		[3]string{"600000000", "0.15", "3"}, [3]string{"800000000", "0.25", "2"}, [3]string{"1200000000", "0.5", "1"})
	ethTiers = tiers([3]string{"0", "0.005", "100"}, [3]string{"10000", "0.0065", "75"}, [3]string{"100000", "0.01", "50"},
		[3]string{"500000", "0.02", "25"}, [3]string{"1000000", "0.05", "10"}, [3]string{"2000000", "0.1", "5"},
		[3]string{"5000000", "0.125", "4"}, [3]string{"10000000", "0.15", "3"}, [3]string{"20000000", "0.25", "2"})
	altTiers = func() []Bracket {
		return tiers([3]string{"0", "0.01", "50"}, [3]string{"5000", "0.025", "20"}, [3]string{"25000", "0.05", "10"},
			[3]string{"100000", "0.1", "5"}, [3]string{"250000", "0.125", "2"}, [3]string{"1000000", "0.25", "1"})
	}
)

// DefaultSpecs 内置默认规格（币安 exchangeInfo 的常见值）。启动时会尝试从交易所读取最新值覆盖。
func DefaultSpecs() map[string]*Spec {
	mk := func(inst, sym, tick, step, minNotional string, br []Bracket) *Spec {
		return &Spec{Instrument: inst, Symbol: sym, Tick: d(tick), Step: d(step), MinQty: d(step), MinNotional: d(minNotional),
			PriceUp: d("1.05"), PriceDown: d("0.95"), MaxOrders: 200, MarketBound: d("0.05"), LiqFee: d("0.0125"), TriggerProtect: d("0.05"),
			Brackets: br, Source: "default", BracketSrc: "default"}
	}
	btc := mk("BTC-PERP", "BTCUSDT", "0.1", "0.001", "50", btcTiers)
	btc.BracketSrc = "binance-page" // 官网页面读的完整分档，不是估计
	btc.PriceUp, btc.PriceDown, btc.MarketBound = d("1.03"), d("0.97"), d("0.03")
	btc.MaxQty, btc.MarketMax = d("1000"), d("120")
	eth := mk("ETH-PERP", "ETHUSDT", "0.01", "0.001", "20", ethTiers)
	eth.PriceUp, eth.PriceDown, eth.MarketBound = d("1.03"), d("0.97"), d("0.03")
	eth.MaxQty, eth.MarketMax = d("10000"), d("2000")
	return map[string]*Spec{
		"BTC-PERP":  btc,
		"ETH-PERP":  eth,
		"SOL-PERP":  mk("SOL-PERP", "SOLUSDT", "0.01", "0.01", "5", altTiers()),
		"BNB-PERP":  mk("BNB-PERP", "BNBUSDT", "0.01", "0.01", "5", altTiers()),
		"XRP-PERP":  mk("XRP-PERP", "XRPUSDT", "0.0001", "0.1", "5", altTiers()),
		"DOGE-PERP": mk("DOGE-PERP", "DOGEUSDT", "0.00001", "1", "5", altTiers()),
	}
}

// Specs 线程安全的规格表。
type Specs struct {
	mu      sync.RWMutex
	m       map[string]*Spec
	catalog map[string]*Spec // 全市场目录：用户点开才启用，没启用的不进撮合循环
}

func NewSpecs(m map[string]*Spec) *Specs { return &Specs{m: m} }

// HasCatalog 全市场目录取回来了吗。
func (s *Specs) HasCatalog() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.catalog) > 0
}

// Known 目录里有没有这个品种（不管启用没有）。symbol 是交易所代码，如 SUIUSDT。
func (s *Specs) Known(symbol string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	inst := strings.TrimSuffix(symbol, "USDT") + "-PERP"
	if _, ok := s.m[inst]; ok {
		return true
	}
	_, ok := s.catalog[inst]
	return ok
}

// Enable 启用一个品种：能下单、能撮合。ok = false 表示目录里没有。
func (s *Specs) Enable(inst string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[inst]; ok {
		return true
	}
	x, ok := s.catalog[inst]
	if ok {
		s.m[inst] = x
	}
	return ok
}

// AddCatalog 把一批代码放进目录（模拟行情用）。tick 按起始价定。
func (s *Specs) AddCatalog(symbols []string, price func(string) float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.catalog == nil {
		s.catalog = map[string]*Spec{}
	}
	for _, sym := range symbols {
		inst := strings.TrimSuffix(sym, "USDT") + "-PERP"
		if _, ok := s.m[inst]; ok {
			continue
		}
		tick, step := "0.01", "0.01"
		if p := price(sym); p < 0.1 {
			tick, step = "0.000001", "1"
		} else if p < 1 {
			tick, step = "0.0001", "1"
		} else if p < 100 {
			tick, step = "0.001", "0.1"
		}
		s.catalog[inst] = &Spec{Instrument: inst, Symbol: sym, Tick: d(tick), Step: d(step), MinQty: d(step), MinNotional: d("5"),
			PriceUp: d("1.05"), PriceDown: d("0.95"), Brackets: altTiers(), Source: "default", BracketSrc: "default"}
	}
}

func (s *Specs) Get(inst string) (*Spec, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	x, ok := s.m[inst]
	return x, ok
}

func (s *Specs) All() map[string]*Spec {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]*Spec, len(s.m))
	for k, v := range s.m {
		out[k] = v
	}
	return out
}

// FetchBinance 从币安公开接口读取价格单位、数量单位、最小名义金额、价格范围，覆盖内置值。失败时保留内置值。
func (s *Specs) FetchBinance(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://fapi.binance.com/fapi/v1/exchangeInfo", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("exchangeInfo: HTTP %d", res.StatusCode)
	}
	var info struct {
		Symbols []struct {
			Symbol         string `json:"symbol"`
			ContractType   string `json:"contractType"`
			QuoteAsset     string `json:"quoteAsset"`
			Status         string `json:"status"`
			LiquidationFee string `json:"liquidationFee"`
			MarketBound    string `json:"marketTakeBound"`
			TriggerProtect string `json:"triggerProtect"`
			Filters        []struct {
				Type           string `json:"filterType"`
				TickSize       string `json:"tickSize"`
				MinPrice       string `json:"minPrice"`
				MaxPrice       string `json:"maxPrice"`
				StepSize       string `json:"stepSize"`
				MinQty         string `json:"minQty"`
				MaxQty         string `json:"maxQty"`
				Notional       string `json:"notional"`
				MultiplierUp   string `json:"multiplierUp"`
				MultiplierDown string `json:"multiplierDown"`
				Limit          int    `json:"limit"`
			} `json:"filters"`
		} `json:"symbols"`
	}
	if err := json.NewDecoder(res.Body).Decode(&info); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	bySym := map[string]*Spec{}
	for _, x := range s.m {
		bySym[strings.ToUpper(x.Symbol)] = x
	}
	for _, sy := range info.Symbols {
		x, ok := bySym[sy.Symbol]
		if !ok {
			// 不在正在跑的品种里：记进目录，用户点开时再启用（只要 U 本位永续、正常交易的）
			if sy.ContractType != "PERPETUAL" || sy.QuoteAsset != "USDT" || sy.Status != "TRADING" {
				continue
			}
			inst := strings.TrimSuffix(sy.Symbol, "USDT") + "-PERP"
			x = &Spec{Instrument: inst, Symbol: sy.Symbol, Tick: d("0.0001"), Step: d("1"), MinQty: d("1"), MinNotional: d("5"),
				PriceUp: d("1.05"), PriceDown: d("0.95"), MaxOrders: 200, MarketBound: d("0.05"), LiqFee: d("0.0125"), TriggerProtect: d("0.05"),
				Brackets: altTiers(), Source: "default", BracketSrc: "default"}
			ok = false
		}
		active := ok
		n := *x
		for _, f := range sy.Filters {
			p := func(v string, dst *decimal.Decimal) {
				if dv, err := decimal.NewFromString(v); err == nil && dv.Sign() > 0 {
					*dst = dv
				}
			}
			switch f.Type {
			case "PRICE_FILTER":
				p(f.TickSize, &n.Tick)
				p(f.MinPrice, &n.MinPrice)
				p(f.MaxPrice, &n.MaxPrice)
			case "LOT_SIZE":
				p(f.StepSize, &n.Step)
				p(f.MinQty, &n.MinQty)
				p(f.MaxQty, &n.MaxQty)
			case "MARKET_LOT_SIZE":
				p(f.MaxQty, &n.MarketMax)
			case "MAX_NUM_ORDERS":
				if f.Limit > 0 {
					n.MaxOrders = f.Limit
				}
			case "MAX_NUM_ALGO_ORDERS":
				if f.Limit > 0 {
					n.MaxAlgo = f.Limit
				}
			case "MIN_NOTIONAL":
				p(f.Notional, &n.MinNotional)
			case "PERCENT_PRICE":
				p(f.MultiplierUp, &n.PriceUp)
				p(f.MultiplierDown, &n.PriceDown)
			}
		}
		if v, err := decimal.NewFromString(sy.LiquidationFee); err == nil && v.Sign() > 0 {
			n.LiqFee = v
		}
		if v, err := decimal.NewFromString(sy.MarketBound); err == nil && v.Sign() > 0 {
			n.MarketBound = v
		}
		if v, err := decimal.NewFromString(sy.TriggerProtect); err == nil && v.Sign() > 0 {
			n.TriggerProtect = v
		}
		n.Source = "binance"
		if active {
			s.m[x.Instrument] = &n
		} else {
			if s.catalog == nil {
				s.catalog = map[string]*Spec{}
			}
			s.catalog[x.Instrument] = &n
		}
	}
	return nil
}

// LooseSpecs 测试用：和 DefaultSpecs 一样，只是价格带和市价单保护都放宽到 5%。
// 老测试的算术是按 5% 的价格带写的（挂在标记价 3–5% 之外的单）；币安 BTC、ETH 现在是 3%，另有测试专门查。
func LooseSpecs() map[string]*Spec {
	m := DefaultSpecs()
	for _, s := range m {
		s.PriceUp, s.PriceDown, s.MarketBound = d("1.05"), d("0.95"), d("0.05")
	}
	return m
}

// FetchBrackets 用币安 API 密钥读真实的保证金分档（GET /fapi/v1/leverageBracket，要签名），覆盖内置值（BracketSrc = "exchange"）。
// 只读，不下单。没有配密钥时不调用：分档表没有不要密钥的正式接口（币安官网页面用的是网站内部接口，后端不去抓）。
func (s *Specs) FetchBrackets(ctx context.Context, base, key, secret string) (int, error) {
	if key == "" || secret == "" {
		return 0, nil
	}
	if base == "" {
		base = "https://fapi.binance.com"
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	// 签名是对"实际发出去的查询串"算的，signature 必须原样接在最后（再编码一次会被重新排序，签名就对不上了）
	qs := url.Values{"timestamp": {strconv.FormatInt(time.Now().UnixMilli(), 10)}, "recvWindow": {"5000"}}.Encode()
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(qs))
	req, _ := http.NewRequestWithContext(ctx, "GET", base+"/fapi/v1/leverageBracket?"+qs+"&signature="+hex.EncodeToString(mac.Sum(nil)), nil)
	req.Header.Set("X-MBX-APIKEY", key)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		var e struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		}
		_ = json.NewDecoder(res.Body).Decode(&e)
		return 0, fmt.Errorf("leverageBracket: HTTP %d %d %s", res.StatusCode, e.Code, e.Msg)
	}
	var list []struct {
		Symbol   string `json:"symbol"`
		Brackets []struct {
			InitialLeverage  int     `json:"initialLeverage"`
			NotionalFloor    float64 `json:"notionalFloor"`
			MaintMarginRatio float64 `json:"maintMarginRatio"`
			Cum              float64 `json:"cum"`
		} `json:"brackets"`
	}
	if err := json.NewDecoder(res.Body).Decode(&list); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	apply := func(m map[string]*Spec) {
		bySym := map[string]*Spec{}
		for _, x := range m {
			bySym[strings.ToUpper(x.Symbol)] = x
		}
		for _, it := range list {
			x, ok := bySym[it.Symbol]
			if !ok || len(it.Brackets) == 0 {
				continue
			}
			bs := make([]Bracket, 0, len(it.Brackets))
			for _, b := range it.Brackets {
				bs = append(bs, Bracket{Floor: decimal.NewFromFloat(b.NotionalFloor), MMR: decimal.NewFromFloat(b.MaintMarginRatio), MaxLev: b.InitialLeverage, Cum: decimal.NewFromFloat(b.Cum)})
			}
			c := *x
			c.Brackets, c.BracketSrc = bs, "exchange"
			m[x.Instrument] = &c
			n++
		}
	}
	apply(s.m)
	if s.catalog != nil {
		apply(s.catalog)
	}
	return n, nil
}
