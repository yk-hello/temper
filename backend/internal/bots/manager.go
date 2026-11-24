package bots

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

// Rej 拒绝，Code 对应界面词条 err.<Code>。
type Rej struct {
	Code   string
	Params map[string]string
}

func (r *Rej) Error() string { return r.Code }

func rej(code string, kv ...string) *Rej {
	r := &Rej{Code: code, Params: map[string]string{}}
	for i := 0; i+1 < len(kv); i += 2 {
		r.Params[kv[i]] = kv[i+1]
	}
	return r
}

// Exec 下单通道：现货和合约各一个实现。
type Exec interface {
	Quote(inst string) (bid, ask decimal.Decimal, ok bool)
	High24(inst string) decimal.Decimal
	SpecOf(inst string) (Spec, bool)
	Prepare(acct int64, inst string, leverage int) error
	Place(acct int64, inst, side, typ string, price, qty decimal.Decimal, reduce bool, source string) (id string, filled, avg decimal.Decimal, err error)
	Cancel(acct int64, id string)
	Order(acct int64, id string) (status string, filled, avg decimal.Decimal, ok bool)
	// Position 现在这个币的净持仓（现货是可用 + 占用的币）
	Position(acct int64, inst string) decimal.Decimal
}

// 状态。
const (
	StRunning = "running"
	StWaiting = "waiting" // 抄底：等跌到
	StPaused  = "paused"
	StStopped = "stopped"
)

// GridSt 一格。Entry 不是 0 表示这一格开了仓，下一次成交是平仓。
type GridSt struct {
	I       int             `json:"i"`
	Side    string          `json:"side"`
	OrderID string          `json:"order,omitempty"`
	Entry   decimal.Decimal `json:"entry"`
	SellQty decimal.Decimal `json:"sell_qty"` // 现货：这一格这次卖多少（可能带上攒的零头）
	Done    int             `json:"done"`     // 这一格一买一卖做完了几次
	Net     decimal.Decimal `json:"net"`      // 这一格累计净利
}

// Rec 一条记录：成交、补仓、暂停、停机。
type Rec struct {
	T    int64           `json:"t"`
	Kind string          `json:"kind"` // fill | add | tp | sl | start | stop | pause | resume | down | buy
	Side string          `json:"side,omitempty"`
	Px   decimal.Decimal `json:"px"`
	Qty  decimal.Decimal `json:"qty"`
	Net  decimal.Decimal `json:"net"`
	Note string          `json:"note,omitempty"`
}

// RungSt DCA 的一档。
type RungSt struct {
	Px      decimal.Decimal `json:"px"`
	USD     decimal.Decimal `json:"usd"`
	OrderID string          `json:"order,omitempty"`
	Filled  bool            `json:"filled"`
}

