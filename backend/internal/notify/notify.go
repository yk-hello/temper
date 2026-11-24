// Package notify 通知（模块 7）：站内通知中心和 Telegram，用户可以按事件逐项开关（D55、D85）。
// 这里只报事实：强平预警、成交、到价。不发"趁热再来一单"这类诱导交易的内容（宪法、D16、D23）。
// 手机推送和邮件要公网 HTTPS 和发信服务，排在"实盘和上云"（D85 第 1 条）。
package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"qs/internal/auth"
)

// 事件种类。
const (
	KLiq    = "liq_warn"        // 快到强平价、已被强平
	KMargin = "margin_high"     // 保证金率过高
	KLock   = "discipline_lock" // 纪律锁触发
	KBot    = "bot_error"       // 机器人被暂停
	KPrice  = "price_alert"     // 用户自己设的到价提醒
	KFill   = "fill"            // 成交
	KStop   = "stop_hit"        // 止盈止损触发
	KFollow = "follow_stop"     // 跟随自动停止
	KWhale  = "whale"           // 链上大户动作
	KEcon   = "econ"            // 经济事件前一小时
	KHigh   = "highlight"       // 高光时刻（D88）
	KLogin  = "login_new"       // 用一个新设备登录了（18 第 6.2 条）
	KRecon  = "live_recon"      // 交易所账户对账发现对不上（18 第 6.4 条）
	KStrat  = "strategy_signal" // 自己建的策略发了新的目标仓位：只提醒，不下单（18 第 8.4 条，D100 第 1 条）
	KRecap  = "daily_recap"     // 每日复盘：昨天（UTC）这个账户的盈亏、手续费、成交（18 第 8.4 条）
)

// Kinds 设置页的顺序。
var Kinds = []string{KLiq, KMargin, KLock, KRecon, KLogin, KRecap, KStrat, KBot, KPrice, KRule, KHigh, KStop, KFill, KFollow, KWhale, KEcon}

// 默认打开的（D85 第 4 条）。到价提醒是用户自己设的，到期提醒是 `01` 定的，也开着。
var defaultOn = map[string]bool{KLiq: true, KMargin: true, KLock: true, KRecon: true, KLogin: true, KRecap: true, KStrat: true, KBot: true, KPrice: true, KRule: true}

// IsSafety 安全类通知（强平、保证金、纪律锁）：保护用户的钱，始终可以发 Telegram（D85 第 3 条；收费套餐已按 D90 取消）。
func IsSafety(kind string) bool {
	return kind == KLiq || kind == KMargin || kind == KLock || kind == KLogin || kind == KRecon
}

// Pref 一种事件的开关。
type Pref struct {
	Site bool `json:"site"`
	TG   bool `json:"tg"`
	WH   bool `json:"wh"`   // 对外 Webhook（18 第 6.3 条）
	ML   bool `json:"mail"` // 邮件（18 第 8.2 条）：默认只开和钱、安全有关的几种
}

// Mailer 发信（mailer.Config 实现了它）。
type Mailer interface {
	Enabled() bool
	Send(ctx context.Context, to, subject, body string) error
}

// mailPerHour 每人每小时最多发几封通知邮件：成交多的时候不把邮箱刷爆（站内和 Telegram 不受影响）。
const mailPerHour = 20

// Item 一条通知。参数是结构化的，文字由界面按语言拼，换语言不用重发。
type Item struct {
	ID     int64             `json:"id"`
	Kind   string            `json:"kind"`
	Params map[string]string `json:"params"`
	T      int64             `json:"t"`
	Read   bool              `json:"read"`
}

// Alert 到价提醒。
type Alert struct {
	ID      int64   `json:"id"`
	User    int64   `json:"-"`
	Inst    string  `json:"instrument"`
	Px      float64 `json:"px"`
	Up      bool    `json:"up"` // true：涨到；false：跌到
	Created int64   `json:"created"`
	Hit     int64   `json:"hit,omitempty"`
}

// MaxAlerts 每人最多几个还没触发的到价提醒。
var MaxAlerts = 50 // 部署的人可以改：QS_LIMIT_PRICE_ALERTS（D103）

// Service 服务。
type Service struct {
	// RuleMetrics 条件提醒加上、恢复时用此刻的数字判断"现在满不满足"（和 CheckRules 用同一份数据）。
	// 没有时按"已经满足"处理（保守：宁可这一次不响，不在加的时候就响）。
	RuleMetrics func(inst string) map[string]float64
	Pool        *pgxpool.Pool
	Log         *slog.Logger
	Now         func() int64
	TG          *Telegram
	WH          *Webhooks
	Mail        Mailer // 部署的人自己的发信邮箱（D100）；没配为空

	mailMu   sync.Mutex
	mailSent map[int64][]int64       // user → 最近一小时发过的通知邮件时间（毫秒）
	emailOf  func(user int64) string // 测试用：不查库直接给邮箱

	mu     sync.Mutex
	hooks  map[int64]*Hook // user → 对外 Webhook
	prefs  map[int64]map[string]Pref
	links  map[int64]int64 // user → Telegram chat
	alerts map[int64]*Alert
	rules  map[int64]*Rule  // 自定义提醒条件（rules.go）
	mem    map[int64][]Item // 没有数据库时（测试）
	nextID int64
}

