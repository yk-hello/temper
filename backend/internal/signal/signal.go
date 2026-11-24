// Package signal 用户自己的信号（D48、D83）：TradingView 警报和 Webhook。
// 格式比 qsig 简单，比如"做多 BTC 30%"。下单照样过平台风控（纪律锁、引擎优先级）。
// 没有签名（TradingView 发不了），靠地址里的密钥：只存哈希，只在创建时显示一次。
package signal

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"qs/internal/auth"
)

// Command 解析出来的一条信号。
type Command struct {
	Action string          // long | short | close | buy | sell
	Coin   string          // BTC
	Spot   bool            // 现货
	Pct    decimal.Decimal // 百分比，0 ~ 100；close 不填就是全部
	ID     string          // 警报 id：重复的不执行
}

var (
	hund    = decimal.NewFromInt(100)
	pctRe   = regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*%?$`)
	coinRe  = regexp.MustCompile(`^[A-Z0-9]{2,12}$`)
	actions = map[string]string{"做多": "long", "long": "long", "买多": "long", "做空": "short", "short": "short", "卖空": "short", "平仓": "close", "close": "close", "exit": "close", "flat": "close", "买入": "buy", "buy": "buy", "卖出": "sell", "sell": "sell"}
)

// Parse 解析警报内容：JSON（{"action":"long","symbol":"BTC","size":"30%"}）或一句话（做多 BTC 30%）。
func Parse(body []byte) (*Command, error) {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return nil, rej("SIG_EMPTY")
	}
	c := &Command{}
	var action, symbol, size, market string
	if strings.HasPrefix(text, "{") {
		var in struct {
			Action string          `json:"action"`
			Symbol string          `json:"symbol"`
			Ticker string          `json:"ticker"`
			Size   json.RawMessage `json:"size"`
			Market string          `json:"market"`
			ID     string          `json:"id"`
		}
		if err := json.Unmarshal(body, &in); err != nil {
			return nil, rej("SIG_BAD_JSON")
		}
		action, symbol, market, c.ID = in.Action, in.Symbol, in.Market, in.ID
		if symbol == "" {
			symbol = in.Ticker
		}
		size = strings.Trim(string(in.Size), `" `)
	} else {
		// TradingView 策略警报的默认消息："buy @ 0.01 filled on BTCUSDT.P. New strategy position is 0.01"。
		// 写的是张数，这里按资金百分比下单，猜不出该下多少：明确告诉用户怎么改（11-18）
		if strings.Contains(strings.ToLower(text), " filled on ") {
			return nil, rej("SIG_TV_DEFAULT")
		}
		for _, w := range strings.Fields(text) {
			lw := strings.ToLower(w)
			switch {
			case actions[lw] != "" && action == "":
				action = lw
			case lw == "spot" || lw == "现货":
				market = "spot"
			case lw == "perp" || lw == "合约":
				market = "perp"
			case pctRe.MatchString(w) && strings.ContainsAny(w, "%") && size == "":
				size = w
			case symbol == "":
				symbol = w
			case size == "" && pctRe.MatchString(w):
				size = w
			}
		}
	}
	c.Action = actions[strings.ToLower(strings.TrimSpace(action))]
	if c.Action == "" {
		return nil, rej("SIG_BAD_ACTION")
	}
	// BTCUSDT、BTCUSDT.P、BINANCE:BTCUSDT.P、BTC-PERP 都认
	s := strings.ToUpper(strings.TrimSpace(symbol))
	if i := strings.LastIndex(s, ":"); i >= 0 {
		s = s[i+1:]
	}
	// 品种写明了是永续（TradingView 的 .P、我们的 -PERP）或现货（-SPOT），没写 market 时按它来
	if market == "" {
		switch {
		case strings.HasSuffix(s, ".P") || strings.HasSuffix(s, "-PERP"):
			market = "perp"
		case strings.HasSuffix(s, "-SPOT"):
			market = "spot"
		}
	}
	s = strings.TrimSuffix(s, ".P")
	s = strings.TrimSuffix(strings.TrimSuffix(s, "-PERP"), "-SPOT")
	s = strings.TrimSuffix(s, "USDT")
	if !coinRe.MatchString(s) {
		return nil, rej("SIG_BAD_SYMBOL")
	}
	c.Coin = s
	// 买入 / 卖出默认是现货；明说了合约（market: perp、BTCUSDT.P）就是合约的买卖，按做多 / 做空下单。
	// 原来 "buy BTCUSDT.P 30%" 会去买现货，"sell" + market perp 也去卖现货（11-18）
	c.Spot = market == "spot" || market != "perp" && (c.Action == "buy" || c.Action == "sell")
	if !c.Spot {
		switch c.Action {
		case "buy":
			c.Action = "long"
		case "sell":
			c.Action = "short"
		}
	}
	if size != "" {
		m := pctRe.FindStringSubmatch(size)
		if m == nil {
			return nil, rej("SIG_BAD_SIZE")
		}
		c.Pct, _ = decimal.NewFromString(m[1])
		if !c.Pct.IsPositive() || c.Pct.GreaterThan(hund) {
			return nil, rej("SIG_BAD_SIZE")
		}
	} else if c.Action != "close" {
		return nil, rej("SIG_BAD_SIZE")
	}
	return c, nil
}

