package notify

import (
	"context"
	"math"
	"strconv"
	"time"
)

// 报警线。开和关用两条线，价格在线附近来回时不会响个不停。
const (
	MarginFire  = 0.70  // 维持保证金占权益 70% 时提醒
	MarginRearm = 0.55  // 回到 55% 以下才会再响
	LiqFire     = 0.05  // 离强平价不到 5%
	LiqRearm    = 0.075 // 拉开到 7.5% 以上才会再响
	WhaleMin    = 5e6   // 大户动作的金额下限，再小就太吵了
	WhaleGapMs  = 5 * 60 * 1000
	EconAheadMs = 60 * 60 * 1000
	refireMs    = 60 * 60 * 1000 // 重启后一小时内响过的危险提醒不再响
)

// AcctRef 一个模拟账户。
type AcctRef struct {
	ID, User int64
	Name     string
}

// PosSnap 一个持仓。
type PosSnap struct {
	Inst      string
	Mark, Liq float64
}

// NoticeSnap 引擎的事件：强平、止盈止损。
type NoticeSnap struct {
	Seq        int64
	Kind, Inst string
	Px, Loss   float64
	Partial    string // 部分强平平掉的数量（D103）；空 = 全部强平
}

// Snap 一个账户此刻的样子。
type Snap struct {
	Ratio     float64
	Positions []PosSnap
	Notices   []NoticeSnap
	Locked    bool
	Rule      string
}

// FillSnap 一笔成交。
type FillSnap struct {
	Seq     int64
	Inst    string
	Side    string
	Qty, Px float64
	Liq     bool
}

// BotSnap 一个机器人。
type BotSnap struct {
	ID           int64
	Inst, Reason string
	Paused       bool
}

// FollowSnap 一个策略跟随。
type FollowSnap struct {
	ID      int64
	Stopped bool
	Reason  string
}

// WhaleSnap 一条链上大户动作。
type WhaleSnap struct {
	T          int64
	Coin, Kind string
	Value      float64
}

// EconSnap 一个经济事件。
type EconSnap struct {
	T   int64
	Key string
}

// Sources 看什么。都是读内存，由 main 接上真正的引擎。
type Sources struct {
	Accounts func(ctx context.Context) []AcctRef
	Snap     func(acct int64) (*Snap, bool)
	FillSeq  func(acct int64) int64
	Fills    func(acct, after int64) []FillSnap
	Bots     func(user int64) []BotSnap
	Follows  func(user int64) []FollowSnap
	Price    func(inst string) float64
	Metrics  func(inst string) map[string]float64 // 自定义提醒条件用（rules.go）
	Whales   func() []WhaleSnap
	Econ     func() []EconSnap
}

type acctState struct {
	notice, fill int64
	ratio        bool
	liq          map[string]bool
	locked       bool
}

// Watcher 每两秒看一眼，发现变化就发通知。
// 第一轮只记下现状，不把重启前的事再报一遍。
type Watcher struct {
	S   *Service
	Src Sources

	accts   map[int64]*acctState
	bots    map[int64]bool
	follows map[int64]bool
	seenU   map[int64]bool
	econ    map[string]bool
	whaleT  int64
	whaleAt map[string]int64
	booted  bool
	n       int
	list    []AcctRef
}

// Run 一直跑。
func (w *Watcher) Run(ctx context.Context) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.Tick(ctx)
		}
	}
}

// Tick 看一轮。
func (w *Watcher) Tick(ctx context.Context) {
	if w.accts == nil {
		w.accts, w.bots, w.follows, w.seenU = map[int64]*acctState{}, map[int64]bool{}, map[int64]bool{}, map[int64]bool{}
		w.econ, w.whaleAt = map[string]bool{}, map[string]int64{}
	}
	w.n++
	if w.Src.Accounts != nil && (w.list == nil || w.n%3 == 1) {
		w.list = w.Src.Accounts(ctx)
		alive := map[int64]bool{}
		for _, a := range w.list {
			alive[a.ID] = true
		}
		for id := range w.accts {
			if !alive[id] {
				delete(w.accts, id)
			}
		}
	}
	for _, a := range w.list {
		w.account(ctx, a)
	}
	if w.Src.Price != nil {
		w.S.CheckAlerts(ctx, w.Src.Price)
	}
	if w.Src.Metrics != nil && w.n%5 == 1 { // 每 10 秒看一遍自定义提醒条件
		w.S.CheckRules(ctx, w.Src.Metrics)
	}
	if w.n%3 == 1 {
		w.people(ctx)
	}
	if w.n%15 == 1 {
		w.whales(ctx)
	}
	if w.n%30 == 1 {
		w.calendar(ctx)
	}
	w.booted = true
}

