// Package latency 模拟盘的下单延迟（D94）：单子要在网上跑一段才到交易所，回报再跑一段才回来。
//
// 怎么算（往返时间记作 RTT）：
//   - 跟随实测：这台服务器到币安的真实 RTT。本地的盘口本来就晚了半个 RTT（行情传过来要时间），
//     单子到交易所还要半个 RTT，交易所那一刻的盘口再传回本地又要半个 RTT——所以按"一个 RTT 之后的本地盘口"成交，
//     回报也在一个 RTT 之后到。
//   - 选一个地区：假装人在那里。单子按"那个地区的半个 RTT + 本地行情晚到的半个 RTT"之后的盘口成交，
//     回报在那个地区的一个 RTT 之后到。
//   - 关闭：立即成交（以前的做法）。
//
// 每次都在基准上加抖动；大约 1% 的请求碰上网络抖一下（3–8 倍）。地区的 RTT 是参考值：
// 公开资料显示币安合约的撮合在 AWS 东京，表里是 AWS 各区域到东京的往返时间（cloudping.co 公开的中位数，2026-11 取），
// 再加几毫秒交易所网关处理。以前存过、现在表里没有的地区，读出来时按"跟随实测"算（Prefs.Attach）。
package latency

import (
	"context"
	"math"
	"math/rand/v2"
	"net/http"
	"sort"
	"sync"
	"time"
)

// Region 一个地区到币安的往返时间。
type Region struct {
	Key    string        `json:"key"`
	RTT    time.Duration `json:"-"`
	Jitter time.Duration `json:"-"`
	RTTms  float64       `json:"rtt_ms"`
	Jms    float64       `json:"jitter_ms"`
}

const gateway = 4 * time.Millisecond // 交易所网关和撮合处理

func reg(key string, rtt, jitter float64) Region {
	return Region{Key: key, RTT: time.Duration(rtt*float64(time.Millisecond)) + gateway, Jitter: time.Duration(jitter * float64(time.Millisecond)), RTTms: rtt + float64(gateway/time.Millisecond), Jms: jitter}
}

// Regions 能选的地区（按离东京由近到远）。
var Regions = []Region{
	reg("tokyo", 2, 0.5),
	reg("seoul", 33, 2),
	reg("hongkong", 50, 4),
	reg("singapore", 69, 4),
	reg("uswest", 100, 6),
	reg("useast", 147, 8),
	reg("london", 212, 10),
	reg("frankfurt", 226, 10),
}

// Modes 能选的模式：measured（跟随实测）、off（关闭）、各个地区。
func Valid(mode string) bool {
	if mode == "measured" || mode == "off" {
		return true
	}
	_, ok := regionOf(mode)
	return ok
}

func regionOf(key string) (Region, bool) {
	for _, r := range Regions {
		if r.Key == key {
			return r, true
		}
	}
	return Region{}, false
}

// Meter 测这台服务器到交易所的往返时间：每 15 秒请求一次交易所的时间接口（复用连接，第一次握手不算）。
type Meter struct {
	URL  string // 例如 https://fapi.binance.com/fapi/v1/time；空 = 不测（模拟行情）
	HTTP *http.Client

	mu      sync.Mutex
	samples []time.Duration
	at      time.Time
}

func (m *Meter) Run(ctx context.Context) {
	if m == nil || m.URL == "" {
		return
	}
	c := m.HTTP
	if c == nil {
		c = &http.Client{Timeout: 5 * time.Second}
	}
	first := true
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		t0 := time.Now()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, m.URL, nil)
		res, err := c.Do(req)
		d := time.Since(t0)
		if err == nil {
			_ = res.Body.Close()
			if res.StatusCode == 200 && !first {
				m.add(d)
			}
			first = false
		}
		if first { // 握手那次之后马上再测一次
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (m *Meter) add(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.samples = append(m.samples, d)
	if len(m.samples) > 40 {
		m.samples = m.samples[len(m.samples)-40:]
	}
	m.at = time.Now()
}

// Measured 实测的往返时间（中位数）和抖动（90 分位减中位数）；ok = false 表示还没测到或不测。
func (m *Meter) Measured() (rtt, jitter time.Duration, ok bool) {
	if m == nil {
		return 0, 0, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.samples) == 0 || time.Since(m.at) > 5*time.Minute {
		return 0, 0, false
	}
	s := append([]time.Duration(nil), m.samples...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	med := s[len(s)/2]
	p90 := s[min(len(s)-1, len(s)*9/10)]
	return med, p90 - med, true
}

// Model 按用户选的模式给每次请求抽一个延迟。
type Model struct {
	Meter *Meter
	Mode  func(acct int64) string // 这个账户的主人选的模式
	// Rand 测试用：固定随机数
	Rand func() float64
}

func (m *Model) rnd() float64 {
	if m.Rand != nil {
		return m.Rand()
	}
	return rand.Float64()
}

// sample 一次往返：基准 + 抖动（半正态）+ 偶尔一次网络抖动。
func (m *Model) sample(base, jitter time.Duration) time.Duration {
	u1, u2 := math.Max(m.rnd(), 1e-12), m.rnd()
	z := math.Abs(math.Sqrt(-2*math.Log(u1)) * math.Cos(2*math.Pi*u2))
	d := base + time.Duration(z*float64(jitter))
	if m.rnd() < 0.01 {
		d = time.Duration(float64(d) * (3 + 5*m.rnd()))
	}
	return d
}

// Hop 给引擎用：exec = 多久之后按当时的盘口成交，reply = 多久之后回报回来。
func (m *Model) Hop(acct int64) (exec, reply time.Duration) {
	mode := "measured"
	if m.Mode != nil {
		mode = m.Mode(acct)
	}
	local, lj, ok := m.Meter.Measured()
	if !ok {
		local, lj = 0, 0
	}
	switch mode {
	case "off":
		return 0, 0
	case "measured":
		if !ok {
			return 0, 0
		}
		d := m.sample(local, lj)
		return d, d
	}
	r, found := regionOf(mode)
	if !found {
		return 0, 0
	}
	there := m.sample(r.RTT, r.Jitter)
	exec = there/2 + local/2
	return exec, max(exec, there)
}

// Info 给界面看的：现在的模式、实测值、各地区的参考值。
type Info struct {
	Mode     string   `json:"mode"`
	Measured *float64 `json:"measured_ms"` // nil = 测不到（模拟行情或还没测）
	MJitter  float64  `json:"measured_jitter_ms"`
	Regions  []Region `json:"regions"`
}

func (m *Model) Info(mode string) Info {
	in := Info{Mode: mode, Regions: Regions}
	if rtt, j, ok := m.Meter.Measured(); ok {
		v := math.Round(float64(rtt)/float64(time.Millisecond)*10) / 10
		in.Measured = &v
		in.MJitter = math.Round(float64(j)/float64(time.Millisecond)*10) / 10
	}
	return in
}
