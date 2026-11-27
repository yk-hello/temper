package market

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// 样本取自币安官方文档的消息格式。
const (
	sampleAggTrade = `{"stream":"btcusdt@aggTrade","data":{"e":"aggTrade","E":123456789,"s":"BTCUSDT","a":5933014,"p":"68250.10","q":"0.012","f":100,"l":105,"T":123456785,"m":true}}`
	sampleBook     = `{"stream":"btcusdt@bookTicker","data":{"e":"bookTicker","u":400900217,"E":1568014460893,"T":1568014460891,"s":"BTCUSDT","b":"68250.00","B":"31.21","a":"68250.20","A":"40.66"}}`
	sampleMark     = `{"stream":"btcusdt@markPrice@1s","data":{"e":"markPriceUpdate","E":1562305380000,"s":"BTCUSDT","p":"68251.33","i":"68249.90","P":"68260.00","r":"0.00038167","T":1562306400000}}`
	sampleTicker   = `{"stream":"btcusdt@ticker","data":{"e":"24hrTicker","E":123456789,"s":"BTCUSDT","p":"0.0015","P":"2.512","w":"0.0018","c":"68250.10","Q":"10","o":"66577.00","h":"68900.00","l":"66100.00","v":"10000","q":"18000000.00","O":0,"C":86400000,"F":0,"L":18150,"n":18151}}`
)

func TestParseBinance(t *testing.T) {
	ev, ok := ParseBinance([]byte(sampleAggTrade))
	if !ok || ev.Kind != KindTrade || ev.Instrument != "BTC-PERP" || ev.Price != "68250.10" || ev.Qty != "0.012" || ev.Side != Sell || ev.Time != 123456785 {
		t.Fatalf("aggTrade parsed wrong: %+v ok=%v", ev, ok)
	}
	ev, ok = ParseBinance([]byte(sampleBook))
	if !ok || ev.Kind != KindBook || ev.Bid != "68250.00" || ev.BidQty != "31.21" || ev.Ask != "68250.20" || ev.AskQty != "40.66" {
		t.Fatalf("bookTicker parsed wrong: %+v", ev)
	}
	ev, ok = ParseBinance([]byte(sampleMark))
	if !ok || ev.Kind != KindMark || ev.Mark != "68251.33" || ev.FundingRate != "0.00038167" || ev.NextFundingTime != 1562306400000 {
		t.Fatalf("markPrice parsed wrong: %+v", ev)
	}
	// 关键用例：c/C、q/Q、l/L 大小写不同含义不同，不能串
	ev, ok = ParseBinance([]byte(sampleTicker))
	if !ok || ev.Kind != KindTicker || ev.Price != "68250.10" || ev.Change24hPct != "2.512" ||
		ev.QuoteVolume24h != "18000000.00" || ev.High24h != "68900.00" || ev.Low24h != "66100.00" {
		t.Fatalf("ticker parsed wrong: %+v", ev)
	}
	for _, bad := range []string{``, `{}`, `{"data":{}}`, `{"data":{"e":"unknown","s":"BTCUSDT"}}`, `not json`} {
		if _, ok := ParseBinance([]byte(bad)); ok {
			t.Fatalf("expected reject for %q", bad)
		}
	}
}

