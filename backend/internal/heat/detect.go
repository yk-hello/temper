package heat

import (
	"math"
	"sort"
)

// 自动识别（12 的 V.8）。输入每秒的分桶盘口和这一秒的成交，输出疑似事件。
// 币安不提供逐笔委托，看不到是谁挂的、谁撤的，所以全部是推断，界面上都写"疑似"，并给出判断依据。

// 事件种类。
const (
	KindSpoof   = "spoof"   // 疑似撤单：大单还没被成交就消失
	KindAbsorb  = "absorb"  // 疑似吸收：主动成交很多，价格没穿过去
	KindIceberg = "iceberg" // 疑似冰山：成交量超过显示的挂单，挂单却还在
	KindSweep   = "sweep"   // 疑似止损扫单：几秒内一边倒地扫过很多档
	KindLarge   = "large"   // 大单：单笔成交远大于平时
)

// Trade 一笔成交。
type Trade struct {
	T   int64   `json:"t"`
	P   float64 `json:"p"`
	Q   float64 `json:"q"` // 币的数量
	Buy bool    `json:"b"` // 主动买入
}

// Event 一个识别结果。Evidence 是判断依据，界面按语言翻译。
type Event struct {
	T        int64              `json:"t"`
	Kind     string             `json:"kind"`
	P        float64            `json:"p"`
	Bid      bool               `json:"bid"` // 买盘一侧（价格下方）
	USD      float64            `json:"usd"`
	Evidence map[string]float64 `json:"ev"`
}

// 判断用的参数。第一版，用真实数据调过后写在 `07`。
const (
	bigMult      = 8.0     // 大单：是附近挂单中位数的多少倍
	bigFloorUSD  = 50_000  // 大单至少这么多美元
	nearFrac     = 0.002   // 只看现价上下 0.2% 以内的挂单算中位数
	spoofNear    = 0.0015  // 离现价这么近的才算：远处的挂单撤了对价格没影响
	spoofMinAge  = 5       // 至少挂了几秒：做市商换价每秒都在撤挂，不算
	spoofMoved   = 0.5     // 旁边几档多出来的超过减少量的一半，算挪价，不算撤单
	spoofSpread  = 5       // 看旁边几档
	spoofGap     = 10      // 同一个币至少隔几秒才报一次撤单
	spoofShare   = 0.15    // 撤掉的至少占这一侧近处挂单的多少：薄的币上小单撤掉不算事
	spoofDrop    = 0.7     // 一次少了多少
	spoofFilled  = 0.3     // 这期间的成交不超过减少量的多少
	windowSec    = 10      // 吸收和冰山看几秒内的成交
	absorbMult   = 3.0     // 吸收：窗口内成交是平时这段时间的几倍
	absorbHold   = 0.5     // 吸收：挂单至少还剩开始时的多少
	icebergMult  = 1.5     // 冰山：成交是窗口内最大挂单的几倍
	sweepSec     = 3       // 扫单看几秒
	sweepOneSide = 0.85    // 一边倒的比例
	sweepVolMult = 5.0     // 成交量是平时的几倍
	sweepMove    = 0.0015  // 价格至少走多少
	largeMult    = 100.0   // 大单：单笔是平时中位数的几倍
	largeFloor   = 250_000 // 大单至少这么多美元
	spoofRare    = 0.9     // 够条件的撤单里，只报这个币上金额排在前 10% 的
	largeRare    = 0.95    // 够条件的大单里，只报这个币上金额排在前 5% 的
	cooldownSec  = 30      // 同一价位同一种事件多久才再报一次
)

type levelState struct {
	usd     float64
	age     int
	peakUSD float64
	touch   int  // 这一单挂着期间，价格碰过几次
	near    bool // 价格现在贴着它
}

// Wall 现在还挂着的大单：顶部读数和价格尺用。
type Wall struct {
	P     float64 `json:"p"`
	USD   float64 `json:"usd"`
	Bid   bool    `json:"bid"`
	Age   int     `json:"age"`   // 挂了多少秒
	Touch int     `json:"touch"` // 价格碰过几次
	Dist  float64 `json:"dist"`  // 离现价的百分比（带符号）
}

