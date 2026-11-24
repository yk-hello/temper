// Package journal 交易日志和复盘（模块 5，12 的 5.1–5.12）。
//
// 每笔交易从账本里的成交算出来（portfolio.BuildTrades），这里加上 R 倍数、违规、分组统计、
// MAE / MFE 和结论。标签、笔记、截图、预案存在自己的表里。这些数字只用于展示，不进账本。
package journal

import (
	"fmt"
	"github.com/shopspring/decimal"
	"math"
	"sort"
	"strings"
	"time"

	"qs/internal/portfolio"
	"qs/internal/trading"
)

// Leg 这笔交易的一次成交。
type Leg struct {
	T       int64   `json:"t"`
	Side    string  `json:"side"`
	Qty     float64 `json:"qty"`
	Px      float64 `json:"px"`
	Fee     float64 `json:"fee"`
	Ref     float64 `json:"ref,omitempty"` // 下单时的参考价
	Source  string  `json:"source,omitempty"`
	OrderID string  `json:"order_id"`
}

// StopPoint 止损的一次变化。
type StopPoint struct {
	T    int64   `json:"t"`
	Px   float64 `json:"px"`
	Kind string  `json:"kind"` // set | move | cancel
}

// Trade 日志里的一笔。
type Trade struct {
	ID         string      `json:"id"`
	Instrument string      `json:"instrument"`
	Side       string      `json:"side"`
	OpenTime   int64       `json:"open_time"`
	CloseTime  int64       `json:"close_time"` // 0 = 还没平完
	HoldMs     int64       `json:"hold_ms"`
	Qty        float64     `json:"qty"`
	Entry      float64     `json:"entry"`
	Exit       float64     `json:"exit"`
	Gross      float64     `json:"gross"`
	Fees       float64     `json:"fees"`
	Funding    float64     `json:"funding"`
	Net        float64     `json:"net"`
	Slip       float64     `json:"slip"`       // 滑点成本：正 = 比下单时的参考价差；已经算在 Gross 里
	SlipKnown  bool        `json:"slip_known"` // 每次成交都有参考价
	Origin     string      `json:"origin"`     // manual | ai | mixed
	ClosedBy   string      `json:"closed_by,omitempty"`
	Stop       float64     `json:"stop"`       // 初始止损，0 = 没设
	Risk       float64     `json:"risk"`       // 初始风险（USDT）：开仓价到初始止损 × 数量
	R          *float64    `json:"r"`          // 净收益 ÷ 初始风险；没设止损时是空
	Violations []string    `json:"violations"` // no_stop | over_risk | moved_stop | locked_reject
	Stops      []StopPoint `json:"stops"`
	Legs       []Leg       `json:"legs"`
	Note       *Note       `json:"note,omitempty"`
	Shots      []ShotMeta  `json:"shots,omitempty"`
	Playbook   int64       `json:"playbook,omitempty"`
}

// Reject 纪律锁拒绝的一次下单。
type Reject struct {
	T    int64  `json:"t"`
	Inst string `json:"inst"`
	Rule string `json:"rule"`
}

func f(d interface{ InexactFloat64() float64 }) float64 { return d.InexactFloat64() }

func round(v float64, d int) float64 {
	p := math.Pow(10, float64(d))
	return math.Round(v*p) / p
}

