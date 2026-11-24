package highlight

import (
	"math"
	"testing"

	"qs/internal/journal"
)

const min_ = int64(60_000)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestROE(t *testing.T) {
	x := journal.Trade{Entry: 100000, Qty: 0.1, Net: 250}
	if v := ROE(x, 10); !near(v, 0.25) { // 保证金 1,000，赚 250
		t.Fatalf("ROE %v", v)
	}
	if v := ROE(x, 0); !near(v, 0.025) {
		t.Fatalf("杠杆没有时按 1 倍 %v", v)
	}
	if v := ROE(journal.Trade{Net: 5}, 10); v != 0 {
		t.Fatalf("没有开仓价时是 0：%v", v)
	}
}

func TestQualifies(t *testing.T) {
	win := journal.Trade{CloseTime: 1, Entry: 100000, Qty: 0.1, Net: 200} // 10 倍时 20%
	if !Qualifies(win, 10, 0.20) {
		t.Fatal("刚好 20% 算")
	}
	if Qualifies(win, 5, 0.20) {
		t.Fatal("5 倍时只有 10%，不算")
	}
	if !Qualifies(win, 5, 0.10) {
		t.Fatal("门槛改成 10% 就算")
	}
	if !Qualifies(win, 10, 0) {
		t.Fatal("没设门槛用默认 20%")
	}
	if Qualifies(journal.Trade{CloseTime: 1, Entry: 100000, Qty: 0.1, Net: -200}, 10, 0.01) {
		t.Fatal("亏的不算")
	}
	if Qualifies(journal.Trade{Entry: 100000, Qty: 0.1, Net: 500}, 10, 0.2) {
		t.Fatal("没平仓的不算")
	}
	for _, o := range []string{"manual", "ai", "mixed", "bot", "signal"} {
		w := win
		w.Origin = o
		if !Qualifies(w, 10, 0.2) {
			t.Fatalf("%s 的交易也算：赚了就是赚了", o)
		}
	}
}

func TestClampMin(t *testing.T) {
	if ClampMin(0) != DefaultMin || ClampMin(-1) != DefaultMin {
		t.Fatal("没设过用默认值")
	}
	if ClampMin(0.001) != MinFloor || ClampMin(999) != MinCeil {
		t.Fatal("超出范围夹住")
	}
	if ClampMin(0.35) != 0.35 {
		t.Fatal("范围里原样")
	}
}

func TestAcctRet(t *testing.T) {
	all := []journal.Trade{
		{OpenTime: 0, CloseTime: min_, Net: -500},
		{OpenTime: 5 * min_, CloseTime: 9 * min_, Net: 300}, // 这一笔开仓时，前一笔已经平了
		{OpenTime: 6 * min_, CloseTime: 7 * min_, Net: 100}, // 开在这一笔之后、平在它之前：不算进开仓时的权益
	}
	if v := AcctRet(all[1], all, 10000); !near(v, 300.0/9500) {
		t.Fatalf("占账户 %v", v)
	}
}

func TestExcursion(t *testing.T) {
	x := journal.Trade{Entry: 100000}
	mae, mfe := Excursion(x, 10, 500, 2000) // 价格最多逆向 500、顺向 2000
	if !near(mae, -0.05) || !near(mfe, 0.2) {
		t.Fatalf("按保证金：%v %v", mae, mfe)
	}
}

func TestPick(t *testing.T) {
	evs := []Ev{{T: 0, Kind: "liq", USD: 1e6}, {T: 1, Kind: "liq", USD: 5e6}, {T: 50, Kind: "whale", USD: 2e7}, {T: 100, Kind: "econ", Key: "cpi"}}
	out := Pick(evs, 4)
	if len(out) != 3 || out[0].USD != 5e6 || out[2].Kind != "econ" {
		t.Fatalf("挨着的两个只留大的，按时间排：%+v", out)
	}
	if len(Pick(nil, 4)) != 0 {
		t.Fatal("没有事件")
	}
}

func TestPickDemo(t *testing.T) {
	var rec []int64
	var bars []Bar
	px := 100.0
	for i := 0; i < 200; i++ {
		t0 := int64(i) * min_
		o := px
		if i >= 120 && i < 150 {
			px *= 0.995 // 一段下跌
		}
		bars = append(bars, Bar{T: t0, O: o, C: px})
		if i < 60 || i >= 100 { // 60–100 分钟没录到
			rec = append(rec, t0)
		}
	}
	d, ok := PickDemo("BTC-PERP", rec, bars, 30)
	if !ok || d.Side != "short" || d.From != 120*min_ || d.To != 150*min_ {
		t.Fatalf("应该挑中 120–150 分钟的下跌：%+v %v", d, ok)
	}
	if d.Move < 0.13 || d.Move > 0.14 {
		t.Fatalf("跌了约 13.9%%：%v", d.Move)
	}
	if _, ok := PickDemo("BTC-PERP", rec[:10], bars, 30); ok {
		t.Fatal("录到的不够 30 分钟连续")
	}
}

func TestCandles(t *testing.T) {
	if PickIv(5*60_000, []int64{1000, 5000, 60_000}) != 5000 {
		t.Fatal("5 分钟用 5 秒一根（60 根）")
	}
	if PickIv(10*86_400_000, []int64{60_000, 3_600_000}) != 3_600_000 {
		t.Fatal("太长就用最长的周期")
	}
	ts := []Tick{{T: 1000, P: 10}, {T: 1400, P: 12}, {T: 2100, P: 11}, {T: 5200, P: 13}, {T: 9000, P: 99}}
	c := FromTicks(ts, 1000, 6000, 1000)
	if len(c) != 3 || c[0].T != 1000 || c[0].O != 10 || c[0].H != 12 || c[0].C != 12 {
		t.Fatalf("第一根：%+v", c)
	}
	if c[1].O != 12 || c[1].C != 11 || c[1].L != 11 {
		t.Fatalf("第二根接上一根的收盘：%+v", c[1])
	}
	if c[2].T != 5000 || c[2].C != 13 {
		t.Fatalf("没成交的几秒不出 K 线，平仓之后的不算：%+v", c)
	}
}
