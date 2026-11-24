// Package discipline 纪律锁（D45、D80、12 的 4.10–4.20）：用户给自己定的规则，系统硬性执行。
//
//   - 规则每个用户一套，状态按账户分别算。
//   - 锁定时开仓和加仓一律拒绝，减仓和平仓永远接受（D07）。
//   - 所有变化都是只追加的事件：什么时候锁的、为什么、谁解的，都能查，重启后状态不变。
//
// 锁的顺序：引擎下单时持有引擎的锁再调 Guard，所以这里持锁时绝不能调引擎。
package discipline

import (
	"context"
	"log/slog"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"qs/internal/trading"
)

// 规则代号。
const (
	RuleDaily    = "daily_loss"
	RuleGiveBack = "give_back"
	RuleDrawdown = "max_drawdown"
	RuleStreak   = "loss_streak"
)

// UnlockWait 输入二级密码后等多久生效。
const UnlockWait = 10 * time.Minute

const dayMs = 24 * 3600 * 1000

var one = decimal.NewFromInt(1)

// Rules 一套规则。比例都是小数，0.05 就是 5%。
type Rules struct {
	DailyLoss   decimal.Decimal `json:"daily_loss"`   // 单日最大亏损，占当日开盘权益
	PerTrade    decimal.Decimal `json:"per_trade"`    // 每笔最大风险，占权益；也是 1R 的大小
	MaxDrawdown decimal.Decimal `json:"max_drawdown"` // 最大回撤，从最高权益算
	PeakR       decimal.Decimal `json:"peak_r"`       // 利润回吐：盈利到过这么多 R
	StopR       decimal.Decimal `json:"stop_r"`       // 又回落到这么多 R 就停手
	LossStreak  int             `json:"loss_streak"`  // 连亏几笔
	CooldownMin int             `json:"cooldown_min"` // 冷静多少分钟
	// MinLoss 连亏只数亏得比这个多的单（占权益）；比它小的盈亏（试单、手续费）当没发生，不算一笔也不打断。
	// 0 = 每一笔都算（D107 以前存的规则就是这样）
	MinLoss decimal.Decimal `json:"min_loss"`
}

// Default 默认规则（D80）。
func Default() Rules {
	d := decimal.RequireFromString
	return Rules{DailyLoss: d("0.05"), PerTrade: d("0.01"), MaxDrawdown: d("0.15"), PeakR: d("3"), StopR: d("1"), LossStreak: 3, CooldownMin: 30, MinLoss: d("0.001")}
}

func between(v decimal.Decimal, lo, hi string) bool {
	return v.GreaterThanOrEqual(decimal.RequireFromString(lo)) && v.LessThanOrEqual(decimal.RequireFromString(hi))
}

// Validate 超出合理范围拒绝（4.10）。
func (r Rules) Validate() error {
	switch {
	case !between(r.DailyLoss, "0.005", "0.5"):
		return errOf(400, "BAD_DAILY_LOSS")
	case !between(r.PerTrade, "0.001", "0.1"):
		return errOf(400, "BAD_PER_TRADE")
	case !between(r.MaxDrawdown, "0.01", "0.8"):
		return errOf(400, "BAD_MAX_DRAWDOWN")
	case !between(r.PeakR, "1", "20") || r.StopR.Sign() < 0 || r.StopR.GreaterThanOrEqual(r.PeakR):
		return errOf(400, "BAD_GIVE_BACK")
	case r.LossStreak < 2 || r.LossStreak > 20:
		return errOf(400, "BAD_LOSS_STREAK")
	case r.CooldownMin < 5 || r.CooldownMin > 1440:
		return errOf(400, "BAD_COOLDOWN")
	case r.MinLoss.Sign() < 0 || r.MinLoss.GreaterThan(decimal.RequireFromString("0.05")):
		return errOf(400, "BAD_MIN_LOSS")
	}
	return nil
}

