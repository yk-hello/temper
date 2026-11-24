package binapi

// 用仓库里接币安实盘的同一个客户端（internal/binance）来连这个仿真接口：
// 它按币安的格式发请求、按币安的格式解析回复，能跑通说明"现成的币安程序改个地址就能用"。

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"qs/internal/binance"
	"qs/internal/book"
	"qs/internal/trading"
)

const venue = "binance-usdm"

type env struct {
	t     *testing.T
	srv   *httptest.Server
	books *book.Store
	eng   *trading.Engine
	c     *binance.Client
}

func (e *env) price(inst string, px float64) {
	m := decimal.NewFromFloat(px)
	e.books.SetBook(book.Book{Venue: venue, Instrument: inst, Bids: []book.Level{{Px: m.Sub(decimal.NewFromFloat(0.1)), Qty: decimal.NewFromInt(100)}}, Asks: []book.Level{{Px: m, Qty: decimal.NewFromInt(100)}}})
	e.books.UpdateTicker(venue, inst, func(t *book.Ticker) { t.Mark = m; t.Last = m; t.Time = time.Now().UnixMilli() })
}

func newEnv(t *testing.T) *env {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	e := &env{t: t, books: book.NewStore()}
	e.eng = trading.New(e.books, venue, trading.NewSpecs(trading.LooseSpecs()), log)
	e.price("BTC-PERP", 60000)
	e.price("ETH-PERP", 3000)
	e.eng.Open(7, decimal.NewFromInt(10000))
	keys := &Store{cache: map[string]Key{"k1": {APIKey: "k1", Secret: "s3cret-s3cret-s3cret", User: 1, Account: 7}}}
	s := &Server{Keys: keys, E: e.eng, Books: e.books, Venue: venue}
	mux := http.NewServeMux()
	mux.Handle("/bapi/", s)
	e.srv = httptest.NewServer(mux)
	t.Cleanup(e.srv.Close)
	e.c = &binance.Client{FAPI: e.srv.URL + "/bapi", WS: "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/bapi", Key: "k1", Secret: "s3cret-s3cret-s3cret"}
	return e
}

