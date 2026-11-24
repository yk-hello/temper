package bots

import (
	"errors"
	"fmt"
	"testing"

	"github.com/shopspring/decimal"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func eq(t *testing.T, got decimal.Decimal, want, what string) {
	t.Helper()
	if got.Sub(d(want)).Abs().GreaterThan(d("0.0000001")) {
		t.Fatalf("%s：应为 %s，实际 %s", what, want, got)
	}
}

// fakeExec 按价格撮合：价格穿过挂单就算成交；市价按现价成交。
type fakeOrder struct {
	id, side, typ string
	px, qty       decimal.Decimal
	status        string
	avg           decimal.Decimal
	reduce        bool
}

type fakeExec struct {
	price  decimal.Decimal
	perp   bool
	orders map[string]*fakeOrder
	seq    int
	high   decimal.Decimal
	pos    decimal.Decimal
	fail   error
	band   decimal.Decimal
	far    decimal.Decimal // >0：离现价超过这个比例的限价单被拒（像撮合的价格保护）
}

func newFake(price string, perp bool) *fakeExec {
	return &fakeExec{price: d(price), perp: perp, orders: map[string]*fakeOrder{}}
}

func (f *fakeExec) Quote(string) (decimal.Decimal, decimal.Decimal, bool) {
	return f.price, f.price, true
}
func (f *fakeExec) High24(string) decimal.Decimal { return f.high }
func (f *fakeExec) SpecOf(string) (Spec, bool) {
	if f.perp {
		return Spec{Tick: d("0.1"), Step: d("0.001"), MinNotional: d("5"), Fee: d("0.0002"), Taker: d("0.0005"), Perp: true, Band: f.band}, true
	}
	return Spec{Tick: d("0.01"), Step: d("0.0001"), MinNotional: d("5"), Fee: d("0.001"), Taker: d("0.001")}, true
}
func (f *fakeExec) Prepare(int64, string, int) error { return nil }
func (f *fakeExec) Place(_ int64, _, side, typ string, px, qty decimal.Decimal, reduce bool, _ string) (string, decimal.Decimal, decimal.Decimal, error) {
	if f.fail != nil {
		return "", zero, zero, f.fail
	}
	if typ == "limit" && f.far.IsPositive() && px.Sub(f.price).Abs().GreaterThan(f.price.Mul(f.far)) {
		return "", zero, zero, rej("PRICE_OUT_OF_RANGE")
	}
	f.seq++
	id := fmt.Sprintf("o%d", f.seq)
	o := &fakeOrder{id: id, side: side, typ: typ, px: px, qty: qty, status: "new", reduce: reduce}
	f.orders[id] = o
	if typ == "market" {
		o.status, o.avg = "filled", f.price
		f.apply(o)
		return id, qty, f.price, nil
	}
	return id, zero, zero, nil
}
func (f *fakeExec) apply(o *fakeOrder) {
	if o.side == "buy" {
		f.pos = f.pos.Add(o.qty)
	} else {
		f.pos = f.pos.Sub(o.qty)
	}
}
func (f *fakeExec) Cancel(_ int64, id string) {
	if o := f.orders[id]; o != nil && o.status == "new" {
		o.status = "canceled"
	}
}
func (f *fakeExec) Order(_ int64, id string) (string, decimal.Decimal, decimal.Decimal, bool) {
	o := f.orders[id]
	if o == nil {
		return "", zero, zero, false
	}
	if o.status == "filled" {
		return o.status, o.qty, o.avg, true
	}
	return o.status, zero, zero, true
}
func (f *fakeExec) Position(int64, string) decimal.Decimal { return f.pos }

// setPrice 价格走到这里，穿过的挂单都成交（按挂单价）。
func (f *fakeExec) setPrice(s string) {
	f.price = d(s)
	for _, o := range f.orders {
		if o.status != "new" || o.typ != "limit" {
			continue
		}
		if (o.side == "buy" && f.price.LessThanOrEqual(o.px)) || (o.side == "sell" && f.price.GreaterThanOrEqual(o.px)) {
			o.status, o.avg = "filled", o.px
			f.apply(o)
		}
	}
}

func (f *fakeExec) open(side string) int {
	n := 0
	for _, o := range f.orders {
		if o.status == "new" && o.side == side {
			n++
		}
	}
	return n
}

func mgr(spot, perp *fakeExec) *Manager {
	m := New(spot, perp, nil)
	now := int64(1_700_000_000_000)
	m.Now = func() int64 { now += 1000; return now }
	return m
}

func spotGrid() Params {
	return Params{Instrument: "BTC-SPOT", Lower: d("90"), Upper: d("110"), Grids: 4, Invest: d("1000")}
}

// 手算：4 格 90/95/100/105/110，现价 100：下面 2 格（90、95）、上面 2 格（100、105）
func TestPlanByHand(t *testing.T) {
	p, err := CheckGrid(SpotGrid, spotGrid(), d("100"), Spec{Tick: d("0.01"), Step: d("0.0001"), MinNotional: d("5"), Fee: d("0.001"), Taker: d("0.001")})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Lines) != 5 || !p.Lines[1].Equal(d("95")) || !p.Lines[4].Equal(d("110")) {
		t.Fatalf("线：%v", p.Lines)
	}
	if p.Below != 2 || p.Above != 2 {
		t.Fatalf("上下：%d %d", p.Below, p.Above)
	}
	// 90 + 95 + 100 + 100 = 385；1000 / 385 = 2.5974
	eq(t, p.Qty, "2.5974", "每格数量")
	eq(t, p.SellQty, "2.5948", "卖出数量扣手续费")
	if p.InitSide != "buy" {
		t.Fatal("上面的格子要先买货")
	}
	// 到手的数量算：2.5974×0.999×110×0.999 − 2.5974×105（最上面一格赚得最少，手续费按金额算）
	eq(t, p.NetLo, d("2.5974").Mul(d("0.999")).Mul(d("110")).Mul(d("0.999")).Sub(d("2.5974").Mul(d("105"))).String(), "最少的一格")
	eq(t, p.NetHi, d("2.5974").Mul(d("0.999")).Mul(d("95")).Mul(d("0.999")).Sub(d("2.5974").Mul(d("90"))).String(), "最多的一格")
	if !p.NetLo.IsPositive() {
		t.Fatal("这个间距应该赚得到")
	}
}

