package binapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/shopspring/decimal"

	"qs/internal/trading"
)

// 账户推送（币安的 user data stream）：POST /fapi/v1/listenKey 拿一个 listenKey，连 ws://服务器/bapi/ws/<listenKey>
// （或 /bapi/private/ws/<listenKey>，币安 2026-04 起的新地址）。推 ORDER_TRADE_UPDATE、ACCOUNT_UPDATE，60 分钟不续期就过期。
// 做法：每 250 毫秒看一眼账户的事件序号，变了才比对：哪些委托变了、有哪些新成交、余额和仓位变了没有。

const listenTTL = 60 * time.Minute

type listen struct {
	acct    int64
	expires time.Time
}

func (s *Server) listenKey(w http.ResponseWriter, method string, rq *req) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lk == nil {
		s.lk = map[string]*listen{}
	}
	now := s.now()
	for k, v := range s.lk {
		if now.After(v.expires) {
			delete(s.lk, k)
		}
	}
	// 和币安一样：一个账户同一时间一个 listenKey，重复申请回同一个并续期
	var key string
	for k, v := range s.lk {
		if v.acct == rq.acct {
			key = k
		}
	}
	switch method {
	case http.MethodDelete:
		if key != "" {
			delete(s.lk, key)
		}
		s.write(w, map[string]any{})
		return
	case http.MethodPut:
		if key == "" {
			s.fail(w, bad(-1125, "This listenKey does not exist."))
			return
		}
	}
	if key == "" {
		key = randHex(32)
		s.lk[key] = &listen{acct: rq.acct}
	}
	s.lk[key].expires = now.Add(listenTTL)
	s.write(w, map[string]any{"listenKey": key})
}

func (s *Server) listenAcct(key string) (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.lk[key]
	if !ok || s.now().After(v.expires) {
		return 0, false
	}
	return v.acct, true
}

type snap struct {
	orders map[string]trading.Order
	pos    map[string]string // 品种 → 数量和开仓价（变了才推）
	wallet decimal.Decimal
	fill   int64 // 推到第几笔成交
}

func (s *Server) snapOf(acct int64, v *trading.AccountView) snap {
	sn := snap{orders: map[string]trading.Order{}, pos: map[string]string{}, wallet: v.Wallet}
	for _, o := range v.Orders {
		sn.orders[o.ID] = *o
	}
	for _, p := range v.Positions {
		sn.pos[p.Instrument] = p.Side + p.Qty.String() + "@" + p.Entry.String()
	}
	return sn
}

func (s *Server) stream(w http.ResponseWriter, r *http.Request, key string) {
	acct, ok := s.listenAcct(key)
	if !ok {
		http.Error(w, `{"code":-1125,"msg":"This listenKey does not exist."}`, http.StatusBadRequest)
		return
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true}) // 程序连，不是浏览器：不查来源
	if err != nil {
		return
	}
	defer c.CloseNow()
	ctx := c.CloseRead(r.Context())
	send := func(v any) bool {
		b, _ := json.Marshal(v)
		wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return c.Write(wctx, websocket.MessageText, b) == nil
	}
	v, err := s.E.View(acct)
	if err != nil {
		return
	}
	prev := s.snapOf(acct, v)
	seq := v.Seq
	if fills, _, _, err := s.E.Records(acct); err == nil && len(fills) > 0 {
		prev.fill = fills[len(fills)-1].Seq
	}
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		if _, ok := s.listenAcct(key); !ok {
			_ = send(map[string]any{"e": "listenKeyExpired", "E": s.now().UnixMilli(), "listenKey": key})
			c.Close(websocket.StatusNormalClosure, "listenKey expired")
			return
		}
		v, err := s.E.View(acct)
		if err != nil {
			c.Close(websocket.StatusNormalClosure, "account archived")
			return
		}
		if v.Seq == seq {
			continue
		}
		seq = v.Seq
		for _, ev := range s.diff(acct, &prev, v) {
			if !send(ev) {
				return
			}
		}
	}
}

