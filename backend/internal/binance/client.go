// Package binance 币安 U 本位合约（USDⓈ-M）的 REST 客户端：签名、对时、错误码、用到的接口（D89：用户接自己的账号做实盘）。
//
// 只做下单、撤单、改单、查询和读权限。代码里没有任何提现、转账接口（D90 第 3 条）。
// 条件单（止损、止盈）从 2025-12-09 起只能走 Algo 接口（/fapi/v1/algoOrder，参数叫 triggerPrice），老接口会报 -4120。
// 接口和字段按币安官方文档（developers.binance.com，2026-11 读取）。测试用 fake_test.go 里的假服务器。
package binance

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// 正式环境的地址。测试网、模拟交易、假服务器在 Client 里改。
const (
	MainFAPI  = "https://fapi.binance.com"
	MainSAPI  = "https://api.binance.com" // 读 API 权限（现货那边的接口）
	MainWS    = "wss://fstream.binance.com"
	RecvDflt  = 5000
	userAgent = "qs-open/1"
)

// APIError 币安返回的错误：HTTP 状态和 {"code":-2019,"msg":"Margin is insufficient."}。
type APIError struct {
	Status int
	Code   int
	Msg    string
}

func (e *APIError) Error() string { return fmt.Sprintf("binance %d: %d %s", e.Status, e.Code, e.Msg) }

// Code 取出币安的错误码（不是 APIError 时返回 0）。
func Code(err error) int {
	var e *APIError
	if errors.As(err, &e) {
		return e.Code
	}
	return 0
}

// Client 一把密钥一个客户端。并发安全。
type Client struct {
	FAPI   string // 合约 REST
	SAPI   string // 现货 REST（只用来读密钥权限）
	WS     string // 用户数据流
	Key    string
	Secret string
	HTTP   *http.Client
	Recv   int64
	Now    func() time.Time

	mu     sync.Mutex
	offset int64 // 交易所时间 − 本机时间（毫秒）
	synced bool
}

func (c *Client) now() int64 {
	if c.Now != nil {
		return c.Now().UnixMilli()
	}
	return time.Now().UnixMilli()
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 15 * time.Second}
}

// SyncTime 和交易所对时：签名请求带的时间戳差太多会被拒（-1021）。
func (c *Client) SyncTime(ctx context.Context) error {
	var r struct {
		ServerTime int64 `json:"serverTime"`
	}
	t0 := c.now()
	if err := c.do(ctx, http.MethodGet, c.FAPI, "/fapi/v1/time", nil, false, false, &r); err != nil {
		return err
	}
	t1 := c.now()
	c.mu.Lock()
	c.offset = r.ServerTime - (t0+t1)/2
	c.synced = true
	c.mu.Unlock()
	return nil
}

// sign 给查询串签名。签名对"实际发出去的查询串"算，signature 原样接在最后。
func (c *Client) sign(qs string) string {
	m := hmac.New(sha256.New, []byte(c.Secret))
	m.Write([]byte(qs))
	return hex.EncodeToString(m.Sum(nil))
}

// do 发一个请求。signed：要签名（USER_DATA、TRADE）；keyOnly：只带密钥头不签名（listenKey）。
func (c *Client) do(ctx context.Context, method, base, path string, q url.Values, signed, keyOnly bool, out any) error {
	try := func() error {
		if q == nil {
			q = url.Values{}
		}
		if signed {
			c.mu.Lock()
			off := c.offset
			c.mu.Unlock()
			q.Set("timestamp", strconv.FormatInt(c.now()+off, 10))
			recv := c.Recv
			if recv <= 0 {
				recv = RecvDflt
			}
			q.Set("recvWindow", strconv.FormatInt(recv, 10))
		}
		qs := q.Encode()
		if signed {
			if qs != "" {
				qs += "&"
			}
			qs += "signature=" + c.sign(q.Encode())
		}
		u := base + path
		if qs != "" {
			u += "?" + qs
		}
		req, err := http.NewRequestWithContext(ctx, method, u, nil)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", userAgent)
		if signed || keyOnly {
			req.Header.Set("X-MBX-APIKEY", c.Key)
		}
		res, err := c.http().Do(req)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		body, err := io.ReadAll(io.LimitReader(res.Body, 16<<20))
		if err != nil {
			return err
		}
		if res.StatusCode >= 300 {
			e := &APIError{Status: res.StatusCode}
			_ = json.Unmarshal(body, e)
			if e.Msg == "" {
				e.Msg = string(body)
			}
			return e
		}
		if out == nil {
			return nil
		}
		return json.Unmarshal(body, out)
	}
	if signed {
		c.mu.Lock()
		ok := c.synced
		c.mu.Unlock()
		if !ok {
			_ = c.SyncTime(ctx)
		}
	}
	err := try()
	if signed && Code(err) == -1021 { // 时间戳超出窗口：对一次时，再试一次
		if c.SyncTime(ctx) == nil {
			err = try()
		}
	}
	return err
}

