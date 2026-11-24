// Package spot 现货模拟交易（D44、D83）：没有杠杆，余额按币种算，手续费从收到的币里扣（和币安一样）。
// 和合约引擎分开：合约那边全是保证金、强平和资金费，现货一样都用不上。
// 事件溯源：所有状态变化都是事件，重放得到同样的状态。
package spot

import (
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"qs/internal/book"
)

// Quote 计价币。
const Quote = "USDT"

const (
	Market = "market"
	Limit  = "limit"
	Cond   = "cond_market" // 条件单：价格到了下市价单

	GTC = "GTC"
	IOC = "IOC"
	FOK = "FOK"
	GTX = "GTX"

	StNew      = "new"
	StPartial  = "partially_filled"
	StFilled   = "filled"
	StCanceled = "canceled"
)

var (
	zero = decimal.Zero
	one  = decimal.NewFromInt(1)
)

// Rej 拒单，Code 对应界面词条 err.<Code>。
type Rej struct {
	Code   string
	Params map[string]string
}

func (r *Rej) Error() string { return r.Code }

// RejectCode 见 trading.Reject。
func (r *Rej) RejectCode() string { return r.Code }

func rej(code string, kv ...string) *Rej {
	r := &Rej{Code: code, Params: map[string]string{}}
	for i := 0; i+1 < len(kv); i += 2 {
		r.Params[kv[i]] = kv[i+1]
	}
	return r
}

// Spec 现货规格。真实规格要从 exchangeInfo 读，接实盘时覆盖。
type Spec struct {
	Instrument  string          `json:"instrument"`
	Base        string          `json:"base"`
	Tick        decimal.Decimal `json:"tick"`
	Step        decimal.Decimal `json:"step"`
	MinNotional decimal.Decimal `json:"min_notional"`
	Fee         decimal.Decimal `json:"fee"`
}

// DefaultSpecs 6 个重点币。
func DefaultSpecs() map[string]*Spec {
	d := decimal.RequireFromString
	out := map[string]*Spec{}
	for _, x := range [][3]string{{"BTC", "0.01", "0.00001"}, {"ETH", "0.01", "0.0001"}, {"SOL", "0.01", "0.001"}, {"BNB", "0.01", "0.001"}, {"XRP", "0.0001", "0.1"}, {"DOGE", "0.00001", "1"}} {
		inst := x[0] + "-SPOT"
		out[inst] = &Spec{Instrument: inst, Base: x[0], Tick: d(x[1]), Step: d(x[2]), MinNotional: d("5"), Fee: d("0.001")}
	}
	return out
}

// Balance 一个币的余额。
type Balance struct {
	Free   decimal.Decimal `json:"free"`
	Locked decimal.Decimal `json:"locked"`
	Cost   decimal.Decimal `json:"cost"` // 这个币现在这些持仓的平均成本（计价币），USDT 为 0
}

// Order 委托。
type Order struct {
	ID         string          `json:"id"`
	Account    int64           `json:"account"`
	Instrument string          `json:"instrument"`
	Side       string          `json:"side"`
	Type       string          `json:"type"`
	TIF        string          `json:"tif,omitempty"`
	Price      decimal.Decimal `json:"price"`
	TriggerPx  decimal.Decimal `json:"trigger_px"`
	TriggerDir string          `json:"trigger_dir,omitempty"` // up | down
	Qty        decimal.Decimal `json:"qty"`
	Filled     decimal.Decimal `json:"filled"`
	AvgPx      decimal.Decimal `json:"avg_px"`
	Status     string          `json:"status"`
	Reason     string          `json:"reason,omitempty"`
	Source     string          `json:"source"`
	Tag        string          `json:"tag,omitempty"`
	Created    int64           `json:"created"`
	Updated    int64           `json:"updated"`
	QueueAhead decimal.Decimal `json:"queue_ahead"` // 只在查询结果里：限价挂单前面还排着多少（存在 e.queue，不进事件）
}

func (o *Order) buy() bool                  { return o.Side == "buy" }
func (o *Order) open() bool                 { return o.Status == StNew || o.Status == StPartial }
func (o *Order) remaining() decimal.Decimal { return o.Qty.Sub(o.Filled) }

