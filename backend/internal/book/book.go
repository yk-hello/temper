// Package book 保存每个交易所、每个品种最新的盘口（前 N 档）和最新成交价、标记价。
// 模拟成交（paper）按这里的盘口逐档成交；频谱记录器也从这里取快照。
package book

import (
	"sync"
	"time"

	"github.com/shopspring/decimal"
)

type Level struct {
	Px  decimal.Decimal
	Qty decimal.Decimal
}

// Book 是某一时刻的盘口。Bids 从高到低，Asks 从低到高。
type Book struct {
	Venue      string
	Instrument string
	Bids       []Level
	Asks       []Level
	Time       int64 // 交易所时间（毫秒）
	Recv       int64 // 本机收到的时间（毫秒，Store 填写；用来判断盘口是不是还在更新）
}

func (b Book) BestBid() (Level, bool) {
	if len(b.Bids) == 0 {
		return Level{}, false
	}
	return b.Bids[0], true
}

func (b Book) BestAsk() (Level, bool) {
	if len(b.Asks) == 0 {
		return Level{}, false
	}
	return b.Asks[0], true
}

// Mid 中间价；只有一边时返回那一边。
func (b Book) Mid() (decimal.Decimal, bool) {
	bb, okb := b.BestBid()
	ba, oka := b.BestAsk()
	switch {
	case okb && oka:
		return bb.Px.Add(ba.Px).Div(decimal.NewFromInt(2)), true
	case okb:
		return bb.Px, true
	case oka:
		return ba.Px, true
	}
	return decimal.Zero, false
}

// Ticker 是一个品种的最新价格信息。
type Ticker struct {
	Last        decimal.Decimal
	Mark        decimal.Decimal
	Index       decimal.Decimal
	FundingRate decimal.Decimal
	NextFunding int64
	Time        int64
}

type key struct{ venue, instrument string }

// Store 线程安全。
type Store struct {
	mu      sync.RWMutex
	books   map[key]Book
	tickers map[key]Ticker
	subs    []func(venue, instrument string, px, qty decimal.Decimal, buy bool, t int64)
	trades  map[key][]TradeRec // 每个品种最近的成交（最多 keepTrades 条）
}

// TradeRec 一笔市场成交（"最新成交"列表用）。
type TradeRec struct {
	Time int64           `json:"t"`
	Px   decimal.Decimal `json:"px"`
	Qty  decimal.Decimal `json:"qty"`
	Buy  bool            `json:"buy"` // 主动买（吃卖单）
}

const keepTrades = 120

func NewStore() *Store {
	return &Store{books: map[key]Book{}, tickers: map[key]Ticker{}, trades: map[key][]TradeRec{}}
}

func (s *Store) SetBook(b Book) {
	b.Recv = time.Now().UnixMilli()
	s.mu.Lock()
	s.books[key{b.Venue, b.Instrument}] = b
	s.mu.Unlock()
}

// SetBookAt 同 SetBook，但指定本机收到时间（测试用）。
func (s *Store) SetBookAt(b Book, recv int64) {
	b.Recv = recv
	s.mu.Lock()
	s.books[key{b.Venue, b.Instrument}] = b
	s.mu.Unlock()
}

// SetTop 在没有深度数据时，用最优买卖价合成一个一档盘口（只在该品种没有深度数据或深度更旧时使用）。
func (s *Store) SetTop(venue, instrument string, bid, bidQty, ask, askQty decimal.Decimal, t int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key{venue, instrument}
	if cur, ok := s.books[k]; ok && len(cur.Bids) > 1 && t-cur.Time < 2000 {
		return
	}
	s.books[k] = Book{Venue: venue, Instrument: instrument, Bids: []Level{{bid, bidQty}}, Asks: []Level{{ask, askQty}}, Time: t, Recv: time.Now().UnixMilli()}
}

func (s *Store) Book(venue, instrument string) (Book, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.books[key{venue, instrument}]
	return b, ok
}

func (s *Store) UpdateTicker(venue, instrument string, f func(*Ticker)) {
	s.mu.Lock()
	k := key{venue, instrument}
	t := s.tickers[k]
	f(&t)
	s.tickers[k] = t
	s.mu.Unlock()
}

func (s *Store) Ticker(venue, instrument string) (Ticker, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tickers[key{venue, instrument}]
	return t, ok
}

// OnTrade 注册成交回调（模拟挂单成交用：市场成交价穿过挂单价时算挂单成交）。
func (s *Store) OnTrade(f func(venue, instrument string, px, qty decimal.Decimal, buy bool, t int64)) {
	s.mu.Lock()
	s.subs = append(s.subs, f)
	s.mu.Unlock()
}

// Trade 记录一笔市场成交，并通知订阅者。
func (s *Store) Trade(venue, instrument string, px, qty decimal.Decimal, buy bool, t int64) {
	s.UpdateTicker(venue, instrument, func(tk *Ticker) { tk.Last = px; tk.Time = t })
	s.mu.Lock()
	k := key{venue, instrument}
	tr := append(s.trades[k], TradeRec{Time: t, Px: px, Qty: qty, Buy: buy})
	if len(tr) > 2*keepTrades { // 攒到两倍再裁一次，避免每笔都拷贝
		tr = append([]TradeRec(nil), tr[len(tr)-keepTrades:]...)
	}
	s.trades[k] = tr
	subs := s.subs
	s.mu.Unlock()
	for _, f := range subs {
		f(venue, instrument, px, qty, buy, t)
	}
}

// RecentTrades 最近 n 笔成交，新的在前。
func (s *Store) RecentTrades(venue, instrument string, n int) []TradeRec {
	s.mu.RLock()
	defer s.mu.RUnlock()
	tr := s.trades[key{venue, instrument}]
	out := make([]TradeRec, 0, min(n, len(tr)))
	for i := len(tr) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, tr[i])
	}
	return out
}
