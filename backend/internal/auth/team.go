package auth

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// 这台装的账号（D104 独立使用：没有角色，谁也管不了别人的账号）：账号列表、在"系统"页里添加账号、注册开关。

// RegistrationOpen 登录页用：现在能不能自己注册。
func (s *Service) RegistrationOpen(ctx context.Context) bool { return !s.regClosed(ctx) }

func (s *Service) regClosed(ctx context.Context) bool {
	if s.RegClosed == nil || !s.RegClosed(ctx) {
		return false
	}
	var some bool
	_ = s.Pool.QueryRow(ctx, `select exists (select 1 from users)`).Scan(&some)
	return some // 一个人都没有时总能注册：部署的人自己
}

// Member "系统"页账号列表的一行。
type Member struct {
	ID        int64      `json:"id"`
	Name      string     `json:"name,omitempty"`
	Email     string     `json:"email,omitempty"`
	Wallet    string     `json:"wallet,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	LastSeen  *time.Time `json:"last_seen,omitempty"` // 最近一次登录（会话创建时间）；没登录过时不给（11-16 走查：零值被显示成"01-01 08:05"）
	Accounts  int        `json:"accounts"`            // 模拟账户数
	TwoFA     bool       `json:"two_fa"`              // 开了二次验证
	TradePw   bool       `json:"trade_pw"`            // 设了二级密码
}

// Members 这台上所有的账号，先注册的在前。
func (s *Service) Members(ctx context.Context) ([]Member, error) {
	rows, err := s.Pool.Query(ctx, `
select u.id, coalesce(u.name, ''), coalesce(u.email, ''), coalesce(u.wallet, ''), u.created_at,
  coalesce((select max(created_at) from sessions where user_id = u.id), 'epoch'),
  (select count(*) from paper_accounts where user_id = u.id and archived_at is null),
  u.totp_secret <> '', u.trade_pw_hash <> ''
from users u
order by u.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Member{}
	for rows.Next() {
		var m Member
		var seen time.Time
		if err := rows.Scan(&m.ID, &m.Name, &m.Email, &m.Wallet, &m.CreatedAt, &seen, &m.Accounts, &m.TwoFA, &m.TradePw); err != nil {
			return nil, err
		}
		if seen.Unix() > 0 {
			m.LastSeen = &seen
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// UserByID 按编号找账号。
func (s *Service) UserByID(ctx context.Context, id int64) (*User, error) {
	return s.userBy(ctx, "id = $1", id)
}

// AddMember 在"系统"页里添加账号：账户名 + 初始密码（比如注册关着时给自己再开一个测试账号）。不发验证码，算已验证。
func (s *Service) AddMember(ctx context.Context, email, pw, lang string) (*User, error) {
	email, err := normName(email) // 账户名（D113）
	if err != nil {
		return nil, err
	}
	if len([]rune(pw)) < minPasswordLen {
		return nil, ErrWeakPassword
	}
	h, err := HashPassword(pw)
	if err != nil {
		return nil, err
	}
	var id int64
	err = s.Pool.QueryRow(ctx, `insert into users (name, password_hash, lang) values ($1,$2,$3) on conflict (name) do nothing returning id`, email, h, normLang(lang)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNameTaken
	}
	if err != nil {
		return nil, err
	}
	s.newUser(id)
	if _, err := s.createPaper(ctx, id, defaultPaperName(lang, 1)); err != nil {
		return nil, err
	}
	return s.userBy(ctx, "id = $1", id)
}
