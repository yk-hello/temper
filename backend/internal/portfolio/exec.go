package portfolio

import (
	"errors"
	"time"

	"github.com/shopspring/decimal"

	"qs/internal/trading"
)

// 04 第 5 节的三种执行方式。模型组合和跟随用户共用这一段，订单都走 trading 引擎。
const (
	PassiveWindow = 60 * time.Second // passive：挂这么久没成交就放弃
	NormalMaker   = 10 * time.Second // normal：先挂这么久，剩余转吃单
)

// Work 是一笔正在执行的调仓（可能分几张委托）。
// 不自己加锁，由持有它的一方保证同一时间只有一处在调用。
type Work struct {
	Trader   trading.Executor // 模拟盘引擎或交易所账户（trading.Mux 按账户分）
	Acct     int64
	Inst     string
	Buy      bool
	Qty      decimal.Decimal // 这次要成交的总数量
	Limit    decimal.Decimal // 滑点上限对应的价
	Urgency  string          // passive | normal | immediate
	Reduce   bool            // 只减仓：不查保证金，也不受最小金额限制（减仓永远能做）
	Start    int64
	Deadline int64 // 信号的 valid_until
	Source   string
	Tag      string

	Filled decimal.Decimal
	Fee    decimal.Decimal
	Reject string // 被引擎拒绝时的代码
	Done   bool

	cost    decimal.Decimal
	orderID string
	retryAt int64 // 行情过期被拒：到这个时间再试（毫秒）；0 = 不用重试
}

// staleWait 行情过期（断线重连那几秒）时最多等多久。交易所账户不会有这个拒绝（交易所自己有行情），
// 模拟盘原来一被拒就放弃，和同一笔在交易所上的结果对不上（11-16 实测：一小时里断了十几次）
const staleWait = 60 * time.Second

// Begin 开始执行。immediate 当场结束；passive、normal 挂单后等 Step。
// 行情过期被拒时不结束：每秒再试，直到行情回来，或者过了信号的有效期 / 最多 60 秒。
func (w *Work) Begin() {
	w.begin()
	if w.Reject == "MARKET_STALE" && w.Filled.IsZero() && w.orderID == "" && w.Start > 0 {
		w.Done, w.retryAt = false, w.Start+1000
	}
}

func (w *Work) begin() {
	if w.Urgency == "immediate" {
		w.take()
		return
	}
	if !w.rest() {
		if w.Urgency == "normal" {
			w.take()
			return
		}
		w.Done = true
	}
}

// Step 每秒调用：看挂单成交了没有，时间到了就放弃或转吃单。
func (w *Work) Step(now int64) {
	if !w.Done && w.retryAt > 0 && w.orderID == "" {
		end := w.Start + staleWait.Milliseconds()
		if w.Deadline > 0 && w.Deadline < end {
			end = w.Deadline
		}
		if now < w.retryAt {
			return
		}
		if now >= end {
			w.Done, w.retryAt = true, 0 // 等不到了：按原来的拒绝结束
			return
		}
		w.Reject, w.Done = "", false
		w.begin()
		if w.Reject == "MARKET_STALE" && w.Filled.IsZero() && w.orderID == "" {
			w.Done, w.retryAt = false, now+1000
		} else {
			w.retryAt = 0
		}
		return
	}
	if w.Done || w.orderID == "" {
		return
	}
	o := w.Trader.OrderOf(w.Acct, w.orderID)
	if o == nil {
		w.Done = true
		return
	}
	if o.Open() {
		window := NormalMaker
		if w.Urgency == "passive" {
			window = PassiveWindow
		}
		expiry := w.Start + window.Milliseconds()
		if w.Deadline > 0 && w.Deadline < expiry {
			expiry = w.Deadline
		}
		if now < expiry {
			return
		}
		if c, err := w.Trader.CancelAs(w.Acct, w.orderID, "EXEC_WINDOW"); err == nil && c != nil {
			o = c
		} else if c := w.Trader.OrderOf(w.Acct, w.orderID); c != nil {
			o = c
		}
	}
	w.absorb(o)
	w.orderID = ""
	if w.Urgency == "normal" && w.remaining().Sign() > 0 {
		w.take()
		return
	}
	w.Done = true
}

// Stop 不做了（比如来了新目标）：撤掉还挂着的部分。已成交的算数。
func (w *Work) Stop() {
	if w.Done {
		return
	}
	if w.orderID != "" {
		o, err := w.Trader.CancelAs(w.Acct, w.orderID, "REPLACED_TARGET")
		if err != nil || o == nil {
			o = w.Trader.OrderOf(w.Acct, w.orderID)
		}
		if o != nil {
			w.absorb(o)
		}
		w.orderID = ""
	}
	w.Done = true
}

// State 按成交情况给出 04 里的执行状态。
func (w *Work) State() string {
	switch {
	case w.remaining().Sign() <= 0:
		return "filled"
	case w.Filled.Sign() > 0:
		return "partial"
	}
	return "expired"
}

// AvgPx 已成交部分的均价。
func (w *Work) AvgPx() decimal.Decimal {
	if w.Filled.Sign() <= 0 {
		return decimal.Zero
	}
	return w.cost.Div(w.Filled)
}

func (w *Work) remaining() decimal.Decimal { return w.Qty.Sub(w.Filled) }

func (w *Work) side() string {
	if w.Buy {
		return "buy"
	}
	return "sell"
}

// rest 在最优价挂只挂单。挂不上（价格刚好跑了，或者被拒）返回 false。
func (w *Work) rest() bool {
	px, ok := w.Trader.RestPrice(w.Inst, w.Buy, w.Limit)
	if !ok {
		w.Reject = "NO_LIQUIDITY"
		return false
	}
	o, err := w.Trader.PlaceAs(w.Acct, trading.OrderReq{Instrument: w.Inst, Side: w.side(), Type: trading.Limit, TIF: trading.GTX,
		Price: px, Qty: w.remaining(), ReduceOnly: w.Reduce}, w.Source, w.Tag)
	if err != nil {
		w.noteReject(err)
		return false
	}
	if !o.Open() {
		w.absorb(o)
		return false
	}
	w.orderID = o.ID
	return true
}

// take 在滑点上限内吃单，吃不完的撤掉。
func (w *Work) take() {
	w.Done = true
	if w.remaining().Sign() <= 0 {
		return
	}
	px, ok := w.Trader.RoundPrice(w.Inst, w.Buy, w.Limit)
	if !ok {
		w.Reject = "BAD_INSTRUMENT"
		return
	}
	o, err := w.Trader.PlaceAs(w.Acct, trading.OrderReq{Instrument: w.Inst, Side: w.side(), Type: trading.Limit, TIF: trading.IOC,
		Price: px, Qty: w.remaining(), ReduceOnly: w.Reduce}, w.Source, w.Tag)
	if err != nil {
		w.noteReject(err)
		return
	}
	w.absorb(o)
}

func (w *Work) absorb(o *trading.Order) {
	if o == nil || o.Filled.Sign() <= 0 {
		return
	}
	w.Filled = w.Filled.Add(o.Filled)
	w.cost = w.cost.Add(o.Filled.Mul(o.AvgPx))
	w.Fee = w.Fee.Add(o.Fee)
}

func (w *Work) noteReject(err error) {
	var r *trading.Reject
	if errors.As(err, &r) {
		w.Reject = r.Code
	} else {
		w.Reject = err.Error()
	}
}
