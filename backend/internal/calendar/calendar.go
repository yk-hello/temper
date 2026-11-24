// Package calendar 经济事件（可视化 A）：议息、CPI 这类大事插在 K 线上，附上过去 12 次同类事件后 BTC 一小时的平均振幅。
//
// 数据源只用官方的（D56）：
//   - 美联储议息：日期写在下面，对照 federalreserve.gov/monetarypolicy/fomccalendars.htm 核对过（2026-11-07）
//   - 经济分析局（BEA）的日历文件：PCE（个人收入和支出）、GDP，每 6 小时取一次
//   - 劳工统计局（BLS）：CPI、非农、PPI。它拒绝脚本访问，不绕过（D56）。内置日程：这个目录的 bls.ics（到 2026 年底）
//     加 sources.go 的 blsMore（到 2027 年底）。
//   - FRED（可选，部署的人的免费密钥）：上面这些全自动更新，见 sources.go（D102）。
package calendar

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // 容器里不一定有时区库，BLS 的时间是美东时间

	"github.com/jackc/pgx/v5/pgxpool"

	"qs/internal/candles"
)

//go:embed bls.ics
var blsICS []byte

// Event 一个事件。
type Event struct {
	T     int64  `json:"t"`
	Key   string `json:"key"`   // 同类事件的标识：fomc | cpi | nfp | ppi | pce | gdp
	Title string `json:"title"` // 原文标题
	Vol   *Vol   `json:"vol,omitempty"`
}

// Vol 同类事件后一小时的波动。
type Vol struct {
	N    int     `json:"n"`
	Mean float64 `json:"mean"` // 平均振幅（%）
}

// fomc 议息决议公布的时间（美东 14:00）。
var fomc = []string{
	"2024-01-31T19:00:00Z", "2024-03-20T18:00:00Z", "2024-05-01T18:00:00Z", "2024-06-12T18:00:00Z", "2024-07-31T18:00:00Z", "2024-09-18T18:00:00Z", "2024-11-07T19:00:00Z", "2024-12-18T19:00:00Z",
	"2025-01-29T19:00:00Z", "2025-03-19T18:00:00Z", "2025-05-07T18:00:00Z", "2025-06-18T18:00:00Z", "2025-07-30T18:00:00Z", "2025-09-17T18:00:00Z", "2025-10-29T18:00:00Z", "2025-12-10T19:00:00Z",
	"2026-01-28T19:00:00Z", "2026-03-18T18:00:00Z", "2026-04-29T18:00:00Z", "2026-06-17T18:00:00Z", "2026-07-29T18:00:00Z", "2026-09-16T18:00:00Z", "2026-10-28T18:00:00Z", "2026-12-09T19:00:00Z",
	"2027-01-27T19:00:00Z", "2027-03-17T18:00:00Z", "2027-04-28T18:00:00Z", "2027-06-09T18:00:00Z", "2027-07-28T18:00:00Z", "2027-09-15T18:00:00Z", "2027-10-27T18:00:00Z", "2027-12-08T19:00:00Z",
}

// KeyOf 把 BEA、BLS 的标题归类，别的发布不要。
func KeyOf(title string) string {
	t := strings.ToLower(strings.TrimSpace(title))
	switch {
	case t == "consumer price index" || strings.HasPrefix(t, "consumer price index,"):
		return "cpi"
	case t == "employment situation" || strings.HasPrefix(t, "employment situation,"):
		return "nfp"
	case t == "producer price index" || strings.HasPrefix(t, "producer price index,"):
		return "ppi"
	case strings.HasPrefix(t, "personal income and outlays"):
		return "pce"
	case strings.HasPrefix(t, "gross domestic product by state"), strings.HasPrefix(t, "gdp by state"):
		return ""
	case strings.HasPrefix(t, "gross domestic product"), strings.HasPrefix(t, "gdp ("):
		return "gdp"
	}
	return ""
}

// ParseICS 读日历文件。
func ParseICS(r io.Reader) []Event {
	out := []Event{}
	var title, start string
	var eastern bool
	ny, nyErr := time.LoadLocation("America/New_York")
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		switch {
		case line == "BEGIN:VEVENT":
			title, start, eastern = "", "", false
		case strings.HasPrefix(line, "SUMMARY:"):
			title = strings.ReplaceAll(strings.TrimPrefix(line, "SUMMARY:"), `\,`, ",")
		case strings.HasPrefix(line, "DTSTART"):
			if i := strings.IndexByte(line, ':'); i > 0 {
				start = line[i+1:]
				eastern = strings.Contains(line[:i], "TZID=US-Eastern")
			}
		case line == "END:VEVENT":
			k := KeyOf(title)
			var t time.Time
			var err error
			if eastern && nyErr == nil { // BLS：美东当地时间，夏令时由时区库算
				t, err = time.ParseInLocation("20060102T150405", start, ny)
			} else { // BEA：UTC
				t, err = time.Parse("20060102T150405Z", start)
			}
			if k != "" && err == nil {
				out = append(out, Event{T: t.UnixMilli(), Key: k, Title: title})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].T < out[j].T })
	return out
}

