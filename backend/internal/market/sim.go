package market

import (
	"context"
	"math"
	"math/rand/v2"
	"strconv"
	"sync"
	"time"
)

// Sim 是模拟行情源：价格随机游走。只用于开发、测试和截图自检，
// 因为开发环境在美国，连不上币安合约行情。界面上会明确显示数据源是 sim。
type Sim struct {
	Symbols []string
	Seed    uint64
	Tick    time.Duration // 成交间隔，默认 40ms

	// BasePrice 不在内置表里的品种的起始价（和全市场方块图用同一个）
	BasePrice func(symbol string) float64

	mu      sync.Mutex
	forced  map[string]float64 // SetPrice 设的价格，下一个节拍生效
	added   []string           // 运行中加进来的品种，下一个节拍生效
	current map[string]float64 // 现价（方块图读）
}

// Add 运行中加一个品种（用户点开了别的币）。
func (s *Sim) Add(symbol string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.added = append(s.added, symbol)
	return true
}

// Price 某个品种的现价；还没在跑时 ok 是 false。
func (s *Sim) Price(symbol string) (float64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.current[symbol]
	return p, ok
}

func (s *Sim) takeAdded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.added
	s.added = nil
	return a
}

// SetPrice 把某个品种的价格直接设到 price，之后接着随机游走。只有模拟行情有这个功能，
// 用来在测试环境造出强平、止损这类不好等的场景（D75）。
func (s *Sim) SetPrice(symbol string, price float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.forced == nil {
		s.forced = map[string]float64{}
	}
	s.forced[symbol] = price
}

func (s *Sim) takeForced() map[string]float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.forced
	s.forced = nil
	return f
}

func (s *Sim) Name() string { return "sim" }

var simBase = map[string]float64{
	"BTCUSDT": 68250, "ETHUSDT": 3480, "SOLUSDT": 162, "BNBUSDT": 598, "XRPUSDT": 0.6123, "DOGEUSDT": 0.1534,
}

const simDepth = 20 // 盘口档数，和币安 depth20 一样

type simState struct {
	symbol           string
	price, open      float64
	high, low, vol   float64
	decimals         int
	nextFundingStart time.Time
}

func (s *Sim) Run(ctx context.Context, sink Sink) {
	sink.UpdateStatus(func(st *SourceStatus) { st.Name = "sim"; st.Connections["sim"] = true })
	rng := rand.New(rand.NewPCG(s.Seed, s.Seed^0x9e3779b97f4a7c15))
	tick := s.Tick
	if tick <= 0 {
		tick = 40 * time.Millisecond
	}
	states := make([]*simState, 0, len(s.Symbols))
	have := map[string]bool{}
	add := func(sym string) {
		if have[sym] {
			return
		}
		have[sym] = true
		p := simBase[sym]
		if p == 0 && s.BasePrice != nil {
			p = s.BasePrice(sym)
		}
		if p == 0 {
			p = 100
		}
		dec := 2
		if p < 1 {
			dec = 4
		}
		if p < 0.1 {
			dec = 6
		}
		open := p * (1 - 0.02 + rng.Float64()*0.04)
		states = append(states, &simState{symbol: sym, price: p, open: open, high: math.Max(p, open), low: math.Min(p, open), decimals: dec})
	}
	for _, sym := range s.Symbols {
		add(sym)
	}
	if len(states) == 0 {
		<-ctx.Done()
		return
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	lastSlow := time.Time{}
	for {
		select {
		case <-ctx.Done():
			sink.UpdateStatus(func(st *SourceStatus) { st.Connections["sim"] = false })
			return
		case now := <-t.C:
			for _, sym := range s.takeAdded() {
				add(sym)
			}
			st := states[rng.IntN(len(states))]
			if forced := s.takeForced(); len(forced) > 0 {
				for _, x := range states {
					if p, ok := forced[x.symbol]; ok {
						x.price = p
						st = x
					}
				}
				lastSlow = time.Time{} // 标记价马上跟上
			} else {
				st.price *= 1 + rng.NormFloat64()*0.00018
			}
			st.high, st.low = math.Max(st.high, st.price), math.Min(st.low, st.price)
			s.mu.Lock()
			if s.current == nil {
				s.current = map[string]float64{}
			}
			s.current[st.symbol] = st.price
			s.mu.Unlock()
			qty := math.Abs(rng.NormFloat64()) * 2000 / st.price
			st.vol += qty * st.price
			side := Buy
			if rng.IntN(2) == 0 {
				side = Sell
			}
			ms := now.UnixMilli()
			in := CanonicalPerp(st.symbol)
			sink.Publish(Event{Kind: KindTrade, Instrument: in, Symbol: st.symbol, Venue: "sim", Time: ms,
				Price: f(st.price, st.decimals), Qty: f(qty, 4), Side: side})
			spread := st.price * 0.00002
			bids, asks := make([][2]string, simDepth), make([][2]string, simDepth)
			for i := 0; i < simDepth; i++ {
				step := spread * float64(i)
				bids[i] = [2]string{f(st.price-spread/2-step, st.decimals), f(rng.Float64()*20, 3)}
				asks[i] = [2]string{f(st.price+spread/2+step, st.decimals), f(rng.Float64()*20, 3)}
			}
			sink.Publish(Event{Kind: KindDepth, Instrument: in, Symbol: st.symbol, Venue: "sim", Time: ms, Bids: bids, Asks: asks})
			if now.Sub(lastSlow) >= time.Second {
				lastSlow = now
				next := now.Truncate(8 * time.Hour).Add(8 * time.Hour).UnixMilli()
				for _, x := range states {
					xin := CanonicalPerp(x.symbol)
					sink.Publish(Event{Kind: KindMark, Instrument: xin, Symbol: x.symbol, Venue: "sim", Time: ms,
						Mark: f(x.price, x.decimals), FundingRate: "0.00010000", NextFundingTime: next})
					sink.Publish(Event{Kind: KindTicker, Instrument: xin, Symbol: x.symbol, Venue: "sim", Time: ms,
						Price: f(x.price, x.decimals), Change24hPct: f((x.price/x.open-1)*100, 3),
						QuoteVolume24h: f(x.vol+5e8, 2), High24h: f(x.high, x.decimals), Low24h: f(x.low, x.decimals)})
				}
			}
		}
	}
}

func f(v float64, dec int) string { return strconv.FormatFloat(v, 'f', dec, 64) }