func TestBinanceClientRoundTrip(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a, err := e.c.Account(ctx)
	if err != nil || a.TotalWalletBalance.F() != 10000 || a.AvailableBalance.F() != 10000 {
		t.Fatalf("账户：%v %+v", err, a)
	}
	if dual, err := e.c.DualSide(ctx); err != nil || dual {
		t.Fatalf("单向持仓：%v %v", err, dual)
	}
	if err := e.c.SetLeverage(ctx, "BTCUSDT", 5); err != nil {
		t.Fatalf("改杠杆：%v", err)
	}
	cfg, err := e.c.SymbolConfigs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range cfg {
		if x.Symbol == "BTCUSDT" && x.Leverage != 5 {
			t.Fatalf("杠杆没改上：%+v", x)
		}
	}
	// 市价买 0.1：按卖一 60000 成交，手续费 0.05%
	o, err := e.c.NewOrder(ctx, url.Values{"symbol": {"BTCUSDT"}, "side": {"BUY"}, "type": {"MARKET"}, "quantity": {"0.1"}, "newClientOrderId": {"my-1"}})
	if err != nil || o.Status != "FILLED" || o.ExecutedQty.F() != 0.1 || o.ClientOrderID != "my-1" || o.AvgPrice.F() != 60000 {
		t.Fatalf("市价单：%v %+v", err, o)
	}
	pos, err := e.c.Positions(ctx)
	if err != nil || len(pos) != 1 || pos[0].Symbol != "BTCUSDT" || pos[0].PositionAmt.F() != 0.1 || pos[0].EntryPrice.F() != 60000 {
		t.Fatalf("仓位：%v %+v", err, pos)
	}
	tr, err := e.c.UserTrades(ctx, "BTCUSDT", 0, 0)
	if err != nil || len(tr) != 1 || tr[0].Commission.F() != 3 || tr[0].OrderID != o.OrderID {
		t.Fatalf("成交：%v %+v", err, tr)
	}
	// 限价单挂着、改价、撤
	l, err := e.c.NewOrder(ctx, url.Values{"symbol": {"BTCUSDT"}, "side": {"BUY"}, "type": {"LIMIT"}, "timeInForce": {"GTC"}, "price": {"59000"}, "quantity": {"0.05"}})
	if err != nil || l.Status != "NEW" {
		t.Fatalf("限价单：%v %+v", err, l)
	}
	open, err := e.c.OpenOrders(ctx)
	if err != nil || len(open) != 1 || open[0].OrderID != l.OrderID || open[0].Price.F() != 59000 {
		t.Fatalf("挂单：%v %+v", err, open)
	}
	if m, err := e.c.ModifyOrder(ctx, "BTCUSDT", l.OrderID, "BUY", "0.05", "59500"); err != nil || m.Price.F() != 59500 {
		t.Fatalf("改价：%v %+v", err, m)
	}
	if c, err := e.c.CancelOrder(ctx, "BTCUSDT", l.OrderID); err != nil || c.Status != "CANCELED" {
		t.Fatalf("撤单：%v %+v", err, c)
	}
	if _, err := e.c.CancelOrder(ctx, "BTCUSDT", l.OrderID); binance.Code(err) != -2011 {
		t.Fatalf("撤已经撤掉的：%v", err)
	}
	// 条件单走 Algo 接口：整仓止损挂到仓位上
	al, err := e.c.NewAlgo(ctx, url.Values{"algoType": {"CONDITIONAL"}, "symbol": {"BTCUSDT"}, "side": {"SELL"}, "type": {"STOP_MARKET"}, "triggerPrice": {"55000"}, "closePosition": {"true"}, "workingType": {"MARK_PRICE"}})
	if err != nil || al.AlgoStatus != "NEW" || !al.ClosePosition || al.TriggerPrice.F() != 55000 {
		t.Fatalf("止损：%v %+v", err, al)
	}
	algos, err := e.c.OpenAlgoOrders(ctx)
	if err != nil || len(algos) != 1 {
		t.Fatalf("条件单列表：%v %+v", err, algos)
	}
	if open, _ := e.c.OpenOrders(ctx); len(open) != 0 {
		t.Fatalf("普通挂单列表里不该有条件单：%+v", open)
	}
	if err := e.c.CancelAlgo(ctx, al.AlgoID); err != nil {
		t.Fatalf("撤条件单：%v", err)
	}
	// 拒单：错误码和币安一样（程序按错误码处理）
	_, err = e.c.NewOrder(ctx, url.Values{"symbol": {"BTCUSDT"}, "side": {"BUY"}, "type": {"MARKET"}, "quantity": {"50"}})
	if binance.Code(err) != -2019 {
		t.Fatalf("保证金不够：%v", err)
	}
	_, err = e.c.NewOrder(ctx, url.Values{"symbol": {"BTCUSDT"}, "side": {"SELL"}, "type": {"MARKET"}, "quantity": {"0.5"}, "reduceOnly": {"true"}})
	if err != nil {
		t.Fatalf("只减仓（超过仓位按仓位减）：%v", err)
	}
	_, err = e.c.NewOrder(ctx, url.Values{"symbol": {"NOPEUSDT"}, "side": {"BUY"}, "type": {"MARKET"}, "quantity": {"1"}})
	if binance.Code(err) != -1121 {
		t.Fatalf("没有的品种：%v", err)
	}
	fund, err := e.c.Funding(ctx, 0)
	if err != nil || fund == nil {
		t.Fatalf("资金费流水：%v", err)
	}
}

func TestAuth(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	bad := &binance.Client{FAPI: e.c.FAPI, Key: e.c.Key, Secret: "wrong-secret-wrong"}
	if _, err := bad.Account(ctx); binance.Code(err) != -1022 {
		t.Fatalf("签名错：%v", err)
	}
	bad = &binance.Client{FAPI: e.c.FAPI, Key: "nope", Secret: e.c.Secret}
	if _, err := bad.Account(ctx); binance.Code(err) != -2015 {
		t.Fatalf("密钥不对：%v", err)
	}
	// 时间戳差太多
	q := url.Values{"timestamp": {"1000"}}
	res, err := http.Get(e.srv.URL + "/bapi/fapi/v2/balance?" + q.Encode() + "&signature=x")
	if err == nil {
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if !strings.Contains(string(b), "-2014") { // 没带密钥头：先报密钥
			t.Fatalf("没带密钥：%s", b)
		}
	}
	// 没仿的接口：说清楚，不装作成功
	res, err = http.Get(e.srv.URL + "/bapi/fapi/v1/somethingElse")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 404 || !strings.Contains(string(b), "not supported") {
		t.Fatalf("没仿的接口：%d %s", res.StatusCode, b)
	}
	// 公开接口不要密钥
	res, err = http.Get(e.srv.URL + "/bapi/fapi/v1/exchangeInfo")
	if err != nil {
		t.Fatal(err)
	}
	b, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(b), `"symbol":"BTCUSDT"`) || !strings.Contains(string(b), `"filterType":"PERCENT_PRICE"`) {
		t.Fatalf("品种规则：%.200s", b)
	}
}

