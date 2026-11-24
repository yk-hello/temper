package portfolio

import (
	"math"
	"sort"
	"time"

	"github.com/shopspring/decimal"

	"qs/internal/trading"
)

// Trade 一笔完整的交易：从空仓开仓，到这个方向的仓位平完（反手算一笔结束、一笔开始）。
// 净收益 = 平仓盈亏 − 手续费 + 资金费，所有交易的净收益加起来等于账户钱包的变化（强平费另算）。
type Trade struct {
	Instrument string          `json:"instrument"`
	Side       string          `json:"side"` // long | short
	OpenTime   int64           `json:"open_time"`
	CloseTime  int64           `json:"close_time,omitempty"` // 0 = 还没平完
	Qty        decimal.Decimal `json:"qty"`                  // 开过的总数量
	Entry      decimal.Decimal `json:"entry"`                // 开仓均价
	Exit       decimal.Decimal `json:"exit"`                 // 平仓均价
	Gross      decimal.Decimal `json:"gross"`                // 平仓盈亏（不含费用）
	Fees       decimal.Decimal `json:"fees"`
	Funding    decimal.Decimal `json:"funding"` // 正 = 收到
	Net        decimal.Decimal `json:"net"`
	ClosedBy   string          `json:"closed_by,omitempty"` // 平台风控平的：stop_loss | take_profit | halt ...
	Signals    []string        `json:"-"`                   // 参与这笔交易的信号 id（不公开，3.13）

	Legs        []trading.Fill       `json:"-"` // 这笔交易的每笔成交（反手的那笔按数量拆开）
	FundingRecs []trading.FundingRec `json:"-"`

	openCost  decimal.Decimal
	closeQty  decimal.Decimal
	closeCost decimal.Decimal
	held      decimal.Decimal
}

// BuildTrades 按顺序走一遍成交和资金费，拼出每笔交易。closed 按平仓时间排，open 是还没平完的。
func BuildTrades(fills []trading.Fill, fundings []trading.FundingRec) (closed, open []Trade) {
	cur := map[string]*Trade{}
	fi := 0
	applyFunding := func(before int64) {
		for fi < len(fundings) && fundings[fi].Seq < before {
			f := fundings[fi]
			fi++
			if t := cur[f.Instrument]; t != nil {
				t.Funding = t.Funding.Add(f.Amount)
				t.FundingRecs = append(t.FundingRecs, f)
			}
		}
	}
	finish := func(t *Trade, at int64) {
		t.CloseTime = at
		finishNumbers(t)
		closed = append(closed, *t)
	}
	for _, f := range fills {
		applyFunding(f.Seq)
		q := f.Qty
		dir := decimal.NewFromInt(1) // 这笔成交让仓位往哪边变
		if f.Side == "sell" {
			dir = dir.Neg()
		}
		t := cur[f.Instrument]
		remainFee := f.Fee
		if t != nil && t.held.Sign() != dir.Sign() {
			// 减仓或平仓
			c := decimal.Min(q, t.held.Abs())
			fee := f.Fee
			if !c.Equal(q) {
				fee = f.Fee.Mul(c).Div(q)
			}
			remainFee = f.Fee.Sub(fee)
			t.Fees = t.Fees.Add(fee)
			t.closeQty = t.closeQty.Add(c)
			t.closeCost = t.closeCost.Add(c.Mul(f.Px))
			t.Gross = t.Gross.Add(f.Realized)
			t.held = t.held.Add(dir.Mul(c))
			noteTag(t, f)
			leg := f
			leg.Qty, leg.Fee = c, fee
			t.Legs = append(t.Legs, leg)
			q = q.Sub(c)
			if t.held.IsZero() {
				finish(t, f.Time)
				delete(cur, f.Instrument)
				t = nil
			}
		}
		if q.Sign() > 0 {
			// 开仓或加仓（反手时是剩下的部分）
			if t == nil {
				side := "long"
				if dir.Sign() < 0 {
					side = "short"
				}
				t = &Trade{Instrument: f.Instrument, Side: side, OpenTime: f.Time}
				cur[f.Instrument] = t
			}
			t.Fees = t.Fees.Add(remainFee)
			t.Qty = t.Qty.Add(q)
			t.openCost = t.openCost.Add(q.Mul(f.Px))
			t.held = t.held.Add(dir.Mul(q))
			noteTag(t, f)
			leg := f
			leg.Qty, leg.Fee, leg.Realized = q, remainFee, decimal.Zero
			t.Legs = append(t.Legs, leg)
		}
	}
	applyFunding(math.MaxInt64)
	insts := make([]string, 0, len(cur))
	for inst := range cur {
		insts = append(insts, inst)
	}
	sort.Strings(insts)
	for _, inst := range insts {
		finishNumbers(cur[inst])
		open = append(open, *cur[inst])
	}
	return closed, open
}

func noteTag(t *Trade, f trading.Fill) {
	if f.Tag == "" {
		return
	}
	if f.Source == "risk" {
		t.ClosedBy = f.Tag
		return
	}
	for _, s := range t.Signals {
		if s == f.Tag {
			return
		}
	}
	t.Signals = append(t.Signals, f.Tag)
}

func finishNumbers(t *Trade) {
	if t.Qty.Sign() > 0 {
		t.Entry = t.openCost.Div(t.Qty)
	}
	if t.closeQty.Sign() > 0 {
		t.Exit = t.closeCost.Div(t.closeQty)
	}
	t.Net = t.Gross.Sub(t.Fees).Add(t.Funding)
}