// Fill 成交。Fee 按 FeeCoin 计，FeeUSDT 是折算后的。
type Fill struct {
	Seq        int64           `json:"seq"`
	Time       int64           `json:"time"`
	OrderID    string          `json:"order_id"`
	Instrument string          `json:"instrument"`
	Side       string          `json:"side"`
	Qty        decimal.Decimal `json:"qty"`
	Px         decimal.Decimal `json:"px"`
	Fee        decimal.Decimal `json:"fee"`
	FeeCoin    string          `json:"fee_coin"`
	FeeUSDT    decimal.Decimal `json:"fee_usdt"`
	Maker      bool            `json:"maker"`
	Realized   decimal.Decimal `json:"realized"` // 卖出时按平均成本算的盈亏，不含手续费
	Source     string          `json:"source"`
	Tag        string          `json:"tag,omitempty"`
}

// Account 一个模拟账户的现货钱包。
type Account struct {
	ID       int64
	Bal      map[string]*Balance
	Orders   map[string]*Order
	Done     []*Order
	Fills    []Fill
	Archived bool
}

func (a *Account) bal(coin string) *Balance {
	b := a.Bal[coin]
	if b == nil {
		b = &Balance{}
		a.Bal[coin] = b
	}
	return b
}

// Event 事件。
type Event struct {
	Seq     int64            `json:"seq"`
	T       int64            `json:"t"`
	Kind    string           `json:"kind"` // transfer | order | fill | close | archive
	Account int64            `json:"account"`
	Amount  *decimal.Decimal `json:"amount,omitempty"`
	Order   *Order           `json:"order,omitempty"`
	Fill    *Fill            `json:"fill,omitempty"`
	Close   *CloseEv         `json:"close,omitempty"`
}

// CloseEv 委托结束（撤单、IOC 剩余）。
type CloseEv struct {
	ID     string `json:"id"`
	Reason string `json:"reason,omitempty"`
}

// Engine 现货引擎。
type Engine struct {
	Books *book.Store
	Venue string
	Specs map[string]*Spec
	Log   *slog.Logger
	Now   func() int64
	// Guard 下单前过一遍（纪律锁）：buy 买入、notional 金额。source 不是 manual 的也过。
	Guard func(acct int64, inst, side, source string, notional decimal.Decimal) error
	// OnFill 成交后回调（在锁外，机器人用）。
	OnFill func(acct int64, f Fill, o Order)
	// Persist 批量写事件。
	Persist func([]Event) error

	mu       sync.Mutex
	accounts map[int64]*Account
	seq      int64
	pending  []Event
	notify   []func()
	queue    map[string]decimal.Decimal // 限价挂单前面还排着多少（委托 id → 数量）。只在内存里，重启后按当时盘口重新排到最后
}

// New 新建。挂单按市场成交排队撮合（和合约一样，审查第 3 条）。
func New(books *book.Store, venue string, log *slog.Logger) *Engine {
	e := &Engine{Books: books, Venue: venue, Specs: DefaultSpecs(), Log: log, accounts: map[int64]*Account{}, queue: map[string]decimal.Decimal{}}
	if books != nil {
		books.OnTrade(e.onTrade)
	}
	return e
}

// levelQty 盘口这一边在这个价位挂着多少（不在前 N 档里的算 0）。
func levelQty(lv []book.Level, px decimal.Decimal) decimal.Decimal {
	for _, l := range lv {
		if l.Px.Equal(px) {
			return l.Qty
		}
	}
	return zero
}

// queueAhead 这张挂单前面还排着多少。第一次问时按现在盘口这一档的量算：刚挂上，排在最后。
func (e *Engine) queueAhead(o *Order) decimal.Decimal {
	if q, ok := e.queue[o.ID]; ok {
		return q
	}
	q := zero
	if b, ok := e.Books.Book(e.Venue, o.Instrument); ok {
		lv := b.Asks
		if o.buy() {
			lv = b.Bids
		}
		q = levelQty(lv, o.Price)
	}
	e.queue[o.ID] = q
	return q
}