// Bot 一个机器人。
type Bot struct {
	ID      int64  `json:"id"`
	User    int64  `json:"-"`
	Account int64  `json:"account"`
	Kind    string `json:"kind"`
	Params  Params `json:"params"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	Created int64  `json:"created"`
	Stopped int64  `json:"stopped,omitempty"`
	LastOK  int64  `json:"last_ok"`

	Plan  *Plan    `json:"plan,omitempty"`
	Grids []GridSt `json:"grids,omitempty"`

	Rungs   []RungSt        `json:"rungs,omitempty"`
	TPOrder string          `json:"tp_order,omitempty"`
	Round   int             `json:"round"`
	Qty     decimal.Decimal `json:"qty"`  // DCA：这一轮持仓
	Cost    decimal.Decimal `json:"cost"` // DCA：这一轮花了多少
	Avg     decimal.Decimal `json:"avg"`

	NextAt int64           `json:"next_at,omitempty"`
	Count  int             `json:"count"`
	Spent  decimal.Decimal `json:"spent"`
	Bought decimal.Decimal `json:"bought"`

	Dust     decimal.Decimal `json:"dust"`     // 现货网格：买入被扣手续费后取整剩下的零头，攒够一个单位就一起卖
	Realized decimal.Decimal `json:"realized"` // 已实现净利（扣过手续费）
	Pairs    int             `json:"pairs"`    // 网格做完几次一买一卖
	Fills    int             `json:"fills"`    // 成交了几次
	Recs     []Rec           `json:"recs"`

	dirty bool
	exec  Exec
}

func (b *Bot) rec(r Rec, now int64) {
	r.T = now
	b.Recs = append(b.Recs, r)
	if len(b.Recs) > 300 {
		b.Recs = b.Recs[len(b.Recs)-300:]
	}
	b.dirty = true
}

func (b *Bot) source() string { return "bot:" + strconv.FormatInt(b.ID, 10) }

func (b *Bot) active() bool { return b.Status == StRunning || b.Status == StWaiting }

// Rank 引擎优先级：数字越小越优先。手动永远最高（D80）。
type Rank struct {
	Order []string `json:"order"` // ai、signal、bot 的顺序
}

// DefaultOrder 默认优先级（D83）。
var DefaultOrder = []string{"ai", "signal", "bot"}

// Manager 管所有机器人。
type Manager struct {
	Spot, Perp Exec
	Log        *slog.Logger
	Now        func() int64
	// AIRunning 这个账户在跟 AI 吗（机器人要和 AI 比优先级）
	AIRunning func(acct int64) bool
	// MaxBots 这个人最多同时开几个（防滥用，D85 第 2 条）。没接时不限
	MaxBots func(user int64) int
	// Locked 纪律锁锁着就返回拒绝：锁定期间不能开、不能恢复机器人。没接时不查
	Locked func(acct int64) error

	pool *pgxpool.Pool

	mu    sync.Mutex
	bots  map[int64]*Bot
	next  int64
	ranks map[int64][]string

	busyMu sync.RWMutex
	busy   map[string]int64 // 账户:币 → 机器人 id。下单时要读，不能碰 mu（引擎锁里调用）
	queue  chan pauseReq
}

type pauseReq struct {
	acct   int64
	inst   string
	reason string
}

// New 新建。
func New(spot, perp Exec, log *slog.Logger) *Manager {
	return &Manager{Spot: spot, Perp: perp, Log: log, bots: map[int64]*Bot{}, ranks: map[int64][]string{}, busy: map[string]int64{}, queue: make(chan pauseReq, 256), next: 1}
}

func (m *Manager) now() int64 {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now().UnixMilli()
}

// busyKey 现货和合约是两个品种：BTC 现货网格和 BTC 合约可以同时跑。
func busyKey(acct int64, inst string) string { return strconv.FormatInt(acct, 10) + ":" + inst }

func (m *Manager) setBusy(b *Bot, on bool) {
	m.busyMu.Lock()
	defer m.busyMu.Unlock()
	k := busyKey(b.Account, b.Params.Instrument)
	if on {
		m.busy[k] = b.ID
	} else if m.busy[k] == b.ID {
		delete(m.busy, k)
	}
}

func baseOf(inst string) string {
	return strings.TrimSuffix(strings.TrimSuffix(inst, "-SPOT"), "-PERP")
}

// BotOn 这个账户这个币上有在跑的机器人吗。
func (m *Manager) BotOn(acct int64, inst string) bool {
	m.busyMu.RLock()
	defer m.busyMu.RUnlock()
	_, ok := m.busy[busyKey(acct, inst)]
	return ok
}

// ManualAction 手动单在这个币上成交了：手动权限最高，机器人暂停（D83 第 6 条）。引擎锁里外都可以调。
func (m *Manager) ManualAction(acct int64, inst string) {
	if m.BotOn(acct, inst) {
		select {
		case m.queue <- pauseReq{acct, inst, "MANUAL"}:
		default:
		}
	}
}

// Arbitrate 别的引擎（ai、signal）要在这个币上下单：比优先级。
// 对方更优先：机器人暂停，放行。机器人更优先：拒绝对方。在引擎锁里调用，不能碰 mu。
func (m *Manager) Arbitrate(user, acct int64, inst, engine string) error {
	if !m.BotOn(acct, inst) {
		return nil
	}
	if m.rankOf(user, engine) < m.rankOf(user, "bot") {
		select {
		case m.queue <- pauseReq{acct, inst, strings.ToUpper(engine)}:
		default:
		}
		return nil
	}
	return rej("BOT_HAS_PRIORITY")
}

func (m *Manager) rankOf(user int64, engine string) int {
	m.busyMu.RLock()
	order := m.ranks[user]
	m.busyMu.RUnlock()
	if len(order) == 0 {
		order = DefaultOrder
	}
	for i, x := range order {
		if x == engine {
			return i
		}
	}
	return 99
}

// Order 这个用户的引擎优先级。
func (m *Manager) Order(user int64) []string {
	m.busyMu.RLock()
	defer m.busyMu.RUnlock()
	if o := m.ranks[user]; len(o) > 0 {
		return o
	}
	return DefaultOrder
}

// SetOrder 改引擎优先级。
func (m *Manager) SetOrder(ctx context.Context, user int64, order []string) error {
	seen := map[string]bool{}
	for _, x := range order {
		if x != "ai" && x != "signal" && x != "bot" {
			return rej("BAD_REQUEST")
		}
		seen[x] = true
	}
	if len(order) != 3 || len(seen) != 3 {
		return rej("BAD_REQUEST")
	}
	m.busyMu.Lock()
	m.ranks[user] = order
	m.busyMu.Unlock()
	if m.pool != nil {
		b, _ := json.Marshal(order)
		_, err := m.pool.Exec(ctx, `insert into bot_prefs (usr, rank_order) values ($1,$2) on conflict (usr) do update set rank_order=excluded.rank_order`, user, b)
		return err
	}
	return nil
}

func (m *Manager) execOf(inst string) Exec {
	if strings.HasSuffix(inst, "-SPOT") {
		return m.Spot
	}
	return m.Perp
}

func mid(bid, ask decimal.Decimal) decimal.Decimal { return bid.Add(ask).Div(two) }

// Preview 算计划，不下单。
func (m *Manager) Preview(kind string, p Params) (*Plan, error) {
	e := m.execOf(p.Instrument)
	s, ok := e.SpecOf(p.Instrument)
	if !ok {
		return nil, rej("UNKNOWN_INSTRUMENT")
	}
	bid, ask, ok := e.Quote(p.Instrument)
	if !ok {
		return nil, rej("NO_BOOK")
	}
	return planOf(kind, p, mid(bid, ask), s)
}

func planOf(kind string, p Params, price decimal.Decimal, s Spec) (*Plan, error) {
	switch kind {
	case SpotGrid:
		if !strings.HasSuffix(p.Instrument, "-SPOT") {
			return nil, rej("BAD_REQUEST")
		}
		return CheckGrid(kind, p, price, s)
	case PerpGrid:
		if !strings.HasSuffix(p.Instrument, "-PERP") {
			return nil, rej("BAD_REQUEST")
		}
		return CheckGrid(kind, p, price, s)
	case DCA, Dip:
		if kind == Dip && !p.TriggerPx.IsPositive() && !p.DropPct.IsPositive() {
			return nil, rej("DIP_NO_TRIGGER")
		}
		start := price
		if kind == Dip && p.TriggerPx.IsPositive() {
			start = p.TriggerPx
		}
		return CheckDCA(p, start, s)
	case Recur:
		if !strings.HasSuffix(p.Instrument, "-SPOT") {
			return nil, rej("BAD_REQUEST")
		}
		if p.Amount.LessThan(s.MinNotional) {
			return nil, rej("MIN_NOTIONAL", "min", s.MinNotional.String())
		}
		switch p.Every {
		case "hour", "day", "week", "month":
		default:
			return nil, rej("BAD_REQUEST")
		}
		return &Plan{Warn: []string{}, Need: p.Amount}, nil
	}
	return nil, rej("BAD_REQUEST")
}

// countActive 这个人有几个还没停的机器人（暂停的也占名额）。
func (m *Manager) countActive(user int64) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, b := range m.bots {
		if b.User == user && b.Status != StStopped {
			n++
		}
	}
	return n
}

// Start 开一个机器人。
func (m *Manager) Start(user, acct int64, kind string, p Params) (*Bot, error) {
	if m.Locked != nil {
		if err := m.Locked(acct); err != nil {
			return nil, err
		}
	}
	if m.MaxBots != nil {
		if lim := m.MaxBots(user); lim > 0 && m.countActive(user) >= lim {
			return nil, rej("BOT_LIMIT")
		}
	}
	e := m.execOf(p.Instrument)
	s, ok := e.SpecOf(p.Instrument)
	if !ok {
		return nil, rej("UNKNOWN_INSTRUMENT")
	}
	bid, ask, ok := e.Quote(p.Instrument)
	if !ok {
		return nil, rej("NO_BOOK")
	}
	price := mid(bid, ask)
	plan, err := planOf(kind, p, price, s)
	if err != nil {
		return nil, err
	}
	if m.BotOn(acct, p.Instrument) {
		return nil, rej("BOT_COIN_BUSY")
	}
	// 同一个币只有一个引擎（D42）：这个币上有手动仓位时不开合约机器人；账户在跟 AI 时比优先级
	if s.Perp && !e.Position(acct, p.Instrument).IsZero() {
		return nil, rej("BOT_COIN_HAS_POSITION")
	}
	if m.AIRunning != nil && m.AIRunning(acct) && m.rankOf(user, "ai") < m.rankOf(user, "bot") {
		return nil, rej("BOT_ACCOUNT_FOLLOWING")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	b := &Bot{ID: m.next, User: user, Account: acct, Kind: kind, Params: p, Status: StRunning, Created: m.now(), LastOK: m.now(), Plan: plan, exec: e, dirty: true, Recs: []Rec{}}
	m.next++
	if err := e.Prepare(acct, p.Instrument, p.Leverage); err != nil {
		return nil, err
	}
	m.bots[b.ID] = b
	m.setBusy(b, true)
	b.rec(Rec{Kind: "start", Px: price}, m.now())
	var serr error
	switch kind {
	case SpotGrid, PerpGrid:
		serr = m.startGrid(b, price, s)
	case DCA:
		serr = m.startDCA(b, price, s)
	case Dip:
		b.Status = StWaiting
	case Recur:
		b.NextAt = m.now() // 第一次马上买
	}
	if serr != nil {
		m.cancelAll(b)
		m.unwindStart(b, s)
		delete(m.bots, b.ID)
		m.setBusy(b, false)
		return nil, serr
	}
	m.save(b)
	cp := *b
	return &cp, nil
}

// unwindStart 启动没成（后面的挂单被拒）：开始时按市价买卖进来的那一笔按市价退回去，不留一个没人管的仓位。
// 11-16 走查：ETH 网格先市价买了 0.747 ETH，挂第一张远处的格子被价格保护拒掉，机器人没开成，仓位却留在账户里。
func (m *Manager) unwindStart(b *Bot, s Spec) {
	for _, r := range b.Recs {
		if (r.Kind != "fill" && r.Kind != "buy") || !r.Qty.IsPositive() {
			continue
		}
		back := "sell"
		if r.Side == "sell" {
			back = "buy"
		}
		qty := m.netQty(b, r.Qty, s) // 现货买进来的扣过手续费，卖回去按到手的数
		if !s.Perp {
			qty = floorTo(qty, s.Step)
		}
		if !qty.IsPositive() {
			continue
		}
		if _, _, _, err := b.exec.Place(b.Account, b.Params.Instrument, back, "market", zero, qty, true, b.source()); err != nil && m.Log != nil {
			m.Log.Warn("机器人启动失败，退回开始那一笔也失败", "bot", b.ID, "inst", b.Params.Instrument, "err", err)
		}
	}
}

// ---------- 网格 ----------

func (m *Manager) startGrid(b *Bot, price decimal.Decimal, s Spec) error {
	p := b.Plan
	n := len(p.Lines) - 1
	dir := b.Params.Dir
	if b.Kind == SpotGrid {
		dir = "long"
	}
	// 先要有货：市价买 / 卖够上面（或下面）格子要用的量
	initPx := price
	if p.InitSide != "" && p.InitQty.IsPositive() {
		_, filled, avg, err := b.exec.Place(b.Account, b.Params.Instrument, p.InitSide, "market", zero, p.InitQty, false, b.source())
		if err != nil {
			return err
		}
		if filled.IsPositive() {
			initPx = avg
			b.Fills++
			b.rec(Rec{Kind: "fill", Side: p.InitSide, Px: avg, Qty: filled, Note: "init"}, m.now())
		}
	}
	b.Grids = make([]GridSt, n)
	for i := 0; i < n; i++ {
		g := GridSt{I: i}
		switch {
		case p.Lines[i].GreaterThanOrEqual(price): // 现价下方的线都在下面：这一格在现价上面
			g.Side = "sell"
			if dir == "long" {
				g.Entry = initPx // 这一格的货是开始时买的
			}
		case p.Lines[i+1].LessThanOrEqual(price):
			g.Side = "buy"
			if dir == "short" {
				g.Entry = initPx
			}
		default: // 现价就在这一格里：按买处理（线在现价下面）
			g.Side = "buy"
			if dir == "short" {
				g.Entry = initPx
			}
		}
		b.Grids[i] = g
	}
	for i := range b.Grids {
		if err := m.placeGrid(b, &b.Grids[i], s); err != nil {
			return err
		}
	}
	return nil
}

func gridQty(b *Bot, g *GridSt) decimal.Decimal {
	if g.Side == "sell" && b.Kind == SpotGrid {
		if g.SellQty.IsPositive() {
			return g.SellQty
		}
		return b.Plan.SellQty
	}
	return b.Plan.Qty
}

func gridPx(b *Bot, g *GridSt) decimal.Decimal {
	if g.Side == "buy" {
		return b.Plan.Lines[g.I]
	}
	return b.Plan.Lines[g.I+1]
}

func tooFar(px, price decimal.Decimal, s Spec) bool {
	return s.Band.IsPositive() && px.Sub(price).Abs().GreaterThan(price.Mul(s.Band))
}

func (m *Manager) placeGrid(b *Bot, g *GridSt, s Spec) error {
	if bid, ask, ok := b.exec.Quote(b.Params.Instrument); ok && tooFar(gridPx(b, g), mid(bid, ask), s) {
		return nil
	}
	id, _, _, err := b.exec.Place(b.Account, b.Params.Instrument, g.Side, "limit", gridPx(b, g), gridQty(b, g), false, b.source())
	if err != nil {
		return err
	}
	g.OrderID = id
	b.dirty = true
	return nil
}

func (m *Manager) stepGrid(b *Bot, price decimal.Decimal, s Spec) {
	for i := range b.Grids {
		g := &b.Grids[i]
		if g.OrderID == "" {
			if err := m.placeGrid(b, g, s); err != nil {
				m.pause(b, reasonOf(err))
				return
			}
			continue
		}
		st, filled, avg, ok := b.exec.Order(b.Account, g.OrderID)
		if !ok {
			continue
		}
		switch st {
		case "filled":
			m.gridFilled(b, g, filled, avg, s)
			if err := m.placeGrid(b, g, s); err != nil {
				g.OrderID = ""
				m.pause(b, reasonOf(err))
				return
			}
		case "canceled", "expired":
			g.OrderID = ""
			m.pause(b, "ORDER_CANCELED")
			return
		}
	}
	// 止盈止损：到价全部平掉（用户手动停止时才让选，到价就按设的做）
	p := b.Params
	long := b.Kind == SpotGrid || p.Dir != "short"
	switch {
	case p.StopLoss.IsPositive() && ((long && price.LessThanOrEqual(p.StopLoss)) || (!long && price.GreaterThanOrEqual(p.StopLoss))):
		m.stop(b, true, "STOP_LOSS")
	case p.TakeProfit.IsPositive() && ((long && price.GreaterThanOrEqual(p.TakeProfit)) || (!long && price.LessThanOrEqual(p.TakeProfit))):
		m.stop(b, true, "TAKE_PROFIT")
	}
}

func (m *Manager) gridFilled(b *Bot, g *GridSt, qty, px decimal.Decimal, s Spec) {
	b.Fills++
	rec := Rec{Kind: "fill", Side: g.Side, Px: px, Qty: qty}
	fee := qty.Mul(px).Mul(s.Fee)
	if g.Entry.IsZero() {
		// 开仓：这一格开始
		g.Entry = px
		if b.Kind == SpotGrid {
			// 这次到手的比下次要卖的多一点点，攒起来
			b.Dust = b.Dust.Add(qty.Mul(one.Sub(s.Fee))).Sub(b.Plan.SellQty)
			g.SellQty = b.Plan.SellQty
			if extra := floorTo(b.Dust, s.Step); extra.IsPositive() {
				g.SellQty = g.SellQty.Add(extra)
				b.Dust = b.Dust.Sub(extra)
			}
		}
	} else {
		// 平仓：算这一格赚了多少。手续费一买一卖都算。现货买入时的手续费是扣币的，已经体现在卖的数量少了
		var gross decimal.Decimal
		if g.Side == "sell" {
			if b.Kind == SpotGrid {
				// 按到手的数量算：取整的零头留着下次卖，算进这一格，所以每格的数字和下单前算的一样
				got := b.Plan.Qty.Mul(one.Sub(s.Fee))
				gross = got.Mul(px).Sub(b.Plan.Qty.Mul(g.Entry))
				fee = got.Mul(px).Mul(s.Fee)
			} else {
				gross = px.Sub(g.Entry).Mul(qty)
				fee = fee.Add(qty.Mul(g.Entry).Mul(s.Fee))
			}
		} else {
			gross = g.Entry.Sub(px).Mul(qty)
			fee = fee.Add(qty.Mul(g.Entry).Mul(s.Fee))
		}
		net := gross.Sub(fee)
		g.Net = g.Net.Add(net)
		g.Done++
		g.Entry = zero
		b.Realized = b.Realized.Add(net)
		b.Pairs++
		rec.Net = net
	}
	b.rec(rec, m.now())
	if g.Side == "buy" {
		g.Side = "sell"
	} else {
		g.Side = "buy"
	}
	g.OrderID = ""
}

// ---------- DCA、抄底 ----------

func (m *Manager) dcaSides(b *Bot) (open, closeSide string, sign decimal.Decimal) {
	if b.Params.Side == "short" {
		return "sell", "buy", one.Neg()
	}
	return "buy", "sell", one
}

func (m *Manager) startDCA(b *Bot, price decimal.Decimal, s Spec) error {
	open, _, sign := m.dcaSides(b)
	p := b.Params
	qty := floorTo(p.First.Div(price), s.Step)
	if !qty.IsPositive() {
		return rej("BOT_BAD_AMOUNT")
	}
	_, filled, avg, err := b.exec.Place(b.Account, p.Instrument, open, "market", zero, qty, false, b.source())
	if err != nil {
		return err
	}
	if !filled.IsPositive() {
		return rej("NO_LIQUIDITY")
	}
	b.Round++
	b.Qty, b.Cost = m.netQty(b, filled, s), filled.Mul(avg)
	b.Avg = b.Cost.Div(b.Qty)
	b.Fills++
	b.rec(Rec{Kind: "buy", Side: open, Px: avg, Qty: filled, Note: "first"}, m.now())
	// 梯子
	b.Rungs = nil
	amt := p.Add
	mult := p.Mult
	if !mult.IsPositive() {
		mult = one
	}
	for k := 1; k <= p.MaxAdds; k++ {
		d := p.StepPct.Mul(decimal.NewFromInt(int64(k))).Div(hund).Mul(sign)
		px := floorTo(avg.Mul(one.Sub(d)), s.Tick)
		r := RungSt{Px: px, USD: amt}
		if !tooFar(px, price, s) {
			id, _, _, err := b.exec.Place(b.Account, p.Instrument, open, "limit", px, floorTo(amt.Div(px), s.Step), false, b.source())
			if err != nil {
				return err
			}
			r.OrderID = id
		}
		b.Rungs = append(b.Rungs, r)
		amt = amt.Mul(mult)
	}
	return m.placeTP(b, s)
}

// netQty 现货买入时手续费扣币，到手的数量少一点。
func (m *Manager) netQty(b *Bot, filled decimal.Decimal, s Spec) decimal.Decimal {
	if !s.Perp {
		return floorTo(filled.Mul(one.Sub(s.Taker)), decimal.RequireFromString("0.00000001"))
	}
	return filled
}

func (m *Manager) tpPrice(b *Bot, s Spec) decimal.Decimal {
	d := b.Params.TPPct.Div(hund)
	if b.Params.Side == "short" {
		return floorTo(b.Avg.Mul(one.Sub(d)), s.Tick)
	}
	return ceilTo(b.Avg.Mul(one.Add(d)), s.Tick)
}

func (m *Manager) placeTP(b *Bot, s Spec) error {
	if b.TPOrder != "" {
		b.exec.Cancel(b.Account, b.TPOrder)
		b.TPOrder = ""
	}
	_, closeSide, _ := m.dcaSides(b)
	q := floorTo(b.Qty, s.Step)
	if !q.IsPositive() {
		return nil
	}
	id, _, _, err := b.exec.Place(b.Account, b.Params.Instrument, closeSide, "limit", m.tpPrice(b, s), q, s.Perp, b.source())
	if err != nil {
		return err
	}
	b.TPOrder = id
	b.dirty = true
	return nil
}

func (m *Manager) stepDip(b *Bot, price decimal.Decimal, s Spec) {
	p := b.Params
	hit := p.TriggerPx.IsPositive() && price.LessThanOrEqual(p.TriggerPx)
	if !hit && p.DropPct.IsPositive() {
		if h := b.exec.High24(p.Instrument); h.IsPositive() && price.LessThanOrEqual(h.Mul(one.Sub(p.DropPct.Div(hund)))) {
			hit = true
		}
	}
	if !hit {
		return
	}
	if err := m.startDCA(b, price, s); err != nil {
		m.pause(b, reasonOf(err))
		return
	}
	b.Status = StRunning
	b.dirty = true
}

func (m *Manager) stepDCA(b *Bot, price decimal.Decimal, s Spec) {
	p := b.Params
	_, _, sign := m.dcaSides(b)
	// 补仓成交
	changed := false
	for i := range b.Rungs {
		r := &b.Rungs[i]
		if r.Filled {
			continue
		}
		if r.OrderID == "" { // 之前离得太远没挂：走近了再挂
			if !tooFar(r.Px, price, s) {
				open, _, _ := m.dcaSides(b)
				if id, _, _, err := b.exec.Place(b.Account, p.Instrument, open, "limit", r.Px, floorTo(r.USD.Div(r.Px), s.Step), false, b.source()); err == nil {
					r.OrderID = id
					b.dirty = true
				}
			}
			continue
		}
		st, filled, avg, ok := b.exec.Order(b.Account, r.OrderID)
		if !ok || st != "filled" {
			continue
		}
		r.Filled = true
		b.Qty = b.Qty.Add(m.netQty(b, filled, s))
		b.Cost = b.Cost.Add(filled.Mul(avg))
		b.Avg = b.Cost.Div(b.Qty)
		b.Fills++
		b.rec(Rec{Kind: "add", Side: "buy", Px: avg, Qty: filled}, m.now())
		changed = true
	}
	if changed {
		if err := m.placeTP(b, s); err != nil {
			m.pause(b, reasonOf(err))
			return
		}
	}
	// 止盈
	if b.TPOrder != "" {
		if st, filled, avg, ok := b.exec.Order(b.Account, b.TPOrder); ok && st == "filled" {
			fee := filled.Mul(avg).Mul(s.Fee)
			gross := avg.Mul(filled).Sub(b.Avg.Mul(filled))
			if sign.IsNegative() {
				gross = gross.Neg()
			}
			openFee := zero
			if s.Perp {
				openFee = b.Cost.Mul(s.Taker)
			}
			net := gross.Sub(fee).Sub(openFee)
			b.Realized = b.Realized.Add(net)
			b.Fills++
			b.rec(Rec{Kind: "tp", Px: avg, Qty: filled, Net: net}, m.now())
			m.cancelRungs(b)
			b.TPOrder = ""
			b.Qty, b.Cost, b.Avg = zero, zero, zero
			if p.Repeat {
				if err := m.startDCA(b, price, s); err != nil {
					m.pause(b, reasonOf(err))
				}
				return
			}
			b.Status, b.Reason, b.Stopped = StStopped, "TAKE_PROFIT", m.now()
			m.setBusy(b, false)
			return
		}
	}
	// 止损
	if p.SLPct.IsPositive() && b.Avg.IsPositive() {
		d := p.SLPct.Div(hund).Mul(sign)
		if line := b.Avg.Mul(one.Sub(d)); (sign.IsPositive() && price.LessThanOrEqual(line)) || (sign.IsNegative() && price.GreaterThanOrEqual(line)) {
			m.stop(b, true, "STOP_LOSS")
		}
	}
}

func (m *Manager) cancelRungs(b *Bot) {
	for i := range b.Rungs {
		r := &b.Rungs[i]
		if !r.Filled && r.OrderID != "" {
			b.exec.Cancel(b.Account, r.OrderID)
			r.OrderID = ""
		}
	}
}

// ---------- 定投 ----------

func nextAt(from int64, every string) int64 {
	t := time.UnixMilli(from)
	switch every {
	case "hour":
		t = t.Add(time.Hour)
	case "week":
		t = t.AddDate(0, 0, 7)
	case "month":
		t = t.AddDate(0, 1, 0)
	default:
		t = t.AddDate(0, 0, 1)
	}
	return t.UnixMilli()
}

func (m *Manager) stepRecur(b *Bot, price decimal.Decimal, s Spec) {
	now := m.now()
	if now < b.NextAt {
		return
	}
	q := floorTo(b.Params.Amount.Div(price), s.Step)
	_, filled, avg, err := b.exec.Place(b.Account, b.Params.Instrument, "buy", "market", zero, q, false, b.source())
	if err != nil {
		b.rec(Rec{Kind: "miss", Note: reasonOf(err), Px: price}, now)
	} else if filled.IsPositive() {
		b.Count++
		b.Fills++
		b.Spent = b.Spent.Add(filled.Mul(avg))
		b.Bought = b.Bought.Add(m.netQty(b, filled, s))
		b.rec(Rec{Kind: "buy", Side: "buy", Px: avg, Qty: filled}, now)
	}
	// 错过的不补：下一次从现在算
	b.NextAt = nextAt(now, b.Params.Every)
	b.dirty = true
	if b.Params.Times > 0 && b.Count >= b.Params.Times {
		b.Status, b.Reason, b.Stopped = StStopped, "DONE", now
		m.setBusy(b, false)
	}
}

// ---------- 暂停、恢复、停止 ----------

func reasonOf(err error) string {
	type coded interface{ RejectCode() string }
	switch e := err.(type) {
	case *Rej:
		return e.Code
	case coded:
		return e.RejectCode()
	}
	return "ORDER_FAILED"
}

func (m *Manager) cancelAll(b *Bot) {
	for i := range b.Grids {
		g := &b.Grids[i]
		if g.OrderID != "" {
			b.exec.Cancel(b.Account, g.OrderID)
			g.OrderID = ""
		}
	}
	m.cancelRungs(b)
	if b.TPOrder != "" {
		b.exec.Cancel(b.Account, b.TPOrder)
		b.TPOrder = ""
	}
}

func (m *Manager) pause(b *Bot, reason string) {
	m.cancelAll(b)
	b.Status, b.Reason = StPaused, reason
	b.rec(Rec{Kind: "pause", Note: reason}, m.now())
	m.setBusy(b, false)
}

// position 这个机器人名下还有多少货，要平掉的方向和数量。
func (m *Manager) holding(b *Bot, s Spec) (side string, qty decimal.Decimal) {
	switch b.Kind {
	case DCA, Dip:
		_, c, _ := m.dcaSides(b)
		return c, floorTo(b.Qty, s.Step)
	case Recur:
		return "sell", floorTo(b.Bought, s.Step)
	}
	q := zero
	long := zero
	for _, g := range b.Grids {
		if !g.Entry.IsPositive() {
			continue
		}
		if g.Side == "sell" {
			long = long.Add(gridQty(b, &g))
		} else {
			q = q.Add(b.Plan.Qty)
		}
	}
	switch {
	case long.GreaterThan(q):
		return "sell", floorTo(long.Sub(q), s.Step)
	case q.GreaterThan(long):
		return "buy", floorTo(q.Sub(long), s.Step)
	}
	return "", zero
}

// stop 停止。closePos 平掉这个机器人名下的货；不平就留给用户手动管理。
func (m *Manager) stop(b *Bot, closePos bool, reason string) {
	m.cancelAll(b)
	s, ok := b.exec.SpecOf(b.Params.Instrument)
	if closePos && ok {
		side, qty := m.holding(b, s)
		if qty.IsPositive() {
			_, filled, avg, err := b.exec.Place(b.Account, b.Params.Instrument, side, "market", zero, qty, s.Perp, b.source())
			if err != nil {
				b.rec(Rec{Kind: "stop", Note: reasonOf(err)}, m.now())
			} else if filled.IsPositive() {
				net := m.closeNet(b, side, filled, avg, s)
				b.Realized = b.Realized.Add(net)
				b.rec(Rec{Kind: "stop", Side: side, Px: avg, Qty: filled, Net: net, Note: reason}, m.now())
			}
		}
	}
	for i := range b.Grids {
		b.Grids[i].Entry = zero
	}
	b.Qty, b.Cost, b.Avg = zero, zero, zero
	b.Status, b.Reason, b.Stopped = StStopped, reason, m.now()
	b.rec(Rec{Kind: "stop", Note: reason}, m.now())
	m.setBusy(b, false)
}

// closeNet 平掉时这部分赚亏：按开仓价和这次的成交价算。
func (m *Manager) closeNet(b *Bot, side string, qty, px decimal.Decimal, s Spec) decimal.Decimal {
	entry := zero
	switch b.Kind {
	case DCA, Dip:
		entry = b.Avg
	case Recur:
		if b.Bought.IsPositive() {
			entry = b.Spent.Div(b.Bought)
		}
	default:
		sum, cnt := zero, zero
		for _, g := range b.Grids {
			if g.Entry.IsPositive() {
				sum = sum.Add(g.Entry)
				cnt = cnt.Add(one)
			}
		}
		if cnt.IsPositive() {
			entry = sum.Div(cnt)
		}
	}
	gross := px.Sub(entry).Mul(qty)
	if side == "buy" {
		gross = gross.Neg()
	}
	return gross.Sub(qty.Mul(px).Mul(s.Taker))
}

// Stop 用户手动停止。
func (m *Manager) Stop(user, id int64, closePos bool) (*Bot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b := m.bots[id]
	if b == nil || b.User != user {
		return nil, rej("NOT_FOUND")
	}
	if b.Status == StStopped {
		return nil, rej("BOT_ALREADY_STOPPED")
	}
	m.stop(b, closePos, "USER")
	m.save(b)
	cp := *b
	return &cp, nil
}

// Pause 用户手动暂停。
func (m *Manager) Pause(user, id int64) (*Bot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b := m.bots[id]
	if b == nil || b.User != user {
		return nil, rej("NOT_FOUND")
	}
	if !b.active() {
		return nil, rej("BOT_NOT_RUNNING")
	}
	m.pause(b, "USER")
	m.save(b)
	cp := *b
	return &cp, nil
}

// Resume 恢复：从现在的状态接着挂单。
func (m *Manager) Resume(user, id int64) (*Bot, error) {
	if m.Locked != nil { // 先在机器人的锁外面问纪律锁，两把锁不嵌套
		m.mu.Lock()
		var acct int64
		if b := m.bots[id]; b != nil && b.User == user {
			acct = b.Account
		}
		m.mu.Unlock()
		if acct != 0 {
			if err := m.Locked(acct); err != nil {
				return nil, err
			}
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	b := m.bots[id]
	if b == nil || b.User != user {
		return nil, rej("NOT_FOUND")
	}
	if b.Status != StPaused {
		return nil, rej("BOT_NOT_PAUSED")
	}
	if m.BotOn(b.Account, b.Params.Instrument) {
		return nil, rej("BOT_COIN_BUSY")
	}
	s, ok := b.exec.SpecOf(b.Params.Instrument)
	if !ok {
		return nil, rej("UNKNOWN_INSTRUMENT")
	}
	b.Status, b.Reason = StRunning, ""
	m.setBusy(b, true)
	switch b.Kind {
	case DCA, Dip:
		if b.Qty.IsZero() && b.Kind == Dip {
			b.Status = StWaiting
			break
		}
		for i := range b.Rungs {
			r := &b.Rungs[i]
			if r.Filled {
				continue
			}
			open, _, _ := m.dcaSides(b)
			id2, _, _, err := b.exec.Place(b.Account, b.Params.Instrument, open, "limit", r.Px, floorTo(r.USD.Div(r.Px), s.Step), false, b.source())
			if err != nil {
				m.pause(b, reasonOf(err))
				cp := *b
				return &cp, err
			}
			r.OrderID = id2
		}
		if err := m.placeTP(b, s); err != nil {
			m.pause(b, reasonOf(err))
			cp := *b
			return &cp, err
		}
	}
	b.rec(Rec{Kind: "resume"}, m.now())
	m.save(b)
	cp := *b
	return &cp, nil
}

// List 用户的机器人，新的在前。
// Running 正在跑的机器人数量（系统控制台）。
func (m *Manager) Running() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, b := range m.bots {
		if b.active() {
			n++
		}
	}
	return n
}

func (m *Manager) List(user int64) []Bot {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Bot{}
	for _, b := range m.bots {
		if b.User == user {
			out = append(out, *b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out
}

// Tick 跑一轮。
func (m *Manager) Tick() {
	for {
		select {
		case r := <-m.queue:
			m.mu.Lock()
			for _, b := range m.bots {
				if b.Account == r.acct && b.Params.Instrument == r.inst && b.active() {
					m.pause(b, r.reason)
				}
			}
			m.mu.Unlock()
			continue
		default:
		}
		break
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	for _, b := range m.bots {
		if !b.active() {
			continue
		}
		s, ok := b.exec.SpecOf(b.Params.Instrument)
		if !ok {
			continue
		}
		bid, ask, ok := b.exec.Quote(b.Params.Instrument)
		if !ok {
			continue
		}
		price := mid(bid, ask)
		switch {
		case b.Status == StWaiting:
			m.stepDip(b, price, s)
		case b.Kind == SpotGrid || b.Kind == PerpGrid:
			m.stepGrid(b, price, s)
		case b.Kind == Recur:
			m.stepRecur(b, price, s)
		default:
			m.stepDCA(b, price, s)
		}
		b.LastOK = now
	}
}

// ---------- 存盘 ----------

const schema = `
create table if not exists bots (
  id      bigint primary key,
  usr     bigint not null,
  account bigint not null,
  status  text   not null,
  body    jsonb  not null
);
create index if not exists bots_usr on bots (usr);
create table if not exists bot_prefs (
  usr        bigint primary key,
  rank_order jsonb  not null
);
`

// Attach 接数据库：建表、读回来。停机期间不补（D83 第 11 条）：写一条停了多久，从现在接着跑。
func (m *Manager) Attach(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, schema); err != nil {
		return err
	}
	m.pool = pool
	rows, err := pool.Query(ctx, `select usr, body from bots order by id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	for rows.Next() {
		var usr int64
		var raw []byte
		if err := rows.Scan(&usr, &raw); err != nil {
			return err
		}
		b := &Bot{}
		if err := json.Unmarshal(raw, b); err != nil {
			continue
		}
		b.User = usr
		b.exec = m.execOf(b.Params.Instrument)
		m.bots[b.ID] = b
		if b.ID >= m.next {
			m.next = b.ID + 1
		}
		if b.active() {
			m.setBusy(b, true)
			if gap := now - b.LastOK; gap > 60_000 {
				b.rec(Rec{Kind: "down", Note: strconv.FormatInt(gap/60_000, 10)}, now)
				if b.Kind == Recur && b.NextAt < now {
					b.NextAt = nextAt(now, b.Params.Every)
				}
			}
		}
	}
	pr, err := pool.Query(ctx, `select usr, rank_order from bot_prefs`)
	if err != nil {
		return err
	}
	defer pr.Close()
	for pr.Next() {
		var usr int64
		var raw []byte
		if pr.Scan(&usr, &raw) == nil {
			var o []string
			if json.Unmarshal(raw, &o) == nil {
				m.busyMu.Lock()
				m.ranks[usr] = o
				m.busyMu.Unlock()
			}
		}
	}
	return rows.Err()
}

