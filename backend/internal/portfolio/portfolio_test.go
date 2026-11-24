package portfolio

import (
	"io"
	"log/slog"
	"math"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"qs/internal/book"
	"qs/internal/trading"
)

// 04 第 5 节的执行规则逐条测（12 的 3.2）。行情和时钟都由测试控制。

const V = "binance-usdm"

func D(s string) decimal.Decimal { return decimal.RequireFromString(s) }

type env struct {
	t     *testing.T
	books *book.Store
	eng   *trading.Engine
	pf    *Portfolio
	clock time.Time
	evs   []trading.Event
}

func newEnv(t *testing.T) *env {
	x := &env{t: t, books: book.NewStore(), clock: time.Date(2026, 11, 6, 8, 0, 0, 0, time.UTC)}
	x.eng = trading.New(x.books, V, trading.NewSpecs(oldMinSpecs()), slog.New(slog.NewTextHandler(io.Discard, nil)))
	x.eng.Now = func() time.Time { return x.clock }
	x.eng.SetPersist(func(b []trading.Event) error {
		x.evs = append(x.evs, b...)
		return nil
	})
	x.market("BTC-PERP", "77000", "1")
	x.market("ETH-PERP", "2900", "10")
	x.pf = New("s1", -1, V, V, x.books, x.eng)
	x.pf.Now = func() time.Time { return x.clock }
	return x
}

// market 中间价 mid，上下各 10 档，每档 qty，档距万分之一。
func (x *env) market(inst, mid, qty string) {
	m := D(mid)
	step := m.Mul(D("0.0001"))
	var bids, asks []book.Level
	for i := int64(1); i <= 10; i++ {
		k := decimal.NewFromInt(i)
		bids = append(bids, book.Level{Px: m.Sub(step.Mul(k)), Qty: D(qty)})
		asks = append(asks, book.Level{Px: m.Add(step.Mul(k)), Qty: D(qty)})
	}
	x.books.SetBook(book.Book{Venue: V, Instrument: inst, Bids: bids, Asks: asks, Time: time.Now().UnixMilli()})
	x.books.UpdateTicker(V, inst, func(t *book.Ticker) { t.Last, t.Mark = m, m })
}

func (x *env) signal(id string, inst, w, ref, urgency string, slip int) []Execution {
	x.pf.Apply(id, x.clock.UnixMilli(), x.clock.Add(5*time.Minute).UnixMilli(), false,
		[]Target{{Instrument: inst, Weight: D(w), RefPrice: D(ref), Urgency: urgency, MaxSlipBps: slip}})
	return x.pf.Executions(id)
}

func (x *env) one(id string) Execution {
	list := x.pf.Executions(id)
	if len(list) != 1 {
		x.t.Fatalf("%s 应有一条执行回报：%+v", id, list)
	}
	return list[0]
}

func (x *env) pos(inst string) decimal.Decimal {
	b, err := x.eng.Brief(x.pf.Acct)
	if err != nil {
		x.t.Fatal(err)
	}
	return b.Pos[inst].Qty
}

func (x *env) posState(inst string) PositionState {
	for _, p := range x.pf.State().Positions {
		if p.Instrument == inst {
			return p
		}
	}
	x.t.Fatalf("State 里没有 %s", inst)
	return PositionState{}
}

func (x *env) advance(d time.Duration) {
	x.clock = x.clock.Add(d)
	x.pf.Watch()
}

func TestSystemAccountStartsAt10000(t *testing.T) {
	x := newEnv(t)
	if !x.pf.NAV().Equal(D("10000")) {
		t.Fatalf("初始净值 %s", x.pf.NAV())
	}
	v, err := x.eng.View(-1)
	if err != nil || !v.Initial.Equal(D("10000")) {
		t.Fatalf("系统账户：%+v %v", v, err)
	}
	// 再建一次不会多开或重置
	New("s1", -1, V, V, x.books, x.eng)
	if !x.pf.NAV().Equal(D("10000")) {
		t.Fatal("重复开户把账户改了")
	}
}

// 价格偏离决策价超过滑点上限：不执行
func TestPriceMovedSkips(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "77500", "1") // 偏离 65 bps
	x.signal("a", "BTC-PERP", "0.5", "77000", "immediate", 20)
	if st := x.one("a").State; st != "skipped_price_moved" {
		t.Fatalf("状态 %s", st)
	}
	if !x.pos("BTC-PERP").IsZero() {
		t.Fatal("不该有仓位")
	}
	if len(x.pf.Targets()) != 0 {
		t.Fatalf("没执行的信号不该改目标：%+v", x.pf.Targets())
	}
}

