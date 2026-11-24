package binance

// Fake 一个假的币安合约交易所，给测试和测试环境用（没有真账号时也能把实盘这条路从头走到尾）。
// 只实现客户端用到的接口，规则尽量照真的来：签名和时间戳、单向持仓、市价和限价、条件单（Algo）、杠杆和保证金模式、
// 成交和资金费记录、用户数据流。撮合很简单：市价按当前价成交，限价单价格穿过当前价就成交。
// 不是交易所的完整复刻，不要拿它的数字当真。

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

type fakePos struct {
	amt, entry float64
	isolated   bool
	isoMargin  float64
	lev        int
}

type fakeOrder struct {
	Order
	algo bool
	// 条件单
	trigger float64
	algoID  int64
	status  string
}

// Fake 假交易所。用 NewFake 建，Close 关。
type Fake struct {
	Key, Secret string
	Withdraw    bool // 这把密钥开了提现权限（用来测"拒绝"）
	Dual        bool // 双向持仓模式
	Taker       float64
	Maker       float64
	// NoAvgInReply 下单回复里成交了、却不带均价（币安模拟交易常这样，11-16 实测）；均价只在推送和成交记录里有
	NoAvgInReply bool
	// Delay 每个请求先等这么久再处理（模拟网络慢、交易所卡住）。测试里中途改它，用 SetDelay
	delay atomic.Int64

	srv     *httptest.Server
	mu      sync.Mutex
	prices  map[string]float64
	wallet  float64
	pos     map[string]*fakePos
	orders  map[int64]*fakeOrder
	algos   map[int64]*fakeOrder
	trades  []Trade
	income  []Income
	nextID  int64
	listen  map[string]bool
	streams []chan []byte
	clock   func() time.Time
}

// NewFake 起一个假交易所，钱包里有 wallet USDT。
func NewFake(key, secret string, wallet float64) *Fake {
	f := &Fake{Key: key, Secret: secret, Taker: 0.0005, Maker: 0.0002, prices: map[string]float64{}, wallet: wallet,
		pos: map[string]*fakePos{}, orders: map[int64]*fakeOrder{}, algos: map[int64]*fakeOrder{}, nextID: 1000, listen: map[string]bool{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	return f
}

// URL REST 地址；WSURL 用户数据流地址。
func (f *Fake) URL() string   { return f.srv.URL }
func (f *Fake) WSURL() string { return "ws" + strings.TrimPrefix(f.srv.URL, "http") }
func (f *Fake) Close()        { f.srv.Close() }

func (f *Fake) now() time.Time {
	if f.clock != nil {
		return f.clock()
	}
	return time.Now()
}

// Client 连这个假交易所的客户端。
func (f *Fake) Client() *Client {
	return &Client{FAPI: f.URL(), SAPI: f.URL(), WS: f.WSURL(), Key: f.Key, Secret: f.Secret}
}

// SetPrice 改价格：会触发条件单、成交穿价的限价单。
func (f *Fake) SetPrice(symbol string, px float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prices[symbol] = px
	f.match(symbol)
}

// External 模拟在别处（币安 App、网页）下的一笔市价单：客户端编号不是 qs 开头。对账告警的测试用。
func (f *Fake) External(symbol, side string, qty float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	px := f.prices[symbol]
	if px == 0 {
		return
	}
	f.nextID++
	now := f.now().UnixMilli()
	o := &fakeOrder{Order: Order{OrderID: f.nextID, ClientOrderID: fmt.Sprintf("web_%d", f.nextID), Symbol: symbol, Side: side, Type: "MARKET",
		OrigQty: num(qty), ExecutedQty: "0", AvgPrice: "0", Status: "NEW", Time: now, UpdateTime: now}}
	f.pushOrder(o, "NEW", 0, 0, 0)
	f.fill(o, px, true)
}

// Transfer 模拟从现货钱包划转进来（正数）或转出去（负数）。
func (f *Fake) Transfer(amount float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.wallet += amount
	f.nextID++
	f.income = append(f.income, Income{IncomeType: "TRANSFER", Income: num(amount), Asset: "USDT", Time: f.now().UnixMilli(), TranID: f.nextID})
	f.pushAccount("DEPOSIT")
}

func num(v float64) Num { return Num(strconv.FormatFloat(v, 'f', -1, 64)) }

func (f *Fake) fail(w http.ResponseWriter, status, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "msg": msg})
}

