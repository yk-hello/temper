package trading

// 模拟盘审查（2026-11-07）：把查到的问题写成能重放的场景。
// 修好的（第 1、5、6 条等，见 docs/16）已经改成断言；还没修的只打日志，记录现状。
//   go test -run SimAudit -v ./internal/trading

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"qs/internal/book"
)

// 审查 1（已修）：挂单被一笔很小的成交穿过，只成交这笔成交的量，不是全部。
func TestSimAudit1_SizeFantasy(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "100000")
	_ = x.e.SetLeverage(1, "BTC-PERP", 100)
	o := x.place(limit("BTC-PERP", "buy", "99990", "7.5", GTC))  // 约 75 万美元
	x.e.onTrade(V, "BTC-PERP", D("99989"), D("0.001"), false, 0) // 市场上只成交了 0.001 BTC
	v, _ := x.e.View(1)
	t.Logf("挂单 %s BTC，市场只成交 0.001 BTC 穿过去：我们成交了 %s BTC", o.Qty, v.Positions[0].Qty)
	if !v.Positions[0].Qty.Equal(D("0.001")) {
		t.Fatalf("应该只成交 0.001：%s", v.Positions[0].Qty)
	}
}

// 审查 2（已修）：恰好在挂单价成交时，先吃前面排着的。
func TestSimAudit2_NoQueue(t *testing.T) {
	x := newEnv(t)
	x.setBook("BTC-PERP", []book.Level{{Px: D("99999"), Qty: D("50")}}, []book.Level{{Px: D("100000"), Qty: D("50")}})
	x.books.UpdateTicker(V, "BTC-PERP", func(k *book.Ticker) { k.Mark = D("100000"); k.Last = D("100000") })
	x.place(limit("BTC-PERP", "buy", "99999", "0.05", GTC)) // 排在 50 BTC 后面
	x.e.onTrade(V, "BTC-PERP", D("99999"), D("0.1"), false, 0)
	v, _ := x.e.View(1)
	if len(v.Positions) != 0 {
		t.Fatalf("前面 50 BTC 没吃完，不该成交：%+v", v.Positions)
	}
	x.e.onTrade(V, "BTC-PERP", D("99999"), D("49.93"), false, 0) // 前面还剩 49.9，吃完给我们 0.03
	v, _ = x.e.View(1)
	if len(v.Positions) != 1 || !v.Positions[0].Qty.Equal(D("0.03")) {
		t.Fatalf("排到后应成交 0.03：%+v", v.Positions)
	}
}

