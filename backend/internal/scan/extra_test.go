package scan

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

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestExtras(t *testing.T) {
	h0 := time.Now().Truncate(time.Hour).Unix() - 3600 // 最近一根收盘的小时线
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/products/BTC-USD/candles": // Coinbase：新的在前
			gotUA = r.Header.Get("User-Agent")
			_, _ = fmt.Fprintf(w, `[[%d,1,1,1,100100],[%d,1,1,1,100000],[%d,1,1,1,99000]]`, h0, h0-3600, h0-7200)
		case r.URL.Path == "/api/v3/klines": // 币安现货：旧的在前，第一列毫秒
			_, _ = fmt.Fprintf(w, `[[%d,"0","0","0","100000"],[%d,"0","0","0","100000"],[%d,"0","0","0","100000"]]`, (h0-7200)*1000, (h0-3600)*1000, h0*1000)
		case r.URL.Path == "/api/v2/public/get_volatility_index_data":
			now := time.Now().UnixMilli()
			v := 50.0
			if r.URL.Query().Get("currency") == "ETH" {
				v = 70
			}
			_, _ = fmt.Fprintf(w, `{"result":{"data":[[%d,1,1,1,%v],[%d,1,1,1,%v]]}}`, now-25*3600_000, v-5, now-600_000, v)
		case r.URL.Path == "/stablecoins":
			_, _ = w.Write([]byte(`{"peggedAssets":[{"pegType":"peggedUSD","circulating":{"peggedUSD":100},"circulatingPrevDay":{"peggedUSD":90},"circulatingPrevWeek":{"peggedUSD":80}},
				{"pegType":"peggedUSD","circulating":{"peggedUSD":50},"circulatingPrevDay":{"peggedUSD":50},"circulatingPrevWeek":{"peggedUSD":60}},
				{"pegType":"peggedEUR","circulating":{"peggedEUR":999}}]}`))
		case r.URL.Path == "/fred/series/observations":
			if r.URL.Query().Get("api_key") != "k" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"observations":[{"date":"2026-11-12","value":"."},{"date":"2026-11-11","value":"4.20"},{"date":"2026-11-10","value":"4.10"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	p := &Public{Spot: srv.URL, Coinbase: srv.URL, Deribit: srv.URL, Llama: srv.URL, FRED: srv.URL}
	ctx := context.Background()

	pr, err := p.Premium(ctx)
	if err != nil || len(pr.Hours) != 3 || !near(pr.Now, 0.1) || !near(pr.Hours[0], -1) {
		t.Fatalf("Coinbase 溢价：%v %+v", err, pr)
	}
	if !strings.HasPrefix(gotUA, "qs/") {
		t.Fatalf("要带 User-Agent：%q", gotUA)
	}
	iv, err := p.ImpliedVol(ctx)
	if err != nil || iv.BTC != 50 || iv.BTCPrev != 45 || iv.ETH != 70 {
		t.Fatalf("隐含波动：%v %+v", err, iv)
	}
	st, err := p.Stable(ctx)
	if err != nil || st.Total != 150 || st.Day != 10 || st.Week != 10 {
		t.Fatalf("稳定币（只算锚定美元的）：%v %+v", err, st)
	}
	if m, err := p.Macro(ctx); err != nil || m != nil {
		t.Fatal("没配 FRED 密钥：不取")
	}
	p.FREDKey = "k"
	m, err := p.Macro(ctx)
	if err != nil || len(m) != 3 || m[1].ID != "us10y" || m[1].Value != 4.2 || m[1].Prev != 4.1 || m[1].Date != "2026-11-11" {
		t.Fatalf("宏观（跳过没数据的那天）：%v %+v", err, m)
	}
	p.FREDKey = "wrong-key-123"
	if _, err := p.Macro(ctx); err == nil || strings.Contains(err.Error(), "wrong-key-123") {
		t.Fatalf("密钥不对要报错，且不带出密钥：%v", err)
	}
}
