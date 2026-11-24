package aireview

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fake struct {
	text  string
	err   error
	calls int
	sys   string
	user  string
}

func (f *fake) Complete(_ context.Context, sys, user string) (string, int, string, error) {
	f.calls++
	f.sys, f.user = sys, user
	return f.text, 42, "fake", f.err
}

const ok = "做对了：按计划止损。\n先改这个：持仓太短。\n下次的规则：至少拿 30 分钟。"

func TestCacheAndQuota(t *testing.T) {
	ctx := context.Background()
	f := &fake{text: ok}
	now := int64(10 * dayMs)
	s := &Service{Provider: f, Now: func() int64 { return now }}
	r, err := s.Generate(ctx, 1, 2, "t1", "zh", Facts{Coin: "BTC"})
	if err != nil || r.Text != ok || r.Tokens != 42 {
		t.Fatalf("%v %+v", err, r)
	}
	// 同一笔同一语言不再花钱
	if _, err = s.Generate(ctx, 1, 2, "t1", "zh", Facts{}); err != nil || f.calls != 1 {
		t.Fatalf("缓存：%v，调用 %d 次", err, f.calls)
	}
	// 换语言是另一条
	if _, err = s.Generate(ctx, 1, 2, "t1", "en", Facts{}); err != nil || f.calls != 2 {
		t.Fatalf("语言：%d", f.calls)
	}
	if !strings.Contains(f.sys, "Did well") {
		t.Fatal("英文要用英文提示词")
	}
	// 别人的不串
	if g, _ := s.Get(ctx, 9, 2, "t1", "zh"); g != nil {
		t.Fatal("串号")
	}
	// 每人每天 20 次
	for i := 0; i < PerUserDay-2; i++ {
		if _, err = s.Generate(ctx, 1, 2, "x"+string(rune('a'+i)), "zh", Facts{}); err != nil {
			t.Fatal(err)
		}
	}
	if s.Left(ctx, 1) != 0 {
		t.Fatalf("还剩 %d", s.Left(ctx, 1))
	}
	var e *Err
	if _, err = s.Generate(ctx, 1, 2, "more", "zh", Facts{}); !errors.As(err, &e) || e.Code != "AI_QUOTA" {
		t.Fatalf("超额：%v", err)
	}
	// 已有的还能看
	if _, err = s.Generate(ctx, 1, 2, "t1", "zh", Facts{}); err != nil {
		t.Fatal("超额后已有的也要能看")
	}
	// 别人不受影响，第二天恢复
	if s.Left(ctx, 2) != PerUserDay {
		t.Fatal("别人的额度")
	}
	now += dayMs + 1
	if s.Left(ctx, 1) != PerUserDay {
		t.Fatal("第二天恢复")
	}
}

func TestFailureIsNotSaved(t *testing.T) {
	ctx := context.Background()
	f := &fake{err: errors.New("boom")}
	s := &Service{Provider: f}
	if _, err := s.Generate(ctx, 1, 2, "t1", "zh", Facts{}); err == nil {
		t.Fatal("应该失败")
	}
	if s.Left(ctx, 1) != PerUserDay {
		t.Fatal("失败不扣额度")
	}
	f.err, f.text = nil, "   \n "
	var e *Err
	if _, err := s.Generate(ctx, 1, 2, "t1", "zh", Facts{}); !errors.As(err, &e) || e.Code != "AI_FAILED" {
		t.Fatalf("空回复：%v", err)
	}
	if off := (&Service{}); off.Enabled() {
		t.Fatal("没 key")
	} else if _, err := off.Generate(ctx, 1, 2, "t", "zh", Facts{}); !errors.As(err, &e) || e.Code != "AI_OFF" {
		t.Fatalf("没 key：%v", err)
	}
}

// 第一次回空或说了不该说的：自动再试一次，用户不用重新点。
type flaky struct {
	replies []string
	calls   int
}

func (f *flaky) Complete(context.Context, string, string) (string, int, string, error) {
	r := f.replies[min(f.calls, len(f.replies)-1)]
	f.calls++
	return r, 1, "fake", nil
}

func TestRetriesOnce(t *testing.T) {
	ctx := context.Background()
	for _, first := range []string{"", "建议买入 BTC"} {
		f := &flaky{replies: []string{first, ok}}
		r, err := (&Service{Provider: f}).Generate(ctx, 1, 2, "t", "zh", Facts{})
		if err != nil || r.Text != ok || f.calls != 2 {
			t.Fatalf("第一次回 %q：%v，调用 %d 次", first, err, f.calls)
		}
	}
	// 只重试一次，不能一直花钱
	f := &flaky{replies: []string{""}}
	if _, err := (&Service{Provider: f}).Generate(ctx, 1, 2, "t", "zh", Facts{}); err == nil || f.calls != 2 {
		t.Fatalf("%v，调用 %d 次", err, f.calls)
	}
}

