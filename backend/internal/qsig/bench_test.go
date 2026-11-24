package qsig

import (
	"encoding/json"
	"testing"
	"time"

	"qs/internal/book"
	"qs/internal/ledger"
	"qs/internal/market"
	"qs/internal/portfolio"
	"qs/internal/trading"

	"github.com/shopspring/decimal"
)

// 一条信号从 HTTP 请求进来到回复（签名校验 + 格式校验 + 业务检查 + 记录），不含异步执行
func BenchmarkSignalHTTP(b *testing.B) {
	e := newEnv(&testing.T{})
	e.svc.RatePerMin = 1 << 30
	base := time.Now()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := "0.1"
		if i%2 == 1 {
			w = "-0.1"
		}
		msg := signal(newID(), base.Add(time.Duration(i)*time.Microsecond), target("BTC-PERP", w, "77000"))
		body, _ := json.Marshal(msg)
		code, _ := e.do("POST", "/v1/messages", "k1", "s3cret", body, 0)
		if code != 200 {
			b.Fatalf("code %d", code)
		}
	}
}

// 组合执行：目标权重 → 订单 → 按盘口逐档成交 → 记账（immediate）
func BenchmarkApplyAndFill(b *testing.B) {
	books := book.NewStore()
	setBook(books, "BTC-PERP", "77000")
	eng := trading.New(books, venue, trading.NewSpecs(trading.DefaultSpecs()), quiet())
	pf := portfolio.New("p", -1, venue, venue, books, eng)
	ref := decimal.RequireFromString("77000")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := decimal.RequireFromString("0.3")
		if i%2 == 1 {
			w = w.Neg()
		}
		pf.Apply(newID(), int64(i+1), time.Now().Add(time.Minute).UnixMilli(), false,
			[]portfolio.Target{{Instrument: "BTC-PERP", Weight: w, RefPrice: ref, Urgency: "immediate", MaxSlipBps: 20}})
	}
	b.StopTimer()
	if err := eng.Check(); err != nil {
		b.Fatal(err)
	}
}

// 盘口：一条 20 档深度推送从原始 JSON 到写进盘口仓库
func BenchmarkDepthIngest(b *testing.B) {
	raw := []byte(`{"stream":"btcusdt@depth20@100ms","data":{"e":"depthUpdate","E":1,"T":1,"s":"BTCUSDT","U":1,"u":2,"pu":0,"b":[` + lv(20, 77000, -0.1) + `],"a":[` + lv(20, 77000.1, 0.1) + `]}}`)
	fan := &market.Fanout{Primary: "none", Books: book.NewStore()}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ev, ok := market.ParseBinance(raw)
		if !ok {
			b.Fatal("parse")
		}
		fan.Publish(ev)
	}
}

func lv(n int, start, step float64) string {
	out := ""
	for i := 0; i < n; i++ {
		if i > 0 {
			out += ","
		}
		p := decimal.NewFromFloat(start + step*float64(i)).StringFixed(1)
		out += `["` + p + `","1.234"]`
	}
	return out
}

func BenchmarkLedgerPost(b *testing.B) {
	led := ledger.New()
	amt := decimal.RequireFromString("1.2345")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = led.Post(ledger.Entry{Portfolio: "p", Lines: []ledger.Line{{Account: ledger.Cash, Amount: amt}, {Account: ledger.Fee, Amount: amt.Neg()}}})
	}
}