func TestBinanceURLs(t *testing.T) {
	b := &Binance{Symbols: []string{"BTCUSDT", "ETHUSDT"}}
	u := b.URLs()
	if u["public"] != "wss://fstream.binance.com/public/stream?streams=btcusdt@depth20@100ms/ethusdt@depth20@100ms" {
		t.Fatalf("public url: %s", u["public"])
	}
	if !strings.HasPrefix(u["market"], "wss://fstream.binance.com/market/stream?streams=btcusdt@aggTrade/btcusdt@markPrice@1s/btcusdt@ticker/btcusdt@forceOrder/ethusdt@aggTrade") {
		t.Fatalf("market url: %s", u["market"])
	}
	// 现货默认走 443（有的网络拦 9443）；BINANCE_SPOT_WS_URL 能换地址，合约不受影响
	if u := (&Binance{Symbols: []string{"BTCUSDT"}, Spot: true}).URLs(); u["spot"] != "wss://stream.binance.com:443/stream?streams=btcusdt@aggTrade/btcusdt@depth20@100ms/btcusdt@ticker" {
		t.Fatalf("spot url: %s", u["spot"])
	}
	if u := (&Binance{Symbols: []string{"BTCUSDT"}, Spot: true, BaseURL: "wss://data-stream.binance.vision:443/"}).URLs(); !strings.HasPrefix(u["spot"], "wss://data-stream.binance.vision:443/stream?streams=btcusdt@aggTrade") {
		t.Fatalf("spot url override: %s", u["spot"])
	}
}

func TestCanonicalPerp(t *testing.T) {
	for in, want := range map[string]string{"BTCUSDT": "BTC-PERP", "ethusdt": "ETH-PERP", "1000PEPEUSDT": "1000PEPE-PERP", "USDT": "USDT"} {
		if got := CanonicalPerp(in); got != want {
			t.Errorf("%s → %s, want %s", in, got, want)
		}
	}
}

func TestHubConflatesAndNeverBlocks(t *testing.T) {
	h := NewHub([]string{"BTC-PERP"}, time.Hour)
	fast, cancelFast := h.Subscribe()
	defer cancelFast()
	_, cancelSlow := h.Subscribe() // 从不读取的慢订阅者
	defer cancelSlow()

	// 1000 条成交只应合并成 1 帧，并统计到 1000 笔
	for i := 0; i < 1000; i++ {
		h.Publish(Event{Kind: KindTrade, Instrument: "BTC-PERP", Price: "1", Side: Buy, Time: int64(i)})
	}
	h.Publish(Event{Kind: KindTrade, Instrument: "BTC-PERP", Price: "2", Side: Sell, Time: 1000})
	for i := 0; i < 10; i++ { // 多次 flush 不能因为慢订阅者阻塞
		h.flush()
		h.Publish(Event{Kind: KindBook, Instrument: "BTC-PERP", Bid: "1", Ask: "3"})
	}
	var snap Snapshot
	if err := json.Unmarshal(<-fast.C, &snap); err != nil {
		t.Fatal(err)
	}
	st := snap.Instruments[0]
	if st.Last != "2" || st.LastSide != Sell || st.Trades != 1001 {
		t.Fatalf("unexpected first frame: %+v", st)
	}
	// 没有变化时不推送：先把最后一次的变化推掉、清空，再连续 flush 两次
	h.flush()
	for len(fast.C) > 0 {
		<-fast.C
	}
	h.flush()
	h.flush()
	if len(fast.C) != 0 {
		t.Fatal("frames pushed without changes")
	}
}

func TestSimProducesAllKinds(t *testing.T) {
	h := NewHub([]string{"BTC-PERP", "ETH-PERP"}, time.Hour)
	ctx, cancel := context.WithTimeout(context.Background(), 1300*time.Millisecond)
	defer cancel()
	(&Sim{Symbols: []string{"BTCUSDT", "ETHUSDT"}, Seed: 1, Tick: 5 * time.Millisecond}).Run(ctx, &Fanout{Primary: "sim", Hub: h})
	snap := h.Snapshot()
	for _, st := range snap.Instruments {
		if st.Last == "" || st.Bid == "" || st.Mark == "" || st.Change24hPct == "" {
			t.Fatalf("sim missing fields: %+v", st)
		}
	}
}

