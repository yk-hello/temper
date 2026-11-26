package qsig

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"qs/internal/book"
	"qs/internal/portfolio"
)

// Key 是一把策略密钥。每个环境（测试、生产）单独一套；strategy_id 与 key 绑定。
type Key struct {
	ID         string
	Secret     string
	StrategyID string
}

// Instrument 是可交易品种的元数据（GET /v1/instruments）。
type Instrument struct {
	Instrument string   `json:"instrument"`
	Base       string   `json:"base"`
	Quote      string   `json:"quote"`
	Kind       string   `json:"kind"`
	Venues     []string `json:"venues"`
	Tradable   bool     `json:"tradable"`
}

// Record 是平台收到的一条消息（审计、执行回报、前端"AI 决策流"都用它）。
type Record struct {
	ID         string          `json:"id"`
	StrategyID string          `json:"strategy_id"`
	Type       string          `json:"type"`
	Status     string          `json:"status"`
	Code       string          `json:"code,omitempty"`
	ReceivedAt int64           `json:"received_at"`
	Raw        json.RawMessage `json:"message"`
	hash       [32]byte
	reply      Reply
}

type stratState struct {
	lastHeartbeat time.Time
	hbStatus      string
	hbNote        string
	sent          []time.Time // 最近一分钟的信号（限流）
}

type Service struct {
	Keys        map[string]Key // key id → key；运行中会加减（自助建的策略），读写都要拿 kmu
	Instruments map[string]Instrument
	Books       *book.Store
	RefVenue    string
	Portfolio   func(strategyID string) *portfolio.Portfolio
	Log         *slog.Logger
	Now         func() time.Time
	// OnRecord 每收到一条被接受的消息就回调（推给前端的 AI 决策流）
	OnRecord func(Record)
	// RatePerMin 每个策略每分钟最多几条信号（协议规定 30，测试时可调大）
	RatePerMin int
	// Persist 把消息写进数据库；写失败时这条消息按平台故障（500）处理，不执行，对方可用同一 id 重试
	Persist func(Stored) error

	kmu    sync.RWMutex
	cmu    sync.Mutex
	chains map[string]string // 策略 → 链的最后一环
	v      *validator
	mu     sync.Mutex
	recs   map[string]*Record
	order  []string
	strats map[string]*stratState
}

const (
	maxBody       = 256 << 10
	maxSkew       = 30 * time.Second
	maxValidAhead = 5 * time.Minute
	ratePerMin    = 30
	OfflineAfter  = 180 * time.Second // 超过这么久没有心跳（发出的信号也算）判定离线；策略页和掉线提醒用同一条线（D117）
	weightLimit   = 2
)

func NewService(keys []Key, instruments []Instrument, books *book.Store, refVenue string, pf func(string) *portfolio.Portfolio, log *slog.Logger) (*Service, error) {
	v, err := newValidator()
	if err != nil {
		return nil, err
	}
	s := &Service{Keys: map[string]Key{}, Instruments: map[string]Instrument{}, Books: books, RefVenue: refVenue, Portfolio: pf,
		Log: log, Now: time.Now, v: v, recs: map[string]*Record{}, strats: map[string]*stratState{}, RatePerMin: ratePerMin, chains: map[string]string{}}
	for _, k := range keys {
		s.Keys[k.ID] = k
	}
	for _, in := range instruments {
		s.Instruments[in.Instrument] = in
	}
	return s, nil
}

// Handler 挂在 /v1/ 下。
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/messages", s.auth(s.postMessage))
	mux.HandleFunc("GET /v1/messages/{id}", s.auth(s.getMessage))
	mux.HandleFunc("GET /v1/instruments", s.auth(s.getInstruments))
	mux.HandleFunc("GET /v1/ticker", s.auth(s.getTicker))
	mux.HandleFunc("GET /v1/portfolio", s.auth(s.getPortfolio))
	mux.HandleFunc("GET /v1/performance", s.auth(s.getPerformance))
	return mux
}

type authed func(w http.ResponseWriter, r *http.Request, key Key, body []byte)

