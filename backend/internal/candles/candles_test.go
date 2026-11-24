package candles

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBackfillMerge(t *testing.T) {
	s := New()
	// 实时数据从 12:03:20 开始（那一分钟只看到了一部分）
	s.Trade("BTC-PERP", 180_000+20_000, "101", "1", true)
	s.Trade("BTC-PERP", 240_000+5_000, "102", "1", false)
	hist := []Candle{
		{T: 60_000, O: 90, H: 95, L: 89, C: 94, VB: 3, VS: 2},
		{T: 120_000, O: 94, H: 99, L: 93, C: 98, VB: 1, VS: 1},
		{T: 180_000, O: 98, H: 100, L: 97, C: 99, VB: 5, VS: 4}, // 和实时的第一根重叠
	}
	s.Backfill("BTC-PERP", 60_000, hist)
	got := s.Get("BTC-PERP", 60_000, 0)
	if len(got) != 4 {
		t.Fatalf("应有 4 根，实际 %d：%+v", len(got), got)
	}
	m := got[2]
	if m.T != 180_000 || m.O != 98 || m.H != 101 || m.L != 97 || m.C != 101 {
		t.Fatalf("重叠的一根合并不对：%+v", m)
	}
	if got[0].T != 60_000 || got[3].T != 240_000 {
		t.Fatal("顺序不对")
	}
	// 再补一次不会重复
	s.Backfill("BTC-PERP", 60_000, hist)
	if n := len(s.Get("BTC-PERP", 60_000, 0)); n != 4 {
		t.Fatalf("重复补数据：%d", n)
	}
	// 没有实时数据时直接用历史
	s.Backfill("ETH-PERP", 60_000, hist)
	if n := len(s.Get("ETH-PERP", 60_000, 0)); n != 3 {
		t.Fatal(n)
	}
}

// 第 13 条：断流后第一根的高低点不能被上一根的收盘价拉宽；开盘价是这一根的第一笔成交（和币安一样）。
func TestGapDoesNotWidenBar(t *testing.T) {
	s := New()
	t0 := int64(1_793_779_200_000)
	s.Trade("BTC-PERP", t0+1000, "80000", "1", true)
	s.Trade("BTC-PERP", t0+3*3600_000+1000, "75000", "1", true) // 睡了 3 小时，价格已经跌到 75000
	s.Trade("BTC-PERP", t0+3*3600_000+2000, "75100", "1", true)
	got := s.Get("BTC-PERP", 60_000, 0)
	if len(got) != 2 {
		t.Fatalf("应有 2 根：%+v", got)
	}
	if b := got[1]; b.O != 75000 || b.H != 75100 || b.L != 75000 || b.C != 75100 {
		t.Fatalf("断流后第一根不对：%+v", b)
	}
	// 没断流时分钟线也按第一笔成交开盘，不接上一根的收盘价
	s.Trade("BTC-PERP", t0+3*3600_000+60_000+1000, "75300", "1", true)
	got = s.Get("BTC-PERP", 60_000, 0)
	if b := got[2]; b.O != 75300 || b.L != 75300 {
		t.Fatalf("分钟线开盘价应是第一笔成交：%+v", b)
	}
	// 1 秒线紧挨着时接上一根的收盘价（图上连续），断流后不接
	sec := s.Get("BTC-PERP", 1000, 0)
	if b := sec[len(sec)-2]; b.O != 75000 {
		t.Fatalf("1 秒线紧挨着时应接收盘价：%+v", b)
	}
	if b := sec[1]; b.O != 75000 || b.H != 75000 {
		t.Fatalf("1 秒线断流后不接：%+v", b)
	}
}

