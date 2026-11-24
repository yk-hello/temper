package trading

import (
	"github.com/shopspring/decimal"
)

// 保证金和强平价的计算（币安 U 本位永续、单向持仓）。全部是纯函数，方便用手算样例对照。
//
//   初始保证金   = 名义价值 ÷ 杠杆（名义价值按标记价）
//   维持保证金   = 名义价值 × 维持保证金率 − 速算数（按分档）
//   逐仓强平价   LP = (WB + cum − s·Q·EP) ÷ (Q·MMR − s·Q)            WB = 逐仓保证金
//   全仓强平价   LP = (WB − TMM1 + UPNL1 + cum − s·Q·EP) ÷ (Q·MMR − s·Q)
//                WB = 全仓钱包余额，TMM1、UPNL1 = 其他全仓仓位的维持保证金和未实现盈亏
//   s = 多 1 / 空 −1，Q = 持仓数量（正数），EP = 开仓均价

func lev(n int) decimal.Decimal { return decimal.NewFromInt(int64(n)) }

func upnl(p *Position, mark decimal.Decimal) decimal.Decimal {
	return p.Qty.Mul(mark.Sub(p.Entry))
}

// liqPrice 用给定的 WB（以及其他仓位的 TMM1、UPNL1）算强平价。没有强平价（亏不到爆仓）时返回 0。
// 分档取决于强平时的名义价值，所以迭代几次直到分档稳定。
func liqPrice(spec *Spec, p *Position, wb, tmm1, upnl1, mark decimal.Decimal) decimal.Decimal {
	if p.Qty.IsZero() {
		return zero
	}
	q := p.Qty.Abs()
	s := decimal.NewFromInt(int64(p.Qty.Sign()))
	b := spec.BracketFor(q.Mul(mark))
	var lp decimal.Decimal
	for i := 0; i < 6; i++ {
		num := wb.Sub(tmm1).Add(upnl1).Add(b.Cum).Sub(s.Mul(q).Mul(p.Entry))
		den := q.Mul(b.MMR).Sub(s.Mul(q))
		if den.IsZero() {
			return zero
		}
		lp = num.Div(den)
		if lp.Sign() <= 0 {
			return zero
		}
		nb := spec.BracketFor(q.Mul(lp))
		if nb.Floor.Equal(b.Floor) {
			break
		}
		b = nb
	}
	return lp.Round(8)
}

// accountMath 是一次计算用到的所有中间量。
type accountMath struct {
	crossWB    decimal.Decimal // 全仓钱包余额 = 钱包余额 − 逐仓保证金
	crossUPnL  decimal.Decimal
	crossIM    decimal.Decimal
	crossMM    decimal.Decimal
	isoMargin  decimal.Decimal // 全部逐仓保证金
	isoUPnL    decimal.Decimal
	isoMM      decimal.Decimal
	orderIM    decimal.Decimal
	upnl       decimal.Decimal
	available  decimal.Decimal
	equity     decimal.Decimal
	posIM      decimal.Decimal // 全仓按杠杆算的保证金 + 逐仓保证金
	marginRate decimal.Decimal // 全仓：维持保证金 ÷ 保证金余额
}

type marks func(inst string) (decimal.Decimal, bool)

// transferable 最多能转出多少：可用余额减去全仓浮盈（币安也这样，赚的要平仓才能转），而且钱包不能转成负数。
// 浮亏照样扣（已经算在可用里了）。
func transferable(m accountMath) decimal.Decimal {
	x := m.available.Sub(decimal.Max(zero, m.crossUPnL))
	x = decimal.Min(x, m.crossWB)
	return decimal.Max(zero, x)
}

func (e *Engine) compute(a *Account, mark marks) accountMath {
	var m accountMath
	for inst, p := range a.Pos {
		if p.Qty.IsZero() {
			continue
		}
		spec, ok := e.Specs.Get(inst)
		px, okm := mark(inst)
		if !ok || !okm {
			px = p.Entry
		}
		set := a.setting(inst)
		n := p.Qty.Abs().Mul(px)
		u := upnl(p, px)
		var mm decimal.Decimal
		if ok {
			mm = spec.MaintMargin(n)
		}
		m.upnl = m.upnl.Add(u)
		if set.Mode == "isolated" {
			m.isoMargin = m.isoMargin.Add(p.IsoMargin)
			m.isoUPnL = m.isoUPnL.Add(u)
			m.isoMM = m.isoMM.Add(mm)
			m.posIM = m.posIM.Add(p.IsoMargin)
		} else {
			m.crossUPnL = m.crossUPnL.Add(u)
			m.crossIM = m.crossIM.Add(n.Div(lev(set.Leverage)))
			m.crossMM = m.crossMM.Add(mm)
			m.posIM = m.posIM.Add(n.Div(lev(set.Leverage)))
		}
	}
	for inst := range e.ordersByInst(a) {
		m.orderIM = m.orderIM.Add(e.orderIM(a, inst, nil, mark))
	}
	m.crossWB = a.Wallet.Sub(m.isoMargin)
	m.available = m.crossWB.Add(m.crossUPnL).Sub(m.crossIM).Sub(m.orderIM)
	m.equity = a.Wallet.Add(m.upnl)
	bal := m.crossWB.Add(m.crossUPnL)
	if m.crossMM.Sign() > 0 {
		if bal.Sign() <= 0 {
			m.marginRate = decimal.NewFromInt(1)
		} else {
			m.marginRate = m.crossMM.Div(bal)
		}
	}
	return m
}