// Rej 拒绝。
type Rej struct{ Code string }

func (r *Rej) Error() string { return r.Code }

// RejectCode 见 trading.Reject。
func (r *Rej) RejectCode() string { return r.Code }

func rej(code string) *Rej { return &Rej{Code: code} }

// Trader 下单通道。source 固定是 signal，这样纪律锁和优先级都认得。
type Trader interface {
	// Perp 合约：side 买卖，notional 金额（USDT）；close 为真时平掉整个仓位。
	Perp(acct int64, inst, side string, notional decimal.Decimal, closeAll bool) (string, error)
	// Spot 现货：买入 pct% 的可用 USDT，或卖出 pct% 的币。
	Spot(acct int64, inst, side string, pct decimal.Decimal) (string, error)
	// Budget 这个账户合约可用。
	Available(acct int64) decimal.Decimal
}

// Hook 一个信号入口。
type Hook struct {
	ID      int64           `json:"id"`
	User    int64           `json:"-"`
	Account int64           `json:"account"`
	Name    string          `json:"name"`
	Budget  decimal.Decimal `json:"budget"` // 资金桶：30% 是它的 30%；0 = 账户可用
	Enabled bool            `json:"enabled"`
	Created int64           `json:"created"`
	Hash    string          `json:"-"`
}

// Log 一条收到的信号。
type Log struct {
	T      int64  `json:"t"`
	Hook   int64  `json:"hook"`
	Raw    string `json:"raw"`
	Status string `json:"status"` // ok | rejected | duplicate
	Code   string `json:"code,omitempty"`
	Order  string `json:"order,omitempty"`
}

const schema = `
create table if not exists signal_hooks (
  id      bigserial primary key,
  usr     bigint not null,
  account bigint not null,
  name    text   not null,
  budget  numeric not null default 0,
  enabled boolean not null default true,
  created bigint not null,
  hash    text   not null unique
);
create table if not exists signal_logs (
  id    bigserial primary key,
  hook  bigint not null,
  t     bigint not null,
  raw   text   not null,
  status text  not null,
  code  text   not null default '',
  ord   text   not null default ''
);
create index if not exists signal_logs_hook on signal_logs (hook, id);
`

// Service 服务。
type Service struct {
	T     Trader
	Log   *slog.Logger
	Now   func() int64
	Known func(inst string) bool
	// Owns 这个账户能不能绑信号（模拟账户和交易所账户）。没设置时只认模拟账户
	Owns func(ctx context.Context, user, acct int64) bool
	// RealMoney 交易所账户（真钱）：给它建信号地址要二级密码。地址里带着密钥，谁拿到谁就能从任何地方往这个账户下单，
	// 坐到你电脑前的人建一个抄走，走了以后照样能用（D107 核对二级密码时发现）
	RealMoney func(acct int64) bool

	// Received 收到的信号总数（系统控制台）
	Received atomic.Int64

	pool *pgxpool.Pool
	mu   sync.Mutex
	seen map[string]int64 // 重复判断：hook + id 或内容 → 时间
	hits map[int64][]int64
	mem  map[int64]*Hook // 没有数据库时（测试）
	logs []Log
	next int64
}

// New 新建。
func New(t Trader, log *slog.Logger) *Service {
	return &Service{T: t, Log: log, seen: map[string]int64{}, hits: map[int64][]int64{}, mem: map[int64]*Hook{}, next: 1}
}

// Attach 接数据库。
func (s *Service) Attach(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, schema); err != nil {
		return err
	}
	s.pool = pool
	return nil
}