// VolAfter 事件后一小时的振幅（%）：取覆盖这个时刻的 1 小时 K 线。
func VolAfter(ctx context.Context, bars *candles.Remote, t int64) (float64, bool) {
	if bars == nil {
		return 0, false
	}
	h := t - t%3_600_000
	list, err := bars.Get(ctx, "BTC-PERP", 3_600_000, h+3_600_000, 2)
	if err != nil {
		return 0, false
	}
	for _, c := range list {
		if c.T == h && c.O > 0 {
			return (c.H - c.L) / c.O * 100, true
		}
	}
	return 0, false
}

// Service 服务。
type Service struct {
	Bars *candles.Remote
	URL  string
	HTTP *http.Client
	Sim  bool
	Log  *slog.Logger
	// FREDKey 部署的人的 FRED 密钥（FRED_API_KEY）：有了它日程全自动更新。FREDBase 测试用
	FREDKey  string
	keyMu    sync.RWMutex  // 管理后台随时能换 FRED 密钥（2026-11-15）
	kick     chan struct{} // 换了密钥：马上刷一遍，不等 6 小时
	FREDBase string
	fredOK   bool
	fredErr  string

	pool *pgxpool.Pool
	mu   sync.Mutex
	list []Event
	vols map[string]*Vol
	err  string
}

