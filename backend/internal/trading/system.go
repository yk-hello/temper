package trading

import (
	"time"

	"github.com/shopspring/decimal"
)

// 给系统自己下单的地方用：AI 模型组合（系统账户，id 是负数，和用户账户不会冲突）和跟随层。
// 这些接口不经过 HTTP，用户碰不到；订单照样走 place，手续费、资金费、强平都和手动单一样（D74）。

// PosBrief 一个仓位的数量（带符号）和开仓均价。
type PosBrief struct {
	Qty   decimal.Decimal
	Entry decimal.Decimal
}

// Brief 账户概要，比 View 轻（调仓每次都要读）。
type Brief struct {
	Initial   decimal.Decimal
	Wallet    decimal.Decimal
	Equity    decimal.Decimal
	Transfers decimal.Decimal // 累计划转净额：算盈亏时要扣掉，划转不是赚亏
	Available decimal.Decimal
	Pos       map[string]PosBrief // 只含有仓位的品种
}

func (e *Engine) Brief(acct int64) (*Brief, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, ok := e.accounts[acct]
	if !ok {
		return nil, rej("NOT_FOUND")
	}
	m := e.compute(a, e.marks())
	b := &Brief{Initial: a.Initial, Wallet: a.Wallet, Transfers: a.Transfers, Equity: m.equity, Available: decimal.Max(zero, m.available), Pos: map[string]PosBrief{}}
	for inst, p := range a.Pos {
		if !p.Qty.IsZero() {
			b.Pos[inst] = PosBrief{Qty: p.Qty, Entry: p.Entry}
		}
	}
	return b, nil
}

// PlaceAs 系统下单：source 标来源（ai、follow、risk…），tag 记来自哪条信号。
func (e *Engine) PlaceAs(acct int64, r OrderReq, source, tag string) (*Order, error) {
	defer e.travel(acct)()
	e.mu.Lock()
	defer e.mu.Unlock()
	a, err := e.live(acct)
	if err == nil {
		err = e.rateCheck(acct)
	}
	if err != nil {
		return nil, err
	}
	r.source, r.tag = source, tag
	return e.place(a, r)
}

// CloseAs 系统平仓：市价平掉整个仓位。
func (e *Engine) CloseAs(acct int64, inst, source, tag string) (*Order, error) {
	defer e.travel(acct)()
	e.mu.Lock()
	defer e.mu.Unlock()
	a, err := e.live(acct)
	if err == nil {
		err = e.rateCheck(acct)
	}
	if err != nil {
		return nil, err
	}
	p, ok := a.Pos[inst]
	if !ok || p.Qty.IsZero() {
		return nil, rej("NO_POSITION")
	}
	side := "sell"
	if p.Qty.Sign() < 0 {
		side = "buy"
	}
	return e.place(a, OrderReq{Instrument: inst, Side: side, Type: Market, Qty: p.Qty.Abs(), ReduceOnly: true, source: source, tag: tag})
}

// CancelAs 系统撤单，reason 写明原因（不算用户撤的）。
func (e *Engine) CancelAs(acct int64, id, reason string) (*Order, error) {
	defer e.travel(acct)()
	e.mu.Lock()
	defer e.mu.Unlock()
	a, err := e.live(acct)
	if err != nil {
		return nil, err
	}
	o, ok := a.Orders[id]
	if !ok || o.Status == StTriggered {
		return nil, rej("NOT_FOUND")
	}
	e.closeOrder(a, o, StCanceled, reason)
	return e.findOrder(a, id), nil
}

// OrderOf 查一张委托（未结束或已结束的）。
func (e *Engine) OrderOf(acct int64, id string) *Order {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, ok := e.accounts[acct]
	if !ok {
		return nil
	}
	return e.findOrder(a, id)
}

// Open 这张委托还在挂着吗。
func (o *Order) Open() bool { return o.open() }

// Records 账户的全部成交和资金费（按时间顺序）。算战绩、算每笔交易用。
func (e *Engine) Records(acct int64) (fills []Fill, fundings []FundingRec, liqFees decimal.Decimal, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, ok := e.accounts[acct]
	if !ok {
		return nil, nil, zero, rej("NOT_FOUND")
	}
	return append([]Fill(nil), a.Fills...), append([]FundingRec(nil), a.Fundings...), a.LiqFees, nil
}

// Stops 账户止损的全部变化（按时间顺序）。复盘算 R 和"移动了止损"用。
func (e *Engine) Stops(acct int64) []StopRec {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, ok := e.accounts[acct]
	if !ok {
		return nil
	}
	return append([]StopRec(nil), a.Stops...)
}

// isStop 是止损单吗（条件开仓单不算）。
func (o *Order) isStop() bool { return o.Type == StopMarket && (o.ClosePos || o.ReduceOnly) }

// RoundPrice 把价格对齐到价格单位。买单向下、卖单向上，保证不比原价差。
func (e *Engine) RoundPrice(inst string, buy bool, px decimal.Decimal) (decimal.Decimal, bool) {
	spec, ok := e.Specs.Get(inst)
	if !ok {
		return zero, false
	}
	n := px.Div(spec.Tick)
	if buy {
		n = n.Floor()
	} else {
		n = n.Ceil()
	}
	return n.Mul(spec.Tick), true
}

