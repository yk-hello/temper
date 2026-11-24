package trading

import (
	"testing"
	"time"
)

// 下单延迟（D94）：单子按"到交易所那一刻"的盘口成交，回报在一个往返之后才回来。
func TestLatencyFillsAtArrivalBook(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	x.e.Latency = func(int64) (time.Duration, time.Duration) { return 120 * time.Millisecond, 200 * time.Millisecond }
	go func() {
		time.Sleep(40 * time.Millisecond) // 单子还在路上，价格涨了
		x.market("BTC-PERP", "60300")
	}()
	t0 := time.Now()
	o := x.place(OrderReq{Instrument: "BTC-PERP", Side: "buy", Type: Market, Qty: D("0.01")})
	took := time.Since(t0)
	if !o.AvgPx.Equal(D("60300")) {
		t.Fatalf("应按到交易所时的价格成交：%s", o.AvgPx)
	}
	if took < 200*time.Millisecond || took > 600*time.Millisecond {
		t.Fatalf("回报应在一个往返之后：%v", took)
	}
	// 撤单也晚到：挂单在撤单路上成交了，撤单就扑空
	x.e.Latency = func(int64) (time.Duration, time.Duration) { return 80 * time.Millisecond, 80 * time.Millisecond }
	lim := x.place(OrderReq{Instrument: "BTC-PERP", Side: "buy", Type: Limit, TIF: GTC, Price: D("60000"), Qty: D("0.01")})
	go func() {
		time.Sleep(20 * time.Millisecond)
		x.market("BTC-PERP", "59990") // 卖一跌到挂单价下面：挂单成交
		x.e.Tick()
	}()
	if _, err := x.e.Cancel(1, lim.ID); err == nil {
		t.Fatal("撤单到的时候挂单已经成交了，应该撤不掉")
	}
}

// 交易所的下单频率限制：10 秒 300 张。
func TestExchangeRateLimit(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "60000")
	side := func(i int) string { return []string{"buy", "sell"}[i%2] } // 买卖交替，不会碰到单品种委托上限
	for i := 0; i < rate10s; i++ {
		if _, err := x.e.Place(1, OrderReq{Instrument: "BTC-PERP", Side: side(i), Type: Market, Qty: D("0.002")}); err != nil {
			t.Fatalf("第 %d 张：%v", i+1, err)
		}
	}
	r := x.reject("EXCHANGE_RATE_LIMIT", OrderReq{Instrument: "BTC-PERP", Side: "buy", Type: Market, Qty: D("0.002")})
	if r.Params["n10"] != "300" {
		t.Fatalf("参数：%+v", r)
	}
	x.now = x.now.Add(11 * time.Second) // 10 秒之后又能下
	x.market("BTC-PERP", "60000")
	x.place(OrderReq{Instrument: "BTC-PERP", Side: "buy", Type: Market, Qty: D("0.002")})
}
