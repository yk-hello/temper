package aireview

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 2026-11-15：AI 点评能用 Claude（Anthropic 自己的接口）。请求发到 /v1/messages、密钥在请求头里（SDK 用 Authorization: Bearer）、
// 系统提示和用户内容都带上；回复里只取文字（思考块跳过）；token 数加总；出错只报状态码，不带对方返回的内容。
func TestClaudeProvider(t *testing.T) {
	var got struct {
		path, key, beta string
		body            map[string]any
	}
	status := 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path, got.key, got.beta = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("anthropic-beta")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got.body)
		w.Header().Set("Content-Type", "application/json")
		if status != 200 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key sk-ant-secret"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-5",
			"content":[{"type":"thinking","thinking":"","signature":"sig"},{"type":"text","text":"做对了：止损设了"},{"type":"text","text":"。"}],
			"stop_reason":"end_turn","usage":{"input_tokens":120,"output_tokens":30,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}`))
	}))
	defer srv.Close()

	c := &Claude{Key: "sk-ant-secret", Base: srv.URL + "/v1/"}
	text, tokens, model, err := c.Complete(context.Background(), "系统提示", "用户内容")
	if err != nil {
		t.Fatal(err)
	}
	if got.path != "/v1/messages" || got.key != "Bearer sk-ant-secret" {
		t.Fatalf("请求地址或密钥头不对：%q %q", got.path, got.key)
	}
	if got.body["model"] != ClaudeModel {
		t.Fatalf("没填模型用默认的：%v", got.body["model"])
	}
	if s, _ := json.Marshal(got.body); !strings.Contains(string(s), "系统提示") || !strings.Contains(string(s), "用户内容") {
		t.Fatalf("系统提示和用户内容都要带上：%s", s)
	}
	if got.beta != "" || got.body["fallbacks"] != nil {
		t.Fatalf("中转站地址不带备用模型参数：%q %v", got.beta, got.body["fallbacks"])
	}
	if text != "做对了：止损设了。" || tokens != 150 || model != "claude-opus-5-5" {
		t.Fatalf("回复：%q %d %q", text, tokens, model)
	}

	status = 401
	_, _, _, err = c.Complete(context.Background(), "s", "u")
	if err == nil || err.Error() != "HTTP 401" {
		t.Fatalf("出错只报状态码：%v", err)
	}
}

func TestClaudeBase(t *testing.T) {
	for in, want := range map[string]string{"": "", "https://api.anthropic.com": "https://api.anthropic.com", "https://relay.example/v1/": "https://relay.example", " https://x.example/ ": "https://x.example"} {
		if got := ClaudeBase(in); got != want {
			t.Fatalf("%q → %q，应该是 %q", in, got, want)
		}
	}
}
