package follow

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"qs/internal/auth"
	"qs/internal/portfolio"
	"qs/internal/trading"
)

var (
	bps      = decimal.NewFromInt(10_000)
	riskSlip = decimal.NewFromInt(200) // 减仓时价格已经偏离：放宽到 2% 也要减掉
	headroom = decimal.RequireFromString("0.99")
)

// Accounts 跟随要用到的账户操作（auth.Service 实现了它）。
type Accounts interface {
	OwnsPaper(ctx context.Context, userID, accountID int64) bool
	PaperAccounts(ctx context.Context, userID int64) ([]auth.PaperAccount, error)
	NewPaper(ctx context.Context, u *auth.User, name string) (*auth.PaperAccount, error)
}

type Manager struct {
	Trader    trading.Executor // 模拟盘引擎，或 trading.Mux（交易所账户也能跟，18 第 5.2 条）
	Accounts  Accounts
	Portfolio func(strategy string) *portfolio.Portfolio // 没有这个策略时返回 nil
	Persist   func([]Event) error
	Log       *slog.Logger
	Now       func() time.Time
	Shuffle   func(n int, swap func(i, j int)) // 多个用户随机排序（D15）
	// LiveFills 交易所账户上某个来源一段时间里的成交合计（live.Desks.SourceFills）。重启时补"中断"那一笔的真实结果用；为空时只标中断
	LiveFills func(acct int64, inst, side, source string, from, to int64) (qty, avg, fee decimal.Decimal)
	// TopUp 给模拟账户加钱（D103 模拟资金不限额）：分配的钱比模拟账户可用的多时，差的部分自动加上（记成加钱，不算盈亏）。
	// 交易所账户不加：真钱不够就是不够。为空时不加，照旧拒
	TopUp func(acct int64, amount decimal.Decimal) error
	// LiveAcct 交易所账户（18 第 5.2 条）：是不是这个人的、确认过"这是真钱"没有。没接实盘时为空，只能跟到模拟账户
	LiveAcct func(ctx context.Context, user, acct int64) (owned, armed bool)
	// TradePassword 在交易所账户上开启或改参数要二级密码
	TradePassword func(u *auth.User, pw string) error
	// LiveStrategy 这个人能不能用这个策略驱动这个交易所账户（D99），返回拒绝的代码，空 = 可以。只能是他自己的策略：
	// 别人的策略只能跟到模拟账户——别人的信号直接操作自己的真钱账户就是带单，不做（D90）。没接上时一律不行
	LiveStrategy func(ctx context.Context, u *auth.User, acct int64, strategy string) string

	mu      sync.Mutex
	seq     int64
	nextID  int64
	nextEx  int64
	follows map[int64]*Follow
	recs    map[int64][]*Exec // follow → 执行记录（按时间）
	works   map[workKey]*live
	fillSeq map[int64]int64 // follow → 看过的最后一笔成交（不用存：重启期间没有成交）

	qmu   sync.Mutex
	queue []batch
	wake  chan struct{}

	pmu     sync.Mutex
	wmu     sync.Mutex
	pending []Event

	// 交易所账户的网络调用（下单、查单、撤单、平仓、设杠杆、挂止盈止损）放进每个账户自己的队列里做，不拿 mu（18 第 9.1 条）。
	// 一个交易所卡住（一次请求最长等 15 秒）只拖慢它自己，别的跟随和页面照常。模拟账户在内存里，照旧当场做。
	lmu     sync.Mutex
	lanes   map[int64]*laneQ
	closing map[int64]bool // 交易所账户：平仓已经排进队列的跟随（mu 保护）
}

type laneQ struct {
	jobs    []func()
	running bool
}

type workKey struct {
	follow int64
	inst   string
}

type live struct {
	w     *portfolio.Work
	ex    *Exec
	exits *portfolio.Exits
	busy  bool // 交易所账户：这张单在队列里有活还没做完（下单或推进），Tick 不再排新的（mu 保护）
}

// remote 交易所账户：调用要走网络。
func (m *Manager) remote(acct int64) bool { return acct >= trading.LiveBase }

// enqueue 排进这个账户的队列：先进先出，一个账户同一时间只做一件。不会阻塞（持 mu 时也能调）。
func (m *Manager) enqueue(acct int64, job func()) {
	m.lmu.Lock()
	if m.lanes == nil {
		m.lanes = map[int64]*laneQ{}
	}
	q := m.lanes[acct]
	if q == nil {
		q = &laneQ{}
		m.lanes[acct] = q
	}
	q.jobs = append(q.jobs, job)
	if q.running {
		m.lmu.Unlock()
		return
	}
	q.running = true
	m.lmu.Unlock()
	go func() {
		for {
			m.lmu.Lock()
			if len(q.jobs) == 0 {
				q.running = false
				m.lmu.Unlock()
				return
			}
			j := q.jobs[0]
			q.jobs = q.jobs[1:]
			m.lmu.Unlock()
			j()
		}
	}()
}

// Idle 交易所账户的队列都做完了（测试用）。
func (m *Manager) Idle() bool {
	m.lmu.Lock()
	defer m.lmu.Unlock()
	for _, q := range m.lanes {
		if q.running || len(q.jobs) > 0 {
			return false
		}
	}
	return true
}

type batch struct {
	strategy string
	changes  []portfolio.Change
}

func New(trader trading.Executor, accounts Accounts, pf func(string) *portfolio.Portfolio, log *slog.Logger) *Manager {
	return &Manager{Trader: trader, Accounts: accounts, Portfolio: pf, Log: log, Now: time.Now, Shuffle: rand.Shuffle,
		follows: map[int64]*Follow{}, recs: map[int64][]*Exec{}, works: map[workKey]*live{}, fillSeq: map[int64]int64{}, wake: make(chan struct{}, 1)}
}

func (m *Manager) now() int64 { return m.Now().UnixMilli() }

// ---------- 事件 ----------

func (m *Manager) emit(ev Event) {
	m.seq++
	ev.Seq = m.seq
	if ev.Time == 0 {
		ev.Time = m.now()
	}
	m.apply(ev)
	if m.Persist != nil {
		m.pmu.Lock()
		m.pending = append(m.pending, ev)
		m.pmu.Unlock()
	}
}

