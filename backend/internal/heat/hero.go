package heat

// 开屏地形的数据：最近录到的两小时（D87，用户 2026-11-09）。
// 按钟点取最近两小时的话，电脑睡过一觉就只剩睡醒后的一小段，拉伸铺满以后山又宽又圆，丰度差很多。
// 这里改成从现在往回数"录到盘口的分钟"，凑够两小时：中间没录到的时段跳过，前后接上。
// 每一段按它自己的价位居中（睡一晚价格可能变了 1–2%，不居中的话两段的山落在不同高度，画面被分成两片），
// Shift 记下每一列平移了多少，真实价格 = P0 + 行 × Step + Shift[列]。
// 返回的时间轴是压缩过的：T0 = 0、DT = 1，成交的时间就是列号。开屏不显示时间，鼠标读数只读价格和金额。

import (
	"context"
	"encoding/base64"
	"math"
	"sort"
	"time"
)

type span struct{ from, to int64 } // 一段连续录到的分钟 [from, to)

// recordedSpans 从 to 往回找录到盘口的分钟，凑够 want 毫秒为止（最多往回 lookback）。新的在前。
func (r *Recorder) recordedSpans(ctx context.Context, inst string, to, want, lookback int64) []span {
	mins := map[int64]bool{}
	if s := r.syms[inst]; s != nil {
		s.mu.Lock()
		for _, c := range s.recent {
			if c.Rows > 0 {
				mins[c.T] = true
			}
		}
		if s.minT != 0 && s.width != 0 {
			mins[s.minT] = true
		}
		s.mu.Unlock()
	}
	if p := r.db(); p != nil {
		rows, err := p.Query(ctx, `select t from heat_chunks where instrument=$1 and t>=$2 and t<$3 and rows>0 order by t desc limit $4`, inst, to-lookback, to, want/60_000+5)
		if err == nil {
			for rows.Next() {
				var t int64
				if rows.Scan(&t) == nil {
					mins[t] = true
				}
			}
			rows.Close()
		}
	}
	ts := make([]int64, 0, len(mins))
	for t := range mins {
		if t >= to-lookback && t < to {
			ts = append(ts, t)
		}
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i] > ts[j] })
	if n := int(want / 60_000); len(ts) > n {
		ts = ts[:n]
	}
	var out []span
	for _, t := range ts {
		if k := len(out) - 1; k >= 0 && out[k].from == t+60_000 {
			out[k].from = t
			continue
		}
		end := min(t+60_000, to) // 正在录的这一分钟只到现在
		out = append(out, span{from: t, to: end})
	}
	return out
}

// RecentHistory 最近录到的 want 毫秒，拼成一张图。cols、rows 是整张图大概要多少列、多少行。
func (r *Recorder) RecentHistory(ctx context.Context, inst string, want int64, cols, rows int, frac float64) (*History, error) {
	now := time.Now().UnixMilli()
	spans := r.recordedSpans(ctx, inst, now, want, 7*24*3600_000)
	// 只有一段、而且就是最近这段时间：和按钟点取一样，直接算
	if len(spans) <= 1 {
		from := now - want
		if len(spans) == 1 && spans[0].from > from {
			from = spans[0].from
		}
		return r.BuildHistory(ctx, inst, from, now, cols, rows, 0, 0, frac)
	}
	total := int64(0)
	for _, s := range spans {
		total += s.to - s.from
	}
	// 第一遍：每段自己的价格范围；取最高的那段做统一的高度，每段按自己的中心放，行宽才一样
	type seg struct {
		s    span
		cols int
		h    *History
	}
	segs := make([]seg, 0, len(spans))
	H := 0.0
	for _, s := range spans {
		c := max(2, int(math.Round(float64(cols)*float64(s.to-s.from)/float64(total))))
		h, err := r.BuildHistory(ctx, inst, s.from, s.to, c, rows, 0, 0, frac)
		if err != nil {
			return nil, err
		}
		if h.Rows < 2 || h.Cells == "" {
			continue
		}
		H = math.Max(H, h.Step*float64(h.Rows-1))
		segs = append(segs, seg{s, c, h})
	}
	if len(segs) == 0 {
		return r.BuildHistory(ctx, inst, now-want, now, cols, rows, 0, 0, frac)
	}
	R := math.MaxInt
	for i := range segs {
		h := segs[i].h
		mid := h.P0 + h.Step*float64(h.Rows-1)/2
		h2, err := r.BuildHistory(ctx, inst, segs[i].s.from, segs[i].s.to, segs[i].cols, rows, mid-H/2, mid+H/2, 0)
		if err != nil {
			return nil, err
		}
		segs[i].h = h2
		R = min(R, h2.Rows)
	}
	// 拼起来：从旧到新
	sort.Slice(segs, func(i, j int) bool { return segs[i].s.from < segs[j].s.from })
	newest := segs[len(segs)-1].h
	C := 0
	for _, g := range segs {
		C += g.h.Cols
	}
	out := &History{Instrument: inst, T0: 0, DT: 1, Cols: C, Rows: R, P0: newest.P0, Step: newest.Step,
		Mids: make([]float64, 0, C), Buy: make([]float64, 0, C), Sell: make([]float64, 0, C), Shift: make([]float64, 0, C),
		Trades: [][4]float64{}, Events: []Event{}, Since: newest.Since, QLo: newest.QLo, QHi: newest.QHi}
	cells := make([]byte, R*C)
	x0 := 0
	for _, g := range segs {
		h := g.h
		src, err := base64.StdEncoding.DecodeString(h.Cells)
		if err != nil {
			return nil, err
		}
		for rr := 0; rr < R; rr++ {
			copy(cells[rr*C+x0:rr*C+x0+h.Cols], src[rr*h.Cols:rr*h.Cols+h.Cols])
		}
		shift := h.P0 - newest.P0
		for i := 0; i < h.Cols; i++ {
			out.Mids = append(out.Mids, h.Mids[i])
			out.Buy = append(out.Buy, h.Buy[i])
			out.Sell = append(out.Sell, h.Sell[i])
			out.Shift = append(out.Shift, shift)
		}
		for _, t := range h.Trades {
			col := float64(x0) + (t[0]-float64(h.T0))/float64(h.DT)
			out.Trades = append(out.Trades, [4]float64{col, t[1], t[2], t[3]})
		}
		x0 += h.Cols
	}
	out.Cells = base64.StdEncoding.EncodeToString(cells)
	return out, nil
}

// RecordedMinutes 这段时间里录到盘口的分钟（毫秒时刻，从旧到新）。演示模式挑行情用（D88）。
func (r *Recorder) RecordedMinutes(ctx context.Context, inst string, from, to int64) []int64 {
	sp := r.recordedSpans(ctx, inst, to, to-from, to-from)
	var out []int64
	for _, s := range sp {
		for t := s.from; t < s.to; t += 60_000 {
			out = append(out, t-t%60_000)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
