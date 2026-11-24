package trading

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"qs/internal/auth"
)

// API 交易接口。只认会话；每个请求都先检查模拟账户属于当前用户（不属于一律 404，验收 2.17）。
type API struct {
	Engine *Engine
	Users  *auth.Service
	Gate   *auth.Gate
	// 每个用户每秒最多 10 个下单类操作
	RatePerSec int
	// 手动操作之后调用：跟随层马上对账。inst 不为空表示手动改了这个品种的止盈止损
	OnManual func(acct int64, inst string)
	// Live 实盘交易台（账户编号从 LiveBase 起）；LiveOwns 查实盘账户是不是这个人的。没接实盘时都是 nil
	Live     Desk
	LiveOwns func(ctx context.Context, user, acct int64) bool

	mu      sync.Mutex
	buckets map[int64]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

func (h *API) allow(user int64) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.buckets == nil {
		h.buckets = map[int64]*bucket{}
	}
	rate := float64(h.RatePerSec)
	if rate <= 0 {
		rate = 10
	}
	now := h.Engine.Now()
	b := h.buckets[user]
	if b == nil {
		b = &bucket{tokens: rate, last: now}
		h.buckets[user] = b
	}
	b.tokens = min(rate, b.tokens+now.Sub(b.last).Seconds()*rate)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// LiveBase 实盘账户的编号从这里开始（模拟账户是小数字，不会撞上）。
const LiveBase int64 = 1_000_000_000_000

func (h *API) owns(ctx context.Context, user, acct int64) bool {
	if acct >= LiveBase {
		return h.Live != nil && h.LiveOwns != nil && h.LiveOwns(ctx, user, acct)
	}
	return h.Users.OwnsPaper(ctx, user, acct) && h.Engine.Has(acct)
}

// d 这个账户用哪个交易台：实盘账户走交易所，其他走模拟引擎。
func (h *API) d(acct int64) Desk {
	if acct >= LiveBase && h.Live != nil {
		return h.Live
	}
	return h.Engine
}

func (h *API) Routes(mux *http.ServeMux) {
	r := func(pattern string, limited bool, fn func(w http.ResponseWriter, r *http.Request, acct int64)) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, req *http.Request) {
			if h.Gate != nil && !h.Gate.IsOpen() {
				w.Header().Set("Retry-After", "5")
				writeJSON(w, 503, map[string]string{"code": "STARTING"})
				return
			}
			h.Users.Require(func(w http.ResponseWriter, req *http.Request) {
				u := auth.UserFrom(req.Context())
				acct, err := strconv.ParseInt(req.PathValue("acct"), 10, 64)
				if err != nil || acct <= 0 || !h.owns(req.Context(), u.ID, acct) {
					writeErr(w, rej("NOT_FOUND"))
					return
				}
				if limited && !h.allow(u.ID) {
					writeJSON(w, 429, map[string]string{"code": "RATE_LIMITED"})
					return
				}
				fn(w, req, acct)
			})(w, req)
		})
	}
	mux.HandleFunc("GET /api/trade/specs", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"specs": h.Engine.SpecList(), "taker_fee": h.Engine.TakerFee, "maker_fee": h.Engine.MakerFee})
	})
	r("GET /api/trade/{acct}", false, h.view)
	r("GET /api/trade/{acct}/history", false, h.history)
	r("POST /api/trade/{acct}/preview", false, h.preview)
	r("POST /api/trade/{acct}/orders", true, h.place)
	r("PATCH /api/trade/{acct}/orders/{id}", true, h.amend)
	r("DELETE /api/trade/{acct}/orders/{id}", true, h.cancel)
	r("POST /api/trade/{acct}/orders/{id}/undo", true, func(w http.ResponseWriter, req *http.Request, acct int64) {
		o, err := h.d(acct).UndoCancel(acct, req.PathValue("id"))
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, o)
	})
	r("POST /api/trade/{acct}/cancel-all", true, h.cancelAll)
	r("POST /api/trade/{acct}/close", true, h.close)
	r("POST /api/trade/{acct}/close-all", true, h.closeAll)
	r("POST /api/trade/{acct}/leverage", true, h.leverage)
	r("POST /api/trade/{acct}/margin-mode", true, h.mode)
	r("POST /api/trade/{acct}/tpsl", true, h.tpsl)
	r("POST /api/trade/{acct}/margin", true, h.margin)
	r("POST /api/trade/{acct}/fees", true, h.fees)
	r("POST /api/trade/{acct}/funds", true, h.funds)
	r("GET /api/trade/{acct}/stream", false, h.stream)
	mux.HandleFunc("GET /api/market/trades", h.trades)
}

