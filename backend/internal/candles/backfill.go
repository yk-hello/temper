package candles

import (
	"context"
	"time"
)

// Backfill 把从交易所补回来的历史 K 线并进序列：只补比现有数据更早的部分；
// 重叠的那一根合并（开盘价用历史的，最高最低取两边，收盘价用实时的）。
func (s *Store) Backfill(instrument string, interval int64, hist []Candle) {
	if len(hist) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ser := s.m[instrument]
	if ser == nil {
		ser = map[int64]*series{1000: {ms: 1000, keep: 7200}, 60000: {ms: 60000, keep: 4320}}
		s.m[instrument] = ser
	}
	x := ser[interval]
	if x == nil {
		return
	}
	if len(x.bars) == 0 {
		x.bars = append([]Candle(nil), hist...)
		return
	}
	first := x.bars[0]
	var out []Candle
	for _, h := range hist {
		switch {
		case h.T < first.T:
			out = append(out, h)
		case h.T == first.T:
			m := first
			m.O = h.O
			m.H, m.L = max(h.H, first.H), min(h.L, first.L)
			m.VB, m.VS = max(h.VB, first.VB), max(h.VS, first.VS)
			x.bars[0] = m
			// 开机那一分钟一半来自补的历史、一半来自实时成交，拼出来的和币安差一点（11-17 走查）：
			// 记成窟窿，这一分钟走完以后 Heal 用交易所的完整数据换掉
			if interval >= 60_000 {
				x.gaps = append(x.gaps, gap{from: first.T, to: first.T})
			}
		}
	}
	x.bars = append(out, x.bars...)
	if len(x.bars) > x.keep {
		x.bars = x.bars[len(x.bars)-x.keep:]
	}
}

// Heal 用交易所的 1 分钟线补断流留下的窟窿（电脑睡眠、断网之后），成功补上的返回根数。
// 取不到就把窟窿放回去，下次再试。
func (s *Store) Heal(ctx context.Context) int {
	if s.Remote == nil {
		return 0
	}
	s.mu.Lock()
	todo := map[string][]gap{}
	now := s.now()
	for inst, ser := range s.m {
		x := ser[60_000]
		if x == nil || len(x.gaps) == 0 {
			continue
		}
		// 窟窿最后那一分钟还没走完就先不补：现在取到的也只是一半，补了以后就不会再换成完整的
		var later []gap
		for _, g := range x.gaps {
			if g.to+60_000 > now {
				later = append(later, g)
			} else {
				todo[inst] = append(todo[inst], g)
			}
		}
		x.gaps = later
	}
	s.mu.Unlock()
	total := 0
	for inst, gaps := range todo {
		for i, g := range gaps {
			bars, err := s.Remote.minutes(ctx, inst, g.from, g.to)
			if err != nil {
				s.mu.Lock()
				x := s.m[inst][60_000]
				x.gaps = append(append([]gap(nil), gaps[i:]...), x.gaps...)
				s.mu.Unlock()
				break
			}
			s.mu.Lock()
			s.m[inst][60_000].merge(bars)
			s.mu.Unlock()
			total += len(bars)
		}
	}
	return total
}

// RunHeal 定期补窟窿，直到 ctx 结束。
func (s *Store) RunHeal(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Heal(ctx)
		}
	}
}

// merge 把交易所的 K 线并进序列：没有的插进去；已有的以交易所为准。
// 最新那根还在走，开盘价用交易所的，高低点和成交量取两边，收盘价留实时的。
func (x *series) merge(hist []Candle) {
	if len(hist) == 0 {
		return
	}
	out := make([]Candle, 0, len(x.bars)+len(hist))
	last := int64(-1)
	if n := len(x.bars); n > 0 {
		last = x.bars[n-1].T
	}
	i, j := 0, 0
	for i < len(x.bars) || j < len(hist) {
		switch {
		case j == len(hist) || (i < len(x.bars) && x.bars[i].T < hist[j].T):
			out = append(out, x.bars[i])
			i++
		case i == len(x.bars) || hist[j].T < x.bars[i].T:
			out = append(out, hist[j])
			j++
		default:
			m := hist[j]
			if m.T == last {
				b := x.bars[i]
				m.H, m.L, m.C = max(m.H, b.H), min(m.L, b.L), b.C
				m.VB, m.VS, m.N = max(m.VB, b.VB), max(m.VS, b.VS), max(m.N, b.N)
			}
			out = append(out, m)
			i++
			j++
		}
	}
	if len(out) > x.keep {
		out = out[len(out)-x.keep:]
	}
	x.bars = out
}

// FetchBinance 从币安 U 本位合约公开接口取最近 limit 根 1 分钟 K 线（最多 1500 根，约 25 小时）。
// 重启后图表不再是空的（K 线平时只在内存里）。
func FetchBinance(ctx context.Context, symbol string, limit int) ([]Candle, error) {
	return fetch(ctx, symbol, "1m", 0, limit)
}