func (w *Watcher) account(ctx context.Context, a AcctRef) {
	if w.Src.Snap == nil {
		return
	}
	v, ok := w.Src.Snap(a.ID)
	if !ok {
		return
	}
	st := w.accts[a.ID]
	first := st == nil
	if first {
		st = &acctState{liq: map[string]bool{}}
		w.accts[a.ID] = st
		if !w.booted { // 重启：之前的事不再报。启动之后新开的账户从头算
			for _, n := range v.Notices {
				st.notice = max(st.notice, n.Seq)
			}
			if w.Src.FillSeq != nil {
				st.fill = w.Src.FillSeq(a.ID)
			}
			st.locked = v.Locked
		}
	}
	// 重启后的危险提醒：一小时内响过就不再响，没响过要响，停机时可能已经变危险了
	push := func(kind, key string, p map[string]string) {
		if first && !w.booted && w.S.recent(ctx, a.User, kind, key, refireMs) {
			return
		}
		w.S.Push(ctx, a.User, kind, p)
	}
	for _, n := range v.Notices {
		if n.Seq <= st.notice {
			continue
		}
		st.notice = n.Seq
		switch n.Kind {
		case "liquidation":
			p := map[string]string{"inst": n.Inst, "loss": fmt2(n.Loss), "done": "1", "acct": a.Name}
			if n.Partial != "" {
				p["partial"] = n.Partial
			}
			if n.Px > 0 { // 全仓强平一次平掉所有仓位，没有单一的价格
				p["px"] = fmtPx(n.Px)
			}
			w.S.Push(ctx, a.User, KLiq, p)
		case "tp", "sl":
			w.S.Push(ctx, a.User, KStop, map[string]string{"inst": n.Inst, "px": fmtPx(n.Px), "what": n.Kind, "acct": a.Name})
		}
	}
	// 保证金率
	if !st.ratio && v.Ratio >= MarginFire {
		st.ratio = true
		push(KMargin, "", map[string]string{"ratio": strconv.Itoa(int(math.Round(v.Ratio * 100))), "acct": a.Name})
	} else if st.ratio && v.Ratio < MarginRearm {
		st.ratio = false
	}
	// 离强平价的距离
	open := map[string]bool{}
	for _, p := range v.Positions {
		if p.Mark <= 0 || p.Liq <= 0 {
			continue
		}
		open[p.Inst] = true
		d := math.Abs(p.Mark-p.Liq) / p.Mark
		if !st.liq[p.Inst] && d < LiqFire {
			st.liq[p.Inst] = true
			push(KLiq, p.Inst, map[string]string{"inst": p.Inst, "px": fmtPx(p.Liq), "dist": fmt1(d * 100), "acct": a.Name})
		} else if st.liq[p.Inst] && d > LiqRearm {
			st.liq[p.Inst] = false
		}
	}
	for k := range st.liq {
		if !open[k] { // 平掉了，下次开仓重新算
			delete(st.liq, k)
		}
	}
	// 纪律锁
	if v.Locked && !st.locked {
		w.S.Push(ctx, a.User, KLock, map[string]string{"rule": v.Rule, "acct": a.Name})
	}
	st.locked = v.Locked
	// 成交：默认关，打开了才去读
	if w.Src.Fills != nil {
		w.S.mu.Lock()
		on := w.S.prefOf(a.User, KFill)
		w.S.mu.Unlock()
		if on.Site || on.TG {
			for _, f := range w.Src.Fills(a.ID, st.fill) {
				st.fill = max(st.fill, f.Seq)
				if f.Liq { // 强平已经单独报过
					continue
				}
				w.S.Push(ctx, a.User, KFill, map[string]string{"inst": f.Inst, "side": f.Side, "qty": trimNum(f.Qty), "px": fmtPx(f.Px), "acct": a.Name})
			}
		} else if w.Src.FillSeq != nil {
			st.fill = w.Src.FillSeq(a.ID) // 关着的时候不攒，打开后不补发旧的
		}
	}
}

