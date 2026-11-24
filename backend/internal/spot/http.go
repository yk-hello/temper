package spot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"qs/internal/auth"
)

// Schema 建表。
const Schema = `
create table if not exists spot_events (
  seq     bigint primary key,
  account bigint not null,
  t       bigint not null,
  kind    text   not null,
  body    jsonb  not null
);
create index if not exists spot_events_account on spot_events (account, seq);
`

// Attach 接上数据库：建表、重放、之后的事件写库。
func (e *Engine) Attach(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, Schema); err != nil {
		return err
	}
	rows, err := pool.Query(ctx, `select body from spot_events order by seq`)
	if err != nil {
		return err
	}
	var evs []Event
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			rows.Close()
			return err
		}
		var ev Event
		if err := json.Unmarshal(b, &ev); err != nil {
			rows.Close()
			return err
		}
		evs = append(evs, ev)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	e.Restore(evs)
	e.Persist = func(list []Event) error {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		batch := &pgx.Batch{}
		for _, ev := range list {
			b, _ := json.Marshal(ev)
			batch.Queue(`insert into spot_events (seq,account,t,kind,body) values ($1,$2,$3,$4,$5) on conflict do nothing`, ev.Seq, ev.Account, ev.T, ev.Kind, b)
		}
		return pool.SendBatch(c, batch).Close()
	}
	return nil
}

// Run 撮合和写库。
func (e *Engine) Run(ctx context.Context) {
	match := time.NewTicker(200 * time.Millisecond)
	save := time.NewTicker(500 * time.Millisecond)
	defer match.Stop()
	defer save.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = e.Flush()
			return
		case <-match.C:
			e.Tick()
		case <-save.C:
			if err := e.Flush(); err != nil && e.Log != nil {
				e.Log.Warn("存现货事件失败", "err", err)
			}
		}
	}
}

// Wallet 合约钱包那边：划转时用。
type Wallet interface {
	Transfer(acct int64, amount decimal.Decimal) error
}

// Users 账户服务。
type Users interface {
	Require(http.HandlerFunc) http.HandlerFunc
	OwnsPaper(ctx context.Context, user, acct int64) bool
}

// API 接口。
type API struct {
	E     *Engine
	Perps Wallet
	Users Users
	// Busy 这个账户现在不能划转（在跟 AI：跟随的盈亏按权益算，划转会算错）
	Busy func(acct int64) bool
}

var hugeNumber = regexp.MustCompile(`[:\[,]\s*"?[-+]?(\d[\d.]*[eE][-+]?\d{3,}|\d{40,}|\d*\.\d{40,})"?\s*[,}\]]`)

func read(w http.ResponseWriter, r *http.Request, v any) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<10))
	if err != nil || hugeNumber.Match(body) || json.Unmarshal(body, v) != nil {
		writeErr(w, rej("BAD_REQUEST"))
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	var re *Rej
	if errors.As(err, &re) {
		writeJSON(w, 422, map[string]any{"code": re.Code, "params": re.Params})
		return
	}
	// 纪律锁等其他模块的拒单：都带 Code
	type coded interface {
		RejectCode() string
		RejectParams() map[string]string
	}
	var c coded
	if errors.As(err, &c) {
		writeJSON(w, 422, map[string]any{"code": c.RejectCode(), "params": c.RejectParams()})
		return
	}
	writeJSON(w, 500, map[string]string{"code": "INTERNAL"})
}

// Routes 挂接口。
func (h *API) Routes(mux *http.ServeMux) {
	r := func(pattern string, fn func(w http.ResponseWriter, q *http.Request, acct int64)) {
		mux.HandleFunc(pattern, h.Users.Require(func(w http.ResponseWriter, q *http.Request) {
			u := auth.UserFrom(q.Context())
			acct, err := strconv.ParseInt(q.PathValue("acct"), 10, 64)
			if err != nil || acct <= 0 || u == nil || !h.Users.OwnsPaper(q.Context(), u.ID, acct) {
				writeJSON(w, 404, map[string]string{"code": "NOT_FOUND"})
				return
			}
			fn(w, q, acct)
		}))
	}
	mux.HandleFunc("GET /api/spot/specs", func(w http.ResponseWriter, _ *http.Request) {
		out := make([]*Spec, 0, len(h.E.Specs))
		for _, k := range []string{"BTC-SPOT", "ETH-SPOT", "SOL-SPOT", "BNB-SPOT", "XRP-SPOT", "DOGE-SPOT"} {
			if s := h.E.Specs[k]; s != nil {
				out = append(out, s)
			}
		}
		writeJSON(w, 200, out)
	})
	r("GET /api/spot/{acct}", func(w http.ResponseWriter, _ *http.Request, acct int64) {
		writeJSON(w, 200, h.E.ViewOf(acct))
	})
	r("POST /api/spot/{acct}/orders", func(w http.ResponseWriter, q *http.Request, acct int64) {
		var in OrderReq
		if !read(w, q, &in) {
			return
		}
		o, err := h.E.Place(acct, in)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, o)
	})
	r("DELETE /api/spot/{acct}/orders/{id}", func(w http.ResponseWriter, q *http.Request, acct int64) {
		o, err := h.E.Cancel(acct, q.PathValue("id"), "USER")
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, o)
	})
	r("POST /api/spot/{acct}/transfer", func(w http.ResponseWriter, q *http.Request, acct int64) {
		var in struct {
			To     string          `json:"to"` // spot | perp
			Amount decimal.Decimal `json:"amount"`
		}
		if !read(w, q, &in) {
			return
		}
		if !in.Amount.IsPositive() || (in.To != "spot" && in.To != "perp") {
			writeErr(w, rej("BAD_REQUEST"))
			return
		}
		if h.Busy != nil && h.Busy(acct) {
			writeErr(w, rej("FOLLOWING_NO_TRANSFER"))
			return
		}
		if err := h.transfer(acct, in.To, in.Amount); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, h.E.ViewOf(acct))
	})
}

// transfer 两边要一起成功：先扣转出的那边，另一边失败就退回去。
func (h *API) transfer(acct int64, to string, amt decimal.Decimal) error {
	if to == "spot" {
		if err := h.Perps.Transfer(acct, amt.Neg()); err != nil {
			return err
		}
		if err := h.E.Transfer(acct, amt); err != nil {
			_ = h.Perps.Transfer(acct, amt)
			return err
		}
		return nil
	}
	if err := h.E.Transfer(acct, amt.Neg()); err != nil {
		return err
	}
	if err := h.Perps.Transfer(acct, amt); err != nil {
		_ = h.E.Transfer(acct, amt)
		return err
	}
	return nil
}
