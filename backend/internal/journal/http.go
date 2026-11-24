package journal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"qs/internal/aireview"
	"qs/internal/auth"
	"qs/internal/portfolio"
	"qs/internal/trading"
)

// API 复盘页的接口。
type API struct {
	Users    *auth.Service
	Gate     *auth.Gate
	Engine   *trading.Engine
	Store    *Store
	Log      *slog.Logger
	Spot     func(acct int64) []trading.Fill                                       // 现货的成交，换算成和合约一样的格式
	PerTrade func(user int64) float64                                              // 每笔风险上限；纪律锁关着时是 0
	Rejects  func(user, acct int64) []Reject                                       // 纪律锁拒绝的下单
	Review   *aireview.Service                                                     // AI 点评，没配 key 时不开
	Path     func(ctx context.Context, inst string, from, to int64) ([]Pt, string) // 价格路径，第二个返回值是用的哪种数据
	// 实盘账户（D89）：成交和资金费从交易所同步下来，复盘和模拟盘一样算。没接实盘时是 nil
	Live        Recorder
	LiveAccount func(ctx context.Context, user, acct int64) (*auth.PaperAccount, bool)

	mu    sync.Mutex
	cache map[string]Excursion // 平掉的交易不会再变，算过就记住
}

func reviewLang(r *http.Request) string {
	if r.URL.Query().Get("lang") == "en" {
		return "en"
	}
	return "zh"
}

