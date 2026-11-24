package auth

// 验收标准模块 1（docs/12）的后端测试。
// 需要数据库的测试读环境变量 QS_TEST_DB（会清空里面的账户表），没设置就跳过。
//   QS_TEST_DB='postgres://qs@localhost:5433/qs_test?host=/var/tmp&sslmode=disable' go test ./internal/auth/

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ---------- 不需要数据库 ----------

func TestPasswordHash(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("格式不对：%s", h)
	}
	if strings.Contains(h, "correct horse") {
		t.Fatal("哈希里出现了明文")
	}
	if !CheckPassword("correct horse battery", h) || CheckPassword("correct horse batterY", h) || CheckPassword("", h) {
		t.Fatal("校验结果不对")
	}
	h2, _ := HashPassword("correct horse battery")
	if h == h2 {
		t.Fatal("同一个密码两次哈希应该不同（随机盐）")
	}
}

// 用 ethers.js v6.13.4 的 Wallet.signMessage 生成的真实签名（私钥是公开的 Hardhat 测试账户 0，没有任何资产）。11-23 声明改成 Temper 后重签。
const vecAddr = "0xf39fd6e51aad88f6f4ce6ab8827279cfffb92266"
const vecSig = "0x4ba69609c675ba3c2c0fb9ecdc97bb80cfdc8cdbdab17775ecbd500aec584e5718860bac14eb428591383b03645d84464ec933b059bee2cced8c837b7d8a1f1d1c"
const vecMsg = "localhost:8080 wants you to sign in with your Ethereum account:\n0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266\n\n" + SiweStatement + "\n\nURI: http://localhost:8080\nVersion: 1\nChain ID: 1\nNonce: TESTNONCE123\nIssued At: 2026-11-04T10:00:00Z\nExpiration Time: 2026-11-04T10:05:00Z"

func TestRecoverEthersVector(t *testing.T) {
	addr, err := RecoverAddress(vecMsg, vecSig)
	if err != nil || addr != vecAddr {
		t.Fatalf("恢复出 %s %v，应为 %s", addr, err, vecAddr)
	}
	m, err := ParseSiwe(vecMsg)
	if err != nil || m.Domain != "localhost:8080" || m.Address != vecAddr || m.Nonce != "TESTNONCE123" || m.Statement != SiweStatement || m.ChainID != "1" {
		t.Fatalf("解析不对：%+v %v", m, err)
	}
	if m.ExpiresAt.Sub(m.IssuedAt) != 5*time.Minute {
		t.Fatal("时间解析不对")
	}
	// 改一个字就恢复出别的地址
	other, _ := RecoverAddress(strings.Replace(vecMsg, "TESTNONCE123", "TESTNONCE124", 1), vecSig)
	if other == vecAddr {
		t.Fatal("消息被改了还能通过")
	}
	if _, err := RecoverAddress(vecMsg, vecSig[:20]); err == nil {
		t.Fatal("短签名应该报错")
	}
}

func TestParseSiweRejects(t *testing.T) {
	bad := []string{
		"",
		"hello",
		strings.Replace(vecMsg, "wants you to sign in", "wants you to sign", 1),
		strings.Replace(vecMsg, "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266", "0x1234", 1),
		strings.Replace(vecMsg, "Version: 1", "Version: 2", 1),
		strings.Replace(vecMsg, "Nonce: TESTNONCE123\n", "", 1),
		strings.Replace(vecMsg, "Issued At: 2026-11-04T10:00:00Z", "Issued At: yesterday", 1),
	}
	for i, m := range bad {
		if _, err := ParseSiwe(m); err == nil {
			t.Errorf("第 %d 条应该被拒绝", i)
		}
	}
}

// ---------- 测试用的钱包 ----------

type wallet struct {
	key  *secp256k1.PrivateKey
	addr string
}

func newWallet(t *testing.T) wallet {
	k, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	raw := k.PubKey().SerializeUncompressed()
	return wallet{k, "0x" + hex.EncodeToString(keccak(raw[1:])[12:])}
}

// sign 和钱包的 personal_sign 一样：r||s||v，v 为 27/28
func (w wallet) sign(msg string) string {
	digest := keccak([]byte("\x19Ethereum Signed Message:\n" + strconv.Itoa(len(msg)) + msg))
	c := ecdsa.SignCompact(w.key, digest, false) // [27+v][r][s]
	out := append(append([]byte{}, c[1:]...), c[0])
	return "0x" + hex.EncodeToString(out)
}

