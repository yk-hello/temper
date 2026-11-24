package auth

import (
	"context"
	"encoding/hex"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// 账户安全（18 第 6.2 条）：二次验证的接口、登录设备（会话）、新设备登录提醒、防钓鱼码。

const deviceSchema = `
alter table users add column if not exists anti_phish text not null default '';
create table if not exists login_history (user_id bigint not null, device text not null, ip text not null default '', at timestamptz not null default now());
create index if not exists login_history_user on login_history (user_id, at desc);
`

// Device 一个登录着的设备（会话）。
type Device struct {
	ID      string    `json:"id"` // 会话指纹的前 16 位（不是令牌本身）
	Device  string    `json:"device"`
	Created time.Time `json:"created"`
	Current bool      `json:"current"`
}

var (
	reEdge    = regexp.MustCompile(`Edg/(\d+)`)
	reChrome  = regexp.MustCompile(`Chrome/(\d+)`)
	reFirefox = regexp.MustCompile(`Firefox/(\d+)`)
	reSafari  = regexp.MustCompile(`Version/(\d+).*Safari`)
)

// DeviceName 把浏览器的 User-Agent 写成人能看懂的样子："Edge · Windows"。只认大类，不记版本小号（换了小版本不算新设备）。
func DeviceName(ua string) string {
	b := "Browser" // 中英文通知里都用，不写中文
	switch {
	case reEdge.MatchString(ua):
		b = "Edge"
	case strings.Contains(ua, "OPR/"):
		b = "Opera"
	case reFirefox.MatchString(ua):
		b = "Firefox"
	case reChrome.MatchString(ua):
		b = "Chrome"
	case reSafari.MatchString(ua):
		b = "Safari"
	case ua == "":
		b = "API client"
	}
	os := ""
	switch {
	case strings.Contains(ua, "Windows"):
		os = "Windows"
	case strings.Contains(ua, "iPhone"), strings.Contains(ua, "iPad"):
		os = "iOS"
	case strings.Contains(ua, "Mac OS X"):
		os = "macOS"
	case strings.Contains(ua, "Android"):
		os = "Android"
	case strings.Contains(ua, "Linux"):
		os = "Linux"
	}
	if os == "" {
		return b
	}
	return b + " · " + os
}

// noteLogin 记一次登录；这个设备 90 天里第一次出现（并且不是这个人第一次登录）时，告诉调用方要提醒。
func (s *Service) noteLogin(ctx context.Context, user int64, ua, ip string) bool {
	dev := DeviceName(ua)
	var seen, any bool
	_ = s.Pool.QueryRow(ctx, `select exists (select 1 from login_history where user_id = $1 and device = $2 and at > now() - interval '90 days'),
		exists (select 1 from login_history where user_id = $1)`, user, dev).Scan(&seen, &any)
	_, _ = s.Pool.Exec(ctx, `insert into login_history (user_id, device, ip) values ($1,$2,$3)`, user, dev, ip)
	_, _ = s.Pool.Exec(ctx, `delete from login_history where user_id = $1 and at < now() - interval '180 days'`, user)
	return any && !seen
}

// Devices 这个人登录着的设备，当前这个排最前。
func (s *Service) Devices(ctx context.Context, user int64, curTok string) ([]Device, error) {
	cur := hex.EncodeToString(hashToken(curTok))[:16]
	rows, err := s.Pool.Query(ctx, `select encode(token_hash, 'hex'), user_agent, created_at from sessions where user_id = $1 and expires_at > now() order by created_at desc`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Device{}
	for rows.Next() {
		var h, ua string
		var at time.Time
		if err := rows.Scan(&h, &ua, &at); err != nil {
			return nil, err
		}
		d := Device{ID: h[:16], Device: DeviceName(ua), Created: at, Current: h[:16] == cur}
		if d.Current {
			out = append([]Device{d}, out...)
		} else {
			out = append(out, d)
		}
	}
	return out, rows.Err()
}

// RevokeDevice 退出某个设备（id 是会话指纹前 16 位）；others = 退出除了当前以外的全部。
func (s *Service) RevokeDevice(ctx context.Context, user int64, id, curTok string, others bool) error {
	cur := hex.EncodeToString(hashToken(curTok))
	if others {
		_, err := s.Pool.Exec(ctx, `delete from sessions where user_id = $1 and encode(token_hash, 'hex') <> $2`, user, cur)
		return err
	}
	if len(id) != 16 || strings.HasPrefix(cur, id) {
		return ErrBadRequest // 退出当前设备用"退出登录"
	}
	tag, err := s.Pool.Exec(ctx, `delete from sessions where user_id = $1 and encode(token_hash, 'hex') like $2 || '%'`, user, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// SetAntiPhish 防钓鱼码：1–20 个字，出现在 Telegram 和 Webhook 的通知开头。看到没有这句话的"通知"，就不是这里发的。
func (s *Service) SetAntiPhish(ctx context.Context, u *User, code string) error {
	code = strings.TrimSpace(code)
	if len([]rune(code)) > 20 {
		return ErrBadRequest
	}
	_, err := s.Pool.Exec(ctx, `update users set anti_phish = $2 where id = $1`, u.ID, code)
	return err
}

func (s *Service) securityRoutes(h func(string, http.HandlerFunc)) {
	h("GET /api/me/security", s.Require(func(w http.ResponseWriter, r *http.Request) {
		u := UserFrom(r.Context())
		c, _ := r.Cookie(SessionCookie)
		tok := ""
		if c != nil {
			tok = c.Value
		}
		devs, err := s.Devices(r.Context(), u.ID, tok)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"two_fa": u.TwoFA, "recovery_left": s.RecoveryLeft(r.Context(), u.ID), "devices": devs, "anti_phish": u.AntiPhish})
	}))
	h("POST /api/me/2fa/setup", s.Require(func(w http.ResponseWriter, r *http.Request) {
		uri, sec, err := s.Setup2FA(r.Context(), UserFrom(r.Context()))
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]string{"uri": uri, "secret": sec})
	}))
	h("POST /api/me/2fa/enable", s.Require(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Code string }
		if readJSON(w, r, &in) != nil {
			writeErr(w, ErrBadRequest)
			return
		}
		codes, err := s.Enable2FA(r.Context(), UserFrom(r.Context()), in.Code)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"recovery": codes})
	}))
	h("POST /api/me/2fa/disable", s.Require(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Code string }
		if readJSON(w, r, &in) != nil {
			writeErr(w, ErrBadRequest)
			return
		}
		if err := s.Disable2FA(r.Context(), UserFrom(r.Context()), in.Code); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	}))
	h("POST /api/me/devices/{id}/revoke", s.Require(func(w http.ResponseWriter, r *http.Request) {
		c, _ := r.Cookie(SessionCookie)
		if err := s.RevokeDevice(r.Context(), UserFrom(r.Context()).ID, r.PathValue("id"), c.Value, false); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	}))
	h("POST /api/me/devices/revoke-others", s.Require(func(w http.ResponseWriter, r *http.Request) {
		c, _ := r.Cookie(SessionCookie)
		if err := s.RevokeDevice(r.Context(), UserFrom(r.Context()).ID, "", c.Value, true); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	}))
	h("POST /api/me/anti-phish", s.Require(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Code string }
		if readJSON(w, r, &in) != nil {
			writeErr(w, ErrBadRequest)
			return
		}
		if err := s.SetAntiPhish(r.Context(), UserFrom(r.Context()), in.Code); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	}))
}
