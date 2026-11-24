package bots

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/shopspring/decimal"

	"qs/internal/auth"
	"qs/internal/book"
	"qs/internal/spot"
	"qs/internal/trading"
)

func quoteOf(store *book.Store, venue, inst string) (decimal.Decimal, decimal.Decimal, bool) {
	b, ok := store.Book(venue, inst)
	if !ok || len(b.Bids) == 0 || len(b.Asks) == 0 {
		return zero, zero, false
	}
	return b.Bids[0].Px, b.Asks[0].Px, true
}

// SpotExec 现货通道。
type SpotExec struct {
	E     *spot.Engine
	Books *book.Store
	Venue string
	High  func(inst string) decimal.Decimal
}

// Quote 见 Exec。
func (x *SpotExec) Quote(inst string) (decimal.Decimal, decimal.Decimal, bool) {
	return quoteOf(x.Books, x.Venue, inst)
}

// High24 见 Exec。
func (x *SpotExec) High24(inst string) decimal.Decimal {
	if x.High != nil {
		return x.High(inst)
	}
	return zero
}

// SpecOf 见 Exec。
func (x *SpotExec) SpecOf(inst string) (Spec, bool) {
	s := x.E.Specs[inst]
	if s == nil {
		return Spec{}, false
	}
	return Spec{Tick: s.Tick, Step: s.Step, MinNotional: s.MinNotional, Fee: s.Fee, Taker: s.Fee}, true
}

// Prepare 见 Exec。现货没有杠杆。
func (x *SpotExec) Prepare(int64, string, int) error { return nil }

// Place 见 Exec。
func (x *SpotExec) Place(acct int64, inst, side, typ string, price, qty decimal.Decimal, _ bool, source string) (string, decimal.Decimal, decimal.Decimal, error) {
	o, err := x.E.Place(acct, spot.OrderReq{Instrument: inst, Side: side, Type: typ, Price: price, Qty: qty, Source: source})
	if err != nil {
		return "", zero, zero, err
	}
	return o.ID, o.Filled, o.AvgPx, nil
}

// Cancel 见 Exec。
func (x *SpotExec) Cancel(acct int64, id string) { _, _ = x.E.Cancel(acct, id, "BOT") }

// Order 见 Exec。
func (x *SpotExec) Order(acct int64, id string) (string, decimal.Decimal, decimal.Decimal, bool) {
	o := x.E.OrderOf(acct, id)
	if o == nil {
		return "", zero, zero, false
	}
	return o.Status, o.Filled, o.AvgPx, true
}

// Position 见 Exec。
func (x *SpotExec) Position(acct int64, inst string) decimal.Decimal {
	for _, b := range x.E.ViewOf(acct).Balances {
		if b.Coin == baseOf(inst) {
			return b.Free.Add(b.Locked)
		}
	}
	return zero
}

// PerpExec 合约通道。
type PerpExec struct {
	E     *trading.Engine
	Books *book.Store
	Venue string
	High  func(inst string) decimal.Decimal
}

// Quote 见 Exec。
func (x *PerpExec) Quote(inst string) (decimal.Decimal, decimal.Decimal, bool) {
	return quoteOf(x.Books, x.Venue, inst)
}

// High24 见 Exec。
func (x *PerpExec) High24(inst string) decimal.Decimal {
	if x.High != nil {
		return x.High(inst)
	}
	return zero
}

// SpecOf 见 Exec。
func (x *PerpExec) SpecOf(inst string) (Spec, bool) {
	s, ok := x.E.Specs.Get(inst)
	if !ok {
		return Spec{}, false
	}
	return Spec{Tick: s.Tick, Step: s.Step, MinNotional: s.MinNotional, Fee: decimal.RequireFromString("0.0002"), Taker: decimal.RequireFromString("0.0005"), Perp: true, Band: bandOf(s.PriceUp, s.PriceDown)}, true
}

// bandOf 机器人的挂单离现价最远多少（比例）：按这个品种真实的限价范围（币安 PERCENT_PRICE，BTC、ETH 是 ±3%，多数币 ±5%），
// 留一成余量（撮合按标记价判断，机器人看的是盘口中间价）。原来统一写 4.5%，BTC、ETH 3%–4.5% 之间的格子一挂就被拒（11-16 走查）。
func bandOf(up, down decimal.Decimal) decimal.Decimal {
	b := decimal.Zero
	if up.GreaterThan(one) {
		b = up.Sub(one)
	}
	if down.IsPositive() && down.LessThan(one) && (b.IsZero() || one.Sub(down).LessThan(b)) {
		b = one.Sub(down)
	}
	if !b.IsPositive() {
		return decimal.RequireFromString("0.045")
	}
	return b.Mul(decimal.RequireFromString("0.9"))
}

