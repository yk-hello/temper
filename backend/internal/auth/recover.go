package auth

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// 邮件（D100 第 2、3 条）：注册的邮箱验证码真的发出去；忘记登录密码（1.12）；找回二级密码（1.13）。
// 发信用部署的人自己的邮箱（internal/mailer）。没配邮件、又不是开发模式时，忘记密码说清楚"这个部署没配发信邮箱"。

// Mailer 发信（mailer.Config 实现了它）。
type Mailer interface {
	Enabled() bool
	Send(ctx context.Context, to, subject, body string) error
}

const recoverSchema = `
create table if not exists reset_codes (
  user_id    bigint not null references users(id) on delete cascade,
  kind       text   not null,           -- login | trade
  code_hash  bytea  not null,
  expires_at timestamptz not null,
  primary key (user_id, kind)
);
`

const (
	resetTTL      = 15 * time.Minute
	resetMaxTries = 5
)

var (
	ErrMailOff   = e(400, "MAIL_OFF")
	ErrNeedEmail = e(400, "NEED_EMAIL")
	ErrTooOften  = e(429, "TOO_OFTEN")
)

func (s *Service) mailOn() bool { return s.Mail != nil && s.Mail.Enabled() }

// recoverDev 找回登录密码、二级密码时，验证码能不能直接显示在页面上：只在明写了 AUTH_DEV=1 的开发、测试环境。
// 没配发信邮箱的普通部署（自动的开发模式）不行：那样谁能打开这个网页，填上别人的邮箱就能改掉别人的密码；
// 拿到一个登录着的浏览器的人也能绕过二级密码（11-16 走查发现）。这时找回只能用部署那台电脑上的本机命令（D105）。
func (s *Service) recoverDev() bool { return !s.DevAuto && s.DevMode }

// mailTo 后台发一封给这个人（按他的语言写，开头带防钓鱼码）。发不出去只记日志：不让发信的快慢暴露邮箱有没有注册。
func (s *Service) mailTo(userID int64, kind, code string) {
	if !s.mailOn() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	u, err := s.userBy(ctx, "id = $1", userID)
	cancel()
	if err != nil || !IsEmail(u.Email) { // 账户名不是邮箱（D113）：没地方发
		return
	}
	subj, body := mailText(u.Lang, kind, code, u.AntiPhish)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := s.Mail.Send(ctx, u.Email, subj, body); err != nil && s.Log != nil {
			s.Log.Error("发信失败", "kind", kind, "user", userID, "err", err)
		}
	}()
}

// mailText 邮件的标题和正文。只写事实和该怎么做，不放链接（钓鱼邮件最爱放链接：让人认准"我们的信里没有链接"）。
func mailText(lang, kind, code, phish string) (string, string) {
	en := lang == "en"
	var subj, body string
	switch kind {
	case "verify":
		if en {
			subj, body = "Email verification code: "+code, "Your email verification code is "+code+". It expires in 10 minutes.\n\nIf you did not request this, ignore this email."
		} else {
			subj, body = "邮箱验证码："+code, "你的邮箱验证码是 "+code+"，10 分钟内有效。\n\n不是你本人操作的话，不用理会这封邮件。"
		}
	case "login":
		if en {
			subj, body = "Password reset code: "+code, "Your code to reset your sign-in password is "+code+". It expires in 15 minutes.\n\nIf you did not request this, ignore this email: your password stays the same. Consider turning on two-step verification on the account page."
		} else {
			subj, body = "重设登录密码的验证码："+code, "重设登录密码的验证码是 "+code+"，15 分钟内有效。\n\n不是你本人操作的话，不用理会，你的密码不会变。可以考虑在账户页开启二次验证。"
		}
	case "login_done":
		if en {
			subj, body = "Your sign-in password was reset", "Your sign-in password was just reset, and every device was signed out.\n\nIf this was not you, reset it again right away with \"Forgot password\" and turn on two-step verification."
		} else {
			subj, body = "登录密码已重设", "你的登录密码刚刚被重设，所有设备都已退出登录。\n\n不是你本人操作的话，马上用\"忘记密码\"再重设一次，并开启二次验证。"
		}
	}
	if phish != "" {
		subj = "【" + phish + "】" + subj
		body = "【" + phish + "】\n\n" + body
	}
	return subj, body
}

// storeReset 记一个重设用的验证码（同一种只留最新的一个）。
func (s *Service) storeReset(ctx context.Context, userID int64, kind string) (string, error) {
	code := randomDigits(6)
	_, err := s.Pool.Exec(ctx, `insert into reset_codes (user_id, kind, code_hash, expires_at) values ($1,$2,$3,$4)
		on conflict (user_id, kind) do update set code_hash = excluded.code_hash, expires_at = excluded.expires_at`, userID, kind, hashToken(code), s.now().Add(resetTTL))
	return code, err
}