// Build 把账本里的交易变成日志。initial 是账户初始资金，perTrade 是每笔风险上限（占权益，0 = 不查）。
func Build(closed, open []portfolio.Trade, stops []trading.StopRec, rejects []Reject, initial, perTrade float64) []Trade {
	out := make([]Trade, 0, len(closed)+len(open))
	// 开仓时的权益 = 初始资金 + 之前平掉的交易的净收益
	byClose := append([]portfolio.Trade(nil), closed...)
	sort.Slice(byClose, func(i, j int) bool { return byClose[i].CloseTime < byClose[j].CloseTime })
	equityAt := func(t int64) float64 {
		e := initial
		for _, c := range byClose {
			if c.CloseTime > t {
				break
			}
			e += f(c.Net)
		}
		return e
	}
	conv := func(p portfolio.Trade) Trade {
		t := Trade{
			Instrument: p.Instrument, Side: p.Side, OpenTime: p.OpenTime, CloseTime: p.CloseTime,
			Qty: f(p.Qty), Entry: f(p.Entry), Exit: f(p.Exit), Gross: f(p.Gross), Fees: f(p.Fees), Funding: f(p.Funding), Net: f(p.Net),
			ClosedBy: p.ClosedBy, Violations: []string{}, Stops: []StopPoint{}, Legs: []Leg{},
		}
		if p.CloseTime > 0 {
			t.HoldMs = p.CloseTime - p.OpenTime
		}
		t.SlipKnown = len(p.Legs) > 0
		for _, l := range p.Legs {
			leg := Leg{T: l.Time, Side: l.Side, Qty: f(l.Qty), Px: f(l.Px), Fee: f(l.Fee), Ref: f(l.Ref), Source: l.Source, OrderID: l.OrderID}
			t.Legs = append(t.Legs, leg)
			if leg.Ref > 0 {
				if leg.Side == "buy" {
					t.Slip += (leg.Px - leg.Ref) * leg.Qty
				} else {
					t.Slip += (leg.Ref - leg.Px) * leg.Qty
				}
			} else {
				t.SlipKnown = false
			}
		}
		t.Slip = round(t.Slip, 8)
		if len(p.Legs) > 0 {
			t.ID = fmt.Sprintf("t%d", p.Legs[0].Seq)
			t.Origin = originOf(p.Legs)
		} else {
			t.ID = fmt.Sprintf("t%s-%d", p.Instrument, p.OpenTime)
			t.Origin = "manual"
		}
		if t.ClosedBy == "" && t.CloseTime > 0 && len(p.Legs) > 0 {
			switch last := p.Legs[len(p.Legs)-1]; {
			case last.Liq:
				t.ClosedBy = "liquidation"
			case last.Source == "sl":
				t.ClosedBy = "stop_loss"
			case last.Source == "tp":
				t.ClosedBy = "take_profit"
			}
		}
		applyStops(&t, stops)
		if t.Origin != "ai" {
			t.violate(equityAt(p.OpenTime), perTrade, rejects)
		}
		return t
	}
	for _, p := range closed {
		out = append(out, conv(p))
	}
	for _, p := range open {
		out = append(out, conv(p))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].OpenTime > out[j].OpenTime })
	return out
}

func originOf(legs []trading.Fill) string {
	for _, l := range legs {
		if strings.HasPrefix(l.Source, "bot:") {
			return "bot" // 机器人的单不混进手动的统计（D83 第 9 条）
		}
		if l.Source == "signal" {
			return "signal"
		}
	}
	isAI := func(s string) bool { return s == "ai" || s == "follow" }
	ai, manual := isAI(legs[0].Source), false
	for _, l := range legs {
		if s := l.Source; s == "manual" || s == "close" || s == "cond" {
			manual = true
		}
	}
	switch {
	case ai && manual:
		return "mixed"
	case ai:
		return "ai"
	}
	return "manual"
}

// applyStops 找出这笔交易期间止损的变化，定初始止损和 R。
func applyStops(t *Trade, stops []trading.StopRec) {
	end := t.CloseTime
	long := t.Side == "long"
	for _, s := range stops {
		if s.Instrument != t.Instrument || s.Time < t.OpenTime {
			continue
		}
		if end > 0 && s.Time >= end {
			continue
		}
		t.Stops = append(t.Stops, StopPoint{T: s.Time, Px: f(s.Px), Kind: s.Kind})
	}
	for _, s := range t.Stops {
		if s.Kind == "cancel" {
			continue
		}
		// 第一个止损要在亏钱的一侧才算风险；一开始就设在盈利一侧的，算不出 R
		if (long && s.Px < t.Entry) || (!long && s.Px > t.Entry) {
			t.Stop = s.Px
			t.Risk = round(math.Abs(t.Entry-s.Px)*t.Qty, 8)
		}
		break
	}
	if t.Risk > 0 && t.CloseTime > 0 {
		r := round(t.Net/t.Risk, 4)
		t.R = &r
	}
}

