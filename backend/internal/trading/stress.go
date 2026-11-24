package trading

import "github.com/shopspring/decimal"

// 压力测试（`10` 的 G、4.24）：BTC 涨跌 x% 时账户还剩多少、哪些仓位会被强平。
//
//   - 每个币的涨跌 = x × 它对 BTC 的联动系数。
//   - 强平判断和引擎一样：逐仓 逐仓保证金 + 未实现盈亏 ≤ 维持保证金；全仓 保证金余额 ≤ 维持保证金。
//   - 从 0 往两边一格一格走：走到强平就留在强平，再走也不会活过来。
//   - 逐仓爆了亏掉整个逐仓保证金；全仓爆了全仓部分归零（和引擎的强平费算法一致）。
//   - 只看现有仓位，不考虑止损、止盈和挂单。

const (
	StressStep = 1  // 每格 1%
	StressMax  = 50 // ±50%
)

// StressPos 一个仓位的压力测试结果。
type StressPos struct {
	Instrument string            `json:"instrument"`
	Side       string            `json:"side"`
	Mode       string            `json:"mode"`
	Beta       decimal.Decimal   `json:"beta"`
	PnL        []decimal.Decimal `json:"pnl"`                // 每一格相对现在的盈亏变化
	LiqDown    *int              `json:"liq_down,omitempty"` // BTC 跌到多少被强平（负数）
	LiqUp      *int              `json:"liq_up,omitempty"`   // BTC 涨到多少被强平
}

// StressResult 压力测试结果。Moves 和 Equity、PnL 一一对应。
type StressResult struct {
	Moves     []int             `json:"moves"`
	Equity    []decimal.Decimal `json:"equity"`
	Positions []StressPos       `json:"positions"`
}

// Stress beta 给出每个品种的联动系数。
func (e *Engine) Stress(acct int64, beta func(inst string) decimal.Decimal) (*StressResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, ok := e.accounts[acct]
	if !ok {
		return nil, rej("NOT_FOUND")
	}
	mk := e.marks()
	n := StressMax/StressStep*2 + 1
	r := &StressResult{Moves: make([]int, n), Equity: make([]decimal.Decimal, n), Positions: []StressPos{}}
	for i := range r.Moves {
		r.Moves[i] = (i - StressMax/StressStep) * StressStep
	}
	type leg struct {
		p    *Position
		spec *Spec
		mark decimal.Decimal
		beta decimal.Decimal
		iso  bool
		out  *StressPos
		u0   decimal.Decimal
	}
	var legs []*leg
	wb := a.Wallet
	for _, inst := range sortedInsts(a) {
		p := a.Pos[inst]
		if p.Qty.IsZero() {
			continue
		}
		spec, sok := e.Specs.Get(inst)
		mark, mok := mk(inst)
		if !sok || !mok {
			continue
		}
		side := "long"
		if p.Qty.Sign() < 0 {
			side = "short"
		}
		set := a.setting(inst)
		b := beta(inst)
		r.Positions = append(r.Positions, StressPos{Instrument: inst, Side: side, Mode: set.Mode, Beta: b, PnL: make([]decimal.Decimal, n)})
		l := &leg{p: p, spec: spec, mark: mark, beta: b, iso: set.Mode == "isolated", u0: upnl(p, mark)}
		if l.iso {
			wb = wb.Sub(p.IsoMargin)
		}
		legs = append(legs, l)
	}
	for i, l := range legs {
		l.out = &r.Positions[i]
	}
	// 没有仓位的品种不影响结果；钱包里已经包含所有已实现盈亏
	floor := decimal.RequireFromString("0.001")
	mid := StressMax / StressStep
	for _, dir := range []int{-1, 1} {
		liq := map[*leg]decimal.Decimal{} // 强平了的仓位 → 定格的盈亏变化
		crossDead := false
		for k := 0; k <= mid; k++ {
			x := dir * k * StressStep
			idx := mid + dir*k
			move := decimal.NewFromInt(int64(x)).Div(hundred)
			crossU, crossMM := decimal.Zero, decimal.Zero
			eq := decimal.Zero
			upnls := make([]decimal.Decimal, len(legs))
			for j, l := range legs {
				if _, dead := liq[l]; dead {
					continue
				}
				m := l.mark.Mul(decimal.NewFromInt(1).Add(move.Mul(l.beta)))
				if m.LessThan(l.mark.Mul(floor)) {
					m = l.mark.Mul(floor)
				}
				u := upnl(l.p, m)
				upnls[j] = u
				mm := l.spec.MaintMargin(l.p.Qty.Abs().Mul(m))
				if l.iso {
					if l.p.IsoMargin.Add(u).LessThanOrEqual(mm) {
						liq[l] = l.p.IsoMargin.Add(l.u0).Neg() // 现在的价值全部亏掉
						markLiq(l.out, dir, x)
						continue
					}
					eq = eq.Add(l.p.IsoMargin).Add(u)
				} else {
					crossU = crossU.Add(u)
					crossMM = crossMM.Add(mm)
				}
			}
			if !crossDead {
				if crossMM.Sign() > 0 && wb.Add(crossU).LessThanOrEqual(crossMM) {
					crossDead = true
					for j, l := range legs {
						if _, dead := liq[l]; !dead && !l.iso {
							liq[l] = upnls[j].Sub(l.u0)
							markLiq(l.out, dir, x)
						}
					}
				} else {
					eq = eq.Add(wb).Add(crossU)
				}
			}
			r.Equity[idx] = eq.Round(2)
			for j, l := range legs {
				if v, dead := liq[l]; dead {
					l.out.PnL[idx] = v.Round(2)
				} else {
					l.out.PnL[idx] = upnls[j].Sub(l.u0).Round(2)
				}
			}
		}
	}
	return r, nil
}