// ---------- 读 ----------

// Num 币安的数字都是字符串。
type Num string

// F 转成 float64（读不出来是 0）。
func (n Num) F() float64 {
	v, _ := strconv.ParseFloat(string(n), 64)
	return v
}

// Account GET /fapi/v3/account 的一部分。
type Account struct {
	TotalWalletBalance          Num `json:"totalWalletBalance"`
	TotalUnrealizedProfit       Num `json:"totalUnrealizedProfit"`
	TotalMarginBalance          Num `json:"totalMarginBalance"`
	TotalPositionInitialMargin  Num `json:"totalPositionInitialMargin"`
	TotalOpenOrderInitialMargin Num `json:"totalOpenOrderInitialMargin"`
	TotalMaintMargin            Num `json:"totalMaintMargin"`
	AvailableBalance            Num `json:"availableBalance"`
	MaxWithdrawAmount           Num `json:"maxWithdrawAmount"`
	Assets                      []struct {
		Asset            string `json:"asset"`
		WalletBalance    Num    `json:"walletBalance"`
		UnrealizedProfit Num    `json:"unrealizedProfit"`
		MarginBalance    Num    `json:"marginBalance"`
		AvailableBalance Num    `json:"availableBalance"`
	} `json:"assets"`
}

func (c *Client) Account(ctx context.Context) (*Account, error) {
	var a Account
	return &a, c.do(ctx, http.MethodGet, c.FAPI, "/fapi/v3/account", nil, true, false, &a)
}

// Position GET /fapi/v3/positionRisk 的一行。
type Position struct {
	Symbol           string `json:"symbol"`
	PositionSide     string `json:"positionSide"`
	PositionAmt      Num    `json:"positionAmt"`
	EntryPrice       Num    `json:"entryPrice"`
	BreakEvenPrice   Num    `json:"breakEvenPrice"`
	MarkPrice        Num    `json:"markPrice"`
	UnRealizedProfit Num    `json:"unRealizedProfit"`
	LiquidationPrice Num    `json:"liquidationPrice"`
	IsolatedMargin   Num    `json:"isolatedMargin"`
	Notional         Num    `json:"notional"`
	IsolatedWallet   Num    `json:"isolatedWallet"`
	InitialMargin    Num    `json:"initialMargin"`
	MaintMargin      Num    `json:"maintMargin"`
	UpdateTime       int64  `json:"updateTime"`
}

func (c *Client) Positions(ctx context.Context) ([]Position, error) {
	var p []Position
	return p, c.do(ctx, http.MethodGet, c.FAPI, "/fapi/v3/positionRisk", nil, true, false, &p)
}

// SymbolConfig GET /fapi/v1/symbolConfig：每个品种的杠杆和保证金模式。
type SymbolConfig struct {
	Symbol           string `json:"symbol"`
	MarginType       string `json:"marginType"` // CROSSED | ISOLATED
	IsAutoAddMargin  bool   `json:"isAutoAddMargin"`
	Leverage         int    `json:"leverage"`
	MaxNotionalValue string `json:"maxNotionalValue"`
}

func (c *Client) SymbolConfigs(ctx context.Context) ([]SymbolConfig, error) {
	var s []SymbolConfig
	return s, c.do(ctx, http.MethodGet, c.FAPI, "/fapi/v1/symbolConfig", nil, true, false, &s)
}

// Commission 这个账户在一个品种上的手续费率（VIP 等级、返佣都算进去了）。
type Commission struct {
	Symbol string `json:"symbol"`
	Maker  Num    `json:"makerCommissionRate"`
	Taker  Num    `json:"takerCommissionRate"`
}

