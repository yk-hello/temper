package heat

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 录制范围：现价上下 3%。再远的挂单对判断没有帮助，还占地方（实测见 `07`）。
const recFrac = 0.03

const (
	stallMs    = int64(5_000)       // 这么久没有盘口增量：连接卡住了，不再把旧盘口当新数据录
	refreshGap = int64(60_000)      // 补全盘口的最短间隔（快照权重 20，6 个币每分钟 120，上限 2400）
	refreshMax = int64(15 * 60_000) // 价格没怎么动，也隔这么久补一次
)

// 保留 30 天（D81）。
const keepDays = 30

const schema = `
create table if not exists heat_chunks (
  instrument text   not null,
  t          bigint not null,
  width      double precision not null,
  base       bigint not null,
  rows       int    not null,
  cols       int    not null,
  cells      bytea  not null,
  mids       bytea  not null,
  trades     bytea  not null,
  primary key (instrument, t)
);
create table if not exists heat_events (
  instrument text   not null,
  t          bigint not null,
  kind       text   not null,
  body       jsonb  not null
);
create index if not exists heat_events_i on heat_events (instrument, t);
create table if not exists heat_fill (
  instrument text   not null,
  t          bigint not null,
  mids       bytea  not null,
  trades     bytea  not null,
  buy        double precision not null default 0,
  sell       double precision not null default 0,
  primary key (instrument, t)
);
`

// Frame 推给正在看的人的一列（100 毫秒）。
type Frame struct {
	T      int64       `json:"t"`
	Width  float64     `json:"w"`
	Base   int64       `json:"b"` // 第 0 个字节对应的桶号
	Cells  string      `json:"c"` // base64；一个字节一档
	Mid    float64     `json:"m"`
	Trades []Trade     `json:"tr,omitempty"`
	Events []Event     `json:"ev,omitempty"`
	Sync   bool        `json:"sync,omitempty"` // 正在重新拼盘口，这一列没有数据
	Walls  []Wall      `json:"walls,omitempty"`
	Flow   *[2]float64 `json:"flow,omitempty"` // 最近 5 分钟主动买、卖额：有这个字段的是每秒一次的读数
	Cover  *[2]float64 `json:"cov,omitempty"`
}

type sym struct {
	inst  string
	mu    sync.Mutex
	book  *LocalBook
	buf   []Diff // 等快照时攒着的增量
	sync  bool   // 正在取快照
	width float64
	det   *Detector

	cover [2]float64 // 最近一次快照覆盖的范围

	trades1s   []Trade // 这一秒的成交
	tradesLive []Trade // 这 100 毫秒的成交（有人看时才攒）

	// 这一分钟
	minT   int64
	cols   []Column
	mids   []float64
	mTrade []Trade
	recent []*Chunk // 还没写进数据库或刚写进去的几分钟

	subs   map[chan []byte]struct{}
	events []Event // 最近的识别结果（没有数据库时也能看）
	resnap int     // 重取快照的次数
	since  int64   // 开始录制的时间
	lastOK int64   // 最后一列的时间

	lastDiff   int64  // 最后一条增量到的时间
	holeFrom   int64  // 正在缺数据：从这一秒开始
	lastSnap   int64  // 最近一次取快照的时间
	refreshing bool   // 正在补全盘口（不断流）
	refreshBuf []Diff // 补全期间来的增量，拼完要重放
}

// stalled 连接还连着，但增量停了。
func (s *sym) stalled() bool {
	return s.lastDiff != 0 && time.Now().UnixMilli()-s.lastDiff > stallMs
}

// Recorder 频谱的录制和实时推送。
type Recorder struct {
	Feed  Feed
	Tick  func(inst string) float64
	Log   *slog.Logger
	syms  map[string]*sym
	pool  *pgxpool.Pool
	save  chan *saveJob
	evs   chan saveEv
	holes chan hole
	ctx   context.Context
	mu    sync.Mutex
}

type saveJob struct {
	inst string
	c    *Chunk
}
type saveEv struct {
	inst string
	e    Event
}

// New 新建。instruments 是要录的品种（6 个重点币）。
func New(feed Feed, instruments []string, tick func(string) float64, log *slog.Logger) *Recorder {
	r := &Recorder{Feed: feed, Tick: tick, Log: log, syms: map[string]*sym{}, save: make(chan *saveJob, 64), evs: make(chan saveEv, 256), holes: make(chan hole, 64)}
	for _, x := range instruments {
		r.syms[x] = &sym{inst: x, book: NewLocalBook(), subs: map[chan []byte]struct{}{}}
	}
	return r
}

