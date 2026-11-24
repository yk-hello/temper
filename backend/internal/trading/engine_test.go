package trading

// 验收标准模块 2（docs/12）：撮合、保证金、强平价（手算样例）、止盈止损、资金费、账本、重放恢复。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"qs/internal/book"
)

const V = "binance-usdm"

type env struct {
	t     *testing.T
	books *book.Store
	e     *Engine
	now   time.Time
	evs   []Event
}

func newEnv(t *testing.T) *env {
	x := &env{t: t, books: book.NewStore(), now: time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)}
	x.e = New(x.books, V, NewSpecs(LooseSpecs()), slog.New(slog.NewTextHandler(io.Discard, nil)))
	x.e.Now = func() time.Time { return x.now }
	x.e.Persist = func(evs []Event) error { x.evs = append(x.evs, evs...); return nil }
	x.e.Open(1, D("10000"))
	return x
}

func D(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func lv(pairs ...string) []book.Level {
	var out []book.Level
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, book.Level{Px: D(pairs[i]), Qty: D(pairs[i+1])})
	}
	return out
}

// market 设标记价和盘口（买一 = mark − tick，卖一 = mark，深度很厚）
func (x *env) market(inst, mark string) {
	m := D(mark)
	spec, _ := x.e.Specs.Get(inst)
	x.books.SetBook(book.Book{Venue: V, Instrument: inst, Bids: []book.Level{{Px: m.Sub(spec.Tick), Qty: D("1000")}}, Asks: []book.Level{{Px: m, Qty: D("1000")}}, Time: x.now.UnixMilli()})
	x.books.UpdateTicker(V, inst, func(t *book.Ticker) { t.Mark = m; t.Last = m })
}

func (x *env) setBook(inst string, bids, asks []book.Level) {
	x.books.SetBook(book.Book{Venue: V, Instrument: inst, Bids: bids, Asks: asks})
}

func (x *env) place(r OrderReq) *Order {
	x.t.Helper()
	o, err := x.e.Place(1, r)
	if err != nil {
		x.t.Fatalf("下单失败 %+v：%v", r, err)
	}
	return o
}

func (x *env) reject(code string, r OrderReq) *Reject {
	x.t.Helper()
	_, err := x.e.Place(1, r)
	rj, ok := err.(*Reject)
	if !ok || rj.Code != code {
		x.t.Fatalf("应被拒绝 %s，实际 %v", code, err)
	}
	return rj
}

func (x *env) acct() *Account { return x.e.accounts[1] }
func (x *env) pos(inst string) decimal.Decimal {
	if p := x.acct().Pos[inst]; p != nil {
		return p.Qty
	}
	return zero
}

func eq(t *testing.T, what string, got, want decimal.Decimal) {
	t.Helper()
	if !got.Equal(want) {
		t.Fatalf("%s = %s，应为 %s", what, got, want)
	}
}

func near(t *testing.T, what string, got, want, tol decimal.Decimal) {
	t.Helper()
	if got.Sub(want).Abs().GreaterThan(tol) {
		t.Fatalf("%s = %s，应为 %s（误差上限 %s）", what, got, want, tol)
	}
}

func buy(inst, qty string) OrderReq {
	return OrderReq{Instrument: inst, Side: "buy", Type: Market, Qty: D(qty)}
}
func sell(inst, qty string) OrderReq {
	return OrderReq{Instrument: inst, Side: "sell", Type: Market, Qty: D(qty)}
}
func limit(inst, side, px, qty, tif string) OrderReq {
	return OrderReq{Instrument: inst, Side: side, Type: Limit, Price: D(px), Qty: D(qty), TIF: tif}
}

// ---------- 2.5 强平价：手算样例 ----------

func TestLiqPriceHandSamples(t *testing.T) {
	spec := DefaultSpecs()["BTC-PERP"]
	tick := spec.Tick
	// 速算数由分档自动算出，和币安官方表一致（2026-10：第 2 档 300、第 3 档 1500、第 4 档 12000）
	eq(t, "第 2 档速算数", spec.Brackets[1].Cum, D("300"))
	eq(t, "第 3 档速算数", spec.Brackets[2].Cum, D("1500"))
	eq(t, "第 4 档速算数", spec.Brackets[3].Cum, D("12000"))
	eq(t, "第 1 档最高杠杆", D(fmt.Sprint(spec.Brackets[0].MaxLev)), D("150"))

	// 逐仓多 1 BTC @ 60000，10 倍，保证金 6000：名义 60000 在第 1 档（0.4%，速算 0）
	// LP = (6000 − 60000) ÷ (0.004 − 1) = 54216.8675…
	lp := liqPrice(spec, &Position{Qty: D("1"), Entry: D("60000")}, D("6000"), zero, zero, D("60000"))
	near(t, "逐仓多强平价", lp, D("54216.8675"), tick)
	// 逐仓空 1 BTC @ 60000：LP = (6000 + 60000) ÷ (0.004 + 1) = 65737.0518…
	lp = liqPrice(spec, &Position{Qty: D("-1"), Entry: D("60000")}, D("6000"), zero, zero, D("60000"))
	near(t, "逐仓空强平价", lp, D("65737.0518"), tick)
	// 第 2 档（和用户 2026-11-05 截图里的仓位同一档）：逐仓多 5 BTC @ 80000，名义 40 万（0.5%，速算 300），保证金 10000
	// LP = (10000 + 300 − 400000) ÷ (5×0.005 − 5) = 78331.6583…
	lp = liqPrice(spec, &Position{Qty: D("5"), Entry: D("80000")}, D("10000"), zero, zero, D("80000"))
	near(t, "第 2 档强平价", lp, D("78331.6583"), tick)
	// 小仓位在第 1 档：0.01 BTC @ 60000，20 倍，保证金 30：LP = (30 − 600) ÷ (0.01×0.004 − 0.01) = 57228.9157…
	lp = liqPrice(spec, &Position{Qty: D("0.01"), Entry: D("60000")}, D("30"), zero, zero, D("60000"))
	near(t, "第 1 档强平价", lp, D("57228.9157"), tick)
	// 验证：在强平价上，保证金 + 未实现盈亏 正好等于维持保证金
	p := &Position{Qty: D("1"), Entry: D("60000")}
	lp = liqPrice(spec, p, D("6000"), zero, zero, D("60000"))
	near(t, "强平价处的权益 − 维持保证金", D("6000").Add(upnl(p, lp)).Sub(spec.MaintMargin(lp)), zero, D("0.01"))
	// 亏不到爆仓（1 倍多单）：没有强平价
	eq(t, "1 倍多单", liqPrice(spec, &Position{Qty: D("1"), Entry: D("60000")}, D("60000"), zero, zero, D("60000")), zero)
}

func TestLiqPriceCross(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	x.market("ETH-PERP", "3000")
	_ = x.e.SetLeverage(1, "BTC-PERP", 10)
	_ = x.e.SetLeverage(1, "ETH-PERP", 10)
	x.place(buy("BTC-PERP", "0.5"))
	x.place(sell("ETH-PERP", "10"))
	// 手续费：BTC 30000×0.05% = 15；ETH 卖在买一价 2999.99：29999.9×0.05% = 14.99995 → 钱包 9970.00005
	eq(t, "钱包", x.acct().Wallet, D("9970.00005"))
	// ETH 名义 30000 在第 2 档（0.65%，速算 15）：TMM1 = 30000×0.0065 − 15 = 180；UPNL1 = 0（卖在买一价 2999.99，标记价 3000 → −0.1）
	// 这里 ETH 卖在 2999.99，UPNL1 = −10 × (3000 − 2999.99) = −0.1
	// LP = (9970 − 180 − 0.1 − 0.5×60000) ÷ (0.5×0.004 − 0.5) = 40642.37…
	want := D("9970.00005").Sub(D("180")).Sub(D("0.1")).Sub(D("30000")).Div(D("0.002").Sub(D("0.5")))
	v, _ := x.e.View(1)
	var got decimal.Decimal
	for _, p := range v.Positions {
		if p.Instrument == "BTC-PERP" {
			got = p.LiqPx
		}
	}
	near(t, "全仓强平价", got, want, D("0.1"))
}

// ---------- 2.4 保证金 ----------