func (s *Service) auth(next authed) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
		if err != nil || len(body) > maxBody {
			s.writeErr(w, "", reject(400, "INVALID_JSON", "消息体过大或读取失败"))
			return
		}
		key, ok := s.key(r.Header.Get("X-QS-Key"))
		ts, terr := strconv.ParseInt(r.Header.Get("X-QS-Timestamp"), 10, 64)
		if !ok || terr != nil || !verify(key.Secret, ts, r.Method, r.URL.RequestURI(), body, r.Header.Get("X-QS-Signature")) {
			s.writeErr(w, "", reject(401, "AUTH_FAILED", "签名校验失败"))
			return
		}
		if d := s.Now().Sub(time.UnixMilli(ts)); d > maxSkew || d < -maxSkew {
			s.writeErr(w, "", reject(401, "TIMESTAMP_SKEW", "请求时间与平台时间相差超过 30 秒，请校准时钟"))
			return
		}
		next(w, r, key, body)
	}
}

func (s *Service) postMessage(w http.ResponseWriter, r *http.Request, key Key, body []byte) {
	received := s.Now()
	var m Message
	if err := json.Unmarshal(body, &m); err != nil {
		s.writeErr(w, "", reject(400, "INVALID_JSON", "不是合法的 JSON"))
		return
	}
	if m.Protocol != "qsig/1" {
		s.writeErr(w, m.ID, reject(400, "UNSUPPORTED_PROTOCOL", "protocol 必须是 qsig/1"))
		return
	}
	if e := s.v.validate(m.Type, body); e != nil {
		s.writeErr(w, m.ID, e)
		return
	}
	if m.StrategyID != key.StrategyID {
		s.writeErr(w, m.ID, reject(403, "STRATEGY_MISMATCH", "strategy_id 与密钥不符"))
		return
	}
	hash := sha256.Sum256(canonical(body))

	s.mu.Lock()
	if old, ok := s.recs[m.ID]; ok {
		s.mu.Unlock()
		if old.hash == hash {
			rep := old.reply
			if rep.Status == "accepted" {
				rep.Status = "duplicate"
				s.writeJSON(w, 200, rep)
				return
			}
			s.writeJSON(w, httpOfCode(rep.Code), rep) // 被拒的重发，还是同样的拒绝
			return
		}
		s.writeErr(w, m.ID, reject(409, "ID_CONFLICT", "同一个 id 已收到过不同的内容"))
		return
	}
	st := s.strats[m.StrategyID]
	if st == nil {
		st = &stratState{}
		s.strats[m.StrategyID] = st
	}
	if m.Type == "signal" {
		cut := received.Add(-time.Minute)
		kept := st.sent[:0]
		for _, t := range st.sent {
			if t.After(cut) {
				kept = append(kept, t)
			}
		}
		st.sent = kept
		if len(st.sent) >= s.RatePerMin {
			s.mu.Unlock()
			w.Header().Set("Retry-After", "5")
			s.writeErr(w, m.ID, reject(429, "RATE_LIMITED", "每个策略每分钟最多 30 条信号"))
			return
		}
	}
	s.mu.Unlock()

	pf := s.Portfolio(m.StrategyID)
	var warnings []Warning
	var apply func()
	switch m.Type {
	case "signal":
		targets, warn, e := s.checkSignal(&m, pf, received)
		if e != nil {
			if _, perr := s.store(m, body, hash, received, e, Reply{}); perr != nil {
				s.Log.Error("qsig persist", "err", perr)
			}
			s.writeErr(w, m.ID, e)
			return
		}
		warnings = warn
		issued, _ := parseTime(m.IssuedAt)
		valid, _ := parseTime(m.ValidUntil)
		apply = func() { pf.Apply(m.ID, issued.UnixMilli(), valid.UnixMilli(), m.Mode == "snapshot", targets) }
	case "heartbeat":
		s.mu.Lock()
		st.lastHeartbeat, st.hbStatus, st.hbNote = received, m.Status, m.Note
		s.mu.Unlock()
	case "halt":
		reason := ""
		if m.Reason != nil {
			reason = m.Reason.Zh
		}
		apply = func() { pf.Halt(m.Action, reason) }
	}
	rep := Reply{Status: "accepted", ID: m.ID, ReceivedAt: received.UTC().Format(TimeLayout), Warnings: warnings}
	s.mu.Lock()
	if m.Type == "signal" {
		// 收到信号说明程序在跑：也算一次心跳（11-16 走查：示例脚本只发了一条信号，策略页马上写"策略离线"）。
		// 心跳里的状态（degraded、paused）照旧，只有从没发过心跳时记成 ok
		st.lastHeartbeat = received
		if st.hbStatus == "" {
			st.hbStatus = "ok"
		}
		st.sent = append(st.sent, received)
	}
	s.mu.Unlock()
	rec, perr := s.store(m, body, hash, received, nil, rep)
	if perr != nil {
		s.mu.Lock()
		delete(s.recs, m.ID)
		if m.Type == "signal" && len(st.sent) > 0 {
			st.sent = st.sent[:len(st.sent)-1]
		}
		s.mu.Unlock()
		s.Log.Error("qsig persist", "err", perr)
		s.writeErr(w, m.ID, reject(500, "INTERNAL_ERROR", "平台暂时无法保存消息，请用同一个 id 重试"))
		return
	}
	s.writeJSON(w, 200, rep)
	if apply != nil {
		go apply()
	}
	if s.OnRecord != nil {
		s.OnRecord(*rec)
	}
}

