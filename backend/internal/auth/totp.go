package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

// 二次验证（18 第 6.2 条）：手机上的验证器（Google Authenticator、1Password、Authy……）每 30 秒一个 6 位数（RFC 6238）。
// 开了以后，密码对了还要输这个数才能登录；另给 8 个一次性的恢复码（手机丢了用），只显示一次、库里只存哈希。

const (
	totpStep    = 30
	totpDigits  = 6
	ticketTTL   = 5 * time.Minute
	ticketTries = 5
	recoveryN   = 8
)

var (
	ErrNeed2FA    = e(401, "NEED_2FA")
	ErrBad2FA     = e(401, "BAD_2FA")
	Err2FATicket  = e(401, "2FA_EXPIRED")
	Err2FAOn      = e(400, "2FA_ALREADY_ON")
	Err2FANoSetup = e(400, "2FA_NO_SETUP")
)

const totpSchema = `
alter table users add column if not exists totp_secret text not null default '';
alter table users add column if not exists totp_last bigint not null default 0;
create table if not exists recovery_codes (user_id bigint not null, code_hash text not null, used_at timestamptz, primary key (user_id, code_hash));
`

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// totpCode 某个时间步的 6 位数。
func totpCode(secret []byte, step int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	m := hmac.New(sha1.New, secret)
	m.Write(msg[:])
	h := m.Sum(nil)
	off := h[len(h)-1] & 0x0f
	v := (binary.BigEndian.Uint32(h[off:off+4]) & 0x7fffffff) % 1000000
	return fmt.Sprintf("%06d", v)
}

// totpCheck 认前后各一个时间步（手机时钟差几十秒也能过）；返回认的是哪一步（防同一个数用两次），0 = 不对。
func totpCheck(secretB32, code string, now time.Time) int64 {
	key, err := b32.DecodeString(strings.ToUpper(strings.ReplaceAll(secretB32, " ", "")))
	if err != nil || len(code) != totpDigits {
		return 0
	}
	cur := now.Unix() / totpStep
	for _, st := range []int64{cur, cur - 1, cur + 1} {
		if hmac.Equal([]byte(totpCode(key, st)), []byte(code)) {
			return st
		}
	}
	return 0
}

func hashCode(c string) string {
	h := sha256.Sum256([]byte("qs-recovery:" + strings.ToLower(strings.ReplaceAll(c, "-", ""))))
	return hex.EncodeToString(h[:])
}

func newRecovery() string {
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	s := strings.ToLower(b32.EncodeToString(b)) // 8 位
	return s[:4] + "-" + s[4:]
}

// tfa 登录中途的票：密码对了、还差验证码
type tfaTicket struct {
	user  int64
	exp   time.Time
	tries int
}

type tfaState struct {
	mu      sync.Mutex
	tickets map[string]*tfaTicket
	pending map[int64]string // 正在设置、还没确认的密钥
}

func (s *Service) tfa() *tfaState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tf == nil {
		s.tf = &tfaState{tickets: map[string]*tfaTicket{}, pending: map[int64]string{}}
	}
	return s.tf
}

// Has2FA 这个人开了二次验证吗。
func (s *Service) Has2FA(ctx context.Context, user int64) bool {
	var sec string
	_ = s.Pool.QueryRow(ctx, `select totp_secret from users where id = $1`, user).Scan(&sec)
	return sec != ""
}

// NewTicket 密码对了、要二次验证：发一张 5 分钟的票。
func (s *Service) NewTicket(user int64) string {
	t := s.tfa()
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	for k, v := range t.tickets {
		if now.After(v.exp) {
			delete(t.tickets, k)
		}
	}
	tok := randomToken(24)
	t.tickets[tok] = &tfaTicket{user: user, exp: now.Add(ticketTTL)}
	return tok
}

// verifyCode 认 6 位数或恢复码（恢复码用过就作废）。
func (s *Service) verifyCode(ctx context.Context, user int64, code string) error {
	code = strings.TrimSpace(code)
	var sec string
	var last int64
	if err := s.Pool.QueryRow(ctx, `select totp_secret, totp_last from users where id = $1`, user).Scan(&sec, &last); err != nil {
		return err
	}
	if sec == "" {
		return nil
	}
	if len(code) == totpDigits {
		st := totpCheck(sec, code, s.now())
		if st == 0 || st <= last { // 同一个数不能用两次
			return ErrBad2FA
		}
		_, err := s.Pool.Exec(ctx, `update users set totp_last = $2 where id = $1`, user, st)
		return err
	}
	tag, err := s.Pool.Exec(ctx, `update recovery_codes set used_at = now() where user_id = $1 and code_hash = $2 and used_at is null`, user, hashCode(code))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrBad2FA
	}
	return nil
}

