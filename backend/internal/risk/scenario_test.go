package risk

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 最坏的一天：按现在的仓位重演每天的涨跌，排好取分位；没有数据的那天按联动系数乘 BTC
func TestWorstDay(t *testing.T) {
	btc := map[string]float64{}
	sol := map[string]float64{}
	for i := 0; i < 100; i++ {
		d := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i).Format("2006-01-02")
		btc[d] = float64(i-50) / 1000 // −5% … +4.9%
		if i != 0 {
			sol[d] = 2 * btc[d]
		}
	}
	// 多 10,000 的 BTC、空 5,000 的 SOL（SOL 第一天没数据，按 2 倍 BTC 估）
	w := worstDay(map[string]float64{"BTC-PERP": 10000, "SOL-PERP": -5000}, map[string]map[string]float64{"BTC-PERP": btc, "SOL-PERP": sol}, map[string]float64{"BTC-PERP": 1, "SOL-PERP": 2}, 20000)
	// 每天盈亏 = 10000 r − 5000 × 2r = 0：完全对冲
	if w == nil || w.Days != 100 || w.Worst != 0 || w.P99 != 0 {
		t.Fatalf("对冲后每天都是 0：%+v", w)
	}
	w = worstDay(map[string]float64{"BTC-PERP": 10000}, map[string]map[string]float64{"BTC-PERP": btc}, map[string]float64{"BTC-PERP": 1}, 20000)
	if w.Worst != -500 || w.WorstDate != "2026-01-01" || w.P99 != -500 || w.P95 != -460 {
		t.Fatalf("100 天：最差 −5%% × 10000、第 1 个、第 5 个：%+v", w)
	}
	if Quantile([]float64{-3, -2, -1}, 0.5) != -2 || Quantile(nil, 0.1) != 0 {
		t.Fatal("分位")
	}
}

// 历史日线：那天还没上市（交易所给的第一根不是那天）返回 nil；一年的去掉今天没收盘的
func TestHistory(t *testing.T) {
	day := time.Date(2025, 10, 10, 0, 0, 0, 0, time.UTC).UnixMilli()
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		q := r.URL.Query()
		switch {
		case q.Get("symbol") == "NEWUSDT":
			_, _ = fmt.Fprintf(w, `[[%d,"1","1","1","1"]]`, day+86400_000*30) // 30 天后才上市
		case q.Get("limit") == "1":
			_, _ = fmt.Fprintf(w, `[[%d,"100","101","34","85"]]`, day)
		default:
			now := time.Now().UTC().Truncate(24 * time.Hour).UnixMilli()
			_, _ = fmt.Fprintf(w, `[[%d,"0","0","0","100"],[%d,"0","0","0","90"],[%d,"0","0","0","99"]]`, now-2*86400_000, now-86400_000, now)
		}
	}))
	defer srv.Close()
	h := &History{FAPI: srv.URL}
	ctx := context.Background()
	b, err := h.Day(ctx, "SOLUSDT", "2025-10-10")
	if err != nil || b == nil || b.L != 34 || b.C != 85 {
		t.Fatalf("%v %+v", err, b)
	}
	if b, _ := h.Day(ctx, "SOLUSDT", "2025-10-10"); b == nil || calls != 1 {
		t.Fatal("同一天第二次不再去取")
	}
	if b, err := h.Day(ctx, "NEWUSDT", "2025-10-10"); err != nil || b != nil {
		t.Fatalf("那天还没上市：%v %+v", err, b)
	}
	y, err := h.Year(ctx, "BTCUSDT")
	if err != nil || len(y) != 1 {
		t.Fatalf("今天没收盘的不算：%v %v", err, y)
	}
	for d, r := range y {
		if math.Abs(r+0.1) > 1e-9 || !strings.HasPrefix(d, "20") {
			t.Fatalf("%s %v", d, r)
		}
	}
}
