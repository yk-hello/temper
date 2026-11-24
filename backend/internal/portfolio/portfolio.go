// Package portfolio 是策略的模型组合（D11）：把 qsig 的目标权重变成订单、算净值、做风控
// （止损止盈、单日熔断、halt）。04 第 1、5 节的规则都在这里落地。
//
// 成交、手续费、资金费、强平都交给 trading 引擎（D74）：每个策略一个系统账户，
// 和用户的模拟账户同一套算法。这里只记引擎不知道的事：目标权重、止损止盈比例、暂停、熔断。
package portfolio

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"qs/internal/book"
	"qs/internal/trading"
)

var (
	InitialNAV   = decimal.NewFromInt(10_000)
	MinAdjust    = decimal.RequireFromString("0.01")
	BreakerLoss  = decimal.RequireFromString("0.05")
	bps          = decimal.NewFromInt(10_000)
	DefaultSlip  = 20
	DefaultUrgen = "normal"
)

// Exits 止损止盈（相对入场均价的比例）
type Exits struct {
	StopLoss   decimal.Decimal `json:"stop_loss_pct"`
	TakeProfit decimal.Decimal `json:"take_profit_pct"`
}

// slot 一个品种上引擎不知道的状态。
type slot struct {
	Instrument  string
	Target      decimal.Decimal
	TargetSetAt int64
	HorizonEnd  int64
	Exits       *Exits
	ClosedBy    string // stop_loss | take_profit | risk | circuit_breaker | halt
	LastIssued  int64  // 该品种已执行的最新判断的 issued_at（判断 STALE）
	Reached     bool   // 这个目标已经做到了。重发同样的目标才算"维持现状"，没做到的还要再做
	work        *Work
	ex          *Execution
	exMsg       string
}

// Target 是一条信号里对一个品种的要求（已通过格式校验）。
type Target struct {
	Instrument string
	Weight     decimal.Decimal
	RefPrice   decimal.Decimal
	Urgency    string
	MaxSlipBps int
	Exits      *Exits
	HorizonMin int
}

// Execution 是执行回报里的一项（GET /v1/messages/{id}）。
type Execution struct {
	Instrument     string          `json:"instrument"`
	PrevWeight     string          `json:"prev_weight"`
	TargetWeight   string          `json:"target_weight"`
	AchievedWeight string          `json:"achieved_weight"`
	State          string          `json:"state"` // pending | filled | partial | skipped_* | expired
	AvgPx          string          `json:"avg_price,omitempty"`
	SlippageBps    string          `json:"slippage_bps,omitempty"`
	Fee            string          `json:"fee"`
	Start          int64           `json:"start,omitempty"`
	End            int64           `json:"end,omitempty"`
	refPx          decimal.Decimal `json:"-"`
	buy            bool
}

// Change 模型组合的目标变了（信号，或者平台风控平仓）。跟随层据此换算到每个用户。
type Change struct {
	MsgID      string
	Instrument string
	Weight     decimal.Decimal
	RefPrice   decimal.Decimal
	Urgency    string
	MaxSlipBps int
	Deadline   int64
	Exits      *Exits
	ClosedBy   string // 平台风控平仓时写原因
}

type Portfolio struct {
	ID       string
	Acct     int64  // 引擎里的系统账户
	Venue    string // 模拟成交用哪个交易所的盘口
	RefVenue string // ref_price 对照的价格来源（binance_usdm_last → binance-usdm）
	Books    *book.Store
	Trader   *trading.Engine
	Now      func() time.Time

	mu          sync.Mutex
	slots       map[string]*slot
	halt        string // "" | pause | flatten_all
	haltReason  string
	breakerDay  string
	dayKey      string
	dayStartNAV decimal.Decimal
	execs       map[string][]*Execution // 信号 id → 执行回报
	navHist     []NAVPoint
	lastSnap    string

	// 持久化钩子（不设置就只在内存里）
	OnExecutions func(msgID string, execs []Execution) // 某条信号的执行回报有变化
	OnNAV        func(t int64, nav float64)            // 每分钟一个净值点
	OnState      func(Snap)                            // 组合状态有变化（每秒最多一次）
	// OnChange 目标变了之后调用（不在锁里）。
	OnChange func([]Change)
}

type NAVPoint struct {
	T   int64   `json:"t"`
	NAV float64 `json:"nav"`
}

