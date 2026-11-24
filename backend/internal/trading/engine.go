package trading

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"qs/internal/book"
	"qs/internal/ledger"
)

var (
	DefaultTaker = decimal.RequireFromString("0.0005") // 币安 VIP0 吃单 0.05%
	DefaultMaker = decimal.RequireFromString("0.0002") // 挂单 0.02%
)

const maxOpenOrders = 200 // 规格里没有时的默认值（币安 MAX_NUM_ORDERS 常见值）

// maxCondOrders 条件单（止损、止盈、条件开仓）所有品种合计最多挂多少张。
// 币安 2025-12 把条件单搬到 Algo 服务，2025-12-29 去掉了按品种的 MAX_NUM_ALGO_ORDERS，改成所有品种合计 200 张。
const maxCondOrders = 200

// openLimit 这个品种同时挂着的委托（含条件单）和条件单各有上限，按币安 exchangeInfo 的 MAX_NUM_ORDERS、MAX_NUM_ALGO_ORDERS。
func openLimit(a *Account, o *Order, spec *Spec) error {
	n, algo, allCond := 0, 0, 0
	for _, x := range a.Orders {
		if x.open() && x.conditional() {
			allCond++
		}
		if x.Instrument == o.Instrument && x.open() {
			n++
			if x.conditional() {
				algo++
			}
		}
	}
	mx := spec.MaxOrders
	if mx <= 0 {
		mx = maxOpenOrders
	}
	if n >= mx {
		return rej("TOO_MANY_ORDERS", "max", fmt.Sprint(mx))
	}
	if o.conditional() && spec.MaxAlgo > 0 && algo >= spec.MaxAlgo {
		return rej("TOO_MANY_CONDITIONAL", "max", fmt.Sprint(spec.MaxAlgo))
	}
	if o.conditional() && allCond >= maxCondOrders {
		return rej("TOO_MANY_CONDITIONAL", "max", fmt.Sprint(maxCondOrders))
	}
	return nil
}

// Reject 拒单或拒绝操作。Code 由前端按语言翻译，Params 填进提示里的占位符。
type Reject struct {
	HTTP   int               `json:"-"`
	Code   string            `json:"code"`
	Params map[string]string `json:"params,omitempty"`
}

func (r *Reject) Error() string { return r.Code }

// RejectCode、RejectParams 让别的模块（现货接口）能写出同样格式的拒单。
func (r *Reject) RejectCode() string { return r.Code }

// RejectParams 见 RejectCode。
func (r *Reject) RejectParams() map[string]string { return r.Params }

func rej(code string, kv ...string) *Reject {
	r := &Reject{HTTP: 400, Code: code}
	if len(kv) > 1 {
		r.Params = map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			r.Params[kv[i]] = kv[i+1]
		}
	}
	switch code {
	case "NOT_FOUND":
		r.HTTP = 404
	case "ACCOUNT_ARCHIVED":
		r.HTTP = 409
	case "MARKET_UNAVAILABLE", "MARKET_STALE":
		r.HTTP = 503
	}
	return r
}

func fs(x decimal.Decimal) string { return x.String() }

type fundState struct {
	rate decimal.Decimal
	next int64
}

// fundGap 进程睡着时错过的结算：from 是错过的第一期，seen 是发现的时间。
type fundGap struct {
	from, seen int64
	to         int64                     // >0：只补到这个时刻（开机补结、实时结算）
	snap       map[int64]decimal.Decimal // 按这份持仓算（开机那一刻 / 结算那一刻的）
	inst       string                    // pending 里的才有
	settle     int64                     // 实时看到的那一期结算时刻：交易所的记录里要有这一期才算取到
	est        decimal.Decimal           // 推送里的预估费率、当时的标记价：15 分钟还取不到记录时用
	estMark    decimal.Decimal
	born       int64
}

// 实时结算等交易所公布的正式记录，最多等这么久，再取不到就按推送里的预估费率结（免得漏收）
const fundingLiveFallback = 15 * time.Minute

// 交易所的"下次结算时间"往后推了，而上一期结算已经过去这么久：说明结算时我们没在跑（电脑睡眠）。
const fundingLiveWindow = 5 * time.Minute

// 等真实结算记录的这段时间，这个品种先不成交、不触发、不强平：
// 资金费要按睡着时的持仓算，一旦持仓变了就算不对。取不到记录也不会一直等。
const fundingPauseMax = 30 * time.Second

type Engine struct {
	// OnManualFill 手动单有成交（持有 e.mu 时调用，不能回调引擎）。机器人要让路（D83）
	OnManualFill func(acct int64, inst string)
	// Guard 开仓和加仓前的预检（纪律锁）。在引擎的锁里调用，不能回调引擎；返回错误就拒单。
	Guard func(PreOrder) error
	// Latency 模拟下单延迟（D94）：exec = 点下单到这张单按交易所当时的盘口成交，reply = 点下单到收到回报。nil = 立即成交
	Latency  func(acct int64) (exec, reply time.Duration)
	sent     map[int64][]int64 // 每个账户最近一分钟的下单时间（交易所的下单频率限制）
	Books    *book.Store
	Venue    string
	Specs    *Specs
	Log      *slog.Logger
	Now      func() time.Time
	Persist  func([]Event) error // 为空时只在内存里
	TakerFee decimal.Decimal
	MakerFee decimal.Decimal

	mu        sync.Mutex
	accounts  map[int64]*Account
	ledger    *ledger.Ledger
	seq       int64
	lastEvent int64 // 重放时最后一个事件的时间（判断后端停了多久，补资金费用）
	funding   map[string]fundState
	undone    map[string]bool            // 已经撤回过的委托（只在内存里：窗口只有 5 秒）
	outage    map[string]int64           // 正在断流的品种：从哪一刻开始（盘口最后一次更新）
	gaps      []priceGap                 // 断流结束、等着取 K 线补算的时段
	queue     map[string]decimal.Decimal // 限价挂单前面还排着多少（委托 id → 数量）。只在内存里：重启后按当时盘口重新排到最后
	fundGaps  map[string]fundGap         // 错过的资金费结算，等 settleGaps 从交易所取真实记录补上
	fundWait  map[string]fundGap         // 等交易所正式记录的结算（开机补结、实时看到的结算），settleGaps 每半分钟取一次

	// FundingHistory 取交易所一段时间内的真实结算记录。为空时（模拟行情）错过的结算只按最后看到的费率收一次
	FundingHistory func(ctx context.Context, inst string, from, to int64) ([]FundingSettle, error)
	// Bars1m 取一段时间的 1 分钟 K 线（mark = 标记价，否则成交价）。断流补算用；没接时不补
	Bars1m func(ctx context.Context, inst string, from, to int64, mark bool) ([]Bar1m, error)

	wmu     sync.Mutex // 一次只有一个写库过程
	pmu     sync.Mutex
	pending []Event
	wake    chan struct{}
}

func New(books *book.Store, venue string, specs *Specs, log *slog.Logger) *Engine {
	e := &Engine{Books: books, Venue: venue, Specs: specs, Log: log, Now: time.Now, TakerFee: DefaultTaker, MakerFee: DefaultMaker,
		accounts: map[int64]*Account{}, ledger: ledger.New(), funding: map[string]fundState{}, wake: make(chan struct{}, 1)}
	books.OnTrade(e.onTrade)
	return e
}

func (e *Engine) now() int64 { return e.Now().UnixMilli() }

// ---------- 事件 ----------

func (e *Engine) emit(ev Event) Event {
	e.seq++
	ev.Seq = e.seq
	if ev.Time == 0 {
		ev.Time = e.now()
	}
	if err := e.apply(ev); err != nil {
		e.Log.Error("trading: 应用事件失败（程序错误）", "seq", ev.Seq, "kind", ev.Kind, "err", err)
	}
	if e.Persist != nil {
		e.pmu.Lock()
		e.pending = append(e.pending, ev)
		e.pmu.Unlock()
		select {
		case e.wake <- struct{}{}:
		default:
		}
	}
	return ev
}

// SetPersist 设置写库函数（必须在 Restore 之前、接收操作之前调用）。
func (e *Engine) SetPersist(f func([]Event) error) {
	e.mu.Lock()
	e.Persist = f
	e.mu.Unlock()
}

// Restore 启动时按顺序重放数据库里的事件。必须在接收任何操作之前调用。
func (e *Engine) Restore(evs []Event) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	sort.Slice(evs, func(i, j int) bool { return evs[i].Seq < evs[j].Seq })
	for _, ev := range evs {
		if err := e.apply(ev); err != nil {
			return fmt.Errorf("重放事件 %d：%w", ev.Seq, err)
		}
		e.seq = ev.Seq
		if ev.Time > e.lastEvent {
			e.lastEvent = ev.Time
		}
	}
	return nil
}

// RunPersist 后台把事件按顺序写进数据库；失败时 2 秒后重试，不丢顺序。
func (e *Engine) RunPersist(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			e.Flush()
			return
		case <-e.wake:
		case <-time.After(2 * time.Second):
		}
		e.Flush()
	}
}