// 账户推送：用同一个客户端的 Stream（它会申请 listenKey、连 /private/ws/<listenKey>）
func TestUserStream(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var mu sync.Mutex
	var got []binance.Event
	up := make(chan struct{}, 1)
	go e.c.Stream(ctx, func(ev binance.Event) {
		mu.Lock()
		got = append(got, ev)
		mu.Unlock()
	}, func(ok bool, _ string) {
		if ok {
			select {
			case up <- struct{}{}:
			default:
			}
		}
	})
	select {
	case <-up:
	case <-ctx.Done():
		t.Fatal("推送连不上")
	}
	if _, err := e.c.NewOrder(ctx, url.Values{"symbol": {"ETHUSDT"}, "side": {"BUY"}, "type": {"MARKET"}, "quantity": {"1"}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		var trade, acct bool
		for _, ev := range got {
			if ev.Type == "ORDER_TRADE_UPDATE" {
				if u, err := binance.ParseOrderUpdate(ev.Raw); err == nil && u.ExecType == "TRADE" && u.LastQty.F() == 1 && u.LastPrice.F() == 3000 && u.Status == "FILLED" {
					trade = true
				}
			}
			if ev.Type == "ACCOUNT_UPDATE" {
				acct = true
			}
		}
		mu.Unlock()
		if trade && acct {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("没收到成交和账户变化的推送：%+v", got)
}

// D103：现成的币安程序发 GTD、priceMatch、priceProtect，模拟盘按币安的规则处理。
func TestBinanceExtraRules(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	// priceMatch：不填价格，排队价 = 买一 59999.9
	o, err := e.c.NewOrder(ctx, url.Values{"symbol": {"BTCUSDT"}, "side": {"BUY"}, "type": {"LIMIT"}, "timeInForce": {"GTC"}, "priceMatch": {"QUEUE"}, "quantity": {"0.01"}})
	if err != nil || o.Price.F() != 59999.9 {
		t.Fatalf("排队价应为买一 59999.9：%v %+v", err, o)
	}
	if _, err := e.c.NewOrder(ctx, url.Values{"symbol": {"BTCUSDT"}, "side": {"BUY"}, "type": {"LIMIT"}, "timeInForce": {"GTC"}, "priceMatch": {"QUEUE"}, "price": {"59000"}, "quantity": {"0.01"}}); err == nil || !strings.Contains(err.Error(), "-1106") {
		t.Fatalf("同时填价格应 -1106：%v", err)
	}
	// GTD：太近的拒（-5040），够远的记下有效期
	soon := strconv.FormatInt(time.Now().Add(time.Minute).UnixMilli(), 10)
	if _, err := e.c.NewOrder(ctx, url.Values{"symbol": {"BTCUSDT"}, "side": {"BUY"}, "type": {"LIMIT"}, "timeInForce": {"GTD"}, "goodTillDate": {soon}, "price": {"59000"}, "quantity": {"0.01"}}); err == nil || !strings.Contains(err.Error(), "-5040") {
		t.Fatalf("有效期太近应 -5040：%v", err)
	}
	later := time.Now().Add(time.Hour).UnixMilli()
	if _, err := e.c.NewOrder(ctx, url.Values{"symbol": {"BTCUSDT"}, "side": {"BUY"}, "type": {"LIMIT"}, "timeInForce": {"GTD"}, "goodTillDate": {strconv.FormatInt(later, 10)}, "price": {"59000"}, "quantity": {"0.01"}}); err != nil {
		t.Fatal(err)
	}
	// 条件单的价格保护
	if _, err := e.c.NewAlgo(ctx, url.Values{"algoType": {"CONDITIONAL"}, "symbol": {"BTCUSDT"}, "side": {"BUY"}, "type": {"STOP_MARKET"}, "triggerPrice": {"61000"}, "quantity": {"0.01"}, "priceProtect": {"TRUE"}}); err != nil {
		t.Fatal(err)
	}
	v, _ := e.eng.View(7)
	var gtd, protect bool
	for _, o := range v.Orders {
		if o.TIF == trading.GTD && o.GoodTill == later/1000*1000 {
			gtd = true
		}
		if o.Type == trading.CondMarket && o.PriceProtect {
			protect = true
		}
	}
	if !gtd || !protect {
		t.Fatalf("GTD 有效期 %v、价格保护 %v 应记在委托上", gtd, protect)
	}
}
