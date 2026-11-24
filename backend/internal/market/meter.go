package market

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// Meter 数每条线路每秒多少条消息、从交易所发出到我们处理的时间（可视化 11：系统控制台）。
type Meter struct {
	mu    sync.Mutex
	v     map[string]*meterLine
	Now   func() time.Time
	start time.Time
}

type meterLine struct {
	total, trades, depth, marks       int64
	lastTotal, lastTrades, lastDepth  int64
	lastMarks                         int64
	lastAt                            time.Time
	ms                                float64
	rate, tradeRate, depthRate, markR float64
	lastMsg                           int64
}

// LineStat 一条线路现在的数字。
type LineStat struct {
	Venue  string  `json:"venue"`
	Rate   float64 `json:"rate"`
	Trades float64 `json:"trades"`
	Depth  float64 `json:"depth"`
	Marks  float64 `json:"marks"`
	Ms     float64 `json:"ms"`
	Total  int64   `json:"total"`
	Last   int64   `json:"last"`
}

func (m *Meter) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// Add 记一条事件。
func (m *Meter) Add(ev Event) {
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.v == nil {
		m.v = map[string]*meterLine{}
	}
	key := ev.Venue
	if strings.HasSuffix(ev.Instrument, "-SPOT") {
		key = "binance-spot"
	}
	l := m.v[key]
	if l == nil {
		l = &meterLine{lastAt: now}
		m.v[key] = l
	}
	l.total++
	l.lastMsg = now.UnixMilli()
	switch ev.Kind {
	case KindTrade:
		l.trades++
	case KindDepth, KindBook:
		l.depth++
	case KindMark:
		l.marks++
	}
	// 只用实时推送的算延迟：标记价和 24 小时统计每秒才推一次，时间戳是整批的。
	// 现货的 depth20 没有事件时间（解析时填的本机时间），只能看成交。
	real := ev.Kind == KindTrade || ((ev.Kind == KindDepth || ev.Kind == KindBook) && key != "binance-spot")
	if real && ev.Time > 0 {
		d := float64(now.UnixMilli() - ev.Time)
		if d < 0 {
			d = 0
		}
		if d > 5000 { // 重连后会补发最近的成交，时间戳是旧的。真的断流时看消息数和上一条的时间
			d = 5000
		}
		if l.ms == 0 {
			l.ms = d
		} else {
			l.ms += (d - l.ms) * 0.05
		}
	}
}

// Stats 每条线路的数字，按总量从大到小。隔不到半秒再问，回上一次算的。
func (m *Meter) Stats() []LineStat {
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]LineStat, 0, len(m.v))
	for name, l := range m.v {
		if dt := now.Sub(l.lastAt).Seconds(); dt >= 0.5 {
			l.rate = float64(l.total-l.lastTotal) / dt
			l.tradeRate = float64(l.trades-l.lastTrades) / dt
			l.depthRate = float64(l.depth-l.lastDepth) / dt
			l.markR = float64(l.marks-l.lastMarks) / dt
			l.lastTotal, l.lastTrades, l.lastDepth, l.lastMarks, l.lastAt = l.total, l.trades, l.depth, l.marks, now
		}
		out = append(out, LineStat{Venue: name, Rate: l.rate, Trades: l.tradeRate, Depth: l.depthRate, Marks: l.markR, Ms: l.ms, Total: l.total, Last: l.lastMsg})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Total > out[j].Total })
	return out
}
