// Package auth 是用户和账户（验收标准模块 1）：邮箱登录、钱包登录（SIWE）、会话、二级密码、模拟账户。
// 错误只返回错误码，界面文字由前端按语言翻译（D49）。
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/shopspring/decimal"
	"log/slog"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	SessionCookie   = "qs_session"
	sessionTTL      = 30 * 24 * time.Hour
	minPasswordLen  = 10
	loginMaxFails   = 5
	loginLock       = 15 * time.Minute
	loginIPFails    = 30  // 同一来源 15 分钟里对所有账号一共错这么多次，锁这个来源（撞库）
	loginAllFails   = 100 // 同一个账号 15 分钟里从各处一共错这么多次，整个账号锁 15 分钟（分布式猜密码）
	tradePwMaxFails = 5
	tradePwLock     = 30 * time.Minute
	codeTTL         = 10 * time.Minute
	nonceTTL        = 5 * time.Minute
	InitialPaper    = "10000"
	maxNonces       = 100000
)

// 部署的人可以改的上限（D103"不够开源"那一条：环境变量 QS_LIMIT_*，见 main.go 的 applyLimits）。
var (
	MaxPaperAccounts = 10 // 每人最多几个模拟账户
	SignupPerIPHour  = 10 // 同一来源一小时最多注册几个账号（小团队在同一个出口后面时可以调大）
)

var dummyHash, _ = HashPassword("timing-equalizer")

type User struct {
	ID            int64  `json:"id"`
	Name          string `json:"name,omitempty"`  // 账户名（D113）：登录用
	Owner         bool   `json:"owner"`           // 所有者：这台第一个注册的账号，只有他能进"系统"页（D113）
	Email         string `json:"email,omitempty"` // 绑定的邮箱：找回密码、收提醒（D113 起不用来登录）
	EmailVerified bool   `json:"email_verified"`
	Wallet        string `json:"wallet,omitempty"`
	Lang          string `json:"lang"`
	HasTradePw    bool   `json:"has_trade_password"`
	HasPassword   bool   `json:"has_password"`
	TwoFA         bool   `json:"two_fa"`     // 开了二次验证（18 第 6.2 条）
	AntiPhish     string `json:"anti_phish"` // 防钓鱼码：Telegram 和 Webhook 的通知里带上，用来认出真的通知
	passwordHash  string
	tradePwHash   string
}

type PaperAccount struct {
	ID        int64     `json:"id"`
	UserID    int64     `json:"-"` // 只有 ActivePapers 填
	Name      string    `json:"name"`
	Initial   string    `json:"initial"`
	CreatedAt time.Time `json:"created_at"`
	TakerFee  string    `json:"taker_fee,omitempty"` // 列表里才填（交易引擎给的），D103
	MakerFee  string    `json:"maker_fee,omitempty"`
	CustomFee bool      `json:"custom_fee,omitempty"`
}

type Service struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger
	// Domain 钱包登录消息里必须出现的网址（不设置时用请求的 Host）
	Domain string
	// DevMode 没有邮件服务时，验证码打印到日志并在接口里注明。
	// DevAuto = true 时不看 DevMode，跟着有没有发信邮箱走（管理后台配上邮箱马上关，清掉马上开）
	DevMode bool
	DevAuto bool
	// Mail 发信（部署的人自己的邮箱，D100）。没配时为空：注册的验证码发不出去，忘记密码不能用
	Mail Mailer
	// OnNewUser 新建了用户（注册、钱包第一次登录、"系统"页添加）：纪律锁按新用户的默认（关着，D107）
	OnNewUser func(userID int64)
	// OnPaperAccount 新建或重置模拟账户时回调（交易引擎据此开户）
	OnPaperAccount func(userID, accountID int64, initial string)
	// OnPaperArchive 模拟账户被重置（归档）时回调：交易引擎撤掉它的挂单、停止撮合
	OnPaperArchive func(userID, accountID int64)
	// OnPaperReset 重置建好新账户后回调：交易引擎把旧账户自己设的手续费率带过去（D103）
	OnPaperReset func(oldID, newID int64)
	// PaperFees 列表里显示每个账户现在的手续费率（交易引擎给，D103）
	PaperFees func(accountID int64) (taker, maker string, custom bool)
	// OnUserDelete 删除账户时回调：把这些模拟账户的交易、跟随、纪律、复盘记录都删掉（D83）
	OnUserDelete func(ctx context.Context, userID int64, accountIDs []int64) error
	// OnTradePwReset 用登录密码重置了二级密码（D107）：交易所账户回到"未开启"、发提醒
	OnTradePwReset func(userID int64)
	// OnNewDevice 用一个 90 天里没用过的设备登录了（发提醒）
	OnNewDevice func(user int64, device, ip string)
	// RegClosed 管理员把注册方式设成"只能管理员添加"时返回 true（第一个人总能注册，他就是所有者）
	RegClosed func(ctx context.Context) bool
	Now       func() time.Time

	mu     sync.Mutex
	fails  map[string]*lockState // 登录失败（按邮箱）和二级密码失败（按 "tp:"+用户）
	nonces map[string]time.Time  // 钱包登录随机码 → 过期时间
	counts map[string]*winCount  // 按时间窗计次（注册限流、按来源的登录失败）
	tf     *tfaState             // 二次验证：登录中途的票、正在设置的密钥
}