// FinishLogin 用票和验证码换登录。错 5 次这张票作废。
func (s *Service) FinishLogin(ctx context.Context, ticket, code string) (*User, error) {
	t := s.tfa()
	t.mu.Lock()
	tk := t.tickets[ticket]
	if tk == nil || time.Now().After(tk.exp) {
		t.mu.Unlock()
		return nil, Err2FATicket
	}
	tk.tries++
	if tk.tries > ticketTries {
		delete(t.tickets, ticket)
		t.mu.Unlock()
		return nil, Err2FATicket
	}
	user := tk.user
	t.mu.Unlock()
	if err := s.verifyCode(ctx, user, code); err != nil {
		return nil, err
	}
	t.mu.Lock()
	delete(t.tickets, ticket)
	t.mu.Unlock()
	return s.userBy(ctx, "id = $1", user)
}

// Setup2FA 开始设置：生成密钥，返回给验证器扫的地址（otpauth://）和密钥原文（手输用）。
func (s *Service) Setup2FA(ctx context.Context, u *User) (uri, secret string, err error) {
	if s.Has2FA(ctx, u.ID) {
		return "", "", Err2FAOn
	}
	b := make([]byte, 20)
	_, _ = rand.Read(b)
	secret = b32.EncodeToString(b)
	t := s.tfa()
	t.mu.Lock()
	t.pending[u.ID] = secret
	t.mu.Unlock()
	label := u.Email
	if label == "" {
		label = u.Wallet
	}
	issuer := "Temper"
	uri = "otpauth://totp/" + url.PathEscape(issuer+":"+label) + "?secret=" + secret + "&issuer=" + url.QueryEscape(issuer) + "&digits=6&period=30"
	return uri, secret, nil
}

// Enable2FA 用验证器上的数确认，开上，返回 8 个恢复码（只这一次）。
func (s *Service) Enable2FA(ctx context.Context, u *User, code string) ([]string, error) {
	t := s.tfa()
	t.mu.Lock()
	sec := t.pending[u.ID]
	t.mu.Unlock()
	if sec == "" {
		return nil, Err2FANoSetup
	}
	st := totpCheck(sec, strings.TrimSpace(code), s.now())
	if st == 0 {
		return nil, ErrBad2FA
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `update users set totp_secret = $2, totp_last = $3 where id = $1`, u.ID, sec, st); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `delete from recovery_codes where user_id = $1`, u.ID); err != nil {
		return nil, err
	}
	codes := make([]string, 0, recoveryN)
	for i := 0; i < recoveryN; i++ {
		c := newRecovery()
		codes = append(codes, c)
		if _, err := tx.Exec(ctx, `insert into recovery_codes (user_id, code_hash) values ($1,$2)`, u.ID, hashCode(c)); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	t.mu.Lock()
	delete(t.pending, u.ID)
	t.mu.Unlock()
	return codes, nil
}

// Disable2FA 关掉要一个有效的数（或恢复码）。
func (s *Service) Disable2FA(ctx context.Context, u *User, code string) error {
	if err := s.verifyCode(ctx, u.ID, code); err != nil {
		return err
	}
	return s.Reset2FA(ctx, u.ID)
}

// Reset2FA 管理员给丢了手机的人关掉（管理后台，要管理员的二级密码，记操作记录）。
func (s *Service) Reset2FA(ctx context.Context, user int64) error {
	if _, err := s.Pool.Exec(ctx, `update users set totp_secret = '', totp_last = 0 where id = $1`, user); err != nil {
		return err
	}
	_, err := s.Pool.Exec(ctx, `delete from recovery_codes where user_id = $1`, user)
	return err
}

// RecoveryLeft 还剩几个没用的恢复码。
func (s *Service) RecoveryLeft(ctx context.Context, user int64) int {
	var n int
	_ = s.Pool.QueryRow(ctx, `select count(*) from recovery_codes where user_id = $1 and used_at is null`, user).Scan(&n)
	return n
}
