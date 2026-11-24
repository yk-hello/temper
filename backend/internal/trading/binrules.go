package trading

import (
	"strconv"

	"github.com/shopspring/decimal"
)

// 币安还有、模拟盘之前没做的三条下单规则（D103"最大限度拟真"，`16` 里写着没做的）：
//   - GTD：限价单有效到某个时间，到点自动过期（币安要求至少比现在晚 600 秒，精确到秒）
//   - priceMatch：不填价格，按下单那一刻的盘口定价。对手价 = 对面第 N 档，排队价 = 自己这边第 N 档（币安 App 里叫 BBO）
//   - 条件单的价格保护（priceProtect）：标记价和最新价差超过这个品种的 triggerProtect 时不触发，等两者靠拢再说

// GTD 有效到指定时间（限价单）。
const GTD = "GTD"

// GTDMin 币安：goodTillDate 至少比现在晚 600 秒。
const GTDMin = 600_000

// PriceMatches 币安 priceMatch 的取值：对手价（OPPONENT）或排队价（QUEUE），第 1、5、10、20 档。
var PriceMatches = map[string]struct {
	Opp bool
	N   int
}{
	"OPPONENT": {true, 1}, "OPPONENT_5": {true, 5}, "OPPONENT_10": {true, 10}, "OPPONENT_20": {true, 20},
	"QUEUE": {false, 1}, "QUEUE_5": {false, 5}, "QUEUE_10": {false, 10}, "QUEUE_20": {false, 20},
}

// matchPrice 按 priceMatch 从盘口取价：对手价看对面（买单看卖盘），排队价看自己这边；盘口不够 N 档时取最远的一档。
func (e *Engine) matchPrice(inst string, buy bool, pm string) (decimal.Decimal, error) {
	m, ok := PriceMatches[pm]
	if !ok {
		return zero, rej("BAD_PRICE_MATCH")
	}
	side := buy // levels(inst, true) 是买单要吃的那边（卖盘）
	if !m.Opp {
		side = !buy
	}
	lv := e.levels(inst, side)
	if len(lv) == 0 {
		return zero, rej("NO_LIQUIDITY")
	}
	i := m.N - 1
	if i >= len(lv) {
		i = len(lv) - 1
	}
	px := lv[i].Px
	// 交易所的盘口本来就在最小价格单位上；模拟行情的盘口不一定，按对自己不吃亏的方向取整（买往下、卖往上）
	if spec, ok := e.Specs.Get(inst); ok && spec.Tick.Sign() > 0 && !spec.OnTick(px) {
		if buy {
			px = px.Div(spec.Tick).Floor().Mul(spec.Tick)
		} else {
			px = px.Div(spec.Tick).Ceil().Mul(spec.Tick)
		}
	}
	return px, nil
}

// checkGTD 校验并规整有效期：至少比现在晚 600 秒，毫秒部分去掉（币安只认到秒）。
func checkGTD(goodTill, now int64) (int64, error) {
	if goodTill < now+GTDMin {
		return 0, rej("GTD_TOO_SOON", "min", strconv.Itoa(GTDMin/60_000))
	}
	return goodTill / 1000 * 1000, nil
}

// expireGTD 到期的 GTD 委托过期（和币安一样，状态"已过期"，原因 GTD）。
func (e *Engine) expireGTD(now int64) {
	for _, id := range e.accountIDs() {
		a := e.accounts[id]
		if a.Archived {
			continue
		}
		for _, o := range sortedOrders(a) {
			if o.TIF == GTD && o.GoodTill > 0 && o.GoodTill <= now && (o.Status == StNew || o.Status == StPartial) {
				e.closeOrder(a, o, StExpired, "GTD")
			}
		}
	}
}

// protectBlocks 条件单开了价格保护时：标记价和最新价差超过 triggerProtect 就这次不触发
// （按交易所 exchangeInfo 读，2026-11-14 币安 BTC 是 3%；读不到时按 5%）。
// 币安：priceProtect = TRUE 时，"标记价与最新价的偏离超过 triggerProtect 时条件单不会被触发"。
func (e *Engine) protectBlocks(inst string, spec *Spec, mark decimal.Decimal) bool {
	t, ok := e.Books.Ticker(e.Venue, inst)
	if !ok || t.Last.Sign() <= 0 || mark.Sign() <= 0 {
		return false
	}
	lim := spec.TriggerProtect
	if lim.Sign() <= 0 {
		lim = decimal.RequireFromString("0.05")
	}
	return t.Last.Sub(mark).Abs().Div(mark).GreaterThan(lim)
}

// protectOf 随单止盈止损跟着开仓单的价格保护设置走。
func protectOf(a *Account, parent string) bool {
	if parent == "" {
		return false
	}
	if o, ok := a.Orders[parent]; ok {
		return o.PriceProtect
	}
	for i := len(a.History) - 1; i >= 0 && i >= len(a.History)-50; i-- {
		if a.History[i].ID == parent {
			return a.History[i].PriceProtect
		}
	}
	return false
}
