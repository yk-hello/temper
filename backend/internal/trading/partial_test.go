package trading

import (
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"github.com/shopspring/decimal"
)

// bigPos 开一个大仓位：账户 id 有 bal 的本金，20 倍，买 300 BTC（60000 × 300 = 1800 万，BTC 第 5 档，维持保证金率 2%）。
func bigPos(t *testing.T, x *env, id int64, bal, mode string) {
	t.Helper()
	x.e.Open(id, D(bal))
	if mode == "isolated" {
		if err := x.e.SetMode(id, "BTC-PERP", "isolated"); err != nil {
			t.Fatal(err)
		}
	}
	if err := x.e.SetLeverage(id, "BTC-PERP", 20); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ { // 市价单一张最多 120 BTC（MARKET_LOT_SIZE），分三张
		if _, err := x.e.Place(id, buy("BTC-PERP", "100")); err != nil {
			t.Fatal(err)
		}
	}
}

func liqPx(t *testing.T, x *env, id int64) decimal.Decimal {
	t.Helper()
	v, _ := x.e.View(id)
	if len(v.Positions) != 1 || v.Positions[0].LiqPx.Sign() <= 0 {
		t.Fatalf("应该有一个带强平价的仓位：%+v", v.Positions)
	}
	return v.Positions[0].LiqPx
}

func lastLiq(a *Account) *Notice {
	for i := len(a.Notices) - 1; i >= 0; i-- {
		if a.Notices[i].Kind == "liquidation" {
			return &a.Notices[i]
		}
	}
	return nil
}

// D103：大仓位刚过强平价时，先平掉"刚好够"的一部分，剩下的仓位还在、保证金够维持；
// 清算费只按平掉的那部分收；平得再少一个最小单位就不够（数量是最小的）。
func TestPartialLiquidationCross(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	bigPos(t, x, 2, "1000000", "cross")
	lp := liqPx(t, x, 2)
	mark := lp.Sub(D("1")).Round(1)
	x.market("BTC-PERP", mark.String())
	x.e.Tick()
	a := x.e.accounts[2]
	p := a.Pos["BTC-PERP"]
	if p.Qty.IsZero() || !p.Qty.LessThan(D("300")) {
		t.Fatalf("应部分强平：剩下 %s", p.Qty)
	}
	n := lastLiq(a)
	if n == nil || n.Reason != "partial" || !n.Qty.Equal(D("300").Sub(p.Qty)) {
		t.Fatalf("强平提示应写明部分强平和平掉的数量：%+v", n)
	}
	cut := n.Qty
	eq(t, "清算费只按平掉的部分收", n.Amount, cut.Mul(mark).Mul(D("0.0125")))
	m := x.e.compute(a, x.e.marks())
	if !m.crossWB.Add(m.crossUPnL).GreaterThan(m.crossMM) {
		t.Fatalf("部分强平后保证金应该够：余额 %s 维持 %s", m.crossWB.Add(m.crossUPnL), m.crossMM)
	}
	// 最小：少平一个最小单位就不够
	spec, _ := x.e.Specs.Get("BTC-PERP")
	bal := D("1000000").Sub(D("300").Mul(D("60000")).Mul(DefaultTaker)).Add(D("300").Mul(mark.Sub(D("60000"))))
	less := cut.Sub(spec.Step)
	if g := bal.Sub(less.Mul(mark).Mul(D("0.0125"))).Sub(spec.MaintMargin(mark.Mul(D("300").Sub(less)))); g.Sign() > 0 {
		t.Fatalf("平掉 %s 就已经够了（%s），%s 不是最小的", less, g, cut)
	}
	if !cut.LessThan(D("150")) {
		t.Fatalf("刚过强平价只应平掉一小部分，平了 %s", cut)
	}
	if err := x.e.Check(); err != nil {
		t.Fatal(err)
	}
	// 再跌下去：继续部分强平，最后全部强平
	for i := 0; i < 40 && !a.Pos["BTC-PERP"].Qty.IsZero(); i++ {
		mark = mark.Mul(D("0.995")).Round(1)
		x.market("BTC-PERP", mark.String())
		x.e.Tick()
	}
	if !a.Pos["BTC-PERP"].Qty.IsZero() {
		t.Fatalf("一路跌下去应该最后全部强平，还剩 %s", a.Pos["BTC-PERP"].Qty)
	}
	if err := x.e.Check(); err != nil {
		t.Fatal(err)
	}
}

