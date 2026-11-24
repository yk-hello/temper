package heat

import (
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// History 一段时间的频谱：挂单矩阵、中间价、成交、识别结果、没数据的时段。
type History struct {
	Instrument string       `json:"instrument"`
	T0         int64        `json:"t0"` // 第 0 列的开始时间（毫秒）
	DT         int64        `json:"dt"` // 每列多少毫秒
	Cols       int          `json:"cols"`
	Rows       int          `json:"rows"`
	P0         float64      `json:"p0"`    // 第 0 行的价格
	Step       float64      `json:"step"`  // 每行的价格宽度
	Cells      string       `json:"cells"` // base64；行优先 cells[row*cols+col]；一个字节，公式见 ToCell
	Mids       []float64    `json:"mids"`
	Buy        []float64    `json:"buy"`    // 每列的主动买入额（美元）
	Sell       []float64    `json:"sell"`   // 每列的主动卖出额（美元）
	Trades     [][4]float64 `json:"trades"` // [时间, 价格, 美元, 1=主动买]（小的合并）
	Events     []Event      `json:"events"`
	Gaps       [][2]int64   `json:"gaps"`  // 没有数据的时段
	Since      int64        `json:"since"` // 这个品种最早有数据的时间
	QLo        float64      `json:"q_lo"`
	QHi        float64      `json:"q_hi"`
	Shift      []float64    `json:"shift,omitempty"` // 开屏拼接用：每列的价格平移，真实价格 = P0 + 行 × Step + Shift[列]（见 hero.go）
}

var cellUSD = func() (t [256]float64) {
	for i := range t {
		t[i] = FromCell(byte(i))
	}
	return
}()

// niceDT 每列的时间宽度：1、2、5、10、15、30 秒，1、2、5 分钟……
func niceDT(span int64, cols int) int64 {
	want := span / int64(max(cols, 1))
	for _, v := range []int64{1, 2, 5, 10, 15, 30, 60, 120, 300, 600, 900, 1800, 3600} {
		if want <= v*1000 {
			return v * 1000
		}
	}
	return 3600_000
}

// BuildHistory 拼一段时间。pa、pb 为 0 时由中间价决定范围（再加上下 frac）。
func (r *Recorder) BuildHistory(ctx context.Context, inst string, from, to int64, maxCols, maxRows int, pa, pb, frac float64) (*History, error) {
	dt := niceDT(to-from, maxCols)
	t0 := from - ((from%dt)+dt)%dt
	cols := int((to-t0)/dt) + 1
	if cols <= 0 || cols > 20000 {
		return nil, fmt.Errorf("时间范围不对")
	}
	// 第一遍：只看中间价，定价格范围。解压贵，所以先把块读到一起。
	var chunks []*Chunk
	mids := make([]float64, cols)
	buy := make([]float64, cols)
	sell := make([]float64, cols)
	secs := make([]bool, (to-from)/1000+2)
	lo, hi := math.Inf(1), math.Inf(-1)
	width := 0.0
	var clusters = map[[3]int64]float64{}
	err := r.loadChunks(ctx, inst, from, to, func(c *Chunk) error {
		chunks = append(chunks, c)
		for i, m := range c.Mids {
			t := c.T + int64(i)*1000
			if m <= 0 || t < from || t >= to {
				continue
			}
			x := int((t - t0) / dt)
			mids[x] = m
			secs[(t-from)/1000] = true
			lo = min(lo, m)
			hi = max(hi, m)
		}
		width = max(width, c.Width)
		return nil
	})
	if err != nil {
		return nil, err
	}
	// 补回来的价格：只填这一列本来没有数据的地方。盘口还是缺的，所以 secs 不动，那段仍然算空缺
	fills := r.loadFill(ctx, inst, from, to)
	for _, f := range fills {
		for i, m := range f.Mids {
			t := f.T + int64(i)*1000
			if m <= 0 || t < from || t >= to {
				continue
			}
			if x := int((t - t0) / dt); mids[x] <= 0 {
				mids[x] = m
				lo = min(lo, m)
				hi = max(hi, m)
			}
		}
	}
	chunkAt := make(map[int64]*Chunk, len(chunks))
	for _, c := range chunks {
		chunkAt[c.T] = c
	}
	// 补回来的成交：这一秒录到过成交的不再算（行情线没断时，两边是同一批）
	fillTrades := func(each func(Trade)) {
		for _, f := range fills {
			have := map[int64]bool{}
			if c := chunkAt[f.T]; c != nil {
				for _, tr := range c.Trades {
					have[tr.T/1000] = true
				}
			}
			for _, tr := range f.Trades {
				if tr.T >= from && tr.T < to && !have[tr.T/1000] {
					each(tr)
				}
			}
			if f.Buy <= 0 && f.Sell <= 0 {
				continue
			}
			n := 0.0
			for i, m := range f.Mids {
				if t := f.T + int64(i)*1000; m > 0 && t >= from && t < to {
					n++
				}
			}
			for i, m := range f.Mids {
				if t := f.T + int64(i)*1000; m > 0 && t >= from && t < to {
					x := int((t - t0) / dt)
					buy[x] += f.Buy / n
					sell[x] += f.Sell / n
				}
			}
		}
	}
	out := &History{Instrument: inst, T0: t0, DT: dt, Cols: cols, Mids: mids, Buy: buy, Sell: sell, Events: r.Events(ctx, inst, from, to), Trades: [][4]float64{}, Gaps: gapsOf(secs, from), QLo: qLo, QHi: qHi}
	if s := r.syms[inst]; s != nil {
		s.mu.Lock()
		out.Since = s.since
		s.mu.Unlock()
	}
	if p := r.db(); p != nil {
		var first *int64
		if p.QueryRow(ctx, `select min(t) from heat_chunks where instrument=$1`, inst).Scan(&first) == nil && first != nil {
			out.Since = *first
		}
	}
	if len(chunks) == 0 || width == 0 || math.IsInf(lo, 1) {
		fillTrades(func(tr Trade) {
			x := int((tr.T - t0) / dt)
			if tr.Buy {
				buy[x] += tr.P * tr.Q
			} else {
				sell[x] += tr.P * tr.Q
			}
		})
		return out, nil
	}
	if pa <= 0 || pb <= pa {
		pa, pb = lo*(1-frac), hi*(1+frac)
	}
	step := width
	if (pb-pa)/step > float64(maxRows) {
		step = (pb - pa) / float64(maxRows)
		k := math.Ceil(step / width)
		step = k * width
	}
	p0 := math.Floor(pa/step) * step
	rows := int(math.Ceil((pb-p0)/step)) + 1
	acc := make([]uint32, rows*cols) // 一列里各秒的亮度加起来，最后除以这一列有数据的秒数
	secsIn := make([]uint32, cols)
	sum := make([]float64, rows)
	same := step == width
	addTrade := func(tr Trade) {
		side := int64(0)
		if x := int((tr.T - t0) / dt); tr.Buy {
			side = 1
			buy[x] += tr.P * tr.Q
		} else {
			sell[x] += tr.P * tr.Q
		}
		clusters[[3]int64{(tr.T - t0) / dt, int64(math.Round((tr.P - p0) / step)), side}] += tr.P * tr.Q
	}
	for _, c := range chunks {
		if c.Rows == 0 {
			continue
		}
		rOf := make([]int, c.Rows)
		for i := range rOf {
			rOf[i] = int(math.Round((float64(c.Base+int64(i))*c.Width - p0) / step))
		}
		for col := 0; col < c.Cols; col++ {
			t := c.T + int64(col)*1000
			if t < from || t >= to {
				continue
			}
			x := int((t - t0) / dt)
			if col >= len(c.Mids) || c.Mids[col] <= 0 {
				continue
			}
			secsIn[x]++
			if same {
				for i, r := range rOf {
					if r < 0 || r >= rows {
						continue
					}
					acc[r*cols+x] += uint32(c.Cells[i*c.Cols+col])
				}
				continue
			}
			// 一行里有好几档：同一秒先把金额加起来
			for i := range sum {
				sum[i] = 0
			}
			for i, r := range rOf {
				if r >= 0 && r < rows {
					sum[r] += cellUSD[c.Cells[i*c.Cols+col]]
				}
			}
			for r, u := range sum {
				if u > 0 {
					acc[r*cols+x] += uint32(ToCell(u))
				}
			}
		}
		for _, tr := range c.Trades {
			if tr.T < from || tr.T >= to {
				continue
			}
			addTrade(tr)
		}
	}
	fillTrades(addTrade)
	cells := make([]byte, rows*cols)
	for r := 0; r < rows; r++ {
		for x := 0; x < cols; x++ {
			if n := secsIn[x]; n > 0 {
				cells[r*cols+x] = byte((acc[r*cols+x] + n/2) / n)
			}
		}
	}
	out.Rows, out.P0, out.Step = rows, p0, step
	out.Cells = base64.StdEncoding.EncodeToString(cells)
	// 成交：同一列同一价位同一方向合并，只留金额最大的一批
	type cl struct {
		k [3]int64
		u float64
	}
	all := make([]cl, 0, len(clusters))
	for k, u := range clusters {
		all = append(all, cl{k, u})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].u > all[j].u })
	if len(all) > 4000 {
		all = all[:4000]
	}
	for _, x := range all {
		out.Trades = append(out.Trades, [4]float64{float64(t0 + x.k[0]*dt + dt/2), p0 + float64(x.k[1])*step, x.u, float64(x.k[2])})
	}
	sort.Slice(out.Trades, func(i, j int) bool { return out.Trades[i][0] < out.Trades[j][0] })
	return out, nil
}

