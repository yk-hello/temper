package heat

// 补数据：断线、连接卡住、重启时没录到的时段，事后从币安补回价格线和成交。
// 币安不提供历史盘口，所以盘口补不回来，那段仍然算"没有录到盘口"，界面上如实标出来。
// 补回来的数据单独存在 heat_fill，不改已经录好的分钟块。

import (
	"context"
	"encoding/binary"
	"math"
	"sort"
	"time"
)

const (
	fillMinHole  = int64(3_000)         // 3 秒以内不算空缺（和 gapsOf 一致）
	fillTradeMax = int64(10 * 60_000)   // 10 分钟以内逐笔补；更长的用 1 分钟 K 线
	fillLookback = int64(24 * 3600_000) // 最多往回补 24 小时
	fillTries    = 30                   // 一段空缺最多重试几次（每分钟一次）
)

// Bar1m 一根 1 分钟 K 线里补数据要用的部分。
type Bar1m struct {
	T           int64
	O, C        float64
	QuoteUSD    float64 // 总成交额
	TakerBuyUSD float64 // 主动买入额
}

// Filler 能取历史的数据源。模拟盘口没有。
type Filler interface {
	// AggTrades 取 [from, to) 的逐笔成交。complete=false 表示翻页到上限还没取完。
	AggTrades(ctx context.Context, inst string, from, to int64) (ts []Trade, complete bool, err error)
	Klines1m(ctx context.Context, inst string, from, to int64) ([]Bar1m, error)
}

type hole struct {
	inst     string
	from, to int64
	tries    int
}

// fillRow 一分钟补回来的数据。
type fillRow struct {
	T         int64
	Mids      []float64 // 60 个；没有的是 0
	Trades    []Trade
	Buy, Sell float64 // 只有 K 线时：这一分钟的成交额，没有逐笔
}

// buildFill 拼出每分钟的补充数据：trades 覆盖 [from, split)，bars 覆盖 [split, to)。
func buildFill(from, split, to int64, trades []Trade, bars []Bar1m) map[int64]*fillRow {
	rows := map[int64]*fillRow{}
	row := func(t int64) *fillRow {
		m := t - t%60_000
		r := rows[m]
		if r == nil {
			r = &fillRow{T: m, Mids: make([]float64, 60)}
			rows[m] = r
		}
		return r
	}
	// 逐笔：每秒的价格取这一秒最后一笔，没有成交的秒沿用上一秒
	if len(trades) > 0 {
		last := trades[0].P
		i := 0
		for t := from - from%1000; t < split; t += 1000 {
			for i < len(trades) && trades[i].T < t+1000 {
				last = trades[i].P
				r := row(trades[i].T)
				r.Trades = append(r.Trades, trades[i])
				i++
			}
			row(t).Mids[(t%60_000)/1000] = last
		}
	}
	// K 线：一分钟内从开盘价直线走到收盘价，成交额按这一分钟里缺的比例算
	for _, b := range bars {
		n := 0
		for s := int64(0); s < 60; s++ {
			t := b.T + s*1000
			if t < split || t >= to {
				continue
			}
			row(t).Mids[s] = b.O + (b.C-b.O)*float64(s)/59
			n++
		}
		if n > 0 {
			r := row(b.T)
			f := float64(n) / 60
			r.Buy += b.TakerBuyUSD * f
			r.Sell += (b.QuoteUSD - b.TakerBuyUSD) * f
		}
	}
	return rows
}

func decodeMids(b []byte) ([]float64, error) {
	mb, err := inflate(b)
	if err != nil {
		return nil, err
	}
	out := make([]float64, len(mb)/4)
	for i := range out {
		out[i] = float64(math.Float32frombits(binary.LittleEndian.Uint32(mb[i*4:])))
	}
	return out, nil
}

// saveFill 写库。同一分钟可能有好几段空缺，所以先读出来合并。
func (r *Recorder) saveFill(ctx context.Context, inst string, rows map[int64]*fillRow) error {
	p := r.db()
	if p == nil {
		return nil
	}
	for _, row := range rows {
		var mb, tb []byte
		var buy, sell float64
		if p.QueryRow(ctx, `select mids,trades,buy,sell from heat_fill where instrument=$1 and t=$2`, inst, row.T).Scan(&mb, &tb, &buy, &sell) == nil {
			if old, err := decodeMids(mb); err == nil {
				for i := range row.Mids {
					if row.Mids[i] <= 0 && i < len(old) {
						row.Mids[i] = old[i]
					}
				}
			}
			if ts, err := DecodeTrades(row.T, tb); err == nil {
				row.Trades = append(ts, row.Trades...)
				sort.SliceStable(row.Trades, func(i, j int) bool { return row.Trades[i].T < row.Trades[j].T })
			}
			row.Buy += buy
			row.Sell += sell
		}
		c := &Chunk{T: row.T, Mids: row.Mids, Trades: row.Trades}
		if _, err := p.Exec(ctx, `insert into heat_fill (instrument,t,mids,trades,buy,sell) values ($1,$2,$3,$4,$5,$6)
			on conflict (instrument,t) do update set mids=excluded.mids, trades=excluded.trades, buy=excluded.buy, sell=excluded.sell`,
			inst, row.T, c.EncodeMids(), c.EncodeTrades(), row.Buy, row.Sell); err != nil {
			return err
		}
	}
	return nil
}

