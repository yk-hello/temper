// Package algo 算法单（D83 第 12 条）：跟踪止损、TWAP、追价，加上一次下多张的分批挂单、多级止盈、一键反手；
// 18 第 9.5 条加了冰山单（一次只挂一小张，成交完再挂下一张）和按成交量比例下单（市场每成交 100，自己跟着成交几）。
// 都是用户自己下的，所以来源写 manual：纪律锁照样查，机器人照样让路。
package algo

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"qs/internal/auth"
	"qs/internal/book"
	"qs/internal/trading"
)

var (
	zero = decimal.Zero
	one  = decimal.NewFromInt(1)
	hund = decimal.NewFromInt(100)
)

// 种类和状态。
const (
	Trail   = "trail"
	TWAP    = "twap"
	Chase   = "chase"
	Iceberg = "iceberg"
	POV     = "pov"

	StRunning = "running"
	StDone    = "done"
	StStopped = "stopped"
)

// Rej 拒绝。
type Rej struct {
	Code   string
	Params map[string]string
}

func (r *Rej) Error() string { return r.Code }

// RejectCode 见 trading.Reject。
func (r *Rej) RejectCode() string { return r.Code }

func rej(code string, kv ...string) *Rej {
	r := &Rej{Code: code, Params: map[string]string{}}
	for i := 0; i+1 < len(kv); i += 2 {
		r.Params[kv[i]] = kv[i+1]
	}
	return r
}

// Algo 一张算法单。
type Algo struct {
	ID         int64           `json:"id"`
	Account    int64           `json:"account"`
	User       int64           `json:"-"`
	Kind       string          `json:"kind"`
	Instrument string          `json:"instrument"`
	Status     string          `json:"status"`
	Reason     string          `json:"reason,omitempty"`
	Created    int64           `json:"created"`
	Ended      int64           `json:"ended,omitempty"`
	Side       string          `json:"side"`
	Qty        decimal.Decimal `json:"qty"`
	Filled     decimal.Decimal `json:"filled"`
	Reduce     bool            `json:"reduce_only"`

	// 跟踪止损
	Callback decimal.Decimal `json:"callback"` // 回撤多少（%）
	Activate decimal.Decimal `json:"activate"` // 价格到这里才开始跟，0 = 马上
	Best     decimal.Decimal `json:"best"`     // 开始以来最有利的价格
	Stop     decimal.Decimal `json:"stop"`     // 现在的止损价
	Live     bool            `json:"live"`     // 已经开始跟了

	// TWAP
	Slices   int             `json:"slices"`
	Done     int             `json:"done"`
	Interval int64           `json:"interval"`
	NextAt   int64           `json:"next_at,omitempty"`
	LimitPx  decimal.Decimal `json:"limit_px"`
	Skipped  int             `json:"skipped"`

	// 追价
	OrderID string          `json:"order,omitempty"`
	Start   decimal.Decimal `json:"start"`
	MaxDist decimal.Decimal `json:"max_dist"` // 最多追多远（%）
	Moves   int             `json:"moves"`

	// 冰山单：总数量拆成 Slices 张，一次只挂一张（OrderID），挂在 Price；Base 是前面几张已经成交的
	Price decimal.Decimal `json:"price"`
	Show  decimal.Decimal `json:"show"`
	Base  decimal.Decimal `json:"base"`

	// 按成交量比例：每 Interval 看一次市场成交了多少（Seen），按 Pct% 跟着下市价单；Owed 是该下还没下的（不够最小金额时攒着）
	Pct   decimal.Decimal `json:"pct"`
	Seen  decimal.Decimal `json:"seen"`
	Owed  decimal.Decimal `json:"owed"`
	VolAt decimal.Decimal `json:"vol_at"`
	Until int64           `json:"until,omitempty"`

	dirty bool
}

// Manager 管理器。
type Manager struct {
	E     *trading.Engine
	Books *book.Store
	Venue string
	Log   *slog.Logger
	Now   func() int64
	// Locked 纪律锁锁着就返回拒绝。没接时不查
	Locked func(acct int64) error

	pool *pgxpool.Pool
	mu   sync.Mutex
	all  map[int64]*Algo
	next int64

	vmu sync.Mutex
	vol map[string]decimal.Decimal // 每个品种启动以来市场一共成交了多少（按成交量比例下单用）
}

// New 新建。
func New(e *trading.Engine, books *book.Store, venue string, log *slog.Logger) *Manager {
	m := &Manager{E: e, Books: books, Venue: venue, Log: log, all: map[int64]*Algo{}, next: 1, vol: map[string]decimal.Decimal{}}
	if books != nil {
		books.OnTrade(func(v, inst string, _, qty decimal.Decimal, _ bool, _ int64) {
			if v != venue {
				return
			}
			m.vmu.Lock()
			m.vol[inst] = m.vol[inst].Add(qty)
			m.vmu.Unlock()
		})
	}
	return m
}

