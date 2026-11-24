package backtest

import (
	"context"
	"math"
	"testing"

	"github.com/shopspring/decimal"

	"qs/internal/trading"
)

// fake 造的历史数据：px(t) 给每分钟的开盘价，K 线上下各 5，收盘比开盘高 1（收涨）。
type fake struct {
	px   func(i int) float64
	mark func(i int) float64 // nil = 和最新价一样
	fund []trading.FundingSettle
}

const t0 = int64(1_767_225_600_000) // 2026-01-01 00:00 UTC

func (f fake) Bars(_ context.Context, _ string, from, to int64, mark bool) ([]Bar, error) {
	var out []Bar
	for t := from; t < to; t += 60_000 {
		i := int((t - t0) / 60_000)
		o := f.px(i)
		if mark && f.mark != nil {
			o = f.mark(i)
		}
		out = append(out, Bar{T: t, O: o, H: o + 5, L: o - 5, C: o + 1})
	}
	return out, nil
}

func (f fake) Funding(context.Context, string, int64, int64) ([]trading.FundingSettle, error) {
	return f.fund, nil
}

func D(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func run(t *testing.T, cfg Config, src Source) *Result {
	t.Helper()
	specs := trading.NewSpecs(trading.LooseSpecs())
	if err := Prepare(&cfg, func(i string) bool { _, ok := specs.Get(i); return ok }); err != nil {
		t.Fatal(err)
	}
	r, err := Run(context.Background(), cfg, src, specs)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func near(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func TestSignalsProfitFeesFunding(t *testing.T) {
	src := fake{px: func(i int) float64 { return 60000 + float64(i) }, fund: []trading.FundingSettle{
		{Time: t0 + 8*3600_000, Rate: D("0.0001"), Mark: D("60480")},
	}}
	r := run(t, Config{From: t0, To: t0 + 10*3600_000, Initial: D("10000"), Leverage: 10, Signals: []Signal{
		{T: t0, Text: "做多 BTC 50%"},
		{T: t0 + 9*3600_000, Text: "平仓 BTC"},
		{T: t0 + 9*3600_000 + 120_000, Text: "buy BTC 10%"}, // 现货信号：合约回测里不执行
	}}, src)
	if len(r.Outcomes) != 3 || !r.Outcomes[0].OK || !r.Outcomes[1].OK || r.Outcomes[2].Code != "BT_SPOT_SIGNAL" {
		t.Fatalf("信号结果：%+v", r.Outcomes)
	}
	// 开仓：可用 10000 × 50% = 5000 USDT，按开盘价 60000 吃单 → 0.083 BTC（数量单位 0.001 向下取整）
	if len(r.Trades) != 1 {
		t.Fatalf("交易：%+v", r.Trades)
	}
	tr := r.Trades[0]
	if tr.Qty != 0.083 || tr.Entry != 60000 {
		t.Fatalf("开仓：%+v", tr)
	}
	// 平仓在第 540 分钟开盘：60540。毛利 0.083 × 540 = 44.82；手续费按吃单 0.05%
	gross := 0.083 * 540
	fees := 0.083*60000*0.0005 + 0.083*60540*0.0005
	if !near(r.Fees, fees, 0.01) {
		t.Fatalf("手续费 %v，应该 %v", r.Fees, fees)
	}
	// 资金费：持仓跨过第 8 小时的结算，多头付 0.083 × 结算时标记价 × 0.0001
	if r.Funding >= 0 || !near(r.Funding, -0.083*60480*0.0001, 0.01) {
		t.Fatalf("资金费 %v", r.Funding)
	}
	if !near(r.Final, 10000+gross-fees+r.Funding, 0.02) {
		t.Fatalf("最后权益 %v", r.Final)
	}
	if r.Stats.Trades != 1 || r.Stats.Wins != 1 || r.Journal.N != 1 {
		t.Fatalf("统计：%+v %+v", r.Stats, r.Journal)
	}
	if len(r.Equity) == 0 || r.Equity[len(r.Equity)-1].Equity != r.Final {
		t.Fatal("权益曲线最后一点是最后权益")
	}
}

func TestStopOnMarkAndLiquidation(t *testing.T) {
	// 最新价有一根插针到 59000，标记价没跟：按标记价触发的止损不该被打掉
	spike := func(i int) float64 {
		if i == 30 {
			return 59000
		}
		return 60000
	}
	src := fake{px: spike, mark: func(int) float64 { return 60000 }}
	r := run(t, Config{From: t0, To: t0 + 3600_000, Leverage: 10, Signals: []Signal{
		{T: t0, Order: &trading.OrderReq{Instrument: "BTC-PERP", Side: "buy", Type: trading.Market, Qty: D("0.1"), SL: D("59500")}},
	}}, src)
	if len(r.Trades) != 1 || r.Trades[0].CloseTime != 0 {
		t.Fatalf("插针不该打到标记价止损：%+v", r.Trades)
	}
	// 按最新价触发：插针就打掉了，成交在止损价
	r = run(t, Config{From: t0, To: t0 + 3600_000, Leverage: 10, Signals: []Signal{
		{T: t0, Order: &trading.OrderReq{Instrument: "BTC-PERP", Side: "buy", Type: trading.Market, Qty: D("0.1"), SL: D("59500"), TriggerBy: trading.ByLast}},
	}}, src)
	if len(r.Trades) != 1 || r.Trades[0].CloseTime == 0 {
		t.Fatalf("按最新价的止损应该被插针打掉：%+v", r.Trades)
	}
	// 20 倍、3 BTC（保证金 9000），每分钟跌 100：半小时左右强平（标记价）
	down := func(i int) float64 { return 60000 - float64(i)*100 }
	r = run(t, Config{From: t0, To: t0 + 3600_000, Leverage: 20, Signals: []Signal{{T: t0, Order: &trading.OrderReq{Instrument: "BTC-PERP", Side: "buy", Type: trading.Market, Qty: D("3")}}}}, fake{px: down})
	if !r.Outcomes[0].OK {
		t.Fatalf("开仓：%+v", r.Outcomes[0])
	}
	if r.Liquidations == 0 || r.LiqFees <= 0 || r.Final > 10000*0.2 {
		t.Fatalf("应该强平：%d 次，清算费 %v，剩 %v", r.Liquidations, r.LiqFees, r.Final)
	}
}

func TestLatencyAndLimit(t *testing.T) {
	src := fake{px: func(i int) float64 { return 60000 + float64(i)*10 }}
	// 延迟 30 秒：第 0 分钟开盘发的信号，到交易所时走到了这一分钟的第三步（收涨的 K 线：最高 60005）
	r := run(t, Config{From: t0, To: t0 + 600_000, LatencyMS: 30_000, Signals: []Signal{{T: t0, Text: "long BTC 10%"}}}, src)
	if r.Outcomes[0].Exec != t0+30_000 || r.Trades[0].Entry != 60005 {
		t.Fatalf("延迟：到交易所 %v，开仓价 %v", r.Outcomes[0].Exec-t0, r.Trades[0].Entry)
	}
	// 延迟 120 毫秒：到交易所的那一刻在开盘和第一步（最低 60000+10·0−5）之间，按插值成交，不跳到最低价
	r = run(t, Config{From: t0, To: t0 + 600_000, LatencyMS: 120, Signals: []Signal{{T: t0, Text: "long BTC 10%"}}}, src)
	if r.Outcomes[0].Exec != t0+120 || math.Abs(r.Trades[0].Entry-(60000-5*0.008)) > 0.11 {
		t.Fatalf("小延迟：到交易所 %v，开仓价 %v", r.Outcomes[0].Exec-t0, r.Trades[0].Entry)
	}
	// 限价单要价格穿过才成交：挂 60000 买，价格只碰到 60000（最低）不成交
	flat := fake{px: func(int) float64 { return 60005 }}
	r = run(t, Config{From: t0, To: t0 + 600_000, Signals: []Signal{{T: t0, Order: &trading.OrderReq{Instrument: "BTC-PERP", Side: "buy", Type: trading.Limit, TIF: trading.GTC, Price: D("60000"), Qty: D("0.01")}}}}, flat)
	if !r.Outcomes[0].OK || len(r.Trades) != 0 {
		t.Fatalf("只碰到不成交：%+v %+v", r.Outcomes, r.Trades)
	}
	// 早于开始时间的信号不执行，说明原因
	r = run(t, Config{From: t0, To: t0 + 600_000, Signals: []Signal{{T: t0 - 3600_000, Text: "long BTC 10%"}, {T: t0, Text: "long BTC 10%"}}}, flat)
	if r.Outcomes[0].Code != "BT_BEFORE_START" || !r.Outcomes[1].OK {
		t.Fatalf("早于开始：%+v", r.Outcomes)
	}
	// 拒单原因原样带回来
	r = run(t, Config{From: t0, To: t0 + 600_000, Signals: []Signal{{T: t0, Order: &trading.OrderReq{Instrument: "BTC-PERP", Side: "buy", Type: trading.Market, Qty: D("0.0001")}}}}, flat)
	if r.Outcomes[0].OK || r.Outcomes[0].Code != "QTY_TOO_SMALL" {
		t.Fatalf("拒单：%+v", r.Outcomes[0])
	}
}

func TestPrepare(t *testing.T) {
	known := func(i string) bool { return i == "BTC-PERP" || i == "ETH-PERP" }
	c := Config{From: t0, To: t0 + 200*day, Signals: []Signal{{T: t0, Text: "做多 BTC 10%"}}}
	if Prepare(&c, known) != ErrRange {
		t.Fatal("超过 180 天")
	}
	c = Config{From: t0, To: t0 + day, Signals: []Signal{{T: t0 + 5, Text: "做多 ETH 10%"}, {T: t0, Text: "做多 BTC 10%"}}}
	if err := Prepare(&c, known); err != nil || len(c.Instruments) != 2 || c.Signals[0].T != t0 || c.Leverage != 10 || !c.Initial.Equal(D("10000")) {
		t.Fatalf("%v %+v", err, c)
	}
	c = Config{From: t0, To: t0 + day, Signals: []Signal{{T: t0, Text: "做多 DOGEX 10%"}}}
	if Prepare(&c, known) != ErrUnknown {
		t.Fatal("不认识的品种")
	}
}

func TestParseLines(t *testing.T) {
	text := "time,signal\n# 注释\n2026-01-01 00:00,做多 BTC 30%\n1767229200,\"{\"\"action\"\":\"\"close\"\",\"\"symbol\"\":\"\"BTCUSDT\"\"}\"\n2026-01-01T02:00:00Z,{\"instrument\":\"BTC-PERP\",\"side\":\"buy\",\"type\":\"limit\",\"price\":\"60000\",\"qty\":\"0.01\"}\n"
	s, err := ParseLines(text)
	if err != nil || len(s) != 3 {
		t.Fatalf("%v %+v", err, s)
	}
	if s[0].T != t0 || s[0].Text != "做多 BTC 30%" || s[1].T != t0+3600_000 || s[1].Text != `{"action":"close","symbol":"BTCUSDT"}` || s[2].Order == nil || s[2].Order.Type != "limit" {
		t.Fatalf("%+v", s)
	}
	if _, err := ParseLines("2026-01-01 00:00,做多 BTC 30%\nnot-a-time,x"); err == nil || err.(*LineError).Line != 2 {
		t.Fatalf("第 2 行时间不对：%v", err)
	}
}

// 18 第 5.5 条：目标仓位（qsig/1 的写法）。按当时权益 × 权重换算，反手先平再开，权重 0 全平
func TestTargets(t *testing.T) {
	src := fake{px: func(i int) float64 { return 60000 }}
	sigs, err := ParseLines("2026-01-01 00:00,{\"instrument\":\"BTC-PERP\",\"weight\":0.5}\n2026-01-01 01:00,{\"instrument\":\"BTC-PERP\",\"weight\":-0.3}\n2026-01-01 02:00,{\"instrument\":\"BTC-PERP\",\"weight\":0}")
	if err != nil || len(sigs) != 3 || sigs[1].Target == nil || !sigs[1].Target.Weight.Equal(D("-0.3")) {
		t.Fatalf("解析：%v %+v", err, sigs)
	}
	r := run(t, Config{From: t0, To: t0 + 3*3600_000, Initial: D("10000"), Leverage: 10, Signals: sigs}, src)
	for _, o := range r.Outcomes {
		if !o.OK {
			t.Fatalf("目标仓位没执行：%+v", r.Outcomes)
		}
	}
	// 0.5 × 10000 ÷ 60000 = 0.0833 → 0.083 多；−0.3 × 权益 ÷ 60000 ≈ −0.05 → 先平 0.083 再开空约 0.049；最后全平
	if len(r.Trades) != 2 || r.Trades[1].Side != "long" || r.Trades[1].Qty != 0.083 || r.Trades[0].Side != "short" || r.Trades[0].Qty < 0.048 || r.Trades[0].Qty > 0.05 { // 新的在前
		t.Fatalf("交易：%+v", r.Trades)
	}
}
