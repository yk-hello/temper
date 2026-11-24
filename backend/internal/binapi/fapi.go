package binapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"qs/internal/book"
	"qs/internal/trading"
)

// Server 挂在 /bapi/ 下：/bapi/fapi/v1/order 就是币安的 /fapi/v1/order。
// 程序里把币安的地址（https://fapi.binance.com）换成 http://你的服务器/bapi，推送换成 ws://你的服务器/bapi。
type Server struct {
	Keys  *Store
	E     *trading.Engine
	Books *book.Store
	Venue string
	// Disabled 这个人被管理员停用了吗（停用的人的密钥不能用）
	Disabled func(ctx context.Context, user int64) bool
	// Upstream 公开行情里这里没有的（K 线、24 小时统计、资金费历史、持仓量）转给币安；空 = 不转（模拟行情）
	Upstream string
	Now      func() time.Time
	HTTP     *http.Client

	mu      sync.Mutex
	clients map[int64]map[string]string // 账户 → 委托编号 → 客户端编号
	byCID   map[int64]map[string]string // 账户 → 客户端编号 → 委托编号
	lk      map[string]*listen          // listenKey → 账户推送
}

const recvMax = 60000

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// apiErr 币安格式的错误：{"code": -2019, "msg": "Margin is insufficient."}
type apiErr struct {
	HTTP int
	Code int
	Msg  string
}

func (e *apiErr) Error() string { return e.Msg }

func bad(code int, msg string) *apiErr { return &apiErr{HTTP: 400, Code: code, Msg: msg} }

func (s *Server) write(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	var e *apiErr
	if !errors.As(err, &e) {
		e = engineErr(err)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.HTTP)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": e.Code, "msg": e.Msg})
}

// engineErr 撮合引擎的拒单原因翻成币安的错误码（程序按错误码处理，所以码要对上）。msg 里带上平台的原因代码。
func engineErr(err error) *apiErr {
	var r *trading.Reject
	if !errors.As(err, &r) {
		return &apiErr{HTTP: 500, Code: -1000, Msg: "An unknown error occurred while processing the request."}
	}
	p := func(k string) string { return r.Params[k] }
	m := map[string]*apiErr{
		"INSUFFICIENT_MARGIN":        bad(-2019, "Margin is insufficient."),
		"MIN_NOTIONAL":               bad(-4164, "Order's notional must be no smaller than "+p("min")+" (unless you choose reduce only)."),
		"QTY_TOO_SMALL":              bad(-4003, "Quantity less than or equal to zero."),
		"BAD_QTY":                    bad(-1111, "Precision is over the maximum defined for this asset."),
		"QTY_REQUIRED":               bad(-1102, "Mandatory parameter 'quantity' was not sent, was empty/null, or malformed."),
		"QTY_TOO_LARGE":              bad(-4005, "Quantity greater than max quantity."),
		"PRICE_TICK":                 bad(-1111, "Precision is over the maximum defined for this asset."),
		"TRIGGER_TICK":               bad(-1111, "Precision is over the maximum defined for this asset."),
		"PRICE_OUT_OF_RANGE":         bad(-4016, "Limit price can't be higher than "+p("max")+" or lower than "+p("min")+"."),
		"REDUCE_ONLY_NO_POSITION":    bad(-2022, "ReduceOnly Order is rejected."),
		"REDUCE_ONLY_SAME_SIDE":      bad(-2022, "ReduceOnly Order is rejected."),
		"NOT_FOUND":                  bad(-2011, "Unknown order sent."),
		"NOT_MODIFIABLE":             bad(-4116, "This order cannot be modified."),
		"TOO_MANY_ORDERS":            bad(-1015, "Too many new orders."),
		"TOO_MANY_CONDITIONAL":       bad(-2025, "Reach max open order limit."),
		"POSITION_LIMIT":             bad(-2027, "Exceeded the maximum allowable position at current leverage."),
		"LEVERAGE_RANGE":             bad(-4028, "Leverage is not valid."),
		"LEVERAGE_TOO_HIGH_FOR_SIZE": bad(-2027, "Exceeded the maximum allowable position at current leverage."),
		"MODE_HAS_POSITION":          bad(-4048, "Margin type cannot be changed if there exists position."),
		"MODE_HAS_ORDERS":            bad(-4047, "Margin type cannot be changed if there exists open orders."),
		"BAD_INSTRUMENT":             bad(-1121, "Invalid symbol."),
		"BAD_SIDE":                   bad(-1117, "Invalid side."),
		"BAD_TYPE":                   bad(-1116, "Invalid orderType."),
		"BAD_TIF":                    bad(-1115, "Invalid timeInForce."),
		"GTD_TOO_SOON":               bad(-5040, "The goodTillDate timestamp must be greater than the current time plus 600 seconds and smaller than 253402300799000."),
		"BAD_PRICE_MATCH":            bad(-1130, "Data sent for parameter 'priceMatch' is not valid."),
		"PRICE_MATCH_WITH_PRICE":     bad(-1106, "Parameter 'price' sent when not required."),
		"WOULD_TRIGGER":              bad(-2021, "Order would immediately trigger."),
		"TRIGGER_AT_PRICE":           bad(-2021, "Order would immediately trigger."),
		"NO_POSITION":                bad(-2022, "ReduceOnly Order is rejected."),
	}
	if e, ok := m[r.Code]; ok {
		return &apiErr{HTTP: e.HTTP, Code: e.Code, Msg: e.Msg + " [" + r.Code + "]"}
	}
	// 平台自己的拦截（全局停机、停用、纪律锁、机器人优先）：币安没有对应的，用"新单被拒"，原因写在 msg 里
	return bad(-2010, "Order rejected by the platform: "+r.Code)
}

// ---------- 请求：参数、签名、密钥 ----------

type req struct {
	q    url.Values
	key  Key
	acct int64
}