// Drain 关机时用：把还没写库的事件写完，最多等 timeout。返回还剩多少没写进去。
func (e *Engine) Drain(timeout time.Duration) int {
	end := time.Now().Add(timeout)
	for {
		n := e.Flush()
		if n == 0 || time.Now().After(end) {
			return n
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Flush 立即尝试写库，返回还没写进去的事件数。
func (e *Engine) Flush() int {
	if e.Persist == nil {
		return 0
	}
	e.wmu.Lock()
	defer e.wmu.Unlock()
	e.pmu.Lock()
	batch := append([]Event(nil), e.pending...)
	e.pmu.Unlock()
	if len(batch) == 0 {
		return 0
	}
	if err := e.Persist(batch); err != nil {
		e.Log.Warn("trading: 写库失败，稍后重试", "pending", len(batch), "err", err)
		return len(batch)
	}
	e.pmu.Lock()
	e.pending = e.pending[len(batch):]
	n := len(e.pending)
	e.pmu.Unlock()
	return n
}

// Check 校验账本：每笔分录平衡，且每个账户账本里的现金等于钱包余额。
func (e *Engine) Check() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.ledger.Check(); err != nil {
		return err
	}
	for id, a := range e.accounts {
		if c := e.ledger.Balance(bookName(id), acCash); !c.Equal(a.Wallet) {
			return fmt.Errorf("账户 %d：账本现金 %s ≠ 钱包余额 %s", id, c, a.Wallet)
		}
	}
	return nil
}

// ---------- 行情 ----------

func (e *Engine) markOf(inst string) (decimal.Decimal, bool) {
	if t, ok := e.Books.Ticker(e.Venue, inst); ok {
		if t.Mark.Sign() > 0 {
			return t.Mark, true
		}
		if t.Last.Sign() > 0 {
			return t.Last, true
		}
	}
	if b, ok := e.Books.Book(e.Venue, inst); ok {
		return b.Mid()
	}
	return zero, false
}

func (e *Engine) marks() marks {
	cache := map[string]decimal.Decimal{}
	return func(inst string) (decimal.Decimal, bool) {
		if v, ok := cache[inst]; ok {
			return v, true
		}
		v, ok := e.markOf(inst)
		if ok {
			cache[inst] = v
		}
		return v, ok
	}
}

// StaleAfter 盘口超过这么久没更新，就不接新单、不按它撮合（避免按过期价格成交）。
const StaleAfter = 5 * time.Second

// staleFor 盘口多久没更新了（秒，向上取整）；0 = 正常或没有盘口。
func (e *Engine) staleFor(inst string) int64 {
	b, ok := e.Books.Book(e.Venue, inst)
	if !ok || b.Recv == 0 {
		return 0
	}
	if age := time.Now().UnixMilli() - b.Recv; age > StaleAfter.Milliseconds() {
		return (age + 999) / 1000
	}
	return 0
}

func (e *Engine) levels(inst string, buy bool) []book.Level {
	b, ok := e.Books.Book(e.Venue, inst)
	if !ok {
		return nil
	}
	if buy {
		return b.Asks
	}
	return b.Bids
}

// ---------- 账户 ----------

// rates 这个账户的手续费率：自己设过用自己的，没设过用引擎默认（币安普通用户）。
func (e *Engine) rates(a *Account) (taker, maker decimal.Decimal) {
	taker, maker = e.TakerFee, e.MakerFee
	if a != nil && a.Taker != nil {
		taker = *a.Taker
	}
	if a != nil && a.Maker != nil {
		maker = *a.Maker
	}
	return
}

// FeeMax 手续费率上限 1%：防输错（把 0.05% 写成 5%），也够模拟任何交易所的普通费率。
var FeeMax = decimal.RequireFromString("0.01")

// SetFees 改模拟账户的手续费率（D103），只影响以后的成交，已经成交的不重算。def = 回到默认。
func (e *Engine) SetFees(acct int64, taker, maker decimal.Decimal, def bool) error {
	if !def && (taker.IsNegative() || maker.IsNegative() || taker.GreaterThan(FeeMax) || maker.GreaterThan(FeeMax)) {
		return rej("BAD_FEE", "max", "1")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, err := e.live(acct); err != nil {
		return err
	}
	ev := &FeesEv{Taker: taker.Round(6), Maker: maker.Round(6), Default: def}
	if def {
		ev.Taker, ev.Maker = zero, zero
	}
	e.emit(Event{Kind: "fees", Account: acct, Fees: ev})
	return nil
}

// FeesOf 账户现在用的费率；custom = 自己设过。
func (e *Engine) FeesOf(acct int64) (taker, maker decimal.Decimal, custom bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	a := e.accounts[acct]
	taker, maker = e.rates(a)
	return taker, maker, a != nil && a.Taker != nil
}

// CopyFees 重置账户时把旧账户自己设的费率带到新账户（重置是"重新开始"，费率是习惯，不该丢）。
func (e *Engine) CopyFees(from, to int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, b := e.accounts[from], e.accounts[to]
	if a == nil || b == nil || a.Taker == nil || a.Maker == nil {
		return
	}
	e.emit(Event{Kind: "fees", Account: to, Fees: &FeesEv{Taker: *a.Taker, Maker: *a.Maker}})
}

// Fund 模拟账户随时加减资金（D103），不用重置。本金（Initial）跟着一起变，所以加进来的钱不算赚的；
// 也记进 Transfers，纪律的"今天亏了多少"不会把它当盈亏。减的时候和转去现货一样：占着的保证金和浮盈不能拿走，
// 本金也不能减到 0（收益率要拿它当分母）。
func (e *Engine) Fund(acct int64, amount decimal.Decimal) error {
	amount = amount.Round(8)
	if amount.IsZero() {
		return rej("BAD_AMOUNT")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	a, err := e.live(acct)
	if err != nil {
		return err
	}
	if amount.IsNegative() {
		mx := decimal.Max(zero, decimal.Min(transferable(e.compute(a, e.marks())), a.Initial.Sub(decimal.New(1, -2))))
		if mx.LessThan(amount.Neg()) {
			return rej("FUND_LIMIT", "max", fs(mx.RoundFloor(2)))
		}
	}
	e.emit(Event{Kind: "fund", Account: acct, Initial: &amount})
	return nil
}

// Open 开一个模拟账户（已存在就什么也不做）。
func (e *Engine) Open(id int64, initial decimal.Decimal) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.accounts[id]; ok {
		return
	}
	e.emit(Event{Kind: "open", Account: id, Initial: &initial})
}

// Archive 账户被重置时归档：撤掉所有委托，之后不再撮合、不再收付资金费、不再强平。
func (e *Engine) Archive(id int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, ok := e.accounts[id]
	if !ok || a.Archived {
		return
	}
	for _, o := range sortedOrders(a) {
		e.closeOrder(a, o, StCanceled, "ARCHIVED")
	}
	e.emit(Event{Kind: "archive", Account: id})
}

func (e *Engine) Has(id int64) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.accounts[id]
	return ok
}

func (e *Engine) live(id int64) (*Account, error) {
	a, ok := e.accounts[id]
	if !ok {
		return nil, rej("NOT_FOUND")
	}
	if a.Archived {
		return nil, rej("ACCOUNT_ARCHIVED")
	}
	return a, nil
}

func sortedOrders(a *Account) []*Order {
	out := make([]*Order, 0, len(a.Orders))
	for _, o := range a.Orders {
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Created < out[j].Created || (out[i].Created == out[j].Created && out[i].ID < out[j].ID)
	})
	return out
}

// ---------- 下单 ----------

type OrderReq struct {
	Instrument string          `json:"instrument"`
	Side       string          `json:"side"`
	Type       string          `json:"type"`
	TIF        string          `json:"tif"`
	Price      decimal.Decimal `json:"price"`
	Qty        decimal.Decimal `json:"qty"`        // 按币数
	Notional   decimal.Decimal `json:"notional"`   // 或按 USDT 名义金额
	MarginPct  decimal.Decimal `json:"margin_pct"` // 或按可用保证金的百分比（0–100）
	ReduceOnly bool            `json:"reduce_only"`
	TP         decimal.Decimal `json:"tp"`
	SL         decimal.Decimal `json:"sl"`
	TriggerPx  decimal.Decimal `json:"trigger_px"` // 条件单的触发价
	TriggerBy  string          `json:"trigger_by"` // mark（默认）| last：条件单和随单止盈止损的触发价类型
	// 币安的另外几条规则（D103）
	GoodTill     int64  `json:"good_till"`     // TIF = GTD 时：有效到这个时刻（毫秒），至少比现在晚 10 分钟
	PriceMatch   string `json:"price_match"`   // 限价单不填价格，按盘口定：OPPONENT / OPPONENT_5 / _10 / _20、QUEUE / QUEUE_5 / _10 / _20
	PriceProtect bool   `json:"price_protect"` // 条件单（和随单止盈止损）开价格保护
	source       string
	parent       string // 条件单触发后生成的委托：来自哪张条件单
	tag          string // 系统下的单：来自哪条信号（或哪种风控）
}

// prepare 校验并生成委托（还没提交）。refPx 是估算用的参考价（限价单为限价，市价单为对手价）。
func (e *Engine) prepare(a *Account, r OrderReq) (o *Order, spec *Spec, refPx decimal.Decimal, err error) {
	spec, ok := e.Specs.Get(r.Instrument)
	if !ok {
		return nil, nil, zero, rej("BAD_INSTRUMENT")
	}
	mark, ok := e.markOf(r.Instrument)
	if !ok {
		return nil, nil, zero, rej("MARKET_UNAVAILABLE")
	}
	if r.Side != "buy" && r.Side != "sell" {
		return nil, nil, zero, rej("BAD_SIDE")
	}
	if s := e.staleFor(r.Instrument); s > 0 {
		return nil, nil, zero, rej("MARKET_STALE", "s", fmt.Sprint(s))
	}
	buy := r.Side == "buy"
	if r.TriggerBy == "" {
		r.TriggerBy = ByMark
	}
	if r.TriggerBy != ByMark && r.TriggerBy != ByLast && r.TriggerBy != ByIndex {
		return nil, nil, zero, rej("BAD_TRIGGER_BY")
	}
	var trigDir string
	if r.PriceMatch != "" && r.Type != Limit { // 币安：priceMatch 这里只给限价单
		return nil, nil, zero, rej("BAD_PRICE_MATCH")
	}
	switch r.Type {
	case CondMarket, CondLimit:
		if r.TriggerPx.Sign() <= 0 || !spec.OnTick(r.TriggerPx) {
			return nil, nil, zero, rej("TRIGGER_TICK", "tick", fs(spec.Tick))
		}
		ref, ok := e.triggerRef(r.Instrument, r.TriggerBy, mark)
		if !ok {
			return nil, nil, zero, rej("MARKET_UNAVAILABLE")
		}
		if r.TriggerPx.Equal(ref) {
			return nil, nil, zero, rej("TRIGGER_AT_PRICE", "px", fs(ref))
		}
		trigDir = "down"
		if r.TriggerPx.GreaterThan(ref) {
			trigDir = "up"
		}
		// 价格带按触发价算（触发那一刻标记价约等于触发价）
		lo, hi := r.TriggerPx.Mul(spec.PriceDown), r.TriggerPx.Mul(spec.PriceUp)
		if r.Type == CondMarket {
			r.TIF, r.Price, refPx = "", zero, r.TriggerPx
		} else {
			if r.TIF == "" {
				r.TIF = GTC
			}
			if r.TIF != GTC && r.TIF != IOC && r.TIF != FOK && r.TIF != GTX {
				return nil, nil, zero, rej("BAD_TIF")
			}
			if r.Price.Sign() <= 0 || !spec.OnTick(r.Price) {
				return nil, nil, zero, rej("PRICE_TICK", "tick", fs(spec.Tick))
			}
			if r.Price.LessThan(lo) || r.Price.GreaterThan(hi) {
				return nil, nil, zero, rej("PRICE_OUT_OF_RANGE", "min", fs(lo.RoundCeil(int32(-spec.Tick.Exponent()))), "max", fs(hi.RoundFloor(int32(-spec.Tick.Exponent()))))
			}
			refPx = r.Price
		}
	case Market:
		r.TIF = ""
		r.Price = zero
		lv := e.levels(r.Instrument, buy)
		if len(lv) == 0 {
			return nil, nil, zero, rej("NO_LIQUIDITY")
		}
		refPx = lv[0].Px
	case Limit:
		if r.PriceMatch != "" { // 按盘口定价：不能同时填价格（币安 -4xxx 一样拒）
			if r.Price.Sign() > 0 {
				return nil, nil, zero, rej("PRICE_MATCH_WITH_PRICE")
			}
			px, err := e.matchPrice(r.Instrument, buy, r.PriceMatch)
			if err != nil {
				return nil, nil, zero, err
			}
			r.Price = px
		}
		if r.TIF == "" {
			r.TIF = GTC
		}
		if r.TIF != GTC && r.TIF != IOC && r.TIF != FOK && r.TIF != GTX && r.TIF != GTD {
			return nil, nil, zero, rej("BAD_TIF")
		}
		if r.TIF == GTD {
			gt, err := checkGTD(r.GoodTill, e.now())
			if err != nil {
				return nil, nil, zero, err
			}
			r.GoodTill = gt
		}
		if r.Price.Sign() <= 0 || !spec.OnTick(r.Price) {
			return nil, nil, zero, rej("PRICE_TICK", "tick", fs(spec.Tick))
		}
		lo, hi := mark.Mul(spec.PriceDown), mark.Mul(spec.PriceUp)
		if r.Price.LessThan(lo) || r.Price.GreaterThan(hi) {
			return nil, nil, zero, rej("PRICE_OUT_OF_RANGE", "min", fs(lo.RoundCeil(int32(-spec.Tick.Exponent()))), "max", fs(hi.RoundFloor(int32(-spec.Tick.Exponent()))))
		}
		refPx = r.Price
	default:
		return nil, nil, zero, rej("BAD_TYPE")
	}
	for _, px := range []decimal.Decimal{r.Price, r.TriggerPx} {
		if px.Sign() > 0 && ((spec.MinPrice.Sign() > 0 && px.LessThan(spec.MinPrice)) || (spec.MaxPrice.Sign() > 0 && px.GreaterThan(spec.MaxPrice))) {
			return nil, nil, zero, rej("PRICE_OUT_OF_RANGE", "min", fs(spec.MinPrice), "max", fs(spec.MaxPrice))
		}
	}
	set := a.setting(r.Instrument)
	qty := r.Qty
	switch {
	case qty.Sign() > 0:
	case r.Notional.Sign() > 0:
		qty = r.Notional.Div(refPx)
	case r.MarginPct.Sign() > 0:
		if r.MarginPct.GreaterThan(hundred) {
			return nil, nil, zero, rej("BAD_QTY")
		}
		m := e.compute(a, e.marks())
		avail := decimal.Max(zero, m.available)
		l := lev(set.Leverage)
		taker, _ := e.rates(a)
		qty = avail.Mul(r.MarginPct).Div(hundred).Mul(l).Div(refPx.Mul(one.Add(l.Mul(taker))))
	default:
		return nil, nil, zero, rej("QTY_REQUIRED")
	}
	qty = spec.RoundQty(qty)
	pos := zero
	if p, ok := a.Pos[r.Instrument]; ok {
		pos = p.Qty
	}
	if r.ReduceOnly {
		if pos.IsZero() {
			return nil, nil, zero, rej("REDUCE_ONLY_NO_POSITION")
		}
		if (pos.Sign() > 0) == buy {
			return nil, nil, zero, rej("REDUCE_ONLY_SAME_SIDE")
		}
		qty = decimal.Min(qty, pos.Abs())
	}
	if qty.LessThan(spec.MinQty) || qty.IsZero() {
		return nil, nil, zero, rej("QTY_TOO_SMALL", "min", fs(spec.MinQty))
	}
	// 一张单的数量上限：限价单看 LOT_SIZE，市价单（含触发后按市价成交的）看 MARKET_LOT_SIZE（币安 -4005）
	mxq := spec.MaxQty
	if r.Type == Market || r.Type == CondMarket {
		mxq = spec.MarketMax
	}
	if mxq.Sign() > 0 && qty.GreaterThan(mxq) {
		return nil, nil, zero, rej("QTY_TOO_LARGE", "max", fs(mxq))
	}
	if !r.ReduceOnly && qty.Mul(refPx).LessThan(spec.MinNotional) {
		return nil, nil, zero, rej("MIN_NOTIONAL", "min", fs(spec.MinNotional), "notional", fs(qty.Mul(refPx).Round(2)))
	}
	if (r.TP.Sign() > 0 || r.SL.Sign() > 0) && r.parent == "" { // 条件单触发后生成的委托不再检查（下条件单时查过了，触发时价格可能已经跳过去）
		if r.ReduceOnly {
			return nil, nil, zero, rej("BAD_TPSL")
		}
		if (r.TP.Sign() > 0 && !spec.OnTick(r.TP)) || (r.SL.Sign() > 0 && !spec.OnTick(r.SL)) {
			return nil, nil, zero, rej("PRICE_TICK", "tick", fs(spec.Tick))
		}
		if r.TP.Sign() < 0 || r.SL.Sign() < 0 ||
			(buy && ((r.TP.Sign() > 0 && r.TP.LessThanOrEqual(refPx)) || (r.SL.Sign() > 0 && r.SL.GreaterThanOrEqual(refPx)))) ||
			(!buy && ((r.TP.Sign() > 0 && r.TP.GreaterThanOrEqual(refPx)) || (r.SL.Sign() > 0 && r.SL.LessThanOrEqual(refPx)))) {
			return nil, nil, zero, rej("BAD_TPSL")
		}
	}
	src := r.source
	if src == "" {
		src = "manual"
	}
	now := e.now()
	o = &Order{Account: a.ID, Instrument: r.Instrument, Side: r.Side, Type: r.Type, TIF: r.TIF, Price: r.Price, Qty: qty,
		ReduceOnly: r.ReduceOnly, TP: r.TP, SL: r.SL, Status: StNew, Source: src, Parent: r.parent, Tag: r.tag, Created: now, Updated: now,
		TriggerPx: r.TriggerPx, TriggerBy: r.TriggerBy, TriggerDir: trigDir, PriceMatch: r.PriceMatch, PriceProtect: r.PriceProtect}
	if r.TIF == GTD {
		o.GoodTill = r.GoodTill
	}
	if !o.entryCond() {
		o.TriggerPx = zero
	}
	o.RefPx = refPx
	return o, spec, refPx, nil
}

// estimate 按当前盘口逐档估算成交均价（限价单最多吃到限价；不能立即成交的部分按限价算）。
func (e *Engine) estimate(o *Order, spec *Spec) (avg decimal.Decimal, takerQty decimal.Decimal) {
	if o.entryCond() { // 条件单：按触发价（市价）或限价估算；触发前不知道盘口
		px := o.TriggerPx
		if o.Type == CondLimit {
			px = o.Price
		}
		return px, zero
	}
	limit := o.Price
	if o.Type == Market {
		limit = e.band(o, spec)
	}
	rem := o.Qty
	cost := zero
	for _, l := range e.levels(o.Instrument, o.buy()) {
		if rem.Sign() <= 0 || !within(o.buy(), l.Px, limit) {
			break
		}
		q := decimal.Min(rem, l.Qty)
		cost = cost.Add(q.Mul(l.Px))
		rem = rem.Sub(q)
		takerQty = takerQty.Add(q)
	}
	if rem.Sign() > 0 {
		if o.Type == Market { // 盘口不够深：剩余按价格带边缘估（实际会过期）
			cost = cost.Add(rem.Mul(limit))
		} else {
			cost = cost.Add(rem.Mul(o.Price))
		}
	}
	return cost.Div(o.Qty), takerQty
}

func within(buy bool, px, limit decimal.Decimal) bool {
	if limit.IsZero() {
		return true
	}
	if buy {
		return px.LessThanOrEqual(limit)
	}
	return px.GreaterThanOrEqual(limit)
}

// band 市价单的价格保护：最多吃到标记价的价格带边缘（币安 PERCENT_PRICE）。
func (e *Engine) band(o *Order, spec *Spec) decimal.Decimal {
	mark, ok := e.markOf(o.Instrument)
	if !ok {
		return zero
	}
	// 市价单最多吃到标记价上下 marketTakeBound（币安"市价单价格上下限"，BTC 3%），没有就用限价单的价格带
	up, down := spec.PriceUp, spec.PriceDown
	if spec.MarketBound.Sign() > 0 {
		up, down = one.Add(spec.MarketBound), one.Sub(spec.MarketBound)
	}
	if o.buy() {
		return mark.Mul(up)
	}
	return mark.Mul(down)
}

func maxNotionalFor(spec *Spec, leverage int) (decimal.Decimal, bool) {
	for _, b := range spec.Brackets {
		if b.MaxLev < leverage {
			return b.Floor, true
		}
	}
	return zero, false
}

// need 下这张单需要的保证金（挂单占用的增量 + 预估手续费），以及会不会超过当前杠杆允许的最大仓位。
func (e *Engine) need(a *Account, o *Order, spec *Spec, estPx decimal.Decimal) (decimal.Decimal, error) {
	mk := e.marks()
	tmp := *o
	if tmp.Price.IsZero() {
		tmp.Price = estPx
	}
	if tmp.entryCond() { // 条件单触发前不占保证金；这里按"现在触发"算够不够，提前告诉用户
		tmp.Type = Limit
		// 触发那一刻标记价约等于触发价：按触发价当标记价算，不算开单损失（币安：没触发的条件单不占保证金）
		at := estPx
		base := mk
		mk = func(i string) (decimal.Decimal, bool) {
			if i == o.Instrument {
				return at, true
			}
			return base(i)
		}
	}
	before := e.orderIM(a, o.Instrument, nil, mk)
	after := e.orderIM(a, o.Instrument, &tmp, mk)
	taker, _ := e.rates(a)
	n := after.Sub(before).Add(o.Qty.Mul(estPx).Mul(taker))
	return n, e.posLimit(a, o, o.Qty, spec, estPx)
}

// posLimit 最大仓位：现有仓位 + 同方向已经挂着的开仓单 + 这张单，全部成交后不能超过当前杠杆的上限。
// 只看这一张单的话，挂几张一起成交就超了（审查第 6 条）。条件单触发前不算，触发下单时会再查一次。
func (e *Engine) posLimit(a *Account, o *Order, qty decimal.Decimal, spec *Spec, estPx decimal.Decimal) error {
	pos := zero
	if p, ok := a.Pos[o.Instrument]; ok {
		pos = p.Qty
	}
	pend := zero
	for _, x := range a.Orders {
		if x.ID != o.ID && x.Instrument == o.Instrument && x.open() && !x.conditional() && !x.ReduceOnly && !x.ClosePos && x.buy() == o.buy() {
			pend = pend.Add(x.remaining())
		}
	}
	newPos := pos.Add(pend.Add(qty).Mul(sideSign(o.buy())))
	if newPos.Abs().GreaterThan(pos.Abs()) {
		set := a.setting(o.Instrument)
		if mx, ok := maxNotionalFor(spec, set.Leverage); ok && newPos.Abs().Mul(estPx).GreaterThan(mx) {
			return rej("POSITION_LIMIT", "leverage", fmt.Sprint(set.Leverage), "max", fs(mx))
		}
	}
	return nil
}

// Place 下单。返回委托的最终状态（可能已经成交、部分成交挂着、或过期）。
func (e *Engine) Place(acct int64, r OrderReq) (*Order, error) {
	defer e.travel(acct)() // 模拟下单延迟：请求晚到交易所，回报再晚回来（D94）
	e.mu.Lock()
	defer e.mu.Unlock()
	a, err := e.live(acct)
	if err == nil {
		err = e.rateCheck(acct)
	}
	if err != nil {
		return nil, err
	}
	return e.place(a, r)
}

func (e *Engine) place(a *Account, r OrderReq) (*Order, error) {
	o, spec, _, err := e.prepare(a, r)
	if err != nil {
		return nil, err
	}
	if err := openLimit(a, o, spec); err != nil {
		return nil, err
	}
	est, _ := e.estimate(o, spec)
	if err := e.guard(a, o, est); err != nil {
		return nil, err
	}
	// 条件单下单时不查保证金和最大仓位：币安把条件单搬到 Algo 服务以后，触发之前不做保证金检查（2025-12 起）。
	// 触发时不够就过期，并通知用户（cond_failed）。预览里照样提前写出来"按现在触发够不够"
	if !o.ReduceOnly && !o.entryCond() {
		n, err := e.need(a, o, spec, est)
		if err != nil {
			return nil, err
		}
		if avail := e.compute(a, e.marks()).available; n.GreaterThan(avail) {
			return nil, rej("INSUFFICIENT_MARGIN", "need", fs(n.Round(2)), "available", fs(decimal.Max(zero, avail).Round(2)))
		}
	}
	// FOK 先看盘口够不够
	fokOK := true
	if o.TIF == FOK {
		_, tq := e.estimate(o, spec)
		fokOK = tq.GreaterThanOrEqual(o.Qty)
	}
	o.ID = orderID(e.seq + 1)
	e.emit(Event{Kind: "order", Account: a.ID, Order: o})
	ord := a.Orders[o.ID]
	if ord.entryCond() { // 条件单：等价格到了再下（triggers）
		return e.findOrder(a, o.ID), nil
	}
	switch {
	case o.Type == Market:
		e.take(a, ord, spec, e.band(ord, spec))
		e.expireRest(a, ord, "NO_LIQUIDITY")
	case o.TIF == GTX:
		if e.crosses(ord) {
			e.closeOrder(a, ord, StExpired, "POST_ONLY_WOULD_TAKE")
		}
	case o.TIF == FOK:
		if !fokOK {
			e.closeOrder(a, ord, StExpired, "FOK_NOT_FILLED")
		} else {
			e.take(a, ord, spec, ord.Price)
		}
	case o.TIF == IOC:
		e.take(a, ord, spec, ord.Price)
		e.expireRest(a, ord, "IOC_REMAINDER")
	default: // GTC：能立即成交的部分先吃，剩余挂着
		if e.crosses(ord) {
			e.take(a, ord, spec, ord.Price)
		}
	}
	if cur, ok := a.Orders[o.ID]; ok && cur.Type == Limit && cur.open() {
		e.queueAhead(cur) // 挂上了：排到这一档的最后
	}
	return e.findOrder(a, o.ID), nil
}

func (e *Engine) expireRest(a *Account, o *Order, reason string) {
	if cur, ok := a.Orders[o.ID]; ok && cur.open() || ok && cur.Status == StTriggered {
		e.closeOrder(a, cur, StExpired, reason)
	}
}

func (e *Engine) crosses(o *Order) bool {
	lv := e.levels(o.Instrument, o.buy())
	if len(lv) == 0 {
		return false
	}
	if o.buy() {
		return o.Price.GreaterThanOrEqual(lv[0].Px)
	}
	return o.Price.LessThanOrEqual(lv[0].Px)
}

// take 按盘口逐档吃单（吃单手续费），最多吃到 limit。只减仓的单每次成交前都按当前仓位封顶。
func (e *Engine) take(a *Account, o *Order, spec *Spec, limit decimal.Decimal) {
	lv := append([]book.Level(nil), e.levels(o.Instrument, o.buy())...)
	for _, l := range lv {
		cur, ok := a.Orders[o.ID]
		if !ok || !(cur.open() || cur.Status == StTriggered) {
			return
		}
		if !within(o.buy(), l.Px, limit) {
			return
		}
		q := spec.RoundQty(decimal.Min(cur.remaining(), l.Qty))
		if cur.ReduceOnly {
			q = decimal.Min(q, e.reducible(a, cur))
		}
		if q.Sign() <= 0 {
			if cur.ReduceOnly {
				return
			}
			continue
		}
		e.fill(a, cur, q, l.Px, false)
	}
}

// reducible 只减仓的单还能成交多少（当前反方向持仓）。
func (e *Engine) reducible(a *Account, o *Order) decimal.Decimal {
	p, ok := a.Pos[o.Instrument]
	if !ok || p.Qty.IsZero() || (p.Qty.Sign() > 0) == o.buy() {
		return zero
	}
	return p.Qty.Abs()
}

func (e *Engine) fill(a *Account, o *Order, q, px decimal.Decimal, maker bool) {
	takerFee, makerFee := e.rates(a)
	rate := takerFee
	if maker {
		rate = makerFee
	}
	fee := q.Mul(px).Mul(rate).Round(8)
	e.emit(Event{Kind: "fill", Account: a.ID, Fill: &FillEv{OrderID: o.ID, Instrument: o.Instrument, Buy: o.buy(), Qty: q, Px: px, Fee: fee, Maker: maker}})
	if cur, ok := a.Orders[o.ID]; !ok || !cur.open() {
		delete(e.queue, o.ID)
	}
	if e.OnManualFill != nil && (o.Source == "manual" || o.Source == "close" || o.Source == "cond") {
		e.OnManualFill(a.ID, o.Instrument)
	}
	e.afterFill(a, o)
}

// afterFill 成交后的连带动作：挂上随单止盈止损；仓位归零时撤掉止盈止损和只减仓的单。
func (e *Engine) afterFill(a *Account, o *Order) {
	inst := o.Instrument
	p := a.Pos[inst]
	if (o.TP.Sign() > 0 || o.SL.Sign() > 0) && p != nil && !p.Qty.IsZero() && (p.Qty.Sign() > 0) == o.buy() {
		// 只带止损的单不动原来的止盈，反过来也一样：没写的那边传空，不是传 0（0 表示撤掉）
		var tp, sl *decimal.Decimal
		if o.TP.Sign() > 0 && !legDone(a, o.ID, TakeProfit) {
			v := o.TP
			tp = &v
		}
		if o.SL.Sign() > 0 && !legDone(a, o.ID, StopMarket) {
			v := o.SL
			sl = &v
		}
		if tp != nil || sl != nil {
			_ = e.setTPSL(a, inst, tp, sl, o.ID, true, o.TriggerBy)
		}
	}
	// 仓位归零，或者一笔成交直接反手（多变空、空变多）：原来那个方向的止盈止损和只减仓单都作废
	flat := p == nil || p.Qty.IsZero()
	for _, x := range sortedOrders(a) {
		if x.Instrument != inst || x.Status == StTriggered || !(x.ClosePos || x.ReduceOnly) {
			continue
		}
		if !flat && (p.Qty.Sign() > 0) != x.buy() {
			continue // 还能减这个仓位，保留
		}
		if x.ClosePos {
			e.closeOrder(a, x, StCanceled, "POSITION_CLOSED")
		} else {
			e.closeOrder(a, x, StCanceled, "REDUCE_ONLY_NO_POSITION")
		}
	}
}

// legDone 这张委托带的止盈（或止损）还要不要再挂。一张委托可能分好几笔成交：
// 已经挂着，或者用户撤过、改过，都不再挂，不能把用户的操作盖掉。
// 触发过、因为仓位清零被撤的不算：剩下的部分成交后是新仓位，还要保护。
func legDone(a *Account, parent, typ string) bool {
	for _, x := range a.Orders {
		if x.Parent == parent && x.Type == typ {
			return true
		}
	}
	for i := len(a.History) - 1; i >= 0; i-- {
		x := a.History[i]
		if x.Parent == parent && x.Type == typ && (x.Reason == "USER" || x.Reason == "REPLACED") {
			return true
		}
	}
	return false
}

func (e *Engine) closeOrder(a *Account, o *Order, status, reason string) {
	delete(e.queue, o.ID)
	e.emit(Event{Kind: "close", Account: a.ID, Close: &CloseEv{ID: o.ID, Status: status, Reason: reason}})
}

func (e *Engine) findOrder(a *Account, id string) *Order {
	if o, ok := a.Orders[id]; ok {
		c := *o
		return &c
	}
	for i := len(a.History) - 1; i >= 0; i-- {
		if a.History[i].ID == id {
			c := *a.History[i]
			return &c
		}
	}
	return nil
}

// ---------- 预览 ----------

type Preview struct {
	Qty       decimal.Decimal `json:"qty"`
	EstPx     decimal.Decimal `json:"est_px"`
	Notional  decimal.Decimal `json:"notional"`
	Fee       decimal.Decimal `json:"fee"`
	Maker     bool            `json:"maker"` // 会挂单（按挂单手续费）
	Margin    decimal.Decimal `json:"margin"`
	Available decimal.Decimal `json:"available"`
	LiqPx     decimal.Decimal `json:"liq_px"` // 全部成交后的预估强平价；0 = 没有
	MaxQty    decimal.Decimal `json:"max_qty"`
	Problem   *Reject         `json:"problem,omitempty"` // 现在下单会被拒的原因
}

func (e *Engine) Preview(acct int64, r OrderReq) (*Preview, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, err := e.live(acct)
	if err != nil {
		return nil, err
	}
	pv := &Preview{}
	taker, maker := e.rates(a)
	o, spec, refPx, perr := e.prepare(a, r)
	if spec == nil {
		spec, _ = e.Specs.Get(r.Instrument)
	}
	m := e.compute(a, e.marks())
	pv.Available = decimal.Max(zero, m.available)
	if spec != nil && refPx.Sign() == 0 {
		refPx, _ = e.markOf(r.Instrument)
	}
	if spec != nil && refPx.Sign() > 0 {
		set := a.setting(r.Instrument)
		l := lev(set.Leverage)
		mq := pv.Available.Mul(l).Div(refPx.Mul(one.Add(l.Mul(taker))))
		if p, ok := a.Pos[r.Instrument]; ok && !p.Qty.IsZero() && (p.Qty.Sign() > 0) != (r.Side == "buy") {
			mq = mq.Add(p.Qty.Abs()) // 反方向先平仓的部分不占保证金
		}
		pv.MaxQty = spec.RoundQty(mq)
	}
	if perr != nil {
		pv.Problem = perr.(*Reject)
		return pv, nil
	}
	est, tq := e.estimate(o, spec)
	pv.Qty, pv.EstPx = o.Qty, est.Round(8)
	pv.Notional = o.Qty.Mul(est).Round(2)
	pv.Maker = o.Type == Limit && tq.IsZero() && o.TIF != IOC && o.TIF != FOK
	rate := taker
	if pv.Maker {
		rate = maker
	}
	pv.Fee = o.Qty.Mul(est).Mul(rate).Round(4)
	if !o.ReduceOnly {
		n, err := e.need(a, o, spec, est)
		pv.Margin = decimal.Max(zero, n.Sub(o.Qty.Mul(est).Mul(taker))).Round(2)
		if err != nil {
			pv.Problem = err.(*Reject)
		} else if n.GreaterThan(m.available) {
			pv.Problem = rej("INSUFFICIENT_MARGIN", "need", fs(n.Round(2)), "available", fs(pv.Available.Round(2)))
		}
	}
	// 成交后的强平价：在账户副本上模拟这笔成交
	c := cloneAccount(a)
	p := c.pos(o.Instrument)
	realized, _ := fillPos(c.setting(o.Instrument), p, o.buy(), o.Qty, est)
	c.Wallet = c.Wallet.Add(realized).Sub(pv.Fee)
	pv.LiqPx = e.positionLiq(c, o.Instrument, e.marks())
	if spec != nil && pv.LiqPx.Sign() > 0 {
		pv.LiqPx = pv.LiqPx.Round(int32(-spec.Tick.Exponent()))
	}
	return pv, nil
}

func cloneAccount(a *Account) *Account {
	c := *a
	c.Pos = map[string]*Position{}
	for k, v := range a.Pos {
		p := *v
		c.Pos[k] = &p
	}
	c.Settings = map[string]*Setting{}
	for k, v := range a.Settings {
		s := *v
		c.Settings[k] = &s
	}
	c.Orders = a.Orders
	return &c
}

// ---------- 改单、撤单 ----------

func (e *Engine) Amend(acct int64, id string, price, qty, trigger decimal.Decimal) (*Order, error) {
	defer e.travel(acct)() // 模拟下单延迟：请求晚到交易所，回报再晚回来（D94）
	e.mu.Lock()
	defer e.mu.Unlock()
	a, err := e.live(acct)
	if err == nil {
		err = e.rateCheck(acct)
	}
	if err != nil {
		return nil, err
	}
	o, ok := a.Orders[id]
	if !ok {
		return nil, rej("NOT_FOUND")
	}
	if o.entryCond() && o.Status == StNew {
		return e.amendCond(a, o, price, qty, trigger)
	}
	if o.Type != Limit || !o.open() {
		return nil, rej("NOT_MODIFIABLE")
	}
	spec, _ := e.Specs.Get(o.Instrument)
	mark, ok := e.markOf(o.Instrument)
	if !ok {
		return nil, rej("MARKET_UNAVAILABLE")
	}
	// 盘口过期时不接改单（D65）：改了价可能马上按过期盘口成交
	if s := e.staleFor(o.Instrument); s > 0 {
		return nil, rej("MARKET_STALE", "s", fmt.Sprint(s))
	}
	if price.IsZero() {
		price = o.Price
	}
	if qty.IsZero() {
		qty = o.Qty
	}
	if price.Sign() < 0 || !spec.OnTick(price) {
		return nil, rej("PRICE_TICK", "tick", fs(spec.Tick))
	}
	if lo, hi := mark.Mul(spec.PriceDown), mark.Mul(spec.PriceUp); price.LessThan(lo) || price.GreaterThan(hi) {
		return nil, rej("PRICE_OUT_OF_RANGE", "min", fs(lo.RoundCeil(int32(-spec.Tick.Exponent()))), "max", fs(hi.RoundFloor(int32(-spec.Tick.Exponent()))))
	}
	qty = spec.RoundQty(qty)
	if qty.LessThanOrEqual(o.Filled) || qty.LessThan(spec.MinQty) {
		return nil, rej("AMEND_QTY", "filled", fs(o.Filled))
	}
	if !o.ReduceOnly && qty.Sub(o.Filled).Mul(price).LessThan(spec.MinNotional) {
		return nil, rej("MIN_NOTIONAL", "min", fs(spec.MinNotional), "notional", fs(qty.Sub(o.Filled).Mul(price).Round(2)))
	}
	if !o.ReduceOnly && (qty.GreaterThan(o.Qty) || !price.Equal(o.Price)) {
		// 改大、改价都按新单重新过一遍纪律预检和最大仓位（审查第 5 条：先下小单过检查再改大）
		tmp := *o
		tmp.Price, tmp.Qty = price, qty
		if err := e.guard(a, &tmp, price); err != nil {
			return nil, err
		}
		if err := e.posLimit(a, o, qty.Sub(o.Filled), spec, price); err != nil {
			return nil, err
		}
	}
	if !o.ReduceOnly {
		mk := e.marks()
		before := e.orderIM(a, o.Instrument, nil, mk)
		saved := *o
		o.Price, o.Qty = price, qty
		after := e.orderIM(a, o.Instrument, nil, mk)
		*o = saved
		if n := after.Sub(before); n.Sign() > 0 && n.GreaterThan(e.compute(a, mk).available) {
			return nil, rej("INSUFFICIENT_MARGIN", "need", fs(n.Round(2)), "available", fs(decimal.Max(zero, e.compute(a, mk).available).Round(2)))
		}
	}
	requeue := !price.Equal(o.Price) || qty.GreaterThan(o.Qty) // 改价、加量要重新排队；只减量保留位置（和币安一样）
	e.emit(Event{Kind: "amend", Account: a.ID, Amend: &AmendEv{ID: id, Price: price, Qty: qty}})
	if requeue {
		delete(e.queue, id)
	}
	if e.crosses(o) {
		if o.TIF == GTX {
			e.closeOrder(a, o, StExpired, "POST_ONLY_WOULD_TAKE")
		} else {
			e.take(a, o, spec, o.Price)
		}
	}
	if cur, ok := a.Orders[id]; ok && cur.open() {
		e.queueAhead(cur)
	}
	return e.findOrder(a, id), nil
}

func (e *Engine) Cancel(acct int64, id string) (*Order, error) {
	defer e.travel(acct)() // 模拟下单延迟：请求晚到交易所，回报再晚回来（D94）
	e.mu.Lock()
	defer e.mu.Unlock()
	a, err := e.live(acct)
	if err != nil {
		return nil, err
	}
	o, ok := a.Orders[id]
	if !ok || o.Status == StTriggered {
		return nil, rej("NOT_FOUND")
	}
	e.closeOrder(a, o, StCanceled, "USER")
	return e.findOrder(a, id), nil
}

// CancelAll 撤掉全部委托（inst 为空时所有品种），含止盈止损。返回撤掉的数量。
func (e *Engine) CancelAll(acct int64, inst string) (int, error) {
	defer e.travel(acct)() // 模拟下单延迟：请求晚到交易所，回报再晚回来（D94）
	e.mu.Lock()
	defer e.mu.Unlock()
	a, err := e.live(acct)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, o := range sortedOrders(a) {
		if (inst == "" || o.Instrument == inst) && o.Status != StTriggered {
			e.closeOrder(a, o, StCanceled, "USER")
			n++
		}
	}
	return n, nil
}

// ---------- 平仓 ----------

// ClosePosition 平仓：fraction 是比例（0–1]，typ 为 market 或 limit（限价平要给价格）。
func (e *Engine) ClosePosition(acct int64, inst string, fraction decimal.Decimal, typ string, price decimal.Decimal) (*Order, error) {
	defer e.travel(acct)() // 模拟下单延迟：请求晚到交易所，回报再晚回来（D94）
	e.mu.Lock()
	defer e.mu.Unlock()
	a, err := e.live(acct)
	if err == nil {
		err = e.rateCheck(acct)
	}
	if err != nil {
		return nil, err
	}
	return e.closePos(a, inst, fraction, typ, price)
}

func (e *Engine) closePos(a *Account, inst string, fraction decimal.Decimal, typ string, price decimal.Decimal) (*Order, error) {
	p, ok := a.Pos[inst]
	if !ok || p.Qty.IsZero() {
		return nil, rej("NO_POSITION")
	}
	if fraction.Sign() <= 0 || fraction.GreaterThan(one) {
		return nil, rej("BAD_QTY")
	}
	qty := p.Qty.Abs()
	if !fraction.Equal(one) {
		spec, _ := e.Specs.Get(inst)
		qty = spec.RoundQty(qty.Mul(fraction))
	}
	side := "sell"
	if p.Qty.Sign() < 0 {
		side = "buy"
	}
	return e.place(a, OrderReq{Instrument: inst, Side: side, Type: typ, Price: price, Qty: qty, ReduceOnly: true, source: "close"})
}

// CloseAll 一键全平：所有仓位市价全平。
func (e *Engine) CloseAll(acct int64) ([]*Order, error) {
	defer e.travel(acct)() // 模拟下单延迟：请求晚到交易所，回报再晚回来（D94）
	e.mu.Lock()
	defer e.mu.Unlock()
	a, err := e.live(acct)
	if err == nil {
		err = e.rateCheck(acct)
	}
	if err != nil {
		return nil, err
	}
	var out []*Order
	insts := make([]string, 0, len(a.Pos))
	for inst, p := range a.Pos {
		if !p.Qty.IsZero() {
			insts = append(insts, inst)
		}
	}
	sort.Strings(insts)
	var firstErr error
	for _, inst := range insts {
		o, err := e.closePos(a, inst, one, Market, zero)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		if o != nil {
			out = append(out, o)
		}
	}
	return out, firstErr
}

// ---------- 杠杆、保证金模式 ----------

func (e *Engine) SetLeverage(acct int64, inst string, l int) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, err := e.live(acct)
	if err != nil {
		return err
	}
	spec, ok := e.Specs.Get(inst)
	if !ok {
		return rej("BAD_INSTRUMENT")
	}
	if l < 1 || l > spec.MaxLeverage() {
		return rej("LEVERAGE_RANGE", "max", fmt.Sprint(spec.MaxLeverage()))
	}
	set := a.setting(inst)
	if l == set.Leverage {
		return nil
	}
	mk := e.marks()
	p := a.Pos[inst]
	if p != nil && !p.Qty.IsZero() {
		mark, _ := mk(inst)
		if spec.MaxLevFor(p.Qty.Abs().Mul(mark)) < l {
			return rej("LEVERAGE_TOO_HIGH_FOR_SIZE", "max", fmt.Sprint(spec.MaxLevFor(p.Qty.Abs().Mul(mark))))
		}
	}
	// 用新杠杆试算：可用余额不能变成负数
	old := set.Leverage
	set.Leverage = l
	m := e.compute(a, mk)
	var xfer decimal.Decimal
	if set.Mode == "isolated" && p != nil && !p.Qty.IsZero() {
		if want := p.Qty.Abs().Mul(p.Entry).Div(lev(l)); want.GreaterThan(p.IsoMargin) {
			xfer = want.Sub(p.IsoMargin) // 逐仓降杠杆：从可用余额补保证金
		}
	}
	set.Leverage = old
	if (l < old && m.available.Sign() < 0) || xfer.GreaterThan(m.available) {
		return rej("INSUFFICIENT_MARGIN", "need", fs(xfer.Round(2)), "available", fs(decimal.Max(zero, m.available).Round(2)))
	}
	if xfer.Sign() > 0 {
		e.emit(Event{Kind: "iso_xfer", Account: a.ID, IsoXfer: &IsoXferEv{Instrument: inst, Amount: xfer}})
	}
	e.emit(Event{Kind: "setting", Account: a.ID, Setting: &SettingEv{Instrument: inst, Leverage: l, Mode: set.Mode}})
	return nil
}