// LooserThan 有没有哪一条比 o 宽松。
func (r Rules) LooserThan(o Rules) bool {
	return r.DailyLoss.GreaterThan(o.DailyLoss) || r.PerTrade.GreaterThan(o.PerTrade) || r.MaxDrawdown.GreaterThan(o.MaxDrawdown) ||
		r.PeakR.GreaterThan(o.PeakR) || r.StopR.LessThan(o.StopR) || r.LossStreak > o.LossStreak || r.CooldownMin < o.CooldownMin ||
		r.MinLoss.GreaterThan(o.MinLoss)
}

// Lock 一次锁定。
type Lock struct {
	Rule     string          `json:"rule"`
	Since    int64           `json:"since"`
	Until    int64           `json:"until,omitempty"`     // 到这个时间自动解开；0 表示只能走解锁
	UnlockAt int64           `json:"unlock_at,omitempty"` // 申请了解锁：到这个时间生效
	Value    decimal.Decimal `json:"value"`               // 触发时的数值
	Limit    decimal.Decimal `json:"limit"`               // 规则的线
}

// Event 只追加的事件。
type Event struct {
	Seq     int64  `json:"seq"`
	Time    int64  `json:"t"`
	Kind    string `json:"k"` // settings | enabled | tz | day | mark | streak | lock | unlock_req | unlock | reject
	User    int64  `json:"u,omitempty"`
	Account int64  `json:"a,omitempty"`

	Rules   *Rules           `json:"rules,omitempty"`
	On      bool             `json:"on,omitempty"`
	By      string           `json:"by,omitempty"` // login | password | time
	TZ      int              `json:"tz,omitempty"` // 比 UTC 早多少分钟
	Day     int64            `json:"day,omitempty"`
	Open    *decimal.Decimal `json:"open,omitempty"`
	Peak    *decimal.Decimal `json:"peak,omitempty"`
	DayPeak *decimal.Decimal `json:"day_peak,omitempty"`
	Streak  int              `json:"streak,omitempty"`
	Seen    int64            `json:"seen,omitempty"`
	Lock    *Lock            `json:"lock,omitempty"`
	At      int64            `json:"at,omitempty"`
	Rule    string           `json:"rule,omitempty"`
	Inst    string           `json:"inst,omitempty"`
	Code    string           `json:"code,omitempty"`
}

type userState struct {
	id      int64
	rules   Rules
	enabled bool
	tz      int
}

type acctState struct {
	id        int64
	user      int64
	day       int64
	dayOpen   decimal.Decimal
	dayPeak   decimal.Decimal
	peak      decimal.Decimal
	streak    int
	seen      int64 // 看过的最后一笔成交
	lock      *Lock
	unlockDay int64            // 哪一天申请过解锁（当天只能一次）
	suspended map[string]int64 // 解锁后这条规则当天不再查：规则 → 哪一天
	markedAt  int64
	markPeak  decimal.Decimal
	markDay   decimal.Decimal
}

// Engine 纪律锁要用到的引擎操作。
type Engine interface {
	Brief(acct int64) (*trading.Brief, error)
	CancelOpening(acct int64, reason string) (int, error)
	ClosedTrades(acct, after int64) ([]trading.ClosedTrade, int64)
	Note(acct int64, kind, inst, reason string) error
}

// Manager 纪律锁。
type Manager struct {
	Trader    Engine
	AIManaged func(acct int64, inst string) bool // 这个币现在是 AI 在管：连亏不算它
	Persist   func([]Event) error
	Log       *slog.Logger
	Now       func() time.Time

	mu      sync.Mutex
	seq     int64
	users   map[int64]*userState
	accts   map[int64]*acctState
	history map[int64][]Event // 每个账户最近的锁定和解锁记录
	userLog map[int64][]Event // 每个用户改规则、开关的记录

	pmu     sync.Mutex
	wmu     sync.Mutex
	pending []Event
}

// New 建一个纪律锁。
func New(t Engine, log *slog.Logger) *Manager {
	return &Manager{Trader: t, Log: log, Now: time.Now, users: map[int64]*userState{}, accts: map[int64]*acctState{}, history: map[int64][]Event{}, userLog: map[int64][]Event{}}
}

