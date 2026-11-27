package heat

import (
	"context"
	"encoding/base64"
	"fmt"
	"math"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func snap() Snapshot {
	return Snapshot{LastID: 100, Bids: [][2]float64{{100, 1}, {99, 2}}, Asks: [][2]float64{{101, 1}, {102, 3}}}
}

// V.5 币安的拼接规则
func TestLocalBookSequence(t *testing.T) {
	b := NewLocalBook()
	if err := b.Apply(Diff{U: 90, Last: 95}); err != ErrGap {
		t.Fatalf("没有快照时应要求取快照：%v", err)
	}
	b.Reset(snap())
	if b.Ready() {
		t.Fatal("还没接上增量，不算好")
	}
	// 快照之前的丢掉，盘口不变
	if err := b.Apply(Diff{U: 90, Last: 99, PU: 89, Bids: [][2]float64{{100, 9}}}); err != nil || b.Bids[100] != 1 {
		t.Fatalf("快照之前的应丢掉：%v %v", err, b.Bids)
	}
	// 第一条必须把快照序号包住：U=105 > 100，中间缺了
	if err := b.Apply(Diff{U: 105, Last: 110, PU: 104}); err != ErrGap {
		t.Fatalf("第一条接不上应报断号：%v", err)
	}
	// U=98 ≤ 100 ≤ u=102：接上
	if err := b.Apply(Diff{U: 98, Last: 102, PU: 97, Bids: [][2]float64{{100, 5}}, Asks: [][2]float64{{101, 0}}}); err != nil {
		t.Fatal(err)
	}
	if !b.Ready() || b.Bids[100] != 5 {
		t.Fatalf("应更新：%v", b.Bids)
	}
	if _, ok := b.Asks[101]; ok {
		t.Fatal("数量 0 表示这一档没了")
	}
	// 之后 pu 必须等于上一条的 u
	if err := b.Apply(Diff{U: 103, Last: 105, PU: 102, Asks: [][2]float64{{103, 1}}}); err != nil {
		t.Fatal(err)
	}
	if err := b.Apply(Diff{U: 107, Last: 108, PU: 106}); err != ErrGap {
		t.Fatalf("序号断了应报错：%v", err)
	}
	// 重新取快照后又能接上
	b.Reset(Snapshot{LastID: 200, Bids: [][2]float64{{90, 1}}, Asks: [][2]float64{{91, 1}}})
	if err := b.Apply(Diff{U: 199, Last: 201, PU: 198}); err != nil {
		t.Fatal(err)
	}
}

// 买一比卖一高说明脏了：修掉
func TestLocalBookCrossFixed(t *testing.T) {
	b := NewLocalBook()
	b.Reset(snap())
	_ = b.Apply(Diff{U: 99, Last: 101, Asks: [][2]float64{{99.5, 1}}})
	bid, ask := b.Best()
	if bid >= ask {
		t.Fatalf("不应交叉：%v %v", bid, ask)
	}
}

// 手算分桶：桶宽 2，金额 = 价格 × 数量
func TestBucketByHand(t *testing.T) {
	b := NewLocalBook()
	b.Reset(Snapshot{LastID: 1, Bids: [][2]float64{{100, 1}, {100.5, 2}, {50, 9}}, Asks: [][2]float64{{103, 1}}})
	c := b.Bucket(2, 101, 0.05)
	// 100 和 100.5 都落在桶 50：100×1 + 100.5×2 = 301；103 落在桶 52
	if math.Abs(c[50]-301) > 1e-9 || math.Abs(c[52]-103) > 1e-9 {
		t.Fatalf("分桶：%v", c)
	}
	if _, ok := c[25]; ok {
		t.Fatal("离现价太远的不要")
	}
	if niceWidth(76000, 0.1) != 2 || niceWidth(0.2, 0.00001) != 0.00001 || niceWidth(3000, 0.01) != 0.05 {
		t.Fatalf("桶宽：%v %v %v", niceWidth(76000, 0.1), niceWidth(0.2, 0.00001), niceWidth(3000, 0.01))
	}
}

// 字节和金额互换：误差在一格以内
func TestCellRoundTrip(t *testing.T) {
	for _, v := range []float64{150, 1000, 12345, 1e6, 3e7} {
		got := FromCell(ToCell(v))
		if got < v*0.95 || got > v*1.05 {
			t.Fatalf("%v → %v", v, got)
		}
	}
	if ToCell(50) != 0 || FromCell(0) != 0 || ToCell(1e12) != 255 {
		t.Fatal("边界")
	}
}

// V.6 一分钟压缩后还原：挂单、中间价、成交都对
func TestChunkRoundTrip(t *testing.T) {
	cols := make([]Column, 60)
	mids := make([]float64, 60)
	for i := range cols {
		if i == 30 {
			continue // 这一秒没数据
		}
		cols[i] = Column{1000: 5000, 1005: 1e6}
		mids[i] = 2000 + float64(i)
	}
	tr := []Trade{{T: 60_000 + 1500, P: 2000.5, Q: 1.5, Buy: true}, {T: 60_000 + 59_000, P: 2010, Q: 0.25}}
	c := BuildChunk(60_000, 2, cols, mids, tr)
	var got Chunk
	got.T, got.Width, got.Base, got.Rows, got.Cols = c.T, c.Width, c.Base, c.Rows, c.Cols
	if err := DecodeChunk(&got, c.EncodeCells(), c.EncodeMids(), c.EncodeTrades()); err != nil {
		t.Fatal(err)
	}
	if got.Rows != 6 || got.Base != 1000 || got.Cells[0] != ToCell(5000) || got.Cells[5*60+1] != ToCell(1e6) || got.Cells[30] != 0 {
		t.Fatalf("矩阵不对：%d %d", got.Rows, got.Base)
	}
	if got.Mids[1] != 2001 || got.Mids[30] != 0 {
		t.Fatalf("中间价：%v", got.Mids[:2])
	}
	if len(got.Trades) != 2 || got.Trades[0].T != 61_500 || got.Trades[0].P != 2000.5 || !got.Trades[0].Buy || got.Trades[1].Buy {
		t.Fatalf("成交：%+v", got.Trades)
	}
}

// ---------- V.8 自动识别：每种都用构造的数据测 ----------

// 背景盘口：现价 100000 上下每桶 1 万美元（桶宽 10）
func baseCol(mid float64) Column {
	c := Column{}
	for p := mid - 150; p <= mid+150; p += 10 {
		c[bucketOf(p, 10)] = 10_000
	}
	return c
}

func kinds(evs []Event) map[string]Event {
	m := map[string]Event{}
	for _, e := range evs {
		m[e.Kind] = e
	}
	return m
}

// 疑似撤单：100100 这一档挂了 200 万，3 秒后消失，没有成交
func TestDetectSpoof(t *testing.T) {
	d := NewDetector(10)
	mid := 100000.0
	wall := bucketOf(100100, 10)
	var last []Event
	for i := 0; i < 8; i++ {
		c := baseCol(mid)
		c[wall] = 2_000_000
		last = d.Step(int64(i)*1000, mid, c, nil)
		if len(last) != 0 {
			t.Fatalf("还在的大单不该报：%+v", last)
		}
	}
	c := baseCol(mid)
	c[wall] = 100_000
	ev := kinds(d.Step(8000, mid, c, nil))
	e, ok := ev[KindSpoof]
	if !ok {
		t.Fatalf("应报撤单：%+v", ev)
	}
	if e.P != 100100 || e.Bid || math.Abs(e.USD-1_900_000) > 1 || e.Evidence["filled"] != 0 {
		t.Fatalf("内容不对：%+v", e)
	}
}

// 大单是被成交掉的：不算撤单
func TestDetectFilledIsNotSpoof(t *testing.T) {
	d := NewDetector(10)
	mid := 100000.0
	wall := bucketOf(100100, 10)
	for i := 0; i < 8; i++ {
		c := baseCol(mid)
		c[wall] = 2_000_000
		d.Step(int64(i)*1000, mid, c, nil)
	}
	c := baseCol(mid)
	c[wall] = 100_000
	ev := kinds(d.Step(8000, mid, c, []Trade{{T: 8000, P: 100100, Q: 19, Buy: true}}))
	if _, bad := ev[KindSpoof]; bad {
		t.Fatalf("被成交掉的不算撤单：%+v", ev)
	}
}

// 大单挪到了旁边一档（做市商换价）：不算撤单。挂得太短、离现价太远也不算
func TestDetectNotSpoof(t *testing.T) {
	mid := 100000.0
	wall := bucketOf(100100, 10)
	run := func(secs int, wallAt int64, after func(Column)) map[string]Event {
		d := NewDetector(10)
		for i := 0; i < secs; i++ {
			c := baseCol(mid)
			c[wallAt] = 2_000_000
			d.Step(int64(i)*1000, mid, c, nil)
		}
		c := baseCol(mid)
		after(c)
		return kinds(d.Step(int64(secs)*1000, mid, c, nil))
	}
	if _, bad := run(8, wall, func(c Column) { c[wall+2] = 2_000_000 })[KindSpoof]; bad {
		t.Fatal("挪到旁边不算撤单")
	}
	if _, bad := run(3, wall, func(Column) {})[KindSpoof]; bad {
		t.Fatal("只挂了 3 秒不算")
	}
	far := bucketOf(100500, 10)
	if _, bad := run(8, far, func(Column) {})[KindSpoof]; bad {
		t.Fatal("离现价 0.5% 不算")
	}
	if _, ok := run(8, wall, func(Column) {})[KindSpoof]; !ok {
		t.Fatal("对照：这种应该报")
	}
}

// 价格穿过去了，是被吃掉的，不算撤单
func TestDetectCrossedIsNotSpoof(t *testing.T) {
	d := NewDetector(10)
	wall := bucketOf(100050, 10)
	for i := 0; i < 8; i++ {
		c := baseCol(100000)
		c[wall] = 2_000_000
		d.Step(int64(i)*1000, 100000, c, nil)
	}
	if _, bad := kinds(d.Step(8000, 100080, baseCol(100080), nil))[KindSpoof]; bad {
		t.Fatal("穿过去了不算撤单")
	}
}

// 现在挂着的大单：手算。附近每档 1 万（中位数 1 万），门槛 max(10 万, 5 万) = 10 万
func TestWallsByHand(t *testing.T) {
	d := NewDetector(10)
	mid := 100000.0
	upper := bucketOf(100100, 10)
	lower := bucketOf(99900, 10)
	fresh := bucketOf(100050, 10)
	for i := 0; i < 40; i++ {
		c := baseCol(mid)
		c[upper] = 1_000_000
		c[upper+1] = 300_000 // 紧挨着的，算同一个
		c[lower] = 500_000
		if i >= 25 {
			c[fresh] = 900_000 // 才挂 15 秒，不算
		}
		// 第 10 秒价格碰到下面那个大单一次
		m := mid
		if i == 10 {
			m = 99920
		}
		d.Step(int64(i)*1000, m, c, nil)
	}
	w := d.Walls(mid)
	if len(w) != 2 {
		t.Fatalf("应有 2 个大单：%+v", w)
	}
	up, dn := w[0], w[1]
	if up.P != 100100 || up.USD != 1_300_000 || up.Bid || up.Age != 39 || up.Dist != 0.1 {
		t.Fatalf("上方：%+v", up)
	}
	if dn.P != 99900 || dn.USD != 500_000 || !dn.Bid || dn.Touch != 1 || dn.Dist != -0.1 {
		t.Fatalf("下方：%+v", dn)
	}
}

// 每边最多 5 个，按离现价从近到远
func TestWallsLimit(t *testing.T) {
	d := NewDetector(10)
	mid := 100000.0
	for i := 0; i < 35; i++ {
		c := baseCol(mid)
		for j := int64(1); j <= 7; j++ {
			c[bucketOf(mid+float64(j)*100, 10)] = 500_000
		}
		d.Step(int64(i)*1000, mid, c, nil)
	}
	w := d.Walls(mid)
	if len(w) != 5 || w[0].P != 100100 || w[4].P != 100500 {
		t.Fatalf("上方 5 个：%+v", w)
	}
}

// 主动买卖额：最近 5 分钟的合计
func TestFlow(t *testing.T) {
	d := NewDetector(10)
	mid := 100000.0
	for i := 0; i < flowSec+10; i++ {
		var tr []Trade
		if i >= 10 {
			tr = []Trade{{T: int64(i) * 1000, P: mid, Q: 1, Buy: i%3 == 0}}
		} else {
			tr = []Trade{{T: int64(i) * 1000, P: mid, Q: 1000, Buy: true}} // 这些已经超过 5 分钟，不算
		}
		d.Step(int64(i)*1000, mid, baseCol(mid), tr)
	}
	f := d.Flow()
	if f[0]+f[1] != float64(flowSec)*mid {
		t.Fatalf("合计：%v", f)
	}
}

// 薄的币上撤掉一个不算大的单：占这一侧不到 15%，不报
func TestDetectSpoofShare(t *testing.T) {
	d := NewDetector(10)
	mid := 100000.0
	wall := bucketOf(100100, 10)
	for i := 0; i < 8; i++ {
		c := baseCol(mid)
		for k := range c {
			c[k] = 2_000_000
		}
		c[wall] = 2_500_000
		d.Step(int64(i)*1000, mid, c, nil)
	}
	c := baseCol(mid)
	for k := range c {
		c[k] = 2_000_000
	}
	c[wall] = 500_000
	if _, bad := kinds(d.Step(8000, mid, c, nil))[KindSpoof]; bad {
		t.Fatal("占比太小不算")
	}
}

// 同一个币 10 秒内只报最大的一个撤单
func TestDetectSpoofRateLimit(t *testing.T) {
	d := NewDetector(10)
	mid := 100000.0
	a, b := bucketOf(100100, 10), bucketOf(99900, 10)
	for i := 0; i < 8; i++ {
		c := baseCol(mid)
		c[a], c[b] = 2_000_000, 3_000_000
		d.Step(int64(i)*1000, mid, c, nil)
	}
	evs := d.Step(8000, mid, baseCol(mid), nil)
	if len(evs) != 1 || evs[0].P != 99900 {
		t.Fatalf("应只报最大的：%+v", evs)
	}
}

// 疑似冰山：显示的挂单只有 50 万，10 秒成交了 200 万，挂单还在，价格没穿过
func TestDetectIceberg(t *testing.T) {
	d := NewDetector(10)
	mid := 100000.0
	wall := bucketOf(99950, 10)
	var ev map[string]Event
	for i := 0; i < 12; i++ {
		c := baseCol(mid)
		c[wall] = 500_000
		evs := d.Step(int64(i)*1000, mid, c, []Trade{{T: int64(i) * 1000, P: 99950, Q: 2, Buy: false}})
		if m := kinds(evs); m[KindIceberg].Kind != "" {
			ev = m
			break
		}
	}
	e, ok := ev[KindIceberg]
	if !ok {
		t.Fatal("应报冰山")
	}
	if e.P != 99950 || !e.Bid || e.Evidence["mult"] < 1.5 {
		t.Fatalf("内容不对：%+v", e)
	}
}

// 疑似吸收：挂单够厚，成交很多但没超过挂单，价格没穿过
func TestDetectAbsorb(t *testing.T) {
	d := NewDetector(10)
	mid := 100000.0
	wall := bucketOf(99950, 10)
	found := false
	for i := 0; i < 40; i++ {
		c := baseCol(mid)
		c[wall] = 3_000_000
		var tr []Trade
		if i >= 25 {
			tr = []Trade{{T: int64(i) * 1000, P: 99950, Q: 1, Buy: false}}
		}
		if _, ok := kinds(d.Step(int64(i)*1000, mid, c, tr))[KindAbsorb]; ok {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("应报吸收")
	}
}

// 价格穿过去了就不算吸收和冰山
func TestDetectCrossedIsNotAbsorb(t *testing.T) {
	d := NewDetector(10)
	wall := bucketOf(99950, 10)
	for i := 0; i < 15; i++ {
		mid := 100000.0
		if i > 8 {
			mid = 99900
		}
		c := baseCol(mid)
		c[wall] = 500_000
		evs := kinds(d.Step(int64(i)*1000, mid, c, []Trade{{T: int64(i) * 1000, P: 99950, Q: 5, Buy: false}}))
		if _, bad := evs[KindAbsorb]; bad {
			t.Fatal("穿过去了不算吸收")
		}
		if _, bad := evs[KindIceberg]; bad {
			t.Fatal("穿过去了不算冰山")
		}
	}
}

// 疑似扫单：平时每秒 1 万，突然 3 秒内主动卖 500 万，价格跌 0.3%
func TestDetectSweep(t *testing.T) {
	d := NewDetector(10)
	mid := 100000.0
	var e Event
	for i := 0; i < 30; i++ {
		var tr []Trade
		if i < 27 {
			tr = []Trade{{T: int64(i) * 1000, P: mid, Q: 0.1, Buy: i%2 == 0}}
		} else {
			mid -= 100
			tr = []Trade{{T: int64(i) * 1000, P: mid, Q: 17, Buy: false}}
		}
		if x, ok := kinds(d.Step(int64(i)*1000, mid, baseCol(mid), tr))[KindSweep]; ok {
			e = x
		}
	}
	if e.Kind != KindSweep || !e.Bid || e.Evidence["oneSide"] < 85 || e.Evidence["move"] < 0.15 {
		t.Fatalf("应报向下扫单：%+v", e)
	}
}

// 大单：平时每笔 1 万美元，来一笔 50 万美元
func TestDetectLarge(t *testing.T) {
	d := NewDetector(10)
	mid := 100000.0
	for i := 0; i < 30; i++ {
		d.Step(int64(i)*1000, mid, baseCol(mid), []Trade{{T: int64(i) * 1000, P: mid, Q: 0.1, Buy: true}})
	}
	if _, bad := kinds(d.Step(30500, mid, baseCol(mid), []Trade{{T: 30500, P: mid, Q: 5, Buy: true}}))[KindLarge]; bad {
		t.Fatal("50 倍不算大单")
	}
	e, ok := kinds(d.Step(31000, mid, baseCol(mid), []Trade{{T: 31000, P: mid, Q: 15, Buy: true}}))[KindLarge]
	if !ok || e.USD != 1_500_000 || e.Bid || e.Evidence["mult"] != 150 {
		t.Fatalf("应报大单：%+v", e)
	}
}

// 同一个价位 30 秒内不重复报
func TestDetectCooldown(t *testing.T) {
	d := NewDetector(10)
	if !d.ok(KindSpoof, 1, 1000) || d.ok(KindSpoof, 1, 20_000) || !d.ok(KindSpoof, 1, 31_001) || !d.ok(KindSpoof, 2, 20_000) {
		t.Fatal("冷却不对")
	}
}

// ---------- 录制：乱序、断号、快照之前的增量 ----------

// fakeFeed 假的盘口数据源。Snapshot 是 Recorder 另开线程调的（resync、补快照），测试这边同时在读次数、换快照，
// 所以都加锁（11-28：go test -race 抓到 n 一边在后台 ++、一边被测试轮询）
type fakeFeed struct {
	mu   sync.Mutex
	snap Snapshot
	n    int
}

func (f *fakeFeed) Run(ctx context.Context, connected func(), diff func(string, Diff)) {}
func (f *fakeFeed) Snapshot(ctx context.Context, inst string) (Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	return f.snap, nil
}

// calls 取了几次快照
func (f *fakeFeed) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n
}

func (f *fakeFeed) setSnap(s Snapshot) {
	f.mu.Lock()
	f.snap = s
	f.mu.Unlock()
}

// 先攒增量，再拼快照；中间断号时自动重取
func TestRecorderResync(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &fakeFeed{snap: Snapshot{LastID: 100, Bids: [][2]float64{{100, 1}}, Asks: [][2]float64{{101, 1}}}}
	r := New(f, []string{"BTC-PERP"}, func(string) float64 { return 0.1 }, nil)
	s := r.syms["BTC-PERP"]
	s.mu.Lock()
	s.buf = nil
	s.mu.Unlock()
	r.resync(ctx, s)
	// 正在取快照时来的增量先攒着
	r.onDiff(ctx, s, Diff{U: 95, Last: 99, PU: 94, Bids: [][2]float64{{100, 9}}}) // 快照之前，要丢
	r.onDiff(ctx, s, Diff{U: 99, Last: 102, PU: 98, Bids: [][2]float64{{100, 2}}})
	waitFor(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return !s.sync })
	s.mu.Lock()
	if s.book.Bids[100] != 2 || !s.book.Ready() || s.cover != [2]float64{100, 101} {
		t.Fatalf("拼接不对：%v %v", s.book.Bids, s.cover)
	}
	s.mu.Unlock()
	if n := f.calls(); n != 1 {
		t.Fatalf("应只取一次快照：%d", n)
	}
	// 断号 → 自动重取
	f.setSnap(Snapshot{LastID: 200, Bids: [][2]float64{{98, 3}}, Asks: [][2]float64{{99, 1}}})
	r.onDiff(ctx, s, Diff{U: 150, Last: 151, PU: 149})
	waitFor(t, func() bool { return f.calls() == 2 })
	waitFor(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return !s.sync && s.book.Bids[98] == 3 })
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 500; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("等不到")
}

