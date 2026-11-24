// Package bots 交易机器人（D41、D42、D83）：现货网格、合约网格、DCA、抄底、定投。
// 每个机器人是一个引擎，用 bot:<id> 的来源下单，同一账户同一个币只有一个引擎。
// 现货和合约都走 Exec 接口，所以逻辑只有一份。
package bots

import (
	"math"

	"github.com/shopspring/decimal"
)

// 机器人种类。
const (
	SpotGrid = "spot_grid"
	PerpGrid = "perp_grid"
	DCA      = "dca"
	Dip      = "dip"  // 抄底：等跌到才开始的 DCA（D83）
	Recur    = "plan" // 定投
)

var (
	zero = decimal.Zero
	one  = decimal.NewFromInt(1)
	two  = decimal.NewFromInt(2)
	hund = decimal.NewFromInt(100)
)

// Params 用户填的参数。不同种类只用其中一部分。
type Params struct {
	Instrument string `json:"instrument"`
	Name       string `json:"name,omitempty"`

	// 网格
	Lower      decimal.Decimal `json:"lower"`
	Upper      decimal.Decimal `json:"upper"`
	Grids      int             `json:"grids"`
	Geo        bool            `json:"geo"`    // 等比；默认等差
	Invest     decimal.Decimal `json:"invest"` // 投入金额
	Dir        string          `json:"dir"`    // 合约网格：long | short | neutral
	Leverage   int             `json:"leverage"`
	StopLoss   decimal.Decimal `json:"stop_loss"`   // 价格
	TakeProfit decimal.Decimal `json:"take_profit"` // 价格

	// DCA、抄底
	Side    string          `json:"side"`     // long | short（合约才能 short）
	First   decimal.Decimal `json:"first"`    // 首单金额
	Add     decimal.Decimal `json:"add"`      // 补仓金额
	StepPct decimal.Decimal `json:"step_pct"` // 跌多少补一次（%）
	Mult    decimal.Decimal `json:"mult"`     // 补仓倍数
	MaxAdds int             `json:"max_adds"` // 最多补几次
	TPPct   decimal.Decimal `json:"tp_pct"`   // 止盈（从均价算，%）
	SLPct   decimal.Decimal `json:"sl_pct"`   // 止损（从均价算，%），0 不设
	Repeat  bool            `json:"repeat"`   // 止盈后重新开始

	// 抄底
	TriggerPx decimal.Decimal `json:"trigger_px"` // 跌到这个价开始
	DropPct   decimal.Decimal `json:"drop_pct"`   // 或者从 24 小时最高跌这么多（%）

	// 定投
	Amount decimal.Decimal `json:"amount"`
	Every  string          `json:"every"` // hour | day | week | month
	Times  int             `json:"times"` // 一共几次，0 不限
}

// Spec 品种规格。
type Spec struct {
	Tick, Step, MinNotional decimal.Decimal
	Fee                     decimal.Decimal // 按挂单算的手续费率
	Taker                   decimal.Decimal
	Perp                    bool
	Band                    decimal.Decimal // 挂单价不能离现价太远（合约有这个限制）：太远的先不挂，走近了再挂
}

// Plan 下单前算好的结果：界面上写的都是这些数字。
type Plan struct {
	Lines      []decimal.Decimal `json:"lines,omitempty"`     // 网格线，从低到高
	Qty        decimal.Decimal   `json:"qty,omitempty"`       // 每格买入数量
	SellQty    decimal.Decimal   `json:"sell_qty,omitempty"`  // 每格卖出数量（现货买入时被扣了手续费的币）
	Below      int               `json:"below"`               // 现价下面几格（挂买）
	Above      int               `json:"above"`               // 现价上面几格（挂卖）
	InitSide   string            `json:"init_side,omitempty"` // 开始时先市价买 / 卖多少，这样上面的格子才有币卖
	InitQty    decimal.Decimal   `json:"init_qty,omitempty"`
	NetLo      decimal.Decimal   `json:"net_lo,omitempty"` // 扣完手续费，每格最少赚多少（USDT）
	NetHi      decimal.Decimal   `json:"net_hi,omitempty"` // 最多赚多少
	NetLoPct   decimal.Decimal   `json:"net_lo_pct"`       // 每格净利占这一格投入的比例（%）
	NetHiPct   decimal.Decimal   `json:"net_hi_pct"`       //
	FeePerGrid decimal.Decimal   `json:"fee_per_grid"`     // 一买一卖的手续费
	Need       decimal.Decimal   `json:"need"`             // 占用资金
	WorstLoss  decimal.Decimal   `json:"worst_loss"`       // 价格走到止损（或跌到下限）时大约亏多少
	LiqPx      decimal.Decimal   `json:"liq_px,omitempty"` // 合约：大约的强平价
	Ladder     []Rung            `json:"ladder,omitempty"` // DCA：每一档
	Warn       []string          `json:"warn"`             // 要提醒的事（代码，界面翻译）
}