func (f *Fake) ok(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// auth 查密钥和签名（签名对去掉 signature 的查询串算）；时间戳差太多报 -1021。
func (f *Fake) auth(w http.ResponseWriter, r *http.Request, signed bool) (url.Values, bool) {
	f.mu.Lock()
	key := f.Key
	f.mu.Unlock()
	if r.Header.Get("X-MBX-APIKEY") != key {
		f.fail(w, 401, -2015, "Invalid API-key, IP, or permissions for action.")
		return nil, false
	}
	q := r.URL.Query()
	if !signed {
		return q, true
	}
	raw := r.URL.RawQuery
	i := strings.LastIndex(raw, "&signature=")
	if i < 0 {
		f.fail(w, 400, -1102, "Mandatory parameter 'signature' was not sent.")
		return nil, false
	}
	m := hmac.New(sha256.New, []byte(f.Secret))
	m.Write([]byte(raw[:i]))
	if raw[i+len("&signature="):] != hex.EncodeToString(m.Sum(nil)) {
		f.fail(w, 400, -1022, "Signature for this request is not valid.")
		return nil, false
	}
	ts, _ := strconv.ParseInt(q.Get("timestamp"), 10, 64)
	if d := f.now().UnixMilli() - ts; d > 5000 || d < -1000 {
		f.fail(w, 400, -1021, "Timestamp for this request is outside of the recvWindow.")
		return nil, false
	}
	return q, true
}

// SetKey 换掉交易所认的密钥（模拟密钥在币安那边被删了）。
func (f *Fake) SetKey(k string) {
	f.mu.Lock()
	f.Key = k
	f.mu.Unlock()
}

// SetDelay 之后每个请求先等 d 再处理（推送不受影响）。
func (f *Fake) SetDelay(d time.Duration) { f.delay.Store(int64(d)) }

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	if d := time.Duration(f.delay.Load()); d > 0 && !strings.HasPrefix(r.URL.Path, "/private/ws/") {
		time.Sleep(d) // 不拿锁：慢的只是这一个请求
	}
	if r.URL.Path == "/fapi/v1/time" {
		f.ok(w, map[string]int64{"serverTime": f.now().UnixMilli()})
		return
	}
	if strings.HasPrefix(r.URL.Path, "/private/ws/") { // 和币安一样：账户推送只在 /private 下
		f.ws(w, r)
		return
	}
	if r.URL.Path == "/fapi/v1/listenKey" {
		if _, ok := f.auth(w, r, false); !ok {
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.Method {
		case http.MethodPost:
			k := fmt.Sprintf("lk%d", f.now().UnixNano())
			f.listen[k] = true
			f.ok(w, map[string]string{"listenKey": k})
		default:
			f.ok(w, map[string]any{})
		}
		return
	}
	q, ok := f.auth(w, r, true)
	if !ok {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	key := r.Method + " " + r.URL.Path
	switch key {
	case "GET /sapi/v1/account/apiRestrictions":
		f.ok(w, Restrictions{EnableWithdrawals: f.Withdraw, EnableFutures: true, EnableReading: true, IPRestrict: false, CreateTime: 1})
	case "GET /fapi/v1/positionSide/dual":
		f.ok(w, map[string]bool{"dualSidePosition": f.Dual})
	case "GET /fapi/v1/commissionRate":
		f.ok(w, Commission{Symbol: q.Get("symbol"), Maker: Num(strconv.FormatFloat(f.Maker, 'f', -1, 64)), Taker: Num(strconv.FormatFloat(f.Taker, 'f', -1, 64))})
	case "GET /fapi/v3/account":
		f.ok(w, f.account())
	case "GET /fapi/v3/positionRisk":
		f.ok(w, f.positions())
	case "GET /fapi/v1/adlQuantile": // 假交易所：有仓位的品种都排在第 2 档
		out := []map[string]any{}
		for s, p := range f.pos {
			if p.amt != 0 {
				out = append(out, map[string]any{"symbol": s, "adlQuantile": map[string]int{"LONG": 0, "SHORT": 0, "BOTH": 2}})
			}
		}
		f.ok(w, out)
	case "GET /fapi/v1/symbolConfig":
		var out []SymbolConfig
		for s := range f.prices {
			p := f.posOf(s)
			mt := "CROSSED"
			if p.isolated {
				mt = "ISOLATED"
			}
			out = append(out, SymbolConfig{Symbol: s, MarginType: mt, Leverage: p.lev, MaxNotionalValue: "1000000"})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Symbol < out[j].Symbol })
		f.ok(w, out)
	case "GET /fapi/v1/openOrders":
		out := []Order{}
		for _, o := range f.sorted(f.orders) {
			out = append(out, o.Order)
		}
		f.ok(w, out)
	case "GET /fapi/v1/openAlgoOrders":
		out := []AlgoOrder{}
		for _, o := range f.sorted(f.algos) {
			out = append(out, f.algoView(o))
		}
		f.ok(w, out)
	case "POST /fapi/v1/order":
		f.newOrder(w, q)
	case "PUT /fapi/v1/order":
		id, _ := strconv.ParseInt(q.Get("orderId"), 10, 64)
		o := f.orders[id]
		if o == nil {
			f.fail(w, 400, -2013, "Order does not exist.")
			return
		}
		o.Price, o.OrigQty, o.UpdateTime = Num(q.Get("price")), Num(q.Get("quantity")), f.now().UnixMilli()
		f.pushOrder(o, "AMENDMENT", 0, 0, 0)
		f.match(o.Symbol)
		f.ok(w, o.Order)
	case "DELETE /fapi/v1/order":
		id, _ := strconv.ParseInt(q.Get("orderId"), 10, 64)
		o := f.orders[id]
		if o == nil {
			f.fail(w, 400, -2011, "Unknown order sent.")
			return
		}
		delete(f.orders, id)
		o.Status = "CANCELED"
		f.pushOrder(o, "CANCELED", 0, 0, 0)
		f.ok(w, o.Order)
	case "DELETE /fapi/v1/allOpenOrders":
		for id, o := range f.orders {
			if o.Symbol == q.Get("symbol") {
				delete(f.orders, id)
				o.Status = "CANCELED"
				f.pushOrder(o, "CANCELED", 0, 0, 0)
			}
		}
		f.ok(w, map[string]any{"code": 200, "msg": "The operation of cancel all open order is done."})
	case "POST /fapi/v1/algoOrder":
		f.newAlgo(w, q)
	case "DELETE /fapi/v1/algoOrder":
		id, _ := strconv.ParseInt(q.Get("algoId"), 10, 64)
		if f.algos[id] == nil {
			f.fail(w, 400, -2011, "Unknown order sent.")
			return
		}
		f.algos[id].status = "CANCELED"
		f.pushAlgo(f.algos[id])
		delete(f.algos, id)
		f.ok(w, map[string]any{"algoId": id, "code": "200", "msg": "success"})
	case "DELETE /fapi/v1/algoOpenOrders":
		for id, o := range f.algos {
			if o.Symbol == q.Get("symbol") {
				o.status = "CANCELED"
				f.pushAlgo(o)
				delete(f.algos, id)
			}
		}
		f.ok(w, map[string]any{"code": 200, "msg": "The operation of cancel all open order is done."})
	case "POST /fapi/v1/leverage":
		lev, _ := strconv.Atoi(q.Get("leverage"))
		if lev < 1 || lev > 125 {
			f.fail(w, 400, -4028, "Leverage is not valid")
			return
		}
		f.posOf(q.Get("symbol")).lev = lev
		f.ok(w, map[string]any{"symbol": q.Get("symbol"), "leverage": lev, "maxNotionalValue": "1000000"})
	case "POST /fapi/v1/marginType":
		p := f.posOf(q.Get("symbol"))
		iso := q.Get("marginType") == "ISOLATED"
		if p.isolated == iso {
			f.fail(w, 400, -4046, "No need to change margin type.")
			return
		}
		if p.amt != 0 {
			f.fail(w, 400, -4048, "Margin type cannot be changed if there exists position.")
			return
		}
		p.isolated = iso
		f.ok(w, map[string]any{"code": 200, "msg": "success"})
	case "POST /fapi/v1/positionMargin":
		p := f.posOf(q.Get("symbol"))
		amt, _ := strconv.ParseFloat(q.Get("amount"), 64)
		if !p.isolated || p.amt == 0 {
			f.fail(w, 400, -4051, "Isolated balance insufficient.")
			return
		}
		if q.Get("type") == "2" {
			amt = -amt
		}
		p.isoMargin += amt
		f.ok(w, map[string]any{"code": 200, "msg": "Successfully modify position margin.", "amount": math.Abs(amt), "type": q.Get("type")})
	case "GET /fapi/v1/userTrades":
		out := []Trade{}
		from, _ := strconv.ParseInt(q.Get("fromId"), 10, 64)
		start, _ := strconv.ParseInt(q.Get("startTime"), 10, 64)
		for _, t := range f.trades {
			if t.Symbol == q.Get("symbol") && t.ID >= from && t.Time >= start {
				out = append(out, t)
			}
		}
		f.ok(w, out)
	case "GET /fapi/v1/income":
		out := []Income{}
		start, _ := strconv.ParseInt(q.Get("startTime"), 10, 64)
		typ := q.Get("incomeType")
		for _, x := range f.income {
			if x.Time >= start && (typ == "" || x.IncomeType == typ) {
				out = append(out, x)
			}
		}
		f.ok(w, out)
	default:
		f.fail(w, 404, -5000, "fake: not implemented "+key)
	}
}

func (f *Fake) posOf(s string) *fakePos {
	p := f.pos[s]
	if p == nil {
		p = &fakePos{lev: 20}
		f.pos[s] = p
	}
	return p
}

func (f *Fake) sorted(m map[int64]*fakeOrder) []*fakeOrder {
	out := make([]*fakeOrder, 0, len(m))
	for _, o := range m {
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].OrderID < out[j].OrderID })
	return out
}

