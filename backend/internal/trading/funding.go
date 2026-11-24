package trading

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/shopspring/decimal"
)

// FundingSettle 交易所一次真实的资金费结算。
type FundingSettle struct {
	Time int64           // 结算时刻（毫秒）
	Rate decimal.Decimal // 资金费率
	Mark decimal.Decimal // 结算时的标记价
}

// LastEventTime 重放完成后，最后一个事件的时间（毫秒）。用来判断后端停了多久。
func (e *Engine) LastEventTime() int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lastEvent
}

// CatchUpFunding 补上后端没运行时错过的资金费结算（家用电脑睡眠、关机很常见）。
//
// 只处理 after 之后、现在之前的结算：after 取重放时最后一个事件的时间，在那之后没有任何事件，
// 说明这段时间里持仓没有变过，用当前持仓结算就是对的。另外按（账户、品种、结算时刻）去重，
// 同一次结算绝不扣两遍。返回补扣的笔数。
func (e *Engine) CatchUpFunding(inst string, settles []FundingSettle, after int64) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.catchUp(inst, settles, after, 0, nil)
}

// FundingGapAtBoot 开机补资金费时取不到交易所记录（刚好被限流、网络或代理还没好）：记下来，settleGaps 每隔几秒重试。
// 原来只写一行日志，这段停机期间的资金费就永远没补（11-18）。
// 持仓按现在（开机、刚重放完）记下来：停机期间没有任何事件，持仓就是这个；过几个小时补上时你可能已经加仓、平仓了，不能按那时的算。
// 只补到 boot：开机以后的结算照常实时结。
func (e *Engine) FundingGapAtBoot(inst string, after, boot int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if snap := e.posSnap(inst, after); len(snap) > 0 {
		e.addPending(fundGap{inst: inst, from: after + 1, seen: e.now(), born: e.now(), to: boot, snap: snap})
	}
}

// catchUp 见 CatchUpFunding。to > 0：只补到这个时刻；snap 不为空：按这份持仓算，不看现在的。调用的人持有锁。
func (e *Engine) catchUp(inst string, settles []FundingSettle, after, to int64, snap map[int64]decimal.Decimal) int {
	now := e.now()
	n := 0
	for _, s := range settles {
		if s.Time <= after || s.Time > now || to > 0 && s.Time > to || s.Rate.IsZero() && s.Mark.IsZero() {
			continue
		}
		for _, id := range e.accountIDs() {
			a := e.accounts[id]
			var qty *decimal.Decimal
			if snap != nil {
				q, ok := snap[id]
				if !ok || a.Archived {
					continue
				}
				qty = &q
			} else if p := a.Pos[inst]; a.Archived || p == nil || p.Qty.IsZero() || p.Opened >= s.Time {
				continue
			}
			done := false
			for i := len(a.Fundings) - 1; i >= 0; i-- {
				// 同一次结算：币安给的结算时刻有时带几毫秒尾数（实时看到的 …000，历史记录里 …004），差一分钟以内都算同一次
				if d := a.Fundings[i].Time - s.Time; a.Fundings[i].Instrument == inst && d > -60_000 && d < 60_000 {
					done = true
					break
				}
			}
			if done {
				continue
			}
			e.emit(Event{Kind: "funding", Account: id, Funding: &FundingEv{Instrument: inst, Rate: s.Rate, Mark: s.Mark, SettleTime: s.Time, Qty: qty}})
			n++
		}
	}
	// 之后的正常结算按交易所的"下次结算时间"往后推来判断，这里把记忆清掉，避免把刚补过的那次再结算一遍
	delete(e.funding, inst)
	return n
}

// FetchFundingHistory 从币安取一段时间内的资金费结算记录（公开接口，不需要密钥）。
func FetchFundingHistory(ctx context.Context, baseURL, symbol string, start, end int64) ([]FundingSettle, error) {
	if baseURL == "" {
		baseURL = "https://fapi.binance.com"
	}
	q := url.Values{"symbol": {symbol}, "startTime": {strconv.FormatInt(start, 10)}, "endTime": {strconv.FormatInt(end, 10)}, "limit": {"1000"}}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", baseURL+"/fapi/v1/fundingRate?"+q.Encode(), nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("币安返回 %d", res.StatusCode)
	}
	var rows []struct {
		Time int64  `json:"fundingTime"`
		Rate string `json:"fundingRate"`
		Mark string `json:"markPrice"`
	}
	if err := json.NewDecoder(res.Body).Decode(&rows); err != nil {
		return nil, err
	}
	out := make([]FundingSettle, 0, len(rows))
	for _, r := range rows {
		rate, err1 := decimal.NewFromString(r.Rate)
		mark, err2 := decimal.NewFromString(r.Mark)
		if err1 != nil || err2 != nil || mark.Sign() <= 0 {
			continue // 币安早期记录没有标记价，没法算金额，跳过
		}
		out = append(out, FundingSettle{Time: r.Time, Rate: rate, Mark: mark})
	}
	return out, nil
}

// FetchBars1m 从币安取一段时间的 1 分钟 K 线（公开接口）。mark = 标记价 K 线（强平和默认的止损止盈按它触发）。
// 一次最多 1500 根，长的分页取。
func FetchBars1m(ctx context.Context, baseURL, symbol string, from, to int64, mark bool) ([]Bar1m, error) {
	if baseURL == "" {
		baseURL = "https://fapi.binance.com"
	}
	path := "/fapi/v1/klines"
	if mark {
		path = "/fapi/v1/markPriceKlines"
	}
	var out []Bar1m
	start := from - from%60_000
	for start < to {
		q := url.Values{"symbol": {symbol}, "interval": {"1m"}, "startTime": {strconv.FormatInt(start, 10)}, "endTime": {strconv.FormatInt(to, 10)}, "limit": {"1500"}}
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		req, _ := http.NewRequestWithContext(c, "GET", baseURL+path+"?"+q.Encode(), nil)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			cancel()
			return nil, err
		}
		var rows [][]any
		err = json.NewDecoder(res.Body).Decode(&rows)
		res.Body.Close()
		cancel()
		if res.StatusCode != 200 {
			return nil, fmt.Errorf("币安返回 %d", res.StatusCode)
		}
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			break
		}
		for _, r := range rows {
			if len(r) < 5 {
				continue
			}
			t, _ := r[0].(float64)
			num := func(i int) decimal.Decimal { s, _ := r[i].(string); d, _ := decimal.NewFromString(s); return d }
			b := Bar1m{T: int64(t), O: num(1), H: num(2), L: num(3)}
			if b.O.Sign() > 0 && b.H.Sign() > 0 && b.L.Sign() > 0 {
				out = append(out, b)
			}
		}
		lastT, _ := rows[len(rows)-1][0].(float64)
		if int64(lastT)+60_000 <= start {
			break
		}
		start = int64(lastT) + 60_000
	}
	return out, nil
}
