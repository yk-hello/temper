package verify

import (
	"math"
	"testing"

	"github.com/shopspring/decimal"

	"qs/internal/portfolio"
)

const day = 24 * hour

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// 一直涨的价格：BTC 从 100 每小时涨 0.1%
func rising(t0 int64, n int) Prices {
	p := Prices{T0: t0}
	v := 100.0
	for i := 0; i < n; i++ {
		p.Closes = append(p.Closes, v)
		v *= 1.001
	}
	return p
}

func TestNumbers(t *testing.T) {
	t0 := int64(1_700_000_000_000) / hour * hour
	px := rising(t0, 24*20)
	since := t0 + 5*day
	now := t0 + 15*day
	perf := portfolio.Performance{StrategyID: "s1", Since: since, Initial: decimal.NewFromInt(10000), NAV: decimal.NewFromInt(11000), Fees: decimal.NewFromInt(100)}
	perf.Return, perf.Trades = 0.1, 3
	perf.Curve = []portfolio.NAVPoint{{T: since, NAV: 10000}, {T: since + 5*day, NAV: 10500}, {T: now, NAV: 11000}}
	perf.Closed = []portfolio.Trade{{Net: decimal.NewFromInt(600)}, {Net: decimal.NewFromInt(300)}, {Net: decimal.NewFromInt(100)}, {Net: decimal.NewFromInt(-50)}}
	sigs := []Signal{
		{ID: "a", At: since + hour, Version: "v1", CostUSD: 2, Done: since + hour + 800},
		{ID: "b", At: since + 2*hour, Version: "v1", CostUSD: 3, Done: since + 2*hour + 1200},
		{ID: "c", At: since + 3*hour, Version: "v2", CostUSD: 5},
	}
	r := Build(Input{Perf: perf, Signals: sigs, Prices: map[string]Prices{"BTC-PERP": px}, Now: now, FeeRate: 0.0005})
	if !near(r.Days, 10) {
		t.Fatalf("前向天数 %v", r.Days)
	}
	if r.FirstHalf == nil || !near(*r.FirstHalf, 0.05) || !near(*r.SecondHalf, 11000.0/10500-1) {
		t.Fatalf("前后半段 %v %v", r.FirstHalf, r.SecondHalf)
	}
	if !near(r.CostPlus50, (11000-50)/10000.0-1) {
		t.Fatalf("成本加 50%% %v", r.CostPlus50)
	}
	if !near(r.InferenceUSD, 10) || !near(r.ReturnAfterInf, (11000-10)/10000.0-1) {
		t.Fatalf("推理成本 %v %v", r.InferenceUSD, r.ReturnAfterInf)
	}
	if r.BestShare == nil || !near(*r.BestShare, 0.6) {
		t.Fatalf("最赚钱一笔占比 %v", r.BestShare)
	}
	if len(r.Versions) != 2 || r.Versions[0].Signals != 2 || r.Versions[1].Version != "v2" {
		t.Fatalf("版本 %+v", r.Versions)
	}
	if r.LatencyP50 == nil || *r.LatencyP50 != 1200 {
		t.Fatalf("延迟 %v", r.LatencyP50)
	}
	// 一直拿着 BTC：10 天 240 小时，每小时 0.1%
	if r.HoldBTC == nil || math.Abs(*r.HoldBTC-(math.Pow(1.001, 239)-1)) > 1e-6 {
		t.Fatalf("一直拿着 %v", r.HoldBTC)
	}
	// 一直涨：动量一直做多，只付一次手续费
	if r.Momentum == nil || *r.Momentum <= 0 || *r.Momentum >= *r.HoldBTC {
		t.Fatalf("动量 %v（应该略低于一直拿着）", r.Momentum)
	}
}

func TestCalibrationAndRandom(t *testing.T) {
	t0 := int64(1_700_000_000_000) / hour * hour
	px := rising(t0, 24*30)
	since, now := t0+2*day, t0+29*day
	var sigs []Signal
	// 信心高的都做多（价格一直涨：对），信心低的都做空（错）
	for i := 0; i < 20; i++ {
		at := since + int64(i)*hour*10
		w, conf := 0.2, 0.8
		if i%2 == 1 {
			w, conf = -0.2, 0.3
		}
		sigs = append(sigs, Signal{ID: "x", At: at, Targets: []Target{{Inst: "BTC-PERP", Weight: w, Conf: conf, HorizonMin: 120, RefPx: px.At(at)}}})
	}
	perf := portfolio.Performance{StrategyID: "s2", Since: since, Initial: decimal.NewFromInt(10000), NAV: decimal.NewFromInt(10000)}
	// 10 笔做多，每笔拿 24 小时：一直涨的行情里应该跑赢大多数随机方向
	for i := 0; i < 10; i++ {
		o := since + int64(i)*2*day
		perf.Closed = append(perf.Closed, portfolio.Trade{Instrument: "BTC-PERP", Side: "long", OpenTime: o, CloseTime: o + day, Entry: decimal.NewFromFloat(px.At(o)), Exit: decimal.NewFromFloat(px.At(o + day))})
	}
	r := Build(Input{Perf: perf, Signals: sigs, Prices: map[string]Prices{"BTC-PERP": px}, Now: now, FeeRate: 0.0005, Seed: 1})
	var lo, hi Bucket
	for _, b := range r.Calibration {
		switch b.Label {
		case "lo":
			lo = b
		case "hi":
			hi = b
		}
	}
	// 第一条是从 0 到 0.2（变多）；之后 0.2 → -0.2（变少）、-0.2 → 0.2（变多）交替
	if hi.N != 10 || lo.N != 10 || hi.Avg <= 0 || lo.Avg >= 0 || hi.Hit != 1 || lo.Hit != 0 {
		t.Fatalf("信心校准 lo=%+v hi=%+v", lo, hi)
	}
	if r.Random == nil || r.Random.Beat < 0.9 || r.Random.StratMean <= r.Random.Median {
		t.Fatalf("随机对照 %+v", r.Random)
	}
	// 同样的数据再算一次：结果一样（种子固定）
	r2 := Build(Input{Perf: perf, Signals: sigs, Prices: map[string]Prices{"BTC-PERP": px}, Now: now, FeeRate: 0.0005, Seed: 1})
	if r2.Random.Median != r.Random.Median {
		t.Fatal("随机对照每次不一样")
	}
}

func TestParseSignal(t *testing.T) {
	raw := []byte(`{"strategy_version":"v3","targets":[{"instrument":"ETH-PERP","weight":"-0.25","confidence":0.7,"horizon_min":60,"ref_price":"3500.5"}],"meta":{"cost_usd":0.012}}`)
	s, ok := ParseSignal("m1", 5, raw)
	if !ok || s.Version != "v3" || s.CostUSD != 0.012 || len(s.Targets) != 1 || s.Targets[0].Weight != -0.25 || s.Targets[0].RefPx != 3500.5 || s.Targets[0].HorizonMin != 60 {
		t.Fatalf("%+v", s)
	}
	if _, ok := ParseSignal("m2", 5, []byte(`{"type":"heartbeat"}`)); ok {
		t.Fatal("没有目标的不算信号")
	}
}