func (e *Engine) SetMode(acct int64, inst, mode string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, err := e.live(acct)
	if err != nil {
		return err
	}
	if _, ok := e.Specs.Get(inst); !ok {
		return rej("BAD_INSTRUMENT")
	}
	if mode != "cross" && mode != "isolated" {
		return rej("BAD_MODE")
	}
	set := a.setting(inst)
	if set.Mode == mode {
		return nil
	}
	if p := a.Pos[inst]; p != nil && !p.Qty.IsZero() {
		return rej("MODE_HAS_POSITION")
	}
	for _, o := range a.Orders {
		if o.Instrument == inst {
			return rej("MODE_HAS_ORDERS")
		}
	}
	e.emit(Event{Kind: "setting", Account: a.ID, Setting: &SettingEv{Instrument: inst, Leverage: set.Leverage, Mode: mode}})
	return nil
}

// ---------- 止盈止损 ----------

// SetTPSL 设在持仓上的止盈止损。nil = 不改；0 = 取消。
func (e *Engine) SetTPSL(acct int64, inst string, tp, sl *decimal.Decimal, by string) error {
	defer e.travel(acct)() // 模拟下单延迟：请求晚到交易所，回报再晚回来（D94）
	e.mu.Lock()
	defer e.mu.Unlock()
	a, err := e.live(acct)
	if err != nil {
		return err
	}
	if by == "" {
		by = ByMark
	}
	if by != ByMark && by != ByLast && by != ByIndex {
		return rej("BAD_TRIGGER_BY")
	}
	return e.setTPSL(a, inst, tp, sl, "", false, by)
}

