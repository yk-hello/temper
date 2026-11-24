package signal

import (
	"context"
	"errors"
	"testing"

	"github.com/shopspring/decimal"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestParse(t *testing.T) {
	cases := []struct {
		in     string
		action string
		coin   string
		spot   bool
		pct    string
	}{
		{"做多 BTC 30%", "long", "BTC", false, "30"},
		{"做空 ETH 12.5%", "short", "ETH", false, "12.5"},
		{"平仓 BTC", "close", "BTC", false, "0"},
		{"long BTCUSDT.P 10%", "long", "BTC", false, "10"},
		{"buy SOL 20%", "buy", "SOL", true, "20"},
		{"买入 现货 DOGE 50%", "buy", "DOGE", true, "50"},
		{`{"action":"long","symbol":"BINANCE:BTCUSDT.P","size":"30%"}`, "long", "BTC", false, "30"},
		{`{"action":"short","ticker":"ETHUSDT","size":25,"id":"a1"}`, "short", "ETH", false, "25"},
		{`{"action":"sell","symbol":"BTC","size":"100%","market":"spot"}`, "sell", "BTC", true, "100"},
		{`{"action":"close","symbol":"BTC-PERP"}`, "close", "BTC", false, "0"},
		// 11-18：品种写明是永续时，买入 / 卖出是合约的买卖（原来会去做现货）
		{"buy BTCUSDT.P 30%", "long", "BTC", false, "30"},
		{"sell BINANCE:ETHUSDT.P 10%", "short", "ETH", false, "10"},
		{`{"action":"sell","symbol":"BTC","market":"perp","size":"5%"}`, "short", "BTC", false, "5"},
		{`{"action":"buy","symbol":"SOL-PERP","size":"5%"}`, "long", "SOL", false, "5"},
		{"做多 BTC-SPOT 10%", "long", "BTC", true, "10"},
	}
	for _, c := range cases {
		got, err := Parse([]byte(c.in))
		if err != nil {
			t.Fatalf("%s：%v", c.in, err)
		}
		if got.Action != c.action || got.Coin != c.coin || got.Spot != c.spot || !got.Pct.Equal(d(c.pct)) {
			t.Fatalf("%s：%+v", c.in, got)
		}
	}
	bad := []struct{ in, code string }{
		{"", "SIG_EMPTY"},
		{"你好 BTC 30%", "SIG_BAD_ACTION"},
		{"做多 BTC", "SIG_BAD_SIZE"},
		{"做多 BTC 300%", "SIG_BAD_SIZE"},
		{"做多 ??? 30%", "SIG_BAD_SYMBOL"},
		{`{"action":`, "SIG_BAD_JSON"},
		{"buy @ 0.01 filled on BTCUSDT.P. New strategy position is 0.01", "SIG_TV_DEFAULT"},
	}
	for _, c := range bad {
		_, err := Parse([]byte(c.in))
		if r, ok := err.(*Rej); !ok || r.Code != c.code {
			t.Fatalf("%q：应为 %s，实际 %v", c.in, c.code, err)
		}
	}
}

type fakeT struct {
	perps, spots int
	lastNotional decimal.Decimal
	lastSide     string
	closes       int
	err          error
}

func (f *fakeT) Perp(_ int64, _, side string, n decimal.Decimal, c bool) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	if c {
		f.closes++
		return "c1", nil
	}
	f.perps++
	f.lastNotional, f.lastSide = n, side
	return "o1", nil
}
func (f *fakeT) Spot(_ int64, _, side string, _ decimal.Decimal) (string, error) {
	f.spots++
	f.lastSide = side
	return "s1", f.err
}
func (f *fakeT) Available(int64) decimal.Decimal { return d("1000") }

