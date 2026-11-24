package marketmap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeFetch struct {
	tickers []Ticker
	coins   []Coin
	err     error
}

func (f *fakeFetch) Tickers(context.Context) ([]Ticker, error) { return f.tickers, f.err }
func (f *fakeFetch) Coins(context.Context) ([]Coin, error)     { return f.coins, nil }
func (f *fakeFetch) VolumeRatio(_ context.Context, sym string) (float64, error) {
	if sym == "BTCUSDT" {
		return 1.5, nil
	}
	return 0, nil
}

func newService(f Fetcher) *Service {
	s := &Service{F: f, Source: "test", Pace: time.Millisecond}
	return s
}

// 只显示目录里的币，门槛以下的不显示，成交额大的在前面，板块带上
func TestOnlyCoinsAboveMinimum(t *testing.T) {
	s := newService(&fakeFetch{coins: []Coin{{"BTCUSDT", "PoW"}, {"ETHUSDT", "Layer-1"}, {"DUSTUSDT", "Meme"}}})
	s.refreshCoins(context.Background())
	s.Update([]Ticker{
		{Symbol: "ETHUSDT", Last: 2000, ChgPct: -7.8, QuoteVol: 13e9},
		{Symbol: "BTCUSDT", Last: 70000, ChgPct: -5.7, QuoteVol: 20e9},
		{Symbol: "DUSTUSDT", Last: 1, ChgPct: 9, QuoteVol: 9_999_999},  // 差一点到门槛
		{Symbol: "NVDAUSDT", Last: 180, ChgPct: -7.6, QuoteVol: 700e6}, // 股票合约：不在目录里
		{Symbol: "XAUUSDT", Last: 4000, ChgPct: 0.3, QuoteVol: 3e9},    // 黄金合约：不在目录里
	})
	got := s.Current()
	if got.State != "ok" || len(got.Nodes) != 2 {
		t.Fatalf("状态 %s，%d 个，应 ok、2 个：%+v", got.State, len(got.Nodes), got.Nodes)
	}
	if got.Nodes[0].S != "BTC" || got.Nodes[0].I != "BTC-PERP" || got.Nodes[0].Sec != "PoW" || got.Nodes[1].S != "ETH" {
		t.Fatalf("顺序或字段不对：%+v", got.Nodes)
	}
	if got.Total != 3 || got.MinVol != 10_000_000 {
		t.Fatalf("总数 %d、门槛 %v，应 3、1000 万", got.Total, got.MinVol)
	}
}

// 重点币种带标记，成交额不到门槛也显示
func TestFocusCoins(t *testing.T) {
	s := newService(&fakeFetch{coins: []Coin{{"BTCUSDT", "PoW"}, {"DOGEUSDT", "Meme"}, {"AUSDT", ""}}})
	s.Focus = []string{"BTCUSDT", "DOGEUSDT"}
	s.refreshCoins(context.Background())
	s.Update([]Ticker{
		{Symbol: "BTCUSDT", Last: 1, QuoteVol: 2e10},
		{Symbol: "DOGEUSDT", Last: 1, QuoteVol: 1e6}, // 低于门槛
		{Symbol: "AUSDT", Last: 1, QuoteVol: 1e6},    // 低于门槛，也不是重点
	})
	n := s.Current().Nodes
	if len(n) != 2 || !n[0].F || !n[1].F || n[1].S != "DOGE" {
		t.Fatalf("重点币种不对：%+v", n)
	}
}

// 目录没到之前什么都不显示：不能先把股票当币摆上去（这是原来的错误）
func TestNothingBeforeCatalog(t *testing.T) {
	s := newService(&fakeFetch{})
	s.Update([]Ticker{{Symbol: "NVDAUSDT", Last: 180, QuoteVol: 700e6}})
	if got := s.Current(); got.State != "loading" || len(got.Nodes) != 0 {
		t.Fatalf("目录没到时应 loading、没有币：%+v", got)
	}
}

// 币种会变：每轮重新筛，跌破门槛的下去，涨上来的进来
func TestSetFollowsVolume(t *testing.T) {
	s := newService(&fakeFetch{coins: []Coin{{"AUSDT", ""}, {"BUSDT", ""}}})
	s.refreshCoins(context.Background())
	s.Update([]Ticker{{Symbol: "AUSDT", Last: 1, QuoteVol: 2e7}, {Symbol: "BUSDT", Last: 1, QuoteVol: 1e6}})
	if n := s.Current().Nodes; len(n) != 1 || n[0].S != "A" {
		t.Fatalf("第一轮：%+v", n)
	}
	s.Update([]Ticker{{Symbol: "AUSDT", Last: 1, QuoteVol: 1e6}, {Symbol: "BUSDT", Last: 1, QuoteVol: 2e7}})
	if n := s.Current().Nodes; len(n) != 1 || n[0].S != "B" {
		t.Fatalf("第二轮：%+v", n)
	}
}

// 成交额倍数算好后带上；没算出来的没有这个字段
func TestRatioAttached(t *testing.T) {
	f := &fakeFetch{coins: []Coin{{"BTCUSDT", "PoW"}, {"ETHUSDT", "Layer-1"}}, tickers: []Ticker{{Symbol: "BTCUSDT", Last: 1, QuoteVol: 2e7}, {Symbol: "ETHUSDT", Last: 1, QuoteVol: 1e7}}}
	s := newService(f)
	s.refreshCoins(context.Background())
	s.Update(f.tickers)
	s.refreshRatios(context.Background(), []string{"BTCUSDT", "ETHUSDT"})
	s.Update(f.tickers)
	n := s.Current().Nodes
	if n[0].RV != 1.5 || n[1].RV != 0 {
		t.Fatalf("倍数不对：%+v", n)
	}
	raw, _ := json.Marshal(n[1])
	if strings.Contains(string(raw), `"rv"`) {
		t.Fatalf("没算出来时不应有 rv 字段：%s", raw)
	}
}

