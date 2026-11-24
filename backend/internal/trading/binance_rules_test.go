package trading

// 模拟盘逐条对齐币安的规则（用户 2026-11-09："特别是模拟盘做到完全仿真"）。
// 数字来自币安合约交易规则页和"杠杆与保证金"页（2026-11-09 读取），启动后以 exchangeInfo 的实时值为准。

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"qs/internal/book"
)

// 用币安真实规格（价格带 3%）的环境
func newRealEnv(t *testing.T) *env {
	x := &env{t: t, books: book.NewStore(), now: time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)}
	x.e = New(x.books, V, NewSpecs(DefaultSpecs()), slog.New(slog.NewTextHandler(io.Discard, nil)))
	x.e.Now = func() time.Time { return x.now }
	x.e.Persist = func(evs []Event) error { x.evs = append(x.evs, evs...); return nil }
	x.e.Open(1, D("10000"))
	return x
}

func TestBTCRealSpec(t *testing.T) {
	s := DefaultSpecs()["BTC-PERP"]
	if len(s.Brackets) != 12 || !s.Brackets[4].Floor.Equal(D("12000000")) || !s.Brackets[4].MMR.Equal(D("0.02")) || s.Brackets[4].MaxLev != 25 {
		t.Fatalf("BTC 第 5 档应是 1200 万起、2%%、25 倍：%+v", s.Brackets[4])
	}
	// 速算数和币安页面上的"维持保证金速算额"一致
	for i, want := range []string{"0", "300", "1500", "12000", "132000", "482000", "2982000", "14482000", "26482000", "41482000", "121482000", "421482000"} {
		eq(t, "BTC 速算数", s.Brackets[i].Cum, D(want))
	}
	if !s.MinNotional.Equal(D("50")) || !s.MarketMax.Equal(D("120")) || !s.MaxQty.Equal(D("1000")) || !s.LiqFee.Equal(D("0.0125")) {
		t.Fatalf("BTC 最小名义金额 50、市价单最多 120、限价单最多 1000、清算费率 1.25%%：%+v", s)
	}
}

func TestPriceBand3Percent(t *testing.T) {
	x := newRealEnv(t)
	x.market("BTC-PERP", "60000")
	x.reject("PRICE_OUT_OF_RANGE", limit("BTC-PERP", "buy", "58000", "0.01", GTC)) // 低 3.3%
	x.place(limit("BTC-PERP", "buy", "58500", "0.01", GTC))                        // 低 2.5%
	x.reject("PRICE_OUT_OF_RANGE", limit("BTC-PERP", "sell", "62000", "0.01", GTC))
}

func TestMarketTakeBound(t *testing.T) {
	x := newRealEnv(t)
	x.market("BTC-PERP", "60000")
	// 卖一只有 0.01，再往上是 4.2% 之外：市价单只吃到标记价 +3%，剩下的过期
	x.setBook("BTC-PERP", lv("59990", "5"), lv("60000", "0.01", "62500", "10"))
	o := x.place(buy("BTC-PERP", "1"))
	eq(t, "只成交保护范围内的", o.Filled, D("0.01"))
	if o.Status == StNew || o.Status == StPartial {
		t.Fatalf("剩下的应过期，现在是 %s", o.Status)
	}
	eq(t, "仓位", x.pos("BTC-PERP"), D("0.01"))
}

func TestMaxQty(t *testing.T) {
	x := newRealEnv(t)
	x.e.Open(2, D("100000000"))
	x.market("BTC-PERP", "60000")
	_, err := x.e.Place(2, buy("BTC-PERP", "121"))
	if rj, ok := err.(*Reject); !ok || rj.Code != "QTY_TOO_LARGE" || rj.Params["max"] != "120" {
		t.Fatalf("市价单超过 120 BTC 应拒绝：%v", err)
	}
	_, err = x.e.Place(2, limit("BTC-PERP", "buy", "59000", "1001", GTC))
	if rj, ok := err.(*Reject); !ok || rj.Code != "QTY_TOO_LARGE" || rj.Params["max"] != "1000" {
		t.Fatalf("限价单超过 1000 BTC 应拒绝：%v", err)
	}
}

func TestOpenOrderLimitsPerSymbol(t *testing.T) {
	x := newRealEnv(t)
	x.market("BTC-PERP", "60000")
	x.market("ETH-PERP", "3000")
	sp, _ := x.e.Specs.Get("BTC-PERP")
	sp.MaxOrders, sp.MaxAlgo = 3, 1
	for _, px := range []string{"59000", "59100", "59200"} {
		x.place(limit("BTC-PERP", "buy", px, "0.002", GTC))
	}
	rj := x.reject("TOO_MANY_ORDERS", limit("BTC-PERP", "buy", "59300", "0.002", GTC))
	if rj.Params["max"] != "3" {
		t.Fatalf("%v", rj.Params)
	}
	x.place(limit("ETH-PERP", "buy", "2950", "0.01", GTC)) // 上限按品种算，ETH 不受影响
	// 条件单另有上限：撤一张普通单腾出位置，下一张条件单；再撤一张，第二张条件单还是被拒
	cancelOne := func() {
		for _, o := range sortedOrders(x.acct()) {
			if o.Instrument == "BTC-PERP" && !o.conditional() {
				_, _ = x.e.Cancel(1, o.ID)
				return
			}
		}
	}
	cancelOne()
	cond := OrderReq{Instrument: "BTC-PERP", Side: "buy", Type: CondMarket, Qty: D("0.002"), TriggerPx: D("61000")}
	x.place(cond)
	cancelOne()
	cond.TriggerPx = D("61500")
	x.reject("TOO_MANY_CONDITIONAL", cond)
}

