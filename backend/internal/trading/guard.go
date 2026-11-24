package trading

import "github.com/shopspring/decimal"

// 模块 4 给引擎加的东西：下单预检（纪律锁）、撤开仓单、提醒、按来源查成交。

// PreOrder 开仓或加仓前的预检信息。减仓和平仓不经过预检（D07：任何时候都接受）。
type PreOrder struct {
	Account    int64
	Instrument string
	Side       string
	Source     string // manual | close | cond | ai | follow
	Qty        decimal.Decimal
	Price      decimal.Decimal // 预估成交价
	Stop       decimal.Decimal // 随单止损价，没带是 0
	Equity     decimal.Decimal
}

// NewReject 引擎外面的模块（纪律锁）用它拒单，前端按 code 翻译。
func NewReject(code string, kv ...string) *Reject { return rej(code, kv...) }

// guard 下单前过一遍预检。持有 e.mu 时调用，所以 e.Guard 不能回调引擎。
func (e *Engine) guard(a *Account, o *Order, est decimal.Decimal) error {
	if e.Guard == nil || o.ReduceOnly || o.ClosePos {
		return nil
	}
	if p := a.Pos[o.Instrument]; p != nil && !p.Qty.IsZero() && (p.Qty.Sign() > 0) != o.buy() && o.Qty.LessThanOrEqual(p.Qty.Abs()) {
		return nil // 只是减仓
	}
	px := est
	switch {
	case o.Price.Sign() > 0:
		px = o.Price
	case o.TriggerPx.Sign() > 0:
		px = o.TriggerPx
	}
	return e.Guard(PreOrder{Account: a.ID, Instrument: o.Instrument, Side: o.Side, Source: o.Source, Qty: o.Qty, Price: px, Stop: o.SL, Equity: e.compute(a, e.marks()).equity})
}

// CancelOpening 撤掉所有开仓单（普通委托和条件单），减仓单、止盈止损留着。返回撤了几张。
func (e *Engine) CancelOpening(acct int64, reason string) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, err := e.live(acct)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, o := range sortedOrders(a) {
		if !o.ReduceOnly && !o.ClosePos {
			e.closeOrder(a, o, StCanceled, reason)
			n++
		}
	}
	return n, nil
}

// Note 记一条提醒，交易页会弹出来。走事件，重启后还在。
func (e *Engine) Note(acct int64, kind, inst, reason string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, err := e.live(acct)
	if err != nil {
		return err
	}
	e.emit(Event{Kind: "notice", Account: a.ID, Note: &Notice{Kind: kind, Instrument: inst, Reason: reason}})
	return nil
}

// FillSeq 这个账户最新一笔成交的序号。
func (e *Engine) FillSeq(acct int64) int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, ok := e.accounts[acct]
	if !ok || len(a.Fills) == 0 {
		return 0
	}
	return a.Fills[len(a.Fills)-1].Seq
}

// FillsAfter 序号 after 之后的成交（通知用）。
func (e *Engine) FillsAfter(acct, after int64) []Fill {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, ok := e.accounts[acct]
	if !ok {
		return nil
	}
	i := len(a.Fills)
	for i > 0 && a.Fills[i-1].Seq > after {
		i--
	}
	return append([]Fill(nil), a.Fills[i:]...)
}

// ManualFills 序号 after 之后，用户手动下的单有成交的品种。止盈止损、强平、AI 的单都不算（4.8）。
func (e *Engine) ManualFills(acct, after int64) (map[string]bool, int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, ok := e.accounts[acct]
	if !ok {
		return nil, after
	}
	last := after
	var out map[string]bool
	for i := len(a.Fills) - 1; i >= 0 && a.Fills[i].Seq > after; i-- {
		f := a.Fills[i]
		last = max(last, f.Seq)
		if f.Liq || (f.Source != "manual" && f.Source != "close" && f.Source != "cond") {
			continue
		}
		if out == nil {
			out = map[string]bool{}
		}
		out[f.Instrument] = true
	}
	return out, last
}

// ClosedTrade 一张委托平仓的结果。Net = 已实现盈亏 − 手续费。
type ClosedTrade struct {
	OrderID    string
	Instrument string
	Net        decimal.Decimal
}

// ClosedTrades 序号 after 之后平仓的单，一张委托算一笔，按时间排。AI 的单不算（4.17）。
func (e *Engine) ClosedTrades(acct, after int64) ([]ClosedTrade, int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, ok := e.accounts[acct]
	if !ok {
		return nil, after
	}
	start := len(a.Fills)
	for start > 0 && a.Fills[start-1].Seq > after {
		start--
	}
	last := after
	var out []ClosedTrade
	at := map[string]int{}
	for _, f := range a.Fills[start:] {
		last = max(last, f.Seq)
		if f.Realized.IsZero() || f.Source == "ai" || f.Source == "follow" {
			continue
		}
		k := f.OrderID + "/" + f.Instrument
		i, seen := at[k]
		if !seen {
			i = len(out)
			at[k] = i
			out = append(out, ClosedTrade{OrderID: f.OrderID, Instrument: f.Instrument})
		}
		out[i].Net = out[i].Net.Add(f.Realized).Sub(f.Fee)
	}
	return out, last
}
