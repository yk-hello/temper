package market

// 现货行情（D44、D83）：币安现货公开流，模拟环境跟着合约价走。
// 品种名 BTC-SPOT，和合约共用行情通道、盘口仓库和 K 线。

import (
	"context"
	"encoding/json"
	"math"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"
)

// CanonicalSpot BTCUSDT → BTC-SPOT。
func CanonicalSpot(symbol string) string { return strings.TrimSuffix(symbol, "USDT") + "-SPOT" }

// ParseBinanceSpot 解析现货的合并流。深度快照没有事件名和品种，品种从流名字里取。
func ParseBinanceSpot(raw []byte) (Event, bool) {
	var w struct {
		Stream string          `json:"stream"`
		Data   json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &w) != nil {
		return Event{}, false
	}
	if i := strings.Index(w.Stream, "@depth"); i > 0 {
		sym := strings.ToUpper(w.Stream[:i])
		var d struct {
			Bids [][2]string `json:"bids"`
			Asks [][2]string `json:"asks"`
		}
		if json.Unmarshal(w.Data, &d) != nil {
			return Event{}, false
		}
		return Event{Kind: KindDepth, Instrument: CanonicalSpot(sym), Symbol: sym, Venue: binanceVenue, Time: time.Now().UnixMilli(), Bids: d.Bids, Asks: d.Asks}, true
	}
	ev, ok := ParseBinance(raw)
	if !ok {
		return Event{}, false
	}
	ev.Instrument = CanonicalSpot(ev.Symbol)
	return ev, true
}

// SpotSim 模拟现货：价格跟着模拟合约走，差一点基差，这样 /api/dev/sim-price 也能驱动现货。
type SpotSim struct {
	Symbols []string
	Price   func(symbol string) (float64, bool)
	Decs    func(symbol string) int // 价格保留几位小数
	Venue   string
	Seed    uint64
}

// Name 见 Source。
func (s *SpotSim) Name() string { return "sim-spot" }

// Run 见 Source。
func (s *SpotSim) Run(ctx context.Context, sink Sink) {
	rng := rand.New(rand.NewPCG(s.Seed, s.Seed^0x51ed270b))
	basis := map[string]float64{}
	open := map[string]float64{}
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	n := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		n++
		now := time.Now().UnixMilli()
		for _, sym := range s.Symbols {
			p0, ok := s.Price(sym)
			if !ok || p0 <= 0 {
				continue
			}
			basis[sym] = math.Max(-0.0005, math.Min(0.0005, basis[sym]+(rng.Float64()-0.5)*0.00004))
			p := p0 * (1 + basis[sym])
			if open[sym] == 0 {
				open[sym] = p
			}
			dec := 2
			if s.Decs != nil {
				dec = s.Decs(sym)
			}
			fmtP := func(v float64) string { return strconv.FormatFloat(v, 'f', dec, 64) }
			step := math.Max(p*0.00004, math.Pow10(-dec))
			bids, asks := make([][2]string, 20), make([][2]string, 20)
			for i := 0; i < 20; i++ {
				q := func() string { return strconv.FormatFloat((0.2+rng.Float64()*3)*500/p, 'f', 5, 64) }
				bids[i] = [2]string{fmtP(p - step*float64(i+1)), q()}
				asks[i] = [2]string{fmtP(p + step*float64(i+1)), q()}
			}
			inst := CanonicalSpot(sym)
			sink.Publish(Event{Kind: KindDepth, Instrument: inst, Symbol: sym, Venue: s.Venue, Time: now, Bids: bids, Asks: asks})
			buy := rng.Float64() < 0.5
			px := p + step
			side := Buy
			if !buy {
				px, side = p-step, Sell
			}
			sink.Publish(Event{Kind: KindTrade, Instrument: inst, Symbol: sym, Venue: s.Venue, Time: now, Price: fmtP(px), Qty: strconv.FormatFloat((0.1+rng.Float64())*300/p, 'f', 5, 64), Side: side})
			if n%10 == 0 {
				sink.Publish(Event{Kind: KindTicker, Instrument: inst, Symbol: sym, Venue: s.Venue, Time: now, Price: fmtP(p),
					Change24hPct: strconv.FormatFloat((p/open[sym]-1)*100, 'f', 2, 64), QuoteVolume24h: "120000000",
					High24h: fmtP(math.Max(p, open[sym])), Low24h: fmtP(math.Min(p, open[sym]))})
			}
		}
	}
}