func siweMsg(domain, addr, nonce string, issued time.Time) string {
	return fmt.Sprintf("%s%s\n%s\n\n%s\n\nURI: http://%s\nVersion: 1\nChain ID: 1\nNonce: %s\nIssued At: %s\nExpiration Time: %s",
		domain, siweHeader, addr, SiweStatement, domain, nonce, issued.UTC().Format(time.RFC3339), issued.Add(5*time.Minute).UTC().Format(time.RFC3339))
}

func TestRecoverOwnSignature(t *testing.T) {
	for i := 0; i < 20; i++ {
		w := newWallet(t)
		msg := siweMsg("example.com", w.addr, "n"+strconv.Itoa(i), time.Now())
		if a, err := RecoverAddress(msg, w.sign(msg)); err != nil || a != w.addr {
			t.Fatalf("恢复出 %s %v，应为 %s", a, err, w.addr)
		}
	}
}

// ---------- 需要数据库：通过 HTTP 接口测 ----------

type env struct {
	t   *testing.T
	srv *httptest.Server
	svc *Service
	mu  sync.Mutex
	now time.Time
	log *bytes.Buffer
}

func (e *env) advance(d time.Duration) {
	e.mu.Lock()
	e.now = e.now.Add(d)
	e.mu.Unlock()
}

func newEnv(t *testing.T) *env {
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
	if _, err := pool.Exec(ctx, `drop table if exists sessions, email_codes, paper_accounts, users, recovery_codes, login_history, reset_codes cascade`); err != nil {
		t.Fatal(err)
	}
	buf := &bytes.Buffer{}
	e := &env{t: t, now: time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC), log: buf}
	svc := New(pool, slog.New(slog.NewTextHandler(buf, nil)))
	svc.DevMode = true
	svc.Now = func() time.Time { e.mu.Lock(); defer e.mu.Unlock(); return e.now }
	if err := svc.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	gate := &Gate{}
	gate.Open()
	mux := http.NewServeMux()
	svc.Routes(mux, gate)
	e.srv = httptest.NewServer(mux)
	e.svc = svc
	t.Cleanup(e.srv.Close)
	return e
}

type client struct {
	e    *env
	http *http.Client
}

func (e *env) client() *client {
	jar, _ := cookiejar.New(nil)
	return &client{e, &http.Client{Jar: jar}}
}

type resp struct {
	status int
	body   map[string]any
	raw    *http.Response
}

func (r resp) code() string { s, _ := r.body["code"].(string); return s }

func (c *client) do(method, path string, body any, hdr ...string) resp {
	c.e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.e.srv.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-QS-Client", "web")
	req.Header.Set("Origin", "http://localhost:8080")
	for i := 0; i+1 < len(hdr); i += 2 {
		if hdr[i+1] == "" {
			req.Header.Del(hdr[i])
		} else {
			req.Header.Set(hdr[i], hdr[i+1])
		}
	}
	res, err := c.http.Do(req)
	if err != nil {
		c.e.t.Fatal(err)
	}
	defer res.Body.Close()
	out := resp{status: res.StatusCode, raw: res, body: map[string]any{}}
	_ = json.NewDecoder(res.Body).Decode(&out.body)
	return out
}

func (c *client) must(method, path string, body any) resp {
	c.e.t.Helper()
	r := c.do(method, path, body)
	if r.status != 200 {
		c.e.t.Fatalf("%s %s → %d %v", method, path, r.status, r.body)
	}
	return r
}

func (c *client) expect(status int, code string, method, path string, body any, hdr ...string) resp {
	c.e.t.Helper()
	r := c.do(method, path, body, hdr...)
	if r.status != status || r.code() != code {
		c.e.t.Fatalf("%s %s → %d %q，应为 %d %q（%v）", method, path, r.status, r.code(), status, code, r.body)
	}
	return r
}

func userOf(r resp) map[string]any { u, _ := r.body["user"].(map[string]any); return u }

func (c *client) register(email string) resp {
	c.e.t.Helper()
	return c.must("POST", "/api/auth/register", map[string]string{"email": email, "password": "password-123", "lang": "zh"})
}