func TestPlanRejects(t *testing.T) {
	s := Spec{Tick: d("0.01"), Step: d("0.0001"), MinNotional: d("5"), Fee: d("0.001"), Taker: d("0.001")}
	bad := func(mut func(*Params), code string) {
		t.Helper()
		p := spotGrid()
		mut(&p)
		_, err := CheckGrid(SpotGrid, p, d("100"), s)
		if r, ok := err.(*Rej); !ok || r.Code != code {
			t.Fatalf("应为 %s，实际 %v", code, err)
		}
	}
	bad(func(p *Params) { p.Upper = d("80") }, "GRID_BAD_RANGE")
	bad(func(p *Params) { p.Grids = 1 }, "GRID_BAD_COUNT")
	bad(func(p *Params) { p.Grids = 101 }, "GRID_BAD_COUNT")
	bad(func(p *Params) { p.Invest = d("10") }, "GRID_TOO_SMALL")
	// 格子太窄赚不够手续费：不拒绝，但要提醒
	p := Params{Instrument: "BTC-SPOT", Lower: d("99.9"), Upper: d("100.1"), Grids: 4, Invest: d("1000")}
	plan, err := CheckGrid(SpotGrid, p, d("100"), s)
	if err != nil {
		t.Fatal(err)
	}
	if plan.NetLo.IsPositive() || len(plan.Warn) == 0 || plan.Warn[0] != "GRID_NET_NEGATIVE" {
		t.Fatalf("要提醒赚不够手续费：%v %v", plan.NetLo, plan.Warn)
	}
}