// 成交均价不超过上限，超出的剩余撤单
func TestSlippageCapCancelsRest(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "77000", "0.01") // 每档 0.01 BTC，档距 1 bps
	// 想买 0.5 × 10000 ÷ 77000 ≈ 0.064；滑点上限 2 bps 只够吃到第 2 档：0.02
	x.signal("a", "BTC-PERP", "0.5", "77000", "immediate", 2)
	ex := x.one("a")
	if ex.State != "partial" {
		t.Fatalf("状态 %s", ex.State)
	}
	if !x.pos("BTC-PERP").Equal(D("0.02")) {
		t.Fatalf("应只成交 0.02：%s", x.pos("BTC-PERP"))
	}
	limit := D("77000").Mul(D("1.0002"))
	if D(ex.AvgPx).GreaterThan(limit) {
		t.Fatalf("均价 %s 超过上限 %s", ex.AvgPx, limit)
	}
	if D(ex.SlippageBps).GreaterThan(D("2")) || D(ex.SlippageBps).Sign() <= 0 {
		t.Fatalf("滑点 %s", ex.SlippageBps)
	}
	if v, _ := x.eng.View(-1); len(v.Orders) != 0 {
		t.Fatalf("剩余应撤单：%+v", v.Orders)
	}
}

// immediate 直接吃单，手续费 0.05%
func TestImmediateTakes(t *testing.T) {
	x := newEnv(t)
	x.signal("a", "BTC-PERP", "0.5", "77000", "immediate", 20)
	ex := x.one("a")
	if ex.State != "filled" || ex.AvgPx != "77007.7" {
		t.Fatalf("%+v", ex)
	}
	want := x.pos("BTC-PERP").Mul(D("77007.7")).Mul(D("0.0005"))
	if !D(ex.Fee).Equal(want.Round(6)) {
		t.Fatalf("手续费 %s，应 %s", ex.Fee, want)
	}
	if err := x.eng.Check(); err != nil {
		t.Fatal(err)
	}
}

// passive 只挂单，成交算挂单手续费
func TestPassiveMakerFill(t *testing.T) {
	x := newEnv(t)
	x.signal("a", "BTC-PERP", "0.5", "77000", "passive", 20)
	ex := x.one("a")
	if ex.State != "pending" {
		t.Fatalf("应挂着：%+v", ex)
	}
	v, _ := x.eng.View(-1)
	if len(v.Orders) != 1 || v.Orders[0].TIF != trading.GTX || !v.Orders[0].Price.Equal(D("76992.3")) {
		t.Fatalf("应在买一挂只挂单：%+v", v.Orders)
	}
	qty := v.Orders[0].Qty
	x.books.Trade(V, "BTC-PERP", D("76990"), D("5"), false, x.clock.UnixMilli())
	x.advance(time.Second)
	ex = x.one("a")
	if ex.State != "filled" || ex.AvgPx != "76992.3" {
		t.Fatalf("%+v", ex)
	}
	want := qty.Mul(D("76992.3")).Mul(D("0.0002")).Round(6)
	if !D(ex.Fee).Equal(want) {
		t.Fatalf("挂单手续费 %s，应 %s", ex.Fee, want)
	}
	if !x.pf.Targets()[0].Weight.Equal(D("0.5")) {
		t.Fatalf("目标 %+v", x.pf.Targets())
	}
}

// passive 60 秒没成交放弃
func TestPassiveGivesUpAfter60s(t *testing.T) {
	x := newEnv(t)
	x.signal("a", "BTC-PERP", "0.5", "77000", "passive", 20)
	x.advance(59 * time.Second)
	if st := x.one("a").State; st != "pending" {
		t.Fatalf("59 秒还该挂着：%s", st)
	}
	x.advance(time.Second)
	if st := x.one("a").State; st != "expired" {
		t.Fatalf("60 秒应放弃：%s", st)
	}
	if v, _ := x.eng.View(-1); len(v.Orders) != 0 {
		t.Fatalf("挂单应撤掉：%+v", v.Orders)
	}
}

// passive 信号过期没成交也放弃
func TestPassiveGivesUpAtDeadline(t *testing.T) {
	x := newEnv(t)
	x.pf.Apply("a", x.clock.UnixMilli(), x.clock.Add(20*time.Second).UnixMilli(), false,
		[]Target{{Instrument: "BTC-PERP", Weight: D("0.5"), RefPrice: D("77000"), Urgency: "passive", MaxSlipBps: 20}})
	x.advance(19 * time.Second)
	if st := x.one("a").State; st != "pending" {
		t.Fatalf("没到期还该挂着：%s", st)
	}
	x.advance(time.Second)
	if st := x.one("a").State; st != "expired" {
		t.Fatalf("信号过期应放弃：%s", st)
	}
}

// normal 先挂单，剩余转吃单
func TestNormalRestsThenTakes(t *testing.T) {
	x := newEnv(t)
	x.signal("a", "BTC-PERP", "0.5", "77000", "normal", 20)
	if st := x.one("a").State; st != "pending" {
		t.Fatalf("应先挂着：%s", st)
	}
	x.advance(9 * time.Second)
	if st := x.one("a").State; st != "pending" {
		t.Fatalf("9 秒还该挂着：%s", st)
	}
	x.advance(time.Second)
	ex := x.one("a")
	if ex.State != "filled" || ex.AvgPx != "77007.7" {
		t.Fatalf("10 秒应转吃单：%+v", ex)
	}
}