// gapsOf 连续 3 秒以上没数据的时段。
func gapsOf(secs []bool, from int64) [][2]int64 {
	out := [][2]int64{}
	// 最后 2 秒的数据可能还没到，不算没数据
	n := len(secs) - 3
	i := 0
	for i < n {
		if secs[i] {
			i++
			continue
		}
		j := i
		for j < n && !secs[j] {
			j++
		}
		if j-i >= 3 {
			out = append(out, [2]int64{from + int64(i)*1000, from + int64(j)*1000})
		}
		i = j
	}
	return out
}

// Status 录制的情况。占用是实测的磁盘大小。
type Status struct {
	Instrument string     `json:"instrument"`
	Ready      bool       `json:"ready"`
	Syncing    bool       `json:"syncing"`
	Width      float64    `json:"width"`
	Mid        float64    `json:"mid"`
	Full       [2]float64 `json:"full"`   // 快照覆盖的完整盘口范围
	Levels     int        `json:"levels"` // 本地盘口现在有多少档
	Resync     int        `json:"resync"`
	Since      int64      `json:"since"`
	Last       int64      `json:"last"`
}

// Statuses 所有品种的情况。
func (r *Recorder) Statuses() []Status {
	out := make([]Status, 0, len(r.syms))
	for _, s := range r.syms {
		s.mu.Lock()
		st := Status{Instrument: s.inst, Ready: s.book.Ready() && !s.sync, Syncing: s.sync, Width: s.width, Full: s.cover, Levels: len(s.book.Bids) + len(s.book.Asks), Resync: s.resnap, Since: s.since, Last: s.lastOK}
		if bb, ba := s.book.Best(); bb > 0 && ba > 0 {
			st.Mid = (bb + ba) / 2
		}
		s.mu.Unlock()
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Instrument < out[j].Instrument })
	return out
}