func (m *Manager) save(b *Bot) {
	b.dirty = false
	if m.pool == nil {
		return
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return
	}
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := m.pool.Exec(c, `insert into bots (id,usr,account,status,body) values ($1,$2,$3,$4,$5) on conflict (id) do update set status=excluded.status, body=excluded.body`, b.ID, b.User, b.Account, b.Status, raw); err != nil && m.Log != nil {
		m.Log.Warn("存机器人失败", "err", err)
	}
}

// Run 一直跑。
func (m *Manager) Run(ctx context.Context) {
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	n := 0
	for {
		select {
		case <-ctx.Done():
			m.saveAll(true)
			return
		case <-tick.C:
			m.Tick()
			n++
			if n%6 == 0 {
				m.saveAll(n%120 == 0)
			}
		}
	}
}

func (m *Manager) saveAll(force bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, b := range m.bots {
		if b.dirty || (force && b.active()) {
			m.save(b)
		}
	}
}

// DeleteAccount 账户删除时一起删（D83 第 14 条）。
func (m *Manager) DeleteUser(ctx context.Context, user int64) {
	m.mu.Lock()
	for id, b := range m.bots {
		if b.User == user {
			m.cancelAll(b)
			m.setBusy(b, false)
			delete(m.bots, id)
		}
	}
	m.mu.Unlock()
	m.busyMu.Lock()
	delete(m.ranks, user)
	m.busyMu.Unlock()
	if m.pool != nil {
		_, _ = m.pool.Exec(ctx, `delete from bots where usr = $1`, user)
		_, _ = m.pool.Exec(ctx, `delete from bot_prefs where usr = $1`, user)
	}
}

// AccountArchived 账户重置：这个账户的机器人都停掉（引擎那边已经撤单）。
func (m *Manager) AccountArchived(acct int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, b := range m.bots {
		if b.Account == acct && b.Status != StStopped {
			b.Status, b.Reason, b.Stopped = StStopped, "ACCOUNT_RESET", m.now()
			m.setBusy(b, false)
			m.save(b)
		}
	}
}
