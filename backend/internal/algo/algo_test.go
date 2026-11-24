package algo

import (
	"io"
	"log/slog"
	"testing"

	"github.com/shopspring/decimal"

	"qs/internal/book"
	"qs/internal/trading"
)

const V = "binance-usdm"

func D(s string) decimal.Decimal { return decimal.RequireFromString(s) }

type env struct {
	books *book.Store
	e     *trading.Engine
	m     *Manager
	now   int64
}

func newEnv(t *testing.T) *env {
	x := &env{books: book.NewStore(), now: 1_794_300_000_000}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	x.e = trading.New(x.books, V, trading.NewSpecs(trading.LooseSpecs()), log)
	x.e.Open(1, D("100000"))
	x.m = New(x.e, x.books, V, log)
	x.m.Now = func() int64 { return x.now }
	x.books.SetBook(book.Book{Venue: V, Instrument: "BTC-PERP", Bids: []book.Level{{Px: D("59999.9"), Qty: D("1000")}}, Asks: []book.Level{{Px: D("60000"), Qty: D("1000")}}})
	x.books.UpdateTicker(V, "BTC-PERP", func(t *book.Ticker) { t.Mark = D("60000"); t.Last = D("60000") })
	return x
}

func (x *env) open(t *testing.T) []*trading.Order {
	t.Helper()
	v, err := x.e.View(1)
	if err != nil {
		t.Fatal(err)
	}
	return v.Orders
}

func (x *env) get(id int64) Algo {
	for _, a := range x.m.List(1) {
		if a.ID == id {
			return a
		}
	}
	return Algo{}
}

// 冰山单（18 第 9.5 条）：盘口上一次只挂一张，成交完挂下一张；最后一张带上零头；挂着的那张被撤了整单停下。
func TestIceberg(t *testing.T) {
	x := newEnv(t)
	if _, err := x.m.StartIceberg(1, 1, "BTC-PERP", "buy", D("0.25"), D("59900"), 1, false); err == nil || codeOf(err) != "ALGO_ICE_COUNT" {
		t.Fatalf("一张不叫冰山：%v", err)
	}
	if _, err := x.m.StartIceberg(1, 1, "BTC-PERP", "buy", D("0.0015"), D("59900"), 3, false); err == nil || codeOf(err) != "ALGO_ICE_TOO_SMALL" {
		t.Fatalf("每张不到最小金额：%v", err)
	}
	a, err := x.m.StartIceberg(1, 1, "BTC-PERP", "buy", D("0.25"), D("59900"), 3, false)
	if err != nil {
		t.Fatal(err)
	}
	var sizes []string
	for i := 0; i < 5 && x.get(a.ID).Status == StRunning; i++ {
		o := x.open(t)
		if len(o) != 1 {
			t.Fatalf("第 %d 张时盘口上应该只有一张：%d", i+1, len(o))
		}
		sizes = append(sizes, o[0].Qty.String())
		x.books.Trade(V, "BTC-PERP", D("59899"), D("5"), false, 0) // 市场卖穿 59900：这一张成交
		x.m.Tick()
	}
	g := x.get(a.ID)
	if g.Status != StDone || !g.Filled.Equal(D("0.25")) || g.Done != 3 {
		t.Fatalf("应该三张成交完：%+v", g)
	}
	if want := "0.083 0.083 0.084"; join(sizes) != want {
		t.Fatalf("每张：%s，应该是 %s", join(sizes), want)
	}
	if len(x.open(t)) != 0 {
		t.Fatal("做完以后不该还有挂单")
	}

	// 挂着的那张被手动撤了：整单停下
	b, _ := x.m.StartIceberg(1, 1, "BTC-PERP", "buy", D("0.2"), D("59800"), 4, false)
	o := x.open(t)
	if _, err := x.e.CancelAs(1, o[0].ID, "USER"); err != nil {
		t.Fatal(err)
	}
	x.m.Tick()
	if g := x.get(b.ID); g.Status != StStopped || g.Reason != "ORDER_CANCELED" {
		t.Fatalf("撤了挂着的那张：%+v", g)
	}
	// 取消冰山单：挂着的那张一起撤
	c, _ := x.m.StartIceberg(1, 1, "BTC-PERP", "sell", D("0.2"), D("60100"), 2, false)
	if len(x.open(t)) != 1 {
		t.Fatal("卖的冰山单挂上一张")
	}
	if err := x.m.Cancel(1, 1, c.ID); err != nil {
		t.Fatal(err)
	}
	if len(x.open(t)) != 0 || x.get(c.ID).Status != StStopped {
		t.Fatal("取消冰山单要撤掉挂着的那张")
	}
}