func (c *client) paperList() []map[string]any {
	c.e.t.Helper()
	r := c.must("GET", "/api/paper-accounts", nil)
	var out []map[string]any
	for _, a := range r.body["accounts"].([]any) {
		out = append(out, a.(map[string]any))
	}
	return out
}

func idOf(a map[string]any) string { return strconv.FormatInt(int64(a["id"].(float64)), 10) }

// 1.1 注册（D113 起是账户名 + 密码；账户名是邮箱的照常验证）
func TestRegister(t *testing.T) {
	e := newEnv(t)
	c := e.client()
	c.expect(400, "BAD_NAME", "POST", "/api/auth/register", map[string]string{"name": "a", "password": "password-123"})
	c.expect(400, "BAD_NAME", "POST", "/api/auth/register", map[string]string{"name": "has space", "password": "password-123"})
	c.expect(400, "BAD_NAME", "POST", "/api/auth/register", map[string]string{"name": strings.Repeat("x", 65), "password": "password-123"})
	c.expect(400, "WEAK_PASSWORD", "POST", "/api/auth/register", map[string]string{"name": "trader02", "password": "123456789"})
	// 账户名 + 密码（D113）：不要邮箱、不发验证码；不分大小写
	r := c.must("POST", "/api/auth/register", map[string]string{"name": "Trader01 ", "password": "password-123", "lang": "zh"})
	u := userOf(r)
	if u["name"] != "trader01" || u["email"] != nil || r.body["dev_code"] != nil || u["owner"] != true {
		t.Fatalf("用户信息不对：%v %v", u, r.body)
	}
	// 数据库里只有 argon2id 哈希
	var h string
	_ = e.svc.Pool.QueryRow(context.Background(), `select password_hash from users where name = 'trader01'`).Scan(&h)
	if !strings.HasPrefix(h, "$argon2id$") || strings.Contains(h, "password-123") {
		t.Fatalf("密码保存方式不对：%s", h)
	}
	// 同名（大小写不同）不能再注册；第二个注册的不是所有者
	e.client().expect(409, "NAME_TAKEN", "POST", "/api/auth/register", map[string]string{"name": "TRADER01", "password": "password-456"})
	if u := userOf(e.client().register("second")); u["owner"] != false {
		t.Fatalf("第二个账号不是所有者：%v", u)
	}
	e.client().must("POST", "/api/auth/login", map[string]string{"name": "TRADER01", "password": "password-123"})
	// 老写法（email 字段）当账户名用：检查脚本里的写法照样能用
	if u := userOf(e.client().register("old@example.com")); u["name"] != "old@example.com" || u["email"] != nil {
		t.Fatalf("老写法：%v", u)
	}
	// 没有自定义头的请求被拒绝（防跨站提交）
	e.client().expect(400, "BAD_REQUEST", "POST", "/api/auth/register", map[string]string{"name": "b-user", "password": "password-123"}, "X-QS-Client", "")
}