// checkSignal 业务规则检查（04 第 1–4 节）。
func (s *Service) checkSignal(m *Message, pf *portfolio.Portfolio, now time.Time) ([]portfolio.Target, []Warning, *Error) {
	issued, err1 := parseTime(m.IssuedAt)
	valid, err2 := parseTime(m.ValidUntil)
	asOf, err3 := parseTime(m.DataAsOf)
	if err1 != nil || err2 != nil || err3 != nil {
		e := reject(400, "SCHEMA_VIOLATION", "时间格式不正确")
		e.Details = map[string]any{"path": "/issued_at"}
		return nil, nil, e
	}
	if !valid.After(issued) || valid.Sub(issued) > maxValidAhead {
		e := reject(400, "SCHEMA_VIOLATION", "valid_until 必须晚于 issued_at，且最多晚 5 分钟")
		e.Details = map[string]any{"path": "/valid_until"}
		return nil, nil, e
	}
	if !now.Before(valid) {
		return nil, nil, reject(422, "EXPIRED", "收到时信号已过期")
	}
	if issued.After(now.Add(maxSkew)) {
		e := reject(400, "SCHEMA_VIOLATION", "issued_at 比平台时间晚太多，检查策略端的时钟")
		e.Details = map[string]any{"path": "/issued_at"}
		return nil, nil, e
	}
	seen := map[string]bool{}
	total := decimal.Zero
	var out []portfolio.Target
	var warnings []Warning
	increase := false
	for i, t := range m.Targets {
		in, ok := s.Instruments[t.Instrument]
		if !ok {
			e := reject(422, "UNKNOWN_INSTRUMENT", "不认识的品种 "+t.Instrument)
			e.Details = map[string]any{"path": "/targets/" + strconv.Itoa(i) + "/instrument"}
			return nil, nil, e
		}
		if !in.Tradable {
			return nil, nil, reject(422, "INSTRUMENT_NOT_TRADABLE", t.Instrument+" 暂不可交易")
		}
		if seen[t.Instrument] {
			e := reject(400, "SCHEMA_VIOLATION", "同一条信号里品种重复："+t.Instrument)
			e.Details = map[string]any{"path": "/targets/" + strconv.Itoa(i) + "/instrument"}
			return nil, nil, e
		}
		seen[t.Instrument] = true
		if last := pf.LastIssued(t.Instrument); last > 0 && issued.UnixMilli() < last {
			return nil, nil, reject(422, "STALE", t.Instrument+" 已执行过更晚的判断，这条信号晚到了")
		}
		w := dec(t.Weight)
		total = total.Add(w.Abs())
		if pf.IsIncrease(t.Instrument, w) {
			increase = true
		}
		tg := portfolio.Target{Instrument: t.Instrument, Weight: w, RefPrice: dec(t.RefPrice), Urgency: portfolio.DefaultUrgen,
			MaxSlipBps: portfolio.DefaultSlip, HorizonMin: t.HorizonMin}
		if t.Execution != nil {
			if t.Execution.Urgency != "" {
				tg.Urgency = t.Execution.Urgency
			}
			if t.Execution.MaxSlippageBps > 0 {
				tg.MaxSlipBps = t.Execution.MaxSlippageBps
			}
		}
		if t.Exits != nil {
			tg.Exits = &portfolio.Exits{StopLoss: dec(t.Exits.StopLossPct), TakeProfit: dec(t.Exits.TakeProfitPct)}
		}
		if px, ok := pf.RefPrice(t.Instrument); ok && tg.RefPrice.Sign() > 0 {
			if px.Sub(tg.RefPrice).Abs().Div(px).GreaterThan(decimal.RequireFromString("0.005")) {
				warnings = append(warnings, Warning{"REF_PRICE_DEVIATION", t.Instrument + " 的 ref_price 与平台价格偏差超过 0.5%（平台价 " + px.String() + "），请检查价格来源"})
			}
		}
		out = append(out, tg)
	}
	// 总杠杆：partial 模式要加上没列出的品种的现有目标
	if m.Mode == "partial" {
		for _, p := range pf.State().Positions {
			if !seen[p.Instrument] {
				total = total.Add(dec(p.TargetWeight).Abs())
			}
		}
	}
	if total.GreaterThan(decimal.NewFromInt(weightLimit)) {
		return nil, nil, reject(422, "WEIGHT_LIMIT_EXCEEDED", "所有品种 |weight| 之和超过 2（"+total.String()+"）")
	}
	if increase {
		if h, _ := pf.Halted(); h != "" {
			return nil, nil, reject(422, "STRATEGY_PAUSED", "策略已暂停，只接受减仓和平仓")
		}
		if pf.Breaker() {
			return nil, nil, reject(422, "CIRCUIT_BREAKER", "今日亏损超过 5% 已熔断，只接受减仓和平仓")
		}
	}
	if issued.Sub(asOf) > 600*time.Second {
		warnings = append(warnings, Warning{"LONG_LATENCY", "issued_at 比 data_as_of 晚了超过 600 秒"})
	}
	return out, warnings, nil
}