func markLiq(p *StressPos, dir, x int) {
	v := x
	if dir < 0 {
		p.LiqDown = &v
	} else {
		p.LiqUp = &v
	}
}

// ScenarioPos 历史情景里一个仓位的结果（18 第 10.3 条）。
type ScenarioPos struct {
	Instrument string          `json:"instrument"`
	Side       string          `json:"side"`
	Move       decimal.Decimal `json:"move"` // 这个币按情景动了多少（比例，-0.25 = 跌 25%）
	PnL        decimal.Decimal `json:"pnl"`  // 相对现在的盈亏变化
	Liq        bool            `json:"liq"`  // 会被强平
}

// ScenarioResult 一个情景下账户的结果。
type ScenarioResult struct {
	Before    decimal.Decimal `json:"before"`
	After     decimal.Decimal `json:"after"`
	Positions []ScenarioPos   `json:"positions"`
}

// Scenario 每个仓位按 move(品种, 方向) 给的比例一次性动到位，算账户还剩多少、谁被强平。强平规则和 Stress 一样：
// 逐仓 逐仓保证金 + 未实现盈亏 ≤ 维持保证金；全仓 保证金余额 ≤ 维持保证金时全仓部分一起爆。只看现有仓位，不考虑止损、止盈和挂单。
func (e *Engine) Scenario(acct int64, move func(inst, side string) decimal.Decimal) (*ScenarioResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, ok := e.accounts[acct]
	if !ok {
		return nil, rej("NOT_FOUND")
	}
	mk := e.marks()
	floor := decimal.RequireFromString("0.001")
	r := &ScenarioResult{Positions: []ScenarioPos{}}
	wb := a.Wallet
	before := a.Wallet
	type leg struct {
		i         int
		iso       bool
		margin    decimal.Decimal
		u0, u, mm decimal.Decimal
	}
	var legs []leg
	for _, inst := range sortedInsts(a) {
		p := a.Pos[inst]
		if p.Qty.IsZero() {
			continue
		}
		spec, sok := e.Specs.Get(inst)
		mark, mok := mk(inst)
		if !sok || !mok {
			continue
		}
		side := "long"
		if p.Qty.Sign() < 0 {
			side = "short"
		}
		mv := move(inst, side)
		m := mark.Mul(decimal.NewFromInt(1).Add(mv))
		if m.LessThan(mark.Mul(floor)) {
			m = mark.Mul(floor)
		}
		u0 := upnl(p, mark)
		before = before.Add(u0)
		iso := a.setting(inst).Mode == "isolated"
		if iso {
			wb = wb.Sub(p.IsoMargin)
		}
		legs = append(legs, leg{i: len(r.Positions), iso: iso, margin: p.IsoMargin, u0: u0, u: upnl(p, m), mm: spec.MaintMargin(p.Qty.Abs().Mul(m))})
		r.Positions = append(r.Positions, ScenarioPos{Instrument: inst, Side: side, Move: mv})
	}
	after := decimal.Zero
	crossU, crossMM := decimal.Zero, decimal.Zero
	for _, l := range legs {
		out := &r.Positions[l.i]
		if l.iso {
			if l.margin.Add(l.u).LessThanOrEqual(l.mm) {
				out.Liq = true
				out.PnL = l.margin.Add(l.u0).Neg().Round(2) // 逐仓保证金全部亏掉
				continue
			}
			out.PnL = l.u.Sub(l.u0).Round(2)
			after = after.Add(l.margin).Add(l.u)
			continue
		}
		crossU = crossU.Add(l.u)
		crossMM = crossMM.Add(l.mm)
		out.PnL = l.u.Sub(l.u0).Round(2)
	}
	if crossMM.Sign() > 0 && wb.Add(crossU).LessThanOrEqual(crossMM) {
		for _, l := range legs {
			if !l.iso {
				r.Positions[l.i].Liq = true
			}
		}
	} else {
		after = after.Add(wb).Add(crossU)
	}
	r.Before, r.After = before.Round(2), after.Round(2)
	return r, nil
}
