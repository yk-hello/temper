package live

// 实盘接入走一遍（用假交易所，不碰真钱）。需要数据库：QS_TEST_DB（会清空相关表），不要指向正在用的库。

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"qs/internal/auth"
	"qs/internal/binance"
	"qs/internal/book"
	"qs/internal/signal"
	"qs/internal/trading"
)

const (
	fKey    = "fakekey-0123456789abcdef"
	fSecret = "fakesecret-0123456789abcdef"
	venue   = "binance-usdm"
)

type env struct {
	t     *testing.T
	srv   *httptest.Server
	fake  *binance.Fake
	books *book.Store
	desks *Desks
	store *Store
}

func newEnv(t *testing.T) *env {
	dsn := os.Getenv("QS_TEST_DB")
	if dsn == "" {
		t.Skip("没有设置 QS_TEST_DB，跳过数据库测试")
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `drop table if exists sessions, email_codes, paper_accounts, users, exchange_keys, live_accounts, live_fills, live_funding, live_orders cascade; drop sequence if exists live_account_ids`); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	users := auth.New(pool, log)
	users.DevMode = true
	if err := users.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	master := make([]byte, 32)
	_, _ = rand.Read(master)
	st := NewStore(master)
	if err := st.Attach(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := binance.NewFake(fKey, fSecret, 10000)
	t.Cleanup(f.Close)
	e := &env{t: t, fake: f, books: book.NewStore(), store: st}
	e.price("BTCUSDT", 60000)
	e.desks = &Desks{S: st, Specs: trading.NewSpecs(trading.LooseSpecs()), Books: e.books, Venue: venue, Log: log,
		URLs: map[string]Endpoints{"fake": {FAPI: f.URL(), SAPI: f.URL(), WS: f.WSURL()}}}
	if err := e.desks.Start(ctx); err != nil {
		t.Fatal(err)
	}
	gate := &auth.Gate{}
	gate.Open()
	mux := http.NewServeMux()
	users.Routes(mux, gate)
	eng := trading.New(e.books, venue, trading.NewSpecs(trading.LooseSpecs()), log)
	(&trading.API{Engine: eng, Users: users, Gate: gate, Live: e.desks, LiveOwns: st.Owns, RatePerSec: 1000}).Routes(mux)
	(&API{S: st, D: e.desks, Users: users}).Routes(mux)
	e.srv = httptest.NewServer(mux)
	t.Cleanup(e.srv.Close)
	return e
}

func (e *env) price(sym string, px float64) {
	e.fake.SetPrice(sym, px)
	m := decimal.NewFromFloat(px)
	inst := instOf(sym)
	e.books.SetBook(book.Book{Venue: venue, Instrument: inst, Bids: []book.Level{{Px: m.Sub(decimal.NewFromFloat(0.1)), Qty: decimal.NewFromInt(100)}}, Asks: []book.Level{{Px: m, Qty: decimal.NewFromInt(100)}}})
	e.books.UpdateTicker(venue, inst, func(t *book.Ticker) { t.Mark = m; t.Last = m })
}

type client struct {
	e    *env
	http *http.Client
}

func (e *env) user(email string) *client {
	jar, _ := cookiejar.New(nil)
	c := &client{e: e, http: &http.Client{Jar: jar}}
	if st, b := c.do("POST", "/api/auth/register", map[string]string{"email": email, "password": "password-123"}); st != 200 {
		e.t.Fatalf("注册：%d %v", st, b)
	}
	if st, b := c.do("POST", "/api/me/trade-password", map[string]string{"new": "trade-pw-8899"}); st != 200 {
		e.t.Fatalf("二级密码：%d %v", st, b)
	}
	return c
}

func (c *client) do(method, path string, body any) (int, map[string]any) {
	var rd io.Reader
	if body != nil {
		j, _ := json.Marshal(body)
		rd = bytes.NewReader(j)
	}
	req, _ := http.NewRequest(method, c.e.srv.URL+path, rd)
	req.Header.Set("X-QS-Client", "web")
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		c.e.t.Fatal(err)
	}
	defer res.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

// until 交易所的推送是异步的：等到条件成立（最多 5 秒）。
func (c *client) until(path string, ok func(map[string]any) bool) map[string]any {
	var b map[string]any
	for i := 0; i < 50; i++ {
		_, b = c.do("GET", path, nil)
		if ok(b) {
			return b
		}
		time.Sleep(100 * time.Millisecond)
	}
	c.e.t.Fatalf("等不到：%s %v", path, b)
	return nil
}

func list(b map[string]any, k string) []any {
	v, _ := b[k].([]any)
	return v
}

func TestLiveConnectAndTrade(t *testing.T) {
	e := newEnv(t)
	a := e.user("alice@example.com")
	b := e.user("bob@example.com")
	key := map[string]any{"env": "fake", "label": "主账户", "key": fKey, "secret": fSecret, "trade_password": "trade-pw-8899"}

	// 二级密码错、密钥不对、开了提现、双向持仓：都拒
	bad := map[string]any{}
	for k, v := range key {
		bad[k] = v
	}
	bad["trade_password"] = "nope-nope"
	if st, r := a.do("POST", "/api/live/keys", bad); st != 403 || r["code"] != "BAD_TRADE_PASSWORD" {
		t.Fatalf("二级密码错：%d %v", st, r)
	}
	bad["trade_password"], bad["key"] = "trade-pw-8899", "wrongkey-0123456789abcdef"
	if st, r := a.do("POST", "/api/live/keys", bad); st != 400 || r["code"] != "KEY_REJECTED" {
		t.Fatalf("密钥不对：%d %v", st, r)
	}
	e.fake.Withdraw = true
	if st, r := a.do("POST", "/api/live/keys", key); st != 400 || r["code"] != "KEY_HAS_WITHDRAW" || r["perms"].(map[string]any)["withdraw"] != true {
		t.Fatalf("开了提现：%d %v", st, r)
	}
	e.fake.Withdraw, e.fake.Dual = false, true
	if st, r := a.do("POST", "/api/live/keys", key); st != 400 || r["code"] != "ACCOUNT_DUAL_SIDE" {
		t.Fatalf("双向持仓：%d %v", st, r)
	}
	e.fake.Dual = false
	st, r := a.do("POST", "/api/live/keys", key)
	if st != 200 {
		t.Fatalf("保存密钥：%d %v", st, r)
	}
	kid := r["key"].(map[string]any)["id"].(float64)
	if h := r["key"].(map[string]any)["hint"]; h != "fake…cdef" {
		t.Fatalf("界面只显示头尾：%v", h)
	}
	// 数据库里存的是密文
	var raw []byte
	_ = e.store.pool.QueryRow(context.Background(), `select key_enc from exchange_keys where id=$1`, int64(kid)).Scan(&raw)
	if bytes.Contains(raw, []byte(fKey)) {
		t.Fatal("密钥没加密")
	}

	st, r = a.do("POST", "/api/live/accounts", map[string]any{"key_id": kid, "name": "币安实盘"})
	if st != 200 || r["id"].(float64) < float64(Base) {
		t.Fatalf("实盘账户：%d %v", st, r)
	}
	id := int64(r["id"].(float64))
	if st, _ := a.do("POST", "/api/live/accounts", map[string]any{"key_id": kid, "name": "再来一个"}); st != 409 {
		t.Fatalf("一把密钥只能一个账户：%d", st)
	}
	base := "/api/trade/" + strconv.FormatInt(id, 10)
	// 别人看不到
	if st, _ := b.do("GET", base, nil); st != 404 {
		t.Fatalf("别人的实盘账户：%d", st)
	}
	v := a.until(base, func(v map[string]any) bool { return v["wallet"] == "10000" })
	if _, ok := v["positions"].([]any); !ok {
		t.Fatalf("没有仓位时也要是数组：%v", v["positions"])
	}
	if len(list(v, "positions")) != 0 {
		t.Fatalf("账户：%v", v)
	}

	// 没确认"这是真钱"不能下单
	order := map[string]any{"instrument": "BTC-PERP", "side": "buy", "type": "market", "qty": "0.1", "tp": "63000", "sl": "58000"}
	if st, r := a.do("POST", base+"/orders", order); st != 400 || r["code"] != "LIVE_NOT_ARMED" {
		t.Fatalf("没确认：%d %v", st, r)
	}
	if st, r := a.do("POST", "/api/live/accounts/"+strconv.FormatInt(id, 10)+"/arm", map[string]any{"trade_password": "trade-pw-8899"}); st != 200 {
		t.Fatalf("确认：%d %v", st, r)
	}
	st, r = a.do("POST", base+"/orders", order)
	if st != 200 || r["status"] != "filled" || r["source"] != "manual" {
		t.Fatalf("市价单：%d %v", st, r)
	}
	// 成交后挂上止盈止损（交易所的条件单）
	v = a.until(base, func(v map[string]any) bool {
		ps := list(v, "positions")
		return len(ps) == 1 && ps[0].(map[string]any)["tp"] == "63000" && ps[0].(map[string]any)["sl"] == "58000"
	})
	p := list(v, "positions")[0].(map[string]any)
	if p["side"] != "long" || p["qty"] != "0.1" || p["entry"] != "60000" {
		t.Fatalf("仓位：%v", p)
	}
	// 自动减仓排队读交易所的（假交易所给 2），不是估的
	if p["adl"] != float64(2) || p["adl_est"] != nil {
		t.Fatalf("自动减仓排队：%v %v", p["adl"], p["adl_est"])
	}
	// 价格动了：浮盈按实时行情
	e.price("BTCUSDT", 61000)
	v = a.until(base, func(v map[string]any) bool {
		ps := list(v, "positions")
		return len(ps) == 1 && ps[0].(map[string]any)["upnl"] == "100"
	})

	// 下单预估：真实费率（假交易所是 0.05% / 0.02%）、成交后的强平价
	st, r = a.do("POST", base+"/preview", map[string]any{"instrument": "BTC-PERP", "side": "buy", "type": "market", "qty": "0.1"})
	liq, _ := decimal.NewFromString(fmt.Sprint(r["liq_px"]))
	if st != 200 || r["fee"] != "3.05" || r["maker"] != false || liq.Sign() <= 0 || liq.GreaterThan(decimal.NewFromInt(61000)) {
		t.Fatalf("预估（吃单、强平价）：%d %v", st, r)
	}
	st, r = a.do("POST", base+"/preview", map[string]any{"instrument": "BTC-PERP", "side": "buy", "type": "limit", "price": "60000", "qty": "0.1"})
	if st != 200 || r["maker"] != true || r["fee"] != "1.2" {
		t.Fatalf("预估（挂单费率）：%d %v", st, r)
	}
	st, r = a.do("POST", base+"/preview", map[string]any{"instrument": "BTC-PERP", "side": "sell", "type": "market", "qty": "0.1", "reduce_only": true})
	if st != 200 || r["margin"] != "0" || r["liq_px"] != "0" {
		t.Fatalf("预估（全平：不占保证金、没有强平价）：%d %v", st, r)
	}

	// 限价单：挂、改、撤
	st, r = a.do("POST", base+"/orders", map[string]any{"instrument": "BTC-PERP", "side": "buy", "type": "limit", "price": "59000", "qty": "0.01"})
	if st != 200 || r["status"] != "new" {
		t.Fatalf("限价单：%d %v", st, r)
	}
	lid := r["id"].(string)
	a.until(base, func(v map[string]any) bool { return len(list(v, "orders")) == 3 })
	if st, r := a.do("PATCH", base+"/orders/"+lid, map[string]any{"price": "59100"}); st != 200 || r["price"] != "59100" {
		t.Fatalf("改价：%d %v", st, r)
	}
	if st, r := a.do("DELETE", base+"/orders/"+lid, nil); st != 200 || r["status"] != "canceled" {
		t.Fatalf("撤单：%d %v", st, r)
	}
	// 11-16 币安模拟交易实测：下完马上改价（本地快照还没跟上）报"委托已经没了"。现在先去交易所读一次再找
	st, r = a.do("POST", base+"/orders", map[string]any{"instrument": "BTC-PERP", "side": "buy", "type": "limit", "price": "58000", "qty": "0.01"})
	if st != 200 {
		t.Fatalf("限价单：%d %v", st, r)
	}
	if st, r := a.do("PATCH", base+"/orders/"+r["id"].(string), map[string]any{"price": "58100"}); st != 200 || r["price"] != "58100" {
		t.Fatalf("下完马上改价：%d %v", st, r)
	} else {
		a.do("DELETE", base+"/orders/"+r["id"].(string), nil)
	}
	if st, r := a.do("POST", base+"/orders/"+lid+"/undo", nil); st != 400 || r["code"] != "NOT_SUPPORTED_LIVE" {
		t.Fatalf("实盘撤不回：%d %v", st, r)
	}
	// 条件开仓单走 Algo
	st, r = a.do("POST", base+"/orders", map[string]any{"instrument": "BTC-PERP", "side": "buy", "type": "cond_market", "trigger_px": "62000", "qty": "0.01"})
	if st != 200 || r["id"].(string)[0] != 'a' || r["trigger_dir"] != "up" {
		t.Fatalf("条件单：%d %v", st, r)
	}
	// 保证金不够：交易所的错误码翻成和模拟盘一样的
	if st, r := a.do("POST", base+"/orders", map[string]any{"instrument": "BTC-PERP", "side": "buy", "type": "market", "qty": "50"}); st != 400 || r["code"] != "LIVE_MARGIN" {
		t.Fatalf("保证金不够：%d %v", st, r)
	}
	// 改止损（拖线）：撤了重挂
	if st, r := a.do("POST", base+"/tpsl", map[string]any{"instrument": "BTC-PERP", "sl": "59500"}); st != 200 {
		t.Fatalf("改止损：%d %v", st, r)
	}
	a.until(base, func(v map[string]any) bool {
		ps := list(v, "positions")
		return len(ps) == 1 && ps[0].(map[string]any)["sl"] == "59500" && ps[0].(map[string]any)["tp"] == "63000"
	})
	// 打到止损：仓位没了，两笔成交记下来（第二笔亏）
	e.price("BTCUSDT", 59400)
	a.until(base, func(v map[string]any) bool { return len(list(v, "positions")) == 0 })
	var fills map[string]any
	for i := 0; i < 50; i++ {
		_, fills = a.do("GET", base+"/history?kind=fills", nil)
		if len(list(fills, "items")) == 2 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	items := list(fills, "items")
	if len(items) != 2 || items[0].(map[string]any)["source"] != "sl" {
		t.Fatalf("成交记录：%v", fills)
	}
	if rz, _ := decimal.NewFromString(items[0].(map[string]any)["realized"].(string)); rz.Sign() >= 0 {
		t.Fatalf("止损应该亏：%v", items[0])
	}
	// TradingView 信号绑在交易所账户上：同一个仓位算法，单子发到交易所，来源记成信号
	sx := &signal.Engines{Live: e.desks}
	if av := sx.Available(id); av.Sign() <= 0 {
		t.Fatalf("可用：%v", av)
	}
	cmd, _ := signal.Parse([]byte("做多 BTC 10%"))
	side, notional, _ := signal.PerpPlan(cmd, decimal.NewFromInt(12000))
	if _, err := sx.Perp(id, "BTC-PERP", side, notional, false); err != nil {
		t.Fatalf("信号开仓：%v", err)
	}
	a.until(base, func(v map[string]any) bool { return len(list(v, "positions")) == 1 })
	if _, err := sx.Perp(id, "BTC-PERP", "", decimal.Zero, true); err != nil {
		t.Fatalf("信号平仓：%v", err)
	}
	a.until(base, func(v map[string]any) bool { return len(list(v, "positions")) == 0 })
	if _, err := sx.Spot(id, "BTC-SPOT", "buy", decimal.NewFromInt(10)); err == nil {
		t.Fatal("交易所账户不接现货信号")
	}
	_, fills = a.do("GET", base+"/history?kind=fills&size=10", nil)
	if src := list(fills, "items")[0].(map[string]any)["source"]; src != "signal" {
		t.Fatalf("信号的成交来源：%v", src)
	}

	// 撤掉全部（条件开仓单还挂着）
	if st, r := a.do("POST", base+"/cancel-all", map[string]any{}); st != 200 || r["canceled"].(float64) < 1 {
		t.Fatalf("全撤：%d %v", st, r)
	}
	// 账户列表和连接状态
	st, r = a.do("GET", "/api/live", nil)
	if st != 200 || r["enabled"] != true || len(list(r, "accounts")) != 1 {
		t.Fatalf("概览：%d %v", st, r)
	}
	if s := list(r, "accounts")[0].(map[string]any)["status"].(map[string]any); s["stream"] != true || s["env"] != "fake" {
		t.Fatalf("连接状态：%v", s)
	}
	// 下单确认：默认打开；关要二级密码，打开不用（D96）
	if acc := list(r, "accounts")[0].(map[string]any); acc["confirm"] != true {
		t.Fatalf("默认要确认：%v", acc)
	}
	cpath := "/api/live/accounts/" + strconv.FormatInt(id, 10) + "/confirm"
	if st, r := a.do("POST", cpath, map[string]any{"on": false, "trade_password": "wrong-pw"}); st != 403 || r["code"] != "BAD_TRADE_PASSWORD" {
		t.Fatalf("关确认要二级密码：%d %v", st, r)
	}
	if st, _ := a.do("POST", cpath, map[string]any{"on": false, "trade_password": "trade-pw-8899"}); st != 200 {
		t.Fatal("关确认")
	}
	if _, r := a.do("GET", "/api/live", nil); list(r, "accounts")[0].(map[string]any)["confirm"] != false {
		t.Fatal("关掉以后是 false")
	}
	if st, _ := a.do("POST", cpath, map[string]any{"on": true}); st != 200 {
		t.Fatal("打开不用二级密码")
	}
	if st, _ := b.do("POST", cpath, map[string]any{"on": true}); st != 404 {
		t.Fatal("别人的账户改不了")
	}

	// 停用：别处也查不到了
	if st, _ := a.do("DELETE", "/api/live/accounts/"+strconv.FormatInt(id, 10), nil); st != 200 {
		t.Fatal("停用")
	}
	if st, _ := a.do("GET", base, nil); st != 404 {
		t.Fatalf("停用后：%d", st)
	}
}

func TestErrorMapping(t *testing.T) {
	for c, want := range map[int]string{-2019: "LIVE_MARGIN", -2022: "REDUCE_ONLY_NO_POSITION", -4005: "LIVE_QTY_LARGE", -2021: "LIVE_TRIGGER_NOW", -1003: "LIVE_RATE_LIMIT", -4061: "LIVE_DUAL_SIDE", -9999: "EXCHANGE_ERROR"} {
		err := reject(&binance.APIError{Code: c, Msg: "x"})
		var r *trading.Reject
		if !asReject(err, &r) || r.Code != want {
			t.Fatalf("%d → %v，应该是 %s", c, err, want)
		}
	}
	// 11-18：被币安按 IP 限流（闸门直接挡下，外面还包着网络库的一层）：写明要等几分钟
	err := reject(&url.Error{Op: "Post", URL: "https://fapi.binance.com/fapi/v1/order", Err: &binance.ErrThrottled{Host: "fapi.binance.com", Wait: 90 * time.Second}})
	var r *trading.Reject
	if !asReject(err, &r) || r.Code != "EXCHANGE_THROTTLED" || r.Params["min"] != "2" {
		t.Fatalf("限流：%v %+v", err, r)
	}
	if sourceOf("qsl-1234") != "sl" || sourceOf("web_abc") != "external" || sourceOf(clientID("bot")) != "bot" {
		t.Fatal("来源")
	}
}

func asReject(err error, r **trading.Reject) bool {
	x, ok := err.(*trading.Reject)
	*r = x
	return ok
}

// D107：二级密码重置后交易所账户回到"未开启"：开新仓被拒；减仓、平仓、设止盈止损照常（不然手上的仓位平不掉、
// 策略的止损退出也做不了）；用新的二级密码重新开启后照常开仓
func TestDisarmedCanStillReduce(t *testing.T) {
	e := newEnv(t)
	a := e.user("carol@example.com")
	st, r := a.do("POST", "/api/live/keys", map[string]any{"env": "fake", "label": "主账户", "key": fKey, "secret": fSecret, "trade_password": "trade-pw-8899"})
	if st != 200 {
		t.Fatalf("保存密钥：%d %v", st, r)
	}
	kid := r["key"].(map[string]any)["id"].(float64)
	st, r = a.do("POST", "/api/live/accounts", map[string]any{"key_id": kid, "name": "币安实盘"})
	if st != 200 {
		t.Fatalf("实盘账户：%d %v", st, r)
	}
	id := int64(r["id"].(float64))
	base := "/api/trade/" + strconv.FormatInt(id, 10)
	if st, r := a.do("POST", "/api/live/accounts/"+strconv.FormatInt(id, 10)+"/arm", map[string]any{"trade_password": "trade-pw-8899"}); st != 200 {
		t.Fatalf("开启：%d %v", st, r)
	}
	if st, r := a.do("POST", base+"/orders", map[string]any{"instrument": "BTC-PERP", "side": "buy", "type": "market", "qty": "0.1"}); st != 200 {
		t.Fatalf("开仓：%d %v", st, r)
	}
	a.until(base, func(v map[string]any) bool { return len(list(v, "positions")) == 1 })

	var uid int64
	_ = e.store.pool.QueryRow(context.Background(), `select usr from live_accounts where id=$1`, id).Scan(&uid)
	if err := e.store.DisarmAll(context.Background(), uid); err != nil {
		t.Fatal(err)
	}
	if st, r := a.do("POST", base+"/orders", map[string]any{"instrument": "BTC-PERP", "side": "buy", "type": "market", "qty": "0.01"}); st != 400 || r["code"] != "LIVE_NOT_ARMED" {
		t.Fatalf("未开启时开新仓要拒：%d %v", st, r)
	}
	if st, r := a.do("POST", base+"/tpsl", map[string]any{"instrument": "BTC-PERP", "sl": "55000"}); st != 200 {
		t.Fatalf("未开启时也能设止损：%d %v", st, r)
	}
	if st, r := a.do("POST", base+"/close", map[string]any{"instrument": "BTC-PERP", "fraction": "0.5"}); st != 200 {
		t.Fatalf("未开启时也能平一半：%d %v", st, r)
	}
	if st, r := a.do("POST", base+"/orders", map[string]any{"instrument": "BTC-PERP", "side": "sell", "type": "market", "qty": "0.01", "reduce_only": true}); st != 200 {
		t.Fatalf("未开启时只减仓的单能下：%d %v", st, r)
	}
	if st, r := a.do("POST", "/api/live/accounts/"+strconv.FormatInt(id, 10)+"/arm", map[string]any{"trade_password": "trade-pw-8899"}); st != 200 {
		t.Fatalf("重新开启：%d %v", st, r)
	}
	if st, r := a.do("POST", base+"/orders", map[string]any{"instrument": "BTC-PERP", "side": "buy", "type": "market", "qty": "0.01"}); st != 200 {
		t.Fatalf("重新开启后能开仓：%d %v", st, r)
	}
}

// 11-18 币安模拟账户：市价单刚成交，返回的 avgPrice 还是 0，提示写"均价 0.00000"。用成交总额 ÷ 成交量算
func TestPlainOrderAvgFromCumQuote(t *testing.T) {
	o := plainOrder(1, binance.Order{OrderID: 7, Symbol: "BTCUSDT", Side: "BUY", Type: "MARKET", OrigQty: "0.002", ExecutedQty: "0.002", AvgPrice: "0.00000", CumQuote: "129.0602", Status: "FILLED"})
	if !o.AvgPx.Equal(decimal.RequireFromString("64530.1")) {
		t.Fatalf("均价：%s", o.AvgPx)
	}
	o = plainOrder(1, binance.Order{OrderID: 8, Symbol: "BTCUSDT", Side: "BUY", Type: "LIMIT", OrigQty: "0.002", ExecutedQty: "0", AvgPrice: "0", CumQuote: "0", Status: "NEW"})
	if !o.AvgPx.IsZero() {
		t.Fatalf("没成交时均价是 0：%s", o.AvgPx)
	}
}

// 11-18：复盘要知道交易所账户上的哪些单是止损：平整个仓位的 STOP、只减仓往亏损方向触发的；止盈、开仓用的条件单不算
func TestIsStopLoss(t *testing.T) {
	cases := []struct {
		o    binance.AlgoOrder
		want bool
	}{
		{binance.AlgoOrder{AlgoID: 1, Symbol: "BTCUSDT", Side: "SELL", OrderType: "STOP_MARKET", ClosePosition: true}, true},
		{binance.AlgoOrder{AlgoID: 2, Symbol: "BTCUSDT", Side: "SELL", OrderType: "TAKE_PROFIT_MARKET", ClosePosition: true}, false},
		{binance.AlgoOrder{AlgoID: 3, Symbol: "BTCUSDT", Side: "SELL", OrderType: "STOP_MARKET", ReduceOnly: true}, true},
		{binance.AlgoOrder{AlgoID: 4, Symbol: "BTCUSDT", Side: "BUY", OrderType: "STOP_MARKET", ReduceOnly: true}, true},
		{binance.AlgoOrder{AlgoID: 5, Symbol: "BTCUSDT", Side: "SELL", OrderType: "TAKE_PROFIT_MARKET", ReduceOnly: true}, false},
		{binance.AlgoOrder{AlgoID: 6, Symbol: "BTCUSDT", Side: "BUY", OrderType: "STOP_MARKET"}, false}, // 涨破开多：开仓用的
	}
	for _, c := range cases {
		if got := isStopLoss(algoOrder(1, c.o)); got != c.want {
			t.Fatalf("%+v：应为 %v", c.o, c.want)
		}
	}
}