func (m *Manager) now() int64 { return m.Now().UnixMilli() }

func dayKey(t int64, tz int) int64 { return (t + int64(tz)*60000) / dayMs }

func nextDay(t int64, tz int) int64 { return (dayKey(t, tz)+1)*dayMs - int64(tz)*60000 }

func (m *Manager) user(id int64) *userState {
	u := m.users[id]
	if u == nil {
		u = &userState{id: id, rules: Default(), enabled: true}
		m.users[id] = u
	}
	return u
}

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

func (m *Manager) apply(ev Event) {
	switch ev.Kind {
	case "settings":
		m.user(ev.User).rules = *ev.Rules
		m.userLog[ev.User] = append(m.userLog[ev.User], ev)
		return
	case "enabled":
		m.user(ev.User).enabled = ev.On
		m.userLog[ev.User] = append(m.userLog[ev.User], ev)
		return
	case "tz":
		m.user(ev.User).tz = ev.TZ
		return
	}
	a := m.accts[ev.Account]
	if a == nil {
		a = &acctState{id: ev.Account, user: ev.User, suspended: map[string]int64{}}
		m.accts[ev.Account] = a
	}
	if ev.User != 0 {
		a.user = ev.User
	}
	switch ev.Kind {
	case "day":
		a.day, a.dayOpen, a.dayPeak = ev.Day, *ev.Open, *ev.Open
		a.suspended = map[string]int64{}
		a.markDay = *ev.Open
	case "mark":
		if ev.Peak != nil {
			a.peak, a.markPeak = *ev.Peak, *ev.Peak
		}
		if ev.DayPeak != nil {
			a.dayPeak, a.markDay = *ev.DayPeak, *ev.DayPeak
		}
		a.markedAt = ev.Time
	case "streak":
		a.streak, a.seen = ev.Streak, ev.Seen
	case "lock":
		l := *ev.Lock
		a.lock = &l
		m.remember(ev)
	case "unlock_req":
		if a.lock != nil {
			a.lock.UnlockAt = ev.At
		}
		a.unlockDay = ev.Day
		m.remember(ev)
	case "unlock":
		if ev.By == "password" && ev.Day != 0 {
			a.suspended[ev.Rule] = ev.Day
		}
		if ev.Rule == RuleStreak {
			a.streak = 0
		}
		a.lock = nil
		m.remember(ev)
	case "reject":
		m.remember(ev)
	}
}

func (m *Manager) remember(ev Event) {
	list := append(m.history[ev.Account], ev)
	if len(list) > 200 {
		list = list[len(list)-200:]
	}
	m.history[ev.Account] = list
}

// Restore 重启恢复：重放事件。
func (m *Manager) Restore(evs []Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, ev := range evs {
		m.seq = max(m.seq, ev.Seq)
		m.apply(ev)
	}
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
		if m.Log != nil {
			m.Log.Error("纪律锁：写库失败，稍后重试", "count", len(b), "err", err)
		}
		m.pmu.Lock()
		m.pending = append(b, m.pending...)
		n := len(m.pending)
		m.pmu.Unlock()
		return n
	}
	return 0
}

// ---------- 账户和登录 ----------

// Register 这个账户归这个用户，开始看着它。
func (m *Manager) Register(user, acct int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.user(user)
	if a := m.accts[acct]; a != nil {
		a.user = user
		return
	}
	m.accts[acct] = &acctState{id: acct, user: user, suspended: map[string]int64{}}
}

// Accounts 所有登记过的账户（模拟和交易所账户）：全局停机撤挂单时用。
func (m *Manager) Accounts() []int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]int64, 0, len(m.accts))
	for id := range m.accts {
		out = append(out, id)
	}
	return out
}

// Unregister 账户被重置或归档。
func (m *Manager) Unregister(acct int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.accts, acct)
}

// NewUser 新建的用户：纪律锁默认关着（D107）。用户自己在风险页打开。
// 以前（D45）默认开、每次登录自动打开；老用户没动过开关的保持原来的"开"，不替他改。
func (m *Manager) NewUser(user int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.emit(Event{Kind: "enabled", User: user, On: false, By: "default"})
}