// normal 挂单成交了一部分，剩余转吃单：均价和手续费两段合起来
func TestNormalPartialMakerThenTaker(t *testing.T) {
	x := newEnv(t)
	x.signal("a", "BTC-PERP", "0.5", "77000", "normal", 20)
	v, _ := x.eng.View(-1)
	qty := v.Orders[0].Qty
	half := D("0.03")
	x.books.Trade(V, "BTC-PERP", D("76992.2"), half, false, x.clock.UnixMilli()) // 穿过挂单价：给我们这笔成交的量
	x.advance(10 * time.Second)
	ex := x.one("a")
	if ex.State != "filled" {
		t.Fatalf("%+v", ex)
	}
	rest := qty.Sub(half)
	wantAvg := half.Mul(D("76992.3")).Add(rest.Mul(D("77007.7"))).Div(qty)
	if !D(ex.AvgPx).Sub(wantAvg).Abs().LessThan(D("0.000001")) {
		t.Fatalf("均价 %s，应 %s", ex.AvgPx, wantAvg)
	}
	wantFee := half.Mul(D("76992.3")).Mul(D("0.0002")).Add(rest.Mul(D("77007.7")).Mul(D("0.0005"))).Round(6)
	if !D(ex.Fee).Equal(wantFee) {
		t.Fatalf("手续费 %s，应 %s", ex.Fee, wantFee)
	}
}

func exitsSig(x *env, id, w, sl, tp string) {
	x.pf.Apply(id, x.clock.UnixMilli(), x.clock.Add(time.Minute).UnixMilli(), false, []Target{{Instrument: "BTC-PERP", Weight: D(w), RefPrice: D("77000"),
		Urgency: "immediate", MaxSlipBps: 20, Exits: &Exits{StopLoss: D(sl), TakeProfit: D(tp)}}})
}

// 止损止盈按标记价：最新价动了不算，标记价到了才算
func TestStopUsesMarkPrice(t *testing.T) {
	x := newEnv(t)
	exitsSig(x, "a", "0.5", "0.02", "0.03")
	x.books.UpdateTicker(V, "BTC-PERP", func(t *book.Ticker) { t.Last = D("70000") }) // 只有最新价跌
	x.advance(time.Second)
	if x.pos("BTC-PERP").IsZero() {
		t.Fatal("标记价没到，不该平仓")
	}
	x.market("BTC-PERP", "75400", "1") // 入场 77007.7，跌 2.09%
	x.advance(time.Second)
	if !x.pos("BTC-PERP").IsZero() {
		t.Fatal("标记价到了应平仓")
	}
	st := x.posState("BTC-PERP")
	if st.ClosedBy != "stop_loss" || !D(st.TargetWeight).IsZero() {
		t.Fatalf("目标应置 0 并记录 closed_by：%+v", st)
	}
	// 平仓是吃单：卖给买一
	fills, _, _, _ := x.eng.Records(-1)
	last := fills[len(fills)-1]
	if last.Maker || last.Source != "risk" || last.Tag != "stop_loss" {
		t.Fatalf("平仓成交：%+v", last)
	}
}

func TestTakeProfitAndShortSide(t *testing.T) {
	x := newEnv(t)
	exitsSig(x, "a", "-0.5", "0.02", "0.03") // 做空，入场 76992.3
	x.market("BTC-PERP", "74600", "1")       // 跌 3.1%，空头赚
	x.advance(time.Second)
	st := x.posState("BTC-PERP")
	if !x.pos("BTC-PERP").IsZero() || st.ClosedBy != "take_profit" {
		t.Fatalf("空头止盈：%+v", st)
	}
}

// 新信号的目标不带止损：旧的止损比例还在；被风控平过之后新目标清掉 closed_by
func TestNewTargetClearsClosedBy(t *testing.T) {
	x := newEnv(t)
	exitsSig(x, "a", "0.5", "0.02", "0")
	x.market("BTC-PERP", "75000", "1")
	x.advance(time.Second)
	x.clock = x.clock.Add(time.Second)
	x.signal("b", "BTC-PERP", "0.3", "75000", "immediate", 20)
	if st := x.posState("BTC-PERP"); st.ClosedBy != "" || x.pos("BTC-PERP").IsZero() {
		t.Fatalf("%+v", st)
	}
}

// 持有时间到期只标记，不平仓
func TestHorizonOnlyMarks(t *testing.T) {
	x := newEnv(t)
	x.pf.Apply("a", x.clock.UnixMilli(), x.clock.Add(time.Minute).UnixMilli(), false,
		[]Target{{Instrument: "BTC-PERP", Weight: D("0.5"), RefPrice: D("77000"), Urgency: "immediate", MaxSlipBps: 20, HorizonMin: 60}})
	if x.posState("BTC-PERP").Expired {
		t.Fatal("还没到期")
	}
	x.market("BTC-PERP", "77000", "1")
	x.advance(61 * time.Minute)
	st := x.posState("BTC-PERP")
	if !st.Expired || x.pos("BTC-PERP").IsZero() {
		t.Fatalf("到期只标记不平仓：%+v", st)
	}
}