// 1.2 邮箱登录、会话 Cookie、退出、锁定
func TestLoginSessionLockout(t *testing.T) {
	e := newEnv(t)
	e.client().register("bob@example.com")
	c := e.client()
	c.expect(401, "NOT_LOGGED_IN", "GET", "/api/me", nil)
	r := c.must("POST", "/api/auth/login", map[string]string{"email": "bob@example.com", "password": "password-123"})
	var ck *http.Cookie
	for _, x := range r.raw.Cookies() {
		if x.Name == SessionCookie {
			ck = x
		}
	}
	if ck == nil || !ck.HttpOnly || ck.SameSite != http.SameSiteStrictMode || ck.MaxAge != 30*24*3600 || ck.Path != "/" || len(ck.Value) < 40 {
		t.Fatalf("会话 Cookie 不对：%+v", ck)
	}
	c.must("GET", "/api/me", nil)
	// 退出后服务端作废：拿旧令牌也不行
	c.must("POST", "/api/auth/logout", nil)
	c.expect(401, "NOT_LOGGED_IN", "GET", "/api/me", nil)
	old := e.client()
	u, _ := http.NewRequest("GET", e.srv.URL+"/api/me", nil)
	u.AddCookie(&http.Cookie{Name: SessionCookie, Value: ck.Value})
	res, _ := old.http.Do(u)
	if res.StatusCode != 401 {
		t.Fatal("退出后旧令牌还能用")
	}
	// 30 天后过期
	c.must("POST", "/api/auth/login", map[string]string{"email": "bob@example.com", "password": "password-123"})
	e.advance(29 * 24 * time.Hour)
	c.must("GET", "/api/me", nil)
	e.advance(2 * 24 * time.Hour)
	c.expect(401, "NOT_LOGGED_IN", "GET", "/api/me", nil)
	// 连续错 5 次锁 15 分钟，锁定期间正确密码也不行
	x := e.client()
	for i := 0; i < 5; i++ {
		x.expect(401, "BAD_LOGIN", "POST", "/api/auth/login", map[string]string{"email": "bob@example.com", "password": "wrong-password"})
	}
	x.expect(429, "LOCKED", "POST", "/api/auth/login", map[string]string{"email": "bob@example.com", "password": "password-123"})
	e.advance(14 * time.Minute)
	x.expect(429, "LOCKED", "POST", "/api/auth/login", map[string]string{"email": "bob@example.com", "password": "password-123"})
	e.advance(2 * time.Minute)
	x.must("POST", "/api/auth/login", map[string]string{"email": "bob@example.com", "password": "password-123"})
	// 不存在的邮箱和错误密码返回同样的错误（不暴露邮箱是否注册过）
	x.expect(401, "BAD_LOGIN", "POST", "/api/auth/login", map[string]string{"email": "nobody@example.com", "password": "password-123"})
}

// 1.3 邮箱验证码（D113 起在账户页绑定邮箱时发）
func TestEmailCode(t *testing.T) {
	e := newEnv(t)
	c := e.client()
	c.register("carol")
	r := c.must("POST", "/api/me/bind-email", map[string]string{"email": "carol@example.com"})
	code, _ := r.body["dev_code"].(string)
	if len(code) != 6 || !strings.Contains(e.log.String(), code) {
		t.Fatalf("开发模式下验证码要在接口和日志里：%q", code)
	}
	wrong := "000000"
	if code == wrong {
		wrong = "111111"
	}
	c.expect(400, "BAD_CODE", "POST", "/api/auth/verify-email", map[string]string{"code": wrong})
	// 过期
	e.advance(11 * time.Minute)
	c.expect(400, "BAD_CODE", "POST", "/api/auth/verify-email", map[string]string{"code": code})
	// 重新发
	code = c.must("POST", "/api/auth/resend-code", nil).body["dev_code"].(string)
	r = c.must("POST", "/api/auth/verify-email", map[string]string{"code": code})
	if userOf(r)["email_verified"] != true {
		t.Fatal("验证后状态没变")
	}
	// 用过就失效
	c.expect(400, "BAD_CODE", "POST", "/api/auth/verify-email", map[string]string{"code": code})
	// 猜码：错 5 次后锁定
	d := e.client()
	d.register("dave")
	d.must("POST", "/api/me/bind-email", map[string]string{"email": "dave@example.com"})
	for i := 0; i < 5; i++ {
		d.do("POST", "/api/auth/verify-email", map[string]string{"code": "99999" + strconv.Itoa(i)})
	}
	d.expect(429, "LOCKED", "POST", "/api/auth/verify-email", map[string]string{"code": "123456"})
}

func (c *client) siwe(w wallet, path string) resp {
	c.e.t.Helper()
	n := c.must("GET", "/api/auth/siwe/nonce", nil)
	if n.body["domain"] != "localhost:8080" {
		c.e.t.Fatalf("网址不对：%v", n.body)
	}
	msg := siweMsg("localhost:8080", w.addr, n.body["nonce"].(string), c.e.svc.now())
	return c.do("POST", path, map[string]string{"message": msg, "signature": w.sign(msg), "lang": "en"})
}

// 1.4 钱包登录