// params 查询串和表单体合在一起；签名按币安的规矩：查询串（去掉 signature）直接接上请求体。
func readParams(r *http.Request) (url.Values, string, string, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		return nil, "", "", err
	}
	strip := func(s string) (string, string) {
		var keep []string
		sig := ""
		for _, part := range strings.Split(s, "&") {
			if strings.HasPrefix(part, "signature=") {
				sig = strings.TrimPrefix(part, "signature=")
				continue
			}
			if part != "" {
				keep = append(keep, part)
			}
		}
		return strings.Join(keep, "&"), sig
	}
	qs, sig1 := strip(r.URL.RawQuery)
	bs, sig2 := strip(string(body))
	vals, err := url.ParseQuery(qs)
	if err != nil {
		return nil, "", "", err
	}
	if bs != "" {
		bv, err := url.ParseQuery(bs)
		if err != nil {
			return nil, "", "", err
		}
		for k, v := range bv {
			vals[k] = append(vals[k], v...)
		}
	}
	sig := sig1
	if sig == "" {
		sig = sig2
	}
	return vals, qs + bs, sig, nil
}

// auth 认密钥（keyOnly：只要 X-MBX-APIKEY，比如 listenKey；否则还要签名和时间戳）。
func (s *Server) auth(r *http.Request, keyOnly bool) (*req, error) {
	q, total, sig, err := readParams(r)
	if err != nil {
		return nil, bad(-1100, "Illegal characters found in a parameter.")
	}
	ak := r.Header.Get("X-MBX-APIKEY")
	if ak == "" {
		return nil, &apiErr{HTTP: 401, Code: -2014, Msg: "API-key format invalid."}
	}
	k, ok := s.Keys.Lookup(ak)
	if !ok {
		return nil, &apiErr{HTTP: 401, Code: -2015, Msg: "Invalid API-key, IP, or permissions for action."}
	}
	if s.Disabled != nil && s.Disabled(r.Context(), k.User) {
		return nil, &apiErr{HTTP: 401, Code: -2015, Msg: "Invalid API-key, IP, or permissions for action. [USER_DISABLED]"}
	}
	if !s.E.Has(k.Account) {
		return nil, &apiErr{HTTP: 401, Code: -2015, Msg: "Invalid API-key, IP, or permissions for action. [ACCOUNT_ARCHIVED]"}
	}
	out := &req{q: q, key: k, acct: k.Account}
	if keyOnly {
		return out, nil
	}
	ts, err := strconv.ParseInt(q.Get("timestamp"), 10, 64)
	if err != nil {
		return nil, bad(-1102, "Mandatory parameter 'timestamp' was not sent, was empty/null, or malformed.")
	}
	recv := int64(5000)
	if v := q.Get("recvWindow"); v != "" {
		if recv, err = strconv.ParseInt(v, 10, 64); err != nil || recv <= 0 || recv > recvMax {
			return nil, bad(-1131, "recvWindow must be less than 60000")
		}
	}
	now := s.now().UnixMilli()
	if ts > now+1000 || now-ts > recv {
		return nil, bad(-1021, "Timestamp for this request is outside of the recvWindow.")
	}
	m := hmac.New(sha256.New, []byte(k.Secret))
	m.Write([]byte(total))
	if !hmac.Equal([]byte(hex.EncodeToString(m.Sum(nil))), []byte(strings.ToLower(sig))) {
		return nil, bad(-1022, "Signature for this request is not valid.")
	}
	return out, nil
}

// ---------- 品种和数字 ----------

func instOf(sym string) string {
	if !strings.HasSuffix(sym, "USDT") {
		return ""
	}
	return strings.TrimSuffix(sym, "USDT") + "-PERP"
}

func symOf(inst string) string { return strings.TrimSuffix(inst, "-PERP") + "USDT" }

func (s *Server) symbol(q url.Values, need bool) (string, error) {
	sym := strings.ToUpper(q.Get("symbol"))
	if sym == "" {
		if need {
			return "", bad(-1102, "Mandatory parameter 'symbol' was not sent, was empty/null, or malformed.")
		}
		return "", nil
	}
	inst := instOf(sym)
	if _, ok := s.E.Specs.Get(inst); !ok {
		return "", bad(-1121, "Invalid symbol.")
	}
	return inst, nil
}

func dec(q url.Values, k string) (decimal.Decimal, error) {
	v := q.Get(k)
	if v == "" {
		return decimal.Zero, nil
	}
	d, err := decimal.NewFromString(v)
	if err != nil || d.Sign() < 0 {
		return decimal.Zero, bad(-1102, "Mandatory parameter '"+k+"' was not sent, was empty/null, or malformed.")
	}
	return d, nil
}

func ds(d decimal.Decimal) string { return d.String() }

// orderNum "O90" → 90
func orderNum(id string) int64 {
	n, _ := strconv.ParseInt(strings.TrimPrefix(id, "O"), 10, 64)
	return n
}

// ---------- 委托：引擎的样子 ↔ 币安的样子 ----------

func statusOf(o *trading.Order) string {
	switch o.Status {
	case trading.StNew:
		return "NEW"
	case trading.StPartial:
		return "PARTIALLY_FILLED"
	case trading.StFilled, trading.StTriggered:
		return "FILLED"
	case trading.StCanceled:
		return "CANCELED"
	default:
		return "EXPIRED"
	}
}

func typeOf(o *trading.Order) string {
	switch o.Type {
	case trading.Limit:
		return "LIMIT"
	case trading.Market:
		return "MARKET"
	case trading.StopMarket:
		return "STOP_MARKET"
	case trading.TakeProfit:
		return "TAKE_PROFIT_MARKET"
	case trading.CondMarket:
		if (o.TriggerDir == "up") == (o.Side == "buy") {
			return "STOP_MARKET" // 涨上去买 / 跌下来卖：止损式（突破追单）
		}
		return "TAKE_PROFIT_MARKET"
	case trading.CondLimit:
		if (o.TriggerDir == "up") == (o.Side == "buy") {
			return "STOP"
		}
		return "TAKE_PROFIT"
	}
	return strings.ToUpper(o.Type)
}

func working(o *trading.Order) string {
	if o.TriggerBy == trading.ByLast {
		return "CONTRACT_PRICE"
	}
	return "MARK_PRICE"
}

func (s *Server) cid(acct int64, id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.clients[acct][id]; c != "" {
		return c
	}
	return "qs-" + strings.ToLower(id)
}

func (s *Server) remember(acct int64, id, cid string) {
	if cid == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.clients == nil {
		s.clients, s.byCID = map[int64]map[string]string{}, map[int64]map[string]string{}
	}
	if s.clients[acct] == nil {
		s.clients[acct], s.byCID[acct] = map[string]string{}, map[string]string{}
	}
	s.clients[acct][id], s.byCID[acct][cid] = cid, id
}