func (e *Engine) setTPSL(a *Account, inst string, tp, sl *decimal.Decimal, parent string, force bool, by string) error {
	p := a.Pos[inst]
	if p == nil || p.Qty.IsZero() {
		return rej("NO_POSITION")
	}
	spec, _ := e.Specs.Get(inst)
	mark0, ok := e.markOf(inst)
	if !ok {
		return rej("MARKET_UNAVAILABLE")
	}
	mark, ok := e.triggerRef(inst, by, mark0)
	if !ok {
		return rej("MARKET_UNAVAILABLE")
	}
	long := p.Qty.Sign() > 0
	check := func(v *decimal.Decimal, isTP bool) error {
		if v == nil || v.IsZero() {
			return nil
		}
		if v.Sign() < 0 || !spec.OnTick(*v) {
			return rej("PRICE_TICK", "tick", fs(spec.Tick))
		}
		if force {
			return nil
		}
		above := v.GreaterThan(mark)
		if (isTP && long != above) || (!isTP && long == above) || v.Equal(mark) {
			return rej("WOULD_TRIGGER", "mark", fs(mark.Round(int32(-spec.Tick.Exponent()))))
		}
		return nil
	}
	if err := check(tp, true); err != nil {
		return err
	}
	if err := check(sl, false); err != nil {
		return err
	}
	side := "sell"
	if !long {
		side = "buy"
	}
	set := func(v *decimal.Decimal, typ, src string) {
		if v == nil {
			return
		}
		for _, o := range sortedOrders(a) {
			if o.Instrument == inst && o.ClosePos && o.Type == typ && o.Status == StNew {
				e.closeOrder(a, o, StCanceled, "REPLACED")
			}
		}
		if v.IsZero() {
			return
		}
		now := e.now()
		e.emit(Event{Kind: "order", Account: a.ID, Order: &Order{ID: orderID(e.seq + 1), Account: a.ID, Instrument: inst, Side: side, Type: typ,
			TriggerPx: *v, RefPx: *v, TriggerBy: by, ReduceOnly: true, ClosePos: true, Status: StNew, Source: src, Parent: parent, Created: now, Updated: now,
			PriceProtect: protectOf(a, parent)}})
	}
	set(tp, TakeProfit, "tp")
	set(sl, StopMarket, "sl")
	return nil
}