// 第 1 条：内存里的 1 分钟线从一天中间开始时，日线不能用这半天的数据，要用交易所的完整数据。
func TestPartialBoundaryBarComesFromExchange(t *testing.T) {
	s := New()
	day := int64(86_400_000)
	d0 := int64(1_793_750_400_000) // 2026-11-04 00:00:00 UTC
	if d0%day != 0 {
		t.Fatal("测试数据不是 UTC 零点")
	}
	start := d0 + 12*3600_000 // 内存从 12:00 开始
	for i := int64(0); i < 13*60; i++ {
		s.Trade("BTC-PERP", start+i*60_000+1000, "80000", "1", true)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		end, _ := strconv.ParseInt(r.URL.Query().Get("endTime"), 10, 64)
		ts := (end + 1) / day * day
		// 交易所的完整日线：最低 75690（上午跌过）
		w.Write([]byte(`[[` + strconv.FormatInt(ts-day, 10) + `,"81000","82000","75690","80000","250597",0,"0",5,"100000","0","0"]]`))
	}))
	defer srv.Close()
	s.Remote = &Remote{Symbols: map[string]string{"BTC-PERP": "BTCUSDT"}, BaseURL: srv.URL}
	got := s.Query(context.Background(), "BTC-PERP", day, 0, 0, 300)
	if len(got) != 2 {
		t.Fatalf("应有 2 根：%+v", got)
	}
	if b := got[0]; b.T != d0 || b.L != 75690 || b.VB+b.VS != 250597 {
		t.Fatalf("边界那根应是交易所的完整数据：%+v", b)
	}
	if got[1].T != d0+day {
		t.Fatalf("新的一天应来自内存：%+v", got[1])
	}
	// 实时刷新走 since：不向交易所要，也不丢最新的
	s.Remote.BaseURL = "http://127.0.0.1:1"
	if r := s.Query(context.Background(), "BTC-PERP", day, d0+day, 0, 0); len(r) != 1 || r[0].T != d0+day {
		t.Fatalf("since：%+v", r)
	}
}

// 断流留下的窟窿由 Heal 从交易所补上；断流前后那两根也换成交易所的完整数据。
func TestHealFillsGap(t *testing.T) {
	s := New()
	t0 := int64(1_793_779_200_000)
	s.Trade("BTC-PERP", t0+1000, "80000", "1", true)
	s.Trade("BTC-PERP", t0+3*60_000+1000, "79000", "1", true)
	var rows []string
	for i := int64(0); i <= 3; i++ {
		rows = append(rows, `[`+strconv.FormatInt(t0+i*60_000, 10)+`,"80100","80200","78000","79500","50",0,"0",5,"20","0","0"]`)
	}
	fail := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			w.WriteHeader(500)
			return
		}
		w.Write([]byte("[" + strings.Join(rows, ",") + "]"))
	}))
	defer srv.Close()
	s.Remote = &Remote{Symbols: map[string]string{"BTC-PERP": "BTCUSDT"}, BaseURL: srv.URL}
	if n := s.Heal(context.Background()); n != 0 {
		t.Fatalf("取不到时不应算补上：%d", n)
	}
	fail = false
	if n := s.Heal(context.Background()); n != 4 {
		t.Fatalf("取不到之后下次应该重试：%d", n)
	}
	got := s.Get("BTC-PERP", 60_000, 0)
	if len(got) != 4 || got[0].L != 78000 || got[1].T != t0+60_000 {
		t.Fatalf("窟窿没补上：%+v", got)
	}
	if b := got[3]; b.L != 78000 || b.C != 79000 {
		t.Fatalf("最新那根：高低点取两边，收盘价留实时的：%+v", b)
	}
	if n := s.Heal(context.Background()); n != 0 {
		t.Fatalf("补过不再补：%d", n)
	}
}

