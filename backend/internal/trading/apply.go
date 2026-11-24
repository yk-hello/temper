package trading

import (
	"fmt"
	"strconv"

	"github.com/shopspring/decimal"

	"qs/internal/ledger"
)

// 复式账本的科目（模拟账户 id 作为账本里的"组合"名：paper:<id>）
const (
	acCash    = ledger.Cash
	acCapital = ledger.Capital
	acPnL     = ledger.RealizedPnL
	acFee     = ledger.Fee
	acFunding = ledger.Funding
	acLiq     = "expense:liquidation"
	maxKeep   = 5000 // 内存里每个账户保留的历史条数（更早的在数据库里）
)

func bookName(id int64) string { return "paper:" + strconv.FormatInt(id, 10) }

// apply 把一个事件作用到状态上。必须是确定性的：同样的事件序列重放出同样的状态。
// 不做任何校验和决策（那些在引擎里做完才生成事件）。
func (e *Engine) apply(ev Event) error {
	if ev.Kind == "open" {
		a := newAccount(ev.Account)
		a.Initial = *ev.Initial
		a.Wallet = *ev.Initial
		a.LastSeq = ev.Seq
		e.accounts[ev.Account] = a
		return e.post(ev, "开户", ledger.Line{Account: acCash, Amount: a.Initial}, ledger.Line{Account: acCapital, Amount: a.Initial.Neg()})
	}
	a, ok := e.accounts[ev.Account]
	if !ok {
		return fmt.Errorf("事件 %d：账户 %d 不存在", ev.Seq, ev.Account)
	}
	a.LastSeq = ev.Seq
	switch ev.Kind {
	case "archive":
		a.Archived = true
	case "fund": // 加减资金（D103）：本金、钱包一起变，划转净额也记上（不算盈亏）
		a.Wallet = a.Wallet.Add(*ev.Initial)
		a.Initial = a.Initial.Add(*ev.Initial)
		a.Transfers = a.Transfers.Add(*ev.Initial)
		return e.post(ev, "加减资金", ledger.Line{Account: acCash, Amount: *ev.Initial}, ledger.Line{Account: acCapital, Amount: ev.Initial.Neg()})
	case "fees":
		if ev.Fees.Default {
			a.Taker, a.Maker = nil, nil
		} else {
			t, m := ev.Fees.Taker, ev.Fees.Maker
			a.Taker, a.Maker = &t, &m
		}
	case "transfer":
		a.Wallet = a.Wallet.Add(*ev.Initial)
		a.Transfers = a.Transfers.Add(*ev.Initial)
		return e.post(ev, "划转", ledger.Line{Account: acCash, Amount: *ev.Initial}, ledger.Line{Account: acCapital, Amount: ev.Initial.Neg()})
	case "notice":
		n := *ev.Note
		n.Seq, n.Time = ev.Seq, ev.Time
		a.notice(n)
	case "setting":
		s := a.setting(ev.Setting.Instrument)
		s.Leverage, s.Mode = ev.Setting.Leverage, ev.Setting.Mode
	case "order":
		o := *ev.Order
		a.Orders[o.ID] = &o
		if o.isStop() {
			a.Stops = keepFor(a, append(a.Stops, StopRec{Time: ev.Time, Instrument: o.Instrument, Px: o.TriggerPx, Kind: "set"}))
		}
	case "amend":
		if o, ok := a.Orders[ev.Amend.ID]; ok {
			o.Price, o.Qty, o.Updated = ev.Amend.Price, ev.Amend.Qty, ev.Time
			if !ev.Amend.TriggerPx.IsZero() {
				if o.isStop() && !o.TriggerPx.Equal(ev.Amend.TriggerPx) {
					a.Stops = keepFor(a, append(a.Stops, StopRec{Time: ev.Time, Instrument: o.Instrument, Px: ev.Amend.TriggerPx, Kind: "move"}))
				}
				o.TriggerPx = ev.Amend.TriggerPx
			}
			if ev.Amend.TriggerDir != "" {
				o.TriggerDir = ev.Amend.TriggerDir
			}
		}
	case "trigger":
		if o, ok := a.Orders[ev.Amend.ID]; ok {
			o.Status, o.Qty, o.Updated = StTriggered, ev.Amend.Qty, ev.Time
			kind := "sl"
			if o.Type == TakeProfit {
				kind = "tp"
			}
			a.notice(Notice{Seq: ev.Seq, Time: ev.Time, Kind: kind, Instrument: o.Instrument, Qty: ev.Amend.Qty, Px: o.TriggerPx})
		}
	case "close":
		if o, ok := a.Orders[ev.Close.ID]; ok {
			o.Status, o.Reason, o.Updated = ev.Close.Status, ev.Close.Reason, ev.Time
			// 改价时撤旧设新不算撤掉；平仓后系统撤的在平仓时刻之后，复盘时不算在这笔里
			if o.isStop() && ev.Close.Status == StCanceled && ev.Close.Reason != "REPLACED" {
				a.Stops = keepFor(a, append(a.Stops, StopRec{Time: ev.Time, Instrument: o.Instrument, Px: o.TriggerPx, Kind: "cancel"}))
			}
			if o.entryCond() && ev.Close.Status == StTriggered {
				a.notice(Notice{Seq: ev.Seq, Time: ev.Time, Kind: "cond", Instrument: o.Instrument, Qty: o.Qty, Px: o.TriggerPx})
			} else if o.entryCond() && ev.Close.Status == StExpired {
				a.notice(Notice{Seq: ev.Seq, Time: ev.Time, Kind: "cond_failed", Instrument: o.Instrument, Qty: o.Qty, Px: o.TriggerPx, Reason: o.Reason})
			}
			a.finish(o)
		}
	case "fill":
		return e.applyFill(a, ev)
	case "funding":
		f := ev.Funding
		p := a.Pos[f.Instrument]
		q := decimal.Zero
		if f.Qty != nil {
			q = *f.Qty
		} else if p != nil {
			q = p.Qty
		}
		if q.IsZero() {
			return nil
		}
		amt := q.Mul(f.Mark).Mul(f.Rate).Neg() // 费率为正时多头付给空头
		a.Wallet = a.Wallet.Add(amt)
		a.Funding = a.Funding.Add(amt)
		if p != nil && !p.Qty.IsZero() && a.setting(f.Instrument).Mode == "isolated" {
			p.IsoMargin = p.IsoMargin.Add(amt)
		}
		a.Fundings = keepFor(a, append(a.Fundings, FundingRec{Seq: ev.Seq, Time: f.SettleTime, Instrument: f.Instrument, Rate: f.Rate, Mark: f.Mark, Qty: q, Amount: amt}))
		return e.post(ev, "资金费 "+f.Instrument, ledger.Line{Account: acCash, Amount: amt}, ledger.Line{Account: acFunding, Amount: amt.Neg()})
	case "liq_fee":
		l := ev.LiqFee
		a.Wallet = a.Wallet.Sub(l.Amount)
		a.LiqFees = a.LiqFees.Add(l.Amount)
		if l.Amount.IsNegative() { // 穿仓：亏过了保证金，超出的部分系统补（审查第 2 条：记下来，成绩里扣回去）
			a.Covered = a.Covered.Sub(l.Amount)
		}
		n := Notice{Seq: ev.Seq, Time: ev.Time, Kind: "liquidation", Instrument: l.Instrument, Px: l.Mark, Amount: l.Amount, Loss: l.Loss, Qty: l.Qty}
		if l.Partial {
			n.Reason = "partial" // 部分强平（D103）：剩下的仓位还在
		}
		a.notice(n)
		return e.post(ev, "强平费", ledger.Line{Account: acCash, Amount: l.Amount.Neg()}, ledger.Line{Account: acLiq, Amount: l.Amount})
	case "iso_xfer":
		p := a.pos(ev.IsoXfer.Instrument)
		p.IsoMargin = p.IsoMargin.Add(ev.IsoXfer.Amount)
	default:
		return fmt.Errorf("未知事件类型 %q", ev.Kind)
	}
	return nil
}