func (s *Server) byClient(acct int64, cid string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byCID[acct][cid]
}

func (s *Server) orderJSON(acct int64, o *trading.Order) map[string]any {
	tif := o.TIF
	if tif == "" {
		tif = "GTC"
	}
	cum := o.Filled.Mul(o.AvgPx)
	return map[string]any{
		"orderId": orderNum(o.ID), "symbol": symOf(o.Instrument), "status": statusOf(o), "clientOrderId": s.cid(acct, o.ID),
		"price": ds(o.Price), "avgPrice": ds(o.AvgPx), "origQty": ds(o.Qty), "executedQty": ds(o.Filled), "cumQty": ds(o.Filled), "cumQuote": ds(cum),
		"timeInForce": tif, "type": typeOf(o), "origType": typeOf(o), "reduceOnly": o.ReduceOnly || o.ClosePos, "closePosition": o.ClosePos,
		"side": strings.ToUpper(o.Side), "positionSide": "BOTH", "stopPrice": ds(o.TriggerPx), "workingType": working(o),
		"priceProtect": o.PriceProtect, "priceMatch": map[bool]string{true: o.PriceMatch, false: "NONE"}[o.PriceMatch != ""], "selfTradePreventionMode": "NONE", "goodTillDate": o.GoodTill,
		"time": o.Created, "updateTime": o.Updated,
	}
}

func (s *Server) algoJSON(acct int64, o *trading.Order) map[string]any {
	st := "NEW"
	switch o.Status {
	case trading.StTriggered, trading.StFilled:
		st = "FINISHED"
	case trading.StCanceled:
		st = "CANCELED"
	case trading.StExpired:
		st = "EXPIRED"
	}
	return map[string]any{
		"algoId": orderNum(o.ID), "clientAlgoId": s.cid(acct, o.ID), "algoType": "CONDITIONAL", "orderType": typeOf(o), "symbol": symOf(o.Instrument),
		"side": strings.ToUpper(o.Side), "positionSide": "BOTH", "timeInForce": map[bool]string{true: o.TIF, false: "GTC"}[o.TIF != ""],
		"quantity": ds(o.Qty), "algoStatus": st, "triggerPrice": ds(o.TriggerPx), "price": ds(o.Price), "workingType": working(o),
		"closePosition": o.ClosePos, "reduceOnly": o.ReduceOnly || o.ClosePos, "priceProtect": o.PriceProtect, "actualOrderId": "",
		"createTime": o.Created, "updateTime": o.Updated,
	}
}

func conditional(o *trading.Order) bool {
	switch o.Type {
	case trading.StopMarket, trading.TakeProfit, trading.CondMarket, trading.CondLimit:
		return true
	}
	return false
}

// ---------- 路由 ----------

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/bapi")
	if strings.HasPrefix(path, "/ws/") || strings.HasPrefix(path, "/private/ws/") {
		s.stream(w, r, path[strings.LastIndex(path, "/")+1:])
		return
	}
	key := r.Method + " " + path
	if h, ok := public[key]; ok {
		h(s, w, r)
		return
	}
	if key == "POST /fapi/v1/listenKey" || key == "PUT /fapi/v1/listenKey" || key == "DELETE /fapi/v1/listenKey" {
		rq, err := s.auth(r, true)
		if err != nil {
			s.fail(w, err)
			return
		}
		s.listenKey(w, r.Method, rq)
		return
	}
	h, ok := signed[key]
	if !ok {
		if s.Upstream != "" && r.Method == http.MethodGet && passThrough[path] {
			s.proxy(w, r, path)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(404)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": -5000, "msg": "Path " + path + " is not supported on this simulated venue (see docs/binance-api.md)."})
		return
	}
	rq, err := s.auth(r, false)
	if err != nil {
		s.fail(w, err)
		return
	}
	out, err := h(s, rq)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.write(w, out)
}

// passThrough 这里不仿、直接转给币安的公开行情（只读，不要密钥）
var passThrough = map[string]bool{
	"/fapi/v1/klines": true, "/fapi/v1/markPriceKlines": true, "/fapi/v1/indexPriceKlines": true, "/fapi/v1/continuousKlines": true,
	"/fapi/v1/ticker/24hr": true, "/fapi/v1/fundingRate": true, "/fapi/v1/fundingInfo": true, "/fapi/v1/openInterest": true,
	"/fapi/v1/aggTrades": true, "/fapi/v1/trades": true, "/futures/data/openInterestHist": true,
}

func (s *Server) proxy(w http.ResponseWriter, r *http.Request, path string) {
	c := s.HTTP
	if c == nil {
		c = &http.Client{Timeout: 15 * time.Second}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	up, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.Upstream+path+"?"+r.URL.RawQuery, nil)
	res, err := c.Do(up)
	if err != nil {
		s.fail(w, &apiErr{HTTP: 502, Code: -1001, Msg: "Internal error; unable to process your request. Please try again."})
		return
	}
	defer res.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(res.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(res.Body, 8<<20))
}

type handler func(s *Server, w http.ResponseWriter, r *http.Request)
type signedHandler func(s *Server, rq *req) (any, error)

var public map[string]handler
var signed map[string]signedHandler

