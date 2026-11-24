package heat

import (
	"context"
	"testing"
	"time"
)

// 开屏：最近录到的两段中间断了 10 小时、价格差 2%，拼成一张连续的图；每段按自己的价位居中，真实价格换算得回来。
func TestRecentHistoryJoinsSpans(t *testing.T) {
	r := New(&fakeFeed{}, []string{"BTC-PERP"}, func(string) float64 { return 1 }, nil)
	s := r.syms["BTC-PERP"]
	now := time.Now().UnixMilli()
	m := now - now%60_000
	chunk := func(t int64, px float64) *Chunk {
		cols := make([]Column, 60)
		mids := make([]float64, 60)
		for i := range cols {
			c := Column{}
			for k := -40; k <= 40; k++ {
				c[int64(px)+int64(k)] = 50_000 + float64((k*k+i)%7)*30_000
			}
			cols[i] = c
			mids[i] = px + float64(i%5)
		}
		return BuildChunk(t, 1, cols, mids, []Trade{{T: t + 30_000, P: px, Q: 1, Buy: true}})
	}
	for i := int64(0); i < 3; i++ {
		s.recent = append(s.recent, chunk(m-600*60_000+i*60_000, 73_500)) // 10 小时前，价格低 2%
	}
	for i := int64(2); i >= 1; i-- {
		s.recent = append(s.recent, chunk(m-i*60_000, 75_000))
	}
	s.minT, s.width = 0, 0 // 这一分钟还没开始录
	h, err := r.RecentHistory(context.Background(), "BTC-PERP", 5*60_000, 100, 120, 0.002)
	if err != nil {
		t.Fatal(err)
	}
	if h.DT != 1 || len(h.Shift) != h.Cols || h.Cols < 50 {
		t.Fatalf("应拼成压缩时间轴、每列有平移：dt %d cols %d shift %d", h.DT, h.Cols, len(h.Shift))
	}
	old, cur := h.Shift[0], h.Shift[h.Cols-1]
	if cur != 0 || old > -1400 || old < -1600 {
		t.Fatalf("旧的一段要往上平移约 1500 才和新的一段对齐：%v %v", old, cur)
	}
	for i, mid := range h.Mids {
		if mid <= 0 {
			continue
		}
		row := (mid - h.Shift[i] - h.P0) / h.Step
		if row < 0 || row > float64(h.Rows-1) {
			t.Fatalf("第 %d 列的价格 %v 换算后不在图里：行 %v（共 %d 行）", i, mid, row, h.Rows)
		}
	}
	if len(h.Trades) != 5 || h.Trades[0][0] < 0 || h.Trades[4][0] > float64(h.Cols) {
		t.Fatalf("成交的时间换成列号：%v", h.Trades)
	}
}
