// Package fakesig 是假信号发生器（08 任务 4c）：按 qsig/1 格式签名后发给平台，
// 用来测试整条链路，也可以当写策略的参考实现。信号方向是随机的，没有任何预测能力。
package fakesig

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	mrand "math/rand/v2"
	"net/http"
	"strings"
	"time"

	"qs/internal/qsig"
)

type Config struct {
	BaseURL     string // 如 http://localhost:8000
	KeyID       string
	Secret      string
	StrategyID  string
	Instruments []string
	Every       time.Duration // 发信号的间隔
	// Price 取决策瞬间的原始市场价（ref_price）。真实策略应从 GET /v1/ticker 取。
	Price func(ctx context.Context, instrument string) (string, error)
	Log   *slog.Logger
}

func uuid4() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func now() string { return time.Now().UTC().Format(qsig.TimeLayout) }

// Send 签名并发送一条消息，返回 HTTP 状态和回复。
func Send(ctx context.Context, cfg Config, method, path string, msg any) (int, []byte, error) {
	var body []byte
	if msg != nil {
		body, _ = json.Marshal(msg)
	}
	req, _ := http.NewRequestWithContext(ctx, method, strings.TrimRight(cfg.BaseURL, "/")+path, bytes.NewReader(body))
	ts := time.Now().UnixMilli()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-QS-Key", cfg.KeyID)
	req.Header.Set("X-QS-Timestamp", fmt.Sprint(ts))
	req.Header.Set("X-QS-Signature", qsig.Sign(cfg.Secret, ts, method, path, body))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	out, _ := io.ReadAll(res.Body)
	return res.StatusCode, out, nil
}

// TickerPrice 从平台的 GET /v1/ticker 取最新价。
func TickerPrice(cfg Config) func(ctx context.Context, instrument string) (string, error) {
	return func(ctx context.Context, instrument string) (string, error) {
		code, body, err := Send(ctx, cfg, "GET", "/v1/ticker?instrument="+instrument, nil)
		if err != nil {
			return "", err
		}
		var t struct {
			Last string `json:"last"`
		}
		if code != 200 || json.Unmarshal(body, &t) != nil || t.Last == "" || t.Last == "0" {
			return "", fmt.Errorf("取价失败 %d %s", code, body)
		}
		return t.Last, nil
	}
}

// 示例理由：写明是随机的，方向和权重一致（第 29 条）。
var (
	longReasons = [][2]string{
		{"示例（随机信号）：短线动量转强，小仓位试多", "Sample (random signal): short-term momentum turning up, small long"},
		{"示例（随机信号）：大单吸收后突破，跟随做多", "Sample (random signal): breakout after large orders absorbed, going long"},
	}
	shortReasons = [][2]string{
		{"示例（随机信号）：资金费率偏高，小仓位试空", "Sample (random signal): funding rate is high, small short"},
		{"示例（随机信号）：上方挂单墙压制，做空", "Sample (random signal): resting sell wall overhead, going short"},
	}
	flatReasons = [][2]string{
		{"示例（随机信号）：波动收窄，先空仓观望", "Sample (random signal): volatility narrowing, staying flat"},
	}
)

// ReasonFor 按方向挑一句理由（中文、英文）。
func ReasonFor(weight float64) (zh, en string) {
	list := flatReasons
	if weight > 0 {
		list = longReasons
	} else if weight < 0 {
		list = shortReasons
	}
	r := list[mrand.IntN(len(list))]
	return r[0], r[1]
}

// SignalMessage 造一条随机信号（格式必须过 qsig 校验，测试里查）。
func SignalMessage(strategy, inst, px string, w float64, issued time.Time) map[string]any {
	zh, en := ReasonFor(w)
	return map[string]any{
		"protocol": "qsig/1", "type": "signal", "id": uuid4(), "strategy_id": strategy, "strategy_version": "fake-1",
		"issued_at": issued.Format(qsig.TimeLayout), "data_as_of": issued.Add(-2 * time.Second).Format(qsig.TimeLayout),
		"valid_until": issued.Add(60 * time.Second).Format(qsig.TimeLayout), "mode": "partial",
		"targets": []any{map[string]any{
			"instrument": inst, "weight": fmt.Sprintf("%.1f", w), "confidence": 0.3 + mrand.Float64()*0.6,
			"horizon_min": 30 + mrand.IntN(240), "ref_price": px, "ref_price_source": "binance_usdm_last",
			"execution": map[string]any{"urgency": []string{"passive", "normal", "immediate"}[mrand.IntN(3)], "max_slippage_bps": 20},
			"exits":     map[string]any{"stop_loss_pct": "0.02", "take_profit_pct": "0.04"},
			"rationale": map[string]any{"summary": map[string]any{"zh": zh, "en": en},
				"factors":  []any{map[string]any{"key": "random", "label": "随机", "score": mrand.Float64()*2 - 1}},
				"evidence": []any{map[string]any{"kind": "other", "title": "示例策略没有真实证据", "source": "platform", "excerpt": "信号方向是随机的，只用来演示界面和成交流程。"}}},
		}},
		"meta": map[string]any{"model": "none", "cost_usd": "0"},
	}
}

// Run 每 30 秒发一次心跳，每 Every 发一条随机信号，直到 ctx 结束。
func Run(ctx context.Context, cfg Config) {
	if cfg.Price == nil {
		cfg.Price = TickerPrice(cfg)
	}
	// 等平台就绪（重启时要先从数据库恢复账本，期间 /v1/ 回复 503）
	for {
		if code, _, err := Send(ctx, cfg, "GET", "/v1/instruments", nil); err == nil && code == 200 {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
	hb := time.NewTicker(30 * time.Second)
	sig := time.NewTicker(cfg.Every)
	defer hb.Stop()
	defer sig.Stop()
	heartbeat := func() {
		msg := map[string]any{"protocol": "qsig/1", "type": "heartbeat", "id": uuid4(), "strategy_id": cfg.StrategyID,
			"strategy_version": "fake-1", "issued_at": now(), "status": "ok", "note": "假信号发生器"}
		if code, body, err := Send(ctx, cfg, "POST", "/v1/messages", msg); err != nil || code != 200 {
			cfg.Log.Warn("fake heartbeat", "code", code, "body", string(body), "err", err)
		}
	}
	send := func() {
		inst := cfg.Instruments[mrand.IntN(len(cfg.Instruments))]
		px, err := cfg.Price(ctx, inst)
		if err != nil {
			cfg.Log.Warn("fake price", "err", err)
			return
		}
		w := float64(mrand.IntN(9)-4) / 10 // -0.4 … 0.4
		issued := time.Now().UTC()
		msg := SignalMessage(cfg.StrategyID, inst, px, w, issued)
		code, body, err := Send(ctx, cfg, "POST", "/v1/messages", msg)
		cfg.Log.Info("fake signal", "instrument", inst, "weight", w, "code", code, "reply", string(body), "err", err)
	}
	heartbeat()
	first := time.After(3 * time.Second) // 启动后先发一条，不用等一整个间隔
	for {
		select {
		case <-ctx.Done():
			return
		case <-hb.C:
			heartbeat()
		case <-first:
			send()
		case <-sig.C:
			send()
		}
	}
}
