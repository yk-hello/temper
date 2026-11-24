// Package highlight 高光时刻（D23、D91）。
//
// 每笔平仓，按保证金的回报够门槛（默认 20%，用户自己可以改）就记进生涯档案：
// 记下那一刻的快照（品种、方向、杠杆、开平仓、回报、最深浮亏、最高浮盈），以后账户怎么变，这条都不改。
// 手动、AI、策略信号、机器人的交易都算（用户："赚了就是赚了"）；亏的不算。
// 没有验真、邀请、排名（D90、D91）。
package highlight

import (
	"strings"

	"qs/internal/journal"
)

// DefaultMin 默认门槛：按保证金的回报 ≥ 20%。
const DefaultMin = 0.20

// 门槛能调的范围：太低了每笔都是高光，太高了永远不出。
const (
	MinFloor = 0.01
	MinCeil  = 50.0
)

// Snap 高光那一刻的快照。金额（Net、Qty）只给本人看，卡片和视频上只写比例。
type Snap struct {
	TradeID    string  `json:"trade_id"`
	Instrument string  `json:"instrument"`
	Side       string  `json:"side"`
	Origin     string  `json:"origin"`
	Mode       string  `json:"mode"` // sim | live
	Leverage   int     `json:"leverage"`
	OpenTime   int64   `json:"open_time"`
	CloseTime  int64   `json:"close_time"`
	Entry      float64 `json:"entry"`
	Exit       float64 `json:"exit"`
	ROE        float64 `json:"roe"`      // 净盈亏 ÷ 初始保证金（开仓均价 × 开过的总数量 ÷ 杠杆）
	AcctRet    float64 `json:"acct_ret"` // 净盈亏 ÷ 开仓时的权益
	MAE        float64 `json:"mae"`      // 持仓中最深浮亏，按保证金（≤ 0）；没有价格路径时是 0
	MFE        float64 `json:"mfe"`      // 持仓中最高浮盈，按保证金（≥ 0）
	PathKnown  bool    `json:"path_known"`
	Net        float64 `json:"net"`
	Qty        float64 `json:"qty"`
}

// IsSpot 现货没有杠杆，按本金算。
func IsSpot(inst string) bool { return strings.HasSuffix(inst, "-SPOT") }

// ROE 按保证金算的回报：净盈亏 ÷（开仓均价 × 开过的总数量 ÷ 杠杆）。杠杆小于 1 按 1 倍。
func ROE(t journal.Trade, lev int) float64 {
	if lev < 1 {
		lev = 1
	}
	m := t.Entry * t.Qty / float64(lev)
	if m <= 0 {
		return 0
	}
	return t.Net / m
}

// Qualifies 这一笔够不够高光：已平仓、赚钱、按保证金的回报够门槛。
func Qualifies(t journal.Trade, lev int, need float64) bool {
	return t.CloseTime > 0 && t.Net > 0 && ROE(t, lev) >= ClampMin(need)
}

// ClampMin 门槛限制在能调的范围里；没设过（0）用默认值。
func ClampMin(v float64) float64 {
	if v <= 0 {
		return DefaultMin
	}
	return max(MinFloor, min(MinCeil, v))
}

// AcctRet 占开仓时权益的比例：开仓时的权益 = 初始资金 + 在它开仓前平掉的交易的净盈亏。
func AcctRet(t journal.Trade, all []journal.Trade, initial float64) float64 {
	eq := initial
	for _, c := range all {
		if c.CloseTime > 0 && c.CloseTime <= t.OpenTime {
			eq += c.Net
		}
	}
	if eq <= 0 {
		return 0
	}
	return t.Net / eq
}

// Excursion 把价格距离（复盘模块算的 MAE、MFE）换成按保证金的比例。
func Excursion(t journal.Trade, lev int, mae, mfe float64) (float64, float64) {
	if lev < 1 {
		lev = 1
	}
	if t.Entry <= 0 {
		return 0, 0
	}
	k := float64(lev) / t.Entry
	return -mae * k, mfe * k
}
