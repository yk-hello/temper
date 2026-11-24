package trading

import "github.com/shopspring/decimal"

// Desk 交易台：交易页、跟随、机器人下单时用的那一组操作。模拟盘是 *Engine；实盘是 live.Desk（D73：同一个界面，账户分模拟和实盘）。
// 方法和参数和 Engine 一样，返回的结构也一样，界面不用分两套。
type Desk interface {
	View(acct int64) (*AccountView, error)
	Preview(acct int64, r OrderReq) (*Preview, error)
	Place(acct int64, r OrderReq) (*Order, error)
	Amend(acct int64, id string, price, qty, trigger decimal.Decimal) (*Order, error)
	Cancel(acct int64, id string) (*Order, error)
	UndoCancel(acct int64, id string) (*Order, error)
	CancelAll(acct int64, inst string) (int, error)
	ClosePosition(acct int64, inst string, fraction decimal.Decimal, typ string, price decimal.Decimal) (*Order, error)
	CloseAll(acct int64) ([]*Order, error)
	SetLeverage(acct int64, inst string, l int) error
	SetMode(acct int64, inst, mode string) error
	SetTPSL(acct int64, inst string, tp, sl *decimal.Decimal, by string) error
	AdjustMargin(acct int64, inst string, amount decimal.Decimal, dry bool) (*MarginAdjust, error)
	History(acct int64, kind string, page, size int) (*Page, error)
	OrderFills(acct int64, id string) ([]Fill, error)
	Records(acct int64) (fills []Fill, fundings []FundingRec, liqFees decimal.Decimal, err error)
}

var _ Desk = (*Engine)(nil)

// SourceTag 这张单从哪来（manual、close、ai、bot、signal…）。没标的算手动。
func (r OrderReq) SourceTag() string {
	if r.source == "" {
		return "manual"
	}
	return r.source
}

// SetSource 标来源（别的包下单时用，比如实盘交易台平仓时标 close）。
func (r *OrderReq) SetSource(s string) { r.source = s }

// LiqEstimate 按币安公布的公式估一个仓位的强平价（和模拟盘同一个公式；实盘下单预估用）。
// qty 带符号；wb 全仓是全仓钱包余额、逐仓是这个仓位的保证金；tmm1、upnl1 是其他全仓仓位的维持保证金和浮动盈亏。
func LiqEstimate(spec *Spec, qty, entry, wb, tmm1, upnl1, mark decimal.Decimal) decimal.Decimal {
	return liqPrice(spec, &Position{Qty: qty, Entry: entry}, wb, tmm1, upnl1, mark)
}

// AfterFill 一笔成交之后的仓位（带符号数量、开仓均价），和模拟盘成交用同一段代码。
func AfterFill(qty, entry decimal.Decimal, buy bool, q, px decimal.Decimal) (decimal.Decimal, decimal.Decimal) {
	p := &Position{Qty: qty, Entry: entry}
	fillPos(&Setting{Mode: "cross"}, p, buy, q, px)
	return p.Qty, p.Entry
}