// 单日亏损超过 5% 熔断；第二天（UTC）解除
func TestDailyBreaker(t *testing.T) {
	x := newEnv(t)
	x.advance(time.Second) // 记下当天起始净值
	x.signal("a", "BTC-PERP", "1", "77000", "immediate", 20)
	x.advance(time.Second)
	if x.pf.Breaker() {
		t.Fatal("还没亏")
	}
	x.market("BTC-PERP", "72900", "1") // 满仓跌 5.3%
	x.advance(time.Second)
	if !x.pf.Breaker() {
		t.Fatalf("应熔断，净值 %s", x.pf.NAV())
	}
	if !x.pf.IsIncrease("BTC-PERP", D("1.2")) || x.pf.IsIncrease("BTC-PERP", D("0.5")) {
		t.Fatal("加仓类判断不对：1.2 是加仓，0.5 是减仓")
	}
	x.market("BTC-PERP", "72900", "1")
	x.clock = x.clock.Add(24 * time.Hour)
	x.pf.Watch()
	if x.pf.Breaker() {
		t.Fatal("第二天应解除")
	}
}

// 差额小于 0.01 不交易
func TestMinAdjust(t *testing.T) {
	x := newEnv(t)
	x.signal("a", "BTC-PERP", "0.5", "77000", "immediate", 20)
	qty := x.pos("BTC-PERP")
	// 0.064 BTC × 77000 ÷ 约 10000 ≈ 0.4928，0.495 只差 0.0022
	x.signal("b", "BTC-PERP", "0.495", "77000", "immediate", 20)
	if st := x.one("b").State; st != "skipped_min_adjust" {
		t.Fatalf("状态 %s", st)
	}
	if !x.pos("BTC-PERP").Equal(qty) {
		t.Fatal("不该交易")
	}
	if !x.pf.Targets()[0].Weight.Equal(D("0.495")) {
		t.Fatal("目标要记下来")
	}
	// 差 0.012，够 0.01 了，但只合 0.001 BTC（77 USDT），不到 BTC 的最小下单金额 100：下不了单
	x.signal("c", "BTC-PERP", "0.505", "77000", "immediate", 20)
	if st := x.one("c").State; st != "skipped_min_adjust" {
		t.Fatalf("状态 %s", st)
	}
	if !x.pos("BTC-PERP").Equal(qty) {
		t.Fatal("不该交易")
	}
}

// 价格涨跌不自动调回比例：重发同一个目标 = 维持现状
func TestNoRebalanceOnSameTarget(t *testing.T) {
	x := newEnv(t)
	x.signal("a", "BTC-PERP", "0.5", "77000", "immediate", 20)
	qty := x.pos("BTC-PERP")
	x.market("BTC-PERP", "80000", "1")
	x.advance(time.Second)
	if !x.pos("BTC-PERP").Equal(qty) {
		t.Fatal("价格变了不该调仓")
	}
	x.signal("b", "BTC-PERP", "0.5", "80000", "immediate", 20)
	if st := x.one("b").State; st != "skipped_no_change" {
		t.Fatalf("状态 %s", st)
	}
	if !x.pos("BTC-PERP").Equal(qty) {
		t.Fatal("重发同一个目标不该调仓")
	}
}

// 第 6 条回归：挂单过期后重发同样的目标，要接着做
func TestRetryAfterExpiry(t *testing.T) {
	x := newEnv(t)
	x.signal("a", "BTC-PERP", "0.5", "77000", "passive", 20)
	x.advance(60 * time.Second)
	if st := x.one("a").State; st != "expired" {
		t.Fatalf("应过期：%s", st)
	}
	x.market("BTC-PERP", "77000", "1")
	x.signal("b", "BTC-PERP", "0.5", "77000", "immediate", 20)
	ex := x.one("b")
	if ex.State != "filled" || x.pos("BTC-PERP").IsZero() {
		t.Fatalf("重发应成交（旧代码这里永远 skipped_no_change）：%+v", ex)
	}
	// 成交之后再发一次才是维持现状
	x.signal("c", "BTC-PERP", "0.5", "77000", "immediate", 20)
	if st := x.one("c").State; st != "skipped_no_change" {
		t.Fatalf("状态 %s", st)
	}
}

// 第 6 条的另一半：挂单还在等的时候重发同样目标，不重复下单
func TestSameTargetWhileWorking(t *testing.T) {
	x := newEnv(t)
	x.signal("a", "BTC-PERP", "0.5", "77000", "passive", 20)
	x.signal("b", "BTC-PERP", "0.5", "77000", "passive", 20)
	if st := x.one("b").State; st != "skipped_no_change" {
		t.Fatalf("状态 %s", st)
	}
	if v, _ := x.eng.View(-1); len(v.Orders) != 1 {
		t.Fatalf("只该有一张挂单：%+v", v.Orders)
	}
}

// 新目标来了，旧挂单撤掉，已成交的部分算进差额
func TestNewTargetReplacesWorkingOrder(t *testing.T) {
	x := newEnv(t)
	x.signal("a", "BTC-PERP", "0.5", "77000", "passive", 20)
	x.books.Trade(V, "BTC-PERP", D("76992.2"), D("0.03"), false, x.clock.UnixMilli()) // 穿过挂单价，成交 0.03
	x.signal("b", "BTC-PERP", "0.2", "77000", "immediate", 20)
	a := x.one("a")
	if a.State != "partial" {
		t.Fatalf("旧单：%+v", a)
	}
	if v, _ := x.eng.View(-1); len(v.Orders) != 0 {
		t.Fatalf("旧挂单应撤掉：%+v", v.Orders)
	}
	got := x.pos("BTC-PERP")
	want := D("0.2").Mul(x.pf.NAV()).Div(D("77000"))
	if got.Sub(want).Abs().GreaterThan(D("0.001")) {
		t.Fatalf("仓位 %s，应接近 %s", got, want)
	}
}

