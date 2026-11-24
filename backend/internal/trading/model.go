package trading

import (
	"strconv"

	"github.com/shopspring/decimal"
)

// ---------- 订单 ----------

const (
	Market     = "market"
	Limit      = "limit"
	StopMarket = "stop_market"        // 止损：价格触发后市价平仓（挂在持仓上）
	TakeProfit = "take_profit_market" // 止盈：价格触发后市价平仓（挂在持仓上）
	CondMarket = "cond_market"        // 条件单：价格到了下市价单（可以开仓，突破追单用）
	CondLimit  = "cond_limit"         // 条件单：价格到了挂限价单

	ByMark  = "mark"  // 触发价按标记价（默认，不容易被插针打掉）
	ByLast  = "last"  // 触发价按最新成交价
	ByIndex = "index" // 触发价按指数价（多家现货的加权，最不容易被单一交易所的波动触发）

	GTC = "GTC" // 一直有效
	IOC = "IOC" // 立即成交，剩余撤销
	FOK = "FOK" // 全部成交，否则全撤
	GTX = "GTX" // 只挂单（Post-only）

	StNew       = "new"
	StPartial   = "partially_filled"
	StFilled    = "filled"
	StCanceled  = "canceled"
	StExpired   = "expired"
	StTriggered = "triggered"
)

type Order struct {
	ID         string          `json:"id"`
	Account    int64           `json:"account"`
	Instrument string          `json:"instrument"`
	Side       string          `json:"side"` // buy | sell
	Type       string          `json:"type"`
	TIF        string          `json:"tif,omitempty"`
	Price      decimal.Decimal `json:"price"`                 // 限价；市价单为 0
	TriggerPx  decimal.Decimal `json:"trigger_px"`            // 止盈止损、条件单的触发价
	TriggerBy  string          `json:"trigger_by,omitempty"`  // mark（默认）| last
	TriggerDir string          `json:"trigger_dir,omitempty"` // 条件单：up = 涨到触发价时触发，down = 跌到时触发
	Qty        decimal.Decimal `json:"qty"`                   // 委托数量；平整个仓位的止盈止损为 0
	Filled     decimal.Decimal `json:"filled"`
	AvgPx      decimal.Decimal `json:"avg_px"`
	Fee        decimal.Decimal `json:"fee"`
	ReduceOnly bool            `json:"reduce_only"`
	ClosePos   bool            `json:"close_position"` // 触发时平掉整个仓位
	TP         decimal.Decimal `json:"tp"`             // 随单设置的止盈触发价（成交后挂到仓位上）
	SL         decimal.Decimal `json:"sl"`
	Status     string          `json:"status"`
	Reason     string          `json:"reason,omitempty"` // 撤销或过期的原因代码
	QueueAhead decimal.Decimal `json:"queue_ahead"`      // 只在查询结果里：限价挂单前面还排着多少（引擎里存在 e.queue，不进事件）
	Source     string          `json:"source"`           // manual | close | tp | sl | liquidation
	Parent     string          `json:"parent,omitempty"` // 随单止盈止损：来自哪张委托
	Tag        string          `json:"tag,omitempty"`    // 系统下的单：来自哪条信号（或哪种风控）
	Created    int64           `json:"created"`
	Updated    int64           `json:"updated"`
	RefPx      decimal.Decimal `json:"ref_px"` // 下单时的参考价：市价单是对手盘一档，止损是触发价。算滑点用（5.10）
	// 币安的另外几条规则（D103）
	GoodTill     int64  `json:"good_till,omitempty"`     // GTD：到这个时刻（毫秒）自动过期
	PriceProtect bool   `json:"price_protect,omitempty"` // 条件单价格保护：标记价和最新价差太多时不触发
	PriceMatch   string `json:"price_match,omitempty"`   // 按盘口定的价（OPPONENT、QUEUE_5……），价格已经填在 Price 里
}

func (o *Order) buy() bool                  { return o.Side == "buy" }
func (o *Order) remaining() decimal.Decimal { return o.Qty.Sub(o.Filled) }
func (o *Order) conditional() bool {
	return o.Type == StopMarket || o.Type == TakeProfit || o.Type == CondMarket || o.Type == CondLimit
}
func (o *Order) entryCond() bool { return o.Type == CondMarket || o.Type == CondLimit }
func (o *Order) open() bool      { return o.Status == StNew || o.Status == StPartial }
func sideSign(buy bool) decimal.Decimal {
	if buy {
		return one
	}
	return minusOne
}

var (
	zero     = decimal.Zero
	one      = decimal.NewFromInt(1)
	minusOne = decimal.NewFromInt(-1)
	two      = decimal.NewFromInt(2)
	half     = decimal.RequireFromString("0.5")
	hundred  = decimal.NewFromInt(100)
)