func TestOpenLoss(t *testing.T) {
	x := newRealEnv(t)
	x.market("BTC-PERP", "60000")
	// 卖一 60500：买单挂在 60200 不会成交，但比标记价高 200，这部分算开单损失
	x.setBook("BTC-PERP", lv("59990", "5"), lv("60500", "5"))
	x.place(limit("BTC-PERP", "buy", "60200", "0.1", GTC))
	v, _ := x.e.View(1)
	eq(t, "挂单保证金 = 6020 ÷ 10 倍 + 0.1 × 200", v.OrderMargin, D("622"))
	// 价格比标记价好的买单没有开单损失
	x2 := newRealEnv(t)
	x2.market("BTC-PERP", "60000")
	x2.place(limit("BTC-PERP", "buy", "59000", "0.1", GTC))
	v2, _ := x2.e.View(1)
	eq(t, "没有开单损失", v2.OrderMargin, D("590"))
}

func TestClearanceFee(t *testing.T) {
	// 正常费率（BTC 1.25%）比第一档维持保证金率（0.4%）高：小仓位整个强平，清算费最多扣到逐仓保证金为 0
	y := newEnv(t)
	y.market("BTC-PERP", "60000")
	_ = y.e.SetMode(1, "BTC-PERP", "isolated")
	y.place(buy("BTC-PERP", "1")) // 逐仓保证金 6000，强平价 54221.1
	w := y.acct().Wallet
	y.market("BTC-PERP", "54200")
	y.e.Tick()
	if !y.pos("BTC-PERP").IsZero() {
		t.Fatal("应整个强平")
	}
	// 平仓亏 5800，剩 200；清算费 54200 × 1.25% = 677.5 > 200，只扣 200：逐仓保证金 6000 全没了
	eq(t, "清算费扣到 0 为止", y.acct().Wallet.Sub(w), D("-6000"))
	// 费率比维持保证金率低时（故意设成 0.1%）：减一部分就够，按币安的做法部分强平（D103），清算费只按平掉的部分收
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	_ = x.e.SetMode(1, "BTC-PERP", "isolated")
	sp, _ := x.e.Specs.Get("BTC-PERP")
	sp.LiqFee = D("0.001")
	x.place(buy("BTC-PERP", "1"))
	x.market("BTC-PERP", "54200")
	x.e.Tick()
	if q := x.pos("BTC-PERP"); q.IsZero() || !q.LessThan(D("1")) {
		t.Fatalf("费率低时应部分强平，剩 %s", q)
	}
	n := lastLiq(x.acct())
	if n == nil || n.Reason != "partial" {
		t.Fatalf("应是部分强平：%+v", n)
	}
	eq(t, "清算费 = 平掉的名义价值 × 0.1%", n.Amount, n.Qty.Mul(D("54200")).Mul(D("0.001")))
	for _, e := range []*Engine{x.e, y.e} {
		if err := e.Check(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFetchBracketsSigned(t *testing.T) {
	const key, secret = "test-key", "test-secret"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fapi/v1/leverageBracket" || r.Header.Get("X-MBX-APIKEY") != key {
			w.WriteHeader(401)
			return
		}
		// 签名 = HMAC-SHA256(去掉 signature 的查询串, secret)
		raw := r.URL.RawQuery
		i := strings.Index(raw, "&signature=")
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(raw[:i]))
		if i < 0 || raw[i+len("&signature="):] != hex.EncodeToString(mac.Sum(nil)) {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"code":-1022,"msg":"Signature for this request is not valid."}`))
			return
		}
		_, _ = w.Write([]byte(`[{"symbol":"ETHUSDT","brackets":[{"bracket":1,"initialLeverage":150,"notionalCap":300000,"notionalFloor":0,"maintMarginRatio":0.004,"cum":0},{"bracket":2,"initialLeverage":100,"notionalCap":800000,"notionalFloor":300000,"maintMarginRatio":0.005,"cum":300}]}]`))
	}))
	defer srv.Close()
	sp := NewSpecs(DefaultSpecs())
	if n, err := sp.FetchBrackets(context.Background(), srv.URL, "", ""); n != 0 || err != nil {
		t.Fatal("没有密钥不调用")
	}
	n, err := sp.FetchBrackets(context.Background(), srv.URL, key, secret)
	if err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	eth, _ := sp.Get("ETH-PERP")
	if eth.BracketSrc != "exchange" || len(eth.Brackets) != 2 || eth.Brackets[0].MaxLev != 150 || !eth.Brackets[1].Cum.Equal(D("300")) {
		t.Fatalf("ETH 分档用交易所的：%+v", eth.Brackets)
	}
	if _, err := sp.FetchBrackets(context.Background(), srv.URL, key, "wrong"); err == nil || !strings.Contains(err.Error(), "-1022") {
		t.Fatalf("签名错了要报出来：%v", err)
	}
}

func TestConditionalNoMarginCheckUntilTrigger(t *testing.T) {
	// 币安 2025-12 起：条件单触发前不查保证金。下单时钱不够也能挂上，触发时不够就过期并通知
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	o := x.place(OrderReq{Instrument: "BTC-PERP", Side: "buy", Type: CondMarket, TriggerPx: D("61000"), Qty: D("5")}) // 要 30500 保证金，账户只有 1 万
	if o.Status != StNew {
		t.Fatalf("应该挂上：%s", o.Status)
	}
	x.market("BTC-PERP", "61000")
	x.e.Tick()
	h := x.e.findOrder(x.acct(), o.ID)
	if h.Status != StExpired || h.Reason != "INSUFFICIENT_MARGIN" {
		t.Fatalf("触发时保证金不够应过期：%+v", h)
	}
	if !x.pos("BTC-PERP").IsZero() {
		t.Fatal("不该开仓")
	}
}