// onTrade 市场成交：主动方向着我们来的成交才可能成交到挂单。穿过挂单价说明这一档已经吃完，
// 恰好在挂单价的先吃前面排着的；一笔成交给我们的不超过它本身。
func (e *Engine) onTrade(venue, inst string, px, qty decimal.Decimal, buy bool, _ int64) {
	if venue != e.Venue {
		return
	}
	e.mu.Lock()
	s := e.Specs[inst]
	if s == nil {
		e.mu.Unlock()
		return
	}
	type cb struct {
		acct int64
		f    Fill
		o    Order
	}
	var after []cb
	ids := make([]int64, 0, len(e.accounts))
	for id := range e.accounts {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		a := e.accounts[id]
		if a.Archived {
			continue
		}
		for _, oid := range orderIDs(a) {
			o := a.Orders[oid]
			if o.Instrument != inst || o.Type != Limit || !o.open() || o.buy() == buy {
				continue
			}
			through := (o.buy() && px.LessThan(o.Price)) || (!o.buy() && px.GreaterThan(o.Price))
			if !through && !px.Equal(o.Price) {
				continue
			}
			take := qty
			ahead := e.queueAhead(o)
			if through {
				ahead = zero
			} else {
				d := decimal.Min(ahead, take)
				ahead, take = ahead.Sub(d), take.Sub(d)
			}
			e.queue[oid] = ahead
			q := decimal.Min(o.remaining(), take).Div(s.Step).Floor().Mul(s.Step)
			if !q.IsPositive() {
				continue
			}
			snap := *o
			for _, f := range e.applyPlan(a, s, oid, []fillPlan{{q, o.Price}}, true) {
				after = append(after, cb{id, f, snap})
			}
			if cur := a.Orders[oid]; cur == nil || !cur.open() {
				delete(e.queue, oid)
			}
		}
	}
	on := e.OnFill
	e.mu.Unlock()
	if on != nil {
		for _, c := range after {
			on(c.acct, c.f, c.o)
		}
	}
}

func (e *Engine) now() int64 {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now().UnixMilli()
}

func (e *Engine) emit(ev Event) {
	e.seq++
	ev.Seq = e.seq
	if ev.T == 0 {
		ev.T = e.now()
	}
	e.apply(ev)
	e.pending = append(e.pending, ev)
}

func (e *Engine) acct(id int64) *Account {
	a := e.accounts[id]
	if a == nil {
		a = &Account{ID: id, Bal: map[string]*Balance{}, Orders: map[string]*Order{}}
		e.accounts[id] = a
	}
	return a
}

// apply 把事件作用到状态上。确定性的，不做校验。
func (e *Engine) apply(ev Event) {
	a := e.acct(ev.Account)
	switch ev.Kind {
	case "archive":
		a.Archived = true
	case "transfer":
		q := a.bal(Quote)
		q.Free = q.Free.Add(*ev.Amount)
	case "order":
		o := *ev.Order
		a.Orders[o.ID] = &o
		if o.Type == Limit {
			lockCoin, amt := lockOf(&o, e.baseOf(o.Instrument), o.Qty)
			b := a.bal(lockCoin)
			b.Free = b.Free.Sub(amt)
			b.Locked = b.Locked.Add(amt)
		}
	case "fill":
		f := *ev.Fill
		f.Seq, f.Time = ev.Seq, ev.T
		o := a.Orders[f.OrderID]
		if o == nil {
			return
		}
		base := e.baseOf(o.Instrument)
		qb, bb, cost := a.bal(Quote), a.bal(base), f.Qty.Mul(f.Px)
		if o.buy() {
			if o.Type == Limit { // 挂的时候按限价锁的，比限价便宜的差额退回
				locked := f.Qty.Mul(o.Price)
				qb.Locked = qb.Locked.Sub(locked)
				qb.Free = qb.Free.Add(locked).Sub(cost)
			} else {
				qb.Free = qb.Free.Sub(cost)
			}
			held := bb.Free.Add(bb.Locked)
			got := f.Qty.Sub(f.Fee)
			if held.Add(got).IsPositive() {
				bb.Cost = held.Mul(bb.Cost).Add(cost).Div(held.Add(got))
			}
			bb.Free = bb.Free.Add(got)
		} else {
			if o.Type == Limit {
				bb.Locked = bb.Locked.Sub(f.Qty)
			} else {
				bb.Free = bb.Free.Sub(f.Qty)
			}
			f.Realized = f.Px.Sub(bb.Cost).Mul(f.Qty)
			qb.Free = qb.Free.Add(cost).Sub(f.Fee)
		}
		total := o.Filled.Add(f.Qty)
		o.AvgPx = o.AvgPx.Mul(o.Filled).Add(cost).Div(total)
		o.Filled = total
		o.Updated = ev.T
		o.Status = StPartial
		if o.remaining().IsZero() {
			o.Status = StFilled
			delete(a.Orders, o.ID)
			a.Done = keep(append(a.Done, o))
		}
		f.Source, f.Tag = o.Source, o.Tag
		a.Fills = keep(append(a.Fills, f))
	case "close":
		o := a.Orders[ev.Close.ID]
		if o == nil {
			return
		}
		if o.Type == Limit { // 还锁着的退回去
			coin, amt := lockOf(o, e.baseOf(o.Instrument), o.remaining())
			b := a.bal(coin)
			b.Locked = b.Locked.Sub(amt)
			b.Free = b.Free.Add(amt)
		}
		o.Status, o.Reason, o.Updated = StCanceled, ev.Close.Reason, ev.T
		delete(a.Orders, o.ID)
		a.Done = keep(append(a.Done, o))
	}
}