func writeJSON(w http.ResponseWriter, r *http.Request, v any) {
	w.Header().Set("Content-Type", "application/json")
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		w.Header().Set("Content-Encoding", "gzip")
		zw := gzip.NewWriter(w)
		defer zw.Close()
		_ = json.NewEncoder(zw).Encode(v)
		return
	}
	_ = json.NewEncoder(w).Encode(v)
}

// Routes 挂接口。公开数据，不需要登录。
func (r *Recorder) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/heat/status", func(w http.ResponseWriter, q *http.Request) {
		res := map[string]any{"instruments": r.Statuses(), "keep_days": keepDays}
		if p := r.db(); p != nil {
			c, cancel := context.WithTimeout(q.Context(), 5*time.Second)
			defer cancel()
			var size, chunks int64
			var first *int64
			if p.QueryRow(c, `select pg_total_relation_size('heat_chunks'), count(*), min(t) from heat_chunks`).Scan(&size, &chunks, &first) == nil {
				res["bytes"], res["chunks"] = size, chunks
				if first != nil {
					res["first"] = *first
				}
			}
		}
		writeJSON(w, q, res)
	})
	mux.HandleFunc("GET /api/flow/footprint", func(w http.ResponseWriter, q *http.Request) {
		v := q.URL.Query()
		interval, _ := strconv.ParseInt(v.Get("interval"), 10, 64)
		now := time.Now().UnixMilli()
		to, _ := strconv.ParseInt(v.Get("to"), 10, 64)
		from, _ := strconv.ParseInt(v.Get("from"), 10, 64)
		if to <= 0 || to > now {
			to = now
		}
		if interval < 60_000 || from >= to {
			http.Error(w, `{"error":"BAD_RANGE"}`, 400)
			return
		}
		if from < to-3*86400_000 {
			from = to - 3*86400_000 // 最多一次取 3 天
		}
		c, cancel := context.WithTimeout(q.Context(), 30*time.Second)
		defer cancel()
		d, ok, err := r.Footprint(c, v.Get("instrument"), from, to, interval)
		if !ok {
			http.Error(w, `{"error":"NOT_RECORDED"}`, 404)
			return
		}
		if err != nil {
			http.Error(w, `{"error":"FOOTPRINT_FAILED"}`, 500)
			return
		}
		writeJSON(w, q, d)
	})
	mux.HandleFunc("GET /api/heat/history", func(w http.ResponseWriter, q *http.Request) {
		inst := q.URL.Query().Get("instrument")
		if r.syms[inst] == nil {
			http.Error(w, `{"error":"NOT_RECORDED"}`, 404)
			return
		}
		num := func(k string, d float64) float64 {
			if v, err := strconv.ParseFloat(q.URL.Query().Get(k), 64); err == nil {
				return v
			}
			return d
		}
		now := time.Now().UnixMilli()
		to := int64(num("to", float64(now)))
		from := int64(num("from", float64(to-30*60*1000)))
		if to > now {
			to = now
		}
		if from < to-24*3600*1000 {
			from = to - 24*3600*1000 // 最多一次看 24 小时
		}
		if from >= to {
			http.Error(w, `{"error":"BAD_RANGE"}`, 400)
			return
		}
		cols := int(math.Max(60, math.Min(num("cols", 1200), 4096)))
		rows := int(math.Max(60, math.Min(num("rows", 1000), 2000)))
		c, cancel := context.WithTimeout(q.Context(), 30*time.Second)
		defer cancel()
		h, err := r.BuildHistory(c, inst, from, to, cols, rows, num("pa", 0), num("pb", 0), math.Max(0.0005, math.Min(num("frac", 0.005), recFrac)))
		if err != nil {
			http.Error(w, `{"error":"HISTORY_FAILED"}`, 500)
			return
		}
		writeJSON(w, q, h)
	})
	// 首页地形用：20 秒算一份，所有访客共用。现算一次要解压两小时的数据，访客一多扛不住。
	hero := &heroCache{}
	mux.HandleFunc("GET /api/heat/hero", func(w http.ResponseWriter, q *http.Request) {
		inst := q.URL.Query().Get("instrument")
		if inst == "" {
			inst = "BTC-PERP"
		}
		if r.syms[inst] == nil {
			http.Error(w, `{"error":"NOT_RECORDED"}`, 404)
			return
		}
		h, err := hero.get(q.Context(), r, inst, q.URL.Query().Get("q") == "lo")
		if err != nil {
			http.Error(w, `{"error":"HISTORY_FAILED"}`, 500)
			return
		}
		writeJSON(w, q, h)
	})
	mux.HandleFunc("GET /api/heat/stream", func(w http.ResponseWriter, q *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, `{"error":"NO_STREAM"}`, 500)
			return
		}
		ch, stop := r.subscribe(q.URL.Query().Get("instrument"))
		if ch == nil {
			http.Error(w, `{"error":"NOT_RECORDED"}`, 404)
			return
		}
		defer stop()
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(200)
		fl.Flush()
		keep := time.NewTicker(15 * time.Second)
		defer keep.Stop()
		for {
			select {
			case <-q.Context().Done():
				return
			case b := <-ch:
				if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
					return
				}
				fl.Flush()
			case <-keep.C:
				if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
					return
				}
				fl.Flush()
			}
		}
	})
}

// heroCache 首页地形的快照。同一时间只算一次，别的请求等它或者用上一份。
type heroCache struct {
	mu   sync.Mutex
	data map[string]*heroEntry
}

type heroEntry struct {
	at int64
	h  *History
}

func (c *heroCache) get(ctx context.Context, r *Recorder, inst string, low bool) (*History, error) {
	key := inst
	cols, rows := 320, 170
	if low {
		key += "|lo"
		cols, rows = 180, 90
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.data == nil {
		c.data = map[string]*heroEntry{}
	}
	now := time.Now().UnixMilli()
	if e := c.data[key]; e != nil && now-e.at < 20_000 {
		return e.h, nil
	}
	x, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	h, err := r.RecentHistory(x, inst, 2*3600_000, cols, rows, 0.005) // 最近录到的两小时，断档跳过
	if err != nil {
		if e := c.data[key]; e != nil {
			return e.h, nil // 算失败就继续用上一份
		}
		return nil, err
	}
	c.data[key] = &heroEntry{at: now, h: h}
	return h, nil
}
