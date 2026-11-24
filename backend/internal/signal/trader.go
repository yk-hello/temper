package signal

import (
	"github.com/shopspring/decimal"

	"qs/internal/spot"
	"qs/internal/trading"
)

// Engines 把信号接到合约和现货引擎。来源写 signal：纪律锁和引擎优先级都认得。
type Engines struct {
	Perps *trading.Engine
	Spots *spot.Engine
	// Live 交易所账户（D93）：信号绑在实盘账户上时，单子发到交易所。没接实盘时是 nil
	Live trading.Desk
}

func (x *Engines) live(acct int64) bool { return acct >= trading.LiveBase && x.Live != nil }

// Perp 见 Trader。
func (x *Engines) Perp(acct int64, inst, side string, notional decimal.Decimal, closeAll bool) (string, error) {
	if x.live(acct) {
		var o *trading.Order
		var err error
		if closeAll { // 只减仓的市价单平掉整个仓位，来源记成信号（和模拟盘的 CloseAs 一样）
			v, verr := x.Live.View(acct)
			if verr != nil {
				return "", verr
			}
			var req *trading.OrderReq
			for _, p := range v.Positions {
				if p.Instrument == inst {
					side := "sell"
					if p.Side == "short" {
						side = "buy"
					}
					req = &trading.OrderReq{Instrument: inst, Side: side, Type: trading.Market, Qty: p.Qty, ReduceOnly: true}
				}
			}
			if req == nil {
				return "", trading.Rej("NO_POSITION")
			}
			req.SetSource("signal")
			o, err = x.Live.Place(acct, *req)
		} else {
			req := trading.OrderReq{Instrument: inst, Side: side, Type: trading.Market, Notional: notional}
			req.SetSource("signal")
			o, err = x.Live.Place(acct, req)
		}
		if err != nil {
			return "", err
		}
		return o.ID, nil
	}
	if closeAll {
		o, err := x.Perps.CloseAs(acct, inst, "signal", "")
		if err != nil {
			return "", err
		}
		return o.ID, nil
	}
	o, err := x.Perps.PlaceAs(acct, trading.OrderReq{Instrument: inst, Side: side, Type: trading.Market, Notional: notional}, "signal", "")
	if err != nil {
		return "", err
	}
	return o.ID, nil
}

// Spot 见 Trader。
func (x *Engines) Spot(acct int64, inst, side string, pct decimal.Decimal) (string, error) {
	if x.live(acct) {
		return "", rej("SIG_LIVE_SPOT") // 交易所账户现在只接 U 本位合约
	}
	s := x.Spots.Specs[inst]
	if s == nil {
		return "", rej("SIG_BAD_SYMBOL")
	}
	r := spot.OrderReq{Instrument: inst, Side: side, Type: spot.Market, Source: "signal"}
	for _, b := range x.Spots.ViewOf(acct).Balances {
		if side == "buy" && b.Coin == spot.Quote {
			r.QuoteQty = b.Free.Mul(pct).Div(hund)
		}
		if side == "sell" && b.Coin == s.Base {
			r.Qty = b.Free.Mul(pct).Div(hund)
		}
	}
	o, err := x.Spots.Place(acct, r)
	if err != nil {
		return "", err
	}
	return o.ID, nil
}

// Available 见 Trader。
func (x *Engines) Available(acct int64) decimal.Decimal {
	if x.live(acct) {
		v, err := x.Live.View(acct)
		if err != nil {
			return decimal.Zero
		}
		return decimal.Max(decimal.Zero, v.Available)
	}
	b, err := x.Perps.Brief(acct)
	if err != nil {
		return decimal.Zero
	}
	return b.Available
}