// New 建一个模型组合。acct 是它在引擎里的系统账户，没有就开一个（初始 10,000）。
func New(id string, acct int64, venue, refVenue string, books *book.Store, trader *trading.Engine) *Portfolio {
	trader.Open(acct, InitialNAV)
	return &Portfolio{ID: id, Acct: acct, Venue: venue, RefVenue: refVenue, Books: books, Trader: trader, Now: time.Now,
		slots: map[string]*slot{}, execs: map[string][]*Execution{}}
}

func (p *Portfolio) now() int64 { return p.Now().UnixMilli() }

// RefPrice 与策略的 ref_price 同一来源的当前价（币安最新成交价），没有时退回执行交易所的中间价。
func (p *Portfolio) RefPrice(inst string) (decimal.Decimal, bool) {
	if t, ok := p.Books.Ticker(p.RefVenue, inst); ok && t.Last.Sign() > 0 {
		return t.Last, true
	}
	return p.execPrice(inst)
}

func (p *Portfolio) execPrice(inst string) (decimal.Decimal, bool) {
	if t, ok := p.Books.Ticker(p.Venue, inst); ok && t.Last.Sign() > 0 {
		return t.Last, true
	}
	if b, ok := p.Books.Book(p.Venue, inst); ok {
		return b.Mid()
	}
	return decimal.Zero, false
}

// markPrice 标记价格（止损止盈、权重用），没有时退回最新价。
func (p *Portfolio) markPrice(inst string) (decimal.Decimal, bool) {
	if t, ok := p.Books.Ticker(p.Venue, inst); ok && t.Mark.Sign() > 0 {
		return t.Mark, true
	}
	return p.execPrice(inst)
}

func (p *Portfolio) brief() *trading.Brief {
	b, err := p.Trader.Brief(p.Acct)
	if err != nil { // 账户在 New 时就开了，不会走到这里
		return &trading.Brief{Pos: map[string]trading.PosBrief{}}
	}
	return b
}

// NAV 净值 = 钱包余额 + 所有持仓的浮动盈亏（引擎按标记价算）。
func (p *Portfolio) NAV() decimal.Decimal { return p.brief().Equity }

func (p *Portfolio) weight(b *trading.Brief, inst string) decimal.Decimal {
	pos, ok := b.Pos[inst]
	if !ok || b.Equity.Sign() <= 0 {
		return decimal.Zero
	}
	m, ok := p.markPrice(inst)
	if !ok {
		return decimal.Zero
	}
	return pos.Qty.Mul(m).Div(b.Equity)
}

func (p *Portfolio) slot(inst string) *slot {
	s := p.slots[inst]
	if s == nil {
		s = &slot{Instrument: inst}
		p.slots[inst] = s
	}
	return s
}

// IsIncrease 这个目标是否属于"加仓类"（熔断和暂停时要拒绝）：绝对值变大或方向反转。
func (p *Portfolio) IsIncrease(inst string, w decimal.Decimal) bool {
	cur := p.weight(p.brief(), inst)
	if w.Abs().GreaterThan(cur.Abs().Add(MinAdjust)) {
		return true
	}
	return w.Sign() != 0 && cur.Sign() != 0 && w.Sign() != cur.Sign()
}

func (p *Portfolio) Halted() (string, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.halt, p.haltReason
}

func (p *Portfolio) Breaker() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.breakerDay != "" && p.breakerDay == p.Now().UTC().Format("2006-01-02")
}

// LastIssued 该品种已执行的最新判断时间（毫秒），用来判断 STALE。
func (p *Portfolio) LastIssued(inst string) int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	if s := p.slots[inst]; s != nil {
		return s.LastIssued
	}
	return 0
}

// Apply 执行一条已接受的信号。snapshot 模式下，没列出的品种目标置 0。
func (p *Portfolio) Apply(msgID string, issuedAt, validUntil int64, snapshot bool, targets []Target) {
	p.mu.Lock()
	if snapshot {
		listed := map[string]bool{}
		for _, t := range targets {
			listed[t.Instrument] = true
		}
		b := p.brief()
		seen := map[string]bool{}
		add := func(inst string) {
			if listed[inst] || seen[inst] {
				return
			}
			seen[inst] = true
			ref, _ := p.RefPrice(inst)
			targets = append(targets, Target{Instrument: inst, Weight: decimal.Zero, RefPrice: ref, Urgency: DefaultUrgen, MaxSlipBps: DefaultSlip})
		}
		for inst := range b.Pos {
			add(inst)
		}
		for inst, s := range p.slots {
			if !s.Target.IsZero() {
				add(inst)
			}
		}
	}
	var changes []Change
	for _, t := range targets {
		if c := p.applyOne(msgID, issuedAt, validUntil, t); c != nil {
			changes = append(changes, *c)
		}
	}
	p.mu.Unlock()
	if len(changes) > 0 && p.OnChange != nil {
		p.OnChange(changes)
	}
}

