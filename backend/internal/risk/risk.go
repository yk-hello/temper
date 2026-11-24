// Package risk 风险页的数据（`10` 的 F、G，12 的 4.22–4.24）：所有账户的持仓、敞口、对 BTC 的联动系数、压力测试。
package risk

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"qs/internal/auth"
	"qs/internal/trading"
)

const (
	betaBars  = 168 // 过去 7 天的 1 小时线
	minBars   = 48
	betaTTL   = 30 * time.Minute
	betaRetry = 2 * time.Minute
	betaWait  = 5 * time.Second
)

// Returns 收盘价序列 → 对数收益。
func Returns(c []float64) []float64 {
	out := make([]float64, 0, len(c))
	for i := 1; i < len(c); i++ {
		if c[i-1] > 0 && c[i] > 0 {
			out = append(out, math.Log(c[i]/c[i-1]))
		} else {
			out = append(out, 0)
		}
	}
	return out
}

// BetaCorr 对 BTC 的联动系数 = 协方差 ÷ BTC 方差；相关系数一起算。长短不一时按最新的对齐。
func BetaCorr(a, btc []float64) (beta, corr float64, ok bool) {
	n := min(len(a), len(btc))
	if n < 2 {
		return 0, 0, false
	}
	a, btc = a[len(a)-n:], btc[len(btc)-n:]
	var ma, mb float64
	for i := 0; i < n; i++ {
		ma += a[i]
		mb += btc[i]
	}
	ma /= float64(n)
	mb /= float64(n)
	var ab, aa, bb float64
	for i := 0; i < n; i++ {
		x, y := a[i]-ma, btc[i]-mb
		ab += x * y
		aa += x * x
		bb += y * y
	}
	if bb <= 0 {
		return 0, 0, false
	}
	beta = ab / bb
	if aa > 0 {
		corr = ab / math.Sqrt(aa*bb)
	}
	return beta, corr, true
}

// Beta 一个币的联动情况。Fallback：数据不够，按 1 算。
type Beta struct {
	Value    decimal.Decimal `json:"beta"`
	Corr     decimal.Decimal `json:"corr"`
	Fallback bool            `json:"fallback,omitempty"`
}

type betaEntry struct {
	until time.Time
	b     Beta
}

// Betas 联动系数，算一次缓存 30 分钟。
type Betas struct {
	Closes func(ctx context.Context, symbol string, n int) ([]float64, error)
	Now    func() time.Time

	mu    sync.Mutex
	cache map[string]betaEntry
}

func symbolOf(inst string) string { return strings.TrimSuffix(inst, "-PERP") + "USDT" }

// Get 取联动系数。
func (s *Betas) Get(ctx context.Context, inst string) Beta {
	one := decimal.NewFromInt(1)
	if inst == "BTC-PERP" {
		return Beta{Value: one, Corr: one}
	}
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	s.mu.Lock()
	if e, ok := s.cache[inst]; ok && now.Before(e.until) {
		s.mu.Unlock()
		return e.b
	}
	s.mu.Unlock()
	b := Beta{Value: one, Fallback: true}
	if s.Closes != nil {
		ctx, cancel := context.WithTimeout(ctx, betaWait) // 交易所慢的时候不能拖住风险页
		defer cancel()
		a, err1 := s.Closes(ctx, symbolOf(inst), betaBars)
		btc, err2 := s.Closes(ctx, "BTCUSDT", betaBars)
		if err1 == nil && err2 == nil && len(a) >= minBars && len(btc) >= minBars {
			if v, c, ok := BetaCorr(Returns(a), Returns(btc)); ok {
				b = Beta{Value: decimal.NewFromFloat(v).Round(2), Corr: decimal.NewFromFloat(c).Round(2)}
			}
		}
	}
	s.mu.Lock()
	if s.cache == nil {
		s.cache = map[string]betaEntry{}
	}
	ttl := betaTTL
	if b.Fallback {
		ttl = betaRetry // 取不到的过一会儿再试
	}
	s.cache[inst] = betaEntry{now.Add(ttl), b}
	s.mu.Unlock()
	return b
}

// Position 一个仓位，加上是谁在管和联动情况。
type Position struct {
	trading.PosView
	Engine       string          `json:"engine"` // manual | ai
	Beta         decimal.Decimal `json:"beta"`
	Corr         decimal.Decimal `json:"corr"`
	BetaFallback bool            `json:"beta_fallback,omitempty"`
}

