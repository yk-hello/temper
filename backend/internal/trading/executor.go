package trading

import "github.com/shopspring/decimal"

// Executor 系统下单（跟随、执行算法）要用的一套方法。模拟盘引擎（*Engine）和交易所账户（live.Desks）都实现它，
// Mux 按账户编号分过去：这样同一个策略既能驱动模拟账户，也能驱动交易所账户（18 第 5.2 条）。
type Executor interface {
	Brief(acct int64) (*Brief, error)
	PlaceAs(acct int64, r OrderReq, source, tag string) (*Order, error)
	CloseAs(acct int64, inst, source, tag string) (*Order, error)
	CancelAs(acct int64, id, reason string) (*Order, error)
	CancelSource(acct int64, source, reason string) (int, error)
	OrderOf(acct int64, id string) *Order
	SetLeverage(acct int64, inst string, l int) error
	SetMode(acct int64, inst, mode string) error
	SetTPSL(acct int64, inst string, tp, sl *decimal.Decimal, by string) error
	Note(acct int64, kind, inst, reason string) error
	FillSeq(acct int64) int64
	ManualFills(acct, after int64) (map[string]bool, int64)
	// 下面三个只看品种：模拟盘和交易所用同一套品种规则（D92）、同一份币安盘口
	RoundPrice(inst string, buy bool, px decimal.Decimal) (decimal.Decimal, bool)
	RoundQty(inst string, q decimal.Decimal) (decimal.Decimal, bool)
	RestPrice(inst string, buy bool, limit decimal.Decimal) (decimal.Decimal, bool)
}

var _ Executor = (*Engine)(nil)

// Mux 编号 ≥ LiveBase 的是交易所账户，交给 Live；其余是模拟账户。Live 为空时只有模拟盘。
type Mux struct {
	Paper *Engine
	Live  Executor
}

func (m Mux) pick(acct int64) Executor {
	if acct >= LiveBase && m.Live != nil {
		return m.Live
	}
	return m.Paper
}

func (m Mux) Brief(acct int64) (*Brief, error) { return m.pick(acct).Brief(acct) }
func (m Mux) PlaceAs(acct int64, r OrderReq, source, tag string) (*Order, error) {
	return m.pick(acct).PlaceAs(acct, r, source, tag)
}
func (m Mux) CloseAs(acct int64, inst, source, tag string) (*Order, error) {
	return m.pick(acct).CloseAs(acct, inst, source, tag)
}
func (m Mux) CancelAs(acct int64, id, reason string) (*Order, error) {
	return m.pick(acct).CancelAs(acct, id, reason)
}
func (m Mux) CancelSource(acct int64, source, reason string) (int, error) {
	return m.pick(acct).CancelSource(acct, source, reason)
}
func (m Mux) OrderOf(acct int64, id string) *Order { return m.pick(acct).OrderOf(acct, id) }
func (m Mux) SetLeverage(acct int64, inst string, l int) error {
	return m.pick(acct).SetLeverage(acct, inst, l)
}
func (m Mux) SetMode(acct int64, inst, mode string) error {
	return m.pick(acct).SetMode(acct, inst, mode)
}
func (m Mux) SetTPSL(acct int64, inst string, tp, sl *decimal.Decimal, by string) error {
	return m.pick(acct).SetTPSL(acct, inst, tp, sl, by)
}
func (m Mux) Note(acct int64, kind, inst, reason string) error {
	return m.pick(acct).Note(acct, kind, inst, reason)
}
func (m Mux) FillSeq(acct int64) int64 { return m.pick(acct).FillSeq(acct) }
func (m Mux) ManualFills(acct, after int64) (map[string]bool, int64) {
	return m.pick(acct).ManualFills(acct, after)
}
func (m Mux) RoundPrice(inst string, buy bool, px decimal.Decimal) (decimal.Decimal, bool) {
	return m.Paper.RoundPrice(inst, buy, px)
}
func (m Mux) RoundQty(inst string, q decimal.Decimal) (decimal.Decimal, bool) {
	return m.Paper.RoundQty(inst, q)
}
func (m Mux) RestPrice(inst string, buy bool, limit decimal.Decimal) (decimal.Decimal, bool) {
	return m.Paper.RestPrice(inst, buy, limit)
}
