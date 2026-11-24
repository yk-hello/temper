package follow

// 跟随接口的权限和持久化（12 的 3.10、3.11）。需要数据库：QS_TEST_DB（会清空相关表）。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"qs/internal/auth"
	"qs/internal/book"
	"qs/internal/portfolio"
	"qs/internal/trading"
)

type hEnv struct {
	t     *testing.T
	srv   *httptest.Server
	books *book.Store
	eng   *trading.Engine
	pf    *portfolio.Portfolio
	m     *Manager
	st    *Store
	tevs  []trading.Event
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
	if _, err := pool.Exec(ctx, `drop table if exists sessions, email_codes, paper_accounts, users, follow_events cascade`); err != nil {
		t.Fatal(err)
	}
	users := auth.New(pool, quiet())
	users.DevMode = true
	if err := users.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	h := &hEnv{t: t, books: book.NewStore(), st: &Store{Pool: pool}}
	if err := h.st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	h.eng = trading.New(h.books, V, trading.NewSpecs(trading.DefaultSpecs()), quiet())
	h.eng.SetPersist(func(b []trading.Event) error {
		h.tevs = append(h.tevs, b...)
		return nil
	})
	m := D("77000")
	step := D("7.7")
	h.books.SetBook(book.Book{Venue: V, Instrument: "BTC-PERP", Bids: []book.Level{{Px: m.Sub(step), Qty: D("5")}}, Asks: []book.Level{{Px: m.Add(step), Qty: D("5")}}, Time: time.Now().UnixMilli()})
	h.books.UpdateTicker(V, "BTC-PERP", func(t *book.Ticker) { t.Last, t.Mark = m, m })
	h.pf = portfolio.New("s1", -1, V, V, h.books, h.eng)
	users.OnPaperAccount = func(_, id int64, initial string) { h.eng.Open(id, decimal.RequireFromString(initial)) }
	h.m = New(h.eng, users, func(id string) *portfolio.Portfolio {
		if id == "s1" {
			return h.pf
		}
		return nil
	}, quiet())
	h.m.Persist = func(b []Event) error { return h.st.Append(ctx, b) }
	h.pf.OnChange = func(c []portfolio.Change) { h.m.Process("s1", c) }
	gate := &auth.Gate{}
	gate.Open()
	mux := http.NewServeMux()
	users.Routes(mux, gate)
	(&API{M: h.m, Users: users, Gate: gate}).Routes(mux)
	h.srv = httptest.NewServer(mux)
	t.Cleanup(h.srv.Close)
	return h
}

type hc struct {
	h    *hEnv
	http *http.Client
}

func (h *hEnv) anon() *hc { return &hc{h: h, http: &http.Client{}} }