// 审查 3：吃单不消耗盘口。两个账户、连续下单，都吃同一档。
func TestSimAudit3_BookNotDepleted(t *testing.T) {
	x := newEnv(t)
	x.e.Open(2, D("10000"))
	x.setBook("BTC-PERP", []book.Level{{Px: D("99999"), Qty: D("1")}}, []book.Level{{Px: D("100000"), Qty: D("0.01")}, {Px: D("100100"), Qty: D("100")}})
	x.books.UpdateTicker(V, "BTC-PERP", func(k *book.Ticker) { k.Mark = D("100000"); k.Last = D("100000") })
	cost := decimal.Zero
	for i := 0; i < 5; i++ {
		o := x.place(buy("BTC-PERP", "0.01"))
		cost = cost.Add(o.AvgPx.Mul(D("0.01")))
	}
	y, err := x.e.Place(2, buy("BTC-PERP", "0.05"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("卖一只有 0.01 BTC。账户 1 分 5 次买，每次都成交在 100000（均价 %s）；一次买 0.05 的均价是 %s。同一份流动性被吃了 5 遍，还能再被别的账户吃", cost.Div(D("0.05")).Round(2), y.AvgPx.Round(2))
}

// 审查 4：盘口冻结 5 秒内照样按旧价成交。
func TestSimAudit4_StaleBook(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "100000")
	b, _ := x.books.Book(V, "BTC-PERP")
	for _, age := range []time.Duration{1 * time.Second, 4900 * time.Millisecond, 5100 * time.Millisecond} {
		x.books.SetBookAt(b, time.Now().Add(-age).UnixMilli())
		_, err := x.e.Place(1, buy("BTC-PERP", "0.001"))
		t.Logf("盘口 %v 没更新：下单结果 %v", age, err)
	}
}

// 审查 5（记账已修）：两个账户一多一空，跳空时亏的一边只亏本金（穿仓部分系统补），赚的一边全拿。
// 不学交易所做自动减仓，改成记下系统补了多少：成绩里扣回去，风险页标出对冲账户。
func TestSimAudit5_HedgedPairGap(t *testing.T) {
	x := newEnv(t)
	x.e.Open(2, D("10000"))
	x.market("BTC-PERP", "100000")
	for _, a := range []int64{1, 2} {
		if err := x.e.SetLeverage(a, "BTC-PERP", 100); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := x.e.Place(1, buy("BTC-PERP", "7.5")); err != nil {
		t.Fatal(err)
	}
	if _, err := x.e.Place(2, sell("BTC-PERP", "7.5")); err != nil {
		t.Fatal(err)
	}
	eq0a, _ := x.e.View(1)
	eq0b, _ := x.e.View(2)
	x.market("BTC-PERP", "98000") // 跳空 2%：断网恢复时很常见
	x.e.Tick()
	a, _ := x.e.View(1)
	b, _ := x.e.View(2)
	t.Logf("开仓后：多 %s，空 %s，合计 %s", eq0a.Equity.Round(2), eq0b.Equity.Round(2), eq0a.Equity.Add(eq0b.Equity).Round(2))
	t.Logf("跳空 2%% 后：多头账户 %s（被强平），空头账户 %s，合计 %s", a.Equity.Round(2), b.Equity.Round(2), a.Equity.Add(b.Equity).Round(2))
	gain := a.Equity.Add(b.Equity).Sub(eq0a.Equity.Add(eq0b.Equity))
	real := gain.Sub(a.Covered).Sub(b.Covered)
	t.Logf("两个账户合起来净赚 %s，其中系统补的穿仓 %s。扣回去以后 %s", gain.Round(2), a.Covered.Add(b.Covered).Round(2), real.Round(2))
	if !a.Covered.IsPositive() || real.IsPositive() {
		t.Fatalf("穿仓要记下来，扣回去以后不该赚钱：补了 %s，扣完 %s", a.Covered, real)
	}
}

// 审查 6（已修）：未实现盈利不能转走，钱包不能转成负数（和币安一样）。
func TestSimAudit6_TransferUnrealized(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "100000")
	_ = x.e.SetLeverage(1, "BTC-PERP", 100)
	if _, err := x.e.Place(1, buy("BTC-PERP", "7.5")); err != nil {
		t.Fatal(err)
	}
	x.market("BTC-PERP", "103000") // 涨 3%，未实现盈利约 2.2 万
	v, _ := x.e.View(1)
	if !v.Transferable.LessThanOrEqual(v.Wallet) || !v.Transferable.LessThanOrEqual(v.Available.Sub(v.UPnL).Add(D("0.01"))) {
		t.Fatalf("能转出 %s，钱包 %s，可用 %s，浮盈 %s：浮盈不该算进去", v.Transferable, v.Wallet, v.Available, v.UPnL)
	}
	err := x.e.Transfer(1, v.Available.Round(2).Neg())
	if r, ok := err.(*Reject); !ok || r.Code != "TRANSFER_LIMIT" {
		t.Fatalf("按可用（含浮盈）%s 转出应该被拒：%v", v.Available.Round(2), err)
	}
	if v.Transferable.Sign() > 0 {
		if err := x.e.Transfer(1, v.Transferable.Neg()); err != nil {
			t.Fatalf("上限以内应该能转：%v", err)
		}
	}
	after, _ := x.e.View(1)
	if after.Wallet.IsNegative() {
		t.Fatalf("钱包转成负数：%s", after.Wallet)
	}
	t.Logf("钱包 %s，浮盈 %s，可用 %s，最多能转 %s。转完钱包 %s", v.Wallet.Round(2), v.UPnL.Round(2), v.Available.Round(2), v.Transferable.Round(2), after.Wallet.Round(2))
}

// 审查 7（已修）：改单加大时也走纪律锁的预检。以前先下小单过检查，再改大就绕过去了。
func TestSimAudit7_AmendSkipsGuard(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "100000")
	calls := 0
	x.e.Guard = func(p PreOrder) error {
		calls++
		risk := p.Price.Sub(p.Stop).Abs().Mul(p.Qty)
		if p.Stop.Sign() > 0 && risk.GreaterThan(p.Equity.Mul(D("0.01"))) {
			return NewReject("DISCIPLINE_RISK")
		}
		return nil
	}
	big := limit("BTC-PERP", "buy", "99000", "0.5", GTC)
	big.SL = D("98000") // 风险 500，超过 1%（100）
	_, err := x.e.Place(1, big)
	t.Logf("直接下大单：%v（预检次数 %d）", err, calls)
	small := limit("BTC-PERP", "buy", "99000", "0.05", GTC)
	small.SL = D("98000") // 风险 50，能过
	o, err := x.e.Place(1, small)
	if err != nil {
		t.Fatal(err)
	}
	before := calls
	_, err = x.e.Amend(1, o.ID, zero, D("0.5"), zero)
	if r, ok := err.(*Reject); !ok || r.Code != "DISCIPLINE_RISK" {
		t.Fatalf("改大到 0.5 应该被预检拦下：%v", err)
	}
	if calls == before {
		t.Fatal("改单没有走预检")
	}
	if _, err := x.e.Amend(1, o.ID, zero, D("0.03"), zero); err != nil {
		t.Fatalf("改小不该被拦：%v", err)
	}
}