func keep[T any](s []T) []T {
	if len(s) > 2000 {
		return s[len(s)-2000:]
	}
	return s
}

func (e *Engine) baseOf(inst string) string {
	if s := e.Specs[inst]; s != nil {
		return s.Base
	}
	return strings.TrimSuffix(inst, "-SPOT")
}

func lockOf(o *Order, base string, qty decimal.Decimal) (string, decimal.Decimal) {
	if o.buy() {
		return Quote, qty.Mul(o.Price)
	}
	return base, qty
}

// Restore 启动时重放。
func (e *Engine) Restore(evs []Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, ev := range evs {
		e.apply(ev)
		if ev.Seq > e.seq {
			e.seq = ev.Seq
		}
	}
}

// Flush 把没写的事件写掉。
func (e *Engine) Flush() error {
	e.mu.Lock()
	batch := e.pending
	e.pending = nil
	e.mu.Unlock()
	if len(batch) == 0 || e.Persist == nil {
		return nil
	}
	if err := e.Persist(batch); err != nil {
		e.mu.Lock()
		e.pending = append(batch, e.pending...)
		e.mu.Unlock()
		return err
	}
	return nil
}

// ---------- 下单 ----------

// OrderReq 下单请求。
type OrderReq struct {
	Instrument string          `json:"instrument"`
	Side       string          `json:"side"`
	Type       string          `json:"type"`
	TIF        string          `json:"tif"`
	Price      decimal.Decimal `json:"price"`
	Qty        decimal.Decimal `json:"qty"`
	QuoteQty   decimal.Decimal `json:"quote_qty"` // 按金额下：只支持市价和限价买入、卖出时换算
	TriggerPx  decimal.Decimal `json:"trigger_px"`
	Source     string          `json:"-"`
	Tag        string          `json:"-"`
}

type fillPlan struct {
	qty, px decimal.Decimal
}

func (e *Engine) freshBook(inst string) (book.Book, error) {
	b, ok := e.Books.Book(e.Venue, inst)
	if !ok || len(b.Bids) == 0 || len(b.Asks) == 0 {
		return b, rej("NO_BOOK")
	}
	if e.now()-b.Time > 5000 {
		return b, rej("STALE_BOOK")
	}
	return b, nil
}

// walk 吃对手盘到限价为止（limit 为 0 不限），最多吃 qty。
func walk(levels []book.Level, qty, limit decimal.Decimal, buy bool) ([]fillPlan, decimal.Decimal) {
	var out []fillPlan
	left := qty
	for _, l := range levels {
		if left.IsZero() {
			break
		}
		if !limit.IsZero() && ((buy && l.Px.GreaterThan(limit)) || (!buy && l.Px.LessThan(limit))) {
			break
		}
		q := decimal.Min(left, l.Qty)
		if q.IsPositive() {
			out = append(out, fillPlan{q, l.Px})
			left = left.Sub(q)
		}
	}
	return out, left
}

// Place 下单。
func (e *Engine) Place(acct int64, r OrderReq) (*Order, error) {
	if r.Source == "" {
		r.Source = "manual"
	}
	e.mu.Lock()
	o, fills, err := e.place(acct, r)
	cb := e.OnFill
	var after []func()
	if err == nil && cb != nil {
		for _, f := range fills {
			f, oo := f, *o
			after = append(after, func() { cb(acct, f, oo) })
		}
	}
	e.mu.Unlock()
	for _, f := range after {
		f()
	}
	return o, err
}