func (t *Trade) violate(equity, perTrade float64, rejects []Reject) {
	add := func(v string) { t.Violations = append(t.Violations, v) }
	if t.Stop == 0 && t.CloseTime > 0 {
		add("no_stop")
	}
	if perTrade > 0 && t.Risk > 0 && t.Risk > perTrade*equity*1.0001 {
		add("over_risk")
	}
	long := t.Side == "long"
	last := 0.0
	moved := false
	for _, s := range t.Stops {
		if s.Kind == "cancel" {
			if last != 0 {
				moved = true // 撤掉止损也算
			}
			last = 0
			continue
		}
		if last != 0 && ((long && s.Px < last) || (!long && s.Px > last)) {
			moved = true // 只有往亏钱方向挪才算，往盈利方向挪是保护利润
		}
		last = s.Px
	}
	if moved {
		add("moved_stop")
	}
	end := t.CloseTime
	if end == 0 {
		end = math.MaxInt64
	}
	for _, r := range rejects {
		if r.Inst == t.Instrument && r.T >= t.OpenTime-600_000 && r.T <= end {
			add("locked_reject")
			break
		}
	}
}

// ---------- 统计 ----------

// SmallSample 少于这么多笔，界面写"样本太少"。
const SmallSample = 20

// Stat 一组交易的统计。
type Stat struct {
	Key          string   `json:"key"`
	N            int      `json:"n"`
	Wins         int      `json:"wins"`
	WinRate      float64  `json:"win_rate"`
	Net          float64  `json:"net"`
	Expectancy   float64  `json:"expectancy"` // 平均每笔净收益
	AvgWin       float64  `json:"avg_win"`
	AvgLoss      float64  `json:"avg_loss"`      // 负数
	Payoff       *float64 `json:"payoff"`        // 盈亏比：平均盈利 ÷ 平均亏损的绝对值；没有亏损时为空
	ProfitFactor *float64 `json:"profit_factor"` // 总盈利 ÷ 总亏损的绝对值
	AvgR         *float64 `json:"avg_r"`
	RCount       int      `json:"r_count"`
	Small        bool     `json:"small"`
}

// StatOf 算一组。
func StatOf(key string, ts []Trade) Stat {
	s := Stat{Key: key, Small: len(ts) < SmallSample}
	var win, loss, rs float64
	lossN := 0
	for _, t := range ts {
		s.N++
		s.Net += t.Net
		if t.Net > 0 {
			s.Wins++
			win += t.Net
		} else if t.Net < 0 {
			lossN++
			loss += t.Net
		}
		if t.R != nil {
			rs += *t.R
			s.RCount++
		}
	}
	if s.N == 0 {
		return s
	}
	s.WinRate = round(float64(s.Wins)/float64(s.N), 4)
	s.Expectancy = round(s.Net/float64(s.N), 4)
	if s.Wins > 0 {
		s.AvgWin = round(win/float64(s.Wins), 4)
	}
	if lossN > 0 {
		s.AvgLoss = round(loss/float64(lossN), 4)
	}
	if lossN > 0 && s.Wins > 0 {
		v := round(s.AvgWin/math.Abs(s.AvgLoss), 4)
		s.Payoff = &v
	}
	if loss < 0 {
		v := round(win/math.Abs(loss), 4)
		s.ProfitFactor = &v
	}
	if s.RCount > 0 {
		v := round(rs/float64(s.RCount), 4)
		s.AvgR = &v
	}
	s.Net = round(s.Net, 4)
	return s
}

// Stats 统计页要的全部东西。
type Stats struct {
	All        Stat                `json:"all"`
	Groups     map[string][]Stat   `json:"groups"` // instrument | slot | weekday | playbook
	Calendar   []CalDay            `json:"calendar"`
	Underwater []WaterPoint        `json:"underwater"`
	RDist      []RBucket           `json:"r_dist"`
	NoR        int                 `json:"no_r"`
	Waterfall  Waterfall           `json:"waterfall"`
	Violations map[string]ViolStat `json:"violations"`
}

