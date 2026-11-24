package trading

import (
	"sort"

	"github.com/shopspring/decimal"
)

// 给界面看的账户全貌（验收 2.13、2.14）。所有金额按当前标记价实时计算。

type PosView struct {
	Instrument string          `json:"instrument"`
	Side       string          `json:"side"` // long | short
	Qty        decimal.Decimal `json:"qty"`
	Entry      decimal.Decimal `json:"entry"`
	Mark       decimal.Decimal `json:"mark"`
	Notional   decimal.Decimal `json:"notional"`
	UPnL       decimal.Decimal `json:"upnl"`
	ROE        decimal.Decimal `json:"roe"`    // 收益率 = 未实现盈亏 ÷ 初始保证金
	Margin     decimal.Decimal `json:"margin"` // 逐仓：逐仓保证金；全仓：名义价值 ÷ 杠杆
	Maint      decimal.Decimal `json:"maint"`
	LiqPx      decimal.Decimal `json:"liq_px"`
	Mode       string          `json:"mode"`
	Leverage   int             `json:"leverage"`
	TP         decimal.Decimal `json:"tp"`
	SL         decimal.Decimal `json:"sl"`
	Realized   decimal.Decimal `json:"realized"`
	Opened     int64           `json:"opened"`
	// ADL 自动减仓排队 0–4（越大越先被减）。交易所账户读币安的；模拟盘没有别人排队，按币安的排序规则估（ADLEst = true）
	ADL    int  `json:"adl"`
	ADLEst bool `json:"adl_est,omitempty"`
}

// ADLEstimate 按币安的排序规则估：盈利的仓位按"收益率 × 有效杠杆"排，越高越靠前；亏损的排在最后。
// 模拟盘没有别的交易者，只能用固定的分段：0.2、0.6、1.5（比如 10 倍杠杆涨 2%，收益率 20%，乘以 10 是 2，排在最前一档）。
func ADLEstimate(roe, effLev decimal.Decimal) int {
	if roe.Sign() <= 0 {
		return 0
	}
	s, _ := roe.Mul(effLev).Float64()
	switch {
	case s < 0.2:
		return 1
	case s < 0.6:
		return 2
	case s < 1.5:
		return 3
	}
	return 4
}

type AccountView struct {
	ID             int64               `json:"id"`
	Archived       bool                `json:"archived"`
	Initial        decimal.Decimal     `json:"initial"`
	Wallet         decimal.Decimal     `json:"wallet"`
	Equity         decimal.Decimal     `json:"equity"`
	UPnL           decimal.Decimal     `json:"upnl"`
	Realized       decimal.Decimal     `json:"realized"`
	Fees           decimal.Decimal     `json:"fees"`
	Funding        decimal.Decimal     `json:"funding"`
	LiqFees        decimal.Decimal     `json:"liq_fees"`
	Covered        decimal.Decimal     `json:"covered"` // 穿仓时系统补的钱（累计）。权益里含着它，真实账户里这是你欠交易所的
	Available      decimal.Decimal     `json:"available"`
	Transferable   decimal.Decimal     `json:"transferable"` // 最多能转出：不算未实现盈利
	PositionMargin decimal.Decimal     `json:"position_margin"`
	OrderMargin    decimal.Decimal     `json:"order_margin"`
	MaintMargin    decimal.Decimal     `json:"maint_margin"`
	MarginRatio    decimal.Decimal     `json:"margin_ratio"` // 全仓维持保证金 ÷ 全仓保证金余额；≥ 1 强平
	Positions      []PosView           `json:"positions"`
	Orders         []*Order            `json:"orders"`
	Settings       map[string]*Setting `json:"settings"`
	Notices        []Notice            `json:"notices"`
	Seq            int64               `json:"seq"`        // 引擎的事件序号：变了说明有新动作
	TakerFee       decimal.Decimal     `json:"taker_fee"`  // 这个账户现在的吃单费率（D103）
	MakerFee       decimal.Decimal     `json:"maker_fee"`  // 挂单费率
	CustomFee      bool                `json:"custom_fee"` // 自己设过（没设过是币安普通用户的费率）
}

