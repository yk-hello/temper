// Package backtest 回测：用模拟盘同一个撮合引擎（trading.Engine），把历史 1 分钟 K 线一步步喂进去（D95）。
//
// 和模拟盘完全一样的：手续费、保证金分档、强平和清算费、止损止盈（按标记价或最新价触发）、限价单要价格穿过才成交、
// 资金费（币安的历史结算记录）、交易所的下单频率限制、信号的仓位算法（和 TradingView 信号同一段代码）。
//
// K 线里面怎么走：一分钟只知道开高低收，分成四步：开盘 → 先到的那个极值 → 另一个极值 → 收盘
// （收涨的先跌到最低再涨到最高，收跌的反过来，这是最常用的保守假设）。最新价和标记价各走各的 K 线。
//
// 不一样的（写在结果里，也写在界面上）：盘口按"深度足够"算，大单没有滑点（可以设一个滑点）；
// 一分钟里的真实顺序不知道；限价单不排队（价格穿过才成交，偏保守）。
package backtest

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"qs/internal/book"
	"qs/internal/journal"
	"qs/internal/portfolio"
	"qs/internal/signal"
	"qs/internal/trading"
)

// Signal 一条信号：一句话或 JSON（和 TradingView 警报一样，例如"做多 BTC 30%"），或者一张完整的委托。
type Signal struct {
	T      int64             `json:"t"`
	Text   string            `json:"text,omitempty"`
	Order  *trading.OrderReq `json:"order,omitempty"`
	Target *Target           `json:"target,omitempty"`
}

// Target 目标仓位（qsig/1 的写法）：这个品种的仓位调到"权益 × 权重"（正多负空，0 平仓）。
// 和模型组合一样按当时的权益换算，策略的前向记录能原样拿来回测（18 第 5.5 条"回测一致性"）。
type Target struct {
	Instrument string          `json:"instrument"`
	Weight     decimal.Decimal `json:"weight"`
}

// Config 一次回测。
type Config struct {
	Instruments []string        `json:"instruments"` // 空 = 从信号里找
	From        int64           `json:"from"`
	To          int64           `json:"to"`
	Initial     decimal.Decimal `json:"initial"`
	Leverage    int             `json:"leverage"`
	Mode        string          `json:"mode"`         // cross | isolated
	LatencyMS   int64           `json:"latency_ms"`   // 信号发出到单子到交易所
	SlippageBps float64         `json:"slippage_bps"` // 吃单额外滑点（万分之几）
	TakerFee    decimal.Decimal `json:"taker_fee"`    // 0 = 币安普通用户
	MakerFee    decimal.Decimal `json:"maker_fee"`
	Signals     []Signal        `json:"signals"`
}

// 上限：默认一次最多 5 个品种、180 天、2 万条信号。部署的人可以改（QS_LIMIT_BACKTEST_*，D103）。
var (
	MaxInstruments       = 5
	MaxDays        int64 = 180
	MaxSignals           = 20000
)

// Point 权益曲线的一个点。
type Point struct {
	T      int64   `json:"t"`
	Equity float64 `json:"equity"`
	Price  float64 `json:"price"` // 第一个品种的收盘价（画"一直拿着"那条线）
}

// Outcome 一条信号的结果。
type Outcome struct {
	I      int               `json:"i"` // 第几条（从 0 起）
	T      int64             `json:"t"`
	Exec   int64             `json:"exec"` // 实际到交易所的时刻
	OK     bool              `json:"ok"`
	Code   string            `json:"code,omitempty"`
	Params map[string]string `json:"params,omitempty"`
	Order  string            `json:"order,omitempty"`
}

// Result 回测结果。
type Result struct {
	Instruments  []string        `json:"instruments"`
	From         int64           `json:"from"`
	To           int64           `json:"to"`
	Initial      float64         `json:"initial"`
	Final        float64         `json:"final"`
	Stats        portfolio.Stats `json:"stats"`
	Journal      journal.Stat    `json:"journal"`
	Fees         float64         `json:"fees"`
	Funding      float64         `json:"funding"`
	LiqFees      float64         `json:"liq_fees"`
	Liquidations int             `json:"liquidations"`
	Equity       []Point         `json:"equity"`
	Trades       []journal.Trade `json:"trades"`
	Outcomes     []Outcome       `json:"outcomes"`
	Bars         int             `json:"bars"`
	Notes        []string        `json:"notes"`
	ElapsedMS    int64           `json:"elapsed_ms"`
}