func (f *Fake) algoView(o *fakeOrder) AlgoOrder {
	return AlgoOrder{AlgoID: o.algoID, ClientAlgoID: o.ClientOrderID, AlgoType: "CONDITIONAL", OrderType: o.Type, Symbol: o.Symbol, Side: o.Side,
		Quantity: o.OrigQty, AlgoStatus: o.status, TriggerPrice: num(o.trigger), Price: o.Price, WorkingType: o.WorkingType,
		ClosePosition: o.ClosePosition, ReduceOnly: o.ReduceOnly, CreateTime: o.Time, UpdateTime: o.UpdateTime}
}

func (f *Fake) upnl() float64 {
	u := 0.0
	for s, p := range f.pos {
		u += p.amt * (f.prices[s] - p.entry)
	}
	return u
}

func (f *Fake) account() Account {
	im, mm := 0.0, 0.0
	for s, p := range f.pos {
		n := math.Abs(p.amt) * f.prices[s]
		if p.lev > 0 {
			im += n / float64(p.lev)
		}
		mm += n * 0.004
	}
	u := f.upnl()
	return Account{TotalWalletBalance: num(f.wallet), TotalUnrealizedProfit: num(u), TotalMarginBalance: num(f.wallet + u),
		TotalPositionInitialMargin: num(im), TotalMaintMargin: num(mm), AvailableBalance: num(f.wallet + u - im), MaxWithdrawAmount: num(f.wallet - im)}
}