// margin 逐仓调整保证金：amount 正数追加、负数减少；dry = true 只预览调整后的强平价。
func (h *API) margin(w http.ResponseWriter, r *http.Request, acct int64) {
	var in struct {
		Instrument string          `json:"instrument"`
		Amount     decimal.Decimal `json:"amount"`
		Dry        bool            `json:"dry"`
	}
	if !read(w, r, &in) {
		return
	}
	res, err := h.d(acct).AdjustMargin(acct, in.Instrument, in.Amount, in.Dry)
	reply(w, res, err)
}

// stream 账户视图实时推送（Server-Sent Events）：每 250 毫秒算一次，有变化才发；每 15 秒发一行注释保活。
// 前端断线后会自动重连，重连不上就回到每秒轮询。
func (h *API) stream(w http.ResponseWriter, r *http.Request, acct int64) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, rej("BAD_REQUEST"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no") // nginx 不要缓冲
	w.WriteHeader(200)
	var last []byte
	send := func() bool {
		v, err := h.d(acct).View(acct)
		if err != nil {
			return false
		}
		b, _ := json.Marshal(v)
		if bytes.Equal(b, last) {
			return true
		}
		last = b
		if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
			return false
		}
		fl.Flush()
		return true
	}
	if !send() {
		return
	}
	tick := time.NewTicker(250 * time.Millisecond)
	keep := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	defer keep.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			if !send() {
				return
			}
		case <-keep.C:
			if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
				return
			}
			fl.Flush()
		}
	}
}

// trades 某个品种最近的市场成交（盘口旁边的"最新成交"）。公开数据，不需要登录。
func (h *API) trades(w http.ResponseWriter, r *http.Request) {
	inst := strings.ToUpper(r.URL.Query().Get("instrument"))
	n, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if n <= 0 || n > 100 {
		n = 50
	}
	writeJSON(w, 200, h.Engine.Books.RecentTrades(h.Engine.Venue, inst, n))
}

// fees 改模拟账户的手续费率（D103）。实盘账户的费率是交易所定的，这里不能改。
func (h *API) fees(w http.ResponseWriter, r *http.Request, acct int64) {
	if acct >= LiveBase {
		writeErr(w, rej("NOT_PAPER"))
		return
	}
	var in struct {
		Taker   decimal.Decimal `json:"taker"`
		Maker   decimal.Decimal `json:"maker"`
		Default bool            `json:"default"`
	}
	if !read(w, r, &in) {
		return
	}
	if err := h.Engine.SetFees(acct, in.Taker, in.Maker, in.Default); err != nil {
		writeErr(w, err)
		return
	}
	t, m, c := h.Engine.FeesOf(acct)
	writeJSON(w, 200, map[string]any{"taker_fee": t, "maker_fee": m, "custom_fee": c})
}

// funds 模拟账户加减资金（D103）：正数加、负数减。引擎记账后同步本金到账户列表。
func (h *API) funds(w http.ResponseWriter, r *http.Request, acct int64) {
	if acct >= LiveBase {
		writeErr(w, rej("NOT_PAPER"))
		return
	}
	var in struct {
		Amount decimal.Decimal `json:"amount"`
	}
	if !read(w, r, &in) {
		return
	}
	if err := h.Engine.Fund(acct, in.Amount); err != nil {
		writeErr(w, err)
		return
	}
	if err := h.Users.AddPaperPrincipal(r.Context(), acct, in.Amount.Round(8)); err != nil {
		h.Engine.Log.Error("同步模拟账户本金", "acct", acct, "err", err)
	}
	v, err := h.Engine.View(acct)
	reply(w, v, err)
}

func (h *API) view(w http.ResponseWriter, _ *http.Request, acct int64) {
	v, err := h.d(acct).View(acct)
	reply(w, v, err)
}

