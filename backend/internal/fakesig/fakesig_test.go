package fakesig

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"qs/internal/book"
	"qs/internal/portfolio"
	"qs/internal/qsig"
	"qs/internal/trading"
)

// 第 29 条：示例理由和方向一致，而且写明是示例
func TestReasonMatchesDirection(t *testing.T) {
	for i := 0; i < 50; i++ {
		long, _ := ReasonFor(0.3)
		short, _ := ReasonFor(-0.3)
		flat, _ := ReasonFor(0)
		if strings.Contains(long, "空") || !strings.Contains(long, "多") {
			t.Fatalf("做多配了：%s", long)
		}
		if strings.Contains(short, "多") || !strings.Contains(short, "空") {
			t.Fatalf("做空配了：%s", short)
		}
		if strings.Contains(flat, "多") || strings.Contains(flat, "试空") {
			t.Fatalf("空仓配了：%s", flat)
		}
		if !strings.Contains(long, "示例") || !strings.Contains(short, "示例") || !strings.Contains(flat, "示例") {
			t.Fatalf("要写明示例：%s %s %s", long, short, flat)
		}
	}
}

// 示例信号的格式要过 qsig 校验（改过 evidence 之后 8080 上全被拒，靠这个测试防住）
func TestSignalPassesSchema(t *testing.T) {
	const venue = "binance-usdm"
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	books := book.NewStore()
	m := decimal.RequireFromString("77000")
	books.SetBook(book.Book{Venue: venue, Instrument: "BTC-PERP", Bids: []book.Level{{Px: m, Qty: m}}, Asks: []book.Level{{Px: m, Qty: m}}, Time: time.Now().UnixMilli()})
	books.UpdateTicker(venue, "BTC-PERP", func(x *book.Ticker) { x.Last, x.Mark = m, m })
	eng := trading.New(books, venue, trading.NewSpecs(trading.DefaultSpecs()), log)
	pf := portfolio.New("demo", -1, venue, venue, books, eng)
	svc, err := qsig.NewService([]qsig.Key{{ID: "k", Secret: "s", StrategyID: "demo"}}, []qsig.Instrument{{Instrument: "BTC-PERP", Tradable: true}}, books, venue,
		func(string) *portfolio.Portfolio { return pf }, log)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()
	cfg := Config{BaseURL: srv.URL, KeyID: "k", Secret: "s", StrategyID: "demo"}
	for _, w := range []float64{0.4, -0.3, 0} {
		code, body, err := Send(context.Background(), cfg, "POST", "/v1/messages", SignalMessage("demo", "BTC-PERP", "77000", w, time.Now().UTC()))
		if err != nil || code != 200 {
			t.Fatalf("权重 %v：%d %s %v", w, code, body, err)
		}
	}
}