// ---------- 行情驱动：挂单成交、止盈止损触发、资金费、强平 ----------

// onTrade 市场成交价穿过挂单价就算挂单成交；恰好成交在挂单价上只算一半（排队位置未知，保守估计）。
func (e *Engine) onTrade(venue, inst string, px, qty decimal.Decimal, buy bool, t int64) {
	if venue != e.Venue {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	spec, ok := e.Specs.Get(inst)
	if !ok || e.fundingPaused(inst) {
		return
	}
	for _, a := range e.accounts {
		if a.Archived {
			continue
		}
		for _, o := range sortedOrders(a) {
			if o.Instrument != inst || o.Type != Limit || !o.open() || o.buy() == buy {
				continue // 只有主动方向着我们来的成交才可能成交到我们：买单等主动卖，卖单等主动买
			}
			through := (o.buy() && px.LessThan(o.Price)) || (!o.buy() && px.GreaterThan(o.Price))
			if !through && !px.Equal(o.Price) {
				continue
			}
			// 排队（审查第 3 条）：价格穿过我们说明这一档已经吃完，前面没人了；
			// 恰好在我们的价成交，先吃前面排着的。一笔成交能给我们的，不超过这笔成交本身
			take := qty
			ahead := e.queueAhead(o)
			if through {
				ahead = zero
			} else {
				d := decimal.Min(ahead, take)
				ahead, take = ahead.Sub(d), take.Sub(d)
			}
			e.queue[o.ID] = ahead
			e.makerFill(a, o, spec.RoundQty(decimal.Min(o.remaining(), take)))
		}
	}
}

// queueAhead 这张挂单前面还排着多少。第一次问时按现在盘口这一档的量算：刚挂上，排在最后。
// 挂在买一卖一中间（这一档盘口上没有）的是 0。
func (e *Engine) queueAhead(o *Order) decimal.Decimal {
	if q, ok := e.queue[o.ID]; ok {
		return q
	}
	q := zero
	if b, ok := e.Books.Book(e.Venue, o.Instrument); ok {
		lv := b.Asks
		if o.buy() {
			lv = b.Bids
		}
		q = levelQty(lv, o.Price)
	}
	if e.queue == nil {
		e.queue = map[string]decimal.Decimal{}
	}
	e.queue[o.ID] = q
	return q
}

// levelQty 盘口这一边在这个价位挂着多少（不在前 N 档里的算 0）。
func levelQty(lv []book.Level, px decimal.Decimal) decimal.Decimal {
	for _, l := range lv {
		if l.Px.Equal(px) {
			return l.Qty
		}
	}
	return zero
}

func (e *Engine) makerFill(a *Account, o *Order, q decimal.Decimal) {
	if o.ReduceOnly {
		q = decimal.Min(q, e.reducible(a, o))
	}
	if q.Sign() <= 0 {
		return
	}
	e.fill(a, o, q, o.Price, true)
}

// Run 每 200 毫秒检查一次标记价相关的事。
func (e *Engine) Run(ctx context.Context) {
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				e.settleGaps(ctx)
				e.replayGaps(ctx)
			}
		}
	}()
	t := time.NewTicker(200 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.Tick()
		}
	}
}

