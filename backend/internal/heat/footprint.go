package heat

import (
	"context"
	"math"
	"sort"
)

// 足迹图（12 的 V.11）：每根 K 线按价位拆开主动买卖量。
// 数据是录下来的逐笔成交，所以只有 6 个重点币，只有开始录制之后的。
// 这里只按细档位汇总；合并到屏幕放得下的档位、算失衡和成交最多的价位在前端做（`web/src/lib/flow.ts`），
// 因为显示档位要跟着缩放变。

// FPLevel 一根 K 线里一个价位的成交（币的数量）。
type FPLevel struct {
	P float64 `json:"p"`
	B float64 `json:"b"`
	S float64 `json:"s"`
}

// FPBar 一根 K 线。
type FPBar struct {
	T int64     `json:"t"`
	L []FPLevel `json:"l"`
}

// FootprintData 接口返回的数据。
type FootprintData struct {
	Instrument string  `json:"instrument"`
	Interval   int64   `json:"interval"`
	Step       float64 `json:"step"` // 细档位宽度
	Bars       []FPBar `json:"bars"`
	Since      int64   `json:"since"` // 最早有成交记录的时间
}

// nice125 取整到 1、2、5 × 10^n。
func nice125(raw float64) float64 {
	if raw <= 0 {
		return 0
	}
	p := math.Pow(10, math.Floor(math.Log10(raw)))
	f := raw / p
	switch {
	case f < 1.5:
		return p
	case f < 3.5:
		return 2 * p
	case f < 7.5:
		return 5 * p
	}
	return 10 * p
}

// BuildFootprint 把成交按 K 线和价位汇总。step 是细档位宽度，价格落到不大于它的整数倍上。
func BuildFootprint(trades []Trade, interval int64, step float64) []FPBar {
	if interval <= 0 || step <= 0 {
		return nil
	}
	type key struct{ t, k int64 }
	sum := map[key]*FPLevel{}
	for _, tr := range trades {
		t := tr.T - ((tr.T%interval)+interval)%interval
		k := int64(math.Floor(tr.P/step + 1e-9))
		lv := sum[key{t, k}]
		if lv == nil {
			lv = &FPLevel{P: float64(k) * step}
			sum[key{t, k}] = lv
		}
		if tr.Buy {
			lv.B += tr.Q
		} else {
			lv.S += tr.Q
		}
	}
	byT := map[int64][]FPLevel{}
	for k, v := range sum {
		byT[k.t] = append(byT[k.t], *v)
	}
	out := make([]FPBar, 0, len(byT))
	for t, l := range byT {
		sort.Slice(l, func(i, j int) bool { return l[i].P < l[j].P })
		out = append(out, FPBar{T: t, L: l})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].T < out[j].T })
	return out
}

// autoFineStep 细档位：K 线平均振幅的四十分之一，不小于价格最小单位。
func autoFineStep(trades []Trade, interval int64, tick float64) float64 {
	hi, lo := map[int64]float64{}, map[int64]float64{}
	for _, tr := range trades {
		t := tr.T - ((tr.T%interval)+interval)%interval
		if v, ok := hi[t]; !ok || tr.P > v {
			hi[t] = tr.P
		}
		if v, ok := lo[t]; !ok || tr.P < v {
			lo[t] = tr.P
		}
	}
	sum := 0.0
	for t, h := range hi {
		sum += h - lo[t]
	}
	s := 0.0
	if len(hi) > 0 {
		s = nice125(sum / float64(len(hi)) / 40)
	}
	return math.Max(s, tick)
}

// loadTrades 读 [from, to) 的逐笔成交：只解压成交那一列，比读整块快。
func (r *Recorder) loadTrades(ctx context.Context, inst string, from, to int64) ([]Trade, error) {
	s := r.syms[inst]
	if s == nil {
		return nil, nil
	}
	s.mu.Lock()
	mem := map[int64][]Trade{}
	for _, c := range s.recent {
		mem[c.T] = c.Trades
	}
	if s.minT != 0 {
		mem[s.minT] = append(append([]Trade(nil), s.mTrade...), s.trades1s...)
	}
	s.mu.Unlock()
	start := from - ((from%60000)+60000)%60000
	var out []Trade
	keep := func(ts []Trade) {
		for _, t := range ts {
			if t.T >= from && t.T < to {
				out = append(out, t)
			}
		}
	}
	if p := r.db(); p != nil {
		rows, err := p.Query(ctx, `select t, trades from heat_chunks where instrument=$1 and t>=$2 and t<$3 order by t`, inst, start, to)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var t int64
			var b []byte
			if err := rows.Scan(&t, &b); err != nil {
				return nil, err
			}
			if _, inMem := mem[t]; inMem {
				continue
			}
			ts, err := DecodeTrades(t, b)
			if err == nil {
				keep(ts)
			}
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	var ms []int64
	for t := range mem {
		if t >= start && t < to {
			ms = append(ms, t)
		}
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i] < ms[j] })
	for _, t := range ms {
		keep(mem[t])
	}
	// 断线时补回来的成交：这一秒录到过的不再算
	if fills := r.loadFill(ctx, inst, from, to); len(fills) > 0 {
		have := make(map[int64]bool, len(out))
		for _, t := range out {
			have[t.T/1000] = true
		}
		for _, f := range fills {
			for _, t := range f.Trades {
				if t.T >= from && t.T < to && !have[t.T/1000] {
					out = append(out, t)
				}
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].T < out[j].T })
	return out, nil
}

// Trades 取一段时间的逐笔成交。没录到这段时间时返回空（第一笔比 from 晚太多也算没录到）。
func (r *Recorder) Trades(ctx context.Context, inst string, from, to int64) []Trade {
	ts, err := r.loadTrades(ctx, inst, from, to)
	if err != nil || len(ts) == 0 || ts[0].T > from+60_000 {
		return nil
	}
	return ts
}

// Footprint 取一段时间的足迹数据。品种没录时 ok 是 false。
func (r *Recorder) Footprint(ctx context.Context, inst string, from, to, interval int64) (*FootprintData, bool, error) {
	s := r.syms[inst]
	if s == nil {
		return nil, false, nil
	}
	trades, err := r.loadTrades(ctx, inst, from, to)
	if err != nil {
		return nil, true, err
	}
	tick := 0.0
	if r.Tick != nil {
		tick = r.Tick(inst)
	}
	step := autoFineStep(trades, interval, tick)
	out := &FootprintData{Instrument: inst, Interval: interval, Step: step, Bars: []FPBar{}}
	if step > 0 {
		out.Bars = BuildFootprint(trades, interval, step)
	}
	s.mu.Lock()
	out.Since = s.since
	s.mu.Unlock()
	if p := r.db(); p != nil {
		var first *int64
		if p.QueryRow(ctx, `select min(t) from heat_chunks where instrument=$1`, inst).Scan(&first) == nil && first != nil {
			out.Since = *first
		}
	}
	return out, true, nil
}
