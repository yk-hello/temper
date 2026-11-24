// simaudit 量化模拟盘比真实乐观多少（模拟盘审查，2026-11-07）。只读币安公开行情，不下单、不碰任何账户。
//
// 同一份行情，跑同一个挂单策略，用两种成交规则各算一遍：
//
//	A 现行规则（trading/engine.go 的 onTrade、bookCross）：成交价穿过挂单价，不管多大全部成交；
//	  恰好在挂单价成交，按那笔成交量的一半成交；不看排在前面的量。
//	B 排队规则：前面排着的量要先被吃完才轮到我们；一笔成交能给我们的量，不超过这笔成交本身。
//
// 另外量三件事：吃单晚 200 毫秒成交差多少；盘口不被自己吃掉省了多少；盘口冻结 5 秒能看到多大的价差。
//
//	go run ./cmd/simaudit -dur 40m -out audit.json
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"

	"qs/internal/market"
)

const (
	makerFee = 0.0002
	takerFee = 0.0005
)

type lvl struct{ px, qty float64 }

type order struct {
	buy   bool
	px    float64
	rem   float64
	qty   float64
	queue float64
	t     int64
	line  int
}

type fillRec struct {
	t   int64
	buy bool
	qty float64
	px  float64
}

// 一个策略、一种规则、一个下单金额。
type sim struct {
	name       string
	queued     bool // false = A 现行规则，true = B 排队规则
	notion     float64
	grid       bool
	orders     []*order
	fills      []fillRec
	pending    []fillRec // 还没到时间算事后涨跌的
	edge5      float64   // 成交 5 秒后，相对中间价赚了多少（美元，已扣手续费）
	edge30     float64
	n5, n30    int
	notional   float64
	gridLines  []float64
	roundTrips int
	gridNet    float64
	base       float64 // 网格：持仓（用来算未实现盈亏）
	cash       float64
	lastTop    [2]float64
	cool       [2]int64
}

type inst struct {
	symbol    string
	bids      []lvl
	asks      []lvl
	mid       float64
	tick      float64
	sims      []*sim
	mids      []midPt
	nextTaker int64
	pendTaker []takerSnap
	cost      map[string]*sample // 吃单成本
	delayAbs  map[string]*sample
	split     map[string]*sample
	moves     map[string]*sample
	trades    int
	books     int
}

type midPt struct {
	t   int64
	mid float64
}

type takerSnap struct {
	t     int64
	mid   float64
	costs map[string]float64
}

type sample struct{ v []float64 }

func (s *sample) add(x float64) { s.v = append(s.v, x) }
func (s *sample) pct(p float64) float64 {
	if len(s.v) == 0 {
		return 0
	}
	c := append([]float64(nil), s.v...)
	sort.Float64s(c)
	return c[int(math.Min(float64(len(c)-1), p*float64(len(c))))]
}
func (s *sample) mean() float64 {
	if len(s.v) == 0 {
		return 0
	}
	t := 0.0
	for _, x := range s.v {
		t += x
	}
	return t / float64(len(s.v))
}

var takerSizes = []float64{10_000, 100_000, 1_000_000}

// walk 按盘口逐档吃 notional 美元，返回均价和吃满了没有。
func walk(side []lvl, notional float64) (float64, bool) {
	left, cost, qty := notional, 0.0, 0.0
	for _, l := range side {
		v := l.px * l.qty
		take := math.Min(left, v)
		q := take / l.px
		cost += q * l.px
		qty += q
		left -= take
		if left <= 0 {
			break
		}
	}
	if qty == 0 {
		return 0, false
	}
	return cost / qty, left <= 0
}

func levelQty(side []lvl, px float64) float64 {
	for _, l := range side {
		if l.px == px {
			return l.qty
		}
	}
	return 0
}

func (s *sim) record(in *inst, o *order, q float64, t int64) {
	if q <= 0 {
		return
	}
	o.rem -= q
	f := fillRec{t: t, buy: o.buy, qty: q, px: o.px}
	s.fills = append(s.fills, f)
	s.pending = append(s.pending, f)
	s.notional += q * o.px
	if o.buy {
		s.base += q
		s.cash -= q*o.px + q*o.px*makerFee
	} else {
		s.base -= q
		s.cash += q*o.px - q*o.px*makerFee
	}
}