// 每秒一列，满一分钟封一块；识别结果也会进去
func TestRecorderSecondsAndHistory(t *testing.T) {
	r := New(&fakeFeed{}, []string{"BTC-PERP"}, func(string) float64 { return 0.1 }, nil)
	s := r.syms["BTC-PERP"]
	s.book.Reset(Snapshot{LastID: 1, Bids: [][2]float64{{99990, 1}}, Asks: [][2]float64{{100010, 2}}})
	_ = s.book.Apply(Diff{U: 1, Last: 2})
	t0 := int64(1_700_000_040_000) // 整分钟
	r.OnTrade("BTC-PERP", t0+500, 100000, 0.5, true)
	r.OnTrade("OTHER", t0, 1, 1, true) // 没录的品种不管
	// 第 5 秒到第 9 秒断开（快照中）
	for i := int64(0); i < 130; i++ {
		s.mu.Lock()
		s.sync = i >= 5 && i < 9
		s.mu.Unlock()
		r.second(s, t0+i*1000)
	}
	if len(s.recent) != 2 {
		t.Fatalf("应封了两分钟：%d", len(s.recent))
	}
	h, err := r.BuildHistory(context.Background(), "BTC-PERP", t0, t0+120_000, 200, 500, 0, 0, 0.001)
	if err != nil {
		t.Fatal(err)
	}
	if h.DT != 1000 || h.Rows == 0 || h.Step != 2 {
		t.Fatalf("列宽和行宽：%+v", h.DT)
	}
	if len(h.Gaps) != 1 || h.Gaps[0] != [2]int64{t0 + 5000, t0 + 9000} {
		t.Fatalf("没数据的时段：%v", h.Gaps)
	}
	if len(h.Trades) != 1 || h.Trades[0][2] != 50000 || h.Trades[0][3] != 1 {
		t.Fatalf("成交：%v", h.Trades)
	}
	raw, _ := base64.StdEncoding.DecodeString(h.Cells)
	r0 := int(math.Round((99990 - h.P0) / h.Step))
	if got := raw[r0*h.Cols]; got != ToCell(99990) {
		t.Fatalf("第一列第 %d 行应是 %d：%d", r0, ToCell(99990), got)
	}
	if raw[r0*h.Cols+6] != 0 {
		t.Fatal("断开的那几秒应是空的")
	}
	if h.Mids[1] != 100000 || h.Mids[6] != 0 {
		t.Fatalf("中间价：%v", h.Mids[:7])
	}
	// 一次看 1 小时：一列 5 秒，一列里取平均。一直挂着的单亮度不变
	h2, _ := r.BuildHistory(context.Background(), "BTC-PERP", t0, t0+3600_000, 720, 500, 0, 0, 0.001)
	if h2.DT != 5000 {
		t.Fatalf("应 5 秒一列：%d", h2.DT)
	}
	raw2, _ := base64.StdEncoding.DecodeString(h2.Cells)
	if raw2[int(math.Round((99990-h2.P0)/h2.Step))*h2.Cols] != ToCell(99990) {
		t.Fatal("一直挂着的单，合并后亮度应不变")
	}
}

