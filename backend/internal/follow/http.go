package follow

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"qs/internal/auth"
)

// Store 跟随事件表（只追加）。
type Store struct {
	Pool *pgxpool.Pool
}

const storeSchema = `
create table if not exists follow_events (
  seq    bigint primary key,
  follow bigint not null,
  t      bigint not null,
  kind   text   not null,
  body   jsonb  not null
);
create index if not exists follow_events_follow on follow_events (follow, seq);
`

func (s *Store) Migrate(ctx context.Context) error {
	_, err := s.Pool.Exec(ctx, storeSchema)
	return err
}

// Append 按顺序写入；重试时已写过的跳过。
func (s *Store) Append(ctx context.Context, evs []Event) error {
	b := &pgx.Batch{}
	for _, ev := range evs {
		body, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		b.Queue(`insert into follow_events (seq, follow, t, kind, body) values ($1,$2,$3,$4,$5) on conflict (seq) do nothing`, ev.Seq, ev.Follow, ev.Time, ev.Kind, body)
	}
	return s.Pool.SendBatch(ctx, b).Close()
}

func (s *Store) Load(ctx context.Context) ([]Event, error) {
	rows, err := s.Pool.Query(ctx, `select body from follow_events order by seq`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Event, error) {
		var raw []byte
		var ev Event
		if err := r.Scan(&raw); err != nil {
			return ev, err
		}
		err := json.Unmarshal(raw, &ev)
		return ev, err
	})
}

// API 跟随接口。权限全部在服务器判断：没登录 401，别人的跟随一律 404（3.11）。
type API struct {
	M     *Manager
	Users *auth.Service
	Gate  *auth.Gate
}

func (h *API) Routes(mux *http.ServeMux) {
	r := func(pattern string, fn func(w http.ResponseWriter, r *http.Request, u *auth.User)) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, req *http.Request) {
			if h.Gate != nil && !h.Gate.IsOpen() {
				w.Header().Set("Retry-After", "5")
				writeJSON(w, 503, map[string]string{"code": "STARTING"})
				return
			}
			h.Users.Require(func(w http.ResponseWriter, req *http.Request) {
				fn(w, req, auth.UserFrom(req.Context()))
			})(w, req)
		})
	}
	withID := func(fn func(w http.ResponseWriter, r *http.Request, u *auth.User, id int64)) func(http.ResponseWriter, *http.Request, *auth.User) {
		return func(w http.ResponseWriter, req *http.Request, u *auth.User) {
			id, err := strconv.ParseInt(req.PathValue("id"), 10, 64)
			if err != nil {
				writeErr(w, rej(404, "NOT_FOUND"))
				return
			}
			fn(w, req, u, id)
		}
	}
	r("GET /api/follows", func(w http.ResponseWriter, _ *http.Request, u *auth.User) {
		writeJSON(w, 200, map[string]any{"follows": h.M.List(u)})
	})
	r("POST /api/follows", func(w http.ResponseWriter, req *http.Request, u *auth.User) {
		var in StartReq
		if err := json.NewDecoder(req.Body).Decode(&in); err != nil {
			writeErr(w, rej(400, "BAD_REQUEST"))
			return
		}
		v, err := h.M.Start(req.Context(), u, in)
		if err != nil {
			writeErr(w, err)
			return
		}
		h.M.Flush()
		writeJSON(w, 200, v)
	})
	// 这个策略能不能接到我的这几个交易所账户上（只能是自己的策略，D99）：向导用它决定哪些交易所账户能选、为什么不能选
	// GET /api/follows/live-ok?strategy=x&accounts=1000000000001,1000000000002 → {"accounts": {"1000000000001": "", "1000000000002": "LIVE_OWN_STRATEGY"}}
	r("GET /api/follows/live-ok", func(w http.ResponseWriter, req *http.Request, u *auth.User) {
		var ids []int64
		for _, s := range strings.Split(req.URL.Query().Get("accounts"), ",") {
			if id, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil && len(ids) < 20 {
				ids = append(ids, id)
			}
		}
		out := map[string]string{}
		for id, code := range h.M.LiveCheck(req.Context(), u, req.URL.Query().Get("strategy"), ids) {
			out[strconv.FormatInt(id, 10)] = code
		}
		writeJSON(w, 200, map[string]any{"accounts": out})
	})
	r("GET /api/follows/{id}", withID(func(w http.ResponseWriter, _ *http.Request, u *auth.User, id int64) {
		v, err := h.M.Get(u, id)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, v)
	}))
	r("PATCH /api/follows/{id}", withID(func(w http.ResponseWriter, req *http.Request, u *auth.User, id int64) {
		var in StartReq
		if err := json.NewDecoder(req.Body).Decode(&in); err != nil {
			writeErr(w, rej(400, "BAD_REQUEST"))
			return
		}
		v, err := h.M.Update(u, id, in)
		if err != nil {
			writeErr(w, err)
			return
		}
		h.M.Flush()
		writeJSON(w, 200, v)
	}))
	r("POST /api/follows/{id}/pause", withID(func(w http.ResponseWriter, _ *http.Request, u *auth.User, id int64) {
		v, err := h.M.Pause(u, id)
		if err != nil {
			writeErr(w, err)
			return
		}
		h.M.Flush()
		writeJSON(w, 200, v)
	}))
	r("GET /api/follows/{id}/resume-plan", withID(func(w http.ResponseWriter, _ *http.Request, u *auth.User, id int64) {
		v, err := h.M.ResumePlan(u, id)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"trades": v})
	}))
	r("POST /api/follows/{id}/resume", withID(func(w http.ResponseWriter, _ *http.Request, u *auth.User, id int64) {
		v, err := h.M.Resume(u, id)
		if err != nil {
			writeErr(w, err)
			return
		}
		h.M.Flush()
		writeJSON(w, 200, v)
	}))
	r("POST /api/follows/{id}/stop", withID(func(w http.ResponseWriter, req *http.Request, u *auth.User, id int64) {
		var in struct {
			Mode string `json:"mode"` // close | keep
		}
		if err := json.NewDecoder(req.Body).Decode(&in); err != nil || (in.Mode != "close" && in.Mode != "keep") {
			writeErr(w, rej(400, "BAD_STOP_MODE"))
			return
		}
		v, err := h.M.Stop(u, id, in.Mode == "close")
		if err != nil {
			writeErr(w, err)
			return
		}
		h.M.Flush()
		writeJSON(w, 200, v)
	}))
}

func writeErr(w http.ResponseWriter, err error) {
	var e *Err
	if errors.As(err, &e) {
		writeJSON(w, e.HTTP, e)
		return
	}
	writeJSON(w, 500, map[string]string{"code": "INTERNAL_ERROR"})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
