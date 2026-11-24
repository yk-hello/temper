package trading

import (
	"testing"

	"github.com/shopspring/decimal"
)

// 模块 4：预检、撤开仓单、提醒、按来源查成交、压力测试（12 的 4.18、4.24）。

func at(t *testing.T, r *StressResult, x int) int {
	t.Helper()
	for i, m := range r.Moves {
		if m == x {
			return i
		}
	}
	t.Fatalf("没有 %d%% 这一格", x)
	return 0
}

func betaOne(string) decimal.Decimal { return decimal.NewFromInt(1) }

// 手算（逐仓多 1 BTC，开仓 60,000，10 倍）：逐仓保证金 6,000，手续费 30，钱包 9,970。
//   - 跌 9%（54,600）：逐仓余额 600，维持 54,600 × 0.4% = 218.4，不强平；权益 9,970 − 5,400 = 4,570
//   - 跌 10%（54,000）：逐仓余额 0 ≤ 维持，强平；亏掉 6,000，权益 3,970
//   - 再跌也是 3,970
//   - 涨 10%：权益 15,970
func TestStressIsolatedByHand(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	_ = x.e.SetMode(1, "BTC-PERP", "isolated")
	x.place(buy("BTC-PERP", "1"))
	r, err := x.e.Stress(1, betaOne)
	if err != nil {
		t.Fatal(err)
	}
	want := func(m int, v string) {
		t.Helper()
		eq(t, "权益", r.Equity[at(t, r, m)], D(v))
	}
	want(0, "9970")
	want(-9, "4570")
	want(-10, "3970")
	want(-50, "3970")
	want(10, "15970")
	p := r.Positions[0]
	if p.LiqDown == nil || *p.LiqDown != -10 || p.LiqUp != nil {
		t.Fatalf("应在跌 10%% 时强平：%+v", p)
	}
	eq(t, "强平后的盈亏变化", p.PnL[at(t, r, -30)], D("-6000"))
	// 和引擎真的强平对一下
	x.market("BTC-PERP", "54000")
	x.e.Tick()
	eq(t, "引擎强平后的钱包", x.acct().Wallet, D("3970"))
}

// 手算（全仓多 1.6 BTC，开仓 60,000）：手续费 48，钱包 9,952。
//   - 跌 10%（54,000）：余额 9,952 − 9,600 = 352，维持 86,400 × 0.4% = 345.6，不强平
//   - 跌 11%（53,400）：余额 9,952 − 10,560 < 0，强平，权益 0
func TestStressCrossByHand(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	x.place(buy("BTC-PERP", "1.6"))
	r, err := x.e.Stress(1, betaOne)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "跌 10%", r.Equity[at(t, r, -10)], D("352"))
	eq(t, "跌 11%", r.Equity[at(t, r, -11)], D("0"))
	eq(t, "跌 50%", r.Equity[at(t, r, -50)], D("0"))
	if p := r.Positions[0]; p.LiqDown == nil || *p.LiqDown != -11 {
		t.Fatalf("应在跌 11%% 时强平：%+v", p)
	}
	// 引擎在 54,000 不强平，53,400 强平
	x.market("BTC-PERP", "54000")
	x.e.Tick()
	if x.pos("BTC-PERP").IsZero() {
		t.Fatal("54,000 引擎不应强平")
	}
	x.market("BTC-PERP", "53400")
	x.e.Tick()
	if !x.pos("BTC-PERP").IsZero() {
		t.Fatal("53,400 引擎应强平")
	}
}

// 手算（逐仓空 1 BTC）：涨 10% 强平；跌的时候赚钱。
// 联动系数 2 的币：BTC 跌 5% 它跌 10%。
func TestStressShortAndBeta(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	_ = x.e.SetMode(1, "BTC-PERP", "isolated")
	x.place(OrderReq{Instrument: "BTC-PERP", Side: "sell", Type: Market, Qty: D("1")})
	r, _ := x.e.Stress(1, betaOne)
	if p := r.Positions[0]; p.LiqUp == nil || *p.LiqUp != 10 || p.LiqDown != nil || p.Side != "short" {
		t.Fatalf("空单应在涨 10%% 时强平：%+v", p)
	}
	r, _ = x.e.Stress(1, func(string) decimal.Decimal { return D("2") })
	if p := r.Positions[0]; p.LiqUp == nil || *p.LiqUp != 5 {
		t.Fatalf("联动 2 倍时涨 5%% 就强平：%+v", p)
	}
	// 没有仓位时权益不变
	y := newEnv(t)
	r, _ = y.e.Stress(1, betaOne)
	eq(t, "没有仓位", r.Equity[at(t, r, -50)], D("10000"))
}

