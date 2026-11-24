package binance

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// Event 用户数据流里的一条消息：ORDER_TRADE_UPDATE（委托和成交）、ACCOUNT_UPDATE（余额和仓位）、
// ALGO_UPDATE（条件单）、listenKeyExpired、MARGIN_CALL。Raw 是原文，由使用方按类型解析。
type Event struct {
	Type string          `json:"e"`
	Time int64           `json:"E"`
	Raw  json.RawMessage `json:"-"`
}

// OrderUpdate ORDER_TRADE_UPDATE 的 o。
type OrderUpdate struct {
	Symbol        string `json:"s"`
	ClientOrderID string `json:"c"`
	Side          string `json:"S"`
	Type          string `json:"o"`
	TimeInForce   string `json:"f"`
	OrigQty       Num    `json:"q"`
	Price         Num    `json:"p"`
	AvgPrice      Num    `json:"ap"`
	StopPrice     Num    `json:"sp"`
	ExecType      string `json:"x"` // NEW | CANCELED | CALCULATED（强平）| EXPIRED | TRADE | AMENDMENT
	Status        string `json:"X"`
	OrderID       int64  `json:"i"`
	LastQty       Num    `json:"l"`
	CumQty        Num    `json:"z"`
	LastPrice     Num    `json:"L"`
	FeeAsset      string `json:"N"`
	Fee           Num    `json:"n"`
	TradeTime     int64  `json:"T"`
	TradeID       int64  `json:"t"`
	Maker         bool   `json:"m"`
	ReduceOnly    bool   `json:"R"`
	Realized      Num    `json:"rp"`
}

// ParseOrderUpdate 解析 ORDER_TRADE_UPDATE。
func ParseOrderUpdate(raw json.RawMessage) (*OrderUpdate, error) {
	var m struct {
		O OrderUpdate `json:"o"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return &m.O, nil
}

// Stream 连上用户数据流，一直读，有消息就调 on。断了、listenKey 过期了就重连（退避到最多 1 分钟）。ctx 结束时返回。
// status 报告连接状态（连上、断开、原因），给界面显示用。
func (c *Client) Stream(ctx context.Context, on func(Event), status func(ok bool, why string)) {
	wait := time.Second
	for ctx.Err() == nil {
		err := c.streamOnce(ctx, on, status)
		if ctx.Err() != nil {
			return
		}
		why := "closed"
		if err != nil {
			why = err.Error()
		}
		if status != nil {
			status(false, why)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if wait < time.Minute {
			wait *= 2
		}
	}
}

func (c *Client) streamOnce(ctx context.Context, on func(Event), status func(bool, string)) error {
	key, err := c.ListenKey(ctx)
	if err != nil {
		return err
	}
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// 币安 2026-03 把合约推送拆成 /public、/market、/private 三条线，旧的 /ws/<listenKey> 在 2026-04-23 下线：
	// 账户推送要走 /private/ws/<listenKey>，走旧地址连得上但收不到账户事件。
	// 文档：https://developers.binance.com/en/docs/products/derivatives-trading-usds-futures/user-data-streams.md
	conn, _, err := websocket.Dial(cctx, strings.TrimRight(c.WS, "/")+"/private/ws/"+key, nil)
	if err != nil {
		return err
	}
	defer conn.CloseNow()
	conn.SetReadLimit(1 << 20)
	if status != nil {
		status(true, "")
	}
	go func() { // 每 30 分钟续一次 listenKey
		t := time.NewTicker(30 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-cctx.Done():
				return
			case <-t.C:
				if c.KeepAlive(cctx) != nil {
					cancel()
					return
				}
			}
		}
	}()
	for {
		_, data, err := conn.Read(cctx)
		if err != nil {
			return err
		}
		var ev Event
		if json.Unmarshal(data, &ev) != nil || ev.Type == "" {
			continue
		}
		ev.Raw = data
		if ev.Type == "listenKeyExpired" {
			return nil // 换一个 listenKey 重连
		}
		on(ev)
	}
}