// Stored 是写进数据库的一条消息。
type Stored struct {
	ID, StrategyID, Type, Status, Code string
	ReceivedAt                         time.Time
	Body                               []byte
	Hash                               [32]byte
	Reply                              []byte
	// 前向记录（D96 第 2 条）：每个策略的消息串成一条链，每条带上一条的指纹，改了中间任何一条后面全对不上
	Prev, Chain string
}

// ChainHash 链上一环：sha256(上一环 + 换行 + 消息 id + 换行 + 收到时间毫秒 + 换行 + sha256(收到的原始字节))，都是小写十六进制。
// 第一环的"上一环"是空字符串。用原始字节而不是整理过的 JSON：任何语言都能照着算（docs/examples/verify_log.py）。
func ChainHash(prev, id string, receivedMs int64, raw []byte) string {
	b := sha256.Sum256(raw)
	h := sha256.Sum256([]byte(prev + "\n" + id + "\n" + strconv.FormatInt(receivedMs, 10) + "\n" + hex.EncodeToString(b[:])))
	return hex.EncodeToString(h[:])
}

// SetChains 启动时接上每个策略链的最后一环。
func (s *Service) SetChains(last map[string]string) {
	s.cmu.Lock()
	defer s.cmu.Unlock()
	for k, v := range last {
		s.chains[k] = v
	}
}