// New 新服务。
func New(log *slog.Logger) *Service {
	return &Service{Log: log, prefs: map[int64]map[string]Pref{}, links: map[int64]int64{}, alerts: map[int64]*Alert{}, rules: map[int64]*Rule{}, mem: map[int64][]Item{}, hooks: map[int64]*Hook{}}
}

func (s *Service) now() int64 {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UnixMilli()
}

// Attach 建表，读回开关、绑定和到价提醒。
func (s *Service) Attach(ctx context.Context, pool *pgxpool.Pool) error {
	s.Pool = pool
	for _, q := range []string{
		`create table if not exists notifications (id bigserial primary key, user_id bigint not null, kind text not null, params jsonb not null default '{}', t bigint not null, read boolean not null default false)`,
		`create index if not exists notifications_user on notifications (user_id, id desc)`,
		`create table if not exists notify_prefs (user_id bigint not null, kind text not null, site boolean not null, tg boolean not null, primary key (user_id, kind))`,
		`create table if not exists notify_links (user_id bigint primary key, chat_id bigint not null, linked_at bigint not null)`,
		`alter table notify_prefs add column if not exists wh boolean not null default false`,
		`alter table notify_prefs add column if not exists mail boolean not null default false`,
		`create table if not exists notify_webhooks (user_id bigint primary key, url text not null, secret text not null, created bigint not null, last_at bigint not null default 0, last_status int not null default 0, last_err text not null default '')`,
		`create table if not exists price_alerts (id bigserial primary key, user_id bigint not null, instrument text not null, px double precision not null, up boolean not null, created bigint not null, hit bigint not null default 0)`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := pool.Query(ctx, `select user_id, kind, site, tg, wh, mail from notify_prefs`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var u int64
		var k string
		var p Pref
		if rows.Scan(&u, &k, &p.Site, &p.TG, &p.WH, &p.ML) == nil {
			if s.prefs[u] == nil {
				s.prefs[u] = map[string]Pref{}
			}
			s.prefs[u][k] = p
		}
	}
	rows.Close()
	if rows, err := pool.Query(ctx, `select user_id, url, secret, last_at, last_status, last_err from notify_webhooks`); err == nil {
		for rows.Next() {
			var u int64
			h := &Hook{}
			if rows.Scan(&u, &h.URL, &h.Secret, &h.LastAt, &h.LastStatus, &h.LastErr) == nil {
				s.hooks[u] = h
			}
		}
		rows.Close()
	}
	rows, err = pool.Query(ctx, `select user_id, chat_id from notify_links`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var u, c int64
		if rows.Scan(&u, &c) == nil {
			s.links[u] = c
		}
	}
	rows.Close()
	rows, err = pool.Query(ctx, `select id, user_id, instrument, px, up, created from price_alerts where hit = 0`)
	if err != nil {
		return err
	}
	for rows.Next() {
		a := &Alert{}
		if rows.Scan(&a.ID, &a.User, &a.Inst, &a.Px, &a.Up, &a.Created) == nil {
			s.alerts[a.ID] = a
		}
	}
	rows.Close()
	return s.attachRules(ctx)
}

// prefOf 这个人这种事件的开关。没设置过就用默认值。
func (s *Service) prefOf(user int64, kind string) Pref {
	if p, ok := s.prefs[user][kind]; ok {
		return p
	}
	on := defaultOn[kind]
	if kind == KHigh { // 高光默认只在站内（相册里也看得到），Telegram 自己打开（D88）
		return Pref{Site: true}
	}
	return Pref{Site: on, TG: on, WH: on, ML: IsSafety(kind) || kind == KRecap} // 每日复盘一天最多一封，默认也发邮件
}

// Wants 有人打开了这种事件吗（大户动作、经济事件是广播的，只发给打开的人）。
func (s *Service) Wants(kind string) []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []int64
	for u, m := range s.prefs {
		if p, ok := m[kind]; ok && (p.Site || p.TG || p.WH || p.ML) {
			out = append(out, u)
		}
	}
	return out
}

// Push 发一条通知：按用户的开关写站内、发 Telegram。
func (s *Service) Push(ctx context.Context, user int64, kind string, params map[string]string) {
	s.mu.Lock()
	p := s.prefOf(user, kind)
	chat := s.links[user]
	var hook *Hook
	if h := s.hooks[user]; h != nil {
		c := *h
		hook = &c
	}
	s.mu.Unlock()
	t := s.now()
	if p.Site {
		s.save(ctx, user, kind, params, t)
	}
	if p.TG && chat != 0 && s.TG != nil && s.TG.Enabled() {
		s.TG.Send(chat, s.outText(ctx, user, kind, params))
	}
	if p.WH && hook != nil && s.WH != nil {
		s.WH.enqueue(whJob{user: user, hook: *hook, kind: kind, body: payload(kind, t, params, s.outText(ctx, user, kind, params))})
	}
	if p.ML && s.mailOn() {
		s.mailOut(ctx, user, kind, params, t)
	}
}

func (s *Service) mailOn() bool { return s.Mail != nil && s.Mail.Enabled() }

// mailOf 这个人验证过的邮箱（没验证过的不发：可能不是他的）。
func (s *Service) mailOf(ctx context.Context, user int64) string {
	if s.emailOf != nil { // 测试用
		return s.emailOf(user)
	}
	if s.Pool == nil {
		return ""
	}
	var email string
	var ok bool
	if s.Pool.QueryRow(ctx, `select coalesce(email, ''), email_verified from users where id = $1`, user).Scan(&email, &ok) != nil || !ok {
		return ""
	}
	if !strings.Contains(email, "@") { // 账户名不是邮箱（D113）：不发信
		return ""
	}
	return email
}

