package highlight

import (
	"context"
	"log/slog"
	"sort"
	"time"

	"qs/internal/journal"
)

// Acct 一个模拟账户。
type Acct struct {
	ID, User int64
	Initial  float64
}

// Sources 看什么。都由 main 接上真正的引擎和交易日志。
type Sources struct {
	Accounts func(ctx context.Context) []Acct
	FillSeq  func(acct int64) int64
	Trades   func(ctx context.Context, user, acct int64) ([]journal.Trade, error)
	Leverage func(acct int64, inst string) int
	// Excursion 持仓中最深浮亏、最高浮盈（价格距离，复盘模块按逐笔成交或 1 分钟线算）。算不出来时 ok = false
	Excursion func(ctx context.Context, acct int64, t journal.Trade) (mae, mfe float64, ok bool)
}

// Watcher 每三秒看一眼：哪个账户有新成交，就重算它的交易，看刚平掉的那笔够不够门槛。
// 第一轮只记下现状：重启前平掉的交易不补发。
type Watcher struct {
	S    *Store
	Src  Sources
	Log  *slog.Logger
	Push func(ctx context.Context, h *Highlight) // 发站内通知

	list   []Acct
	seq    map[int64]int64
	seen   map[int64]int64 // 每个账户看到哪一笔平仓了（平仓时刻）
	known  map[int64]bool
	booted bool
	n      int
}

// Run 一直跑。
func (w *Watcher) Run(ctx context.Context) {
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.Tick(ctx, time.Now().UnixMilli())
		}
	}
}

func lastClose(ts []journal.Trade) int64 {
	var m int64
	for _, t := range ts {
		m = max(m, t.CloseTime)
	}
	return m
}

// Tick 看一轮。
func (w *Watcher) Tick(ctx context.Context, now int64) {
	if !w.S.Ready() || w.Src.Accounts == nil || w.Src.Trades == nil {
		return
	}
	if w.seq == nil {
		w.seq, w.seen, w.known = map[int64]int64{}, map[int64]int64{}, map[int64]bool{}
	}
	w.n++
	if w.list == nil || w.n%5 == 1 {
		w.list = w.Src.Accounts(ctx)
	}
	for _, a := range w.list {
		if w.known[a.ID] && w.Src.FillSeq != nil && w.Src.FillSeq(a.ID) == w.seq[a.ID] {
			continue
		}
		seq := int64(0)
		if w.Src.FillSeq != nil {
			seq = w.Src.FillSeq(a.ID)
		}
		ts, err := w.Src.Trades(ctx, a.User, a.ID)
		if err != nil {
			continue
		}
		w.seq[a.ID] = seq
		if !w.known[a.ID] {
			w.known[a.ID] = true
			if !w.booted { // 重启：之前平掉的不补发。启动之后新开的账户从头算
				w.seen[a.ID] = lastClose(ts)
				continue
			}
		}
		var fresh []journal.Trade
		for _, t := range ts {
			if t.CloseTime > w.seen[a.ID] {
				fresh = append(fresh, t)
			}
		}
		if len(fresh) == 0 {
			continue
		}
		sort.Slice(fresh, func(i, j int) bool { return fresh[i].CloseTime < fresh[j].CloseTime })
		w.seen[a.ID] = lastClose(ts)
		need := w.S.Min(ctx, a.User)
		for _, t := range fresh {
			w.judge(ctx, a, ts, t, need, now)
		}
	}
	w.booted = true
}

func (w *Watcher) lev(a Acct, inst string) int {
	if IsSpot(inst) || w.Src.Leverage == nil {
		return 1
	}
	return max(1, w.Src.Leverage(a.ID, inst))
}

func (w *Watcher) judge(ctx context.Context, a Acct, all []journal.Trade, t journal.Trade, need float64, now int64) {
	lev := w.lev(a, t.Instrument)
	if !Qualifies(t, lev, need) {
		return
	}
	sn := Snap{
		TradeID: t.ID, Instrument: t.Instrument, Side: t.Side, Origin: t.Origin, Mode: "sim", Leverage: lev,
		OpenTime: t.OpenTime, CloseTime: t.CloseTime, Entry: t.Entry, Exit: t.Exit,
		ROE: round(ROE(t, lev), 6), AcctRet: round(AcctRet(t, all, a.Initial), 6), Net: round(t.Net, 8), Qty: t.Qty,
	}
	if w.Src.Excursion != nil {
		if mae, mfe, ok := w.Src.Excursion(ctx, a.ID, t); ok {
			x, y := Excursion(t, lev, mae, mfe)
			sn.MAE, sn.MFE, sn.PathKnown = round(x, 6), round(y, 6), true
		}
	}
	h, err := w.S.Add(ctx, a.User, a.ID, sn, now)
	if err != nil {
		if w.Log != nil {
			w.Log.Warn("高光没记上", "err", err)
		}
		return
	}
	if h != nil && w.Push != nil {
		w.Push(ctx, h)
	}
}

func round(v float64, d int) float64 {
	p := 1.0
	for i := 0; i < d; i++ {
		p *= 10
	}
	if v >= 0 {
		return float64(int64(v*p+0.5)) / p
	}
	return -float64(int64(-v*p+0.5)) / p
}