func (s *Service) now() int64 {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UnixMilli()
}

func hashOf(tok string) string {
	h := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(h[:])
}

// Create 建一个入口，返回只显示这一次的密钥。
func (s *Service) Create(ctx context.Context, user, acct int64, name string, budget decimal.Decimal) (*Hook, string, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, "", err
	}
	tok := hex.EncodeToString(b[:])
	h := &Hook{User: user, Account: acct, Name: name, Budget: budget, Enabled: true, Created: s.now(), Hash: hashOf(tok)}
	if s.pool != nil {
		if err := s.pool.QueryRow(ctx, `insert into signal_hooks (usr,account,name,budget,created,hash) values ($1,$2,$3,$4,$5,$6) returning id`, user, acct, name, budget, h.Created, h.Hash).Scan(&h.ID); err != nil {
			return nil, "", err
		}
	} else {
		s.mu.Lock()
		h.ID = s.next
		s.next++
		s.mem[h.ID] = h
		s.mu.Unlock()
	}
	return h, tok, nil
}

func (s *Service) byToken(ctx context.Context, tok string) (*Hook, error) {
	hash := hashOf(tok)
	if s.pool == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, h := range s.mem {
			if h.Hash == hash {
				return h, nil
			}
		}
		return nil, rej("SIG_NO_HOOK")
	}
	h := &Hook{}
	err := s.pool.QueryRow(ctx, `select id,usr,account,name,budget,enabled,created from signal_hooks where hash=$1`, hash).Scan(&h.ID, &h.User, &h.Account, &h.Name, &h.Budget, &h.Enabled, &h.Created)
	if err != nil {
		return nil, rej("SIG_NO_HOOK")
	}
	return h, nil
}

// List 这个用户的入口。
func (s *Service) List(ctx context.Context, user int64) ([]Hook, error) {
	out := []Hook{}
	if s.pool == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, h := range s.mem {
			if h.User == user {
				out = append(out, *h)
			}
		}
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `select id,account,name,budget,enabled,created from signal_hooks where usr=$1 order by id desc`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		h := Hook{User: user}
		if err := rows.Scan(&h.ID, &h.Account, &h.Name, &h.Budget, &h.Enabled, &h.Created); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// Delete 删掉入口：地址立刻失效。
func (s *Service) Delete(ctx context.Context, user, id int64) error {
	if s.pool == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if h := s.mem[id]; h != nil && h.User == user {
			delete(s.mem, id)
		}
		return nil
	}
	_, err := s.pool.Exec(ctx, `delete from signal_hooks where id=$1 and usr=$2`, id, user)
	return err
}

// Logs 最近收到的信号。
func (s *Service) Logs(ctx context.Context, user int64, limit int) ([]Log, error) {
	out := []Log{}
	if s.pool == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		for i := len(s.logs) - 1; i >= 0 && len(out) < limit; i-- {
			if h := s.mem[s.logs[i].Hook]; h != nil && h.User == user {
				out = append(out, s.logs[i])
			}
		}
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `select l.t,l.hook,l.raw,l.status,l.code,l.ord from signal_logs l join signal_hooks h on h.id=l.hook where h.usr=$1 order by l.id desc limit $2`, user, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var l Log
		if err := rows.Scan(&l.T, &l.Hook, &l.Raw, &l.Status, &l.Code, &l.Order); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Service) record(ctx context.Context, l Log) {
	if len(l.Raw) > 500 {
		l.Raw = l.Raw[:500]
	}
	if s.pool != nil {
		if _, err := s.pool.Exec(ctx, `insert into signal_logs (hook,t,raw,status,code,ord) values ($1,$2,$3,$4,$5,$6)`, l.Hook, l.T, l.Raw, l.Status, l.Code, l.Order); err != nil && s.Log != nil {
			s.Log.Warn("存信号记录失败", "err", err)
		}
		return
	}
	s.mu.Lock()
	s.logs = append(s.logs, l)
	s.mu.Unlock()
}