// mailOut 发一封通知邮件：标题就是那句话（太长截断），正文多一行"在哪关"。每人每小时最多 mailPerHour 封。
func (s *Service) mailOut(ctx context.Context, user int64, kind string, params map[string]string, t int64) {
	to := s.mailOf(ctx, user)
	if to == "" {
		return
	}
	s.mailMu.Lock()
	if s.mailSent == nil {
		s.mailSent = map[int64][]int64{}
	}
	recent := s.mailSent[user][:0]
	for _, x := range s.mailSent[user] {
		if t-x < 3600_000 {
			recent = append(recent, x)
		}
	}
	if len(recent) >= mailPerHour {
		s.mailSent[user] = recent
		s.mailMu.Unlock()
		return
	}
	s.mailSent[user] = append(recent, t)
	s.mailMu.Unlock()
	text := s.outText(ctx, user, kind, params)
	subj := []rune(text)
	if len(subj) > 70 {
		subj = append(subj[:68], '…')
	}
	foot := "\n\n—\n这封邮件是你在账户页\"通知\"里打开的，在那里可以关掉。"
	if s.langOf(ctx, user) == "en" {
		foot = "\n\n—\nYou turned this email on under Notifications on the account page; turn it off there."
	}
	go func() {
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := s.Mail.Send(c, to, string(subj), text+foot); err != nil && s.Log != nil {
			s.Log.Warn("通知邮件发不出去", "user", user, "kind", kind, "err", err)
		}
	}()
}

func (s *Service) save(ctx context.Context, user int64, kind string, params map[string]string, t int64) {
	if s.Pool == nil {
		s.mu.Lock()
		s.nextID++
		s.mem[user] = append(s.mem[user], Item{ID: s.nextID, Kind: kind, Params: params, T: t})
		s.mu.Unlock()
		return
	}
	b, _ := json.Marshal(params)
	if _, err := s.Pool.Exec(ctx, `insert into notifications (user_id, kind, params, t) values ($1, $2, $3, $4)`, user, kind, b, t); err != nil && s.Log != nil {
		s.Log.Warn("通知写库失败", "err", err)
	}
	// 每人只留最近 300 条
	_, _ = s.Pool.Exec(ctx, `delete from notifications where user_id = $1 and id not in (select id from notifications where user_id = $1 order by id desc limit 300)`, user)
}

// outText 发到站外（Telegram、Webhook）的那句话：按这个人的语言写，开头带上他设的防钓鱼码（18 第 6.2 条）。
func (s *Service) outText(ctx context.Context, user int64, kind string, params map[string]string) string {
	txt := Render(s.langOf(ctx, user), kind, params)
	if s.Pool != nil {
		var code string
		if s.Pool.QueryRow(ctx, `select anti_phish from users where id = $1`, user).Scan(&code) == nil && code != "" {
			txt = "【" + code + "】" + txt
		}
	}
	return txt
}

func (s *Service) langOf(ctx context.Context, user int64) string {
	if s.Pool != nil {
		var l string
		if err := s.Pool.QueryRow(ctx, `select lang from users where id = $1`, user).Scan(&l); err == nil && l != "" {
			return l
		}
	}
	return "zh"
}

// List 最近的通知和未读数。
func (s *Service) List(ctx context.Context, user int64, limit int) ([]Item, int, error) {
	if s.Pool == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		l := s.mem[user]
		out, unread := []Item{}, 0
		for i := len(l) - 1; i >= 0; i-- {
			if !l[i].Read {
				unread++
			}
			if len(out) < limit {
				out = append(out, l[i])
			}
		}
		return out, unread, nil
	}
	rows, err := s.Pool.Query(ctx, `select id, kind, params, t, read from notifications where user_id = $1 order by id desc limit $2`, user, limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Item{}
	for rows.Next() {
		var it Item
		var b []byte
		if err := rows.Scan(&it.ID, &it.Kind, &b, &it.T, &it.Read); err != nil {
			return nil, 0, err
		}
		_ = json.Unmarshal(b, &it.Params)
		out = append(out, it)
	}
	var unread int
	_ = s.Pool.QueryRow(ctx, `select count(*) from notifications where user_id = $1 and not read`, user).Scan(&unread)
	return out, unread, nil
}

// MarkRead 全部标成已读。
func (s *Service) MarkRead(ctx context.Context, user int64) error {
	if s.Pool == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		for i := range s.mem[user] {
			s.mem[user][i].Read = true
		}
		return nil
	}
	_, err := s.Pool.Exec(ctx, `update notifications set read = true where user_id = $1 and not read`, user)
	return err
}

// SetPref 改开关。
func (s *Service) SetPref(ctx context.Context, user int64, kind string, p Pref) error {
	known := false
	for _, k := range Kinds {
		known = known || k == kind
	}
	if !known {
		return fmt.Errorf("BAD_KIND")
	}
	s.mu.Lock()
	if s.prefs[user] == nil {
		s.prefs[user] = map[string]Pref{}
	}
	s.prefs[user][kind] = p
	s.mu.Unlock()
	if s.Pool != nil {
		_, err := s.Pool.Exec(ctx, `insert into notify_prefs (user_id, kind, site, tg, wh, mail) values ($1, $2, $3, $4, $5, $6) on conflict (user_id, kind) do update set site = excluded.site, tg = excluded.tg, wh = excluded.wh, mail = excluded.mail`, user, kind, p.Site, p.TG, p.WH, p.ML)
		return err
	}
	return nil
}