// 反手：多变空，一张单走完
func TestFlip(t *testing.T) {
	x := newEnv(t)
	x.signal("a", "BTC-PERP", "0.5", "77000", "immediate", 20)
	x.signal("b", "BTC-PERP", "-0.3", "77000", "immediate", 20)
	if st := x.one("b").State; st != "filled" {
		t.Fatalf("状态 %s", st)
	}
	if x.pos("BTC-PERP").Sign() >= 0 {
		t.Fatalf("应翻成空：%s", x.pos("BTC-PERP"))
	}
	if err := x.eng.Check(); err != nil {
		t.Fatal(err)
	}
}

// partial 只动列出的品种；snapshot 没列出的品种平掉
func TestPartialAndSnapshot(t *testing.T) {
	x := newEnv(t)
	x.signal("a", "BTC-PERP", "0.5", "77000", "immediate", 20)
	x.signal("b", "ETH-PERP", "0.3", "2900", "immediate", 20)
	if x.pos("BTC-PERP").IsZero() || x.pos("ETH-PERP").IsZero() {
		t.Fatal("partial 不该动没列出的品种")
	}
	x.pf.Apply("c", x.clock.UnixMilli(), x.clock.Add(time.Minute).UnixMilli(), true,
		[]Target{{Instrument: "ETH-PERP", Weight: D("0.3"), RefPrice: D("2900"), Urgency: "immediate", MaxSlipBps: 20}})
	x.advance(10 * time.Second) // 没列出的品种按 normal 走：10 秒后转吃单
	if !x.pos("BTC-PERP").IsZero() {
		t.Fatalf("snapshot 没列出的应平掉：%s", x.pos("BTC-PERP"))
	}
	if x.pos("ETH-PERP").IsZero() {
		t.Fatal("snapshot 列出的不动")
	}
}

// flatten_all 全平；pause 不平；都只有平台方能恢复
func TestHalt(t *testing.T) {
	x := newEnv(t)
	x.signal("a", "BTC-PERP", "0.5", "77000", "immediate", 20)
	x.pf.Halt("pause", "测试")
	if h, _ := x.pf.Halted(); h != "pause" {
		t.Fatal(h)
	}
	if x.pos("BTC-PERP").IsZero() {
		t.Fatal("pause 不平仓")
	}
	x.pf.Resume()
	if h, _ := x.pf.Halted(); h != "" {
		t.Fatal(h)
	}
	x.pf.Halt("flatten_all", "测试")
	if !x.pos("BTC-PERP").IsZero() {
		t.Fatal("flatten_all 应全平")
	}
	st := x.posState("BTC-PERP")
	if st.ClosedBy != "halt" || !D(st.TargetWeight).IsZero() {
		t.Fatalf("%+v", st)
	}
	x.advance(time.Second)
	if h, _ := x.pf.Halted(); h != "flatten_all" {
		t.Fatal("不会自己恢复")
	}
}

// 风控平仓第一次没平完（行情暂停时）：下一秒接着平
func TestRiskCloseRetries(t *testing.T) {
	x := newEnv(t)
	x.signal("a", "BTC-PERP", "0.5", "77000", "immediate", 20)
	x.books.SetBook(book.Book{Venue: V, Instrument: "BTC-PERP"}) // 盘口空了
	x.pf.Halt("flatten_all", "测试")
	if x.pos("BTC-PERP").IsZero() {
		t.Fatal("盘口空了平不掉")
	}
	x.market("BTC-PERP", "77000", "1")
	x.advance(time.Second)
	if !x.pos("BTC-PERP").IsZero() {
		t.Fatal("盘口回来后应接着平")
	}
}

// 第 5 条回归：资金费按结算前的费率算，不是新一期的
func TestFundingUsesOldRate(t *testing.T) {
	x := newEnv(t)
	x.signal("a", "BTC-PERP", "0.5", "77000", "immediate", 20)
	qty := x.pos("BTC-PERP")
	next := x.clock.Add(time.Minute).UnixMilli()
	x.books.UpdateTicker(V, "BTC-PERP", func(t *book.Ticker) { t.FundingRate, t.NextFunding = D("0.0001"), next })
	x.eng.Tick()
	before, _ := x.eng.Brief(-1)
	x.clock = x.clock.Add(time.Minute)
	x.books.UpdateTicker(V, "BTC-PERP", func(t *book.Ticker) { t.FundingRate, t.NextFunding = D("0.003"), next+8*3600*1000 })
	x.eng.Tick()
	after, _ := x.eng.Brief(-1)
	got := before.Wallet.Sub(after.Wallet)
	want := qty.Mul(D("77000")).Mul(D("0.0001"))
	if !got.Equal(want) {
		t.Fatalf("资金费 %s，应 %s（旧代码按 0.3%% 收）", got, want)
	}
	if err := x.eng.Check(); err != nil {
		t.Fatal(err)
	}
}