func TestMarginAndFees(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	o := x.place(buy("BTC-PERP", "0.1"))
	if o.Status != StFilled || !o.AvgPx.Equal(D("60000")) {
		t.Fatalf("%+v", o)
	}
	eq(t, "手续费", o.Fee, D("3")) // 6000 × 0.05%
	v, _ := x.e.View(1)
	eq(t, "钱包", v.Wallet, D("9997"))
	eq(t, "仓位保证金", v.PositionMargin, D("600")) // 6000 ÷ 10
	eq(t, "可用", v.Available, D("9397"))
	eq(t, "维持保证金", v.MaintMargin, D("24")) // 6000 × 0.4%
	// 挂单占用保证金：限价买 0.1 @ 59000 → 590
	x.place(limit("BTC-PERP", "buy", "59000", "0.1", GTC))
	v, _ = x.e.View(1)
	eq(t, "挂单保证金", v.OrderMargin, D("590"))
	eq(t, "可用", v.Available, D("8807"))
	// 反方向挂单能抵消持仓的部分不占保证金：卖 0.1 @ 61000（平掉多仓）只要和买单两边取大
	x.place(limit("BTC-PERP", "sell", "61000", "0.1", GTC))
	v, _ = x.e.View(1)
	eq(t, "挂单保证金（取较大一边）", v.OrderMargin, D("590"))
	// 可用不够就拒单
	rj := x.reject("INSUFFICIENT_MARGIN", buy("BTC-PERP", "2"))
	if rj.Params["need"] == "" || rj.Params["available"] == "" {
		t.Fatal("拒单要说明需要多少、可用多少")
	}
	if err := x.e.Check(); err != nil {
		t.Fatal(err)
	}
}

// ---------- 2.1 订单类型 ----------

func TestOrderTypes(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	x.setBook("BTC-PERP", lv("59990", "1"), lv("60000", "0.05", "60010", "0.05", "60020", "1"))

	// IOC：吃到限价为止，剩余撤销
	o := x.place(limit("BTC-PERP", "buy", "60010", "0.2", IOC))
	if o.Status != StExpired || !o.Filled.Equal(D("0.1")) || o.Reason != "IOC_REMAINDER" {
		t.Fatalf("IOC：%+v", o)
	}
	eq(t, "IOC 均价", o.AvgPx, D("60005"))
	// FOK：不够就全撤，一点都不成交
	o = x.place(limit("BTC-PERP", "buy", "60010", "0.2", FOK))
	if o.Status != StExpired || !o.Filled.IsZero() || o.Reason != "FOK_NOT_FILLED" {
		t.Fatalf("FOK：%+v", o)
	}
	o = x.place(limit("BTC-PERP", "buy", "60020", "0.2", FOK))
	if o.Status != StFilled {
		t.Fatalf("FOK 够：%+v", o)
	}
	// Post-only：会立即成交就拒（过期），不会就挂着
	o = x.place(limit("BTC-PERP", "buy", "60000", "0.01", GTX))
	if o.Status != StExpired || o.Reason != "POST_ONLY_WOULD_TAKE" {
		t.Fatalf("GTX：%+v", o)
	}
	x.place(limit("BTC-PERP", "buy", "59000", "0.01", GTX)) // 下面会测是否挂着
	// GTC 穿价：先吃再挂
	o = x.place(limit("BTC-PERP", "buy", "60010", "0.2", GTC))
	if o.Status != StPartial || !o.Filled.Equal(D("0.1")) {
		t.Fatalf("GTC 穿价：%+v", o)
	}
	// 开空
	x.market("ETH-PERP", "3000")
	o = x.place(sell("ETH-PERP", "1"))
	if x.pos("ETH-PERP").Sign() >= 0 || o.Status != StFilled {
		t.Fatal("开空失败")
	}
	// 市价单盘口不够深：成交能成交的，剩余过期
	x.setBook("SOL-PERP", lv("149.99", "1"), lv("150", "1"))
	x.books.UpdateTicker(V, "SOL-PERP", func(tk *book.Ticker) { tk.Mark = D("150") })
	o = x.place(buy("SOL-PERP", "3"))
	if o.Status != StExpired || !o.Filled.Equal(D("1")) || o.Reason != "NO_LIQUIDITY" {
		t.Fatalf("盘口不够：%+v", o)
	}
	// 价格要在价格单位上、在价格带内
	x.reject("PRICE_TICK", limit("BTC-PERP", "buy", "59000.05", "0.01", GTC))
	rj := x.reject("PRICE_OUT_OF_RANGE", limit("BTC-PERP", "buy", "70000", "0.01", GTC))
	if rj.Params["max"] != "63000" || rj.Params["min"] != "57000" {
		t.Fatalf("价格带：%v", rj.Params)
	}
	x.reject("BAD_TIF", limit("BTC-PERP", "buy", "59000", "0.01", "GTE"))
	x.reject("GTD_TOO_SOON", limit("BTC-PERP", "buy", "59000", "0.01", GTD)) // GTD 要给有效期（D103）
	x.reject("BAD_SIDE", OrderReq{Instrument: "BTC-PERP", Side: "long", Type: Market, Qty: D("1")})
	x.reject("BAD_INSTRUMENT", buy("FOO-PERP", "1"))
}

func TestReduceOnly(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	r := sell("BTC-PERP", "0.1")
	r.ReduceOnly = true
	x.reject("REDUCE_ONLY_NO_POSITION", r)
	x.place(buy("BTC-PERP", "0.1"))
	rb := buy("BTC-PERP", "0.1")
	rb.ReduceOnly = true
	x.reject("REDUCE_ONLY_SAME_SIDE", rb)
	r.Qty = D("0.5") // 超过持仓：封顶到持仓数量
	o := x.place(r)
	if !o.Qty.Equal(D("0.1")) || !x.pos("BTC-PERP").IsZero() {
		t.Fatalf("只减仓封顶：%+v pos=%s", o, x.pos("BTC-PERP"))
	}
	// 只减仓挂单在仓位平掉后自动撤销
	x.place(buy("BTC-PERP", "0.1"))
	rl := limit("BTC-PERP", "sell", "61000", "0.1", GTC)
	rl.ReduceOnly = true
	ro := x.place(rl)
	x.place(sell("BTC-PERP", "0.1"))
	if o := x.e.findOrder(x.acct(), ro.ID); o.Status != StCanceled || o.Reason != "REDUCE_ONLY_NO_POSITION" {
		t.Fatalf("只减仓挂单应撤销：%+v", o)
	}
	// 只减仓不受最小名义金额限制
	x.place(buy("BTC-PERP", "0.002"))
	rs := sell("BTC-PERP", "0.001")
	rs.ReduceOnly = true
	x.place(rs)
}

// ---------- 2.2 数量 ----------

func TestQuantityModes(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	o := x.place(limit("BTC-PERP", "buy", "59000", "0.0029", GTC)) // 按数量单位向下取整
	eq(t, "取整", o.Qty, D("0.002"))
	_, _ = x.e.Cancel(1, o.ID)
	r := limit("BTC-PERP", "buy", "58000", "0", GTC)
	r.Qty, r.Notional = zero, D("5800")
	o = x.place(r)
	eq(t, "按名义金额", o.Qty, D("0.1"))
	_, _ = x.e.Cancel(1, o.ID)
	r.Notional, r.MarginPct = zero, D("50")
	r.Price = D("60000")
	o = x.place(r) // 可用 10000 × 50% × 10 倍 ÷ (60000 × 1.005) = 0.82918… → 0.829
	eq(t, "按可用保证金百分比", o.Qty, D("0.829"))
	_, _ = x.e.Cancel(1, o.ID)
	sp, _ := x.e.Specs.Get("BTC-PERP") // 币安 BTC 最小名义金额是 50，0.001 BTC 够了：这里临时改成 100 测规则本身
	sp.MinNotional = D("100")
	rj := x.reject("MIN_NOTIONAL", limit("BTC-PERP", "buy", "59000", "0.001", GTC))
	sp.MinNotional = D("50")
	if rj.Params["min"] != "100" {
		t.Fatalf("%v", rj.Params)
	}
	x.reject("QTY_TOO_SMALL", limit("BTC-PERP", "buy", "59000", "0.0004", GTC))
	x.reject("QTY_REQUIRED", OrderReq{Instrument: "BTC-PERP", Side: "buy", Type: Market})
}

// ---------- 2.3 杠杆和保证金模式 ----------