func (c *Client) CommissionRate(ctx context.Context, symbol string) (*Commission, error) {
	var r Commission
	return &r, c.do(ctx, http.MethodGet, c.FAPI, "/fapi/v1/commissionRate", url.Values{"symbol": {symbol}}, true, false, &r)
}

// DualSide 账户是不是双向持仓模式。系统只支持单向（D83 第 8 条），双向的账户要先在币安改成单向。
func (c *Client) DualSide(ctx context.Context) (bool, error) {
	var r struct {
		DualSidePosition bool `json:"dualSidePosition"`
	}
	err := c.do(ctx, http.MethodGet, c.FAPI, "/fapi/v1/positionSide/dual", nil, true, false, &r)
	return r.DualSidePosition, err
}

// Order 普通委托（/fapi/v1/order、openOrders）。
type Order struct {
	OrderID       int64  `json:"orderId"`
	ClientOrderID string `json:"clientOrderId"`
	Symbol        string `json:"symbol"`
	Side          string `json:"side"`
	Type          string `json:"type"`
	TimeInForce   string `json:"timeInForce"`
	Price         Num    `json:"price"`
	OrigQty       Num    `json:"origQty"`
	ExecutedQty   Num    `json:"executedQty"`
	AvgPrice      Num    `json:"avgPrice"`
	CumQuote      Num    `json:"cumQuote"` // 成交总额：avgPrice 还是 0 时用它 ÷ 成交量算均价
	Status        string `json:"status"`
	ReduceOnly    bool   `json:"reduceOnly"`
	ClosePosition bool   `json:"closePosition"`
	StopPrice     Num    `json:"stopPrice"`
	WorkingType   string `json:"workingType"`
	PositionSide  string `json:"positionSide"`
	Time          int64  `json:"time"`
	UpdateTime    int64  `json:"updateTime"`
}

func (c *Client) OpenOrders(ctx context.Context) ([]Order, error) {
	var o []Order
	return o, c.do(ctx, http.MethodGet, c.FAPI, "/fapi/v1/openOrders", nil, true, false, &o)
}

// AlgoOrder 条件单（Algo 服务）。
type AlgoOrder struct {
	AlgoID        int64  `json:"algoId"`
	ClientAlgoID  string `json:"clientAlgoId"`
	AlgoType      string `json:"algoType"`
	OrderType     string `json:"orderType"`
	Symbol        string `json:"symbol"`
	Side          string `json:"side"`
	Quantity      Num    `json:"quantity"`
	AlgoStatus    string `json:"algoStatus"` // NEW | TRIGGERING | TRIGGERED | FINISHED | CANCELED | REJECTED | EXPIRED
	TriggerPrice  Num    `json:"triggerPrice"`
	Price         Num    `json:"price"`
	WorkingType   string `json:"workingType"`
	ClosePosition bool   `json:"closePosition"`
	ReduceOnly    bool   `json:"reduceOnly"`
	ActualOrderID string `json:"actualOrderId"`
	CreateTime    int64  `json:"createTime"`
	UpdateTime    int64  `json:"updateTime"`
}

func (c *Client) OpenAlgoOrders(ctx context.Context) ([]AlgoOrder, error) {
	var o []AlgoOrder
	return o, c.do(ctx, http.MethodGet, c.FAPI, "/fapi/v1/openAlgoOrders", nil, true, false, &o)
}

// Trade 一笔成交（/fapi/v1/userTrades）。
type Trade struct {
	ID              int64  `json:"id"`
	OrderID         int64  `json:"orderId"`
	Symbol          string `json:"symbol"`
	Side            string `json:"side"`
	Price           Num    `json:"price"`
	Qty             Num    `json:"qty"`
	RealizedPnl     Num    `json:"realizedPnl"`
	Commission      Num    `json:"commission"`
	CommissionAsset string `json:"commissionAsset"`
	QuoteQty        Num    `json:"quoteQty"`
	Time            int64  `json:"time"`
	Maker           bool   `json:"maker"`
}

// UserTrades 某品种从 fromID 起（含）的成交，最多 1000 条。fromID ≤ 0 时按时间取最近的。
func (c *Client) UserTrades(ctx context.Context, symbol string, fromID int64, start int64) ([]Trade, error) {
	q := url.Values{"symbol": {symbol}, "limit": {"1000"}}
	if fromID > 0 {
		q.Set("fromId", strconv.FormatInt(fromID, 10))
	} else if start > 0 {
		q.Set("startTime", strconv.FormatInt(start, 10))
	}
	var t []Trade
	return t, c.do(ctx, http.MethodGet, c.FAPI, "/fapi/v1/userTrades", q, true, false, &t)
}