// applyOne 持锁调用。返回 nil 表示目标没变。
func (p *Portfolio) applyOne(msgID string, issuedAt, validUntil int64, t Target) *Change {
	s := p.slot(t.Instrument)
	b := p.brief()
	cur := p.weight(b, t.Instrument)
	ex := &Execution{Instrument: t.Instrument, PrevWeight: s.Target.StringFixed(4), TargetWeight: t.Weight.StringFixed(4),
		AchievedWeight: cur.StringFixed(4), Fee: "0", refPx: t.RefPrice}
	p.execs[msgID] = append(p.execs[msgID], ex)
	s.LastIssued = issuedAt
	if t.HorizonMin > 0 {
		s.HorizonEnd = issuedAt + int64(t.HorizonMin)*60_000
	}
	urg := t.Urgency
	if urg == "" {
		urg = DefaultUrgen
	}
	change := &Change{MsgID: msgID, Instrument: t.Instrument, Weight: t.Weight, RefPrice: t.RefPrice, Urgency: urg, MaxSlipBps: t.MaxSlipBps, Deadline: validUntil, Exits: t.Exits}

	// 重复发同一个目标 = 维持现状（价格涨跌不自动调回比例）。
	// 但上次没做到（挂单过期、只成交了一部分），这次要接着做（08 第 6 条）。
	if s.TargetSetAt != 0 && s.Target.Equal(t.Weight) && (s.Reached || s.work != nil) {
		ex.State = "skipped_no_change"
		p.notifyExec(msgID)
		return nil
	}
	if t.Weight.Sub(cur).Abs().LessThan(MinAdjust) {
		p.stopWork(s) // 上一张单还在做别的目标：不能让它把仓位带偏
		cur = p.weight(p.brief(), t.Instrument)
		ex.AchievedWeight = cur.StringFixed(4)
		ex.State = "skipped_min_adjust"
		p.setTarget(s, t)
		s.Reached = true
		p.notifyExec(msgID)
		return change
	}
	px, ok := p.RefPrice(t.Instrument)
	slip := decimal.NewFromInt(int64(t.MaxSlipBps))
	if !ok || t.RefPrice.Sign() <= 0 || px.Sub(t.RefPrice).Abs().Div(t.RefPrice).Mul(bps).GreaterThan(slip) {
		ex.State = "skipped_price_moved"
		p.notifyExec(msgID)
		return nil
	}
	p.setTarget(s, t)
	s.Reached = false
	change.Exits = s.Exits // 新信号没写止损止盈时沿用旧的
	// 同一品种上一张单还没结束：先撤掉，以新目标为准。撤完再算差额，已成交的部分才算得进去
	p.stopWork(s)
	b = p.brief()
	pos := b.Pos[t.Instrument].Qty
	delta := t.Weight.Mul(b.Equity).Div(px).Sub(pos)
	buy := delta.Sign() > 0
	qty, known := p.Trader.RoundQty(t.Instrument, delta.Abs()) // 交易所不接受更细的数量
	if !known || qty.IsZero() {
		ex.State = "skipped_min_adjust"
		s.Reached = true
		p.notifyExec(msgID)
		return change
	}
	limit := t.RefPrice.Mul(bps.Add(slip)).Div(bps)
	if !buy {
		limit = t.RefPrice.Mul(bps.Sub(slip)).Div(bps)
	}
	reduce := !pos.IsZero() && (pos.Sign() > 0) != buy && qty.LessThanOrEqual(pos.Abs())
	ex.State, ex.buy = "pending", buy
	ex.Start = p.now()
	w := &Work{Trader: p.Trader, Acct: p.Acct, Inst: t.Instrument, Buy: buy, Qty: qty, Limit: limit, Urgency: urg, Reduce: reduce,
		Start: ex.Start, Deadline: validUntil, Source: "ai", Tag: msgID}
	s.work, s.ex, s.exMsg = w, ex, msgID
	w.Begin()
	if w.Done {
		p.finishWork(s)
	}
	return change
}

func (p *Portfolio) setTarget(s *slot, t Target) {
	s.Target = t.Weight
	s.TargetSetAt = p.now()
	s.ClosedBy = ""
	if t.Exits != nil {
		s.Exits = t.Exits
	}
}