// rebase 刚打开：基准按现在的权益重新定，关着期间的亏损不算，连亏从零数。持锁调用
func (m *Manager) rebase(user int64) {
	for _, a := range m.accts {
		if a.user == user {
			a.day = 0 // 下一次检查时按当前权益重新定今天的开盘
			z := decimal.Zero
			m.emit(Event{Kind: "mark", Account: a.id, Peak: &z})
			if a.streak != 0 {
				m.emit(Event{Kind: "streak", Account: a.id, Streak: 0, Seen: a.seen})
			}
		}
	}
}

// ---------- 每秒检查 ----------

// Run 每秒检查所有账户、写库。
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			m.Flush()
			return
		case <-t.C:
			m.Tick()
			m.Flush()
		}
	}
}

// Tick 检查所有账户。
func (m *Manager) Tick() {
	m.mu.Lock()
	ids := make([]int64, 0, len(m.accts))
	for id := range m.accts {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		m.check(id)
	}
}

// check 先在锁外读引擎，再进锁算，出来再撤单和提醒。
func (m *Manager) check(id int64) {
	m.mu.Lock()
	a := m.accts[id]
	if a == nil || a.user == 0 { // 已归档的账户只在事件里，不再检查
		m.mu.Unlock()
		return
	}
	seen := a.seen
	m.mu.Unlock()
	b, err := m.Trader.Brief(id)
	if err != nil {
		return
	}
	closes, last := m.Trader.ClosedTrades(id, seen)
	kept := closes[:0:0]
	for _, c := range closes {
		if m.AIManaged == nil || !m.AIManaged(id, c.Instrument) {
			kept = append(kept, c)
		}
	}
	m.mu.Lock()
	locked, unlocked := m.evaluate(id, b.Equity.Sub(b.Transfers), kept, last) // 划转不是赚亏
	m.mu.Unlock()
	if locked != "" {
		if _, err := m.Trader.CancelOpening(id, "DISCIPLINE"); err != nil && m.Log != nil {
			m.Log.Debug("纪律锁：撤开仓单", "account", id, "err", err)
		}
		_ = m.Trader.Note(id, "discipline_lock", "", locked)
	}
	if unlocked != "" {
		_ = m.Trader.Note(id, "discipline_unlock", "", unlocked)
	}
}

