package live

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"qs/internal/trading"
)

// 系统下单（18 第 5.2 条）：跟随和执行算法（portfolio.Work）在交易所账户上下单用。
// 和模拟盘引擎的同名方法一个意思，这样 trading.Mux 能按账户编号分过来。

var _ trading.Executor = (*Desks)(nil)

// PlaceAs 系统下单：来源记成 source（交易所上的客户端编号带着它，复盘按来源分）。
func (d *Desks) PlaceAs(acct int64, r trading.OrderReq, source, tag string) (*trading.Order, error) {
	r.SetSource(source)
	o, err := d.Place(acct, r)
	if err != nil {
		return nil, err
	}
	o.Tag = tag
	d.settle(acct, o)
	d.withFee(acct, o)
	return o, nil
}

// settle 下单回复里有成交、却没有均价（币安的回复常常先于撮合结果；模拟交易更明显）：等成交推送写进记录再取，最多等 2 秒。
// 11-16 跟随记录里交易所账户的成交价全是 0、手续费也算成 0，就是这里拿到的回复不全。
func (d *Desks) settle(acct int64, o *trading.Order) {
	if o == nil || o.Filled.Sign() <= 0 || o.AvgPx.Sign() > 0 || d.S.pool == nil || d.ctx == nil {
		return
	}
	for i := 0; i < 14; i++ {
		ctx, cancel := context.WithTimeout(d.ctx, 2*time.Second)
		f, a, ok := d.fillsOf(ctx, acct, o.ID)
		cancel()
		if ok && f.GreaterThanOrEqual(o.Filled) {
			o.Filled, o.AvgPx = f, a
			return
		}
		select {
		case <-d.ctx.Done():
			return
		case <-time.After(150 * time.Millisecond):
		}
	}
}

// SourceFills 某个来源在一段时间里、一个品种一个方向上的成交合计（数量、均价、USDT 手续费）。
// 跟随在服务重启时还没做完的那一笔，用它补上真实结果（11-16：重启前挂在交易所的单，停机期间成交了，记录却写着"中断、成交 0"）。
func (d *Desks) SourceFills(acct int64, inst, side, source string, from, to int64) (qty, avg, fee decimal.Decimal) {
	if d.S.pool == nil || d.ctx == nil {
		return zero, zero, zero
	}
	prefix := clientID(source)
	if len(prefix) < 4 {
		return zero, zero, zero
	}
	ctx, cancel := context.WithTimeout(d.ctx, 5*time.Second)
	defer cancel()
	var cost decimal.Decimal
	if err := d.S.pool.QueryRow(ctx, `select coalesce(sum(qty),0), coalesce(sum(qty * px),0), coalesce(sum(case when fee_asset = 'USDT' then fee else 0 end),0)
		from live_fills where account = $1 and symbol = $2 and side = $3 and left(client_id, 4) = $4 and t >= $5 and t <= $6`,
		acct, symOf(inst), side, prefix[:4], from, to).Scan(&qty, &cost, &fee); err != nil || qty.Sign() <= 0 {
		return zero, zero, zero
	}
	return qty, cost.Div(qty), fee
}

// CloseAs 系统平仓：只减仓的市价单平掉整个仓位。
func (d *Desks) CloseAs(acct int64, inst, source, tag string) (*trading.Order, error) {
	v, err := d.View(acct)
	if err != nil {
		return nil, err
	}
	for _, p := range v.Positions {
		if p.Instrument != inst || p.Qty.IsZero() {
			continue
		}
		side := "sell"
		if p.Side == "short" {
			side = "buy"
		}
		return d.PlaceAs(acct, trading.OrderReq{Instrument: inst, Side: side, Type: trading.Market, Qty: p.Qty, ReduceOnly: true}, source, tag)
	}
	return nil, trading.Rej("NO_POSITION")
}

// CancelAs 系统撤单。撤的时候已经成交的部分，交易所的回复里有。
func (d *Desks) CancelAs(acct int64, id, reason string) (*trading.Order, error) {
	o, err := d.Cancel(acct, id)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(d.ctx, 5*time.Second)
	defer cancel()
	d.settle(acct, o)
	d.updateOrder(ctx, acct, id, trading.StCanceled, o.Filled, o.AvgPx, reason)
	d.withFee(acct, o)
	return o, nil
}

// CancelSource 撤掉某个来源还挂着的单（不动用户自己的单和仓位上的止盈止损）。
func (d *Desks) CancelSource(acct int64, source, reason string) (int, error) {
	v, err := d.View(acct)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, o := range v.Orders {
		if o.Source != source || o.ClosePos {
			continue
		}
		if _, err := d.CancelAs(acct, o.ID, reason); err == nil {
			n++
		}
	}
	return n, nil
}

// OrderOf 查一张委托：还挂着的看交易所的最新状态；结束了的看本地记录（推送和每分钟的补读都写进去），成交数量、均价、手续费按成交记录算。
func (d *Desks) OrderOf(acct int64, id string) *trading.Order {
	if v, err := d.View(acct); err == nil {
		for _, o := range v.Orders {
			if o.ID == id {
				c := *o
				d.withFee(acct, &c)
				return &c
			}
		}
	}
	if d.S.pool == nil || d.ctx == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(d.ctx, 5*time.Second)
	defer cancel()
	o := &trading.Order{ID: id, Account: acct}
	var sym, price, qty, filled, avg string
	if err := d.S.pool.QueryRow(ctx, `select symbol, side, type, price::text, qty::text, filled::text, avg_px::text, status, source, created, updated from live_orders where account=$1 and id=$2`, acct, id).
		Scan(&sym, &o.Side, &o.Type, &price, &qty, &filled, &avg, &o.Status, &o.Source, &o.Created, &o.Updated); err != nil {
		return nil
	}
	o.Instrument = instOf(sym)
	o.Price, o.Qty, o.Filled, o.AvgPx = num(price), num(qty), num(filled), num(avg)
	if f, a, ok := d.fillsOf(ctx, acct, id); ok && (f.GreaterThan(o.Filled) || o.AvgPx.Sign() <= 0) {
		o.Filled, o.AvgPx = f, a // 推送丢了、本地状态没更新，或者没写均价：以成交记录为准
	}
	d.withFee(acct, o)
	return o
}

