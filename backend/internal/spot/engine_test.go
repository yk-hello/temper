package spot

import (
	"testing"

	"github.com/shopspring/decimal"

	"qs/internal/book"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

type env struct {
	e   *Engine
	now int64
}

func newEnv(t *testing.T) *env {
	t.Helper()
	x := &env{now: 1_700_000_000_000}
	x.e = New(book.NewStore(), "v", nil)
	x.e.Now = func() int64 { return x.now }
	x.setBook("100", "101")
	if err := x.e.Transfer(1, d("1000")); err != nil {
		t.Fatal(err)
	}
	return x
}

func (x *env) setBook(bid, ask string) {
	x.e.Books.SetBook(book.Book{Venue: "v", Instrument: "BTC-SPOT", Time: x.now,
		Bids: []book.Level{{Px: d(bid), Qty: d("5")}}, Asks: []book.Level{{Px: d(ask), Qty: d("5")}}})
}

func (x *env) bal(coin string) *Balance { return x.e.accounts[1].bal(coin) }

func eq(t *testing.T, got decimal.Decimal, want, what string) {
	t.Helper()
	if !got.Equal(d(want)) {
		t.Fatalf("%s：应为 %s，实际 %s", what, want, got)
	}
}

// 市价买：按卖一成交，手续费从买到的币里扣；USDT 不扣手续费
func TestMarketBuy(t *testing.T) {
	x := newEnv(t)
	o, err := x.e.Place(1, OrderReq{Instrument: "BTC-SPOT", Side: "buy", Type: Market, Qty: d("1")})
	if err != nil {
		t.Fatal(err)
	}
	if o.Status != StFilled {
		t.Fatalf("状态：%s", o.Status)
	}
	eq(t, x.bal("USDT").Free, "899", "花掉 101")
	eq(t, x.bal("BTC").Free, "0.999", "买到 1 个，扣 0.1%")
	eq(t, x.bal("BTC").Cost, "101.1011011011011011", "成本把手续费算进去")
}

// 市价卖：手续费从 USDT 里扣；盈亏按平均成本算
func TestMarketSell(t *testing.T) {
	x := newEnv(t)
	_, _ = x.e.Place(1, OrderReq{Instrument: "BTC-SPOT", Side: "buy", Type: Market, Qty: d("1")})
	x.setBook("110", "111")
	o, err := x.e.Place(1, OrderReq{Instrument: "BTC-SPOT", Side: "sell", Type: Market, Qty: d("0.5")})
	if err != nil || o.Status != StFilled {
		t.Fatal(err, o)
	}
	eq(t, x.bal("USDT").Free, "953.945", "899 + 55 − 0.055")
	eq(t, x.bal("BTC").Free, "0.499", "还剩")
	if f := x.e.accounts[1].Fills; f[len(f)-1].Realized.LessThanOrEqual(zero) {
		t.Fatal("盈亏算错", f[len(f)-1].Realized)
	}
}

// 限价买：挂单锁钱；撤单退回；盘口越过时按限价成交
func TestLimitLockAndFill(t *testing.T) {
	x := newEnv(t)
	o, err := x.e.Place(1, OrderReq{Instrument: "BTC-SPOT", Side: "buy", Type: Limit, Price: d("95"), Qty: d("2")})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, x.bal("USDT").Free, "810", "锁 190")
	eq(t, x.bal("USDT").Locked, "190", "锁 190")
	if _, err := x.e.Cancel(1, o.ID, ""); err != nil {
		t.Fatal(err)
	}
	eq(t, x.bal("USDT").Free, "1000", "撤单退回")
	eq(t, x.bal("USDT").Locked, "0", "撤单退回")
	o2, _ := x.e.Place(1, OrderReq{Instrument: "BTC-SPOT", Side: "buy", Type: Limit, Price: d("95"), Qty: d("2")})
	x.setBook("93", "94")
	x.e.Tick()
	if x.e.OrderOf(1, o2.ID).Status != StFilled {
		t.Fatal("盘口越过要成交")
	}
	eq(t, x.bal("USDT").Free, "810", "按限价成交")
	eq(t, x.bal("USDT").Locked, "0", "锁的钱用掉了")
	eq(t, x.bal("BTC").Free, "1.998", "扣手续费")
	if err := x.e.Check(); err != nil {
		t.Fatal(err)
	}
}

// 限价买穿过卖一：按卖一成交，差额退回
func TestLimitTakesAndRefunds(t *testing.T) {
	x := newEnv(t)
	o, err := x.e.Place(1, OrderReq{Instrument: "BTC-SPOT", Side: "buy", Type: Limit, Price: d("105"), Qty: d("1")})
	if err != nil || o.Status != StFilled {
		t.Fatal(err, o)
	}
	eq(t, x.bal("USDT").Free, "899", "按 101 成交，多锁的退回")
	eq(t, x.bal("USDT").Locked, "0", "不留锁")
}