// 一笔成交对一张挂单的作用。
func (s *sim) onTrade(in *inst, o *order, px, qty float64, aggressorBuy bool, t int64) {
	if o.rem <= 0 {
		return
	}
	through := (o.buy && px < o.px) || (!o.buy && px > o.px)
	at := px == o.px
	if !through && !at {
		return
	}
	if !s.queued { // A：现行规则，和 onTrade 一样
		if through {
			s.record(in, o, o.rem, t)
		} else {
			s.record(in, o, math.Min(o.rem, qty/2), t)
		}
		return
	}
	// B：只有吃向我们这一边的成交才算
	if o.buy == aggressorBuy {
		return
	}
	take := qty
	if through {
		o.queue = 0
	} else {
		d := math.Min(o.queue, take)
		o.queue -= d
		take -= d
	}
	s.record(in, o, math.Min(o.rem, take), t)
}

func (s *sim) onBook(in *inst, o *order, t int64) {
	if o.rem <= 0 {
		return
	}
	if o.buy {
		if len(in.asks) > 0 && in.asks[0].px < o.px { // 盘口越过了挂单价
			if s.queued {
				s.record(in, o, math.Min(o.rem, in.asks[0].qty), t)
			} else {
				s.record(in, o, o.rem, t)
			}
		}
		if s.queued {
			if l := levelQty(in.bids, o.px); l < o.queue && l > 0 {
				o.queue = l // 前面有人撤单。全算在我们前面撤的，对 B 有利，所以 B 是偏乐观的估计
			}
		}
		return
	}
	if len(in.bids) > 0 && in.bids[0].px > o.px {
		if s.queued {
			s.record(in, o, math.Min(o.rem, in.bids[0].qty), t)
		} else {
			s.record(in, o, o.rem, t)
		}
	}
	if s.queued {
		if l := levelQty(in.asks, o.px); l < o.queue && l > 0 {
			o.queue = l
		}
	}
}

func (s *sim) place(in *inst, buy bool, px float64, t int64, line int) *order {
	q := s.notion / px
	side := in.asks
	if buy {
		side = in.bids
	}
	o := &order{buy: buy, px: px, qty: q, rem: q, queue: levelQty(side, px), t: t, line: line}
	s.orders = append(s.orders, o)
	return o
}

// 贴盘口挂单：买一卖一各挂一张，成交后或者价格跑开了就重挂。
func (s *sim) stepQuote(in *inst, t int64) {
	if len(in.bids) == 0 || len(in.asks) == 0 {
		return
	}
	for i, buy := range []bool{true, false} {
		top := in.asks[0].px
		if buy {
			top = in.bids[0].px
		}
		var cur *order
		for _, o := range s.orders {
			if o.buy == buy && o.rem > 0 {
				cur = o
			}
		}
		if cur != nil {
			far := math.Abs(cur.px-top) > 2*in.tick
			if far || t-cur.t > 30_000 {
				cur.rem = 0
				cur = nil
			}
		}
		if cur == nil && t >= s.cool[i] {
			s.place(in, buy, top, t, 0)
			s.cool[i] = t + 500
		}
	}
	// 清掉做完的
	kept := s.orders[:0]
	for _, o := range s.orders {
		if o.rem > 0 {
			kept = append(kept, o)
		}
	}
	s.orders = kept
}

// 网格：和产品里的网格机器人一样，买了之后在上一格挂卖，卖了之后在下一格挂买。
func (s *sim) stepGrid(in *inst, t int64) {
	if len(in.bids) == 0 || len(in.asks) == 0 {
		return
	}
	if s.gridLines == nil {
		const lines, step = 21, 0.0005
		for i := 0; i < lines; i++ {
			s.gridLines = append(s.gridLines, roundTo(in.mid*(1+step*float64(i-lines/2)), in.tick))
		}
		for i, p := range s.gridLines {
			if p < in.bids[0].px {
				s.place(in, true, p, t, i)
			} else if p > in.asks[0].px {
				s.place(in, false, p, t, i)
			}
		}
		return
	}
	kept := s.orders[:0]
	var next []*order
	for _, o := range s.orders {
		if o.rem > 1e-12 {
			kept = append(kept, o)
			continue
		}
		if o.buy && o.line+1 < len(s.gridLines) {
			next = append(next, &order{buy: false, line: o.line + 1})
		} else if !o.buy && o.line > 0 {
			next = append(next, &order{buy: true, line: o.line - 1})
			s.roundTrips++
			gross := (s.gridLines[o.line] - s.gridLines[o.line-1]) * s.notion / s.gridLines[o.line]
			s.gridNet += gross - s.notion*makerFee*2
		}
	}
	s.orders = kept
	for _, n := range next {
		s.place(in, n.buy, s.gridLines[n.line], t, n.line)
	}
}

func roundTo(v, tick float64) float64 { return math.Round(v/tick) * tick }