// ---------- 持仓与账户 ----------

type Setting struct {
	Leverage int    `json:"leverage"`
	Mode     string `json:"mode"` // cross | isolated
}

const DefaultLeverage = 10 // 默认 10 倍（币安默认 20 倍；我们偏保守，用户可改）

type Position struct {
	Instrument string          `json:"instrument"`
	Qty        decimal.Decimal `json:"qty"` // 带符号：正 = 多，负 = 空
	Entry      decimal.Decimal `json:"entry"`
	IsoMargin  decimal.Decimal `json:"iso_margin"` // 逐仓保证金（全仓为 0）
	Realized   decimal.Decimal `json:"realized"`   // 这个仓位累计已实现盈亏（不含手续费）
	Opened     int64           `json:"opened"`
}

type Fill struct {
	Seq        int64           `json:"seq"`
	Time       int64           `json:"time"`
	OrderID    string          `json:"order_id"`
	Instrument string          `json:"instrument"`
	Side       string          `json:"side"`
	Qty        decimal.Decimal `json:"qty"`
	Px         decimal.Decimal `json:"px"`
	Fee        decimal.Decimal `json:"fee"`
	Maker      bool            `json:"maker"`
	Realized   decimal.Decimal `json:"realized"`
	Liq        bool            `json:"liquidation,omitempty"`
	Ref        decimal.Decimal `json:"ref"`              // 这张委托的参考价，0 = 改版之前的成交
	Source     string          `json:"source,omitempty"` // 这张委托的来源：manual | close | ai | follow | risk ...
	Tag        string          `json:"tag,omitempty"`
}

type FundingRec struct {
	Seq        int64           `json:"seq"`
	Time       int64           `json:"time"`
	Instrument string          `json:"instrument"`
	Rate       decimal.Decimal `json:"rate"`
	Mark       decimal.Decimal `json:"mark"`
	Qty        decimal.Decimal `json:"qty"`
	Amount     decimal.Decimal `json:"amount"` // 正 = 收到，负 = 支付
}

// Notice 需要提醒用户的事：强平、止盈止损触发、资金不足撤单等。
type Notice struct {
	Seq        int64           `json:"seq"`
	Time       int64           `json:"time"`
	Kind       string          `json:"kind"` // liquidation | tp | sl | canceled
	Instrument string          `json:"instrument"`
	Qty        decimal.Decimal `json:"qty"`
	Px         decimal.Decimal `json:"px"`
	Amount     decimal.Decimal `json:"amount"`
	Reason     string          `json:"reason,omitempty"`
	Loss       decimal.Decimal `json:"loss"`         // 强平：这次一共亏了多少
	At         int64           `json:"at,omitempty"` // 断流补算（gap_replay）：价格到那里的那一分钟
}

type Account struct {
	ID        int64                `json:"id"`
	Initial   decimal.Decimal      `json:"initial"`
	Transfers decimal.Decimal      `json:"transfers"` // 累计划转进来的净额（转去现货是负的）
	Wallet    decimal.Decimal      `json:"wallet"`    // 钱包余额（含所有已实现盈亏、手续费、资金费）
	Realized  decimal.Decimal      `json:"realized"`
	Fees      decimal.Decimal      `json:"fees"`
	Funding   decimal.Decimal      `json:"funding"`
	LiqFees   decimal.Decimal      `json:"liq_fees"`
	Covered   decimal.Decimal      `json:"covered"`             // 穿仓时系统替这个账户补的钱（累计，正数）。LiqFees 里已经减掉了
	Taker     *decimal.Decimal     `json:"taker_fee,omitempty"` // 这个账户自己设的吃单费率；没设是 nil，用引擎默认
	Maker     *decimal.Decimal     `json:"maker_fee,omitempty"`
	Archived  bool                 `json:"archived"`
	Settings  map[string]*Setting  `json:"settings"`
	Pos       map[string]*Position `json:"positions"`
	Orders    map[string]*Order    `json:"orders"` // 未结束的委托（含未触发的止盈止损）

	LastSeq  int64        `json:"-"` // 这个账户最后一个事件的序号
	History  []*Order     `json:"-"` // 已结束的委托（按结束顺序）
	Fills    []Fill       `json:"-"`
	Fundings []FundingRec `json:"-"`
	Notices  []Notice     `json:"-"`
	Stops    []StopRec    `json:"-"` // 止损的每一次变化（复盘用）
}

// StopRec 止损的一次变化：set = 新设（改价也是撤旧设新），move = 改触发价，cancel = 撤掉。
type StopRec struct {
	Time       int64           `json:"time"`
	Instrument string          `json:"instrument"`
	Px         decimal.Decimal `json:"px"`
	Kind       string          `json:"kind"`
}

