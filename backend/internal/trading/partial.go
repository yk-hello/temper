package trading

import (
	"sort"

	"github.com/shopspring/decimal"
)

// 部分强平（D103：模拟资金不限额度后，大仓位到得了高档位，按币安的做法来）。
//
// 币安的说明（"Liquidation"帮助页，2026-11-14 读）：标记价触发后先撤掉委托，再"用一张很大的 IOC 单把仓位往市场里减，
// 尽量不全部强平"；减完、扣掉清算费以后保证金够维持剩下的仓位，强平就停；不够才全部强平。
// 币安没公开这张单有多大，这里取"刚好够"的最小数量（按数量步长向上取），按标记价成交，和全部强平用同一个价。
//
// 为什么小仓位几乎总是全部强平（和币安说的一致）：减掉 x 能少的维持保证金是 x × 那一档的维持保证金率，
// 要多付的清算费是 x × 清算费率（BTC 1.25%）。前几档的维持保证金率（0.4%–1%）比清算费率低，越减越不够；
// 只有高档位（2% 以上）的大仓位，减一部分才划算。

// partialQty 要平掉多少才够：找最小的 x（0 < x < qty），使
//
//	bal − x·mark·rate > other + 维持保证金(mark·(qty − x))
//
// bal：现在的保证金余额（逐仓 = 逐仓保证金 + 浮动盈亏；全仓 = 全仓钱包 + 全仓浮动盈亏），平仓按标记价成交时不变；
// other：别的仓位的维持保证金（逐仓为 0）。剩下的数量不能低于最小数量。找不到返回 0（要全部强平）。
func partialQty(spec *Spec, qty, mark, bal, other, rate decimal.Decimal) decimal.Decimal {
	if qty.Sign() <= 0 || mark.Sign() <= 0 || spec.Step.Sign() <= 0 || len(spec.Brackets) == 0 {
		return zero
	}
	g := func(x decimal.Decimal) decimal.Decimal {
		return bal.Sub(x.Mul(mark).Mul(rate)).Sub(other).Sub(spec.MaintMargin(mark.Mul(qty.Sub(x))))
	}
	// g 是分段直线，在名义价值跨过某一档下限的地方拐弯：按 x 从小到大的拐点切段
	n := qty.Mul(mark)
	cuts := []decimal.Decimal{zero}
	for i := len(spec.Brackets) - 1; i >= 0; i-- {
		if f := spec.Brackets[i].Floor; f.Sign() > 0 && f.LessThan(n) {
			cuts = append(cuts, qty.Sub(f.Div(mark)))
		}
	}
	cuts = append(cuts, qty)
	sort.Slice(cuts, func(i, j int) bool { return cuts[i].LessThan(cuts[j]) })
	ok := func(x decimal.Decimal) bool {
		rest := qty.Sub(x)
		return x.Sign() > 0 && rest.Sign() > 0 && rest.GreaterThanOrEqual(spec.MinQty) && g(x).Sign() > 0
	}
	for i := 0; i+1 < len(cuts); i++ {
		xa, xb := cuts[i], cuts[i+1]
		ga, gb := g(xa), g(xb)
		if gb.Sign() <= 0 && ga.Sign() <= 0 {
			continue
		}
		x := xa
		if ga.Sign() <= 0 { // 这一段里从不够变成够：解出交点
			x = xa.Add(ga.Neg().Mul(xb.Sub(xa)).Div(gb.Sub(ga)))
		}
		x = x.Div(spec.Step).Ceil().Mul(spec.Step)
		for k := 0; k < 3 && !ok(x) && x.LessThan(qty); k++ { // 取整误差：往上多试几步
			x = x.Add(spec.Step)
		}
		if ok(x) {
			return x
		}
	}
	return zero
}