// 事后涨跌：成交之后中间价往哪走。挂单容易在要跌的时候被买到（逆向选择），这一项能看出来。
func (s *sim) settle(in *inst, t int64) {
	kept := s.pending[:0]
	for _, f := range s.pending {
		age := t - f.t
		if age < 30_000 {
			kept = append(kept, f)
			if age >= 5000 && f.t%1 == 0 {
				continue
			}
			continue
		}
		sign := 1.0
		if !f.buy {
			sign = -1
		}
		m5 := in.midAt(f.t + 5000)
		m30 := in.midAt(f.t + 30000)
		fee := f.qty * f.px * makerFee
		s.edge5 += sign*(m5-f.px)*f.qty - fee
		s.edge30 += sign*(m30-f.px)*f.qty - fee
		s.n5++
		s.n30++
	}
	s.pending = kept
}

func (in *inst) midAt(t int64) float64 {
	i := sort.Search(len(in.mids), func(i int) bool { return in.mids[i].t >= t })
	if i >= len(in.mids) {
		return in.mid
	}
	return in.mids[i].mid
}

type audit struct {
	mu       sync.Mutex
	insts    map[string]*inst
	start    int64
	raw      int
	feedGaps []int64
	last     int64
}

func (a *audit) UpdateStatus(func(*market.SourceStatus)) {}

func f(s string) float64 { v, _ := strconv.ParseFloat(s, 64); return v }

func (a *audit) Publish(ev market.Event) {
	a.mu.Lock()
	defer a.mu.Unlock()
	in := a.insts[ev.Symbol]
	if in == nil {
		return
	}
	now := time.Now().UnixMilli()
	a.raw++
	if a.last > 0 && now-a.last > 3000 {
		a.feedGaps = append(a.feedGaps, now-a.last)
	}
	a.last = now
	switch ev.Kind {
	case market.KindTrade:
		in.trades++
		px, qty := f(ev.Price), f(ev.Qty)
		for _, s := range in.sims {
			for _, o := range s.orders {
				s.onTrade(in, o, px, qty, ev.Side == market.Buy, now)
			}
		}
	case market.KindDepth:
		in.books++
		in.bids, in.asks = in.bids[:0], in.asks[:0]
		for _, l := range ev.Bids {
			in.bids = append(in.bids, lvl{f(l[0]), f(l[1])})
		}
		for _, l := range ev.Asks {
			in.asks = append(in.asks, lvl{f(l[0]), f(l[1])})
		}
		if len(in.bids) == 0 || len(in.asks) == 0 {
			return
		}
		in.mid = (in.bids[0].px + in.asks[0].px) / 2
		in.mids = append(in.mids, midPt{now, in.mid})
		for _, s := range in.sims {
			for _, o := range s.orders {
				s.onBook(in, o, now)
			}
			if s.grid {
				s.stepGrid(in, now)
			} else {
				s.stepQuote(in, now)
			}
			s.settle(in, now)
		}
		in.stepTaker(now)
	}
}

func (in *inst) key(sz float64) string { return fmt.Sprintf("%.0f", sz) }

// 吃单：现在成交和晚 200 毫秒成交差多少；一次吃和拆成十次吃（盘口不被自己吃掉）差多少。
func (in *inst) stepTaker(now int64) {
	// 处理到时间的
	kept := in.pendTaker[:0]
	for _, p := range in.pendTaker {
		if now-p.t < 200 {
			kept = append(kept, p)
			continue
		}
		for _, sz := range takerSizes {
			avg, ok := walk(in.asks, sz)
			if !ok {
				continue
			}
			k := in.key(sz)
			if was, has := p.costs[k]; has {
				in.delayAbs[k].add(math.Abs(avg-was) / p.mid * 1e4)
			}
		}
	}
	in.pendTaker = kept
	if now < in.nextTaker {
		return
	}
	in.nextTaker = now + 5000
	costs := map[string]float64{}
	for _, sz := range takerSizes {
		avg, ok := walk(in.asks, sz)
		if !ok {
			in.cost[in.key(sz)+"_nodepth"].add(1)
			continue
		}
		k := in.key(sz)
		costs[k] = avg
		in.cost[k].add((avg - in.mid) / in.mid * 1e4)
		small, ok2 := walk(in.asks, sz/10)
		if ok2 {
			in.split[k].add((avg - small) / in.mid * 1e4)
		}
	}
	in.pendTaker = append(in.pendTaker, takerSnap{now, in.mid, costs})
	// 盘口冻结期间价格能走多远
	for _, d := range []int64{1000, 5000} {
		if old := in.midAt(now - d); old > 0 && len(in.mids) > 0 && in.mids[0].t < now-d {
			in.moves[fmt.Sprintf("%dms", d)].add(math.Abs(in.mid-old) / old * 1e4)
		}
	}
}