// 1.5 绑定和解绑
func TestBindUnbind(t *testing.T) {
	e := newEnv(t)
	// 钱包登录、绑定钱包的接口关掉了（D113）
	e.client().expect(404, "", "GET", "/api/auth/siwe/nonce", nil)
	c := e.client()
	c.register("erin")
	// 邮箱被别人绑了
	o := e.client()
	o.register("other")
	o.must("POST", "/api/me/bind-email", map[string]string{"email": "taken@example.com"})
	c.expect(409, "EMAIL_TAKEN", "POST", "/api/me/bind-email", map[string]string{"email": "taken@example.com"})
	c.expect(400, "BAD_EMAIL", "POST", "/api/me/bind-email", map[string]string{"email": "not-an-email"})
	// 已经有登录密码：绑邮箱不用再填密码，密码也不变
	r := c.must("POST", "/api/me/bind-email", map[string]string{"email": "Erin@Example.com"})
	if userOf(r)["email"] != "erin@example.com" || r.body["dev_code"] == nil {
		t.Fatalf("%v", r.body)
	}
	e.client().must("POST", "/api/auth/login", map[string]string{"name": "erin", "password": "password-123"})
	// 邮箱不能拿来登录（只有账户名能）
	e.client().expect(401, "BAD_LOGIN", "POST", "/api/auth/login", map[string]string{"name": "erin@example.com", "password": "password-123"})
	// 解绑邮箱：登录不受影响
	if r := c.must("POST", "/api/me/unbind", map[string]string{"kind": "email"}); userOf(r)["email"] != nil {
		t.Fatalf("解绑后没有邮箱：%v", r.body)
	}
	e.client().must("POST", "/api/auth/login", map[string]string{"name": "erin", "password": "password-123"})
	c.expect(400, "BAD_REQUEST", "POST", "/api/me/unbind", map[string]string{"kind": "phone"})
}

// 1.6 二级密码
func TestTradePassword(t *testing.T) {
	e := newEnv(t)
	c := e.client()
	c.register("frank@example.com")
	a := c.paperList()[0]
	// 重置模拟账户不再要二级密码（D103）
	c.must("POST", "/api/paper-accounts/"+idOf(a)+"/reset", map[string]string{})
	c.expect(400, "WEAK_PASSWORD", "POST", "/api/me/trade-password", map[string]string{"new": "12345"})
	c.expect(400, "SAME_AS_LOGIN", "POST", "/api/me/trade-password", map[string]string{"new": "password-123"})
	r := c.must("POST", "/api/me/trade-password", map[string]string{"new": "trade-pw-1"})
	if userOf(r)["has_trade_password"] != true {
		t.Fatal(r.body)
	}
	// 修改要旧的
	c.expect(403, "BAD_TRADE_PASSWORD", "POST", "/api/me/trade-password", map[string]string{"old": "nope", "new": "trade-pw-2"})
	c.must("POST", "/api/me/trade-password", map[string]string{"old": "trade-pw-1", "new": "trade-pw-2"})
	// 和登录密码分开：登录密码不能当二级密码
	pw := func(old string) map[string]string { return map[string]string{"old": old, "new": "trade-pw-3"} }
	c.expect(403, "BAD_TRADE_PASSWORD", "POST", "/api/me/trade-password", pw("password-123"))
	// 连续错 5 次锁 30 分钟（成功一次会清零；上面改密成功后又错了 1 次）
	for i := 0; i < 4; i++ {
		c.expect(403, "BAD_TRADE_PASSWORD", "POST", "/api/me/trade-password", pw("wrong"))
	}
	c.expect(429, "LOCKED", "POST", "/api/me/trade-password", pw("trade-pw-2"))
	e.advance(29 * time.Minute)
	c.expect(429, "LOCKED", "POST", "/api/me/trade-password", pw("trade-pw-2"))
	e.advance(2 * time.Minute)
	c.must("POST", "/api/me/trade-password", pw("trade-pw-2"))
}