// ManualFills 序号 after 之后，用户自己下的单（这里手动下的、平仓按钮、条件单，以及在币安 App 上下的）有成交的品种。
// 止盈止损、强平、跟随、机器人、信号的不算（和模拟盘一样）。
func (d *Desks) ManualFills(acct, after int64) (map[string]bool, int64) {
	if d.S.pool == nil || d.ctx == nil {
		return nil, after
	}
	ctx, cancel := context.WithTimeout(d.ctx, 5*time.Second)
	defer cancel()
	rows, err := d.S.pool.Query(ctx, `select symbol, client_id, seq from live_fills where account=$1 and seq > $2 order by seq`, acct, after)
	if err != nil {
		return nil, after
	}
	defer rows.Close()
	last := after
	var out map[string]bool
	for rows.Next() {
		var sym, client string
		var seq int64
		if rows.Scan(&sym, &client, &seq) != nil {
			continue
		}
		last = max(last, seq)
		if client == "" || liqKind(client) != "" { // 补读来的没有客户端编号（分不出来源）；强平不算
			continue
		}
		if src := sourceOf(client); src == "manual" || src == "close" || src == "external" {
			if out == nil {
				out = map[string]bool{}
			}
			out[instOf(sym)] = true
		}
	}
	return out, last
}

// fillsOf 一张委托在本地成交记录里的数量和均价。
func (d *Desks) fillsOf(ctx context.Context, acct int64, id string) (decimal.Decimal, decimal.Decimal, bool) {
	oid, err := strconv.ParseInt(strings.TrimPrefix(id, "b"), 10, 64)
	if err != nil || !strings.HasPrefix(id, "b") {
		return zero, zero, false
	}
	var q, cost decimal.Decimal
	if err := d.S.pool.QueryRow(ctx, `select coalesce(sum(qty),0), coalesce(sum(qty * px),0) from live_fills where account=$1 and order_id=$2`, acct, oid).Scan(&q, &cost); err != nil || q.Sign() <= 0 {
		return zero, zero, false
	}
	return q, cost.Div(q), true
}

// withFee 手续费：本地成交记录里有就用记录的（USDT 计的），还没有就按这个账户在这个品种的真实费率估。
func (d *Desks) withFee(acct int64, o *trading.Order) {
	if o == nil || o.Filled.Sign() <= 0 || o.Fee.Sign() > 0 {
		return
	}
	if oid, err := strconv.ParseInt(strings.TrimPrefix(o.ID, "b"), 10, 64); err == nil && strings.HasPrefix(o.ID, "b") && d.S.pool != nil && d.ctx != nil {
		ctx, cancel := context.WithTimeout(d.ctx, 5*time.Second)
		defer cancel()
		var fee decimal.Decimal
		if d.S.pool.QueryRow(ctx, `select coalesce(sum(case when fee_asset='USDT' then fee else 0 end),0) from live_fills where account=$1 and order_id=$2`, acct, oid).Scan(&fee) == nil && fee.Sign() > 0 {
			o.Fee = fee
			return
		}
	}
	k, err := d.desk(acct)
	if err != nil {
		return
	}
	maker, taker := d.fees(k, o.Instrument)
	rate := taker
	if o.Type == trading.Limit && o.TIF == trading.GTX {
		rate = maker
	}
	o.Fee = o.Filled.Mul(o.AvgPx).Mul(rate)
}

func num(s string) decimal.Decimal {
	v, err := decimal.NewFromString(s)
	if err != nil {
		return zero
	}
	return v
}

// RoundPrice 价格对齐到价格单位：买单向下、卖单向上（和模拟盘一样）。
func (d *Desks) RoundPrice(inst string, buy bool, px decimal.Decimal) (decimal.Decimal, bool) {
	spec, ok := d.Specs.Get(inst)
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

// RoundQty 数量按数量单位向下取整。
func (d *Desks) RoundQty(inst string, q decimal.Decimal) (decimal.Decimal, bool) {
	spec, ok := d.Specs.Get(inst)
	if !ok {
		return zero, false
	}
	return spec.RoundQty(q), true
}

// RestPrice 挂单价：买单挂买一、卖单挂卖一，不比 limit 差（币安的真实盘口）。
func (d *Desks) RestPrice(inst string, buy bool, limit decimal.Decimal) (decimal.Decimal, bool) {
	b, ok := d.Books.Book(d.Venue, inst)
	if !ok {
		return zero, false
	}
	lv := b.Asks
	if buy {
		lv = b.Bids
	}
	if len(lv) == 0 {
		return zero, false
	}
	px := lv[0].Px
	if !limit.IsZero() && ((buy && px.GreaterThan(limit)) || (!buy && px.LessThan(limit))) {
		px = limit
	}
	return d.RoundPrice(inst, buy, px)
}