func TestParseBinanceDepthAndLiquidation(t *testing.T) {
	ev, ok := ParseBinance([]byte(`{"stream":"btcusdt@depth20@100ms","data":{"e":"depthUpdate","E":1571889248277,"T":1571889248276,"s":"BTCUSDT","U":1,"u":2,"pu":0,"b":[["7403.89","0.002"],["7403.90","3.906"]],"a":[["7405.96","3.340"]]}}`))
	if !ok || ev.Kind != KindDepth || len(ev.Bids) != 2 || ev.Asks[0][0] != "7405.96" || ev.Time != 1571889248276 {
		t.Fatalf("depth: %+v", ev)
	}
	ev, ok = ParseBinance([]byte(`{"stream":"btcusdt@forceOrder","data":{"e":"forceOrder","E":1568014460893,"o":{"s":"BTCUSDT","S":"SELL","o":"LIMIT","f":"IOC","q":"0.014","p":"9910","ap":"9910.5","X":"FILLED","l":"0.014","z":"0.014","T":1568014460893}}}`))
	if !ok || ev.Kind != KindLiquidation || ev.Side != Sell || ev.Price != "9910.5" || ev.Qty != "0.014" || ev.Instrument != "BTC-PERP" {
		t.Fatalf("liquidation: %+v", ev)
	}
}

func TestParseHyperliquid(t *testing.T) {
	evs := ParseHyperliquid([]byte(`{"channel":"l2Book","data":{"coin":"BTC","time":1700000000000,"levels":[[{"px":"77447","sz":"0.9","n":3}],[{"px":"77448","sz":"1.2","n":2}]]}}`))
	if len(evs) != 2 || evs[0].Kind != KindDepth || evs[0].Bids[0][0] != "77447" || evs[1].Kind != KindBook || evs[1].Ask != "77448" || evs[0].Venue != "hyperliquid" {
		t.Fatalf("l2Book: %+v", evs)
	}
	evs = ParseHyperliquid([]byte(`{"channel":"trades","data":[{"coin":"ETH","side":"A","px":"2900.1","sz":"0.5","time":1700000000001,"hash":"0x","tid":1}]}`))
	if len(evs) != 1 || evs[0].Side != Sell || evs[0].Instrument != "ETH-PERP" {
		t.Fatalf("trades: %+v", evs)
	}
	evs = ParseHyperliquid([]byte(`{"channel":"activeAssetCtx","data":{"coin":"BTC","ctx":{"funding":"0.0000125","openInterest":"30000","prevDayPx":"76000","dayNtlVlm":"1000000","markPx":"77443","midPx":"77447.5"}}}`))
	if len(evs) != 2 || evs[0].Mark != "77443" || evs[0].FundingRate != "0.0000125" || evs[1].Change24hPct != "1.899" {
		t.Fatalf("ctx: %+v", evs)
	}
}

// 最后一笔成交的时间只跟着成交走：盘口、标记价格、24 小时统计只改 UpdatedAt（前端拿 LastAt 判断实时价落在哪一根 K 线）
func TestHubLastAtFollowsTradesOnly(t *testing.T) {
	h := NewHub([]string{"BTC-PERP"}, time.Hour)
	h.Publish(Event{Kind: KindTrade, Instrument: "BTC-PERP", Price: "100", Side: Buy, Time: 60_000})
	h.Publish(Event{Kind: KindBook, Instrument: "BTC-PERP", Bid: "99", Ask: "101", Time: 120_500})
	h.Publish(Event{Kind: KindMark, Instrument: "BTC-PERP", Mark: "100.1", Time: 121_000})
	st := h.Snapshot().Instruments[0]
	if st.LastAt != 60_000 || st.UpdatedAt != 121_000 {
		t.Fatalf("LastAt = %d（应 60000），UpdatedAt = %d（应 121000）", st.LastAt, st.UpdatedAt)
	}
	h.Publish(Event{Kind: KindTrade, Instrument: "BTC-PERP", Price: "100.2", Side: Sell, Time: 121_200})
	if st := h.Snapshot().Instruments[0]; st.LastAt != 121_200 || st.Last != "100.2" {
		t.Fatalf("新成交以后 LastAt = %d、Last = %s", st.LastAt, st.Last)
	}
}
