package admin

// 管理后台走一遍。需要数据库：QS_TEST_DB（会清空相关表），不要指向正在用的库。

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
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"qs/internal/auth"
)

const tp = "trade-pw-8899"

type env struct {
	t    *testing.T
	srv  *httptest.Server
	pool *pgxpool.Pool
	api  *API

	cancels int
	resumed []string
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
	if _, err := pool.Exec(ctx, `drop table if exists sessions, email_codes, paper_accounts, users, admin_settings, admin_log cascade`); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	users := auth.New(pool, log)
	users.DevMode = true
	if err := users.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, pool: pool}
	st := &Store{Pool: pool, SignupOpen: true} // 测试要注册好几个账号；默认关的情况在 TestEveryAccountIsEqual 里单独看
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	e.api = &API{S: st, G: &Gate{}, Users: users, Log: log,
		CancelOpening: func() int { e.cancels++; return 3 },
		Strategies:    func() any { return []map[string]any{{"strategy_id": "s1", "halted": "pause_new"}} },
		Resume: func(id string) bool {
			if id != "s1" {
				return false
			}
			e.resumed = append(e.resumed, id)
			return true
		}}
	users.RegClosed = func(ctx context.Context) bool { return st.RegMode(ctx) == "admin" }
	if err := e.api.Load(ctx); err != nil {
		t.Fatal(err)
	}
	gate := &auth.Gate{}
	gate.Open()
	mux := http.NewServeMux()
	users.Routes(mux, gate)
	e.api.Routes(mux)
	e.srv = httptest.NewServer(mux)
	t.Cleanup(e.srv.Close)
	return e
}

type client struct {
	e    *env
	http *http.Client
	id   int64
}

func (e *env) client() *client {
	jar, _ := cookiejar.New(nil)
	return &client{e: e, http: &http.Client{Jar: jar}}
}

