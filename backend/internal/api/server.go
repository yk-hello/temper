// Package api 是给前端用的 HTTP 和 WebSocket 接口。
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"qs/internal/book"
	"qs/internal/candles"
	"qs/internal/market"
)

type Server struct {
	Hub            *market.Hub
	DBStatus       func() string
	ExchangeRTT    func() (ms float64, ok bool) // 这台服务器到交易所的往返时间（模拟下单延迟用，D94）
	BrandName      string
	AllowedOrigins []string
	Log            *slog.Logger
	Started        time.Time
	// 交易相关（前端的 AI 决策流、组合、策略状态），返回值直接序列化成 JSON
	Strategies   func() any
	Portfolio    func(id string, full bool) (any, bool) // full：开启服务的人，带持仓
	Decisions    func(id string) (any, bool)
	Trades       func(id string) (any, bool)
	Calibration  func(id string) (any, bool)
	Access       func(r *http.Request) (loggedIn, open bool)                  // 登录了吗、开启服务了吗
	Performance  func(id string) (any, bool)                                  // 公开的战绩：净值、汇总、已平仓的每笔交易
	Ensure       func(instrument string) bool                                 // 开始收这个品种的行情（用户点开了它）
	OpenInterest func(ctx context.Context, instrument string) (float64, bool) // 持仓量（USDT）
	MarketMap    http.Handler                                                 // 全市场方块图的数据
	Signals      func(n int) any
	// 行情页：盘口（按连接订阅一个品种）和 K 线
	Books   *book.Store
	Candles *candles.Store
	Primary string
}

func (s *Server) Handler() http.Handler { return s.routes() }

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/brand", s.brand)
	mux.HandleFunc("GET /api/market/snapshot", s.snapshot)
	mux.HandleFunc("GET /ws", s.ws)
	mux.HandleFunc("GET /api/strategies", func(w http.ResponseWriter, _ *http.Request) { s.json(w, 200, s.Strategies()) })
	// 公开：净值和汇总。还没平仓的持仓只给开启服务的人（D75 第 17、18 条，12 的 3.13）
	mux.HandleFunc("GET /api/strategies/{id}", func(w http.ResponseWriter, r *http.Request) {
		_, open := s.access(r)
		if v, ok := s.Portfolio(r.PathValue("id"), open); ok {
			s.json(w, 200, v)
			return
		}
		s.json(w, 404, map[string]string{"error": "没有这个策略"})
	})
	mux.HandleFunc("GET /api/strategies/{id}/performance", func(w http.ResponseWriter, r *http.Request) {
		if s.Performance != nil {
			if v, ok := s.Performance(r.PathValue("id")); ok {
				s.json(w, 200, v)
				return
			}
		}
		s.json(w, 404, map[string]string{"error": "没有这个策略"})
	})
	// 只给开启服务的人：没登录 401，登录了没开启 403
	gated := func(pattern string, fn func(r *http.Request) (any, bool)) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			in, open := s.access(r)
			if !in {
				s.json(w, 401, map[string]string{"code": "NOT_LOGGED_IN"})
				return
			}
			if !open {
				s.json(w, 403, map[string]string{"code": "FOLLOW_REQUIRED"})
				return
			}
			v, ok := fn(r)
			if !ok {
				s.json(w, 404, map[string]string{"error": "没有这个策略"})
				return
			}
			s.json(w, 200, v)
		})
	}
	gated("GET /api/signals", func(*http.Request) (any, bool) { return s.Signals(100), true })
	gated("GET /api/strategies/{id}/decisions", func(r *http.Request) (any, bool) { return s.call(s.Decisions, r) })
	gated("GET /api/strategies/{id}/trades", func(r *http.Request) (any, bool) { return s.call(s.Trades, r) })
	gated("GET /api/strategies/{id}/calibration", func(r *http.Request) (any, bool) { return s.call(s.Calibration, r) })
	mux.HandleFunc("GET /api/market/oi", func(w http.ResponseWriter, r *http.Request) {
		if s.OpenInterest == nil {
			s.json(w, 404, map[string]string{"error": "没有"})
			return
		}
		v, ok := s.OpenInterest(r.Context(), r.URL.Query().Get("instrument"))
		if !ok {
			s.json(w, 404, map[string]string{"error": "没有"})
			return
		}
		s.json(w, 200, map[string]float64{"value": v})
	})
	mux.HandleFunc("GET /api/marketmap", func(w http.ResponseWriter, r *http.Request) {
		if s.MarketMap == nil {
			s.json(w, 404, map[string]string{"error": "没有"})
			return
		}
		s.MarketMap.ServeHTTP(w, r)
	})
	mux.HandleFunc("GET /api/candles", s.candles)
	return mux
}

type health struct {
	Status   string              `json:"status"`
	Brand    string              `json:"brand"`
	DB       string              `json:"db"`
	Market   market.SourceStatus `json:"market"`
	UptimeS  int64               `json:"uptime_s"`
	ServerTs int64               `json:"server_ts"`
	RTT      *float64            `json:"exchange_rtt_ms,omitempty"`
}

