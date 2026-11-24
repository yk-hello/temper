package onchain

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// V.18 手算样例：BTC 开仓价 100，四笔
func TestPnLDistByHand(t *testing.T) {
	s := &Scanner{pos: map[string][]Pos{
		"a": {{Coin: "BTC", Size: 10, Entry: 100, Value: 1250, UPnL: 250}},   // 多，+25%，浮盈
		"b": {{Coin: "BTC", Size: 5, Entry: 100, Value: 470, UPnL: -30}},     // 多，-6%，浮亏
		"c": {{Coin: "BTC", Size: -10, Entry: 100, Value: 1100, UPnL: -100}}, // 空，-10%，正好落在 [-10,-5)
		"d": {{Coin: "BTC", Size: -2, Entry: 100, Value: 180, UPnL: 20}},     // 空，+10%，落在 [10,20)
		"e": {{Coin: "ETH", Size: 1, Entry: 100, Value: 100, UPnL: 50}},      // 别的币，不算
	}}
	d := s.PnLDistOf("BTC")
	if len(d.Buckets) != 8 {
		t.Fatalf("应 8 组：%d", len(d.Buckets))
	}
	want := map[float64][4]float64{ // 每组下沿 → 多金额、多笔数、空金额、空笔数
		20:  {1250, 1, 0, 0},
		-10: {470, 1, 1100, 1},
		10:  {0, 0, 180, 1},
	}
	for _, b := range d.Buckets {
		w := want[b.Lo]
		if b.LongValue != w[0] || float64(b.LongN) != w[1] || b.ShortValue != w[2] || float64(b.ShortN) != w[3] {
			t.Fatalf("[%v,%v)：%+v，应是 %v", b.Lo, b.Hi, b, w)
		}
	}
	if d.LongUp != 1250 || d.LongDn != 470 || d.ShortUp != 180 || d.ShortDn != 1100 {
		t.Fatalf("合计：%+v", d)
	}
	if _, err := json.Marshal(d); err != nil {
		t.Fatalf("两头不能是无穷大：%v", err)
	}
}

// V.15 覆盖率：多单 30 + 空单 10 = 40，除以 2 是 20，总持仓 50，覆盖 40%
func TestCoverage(t *testing.T) {
	s := &Scanner{oi: map[string]float64{"BTC": 50}, pos: map[string][]Pos{
		"a": {{Coin: "BTC", Size: 30}},
		"b": {{Coin: "BTC", Size: -10}, {Coin: "ETH", Size: 99}},
	}}
	m := s.LiqMap("BTC", 100, 0.2, 40)
	if m.Coverage != 0.4 || m.OI != 50 {
		t.Fatalf("覆盖率：%v %v", m.Coverage, m.OI)
	}
	if m := s.LiqMap("DOGE", 100, 0.2, 40); m.Coverage != 0 {
		t.Fatalf("不知道总量时写 0：%v", m.Coverage)
	}
}

// 不知道总持仓时不要瞎算，覆盖率超过 100% 时封顶
func TestCoverageCap(t *testing.T) {
	s := &Scanner{oi: map[string]float64{"BTC": 1}, pos: map[string][]Pos{"a": {{Coin: "BTC", Size: 30}}}}
	if c, _ := s.coverageLocked("BTC"); c != 1 {
		t.Fatalf("封顶：%v", c)
	}
}

// V.20 存盘再恢复：地址、持仓、动作都在，重启后马上有数据
func TestSnapshotRoundTrip(t *testing.T) {
	a := &Scanner{}
	a.defaults()
	a.accounts["x"] = Account{Address: "x", Value: 5}
	a.update("x", []Pos{{Coin: "BTC", Size: 1, Entry: 100, Value: 100}})
	a.update("x", []Pos{{Coin: "BTC", Size: 2, Entry: 100, Value: 200}})
	a.cycle, a.cycleEnd = 3, 12345
	a.oi = map[string]float64{"BTC": 9}
	b, err := a.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	c := &Scanner{}
	c.defaults()
	c.accounts["y"] = Account{Address: "y"}
	now := time.Now().UnixMilli()
	c.scanned["x"] = now + 1000 // 这个地址已经有更新的结果了
	c.pos["x"] = []Pos{{Coin: "BTC", Size: 7, Value: 700}}
	if err := c.Restore(b); err != nil {
		t.Fatal(err)
	}
	if c.cycle != 3 || c.cycleEnd != 12345 || c.oi["BTC"] != 9 || len(c.events) != 1 {
		t.Fatalf("恢复：%d %d %v %d", c.cycle, c.cycleEnd, c.oi, len(c.events))
	}
	if c.pos["x"][0].Size != 7 {
		t.Fatal("已经有更新的结果，不该被旧的覆盖")
	}
	if _, ok := c.accounts["y"]; !ok {
		t.Fatal("新地址不该丢")
	}
	// 没有更新结果时用存盘的
	d := &Scanner{}
	if err := d.Restore(b); err != nil {
		t.Fatal(err)
	}
	if d.pos["x"][0].Size != 2 || d.LiqMap("BTC", 100, 0.2, 40).Cycle != 3 {
		t.Fatalf("恢复持仓：%+v", d.pos["x"])
	}
}

