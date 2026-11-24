package journal

import (
	"context"
	"math"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"qs/internal/portfolio"
	"qs/internal/trading"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func pt(id int64, inst, side string, open, closeT int64, qty, entry, exit, gross, fees, funding, net string, legs ...trading.Fill) portfolio.Trade {
	if len(legs) == 0 {
		legs = []trading.Fill{{Seq: id, Time: open, Side: "buy", Qty: d(qty), Px: d(entry), OrderID: "o1", Source: "manual"}}
	}
	return portfolio.Trade{Instrument: inst, Side: side, OpenTime: open, CloseTime: closeT, Qty: d(qty), Entry: d(entry), Exit: d(exit), Gross: d(gross), Fees: d(fees), Funding: d(funding), Net: d(net), Legs: legs}
}

// 5.1 手算：多 2 个，开仓 100，止损 95，风险 = 5 × 2 = 10；净收益 25，R = 2.5
func TestRByHand(t *testing.T) {
	stops := []trading.StopRec{{Time: 1500, Instrument: "BTC-PERP", Px: d("95"), Kind: "set"}, {Time: 2500, Instrument: "BTC-PERP", Px: d("99"), Kind: "set"}}
	out := Build([]portfolio.Trade{pt(7, "BTC-PERP", "long", 1000, 5000, "2", "100", "114", "28", "3", "0", "25")}, nil, stops, nil, 10000, 0)
	x := out[0]
	if x.ID != "t7" || x.Stop != 95 || x.Risk != 10 || x.R == nil || *x.R != 2.5 || x.HoldMs != 4000 {
		t.Fatalf("%+v", x)
	}
	if len(x.Violations) != 0 {
		t.Fatalf("往盈利方向挪止损不算违规：%v", x.Violations)
	}
}

// 没设止损：写无 R，标违规
func TestNoStop(t *testing.T) {
	out := Build([]portfolio.Trade{pt(1, "BTC-PERP", "long", 1000, 5000, "1", "100", "90", "-10", "1", "0", "-11")}, nil, nil, nil, 10000, 0)
	if out[0].R != nil || out[0].Stop != 0 || !has(out[0].Violations, "no_stop") {
		t.Fatalf("%+v", out[0])
	}
}

func has(v []string, s string) bool {
	for _, x := range v {
		if x == s {
			return true
		}
	}
	return false
}

// 5.4 四种违规
func TestViolations(t *testing.T) {
	inst := "BTC-PERP"
	// 超过每笔风险：权益 10000，上限 1% = 100；风险 = 5 × 100 = 500
	a := Build([]portfolio.Trade{pt(1, inst, "long", 1000, 5000, "100", "100", "99", "-100", "1", "0", "-101")}, nil,
		[]trading.StopRec{{Time: 1100, Instrument: inst, Px: d("95"), Kind: "set"}}, nil, 10000, 0.01)
	if !has(a[0].Violations, "over_risk") {
		t.Fatalf("应超风险：%v", a[0].Violations)
	}
	// 风险 = 5 × 10 = 50，没超
	b := Build([]portfolio.Trade{pt(1, inst, "long", 1000, 5000, "10", "100", "99", "-10", "1", "0", "-11")}, nil,
		[]trading.StopRec{{Time: 1100, Instrument: inst, Px: d("95"), Kind: "set"}}, nil, 10000, 0.01)
	if has(b[0].Violations, "over_risk") {
		t.Fatal("没超风险")
	}
	// 多单止损往下挪：移动止损
	c := Build([]portfolio.Trade{pt(1, inst, "long", 1000, 5000, "1", "100", "99", "-1", "1", "0", "-2")}, nil,
		[]trading.StopRec{{Time: 1100, Instrument: inst, Px: d("95"), Kind: "set"}, {Time: 1200, Instrument: inst, Px: d("90"), Kind: "set"}}, nil, 10000, 0)
	if !has(c[0].Violations, "moved_stop") || c[0].Stop != 95 {
		t.Fatalf("多单止损下移：%+v", c[0])
	}
	// 空单止损往上挪：移动止损；初始风险按第一个算
	e := Build([]portfolio.Trade{pt(1, inst, "short", 1000, 5000, "1", "100", "101", "-1", "1", "0", "-2")}, nil,
		[]trading.StopRec{{Time: 1100, Instrument: inst, Px: d("105"), Kind: "set"}, {Time: 1200, Instrument: inst, Px: d("110"), Kind: "set"}}, nil, 10000, 0)
	if !has(e[0].Violations, "moved_stop") || e[0].Risk != 5 {
		t.Fatalf("空单止损上移：%+v", e[0])
	}
	// 撤掉止损也算
	g := Build([]portfolio.Trade{pt(1, inst, "long", 1000, 5000, "1", "100", "99", "-1", "1", "0", "-2")}, nil,
		[]trading.StopRec{{Time: 1100, Instrument: inst, Px: d("95"), Kind: "set"}, {Time: 1200, Instrument: inst, Px: d("95"), Kind: "cancel"}}, nil, 10000, 0)
	if !has(g[0].Violations, "moved_stop") {
		t.Fatalf("撤止损：%v", g[0].Violations)
	}
	// 锁定期间被拒：开仓前 10 分钟内或持仓期间
	h := Build([]portfolio.Trade{pt(1, inst, "long", 1000_000, 2000_000, "1", "100", "99", "-1", "1", "0", "-2")}, nil, nil,
		[]Reject{{T: 900_000, Inst: inst, Rule: "daily_loss"}}, 10000, 0)
	if !has(h[0].Violations, "locked_reject") {
		t.Fatalf("被拒：%v", h[0].Violations)
	}
	k := Build([]portfolio.Trade{pt(1, inst, "long", 1000_000, 2000_000, "1", "100", "99", "-1", "1", "0", "-2")}, nil, nil,
		[]Reject{{T: 1, Inst: inst}, {T: 1500_000, Inst: "ETH-PERP"}}, 10000, 0)
	if has(k[0].Violations, "locked_reject") {
		t.Fatal("太早的、别的币的不算")
	}
	// AI 的单不算违规
	m := Build([]portfolio.Trade{pt(1, inst, "long", 1000, 5000, "1", "100", "99", "-1", "1", "0", "-2", trading.Fill{Seq: 1, Source: "ai", Side: "buy", Qty: d("1"), Px: d("100")})}, nil, nil, nil, 10000, 0)
	if len(m[0].Violations) != 0 || m[0].Origin != "ai" {
		t.Fatalf("AI：%+v", m[0])
	}
}

// 别的币的止损、这笔之外的止损不算
func TestStopsScoped(t *testing.T) {
	inst := "BTC-PERP"
	out := Build([]portfolio.Trade{pt(1, inst, "long", 1000, 5000, "1", "100", "99", "-1", "1", "0", "-2")}, nil,
		[]trading.StopRec{{Time: 900, Instrument: inst, Px: d("80"), Kind: "set"}, {Time: 1100, Instrument: "ETH-PERP", Px: d("80"), Kind: "set"}, {Time: 5000, Instrument: inst, Px: d("80"), Kind: "set"}}, nil, 10000, 0)
	if out[0].Stop != 0 {
		t.Fatalf("不该算到这笔：%+v", out[0])
	}
}

// 第一个止损设在盈利一侧：没有初始风险，算不出 R
func TestStopOnProfitSide(t *testing.T) {
	inst := "BTC-PERP"
	out := Build([]portfolio.Trade{pt(1, inst, "long", 1000, 5000, "1", "100", "110", "10", "1", "0", "9")}, nil,
		[]trading.StopRec{{Time: 1100, Instrument: inst, Px: d("105"), Kind: "set"}}, nil, 10000, 0)
	if out[0].R != nil || out[0].Stop != 0 {
		t.Fatalf("%+v", out[0])
	}
}

// 手动、AI、混合
func TestOrigin(t *testing.T) {
	f := func(srcs ...string) string {
		var l []trading.Fill
		for _, s := range srcs {
			l = append(l, trading.Fill{Source: s})
		}
		return originOf(l)
	}
	if f("manual", "sl") != "manual" || f("ai", "ai") != "ai" || f("follow", "tp") != "ai" || f("ai", "close") != "mixed" || f("cond") != "manual" {
		t.Fatal("来源")
	}
}

// 5.10 滑点：买入 1 个，参考价 100，成交 101，成本 1；卖出 1 个，参考价 110，成交 109，成本 1
func TestSlipByHand(t *testing.T) {
	legs := []trading.Fill{
		{Seq: 1, Time: 1000, Side: "buy", Qty: d("1"), Px: d("101"), Ref: d("100"), Source: "manual"},
		{Seq: 2, Time: 2000, Side: "sell", Qty: d("1"), Px: d("109"), Ref: d("110"), Source: "close"},
	}
	out := Build([]portfolio.Trade{pt(1, "BTC-PERP", "long", 1000, 2000, "1", "101", "109", "8", "0.5", "0.1", "7.6", legs...)}, nil, nil, nil, 10000, 0)
	if out[0].Slip != 2 || !out[0].SlipKnown {
		t.Fatalf("滑点：%+v", out[0])
	}
	st := Compute(out, 0, 10000)
	w := st.Waterfall
	// 理想 10 − 滑点 2 = 平仓 8；8 − 手续费 0.5 + 资金费 0.1 = 净 7.6
	if w.Ideal != 10 || w.Slip != 2 || w.Gross != 8 || w.Fees != 0.5 || w.Funding != 0.1 || w.Net != 7.6 {
		t.Fatalf("瀑布：%+v", w)
	}
	if math.Abs(w.Ideal-w.Slip-w.Fees+w.Funding-w.Net) > 1e-9 {
		t.Fatal("数字要能加回去")
	}
	// 没有参考价（改版之前的成交）
	old := Build([]portfolio.Trade{pt(1, "BTC-PERP", "long", 1000, 2000, "1", "100", "101", "1", "0", "0", "1")}, nil, nil, nil, 10000, 0)
	if old[0].SlipKnown || Compute(old, 0, 10000).Waterfall.Unknown != 1 {
		t.Fatal("没有参考价要标出")
	}
}

func mk(net float64, r *float64) Trade {
	return Trade{ID: "x", Instrument: "BTC-PERP", OpenTime: 1000, CloseTime: 2000, Net: net, R: r}
}
func rp(v float64) *float64 { return &v }

// 5.5 手算：+30、+10、−20、0。胜率 2/4；平均盈利 20；平均亏损 −20；盈亏比 1；利润因子 40/20 = 2；期望 20/4 = 5
func TestStatsByHand(t *testing.T) {
	s := StatOf("all", []Trade{mk(30, rp(3)), mk(10, rp(1)), mk(-20, rp(-2)), mk(0, nil)})
	if s.N != 4 || s.Wins != 2 || s.WinRate != 0.5 || s.Net != 20 || s.Expectancy != 5 || s.AvgWin != 20 || s.AvgLoss != -20 {
		t.Fatalf("%+v", s)
	}
	if *s.Payoff != 1 || *s.ProfitFactor != 2 || *s.AvgR != 0.6667 || s.RCount != 3 || !s.Small {
		t.Fatalf("%+v", s)
	}
	// 全赢：没有亏损，盈亏比和利润因子写不出来
	a := StatOf("all", []Trade{mk(30, nil)})
	if a.Payoff != nil || a.ProfitFactor != nil || a.AvgR != nil {
		t.Fatalf("%+v", a)
	}
	big := make([]Trade, SmallSample)
	if StatOf("all", big).Small {
		t.Fatal("够 20 笔不算样本少")
	}
	if e := StatOf("x", nil); e.N != 0 || e.WinRate != 0 {
		t.Fatal("空")
	}
}

// 5.5、5.6 按时段、星期分组，日历、水下图、R 分布。时区东八区
func TestComputeByHand(t *testing.T) {
	// 2026-11-02 是星期一。UTC 15:30 = 东八区 23:30（周一、18–24 点）；UTC 16:30 = 东八区周二 00:30（0–6 点）
	const monday = int64(1793923200000) // 2026-11-02 00:00 UTC? 用下面的断言校准
	_ = monday
	base := localBase()
	a := Trade{ID: "a", Instrument: "BTC-PERP", OpenTime: base + 15*3600_000 + 1800_000, CloseTime: base + 15*3600_000 + 1900_000, Net: 100, R: rp(1)}
	b := Trade{ID: "b", Instrument: "ETH-PERP", OpenTime: base + 16*3600_000 + 1800_000, CloseTime: base + 17*3600_000, Net: -300, R: rp(-3.5), Playbook: 7}
	c := Trade{ID: "c", Instrument: "BTC-PERP", OpenTime: base + 17*3600_000, CloseTime: base + 18*3600_000, Net: 50}
	open := Trade{ID: "o", Instrument: "BTC-PERP", OpenTime: base, Net: 999}
	st := Compute([]Trade{b, a, c, open}, 480, 1000)
	if st.All.N != 3 || st.All.Net != -150 {
		t.Fatalf("还没平的不算：%+v", st.All)
	}
	keys := func(g string) map[string]int {
		m := map[string]int{}
		for _, s := range st.Groups[g] {
			m[s.Key] = s.N
		}
		return m
	}
	if k := keys("slot"); k["s3"] != 1 || k["s0"] != 2 {
		t.Fatalf("时段：%v", k)
	}
	if k := keys("weekday"); k["d0"] != 1 || k["d1"] != 2 {
		t.Fatalf("星期：%v", k)
	}
	if k := keys("instrument"); k["BTC-PERP"] != 2 || k["ETH-PERP"] != 1 {
		t.Fatalf("品种：%v", k)
	}
	if k := keys("playbook"); k["7"] != 1 || k["none"] != 2 {
		t.Fatalf("预案：%v", k)
	}
	// 日历：a 周一，b、c 周二
	if len(st.Calendar) != 2 || st.Calendar[0].Day != "2026-11-02" || st.Calendar[0].Net != 100 || st.Calendar[1].Day != "2026-11-03" || st.Calendar[1].Net != -250 || st.Calendar[1].N != 2 {
		t.Fatalf("日历：%+v", st.Calendar)
	}
	// 水下：1000 → 1100（新高，0）→ 800（800/1100−1）→ 850
	u := st.Underwater
	if len(u) != 3 || u[0].DD != 0 || u[1].Eq != 800 || math.Abs(u[1].DD-(800.0/1100-1)) > 1e-5 || u[2].Eq != 850 {
		t.Fatalf("水下：%+v", u)
	}
	// R 分布：1 落在 [1,2)，−3.5 落在最左边，无 R 一笔
	if st.NoR != 1 || st.RDist[0].N != 1 || st.RDist[5].N != 1 {
		t.Fatalf("R：%+v %d", st.RDist, st.NoR)
	}
}

func localBase() int64 {
	// 2026-11-02 00:00:00 UTC
	return 1793577600000
}

// 5.7 手算：多单开仓 100，止损 95，数量 2；最低 92、最高 112。MAE 8（1.6R），MFE 12（2.4R）
func TestExcursionByHand(t *testing.T) {
	x := Trade{ID: "a", Side: "long", Entry: 100, Stop: 95, Qty: 2, OpenTime: 1000_000, CloseTime: 2000_000, Net: 5}
	pts := []Pt{{T: 500_000, Hi: 200, Lo: 1}, {T: 1000_000, Hi: 101, Lo: 99}, {T: 1500_000, Hi: 112, Lo: 92}, {T: 3000_000, Hi: 300, Lo: 1}}
	e := ExcursionOf(x, pts, "1m")
	if e.MAE != 8 || e.MFE != 12 || e.MAEUSD != 16 || e.MFEUSD != 24 || *e.MAER != 1.6 || *e.MFER != 2.4 || e.Source != "1m" {
		t.Fatalf("%+v", e)
	}
	// 空单反过来
	y := Trade{ID: "b", Side: "short", Entry: 100, Stop: 105, Qty: 1, OpenTime: 1000_000, CloseTime: 2000_000}
	s := ExcursionOf(y, []Pt{{T: 1500_000, Hi: 103, Lo: 90}}, "ticks")
	if s.MAE != 3 || s.MFE != 10 || *s.MFER != 2 {
		t.Fatalf("空单：%+v", s)
	}
	// 没设止损：只有价格距离，没有 R
	z := ExcursionOf(Trade{ID: "c", Side: "long", Entry: 100, Qty: 1, OpenTime: 1, CloseTime: 2}, nil, "none")
	if z.MAER != nil || z.MAE != 0 {
		t.Fatalf("%+v", z)
	}
}

// 5.8 反事实手算：三笔多单，开仓 100，止损 90，数量 1（风险 10）
//
//	a：MAE 8（0.8R）MFE 25（2.5R），实际盈亏 +20
//	b：MAE 10（1R）MFE 2（0.2R），止损，实际 −10
//	c：MAE 3（0.3R）MFE 12（1.2R），实际 +5
//
// 止损收紧到 0.75 倍（7.5）：a、b 碰到。a：−7.5 − 20 = −27.5；b：−7.5 + 10 = +2.5。合计 −25
// 止盈提前到 1R：a、c 碰到。a：10 − 20 = −10；c：10 − 5 = +5。合计 −5
// 止盈提前到 2R：a。20 − 20 = 0
func TestFindingsByHand(t *testing.T) {
	mkT := func(id string, gross float64, by string) Trade {
		return Trade{ID: id, Instrument: "BTC-PERP", Side: "long", Entry: 100, Stop: 90, Risk: 10, Qty: 1, OpenTime: 1000, CloseTime: 3600_000, HoldMs: 3599_000, Gross: gross, Net: gross, ClosedBy: by}
	}
	ts := []Trade{mkT("a", 20, ""), mkT("b", -10, "stop_loss"), mkT("c", 5, "")}
	exc := map[string]Excursion{
		"a": {ID: "a", MAE: 8, MFE: 25, Source: "ticks"},
		"b": {ID: "b", MAE: 10, MFE: 2, Source: "ticks"},
		"c": {ID: "c", MAE: 3, MFE: 12, Source: "1m"},
	}
	// 被止损的 b：之后最低 88（没到放宽后的 85），最后 106。放宽后盈亏 +6，比 −10 多 16
	after := func(tr Trade, until int64) []Pt {
		return []Pt{{T: tr.CloseTime, Hi: 95, Lo: 88, C: 92}, {T: tr.CloseTime + 1, Hi: 107, Lo: 92, C: 106}}
	}
	fs := Findings(ts, exc, after)
	get := func(code string, x float64) *Finding {
		for i := range fs {
			if fs[i].Code == code && (x == 0 || fs[i].Args["x"] == x) {
				return &fs[i]
			}
		}
		return nil
	}
	if g := get("stop_tighter", 0); g == nil || g.N != 2 || g.Delta != -25 {
		t.Fatalf("收紧：%+v", g)
	}
	if g := get("tp_earlier", 1); g == nil || g.N != 2 || g.Delta != -5 {
		t.Fatalf("1R：%+v", g)
	}
	if g := get("tp_earlier", 2); g == nil || g.N != 1 || g.Delta != 0 {
		t.Fatalf("2R：%+v", g)
	}
	if g := get("stop_wider", 0); g == nil || g.N != 1 || g.Delta != 16 {
		t.Fatalf("放宽：%+v", g)
	}
	if !fs[0].Small {
		t.Fatal("总共 3 笔，样本太少要标出")
	}
	if math.Abs(fs[0].Delta) < math.Abs(fs[len(fs)-1].Delta) {
		t.Fatal("按影响大小排")
	}
	// 放宽后碰到更远的止损：−15，比 −10 少 5
	hit := func(tr Trade, until int64) []Pt { return []Pt{{T: tr.CloseTime, Hi: 95, Lo: 84, C: 84}} }
	if g := findCode(Findings(ts, exc, hit), "stop_wider"); g == nil || g.Delta != -5 {
		t.Fatalf("放宽后碰到：%+v", g)
	}
}

func findCode(fs []Finding, c string) *Finding {
	for i := range fs {
		if fs[i].Code == c {
			return &fs[i]
		}
	}
	return nil
}

func TestFindingsOther(t *testing.T) {
	ts := []Trade{
		{ID: "a", Instrument: "BTC-PERP", OpenTime: 1, CloseTime: 2, Net: -30, Gross: -25, Fees: 5, Violations: []string{"no_stop"}},
		{ID: "b", Instrument: "ETH-PERP", OpenTime: 1, CloseTime: 2, Net: -10, Gross: 100, Fees: 10, Stop: 1, Violations: []string{"moved_stop"}},
		{ID: "c", Instrument: "ETH-PERP", OpenTime: 1, CloseTime: 2, Net: 90, Gross: 100, Fees: 10, Stop: 1},
	}
	fs := Findings(ts, nil, nil)
	if g := findCode(fs, "no_stop"); g == nil || g.N != 1 || g.Delta != -30 {
		t.Fatalf("没设止损：%+v", g)
	}
	if g := findCode(fs, "moved_stop"); g == nil || g.N != 1 || g.Delta != -10 {
		t.Fatalf("挪止损：%+v", g)
	}
	if g := findCode(fs, "worst_inst"); g == nil || g.Key != "BTC-PERP" || g.Delta != -30 {
		t.Fatalf("最差品种：%+v", g)
	}
	// 手续费 25，毛利 200，占 12.5%
	if g := findCode(fs, "cost_share"); g == nil || g.Args["pct"] != 12.5 {
		t.Fatalf("费用：%+v", g)
	}
	if findCode(fs, "stop_tighter") != nil {
		t.Fatal("没有 MAE 数据时不算反事实")
	}
}

// 5.12 CSV：转义、防公式注入
func TestCSV(t *testing.T) {
	if csvCell(`a,b`) != `"a,b"` || csvCell(`say "hi"`) != `"say ""hi"""` || csvCell("=1+1") != "'=1+1" || csvCell("+1") != "'+1" || csvCell("plain") != "plain" {
		t.Fatal("转义")
	}
	var b strings.Builder
	r := 1.5
	ExportTrades(&b, []Trade{{ID: "t1", Instrument: "BTC-PERP", Side: "long", Origin: "manual", OpenTime: 1793577600000, CloseTime: 1793577660000, Qty: 1, Entry: 100, Net: 5, R: &r, Violations: []string{"no_stop"}, Note: &Note{Tags: []string{"追高"}, Note: "=危险, 内容"}}})
	s := b.String()
	if !strings.Contains(s, "t1,BTC-PERP,long,manual,2026-11-02 00:00:00,2026-11-02 00:01:00") || !strings.Contains(s, `"'=危险, 内容"`) || !strings.Contains(s, ",1.5,no_stop,追高,") {
		t.Fatalf("交易：%s", s)
	}
	b.Reset()
	ExportFills(&b, []trading.Fill{{Time: 1793577600000, Instrument: "BTC-PERP", Side: "buy", Qty: d("1"), Px: d("100"), Fee: d("0.1"), Realized: d("0"), OrderID: "o", Source: "manual"}})
	if !strings.Contains(b.String(), "2026-11-02 00:00:00,BTC-PERP,buy,1,100,0.1,0,false,manual,o") {
		t.Fatalf("成交：%s", b.String())
	}
	b.Reset()
	ExportFunding(&b, []trading.FundingRec{{Time: 1793577600000, Instrument: "BTC-PERP", Rate: d("0.0001"), Mark: d("100"), Qty: d("1"), Amount: d("-0.01")}})
	if !strings.Contains(b.String(), "2026-11-02 00:00:00,BTC-PERP,0.0001,100,1,-0.01") {
		t.Fatalf("资金费：%s", b.String())
	}
}

// ---------- 数据库 ----------

func testStore(t *testing.T) *Store {
	url := os.Getenv("QS_TEST_DB")
	if url == "" {
		t.Skip("没有 QS_TEST_DB")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `drop table if exists journal_notes, journal_shots, journal_playbooks, journal_order_playbook`); err != nil {
		t.Fatal(err)
	}
	s := &Store{Pool: pool}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}

// 5.2 笔记和截图：只有本人能看，每人上限
func TestStoreNotesShots(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.SaveNote(ctx, 1, 10, "t1", Note{Tags: []string{"追高"}, Note: "下次等回踩", Emotion: "急", Playbook: 3}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveNote(ctx, 1, 10, "t1", Note{Tags: []string{"追高", "重仓"}, Note: "改过", Emotion: "平静"}); err != nil {
		t.Fatal(err)
	}
	n, _ := s.Notes(ctx, 1, 10)
	if n["t1"].Note != "改过" || len(n["t1"].Tags) != 2 || n["t1"].Playbook != 0 {
		t.Fatalf("笔记：%+v", n["t1"])
	}
	if o, _ := s.Notes(ctx, 2, 10); len(o) != 0 {
		t.Fatal("别人看不到")
	}
	m, err := s.AddShot(ctx, 1, 10, "t1", "image/png", []byte("pngdata"))
	if err != nil || m.ID == 0 || m.Size != 7 {
		t.Fatalf("截图：%+v %v", m, err)
	}
	if _, _, err := s.Shot(ctx, 2, m.ID); err != ErrNotFound {
		t.Fatalf("别人取不到：%v", err)
	}
	mime, data, err := s.Shot(ctx, 1, m.ID)
	if err != nil || mime != "image/png" || string(data) != "pngdata" {
		t.Fatalf("取截图：%v %s", err, mime)
	}
	if err := s.DeleteShot(ctx, 2, m.ID); err != ErrNotFound {
		t.Fatalf("别人删不了：%v", err)
	}
	sh, _ := s.Shots(ctx, 1, 10)
	if len(sh["t1"]) != 1 {
		t.Fatalf("列表：%+v", sh)
	}
	// 已用快满：造一条占空间的记录（只改大小，不真的存 500 MB）
	if _, err := s.Pool.Exec(ctx, `update journal_shots set size=$1 where id=$2`, MaxShotsUser-3, m.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddShot(ctx, 1, 10, "t1", "image/png", []byte("xxxx")); err != ErrShotQuota {
		t.Fatalf("超过上限：%v", err)
	}
	if _, err := s.AddShot(ctx, 2, 20, "t9", "image/png", []byte("xxxx")); err != nil {
		t.Fatalf("别人不受影响：%v", err)
	}
	if err := s.DeleteShot(ctx, 1, m.ID); err != nil {
		t.Fatal(err)
	}
}

// 5.3 预案：建、改、删；删掉后统计里还能查到名字；别人动不了
func TestStorePlaybooks(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, err := s.SavePlaybook(ctx, 1, Playbook{Name: "回踩", Entry: "回踩均线", Stop: "前低", Target: "前高"})
	if err != nil || p.ID == 0 {
		t.Fatal(err)
	}
	if _, err := s.SavePlaybook(ctx, 2, Playbook{ID: p.ID, Name: "偷改"}); err != ErrNotFound {
		t.Fatalf("别人改不了：%v", err)
	}
	if _, err := s.SavePlaybook(ctx, 1, Playbook{ID: p.ID, Name: "回踩2", Entry: "x"}); err != nil {
		t.Fatal(err)
	}
	l, _ := s.Playbooks(ctx, 1)
	if len(l) != 1 || l[0].Name != "回踩2" {
		t.Fatalf("%+v", l)
	}
	if o, _ := s.Playbooks(ctx, 2); len(o) != 0 {
		t.Fatal("别人看不到")
	}
	if err := s.DeletePlaybook(ctx, 2, p.ID); err != ErrNotFound {
		t.Fatalf("别人删不了：%v", err)
	}
	if err := s.DeletePlaybook(ctx, 1, p.ID); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.Playbooks(ctx, 1); len(l) != 0 {
		t.Fatal("删掉后不在列表里")
	}
	if n, _ := s.AllPlaybookNames(ctx, 1); n[p.ID] != "回踩2" {
		t.Fatalf("统计里还能查到名字：%v", n)
	}
	if err := s.SetOrderPlaybook(ctx, 1, 10, "o1", p.ID); err != nil {
		t.Fatal(err)
	}
	if m, _ := s.OrderPlaybooks(ctx, 1, 10); m["o1"] != p.ID {
		t.Fatalf("%v", m)
	}
}

// 路由不能互相冲突：冲突时 Go 启动就崩（这里真的崩过一次）
func TestRoutesRegister(t *testing.T) {
	(&API{}).Routes(http.NewServeMux())
}
