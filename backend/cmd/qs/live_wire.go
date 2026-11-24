package main

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"

	"qs/internal/binance"
	"qs/internal/book"
	"qs/internal/live"
	"qs/internal/trading"
)

// liveURLs 实盘能接的环境。正式和测试网的地址可以用环境变量换（币安换过测试网地址）。
// QS_LIVE_FAKE=key:secret 时再起一个假交易所（测试环境用：价格跟着行情走，不碰真钱）。
func liveURLs(ctx context.Context, books *book.Store, venue string, instruments []string, log *slog.Logger) map[string]live.Endpoints {
	env := func(k, def string) string {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
		return def
	}
	urls := map[string]live.Endpoints{}
	if os.Getenv("QS_LIVE_MAIN") != "0" {
		urls["main"] = live.Endpoints{
			FAPI: env("QS_LIVE_MAIN_FAPI", "https://fapi.binance.com"),
			SAPI: env("QS_LIVE_MAIN_SAPI", "https://api.binance.com"),
			WS:   env("QS_LIVE_MAIN_WS", "wss://fstream.binance.com"),
		}
	}
	// 币安"模拟交易"（原来的合约测试网已经并进来了）：官方文档 General Info 页，2026-11-10 查。
	// 环境名沿用 testnet（库里已经存的密钥不用改），界面上叫"币安模拟交易"。
	if os.Getenv("QS_LIVE_TESTNET") != "0" {
		urls["testnet"] = live.Endpoints{
			FAPI: env("QS_LIVE_TESTNET_FAPI", "https://demo-fapi.binance.com"),
			WS:   env("QS_LIVE_TESTNET_WS", "wss://demo-fstream.binance.com"),
		}
	}
	if kv := os.Getenv("QS_LIVE_FAKE"); kv != "" {
		key, secret, ok := strings.Cut(kv, ":")
		if ok {
			f := binance.NewFake(key, secret, 10000)
			urls["fake"] = live.Endpoints{FAPI: f.URL(), SAPI: f.URL(), WS: f.WSURL()}
			go func() {
				t := time.NewTicker(500 * time.Millisecond)
				defer t.Stop()
				for {
					select {
					case <-ctx.Done():
						f.Close()
						return
					case <-t.C:
						for _, inst := range instruments {
							if tk, ok := books.Ticker(venue, inst); ok && tk.Last.Sign() > 0 {
								f.SetPrice(strings.TrimSuffix(inst, "-PERP")+"USDT", tk.Last.InexactFloat64())
							}
						}
					}
				}
			}()
			if log != nil {
				log.Warn("实盘：开了假交易所（只给测试环境用）", "url", f.URL())
			}
		}
	}
	return urls
}

// discMux 纪律锁按账户编号找交易台：实盘账户找交易所，其他找模拟引擎。
type discMux struct {
	paper *trading.Engine
	live  *live.Desks
}

func (m discMux) Brief(acct int64) (*trading.Brief, error) {
	if live.IsLive(acct) {
		return m.live.Brief(acct)
	}
	return m.paper.Brief(acct)
}

func (m discMux) CancelOpening(acct int64, reason string) (int, error) {
	if live.IsLive(acct) {
		return m.live.CancelOpening(acct, reason)
	}
	return m.paper.CancelOpening(acct, reason)
}

func (m discMux) ClosedTrades(acct, after int64) ([]trading.ClosedTrade, int64) {
	if live.IsLive(acct) {
		return m.live.ClosedTrades(acct, after)
	}
	return m.paper.ClosedTrades(acct, after)
}

func (m discMux) Note(acct int64, kind, inst, reason string) error {
	if live.IsLive(acct) {
		return m.live.Note(acct, kind, inst, reason)
	}
	return m.paper.Note(acct, kind, inst, reason)
}
