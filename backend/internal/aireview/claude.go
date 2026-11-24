package aireview

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
)

// ---------- Claude（Anthropic 自己的接口） ----------

// Claude 用 Anthropic 官方 SDK 调 Messages API。它的格式和 OpenAI 兼容接口不一样，所以单独接（2026-11-15）。
// Base 留空用官方地址 https://api.anthropic.com；用中转站就填中转站给的地址（到 /v1/messages 之前那一段）。
type Claude struct {
	Key    string
	Model  string // 留空用 claude-opus-5-5
	Base   string
	Effort string // 思考程度（D112）：空 = 不传（模型自己的默认档）；none / low / medium / high / xhigh
	HTTP   *http.Client
}

// 这几个模型的思考关不掉（发 disabled 直接 400）：选"关"时按最低一档（low）发（11-23 按官方文档）。
func alwaysThinks(model string) bool {
	for _, p := range []string{"claude-opus-5-5", "claude-fable-", "claude-mythos-", "claude-sonnet-5-5"} {
		if strings.HasPrefix(model, p) {
			return true
		}
	}
	return false
}

// ClaudeModel 默认模型。
const ClaudeModel = "claude-opus-5-5"

// 这几个模型支持"服务端备用模型"：万一被安全策略拒了，同一次请求里换个模型接着答。
var claudeFallback = map[string]bool{"claude-fable-5-2": true, "claude-fable-5-1": true, "claude-opus-5-5": true, "claude-opus-5": true, "claude-sonnet-5-5": true}

// ClaudeBase 整理地址：去掉结尾的 / 和 /v1（SDK 自己会加 /v1/messages）。
func ClaudeBase(b string) string {
	b = strings.TrimRight(strings.TrimSpace(b), "/")
	return strings.TrimSuffix(b, "/v1")
}

// Complete 问一次。点评只有三行，不用流式；思考程度按设置（不设就是模型自己的默认档），max_tokens 给足免得被思考用光。
// 错误里不带 key：key 在请求头里；对方返回的内容也不进错误。
func (c *Claude) Complete(ctx context.Context, system, user string) (string, int, string, error) {
	model := strings.TrimSpace(c.Model)
	if model == "" {
		model = ClaudeModel
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 58 * time.Second}
	}
	base := ClaudeBase(c.Base)
	opts := []option.RequestOption{option.WithAPIKey(c.Key), option.WithHTTPClient(hc), option.WithMaxRetries(1)}
	if base != "" {
		opts = append(opts, option.WithBaseURL(base+"/"))
	}
	client := anthropic.NewClient(opts...)
	p := anthropic.BetaMessageNewParams{
		Model:     model,
		MaxTokens: 16000,
		System:    []anthropic.BetaTextBlockParam{{Text: system}},
		Messages:  []anthropic.BetaMessageParam{anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(user))},
	}
	switch c.Effort {
	case "":
	case "none":
		if alwaysThinks(model) {
			p.OutputConfig = anthropic.BetaOutputConfigParam{Effort: anthropic.BetaOutputConfigEffortLow}
		} else {
			p.Thinking = anthropic.BetaThinkingConfigParamUnion{OfDisabled: &anthropic.BetaThinkingConfigDisabledParam{}}
		}
	default:
		p.OutputConfig = anthropic.BetaOutputConfigParam{Effort: anthropic.BetaOutputConfigEffort(c.Effort)}
	}
	// 备用模型只在官方地址上开：中转站不一定认这个参数
	if (base == "" || strings.Contains(base, "api.anthropic.com")) && claudeFallback[model] {
		p.Betas = []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01}
		p.Fallbacks = anthropic.BetaFallbacksParamOfDefault()
	}
	r, err := client.Beta.Messages.New(ctx, p)
	if err != nil {
		var ae *anthropic.Error
		if errors.As(err, &ae) {
			return "", 0, model, fmt.Errorf("HTTP %d", ae.StatusCode)
		}
		var ue *url.Error
		if errors.As(err, &ue) {
			return "", 0, model, ue.Err
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return "", 0, model, err
		}
		return "", 0, model, errors.New("request failed")
	}
	if r.StopReason == anthropic.BetaStopReasonRefusal {
		return "", 0, model, errors.New("refused")
	}
	var text strings.Builder
	for _, b := range r.Content {
		if v, ok := b.AsAny().(anthropic.BetaTextBlock); ok {
			text.WriteString(v.Text)
		}
	}
	if text.Len() == 0 {
		return "", 0, model, errors.New("empty response")
	}
	if r.Model != "" {
		model = string(r.Model)
	}
	u := r.Usage
	return text.String(), int(u.InputTokens + u.OutputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens), model, nil
}