// Attach 建表。
func (s *Service) Attach(ctx context.Context, pool *pgxpool.Pool) error {
	s.pool = pool
	_, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS econ_events (t BIGINT NOT NULL, key TEXT NOT NULL, title TEXT NOT NULL, vol DOUBLE PRECISION, PRIMARY KEY (t, key))`)
	return err
}

func (s *Service) fetch(ctx context.Context) ([]Event, error) {
	url := s.URL
	if url == "" {
		url = "https://www.bea.gov/news/schedule/ics/online-calendar-subscription.ics"
	}
	hc := s.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return ParseICS(resp.Body), nil
}

func fomcEvents() []Event {
	out := make([]Event, 0, len(fomc))
	for _, v := range fomc {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			out = append(out, Event{T: t.UnixMilli(), Key: "fomc", Title: "FOMC"})
		}
	}
	return out
}

// syncDB 存事件、补还没算的振幅、重新算平均、读出前后一段。
func (s *Service) syncDB(ctx context.Context, fresh []Event) {
	if s.pool == nil {
		s.mu.Lock()
		s.list = fresh
		s.mu.Unlock()
		return
	}
	for _, e := range fresh {
		if _, err := s.pool.Exec(ctx, `INSERT INTO econ_events (t, key, title) VALUES ($1, $2, $3) ON CONFLICT (t, key) DO UPDATE SET title = EXCLUDED.title`, e.T, e.Key, e.Title); err != nil {
			return
		}
		// 同一类、同一天以前按别的来源存过（比如 FRED 只有日期，补的时间不准）：只留这一条
		if _, err := s.pool.Exec(ctx, `DELETE FROM econ_events WHERE key = $1 AND t <> $2 AND t > $2 - 43200000 AND t < $2 + 43200000 AND vol IS NULL`, e.Key, e.T); err != nil {
			return
		}
	}
	now := time.Now().UnixMilli()
	// 每类最近 12 次已经过去的、还没算振幅的，补上（算过的存在表里，以后不再请求）
	rows, err := s.pool.Query(ctx, `SELECT t, key FROM (SELECT t, key, vol, row_number() OVER (PARTITION BY key ORDER BY t DESC) AS r FROM econ_events WHERE t < $1) x WHERE r <= 12 AND vol IS NULL`, now-3_600_000)
	if err != nil {
		return
	}
	type todo struct {
		t int64
		k string
	}
	var list []todo
	for rows.Next() {
		var a todo
		if rows.Scan(&a.t, &a.k) == nil {
			list = append(list, a)
		}
	}
	rows.Close()
	for _, a := range list {
		if v, ok := VolAfter(ctx, s.Bars, a.t); ok {
			_, _ = s.pool.Exec(ctx, `UPDATE econ_events SET vol = $1 WHERE t = $2 AND key = $3`, v, a.t, a.k)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
	vols := map[string]*Vol{}
	if rows, err := s.pool.Query(ctx, `SELECT key, count(*), avg(vol) FROM (SELECT key, vol, row_number() OVER (PARTITION BY key ORDER BY t DESC) AS r FROM econ_events WHERE t < $1 AND vol IS NOT NULL) x WHERE r <= 12 GROUP BY key`, now-3_600_000); err == nil {
		for rows.Next() {
			var k string
			var v Vol
			if rows.Scan(&k, &v.N, &v.Mean) == nil {
				vv := v
				vols[k] = &vv
			}
		}
		rows.Close()
	}
	var near []Event
	if rows, err := s.pool.Query(ctx, `SELECT t, key, title FROM econ_events WHERE t BETWEEN $1 AND $2 ORDER BY t`, now-120*86_400_000, now+60*86_400_000); err == nil {
		for rows.Next() {
			var e Event
			if rows.Scan(&e.T, &e.Key, &e.Title) == nil {
				near = append(near, e)
			}
		}
		rows.Close()
	}
	s.mu.Lock()
	s.vols, s.list = vols, near
	s.mu.Unlock()
}

// List 前后一段时间的事件。
func (s *Service) List() ([]Event, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Event, len(s.list))
	for i, e := range s.list {
		e.Vol = s.vols[e.Key]
		out[i] = e
	}
	return out, s.err
}

// Run 一直跑：每 6 小时取一次日程。
func (s *Service) Run(ctx context.Context) {
	if s.Sim {
		now := time.Now().UnixMilli()
		s.mu.Lock()
		s.vols = map[string]*Vol{"fomc": {N: 12, Mean: 1.42}, "pce": {N: 12, Mean: 0.86}, "gdp": {N: 2, Mean: 0.5}}
		s.list = []Event{{T: now - 3_000_000, Key: "pce", Title: "Personal Income and Outlays"}, {T: now + 10_800_000, Key: "fomc", Title: "FOMC"}, {T: now + 90_000_000, Key: "gdp", Title: "GDP (Advance Estimate)"}}
		s.mu.Unlock()
		return
	}
	tick := time.NewTicker(6 * time.Hour)
	defer tick.Stop()
	for {
		c, cancel := context.WithTimeout(ctx, 5*time.Minute)
		list, err := s.fetch(c)
		s.mu.Lock()
		if err != nil {
			s.err = err.Error()
		} else {
			s.err = ""
		}
		s.mu.Unlock()
		if err != nil && s.Log != nil {
			s.Log.Warn("经济日历取不到", "err", err)
		}
		fred, ferr := s.fetchFRED(c)
		s.mu.Lock()
		s.fredOK = s.fredKey() != "" && ferr == nil && len(fred) > 0
		s.fredErr = ""
		if ferr != nil {
			s.fredErr = ferr.Error()
		}
		s.mu.Unlock()
		if ferr != nil && s.Log != nil {
			s.Log.Warn("FRED 的发布日程取不到", "err", ferr)
		}
		// 更准的来源放前面：议息的固定日程、经济分析局的日历文件、内置的劳工统计局日程，最后是 FRED（只有日期）
		s.syncDB(c, merge(fomcEvents(), list, builtinBLS(), fred))
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-s.kickCh():
		}
	}
}

// Routes 挂接口。
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/calendar", func(w http.ResponseWriter, _ *http.Request) {
		list, err := s.List()
		st := s.Status()
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]any{"events": list, "error": err != "", "auto": st.Auto, "until": st.Until, "fred_error": st.FREDErr != ""})
	})
}

// CalStatus 日程会不会自己更新：Auto = FRED 在用；Until = 内置的劳工统计局日程到哪天（毫秒）。
type CalStatus struct {
	Auto    bool
	Until   int64
	FREDErr string
}

// Status 见 CalStatus。
func (s *Service) Status() CalStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return CalStatus{Auto: s.fredOK, Until: BuiltinUntil(), FREDErr: s.fredErr}
}

// SetFREDKey 换 FRED 密钥（管理后台），马上刷一遍日程。
func (s *Service) SetFREDKey(k string) {
	s.keyMu.Lock()
	s.FREDKey = k
	s.keyMu.Unlock()
	select {
	case s.kickCh() <- struct{}{}:
	default:
	}
}

func (s *Service) fredKey() string {
	s.keyMu.RLock()
	defer s.keyMu.RUnlock()
	return s.FREDKey
}

func (s *Service) kickCh() chan struct{} {
	s.keyMu.Lock()
	defer s.keyMu.Unlock()
	if s.kick == nil {
		s.kick = make(chan struct{}, 1)
	}
	return s.kick
}