func init() {
	public = map[string]handler{
		"GET /fapi/v1/ping": func(s *Server, w http.ResponseWriter, _ *http.Request) { s.write(w, map[string]any{}) },
		"GET /fapi/v1/time": func(s *Server, w http.ResponseWriter, _ *http.Request) {
			s.write(w, map[string]any{"serverTime": s.now().UnixMilli()})
		},
		"GET /fapi/v1/exchangeInfo":      (*Server).exchangeInfo,
		"GET /fapi/v1/ticker/price":      (*Server).tickerPrice,
		"GET /fapi/v2/ticker/price":      (*Server).tickerPrice,
		"GET /fapi/v1/ticker/bookTicker": (*Server).bookTicker,
		"GET /fapi/v1/premiumIndex":      (*Server).premiumIndex,
		"GET /fapi/v1/depth":             (*Server).depth,
	}
	signed = map[string]signedHandler{
		"GET /fapi/v2/account":            func(s *Server, rq *req) (any, error) { return s.account(rq, 2) },
		"GET /fapi/v3/account":            func(s *Server, rq *req) (any, error) { return s.account(rq, 3) },
		"GET /fapi/v2/balance":            (*Server).balance,
		"GET /fapi/v3/balance":            (*Server).balance,
		"GET /fapi/v2/positionRisk":       func(s *Server, rq *req) (any, error) { return s.positionRisk(rq, true) },
		"GET /fapi/v3/positionRisk":       func(s *Server, rq *req) (any, error) { return s.positionRisk(rq, false) },
		"GET /fapi/v1/symbolConfig":       (*Server).symbolConfig,
		"GET /fapi/v1/positionSide/dual":  func(*Server, *req) (any, error) { return map[string]bool{"dualSidePosition": false}, nil },
		"POST /fapi/v1/positionSide/dual": (*Server).setDual,
		"GET /fapi/v1/multiAssetsMargin":  func(*Server, *req) (any, error) { return map[string]bool{"multiAssetsMargin": false}, nil },
		"GET /fapi/v1/commissionRate":     (*Server).commission,
		"POST /fapi/v1/leverage":          (*Server).leverage,
		"POST /fapi/v1/marginType":        (*Server).marginType,
		"POST /fapi/v1/order":             (*Server).newOrder,
		"POST /fapi/v1/order/test":        (*Server).testOrder,
		"GET /fapi/v1/order":              (*Server).getOrder,
		"PUT /fapi/v1/order":              (*Server).amend,
		"DELETE /fapi/v1/order":           (*Server).cancel,
		"DELETE /fapi/v1/allOpenOrders":   func(s *Server, rq *req) (any, error) { return s.cancelAll(rq, false) },
		"GET /fapi/v1/openOrders":         func(s *Server, rq *req) (any, error) { return s.openOrders(rq, false) },
		"GET /fapi/v1/openOrder":          (*Server).getOrder,
		"GET /fapi/v1/allOrders":          (*Server).allOrders,
		"POST /fapi/v1/algoOrder":         (*Server).newAlgo,
		"GET /fapi/v1/algoOrder":          (*Server).getAlgo,
		"DELETE /fapi/v1/algoOrder":       (*Server).cancelAlgo,
		"GET /fapi/v1/openAlgoOrders":     func(s *Server, rq *req) (any, error) { return s.openOrders(rq, true) },
		"DELETE /fapi/v1/algoOpenOrders":  func(s *Server, rq *req) (any, error) { return s.cancelAll(rq, true) },
		"GET /fapi/v1/userTrades":         (*Server).userTrades,
		"GET /fapi/v1/adlQuantile": func(s *Server, rq *req) (any, error) {
			v, err := s.view(rq)
			if err != nil {
				return nil, err
			}
			out := []map[string]any{}
			for _, p := range v.Positions {
				out = append(out, map[string]any{"symbol": symOf(p.Instrument), "adlQuantile": map[string]int{"LONG": 0, "SHORT": 0, "BOTH": p.ADL}})
			}
			return out, nil
		},
		"GET /fapi/v1/income": (*Server).income,
	}
}

// ---------- 公开行情 ----------

func (s *Server) specs() []*trading.Spec {
	all := s.E.Specs.All()
	out := make([]*trading.Spec, 0, len(all))
	for _, sp := range all {
		out = append(out, sp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Instrument < out[j].Instrument })
	return out
}

func places(d decimal.Decimal) int {
	if e := -d.Exponent(); e > 0 {
		return int(e)
	}
	return 0
}

func (s *Server) exchangeInfo(w http.ResponseWriter, _ *http.Request) {
	syms := []map[string]any{}
	for _, sp := range s.specs() {
		maxQty, mktMax := sp.MaxQty, sp.MarketMax
		if maxQty.IsZero() {
			maxQty = decimal.NewFromInt(1000000)
		}
		if mktMax.IsZero() {
			mktMax = maxQty
		}
		maxPx := sp.MaxPrice
		if maxPx.IsZero() {
			maxPx = decimal.NewFromInt(10000000)
		}
		syms = append(syms, map[string]any{
			"symbol": symOf(sp.Instrument), "pair": symOf(sp.Instrument), "contractType": "PERPETUAL", "status": "TRADING",
			"baseAsset": strings.TrimSuffix(sp.Instrument, "-PERP"), "quoteAsset": "USDT", "marginAsset": "USDT", "underlyingType": "COIN",
			"pricePrecision": places(sp.Tick), "quantityPrecision": places(sp.Step), "baseAssetPrecision": 8, "quotePrecision": 8,
			"maintMarginPercent": "2.5000", "requiredMarginPercent": "5.0000", "triggerProtect": ds(sp.TriggerProtect), "liquidationFee": ds(sp.LiqFee), "marketTakeBound": ds(sp.MarketBound),
			"orderTypes": []string{"LIMIT", "MARKET", "STOP", "STOP_MARKET", "TAKE_PROFIT", "TAKE_PROFIT_MARKET"}, "timeInForce": []string{"GTC", "IOC", "FOK", "GTX", "GTD"},
			"filters": []map[string]any{
				{"filterType": "PRICE_FILTER", "tickSize": ds(sp.Tick), "minPrice": ds(sp.MinPrice), "maxPrice": ds(maxPx)},
				{"filterType": "LOT_SIZE", "stepSize": ds(sp.Step), "minQty": ds(sp.MinQty), "maxQty": ds(maxQty)},
				{"filterType": "MARKET_LOT_SIZE", "stepSize": ds(sp.Step), "minQty": ds(sp.MinQty), "maxQty": ds(mktMax)},
				{"filterType": "MAX_NUM_ORDERS", "limit": sp.MaxOrders},
				{"filterType": "MIN_NOTIONAL", "notional": ds(sp.MinNotional)},
				{"filterType": "PERCENT_PRICE", "multiplierUp": ds(sp.PriceUp), "multiplierDown": ds(sp.PriceDown), "multiplierDecimal": "4"},
			},
		})
	}
	s.write(w, map[string]any{"timezone": "UTC", "serverTime": s.now().UnixMilli(), "futuresType": "U_MARGINED",
		"rateLimits":      []map[string]any{{"rateLimitType": "ORDERS", "interval": "MINUTE", "intervalNum": 1, "limit": 1200}, {"rateLimitType": "ORDERS", "interval": "SECOND", "intervalNum": 10, "limit": 300}},
		"exchangeFilters": []any{}, "assets": []map[string]any{{"asset": "USDT", "marginAvailable": true}}, "symbols": syms})
}