func TestLeverageAndMode(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	for _, l := range []int{0, 151} {
		if err := x.e.SetLeverage(1, "BTC-PERP", l); err == nil || err.(*Reject).Code != "LEVERAGE_RANGE" {
			t.Fatalf("杠杆 %d 应被拒：%v", l, err)
		}
	}
	if err := x.e.SetLeverage(1, "BTC-PERP", 150); err != nil {
		t.Fatal(err)
	}
	// 150 倍最大仓位 30 万 USDT（币安 2026-10 的第 1 档）
	rj := x.reject("POSITION_LIMIT", buy("BTC-PERP", "5.1"))
	if rj.Params["max"] != "300000" {
		t.Fatalf("%v", rj.Params)
	}
	_ = x.e.SetLeverage(1, "BTC-PERP", 50)
	x.place(buy("BTC-PERP", "5.1"))
	// 有仓位时：仓位 30.6 万 USDT 在第 2 档，只允许到 100 倍
	if err := x.e.SetLeverage(1, "BTC-PERP", 150); err == nil || err.(*Reject).Code != "LEVERAGE_TOO_HIGH_FOR_SIZE" || err.(*Reject).Params["max"] != "100" {
		t.Fatalf("%v", err)
	}
	x.place(sell("BTC-PERP", "4.1")) // 减回 1 BTC，下面接着测
	// 有仓位或挂单不能改模式
	if err := x.e.SetMode(1, "BTC-PERP", "isolated"); err == nil || err.(*Reject).Code != "MODE_HAS_POSITION" {
		t.Fatalf("%v", err)
	}
	x.market("ETH-PERP", "3000")
	o := x.place(limit("ETH-PERP", "buy", "2900", "0.01", GTC))
	if err := x.e.SetMode(1, "ETH-PERP", "isolated"); err == nil || err.(*Reject).Code != "MODE_HAS_ORDERS" {
		t.Fatalf("%v", err)
	}
	_, _ = x.e.Cancel(1, o.ID)
	if err := x.e.SetMode(1, "ETH-PERP", "isolated"); err != nil {
		t.Fatal(err)
	}
	// 全仓降杠杆：可用不够就拒（1 BTC 6 万，降到 2 倍要 3 万保证金）
	if err := x.e.SetLeverage(1, "BTC-PERP", 2); err == nil || err.(*Reject).Code != "INSUFFICIENT_MARGIN" {
		t.Fatalf("%v", err)
	}
	// 逐仓降杠杆：从可用余额补保证金；不够就拒
	_ = x.e.SetLeverage(1, "ETH-PERP", 20)
	x.place(buy("ETH-PERP", "1")) // 3000 ÷ 20 = 150
	eq(t, "逐仓保证金", x.acct().Pos["ETH-PERP"].IsoMargin, D("150"))
	if err := x.e.SetLeverage(1, "ETH-PERP", 10); err != nil {
		t.Fatal(err)
	}
	eq(t, "降杠杆后补到", x.acct().Pos["ETH-PERP"].IsoMargin, D("300"))
	x.place(limit("BTC-PERP", "buy", "59000", "6", GTC)) // 占掉大部分可用余额
	if err := x.e.SetLeverage(1, "ETH-PERP", 1); err == nil || err.(*Reject).Code != "INSUFFICIENT_MARGIN" {
		t.Fatalf("可用不够应拒：%v", err)
	}
	if err := x.e.Check(); err != nil {
		t.Fatal(err)
	}
}

// ---------- 2.7 挂单撮合 ----------

func TestRestingFills(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	o := x.place(limit("BTC-PERP", "buy", "59900", "0.4", GTC))
	if o.Status != StNew {
		t.Fatal(o.Status)
	}
	// 59900 这一档盘口上没有别人，我们排第一。主动买的成交不会成交到买单
	x.books.Trade(V, "BTC-PERP", D("59900"), D("0.2"), true, 0)
	eq(t, "主动买不成交买单", x.e.findOrder(x.acct(), o.ID).Filled, zero)
	// 主动卖恰好在挂单价成交 0.1：成交 0.1（一笔成交给我们的不超过它本身）
	x.books.Trade(V, "BTC-PERP", D("59900"), D("0.1"), false, 0)
	o = x.e.findOrder(x.acct(), o.ID)
	eq(t, "挂单价上成交", o.Filled, D("0.1"))
	if !o.Fee.Equal(D("1.198")) { // 5990 × 0.02% 挂单费
		t.Fatalf("挂单手续费 %s", o.Fee)
	}
	// 市场价穿过：这一档吃完了，但一笔 0.01 的成交只给我们 0.01（审查第 3 条）
	x.books.Trade(V, "BTC-PERP", D("59899.9"), D("0.01"), false, 0)
	eq(t, "穿过时按成交量", x.e.findOrder(x.acct(), o.ID).Filled, D("0.11"))
	x.books.Trade(V, "BTC-PERP", D("59899"), D("5"), false, 0)
	o = x.e.findOrder(x.acct(), o.ID)
	if o.Status != StFilled {
		t.Fatalf("%+v", o)
	}
	// 排队：卖一 60050 已经挂着 2 BTC，我们排在后面
	x.setBook("BTC-PERP", lv("59999.9", "1000"), []book.Level{{Px: D("60050"), Qty: D("2")}})
	q := x.place(limit("BTC-PERP", "sell", "60050", "0.5", GTC))
	if !q.QueueAhead.IsZero() {
		t.Fatalf("findOrder 不带排队量：%s", q.QueueAhead)
	}
	if v, _ := x.e.View(1); len(v.Orders) == 0 || !v.Orders[len(v.Orders)-1].QueueAhead.Equal(D("2")) {
		t.Fatalf("前面应排着 2：%+v", v.Orders)
	}
	x.books.Trade(V, "BTC-PERP", D("60050"), D("1.5"), true, 0)
	eq(t, "前面还没吃完", x.e.findOrder(x.acct(), q.ID).Filled, zero)
	x.books.Trade(V, "BTC-PERP", D("60050"), D("1"), true, 0) // 前面剩 0.5 吃完，给我们 0.5
	eq(t, "排到了", x.e.findOrder(x.acct(), q.ID).Filled, D("0.5"))
	// 别的交易所的成交不算
	o2 := x.place(limit("BTC-PERP", "sell", "60100", "0.1", GTC))
	x.books.Trade("hyperliquid", "BTC-PERP", D("60200"), D("5"), true, 0)
	if x.e.findOrder(x.acct(), o2.ID).Filled.Sign() != 0 {
		t.Fatal("别的交易所的成交不应撮合")
	}
	// 盘口越过挂单价（买一高于卖单价）：成交
	x.setBook("BTC-PERP", lv("60100.1", "1"), lv("60100.2", "1"))
	x.e.Tick()
	if x.e.findOrder(x.acct(), o2.ID).Status != StFilled {
		t.Fatal("盘口越过挂单价应成交")
	}
}

// ---------- 2.8 改单、撤单 ----------

func TestAmendCancel(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	o := x.place(limit("BTC-PERP", "buy", "59900", "0.4", GTC))
	x.books.Trade(V, "BTC-PERP", D("59900"), D("0.1"), false, 0) // 这一档没有别人，成交 0.1
	o2, err := x.e.Amend(1, o.ID, D("59800"), D("0.5"), zero)
	if err != nil || !o2.Price.Equal(D("59800")) || !o2.Qty.Equal(D("0.5")) || !o2.Filled.Equal(D("0.1")) {
		t.Fatalf("改单：%+v %v", o2, err)
	}
	if _, err := x.e.Amend(1, o.ID, zero, D("0.1"), zero); err == nil || err.(*Reject).Code != "AMEND_QTY" {
		t.Fatalf("改到不大于已成交数量应拒：%v", err)
	}
	// 改价到穿价：立即成交
	x.setBook("BTC-PERP", lv("59990", "5"), lv("60000", "5"))
	o3, _ := x.e.Amend(1, o.ID, D("60000"), zero, zero)
	if o3.Status != StFilled {
		t.Fatalf("改到穿价应成交：%+v", o3)
	}
	a := x.place(limit("BTC-PERP", "buy", "59000", "0.01", GTC))
	b := x.place(limit("BTC-PERP", "buy", "58000", "0.01", GTC))
	c, _ := x.e.Cancel(1, a.ID)
	if c.Status != StCanceled || c.Reason != "USER" {
		t.Fatal(c)
	}
	if _, err := x.e.Cancel(1, a.ID); err == nil {
		t.Fatal("撤已撤的单应报找不到")
	}
	x.market("ETH-PERP", "3000")
	x.place(limit("ETH-PERP", "buy", "2900", "0.01", GTC))
	n, _ := x.e.CancelAll(1, "BTC-PERP")
	if n != 1 || x.e.findOrder(x.acct(), b.ID).Status != StCanceled || len(x.acct().Orders) != 1 {
		t.Fatalf("按品种全撤：%d", n)
	}
	n, _ = x.e.CancelAll(1, "")
	if n != 1 || len(x.acct().Orders) != 0 {
		t.Fatal("全部撤销")
	}
}

// ---------- 2.9 平仓 ----------