// apply 把事件作用到状态上。同样的事件序列重放出同样的状态。
func (m *Manager) apply(ev Event) {
	if ev.Kind == "start" {
		s := ev.Start
		m.follows[ev.Follow] = &Follow{ID: ev.Follow, User: s.User, Account: s.Account, Strategy: s.Strategy, Params: s.Params, Status: StActive,
			StartEquity: s.StartEquity, Started: ev.Time, Insts: map[string]bool{}, Manual: map[string]int64{}}
		if ev.Follow >= m.nextID {
			m.nextID = ev.Follow
		}
		return
	}
	f := m.follows[ev.Follow]
	if f == nil {
		return
	}
	switch ev.Kind {
	case "params":
		f.Params = *ev.Params
	case "pause":
		f.Status = StPaused
	case "resume":
		f.Status = StActive
	case "stop":
		f.Reason, f.Stopped = ev.Reason, ev.Time
		f.Status = StStopped
		if ev.Close {
			f.Status = StClosing
		}
	case "closed":
		f.Status = StStopped
	case "manual":
		f.Manual[ev.Inst] = ev.Time
	case "manual_end":
		delete(f.Manual, ev.Inst)
	case "exec":
		x := *ev.Exec
		if x.ID > m.nextEx {
			m.nextEx = x.ID
		}
		if x.Side != "" && !x.Filled.IsZero() {
			f.Insts[x.Instrument] = true
		}
		list := m.recs[ev.Follow]
		for i := len(list) - 1; i >= 0 && i >= len(list)-50; i-- {
			if list[i].ID == x.ID {
				list[i] = &x
				return
			}
		}
		m.recs[ev.Follow] = append(list, &x)
	}
}

// Restore 重启恢复：重放事件。没做完的执行标"中断"，挂着的跟随单撤掉（和模型组合一样）。
// 必须在引擎恢复之后调用。
func (m *Manager) Restore(evs []Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, ev := range evs {
		m.seq = max(m.seq, ev.Seq)
		m.apply(ev)
	}
	restart := m.now()
	for id, list := range m.recs {
		for _, x := range list {
			if x.State == "pending" {
				c := *x
				c.State, c.End = "interrupted", restart
				m.emit(Event{Kind: "exec", Follow: id, Exec: &c})
				// 交易所账户：单子在交易所那边可能已经成交了（停机期间也会成交），按成交记录补上真实结果
				if f := m.follows[id]; f != nil && m.LiveFills != nil && m.remote(f.Account) && c.Side != "" {
					acct, fid, ex := f.Account, id, c
					m.enqueue(acct, func() {
						q, a, fee := m.LiveFills(acct, ex.Instrument, ex.Side, "follow", ex.Time, restart)
						if q.Sign() <= 0 {
							return
						}
						ex.Filled, ex.AvgPx, ex.Fee, ex.Note = q, a, fee, "restart"
						ex.State = "filled"
						if q.LessThan(ex.Qty) {
							ex.State = "partial"
						}
						m.mu.Lock()
						m.emit(Event{Kind: "exec", Follow: fid, Exec: &ex})
						m.mu.Unlock()
					})
				}
			}
		}
	}
	for _, f := range m.follows {
		if f.Status != StStopped {
			if acct := f.Account; m.remote(acct) {
				m.enqueue(acct, func() { _, _ = m.Trader.CancelSource(acct, "follow", "RESTART") })
			} else {
				_, _ = m.Trader.CancelSource(acct, "follow", "RESTART")
			}
			m.fillSeq[f.ID] = m.Trader.FillSeq(f.Account)
		}
	}
}

// ---------- 手动优先（D80）：手动权限最高，不拦，事后提示 ----------

// record 记一条执行记录（没有交易的那种）。
func (m *Manager) record(f *Follow, inst, state string) {
	m.nextEx++
	now := m.now()
	m.emit(Event{Kind: "exec", Follow: f.ID, Exec: &Exec{ID: m.nextEx, Follow: f.ID, Instrument: inst, Time: now, End: now, State: state}})
}

// takeover 这个币转为手动管理：AI 正在执行的单撤掉，之后不再调整。
func (m *Manager) takeover(f *Follow, inst string) {
	m.stopWork(f, inst)
	m.emit(Event{Kind: "manual", Follow: f.ID, Inst: inst})
	m.record(f, inst, "manual")
	_ = m.Trader.Note(f.Account, "takeover", inst, "")
}

// syncManual 对账：手动单有成交的币转为手动管理；手动管理的币平仓后还给 AI。持锁调用。
func (m *Manager) syncManual(f *Follow) {
	filled, last := m.Trader.ManualFills(f.Account, m.fillSeq[f.ID])
	m.fillSeq[f.ID] = last
	b, err := m.Trader.Brief(f.Account)
	if err != nil {
		return
	}
	for inst := range filled {
		if f.Manual[inst] != 0 {
			continue
		}
		if _, held := b.Pos[inst]; held {
			m.takeover(f, inst)
		} else if f.Insts[inst] { // 手动把 AI 的仓位平掉了
			m.stopWork(f, inst)
			m.record(f, inst, "manual_end")
			_ = m.Trader.Note(f.Account, "takeover_end", inst, "")
		}
	}
	for inst := range f.Manual {
		if _, held := b.Pos[inst]; !held {
			m.emit(Event{Kind: "manual_end", Follow: f.ID, Inst: inst})
			m.record(f, inst, "manual_end")
			_ = m.Trader.Note(f.Account, "takeover_end", inst, "")
		}
	}
}

// ManualAction 用户刚做了手动操作：马上对账，提示不用等下一秒。inst 不为空表示手动改了这个品种的止盈止损。
func (m *Manager) ManualAction(acct int64, inst string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, f := range m.follows {
		if f.Account != acct || !f.running() {
			continue
		}
		m.syncManual(f)
		if inst != "" && f.Manual[inst] == 0 {
			if b, err := m.Trader.Brief(acct); err == nil {
				if _, held := b.Pos[inst]; held {
					m.takeover(f, inst)
				}
			}
		}
		return
	}
}

// AIManaged 这个币现在是 AI 在管吗（纪律锁的连亏不算 AI 的交易）。
func (m *Manager) AIManaged(acct int64, inst string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, f := range m.follows {
		if f.Account == acct && f.running() {
			return f.Insts[inst] && f.Manual[inst] == 0
		}
	}
	return false
}

// ManualInsts 这个账户手动管理的品种。没有在跟随时返回 nil，所有仓位都是手动的。
func (m *Manager) ManualInsts(acct int64) (manual map[string]bool, following bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, f := range m.follows {
		if f.Account == acct && f.running() {
			out := make(map[string]bool, len(f.Manual))
			for k := range f.Manual {
				out[k] = true
			}
			return out, true
		}
	}
	return nil, false
}

// Flush 把事件写进数据库。返回还没写成的条数。
func (m *Manager) Flush() int {
	if m.Persist == nil {
		return 0
	}
	m.wmu.Lock()
	defer m.wmu.Unlock()
	m.pmu.Lock()
	b := m.pending
	m.pending = nil
	m.pmu.Unlock()
	if len(b) == 0 {
		return 0
	}
	if err := m.Persist(b); err != nil {
		m.Log.Error("follow: 写库失败，稍后重试", "count", len(b), "err", err)
		m.pmu.Lock()
		m.pending = append(b, m.pending...)
		n := len(m.pending)
		m.pmu.Unlock()
		return n
	}
	return 0
}