// 重启：净值、持仓、目标不变；没做完的挂单撤掉，标"中断"
func TestRestartKeepsEverything(t *testing.T) {
	x := newEnv(t)
	exitsSig(x, "a", "0.5", "0.02", "0.03")
	x.signal("b", "ETH-PERP", "0.3", "2900", "passive", 20)
	x.advance(time.Second)
	before := x.pf.State()
	var snap *Snap
	x.pf.mu.Lock()
	s := x.pf.snapLocked()
	x.pf.mu.Unlock()
	snap = &s
	execs := map[string][]Execution{"a": x.pf.Executions("a"), "b": x.pf.Executions("b")}
	nav := x.pf.History()
	x.eng.Flush()

	eng2 := trading.New(x.books, V, trading.NewSpecs(oldMinSpecs()), slog.New(slog.NewTextHandler(io.Discard, nil)))
	eng2.Now = func() time.Time { return x.clock }
	if err := eng2.Restore(x.evs); err != nil {
		t.Fatal(err)
	}
	p2 := New("s1", -1, V, V, x.books, eng2)
	p2.Now = func() time.Time { return x.clock }
	p2.Restore(snap, execs, nav)

	after := p2.State()
	if after.NAV != before.NAV || after.Cash != before.Cash {
		t.Fatalf("净值变了：%+v → %+v", before, after)
	}
	if len(after.Positions) != len(before.Positions) {
		t.Fatalf("持仓变了：%+v → %+v", before.Positions, after.Positions)
	}
	for i := range after.Positions {
		a, b := after.Positions[i], before.Positions[i]
		if a.Qty != b.Qty || a.EntryPrice != b.EntryPrice || a.TargetWeight != b.TargetWeight {
			t.Fatalf("%+v → %+v", b, a)
		}
	}
	if v, _ := eng2.View(-1); len(v.Orders) != 0 {
		t.Fatalf("挂单应撤掉：%+v", v.Orders)
	}
	if st := p2.Executions("b")[0].State; st != "interrupted" {
		t.Fatalf("状态 %s", st)
	}
	// 同样的目标再发一次，会接着做
	x.market("ETH-PERP", "2900", "10")
	p2.Apply("c", x.clock.UnixMilli(), x.clock.Add(time.Minute).UnixMilli(), false,
		[]Target{{Instrument: "ETH-PERP", Weight: D("0.3"), RefPrice: D("2900"), Urgency: "immediate", MaxSlipBps: 20}})
	if st := p2.Executions("c")[0].State; st != "filled" {
		t.Fatalf("状态 %s", st)
	}
	// 止损比例也还在
	x.market("BTC-PERP", "75000", "1")
	x.clock = x.clock.Add(time.Second)
	p2.Watch()
	for _, p := range p2.State().Positions {
		if p.Instrument == "BTC-PERP" && p.ClosedBy != "stop_loss" {
			t.Fatalf("重启后止损应照常：%+v", p)
		}
	}
	if err := eng2.Check(); err != nil {
		t.Fatal(err)
	}
}

// ---------- 战绩（3.4） ----------

// 手算样例：见每行注释。
func TestBuildTradesByHand(t *testing.T) {
	fills := []trading.Fill{
		{Seq: 1, Time: 1000, Instrument: "BTC-PERP", Side: "buy", Qty: D("1"), Px: D("100"), Fee: D("0.05"), Tag: "m1", Source: "ai"},
		{Seq: 3, Time: 3000, Instrument: "BTC-PERP", Side: "buy", Qty: D("1"), Px: D("110"), Fee: D("0.055"), Tag: "m2", Source: "ai"}, // 均价 105
		{Seq: 4, Time: 4000, Instrument: "BTC-PERP", Side: "sell", Qty: D("1"), Px: D("120"), Fee: D("0.06"), Realized: D("15"), Tag: "m3", Source: "ai"},
		{Seq: 5, Time: 5000, Instrument: "BTC-PERP", Side: "sell", Qty: D("2"), Px: D("90"), Fee: D("0.09"), Realized: D("-15"), Tag: "m4", Source: "ai"}, // 平 1（亏 15），开空 1；手续费各一半
		{Seq: 6, Time: 6000, Instrument: "BTC-PERP", Side: "buy", Qty: D("1"), Px: D("80"), Fee: D("0.04"), Realized: D("10"), Tag: "stop_loss", Source: "risk"},
		{Seq: 7, Time: 7000, Instrument: "ETH-PERP", Side: "buy", Qty: D("3"), Px: D("10"), Fee: D("0.015"), Tag: "m5", Source: "ai"}, // 还没平
	}
	fundings := []trading.FundingRec{
		{Seq: 2, Time: 2000, Instrument: "BTC-PERP", Amount: D("-0.1")},
		{Seq: 8, Time: 8000, Instrument: "ETH-PERP", Amount: D("0.02")},
	}
	closed, open := BuildTrades(fills, fundings)
	if len(closed) != 2 || len(open) != 1 {
		t.Fatalf("%+v %+v", closed, open)
	}
	a := closed[0]
	// 第一笔：多，开 2（均价 105），平 2（均价 (120+90)/2=105），毛利 15−15=0，
	// 手续费 0.05+0.055+0.06+0.045=0.21，资金费 −0.1，净 −0.31
	if a.Side != "long" || !a.Qty.Equal(D("2")) || !a.Entry.Equal(D("105")) || !a.Exit.Equal(D("105")) || !a.Gross.IsZero() ||
		!a.Fees.Equal(D("0.21")) || !a.Funding.Equal(D("-0.1")) || !a.Net.Equal(D("-0.31")) || a.OpenTime != 1000 || a.CloseTime != 5000 {
		t.Fatalf("第一笔：%+v", a)
	}
	if len(a.Signals) != 4 {
		t.Fatalf("参与的信号：%v", a.Signals)
	}
	b := closed[1]
	// 第二笔：空，开 1 @90，平 1 @80，毛利 10，手续费 0.045+0.04=0.085，净 9.915；风控平的
	if b.Side != "short" || !b.Qty.Equal(D("1")) || !b.Entry.Equal(D("90")) || !b.Exit.Equal(D("80")) || !b.Gross.Equal(D("10")) ||
		!b.Fees.Equal(D("0.085")) || !b.Net.Equal(D("9.915")) || b.ClosedBy != "stop_loss" {
		t.Fatalf("第二笔：%+v", b)
	}
	o := open[0]
	if o.CloseTime != 0 || !o.Funding.Equal(D("0.02")) || !o.Net.Equal(D("0.005")) {
		t.Fatalf("没平的：%+v", o)
	}
	// 所有净收益加起来 = 钱包变化：平仓盈亏 10，手续费 0.31，资金费 −0.08 → 9.61
	total := a.Net.Add(b.Net).Add(o.Net)
	if !total.Equal(D("9.61")) {
		t.Fatalf("合计 %s", total)
	}
}

