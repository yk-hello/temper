package risk

import (
	"context"
	"math"
	"testing"

	"github.com/shopspring/decimal"

	"qs/internal/trading"
)

func near(t *testing.T, name string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Fatalf("%s = %v，应 %v", name, got, want)
	}
}

// 手算：BTC 收益 (1, −1, 1, −1)，均值 0，方差和 = 4。
// 币 A 收益 = 2 × BTC：协方差和 = 8，β = 8 ÷ 4 = 2，相关系数 1。
// 币 B 收益 (1, 1, −1, −1)：和 BTC 的协方差和 = 0，β = 0，相关系数 0。
func TestBetaByHand(t *testing.T) {
	btc := []float64{1, -1, 1, -1}
	b, c, ok := BetaCorr([]float64{2, -2, 2, -2}, btc)
	if !ok {
		t.Fatal("应能算")
	}
	near(t, "β", b, 2, 1e-9)
	near(t, "相关", c, 1, 1e-9)
	b, c, _ = BetaCorr([]float64{1, 1, -1, -1}, btc)
	near(t, "β", b, 0, 1e-9)
	near(t, "相关", c, 0, 1e-9)
	// 反向：β = −1
	b, c, _ = BetaCorr([]float64{-1, 1, -1, 1}, btc)
	near(t, "β", b, -1, 1e-9)
	near(t, "相关", c, -1, 1e-9)
	// BTC 没波动：算不了
	if _, _, ok := BetaCorr([]float64{1, 2}, []float64{3, 3}); ok {
		t.Fatal("BTC 没波动时算不了")
	}
	// 长短不一时按最新的对齐
	b, _, _ = BetaCorr([]float64{9, 9, 2, -2, 2, -2}, btc)
	near(t, "对齐后 β", b, 2, 1e-9)
}

func TestReturnsByHand(t *testing.T) {
	r := Returns([]float64{100, 110, 0, 50})
	near(t, "ln(1.1)", r[0], math.Log(1.1), 1e-12)
	if r[1] != 0 || r[2] != 0 {
		t.Fatalf("价格是 0 时收益记 0：%v", r)
	}
}

// BTC 永远是 1；数据不够按 1 算并标出来；算过的缓存
func TestBetasFallback(t *testing.T) {
	calls := 0
	s := &Betas{Closes: func(_ context.Context, sym string, n int) ([]float64, error) {
		calls++
		if sym == "NEWUSDT" {
			return []float64{1, 2, 3}, nil
		}
		out := make([]float64, n)
		for i := range out {
			out[i] = 100 + float64(i%2)
		}
		return out, nil
	}}
	ctx := context.Background()
	if b := s.Get(ctx, "BTC-PERP"); !b.Value.Equal(decimal.NewFromInt(1)) || b.Fallback || calls != 0 {
		t.Fatalf("BTC：%+v，取数 %d 次", b, calls)
	}
	if b := s.Get(ctx, "NEW-PERP"); !b.Fallback || !b.Value.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("数据不够：%+v", b)
	}
	b := s.Get(ctx, "ETH-PERP")
	if b.Fallback || !b.Value.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("走势一样时 β = 1：%+v", b)
	}
	n := calls
	s.Get(ctx, "ETH-PERP")
	if calls != n {
		t.Fatal("应该走缓存")
	}
}

// 手算：多 BTC 名义 10,000（β 1）、多 SOL 名义 5,000（β 2）、空 ETH 名义 4,000（β 1.5）
// 总敞口 19,000；净敞口 11,000；折合 BTC = 10,000 + 10,000 − 6,000 = 14,000
func TestExposureByHand(t *testing.T) {
	d := decimal.RequireFromString
	pos := func(side, n, beta string) Position {
		return Position{PosView: trading.PosView{Side: side, Notional: d(n)}, Beta: d(beta)}
	}
	g, n, b := Exposure([]Position{pos("long", "10000", "1"), pos("long", "5000", "2"), pos("short", "4000", "1.5")})
	if !g.Equal(d("19000")) || !n.Equal(d("11000")) || !b.Equal(d("14000")) {
		t.Fatalf("总 %s 净 %s 折合 %s", g, n, b)
	}
}
