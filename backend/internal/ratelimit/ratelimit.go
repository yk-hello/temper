// Package ratelimit 按来源地址限流（18 第 6.8 条，放到公网前要有）：防一个来源把服务器打满。
// 正常用的人碰不到：打开交易页一下发几十个请求、之后每秒几个；限额按"每秒 30 个、一次最多攒 300 个"给。
// 来源地址和登录锁定用同一个规则（auth.ClientIP）：本机直连、没经过 nginx 的（开发、测试脚本）不限。
package ratelimit

import (
	"net/http"
	"strings"
	"sync"
	"time"
)

type bucket struct {
	tokens float64
	last   time.Time
}

// Limiter 令牌桶，每个来源一个。
type Limiter struct {
	Rate  float64 // 每秒补多少
	Burst float64 // 最多攒多少
	IP    func(r *http.Request) string
	Now   func() time.Time

	mu    sync.Mutex
	b     map[string]*bucket
	sweep time.Time
}

func (l *Limiter) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

// Allow 这个来源现在能不能再发一个请求。
func (l *Limiter) Allow(key string) bool {
	if key == "" {
		return true
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.b == nil {
		l.b = map[string]*bucket{}
	}
	// 每分钟清一次很久没来的来源，表不会越长越大
	if now.Sub(l.sweep) > time.Minute {
		for k, v := range l.b {
			if now.Sub(v.last) > 10*time.Minute {
				delete(l.b, k)
			}
		}
		l.sweep = now
	}
	b := l.b[key]
	if b == nil {
		b = &bucket{tokens: l.Burst, last: now}
		l.b[key] = b
	}
	b.tokens = min(l.Burst, b.tokens+now.Sub(b.last).Seconds()*l.Rate)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// Wrap 只限 /api/、/v1/、/bapi/（推送是长连接，不按请求数算）。超了回 429，告诉对方过几秒再试。
func (l *Limiter) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		limited := strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/v1/") || (strings.HasPrefix(p, "/bapi/") && !strings.Contains(p, "/ws/"))
		if limited && !l.Allow(l.IP(r)) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"code":"RATE_LIMITED"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}