// Stats 战绩的汇总数字。算法写在这里，手算样例在测试里。
type Stats struct {
	Return      float64  `json:"return"`       // 总收益率 = 净值 ÷ 初始 − 1
	MaxDrawdown float64  `json:"max_drawdown"` // 净值从高点回落的最大比例
	Sharpe      *float64 `json:"sharpe"`       // 夏普（日收益年化，按 365 天）；不到 2 个日收益时没有
	Trades      int      `json:"trades"`       // 已平仓的交易笔数
	Wins        int      `json:"wins"`
	WinRate     *float64 `json:"win_rate"` // 没有已平仓交易时没有
	Days        int      `json:"days"`     // 有净值记录的天数
}

// ComputeStats nav 是按时间排的净值点；closed 是已平仓的交易。
func ComputeStats(initial, nav float64, points []NAVPoint, closed []Trade) Stats {
	var s Stats
	if initial > 0 {
		s.Return = nav/initial - 1
	}
	peak := initial
	for _, p := range points {
		if p.NAV > peak {
			peak = p.NAV
		}
		if peak > 0 {
			s.MaxDrawdown = math.Max(s.MaxDrawdown, (peak-p.NAV)/peak)
		}
	}
	if peak > 0 && nav < peak {
		s.MaxDrawdown = math.Max(s.MaxDrawdown, (peak-nav)/peak)
	}
	// 日收益：每个 UTC 日最后一个净值，和前一天比（第一天和初始资金比）
	var closes []float64
	lastDay := ""
	for _, p := range points {
		day := time.UnixMilli(p.T).UTC().Format("2006-01-02")
		if day != lastDay {
			closes = append(closes, p.NAV)
			lastDay = day
		} else {
			closes[len(closes)-1] = p.NAV
		}
	}
	s.Days = len(closes)
	prev := initial
	var rets []float64
	for _, c := range closes {
		if prev > 0 {
			rets = append(rets, c/prev-1)
		}
		prev = c
	}
	if len(rets) >= 2 {
		mean := 0.0
		for _, r := range rets {
			mean += r
		}
		mean /= float64(len(rets))
		v := 0.0
		for _, r := range rets {
			v += (r - mean) * (r - mean)
		}
		std := math.Sqrt(v / float64(len(rets)-1))
		if std > 0 {
			x := mean / std * math.Sqrt(365)
			s.Sharpe = &x
		}
	}
	s.Trades = len(closed)
	for _, t := range closed {
		if t.Net.Sign() > 0 {
			s.Wins++
		}
	}
	if s.Trades > 0 {
		x := float64(s.Wins) / float64(s.Trades)
		s.WinRate = &x
	}
	return s
}

// Performance 公开的战绩（GET /api/strategies/{id}/performance）。
// 没开启服务的人也能看：净值、汇总、已平仓的每笔交易，不含理由、不含还没平的仓位（D75 第 17 条）。
type Performance struct {
	StrategyID string          `json:"strategy_id"`
	Since      int64           `json:"since"`
	Initial    decimal.Decimal `json:"initial"`
	NAV        decimal.Decimal `json:"nav"`
	Fees       decimal.Decimal `json:"fees"`
	Funding    decimal.Decimal `json:"funding"`
	LiqFees    decimal.Decimal `json:"liq_fees"`
	Stats
	Followers     int             `json:"followers"`      // 正在跟随的人数
	FollowCapital decimal.Decimal `json:"follow_capital"` // 跟随资金合计
	Curve         []NAVPoint      `json:"curve"`          // 净值曲线（点多时抽稀，最多 maxCurve 个）
	Closed        []Trade         `json:"closed"`         // 已平仓的每笔交易，最新的在前
}

const maxCurve = 720

// Performance 按引擎的成交和资金费算战绩，数字和账本同一个来源。
func (p *Portfolio) Performance() Performance {
	b := p.brief()
	fills, fundings, liq, _ := p.Trader.Records(p.Acct)
	closed, _ := BuildTrades(fills, fundings)
	pts := p.History()
	initial, _ := b.Initial.Float64()
	nav, _ := b.Equity.Float64()
	out := Performance{StrategyID: p.ID, Initial: b.Initial, NAV: b.Equity.Round(4), LiqFees: liq, Stats: ComputeStats(initial, nav, pts, closed), Curve: thin(pts, maxCurve), Closed: []Trade{}}
	if len(pts) > 0 {
		out.Since = pts[0].T
	}
	for _, f := range fills {
		out.Fees = out.Fees.Add(f.Fee)
	}
	for _, f := range fundings {
		out.Funding = out.Funding.Add(f.Amount)
	}
	for i := len(closed) - 1; i >= 0; i-- {
		out.Closed = append(out.Closed, closed[i])
	}
	return out
}

// AllTrades 全部交易：已平仓的和还没平完的。含理由的部分只给开启服务的人，由调用方判断。
func (p *Portfolio) AllTrades() (closed, open []Trade) {
	fills, fundings, _, err := p.Trader.Records(p.Acct)
	if err != nil {
		return nil, nil
	}
	return BuildTrades(fills, fundings)
}

// Since 战绩从什么时候开始算（第一个净值点）。
func (p *Portfolio) Since() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.navHist) > 0 {
		return p.navHist[0].T
	}
	return 0
}

// thin 抽稀：保留第一个和最后一个点，中间等间隔取。
func thin(pts []NAVPoint, n int) []NAVPoint {
	if len(pts) <= n {
		return append([]NAVPoint{}, pts...)
	}
	out := make([]NAVPoint, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, pts[i*(len(pts)-1)/(n-1)])
	}
	return out
}
