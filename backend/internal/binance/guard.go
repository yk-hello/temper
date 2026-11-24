package binance

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Guard 所有发往 *.binance.com 的 HTTP 请求都过这里（main 里装到 http.DefaultTransport 上，行情、补数据、实盘一起管）。
//
// 币安的限流按 IP 算：合约每分钟 2400 权重，行情、补历史、实盘下单共用。超了先回 429，
// 收到 429 还接着发就回 418、把 IP 封掉（2 分钟起，越撞越久），同一台机器上的实盘下单、平仓也一起被拒。
// 11-17 走查：Docker 重启后三套后端同时补历史，全被 418 封了。所以：
//   - 收到 429 / 418：按 Retry-After 停发，期间直接返回错误、不再去撞
//   - 行情类请求（不带密钥）：这一分钟已用权重过了上限的 3/4 就等到下一分钟；同一时间最多 2 个，免得开机一起冲
//   - 带密钥的请求（实盘下单、查账户）不排队，额度留给它；只在被封期间直接报错
type Guard struct {
	Next  http.RoundTripper // 空 = 装上之前的默认通道
	Log   *slog.Logger
	Now   func() time.Time       // 测试时换掉
	Match func(host string) bool // 空 = *.binance.com

	mu    sync.Mutex
	hosts map[string]*hostState
}

type hostState struct {
	until      time.Time // 被限流，到这个时间前不发
	used       int       // 最近一次响应头里这一分钟已用的权重
	minute     int64     // used 属于哪一分钟
	slots      chan struct{}
	restricted bool      // 收到过 451：这台服务器所在地区被币安拒绝（成功一次就清掉）
	okAt       time.Time // 最近一次成功
	failAt     time.Time // 最近一次连不上（网络、代理、超时）
}

// Default main 里装上的那一个；别的模块用 Problem() 查"现在为什么取不到币安的数据"
var Default *Guard

// Problem 合约公开接口现在的问题，给界面写原因（11-18：有些地区本来就连不上币安，原来页面一直写"正在加载"）：
// throttled 被限流（min 分钟后恢复）、restricted 所在地区被拒（451）、unreachable 最近两分钟连不上且之后没成功过、"" 没问题
func Problem() (kind string, min int) {
	g := Default
	if g == nil {
		return "", 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.hosts["fapi.binance.com"]
	if st == nil {
		return "", 0
	}
	now := g.now()
	switch {
	case now.Before(st.until):
		return "throttled", (&ErrThrottled{Wait: st.until.Sub(now)}).Minutes()
	case st.restricted:
		return "restricted", 0
	case !st.failAt.IsZero() && st.failAt.After(st.okAt) && now.Sub(st.failAt) < 2*time.Minute:
		return "unreachable", 0
	}
	return "", 0
}

// 装上之前的默认通道（包初始化时取，main 换掉 http.DefaultTransport 之前）
var plainTransport = http.DefaultTransport

// ErrThrottled 币安限流期间直接返回的错误
type ErrThrottled struct {
	Host string
	Wait time.Duration
}

// Minutes 还要等几分钟（向上取整，至少 1），给界面的提示用
func (e *ErrThrottled) Minutes() int {
	return max(1, int((e.Wait+time.Minute-1)/time.Minute))
}

// Throttled err 是不是被币安限流（http.Client 包了一层 *url.Error，errors.As 拆得开）
func Throttled(err error) (*ErrThrottled, bool) {
	var t *ErrThrottled
	ok := errors.As(err, &t)
	return t, ok
}

func (e *ErrThrottled) Error() string {
	return fmt.Sprintf("币安暂时限制了这台服务器的请求（太频繁），约 %d 秒后恢复", int(e.Wait.Seconds()+0.999))
}

// 每分钟权重上限：合约 2400，现货 6000
func softLimit(host string) int {
	if strings.Contains(host, "fapi") {
		return 2400 * 3 / 4
	}
	return 6000 * 3 / 4
}

func (g *Guard) next() http.RoundTripper {
	if g.Next != nil {
		return g.Next
	}
	return plainTransport
}

func (g *Guard) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

func (g *Guard) match(host string) bool {
	if g.Match != nil {
		return g.Match(host)
	}
	return host == "binance.com" || strings.HasSuffix(host, ".binance.com")
}

func (g *Guard) state(host string) *hostState {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.hosts == nil {
		g.hosts = map[string]*hostState{}
	}
	st := g.hosts[host]
	if st == nil {
		st = &hostState{slots: make(chan struct{}, 2)}
		g.hosts[host] = st
	}
	return st
}

// RoundTrip 见 Guard。
func (g *Guard) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Hostname()
	if !g.match(host) {
		return g.next().RoundTrip(req)
	}
	st := g.state(host)
	keyed := req.Header.Get("X-MBX-APIKEY") != ""
	if !keyed {
		select {
		case st.slots <- struct{}{}:
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
		defer func() { <-st.slots }()
	}
	if err := g.wait(req.Context(), host, st, keyed); err != nil {
		return nil, err
	}
	res, err := g.next().RoundTrip(req)
	if err != nil {
		if req.Context().Err() == nil { // 自己取消的不算连不上
			g.mu.Lock()
			st.failAt = g.now()
			g.mu.Unlock()
		}
		return res, err
	}
	g.observe(host, st, res)
	return res, nil
}

func (g *Guard) wait(ctx context.Context, host string, st *hostState, keyed bool) error {
	for {
		g.mu.Lock()
		now := g.now()
		if now.Before(st.until) {
			w := st.until.Sub(now)
			g.mu.Unlock()
			return &ErrThrottled{Host: host, Wait: w}
		}
		var d time.Duration
		if !keyed && st.minute == now.Unix()/60 && st.used >= softLimit(host) {
			d = time.Unix((st.minute+1)*60, 0).Sub(now) + 200*time.Millisecond
		}
		g.mu.Unlock()
		if d <= 0 {
			return nil
		}
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (g *Guard) observe(host string, st *hostState, res *http.Response) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	if n, err := strconv.Atoi(res.Header.Get("X-Mbx-Used-Weight-1m")); err == nil {
		st.used, st.minute = n, now.Unix()/60
	}
	switch {
	case res.StatusCode < 300:
		st.okAt, st.restricted = now, false
	case res.StatusCode == http.StatusUnavailableForLegalReasons:
		if !st.restricted && g.Log != nil {
			g.Log.Warn("币安拒绝了这台服务器所在地区的访问（451）", "host", host)
		}
		st.restricted = true
	}
	if res.StatusCode != http.StatusTooManyRequests && res.StatusCode != http.StatusTeapot {
		return
	}
	sec, _ := strconv.Atoi(res.Header.Get("Retry-After"))
	d := time.Duration(sec) * time.Second
	if d <= 0 {
		d = 30 * time.Second
		if res.StatusCode == http.StatusTeapot {
			d = 2 * time.Minute
		}
	}
	if until := now.Add(d); until.After(st.until) {
		st.until = until
		if g.Log != nil {
			g.Log.Warn("币安限流，暂停发请求", "host", host, "status", res.StatusCode, "seconds", int(d.Seconds()))
		}
	}
}
