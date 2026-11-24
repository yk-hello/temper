package trading

// 断流补算（审查第 4 条）：行情断了一段（网络、交易所、电脑睡眠），这期间价格可能穿过止损价、强平价又回来。
// 恢复后取这段时间币安的 1 分钟 K 线，按每分钟的最高最低价补查止损、止盈和强平，
// 不然模拟账户会躲过真实账户躲不过的亏损。
//
// 规则（宁可偏保守）：
//   - 只管断流开始前就有的仓位和委托；断流以后新开的不动
//   - 强平和标记价触发的止损止盈看标记价 K 线；按最新价触发的看成交价 K 线
//   - 同一分钟里先碰到哪个分不清时：止损先于止盈，强平先于止盈；止损和强平按价格谁先到算
//   - 成交价：触发价（或强平价）；那一分钟开盘就已经越过（跳空）的，按开盘价
//   - 挂着的限价单不补（要成交量，K 线给不了排队信息）；没触发的条件开仓单也不补

import (
	"context"
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

// Bar1m 一分钟 K 线：开盘、最高、最低。
type Bar1m struct {
	T       int64
	O, H, L decimal.Decimal
}

type priceGap struct {
	inst     string
	from, to int64
	tries    int
	next     int64 // 下次重试的时刻
}

// AddGap 记一段要补算的时段。后端没运行的那段（启动时按行情录制的最后时刻算）由 main 调用。
func (e *Engine) AddGap(inst string, from, to int64) {
	if to-from < StaleAfter.Milliseconds() {
		return
	}
	e.mu.Lock()
	e.gaps = append(e.gaps, priceGap{inst: inst, from: from, to: to})
	e.mu.Unlock()
}

// watchOutage 每次 Tick 调用：盘口停了记下从哪一刻开始，恢复时把这一段记成要补算的时段。
func (e *Engine) watchOutage(inst string) {
	if e.staleFor(inst) > 0 {
		if _, ok := e.outage[inst]; !ok {
			if b, ok := e.Books.Book(e.Venue, inst); ok && b.Recv > 0 {
				if e.outage == nil {
					e.outage = map[string]int64{}
				}
				e.outage[inst] = b.Recv
			}
		}
		return
	}
	if from, ok := e.outage[inst]; ok {
		delete(e.outage, inst)
		e.gaps = append(e.gaps, priceGap{inst: inst, from: from, to: time.Now().UnixMilli()})
	}
}

// replayGaps 取 K 线补算等着的时段。取不到就过一会儿再试，最多试 30 次（K 线接口偶尔失败）。
func (e *Engine) replayGaps(ctx context.Context) int {
	e.mu.Lock()
	fetch := e.Bars1m
	now := time.Now().UnixMilli()
	var todo, later []priceGap
	for _, g := range e.gaps {
		if g.next > now {
			later = append(later, g)
		} else {
			todo = append(todo, g)
		}
	}
	e.gaps = later
	e.mu.Unlock()
	if fetch == nil || len(todo) == 0 {
		return 0
	}
	n := 0
	var retry []priceGap
	for _, g := range todo {
		mark, err1 := fetch(ctx, g.inst, g.from, g.to, true)
		last, err2 := fetch(ctx, g.inst, g.from, g.to, false)
		if err1 != nil || err2 != nil {
			g.tries++
			if g.tries < 30 {
				g.next = now + int64(g.tries)*20_000
				retry = append(retry, g)
			} else {
				e.Log.Warn("trading: 断流补算一直取不到 K 线，放弃这一段", "instrument", g.inst, "from", g.from, "to", g.to)
			}
			continue
		}
		n += e.ReplayBars(g.inst, g.from, g.to, mark, last)
	}
	if len(retry) > 0 {
		e.mu.Lock()
		e.gaps = append(e.gaps, retry...)
		e.mu.Unlock()
	}
	return n
}

// ReplayBars 按断流期间（from–to）的 1 分钟 K 线补查止损、止盈和强平。返回补了几笔。
// mark 是标记价 K 线，last 是成交价 K 线（没有就用标记价的）。
func (e *Engine) ReplayBars(inst string, from, to int64, mark, last []Bar1m) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.Specs.Get(inst); !ok {
		return 0
	}
	lastAt := make(map[int64]Bar1m, len(last))
	for _, b := range last {
		lastAt[b.T] = b
	}
	sort.Slice(mark, func(i, j int) bool { return mark[i].T < mark[j].T })
	n := 0
	for _, mb := range mark {
		if mb.T+60_000 <= from || mb.T >= to {
			continue
		}
		lb, ok := lastAt[mb.T]
		if !ok {
			lb = mb
		}
		for _, id := range e.accountIDs() {
			a := e.accounts[id]
			if a.Archived {
				continue
			}
			if e.replayOne(a, inst, from, mb, lb) {
				n++
			}
		}
	}
	return n
}

