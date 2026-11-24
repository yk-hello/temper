package scan

import (
	"context"
	"errors"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"qs/internal/marketmap"
)

type src struct{ days []Day }

func (src) Funding(context.Context) (map[string]float64, error) {
	return map[string]float64{"BTCUSDT": 0.0001, "PEPEUSDT": 0.0015}, nil
}
func (src) OpenInterest(_ context.Context, s string) (float64, float64, float64, error) {
	if s == "PEPEUSDT" {
		return 120, 100, 80, nil
	}
	return 1000, 1000, 1000, nil
}
func (x src) Days(context.Context, string, int) ([]Day, error) { return x.days, nil }
func (src) FearGreed(context.Context) (*FearGreed, error) {
	return &FearGreed{Value: 22, Label: fgLabel(22)}, nil
}
func (src) Global(context.Context) (*Global, error) { return nil, errors.New("down") }
func (src) LongShort(context.Context, string) (*LongShort, error) {
	return &LongShort{Ratio: 1.8, Long: 0.64}, nil
}
func (src) Minutes(context.Context, string, int) ([]sample, error) { return nil, errors.New("none") }

func TestScanAndEvents(t *testing.T) {
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC) // 周二
	px := map[string]float64{"BTC": 60000, "PEPE": 0.01}
	snap := func() marketmap.Snapshot {
		return marketmap.Snapshot{State: "ok", Nodes: []marketmap.Node{
			{S: "BTC", I: "BTC-PERP", Px: px["BTC"], Chg: 1.2, QV: 5e9, F: true},
			{S: "PEPE", I: "PEPE-PERP", Px: px["PEPE"], Chg: -8, QV: 3e8, RV: 3.4},
		}}
	}
	day := int64(86_400_000)
	today := now.Truncate(24 * time.Hour).UnixMilli()
	s := &Service{Map: snap, Src: src{days: []Day{
		{T: today - 2*day, O: 58000, H: 59000, L: 57000, C: 58500}, // 周日
		{T: today - day, O: 58500, H: 61000, L: 58000, C: 60100},   // 周一
		{T: today, O: 60100, H: 60500, L: 59500, C: 60000},         // 周二
	}}, Now: func() time.Time { return now }, Pace: time.Millisecond}
	ctx := context.Background()
	s.started = now.Add(-time.Hour) // 已经跑了一阵（刚启动时的状态不算异动，下面单独测）
	s.Step(ctx)
	time.Sleep(50 * time.Millisecond) // 持仓量是后台取的
	// 5 分钟里 BTC 涨 1.5%（够 1% 的线）、PEPE 涨 0.5%（不够 2%）
	for i := 1; i <= 20; i++ {
		now = now.Add(15 * time.Second)
		px["BTC"] = 60000 * (1 + 0.015*float64(i)/20)
		px["PEPE"] = 0.01 * (1 + 0.005*float64(i)/20)
		s.Step(ctx)
	}
	rows, events, _, _ := s.Screen()
	var btc, pepe Row
	for _, r := range rows {
		if r.S == "BTC" {
			btc = r
		} else {
			pepe = r
		}
	}
	if btc.Chg5m == nil || math.Abs(*btc.Chg5m-1.5) > 0.01 || btc.Chg1h != nil {
		t.Fatalf("BTC 5 分钟 %v，1 小时（样本不够）%v", btc.Chg5m, btc.Chg1h)
	}
	if pepe.Fund == nil || *pepe.Fund != 0.0015 || pepe.OI1h == nil || math.Abs(*pepe.OI1h-0.2) > 1e-9 || math.Abs(*pepe.OI24h-0.5) > 1e-9 {
		t.Fatalf("PEPE 资金费 %v，持仓量 %v %v", pepe.Fund, pepe.OI1h, pepe.OI24h)
	}
	kinds := map[string]int{}
	for _, e := range events {
		kinds[e.S+"|"+e.Kind]++
	}
	// BTC 涨了一次（之后冷却 30 分钟，不重复记）；PEPE 放量、资金费高、持仓量 1 小时 +20%
	if kinds["BTC|up"] != 1 || kinds["PEPE|volume"] != 1 || kinds["PEPE|funding_hi"] != 1 || kinds["PEPE|oi_up"] != 1 || kinds["PEPE|up"] != 0 {
		t.Fatalf("异动：%v", kinds)
	}
	if events[0].T < events[len(events)-1].T {
		t.Fatal("新的在前")
	}

	s.refreshDue(ctx) // 外部数据在后台取（warm.go），"今日"只读现成的
	td := s.Today(ctx)
	if td.Breadth.Up != 1 || td.Breadth.Down != 1 || td.Fear == nil || td.Fear.Label != "extreme_fear" || td.Global != nil || td.LSR == nil {
		t.Fatalf("今日：%+v", td)
	}
	if len(td.Missing) != 1 || td.Missing[0] != "global" {
		t.Fatalf("取不到的要写出来：%v", td.Missing)
	}
	if len(td.Levels) != 1 {
		t.Fatalf("重点币价位：%+v", td.Levels)
	}
	l := td.Levels[0]
	if l.PrevHigh != 61000 || l.PrevLow != 58000 || l.PrevClose != 60100 || l.DayOpen != 60100 || l.WeekOpen != 58500 || l.WeekHigh != 61000 || l.WeekLow != 58000 {
		t.Fatalf("价位：%+v", l)
	}
	if len(td.Movers) != 2 || td.Movers[0].S != "BTC" || len(td.Events) != len(events) {
		t.Fatalf("涨跌榜、异动：%+v %d", td.Movers, len(td.Events))
	}
}