func (e *Engine) View(acct int64) (*AccountView, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, ok := e.accounts[acct]
	if !ok {
		return nil, rej("NOT_FOUND")
	}
	mk := e.marks()
	m := e.compute(a, mk)
	v := &AccountView{ID: a.ID, Archived: a.Archived, Initial: a.Initial, Wallet: a.Wallet.Round(8), Equity: m.equity.Round(8), UPnL: m.upnl.Round(8),
		Realized: a.Realized, Fees: a.Fees, Funding: a.Funding, LiqFees: a.LiqFees, Covered: a.Covered, Available: decimal.Max(zero, m.available).Round(8), Transferable: transferable(m).RoundFloor(8),
		PositionMargin: m.posIM.Round(8), OrderMargin: m.orderIM.Round(8), MaintMargin: m.crossMM.Add(m.isoMM).Round(8), MarginRatio: m.marginRate.Round(6),
		Settings: map[string]*Setting{}, Seq: a.LastSeq, CustomFee: a.Taker != nil}
	v.TakerFee, v.MakerFee = e.rates(a)
	for k, s := range a.Settings {
		c := *s
		v.Settings[k] = &c
	}
	tpsl := map[string][2]decimal.Decimal{}
	for _, o := range a.Orders {
		if o.ClosePos && o.Status == StNew {
			x := tpsl[o.Instrument]
			if o.Type == TakeProfit {
				x[0] = o.TriggerPx
			} else {
				x[1] = o.TriggerPx
			}
			tpsl[o.Instrument] = x
		}
	}
	for _, inst := range sortedInsts(a) {
		p := a.Pos[inst]
		if p.Qty.IsZero() {
			continue
		}
		spec, _ := e.Specs.Get(inst)
		mark, okm := mk(inst)
		if !okm {
			mark = p.Entry
		}
		set := a.setting(inst)
		n := p.Qty.Abs().Mul(mark)
		u := upnl(p, mark)
		margin := n.Div(lev(set.Leverage))
		im := p.Qty.Abs().Mul(p.Entry).Div(lev(set.Leverage))
		if set.Mode == "isolated" {
			margin, im = p.IsoMargin, p.IsoMargin
		}
		pv := PosView{Instrument: inst, Side: "long", Qty: p.Qty.Abs(), Entry: p.Entry.Round(8), Mark: mark, Notional: n.Round(4), UPnL: u.Round(8),
			Margin: margin.Round(8), Mode: set.Mode, Leverage: set.Leverage, TP: tpsl[inst][0], SL: tpsl[inst][1], Realized: p.Realized, Opened: p.Opened}
		if p.Qty.Sign() < 0 {
			pv.Side = "short"
		}
		if im.Sign() > 0 {
			pv.ROE = u.Div(im).Round(6)
		}
		if margin.Sign() > 0 {
			pv.ADL, pv.ADLEst = ADLEstimate(pv.ROE, n.Div(margin)), true
		}
		if spec != nil {
			pv.Maint = spec.MaintMargin(n).Round(8)
			if lp := e.positionLiq(a, inst, mk); lp.Sign() > 0 {
				pv.LiqPx = lp.Round(int32(-spec.Tick.Exponent()))
			}
		}
		v.Positions = append(v.Positions, pv)
	}
	for _, o := range sortedOrders(a) {
		c := *o
		if q, ok := e.queue[o.ID]; ok && o.Type == Limit {
			c.QueueAhead = q
		}
		v.Orders = append(v.Orders, &c)
	}
	n := len(a.Notices)
	if n > 20 {
		v.Notices = append(v.Notices, a.Notices[n-20:]...)
	} else {
		v.Notices = append(v.Notices, a.Notices...)
	}
	if v.Positions == nil {
		v.Positions = []PosView{}
	}
	if v.Orders == nil {
		v.Orders = []*Order{}
	}
	if v.Notices == nil {
		v.Notices = []Notice{}
	}
	return v, nil
}

type Page struct {
	Total int `json:"total"`
	Items any `json:"items"`
}

// History 历史委托、成交明细、资金费记录，最新的在前，分页（验收 2.13）。
func (e *Engine) History(acct int64, kind string, page, size int) (*Page, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, ok := e.accounts[acct]
	if !ok {
		return nil, rej("NOT_FOUND")
	}
	if size <= 0 || size > 200 {
		size = 50
	}
	if page < 1 {
		page = 1
	}
	slice := func(n int) (int, int) {
		hi := n - (page-1)*size
		lo := hi - size
		if hi < 0 {
			hi = 0
		}
		if lo < 0 {
			lo = 0
		}
		return lo, hi
	}
	switch kind {
	case "orders":
		lo, hi := slice(len(a.History))
		out := make([]*Order, 0, hi-lo)
		for i := hi - 1; i >= lo; i-- {
			c := *a.History[i]
			out = append(out, &c)
		}
		return &Page{Total: len(a.History), Items: out}, nil
	case "fills":
		lo, hi := slice(len(a.Fills))
		out := make([]Fill, 0, hi-lo)
		for i := hi - 1; i >= lo; i-- {
			out = append(out, a.Fills[i])
		}
		return &Page{Total: len(a.Fills), Items: out}, nil
	case "funding":
		lo, hi := slice(len(a.Fundings))
		out := make([]FundingRec, 0, hi-lo)
		for i := hi - 1; i >= lo; i-- {
			out = append(out, a.Fundings[i])
		}
		return &Page{Total: len(a.Fundings), Items: out}, nil
	}
	return nil, rej("BAD_REQUEST")
}

// OrderFills 某张委托的逐笔成交（时间顺序）。内存里只留最近 5000 笔成交，更早的在 trading_events 表里。
func (e *Engine) OrderFills(acct int64, id string) ([]Fill, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, ok := e.accounts[acct]
	if !ok {
		return nil, rej("NOT_FOUND")
	}
	out := []Fill{}
	for _, f := range a.Fills {
		if f.OrderID == id {
			out = append(out, f)
		}
	}
	return out, nil
}

// SpecList 所有品种规格（前端下单区用：价格、数量单位、最小金额、杠杆上限）。
func (e *Engine) SpecList() []*Spec {
	all := e.Specs.All()
	out := make([]*Spec, 0, len(all))
	for _, s := range all {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Instrument < out[j].Instrument })
	return out
}

// state 账户完整状态（测试比较重启前后是否一致用）。
func (e *Engine) state(acct int64) map[string]any {
	e.mu.Lock()
	defer e.mu.Unlock()
	a := e.accounts[acct]
	if a == nil {
		return nil
	}
	return map[string]any{"account": a, "history": a.History, "fills": a.Fills, "funding": a.Fundings, "notices": a.Notices}
}