// Errors（界面翻译）。
var (
	ErrRange      = errors.New("BT_BAD_RANGE")      // 时间不对或超过 180 天
	ErrNoSignals  = errors.New("BT_NO_SIGNALS")     // 没有信号
	ErrTooMany    = errors.New("BT_TOO_MANY")       // 品种或信号太多
	ErrUnknown    = errors.New("BT_BAD_INSTRUMENT") // 不认识的品种
	ErrNoData     = errors.New("BT_NO_DATA")        // 这段时间没有 K 线
	ErrDataFailed = errors.New("BT_DATA_FAILED")    // 下载历史数据失败
)

// Prepare 校验配置，补默认值，找出要用的品种。
func Prepare(cfg *Config, known func(inst string) bool) error {
	if cfg.To <= cfg.From || cfg.From <= 0 || cfg.To-cfg.From > MaxDays*day {
		return ErrRange
	}
	if cfg.To > time.Now().UnixMilli() {
		cfg.To = time.Now().UnixMilli() / 60_000 * 60_000
	}
	if len(cfg.Signals) == 0 {
		return ErrNoSignals
	}
	if len(cfg.Signals) > MaxSignals {
		return ErrTooMany
	}
	if cfg.Initial.Sign() <= 0 {
		cfg.Initial = decimal.NewFromInt(10000)
	}
	if cfg.Leverage <= 0 {
		cfg.Leverage = 10
	}
	if cfg.Mode != "isolated" {
		cfg.Mode = "cross"
	}
	cfg.LatencyMS = max(0, min(cfg.LatencyMS, 60_000))
	cfg.SlippageBps = math.Max(0, math.Min(cfg.SlippageBps, 500))
	set := map[string]bool{}
	for _, i := range cfg.Instruments {
		set[strings.ToUpper(i)] = true
	}
	for _, s := range cfg.Signals {
		if s.Target != nil {
			set[strings.ToUpper(s.Target.Instrument)] = true
		} else if s.Order != nil {
			set[strings.ToUpper(s.Order.Instrument)] = true
		} else if c, err := signal.Parse([]byte(s.Text)); err == nil && !c.Spot {
			set[c.Coin+"-PERP"] = true
		}
	}
	cfg.Instruments = cfg.Instruments[:0]
	for i := range set {
		if !known(i) {
			return ErrUnknown
		}
		cfg.Instruments = append(cfg.Instruments, i)
	}
	sort.Strings(cfg.Instruments)
	if len(cfg.Instruments) == 0 {
		return ErrNoSignals
	}
	if len(cfg.Instruments) > MaxInstruments {
		return ErrTooMany
	}
	sort.SliceStable(cfg.Signals, func(i, j int) bool { return cfg.Signals[i].T < cfg.Signals[j].T })
	return nil
}

func symOf(inst string) string { return strings.TrimSuffix(inst, "-PERP") + "USDT" }