func (a *audit) report() map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := map[string]any{"seconds": (time.Now().UnixMilli() - a.start) / 1000, "events": a.raw, "feed_gaps_over_3s": len(a.feedGaps)}
	insts := map[string]any{}
	for name, in := range a.insts {
		r := map[string]any{"trades": in.trades, "books": in.books, "mid": in.mid}
		sims := []map[string]any{}
		for _, s := range in.sims {
			item := map[string]any{"name": s.name, "fills": len(s.fills), "notional": math.Round(s.notional), "edge5_usd": math.Round(s.edge5*100) / 100, "edge30_usd": math.Round(s.edge30*100) / 100, "settled": s.n5}
			if s.notional > 0 && s.n5 > 0 {
				tot := 0.0
				for i := 0; i < s.n5 && i < len(s.fills); i++ {
					tot += s.fills[i].px * s.fills[i].qty
				}
				if tot > 0 {
					item["edge5_bps"] = math.Round(s.edge5/tot*1e4*100) / 100
					item["edge30_bps"] = math.Round(s.edge30/tot*1e4*100) / 100
				}
			}
			if s.grid {
				item["round_trips"] = s.roundTrips
				item["grid_net_usd"] = math.Round(s.gridNet*100) / 100
				item["mtm_usd"] = math.Round((s.cash+s.base*in.mid)*100) / 100
			}
			sims = append(sims, item)
		}
		r["sims"] = sims
		tk := map[string]any{}
		for _, sz := range takerSizes {
			k := in.key(sz)
			tk[k] = map[string]any{
				"cost_bps_mean": round2(in.cost[k].mean()), "samples": len(in.cost[k].v), "no_depth": len(in.cost[k+"_nodepth"].v),
				"delay200ms_abs_bps_mean": round2(in.delayAbs[k].mean()), "delay200ms_abs_bps_p95": round2(in.delayAbs[k].pct(.95)),
				"split10_saves_bps_mean": round2(in.split[k].mean()), "split10_saves_bps_p95": round2(in.split[k].pct(.95)),
			}
		}
		r["taker"] = tk
		mv := map[string]any{}
		for k, s := range in.moves {
			over := 0
			for _, x := range s.v {
				if x > (takerFee*2)*1e4 {
					over++
				}
			}
			share := 0.0
			if len(s.v) > 0 {
				share = float64(over) / float64(len(s.v))
			}
			mv[k] = map[string]any{"p50_bps": round2(s.pct(.5)), "p95_bps": round2(s.pct(.95)), "p99_bps": round2(s.pct(.99)), "over_roundtrip_fee_share": round2(share * 100)}
		}
		r["freeze_move"] = mv
		insts[name] = r
	}
	out["instruments"] = insts
	return out
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

func main() {
	dur := flag.Duration("dur", 40*time.Minute, "跑多久")
	outPath := flag.String("out", "simaudit.json", "结果写到哪")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), *dur)
	defer cancel()
	symbols := map[string]float64{"BTCUSDT": 0.1, "ETHUSDT": 0.01, "SOLUSDT": 0.01}
	a := &audit{insts: map[string]*inst{}, start: time.Now().UnixMilli()}
	list := []string{}
	for sym, tick := range symbols {
		list = append(list, sym)
		in := &inst{symbol: sym, tick: tick, cost: map[string]*sample{}, delayAbs: map[string]*sample{}, split: map[string]*sample{}, moves: map[string]*sample{"1000ms": {}, "5000ms": {}}}
		for _, sz := range takerSizes {
			k := in.key(sz)
			in.cost[k], in.cost[k+"_nodepth"], in.delayAbs[k], in.split[k] = &sample{}, &sample{}, &sample{}, &sample{}
		}
		for _, n := range []float64{5_000, 100_000, 1_000_000} {
			in.sims = append(in.sims,
				&sim{name: fmt.Sprintf("盘口挂单 %.0f 现行", n), notion: n},
				&sim{name: fmt.Sprintf("盘口挂单 %.0f 排队", n), notion: n, queued: true})
		}
		for _, n := range []float64{1_000, 50_000} {
			in.sims = append(in.sims,
				&sim{name: fmt.Sprintf("网格 每格 %.0f 现行", n), notion: n, grid: true},
				&sim{name: fmt.Sprintf("网格 每格 %.0f 排队", n), notion: n, grid: true, queued: true})
		}
		a.insts[sym] = in
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	go (&market.Binance{Symbols: list, Log: log}).Run(ctx, a)
	save := func() {
		b, _ := json.MarshalIndent(a.report(), "", " ")
		_ = os.WriteFile(*outPath, b, 0o644)
	}
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			save()
			fmt.Println("done")
			return
		case <-t.C:
			save()
		}
	}
}
