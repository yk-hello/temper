package admin

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
)

// D112（11-23）：AI 复盘点评的厂商列表。原来网页里存的一套自动搬进来；加、改、切换、删都要二级密码；
// 密钥加密存、接口不回；第一家加进来就用它；删掉正在用的换成剩下的第一家；测试用存着的密钥。
func TestAIProviders(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	seal := func(v string) string {
		b, _ := fakeCrypt{}.Seal(v)
		return "enc:" + base64.StdEncoding.EncodeToString(b)
	}
	env := map[string]string{"AI_API_KEY": "sk-env-1", "AI_MODEL": "env-model"}
	applied := 0
	var tested AIProvider
	mk := func() *Sys {
		return &Sys{S: e.api.S, C: fakeCrypt{}, Env: func(n string) string { return env[n] }, Apply: func(g string) {
			if g == "ai" {
				applied++
			}
		}, TestAI: func(_ context.Context, p AIProvider, _ bool) (AITest, error) {
			tested = p
			return AITest{Model: "m", Reply: "收到", Tokens: 9}, nil
		}, ListAI: func(_ context.Context, p AIProvider) ([]string, error) {
			tested = p
			return []string{"m-a", "m-b"}, nil
		}}
	}
	// 1. 原来网页里存的那一套：启动时搬成第一家并用它，系统设置里的 ai.* 去掉
	if err := e.api.S.put(ctx, sysKey, map[string]string{"ai.format": "anthropic", "ai.key": seal("sk-ant-old-123456"), "mail.host": "smtp.gmail.com"}); err != nil {
		t.Fatal(err)
	}
	e.api.Sys = mk()
	if err := e.api.Sys.Load(ctx); err != nil {
		t.Fatal(err)
	}
	a := e.api.Sys.AIActive()
	if a == nil || a.Format != "anthropic" || a.Key != "sk-ant-old-123456" || a.Name != "Claude" {
		t.Fatalf("旧设置搬过来并用它：%+v", a)
	}
	var raw string
	_ = e.pool.QueryRow(ctx, `select value::text from admin_settings where key = 'sys'`).Scan(&raw)
	if strings.Contains(raw, "ai.") || !strings.Contains(raw, "smtp.gmail.com") {
		t.Fatalf("系统设置里的 ai.* 去掉、别的留着：%s", raw)
	}
	owner := e.user("owner@example.com")
	// 2. 二级密码不对：拒；新加的不填密钥：拒；思考程度不认识：拒
	if st, _ := owner.do("POST", "/api/admin/ai", map[string]any{"format": "openai", "base": "https://api.deepseek.com", "model": "deepseek-chat", "key": "sk-1", "trade_password": "wrong"}); st == 200 {
		t.Fatal("二级密码不对应该拒")
	}
	if st, b := owner.do("POST", "/api/admin/ai", map[string]any{"format": "openai", "base": "https://api.deepseek.com", "model": "deepseek-chat", "trade_password": tp}); st != 400 || b["code"] != "AI_NO_KEY" {
		t.Fatalf("新加的要填密钥：%d %v", st, b)
	}
	if st, _ := owner.do("POST", "/api/admin/ai", map[string]any{"format": "openai", "base": "https://api.deepseek.com", "model": "deepseek-chat", "key": "sk-1", "effort": "turbo", "trade_password": tp}); st != 400 {
		t.Fatal("思考程度只收那几档")
	}
	if st, _ := owner.do("POST", "/api/admin/ai", map[string]any{"format": "openai", "model": "x", "key": "sk-1", "trade_password": tp}); st != 400 {
		t.Fatal("OpenAI 兼容的要填地址")
	}
	// 3. 加一家 OpenAI 兼容的：正在用的还是原来那家
	st, b := owner.do("POST", "/api/admin/ai", map[string]any{"name": "DeepSeek", "format": "openai", "base": "https://api.deepseek.com/", "model": "deepseek-chat", "key": "sk-deep 123456", "effort": "high", "trade_password": tp})
	if st != 200 {
		t.Fatalf("加一家：%d %v", st, b)
	}
	id := b["id"].(string)
	if e.api.Sys.AIActive().Name != "Claude" || applied != 1 {
		t.Fatalf("加一家不换正在用的，但要通知一下：%v %d", e.api.Sys.AIActive().Name, applied)
	}
	// 接口不回密钥，只给头尾几位；地址去掉结尾的 /；密钥去掉空格
	_, b = owner.do("GET", "/api/admin/ai", nil)
	list := b["list"].([]any)
	d := list[1].(map[string]any)
	if len(list) != 2 || d["key"] != nil || d["key_hint"] != "sk-d…3456" || d["base"] != "https://api.deepseek.com" || d["effort"] != "high" {
		t.Fatalf("列表：%v", list)
	}
	if b["env"].(map[string]any)["model"] != "env-model" {
		t.Fatalf("配置文件里配了也要告诉界面：%v", b["env"])
	}
	_ = e.pool.QueryRow(ctx, `select value::text from admin_settings where key = 'ai_providers'`).Scan(&raw)
	if strings.Contains(raw, "sk-deep123456") || strings.Contains(raw, "sk-ant-old") || !strings.Contains(raw, "deepseek-chat") {
		t.Fatalf("密钥加密存：%s", raw)
	}
	// 4. 切换到它；改它时密钥留空 = 不改
	if st, _ := owner.do("POST", "/api/admin/ai/use", map[string]any{"id": id, "trade_password": tp}); st != 200 || e.api.Sys.AIActive().ID != id {
		t.Fatalf("切换：%d", st)
	}
	owner.do("POST", "/api/admin/ai", map[string]any{"id": id, "name": "DS", "format": "openai", "base": "https://api.deepseek.com", "model": "deepseek-reasoner", "effort": "", "trade_password": tp})
	if a := e.api.Sys.AIActive(); a.Key != "sk-deep123456" || a.Model != "deepseek-reasoner" || a.Effort != "" || a.Name != "DS" {
		t.Fatalf("改了模型、思考程度，密钥留着：%+v", a)
	}
	// 5. 测试：只给 id 就用存着的那家（密钥在服务器上，不用再填）
	_, b = owner.do("POST", "/api/admin/ai/test", map[string]any{"id": id})
	if b["ok"] != true || tested.Key != "sk-deep123456" || tested.Model != "deepseek-reasoner" {
		t.Fatalf("测试存着的：%v %+v", b, tested)
	}
	// 草稿（还没存）：改了模型、密钥空着，用存着的密钥试草稿的模型
	owner.do("POST", "/api/admin/ai/test", map[string]any{"id": id, "format": "openai", "base": "https://api.deepseek.com", "model": "deepseek-chat", "effort": "low"})
	if tested.Key != "sk-deep123456" || tested.Model != "deepseek-chat" || tested.Effort != "low" {
		t.Fatalf("测试草稿：%+v", tested)
	}
	// 获取模型列表：只给 id + 格式、地址（模型还没选），用存着的密钥去问；没密钥的草稿直接说缺密钥
	_, b = owner.do("POST", "/api/admin/ai/models", map[string]any{"id": id, "format": "openai", "base": "https://api.deepseek.com"})
	if ms, _ := b["models"].([]any); b["ok"] != true || len(ms) != 2 || tested.Key != "sk-deep123456" {
		t.Fatalf("获取模型列表：%v %+v", b, tested)
	}
	if _, b = owner.do("POST", "/api/admin/ai/models", map[string]any{"format": "openai", "base": "https://x.example"}); b["code"] != "AI_NO_KEY" {
		t.Fatalf("没密钥：%v", b)
	}
	// 6. 重启后读回来
	s2 := mk()
	if err := s2.Load(ctx); err != nil || s2.AIActive() == nil || s2.AIActive().Key != "sk-deep123456" {
		t.Fatalf("重启后读回来：%v %+v", err, s2.AIActive())
	}
	// 7. 删正在用的：换成剩下的第一家；全删光：用配置文件里的，旧的 ai.* 也不会再被搬回来
	owner.do("POST", "/api/admin/ai/delete", map[string]any{"id": id, "trade_password": tp})
	if a := e.api.Sys.AIActive(); a == nil || a.Name != "Claude" {
		t.Fatalf("删掉正在用的换成剩下的：%+v", a)
	}
	owner.do("POST", "/api/admin/ai/delete", map[string]any{"id": e.api.Sys.AIActive().ID, "trade_password": tp})
	s3 := mk()
	if err := s3.Load(ctx); err != nil || s3.AIActive() != nil {
		t.Fatalf("删光后用配置文件里的，不再搬旧的：%v %+v", err, s3.AIActive())
	}
	// 操作记录不带密钥
	var detail string
	_ = e.pool.QueryRow(ctx, `select coalesce(string_agg(target, '|'), '') from admin_log where action = 'settings'`).Scan(&detail)
	if !strings.Contains(detail, "ai.use:"+id) || strings.Contains(detail, "sk-") {
		t.Fatalf("操作记录：%q", detail)
	}
}