// 刚启动：早就在放量、资金费早就偏高的币不算异动；价格异动照常
func TestNoEventsForStandingStateAtStart(t *testing.T) {
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	s := &Service{Map: func() marketmap.Snapshot {
		return marketmap.Snapshot{State: "ok", Nodes: []marketmap.Node{{S: "PEPE", I: "PEPE-PERP", Px: 0.01, QV: 3e8, RV: 5}}}
	}, Src: src{}, Now: func() time.Time { return now }, Pace: time.Millisecond}
	s.started = now
	s.Step(context.Background())
	if _, ev, _, _ := s.Screen(); len(ev) != 0 {
		t.Fatalf("刚启动不该有异动：%+v", ev)
	}
}

func TestFearGreedLabels(t *testing.T) {
	for v, want := range map[int]string{0: "extreme_fear", 24: "extreme_fear", 25: "fear", 45: "neutral", 55: "neutral", 56: "greed", 76: "extreme_greed"} {
		if fgLabel(v) != want {
			t.Fatalf("%d → %s", v, fgLabel(v))
		}
	}
}

// 打开"今日"不再等外面的接口（2026-11-15）：后台没取过的不算"取不到"、界面显示加载中；
// 取过但失败的才写进 Missing；Today 本身从不去取。
func TestTodayNeverWaits(t *testing.T) {
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	slow := &slowSrc{}
	s := &Service{Map: func() marketmap.Snapshot {
		return marketmap.Snapshot{State: "ok", Nodes: []marketmap.Node{{S: "BTC", I: "BTC-PERP", Px: 60000, Chg: 1, QV: 5e9, F: true}}}
	}, Src: slow, Now: func() time.Time { return now }}
	ctx := context.Background()
	s.Step(ctx)
	t0 := time.Now()
	td := s.Today(ctx)
	if time.Since(t0) > 100*time.Millisecond {
		t.Fatalf("Today 去取外部接口了，用了 %v", time.Since(t0))
	}
	if slow.calls.Load() != 0 {
		t.Fatalf("Today 不该调用外部接口，调用了 %d 次", slow.calls.Load())
	}
	if td.Fear != nil || len(td.Missing) != 0 {
		t.Fatalf("还没取过：不写取不到（界面显示加载中）：%+v", td.Missing)
	}
}

// slowSrc 每个外部接口都要 2 秒，并记下被调用了几次。
type slowSrc struct{ calls atomic.Int32 }

func (x *slowSrc) wait() { x.calls.Add(1); time.Sleep(2 * time.Second) }
func (x *slowSrc) Funding(context.Context) (map[string]float64, error) {
	return map[string]float64{}, nil
}
func (x *slowSrc) OpenInterest(context.Context, string) (float64, float64, float64, error) {
	return 0, 0, 0, errors.New("x")
}
func (x *slowSrc) Days(context.Context, string, int) ([]Day, error) {
	x.wait()
	return nil, errors.New("x")
}
func (x *slowSrc) FearGreed(context.Context) (*FearGreed, error) {
	x.wait()
	return nil, errors.New("x")
}
func (x *slowSrc) Global(context.Context) (*Global, error) { x.wait(); return nil, errors.New("x") }
func (x *slowSrc) Minutes(context.Context, string, int) ([]sample, error) {
	return nil, errors.New("x") // 开机补价格在后台，不算"打开今日要等"
}
func (x *slowSrc) LongShort(context.Context, string) (*LongShort, error) {
	x.wait()
	return nil, errors.New("x")
}

// minSrc 只有 1 分钟 K 线：每分钟涨 0.1 USDT，现在是 106。
type minSrc struct {
	src
	now time.Time
}

func (m minSrc) Minutes(_ context.Context, _ string, n int) ([]sample, error) {
	out := []sample{}
	for i := n; i >= 1; i-- {
		out = append(out, sample{t: m.now.Add(-time.Duration(i) * time.Minute).UnixMilli(), px: 106 - float64(i)*0.1})
	}
	return out, nil
}

// 11-16 走查：开机时补最近 75 分钟的价格，5 分钟、1 小时涨跌开机就有，不用等一小时。
func TestBackfillOnStart(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	s := &Service{Map: func() marketmap.Snapshot {
		return marketmap.Snapshot{State: "ok", Nodes: []marketmap.Node{{S: "BTC", I: "BTC-PERP", Px: 106, QV: 5e9, F: true}}}
	}, Src: minSrc{now: now}, Now: func() time.Time { return now }, Pace: time.Millisecond}
	ctx := context.Background()
	s.Step(ctx)
	for i := 0; i < 200; i++ {
		s.mu.Lock()
		n := len(s.hist["BTC-PERP"])
		s.mu.Unlock()
		if n > 70 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	s.Step(ctx)
	s.mu.Lock()
	r := s.rows[0]
	s.mu.Unlock()
	if r.Chg1h == nil || r.Chg5m == nil {
		t.Fatalf("开机就该有 5 分钟、1 小时涨跌：%+v", r)
	}
	if want := (106/(106-6.0) - 1) * 100; math.Abs(*r.Chg1h-want) > 0.01 {
		t.Fatalf("1 小时涨跌 %.4f，应为 %.4f", *r.Chg1h, want)
	}
}