// diff 和上一次比：新成交（TRADE）、新委托（NEW）、撤销或过期（CANCELED / EXPIRED）、余额或仓位变了（ACCOUNT_UPDATE）。
func (s *Server) diff(acct int64, prev *snap, v *trading.AccountView) []any {
	now := s.now().UnixMilli()
	var out []any
	upd := func(o trading.Order, x string, f *trading.Fill) map[string]any {
		tif := o.TIF
		if tif == "" {
			tif = "GTC"
		}
		ou := map[string]any{"s": symOf(o.Instrument), "c": s.cid(acct, o.ID), "S": strings.ToUpper(o.Side), "o": typeOf(&o), "f": tif,
			"q": ds(o.Qty), "p": ds(o.Price), "ap": ds(o.AvgPx), "sp": ds(o.TriggerPx), "x": x, "X": statusOf(&o), "i": orderNum(o.ID),
			"l": "0", "z": ds(o.Filled), "L": "0", "N": "USDT", "n": "0", "T": o.Updated, "t": 0, "b": "0", "a": "0", "m": false,
			"R": o.ReduceOnly || o.ClosePos, "wt": working(&o), "ot": typeOf(&o), "ps": "BOTH", "cp": o.ClosePos, "rp": "0", "pP": false, "si": 0, "ss": 0, "V": "NONE", "pm": "NONE", "gtd": 0}
		if f != nil {
			ou["l"], ou["L"], ou["n"], ou["T"], ou["t"], ou["m"], ou["rp"] = ds(f.Qty), ds(f.Px), ds(f.Fee), f.Time, f.Seq, f.Maker, ds(f.Realized)
		}
		return map[string]any{"e": "ORDER_TRADE_UPDATE", "E": now, "T": now, "o": ou}
	}
	// 新成交：按成交一笔一笔推（价格、数量、手续费、实现盈亏都是那一笔的）
	if fills, _, _, err := s.E.Records(acct); err == nil {
		for i := range fills {
			f := fills[i]
			if f.Seq <= prev.fill {
				continue
			}
			prev.fill = f.Seq
			if o := s.E.OrderOf(acct, f.OrderID); o != nil {
				out = append(out, upd(*o, "TRADE", &f))
			}
		}
	}
	cur := map[string]trading.Order{}
	for _, o := range v.Orders {
		cur[o.ID] = *o
		if _, seen := prev.orders[o.ID]; !seen && o.Filled.IsZero() {
			out = append(out, upd(*o, "NEW", nil))
		}
	}
	for id := range prev.orders {
		if _, still := cur[id]; still {
			continue
		}
		o := s.E.OrderOf(acct, id)
		if o == nil || o.Status == trading.StFilled || o.Status == trading.StTriggered {
			continue // 成交完的已经按 TRADE 推过
		}
		x := "CANCELED"
		if o.Status == trading.StExpired {
			x = "EXPIRED"
		}
		out = append(out, upd(*o, x, nil))
	}
	prev.orders = cur
	next := s.snapOf(acct, v)
	changed := !next.wallet.Equal(prev.wallet) || len(next.pos) != len(prev.pos)
	for k, val := range next.pos {
		if prev.pos[k] != val {
			changed = true
		}
	}
	if changed {
		pos := []map[string]any{}
		for _, p := range v.Positions {
			amt := p.Qty
			if p.Side == "short" {
				amt = amt.Neg()
			}
			pos = append(pos, map[string]any{"s": symOf(p.Instrument), "pa": ds(amt), "ep": ds(p.Entry), "bep": ds(p.Entry), "cr": ds(p.Realized),
				"up": ds(p.UPnL), "mt": map[bool]string{true: "isolated", false: "cross"}[p.Mode == "isolated"], "iw": "0", "ps": "BOTH"})
		}
		for k := range prev.pos { // 平掉的仓位：数量 0
			if _, ok := next.pos[k]; !ok {
				pos = append(pos, map[string]any{"s": symOf(k), "pa": "0", "ep": "0", "bep": "0", "cr": "0", "up": "0", "mt": "cross", "iw": "0", "ps": "BOTH"})
			}
		}
		out = append(out, map[string]any{"e": "ACCOUNT_UPDATE", "E": now, "T": now, "a": map[string]any{"m": "ORDER",
			"B": []map[string]any{{"a": "USDT", "wb": ds(v.Wallet), "cw": ds(v.Wallet), "bc": "0"}}, "P": pos}})
	}
	prev.wallet, prev.pos = next.wallet, next.pos
	return out
}