// allow 每个入口每分钟最多 30 条；重复的（同一个 id，或者没有 id 时同样内容 5 秒内）不执行。
func (s *Service) allow(hook int64, key string, now int64, window int64) (dup, limited bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, t := range s.seen {
		if now-t > 3_600_000 {
			delete(s.seen, k)
		}
	}
	if t, ok := s.seen[key]; ok && now-t < window {
		return true, false
	}
	hits := s.hits[hook][:0]
	for _, t := range s.hits[hook] {
		if now-t < 60_000 {
			hits = append(hits, t)
		}
	}
	if len(hits) >= 30 {
		s.hits[hook] = hits
		return false, true
	}
	s.hits[hook] = append(hits, now)
	s.seen[key] = now
	return false, false
}

// forget 这条没下出去（我们这边明确拒了）：同一个 id 再发要能重试，不能算重复（11-18）
func (s *Service) forget(key string) {
	s.mu.Lock()
	delete(s.seen, key)
	s.mu.Unlock()
}

// Receive 收到一条信号：解析、去重、下单。返回写进日志的结果。
func (s *Service) Receive(ctx context.Context, tok string, body []byte) (Log, error) {
	h, err := s.byToken(ctx, tok)
	if err != nil {
		return Log{}, err // 地址不对不记录，也不说明原因
	}
	s.Received.Add(1)
	now := s.now()
	l := Log{T: now, Hook: h.ID, Raw: strings.TrimSpace(string(body)), Status: "ok"}
	fail := func(e error) (Log, error) {
		l.Status = "rejected"
		l.Code = codeOf(e)
		s.record(ctx, l)
		return l, nil
	}
	if !h.Enabled {
		return fail(rej("SIG_DISABLED"))
	}
	cmd, err := Parse(body)
	if err != nil {
		return fail(err)
	}
	key, window := strconv.FormatInt(h.ID, 10)+":"+cmd.ID, int64(3_600_000)
	if cmd.ID == "" {
		sum := sha256.Sum256(body)
		key, window = strconv.FormatInt(h.ID, 10)+":"+hex.EncodeToString(sum[:8]), 5_000
	}
	dup, limited := s.allow(h.ID, key, now, window)
	if dup {
		l.Status = "duplicate"
		s.record(ctx, l)
		return l, nil
	}
	if limited {
		return fail(rej("SIG_RATE_LIMITED"))
	}
	// 之后被拒（金额太小、纪律锁、行情断了、没有持仓）：单没下出去，忘掉这个 id，改好了可以重发。
	// 说不清有没有下出去的错误（INTERNAL_ERROR，比如交易所超时）仍然算发过，免得重复下单
	fail = func(e error) (Log, error) {
		l.Status = "rejected"
		l.Code = codeOf(e)
		if l.Code != "INTERNAL_ERROR" {
			s.forget(key)
		}
		s.record(ctx, l)
		return l, nil
	}
	inst := cmd.Coin + "-PERP"
	if cmd.Spot {
		inst = cmd.Coin + "-SPOT"
	}
	if s.Known != nil && !s.Known(inst) {
		return fail(rej("SIG_BAD_SYMBOL"))
	}
	if cmd.Spot {
		side := "buy"
		if cmd.Action == "sell" || cmd.Action == "close" || cmd.Action == "short" {
			side = "sell"
		}
		pct := cmd.Pct
		if cmd.Action == "close" {
			pct = hund
		}
		id, err := s.T.Spot(h.Account, inst, side, pct)
		if err != nil {
			return fail(err)
		}
		l.Order = id
		s.record(ctx, l)
		return l, nil
	}
	budget := h.Budget
	if !budget.IsPositive() && cmd.Action != "close" {
		budget = s.T.Available(h.Account)
	}
	side, notional, closeAll := PerpPlan(cmd, budget)
	id, err := s.T.Perp(h.Account, inst, side, notional, closeAll)
	if err != nil {
		return fail(err)
	}
	l.Order = id
	s.record(ctx, l)
	return l, nil
}

// PerpPlan 一条合约信号要下的单：平掉整个仓位，或者按"资金 × 百分比"的金额市价买卖。
// 实盘信号和回测用同一个算法（回测的结果才算数）。
func PerpPlan(cmd *Command, budget decimal.Decimal) (side string, notional decimal.Decimal, closeAll bool) {
	if cmd.Action == "close" {
		return "", decimal.Zero, true
	}
	side = "buy"
	if cmd.Action == "short" {
		side = "sell"
	}
	return side, budget.Mul(cmd.Pct).Div(hund), false
}

func codeOf(err error) string {
	type coded interface{ RejectCode() string }
	var c coded
	if errors.As(err, &c) {
		return c.RejectCode()
	}
	return "INTERNAL_ERROR"
}