func TestRejections(t *testing.T) {
	x := newEnv(t)
	_ = x.e.Transfer(1, d("-600")) // 还剩 400
	cases := []struct {
		r    OrderReq
		code string
	}{
		{OrderReq{Instrument: "ADA-SPOT", Side: "buy", Type: Market, Qty: d("1")}, "UNKNOWN_INSTRUMENT"},
		{OrderReq{Instrument: "BTC-SPOT", Side: "buy", Type: Market, Qty: d("5")}, "INSUFFICIENT_BALANCE"},
		{OrderReq{Instrument: "BTC-SPOT", Side: "sell", Type: Market, Qty: d("1")}, "INSUFFICIENT_BALANCE"},
		{OrderReq{Instrument: "BTC-SPOT", Side: "buy", Type: Limit, Price: d("95.001"), Qty: d("1")}, "BAD_TICK"},
		{OrderReq{Instrument: "BTC-SPOT", Side: "buy", Type: Limit, Price: d("95"), Qty: d("0.01")}, "MIN_NOTIONAL"},
		{OrderReq{Instrument: "BTC-SPOT", Side: "buy", Type: Limit, TIF: GTX, Price: d("102"), Qty: d("1")}, "WOULD_TAKE"},
		{OrderReq{Instrument: "BTC-SPOT", Side: "buy", Type: Limit, Price: d("95"), Qty: d("100")}, "INSUFFICIENT_BALANCE"},
	}
	for _, c := range cases {
		_, err := x.e.Place(1, c.r)
		re, ok := err.(*Rej)
		if !ok || re.Code != c.code {
			t.Fatalf("%+v：应为 %s，实际 %v", c.r, c.code, err)
		}
	}
	eq(t, x.bal("USDT").Free, "400", "拒单不动钱")
}

// 条件单：到价下市价；钱不够就撤，写原因
func TestCond(t *testing.T) {
	x := newEnv(t)
	o, err := x.e.Place(1, OrderReq{Instrument: "BTC-SPOT", Side: "buy", Type: Cond, TriggerPx: d("110"), Qty: d("1")})
	if err != nil {
		t.Fatal(err)
	}
	x.e.Tick()
	if x.e.OrderOf(1, o.ID).Status != StNew {
		t.Fatal("没到价不触发")
	}
	x.setBook("110", "111")
	x.e.Tick()
	if x.e.OrderOf(1, o.ID).Status != StFilled {
		t.Fatal("到价要成交")
	}
	big, err := x.e.Place(1, OrderReq{Instrument: "BTC-SPOT", Side: "buy", Type: Cond, TriggerPx: d("120"), Qty: d("4.9")})
	if err != nil {
		t.Fatal(err)
	}
	_ = x.e.Transfer(1, d("-800"))
	x.setBook("120", "121")
	x.e.Tick()
	if got := x.e.OrderOf(1, big.ID); got.Status != StCanceled || got.Reason != "INSUFFICIENT_BALANCE" {
		t.Fatalf("钱不够要撤：%+v", got)
	}
}

// 重放得到同样的状态
func TestRestore(t *testing.T) {
	x := newEnv(t)
	var evs []Event
	x.e.Persist = func(l []Event) error { evs = append(evs, l...); return nil }
	_, _ = x.e.Place(1, OrderReq{Instrument: "BTC-SPOT", Side: "buy", Type: Market, Qty: d("1")})
	_, _ = x.e.Place(1, OrderReq{Instrument: "BTC-SPOT", Side: "buy", Type: Limit, Price: d("95"), Qty: d("1")})
	_ = x.e.Flush()
	y := New(book.NewStore(), "v", nil)
	y.Restore(evs)
	a, b := x.e.accounts[1], y.accounts[1]
	if !a.Bal["USDT"].Free.Equal(b.Bal["USDT"].Free) || !a.Bal["USDT"].Locked.Equal(b.Bal["USDT"].Locked) || !a.Bal["BTC"].Free.Equal(b.Bal["BTC"].Free) || len(b.Orders) != 1 {
		t.Fatalf("重放不一致：%+v %+v", a.Bal["USDT"], b.Bal["USDT"])
	}
	if y.seq != x.e.seq {
		t.Fatal("序号要接上")
	}
}

// 盘口过期不成交
func TestStaleBook(t *testing.T) {
	x := newEnv(t)
	x.now += 6000
	if _, err := x.e.Place(1, OrderReq{Instrument: "BTC-SPOT", Side: "buy", Type: Market, Qty: d("1")}); err == nil || err.(*Rej).Code != "STALE_BOOK" {
		t.Fatal("过期盘口不下单", err)
	}
}