// Instruments 录制的品种。
func (r *Recorder) Instruments() []string {
	out := make([]string, 0, len(r.syms))
	for k := range r.syms {
		out = append(out, k)
	}
	return out
}

// Migrate 建表。
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, schema)
	return err
}

// SetPool 数据库连上后调用，之后的录制才会存盘。
func (r *Recorder) SetPool(p *pgxpool.Pool) {
	r.mu.Lock()
	r.pool = p
	r.mu.Unlock()
}

func (r *Recorder) db() *pgxpool.Pool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pool
}

// OnTrade 喂一笔成交（行情那条线来的）。要快：只加锁放进去。
func (r *Recorder) OnTrade(inst string, t int64, p, q float64, buy bool) {
	s := r.syms[inst]
	if s == nil {
		return
	}
	tr := Trade{T: t, P: p, Q: q, Buy: buy}
	s.mu.Lock()
	s.trades1s = append(s.trades1s, tr)
	if len(s.subs) > 0 {
		s.tradesLive = append(s.tradesLive, tr)
	}
	s.mu.Unlock()
}

// Run 一直跑。
func (r *Recorder) Run(ctx context.Context) {
	r.ctx = ctx
	go r.saver(ctx)
	go r.filler(ctx)
	go r.ticker(ctx)
	go r.janitor(ctx)
	r.Feed.Run(ctx, func() {
		for _, s := range r.syms {
			s.mu.Lock()
			s.buf = nil
			s.mu.Unlock()
			r.resync(ctx, s)
		}
	}, func(inst string, d Diff) {
		if s := r.syms[inst]; s != nil {
			r.onDiff(ctx, s, d)
		}
	})
}

// onDiff 拼一条增量。序号断了就重新取快照。
func (r *Recorder) onDiff(ctx context.Context, s *sym, d Diff) {
	s.mu.Lock()
	s.lastDiff = time.Now().UnixMilli()
	if s.sync {
		if len(s.buf) < 3000 {
			s.buf = append(s.buf, d)
		}
		s.mu.Unlock()
		return
	}
	err := s.book.Apply(d)
	if err == nil && s.refreshing && len(s.refreshBuf) < 3000 {
		s.refreshBuf = append(s.refreshBuf, d)
	}
	s.mu.Unlock()
	if err != nil {
		if r.Log != nil {
			r.Log.Info("盘口序号断了，重新取快照", "instrument", s.inst)
		}
		s.mu.Lock()
		s.buf = []Diff{d}
		s.mu.Unlock()
		r.resync(ctx, s)
	}
}

