package backtest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"qs/internal/auth"
	"qs/internal/binance"
	"qs/internal/trading"
)

// API POST /api/backtest：一次回测（要登录）。每人同时只跑一个，全站同时最多两个。
type API struct {
	Src   Source
	Specs *trading.Specs
	Users *auth.Service

	mu      sync.Mutex
	running map[int64]bool
}

func write(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (h *API) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/backtest", h.Users.Require(h.run))
}

func (h *API) run(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	var in struct {
		Config
		Lines string `json:"lines"` // 信号表（每行"时间,信号"），和 signals 二选一
	}
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
	if err != nil || json.Unmarshal(b, &in) != nil {
		write(w, 400, map[string]string{"code": "BAD_REQUEST"})
		return
	}
	cfg := in.Config
	if in.Lines != "" {
		sigs, err := ParseLines(in.Lines)
		if err != nil {
			var le *LineError
			if errors.As(err, &le) {
				write(w, 400, map[string]any{"code": le.Code, "params": map[string]string{"line": strconv.Itoa(le.Line)}})
				return
			}
			write(w, 400, map[string]string{"code": err.Error()})
			return
		}
		cfg.Signals = sigs
	}
	if err := Prepare(&cfg, func(i string) bool { _, ok := h.Specs.Get(i); return ok }); err != nil {
		write(w, 400, map[string]string{"code": err.Error()})
		return
	}
	h.mu.Lock()
	if h.running == nil {
		h.running = map[int64]bool{}
	}
	if h.running[u.ID] || len(h.running) >= 2 {
		h.mu.Unlock()
		write(w, 429, map[string]string{"code": "BT_BUSY"})
		return
	}
	h.running[u.ID] = true
	h.mu.Unlock()
	defer func() { h.mu.Lock(); delete(h.running, u.ID); h.mu.Unlock() }()
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Minute)
	defer cancel()
	res, err := Run(ctx, cfg, h.Src, h.Specs)
	if err != nil {
		code := "BT_DATA_FAILED"
		for _, e := range []error{ErrNoData, ErrUnknown, ErrRange} {
			if errors.Is(err, e) {
				code = e.Error()
			}
		}
		if errors.Is(err, context.DeadlineExceeded) {
			code = "BT_TIMEOUT"
		}
		if t, ok := binance.Throttled(err); ok { // 币安限流期间：说清楚要等多久
			write(w, 503, map[string]any{"code": "EXCHANGE_THROTTLED", "params": map[string]string{"min": strconv.Itoa(t.Minutes())}})
			return
		}
		write(w, 502, map[string]string{"code": code})
		return
	}
	write(w, 200, res)
}