// Prepare 见 Exec：默认逐仓（D83）。
func (x *PerpExec) Prepare(acct int64, inst string, leverage int) error {
	if leverage <= 0 {
		leverage = 3
	}
	if err := x.E.SetMode(acct, inst, "isolated"); err != nil {
		return err
	}
	return x.E.SetLeverage(acct, inst, leverage)
}

// Place 见 Exec。
func (x *PerpExec) Place(acct int64, inst, side, typ string, price, qty decimal.Decimal, reduce bool, source string) (string, decimal.Decimal, decimal.Decimal, error) {
	o, err := x.E.PlaceAs(acct, trading.OrderReq{Instrument: inst, Side: side, Type: typ, Price: price, Qty: qty, ReduceOnly: reduce}, source, "")
	if err != nil {
		return "", zero, zero, err
	}
	return o.ID, o.Filled, o.AvgPx, nil
}

// Cancel 见 Exec。
func (x *PerpExec) Cancel(acct int64, id string) { _, _ = x.E.CancelAs(acct, id, "BOT") }

// Order 见 Exec。
func (x *PerpExec) Order(acct int64, id string) (string, decimal.Decimal, decimal.Decimal, bool) {
	o := x.E.OrderOf(acct, id)
	if o == nil {
		return "", zero, zero, false
	}
	return o.Status, o.Filled, o.AvgPx, true
}

// Position 见 Exec。
func (x *PerpExec) Position(acct int64, inst string) decimal.Decimal {
	b, err := x.E.Brief(acct)
	if err != nil {
		return zero
	}
	return b.Pos[inst].Qty
}

// ---------- 接口 ----------

// View 界面看到的一个机器人：加上现价和浮动盈亏。
type View struct {
	Bot
	Price  decimal.Decimal `json:"price"`
	Unreal decimal.Decimal `json:"unreal"`
	Range  string          `json:"range,omitempty"` // below | inside | above：现价和网格区间的关系
	Lower  int             `json:"grid_now"`        // 现价在第几格（从 0 数）
}

func (m *Manager) viewOf(b Bot) View {
	v := View{Bot: b}
	bid, ask, ok := m.execOf(b.Params.Instrument).Quote(b.Params.Instrument)
	if !ok {
		return v
	}
	p := mid(bid, ask)
	v.Price = p
	if b.Status == StStopped {
		return v
	}
	switch b.Kind {
	case SpotGrid, PerpGrid:
		for i := range b.Grids {
			g := &b.Grids[i]
			if !g.Entry.IsPositive() {
				continue
			}
			if g.Side == "sell" {
				v.Unreal = v.Unreal.Add(p.Sub(g.Entry).Mul(gridQty(&b, g)))
			} else {
				v.Unreal = v.Unreal.Add(g.Entry.Sub(p).Mul(b.Plan.Qty))
			}
		}
		if b.Plan != nil && len(b.Plan.Lines) > 1 {
			switch {
			case p.LessThan(b.Plan.Lines[0]):
				v.Range = "below"
			case p.GreaterThan(b.Plan.Lines[len(b.Plan.Lines)-1]):
				v.Range = "above"
			default:
				v.Range = "inside"
			}
			for i := 0; i+1 < len(b.Plan.Lines); i++ {
				if p.GreaterThanOrEqual(b.Plan.Lines[i]) {
					v.Lower = i
				}
			}
		}
	case DCA, Dip:
		if b.Avg.IsPositive() {
			if b.Params.Side == "short" {
				v.Unreal = b.Avg.Sub(p).Mul(b.Qty)
			} else {
				v.Unreal = p.Sub(b.Avg).Mul(b.Qty)
			}
		}
	case Recur:
		v.Unreal = b.Bought.Mul(p).Sub(b.Spent)
	}
	return v
}

// Users 账户服务。
type Users interface {
	Require(http.HandlerFunc) http.HandlerFunc
	OwnsPaper(ctx context.Context, user, acct int64) bool
}

