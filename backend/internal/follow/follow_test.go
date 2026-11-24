package follow

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"qs/internal/auth"
	"qs/internal/book"
	"qs/internal/portfolio"
	"qs/internal/trading"
)

// 跟随层的规则逐条测（12 的 3.5–3.10）。行情和时钟都由测试控制。

const V = "binance-usdm"

func D(s string) decimal.Decimal { return decimal.RequireFromString(s) }

type fakeAccounts struct {
	eng    *trading.Engine
	owned  map[int64]map[int64]string // user → acct → name
	next   int64
	failAt int
}

func (a *fakeAccounts) OwnsPaper(_ context.Context, user, acct int64) bool {
	_, ok := a.owned[user][acct]
	return ok
}

func (a *fakeAccounts) PaperAccounts(_ context.Context, user int64) ([]auth.PaperAccount, error) {
	var out []auth.PaperAccount
	for id, name := range a.owned[user] {
		out = append(out, auth.PaperAccount{ID: id, Name: name})
	}
	return out, nil
}

func (a *fakeAccounts) NewPaper(_ context.Context, u *auth.User, name string) (*auth.PaperAccount, error) {
	if len(a.owned[u.ID]) >= 10 {
		return nil, auth.ErrTooManyAccounts
	}
	return a.add(u.ID, name), nil
}

func (a *fakeAccounts) add(user int64, name string) *auth.PaperAccount {
	a.next++
	if a.owned[user] == nil {
		a.owned[user] = map[int64]string{}
	}
	a.owned[user][a.next] = name
	a.eng.Open(a.next, D("10000"))
	return &auth.PaperAccount{ID: a.next, Name: name}
}

