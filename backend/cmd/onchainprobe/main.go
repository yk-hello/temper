// onchainprobe：扫描少量 Hyperliquid 大户，打印 BTC 的爆仓分布和最大持仓（开发时检查用）。
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"qs/internal/onchain"
)

func main() {
	s := &onchain.Scanner{TopByValue: 60, TopByPnL: 20, PerSecond: 8, Log: slog.New(slog.NewTextHandler(os.Stdout, nil))}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	s.Run(ctx)
	m := s.LiqMap("BTC", 77400, 0.3, 30)
	fmt.Printf("地址 %d，BTC 持仓 %d 个，多 %.0f 万美元，空 %.0f 万美元，无强平价 %.0f 万美元\n", m.Addresses, m.Positions, m.LongValue/1e4, m.ShortVal/1e4, m.NoLiq/1e4)
	for _, b := range m.Bins {
		if b.LongLiq+b.ShortLiq > 0 {
			fmt.Printf("  %8.0f–%8.0f  多单强平 %10.0f  空单强平 %10.0f\n", b.Lo, b.Hi, b.LongLiq, b.ShortLiq)
		}
	}
	for _, w := range s.Whales("BTC", 5) {
		fmt.Printf("  %s… %+.2f BTC 均价 %.0f 强平 %.0f 浮盈 %.0f 聪明钱=%v\n", w.Address[:10], w.Size, w.Entry, w.LiqPx, w.UPnL, w.Smart)
	}
}