func (h *hEnv) user(email string) *hc {
	jar, _ := cookiejar.New(nil)
	c := &hc{h: h, http: &http.Client{Jar: jar}}
	if st, _ := c.do("POST", "/api/auth/register", map[string]string{"email": email, "password": "password-123"}); st != 200 {
		h.t.Fatal("注册失败")
	}
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

func TestHTTPPermissionsAndPersistence(t *testing.T) {
	h := newHTTPEnv(t)
	alice := h.user("alice@example.com")
	bob := h.user("bob@example.com")
	anon := h.anon()

	st, out := alice.do("POST", "/api/follows", map[string]any{"strategy": "s1", "alloc": "5000"})
	if st != 400 || out["code"] != "CONFIRM_REQUIRED" {
		t.Fatalf("没确认：%d %v", st, out)
	}
	st, out = alice.do("POST", "/api/follows", map[string]any{"strategy": "s1", "alloc": "5000", "confirm": true})
	if st != 200 || out["status"] != "active" {
		t.Fatalf("开启：%d %v", st, out)
	}
	id := int64(out["id"].(float64))
	acct := int64(out["account"].(float64))
	base := fmt.Sprintf("/api/follows/%d", id)

	// 第一次跟随自动建的账户真的在用户名下
	_, list := alice.do("GET", "/api/paper-accounts", nil)
	found := false
	for _, a := range list["accounts"].([]any) {
		x := a.(map[string]any)
		if int64(x["id"].(float64)) == acct && x["name"] == "策略跟随" {
			found = true
		}
	}
	if !found {
		t.Fatalf("账户列表里应有 策略跟随：%v", list)
	}
	// 用别人的账户开跟随：404
	if st, out := bob.do("POST", "/api/follows", map[string]any{"strategy": "s1", "account": acct, "alloc": "1000", "confirm": true}); st != 404 {
		t.Fatalf("别人的账户：%d %v", st, out)
	}
	// 系统账户（负数 id）用户碰不到
	if st, out := bob.do("POST", "/api/follows", map[string]any{"strategy": "s1", "account": -1, "alloc": "1000", "confirm": true}); st != 404 {
		t.Fatalf("系统账户：%d %v", st, out)
	}

	calls := []struct {
		method, path string
		body         any
	}{
		{"GET", base, nil},
		{"PATCH", base, map[string]any{"alloc": "1000"}},
		{"POST", base + "/pause", map[string]any{}},
		{"GET", base + "/resume-plan", nil},
		{"POST", base + "/resume", map[string]any{}},
		{"POST", base + "/stop", map[string]any{"mode": "close"}},
	}
	for _, c := range calls {
		if st, out := anon.do(c.method, c.path, c.body); st != 401 {
			t.Fatalf("没登录 %s %s：%d %v", c.method, c.path, st, out)
		}
		if st, out := bob.do(c.method, c.path, c.body); st != 404 {
			t.Fatalf("别人的 %s %s：%d %v", c.method, c.path, st, out)
		}
	}
	for _, c := range []struct{ m, p string }{{"GET", "/api/follows"}, {"POST", "/api/follows"}} {
		if st, out := anon.do(c.m, c.p, map[string]any{}); st != 401 {
			t.Fatalf("没登录 %s %s：%d %v", c.m, c.p, st, out)
		}
	}
	if _, out := bob.do("GET", "/api/follows", nil); len(out["follows"].([]any)) != 0 {
		t.Fatalf("列表里不该有别人的：%v", out)
	}
	if st, out := alice.do("GET", base, nil); st != 200 {
		t.Fatalf("自己的：%d %v", st, out)
	}

	// 走一遍：改参数、下单、暂停、恢复前看计划、恢复
	if st, out := alice.do("PATCH", base, map[string]any{"max_loss": "0.1"}); st != 200 || out["params"].(map[string]any)["max_loss"] != "0.1" {
		t.Fatalf("改参数：%d %v", st, out)
	}
	h.pf.Apply("m1", time.Now().UnixMilli(), time.Now().Add(time.Minute).UnixMilli(), false,
		[]portfolio.Target{{Instrument: "BTC-PERP", Weight: D("0.5"), RefPrice: D("77000"), Urgency: "immediate", MaxSlipBps: 20}})
	if st, _ := alice.do("POST", base+"/pause", map[string]any{}); st != 200 {
		t.Fatal(st)
	}
	h.pf.Apply("m2", time.Now().UnixMilli()+1, time.Now().Add(time.Minute).UnixMilli(), false,
		[]portfolio.Target{{Instrument: "BTC-PERP", Weight: D("0.2"), RefPrice: D("77000"), Urgency: "immediate", MaxSlipBps: 20}})
	_, plan := alice.do("GET", base+"/resume-plan", nil)
	if len(plan["trades"].([]any)) != 1 {
		t.Fatalf("恢复计划：%v", plan)
	}
	if st, _ := alice.do("POST", base+"/resume", map[string]any{}); st != 200 {
		t.Fatal(st)
	}

	// 真的从数据库重放
	h.m.Flush()
	evs, err := h.st.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	h.eng.Flush()
	eng2 := trading.New(h.books, V, trading.NewSpecs(trading.DefaultSpecs()), quiet())
	if err := eng2.Restore(h.tevs); err != nil {
		t.Fatal(err)
	}
	m2 := New(eng2, h.m.Accounts, h.m.Portfolio, quiet())
	m2.Restore(evs)
	u := &auth.User{ID: h.m.follows[id].User}
	a, err := h.m.Get(u, id)
	if err != nil {
		t.Fatal(err)
	}
	b, err := m2.Get(u, id)
	if err != nil {
		t.Fatal(err)
	}
	ja, _ := json.Marshal(a.Follow)
	jb, _ := json.Marshal(b.Follow)
	if string(ja) != string(jb) || len(a.Records) != len(b.Records) {
		t.Fatalf("重放后不一样：\n%s\n%s\n记录 %d / %d", ja, jb, len(a.Records), len(b.Records))
	}
	if err := eng2.Check(); err != nil {
		t.Fatal(err)
	}
	if st, _ := alice.do("POST", base+"/stop", map[string]any{"mode": "wat"}); st != 400 {
		t.Fatal("停止方式不对应该拒绝")
	}
	if st, out := alice.do("POST", base+"/stop", map[string]any{"mode": "close"}); st != 200 || out["status"] != "stopped" {
		t.Fatalf("停止：%d %v", st, out)
	}
}
