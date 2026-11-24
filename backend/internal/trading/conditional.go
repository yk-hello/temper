package trading

import (
	"github.com/shopspring/decimal"
)

// triggerRef 触发判断用的价格：标记价（默认）或最新成交价。
func (e *Engine) triggerRef(inst, by string, mark decimal.Decimal) (decimal.Decimal, bool) {
	if by == ByLast {
		if t, ok := e.Books.Ticker(e.Venue, inst); ok && t.Last.Sign() > 0 {
			return t.Last, true
		}
		return zero, false
	}
	if by == ByIndex {
		if t, ok := e.Books.Ticker(e.Venue, inst); ok && t.Index.Sign() > 0 {
			return t.Index, true
		}
		return mark, mark.Sign() > 0
	}
	return mark, mark.Sign() > 0
}

// fireCond 条件单触发：按原来的设置下一张市价单或限价单（走正常的下单校验，保证金不够、价格出界都会被拒）。
// 成功：条件单记为"已触发"，新委托的 Parent 指向它；失败：条件单记为"已过期"，原因就是拒单代码，并通知用户。
func (e *Engine) fireCond(a *Account, o *Order) {
	typ := Market
	if o.Type == CondLimit {
		typ = Limit
	}
	_, err := e.place(a, OrderReq{Instrument: o.Instrument, Side: o.Side, Type: typ, TIF: o.TIF, Price: o.Price, Qty: o.Qty,
		ReduceOnly: o.ReduceOnly, TP: o.TP, SL: o.SL, TriggerBy: o.TriggerBy, source: "cond", parent: o.ID})
	if cur, ok := a.Orders[o.ID]; !ok || cur.Status != StNew {
		return // 下单过程中已经被撤（比如只减仓单在仓位归零时被撤）
	}
	if err != nil {
		code := "REJECTED"
		if r, ok := err.(*Reject); ok {
			code = r.Code
		}
		e.closeOrder(a, o, StExpired, code)
		return
	}
	e.closeOrder(a, o, StTriggered, "TRIGGERED")
}

// amendCond 改条件单：触发价、限价、数量（图上拖动触发价线就是调这个）。改了触发价要按现价重新判断方向。
func (e *Engine) amendCond(a *Account, o *Order, price, qty, trigger decimal.Decimal) (*Order, error) {
	spec, _ := e.Specs.Get(o.Instrument)
	mark, ok := e.markOf(o.Instrument)
	if !ok {
		return nil, rej("MARKET_UNAVAILABLE")
	}
	if trigger.IsZero() {
		trigger = o.TriggerPx
	}
	if trigger.Sign() < 0 || !spec.OnTick(trigger) {
		return nil, rej("TRIGGER_TICK", "tick", fs(spec.Tick))
	}
	ref, ok := e.triggerRef(o.Instrument, o.TriggerBy, mark)
	if !ok {
		return nil, rej("MARKET_UNAVAILABLE")
	}
	if trigger.Equal(ref) {
		return nil, rej("TRIGGER_AT_PRICE", "px", fs(ref))
	}
	dir := "down"
	if trigger.GreaterThan(ref) {
		dir = "up"
	}
	if price.IsZero() {
		price = o.Price
	}
	if o.Type == CondLimit {
		if !spec.OnTick(price) {
			return nil, rej("PRICE_TICK", "tick", fs(spec.Tick))
		}
		if lo, hi := trigger.Mul(spec.PriceDown), trigger.Mul(spec.PriceUp); price.LessThan(lo) || price.GreaterThan(hi) {
			return nil, rej("PRICE_OUT_OF_RANGE", "min", fs(lo.RoundCeil(int32(-spec.Tick.Exponent()))), "max", fs(hi.RoundFloor(int32(-spec.Tick.Exponent()))))
		}
	} else {
		price = zero
	}
	if qty.IsZero() {
		qty = o.Qty
	}
	qty = spec.RoundQty(qty)
	if qty.LessThan(spec.MinQty) {
		return nil, rej("QTY_TOO_SMALL", "min", fs(spec.MinQty))
	}
	ref2 := trigger
	if o.Type == CondLimit {
		ref2 = price
	}
	if !o.ReduceOnly && qty.Mul(ref2).LessThan(spec.MinNotional) {
		return nil, rej("MIN_NOTIONAL", "min", fs(spec.MinNotional), "notional", fs(qty.Mul(ref2).Round(2)))
	}
	if qty.GreaterThan(o.Qty) || !trigger.Equal(o.TriggerPx) || !price.Equal(o.Price) {
		tmp := *o // 改大、改价按新单重新过纪律预检
		tmp.Price, tmp.Qty, tmp.TriggerPx = price, qty, trigger
		if err := e.guard(a, &tmp, ref2); err != nil {
			return nil, err
		}
	}
	e.emit(Event{Kind: "amend", Account: a.ID, Amend: &AmendEv{ID: o.ID, Price: price, Qty: qty, TriggerPx: trigger, TriggerDir: dir}})
	return e.findOrder(a, o.ID), nil
}