func (r *Recorder) resync(ctx context.Context, s *sym) {
	s.mu.Lock()
	if s.sync {
		s.mu.Unlock()
		return
	}
	s.sync = true
	s.resnap++
	s.refreshing = false
	s.refreshBuf = nil
	s.mu.Unlock()
	go func() {
		wait := time.Second
		for ctx.Err() == nil {
			// 等一下，让增量先攒起来（币安文档要求先缓存再取快照）
			select {
			case <-ctx.Done():
				return
			case <-time.After(300 * time.Millisecond):
			}
			c, cancel := context.WithTimeout(ctx, 10*time.Second)
			snap, err := r.Feed.Snapshot(c, s.inst)
			cancel()
			if err == nil && r.finishSync(s, snap) {
				return
			}
			if err != nil && r.Log != nil {
				r.Log.Warn("取盘口快照失败", "instrument", s.inst, "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			wait = min(wait*2, 30*time.Second)
		}
	}()
}

// finishSync 把快照和攒着的增量拼起来。拼不上返回 false，再取一次。
func (r *Recorder) finishSync(s *sym, snap Snapshot) bool {
	if len(snap.Bids) == 0 || len(snap.Asks) == 0 {
		return false // 空的快照没有用（模拟盘口刚开始时），再取一次
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.book.Reset(snap)
	for _, d := range s.buf {
		if err := s.book.Apply(d); err != nil {
			s.buf = nil
			return false
		}
	}
	s.buf = nil
	s.sync = false
	s.cover = coverOf(snap)
	s.lastSnap = time.Now().UnixMilli()
	return true
}

func coverOf(snap Snapshot) [2]float64 {
	lo, hi := 0.0, 0.0
	for _, x := range snap.Bids {
		if lo == 0 || x[0] < lo {
			lo = x[0]
		}
	}
	for _, x := range snap.Asks {
		if x[0] > hi {
			hi = x[0]
		}
	}
	return [2]float64{lo, hi}
}

// maybeRefresh 价格离开上次快照的中间，或者隔得久了：再取一份快照把盘口补全（调用时持锁）。
// 不停录制。这样"完整盘口"的范围一直跟着现价，挂单也不会越录越稀。
func (r *Recorder) maybeRefresh(s *sym, mid float64) {
	if r.ctx == nil || s.refreshing || s.cover[1] <= s.cover[0] {
		return
	}
	now := time.Now().UnixMilli()
	age := now - s.lastSnap
	if age < refreshGap {
		return
	}
	half := (s.cover[1] - s.cover[0]) / 2
	if math.Abs(mid-(s.cover[0]+s.cover[1])/2) < half*0.33 && age < refreshMax {
		return
	}
	s.refreshing = true
	s.refreshBuf = nil
	s.lastSnap = now
	go r.refresh(s)
}

func (r *Recorder) refresh(s *sym) {
	select {
	case <-r.ctx.Done():
		return
	case <-time.After(300 * time.Millisecond): // 先让增量攒起来，保证接得上快照
	}
	c, cancel := context.WithTimeout(r.ctx, 10*time.Second)
	snap, err := r.Feed.Snapshot(c, s.inst)
	cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.refreshing {
		return // 中途重新拼过盘口，这份不要了
	}
	buf := s.refreshBuf
	s.refreshing = false
	s.refreshBuf = nil
	if err != nil || s.sync || len(snap.Bids) == 0 || len(snap.Asks) == 0 || len(buf) == 0 || buf[0].U > snap.LastID {
		return // 下一轮再补
	}
	s.book.Merge(snap, buf)
	s.cover = coverOf(snap)
}

func (r *Recorder) ticker(ctx context.Context) {
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	lastSec := int64(0)
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			ms := now.UnixMilli()
			sec := ms / 1000
			for _, s := range r.syms {
				s.live(ms)
			}
			if sec != lastSec {
				lastSec = sec
				for _, s := range r.syms {
					r.second(s, sec*1000)
				}
			}
		}
	}
}

// live 给正在看的人切一列。
func (s *sym) live(ms int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.subs) == 0 {
		return
	}
	f := Frame{T: ms, Width: s.width, Trades: s.tradesLive}
	s.tradesLive = nil
	if s.sync || !s.book.Ready() || s.width == 0 || s.stalled() {
		f.Sync = true
	} else {
		bb, ba := s.book.Best()
		if bb <= 0 || ba <= 0 {
			f.Sync = true
		} else {
			mid := (bb + ba) / 2
			col := s.book.Bucket(s.width, mid, recFrac)
			f.Mid = mid
			f.Base, f.Cells = encodeColumn(col)
			c := s.cover
			f.Cover = &c
		}
	}
	s.push(&f)
}

func (s *sym) push(f *Frame) {
	b, err := json.Marshal(f)
	if err != nil {
		return
	}
	for ch := range s.subs {
		select {
		case ch <- b:
		default: // 看的人太慢：丢一列，不拖累录制
		}
	}
}

func encodeColumn(c Column) (int64, string) {
	lo, hi := int64(math.MaxInt64), int64(math.MinInt64)
	for k := range c {
		lo = min(lo, k)
		hi = max(hi, k)
	}
	if lo > hi {
		return 0, ""
	}
	b := make([]byte, hi-lo+1)
	for k, v := range c {
		b[k-lo] = ToCell(v)
	}
	return lo, base64.StdEncoding.EncodeToString(b)
}