// partialIso 逐仓部分强平：撤掉这个品种的委托，平掉一部分，清算费和平仓亏损都从这个仓位的逐仓保证金里出
// （剩下的仓位继续用它；全仓余额不受影响）。做了返回 true；减一部分不够就返回 false，由 liquidateIso 全部强平。
func (e *Engine) partialIso(a *Account, inst string, mark decimal.Decimal) bool {
	p := a.Pos[inst]
	spec, ok := e.Specs.Get(inst)
	if !ok || p == nil || p.Qty.IsZero() {
		return false
	}
	q := p.Qty.Abs()
	rate := e.liqRate(inst)
	x := partialQty(spec, q, mark, p.IsoMargin.Add(upnl(p, mark)), zero, rate)
	if x.Sign() <= 0 {
		return false
	}
	for _, o := range sortedOrders(a) {
		if o.Instrument == inst {
			e.closeOrder(a, o, StCanceled, "LIQUIDATION")
		}
	}
	before, iso0 := a.Wallet, p.IsoMargin
	realized := x.Mul(mark.Sub(p.Entry)).Mul(decimal.NewFromInt(int64(p.Qty.Sign())))
	fee := x.Mul(mark).Mul(rate)
	e.emit(Event{Kind: "fill", Account: a.ID, Fill: &FillEv{OrderID: "LIQ", Instrument: inst, Buy: p.Qty.Sign() < 0, Qty: x, Px: mark, Liq: true}})
	e.emit(Event{Kind: "liq_fee", Account: a.ID, LiqFee: &LiqFeeEv{Instrument: inst, Mark: mark, Amount: fee, Loss: before.Sub(a.Wallet).Add(fee), Qty: x, Partial: true}})
	// 成交时逐仓保证金按比例放回了全仓，这里挪回来：剩下的逐仓保证金 = 原来的 + 平仓盈亏 − 清算费
	if move := iso0.Add(realized).Sub(fee).Sub(a.Pos[inst].IsoMargin); !move.IsZero() {
		e.emit(Event{Kind: "iso_xfer", Account: a.ID, IsoXfer: &IsoXferEv{Instrument: inst, Amount: move}})
	}
	return true
}

// partialCross 全仓部分强平：撤掉全仓的委托，从维持保证金最大的仓位开始，找第一个"减一部分就够"的仓位减掉。
// 都不够返回 false，由 liquidateCross 全部强平。币安说的是"一张单"，所以只减一个仓位。
func (e *Engine) partialCross(a *Account, mk marks, m accountMath) bool {
	type cand struct {
		inst      string
		mm        decimal.Decimal
		mark      decimal.Decimal
		spec      *Spec
		qty, rate decimal.Decimal
	}
	var cs []cand
	for _, inst := range sortedInsts(a) {
		p := a.Pos[inst]
		if a.setting(inst).Mode == "isolated" || p.Qty.IsZero() {
			continue
		}
		spec, ok := e.Specs.Get(inst)
		mark, okm := mk(inst)
		if !ok || !okm {
			return false
		}
		cs = append(cs, cand{inst, spec.MaintMargin(p.Qty.Abs().Mul(mark)), mark, spec, p.Qty.Abs(), e.liqRate(inst)})
	}
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].mm.GreaterThan(cs[j].mm) })
	bal := m.crossWB.Add(m.crossUPnL)
	for _, c := range cs {
		x := partialQty(c.spec, c.qty, c.mark, bal, m.crossMM.Sub(c.mm), c.rate)
		if x.Sign() <= 0 {
			continue
		}
		for _, o := range sortedOrders(a) {
			if a.setting(o.Instrument).Mode != "isolated" {
				e.closeOrder(a, o, StCanceled, "LIQUIDATION")
			}
		}
		p := a.Pos[c.inst]
		before := a.Wallet
		fee := x.Mul(c.mark).Mul(c.rate)
		e.emit(Event{Kind: "fill", Account: a.ID, Fill: &FillEv{OrderID: "LIQ", Instrument: c.inst, Buy: p.Qty.Sign() < 0, Qty: x, Px: c.mark, Liq: true}})
		e.emit(Event{Kind: "liq_fee", Account: a.ID, LiqFee: &LiqFeeEv{Instrument: c.inst, Mark: c.mark, Amount: fee, Loss: before.Sub(a.Wallet).Add(fee), Qty: x, Partial: true}})
		return true
	}
	return false
}
