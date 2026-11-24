package live

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"qs/internal/binance"
	"qs/internal/book"
	"qs/internal/trading"
)

var zero = decimal.Zero

// Endpoints 各个环境的地址。
type Endpoints struct{ FAPI, SAPI, WS string }

// Desks 所有实盘账户的交易台。实现 trading.Desk：交易页的接口按账户编号分到这里。
type Desks struct {
	S     *Store
	Specs *trading.Specs
	Books *book.Store // 预览用（估成交价）：真实的币安盘口
	Venue string
	Log   *slog.Logger
	// Guard 下单前的检查（纪律锁、机器人优先级），和模拟盘同一个。没接时不检查
	Guard func(p trading.PreOrder) error
	// URLs 每个环境的地址：main（正式）、testnet（测试网）、fake（测试环境的假交易所）
	URLs map[string]Endpoints
	// OnFill 实盘成交了（高光、通知用）
	OnFill func(acct int64)
	// OnAlert 对账发现和交易所对不上（recon.go）：给账户的主人发通知
	OnAlert func(user int64, params map[string]string)

	mu  sync.Mutex
	run map[int64]*desk
	ctx context.Context
}

type tpsl struct {
	tp, sl decimal.Decimal
	by     string
}

type desk struct {
	a       Account
	c       *binance.Client
	env     string
	mu      sync.Mutex
	view    *trading.AccountView
	at      time.Time
	err     error
	stream  bool
	why     string
	seq     int64
	pending map[int64]tpsl  // 下单时带的止盈止损：成交后再挂（币安的条件单要有仓位才能 closePosition）
	stops   map[string]bool // 上次看到挂着的止损单（trackStops）
	kick    chan struct{}
	fee     map[string]feeRate // 每个品种的真实费率
	notes   []trading.Notice   // 纪律锁等系统提醒（只在内存里，重启后交易所那边没有这些）
	stop    context.CancelFunc
	rc      recon // 对账差异告警的状态（recon.go），用 mu 保护
}

// Start 开始管所有实盘账户。
func (d *Desks) Start(ctx context.Context) error {
	d.mu.Lock()
	d.ctx = ctx
	if d.run == nil {
		d.run = map[int64]*desk{}
	}
	d.mu.Unlock()
	list, err := d.S.Accounts(ctx, 0)
	if err != nil {
		return err
	}
	for _, a := range list {
		d.launch(a)
	}
	return nil
}

func (d *Desks) logf(msg string, kv ...any) {
	if d.Log != nil {
		d.Log.Warn(msg, kv...)
	}
}