func TestQueryAggregateAndHistory(t *testing.T) {
	s := New()
	// 内存里：从 08:00 开始 3 小时的 1 分钟线（UTC 时间戳，按 4 小时对齐要落在 08:00 那根）
	t0 := int64(1_793_779_200_000) // 2026-11-04 08:00:00 UTC
	for i := int64(0); i < 180; i++ {
		s.Trade("BTC-PERP", t0+i*60_000+1000, strconv.FormatInt(100+i, 10), "1", i%2 == 0)
	}
	// 5 分钟线：36 根，按 UTC 整 5 分钟对齐
	got := s.Query(context.Background(), "BTC-PERP", 300_000, 0, 0, 1000)
	if len(got) != 36 || got[0].T != t0 || got[1].T != t0+300_000 || got[0].O != 100 || got[0].C != 104 || got[0].H != 104 {
		t.Fatalf("5 分钟线不对：%d %+v", len(got), got[0])
	}
	// since：从包含 since 的那根开始
	got = s.Query(context.Background(), "BTC-PERP", 300_000, t0+170*60_000+5, 0, 0)
	if len(got) != 2 || got[0].T != t0+170*60_000 {
		t.Fatalf("since：%+v", got)
	}
	// before + limit：之前的最近几根
	got = s.Query(context.Background(), "BTC-PERP", 300_000, 0, t0+60*60_000, 3)
	if len(got) != 3 || got[2].T != t0+55*60_000 {
		t.Fatalf("before：%+v", got)
	}

	// 内存不够时从交易所补更早的
	var asked string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.RawQuery
		end, _ := strconv.ParseInt(r.URL.Query().Get("endTime"), 10, 64)
		var rows []string
		for i := int64(3); i >= 1; i-- {
			ts := (end+1)/14_400_000*14_400_000 - i*14_400_000
			rows = append(rows, `[`+strconv.FormatInt(ts, 10)+`,"90","95","85","92","10",0,"0",5,"6","0","0"]`)
		}
		w.Write([]byte("[" + strings.Join(rows, ",") + "]"))
	}))
	defer srv.Close()
	s.Remote = &Remote{Symbols: map[string]string{"BTC-PERP": "BTCUSDT"}, BaseURL: srv.URL}
	got = s.Query(context.Background(), "BTC-PERP", 14_400_000, 0, 0, 4)
	if len(got) != 4 || got[3].T != t0 || got[0].T != t0-3*14_400_000 || got[0].VB != 6 || got[0].VS != 4 {
		t.Fatalf("补历史：%d %+v（请求 %s）", len(got), got, asked)
	}
	if !strings.Contains(asked, "interval=4h") || !strings.Contains(asked, "symbol=BTCUSDT") {
		t.Fatal(asked)
	}
	// 1 秒线不向交易所要
	asked = ""
	s.Query(context.Background(), "BTC-PERP", 1000, 0, 0, 300)
	if asked != "" {
		t.Fatal("1 秒线不应请求交易所")
	}
}

// 开机那一分钟一半是补的历史、一半是实时成交：这一分钟走完以后换成交易所的完整数据，没走完不换（11-17 走查）
func TestBootMinuteHealedAfterClose(t *testing.T) {
	s := New()
	t0 := int64(1_793_779_200_000)
	now := time.UnixMilli(t0 + 30_000)
	s.Now = func() time.Time { return now }
	s.Trade("BTC-PERP", t0+20_000, "101", "1", true) // 开机后收到的第一笔，在 t0 这一分钟中间
	s.Backfill("BTC-PERP", 60_000, []Candle{{T: t0 - 60_000, O: 90, H: 95, L: 89, C: 94}, {T: t0, O: 98, H: 100, L: 97, C: 99, VB: 5}})
	full := `[[` + strconv.FormatInt(t0, 10) + `,"98","103","96","102","20",0,"0",9,"12","0","0"]]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(full)) }))
	defer srv.Close()
	s.Remote = &Remote{Symbols: map[string]string{"BTC-PERP": "BTCUSDT"}, BaseURL: srv.URL}
	if n := s.Heal(context.Background()); n != 0 {
		t.Fatalf("这一分钟还没走完，不该换：%d", n)
	}
	now = time.UnixMilli(t0 + 61_000)
	s.Trade("BTC-PERP", t0+60_500, "102", "1", false) // 下一分钟开始了
	if n := s.Heal(context.Background()); n != 1 {
		t.Fatalf("走完以后应该换成交易所的：%d", n)
	}
	got := s.Get("BTC-PERP", 60_000, 0)
	var b Candle
	for _, c := range got {
		if c.T == t0 {
			b = c
		}
	}
	if b.H != 103 || b.L != 96 || b.C != 102 || b.VB != 12 {
		t.Fatalf("开机那一分钟没换成交易所的完整数据：%+v", b)
	}
}

// 11-18：攒满 2 倍保留量砍半的那一刻，原来下标越界、后端崩（1 秒线连续 4 小时就到）。砍完最新一根要对，长度不超过 2 倍
func TestTrimDoesNotPanic(t *testing.T) {
	s := &series{ms: 1000, keep: 3}
	for i := int64(0); i < 20; i++ {
		s.add(i*1000, float64(100+i), 1, true)
		s.add(i*1000+500, float64(100+i)+0.5, 1, false)
		if len(s.bars) > 6 {
			t.Fatalf("第 %d 根后留了 %d 根", i, len(s.bars))
		}
		last := s.bars[len(s.bars)-1]
		if last.T != i*1000 || last.C != float64(100+i)+0.5 || last.N != 2 || last.VB != 1 || last.VS != 1 {
			t.Fatalf("第 %d 根：%+v", i, last)
		}
	}
}