// 合并时闪一下就撤的单要变暗：10 秒一列，一个单只挂了 1 秒，亮度只剩十分之一
func TestHistoryAverage(t *testing.T) {
	r := New(&fakeFeed{}, []string{"BTC-PERP"}, func(string) float64 { return 0.1 }, nil)
	s := r.syms["BTC-PERP"]
	s.book.Reset(Snapshot{LastID: 1, Bids: [][2]float64{{99990, 1}}, Asks: [][2]float64{{100010, 1}}})
	_ = s.book.Apply(Diff{U: 1, Last: 2})
	t0 := int64(1_700_000_040_000)
	for i := int64(0); i < 60; i++ {
		s.mu.Lock()
		if i == 3 {
			s.book.Asks[100100] = 10
		} else {
			delete(s.book.Asks, 100100)
		}
		s.mu.Unlock()
		r.second(s, t0+i*1000)
	}
	r.second(s, t0+60_000)
	h, err := r.BuildHistory(context.Background(), "BTC-PERP", t0, t0+3600_000, 360, 500, 99000, 101000, 0)
	if err != nil {
		t.Fatal(err)
	}
	if h.DT != 10_000 {
		t.Fatalf("应 10 秒一列：%d", h.DT)
	}
	raw, _ := base64.StdEncoding.DecodeString(h.Cells)
	row := int(math.Round((100100 - h.P0) / h.Step))
	want := (int(ToCell(100100*10)) + 5) / 10
	if int(raw[row*h.Cols]) != want {
		t.Fatalf("闪一下的单应变暗：%d，应是 %d", raw[row*h.Cols], want)
	}
	stay := int(math.Round((99990 - h.P0) / h.Step))
	if raw[stay*h.Cols] != ToCell(99990) {
		t.Fatalf("一直挂着的单亮度不变：%d", raw[stay*h.Cols])
	}
}

