package auth

import (
	"strings"
	"testing"
	"time"
)

func codeAt(t *testing.T, secret string, at time.Time) string {
	key, err := b32.DecodeString(secret)
	if err != nil {
		t.Fatal(err)
	}
	return totpCode(key, at.Unix()/totpStep)
}

// RFC 6238 附录 B 的测试向量（SHA1，密钥 "12345678901234567890"）
func TestTOTPVector(t *testing.T) {
	key := []byte("12345678901234567890")
	for _, c := range []struct {
		t    int64
		want string
	}{{59, "287082"}, {1111111109, "081804"}, {1234567890, "005924"}, {2000000000, "279037"}} {
		if got := totpCode(key, c.t/30); got != c.want {
			t.Fatalf("t=%d：%s，应该是 %s", c.t, got, c.want)
		}
	}
}

func TestTwoFactor(t *testing.T) {
	e := newEnv(t)
	var alerts []string
	e.svc.OnNewDevice = func(_ int64, dev, _ string) { alerts = append(alerts, dev) }
	c := e.client()
	c.register("tfa@example.com")
	// 设置：先拿密钥，用验证器上的数确认，给 8 个恢复码
	r := c.must("POST", "/api/me/2fa/setup", nil)
	secret, _ := r.body["secret"].(string)
	if !strings.HasPrefix(r.body["uri"].(string), "otpauth://totp/") || len(secret) != 32 {
		t.Fatalf("设置：%v", r.body)
	}
	c.expect(401, "BAD_2FA", "POST", "/api/me/2fa/enable", map[string]string{"code": "000000"})
	r = c.must("POST", "/api/me/2fa/enable", map[string]string{"code": codeAt(t, secret, e.svc.now())})
	codes, _ := r.body["recovery"].([]any)
	if len(codes) != 8 {
		t.Fatalf("恢复码：%v", r.body)
	}
	if me := c.must("GET", "/api/me", nil); me.body["user"].(map[string]any)["two_fa"] != true {
		t.Fatal("开了以后 /api/me 里 two_fa 应该是 true")
	}
	// 登录：密码对了先给票，不给会话
	d := e.client()
	r = d.do("POST", "/api/auth/login", map[string]string{"email": "tfa@example.com", "password": "password-123"}, "User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) AppleWebKit/605 Version/17.0 Safari/605")
	if r.status != 200 {
		t.Fatalf("登录：%d %v", r.status, r.body)
	}
	ticket, _ := r.body["ticket"].(string)
	if r.body["need_2fa"] != true || ticket == "" {
		t.Fatalf("开了二次验证还直接登录了：%v", r.body)
	}
	d.expect(401, "NOT_LOGGED_IN", "GET", "/api/me", nil)
	// 刚才确认用的那个数不能再用（同一个时间步）
	d.expect(401, "BAD_2FA", "POST", "/api/auth/2fa", map[string]string{"ticket": ticket, "code": codeAt(t, secret, e.svc.now())})
	e.advance(30 * time.Second)
	// 同一个浏览器：两步的 User-Agent 一样
	if r = d.do("POST", "/api/auth/2fa", map[string]string{"ticket": ticket, "code": codeAt(t, secret, e.svc.now())}, "User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) AppleWebKit/605 Version/17.0 Safari/605"); r.status != 200 {
		t.Fatalf("第二步：%d %v", r.status, r.body)
	}
	d.must("GET", "/api/me", nil)
	if len(alerts) != 1 || alerts[0] != "Safari · macOS" {
		t.Fatalf("新设备登录提醒：%v", alerts)
	}
	// 恢复码能登录一次，用过作废
	f := e.client()
	r = f.must("POST", "/api/auth/login", map[string]string{"email": "tfa@example.com", "password": "password-123"})
	rc := codes[0].(string)
	f.must("POST", "/api/auth/2fa", map[string]string{"ticket": r.body["ticket"].(string), "code": rc})
	g := e.client()
	r = g.must("POST", "/api/auth/login", map[string]string{"email": "tfa@example.com", "password": "password-123"})
	g.expect(401, "BAD_2FA", "POST", "/api/auth/2fa", map[string]string{"ticket": r.body["ticket"].(string), "code": rc})
	// 票错 5 次作废
	for i := 0; i < 4; i++ {
		g.expect(401, "BAD_2FA", "POST", "/api/auth/2fa", map[string]string{"ticket": r.body["ticket"].(string), "code": "111111"})
	}
	g.expect(401, "2FA_EXPIRED", "POST", "/api/auth/2fa", map[string]string{"ticket": r.body["ticket"].(string), "code": "111111"})
	// 登录设备：4 个（注册的、Safari、恢复码那个……），退出别的只剩当前
	sec := c.must("GET", "/api/me/security", nil)
	devs, _ := sec.body["devices"].([]any)
	if len(devs) < 3 || devs[0].(map[string]any)["current"] != true || sec.body["recovery_left"] != float64(7) {
		t.Fatalf("设备和恢复码：%v", sec.body)
	}
	other := devs[1].(map[string]any)["id"].(string)
	c.must("POST", "/api/me/devices/"+other+"/revoke", nil)
	c.must("POST", "/api/me/devices/revoke-others", nil)
	if sec = c.must("GET", "/api/me/security", nil); len(sec.body["devices"].([]any)) != 1 {
		t.Fatalf("退出别的设备后：%v", sec.body["devices"])
	}
	d.expect(401, "NOT_LOGGED_IN", "GET", "/api/me", nil)
	// 防钓鱼码
	c.must("POST", "/api/me/anti-phish", map[string]string{"code": "蓝色海豚"})
	if me := c.must("GET", "/api/me", nil); me.body["user"].(map[string]any)["anti_phish"] != "蓝色海豚" {
		t.Fatal("防钓鱼码没存上")
	}
	// 关掉要有效的数
	c.expect(401, "BAD_2FA", "POST", "/api/me/2fa/disable", map[string]string{"code": "123456"})
	e.advance(30 * time.Second)
	c.must("POST", "/api/me/2fa/disable", map[string]string{"code": codeAt(t, secret, e.svc.now())})
	h := e.client()
	if r = h.must("POST", "/api/auth/login", map[string]string{"email": "tfa@example.com", "password": "password-123"}); r.body["need_2fa"] == true {
		t.Fatal("关了还要验证码")
	}
}