// evaluate 更新状态、判断规则。返回这次锁了哪条、解了哪条。持锁调用，不碰引擎。
func (m *Manager) evaluate(id int64, equity decimal.Decimal, closes []trading.ClosedTrade, last int64) (locked, unlocked string) {
	a := m.accts[id]
	if a == nil {
		return "", ""
	}
	u := m.user(a.user)
	now := m.now()
	day := dayKey(now, u.tz)
	if a.day == 0 {
		closes = nil // 第一次看这个账户，或者刚重新开启：以前的亏损单不算连亏
	}
	if a.day != day {
		eq := equity
		m.emit(Event{Kind: "day", Account: id, Day: day, Open: &eq})
	}
	// 到时间解开
	if l := a.lock; l != nil {
		switch {
		case l.UnlockAt > 0 && now >= l.UnlockAt:
			unlocked = l.Rule
			if l.Rule == RuleDrawdown {
				eq := equity
				m.emit(Event{Kind: "mark", Account: id, Peak: &eq})
			}
			m.emit(Event{Kind: "unlock", Account: id, Rule: l.Rule, By: "password", Day: day})
		case l.Until > 0 && now >= l.Until:
			unlocked = l.Rule
			m.emit(Event{Kind: "unlock", Account: id, Rule: l.Rule, By: "time"})
		}
	}
	m.track(a, equity, now)
	// 连亏
	if last != a.seen {
		s := a.streak
		small := equity.Mul(u.rules.MinLoss).Abs() // 比这个小的盈亏当没发生（D107：试单亏几毛钱不该锁人）
		for _, c := range closes {
			if c.Net.Abs().LessThan(small) {
				continue
			}
			switch c.Net.Sign() {
			case -1:
				s++
			case 1:
				s = 0
			}
		}
		m.emit(Event{Kind: "streak", Account: id, Streak: s, Seen: last})
	}
	if !u.enabled || a.lock != nil {
		return "", unlocked
	}
	r := u.rules
	if a.dayOpen.Sign() <= 0 {
		return "", unlocked
	}
	pnl := equity.Sub(a.dayOpen)
	R := a.dayOpen.Mul(r.PerTrade)
	lock := func(rule string, until int64, value, limit decimal.Decimal) {
		m.emit(Event{Kind: "lock", Account: id, Lock: &Lock{Rule: rule, Since: now, Until: until, Value: value.Round(4), Limit: limit.Round(4)}})
		locked = rule
	}
	free := func(rule string) bool { return a.suspended[rule] != day }
	switch {
	case free(RuleDaily) && pnl.LessThanOrEqual(a.dayOpen.Mul(r.DailyLoss).Neg()):
		lock(RuleDaily, nextDay(now, u.tz), pnl, a.dayOpen.Mul(r.DailyLoss).Neg())
	case free(RuleGiveBack) && a.dayPeak.Sub(a.dayOpen).GreaterThanOrEqual(R.Mul(r.PeakR)) && pnl.LessThanOrEqual(R.Mul(r.StopR)):
		lock(RuleGiveBack, nextDay(now, u.tz), pnl, R.Mul(r.StopR))
	case a.peak.Sign() > 0 && a.peak.Sub(equity).Div(a.peak).GreaterThanOrEqual(r.MaxDrawdown):
		lock(RuleDrawdown, 0, a.peak.Sub(equity).Div(a.peak), r.MaxDrawdown)
	case a.streak >= r.LossStreak:
		lock(RuleStreak, now+int64(r.CooldownMin)*60000, decimal.NewFromInt(int64(a.streak)), decimal.NewFromInt(int64(r.LossStreak)))
	}
	return locked, unlocked
}

// track 记下最高权益和今天的最高权益。写事件有节流：涨得多或者过了一分钟才写。
func (m *Manager) track(a *acctState, equity decimal.Decimal, now int64) {
	peakUp := equity.GreaterThan(a.peak)
	dayUp := equity.GreaterThan(a.dayPeak)
	if !peakUp && !dayUp {
		return
	}
	if peakUp {
		a.peak = equity
	}
	if dayUp {
		a.dayPeak = equity
	}
	thin := decimal.RequireFromString("1.001")
	big := a.peak.GreaterThan(a.markPeak.Mul(thin)) || a.dayPeak.GreaterThan(a.markDay.Mul(thin))
	if big || now-a.markedAt > 60000 {
		p, d := a.peak, a.dayPeak
		m.emit(Event{Kind: "mark", Account: a.id, Peak: &p, DayPeak: &d})
	}
}

// ---------- 下单预检 ----------

// Guard 引擎开仓和加仓前调用（持有引擎的锁，这里不能碰引擎）。
func (m *Manager) Guard(p trading.PreOrder) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.accts[p.Account]
	if a == nil {
		return nil // 系统账户
	}
	u := m.user(a.user)
	if !u.enabled {
		return nil
	}
	if l := a.lock; l != nil {
		m.emit(Event{Kind: "reject", Account: p.Account, Rule: l.Rule, Inst: p.Instrument, Code: "DISCIPLINE_LOCKED"})
		kv := []string{"rule", l.Rule}
		if l.Until > 0 {
			kv = append(kv, "until", strconv.FormatInt(l.Until, 10))
		}
		if l.UnlockAt > 0 {
			kv = append(kv, "unlock_at", strconv.FormatInt(l.UnlockAt, 10))
		}
		return trading.NewReject("DISCIPLINE_LOCKED", kv...)
	}
	// 每笔风险只查手动单；没带止损的不拦（D80）
	if (p.Source == "manual" || p.Source == "cond") && p.Stop.Sign() > 0 && p.Price.Sign() > 0 {
		risk := p.Price.Sub(p.Stop).Abs().Mul(p.Qty)
		limit := p.Equity.Mul(u.rules.PerTrade)
		if risk.GreaterThan(limit) {
			m.emit(Event{Kind: "reject", Account: p.Account, Rule: "per_trade", Inst: p.Instrument, Code: "DISCIPLINE_RISK"})
			return trading.NewReject("DISCIPLINE_RISK", "risk", risk.Round(2).String(), "limit", limit.Round(2).String(),
				"pct", u.rules.PerTrade.Mul(decimal.NewFromInt(100)).String())
		}
	}
	return nil
}

