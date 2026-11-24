package ratelimit

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBucket(t *testing.T) {
	now := time.Unix(1000, 0)
	l := &Limiter{Rate: 10, Burst: 5, Now: func() time.Time { return now }}
	for i := 0; i < 5; i++ {
		if !l.Allow("a") {
			t.Fatalf("第 %d 个应该放行（攒了 5 个）", i+1)
		}
	}
	if l.Allow("a") {
		t.Fatal("攒的用完了应该拦")
	}
	if !l.Allow("b") {
		t.Fatal("别的来源不受影响")
	}
	if !l.Allow("") {
		t.Fatal("没有来源地址（本机直连）不限")
	}
	now = now.Add(300 * time.Millisecond) // 补 3 个
	for i := 0; i < 3; i++ {
		if !l.Allow("a") {
			t.Fatalf("过了 0.3 秒应该补回 3 个，第 %d 个被拦", i+1)
		}
	}
	if l.Allow("a") {
		t.Fatal("补的也用完了")
	}
	now = now.Add(time.Hour)
	for i := 0; i < 5; i++ {
		if !l.Allow("a") {
			t.Fatal("很久以后最多攒满 5 个")
		}
	}
	if l.Allow("a") {
		t.Fatal("攒不超过上限")
	}
}

func TestWrap(t *testing.T) {
	l := &Limiter{Rate: 1, Burst: 2, IP: func(r *http.Request) string { return r.Header.Get("X-Test-IP") }}
	h := l.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	do := func(path, ip string) int {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("X-Test-IP", ip)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if do("/api/x", "1.2.3.4") != 200 || do("/api/x", "1.2.3.4") != 200 {
		t.Fatal("前两个放行")
	}
	if c := do("/v1/messages", "1.2.3.4"); c != 429 {
		t.Fatalf("第三个应该 429：%d", c)
	}
	if do("/ws", "1.2.3.4") != 200 {
		t.Fatal("推送连接不按请求数限")
	}
}
