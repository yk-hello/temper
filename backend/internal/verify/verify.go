// Package verify 策略的验证报告（D96 第 2 条）。
// 只算数字、不下结论：门槛由看报告的人自己定（界面上能改），这里不判断"合格"。
// 数据都是平台已经有的：模型组合的净值和交易、收到的信号、执行回报、币安的 1 小时收盘价。
package verify

import (
	"encoding/json"
	"math"
	"math/rand/v2"
	"sort"
	"strconv"
	"strings"

	"qs/internal/portfolio"
)

// Signal 一条被接受的信号（从 qsig 的原始消息里取出报告要用的字段）。
type Signal struct {
	ID      string
	At      int64 // 收到时间，毫秒
	Version string
	CostUSD float64
	Targets []Target
	// Done 执行完成的时间（毫秒，所有品种里最晚的那个）；0 = 没有执行回报
	Done int64
}

type Target struct {
	Inst       string
	Weight     float64
	Conf       float64
	HorizonMin int
	RefPx      float64
}

// ParseSignal 从原始消息取字段。坏的消息返回 false。
func ParseSignal(id string, at int64, raw []byte) (Signal, bool) {
	var m struct {
		Version string `json:"strategy_version"`
		Targets []struct {
			Instrument string  `json:"instrument"`
			Weight     string  `json:"weight"`
			Confidence float64 `json:"confidence"`
			Horizon    int     `json:"horizon_min"`
			RefPrice   string  `json:"ref_price"`
		} `json:"targets"`
		Meta struct {
			CostUSD float64 `json:"cost_usd"`
		} `json:"meta"`
	}
	if json.Unmarshal(raw, &m) != nil || len(m.Targets) == 0 {
		return Signal{}, false
	}
	s := Signal{ID: id, At: at, Version: m.Version, CostUSD: m.Meta.CostUSD}
	for _, t := range m.Targets {
		w, _ := strconv.ParseFloat(t.Weight, 64)
		px, _ := strconv.ParseFloat(t.RefPrice, 64)
		s.Targets = append(s.Targets, Target{Inst: t.Instrument, Weight: w, Conf: t.Confidence, HorizonMin: t.Horizon, RefPx: px})
	}
	return s, true
}

// Prices 一个品种的 1 小时收盘价，最后一根是当前这一小时；T0 是第一根的时间（毫秒）。
type Prices struct {
	T0     int64
	Closes []float64
}

const hour = int64(3600_000)

// At 某一时刻的价格（取那一小时的收盘价）；超出范围返回 0。
func (p Prices) At(t int64) float64 {
	if len(p.Closes) == 0 || t < p.T0 {
		return 0
	}
	i := int((t - p.T0) / hour)
	if i >= len(p.Closes) {
		return 0
	}
	return p.Closes[i]
}

// Input 算报告要的东西。
type Input struct {
	Perf    portfolio.Performance
	Signals []Signal          // 按时间先后
	Prices  map[string]Prices // 品种 → 1 小时收盘价（至少要有 BTC-PERP）
	Now     int64
	FeeRate float64 // 对照组用的单边手续费率（吃单 0.05%）
	Seed    uint64  // 随机对照组的种子（测试用；0 = 按策略 id 定，同一份数据每次结果一样）
}

// Report 验证报告。nil 的字段表示数据不够算不出来，界面写"还不够"。
type Report struct {
	StrategyID string  `json:"strategy_id"`
	Since      int64   `json:"since"`
	Days       float64 `json:"days"`   // 前向时长
	Trades     int     `json:"trades"` // 已平仓笔数
	Return     float64 `json:"return"` // 扣手续费、资金费后的总收益
	// 前半段、后半段（按时间对半分）：只在一段里赚钱的策略要小心
	FirstHalf   *float64 `json:"first_half"`
	SecondHalf  *float64 `json:"second_half"`
	Sharpe      *float64 `json:"sharpe"`
	MaxDrawdown float64  `json:"max_drawdown"`
	Fees        float64  `json:"fees"`
	// 成本加 50%：手续费多付一半以后的收益
	CostPlus50 float64 `json:"cost_plus50"`
	// 推理成本（AI 策略在信号里报的 cost_usd 合计）和扣掉它以后的收益
	InferenceUSD   float64 `json:"inference_usd"`
	ReturnAfterInf float64 `json:"return_after_inference"`
	// 最赚钱的一笔占全部盈利笔的利润的比例：太高说明靠运气
	BestShare *float64 `json:"best_share"`
	// 对照组：同一段时间
	HoldBTC  *float64 `json:"hold_btc"` // 一直拿着 BTC
	Momentum *float64 `json:"momentum"` // BTC 简单动量：比 24 小时前高就做多、低就做空，每小时看一次，扣手续费
	Random   *Random  `json:"random"`   // 同样的品种、同样的持仓时长、随机时间和方向
	// 信心校准：信心高的信号是不是真的更准（按信号自己说的时长看结果）
	Calibration []Bucket `json:"calibration"`
	// 延迟：收到信号到执行完成
	LatencyP50 *int64 `json:"latency_p50_ms"`
	LatencyP90 *int64 `json:"latency_p90_ms"`
	Signals    int    `json:"signals"`
	// 版本：考察期里改过版本，成绩应该从最后一次改版算
	Versions []Version `json:"versions"`
	Notes    []string  `json:"notes"` // 算不出来的原因（代码，界面翻译）
}

