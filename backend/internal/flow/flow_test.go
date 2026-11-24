package flow

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// V.13 手算样例：四种变化各一个。周期 5 分钟，币安统计时刻是周期结束的时刻
func TestDecomposeByHand(t *testing.T) {
	const m = int64(300_000)
	raw := []OIRaw{
		{T: 5 * m, Coin: 1000, USD: 100_000_000},
		{T: 6 * m, Coin: 1010, USD: 101_000_000}, // 价格涨、持仓增：新开多
		{T: 7 * m, Coin: 1030, USD: 102_000_000}, // 价格跌、持仓增：新开空
		{T: 8 * m, Coin: 1020, USD: 103_000_000}, // 价格涨、持仓减：空头回补
		{T: 9 * m, Coin: 1000, USD: 104_000_000}, // 价格跌、持仓减：多头止损
	}
	closes := []Close{{4 * m, 100}, {5 * m, 110}, {6 * m, 105}, {7 * m, 120}, {8 * m, 90}}
	got := Decompose(raw, closes, m)
	if len(got) != 4 {
		t.Fatalf("应有 4 个点：%+v", got)
	}
	want := []struct {
		kind  string
		delta float64
		dp    float64
	}{
		{NewLong, 10 * 110, 10},
		{NewShort, 20 * 105, -4.5455},
		{ShortCover, -10 * 120, 14.2857},
		{LongClose, -20 * 90, -25},
	}
	for i, w := range want {
		if got[i].Kind != w.kind || got[i].Delta != w.delta || got[i].DP != w.dp {
			t.Fatalf("第 %d 个：%+v，应是 %+v", i, got[i], w)
		}
		if got[i].T != int64(5+i)*m {
			t.Fatalf("时间应对齐 K 线开始时间：%d", got[i].T)
		}
	}
}

// 缺 K 线或者统计断了一段：跳过，不瞎算
func TestDecomposeGaps(t *testing.T) {
	const m = int64(300_000)
	raw := []OIRaw{{T: 2 * m, Coin: 1}, {T: 3 * m, Coin: 2}, {T: 9 * m, Coin: 3}, {T: 10 * m, Coin: 4}}
	closes := []Close{{1 * m, 100}, {2 * m, 101}, {8 * m, 100}}
	got := Decompose(raw, closes, m)
	if len(got) != 1 || got[0].T != 2*m {
		t.Fatalf("应只有第一段：%+v", got)
	}
}

func TestPeriodOf(t *testing.T) {
	for _, c := range []struct {
		iv   int64
		want string
	}{{1000, "5m"}, {60_000, "5m"}, {300_000, "5m"}, {900_000, "15m"}, {3600_000, "1h"}, {14400_000, "4h"}, {86400_000, "1d"}} {
		if p, _ := periodOf(c.iv); p != c.want {
			t.Fatalf("%d → %s", c.iv, p)
		}
	}
}

// V.14 爆仓流：新的在前，按品种过滤，5 分钟和 1 小时合计
func TestLiquidations(t *testing.T) {
	s := New(&Sim{})
	now := int64(10_000_000)
	s.AddLiquidation(Liq{T: now - 4000_000, Instrument: "BTC-PERP", Side: "long", USD: 999}) // 超过 1 小时
	s.AddLiquidation(Liq{T: now - 1000_000, Instrument: "BTC-PERP", Side: "long", USD: 100})
	s.AddLiquidation(Liq{T: now - 100_000, Instrument: "ETH-PERP", Side: "short", USD: 50})
	s.AddLiquidation(Liq{T: now - 10_000, Instrument: "BTC-PERP", Side: "short", USD: 30})
	items, m5, h1, _ := s.Liquidations("BTC-PERP", now, 2)
	if len(items) != 2 || items[0].USD != 30 || items[1].USD != 100 {
		t.Fatalf("新的在前、最多 2 条：%+v", items)
	}
	if m5.Short != 30 || m5.Long != 0 || h1.Long != 100 || h1.Short != 30 {
		t.Fatalf("合计：%+v %+v", m5, h1)
	}
	_, m5, h1, _ = s.Liquidations("", now, 10)
	if m5.Short != 80 || h1.Long != 100 {
		t.Fatalf("全部品种：%+v %+v", m5, h1)
	}
}

func TestLiquidationCap(t *testing.T) {
	s := New(&Sim{})
	for i := 0; i < liqKeep+50; i++ {
		s.AddLiquidation(Liq{T: int64(i), USD: 1})
	}
	if len(s.liqs) != liqKeep {
		t.Fatalf("应只留 %d 条：%d", liqKeep, len(s.liqs))
	}
}

// 解析币安的返回：用假服务器
func TestBinanceParse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "openInterestHist"):
			_, _ = w.Write([]byte(`[{"symbol":"BTCUSDT","sumOpenInterest":"100.5","sumOpenInterestValue":"7000000","timestamp":600000}]`))
		case strings.Contains(r.URL.Path, "klines"):
			_, _ = w.Write([]byte(`[[300000,"1","2","0.5","1.5","10",599999,"15",3,"5","7","0"]]`))
		case strings.Contains(r.URL.Path, "LongShort"):
			_, _ = w.Write([]byte(`[{"symbol":"BTCUSDT","longShortRatio":"1.5","longAccount":"0.6","shortAccount":"0.4","timestamp":600000}]`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	b := &Binance{Base: srv.URL}
	oi, err := b.OI(context.Background(), "BTCUSDT", "5m", 1)
	if err != nil || oi[0].Coin != 100.5 || oi[0].USD != 7000000 || oi[0].T != 600000 {
		t.Fatalf("持仓量：%+v %v", oi, err)
	}
	cl, err := b.Closes(context.Background(), "BTCUSDT", "5m", 1)
	if err != nil || cl[0].T != 300000 || cl[0].C != 1.5 {
		t.Fatalf("K 线：%+v %v", cl, err)
	}
	r, err := b.Ratios(context.Background(), "BTCUSDT")
	if err != nil || r.Account.Ratio != 1.5 || r.Top.Long != 0.6 {
		t.Fatalf("多空比：%+v %v", r, err)
	}
}

// 币安连不上：有旧数据就先用旧的，没有才报错
func TestMemoKeepsOld(t *testing.T) {
	s := New(&Sim{})
	if _, err := s.memo("a", 0, func() (any, error) { return 1, nil }); err != nil {
		t.Fatal(err)
	}
	v, err := s.memo("a", 0, func() (any, error) { return nil, context.DeadlineExceeded })
	if err != nil || v != 1 {
		t.Fatalf("应返回旧数据：%v %v", v, err)
	}
	if _, err := s.memo("b", 0, func() (any, error) { return nil, context.DeadlineExceeded }); err == nil {
		t.Fatal("没有旧数据应报错")
	}
}

func TestRoutes(t *testing.T) {
	s := New(&Sim{Price: func(string) float64 { return 100 }})
	mux := http.NewServeMux()
	s.Routes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	r, err := http.Get(srv.URL + "/api/flow/oi?instrument=BTC-PERP&interval=300000")
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		Period int64     `json:"period_ms"`
		Points []OIPoint `json:"points"`
	}
	_ = json.NewDecoder(r.Body).Decode(&d)
	r.Body.Close()
	if d.Period != 300000 || len(d.Points) < 100 {
		t.Fatalf("接口：%d %d", d.Period, len(d.Points))
	}
	seen := map[string]bool{}
	for _, p := range d.Points {
		seen[p.Kind] = true
	}
	if len(seen) != 4 {
		t.Fatalf("模拟数据应四种都有：%v", seen)
	}
}