// 模拟盘口：序号连续，拼接代码能直接用，也有大单
func TestSimDepthSequence(t *testing.T) {
	s := &SimDepth{Instruments: []string{"BTC-PERP"}, Price: func(string) (float64, bool) { return 70000, true }, Tick: func(string) float64 { return 0.1 }, Seed: 7}
	s.rnd = rand.New(rand.NewPCG(7, 8))
	b := NewLocalBook()
	first, _ := s.step("BTC-PERP")
	b.Reset(Snapshot{LastID: first.Last})
	seenWall := false
	for i := 0; i < 2000; i++ {
		d, ok := s.step("BTC-PERP")
		if !ok {
			t.Fatal("应有数据")
		}
		if err := b.Apply(d); err != nil {
			t.Fatalf("序号断了：%v", err)
		}
		for _, q := range b.Bids {
			if q*70000 > 500_000 {
				seenWall = true
			}
		}
	}
	bid, ask := b.Best()
	if bid >= ask || bid < 69000 || ask > 71000 {
		t.Fatalf("盘口不对：%v %v", bid, ask)
	}
	if !seenWall {
		t.Fatal("应出现过大单")
	}
}

// 解析币安的增量
func TestParseDepth(t *testing.T) {
	sym, d, err := parseDepth([]byte(`{"stream":"btcusdt@depth@100ms","data":{"e":"depthUpdate","s":"BTCUSDT","U":5,"u":7,"pu":4,"b":[["100.5","2"]],"a":[["101","0"]]}}`))
	if err != nil || sym != "BTCUSDT" || d.U != 5 || d.Last != 7 || d.PU != 4 || d.Bids[0] != [2]float64{100.5, 2} || d.Asks[0][1] != 0 {
		t.Fatalf("解析：%v %+v %v", sym, d, err)
	}
	if instOf("BTCUSDT") != "BTC-PERP" || symOf("ETH-PERP") != "ETHUSDT" {
		t.Fatal("品种名")
	}
}