// CalDay 一天的盈亏（用户所在时区）。
type CalDay struct {
	Day string  `json:"day"`
	Net float64 `json:"net"`
	N   int     `json:"n"`
}

// WaterPoint 回撤水下图的一个点：平掉一笔之后的权益和离最高点的距离。
type WaterPoint struct {
	T  int64   `json:"t"`
	Eq float64 `json:"eq"`
	DD float64 `json:"dd"` // 负数，比例
}

// RBucket R 分布的一格：[Lo, Hi)。
type RBucket struct {
	Lo float64 `json:"lo"`
	Hi float64 `json:"hi"`
	N  int     `json:"n"`
}

// Waterfall 成本瀑布。理想盈亏 − 滑点 = 平仓盈亏；平仓盈亏 − 手续费 + 资金费 = 净收益。
type Waterfall struct {
	Ideal   float64 `json:"ideal"`
	Slip    float64 `json:"slip"`
	Gross   float64 `json:"gross"`
	Fees    float64 `json:"fees"`
	Funding float64 `json:"funding"`
	Net     float64 `json:"net"`
	Unknown int     `json:"unknown_slip"` // 没有参考价的笔数（改版之前的成交）
}

// ViolStat 一种违规的笔数和这些交易的净收益。
type ViolStat struct {
	N   int     `json:"n"`
	Net float64 `json:"net"`
}

var slotNames = []string{"s0", "s1", "s2", "s3"} // 0–6、6–12、12–18、18–24 点

func localOf(ms int64, tzMin int) time.Time {
	return time.UnixMilli(ms).UTC().Add(time.Duration(tzMin) * time.Minute)
}