func (s *Service) store(m Message, body []byte, hash [32]byte, at time.Time, e *Error, accepted Reply) (rec2 *Record, err error) {
	rec := &Record{ID: m.ID, StrategyID: m.StrategyID, Type: m.Type, Status: "accepted", ReceivedAt: at.UnixMilli(), Raw: append(json.RawMessage(nil), body...), hash: hash, reply: accepted}
	if e != nil {
		rec.Status, rec.Code = "rejected", e.Code
		rec.reply = Reply{Status: "rejected", ID: m.ID, ReceivedAt: at.UTC().Format(TimeLayout), Code: e.Code, Message: e.Msg, Details: e.Details}
	}
	// 链的顺序必须和写库的顺序一样：从算这一环到写完库一直拿着 cmu
	s.cmu.Lock()
	defer s.cmu.Unlock()
	prev := s.chains[m.StrategyID]
	chain := ChainHash(prev, m.ID, at.UnixMilli(), body)
	defer func() {
		if err == nil {
			s.chains[m.StrategyID] = chain
		}
	}()
	s.mu.Lock()
	// 被拒绝的消息也记下（同 id 重发同样内容会得到同样的拒绝），限流的不记（允许重试）
	s.recs[m.ID] = rec
	s.order = append(s.order, m.ID)
	s.mu.Unlock()
	if s.Persist != nil {
		rb, _ := json.Marshal(rec.reply)
		defer func() {
			if err != nil {
				s.mu.Lock()
				delete(s.recs, m.ID)
				if n := len(s.order); n > 0 && s.order[n-1] == m.ID {
					s.order = s.order[:n-1]
				} else {
					for i := n - 1; i >= 0; i-- {
						if s.order[i] == m.ID {
							s.order = append(s.order[:i], s.order[i+1:]...)
							break
						}
					}
				}
				s.mu.Unlock()
			}
		}()
		if err := s.Persist(Stored{ID: rec.ID, StrategyID: rec.StrategyID, Type: rec.Type, Status: rec.Status, Code: rec.Code,
			ReceivedAt: at, Body: body, Hash: hash, Reply: rb, Prev: prev, Chain: chain}); err != nil {
			return rec, err
		}
	}
	return rec, nil
}

// Load 重启时恢复最近的消息（同 id 重发仍能判为重复；前端决策流有历史）。必须在开始接收之前调用。
func (s *Service) Load(list []Stored) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, st := range list {
		var rep Reply
		_ = json.Unmarshal(st.Reply, &rep)
		s.recs[st.ID] = &Record{ID: st.ID, StrategyID: st.StrategyID, Type: st.Type, Status: st.Status, Code: st.Code,
			ReceivedAt: st.ReceivedAt.UnixMilli(), Raw: st.Body, hash: st.Hash, reply: rep}
		s.order = append(s.order, st.ID)
		// 最后一次心跳也从记录里接上（11-17 走查：心跳只记在内存，重启后"系统"页写"开机后还没收到"、
		// 策略页写"离线"，其实几分钟前刚发过）。收到的信号也算心跳，和接收时一样
		if st.Status != "accepted" || (st.Type != "heartbeat" && st.Type != "signal") {
			continue
		}
		ss := s.strats[st.StrategyID]
		if ss == nil {
			ss = &stratState{}
			s.strats[st.StrategyID] = ss
		}
		if !st.ReceivedAt.After(ss.lastHeartbeat) {
			continue
		}
		ss.lastHeartbeat = st.ReceivedAt
		if st.Type == "heartbeat" {
			var hb struct {
				Status string `json:"status"`
				Note   string `json:"note"`
			}
			_ = json.Unmarshal(st.Body, &hb)
			ss.hbStatus, ss.hbNote = hb.Status, hb.Note
		} else if ss.hbStatus == "" {
			ss.hbStatus = "ok"
		}
	}
}