// checkReset 核对验证码：错 5 次作废（要重新发）。
func (s *Service) checkReset(ctx context.Context, userID int64, kind, code string) error {
	key := "reset:" + kind + ":" + strconv.FormatInt(userID, 10)
	if s.locked(key) {
		return ErrLocked
	}
	var h []byte
	var exp time.Time
	err := s.Pool.QueryRow(ctx, `select code_hash, expires_at from reset_codes where user_id = $1 and kind = $2`, userID, kind).Scan(&h, &exp)
	if err != nil || s.now().After(exp) || string(h) != string(hashToken(strings.TrimSpace(code))) {
		s.fail(key, resetMaxTries, 15*time.Minute)
		if s.locked(key) { // 刚好错满：这个验证码作废
			_, _ = s.Pool.Exec(ctx, `delete from reset_codes where user_id = $1 and kind = $2`, userID, kind)
		}
		return ErrBadCode
	}
	s.clearFails(key)
	_, err = s.Pool.Exec(ctx, `delete from reset_codes where user_id = $1 and kind = $2`, userID, kind)
	return err
}

// ForgotPassword 忘记登录密码：输账户名，验证码发到这个账号绑定的邮箱（D113）。账户名存不存在、绑没绑邮箱，回复都一样（不暴露）。
// 开发模式下把验证码直接返回（界面标明"开发模式"），对外开放前要关掉开发模式。
func (s *Service) ForgotPassword(ctx context.Context, email string) (string, error) {
	email, err := normName(email)
	if err != nil {
		return "", err
	}
	if !s.recoverDev() && !s.mailOn() {
		return "", ErrMailOff
	}
	// 按邮箱和按来源限次：同一个邮箱 15 分钟 3 次，同一个来源 1 小时 20 次（不管邮箱存不存在都一样算）
	ip := ipOf(ctx)
	if s.tooMany("forgot:"+email, 3, 15*time.Minute) || (ip != "" && s.tooMany("forgot-ip:"+ip, 20, time.Hour)) {
		return "", ErrTooOften
	}
	s.count("forgot:"+email, 15*time.Minute)
	if ip != "" {
		s.count("forgot-ip:"+ip, time.Hour)
	}
	u, err := s.userBy(ctx, "name = $1", email)
	if err != nil || u.Email == "" || !u.EmailVerified {
		return "", nil // 没这个账号、没绑邮箱、邮箱没验证过：和成功一样回复
	}
	code, err := s.storeReset(ctx, u.ID, "login")
	if err != nil {
		return "", err
	}
	s.mailTo(u.ID, "login", code)
	if s.recoverDev() {
		s.Log.Warn("开发模式：重设登录密码的验证码", "email", email, "code", code)
		return code, nil
	}
	return "", nil
}

// ResetPassword 用邮件里的验证码重设登录密码：所有设备退出登录；邮箱算验证过了（能收到信）。二次验证不受影响。
func (s *Service) ResetPassword(ctx context.Context, email, code, pw string) error {
	email, err := normName(email) // 账户名（D113）
	if err != nil {
		return err
	}
	if len([]rune(pw)) < minPasswordLen {
		return ErrWeakPassword
	}
	u, err := s.userBy(ctx, "name = $1", email)
	if err != nil {
		s.fail("reset-unknown:"+email, resetMaxTries, 15*time.Minute)
		return ErrBadCode // 和验证码不对一样，不暴露邮箱是否注册过
	}
	if err := s.checkReset(ctx, u.ID, "login", code); err != nil {
		return err
	}
	if u.tradePwHash != "" && CheckPassword(pw, u.tradePwHash) {
		return ErrSameAsLogin
	}
	h, err := HashPassword(pw)
	if err != nil {
		return err
	}
	if _, err := s.Pool.Exec(ctx, `update users set password_hash = $1, email_verified = true where id = $2`, h, u.ID); err != nil {
		return err
	}
	if _, err := s.Pool.Exec(ctx, `delete from sessions where user_id = $1`, u.ID); err != nil {
		return err
	}
	s.mailTo(u.ID, "login_done", "")
	return nil
}