type env struct {
	t     *testing.T
	books *book.Store
	eng   *trading.Engine
	pf    *portfolio.Portfolio
	m     *Manager
	acc   *fakeAccounts
	clock time.Time
	evs   []Event
	tevs  []trading.Event
	u     *auth.User
	u2    *auth.User
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newEnv(t *testing.T) *env {
	x := &env{t: t, books: book.NewStore(), clock: time.Date(2026, 11, 6, 8, 0, 0, 0, time.UTC), u: &auth.User{ID: 1, Lang: "zh"}, u2: &auth.User{ID: 2, Lang: "zh"}}
	x.eng = trading.New(x.books, V, trading.NewSpecs(oldMinSpecs()), quiet())
	x.eng.Now = func() time.Time { return x.clock }
	x.eng.SetPersist(func(b []trading.Event) error {
		x.tevs = append(x.tevs, b...)
		return nil
	})
	x.market("BTC-PERP", "77000", "5")
	x.market("ETH-PERP", "2900", "100")
	x.market("SOL-PERP", "180", "1000")
	x.pf = portfolio.New("s1", -1, V, V, x.books, x.eng)
	x.pf.Now = func() time.Time { return x.clock }
	x.acc = &fakeAccounts{eng: x.eng, owned: map[int64]map[int64]string{}}
	x.m = New(x.eng, x.acc, func(id string) *portfolio.Portfolio {
		if id == "s1" {
			return x.pf
		}
		return nil
	}, quiet())
	x.m.Now = func() time.Time { return x.clock }
	x.m.Persist = func(b []Event) error {
		x.evs = append(x.evs, b...)
		return nil
	}
	x.pf.OnChange = func(c []portfolio.Change) { x.m.Process("s1", c) }
	return x
}

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

func (x *env) signal(id, inst, w, ref string) {
	x.signalU(id, inst, w, ref, "immediate", nil)
}

func (x *env) signalU(id, inst, w, ref, urgency string, exits *portfolio.Exits) {
	x.clock = x.clock.Add(time.Second)
	x.pf.Apply(id, x.clock.UnixMilli(), x.clock.Add(time.Minute).UnixMilli(), false,
		[]portfolio.Target{{Instrument: inst, Weight: D(w), RefPrice: D(ref), Urgency: urgency, MaxSlipBps: 20, Exits: exits}})
}

func (x *env) start(r StartReq) *View {
	x.t.Helper()
	r.Strategy, r.Confirm = "s1", true
	v, err := x.m.Start(context.Background(), x.u, r)
	if err != nil {
		x.t.Fatalf("开启跟随：%v", err)
	}
	return v
}

func (x *env) pos(acct int64, inst string) decimal.Decimal {
	b, err := x.eng.Brief(acct)
	if err != nil {
		x.t.Fatal(err)
	}
	return b.Pos[inst].Qty
}

func (x *env) lastExec(id int64) ExecView {
	x.t.Helper()
	v, err := x.m.Get(x.u, id)
	if err != nil || len(v.Records) == 0 {
		x.t.Fatalf("没有执行记录：%v", err)
	}
	return v.Records[0]
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	e, ok := err.(*Err)
	if !ok || e.Code != code {
		t.Fatalf("应拒绝 %s：%v", code, err)
	}
}

// 3.5 开启跟随
func TestStartDefaultsAndAutoAccount(t *testing.T) {
	x := newEnv(t)
	v := x.start(StartReq{Alloc: D("5000")})
	if v.Params.MaxLeverage != 3 || !v.Params.MaxLoss.Equal(D("0.2")) || !v.Params.PerInst.Equal(D("1")) || v.Status != StActive {
		t.Fatalf("默认值：%+v", v.Params)
	}
	if x.acc.owned[1][v.Account] != "策略跟随" {
		t.Fatalf("应自动建 策略跟随 账户：%v", x.acc.owned[1])
	}
	b, _ := x.eng.Brief(v.Account)
	if !b.Initial.Equal(D("10000")) {
		t.Fatalf("初始 %s", b.Initial)
	}
	if !v.StopAt.Equal(D("4000")) {
		t.Fatalf("5000 亏 20%% 停在 4000：%s", v.StopAt)
	}
}

func TestStartNeedsConfirm(t *testing.T) {
	x := newEnv(t)
	_, err := x.m.Start(context.Background(), x.u, StartReq{Strategy: "s1", Alloc: D("1000")})
	wantCode(t, err, "CONFIRM_REQUIRED")
	if len(x.m.List(x.u)) != 0 || len(x.acc.owned[1]) != 0 {
		t.Fatal("没确认不该开启，也不该建账户")
	}
}

func TestOneStrategyPerAccount(t *testing.T) {
	x := newEnv(t)
	v := x.start(StartReq{Alloc: D("1000")})
	_, err := x.m.Start(context.Background(), x.u, StartReq{Strategy: "s1", Account: v.Account, Alloc: D("1000"), Confirm: true})
	wantCode(t, err, "ACCOUNT_FOLLOWING")
	// 没指定账户时同样说明原因，不是偷偷再建一个
	_, err = x.m.Start(context.Background(), x.u, StartReq{Strategy: "s1", Alloc: D("1000"), Confirm: true})
	wantCode(t, err, "ACCOUNT_FOLLOWING")
	if _, err := x.m.Stop(x.u, v.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := x.m.Start(context.Background(), x.u, StartReq{Strategy: "s1", Account: v.Account, Alloc: D("1000"), Confirm: true}); err != nil {
		t.Fatalf("停了之后可以再开：%v", err)
	}
}

func TestAllocLimits(t *testing.T) {
	x := newEnv(t)
	_, err := x.m.Start(context.Background(), x.u, StartReq{Strategy: "s1", Alloc: D("10001"), Confirm: true})
	wantCode(t, err, "ALLOC_OVER_AVAILABLE")
	_, err = x.m.Start(context.Background(), x.u, StartReq{Strategy: "s1", Alloc: D("50"), Confirm: true})
	wantCode(t, err, "ALLOC_TOO_SMALL")
	_, err = x.m.Start(context.Background(), x.u, StartReq{Strategy: "nope", Alloc: D("1000"), Confirm: true})
	wantCode(t, err, "NO_STRATEGY")
}

// 11-15 用户："我们不是无限的吗"：模拟资金不限额（D103）。接上 TopUp 后，分配的比模拟账户可用的多，差的部分自动加上，
// 记成加钱（本金跟着变，不算赚的）；以后调大分配、多出来的比可用的多时也一样。
func TestAllocTopsUpPaper(t *testing.T) {
	x := newEnv(t)
	x.m.TopUp = x.eng.Fund
	v, err := x.m.Start(context.Background(), x.u, StartReq{Strategy: "s1", Alloc: D("25000"), Confirm: true})
	if err != nil {
		t.Fatalf("分配 25000（账户只有 10000）：%v", err)
	}
	b, _ := x.eng.Brief(v.Account)
	if !b.Equity.Equal(D("25000")) || !b.Initial.Equal(D("25000")) {
		t.Fatalf("补完后权益和本金都是 25000：%s %s", b.Equity, b.Initial)
	}
	// 调大 50000（多出来的比账户可用的多）：差的部分补上
	if _, err := x.m.Update(x.u, v.ID, StartReq{Alloc: D("75000"), MaxLoss: D("0.2"), MaxLeverage: 3, PerInst: D("1")}); err != nil {
		t.Fatalf("调大到 75000：%v", err)
	}
	if b, _ := x.eng.Brief(v.Account); !b.Initial.GreaterThan(D("25000")) || b.Available.LessThan(D("50000")) {
		t.Fatalf("调大后补了钱：本金 %s 可用 %s", b.Initial, b.Available)
	}
}

func TestTooManyAccounts(t *testing.T) {
	x := newEnv(t)
	for i := 0; i < 10; i++ {
		x.acc.add(1, "别的账户")
	}
	_, err := x.m.Start(context.Background(), x.u, StartReq{Strategy: "s1", Alloc: D("1000"), Confirm: true})
	wantCode(t, err, "TOO_MANY_ACCOUNTS")
}

// 3.6 换算，手算样例：分配 5000，BTC 权重 0.5，价格 77000
func TestConversionByHand(t *testing.T) {
	x := newEnv(t)
	v := x.start(StartReq{Alloc: D("5000")})
	// 目标名义 2500 ÷ 77000 = 0.03246…，按 0.001 取整 = 0.032
	x.signal("a", "BTC-PERP", "0.5", "77000")
	if q := x.pos(v.Account, "BTC-PERP"); !q.Equal(D("0.032")) {
		t.Fatalf("多 %s", q)
	}
	// 反手：−0.3 → 名义 −1500 ÷ 77000 = −0.01948；差额 −0.05148 取整 −0.051；0.032−0.051 = −0.019
	x.signal("b", "BTC-PERP", "-0.3", "77000")
	if q := x.pos(v.Account, "BTC-PERP"); !q.Equal(D("-0.019")) {
		t.Fatalf("空 %s", q)
	}
	// 差额小于 0.01（分配金额的 1% = 50 USDT）不动：−0.305 差 25 USDT
	x.signal("c", "BTC-PERP", "-0.305", "77000")
	if st := x.lastExec(v.ID).State; st != "skipped_min_adjust" {
		t.Fatalf("状态 %s", st)
	}
	// 够 0.01 但比交易所最小下单金额（BTC 100 USDT）小：下不了单
	x.signal("d", "BTC-PERP", "-0.32", "77000")
	if st := x.lastExec(v.ID).State; st != "skipped_min_adjust" {
		t.Fatalf("状态 %s", st)
	}
	// 权重 0 全平，一点不留
	x.signal("e", "BTC-PERP", "0", "77000")
	if q := x.pos(v.Account, "BTC-PERP"); !q.IsZero() {
		t.Fatalf("应全平：%s", q)
	}
	if err := x.eng.Check(); err != nil {
		t.Fatal(err)
	}
}

// 3.6 总仓位到 2 倍时，逐仓 3 倍的保证金也够
func TestGross2xMarginEnough(t *testing.T) {
	x := newEnv(t)
	v := x.start(StartReq{Alloc: D("10000")}) // 把账户全部分配
	x.signal("a", "BTC-PERP", "1", "77000")
	x.signal("b", "ETH-PERP", "1", "2900")
	if st := x.lastExec(v.ID).State; st != "filled" {
		t.Fatalf("状态 %+v", x.lastExec(v.ID))
	}
	if x.pos(v.Account, "BTC-PERP").IsZero() || x.pos(v.Account, "ETH-PERP").IsZero() {
		t.Fatal("两个都该开")
	}
	view, _ := x.eng.View(v.Account)
	if view.Positions[0].Mode != "isolated" || view.Positions[0].Leverage != 3 {
		t.Fatalf("应逐仓 3 倍：%+v", view.Positions[0])
	}
	if view.Available.Sign() <= 0 {
		t.Fatalf("保证金应够：可用 %s", view.Available)
	}
}

// 3.7 来源标跟随，手续费和手动一样，记录跟随滑点
func TestExecutionRecords(t *testing.T) {
	x := newEnv(t)
	v := x.start(StartReq{Alloc: D("5000")})
	x.signal("a", "BTC-PERP", "0.5", "77000")
	fills, _, _, _ := x.eng.Records(v.Account)
	if len(fills) != 1 || fills[0].Source != "follow" || fills[0].Tag != "a" {
		t.Fatalf("成交：%+v", fills)
	}
	if !fills[0].Fee.Equal(D("0.032").Mul(D("77007.7")).Mul(D("0.0005")).Round(8)) {
		t.Fatalf("手续费 %s", fills[0].Fee)
	}
	e := x.lastExec(v.ID)
	if e.State != "filled" || !e.ModelAvg.Equal(D("77007.7")) || !e.AvgPx.Equal(D("77007.7")) || !e.SlipBps.IsZero() {
		t.Fatalf("记录：%+v", e)
	}
	// 滑点手算：买在 77085，模型 77007.7：+10.04 bps；卖就反过来
	if s := Slippage("buy", D("77085"), D("77007.7")); !s.Equal(D("10.04")) {
		t.Fatalf("滑点 %s", s)
	}
	if s := Slippage("sell", D("77085"), D("77007.7")); !s.Equal(D("-10.04")) {
		t.Fatalf("滑点 %s", s)
	}
}

// 3.7 多个用户随机排序（D15）
func TestRandomOrder(t *testing.T) {
	x := newEnv(t)
	for i := int64(1); i <= 5; i++ {
		u := &auth.User{ID: 100 + i, Lang: "zh"}
		if _, err := x.m.Start(context.Background(), u, StartReq{Strategy: "s1", Alloc: D("1000"), Confirm: true}); err != nil {
			t.Fatal(err)
		}
	}
	called := 0
	// 倒过来排：看得出确实按 Shuffle 的结果执行
	x.m.Shuffle = func(n int, swap func(i, j int)) {
		called = n
		for i := 0; i < n/2; i++ {
			swap(i, n-1-i)
		}
	}
	x.signal("a", "BTC-PERP", "0.5", "77000")
	if called != 5 {
		t.Fatalf("Shuffle 收到 %d 个用户", called)
	}
	first, last := int64(0), int64(0)
	for _, ev := range x.pendingExecs() {
		if first == 0 {
			first = ev.Follow
		}
		last = ev.Follow
	}
	if first <= last {
		t.Fatalf("应按打乱后的顺序执行：先 %d 后 %d", first, last)
	}
}

func (x *env) pendingExecs() []Event {
	x.m.Flush()
	var out []Event
	for _, ev := range x.evs {
		if ev.Kind == "exec" {
			out = append(out, ev)
		}
	}
	return out
}

// 3.7 过期信号作废；价格偏离就跳过，并记录原因
func TestExpiredAndMoved(t *testing.T) {
	x := newEnv(t)
	v := x.start(StartReq{Alloc: D("5000")})
	x.m.Process("s1", []portfolio.Change{{MsgID: "old", Instrument: "BTC-PERP", Weight: D("0.5"), RefPrice: D("77000"), Urgency: "immediate", MaxSlipBps: 20, Deadline: x.clock.Add(-time.Second).UnixMilli()}})
	if st := x.lastExec(v.ID).State; st != "skipped_expired" {
		t.Fatalf("状态 %s", st)
	}
	x.m.Process("s1", []portfolio.Change{{MsgID: "moved", Instrument: "BTC-PERP", Weight: D("0.5"), RefPrice: D("76000"), Urgency: "immediate", MaxSlipBps: 20, Deadline: x.clock.Add(time.Minute).UnixMilli()}})
	if st := x.lastExec(v.ID).State; st != "skipped_price_moved" {
		t.Fatalf("状态 %s", st)
	}
	if !x.pos(v.Account, "BTC-PERP").IsZero() {
		t.Fatal("都不该成交")
	}
}

// 3.8 亏到最大亏损：自动平仓并停止
func TestMaxLossStops(t *testing.T) {
	x := newEnv(t)
	v := x.start(StartReq{Alloc: D("5000"), MaxLoss: D("0.1")})
	x.signal("a", "BTC-PERP", "1", "77000")
	x.m.Tick()
	if s, _ := x.m.Get(x.u, v.ID); s.Status != StActive {
		t.Fatalf("还没亏够：%+v", s.Status)
	}
	x.market("BTC-PERP", "68000", "5") // 跌 11.7%，亏超过 500
	x.m.Tick()
	s, _ := x.m.Get(x.u, v.ID)
	if s.Status != StStopped || s.Reason != ReasonMaxLoss {
		t.Fatalf("应自动停止：%s %s", s.Status, s.Reason)
	}
	if !x.pos(v.Account, "BTC-PERP").IsZero() {
		t.Fatal("应平仓")
	}
	if x.m.IsFollowing(1) {
		t.Fatal("停了就不算开启服务")
	}
	// 之后的信号不再执行
	x.market("BTC-PERP", "68000", "5")
	x.signal("b", "BTC-PERP", "0.5", "68000")
	if !x.pos(v.Account, "BTC-PERP").IsZero() {
		t.Fatal("停了不该再下单")
	}
}

// 3.8 盘口空了平不掉：下一秒接着平，平完才算停好
func TestMaxLossRetriesClose(t *testing.T) {
	x := newEnv(t)
	v := x.start(StartReq{Alloc: D("5000"), MaxLoss: D("0.1")})
	x.signal("a", "BTC-PERP", "1", "77000")
	x.books.UpdateTicker(V, "BTC-PERP", func(t *book.Ticker) { t.Last, t.Mark = D("68000"), D("68000") })
	x.books.SetBook(book.Book{Venue: V, Instrument: "BTC-PERP"})
	x.m.Tick()
	if s, _ := x.m.Get(x.u, v.ID); s.Status != StClosing {
		t.Fatalf("应在平仓中：%s", s.Status)
	}
	x.market("BTC-PERP", "68000", "5")
	x.m.Tick()
	if s, _ := x.m.Get(x.u, v.ID); s.Status != StStopped {
		t.Fatalf("应平完：%s", s.Status)
	}
	if !x.pos(v.Account, "BTC-PERP").IsZero() {
		t.Fatal("应平仓")
	}
}

// 3.8 单品种超限：缩到上限并记录
func TestPerInstCap(t *testing.T) {
	x := newEnv(t)
	v := x.start(StartReq{Alloc: D("5000"), PerInst: D("0.2")})
	x.signal("a", "BTC-PERP", "0.8", "77000") // 想要 4000，上限 1000
	e := x.lastExec(v.ID)
	if e.Note != "scaled_instrument" || !e.Applied.Equal(D("0.2")) || !e.Weight.Equal(D("0.8")) {
		t.Fatalf("%+v", e)
	}
	if q := x.pos(v.Account, "BTC-PERP"); !q.Equal(D("0.012")) { // 1000 ÷ 77000 = 0.01298 → 0.012
		t.Fatalf("仓位 %s", q)
	}
}

// 3.8 杠杆超限：总仓位缩到上限并记录
func TestLeverageCap(t *testing.T) {
	x := newEnv(t)
	v := x.start(StartReq{Alloc: D("5000"), MaxLeverage: 1})
	x.signal("a", "BTC-PERP", "0.6", "77000") // 3000，没超
	x.signal("b", "ETH-PERP", "0.6", "2900")  // 想要 3000，只剩 5000×0.99 − BTC 约 3000
	e := x.lastExec(v.ID)
	if e.Note != "scaled_leverage" || e.State != "filled" {
		t.Fatalf("%+v", e)
	}
	view, _ := x.eng.View(v.Account)
	total := decimal.Zero
	for _, p := range view.Positions {
		total = total.Add(p.Notional)
		if p.Leverage != 1 {
			t.Fatalf("杠杆 %d", p.Leverage)
		}
	}
	if total.GreaterThan(D("5000")) {
		t.Fatalf("总仓位 %s 超过 1 倍", total)
	}
}

// 3.8 黑名单跳过；减仓和平仓永远执行
func TestBlacklist(t *testing.T) {
	x := newEnv(t)
	v := x.start(StartReq{Alloc: D("5000"), Blacklist: []string{"ETH-PERP"}})
	x.signal("a", "ETH-PERP", "0.5", "2900")
	if st := x.lastExec(v.ID).State; st != "skipped_blacklist" {
		t.Fatalf("状态 %s", st)
	}
	if !x.pos(v.Account, "ETH-PERP").IsZero() {
		t.Fatal("黑名单不开仓")
	}
	// 持仓后才拉黑：减仓照做，加仓不做，反手只平不开
	x.signal("b", "BTC-PERP", "0.5", "77000")
	q0 := x.pos(v.Account, "BTC-PERP")
	if _, err := x.m.Update(x.u, v.ID, StartReq{Blacklist: []string{"BTC-PERP"}}); err != nil {
		t.Fatal(err)
	}
	x.signal("c", "BTC-PERP", "0.8", "77000")
	if !x.pos(v.Account, "BTC-PERP").Equal(q0) {
		t.Fatal("黑名单不加仓")
	}
	x.signal("d", "BTC-PERP", "0.25", "77000")
	if q := x.pos(v.Account, "BTC-PERP"); !q.LessThan(q0) || q.IsZero() {
		t.Fatalf("减仓照做：%s", q)
	}
	x.signal("e", "BTC-PERP", "-0.5", "77000")
	if q := x.pos(v.Account, "BTC-PERP"); !q.IsZero() {
		t.Fatalf("反手只平不开：%s", q)
	}
}

// 3.8 减仓时价格已经偏离：放宽价格也要减掉
func TestReduceRunsWhenPriceMoved(t *testing.T) {
	x := newEnv(t)
	v := x.start(StartReq{Alloc: D("5000")})
	x.signal("a", "BTC-PERP", "0.5", "77000")
	q0 := x.pos(v.Account, "BTC-PERP")
	x.market("BTC-PERP", "76000", "5")
	x.m.Process("s1", []portfolio.Change{{MsgID: "b", Instrument: "BTC-PERP", Weight: D("0.1"), RefPrice: D("77000"), Urgency: "passive", MaxSlipBps: 20, Deadline: x.clock.Add(time.Minute).UnixMilli()}})
	if q := x.pos(v.Account, "BTC-PERP"); !q.LessThan(q0) {
		t.Fatalf("减仓要立刻做：%s", q)
	}
}

// 平台风控平仓：跟着平
func TestFollowsModelRiskClose(t *testing.T) {
	x := newEnv(t)
	v := x.start(StartReq{Alloc: D("5000")})
	x.signal("a", "BTC-PERP", "0.5", "77000")
	x.pf.Halt("flatten_all", "测试")
	if !x.pos(v.Account, "BTC-PERP").IsZero() {
		t.Fatal("应跟着平")
	}
	if e := x.lastExec(v.ID); e.Note != "halt" || e.State != "filled" {
		t.Fatalf("%+v", e)
	}
}

// 3.9 暂停：不执行新目标，持仓和止损保留；恢复：先能看到要交易什么，然后马上补齐
func TestPauseResume(t *testing.T) {
	x := newEnv(t)
	v := x.start(StartReq{Alloc: D("5000")})
	x.signalU("a", "BTC-PERP", "0.5", "77000", "immediate", &portfolio.Exits{StopLoss: D("0.5")})
	q0 := x.pos(v.Account, "BTC-PERP")
	if _, err := x.m.Pause(x.u, v.ID); err != nil {
		t.Fatal(err)
	}
	x.signal("b", "BTC-PERP", "0.2", "77000")
	x.signal("c", "ETH-PERP", "0.4", "2900")
	if !x.pos(v.Account, "BTC-PERP").Equal(q0) || !x.pos(v.Account, "ETH-PERP").IsZero() {
		t.Fatal("暂停时不执行")
	}
	view, _ := x.eng.View(v.Account)
	if len(view.Positions) != 1 || view.Positions[0].SL.Sign() <= 0 {
		t.Fatalf("止损应保留：%+v", view.Positions)
	}
	if !x.m.IsFollowing(1) {
		t.Fatal("暂停也算开启服务")
	}
	plan, err := x.m.ResumePlan(x.u, v.ID)
	if err != nil || len(plan) != 2 {
		t.Fatalf("恢复前应说明要做的交易：%+v %v", plan, err)
	}
	if !x.pos(v.Account, "ETH-PERP").IsZero() {
		t.Fatal("看计划不该下单")
	}
	if _, err := x.m.Resume(x.u, v.ID); err != nil {
		t.Fatal(err)
	}
	// 0.2 × 5000 ÷ 77000 = 0.01298；差额 0.032−0.0129 = 0.019 → 留 0.013
	if q := x.pos(v.Account, "BTC-PERP"); !q.Equal(D("0.013")) {
		t.Fatalf("恢复后 BTC %s", q)
	}
	if x.pos(v.Account, "ETH-PERP").IsZero() {
		t.Fatal("恢复后应补上 ETH")
	}
}

// 3.9 暂停时止损照样触发
func TestStopFiresWhilePaused(t *testing.T) {
	x := newEnv(t)
	v := x.start(StartReq{Alloc: D("5000")})
	x.signalU("a", "BTC-PERP", "0.5", "77000", "immediate", &portfolio.Exits{StopLoss: D("0.02")})
	if _, err := x.m.Pause(x.u, v.ID); err != nil {
		t.Fatal(err)
	}
	x.pf.OnChange = nil // 只看用户自己的止损
	x.market("BTC-PERP", "75000", "5")
	x.eng.Tick()
	if !x.pos(v.Account, "BTC-PERP").IsZero() {
		t.Fatal("暂停时止损应照常")
	}
}

// 止损止盈按用户自己的开仓价算
func TestExitsUseUsersEntry(t *testing.T) {
	x := newEnv(t)
	v := x.start(StartReq{Alloc: D("5000")})
	x.signalU("a", "BTC-PERP", "0.5", "77000", "immediate", &portfolio.Exits{StopLoss: D("0.02"), TakeProfit: D("0.03")})
	view, _ := x.eng.View(v.Account)
	// 开仓 77007.7：止损 × 0.98 = 75467.546，对齐到 75467.5；止盈 × 1.03 = 79317.931，对齐到 79318.0
	if p := view.Positions[0]; !p.SL.Equal(D("75467.5")) || !p.TP.Equal(D("79318")) {
		t.Fatalf("止损 %s 止盈 %s", p.SL, p.TP)
	}
}

// 3.9 停止的两种方式
func TestStopModes(t *testing.T) {
	x := newEnv(t)
	v := x.start(StartReq{Alloc: D("5000")})
	x.signal("a", "BTC-PERP", "0.5", "77000")
	s, err := x.m.Stop(x.u, v.ID, false)
	if err != nil || s.Status != StStopped || s.Reason != ReasonKeep {
		t.Fatalf("%+v %v", s, err)
	}
	if x.pos(v.Account, "BTC-PERP").IsZero() {
		t.Fatal("保留仓位")
	}
	// 之后的信号不碰它，就是用户自己的仓位了
	q := x.pos(v.Account, "BTC-PERP")
	x.signal("b", "BTC-PERP", "0", "77000")
	if !x.pos(v.Account, "BTC-PERP").Equal(q) {
		t.Fatal("停止后不该动用户的仓位")
	}

	acct2 := x.acc.add(1, "第二个").ID
	v2, err := x.m.Start(context.Background(), x.u, StartReq{Strategy: "s1", Account: acct2, Alloc: D("5000"), Confirm: true})
	if err != nil {
		t.Fatal(err)
	}
	x.signal("c", "BTC-PERP", "0.5", "77000")
	s, err = x.m.Stop(x.u, v2.ID, true)
	if err != nil || s.Status != StStopped || s.Reason != ReasonUser {
		t.Fatalf("%+v %v", s, err)
	}
	if !x.pos(acct2, "BTC-PERP").IsZero() {
		t.Fatal("应平仓")
	}
}

// 用户手动单不受跟随停止影响
func TestStopKeepsManualOrders(t *testing.T) {
	x := newEnv(t)
	v := x.start(StartReq{Alloc: D("5000")})
	o, err := x.eng.Place(v.Account, trading.OrderReq{Instrument: "BTC-PERP", Side: "buy", Type: trading.Limit, Price: D("75000"), Qty: D("0.01")})
	if err != nil {
		t.Fatal(err)
	}
	x.signalU("a", "ETH-PERP", "0.5", "2900", "passive", nil)
	if _, err := x.m.Stop(x.u, v.ID, true); err != nil {
		t.Fatal(err)
	}
	view, _ := x.eng.View(v.Account)
	if len(view.Orders) != 1 || view.Orders[0].ID != o.ID {
		t.Fatalf("只撤跟随的挂单：%+v", view.Orders)
	}
}

// 挂单到时转吃单，结果写进记录
func TestWorkingOrderFinishes(t *testing.T) {
	x := newEnv(t)
	v := x.start(StartReq{Alloc: D("5000")})
	x.signalU("a", "BTC-PERP", "0.5", "77000", "normal", nil)
	if st := x.lastExec(v.ID).State; st != "pending" {
		t.Fatalf("状态 %s", st)
	}
	x.clock = x.clock.Add(10 * time.Second)
	x.m.Tick()
	if e := x.lastExec(v.ID); e.State != "filled" || !e.Filled.Equal(D("0.032")) {
		t.Fatalf("%+v", e)
	}
}

// 新目标来了，旧挂单撤掉
func TestNewTargetReplacesWork(t *testing.T) {
	x := newEnv(t)
	v := x.start(StartReq{Alloc: D("5000")})
	x.signalU("a", "BTC-PERP", "0.5", "77000", "passive", nil)
	x.signal("b", "BTC-PERP", "0.2", "77000")
	view, _ := x.eng.View(v.Account)
	if len(view.Orders) != 0 {
		t.Fatalf("旧挂单应撤掉：%+v", view.Orders)
	}
	if q := x.pos(v.Account, "BTC-PERP"); !q.Equal(D("0.012")) {
		t.Fatalf("仓位 %s", q)
	}
}

// 3.5：开启时 AI 已有仓位：马上按当前目标建仓
func TestStartCatchesUp(t *testing.T) {
	x := newEnv(t)
	x.signal("a", "BTC-PERP", "0.5", "77000")
	v := x.start(StartReq{Alloc: D("5000")})
	if q := x.pos(v.Account, "BTC-PERP"); !q.Equal(D("0.032")) {
		t.Fatalf("仓位 %s", q)
	}
	if e := x.lastExec(v.ID); e.Note != "catch_up" {
		t.Fatalf("%+v", e)
	}
}

// 3.10 重启：状态、参数、持仓完全一样，账本校验通过
func TestRestart(t *testing.T) {
	x := newEnv(t)
	v := x.start(StartReq{Alloc: D("5000"), MaxLoss: D("0.15"), MaxLeverage: 5, PerInst: D("0.6"), Blacklist: []string{"SOL-PERP"}})
	x.signal("a", "BTC-PERP", "0.5", "77000")
	x.signalU("b", "ETH-PERP", "0.3", "2900", "passive", nil) // 重启时还挂着
	if _, err := x.m.Pause(x.u, v.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := x.m.Resume(x.u, v.ID); err != nil {
		t.Fatal(err)
	}
	x.signalU("c", "ETH-PERP", "0.4", "2900", "passive", nil)
	before, _ := x.m.Get(x.u, v.ID)
	beforeJSON, _ := json.Marshal(before.Follow)
	x.m.Flush()
	x.eng.Flush()

	eng2 := trading.New(x.books, V, trading.NewSpecs(oldMinSpecs()), quiet())
	eng2.Now = func() time.Time { return x.clock }
	if err := eng2.Restore(x.tevs); err != nil {
		t.Fatal(err)
	}
	pf2 := portfolio.New("s1", -1, V, V, x.books, eng2)
	m2 := New(eng2, x.acc, func(string) *portfolio.Portfolio { return pf2 }, quiet())
	m2.Now = func() time.Time { return x.clock }
	m2.Restore(x.evs)

	after, err := m2.Get(x.u, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	afterJSON, _ := json.Marshal(after.Follow)
	if string(beforeJSON) != string(afterJSON) {
		t.Fatalf("重启后不一样：\n%s\n%s", beforeJSON, afterJSON)
	}
	if !after.StartEquity.Equal(before.StartEquity) {
		t.Fatal("开启时的权益丢了")
	}
	if len(after.Records) != len(before.Records) {
		t.Fatalf("记录数 %d → %d", len(before.Records), len(after.Records))
	}
	b1, _ := x.eng.Brief(v.Account)
	b2, _ := eng2.Brief(v.Account)
	if !b1.Wallet.Equal(b2.Wallet) || !b1.Pos["BTC-PERP"].Qty.Equal(b2.Pos["BTC-PERP"].Qty) {
		t.Fatalf("持仓：%+v → %+v", b1, b2)
	}
	if err := eng2.Check(); err != nil {
		t.Fatal(err)
	}
	// 挂着的跟随单撤掉，记录标中断
	view, _ := eng2.View(v.Account)
	if len(view.Orders) != 0 {
		t.Fatalf("挂单应撤掉：%+v", view.Orders)
	}
	found := false
	for _, r := range after.Records {
		if r.State == "pending" {
			t.Fatalf("不该有 pending：%+v", r)
		}
		if r.State == "interrupted" {
			found = true
		}
	}
	if !found {
		t.Fatalf("应标中断：%+v", after.Records)
	}
	// 再做一次重放（事件已经写了 interrupted）结果也一样；新 id 不会撞车
	m3 := New(eng2, x.acc, func(string) *portfolio.Portfolio { return pf2 }, quiet())
	m3.Now = func() time.Time { return x.clock }
	m3.Restore(append(append([]Event{}, x.evs...), m2.pending...))
	if m3.nextID != m2.nextID || m3.seq != m2.seq || m3.nextEx != m2.nextEx {
		t.Fatalf("编号不一致：%d/%d %d/%d %d/%d", m3.nextID, m2.nextID, m3.seq, m2.seq, m3.nextEx, m2.nextEx)
	}
}

// 3.11（管理层面）：别人的跟随一律 NOT_FOUND
func TestOwnership(t *testing.T) {
	x := newEnv(t)
	v := x.start(StartReq{Alloc: D("5000")})
	if _, err := x.m.Get(x.u2, v.ID); err == nil {
		t.Fatal("别人的不能看")
	}
	if _, err := x.m.Pause(x.u2, v.ID); err == nil {
		t.Fatal("别人的不能暂停")
	}
	if _, err := x.m.Stop(x.u2, v.ID, true); err == nil {
		t.Fatal("别人的不能停止")
	}
	if _, err := x.m.Update(x.u2, v.ID, StartReq{Alloc: D("100")}); err == nil {
		t.Fatal("别人的不能改")
	}
	if _, err := x.m.ResumePlan(x.u2, v.ID); err == nil {
		t.Fatal("别人的不能看计划")
	}
	if _, err := x.m.Resume(x.u2, v.ID); err == nil {
		t.Fatal("别人的不能恢复")
	}
	if len(x.m.List(x.u2)) != 0 {
		t.Fatal("列表里不该有别人的")
	}
	_, err := x.m.Start(context.Background(), x.u2, StartReq{Strategy: "s1", Account: v.Account, Alloc: D("1000"), Confirm: true})
	wantCode(t, err, "NOT_FOUND")
}

func TestStats(t *testing.T) {
	x := newEnv(t)
	x.start(StartReq{Alloc: D("5000")})
	if _, err := x.m.Start(context.Background(), x.u2, StartReq{Strategy: "s1", Alloc: D("2000"), Confirm: true}); err != nil {
		t.Fatal(err)
	}
	n, c := x.m.Stats("s1")
	if n != 2 || !c.Equal(D("7000")) {
		t.Fatalf("%d 人 %s", n, c)
	}
}

// 账户重置：跟随结束
func TestAccountArchived(t *testing.T) {
	x := newEnv(t)
	v := x.start(StartReq{Alloc: D("5000")})
	x.eng.Archive(v.Account)
	x.m.AccountArchived(v.Account)
	s, _ := x.m.Get(x.u, v.ID)
	if s.Status != StStopped || s.Reason != ReasonArchived {
		t.Fatalf("%s %s", s.Status, s.Reason)
	}
	if strings.Contains(s.Reason, "user") {
		t.Fatal(s.Reason)
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

// 18 第 6.6 条：跟随比例。0.5 倍：策略 0.5 → 你 0.25；单品种上限、改参数时不传比例保持原来的；超出范围拒绝
func TestRatio(t *testing.T) {
	x := newEnv(t)
	_, err := x.m.Start(context.Background(), x.u, StartReq{Strategy: "s1", Alloc: D("1000"), Ratio: D("5"), Confirm: true})
	wantCode(t, err, "BAD_RATIO")
	v := x.start(StartReq{Alloc: D("5000"), Ratio: D("0.5")})
	// 目标名义 0.5 × 0.5 × 5000 = 1250 ÷ 77000 = 0.01623 → 0.016
	x.signal("a", "BTC-PERP", "0.5", "77000")
	e := x.lastExec(v.ID)
	if q := x.pos(v.Account, "BTC-PERP"); !q.Equal(D("0.016")) || !e.Weight.Equal(D("0.5")) || !e.Applied.Equal(D("0.25")) {
		t.Fatalf("0.5 倍：%s %+v", q, e)
	}
	// 改分配金额不传比例：还是 0.5 倍
	if _, err := x.m.Update(x.u, v.ID, StartReq{Alloc: D("5000"), MaxLeverage: 3}); err != nil {
		t.Fatal(err)
	}
	if r := x.m.follows[v.ID].Params.Ratio; !r.Equal(D("0.5")) {
		t.Fatalf("改参数后比例：%s", r)
	}
	// 2 倍：0.3 × 2 = 0.6，单品种上限 50% 管着
	if _, err := x.m.Update(x.u, v.ID, StartReq{Ratio: D("2"), PerInst: D("0.5")}); err != nil {
		t.Fatal(err)
	}
	x.signal("b", "BTC-PERP", "0.3", "77000")
	if e := x.lastExec(v.ID); e.Note != "scaled_instrument" || !e.Applied.Equal(D("0.5")) {
		t.Fatalf("2 倍被单品种上限缩：%+v", e)
	}
	if err := x.eng.Check(); err != nil {
		t.Fatal(err)
	}
}

// 18 第 5.8 条：以前自动建的"AI 跟随"账户还认，不会再建一个
func TestOldFollowAccountName(t *testing.T) {
	x := newEnv(t)
	old := x.acc.add(1, "AI 跟随")
	v := x.start(StartReq{Alloc: D("1000")})
	if v.Account != old.ID || len(x.acc.owned[1]) != 1 {
		t.Fatalf("应该用以前的账户 %d：%d %v", old.ID, v.Account, x.acc.owned[1])
	}
}