func (e *Engine) Tick() {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	e.expireGTD(now)
	for inst, spec := range e.Specs.All() {
		mark, ok := e.markOf(inst)
		if !ok {
			continue
		}
		e.watchOutage(inst)
		e.fundingCheck(inst, mark, now)
		e.bookCross(inst)
		e.triggers(inst, spec, mark)
	}
	e.liquidations()
}

func (e *Engine) fundingCheck(inst string, mark decimal.Decimal, _ int64) {
	tk, ok := e.Books.Ticker(e.Venue, inst)
	if !ok || tk.NextFunding == 0 {
		return
	}
	st, seen := e.funding[inst]
	// 交易所把"下次结算时间"往后推了，说明上一期刚结算：按上一期最后看到的费率收付
	if seen && tk.NextFunding > st.next {
		if e.FundingHistory != nil && e.now()-st.next > fundingLiveWindow.Milliseconds() {
			// 结算时我们没在跑，可能不止错过一期：不猜，等 settleGaps 取交易所的真实记录
			if g, ok := e.fundGaps[inst]; !ok || st.next < g.from {
				if e.fundGaps == nil {
					e.fundGaps = map[string]fundGap{}
				}
				e.fundGaps[inst] = fundGap{from: st.next, seen: e.now()}
			}
		} else if e.FundingHistory != nil {
			// 实时看到这一期结算：推送里的费率只是预估，正式的要交易所结算时才定（11-18 对照：实时结的两笔和币安公布的差了约 1%）。
			// 记下这一刻的持仓，等交易所公布正式记录（通常几秒到一分钟）按它的费率和标记价结；撮合不用停，用的是这一刻的持仓
			if snap := e.posSnap(inst, st.next); len(snap) > 0 {
				now := e.now()
				e.addPending(fundGap{inst: inst, from: st.next - 60_000, to: st.next + 60_000, settle: st.next, seen: now, born: now, snap: snap, est: st.rate, estMark: mark})
			}
		} else { // 没有交易所记录可查（模拟行情）：按推送里的费率结
			for _, id := range e.accountIDs() {
				a := e.accounts[id]
				if p := a.Pos[inst]; !a.Archived && p != nil && !p.Qty.IsZero() {
					e.emit(Event{Kind: "funding", Account: id, Funding: &FundingEv{Instrument: inst, Rate: st.rate, Mark: mark, SettleTime: st.next}})
				}
			}
		}
	}
	if !seen || tk.NextFunding >= st.next {
		e.funding[inst] = fundState{rate: tk.FundingRate, next: tk.NextFunding}
	}
}