// 网格走一遍：下跌买入 → 挂卖 → 上涨卖出 → 赚一格，再挂回买单
func TestGridCycle(t *testing.T) {
	sp := newFake("100", false)
	m := mgr(sp, newFake("100", true))
	b, err := m.Start(1, 1, SpotGrid, spotGrid())
	if err != nil {
		t.Fatal(err)
	}
	if sp.open("buy") != 2 || sp.open("sell") != 2 {
		t.Fatalf("应该下面 2 张买、上面 2 张卖：%d %d", sp.open("buy"), sp.open("sell"))
	}
	if !sp.pos.Equal(b.Plan.InitQty) {
		t.Fatalf("开始要先买货：%s", sp.pos)
	}
	sp.setPrice("94.9") // 穿过 95
	m.Tick()
	if sp.open("buy") != 1 || sp.open("sell") != 3 {
		t.Fatalf("买到后挂卖单：%d %d", sp.open("buy"), sp.open("sell"))
	}
	if got := m.List(1)[0]; got.Pairs != 0 {
		t.Fatal("还没做完一买一卖")
	}
	sp.setPrice("100.1") // 穿过 100
	m.Tick()
	got := m.List(1)[0]
	if got.Pairs != 1 {
		t.Fatalf("做完一格：%d", got.Pairs)
	}
	got0 := b.Plan.Qty.Mul(d("0.999"))
	want := got0.Mul(d("100")).Sub(b.Plan.Qty.Mul(d("95"))).Sub(got0.Mul(d("100")).Mul(d("0.001")))
	eq(t, got.Realized, want.String(), "这一格的净利")
	if sp.open("buy") != 2 {
		t.Fatal("卖掉后挂回买单")
	}
	// 涨穿上面的格子：卖开始时买的货，也算赚
	sp.setPrice("105.1")
	m.Tick()
	if got := m.List(1)[0]; got.Pairs < 2 || !got.Realized.GreaterThan(want) {
		t.Fatalf("上面的格子也赚：%d %s", got.Pairs, got.Realized)
	}
}

// 跌破下限：不再买，持币等（D83）；涨回来接着做
func TestGridBelowRange(t *testing.T) {
	sp := newFake("100", false)
	m := mgr(sp, newFake("100", true))
	if _, err := m.Start(1, 1, SpotGrid, spotGrid()); err != nil {
		t.Fatal(err)
	}
	sp.setPrice("80")
	m.Tick()
	got := m.List(1)[0]
	if got.Status != StRunning || sp.open("buy") != 0 {
		t.Fatalf("跌破下限不再买，也不停：%s %d", got.Status, sp.open("buy"))
	}
	if v := m.viewOf(got); v.Range != "below" || !v.Unreal.IsNegative() {
		t.Fatalf("写明在区间下面，浮亏：%s %s", v.Range, v.Unreal)
	}
}

func TestGridStopLoss(t *testing.T) {
	sp := newFake("100", false)
	m := mgr(sp, newFake("100", true))
	p := spotGrid()
	p.StopLoss = d("85")
	if _, err := m.Start(1, 1, SpotGrid, p); err != nil {
		t.Fatal(err)
	}
	sp.setPrice("84")
	m.Tick()
	got := m.List(1)[0]
	if got.Status != StStopped || got.Reason != "STOP_LOSS" {
		t.Fatalf("到止损要停：%s %s", got.Status, got.Reason)
	}
	if sp.open("buy") != 0 || sp.open("sell") != 0 {
		t.Fatal("挂单全撤")
	}
	if sp.pos.GreaterThan(d("0.2")) {
		t.Fatalf("货要卖掉：%s", sp.pos)
	}
	if m.BotOn(1, "BTC-SPOT") {
		t.Fatal("停了以后这个币空出来")
	}
}

// 手动停止：可以留着货转手动
func TestGridStopKeep(t *testing.T) {
	sp := newFake("100", false)
	m := mgr(sp, newFake("100", true))
	b, _ := m.Start(1, 1, SpotGrid, spotGrid())
	held := sp.pos
	if _, err := m.Stop(1, b.ID, false); err != nil {
		t.Fatal(err)
	}
	if !sp.pos.Equal(held) || sp.open("buy") != 0 {
		t.Fatal("留着货、撤单")
	}
	if _, err := m.Stop(2, b.ID, false); err == nil {
		t.Fatal("别人的不能停")
	}
}