func TestClose(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	x.place(buy("BTC-PERP", "0.1"))
	if _, err := x.e.ClosePosition(1, "BTC-PERP", D("0.25"), Market, zero); err != nil {
		t.Fatal(err)
	}
	eq(t, "平 25%", x.pos("BTC-PERP"), D("0.075"))
	o, err := x.e.ClosePosition(1, "BTC-PERP", D("0.5"), Limit, D("61000"))
	if err != nil || o.Status != StNew || !o.ReduceOnly || !o.Qty.Equal(D("0.037")) {
		t.Fatalf("限价平 50%%：%+v %v", o, err)
	}
	x.market("ETH-PERP", "3000")
	x.place(sell("ETH-PERP", "1"))
	os, err := x.e.CloseAll(1)
	if err != nil || len(os) != 2 || !x.pos("BTC-PERP").IsZero() || !x.pos("ETH-PERP").IsZero() {
		t.Fatalf("一键全平：%v %v", os, err)
	}
	if o := x.e.findOrder(x.acct(), o.ID); o.Status != StCanceled {
		t.Fatal("仓位平掉后限价平仓单应撤销")
	}
	if _, err := x.e.ClosePosition(1, "BTC-PERP", one, Market, zero); err == nil || err.(*Reject).Code != "NO_POSITION" {
		t.Fatal(err)
	}
	// 反手：多 0.1 再市价卖 0.3 → 空 0.2，开仓价为成交价
	x.place(buy("BTC-PERP", "0.1"))
	x.place(sell("BTC-PERP", "0.3"))
	eq(t, "反手后仓位", x.pos("BTC-PERP"), D("-0.2"))
	eq(t, "反手后开仓价", x.acct().Pos["BTC-PERP"].Entry, D("59999.9"))
	if err := x.e.Check(); err != nil {
		t.Fatal(err)
	}
}

// ---------- 2.10 止盈止损 ----------