func (e *Engine) ordersByInst(a *Account) map[string]bool {
	out := map[string]bool{}
	for _, o := range a.Orders {
		if !o.conditional() && !o.ReduceOnly && o.open() {
			out[o.Instrument] = true
		}
	}
	return out
}

// orderIM 某品种挂单占用的保证金。能抵消反方向持仓的部分不占保证金；买卖两边取较大的一边（单向持仓模式）；
// 再加上开单损失（价格比标记价差的限价单）。
// extra 是还没下的新单（算下单前后的差额用）。
func (e *Engine) orderIM(a *Account, inst string, extra *Order, mark marks) decimal.Decimal {
	var bq, bn, sq, sn, loss decimal.Decimal
	mk, okm := mark(inst)
	add := func(o *Order) {
		if o.Instrument != inst || o.ReduceOnly || o.conditional() || !(o.open() || o == extra) {
			return
		}
		px := o.Price
		if px.IsZero() { // 市价单（只在预估时出现）按标记价
			px = mk
		}
		r := o.remaining()
		// 开单损失（币安）：限价单的价格比标记价差（买单高于标记价、卖单低于标记价），差的那部分要先占着，
		// 不然一成交就是浮亏，可能马上被强平。= 数量 × |min(0, 方向 × (标记价 − 委托价))|
		if okm && o.Price.Sign() > 0 {
			if o.buy() && px.GreaterThan(mk) {
				loss = loss.Add(r.Mul(px.Sub(mk)))
			} else if !o.buy() && px.LessThan(mk) {
				loss = loss.Add(r.Mul(mk.Sub(px)))
			}
		}
		if o.buy() {
			bq, bn = bq.Add(r), bn.Add(r.Mul(px))
		} else {
			sq, sn = sq.Add(r), sn.Add(r.Mul(px))
		}
	}
	for _, o := range a.Orders {
		add(o)
	}
	if extra != nil {
		add(extra)
	}
	var q decimal.Decimal
	if p, ok := a.Pos[inst]; ok {
		q = p.Qty
	}
	eff := func(qty, notional, offset decimal.Decimal) decimal.Decimal {
		if qty.IsZero() {
			return zero
		}
		left := decimal.Max(zero, qty.Sub(decimal.Max(zero, offset)))
		return notional.Mul(left).Div(qty)
	}
	need := decimal.Max(eff(bq, bn, q.Neg()), eff(sq, sn, q))
	return need.Div(lev(a.setting(inst).Leverage)).Add(loss)
}

// positionLiq 一个仓位当前的强平价。
func (e *Engine) positionLiq(a *Account, inst string, mark marks) decimal.Decimal {
	p := a.Pos[inst]
	spec, ok := e.Specs.Get(inst)
	if p == nil || p.Qty.IsZero() || !ok {
		return zero
	}
	px, okm := mark(inst)
	if !okm {
		px = p.Entry
	}
	if a.setting(inst).Mode == "isolated" {
		return liqPrice(spec, p, p.IsoMargin, zero, zero, px)
	}
	// 全仓：其他全仓仓位的维持保证金和未实现盈亏
	var tmm, up, iso decimal.Decimal
	for i2, p2 := range a.Pos {
		if p2.Qty.IsZero() {
			continue
		}
		if a.setting(i2).Mode == "isolated" {
			iso = iso.Add(p2.IsoMargin)
			continue
		}
		if i2 == inst {
			continue
		}
		s2, ok := e.Specs.Get(i2)
		m2, okm := mark(i2)
		if !ok || !okm {
			m2 = p2.Entry
		}
		if ok {
			tmm = tmm.Add(s2.MaintMargin(p2.Qty.Abs().Mul(m2)))
		}
		up = up.Add(upnl(p2, m2))
	}
	return liqPrice(spec, p, a.Wallet.Sub(iso), tmm, up, px)
}

// fillPos 把一笔成交作用到仓位上，返回已实现盈亏。apply 和预估共用，保证两边算法一致。
func fillPos(set *Setting, p *Position, buy bool, qty, px decimal.Decimal) (realized decimal.Decimal, opened bool) {
	s := sideSign(buy)
	q0 := p.Qty
	if q0.IsZero() || q0.Sign() == s.Sign() {
		nq := q0.Add(qty.Mul(s))
		p.Entry = q0.Abs().Mul(p.Entry).Add(qty.Mul(px)).Div(nq.Abs())
		if set.Mode == "isolated" {
			p.IsoMargin = p.IsoMargin.Add(qty.Mul(px).Div(lev(set.Leverage)))
		}
		p.Qty = nq
		return zero, q0.IsZero()
	}
	closeQ := decimal.Min(qty, q0.Abs())
	realized = closeQ.Mul(px.Sub(p.Entry)).Mul(decimal.NewFromInt(int64(q0.Sign())))
	if set.Mode == "isolated" {
		p.IsoMargin = p.IsoMargin.Sub(p.IsoMargin.Mul(closeQ).Div(q0.Abs()))
	}
	p.Qty = q0.Add(closeQ.Mul(s))
	if p.Qty.IsZero() {
		p.Entry, p.IsoMargin = zero, zero
	}
	if rest := qty.Sub(closeQ); rest.Sign() > 0 {
		p.Qty = rest.Mul(s)
		p.Entry = px
		if set.Mode == "isolated" {
			p.IsoMargin = rest.Mul(px).Div(lev(set.Leverage))
		}
		opened = true
	}
	return realized, opened
}