// Users 账户服务。
type Users interface {
	Require(http.HandlerFunc) http.HandlerFunc
	OwnsPaper(ctx context.Context, user, acct int64) bool
	CheckTradePassword(u *auth.User, pw string) error
}

// Routes 挂接口。
func (s *Service) Routes(mux *http.ServeMux, users Users) {
	wj := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	// TradingView 发过来的：不带登录，地址里的密钥就是凭证
	mux.HandleFunc("POST /api/hook/tv/{token}", func(w http.ResponseWriter, q *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, q.Body, 4<<10))
		if err != nil {
			wj(w, 413, map[string]string{"code": "TOO_LARGE"})
			return
		}
		l, err := s.Receive(q.Context(), q.PathValue("token"), body)
		if err != nil {
			wj(w, 404, map[string]string{"code": "NOT_FOUND"})
			return
		}
		wj(w, 200, map[string]string{"status": l.Status, "code": l.Code, "order": l.Order})
	})
	r := func(pattern string, fn func(w http.ResponseWriter, q *http.Request, u *auth.User)) {
		mux.HandleFunc(pattern, users.Require(func(w http.ResponseWriter, q *http.Request) { fn(w, q, auth.UserFrom(q.Context())) }))
	}
	r("GET /api/signal-hooks", func(w http.ResponseWriter, q *http.Request, u *auth.User) {
		hooks, err := s.List(q.Context(), u.ID)
		if err != nil {
			wj(w, 500, map[string]string{"code": "INTERNAL_ERROR"})
			return
		}
		logs, err := s.Logs(q.Context(), u.ID, 30)
		if err != nil {
			wj(w, 500, map[string]string{"code": "INTERNAL_ERROR"})
			return
		}
		wj(w, 200, map[string]any{"hooks": hooks, "logs": logs})
	})
	r("POST /api/signal-hooks", func(w http.ResponseWriter, q *http.Request, u *auth.User) {
		var in struct {
			Account       int64           `json:"account"`
			Name          string          `json:"name"`
			Budget        decimal.Decimal `json:"budget"`
			TradePassword string          `json:"trade_password"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, q.Body, 4<<10)).Decode(&in); err != nil || in.Budget.IsNegative() || len(in.Name) > 60 {
			wj(w, 400, map[string]string{"code": "BAD_REQUEST"})
			return
		}
		owns := users.OwnsPaper
		if s.Owns != nil {
			owns = s.Owns
		}
		if !owns(q.Context(), u.ID, in.Account) {
			wj(w, 404, map[string]string{"code": "NOT_FOUND"})
			return
		}
		if s.RealMoney != nil && s.RealMoney(in.Account) {
			if err := users.CheckTradePassword(u, in.TradePassword); err != nil {
				st, code := 403, err.Error()
				if ae, ok := err.(*auth.Err); ok {
					st = ae.HTTP
				}
				wj(w, st, map[string]string{"code": code})
				return
			}
		}
		h, tok, err := s.Create(q.Context(), u.ID, in.Account, in.Name, in.Budget)
		if err != nil {
			wj(w, 500, map[string]string{"code": "INTERNAL_ERROR"})
			return
		}
		wj(w, 200, map[string]any{"hook": h, "token": tok})
	})
	r("DELETE /api/signal-hooks/{id}", func(w http.ResponseWriter, q *http.Request, u *auth.User) {
		id, err := strconv.ParseInt(q.PathValue("id"), 10, 64)
		if err != nil {
			wj(w, 404, map[string]string{"code": "NOT_FOUND"})
			return
		}
		if err := s.Delete(q.Context(), u.ID, id); err != nil {
			wj(w, 500, map[string]string{"code": "INTERNAL_ERROR"})
			return
		}
		wj(w, 200, map[string]bool{"ok": true})
	})
	r("POST /api/signal-hooks/parse", func(w http.ResponseWriter, q *http.Request, _ *auth.User) {
		body, err := io.ReadAll(http.MaxBytesReader(w, q.Body, 4<<10))
		if err != nil {
			wj(w, 413, map[string]string{"code": "TOO_LARGE"})
			return
		}
		c, err := Parse(body)
		if err != nil {
			wj(w, 422, map[string]string{"code": codeOf(err)})
			return
		}
		wj(w, 200, map[string]any{"action": c.Action, "coin": c.Coin, "spot": c.Spot, "pct": c.Pct})
	})
}