func TestTPSL(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	tp, sl := D("61000"), D("59000")
	if err := x.e.SetTPSL(1, "BTC-PERP", &tp, &sl, ""); err == nil || err.(*Reject).Code != "NO_POSITION" {
		t.Fatal(err)
	}
	x.place(buy("BTC-PERP", "0.1"))
	bad := D("59500")
	if err := x.e.SetTPSL(1, "BTC-PERP", &bad, nil, ""); err == nil || err.(*Reject).Code != "WOULD_TRIGGER" {
		t.Fatalf("止盈价低于标记价应拒：%v", err)
	}
	if err := x.e.SetTPSL(1, "BTC-PERP", &tp, &sl, ""); err != nil {
		t.Fatal(err)
	}
	v, _ := x.e.View(1)
	if !v.Positions[0].TP.Equal(tp) || !v.Positions[0].SL.Equal(sl) {
		t.Fatalf("%+v", v.Positions[0])
	}
	// 改止盈
	tp2 := D("62000")
	_ = x.e.SetTPSL(1, "BTC-PERP", &tp2, nil, "")
	v, _ = x.e.View(1)
	if !v.Positions[0].TP.Equal(tp2) || len(v.Orders) != 2 {
		t.Fatalf("改止盈：%+v", v.Positions[0])
	}
	// 标记价到 62000：止盈触发，市价平仓，止损自动撤销
	x.market("BTC-PERP", "62000")
	x.e.Tick()
	if !x.pos("BTC-PERP").IsZero() || len(x.acct().Orders) != 0 {
		t.Fatalf("止盈后应平仓并撤销止损：pos=%s orders=%d", x.pos("BTC-PERP"), len(x.acct().Orders))
	}
	if n := x.acct().Notices; len(n) == 0 || n[len(n)-1].Kind != "tp" {
		t.Fatal("应有止盈通知")
	}
	// 空单止损：标记价上涨触发
	x.place(sell("BTC-PERP", "0.1"))
	sl2 := D("63000")
	_ = x.e.SetTPSL(1, "BTC-PERP", nil, &sl2, "")
	x.market("BTC-PERP", "63000")
	x.e.Tick()
	if !x.pos("BTC-PERP").IsZero() {
		t.Fatal("空单止损应触发")
	}
	// 随单止盈止损：成交后挂到仓位上；方向不对拒单
	r := limit("BTC-PERP", "buy", "63000", "0.1", GTC)
	r.TP, r.SL = D("62000"), D("62500")
	x.reject("BAD_TPSL", r)
	r.TP, r.SL = D("65000"), D("61000")
	o := x.place(r)
	n := 0
	for _, c := range x.acct().Orders {
		if c.Parent == o.ID {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("随单止盈止损应挂上 2 张，实际 %d", n)
	}
	// 取消止盈
	z := zero
	_ = x.e.SetTPSL(1, "BTC-PERP", &z, nil, "")
	v, _ = x.e.View(1)
	if !v.Positions[0].TP.IsZero() || v.Positions[0].SL.IsZero() {
		t.Fatalf("取消止盈：%+v", v.Positions[0])
	}
}

// ---------- 2.11 资金费 ----------

func TestFunding(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	t1 := x.now.Add(time.Hour).UnixMilli()
	x.books.UpdateTicker(V, "BTC-PERP", func(tk *book.Ticker) { tk.FundingRate = D("0.0001"); tk.NextFunding = t1 })
	x.e.Tick() // 记下本期
	x.place(buy("BTC-PERP", "1"))
	w := x.acct().Wallet
	x.now = x.now.Add(time.Hour)
	x.books.UpdateTicker(V, "BTC-PERP", func(tk *book.Ticker) { tk.FundingRate = D("0.0003"); tk.NextFunding = t1 + 8*3600_000 })
	x.e.Tick()
	eq(t, "多头付资金费", x.acct().Wallet.Sub(w), D("-6")) // 1 × 60000 × 0.01%
	x.e.Tick()
	eq(t, "同一期不重复收", x.acct().Wallet.Sub(w), D("-6"))
	p, _ := x.e.History(1, "funding", 1, 10)
	if p.Total != 1 {
		t.Fatal("应有一条资金费记录")
	}
	if err := x.e.Check(); err != nil {
		t.Fatal(err)
	}
}

// ---------- 2.5 强平 ----------

func TestLiquidationIsolated(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	_ = x.e.SetMode(1, "BTC-PERP", "isolated")
	x.place(buy("BTC-PERP", "1")) // 逐仓保证金 6000，强平价 54221.1
	w := x.acct().Wallet
	x.market("BTC-PERP", "54300")
	x.e.Tick()
	if x.pos("BTC-PERP").IsZero() {
		t.Fatal("没到强平价不应强平")
	}
	x.market("BTC-PERP", "54200")
	x.e.Tick()
	if !x.pos("BTC-PERP").IsZero() {
		t.Fatal("应强平")
	}
	eq(t, "逐仓爆仓损失整个逐仓保证金", x.acct().Wallet.Sub(w), D("-6000"))
	if n := x.acct().Notices; len(n) == 0 || n[len(n)-1].Kind != "liquidation" {
		t.Fatal("应通知用户")
	}
	if err := x.e.Check(); err != nil {
		t.Fatal(err)
	}
}

func TestLiquidationCross(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	x.place(buy("BTC-PERP", "1.6")) // 名义 96000，保证金 9600，手续费 48
	x.place(limit("BTC-PERP", "buy", "57000", "0.002", GTC))
	// LP = (9952 + 50 − 96000) ÷ (1.6×0.005 − 1.6) = 54018.8
	x.market("BTC-PERP", "54100")
	x.e.Tick()
	if x.pos("BTC-PERP").IsZero() {
		t.Fatal("没到强平价不应强平")
	}
	x.market("BTC-PERP", "54000")
	x.e.Tick()
	if !x.pos("BTC-PERP").IsZero() || len(x.acct().Orders) != 0 {
		t.Fatal("全仓应强平并撤销所有挂单")
	}
	near(t, "全仓爆仓后余额", x.acct().Wallet, zero, D("0.000001"))
	if err := x.e.Check(); err != nil {
		t.Fatal(err)
	}
}

// ---------- 2.6 预览 ----------

func TestPreview(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	x.setBook("BTC-PERP", lv("59990", "1"), lv("60000", "0.05", "60010", "0.05", "60020", "1"))
	p, err := x.e.Preview(1, buy("BTC-PERP", "0.2"))
	if err != nil || p.Problem != nil {
		t.Fatal(err, p.Problem)
	}
	eq(t, "预估均价", p.EstPx, D("60012.5")) // (3000+3000.5+6004)/0.2
	eq(t, "预估手续费", p.Fee, D("6.0013"))
	if p.Maker || p.LiqPx.Sign() <= 0 || p.Margin.Sign() <= 0 {
		t.Fatalf("%+v", p)
	}
	// 预估强平价和成交后实际的一致
	x.place(buy("BTC-PERP", "0.2"))
	v, _ := x.e.View(1)
	near(t, "预估强平价 vs 实际", p.LiqPx, v.Positions[0].LiqPx, D("0.2"))
	// 挂单（不穿价）按挂单手续费
	p, _ = x.e.Preview(1, limit("BTC-PERP", "buy", "59000", "0.1", GTC))
	if !p.Maker || !p.Fee.Equal(D("1.18")) {
		t.Fatalf("%+v", p)
	}
	// 会被拒的单：预览里说明原因
	p, _ = x.e.Preview(1, buy("BTC-PERP", "5"))
	if p.Problem == nil || p.Problem.Code != "INSUFFICIENT_MARGIN" || p.MaxQty.Sign() <= 0 {
		t.Fatalf("%+v", p)
	}
}

// ---------- 2.12 账本和重启恢复 ----------

func TestReplayRestoresEverything(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	x.market("ETH-PERP", "3000")
	t1 := x.now.Add(time.Hour).UnixMilli()
	x.books.UpdateTicker(V, "BTC-PERP", func(tk *book.Ticker) { tk.FundingRate = D("0.0001"); tk.NextFunding = t1 })
	x.e.Tick()
	x.e.Open(2, D("10000"))
	_ = x.e.SetMode(1, "ETH-PERP", "isolated")
	_ = x.e.SetLeverage(1, "ETH-PERP", 20)
	x.place(buy("BTC-PERP", "0.3"))
	x.place(sell("ETH-PERP", "2"))
	r := limit("BTC-PERP", "buy", "59000", "0.1", GTC)
	r.TP = D("65000")
	x.place(r)
	x.place(limit("ETH-PERP", "sell", "3100", "0.5", GTC))
	tp, sl := D("64000"), D("57000")
	_ = x.e.SetTPSL(1, "BTC-PERP", &tp, &sl, "")
	x.books.Trade(V, "BTC-PERP", D("58999"), D("1"), false, 0) // 挂单成交，随单止盈挂上
	_, _ = x.e.ClosePosition(1, "BTC-PERP", D("0.5"), Market, zero)
	x.books.UpdateTicker(V, "BTC-PERP", func(tk *book.Ticker) { tk.NextFunding = t1 + 8*3600_000 })
	x.e.Tick() // 资金费
	// 条件单（一张触发、一张还挂着、一张改过触发价）和逐仓调整保证金也要能重放
	c1 := x.place(OrderReq{Instrument: "BTC-PERP", Side: "buy", Type: CondMarket, TriggerPx: D("60500"), Qty: D("0.01")})
	x.place(OrderReq{Instrument: "BTC-PERP", Side: "sell", Type: CondLimit, TriggerPx: D("59000"), Price: D("58900"), Qty: D("0.01"), TriggerBy: ByLast})
	if _, err := x.e.Amend(1, c1.ID, zero, zero, D("60600")); err != nil {
		t.Fatal(err)
	}
	x.market("BTC-PERP", "60700")
	x.e.Tick() // c1 触发
	if _, err := x.e.AdjustMargin(1, "ETH-PERP", D("25"), false); err != nil {
		t.Fatal(err)
	}
	x.e.Archive(2)

	if x.e.Flush() != 0 {
		t.Fatal("还有没写的事件")
	}
	before1, _ := json.Marshal(x.e.state(1))
	before2, _ := json.Marshal(x.e.state(2))
	if err := x.e.Check(); err != nil {
		t.Fatal(err)
	}

	// 新引擎从事件重放（事件先序列化再反序列化，模拟存进数据库再读出来）
	raw, _ := json.Marshal(x.evs)
	var evs []Event
	if err := json.Unmarshal(raw, &evs); err != nil {
		t.Fatal(err)
	}
	y := New(x.books, V, NewSpecs(LooseSpecs()), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := y.Restore(evs); err != nil {
		t.Fatal(err)
	}
	after1, _ := json.Marshal(y.state(1))
	after2, _ := json.Marshal(y.state(2))
	if !bytes.Equal(before1, after1) || !bytes.Equal(before2, after2) {
		t.Fatalf("重放后状态不一致\n前：%s\n后：%s", before1, after1)
	}
	if err := y.Check(); err != nil {
		t.Fatal(err)
	}
	if y.seq != x.e.seq {
		t.Fatal("事件序号没恢复")
	}
	// 恢复后挂单继续有效
	if len(y.accounts[1].Orders) == 0 {
		t.Fatal("挂单应恢复")
	}
	if !y.accounts[2].Archived {
		t.Fatal("归档状态应恢复")
	}
}

func TestArchive(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	o := x.place(limit("BTC-PERP", "buy", "59000", "0.01", GTC))
	x.e.Archive(1)
	if x.e.findOrder(x.acct(), o.ID).Status != StCanceled {
		t.Fatal("归档时应撤销挂单")
	}
	if _, err := x.e.Place(1, buy("BTC-PERP", "0.01")); err == nil || err.(*Reject).Code != "ACCOUNT_ARCHIVED" {
		t.Fatal(err)
	}
}

// 盘口停止更新超过 StaleAfter：不接新单（避免按过期价格成交），恢复后正常。
func TestStaleBook(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	b, _ := x.books.Book(V, "BTC-PERP")
	x.books.SetBookAt(b, time.Now().Add(-StaleAfter-2*time.Second).UnixMilli())
	_, err := x.e.Place(1, OrderReq{Instrument: "BTC-PERP", Side: "buy", Type: Market, Qty: D("0.01")})
	if r, ok := err.(*Reject); !ok || r.Code != "MARKET_STALE" || r.HTTP != 503 {
		t.Fatalf("过期盘口应拒单：%v", err)
	}
	x.books.SetBook(b) // 盘口恢复
	if _, err := x.e.Place(1, OrderReq{Instrument: "BTC-PERP", Side: "buy", Type: Market, Qty: D("0.01")}); err != nil {
		t.Fatalf("盘口恢复后应能下单：%v", err)
	}
}

// 一笔成交直接反手：原来多仓的止盈止损作废，不留在委托列表里。
func TestFlipCancelsOldTPSL(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	if _, err := x.e.Place(1, OrderReq{Instrument: "BTC-PERP", Side: "buy", Type: Market, Qty: D("0.01"), TP: D("66000"), SL: D("55000")}); err != nil {
		t.Fatal(err)
	}
	if n := len(x.e.accounts[1].Orders); n != 2 {
		t.Fatalf("随单止盈止损应挂上 2 张：%d", n)
	}
	if _, err := x.e.Place(1, OrderReq{Instrument: "BTC-PERP", Side: "sell", Type: Market, Qty: D("0.03")}); err != nil {
		t.Fatal(err)
	}
	if p := x.e.accounts[1].Pos["BTC-PERP"]; !p.Qty.Equal(D("-0.02")) {
		t.Fatalf("应反手成空 0.02：%s", p.Qty)
	}
	if n := len(x.e.accounts[1].Orders); n != 0 {
		t.Fatalf("反手后原来的止盈止损应撤掉：还剩 %d 张", n)
	}
}

// 后端停机期间错过的资金费：开机补扣；已经结算过的、开仓之前的、停机之前的都不补；补两遍也只扣一次。
func TestCatchUpFunding(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	if _, err := x.e.Place(1, OrderReq{Instrument: "BTC-PERP", Side: "buy", Type: Market, Qty: D("0.1")}); err != nil {
		t.Fatal(err)
	}
	opened := x.now.UnixMilli()
	wallet := x.e.accounts[1].Wallet
	x.now = x.now.Add(20 * time.Hour) // 停机 20 小时
	h := int64(3600 * 1000)
	settles := []FundingSettle{
		{Time: opened - 2*h, Rate: D("0.0001"), Mark: D("60000")},   // 开仓之前：不补
		{Time: opened + 6*h, Rate: D("0.0001"), Mark: D("61000")},   // 停机期间：补
		{Time: opened + 14*h, Rate: D("-0.0002"), Mark: D("59000")}, // 停机期间，费率为负：多头收钱
		{Time: opened + 30*h, Rate: D("0.0001"), Mark: D("60000")},  // 还没到：不补
	}
	if n := x.e.CatchUpFunding("BTC-PERP", settles, opened); n != 2 {
		t.Fatalf("应补 2 笔：%d", n)
	}
	if n := x.e.CatchUpFunding("BTC-PERP", settles, opened); n != 0 {
		t.Fatalf("重复补应为 0：%d", n)
	}
	want := wallet.Sub(D("0.1").Mul(D("61000")).Mul(D("0.0001"))).Add(D("0.1").Mul(D("59000")).Mul(D("0.0002")))
	eq(t, "补扣后的钱包余额", x.e.accounts[1].Wallet, want)
	if err := x.e.Check(); err != nil {
		t.Fatalf("账本：%v", err)
	}
}

// 11-18：开机补资金费时取不到交易所记录（被限流、代理还没好）——原来就不补了。
// 现在记下来稍后重试；重试成功前你又加了仓，也按开机那一刻（= 停机期间）的持仓算；开机以后的结算不归它管。
func TestFundingGapAtBoot(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	x.place(buy("BTC-PERP", "0.1"))
	opened := x.now.UnixMilli()
	h := int64(3600_000)
	x.now = x.now.Add(20 * time.Hour) // 停机 20 小时后开机
	boot := x.now.UnixMilli()
	fail := true
	x.e.FundingHistory = func(_ context.Context, inst string, from, to int64) ([]FundingSettle, error) {
		if fail {
			return nil, fmt.Errorf("币安返回 418")
		}
		all := []FundingSettle{
			{Time: opened + 6*h, Rate: D("0.0001"), Mark: D("61000")},
			{Time: opened + 14*h, Rate: D("-0.0002"), Mark: D("59000")},
			{Time: boot + h, Rate: D("0.0005"), Mark: D("60000")}, // 开机以后的：实时结，不归开机补结
		}
		var out []FundingSettle
		for _, s := range all {
			if s.Time >= from && s.Time <= to {
				out = append(out, s)
			}
		}
		return out, nil
	}
	w := x.acct().Wallet
	x.e.FundingGapAtBoot("BTC-PERP", opened, boot)
	if n := x.e.settleGaps(context.Background()); n != 0 {
		t.Fatalf("取不到记录时不算补上：%d", n)
	}
	x.place(buy("BTC-PERP", "0.4")) // 开机后加仓到 0.5
	x.now = x.now.Add(2 * time.Hour)
	fail = false
	if n := x.e.settleGaps(context.Background()); n != 2 {
		t.Fatalf("应补停机期间的 2 笔：%d", n)
	}
	want := D("0.1").Mul(D("61000")).Mul(D("0.0001")).Neg().Add(D("0.1").Mul(D("59000")).Mul(D("0.0002")))
	got := x.acct().Wallet.Sub(w).Add(x.acct().Fills[len(x.acct().Fills)-1].Fee) // 去掉加仓那笔的手续费
	eq(t, "按停机时的 0.1 算，不按现在的 0.5", got, want)
	if n := x.e.settleGaps(context.Background()); n != 0 {
		t.Fatalf("补过了不再补：%d", n)
	}
	if fs := x.acct().Fundings; len(fs) != 2 || !fs[0].Qty.Equal(D("0.1")) {
		t.Fatalf("资金费记录：%+v", fs)
	}
	if err := x.e.Check(); err != nil {
		t.Fatalf("账本：%v", err)
	}
}

// 11-18：实时看到的结算按交易所公布的正式费率结（推送里的只是预估，对照时有两期差了约 1%），持仓按结算那一刻的；
// 记录还没公布就等；15 分钟还取不到按预估结
func TestLiveFundingUsesPublishedRate(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	t1 := x.now.Add(time.Hour).UnixMilli()
	x.books.UpdateTicker(V, "BTC-PERP", func(tk *book.Ticker) { tk.FundingRate = D("0.0001"); tk.NextFunding = t1 })
	published := false
	x.e.FundingHistory = func(_ context.Context, inst string, from, to int64) ([]FundingSettle, error) {
		if !published {
			return nil, nil // 交易所还没公布这一期
		}
		return []FundingSettle{{Time: t1 + 4, Rate: D("0.00012"), Mark: D("60100")}}, nil
	}
	x.e.Tick()
	x.place(buy("BTC-PERP", "0.1"))
	x.now = time.UnixMilli(t1 + 1000) // 结算刚过
	x.books.UpdateTicker(V, "BTC-PERP", func(tk *book.Ticker) { tk.FundingRate = D("0.0002"); tk.NextFunding = t1 + 8*3600_000 })
	x.e.Tick()
	w := x.acct().Wallet
	eq(t, "推送里的预估费率不直接扣", x.acct().Wallet.Sub(w), D("0"))
	x.place(buy("BTC-PERP", "0.1")) // 结算以后加仓：不该算进这一期
	w = x.acct().Wallet
	x.now = x.now.Add(31 * time.Second)
	if n := x.e.settleGaps(context.Background()); n != 0 {
		t.Fatalf("交易所还没公布时不结：%d", n)
	}
	published = true
	x.now = x.now.Add(31 * time.Second)
	if n := x.e.settleGaps(context.Background()); n != 1 {
		t.Fatalf("公布以后结 1 笔：%d", n)
	}
	eq(t, "按正式费率 0.00012、标记价 60100、结算那一刻的 0.1 BTC", x.acct().Wallet.Sub(w), D("-0.7212"))
	if n := x.e.settleGaps(context.Background()); n != 0 {
		t.Fatalf("结过不再结：%d", n)
	}
	if err := x.e.Check(); err != nil {
		t.Fatal(err)
	}

	x.e.Tick() // 补结以后重新记住下一期（真实运行时每 200 毫秒一拍）

	// 下一期：交易所一直取不到（被限流），15 分钟后按推送里的预估结
	t2 := t1 + 8*3600_000
	x.e.FundingHistory = func(context.Context, string, int64, int64) ([]FundingSettle, error) {
		return nil, fmt.Errorf("币安返回 418")
	}
	x.now = time.UnixMilli(t2 + 2000)
	x.books.UpdateTicker(V, "BTC-PERP", func(tk *book.Ticker) { tk.NextFunding = t2 + 8*3600_000 })
	x.e.Tick()
	w = x.acct().Wallet
	x.now = x.now.Add(5 * time.Minute)
	if n := x.e.settleGaps(context.Background()); n != 0 {
		t.Fatalf("5 分钟时还等：%d", n)
	}
	x.now = x.now.Add(11 * time.Minute)
	if n := x.e.settleGaps(context.Background()); n != 1 {
		t.Fatalf("15 分钟后按预估结：%d", n)
	}
	mk := x.acct().Fundings[len(x.acct().Fundings)-1]
	if !mk.Rate.Equal(D("0.0002")) || !mk.Qty.Equal(D("0.2")) {
		t.Fatalf("按预估费率 0.0002、结算那一刻的 0.2 BTC：%+v", mk)
	}
	if err := x.e.Check(); err != nil {
		t.Fatal(err)
	}
}

// 某张委托的逐笔成交。
func TestOrderFills(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	x.setBook("BTC-PERP", lv("59990", "1"), lv("60000", "0.05", "60010", "0.05", "60020", "1"))
	o := x.place(limit("BTC-PERP", "buy", "60010", "0.2", IOC))
	fs, err := x.e.OrderFills(1, o.ID)
	if err != nil || len(fs) != 2 || !fs[0].Px.Equal(D("60000")) || !fs[1].Px.Equal(D("60010")) || fs[0].Fee.Sign() <= 0 {
		t.Fatalf("逐笔成交：%+v %v", fs, err)
	}
}

// ---------- 条件单 ----------

// 条件市价单：价格涨到触发价才下单（可以开仓）；触发前不占保证金；触发后生成一张市价单，条件单记为"已触发"并通知。
func TestCondMarket(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	o := x.place(OrderReq{Instrument: "BTC-PERP", Side: "buy", Type: CondMarket, TriggerPx: D("61000"), Qty: D("0.1")})
	if o.Status != StNew || o.TriggerDir != "up" || o.TriggerBy != ByMark {
		t.Fatalf("条件单：%+v", o)
	}
	if v, _ := x.e.View(1); !v.OrderMargin.IsZero() {
		t.Fatalf("触发前不占保证金：%s", v.OrderMargin)
	}
	x.market("BTC-PERP", "60900")
	x.e.Tick()
	if p := x.acct().Pos["BTC-PERP"]; p != nil && !p.Qty.IsZero() {
		t.Fatal("没到触发价不该成交")
	}
	x.market("BTC-PERP", "61000")
	x.e.Tick()
	if p := x.acct().Pos["BTC-PERP"]; p == nil || !p.Qty.Equal(D("0.1")) {
		t.Fatalf("触发后应开多 0.1：%+v", p)
	}
	h := x.e.findOrder(x.acct(), o.ID)
	if h.Status != StTriggered || h.Reason != "TRIGGERED" {
		t.Fatalf("条件单状态：%+v", h)
	}
	var child *Order
	for _, x2 := range x.acct().History {
		if x2.Parent == o.ID {
			child = x2
		}
	}
	if child == nil || child.Type != Market || child.Status != StFilled || child.Source != "cond" {
		t.Fatalf("触发生成的市价单：%+v", child)
	}
	if n := x.acct().Notices; len(n) == 0 || n[len(n)-1].Kind != "cond" {
		t.Fatalf("应有触发通知：%+v", n)
	}
}

// 条件限价单：跌破触发价后挂一张限价卖单；按最新价触发；触发时保证金不够 → 条件单过期并说明原因。
func TestCondLimitAndFailures(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	o := x.place(OrderReq{Instrument: "BTC-PERP", Side: "sell", Type: CondLimit, TriggerPx: D("59000"), Price: D("58500"), Qty: D("0.1"), TriggerBy: ByLast})
	if o.TriggerDir != "down" {
		t.Fatalf("方向：%s", o.TriggerDir)
	}
	x.books.UpdateTicker(V, "BTC-PERP", func(tk *book.Ticker) { tk.Mark = D("58000") }) // 标记价到了、最新价没到：不触发
	x.e.Tick()
	if x.e.findOrder(x.acct(), o.ID).Status != StNew {
		t.Fatal("按最新价触发的单，不该因为标记价触发")
	}
	x.market("BTC-PERP", "59000")
	x.setBook("BTC-PERP", lv("58400", "1"), lv("59000", "1")) // 买一 58400 < 限价 58500：挂着不成交
	x.e.Tick()
	var child *Order
	for _, x2 := range x.acct().Orders {
		if x2.Parent == o.ID {
			child = x2
		}
	}
	if child == nil || child.Type != Limit || child.Status != StNew || !child.Price.Equal(D("58500")) {
		t.Fatalf("触发后应挂一张限价单：%+v", child)
	}
	// 拒单：触发价等于现价、触发价不在价格单位上
	x.market("BTC-PERP", "60000")
	x.reject("TRIGGER_AT_PRICE", OrderReq{Instrument: "BTC-PERP", Side: "buy", Type: CondMarket, TriggerPx: D("60000"), Qty: D("0.1")})
	x.reject("TRIGGER_TICK", OrderReq{Instrument: "BTC-PERP", Side: "buy", Type: CondMarket, TriggerPx: D("60000.05"), Qty: D("0.1")})
	x.reject("BAD_TRIGGER_BY", OrderReq{Instrument: "BTC-PERP", Side: "buy", Type: CondMarket, TriggerPx: D("61000"), Qty: D("0.1"), TriggerBy: "oracle"})
	// 触发时保证金不够：下单时够（1.5 BTC ≈ 9 万，10 倍要 9000），之后先用掉大部分余额
	big := x.place(OrderReq{Instrument: "BTC-PERP", Side: "buy", Type: CondMarket, TriggerPx: D("61000"), Qty: D("1.5")})
	x.place(limit("BTC-PERP", "buy", "59000", "1.4", GTC)) // 占掉 8260 保证金
	x.market("BTC-PERP", "61000")
	x.e.Tick()
	h := x.e.findOrder(x.acct(), big.ID)
	if h.Status != StExpired || h.Reason != "INSUFFICIENT_MARGIN" {
		t.Fatalf("保证金不够应过期：%+v", h)
	}
	if n := x.acct().Notices; n[len(n)-1].Kind != "cond_failed" || n[len(n)-1].Reason != "INSUFFICIENT_MARGIN" {
		t.Fatalf("应通知触发失败：%+v", n[len(n)-1])
	}
	if err := x.e.Check(); err != nil {
		t.Fatal(err)
	}
}

// 改条件单的触发价（图上拖动）：方向按现价重新判断。
func TestAmendCond(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	o := x.place(OrderReq{Instrument: "BTC-PERP", Side: "buy", Type: CondMarket, TriggerPx: D("61000"), Qty: D("0.1")})
	o2, err := x.e.Amend(1, o.ID, zero, zero, D("59000"))
	if err != nil || !o2.TriggerPx.Equal(D("59000")) || o2.TriggerDir != "down" {
		t.Fatalf("改触发价：%+v %v", o2, err)
	}
	if _, err := x.e.Amend(1, o.ID, zero, zero, D("60000")); err == nil || err.(*Reject).Code != "TRIGGER_AT_PRICE" {
		t.Fatalf("改到现价应拒：%v", err)
	}
	x.market("BTC-PERP", "58990")
	x.e.Tick()
	if p := x.acct().Pos["BTC-PERP"]; p == nil || !p.Qty.Equal(D("0.1")) {
		t.Fatalf("跌到新触发价应开多：%+v", p)
	}
}

// 止盈止损按最新价触发。
func TestTPSLByLast(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	x.place(buy("BTC-PERP", "0.1"))
	sl := D("59000")
	if err := x.e.SetTPSL(1, "BTC-PERP", nil, &sl, ByLast); err != nil {
		t.Fatal(err)
	}
	x.books.UpdateTicker(V, "BTC-PERP", func(tk *book.Ticker) { tk.Mark = D("58000") })
	x.e.Tick()
	if p := x.acct().Pos["BTC-PERP"]; p.Qty.IsZero() {
		t.Fatal("按最新价的止损，不该因为标记价触发")
	}
	x.books.UpdateTicker(V, "BTC-PERP", func(tk *book.Ticker) { tk.Last = D("58999") })
	x.e.Tick()
	if p := x.acct().Pos["BTC-PERP"]; !p.Qty.IsZero() {
		t.Fatal("最新价到了应触发")
	}
}

// 逐仓调整保证金：追加不超过可用、减少不超过"逐仓保证金 + 浮亏 − 初始保证金"；强平价跟着变。
func TestAdjustMargin(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	x.place(buy("BTC-PERP", "0.1"))
	if _, err := x.e.AdjustMargin(1, "BTC-PERP", D("10"), false); err == nil || err.(*Reject).Code != "NOT_ISOLATED" {
		t.Fatalf("全仓不能调：%v", err)
	}
	x2 := newEnv(t)
	x2.market("BTC-PERP", "60000")
	_ = x2.e.SetMode(1, "BTC-PERP", "isolated")
	x2.place(buy("BTC-PERP", "0.1")) // 10 倍：逐仓保证金 600
	pre, _ := x2.e.AdjustMargin(1, "BTC-PERP", zero, true)
	eq(t, "最多能减少（刚开仓，浮亏 0.1）", pre.MaxRemove, D("0"))
	r, err := x2.e.AdjustMargin(1, "BTC-PERP", D("400"), false)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "追加后逐仓保证金", x2.acct().Pos["BTC-PERP"].IsoMargin, D("1000"))
	if !r.LiqPx.LessThan(pre.LiqPx) {
		t.Fatalf("追加后强平价应下移：%s → %s", pre.LiqPx, r.LiqPx)
	}
	if _, err := x2.e.AdjustMargin(1, "BTC-PERP", D("-500"), false); err == nil || err.(*Reject).Code != "MARGIN_REMOVE_TOO_MUCH" {
		t.Fatalf("减太多应拒：%v", err)
	}
	if _, err := x2.e.AdjustMargin(1, "BTC-PERP", D("-300"), false); err != nil {
		t.Fatal(err)
	}
	eq(t, "减少后逐仓保证金", x2.acct().Pos["BTC-PERP"].IsoMargin, D("700"))
	if _, err := x2.e.AdjustMargin(1, "BTC-PERP", D("100000"), false); err == nil || err.(*Reject).Code != "MARGIN_ADD_TOO_MUCH" {
		t.Fatalf("追加超过可用应拒：%v", err)
	}
	if err := x2.e.Check(); err != nil {
		t.Fatal(err)
	}
}

// 最新成交：新的在前，最多保留 keepTrades 条。
func TestRecentTrades(t *testing.T) {
	s := book.NewStore()
	for i := 1; i <= 300; i++ {
		s.Trade(V, "BTC-PERP", D(fmt.Sprint(60000+i)), D("0.1"), i%2 == 0, int64(i))
	}
	tr := s.RecentTrades(V, "BTC-PERP", 50)
	if len(tr) != 50 || !tr[0].Px.Equal(D("60300")) || tr[0].Time != 300 || !tr[0].Buy {
		t.Fatalf("最新成交：%d %+v", len(tr), tr[0])
	}
	if len(s.RecentTrades(V, "BTC-PERP", 1000)) > 240 {
		t.Fatal("保留条数没有上限")
	}
}

// 第 2 条：给仓位加一笔只带止损的单，原来的止盈还在。
func TestAddSLOnlyKeepsTP(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	if _, err := x.e.Place(1, OrderReq{Instrument: "BTC-PERP", Side: "buy", Type: Market, Qty: D("0.01"), TP: D("66000")}); err != nil {
		t.Fatal(err)
	}
	if _, err := x.e.Place(1, OrderReq{Instrument: "BTC-PERP", Side: "buy", Type: Market, Qty: D("0.01"), SL: D("55000")}); err != nil {
		t.Fatal(err)
	}
	tp, sl := 0, 0
	for _, o := range x.e.accounts[1].Orders {
		if o.Type == TakeProfit && o.TriggerPx.Equal(D("66000")) {
			tp++
		}
		if o.Type == StopMarket && o.TriggerPx.Equal(D("55000")) {
			sl++
		}
	}
	if tp != 1 || sl != 1 {
		t.Fatalf("止盈 %d 张、止损 %d 张，应该各 1 张", tp, sl)
	}
}

// 第 3 条：用户撤掉或改过随单止盈止损，这张委托后面的成交不能把它变回来。
func TestAttachedLegRespectsUser(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	x.place(OrderReq{Instrument: "BTC-PERP", Side: "buy", Type: Limit, Price: D("59000"), Qty: D("0.3"), TP: D("66000"), SL: D("55000")})
	child := func(typ string) *Order {
		for _, c := range x.e.accounts[1].Orders {
			if c.Type == typ {
				return c
			}
		}
		return nil
	}
	// 主动卖恰好在挂单价成交 0.1：部分成交
	x.e.onTrade(V, "BTC-PERP", D("59000"), D("0.1"), false, x.now.UnixMilli())
	if child(TakeProfit) == nil || child(StopMarket) == nil {
		t.Fatal("第一笔成交后应挂上止盈止损")
	}
	if _, err := x.e.Cancel(1, child(TakeProfit).ID); err != nil {
		t.Fatal(err)
	}
	sl := D("54000")
	if err := x.e.SetTPSL(1, "BTC-PERP", nil, &sl, "mark"); err != nil {
		t.Fatal(err)
	}
	x.e.onTrade(V, "BTC-PERP", D("59000"), D("0.1"), false, x.now.UnixMilli())
	if c := child(TakeProfit); c != nil {
		t.Fatalf("用户撤掉的止盈又回来了：%+v", c)
	}
	if c := child(StopMarket); c == nil || !c.TriggerPx.Equal(sl) {
		t.Fatalf("用户改过的止损被盖掉了：%+v", c)
	}
}

// 第 10 条：盘口过期时不接改单，不然改价后会按过期盘口成交。
func TestAmendRejectedWhenStale(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	o := x.place(OrderReq{Instrument: "BTC-PERP", Side: "buy", Type: Limit, Price: D("59000"), Qty: D("0.01")})
	b, _ := x.books.Book(V, "BTC-PERP")
	x.books.SetBookAt(b, time.Now().Add(-StaleAfter-2*time.Second).UnixMilli())
	_, err := x.e.Amend(1, o.ID, D("60500"), D("0.01"), D("0"))
	if r, ok := err.(*Reject); !ok || r.Code != "MARKET_STALE" {
		t.Fatalf("过期盘口应拒绝改单：%v", err)
	}
	if p := x.acct().Pos["BTC-PERP"]; p != nil && !p.Qty.IsZero() {
		t.Fatalf("不该成交：%s", p.Qty)
	}
	x.books.SetBook(b)
	if _, err := x.e.Amend(1, o.ID, D("59500"), D("0.01"), D("0")); err != nil {
		t.Fatalf("盘口恢复后应能改单：%v", err)
	}
}

// 第 4 条：电脑睡眠跨 3 次结算，醒来后按交易所的真实记录补 3 笔，不是只收 1 笔；取不到记录就等下次；不重复。
func TestSleepAcrossFundings(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	t1 := x.now.Add(time.Hour).UnixMilli()
	x.books.UpdateTicker(V, "BTC-PERP", func(tk *book.Ticker) { tk.FundingRate = D("0.0001"); tk.NextFunding = t1 })
	h := int64(3600_000)
	fail := true
	x.e.FundingHistory = func(_ context.Context, inst string, from, to int64) ([]FundingSettle, error) {
		if fail {
			return nil, fmt.Errorf("网络还没恢复")
		}
		return []FundingSettle{{Time: t1, Rate: D("0.0001"), Mark: D("60000")}, {Time: t1 + 8*h, Rate: D("0.0002"), Mark: D("61000")}, {Time: t1 + 16*h, Rate: D("-0.0001"), Mark: D("59000")}}, nil
	}
	x.e.Tick()
	x.place(buy("BTC-PERP", "1"))
	sl := D("59900")
	if err := x.e.SetTPSL(1, "BTC-PERP", nil, &sl, "mark"); err != nil {
		t.Fatal(err)
	}
	w := x.acct().Wallet
	// 睡了 20 小时：醒来时交易所的"下次结算"已经推后了 3 期，价格也跌穿了止损
	x.now = x.now.Add(20 * time.Hour)
	x.market("BTC-PERP", "59000")
	x.books.UpdateTicker(V, "BTC-PERP", func(tk *book.Ticker) { tk.FundingRate = D("0.0003"); tk.NextFunding = t1 + 24*h })
	x.e.Tick()
	eq(t, "醒来这一下不能只收一笔", x.acct().Wallet.Sub(w), D("0"))
	if p := x.acct().Pos["BTC-PERP"]; p == nil || p.Qty.IsZero() {
		t.Fatal("等结算记录时不该触发止损：资金费要按睡着时的持仓算")
	}
	if n := x.e.settleGaps(context.Background()); n != 0 {
		t.Fatalf("取不到记录时不算补上：%d", n)
	}
	fail = false
	if n := x.e.settleGaps(context.Background()); n != 3 {
		t.Fatalf("应补 3 笔：%d", n)
	}
	eq(t, "3 笔都按真实费率", x.acct().Wallet.Sub(w), D("-6").Sub(D("12.2")).Add(D("5.9")))
	if n := x.e.settleGaps(context.Background()); n != 0 {
		t.Fatalf("补过不再补：%d", n)
	}
	x.e.Tick()
	x.e.Tick()
	if p := x.acct().Pos["BTC-PERP"]; p != nil && !p.Qty.IsZero() {
		t.Fatalf("补完资金费后止损应正常触发：%s", p.Qty)
	}
	if err := x.e.Check(); err != nil {
		t.Fatal(err)
	}
}

// 第 16 条：别人的事件不改变我的序号，页面不用跟着重新请求。
func TestViewSeqIsPerAccount(t *testing.T) {
	x := newEnv(t)
	x.e.Open(2, D("10000"))
	x.market("BTC-PERP", "60000")
	x.place(buy("BTC-PERP", "0.01"))
	v1, _ := x.e.View(1)
	if _, err := x.e.Place(2, buy("BTC-PERP", "0.01")); err != nil {
		t.Fatal(err)
	}
	v2, _ := x.e.View(1)
	if v1.Seq != v2.Seq {
		t.Fatalf("别人下单不该改变我的序号：%d → %d", v1.Seq, v2.Seq)
	}
	x.place(buy("BTC-PERP", "0.01"))
	v3, _ := x.e.View(1)
	if v3.Seq == v2.Seq {
		t.Fatal("自己下单序号要变")
	}
}

// 第 19 条：关机时把还没写库的事件写完。
func TestDrainWritesPending(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	x.place(buy("BTC-PERP", "0.01"))
	if n := x.e.Drain(time.Second); n != 0 {
		t.Fatalf("还剩 %d 条", n)
	}
	if len(x.evs) == 0 {
		t.Fatal("事件应该写进去了")
	}
	x.e.Persist = func([]Event) error { return fmt.Errorf("数据库挂了") }
	x.place(buy("BTC-PERP", "0.01"))
	if n := x.e.Drain(150 * time.Millisecond); n == 0 {
		t.Fatal("写不进去时要报告还剩多少")
	}
}

// 2.26：撤单后 5 秒内能撤回，只能一次；超过 5 秒、不是用户撤的都不行
func TestUndoCancel(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	x.e.Open(1, D("10000"))
	o, err := x.e.Place(1, OrderReq{Instrument: "BTC-PERP", Side: "buy", Type: Limit, Price: D("59000"), Qty: D("0.01"), TP: D("61000")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x.e.Cancel(1, o.ID); err != nil {
		t.Fatal(err)
	}
	n, err := x.e.UndoCancel(1, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n.ID == o.ID || !n.Price.Equal(o.Price) || !n.Qty.Equal(o.Qty) || !n.TP.Equal(o.TP) || n.Status != StNew {
		t.Fatalf("应按原样重新挂上：%+v", n)
	}
	if _, err := x.e.UndoCancel(1, o.ID); err == nil || err.(*Reject).Code != "UNDO_EXPIRED" {
		t.Fatalf("同一张只能撤回一次：%v", err)
	}
	// 超过 5 秒
	if _, err := x.e.Cancel(1, n.ID); err != nil {
		t.Fatal(err)
	}
	real := x.e.Now
	x.e.Now = func() time.Time { return real().Add(UndoWindow + time.Second) }
	if _, err := x.e.UndoCancel(1, n.ID); err == nil || err.(*Reject).Code != "UNDO_EXPIRED" {
		t.Fatalf("超过 5 秒不能撤回：%v", err)
	}
	x.e.Now = real
	// 系统撤的不算
	o2, _ := x.e.Place(1, OrderReq{Instrument: "BTC-PERP", Side: "buy", Type: Limit, Price: D("59000"), Qty: D("0.01")})
	if _, err := x.e.CancelAs(1, o2.ID, "EXEC_WINDOW"); err != nil {
		t.Fatal(err)
	}
	if _, err := x.e.UndoCancel(1, o2.ID); err == nil {
		t.Fatal("系统撤的不能撤回")
	}
	if _, err := x.e.UndoCancel(1, "nope"); err == nil || err.(*Reject).Code != "NOT_FOUND" {
		t.Fatalf("%v", err)
	}
	if err := x.e.Check(); err != nil {
		t.Fatal(err)
	}
}