// AddAlert 加一个到价提醒。方向按现价自动判断。
func (s *Service) AddAlert(ctx context.Context, user int64, inst string, px, last float64) (*Alert, error) {
	if !(px > 0) || !(last > 0) || px == last {
		return nil, fmt.Errorf("BAD_PRICE")
	}
	s.mu.Lock()
	n := 0
	for _, a := range s.alerts {
		if a.User == user {
			n++
		}
	}
	s.mu.Unlock()
	if n >= MaxAlerts {
		return nil, fmt.Errorf("TOO_MANY_ALERTS")
	}
	a := &Alert{User: user, Inst: inst, Px: px, Up: px > last, Created: s.now()}
	if s.Pool != nil {
		if err := s.Pool.QueryRow(ctx, `insert into price_alerts (user_id, instrument, px, up, created) values ($1, $2, $3, $4, $5) returning id`, user, inst, px, a.Up, a.Created).Scan(&a.ID); err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	if s.Pool == nil {
		s.nextID++
		a.ID = s.nextID
	}
	s.alerts[a.ID] = a
	s.mu.Unlock()
	return a, nil
}

// Alerts 还没触发的。
func (s *Service) Alerts(user int64) []Alert {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Alert{}
	for _, a := range s.alerts {
		if a.User == user {
			out = append(out, *a)
		}
	}
	return out
}

// DelAlert 删一个。只能删自己的。
func (s *Service) DelAlert(ctx context.Context, user, id int64) bool {
	s.mu.Lock()
	a := s.alerts[id]
	if a == nil || a.User != user {
		s.mu.Unlock()
		return false
	}
	delete(s.alerts, id)
	s.mu.Unlock()
	if s.Pool != nil {
		_, _ = s.Pool.Exec(ctx, `delete from price_alerts where id = $1 and user_id = $2`, id, user)
	}
	return true
}

// CheckAlerts 看到价提醒。每个只响一次。price 取不到价时返回 0。
func (s *Service) CheckAlerts(ctx context.Context, price func(inst string) float64) {
	type hit struct {
		a  Alert
		px float64
	}
	var hits []hit
	s.mu.Lock()
	for id, a := range s.alerts {
		p := price(a.Inst)
		if p <= 0 {
			continue
		}
		if (a.Up && p >= a.Px) || (!a.Up && p <= a.Px) {
			hits = append(hits, hit{*a, p})
			delete(s.alerts, id)
		}
	}
	s.mu.Unlock()
	for _, h := range hits {
		if s.Pool != nil {
			_, _ = s.Pool.Exec(ctx, `update price_alerts set hit = $1 where id = $2`, s.now(), h.a.ID)
		}
		dir := "down"
		if h.a.Up {
			dir = "up"
		}
		s.Push(ctx, h.a.User, KPrice, map[string]string{"inst": h.a.Inst, "px": trimNum(h.a.Px), "dir": dir})
	}
}

func trimNum(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// DeleteUser 删除账户时一起删。
func (s *Service) DeleteUser(ctx context.Context, user int64) {
	s.mu.Lock()
	delete(s.prefs, user)
	delete(s.links, user)
	delete(s.mem, user)
	for id, a := range s.alerts {
		if a.User == user {
			delete(s.alerts, id)
		}
	}
	for id, r := range s.rules {
		if r.User == user {
			delete(s.rules, id)
		}
	}
	s.mu.Unlock()
	if s.Pool != nil {
		for _, q := range []string{`delete from notifications where user_id = $1`, `delete from notify_prefs where user_id = $1`, `delete from notify_links where user_id = $1`, `delete from price_alerts where user_id = $1`, `delete from alert_rules where user_id = $1`} {
			_, _ = s.Pool.Exec(ctx, q, user)
		}
	}
}

// bind Telegram 绑定成功。
func (s *Service) bind(ctx context.Context, user, chat int64) {
	s.mu.Lock()
	s.links[user] = chat
	s.mu.Unlock()
	if s.Pool != nil {
		_, _ = s.Pool.Exec(ctx, `insert into notify_links (user_id, chat_id, linked_at) values ($1, $2, $3) on conflict (user_id) do update set chat_id = excluded.chat_id, linked_at = excluded.linked_at`, user, chat, s.now())
	}
}

func (s *Service) unbindUser(ctx context.Context, user int64) {
	s.mu.Lock()
	delete(s.links, user)
	s.mu.Unlock()
	if s.Pool != nil {
		_, _ = s.Pool.Exec(ctx, `delete from notify_links where user_id = $1`, user)
	}
}

// unbindChat 用户在 Telegram 里发 /stop。同一个 Telegram 可能绑了几个账号，都解开。
func (s *Service) unbindChat(ctx context.Context, chat int64) {
	s.mu.Lock()
	var users []int64
	for u, c := range s.links {
		if c == chat {
			users = append(users, u)
		}
	}
	s.mu.Unlock()
	for _, u := range users {
		s.unbindUser(ctx, u)
	}
}

// ---------- 接口 ----------

// Routes 挂接口。last 取现价。
func (s *Service) Routes(mux *http.ServeMux, users *auth.Service, last func(inst string) float64) {
	wj := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	r := func(pattern string, fn func(w http.ResponseWriter, q *http.Request, u *auth.User)) {
		mux.HandleFunc(pattern, users.Require(func(w http.ResponseWriter, q *http.Request) { fn(w, q, auth.UserFrom(q.Context())) }))
	}
	s.ruleRoutes(func(pattern string, fn func(w http.ResponseWriter, q *http.Request, uid int64)) {
		r(pattern, func(w http.ResponseWriter, q *http.Request, u *auth.User) { fn(w, q, u.ID) })
	}, wj)
	tgState := func(user int64) map[string]any {
		s.mu.Lock()
		linked := s.links[user] != 0
		s.mu.Unlock()
		bot := ""
		on := s.TG != nil && s.TG.Enabled()
		if on {
			bot = s.TG.Bot()
		}
		return map[string]any{"enabled": on, "linked": linked, "bot": bot}
	}
	r("GET /api/notify", func(w http.ResponseWriter, q *http.Request, u *auth.User) {
		items, unread, err := s.List(q.Context(), u.ID, 40)
		if err != nil {
			wj(w, 500, map[string]string{"code": "INTERNAL_ERROR"})
			return
		}
		wj(w, 200, map[string]any{"items": items, "unread": unread})
	})
	r("POST /api/notify/read", func(w http.ResponseWriter, q *http.Request, u *auth.User) {
		_ = s.MarkRead(q.Context(), u.ID)
		wj(w, 200, map[string]bool{"ok": true})
	})
	r("GET /api/notify/prefs", func(w http.ResponseWriter, q *http.Request, u *auth.User) {
		s.mu.Lock()
		out := make([]map[string]any, 0, len(Kinds))
		for _, k := range Kinds {
			p := s.prefOf(u.ID, k)
			out = append(out, map[string]any{"kind": k, "site": p.Site, "tg": p.TG, "wh": p.WH, "mail": p.ML, "tg_ok": true})
		}
		var hook any
		if h := s.hooks[u.ID]; h != nil {
			hook = *h
		}
		s.mu.Unlock()
		// 邮件这一列：这个部署配了发信邮箱才有；没验证过邮箱的写明要先验证
		var mail any
		if s.mailOn() {
			mail = map[string]any{"to": s.mailOf(q.Context(), u.ID), "email": u.Email}
		}
		wj(w, 200, map[string]any{"kinds": out, "tg": tgState(u.ID), "webhook": hook, "mail": mail})
	})
	r("POST /api/notify/prefs", func(w http.ResponseWriter, q *http.Request, u *auth.User) {
		var in struct {
			Kind string `json:"kind"`
			Pref
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, q.Body, 4<<10)).Decode(&in); err != nil {
			wj(w, 400, map[string]string{"code": "BAD_REQUEST"})
			return
		}
		if err := s.SetPref(q.Context(), u.ID, in.Kind, in.Pref); err != nil {
			wj(w, 400, map[string]string{"code": "BAD_REQUEST"})
			return
		}
		wj(w, 200, map[string]bool{"ok": true})
	})
	r("GET /api/notify/alerts", func(w http.ResponseWriter, _ *http.Request, u *auth.User) {
		wj(w, 200, map[string]any{"alerts": s.Alerts(u.ID)})
	})
	r("POST /api/notify/alerts", func(w http.ResponseWriter, q *http.Request, u *auth.User) {
		var in struct {
			Inst string  `json:"instrument"`
			Px   float64 `json:"px"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, q.Body, 4<<10)).Decode(&in); err != nil {
			wj(w, 400, map[string]string{"code": "BAD_REQUEST"})
			return
		}
		cur := last(in.Inst)
		if cur <= 0 {
			wj(w, 400, map[string]string{"code": "UNKNOWN_INSTRUMENT"})
			return
		}
		a, err := s.AddAlert(q.Context(), u.ID, in.Inst, in.Px, cur)
		if err != nil {
			code := "INTERNAL_ERROR"
			if m := err.Error(); m == "BAD_PRICE" || m == "TOO_MANY_ALERTS" {
				code = m
			}
			wj(w, 400, map[string]string{"code": code})
			return
		}
		wj(w, 200, a)
	})
	r("DELETE /api/notify/alerts/{id}", func(w http.ResponseWriter, q *http.Request, u *auth.User) {
		id, _ := strconv.ParseInt(q.PathValue("id"), 10, 64)
		if !s.DelAlert(q.Context(), u.ID, id) {
			wj(w, 404, map[string]string{"code": "NOT_FOUND"})
			return
		}
		wj(w, 200, map[string]bool{"ok": true})
	})
	r("POST /api/notify/telegram/link", func(w http.ResponseWriter, _ *http.Request, u *auth.User) {
		if s.TG == nil || !s.TG.Enabled() || s.TG.Bot() == "" {
			wj(w, 400, map[string]string{"code": "TELEGRAM_OFF"})
			return
		}
		code := s.TG.NewCode(u.ID)
		wj(w, 200, map[string]string{"url": "https://t.me/" + s.TG.Bot() + "?start=" + code})
	})
	r("POST /api/notify/telegram/unlink", func(w http.ResponseWriter, q *http.Request, u *auth.User) {
		s.unbindUser(q.Context(), u.ID)
		wj(w, 200, map[string]bool{"ok": true})
	})
	// 对外 Webhook（18 第 6.3 条）
	r("POST /api/notify/webhook", func(w http.ResponseWriter, q *http.Request, u *auth.User) {
		var in struct {
			URL    string `json:"url"`
			Rotate bool   `json:"rotate"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, q.Body, 4<<10)).Decode(&in); err != nil {
			wj(w, 400, map[string]string{"code": "BAD_REQUEST"})
			return
		}
		secret, err := s.SetWebhook(q.Context(), u.ID, in.URL, in.Rotate)
		if err != nil {
			code := err.Error()
			if code != "BAD_URL" && code != "WEBHOOK_PRIVATE" && code != "WEBHOOK_OFF" {
				code = "INTERNAL_ERROR"
			}
			wj(w, 400, map[string]string{"code": code})
			return
		}
		wj(w, 200, map[string]string{"secret": secret})
	})
	r("DELETE /api/notify/webhook", func(w http.ResponseWriter, q *http.Request, u *auth.User) {
		s.DelWebhook(q.Context(), u.ID)
		wj(w, 200, map[string]bool{"ok": true})
	})
	r("POST /api/notify/webhook/test", func(w http.ResponseWriter, q *http.Request, u *auth.User) {
		st, msg, err := s.TestWebhook(q.Context(), u.ID)
		if err != nil {
			wj(w, 404, map[string]string{"code": "NOT_FOUND"})
			return
		}
		wj(w, 200, map[string]any{"status": st, "error": msg})
	})
}