func (f *Fake) positions() []Position {
	out := []Position{}
	for s, p := range f.pos {
		if p.amt == 0 {
			continue
		}
		px := f.prices[s]
		out = append(out, Position{Symbol: s, PositionSide: "BOTH", PositionAmt: num(p.amt), EntryPrice: num(p.entry), MarkPrice: num(px),
			UnRealizedProfit: num(p.amt * (px - p.entry)), Notional: num(p.amt * px), IsolatedMargin: num(p.isoMargin), IsolatedWallet: num(p.isoMargin),
			InitialMargin: num(math.Abs(p.amt) * px / float64(max(1, p.lev))), MaintMargin: num(math.Abs(p.amt) * px * 0.004), UpdateTime: f.now().UnixMilli()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Symbol < out[j].Symbol })
	return out
}

func (f *Fake) newOrder(w http.ResponseWriter, q url.Values) {
	typ := q.Get("type")
	if typ != "LIMIT" && typ != "MARKET" {
		f.fail(w, 400, -4120, "Order type not supported for this endpoint. Please use the Algo Order API endpoints instead.")
		return
	}
	s := q.Get("symbol")
	px, ok := f.prices[s]
	if !ok {
		f.fail(w, 400, -1121, "Invalid symbol.")
		return
	}
	qty, _ := strconv.ParseFloat(q.Get("quantity"), 64)
	if qty <= 0 {
		f.fail(w, 400, -4003, "Quantity less than or equal to zero.")
		return
	}
	side := q.Get("side")
	reduce := q.Get("reduceOnly") == "true"
	p := f.posOf(s)
	if reduce && (p.amt == 0 || (p.amt > 0) == (side == "BUY")) {
		f.fail(w, 400, -2022, "ReduceOnly Order is rejected.")
		return
	}
	// 保证金粗查：新开的部分 ÷ 杠杆 不能超过可用
	if !reduce {
		need := qty * px / float64(max(1, p.lev))
		if need > f.wallet+f.upnl()-f.usedIM() {
			f.fail(w, 400, -2019, "Margin is insufficient.")
			return
		}
	}
	f.nextID++
	now := f.now().UnixMilli()
	o := &fakeOrder{Order: Order{OrderID: f.nextID, ClientOrderID: q.Get("newClientOrderId"), Symbol: s, Side: side, Type: typ, TimeInForce: q.Get("timeInForce"),
		Price: Num(q.Get("price")), OrigQty: Num(q.Get("quantity")), ExecutedQty: "0", AvgPrice: "0", Status: "NEW", ReduceOnly: reduce, Time: now, UpdateTime: now}}
	if o.ClientOrderID == "" {
		o.ClientOrderID = fmt.Sprintf("auto%d", o.OrderID)
	}
	if typ == "LIMIT" && q.Get("timeInForce") == "GTX" && f.crosses(o, px) {
		o.Status = "EXPIRED"
		f.pushOrder(o, "EXPIRED", 0, 0, 0)
		f.ok(w, o.Order)
		return
	}
	f.pushOrder(o, "NEW", 0, 0, 0)
	if typ == "MARKET" || f.crosses(o, px) {
		f.fill(o, px, true) // 市价单、穿价的限价单都按吃单
	} else {
		f.orders[o.OrderID] = o
	}
	reply := o.Order
	if f.NoAvgInReply {
		reply.AvgPrice = "0"
	}
	f.ok(w, reply)
}

func (f *Fake) usedIM() float64 {
	im := 0.0
	for s, p := range f.pos {
		im += math.Abs(p.amt) * f.prices[s] / float64(max(1, p.lev))
	}
	return im
}

func (f *Fake) crosses(o *fakeOrder, px float64) bool {
	lp := o.Price.F()
	return (o.Side == "BUY" && px <= lp) || (o.Side == "SELL" && px >= lp)
}

// fill 整张成交（价格 px），更新仓位、钱包，记成交，推送事件。
func (f *Fake) fill(o *fakeOrder, px float64, taker bool) {
	qty := o.OrigQty.F()
	p := f.posOf(o.Symbol)
	if o.ReduceOnly {
		qty = math.Min(qty, math.Abs(p.amt))
	}
	sign := 1.0
	if o.Side == "SELL" {
		sign = -1
	}
	realized := 0.0
	d := sign * qty
	if p.amt != 0 && (p.amt > 0) != (d > 0) { // 减仓或反手
		closeQ := math.Min(math.Abs(d), math.Abs(p.amt))
		realized = closeQ * (px - p.entry) * math.Copysign(1, p.amt)
		rest := math.Abs(d) - closeQ
		p.amt += math.Copysign(closeQ, d)
		if math.Abs(p.amt) < 1e-12 {
			p.amt, p.entry = 0, 0
		}
		if rest > 0 {
			p.amt, p.entry = math.Copysign(rest, d), px
		}
	} else {
		n := math.Abs(p.amt)*p.entry + math.Abs(d)*px
		p.amt += d
		p.entry = n / math.Abs(p.amt)
	}
	rate := f.Taker
	if !taker {
		rate = f.Maker
	}
	fee := qty * px * rate
	f.wallet += realized - fee
	o.ExecutedQty, o.AvgPrice, o.Status = num(qty), num(px), "FILLED"
	o.UpdateTime = f.now().UnixMilli()
	f.nextID++
	t := Trade{ID: f.nextID, OrderID: o.OrderID, Symbol: o.Symbol, Side: o.Side, Price: num(px), Qty: num(qty), RealizedPnl: num(realized),
		Commission: num(fee), CommissionAsset: "USDT", QuoteQty: num(qty * px), Time: o.UpdateTime, Maker: !taker}
	f.trades = append(f.trades, t)
	f.pushOrder(o, "TRADE", qty, px, fee, t.ID, realized, !taker)
	f.pushAccount("ORDER")
}

// match 价格变了：挂着的限价单穿价就成交，条件单到价就触发。
func (f *Fake) match(s string) {
	px := f.prices[s]
	for id, o := range f.orders {
		if o.Symbol == s && f.crosses(o, px) {
			delete(f.orders, id)
			f.fill(o, o.Price.F(), false)
		}
	}
	for id, a := range f.algos {
		if a.Symbol != s {
			continue
		}
		up := a.Type == "STOP_MARKET" || a.Type == "STOP"
		if a.Side == "SELL" {
			up = !up
		}
		hit := (up && px >= a.trigger) || (!up && px <= a.trigger)
		if !hit {
			continue
		}
		delete(f.algos, id)
		a.status = "TRIGGERED"
		f.pushAlgo(a)
		f.nextID++
		o := &fakeOrder{Order: a.Order}
		o.OrderID = f.nextID
		o.Type = "MARKET"
		if a.Type == "STOP" || a.Type == "TAKE_PROFIT" {
			o.Type = "LIMIT"
		}
		if a.ClosePosition {
			o.OrigQty, o.ReduceOnly = num(math.Abs(f.posOf(s).amt)), true
		}
		if o.Type == "MARKET" || f.crosses(o, px) {
			f.fill(o, px, true)
		} else {
			f.orders[o.OrderID] = o
		}
		a.status = "FINISHED"
		f.pushAlgo(a)
	}
}

func (f *Fake) newAlgo(w http.ResponseWriter, q url.Values) {
	if q.Get("algoType") != "CONDITIONAL" {
		f.fail(w, 400, -1102, "algoType must be CONDITIONAL")
		return
	}
	s := q.Get("symbol")
	px, ok := f.prices[s]
	if !ok {
		f.fail(w, 400, -1121, "Invalid symbol.")
		return
	}
	typ := q.Get("type")
	trig, _ := strconv.ParseFloat(q.Get("triggerPrice"), 64)
	side := q.Get("side")
	up := typ == "STOP_MARKET" || typ == "STOP"
	if side == "SELL" {
		up = !up
	}
	if (up && px >= trig) || (!up && px <= trig) {
		f.fail(w, 400, -2021, "Order would immediately trigger.")
		return
	}
	close := q.Get("closePosition") == "true"
	if close && q.Get("quantity") != "" {
		f.fail(w, 400, -1106, "Parameter 'quantity' sent when not required.")
		return
	}
	f.nextID++
	now := f.now().UnixMilli()
	o := &fakeOrder{Order: Order{ClientOrderID: q.Get("clientAlgoId"), Symbol: s, Side: side, Type: typ, Price: Num(q.Get("price")), OrigQty: Num(q.Get("quantity")),
		ReduceOnly: q.Get("reduceOnly") == "true", ClosePosition: close, WorkingType: q.Get("workingType"), Time: now, UpdateTime: now}, algo: true, trigger: trig, algoID: f.nextID, status: "NEW"}
	if o.ClientOrderID == "" {
		o.ClientOrderID = fmt.Sprintf("algo%d", o.algoID)
	}
	o.OrderID = o.algoID
	f.algos[o.algoID] = o
	f.pushAlgo(o)
	f.ok(w, f.algoView(o))
}

// ---------- 用户数据流 ----------

func (f *Fake) ws(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	ok := f.listen[strings.TrimPrefix(r.URL.Path, "/private/ws/")]
	f.mu.Unlock()
	if !ok {
		http.Error(w, "bad listenKey", 400)
		return
	}
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.CloseNow()
	ch := make(chan []byte, 256)
	f.mu.Lock()
	f.streams = append(f.streams, ch)
	f.mu.Unlock()
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case b := <-ch:
			if c.Write(ctx, websocket.MessageText, b) != nil {
				return
			}
		}
	}
}

