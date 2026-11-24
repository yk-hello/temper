// Package aireview AI 复盘点评（模块 7，D85）：用户点一下，只点评这一笔已经平掉的交易。
//
// 边界：发出去的只有这笔交易的数字（Facts），没有邮箱、账号、编号、笔记。
// 只复盘，不预测、不建议买卖、不谈收益（宪法、D16、D23）；模型不听话时由 Blocked 拦下，不给用户看。
// 走 OpenAI 兼容的接口，换一家只改配置（AI_BASE_URL、AI_API_KEY、AI_MODEL）。
package aireview

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// 额度：先防滥用，也保护这把 key 的花费。
const (
	PerUserDay = 20
	TotalDay   = 1000
	dayMs      = 24 * 60 * 60 * 1000
)

// Facts 发给模型的全部内容。加字段前想清楚：这里的东西都会出境到第三方。
type Facts struct {
	Coin       string   `json:"coin"`
	Side       string   `json:"side"`
	Entry      float64  `json:"entry"`
	Exit       float64  `json:"exit"`
	NetUSDT    float64  `json:"net_usdt"`
	FeesUSDT   float64  `json:"fees_usdt"`
	Funding    float64  `json:"funding_usdt"`
	SlipUSDT   *float64 `json:"slippage_usdt,omitempty"`
	R          *float64 `json:"r_multiple,omitempty"`
	HoldMin    float64  `json:"hold_minutes"`
	StopDist   *float64 `json:"initial_stop_distance_pct,omitempty"`
	MAEPct     *float64 `json:"max_adverse_pct,omitempty"`
	MFEPct     *float64 `json:"max_favorable_pct,omitempty"`
	ClosedBy   string   `json:"closed_by,omitempty"`
	Violations []string `json:"rule_violations,omitempty"`
}

// Review 一条点评。
type Review struct {
	Text   string `json:"text"`
	Model  string `json:"model"`
	Tokens int    `json:"tokens"`
	T      int64  `json:"t"`
}

// Err 拒绝。
type Err struct{ Code string }

func (e *Err) Error() string { return e.Code }

// Provider 模型服务。
type Provider interface {
	Complete(ctx context.Context, system, user string) (text string, tokens int, model string, err error)
}

// ---------- OpenAI 兼容接口 ----------

// Chat 任何 OpenAI 兼容的服务都能用：OpenAI、谷歌 Gemini、DeepSeek、通义、Kimi、中转站。换一家只改配置。
type Chat struct {
	Key    string
	Model  string
	Base   string         // 到 /chat/completions 之前的部分，例如 https://api.deepseek.com
	Effort string         // 思考程度（D112）：空 = 不传，厂商自己定；none / low / medium / high / xhigh 原样传 reasoning_effort
	Extra  map[string]any // 这家服务特有的参数
	HTTP   *http.Client
}

// Build 按接口格式造一个（系统页的厂商列表、配置文件都走这里）。
func Build(format, key, base, model, effort string) Provider {
	if format == "anthropic" {
		return &Claude{Key: key, Base: base, Model: model, Effort: effort}
	}
	return &Chat{Key: key, Base: base, Model: model, Effort: effort}
}