// Render Telegram 的文字。中英文都按各自的习惯写，不是互相翻译。
func Render(lang, kind string, p map[string]string) string {
	coin := strings.TrimSuffix(strings.TrimSuffix(p["inst"], "-PERP"), "-SPOT")
	en := lang == "en"
	if m := defaultName.FindStringSubmatch(p["acct"]); en && m != nil { // 默认账户名是注册时的语言，英文里不能出中文
		c := map[string]string{}
		for k, v := range p {
			c[k] = v
		}
		c["acct"] = "Paper " + m[1]
		p = c
	}
	pick := func(zh, enText string) string {
		if en {
			return enText
		}
		return zh
	}
	switch kind {
	case KLogin:
		if p["event"] == "tp_reset" { // 二级密码被重置（D107）
			if en {
				return "Your security password was reset. Every device was signed out and your exchange accounts stopped opening new positions until you turn them on again. If this was not you, change your sign-in password now."
			}
			return "二级密码刚刚被重置：所有设备已退出，交易所账户暂停开新仓，要用新的二级密码重新开启。不是你本人操作的话，马上改登录密码。"
		}
		if en {
			return fmt.Sprintf("New sign-in from %s%s. If this was not you, change your password and sign out other devices on the account page.", p["device"], ipEN(p["ip"]))
		}
		return fmt.Sprintf("新设备登录：%s%s。不是你本人的话，马上改密码，在账户页退出其他设备。", p["device"], ipZH(p["ip"]))
	case KRecon:
		return reconText(en, coin, p)
	case KStrat:
		return stratText(en, p)
	case KRecap:
		return recapText(en, p)
	case "test": // 对外 Webhook 的测试消息
		if en {
			return "Temper webhook test: this address will receive the notifications you selected."
		}
		return "Temper Webhook 测试：这个地址会收到你勾选的通知。"
	case KLiq:
		if p["done"] == "1" && p["partial"] != "" {
			return pick(fmt.Sprintf("部分强平：%s 在 %s 平掉 %s，亏损 %s USDT，剩下的仓位还在", coin, num(p["px"]), p["partial"], num(p["loss"])),
				fmt.Sprintf("%s partly liquidated at %s: %s closed, loss %s USDT; the rest of the position remains", coin, num(p["px"]), p["partial"], num(p["loss"])))
		}
		if p["done"] == "1" && p["px"] == "" {
			return pick(fmt.Sprintf("%s 全仓强平，亏损 %s USDT", p["acct"], num(p["loss"])), fmt.Sprintf("%s: cross-margin liquidation, loss %s USDT", p["acct"], num(p["loss"])))
		}
		if p["done"] == "1" {
			return pick(fmt.Sprintf("强平：%s 在 %s 被强平，亏损 %s USDT", coin, num(p["px"]), num(p["loss"])), fmt.Sprintf("%s liquidated at %s, loss %s USDT", coin, num(p["px"]), num(p["loss"])))
		}
		return pick(fmt.Sprintf("强平预警：%s 离强平价 %s 还有 %s%%", coin, num(p["px"]), p["dist"]), fmt.Sprintf("Liquidation warning: %s is %s%% from its liquidation price, %s", coin, p["dist"], num(p["px"])))
	case KMargin:
		return pick(fmt.Sprintf("保证金率 %s%%：%s", p["ratio"], p["acct"]), fmt.Sprintf("Margin ratio %s%% on %s", p["ratio"], p["acct"]))
	case KLock:
		return pick("纪律锁已触发：现在只能减仓和平仓", "Discipline lock on: reduce-only until it clears")
	case KBot:
		return pick(fmt.Sprintf("机器人已暂停：%s", coin), fmt.Sprintf("Bot paused: %s", coin))
	case KRule:
		return ruleText(en, coin, p)
	case KPrice:
		if p["dir"] == "up" {
			return pick(fmt.Sprintf("%s 涨到 %s", coin, num(p["px"])), fmt.Sprintf("%s rose to %s", coin, num(p["px"])))
		}
		return pick(fmt.Sprintf("%s 跌到 %s", coin, num(p["px"])), fmt.Sprintf("%s fell to %s", coin, num(p["px"])))
	case KFill:
		return pick(fmt.Sprintf("成交：%s %s %s @ %s", coin, sideZh(p["side"]), p["qty"], num(p["px"])), fmt.Sprintf("Filled: %s %s %s @ %s", sideEn(p["side"]), p["qty"], coin, num(p["px"])))
	case KStop:
		if p["what"] == "tp" {
			return pick(fmt.Sprintf("止盈触发：%s @ %s", coin, num(p["px"])), fmt.Sprintf("Take profit hit: %s @ %s", coin, num(p["px"])))
		}
		return pick(fmt.Sprintf("止损触发：%s @ %s", coin, num(p["px"])), fmt.Sprintf("Stop loss hit: %s @ %s", coin, num(p["px"])))
	case KFollow:
		return pick("策略跟随已停止：亏损达到你设的上限", "Strategy follow stopped: it reached your max loss")
	case KWhale:
		return pick(fmt.Sprintf("链上大户%s %s，%s USD", whaleZh[p["what"]], p["coin"], p["value"]), fmt.Sprintf("On-chain whale %s %s, $%s", whaleEn[p["what"]], p["coin"], p["value"]))
	case KEcon:
		return pick(fmt.Sprintf("一小时后公布：%s", econZh[p["key"]]), fmt.Sprintf("Due in one hour: %s", econEn[p["key"]]))
	case KHigh:
		zh, en := "做多", "long"
		if p["side"] == "short" {
			zh, en = "做空", "short"
		}
		return pick(fmt.Sprintf("高光时刻：%s %s +%s%%（按保证金，%s 倍）", coin, zh, p["roe"], p["lev"]), fmt.Sprintf("Highlight: %s %s +%s%% on margin, %sx", coin, en, p["roe"], p["lev"]))
	}
	return kind
}

