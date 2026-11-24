package highlight

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"qs/internal/auth"
)

// Ev 视频时间轴上的一个小记号：持仓期间的经济事件、链上大户动作、大额爆仓。只用我们自己的数据（D88 第 3 条）。
type Ev struct {
	T    int64   `json:"t"`
	Kind string  `json:"kind"`          // econ | whale | liq
	Key  string  `json:"key,omitempty"` // 经济事件：fomc | cpi | nfp ...；大户：open | add | reduce | close | flip
	Side string  `json:"side,omitempty"`
	USD  float64 `json:"usd,omitempty"`
}

// Demo "看示例"用的一段行情：从录到的数据里挑出来的，全程写"演示"（D91 第 5 条）。
type Demo struct {
	Instrument string  `json:"instrument"`
	Side       string  `json:"side"`
	From       int64   `json:"from"`
	To         int64   `json:"to"`
	Entry      float64 `json:"entry"`
	Exit       float64 `json:"exit"`
	Move       float64 `json:"move"` // 价格走了多少（比例，顺着方向是正的）
}

// SceneSrc 视频要的事件。由 main 接上经济日历、链上扫描、爆仓流。
type SceneSrc struct {
	Econ   func(from, to int64) []Ev
	Whales func(coin string, from, to int64) []Ev
	Liqs   func(inst string, from, to int64) []Ev
	Demo   func(ctx context.Context) (*Demo, error)
	// Candles 开仓到平仓的 K 线，周期自动选到 30–60 根；短单用逐笔成交合成。第二个返回值是周期（毫秒）
	Candles func(ctx context.Context, inst string, from, to int64) ([]Candle, int64)
}

// API 生涯档案的接口。
type API struct {
	Users *auth.Service
	Gate  *auth.Gate
	S     *Store
	Scene SceneSrc
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func code(w http.ResponseWriter, status int, c string) {
	writeJSON(w, status, map[string]string{"error": c})
}

func (h *API) route(mux *http.ServeMux, pattern string, fn func(w http.ResponseWriter, r *http.Request, u *auth.User)) {
	mux.HandleFunc(pattern, func(w http.ResponseWriter, req *http.Request) {
		if (h.Gate != nil && !h.Gate.IsOpen()) || !h.S.Ready() {
			w.Header().Set("Retry-After", "5")
			code(w, 503, "STARTING")
			return
		}
		h.Users.Require(func(w http.ResponseWriter, r *http.Request) { fn(w, r, auth.UserFrom(r.Context())) })(w, req)
	})
}

func (h *API) fail(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrNotFound) {
		code(w, 404, "NOT_FOUND")
		return
	}
	code(w, 500, "INTERNAL_ERROR")
}

func coinOf(inst string) string {
	return strings.TrimSuffix(strings.TrimSuffix(inst, "-PERP"), "-SPOT")
}

// events 这段时间里的小记号，按时间排，最多 6 个。
func (h *API) events(inst string, from, to int64) []Ev {
	out := []Ev{}
	if h.Scene.Econ != nil {
		out = append(out, h.Scene.Econ(from, to)...)
	}
	if h.Scene.Whales != nil {
		out = append(out, h.Scene.Whales(coinOf(inst), from, to)...)
	}
	if h.Scene.Liqs != nil {
		out = append(out, h.Scene.Liqs(inst, from, to)...)
	}
	return Pick(out, 6)
}

func (h *API) candles(ctx context.Context, inst string, from, to int64) ([]Candle, int64) {
	if h.Scene.Candles == nil {
		return []Candle{}, 60_000
	}
	c, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cs, iv := h.Scene.Candles(c, inst, from, to)
	if cs == nil {
		cs = []Candle{}
	}
	return cs, iv
}

// Routes 挂接口。
func (h *API) Routes(mux *http.ServeMux) {
	h.route(mux, "GET /api/highlights", func(w http.ResponseWriter, r *http.Request, u *auth.User) {
		list, err := h.S.List(r.Context(), u.ID)
		if err != nil {
			h.fail(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"items": list, "min_roe": h.S.Min(r.Context(), u.ID), "default_min": DefaultMin, "floor": MinFloor, "ceil": MinCeil})
	})
	h.route(mux, "POST /api/highlights/prefs", func(w http.ResponseWriter, r *http.Request, u *auth.User) {
		var b struct {
			MinROE float64 `json:"min_roe"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&b) != nil || b.MinROE <= 0 {
			code(w, 400, "BAD_REQUEST")
			return
		}
		v, err := h.S.SetMin(r.Context(), u.ID, b.MinROE)
		if err != nil {
			h.fail(w, err)
			return
		}
		writeJSON(w, 200, map[string]float64{"min_roe": v})
	})
	h.route(mux, "DELETE /api/highlights/{id}", func(w http.ResponseWriter, r *http.Request, u *auth.User) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			code(w, 400, "BAD_REQUEST")
			return
		}
		if err := h.S.Delete(r.Context(), u.ID, id); err != nil {
			h.fail(w, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	h.route(mux, "GET /api/highlights/{id}/scene", func(w http.ResponseWriter, r *http.Request, u *auth.User) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			code(w, 400, "BAD_REQUEST")
			return
		}
		x, err := h.S.Get(r.Context(), u.ID, id)
		if err != nil {
			h.fail(w, err)
			return
		}
		cs, iv := h.candles(r.Context(), x.Snap.Instrument, x.Snap.OpenTime, x.Snap.CloseTime)
		writeJSON(w, 200, map[string]any{"events": h.events(x.Snap.Instrument, x.Snap.OpenTime, x.Snap.CloseTime), "candles": cs, "iv": iv})
	})
	h.route(mux, "GET /api/highlights/demo", func(w http.ResponseWriter, r *http.Request, u *auth.User) {
		if h.Scene.Demo == nil {
			code(w, 404, "NO_DEMO")
			return
		}
		c, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		d, err := h.Scene.Demo(c)
		if err != nil || d == nil {
			code(w, 404, "NO_DEMO")
			return
		}
		cs, iv := h.candles(r.Context(), d.Instrument, d.From, d.To)
		writeJSON(w, 200, map[string]any{"demo": d, "events": h.events(d.Instrument, d.From, d.To), "candles": cs, "iv": iv})
	})
}