// Run 处理目标变化，每秒推进挂单、检查最大亏损、写库。
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			m.Flush()
			return
		case <-m.wake:
			m.drain()
		case <-t.C:
			m.drain()
			m.Tick()
		}
		m.Flush()
	}
}

// Notify 模型组合的目标变了（在模型组合的调用路径上，不能阻塞）。
func (m *Manager) Notify(strategy string, changes []portfolio.Change) {
	m.qmu.Lock()
	m.queue = append(m.queue, batch{strategy, changes})
	m.qmu.Unlock()
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *Manager) drain() {
	for {
		m.qmu.Lock()
		q := m.queue
		m.queue = nil
		m.qmu.Unlock()
		if len(q) == 0 {
			return
		}
		for _, b := range q {
			m.Process(b.strategy, b.changes)
		}
	}
}

// Process 把一批目标变化执行到所有跟随这个策略的用户身上。
func (m *Manager) Process(strategy string, changes []portfolio.Change) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var list []*Follow
	for _, f := range m.follows {
		if f.Strategy == strategy && f.Status == StActive {
			list = append(list, f)
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	m.Shuffle(len(list), func(i, j int) { list[i], list[j] = list[j], list[i] }) // D15：不卖优先成交
	for _, f := range list {
		m.syncManual(f)
		for _, c := range m.reduceFirst(f, changes) {
			m.execute(f, c, "")
		}
	}
}

// reduceFirst 先做减仓的，腾出保证金。
func (m *Manager) reduceFirst(f *Follow, changes []portfolio.Change) []portfolio.Change {
	b, err := m.Trader.Brief(f.Account)
	if err != nil {
		return changes
	}
	out := append([]portfolio.Change(nil), changes...)
	reduces := func(c portfolio.Change) bool {
		px := m.price(f, c.Instrument, c.RefPrice)
		return px.Sign() > 0 && isReduce(b.Pos[c.Instrument].Qty, c.Weight.Mul(f.Params.Alloc).Div(px))
	}
	sort.SliceStable(out, func(i, j int) bool { return reduces(out[i]) && !reduces(out[j]) })
	return out
}

func (m *Manager) price(f *Follow, inst string, fallback decimal.Decimal) decimal.Decimal {
	if p := m.Portfolio(f.Strategy); p != nil {
		if px, ok := p.RefPrice(inst); ok {
			return px
		}
	}
	return fallback
}

// isReduce 目标仓位比现在小、方向没变（或者目标是空仓）。
func isReduce(cur, target decimal.Decimal) bool {
	if cur.IsZero() {
		return false
	}
	if target.IsZero() {
		return true
	}
	return cur.Sign() == target.Sign() && target.Abs().LessThan(cur.Abs())
}

// plan 用户的上限过一遍（在平台风控之后，3.8）。减仓和平仓不受限制。
// 返回目标数量（带符号）、备注，skip 不为空表示不做。
func plan(f *Follow, b *trading.Brief, inst string, w, px decimal.Decimal, markOf func(string) decimal.Decimal) (target, applied decimal.Decimal, note, skip string) {
	alloc := f.Params.Alloc
	cur := b.Pos[inst].Qty
	n := w.Mul(f.Params.ratio()).Mul(alloc) // 目标名义价值 = 权重 × 跟随比例 × 分配资金
	if isReduce(cur, n.Div(px)) || n.IsZero() {
		return n.Div(px), w, "", ""
	}
	sign := decimal.NewFromInt(int64(w.Sign()))
	flip := !cur.IsZero() && cur.Sign() != w.Sign()
	if f.Params.blacklisted(inst) {
		if flip { // 旧方向平掉（减仓永远执行），新方向不开
			return decimal.Zero, decimal.Zero, "blacklist", ""
		}
		return cur, decimal.Zero, "blacklist", "skipped_blacklist"
	}
	if c := f.Params.PerInst.Mul(alloc); n.Abs().GreaterThan(c) {
		n = sign.Mul(c)
		note = "scaled_instrument"
	}
	others := decimal.Zero
	for x, p := range b.Pos {
		if x != inst && f.Manual[x] == 0 { // 手动管理的仓位不占 AI 的额度
			others = others.Add(p.Qty.Abs().Mul(markOf(x)))
		}
	}
	room := decimal.NewFromInt(int64(f.Params.MaxLeverage)).Mul(alloc).Mul(headroom).Sub(others)
	if n.Abs().GreaterThan(room) {
		keep := decimal.Zero
		if !flip {
			keep = cur.Abs().Mul(px) // 已有的仓位不因为上限被砍
		}
		n = sign.Mul(decimal.Max(room, keep))
		note = "scaled_leverage"
	}
	return n.Div(px), n.Div(alloc), note, ""
}

// execute 把一条目标变化做到一个用户身上。持锁调用。note 给恢复补齐时用。
func (m *Manager) execute(f *Follow, c portfolio.Change, note string) {
	now := m.now()
	inst := c.Instrument
	m.stopWork(f, inst) // 同一品种上一张单还没结束：以新目标为准
	b, err := m.Trader.Brief(f.Account)
	if err != nil {
		return
	}
	cur := b.Pos[inst].Qty
	m.nextEx++
	ex := &Exec{ID: m.nextEx, Follow: f.ID, Msg: c.MsgID, Instrument: inst, Time: now, Weight: c.Weight, Note: note}
	skip := func(state string) {
		ex.State, ex.End = state, now
		m.emit(Event{Kind: "exec", Follow: f.ID, Exec: ex})
	}
	if f.Manual[inst] != 0 { // 手动权限最高：AI 跳过（4.5）
		skip("skipped_manual")
		return
	}
	// 平台风控平仓：跟着平，不看价格
	if c.ClosedBy != "" {
		ex.Note = c.ClosedBy
		if cur.IsZero() {
			skip("skipped_min_adjust")
			return
		}
		side := "sell"
		if cur.Sign() < 0 {
			side = "buy"
		}
		ex.Side, ex.Qty = side, cur.Abs()
		closeIt := func() {
			o, err := m.Trader.CloseAs(f.Account, inst, "follow", c.ClosedBy)
			ex.End = m.now()
			if err != nil {
				ex.State, ex.Reject = "expired", codeOf(err)
			} else {
				ex.Filled, ex.AvgPx, ex.Fee = o.Filled, o.AvgPx, o.Fee
				ex.State = "filled"
				if o.Filled.LessThan(o.Qty) {
					ex.State = "partial"
				}
			}
		}
		if m.remote(f.Account) {
			ex.State = "pending"
			m.emit(Event{Kind: "exec", Follow: f.ID, Exec: ex})
			done := *ex
			m.enqueue(f.Account, func() {
				ex := &done
				o, err := m.Trader.CloseAs(f.Account, inst, "follow", c.ClosedBy)
				ex.End = m.now()
				if err != nil {
					ex.State, ex.Reject = "expired", codeOf(err)
				} else {
					ex.Filled, ex.AvgPx, ex.Fee = o.Filled, o.AvgPx, o.Fee
					ex.State = "filled"
					if o.Filled.LessThan(o.Qty) {
						ex.State = "partial"
					}
				}
				m.mu.Lock()
				m.emit(Event{Kind: "exec", Follow: f.ID, Exec: ex})
				m.mu.Unlock()
			})
			return
		}
		closeIt()
		m.emit(Event{Kind: "exec", Follow: f.ID, Exec: ex})
		return
	}
	// 过期信号作废（3.7）
	if c.Deadline > 0 && now > c.Deadline {
		skip("skipped_expired")
		return
	}
	px := m.price(f, inst, decimal.Zero)
	if px.Sign() <= 0 {
		skip("skipped_price_moved")
		return
	}
	mark := func(x string) decimal.Decimal { return m.price(f, x, b.Pos[x].Entry) }
	target, applied, pnote, sk := plan(f, b, inst, c.Weight, px, mark)
	if pnote != "" {
		ex.Note = pnote
	}
	ex.Applied = applied
	if sk != "" {
		skip(sk)
		return
	}
	reducing := isReduce(cur, target)
	slip := decimal.NewFromInt(int64(c.MaxSlipBps))
	moved := c.RefPrice.Sign() <= 0 || px.Sub(c.RefPrice).Abs().Div(c.RefPrice).Mul(bps).GreaterThan(slip)
	if moved && !reducing {
		skip("skipped_price_moved")
		return
	}
	// 差额小于 0.01 不动
	delta := target.Sub(cur)
	if delta.Mul(px).Abs().LessThan(portfolio.MinAdjust.Mul(f.Params.Alloc)) && !(target.IsZero() && !cur.IsZero()) {
		skip("skipped_min_adjust")
		return
	}
	buy := delta.Sign() > 0
	qty := delta.Abs()
	if target.IsZero() {
		qty = cur.Abs() // 全平：一点不留
	} else if q, known := m.Trader.RoundQty(inst, qty); known {
		qty = q
	}
	if qty.IsZero() {
		skip("skipped_min_adjust")
		return
	}
	// 开新仓时才设杠杆和保证金模式（D75 第 14 条：默认逐仓 3 倍）
	setup := func() {}
	if cur.IsZero() {
		acct, lev := f.Account, f.Params.MaxLeverage
		setup = func() {
			if err := m.Trader.SetMode(acct, inst, "isolated"); err != nil {
				m.Log.Debug("follow: 设保证金模式", "err", err)
			}
			if err := m.Trader.SetLeverage(acct, inst, lev); err != nil {
				m.Log.Debug("follow: 设杠杆", "err", err)
			}
		}
	}
	ref, sl := c.RefPrice, slip
	urg := c.Urgency
	if moved { // 能走到这里只可能是减仓：减仓永远执行，放宽价格立刻吃单
		ref, sl, urg = px, riskSlip, "immediate"
	}
	limit := ref.Mul(bps.Add(sl)).Div(bps)
	if !buy {
		limit = ref.Mul(bps.Sub(sl)).Div(bps)
	}
	reduceOnly := !cur.IsZero() && (cur.Sign() > 0) != buy && qty.LessThanOrEqual(cur.Abs())
	ex.State, ex.Qty, ex.Side = "pending", qty, sideOf(buy)
	m.emit(Event{Kind: "exec", Follow: f.ID, Exec: ex})
	w := &portfolio.Work{Trader: m.Trader, Acct: f.Account, Inst: inst, Buy: buy, Qty: qty, Limit: limit, Urgency: urg, Reduce: reduceOnly,
		Start: now, Deadline: c.Deadline, Source: "follow", Tag: c.MsgID}
	lv := &live{w: w, ex: ex, exits: c.Exits}
	k := workKey{f.ID, inst}
	if m.remote(f.Account) {
		// 交易所账户：先登记，网络调用排进这个账户的队列；做完再拿锁写记录
		lv.busy = true
		m.works[k] = lv
		m.enqueue(f.Account, func() {
			setup()
			w.Begin()
			m.afterRemote(f, k, lv)
		})
		return
	}
	setup()
	w.Begin()
	if w.Done {
		m.finish(f, lv)
		return
	}
	m.works[k] = lv
}

// afterRemote 交易所账户的一步（下单或推进）做完了：没做完就留着等下次推进；做完了写记录、挂止盈止损（挂单在锁外）。
// 这张单在这期间被替换或停掉了（不在 works 里了）：停的那一步会写记录，这里不管。
func (m *Manager) afterRemote(f *Follow, k workKey, lv *live) {
	m.mu.Lock()
	lv.busy = false
	if m.works[k] != lv || !lv.w.Done {
		m.mu.Unlock()
		return
	}
	delete(m.works, k)
	m.record1(f, lv)
	m.mu.Unlock()
	m.applyExits(f, lv.ex.Instrument, lv.exits)
}

func sideOf(buy bool) string {
	if buy {
		return "buy"
	}
	return "sell"
}

func codeOf(err error) string {
	var r *trading.Reject
	if errors.As(err, &r) {
		return r.Code
	}
	return err.Error()
}

// finish 一笔结束：写记录，挂止盈止损。持锁调用（模拟账户）。
func (m *Manager) finish(f *Follow, lv *live) {
	m.record1(f, lv)
	m.applyExits(f, lv.ex.Instrument, lv.exits)
}

// record1 写这一笔的执行记录。持锁调用。
func (m *Manager) record1(f *Follow, lv *live) {
	w, ex := lv.w, *lv.ex
	ex.State, ex.End = w.State(), m.now()
	ex.Filled, ex.AvgPx, ex.Fee, ex.Reject = w.Filled, w.AvgPx(), w.Fee, w.Reject
	if w.Filled.IsZero() && (w.Reject == "MIN_NOTIONAL" || w.Reject == "QTY_TOO_SMALL") {
		ex.State = "skipped_min_adjust"
	}
	m.emit(Event{Kind: "exec", Follow: f.ID, Exec: &ex})
}

// applyExits 用这个用户自己的开仓价换算止盈止损，挂在持仓上：暂停时也管用（3.9）。
func (m *Manager) applyExits(f *Follow, inst string, x *portfolio.Exits) {
	if x == nil {
		return
	}
	b, err := m.Trader.Brief(f.Account)
	if err != nil {
		return
	}
	pos, ok := b.Pos[inst]
	if !ok {
		return
	}
	long := pos.Qty.Sign() > 0
	one := decimal.NewFromInt(1)
	price := func(pct decimal.Decimal, up bool) *decimal.Decimal {
		if pct.Sign() <= 0 {
			return nil
		}
		k := one.Sub(pct)
		if up {
			k = one.Add(pct)
		}
		// 对齐到价格单位：往上算的向上取整，往下算的向下取整
		v, ok := m.Trader.RoundPrice(inst, !up, pos.Entry.Mul(k))
		if !ok {
			return nil
		}
		return &v
	}
	tp, sl := price(x.TakeProfit, long), price(x.StopLoss, !long)
	if tp == nil && sl == nil {
		return
	}
	if err := m.Trader.SetTPSL(f.Account, inst, tp, sl, trading.ByMark); err != nil {
		m.Log.Debug("follow: 设止盈止损", "follow", f.ID, "instrument", inst, "err", err)
	}
}

func (m *Manager) stopWork(f *Follow, inst string) {
	k := workKey{f.ID, inst}
	lv := m.works[k]
	if lv == nil {
		return
	}
	delete(m.works, k)
	if m.remote(f.Account) {
		// 交易所账户：撤单排进队列（排在这张单自己的下单后面），撤完写记录
		m.enqueue(f.Account, func() {
			lv.w.Stop()
			m.mu.Lock()
			m.record1(f, lv)
			m.mu.Unlock()
			m.applyExits(f, lv.ex.Instrument, lv.exits)
		})
		return
	}
	lv.w.Stop()
	m.finish(f, lv)
}

func (m *Manager) stopAllWork(f *Follow) {
	for k := range m.works {
		if k.follow == f.ID {
			m.stopWork(f, k.inst)
		}
	}
}

// Tick 每秒：推进挂单、最大亏损、把要平的仓位平掉。
func (m *Manager) Tick() {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	for k, lv := range m.works {
		f := m.follows[k.follow]
		if m.remote(f.Account) {
			if !lv.busy { // 交易所账户：推进一步排进队列（查单、到时间撤单都要走网络）
				lv.busy = true
				k, lv := k, lv
				m.enqueue(f.Account, func() {
					lv.w.Step(now)
					m.afterRemote(f, k, lv)
				})
			}
			continue
		}
		lv.w.Step(now)
		if lv.w.Done {
			delete(m.works, k)
			m.finish(f, lv)
		}
	}
	for _, f := range m.follows {
		switch f.Status {
		case StActive, StPaused:
			m.syncManual(f)
			b, err := m.Trader.Brief(f.Account)
			if err != nil {
				continue
			}
			// 跟随净值 = 分配资金 + 权益变化，跌破 分配资金 × (1 − 最大亏损) 就平仓并停止
			if b.Equity.Sub(f.StartEquity).LessThanOrEqual(f.Params.Alloc.Mul(f.Params.MaxLoss).Neg()) {
				m.stopLocked(f, ReasonMaxLoss, true)
			}
		case StClosing:
			m.closeOut(f)
		}
	}
}

// stopLocked 停止。closeAll 时平掉跟随下过单的品种，平完才算停好（行情暂停时下一秒接着平）。
func (m *Manager) stopLocked(f *Follow, reason string, closeAll bool) {
	m.stopAllWork(f)
	cancel := func() {
		if _, err := m.Trader.CancelSource(f.Account, "follow", "FOLLOW_STOPPED"); err != nil {
			m.Log.Debug("follow: 撤单", "err", err)
		}
	}
	if m.remote(f.Account) {
		m.enqueue(f.Account, cancel)
	} else {
		cancel()
	}
	m.emit(Event{Kind: "stop", Follow: f.ID, Reason: reason, Close: closeAll})
	if closeAll {
		m.closeOut(f)
	}
}

func (m *Manager) closeOut(f *Follow) {
	if m.remote(f.Account) {
		m.closeOutRemote(f)
		return
	}
	b, err := m.Trader.Brief(f.Account)
	if err != nil {
		m.emit(Event{Kind: "closed", Follow: f.ID})
		return
	}
	left := 0
	for inst := range f.Insts {
		if _, ok := b.Pos[inst]; !ok || f.Manual[inst] != 0 { // 手动管理的不动（4.7）
			continue
		}
		o, err := m.Trader.CloseAs(f.Account, inst, "follow", f.Reason)
		if err != nil || o.Filled.LessThan(o.Qty) {
			left++
		}
		if o != nil && o.Filled.Sign() > 0 {
			m.nextEx++
			m.emit(Event{Kind: "exec", Follow: f.ID, Exec: &Exec{ID: m.nextEx, Follow: f.ID, Instrument: inst, Time: m.now(), End: m.now(), Note: f.Reason,
				State: "filled", Side: o.Side, Qty: o.Qty, Filled: o.Filled, AvgPx: o.AvgPx, Fee: o.Fee}})
		}
	}
	if left == 0 {
		m.emit(Event{Kind: "closed", Follow: f.ID})
	}
}

// closeOutRemote 交易所账户的平仓：要平哪些在锁里定，下单在队列里做，做完再拿锁写记录。同一个跟随同一时间只排一次。持锁调用。
func (m *Manager) closeOutRemote(f *Follow) {
	if m.closing == nil {
		m.closing = map[int64]bool{}
	}
	if m.closing[f.ID] {
		return
	}
	b, err := m.Trader.Brief(f.Account)
	if err != nil {
		m.emit(Event{Kind: "closed", Follow: f.ID})
		return
	}
	var insts []string
	for inst := range f.Insts {
		if _, ok := b.Pos[inst]; ok && f.Manual[inst] == 0 { // 手动管理的不动（4.7）
			insts = append(insts, inst)
		}
	}
	sort.Strings(insts)
	m.closing[f.ID] = true
	reason := f.Reason
	m.enqueue(f.Account, func() {
		type res struct {
			inst string
			o    *trading.Order
			err  error
		}
		var out []res
		for _, inst := range insts {
			o, err := m.Trader.CloseAs(f.Account, inst, "follow", reason)
			out = append(out, res{inst, o, err})
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		m.closing[f.ID] = false
		left := 0
		for _, r := range out {
			if r.err != nil || r.o.Filled.LessThan(r.o.Qty) {
				left++
			}
			if r.o != nil && r.o.Filled.Sign() > 0 {
				m.nextEx++
				m.emit(Event{Kind: "exec", Follow: f.ID, Exec: &Exec{ID: m.nextEx, Follow: f.ID, Instrument: r.inst, Time: m.now(), End: m.now(), Note: reason,
					State: "filled", Side: r.o.Side, Qty: r.o.Qty, Filled: r.o.Filled, AvgPx: r.o.AvgPx, Fee: r.o.Fee}})
			}
		}
		if left == 0 && f.Status == StClosing {
			m.emit(Event{Kind: "closed", Follow: f.ID})
		}
	})
}

// ---------- 用户操作 ----------

// StartReq 开启跟随。Account 为 0 时用"策略跟随"账户，第一次会自动建（D75 第 12 条）。
type StartReq struct {
	Strategy    string          `json:"strategy"`
	Account     int64           `json:"account"`
	Alloc       decimal.Decimal `json:"alloc"`
	MaxLoss     decimal.Decimal `json:"max_loss"`
	MaxLeverage int             `json:"max_leverage"`
	PerInst     decimal.Decimal `json:"per_inst"`
	Blacklist   []string        `json:"blacklist"`
	Ratio       decimal.Decimal `json:"ratio"`   // 跟随比例，不填 = 1
	Confirm     bool            `json:"confirm"` // 用户自己确认过（D75 第 23 条）
	// TradePassword 交易所账户要二级密码（不存，不进事件）
	TradePassword string `json:"trade_password"`
}

func (r StartReq) params() Params {
	p := Params{Alloc: r.Alloc, MaxLoss: r.MaxLoss, MaxLeverage: r.MaxLeverage, PerInst: r.PerInst, Blacklist: r.Blacklist, Ratio: r.Ratio}
	if p.Ratio.IsZero() {
		p.Ratio = decimal.NewFromInt(1)
	}
	if p.MaxLoss.IsZero() {
		p.MaxLoss = DefaultMaxLoss
	}
	if p.MaxLeverage == 0 {
		p.MaxLeverage = DefaultMaxLeverage
	}
	if p.PerInst.IsZero() {
		p.PerInst = DefaultPerInst
	}
	if p.Blacklist == nil {
		p.Blacklist = []string{}
	}
	return p
}

func checkParams(p Params) error {
	if p.MaxLoss.Sign() <= 0 || p.MaxLoss.GreaterThan(decimal.NewFromInt(1)) {
		return rej(400, "BAD_MAX_LOSS")
	}
	if p.MaxLeverage < 1 || p.MaxLeverage > MaxLeverageLimit {
		return rej(400, "BAD_LEVERAGE", "max", decimal.NewFromInt(int64(MaxLeverageLimit)).String())
	}
	if p.PerInst.Sign() <= 0 || p.PerInst.GreaterThan(decimal.NewFromInt(1)) {
		return rej(400, "BAD_PER_INST")
	}
	if p.Ratio.LessThan(MinRatio) || p.Ratio.GreaterThan(MaxRatio) {
		return rej(400, "BAD_RATIO", "min", MinRatio.String(), "max", MaxRatio.String())
	}
	if p.Alloc.LessThan(MinAlloc) {
		return rej(400, "ALLOC_TOO_SMALL", "min", MinAlloc.String())
	}
	return nil
}

// livePassword 交易所账户（真钱）上开启跟随、改参数要二级密码。
func (m *Manager) livePassword(u *auth.User, acct int64, pw string) error {
	if acct < trading.LiveBase {
		return nil
	}
	if m.TradePassword == nil {
		return rej(404, "NOT_FOUND")
	}
	if err := m.TradePassword(u, pw); err != nil {
		var ae *auth.Err
		if errors.As(err, &ae) {
			return rej(ae.HTTP, ae.Code)
		}
		return err
	}
	return nil
}

// liveStrategy 交易所账户只能跟自己的策略（D99）。
func (m *Manager) liveStrategy(ctx context.Context, u *auth.User, acct int64, strategy string) error {
	if acct < trading.LiveBase {
		return nil
	}
	if m.LiveStrategy == nil {
		return rej(403, "LIVE_OWN_STRATEGY")
	}
	if code := m.LiveStrategy(ctx, u, acct, strategy); code != "" {
		return rej(403, code)
	}
	return nil
}

// LiveCheck 给向导用：这个策略能不能接到这几个交易所账户上。账户编号 → 拒绝的代码（空 = 可以）。
// 账户是不是这个人的在这里也查：别人的账户一律当作不行，不透露存不存在。
func (m *Manager) LiveCheck(ctx context.Context, u *auth.User, strategy string, accts []int64) map[int64]string {
	out := map[int64]string{}
	for _, a := range accts {
		if a < trading.LiveBase {
			continue
		}
		if m.LiveAcct == nil {
			out[a] = "NOT_FOUND"
			continue
		}
		if owned, _ := m.LiveAcct(ctx, u.ID, a); !owned {
			out[a] = "NOT_FOUND"
			continue
		}
		code := ""
		if err := m.liveStrategy(ctx, u, a, strategy); err != nil {
			code = err.Error()
		}
		out[a] = code
	}
	return out
}

// resolveAccount 找到要用的账户。
func (m *Manager) resolveAccount(ctx context.Context, u *auth.User, id int64) (int64, error) {
	if id >= trading.LiveBase {
		if m.LiveAcct == nil {
			return 0, rej(404, "NOT_FOUND")
		}
		owned, armed := m.LiveAcct(ctx, u.ID, id)
		if !owned {
			return 0, rej(404, "NOT_FOUND")
		}
		if !armed {
			return 0, rej(400, "LIVE_NOT_ARMED")
		}
		return id, nil
	}
	if id != 0 {
		if id < 0 || !m.Accounts.OwnsPaper(ctx, u.ID, id) {
			return 0, rej(404, "NOT_FOUND")
		}
		return id, nil
	}
	list, err := m.Accounts.PaperAccounts(ctx, u.ID)
	if err != nil {
		return 0, err
	}
	name := PaperInitialName["zh"]
	if u.Lang == "en" {
		name = PaperInitialName["en"]
	}
	for _, a := range list {
		if a.Name == PaperInitialName["zh"] || a.Name == PaperInitialName["en"] || slices.Contains(oldInitialNames, a.Name) {
			return a.ID, nil
		}
	}
	a, err := m.Accounts.NewPaper(ctx, u, name)
	if err != nil {
		if errors.Is(err, auth.ErrTooManyAccounts) {
			return 0, rej(409, "TOO_MANY_ACCOUNTS")
		}
		return 0, err
	}
	return a.ID, nil
}

// topUp 模拟账户可用的钱不够 need 时，把差的补上（D103），返回补完后的账户概况。交易所账户、没设 TopUp 时原样返回。
func (m *Manager) topUp(ctx context.Context, u *auth.User, acct int64, need decimal.Decimal, b *trading.Brief) (*trading.Brief, error) {
	if m.TopUp == nil || !need.GreaterThan(b.Available) || !m.Accounts.OwnsPaper(ctx, u.ID, acct) {
		return b, nil
	}
	if err := m.TopUp(acct, need.Sub(b.Available).RoundCeil(2)); err != nil {
		return nil, err
	}
	nb, err := m.Trader.Brief(acct)
	if err != nil {
		return nil, rej(404, "NOT_FOUND")
	}
	return nb, nil
}

// Start 开启跟随。开启后马上按 AI 现在的目标建仓（和恢复一样）。
func (m *Manager) Start(ctx context.Context, u *auth.User, r StartReq) (*View, error) {
	if !r.Confirm {
		return nil, rej(400, "CONFIRM_REQUIRED")
	}
	if m.Portfolio(r.Strategy) == nil {
		return nil, rej(404, "NO_STRATEGY")
	}
	p := r.params()
	if err := checkParams(p); err != nil {
		return nil, err
	}
	acct, err := m.resolveAccount(ctx, u, r.Account)
	if err != nil {
		return nil, err
	}
	if err := m.liveStrategy(ctx, u, acct, r.Strategy); err != nil {
		return nil, err
	}
	if err := m.livePassword(u, acct, r.TradePassword); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, f := range m.follows {
		if f.Account == acct && f.Status != StStopped {
			return nil, rej(409, "ACCOUNT_FOLLOWING", "strategy", f.Strategy)
		}
	}
	b, err := m.Trader.Brief(acct)
	if err != nil {
		return nil, rej(404, "NOT_FOUND")
	}
	if b, err = m.topUp(ctx, u, acct, p.Alloc, b); err != nil {
		return nil, err
	}
	if p.Alloc.GreaterThan(b.Available) {
		return nil, rej(400, "ALLOC_OVER_AVAILABLE", "available", b.Available.Round(2).String())
	}
	m.nextID++
	id := m.nextID
	m.emit(Event{Kind: "start", Follow: id, Start: &StartEv{User: u.ID, Account: acct, Strategy: r.Strategy, Params: p, StartEquity: b.Equity}})
	m.fillSeq[id] = m.Trader.FillSeq(acct)
	// 账户里已有的仓位算手动管理，AI 不动（4.6）
	for inst := range b.Pos {
		m.emit(Event{Kind: "manual", Follow: id, Inst: inst})
		m.record(m.follows[id], inst, "manual")
	}
	m.catchUp(m.follows[id])
	return m.viewLocked(m.follows[id]), nil
}

// targetsFor AI 现在的目标；跟随持有、但 AI 没有目标的品种算 0。
func (m *Manager) targetsFor(f *Follow) []portfolio.Change {
	pf := m.Portfolio(f.Strategy)
	if pf == nil {
		return nil
	}
	list := pf.Targets()
	seen := map[string]bool{}
	for i := range list {
		seen[list[i].Instrument] = true
		list[i].ClosedBy = "" // 补齐是按目标算，不是跟着风控平仓
	}
	if b, err := m.Trader.Brief(f.Account); err == nil {
		for inst := range f.Insts {
			if _, held := b.Pos[inst]; held && !seen[inst] {
				if ref, ok := pf.RefPrice(inst); ok {
					list = append(list, portfolio.Change{Instrument: inst, Weight: decimal.Zero, RefPrice: ref, Urgency: "immediate", MaxSlipBps: portfolio.DefaultSlip})
				}
			}
		}
	}
	return list
}

func (m *Manager) catchUp(f *Follow) {
	for _, c := range m.reduceFirst(f, m.targetsFor(f)) {
		m.execute(f, c, "catch_up")
	}
}

// own 取用户自己的跟随；别人的一律 404（3.11）。
func (m *Manager) own(u *auth.User, id int64) (*Follow, error) {
	f := m.follows[id]
	if f == nil || f.User != u.ID {
		return nil, rej(404, "NOT_FOUND")
	}
	return f, nil
}

// Update 改参数，下一次调仓开始生效。
func (m *Manager) Update(u *auth.User, id int64, r StartReq) (*View, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, err := m.own(u, id)
	if err != nil {
		return nil, err
	}
	if !f.running() {
		return nil, rej(409, "FOLLOW_STOPPED")
	}
	if err := m.livePassword(u, f.Account, r.TradePassword); err != nil {
		return nil, err
	}
	p := r.params()
	if r.Alloc.IsZero() {
		p.Alloc = f.Params.Alloc
	}
	if r.Ratio.IsZero() { // 没改比例：保持原来的
		p.Ratio = f.Params.ratio()
	}
	if err := checkParams(p); err != nil {
		return nil, err
	}
	b, err := m.Trader.Brief(f.Account)
	if err != nil {
		return nil, rej(404, "NOT_FOUND")
	}
	if b, err = m.topUp(context.Background(), u, f.Account, p.Alloc.Sub(f.Params.Alloc), b); err != nil {
		return nil, err
	}
	if add := p.Alloc.Sub(f.Params.Alloc); add.GreaterThan(b.Available) {
		return nil, rej(400, "ALLOC_OVER_AVAILABLE", "available", b.Available.Round(2).String())
	}
	m.emit(Event{Kind: "params", Follow: id, Params: &p})
	return m.viewLocked(f), nil
}

// Pause 暂停：不再执行新目标，持仓和止损留着。
func (m *Manager) Pause(u *auth.User, id int64) (*View, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, err := m.own(u, id)
	if err != nil {
		return nil, err
	}
	if f.Status != StActive {
		return nil, rej(409, "FOLLOW_NOT_ACTIVE")
	}
	m.stopAllWork(f)
	m.emit(Event{Kind: "pause", Follow: id})
	return m.viewLocked(f), nil
}

// PlanItem 恢复时会产生的一笔交易（提示用）。
type PlanItem struct {
	Instrument string          `json:"instrument"`
	Side       string          `json:"side"`
	Qty        decimal.Decimal `json:"qty"`
	Notional   decimal.Decimal `json:"notional"`
}

// ResumePlan 恢复时会产生哪些交易：恢复前先给用户看（D75 第 16 条）。
func (m *Manager) ResumePlan(u *auth.User, id int64) ([]PlanItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, err := m.own(u, id)
	if err != nil {
		return nil, err
	}
	m.syncManual(f)
	out := []PlanItem{}
	b, err := m.Trader.Brief(f.Account)
	if err != nil {
		return out, nil
	}
	for _, c := range m.targetsFor(f) {
		px := m.price(f, c.Instrument, decimal.Zero)
		if px.Sign() <= 0 || f.Manual[c.Instrument] != 0 {
			continue
		}
		mark := func(x string) decimal.Decimal { return m.price(f, x, b.Pos[x].Entry) }
		target, _, _, sk := plan(f, b, c.Instrument, c.Weight, px, mark)
		if sk != "" {
			continue
		}
		delta := target.Sub(b.Pos[c.Instrument].Qty)
		if delta.Mul(px).Abs().LessThan(portfolio.MinAdjust.Mul(f.Params.Alloc)) && !(target.IsZero() && !b.Pos[c.Instrument].Qty.IsZero()) {
			continue
		}
		q, known := m.Trader.RoundQty(c.Instrument, delta.Abs())
		if target.IsZero() {
			q, known = delta.Abs(), true
		}
		if !known || q.IsZero() {
			continue
		}
		out = append(out, PlanItem{Instrument: c.Instrument, Side: sideOf(delta.Sign() > 0), Qty: q, Notional: q.Mul(px).Round(2)})
	}
	return out, nil
}

// Resume 恢复：马上补齐到 AI 现在的目标（D75 第 16 条）。
func (m *Manager) Resume(u *auth.User, id int64) (*View, error) {
	// 以前开在交易所账户上、跟的是别人策略的：不能再恢复（D99）。要查库，放在锁外
	m.mu.Lock()
	f, err := m.own(u, id)
	var acct int64
	var strat string
	if err == nil {
		acct, strat = f.Account, f.Strategy
	}
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if err := m.liveStrategy(context.Background(), u, acct, strat); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	f, err = m.own(u, id)
	if err != nil {
		return nil, err
	}
	if f.Status != StPaused {
		return nil, rej(409, "FOLLOW_NOT_PAUSED")
	}
	m.emit(Event{Kind: "resume", Follow: id})
	m.syncManual(f)
	m.catchUp(f)
	return m.viewLocked(f), nil
}

// Stop 停止。closeAll = true 平掉跟随的仓位；false 仓位留下，之后就是用户自己的。
func (m *Manager) Stop(u *auth.User, id int64, closeAll bool) (*View, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, err := m.own(u, id)
	if err != nil {
		return nil, err
	}
	if !f.running() {
		return nil, rej(409, "FOLLOW_STOPPED")
	}
	reason := ReasonKeep
	if closeAll {
		reason = ReasonUser
	}
	m.stopLocked(f, reason, closeAll)
	return m.viewLocked(f), nil
}

// AccountArchived 账户被重置：跟随随之结束。引擎已经撤单、不再撮合，不用平仓。
func (m *Manager) AccountArchived(acct int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, f := range m.follows {
		if f.Account == acct && f.Status != StStopped {
			for k, lv := range m.works {
				if k.follow == f.ID {
					if !m.remote(f.Account) {
						lv.w.Done = true
					}
					delete(m.works, k)
				}
			}
			m.emit(Event{Kind: "stop", Follow: f.ID, Reason: ReasonArchived})
		}
	}
}

// ---------- 查询 ----------

// View 给用户看的跟随状态。
type View struct {
	*Follow
	NAV     decimal.Decimal `json:"nav"`     // 跟随净值 = 分配资金 + 权益变化
	PnL     decimal.Decimal `json:"pnl"`     // 权益变化
	StopAt  decimal.Decimal `json:"stop_at"` // 跌到这个净值自动平仓
	Sample  bool            `json:"-"`       //
	Records []ExecView      `json:"records"` // 最近的执行记录，新的在前
}

// ExecView 执行记录，加上和模型组合对比的滑点。
type ExecView struct {
	Exec
	ModelAvg decimal.Decimal `json:"model_avg"`
	SlipBps  decimal.Decimal `json:"slip_bps"` // 跟随滑点：用户成交价相对模型组合成交价，对用户不利为正
}

func (m *Manager) viewLocked(f *Follow) *View {
	c := *f
	c.Manual = copyManual(f.Manual)
	v := &View{Follow: &c, StopAt: f.Params.Alloc.Mul(decimal.NewFromInt(1).Sub(f.Params.MaxLoss)), Records: []ExecView{}}
	if b, err := m.Trader.Brief(f.Account); err == nil {
		v.PnL = b.Equity.Sub(f.StartEquity).Round(4)
		v.NAV = f.Params.Alloc.Add(v.PnL)
	}
	list := m.recs[f.ID]
	pf := m.Portfolio(f.Strategy)
	for i := len(list) - 1; i >= 0 && len(v.Records) < 100; i-- {
		v.Records = append(v.Records, slipOf(*list[i], pf))
	}
	return v
}

func copyManual(in map[string]int64) map[string]int64 {
	out := make(map[string]int64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// Slippage 用户成交均价相对模型组合成交均价的差（bps），对用户不利为正。
func Slippage(side string, avg, model decimal.Decimal) decimal.Decimal {
	if avg.Sign() <= 0 || model.Sign() <= 0 {
		return decimal.Zero
	}
	s := avg.Sub(model).Div(model).Mul(bps)
	if side == "sell" {
		s = s.Neg()
	}
	return s.Round(2)
}

func slipOf(x Exec, pf *portfolio.Portfolio) ExecView {
	v := ExecView{Exec: x}
	if pf == nil || x.Msg == "" {
		return v
	}
	for _, e := range pf.Executions(x.Msg) {
		if e.Instrument == x.Instrument && e.AvgPx != "" {
			if a, err := decimal.NewFromString(e.AvgPx); err == nil {
				v.ModelAvg = a
				v.SlipBps = Slippage(x.Side, x.AvgPx, a)
			}
		}
	}
	return v
}

// List 这个用户的所有跟随，正在跑的在前。
func (m *Manager) List(u *auth.User) []*View {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []*View{}
	for _, f := range m.follows {
		if f.User == u.ID {
			out = append(out, m.viewLocked(f))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.running() != b.running() {
			return a.running()
		}
		return a.ID > b.ID
	})
	return out
}

func (m *Manager) Get(u *auth.User, id int64) (*View, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, err := m.own(u, id)
	if err != nil {
		return nil, err
	}
	return m.viewLocked(f), nil
}

// IsFollowing 这个用户是否开启了服务：正在跟随（包括暂停和试用，D75 第 18 条）。
func (m *Manager) IsFollowing(userID int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, f := range m.follows {
		if f.User == userID && f.running() {
			return true
		}
	}
	return false
}

// Stats 某个策略的跟随人数和跟随资金（战绩页，3.22）。
func (m *Manager) Stats(strategy string) (followers int, capital decimal.Decimal) {
	m.mu.Lock()
	defer m.mu.Unlock()
	users := map[int64]bool{}
	for _, f := range m.follows {
		if f.Strategy == strategy && f.running() {
			users[f.User] = true
			capital = capital.Add(f.Params.Alloc)
		}
	}
	return len(users), capital
}

// AccountRunning 这个账户正在跟 AI 吗。
// RunningCount 正在跟的数量（系统控制台）。
func (m *Manager) RunningCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, f := range m.follows {
		if f.running() {
			n++
		}
	}
	return n
}

func (m *Manager) AccountRunning(acct int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, f := range m.follows {
		if f.Account == acct && f.running() {
			return true
		}
	}
	return false
}