type Random struct {
	Trials    int     `json:"trials"`
	StratMean float64 `json:"strategy_mean"` // 策略每笔交易的平均收益（按价格变动，扣双边手续费）
	Median    float64 `json:"median"`        // 随机进出每笔的平均收益的中位数
	P90       float64 `json:"p90"`
	Beat      float64 `json:"beat"` // 策略跑赢了多少比例的随机结果
}

type Bucket struct {
	Label string  `json:"label"` // lo（< 0.5）| mid（0.5–0.7）| hi（≥ 0.7）
	N     int     `json:"n"`
	Avg   float64 `json:"avg"` // 平均方向收益：信号让仓位变多就看涨了多少，变少就看跌了多少
	Hit   float64 `json:"hit"` // 方向对的比例
}

type Version struct {
	Version string `json:"version"`
	From    int64  `json:"from"`
	Signals int    `json:"signals"`
}

func ptr[T any](v T) *T { return &v }

// Build 算报告。
func Build(in Input) Report {
	p := in.Perf
	r := Report{StrategyID: p.StrategyID, Since: p.Since, Trades: p.Trades, Return: p.Return, Sharpe: p.Sharpe, MaxDrawdown: p.MaxDrawdown, Notes: []string{}, Calibration: []Bucket{}, Versions: []Version{}}
	initial, _ := p.Initial.Float64()
	nav, _ := p.NAV.Float64()
	fees, _ := p.Fees.Float64()
	r.Fees = fees
	if p.Since > 0 {
		r.Days = float64(in.Now-p.Since) / float64(24*hour)
	}
	if initial > 0 {
		r.CostPlus50 = (nav-fees*0.5)/initial - 1
	}
	// 前半段、后半段
	if n := len(p.Curve); n >= 3 {
		mid := p.Curve[0].T + (p.Curve[n-1].T-p.Curve[0].T)/2
		k := sort.Search(n, func(i int) bool { return p.Curve[i].T >= mid })
		if k > 0 && k < n && p.Curve[0].NAV > 0 && p.Curve[k].NAV > 0 {
			r.FirstHalf = ptr(p.Curve[k].NAV/p.Curve[0].NAV - 1)
			r.SecondHalf = ptr(p.Curve[n-1].NAV/p.Curve[k].NAV - 1)
		}
	}
	// 最赚钱的一笔
	var best, pos float64
	for _, t := range p.Closed {
		v, _ := t.Net.Float64()
		if v > 0 {
			pos += v
			best = math.Max(best, v)
		}
	}
	if pos > 0 {
		r.BestShare = ptr(best / pos)
	}
	// 信号：推理成本、版本、延迟
	var lat []int64
	for _, s := range in.Signals {
		r.Signals++
		r.InferenceUSD += s.CostUSD
		if n := len(r.Versions); n == 0 || r.Versions[n-1].Version != s.Version {
			r.Versions = append(r.Versions, Version{Version: s.Version, From: s.At})
		}
		r.Versions[len(r.Versions)-1].Signals++
		if s.Done > s.At {
			lat = append(lat, s.Done-s.At)
		}
	}
	if initial > 0 {
		r.ReturnAfterInf = (nav-r.InferenceUSD)/initial - 1
	}
	if len(lat) > 0 {
		sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
		r.LatencyP50, r.LatencyP90 = ptr(lat[len(lat)/2]), ptr(lat[min(len(lat)-1, len(lat)*9/10)])
	}
	r.Calibration = calibrate(in)
	// 对照组
	btc := in.Prices["BTC-PERP"]
	start, end := p.Since, in.Now
	if a, b := btc.At(start), btc.At(end-1); a > 0 && b > 0 && end > start {
		r.HoldBTC = ptr(b/a - 1)
		r.Momentum = momentum(btc, start, end, in.FeeRate)
	} else {
		r.Notes = append(r.Notes, "NO_PRICES")
	}
	r.Random = random(in)
	if r.Days < 1 {
		r.Notes = append(r.Notes, "TOO_SHORT")
	}
	return r
}