// Income 资金费等收支（/fapi/v1/income）。
type Income struct {
	Symbol     string `json:"symbol"`
	IncomeType string `json:"incomeType"`
	Income     Num    `json:"income"`
	Asset      string `json:"asset"`
	Time       int64  `json:"time"`
	TranID     int64  `json:"tranId"`
}

// Funding start 之后的资金费，最多 1000 条。
func (c *Client) Funding(ctx context.Context, start int64) ([]Income, error) {
	q := url.Values{"incomeType": {"FUNDING_FEE"}, "limit": {"1000"}}
	if start > 0 {
		q.Set("startTime", strconv.FormatInt(start, 10))
	}
	var r []Income
	return r, c.do(ctx, http.MethodGet, c.FAPI, "/fapi/v1/income", q, true, false, &r)
}

// Income 所有类型的资金流水（划转、强平清算费、返佣……），对账发现钱包变了时用来解释。
func (c *Client) Income(ctx context.Context, start int64) ([]Income, error) {
	q := url.Values{"limit": {"1000"}}
	if start > 0 {
		q.Set("startTime", strconv.FormatInt(start, 10))
	}
	var r []Income
	return r, c.do(ctx, http.MethodGet, c.FAPI, "/fapi/v1/income", q, true, false, &r)
}

// ADLQuantile GET /fapi/v1/adlQuantile：每个品种的自动减仓排队（0–4，越大越靠前，币安每 30 秒更新）。单向持仓看 BOTH。
func (c *Client) ADLQuantile(ctx context.Context) (map[string]int, error) {
	var r []struct {
		Symbol string         `json:"symbol"`
		Q      map[string]int `json:"adlQuantile"`
	}
	if err := c.do(ctx, http.MethodGet, c.FAPI, "/fapi/v1/adlQuantile", nil, true, false, &r); err != nil {
		return nil, err
	}
	out := map[string]int{}
	for _, x := range r {
		out[x.Symbol] = x.Q["BOTH"]
	}
	return out, nil
}

// Restrictions GET /sapi/v1/account/apiRestrictions：这把密钥开了哪些权限。
type Restrictions struct {
	IPRestrict                 bool  `json:"ipRestrict"`
	EnableWithdrawals          bool  `json:"enableWithdrawals"`
	EnableInternalTransfer     bool  `json:"enableInternalTransfer"`
	PermitsUniversalTransfer   bool  `json:"permitsUniversalTransfer"`
	EnableFutures              bool  `json:"enableFutures"`
	EnableReading              bool  `json:"enableReading"`
	EnableSpotAndMarginTrading bool  `json:"enableSpotAndMarginTrading"`
	CreateTime                 int64 `json:"createTime"`
}

func (c *Client) Restrictions(ctx context.Context) (*Restrictions, error) {
	var r Restrictions
	return &r, c.do(ctx, http.MethodGet, c.SAPI, "/sapi/v1/account/apiRestrictions", nil, true, false, &r)
}

// ---------- 写 ----------

// QueryOrder 查一张普通委托（GET /fapi/v1/order）。
func (c *Client) QueryOrder(ctx context.Context, symbol string, id int64) (*Order, error) {
	var o Order
	return &o, c.do(ctx, http.MethodGet, c.FAPI, "/fapi/v1/order", url.Values{"symbol": {symbol}, "orderId": {strconv.FormatInt(id, 10)}}, true, false, &o)
}

// NewOrder 下普通委托（LIMIT、MARKET）。p 里放 symbol、side、type、quantity、price、timeInForce、reduceOnly、newClientOrderId。
func (c *Client) NewOrder(ctx context.Context, p url.Values) (*Order, error) {
	p.Set("newOrderRespType", "RESULT")
	var o Order
	return &o, c.do(ctx, http.MethodPost, c.FAPI, "/fapi/v1/order", p, true, false, &o)
}