// V.17 动作：五种都能识别，带数据延迟（两次扫描的间隔）；停了很久再扫，不报动作
func TestEvents(t *testing.T) {
	s := &Scanner{}
	s.defaults()
	s.update("x", []Pos{{Coin: "BTC", Size: 10, Entry: 100, Value: 1000}})
	s.mu.Lock()
	s.scanned["x"] -= 30_000 // 假装上次是 30 秒前
	s.mu.Unlock()
	s.update("x", []Pos{{Coin: "BTC", Size: 20, Entry: 100, Value: 2000}, {Coin: "ETH", Size: 1, Entry: 10, Value: 10}})
	s.update("x", []Pos{{Coin: "BTC", Size: 5, Entry: 100, Value: 500}, {Coin: "ETH", Size: -1, Entry: 10, Value: 10}})
	s.update("x", []Pos{{Coin: "ETH", Size: -1, Entry: 10, Value: 10}})
	kinds := map[string]Event{}
	for _, e := range s.events {
		kinds[e.Coin+":"+e.Kind] = e
	}
	for _, k := range []string{"BTC:add", "ETH:open", "BTC:reduce", "ETH:flip", "BTC:close"} {
		if _, ok := kinds[k]; !ok {
			t.Fatalf("应有 %s：%+v", k, s.events)
		}
	}
	if l := kinds["BTC:add"].Lag; l < 29_000 || l > 31_000 {
		t.Fatalf("延迟应约 30 秒：%d", l)
	}
	n := len(s.events)
	s.mu.Lock()
	s.scanned["x"] -= maxEventGap + 1000
	s.mu.Unlock()
	s.update("x", []Pos{{Coin: "BTC", Size: 99, Entry: 100, Value: 9900}})
	if len(s.events) != n {
		t.Fatal("停了太久不知道什么时候发生的，不该报")
	}
}

// V.17 调度：持仓最大的一批单独一队
func TestQueues(t *testing.T) {
	s := &Scanner{PrioN: 2}
	s.defaults()
	for _, a := range []string{"a", "b", "c", "d"} {
		s.accounts[a] = Account{Address: a}
	}
	s.pos["a"] = []Pos{{Value: 5}}
	s.pos["b"] = []Pos{{Value: 50}}
	s.pos["c"] = []Pos{{Value: 500}, {Value: 1}}
	prio, rest := s.queues()
	if len(prio) != 2 || prio[0] != "c" || prio[1] != "b" {
		t.Fatalf("优先队：%v", prio)
	}
	if len(rest) != 2 || rest[0] != "a" || rest[1] != "d" {
		t.Fatalf("其余：%v", rest)
	}
}

// 取总持仓量：假服务器
func TestLoadOI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"universe":[{"name":"BTC"},{"name":"ETH"}]},[{"openInterest":"123.5"},{"openInterest":"7"}]]`))
	}))
	defer srv.Close()
	s := &Scanner{InfoURL: srv.URL}
	s.defaults()
	if err := s.loadOI(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.oi["BTC"] != 123.5 || s.oi["ETH"] != 7 {
		t.Fatalf("总持仓：%v", s.oi)
	}
}

func TestDropLeft(t *testing.T) {
	s := &Scanner{accounts: map[string]Account{"a": {}}, pos: map[string][]Pos{"a": {{Coin: "BTC", Size: 1}}, "b": {{Coin: "BTC", Size: 9}}}, scanned: map[string]int64{"a": 1, "b": 1}}
	s.dropLeft()
	if len(s.pos) != 1 || s.pos["a"] == nil || len(s.scanned) != 1 {
		t.Fatalf("跌出排行榜的要删：%v %v", s.pos, s.scanned)
	}
}

// 11-17：大户榜 40MB，网慢时下不完。手里已经有名单（上次存盘恢复的、或者以前下好的）就接着扫，
// 原来刷新失败会卡在重试里，所有地址都不扫了，大户提醒一条都发不出来
func TestLeaderboardFailKeepsScanning(t *testing.T) {
	var lb, scans atomic.Int32
	board := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lb.Add(1)
		http.Error(w, "slow", http.StatusGatewayTimeout)
	}))
	defer board.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Type string `json:"type"`
		}
		_ = json.NewDecoder(r.Body).Decode(&q)
		if q.Type == "clearinghouseState" {
			scans.Add(1)
			_, _ = w.Write([]byte(`{"assetPositions":[]}`))
			return
		}
		_, _ = w.Write([]byte(`[{"universe":[]},[]]`))
	}))
	defer api.Close()
	s := &Scanner{LeaderboardURL: board.URL, InfoURL: api.URL, PerSecond: 50, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s.defaults()
	s.accounts["0xa"] = Account{Address: "0xa"}
	s.accounts["0xb"] = Account{Address: "0xb"}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s.Run(ctx)
	if lb.Load() == 0 {
		t.Fatal("没去刷新榜单")
	}
	if lb.Load() > 1 {
		t.Fatalf("刷新失败后应该半小时后再试，2 秒里试了 %d 次", lb.Load())
	}
	if scans.Load() < 4 {
		t.Fatalf("榜单刷新失败后扫描停了：只查了 %d 次", scans.Load())
	}
}

// 11-18：名单下载很慢（网慢时要四五分钟）：手里有旧名单就放后台下载，扫描不等它
func TestLeaderboardSlowDoesNotBlock(t *testing.T) {
	var scans atomic.Int32
	release := make(chan struct{})
	board := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select { // 一直卡着，直到测试结束
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer board.Close()
	defer close(release)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Type string `json:"type"`
		}
		_ = json.NewDecoder(r.Body).Decode(&q)
		if q.Type == "clearinghouseState" {
			scans.Add(1)
			_, _ = w.Write([]byte(`{"assetPositions":[]}`))
			return
		}
		_, _ = w.Write([]byte(`[{"universe":[]},[]]`))
	}))
	defer api.Close()
	s := &Scanner{LeaderboardURL: board.URL, InfoURL: api.URL, PerSecond: 50, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s.defaults()
	s.accounts["0xa"] = Account{Address: "0xa"}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	s.Run(ctx)
	if scans.Load() < 4 {
		t.Fatalf("名单还在下载时扫描停了：只查了 %d 次", scans.Load())
	}
}
