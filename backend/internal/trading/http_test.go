package trading

// 交易接口的安全和持久化（验收 2.7 重启、2.12、2.17）。需要数据库：QS_TEST_DB（会清空相关表）。

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"qs/internal/auth"
	"qs/internal/book"
)

type hEnv struct {
	t     *testing.T
	srv   *httptest.Server
	eng   *Engine
	st    *Store
	books *book.Store
	users *auth.Service
	now   time.Time
}

func newHTTPEnv(t *testing.T) *hEnv {
	dsn := os.Getenv("QS_TEST_DB")
	if dsn == "" {
		t.Skip("没有设置 QS_TEST_DB，跳过数据库测试")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `drop table if exists sessions, email_codes, paper_accounts, users, trading_events cascade`); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := &hEnv{t: t, books: book.NewStore(), now: time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)}
	h.users = auth.New(pool, log)
	h.users.DevMode = true
	if err := h.users.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	h.st = &Store{Pool: pool}
	if err := h.st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	sp := LooseSpecs()
	sp["BTC-PERP"].MinNotional = d("100") // 这个测试按以前的最小金额写的（币安现在 BTC 是 50）
	h.eng = New(h.books, V, NewSpecs(sp), log)
	h.eng.Now = func() time.Time { return h.now }
	h.eng.Persist = func(evs []Event) error { return h.st.Append(ctx, evs) }
	h.users.OnPaperAccount = func(_, id int64, initial string) { h.eng.Open(id, decimal.RequireFromString(initial)) }
	h.users.OnPaperArchive = func(_, id int64) { h.eng.Archive(id) }
	gate := &auth.Gate{}
	gate.Open()
	mux := http.NewServeMux()
	h.users.Routes(mux, gate)
	(&API{Engine: h.eng, Users: h.users, Gate: gate}).Routes(mux)
	h.srv = httptest.NewServer(mux)
	t.Cleanup(h.srv.Close)
	return h
}

type hc struct {
	h    *hEnv
	http *http.Client
	acct string
}

func (h *hEnv) user(email string) *hc {
	jar, _ := cookiejar.New(nil)
	c := &hc{h: h, http: &http.Client{Jar: jar}}
	if st, _ := c.do("POST", "/api/auth/register", map[string]string{"email": email, "password": "password-123"}); st != 200 {
		h.t.Fatal("注册失败")
	}
	_, b := c.do("GET", "/api/paper-accounts", nil)
	c.acct = strconv.FormatInt(int64(b["accounts"].([]any)[0].(map[string]any)["id"].(float64)), 10)
	return c
}

func (c *hc) do(method, path string, body any) (int, map[string]any) {
	var rd io.Reader
	if body != nil {
		j, _ := json.Marshal(body)
		rd = bytes.NewReader(j)
	}
	req, _ := http.NewRequest(method, c.h.srv.URL+path, rd)
	req.Header.Set("X-QS-Client", "web")
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		c.h.t.Fatal(err)
	}
	defer res.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func (h *hEnv) market(inst, mark string) {
	m := D(mark)
	spec, _ := h.eng.Specs.Get(inst)
	h.books.SetBook(book.Book{Venue: V, Instrument: inst, Bids: []book.Level{{Px: m.Sub(spec.Tick), Qty: D("1000")}}, Asks: []book.Level{{Px: m, Qty: D("1000")}}})
	h.books.UpdateTicker(V, inst, func(t *book.Ticker) { t.Mark = m; t.Last = m })
}