// 1.7 模拟账户
func TestPaperAccounts(t *testing.T) {
	e := newEnv(t)
	var mu sync.Mutex
	var opened []int64
	e.svc.OnPaperAccount = func(_, id int64, initial string) {
		mu.Lock()
		opened = append(opened, id)
		mu.Unlock()
		if initial != "10000" {
			t.Errorf("初始资金 %s", initial)
		}
	}
	c := e.client()
	c.register("grace@example.com")
	l := c.paperList()
	if len(l) != 1 || l[0]["initial"] != "10000" || l[0]["name"] != "模拟账户 1" {
		t.Fatalf("注册要送一个 10,000 USDT 的模拟账户：%v", l)
	}
	for i := 2; i <= 10; i++ {
		c.must("POST", "/api/paper-accounts", map[string]string{"name": ""})
	}
	c.expect(400, "TOO_MANY_ACCOUNTS", "POST", "/api/paper-accounts", map[string]string{"name": "第 11 个"})
	l = c.paperList()
	if len(l) != 10 || l[9]["name"] != "模拟账户 10" {
		t.Fatalf("%v", l)
	}
	c.must("PATCH", "/api/paper-accounts/"+idOf(l[0]), map[string]string{"name": "  网格测试  "})
	c.expect(400, "BAD_REQUEST", "PATCH", "/api/paper-accounts/"+idOf(l[0]), map[string]string{"name": "   "})
	c.expect(400, "BAD_REQUEST", "PATCH", "/api/paper-accounts/"+idOf(l[0]), map[string]string{"name": strings.Repeat("长", 25)})
	if c.paperList()[0]["name"] != "网格测试" {
		t.Fatal("改名没生效")
	}
	// 重置：旧账户归档保留，新账户同名、新编号，总数不变
	c.must("POST", "/api/me/trade-password", map[string]string{"new": "trade-pw-1"})
	n := c.must("POST", "/api/paper-accounts/"+idOf(l[0])+"/reset", map[string]string{"trade_password": "trade-pw-1"})
	if n.body["name"] != "网格测试" || n.body["id"] == l[0]["id"] || n.body["initial"] != "10000" {
		t.Fatalf("%v", n.body)
	}
	if l2 := c.paperList(); len(l2) != 10 || l2[0]["name"] != "网格测试" || idOf(l2[0]) != idOf(n.body) {
		t.Fatal("重置后数量或位置变了（新账户应留在原来的位置）")
	}
	var archived int
	_ = e.svc.Pool.QueryRow(context.Background(), `select count(*) from paper_accounts where archived_at is not null`).Scan(&archived)
	if archived != 1 {
		t.Fatal("旧账户应该归档保留")
	}
	// 已归档的账户不能再改、再重置
	c.expect(404, "NOT_FOUND", "PATCH", "/api/paper-accounts/"+idOf(l[0]), map[string]string{"name": "x"})
	c.expect(404, "NOT_FOUND", "POST", "/api/paper-accounts/"+idOf(l[0])+"/reset", map[string]string{"trade_password": "trade-pw-1"})
	mu.Lock()
	if len(opened) != 11 {
		t.Fatalf("交易引擎开户回调 %d 次，应为 11", len(opened))
	}
	mu.Unlock()
	// 删除（D108）：归档、不新建；要通知各引擎停掉；至少留一个；不要二级密码
	var archivedIDs []int64
	e.svc.OnPaperArchive = func(_, id int64) { archivedIDs = append(archivedIDs, id) }
	l = c.paperList()
	c.must("DELETE", "/api/paper-accounts/"+idOf(l[1]), nil)
	if l2 := c.paperList(); len(l2) != 9 || idOf(l2[0]) != idOf(l[0]) || idOf(l2[1]) != idOf(l[2]) {
		t.Fatalf("删除后列表不对：%v", l2)
	}
	if len(archivedIDs) != 1 || fmt.Sprint(archivedIDs[0]) != idOf(l[1]) {
		t.Fatalf("删除要通知各引擎（机器人、跟随、挂单）：%v", archivedIDs)
	}
	c.expect(404, "NOT_FOUND", "DELETE", "/api/paper-accounts/"+idOf(l[1]), nil) // 已经删了
	for _, a := range c.paperList()[1:] {
		c.must("DELETE", "/api/paper-accounts/"+idOf(a), nil)
	}
	c.expect(400, "LAST_PAPER_ACCOUNT", "DELETE", "/api/paper-accounts/"+idOf(l[0]), nil)
	if len(c.paperList()) != 1 {
		t.Fatal("至少留一个")
	}
}