// 小仓位（第 1 档，维持保证金率 0.4% 比清算费率 1.25% 低）：减一部分只会更不够，所以全部强平（和币安说的一致）。
func TestSmallPositionFullLiquidation(t *testing.T) {
	spec := DefaultSpecs()["BTC-PERP"]
	mark := D("54000")
	q := D("1")
	mm := spec.MaintMargin(q.Mul(mark))
	if x := partialQty(spec, q, mark, mm.Sub(D("0.01")), zero, D("0.0125")); x.Sign() != 0 {
		t.Fatalf("1 BTC 不该部分强平，算出 %s", x)
	}
	// 引擎里：1 BTC、10 倍，跌穿强平价后整个仓位都没了
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	x.place(buy("BTC-PERP", "1"))
	lp := liqPx(t, x, 1)
	x.market("BTC-PERP", lp.Sub(D("1")).Round(1).String())
	x.e.Tick()
	if !x.e.accounts[1].Pos["BTC-PERP"].Qty.IsZero() {
		t.Fatal("小仓位应全部强平")
	}
	if n := lastLiq(x.e.accounts[1]); n == nil || n.Reason == "partial" {
		t.Fatalf("应是全部强平：%+v", n)
	}
}

// 逐仓部分强平：平仓亏损和清算费都从这个仓位的逐仓保证金里出，全仓那边的钱不动；账本对得上；重放一样。
func TestPartialLiquidationIsolated(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	bigPos(t, x, 3, "5000000", "isolated")
	a := x.e.accounts[3]
	p := a.Pos["BTC-PERP"]
	crossBefore := a.Wallet.Sub(p.IsoMargin)
	iso0, entry := p.IsoMargin, p.Entry
	lp := liqPx(t, x, 3)
	mark := lp.Sub(D("1")).Round(1)
	x.market("BTC-PERP", mark.String())
	x.e.Tick()
	p = a.Pos["BTC-PERP"]
	n := lastLiq(a)
	if p.Qty.IsZero() || n == nil || n.Reason != "partial" {
		t.Fatalf("逐仓大仓位应部分强平：剩 %s，提示 %+v", p.Qty, n)
	}
	cut := n.Qty
	eq(t, "全仓那边的钱不动", a.Wallet.Sub(p.IsoMargin), crossBefore)
	eq(t, "剩下的逐仓保证金 = 原来的 + 平仓盈亏 − 清算费", p.IsoMargin, iso0.Add(cut.Mul(mark.Sub(entry))).Sub(cut.Mul(mark).Mul(D("0.0125"))))
	spec, _ := x.e.Specs.Get("BTC-PERP")
	if !p.IsoMargin.Add(upnl(p, mark)).GreaterThan(spec.MaintMargin(p.Qty.Abs().Mul(mark))) {
		t.Fatal("部分强平后逐仓保证金应该够")
	}
	if err := x.e.Check(); err != nil {
		t.Fatal(err)
	}
	x.e.Flush()
	raw, _ := json.Marshal(x.evs)
	var evs []Event
	_ = json.Unmarshal(raw, &evs)
	y := New(x.books, V, NewSpecs(LooseSpecs()), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := y.Restore(evs); err != nil {
		t.Fatal(err)
	}
	b1, _ := json.Marshal(x.e.state(3))
	b2, _ := json.Marshal(y.state(3))
	if string(b1) != string(b2) {
		t.Fatalf("重放后不一样\n前 %s\n后 %s", b1, b2)
	}
}