func (e *Engine) place(acct int64, r OrderReq) (*Order, []Fill, error) {
	a := e.accounts[acct]
	if a == nil {
		a = e.acct(acct)
	}
	if a.Archived {
		return nil, nil, rej("ACCOUNT_ARCHIVED")
	}
	s := e.Specs[r.Instrument]
	if s == nil {
		return nil, nil, rej("UNKNOWN_INSTRUMENT")
	}
	if r.Side != "buy" && r.Side != "sell" {
		return nil, nil, rej("BAD_REQUEST")
	}
	if r.Type == "" {
		r.Type = Limit
	}
	if r.Type != Market && r.Type != Limit && r.Type != Cond {
		return nil, nil, rej("BAD_REQUEST")
	}
	b, err := e.freshBook(r.Instrument)
	if err != nil {
		return nil, nil, err
	}
	buy := r.Side == "buy"
	ref := r.Price
	if r.Type != Limit {
		ref = b.Asks[0].Px
		if !buy {
			ref = b.Bids[0].Px
		}
	}
	if r.Type == Limit {
		if !r.Price.IsPositive() {
			return nil, nil, rej("BAD_PRICE")
		}
		if !r.Price.Mod(s.Tick).IsZero() {
			return nil, nil, rej("BAD_TICK", "tick", s.Tick.String())
		}
	}
	if r.Type == Cond {
		if !r.TriggerPx.IsPositive() {
			return nil, nil, rej("BAD_TRIGGER")
		}
		ref = r.TriggerPx
	}
	qty := r.Qty
	if !qty.IsPositive() && r.QuoteQty.IsPositive() {
		qty = r.QuoteQty.Div(ref)
	}
	qty = qty.Div(s.Step).Floor().Mul(s.Step)
	if !qty.IsPositive() {
		return nil, nil, rej("BAD_QTY")
	}
	notional := qty.Mul(ref)
	if notional.LessThan(s.MinNotional) {
		return nil, nil, rej("MIN_NOTIONAL", "min", s.MinNotional.String())
	}
	if e.Guard != nil {
		if err := e.Guard(acct, r.Instrument, r.Side, r.Source, notional); err != nil {
			return nil, nil, err
		}
	}
	tif := r.TIF
	if r.Type == Limit && tif == "" {
		tif = GTC
	}
	now := e.now()
	o := &Order{ID: "S" + strconv.FormatInt(e.seq+1, 10), Account: acct, Instrument: r.Instrument, Side: r.Side, Type: r.Type, TIF: tif, Price: r.Price, Qty: qty, Status: StNew, Source: r.Source, Tag: r.Tag, Created: now, Updated: now}
	levels := b.Asks
	if !buy {
		levels = b.Bids
	}
	switch r.Type {
	case Cond:
		// 条件单不锁资金，触发时钱不够就撤（写明原因）
		o.TriggerPx = r.TriggerPx
		o.TriggerDir = "up"
		last := b.Asks[0].Px.Add(b.Bids[0].Px).Div(decimal.NewFromInt(2))
		if r.TriggerPx.LessThan(last) {
			o.TriggerDir = "down"
		}
		e.emit(Event{Kind: "order", Account: acct, Order: o})
		return a.Orders[o.ID], nil, nil
	case Market:
		plan, _ := walk(levels, qty, zero, buy)
		if err := e.checkFunds(a, s, buy, plan, qty); err != nil {
			return nil, nil, err
		}
		e.emit(Event{Kind: "order", Account: acct, Order: o})
		fills := e.applyPlan(a, s, o.ID, plan, false)
		if cur := a.Orders[o.ID]; cur != nil { // 盘口不够深：剩下的撤掉
			e.emit(Event{Kind: "close", Account: acct, Close: &CloseEv{ID: o.ID, Reason: "NO_LIQUIDITY"}})
		}
		return e.find(a, o.ID), fills, nil
	}
	// 限价
	plan, left := walk(levels, qty, r.Price, buy)
	if tif == GTX && len(plan) > 0 {
		return nil, nil, rej("WOULD_TAKE")
	}
	if tif == FOK && left.IsPositive() {
		return nil, nil, rej("FOK_NOT_FILLED")
	}
	coin, need := lockOf(o, s.Base, qty)
	if a.bal(coin).Free.LessThan(need) {
		return nil, nil, rej("INSUFFICIENT_BALANCE", "coin", coin, "need", need.Round(8).String(), "have", a.bal(coin).Free.Round(8).String())
	}
	e.emit(Event{Kind: "order", Account: acct, Order: o})
	fills := e.applyPlan(a, s, o.ID, plan, false)
	if tif == IOC {
		if a.Orders[o.ID] != nil {
			e.emit(Event{Kind: "close", Account: acct, Close: &CloseEv{ID: o.ID, Reason: "IOC"}})
		}
	}
	if cur := a.Orders[o.ID]; cur != nil && cur.open() {
		side := b.Asks
		if buy {
			side = b.Bids
		}
		e.queue[o.ID] = levelQty(side, o.Price) // 挂上了：排到这一档的最后
	}
	return e.find(a, o.ID), fills, nil
}