func (s *Service) getMessage(w http.ResponseWriter, r *http.Request, key Key, _ []byte) {
	id := r.PathValue("id")
	s.mu.Lock()
	rec, ok := s.recs[id]
	s.mu.Unlock()
	if !ok || rec.StrategyID != key.StrategyID {
		s.writeJSON(w, 404, map[string]string{"code": "NOT_FOUND", "message": "没有这条消息"})
		return
	}
	out := map[string]any{"id": rec.ID, "status": rec.Status, "type": rec.Type, "received_at": time.UnixMilli(rec.ReceivedAt).UTC().Format(TimeLayout)}
	if rec.Code != "" {
		out["code"] = rec.Code
	}
	if rec.Type == "signal" && rec.Status == "accepted" {
		out["executions"] = s.Portfolio(rec.StrategyID).Executions(rec.ID)
	}
	s.writeJSON(w, 200, out)
}

func (s *Service) getInstruments(w http.ResponseWriter, _ *http.Request, _ Key, _ []byte) {
	list := make([]Instrument, 0, len(s.Instruments))
	for _, in := range s.Instruments {
		list = append(list, in)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Instrument < list[j].Instrument })
	s.writeJSON(w, 200, map[string]any{"instruments": list})
}

func (s *Service) getTicker(w http.ResponseWriter, r *http.Request, _ Key, _ []byte) {
	inst := r.URL.Query().Get("instrument")
	if _, ok := s.Instruments[inst]; !ok {
		s.writeJSON(w, 404, map[string]string{"code": "UNKNOWN_INSTRUMENT", "message": "不认识的品种"})
		return
	}
	t, _ := s.Books.Ticker(s.RefVenue, inst)
	b, _ := s.Books.Book(s.RefVenue, inst)
	out := map[string]any{"instrument": inst, "last": t.Last.String(), "mark": t.Mark.String(), "funding_rate": t.FundingRate.String(),
		"next_funding_time": t.NextFunding, "ts": t.Time}
	if l, ok := b.BestBid(); ok {
		out["bid"] = l.Px.String()
	}
	if l, ok := b.BestAsk(); ok {
		out["ask"] = l.Px.String()
	}
	s.writeJSON(w, 200, out)
}

func (s *Service) getPortfolio(w http.ResponseWriter, _ *http.Request, key Key, _ []byte) {
	s.writeJSON(w, 200, s.Portfolio(key.StrategyID).State())
}

func (s *Service) getPerformance(w http.ResponseWriter, r *http.Request, key Key, _ []byte) {
	window := r.URL.Query().Get("window")
	pf := s.Portfolio(key.StrategyID)
	hist := pf.History()
	cut := int64(0)
	switch window {
	case "7d":
		cut = s.Now().Add(-7 * 24 * time.Hour).UnixMilli()
	case "30d":
		cut = s.Now().Add(-30 * 24 * time.Hour).UnixMilli()
	}
	var pts []portfolio.NAVPoint
	for _, p := range hist {
		if p.T >= cut {
			pts = append(pts, p)
		}
	}
	out := map[string]any{"window": window, "points": len(pts)}
	if len(pts) > 1 {
		start, end := pts[0].NAV, pts[len(pts)-1].NAV
		peak, mdd := pts[0].NAV, 0.0
		for _, p := range pts {
			if p.NAV > peak {
				peak = p.NAV
			}
			if dd := (peak - p.NAV) / peak; dd > mdd {
				mdd = dd
			}
		}
		out["return"] = (end - start) / start
		out["max_drawdown"] = mdd
	}
	out["note"] = "完整的考核报告（夏普、盈亏比、对照组等，见 04 第 6 节）在后续版本提供"
	s.writeJSON(w, 200, out)
}

// StrategyStatus 给前端和监控用：心跳、是否离线、暂停、熔断。
type StrategyStatus struct {
	StrategyID    string `json:"strategy_id"`
	Online        bool   `json:"online"`
	LastHeartbeat int64  `json:"last_heartbeat"`
	Heartbeat     string `json:"heartbeat_status"`
	Note          string `json:"note,omitempty"`
	Halted        string `json:"halted,omitempty"`
	HaltReason    string `json:"halt_reason,omitempty"`
	Breaker       bool   `json:"circuit_breaker"`
	Sample        bool   `json:"sample"` // 示例策略：界面上要写明
}

