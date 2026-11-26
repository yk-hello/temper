// Package market 负责行情：从交易所接入、统一格式、合并后推给前端。
//
// 价格和数量一律保留交易所给的原始字符串，不转成浮点数（决策 D09）。
package market

import (
	"context"
	"strings"
)

type Side string

const (
	Buy  Side = "buy"
	Sell Side = "sell"
)

type EventKind int

const (
	KindTrade       EventKind = iota + 1 // 逐笔成交
	KindBook                             // 最优买卖价
	KindMark                             // 标记价格与资金费率
	KindTicker                           // 24 小时统计
	KindDepth                            // 盘口前 N 档
	KindLiquidation                      // 强平单（爆仓）
)

// Event 是各数据源统一后的行情更新。只填与 Kind 对应的字段。
type Event struct {
	Kind       EventKind
	Instrument string // 平台统一代码，如 BTC-PERP（决策 D12）
	Symbol     string // 交易所代码，如 BTCUSDT
	Venue      string // 如 binance-usdm
	Time       int64  // 交易所事件时间（毫秒）

	Price string // 成交价
	Qty   string // 成交量
	Side  Side   // 主动成交方向

	Bid, BidQty, Ask, AskQty string

	Mark, FundingRate string
	Index             string // 指数价（币安 markPriceUpdate 的 i）
	NextFundingTime   int64

	Change24hPct, QuoteVolume24h, High24h, Low24h string

	OpenInterest string      // 持仓量（Hyperliquid 推送里自带）
	Bids, Asks   [][2]string // 盘口：[价格, 数量]，买单从高到低，卖单从低到高
}

// InstrumentState 是某个品种当前的完整状态，推给前端。
type InstrumentState struct {
	Instrument      string `json:"instrument"`
	Symbol          string `json:"symbol"`
	Venue           string `json:"venue"`
	Last            string `json:"last"`
	LastSide        Side   `json:"last_side,omitempty"`
	Bid             string `json:"bid"`
	BidQty          string `json:"bid_qty"`
	Ask             string `json:"ask"`
	AskQty          string `json:"ask_qty"`
	Mark            string `json:"mark"`
	FundingRate     string `json:"funding_rate"`
	NextFundingTime int64  `json:"next_funding_time"`
	Change24hPct    string `json:"change_24h_pct"`
	QuoteVolume24h  string `json:"quote_volume_24h"`
	High24h         string `json:"high_24h"`
	Low24h          string `json:"low_24h"`
	Trades          int    `json:"trades"` // 距上一次推送之间的成交笔数
	UpdatedAt       int64  `json:"updated_at"`
	LastAt          int64  `json:"last_at,omitempty"` // 最后一笔成交的时间（UpdatedAt 盘口、标记价格也会改）：前端拿它判断实时价落在哪一根 K 线
}

// SourceStatus 描述数据源连接健康度，前端"系统状态"用。
type SourceStatus struct {
	Name          string          `json:"name"`
	Connections   map[string]bool `json:"connections"`
	Reconnects    int             `json:"reconnects"`
	LastError     string          `json:"last_error,omitempty"`
	LastMessageAt int64           `json:"last_message_at"`
	// Lag 每条线路的延迟（毫秒）：交易所发出消息到我们处理完的时间，减去这条线路最近一分钟的最小值
	// （扣掉两边时钟的固定偏差和网络基础延迟），只反映"积压"。超过 2 秒前端会提示行情延迟。
	Lag map[string]int64 `json:"lag_ms,omitempty"`
}

// Connected 表示所有连接都正常。
func (s SourceStatus) Connected() bool {
	if len(s.Connections) == 0 {
		return false
	}
	for _, ok := range s.Connections {
		if !ok {
			return false
		}
	}
	return true
}

// Sink 是数据源往外送数据的地方（由 Hub 实现）。
type Sink interface {
	Publish(Event)
	UpdateStatus(func(*SourceStatus))
}

// Source 是一个行情数据源。Run 一直运行到 ctx 取消，内部自己负责断线重连。
type Source interface {
	Name() string
	Run(ctx context.Context, sink Sink)
}

// CanonicalPerp 把交易所的 U 本位永续代码转成平台统一代码：BTCUSDT → BTC-PERP。
func CanonicalPerp(symbol string) string {
	s := strings.ToUpper(symbol)
	for _, q := range []string{"USDT", "USDC"} {
		if strings.HasSuffix(s, q) && len(s) > len(q) {
			return strings.TrimSuffix(s, q) + "-PERP"
		}
	}
	return s
}