// 同一账户同一个币只有一个引擎（D42）；手动单成交后机器人暂停，可以恢复（D83）
func TestConflicts(t *testing.T) {
	sp, pe := newFake("100", false), newFake("100", true)
	m := mgr(sp, pe)
	if _, err := m.Start(1, 1, SpotGrid, spotGrid()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(1, 1, SpotGrid, spotGrid()); err == nil || err.(*Rej).Code != "BOT_COIN_BUSY" {
		t.Fatal("同一个币不能开两个", err)
	}
	if _, err := m.Start(1, 2, SpotGrid, spotGrid()); err != nil {
		t.Fatal("别的账户可以", err)
	}
	m.ManualAction(1, "BTC-SPOT")
	m.Tick()
	var b Bot
	for _, x := range m.List(1) {
		if x.Account == 1 {
			b = x
		}
	}
	if b.Status != StPaused || b.Reason != "MANUAL" {
		t.Fatalf("手动单要让机器人暂停：%s %s", b.Status, b.Reason)
	}
	if _, err := m.Resume(1, b.ID); err != nil {
		t.Fatal(err)
	}
	m.Tick()
	if sp.open("buy") == 0 {
		t.Fatal("恢复后接着挂单")
	}
	// 合约：这个币上已经有手动仓位，不开
	pe.pos = d("1")
	gp := Params{Instrument: "BTC-PERP", Lower: d("90"), Upper: d("110"), Grids: 4, Invest: d("1000"), Dir: "long", Leverage: 3}
	if _, err := m.Start(1, 1, PerpGrid, gp); err == nil || err.(*Rej).Code != "BOT_COIN_HAS_POSITION" {
		t.Fatal("有手动仓位不开", err)
	}
}

// 优先级：默认 AI 优先；用户把机器人调到最前面后，拒绝 AI
func TestArbitrate(t *testing.T) {
	sp := newFake("100", false)
	m := mgr(sp, newFake("100", true))
	if _, err := m.Start(7, 1, SpotGrid, spotGrid()); err != nil {
		t.Fatal(err)
	}
	if err := m.Arbitrate(7, 1, "BTC-SPOT", "ai"); err != nil {
		t.Fatal("默认 AI 优先", err)
	}
	m.Tick()
	if m.List(7)[0].Status != StPaused {
		t.Fatal("机器人让路")
	}
	if _, err := m.Resume(7, m.List(7)[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := m.SetOrder(nil, 7, []string{"bot", "ai", "signal"}); err != nil { //nolint
		t.Fatal(err)
	}
	if err := m.Arbitrate(7, 1, "BTC-SPOT", "ai"); err == nil {
		t.Fatal("机器人优先时拒绝 AI")
	}
	if err := m.Arbitrate(7, 9, "BTC-SPOT", "ai"); err != nil {
		t.Fatal("别的账户不受影响", err)
	}
	if err := m.SetOrder(nil, 7, []string{"bot", "bot", "ai"}); err == nil { //nolint
		t.Fatal("顺序写错要拒绝")
	}
}

// 合约网格：中性没有初始仓位；做空先卖下面的格子；太远的线先不挂
func TestPerpGrid(t *testing.T) {
	pe := newFake("100", true)
	m := mgr(newFake("100", false), pe)
	p := Params{Instrument: "BTC-PERP", Lower: d("90"), Upper: d("110"), Grids: 4, Invest: d("1000"), Dir: "neutral", Leverage: 3}
	b, err := m.Start(1, 1, PerpGrid, p)
	if err != nil {
		t.Fatal(err)
	}
	if !pe.pos.IsZero() || b.Plan.InitSide != "" {
		t.Fatal("中性没有初始仓位")
	}
	if pe.open("buy") != 2 || pe.open("sell") != 2 {
		t.Fatalf("上下都挂：%d %d", pe.open("buy"), pe.open("sell"))
	}
	if b.Plan.LiqPx.IsZero() {
		t.Fatal("合约要算强平价")
	}
	// 上涨卖开空 → 回落买平，赚一格
	pe.setPrice("105.1")
	m.Tick()
	pe.setPrice("99.9")
	m.Tick()
	got := m.List(1)[0]
	if got.Pairs == 0 || !got.Realized.IsPositive() {
		t.Fatalf("中性上涨卖、回落买要赚：%d %s", got.Pairs, got.Realized)
	}
	_, _ = m.Stop(1, b.ID, true)

	// 做空
	ps := newFake("100", true)
	m2 := mgr(newFake("100", false), ps)
	p.Dir = "short"
	bs, err := m2.Start(1, 1, PerpGrid, p)
	if err != nil {
		t.Fatal(err)
	}
	if bs.Plan.InitSide != "sell" || !ps.pos.IsNegative() {
		t.Fatalf("做空先卖：%s %s", bs.Plan.InitSide, ps.pos)
	}
	// 太远的线先不挂，走近了再挂
	pb := newFake("100", true)
	pb.band = d("0.06")
	m3 := mgr(newFake("100", false), pb)
	p.Dir = "neutral"
	p.Instrument = "ETH-PERP"
	if _, err := m3.Start(1, 1, PerpGrid, p); err != nil {
		t.Fatal(err)
	}
	if n := pb.open("buy") + pb.open("sell"); n != 2 {
		t.Fatalf("90 和 110 离现价太远不挂，95、105 挂：%d", n)
	}
	pb.setPrice("94")
	m3.Tick()
	m3.Tick()
	if pb.open("buy") == 0 {
		t.Fatal("走近了要补挂")
	}
}

func dcaParams() Params {
	return Params{Instrument: "BTC-SPOT", First: d("100"), Add: d("100"), StepPct: d("5"), Mult: d("2"), MaxAdds: 2, TPPct: d("2")}
}

// DCA：首单 + 跌 5% 补 100 + 跌 10% 补 200；每补一次止盈价跟着均价走
func TestDCA(t *testing.T) {
	sp := newFake("100", false)
	m := mgr(sp, newFake("100", true))
	plan, err := m.Preview(DCA, dcaParams())
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Ladder) != 2 || !plan.Ladder[0].Px.Equal(d("95")) || !plan.Ladder[1].Px.Equal(d("90")) || !plan.Ladder[1].USD.Equal(d("200")) {
		t.Fatalf("梯子：%+v", plan.Ladder)
	}
	eq(t, plan.Need, "400", "总共要 100 + 100 + 200")
	if _, err := m.Start(1, 1, DCA, dcaParams()); err != nil {
		t.Fatal(err)
	}
	if sp.open("buy") != 2 || sp.open("sell") != 1 {
		t.Fatalf("两档补仓加止盈：%d %d", sp.open("buy"), sp.open("sell"))
	}
	b := m.List(1)[0]
	tp0 := sp.orders[b.TPOrder].px
	eq(t, tp0, "102.11", "止盈价 = 均价 × 1.02（均价含手续费，向上对齐）")
	sp.setPrice("94.9")
	m.Tick()
	b = m.List(1)[0]
	if !b.Avg.LessThan(d("100")) || sp.orders[b.TPOrder].px.GreaterThanOrEqual(tp0) {
		t.Fatalf("补仓后均价和止盈价都下来：%s %s", b.Avg, sp.orders[b.TPOrder].px)
	}
	sp.setPrice(sp.orders[b.TPOrder].px.Add(d("0.1")).String())
	m.Tick()
	b = m.List(1)[0]
	if b.Status != StStopped || b.Reason != "TAKE_PROFIT" || !b.Realized.IsPositive() {
		t.Fatalf("到止盈结束，赚钱：%s %s %s", b.Status, b.Reason, b.Realized)
	}
	if sp.open("buy") != 0 {
		t.Fatal("剩下的补仓单撤掉")
	}
}

func TestDCARepeatAndStop(t *testing.T) {
	sp := newFake("100", false)
	m := mgr(sp, newFake("100", true))
	p := dcaParams()
	p.Repeat = true
	if _, err := m.Start(1, 1, DCA, p); err != nil {
		t.Fatal(err)
	}
	b := m.List(1)[0]
	sp.setPrice(sp.orders[b.TPOrder].px.Add(d("0.1")).String())
	m.Tick()
	b = m.List(1)[0]
	if b.Status != StRunning || b.Round != 2 {
		t.Fatalf("重复：开始第二轮：%s %d", b.Status, b.Round)
	}
	// 止损
	sp2 := newFake("100", false)
	m2 := mgr(sp2, newFake("100", true))
	q := dcaParams()
	q.SLPct = d("20")
	if _, err := m2.Start(1, 1, DCA, q); err != nil {
		t.Fatal(err)
	}
	sp2.setPrice("70")
	m2.Tick() // 先成交两档补仓，再算止损
	m2.Tick()
	if got := m2.List(1)[0]; got.Status != StStopped || got.Reason != "STOP_LOSS" || !got.Realized.IsNegative() {
		t.Fatalf("到止损平掉：%s %s %s", got.Status, got.Reason, got.Realized)
	}
}

// 抄底：没跌到不动；跌到才买第一笔（D83）
func TestDip(t *testing.T) {
	sp := newFake("100", false)
	m := mgr(sp, newFake("100", true))
	p := dcaParams()
	p.TriggerPx = d("90")
	if _, err := m.Start(1, 1, Dip, p); err != nil {
		t.Fatal(err)
	}
	m.Tick()
	b := m.List(1)[0]
	if b.Status != StWaiting || sp.seq != 0 {
		t.Fatalf("没跌到不动：%s %d", b.Status, sp.seq)
	}
	sp.setPrice("89")
	m.Tick()
	b = m.List(1)[0]
	if b.Status != StRunning || !b.Avg.IsPositive() {
		t.Fatalf("跌到开始：%s %s", b.Status, b.Avg)
	}
	// 按 24 小时高点跌幅
	sp2 := newFake("95", false)
	sp2.high = d("100")
	m2 := mgr(sp2, newFake("100", true))
	q := dcaParams()
	q.DropPct = d("10")
	if _, err := m2.Start(1, 1, Dip, q); err != nil {
		t.Fatal(err)
	}
	m2.Tick()
	if m2.List(1)[0].Status != StWaiting {
		t.Fatal("跌 5% 不够")
	}
	sp2.setPrice("89.9")
	m2.Tick()
	if m2.List(1)[0].Status != StRunning {
		t.Fatal("跌 10% 开始")
	}
	if _, err := m2.Preview(Dip, dcaParams()); err == nil {
		t.Fatal("抄底没写条件要拒绝")
	}
}

// 定投：第一次马上买，到时间再买；到次数停；错过的不补
func TestRecur(t *testing.T) {
	sp := newFake("100", false)
	m := New(sp, newFake("100", true), nil)
	now := int64(1_700_000_000_000)
	m.Now = func() int64 { return now }
	p := Params{Instrument: "BTC-SPOT", Amount: d("50"), Every: "day", Times: 3}
	if _, err := m.Start(1, 1, Recur, p); err != nil {
		t.Fatal(err)
	}
	m.Tick()
	b := m.List(1)[0]
	if b.Count != 1 {
		t.Fatalf("第一次马上买：%d", b.Count)
	}
	m.Tick()
	if m.List(1)[0].Count != 1 {
		t.Fatal("没到时间不买")
	}
	now += 86_400_000 * 5 // 停了 5 天
	m.Tick()
	b = m.List(1)[0]
	if b.Count != 2 {
		t.Fatalf("错过的不补，只买一次：%d", b.Count)
	}
	if b.NextAt <= now {
		t.Fatal("下一次从现在算")
	}
	now += 86_400_000
	m.Tick()
	b = m.List(1)[0]
	if b.Count != 3 || b.Status != StStopped || b.Reason != "DONE" {
		t.Fatalf("买够次数停：%d %s", b.Count, b.Status)
	}
	eq(t, b.Spent, "150", "花了多少")
}

// 下单失败：暂停，写原因，不是一直重试
func TestPlaceFailPauses(t *testing.T) {
	sp := newFake("100", false)
	m := mgr(sp, newFake("100", true))
	if _, err := m.Start(1, 1, SpotGrid, spotGrid()); err != nil {
		t.Fatal(err)
	}
	sp.setPrice("94.9")
	sp.fail = rej("INSUFFICIENT_BALANCE")
	m.Tick()
	got := m.List(1)[0]
	if got.Status != StPaused || got.Reason != "INSUFFICIENT_BALANCE" {
		t.Fatalf("%s %s", got.Status, got.Reason)
	}
}

// 每格数量很小时，手续费取整的零头不能丢：攒够一个单位就带着一起卖
func TestGridDust(t *testing.T) {
	sp := newFake("100", false)
	m := mgr(sp, newFake("100", true))
	// 最小单位 0.0001：每格 0.0733 个，扣 0.1% 后 0.07322，卖 0.0732，零头 0.00003 左右
	p := Params{Instrument: "BTC-SPOT", Lower: d("90"), Upper: d("110"), Grids: 4, Invest: d("28.3")}
	b, err := m.Start(1, 1, SpotGrid, p)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		sp.setPrice("94.9")
		m.Tick()
		sp.setPrice("100.1")
		m.Tick()
	}
	got := m.List(1)[0]
	if got.Pairs < 10 || !got.Realized.IsPositive() {
		t.Fatalf("%d %s", got.Pairs, got.Realized)
	}
	if got.Dust.GreaterThanOrEqual(d("0.0001")) || got.Dust.IsNegative() {
		t.Fatalf("零头攒够就要卖掉：%s", got.Dust)
	}
	_ = b
}

// 机器人数量上限：防滥用，写在配置里（D85 第 2 条）。停掉的不占名额。
func TestBotLimit(t *testing.T) {
	sp := newFake("100", false)
	m := mgr(sp, newFake("100", true))
	m.MaxBots = func(int64) int { return 1 }
	b, err := m.Start(1, 1, SpotGrid, spotGrid())
	if err != nil {
		t.Fatal(err)
	}
	var r *Rej
	if _, err := m.Start(1, 2, SpotGrid, spotGrid()); !errors.As(err, &r) || r.Code != "BOT_LIMIT" {
		t.Fatalf("超过上限：%v", err)
	}
	if _, err := m.Start(2, 3, SpotGrid, spotGrid()); err != nil {
		t.Fatalf("别人不受影响：%v", err)
	}
	if _, err := m.Stop(1, b.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(1, 2, SpotGrid, spotGrid()); err != nil {
		t.Fatalf("停掉后名额空出来：%v", err)
	}
}

// 11-16 走查：机器人挂远处的格子被撮合的价格保护拒掉时，启动失败，开始那笔市价单要退回去，不能留一个没人管的仓位；
// 机器人的挂单范围按品种真实的价格保护算（BTC、ETH ±3%，多数币 ±5%），留一成余量。
func TestGridStartFailureUnwinds(t *testing.T) {
	pe := newFake("100", true)
	pe.band, pe.far = d("0.045"), d("0.03") // 机器人以为 4.5% 以内都能挂，撮合只认 3%（改之前的样子）
	m := mgr(newFake("100", false), pe)
	p := Params{Instrument: "ETH-PERP", Lower: d("90"), Upper: d("110"), Grids: 20, Invest: d("1000"), Dir: "long", Leverage: 3}
	if _, err := m.Start(1, 1, PerpGrid, p); err == nil {
		t.Fatal("挂单被拒，启动应该失败")
	}
	if !pe.pos.IsZero() {
		t.Fatalf("启动失败后开始那笔要退回去，现在还有仓位 %s", pe.pos)
	}
	if pe.open("buy")+pe.open("sell") != 0 {
		t.Fatal("挂单都要撤掉")
	}
	if m.BotOn(1, "ETH-PERP") {
		t.Fatal("这个币不能还被占着")
	}
	// 范围按品种算：ETH 3% → 2.7%，多数币 5% → 4.5%，不知道时 4.5%
	eq(t, bandOf(d("1.03"), d("0.97")), "0.027", "ETH 的挂单范围")
	eq(t, bandOf(d("1.05"), d("0.95")), "0.045", "多数币的挂单范围")
	eq(t, bandOf(zero, zero), "0.045", "不知道时")
	// 按真实范围算，同样的网格能开起来：远处的格子先不挂
	pe2 := newFake("100", true)
	pe2.band, pe2.far = bandOf(d("1.03"), d("0.97")), d("0.03")
	m2 := mgr(newFake("100", false), pe2)
	if _, err := m2.Start(1, 1, PerpGrid, p); err != nil {
		t.Fatal("按真实范围应该能开：", err)
	}
}
