package liqfeed

import "testing"

var inst = map[string]string{"BTCUSDT": "BTC-PERP", "DOGEUSDT": "DOGE-PERP", "ETHUSDT": "ETH-PERP"}

// 用 11-13 实际收到的推送格式
func TestParseBybit(t *testing.T) {
	got := ParseBybit([]byte(`{"topic":"allLiquidation.ETHUSDT","type":"snapshot","ts":1794571370630,"data":[{"T":1794571370332,"s":"ETHUSDT","S":"Sell","v":"1.75","p":"1969.17"},{"T":1794571370333,"s":"ETHUSDT","S":"Buy","v":"2","p":"1960"},{"T":1,"s":"XYZUSDT","S":"Buy","v":"1","p":"1"}]}`), inst)
	if len(got) != 2 || got[0].Side != "short" || got[1].Side != "long" || got[0].Instrument != "ETH-PERP" || got[0].Venue != "bybit" || got[0].USD != 1.75*1969.17 {
		t.Fatalf("%+v", got)
	}
	if ParseBybit([]byte(`{"success":true,"op":"subscribe"}`), inst) != nil {
		t.Fatal("订阅回执不算")
	}
}

func TestParseOKX(t *testing.T) {
	cv := map[string]float64{"DOGE-USDT-SWAP": 1000, "BTC-USDT-SWAP": 0.01}
	got := ParseOKX([]byte(`{"arg":{"channel":"liquidation-orders","instType":"SWAP"},"data":[
		{"details":[{"bkLoss":"0","bkPx":"0.06539","ccy":"","posSide":"short","side":"buy","sz":"6.98","ts":"1794571373948"}],"instFamily":"DOGE-USDT","instId":"DOGE-USDT-SWAP","instType":"SWAP","uly":"DOGE-USDT"},
		{"details":[{"bkPx":"68000","posSide":"net","side":"sell","sz":"3","ts":"1794571373949"}],"instId":"BTC-USDT-SWAP"},
		{"details":[{"bkPx":"68000","side":"sell","sz":"3","ts":"1"}],"instId":"BTC-USD-SWAP"},
		{"details":[{"bkPx":"1","side":"sell","sz":"3","ts":"1"}],"instId":"HYPE-USDT-SWAP"}]}`), inst, cv)
	if len(got) != 2 {
		t.Fatalf("只要 USDT 本位、只要在看的币：%+v", got)
	}
	if d := got[0]; d.Instrument != "DOGE-PERP" || d.Side != "short" || d.Q != 6980 || d.T != 1794571373948 || d.Venue != "okx" {
		t.Fatalf("张数乘每张的币数、buy = 空单被强平：%+v", d)
	}
	if b := got[1]; b.Side != "long" || b.Q != 0.03 || b.USD != 0.03*68000 {
		t.Fatalf("sell = 多单被强平：%+v", b)
	}
}