// V.11 足迹手算样例：两根 1 分钟 K 线，档位宽 10
func TestFootprintByHand(t *testing.T) {
	trades := []Trade{
		{T: 60_000, P: 100, Q: 1, Buy: true},
		{T: 61_000, P: 104, Q: 2, Buy: true}, // 同一档：100–110
		{T: 62_000, P: 109, Q: 0.5},          // 同一档的主动卖
		{T: 63_000, P: 110, Q: 3},            // 下一档
		{T: 120_000, P: 95, Q: 4, Buy: true}, // 下一根
		{T: 179_999, P: 95, Q: 1},
	}
	bars := BuildFootprint(trades, 60_000, 10)
	if len(bars) != 2 || bars[0].T != 60_000 || bars[1].T != 120_000 {
		t.Fatalf("两根 K 线：%+v", bars)
	}
	l := bars[0].L
	if len(l) != 2 || l[0].P != 100 || l[0].B != 3 || l[0].S != 0.5 || l[1].P != 110 || l[1].S != 3 || l[1].B != 0 {
		t.Fatalf("第一根：%+v", l)
	}
	if bars[1].L[0].P != 90 || bars[1].L[0].B != 4 || bars[1].L[0].S != 1 {
		t.Fatalf("第二根：%+v", bars[1].L)
	}
}

