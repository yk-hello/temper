package main

// 高光（D88）接到真正的引擎、交易日志、经济日历、链上扫描、爆仓流。

import (
	"context"
	"errors"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"qs/internal/calendar"
	"qs/internal/candles"
	"qs/internal/flow"
	"qs/internal/heat"
	"qs/internal/highlight"
	"qs/internal/journal"
	"qs/internal/live"
	"qs/internal/notify"
	"qs/internal/onchain"
	"qs/internal/trading"
)

// highlightNotice 站内通知的参数：文字由界面按语言拼。
func highlightNotice(h *highlight.Highlight) map[string]string {
	return map[string]string{
		"id": strconv.FormatInt(h.ID, 10), "inst": h.Snap.Instrument, "side": h.Snap.Side,
		"roe": strconv.FormatFloat(math.Round(h.Snap.ROE*1000)/10, 'f', -1, 64), "lev": strconv.Itoa(h.Snap.Leverage),
	}
}

func highlightSources(pool *pgxpool.Pool, trader *trading.Engine, desks *live.Desks, j *journal.API) highlight.Sources {
	desk := func(acct int64) trading.Desk {
		if live.IsLive(acct) {
			return desks
		}
		return trader
	}
	return highlight.Sources{
		Accounts: func(ctx context.Context) []highlight.Acct {
			out := []highlight.Acct{}
			rows, err := pool.Query(ctx, `select id, user_id, initial::float8 from paper_accounts where archived_at is null`)
			if err != nil {
				return nil
			}
			for rows.Next() {
				var a highlight.Acct
				if rows.Scan(&a.ID, &a.User, &a.Initial) == nil {
					out = append(out, a)
				}
			}
			rows.Close()
			// 实盘账户（D89）：起始资金按交易所的余额倒推（钱包 − 累计已实现 − 资金费 + 手续费）
			if rows, err := pool.Query(ctx, `select id, usr from live_accounts where archived_at is null`); err == nil {
				for rows.Next() {
					var a highlight.Acct
					if rows.Scan(&a.ID, &a.User) == nil {
						if v, err := desks.View(a.ID); err == nil {
							a.Initial = v.Initial.InexactFloat64()
						}
						out = append(out, a)
					}
				}
				rows.Close()
			}
			return out
		},
		FillSeq: func(acct int64) int64 {
			if live.IsLive(acct) {
				return desks.FillSeq(acct)
			}
			return trader.FillSeq(acct)
		},
		Trades:    j.TradesOf,
		Excursion: j.ExcursionOf,
		Leverage: func(acct int64, inst string) int {
			v, err := desk(acct).View(acct)
			if err != nil {
				return 1
			}
			if s := v.Settings[inst]; s != nil {
				return s.Leverage
			}
			return 1
		},
	}
}

// 演示用的那段行情算一次要翻一周的 K 线，记 10 分钟
var demoCache struct {
	sync.Mutex
	at int64
	d  *highlight.Demo
}

func highlightScene(cal *calendar.Service, chain *onchain.Scanner, flows *flow.Service, heats *heat.Recorder, bars *candles.Store) highlight.SceneSrc {
	return highlight.SceneSrc{
		Econ: func(from, to int64) []highlight.Ev {
			list, _ := cal.List()
			out := []highlight.Ev{}
			for _, e := range list {
				if e.T >= from && e.T <= to {
					out = append(out, highlight.Ev{T: e.T, Kind: "econ", Key: e.Key})
				}
			}
			return out
		},
		Whales: func(coin string, from, to int64) []highlight.Ev {
			out := []highlight.Ev{}
			if chain == nil {
				return out
			}
			for _, e := range chain.Events(coin, notify.WhaleMin, 1000) {
				if e.Time >= from && e.Time <= to {
					out = append(out, highlight.Ev{T: e.Time, Kind: "whale", Key: e.Kind, USD: e.Value})
				}
			}
			return out
		},
		Liqs: func(inst string, from, to int64) []highlight.Ev {
			items, _, _, _ := flows.Liquidations(inst, time.Now().UnixMilli(), 300)
			out := []highlight.Ev{}
			for _, l := range items {
				if l.T >= from && l.T <= to && l.USD >= 250_000 {
					out = append(out, highlight.Ev{T: l.T, Kind: "liq", Side: l.Side, USD: l.USD})
				}
			}
			return out
		},
		Candles: func(ctx context.Context, inst string, from, to int64) ([]highlight.Candle, int64) {
			hold := max(1_000, to-from)
			// 短单：交易所最细只有 1 秒线，用录到的逐笔成交合成更细的（6 个重点币录了逐笔）
			if hold <= 30*60_000 {
				if ts := heats.Trades(ctx, inst, from, to); len(ts) >= 20 {
					ticks := make([]highlight.Tick, len(ts))
					for i, x := range ts {
						ticks[i] = highlight.Tick{T: x.T, P: x.P}
					}
					iv := highlight.PickIv(hold, []int64{250, 500, 1_000, 2_000, 5_000, 10_000, 15_000, 30_000, 60_000})
					if cs := highlight.FromTicks(ticks, from, to, iv); len(cs) >= 3 {
						return cs, iv
					}
				}
			}
			iv := highlight.PickIv(hold, []int64{60_000, 180_000, 300_000, 900_000, 1_800_000, 3_600_000, 7_200_000, 14_400_000, 86_400_000})
			list := bars.Query(ctx, inst, iv, 0, to+iv, int(min(1000, hold/iv+3)))
			out := []highlight.Candle{}
			for _, c := range list {
				if c.T+iv > from && c.T < to {
					out = append(out, highlight.Candle{T: c.T, O: c.O, H: c.H, L: c.L, C: c.C})
				}
			}
			return out, iv
		},
		Demo: func(ctx context.Context) (*highlight.Demo, error) {
			now := time.Now().UnixMilli()
			demoCache.Lock()
			defer demoCache.Unlock()
			if demoCache.d != nil && now-demoCache.at < 10*60*1000 {
				return demoCache.d, nil
			}
			const inst = "BTC-PERP"
			rec := heats.RecordedMinutes(ctx, inst, now-7*24*3600*1000, now)
			if len(rec) < 30 {
				return nil, errors.New("NO_DEMO")
			}
			// 从现在往回一千根一千根地取 1 分钟 K 线，取到最早录到的那一分钟
			var all []highlight.Bar
			before := now + 60_000
			for i := 0; i < 12 && before > rec[0]; i++ {
				cs := bars.Query(ctx, inst, 60_000, 0, before, 1000)
				if len(cs) == 0 {
					break
				}
				for _, c := range cs {
					all = append(all, highlight.Bar{T: c.T, O: c.O, C: c.C})
				}
				if cs[0].T >= before {
					break
				}
				before = cs[0].T
			}
			d, ok := highlight.PickDemo(inst, rec, all, 45)
			if !ok {
				d, ok = highlight.PickDemo(inst, rec, all, 20)
			}
			if !ok {
				return nil, errors.New("NO_DEMO")
			}
			demoCache.d, demoCache.at = d, now
			return d, nil
		},
	}
}