// Run 跑一次回测：下载数据，再一步步喂给引擎。specs 用模拟盘正在用的品种规格（从币安读的）。
func Run(ctx context.Context, cfg Config, src Source, specs *trading.Specs) (*Result, error) {
	t0 := time.Now()
	type series struct {
		last, mark map[int64]Bar
		fund       []trading.FundingSettle
		spec       *trading.Spec
	}
	data := map[string]*series{}
	minutes := map[int64]bool{}
	for _, inst := range cfg.Instruments {
		sp, ok := specs.Get(inst)
		if !ok {
			return nil, ErrUnknown
		}
		last, err := src.Bars(ctx, symOf(inst), cfg.From, cfg.To, false)
		if err != nil {
			return nil, errors.Join(ErrDataFailed, err)
		}
		mark, err := src.Bars(ctx, symOf(inst), cfg.From, cfg.To, true)
		if err != nil {
			return nil, errors.Join(ErrDataFailed, err)
		}
		fund, err := src.Funding(ctx, symOf(inst), cfg.From, cfg.To+8*3600_000)
		if err != nil {
			return nil, errors.Join(ErrDataFailed, err)
		}
		s := &series{last: map[int64]Bar{}, mark: map[int64]Bar{}, fund: fund, spec: sp}
		for _, b := range last {
			s.last[b.T] = b
			minutes[b.T] = true
		}
		for _, b := range mark {
			s.mark[b.T] = b
		}
		sort.Slice(s.fund, func(i, j int) bool { return s.fund[i].Time < s.fund[j].Time })
		data[inst] = s
	}
	if len(minutes) == 0 {
		return nil, ErrNoData
	}
	ts := make([]int64, 0, len(minutes))
	for t := range minutes {
		ts = append(ts, t)
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i] < ts[j] })

	// 引擎：和模拟盘同一个，只有这几个品种
	books := book.NewStore()
	only := map[string]*trading.Spec{}
	for inst, s := range data {
		c := *s.spec
		only[inst] = &c
	}
	clock := time.UnixMilli(ts[0])
	e := trading.New(books, "bt", trading.NewSpecs(only), slog.New(slog.NewTextHandler(io.Discard, nil)))
	e.Now = func() time.Time { return clock }
	e.Persist = func([]trading.Event) error { return nil }
	if cfg.TakerFee.Sign() > 0 {
		e.TakerFee = cfg.TakerFee
	}
	if cfg.MakerFee.Sign() > 0 {
		e.MakerFee = cfg.MakerFee
	}
	const acct = 1
	e.Open(acct, cfg.Initial)
	slip := cfg.SlippageBps / 10000
	setPx := func(inst string, px, mk float64, t int64) {
		s := data[inst]
		tick := s.spec.Tick
		ask := decimal.NewFromFloat(px * (1 + slip))
		bid := decimal.NewFromFloat(px * (1 - slip))
		ask = ask.Div(tick).Ceil().Mul(tick)
		bid = bid.Div(tick).Floor().Mul(tick)
		if !bid.LessThan(ask) {
			bid = ask.Sub(tick)
		}
		deep := decimal.NewFromInt(1_000_000_000)
		books.SetBook(book.Book{Venue: "bt", Instrument: inst, Bids: []book.Level{{Px: bid, Qty: deep}}, Asks: []book.Level{{Px: ask, Qty: deep}}, Time: t})
		// 下一次资金费结算：用币安的历史记录；记录之外按 8 小时一期、费率 0（只是让引擎知道"又结算了一期"）
		var rate decimal.Decimal
		next := (t/(8*3600_000) + 1) * 8 * 3600_000
		for _, f := range s.fund {
			if f.Time > t {
				rate, next = f.Rate, f.Time
				break
			}
		}
		books.UpdateTicker("bt", inst, func(k *book.Ticker) {
			k.Last, k.Mark, k.Index, k.Time = decimal.NewFromFloat(px), decimal.NewFromFloat(mk), decimal.NewFromFloat(mk), t
			k.FundingRate, k.NextFunding = rate, next
		})
	}
	for inst := range data {
		_ = e.SetLeverage(acct, inst, cfg.Leverage)
		_ = e.SetMode(acct, inst, cfg.Mode)
	}

	lastPx := func(inst string) decimal.Decimal { // 目标仓位按最新成交价换算（和模型组合一样）
		if t, ok := books.Ticker("bt", inst); ok {
			return t.Last
		}
		return decimal.Zero
	}
	res := &Result{Instruments: cfg.Instruments, From: cfg.From, To: cfg.To, Initial: cfg.Initial.InexactFloat64()}
	next := 0                       // 下一条要执行的信号
	prev := map[string][2]float64{} // 上一步每个品种的最新价、标记价
	var prevT int64
	for next < len(cfg.Signals) && cfg.Signals[next].T+cfg.LatencyMS < ts[0] { // 早于第一根 K 线的信号
		res.Outcomes = append(res.Outcomes, Outcome{I: next, T: cfg.Signals[next].T, Code: "BT_BEFORE_START"})
		next++
	}
	var nav []portfolio.NAVPoint
	step := max(1, len(ts)/1500)
	for bi, t := range ts {
		if bi%2000 == 0 && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// 一分钟分四步：开 → 先到的极值 → 另一个极值 → 收。
		// 信号在两步之间到交易所的，按到达那一刻在两步之间线性插值的价格成交（不跳到下一步的极值上）
		for k := 0; k < 4; k++ {
			pt := t + []int64{0, 15_000, 30_000, 59_000}[k]
			cur := map[string][2]float64{}
			for inst, s := range data {
				b, ok := s.last[t]
				if !ok {
					continue
				}
				m, ok := s.mark[t]
				if !ok {
					m = b
				}
				cur[inst] = [2]float64{path(b, k), path(m, k)}
			}
			for next < len(cfg.Signals) && cfg.Signals[next].T+cfg.LatencyMS < pt {
				due := max(cfg.Signals[next].T+cfg.LatencyMS, prevT)
				if prevT > 0 && due > prevT {
					clock = time.UnixMilli(due)
					w := float64(due-prevT) / float64(pt-prevT)
					for inst, c := range cur {
						if p, ok := prev[inst]; ok {
							setPx(inst, p[0]+(c[0]-p[0])*w, p[1]+(c[1]-p[1])*w, due)
						}
					}
					e.Tick()
				}
				res.Outcomes = append(res.Outcomes, execute(e, acct, cfg.Signals[next], next, due, lastPx))
				next++
			}
			clock = time.UnixMilli(pt)
			for inst, c := range cur {
				setPx(inst, c[0], c[1], pt)
				prev[inst] = c
			}
			prevT = pt
			e.Tick()
			for next < len(cfg.Signals) && cfg.Signals[next].T+cfg.LatencyMS <= pt {
				res.Outcomes = append(res.Outcomes, execute(e, acct, cfg.Signals[next], next, pt, lastPx))
				next++
			}
		}
		res.Bars++
		if b, err := e.Brief(acct); err == nil {
			nav = append(nav, portfolio.NAVPoint{T: t + 60_000, NAV: b.Equity.InexactFloat64()})
			if bi%step == 0 || bi == len(ts)-1 {
				px := 0.0
				if bar, ok := data[cfg.Instruments[0]].last[t]; ok {
					px = bar.C
				}
				res.Equity = append(res.Equity, Point{T: t + 60_000, Equity: b.Equity.InexactFloat64(), Price: px})
			}
		}
		if bi%5000 == 0 {
			e.Flush()
		}
	}
	for ; next < len(cfg.Signals); next++ { // 晚于最后一根 K 线的信号
		res.Outcomes = append(res.Outcomes, Outcome{I: next, T: cfg.Signals[next].T, Code: "BT_AFTER_END"})
	}

	fills, funds, liqFees, _ := e.Records(acct)
	closed, open := portfolio.BuildTrades(fills, funds)
	res.Trades = journal.Build(closed, open, e.Stops(acct), nil, res.Initial, 0)
	res.Journal = journal.StatOf("all", closedOnly(res.Trades))
	if b, err := e.Brief(acct); err == nil {
		res.Final = b.Equity.InexactFloat64()
	}
	res.Stats = portfolio.ComputeStats(res.Initial, res.Final, nav, closed)
	for _, f := range fills {
		res.Fees += f.Fee.InexactFloat64()
		if f.Liq {
			res.Liquidations++
		}
	}
	for _, f := range funds {
		res.Funding += f.Amount.InexactFloat64()
	}
	res.LiqFees = liqFees.InexactFloat64()
	res.Notes = notes(cfg)
	res.ElapsedMS = time.Since(t0).Milliseconds()
	return res, nil
}

