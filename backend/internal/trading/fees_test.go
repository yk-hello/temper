package trading

import (
	"encoding/json"
	"io"
	"log/slog"
	"testing"
)

// D103：每个模拟账户可以自己定手续费率，只影响以后的成交；重放后还在；重置时带到新账户；可以回到默认。
func TestAccountFees(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	// 默认：币安普通用户 0.05%
	x.place(buy("BTC-PERP", "0.1"))
	eq(t, "默认吃单费", x.e.accounts[1].Fees, D("3")) // 6000 × 0.05%
	if err := x.e.SetFees(1, D("0.0004"), D("0.0001"), false); err != nil {
		t.Fatal(err)
	}
	x.place(sell("BTC-PERP", "0.1"))
	// 卖在买一 59999.9（盘口 tick 0.1）：5999.99 × 0.04% ≈ 2.4；累计 ≈ 5.4
	if f := x.e.accounts[1].Fees; f.LessThan(D("5.39")) || f.GreaterThan(D("5.41")) {
		t.Fatalf("改费率后累计手续费 %s，应约 5.4", f)
	}
	pv, err := x.e.Preview(1, buy("BTC-PERP", "1"))
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "预览按新费率", pv.Fee, D("24")) // 60000 × 0.04%
	v, _ := x.e.View(1)
	if !v.CustomFee || !v.TakerFee.Equal(D("0.0004")) || !v.MakerFee.Equal(D("0.0001")) {
		t.Fatalf("账户视图里的费率 %+v", v)
	}
	// 超过 1% 或负数：拒绝
	if err := x.e.SetFees(1, D("0.02"), D("0"), false); err == nil {
		t.Fatal("2% 应该被拒")
	}
	if err := x.e.SetFees(1, D("0.0005"), D("-0.0001"), false); err == nil {
		t.Fatal("负费率应该被拒")
	}
	// 重置：新账户带上旧账户的费率
	x.e.Open(2, D("500"))
	x.e.CopyFees(1, 2)
	if tk, mk, c := x.e.FeesOf(2); !c || !tk.Equal(D("0.0004")) || !mk.Equal(D("0.0001")) {
		t.Fatalf("重置后费率 %s %s %v", tk, mk, c)
	}
	// 重放
	x.e.Flush()
	raw, _ := json.Marshal(x.evs)
	var evs []Event
	if err := json.Unmarshal(raw, &evs); err != nil {
		t.Fatal(err)
	}
	y := New(x.books, V, NewSpecs(LooseSpecs()), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := y.Restore(evs); err != nil {
		t.Fatal(err)
	}
	if tk, _, c := y.FeesOf(1); !c || !tk.Equal(D("0.0004")) {
		t.Fatalf("重放后费率 %s %v", tk, c)
	}
	if err := y.Check(); err != nil {
		t.Fatal(err)
	}
	// 回到默认
	if err := x.e.SetFees(1, zero, zero, true); err != nil {
		t.Fatal(err)
	}
	if tk, mk, c := x.e.FeesOf(1); c || !tk.Equal(DefaultTaker) || !mk.Equal(DefaultMaker) {
		t.Fatalf("回到默认后 %s %s %v", tk, mk, c)
	}
}

// D103：随时加减模拟资金。本金跟着变（加进来的钱不算赚的），划转净额也记上（纪律不当成盈亏）；
// 减的时候占着的保证金、浮盈不能拿，本金不能减到 0；账本对得上；重放后一样。
func TestFund(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	x.place(buy("BTC-PERP", "1")) // 10 倍全仓：占 6000 保证金
	v0, _ := x.e.View(1)
	pnl0 := v0.Equity.Sub(v0.Initial)
	if err := x.e.Fund(1, D("5000")); err != nil {
		t.Fatal(err)
	}
	v1, _ := x.e.View(1)
	eq(t, "加 5000 后本金", v1.Initial, D("15000"))
	eq(t, "加 5000 后权益", v1.Equity, v0.Equity.Add(D("5000")))
	eq(t, "盈亏不变", v1.Equity.Sub(v1.Initial), pnl0)
	eq(t, "划转净额", x.e.accounts[1].Transfers, D("5000"))
	// 减：最多拿走可转出的部分
	mx := transferable(x.e.compute(x.e.accounts[1], x.e.marks()))
	if err := x.e.Fund(1, mx.Add(D("1")).Neg()); err == nil || err.(*Reject).Code != "FUND_LIMIT" {
		t.Fatalf("超过可转出应拒绝 FUND_LIMIT：%v", err)
	}
	if err := x.e.Fund(1, D("-3000")); err != nil {
		t.Fatal(err)
	}
	eq(t, "减 3000 后本金", x.e.accounts[1].Initial, D("12000"))
	if err := x.e.Fund(1, zero); err == nil {
		t.Fatal("0 应拒绝")
	}
	// 本金不能减到 0：没仓位时可转出的就是全部钱包，也要留一点
	x.e.Open(3, D("100"))
	if err := x.e.Fund(3, D("-100")); err == nil {
		t.Fatal("本金减到 0 应拒绝")
	}
	if err := x.e.Fund(3, D("-99.99")); err != nil {
		t.Fatal(err)
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
	eq(t, "重放后本金", y.accounts[1].Initial, D("12000"))
	eq(t, "重放后钱包", y.accounts[1].Wallet, x.e.accounts[1].Wallet)
	if err := y.Check(); err != nil {
		t.Fatal(err)
	}
}