// Complete 问一次。错误里不带 key：key 在请求头，地址里没有；回复的内容也不进错误（可能带上 key）。
func (d *Chat) Complete(ctx context.Context, system, user string) (string, int, string, error) {
	model := d.Model
	hc := d.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 58 * time.Second}
	}
	req0 := map[string]any{
		"model":    model,
		"messages": []map[string]string{{"role": "system", "content": system}, {"role": "user", "content": user}},
		"stream":   false,
	}
	// 不传 temperature：推理模型（OpenAI 的 o 系列、GPT-5 以后）只认默认值，传了直接 400（11-23）。
	// 输出上限：带思考的模型会先花掉一部分，给少了回复是空的；选了思考程度就给足。
	// OpenAI 官方的 max_tokens 已经废弃、o 系列不认，用 max_completion_tokens；别家大多只认 max_tokens。
	thinking := d.Effort != "" && d.Effort != "none"
	limit := 2500
	if thinking {
		limit = 8000
	}
	if strings.Contains(d.Base, "api.openai.com") {
		req0["max_completion_tokens"] = limit * 2
	} else {
		req0["max_tokens"] = limit
	}
	if d.Effort != "" {
		req0["reasoning_effort"] = d.Effort
	}
	for k, v := range d.Extra {
		req0[k] = v
	}
	body, _ := json.Marshal(req0)
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(d.Base, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", 0, model, errors.New("bad request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+d.Key)
	resp, err := hc.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			return "", 0, model, ue.Err
		}
		return "", 0, model, errors.New("request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return "", 0, model, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var r struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			Total int `json:"total_tokens"`
		} `json:"usage"`
		Model string `json:"model"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r); err != nil {
		return "", 0, model, errors.New("bad response")
	}
	if len(r.Choices) == 0 {
		return "", 0, model, errors.New("empty response")
	}
	if r.Model != "" {
		model = r.Model
	}
	return r.Choices[0].Message.Content, r.Usage.Total, model, nil
}

// DeepSeek 官方接口。关掉思考模式：点评不需要。
func DeepSeek(key, model, base string) *Chat {
	if base == "" {
		base = "https://api.deepseek.com"
	}
	if model == "" {
		model = "deepseek-flash"
	}
	return &Chat{Key: key, Model: model, Base: base, Extra: map[string]any{"thinking": map[string]string{"type": "disabled"}}}
}

// ---------- 提示词 ----------

// 中英文各写一份，不是互相翻译。
const systemZh = `你替交易者复盘一笔已经平仓的模拟交易，依据只有用户给的这组数字。
字段含义：net_usdt 净盈亏；r_multiple 净盈亏是初始风险的几倍；initial_stop_distance_pct 初始止损离开仓价的百分比，没有这个字段就是没设止损；max_adverse_pct 持仓期间最大浮亏幅度；max_favorable_pct 持仓期间最大浮盈幅度；hold_minutes 持仓分钟数；closed_by 平仓方式：stop_loss 止损（止损已经执行了；亏损超过 1R 通常来自滑点和手续费，不代表止损没执行）、take_profit 止盈、liquidation 强平，没有这个字段就是手动平仓；rule_violations 违反的规则：no_stop 没设止损、over_risk 单笔风险超过交易者自己设的上限、moved_stop 把止损往亏损方向移过、locked_reject 纪律锁期间下单被拒。
只输出三行，每行一句话，每句不超过 40 个字，不要 Markdown：
做对了：……（只写数字能证明的。没有做对的就写“这一笔没有明显做对的地方”，不要硬夸）
先改这个：……（只说最重要的一件）
下次的规则：……（一条能照做的规则，带具体数字）
不要推测交易者的想法或计划，不要预测行情，不要建议买卖任何品种，不要提收益率或承诺盈利，不要评价交易者本人，不要编造数字里没有的信息。数字不够下结论时就直说。`

const systemEn = `You review one closed paper trade for the trader, using only the numbers provided.
Fields: net_usdt is net profit or loss. r_multiple is net result as a multiple of initial risk. initial_stop_distance_pct is how far the first stop sat from entry; if it's missing, no stop was set. max_adverse_pct and max_favorable_pct are the largest moves against and in favor of the position. hold_minutes is time held. closed_by: stop_loss (the stop did execute; a loss beyond 1R usually comes from slippage and fees, not an ignored stop), take_profit or liquidation; if it's missing, the trader closed by hand. rule_violations: no_stop (no stop set), over_risk (risk above the trader's own limit), moved_stop (stop moved further from entry), locked_reject (order rejected during a discipline lock).
Reply with exactly three lines, one short sentence each (20 words at most), no Markdown:
Did well: ... (only what the numbers prove. If nothing, write "Nothing stands out in this trade." Don't invent praise.)
Fix first: ... (the single most important thing)
Rule for next time: ... (one rule they can follow, with a concrete number)
Do not guess the trader's thinking or plans, predict the market, suggest buying or selling anything, mention returns or promise profit, judge the person, or invent anything the numbers don't show. If the numbers are too thin to conclude, say so.`

// blocked 模型偶尔不听话。出现这些词就不给用户看（宪法：不谈收益、不给交易建议）。
var blocked = regexp.MustCompile(`(?i)年化|保底|稳赚|无风险|必涨|必跌|建议(买入|卖出|做多|做空|加仓|抄底)|将会?(上涨|下跌)|\b(apr|apy|guaranteed?|risk[- ]free)\b|\b(should|recommend(ed)?)\s+(you\s+)?(buy|sell|go long|go short|buying|selling)\b|\bwill\s+(rise|fall|go up|go down|pump|dump)\b`)

// Blocked 这段话能不能给用户看。
func Blocked(s string) bool { return blocked.MatchString(s) }

// Clean 整理模型的回复：去掉 Markdown 记号和空行，最多留三行，太长就截断。
func Clean(s string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(strings.NewReplacer("**", "", "`", "", "#", "").Replace(l))
		l = strings.TrimSpace(strings.TrimLeft(l, "-*• "))
		if l != "" {
			out = append(out, l)
		}
		if len(out) == 3 {
			break
		}
	}
	r := []rune(strings.Join(out, "\n"))
	if len(r) > 500 {
		r = r[:500]
	}
	return string(r)
}

// ---------- 服务 ----------

// Service 服务。
type Service struct {
	Pool     *pgxpool.Pool
	Provider Provider
	provMu   sync.RWMutex // 管理后台随时能换（2026-11-15）
	Now      func() int64

	mu       sync.Mutex
	inflight map[string]bool
	mem      map[string]memRow
}

type memRow struct {
	Review
	user int64
}

func (s *Service) now() int64 {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UnixMilli()
}

// Enabled 配了 key 吗。
func (s *Service) Enabled() bool { return s != nil && s.prov() != nil }

// SetProvider 换模型服务（管理后台）；nil = 关掉 AI 点评。
func (s *Service) SetProvider(p Provider) {
	s.provMu.Lock()
	s.Provider = p
	s.provMu.Unlock()
}

