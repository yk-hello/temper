package market

import (
	"testing"
	"time"
)

func TestMeter(t *testing.T) {
	now := time.UnixMilli(1_000_000)
	m := &Meter{Now: func() time.Time { return now }}
	for i := 0; i < 10; i++ {
		m.Add(Event{Kind: KindTrade, Venue: "binance-usdm", Instrument: "BTC-PERP", Time: now.UnixMilli() - 20})
	}
	m.Add(Event{Kind: KindDepth, Venue: "binance-usdm", Instrument: "BTC-SPOT", Time: now.UnixMilli()})
	m.Add(Event{Kind: KindTrade, Venue: "binance-usdm", Instrument: "BTC-SPOT", Time: now.UnixMilli() - 7})
	// 重连后补发的旧成交，延迟封顶，不把读数顶上去
	m.Add(Event{Kind: KindTrade, Venue: "hyperliquid", Instrument: "BTC-PERP", Time: now.UnixMilli() - 60_000})

	now = now.Add(2 * time.Second)
	got := map[string]LineStat{}
	for _, l := range m.Stats() {
		got[l.Venue] = l
	}
	if got["binance-usdm"].Rate != 5 || got["binance-usdm"].Trades != 5 {
		t.Fatalf("合约 %+v", got["binance-usdm"])
	}
	if got["binance-usdm"].Ms != 20 {
		t.Fatalf("合约延迟 %+v", got["binance-usdm"])
	}
	// 现货单独一条线，depth20 的时间戳是本机填的，不算延迟
	if s := got["binance-spot"]; s.Total != 2 || s.Ms != 7 {
		t.Fatalf("现货 %+v", s)
	}
	if h := got["hyperliquid"]; h.Ms != 5000 {
		t.Fatalf("封顶 %+v", h)
	}
	// 没有新消息：下一轮速率归零，不留着旧数
	now = now.Add(2 * time.Second)
	for _, l := range m.Stats() {
		if l.Rate != 0 {
			t.Fatalf("%+v", l)
		}
	}
}
