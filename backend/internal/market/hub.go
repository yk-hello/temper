package market

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

// Hub 汇总所有行情更新，按固定间隔合并成一帧推给所有订阅者（前端）。
//
// 交易所的推送频率远高于屏幕需要的频率，所以这里"合并降频"：
// 不管一秒内来多少条更新，前端只会收到 1000/interval 帧，这是前端零卡顿的关键（见 05 第 5 节）。
// 慢的订阅者不会拖慢其他人：发不进去就跳过这一帧，下一帧仍是完整快照。
type Hub struct {
	interval time.Duration
	now      func() time.Time

	mu     sync.Mutex
	states map[string]*InstrumentState
	order  []string
	status SourceStatus
	dirty  bool
	subs   map[*Subscriber]struct{}
}

type Subscriber struct {
	C chan []byte
}

// Snapshot 是推给前端的一帧。
type Snapshot struct {
	Type        string            `json:"type"`
	Time        int64             `json:"ts"`
	Status      SourceStatus      `json:"status"`
	Instruments []InstrumentState `json:"instruments"`
}

func NewHub(instruments []string, interval time.Duration) *Hub {
	h := &Hub{
		interval: interval,
		now:      time.Now,
		states:   make(map[string]*InstrumentState),
		subs:     make(map[*Subscriber]struct{}),
		status:   SourceStatus{Connections: map[string]bool{}},
	}
	for _, in := range instruments {
		h.ensure(in)
	}
	return h
}

func (h *Hub) ensure(instrument string) *InstrumentState {
	st, ok := h.states[instrument]
	if !ok {
		st = &InstrumentState{Instrument: instrument}
		h.states[instrument] = st
		h.order = append(h.order, instrument)
	}
	return st
}

// Publish 应用一条行情更新。
func (h *Hub) Publish(ev Event) {
	if ev.Instrument == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	st := h.ensure(ev.Instrument)
	if ev.Symbol != "" {
		st.Symbol = ev.Symbol
	}
	if ev.Venue != "" {
		st.Venue = ev.Venue
	}
	switch ev.Kind {
	case KindTrade:
		st.Last, st.LastSide, st.LastAt = ev.Price, ev.Side, ev.Time
		st.Trades++
	case KindBook:
		st.Bid, st.BidQty, st.Ask, st.AskQty = ev.Bid, ev.BidQty, ev.Ask, ev.AskQty
	case KindMark:
		st.Mark, st.FundingRate, st.NextFundingTime = ev.Mark, ev.FundingRate, ev.NextFundingTime
	case KindTicker:
		st.Change24hPct, st.QuoteVolume24h = ev.Change24hPct, ev.QuoteVolume24h
		st.High24h, st.Low24h = ev.High24h, ev.Low24h
		if st.Last == "" {
			st.Last = ev.Price
		}
	default:
		return
	}
	st.UpdatedAt = ev.Time
	h.status.LastMessageAt = h.now().UnixMilli()
	h.dirty = true
}

func (h *Hub) UpdateStatus(fn func(*SourceStatus)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.status.Connections == nil {
		h.status.Connections = map[string]bool{}
	}
	fn(&h.status)
	h.dirty = true
}

// Status 返回数据源状态的副本。
func (h *Hub) Status() SourceStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	return copyStatus(h.status)
}

func copyStatus(s SourceStatus) SourceStatus {
	c := s
	c.Connections = make(map[string]bool, len(s.Connections))
	for k, v := range s.Connections {
		c.Connections[k] = v
	}
	if s.Lag != nil {
		c.Lag = make(map[string]int64, len(s.Lag))
		for k, v := range s.Lag {
			c.Lag[k] = v
		}
	}
	return c
}

// Snapshot 返回当前完整状态。reset 为 true 时清零"区间成交笔数"。
func (h *Hub) snapshot(reset bool) Snapshot {
	snap := Snapshot{Type: "snapshot", Time: h.now().UnixMilli(), Status: copyStatus(h.status)}
	for _, in := range h.order {
		st := h.states[in]
		snap.Instruments = append(snap.Instruments, *st)
		if reset {
			st.Trades = 0
		}
	}
	return snap
}

func (h *Hub) Snapshot() Snapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.snapshot(false)
}

// Subscribers 现在连着多少个浏览器。
func (h *Hub) Subscribers() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// Subscribe 注册一个订阅者，返回取消函数。
func (h *Hub) Subscribe() (*Subscriber, func()) {
	s := &Subscriber{C: make(chan []byte, 2)}
	h.mu.Lock()
	h.subs[s] = struct{}{}
	h.mu.Unlock()
	return s, func() {
		h.mu.Lock()
		delete(h.subs, s)
		h.mu.Unlock()
	}
}

// Interval 合并推送的间隔（盘口推送也用它）。
func (h *Hub) Interval() time.Duration { return h.interval }

// Run 按间隔推送，直到 ctx 取消。
func (h *Hub) Run(ctx context.Context) {
	t := time.NewTicker(h.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.flush()
		}
	}
}

// flush 有变化时生成一帧并广播。导出给测试用的是 Flush。
func (h *Hub) flush() {
	h.mu.Lock()
	if !h.dirty {
		h.mu.Unlock()
		return
	}
	h.dirty = false
	snap := h.snapshot(true)
	subs := make([]*Subscriber, 0, len(h.subs))
	for s := range h.subs {
		subs = append(subs, s)
	}
	h.mu.Unlock()

	data, err := json.Marshal(snap)
	if err != nil {
		return
	}
	for _, s := range subs {
		select {
		case s.C <- data:
		default: // 订阅者太慢：丢掉这一帧，绝不阻塞
		}
	}
}