var defaultName = regexp.MustCompile(`^模拟账户 (\d+)$`)

var whaleZh = map[string]string{"open": "开仓", "add": "加仓", "reduce": "减仓", "close": "平仓", "flip": "反手"}
var whaleEn = map[string]string{"open": "opened", "add": "added to", "reduce": "reduced", "close": "closed", "flip": "flipped"}
var econZh = map[string]string{"fomc": "FOMC 利率决议", "cpi": "CPI", "nfp": "非农就业", "ppi": "PPI", "pce": "PCE", "gdp": "GDP"}
var econEn = map[string]string{"fomc": "FOMC decision", "cpi": "CPI", "nfp": "Nonfarm Payrolls", "ppi": "PPI", "pce": "PCE", "gdp": "GDP"}

// reconText 交易所账户对账发现对不上（live/recon.go 的 what）。
func reconText(en bool, coin string, p map[string]string) string {
	a := p["account"]
	trade := fmt.Sprintf("%s %s %s @ %s", coin, sideZh(p["side"]), p["qty"], num(p["px"]))
	tradeEN := fmt.Sprintf("%s %s %s @ %s", sideEn(p["side"]), p["qty"], coin, num(p["px"]))
	switch p["what"] {
	case "external":
		if en {
			return fmt.Sprintf("%s: a trade that was not placed from here: %s. If you did not place it on Binance yourself, delete this API key on Binance now.", a, tradeEN)
		}
		return fmt.Sprintf("%s：交易所上有一笔不是从这里下的成交：%s。不是你在币安上自己下的话，马上去币安删掉这把 API 密钥。", a, trade)
	case "liq":
		if en {
			return fmt.Sprintf("%s: Binance liquidated your %s position: %s", a, coin, tradeEN)
		}
		return fmt.Sprintf("%s：交易所强平了 %s 仓位：%s", a, coin, trade)
	case "adl":
		if en {
			return fmt.Sprintf("%s: Binance auto-deleveraged your %s position: %s", a, coin, tradeEN)
		}
		return fmt.Sprintf("%s：%s 仓位被交易所自动减仓：%s", a, coin, trade)
	case "money":
		var zh, eng []string
		for _, x := range []struct{ k, zh, en string }{{"transfer", "划转", "transfers"}, {"insurance", "强平清算费", "liquidation clearance"}, {"rebate", "返佣", "rebates"}} {
			if v := p[x.k]; v != "" {
				zh = append(zh, x.zh+" "+signed(v))
				eng = append(eng, x.en+" "+signed(v))
			}
		}
		if en {
			why := "Binance's income history shows nothing that explains it; Binance's numbers are used."
			if len(eng) > 0 {
				why = "Binance's income history: " + strings.Join(eng, ", ")
				if p["rest"] != "" {
					why += "; " + signed(p["rest"]) + " still unexplained"
				}
				why += "."
			}
			return fmt.Sprintf("%s: the futures wallet changed by %s USDT outside of trades, fees and funding. %s", a, signed(p["amount"]), why)
		}
		why := "交易所的资金流水里没找到原因，以交易所的数为准。"
		if len(zh) > 0 {
			why = "交易所的资金流水：" + strings.Join(zh, "，")
			if p["rest"] != "" {
				why += "；还有 " + signed(p["rest"]) + " 对不上"
			}
			why += "。"
		}
		return fmt.Sprintf("%s：合约钱包变了 %s USDT，不是交易、手续费、资金费造成的。%s", a, signed(p["amount"]), why)
	case "fail":
		if en {
			return fmt.Sprintf("%s: syncing with Binance has failed for 2 minutes (%s). Positions and balances on screen may be stale. Check whether the API key was deleted, its IP whitelist and permissions.", a, p["err"])
		}
		return fmt.Sprintf("%s：和交易所对账已经失败 2 分钟（%s），界面上的仓位和余额可能是旧的。看一下 API 密钥是不是被删了、IP 白名单和权限。", a, p["err"])
	case "ok":
		if en {
			return fmt.Sprintf("%s: syncing with Binance works again", a)
		}
		return fmt.Sprintf("%s：和交易所对账恢复了", a)
	}
	return KRecon
}

