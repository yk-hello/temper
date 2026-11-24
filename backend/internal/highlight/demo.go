package highlight

import (
	"math"
	"sort"
)

// Pick 视频里最多放 n 张事件卡片：经济事件先放，再按金额从大到小；离得太近的只留一张（同一时刻弹两张看不清）。
func Pick(evs []Ev, n int) []Ev {
	if len(evs) == 0 {
		return []Ev{}
	}
	sort.SliceStable(evs, func(i, j int) bool {
		a, b := evs[i], evs[j]
		if (a.Kind == "econ") != (b.Kind == "econ") {
			return a.Kind == "econ"
		}
		return a.USD > b.USD
	})
	lo, hi := evs[0].T, evs[0].T
	for _, e := range evs {
		lo, hi = min(lo, e.T), max(hi, e.T)
	}
	gap := (hi - lo) / int64(n*2+1)
	out := []Ev{}
	for _, e := range evs {
		near := false
		for _, o := range out {
			if d := e.T - o.T; d < gap && -d < gap {
				near = true
			}
		}
		if !near {
			out = append(out, e)
		}
		if len(out) == n {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].T < out[j].T })
	return out
}

// Bar 一分钟 K 线：开盘、收盘。
type Bar struct {
	T    int64
	O, C float64
}

// PickDemo 演示模式：在录到盘口的分钟里，找连续 span 分钟价格走得最多的一段（D88 第 4 条）。
// recorded 是录到盘口的分钟（毫秒时刻），bars 是同一段时间的 1 分钟 K 线。
// 这段行情是事后挑的，界面和视频上全程写"演示回放"。
func PickDemo(inst string, recorded []int64, bars []Bar, span int) (*Demo, bool) {
	have := make(map[int64]bool, len(recorded))
	for _, t := range recorded {
		have[t] = true
	}
	sort.Slice(bars, func(i, j int) bool { return bars[i].T < bars[j].T })
	var best *Demo
	for i := 0; i+span <= len(bars); i++ {
		a, b := bars[i], bars[i+span-1]
		if b.T-a.T != int64(span-1)*60_000 || a.O <= 0 {
			continue // 中间缺了 K 线
		}
		ok := true
		for k := i; k < i+span; k++ {
			if !have[bars[k].T] {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		mv := (b.C - a.O) / a.O
		if best == nil || math.Abs(mv) > math.Abs(best.Move) {
			side := "long"
			if mv < 0 {
				side = "short"
			}
			best = &Demo{Instrument: inst, Side: side, From: a.T, To: b.T + 60_000, Entry: a.O, Exit: b.C, Move: mv}
		}
	}
	if best == nil {
		return nil, false
	}
	best.Move = math.Abs(best.Move)
	return best, true
}

// Candle 卡片和视频上的一根 K 线。
type Candle struct {
	T int64   `json:"t"`
	O float64 `json:"o"`
	H float64 `json:"h"`
	L float64 `json:"l"`
	C float64 `json:"c"`
}

// Tick 一笔成交：时间、价格。
type Tick struct {
	T int64
	P float64
}

// PickIv 从短到长挑一个周期，让 hold 这段时间里不超过 60 根；都太短就用最长的。
func PickIv(hold int64, ivs []int64) int64 {
	for _, iv := range ivs {
		if hold/iv <= 60 {
			return iv
		}
	}
	return ivs[len(ivs)-1]
}

// FromTicks 用逐笔成交合成 K 线，从开仓那一刻对齐（短单用：交易所最细只有 1 秒线，几秒钟的单只有几根）。
func FromTicks(ts []Tick, from, to, iv int64) []Candle {
	var out []Candle
	for _, x := range ts {
		if x.T < from || x.T > to || x.P <= 0 {
			continue
		}
		t := from + (x.T-from)/iv*iv
		if n := len(out); n > 0 && out[n-1].T == t {
			c := &out[n-1]
			c.H, c.L, c.C = math.Max(c.H, x.P), math.Min(c.L, x.P), x.P
			continue
		}
		o := x.P
		if n := len(out); n > 0 {
			o = out[n-1].C // 接上一根的收盘，中间没成交的空档不留缺口
		}
		out = append(out, Candle{T: t, O: o, H: math.Max(o, x.P), L: math.Min(o, x.P), C: x.P})
	}
	return out
}