// 什么算大单：第一版，真实盘口上调过后写在 `07`。
const (
	wallRange  = 0.01    // 只看现价上下 1%
	wallFloor  = 100_000 // 至少这么多美元
	wallMult   = 5.0     // 至少是附近挂单中位数的多少倍
	wallMinAge = 30      // 至少挂了多少秒：闪一下就撤的不算
	wallMerge  = 2       // 相隔不超过几档的大单算同一个
	wallMax    = 5       // 每边最多几个
	touchFrac  = 0.0003  // 价格离它这么近算碰到
	flowSec    = 300     // 主动买卖比例看最近几秒
)

// Detector 每个币一个。
type Detector struct {
	width                float64
	levels               map[int64]*levelState
	traded               []map[int64]float64 // 最近 windowSec 秒每秒每桶的成交额
	sides                [][2]float64        // 最近每秒的主动买、卖额
	mids                 []float64
	window               []map[int64]float64 // 最近 windowSec 秒每秒的挂单快照（只记大单）
	cool                 map[string]int64
	sizes                []float64 // 最近的单笔成交额（算平时的中位数）
	totalVs              []float64 // 最近 60 秒每秒成交额
	spoofAt              int64     // 上一次报撤单的时间
	spoofSeen, largeSeen []float64 // 最近够条件的金额，见 rare
	flow                 [][2]float64
}

// NewDetector 新建。width 是桶宽。
func NewDetector(width float64) *Detector {
	return &Detector{width: width, levels: map[int64]*levelState{}, cool: map[string]int64{}}
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	return s[len(s)/2]
}