// 预检：开仓和加仓要过；减仓、平仓、止盈止损不过预检（D07）
func TestGuardOnlyOnOpening(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	x.place(buy("BTC-PERP", "0.1"))
	calls := 0
	var seen PreOrder
	block := false
	x.e.Guard = func(p PreOrder) error {
		calls++
		seen = p
		if block {
			return NewReject("DISCIPLINE_LOCKED", "rule", "daily_loss")
		}
		return nil
	}
	x.place(OrderReq{Instrument: "BTC-PERP", Side: "buy", Type: Limit, TIF: GTC, Price: D("59000"), Qty: D("0.01"), SL: D("58000")})
	if calls != 1 || seen.Source != "manual" || !seen.Price.Equal(D("59000")) || !seen.Stop.Equal(D("58000")) || !seen.Equity.IsPositive() {
		t.Fatalf("加仓要过预检并带上价格、止损、权益：%d 次，%+v", calls, seen)
	}
	block = true
	x.reject("DISCIPLINE_LOCKED", buy("BTC-PERP", "0.01"))
	calls = 0
	// 减仓
	x.place(OrderReq{Instrument: "BTC-PERP", Side: "sell", Type: Market, Qty: D("0.01")})
	// 只减仓
	x.place(OrderReq{Instrument: "BTC-PERP", Side: "sell", Type: Market, Qty: D("0.01"), ReduceOnly: true})
	// 平仓
	if _, err := x.e.ClosePosition(1, "BTC-PERP", D("0.5"), Market, decimal.Zero); err != nil {
		t.Fatal(err)
	}
	// 止盈止损
	sl := D("50000")
	if err := x.e.SetTPSL(1, "BTC-PERP", nil, &sl, ByMark); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("减仓、平仓、止损不应过预检：%d 次", calls)
	}
	if _, err := x.e.ClosePosition(1, "BTC-PERP", D("1"), Market, decimal.Zero); err != nil {
		t.Fatal(err)
	}
	// 反手：超过持仓的部分是开新仓，要过预检
	x.e.Guard = nil
	x.place(buy("BTC-PERP", "0.1"))
	x.e.Guard = func(PreOrder) error { return NewReject("DISCIPLINE_LOCKED") }
	x.reject("DISCIPLINE_LOCKED", OrderReq{Instrument: "BTC-PERP", Side: "sell", Type: Market, Qty: D("0.2")})
}

// 条件单触发时也过预检：锁定中触发就过期，写明原因
func TestGuardOnConditional(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	o := x.place(OrderReq{Instrument: "BTC-PERP", Side: "buy", Type: CondMarket, Qty: D("0.01"), TriggerPx: D("61000")})
	x.e.Guard = func(PreOrder) error { return NewReject("DISCIPLINE_LOCKED") }
	x.market("BTC-PERP", "61000")
	x.e.Tick()
	if !x.pos("BTC-PERP").IsZero() {
		t.Fatal("锁定中条件单不应开仓")
	}
	h := x.acct().History
	if len(h) == 0 || h[len(h)-1].ID != o.ID || h[len(h)-1].Status != StExpired || h[len(h)-1].Reason != "DISCIPLINE_LOCKED" {
		t.Fatalf("条件单应过期并写明原因：%+v", h)
	}
}

// 锁定时撤开仓单：普通委托和条件单撤掉，止损和只减仓留着
func TestCancelOpening(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	x.place(buy("BTC-PERP", "0.1"))
	sl := D("50000")
	if err := x.e.SetTPSL(1, "BTC-PERP", nil, &sl, ByMark); err != nil {
		t.Fatal(err)
	}
	x.place(OrderReq{Instrument: "BTC-PERP", Side: "buy", Type: Limit, TIF: GTC, Price: D("59000"), Qty: D("0.01")})
	x.place(OrderReq{Instrument: "BTC-PERP", Side: "buy", Type: CondMarket, Qty: D("0.01"), TriggerPx: D("61000")})
	keep := x.place(OrderReq{Instrument: "BTC-PERP", Side: "sell", Type: Limit, TIF: GTC, Price: D("61000"), Qty: D("0.01"), ReduceOnly: true})
	n, err := x.e.CancelOpening(1, "DISCIPLINE")
	if err != nil || n != 2 {
		t.Fatalf("应撤 2 张，实际 %d，%v", n, err)
	}
	if _, ok := x.acct().Orders[keep.ID]; !ok || len(x.acct().Orders) != 2 {
		t.Fatalf("止损和只减仓单要留着：%+v", x.acct().Orders)
	}
}