// Account 一个账户的风险全貌。
type Account struct {
	ID             int64                 `json:"id"`
	Name           string                `json:"name"`
	Following      bool                  `json:"following"`
	Equity         decimal.Decimal       `json:"equity"`
	SpotEquity     decimal.Decimal       `json:"spot_equity"`
	Transfers      decimal.Decimal       `json:"transfers"` // 累计划转净额：纪律仪表算盈亏要扣掉，划转不是赚亏
	Covered        decimal.Decimal       `json:"covered"`   // 穿仓时系统补的钱（累计）
	Available      decimal.Decimal       `json:"available"`
	PositionMargin decimal.Decimal       `json:"position_margin"`
	Gross          decimal.Decimal       `json:"gross"`     // 总敞口：名义价值相加
	Net            decimal.Decimal       `json:"net"`       // 净敞口：多减空
	BTCEquiv       decimal.Decimal       `json:"btc_equiv"` // 折合 BTC 敞口：带符号的名义价值 × 联动系数
	Positions      []Position            `json:"positions"`
	Stress         *trading.StressResult `json:"stress"`
}

// Exposure 算一个账户的三个敞口（手算样例在测试里）。
func Exposure(list []Position) (gross, net, btc decimal.Decimal) {
	for _, p := range list {
		n := p.Notional.Abs()
		if p.Side == "short" {
			n = n.Neg()
		}
		gross = gross.Add(p.Notional.Abs())
		net = net.Add(n)
		btc = btc.Add(n.Mul(p.Beta))
	}
	return gross, net, btc
}

// API 风险页接口。只看自己的账户。
type API struct {
	BotOn  func(acct int64, inst string) bool // 这个仓位是机器人的吗
	Spot   func(acct int64) decimal.Decimal   // 现货资产折合 USDT
	Users  *auth.Service
	Gate   *auth.Gate
	Trader *trading.Engine
	Betas  *Betas
	Manual func(acct int64) (manual map[string]bool, following bool) // 这个账户手动管理的品种
	// History 历史日线（历史情景、最坏的一天，scenario.go）；不设就没有这两项
	History *History
}

func errorsAs(err error, target any) bool { return errors.As(err, target) }

// Routes 注册接口。
func (h *API) Routes(mux *http.ServeMux) {
	h.scenarioRoutes(mux)
	mux.HandleFunc("GET /api/risk/overview", func(w http.ResponseWriter, req *http.Request) {
		if h.Gate != nil && !h.Gate.IsOpen() {
			w.Header().Set("Retry-After", "5")
			writeJSON(w, 503, map[string]string{"code": "STARTING"})
			return
		}
		h.Users.Require(func(w http.ResponseWriter, req *http.Request) {
			u := auth.UserFrom(req.Context())
			papers, err := h.Users.PaperAccounts(req.Context(), u.ID)
			if err != nil {
				writeJSON(w, 500, map[string]string{"code": "INTERNAL_ERROR"})
				return
			}
			out := []Account{}
			for _, p := range papers {
				a, err := h.account(req.Context(), p)
				if err != nil {
					var r *trading.Reject
					if errors.As(err, &r) {
						continue // 账户在引擎里还没开好
					}
					writeJSON(w, 500, map[string]string{"code": "INTERNAL_ERROR"})
					return
				}
				out = append(out, *a)
			}
			writeJSON(w, 200, map[string]any{"accounts": out})
		})(w, req)
	})
}

func (h *API) account(ctx context.Context, p auth.PaperAccount) (*Account, error) {
	v, err := h.Trader.View(p.ID)
	if err != nil {
		return nil, err
	}
	manual, following := map[string]bool(nil), false
	if h.Manual != nil {
		manual, following = h.Manual(p.ID)
	}
	var spotV decimal.Decimal
	if h.Spot != nil {
		spotV = h.Spot(p.ID)
	}
	var moved decimal.Decimal
	if b, err := h.Trader.Brief(p.ID); err == nil {
		moved = b.Transfers
	}
	a := &Account{ID: p.ID, Name: p.Name, Following: following, SpotEquity: spotV, Transfers: moved, Covered: v.Covered, Equity: v.Equity, Available: v.Available, PositionMargin: v.PositionMargin, Positions: []Position{}}
	betas := map[string]Beta{}
	for _, pos := range v.Positions {
		b := h.Betas.Get(ctx, pos.Instrument)
		betas[pos.Instrument] = b
		engine := "manual"
		if following && !manual[pos.Instrument] {
			engine = "ai"
		}
		if h.BotOn != nil && h.BotOn(p.ID, pos.Instrument) {
			engine = "bot"
		}
		a.Positions = append(a.Positions, Position{PosView: pos, Engine: engine, Beta: b.Value, Corr: b.Corr, BetaFallback: b.Fallback})
	}
	a.Gross, a.Net, a.BTCEquiv = Exposure(a.Positions)
	a.Stress, err = h.Trader.Stress(p.ID, func(inst string) decimal.Decimal {
		if b, ok := betas[inst]; ok {
			return b.Value
		}
		return decimal.NewFromInt(1)
	})
	if err != nil {
		return nil, err
	}
	return a, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