func (d *Detector) ok(kind string, bk, t int64) bool {
	k := kind + ":" + itoa(bk)
	if last, seen := d.cool[k]; seen && t-last < cooldownSec*1000 {
		return false
	}
	d.cool[k] = t
	return true
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	var b [24]byte
	i := len(b)
	neg := v < 0
	if neg {
		v = -v
	}
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// Step 喂一秒的数据。col 是这一秒的分桶盘口（美元），trades 是这一秒的成交。
func (d *Detector) Step(t int64, mid float64, col Column, trades []Trade) []Event {
	var out []Event
	if mid <= 0 {
		return nil
	}
	bucket := func(p float64) int64 { return bucketOf(p, d.width) }
	// 这一秒的成交
	tr := map[int64]float64{}
	buyUSD, sellUSD := 0.0, 0.0
	for _, x := range trades {
		usd := x.P * x.Q
		tr[bucket(x.P)] += usd
		if x.Buy {
			buyUSD += usd
		} else {
			sellUSD += usd
		}
		// 大单：先和之前的中位数比，再放进去
		if m := median(d.sizes); usd >= largeFloor && (m == 0 || usd >= largeMult*m) && len(d.sizes) >= 20 {
			if rare(&d.largeSeen, usd, largeRare) && d.ok(KindLarge, bucket(x.P), t) {
				out = append(out, Event{T: t, Kind: KindLarge, P: x.P, Bid: !x.Buy, USD: usd, Evidence: map[string]float64{"mult": round2(usd / math.Max(m, 1))}})
			}
		}
		d.sizes = append(d.sizes, usd)
	}
	if len(d.sizes) > 2000 {
		d.sizes = d.sizes[len(d.sizes)-2000:]
	}
	d.traded = appendCap(d.traded, tr, windowSec)
	d.sides = appendCap(d.sides, [2]float64{buyUSD, sellUSD}, 60)
	d.flow = appendCap(d.flow, [2]float64{buyUSD, sellUSD}, flowSec)
	d.mids = appendCap(d.mids, mid, 60)
	d.totalVs = appendCap(d.totalVs, buyUSD+sellUSD, 60)

	// 附近的挂单中位数，定什么算大单
	lo, hi := mid*(1-nearFrac), mid*(1+nearFrac)
	var near []float64
	for k, v := range col {
		p := float64(k) * d.width
		if p >= lo && p <= hi && v > 0 {
			near = append(near, v)
		}
	}
	big := math.Max(bigFloorUSD, bigMult*median(near))

	tradedRecent := func(k int64, secs int) float64 {
		s := 0.0
		n := len(d.traded)
		for i := max(0, n-secs); i < n; i++ {
			s += d.traded[i][k]
		}
		return s
	}

	// 疑似撤单：之前的大单一下子少了很多，而成交解释不了，也不是挪到了旁边，价格也没穿过它
	prevMid := mid
	if n := len(d.mids); n >= 2 {
		prevMid = d.mids[n-2]
	}
	var spoof *Event
	sideBid, sideAsk := 0.0, 0.0
	for k, st := range d.levels {
		p := float64(k) * d.width
		if math.Abs(p-mid) <= mid*spoofNear {
			if p < mid {
				sideBid += st.usd
			} else {
				sideAsk += st.usd
			}
		}
	}
	for k, st := range d.levels {
		now := col[k]
		p := float64(k) * d.width
		if st.usd < big || st.age < spoofMinAge || math.Abs(p-mid) > mid*spoofNear || now >= st.usd*(1-spoofDrop) {
			continue
		}
		if (p < prevMid) != (p < mid) {
			continue // 价格穿过去了，是被吃掉的
		}
		drop := st.usd - now
		filled := tradedRecent(k, 2)
		if filled > drop*spoofFilled {
			continue
		}
		if side := sideAsk; p < mid {
			if drop < sideBid*spoofShare {
				continue
			}
		} else if drop < side*spoofShare {
			continue
		}
		gain := 0.0
		for j := int64(1); j <= spoofSpread; j++ {
			for _, nb := range [2]int64{k - j, k + j} {
				was := 0.0
				if o := d.levels[nb]; o != nil {
					was = o.usd
				}
				if g := col[nb] - was; g > 0 {
					gain += g
				}
			}
		}
		if gain > drop*spoofMoved {
			continue
		}
		if spoof == nil || drop > spoof.USD {
			spoof = &Event{T: t, Kind: KindSpoof, P: p, Bid: p < mid, USD: drop, Evidence: map[string]float64{
				"age": float64(st.age), "dist": round2(math.Abs(p-mid) / mid * 100), "filled": round2(filled / drop * 100)}}
		}
	}
	if spoof != nil && rare(&d.spoofSeen, spoof.USD, spoofRare) && (d.spoofAt == 0 || t-d.spoofAt >= spoofGap*1000) && d.ok(KindSpoof, bucketOf(spoof.P, d.width), t) {
		d.spoofAt = t
		out = append(out, *spoof)
	}

	// 窗口里的大单快照
	big0 := map[int64]float64{}
	for k, v := range col {
		if v >= big/2 {
			big0[k] = v
		}
	}
	d.window = appendCap(d.window, big0, windowSec)

	// 吸收和冰山：窗口内这一档成交很多
	if len(d.traded) >= windowSec {
		avg := 0.0
		for _, v := range d.totalVs {
			avg += v
		}
		avg = avg / float64(len(d.totalVs)) * windowSec
		sum := map[int64]float64{}
		for _, m := range d.traded {
			for k, v := range m {
				sum[k] += v
			}
		}
		for k, v := range sum {
			if v < big {
				continue
			}
			p := float64(k) * d.width
			first, peak := 0.0, 0.0
			for i, m := range d.window {
				if i == 0 {
					first = m[k]
				}
				peak = math.Max(peak, m[k])
			}
			now := col[k]
			crossed := false
			side := p < d.mids[max(0, len(d.mids)-windowSec)]
			for _, m := range d.mids[max(0, len(d.mids)-windowSec):] {
				if (m < p) == side && m != p {
					crossed = true
				}
			}
			if crossed || peak <= 0 {
				continue
			}
			switch {
			case v >= icebergMult*peak && now >= peak*absorbHold:
				if d.ok(KindIceberg, k, t) {
					out = append(out, Event{T: t, Kind: KindIceberg, P: p, Bid: p < mid, USD: v, Evidence: map[string]float64{"shown": round2(peak), "mult": round2(v / peak), "secs": windowSec}})
				}
			case avg > 0 && v >= absorbMult*avg/5 && first > 0 && now >= first*absorbHold:
				if d.ok(KindAbsorb, k, t) {
					out = append(out, Event{T: t, Kind: KindAbsorb, P: p, Bid: p < mid, USD: v, Evidence: map[string]float64{"left": round2(now), "secs": windowSec}})
				}
			}
		}
	}

	// 疑似止损扫单：几秒内一边倒、量大、价格走得远
	if n := len(d.sides); n >= sweepSec+10 {
		var b3, s3 float64
		for _, x := range d.sides[n-sweepSec:] {
			b3 += x[0]
			s3 += x[1]
		}
		total := b3 + s3
		base := 0.0
		for _, x := range d.sides[:n-sweepSec] {
			base += x[0] + x[1]
		}
		base = base / float64(n-sweepSec) * sweepSec
		m0 := d.mids[len(d.mids)-sweepSec-1]
		move := (mid - m0) / m0
		oneSide := math.Max(b3, s3) / math.Max(total, 1)
		if total > 0 && base > 0 && total >= sweepVolMult*base && oneSide >= sweepOneSide && math.Abs(move) >= sweepMove && (move > 0) == (b3 > s3) {
			if d.ok(KindSweep, 0, t) {
				out = append(out, Event{T: t, Kind: KindSweep, P: mid, Bid: move < 0, USD: total, Evidence: map[string]float64{
					"secs": sweepSec, "move": round2(math.Abs(move) * 100), "oneSide": round2(oneSide * 100), "mult": round2(total / base)}})
			}
		}
	}

	// 记下这一秒的挂单
	for k, v := range col {
		st := d.levels[k]
		if st == nil {
			st = &levelState{}
			d.levels[k] = st
		}
		// 少了一半以上，或者一下子多了一倍以上：算新的一单，挂了多久重新算
		if st.usd > 0 && v >= st.usd*0.5 && v <= st.usd*2 {
			st.age++
		} else {
			st.age = 0
			st.touch = 0
		}
		near := math.Abs(float64(k)*d.width-mid) <= mid*touchFrac
		if near && !st.near {
			st.touch++
		}
		st.near = near
		st.usd = v
		st.peakUSD = math.Max(st.peakUSD, v)
	}
	for k := range d.levels {
		if _, ok := col[k]; !ok {
			delete(d.levels, k)
		}
	}
	return out
}

// Walls 现在还挂着的大单：每边按离现价从近到远，最多 wallMax 个。
func (d *Detector) Walls(mid float64) []Wall {
	if mid <= 0 {
		return nil
	}
	var near []float64
	for k, st := range d.levels {
		p := float64(k) * d.width
		if math.Abs(p-mid) <= mid*nearFrac && st.usd > 0 {
			near = append(near, st.usd)
		}
	}
	thr := math.Max(wallFloor, wallMult*median(near))
	var keys []int64
	for k, st := range d.levels {
		if math.Abs(float64(k)*d.width-mid) <= mid*wallRange && st.usd >= thr/2 {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	var bids, asks []Wall
	emit := func(group []int64) {
		if len(group) == 0 {
			return
		}
		sum, best := 0.0, group[0]
		touch := 0
		for _, k := range group {
			st := d.levels[k]
			sum += st.usd
			if st.usd > d.levels[best].usd {
				best = k
			}
			touch = max(touch, st.touch)
		}
		st := d.levels[best]
		if sum < thr || st.age < wallMinAge {
			return
		}
		p := float64(best) * d.width
		w := Wall{P: p, USD: sum, Bid: p < mid, Age: st.age, Touch: touch, Dist: round2((p - mid) / mid * 100)}
		if w.Bid {
			bids = append(bids, w)
		} else {
			asks = append(asks, w)
		}
	}
	var group []int64
	for _, k := range keys {
		if len(group) > 0 && k-group[len(group)-1] > wallMerge {
			emit(group)
			group = nil
		}
		group = append(group, k)
	}
	emit(group)
	sort.Slice(bids, func(i, j int) bool { return bids[i].P > bids[j].P })
	sort.Slice(asks, func(i, j int) bool { return asks[i].P < asks[j].P })
	if len(bids) > wallMax {
		bids = bids[:wallMax]
	}
	if len(asks) > wallMax {
		asks = asks[:wallMax]
	}
	return append(asks, bids...)
}

// Flow 最近 5 分钟的主动买入额、主动卖出额。
func (d *Detector) Flow() [2]float64 {
	var out [2]float64
	for _, x := range d.flow {
		out[0] += x[0]
		out[1] += x[1]
	}
	return out
}

func appendCap[T any](s []T, v T, n int) []T {
	s = append(s, v)
	if len(s) > n {
		s = s[len(s)-n:]
	}
	return s
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// rare 这个币上够不够少见。固定门槛在不同的币上差很多（实测 8 小时：BNB 撤单 273 次、BTC 6 次；
// BTC 大单 2054 次、DOGE 3 次），所以记下最近够条件的金额，只报排在前面的。不到 20 个时全报。
func rare(seen *[]float64, v, q float64) bool {
	const keep, least = 200, 20
	r := append(*seen, v)
	if len(r) > keep {
		r = r[len(r)-keep:]
	}
	*seen = r
	if len(r) < least {
		return true
	}
	s := append([]float64(nil), r...)
	sort.Float64s(s)
	return v >= s[int(q*float64(len(s)-1))]
}