// Rung DCA 的一档补仓。
type Rung struct {
	Px     decimal.Decimal `json:"px"`
	USD    decimal.Decimal `json:"usd"`
	Avg    decimal.Decimal `json:"avg"`     // 这一档成交后的均价
	TP     decimal.Decimal `json:"tp"`      // 这一档成交后的止盈价
	DownPc decimal.Decimal `json:"down_pc"` // 离现价多远（%）
	Sum    decimal.Decimal `json:"sum"`     // 累计投入
}

func floorTo(v, step decimal.Decimal) decimal.Decimal {
	if !step.IsPositive() {
		return v
	}
	return v.Div(step).Floor().Mul(step)
}

func ceilTo(v, step decimal.Decimal) decimal.Decimal {
	if !step.IsPositive() {
		return v
	}
	return v.Div(step).Ceil().Mul(step)
}

// BuildLines 网格线：等差或等比，对齐到最小价位。
func BuildLines(lower, upper decimal.Decimal, n int, geo bool, tick decimal.Decimal) []decimal.Decimal {
	out := make([]decimal.Decimal, n+1)
	f := func(i int) decimal.Decimal {
		if geo {
			r := upper.Div(lower).InexactFloat64()
			return lower.Mul(decimal.NewFromFloat(pow(r, float64(i)/float64(n))))
		}
		return lower.Add(upper.Sub(lower).Mul(decimal.NewFromInt(int64(i))).Div(decimal.NewFromInt(int64(n))))
	}
	for i := 0; i <= n; i++ {
		out[i] = floorTo(f(i), tick)
	}
	out[n] = floorTo(upper, tick)
	return out
}