// 审查 8（已修）：最大仓位算上同方向已经挂着的单。以前只看这一张，挂多张一起成交就超了。
func TestSimAudit8_PositionLimitStacked(t *testing.T) {
	x := newEnv(t)
	x.e.Open(3, D("10000000"))
	x.market("BTC-PERP", "100000")
	spec, _ := x.e.Specs.Get("BTC-PERP")
	_ = x.e.SetLeverage(3, "BTC-PERP", 100)
	mx, ok := maxNotionalFor(spec, 100)
	if !ok {
		t.Skip("这个杠杆没有上限")
	}
	qty := mx.Div(D("99000")).Mul(D("0.9")).Round(3)
	r := limit("BTC-PERP", "buy", "99000", qty.String(), GTC)
	placed := 0
	for i := 0; i < 5; i++ {
		if _, err := x.e.Place(3, r); err == nil {
			placed++
		}
	}
	x.e.onTrade(V, "BTC-PERP", D("98999"), D("100"), false, 0)
	v, _ := x.e.View(3)
	n := decimal.Zero
	if len(v.Positions) > 0 {
		n = v.Positions[0].Qty.Mul(D("99000"))
	}
	t.Logf("100 倍最大仓位 %s 美元。每张单是限额的 90%%，挂上了 %d 张，一起成交后仓位 %s 美元", mx, placed, n.Round(0))
	if placed != 1 || n.GreaterThan(mx) {
		t.Fatalf("应该只挂得上 1 张、仓位不超过上限：挂了 %d 张，仓位 %s", placed, n)
	}
}

// 审查 9（已修）：断流期间价格穿过止损和强平价又回来，恢复后按这段的 1 分钟 K 线补算。
func TestSimAudit9_OutageHidesLoss(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "100000")
	_ = x.e.SetLeverage(1, "BTC-PERP", 20)
	r := buy("BTC-PERP", "1")
	r.SL = D("98000")
	if _, err := x.e.Place(1, r); err != nil {
		t.Fatal(err)
	}
	// 断流：这期间真实价格跌到 90000（穿过止损，也穿过强平价）又涨回来
	from := x.now.UnixMilli() + 1000
	bar := Bar1m{T: from - from%60_000, O: D("100000"), H: D("100100"), L: D("90000")}
	x.market("BTC-PERP", "100500") // 恢复时已经回来了
	n := x.e.ReplayBars("BTC-PERP", from, from+120_000, []Bar1m{bar}, nil)
	v, _ := x.e.View(1)
	if n != 1 || len(v.Positions) != 0 {
		t.Fatalf("应该补一笔止损、仓位平掉：补了 %d，仓位 %+v", n, v.Positions)
	}
	var got *Notice
	for i := range v.Notices {
		if v.Notices[i].Kind == "gap_replay" {
			got = &v.Notices[i]
		}
	}
	if got == nil || got.Reason != "sl" || !got.Px.Equal(D("98000")) {
		t.Fatalf("要留一条补算提醒，按止损价 98000 成交：%+v", got)
	}
	t.Logf("断流期间价格到过 90000：止损先到，按 98000 补成交，权益 %s", v.Equity.Round(2))
}