// fundingPaused 这个品种正在等错过的资金费记录（inst 为空：任何品种）。
func (e *Engine) fundingPaused(inst string) bool {
	now := e.now()
	for k, g := range e.fundGaps {
		if (inst == "" || inst == k) && now-g.seen < fundingPauseMax.Milliseconds() {
			return true
		}
	}
	return false
}

// settleGaps 补错过的资金费：取不到记录就留着，下次再试。按（账户、品种、结算时刻）去重，绝不扣两遍。
func (e *Engine) settleGaps(ctx context.Context) int {
	e.mu.Lock()
	gaps := make(map[string]fundGap, len(e.fundGaps))
	for k, g := range e.fundGaps {
		gaps[k] = g
	}
	pend := make(map[string]fundGap, len(e.fundWait))
	for k, g := range e.fundWait {
		pend[k] = g
	}
	hist := e.FundingHistory
	now := e.now()
	e.mu.Unlock()
	if hist == nil {
		return 0
	}
	total := 0
	for inst, g := range gaps {
		recs, err := hist(ctx, inst, g.from, now)
		if err != nil {
			e.Log.Warn("trading: 取资金费结算记录失败，稍后重试", "instrument", inst, "err", err)
			continue
		}
		total += e.CatchUpFunding(inst, recs, g.from-1)
		e.mu.Lock()
		delete(e.fundGaps, inst)
		e.mu.Unlock()
	}
	for key, g := range pend {
		if now-g.seen < 30_000 && g.seen != now { // 隔半分钟取一次，不要每 2 秒撞一次
			continue
		}
		recs, err := hist(ctx, g.inst, g.from, g.to)
		notYet := err == nil && g.settle > 0 && !hasSettle(recs, g.settle) // 交易所还没公布这一期
		e.mu.Lock()
		if err != nil || notYet {
			if g.settle > 0 && now-g.born > fundingLiveFallback.Milliseconds() {
				total += e.catchUp(g.inst, []FundingSettle{{Time: g.settle, Rate: g.est, Mark: g.estMark}}, g.settle-1, g.settle, g.snap)
				delete(e.fundWait, key)
				e.mu.Unlock()
				e.Log.Warn("trading: 交易所的资金费记录 15 分钟还没取到，按推送里的预估费率结", "instrument", g.inst, "err", err)
				continue
			}
			g.seen = now
			e.fundWait[key] = g
			e.mu.Unlock()
			if err != nil {
				e.Log.Warn("trading: 取资金费结算记录失败，稍后重试", "instrument", g.inst, "err", err)
			}
			continue
		}
		total += e.catchUp(g.inst, recs, g.from-1, g.to, g.snap)
		delete(e.fundWait, key)
		e.mu.Unlock()
	}
	return total
}

func hasSettle(recs []FundingSettle, t int64) bool {
	for _, r := range recs {
		if d := r.Time - t; d > -60_000 && d < 60_000 {
			return true
		}
	}
	return false
}

// addPending 调用的人持有锁。
func (e *Engine) addPending(g fundGap) {
	if e.fundWait == nil {
		e.fundWait = map[string]fundGap{}
	}
	e.fundWait[g.inst+"@"+strconv.FormatInt(g.from, 10)] = g
}

// posSnap 这个品种在 before 之前开的持仓（带符号），调用的人持有锁。
func (e *Engine) posSnap(inst string, before int64) map[int64]decimal.Decimal {
	snap := map[int64]decimal.Decimal{}
	for _, id := range e.accountIDs() {
		a := e.accounts[id]
		if p := a.Pos[inst]; !a.Archived && p != nil && !p.Qty.IsZero() && p.Opened <= before {
			snap[id] = p.Qty
		}
	}
	return snap
}

