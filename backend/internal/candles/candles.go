// Package candles 把逐笔成交聚合成 1 秒和 1 分钟 K 线（行情页的图表用）。
// 只在内存里保留最近一段：1 秒线 2 小时，1 分钟线 3 天。展示用，所以用 float64。
package candles

import (
	"strconv"
	"sync"
	"time"
)

type Candle struct {
	T  int64   `json:"t"` // 开始时间（毫秒）
	O  float64 `json:"o"`
	H  float64 `json:"h"`
	L  float64 `json:"l"`
	C  float64 `json:"c"`
	VB float64 `json:"vb"` // 主动买入量
	VS float64 `json:"vs"` // 主动卖出量
	N  int     `json:"n"`  // 成交笔数
}

type series struct {
	ms   int64
	keep int
	bars []Candle
	gaps []gap // 断流留下的窟窿，等 Heal 从交易所补
}

// gap 从 from 那根到 to 那根之间断过流（电脑睡眠、断网）。这两根本身也只有一部分成交，要一起补。
type gap struct{ from, to int64 }

func (s *series) add(t int64, px, qty float64, buy bool) {
	start := t - t%s.ms
	n := len(s.bars)
	if n == 0 || s.bars[n-1].T < start {
		o := px
		// 分钟线按币安的算法：开盘价就是这一根的第一笔成交，高低点只看这一根自己的成交。
		// 1 秒线紧挨着上一根时从上一根的收盘价接起，图上才是连续的。断流后不接，不然高低点会被拉宽。
		if n > 0 && s.ms < 60_000 && s.bars[n-1].T == start-s.ms {
			o = s.bars[n-1].C
		}
		if n > 0 && s.ms >= 60_000 && start > s.bars[n-1].T+s.ms {
			s.gaps = append(s.gaps, gap{from: s.bars[n-1].T, to: start})
		}
		s.bars = append(s.bars, Candle{T: start, O: o, H: max(o, px), L: min(o, px), C: px})
		if len(s.bars) > s.keep*2 {
			s.bars = append([]Candle(nil), s.bars[len(s.bars)-s.keep:]...)
		}
		// 按砍完以后的长度找最新一根：原来是 n++，砍半那一刻下标越界，1 秒线连续攒满 4 小时后端就崩（11-18 开了一晚上，一晚崩两次）
		n = len(s.bars)
	} else if s.bars[n-1].T > start {
		return // 乱序的旧成交，忽略
	}
	b := &s.bars[n-1]
	b.H, b.L, b.C = max(b.H, px), min(b.L, px), px
	if buy {
		b.VB += qty
	} else {
		b.VS += qty
	}
	b.N++
}

func (s *series) since(t int64) []Candle {
	i := len(s.bars)
	for i > 0 && s.bars[i-1].T >= t {
		i--
	}
	return append([]Candle(nil), s.bars[i:]...)
}

type Store struct {
	mu sync.RWMutex
	m  map[string]map[int64]*series // 品种 → 周期 → 序列
	// Remote 内存里没有的更早历史从交易所补（模拟行情时为空）
	Remote *Remote
	// Now 测试时换掉；空 = 真实时间
	Now func() time.Time
}

func (s *Store) now() int64 {
	if s.Now != nil {
		return s.Now().UnixMilli()
	}
	return time.Now().UnixMilli()
}

func New() *Store { return &Store{m: map[string]map[int64]*series{}} }

// Trade 记录一笔成交。价格和数量是交易所给的字符串。
func (s *Store) Trade(instrument string, t int64, price, qty string, buy bool) {
	px, err1 := strconv.ParseFloat(price, 64)
	q, err2 := strconv.ParseFloat(qty, 64)
	if err1 != nil || err2 != nil || px <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ser := s.m[instrument]
	if ser == nil {
		ser = map[int64]*series{1000: {ms: 1000, keep: 7200}, 60000: {ms: 60000, keep: 4320}}
		s.m[instrument] = ser
	}
	for _, x := range ser {
		x.add(t, px, q, buy)
	}
}

// Get 某品种某周期（1000 或 60000 毫秒）从 since 开始的 K 线。
func (s *Store) Get(instrument string, interval, since int64) []Candle {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if ser := s.m[instrument][interval]; ser != nil {
		return ser.since(since)
	}
	return []Candle{}
}