func (s *Service) prov() Provider {
	s.provMu.RLock()
	defer s.provMu.RUnlock()
	return s.Provider
}

// Attach 建表。
func (s *Service) Attach(ctx context.Context, pool *pgxpool.Pool) error {
	s.Pool = pool
	_, err := pool.Exec(ctx, `create table if not exists ai_reviews (user_id bigint not null, acct bigint not null, trade_id text not null, lang text not null, text text not null, model text not null, tokens int not null, t bigint not null, primary key (user_id, acct, trade_id, lang))`)
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx, `create index if not exists ai_reviews_t on ai_reviews (t)`)
	return err
}

func key(user, acct int64, id, lang string) string {
	return fmt.Sprintf("%d:%d:%s:%s", user, acct, id, lang)
}

// Get 已经有的点评。
func (s *Service) Get(ctx context.Context, user, acct int64, id, lang string) (*Review, error) {
	if s.Pool == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if r, ok := s.mem[key(user, acct, id, lang)]; ok {
			c := r.Review
			return &c, nil
		}
		return nil, nil
	}
	r := &Review{}
	err := s.Pool.QueryRow(ctx, `select text, model, tokens, t from ai_reviews where user_id = $1 and acct = $2 and trade_id = $3 and lang = $4`, user, acct, id, lang).Scan(&r.Text, &r.Model, &r.Tokens, &r.T)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			return nil, nil
		}
		return nil, err
	}
	return r, nil
}

// used 这个人今天用了几次、今天一共用了几次。
func (s *Service) used(ctx context.Context, user int64) (int, int) {
	since := s.now() - dayMs
	if s.Pool == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		mine, all := 0, 0
		for _, r := range s.mem {
			if r.T >= since {
				all++
				if r.user == user {
					mine++
				}
			}
		}
		return mine, all
	}
	var mine, all int
	_ = s.Pool.QueryRow(ctx, `select count(*) filter (where user_id = $1), count(*) from ai_reviews where t >= $2`, user, since).Scan(&mine, &all)
	return mine, all
}

// Left 这个人今天还能生成几次。
func (s *Service) Left(ctx context.Context, user int64) int {
	mine, all := s.used(ctx, user)
	return max(0, min(PerUserDay-mine, TotalDay-all))
}

// Generate 生成一条。已有的直接返回，不花钱。
func (s *Service) Generate(ctx context.Context, user, acct int64, id, lang string, f Facts) (*Review, error) {
	if !s.Enabled() {
		return nil, &Err{"AI_OFF"}
	}
	if r, err := s.Get(ctx, user, acct, id, lang); err != nil {
		return nil, err
	} else if r != nil {
		return r, nil
	}
	if s.Left(ctx, user) <= 0 {
		return nil, &Err{"AI_QUOTA"}
	}
	k := key(user, acct, id, lang)
	s.mu.Lock()
	if s.inflight == nil {
		s.inflight = map[string]bool{}
	}
	if s.inflight[k] { // 连点两下只算一次
		s.mu.Unlock()
		return nil, &Err{"AI_BUSY"}
	}
	s.inflight[k] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.inflight, k)
		s.mu.Unlock()
	}()

	sys, label := systemZh, "这笔交易："
	if lang == "en" {
		sys, label = systemEn, "The trade: "
	}
	b, _ := json.Marshal(f)
	// 偶尔会等很久然后回空，所以限时再试一次。两次加起来不超过 55 秒：nginx 默认只等 60 秒
	var text, model string
	var tokens int
	var err error
	for _, limit := range []time.Duration{35 * time.Second, 20 * time.Second} {
		c, cancel := context.WithTimeout(ctx, limit)
		text, tokens, model, err = s.prov().Complete(c, sys, label+string(b))
		cancel()
		text = Clean(text)
		if err == nil && text != "" && !Blocked(text) {
			break
		}
		if ctx.Err() != nil {
			break
		}
	}
	if err != nil {
		return nil, fmt.Errorf("AI_FAILED: %w", err)
	}
	if text == "" {
		return nil, &Err{"AI_FAILED"}
	}
	if Blocked(text) {
		return nil, &Err{"AI_FILTERED"}
	}
	r := &Review{Text: text, Model: model, Tokens: tokens, T: s.now()}
	if s.Pool == nil {
		s.mu.Lock()
		if s.mem == nil {
			s.mem = map[string]memRow{}
		}
		s.mem[k] = memRow{*r, user}
		s.mu.Unlock()
		return r, nil
	}
	_, err = s.Pool.Exec(ctx, `insert into ai_reviews (user_id, acct, trade_id, lang, text, model, tokens, t) values ($1,$2,$3,$4,$5,$6,$7,$8) on conflict do nothing`, user, acct, id, lang, r.Text, r.Model, r.Tokens, r.T)
	if err != nil {
		return nil, err
	}
	return r, nil
}

// Pct 算百分比，保留两位。给构造 Facts 用。
func Pct(part, whole float64) *float64 {
	if whole == 0 {
		return nil
	}
	v := math.Round(part/whole*10000) / 100
	return &v
}