type winCount struct {
	n     int
	start time.Time
}

type ctxIP struct{}

// WithIP 把请求方的地址放进 context：登录锁定和注册限流按它算。空字符串 = 不按地址算（本机直连）。
func WithIP(ctx context.Context, ip string) context.Context {
	return context.WithValue(ctx, ctxIP{}, ip)
}

func ipOf(ctx context.Context) string { s, _ := ctx.Value(ctxIP{}).(string); return s }

// tooMany 这个键在时间窗里已经到 max 次了吗（只看不记）。
func (s *Service) tooMany(key string, max int, window time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.counts[key]
	return c != nil && s.now().Sub(c.start) < window && c.n >= max
}

// count 记一次。窗口过了重新数；记的键太多时清掉过期的，内存不会被刷爆。
func (s *Service) count(key string, window time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.counts == nil {
		s.counts = map[string]*winCount{}
	}
	now := s.now()
	if len(s.counts) > 20000 {
		for k, c := range s.counts {
			if now.Sub(c.start) >= window {
				delete(s.counts, k)
			}
		}
	}
	c := s.counts[key]
	if c == nil || now.Sub(c.start) >= window {
		c = &winCount{start: now}
		s.counts[key] = c
	}
	c.n++
}

type lockState struct {
	n     int
	until time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

const schema = `
create table if not exists users (
  id             bigserial primary key,
  email          text unique,
  email_verified boolean not null default false,
  password_hash  text not null default '',
  wallet         text unique,
  trade_pw_hash  text not null default '',
  lang           text not null default 'zh',
  created_at     timestamptz not null default now()
);
create table if not exists sessions (
  token_hash bytea primary key,
  user_id    bigint not null references users(id),
  created_at timestamptz not null default now(),
  expires_at timestamptz not null,
  user_agent text not null default ''
);
create table if not exists email_codes (
  user_id    bigint primary key references users(id),
  code_hash  bytea not null,
  expires_at timestamptz not null
);
create table if not exists paper_accounts (
  id          bigserial primary key,
  user_id     bigint not null references users(id),
  name        text not null,
  initial     numeric not null,
  created_at  timestamptz not null default now(),
  archived_at timestamptz
);
create index if not exists paper_accounts_user on paper_accounts (user_id) where archived_at is null;
-- 排序位置：重置出来的新账户沿用旧账户的位置，列表顺序不跳
alter table paper_accounts add column if not exists sort_key bigint;
-- 小团队的角色和停用（D89 第 5 条）。已有的库：最早注册的人成为所有者
alter table users add column if not exists role text not null default 'member';
alter table users add column if not exists disabled_at timestamptz;
update users set disabled_at = null where disabled_at is not null; -- D104：没有停用了，以前停用的放开
-- 账户名（D113）：登录只用账户名 + 密码；邮箱改成在账户页绑定，用来找回密码、收提醒
alter table users add column if not exists name text;
create unique index if not exists users_name on users (name);
`

// New 创建服务。Pool 可以稍后再设置（数据库是后台连接的），设置好并 Migrate 之后再开放接口。
func New(pool *pgxpool.Pool, log *slog.Logger) *Service {
	return &Service{Pool: pool, Log: log, fails: map[string]*lockState{}, nonces: map[string]time.Time{}}
}

func (s *Service) Migrate(ctx context.Context) error {
	if _, err := s.Pool.Exec(ctx, schema); err != nil {
		return err
	}
	_, err := s.Pool.Exec(ctx, totpSchema+deviceSchema+recoverSchema)
	return err
}

// ---------- 错误 ----------

type Err struct {
	HTTP int
	Code string
}

func (e *Err) Error() string       { return e.Code }
func e(http int, code string) *Err { return &Err{http, code} }

var (
	ErrBadEmail        = e(400, "BAD_EMAIL")
	ErrWeakPassword    = e(400, "WEAK_PASSWORD")
	ErrEmailTaken      = e(409, "EMAIL_TAKEN")
	ErrBadName         = e(400, "BAD_NAME")
	ErrNameTaken       = e(409, "NAME_TAKEN")
	ErrWalletTaken     = e(409, "WALLET_TAKEN")
	ErrBadLogin        = e(401, "BAD_LOGIN")
	ErrLocked          = e(429, "LOCKED")
	ErrSignupLimit     = e(429, "SIGNUP_LIMIT")
	ErrNotLoggedIn     = e(401, "NOT_LOGGED_IN")
	ErrBadCode         = e(400, "BAD_CODE")
	ErrBadSignature    = e(401, "BAD_SIGNATURE")
	ErrBadNonce        = e(401, "BAD_NONCE")
	ErrBadDomain       = e(401, "BAD_DOMAIN")
	ErrNeedTradePw     = e(403, "NEED_TRADE_PASSWORD")
	ErrBadTradePw      = e(403, "BAD_TRADE_PASSWORD")
	ErrTradePwEmpty    = e(400, "TRADE_PASSWORD_EMPTY") // 空着提交：只提示要填，不算输错（不然手滑 5 次就锁 30 分钟）
	ErrTooManyAccounts = e(400, "TOO_MANY_ACCOUNTS")
	ErrBadInitial      = e(400, "BAD_INITIAL")
	ErrNotFound        = e(404, "NOT_FOUND")
	ErrLastLogin       = e(400, "LAST_LOGIN_METHOD")
	ErrBadRequest      = e(400, "BAD_REQUEST")
	ErrBusy            = e(429, "BUSY")
	ErrSameAsLogin     = e(400, "SAME_AS_LOGIN")
	ErrRegClosed       = e(403, "REGISTRATION_CLOSED")
	ErrLastPaper       = e(400, "LAST_PAPER_ACCOUNT") // 删到只剩一个模拟账户时不让删（D108）
)

// ---------- 失败锁定 ----------

func (s *Service) locked(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.fails[key]
	return st != nil && s.now().Before(st.until)
}

func (s *Service) fail(key string, max int, lock time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.fails[key]
	if st == nil || (!st.until.IsZero() && s.now().After(st.until)) {
		st = &lockState{}
		s.fails[key] = st
	}
	st.n++
	if st.n >= max {
		st.until = s.now().Add(lock)
		st.n = 0
	}
}

func (s *Service) clearFails(key string) {
	s.mu.Lock()
	delete(s.fails, key)
	s.mu.Unlock()
}

// ---------- 用户 ----------

func (s *Service) userBy(ctx context.Context, where string, arg any) (*User, error) {
	u := &User{}
	var email, wallet, name *string
	err := s.Pool.QueryRow(ctx, `select id, email, email_verified, password_hash, wallet, trade_pw_hash, lang, totp_secret <> '', anti_phish, name,
		id = (select min(id) from users) from users where `+where, arg).
		Scan(&u.ID, &email, &u.EmailVerified, &u.passwordHash, &wallet, &u.tradePwHash, &u.Lang, &u.TwoFA, &u.AntiPhish, &name, &u.Owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if email != nil {
		u.Email = *email
	}
	if wallet != nil {
		u.Wallet = *wallet
	}
	if name != nil {
		u.Name = *name
	}
	u.HasTradePw = u.tradePwHash != ""
	u.HasPassword = u.passwordHash != ""
	return u, nil
}

func normEmail(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	a, err := mail.ParseAddress(s)
	if err != nil || a.Address != s || !strings.Contains(s[strings.Index(s, "@"):], ".") {
		return "", ErrBadEmail
	}
	return s, nil
}

// normName 账户名（D113，11-24，用户："登录不用钱包和邮箱，自定义账户名和密码就行""去设置页再绑定邮箱，重置密码和发送其他东西"）：
// 2–64 个字，不能有空格，不分大小写。存在 users.name；邮箱是另一回事（账户页绑定）。
func normName(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	n := utf8.RuneCountInString(s)
	if n < 2 || n > 64 || strings.IndexFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return "", ErrBadName
	}
	return s, nil
}

// IsEmail 账户名本身是不是一个邮箱：是的话，配了发信邮箱时照常验证、能收邮件提醒；不是的话从来不发信。
func IsEmail(s string) bool {
	_, err := normEmail(s)
	return err == nil
}

func normLang(l string) string {
	if l == "en" {
		return "en"
	}
	return "zh"
}

// Register 注册：账户名 + 密码，建用户、送一个模拟账户（D113）。不要邮箱、不发验证码；邮箱以后在账户页绑定。
func (s *Service) Register(ctx context.Context, name, pw, lang string) (*User, string, error) {
	name, err := normName(name)
	if err != nil {
		return nil, "", err
	}
	if len([]rune(pw)) < minPasswordLen {
		return nil, "", ErrWeakPassword
	}
	if s.regClosed(ctx) {
		return nil, "", ErrRegClosed
	}
	ip := ipOf(ctx)
	if ip != "" && s.tooMany("reg:"+ip, SignupPerIPHour, time.Hour) {
		return nil, "", ErrSignupLimit
	}
	h, err := HashPassword(pw)
	if err != nil {
		return nil, "", err
	}
	var id int64
	err = s.Pool.QueryRow(ctx, `insert into users (name, password_hash, lang) values ($1,$2,$3) on conflict (name) do nothing returning id`, name, h, normLang(lang)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", ErrNameTaken
	}
	if err != nil {
		return nil, "", err
	}
	if ip != "" {
		s.count("reg:"+ip, time.Hour)
	}
	s.newUser(id)
	if _, err := s.createPaper(ctx, id, defaultPaperName(lang, 1)); err != nil {
		return nil, "", err
	}
	u, err := s.userBy(ctx, "id = $1", id)
	return u, "", err
}

func defaultPaperName(lang string, n int) string {
	if normLang(lang) == "en" {
		return "Paper " + strconv.Itoa(n)
	}
	return "模拟账户 " + strconv.Itoa(n)
}

// issueCode 生成 6 位邮箱验证码。开发模式下打印到日志并返回（界面会标明"开发模式"）。
// newUser 新建了用户：通知纪律锁（新用户默认关着，D107）
func (s *Service) newUser(id int64) {
	if s.OnNewUser != nil {
		s.OnNewUser(id)
	}
}

// trustEmail 没配发信邮箱的普通部署（AUTH_DEV 没写）：邮箱验证证明不了什么（验证码就显示在页面上），
// 注册、绑定邮箱时直接算已验证，不让人对着页面再抄一遍数字（11-16 走查）。配了发信邮箱、或者明写了 AUTH_DEV 时照常验证。
func (s *Service) trustEmail() bool { return s.DevAuto && !s.mailOn() }

// startVerify 注册、绑定邮箱以后：能发信就发验证码；没配发信邮箱的普通部署直接算已验证。
func (s *Service) startVerify(ctx context.Context, userID int64, email string) (string, error) {
	if s.trustEmail() {
		_, err := s.Pool.Exec(ctx, `update users set email_verified = true where id = $1`, userID)
		return "", err
	}
	return s.issueCode(ctx, userID, email)
}

func (s *Service) issueCode(ctx context.Context, userID int64, email string) (string, error) {
	code := randomDigits(6)
	_, err := s.Pool.Exec(ctx, `insert into email_codes (user_id, code_hash, expires_at) values ($1,$2,$3)
		on conflict (user_id) do update set code_hash = excluded.code_hash, expires_at = excluded.expires_at`, userID, hashToken(code), s.now().Add(codeTTL))
	if err != nil {
		return "", err
	}
	s.mailTo(userID, "verify", code) // 配了发信邮箱就真的发出去
	if s.dev() {
		s.Log.Warn("开发模式：邮箱验证码（对外开放前要配发信邮箱并关掉开发模式）", "email", email, "code", code)
		return code, nil
	}
	return "", nil
}

func (s *Service) VerifyEmail(ctx context.Context, userID int64, code string) error {
	key := "code:" + strconv.FormatInt(userID, 10)
	if s.locked(key) {
		return ErrLocked
	}
	var h []byte
	var exp time.Time
	err := s.Pool.QueryRow(ctx, `select code_hash, expires_at from email_codes where user_id = $1`, userID).Scan(&h, &exp)
	if err != nil || s.now().After(exp) || string(h) != string(hashToken(strings.TrimSpace(code))) {
		s.fail(key, 5, 15*time.Minute)
		return ErrBadCode
	}
	s.clearFails(key)
	_, err = s.Pool.Exec(ctx, `update users set email_verified = true where id = $1`, userID)
	if err == nil {
		_, err = s.Pool.Exec(ctx, `delete from email_codes where user_id = $1`, userID)
	}
	return err
}

func (s *Service) Login(ctx context.Context, email, pw string) (*User, error) {
	email, err := normName(email) // 账户名（D113）；变量名沿用 email，锁定计数的键不变
	if err != nil {
		return nil, ErrBadLogin
	}
	// 按"邮箱 + 来源"计错：别人在别处连输错密码，锁的是他那边，你照样能登录（以前锁的是整个账号）
	ip := ipOf(ctx)
	key := "login:" + email + "|" + ip
	if s.locked(key) || (ip != "" && s.tooMany("loginip:"+ip, loginIPFails, loginLock)) || s.tooMany("loginall:"+email, loginAllFails, loginLock) {
		return nil, ErrLocked
	}
	u, err := s.userBy(ctx, "name = $1", email)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if err != nil || u.passwordHash == "" {
		CheckPassword(pw, dummyHash) // 邮箱不存在时也算一次哈希，响应时间不暴露邮箱是否注册过
	}
	if err != nil || u.passwordHash == "" || !CheckPassword(pw, u.passwordHash) {
		s.fail(key, loginMaxFails, loginLock)
		if ip != "" {
			s.count("loginip:"+ip, loginLock)
		}
		s.count("loginall:"+email, loginLock)
		return nil, ErrBadLogin
	}
	s.clearFails(key)
	return u, nil
}

// ---------- 钱包登录 ----------

// Nonce 发一个一次性随机码。未用的最多保留 maxNonces 个，防止被刷爆内存。
func (s *Service) Nonce() (string, error) {
	n := randomToken(12)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if len(s.nonces) >= maxNonces/2 {
		for k, exp := range s.nonces {
			if now.After(exp) {
				delete(s.nonces, k)
			}
		}
	}
	if len(s.nonces) >= maxNonces {
		return "", ErrBusy
	}
	s.nonces[n] = now.Add(nonceTTL)
	return n, nil
}

// checkSiwe 校验网址、随机码（一次有效）、时间、签名，返回地址。
func (s *Service) checkSiwe(msg, sig, host string) (string, error) {
	m, err := ParseSiwe(msg)
	if err != nil {
		return "", ErrBadSignature
	}
	domain := s.Domain
	if domain == "" {
		domain = host
	}
	if m.Domain != domain {
		return "", ErrBadDomain
	}
	s.mu.Lock()
	exp, ok := s.nonces[m.Nonce]
	delete(s.nonces, m.Nonce)
	s.mu.Unlock()
	now := s.now()
	if !ok || now.After(exp) || (!m.ExpiresAt.IsZero() && now.After(m.ExpiresAt)) || m.IssuedAt.After(now.Add(time.Minute)) {
		return "", ErrBadNonce
	}
	addr, err := RecoverAddress(msg, sig)
	if err != nil || addr != m.Address {
		return "", ErrBadSignature
	}
	return addr, nil
}

// WalletLogin 第一次用这个钱包登录时自动建账户（附一个模拟账户）。
func (s *Service) WalletLogin(ctx context.Context, msg, sig, host, lang string) (*User, error) {
	addr, err := s.checkSiwe(msg, sig, host)
	if err != nil {
		return nil, err
	}
	u, err := s.userBy(ctx, "wallet = $1", addr)
	if err == nil {
		return u, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if s.regClosed(ctx) {
		return nil, ErrRegClosed
	}
	var id int64
	if err := s.Pool.QueryRow(ctx, `insert into users (wallet, lang) values ($1,$2) returning id`, addr, normLang(lang)).Scan(&id); err != nil {
		return nil, err
	}
	s.newUser(id)
	if _, err := s.createPaper(ctx, id, defaultPaperName(lang, 1)); err != nil {
		return nil, err
	}
	return s.userBy(ctx, "id = $1", id)
}

func (s *Service) BindWallet(ctx context.Context, u *User, msg, sig, host string) error {
	addr, err := s.checkSiwe(msg, sig, host)
	if err != nil {
		return err
	}
	tag, err := s.Pool.Exec(ctx, `update users set wallet = $1 where id = $2 and not exists (select 1 from users where wallet = $1 and id <> $2)`, addr, u.ID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrWalletTaken
	}
	return nil
}

// BindEmail 在账户页绑定邮箱（D113）：用来找回密码、收提醒。已经有登录密码的不用再填；配了发信邮箱就发验证码。
func (s *Service) BindEmail(ctx context.Context, u *User, email, pw string) (string, error) {
	email, err := normEmail(email)
	if err != nil {
		return "", err
	}
	h := u.passwordHash
	if h == "" { // 只有钱包的老账号：顺便设登录密码
		if len([]rune(pw)) < minPasswordLen {
			return "", ErrWeakPassword
		}
		h, _ = HashPassword(pw)
	}
	tag, err := s.Pool.Exec(ctx, `update users set email = $1, email_verified = false, password_hash = $2 where id = $3 and not exists (select 1 from users where email = $1 and id <> $3)`, email, h, u.ID)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 0 {
		return "", ErrEmailTaken
	}
	return s.startVerify(ctx, u.ID, email)
}

// Unbind 解绑邮箱或钱包，但至少保留一种登录方式。
func (s *Service) Unbind(ctx context.Context, u *User, kind string) error {
	switch kind {
	case "email":
		if u.Name != "" { // 有账户名（D113）：邮箱只是找回密码、收提醒用，去掉就行，登录不受影响
			_, err := s.Pool.Exec(ctx, `update users set email = null, email_verified = false where id = $1`, u.ID)
			return err
		}
		if u.Wallet == "" {
			return ErrLastLogin
		}
		_, err := s.Pool.Exec(ctx, `update users set email = null, email_verified = false, password_hash = '' where id = $1`, u.ID)
		return err
	case "wallet":
		if u.Email == "" {
			return ErrLastLogin
		}
		_, err := s.Pool.Exec(ctx, `update users set wallet = null where id = $1`, u.ID)
		return err
	}
	return ErrBadRequest
}

// ---------- 会话 ----------

func (s *Service) NewSession(ctx context.Context, userID int64, ua string) (string, error) {
	tok := randomToken(32)
	_, err := s.Pool.Exec(ctx, `insert into sessions (token_hash, user_id, expires_at, user_agent) values ($1,$2,$3,$4)`, hashToken(tok), userID, s.now().Add(sessionTTL), ua)
	return tok, err
}

func (s *Service) UserBySession(ctx context.Context, tok string) (*User, error) {
	if tok == "" {
		return nil, ErrNotLoggedIn
	}
	var uid int64
	var exp time.Time
	err := s.Pool.QueryRow(ctx, `select user_id, expires_at from sessions where token_hash = $1`, hashToken(tok)).Scan(&uid, &exp)
	if err != nil || s.now().After(exp) {
		return nil, ErrNotLoggedIn
	}
	return s.userBy(ctx, "id = $1", uid)
}

func (s *Service) Logout(ctx context.Context, tok string) error {
	_, err := s.Pool.Exec(ctx, `delete from sessions where token_hash = $1`, hashToken(tok))
	return err
}

// ---------- 二级密码 ----------

// SetTradePassword 第一次设置只需登录；修改要旧的二级密码。
func (s *Service) SetTradePassword(ctx context.Context, u *User, oldPw, newPw string) error {
	if len([]rune(newPw)) < 6 {
		return ErrWeakPassword
	}
	if u.HasTradePw {
		if err := s.CheckTradePassword(u, oldPw); err != nil {
			return err
		}
	}
	if u.passwordHash != "" && CheckPassword(newPw, u.passwordHash) {
		return ErrSameAsLogin // 二级密码和登录密码分开，否则登录密码泄露时二级密码也失效
	}
	h, err := HashPassword(newPw)
	if err != nil {
		return err
	}
	_, err = s.Pool.Exec(ctx, `update users set trade_pw_hash = $1 where id = $2`, h, u.ID)
	if err == nil { // 设了新的：之前输错的次数不再算（命令行重置后设新的，不用等锁定的 30 分钟）
		s.clearFails("tp:" + strconv.FormatInt(u.ID, 10))
	}
	return err
}

// ChangePassword 改登录密码：要旧密码；改完其他设备上的登录失效，当前这台不受影响（1.11）。
func (s *Service) ChangePassword(ctx context.Context, u *User, oldPw, newPw, keepToken string) error {
	if u.passwordHash == "" {
		return ErrBadRequest // 只绑了钱包，没有登录密码
	}
	key := "login:" + u.Email + "|" + ipOf(ctx)
	if s.locked(key) {
		return ErrLocked
	}
	if !CheckPassword(oldPw, u.passwordHash) {
		s.fail(key, loginMaxFails, loginLock)
		return ErrBadLogin
	}
	s.clearFails(key)
	if len([]rune(newPw)) < 10 {
		return ErrWeakPassword
	}
	if u.tradePwHash != "" && CheckPassword(newPw, u.tradePwHash) {
		return ErrSameAsLogin
	}
	h, err := HashPassword(newPw)
	if err != nil {
		return err
	}
	if _, err = s.Pool.Exec(ctx, `update users set password_hash = $1 where id = $2`, h, u.ID); err != nil {
		return err
	}
	_, err = s.Pool.Exec(ctx, `delete from sessions where user_id = $1 and token_hash <> $2`, u.ID, hashToken(keepToken))
	return err
}

// DeleteUser 删除账户：要二级密码。账户和模拟盘记录一起删，删了找不回来（1.14、D83）。
func (s *Service) DeleteUser(ctx context.Context, u *User, tradePw string) error {
	if err := s.CheckTradePassword(u, tradePw); err != nil {
		return err
	}
	rows, err := s.Pool.Query(ctx, `select id, archived_at is null from paper_accounts where user_id = $1`, u.ID)
	if err != nil {
		return err
	}
	var ids, active []int64
	for rows.Next() {
		var id int64
		var on bool
		if err := rows.Scan(&id, &on); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
		if on {
			active = append(active, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	// 先让引擎停下来，再删记录，不然撤单产生的事件又写回去
	if s.OnPaperArchive != nil {
		for _, id := range active {
			s.OnPaperArchive(u.ID, id)
		}
	}
	if s.OnUserDelete != nil {
		if err := s.OnUserDelete(ctx, u.ID, ids); err != nil {
			return err
		}
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, q := range []string{`delete from sessions where user_id = $1`, `delete from email_codes where user_id = $1`, `delete from paper_accounts where user_id = $1`, `delete from users where id = $1`} {
		if _, err := tx.Exec(ctx, q, u.ID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// CheckTradePassword 敏感操作前调用。连续错 5 次锁 30 分钟。
func (s *Service) CheckTradePassword(u *User, pw string) error {
	if !u.HasTradePw {
		return ErrNeedTradePw
	}
	key := "tp:" + strconv.FormatInt(u.ID, 10)
	if s.locked(key) {
		return ErrLocked
	}
	if pw == "" {
		return ErrTradePwEmpty
	}
	if !CheckPassword(pw, u.tradePwHash) {
		s.fail(key, tradePwMaxFails, tradePwLock)
		return ErrBadTradePw
	}
	s.clearFails(key)
	return nil
}

// ---------- 模拟账户 ----------

func (s *Service) createPaper(ctx context.Context, userID int64, name string) (*PaperAccount, error) {
	return s.createPaperAt(ctx, userID, name, 0, InitialPaper)
}

// createPaperAt sortKey 为 0 时排在最后（用自己的编号）。
func (s *Service) createPaperAt(ctx context.Context, userID int64, name string, sortKey int64, initial string) (*PaperAccount, error) {
	a := &PaperAccount{Name: name, Initial: initial}
	err := s.Pool.QueryRow(ctx, `insert into paper_accounts (user_id, name, initial, sort_key) values ($1,$2,$3,nullif($4,0)) returning id, created_at`, userID, name, initial, sortKey).Scan(&a.ID, &a.CreatedAt)
	if err == nil && s.OnPaperAccount != nil {
		s.OnPaperAccount(userID, a.ID, initial)
	}
	return a, err
}

func (s *Service) PaperAccounts(ctx context.Context, userID int64) ([]PaperAccount, error) {
	rows, err := s.Pool.Query(ctx, `select id, name, initial::text, created_at from paper_accounts where user_id = $1 and archived_at is null order by coalesce(sort_key, id), id`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (PaperAccount, error) {
		var a PaperAccount
		err := r.Scan(&a.ID, &a.Name, &a.Initial, &a.CreatedAt)
		return a, err
	})
}

// ActivePapers 所有未归档的模拟账户（启动时交易引擎据此补开户）。
func (s *Service) ActivePapers(ctx context.Context) ([]PaperAccount, error) {
	rows, err := s.Pool.Query(ctx, `select id, user_id, name, initial::text, created_at from paper_accounts where archived_at is null order by id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (PaperAccount, error) {
		var a PaperAccount
		err := r.Scan(&a.ID, &a.UserID, &a.Name, &a.Initial, &a.CreatedAt)
		return a, err
	})
}

// OwnsPaper 账户归属检查（验收 1.8）：不属于这个用户就当不存在。
func (s *Service) OwnsPaper(ctx context.Context, userID, accountID int64) bool {
	var ok bool
	_ = s.Pool.QueryRow(ctx, `select exists (select 1 from paper_accounts where id = $1 and user_id = $2 and archived_at is null)`, accountID, userID).Scan(&ok)
	return ok
}

func (s *Service) NewPaper(ctx context.Context, u *User, name string) (*PaperAccount, error) {
	return s.NewPaperSized(ctx, u, name, InitialPaper)
}

// ParseInitial 模拟资金不限额度（D103，改 D75 第 8 条的三档；用户原话"完全自由"）：大于 0 就行，最多 8 位小数。
// "10,000" 这种带逗号的也认。
func ParseInitial(s string) (string, error) {
	d, err := decimal.NewFromString(strings.ReplaceAll(strings.TrimSpace(s), ",", ""))
	if err != nil || d.Round(8).Sign() <= 0 {
		return "", ErrBadInitial
	}
	return d.Round(8).String(), nil
}

// AddPaperPrincipal 模拟账户加减资金后，本金跟着变（交易引擎已经记过账，这里只同步列表和复盘用的本金，D103）。
func (s *Service) AddPaperPrincipal(ctx context.Context, accountID int64, amount decimal.Decimal) error {
	_, err := s.Pool.Exec(ctx, `update paper_accounts set initial = initial + $1 where id = $2 and archived_at is null`, amount.String(), accountID)
	return err
}

// NewPaperSized 新建指定初始资金的模拟账户。
func (s *Service) NewPaperSized(ctx context.Context, u *User, name, initial string) (*PaperAccount, error) {
	initial, err := ParseInitial(initial)
	if err != nil {
		return nil, err
	}
	list, err := s.PaperAccounts(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	if len(list) >= MaxPaperAccounts {
		return nil, ErrTooManyAccounts
	}
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 24 {
		name = defaultPaperName(u.Lang, len(list)+1)
	}
	return s.createPaperAt(ctx, u.ID, name, 0, initial)
}

func (s *Service) RenamePaper(ctx context.Context, u *User, id int64, name string) error {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 24 {
		return ErrBadRequest
	}
	tag, err := s.Pool.Exec(ctx, `update paper_accounts set name = $1 where id = $2 and user_id = $3 and archived_at is null`, name, id, u.ID)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// ResetPaper 一键重置：旧账户归档保留（历史不删），新建同名账户。
// 不再要二级密码（D103，用户原话"不再限制额度和重置，完全自由"）：模拟盘的钱不是真钱，历史也不删，用不着拦。
// initial 为空 = 还是原来的本金；填了 = 换成这个金额。
func (s *Service) ResetPaper(ctx context.Context, u *User, id int64, initial string) (*PaperAccount, error) {
	if initial != "" {
		v, err := ParseInitial(initial)
		if err != nil {
			return nil, err
		}
		initial = v
	}
	want := initial
	var name string
	var key int64
	err := s.Pool.QueryRow(ctx, `update paper_accounts set archived_at = now() where id = $1 and user_id = $2 and archived_at is null returning name, coalesce(sort_key, id), initial::text`, id, u.ID).Scan(&name, &key, &initial)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if s.OnPaperArchive != nil {
		s.OnPaperArchive(u.ID, id)
	}
	if want != "" {
		initial = want
	}
	a, err := s.createPaperAt(ctx, u.ID, name, key, initial)
	if err == nil && s.OnPaperReset != nil {
		s.OnPaperReset(id, a.ID)
	}
	return a, err
}

// DeletePaper 删除模拟账户（D108，用户 11-17 同意）：和重置一样归档（上面的机器人、跟随停掉，挂单撤掉），
// 只是不再新建，列表里就没有了；至少留一个。不要二级密码：只碰模拟盘（D107 第 5 条）。
func (s *Service) DeletePaper(ctx context.Context, u *User, id int64) error {
	var n int
	if err := s.Pool.QueryRow(ctx, `select count(*) from paper_accounts where user_id = $1 and archived_at is null`, u.ID).Scan(&n); err != nil {
		return err
	}
	if !s.OwnsPaper(ctx, u.ID, id) {
		return ErrNotFound
	}
	if n <= 1 {
		return ErrLastPaper
	}
	tag, err := s.Pool.Exec(ctx, `update paper_accounts set archived_at = now() where id = $1 and user_id = $2 and archived_at is null`, id, u.ID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if s.OnPaperArchive != nil {
		s.OnPaperArchive(u.ID, id)
	}
	return nil
}

func (s *Service) SetLang(ctx context.Context, u *User, lang string) error {
	_, err := s.Pool.Exec(ctx, `update users set lang = $1 where id = $2`, normLang(lang), u.ID)
	return err
}

// ---------- HTTP ----------

type ctxKey struct{}

// UserFrom 取出中间件放进去的当前用户。
func UserFrom(ctx context.Context) *User {
	u, _ := ctx.Value(ctxKey{}).(*User)
	return u
}

// Require 要求登录。改变状态的请求还要带 X-QS-Client 头（配合 SameSite=Strict，防跨站请求伪造）。
func (s *Service) Require(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Header.Get("X-QS-Client") != "web" {
			writeErr(w, ErrBadRequest)
			return
		}
		c, _ := r.Cookie(SessionCookie)
		tok := ""
		if c != nil {
			tok = c.Value
		}
		u, err := s.UserBySession(r.Context(), tok)
		if err != nil {
			writeErr(w, ErrNotLoggedIn)
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, u)))
	}
}

func writeErr(w http.ResponseWriter, err error) {
	var ae *Err
	if errors.As(err, &ae) {
		writeJSON(w, ae.HTTP, map[string]string{"code": ae.Code})
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

func readJSON(w http.ResponseWriter, r *http.Request, v any) error {
	return json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(v)
}

// dev 现在是不是开发模式（验证码显示在页面上）。
func (s *Service) dev() bool {
	if s.DevAuto {
		return !s.mailOn()
	}
	return s.DevMode
}