func TestHTTPOwnershipRateLimitPersistence(t *testing.T) {
	h := newHTTPEnv(t)
	h.market("BTC-PERP", "60000")
	a := h.user("alice@example.com")
	b := h.user("bob@example.com")
	base := "/api/trade/" + a.acct

	// A 正常下单
	st, o := a.do("POST", base+"/orders", map[string]any{"instrument": "BTC-PERP", "side": "buy", "type": "market", "qty": "0.1"})
	if st != 200 || o["status"] != "filled" {
		t.Fatalf("下单：%d %v", st, o)
	}
	st, v := a.do("GET", base, nil)
	if st != 200 || len(v["positions"].([]any)) != 1 {
		t.Fatalf("账户：%d %v", st, v)
	}
	// 拒单带参数（前端拼成"最小 100 USDT"）
	st, r := a.do("POST", base+"/orders", map[string]any{"instrument": "BTC-PERP", "side": "buy", "type": "limit", "price": "59000", "qty": "0.001"})
	if st != 400 || r["code"] != "MIN_NOTIONAL" || r["params"].(map[string]any)["min"] != "100" {
		t.Fatalf("拒单：%d %v", st, r)
	}
	st, lo := a.do("POST", base+"/orders", map[string]any{"instrument": "BTC-PERP", "side": "buy", "type": "limit", "price": "59000", "qty": "0.01"})
	if st != 200 {
		t.Fatal(lo)
	}
	oid := lo["id"].(string)

	// B 动 A 的任何东西都是 404；没登录是 401
	reqs := []struct{ m, p string }{
		{"GET", base}, {"GET", base + "/history?kind=orders"}, {"POST", base + "/preview"}, {"POST", base + "/orders"},
		{"PATCH", base + "/orders/" + oid}, {"DELETE", base + "/orders/" + oid}, {"POST", base + "/cancel-all"}, {"POST", base + "/close"},
		{"POST", base + "/close-all"}, {"POST", base + "/leverage"}, {"POST", base + "/margin-mode"}, {"POST", base + "/tpsl"},
	}
	body := map[string]any{"instrument": "BTC-PERP", "side": "sell", "type": "market", "qty": "0.1", "leverage": 5, "mode": "isolated", "fraction": "1"}
	for _, q := range reqs {
		if st, r := b.do(q.m, q.p, body); st != 404 || r["code"] != "NOT_FOUND" {
			t.Fatalf("B %s %s → %d %v，应为 404", q.m, q.p, st, r)
		}
	}
	jar, _ := cookiejar.New(nil)
	anon := &hc{h: h, http: &http.Client{Jar: jar}}
	for _, q := range reqs {
		if st, _ := anon.do(q.m, q.p, body); st != 401 {
			t.Fatalf("未登录 %s %s → %d，应为 401", q.m, q.p, st)
		}
	}
	if st, _ := a.do("GET", "/api/trade/999999", nil); st != 404 {
		t.Fatal("不存在的账户应 404")
	}
	// A 的仓位和挂单没被动过
	if _, v := a.do("GET", base, nil); len(v["orders"].([]any)) != 1 || len(v["positions"].([]any)) != 1 {
		t.Fatalf("A 的东西被改了：%v", v)
	}

	// 限流：同一秒内第 11 个下单类操作被拒（前面已经用了 3 个）
	n429 := 0
	for i := 0; i < 10; i++ {
		st, _ := a.do("POST", base+"/orders", map[string]any{"instrument": "BTC-PERP", "side": "buy", "type": "limit", "price": "58000", "qty": "0.01"})
		if st == 429 {
			n429++
		}
	}
	if n429 != 3 {
		t.Fatalf("应有 3 次被限流，实际 %d", n429)
	}
	if st, r := a.do("POST", base+"/orders", map[string]any{}); st != 429 || r["code"] != "RATE_LIMITED" {
		t.Fatalf("%d %v", st, r)
	}
	h.now = h.now.Add(time.Second) // 一秒后恢复
	if st, _ := a.do("DELETE", base+"/orders/"+oid, nil); st != 200 {
		t.Fatal("一秒后应恢复")
	}

	// 重启：从数据库读出事件重放，状态完全一致
	if h.eng.Flush() != 0 {
		t.Fatal("还有没写进数据库的事件")
	}
	id, _ := strconv.ParseInt(a.acct, 10, 64)
	before, _ := json.Marshal(h.eng.state(id))
	evs, err := h.st.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	y := New(h.books, V, NewSpecs(LooseSpecs()), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := y.Restore(evs); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(y.state(id))
	if !bytes.Equal(before, after) {
		t.Fatalf("重启后状态不一致\n前：%s\n后：%s", before, after)
	}
	if err := y.Check(); err != nil {
		t.Fatal(err)
	}

	// 重置模拟账户：旧账户归档，接口上就找不到了；新账户可以下单
	a.do("POST", "/api/me/trade-password", map[string]string{"new": "trade-pw-1"})
	st, na := a.do("POST", "/api/paper-accounts/"+a.acct+"/reset", map[string]string{"trade_password": "trade-pw-1"})
	if st != 200 {
		t.Fatal(na)
	}
	if st, _ := a.do("GET", base, nil); st != 404 {
		t.Fatal("归档的账户应 404")
	}
	if !h.eng.accounts[id].Archived || len(h.eng.accounts[id].Orders) != 0 {
		t.Fatal("引擎里旧账户应归档并撤单")
	}
	nb := "/api/trade/" + strconv.FormatInt(int64(na["id"].(float64)), 10)
	h.now = h.now.Add(2 * time.Second)
	if st, v := a.do("GET", nb, nil); st != 200 || v["wallet"] != "10000" {
		t.Fatalf("新账户：%d %v", st, v)
	}
}

// 第 8 节第 9 条：超大数字不能进引擎
func TestHugeNumberRejected(t *testing.T) {
	for _, b := range []string{`{"price":"1e20000000"}`, `{"qty":1e999}`, `{"tp":"-1E+5000","sl":""}`, `{"price":"` + strings.Repeat("9", 60) + `"}`, `{"price":"0.` + strings.Repeat("0", 60) + `1"}`} {
		if !hugeNumber.MatchString(b) {
			t.Fatalf("应该拦住：%s", b)
		}
	}
	for _, b := range []string{`{"price":"75000.1","qty":"0.001"}`, `{"instrument":"1e999-PERP"}`, `{"note":"abc1e999x"}`, `{"qty":1.5e3}`, `{"price":"1e5"}`} {
		if hugeNumber.MatchString(b) {
			t.Fatalf("不该拦：%s", b)
		}
	}
}
