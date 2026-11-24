package market

import (
	"github.com/shopspring/decimal"

	"qs/internal/book"
)

// Fanout 把多个数据源的事件分发出去：
//   - 所有交易所的盘口、成交、标记价 → 盘口仓库（模拟成交、频谱、资金费矩阵）
//   - 主交易所（Primary）的事件 → 前端推送 Hub（行情页）
//   - 强平单 → OnLiquidation（爆仓流）
type Fanout struct {
	Primary       string
	Hub           *Hub
	Books         *book.Store
	OnLiquidation func(Event)
	OnEvent       func(Event) // 可选：所有事件（记录器用）
	Meter         *Meter      // 可选：每条线路的速率和延迟（系统控制台）
}

func (f *Fanout) Publish(ev Event) {
	if f.Meter != nil {
		f.Meter.Add(ev)
	}
	if f.OnEvent != nil {
		f.OnEvent(ev)
	}
	if f.Books != nil {
		f.toBooks(ev)
	}
	if ev.Kind == KindLiquidation {
		if f.OnLiquidation != nil {
			f.OnLiquidation(ev)
		}
		return
	}
	if ev.Venue == f.Primary && f.Hub != nil {
		if ev.Kind == KindDepth {
			// 不再订 bookTicker：前端的最优买卖价从深度的第一档来
			if len(ev.Bids) > 0 && len(ev.Asks) > 0 {
				f.Hub.Publish(Event{Kind: KindBook, Instrument: ev.Instrument, Symbol: ev.Symbol, Venue: ev.Venue, Time: ev.Time,
					Bid: ev.Bids[0][0], BidQty: ev.Bids[0][1], Ask: ev.Asks[0][0], AskQty: ev.Asks[0][1]})
			}
			return
		}
		f.Hub.Publish(ev)
	}
}

func (f *Fanout) UpdateStatus(fn func(*SourceStatus)) {
	if f.Hub != nil {
		f.Hub.UpdateStatus(fn)
	}
}

func (f *Fanout) toBooks(ev Event) {
	d := func(s string) decimal.Decimal { v, _ := decimal.NewFromString(s); return v }
	switch ev.Kind {
	case KindDepth:
		b := book.Book{Venue: ev.Venue, Instrument: ev.Instrument, Time: ev.Time}
		for _, l := range ev.Bids {
			b.Bids = append(b.Bids, book.Level{Px: d(l[0]), Qty: d(l[1])})
		}
		for _, l := range ev.Asks {
			b.Asks = append(b.Asks, book.Level{Px: d(l[0]), Qty: d(l[1])})
		}
		f.Books.SetBook(b)
	case KindBook:
		f.Books.SetTop(ev.Venue, ev.Instrument, d(ev.Bid), d(ev.BidQty), d(ev.Ask), d(ev.AskQty), ev.Time)
	case KindTrade:
		f.Books.Trade(ev.Venue, ev.Instrument, d(ev.Price), d(ev.Qty), ev.Side == Buy, ev.Time)
	case KindMark:
		f.Books.UpdateTicker(ev.Venue, ev.Instrument, func(t *book.Ticker) {
			if ev.Index != "" {
				t.Index = d(ev.Index)
			} else if ev.Mark != "" {
				t.Index = d(ev.Mark) // 模拟行情没有指数价：和标记价一样
			}
			if ev.Mark != "" {
				t.Mark = d(ev.Mark)
			}
			if ev.FundingRate != "" {
				t.FundingRate = d(ev.FundingRate)
			}
			if ev.NextFundingTime != 0 {
				t.NextFunding = ev.NextFundingTime
			}
		})
	}
}
