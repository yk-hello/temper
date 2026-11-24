package auth

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// capMail 记下"发出去"的信（测试用，不真的发）。
type capMail struct {
	mu   sync.Mutex
	sent []struct{ to, subject, body string }
}

func (m *capMail) Enabled() bool { return true }
func (m *capMail) Send(_ context.Context, to, subject, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, struct{ to, subject, body string }{to, subject, body})
	return nil
}

// wait 等发给 to、标题里有 want 的信（发信在后台），返回信里的 6 位验证码。
func (m *capMail) wait(t *testing.T, to, want string) (string, string) {
	t.Helper()
	for i := 0; i < 100; i++ {
		m.mu.Lock()
		for _, s := range m.sent {
			if s.to == to && strings.Contains(s.subject, want) {
				m.mu.Unlock()
				return regexp.MustCompile(`\d{6}`).FindString(s.body), s.subject
			}
		}
		m.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("没等到发给 %s 的「%s」", to, want)
	return "", ""
}

func (m *capMail) count(to string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, s := range m.sent {
		if s.to == to {
			n++
		}
	}
	return n
}

// D100、1.12、1.13：配了发信邮箱、关了开发模式时的整个流程。
func TestMailRecover(t *testing.T) {
	e := newEnv(t)
	// 没配发信邮箱、又不是开发模式：忘记密码说清楚用不了
	e.svc.DevMode = false
	e.client().expect(400, "MAIL_OFF", "POST", "/api/auth/forgot", map[string]string{"name": "a-user"})
	m := &capMail{}
	e.svc.Mail = m

	// 注册只要账户名（D113）；在账户页绑定邮箱：验证码真的发出去，页面上不再给
	c := e.client()
	c.register("gina")
	r := c.must("POST", "/api/me/bind-email", map[string]string{"email": "gina@example.com"})
	if r.body["dev_code"] != nil && r.body["dev_code"] != "" {
		t.Fatalf("关了开发模式还把验证码给页面：%v", r.body)
	}
	code, _ := m.wait(t, "gina@example.com", "邮箱验证码")
	c.must("POST", "/api/auth/verify-email", map[string]string{"code": code})
	c.must("POST", "/api/me/anti-phish", map[string]string{"code": "蓝色海豚"})

	// 忘记密码（输账户名，D113）：账户名有没有注册，回复一样；没注册的不发信
	anon := e.client()
	a := anon.must("POST", "/api/auth/forgot", map[string]string{"name": "nobody"})
	b := anon.must("POST", "/api/auth/forgot", map[string]string{"name": "gina"})
	if a.body["ok"] != true || b.body["ok"] != true || a.body["dev_code"] != b.body["dev_code"] {
		t.Fatalf("回复不一样会暴露邮箱是否注册过：%v %v", a.body, b.body)
	}
	code, subj := m.wait(t, "gina@example.com", "重设登录密码")
	if !strings.HasPrefix(subj, "【蓝色海豚】") {
		t.Fatalf("标题开头要有防钓鱼码：%s", subj)
	}
	if m.count("nobody@example.com") != 0 {
		t.Fatal("没注册的邮箱不该收到信")
	}
	// 错的验证码；没注册的邮箱和验证码不对回复一样
	anon.expect(400, "BAD_CODE", "POST", "/api/auth/reset", map[string]string{"name": "gina", "code": "000000", "password": "new-password-1"})
	anon.expect(400, "BAD_CODE", "POST", "/api/auth/reset", map[string]string{"name": "nobody", "code": code, "password": "new-password-1"})
	anon.expect(400, "WEAK_PASSWORD", "POST", "/api/auth/reset", map[string]string{"name": "gina", "code": code, "password": "short"})
	anon.must("POST", "/api/auth/reset", map[string]string{"name": "gina", "code": code, "password": "new-password-1"})
	// 同一个验证码不能再用；原来登录着的设备都退出了；新密码能登录，旧的不行
	anon.expect(400, "BAD_CODE", "POST", "/api/auth/reset", map[string]string{"name": "gina", "code": code, "password": "new-password-2"})
	c.expect(401, "NOT_LOGGED_IN", "GET", "/api/me", nil)
	m.wait(t, "gina@example.com", "登录密码已重设")
	d := e.client()
	d.expect(401, "BAD_LOGIN", "POST", "/api/auth/login", map[string]string{"name": "gina", "password": "password-123"})
	d.must("POST", "/api/auth/login", map[string]string{"name": "gina", "password": "new-password-1"})

	// 错 5 次：这个验证码作废
	anon.must("POST", "/api/auth/forgot", map[string]string{"name": "gina"})
	time.Sleep(50 * time.Millisecond)
	m.mu.Lock()
	last := m.sent[len(m.sent)-1].body
	m.mu.Unlock()
	good := regexp.MustCompile(`\d{6}`).FindString(last)
	for i := 0; i < 3; i++ { // 上面"同一个验证码再用"已经算错了 1 次
		anon.expect(400, "BAD_CODE", "POST", "/api/auth/reset", map[string]string{"name": "gina", "code": "111111", "password": "new-password-3"})
	}
	anon.expect(400, "BAD_CODE", "POST", "/api/auth/reset", map[string]string{"name": "gina", "code": "111111", "password": "new-password-3"})
	anon.expect(429, "LOCKED", "POST", "/api/auth/reset", map[string]string{"name": "gina", "code": good, "password": "new-password-3"})
	// 同一个邮箱 15 分钟最多 3 次（上面已经 2 次）
	anon.must("POST", "/api/auth/forgot", map[string]string{"name": "gina"})
	anon.expect(429, "TOO_OFTEN", "POST", "/api/auth/forgot", map[string]string{"name": "gina"})
	e.advance(16 * time.Minute)

	d.must("POST", "/api/me/trade-password", map[string]string{"new": "trade-pw-3"})

	// 本机命令清空二级密码：所有设备退出；重新登录后设新的不用旧的
	if err := e.svc.ClearTradePassword(context.Background(), int64(userOf(d.must("GET", "/api/me", nil))["id"].(float64))); err != nil {
		t.Fatal(err)
	}
	d.expect(401, "NOT_LOGGED_IN", "GET", "/api/me", nil)
	d.must("POST", "/api/auth/login", map[string]string{"name": "gina", "password": "new-password-1"})
	d.must("POST", "/api/me/trade-password", map[string]string{"new": "trade-pw-4"})
}

// 开发模式（没配发信邮箱时的默认）：验证码直接给页面，方便本机试用；英文用户收英文
func TestMailDevAndEnglish(t *testing.T) {
	e := newEnv(t)
	c := e.client()
	c.must("POST", "/api/auth/register", map[string]string{"name": "hank", "password": "password-123", "lang": "en"})
	// 没绑邮箱：忘记密码和成功一样回复，但不给验证码（没地方发）
	if r := e.client().must("POST", "/api/auth/forgot", map[string]string{"name": "hank"}); r.body["dev_code"] != nil && r.body["dev_code"] != "" {
		t.Fatalf("没绑邮箱不该有验证码：%v", r.body)
	}
	b := c.must("POST", "/api/me/bind-email", map[string]string{"email": "hank@example.com"})
	c.must("POST", "/api/auth/verify-email", map[string]string{"code": b.body["dev_code"].(string)})
	r := e.client().must("POST", "/api/auth/forgot", map[string]string{"name": "hank"})
	code, _ := r.body["dev_code"].(string)
	if len(code) != 6 {
		t.Fatalf("开发模式要把验证码给页面：%v", r.body)
	}
	e.client().must("POST", "/api/auth/reset", map[string]string{"name": "hank", "code": code, "password": "new-password-9"})
	e.client().must("POST", "/api/auth/login", map[string]string{"name": "hank", "password": "new-password-9"})
	subj, body := mailText("en", "login_done", "", "")
	if !strings.Contains(subj, "sign-in password") || strings.ContainsAny(body, "，。") {
		t.Fatalf("英文邮件：%s / %s", subj, body)
	}
}

// 11-16 走查：没配发信邮箱的普通部署（AUTH_DEV 没写，自动的开发模式）。注册直接算邮箱已验证（验证码只会显示在页面上，验证不了什么）；
// 找回登录密码、二级密码的验证码不能给页面：不然谁能打开网页，填上别人的邮箱就能改掉别人的密码。
func TestNoMailRecoverNeedsLocalCommand(t *testing.T) {
	e := newEnv(t)
	e.svc.DevMode, e.svc.DevAuto = true, true // 和 main.go 里没写 AUTH_DEV、也没配发信邮箱时一样
	c := e.client()
	c.register("ivy")
	r := c.must("POST", "/api/me/bind-email", map[string]string{"email": "ivy@example.com"})
	// 绑定邮箱：验证码只会显示在页面上，验证证明不了什么，直接算已验证
	if r.body["dev_code"] != nil || r.body["user"].(map[string]any)["email_verified"] != true {
		t.Fatalf("没配发信邮箱时绑定邮箱直接算已验证、不给验证码：%v", r.body)
	}
	if m := e.client().must("GET", "/api/auth/mail", nil); m.body["dev"] != false || m.body["mail"] != false {
		t.Fatalf("找回的验证码不该显示在页面上：%v", m.body)
	}
	got := e.client().expect(400, "MAIL_OFF", "POST", "/api/auth/forgot", map[string]string{"name": "ivy"})
	if got.body["dev_code"] != nil {
		t.Fatalf("忘记密码把验证码给了页面：%v", got.body)
	}
	// 二级密码不用邮件：输登录密码就能重置（D107），没配发信邮箱也一样
	c.must("POST", "/api/me/trade-password", map[string]string{"new": "trade-pw-77"})
	c.must("POST", "/api/me/trade-password/reset", map[string]string{"password": "password-123", "new": "trade-pw-78"})
}

// D107：忘了二级密码，输一遍登录密码（开了二次验证的再输验证器上的数）就能设新的。
// 代价：所有设备退出（包括这台）、通知主程序（交易所账户回到"未开启"、发提醒）。登录密码错了算错次数，错满锁 15 分钟
func TestTradePasswordResetByLogin(t *testing.T) {
	e := newEnv(t)
	var hooked []int64
	e.svc.OnTradePwReset = func(u int64) { hooked = append(hooked, u) }
	c := e.client()
	c.register("jill@example.com")
	other := e.client()
	other.must("POST", "/api/auth/login", map[string]string{"email": "jill@example.com", "password": "password-123"})
	c.must("POST", "/api/me/trade-password", map[string]string{"new": "trade-pw-1"})

	c.expect(400, "SAME_AS_LOGIN", "POST", "/api/me/trade-password/reset", map[string]string{"password": "password-123", "new": "password-123"})
	c.expect(400, "WEAK_PASSWORD", "POST", "/api/me/trade-password/reset", map[string]string{"password": "password-123", "new": "123"})
	c.expect(401, "BAD_LOGIN", "POST", "/api/me/trade-password/reset", map[string]string{"password": "wrong-pass-1", "new": "trade-pw-2"})
	if len(hooked) != 0 {
		t.Fatal("没重置成功不能通知")
	}
	c.must("POST", "/api/me/trade-password/reset", map[string]string{"password": "password-123", "new": "trade-pw-2"})
	if len(hooked) != 1 {
		t.Fatalf("重置后要通知主程序（暂停交易所账户、发提醒）：%v", hooked)
	}
	// 所有设备退出，包括这台
	c.expect(401, "NOT_LOGGED_IN", "GET", "/api/me", nil)
	other.expect(401, "NOT_LOGGED_IN", "GET", "/api/me", nil)
	// 重新登录：新的二级密码能用，旧的不行
	c.must("POST", "/api/auth/login", map[string]string{"email": "jill@example.com", "password": "password-123"})
	c.expect(403, "BAD_TRADE_PASSWORD", "POST", "/api/me/trade-password", map[string]string{"old": "trade-pw-1", "new": "trade-pw-3"})
	c.must("POST", "/api/me/trade-password", map[string]string{"old": "trade-pw-2", "new": "trade-pw-3"})

	// 开了二次验证：还要验证器上的数
	r := c.must("POST", "/api/me/2fa/setup", nil)
	secret, _ := r.body["secret"].(string)
	c.must("POST", "/api/me/2fa/enable", map[string]string{"code": codeAt(t, secret, e.svc.now())})
	e.advance(31 * time.Second)
	c.expect(401, "BAD_2FA", "POST", "/api/me/trade-password/reset", map[string]string{"password": "password-123", "new": "trade-pw-4"})
	c.must("POST", "/api/me/trade-password/reset", map[string]string{"password": "password-123", "code": codeAt(t, secret, e.svc.now()), "new": "trade-pw-4"})
	if len(hooked) != 2 {
		t.Fatalf("第二次重置也要通知：%v", hooked)
	}

	// 登录密码连续错 5 次：锁 15 分钟，填对也不行
	d := e.client()
	d.register("kate@example.com")
	for i := 0; i < 5; i++ {
		d.expect(401, "BAD_LOGIN", "POST", "/api/me/trade-password/reset", map[string]string{"password": "wrong-pass-1", "new": "trade-pw-9"})
	}
	d.expect(429, "LOCKED", "POST", "/api/me/trade-password/reset", map[string]string{"password": "password-123", "new": "trade-pw-9"})
}