func newAccount(id int64) *Account {
	return &Account{ID: id, Settings: map[string]*Setting{}, Pos: map[string]*Position{}, Orders: map[string]*Order{}}
}

func (a *Account) setting(inst string) *Setting {
	s, ok := a.Settings[inst]
	if !ok {
		s = &Setting{Leverage: DefaultLeverage, Mode: "cross"}
		a.Settings[inst] = s
	}
	return s
}

func (a *Account) pos(inst string) *Position {
	p, ok := a.Pos[inst]
	if !ok {
		p = &Position{Instrument: inst}
		a.Pos[inst] = p
	}
	return p
}

// ---------- 事件（所有状态变化都是事件；重启时按顺序重放即可完全恢复） ----------

type Event struct {
	Seq     int64  `json:"seq"`
	Time    int64  `json:"t"`
	Account int64  `json:"a"`
	Kind    string `json:"k"`

	Initial *decimal.Decimal `json:"initial,omitempty"`  // open；transfer、fund 也用它放金额
	Setting *SettingEv       `json:"setting,omitempty"`  // setting
	Order   *Order           `json:"order,omitempty"`    // order（新委托的完整快照）
	Amend   *AmendEv         `json:"amend,omitempty"`    // amend
	Close   *CloseEv         `json:"close,omitempty"`    // close（撤销、过期、触发）
	Fill    *FillEv          `json:"fill,omitempty"`     // fill
	Funding *FundingEv       `json:"funding,omitempty"`  // funding
	LiqFee  *LiqFeeEv        `json:"liq_fee,omitempty"`  // liq_fee
	Note    *Notice          `json:"note,omitempty"`     // notice（转手动管理、纪律锁这类提醒）
	IsoXfer *IsoXferEv       `json:"iso_xfer,omitempty"` // iso_xfer（逐仓降杠杆时从可用余额补保证金）
	Fees    *FeesEv          `json:"fees,omitempty"`     // fees（这个账户的手续费率改了）
}

// FeesEv 模拟账户自己的手续费率（D103）。Default = 回到引擎默认（币安普通用户）。
type FeesEv struct {
	Taker   decimal.Decimal `json:"taker"`
	Maker   decimal.Decimal `json:"maker"`
	Default bool            `json:"default,omitempty"`
}

type SettingEv struct {
	Instrument string `json:"instrument"`
	Leverage   int    `json:"leverage"`
	Mode       string `json:"mode"`
}

type AmendEv struct {
	ID         string          `json:"id"`
	Price      decimal.Decimal `json:"price"`
	Qty        decimal.Decimal `json:"qty"`
	TriggerPx  decimal.Decimal `json:"trigger_px"`            // 条件单改触发价（0 = 不改）
	TriggerDir string          `json:"trigger_dir,omitempty"` // 改触发价后重新判断的方向
}

type CloseEv struct {
	ID     string `json:"id"`
	Status string `json:"status"` // canceled | expired | triggered
	Reason string `json:"reason,omitempty"`
}

type FillEv struct {
	OrderID    string          `json:"order_id"`
	Instrument string          `json:"instrument"`
	Buy        bool            `json:"buy"`
	Qty        decimal.Decimal `json:"qty"`
	Px         decimal.Decimal `json:"px"`
	Fee        decimal.Decimal `json:"fee"`
	Maker      bool            `json:"maker"`
	Liq        bool            `json:"liq,omitempty"`
}

type FundingEv struct {
	Instrument string          `json:"instrument"`
	Rate       decimal.Decimal `json:"rate"`
	Mark       decimal.Decimal `json:"mark"`
	SettleTime int64           `json:"settle_time"`
	// Qty 按这个持仓算（带符号）；空 = 按入账时的持仓。开机补结失败、过一阵才补上时用开机那一刻的持仓（11-18）
	Qty *decimal.Decimal `json:"qty,omitempty"`
}

type LiqFeeEv struct {
	Instrument string          `json:"instrument"` // 全仓强平时为空
	Mark       decimal.Decimal `json:"mark"`
	Amount     decimal.Decimal `json:"amount"`            // 强平费（负数表示穿仓部分由保险基金承担）
	Loss       decimal.Decimal `json:"loss"`              // 这次强平一共亏了多少（平仓亏损 + 手续费 + 强平费）
	Qty        decimal.Decimal `json:"qty,omitempty"`     // 部分强平平掉的数量（D103）
	Partial    bool            `json:"partial,omitempty"` // 部分强平：剩下的仓位还在
}

type IsoXferEv struct {
	Instrument string          `json:"instrument"`
	Amount     decimal.Decimal `json:"amount"`
}

func orderID(seq int64) string { return "O" + strconv.FormatInt(seq, 10) }