// factsOf 这笔交易里可以发给模型的数字。不要往这里加笔记、编号、账号。
func factsOf(t Trade, e Excursion) aireview.Facts {
	f := aireview.Facts{Coin: strings.TrimSuffix(strings.TrimSuffix(t.Instrument, "-PERP"), "-SPOT"), Side: t.Side,
		Entry: t.Entry, Exit: t.Exit, NetUSDT: round(t.Net, 2), FeesUSDT: round(t.Fees, 2), Funding: round(t.Funding, 2),
		R: t.R, HoldMin: round(float64(t.HoldMs)/60000, 2), ClosedBy: t.ClosedBy, Violations: t.Violations}
	if t.SlipKnown {
		v := round(t.Slip, 2)
		f.SlipUSDT = &v
	}
	if t.Stop > 0 {
		f.StopDist = aireview.Pct(math.Abs(t.Entry-t.Stop), t.Entry)
	}
	if e.Source != "none" && e.Source != "" {
		f.MAEPct, f.MFEPct = aireview.Pct(e.MAE, t.Entry), aireview.Pct(e.MFE, t.Entry)
	}
	return f
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func code(w http.ResponseWriter, status int, c string) {
	writeJSON(w, status, map[string]string{"code": c})
}

func (h *API) route(mux *http.ServeMux, pattern string, fn func(w http.ResponseWriter, r *http.Request, u *auth.User)) {
	mux.HandleFunc(pattern, func(w http.ResponseWriter, req *http.Request) {
		if h.Gate != nil && !h.Gate.IsOpen() {
			w.Header().Set("Retry-After", "5")
			code(w, 503, "STARTING")
			return
		}
		h.Users.Require(func(w http.ResponseWriter, r *http.Request) { fn(w, r, auth.UserFrom(r.Context())) })(w, req)
	})
}

// Recorder 能给出一个账户全部成交和资金费的交易台（模拟引擎、实盘）。
type Recorder interface {
	Records(acct int64) ([]trading.Fill, []trading.FundingRec, decimal.Decimal, error)
}

func (h *API) records(acct int64) ([]trading.Fill, []trading.FundingRec, decimal.Decimal, error) {
	if acct >= trading.LiveBase && h.Live != nil {
		return h.Live.Records(acct)
	}
	return h.Engine.Records(acct)
}

func (h *API) stops(acct int64) []trading.StopRec {
	if acct >= trading.LiveBase {
		// 实盘：实盘那边每次和交易所对账时记下挂着的止损单（live.Desks.Stops，11-18）
		if s, ok := h.Live.(interface{ Stops(int64) []trading.StopRec }); ok && h.Live != nil {
			return s.Stops(acct)
		}
		return nil
	}
	return h.Engine.Stops(acct)
}

// paper 账户要是这个人的（模拟账户或实盘账户）。
func (h *API) paper(w http.ResponseWriter, r *http.Request, u *auth.User) (*auth.PaperAccount, bool) {
	id, err := strconv.ParseInt(r.PathValue("acct"), 10, 64)
	if err != nil {
		code(w, 400, "BAD_REQUEST")
		return nil, false
	}
	if id >= trading.LiveBase {
		if h.LiveAccount != nil {
			if p, ok := h.LiveAccount(r.Context(), u.ID, id); ok {
				return p, true
			}
		}
		code(w, 404, "NOT_FOUND")
		return nil, false
	}
	list, err := h.Users.PaperAccounts(r.Context(), u.ID)
	if err != nil {
		code(w, 500, "INTERNAL_ERROR")
		return nil, false
	}
	for i := range list {
		if list[i].ID == id {
			return &list[i], true
		}
	}
	code(w, 404, "NOT_FOUND")
	return nil, false
}

func initialOf(p *auth.PaperAccount) float64 {
	v, _ := strconv.ParseFloat(p.Initial, 64)
	return v
}

// load 算这个账户的全部交易，并贴上标签、笔记、截图、预案。
func (h *API) load(ctx context.Context, user int64, p *auth.PaperAccount, origin string) ([]Trade, error) {
	fills, funds, _, err := h.records(p.ID)
	if err != nil {
		return nil, err
	}
	if h.Spot != nil {
		fills = append(fills, h.Spot(p.ID)...)
	}
	closed, open := portfolio.BuildTrades(fills, funds)
	var rejects []Reject
	if h.Rejects != nil {
		rejects = h.Rejects(user, p.ID)
	}
	per := 0.0
	if h.PerTrade != nil {
		per = h.PerTrade(user)
	}
	all := Build(closed, open, h.stops(p.ID), rejects, initialOf(p), per)
	notes, err := h.Store.Notes(ctx, user, p.ID)
	if err != nil {
		return nil, err
	}
	shots, err := h.Store.Shots(ctx, user, p.ID)
	if err != nil {
		return nil, err
	}
	orders, err := h.Store.OrderPlaybooks(ctx, user, p.ID)
	if err != nil {
		return nil, err
	}
	out := make([]Trade, 0, len(all))
	for _, t := range all {
		if n := notes[t.ID]; n != nil {
			t.Note = n
			t.Playbook = n.Playbook
		}
		t.Shots = shots[t.ID]
		if t.Playbook == 0 && len(t.Legs) > 0 {
			t.Playbook = orders[t.Legs[0].OrderID]
		}
		switch origin {
		case "ai":
			if t.Origin != "ai" && t.Origin != "mixed" {
				continue
			}
		case "bot", "signal":
			if t.Origin != origin {
				continue
			}
		case "all":
		default:
			if t.Origin == "ai" || t.Origin == "bot" || t.Origin == "signal" {
				continue
			}
		}
		out = append(out, t)
	}
	return out, nil
}

// ExcursionOf 一笔已平仓交易持仓中的最深浮亏、最高浮盈（价格距离）。没有价格路径时 ok = false。高光用。
func (h *API) ExcursionOf(ctx context.Context, acct int64, t Trade) (mae, mfe float64, ok bool) {
	e := h.excursions(ctx, acct, []Trade{t})[t.ID]
	if e.Source == "" || e.Source == "none" {
		return 0, 0, false
	}
	return e.MAE, e.MFE, true
}

// TradesOf 这个账户的全部交易（所有来源），高光判断用。账户要是这个人的。
func (h *API) TradesOf(ctx context.Context, user, acct int64) ([]Trade, error) {
	if acct >= trading.LiveBase {
		if h.LiveAccount != nil {
			if p, ok := h.LiveAccount(ctx, user, acct); ok {
				return h.load(ctx, user, p, "all")
			}
		}
		return nil, ErrNotFound
	}
	list, err := h.Users.PaperAccounts(ctx, user)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].ID == acct {
			return h.load(ctx, user, &list[i], "all")
		}
	}
	return nil, ErrNotFound
}

func (h *API) excursions(ctx context.Context, acct int64, ts []Trade) map[string]Excursion {
	out := map[string]Excursion{}
	for _, t := range ts {
		if t.CloseTime == 0 {
			continue
		}
		key := fmt.Sprintf("%d:%s:%d", acct, t.ID, t.CloseTime)
		h.mu.Lock()
		e, ok := h.cache[key]
		h.mu.Unlock()
		if !ok {
			src := "none"
			var pts []Pt
			if h.Path != nil {
				pts, src = h.Path(ctx, t.Instrument, t.OpenTime, t.CloseTime)
			}
			if len(pts) == 0 {
				src = "none"
			}
			e = ExcursionOf(t, pts, src)
			if src != "none" {
				h.mu.Lock()
				if h.cache == nil {
					h.cache = map[string]Excursion{}
				}
				h.cache[key] = e
				h.mu.Unlock()
			}
		}
		// R 和净收益不缓存：笔记、止损记录变了不影响，但以后手续费口径可能变
		e.Net, e.R = t.Net, t.R
		out[t.ID] = e
	}
	return out
}