func (e *env) user(email string) *client {
	c := e.client()
	st, b := c.do("POST", "/api/auth/register", map[string]string{"email": email, "password": "password-123"})
	if st != 200 {
		e.t.Fatalf("注册 %s：%d %v", email, st, b)
	}
	c.id = int64(b["user"].(map[string]any)["id"].(float64))
	if st, b := c.do("POST", "/api/me/trade-password", map[string]string{"new": tp}); st != 200 {
		e.t.Fatalf("设二级密码：%d %v", st, b)
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

func code(b map[string]any) string { s, _ := b["code"].(string); return s }

// D104 独立使用 + D113（11-24）：只有所有者（这台第一个注册的账号）能进"系统"页，别的账号看不到全部账号、改不了全局设置；
// 管别人账号的接口（改角色、停用、重置别人的二次验证和二级密码）都没有了；第一个账号以后默认不能自己注册。
func TestEveryAccountIsEqual(t *testing.T) {
	e := newEnv(t)
	a := e.user("first@example.com")
	b := e.user("second@example.com")
	if st, r := a.do("GET", "/api/admin", nil); st != 200 || len(r["users"].([]any)) != 2 {
		t.Fatalf("所有者能进系统页：%d %v", st, r)
	}
	for _, path := range []string{"/api/admin", "/api/admin/log", "/api/admin/settings", "/api/admin/ai"} {
		if st, r := b.do("GET", path, nil); st != 403 || code(r) != "FORBIDDEN" {
			t.Fatalf("别的账号看不到 %s：%d %v", path, st, r)
		}
	}
	if st, _ := b.do("POST", "/api/admin/halt", map[string]any{"level": 1, "trade_password": tp}); st != 403 {
		t.Fatalf("别的账号不能全局停机：%d", st)
	}
	for _, c := range []*client{a, b} {
		_, me := c.do("GET", "/api/me", nil)
		if s, _ := json.Marshal(me); bytes.Contains(s, []byte(`"role"`)) {
			t.Fatalf("账号信息里不该再有角色：%s", s)
		}
	}
	for _, path := range []string{"/role", "/disable", "/reset-2fa", "/reset-trade-password"} {
		if st, _ := a.do("POST", "/api/admin/users/"+strconv.FormatInt(b.id, 10)+path, map[string]any{"trade_password": tp}); st != 404 && st != 405 {
			t.Fatalf("管别人账号的接口 %s 应该没有了：%d", path, st)
		}
	}
	// 第一个账号以后默认不能自己注册（放在云上时别人注册不进来）；在系统页里能打开
	e.api.S.SignupOpen = false
	e.api.S.reg = ""
	if st, r := e.client().do("POST", "/api/auth/register", map[string]string{"email": "stranger@example.com", "password": "password-123"}); st != 403 || code(r) != "REGISTRATION_CLOSED" {
		t.Fatalf("默认关着：%d %v", st, r)
	}
	// 打开注册要二级密码（放进来的人和你权限一样）
	if st, _ := b.do("POST", "/api/admin/registration", map[string]any{"mode": "open", "trade_password": tp}); st != 403 {
		t.Fatal("别的账号不能打开注册")
	}
	if st, _ := a.do("POST", "/api/admin/registration", map[string]any{"mode": "open"}); st == 200 {
		t.Fatal("不带二级密码不能打开注册")
	}
	if st, r := a.do("POST", "/api/admin/registration", map[string]any{"mode": "open", "trade_password": tp}); st != 200 {
		t.Fatalf("打开注册：%d %v", st, r)
	}
	if st, r := e.client().do("POST", "/api/auth/register", map[string]string{"email": "third@example.com", "password": "password-123"}); st != 200 {
		t.Fatalf("打开后能注册：%d %v", st, r)
	}
	// 关上不要二级密码：往安全的方向改不拦（11-16 走查）
	if st, r := a.do("POST", "/api/admin/registration", map[string]any{"mode": "admin"}); st != 200 {
		t.Fatalf("关注册不该要二级密码：%d %v", st, r)
	}
}

func TestRegistrationHaltAndLog(t *testing.T) {
	e := newEnv(t)
	own := e.user("owner@example.com")
	// 只能管理员添加：自己注册被拒，管理员加的人能登录（邮箱算已验证）
	if st, b := own.do("POST", "/api/admin/registration", map[string]any{"mode": "admin", "trade_password": tp}); st != 200 {
		t.Fatalf("改注册方式：%d %v", st, b)
	}
	if st, b := e.client().do("POST", "/api/auth/register", map[string]string{"email": "x@example.com", "password": "password-123"}); st != 403 || code(b) != "REGISTRATION_CLOSED" {
		t.Fatalf("关了注册还能注册：%d %v", st, b)
	}
	if st, b := own.do("POST", "/api/admin/users", map[string]any{"email": "new@example.com", "password": "initial-pass-1", "trade_password": tp}); st != 200 {
		t.Fatalf("添加用户：%d %v", st, b)
	}
	if st, b := e.client().do("POST", "/api/auth/login", map[string]string{"email": "new@example.com", "password": "initial-pass-1"}); st != 200 {
		t.Fatalf("添加的人登录：%d %v", st, b)
	}
	// 停机：停不要二级密码；第二档撤挂单；恢复要二级密码
	if st, b := own.do("POST", "/api/admin/halt", map[string]any{"level": 1, "reason": "交易所维护"}); st != 200 {
		t.Fatalf("第一档：%d %v", st, b)
	}
	if err := e.api.G.Check(5); err == nil || err.Error() != "SYSTEM_HALTED" {
		t.Fatalf("停机后还能开仓：%v", err)
	}
	if st, b := own.do("GET", "/api/halt", nil); st != 200 || b["halt"].(map[string]any)["level"].(float64) != 1 || b["halt"].(map[string]any)["reason"] != "交易所维护" {
		t.Fatalf("横幅读到的状态：%d %v", st, b)
	}
	if e.cancels != 0 {
		t.Fatal("第一档不该撤单")
	}
	if st, b := own.do("POST", "/api/admin/halt", map[string]any{"level": 2}); st != 200 || b["cancelled"].(float64) != 3 || e.cancels != 1 {
		t.Fatalf("第二档：%d %v", st, b)
	}
	if st, b := own.do("POST", "/api/admin/halt", map[string]any{"level": 0, "trade_password": "nope-nope"}); st != 403 {
		t.Fatalf("恢复没要二级密码：%d %v", st, b)
	}
	// 重启后停机还在
	e.api.G.setLevel(0)
	if err := e.api.Load(context.Background()); err != nil || e.api.G.Level() != 2 {
		t.Fatalf("重启后停机档位：%d %v", e.api.G.Level(), err)
	}
	if st, b := own.do("POST", "/api/admin/halt", map[string]any{"level": 0, "trade_password": tp}); st != 200 || e.api.G.Check(5) != nil {
		t.Fatalf("恢复：%d %v", st, b)
	}
	// 策略：恢复暂停的；没有这个策略说清楚
	if st, b := own.do("POST", "/api/admin/strategies/s1/resume", map[string]any{"trade_password": tp}); st != 200 || len(e.resumed) != 1 {
		t.Fatalf("恢复策略：%d %v", st, b)
	}
	if st, _ := own.do("POST", "/api/admin/strategies/nope/resume", map[string]any{"trade_password": tp}); st != 404 {
		t.Fatalf("没有的策略：%d", st)
	}
	// 操作记录：每一步都记了，改不了也删不了
	_, b := own.do("GET", "/api/admin", nil)
	acts := []string{}
	for _, x := range b["log"].([]any) {
		acts = append(acts, x.(map[string]any)["action"].(string))
	}
	want := []string{"strategy_resume", "halt", "halt", "halt", "user_add", "registration"}
	if len(acts) != len(want) {
		t.Fatalf("操作记录：%v", acts)
	}
	for i := range want {
		if acts[i] != want[i] {
			t.Fatalf("操作记录第 %d 条：%v", i, acts)
		}
	}
	if _, err := e.pool.Exec(context.Background(), `update admin_log set action = 'x'`); err == nil {
		t.Fatal("操作记录能改")
	}
	if _, err := e.pool.Exec(context.Background(), `delete from admin_log`); err == nil {
		t.Fatal("操作记录能删")
	}
}

// 删了一个账号，别的账号照常用（以前删所有者要把所有者交给别人，现在没有这回事）。
func TestDeleteAccount(t *testing.T) {
	e := newEnv(t)
	first := e.user("test-account@example.com")
	real := e.user("me@example.com")
	if st, b := first.do("POST", "/api/me/delete", map[string]string{"trade_password": tp}); st != 200 {
		t.Fatalf("删账户：%d %v", st, b)
	}
	if st, b := real.do("GET", "/api/admin", nil); st != 200 || len(b["users"].([]any)) != 1 {
		t.Fatalf("剩下的账号照常用系统页：%d %v", st, b)
	}
}