func signed(v string) string {
	if v != "" && !strings.HasPrefix(v, "-") && !strings.HasPrefix(v, "+") {
		v = "+" + v
	}
	return num(v)
}

// num 通知里的数字按界面的写法：整数部分加千位分隔，负号用"−"（11-17 走查：Telegram、Webhook、邮件里
// "离强平价 64103.6""已实现 -0.25"，和网页上的 64,103.6、−0.25 不一样）。不是数字的原样返回。
func num(v string) string {
	s := strings.TrimSpace(v)
	sign := ""
	if strings.HasPrefix(s, "-") || strings.HasPrefix(s, "+") {
		sign, s = s[:1], s[1:]
	}
	intPart, frac := s, ""
	if i := strings.IndexByte(s, '.'); i >= 0 {
		intPart, frac = s[:i], s[i:]
	}
	if intPart == "" || strings.Trim(intPart, "0123456789") != "" || strings.Trim(strings.TrimPrefix(frac, "."), "0123456789") != "" {
		return v
	}
	var out []byte
	for i := 0; i < len(intPart); i++ {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, intPart[i])
	}
	if sign == "-" {
		sign = "−"
	}
	return sign + string(out) + frac
}

func sideEn(s string) string {
	if s == "buy" {
		return "bought"
	}
	return "sold"
}