// people 按人看：机器人、跟随。
func (w *Watcher) people(ctx context.Context) {
	done := map[int64]bool{}
	for _, a := range w.list {
		if done[a.User] {
			continue
		}
		done[a.User] = true
		first := !w.seenU[a.User] && !w.booted
		w.seenU[a.User] = true
		if w.Src.Bots != nil {
			for _, b := range w.Src.Bots(a.User) {
				bad := b.Paused && b.Reason != "USER" // 自己点的暂停（USER）不用提醒自己；手动单成交让机器人让路（MANUAL）要提醒（11-17 走查：原来写反了）
				if bad && !w.bots[b.ID] && !first {
					w.S.Push(ctx, a.User, KBot, map[string]string{"inst": b.Inst, "reason": b.Reason})
				}
				w.bots[b.ID] = bad
			}
		}
		if w.Src.Follows != nil {
			for _, f := range w.Src.Follows(a.User) {
				bad := f.Stopped && f.Reason == "max_loss" // 用户自己停的不算
				if bad && !w.follows[f.ID] && !first {
					w.S.Push(ctx, a.User, KFollow, map[string]string{"reason": f.Reason})
				}
				w.follows[f.ID] = bad
			}
		}
	}
}

// whales 大户动作。发给打开了这一项的人，同一币种五分钟最多一条。
func (w *Watcher) whales(ctx context.Context) {
	if w.Src.Whales == nil {
		return
	}
	list := w.Src.Whales()
	first := w.whaleT == 0
	last := w.whaleT
	for i := len(list) - 1; i >= 0; i-- { // 旧的在后，按时间顺序看
		e := list[i]
		if e.T <= w.whaleT {
			continue
		}
		last = max(last, e.T)
		if first || e.T-w.whaleAt[e.Coin] < WhaleGapMs {
			continue
		}
		w.whaleAt[e.Coin] = e.T
		for _, u := range w.S.Wants(KWhale) {
			w.S.Push(ctx, u, KWhale, map[string]string{"coin": e.Coin, "what": e.Kind, "value": fmtUSD(e.Value)})
		}
	}
	w.whaleT = max(last, 1)
}

// calendar 经济事件前一小时。
func (w *Watcher) calendar(ctx context.Context) {
	if w.Src.Econ == nil {
		return
	}
	now := w.S.now()
	for _, e := range w.Src.Econ() {
		k := e.Key + strconv.FormatInt(e.T, 10)
		if e.T <= now || e.T-now > EconAheadMs || w.econ[k] {
			continue
		}
		w.econ[k] = true
		for _, u := range w.S.Wants(KEcon) {
			w.S.Push(ctx, u, KEcon, map[string]string{"key": e.Key, "t": strconv.FormatInt(e.T, 10)})
		}
	}
}

// recent 这种提醒最近响过吗。
func (s *Service) recent(ctx context.Context, user int64, kind, inst string, within int64) bool {
	since := s.now() - within
	if s.Pool == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, it := range s.mem[user] {
			if it.Kind == kind && it.T >= since && it.Params["inst"] == inst {
				return true
			}
		}
		return false
	}
	var n int
	err := s.Pool.QueryRow(ctx, `select count(*) from notifications where user_id = $1 and kind = $2 and t >= $3 and coalesce(params->>'inst', '') = $4`, user, kind, since, inst).Scan(&n)
	return err == nil && n > 0
}

func fmtPx(v float64) string {
	a := math.Abs(v)
	switch {
	case a >= 1000:
		return strconv.FormatFloat(v, 'f', 1, 64)
	case a >= 1:
		return strconv.FormatFloat(v, 'f', 3, 64)
	}
	return strconv.FormatFloat(v, 'g', 5, 64)
}

func fmt2(v float64) string { return strconv.FormatFloat(v, 'f', 2, 64) }
func fmt1(v float64) string { return strconv.FormatFloat(v, 'f', 1, 64) }

func fmtUSD(v float64) string {
	if v >= 1e6 {
		return strconv.FormatFloat(v/1e6, 'f', 1, 64) + "M"
	}
	return strconv.FormatFloat(v/1e3, 'f', 0, 64) + "K"
}
