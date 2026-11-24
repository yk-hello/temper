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

// D112（11-23）：思考程度按各家规矩传。OpenAI 兼容：reasoning_effort；不传 temperature（推理模型只认默认值）；
// OpenAI 官方用 max_completion_tokens，别家用 max_tokens；开了思考给足输出上限。
func TestChatEffort(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = map[string]any{}
		_ = json.Unmarshal(b, &body)
		_, _ = io.WriteString(w, `{"model":"m","choices":[{"message":{"content":"ok"}}],"usage":{"total_tokens":3}}`)
	}))
	defer srv.Close()
	for _, c := range []struct {
		effort   string
		want     any
		maxToken float64
	}{{"", nil, 2500}, {"none", "none", 2500}, {"low", "low", 8000}, {"xhigh", "xhigh", 8000}} {
		p := Build("openai", "k", srv.URL, "m", c.effort)
		if _, _, _, err := p.Complete(context.Background(), "S", "U"); err != nil {
			t.Fatal(err)
		}
		if body["reasoning_effort"] != c.want || body["max_tokens"] != c.maxToken || body["temperature"] != nil || body["max_completion_tokens"] != nil {
			t.Fatalf("思考程度 %q：%v", c.effort, body)
		}
	}
	// OpenAI 官方：max_completion_tokens（max_tokens 废弃、o 系列不认）。用改写地址的办法把请求引到假服务器
	tr := http.DefaultTransport
	http.DefaultTransport = roundTrip(func(r *http.Request) (*http.Response, error) {
		r.URL.Scheme, r.URL.Host = "http", strings.TrimPrefix(srv.URL, "http://")
		return tr.RoundTrip(r)
	})
	defer func() { http.DefaultTransport = tr }()
	if _, _, _, err := (&Chat{Key: "k", Base: "https://api.openai.com/v1", Model: "gpt", Effort: "high"}).Complete(context.Background(), "S", "U"); err != nil {
		t.Fatal(err)
	}
	if body["max_completion_tokens"] != float64(16000) || body["max_tokens"] != nil || body["reasoning_effort"] != "high" {
		t.Fatalf("OpenAI 官方：%v", body)
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Claude：effort 放在 output_config 里；"关"：关得掉的模型发 thinking disabled，关不掉的（Opus 5.5 等）按 low 发。
func TestClaudeEffort(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = map[string]any{}
		_ = json.Unmarshal(b, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"m","type":"message","role":"assistant","model":"x","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer srv.Close()
	effortOf := func() any {
		oc, _ := body["output_config"].(map[string]any)
		return oc["effort"]
	}
	thinkingOf := func() any {
		th, _ := body["thinking"].(map[string]any)
		return th["type"]
	}
	for _, c := range []struct {
		model, effort string
		wantEffort    any
		wantThinking  any
	}{
		{"", "", nil, nil},
		{"", "high", "high", nil},
		{"claude-opus-5-5", "none", "low", nil},
		{"claude-sonnet-5-5", "none", "low", nil},
		{"claude-haiku-5-5", "none", nil, "disabled"},
		{"claude-sonnet-5-5", "xhigh", "xhigh", nil},
	} {
		p := Build("anthropic", "k", srv.URL, c.model, c.effort)
		if _, _, _, err := p.Complete(context.Background(), "S", "U"); err != nil {
			t.Fatal(err)
		}
		if effortOf() != c.wantEffort || thinkingOf() != c.wantThinking || body["temperature"] != nil {
			t.Fatalf("%s %q：%v", c.model, c.effort, body)
		}
	}
}

// 11-24：获取模型列表。OpenAI 兼容问 /models（密钥在 Authorization）；Claude 问 /v1/models。按名字排好。
func TestListModels(t *testing.T) {
	var path, auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("Authorization")+r.Header.Get("X-Api-Key")
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Anthropic-Version") != "" { // Claude 的 SDK 总带这个头
			_, _ = io.WriteString(w, `{"data":[{"id":"claude-sonnet-5-5","type":"model","display_name":"S","created_at":"2026-01-01T00:00:00Z"},{"id":"claude-haiku-5-5","type":"model","display_name":"H","created_at":"2026-01-01T00:00:00Z"}],"has_more":false,"first_id":"a","last_id":"b"}`)
			return
		}
		_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"gpt-b"},{"id":"gpt-a"}]}`)
	}))
	defer srv.Close()
	ids, err := ListModels(context.Background(), "openai", "sk-1", srv.URL+"/v1/", nil)
	if err != nil || strings.Join(ids, ",") != "gpt-a,gpt-b" || path != "/v1/models" || auth != "Bearer sk-1" {
		t.Fatalf("OpenAI 兼容：%v %v %q %q", err, ids, path, auth)
	}
	ids, err = ListModels(context.Background(), "anthropic", "sk-ant", srv.URL, nil)
	if err != nil || strings.Join(ids, ",") != "claude-haiku-5-5,claude-sonnet-5-5" || path != "/v1/models" || !strings.Contains(auth, "sk-ant") {
		t.Fatalf("Claude：%v %v %q %q", err, ids, path, auth)
	}
	if _, err := ListModels(context.Background(), "openai", "sk-secret", "http://127.0.0.1:9", nil); err == nil || strings.Contains(err.Error(), "sk-secret") {
		t.Fatalf("连不上要报错、不带密钥：%v", err)
	}
}