func TestReceive(t *testing.T) {
	f := &fakeT{}
	s := New(f, nil)
	now := int64(1_700_000_000_000)
	s.Now = func() int64 { return now }
	ctx := context.Background()
	_, tok, err := s.Create(ctx, 1, 9, "趋势", d("500"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Receive(ctx, "wrong", []byte("做多 BTC 30%")); err == nil {
		t.Fatal("地址不对要拒绝")
	}
	l, err := s.Receive(ctx, tok, []byte("做多 BTC 30%"))
	if err != nil || l.Status != "ok" || f.perps != 1 || !f.lastNotional.Equal(d("150")) || f.lastSide != "buy" {
		t.Fatalf("500 的 30%% 买入：%+v %v %s", l, err, f.lastNotional)
	}
	// 同样内容 5 秒内算重复（TradingView 会重发）
	l, _ = s.Receive(ctx, tok, []byte("做多 BTC 30%"))
	if l.Status != "duplicate" || f.perps != 1 {
		t.Fatalf("重复：%+v", l)
	}
	now += 6000
	if l, _ = s.Receive(ctx, tok, []byte("做多 BTC 30%")); l.Status != "ok" || f.perps != 2 {
		t.Fatalf("过了窗口是新信号：%+v", l)
	}
	// 同一个 id 一小时内不重复执行
	s.Receive(ctx, tok, []byte(`{"action":"short","symbol":"ETH","size":"10%","id":"x"}`))
	now += 60_000
	l, _ = s.Receive(ctx, tok, []byte(`{"action":"short","symbol":"ETH","size":"20%","id":"x"}`))
	if l.Status != "duplicate" {
		t.Fatalf("同一个 id：%+v", l)
	}
	// 平仓、现货
	s.Receive(ctx, tok, []byte("平仓 BTC"))
	s.Receive(ctx, tok, []byte("buy SOL 10%"))
	if f.closes != 1 || f.spots != 1 {
		t.Fatalf("%d %d", f.closes, f.spots)
	}
	// 风控拒绝：记下来，不是 500
	f.err = rej("DISCIPLINE_LOCKED")
	now += 10_000
	l, err = s.Receive(ctx, tok, []byte("做多 XRP 10%"))
	if err != nil || l.Status != "rejected" || l.Code != "DISCIPLINE_LOCKED" {
		t.Fatalf("%+v %v", l, err)
	}
	// 11-18：带 id 的被我们这边拒了（单没下出去），解锁后同一个 id 重发要能执行，不能算重复
	l, _ = s.Receive(ctx, tok, []byte(`{"action":"long","symbol":"BTC","size":"10%","id":"retry-1"}`))
	if l.Status != "rejected" {
		t.Fatalf("纪律锁期间：%+v", l)
	}
	f.err = nil
	n := f.perps
	if l, _ = s.Receive(ctx, tok, []byte(`{"action":"long","symbol":"BTC","size":"10%","id":"retry-1"}`)); l.Status != "ok" || f.perps != n+1 {
		t.Fatalf("被拒的 id 重发应能执行：%+v", l)
	}
	// 说不清有没有下出去的（交易所超时之类）：同一个 id 仍算发过，免得重复下单
	f.err = errors.New("timeout")
	s.Receive(ctx, tok, []byte(`{"action":"long","symbol":"BTC","size":"10%","id":"retry-2"}`))
	f.err = nil
	if l, _ = s.Receive(ctx, tok, []byte(`{"action":"long","symbol":"BTC","size":"10%","id":"retry-2"}`)); l.Status != "duplicate" {
		t.Fatalf("不确定有没有下出去时不能重下：%+v", l)
	}
	// 写错的也记下来，用户才知道哪里错了
	l, _ = s.Receive(ctx, tok, []byte("随便写"))
	if l.Status != "rejected" || l.Code != "SIG_BAD_ACTION" {
		t.Fatalf("%+v", l)
	}
	logs, _ := s.Logs(ctx, 1, 100)
	if len(logs) < 5 {
		t.Fatalf("日志：%d", len(logs))
	}
	if other, _ := s.Logs(ctx, 2, 100); len(other) != 0 {
		t.Fatal("别人看不到")
	}
}

func TestRateLimit(t *testing.T) {
	f := &fakeT{}
	s := New(f, nil)
	now := int64(1_700_000_000_000)
	s.Now = func() int64 { return now }
	ctx := context.Background()
	_, tok, _ := s.Create(ctx, 1, 9, "", d("0"))
	var last Log
	for i := 0; i < 35; i++ {
		now += 100
		last, _ = s.Receive(ctx, tok, []byte(`{"action":"long","symbol":"BTC","size":"1%","id":"`+string(rune('a'+i%26))+string(rune('a'+i/26))+`"}`))
	}
	if last.Code != "SIG_RATE_LIMITED" || f.perps != 30 {
		t.Fatalf("一分钟最多 30 条：%+v %d", last, f.perps)
	}
	if !f.lastNotional.Equal(d("10")) {
		t.Fatalf("没设资金桶就按账户可用算：%s", f.lastNotional)
	}
	_ = s.Delete(ctx, 1, 1)
	if _, err := s.Receive(ctx, tok, []byte("做多 BTC 1%")); err == nil {
		t.Fatal("删掉后地址失效")
	}
}
