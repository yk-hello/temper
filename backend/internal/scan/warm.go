package scan

// 打开页面不再等外面的接口（2026-11-14，用户："今日等了半天才有显示，至少半分钟……尽可能看不出延迟"）。
//
// 原来"今日"在请求里一个接一个去取十几个外部接口（恐慌贪婪、总市值、多空比、重点币日线、溢价、期权波动、稳定币……），
// 刚启动、或者哪一项缓存过期时，打开页面要等全部取完——用户这边经代理，刚启动实测 49 秒。现在：
//   - 后台按各自的节奏同时去取，请求只读现成的，永远不等；
//   - 上一次取到的、筛选结果和最近的异动存进数据库，重启后马上就有（标着取到的时间），后台更新完再换新的。

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// focusDefault 还没有筛选结果时，先按这几个重点币取日线。
var focusDefault = []string{"BTC", "ETH", "SOL", "BNB", "XRP", "DOGE"}

// source 一项外部数据：键、多久更新一次、怎么取。
type source struct {
	key string
	ttl time.Duration
	get func(ctx context.Context) (any, error)
}

func (s *Service) sources() []source {
	if s.Src == nil {
		return nil
	}
	out := []source{
		{"fear", time.Hour, func(ctx context.Context) (any, error) { return s.Src.FearGreed(ctx) }},
		{"global", 10 * time.Minute, func(ctx context.Context) (any, error) { return s.Src.Global(ctx) }},
		{"lsr", 5 * time.Minute, func(ctx context.Context) (any, error) { return s.Src.LongShort(ctx, "BTCUSDT") }},
	}
	for _, c := range s.focusCoins() {
		c := c
		out = append(out, source{"days|" + c, 10 * time.Minute, func(ctx context.Context) (any, error) { return s.Src.Days(ctx, sym(c), 8) }})
	}
	if x, ok := s.Src.(Extras); ok {
		out = append(out,
			source{"premium", 5 * time.Minute, func(ctx context.Context) (any, error) { return x.Premium(ctx) }},
			source{"iv", 10 * time.Minute, func(ctx context.Context) (any, error) { return x.ImpliedVol(ctx) }},
			source{"stable", time.Hour, func(ctx context.Context) (any, error) { return x.Stable(ctx) }},
			source{"macro", time.Hour, func(ctx context.Context) (any, error) { return x.Macro(ctx) }},
		)
	}
	return out
}

// focusCoins 重点币（筛选结果里标了 F 的）；还没有结果时用默认的几个。
func (s *Service) focusCoins() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, r := range s.rows {
		if r.F {
			out = append(out, r.S)
		}
	}
	if len(out) == 0 {
		return focusDefault
	}
	return out
}