func (s *Server) tickerOf(inst string) (book.Ticker, bool) { return s.Books.Ticker(s.Venue, inst) }

// eachSymbol symbol 给了就一个，没给就全部（币安：没给 symbol 时回数组）
func (s *Server) eachSymbol(w http.ResponseWriter, r *http.Request, row func(inst string) (map[string]any, bool)) {
	q := r.URL.Query()
	if q.Get("symbol") != "" {
		inst, err := s.symbol(q, true)
		if err != nil {
			s.fail(w, err)
			return
		}
		v, ok := row(inst)
		if !ok {
			s.fail(w, bad(-1121, "Invalid symbol."))
			return
		}
		s.write(w, v)
		return
	}
	out := []map[string]any{}
	for _, sp := range s.specs() {
		if v, ok := row(sp.Instrument); ok {
			out = append(out, v)
		}
	}
	s.write(w, out)
}

func (s *Server) tickerPrice(w http.ResponseWriter, r *http.Request) {
	s.eachSymbol(w, r, func(inst string) (map[string]any, bool) {
		t, ok := s.tickerOf(inst)
		return map[string]any{"symbol": symOf(inst), "price": ds(t.Last), "time": t.Time}, ok && t.Last.Sign() > 0
	})
}

func (s *Server) premiumIndex(w http.ResponseWriter, r *http.Request) {
	s.eachSymbol(w, r, func(inst string) (map[string]any, bool) {
		t, ok := s.tickerOf(inst)
		idx := t.Index
		if idx.IsZero() {
			idx = t.Mark
		}
		return map[string]any{"symbol": symOf(inst), "markPrice": ds(t.Mark), "indexPrice": ds(idx), "estimatedSettlePrice": ds(t.Mark),
			"lastFundingRate": ds(t.FundingRate), "interestRate": "0.00010000", "nextFundingTime": t.NextFunding, "time": t.Time}, ok && t.Mark.Sign() > 0
	})
}

func (s *Server) bookTicker(w http.ResponseWriter, r *http.Request) {
	s.eachSymbol(w, r, func(inst string) (map[string]any, bool) {
		b, ok := s.Books.Book(s.Venue, inst)
		if !ok || len(b.Bids) == 0 || len(b.Asks) == 0 {
			return nil, false
		}
		return map[string]any{"symbol": symOf(inst), "bidPrice": ds(b.Bids[0].Px), "bidQty": ds(b.Bids[0].Qty), "askPrice": ds(b.Asks[0].Px), "askQty": ds(b.Asks[0].Qty), "time": s.now().UnixMilli()}, true
	})
}

func (s *Server) depth(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	inst, err := s.symbol(q, true)
	if err != nil {
		s.fail(w, err)
		return
	}
	n, _ := strconv.Atoi(q.Get("limit"))
	if n <= 0 || n > 1000 {
		n = 500
	}
	b, _ := s.Books.Book(s.Venue, inst)
	side := func(lv []book.Level) [][2]string {
		out := [][2]string{}
		for i, l := range lv {
			if i >= n {
				break
			}
			out = append(out, [2]string{ds(l.Px), ds(l.Qty)})
		}
		return out
	}
	now := s.now().UnixMilli()
	s.write(w, map[string]any{"lastUpdateId": now, "E": now, "T": now, "bids": side(b.Bids), "asks": side(b.Asks)})
}

// ---------- 账户 ----------

func (s *Server) view(rq *req) (*trading.AccountView, error) { return s.E.View(rq.acct) }

func posRow(p trading.PosView, full bool) map[string]any {
	amt := p.Qty
	if p.Side == "short" {
		amt = amt.Neg()
	}
	notional := p.Notional
	if p.Side == "short" {
		notional = notional.Neg()
	}
	iso := decimal.Zero
	marginType := "cross"
	if p.Mode == "isolated" {
		iso, marginType = p.Margin, "isolated"
	}
	row := map[string]any{"symbol": symOf(p.Instrument), "positionSide": "BOTH", "positionAmt": ds(amt), "entryPrice": ds(p.Entry), "breakEvenPrice": ds(p.Entry),
		"markPrice": ds(p.Mark), "unRealizedProfit": ds(p.UPnL), "liquidationPrice": ds(p.LiqPx), "isolatedMargin": ds(iso), "notional": ds(notional),
		"isolatedWallet": ds(iso), "initialMargin": ds(p.Margin), "maintMargin": ds(p.Maint), "marginAsset": "USDT", "adl": p.ADL, "updateTime": p.Opened}
	if full {
		row["leverage"] = strconv.Itoa(p.Leverage)
		row["marginType"] = marginType
		row["isAutoAddMargin"] = "false"
		row["maxNotionalValue"] = "0"
	}
	return row
}

func (s *Server) account(rq *req, ver int) (any, error) {
	v, err := s.view(rq)
	if err != nil {
		return nil, err
	}
	now := s.now().UnixMilli()
	pos := []map[string]any{}
	for _, p := range v.Positions {
		pos = append(pos, posRow(p, ver == 2))
	}
	asset := map[string]any{"asset": "USDT", "walletBalance": ds(v.Wallet), "unrealizedProfit": ds(v.UPnL), "marginBalance": ds(v.Equity),
		"maintMargin": ds(v.MaintMargin), "initialMargin": ds(v.PositionMargin.Add(v.OrderMargin)), "positionInitialMargin": ds(v.PositionMargin),
		"openOrderInitialMargin": ds(v.OrderMargin), "crossWalletBalance": ds(v.Wallet), "crossUnPnl": ds(v.UPnL), "availableBalance": ds(v.Available),
		"maxWithdrawAmount": ds(v.Transferable), "marginAvailable": true, "updateTime": now}
	return map[string]any{"feeTier": 0, "canTrade": true, "canDeposit": false, "canWithdraw": false, "feeBurn": false, "multiAssetsMargin": false, "tradeGroupId": -1, "updateTime": now,
		"totalInitialMargin": ds(v.PositionMargin.Add(v.OrderMargin)), "totalMaintMargin": ds(v.MaintMargin), "totalWalletBalance": ds(v.Wallet),
		"totalUnrealizedProfit": ds(v.UPnL), "totalMarginBalance": ds(v.Equity), "totalPositionInitialMargin": ds(v.PositionMargin),
		"totalOpenOrderInitialMargin": ds(v.OrderMargin), "totalCrossWalletBalance": ds(v.Wallet), "totalCrossUnPnl": ds(v.UPnL),
		"availableBalance": ds(v.Available), "maxWithdrawAmount": ds(v.Transferable), "assets": []any{asset}, "positions": pos}, nil
}

