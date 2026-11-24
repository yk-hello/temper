package trading

import (
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"qs/internal/book"

	"github.com/shopspring/decimal"
)

func codeOf(err error) string {
	if r, ok := err.(*Reject); ok {
		return r.Code
	}
	return ""
}

// D103：priceMatch 按下单那一刻的盘口定价。对手价看对面第 N 档，排队价看自己这边第 N 档，不够 N 档取最远的。
func TestPriceMatch(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	x.setBook("BTC-PERP",
		lv("59999", "1", "59998", "1", "59997", "1", "59996", "1", "59995", "1", "59994", "1"),
		lv("60001", "1", "60002", "1", "60003", "1", "60004", "1", "60005", "1", "60006", "1"))
	want := map[string]string{"QUEUE": "59999", "QUEUE_5": "59995", "QUEUE_20": "59994", "OPPONENT_5": "60005", "OPPONENT_10": "60006"}
	for pm, px := range want {
		o, err := x.e.Place(1, OrderReq{Instrument: "BTC-PERP", Side: "buy", Type: Limit, Qty: D("0.01"), PriceMatch: pm})
		if err != nil {
			t.Fatalf("%s：%v", pm, err)
		}
		if !o.Price.Equal(D(px)) || o.PriceMatch != pm {
			t.Fatalf("%s 买单价格应为 %s，得到 %s", pm, px, o.Price)
		}
	}
	// 卖单的对手价是买一
	o, err := x.e.Place(1, OrderReq{Instrument: "BTC-PERP", Side: "sell", Type: Limit, Qty: D("0.01"), PriceMatch: "QUEUE"})
	if err != nil || !o.Price.Equal(D("60001")) {
		t.Fatalf("卖单排队价应为卖一 60001：%v %v", o, err)
	}
	if _, err := x.e.Place(1, OrderReq{Instrument: "BTC-PERP", Side: "buy", Type: Limit, Qty: D("0.01"), Price: D("59000"), PriceMatch: "QUEUE"}); codeOf(err) != "PRICE_MATCH_WITH_PRICE" {
		t.Fatalf("填了价格又要按盘口定价，应拒绝：%v", err)
	}
	if _, err := x.e.Place(1, OrderReq{Instrument: "BTC-PERP", Side: "buy", Type: Market, Qty: D("0.01"), PriceMatch: "QUEUE"}); codeOf(err) != "BAD_PRICE_MATCH" {
		t.Fatalf("市价单不能按盘口定价：%v", err)
	}
	if _, err := x.e.Place(1, OrderReq{Instrument: "BTC-PERP", Side: "buy", Type: Limit, Qty: D("0.01"), PriceMatch: "OPPONENT_3"}); codeOf(err) != "BAD_PRICE_MATCH" {
		t.Fatalf("没有 OPPONENT_3 这一档：%v", err)
	}
}

// D103：GTD 有效到某个时间，至少比现在晚 10 分钟，到点过期；重放后一样。
func TestGTD(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	now := x.now.UnixMilli()
	if _, err := x.e.Place(1, OrderReq{Instrument: "BTC-PERP", Side: "buy", Type: Limit, TIF: GTD, Price: D("59000"), Qty: D("0.01"), GoodTill: now + 5*60_000}); codeOf(err) != "GTD_TOO_SOON" {
		t.Fatalf("5 分钟太近，应拒绝：%v", err)
	}
	o, err := x.e.Place(1, OrderReq{Instrument: "BTC-PERP", Side: "buy", Type: Limit, TIF: GTD, Price: D("59000"), Qty: D("0.01"), GoodTill: now + 20*60_000 + 123})
	if err != nil {
		t.Fatal(err)
	}
	if o.GoodTill != (now+20*60_000+123)/1000*1000 {
		t.Fatalf("有效期应去掉毫秒：%d", o.GoodTill)
	}
	x.now = x.now.Add(19 * time.Minute)
	x.e.Tick()
	if x.e.accounts[1].Orders[o.ID] == nil {
		t.Fatal("还没到期就没了")
	}
	x.now = x.now.Add(2 * time.Minute)
	x.e.Tick()
	if x.e.accounts[1].Orders[o.ID] != nil {
		t.Fatal("到期应过期")
	}
	h := x.e.accounts[1].History[len(x.e.accounts[1].History)-1]
	if h.ID != o.ID || h.Status != StExpired || h.Reason != "GTD" {
		t.Fatalf("应记为已过期（GTD）：%+v", h)
	}
	x.e.Flush()
	raw, _ := json.Marshal(x.evs)
	var evs []Event
	_ = json.Unmarshal(raw, &evs)
	y := New(x.books, V, NewSpecs(LooseSpecs()), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := y.Restore(evs); err != nil {
		t.Fatal(err)
	}
	if y.accounts[1].Orders[o.ID] != nil {
		t.Fatal("重放后也应是过期的")
	}
}

// D103：条件单开了价格保护，标记价和最新价差超过 triggerProtect（5%）时不触发，靠拢后再触发；没开的照常触发。
func TestPriceProtect(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	cond := func(protect bool) *Order {
		return x.place(OrderReq{Instrument: "BTC-PERP", Side: "buy", Type: CondMarket, TriggerPx: D("61000"), Qty: D("0.01"), PriceProtect: protect})
	}
	guarded, plain := cond(true), cond(false)
	if !guarded.PriceProtect || plain.PriceProtect {
		t.Fatal("价格保护的设置要记在委托上")
	}
	// 标记价涨过触发价，但最新价插针到 65000（差 6.5%）
	x.market("BTC-PERP", "61100")
	x.books.UpdateTicker(V, "BTC-PERP", func(tk *book.Ticker) { tk.Last = decimal.RequireFromString("65100") })
	x.e.Tick()
	a := x.e.accounts[1]
	if a.Orders[plain.ID] != nil {
		t.Fatal("没开价格保护的应照常触发")
	}
	if a.Orders[guarded.ID] == nil || a.Orders[guarded.ID].Status != StNew {
		t.Fatal("开了价格保护：差太多不应触发")
	}
	// 最新价回来，差 1%：触发
	x.books.UpdateTicker(V, "BTC-PERP", func(tk *book.Ticker) { tk.Last = decimal.RequireFromString("61700") })
	x.e.Tick()
	if a.Orders[guarded.ID] != nil {
		t.Fatal("两者靠拢后应触发")
	}
	// 随单止盈止损跟着开仓单的价格保护设置
	o := x.place(OrderReq{Instrument: "BTC-PERP", Side: "buy", Type: Market, Qty: D("0.01"), SL: D("55000"), PriceProtect: true})
	_ = o
	found := false
	for _, c := range a.Orders {
		if c.ClosePos && c.Type == StopMarket {
			found = true
			if !c.PriceProtect {
				t.Fatal("随单止损应带上价格保护")
			}
		}
	}
	if !found {
		t.Fatal("应挂上随单止损")
	}
}