func (f *Fake) push(v any) {
	b, _ := json.Marshal(v)
	for _, ch := range f.streams {
		select {
		case ch <- b:
		default:
		}
	}
}

func (f *Fake) pushOrder(o *fakeOrder, exec string, lastQty, lastPx, fee float64, extra ...any) {
	m := map[string]any{"s": o.Symbol, "c": o.ClientOrderID, "S": o.Side, "o": o.Type, "f": o.TimeInForce, "q": o.OrigQty, "p": o.Price,
		"ap": o.AvgPrice, "x": exec, "X": o.Status, "i": o.OrderID, "l": num(lastQty), "z": o.ExecutedQty, "L": num(lastPx), "N": "USDT", "n": num(fee),
		"T": f.now().UnixMilli(), "R": o.ReduceOnly}
	if len(extra) == 3 {
		m["t"], m["rp"], m["m"] = extra[0], num(extra[1].(float64)), extra[2]
	}
	f.push(map[string]any{"e": "ORDER_TRADE_UPDATE", "E": f.now().UnixMilli(), "T": f.now().UnixMilli(), "o": m})
}

func (f *Fake) pushAlgo(o *fakeOrder) {
	f.push(map[string]any{"e": "ALGO_UPDATE", "E": f.now().UnixMilli(), "o": map[string]any{"aid": o.algoID, "caid": o.ClientOrderID, "s": o.Symbol, "X": o.status}})
}

func (f *Fake) pushAccount(reason string) {
	f.push(map[string]any{"e": "ACCOUNT_UPDATE", "E": f.now().UnixMilli(), "a": map[string]any{"m": reason, "B": []any{map[string]any{"a": "USDT", "wb": num(f.wallet)}}}})
}
