// Package follow 是跟随层（模块 3）：把模型组合的目标权重，按用户自己设的金额和风控，
// 换算成他账户里的订单。订单走 trading 引擎，来源标 follow，手续费和资金费和手动单一样（D74）。
//
// 用户自己开启、随时能停（D75 第 23 条）。所有状态变化都是只追加的事件，重启后重放（3.10）。
package follow

import (
	"github.com/shopspring/decimal"
)

const (
	StActive  = "active"
	StPaused  = "paused"
	StClosing = "closing" // 正在平仓，平完变 stopped
	StStopped = "stopped"

	// 停止的原因
	ReasonUser     = "user"     // 用户停止并平仓
	ReasonKeep     = "keep"     // 用户停止，仓位留给自己
	ReasonMaxLoss  = "max_loss" // 亏到最大亏损，自动平仓并停止（D75 第 15 条）
	ReasonArchived = "archived" // 账户被重置
)

// 默认值（D75 第 14 条）
var (
	DefaultMaxLoss     = decimal.RequireFromString("0.2")
	DefaultMaxLeverage = 3
	DefaultPerInst     = decimal.NewFromInt(1)
	MinRatio           = decimal.RequireFromString("0.1") // 跟随比例（18 第 6.6 条）：策略仓位的多少倍
	MaxRatio           = decimal.NewFromInt(3)
	MinAlloc           = decimal.NewFromInt(100) // 再小，BTC 的最小下单金额（100 USDT）都够不上
	MaxLeverageLimit   = 20
	PaperInitialName   = map[string]string{"zh": "策略跟随", "en": "Strategy follow"}
	// 18 第 5.8 条以前自动建的账户叫这个名字，还认
	oldInitialNames = []string{"AI 跟随", "AI Follow"}
)

// Params 用户设的跟随参数。
type Params struct {
	Alloc       decimal.Decimal `json:"alloc"`        // 分配金额（USDT）
	MaxLoss     decimal.Decimal `json:"max_loss"`     // 最大亏损，占分配金额的比例
	MaxLeverage int             `json:"max_leverage"` // 杠杆上限：总仓位不超过分配金额的这么多倍，也是下单用的杠杆
	PerInst     decimal.Decimal `json:"per_inst"`     // 单品种上限，占分配金额的比例
	Blacklist   []string        `json:"blacklist"`    // 不跟的品种
	// Ratio 跟随比例（18 第 6.6 条）：策略的目标仓位（占资金的比例）乘这个数。1 = 策略用 40% 你也用分配金额的 40%；
	// 0.5 = 20%。杠杆上限、单品种上限照样管着。以前的记录里没有这一项，按 1 算。
	Ratio decimal.Decimal `json:"ratio"`
}

func (p Params) ratio() decimal.Decimal {
	if p.Ratio.Sign() <= 0 {
		return decimal.NewFromInt(1)
	}
	return p.Ratio
}

func (p Params) blacklisted(inst string) bool {
	for _, x := range p.Blacklist {
		if x == inst {
			return true
		}
	}
	return false
}

// Follow 一次跟随。
type Follow struct {
	ID          int64            `json:"id"`
	User        int64            `json:"-"`
	Account     int64            `json:"account"`
	Strategy    string           `json:"strategy"`
	Params      Params           `json:"params"`
	Status      string           `json:"status"`
	Reason      string           `json:"reason,omitempty"`
	StartEquity decimal.Decimal  `json:"-"` // 开启时账户的权益：跟随盈亏 = 现在的权益 − 这个数
	Started     int64            `json:"started"`
	Stopped     int64            `json:"stopped,omitempty"`
	Insts       map[string]bool  `json:"-"`      // 跟随下过单的品种
	Manual      map[string]int64 `json:"manual"` // 手动管理的品种 → 什么时候转的（D80）
}

func (f *Follow) running() bool { return f.Status == StActive || f.Status == StPaused }

// Exec 一条目标在这个用户身上的执行记录。
type Exec struct {
	ID         int64           `json:"id"`
	Follow     int64           `json:"-"`
	Msg        string          `json:"msg,omitempty"` // 来自哪条信号；恢复时补齐的为空
	Instrument string          `json:"instrument"`
	Time       int64           `json:"time"`
	End        int64           `json:"end,omitempty"`
	Weight     decimal.Decimal `json:"weight"`         // AI 的目标权重
	Applied    decimal.Decimal `json:"applied"`        // 按用户上限缩过之后的权重
	Note       string          `json:"note,omitempty"` // scaled_instrument | scaled_leverage | blacklist | catch_up | 风控原因
	State      string          `json:"state"`          // pending | filled | partial | expired | interrupted | skipped_*
	Reject     string          `json:"reject,omitempty"`
	Side       string          `json:"side,omitempty"`
	Qty        decimal.Decimal `json:"qty"`
	Filled     decimal.Decimal `json:"filled"`
	AvgPx      decimal.Decimal `json:"avg_px"`
	Fee        decimal.Decimal `json:"fee"`
}

// Event 只追加的事件。
type Event struct {
	Seq    int64  `json:"seq"`
	Time   int64  `json:"t"`
	Follow int64  `json:"f"`
	Kind   string `json:"k"` // start | params | pause | resume | stop | closed | exec | manual | manual_end
	Inst   string `json:"inst,omitempty"`

	Start  *StartEv `json:"start,omitempty"`
	Params *Params  `json:"params,omitempty"`
	Reason string   `json:"reason,omitempty"`
	Close  bool     `json:"close,omitempty"` // stop：要先把仓位平掉
	Exec   *Exec    `json:"exec,omitempty"`
}

type StartEv struct {
	User        int64           `json:"user"`
	Account     int64           `json:"account"`
	Strategy    string          `json:"strategy"`
	Params      Params          `json:"params"`
	StartEquity decimal.Decimal `json:"start_equity"`
}

// Err 拒绝操作。Code 由前端按语言翻译。
type Err struct {
	HTTP   int               `json:"-"`
	Code   string            `json:"code"`
	Params map[string]string `json:"params,omitempty"`
}

func (e *Err) Error() string { return e.Code }

func rej(http int, code string, kv ...string) *Err {
	e := &Err{HTTP: http, Code: code}
	if len(kv) > 1 {
		e.Params = map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			e.Params[kv[i]] = kv[i+1]
		}
	}
	return e
}