// second 每秒记一列，跑一次识别；满一分钟存盘。
func (r *Recorder) second(s *sym, t int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	trades := s.trades1s
	s.trades1s = nil
	minT := t - t%60000
	if s.minT != 0 && minT != s.minT {
		r.flush(s)
	}
	if s.minT != minT {
		s.minT = minT
		s.cols = make([]Column, 60)
		s.mids = make([]float64, 60)
		s.mTrade = nil
	}
	s.mTrade = append(s.mTrade, trades...)
	if s.sync || !s.book.Ready() || s.stalled() {
		if s.width != 0 && s.holeFrom == 0 {
			s.holeFrom = t
		}
		return
	}
	bb, ba := s.book.Best()
	if bb <= 0 || ba <= 0 {
		return
	}
	mid := (bb + ba) / 2
	if s.holeFrom != 0 {
		if t-s.holeFrom >= fillMinHole {
			select {
			case r.holes <- hole{inst: s.inst, from: s.holeFrom, to: t}:
			default:
			}
		}
		s.holeFrom = 0
	}
	r.maybeRefresh(s, mid)
	if s.width == 0 {
		tick := 0.0
		if r.Tick != nil {
			tick = r.Tick(s.inst)
		}
		s.width = niceWidth(mid, tick)
		s.det = NewDetector(s.width)
		s.since = t
	}
	s.book.Prune(mid, recFrac*1.5)
	col := s.book.Bucket(s.width, mid, recFrac)
	i := (t - minT) / 1000
	s.cols[i] = col
	s.mids[i] = mid
	s.lastOK = t
	if evs := s.det.Step(t, mid, col, trades); len(evs) > 0 {
		s.events = append(s.events, evs...)
		if len(s.events) > 500 {
			s.events = s.events[len(s.events)-500:]
		}
		for _, e := range evs {
			select {
			case r.evs <- saveEv{s.inst, e}:
			default:
			}
		}
		if len(s.subs) > 0 {
			s.push(&Frame{T: t, Width: s.width, Events: evs})
		}
	}
	if len(s.subs) > 0 {
		flow := s.det.Flow()
		s.push(&Frame{T: t, Width: s.width, Mid: mid, Walls: s.det.Walls(mid), Flow: &flow})
	}
}

// flush 把这一分钟封好（调用时持锁）。
func (r *Recorder) flush(s *sym) {
	has := false
	for _, m := range s.mids {
		if m > 0 {
			has = true
			break
		}
	}
	if !has {
		return
	}
	c := BuildChunk(s.minT, s.width, s.cols, s.mids, s.mTrade)
	s.recent = append(s.recent, c)
	if len(s.recent) > 5 {
		s.recent = s.recent[len(s.recent)-5:]
	}
	select {
	case r.save <- &saveJob{s.inst, c}:
	default:
		if r.Log != nil {
			r.Log.Warn("频谱写盘跟不上，丢了一分钟", "instrument", s.inst)
		}
	}
}

func (r *Recorder) saver(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-r.save:
			p := r.db()
			if p == nil || j.c.Rows == 0 {
				continue
			}
			c, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			_, err := p.Exec(c, `insert into heat_chunks (instrument,t,width,base,rows,cols,cells,mids,trades) values ($1,$2,$3,$4,$5,$6,$7,$8,$9) on conflict do nothing`,
				j.inst, j.c.T, j.c.Width, j.c.Base, j.c.Rows, j.c.Cols, j.c.EncodeCells(), j.c.EncodeMids(), j.c.EncodeTrades())
			cancel()
			if err != nil && r.Log != nil {
				r.Log.Warn("存频谱失败", "err", err)
			}
		case e := <-r.evs:
			p := r.db()
			if p == nil {
				continue
			}
			body, _ := json.Marshal(e.e)
			c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, err := p.Exec(c, `insert into heat_events (instrument,t,kind,body) values ($1,$2,$3,$4)`, e.inst, e.e.T, e.e.Kind, body)
			cancel()
			if err != nil && r.Log != nil {
				r.Log.Warn("存识别结果失败", "err", err)
			}
		}
	}
}

// janitor 每小时删掉 30 天以前的。
func (r *Recorder) janitor(ctx context.Context) {
	tick := time.NewTicker(time.Hour)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			p := r.db()
			if p == nil {
				continue
			}
			cut := time.Now().Add(-keepDays * 24 * time.Hour).UnixMilli()
			c, cancel := context.WithTimeout(ctx, time.Minute)
			_, _ = p.Exec(c, `delete from heat_chunks where t < $1`, cut)
			_, _ = p.Exec(c, `delete from heat_events where t < $1`, cut)
			_, _ = p.Exec(c, `delete from heat_fill where t < $1`, cut)
			cancel()
		}
	}
}