// 提醒走事件，重放后还在
func TestNoteReplay(t *testing.T) {
	x := newEnv(t)
	if err := x.e.Note(1, "takeover", "BTC-PERP", ""); err != nil {
		t.Fatal(err)
	}
	if n := x.acct().Notices; len(n) != 1 || n[0].Kind != "takeover" || n[0].Instrument != "BTC-PERP" || n[0].Seq == 0 {
		t.Fatalf("提醒：%+v", n)
	}
}

// 手动成交按来源查：手动、平仓算；止损、AI 的不算。平仓一张委托算一笔
func TestFillsBySource(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	x.market("ETH-PERP", "3000")
	if _, err := x.e.PlaceAs(1, OrderReq{Instrument: "ETH-PERP", Side: "buy", Type: Market, Qty: D("1")}, "follow", "m1"); err != nil {
		t.Fatal(err)
	}
	got, seq := x.e.ManualFills(1, 0)
	if len(got) != 0 || seq == 0 {
		t.Fatalf("AI 的单不算手动：%v，%d", got, seq)
	}
	x.place(buy("BTC-PERP", "0.1"))
	got, seq2 := x.e.ManualFills(1, seq)
	if !got["BTC-PERP"] || len(got) != 1 || seq2 <= seq {
		t.Fatalf("手动单算：%v", got)
	}
	// 平手动仓：亏 10（0.1 × 100）+ 手续费
	x.market("BTC-PERP", "59900")
	if _, err := x.e.ClosePosition(1, "BTC-PERP", D("1"), Market, decimal.Zero); err != nil {
		t.Fatal(err)
	}
	if err := func() error { _, e := x.e.CloseAs(1, "ETH-PERP", "follow", "x"); return e }(); err != nil {
		t.Fatal(err)
	}
	list, _ := x.e.ClosedTrades(1, seq2)
	if len(list) != 1 || list[0].Instrument != "BTC-PERP" || list[0].Net.Sign() >= 0 {
		t.Fatalf("手动平仓算一笔亏损，AI 的不算：%+v", list)
	}
}

// 历史情景（18 第 10.3 条）一次性动到位，结果要和压力测试同一格一致（同样的手算样例）
func TestScenarioMatchesStress(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	x.place(buy("BTC-PERP", "1.6"))
	at10 := func(inst, side string) decimal.Decimal { return D("-0.10") }
	r, err := x.e.Scenario(1, at10)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "跌 10% 后", r.After, D("352"))
	if r.Positions[0].Liq || r.Positions[0].Side != "long" {
		t.Fatalf("跌 10%% 不强平：%+v", r.Positions[0])
	}
	r, _ = x.e.Scenario(1, func(string, string) decimal.Decimal { return D("-0.11") })
	eq(t, "跌 11% 后", r.After, D("0"))
	if !r.Positions[0].Liq {
		t.Fatal("跌 11% 全仓强平")
	}
	// 逐仓：跌 10% 爆掉，亏掉整个逐仓保证金（和 TestStressIsolatedByHand 一样：剩 3970）
	y := newEnv(t)
	y.market("BTC-PERP", "60000")
	_ = y.e.SetMode(1, "BTC-PERP", "isolated")
	y.place(buy("BTC-PERP", "1"))
	r, _ = y.e.Scenario(1, func(string, string) decimal.Decimal { return D("-0.10") })
	eq(t, "逐仓跌 10% 后", r.After, D("3970"))
	if !r.Positions[0].Liq {
		t.Fatal("逐仓跌 10% 强平")
	}
	// 方向：空单按 side 拿到自己那一边的价
	var sides []string
	_, _ = y.e.Scenario(1, func(_ string, side string) decimal.Decimal { sides = append(sides, side); return decimal.Zero })
	if len(sides) != 1 || sides[0] != "long" {
		t.Fatalf("%v", sides)
	}
}
