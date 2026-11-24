package binance

import (
	"context"
	"net/url"
	"testing"
	"time"
)

func TestSignedRequestsAndClock(t *testing.T) {
	f := NewFake("k", "s", 10000)
	defer f.Close()
	f.SetPrice("BTCUSDT", 60000)
	c := f.Client()
	ctx := context.Background()
	a, err := c.Account(ctx)
	if err != nil || a.TotalWalletBalance.F() != 10000 {
		t.Fatalf("账户：%+v %v", a, err)
	}
	// 本机时钟快了 10 秒：第一次被拒（-1021），对时后自动重试成功
	c.Now = func() time.Time { return time.Now().Add(10 * time.Second) }
	c.synced = true
	if _, err := c.Account(ctx); err != nil {
		t.Fatalf("对时后应成功：%v", err)
	}
	// 密钥错了、签名错了
	bad := f.Client()
	bad.Secret = "wrong"
	if _, err := bad.Account(ctx); Code(err) != -1022 {
		t.Fatalf("签名错：%v", err)
	}
	bad = f.Client()
	bad.Key = "nope"
	if _, err := bad.Account(ctx); Code(err) != -2015 {
		t.Fatalf("密钥错：%v", err)
	}
}

func TestOrdersAndAlgo(t *testing.T) {
	f := NewFake("k", "s", 10000)
	defer f.Close()
	f.SetPrice("BTCUSDT", 60000)
	c := f.Client()
	ctx := context.Background()
	if err := c.SetLeverage(ctx, "BTCUSDT", 10); err != nil {
		t.Fatal(err)
	}
	if err := c.SetMarginType(ctx, "BTCUSDT", false); err != nil { // 已经是全仓：-4046 当作成功
		t.Fatal(err)
	}
	o, err := c.NewOrder(ctx, url.Values{"symbol": {"BTCUSDT"}, "side": {"BUY"}, "type": {"MARKET"}, "quantity": {"0.1"}, "newClientOrderId": {"qs-m-1"}})
	if err != nil || o.Status != "FILLED" || o.AvgPrice.F() != 60000 {
		t.Fatalf("市价单：%+v %v", o, err)
	}
	ps, _ := c.Positions(ctx)
	if len(ps) != 1 || ps[0].PositionAmt.F() != 0.1 {
		t.Fatalf("仓位：%+v", ps)
	}
	// 老接口下止损单报 -4120（币安 2025-12 起止损止盈只能走 Algo）
	if _, err := c.NewOrder(ctx, url.Values{"symbol": {"BTCUSDT"}, "side": {"SELL"}, "type": {"STOP_MARKET"}, "quantity": {"0.1"}}); Code(err) != -4120 {
		t.Fatalf("老接口止损：%v", err)
	}
	a, err := c.NewAlgo(ctx, url.Values{"symbol": {"BTCUSDT"}, "side": {"SELL"}, "type": {"STOP_MARKET"}, "closePosition": {"true"}, "triggerPrice": {"58000"}, "workingType": {"MARK_PRICE"}})
	if err != nil || a.AlgoStatus != "NEW" || a.TriggerPrice.F() != 58000 {
		t.Fatalf("Algo 止损：%+v %v", a, err)
	}
	if _, err := c.NewAlgo(ctx, url.Values{"symbol": {"BTCUSDT"}, "side": {"SELL"}, "type": {"STOP_MARKET"}, "closePosition": {"true"}, "triggerPrice": {"61000"}}); Code(err) != -2021 {
		t.Fatalf("会立刻触发的止损：%v", err)
	}
	open, _ := c.OpenAlgoOrders(ctx)
	if len(open) != 1 {
		t.Fatalf("挂着的条件单：%+v", open)
	}
	f.SetPrice("BTCUSDT", 57900) // 触发止损，平仓
	ps, _ = c.Positions(ctx)
	if len(ps) != 0 {
		t.Fatalf("止损后应平仓：%+v", ps)
	}
	tr, _ := c.UserTrades(ctx, "BTCUSDT", 0, 0)
	if len(tr) != 2 || tr[1].RealizedPnl.F() >= 0 {
		t.Fatalf("两笔成交，第二笔亏：%+v", tr)
	}
	// 限价单挂着、改价、撤单
	lo, err := c.NewOrder(ctx, url.Values{"symbol": {"BTCUSDT"}, "side": {"BUY"}, "type": {"LIMIT"}, "timeInForce": {"GTC"}, "quantity": {"0.01"}, "price": {"57000"}})
	if err != nil || lo.Status != "NEW" {
		t.Fatalf("限价单：%+v %v", lo, err)
	}
	if _, err := c.ModifyOrder(ctx, "BTCUSDT", lo.OrderID, "BUY", "0.01", "57100"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CancelOrder(ctx, "BTCUSDT", lo.OrderID); err != nil {
		t.Fatal(err)
	}
	if oo, _ := c.OpenOrders(ctx); len(oo) != 0 {
		t.Fatalf("撤掉了：%+v", oo)
	}
	// 保证金不够
	if _, err := c.NewOrder(ctx, url.Values{"symbol": {"BTCUSDT"}, "side": {"BUY"}, "type": {"MARKET"}, "quantity": {"50"}}); Code(err) != -2019 {
		t.Fatalf("保证金不够：%v", err)
	}
}

func TestRestrictions(t *testing.T) {
	f := NewFake("k", "s", 0)
	defer f.Close()
	f.Withdraw = true
	r, err := f.Client().Restrictions(context.Background())
	if err != nil || !r.EnableWithdrawals || !r.EnableFutures {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestUserStream(t *testing.T) {
	f := NewFake("k", "s", 10000)
	defer f.Close()
	f.SetPrice("ETHUSDT", 3000)
	c := f.Client()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	got := make(chan Event, 16)
	up := make(chan bool, 4)
	go c.Stream(ctx, func(e Event) { got <- e }, func(ok bool, _ string) { up <- ok })
	select {
	case ok := <-up:
		if !ok {
			t.Fatal("没连上")
		}
	case <-ctx.Done():
		t.Fatal("连不上用户数据流")
	}
	if _, err := c.NewOrder(ctx, url.Values{"symbol": {"ETHUSDT"}, "side": {"BUY"}, "type": {"MARKET"}, "quantity": {"1"}}); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	var trade *OrderUpdate
	for len(seen) < 2 || trade == nil {
		select {
		case e := <-got:
			seen[e.Type] = true
			if e.Type == "ORDER_TRADE_UPDATE" {
				if u, _ := ParseOrderUpdate(e.Raw); u != nil && u.ExecType == "TRADE" {
					trade = u
				}
			}
		case <-ctx.Done():
			t.Fatalf("事件：%v", seen)
		}
	}
	if !seen["ACCOUNT_UPDATE"] || trade.LastQty.F() != 1 || trade.LastPrice.F() != 3000 || trade.TradeID == 0 {
		t.Fatalf("成交事件：%+v %v", trade, seen)
	}
}
