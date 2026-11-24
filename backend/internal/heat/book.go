// Package heat 流动性频谱的数据（10 的第 1 项、12 的 V.5–V.10，D81）：
//
//   - 用币安增量流 + 快照维护完整盘口；
//   - 每 100 毫秒切一列推给正在看的人，每秒存一列（保留 30 天）；
//   - 从盘口变化和逐笔成交里推断撤单、吸收、冰山、扫单、大单。币安不提供逐笔委托，这些都只能写"疑似"。
//
// 这里的数字只用于展示和分析，不进账本，所以用 float64。
package heat

import (
	"errors"
	"sort"
)

// Diff 币安的一条增量。U、u 是这条覆盖的序号范围，PU 是上一条的 u。
type Diff struct {
	U, Last, PU int64
	Bids, Asks  [][2]float64 // [价格, 数量]；数量 0 表示这一档没了
}

// Snapshot REST 快照。
type Snapshot struct {
	LastID     int64
	Bids, Asks [][2]float64
}

// ErrGap 序号断了：要重新取快照。
var ErrGap = errors.New("盘口序号断了")

// LocalBook 本地盘口。规则见币安文档《如何正确在本地维护一个订单簿》：
//  1. 先缓存增量，再取快照；
//  2. 丢掉 u < lastUpdateId 的增量；
//  3. 第一条必须满足 U <= lastUpdateId <= u（或者 pu == lastUpdateId，正好紧接）；
//  4. 之后每条的 pu 必须等于上一条的 u，否则重来。
type LocalBook struct {
	Bids, Asks map[float64]float64
	lastU      int64
	ready      bool
	snapID     int64
}

// NewLocalBook 建一个空盘口。
func NewLocalBook() *LocalBook {
	return &LocalBook{Bids: map[float64]float64{}, Asks: map[float64]float64{}}
}

// Reset 用快照重置。
func (b *LocalBook) Reset(s Snapshot) {
	b.Bids = make(map[float64]float64, len(s.Bids))
	b.Asks = make(map[float64]float64, len(s.Asks))
	for _, x := range s.Bids {
		if x[1] > 0 {
			b.Bids[x[0]] = x[1]
		}
	}
	for _, x := range s.Asks {
		if x[1] > 0 {
			b.Asks[x[0]] = x[1]
		}
	}
	b.snapID = s.LastID
	b.lastU = 0
	b.ready = true
}

// Ready 拼上了吗（有快照，并且已经接上增量）。
func (b *LocalBook) Ready() bool { return b.ready && b.lastU > 0 }

// Apply 应用一条增量。序号断了返回 ErrGap，这时要重新取快照。
func (b *LocalBook) Apply(d Diff) error {
	if !b.ready {
		return ErrGap
	}
	if d.Last < b.snapID {
		return nil // 快照之前的，丢掉
	}
	if b.lastU == 0 {
		// 第一条要把快照的序号包在里面；或者正好紧接快照（pu 等于快照序号），也没有漏
		if d.U > b.snapID && d.PU != b.snapID {
			return ErrGap
		}
	} else if d.PU != b.lastU {
		return ErrGap
	}
	setLevels(b.Bids, d.Bids)
	setLevels(b.Asks, d.Asks)
	b.lastU = d.Last
	b.uncross()
	return nil
}

func setLevels(m map[float64]float64, lv [][2]float64) {
	for _, x := range lv {
		if x[1] <= 0 {
			delete(m, x[0])
		} else {
			m[x[0]] = x[1]
		}
	}
}

// uncross 买一比卖一还高：说明本地盘口脏了（漏了撤单），修掉交叉的部分
func (b *LocalBook) uncross() {
	bb, ba := b.Best()
	if bb > 0 && ba > 0 && bb >= ba {
		for p := range b.Bids {
			if p >= ba {
				delete(b.Bids, p)
			}
		}
	}
}

// Merge 用一份新快照补全盘口，不断流：快照范围内的档换成快照的，再重放取快照期间来的增量。
// 范围外的档不动。价格走远后，那边一直没变动过的挂单就是这样补上的。
func (b *LocalBook) Merge(s Snapshot, later []Diff) {
	// 快照是从买一、卖一往外连续的 1000 档，所以这一段里快照没有的档都已经不在了
	replace := func(m map[float64]float64, lv [][2]float64, stale func(p, far float64) bool, farther func(a, b float64) float64) {
		if len(lv) == 0 {
			return
		}
		far := lv[0][0]
		for _, x := range lv {
			far = farther(far, x[0])
		}
		for p := range m {
			if stale(p, far) {
				delete(m, p)
			}
		}
		for _, x := range lv {
			if x[1] > 0 {
				m[x[0]] = x[1]
			}
		}
	}
	replace(b.Bids, s.Bids, func(p, far float64) bool { return p >= far }, func(a, c float64) float64 { return min(a, c) })
	replace(b.Asks, s.Asks, func(p, far float64) bool { return p <= far }, func(a, c float64) float64 { return max(a, c) })
	for _, d := range later {
		if d.Last < s.LastID {
			continue
		}
		setLevels(b.Bids, d.Bids)
		setLevels(b.Asks, d.Asks)
	}
	b.uncross()
}

// Best 买一、卖一价。没有时是 0。
func (b *LocalBook) Best() (bid, ask float64) {
	for p := range b.Bids {
		if p > bid {
			bid = p
		}
	}
	for p := range b.Asks {
		if ask == 0 || p < ask {
			ask = p
		}
	}
	return bid, ask
}

// Prune 去掉离现价太远的档，免得盘口越攒越大。
func (b *LocalBook) Prune(mid, frac float64) {
	lo, hi := mid*(1-frac), mid*(1+frac)
	for p := range b.Bids {
		if p < lo {
			delete(b.Bids, p)
		}
	}
	for p := range b.Asks {
		if p > hi {
			delete(b.Asks, p)
		}
	}
}

// Column 一列：按价位分桶后的挂单金额（美元）。key 是桶号 = round(价格 / 桶宽)。
type Column map[int64]float64

// Bucket 把盘口按桶宽分桶，只留现价上下 frac 以内。
func (b *LocalBook) Bucket(width, mid, frac float64) Column {
	out := make(Column, len(b.Bids)+len(b.Asks))
	lo, hi := mid*(1-frac), mid*(1+frac)
	add := func(m map[float64]float64) {
		for p, q := range m {
			if p < lo || p > hi {
				continue
			}
			out[bucketOf(p, width)] += p * q
		}
	}
	add(b.Bids)
	add(b.Asks)
	return out
}

func bucketOf(p, width float64) int64 { return int64(p/width + 0.5) }

// niceWidth 桶宽：价格的十万分之二，取整到 1、2、5 × 10^n，不小于最小价格单位。
func niceWidth(price, tick float64) float64 {
	raw := price * 0.00002
	if raw < tick {
		raw = tick
	}
	p := 1.0
	for p*10 <= raw {
		p *= 10
	}
	for p > raw {
		p /= 10
	}
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

// SortedKeys 桶号从小到大。
func (c Column) SortedKeys() []int64 {
	keys := make([]int64, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}