// refreshLoop 后台取外部数据：启动马上取一遍（同时取），之后每 20 秒看一次哪些过期了。
func (s *Service) refreshLoop(ctx context.Context) {
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	for {
		s.refreshDue(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// refreshDue 过期了的都同时去取（每一项最多等 20 秒）；取不到就留着上一次的，下一轮再试。
func (s *Service) refreshDue(ctx context.Context) {
	now := s.now()
	var wg sync.WaitGroup
	for _, src := range s.sources() {
		s.mu.Lock()
		c, ok := s.cache[src.key]
		busy := s.fetching[src.key]
		due := !ok || now.Sub(c.at) >= src.ttl
		if due && !busy {
			if s.fetching == nil {
				s.fetching = map[string]bool{}
			}
			s.fetching[src.key] = true
		}
		s.mu.Unlock()
		if !due || busy {
			continue
		}
		wg.Add(1)
		go func(src source) {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			v, err := src.get(cctx)
			cancel()
			s.mu.Lock()
			delete(s.fetching, src.key)
			if s.tried == nil {
				s.tried = map[string]bool{}
			}
			s.tried[src.key] = true
			if err == nil && v != nil {
				if s.cache == nil {
					s.cache = map[string]cached{}
				}
				s.cache[src.key] = cached{at: s.now(), v: v}
			}
			s.mu.Unlock()
			if err == nil && v != nil {
				s.persist(ctx, "src|"+src.key, v)
			}
		}(src)
	}
	wg.Wait()
}

// peek 只读现成的（不去取）；有没有、算不算"取过但没取到"。
func (s *Service) peek(key string) (v any, ok bool, failed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.cache[key]
	if ok {
		return c.v, true, false
	}
	return nil, false, s.tried[key]
}

// ---------- 存进数据库，重启后马上就有 ----------

// Attach 接上数据库：建表，把上一次的外部数据、筛选结果和异动读回来（内存里已经有更新的就不覆盖）。
func (s *Service) Attach(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, `create table if not exists scan_cache (key text primary key, at timestamptz not null, body jsonb not null)`); err != nil {
		return err
	}
	rows, err := pool.Query(ctx, `select key, at, body from scan_cache`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type item struct {
		key  string
		at   time.Time
		body []byte
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.key, &it.at, &it.body); err != nil {
			return err
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pool = pool
	if s.cache == nil {
		s.cache = map[string]cached{}
	}
	for _, it := range items {
		switch {
		case strings.HasPrefix(it.key, "src|"):
			key := strings.TrimPrefix(it.key, "src|")
			if _, fresh := s.cache[key]; fresh {
				continue
			}
			if v, ok := decodeSource(key, it.body); ok {
				s.cache[key] = cached{at: it.at, v: v}
			}
		case it.key == "rows" && len(s.rows) == 0 && time.Since(it.at) < 6*time.Hour:
			var rs []Row
			if json.Unmarshal(it.body, &rs) == nil {
				s.rows, s.rowsAt = rs, it.at.UnixMilli()
			}
		case it.key == "events" && len(s.events) == 0:
			var es []Event
			if json.Unmarshal(it.body, &es) == nil {
				cut := s.now().Add(-12 * time.Hour).UnixMilli()
				if s.last == nil {
					s.last = map[string]int64{}
				}
				for _, e := range es {
					if e.T < cut {
						continue
					}
					s.events = append(s.events, e)
					if k := e.I + "|" + e.Kind; e.T > s.last[k] { // 冷静期接着算，重启后不重复记
						s.last[k] = e.T
					}
				}
			}
		}
	}
	return nil
}

// decodeSource 按键把存着的 JSON 还原成原来的类型（"今日"里按类型取）。
func decodeSource(key string, b []byte) (any, bool) {
	dec := func(v any) (any, bool) { return v, json.Unmarshal(b, v) == nil }
	switch {
	case key == "fear":
		return dec(&FearGreed{})
	case key == "global":
		return dec(&Global{})
	case key == "lsr":
		return dec(&LongShort{})
	case key == "premium":
		return dec(&Premium{})
	case key == "iv":
		return dec(&ImpliedVol{})
	case key == "stable":
		return dec(&Stable{})
	case key == "macro":
		var m []Macro
		if json.Unmarshal(b, &m) != nil {
			return nil, false
		}
		return m, true
	case strings.HasPrefix(key, "days|"):
		var d []Day
		if json.Unmarshal(b, &d) != nil {
			return nil, false
		}
		return d, true
	}
	return nil, false
}

// persist 存一项（接上数据库以后才存；存不进去不影响页面）。
func (s *Service) persist(ctx context.Context, key string, v any) {
	s.mu.Lock()
	pool := s.pool
	s.mu.Unlock()
	if pool == nil {
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, _ = pool.Exec(cctx, `insert into scan_cache (key, at, body) values ($1, now(), $2) on conflict (key) do update set at = now(), body = excluded.body`, key, b)
}

// persistScreen 每分钟存一次筛选结果和最近 12 小时的异动。
func (s *Service) persistScreen(ctx context.Context) {
	s.mu.Lock()
	if s.pool == nil || s.now().Sub(s.savedAt) < time.Minute || len(s.rows) == 0 {
		s.mu.Unlock()
		return
	}
	s.savedAt = s.now()
	rows := append([]Row(nil), s.rows...)
	cut := s.now().Add(-12 * time.Hour).UnixMilli()
	var events []Event
	for _, e := range s.events {
		if e.T >= cut {
			events = append(events, e)
		}
	}
	s.mu.Unlock()
	go func() {
		s.persist(ctx, "rows", rows)
		if events == nil {
			events = []Event{}
		}
		s.persist(ctx, "events", events)
	}()
}