// SetPasswordLocal 本机命令（qs reset-password）重设登录密码：换掉密码、所有设备退出登录。
// 只有能进部署那台电脑的人能用，不经过网页（没配发信邮箱时忘了登录密码靠它）。
func (s *Service) SetPasswordLocal(ctx context.Context, id int64, pw string) error {
	if len([]rune(pw)) < minPasswordLen {
		return ErrWeakPassword
	}
	h, err := HashPassword(pw)
	if err != nil {
		return err
	}
	tag, err := s.Pool.Exec(ctx, `update users set password_hash = $1 where id = $2`, h, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	_, err = s.Pool.Exec(ctx, `delete from sessions where user_id = $1`, id)
	return err
}

// ResetTradePassword 忘了二级密码（D107）：输一遍登录密码（开了二次验证的再输验证器上的数或恢复码），就能设新的。
// 不用邮箱、不用验证码、不用命令。坐到你电脑前的人不知道登录密码，重置不了。
// 代价（让偷偷重置藏不住，也给你时间看一眼）：所有设备退出，包括这台；交易所账户回到"未开启"
// （开新仓要用新的二级密码重新开启，减仓、平仓、止盈止损照常）；发一条提醒。这些由 OnTradePwReset 做。
func (s *Service) ResetTradePassword(ctx context.Context, u *User, loginPw, code, newPw string) error {
	if u.passwordHash == "" {
		return ErrNeedEmail // 只绑了钱包：没有登录密码可验。先绑一个邮箱，或者在部署的电脑上用本机命令
	}
	if len([]rune(newPw)) < 6 {
		return ErrWeakPassword
	}
	if CheckPassword(newPw, u.passwordHash) {
		return ErrSameAsLogin
	}
	key := "tpreset:" + strconv.FormatInt(u.ID, 10)
	if s.locked(key) {
		return ErrLocked
	}
	if !CheckPassword(loginPw, u.passwordHash) {
		s.fail(key, loginMaxFails, loginLock)
		return ErrBadLogin
	}
	if u.TwoFA {
		if err := s.verifyCode(ctx, u.ID, code); err != nil {
			s.fail(key, loginMaxFails, loginLock)
			return err
		}
	}
	s.clearFails(key)
	h, err := HashPassword(newPw)
	if err != nil {
		return err
	}
	if _, err := s.Pool.Exec(ctx, `update users set trade_pw_hash = $1 where id = $2`, h, u.ID); err != nil {
		return err
	}
	s.clearFails("tp:" + strconv.FormatInt(u.ID, 10))
	if _, err := s.Pool.Exec(ctx, `delete from sessions where user_id = $1`, u.ID); err != nil {
		return err
	}
	if s.OnTradePwReset != nil {
		s.OnTradePwReset(u.ID)
	}
	return nil
}

// ClearTradePassword 本机命令 qs reset-trade-password：清空二级密码（下次设新的不用旧的），所有设备退出。
// 交易所账户回到"未开启"由命令那边做（和网页上重置一样，D107）。
func (s *Service) ClearTradePassword(ctx context.Context, user int64) error {
	if _, err := s.Pool.Exec(ctx, `update users set trade_pw_hash = '' where id = $1`, user); err != nil {
		return err
	}
	s.clearFails("tp:" + strconv.FormatInt(user, 10))
	_, err := s.Pool.Exec(ctx, `delete from sessions where user_id = $1`, user)
	return err
}

func (s *Service) recoverRoutes(h func(string, http.HandlerFunc)) {
	// 这个部署能不能发信（界面决定显不显示"忘记密码"、怎么写说明）；dev = 找回的验证码显示在页面上（只在明写的开发模式）
	h("GET /api/auth/mail", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]bool{"mail": s.mailOn(), "dev": s.recoverDev()})
	})
	h("POST /api/auth/forgot", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Name, Email string } // Email：老写法，当账户名用
		if readJSON(w, r, &in) != nil {
			writeErr(w, ErrBadRequest)
			return
		}
		code, err := s.ForgotPassword(WithIP(r.Context(), clientIP(r)), firstOf(in.Name, in.Email))
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "dev_code": code})
	})
	h("POST /api/auth/reset", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Name, Email, Code, Password string }
		if readJSON(w, r, &in) != nil {
			writeErr(w, ErrBadRequest)
			return
		}
		if err := s.ResetPassword(r.Context(), firstOf(in.Name, in.Email), in.Code, in.Password); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	h("POST /api/me/trade-password/reset", s.Require(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Password, Code, New string }
		if readJSON(w, r, &in) != nil {
			writeErr(w, ErrBadRequest)
			return
		}
		if err := s.ResetTradePassword(r.Context(), UserFrom(r.Context()), in.Password, in.Code, in.New); err != nil {
			writeErr(w, err)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true}) // 这台也退出
		writeJSON(w, 200, map[string]bool{"ok": true})
	}))
}
