package discipline

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"qs/internal/auth"
)

// Err 拒绝操作。Code 由前端按语言翻译。
type Err struct {
	HTTP int    `json:"-"`
	Code string `json:"code"`
}

func (e *Err) Error() string { return e.Code }

func errOf(status int, code string) *Err { return &Err{HTTP: status, Code: code} }

// Store 纪律事件表（只追加）。
type Store struct {
	Pool *pgxpool.Pool
}

const storeSchema = `
create table if not exists discipline_events (
  seq     bigint primary key,
  account bigint not null,
  usr     bigint not null,
  t       bigint not null,
  kind    text   not null,
  body    jsonb  not null
);
create index if not exists discipline_events_account on discipline_events (account, seq);
`

// Migrate 建表。
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
		b.Queue(`insert into discipline_events (seq, account, usr, t, kind, body) values ($1,$2,$3,$4,$5,$6) on conflict (seq) do nothing`,
			ev.Seq, ev.Account, ev.User, ev.Time, ev.Kind, body)
	}
	return s.Pool.SendBatch(ctx, b).Close()
}

// Load 读出全部事件。
func (s *Store) Load(ctx context.Context) ([]Event, error) {
	rows, err := s.Pool.Query(ctx, `select body from discipline_events order by seq`)
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

// API 纪律锁接口。权限全部在服务器判断：没登录 401，别人的账户一律 404。
type API struct {
	M     *Manager
	Users *auth.Service
	Gate  *auth.Gate
}

// Routes 注册接口。
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
	r("GET /api/discipline", func(w http.ResponseWriter, req *http.Request, u *auth.User) {
		if tz, err := strconv.Atoi(req.URL.Query().Get("tz")); err == nil {
			h.M.SetTZ(u.ID, tz)
		}
		writeJSON(w, 200, h.M.View(u.ID))
	})
	r("PUT /api/discipline/rules", func(w http.ResponseWriter, req *http.Request, u *auth.User) {
		var in struct {
			Rules         Rules  `json:"rules"`
			TradePassword string `json:"trade_password"`
		}
		if err := json.NewDecoder(req.Body).Decode(&in); err != nil {
			writeErr(w, errOf(400, "BAD_REQUEST"))
			return
		}
		if err := in.Rules.Validate(); err != nil {
			writeErr(w, err)
			return
		}
		if h.M.NeedsPassword(u.ID, in.Rules) {
			if err := h.Users.CheckTradePassword(u, in.TradePassword); err != nil {
				writeAuthErr(w, err)
				return
			}
		}
		if err := h.M.SetRules(u.ID, in.Rules); err != nil {
			writeErr(w, err)
			return
		}
		h.M.Flush()
		writeJSON(w, 200, h.M.View(u.ID))
	})
	r("POST /api/discipline/enabled", func(w http.ResponseWriter, req *http.Request, u *auth.User) {
		var in struct {
			On            bool   `json:"on"`
			TradePassword string `json:"trade_password"`
		}
		if err := json.NewDecoder(req.Body).Decode(&in); err != nil {
			writeErr(w, errOf(400, "BAD_REQUEST"))
			return
		}
		if !in.On {
			if h.M.IsLocked(u.ID) { // 先看这个，锁定中输密码也没用，不消耗次数
				writeErr(w, errOf(409, "DISCIPLINE_IS_LOCKED"))
				return
			}
			if err := h.Users.CheckTradePassword(u, in.TradePassword); err != nil {
				writeAuthErr(w, err)
				return
			}
		}
		if err := h.M.SetEnabled(u.ID, in.On); err != nil {
			writeErr(w, err)
			return
		}
		h.M.Flush()
		writeJSON(w, 200, h.M.View(u.ID))
	})
	r("POST /api/discipline/{acct}/unlock", func(w http.ResponseWriter, req *http.Request, u *auth.User) {
		acct, err := strconv.ParseInt(req.PathValue("acct"), 10, 64)
		if err != nil {
			writeErr(w, errOf(404, "NOT_FOUND"))
			return
		}
		var in struct {
			TradePassword string `json:"trade_password"`
		}
		if err := json.NewDecoder(req.Body).Decode(&in); err != nil {
			writeErr(w, errOf(400, "BAD_REQUEST"))
			return
		}
		if err := h.M.CanUnlock(u.ID, acct); err != nil {
			writeErr(w, err)
			return
		}
		if err := h.Users.CheckTradePassword(u, in.TradePassword); err != nil {
			writeAuthErr(w, err)
			return
		}
		if err := h.M.RequestUnlock(u.ID, acct); err != nil {
			writeErr(w, err)
			return
		}
		h.M.Flush()
		writeJSON(w, 200, h.M.View(u.ID))
	})
	r("GET /api/discipline/{acct}/history", func(w http.ResponseWriter, req *http.Request, u *auth.User) {
		acct, err := strconv.ParseInt(req.PathValue("acct"), 10, 64)
		if err != nil {
			writeErr(w, errOf(404, "NOT_FOUND"))
			return
		}
		list, err := h.M.History(u.ID, acct)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"events": list})
	})
}

// IsLocked 这个用户有账户正在锁定吗。
func (m *Manager) IsLocked(user int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lockedAny(user)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	var e *Err
	if errors.As(err, &e) {
		writeJSON(w, e.HTTP, map[string]string{"code": e.Code})
		return
	}
	writeJSON(w, 500, map[string]string{"code": "INTERNAL_ERROR"})
}

// writeAuthErr 二级密码的错误（需要设置、不对、错太多次）。
func writeAuthErr(w http.ResponseWriter, err error) {
	var e *auth.Err
	if errors.As(err, &e) {
		writeJSON(w, e.HTTP, map[string]string{"code": e.Code})
		return
	}
	writeJSON(w, 500, map[string]string{"code": "INTERNAL_ERROR"})
}