func (e *Engine) accountIDs() []int64 {
	ids := make([]int64, 0, len(e.accounts))
	for id := range e.accounts {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// bookCross 盘口已经越过挂单价（例如卖一价低于我们的买价）：真实市场里这张单早就被成交了。
func (e *Engine) bookCross(inst string) {
	b, ok := e.Books.Book(e.Venue, inst)
	if !ok || e.staleFor(inst) > 0 || e.fundingPaused(inst) {
		return
	}
	spec, ok := e.Specs.Get(inst)
	if !ok {
		return
	}
	if e.queue == nil {
		e.queue = map[string]decimal.Decimal{}
	}
	for _, a := range e.accounts {
		if a.Archived {
			continue
		}
		for _, o := range sortedOrders(a) {
			if o.Instrument != inst || o.Type != Limit || !o.open() {
				continue
			}
			// 越过去的那一档有多少，最多成交多少（审查第 3 条：以前不管挂多大一次全部成交）
			switch {
			case o.buy() && len(b.Asks) > 0 && b.Asks[0].Px.LessThan(o.Price):
				e.queue[o.ID] = zero
				e.makerFill(a, o, spec.RoundQty(decimal.Min(o.remaining(), b.Asks[0].Qty)))
			case !o.buy() && len(b.Bids) > 0 && b.Bids[0].Px.GreaterThan(o.Price):
				e.queue[o.ID] = zero
				e.makerFill(a, o, spec.RoundQty(decimal.Min(o.remaining(), b.Bids[0].Qty)))
			}
			if cur, ok := a.Orders[o.ID]; !ok || !cur.open() {
				continue
			}
			// 前面有人撤单：这一档的量比记着的"前面"还少，就按这一档的量算（全算成排在我们前面的人撤的，偏乐观）
			lv := b.Asks
			if o.buy() {
				lv = b.Bids
			}
			if l := levelQty(lv, o.Price); l.Sign() > 0 && l.LessThan(e.queueAhead(o)) {
				e.queue[o.ID] = l
			}
		}
	}
}

func (e *Engine) triggers(inst string, spec *Spec, mark decimal.Decimal) {
	if e.staleFor(inst) > 0 || e.fundingPaused(inst) { // 盘口停了：等恢复再触发，不按过期盘口成交
		return
	}
	for _, id := range e.accountIDs() {
		a := e.accounts[id]
		if a.Archived {
			continue
		}
		for _, o := range sortedOrders(a) {
			if o.Instrument != inst || !o.conditional() || o.Status != StNew {
				continue
			}
			px, ok := e.triggerRef(inst, o.TriggerBy, mark)
			if !ok {
				continue
			}
			if o.PriceProtect && e.protectBlocks(inst, spec, mark) { // 价格保护：标记价和最新价差太多，这次不触发
				continue
			}
			up := px.GreaterThanOrEqual(o.TriggerPx)
			down := px.LessThanOrEqual(o.TriggerPx)
			if o.entryCond() { // 条件单：价格按下单时判断的方向到达触发价
				if (o.TriggerDir == "up" && up) || (o.TriggerDir == "down" && down) {
					e.fireCond(a, o)
				}
				continue
			}
			// 平多（卖）：止盈在价格 ≥ 触发价时触发，止损在 ≤ 时；平空反过来
			hit := (o.Type == TakeProfit && ((!o.buy() && up) || (o.buy() && down))) ||
				(o.Type == StopMarket && ((!o.buy() && down) || (o.buy() && up)))
			if !hit {
				continue
			}
			q := e.reducible(a, o)
			if !o.ClosePos {
				q = decimal.Min(q, o.Qty)
			}
			if q.Sign() <= 0 {
				e.closeOrder(a, o, StCanceled, "NO_POSITION")
				continue
			}
			e.emit(Event{Kind: "trigger", Account: id, Amend: &AmendEv{ID: o.ID, Qty: q}})
			e.take(a, o, spec, e.band(o, spec))
			e.expireRest(a, o, "NO_LIQUIDITY")
		}
	}
}

func (e *Engine) liquidations() {
	if e.fundingPaused("") {
		return
	}
	mk := e.marks()
	for _, id := range e.accountIDs() {
		a := e.accounts[id]
		if a.Archived {
			continue
		}
		// 逐仓：逐仓保证金 + 未实现盈亏 ≤ 维持保证金
		for _, inst := range sortedInsts(a) {
			p := a.Pos[inst]
			if a.setting(inst).Mode != "isolated" || p.Qty.IsZero() {
				continue
			}
			spec, ok := e.Specs.Get(inst)
			mark, okm := mk(inst)
			if !ok || !okm {
				continue
			}
			if p.IsoMargin.Add(upnl(p, mark)).LessThanOrEqual(spec.MaintMargin(p.Qty.Abs().Mul(mark))) {
				if !e.partialIso(a, inst, mark) { // 先试部分强平（大仓位），不够再全部强平
					e.liquidateIso(a, inst, mark)
				}
			}
		}
		// 全仓：保证金余额 ≤ 维持保证金
		m := e.compute(a, mk)
		if m.crossMM.Sign() > 0 && m.crossWB.Add(m.crossUPnL).LessThanOrEqual(m.crossMM) {
			if !e.partialCross(a, mk, m) {
				e.liquidateCross(a, mk, m.crossMM)
			}
		}
	}
}

// clearFee 强平清算费：费用 = 名义价值 × 费率，但最多扣到剩下的钱为 0；剩下的已经是负数（穿仓）时返回负数，由保险基金补。
func clearFee(left, notional, rate decimal.Decimal) decimal.Decimal {
	if left.Sign() <= 0 {
		return left
	}
	return decimal.Min(left, notional.Mul(rate))
}

// liqRate 这个品种的强平清算费率（exchangeInfo 的 liquidationFee）；读不到时按币安常见的 1.25%。
func (e *Engine) liqRate(inst string) decimal.Decimal {
	if s, ok := e.Specs.Get(inst); ok && s.LiqFee.Sign() > 0 {
		return s.LiqFee
	}
	return decimal.RequireFromString("0.0125")
}

func sortedInsts(a *Account) []string {
	out := make([]string, 0, len(a.Pos))
	for k := range a.Pos {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (e *Engine) liquidateIso(a *Account, inst string, mark decimal.Decimal) {
	before := a.Wallet
	for _, o := range sortedOrders(a) {
		if o.Instrument == inst {
			e.closeOrder(a, o, StCanceled, "LIQUIDATION")
		}
	}
	p := a.Pos[inst]
	iso := p.IsoMargin
	realized := p.Qty.Mul(mark.Sub(p.Entry))
	notional := p.Qty.Abs().Mul(mark)
	e.emit(Event{Kind: "fill", Account: a.ID, Fill: &FillEv{OrderID: "LIQ", Instrument: inst, Buy: p.Qty.Sign() < 0, Qty: p.Qty.Abs(), Px: mark, Liq: true}})
	// 强平清算费（币安）：名义价值 × 这个品种的清算费率，从剩下的逐仓保证金里扣，扣到 0 为止；
	// 剩下的还给钱包。BTC 第一档维持保证金率 0.4% 比费率 1.25% 低，所以小仓位强平基本是整个逐仓保证金都没了。
	// 穿仓（剩下的是负数）时由保险基金补，记成 covered（审查第 2 条）
	fee := clearFee(iso.Add(realized), notional, e.liqRate(inst))
	e.emit(Event{Kind: "liq_fee", Account: a.ID, LiqFee: &LiqFeeEv{Instrument: inst, Mark: mark, Amount: fee, Loss: before.Sub(a.Wallet).Add(fee)}})
}

func (e *Engine) liquidateCross(a *Account, mk marks, mm decimal.Decimal) {
	before := a.Wallet
	cross := map[string]bool{}
	for _, inst := range sortedInsts(a) {
		if a.setting(inst).Mode != "isolated" && !a.Pos[inst].Qty.IsZero() {
			cross[inst] = true
		}
	}
	for _, o := range sortedOrders(a) {
		if a.setting(o.Instrument).Mode != "isolated" {
			e.closeOrder(a, o, StCanceled, "LIQUIDATION")
		}
	}
	var names []string
	charge := zero // 各仓位 名义价值 × 清算费率 的和
	for _, inst := range sortedInsts(a) {
		if !cross[inst] {
			continue
		}
		p := a.Pos[inst]
		mark, _ := mk(inst)
		names = append(names, inst)
		charge = charge.Add(p.Qty.Abs().Mul(mark).Mul(e.liqRate(inst)))
		e.emit(Event{Kind: "fill", Account: a.ID, Fill: &FillEv{OrderID: "LIQ", Instrument: inst, Buy: p.Qty.Sign() < 0, Qty: p.Qty.Abs(), Px: mark, Liq: true}})
	}
	_ = mm
	iso := zero
	for _, p := range a.Pos {
		iso = iso.Add(p.IsoMargin)
	}
	// 全仓：清算费从剩下的全仓余额里扣，扣到 0 为止；穿仓时保险基金补到 0
	fee := clearFee(a.Wallet.Sub(iso), decimal.NewFromInt(1), charge)
	e.emit(Event{Kind: "liq_fee", Account: a.ID, LiqFee: &LiqFeeEv{Instrument: strings.Join(names, ","), Amount: fee, Loss: before.Sub(a.Wallet).Add(fee)}})
}

// Rej 给别的包（实盘交易台）用：生成和引擎一样的拒单原因，界面按同一套词条翻译。
func Rej(code string, kv ...string) *Reject { return rej(code, kv...) }