func TestFineStep(t *testing.T) {
	// 平均振幅 80，四十分之一是 2
	trades := []Trade{{T: 60_000, P: 100}, {T: 61_000, P: 180}, {T: 120_000, P: 100}, {T: 121_000, P: 180}}
	if s := autoFineStep(trades, 60_000, 0.1); s != 2 {
		t.Fatalf("细档位：%v", s)
	}
	if s := autoFineStep(trades[:1], 60_000, 0.1); s != 0.1 {
		t.Fatalf("没有振幅时用价格最小单位：%v", s)
	}
	if nice125(0.7) != 0.5 || nice125(0.8) != 1 || nice125(2.6) != 2 || nice125(40) != 50 {
		t.Fatal("取整")
	}
}

// 足迹从录制里取：内存里还没存盘的成交也算；没录的币返回 ok=false
func TestFootprintFromRecorder(t *testing.T) {
	r := New(&fakeFeed{}, []string{"BTC-PERP"}, func(string) float64 { return 0.1 }, nil)
	s := r.syms["BTC-PERP"]
	s.book.Reset(Snapshot{LastID: 1, Bids: [][2]float64{{99990, 1}}, Asks: [][2]float64{{100010, 1}}})
	_ = s.book.Apply(Diff{U: 1, Last: 2})
	t0 := int64(1_700_000_040_000)
	r.OnTrade("BTC-PERP", t0+500, 100000, 0.5, true)
	r.second(s, t0+1000)
	r.OnTrade("BTC-PERP", t0+1500, 100000, 0.25, false)
	d, ok, err := r.Footprint(context.Background(), "BTC-PERP", t0, t0+60_000, 60_000)
	if !ok || err != nil {
		t.Fatal(ok, err)
	}
	if len(d.Bars) != 1 || len(d.Bars[0].L) != 1 || d.Bars[0].L[0].B != 0.5 || d.Bars[0].L[0].S != 0.25 {
		t.Fatalf("足迹：%+v", d.Bars)
	}
	if _, ok, _ := r.Footprint(context.Background(), "ADA-PERP", t0, t0+60_000, 60_000); ok {
		t.Fatal("没录的币")
	}
}

func TestBookMerge(t *testing.T) {
	b := NewLocalBook()
	b.Reset(Snapshot{LastID: 10, Bids: [][2]float64{{100, 1}, {99, 2}, {90, 7}}, Asks: [][2]float64{{101, 1}, {110, 7}}})
	_ = b.Apply(Diff{U: 11, Last: 11, PU: 10})
	// 快照覆盖 95–100 和 101–105：范围内换成快照的，范围外不动；快照之后的增量要重放，之前的不要
	b.Merge(Snapshot{LastID: 20, Bids: [][2]float64{{100, 5}, {95, 3}}, Asks: [][2]float64{{101, 5}, {105, 3}}}, []Diff{
		{U: 18, Last: 19, Bids: [][2]float64{{95, 9}}},
		{U: 20, Last: 21, Bids: [][2]float64{{100, 0}, {98, 4}}, Asks: [][2]float64{{105, 8}}},
	})
	if b.Bids[99] != 0 {
		t.Fatalf("范围内快照没有的档要删：%v", b.Bids)
	}
	if b.Bids[90] != 7 || b.Asks[110] != 7 {
		t.Fatalf("范围外的不动：%v %v", b.Bids, b.Asks)
	}
	if b.Bids[95] != 3 {
		t.Fatalf("快照之前的增量不算：%v", b.Bids)
	}
	if _, ok := b.Bids[100]; ok || b.Bids[98] != 4 || b.Asks[105] != 8 {
		t.Fatalf("快照之后的增量要重放：%v %v", b.Bids, b.Asks)
	}
}