// stopWork 撤掉这个品种还在执行的调仓。
func (p *Portfolio) stopWork(s *slot) {
	if s.work == nil {
		return
	}
	s.work.Stop()
	p.finishWork(s)
}

// finishWork 一笔调仓结束：把结果写进执行回报。
func (p *Portfolio) finishWork(s *slot) {
	w, ex := s.work, s.ex
	s.work, s.ex = nil, nil
	ex.State = w.State()
	s.Reached = ex.State == "filled"
	// 差额比交易所的最小下单金额还小：下不了单，和差额太小一样处理，重发同样目标不再试
	if w.Filled.IsZero() && (w.Reject == "MIN_NOTIONAL" || w.Reject == "QTY_TOO_SMALL") {
		ex.State, s.Reached = "skipped_min_adjust", true
	}
	ex.End = p.now()
	ex.Fee = w.Fee.StringFixed(6)
	ex.AchievedWeight = p.weight(p.brief(), s.Instrument).StringFixed(4)
	if w.Filled.Sign() > 0 {
		avg := w.AvgPx()
		ex.AvgPx = avg.String()
		// 滑点：相对 ref_price，对我们不利为正
		sl := avg.Sub(ex.refPx).Div(ex.refPx).Mul(bps)
		if !ex.buy {
			sl = sl.Neg()
		}
		ex.SlippageBps = sl.StringFixed(2)
	}
	p.notifyExec(s.exMsg)
}

func (p *Portfolio) notifyExec(msgID string) {
	if p.OnExecutions == nil {
		return
	}
	list := make([]Execution, 0, len(p.execs[msgID]))
	for _, e := range p.execs[msgID] {
		list = append(list, *e)
	}
	go p.OnExecutions(msgID, list)
}

// Executions 某条信号的执行回报。
func (p *Portfolio) Executions(msgID string) []Execution {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []Execution
	for _, e := range p.execs[msgID] {
		out = append(out, *e)
	}
	return out
}

// Halt 执行 halt 消息。之后只能人工恢复（Resume）。
func (p *Portfolio) Halt(action, reason string) {
	p.mu.Lock()
	p.halt, p.haltReason = action, reason
	var changes []Change
	if action == "flatten_all" {
		insts := make([]string, 0)
		for inst := range p.brief().Pos {
			insts = append(insts, inst)
		}
		sort.Strings(insts)
		for _, inst := range insts {
			changes = append(changes, p.closeLocked(inst, "halt"))
		}
	}
	p.mu.Unlock()
	if len(changes) > 0 && p.OnChange != nil {
		p.OnChange(changes)
	}
}

// Resume 由平台方人工恢复（不允许心跳自动恢复）。
func (p *Portfolio) Resume() {
	p.mu.Lock()
	p.halt, p.haltReason = "", ""
	p.mu.Unlock()
}

// closeLocked 风控平仓：立即吃单，目标置 0，记录 closed_by。
func (p *Portfolio) closeLocked(inst, reason string) Change {
	s := p.slot(inst)
	p.stopWork(s)
	s.Target, s.TargetSetAt, s.ClosedBy, s.Exits, s.Reached = decimal.Zero, p.now(), reason, nil, true
	ref, _ := p.RefPrice(inst)
	// 盘口不够、行情暂停时平不完：下一秒 Watch 接着平
	_, _ = p.Trader.CloseAs(p.Acct, inst, "risk", reason)
	return Change{Instrument: inst, Weight: decimal.Zero, RefPrice: ref, Urgency: "immediate", ClosedBy: reason}
}