// 1.8 跨用户访问一律 404
func TestCrossUser(t *testing.T) {
	e := newEnv(t)
	a := e.client()
	a.register("alice@example.com")
	a.must("POST", "/api/me/trade-password", map[string]string{"new": "trade-pw-a"})
	acc := idOf(a.paperList()[0])
	b := e.client()
	b.register("bob@example.com")
	b.must("POST", "/api/me/trade-password", map[string]string{"new": "trade-pw-b"})
	b.expect(404, "NOT_FOUND", "PATCH", "/api/paper-accounts/"+acc, map[string]string{"name": "偷改"})
	b.expect(404, "NOT_FOUND", "POST", "/api/paper-accounts/"+acc+"/reset", map[string]string{"trade_password": "trade-pw-b"})
	b.expect(404, "NOT_FOUND", "POST", "/api/paper-accounts/999999/reset", map[string]string{"trade_password": "trade-pw-b"})
	b.expect(404, "NOT_FOUND", "PATCH", "/api/paper-accounts/abc", map[string]string{"name": "x"})
	b.must("POST", "/api/paper-accounts", map[string]string{"name": ""}) // 自己有两个，删别人的也不行
	b.expect(404, "NOT_FOUND", "DELETE", "/api/paper-accounts/"+acc, nil)
	for _, x := range b.paperList() {
		if idOf(x) == acc {
			t.Fatal("B 的列表里出现了 A 的账户")
		}
	}
	if a.paperList()[0]["name"] != "模拟账户 1" {
		t.Fatal("A 的账户被改了")
	}
	// 猜别人账户编号不会消耗自己的二级密码次数（先查归属）
	for i := 0; i < 6; i++ {
		b.expect(404, "NOT_FOUND", "POST", "/api/paper-accounts/"+acc+"/reset", map[string]string{"trade_password": "wrong"})
	}
	b.must("POST", "/api/paper-accounts/"+idOf(b.paperList()[0])+"/reset", map[string]string{"trade_password": "trade-pw-b"})
	// 没登录：所有需要登录的接口都是 401
	anon := e.client()
	for _, p := range [][2]string{{"GET", "/api/me"}, {"GET", "/api/paper-accounts"}, {"POST", "/api/paper-accounts"}, {"PATCH", "/api/paper-accounts/" + acc}, {"POST", "/api/paper-accounts/" + acc + "/reset"},
		{"POST", "/api/me/lang"}, {"POST", "/api/me/trade-password"}, {"POST", "/api/me/bind-email"}, {"POST", "/api/me/unbind"}, {"POST", "/api/auth/verify-email"}, {"POST", "/api/auth/resend-code"}} {
		anon.expect(401, "NOT_LOGGED_IN", p[0], p[1], map[string]string{})
	}
}

func TestLangAndGate(t *testing.T) {
	e := newEnv(t)
	c := e.client()
	c.register("henry@example.com")
	if userOf(c.must("POST", "/api/me/lang", map[string]string{"lang": "en"}))["lang"] != "en" {
		t.Fatal("语言没保存")
	}
	if userOf(c.must("POST", "/api/me/lang", map[string]string{"lang": "fr"}))["lang"] != "zh" {
		t.Fatal("不支持的语言应回到中文")
	}
	// 数据库没准备好时返回 503
	mux := http.NewServeMux()
	e.svc.Routes(mux, &Gate{})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	res, _ := http.Get(srv.URL + "/api/me")
	if res.StatusCode != 503 {
		t.Fatal(res.StatusCode)
	}
}

// 1.11 改登录密码：要旧密码；改完其他设备的登录失效，这台不受影响
func TestChangePassword(t *testing.T) {
	e := newEnv(t)
	a := e.client()
	a.register("change@example.com")
	b := e.client()
	b.must("POST", "/api/auth/login", map[string]string{"email": "change@example.com", "password": "password-123"})
	a.expect(401, "BAD_LOGIN", "POST", "/api/me/password", map[string]string{"old": "wrong-password", "new": "new-password-1"})
	a.expect(400, "WEAK_PASSWORD", "POST", "/api/me/password", map[string]string{"old": "password-123", "new": "short"})
	a.must("POST", "/api/me/password", map[string]string{"old": "password-123", "new": "new-password-1"})
	a.must("GET", "/api/me", nil)
	b.expect(401, "NOT_LOGGED_IN", "GET", "/api/me", nil)
	c := e.client()
	c.expect(401, "BAD_LOGIN", "POST", "/api/auth/login", map[string]string{"email": "change@example.com", "password": "password-123"})
	c.must("POST", "/api/auth/login", map[string]string{"email": "change@example.com", "password": "new-password-1"})
}