// Routes 挂接口。
func (m *Manager) Routes(mux *http.ServeMux, users Users) {
	wj := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	we := func(w http.ResponseWriter, err error) {
		var r *Rej
		if errors.As(err, &r) {
			wj(w, 422, map[string]any{"code": r.Code, "params": r.Params})
			return
		}
		var tr *trading.Reject // 纪律锁的拒绝带着规则和解锁时间
		if errors.As(err, &tr) {
			wj(w, 422, map[string]any{"code": tr.Code, "params": tr.Params})
			return
		}
		type coded interface {
			RejectCode() string
		}
		var c coded
		if errors.As(err, &c) {
			wj(w, 422, map[string]any{"code": c.RejectCode()})
			return
		}
		wj(w, 500, map[string]string{"code": "INTERNAL_ERROR"})
	}
	r := func(pattern string, fn func(w http.ResponseWriter, q *http.Request, u *auth.User)) {
		mux.HandleFunc(pattern, users.Require(func(w http.ResponseWriter, q *http.Request) { fn(w, q, auth.UserFrom(q.Context())) }))
	}
	type startReq struct {
		Kind    string `json:"kind"`
		Account int64  `json:"account"`
		Params  Params `json:"params"`
		Confirm bool   `json:"confirm"`
	}
	read := func(w http.ResponseWriter, q *http.Request, v any) bool {
		if err := json.NewDecoder(http.MaxBytesReader(w, q.Body, 16<<10)).Decode(v); err != nil {
			wj(w, 400, map[string]string{"code": "BAD_REQUEST"})
			return false
		}
		return true
	}
	r("GET /api/bots", func(w http.ResponseWriter, _ *http.Request, u *auth.User) {
		list := m.List(u.ID)
		out := make([]View, 0, len(list))
		for _, b := range list {
			out = append(out, m.viewOf(b))
		}
		wj(w, 200, map[string]any{"bots": out, "order": m.Order(u.ID)})
	})
	r("POST /api/bots/preview", func(w http.ResponseWriter, q *http.Request, _ *auth.User) {
		var in startReq
		if !read(w, q, &in) {
			return
		}
		p, err := m.Preview(in.Kind, in.Params)
		if err != nil {
			we(w, err)
			return
		}
		wj(w, 200, p)
	})
	r("POST /api/bots", func(w http.ResponseWriter, q *http.Request, u *auth.User) {
		var in startReq
		if !read(w, q, &in) {
			return
		}
		if !in.Confirm {
			wj(w, 400, map[string]string{"code": "CONFIRM_REQUIRED"})
			return
		}
		if !users.OwnsPaper(q.Context(), u.ID, in.Account) {
			wj(w, 404, map[string]string{"code": "NOT_FOUND"})
			return
		}
		b, err := m.Start(u.ID, in.Account, in.Kind, in.Params)
		if err != nil {
			we(w, err)
			return
		}
		wj(w, 200, m.viewOf(*b))
	})
	withID := func(w http.ResponseWriter, q *http.Request) (int64, bool) {
		id, err := strconv.ParseInt(q.PathValue("id"), 10, 64)
		if err != nil {
			wj(w, 404, map[string]string{"code": "NOT_FOUND"})
			return 0, false
		}
		return id, true
	}
	r("POST /api/bots/{id}/pause", func(w http.ResponseWriter, q *http.Request, u *auth.User) {
		if id, ok := withID(w, q); ok {
			b, err := m.Pause(u.ID, id)
			if err != nil {
				we(w, err)
				return
			}
			wj(w, 200, m.viewOf(*b))
		}
	})
	r("POST /api/bots/{id}/resume", func(w http.ResponseWriter, q *http.Request, u *auth.User) {
		if id, ok := withID(w, q); ok {
			b, err := m.Resume(u.ID, id)
			if err != nil {
				we(w, err)
				return
			}
			wj(w, 200, m.viewOf(*b))
		}
	})
	r("POST /api/bots/{id}/stop", func(w http.ResponseWriter, q *http.Request, u *auth.User) {
		id, ok := withID(w, q)
		if !ok {
			return
		}
		var in struct {
			Mode string `json:"mode"` // close | keep
		}
		if !read(w, q, &in) {
			return
		}
		if in.Mode != "close" && in.Mode != "keep" {
			wj(w, 400, map[string]string{"code": "BAD_REQUEST"})
			return
		}
		b, err := m.Stop(u.ID, id, in.Mode == "close")
		if err != nil {
			we(w, err)
			return
		}
		wj(w, 200, m.viewOf(*b))
	})
	r("POST /api/bots/order", func(w http.ResponseWriter, q *http.Request, u *auth.User) {
		var in struct {
			Order []string `json:"order"`
		}
		if !read(w, q, &in) {
			return
		}
		if err := m.SetOrder(q.Context(), u.ID, in.Order); err != nil {
			we(w, err)
			return
		}
		wj(w, 200, map[string]any{"order": in.Order})
	})
}