// Status 每个策略的在线状态。没有策略时返回空列表（不是 nil：接口要回 []，回 null 前端会一直显示"加载中"）。
func (s *Service) Status() []StrategyStatus {
	out := s.status()
	if out == nil {
		out = []StrategyStatus{}
	}
	return out
}

func (s *Service) status() []StrategyStatus {
	ids := map[string]bool{}
	s.kmu.RLock()
	for _, k := range s.Keys {
		ids[k.StrategyID] = true
	}
	s.kmu.RUnlock()
	s.mu.Lock()
	snap := map[string]stratState{}
	for id, st := range s.strats {
		snap[id] = *st
	}
	s.mu.Unlock()
	var out []StrategyStatus
	for id := range ids {
		st := snap[id]
		pf := s.Portfolio(id)
		h, reason := pf.Halted()
		out = append(out, StrategyStatus{StrategyID: id, Online: !st.lastHeartbeat.IsZero() && s.Now().Sub(st.lastHeartbeat) <= OfflineAfter,
			LastHeartbeat: unixMilli(st.lastHeartbeat), Heartbeat: st.hbStatus, Note: st.hbNote, Halted: h, HaltReason: reason, Sample: id == DemoStrategyID, Breaker: pf.Breaker()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StrategyID < out[j].StrategyID })
	return out
}

func (s *Service) key(id string) (Key, bool) {
	s.kmu.RLock()
	defer s.kmu.RUnlock()
	k, ok := s.Keys[id]
	return k, ok
}

// AddKey 运行中加一把密钥（自助建的策略、换密钥）。
func (s *Service) AddKey(k Key) {
	s.kmu.Lock()
	defer s.kmu.Unlock()
	s.Keys[k.ID] = k
}

// RemoveKey 吊销：这把密钥签的消息马上不认。
func (s *Service) RemoveKey(id string) {
	s.kmu.Lock()
	defer s.kmu.Unlock()
	delete(s.Keys, id)
}

// HasStrategy 有没有这个策略（有一把能用的密钥）。
func (s *Service) HasStrategy(id string) bool {
	s.kmu.RLock()
	defer s.kmu.RUnlock()
	for _, k := range s.Keys {
		if k.StrategyID == id {
			return true
		}
	}
	return false
}

// Resume 管理员恢复一个被暂停的策略（策略自己发的 halt 只能人工恢复，D-qsig/1）。没有这个策略返回 false。
func (s *Service) Resume(id string) bool {
	if !s.HasStrategy(id) {
		return false
	}
	s.Portfolio(id).Resume()
	return true
}

// Recent 最近的消息（前端 AI 决策流），新的在前。
func (s *Service) Recent(n int) []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Record
	for i := len(s.order) - 1; i >= 0 && len(out) < n; i-- {
		if r := s.recs[s.order[i]]; r != nil && r.Type != "heartbeat" { // 第 18 条：心跳不占名额
			out = append(out, *r)
		}
	}
	return out
}

func unixMilli(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// httpOfCode 错误码对应的状态码（04 第 5 节）。
func httpOfCode(code string) int {
	switch code {
	case "INVALID_JSON", "SCHEMA_VIOLATION", "UNSUPPORTED_PROTOCOL":
		return 400
	case "AUTH_FAILED", "TIMESTAMP_SKEW":
		return 401
	case "STRATEGY_MISMATCH":
		return 403
	case "ID_CONFLICT":
		return 409
	case "RATE_LIMITED":
		return 429
	case "INTERNAL_ERROR":
		return 500
	}
	return 422
}

func (s *Service) writeErr(w http.ResponseWriter, id string, e *Error) {
	s.writeJSON(w, e.HTTP, Reply{Status: "rejected", ID: id, ReceivedAt: s.Now().UTC().Format(TimeLayout), Code: e.Code, Message: e.Msg, Details: e.Details})
}

func (s *Service) writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// canonical 去掉 JSON 的空白差异后再算哈希，用于判断"同一 id 内容是否相同"。
func canonical(body []byte) []byte {
	var v any
	if json.Unmarshal(body, &v) != nil {
		return body
	}
	b, _ := json.Marshal(v)
	return b
}

var _ = strings.ToUpper
