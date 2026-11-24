package live

import (
	"context"
	"time"

	"github.com/shopspring/decimal"

	"qs/internal/trading"
)

// 纪律锁（D80）对实盘账户也管用：下面四个方法和模拟引擎的同名方法一样，纪律锁按账户编号分过来。

// Brief 账户概要。
func (d *Desks) Brief(acct int64) (*trading.Brief, error) {
	v, err := d.View(acct)
	if err != nil {
		return nil, err
	}
	b := &trading.Brief{Initial: v.Initial, Wallet: v.Wallet, Equity: v.Equity, Available: decimal.Max(zero, v.Available), Pos: map[string]trading.PosBrief{}}
	for _, p := range v.Positions {
		q := p.Qty
		if p.Side == "short" {
			q = q.Neg()
		}
		b.Pos[p.Instrument] = trading.PosBrief{Qty: q, Entry: p.Entry}
	}
	return b, nil
}

// CancelOpening 撤掉会开新仓的委托（只减仓和止盈止损留着）。
func (d *Desks) CancelOpening(acct int64, reason string) (int, error) {
	v, err := d.View(acct)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, o := range v.Orders {
		if o.ReduceOnly || o.ClosePos {
			continue
		}
		if _, err := d.Cancel(acct, o.ID); err == nil {
			n++
			ctx, cancel := context.WithTimeout(d.ctx, 5*time.Second)
			d.updateOrder(ctx, acct, o.ID, trading.StCanceled, o.Filled, o.AvgPx, reason)
			cancel()
		}
	}
	return n, nil
}

// ClosedTrades 序号 after 之后平仓的单，一张委托算一笔。AI 的单不算。
func (d *Desks) ClosedTrades(acct, after int64) ([]trading.ClosedTrade, int64) {
	fills, _, _, err := d.Records(acct)
	if err != nil {
		return nil, after
	}
	last := after
	var out []trading.ClosedTrade
	at := map[string]int{}
	for _, f := range fills {
		if f.Seq <= after {
			continue
		}
		last = max(last, f.Seq)
		if f.Realized.IsZero() || f.Source == "ai" || f.Source == "follow" {
			continue
		}
		k := f.OrderID + "/" + f.Instrument
		i, seen := at[k]
		if !seen {
			i = len(out)
			at[k] = i
			out = append(out, trading.ClosedTrade{OrderID: f.OrderID, Instrument: f.Instrument})
		}
		out[i].Net = out[i].Net.Add(f.Realized).Sub(f.Fee)
	}
	return out, last
}

// Note 交易页弹一条提醒（比如纪律锁上锁了）。
func (d *Desks) Note(acct int64, kind, inst, reason string) error {
	k, err := d.desk(acct)
	if err != nil {
		return err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.notes = append(k.notes, trading.Notice{Seq: int64(len(k.notes) + 1), Time: time.Now().UnixMilli(), Kind: kind, Instrument: inst, Reason: reason})
	if len(k.notes) > 20 {
		k.notes = k.notes[len(k.notes)-20:]
	}
	return nil
}

// FillSeq 这个账户最新一笔成交的序号（高光、通知看有没有新成交）。
func (d *Desks) FillSeq(acct int64) int64 {
	if d.S.pool == nil || d.ctx == nil {
		return 0
	}
	var n int64
	ctx, cancel := context.WithTimeout(d.ctx, 5*time.Second)
	defer cancel()
	_ = d.S.pool.QueryRow(ctx, `select coalesce(max(seq),0) from live_fills where account=$1`, acct).Scan(&n)
	return n
}
