package trading

import (
	"context"
	"testing"
	"time"
)

// 没设止损：断流期间到过强平价，补强平，按强平价成交，穿仓照样记账。
func TestGapReplayLiquidates(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "100000")
	_ = x.e.SetLeverage(1, "BTC-PERP", 20)
	x.place(buy("BTC-PERP", "1"))
	v0, _ := x.e.View(1)
	liq := v0.Positions[0].LiqPx
	from := x.now.UnixMilli() + 1000
	n := x.e.ReplayBars("BTC-PERP", from, from+60_000, []Bar1m{{T: from - from%60_000, O: D("100000"), H: D("100000"), L: D("90000")}}, nil)
	v, _ := x.e.View(1)
	if n != 1 || len(v.Positions) != 0 {
		t.Fatalf("应该补强平：补了 %d，仓位 %+v", n, v.Positions)
	}
	var gap, liqN *Notice
	for i := range v.Notices {
		switch v.Notices[i].Kind {
		case "gap_replay":
			gap = &v.Notices[i]
		case "liquidation":
			liqN = &v.Notices[i]
		}
	}
	if gap == nil || gap.Reason != "liquidation" || !gap.Px.Equal(liq) || liqN == nil {
		t.Fatalf("按强平价 %s 补强平，并留补算提醒：%+v %+v", liq, gap, liqN)
	}
	fills, _, _, _ := x.e.Records(1)
	if f := fills[len(fills)-1]; !f.Px.Equal(liq) {
		t.Fatalf("强平成交价应为 %s：%s", liq, f.Px)
	}
}

// 止盈和止损在同一分钟都碰到：按先止损算（偏保守）。没碰到的不动。
func TestGapReplayPessimistic(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "100000")
	r := buy("BTC-PERP", "0.1")
	r.SL, r.TP = D("99000"), D("101000")
	x.place(r)
	from := x.now.UnixMilli() + 1000
	T := from - from%60_000
	// 第一分钟哪个都没碰到
	if n := x.e.ReplayBars("BTC-PERP", from, from+60_000, []Bar1m{{T: T, O: D("100000"), H: D("100500"), L: D("99500")}}, nil); n != 0 {
		t.Fatalf("没碰到不该补：%d", n)
	}
	x.e.ReplayBars("BTC-PERP", from, from+60_000, []Bar1m{{T: T, O: D("100000"), H: D("101500"), L: D("98500")}}, nil)
	v, _ := x.e.View(1)
	var reason string
	for _, n := range v.Notices {
		if n.Kind == "gap_replay" {
			reason = n.Reason
		}
	}
	if len(v.Positions) != 0 || reason != "sl" {
		t.Fatalf("同一分钟都碰到按止损算：%s，仓位 %+v", reason, v.Positions)
	}
}

// 断流以后才开的仓，不拿断流期间的价格去补。
func TestGapReplaySkipsNewPositions(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "100000")
	r := buy("BTC-PERP", "0.1")
	r.SL = D("99000")
	x.place(r)
	from := x.now.UnixMilli() - 60_000 // 断流比开仓早
	if n := x.e.ReplayBars("BTC-PERP", from, from+120_000, []Bar1m{{T: from - from%60_000, O: D("100000"), H: D("100000"), L: D("95000")}}, nil); n != 0 {
		t.Fatalf("断流后开的仓不该补：%d", n)
	}
}

// 盘口停了又恢复：自动记下这一段，取 K 线补算。
func TestGapReplayDetectsOutage(t *testing.T) {
	x := newEnv(t)
	x.market("BTC-PERP", "100000")
	r := buy("BTC-PERP", "0.1")
	r.SL = D("99000")
	x.place(r)
	var asked [][2]int64
	x.e.Bars1m = func(_ context.Context, inst string, from, to int64, mark bool) ([]Bar1m, error) {
		asked = append(asked, [2]int64{from, to})
		return []Bar1m{{T: from - from%60_000, O: D("100000"), H: D("100000"), L: D("98000")}}, nil
	}
	b, _ := x.books.Book(V, "BTC-PERP")
	stale := time.Now().Add(-30 * time.Second).UnixMilli()
	x.books.SetBookAt(b, stale)
	x.e.Tick() // 断着
	x.market("BTC-PERP", "100000")
	x.e.Tick() // 恢复
	if n := x.e.replayGaps(context.Background()); n != 1 || len(asked) != 2 || asked[0][0] != stale {
		t.Fatalf("恢复后应该从断流开始那一刻取 K 线补一笔：补了 %d，取了 %v", n, asked)
	}
}