// Watch 每秒调用：推进挂单、止损止盈（按标记价格）、单日熔断、净值记录。
func (p *Portfolio) Watch() {
	now := p.Now()
	nowMs := now.UnixMilli()
	day := now.UTC().Format("2006-01-02")
	p.mu.Lock()
	for _, s := range p.slots {
		if s.work != nil {
			s.work.Step(nowMs)
			if s.work.Done {
				p.finishWork(s)
			}
		}
	}
	b := p.brief()
	if p.dayKey != day {
		p.dayKey, p.dayStartNAV = day, b.Equity
	}
	if p.breakerDay != day && p.dayStartNAV.Sign() > 0 && b.Equity.LessThan(p.dayStartNAV.Mul(decimal.NewFromInt(1).Sub(BreakerLoss))) {
		p.breakerDay = day
	}
	var changes []Change
	insts := make([]string, 0, len(b.Pos))
	for inst := range b.Pos {
		insts = append(insts, inst)
	}
	sort.Strings(insts)
	for _, inst := range insts {
		pos := b.Pos[inst]
		s := p.slot(inst)
		if s.work != nil {
			continue
		}
		// 风控平仓上一次没平完（盘口不够、行情暂停）：接着平
		if s.ClosedBy != "" && s.Target.IsZero() {
			_, _ = p.Trader.CloseAs(p.Acct, inst, "risk", s.ClosedBy)
			continue
		}
		if s.Exits == nil {
			continue
		}
		m, ok := p.markPrice(inst)
		if !ok || pos.Entry.Sign() <= 0 {
			continue
		}
		chg := m.Sub(pos.Entry).Div(pos.Entry)
		if pos.Qty.Sign() < 0 {
			chg = chg.Neg()
		}
		if sl := s.Exits.StopLoss; sl.Sign() > 0 && chg.LessThanOrEqual(sl.Neg()) {
			changes = append(changes, p.closeLocked(inst, "stop_loss"))
		} else if tp := s.Exits.TakeProfit; tp.Sign() > 0 && chg.GreaterThanOrEqual(tp) {
			changes = append(changes, p.closeLocked(inst, "take_profit"))
		}
	}
	if len(p.navHist) == 0 || nowMs-p.navHist[len(p.navHist)-1].T >= 60_000 {
		f, _ := b.Equity.Float64()
		p.navHist = append(p.navHist, NAVPoint{T: nowMs, NAV: f})
		if p.OnNAV != nil {
			go p.OnNAV(nowMs, f)
		}
	}
	if p.OnState != nil {
		snap := p.snapLocked()
		if raw, _ := json.Marshal(snap); string(raw) != p.lastSnap {
			p.lastSnap = string(raw)
			go p.OnState(snap)
		}
	}
	p.mu.Unlock()
	if len(changes) > 0 && p.OnChange != nil {
		p.OnChange(changes)
	}
}

// Targets 现在每个品种的目标（跟随恢复时补齐用）。
func (p *Portfolio) Targets() []Change {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Change, 0, len(p.slots))
	for inst, s := range p.slots {
		if s.TargetSetAt == 0 {
			continue
		}
		ref, ok := p.RefPrice(inst)
		if !ok {
			continue
		}
		out = append(out, Change{Instrument: inst, Weight: s.Target, RefPrice: ref, Urgency: "immediate", MaxSlipBps: DefaultSlip, Exits: s.Exits, ClosedBy: s.ClosedBy})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Instrument < out[j].Instrument })
	return out
}

// State 组合状态（GET /v1/portfolio）。
type State struct {
	StrategyID     string          `json:"strategy_id"`
	NAV            string          `json:"nav"`
	Cash           string          `json:"cash"`
	CircuitBreaker bool            `json:"circuit_breaker"`
	Halted         string          `json:"halted,omitempty"`
	Positions      []PositionState `json:"positions"`
	Time           int64           `json:"time"`
}

type PositionState struct {
	Instrument   string `json:"instrument"`
	TargetWeight string `json:"target_weight"`
	ActualWeight string `json:"actual_weight"`
	Qty          string `json:"qty"`
	EntryPrice   string `json:"entry_price"`
	MarkPrice    string `json:"mark_price"`
	Unrealized   string `json:"unrealized_pnl"`
	TargetSetAt  string `json:"target_set_at,omitempty"`
	Expired      bool   `json:"horizon_expired"`
	ClosedBy     string `json:"closed_by,omitempty"`
}

func (p *Portfolio) State() State {
	p.mu.Lock()
	defer p.mu.Unlock()
	b := p.brief()
	now := p.now()
	st := State{StrategyID: p.ID, NAV: b.Equity.StringFixed(4), Cash: b.Wallet.StringFixed(4),
		CircuitBreaker: p.breakerDay == p.Now().UTC().Format("2006-01-02"), Halted: p.halt, Time: now, Positions: []PositionState{}}
	seen := map[string]bool{}
	for inst := range b.Pos {
		seen[inst] = true
	}
	for inst, s := range p.slots {
		if !s.Target.IsZero() || s.ClosedBy != "" {
			seen[inst] = true
		}
	}
	for inst := range seen {
		s := p.slots[inst]
		if s == nil {
			s = &slot{Instrument: inst}
		}
		pos := b.Pos[inst]
		m, _ := p.markPrice(inst)
		ps := PositionState{Instrument: inst, TargetWeight: s.Target.StringFixed(4), ActualWeight: p.weight(b, inst).StringFixed(4),
			Qty: pos.Qty.String(), EntryPrice: pos.Entry.String(), MarkPrice: m.String(), Unrealized: m.Sub(pos.Entry).Mul(pos.Qty).StringFixed(4),
			Expired: s.HorizonEnd > 0 && now > s.HorizonEnd && !pos.Qty.IsZero(), ClosedBy: s.ClosedBy}
		if pos.Qty.IsZero() {
			ps.EntryPrice, ps.Unrealized = "0", "0"
		}
		if s.TargetSetAt > 0 {
			ps.TargetSetAt = time.UnixMilli(s.TargetSetAt).UTC().Format("2006-01-02T15:04:05.000Z")
		}
		st.Positions = append(st.Positions, ps)
	}
	sort.Slice(st.Positions, func(i, j int) bool { return st.Positions[i].Instrument < st.Positions[j].Instrument })
	return st
}