func (e *Engine) applyFill(a *Account, ev Event) error {
	f := ev.Fill
	p := a.pos(f.Instrument)
	wasFlat := p.Qty.IsZero()
	realized, opened := fillPos(a.setting(f.Instrument), p, f.Buy, f.Qty, f.Px)
	if wasFlat || opened {
		p.Opened = ev.Time
		p.Realized = zero // 新仓位（含反手后的新仓）从 0 开始累计
	} else {
		p.Realized = p.Realized.Add(realized)
	}
	a.Wallet = a.Wallet.Add(realized).Sub(f.Fee)
	a.Realized = a.Realized.Add(realized)
	a.Fees = a.Fees.Add(f.Fee)
	if p.Qty.IsZero() {
		p.Realized = zero
	}
	var source, tag string
	ref := zero
	if o, ok := a.Orders[f.OrderID]; ok {
		source, tag, ref = o.Source, o.Tag, o.RefPx
		filled := o.Filled.Add(f.Qty)
		o.AvgPx = o.AvgPx.Mul(o.Filled).Add(f.Px.Mul(f.Qty)).Div(filled)
		o.Filled = filled
		o.Fee = o.Fee.Add(f.Fee)
		o.Updated = ev.Time
		if o.remaining().Sign() <= 0 {
			o.Status = StFilled
			a.finish(o)
		} else if o.Status != StTriggered {
			o.Status = StPartial
		}
	}
	side := "sell"
	if f.Buy {
		side = "buy"
	}
	a.Fills = keepFor(a, append(a.Fills, Fill{Source: source, Tag: tag, Ref: ref, Seq: ev.Seq, Time: ev.Time, OrderID: f.OrderID, Instrument: f.Instrument, Side: side, Qty: f.Qty, Px: f.Px, Fee: f.Fee, Maker: f.Maker, Realized: realized, Liq: f.Liq}))
	return e.post(ev, "成交 "+f.Instrument,
		ledger.Line{Account: acCash, Amount: realized.Sub(f.Fee)},
		ledger.Line{Account: acPnL, Amount: realized.Neg()},
		ledger.Line{Account: acFee, Amount: f.Fee})
}

func (a *Account) finish(o *Order) {
	delete(a.Orders, o.ID)
	a.History = keepFor(a, append(a.History, o))
}

func (a *Account) notice(n Notice) { a.Notices = keep(append(a.Notices, n)) }

// 系统账户（负数 id）全留：战绩要从第一笔算起
func keepFor[T any](a *Account, s []T) []T {
	if a.ID < 0 {
		return s
	}
	return keep(s)
}

func keep[T any](s []T) []T {
	if len(s) > maxKeep {
		return append(s[:0:0], s[len(s)-maxKeep:]...)
	}
	return s
}

func (e *Engine) post(ev Event, memo string, lines ...ledger.Line) error {
	_, err := e.ledger.Post(ledger.Entry{Time: ev.Time, Portfolio: bookName(ev.Account), Ref: strconv.FormatInt(ev.Seq, 10), Memo: memo, Lines: lines})
	return err
}