// ModifyOrder 改限价单的价格、数量（PUT /fapi/v1/order，只支持 LIMIT）。
func (c *Client) ModifyOrder(ctx context.Context, symbol string, id int64, side, qty, price string) (*Order, error) {
	q := url.Values{"symbol": {symbol}, "orderId": {strconv.FormatInt(id, 10)}, "side": {side}, "quantity": {qty}, "price": {price}}
	var o Order
	return &o, c.do(ctx, http.MethodPut, c.FAPI, "/fapi/v1/order", q, true, false, &o)
}

func (c *Client) CancelOrder(ctx context.Context, symbol string, id int64) (*Order, error) {
	var o Order
	return &o, c.do(ctx, http.MethodDelete, c.FAPI, "/fapi/v1/order", url.Values{"symbol": {symbol}, "orderId": {strconv.FormatInt(id, 10)}}, true, false, &o)
}

func (c *Client) CancelAll(ctx context.Context, symbol string) error {
	return c.do(ctx, http.MethodDelete, c.FAPI, "/fapi/v1/allOpenOrders", url.Values{"symbol": {symbol}}, true, false, nil)
}

// NewAlgo 下条件单。p 里放 symbol、side、type（STOP_MARKET、TAKE_PROFIT_MARKET、STOP、TAKE_PROFIT）、
// quantity 或 closePosition、triggerPrice、price、workingType、reduceOnly、clientAlgoId。algoType 固定 CONDITIONAL。
func (c *Client) NewAlgo(ctx context.Context, p url.Values) (*AlgoOrder, error) {
	p.Set("algoType", "CONDITIONAL")
	p.Set("newOrderRespType", "RESULT")
	var o AlgoOrder
	return &o, c.do(ctx, http.MethodPost, c.FAPI, "/fapi/v1/algoOrder", p, true, false, &o)
}

func (c *Client) CancelAlgo(ctx context.Context, algoID int64) error {
	return c.do(ctx, http.MethodDelete, c.FAPI, "/fapi/v1/algoOrder", url.Values{"algoId": {strconv.FormatInt(algoID, 10)}}, true, false, nil)
}

func (c *Client) CancelAllAlgo(ctx context.Context, symbol string) error {
	return c.do(ctx, http.MethodDelete, c.FAPI, "/fapi/v1/algoOpenOrders", url.Values{"symbol": {symbol}}, true, false, nil)
}

// SetLeverage 改杠杆。
func (c *Client) SetLeverage(ctx context.Context, symbol string, lev int) error {
	return c.do(ctx, http.MethodPost, c.FAPI, "/fapi/v1/leverage", url.Values{"symbol": {symbol}, "leverage": {strconv.Itoa(lev)}}, true, false, nil)
}

// SetMarginType 改全仓、逐仓。已经是这个模式时币安报 -4046，当作成功。
func (c *Client) SetMarginType(ctx context.Context, symbol string, isolated bool) error {
	t := "CROSSED"
	if isolated {
		t = "ISOLATED"
	}
	err := c.do(ctx, http.MethodPost, c.FAPI, "/fapi/v1/marginType", url.Values{"symbol": {symbol}, "marginType": {t}}, true, false, nil)
	if Code(err) == -4046 {
		return nil
	}
	return err
}

// AdjustMargin 逐仓加减保证金：add = true 加，false 减。
func (c *Client) AdjustMargin(ctx context.Context, symbol string, amount string, add bool) error {
	t := "2"
	if add {
		t = "1"
	}
	return c.do(ctx, http.MethodPost, c.FAPI, "/fapi/v1/positionMargin", url.Values{"symbol": {symbol}, "amount": {amount}, "type": {t}}, true, false, nil)
}

// ---------- 用户数据流的 listenKey ----------

func (c *Client) ListenKey(ctx context.Context) (string, error) {
	var r struct {
		ListenKey string `json:"listenKey"`
	}
	err := c.do(ctx, http.MethodPost, c.FAPI, "/fapi/v1/listenKey", nil, false, true, &r)
	return r.ListenKey, err
}

// KeepAlive 延长 listenKey（60 分钟不续就过期，每 30 分钟续一次）。
func (c *Client) KeepAlive(ctx context.Context) error {
	return c.do(ctx, http.MethodPut, c.FAPI, "/fapi/v1/listenKey", nil, false, true, nil)
}

func (c *Client) CloseListenKey(ctx context.Context) error {
	return c.do(ctx, http.MethodDelete, c.FAPI, "/fapi/v1/listenKey", nil, false, true, nil)
}