// CheckGrid 校验网格参数并算出计划。kind 区分现货和合约。
func CheckGrid(kind string, p Params, price decimal.Decimal, s Spec) (*Plan, error) {
	if !p.Lower.IsPositive() || !p.Upper.GreaterThan(p.Lower) {
		return nil, rej("GRID_BAD_RANGE")
	}
	if p.Grids < 2 || p.Grids > 100 {
		return nil, rej("GRID_BAD_COUNT")
	}
	if !p.Invest.IsPositive() {
		return nil, rej("BOT_BAD_AMOUNT")
	}
	lines := BuildLines(p.Lower, p.Upper, p.Grids, p.Geo, s.Tick)
	for i := 1; i < len(lines); i++ {
		if !lines[i].GreaterThan(lines[i-1]) {
			return nil, rej("GRID_TOO_TIGHT")
		}
	}
	dir := "long"
	lev := decimal.NewFromInt(1)
	if kind == PerpGrid {
		dir = p.Dir
		if dir != "long" && dir != "short" && dir != "neutral" {
			return nil, rej("BAD_REQUEST")
		}
		if p.Leverage < 1 || p.Leverage > 20 {
			return nil, rej("BOT_BAD_LEVERAGE")
		}
		lev = decimal.NewFromInt(int64(p.Leverage))
	}
	below, above := 0, 0
	for i := 0; i < p.Grids; i++ {
		if lines[i].GreaterThanOrEqual(price) {
			above++
		} else {
			below++
		}
	}
	// 每格数量：投入金额要能买下所有的格子。上面的格子现在就要有货，按现价算
	cost := zero
	for i := 0; i < p.Grids; i++ {
		if lines[i].GreaterThanOrEqual(price) {
			cost = cost.Add(price)
		} else {
			cost = cost.Add(lines[i])
		}
	}
	q := floorTo(p.Invest.Mul(lev).Div(cost), s.Step)
	if !q.IsPositive() || q.Mul(lines[0]).LessThan(s.MinNotional) {
		return nil, rej("GRID_TOO_SMALL", "min", s.MinNotional.String())
	}
	plan := &Plan{Lines: lines, Qty: q, SellQty: q, Below: below, Above: above, Warn: []string{}}
	fee := s.Fee
	if kind == SpotGrid {
		plan.SellQty = floorTo(q.Mul(one.Sub(fee)), s.Step) // 买到的币被扣了手续费，卖的时候只有这么多
		if plan.SellQty.Mul(lines[0]).LessThan(s.MinNotional) {
			return nil, rej("GRID_TOO_SMALL", "min", s.MinNotional.String())
		}
	}
	// 每格净利：最窄的一格和最宽的一格
	net := func(i int) decimal.Decimal {
		lo, hi := lines[i], lines[i+1]
		if kind == SpotGrid {
			// 取整剩下的零头还是自己的币，下次一起卖，所以按到手的数量算，不按取整后的
			return q.Mul(one.Sub(fee)).Mul(hi).Mul(one.Sub(fee)).Sub(q.Mul(lo))
		}
		return q.Mul(hi.Sub(lo)).Sub(q.Mul(hi.Add(lo)).Mul(fee))
	}
	plan.NetLo, plan.NetHi = net(0), net(0)
	pct := func(i int, v decimal.Decimal) decimal.Decimal { return v.Div(q.Mul(lines[i])).Mul(hund) }
	plan.NetLoPct, plan.NetHiPct = pct(0, net(0)), pct(0, net(0))
	for i := 0; i < p.Grids; i++ {
		if n := net(i); n.LessThan(plan.NetLo) {
			plan.NetLo, plan.NetLoPct = n, pct(i, n)
		} else if n.GreaterThan(plan.NetHi) {
			plan.NetHi, plan.NetHiPct = n, pct(i, n)
		}
	}
	plan.FeePerGrid = q.Mul(lines[0].Add(lines[1])).Mul(fee)
	if !plan.NetLo.IsPositive() {
		plan.Warn = append(plan.Warn, "GRID_NET_NEGATIVE") // 有的格子赚不够手续费
	}
	if price.LessThanOrEqual(lines[0]) || price.GreaterThanOrEqual(lines[p.Grids]) {
		plan.Warn = append(plan.Warn, "GRID_OUT_OF_RANGE") // 现价不在区间里
	}
	// 开始时要先有的仓位
	switch dir {
	case "long":
		if above > 0 {
			plan.InitSide = "buy"
			if kind == SpotGrid {
				plan.InitQty = ceilTo(plan.SellQty.Mul(decimal.NewFromInt(int64(above))).Div(one.Sub(s.Taker)), s.Step)
			} else {
				plan.InitQty = q.Mul(decimal.NewFromInt(int64(above)))
			}
		}
	case "short":
		if below > 0 {
			plan.InitSide, plan.InitQty = "sell", q.Mul(decimal.NewFromInt(int64(below)))
		}
	}
	plan.Need = p.Invest
	// 最坏情况：价格走到止损（没设就走到区间外面），已经买进的都亏
	worst := func(px decimal.Decimal) decimal.Decimal {
		loss := zero
		for i := 0; i < p.Grids; i++ {
			switch dir {
			case "long":
				entry := lines[i]
				if lines[i].GreaterThanOrEqual(price) {
					entry = price
				}
				if px.LessThan(entry) {
					loss = loss.Add(entry.Sub(px).Mul(q))
				}
			case "short":
				entry := lines[i+1]
				if lines[i+1].LessThanOrEqual(price) {
					entry = price
				}
				if px.GreaterThan(entry) {
					loss = loss.Add(px.Sub(entry).Mul(q))
				}
			case "neutral":
				if lines[i+1].LessThanOrEqual(price) && px.LessThan(lines[i]) {
					loss = loss.Add(lines[i].Sub(px).Mul(q))
				}
				if lines[i].GreaterThanOrEqual(price) && px.GreaterThan(lines[i+1]) {
					loss = loss.Add(px.Sub(lines[i+1]).Mul(q))
				}
			}
		}
		return loss
	}
	stop := p.StopLoss
	if dir == "short" {
		if stop.IsPositive() {
			plan.WorstLoss = worst(stop)
		} else {
			plan.WorstLoss = worst(p.Upper.Mul(decimal.RequireFromString("1.1")))
		}
	} else if stop.IsPositive() {
		plan.WorstLoss = worst(stop)
	} else if kind == SpotGrid {
		plan.WorstLoss = worst(p.Lower)
		plan.Warn = append(plan.Warn, "GRID_NO_STOP")
	} else {
		plan.WorstLoss = worst(p.Lower.Mul(decimal.RequireFromString("0.9")))
	}
	if kind == PerpGrid {
		if !p.StopLoss.IsPositive() {
			plan.Warn = append(plan.Warn, "GRID_NO_STOP")
		}
		// 大约的强平价：保证金亏完（逐仓，全部格子都成交的最坏情况）
		mm := p.Invest.Mul(decimal.RequireFromString("0.95"))
		tot := q.Mul(decimal.NewFromInt(int64(p.Grids)))
		if tot.IsPositive() {
			switch dir {
			case "long", "neutral":
				plan.LiqPx = decimal.Max(zero, p.Lower.Add(price).Div(two).Sub(mm.Div(tot)))
			case "short":
				plan.LiqPx = p.Upper.Add(price).Div(two).Add(mm.Div(tot))
			}
		}
	}
	return plan, nil
}

