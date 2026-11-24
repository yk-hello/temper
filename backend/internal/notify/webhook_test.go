package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// 对外 Webhook：选中的事件发过去、带签名、对方能验；失败重试；不选的不发；默认不发到本机和内网。
func TestWebhookDelivery(t *testing.T) {
	var mu sync.Mutex
	var got []map[string]any
	fails := 1 // 第一次回 500，看重试
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if r.Header.Get("X-QS-Signature") != sign("SECRET", r.Header.Get("X-QS-Timestamp"), b) {
			w.WriteHeader(401)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if fails > 0 {
			fails--
			w.WriteHeader(500)
			return
		}
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		m["_event_header"] = r.Header.Get("X-QS-Event")
		got = append(got, m)
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := New(nil)
	s.WH = &Webhooks{AllowPrivate: true, Retry: []time.Duration{10 * time.Millisecond, 10 * time.Millisecond}} // 测试服务器在本机
	s.StartWebhooks(ctx)
	if _, err := s.SetWebhook(ctx, 1, srv.URL+"/hook", false); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.hooks[1].Secret = "SECRET" // 用已知的密钥，好验签名
	s.mu.Unlock()
	s.Push(ctx, 1, KLiq, map[string]string{"inst": "BTC-PERP", "px": "60000", "dist": "4.5", "acct": "模拟账户 1"})
	_ = s.SetPref(ctx, 1, KPrice, Pref{Site: true, TG: true, WH: false})
	s.Push(ctx, 1, KPrice, map[string]string{"inst": "BTC-PERP", "px": "70000"}) // 关了 Webhook：不发
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("应该只收到强平预警一条（重试后）：%v", got)
	}
	if got[0]["event"] != KLiq || got[0]["_event_header"] != KLiq || got[0]["text"] == "" {
		t.Fatalf("内容：%v", got[0])
	}
	if p, _ := got[0]["params"].(map[string]any); p["px"] != "60000" {
		t.Fatalf("参数：%v", got[0])
	}
	s.mu.Lock()
	h := *s.hooks[1]
	s.mu.Unlock()
	if h.LastStatus != 200 || h.LastErr != "" {
		t.Fatalf("最后一次结果：%+v", h)
	}
}

func TestWebhookPrivateBlocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	defer srv.Close()
	w := &Webhooks{}
	for _, u := range []string{"http://127.0.0.1:9/x", "http://10.0.0.5/x", "http://localhost:8080/x", "http://[::1]/x", "http://169.254.169.254/latest/meta-data"} {
		if err := w.CheckURL(u); err == nil || err.Error() != "WEBHOOK_PRIVATE" {
			t.Fatalf("%s 应该被拦：%v", u, err)
		}
	}
	for _, u := range []string{"ftp://example.com/x", "not a url", "http://user:pw@example.com/x"} {
		if err := w.CheckURL(u); err == nil || err.Error() != "BAD_URL" {
			t.Fatalf("%s 应该算地址不对：%v", u, err)
		}
	}
	if err := w.CheckURL("https://example.com/hook"); err != nil {
		t.Fatalf("正常地址：%v", err)
	}
	// 写的是域名、解析出来是内网：连接时也拦（改 DNS 绕不过）
	st, msg := w.post(context.Background(), Hook{URL: srv.URL, Secret: "s"}, "test", []byte(`{}`))
	if st != 0 || msg != errPrivate.Error() {
		t.Fatalf("连接时没拦住本机：%d %s", st, msg)
	}
	// 自己部署在内网、明确打开的：能发
	w2 := &Webhooks{AllowPrivate: true}
	if st, msg := w2.post(context.Background(), Hook{URL: srv.URL, Secret: "s"}, "test", []byte(`{}`)); st != 200 || msg != "" {
		t.Fatalf("打开内网后：%d %s", st, msg)
	}
}

// 18 第 8.5 条：Discord、Slack 的进站 Webhook 按它们的格式发；别的地址照原样
func TestShapeChatWebhooks(t *testing.T) {
	body := payload(KFill, 1, map[string]string{"inst": "BTC-PERP"}, "成交：BTC 买入 1 @ 1 @everyone")
	var m map[string]any
	if err := json.Unmarshal(shape("https://discord.com/api/webhooks/1/abc", body), &m); err != nil || m["content"] != "成交：BTC 买入 1 @ 1 @everyone" || m["event"] != nil {
		t.Fatalf("Discord：%v %v", err, m)
	}
	if am, _ := m["allowed_mentions"].(map[string]any); am == nil || len(am["parse"].([]any)) != 0 {
		t.Fatalf("Discord 不能让文字触发提醒：%v", m)
	}
	m = nil
	if err := json.Unmarshal(shape("https://hooks.slack.com/services/T/B/x", body), &m); err != nil || m["text"] != "成交：BTC 买入 1 @ 1 @everyone" || len(m) != 1 {
		t.Fatalf("Slack：%v %v", err, m)
	}
	for _, u := range []string{"https://example.com/hook", "https://discord.com/other", "https://evil.example/api/webhooks/1/abc?x=discord.com"} {
		if string(shape(u, body)) != string(body) {
			t.Fatalf("%s 应该照原样发", u)
		}
	}
	long := payload(KFill, 1, nil, strings.Repeat("字", 3000))
	m = nil
	_ = json.Unmarshal(shape("https://discord.com/api/webhooks/1/abc", long), &m)
	if n := len([]rune(m["content"].(string))); n != 1900 {
		t.Fatalf("Discord 超长要截断：%d", n)
	}
}
