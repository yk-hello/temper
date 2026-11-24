// backtest 命令行回测：用模拟盘同一个撮合引擎，按币安的历史 1 分钟 K 线和资金费跑一份信号表（D95）。
//
//	go run ./cmd/backtest -signals signals.csv -from 2026-10-01 -to 2026-10-08
//
// 信号表每行"时间,信号"，写法见 internal/backtest/input.go（和 TradingView 警报一样，或者一张完整的委托）。
// 结果写到 -out（JSON），终端打印汇总。只读币安的公开接口，不需要密钥，不下真单。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/shopspring/decimal"

	"qs/internal/backtest"
	"qs/internal/trading"
)

func main() {
	file := flag.String("signals", "", "信号表文件（每行：时间,信号）")
	from := flag.String("from", "", "开始日期（UTC），例如 2026-10-01")
	to := flag.String("to", "", "结束日期（UTC，不含），例如 2026-10-08")
	initial := flag.String("initial", "10000", "初始资金（USDT）")
	lev := flag.Int("leverage", 10, "杠杆")
	mode := flag.String("mode", "cross", "cross 全仓 | isolated 逐仓")
	lat := flag.Int64("latency", 0, "信号到交易所的延迟（毫秒）")
	slip := flag.Float64("slippage", 0, "吃单额外滑点（万分之几）")
	out := flag.String("out", "backtest-result.json", "结果写到哪里")
	flag.Parse()
	die := func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...); os.Exit(1) }
	if *file == "" || *from == "" || *to == "" {
		flag.Usage()
		os.Exit(2)
	}
	text, err := os.ReadFile(*file)
	if err != nil {
		die("读不了信号表：%v", err)
	}
	sigs, err := backtest.ParseLines(string(text))
	if err != nil {
		die("信号表有问题：%v", err)
	}
	day := func(s string) int64 {
		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			die("日期格式是 2026-10-01：%s", s)
		}
		return t.UnixMilli()
	}
	ini, err := decimal.NewFromString(*initial)
	if err != nil {
		die("初始资金：%v", err)
	}
	cfg := backtest.Config{From: day(*from), To: day(*to), Initial: ini, Leverage: *lev, Mode: *mode, LatencyMS: *lat, SlippageBps: *slip, Signals: sigs}
	ctx := context.Background()
	specs := trading.NewSpecs(trading.DefaultSpecs())
	if err := specs.FetchBinance(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "读币安品种规格失败，用内置值：", err)
	}
	if err := backtest.Prepare(&cfg, func(i string) bool { _, ok := specs.Get(i); return ok }); err != nil {
		die("配置：%v", err)
	}
	fmt.Printf("下载 %v 的历史数据…\n", cfg.Instruments)
	res, err := backtest.Run(ctx, cfg, &backtest.Binance{}, specs)
	if err != nil {
		die("回测失败：%v", err)
	}
	b, _ := json.MarshalIndent(res, "", "  ")
	if err := os.WriteFile(*out, b, 0o644); err != nil {
		die("写结果：%v", err)
	}
	ok := 0
	for _, o := range res.Outcomes {
		if o.OK {
			ok++
		}
	}
	fmt.Printf("K 线 %d 根，信号 %d 条（执行 %d 条），用时 %d 毫秒\n", res.Bars, len(res.Outcomes), ok, res.ElapsedMS)
	fmt.Printf("初始 %.2f → 最后 %.2f USDT（收益 %.2f%%，最大回撤 %.2f%%）\n", res.Initial, res.Final, res.Stats.Return*100, res.Stats.MaxDrawdown*100)
	fmt.Printf("平仓 %d 笔，赚 %d 笔；手续费 %.2f，资金费 %.2f，强平 %d 次\n", res.Stats.Trades, res.Stats.Wins, res.Fees, res.Funding, res.Liquidations)
	fmt.Printf("结果写到 %s\n", *out)
}