// ---------- 用户操作 ----------

func (m *Manager) lockedAny(user int64) bool {
	for _, a := range m.accts {
		if a.user == user && a.lock != nil {
			return true
		}
	}
	return false
}

// SetEnabled 打开随时可以；关闭要先验过二级密码（由 HTTP 层验）。锁定中不能关（4.11）。
func (m *Manager) SetEnabled(user int64, on bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u := m.user(user)
	if u.enabled == on {
		return nil
	}
	if !on && m.lockedAny(user) {
		return errOf(409, "DISCIPLINE_IS_LOCKED")
	}
	by := "password"
	if on {
		by = "user"
	}
	m.emit(Event{Kind: "enabled", User: user, On: on, By: by})
	if on {
		m.rebase(user)
	}
	return nil
}

// NeedsPassword 这次改规则要不要二级密码：放宽才要。
func (m *Manager) NeedsPassword(user int64, r Rules) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return r.LooserThan(m.user(user).rules)
}

// SetRules 改规则。放宽的话调用前已经验过二级密码。锁定中不能放宽（4.12）。
func (m *Manager) SetRules(user int64, r Rules) error {
	if err := r.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if r.LooserThan(m.user(user).rules) && m.lockedAny(user) {
		return errOf(409, "DISCIPLINE_IS_LOCKED")
	}
	m.emit(Event{Kind: "settings", User: user, Rules: &r})
	return nil
}

// SetTZ 记下用户的时区（比 UTC 早多少分钟）。
func (m *Manager) SetTZ(user int64, tz int) {
	if tz < -720 || tz > 840 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.user(user).tz != tz {
		m.emit(Event{Kind: "tz", User: user, TZ: tz})
	}
}

// CanUnlock 能不能申请解锁。放在验密码之前，密码错不算次数（4.19）。
func (m *Manager) CanUnlock(user, acct int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.accts[acct]
	if a == nil || a.user != user {
		return errOf(404, "NOT_FOUND")
	}
	if a.lock == nil {
		return errOf(409, "DISCIPLINE_NOT_LOCKED")
	}
	if a.lock.UnlockAt > 0 {
		return errOf(409, "UNLOCK_PENDING")
	}
	if a.unlockDay == dayKey(m.now(), m.user(user).tz) {
		return errOf(409, "UNLOCK_USED")
	}
	return nil
}

// RequestUnlock 申请解锁：10 分钟后生效，当天只能一次。调用前已经验过二级密码。
func (m *Manager) RequestUnlock(user, acct int64) error {
	if err := m.CanUnlock(user, acct); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	m.emit(Event{Kind: "unlock_req", Account: acct, At: now + UnlockWait.Milliseconds(), Day: dayKey(now, m.user(user).tz), By: "password"})
	return nil
}

// ---------- 查询 ----------

// AcctView 一个账户的纪律状态（仪表用，4.25）。
type AcctView struct {
	Account    int64           `json:"account"`
	DayOpen    decimal.Decimal `json:"day_open"`
	DayPeak    decimal.Decimal `json:"day_peak"`
	Peak       decimal.Decimal `json:"peak"`
	Streak     int             `json:"streak"`
	Lock       *Lock           `json:"lock,omitempty"`
	UnlockUsed bool            `json:"unlock_used"`
	NextDay    int64           `json:"next_day"`
}

