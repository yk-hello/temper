package follow

import (
	"context"
	"testing"

	"github.com/shopspring/decimal"

	"qs/internal/portfolio"
	"qs/internal/trading"
)

// 手动权限最高（12 的 4.1–4.8、D80）。

func (x *env) notices(acct int64) []string {
	v, err := x.eng.View(acct)
	if err != nil {
		x.t.Fatal(err)
	}
	var out []string
	for _, n := range v.Notices {
		out = append(out, n.Kind+":"+n.Instrument)
	}
	return out
}

func has(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func (x *env) manualBuy(acct int64, inst, qty string) {
	x.t.Helper()
	if _, err := x.eng.Place(acct, trading.OrderReq{Instrument: inst, Side: "buy", Type: trading.Market, Qty: D(qty)}); err != nil {
		x.t.Fatalf("手动下单：%v", err)
	}
	x.m.ManualAction(acct, "")
}

// 4.2、4.3、4.5 手动下单直接生效，转手动管理并提示；之后 AI 跳过这个币
func TestManualTakesOver(t *testing.T) {
	x := newEnv(t)
	v := x.start(StartReq{Alloc: D("5000")})
	x.signal("m1", "BTC-PERP", "0.5", "77000")
	before := x.pos(v.Account, "BTC-PERP")
	if before.IsZero() {
		t.Fatal("AI 应该开仓")
	}
	x.manualBuy(v.Account, "BTC-PERP", "0.002")
	got, _ := x.m.Get(x.u, v.ID)
	if got.Manual["BTC-PERP"] == 0 {
		t.Fatalf("应转手动管理：%+v", got.Manual)
	}
	if !has(x.notices(v.Account), "takeover:BTC-PERP") {
		t.Fatalf("应提示：%v", x.notices(v.Account))
	}
	if got.Records[0].State != "manual" {
		t.Fatalf("应记进执行记录：%+v", got.Records[0])
	}
	held := x.pos(v.Account, "BTC-PERP")
	if !held.Equal(before.Add(D("0.002"))) {
		t.Fatalf("手动单直接生效：%s", held)
	}
	x.signal("m2", "BTC-PERP", "0.1", "77000")
	if !x.pos(v.Account, "BTC-PERP").Equal(held) {
		t.Fatal("AI 不应再调整手动管理的币")
	}
	if e := x.lastExec(v.ID); e.State != "skipped_manual" {
		t.Fatalf("AI 跳过要留记录：%+v", e)
	}
	// 别的币照常
	x.signal("m3", "ETH-PERP", "0.2", "2900")
	if x.pos(v.Account, "ETH-PERP").IsZero() {
		t.Fatal("别的币 AI 照常跟")
	}
}

// 4.3 只挂单不算转手动；挂单成交了才算（每秒对账）
func TestRestingOrderIsNotTakeover(t *testing.T) {
	x := newEnv(t)
	v := x.start(StartReq{Alloc: D("5000")})
	x.signal("m1", "BTC-PERP", "0.5", "77000")
	o, err := x.eng.Place(v.Account, trading.OrderReq{Instrument: "BTC-PERP", Side: "buy", Type: trading.Limit, TIF: trading.GTC, Price: D("76000"), Qty: D("0.002")})
	if err != nil {
		t.Fatal(err)
	}
	x.m.ManualAction(v.Account, "")
	if got, _ := x.m.Get(x.u, v.ID); len(got.Manual) != 0 {
		t.Fatalf("只挂单不算：%+v", got.Manual)
	}
	x.signal("m2", "BTC-PERP", "0.4", "77000")
	if e := x.lastExec(v.ID); e.State == "skipped_manual" {
		t.Fatal("只挂单时 AI 照常调整")
	}
	if _, err := x.eng.Cancel(v.Account, o.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := x.m.Get(x.u, v.ID); len(got.Manual) != 0 {
		t.Fatal("撤掉就当没发生过")
	}
}

// 4.2 手动改止盈止损也算转手动
func TestManualTPSLTakesOver(t *testing.T) {
	x := newEnv(t)
	v := x.start(StartReq{Alloc: D("5000")})
	x.signal("m1", "BTC-PERP", "0.5", "77000")
	sl := D("70000")
	if err := x.eng.SetTPSL(v.Account, "BTC-PERP", nil, &sl, trading.ByMark); err != nil {
		t.Fatal(err)
	}
	x.m.ManualAction(v.Account, "BTC-PERP")
	if got, _ := x.m.Get(x.u, v.ID); got.Manual["BTC-PERP"] == 0 {
		t.Fatal("改止损应转手动管理")
	}
}

// 4.4 平仓后还给 AI 并提示；不立刻追，等下次调仓
func TestManualCloseReturnsToAI(t *testing.T) {
	x := newEnv(t)
	v := x.start(StartReq{Alloc: D("5000")})
	x.signal("m1", "BTC-PERP", "0.5", "77000")
	x.manualBuy(v.Account, "BTC-PERP", "0.002")
	if _, err := x.eng.ClosePosition(v.Account, "BTC-PERP", D("1"), trading.Market, decimal.Zero); err != nil {
		t.Fatal(err)
	}
	x.m.ManualAction(v.Account, "")
	got, _ := x.m.Get(x.u, v.ID)
	if len(got.Manual) != 0 || got.Records[0].State != "manual_end" {
		t.Fatalf("应还给 AI 并留记录：%+v %+v", got.Manual, got.Records[0])
	}
	if !has(x.notices(v.Account), "takeover_end:BTC-PERP") {
		t.Fatalf("应提示：%v", x.notices(v.Account))
	}
	x.m.Tick()
	if !x.pos(v.Account, "BTC-PERP").IsZero() {
		t.Fatal("不应立刻追回去")
	}
	x.signal("m2", "BTC-PERP", "0.3", "77000")
	if x.pos(v.Account, "BTC-PERP").IsZero() {
		t.Fatal("下次调仓 AI 恢复")
	}
}

// 手动把 AI 的仓位直接平掉：不用先转手动，直接提示
func TestManualCloseOfAIPosition(t *testing.T) {
	x := newEnv(t)
	v := x.start(StartReq{Alloc: D("5000")})
	x.signal("m1", "BTC-PERP", "0.5", "77000")
	if _, err := x.eng.ClosePosition(v.Account, "BTC-PERP", D("1"), trading.Market, decimal.Zero); err != nil {
		t.Fatal(err)
	}
	x.m.ManualAction(v.Account, "")
	got, _ := x.m.Get(x.u, v.ID)
	if len(got.Manual) != 0 || !has(x.notices(v.Account), "takeover_end:BTC-PERP") || has(x.notices(v.Account), "takeover:BTC-PERP") {
		t.Fatalf("手动管理 %+v，提示 %v", got.Manual, x.notices(v.Account))
	}
}

// 4.6 开启跟随时已有的仓位算手动管理
func TestExistingPositionIsManual(t *testing.T) {
	x := newEnv(t)
	a := x.acc.add(1, "手动")
	if _, err := x.eng.Place(a.ID, trading.OrderReq{Instrument: "BTC-PERP", Side: "buy", Type: trading.Market, Qty: D("0.01")}); err != nil {
		t.Fatal(err)
	}
	v := x.start(StartReq{Alloc: D("5000"), Account: a.ID})
	if v.Manual["BTC-PERP"] == 0 {
		t.Fatalf("已有仓位算手动：%+v", v.Manual)
	}
	x.signal("m1", "BTC-PERP", "0.5", "77000")
	if !x.pos(a.ID, "BTC-PERP").Equal(D("0.01")) {
		t.Fatal("AI 不动已有的仓位")
	}
}

// 4.7 停止跟随选平仓、最大亏损平仓：只平 AI 管的
func TestStopClosesOnlyAIPositions(t *testing.T) {
	x := newEnv(t)
	v := x.start(StartReq{Alloc: D("5000")})
	x.signal("m1", "BTC-PERP", "0.5", "77000")
	x.signal("m2", "ETH-PERP", "0.2", "2900")
	x.manualBuy(v.Account, "BTC-PERP", "0.002")
	held := x.pos(v.Account, "BTC-PERP")
	if _, err := x.m.Stop(x.u, v.ID, true); err != nil {
		t.Fatal(err)
	}
	if !x.pos(v.Account, "ETH-PERP").IsZero() {
		t.Fatal("AI 管的要平")
	}
	if !x.pos(v.Account, "BTC-PERP").Equal(held) {
		t.Fatal("手动管理的不动")
	}
	if got, _ := x.m.Get(x.u, v.ID); got.Status != StStopped {
		t.Fatalf("手动的仓位不影响停好：%s", got.Status)
	}
}

// 4.5 恢复补齐和预览都跳过手动管理的币
func TestResumeSkipsManual(t *testing.T) {
	x := newEnv(t)
	v := x.start(StartReq{Alloc: D("5000")})
	x.signal("m1", "BTC-PERP", "0.5", "77000")
	if _, err := x.m.Pause(x.u, v.ID); err != nil {
		t.Fatal(err)
	}
	x.manualBuy(v.Account, "BTC-PERP", "0.002")
	held := x.pos(v.Account, "BTC-PERP")
	x.signal("m2", "BTC-PERP", "0.1", "77000")
	plan, err := x.m.ResumePlan(x.u, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 0 {
		t.Fatalf("预览不应包含手动管理的币：%+v", plan)
	}
	if _, err := x.m.Resume(x.u, v.ID); err != nil {
		t.Fatal(err)
	}
	if !x.pos(v.Account, "BTC-PERP").Equal(held) {
		t.Fatal("恢复时不动手动管理的币")
	}
}

// 4.8 AI 自己的止损不算手动；手动管理的状态重放后还在
func TestAIExitsAreNotManual(t *testing.T) {
	x := newEnv(t)
	v := x.start(StartReq{Alloc: D("5000")})
	x.signalU("m1", "BTC-PERP", "0.5", "77000", "immediate", &portfolio.Exits{StopLoss: D("0.02")})
	if x.pos(v.Account, "BTC-PERP").IsZero() {
		t.Fatal("AI 应该开仓")
	}
	x.market("BTC-PERP", "75000", "5")
	x.eng.Tick()
	if !x.pos(v.Account, "BTC-PERP").IsZero() {
		t.Fatal("止损应触发")
	}
	x.m.Tick()
	got, _ := x.m.Get(x.u, v.ID)
	if len(got.Manual) != 0 || has(x.notices(v.Account), "takeover_end:BTC-PERP") {
		t.Fatalf("AI 的止损不算手动：%+v %v", got.Manual, x.notices(v.Account))
	}
}

// 4.18 纪律锁锁定时 AI 不开新仓、不加仓，写明原因；减仓和平仓照做
func TestLockedStopsAIOpening(t *testing.T) {
	x := newEnv(t)
	v := x.start(StartReq{Alloc: D("5000")})
	x.signal("m1", "BTC-PERP", "0.5", "77000")
	held := x.pos(v.Account, "BTC-PERP")
	x.eng.Guard = func(p trading.PreOrder) error {
		if p.Account == v.Account {
			return trading.NewReject("DISCIPLINE_LOCKED", "rule", "daily_loss")
		}
		return nil
	}
	x.signal("m2", "ETH-PERP", "0.2", "2900")
	if !x.pos(v.Account, "ETH-PERP").IsZero() {
		t.Fatal("锁定时 AI 不开新仓")
	}
	if e := x.lastExec(v.ID); e.Reject != "DISCIPLINE_LOCKED" {
		t.Fatalf("要写明原因：%+v", e)
	}
	x.signal("m3", "BTC-PERP", "0.8", "77000")
	if !x.pos(v.Account, "BTC-PERP").Equal(held) {
		t.Fatal("锁定时 AI 不加仓")
	}
	x.signal("m4", "BTC-PERP", "0", "77000")
	if !x.pos(v.Account, "BTC-PERP").IsZero() {
		t.Fatal("锁定时 AI 平仓照做")
	}
}

func TestManualSurvivesRestart(t *testing.T) {
	x := newEnv(t)
	v := x.start(StartReq{Alloc: D("5000")})
	x.signal("m1", "BTC-PERP", "0.5", "77000")
	x.manualBuy(v.Account, "BTC-PERP", "0.002")
	x.m.Flush()
	m2 := New(x.eng, x.acc, func(string) *portfolio.Portfolio { return x.pf }, quiet())
	m2.Now = x.m.Now
	m2.Restore(x.evs)
	got, err := m2.Get(x.u, v.ID)
	if err != nil || got.Manual["BTC-PERP"] == 0 {
		t.Fatalf("重放后手动管理还在：%+v %v", got, err)
	}
	if manual, following := m2.ManualInsts(v.Account); !following || !manual["BTC-PERP"] {
		t.Fatalf("查询：%v %v", manual, following)
	}
	if m2.AIManaged(v.Account, "BTC-PERP") {
		t.Fatal("手动管理的币不算 AI 管")
	}
	_ = context.Background()
}
