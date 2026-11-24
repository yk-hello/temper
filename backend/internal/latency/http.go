package latency

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"

	"qs/internal/auth"
)

// Default 没选过的用户：跟随实测（模拟盘最接近真实的情况）。
const Default = "measured"

// Prefs 每个用户选的模式，存在数据库，内存里一份。
type Prefs struct {
	pool *pgxpool.Pool
	mu   sync.RWMutex
	m    map[int64]string
	// Owner 账户编号 → 用户（系统账户没有主人，返回 0，按默认）
	Owner func(acct int64) int64
}

func (p *Prefs) Attach(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, `create table if not exists latency_prefs (usr bigint primary key, mode text not null)`); err != nil {
		return err
	}
	rows, err := pool.Query(ctx, `select usr, mode from latency_prefs`)
	if err != nil {
		return err
	}
	defer rows.Close()
	m := map[int64]string{}
	for rows.Next() {
		var u int64
		var mode string
		if rows.Scan(&u, &mode) == nil && Valid(mode) {
			m[u] = mode
		}
	}
	p.mu.Lock()
	p.pool, p.m = pool, m
	p.mu.Unlock()
	return rows.Err()
}

// Of 这个用户选的模式。
func (p *Prefs) Of(user int64) string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if v, ok := p.m[user]; ok {
		return v
	}
	return Default
}

// ForAccount 给 Model.Mode 用。
func (p *Prefs) ForAccount(acct int64) string {
	if p.Owner == nil {
		return Default
	}
	return p.Of(p.Owner(acct))
}

func (p *Prefs) Set(ctx context.Context, user int64, mode string) error {
	p.mu.RLock()
	pool := p.pool
	p.mu.RUnlock()
	if pool != nil {
		if _, err := pool.Exec(ctx, `insert into latency_prefs (usr, mode) values ($1,$2) on conflict (usr) do update set mode=excluded.mode`, user, mode); err != nil {
			return err
		}
	}
	p.mu.Lock()
	if p.m == nil {
		p.m = map[int64]string{}
	}
	p.m[user] = mode
	p.mu.Unlock()
	return nil
}

func (p *Prefs) DeleteUser(ctx context.Context, user int64) {
	p.mu.Lock()
	delete(p.m, user)
	pool := p.pool
	p.mu.Unlock()
	if pool != nil {
		_, _ = pool.Exec(ctx, `delete from latency_prefs where usr=$1`, user)
	}
}

// API GET /api/trade/latency 看现在的模式和各地区的参考值；PUT 改模式。
type API struct {
	M     *Model
	P     *Prefs
	Users *auth.Service
}

func write(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (h *API) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/trade/latency", h.Users.Require(func(w http.ResponseWriter, r *http.Request) {
		write(w, 200, h.M.Info(h.P.Of(auth.UserFrom(r.Context()).ID)))
	}))
	mux.HandleFunc("PUT /api/trade/latency", h.Users.Require(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Mode string `json:"mode"`
		}
		b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024))
		if err != nil || json.Unmarshal(b, &in) != nil || !Valid(in.Mode) {
			write(w, 400, map[string]string{"code": "BAD_REQUEST"})
			return
		}
		u := auth.UserFrom(r.Context())
		if err := h.P.Set(r.Context(), u.ID, in.Mode); err != nil {
			write(w, 500, map[string]string{"code": "INTERNAL_ERROR"})
			return
		}
		write(w, 200, h.M.Info(in.Mode))
	}))
}