func (s *Server) balance(rq *req) (any, error) {
	v, err := s.view(rq)
	if err != nil {
		return nil, err
	}
	return []map[string]any{{"accountAlias": "qs", "asset": "USDT", "balance": ds(v.Wallet), "crossWalletBalance": ds(v.Wallet), "crossUnPnl": ds(v.UPnL),
		"availableBalance": ds(v.Available), "maxWithdrawAmount": ds(v.Transferable), "marginAvailable": true, "updateTime": s.now().UnixMilli()}}, nil
}

func (s *Server) positionRisk(rq *req, full bool) (any, error) {
	v, err := s.view(rq)
	if err != nil {
		return nil, err
	}
	inst, err := s.symbol(rq.q, false)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, p := range v.Positions {
		if inst == "" || p.Instrument == inst {
			out = append(out, posRow(p, full))
		}
	}
	return out, nil
}

func (s *Server) symbolConfig(rq *req) (any, error) {
	v, err := s.view(rq)
	if err != nil {
		return nil, err
	}
	inst, err := s.symbol(rq.q, false)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, sp := range s.specs() {
		if inst != "" && sp.Instrument != inst {
			continue
		}
		lev, mode := 10, "CROSSED"
		if st := v.Settings[sp.Instrument]; st != nil {
			lev = st.Leverage
			if st.Mode == "isolated" {
				mode = "ISOLATED"
			}
		}
		out = append(out, map[string]any{"symbol": symOf(sp.Instrument), "marginType": mode, "isAutoAddMargin": false, "leverage": lev, "maxNotionalValue": "0"})
	}
	return out, nil
}

func (s *Server) setDual(rq *req) (any, error) {
	if rq.q.Get("dualSidePosition") == "true" {
		return nil, bad(-4068, "This simulated venue supports one-way position mode only.")
	}
	return nil, bad(-4059, "No need to change position side.")
}

func (s *Server) commission(rq *req) (any, error) {
	inst, err := s.symbol(rq.q, true)
	if err != nil {
		return nil, err
	}
	taker, maker, _ := s.E.FeesOf(rq.acct) // 这个模拟账户现在的费率（可以自己设，D103）
	return map[string]any{"symbol": symOf(inst), "makerCommissionRate": maker.StringFixed(4), "takerCommissionRate": taker.StringFixed(4)}, nil
}

func (s *Server) leverage(rq *req) (any, error) {
	inst, err := s.symbol(rq.q, true)
	if err != nil {
		return nil, err
	}
	l, err := strconv.Atoi(rq.q.Get("leverage"))
	if err != nil || l < 1 || l > 125 {
		return nil, bad(-4028, "Leverage is not valid.")
	}
	if err := s.E.SetLeverage(rq.acct, inst, l); err != nil {
		return nil, err
	}
	return map[string]any{"leverage": l, "maxNotionalValue": "0", "symbol": symOf(inst)}, nil
}

func (s *Server) marginType(rq *req) (any, error) {
	inst, err := s.symbol(rq.q, true)
	if err != nil {
		return nil, err
	}
	mode := map[string]string{"ISOLATED": "isolated", "CROSSED": "cross"}[strings.ToUpper(rq.q.Get("marginType"))]
	if mode == "" {
		return nil, bad(-1102, "Mandatory parameter 'marginType' was not sent, was empty/null, or malformed.")
	}
	if v, err := s.view(rq); err == nil {
		if st := v.Settings[inst]; (st == nil && mode == "cross") || (st != nil && st.Mode == mode) {
			return nil, bad(-4046, "No need to change margin type.")
		}
	}
	if err := s.E.SetMode(rq.acct, inst, mode); err != nil {
		return nil, err
	}
	return map[string]any{"code": 200, "msg": "success"}, nil
}

// ---------- 下单 ----------

// buildOrder 币安的参数 → 引擎的委托。closePosition 的止盈止损挂到仓位上（返回 closeTP/closeSL）。
func (s *Server) buildOrder(rq *req, typ string) (r trading.OrderReq, closeKind string, err error) {
	q := rq.q
	if r.Instrument, err = s.symbol(q, true); err != nil {
		return
	}
	switch strings.ToUpper(q.Get("side")) {
	case "BUY":
		r.Side = "buy"
	case "SELL":
		r.Side = "sell"
	default:
		err = bad(-1117, "Invalid side.")
		return
	}
	if q.Get("positionSide") != "" && strings.ToUpper(q.Get("positionSide")) != "BOTH" {
		err = bad(-4061, "Order's position side does not match user's setting.")
		return
	}
	if r.Qty, err = dec(q, "quantity"); err != nil {
		return
	}
	if r.Price, err = dec(q, "price"); err != nil {
		return
	}
	stop := q.Get("stopPrice")
	if stop == "" {
		stop = q.Get("triggerPrice") // algoOrder 用 triggerPrice
	}
	if stop != "" {
		if r.TriggerPx, err = decimal.NewFromString(stop); err != nil {
			err = bad(-1102, "Mandatory parameter 'stopPrice' was not sent, was empty/null, or malformed.")
			return
		}
	}
	r.TriggerBy = trading.ByMark
	if strings.ToUpper(q.Get("workingType")) == "CONTRACT_PRICE" {
		r.TriggerBy = trading.ByLast
	}
	r.ReduceOnly = q.Get("reduceOnly") == "true"
	closePos := q.Get("closePosition") == "true"
	tif := strings.ToUpper(q.Get("timeInForce"))
	// 币安的另外几条（D103）：GTD 的有效期、按盘口定价、条件单价格保护
	if tif == trading.GTD {
		r.GoodTill, _ = strconv.ParseInt(q.Get("goodTillDate"), 10, 64)
	}
	if pm := strings.ToUpper(q.Get("priceMatch")); pm != "" && pm != "NONE" {
		r.PriceMatch = pm
	}
	r.PriceProtect = strings.ToUpper(q.Get("priceProtect")) == "TRUE"
	switch strings.ToUpper(typ) {
	case "LIMIT":
		r.Type = trading.Limit
		if tif == "" {
			err = bad(-1102, "Mandatory parameter 'timeInForce' was not sent, was empty/null, or malformed.")
			return
		}
		r.TIF = tif
		if r.Price.Sign() <= 0 && r.PriceMatch == "" { // 按盘口定价时不填价格
			err = bad(-1102, "Mandatory parameter 'price' was not sent, was empty/null, or malformed.")
		}
	case "MARKET":
		r.Type = trading.Market
	case "STOP_MARKET", "TAKE_PROFIT_MARKET":
		if r.TriggerPx.Sign() <= 0 {
			err = bad(-1102, "Mandatory parameter 'stopPrice' was not sent, was empty/null, or malformed.")
			return
		}
		if closePos {
			closeKind = map[string]string{"STOP_MARKET": "sl", "TAKE_PROFIT_MARKET": "tp"}[strings.ToUpper(typ)]
			return
		}
		r.Type = trading.CondMarket
	case "STOP", "TAKE_PROFIT":
		if r.TriggerPx.Sign() <= 0 || r.Price.Sign() <= 0 {
			err = bad(-1102, "Mandatory parameter 'stopPrice' or 'price' was not sent, was empty/null, or malformed.")
			return
		}
		r.Type, r.TIF = trading.CondLimit, tif
	default:
		err = bad(-1116, "Invalid orderType.")
	}
	return
}