func sideZh(s string) string {
	if s == "buy" {
		return "买入"
	}
	return "卖出"
}

func ipZH(ip string) string {
	if ip == "" {
		return ""
	}
	return "，来源 " + ip
}

func ipEN(ip string) string {
	if ip == "" {
		return ""
	}
	return " (" + ip + ")"
}

// stratText 自己的策略发了新的目标仓位。targets 是 "品种:权重:信心" 用逗号隔开（最多 5 个），more 是没列出来的个数。
// 平台只转达策略自己说的话，不加任何判断。
func stratText(en bool, p map[string]string) string {
	var parts []string
	for _, t := range strings.Split(p["targets"], ",") {
		seg := strings.Split(t, ":")
		if len(seg) < 2 {
			continue
		}
		coin := strings.TrimSuffix(strings.TrimSuffix(seg[0], "-PERP"), "-SPOT")
		w, _ := strconv.ParseFloat(seg[1], 64)
		pct := strconv.FormatFloat(math.Abs(w)*100, 'f', -1, 64)
		var s string
		switch {
		case w == 0 && en:
			s = coin + " flat"
		case w == 0:
			s = coin + " 空仓"
		case w > 0 && en:
			s = coin + " long " + pct + "%"
		case w > 0:
			s = coin + " 做多 " + pct + "%"
		case en:
			s = coin + " short " + pct + "%"
		default:
			s = coin + " 做空 " + pct + "%"
		}
		if len(seg) > 2 && seg[2] != "" && seg[2] != "0" {
			if en {
				s += " (confidence " + seg[2] + ")"
			} else {
				s += "（信心 " + seg[2] + "）"
			}
		}
		parts = append(parts, s)
	}
	more := ""
	if n, _ := strconv.Atoi(p["more"]); n > 0 {
		if en {
			more = fmt.Sprintf(" and %d more", n)
		} else {
			more = fmt.Sprintf("，另有 %d 个", n)
		}
	}
	if en {
		why := p["reason_en"]
		if why == "" {
			why = p["reason"]
		}
		if why != "" {
			why = " Its reason: " + why + "."
		}
		return fmt.Sprintf("Your strategy %s set new targets: %s%s.%s Relayed as sent; nothing was ordered for you.", p["strategy"], strings.Join(parts, ", "), more, why)
	}
	why := p["reason"]
	if why != "" {
		why = "它给的理由：" + why + "。"
	}
	return fmt.Sprintf("你的策略 %s 发了新目标：%s%s。%s平台只转达，没有替你下单。", p["strategy"], strings.Join(parts, "、"), more, why)
}

// recapText 每日复盘：只摆昨天（UTC）的数，不评价。
func recapText(en bool, p map[string]string) string {
	if en {
		return fmt.Sprintf("%s, %s (UTC): closed %s trades (%s won, %s lost), realized %s, fees %s, funding %s, net %s USDT; %s fills.",
			p["acct"], p["day"], p["closed"], p["wins"], p["losses"], signed(p["realized"]), num(p["fees"]), signed(p["funding"]), signed(p["net"]), p["fills"])
	}
	return fmt.Sprintf("%s %s（按 UTC 算）的复盘：平仓 %s 笔（赚 %s 亏 %s），已实现 %s，手续费 %s，资金费 %s，净 %s USDT；成交 %s 笔。",
		p["acct"], p["day"], p["closed"], p["wins"], p["losses"], signed(p["realized"]), num(p["fees"]), signed(p["funding"]), signed(p["net"]), p["fills"])
}