// View 这个用户的规则和所有账户的状态。
type View struct {
	Enabled  bool       `json:"enabled"`
	Rules    Rules      `json:"rules"`
	Defaults Rules      `json:"defaults"`
	TZ       int        `json:"tz"`
	Accounts []AcctView `json:"accounts"`
}

// View 给界面看。
func (m *Manager) View(user int64) *View {
	m.mu.Lock()
	defer m.mu.Unlock()
	u := m.user(user)
	now := m.now()
	v := &View{Enabled: u.enabled, Rules: u.rules, Defaults: Default(), TZ: u.tz, Accounts: []AcctView{}}
	for _, a := range m.accts {
		if a.user != user {
			continue
		}
		av := AcctView{Account: a.id, DayOpen: a.dayOpen, DayPeak: a.dayPeak, Peak: a.peak, Streak: a.streak,
			UnlockUsed: a.unlockDay == dayKey(now, u.tz), NextDay: nextDay(now, u.tz)}
		if a.lock != nil {
			l := *a.lock
			av.Lock = &l
		}
		v.Accounts = append(v.Accounts, av)
	}
	sort.Slice(v.Accounts, func(i, j int) bool { return v.Accounts[i].Account < v.Accounts[j].Account })
	return v
}

// History 这个账户最近的锁定、解锁、拒单记录，新的在前（4.20）。
func (m *Manager) History(user, acct int64) ([]Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.accts[acct]
	if a == nil || a.user != user {
		return nil, errOf(404, "NOT_FOUND")
	}
	out := append(append([]Event{}, m.history[acct]...), m.userLog[user]...)
	sort.Slice(out, func(i, j int) bool { return out[i].Seq > out[j].Seq })
	if len(out) > 100 {
		out = out[:100]
	}
	return out, nil
}

// PerTradeLimit 每笔风险上限；纪律锁关着时是 0。复盘标"超过每笔风险"用。
func (m *Manager) PerTradeLimit(user int64) float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	u := m.user(user)
	if !u.enabled {
		return 0
	}
	return u.rules.PerTrade.InexactFloat64()
}

// SpotGuard 现货：锁定时不能买，卖出算减仓，照做（D45）。
func (m *Manager) SpotGuard(acct int64, inst, side string) error {
	if side == "sell" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.accts[acct]
	if a == nil || !m.user(a.user).enabled || a.lock == nil {
		return nil
	}
	m.emit(Event{Kind: "reject", Account: acct, Rule: a.lock.Rule, Inst: inst, Code: "DISCIPLINE_LOCKED"})
	kv := []string{"rule", a.lock.Rule}
	if a.lock.Until > 0 {
		kv = append(kv, "until", strconv.FormatInt(a.lock.Until, 10))
	}
	return trading.NewReject("DISCIPLINE_LOCKED", kv...)
}

// LockErr 账户锁着就返回拒绝。开机器人、算法单之前先问一句（审查第 8 条：
// 以前先返回成功，第一笔单被拒才自动停，用户以为开上了）。
func (m *Manager) LockErr(acct int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.accts[acct]
	if a == nil || !m.user(a.user).enabled || a.lock == nil {
		return nil
	}
	m.emit(Event{Kind: "reject", Account: acct, Rule: a.lock.Rule, Code: "DISCIPLINE_LOCKED"})
	kv := []string{"rule", a.lock.Rule}
	if a.lock.Until > 0 {
		kv = append(kv, "until", strconv.FormatInt(a.lock.Until, 10))
	}
	if a.lock.UnlockAt > 0 {
		kv = append(kv, "unlock_at", strconv.FormatInt(a.lock.UnlockAt, 10))
	}
	return trading.NewReject("DISCIPLINE_LOCKED", kv...)
}

// UserOf 这个账户是谁的（引擎锁里调用：不能碰别的锁以外的东西）。
func (m *Manager) LockRule(acct int64) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a := m.accts[acct]; a != nil && a.lock != nil {
		return a.lock.Rule
	}
	return ""
}

// UserOf 这个账户是谁的。
func (m *Manager) UserOf(acct int64) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a := m.accts[acct]; a != nil {
		return a.user
	}
	return 0
}
