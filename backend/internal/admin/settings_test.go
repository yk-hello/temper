package admin

import (
	"context"
	"strings"
	"testing"

	"qs/internal/auth"
)

// fakeCrypt 测试用的"加密"：前面加个记号再反转，能看出存进去的不是明文。
type fakeCrypt struct{}

func (fakeCrypt) Enabled() bool { return true }
func (fakeCrypt) Seal(p string) ([]byte, error) {
	r := []rune("box:" + p)
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
	return []byte(string(r)), nil
}
func (fakeCrypt) Open(b []byte) (string, error) {
	r := []rune(string(b))
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
	return strings.TrimPrefix(string(r), "box:"), nil
}

// 2026-11-15：系统设置（发信邮箱、Telegram、数据源密钥；AI 厂商 11-23 起单独一张表，见 aiproviders_test.go）在"系统"页填。登录的账号都能改（D104），要二级密码；
// 密钥加密存、接口只给头尾几位；填错了拒；网页里填的优先，清掉回到配置文件的；改了哪一组就通知哪一组。
func TestSystemSettings(t *testing.T) {
	e := newEnv(t)
	env := map[string]string{"SMTP_HOST": "smtp.env.example", "X_ACCOUNTS": "env-acct"}
	applied := map[string]int{}
	e.api.Sys = &Sys{S: e.api.S, C: fakeCrypt{}, Env: func(n string) string { return env[n] }, Apply: func(g string) { applied[g]++ },
		Test: func(_ context.Context, g string, _ *auth.User) (string, error) { return "测试过了：" + g, nil }}
	owner := e.user("owner@example.com")
	other := e.user("other@example.com")
	if st, _ := other.do("GET", "/api/admin/settings", nil); st != 403 {
		t.Fatalf("D113：只有所有者能看系统设置（里面是整套系统的密钥）：%d", st)
	}
	if st, _ := e.client().do("GET", "/api/admin/settings", nil); st != 401 {
		t.Fatalf("没登录不能看：%d", st)
	}
	// 没填时：值来自配置文件
	_, b := owner.do("GET", "/api/admin/settings", nil)
	if f := field(b, "mail.host"); f["source"] != "env" || f["value"] != "smtp.env.example" {
		t.Fatalf("没填时用配置文件的：%v", f)
	}
	// 二级密码不对：拒
	if st, _ := owner.do("POST", "/api/admin/settings", map[string]any{"values": map[string]string{"mail.host": "smtp.gmail.com"}, "trade_password": "wrong"}); st == 200 {
		t.Fatal("二级密码不对应该拒")
	}
	// 填错的端口、邮箱：拒
	if st, b := owner.do("POST", "/api/admin/settings", map[string]any{"values": map[string]string{"mail.port": "8080"}, "trade_password": tp}); st != 400 || b["code"] != "BAD_SETTING" {
		t.Fatalf("端口不对应该拒：%d %v", st, b)
	}
	// 邮箱少了 .com 也拒（真实发生过：填成 xx@gmail）
	if st, b := owner.do("POST", "/api/admin/settings", map[string]any{"values": map[string]string{"mail.user": "me@gmail"}, "trade_password": tp}); st != 400 || b["code"] != "BAD_SETTING" {
		t.Fatalf("邮箱不完整应该拒：%d %v", st, b)
	}
	// 填好：Gmail 的应用专用密码带空格也认
	st, b := owner.do("POST", "/api/admin/settings", map[string]any{"values": map[string]string{
		"mail.host": "smtp.gmail.com", "mail.port": "465", "mail.user": "me@example.com", "mail.pass": "abcd efgh ijkl mnop", "tg.token": "sk-secret-123456"}, "trade_password": tp})
	if st != 200 {
		t.Fatalf("保存：%d %v", st, b)
	}
	if applied["mail"] != 1 || applied["tg"] != 1 || applied["data"] != 0 {
		t.Fatalf("改了哪一组就通知哪一组：%v", applied)
	}
	if v := e.api.Sys.Get("mail.pass"); v != "abcdefghijklmnop" {
		t.Fatalf("密码去掉空格：%q", v)
	}
	// 数据库里不是明文
	var raw string
	if err := e.pool.QueryRow(context.Background(), `select value::text from admin_settings where key = 'sys'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "abcdefghijklmnop") || strings.Contains(raw, "sk-secret-123456") || !strings.Contains(raw, "smtp.gmail.com") {
		t.Fatalf("密钥要加密存，别的照常：%s", raw)
	}
	// 接口只给头尾几位
	_, b = owner.do("GET", "/api/admin/settings", nil)
	if f := field(b, "mail.pass"); f["value"] != "abcd…mnop" || f["source"] != "web" {
		t.Fatalf("密钥只给头尾几位：%v", f)
	}
	if f := field(b, "x.accounts"); f["value"] != "env-acct" || f["source"] != "env" {
		t.Fatalf("没在网页填的还用配置文件的：%v", f)
	}
	// 密钥留空：不改
	owner.do("POST", "/api/admin/settings", map[string]any{"values": map[string]string{"mail.pass": "", "mail.from_name": "我的工作台"}, "trade_password": tp})
	if e.api.Sys.Get("mail.pass") != "abcdefghijklmnop" {
		t.Fatal("密钥留空不该清掉")
	}
	// 重启后读回来（解得开）
	s2 := &Sys{S: e.api.S, C: fakeCrypt{}, Env: func(n string) string { return env[n] }}
	if err := s2.Load(context.Background()); err != nil || s2.Get("tg.token") != "sk-secret-123456" || s2.Get("mail.from_name") != "我的工作台" {
		t.Fatalf("重启后读回来：%v %q", err, s2.Get("tg.token"))
	}
	// 清掉：回到配置文件的
	owner.do("POST", "/api/admin/settings", map[string]any{"clear": []string{"mail.host"}, "trade_password": tp})
	if e.api.Sys.Get("mail.host") != "smtp.env.example" || e.api.Sys.Source("mail.host") != "env" {
		t.Fatalf("清掉后用配置文件的：%q", e.api.Sys.Get("mail.host"))
	}
	// 测试按钮
	_, b = owner.do("POST", "/api/admin/settings/test", map[string]string{"group": "mail"})
	if b["ok"] != true || b["message"] != "测试过了：mail" {
		t.Fatalf("测试：%v", b)
	}
	// 操作记录只记改了哪几项，不记值
	var detail string
	_ = e.pool.QueryRow(context.Background(), `select coalesce(string_agg(target, '|'), '') from admin_log where action = 'settings'`).Scan(&detail)
	if !strings.Contains(detail, "mail.pass") || strings.Contains(detail, "abcd") {
		t.Fatalf("操作记录：%q", detail)
	}
}

func field(b map[string]any, key string) map[string]any {
	fs, _ := b["fields"].([]any)
	for _, x := range fs {
		f := x.(map[string]any)
		if f["key"] == key {
			return f
		}
	}
	return nil
}