// 提示词要把字段讲清楚，并且不许硬夸、不许猜想法。
func TestPromptsSayTheRules(t *testing.T) {
	for _, k := range []string{"max_adverse_pct", "rule_violations", "over_risk", "moved_stop", "closed_by"} {
		if !strings.Contains(systemZh, k) || !strings.Contains(systemEn, k) {
			t.Fatalf("提示词没解释 %s", k)
		}
	}
	if !strings.Contains(systemZh, "不要硬夸") || !strings.Contains(systemEn, "invent praise") {
		t.Fatal("要写明不许硬夸")
	}
	if !strings.Contains(systemZh, "不要推测") || !strings.Contains(systemEn, "guess the trader") {
		t.Fatal("要写明不许猜想法")
	}
}

func TestBlocked(t *testing.T) {
	bad := []string{
		"这样做年化可以到 30%", "建议买入 BTC", "BTC 将上涨", "这是无风险的做法", "必涨",
		"You should buy the dip", "We recommend selling now", "BTC will rise from here", "a guaranteed win", "APY of 20%", "risk-free",
	}
	for _, s := range bad {
		if !Blocked(s) {
			t.Errorf("没拦住：%s", s)
		}
	}
	good := []string{
		ok, "Did well: you honored your stop.\nFix first: entry was late by 0.4%.\nRule for next time: risk at most 1% per trade.",
		"The stop was 1.2% from entry.", "下次把止损放在开仓价 1% 以内", "The numbers are too thin to conclude.",
	}
	for _, s := range good {
		if Blocked(s) {
			t.Errorf("误伤：%s", s)
		}
	}
	f := &fake{text: "建议买入 BTC"}
	s := &Service{Provider: f}
	var e *Err
	if _, err := s.Generate(context.Background(), 1, 2, "t", "zh", Facts{}); !errors.As(err, &e) || e.Code != "AI_FILTERED" {
		t.Fatalf("被拦的：%v", err)
	}
	if g, _ := s.Get(context.Background(), 1, 2, "t", "zh"); g != nil {
		t.Fatal("被拦的不能存")
	}
}

func TestClean(t *testing.T) {
	got := Clean("**Did well:** a\n\n- Fix first: `b`\n# Rule for next time: c\nextra line")
	want := "Did well: a\nFix first: b\nRule for next time: c"
	if got != want {
		t.Fatalf("%q", got)
	}
	if n := len([]rune(Clean(strings.Repeat("字", 900)))); n != 500 {
		t.Fatalf("截断：%d", n)
	}
}

// 发出去的只有这笔交易的数字：不能有邮箱、账号、编号、笔记。
func TestFactsCarryNoIdentity(t *testing.T) {
	b, _ := json.Marshal(Facts{Coin: "BTC", Side: "long", ClosedBy: "sl"})
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	for k := range m {
		for _, bad := range []string{"email", "user", "acct", "account", "note", "wallet", "address"} {
			if strings.Contains(k, bad) {
				t.Fatalf("字段 %s 不该发出去", k)
			}
		}
		if k == "id" || strings.HasSuffix(k, "_id") {
			t.Fatalf("字段 %s 不该发出去", k)
		}
	}
}

func TestDeepSeekRequest(t *testing.T) {
	var got struct {
		Model    string `json:"model"`
		Thinking struct {
			Type string `json:"type"`
		} `json:"thinking"`
		Messages []struct {
			Role, Content string
		} `json:"messages"`
	}
	var auth, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, path = r.Header.Get("Authorization"), r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = io.WriteString(w, `{"model":"m1","choices":[{"message":{"content":"hello"}}],"usage":{"total_tokens":7}}`)
	}))
	defer srv.Close()
	d := DeepSeek("sk-secret", "", srv.URL)
	text, tokens, model, err := d.Complete(context.Background(), "S", "U")
	if err != nil || text != "hello" || tokens != 7 || model != "m1" {
		t.Fatalf("%v %q %d %q", err, text, tokens, model)
	}
	if auth != "Bearer sk-secret" || path != "/chat/completions" || got.Model != "deepseek-flash" {
		t.Fatalf("请求：%q %q %q", auth, path, got.Model)
	}
	if got.Thinking.Type != "disabled" {
		t.Fatalf("要关掉思考模式：%q", got.Thinking.Type)
	}
	if len(got.Messages) != 2 || got.Messages[0].Role != "system" || got.Messages[1].Content != "U" {
		t.Fatalf("消息：%+v", got.Messages)
	}
}

func TestDeepSeekErrorsHideKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = io.WriteString(w, `{"error":"bad key sk-secret"}`)
	}))
	defer srv.Close()
	for _, base := range []string{srv.URL, "http://127.0.0.1:1"} {
		_, _, _, err := DeepSeek("sk-secret", "", base).Complete(context.Background(), "S", "U")
		if err == nil || strings.Contains(err.Error(), "sk-secret") {
			t.Fatalf("错误信息：%v", err)
		}
	}
}