// momentum BTC 简单动量：每小时看一次，比 24 小时前高就做多、低就做空；换方向时扣双边手续费。
func momentum(px Prices, start, end int64, fee float64) *float64 {
	i0 := int((start - px.T0) / hour)
	i1 := int((end - px.T0) / hour)
	if i0 < 24 || i1 > len(px.Closes) || i1-i0 < 2 {
		return nil
	}
	eq, side := 1.0, 0.0
	for i := i0; i < i1-1; i++ {
		want := 1.0
		if px.Closes[i] < px.Closes[i-24] {
			want = -1
		}
		if want != side {
			eq *= 1 - fee*math.Abs(want-side) // 反手要平再开：两倍手续费
			side = want
		}
		eq *= 1 + side*(px.Closes[i+1]/px.Closes[i]-1)
	}
	return ptr(eq - 1)
}

// random 随机对照：每笔真实交易换成同一品种、同样持仓时长、随机开始时间和方向的一笔，看策略每笔平均收益排在哪。
func random(in Input) *Random {
	type tr struct {
		inst     string
		dur, ret float64
	}
	var real []tr
	for _, t := range in.Perf.Closed {
		px := in.Prices[t.Instrument]
		entry, _ := t.Entry.Float64()
		exit, _ := t.Exit.Float64()
		if len(px.Closes) == 0 || entry <= 0 || t.CloseTime <= t.OpenTime {
			continue
		}
		side := 1.0
		if t.Side == "short" {
			side = -1
		}
		real = append(real, tr{t.Instrument, float64(t.CloseTime - t.OpenTime), side*(exit/entry-1) - 2*in.FeeRate})
	}
	if len(real) < 5 {
		return nil
	}
	seed := in.Seed
	if seed == 0 {
		for _, c := range in.Perf.StrategyID {
			seed = seed*31 + uint64(c)
		}
	}
	rng := rand.New(rand.NewPCG(seed, 7))
	var strat float64
	for _, t := range real {
		strat += t.ret
	}
	strat /= float64(len(real))
	const trials = 500
	means := make([]float64, 0, trials)
	for k := 0; k < trials; k++ {
		sum, n := 0.0, 0
		for _, t := range real {
			px := in.Prices[t.inst]
			first, last := px.T0, px.T0+int64(len(px.Closes)-1)*hour
			if in.Perf.Since > first {
				first = in.Perf.Since
			}
			span := float64(last-first) - t.dur
			if span <= 0 {
				continue
			}
			s := first + int64(rng.Float64()*span)
			a, b := px.At(s), px.At(s+int64(t.dur))
			if a <= 0 || b <= 0 {
				continue
			}
			side := 1.0
			if rng.IntN(2) == 0 {
				side = -1
			}
			sum += side*(b/a-1) - 2*in.FeeRate
			n++
		}
		if n > 0 {
			means = append(means, sum/float64(n))
		}
	}
	if len(means) < trials/2 {
		return nil
	}
	sort.Float64s(means)
	beat := sort.SearchFloat64s(means, strat)
	return &Random{Trials: len(means), StratMean: strat, Median: means[len(means)/2], P90: means[len(means)*9/10], Beat: float64(beat) / float64(len(means))}
}

// calibrate 信心校准：每条让某个品种仓位变化的目标，按它自己说的时长看价格往哪走。
func calibrate(in Input) []Bucket {
	prev := map[string]float64{}
	type acc struct {
		n, hit int
		sum    float64
	}
	b := map[string]*acc{"lo": {}, "mid": {}, "hi": {}}
	for _, s := range in.Signals {
		for _, t := range s.Targets {
			d := t.Weight - prev[t.Inst]
			prev[t.Inst] = t.Weight
			if math.Abs(d) < 1e-9 || t.RefPx <= 0 || t.HorizonMin <= 0 {
				continue
			}
			endT := s.At + int64(t.HorizonMin)*60_000
			if endT > in.Now {
				continue // 还没到它说的时长
			}
			px := in.Prices[t.Inst].At(endT)
			if px <= 0 {
				continue
			}
			dir := 1.0
			if d < 0 {
				dir = -1
			}
			ret := dir * (px/t.RefPx - 1)
			key := "mid"
			switch {
			case t.Conf < 0.5:
				key = "lo"
			case t.Conf >= 0.7:
				key = "hi"
			}
			a := b[key]
			a.n++
			a.sum += ret
			if ret > 0 {
				a.hit++
			}
		}
	}
	out := []Bucket{}
	for _, k := range []string{"lo", "mid", "hi"} {
		a := b[k]
		bk := Bucket{Label: k, N: a.n}
		if a.n > 0 {
			bk.Avg, bk.Hit = a.sum/float64(a.n), float64(a.hit)/float64(a.n)
		}
		out = append(out, bk)
	}
	return out
}

// Instruments 报告要哪些品种的价格（BTC 加上交易过和信号里出现过的）。
func Instruments(p portfolio.Performance, sigs []Signal) []string {
	set := map[string]bool{"BTC-PERP": true}
	for _, t := range p.Closed {
		set[t.Instrument] = true
	}
	for _, s := range sigs {
		for _, t := range s.Targets {
			set[t.Inst] = true
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		if strings.HasSuffix(k, "-PERP") {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