// RoundQty 把数量按数量单位向下取整。
func (e *Engine) RoundQty(inst string, q decimal.Decimal) (decimal.Decimal, bool) {
	spec, ok := e.Specs.Get(inst)
	if !ok {
		return zero, false
	}
	return spec.RoundQty(q), true
}

// RestPrice 挂单用的价：买单挂买一、卖单挂卖一，而且不比 limit 差。
func (e *Engine) RestPrice(inst string, buy bool, limit decimal.Decimal) (decimal.Decimal, bool) {
	e.mu.Lock()
	lv := e.levels(inst, !buy) // levels 给的是对手盘（买单吃卖盘），这里要自己这一边
	opp := e.levels(inst, buy)
	e.mu.Unlock()
	if len(lv) == 0 {
		return zero, false
	}
	px := lv[0].Px
	if !within(buy, px, limit) {
		px = limit
	}
	px, ok := e.RoundPrice(inst, buy, px)
	if !ok {
		return zero, false
	}
	// 不越过对面最优价（11-16）：断线重连那一下盘口会短暂交叉（旧的买单还没清、新的卖单已经来了），
	// 挂"买一"反而吃到了卖盘，只挂单被拒，跟随这一笔就放弃了；币安的盘口不会交叉，同一笔在那边挂得上。
	// 买单最多挂到卖一下面一格，卖单最少挂到买一上面一格（和币安"排队价"一个意思）
	if spec, ok := e.Specs.Get(inst); ok && len(opp) > 0 && spec.Tick.Sign() > 0 {
		if buy && px.GreaterThanOrEqual(opp[0].Px) {
			px, _ = e.RoundPrice(inst, true, opp[0].Px.Sub(spec.Tick))
		} else if !buy && px.LessThanOrEqual(opp[0].Px) {
			px, _ = e.RoundPrice(inst, false, opp[0].Px.Add(spec.Tick))
		}
	}
	return px, px.Sign() > 0
}

// CancelSource 撤掉某个来源的所有挂单（不动用户自己的单，也不动持仓上的止盈止损）。
func (e *Engine) CancelSource(acct int64, source, reason string) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, err := e.live(acct)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, o := range sortedOrders(a) {
		if o.Source == source && o.Status != StTriggered {
			e.closeOrder(a, o, StCanceled, reason)
			n++
		}
	}
	return n, nil
}

// ActiveInstruments 有仓位或挂单的品种：重启后这些品种要接着收行情，不然标记价断了。
func (e *Engine) ActiveInstruments() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	seen := map[string]bool{}
	for _, a := range e.accounts {
		if a.Archived {
			continue
		}
		for inst, p := range a.Pos {
			if !p.Qty.IsZero() {
				seen[inst] = true
			}
		}
		for _, o := range a.Orders {
			seen[o.Instrument] = true
		}
	}
	out := make([]string, 0, len(seen))
	for inst := range seen {
		out = append(out, inst)
	}
	return out
}

// UndoWindow 撤单后多久内能撤回（D38，2.26）。
const UndoWindow = 5 * time.Second

// UndoCancel 撤回一张刚被用户撤掉的委托：按原来的价格和没成交的数量重新下一张。
// 只认用户自己撤的、窗口内的，同一张只能撤回一次。价格、保证金都要重新检查，过不了就照常拒绝。
func (e *Engine) UndoCancel(acct int64, id string) (*Order, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, err := e.live(acct)
	if err != nil {
		return nil, err
	}
	var o *Order
	for i := len(a.History) - 1; i >= 0; i-- {
		if a.History[i].ID == id {
			o = a.History[i]
			break
		}
	}
	if o == nil {
		return nil, rej("NOT_FOUND")
	}
	if o.Status != StCanceled || o.Reason != "USER" || o.ClosePos || e.now()-o.Updated > UndoWindow.Milliseconds() || e.undone[id] {
		return nil, rej("UNDO_EXPIRED")
	}
	left := o.Qty.Sub(o.Filled)
	if left.Sign() <= 0 {
		return nil, rej("UNDO_EXPIRED")
	}
	n, err := e.place(a, OrderReq{Instrument: o.Instrument, Side: o.Side, Type: o.Type, TIF: o.TIF, Price: o.Price, Qty: left, ReduceOnly: o.ReduceOnly,
		TP: o.TP, SL: o.SL, TriggerPx: o.TriggerPx, TriggerBy: o.TriggerBy, source: o.Source, tag: o.Tag})
	if err != nil {
		return nil, err
	}
	if e.undone == nil {
		e.undone = map[string]bool{}
	}
	e.undone[id] = true
	return n, nil
}

// Transfer 和现货钱包划转：正数转进合约，负数转出。转出上限见 transferable：
// 挂单和仓位占用的不能动，未实现盈利也不能转走（审查第 1 条：转走浮盈再爆仓，穿仓由系统补，钱就刷出来了）。
func (e *Engine) Transfer(acct int64, amount decimal.Decimal) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, err := e.live(acct)
	if err != nil {
		return err
	}
	if amount.IsNegative() {
		if mx := transferable(e.compute(a, e.marks())); mx.LessThan(amount.Neg()) {
			return rej("TRANSFER_LIMIT", "max", fs(mx.RoundFloor(2)))
		}
	}
	e.emit(Event{Kind: "transfer", Account: acct, Initial: &amount})
	return nil
}
