package venues

import (
	"math"
	"testing"

	"github.com/shopspring/decimal"

	"qs/internal/book"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestPer8h(t *testing.T) {
	if !near(per8h(0.0001, 8), 0.01) {
		t.Fatal(per8h(0.0001, 8))
	}
	// Hyperliquid 每小时一期：0.00125% 一小时和币安 0.01% 八小时是一样的
	if !near(per8h(0.0000125, 1), 0.01) {
		t.Fatal(per8h(0.0000125, 1))
	}
}

func TestMatrix(t *testing.T) {
	books := book.NewStore()
	set := func(venue string, mark, rate float64) {
		books.UpdateTicker(venue, "BTC-PERP", func(x *book.Ticker) {
			x.Mark = decimal.NewFromFloat(mark)
			x.FundingRate = decimal.NewFromFloat(rate)
		})
	}
	set("binance-usdm", 100000, 0.0001)
	set("hyperliquid", 100020, 0.00005) // 一小时一期：折算每 8 小时 0.04%
	s := &Service{Books: books, Primary: "binance-usdm", Coins: []string{"BTC", "ETH"}, HL: true, Now: func() int64 { return 1 }}
	s.remote = map[string]map[string]Cell{"okx": {"BTC": {Mark: 99990, Rate8h: -0.005, Rate: -0.00005, Hours: 8}}}
	rows := s.Matrix()
	if len(rows) != 2 {
		t.Fatalf("%+v", rows)
	}
	r := rows[0]
	if len(r.Cells) != 3 || r.HiV != "hyperliquid" || r.LoV != "okx" {
		t.Fatalf("%+v", r)
	}
	// 最高 100020，最低 99990：差 30 美元，约 3 个基点
	if math.Abs(r.SpreadBps-3.0003) > 0.001 {
		t.Fatalf("价差 %v", r.SpreadBps)
	}
	if math.Abs(r.Gap8h-0.045) > 1e-6 {
		t.Fatalf("费率差 %v", r.Gap8h)
	}
	if len(rows[1].Cells) != 0 || rows[1].SpreadBps != 0 || rows[1].Gap8h != 0 {
		t.Fatalf("没有数据的币不该有数字：%+v", rows[1])
	}
}

func TestSpreadKeepsMissing(t *testing.T) {
	s := &Service{}
	s.hist = map[string][]point{"BTC": {{T: 1, V: map[string]float64{"okx": 1.5}}, {T: 2, V: map[string]float64{"bybit": -2}}}}
	got := s.Spread("BTC")
	series := got["series"].(map[string][]*float64)
	if series["okx"][0] == nil || *series["okx"][0] != 1.5 || series["okx"][1] != nil {
		t.Fatalf("okx %+v", series["okx"])
	}
	if series["bybit"][0] != nil || *series["bybit"][1] != -2 {
		t.Fatalf("bybit %+v", series["bybit"])
	}
}