// replayOne 这个账户在这一分钟里先碰到了什么。碰到了就成交，返回 true。
func (e *Engine) replayOne(a *Account, inst string, from int64, mb, lb Bar1m) bool {
	p := a.Pos[inst]
	if p == nil || p.Qty.IsZero() || p.Opened > from {
		return false
	}
	long := p.Qty.Sign() > 0
	// 价格往不利方向走时先碰到的排前面：多头是从高往低，空头是从低往高
	worse := func(x, y decimal.Decimal) bool { // x 比 y 先碰到（沿不利方向）
		if long {
			return x.GreaterThan(y)
		}
		return x.LessThan(y)
	}
	// 止损：沿不利方向碰到的第一张
	var sl *Order
	var slPx decimal.Decimal
	var tp *Order
	var tpPx decimal.Decimal
	for _, o := range sortedOrders(a) {
		if o.Instrument != inst || o.Status != StNew || (o.Type != StopMarket && o.Type != TakeProfit) || o.Created > from || o.buy() == long {
			continue // 只看平这个仓位方向的（多头的止损止盈是卖单）
		}
		b := mb
		if o.TriggerBy == "last" {
			b = lb
		}
		trig := o.TriggerPx
		if o.Type == StopMarket {
			// 多头止损：最低价 ≤ 触发价；开盘就在触发价下面按开盘价
			hit := (long && b.L.LessThanOrEqual(trig)) || (!long && b.H.GreaterThanOrEqual(trig))
			if !hit {
				continue
			}
			px := trig
			if (long && b.O.LessThan(trig)) || (!long && b.O.GreaterThan(trig)) {
				px = b.O
			}
			if sl == nil || worse(px, slPx) {
				sl, slPx = o, px
			}
			continue
		}
		hit := (long && b.H.GreaterThanOrEqual(trig)) || (!long && b.L.LessThanOrEqual(trig))
		if !hit {
			continue
		}
		px := trig
		if (long && b.O.GreaterThan(trig)) || (!long && b.O.LessThan(trig)) {
			px = b.O
		}
		if tp == nil {
			tp, tpPx = o, px
		}
	}
	// 强平：按这一分钟标记价最不利的一点算，和引擎实时强平是同一个条件
	adverse := mb.L
	if !long {
		adverse = mb.H
	}
	cur := e.marks()
	at := func(px decimal.Decimal) marks {
		return func(i string) (decimal.Decimal, bool) {
			if i == inst {
				return px, true
			}
			return cur(i)
		}
	}
	spec, _ := e.Specs.Get(inst)
	iso := a.setting(inst).Mode == "isolated"
	liq := false
	if iso {
		liq = p.IsoMargin.Add(upnl(p, adverse)).LessThanOrEqual(spec.MaintMargin(p.Qty.Abs().Mul(adverse)))
	} else {
		m := e.compute(a, at(adverse))
		liq = m.crossMM.Sign() > 0 && m.crossWB.Add(m.crossUPnL).LessThanOrEqual(m.crossMM)
	}
	liqPx := adverse
	if liq {
		if lp := e.positionLiq(a, inst, cur); lp.Sign() > 0 {
			liqPx = lp.Round(int32(-spec.Tick.Exponent())) // 按最小价格单位，和界面上的强平价一致
		}
		// 夹在这一分钟的开盘和最不利价之间：开盘就越过了按开盘价
		if long {
			liqPx = decimal.Max(adverse, decimal.Min(liqPx, mb.O))
		} else {
			liqPx = decimal.Min(adverse, decimal.Max(liqPx, mb.O))
		}
	}
	// 标记价 K 线带很多位小数：成交价取整到最小价格单位
	dp := int32(-spec.Tick.Exponent())
	slPx, tpPx, liqPx = slPx.Round(dp), tpPx.Round(dp), liqPx.Round(dp)
	switch {
	case sl != nil && (!liq || !worse(liqPx, slPx)): // 止损比强平先到（或没到强平）
		e.replayFill(a, sl, slPx, mb.T)
		return true
	case liq:
		e.emit(Event{Kind: "notice", Account: a.ID, Note: &Notice{Kind: "gap_replay", Instrument: inst, Px: liqPx, Reason: "liquidation", At: mb.T}})
		if iso {
			e.liquidateIso(a, inst, liqPx)
		} else {
			m := e.compute(a, at(liqPx))
			e.liquidateCross(a, at(liqPx), m.crossMM)
		}
		return true
	case tp != nil:
		e.replayFill(a, tp, tpPx, mb.T)
		return true
	}
	return false
}

// replayFill 止损、止盈按补算出来的价格成交（吃单手续费），并留一条提醒说明是补算的。
func (e *Engine) replayFill(a *Account, o *Order, px decimal.Decimal, at int64) {
	q := e.reducible(a, o)
	if !o.ClosePos {
		q = decimal.Min(q, o.Qty)
	}
	if q.Sign() <= 0 {
		e.closeOrder(a, o, StCanceled, "NO_POSITION")
		return
	}
	kind := "sl"
	if o.Type == TakeProfit {
		kind = "tp"
	}
	e.emit(Event{Kind: "notice", Account: a.ID, Note: &Notice{Kind: "gap_replay", Instrument: o.Instrument, Qty: q, Px: px, Reason: kind, At: at}})
	e.emit(Event{Kind: "trigger", Account: a.ID, Amend: &AmendEv{ID: o.ID, Qty: q}})
	if cur, ok := a.Orders[o.ID]; ok {
		e.fill(a, cur, q, px, false)
	}
	e.expireRest(a, o, "NO_LIQUIDITY")
}
