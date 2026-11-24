package scan

import (
	"context"
	"os"
	"testing"
)

// 真接口（要联网）：QS_REAL=1 go test ./internal/scan -run Real -v
func TestPublicReal(t *testing.T) {
	if os.Getenv("QS_REAL") != "1" {
		t.Skip("QS_REAL=1 才跑")
	}
	p := &Public{}
	ctx := context.Background()
	f, err := p.Funding(ctx)
	t.Logf("资金费：%d 个，BTC %v，%v", len(f), f["BTCUSDT"], err)
	n, h1, h24, err := p.OpenInterest(ctx, "BTCUSDT")
	t.Logf("持仓量：%.0f，1 小时前 %.0f，24 小时前 %.0f，%v", n, h1, h24, err)
	d, err := p.Days(ctx, "BTCUSDT", 8)
	t.Logf("日线 %d 根，%v", len(d), err)
	fg, err := p.FearGreed(ctx)
	t.Logf("恐惧贪婪：%+v %v", fg, err)
	g, err := p.Global(ctx)
	t.Logf("市值：%+v %v", g, err)
	l, err := p.LongShort(ctx, "BTCUSDT")
	t.Logf("多空比：%+v %v", l, err)
}