func (h *API) fail(w http.ResponseWriter, err error) {
	var rej *trading.Reject
	switch {
	case errors.As(err, &rej):
		code(w, 404, "NOT_FOUND")
	case errors.Is(err, ErrNotFound):
		code(w, 404, "NOT_FOUND")
	case errors.Is(err, ErrShotQuota):
		code(w, 413, "SHOT_QUOTA")
	default:
		code(w, 500, "INTERNAL_ERROR")
	}
}

func cleanText(s string, limit int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) > limit {
		return string(r[:limit])
	}
	return s
}

// Routes 挂接口。
func (h *API) Routes(mux *http.ServeMux) {
	h.route(mux, "GET /api/journal/a/{acct}/trades/{id}/review", func(w http.ResponseWriter, r *http.Request, u *auth.User) {
		p, ok := h.paper(w, r, u)
		if !ok {
			return
		}
		if !h.Review.Enabled() {
			writeJSON(w, 200, map[string]any{"enabled": false})
			return
		}
		rv, err := h.Review.Get(r.Context(), u.ID, p.ID, r.PathValue("id"), reviewLang(r))
		if err != nil {
			h.fail(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"enabled": true, "review": rv, "left": h.Review.Left(r.Context(), u.ID)})
	})
	h.route(mux, "POST /api/journal/a/{acct}/trades/{id}/review", func(w http.ResponseWriter, r *http.Request, u *auth.User) {
		p, ok := h.paper(w, r, u)
		if !ok {
			return
		}
		ts, err := h.load(r.Context(), u.ID, p, "")
		if err != nil {
			h.fail(w, err)
			return
		}
		var t *Trade
		for i := range ts {
			if ts[i].ID == r.PathValue("id") {
				t = &ts[i]
			}
		}
		if t == nil {
			code(w, 404, "NOT_FOUND")
			return
		}
		if t.CloseTime == 0 { // 只点评平掉的，没平的说什么都可能变成交易建议
			code(w, 400, "TRADE_OPEN")
			return
		}
		rv, err := h.Review.Generate(r.Context(), u.ID, p.ID, t.ID, reviewLang(r), factsOf(*t, h.excursions(r.Context(), p.ID, []Trade{*t})[t.ID]))
		if err != nil {
			var e *aireview.Err
			if errors.As(err, &e) {
				code(w, 400, e.Code)
				return
			}
			if h.Log != nil {
				h.Log.Warn("AI 点评失败", "err", err)
			}
			code(w, 502, "AI_FAILED")
			return
		}
		writeJSON(w, 200, map[string]any{"enabled": true, "review": rv, "left": h.Review.Left(r.Context(), u.ID)})
	})
	h.route(mux, "GET /api/journal/a/{acct}/trades", func(w http.ResponseWriter, r *http.Request, u *auth.User) {
		p, ok := h.paper(w, r, u)
		if !ok {
			return
		}
		ts, err := h.load(r.Context(), u.ID, p, r.URL.Query().Get("origin"))
		if err != nil {
			h.fail(w, err)
			return
		}
		used, _ := h.Store.ShotUsage(r.Context(), u.ID)
		writeJSON(w, 200, map[string]any{"trades": ts, "shot_used": used, "shot_max": MaxShotsUser})
	})
	h.route(mux, "GET /api/journal/a/{acct}/stats", func(w http.ResponseWriter, r *http.Request, u *auth.User) {
		p, ok := h.paper(w, r, u)
		if !ok {
			return
		}
		ts, err := h.load(r.Context(), u.ID, p, r.URL.Query().Get("origin"))
		if err != nil {
			h.fail(w, err)
			return
		}
		tz, _ := strconv.Atoi(r.URL.Query().Get("tz"))
		st := Compute(ts, tz, initialOf(p))
		names, _ := h.Store.AllPlaybookNames(r.Context(), u.ID)
		writeJSON(w, 200, map[string]any{"stats": st, "playbook_names": names})
	})
	h.route(mux, "GET /api/journal/a/{acct}/excursions", func(w http.ResponseWriter, r *http.Request, u *auth.User) {
		p, ok := h.paper(w, r, u)
		if !ok {
			return
		}
		ts, err := h.load(r.Context(), u.ID, p, r.URL.Query().Get("origin"))
		if err != nil {
			h.fail(w, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
		defer cancel()
		m := h.excursions(ctx, p.ID, ts)
		out := make([]Excursion, 0, len(m))
		for _, t := range ts {
			if e, ok := m[t.ID]; ok {
				out = append(out, e)
			}
		}
		writeJSON(w, 200, map[string]any{"items": out})
	})
	h.route(mux, "GET /api/journal/a/{acct}/findings", func(w http.ResponseWriter, r *http.Request, u *auth.User) {
		p, ok := h.paper(w, r, u)
		if !ok {
			return
		}
		ts, err := h.load(r.Context(), u.ID, p, r.URL.Query().Get("origin"))
		if err != nil {
			h.fail(w, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
		defer cancel()
		after := func(t Trade, until int64) []Pt {
			if h.Path == nil {
				return nil
			}
			pts, _ := h.Path(ctx, t.Instrument, t.CloseTime, min(until, time.Now().UnixMilli()))
			return pts
		}
		writeJSON(w, 200, map[string]any{"items": Findings(ts, h.excursions(ctx, p.ID, ts), after)})
	})
	h.route(mux, "GET /api/journal/a/{acct}/export", func(w http.ResponseWriter, r *http.Request, u *auth.User) {
		p, ok := h.paper(w, r, u)
		if !ok {
			return
		}
		kind := r.URL.Query().Get("kind")
		var b strings.Builder
		b.WriteString("\xef\xbb\xbf") // 让表格软件按 UTF-8 打开
		switch kind {
		case "fills", "funding":
			fills, funds, _, err := h.records(p.ID)
			if err != nil {
				h.fail(w, err)
				return
			}
			if kind == "fills" {
				ExportFills(&b, fills)
			} else {
				ExportFunding(&b, funds)
			}
		case "trades":
			ts, err := h.load(r.Context(), u.ID, p, "all")
			if err != nil {
				h.fail(w, err)
				return
			}
			ExportTrades(&b, ts)
		default:
			code(w, 400, "BAD_REQUEST")
			return
		}
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="qs-%d-%s.csv"`, p.ID, kind))
		_, _ = io.WriteString(w, b.String())
	})
	h.route(mux, "PUT /api/journal/a/{acct}/trades/{id}/note", func(w http.ResponseWriter, r *http.Request, u *auth.User) {
		p, ok := h.paper(w, r, u)
		if !ok {
			return
		}
		var n Note
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&n); err != nil {
			code(w, 400, "BAD_REQUEST")
			return
		}
		n.Note = cleanText(n.Note, 5000)
		n.Emotion = cleanText(n.Emotion, 32)
		tags := make([]string, 0, len(n.Tags))
		for _, t := range n.Tags {
			if t = cleanText(t, 32); t != "" && len(tags) < 20 {
				tags = append(tags, t)
			}
		}
		n.Tags = tags
		if err := h.Store.SaveNote(r.Context(), u.ID, p.ID, r.PathValue("id"), n); err != nil {
			h.fail(w, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	h.route(mux, "POST /api/journal/a/{acct}/trades/{id}/shots", func(w http.ResponseWriter, r *http.Request, u *auth.User) {
		p, ok := h.paper(w, r, u)
		if !ok {
			return
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, int64(MaxShot)+1))
		if err != nil || len(data) > MaxShot {
			code(w, 413, "SHOT_TOO_BIG")
			return
		}
		// 只看内容，不信文件名和请求头。svg 不收：里面能带脚本
		mime := http.DetectContentType(data)
		if mime != "image/png" && mime != "image/jpeg" && mime != "image/webp" {
			code(w, 415, "SHOT_TYPE")
			return
		}
		m, err := h.Store.AddShot(r.Context(), u.ID, p.ID, r.PathValue("id"), mime, data)
		if err != nil {
			h.fail(w, err)
			return
		}
		writeJSON(w, 200, m)
	})
	h.route(mux, "GET /api/journal/shots/{id}", func(w http.ResponseWriter, r *http.Request, u *auth.User) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		mime, data, err := h.Store.Shot(r.Context(), u.ID, id)
		if err != nil {
			h.fail(w, err)
			return
		}
		w.Header().Set("Content-Type", mime)
		w.Header().Set("Cache-Control", "private, max-age=86400")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(data)
	})
	h.route(mux, "DELETE /api/journal/shots/{id}", func(w http.ResponseWriter, r *http.Request, u *auth.User) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err := h.Store.DeleteShot(r.Context(), u.ID, id); err != nil {
			h.fail(w, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	h.route(mux, "GET /api/journal/playbooks", func(w http.ResponseWriter, r *http.Request, u *auth.User) {
		list, err := h.Store.Playbooks(r.Context(), u.ID)
		if err != nil {
			h.fail(w, err)
			return
		}
		if list == nil {
			list = []Playbook{}
		}
		writeJSON(w, 200, map[string]any{"playbooks": list})
	})
	save := func(w http.ResponseWriter, r *http.Request, u *auth.User, id int64) {
		var p Playbook
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<18)).Decode(&p); err != nil {
			code(w, 400, "BAD_REQUEST")
			return
		}
		p.ID = id
		p.Name = cleanText(p.Name, 60)
		p.Entry, p.Stop, p.Target = cleanText(p.Entry, 1000), cleanText(p.Stop, 1000), cleanText(p.Target, 1000)
		if p.Name == "" {
			code(w, 400, "PLAYBOOK_NAME")
			return
		}
		out, err := h.Store.SavePlaybook(r.Context(), u.ID, p)
		if err != nil {
			h.fail(w, err)
			return
		}
		writeJSON(w, 200, out)
	}
	h.route(mux, "POST /api/journal/playbooks", func(w http.ResponseWriter, r *http.Request, u *auth.User) { save(w, r, u, 0) })
	h.route(mux, "PUT /api/journal/playbooks/{id}", func(w http.ResponseWriter, r *http.Request, u *auth.User) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if id <= 0 {
			code(w, 400, "BAD_REQUEST")
			return
		}
		save(w, r, u, id)
	})
	h.route(mux, "DELETE /api/journal/playbooks/{id}", func(w http.ResponseWriter, r *http.Request, u *auth.User) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err := h.Store.DeletePlaybook(r.Context(), u.ID, id); err != nil {
			h.fail(w, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	h.route(mux, "POST /api/journal/a/{acct}/order-playbook", func(w http.ResponseWriter, r *http.Request, u *auth.User) {
		p, ok := h.paper(w, r, u)
		if !ok {
			return
		}
		var in struct {
			Order    string `json:"order_id"`
			Playbook int64  `json:"playbook"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&in); err != nil || in.Order == "" {
			code(w, 400, "BAD_REQUEST")
			return
		}
		if err := h.Store.SetOrderPlaybook(r.Context(), u.ID, p.ID, in.Order, in.Playbook); err != nil {
			h.fail(w, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
}

// ---------- CSV ----------

func ft(ms int64) string {
	if ms == 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format("2006-01-02 15:04:05")
}

func fn(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// ExportFills 成交。时间都是 UTC。
func ExportFills(b *strings.Builder, fills []trading.Fill) {
	csvRow(b, "time_utc", "instrument", "side", "qty", "price", "fee", "realized", "maker", "source", "order_id")
	for _, x := range fills {
		csvRow(b, ft(x.Time), x.Instrument, x.Side, x.Qty.String(), x.Px.String(), x.Fee.String(), x.Realized.String(), strconv.FormatBool(x.Maker), x.Source, x.OrderID)
	}
}

// ExportFunding 资金费。
func ExportFunding(b *strings.Builder, recs []trading.FundingRec) {
	csvRow(b, "time_utc", "instrument", "rate", "mark", "qty", "amount")
	for _, x := range recs {
		csvRow(b, ft(x.Time), x.Instrument, x.Rate.String(), x.Mark.String(), x.Qty.String(), x.Amount.String())
	}
}

// ExportTrades 每笔交易。
func ExportTrades(b *strings.Builder, ts []Trade) {
	csvRow(b, "id", "instrument", "side", "origin", "open_utc", "close_utc", "qty", "entry", "exit", "gross", "fees", "funding", "net", "slip", "stop", "risk", "r", "violations", "tags", "note")
	for _, t := range ts {
		r := ""
		if t.R != nil {
			r = fn(*t.R)
		}
		tags, note := "", ""
		if t.Note != nil {
			tags, note = strings.Join(t.Note.Tags, " "), t.Note.Note
		}
		csvRow(b, t.ID, t.Instrument, t.Side, t.Origin, ft(t.OpenTime), ft(t.CloseTime), fn(t.Qty), fn(t.Entry), fn(t.Exit), fn(t.Gross), fn(t.Fees), fn(t.Funding), fn(t.Net), fn(t.Slip), fn(t.Stop), fn(t.Risk), r, strings.Join(t.Violations, " "), tags, note)
	}
}
