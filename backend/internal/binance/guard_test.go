package binance

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func resp(code int, hdr map[string]string) *http.Response {
	h := http.Header{}
	for k, v := range hdr {
		h.Set(k, v)
	}
	return &http.Response{StatusCode: code, Header: h, Body: io.NopCloser(strings.NewReader("{}"))}
}

func get(t *testing.T, g *Guard, url string, key bool) (*http.Response, error) {
	t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), "GET", url, nil)
	if key {
		req.Header.Set("X-MBX-APIKEY", "k")
	}
	return g.RoundTrip(req)
}

// 11-17：收到 429 / 418 以后按 Retry-After 停发，期间不再去撞（行情和带密钥的都一样），别的网站不受影响
func TestGuardBackoff(t *testing.T) {
	var hits atomic.Int32
	code := 429
	now := time.Unix(1_800_000_000, 0)
	g := &Guard{Now: func() time.Time { return now }, Next: rtFunc(func(r *http.Request) (*http.Response, error) {
		hits.Add(1)
		if strings.Contains(r.URL.Host, "binance") {
			return resp(code, map[string]string{"Retry-After": "7"}), nil
		}
		return resp(200, nil), nil
	})}
	if r, err := get(t, g, "https://fapi.binance.com/fapi/v1/depth", false); err != nil || r.StatusCode != 429 {
		t.Fatalf("第一次：%v %v", r, err)
	}
	for _, key := range []bool{false, true} {
		_, err := get(t, g, "https://fapi.binance.com/fapi/v1/order", key)
		var th *ErrThrottled
		if !errors.As(err, &th) || th.Wait != 7*time.Second {
			t.Fatalf("限流期间应直接报错（带密钥=%v）：%v", key, err)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("限流期间还去撞了币安：%d 次", hits.Load())
	}
	if _, err := get(t, g, "https://api.hyperliquid.xyz/info", false); err != nil {
		t.Fatalf("别的网站不该受影响：%v", err)
	}
	// 现货那边是另一份额度
	code = 200
	if _, err := get(t, g, "https://api.binance.com/api/v3/time", false); err != nil {
		t.Fatalf("现货不该被合约的限流挡住：%v", err)
	}
	// 时间到了恢复；418 没给 Retry-After 时至少停 2 分钟
	now = now.Add(8 * time.Second)
	code = 418
	if _, err := get(t, g, "https://fapi.binance.com/fapi/v1/depth", false); err != nil {
		t.Fatalf("到时间应该恢复：%v", err)
	}
	g.Next = rtFunc(func(r *http.Request) (*http.Response, error) { hits.Add(1); return resp(418, nil), nil })
	now = now.Add(8 * time.Second) // 上面那次 418 带了 Retry-After 7
	_, _ = get(t, g, "https://fapi.binance.com/fapi/v1/depth", false)
	_, err := get(t, g, "https://fapi.binance.com/fapi/v1/depth", false)
	var th *ErrThrottled
	if !errors.As(err, &th) || th.Wait != 2*time.Minute {
		t.Fatalf("418 没给等待时间时应停 2 分钟：%v", err)
	}
	if !strings.Contains(err.Error(), "120 秒") {
		t.Fatalf("提示：%s", err)
	}
}

// 这一分钟已用权重过了上限的 3/4：行情请求等到下一分钟，带密钥的（下单）不等
func TestGuardWeight(t *testing.T) {
	base := time.Unix(1_800_000_000, 0).Truncate(time.Minute).Add(59*time.Second + 600*time.Millisecond) // 离下一分钟 0.4 秒
	start := time.Now()
	g := &Guard{Now: func() time.Time { return base.Add(time.Since(start)) }, Next: rtFunc(func(r *http.Request) (*http.Response, error) {
		return resp(200, map[string]string{"X-MBX-USED-WEIGHT-1M": "1900"}), nil
	})}
	if _, err := get(t, g, "https://fapi.binance.com/fapi/v1/depth", false); err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	if _, err := get(t, g, "https://fapi.binance.com/fapi/v1/order", true); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(t0); d > 100*time.Millisecond {
		t.Fatalf("下单不该排队：等了 %v", d)
	}
	t0 = time.Now()
	if _, err := get(t, g, "https://fapi.binance.com/fapi/v1/depth", false); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(t0); d < 300*time.Millisecond || d > 2*time.Second {
		t.Fatalf("行情请求应等到下一分钟（约 0.6 秒）：等了 %v", d)
	}
}

// 同一时间最多 2 个行情请求
func TestGuardSlots(t *testing.T) {
	var cur, peak atomic.Int32
	g := &Guard{Next: rtFunc(func(r *http.Request) (*http.Response, error) {
		n := cur.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		cur.Add(-1)
		return resp(200, nil), nil
	})}
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() { _, _ = get(t, g, "https://fapi.binance.com/fapi/v1/depth", false); done <- struct{}{} }()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
	if peak.Load() != 2 {
		t.Fatalf("同时最多 2 个：%d", peak.Load())
	}
}

// 界面写原因用：限流、地区被拒（451）、连不上；成功一次就清掉
func TestGuardProblem(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	code := 451
	fail := false
	g := &Guard{Now: func() time.Time { return now }, Next: rtFunc(func(r *http.Request) (*http.Response, error) {
		if fail {
			return nil, errors.New("dial tcp: i/o timeout")
		}
		return resp(code, map[string]string{"Retry-After": "300"}), nil
	})}
	old := Default
	Default = g
	defer func() { Default = old }()
	if k, _ := Problem(); k != "" {
		t.Fatalf("没请求过不该有问题：%s", k)
	}
	_, _ = get(t, g, "https://fapi.binance.com/fapi/v1/ticker/24hr", false)
	if k, _ := Problem(); k != "restricted" {
		t.Fatalf("451 → restricted：%s", k)
	}
	code = 200
	_, _ = get(t, g, "https://fapi.binance.com/fapi/v1/ticker/24hr", false)
	if k, _ := Problem(); k != "" {
		t.Fatalf("成功一次就清掉：%s", k)
	}
	fail = true
	now = now.Add(time.Second)
	_, _ = get(t, g, "https://fapi.binance.com/fapi/v1/ticker/24hr", false)
	if k, _ := Problem(); k != "unreachable" {
		t.Fatalf("连不上 → unreachable：%s", k)
	}
	now = now.Add(3 * time.Minute)
	if k, _ := Problem(); k != "" {
		t.Fatalf("两分钟以前的连不上不再算：%s", k)
	}
	fail, code = false, 418
	_, _ = get(t, g, "https://fapi.binance.com/fapi/v1/depth", false)
	if k, m := Problem(); k != "throttled" || m != 5 {
		t.Fatalf("418 → throttled 5 分钟：%s %d", k, m)
	}
}