func (e *Engine) find(a *Account, id string) *Order {
	if o := a.Orders[id]; o != nil {
		return o
	}
	for i := len(a.Done) - 1; i >= 0 && i >= len(a.Done)-50; i-- {
		if a.Done[i].ID == id {
			return a.Done[i]
		}
	}
	return nil
}

func (e *Engine) checkFunds(a *Account, s *Spec, buy bool, plan []fillPlan, qty decimal.Decimal) error {
	if buy {
		cost := zero
		for _, p := range plan {
			cost = cost.Add(p.qty.Mul(p.px))
		}
		if a.bal(Quote).Free.LessThan(cost) {
			return rej("INSUFFICIENT_BALANCE", "coin", Quote, "need", cost.Round(2).String(), "have", a.bal(Quote).Free.Round(2).String())
		}
		return nil
	}
	if a.bal(s.Base).Free.LessThan(qty) {
		return rej("INSUFFICIENT_BALANCE", "coin", s.Base, "need", qty.String(), "have", a.bal(s.Base).Free.String())
	}
	return nil
}

func (e *Engine) applyPlan(a *Account, s *Spec, id string, plan []fillPlan, maker bool) []Fill {
	var out []Fill
	o := a.Orders[id]
	if o == nil {
		return nil
	}
	buy := o.buy()
	for _, p := range plan {
		f := Fill{OrderID: id, Instrument: o.Instrument, Side: o.Side, Qty: p.qty, Px: p.px, Maker: maker}
		if buy {
			f.Fee, f.FeeCoin = p.qty.Mul(s.Fee), s.Base
			f.FeeUSDT = f.Fee.Mul(p.px)
		} else {
			f.Fee, f.FeeCoin = p.qty.Mul(p.px).Mul(s.Fee), Quote
			f.FeeUSDT = f.Fee
		}
		e.emit(Event{Kind: "fill", Account: a.ID, Fill: &f})
		f.Source, f.Tag = o.Source, o.Tag
		out = append(out, f)
	}
	return out
}

// Cancel 撤单。
func (e *Engine) Cancel(acct int64, id, reason string) (*Order, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	a := e.accounts[acct]
	if a == nil || a.Orders[id] == nil {
		return nil, rej("ORDER_NOT_FOUND")
	}
	if reason == "" {
		reason = "USER"
	}
	o := a.Orders[id]
	e.emit(Event{Kind: "close", Account: acct, Close: &CloseEv{ID: o.ID, Reason: reason}})
	return e.find(a, id), nil
}

// CancelBuys 撤掉所有买单（现货里买就是开仓）：全局停机第二档用。返回撤了几张。
func (e *Engine) CancelBuys(acct int64, reason string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	a := e.accounts[acct]
	if a == nil {
		return 0
	}
	var ids []string
	for id, o := range a.Orders {
		if o.buy() && o.open() {
			ids = append(ids, id)
		}
	}
	for _, id := range ids {
		e.emit(Event{Kind: "close", Account: acct, Close: &CloseEv{ID: id, Reason: reason}})
	}
	return len(ids)
}

// OrderOf 看一张单（包括已结束的最近 50 张）。
func (e *Engine) OrderOf(acct int64, id string) *Order {
	e.mu.Lock()
	defer e.mu.Unlock()
	a := e.accounts[acct]
	if a == nil {
		return nil
	}
	if o := e.find(a, id); o != nil {
		c := *o
		return &c
	}
	return nil
}