func closedOnly(ts []journal.Trade) []journal.Trade {
	out := make([]journal.Trade, 0, len(ts))
	for _, t := range ts {
		if t.CloseTime > 0 {
			out = append(out, t)
		}
	}
	return out
}

// path 一分钟里的第 k 步：收涨的 开 → 低 → 高 → 收，收跌的 开 → 高 → 低 → 收。
func path(b Bar, k int) float64 {
	up := b.C >= b.O
	switch k {
	case 0:
		return b.O
	case 1:
		if up {
			return b.L
		}
		return b.H
	case 2:
		if up {
			return b.H
		}
		return b.L
	}
	return b.C
}

// execute 执行一条信号：一句话按实盘信号的算法下单（资金 × 百分比，市价）；委托原样下。
func execute(e *trading.Engine, acct int64, s Signal, i int, at int64, lastPx func(string) decimal.Decimal) Outcome {
	o := Outcome{I: i, T: s.T, Exec: at}
	fail := func(err error) Outcome {
		var r *trading.Reject
		if errors.As(err, &r) {
			o.Code, o.Params = r.Code, r.Params
		} else {
			o.Code = err.Error()
			var rc interface{ RejectCode() string }
			if errors.As(err, &rc) {
				o.Code = rc.RejectCode()
			}
		}
		return o
	}
	if s.Target != nil {
		return toTarget(e, acct, *s.Target, lastPx(strings.ToUpper(s.Target.Instrument)), o, fail)
	}
	if s.Order != nil {
		r := *s.Order
		r.Instrument = strings.ToUpper(r.Instrument)
		ord, err := e.PlaceAs(acct, r, "signal", "")
		if err != nil {
			return fail(err)
		}
		o.OK, o.Order = true, ord.ID
		return o
	}
	cmd, err := signal.Parse([]byte(s.Text))
	if err != nil {
		return fail(err)
	}
	if cmd.Spot {
		o.Code = "BT_SPOT_SIGNAL"
		return o
	}
	inst := cmd.Coin + "-PERP"
	budget := decimal.Zero
	if b, err := e.Brief(acct); err == nil {
		budget = b.Available
	}
	side, notional, closeAll := signal.PerpPlan(cmd, budget)
	var ord *trading.Order
	if closeAll {
		ord, err = e.CloseAs(acct, inst, "signal", "")
	} else {
		ord, err = e.PlaceAs(acct, trading.OrderReq{Instrument: inst, Side: side, Type: trading.Market, Notional: notional}, "signal", "")
	}
	if err != nil {
		return fail(err)
	}
	o.OK, o.Order = true, ord.ID
	return o
}