// History 净值曲线（每分钟一个点）。
func (p *Portfolio) History() []NAVPoint {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]NAVPoint(nil), p.navHist...)
}

// Snap 是组合里引擎不知道的状态，存进数据库，重启后恢复。
type Snap struct {
	Targets     map[string]SnapPos `json:"targets"`
	Halt        string             `json:"halt,omitempty"`
	HaltReason  string             `json:"halt_reason,omitempty"`
	BreakerDay  string             `json:"breaker_day,omitempty"`
	DayKey      string             `json:"day_key,omitempty"`
	DayStartNAV decimal.Decimal    `json:"day_start_nav"`
}

type SnapPos struct {
	Target      decimal.Decimal `json:"target"`
	TargetSetAt int64           `json:"target_set_at"`
	HorizonEnd  int64           `json:"horizon_end,omitempty"`
	Exits       *Exits          `json:"exits,omitempty"`
	ClosedBy    string          `json:"closed_by,omitempty"`
	LastIssued  int64           `json:"last_issued,omitempty"`
	Reached     bool            `json:"reached,omitempty"`
}

func (p *Portfolio) snapLocked() Snap {
	s := Snap{Targets: map[string]SnapPos{}, Halt: p.halt, HaltReason: p.haltReason, BreakerDay: p.breakerDay, DayKey: p.dayKey, DayStartNAV: p.dayStartNAV}
	for inst, x := range p.slots {
		s.Targets[inst] = SnapPos{Target: x.Target, TargetSetAt: x.TargetSetAt, HorizonEnd: x.HorizonEnd, Exits: x.Exits, ClosedBy: x.ClosedBy, LastIssued: x.LastIssued, Reached: x.Reached}
	}
	return s
}

// Restore 重启恢复：持仓和钱在引擎的事件里，这里套上保存的目标、执行回报、净值曲线。
// 必须在引擎恢复之后、开始接收信号之前调用。
//
// 重启时还没执行完的调仓不接着做：撤掉挂单，如实标"中断"。目标留着，
// 同样的目标再发一次会接着做（Reached 是 false）。
func (p *Portfolio) Restore(snap *Snap, execs map[string][]Execution, nav []NAVPoint) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if n, err := p.Trader.CancelAll(p.Acct, ""); err == nil && n > 0 {
		for _, s := range p.slots {
			s.Reached = false
		}
	}
	if snap != nil {
		p.halt, p.haltReason, p.breakerDay, p.dayKey, p.dayStartNAV = snap.Halt, snap.HaltReason, snap.BreakerDay, snap.DayKey, snap.DayStartNAV
		for inst, sp := range snap.Targets {
			s := p.slot(inst)
			s.Target, s.TargetSetAt, s.HorizonEnd, s.Exits, s.ClosedBy, s.LastIssued, s.Reached = sp.Target, sp.TargetSetAt, sp.HorizonEnd, sp.Exits, sp.ClosedBy, sp.LastIssued, sp.Reached
		}
		raw, _ := json.Marshal(p.snapLocked())
		p.lastSnap = string(raw)
	}
	for id, list := range execs {
		for i := range list {
			e := list[i]
			if e.State == "pending" {
				e.State = "interrupted" // 重启时还没执行完：如实标出
			}
			p.execs[id] = append(p.execs[id], &e)
		}
	}
	p.navHist = append(p.navHist, nav...)
}

// StoreKey 这个组合在 portfolio_state、nav_points 表里的键。
// 按账户算，不用策略 id：搬引擎之前的旧记录还留在表里，新战绩从零开始（D74、12 的 3.1）。
func StoreKey(acct int64) string { return fmt.Sprintf("acct:%d", acct) }