// Compute 从已平仓的交易算统计。tzMin 是比 UTC 早多少分钟。
func Compute(all []Trade, tzMin int, initial float64) Stats {
	closed := make([]Trade, 0, len(all))
	for _, t := range all {
		if t.CloseTime > 0 {
			closed = append(closed, t)
		}
	}
	sort.Slice(closed, func(i, j int) bool { return closed[i].CloseTime < closed[j].CloseTime })
	st := Stats{All: StatOf("all", closed), Groups: map[string][]Stat{}, Violations: map[string]ViolStat{}}
	group := func(name string, keyOf func(Trade) string) {
		m := map[string][]Trade{}
		for _, t := range closed {
			k := keyOf(t)
			m[k] = append(m[k], t)
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := []Stat{}
		for _, k := range keys {
			out = append(out, StatOf(k, m[k]))
		}
		st.Groups[name] = out
	}
	group("instrument", func(t Trade) string { return t.Instrument })
	group("slot", func(t Trade) string { return slotNames[localOf(t.OpenTime, tzMin).Hour()/6] })
	group("weekday", func(t Trade) string { return fmt.Sprintf("d%d", (int(localOf(t.OpenTime, tzMin).Weekday())+6)%7) }) // d0 = 周一
	group("playbook", func(t Trade) string {
		if t.Playbook == 0 {
			return "none"
		}
		return fmt.Sprintf("%d", t.Playbook)
	})
	// 盈亏日历：按平仓那天算
	days := map[string]*CalDay{}
	for _, t := range closed {
		k := localOf(t.CloseTime, tzMin).Format("2006-01-02")
		d := days[k]
		if d == nil {
			d = &CalDay{Day: k}
			days[k] = d
		}
		d.Net += t.Net
		d.N++
	}
	st.Calendar = []CalDay{}
	for _, d := range days {
		d.Net = round(d.Net, 4)
		st.Calendar = append(st.Calendar, *d)
	}
	sort.Slice(st.Calendar, func(i, j int) bool { return st.Calendar[i].Day < st.Calendar[j].Day })
	// 水下图
	st.Underwater = []WaterPoint{}
	eq, peak := initial, initial
	for _, t := range closed {
		eq += t.Net
		peak = math.Max(peak, eq)
		dd := 0.0
		if peak > 0 {
			dd = eq/peak - 1
		}
		st.Underwater = append(st.Underwater, WaterPoint{T: t.CloseTime, Eq: round(eq, 4), DD: round(dd, 6)})
	}
	// R 分布
	edges := []float64{-1e9, -3, -2, -1, 0, 1, 2, 3, 1e9}
	for i := 0; i+1 < len(edges); i++ {
		st.RDist = append(st.RDist, RBucket{Lo: edges[i], Hi: edges[i+1]})
	}
	for _, t := range closed {
		if t.R == nil {
			st.NoR++
			continue
		}
		for i := range st.RDist {
			if *t.R >= st.RDist[i].Lo && *t.R < st.RDist[i].Hi {
				st.RDist[i].N++
				break
			}
		}
	}
	// 成本瀑布
	w := &st.Waterfall
	for _, t := range closed {
		w.Gross += t.Gross
		w.Fees += t.Fees
		w.Funding += t.Funding
		w.Net += t.Net
		w.Slip += t.Slip
		if !t.SlipKnown {
			w.Unknown++
		}
		for _, v := range t.Violations {
			x := st.Violations[v]
			x.N++
			x.Net = round(x.Net+t.Net, 4)
			st.Violations[v] = x
		}
	}
	w.Ideal = round(w.Gross+w.Slip, 4)
	w.Gross, w.Fees, w.Funding, w.Net, w.Slip = round(w.Gross, 4), round(w.Fees, 4), round(w.Funding, 4), round(w.Net, 4), round(w.Slip, 4)
	return st
}

// ---------- MAE / MFE（J）----------

// Pt 价格路径上的一段。逐笔成交时 Hi = Lo。
type Pt struct {
	T      int64
	Hi, Lo float64
	C      float64
}

// Excursion 一笔交易持仓期间最多浮亏、最多浮盈。
type Excursion struct {
	ID     string   `json:"id"`
	MAE    float64  `json:"mae"` // 价格距离，非负
	MFE    float64  `json:"mfe"`
	MAEUSD float64  `json:"mae_usd"`
	MFEUSD float64  `json:"mfe_usd"`
	MAER   *float64 `json:"mae_r"`
	MFER   *float64 `json:"mfe_r"`
	Source string   `json:"source"` // ticks | 1m | none
	Net    float64  `json:"net"`
	R      *float64 `json:"r"`
}

// ExcursionOf 从价格路径算。
func ExcursionOf(t Trade, pts []Pt, source string) Excursion {
	e := Excursion{ID: t.ID, Source: source, Net: t.Net, R: t.R}
	long := t.Side == "long"
	for _, p := range pts {
		if p.T < t.OpenTime-60_000 || (t.CloseTime > 0 && p.T > t.CloseTime) {
			continue
		}
		if long {
			e.MFE = math.Max(e.MFE, p.Hi-t.Entry)
			e.MAE = math.Max(e.MAE, t.Entry-p.Lo)
		} else {
			e.MFE = math.Max(e.MFE, t.Entry-p.Lo)
			e.MAE = math.Max(e.MAE, p.Hi-t.Entry)
		}
	}
	e.MAE, e.MFE = round(e.MAE, 8), round(e.MFE, 8)
	e.MAEUSD, e.MFEUSD = round(e.MAE*t.Qty, 4), round(e.MFE*t.Qty, 4)
	if t.Stop > 0 {
		d := math.Abs(t.Entry - t.Stop)
		a, b := round(e.MAE/d, 3), round(e.MFE/d, 3)
		e.MAER, e.MFER = &a, &b
	}
	return e
}

// ---------- 结论（5.8）----------

// Finding 一条结论。文案在界面翻译，这里只给数字。
type Finding struct {
	Code  string             `json:"code"`
	N     int                `json:"n"`     // 涉及多少笔
	Of    int                `json:"of"`    // 一共多少笔
	Delta float64            `json:"delta"` // 反事实：净收益会变化多少；非反事实时为这些交易的净收益
	Args  map[string]float64 `json:"args,omitempty"`
	Key   string             `json:"key,omitempty"`
	Small bool               `json:"small"` // 样本太少
}

// After 取平仓之后一段时间的价格路径（看止损放宽会怎样）。
type After func(t Trade, until int64) []Pt

// Findings 算结论，按影响大小排序。
func Findings(trades []Trade, exc map[string]Excursion, after After) []Finding {
	closed := []Trade{}
	for _, t := range trades {
		if t.CloseTime > 0 {
			closed = append(closed, t)
		}
	}
	out := []Finding{}
	total := len(closed)
	push := func(x Finding) {
		x.Of = total
		x.Small = x.N < 5 || total < SmallSample
		x.Delta = round(x.Delta, 4)
		out = append(out, x)
	}
	// 反事实只用有 MAE/MFE 和止损的交易
	withStop := func(fn func(t Trade, e Excursion, d float64)) int {
		n := 0
		for _, t := range closed {
			e, ok := exc[t.ID]
			if !ok || t.Stop == 0 || e.Source == "none" {
				continue
			}
			n++
			fn(t, e, math.Abs(t.Entry-t.Stop))
		}
		return n
	}
	// 止损收紧到 0.75 倍
	{
		const k = 0.75
		delta, n := 0.0, 0
		base := withStop(func(t Trade, e Excursion, d float64) {
			if e.MAE >= k*d {
				n++
				delta += -k*t.Risk - t.Gross
			}
		})
		if base > 0 && n > 0 {
			push(Finding{Code: "stop_tighter", N: n, Delta: delta, Args: map[string]float64{"k": k, "base": float64(base)}})
		}
	}
	// 止盈提前到 1R、2R
	for _, x := range []float64{1, 2} {
		delta, n := 0.0, 0
		base := withStop(func(t Trade, e Excursion, d float64) {
			if e.MFE >= x*d {
				n++
				delta += x*t.Risk - t.Gross
			}
		})
		if base > 0 && n > 0 {
			push(Finding{Code: "tp_earlier", N: n, Delta: delta, Args: map[string]float64{"x": x, "base": float64(base)}})
		}
	}
	// 止损放宽到 1.5 倍：只看被止损平掉的。之后一段时间内没碰到更远的止损，就在那段时间结束时按市价走
	if after != nil {
		const k = 1.5
		delta, n := 0.0, 0
		var hold int64
		cnt := int64(0)
		for _, t := range closed {
			hold += t.HoldMs
			cnt++
		}
		horizon := int64(3600_000)
		if cnt > 0 {
			horizon = max(horizon, hold/cnt)
		}
		for _, t := range closed {
			if t.Stop == 0 || t.ClosedBy != "stop_loss" {
				continue
			}
			pts := after(t, t.CloseTime+horizon)
			if len(pts) == 0 {
				continue
			}
			n++
			d := math.Abs(t.Entry - t.Stop)
			long := t.Side == "long"
			wide := t.Entry - k*d
			if !long {
				wide = t.Entry + k*d
			}
			hit := false
			last := pts[len(pts)-1].C
			for _, p := range pts {
				if (long && p.Lo <= wide) || (!long && p.Hi >= wide) {
					hit = true
					break
				}
			}
			var g float64
			switch {
			case hit:
				g = -k * t.Risk
			case long:
				g = (last - t.Entry) * t.Qty
			default:
				g = (t.Entry - last) * t.Qty
			}
			delta += g - t.Gross
		}
		if n > 0 {
			push(Finding{Code: "stop_wider", N: n, Delta: delta, Args: map[string]float64{"k": k, "hours": float64(horizon) / 3600_000}})
		}
	}
	// 没设止损的交易
	{
		n, net := 0, 0.0
		for _, t := range closed {
			if t.Stop == 0 {
				n++
				net += t.Net
			}
		}
		if n > 0 {
			push(Finding{Code: "no_stop", N: n, Delta: net})
		}
	}
	// 挪过止损的交易
	{
		n, net := 0, 0.0
		for _, t := range closed {
			for _, v := range t.Violations {
				if v == "moved_stop" {
					n++
					net += t.Net
				}
			}
		}
		if n > 0 {
			push(Finding{Code: "moved_stop", N: n, Delta: net})
		}
	}
	// 亏得最多的品种
	{
		m := map[string]*Finding{}
		for _, t := range closed {
			x := m[t.Instrument]
			if x == nil {
				x = &Finding{Code: "worst_inst", Key: t.Instrument}
				m[t.Instrument] = x
			}
			x.N++
			x.Delta += t.Net
		}
		var worst *Finding
		for _, x := range m {
			if x.Delta < 0 && (worst == nil || x.Delta < worst.Delta || (x.Delta == worst.Delta && x.Key < worst.Key)) {
				worst = x
			}
		}
		if worst != nil {
			push(*worst)
		}
	}
	// 费用占比：手续费和滑点吃掉了多少毛利
	{
		profit, cost := 0.0, 0.0
		for _, t := range closed {
			profit += math.Max(t.Gross, 0)
			cost += t.Fees + math.Max(t.Slip, 0)
		}
		if profit > 0 && cost > 0 {
			push(Finding{Code: "cost_share", N: total, Delta: -cost, Args: map[string]float64{"pct": round(cost/profit*100, 1)}})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return math.Abs(out[i].Delta) > math.Abs(out[j].Delta) })
	return out
}

// ---------- CSV ----------

// csvRow 写一行。逗号、引号、换行要转义；开头是 = + @ 的文字前面加单引号，防止表格软件当公式执行。
func csvRow(b *strings.Builder, cols ...string) {
	for i, c := range cols {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(csvCell(c))
	}
	b.WriteString("\r\n")
}

func csvCell(c string) string {
	if c != "" && strings.ContainsAny(c[:1], "=+@\t\r") {
		c = "'" + c
	}
	if strings.ContainsAny(c, ",\"\r\n") {
		return `"` + strings.ReplaceAll(c, `"`, `""`) + `"`
	}
	return c
}

// SpotFill 现货的一笔成交（只留复盘要用的）。
type SpotFill struct {
	Seq     int64
	Time    int64
	OrderID string
	Inst    string
	Side    string
	Qty, Px decimal.Decimal
	Fee     decimal.Decimal // 手续费，按币算
	FeeUSDT decimal.Decimal
	FeeBase bool // 手续费扣的是买到的币
	Maker   bool
	Source  string
}

// FromSpot 把现货成交换算成合约的格式，交易日志只有一套算法。
// 买入时手续费扣币，所以到手的数量少一点；卖完剩下不够一个最小单位的算卖光了，不留一笔永远开着的交易。
func FromSpot(list []SpotFill, stepOf func(string) decimal.Decimal) []trading.Fill {
	type st struct{ qty, avg decimal.Decimal }
	pos := map[string]*st{}
	out := make([]trading.Fill, 0, len(list))
	for _, f := range list {
		p := pos[f.Inst]
		if p == nil {
			p = &st{}
			pos[f.Inst] = p
		}
		x := trading.Fill{Seq: f.Seq + 1<<40, Time: f.Time, OrderID: f.OrderID, Instrument: f.Inst, Side: f.Side, Px: f.Px, Fee: f.FeeUSDT, Maker: f.Maker, Source: f.Source}
		if f.Side == "buy" {
			got := f.Qty
			if f.FeeBase {
				got = got.Sub(f.Fee)
			}
			total := p.qty.Add(got)
			if total.IsPositive() {
				p.avg = p.avg.Mul(p.qty).Add(f.Px.Mul(got)).Div(total)
			}
			p.qty = total
			x.Qty = got
		} else {
			q := f.Qty
			left := p.qty.Sub(q)
			if left.LessThan(stepOf(f.Inst)) || left.IsNegative() {
				q = p.qty
				left = decimal.Zero
			}
			x.Qty = q
			x.Realized = f.Px.Sub(p.avg).Mul(q)
			p.qty = left
		}
		out = append(out, x)
	}
	return out
}