func (m *Manager) volOf(inst string) decimal.Decimal {
	m.vmu.Lock()
	defer m.vmu.Unlock()
	return m.vol[inst]
}

// lockedFor 锁定期间不开会加仓的算法单（只减仓的照常，和手动下单一样，D07）。
func (m *Manager) lockedFor(acct int64, inst, side string, qty decimal.Decimal, reduce bool) error {
	if m.Locked == nil || reduce {
		return nil
	}
	if v, err := m.E.View(acct); err == nil {
		for _, p := range v.Positions {
			if p.Instrument == inst && !p.Qty.IsZero() && (p.Side == "long") != (side == "buy") && qty.LessThanOrEqual(p.Qty) { // 视图里的数量是绝对值
				return nil
			}
		}
	}
	return m.Locked(acct)
}

func (m *Manager) now() int64 {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now().UnixMilli()
}

func (m *Manager) quote(inst string) (bid, ask decimal.Decimal, ok bool) {
	b, ok := m.Books.Book(m.Venue, inst)
	if !ok || len(b.Bids) == 0 || len(b.Asks) == 0 {
		return zero, zero, false
	}
	return b.Bids[0].Px, b.Asks[0].Px, true
}

func (m *Manager) markOf(inst string) (decimal.Decimal, bool) {
	if t, ok := m.Books.Ticker(m.Venue, inst); ok && t.Mark.IsPositive() {
		return t.Mark, true
	}
	bid, ask, ok := m.quote(inst)
	if !ok {
		return zero, false
	}
	return bid.Add(ask).Div(decimal.NewFromInt(2)), true
}

func (m *Manager) posOf(acct int64, inst string) decimal.Decimal {
	b, err := m.E.Brief(acct)
	if err != nil {
		return zero
	}
	return b.Pos[inst].Qty
}

// ---------- 启动 ----------