// 1.14 删除账户：要二级密码；删完登录失效，同一个邮箱可以重新注册，拿到新账户
func TestDeleteUser(t *testing.T) {
	e := newEnv(t)
	var archived, deleted []int64
	e.svc.OnPaperArchive = func(_, id int64) { archived = append(archived, id) }
	e.svc.OnUserDelete = func(_ context.Context, _ int64, ids []int64) error { deleted = ids; return nil }
	c := e.client()
	c.register("delete@example.com")
	c.expect(403, "NEED_TRADE_PASSWORD", "POST", "/api/me/delete", map[string]string{"trade_password": "x"})
	c.must("POST", "/api/me/trade-password", map[string]string{"new": "trade-pw-1"})
	c.expect(403, "BAD_TRADE_PASSWORD", "POST", "/api/me/delete", map[string]string{"trade_password": "nope"})
	// 空着提交只提示要填，不算输错：连按 6 次也不锁，之后填对照样能用（11-16 走查）
	for i := 0; i < 6; i++ {
		c.expect(400, "TRADE_PASSWORD_EMPTY", "POST", "/api/me/delete", map[string]string{"trade_password": ""})
	}
	id0 := idOf(c.paperList()[0])
	c.must("POST", "/api/me/delete", map[string]string{"trade_password": "trade-pw-1"})
	if len(archived) != 1 || len(deleted) != 1 {
		t.Fatalf("要通知引擎并删记录：%v %v", archived, deleted)
	}
	c.expect(401, "NOT_LOGGED_IN", "GET", "/api/me", nil)
	d := e.client()
	d.expect(401, "BAD_LOGIN", "POST", "/api/auth/login", map[string]string{"email": "delete@example.com", "password": "password-123"})
	d.register("delete@example.com")
	if idOf(d.paperList()[0]) == id0 {
		t.Fatal("重新注册要是新账户")
	}
}

// 带来源地址的客户端：测试服务器在本机，后端信 X-Real-IP（和 nginx 转发时一样）
type ipRT struct {
	ip string
	rt http.RoundTripper
}

func (x ipRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set("X-Real-IP", x.ip)
	return x.rt.RoundTrip(r)
}

func (e *env) clientFrom(ip string) *client {
	c := e.client()
	c.http.Transport = ipRT{ip, http.DefaultTransport}
	return c
}

// 别人在别处连输错密码，锁的是他那边，正主从别处照样能登录（审查：以前 5 次就锁整个账号）；
// 同一来源对很多账号乱试（撞库）也会被锁。
func TestLoginLockoutPerSource(t *testing.T) {
	e := newEnv(t)
	e.client().register("carol@example.com")
	ok := map[string]string{"email": "carol@example.com", "password": "password-123"}
	bad := e.clientFrom("203.0.113.5")
	for i := 0; i < 5; i++ {
		bad.expect(401, "BAD_LOGIN", "POST", "/api/auth/login", map[string]string{"email": "carol@example.com", "password": "wrong-password"})
	}
	bad.expect(429, "LOCKED", "POST", "/api/auth/login", ok)
	e.clientFrom("198.51.100.7").must("POST", "/api/auth/login", ok)
	spray := e.clientFrom("203.0.113.9")
	for i := 0; i < loginIPFails; i++ {
		spray.expect(401, "BAD_LOGIN", "POST", "/api/auth/login", map[string]string{"email": "user" + strconv.Itoa(i) + "@example.com", "password": "password-123"})
	}
	spray.expect(429, "LOCKED", "POST", "/api/auth/login", ok)
	e.advance(16 * time.Minute)
	spray.must("POST", "/api/auth/login", ok)
}

// 同一来源一小时最多注册 10 个账号。
func TestSignupLimitPerSource(t *testing.T) {
	e := newEnv(t)
	c := e.clientFrom("203.0.113.20")
	reg := func(i int) map[string]string {
		return map[string]string{"email": "s" + strconv.Itoa(i) + "@example.com", "password": "password-123"}
	}
	for i := 0; i < SignupPerIPHour; i++ {
		c.must("POST", "/api/auth/register", reg(i))
	}
	c.expect(429, "SIGNUP_LIMIT", "POST", "/api/auth/register", reg(100))
	e.clientFrom("203.0.113.21").must("POST", "/api/auth/register", reg(101))
	e.advance(61 * time.Minute)
	c.must("POST", "/api/auth/register", reg(102))
}