// MarginAdjust 逐仓调整保证金的结果（dry = 只预览）。
type MarginAdjust struct {
	LiqPx     decimal.Decimal `json:"liq_px"`     // 调整后的强平价（0 = 没有）
	Margin    decimal.Decimal `json:"margin"`     // 调整后的逐仓保证金
	MaxAdd    decimal.Decimal `json:"max_add"`    // 最多能追加（= 可用余额）
	MaxRemove decimal.Decimal `json:"max_remove"` // 最多能减少
}

// AdjustMargin 逐仓追加（amount > 0）或减少（amount < 0）保证金（币安"调整保证金"）。
// 最多能减少 = 逐仓保证金 + 浮亏（浮盈不算） − 按标记价算的初始保证金。
func (e *Engine) AdjustMargin(acct int64, inst string, amount decimal.Decimal, dry bool) (*MarginAdjust, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, err := e.live(acct)
	if err != nil {
		return nil, err
	}
	p := a.Pos[inst]
	if p == nil || p.Qty.IsZero() {
		return nil, rej("NO_POSITION")
	}
	set := a.setting(inst)
	if set.Mode != "isolated" {
		return nil, rej("NOT_ISOLATED")
	}
	mk := e.marks()
	mark, ok := mk(inst)
	if !ok {
		return nil, rej("MARKET_UNAVAILABLE")
	}
	m := e.compute(a, mk)
	maxAdd := decimal.Max(zero, m.available)
	maxRemove := decimal.Max(zero, p.IsoMargin.Add(decimal.Min(zero, upnl(p, mark))).Sub(p.Qty.Abs().Mul(mark).Div(lev(set.Leverage))))
	amount = amount.Round(2)
	switch {
	case amount.Sign() > 0 && amount.GreaterThan(maxAdd):
		return nil, rej("MARGIN_ADD_TOO_MUCH", "max", fs(maxAdd.RoundFloor(2)))
	case amount.Sign() < 0 && amount.Neg().GreaterThan(maxRemove):
		return nil, rej("MARGIN_REMOVE_TOO_MUCH", "max", fs(maxRemove.RoundFloor(2)))
	}
	// 预览：在账户副本上算调整后的强平价
	c := cloneAccount(a)
	c.Pos[inst].IsoMargin = c.Pos[inst].IsoMargin.Add(amount)
	out := &MarginAdjust{LiqPx: e.positionLiq(c, inst, mk), Margin: c.Pos[inst].IsoMargin.Round(8), MaxAdd: maxAdd.RoundFloor(2), MaxRemove: maxRemove.RoundFloor(2)}
	if spec, ok := e.Specs.Get(inst); ok && out.LiqPx.Sign() > 0 {
		out.LiqPx = out.LiqPx.Round(int32(-spec.Tick.Exponent()))
	}
	if dry || amount.IsZero() {
		return out, nil
	}
	e.emit(Event{Kind: "iso_xfer", Account: a.ID, IsoXfer: &IsoXferEv{Instrument: inst, Amount: amount}})
	return out, nil
}
