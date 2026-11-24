package aireview

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"
)

func fp(v float64) *float64 { return &v }

// 几种真实会遇到的交易。都是编造的数字，不是任何人的交易。
var realCases = []struct {
	name string
	f    Facts
}{
	{"盈利但超了风险上限", Facts{Coin: "BTC", Side: "long", Entry: 96120.5, Exit: 96510, NetUSDT: 31.2, FeesUSDT: 7.8, Funding: -0.4, R: fp(0.4), SlipUSDT: fp(-0.8),
		HoldMin: 42, StopDist: fp(1.1), MAEPct: fp(1.0), MFEPct: fp(2.3), Violations: []string{"over_risk"}}},
	{"止损离场，守规矩", Facts{Coin: "ETH", Side: "short", Entry: 3210.4, Exit: 3242.5, NetUSDT: -101.6, FeesUSDT: 4.1, Funding: 0.3, R: fp(-1.02), SlipUSDT: fp(-1.2),
		HoldMin: 188, StopDist: fp(1.0), MAEPct: fp(1.0), MFEPct: fp(0.6), ClosedBy: "stop_loss"}},
	{"没设止损，被强平", Facts{Coin: "SOL", Side: "long", Entry: 182.34, Exit: 171.2, NetUSDT: -2980.5, FeesUSDT: 18.4, Funding: -6.2,
		HoldMin: 1320, MAEPct: fp(6.1), MFEPct: fp(0.2), ClosedBy: "liquidation", Violations: []string{"no_stop"}}},
	{"止盈，移动过止损", Facts{Coin: "BTC", Side: "short", Entry: 97400, Exit: 95500, NetUSDT: 402.7, FeesUSDT: 15.2, Funding: 2.1, R: fp(2.6), SlipUSDT: fp(0.5),
		HoldMin: 610, StopDist: fp(0.8), MAEPct: fp(1.2), MFEPct: fp(2.1), ClosedBy: "take_profit", Violations: []string{"moved_stop"}}},
	{"止损执行了但亏超 1R", Facts{Coin: "BTC", Side: "long", Entry: 68164.5, Exit: 67480.2, NetUSDT: -37.6, FeesUSDT: 3.4, Funding: 0, R: fp(-1.38), SlipUSDT: fp(-6.75),
		HoldMin: 0.05, StopDist: fp(0.79), MAEPct: fp(1.0), MFEPct: fp(0), ClosedBy: "stop_loss"}},
	{"几乎打平的短线", Facts{Coin: "DOGE", Side: "long", Entry: 0.1824, Exit: 0.18245, NetUSDT: -1.4, FeesUSDT: 1.5, Funding: 0, HoldMin: 0.4, StopDist: fp(0.3), R: fp(-0.05)}},
	{"数字很少", Facts{Coin: "XRP", Side: "long", Entry: 2.41, Exit: 2.43, NetUSDT: 8.1, FeesUSDT: 0.6, Funding: 0, HoldMin: 15}},
}

// 真的调用模型，看模型名对不对、回复像不像样。默认跳过，要花钱，也要 key。
// QS_REAL_AI=1 AI_API_KEY=... AI_BASE_URL=... AI_MODEL=... go test -run Real -v -timeout 15m ./internal/aireview
func TestRealProvider(t *testing.T) {
	key := os.Getenv("AI_API_KEY")
	if os.Getenv("QS_REAL_AI") != "1" || key == "" {
		t.Skip("默认跳过")
	}
	labels := map[string][]string{"zh": {"做对了", "先改这个", "下次的规则"}, "en": {"Did well", "Fix first", "Rule for next time"}}
	type result struct {
		name, lang, text, err string
		tokens                int
		ms                    int64
		bad                   []string
	}
	var mu sync.Mutex
	var out []result
	var wg sync.WaitGroup
	gate := make(chan struct{}, 3) // 一次最多 3 个，别把中转站打满
	for _, c := range realCases {
		for _, lang := range []string{"zh", "en"} {
			wg.Add(1)
			go func(name string, f Facts, lang string) {
				defer wg.Done()
				gate <- struct{}{}
				defer func() { <-gate }()
				svc := &Service{Provider: &Chat{Key: key, Model: os.Getenv("AI_MODEL"), Base: os.Getenv("AI_BASE_URL")}}
				t0 := time.Now()
				rv, err := svc.Generate(context.Background(), 1, 1, name, lang, f)
				r := result{name: name, lang: lang, ms: time.Since(t0).Milliseconds()}
				if err != nil {
					r.err = err.Error()
				} else {
					r.text, r.tokens = rv.Text, rv.Tokens
					lines := strings.Split(rv.Text, "\n")
					if len(lines) != 3 {
						r.bad = append(r.bad, fmt.Sprintf("%d 行", len(lines)))
					}
					for i, l := range lines {
						if i < 3 && !strings.HasPrefix(l, labels[lang][i]) {
							r.bad = append(r.bad, "第 "+fmt.Sprint(i+1)+" 行开头不对")
						}
					}
					if lang == "en" && strings.ContainsFunc(rv.Text, func(c rune) bool { return unicode.Is(unicode.Han, c) || c > 0x2E7F }) {
						r.bad = append(r.bad, "英文混了中文或全角")
					}
				}
				mu.Lock()
				out = append(out, r)
				mu.Unlock()
			}(c.name, c.f, lang)
		}
	}
	wg.Wait()
	ok, slow := 0, int64(0)
	for _, c := range realCases {
		for _, lang := range []string{"zh", "en"} {
			for _, r := range out {
				if r.name != c.name || r.lang != lang {
					continue
				}
				if r.err == "" && len(r.bad) == 0 {
					ok++
				}
				slow = max(slow, r.ms)
				t.Logf("\n【%s｜%s】%.1f 秒，%d tokens %s %v\n%s", r.name, r.lang, float64(r.ms)/1000, r.tokens, r.err, r.bad, r.text)
			}
		}
	}
	t.Logf("\n合格 %d / %d，最慢 %.1f 秒", ok, len(out), float64(slow)/1000)
}