// 行情断了：超过 30 秒写 stale，留着最后一份
func TestStale(t *testing.T) {
	now := time.Unix(1000, 0)
	f := &fakeFetch{coins: []Coin{{"BTCUSDT", "PoW"}}, tickers: []Ticker{{Symbol: "BTCUSDT", Last: 1, QuoteVol: 2e7}}}
	s := newService(f)
	s.Now = func() time.Time { return now }
	s.refreshCoins(context.Background())
	s.step(context.Background())
	for i := 0; i < 100 && s.Current().State != "ok"; i++ {
		time.Sleep(time.Millisecond)
	}
	f.err = errors.New("断了")
	now = now.Add(29 * time.Second)
	s.step(context.Background())
	if s.Current().State != "ok" {
		t.Fatal("29 秒还不算断")
	}
	now = now.Add(2 * time.Second)
	s.step(context.Background())
	got := s.Current()
	if got.State != "stale" || len(got.Nodes) != 1 {
		t.Fatalf("应 stale 并留着最后一份：%+v", got)
	}
}

func TestServeHTTP(t *testing.T) {
	s := newService(&fakeFetch{coins: []Coin{{"BTCUSDT", "PoW"}}})
	s.refreshCoins(context.Background())
	s.Update([]Ticker{{Symbol: "BTCUSDT", Last: 70000, ChgPct: -5.7, QuoteVol: 2e10}})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("GET", "/api/marketmap", nil))
	var got Snapshot
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.State != "ok" || len(got.Nodes) != 1 || got.Nodes[0].Sec != "PoW" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("接口内容不对：%s", w.Body.String())
	}
}

// 币安目录：只要 U 本位永续、正常交易、标的是币的。股票、指数、盘前、交割、下架的都不要
func TestBinanceCoinsFilter(t *testing.T) {
	sv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"symbols":[
			{"symbol":"BTCUSDT","contractType":"PERPETUAL","quoteAsset":"USDT","status":"TRADING","underlyingType":"COIN","underlyingSubType":["PoW"]},
			{"symbol":"NEWUSDT","contractType":"PERPETUAL","quoteAsset":"USDT","status":"TRADING","underlyingType":"COIN","underlyingSubType":[]},
			{"symbol":"NVDAUSDT","contractType":"TRADIFI_PERPETUAL","quoteAsset":"USDT","status":"TRADING","underlyingType":"EQUITY","underlyingSubType":[]},
			{"symbol":"BTCDOMUSDT","contractType":"PERPETUAL","quoteAsset":"USDT","status":"TRADING","underlyingType":"INDEX","underlyingSubType":["Index"]},
			{"symbol":"BTCUSDT_261225","contractType":"CURRENT_QUARTER","quoteAsset":"USDT","status":"TRADING","underlyingType":"COIN","underlyingSubType":["PoW"]},
			{"symbol":"OLDUSDT","contractType":"PERPETUAL","quoteAsset":"USDT","status":"SETTLING","underlyingType":"COIN","underlyingSubType":[]},
			{"symbol":"BTCUSDC","contractType":"PERPETUAL","quoteAsset":"USDC","status":"TRADING","underlyingType":"COIN","underlyingSubType":["PoW"]}
		]}`))
	}))
	defer sv.Close()
	got, err := (&Binance{BaseURL: sv.URL}).Coins(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != (Coin{"BTCUSDT", "PoW"}) || got[1] != (Coin{"NEWUSDT", ""}) {
		t.Fatalf("目录不对：%+v", got)
	}
}

// 手算：前 48 小时每小时 100（日均 2400），后 24 小时每小时 300（共 7200）→ 7200 ÷ 2400 = 3 倍
func TestVolumeRatioByHand(t *testing.T) {
	bars := 72
	sv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.RawQuery, "symbol=BTCUSDT") || !strings.Contains(r.URL.RawQuery, "interval=1h") {
			t.Errorf("参数不对：%s", r.URL.RawQuery)
		}
		var b strings.Builder
		b.WriteString("[")
		for i := 0; i < bars; i++ {
			v := 100
			if i >= bars-24 {
				v = 300
			}
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, `[0,"1","1","1","1","1",0,"%d",0,"0","0","0"]`, v)
		}
		b.WriteString("]")
		_, _ = w.Write([]byte(b.String()))
	}))
	defer sv.Close()
	r, err := (&Binance{BaseURL: sv.URL}).VolumeRatio(context.Background(), "BTCUSDT")
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(r-3) > 1e-9 {
		t.Fatalf("倍数 = %v，应 3", r)
	}
	// 上市不到 3 天：不给倍数
	bars = 40
	if r, err = (&Binance{BaseURL: sv.URL}).VolumeRatio(context.Background(), "BTCUSDT"); err != nil || r != 0 {
		t.Fatalf("不到 72 根时应回 0：%v %v", r, err)
	}
}

// 模拟行情也有板块，测试环境能看到分组
func TestSimHasSectors(t *testing.T) {
	s := &Sim{Symbols: []string{"BTCUSDT"}, N: 40}
	coins, err := s.Coins(context.Background())
	if err != nil || len(coins) != 40 {
		t.Fatalf("%d 个，%v", len(coins), err)
	}
	seen := map[string]bool{}
	for _, c := range coins {
		seen[c.Sector] = true
	}
	if len(seen) < 5 {
		t.Fatalf("板块太少：%v", seen)
	}
	if r, _ := s.VolumeRatio(context.Background(), "BTCUSDT"); r <= 0 {
		t.Fatal("模拟行情应有成交额倍数")
	}
}