// loadFill 读 [from, to) 的补充数据。读不到也不影响看图。
func (r *Recorder) loadFill(ctx context.Context, inst string, from, to int64) []*fillRow {
	p := r.db()
	if p == nil {
		return nil
	}
	start := from - ((from%60_000)+60_000)%60_000
	rows, err := p.Query(ctx, `select t,mids,trades,buy,sell from heat_fill where instrument=$1 and t>=$2 and t<$3 order by t`, inst, start, to)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*fillRow
	for rows.Next() {
		var mb, tb []byte
		f := &fillRow{}
		if rows.Scan(&f.T, &mb, &tb, &f.Buy, &f.Sell) != nil {
			continue
		}
		var err error
		if f.Mids, err = decodeMids(mb); err != nil {
			continue
		}
		if f.Trades, err = DecodeTrades(f.T, tb); err != nil {
			f.Trades = nil
		}
		out = append(out, f)
	}
	return out
}

// fillHole 补一段空缺。
func (r *Recorder) fillHole(ctx context.Context, inst string, from, to int64) error {
	f, ok := r.Feed.(Filler)
	if !ok {
		return nil
	}
	now := time.Now().UnixMilli()
	to = min(to, now-1000)
	from = max(from, now-fillLookback)
	if to-from < fillMinHole {
		return nil
	}
	var ts []Trade
	split := from
	if to-from <= fillTradeMax {
		var complete bool
		var err error
		if ts, complete, err = f.AggTrades(ctx, inst, from, to); err != nil {
			return err
		}
		if len(ts) > 0 {
			split = ts[len(ts)-1].T + 1
		}
		if complete {
			split = to
		}
	}
	var bars []Bar1m
	if split < to {
		var err error
		if bars, err = f.Klines1m(ctx, inst, split, to); err != nil {
			return err
		}
	}
	return r.saveFill(ctx, inst, buildFill(from, split, to, ts, bars))
}

// scanHoles 启动时查一遍最近 24 小时缺了哪些整分钟（关机、重启、Docker 没开的时候）。
func (r *Recorder) scanHoles(ctx context.Context, inst string) []hole {
	p := r.db()
	if p == nil {
		return nil
	}
	now := time.Now().UnixMilli()
	rows, err := p.Query(ctx, `select t from heat_chunks where instrument=$1 and t>=$2 union select t from heat_fill where instrument=$1 and t>=$2 order by t`, inst, now-fillLookback)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var known []int64
	for rows.Next() {
		var t int64
		if rows.Scan(&t) == nil {
			known = append(known, t)
		}
	}
	return holesBetween(inst, known, now-now%60_000-60_000)
}

// holesBetween known 是排好序的分钟开始时间；从第一块算起，到 end 为止缺的整分钟连成段。
func holesBetween(inst string, known []int64, end int64) []hole {
	if len(known) == 0 {
		return nil
	}
	var out []hole
	cur := known[0]
	for _, t := range append(append([]int64(nil), known[1:]...), end) {
		if t-cur > 60_000 {
			out = append(out, hole{inst: inst, from: cur + 60_000, to: t})
		}
		cur = max(cur, t)
	}
	return out
}

// filler 补数据的后台任务：启动时扫一遍，之后录制发现空缺就补，失败的每分钟重试。
func (r *Recorder) filler(ctx context.Context) {
	if _, ok := r.Feed.(Filler); !ok {
		return
	}
	var retry []hole
	do := func(h hole) {
		c, cancel := context.WithTimeout(ctx, time.Minute)
		err := r.fillHole(c, h.inst, h.from, h.to)
		cancel()
		if err != nil {
			if r.Log != nil {
				r.Log.Warn("补频谱数据失败", "instrument", h.inst, "err", err)
			}
			if h.tries++; h.tries < fillTries {
				retry = append(retry, h)
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(200 * time.Millisecond):
		}
	}
	// 等数据库连好、网络稳定
	select {
	case <-ctx.Done():
		return
	case <-time.After(20 * time.Second):
	}
	for _, inst := range r.Instruments() {
		retry = append(retry, r.scanHoles(ctx, inst)...)
	}
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for ctx.Err() == nil {
		batch := retry
		retry = nil
		for _, h := range batch {
			do(h)
		}
		select {
		case <-ctx.Done():
			return
		case h := <-r.holes:
			do(h)
		case <-tick.C:
		}
	}
}