func day(d int, hour int) int64 {
	return time.Date(2026, 1, d, hour, 0, 0, 0, time.UTC).UnixMilli()
}

// 手算样例：初始 10000；第 1 天收 10100；第 2 天盘中 9900、收 10000；第 3 天收 10200。
//   - 收益 = 10200/10000 − 1 = 2%
//   - 回撤 = (10100−9900)/10100 = 1.980198%
//   - 日收益 = 1%、−0.990099%、2%；平均 0.669967%；样本标准差 1.522127%
//   - 夏普 = 0.669967/1.522127 × √365 = 8.409
func TestStatsByHand(t *testing.T) {
	pts := []NAVPoint{{day(1, 12), 10100}, {day(2, 6), 9900}, {day(2, 20), 10000}, {day(3, 12), 10200}}
	closed := []Trade{{Net: D("5")}, {Net: D("-1")}, {Net: D("0")}, {Net: D("2")}}
	s := ComputeStats(10000, 10200, pts, closed)
	near := func(name string, got, want float64) {
		t.Helper()
		if math.Abs(got-want) > 0.0005 {
			t.Fatalf("%s = %v，应 %v", name, got, want)
		}
	}
	near("收益", s.Return, 0.02)
	near("回撤", s.MaxDrawdown, 200.0/10100)
	if s.Sharpe == nil {
		t.Fatal("应有夏普")
	}
	mean := (0.01 + (10000.0/10100 - 1) + 0.02) / 3
	v := 0.0
	for _, r := range []float64{0.01, 10000.0/10100 - 1, 0.02} {
		v += (r - mean) * (r - mean)
	}
	near("夏普", *s.Sharpe, mean/math.Sqrt(v/2)*math.Sqrt(365))
	near("夏普（手算）", *s.Sharpe, 8.409)
	if s.Trades != 4 || s.Wins != 2 || s.WinRate == nil || *s.WinRate != 0.5 || s.Days != 3 {
		t.Fatalf("%+v", s)
	}
	// 不够两个日收益、没有交易：不写假数字
	s = ComputeStats(10000, 10000, []NAVPoint{{day(1, 1), 10000}}, nil)
	if s.Sharpe != nil || s.WinRate != nil {
		t.Fatalf("样本不够时应为空：%+v", s)
	}
}

// 战绩和账本对得上
func TestPerformanceMatchesLedger(t *testing.T) {
	x := newEnv(t)
	x.advance(time.Second)
	x.signal("a", "BTC-PERP", "0.5", "77000", "immediate", 20)
	x.market("BTC-PERP", "78000", "1")
	x.clock = x.clock.Add(time.Minute)
	x.signal("b", "BTC-PERP", "0", "78000", "immediate", 20)
	x.signal("c", "ETH-PERP", "0.3", "2900", "immediate", 20)
	x.advance(2 * time.Minute)
	perf := x.pf.Performance()
	if perf.Trades != 1 || perf.Wins != 1 || len(perf.Closed) != 1 {
		t.Fatalf("%+v", perf)
	}
	tr := perf.Closed[0]
	b, _ := x.eng.Brief(-1)
	fills, fundings, _, _ := x.eng.Records(-1)
	closed, open := BuildTrades(fills, fundings)
	sum := decimal.Zero
	for _, tt := range append(closed, open...) {
		sum = sum.Add(tt.Net)
	}
	if !sum.Equal(b.Wallet.Sub(b.Initial)) {
		t.Fatalf("净收益合计 %s ≠ 钱包变化 %s", sum, b.Wallet.Sub(b.Initial))
	}
	qty := tr.Qty
	want := D("77992.2").Sub(D("77007.7")).Mul(qty) // 卖给买一 78000×(1−0.0001)，买在 77007.7
	if !tr.Gross.Equal(want) {
		t.Fatalf("毛利 %s，应 %s", tr.Gross, want)
	}
	wantFee := qty.Mul(D("77007.7")).Add(qty.Mul(D("77992.2"))).Mul(D("0.0005"))
	if tr.Fees.Sub(wantFee).Abs().GreaterThan(D("0.00000002")) {
		t.Fatalf("手续费 %s，应 %s", tr.Fees, wantFee)
	}
	if len(perf.Curve) == 0 || perf.Since == 0 {
		t.Fatalf("净值曲线：%+v", perf.Curve)
	}
	if err := x.eng.Check(); err != nil {
		t.Fatal(err)
	}
}