// StartTrail 跟踪止损：持仓走到有利方向后，从最好的价格回撤 callback% 就平掉。
func (m *Manager) StartTrail(user, acct int64, inst string, callback, activate decimal.Decimal) (*Algo, error) {
	if !callback.IsPositive() || callback.GreaterThan(decimal.NewFromInt(50)) {
		return nil, rej("ALGO_BAD_CALLBACK")
	}
	pos := m.posOf(acct, inst)
	if pos.IsZero() {
		return nil, rej("NO_POSITION")
	}
	mk, ok := m.markOf(inst)
	if !ok {
		return nil, rej("NO_BOOK")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, x := range m.all {
		if x.Account == acct && x.Instrument == inst && x.Kind == Trail && x.Status == StRunning {
			return nil, rej("ALGO_TRAIL_EXISTS")
		}
	}
	a := &Algo{ID: m.next, Account: acct, User: user, Kind: Trail, Instrument: inst, Status: StRunning, Created: m.now(), Qty: pos.Abs(), Callback: callback, Activate: activate, Reduce: true, Side: "sell", dirty: true}
	if pos.IsNegative() {
		a.Side = "buy"
	}
	a.Live = !activate.IsPositive()
	if a.Live {
		a.Best = mk
		a.Stop = m.trailStop(a)
	}
	m.next++
	m.all[a.ID] = a
	cp := *a
	return &cp, nil
}

func (m *Manager) trailStop(a *Algo) decimal.Decimal {
	d := a.Callback.Div(hund)
	if a.Side == "sell" { // 多单：跌回去就平
		return a.Best.Mul(one.Sub(d))
	}
	return a.Best.Mul(one.Add(d))
}

// StartTWAP 把一笔大单拆开，在一段时间内均匀下完。
func (m *Manager) StartTWAP(user, acct int64, inst, side string, qty decimal.Decimal, minutes int, limit decimal.Decimal, reduce bool) (*Algo, error) {
	if side != "buy" && side != "sell" {
		return nil, rej("BAD_REQUEST")
	}
	if minutes < 1 || minutes > 1440 {
		return nil, rej("ALGO_BAD_MINUTES")
	}
	if err := m.lockedFor(acct, inst, side, qty, reduce); err != nil {
		return nil, err
	}
	spec, ok := m.E.Specs.Get(inst)
	if !ok {
		return nil, rej("UNKNOWN_INSTRUMENT")
	}
	mk, ok := m.markOf(inst)
	if !ok {
		return nil, rej("NO_BOOK")
	}
	if !qty.IsPositive() {
		return nil, rej("BAD_QTY")
	}
	slices := minutes * 2 // 默认 30 秒一片
	// 每片不能小于最小下单额：片数按这个往下减
	minQty := spec.MinNotional.Div(mk)
	if v := qty.Div(minQty).Floor().IntPart(); int64(slices) > v {
		slices = int(v)
	}
	if slices < 2 {
		return nil, rej("ALGO_TWAP_TOO_SMALL", "min", spec.MinNotional.String())
	}
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	a := &Algo{ID: m.next, Account: acct, User: user, Kind: TWAP, Instrument: inst, Status: StRunning, Created: now, Side: side, Qty: qty, Slices: slices, Interval: int64(minutes) * 60_000 / int64(slices), NextAt: now, LimitPx: limit, Reduce: reduce, dirty: true}
	m.next++
	m.all[a.ID] = a
	cp := *a
	return &cp, nil
}

// StartChase 追价：一直挂在最优价，价格跑了就跟着改，直到成交或超过最大距离。
func (m *Manager) StartChase(user, acct int64, inst, side string, qty, maxDist decimal.Decimal, reduce bool) (*Algo, error) {
	if side != "buy" && side != "sell" {
		return nil, rej("BAD_REQUEST")
	}
	if !maxDist.IsPositive() || maxDist.GreaterThan(decimal.NewFromInt(20)) {
		return nil, rej("ALGO_BAD_DIST")
	}
	if err := m.lockedFor(acct, inst, side, qty, reduce); err != nil {
		return nil, err
	}
	bid, ask, ok := m.quote(inst)
	if !ok {
		return nil, rej("NO_BOOK")
	}
	px := bid
	if side == "sell" {
		px = ask
	}
	if v, ok := m.E.RoundPrice(inst, side == "buy", px); ok {
		px = v
	}
	o, err := m.E.PlaceAs(acct, trading.OrderReq{Instrument: inst, Side: side, Type: trading.Limit, TIF: trading.GTC, Price: px, Qty: qty, ReduceOnly: reduce}, "manual", "chase")
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	a := &Algo{ID: m.next, Account: acct, User: user, Kind: Chase, Instrument: inst, Status: StRunning, Created: m.now(), Side: side, Qty: qty, OrderID: o.ID, Start: px, MaxDist: maxDist, Reduce: reduce, dirty: true}
	m.next++
	m.all[a.ID] = a
	cp := *a
	return &cp, nil
}

// StartIceberg 冰山单（18 第 9.5 条）：总数量拆成 count 张，在 price 一次只挂一张，成交完再挂下一张，盘口上只看得到一小张。
func (m *Manager) StartIceberg(user, acct int64, inst, side string, qty, price decimal.Decimal, count int, reduce bool) (*Algo, error) {
	if side != "buy" && side != "sell" {
		return nil, rej("BAD_REQUEST")
	}
	if count < 2 || count > 50 {
		return nil, rej("ALGO_ICE_COUNT")
	}
	if !qty.IsPositive() {
		return nil, rej("BAD_QTY")
	}
	if !price.IsPositive() {
		return nil, rej("PRICE_REQUIRED")
	}
	if err := m.lockedFor(acct, inst, side, qty, reduce); err != nil {
		return nil, err
	}
	spec, ok := m.E.Specs.Get(inst)
	if !ok {
		return nil, rej("UNKNOWN_INSTRUMENT")
	}
	if v, ok := m.E.RoundPrice(inst, side == "buy", price); ok {
		price = v
	}
	show := qty.Div(decimal.NewFromInt(int64(count))).Div(spec.Step).Floor().Mul(spec.Step)
	if !show.IsPositive() || show.Mul(price).LessThan(spec.MinNotional) {
		return nil, rej("ALGO_ICE_TOO_SMALL", "min", spec.MinNotional.String())
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	a := &Algo{ID: m.next, Account: acct, User: user, Kind: Iceberg, Instrument: inst, Status: StRunning, Created: m.now(), Side: side, Qty: qty, Price: price, Show: show, Slices: count, Reduce: reduce, dirty: true}
	if err := m.placeIce(a, spec.Step, spec.MinNotional); err != nil {
		return nil, err
	}
	m.next++
	m.all[a.ID] = a
	cp := *a
	return &cp, nil
}

// placeIce 挂下一张：最后一张、或者剩的不够一张最小金额时，把剩下的全挂上
func (m *Manager) placeIce(a *Algo, step, minNotional decimal.Decimal) error {
	remain := a.Qty.Sub(a.Base)
	q := decimal.Min(a.Show, remain)
	if rest := remain.Sub(q); a.Done >= a.Slices-1 || rest.Mul(a.Price).LessThan(minNotional) { // 最后一张带上零头
		q = remain.Div(step).Floor().Mul(step)
	}
	o, err := m.E.PlaceAs(a.Account, trading.OrderReq{Instrument: a.Instrument, Side: a.Side, Type: trading.Limit, TIF: trading.GTC, Price: a.Price, Qty: q, ReduceOnly: a.Reduce}, "manual", "iceberg")
	if err != nil {
		return err
	}
	a.OrderID = o.ID
	a.Done++
	a.Filled = a.Base.Add(o.Filled)
	a.dirty = true
	return nil
}

func (m *Manager) stepIceberg(a *Algo) {
	o := m.E.OrderOf(a.Account, a.OrderID)
	if o == nil {
		return
	}
	if f := a.Base.Add(o.Filled); !f.Equal(a.Filled) {
		a.Filled, a.dirty = f, true
	}
	switch o.Status {
	case trading.StFilled:
	case trading.StCanceled, trading.StExpired: // 挂着的那张被撤了（手动撤、全撤）：整张冰山单停下
		a.Base = a.Filled
		m.finish(a, StStopped, "ORDER_CANCELED")
		return
	default:
		return
	}
	a.Base = a.Filled
	spec, ok := m.E.Specs.Get(a.Instrument)
	if !ok {
		return
	}
	if a.Qty.Sub(a.Base).LessThan(spec.Step) {
		m.finish(a, StDone, "")
		return
	}
	if err := m.placeIce(a, spec.Step, spec.MinNotional); err != nil {
		m.finish(a, StStopped, codeOf(err))
	}
}

// StartPOV 按成交量比例下单（18 第 9.5 条）：市场每成交 100，自己跟着下 pct；最长 minutes 分钟，到时没下完就停。
// limit 不为 0 时，价格超过它的那一段不跟（也不补）。
func (m *Manager) StartPOV(user, acct int64, inst, side string, qty, pct decimal.Decimal, minutes int, limit decimal.Decimal, reduce bool) (*Algo, error) {
	if side != "buy" && side != "sell" {
		return nil, rej("BAD_REQUEST")
	}
	if pct.LessThan(one) || pct.GreaterThan(decimal.NewFromInt(50)) {
		return nil, rej("ALGO_POV_PCT")
	}
	if minutes < 1 || minutes > 1440 {
		return nil, rej("ALGO_BAD_MINUTES")
	}
	if !qty.IsPositive() {
		return nil, rej("BAD_QTY")
	}
	if err := m.lockedFor(acct, inst, side, qty, reduce); err != nil {
		return nil, err
	}
	spec, ok := m.E.Specs.Get(inst)
	if !ok {
		return nil, rej("UNKNOWN_INSTRUMENT")
	}
	mk, ok := m.markOf(inst)
	if !ok {
		return nil, rej("NO_BOOK")
	}
	if qty.Mul(mk).LessThan(spec.MinNotional) {
		return nil, rej("ALGO_POV_TOO_SMALL", "min", spec.MinNotional.String())
	}
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	a := &Algo{ID: m.next, Account: acct, User: user, Kind: POV, Instrument: inst, Status: StRunning, Created: now, Side: side, Qty: qty, Pct: pct,
		Interval: povEvery, NextAt: now + povEvery, Until: now + int64(minutes)*60_000, LimitPx: limit, VolAt: m.volOf(inst), Reduce: reduce, dirty: true}
	m.next++
	m.all[a.ID] = a
	cp := *a
	return &cp, nil
}

// povEvery 每 10 秒看一次市场成交了多少（毫秒；测试里改小）
var povEvery int64 = 10_000

func (m *Manager) stepPOV(a *Algo, now int64) {
	if now < a.NextAt && now < a.Until {
		return
	}
	spec, ok := m.E.Specs.Get(a.Instrument)
	if !ok {
		return
	}
	a.NextAt = now + a.Interval
	a.dirty = true
	cur := m.volOf(a.Instrument)
	d := cur.Sub(a.VolAt)
	if d.IsNegative() { // 重启过：计数从零开始
		d = cur
	}
	a.VolAt = cur
	a.Seen = a.Seen.Add(d)
	a.Owed = a.Owed.Add(d.Mul(a.Pct).Div(hund))
	if bid, ask, ok := m.quote(a.Instrument); ok {
		px := ask
		if a.Side == "sell" {
			px = bid
		}
		remain := a.Qty.Sub(a.Filled)
		q := decimal.Min(a.Owed, remain).Div(spec.Step).Floor().Mul(spec.Step)
		// 剩下的不够一笔最小金额：这一笔把它带上
		if rest := remain.Sub(q); q.IsPositive() && rest.Mul(px).LessThan(spec.MinNotional) {
			q = remain.Div(spec.Step).Floor().Mul(spec.Step)
		}
		switch {
		case a.LimitPx.IsPositive() && ((a.Side == "buy" && px.GreaterThan(a.LimitPx)) || (a.Side == "sell" && px.LessThan(a.LimitPx))):
			a.Skipped++
			a.Owed = zero // 超过限价的这一段不跟，价格回来也不补（补了就不是按比例了）
		case q.IsPositive() && q.Mul(px).GreaterThanOrEqual(spec.MinNotional):
			o, err := m.E.PlaceAs(a.Account, trading.OrderReq{Instrument: a.Instrument, Side: a.Side, Type: trading.Market, Qty: q, ReduceOnly: a.Reduce}, "manual", "pov")
			if err != nil {
				m.finish(a, StStopped, codeOf(err))
				return
			}
			a.Filled = a.Filled.Add(o.Filled)
			a.Owed = decimal.Max(zero, a.Owed.Sub(q))
			a.Done++
		}
	}
	if a.Qty.Sub(a.Filled).LessThan(spec.Step) {
		m.finish(a, StDone, "")
		return
	}
	if now >= a.Until {
		m.finish(a, StDone, "TIMEOUT")
	}
}

// ---------- 一次性 ----------

// Scale 分批挂单：在一段价格里挂 count 张。skew：equal 每张一样，up 越往上越多，down 越往下越多。
func (m *Manager) Scale(acct int64, inst, side string, lower, upper, qty decimal.Decimal, count int, skew string, reduce bool) ([]string, error) {
	spec, ok := m.E.Specs.Get(inst)
	if !ok {
		return nil, rej("UNKNOWN_INSTRUMENT")
	}
	if count < 2 || count > 20 {
		return nil, rej("ALGO_BAD_COUNT")
	}
	if !lower.IsPositive() || !upper.GreaterThan(lower) {
		return nil, rej("GRID_BAD_RANGE")
	}
	if !qty.IsPositive() {
		return nil, rej("BAD_QTY")
	}
	weights := make([]decimal.Decimal, count)
	sum := zero
	for i := range weights {
		w := one
		switch skew {
		case "up":
			w = decimal.NewFromInt(int64(i + 1))
		case "down":
			w = decimal.NewFromInt(int64(count - i))
		}
		weights[i] = w
		sum = sum.Add(w)
	}
	type leg struct{ px, q decimal.Decimal }
	legs := make([]leg, count)
	step := upper.Sub(lower).Div(decimal.NewFromInt(int64(count - 1)))
	for i := range legs {
		px := lower.Add(step.Mul(decimal.NewFromInt(int64(i)))).Div(spec.Tick).Floor().Mul(spec.Tick)
		q := qty.Mul(weights[i]).Div(sum).Div(spec.Step).Floor().Mul(spec.Step)
		if q.Mul(px).LessThan(spec.MinNotional) {
			return nil, rej("ALGO_SCALE_TOO_SMALL", "min", spec.MinNotional.String())
		}
		legs[i] = leg{px, q}
	}
	ids := []string{}
	for _, l := range legs {
		o, err := m.E.PlaceAs(acct, trading.OrderReq{Instrument: inst, Side: side, Type: trading.Limit, TIF: trading.GTC, Price: l.px, Qty: l.q, ReduceOnly: reduce}, "manual", "scale")
		if err != nil {
			// 中途失败：已经挂上的撤掉，不留半截
			for _, id := range ids {
				_, _ = m.E.CancelAs(acct, id, "SCALE_FAILED")
			}
			return nil, err
		}
		ids = append(ids, o.ID)
	}
	return ids, nil
}

// Level 多级止盈的一档。
type Level struct {
	Px  decimal.Decimal `json:"px"`
	Pct decimal.Decimal `json:"pct"`
}

// MultiExit 分批止盈止损：到价平掉一部分。用只减仓的条件单，互不影响。
func (m *Manager) MultiExit(acct int64, inst string, levels []Level) ([]string, error) {
	if len(levels) == 0 || len(levels) > 5 {
		return nil, rej("ALGO_BAD_LEVELS")
	}
	spec, ok := m.E.Specs.Get(inst)
	if !ok {
		return nil, rej("UNKNOWN_INSTRUMENT")
	}
	pos := m.posOf(acct, inst)
	if pos.IsZero() {
		return nil, rej("NO_POSITION")
	}
	side := "sell"
	if pos.IsNegative() {
		side = "buy"
	}
	total := zero
	for _, l := range levels {
		if !l.Px.IsPositive() || !l.Pct.IsPositive() {
			return nil, rej("ALGO_BAD_LEVELS")
		}
		total = total.Add(l.Pct)
	}
	if total.GreaterThan(hund) {
		return nil, rej("ALGO_LEVELS_OVER")
	}
	ids := []string{}
	for _, l := range levels {
		q := pos.Abs().Mul(l.Pct).Div(hund).Div(spec.Step).Floor().Mul(spec.Step)
		if !q.IsPositive() {
			return nil, rej("ALGO_LEVELS_OVER")
		}
		o, err := m.E.PlaceAs(acct, trading.OrderReq{Instrument: inst, Side: side, Type: trading.CondMarket, TriggerPx: l.Px, Qty: q, ReduceOnly: true}, "manual", "exit")
		if err != nil {
			for _, id := range ids {
				_, _ = m.E.CancelAs(acct, id, "EXIT_FAILED")
			}
			return nil, err
		}
		ids = append(ids, o.ID)
	}
	return ids, nil
}

// Reverse 一键反手：市价平掉再开同样大小的反向仓位。
func (m *Manager) Reverse(acct int64, inst string) (*trading.Order, error) {
	pos := m.posOf(acct, inst)
	if pos.IsZero() {
		return nil, rej("NO_POSITION")
	}
	side := "sell"
	if pos.IsNegative() {
		side = "buy"
	}
	return m.E.PlaceAs(acct, trading.OrderReq{Instrument: inst, Side: side, Type: trading.Market, Qty: pos.Abs().Mul(decimal.NewFromInt(2))}, "manual", "reverse")
}

// ---------- 运行 ----------

func (m *Manager) finish(a *Algo, status, reason string) {
	a.Status, a.Reason, a.Ended = status, reason, m.now()
	a.dirty = true
}

func codeOf(err error) string {
	type coded interface{ RejectCode() string }
	var c coded
	if errors.As(err, &c) {
		return c.RejectCode()
	}
	return "ORDER_FAILED"
}

func (m *Manager) stepTrail(a *Algo) {
	pos := m.posOf(a.Account, a.Instrument)
	if pos.IsZero() || (a.Side == "sell") != pos.IsPositive() {
		m.finish(a, StDone, "POSITION_GONE")
		return
	}
	mk, ok := m.markOf(a.Instrument)
	if !ok {
		return
	}
	better := func(p, q decimal.Decimal) bool {
		if a.Side == "sell" {
			return p.GreaterThan(q)
		}
		return p.LessThan(q)
	}
	if !a.Live {
		if (a.Side == "sell" && mk.GreaterThanOrEqual(a.Activate)) || (a.Side == "buy" && mk.LessThanOrEqual(a.Activate)) {
			a.Live, a.Best = true, mk
			a.Stop = m.trailStop(a)
			a.dirty = true
		}
		return
	}
	if better(mk, a.Best) {
		a.Best = mk
		a.Stop = m.trailStop(a)
		a.dirty = true
		return
	}
	if (a.Side == "sell" && mk.LessThanOrEqual(a.Stop)) || (a.Side == "buy" && mk.GreaterThanOrEqual(a.Stop)) {
		if _, err := m.E.CloseAs(a.Account, a.Instrument, "sl", "trail"); err != nil {
			m.finish(a, StStopped, codeOf(err))
			return
		}
		m.finish(a, StDone, "TRIGGERED")
	}
}

func (m *Manager) stepTWAP(a *Algo, now int64) {
	if now < a.NextAt {
		return
	}
	spec, ok := m.E.Specs.Get(a.Instrument)
	if !ok {
		return
	}
	bid, ask, ok := m.quote(a.Instrument)
	if !ok {
		return
	}
	a.NextAt = now + a.Interval
	a.dirty = true
	left := a.Slices - a.Done
	remain := a.Qty.Sub(a.Filled)
	q := remain.Div(decimal.NewFromInt(int64(left))).Div(spec.Step).Floor().Mul(spec.Step)
	if left == 1 {
		q = remain.Div(spec.Step).Floor().Mul(spec.Step)
	}
	a.Done++
	px := ask
	if a.Side == "sell" {
		px = bid
	}
	// 价格超过限价这一片不下，留给后面的片
	if a.LimitPx.IsPositive() && ((a.Side == "buy" && px.GreaterThan(a.LimitPx)) || (a.Side == "sell" && px.LessThan(a.LimitPx))) {
		a.Skipped++
	} else if q.IsPositive() {
		o, err := m.E.PlaceAs(a.Account, trading.OrderReq{Instrument: a.Instrument, Side: a.Side, Type: trading.Market, Qty: q, ReduceOnly: a.Reduce}, "manual", "twap")
		if err != nil {
			m.finish(a, StStopped, codeOf(err))
			return
		}
		a.Filled = a.Filled.Add(o.Filled)
	}
	if a.Done >= a.Slices {
		if a.Skipped > 0 {
			m.finish(a, StDone, "PARTIAL")
		} else {
			m.finish(a, StDone, "")
		}
	}
}

func (m *Manager) stepChase(a *Algo) {
	o := m.E.OrderOf(a.Account, a.OrderID)
	if o == nil {
		return
	}
	a.Filled = o.Filled
	switch o.Status {
	case trading.StFilled:
		m.finish(a, StDone, "")
		return
	case trading.StCanceled, trading.StExpired:
		m.finish(a, StStopped, "ORDER_CANCELED")
		return
	}
	bid, ask, ok := m.quote(a.Instrument)
	if !ok {
		return
	}
	best := bid
	if a.Side == "sell" {
		best = ask
	}
	if v, ok := m.E.RoundPrice(a.Instrument, a.Side == "buy", best); ok {
		best = v
	}
	if best.Equal(o.Price) {
		return
	}
	// 价格往有利方向走（买单时价格掉下来）也跟，但不超过最大距离
	dist := best.Sub(a.Start).Abs().Div(a.Start).Mul(hund)
	adverse := (a.Side == "buy" && best.GreaterThan(a.Start)) || (a.Side == "sell" && best.LessThan(a.Start))
	if adverse && dist.GreaterThan(a.MaxDist) {
		_, _ = m.E.CancelAs(a.Account, a.OrderID, "CHASE_TOO_FAR")
		m.finish(a, StStopped, "CHASE_TOO_FAR")
		return
	}
	if _, err := m.E.Amend(a.Account, a.OrderID, best, o.Qty, zero); err != nil {
		return // 这一轮改不了，下一轮再试
	}
	a.Moves++
	a.dirty = true
}

// Tick 跑一轮。
func (m *Manager) Tick() {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	for _, a := range m.all {
		if a.Status != StRunning {
			continue
		}
		switch a.Kind {
		case Trail:
			m.stepTrail(a)
		case TWAP:
			m.stepTWAP(a, now)
		case Chase:
			m.stepChase(a)
		case Iceberg:
			m.stepIceberg(a)
		case POV:
			m.stepPOV(a, now)
		}
		if a.dirty {
			m.save(a)
		}
	}
}

// Cancel 用户取消。
func (m *Manager) Cancel(user, acct, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.all[id]
	if a == nil || a.User != user || a.Account != acct {
		return rej("NOT_FOUND")
	}
	if a.Status != StRunning {
		return nil
	}
	if (a.Kind == Chase || a.Kind == Iceberg) && a.OrderID != "" {
		_, _ = m.E.CancelAs(acct, a.OrderID, "USER")
	}
	m.finish(a, StStopped, "USER")
	m.save(a)
	return nil
}

// List 账户的算法单：在跑的和最近结束的。
// Active 正在跑的算法单数量（系统控制台）。
func (m *Manager) Active() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, a := range m.all {
		if a.Status == "running" {
			n++
		}
	}
	return n
}

func (m *Manager) List(acct int64) []Algo {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Algo{}
	for _, a := range m.all {
		if a.Account == acct {
			out = append(out, *a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	if len(out) > 30 {
		out = out[:30]
	}
	return out
}

const schema = `
create table if not exists algo_orders (
  id      bigint primary key,
  usr     bigint not null,
  account bigint not null,
  status  text   not null,
  body    jsonb  not null
);
create index if not exists algo_orders_account on algo_orders (account);
`

// Attach 接数据库，读回来接着跑。
func (m *Manager) Attach(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, schema); err != nil {
		return err
	}
	m.pool = pool
	rows, err := pool.Query(ctx, `select usr, body from algo_orders order by id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	m.mu.Lock()
	defer m.mu.Unlock()
	for rows.Next() {
		var usr int64
		var raw []byte
		if rows.Scan(&usr, &raw) != nil {
			continue
		}
		a := &Algo{}
		if json.Unmarshal(raw, a) != nil {
			continue
		}
		a.User = usr
		m.all[a.ID] = a
		if a.ID >= m.next {
			m.next = a.ID + 1
		}
	}
	return rows.Err()
}

func (m *Manager) save(a *Algo) {
	a.dirty = false
	if m.pool == nil {
		return
	}
	raw, err := json.Marshal(a)
	if err != nil {
		return
	}
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := m.pool.Exec(c, `insert into algo_orders (id,usr,account,status,body) values ($1,$2,$3,$4,$5) on conflict (id) do update set status=excluded.status, body=excluded.body`, a.ID, a.User, a.Account, a.Status, raw); err != nil && m.Log != nil {
		m.Log.Warn("存算法单失败", "err", err)
	}
}

// Run 一直跑。
func (m *Manager) Run(ctx context.Context) {
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			m.Tick()
		}
	}
}

// Users 账户服务。
type Users interface {
	Require(http.HandlerFunc) http.HandlerFunc
	OwnsPaper(ctx context.Context, user, acct int64) bool
}

// Routes 挂接口。
func (m *Manager) Routes(mux *http.ServeMux, users Users, onManual func(acct int64, inst string)) {
	wj := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	we := func(w http.ResponseWriter, err error) {
		var re *Rej
		if errors.As(err, &re) {
			wj(w, 422, map[string]any{"code": re.Code, "params": re.Params})
			return
		}
		var tr *trading.Reject
		if errors.As(err, &tr) {
			wj(w, 422, map[string]any{"code": tr.Code, "params": tr.Params})
			return
		}
		wj(w, 500, map[string]string{"code": "INTERNAL_ERROR"})
	}
	r := func(pattern string, fn func(w http.ResponseWriter, q *http.Request, u *auth.User, acct int64)) {
		mux.HandleFunc(pattern, users.Require(func(w http.ResponseWriter, q *http.Request) {
			u := auth.UserFrom(q.Context())
			acct, err := strconv.ParseInt(q.PathValue("acct"), 10, 64)
			if err != nil || !users.OwnsPaper(q.Context(), u.ID, acct) {
				wj(w, 404, map[string]string{"code": "NOT_FOUND"})
				return
			}
			fn(w, q, u, acct)
		}))
	}
	read := func(w http.ResponseWriter, q *http.Request, v any) bool {
		if err := json.NewDecoder(http.MaxBytesReader(w, q.Body, 16<<10)).Decode(v); err != nil {
			wj(w, 400, map[string]string{"code": "BAD_REQUEST"})
			return false
		}
		return true
	}
	type req struct {
		Instrument string          `json:"instrument"`
		Side       string          `json:"side"`
		Qty        decimal.Decimal `json:"qty"`
		Callback   decimal.Decimal `json:"callback"`
		Activate   decimal.Decimal `json:"activate"`
		Minutes    int             `json:"minutes"`
		LimitPx    decimal.Decimal `json:"limit_px"`
		MaxDist    decimal.Decimal `json:"max_dist"`
		Lower      decimal.Decimal `json:"lower"`
		Upper      decimal.Decimal `json:"upper"`
		Count      int             `json:"count"`
		Skew       string          `json:"skew"`
		Reduce     bool            `json:"reduce_only"`
		Levels     []Level         `json:"levels"`
		Price      decimal.Decimal `json:"price"`
		Pct        decimal.Decimal `json:"pct"`
	}
	r("GET /api/algo/{acct}", func(w http.ResponseWriter, _ *http.Request, _ *auth.User, acct int64) {
		wj(w, 200, map[string]any{"algos": m.List(acct)})
	})
	r("POST /api/algo/{acct}/trail", func(w http.ResponseWriter, q *http.Request, u *auth.User, acct int64) {
		var in req
		if !read(w, q, &in) {
			return
		}
		a, err := m.StartTrail(u.ID, acct, in.Instrument, in.Callback, in.Activate)
		if err != nil {
			we(w, err)
			return
		}
		wj(w, 200, a)
	})
	r("POST /api/algo/{acct}/twap", func(w http.ResponseWriter, q *http.Request, u *auth.User, acct int64) {
		var in req
		if !read(w, q, &in) {
			return
		}
		a, err := m.StartTWAP(u.ID, acct, in.Instrument, in.Side, in.Qty, in.Minutes, in.LimitPx, in.Reduce)
		if err != nil {
			we(w, err)
			return
		}
		wj(w, 200, a)
	})
	r("POST /api/algo/{acct}/chase", func(w http.ResponseWriter, q *http.Request, u *auth.User, acct int64) {
		var in req
		if !read(w, q, &in) {
			return
		}
		a, err := m.StartChase(u.ID, acct, in.Instrument, in.Side, in.Qty, in.MaxDist, in.Reduce)
		if err != nil {
			we(w, err)
			return
		}
		wj(w, 200, a)
	})
	r("POST /api/algo/{acct}/iceberg", func(w http.ResponseWriter, q *http.Request, u *auth.User, acct int64) {
		var in req
		if !read(w, q, &in) {
			return
		}
		a, err := m.StartIceberg(u.ID, acct, in.Instrument, in.Side, in.Qty, in.Price, in.Count, in.Reduce)
		if err != nil {
			we(w, err)
			return
		}
		wj(w, 200, a)
	})
	r("POST /api/algo/{acct}/pov", func(w http.ResponseWriter, q *http.Request, u *auth.User, acct int64) {
		var in req
		if !read(w, q, &in) {
			return
		}
		a, err := m.StartPOV(u.ID, acct, in.Instrument, in.Side, in.Qty, in.Pct, in.Minutes, in.LimitPx, in.Reduce)
		if err != nil {
			we(w, err)
			return
		}
		wj(w, 200, a)
	})
	r("POST /api/algo/{acct}/scale", func(w http.ResponseWriter, q *http.Request, _ *auth.User, acct int64) {
		var in req
		if !read(w, q, &in) {
			return
		}
		ids, err := m.Scale(acct, in.Instrument, in.Side, in.Lower, in.Upper, in.Qty, in.Count, in.Skew, in.Reduce)
		if err != nil {
			we(w, err)
			return
		}
		wj(w, 200, map[string]any{"orders": ids})
	})
	r("POST /api/algo/{acct}/exits", func(w http.ResponseWriter, q *http.Request, _ *auth.User, acct int64) {
		var in req
		if !read(w, q, &in) {
			return
		}
		ids, err := m.MultiExit(acct, in.Instrument, in.Levels)
		if err != nil {
			we(w, err)
			return
		}
		onManual(acct, in.Instrument)
		wj(w, 200, map[string]any{"orders": ids})
	})
	r("POST /api/algo/{acct}/reverse", func(w http.ResponseWriter, q *http.Request, _ *auth.User, acct int64) {
		var in req
		if !read(w, q, &in) {
			return
		}
		o, err := m.Reverse(acct, in.Instrument)
		if err != nil {
			we(w, err)
			return
		}
		onManual(acct, in.Instrument)
		wj(w, 200, o)
	})
	r("DELETE /api/algo/{acct}/{id}", func(w http.ResponseWriter, q *http.Request, u *auth.User, acct int64) {
		id, err := strconv.ParseInt(q.PathValue("id"), 10, 64)
		if err != nil {
			wj(w, 404, map[string]string{"code": "NOT_FOUND"})
			return
		}
		if err := m.Cancel(u.ID, acct, id); err != nil {
			we(w, err)
			return
		}
		wj(w, 200, map[string]bool{"ok": true})
	})
}
