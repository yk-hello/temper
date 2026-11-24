// hlprobe：连 Hyperliquid 十秒，打印收到的事件统计（开发时检查接入是否正常）。
package main

import (
	"context"
	"fmt"
	"time"

	"qs/internal/book"
	"qs/internal/market"
)

type counter struct {
	n   map[market.EventKind]int
	fan *market.Fanout
}

func (c *counter) Publish(ev market.Event) { c.n[ev.Kind]++; c.fan.Publish(ev) }
func (c *counter) UpdateStatus(f func(*market.SourceStatus)) {
	s := market.SourceStatus{Connections: map[string]bool{}}
	f(&s)
}

func main() {
	books := book.NewStore()
	c := &counter{n: map[market.EventKind]int{}, fan: &market.Fanout{Primary: "none", Books: books}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	(&market.Hyperliquid{Coins: []string{"BTC", "ETH"}}).Run(ctx, c)
	fmt.Println("trade", c.n[market.KindTrade], "depth", c.n[market.KindDepth], "mark", c.n[market.KindMark], "ticker", c.n[market.KindTicker])
	b, _ := books.Book("hyperliquid", "BTC-PERP")
	t, _ := books.Ticker("hyperliquid", "BTC-PERP")
	if len(b.Bids) > 0 {
		fmt.Println("BTC 盘口档数", len(b.Bids), len(b.Asks), "买一", b.Bids[0].Px, b.Bids[0].Qty, "卖一", b.Asks[0].Px, "最新", t.Last, "标记", t.Mark, "资金费", t.FundingRate)
	}
}