// CheckDCA 校验 DCA、抄底的参数并算出每一档。
func CheckDCA(p Params, price decimal.Decimal, s Spec) (*Plan, error) {
	if !p.First.IsPositive() {
		return nil, rej("BOT_BAD_AMOUNT")
	}
	if p.First.LessThan(s.MinNotional) || (p.MaxAdds > 0 && p.Add.LessThan(s.MinNotional)) {
		return nil, rej("MIN_NOTIONAL", "min", s.MinNotional.String())
	}
	if p.MaxAdds < 0 || p.MaxAdds > 30 {
		return nil, rej("DCA_BAD_ADDS")
	}
	if p.MaxAdds > 0 && (!p.StepPct.IsPositive() || p.StepPct.GreaterThanOrEqual(hund)) {
		return nil, rej("DCA_BAD_STEP")
	}
	if !p.TPPct.IsPositive() {
		return nil, rej("DCA_BAD_TP")
	}
	mult := p.Mult
	if !mult.IsPositive() {
		mult = one
	}
	short := p.Side == "short"
	if short && !s.Perp {
		return nil, rej("BAD_REQUEST")
	}
	sign := func(d decimal.Decimal) decimal.Decimal { // 做空时反过来
		if short {
			return one.Add(d)
		}
		return one.Sub(d)
	}
	plan := &Plan{Warn: []string{}, Ladder: []Rung{}}
	spent, qty := p.First, p.First.Div(price)
	amt := p.Add
	worstPx := price
	for k := 1; k <= p.MaxAdds; k++ {
		px := floorTo(price.Mul(sign(p.StepPct.Mul(decimal.NewFromInt(int64(k))).Div(hund))), s.Tick)
		if !px.IsPositive() {
			return nil, rej("DCA_BAD_STEP")
		}
		spent = spent.Add(amt)
		qty = qty.Add(amt.Div(px))
		avg := spent.Div(qty)
		tp := avg.Mul(one.Add(p.TPPct.Div(hund)))
		if short {
			tp = avg.Mul(one.Sub(p.TPPct.Div(hund)))
		}
		plan.Ladder = append(plan.Ladder, Rung{Px: px, USD: amt, Avg: avg, TP: floorTo(tp, s.Tick), DownPc: px.Div(price).Sub(one).Mul(hund), Sum: spent})
		worstPx = px
		amt = amt.Mul(mult)
	}
	plan.Need = spent
	if p.SLPct.IsPositive() {
		avg := spent.Div(qty)
		plan.WorstLoss = spent.Mul(p.SLPct).Div(hund)
		_ = avg
	} else {
		plan.Warn = append(plan.Warn, "DCA_NO_STOP")
		_ = worstPx
	}
	return plan, nil
}

func pow(a, b float64) float64 { return math.Pow(a, b) }