// place 下单（普通和条件单共用）。返回引擎里的委托。
func (s *Server) place(rq *req, typ string) (*trading.Order, error) {
	cid := rq.q.Get("newClientOrderId")
	if cid == "" {
		cid = rq.q.Get("clientAlgoId")
	}
	if cid != "" && s.byClient(rq.acct, cid) != "" {
		if o := s.E.OrderOf(rq.acct, s.byClient(rq.acct, cid)); o != nil && (o.Status == trading.StNew || o.Status == trading.StPartial) {
			return nil, bad(-4015, "Client order id is not valid.") // 币安：挂着的委托不能重复用同一个客户端编号
		}
	}
	r, closeKind, err := s.buildOrder(rq, typ)
	if err != nil {
		return nil, err
	}
	if closeKind != "" {
		// 整仓的止盈止损：挂到仓位上（和界面上的止盈止损一样，一个仓位一张止盈、一张止损）
		px := r.TriggerPx
		var tp, sl *decimal.Decimal
		if closeKind == "tp" {
			tp = &px
		} else {
			sl = &px
		}
		if err := s.E.SetTPSL(rq.acct, r.Instrument, tp, sl, r.TriggerBy); err != nil {
			return nil, err
		}
		v, err := s.view(rq)
		if err != nil {
			return nil, err
		}
		want := map[string]string{"tp": trading.TakeProfit, "sl": trading.StopMarket}[closeKind]
		for _, o := range v.Orders {
			if o.Instrument == r.Instrument && o.Type == want && o.ClosePos {
				s.remember(rq.acct, o.ID, cid)
				return o, nil
			}
		}
		return nil, bad(-2022, "ReduceOnly Order is rejected. [NO_POSITION]")
	}
	r.SetSource("api")
	o, err := s.E.Place(rq.acct, r)
	if err != nil {
		return nil, err
	}
	s.remember(rq.acct, o.ID, cid)
	return o, nil
}

func (s *Server) newOrder(rq *req) (any, error) {
	o, err := s.place(rq, rq.q.Get("type"))
	if err != nil {
		return nil, err
	}
	if fresh := s.E.OrderOf(rq.acct, o.ID); fresh != nil { // 市价单这时已经成交完：回最新的状态
		o = fresh
	}
	return s.orderJSON(rq.acct, o), nil
}

// testOrder 只校验参数和签名，不下单（币安的 /order/test）
func (s *Server) testOrder(rq *req) (any, error) {
	if _, _, err := s.buildOrder(rq, rq.q.Get("type")); err != nil {
		return nil, err
	}
	return map[string]any{}, nil
}

// find 按 orderId / origClientOrderId（条件单是 algoId / clientAlgoId）找委托
func (s *Server) find(rq *req, idKey, cidKey string, notFound *apiErr) (*trading.Order, error) {
	id := ""
	if v := rq.q.Get(idKey); v != "" {
		id = "O" + v
	} else if v := rq.q.Get(cidKey); v != "" {
		id = s.byClient(rq.acct, v)
	} else {
		return nil, bad(-1102, "Either '"+idKey+"' or '"+cidKey+"' must be sent.")
	}
	o := s.E.OrderOf(rq.acct, id)
	if id == "" || o == nil {
		return nil, notFound
	}
	if inst, _ := s.symbol(rq.q, false); inst != "" && inst != o.Instrument {
		return nil, notFound
	}
	return o, nil
}

func (s *Server) getOrder(rq *req) (any, error) {
	o, err := s.find(rq, "orderId", "origClientOrderId", bad(-2013, "Order does not exist."))
	if err != nil {
		return nil, err
	}
	return s.orderJSON(rq.acct, o), nil
}

func (s *Server) cancel(rq *req) (any, error) {
	o, err := s.find(rq, "orderId", "origClientOrderId", bad(-2011, "Unknown order sent."))
	if err != nil {
		return nil, err
	}
	c, err := s.E.Cancel(rq.acct, o.ID)
	if err != nil {
		return nil, err
	}
	return s.orderJSON(rq.acct, c), nil
}

func (s *Server) amend(rq *req) (any, error) {
	o, err := s.find(rq, "orderId", "origClientOrderId", bad(-2013, "Order does not exist."))
	if err != nil {
		return nil, err
	}
	px, err := dec(rq.q, "price")
	if err != nil {
		return nil, err
	}
	qty, err := dec(rq.q, "quantity")
	if err != nil {
		return nil, err
	}
	c, err := s.E.Amend(rq.acct, o.ID, px, qty, decimal.Zero)
	if err != nil {
		return nil, err
	}
	return s.orderJSON(rq.acct, c), nil
}

func (s *Server) cancelAll(rq *req, algo bool) (any, error) {
	inst, err := s.symbol(rq.q, true)
	if err != nil {
		return nil, err
	}
	v, err := s.view(rq)
	if err != nil {
		return nil, err
	}
	for _, o := range v.Orders {
		if o.Instrument == inst && conditional(o) == algo {
			_, _ = s.E.Cancel(rq.acct, o.ID)
		}
	}
	return map[string]any{"code": 200, "msg": "The operation of cancel all open order is done."}, nil
}