// Transfer 合约钱包和现货钱包之间划转：正数转入现货，负数转出。余额够不够这里只查转出。
func (e *Engine) Transfer(acct int64, amount decimal.Decimal) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	a := e.acct(acct)
	if a.Archived {
		return rej("ACCOUNT_ARCHIVED")
	}
	if amount.IsNegative() && a.bal(Quote).Free.LessThan(amount.Neg()) {
		return rej("INSUFFICIENT_BALANCE", "coin", Quote, "need", amount.Neg().Round(2).String(), "have", a.bal(Quote).Free.Round(2).String())
	}
	e.emit(Event{Kind: "transfer", Account: acct, Amount: &amount})
	return nil
}

// Archive 账户归档：撤掉所有挂单，以后不能再下。
func (e *Engine) Archive(acct int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	a := e.accounts[acct]
	if a == nil {
		return
	}
	for _, id := range orderIDs(a) {
		e.emit(Event{Kind: "close", Account: acct, Close: &CloseEv{ID: id, Reason: "ARCHIVED"}})
	}
	e.emit(Event{Kind: "archive", Account: acct})
}

func orderIDs(a *Account) []string {
	ids := make([]string, 0, len(a.Orders))
	for id := range a.Orders {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Tick 撮合：盘口越过挂单价时算挂单方成交；条件单到价下市价。
func (e *Engine) Tick() {
	e.mu.Lock()
	type cb struct {
		acct int64
		f    Fill
		o    Order
	}
	var after []cb
	books := map[string]book.Book{}
	for inst := range e.Specs {
		b, err := e.freshBook(inst)
		if err == nil {
			books[inst] = b
		}
	}
	ids := make([]int64, 0, len(e.accounts))
	for id := range e.accounts {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	live := map[string]bool{}
	for _, id := range ids {
		a := e.accounts[id]
		if a.Archived {
			continue
		}
		for _, oid := range orderIDs(a) {
			o := a.Orders[oid]
			if o.Type == Limit && len(books) < len(e.Specs) {
				live[oid] = true // 盘口不全时不清它的排队
			}
			b, ok := books[o.Instrument]
			s := e.Specs[o.Instrument]
			if !ok || s == nil {
				continue
			}
			var fills []Fill
			switch o.Type {
			case Limit:
				live[oid] = true
				// 盘口越过挂单价：最多成交越过去的那一档的量（审查第 3 条：以前一次全部成交）
				top := zero
				switch {
				case o.buy() && b.Asks[0].Px.LessThan(o.Price):
					top = b.Asks[0].Qty
				case !o.buy() && b.Bids[0].Px.GreaterThan(o.Price):
					top = b.Bids[0].Qty
				}
				if top.IsPositive() {
					e.queue[oid] = zero
					if q := decimal.Min(o.remaining(), top).Div(s.Step).Floor().Mul(s.Step); q.IsPositive() {
						fills = e.applyPlan(a, s, oid, []fillPlan{{q, o.Price}}, true)
					}
				} else {
					// 前面有人撤单：这一档的量比记着的"前面"还少，就按这一档的量算（偏乐观）
					lv := b.Asks
					if o.buy() {
						lv = b.Bids
					}
					if l := levelQty(lv, o.Price); l.IsPositive() && l.LessThan(e.queueAhead(o)) {
						e.queue[oid] = l
					}
				}
			case Cond:
				mid := b.Asks[0].Px.Add(b.Bids[0].Px).Div(decimal.NewFromInt(2))
				if (o.TriggerDir == "up" && mid.GreaterThanOrEqual(o.TriggerPx)) || (o.TriggerDir == "down" && mid.LessThanOrEqual(o.TriggerPx)) {
					levels := b.Asks
					if !o.buy() {
						levels = b.Bids
					}
					plan, _ := walk(levels, o.remaining(), zero, o.buy())
					if err := e.checkFunds(a, s, o.buy(), plan, o.remaining()); err != nil {
						e.emit(Event{Kind: "close", Account: id, Close: &CloseEv{ID: oid, Reason: "INSUFFICIENT_BALANCE"}})
						continue
					}
					fills = e.applyPlan(a, s, oid, plan, false)
					if a.Orders[oid] != nil {
						e.emit(Event{Kind: "close", Account: id, Close: &CloseEv{ID: oid, Reason: "NO_LIQUIDITY"}})
					}
				}
			}
			if e.OnFill != nil {
				for _, f := range fills {
					snap := *o
					after = append(after, cb{id, f, snap})
				}
			}
		}
	}
	for id := range e.queue { // 结束了的委托不再记排队
		if !live[id] {
			delete(e.queue, id)
		}
	}
	on := e.OnFill
	e.mu.Unlock()
	for _, c := range after {
		on(c.acct, c.f, c.o)
	}
}

// ---------- 查询 ----------

// BalanceView 界面用。
type BalanceView struct {
	Coin   string          `json:"coin"`
	Free   decimal.Decimal `json:"free"`
	Locked decimal.Decimal `json:"locked"`
	Cost   decimal.Decimal `json:"cost"`
	Px     decimal.Decimal `json:"px"`
	Value  decimal.Decimal `json:"value"`
	Unreal decimal.Decimal `json:"unreal"`
}

// View 账户现货视图。
type View struct {
	Account  int64           `json:"account"`
	Equity   decimal.Decimal `json:"equity"`
	Balances []BalanceView   `json:"balances"`
	Orders   []Order         `json:"orders"`
	Done     []Order         `json:"done"`
	Fills    []Fill          `json:"fills"`
}

func (e *Engine) price(inst string) decimal.Decimal {
	b, ok := e.Books.Book(e.Venue, inst)
	if !ok || len(b.Bids) == 0 || len(b.Asks) == 0 {
		return zero
	}
	return b.Bids[0].Px.Add(b.Asks[0].Px).Div(decimal.NewFromInt(2))
}

// ViewOf 看账户。
func (e *Engine) ViewOf(acct int64) *View {
	e.mu.Lock()
	defer e.mu.Unlock()
	a := e.acct(acct)
	v := &View{Account: acct, Balances: []BalanceView{}, Orders: []Order{}, Done: []Order{}, Fills: []Fill{}}
	coins := make([]string, 0, len(a.Bal))
	for c := range a.Bal {
		coins = append(coins, c)
	}
	sort.Slice(coins, func(i, j int) bool {
		if (coins[i] == Quote) != (coins[j] == Quote) {
			return coins[i] == Quote
		}
		return coins[i] < coins[j]
	})
	for _, c := range coins {
		b := a.Bal[c]
		total := b.Free.Add(b.Locked)
		if total.IsZero() && c != Quote {
			continue
		}
		bv := BalanceView{Coin: c, Free: b.Free, Locked: b.Locked, Cost: b.Cost, Px: one, Value: total}
		if c != Quote {
			bv.Px = e.price(c + "-SPOT")
			bv.Value = total.Mul(bv.Px)
			bv.Unreal = bv.Px.Sub(b.Cost).Mul(total)
		}
		v.Equity = v.Equity.Add(bv.Value)
		v.Balances = append(v.Balances, bv)
	}
	for _, id := range orderIDs(a) {
		c := *a.Orders[id]
		if q, ok := e.queue[id]; ok && c.Type == Limit {
			c.QueueAhead = q
		}
		v.Orders = append(v.Orders, c)
	}
	for i := len(a.Done) - 1; i >= 0 && len(v.Done) < 100; i-- {
		v.Done = append(v.Done, *a.Done[i])
	}
	for i := len(a.Fills) - 1; i >= 0 && len(v.Fills) < 200; i-- {
		v.Fills = append(v.Fills, a.Fills[i])
	}
	return v
}

// Equity 现货总价值（USDT）。
func (e *Engine) Equity(acct int64) decimal.Decimal { return e.ViewOf(acct).Equity }

// Fills 全部成交（复盘用）。
func (e *Engine) Fills(acct int64) []Fill {
	e.mu.Lock()
	defer e.mu.Unlock()
	a := e.accounts[acct]
	if a == nil {
		return nil
	}
	return append([]Fill(nil), a.Fills...)
}

// Check 自检：余额不能是负的（测试和启动时用）。
func (e *Engine) Check() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for id, a := range e.accounts {
		for c, b := range a.Bal {
			if b.Free.IsNegative() || b.Locked.IsNegative() {
				return fmt.Errorf("账户 %d 的 %s 余额是负的：%s / %s", id, c, b.Free, b.Locked)
			}
		}
	}
	return nil
}