// Flush 关机前把当前这一分钟写掉。
func (r *Recorder) Flush(ctx context.Context) {
	p := r.db()
	if p == nil {
		return
	}
	for _, s := range r.syms {
		s.mu.Lock()
		var c *Chunk
		if s.minT != 0 && s.width != 0 {
			c = BuildChunk(s.minT, s.width, s.cols, s.mids, s.mTrade)
		}
		inst := s.inst
		s.mu.Unlock()
		if c == nil || c.Rows == 0 {
			continue
		}
		_, _ = p.Exec(ctx, `insert into heat_chunks (instrument,t,width,base,rows,cols,cells,mids,trades) values ($1,$2,$3,$4,$5,$6,$7,$8,$9) on conflict do nothing`,
			inst, c.T, c.Width, c.Base, c.Rows, c.Cols, c.EncodeCells(), c.EncodeMids(), c.EncodeTrades())
	}
}

// Subscribe 看某个品种的实时数据。
func (r *Recorder) subscribe(inst string) (chan []byte, func()) {
	s := r.syms[inst]
	if s == nil {
		return nil, nil
	}
	ch := make(chan []byte, 64)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		delete(s.subs, ch)
		if len(s.subs) == 0 {
			s.tradesLive = nil
		}
		s.mu.Unlock()
	}
}

// loadChunks 读 [from, to) 的分钟块（from 取整到分钟）：先数据库，再用内存里最新的盖上。
// fn 按时间顺序逐块调用，用完就可以丢，不把 24 小时的数据一起放进内存。
func (r *Recorder) loadChunks(ctx context.Context, inst string, from, to int64, fn func(*Chunk) error) error {
	s := r.syms[inst]
	if s == nil {
		return nil
	}
	s.mu.Lock()
	mem := map[int64]*Chunk{}
	for _, c := range s.recent {
		mem[c.T] = c
	}
	if s.minT != 0 && s.width != 0 {
		// 当前这一分钟：拷一份切片头就够（列只会被整个替换，不会就地改）
		mem[s.minT] = BuildChunk(s.minT, s.width, s.cols, append([]float64(nil), s.mids...), append([]Trade(nil), s.mTrade...))
	}
	s.mu.Unlock()
	start := from - ((from%60000)+60000)%60000
	seen := map[int64]bool{}
	if p := r.db(); p != nil {
		rows, err := p.Query(ctx, `select t,width,base,rows,cols,cells,mids,trades from heat_chunks where instrument=$1 and t>=$2 and t<$3 order by t`, inst, start, to)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c Chunk
			var a, b, d []byte
			if err := rows.Scan(&c.T, &c.Width, &c.Base, &c.Rows, &c.Cols, &a, &b, &d); err != nil {
				return err
			}
			if m := mem[c.T]; m != nil {
				if !seen[c.T] {
					seen[c.T] = true
					if err := fn(m); err != nil {
						return err
					}
				}
				continue
			}
			seen[c.T] = true
			if err := DecodeChunk(&c, a, b, d); err != nil {
				continue
			}
			if err := fn(&c); err != nil {
				return err
			}
		}
		if err := rows.Err(); err != nil && err != pgx.ErrNoRows {
			return err
		}
	}
	var ts []int64
	for t := range mem {
		if !seen[t] && t >= start && t < to {
			ts = append(ts, t)
		}
	}
	for i := range ts {
		for j := i + 1; j < len(ts); j++ {
			if ts[j] < ts[i] {
				ts[i], ts[j] = ts[j], ts[i]
			}
		}
	}
	for _, t := range ts {
		if err := fn(mem[t]); err != nil {
			return err
		}
	}
	return nil
}

// Events 取一段时间的识别结果。
func (r *Recorder) Events(ctx context.Context, inst string, from, to int64) []Event {
	out := []Event{}
	if p := r.db(); p != nil {
		rows, err := p.Query(ctx, `select body from heat_events where instrument=$1 and t>=$2 and t<$3 order by t limit 2000`, inst, from, to)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var raw []byte
				var e Event
				if rows.Scan(&raw) == nil && json.Unmarshal(raw, &e) == nil {
					out = append(out, e)
				}
			}
			return out
		}
	}
	if s := r.syms[inst]; s != nil {
		s.mu.Lock()
		for _, e := range s.events {
			if e.T >= from && e.T < to {
				out = append(out, e)
			}
		}
		s.mu.Unlock()
	}
	return out
}