func (s *Server) openOrders(rq *req, algo bool) (any, error) {
	v, err := s.view(rq)
	if err != nil {
		return nil, err
	}
	inst, err := s.symbol(rq.q, false)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, o := range v.Orders {
		if (inst == "" || o.Instrument == inst) && conditional(o) == algo {
			if algo {
				out = append(out, s.algoJSON(rq.acct, o))
			} else {
				out = append(out, s.orderJSON(rq.acct, o))
			}
		}
	}
	return out, nil
}

func (s *Server) allOrders(rq *req) (any, error) {
	inst, err := s.symbol(rq.q, true)
	if err != nil {
		return nil, err
	}
	limit, _ := strconv.Atoi(rq.q.Get("limit"))
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	pg, err := s.E.History(rq.acct, "orders", 1, 200)
	if err != nil {
		return nil, err
	}
	var list []*trading.Order
	if l, ok := pg.Items.([]*trading.Order); ok {
		list = l
	}
	if v, err := s.view(rq); err == nil {
		list = append(list, v.Orders...)
	}
	sort.Slice(list, func(i, j int) bool { return orderNum(list[i].ID) < orderNum(list[j].ID) })
	out := []map[string]any{}
	seen := map[string]bool{}
	for _, o := range list {
		if o.Instrument == inst && !seen[o.ID] {
			seen[o.ID] = true
			out = append(out, s.orderJSON(rq.acct, o))
		}
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

// ---------- 条件单（币安 2025-12 起走 Algo 接口） ----------

func (s *Server) newAlgo(rq *req) (any, error) {
	if t := strings.ToUpper(rq.q.Get("algoType")); t != "" && t != "CONDITIONAL" {
		return nil, bad(-1116, "Invalid algoType.")
	}
	typ := strings.ToUpper(rq.q.Get("type"))
	if typ != "STOP_MARKET" && typ != "TAKE_PROFIT_MARKET" && typ != "STOP" && typ != "TAKE_PROFIT" {
		return nil, bad(-1116, "Invalid orderType. (TRAILING_STOP_MARKET is not simulated here)")
	}
	o, err := s.place(rq, typ)
	if err != nil {
		return nil, err
	}
	return s.algoJSON(rq.acct, o), nil
}

func (s *Server) getAlgo(rq *req) (any, error) {
	o, err := s.find(rq, "algoId", "clientAlgoId", bad(-2013, "Order does not exist."))
	if err != nil {
		return nil, err
	}
	return s.algoJSON(rq.acct, o), nil
}

func (s *Server) cancelAlgo(rq *req) (any, error) {
	o, err := s.find(rq, "algoId", "clientAlgoId", bad(-2011, "Unknown order sent."))
	if err != nil {
		return nil, err
	}
	c, err := s.E.Cancel(rq.acct, o.ID)
	if err != nil {
		return nil, err
	}
	return s.algoJSON(rq.acct, c), nil
}

// ---------- 成交和资金流水 ----------

func (s *Server) userTrades(rq *req) (any, error) {
	inst, err := s.symbol(rq.q, true)
	if err != nil {
		return nil, err
	}
	fills, _, _, err := s.E.Records(rq.acct)
	if err != nil {
		return nil, err
	}
	from, _ := strconv.ParseInt(rq.q.Get("fromId"), 10, 64)
	start, _ := strconv.ParseInt(rq.q.Get("startTime"), 10, 64)
	end, _ := strconv.ParseInt(rq.q.Get("endTime"), 10, 64)
	oid, _ := strconv.ParseInt(rq.q.Get("orderId"), 10, 64)
	limit, _ := strconv.Atoi(rq.q.Get("limit"))
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	out := []map[string]any{}
	for _, f := range fills {
		if f.Instrument != inst || f.Seq < from || (start > 0 && f.Time < start) || (end > 0 && f.Time > end) || (oid > 0 && orderNum(f.OrderID) != oid) {
			continue
		}
		out = append(out, map[string]any{"id": f.Seq, "orderId": orderNum(f.OrderID), "symbol": symOf(f.Instrument), "side": strings.ToUpper(f.Side),
			"price": ds(f.Px), "qty": ds(f.Qty), "realizedPnl": ds(f.Realized), "quoteQty": ds(f.Px.Mul(f.Qty)), "commission": ds(f.Fee),
			"commissionAsset": "USDT", "time": f.Time, "buyer": f.Side == "buy", "maker": f.Maker, "positionSide": "BOTH"})
	}
	if from <= 0 && len(out) > limit { // 不给 fromId：最近的 limit 条
		out = out[len(out)-limit:]
	} else if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *Server) income(rq *req) (any, error) {
	fills, fundings, _, err := s.E.Records(rq.acct)
	if err != nil {
		return nil, err
	}
	inst, err := s.symbol(rq.q, false)
	if err != nil {
		return nil, err
	}
	kind := strings.ToUpper(rq.q.Get("incomeType"))
	start, _ := strconv.ParseInt(rq.q.Get("startTime"), 10, 64)
	end, _ := strconv.ParseInt(rq.q.Get("endTime"), 10, 64)
	limit, _ := strconv.Atoi(rq.q.Get("limit"))
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	type row struct {
		t int64
		m map[string]any
	}
	var rows []row
	add := func(t int64, typ, in string, amt decimal.Decimal, id int64) {
		if (kind != "" && kind != typ) || (inst != "" && in != inst) || (start > 0 && t < start) || (end > 0 && t > end) || amt.IsZero() {
			return
		}
		rows = append(rows, row{t, map[string]any{"symbol": symOf(in), "incomeType": typ, "income": ds(amt), "asset": "USDT", "info": "", "time": t,
			"tranId": id, "tradeId": ""}})
	}
	for _, f := range fills {
		add(f.Time, "REALIZED_PNL", f.Instrument, f.Realized, f.Seq)
		add(f.Time, "COMMISSION", f.Instrument, f.Fee.Neg(), f.Seq)
	}
	for _, f := range fundings {
		add(f.Time, "FUNDING_FEE", f.Instrument, f.Amount, f.Seq)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].t < rows[j].t })
	out := []map[string]any{}
	for _, r := range rows {
		out = append(out, r.m)
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}