// 按成交量比例（18 第 9.5 条）：市场成交多少，按比例跟着下；不够最小金额先攒着；超过限价的那段不跟；到时没下完就停。
func TestPOV(t *testing.T) {
	x := newEnv(t)
	if _, err := x.m.StartPOV(1, 1, "BTC-PERP", "buy", D("0.3"), D("60"), 5, decimal.Zero, false); err == nil || codeOf(err) != "ALGO_POV_PCT" {
		t.Fatalf("比例超过 50%%：%v", err)
	}
	if _, err := x.m.StartPOV(1, 1, "BTC-PERP", "buy", D("0.0005"), D("10"), 5, decimal.Zero, false); err == nil || codeOf(err) != "ALGO_POV_TOO_SMALL" {
		t.Fatalf("总数不到最小金额：%v", err)
	}
	x.books.Trade(V, "BTC-PERP", D("60000"), D("7"), true, 0) // 开始之前的成交不算
	a, err := x.m.StartPOV(1, 1, "BTC-PERP", "buy", D("0.3"), D("10"), 5, decimal.Zero, false)
	if err != nil {
		t.Fatal(err)
	}
	step := func() { x.now += 10_000; x.m.Tick() }
	pos := func() decimal.Decimal {
		v, _ := x.e.View(1)
		for _, p := range v.Positions {
			if p.Instrument == "BTC-PERP" {
				return p.Qty
			}
		}
		return decimal.Zero
	}
	x.books.Trade(V, "BTC-PERP", D("60000"), D("1"), true, 0)
	x.m.Tick() // 还不到 10 秒：不动
	if !pos().IsZero() {
		t.Fatal("不到 10 秒不该下")
	}
	step()
	if g := x.get(a.ID); !g.Filled.Equal(D("0.1")) || !g.Seen.Equal(D("1")) {
		t.Fatalf("市场成交 1，跟 10%% = 0.1：%+v", g)
	}
	// 只成交了 0.005：该跟 0.0005，不到最小金额，攒着
	x.books.Trade(V, "BTC-PERP", D("60000"), D("0.005"), false, 0)
	step()
	if g := x.get(a.ID); !g.Filled.Equal(D("0.1")) || !g.Owed.Equal(D("0.0005")) {
		t.Fatalf("不够最小金额攒着：%+v", g)
	}
	// 价格超过限价：这一段不跟，也不补
	b, _ := x.m.StartPOV(1, 1, "BTC-PERP", "buy", D("0.3"), D("10"), 5, D("59000"), false)
	x.books.Trade(V, "BTC-PERP", D("60000"), D("2"), true, 0)
	step()
	if g := x.get(b.ID); g.Skipped != 1 || !g.Filled.IsZero() || !g.Owed.IsZero() {
		t.Fatalf("超过限价不跟：%+v", g)
	}
	if g := x.get(a.ID); !g.Filled.Equal(D("0.3")) || g.Status != StDone {
		t.Fatalf("市场成交 2，跟 0.2，加上前面 0.1 正好做完：%+v", g)
	}
	// 到时间没下完：停下，写明是时间到了
	x.now += 5 * 60_000
	x.m.Tick()
	if g := x.get(b.ID); g.Status != StDone || g.Reason != "TIMEOUT" {
		t.Fatalf("到时间：%+v", g)
	}
	if !pos().Equal(D("0.3")) {
		t.Fatalf("持仓：%s", pos())
	}
}

func join(s []string) string {
	out := ""
	for i, v := range s {
		if i > 0 {
			out += " "
		}
		out += v
	}
	return out
}