// notes 这次回测和真实交易不一样的地方（界面照着写）。
func notes(cfg Config) []string {
	n := []string{"BT_NOTE_DEPTH", "BT_NOTE_PATH", "BT_NOTE_QUEUE"}
	if cfg.LatencyMS == 0 {
		n = append(n, "BT_NOTE_NOLAT")
	}
	return n
}

// toTarget 把仓位调到"权益 × 权重"：差额按市价下单，减仓的部分只减仓。差额不够最小下单量就不动（算执行了）。
func toTarget(e *trading.Engine, acct int64, t Target, px decimal.Decimal, o Outcome, fail func(error) Outcome) Outcome {
	inst := strings.ToUpper(t.Instrument)
	b, err := e.Brief(acct)
	if err != nil {
		return fail(err)
	}
	if px.Sign() <= 0 {
		return fail(trading.Rej("MARKET_UNAVAILABLE"))
	}
	cur := b.Pos[inst].Qty
	want := t.Weight.Mul(b.Equity).Div(px)
	delta := want.Sub(cur)
	q, ok := e.RoundQty(inst, delta.Abs())
	if !ok {
		return fail(trading.Rej("UNKNOWN_INSTRUMENT"))
	}
	if t.Weight.IsZero() && !cur.IsZero() {
		ord, err := e.CloseAs(acct, inst, "signal", "")
		if err != nil {
			return fail(err)
		}
		o.OK, o.Order = true, ord.ID
		return o
	}
	if q.IsZero() {
		o.OK = true
		return o
	}
	side := "buy"
	if delta.Sign() < 0 {
		side = "sell"
	}
	// 反向的部分先平（只减仓，不受保证金限制），剩下的再开
	if !cur.IsZero() && (cur.Sign() > 0) != (delta.Sign() > 0) {
		closeQ := decimal.Min(q, cur.Abs())
		ord, err := e.PlaceAs(acct, trading.OrderReq{Instrument: inst, Side: side, Type: trading.Market, Qty: closeQ, ReduceOnly: true}, "signal", "")
		if err != nil {
			return fail(err)
		}
		o.OK, o.Order = true, ord.ID
		q = q.Sub(closeQ)
		if q.IsZero() {
			return o
		}
	}
	ord, err := e.PlaceAs(acct, trading.OrderReq{Instrument: inst, Side: side, Type: trading.Market, Qty: q}, "signal", "")
	if err != nil {
		return fail(err)
	}
	o.OK, o.Order = true, ord.ID
	return o
}