// access 这个请求：登录了吗、开启服务了吗。没配置时一律没有（默认关着）。
func (s *Server) access(r *http.Request) (loggedIn, open bool) {
	if s.Access == nil {
		return false, false
	}
	return s.Access(r)
}

func (s *Server) call(fn func(string) (any, bool), r *http.Request) (any, bool) {
	if fn == nil {
		return nil, false
	}
	return fn(r.PathValue("id"))
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	st := s.Hub.Status()
	h := health{Status: "ok", Brand: s.BrandName, DB: s.DBStatus(), Market: st,
		UptimeS: int64(time.Since(s.Started).Seconds()), ServerTs: time.Now().UnixMilli()}
	if s.ExchangeRTT != nil {
		if v, ok := s.ExchangeRTT(); ok {
			h.RTT = &v
		}
	}
	if h.DB != "ok" && h.DB != "disabled" || !st.Connected() {
		h.Status = "degraded"
	}
	writeJSON(w, http.StatusOK, h)
}

func (s *Server) brand(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"name": s.BrandName})
}

func (s *Server) snapshot(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Hub.Snapshot())
}

// ws 推送行情帧。连接建立后先发一帧完整快照，之后每次合并后的变化都推。
func (s *Server) ws(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: s.AllowedOrigins})
	if err != nil {
		return
	}
	defer c.CloseNow()
	// 客户端可以发 {"book":"BTC-PERP"} 订阅一个品种的盘口（前 20 档），每 200 毫秒推一次（有变化才推）
	ctx, cancelRead := context.WithCancel(r.Context())
	defer cancelRead()
	var bookInst atomic.Value
	bookInst.Store("")
	go func() {
		defer cancelRead()
		for {
			_, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			var m struct {
				Book string `json:"book"`
			}
			if json.Unmarshal(data, &m) == nil {
				if m.Book != "" && s.Ensure != nil {
					s.Ensure(m.Book) // 点开的币可能还没在收行情
				}
				bookInst.Store(m.Book)
			}
		}
	}()
	bookTick := time.NewTicker(200 * time.Millisecond)
	defer bookTick.Stop()
	var lastBook int64
	var lastInst string

	sub, cancel := s.Hub.Subscribe()
	defer cancel()

	first, _ := json.Marshal(s.Hub.Snapshot())
	if err := write(ctx, c, first); err != nil {
		return
	}
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			c.Close(websocket.StatusNormalClosure, "")
			return
		case data := <-sub.C:
			if err := write(ctx, c, data); err != nil {
				return
			}
		case <-bookTick.C:
			inst, _ := bookInst.Load().(string)
			if inst == "" || s.Books == nil {
				continue
			}
			b, ok := s.Books.Book(s.Primary, inst)
			if !ok || (b.Time == lastBook && inst == lastInst) {
				continue
			}
			lastBook, lastInst = b.Time, inst
			if err := write(ctx, c, bookFrame(b)); err != nil {
				return
			}
		case <-ping.C:
			pctx, pcancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.Ping(pctx)
			pcancel()
			if err != nil {
				return
			}
		}
	}
}

func write(ctx context.Context, c *websocket.Conn, data []byte) error {
	wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return c.Write(wctx, websocket.MessageText, data)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) json(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func bookFrame(b book.Book) []byte {
	lv := func(ls []book.Level) [][2]string {
		out := make([][2]string, 0, len(ls))
		for _, l := range ls {
			out = append(out, [2]string{l.Px.String(), l.Qty.String()})
		}
		return out
	}
	data, _ := json.Marshal(map[string]any{"type": "book", "instrument": b.Instrument, "venue": b.Venue, "ts": b.Time, "bids": lv(b.Bids), "asks": lv(b.Asks)})
	return data
}

// GET /api/candles?instrument=BTC-PERP&interval=1s|1m|3m|5m|15m|30m|1h|2h|4h|1d[&since=毫秒 | &before=毫秒][&limit=300]
//
//	since：从这一根开始到最新（实时刷新）；before：这之前的 limit 根（往左拖看历史）；都不给：最新 limit 根
func (s *Server) candles(w http.ResponseWriter, r *http.Request) {
	if s.Ensure != nil {
		if inst := r.URL.Query().Get("instrument"); inst != "" {
			s.Ensure(inst)
		}
	}
	if s.Candles == nil {
		s.json(w, 200, []any{})
		return
	}
	q := r.URL.Query()
	iv, ok := candles.Intervals[q.Get("interval")]
	if !ok {
		iv = 1000
	}
	since, _ := strconv.ParseInt(q.Get("since"), 10, 64)
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	s.json(w, 200, s.Candles.Query(r.Context(), q.Get("instrument"), iv, since, before, limit))
}