// launch 给一个实盘账户起客户端、用户数据流和定时对账。
func (d *Desks) launch(a Account) {
	d.mu.Lock()
	if d.run == nil {
		d.run = map[int64]*desk{}
	}
	if _, ok := d.run[a.ID]; ok || d.ctx == nil {
		d.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(d.ctx)
	k := &desk{a: a, pending: map[int64]tpsl{}, kick: make(chan struct{}, 1), stop: cancel}
	d.run[a.ID] = k
	d.mu.Unlock()
	env, key, secret, err := d.S.secretOf(ctx, a.KeyID)
	if err != nil {
		k.err = err
		return
	}
	ep, ok := d.URLs[env]
	if !ok {
		k.err = fmt.Errorf("unknown env %s", env)
		return
	}
	k.env = env
	k.c = &binance.Client{FAPI: ep.FAPI, SAPI: ep.SAPI, WS: ep.WS, Key: key, Secret: secret}
	go k.c.Stream(ctx, func(ev binance.Event) { d.onEvent(ctx, k, ev) }, func(ok bool, why string) {
		k.mu.Lock()
		k.stream, k.why = ok, why
		k.mu.Unlock()
		if ok {
			d.poke(k)
		}
	})
	go d.loop(ctx, k)
}

// Stop 停掉一个账户（删除、停用时）。
func (d *Desks) Stop(acct int64) {
	d.mu.Lock()
	k := d.run[acct]
	delete(d.run, acct)
	d.mu.Unlock()
	if k != nil && k.stop != nil {
		k.stop()
	}
}

func (d *Desks) poke(k *desk) {
	select {
	case k.kick <- struct{}{}:
	default:
	}
}

// loop 收到推送后 300 毫秒内对一次账；没有推送时每 15 秒对一次；成交和资金费每分钟补一次（防止推送丢了）。
// 一次对账大约用 60 的请求权重，每分钟 4 次约 240，币安的上限是每分钟 2400。
func (d *Desks) loop(ctx context.Context, k *desk) {
	tick := time.NewTicker(reconTick.get())
	defer tick.Stop()
	slow := time.NewTicker(time.Minute)
	defer slow.Stop()
	d.refresh(ctx, k)
	d.syncFills(ctx, k)
	for {
		select {
		case <-ctx.Done():
			return
		case <-k.kick:
			time.Sleep(300 * time.Millisecond)
			d.refresh(ctx, k)
		case <-tick.C:
			d.refresh(ctx, k)
		case <-slow.C:
			d.syncFills(ctx, k)
		}
	}
}

func (d *Desks) desk(acct int64) (*desk, error) {
	d.mu.Lock()
	k := d.run[acct]
	d.mu.Unlock()
	if k == nil {
		return nil, trading.Rej("NOT_FOUND")
	}
	if k.c == nil {
		return nil, trading.Rej("LIVE_KEY_ERROR")
	}
	return k, nil
}

// ---------- 编号、品种、方向 ----------

func symOf(inst string) string { return strings.TrimSuffix(inst, "-PERP") + "USDT" }
func instOf(sym string) string { return strings.TrimSuffix(sym, "USDT") + "-PERP" }
func sideUp(s string) string   { return strings.ToUpper(s) }
func sideLow(s string) string  { return strings.ToLower(s) }
func dec(n binance.Num) decimal.Decimal {
	v, err := decimal.NewFromString(string(n))
	if err != nil {
		return zero
	}
	return v
}

// clientID 我们下的单都带来源，复盘按来源分（手动、AI、机器人、信号……）。
func clientID(source string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	code := map[string]string{"manual": "m", "close": "c", "ai": "a", "follow": "f", "bot": "b", "signal": "s", "tp": "t", "sl": "l", "risk": "r"}[source]
	if code == "" {
		code = "m"
	}
	return "qs" + code + "-" + hex.EncodeToString(b)
}

func sourceOf(client string) string {
	if !strings.HasPrefix(client, "qs") || len(client) < 4 || client[3] != '-' {
		return "external" // 不是从这里下的（用户在币安 App 上自己下的）
	}
	return map[byte]string{'m': "manual", 'c': "close", 'a': "ai", 'f': "follow", 'b': "bot", 's': "signal", 't': "tp", 'l': "sl", 'r': "risk"}[client[2]]
}

// ---------- 对账：把交易所的状态读一遍 ----------

func (d *Desks) refresh(ctx context.Context, k *desk) {
	c, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var (
		acc   *binance.Account
		pos   []binance.Position
		cfg   []binance.SymbolConfig
		ords  []binance.Order
		algos []binance.AlgoOrder
		errs  [5]error
		wg    sync.WaitGroup
	)
	wg.Add(5)
	go func() { defer wg.Done(); acc, errs[0] = k.c.Account(c) }()
	go func() { defer wg.Done(); pos, errs[1] = k.c.Positions(c) }()
	go func() { defer wg.Done(); cfg, errs[2] = k.c.SymbolConfigs(c) }()
	go func() { defer wg.Done(); ords, errs[3] = k.c.OpenOrders(c) }()
	go func() { defer wg.Done(); algos, errs[4] = k.c.OpenAlgoOrders(c) }()
	wg.Wait()
	for _, e := range errs {
		if e != nil {
			k.mu.Lock()
			k.err, k.at = e, time.Now()
			k.mu.Unlock()
			d.checkFail(k, e)
			return
		}
	}
	d.checkFail(k, nil)
	sums := d.sums(ctx, k.a.ID)
	v := d.build(k, acc, pos, cfg, ords, algos, sums)
	d.trackStops(ctx, k, v.Orders)
	d.checkMoney(ctx, k, v)
	d.checkTrades(ctx, k)
	// 自动减仓排队（18 第 6.1 条）：读交易所的；读不到不影响别的（只是这一项空着）
	if len(v.Positions) > 0 {
		if adl, err := k.c.ADLQuantile(c); err == nil {
			for i := range v.Positions {
				v.Positions[i].ADL = adl[symOf(v.Positions[i].Instrument)]
			}
		}
	}
	k.mu.Lock()
	k.seq++
	v.Seq = k.seq
	k.view, k.at, k.err = v, time.Now(), nil
	k.mu.Unlock()
}

type sums struct{ realized, fees, funding decimal.Decimal }

func (d *Desks) sums(ctx context.Context, acct int64) sums {
	var s sums
	if d.S.pool == nil {
		return s
	}
	_ = d.S.pool.QueryRow(ctx, `select coalesce(sum(realized),0), coalesce(sum(case when fee_asset='USDT' then fee else 0 end),0) from live_fills where account=$1`, acct).Scan(&s.realized, &s.fees)
	_ = d.S.pool.QueryRow(ctx, `select coalesce(sum(amount),0) from live_funding where account=$1`, acct).Scan(&s.funding)
	return s
}

func (d *Desks) build(k *desk, acc *binance.Account, pos []binance.Position, cfg []binance.SymbolConfig, ords []binance.Order, algos []binance.AlgoOrder, sm sums) *trading.AccountView {
	wallet := dec(acc.TotalWalletBalance)
	mb := dec(acc.TotalMarginBalance)
	v := &trading.AccountView{
		ID: k.a.ID, Wallet: wallet, Equity: mb, UPnL: dec(acc.TotalUnrealizedProfit), Available: dec(acc.AvailableBalance),
		Transferable: decimal.Max(zero, dec(acc.MaxWithdrawAmount)), PositionMargin: dec(acc.TotalPositionInitialMargin),
		OrderMargin: dec(acc.TotalOpenOrderInitialMargin), MaintMargin: dec(acc.TotalMaintMargin),
		Realized: sm.realized, Fees: sm.fees, Funding: sm.funding,
		Positions: []trading.PosView{}, Orders: []*trading.Order{}, Settings: map[string]*trading.Setting{}, Notices: []trading.Notice{},
	}
	v.Initial = wallet.Sub(sm.realized).Sub(sm.funding).Add(sm.fees)
	if mb.Sign() > 0 {
		v.MarginRatio = v.MaintMargin.Div(mb).Round(6)
	}
	for _, c := range cfg {
		mode := "cross"
		if c.MarginType == "ISOLATED" {
			mode = "isolated"
		}
		v.Settings[instOf(c.Symbol)] = &trading.Setting{Leverage: c.Leverage, Mode: mode}
	}
	tp, sl := map[string]decimal.Decimal{}, map[string]decimal.Decimal{}
	for _, a := range algos {
		o := algoOrder(k.a.ID, a)
		v.Orders = append(v.Orders, o)
		if a.ClosePosition {
			if o.Type == trading.TakeProfit {
				tp[o.Instrument] = o.TriggerPx
			} else if o.Type == trading.StopMarket {
				sl[o.Instrument] = o.TriggerPx
			}
		}
	}
	for _, o := range ords {
		v.Orders = append(v.Orders, plainOrder(k.a.ID, o))
	}
	sort.Slice(v.Orders, func(i, j int) bool { return v.Orders[i].Created < v.Orders[j].Created })
	for _, p := range pos {
		amt := dec(p.PositionAmt)
		if amt.IsZero() {
			continue
		}
		inst := instOf(p.Symbol)
		set := v.Settings[inst]
		if set == nil {
			set = &trading.Setting{Leverage: trading.DefaultLeverage, Mode: "cross"}
		}
		side := "long"
		if amt.Sign() < 0 {
			side = "short"
		}
		margin := dec(p.InitialMargin)
		if set.Mode == "isolated" {
			margin = dec(p.IsolatedWallet)
		}
		u := dec(p.UnRealizedProfit)
		roe := zero
		if im := dec(p.InitialMargin); im.Sign() > 0 {
			roe = u.Div(im).Round(6)
		}
		v.Positions = append(v.Positions, trading.PosView{
			Instrument: inst, Side: side, Qty: amt.Abs(), Entry: dec(p.EntryPrice), Mark: dec(p.MarkPrice), Notional: dec(p.Notional).Abs(),
			UPnL: u, ROE: roe, Margin: margin, Maint: dec(p.MaintMargin), LiqPx: dec(p.LiquidationPrice), Mode: set.Mode, Leverage: set.Leverage,
			TP: tp[inst], SL: sl[inst], Opened: p.UpdateTime,
		})
	}
	sort.Slice(v.Positions, func(i, j int) bool { return v.Positions[i].Instrument < v.Positions[j].Instrument })
	return v
}

func status(s string) string {
	switch s {
	case "NEW":
		return trading.StNew
	case "PARTIALLY_FILLED":
		return trading.StPartial
	case "FILLED":
		return trading.StFilled
	case "CANCELED":
		return trading.StCanceled
	case "TRIGGERED", "TRIGGERING", "FINISHED":
		return trading.StTriggered
	default:
		return trading.StExpired
	}
}

func plainOrder(acct int64, o binance.Order) *trading.Order {
	typ := trading.Limit
	if o.Type == "MARKET" {
		typ = trading.Market
	}
	// 市价单刚下完，币安返回的 avgPrice 有时还是 0（11-18 在币安模拟账户上：提示写"均价 0.00000"）：用成交总额 ÷ 成交量
	avg, filled := dec(o.AvgPrice), dec(o.ExecutedQty)
	if avg.IsZero() && filled.IsPositive() && dec(o.CumQuote).IsPositive() {
		avg = dec(o.CumQuote).Div(filled)
	}
	return &trading.Order{ID: "b" + strconv.FormatInt(o.OrderID, 10), Account: acct, Instrument: instOf(o.Symbol), Side: sideLow(o.Side), Type: typ, TIF: o.TimeInForce,
		Price: dec(o.Price), Qty: dec(o.OrigQty), Filled: filled, AvgPx: avg, ReduceOnly: o.ReduceOnly, ClosePos: o.ClosePosition,
		Status: status(o.Status), Source: sourceOf(o.ClientOrderID), Created: o.Time, Updated: o.UpdateTime}
}

func algoOrder(acct int64, a binance.AlgoOrder) *trading.Order {
	o := &trading.Order{ID: "a" + strconv.FormatInt(a.AlgoID, 10), Account: acct, Instrument: instOf(a.Symbol), Side: sideLow(a.Side),
		Price: dec(a.Price), TriggerPx: dec(a.TriggerPrice), Qty: dec(a.Quantity), ReduceOnly: a.ReduceOnly, ClosePos: a.ClosePosition,
		Status: status(a.AlgoStatus), Source: sourceOf(a.ClientAlgoID), Created: a.CreateTime, Updated: a.UpdateTime, TriggerBy: trading.ByLast}
	if a.WorkingType == "MARK_PRICE" {
		o.TriggerBy = trading.ByMark
	}
	switch {
	case a.ClosePosition && (a.OrderType == "STOP_MARKET" || a.OrderType == "STOP"):
		o.Type = trading.StopMarket
	case a.ClosePosition && (a.OrderType == "TAKE_PROFIT_MARKET" || a.OrderType == "TAKE_PROFIT"):
		o.Type = trading.TakeProfit
	case a.OrderType == "STOP" || a.OrderType == "TAKE_PROFIT":
		o.Type = trading.CondLimit
	default:
		o.Type = trading.CondMarket
	}
	// 买入 STOP 是涨到触发价，买入 TAKE_PROFIT 是跌到触发价；卖出反过来
	up := a.OrderType == "STOP_MARKET" || a.OrderType == "STOP"
	if a.Side == "SELL" {
		up = !up
	}
	o.TriggerDir = "down"
	if up {
		o.TriggerDir = "up"
	}
	return o
}

// ---------- 错误码 ----------

// reject 币安的错误码翻成和模拟盘一样的拒单原因，界面用同一套词条。
func reject(err error) error {
	if err == nil {
		return nil
	}
	var r *trading.Reject
	if errors.As(err, &r) {
		return err
	}
	// 被币安按 IP 限流：说清楚要等多久（11-18：三套后端同时补历史把 IP 撞封了，实盘下单也一起被拒）
	if t, ok := binance.Throttled(err); ok {
		return trading.Rej("EXCHANGE_THROTTLED", "min", strconv.Itoa(t.Minutes()))
	}
	c := binance.Code(err)
	// 交易所的拒单没有模拟盘那么多细节（比如差多少保证金），用实盘自己的词条，不留空的占位
	m := map[int]string{-2019: "LIVE_MARGIN", -2022: "REDUCE_ONLY_NO_POSITION", -4164: "LIVE_MIN_NOTIONAL", -4003: "LIVE_QTY_SMALL",
		-4005: "LIVE_QTY_LARGE", -4016: "LIVE_PRICE_RANGE", -4024: "LIVE_PRICE_RANGE", -4131: "LIVE_PRICE_RANGE", -2021: "LIVE_TRIGGER_NOW",
		-2011: "LIVE_ORDER_GONE", -2013: "LIVE_ORDER_GONE", -1003: "LIVE_RATE_LIMIT", -1015: "LIVE_RATE_LIMIT", -1021: "LIVE_CLOCK", -2015: "KEY_REJECTED",
		-4048: "MODE_HAS_POSITION", -4028: "LIVE_LEVERAGE", -2027: "LIVE_POSITION_LIMIT", -5022: "LIVE_POST_ONLY", -4046: "LIVE_NO_CHANGE",
		-4061: "LIVE_DUAL_SIDE"} // 接入以后在币安那边改成了双向持仓（18 第 9.4 条：只支持单向）
	if code, ok := m[c]; ok {
		return trading.Rej(code)
	}
	msg := err.Error()
	var be *binance.APIError
	if errors.As(err, &be) {
		msg = be.Msg
	}
	if len(msg) > 120 {
		msg = msg[:120]
	}
	return trading.Rej("EXCHANGE_ERROR", "code", strconv.Itoa(c), "msg", msg)
}

// ---------- trading.Desk ----------

func (d *Desks) View(acct int64) (*trading.AccountView, error) {
	k, err := d.desk(acct)
	if err != nil {
		return nil, err
	}
	k.mu.Lock()
	v, e := k.view, k.err
	k.mu.Unlock()
	// 平时由后台对账（每 15 秒、收到推送 300 毫秒后），这里只在一次都还没读到时等一次。读到过就直接给：
	// 网络断了对账失败时，旧的数加上界面上"对账失败"的状态，比让调用方（页面、跟随引擎）干等最长 15 秒好（18 第 9.1 条）
	if v == nil {
		d.refresh(d.ctx, k)
		k.mu.Lock()
		v, e = k.view, k.err
		k.mu.Unlock()
	}
	if v == nil {
		return nil, reject(e)
	}
	c := d.live(v)
	k.mu.Lock()
	c.Notices = append(append([]trading.Notice{}, c.Notices...), k.notes...)
	if !k.stream {
		c.Notices = append(append([]trading.Notice{}, c.Notices...), trading.Notice{Kind: "live_stream_down", Reason: k.why})
	}
	k.mu.Unlock()
	return c, nil
}

// live 两次对账之间，仓位的标记价和浮动盈亏按实时行情（同一个币安的标记价）重算，界面不会一跳一跳。
// 余额、保证金、强平价还是交易所给的。
func (d *Desks) live(v *trading.AccountView) *trading.AccountView {
	c := *v
	c.Positions = append(make([]trading.PosView, 0, len(v.Positions)), v.Positions...) // 空的也要是 []，界面按数组读
	if d.Books == nil {
		return &c
	}
	du := zero
	for i := range c.Positions {
		p := &c.Positions[i]
		t, ok := d.Books.Ticker(d.Venue, p.Instrument)
		if !ok || t.Mark.Sign() <= 0 {
			continue
		}
		dir := decimal.NewFromInt(1)
		if p.Side == "short" {
			dir = dir.Neg()
		}
		u := t.Mark.Sub(p.Entry).Mul(p.Qty).Mul(dir)
		if p.Mode == "cross" {
			du = du.Add(u.Sub(p.UPnL))
		}
		c.UPnL = c.UPnL.Add(u.Sub(p.UPnL))
		p.Mark, p.UPnL, p.Notional = t.Mark, u, t.Mark.Mul(p.Qty)
		if lev := p.Leverage; lev > 0 && p.Notional.Sign() > 0 {
			p.ROE = u.Div(p.Entry.Mul(p.Qty).Div(decimal.NewFromInt(int64(lev)))).Round(6)
		}
	}
	c.Equity = c.Wallet.Add(c.UPnL)
	c.Available = c.Available.Add(du)
	if c.Equity.Sign() > 0 {
		c.MarginRatio = c.MaintMargin.Div(c.Equity).Round(6)
	}
	return &c
}

// qtyOf 下单数量：按数量、按 USDT、按可用保证金的百分比（和模拟盘一样的三种）。按数量单位向下取整。
func (d *Desks) qtyOf(k *desk, r trading.OrderReq, px decimal.Decimal) (decimal.Decimal, error) {
	spec, ok := d.Specs.Get(r.Instrument)
	if !ok {
		return zero, trading.Rej("BAD_INSTRUMENT")
	}
	q := r.Qty
	switch {
	case q.Sign() > 0:
	case r.Notional.Sign() > 0 && px.Sign() > 0:
		q = r.Notional.Div(px)
	case r.MarginPct.Sign() > 0 && px.Sign() > 0:
		v, err := d.View(k.a.ID)
		if err != nil {
			return zero, err
		}
		lev := trading.DefaultLeverage
		if s := v.Settings[r.Instrument]; s != nil {
			lev = s.Leverage
		}
		q = decimal.Max(zero, v.Available).Mul(r.MarginPct).Div(decimal.NewFromInt(100)).Mul(decimal.NewFromInt(int64(lev))).Div(px.Mul(decimal.RequireFromString("1.0005")))
	default:
		return zero, trading.Rej("QTY_REQUIRED")
	}
	q = spec.RoundQty(q)
	if q.LessThan(spec.MinQty) || q.IsZero() {
		return zero, trading.Rej("QTY_TOO_SMALL", "min", spec.MinQty.String())
	}
	return q, nil
}

func (d *Desks) refPx(r trading.OrderReq) decimal.Decimal {
	if r.Price.Sign() > 0 {
		return r.Price
	}
	if r.TriggerPx.Sign() > 0 {
		return r.TriggerPx
	}
	if d.Books != nil {
		if b, ok := d.Books.Book(d.Venue, r.Instrument); ok {
			if r.Side == "buy" && len(b.Asks) > 0 {
				return b.Asks[0].Px
			}
			if r.Side == "sell" && len(b.Bids) > 0 {
				return b.Bids[0].Px
			}
		}
		if t, ok := d.Books.Ticker(d.Venue, r.Instrument); ok && t.Mark.Sign() > 0 {
			return t.Mark
		}
	}
	return zero
}

func (d *Desks) armed(ctx context.Context, k *desk) error {
	var ok bool
	if err := d.S.pool.QueryRow(ctx, `select armed from live_accounts where id=$1`, k.a.ID).Scan(&ok); err != nil || !ok {
		return trading.Rej(ErrNotArmed.Error())
	}
	return nil
}

func (d *Desks) Place(acct int64, r trading.OrderReq) (*trading.Order, error) {
	k, err := d.desk(acct)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(d.ctx, 15*time.Second)
	defer cancel()
	// 没开启（还没确认是真钱，或者二级密码重置后回到未开启，D107）：只拦开新仓。减仓、平仓照常，
	// 不然手上的仓位平不掉、策略的止损退出也做不了
	if !r.ReduceOnly {
		if err := d.armed(ctx, k); err != nil {
			return nil, err
		}
	}
	if r.Side != "buy" && r.Side != "sell" {
		return nil, trading.Rej("BAD_SIDE")
	}
	px := d.refPx(r)
	q, err := d.qtyOf(k, r, px)
	if err != nil {
		return nil, err
	}
	src := r.SourceTag()
	if d.Guard != nil && !r.ReduceOnly {
		eq, reduce := zero, false
		if v, err := d.View(acct); err == nil {
			eq = v.Equity
			for _, p := range v.Positions { // 只是减仓：不拦（和模拟盘一样）
				if p.Instrument == r.Instrument && (p.Side == "long") != (r.Side == "buy") && q.LessThanOrEqual(p.Qty) {
					reduce = true
				}
			}
		}
		gs := src
		if r.Type == trading.CondMarket || r.Type == trading.CondLimit {
			gs = "cond"
		}
		if err := d.Guard(trading.PreOrder{Account: acct, Instrument: r.Instrument, Side: r.Side, Source: gs, Qty: q, Price: px, Stop: r.SL, Equity: eq}); err != nil && !reduce {
			return nil, err
		}
	}
	p := url.Values{"symbol": {symOf(r.Instrument)}, "side": {sideUp(r.Side)}, "quantity": {q.String()}}
	if r.ReduceOnly {
		p.Set("reduceOnly", "true")
	}
	switch r.Type {
	case trading.Market:
		p.Set("type", "MARKET")
	case trading.Limit:
		tif := r.TIF
		if tif == "" {
			tif = trading.GTC
		}
		p.Set("type", "LIMIT")
		p.Set("timeInForce", tif)
		if r.PriceMatch != "" { // 按盘口定价（D103）：交易所定价，不能再带价格
			p.Set("priceMatch", r.PriceMatch)
		} else {
			p.Set("price", r.Price.String())
		}
		if tif == trading.GTD {
			p.Set("goodTillDate", strconv.FormatInt(r.GoodTill, 10))
		}
	case trading.CondMarket, trading.CondLimit:
		return d.placeCond(ctx, k, r, q, src)
	default:
		return nil, trading.Rej("BAD_TYPE")
	}
	p.Set("newClientOrderId", clientID(src))
	o, err := k.c.NewOrder(ctx, p)
	if err != nil {
		return nil, reject(err)
	}
	out := plainOrder(acct, *o)
	// 成交了但交易所还没给均价（币安模拟环境市价单常见，成交总额也是 0）：再查一次这张单，最多等 2 秒（11-18：提示原来写"均价 0.00000"）
	if out.AvgPx.IsZero() && out.Filled.IsPositive() {
		qc, cancel := context.WithTimeout(ctx, 2*time.Second)
		if q, err := k.c.QueryOrder(qc, o.Symbol, o.OrderID); err == nil {
			if a := plainOrder(acct, *q).AvgPx; a.IsPositive() {
				out.AvgPx = a
			}
		}
		cancel()
	}
	out.Source = src
	d.recordOrder(ctx, acct, out, string(p.Get("newClientOrderId")))
	if r.TP.Sign() > 0 || r.SL.Sign() > 0 {
		if o.Status == "FILLED" {
			_ = d.attachTPSL(ctx, k, r.Instrument, r.TP, r.SL, r.TriggerBy)
		} else {
			k.mu.Lock()
			k.pending[o.OrderID] = tpsl{tp: r.TP, sl: r.SL, by: r.TriggerBy}
			k.mu.Unlock()
		}
	}
	d.poke(k)
	return out, nil
}

// placeCond 条件开仓单：走 Algo 接口。触发方向决定是 STOP 还是 TAKE_PROFIT（买入涨到触发价是 STOP，跌到是 TAKE_PROFIT）。
func (d *Desks) placeCond(ctx context.Context, k *desk, r trading.OrderReq, q decimal.Decimal, src string) (*trading.Order, error) {
	if r.TriggerPx.Sign() <= 0 {
		return nil, trading.Rej("TRIGGER_TICK")
	}
	ref := d.refPx(trading.OrderReq{Instrument: r.Instrument, Side: r.Side})
	up := r.TriggerPx.GreaterThan(ref)
	stop := (r.Side == "buy") == up
	typ := "TAKE_PROFIT"
	if stop {
		typ = "STOP"
	}
	if r.Type == trading.CondMarket {
		typ += "_MARKET"
	}
	p := url.Values{"symbol": {symOf(r.Instrument)}, "side": {sideUp(r.Side)}, "type": {typ}, "quantity": {q.String()},
		"triggerPrice": {r.TriggerPx.String()}, "workingType": {working(r.TriggerBy)}, "clientAlgoId": {clientID(src)}}
	if r.Type == trading.CondLimit {
		p.Set("price", r.Price.String())
		p.Set("timeInForce", "GTC")
	}
	if r.PriceProtect { // 条件单价格保护（D103），和模拟盘同一个开关
		p.Set("priceProtect", "TRUE")
	}
	if r.ReduceOnly {
		p.Set("reduceOnly", "true")
	}
	a, err := k.c.NewAlgo(ctx, p)
	if err != nil {
		return nil, reject(err)
	}
	o := algoOrder(k.a.ID, *a)
	o.Source = src
	d.recordOrder(ctx, k.a.ID, o, p.Get("clientAlgoId"))
	d.poke(k)
	return o, nil
}

func working(by string) string {
	if by == trading.ByLast {
		return "CONTRACT_PRICE"
	}
	return "MARK_PRICE" // 标记价（默认）；币安没有按指数价触发，指数价也按标记价
}

// attachTPSL 给仓位挂止盈止损（closePosition，整仓）：先撤掉原来的，再挂新的。传 0 表示不要这一边。
func (d *Desks) attachTPSL(ctx context.Context, k *desk, inst string, tp, sl decimal.Decimal, by string) error {
	algos, err := k.c.OpenAlgoOrders(ctx)
	if err != nil {
		return reject(err)
	}
	pos, err := k.c.Positions(ctx)
	if err != nil {
		return reject(err)
	}
	var amt decimal.Decimal
	for _, p := range pos {
		if p.Symbol == symOf(inst) {
			amt = dec(p.PositionAmt)
		}
	}
	for _, a := range algos {
		if a.Symbol == symOf(inst) && a.ClosePosition {
			isTP := strings.HasPrefix(a.OrderType, "TAKE_PROFIT")
			if (isTP && tp.Sign() >= 0) || (!isTP && sl.Sign() >= 0) {
				_ = k.c.CancelAlgo(ctx, a.AlgoID)
			}
		}
	}
	if amt.IsZero() {
		return nil
	}
	side := "SELL"
	if amt.Sign() < 0 {
		side = "BUY"
	}
	put := func(typ string, px decimal.Decimal, src string) error {
		_, err := k.c.NewAlgo(ctx, url.Values{"symbol": {symOf(inst)}, "side": {side}, "type": {typ}, "closePosition": {"true"},
			"triggerPrice": {px.String()}, "workingType": {working(by)}, "clientAlgoId": {clientID(src)}})
		return reject(err)
	}
	if tp.Sign() > 0 {
		if err := put("TAKE_PROFIT_MARKET", tp, "tp"); err != nil {
			return err
		}
	}
	if sl.Sign() > 0 {
		if err := put("STOP_MARKET", sl, "sl"); err != nil {
			return err
		}
	}
	return nil
}

func (d *Desks) SetTPSL(acct int64, inst string, tp, sl *decimal.Decimal, by string) error {
	k, err := d.desk(acct)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(d.ctx, 15*time.Second)
	defer cancel()
	// 止盈止损只会平仓：没开启时也能设（保护仓位，D107）
	t, s := decimal.NewFromInt(-1), decimal.NewFromInt(-1) // -1：这一边不动
	if tp != nil {
		t = *tp
	}
	if sl != nil {
		s = *sl
	}
	err = d.attachTPSL(ctx, k, inst, t, s, by)
	d.poke(k)
	return err
}

func (d *Desks) Preview(acct int64, r trading.OrderReq) (*trading.Preview, error) {
	k, err := d.desk(acct)
	if err != nil {
		return nil, err
	}
	px := d.refPx(r)
	q, err := d.qtyOf(k, r, px)
	p := &trading.Preview{EstPx: px}
	if err != nil {
		var rj *trading.Reject
		if errors.As(err, &rj) {
			p.Problem = rj
			return p, nil
		}
		return nil, err
	}
	v, err := d.View(acct)
	if err != nil {
		return nil, err
	}
	set := &trading.Setting{Leverage: trading.DefaultLeverage, Mode: "cross"}
	if s := v.Settings[r.Instrument]; s != nil {
		set = s
	}
	levD := decimal.NewFromInt(int64(max(1, set.Leverage)))
	maker, taker := d.fees(k, r.Instrument)
	// 限价单不穿价是挂单（按挂单费率）；穿价、市价按吃单
	p.Maker = r.Type == trading.Limit && !d.crosses(r)
	rate := taker
	if p.Maker {
		rate = maker
	}
	p.Qty, p.Notional = q, q.Mul(px)
	p.Fee = p.Notional.Mul(rate)
	p.Available = v.Available
	// 现在的仓位（带符号）：成交之后变成什么样，减仓部分不占新保证金
	var cur trading.PosView
	signed := zero
	for _, x := range v.Positions {
		if x.Instrument == r.Instrument {
			cur = x
			signed = x.Qty
			if x.Side == "short" {
				signed = signed.Neg()
			}
		}
	}
	buy := r.Side == "buy"
	opening := q
	if !signed.IsZero() && (signed.Sign() > 0) != buy {
		opening = decimal.Max(zero, q.Sub(signed.Abs()))
	}
	if r.ReduceOnly {
		opening = zero
	}
	p.Margin = opening.Mul(px).Div(levD)
	if px.Sign() > 0 {
		p.MaxQty = decimal.Max(zero, v.Available).Mul(levD).Div(px.Mul(decimal.NewFromInt(1).Add(taker)))
		if s, ok := d.Specs.Get(r.Instrument); ok {
			p.MaxQty = s.RoundQty(p.MaxQty)
		}
	}
	if opening.Sign() > 0 && p.Margin.Add(p.Fee).GreaterThan(v.Available) {
		p.Problem = trading.Rej("INSUFFICIENT_MARGIN", "need", p.Margin.Add(p.Fee).Round(2).String(), "available", decimal.Max(zero, v.Available).Round(2).String())
	}
	// 预估强平价：成交后的仓位按币安的公式算（和模拟盘同一个公式）
	nq, ne := trading.AfterFill(signed, cur.Entry, buy, q, px)
	if spec, ok := d.Specs.Get(r.Instrument); ok && !nq.IsZero() && px.Sign() > 0 {
		if set.Mode == "isolated" {
			wb := nq.Abs().Mul(ne).Div(levD)
			if !signed.IsZero() && signed.Sign() == nq.Sign() {
				wb = cur.Margin.Add(p.Margin) // 加仓：原来的逐仓保证金加上新占的
			}
			p.LiqPx = trading.LiqEstimate(spec, nq, ne, wb, zero, zero, px)
		} else {
			wb, tmm, up := v.Wallet, zero, zero
			for _, x := range v.Positions {
				switch {
				case x.Mode == "isolated":
					wb = wb.Sub(x.Margin)
				case x.Instrument != r.Instrument:
					tmm, up = tmm.Add(x.Maint), up.Add(x.UPnL)
				}
			}
			p.LiqPx = trading.LiqEstimate(spec, nq, ne, wb.Sub(p.Fee), tmm, up, px)
		}
	}
	return p, nil
}

// crosses 限价单是不是一挂上就会成交（穿过对手盘）。
func (d *Desks) crosses(r trading.OrderReq) bool {
	if d.Books == nil {
		return false
	}
	b, ok := d.Books.Book(d.Venue, r.Instrument)
	if !ok {
		return false
	}
	if r.Side == "buy" && len(b.Asks) > 0 {
		return r.Price.GreaterThanOrEqual(b.Asks[0].Px)
	}
	if r.Side == "sell" && len(b.Bids) > 0 {
		return r.Price.LessThanOrEqual(b.Bids[0].Px)
	}
	return false
}

type feeRate struct {
	maker, taker decimal.Decimal
	at           time.Time
}

// fees 这个账户在这个品种上的真实费率（含 VIP 等级），从币安读，记 6 小时；读不到时按普通用户的 0.02% / 0.05%。
func (d *Desks) fees(k *desk, inst string) (maker, taker decimal.Decimal) {
	maker, taker = decimal.RequireFromString("0.0002"), decimal.RequireFromString("0.0005")
	k.mu.Lock()
	f, ok := k.fee[inst]
	k.mu.Unlock()
	if ok && time.Since(f.at) < 6*time.Hour {
		return f.maker, f.taker
	}
	ctx, cancel := context.WithTimeout(d.ctx, 5*time.Second)
	defer cancel()
	c, err := k.c.CommissionRate(ctx, symOf(inst))
	if err != nil {
		return
	}
	mk, tk := dec(c.Maker), dec(c.Taker)
	if tk.Sign() <= 0 {
		return
	}
	k.mu.Lock()
	if k.fee == nil {
		k.fee = map[string]feeRate{}
	}
	k.fee[inst] = feeRate{maker: mk, taker: tk, at: time.Now()}
	k.mu.Unlock()
	return mk, tk
}

// parseID "b123" 是普通委托，"a456" 是条件单。
func parseID(id string) (algo bool, n int64, ok bool) {
	if len(id) < 2 || (id[0] != 'a' && id[0] != 'b') {
		return false, 0, false
	}
	n, err := strconv.ParseInt(id[1:], 10, 64)
	return id[0] == 'a', n, err == nil
}

func (d *Desks) find(k *desk, id string) *trading.Order {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.view == nil {
		return nil
	}
	for _, o := range k.view.Orders {
		if o.ID == id {
			c := *o
			return &c
		}
	}
	return nil
}

func (d *Desks) Cancel(acct int64, id string) (*trading.Order, error) {
	k, err := d.desk(acct)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(d.ctx, 15*time.Second)
	defer cancel()
	algo, n, ok := parseID(id)
	o := d.find(k, id)
	if !ok || o == nil {
		return nil, trading.Rej("LIVE_ORDER_GONE")
	}
	if algo {
		err = k.c.CancelAlgo(ctx, n)
	} else {
		_, err = k.c.CancelOrder(ctx, symOf(o.Instrument), n)
	}
	if err != nil {
		return nil, reject(err)
	}
	o.Status = trading.StCanceled
	d.updateOrder(ctx, acct, id, trading.StCanceled, o.Filled, o.AvgPx, "USER")
	d.poke(k)
	return o, nil
}

func (d *Desks) UndoCancel(int64, string) (*trading.Order, error) {
	return nil, trading.Rej(ErrUnsupported.Error()) // 交易所撤掉的单撤不回来；界面在实盘上不显示"撤回"
}

func (d *Desks) CancelAll(acct int64, inst string) (int, error) {
	k, err := d.desk(acct)
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(d.ctx, 20*time.Second)
	defer cancel()
	v, err := d.View(acct)
	if err != nil {
		return 0, err
	}
	syms := map[string]bool{}
	n := 0
	for _, o := range v.Orders {
		if inst == "" || o.Instrument == inst {
			syms[symOf(o.Instrument)] = true
			n++
		}
	}
	for s := range syms {
		if err := k.c.CancelAll(ctx, s); err != nil {
			return 0, reject(err)
		}
		if err := k.c.CancelAllAlgo(ctx, s); err != nil {
			return 0, reject(err)
		}
	}
	d.poke(k)
	return n, nil
}

func (d *Desks) Amend(acct int64, id string, price, qty, trigger decimal.Decimal) (*trading.Order, error) {
	k, err := d.desk(acct)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(d.ctx, 15*time.Second)
	defer cancel()
	algo, n, ok := parseID(id)
	o := d.find(k, id)
	if ok && o == nil {
		// 本地快照还没跟上（刚下的单，推送后 300 毫秒才对账）：去交易所读一次再找（11-16：下完马上改价报"委托已经没了"）
		d.refresh(ctx, k)
		o = d.find(k, id)
	}
	if !ok || o == nil {
		return nil, trading.Rej("LIVE_ORDER_GONE")
	}
	if price.IsZero() {
		price = o.Price
	}
	if qty.IsZero() {
		qty = o.Qty
	}
	if !algo {
		if o.Type != trading.Limit {
			return nil, trading.Rej("NOT_MODIFIABLE")
		}
		x, err := k.c.ModifyOrder(ctx, symOf(o.Instrument), n, sideUp(o.Side), qty.String(), price.String())
		if err != nil {
			return nil, reject(err)
		}
		d.poke(k)
		return plainOrder(acct, *x), nil
	}
	// 条件单：币安不支持改没触发的条件单，只能撤了重下（编号会变）
	if trigger.IsZero() {
		trigger = o.TriggerPx
	}
	if err := k.c.CancelAlgo(ctx, n); err != nil {
		return nil, reject(err)
	}
	if o.ClosePos {
		var tp, sl decimal.Decimal = decimal.NewFromInt(-1), decimal.NewFromInt(-1)
		if o.Type == trading.TakeProfit {
			tp = trigger
		} else {
			sl = trigger
		}
		if err := d.attachTPSL(ctx, k, o.Instrument, tp, sl, o.TriggerBy); err != nil {
			return nil, err
		}
		d.refresh(ctx, k)
		return o, nil
	}
	typ := trading.CondMarket
	if o.Type == trading.CondLimit {
		typ = trading.CondLimit
	}
	return d.placeCond(ctx, k, trading.OrderReq{Instrument: o.Instrument, Side: o.Side, Type: typ, Price: price, TriggerPx: trigger, TriggerBy: o.TriggerBy, ReduceOnly: o.ReduceOnly}, qty, o.Source)
}

func (d *Desks) ClosePosition(acct int64, inst string, fraction decimal.Decimal, typ string, price decimal.Decimal) (*trading.Order, error) {
	v, err := d.View(acct)
	if err != nil {
		return nil, err
	}
	var p *trading.PosView
	for i := range v.Positions {
		if v.Positions[i].Instrument == inst {
			p = &v.Positions[i]
		}
	}
	if p == nil {
		return nil, trading.Rej("NO_POSITION")
	}
	if fraction.Sign() <= 0 || fraction.GreaterThan(decimal.NewFromInt(1)) {
		return nil, trading.Rej("BAD_QTY")
	}
	q := p.Qty.Mul(fraction)
	if s, ok := d.Specs.Get(inst); ok {
		q = s.RoundQty(q)
	}
	side := "sell"
	if p.Side == "short" {
		side = "buy"
	}
	r := trading.OrderReq{Instrument: inst, Side: side, Type: trading.Market, Qty: q, ReduceOnly: true}
	if typ == trading.Limit {
		r.Type, r.Price, r.TIF = trading.Limit, price, trading.GTC
	}
	r.SetSource("close")
	return d.Place(acct, r)
}

func (d *Desks) CloseAll(acct int64) ([]*trading.Order, error) {
	v, err := d.View(acct)
	if err != nil {
		return nil, err
	}
	out := []*trading.Order{}
	for _, p := range v.Positions {
		o, err := d.ClosePosition(acct, p.Instrument, decimal.NewFromInt(1), trading.Market, zero)
		if err != nil {
			return out, err
		}
		out = append(out, o)
	}
	return out, nil
}

func (d *Desks) SetLeverage(acct int64, inst string, l int) error {
	k, err := d.desk(acct)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(d.ctx, 15*time.Second)
	defer cancel()
	err = reject(k.c.SetLeverage(ctx, symOf(inst), l))
	d.poke(k)
	return err
}

func (d *Desks) SetMode(acct int64, inst, mode string) error {
	k, err := d.desk(acct)
	if err != nil {
		return err
	}
	if mode != "cross" && mode != "isolated" {
		return trading.Rej("BAD_MODE")
	}
	ctx, cancel := context.WithTimeout(d.ctx, 15*time.Second)
	defer cancel()
	err = reject(k.c.SetMarginType(ctx, symOf(inst), mode == "isolated"))
	d.poke(k)
	return err
}

func (d *Desks) AdjustMargin(acct int64, inst string, amount decimal.Decimal, dry bool) (*trading.MarginAdjust, error) {
	k, err := d.desk(acct)
	if err != nil {
		return nil, err
	}
	v, err := d.View(acct)
	if err != nil {
		return nil, err
	}
	var p *trading.PosView
	for i := range v.Positions {
		if v.Positions[i].Instrument == inst {
			p = &v.Positions[i]
		}
	}
	if p == nil || p.Mode != "isolated" {
		return nil, trading.Rej("NO_POSITION")
	}
	im := p.Notional.Div(decimal.NewFromInt(int64(max(1, p.Leverage))))
	res := &trading.MarginAdjust{Margin: p.Margin.Add(amount), MaxAdd: decimal.Max(zero, v.Available),
		MaxRemove: decimal.Max(zero, p.Margin.Add(decimal.Min(zero, p.UPnL)).Sub(im)), LiqPx: p.LiqPx}
	if dry || amount.IsZero() {
		return res, nil
	}
	ctx, cancel := context.WithTimeout(d.ctx, 15*time.Second)
	defer cancel()
	if err := k.c.AdjustMargin(ctx, symOf(inst), amount.Abs().String(), amount.Sign() > 0); err != nil {
		return nil, reject(err)
	}
	d.poke(k)
	return res, nil
}

// ---------- 推送 ----------

func (d *Desks) onEvent(ctx context.Context, k *desk, ev binance.Event) {
	switch ev.Type {
	case "ORDER_TRADE_UPDATE":
		u, err := binance.ParseOrderUpdate(ev.Raw)
		if err != nil {
			return
		}
		id := "b" + strconv.FormatInt(u.OrderID, 10)
		d.updateOrder(ctx, k.a.ID, id, status(u.Status), dec(u.CumQty), dec(u.AvgPrice), u.ExecType)
		if u.ExecType == "TRADE" {
			d.saveFill(ctx, k.a.ID, binance.Trade{ID: u.TradeID, OrderID: u.OrderID, Symbol: u.Symbol, Side: u.Side, Price: u.LastPrice, Qty: u.LastQty,
				RealizedPnl: u.Realized, Commission: u.Fee, CommissionAsset: u.FeeAsset, Time: u.TradeTime, Maker: u.Maker}, u.ClientOrderID)
			d.noteTrade(k, u)
			if u.Status == "FILLED" {
				k.mu.Lock()
				t, ok := k.pending[u.OrderID]
				delete(k.pending, u.OrderID)
				k.mu.Unlock()
				if ok {
					if err := d.attachTPSL(ctx, k, instOf(u.Symbol), t.tp, t.sl, t.by); err != nil {
						d.logf("实盘：成交后挂止盈止损失败", "account", k.a.ID, "err", err)
					}
				}
			}
			if d.OnFill != nil {
				d.OnFill(k.a.ID)
			}
		}
		d.poke(k)
	case "ALGO_UPDATE":
		d.noteAlgo(k, ev.Raw)
		d.poke(k)
	case "ACCOUNT_UPDATE", "MARGIN_CALL":
		d.poke(k)
	}
}

// ---------- 成交、资金费、委托的本地记录（复盘、历史用） ----------

func (d *Desks) saveFill(ctx context.Context, acct int64, t binance.Trade, client string) {
	if d.S.pool == nil || t.ID == 0 {
		return
	}
	_, err := d.S.pool.Exec(ctx, `insert into live_fills (account, trade_id, order_id, client_id, symbol, side, qty, px, fee, fee_asset, realized, maker, t)
		values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) on conflict do nothing`,
		acct, t.ID, t.OrderID, client, t.Symbol, sideLow(t.Side), string(t.Qty), string(t.Price), string(t.Commission), t.CommissionAsset, string(t.RealizedPnl), t.Maker, t.Time)
	if err != nil {
		d.logf("实盘：成交没存上", "err", err)
		return
	}
	// 委托表跟着成交记录走（11-16 发现：服务停着时成交的单，补读到了成交，委托却一直写着"新单、成交 0"）
	_, _ = d.S.pool.Exec(ctx, `update live_orders o set filled = f.q, avg_px = f.c / f.q, updated = $4,
		status = case when f.q >= o.qty then 'filled' when o.status = 'new' then 'partially_filled' else o.status end
		from (select sum(qty) q, sum(qty * px) c from live_fills where account = $1 and order_id = $2) f
		where o.account = $1 and o.id = $3 and f.q > o.filled`, acct, t.OrderID, "b"+strconv.FormatInt(t.OrderID, 10), t.Time)
}

func (d *Desks) recordOrder(ctx context.Context, acct int64, o *trading.Order, client string) {
	if d.S.pool == nil {
		return
	}
	now := time.Now().UnixMilli()
	_, _ = d.S.pool.Exec(ctx, `insert into live_orders (account, id, client_id, symbol, side, type, price, trigger, qty, filled, avg_px, status, source, created, updated)
		values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$14) on conflict (account, id) do nothing`,
		acct, o.ID, client, symOf(o.Instrument), o.Side, o.Type, o.Price.String(), o.TriggerPx.String(), o.Qty.String(), o.Filled.String(), o.AvgPx.String(), o.Status, o.Source, now)
}

// isStopLoss 平仓用的止损单：平整个仓位的 STOP，或者只减仓、往亏损方向触发的条件单（卖出跌到 / 买入涨到）。止盈单、开仓用的条件单不算
func isStopLoss(o *trading.Order) bool {
	if o.Type == trading.StopMarket {
		return true
	}
	return o.ReduceOnly && (o.Type == trading.CondMarket || o.Type == trading.CondLimit) &&
		(o.Side == "sell" && o.TriggerDir == "down" || o.Side == "buy" && o.TriggerDir == "up")
}

// trackStops 记下交易所账户上挂着的止损单（不管是在这里下的，还是在币安 App 里下的），复盘才知道这笔有没有设止损、
// 初始止损在哪、有没有往亏损方向挪。原来实盘复盘一律写"没设止损"、算不出 R（11-18 在币安模拟账户上发现）。
// 新出现的记一条（时间用交易所给的创建时间）；之前挂着、现在没了的记成撤掉（触发成交的那一刻已经在平仓之后，复盘不看）
func (d *Desks) trackStops(ctx context.Context, k *desk, orders []*trading.Order) {
	if d.S.pool == nil {
		return
	}
	cur := map[string]bool{}
	for _, o := range orders {
		if !isStopLoss(o) {
			continue
		}
		cur[o.ID] = true
		k.mu.Lock()
		seen := k.stops != nil && k.stops[o.ID]
		k.mu.Unlock()
		if !seen {
			t := o.Created
			if t <= 0 {
				t = time.Now().UnixMilli()
			}
			_, _ = d.S.pool.Exec(ctx, `insert into live_orders (account, id, client_id, symbol, side, type, price, trigger, qty, filled, avg_px, status, source, created, updated)
				values ($1,$2,'',$3,$4,'sl',0,$5,$6,0,0,'new',$7,$8,$8) on conflict (account, id) do nothing`,
				k.a.ID, "sl-"+o.ID, symOf(o.Instrument), o.Side, o.TriggerPx.String(), o.Qty.String(), o.Source, t)
		}
	}
	k.mu.Lock()
	prev := k.stops
	k.stops = cur
	k.mu.Unlock()
	for id := range prev {
		if !cur[id] {
			_, _ = d.S.pool.Exec(ctx, `update live_orders set status='canceled', updated=$3 where account=$1 and id=$2 and status='new'`, k.a.ID, "sl-"+id, time.Now().UnixMilli())
		}
	}
}

// Stops 交易所账户的止损变化（trackStops 记的），给复盘用：挂上记一条，撤掉再记一条
func (d *Desks) Stops(acct int64) []trading.StopRec {
	if d.S.pool == nil {
		return nil
	}
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rows, err := d.S.pool.Query(c, `select symbol, trigger, status, created, updated from live_orders where account=$1 and type='sl' order by created`, acct)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []trading.StopRec
	for rows.Next() {
		var sym, st string
		var px decimal.Decimal
		var cr, up int64
		if rows.Scan(&sym, &px, &st, &cr, &up) != nil {
			continue
		}
		out = append(out, trading.StopRec{Time: cr, Instrument: instOf(sym), Px: px, Kind: "set"})
		if st == "canceled" {
			out = append(out, trading.StopRec{Time: up, Instrument: instOf(sym), Kind: "cancel"})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time < out[j].Time })
	return out
}

func (d *Desks) updateOrder(ctx context.Context, acct int64, id, st string, filled, avg decimal.Decimal, reason string) {
	if d.S.pool == nil {
		return
	}
	_, _ = d.S.pool.Exec(ctx, `update live_orders set status=$3, filled=$4, avg_px=$5, reason=$6, updated=$7 where account=$1 and id=$2`,
		acct, id, st, filled.String(), avg.String(), reason, time.Now().UnixMilli())
}

// syncFills 推送可能丢（断线、重启）：按品种从上次记到的成交往后补，资金费也补。
func (d *Desks) syncFills(ctx context.Context, k *desk) {
	if d.S.pool == nil {
		return
	}
	c, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	syms := map[string]bool{}
	rows, err := d.S.pool.Query(c, `select distinct symbol from live_orders where account=$1 union select distinct symbol from live_fills where account=$1`, k.a.ID)
	if err == nil {
		for rows.Next() {
			var s string
			if rows.Scan(&s) == nil {
				syms[s] = true
			}
		}
		rows.Close()
	}
	k.mu.Lock()
	if k.view != nil {
		for _, p := range k.view.Positions {
			syms[symOf(p.Instrument)] = true
		}
	}
	k.mu.Unlock()
	for s := range syms {
		var last int64
		_ = d.S.pool.QueryRow(c, `select coalesce(max(trade_id),0) from live_fills where account=$1 and symbol=$2`, k.a.ID, s).Scan(&last)
		start := int64(0)
		if last == 0 {
			start = k.a.Created
		}
		ts, err := k.c.UserTrades(c, s, last+boolInt(last > 0), start)
		if err != nil {
			continue
		}
		for _, t := range ts {
			d.saveFill(c, k.a.ID, t, "")
		}
	}
	var lastT int64
	_ = d.S.pool.QueryRow(c, `select coalesce(max(t),0) from live_funding where account=$1`, k.a.ID).Scan(&lastT)
	if lastT == 0 {
		lastT = k.a.Created
	}
	if inc, err := k.c.Funding(c, lastT+1); err == nil {
		for _, x := range inc {
			_, _ = d.S.pool.Exec(c, `insert into live_funding (account, tran_id, symbol, amount, t) values ($1,$2,$3,$4,$5) on conflict do nothing`,
				k.a.ID, x.TranID, x.Symbol, string(x.Income), x.Time)
		}
	}
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// Records 复盘用：本地记下的成交和资金费，换成和模拟盘一样的格式（交易日志、绩效、高光都能用）。
func (d *Desks) Records(acct int64) ([]trading.Fill, []trading.FundingRec, decimal.Decimal, error) {
	if _, err := d.desk(acct); err != nil {
		return nil, nil, zero, err
	}
	ctx, cancel := context.WithTimeout(d.ctx, 20*time.Second)
	defer cancel()
	rows, err := d.S.pool.Query(ctx, `select f.seq, f.t, f.order_id, coalesce(nullif(f.client_id,''), o.client_id, ''), f.symbol, f.side, f.qty::text, f.px::text, f.fee::text, f.fee_asset, f.realized::text, f.maker
		from live_fills f left join live_orders o on o.account=f.account and o.id='b'||f.order_id where f.account=$1 order by f.seq`, acct)
	if err != nil {
		return nil, nil, zero, err
	}
	var fills []trading.Fill
	for rows.Next() {
		var f trading.Fill
		var oid int64
		var client, sym, qty, px, fee, asset, rz string
		if err := rows.Scan(&f.Seq, &f.Time, &oid, &client, &sym, &f.Side, &qty, &px, &fee, &asset, &rz, &f.Maker); err != nil {
			rows.Close()
			return nil, nil, zero, err
		}
		f.OrderID, f.Instrument = "b"+strconv.FormatInt(oid, 10), instOf(sym)
		f.Qty, f.Px, f.Realized = decimal.RequireFromString(qty), decimal.RequireFromString(px), decimal.RequireFromString(rz)
		if asset == "USDT" {
			f.Fee = decimal.RequireFromString(fee)
		}
		f.Source = sourceOf(client)
		fills = append(fills, f)
	}
	rows.Close()
	rows, err = d.S.pool.Query(ctx, `select tran_id, t, symbol, amount::text from live_funding where account=$1 order by t`, acct)
	if err != nil {
		return nil, nil, zero, err
	}
	defer rows.Close()
	var fund []trading.FundingRec
	for rows.Next() {
		var r trading.FundingRec
		var sym, amt string
		if err := rows.Scan(&r.Seq, &r.Time, &sym, &amt); err != nil {
			return nil, nil, zero, err
		}
		r.Instrument, r.Amount = instOf(sym), decimal.RequireFromString(amt)
		fund = append(fund, r)
	}
	return fills, fund, zero, rows.Err()
}

func (d *Desks) History(acct int64, kind string, page, size int) (*trading.Page, error) {
	if _, err := d.desk(acct); err != nil {
		return nil, err
	}
	if size <= 0 || size > 200 {
		size = 50
	}
	if page < 1 {
		page = 1
	}
	ctx, cancel := context.WithTimeout(d.ctx, 15*time.Second)
	defer cancel()
	off := (page - 1) * size
	switch kind {
	case "fills":
		fills, _, _, err := d.Records(acct)
		if err != nil {
			return nil, err
		}
		return pageOf(len(fills), off, size, func(i int) any { return fills[len(fills)-1-i] }), nil
	case "funding":
		_, fund, _, err := d.Records(acct)
		if err != nil {
			return nil, err
		}
		return pageOf(len(fund), off, size, func(i int) any { return fund[len(fund)-1-i] }), nil
	case "orders":
		var total int
		_ = d.S.pool.QueryRow(ctx, `select count(*) from live_orders where account=$1 and status not in ('new','partially_filled')`, acct).Scan(&total)
		rows, err := d.S.pool.Query(ctx, `select id, symbol, side, type, price::text, trigger::text, qty::text, filled::text, avg_px::text, status, source, reason, created, updated
			from live_orders where account=$1 and status not in ('new','partially_filled') order by created desc limit $2 offset $3`, acct, size, off)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []*trading.Order{}
		for rows.Next() {
			o := &trading.Order{Account: acct}
			var sym, px, trig, qty, filled, avg string
			if err := rows.Scan(&o.ID, &sym, &o.Side, &o.Type, &px, &trig, &qty, &filled, &avg, &o.Status, &o.Source, &o.Reason, &o.Created, &o.Updated); err != nil {
				return nil, err
			}
			o.Instrument = instOf(sym)
			o.Price, o.TriggerPx, o.Qty, o.Filled, o.AvgPx = decimal.RequireFromString(px), decimal.RequireFromString(trig), decimal.RequireFromString(qty), decimal.RequireFromString(filled), decimal.RequireFromString(avg)
			out = append(out, o)
		}
		return &trading.Page{Total: total, Items: out}, rows.Err()
	}
	return nil, trading.Rej("BAD_REQUEST")
}

func pageOf(n, off, size int, at func(int) any) *trading.Page {
	items := []any{}
	for i := off; i < n && i < off+size; i++ {
		items = append(items, at(i))
	}
	return &trading.Page{Total: n, Items: items}
}

func (d *Desks) OrderFills(acct int64, id string) ([]trading.Fill, error) {
	fills, _, _, err := d.Records(acct)
	if err != nil {
		return nil, err
	}
	out := []trading.Fill{}
	for _, f := range fills {
		if f.OrderID == id {
			out = append(out, f)
		}
	}
	return out, nil
}

// Status 给界面看：用户数据流连着没有、最近一次对账的错误。
type Status struct {
	Stream bool   `json:"stream"`
	Why    string `json:"why,omitempty"`
	Error  string `json:"error,omitempty"`
	At     int64  `json:"at"`
	Env    string `json:"env"`
}

func (d *Desks) Status(acct int64) Status {
	d.mu.Lock()
	k := d.run[acct]
	d.mu.Unlock()
	if k == nil {
		return Status{Error: "NOT_RUNNING"}
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	s := Status{Stream: k.stream, Why: k.why, At: k.at.UnixMilli(), Env: k.env}
	if k.err != nil {
		s.Error = reject(k.err).Error()
	}
	return s
}

var _ trading.Desk = (*Desks)(nil)
