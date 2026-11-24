// Package system 系统控制台（可视化 11）：数据管线现在的真实数字，每条连接的延迟、每秒消息数、正在处理的信号。
package system

import (
	"context"
	"encoding/json"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"qs/internal/market"
)

// Poll 定时去取的数据源（不是长连接）。
type Poll struct {
	ID string  `json:"id"`
	Ms float64 `json:"ms,omitempty"` // 上一次请求花了多久
	OK bool    `json:"ok"`
	At int64   `json:"at,omitempty"` // 上一次成功的时间
	N  int     `json:"n,omitempty"`  // 这一轮的数量（比如扫描了多少地址）
}

// Service 服务。计数都是函数，取不到就是 0。
type Service struct {
	Polls   func() []Poll
	Meter   *market.Meter
	Hub     *market.Hub
	Pool    func() *pgxpool.Pool
	Books   func() int
	Heat    func() int
	Bots    func() int
	Algos   func() int
	Follows func() int
	Signals func() int64
	Started time.Time

	pollAt  time.Time
	mu      sync.Mutex
	polls   []Poll
	sigAt   time.Time
	sigLast int64
	sigRate float64
}

// Snapshot 返回给前端的一帧。
type Snapshot struct {
	T       int64             `json:"t"`
	Lines   []market.LineStat `json:"lines"`
	Polls   []Poll            `json:"polls"`
	Up      map[string]bool   `json:"up"`
	Rec     int               `json:"reconnects"`
	Clients int               `json:"clients"`
	Books   int               `json:"books"`
	Heat    int               `json:"heat"`
	Bots    int               `json:"bots"`
	Algos   int               `json:"algos"`
	Follows int               `json:"follows"`
	SigMin  float64           `json:"signals_min"`
	DB      *DB               `json:"db,omitempty"`
	Run     Runtime           `json:"runtime"`
}

// DB 数据库。
type DB struct {
	PingMs float64 `json:"ping_ms"`
	Busy   int32   `json:"busy"`
	Total  int32   `json:"total"`
}

// Runtime 进程。
type Runtime struct {
	Goroutines int     `json:"goroutines"`
	HeapMB     float64 `json:"heap_mb"`
	GcMs       float64 `json:"gc_ms"`
	UpSec      int64   `json:"up_sec"`
}

func call(f func() int) int {
	if f == nil {
		return 0
	}
	return f()
}

// Now 现在的一帧。
func (s *Service) Now(ctx context.Context) Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.Hub.Status()
	out := Snapshot{T: time.Now().UnixMilli(), Lines: s.Meter.Stats(), Up: st.Connections, Rec: st.Reconnects, Clients: s.Hub.Subscribers(),
		Books: call(s.Books), Heat: call(s.Heat), Bots: call(s.Bots), Algos: call(s.Algos), Follows: call(s.Follows)}
	if s.Signals != nil {
		n, now := s.Signals(), time.Now()
		if s.sigAt.IsZero() {
			s.sigAt, s.sigLast = now, n
		} else if dt := now.Sub(s.sigAt); dt >= time.Minute {
			s.sigRate = float64(n-s.sigLast) / dt.Minutes()
			s.sigAt, s.sigLast = now, n
		}
		out.SigMin = s.sigRate
	}
	if s.Polls != nil {
		if now := time.Now(); now.Sub(s.pollAt) >= 5*time.Second { // 链上要遍历持仓，不用每秒算
			s.polls, s.pollAt = s.Polls(), now
		}
		out.Polls = s.polls
	}
	if s.Pool != nil {
		if p := s.Pool(); p != nil {
			c, cancel := context.WithTimeout(ctx, time.Second)
			t0 := time.Now()
			if err := p.Ping(c); err == nil {
				ps := p.Stat()
				out.DB = &DB{PingMs: float64(time.Since(t0).Microseconds()) / 1000, Busy: ps.AcquiredConns(), Total: ps.TotalConns()}
			}
			cancel()
		}
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	out.Run = Runtime{Goroutines: runtime.NumGoroutine(), HeapMB: float64(ms.HeapAlloc) / 1048576, GcMs: float64(ms.PauseNs[(ms.NumGC+255)%256]) / 1e6, UpSec: int64(time.Since(s.Started).Seconds())}
	return out
}

// Routes 挂接口。
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/system", func(w http.ResponseWriter, q *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(s.Now(q.Context()))
	})
}
