package market

import "testing"

// 积压估计：扣掉固定的时钟偏差，只反映超出最小值的部分；每秒最多报告一次。
func TestLagMeter(t *testing.T) {
	m := &lagMeter{}
	const skew = 7_000 // 本机时钟比交易所快 7 秒（Windows 上的 Docker 常见）
	now := int64(1_000_000)
	for i := 0; i < 50; i++ { // 正常：每条都在 50 毫秒内到
		now += 20
		m.observe(now, now-skew-50)
	}
	now += 1000
	v, ok := m.observe(now, now-skew-50)
	if !ok || v > 100 {
		t.Fatalf("正常时积压应接近 0：%d %v", v, ok)
	}
	now += 1000
	v, ok = m.observe(now, now-skew-12_050) // 积压 12 秒
	if !ok || v < 11_000 || v > 13_000 {
		t.Fatalf("积压 12 秒应报约 12000：%d", v)
	}
	if _, ok := m.observe(now+10, now-skew); ok {
		t.Fatal("一秒内不应重复报告")
	}
}

func TestBinancePublicOnlyDepth(t *testing.T) {
	u := (&Binance{Symbols: []string{"BTCUSDT"}}).URLs()["public"]
	if u != "wss://fstream.binance.com/public/stream?streams=btcusdt@depth20@100ms" {
		t.Fatalf("public 线路只订深度：%s", u)
	}
}