func TestBuildFill(t *testing.T) {
	t0 := int64(1_700_000_040_000) // 整分钟
	from, split, to := t0+10_000, t0+30_000, t0+120_000
	rows := buildFill(from, split, to, []Trade{{T: t0 + 12_500, P: 100, Q: 1, Buy: true}, {T: t0 + 20_100, P: 110, Q: 2}}, []Bar1m{
		{T: t0, O: 50, C: 109, QuoteUSD: 600, TakerBuyUSD: 360},
		{T: t0 + 60_000, O: 110, C: 169, QuoteUSD: 60, TakerBuyUSD: 30},
	})
	a, b := rows[t0], rows[t0+60_000]
	if a == nil || b == nil || len(rows) != 2 {
		t.Fatalf("应有两分钟：%d", len(rows))
	}
	if a.Mids[9] != 0 || a.Mids[10] != 100 || a.Mids[19] != 100 || a.Mids[20] != 110 || a.Mids[29] != 110 {
		t.Fatalf("逐笔的价格沿用到下一笔：%v", a.Mids[:30])
	}
	if a.Mids[30] != 80 || a.Mids[59] != 109 || b.Mids[0] != 110 || b.Mids[59] != 169 {
		t.Fatalf("K 线一分钟内直线走：%v %v", a.Mids[30:], b.Mids)
	}
	if len(a.Trades) != 2 {
		t.Fatalf("成交：%v", a.Trades)
	}
	// 第一分钟只缺后 30 秒，成交额算一半
	if a.Buy != 180 || a.Sell != 120 || b.Buy != 30 || b.Sell != 30 {
		t.Fatalf("成交额：%v %v %v %v", a.Buy, a.Sell, b.Buy, b.Sell)
	}
}

func TestHolesBetween(t *testing.T) {
	m := int64(60_000)
	got := holesBetween("X", []int64{m, 2 * m, 5 * m, 6 * m}, 9*m)
	if len(got) != 2 || got[0].from != 3*m || got[0].to != 5*m || got[1].from != 7*m || got[1].to != 9*m {
		t.Fatalf("%+v", got)
	}
	if len(holesBetween("X", nil, 9*m)) != 0 {
		t.Fatal("没录过就没有空缺")
	}
}

// 连接卡住：盘口还在，但增量停了。不能把旧盘口当新数据录，恢复后要报出这段空缺
func TestRecorderStall(t *testing.T) {
	r := New(&fakeFeed{}, []string{"BTC-PERP"}, func(string) float64 { return 0.1 }, nil)
	s := r.syms["BTC-PERP"]
	s.book.Reset(Snapshot{LastID: 1, Bids: [][2]float64{{99990, 1}}, Asks: [][2]float64{{100010, 1}}})
	_ = s.book.Apply(Diff{U: 1, Last: 2})
	t0 := int64(1_700_000_040_000)
	s.lastDiff = time.Now().UnixMilli()
	r.second(s, t0)
	if s.mids[0] <= 0 {
		t.Fatal("正常时要录")
	}
	s.lastDiff = time.Now().UnixMilli() - stallMs - 1000
	for i := int64(1); i <= 5; i++ {
		r.second(s, t0+i*1000)
	}
	for i := 1; i <= 5; i++ {
		if s.mids[i] != 0 {
			t.Fatalf("卡住时第 %d 秒不该录：%v", i, s.mids[i])
		}
	}
	s.lastDiff = time.Now().UnixMilli()
	r.second(s, t0+6000)
	if s.mids[6] <= 0 {
		t.Fatal("恢复后要接着录")
	}
	select {
	case h := <-r.holes:
		if h.from != t0+1000 || h.to != t0+6000 {
			t.Fatalf("空缺：%+v", h)
		}
	default:
		t.Fatal("恢复后要报出空缺")
	}
}

// 价格走远了：取一份新快照补全盘口，录制不停，完整盘口的范围跟着走
func TestRecorderRefresh(t *testing.T) {
	f := &fakeFeed{snap: Snapshot{LastID: 50, Bids: [][2]float64{{109, 4}, {108, 6}}, Asks: [][2]float64{{111, 4}}}}
	r := New(f, []string{"BTC-PERP"}, func(string) float64 { return 0.1 }, nil)
	r.ctx = context.Background()
	s := r.syms["BTC-PERP"]
	s.book.Reset(Snapshot{LastID: 1, Bids: [][2]float64{{99, 1}}, Asks: [][2]float64{{101, 1}}})
	_ = s.book.Apply(Diff{U: 1, Last: 2})
	s.cover = [2]float64{99, 101}
	s.lastSnap = time.Now().UnixMilli() - refreshGap - 1
	s.mu.Lock()
	r.maybeRefresh(s, 100) // 价格还在中间：不取
	refreshing := s.refreshing
	s.mu.Unlock()
	if refreshing {
		t.Fatal("价格没动不用补")
	}
	s.mu.Lock()
	r.maybeRefresh(s, 100.9)
	refreshing = s.refreshing
	s.mu.Unlock()
	if !refreshing {
		t.Fatal("价格离开中间要补")
	}
	r.onDiff(context.Background(), s, Diff{U: 3, Last: 60, PU: 2, Bids: [][2]float64{{108, 9}}})
	waitFor(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return !s.refreshing })
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cover != [2]float64{108, 111} || s.book.Bids[109] != 4 || s.book.Bids[108] != 9 || s.sync {
		t.Fatalf("%v %v sync=%v", s.cover, s.book.Bids, s.sync)
	}
}