func (h *API) history(w http.ResponseWriter, r *http.Request, acct int64) {
	if id := r.URL.Query().Get("order"); id != "" { // 某张委托的逐笔成交
		f, err := h.d(acct).OrderFills(acct, id)
		reply(w, f, err)
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	size, _ := strconv.Atoi(r.URL.Query().Get("size"))
	p, err := h.d(acct).History(acct, r.URL.Query().Get("kind"), page, size)
	reply(w, p, err)
}

func (h *API) preview(w http.ResponseWriter, r *http.Request, acct int64) {
	var in OrderReq
	if !read(w, r, &in) {
		return
	}
	p, err := h.d(acct).Preview(acct, in)
	reply(w, p, err)
}

func (h *API) place(w http.ResponseWriter, r *http.Request, acct int64) {
	var in OrderReq
	if !read(w, r, &in) {
		return
	}
	o, err := h.d(acct).Place(acct, in)
	h.manual(acct, "", err)
	reply(w, o, err)
}

func (h *API) manual(acct int64, inst string, err error) {
	if err == nil && h.OnManual != nil {
		h.OnManual(acct, inst)
	}
}

func (h *API) amend(w http.ResponseWriter, r *http.Request, acct int64) {
	var in struct {
		Price     decimal.Decimal `json:"price"`
		Qty       decimal.Decimal `json:"qty"`
		TriggerPx decimal.Decimal `json:"trigger_px"` // 条件单改触发价
	}
	if !read(w, r, &in) {
		return
	}
	o, err := h.d(acct).Amend(acct, r.PathValue("id"), in.Price, in.Qty, in.TriggerPx)
	if err == nil && o != nil && o.ClosePos { // 图上拖止盈止损线
		h.manual(acct, o.Instrument, nil)
	}
	reply(w, o, err)
}

func (h *API) cancel(w http.ResponseWriter, r *http.Request, acct int64) {
	o, err := h.d(acct).Cancel(acct, r.PathValue("id"))
	reply(w, o, err)
}

func (h *API) cancelAll(w http.ResponseWriter, r *http.Request, acct int64) {
	var in struct {
		Instrument string `json:"instrument"`
	}
	if !read(w, r, &in) {
		return
	}
	n, err := h.d(acct).CancelAll(acct, in.Instrument)
	reply(w, map[string]int{"canceled": n}, err)
}

func (h *API) close(w http.ResponseWriter, r *http.Request, acct int64) {
	var in struct {
		Instrument string          `json:"instrument"`
		Fraction   decimal.Decimal `json:"fraction"`
		Type       string          `json:"type"`
		Price      decimal.Decimal `json:"price"`
	}
	if !read(w, r, &in) {
		return
	}
	if in.Fraction.IsZero() {
		in.Fraction = one
	}
	if in.Type == "" {
		in.Type = Market
	}
	o, err := h.d(acct).ClosePosition(acct, in.Instrument, in.Fraction, in.Type, in.Price)
	h.manual(acct, "", err)
	reply(w, o, err)
}

func (h *API) closeAll(w http.ResponseWriter, _ *http.Request, acct int64) {
	os, err := h.d(acct).CloseAll(acct)
	h.manual(acct, "", err)
	reply(w, map[string]any{"orders": os}, err)
}

func (h *API) leverage(w http.ResponseWriter, r *http.Request, acct int64) {
	var in struct {
		Instrument string `json:"instrument"`
		Leverage   int    `json:"leverage"`
	}
	if !read(w, r, &in) {
		return
	}
	reply(w, map[string]bool{"ok": true}, h.d(acct).SetLeverage(acct, in.Instrument, in.Leverage))
}

func (h *API) mode(w http.ResponseWriter, r *http.Request, acct int64) {
	var in struct {
		Instrument string `json:"instrument"`
		Mode       string `json:"mode"`
	}
	if !read(w, r, &in) {
		return
	}
	reply(w, map[string]bool{"ok": true}, h.d(acct).SetMode(acct, in.Instrument, in.Mode))
}

func (h *API) tpsl(w http.ResponseWriter, r *http.Request, acct int64) {
	var in struct {
		Instrument string  `json:"instrument"`
		TP         *string `json:"tp"` // 不传 = 不改；"" 或 "0" = 取消
		SL         *string `json:"sl"`
		TriggerBy  string  `json:"trigger_by"` // mark（默认）| last
	}
	if !read(w, r, &in) {
		return
	}
	parse := func(s *string) (*decimal.Decimal, bool) {
		if s == nil {
			return nil, true
		}
		if *s == "" {
			return &zero, true
		}
		v, err := decimal.NewFromString(*s)
		return &v, err == nil
	}
	tp, ok1 := parse(in.TP)
	sl, ok2 := parse(in.SL)
	if !ok1 || !ok2 {
		writeErr(w, rej("BAD_REQUEST"))
		return
	}
	err := h.d(acct).SetTPSL(acct, in.Instrument, tp, sl, in.TriggerBy)
	h.manual(acct, in.Instrument, err)
	reply(w, map[string]bool{"ok": true}, err)
}

// ---------- 工具 ----------

// hugeNumber 一个字段的值是超大指数或超长数字。1e20000000 这样的数做一次除法要好几秒，而且是在全局锁里。
var hugeNumber = regexp.MustCompile(`[:\[,]\s*"?[-+]?(\d[\d.]*[eE][-+]?\d{3,}|\d{40,}|\d*\.\d{40,})"?\s*[,}\]]`)

func read(w http.ResponseWriter, r *http.Request, v any) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<10))
	if err != nil || hugeNumber.Match(body) || json.Unmarshal(body, v) != nil {
		writeErr(w, rej("BAD_REQUEST"))
		return false
	}
	return true
}

func reply(w http.ResponseWriter, v any, err error) {
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func writeErr(w http.ResponseWriter, err error) {
	var r *Reject
	if errors.As(err, &r) {
		writeJSON(w, r.HTTP, r)
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