// 目标变化通知跟随层
func TestOnChangeFires(t *testing.T) {
	x := newEnv(t)
	var got []Change
	x.pf.OnChange = func(c []Change) { got = append(got, c...) }
	x.signal("a", "BTC-PERP", "0.5", "77000", "immediate", 20)
	x.signal("b", "BTC-PERP", "0.5", "77000", "immediate", 20) // 没变
	x.market("BTC-PERP", "78000", "1")
	x.signal("c", "BTC-PERP", "0.1", "77000", "immediate", 20) // 价格偏离，没执行
	if len(got) != 1 || got[0].MsgID != "a" || !got[0].Weight.Equal(D("0.5")) {
		t.Fatalf("%+v", got)
	}
	x.pf.Halt("flatten_all", "测试")
	if len(got) != 2 || got[1].ClosedBy != "halt" || !got[1].Weight.IsZero() {
		t.Fatalf("%+v", got)
	}
}

// oldMinSpecs 这些测试按 BTC 最小下单金额 100 USDT 写了"差额够 1% 但不够交易所最小金额"的情形；
// 币安现在是 50（和 1% 的门槛一样大，这种情形就碰不到了），所以这里把 BTC 的最小金额固定成 100 测这条规则本身。
// 价格带用宽的 5%（这些测试的价格按 5% 写的）。
func oldMinSpecs() map[string]*trading.Spec {
	m := trading.LooseSpecs()
	m["BTC-PERP"].MinNotional = decimal.RequireFromString("100")
	return m
}

// 11-16 同一个策略同时跟到模拟账户和币安模拟账户对比时发现：
// ① 断线重连那一下盘口短暂交叉，挂"买一"的只挂单反而吃到卖盘被拒，passive 这一笔就放弃了（币安上同一笔挂得上）；
// ② 行情过期（MARKET_STALE）一被拒就放弃，交易所账户照常成交。现在：只挂单不越过对面最优价；行情过期时每秒再试，回来了就做。
func TestPaperLikeExchange(t *testing.T) {
	x := newEnv(t)
	// ① 交叉的盘口：买一 77010 比卖一 77000 还高
	x.books.SetBook(book.Book{Venue: V, Instrument: "BTC-PERP", Bids: []book.Level{{Px: D("77010"), Qty: D("1")}}, Asks: []book.Level{{Px: D("77000"), Qty: D("1")}}, Time: time.Now().UnixMilli()})
	px, ok := x.eng.RestPrice("BTC-PERP", true, D("77100"))
	if !ok || !px.LessThan(D("77000")) {
		t.Fatalf("交叉盘口里买单挂单价要在卖一下面：%s %v", px, ok)
	}
	if px, _ := x.eng.RestPrice("BTC-PERP", false, D("76900")); !px.GreaterThan(D("77010")) {
		t.Fatalf("卖单挂单价要在买一上面：%s", px)
	}
	x.market("BTC-PERP", "77000", "1")

	// ② 行情过期：先不成交也不放弃，行情回来以后成交
	b, _ := x.books.Book(V, "BTC-PERP")
	x.books.SetBookAt(b, time.Now().Add(-trading.StaleAfter-2*time.Second).UnixMilli())
	x.signal("stale", "BTC-PERP", "0.1", "77000", "immediate", 20)
	if e := x.one("stale"); e.State != "pending" {
		t.Fatalf("行情过期时先等着：%+v", e)
	}
	x.clock = x.clock.Add(2 * time.Second)
	x.pf.Watch()
	if e := x.one("stale"); e.State != "pending" {
		t.Fatalf("行情还没回来：继续等 %+v", e)
	}
	x.market("BTC-PERP", "77000", "1") // 行情回来
	x.clock = x.clock.Add(2 * time.Second)
	x.pf.Watch()
	if e := x.one("stale"); e.State != "filled" || x.pos("BTC-PERP").IsZero() {
		t.Fatalf("行情回来以后成交：%+v", e)
	}
	// 一直不回来：过了 60 秒按原来的拒绝结束
	b, _ = x.books.Book(V, "BTC-PERP")
	x.books.SetBookAt(b, time.Now().Add(-trading.StaleAfter-2*time.Second).UnixMilli())
	x.signal("gone", "BTC-PERP", "0", "77000", "immediate", 20)
	x.clock = x.clock.Add(61 * time.Second)
	x.pf.Watch()
	if e := x.one("gone"); e.State == "pending" {
		t.Fatalf("等了 60 秒还没回来：结束 %+v", e)
	}
}