func TestBinanceFill(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		calls++
		switch q.URL.Path {
		case "/fapi/v1/aggTrades":
			// 第一页 1000 笔（最后一笔在 2000 毫秒），第二页从 2000 开始、含重复的那一笔
			if q.URL.Query().Get("startTime") == "1000" {
				var rows []string
				for i := 1; i <= 1000; i++ {
					rows = append(rows, fmt.Sprintf(`{"a":%d,"p":"100","q":"1","T":%d,"m":false}`, i, 1000+i))
				}
				fmt.Fprint(w, "["+strings.Join(rows, ",")+"]")
				return
			}
			fmt.Fprint(w, `[{"a":1000,"p":"100","q":"1","T":2000,"m":false},{"a":1001,"p":"101","q":"2","T":2500,"m":true}]`)
		case "/fapi/v1/klines":
			fmt.Fprint(w, `[[60000,"10","12","9","11","5",119999,"100",3,"2","40","0"]]`)
		}
	}))
	defer srv.Close()
	b := &BinanceDepth{REST: srv.URL}
	ts, done, err := b.AggTrades(context.Background(), "BTC-PERP", 1000, 9000)
	if err != nil || !done || len(ts) != 1001 || ts[1000].P != 101 || ts[1000].Buy || !ts[0].Buy {
		t.Fatalf("%v %v %d", err, done, len(ts))
	}
	bars, err := b.Klines1m(context.Background(), "BTC-PERP", 60_000, 120_000)
	if err != nil || len(bars) != 1 || bars[0].O != 10 || bars[0].C != 11 || bars[0].QuoteUSD != 100 || bars[0].TakerBuyUSD != 40 {
		t.Fatalf("%v %+v", err, bars)
	}
}

// 补回来的价格和成交要进历史：价格填上，那段仍算没有盘口；录到过成交的那一秒不重复算
func TestHistoryWithFill(t *testing.T) {
	url := os.Getenv("QS_TEST_DB")
	if url == "" {
		t.Skip("没有 QS_TEST_DB")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	_, _ = pool.Exec(ctx, `delete from heat_fill where instrument='FILLTEST-PERP'`)
	_, _ = pool.Exec(ctx, `delete from heat_chunks where instrument='FILLTEST-PERP'`)
	defer pool.Exec(ctx, `delete from heat_fill where instrument='FILLTEST-PERP'`)
	r := New(&fakeFeed{}, []string{"FILLTEST-PERP"}, func(string) float64 { return 0.1 }, nil)
	r.SetPool(pool)
	s := r.syms["FILLTEST-PERP"]
	s.book.Reset(Snapshot{LastID: 1, Bids: [][2]float64{{99990, 1}}, Asks: [][2]float64{{100010, 1}}})
	_ = s.book.Apply(Diff{U: 1, Last: 2})
	t0 := int64(1_700_000_040_000)
	r.OnTrade("FILLTEST-PERP", t0+1200, 100000, 1, true)
	r.second(s, t0+1000) // 第 1 秒录到了
	// 同一分钟后面缺了，补回来；第 1 秒也有一笔（和录到的是同一批）不该重复算
	rows := buildFill(t0+1000, t0+10_000, t0+10_000, []Trade{{T: t0 + 1500, P: 100000, Q: 1, Buy: true}, {T: t0 + 5000, P: 100100, Q: 2, Buy: false}}, nil)
	if err := r.saveFill(ctx, "FILLTEST-PERP", rows); err != nil {
		t.Fatal(err)
	}
	// 第二次补同一分钟要合并，不能覆盖
	if err := r.saveFill(ctx, "FILLTEST-PERP", buildFill(t0+20_000, t0+20_000, t0+22_000, nil, []Bar1m{{T: t0, O: 100200, C: 100200, QuoteUSD: 600, TakerBuyUSD: 300}})); err != nil {
		t.Fatal(err)
	}
	h, err := r.BuildHistory(ctx, "FILLTEST-PERP", t0, t0+60_000, 60, 100, 0, 0, 0.005)
	if err != nil {
		t.Fatal(err)
	}
	if h.Mids[1] != s.mids[1] || h.Mids[5] != 100100 || h.Mids[20] != 100200 {
		t.Fatalf("价格：%v", h.Mids[:24])
	}
	if math.Abs(h.Sell[5]-100100*2) > 2 {
		t.Fatalf("补回来的成交要算：%v", h.Sell[:8])
	}
	// 录到的 100000 只算一次，再加 K 线两秒的主动买入额 300×2/60 = 10（摊到这一分钟补回来的各秒）
	buyAll, sellAll := 0.0, 0.0
	for i := range h.Buy {
		buyAll += h.Buy[i]
		sellAll += h.Sell[i]
	}
	if math.Abs(buyAll-100010) > 1e-6 || math.Abs(sellAll-(100100*2+10)) > 1e-6 {
		t.Fatalf("同一秒不重复算：买 %v 卖 %v", buyAll, sellAll)
	}
	found := false
	for _, g := range h.Gaps {
		if g[0] <= t0+5000 && g[1] >= t0+6000 {
			found = true
		}
	}
	if !found {
		t.Fatalf("补了价格，那段仍算没有盘口：%v", h.Gaps)
	}
}

func TestRare(t *testing.T) {
	var seen []float64
	// 不到 20 个：全报
	for i := 0; i < 19; i++ {
		if !rare(&seen, 1, 0.9) {
			t.Fatal("样本少时全报")
		}
	}
	// 够多以后：同样大的不再报，明显大的才报
	n := 0
	rng := rand.New(rand.NewPCG(1, 2))
	for i := 0; i < 500; i++ {
		if rare(&seen, rng.Float64()*100, 0.9) {
			n++
		}
	}
	if n < 30 || n > 80 {
		t.Fatalf("应该只报一成左右：%d", n)
	}
	if !rare(&seen, 1000, 0.9) || len(seen) != 200 {
		t.Fatal("明显大的要报；只留最近 200 个")
	}
}
