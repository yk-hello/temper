package onchain

import (
	"math"
	"testing"
)

// 同一批仓位，两个人在不同价格时刻请求：格子边界一样，聚集区的价格和金额一样
func TestLiqMapStableGrid(t *testing.T) {
	s := &Scanner{pos: map[string][]Pos{
		"a": {{Coin: "BTC", Size: 1, Value: 2_000_000, LiqPx: 75_640}},
		"b": {{Coin: "BTC", Size: 2, Value: 3_000_000, LiqPx: 75_690}},
		"c": {{Coin: "BTC", Size: -1, Value: 1_000_000, LiqPx: 89_450}},
	}}
	m1 := s.LiqMap("BTC", 80_560, 0.12, 240)
	m2 := s.LiqMap("BTC", 80_623, 0.12, 240)
	find := func(m LiqMap, px float64) Bin {
		for _, b := range m.Bins {
			if px >= b.Lo && px < b.Hi {
				return b
			}
		}
		t.Fatalf("%v 不在任何格子里", px)
		return Bin{}
	}
	for _, px := range []float64{75_640, 75_690, 89_450} {
		b1, b2 := find(m1, px), find(m2, px)
		if b1.Lo != b2.Lo || b1.LongLiq != b2.LongLiq || b1.ShortLiq != b2.ShortLiq {
			t.Fatalf("价格 %v：两次请求的格子不一样 %+v vs %+v", px, b1, b2)
		}
	}
	w := m1.Bins[0].Hi - m1.Bins[0].Lo
	if w != 100 || math.Mod(m1.Bins[0].Lo, 100) != 0 {
		t.Fatalf("格宽应该是 100、格边是 100 的整数倍，得到 %v / %v", w, m1.Bins[0].Lo)
	}
	b := find(m1, 75_640)
	if math.Abs(b.LongPx-(2*75_640+3*75_690)/5.0) > 1e-6 {
		t.Fatalf("加权强平价 %v", b.LongPx)
	}
}
