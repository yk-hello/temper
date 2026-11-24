package auth

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// clientIP 请求方的地址。nginx 转发时带 X-Real-IP，只有从本机或内网（nginx 所在的容器网络）来的才信这个头；
// 本机直接连进来、没带这个头的（开发和测试脚本）返回空，不按地址限流。
// 上云时如果前面还有负载均衡，要让 nginx 从负载均衡那里取真实地址（real_ip 模块），不然所有人都算成一个来源。
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip != nil && (ip.IsLoopback() || ip.IsPrivate()) {
		if x := strings.TrimSpace(r.Header.Get("X-Real-IP")); x != "" && net.ParseIP(x) != nil {
			return x
		}
		if ip.IsLoopback() {
			return ""
		}
	}
	return host
}

// ClientIP 别的模块（按来源限流）用同一个规则取来源地址。
func ClientIP(r *http.Request) string { return clientIP(r) }

// Gate 数据库连上并建好表之前，账户接口返回 503。
type Gate struct{ ready atomic.Bool }

func (g *Gate) Open()        { g.ready.Store(true) }
func (g *Gate) IsOpen() bool { return g.ready.Load() }

// Routes 把账户相关接口挂到 mux 上（Go 1.22 起的"方法 + 路径"写法）。
func (s *Service) Routes(mux *http.ServeMux, gate *Gate) {
	h := func(pattern string, fn http.HandlerFunc) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if gate != nil && !gate.IsOpen() {
				w.Header().Set("Retry-After", "5")
				writeJSON(w, 503, map[string]string{"code": "STARTING"})
				return
			}
			fn(w, r)
		})
	}
	// 未登录也能用的接口：同样要求自定义头，防止别的网站代替用户提交登录表单
	open := func(fn http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet && r.Header.Get("X-QS-Client") != "web" {
				writeErr(w, ErrBadRequest)
				return
			}
			fn(w, r)
		}
	}
	// 钱包登录、绑定钱包（siwe）D113 起关掉：用户"登录不用钱包和邮箱"，少一个没人用的入口
	h("POST /api/auth/register", open(s.hRegister))
	h("POST /api/auth/login", open(s.hLogin))
	h("POST /api/auth/logout", open(s.hLogout))
	h("POST /api/auth/2fa", open(s.h2FA))
	h("POST /api/auth/verify-email", s.Require(s.hVerifyEmail))
	h("POST /api/auth/resend-code", s.Require(s.hResend))
	h("GET /api/me", s.Require(s.hMe))
	h("POST /api/me/lang", s.Require(s.hLang))
	h("POST /api/me/trade-password", s.Require(s.hTradePw))
	h("POST /api/me/password", s.Require(s.hChangePw))
	h("POST /api/me/delete", s.Require(s.hDeleteMe))
	h("POST /api/me/bind-email", s.Require(s.hBindEmail))
	h("POST /api/me/unbind", s.Require(s.hUnbind))
	s.securityRoutes(h)
	s.recoverRoutes(h)
	h("GET /api/paper-accounts", s.Require(s.hPaperList))
	h("POST /api/paper-accounts", s.Require(s.hPaperNew))
	h("PATCH /api/paper-accounts/{id}", s.Require(s.hPaperRename))
	h("POST /api/paper-accounts/{id}/reset", s.Require(s.hPaperReset))
	h("DELETE /api/paper-accounts/{id}", s.Require(s.hPaperDelete))
}

// ---------- 工具 ----------

func secure(r *http.Request) bool {
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		return true
	}
	if o, err := url.Parse(r.Header.Get("Origin")); err == nil && o.Scheme == "https" {
		return true
	}
	return false
}

func (s *Service) setSession(ctx context.Context, w http.ResponseWriter, r *http.Request, u *User) error {
	tok, err := s.NewSession(ctx, u.ID, r.UserAgent())
	if err != nil {
		return err
	}
	// 新设备登录提醒（18 第 6.2 条）：这个设备 90 天里第一次来
	if ip := clientIP(r); s.noteLogin(ctx, u.ID, r.UserAgent(), ip) && s.OnNewDevice != nil {
		s.OnNewDevice(u.ID, DeviceName(r.UserAgent()), ip)
	}
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: tok, Path: "/", HttpOnly: true, Secure: secure(r),
		SameSite: http.SameSiteStrictMode, MaxAge: int(sessionTTL / time.Second)})
	return nil
}

// siteHost 钱包登录消息里的网址要和用户正在访问的网址一致（防止别的网站骗签名拿去登录）。
// 浏览器的 POST 请求一定带 Origin，网页改不了它；经过 nginx 时 Host 会丢端口，所以优先用 Origin。
func siteHost(r *http.Request) string {
	if o, err := url.Parse(r.Header.Get("Origin")); err == nil && o.Host != "" {
		return o.Host
	}
	return r.Host
}

type meResp struct {
	User    *User  `json:"user"`
	DevCode string `json:"dev_code,omitempty"` // 开发模式下直接给出验证码（界面会标明）
}

// ---------- 处理函数 ----------

// firstOf 第一个不空的。
func firstOf(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func (s *Service) hRegister(w http.ResponseWriter, r *http.Request) {
	var in struct{ Name, Email, Password, Lang string } // Email：老写法，当账户名用（D113）
	if readJSON(w, r, &in) != nil {
		writeErr(w, ErrBadRequest)
		return
	}
	u, code, err := s.Register(WithIP(r.Context(), clientIP(r)), firstOf(in.Name, in.Email), in.Password, in.Lang)
	if err == nil {
		err = s.setSession(r.Context(), w, r, u)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, meResp{User: u, DevCode: code})
}

func (s *Service) hLogin(w http.ResponseWriter, r *http.Request) {
	var in struct{ Name, Email, Password string } // Email：老写法，当账户名用（D113）
	if readJSON(w, r, &in) != nil {
		writeErr(w, ErrBadRequest)
		return
	}
	u, err := s.Login(WithIP(r.Context(), clientIP(r)), firstOf(in.Name, in.Email), in.Password)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.loginDone(w, r, u)
}

// loginDone 密码（或钱包签名）对了：开了二次验证的先发一张票，再输验证码；没开的直接登录。
func (s *Service) loginDone(w http.ResponseWriter, r *http.Request, u *User) {
	if u.TwoFA {
		writeJSON(w, 200, map[string]any{"need_2fa": true, "ticket": s.NewTicket(u.ID)})
		return
	}
	if err := s.setSession(r.Context(), w, r, u); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, meResp{User: u})
}

// h2FA 登录第二步：票 + 验证器上的 6 位数（或恢复码）
func (s *Service) h2FA(w http.ResponseWriter, r *http.Request) {
	var in struct{ Ticket, Code string }
	if readJSON(w, r, &in) != nil {
		writeErr(w, ErrBadRequest)
		return
	}
	u, err := s.FinishLogin(r.Context(), in.Ticket, in.Code)
	if err == nil {
		err = s.setSession(r.Context(), w, r, u)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, meResp{User: u})
}

func (s *Service) hLogout(w http.ResponseWriter, r *http.Request) {
	if c, _ := r.Cookie(SessionCookie); c != nil {
		_ = s.Logout(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: "", Path: "/", HttpOnly: true, Secure: secure(r), SameSite: http.SameSiteStrictMode, MaxAge: -1})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Service) hNonce(w http.ResponseWriter, r *http.Request) {
	domain := s.Domain
	if domain == "" {
		domain = siteHost(r)
	}
	n, err := s.Nonce()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"nonce": n, "statement": SiweStatement, "domain": domain, "ttl_seconds": int(nonceTTL / time.Second)})
}

func (s *Service) hSiwe(w http.ResponseWriter, r *http.Request) {
	var in struct{ Message, Signature, Lang string }
	if readJSON(w, r, &in) != nil {
		writeErr(w, ErrBadRequest)
		return
	}
	u, err := s.WalletLogin(r.Context(), in.Message, in.Signature, siteHost(r), in.Lang)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.loginDone(w, r, u)
}

func (s *Service) hVerifyEmail(w http.ResponseWriter, r *http.Request) {
	var in struct{ Code string }
	if readJSON(w, r, &in) != nil {
		writeErr(w, ErrBadRequest)
		return
	}
	u := UserFrom(r.Context())
	if err := s.VerifyEmail(r.Context(), u.ID, in.Code); err != nil {
		writeErr(w, err)
		return
	}
	s.hMe(w, r)
}

func (s *Service) hResend(w http.ResponseWriter, r *http.Request) {
	u := UserFrom(r.Context())
	if u.Email == "" || u.EmailVerified {
		writeErr(w, ErrBadRequest)
		return
	}
	code, err := s.issueCode(r.Context(), u.ID, u.Email)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, meResp{User: u, DevCode: code})
}

func (s *Service) hMe(w http.ResponseWriter, r *http.Request) {
	u, err := s.userBy(r.Context(), "id = $1", UserFrom(r.Context()).ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, meResp{User: u})
}

func (s *Service) hLang(w http.ResponseWriter, r *http.Request) {
	var in struct{ Lang string }
	if readJSON(w, r, &in) != nil {
		writeErr(w, ErrBadRequest)
		return
	}
	if err := s.SetLang(r.Context(), UserFrom(r.Context()), in.Lang); err != nil {
		writeErr(w, err)
		return
	}
	s.hMe(w, r)
}

func (s *Service) hChangePw(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Old string `json:"old"`
		New string `json:"new"`
	}
	if readJSON(w, r, &in) != nil {
		writeErr(w, ErrBadRequest)
		return
	}
	tok := ""
	if c, _ := r.Cookie(SessionCookie); c != nil {
		tok = c.Value
	}
	if err := s.ChangePassword(WithIP(r.Context(), clientIP(r)), UserFrom(r.Context()), in.Old, in.New, tok); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Service) hDeleteMe(w http.ResponseWriter, r *http.Request) {
	var in struct {
		TradePassword string `json:"trade_password"`
	}
	if readJSON(w, r, &in) != nil {
		writeErr(w, ErrBadRequest)
		return
	}
	if err := s.DeleteUser(r.Context(), UserFrom(r.Context()), in.TradePassword); err != nil {
		writeErr(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: "", Path: "/", HttpOnly: true, Secure: secure(r), MaxAge: -1})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Service) hTradePw(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Old string `json:"old"`
		New string `json:"new"`
	}
	if readJSON(w, r, &in) != nil {
		writeErr(w, ErrBadRequest)
		return
	}
	if err := s.SetTradePassword(r.Context(), UserFrom(r.Context()), in.Old, in.New); err != nil {
		writeErr(w, err)
		return
	}
	s.hMe(w, r)
}

func (s *Service) hBindEmail(w http.ResponseWriter, r *http.Request) {
	var in struct{ Email, Password string }
	if readJSON(w, r, &in) != nil {
		writeErr(w, ErrBadRequest)
		return
	}
	u := UserFrom(r.Context())
	if u.Email != "" {
		writeErr(w, ErrBadRequest)
		return
	}
	code, err := s.BindEmail(r.Context(), u, in.Email, in.Password)
	if err != nil {
		writeErr(w, err)
		return
	}
	u, _ = s.userBy(r.Context(), "id = $1", u.ID)
	writeJSON(w, 200, meResp{User: u, DevCode: code})
}

func (s *Service) hBindWallet(w http.ResponseWriter, r *http.Request) {
	var in struct{ Message, Signature string }
	if readJSON(w, r, &in) != nil {
		writeErr(w, ErrBadRequest)
		return
	}
	u := UserFrom(r.Context())
	if u.Wallet != "" {
		writeErr(w, ErrBadRequest)
		return
	}
	if err := s.BindWallet(r.Context(), u, in.Message, in.Signature, siteHost(r)); err != nil {
		writeErr(w, err)
		return
	}
	s.hMe(w, r)
}

func (s *Service) hUnbind(w http.ResponseWriter, r *http.Request) {
	var in struct{ Kind string }
	if readJSON(w, r, &in) != nil {
		writeErr(w, ErrBadRequest)
		return
	}
	if err := s.Unbind(r.Context(), UserFrom(r.Context()), in.Kind); err != nil {
		writeErr(w, err)
		return
	}
	s.hMe(w, r)
}

func (s *Service) hPaperList(w http.ResponseWriter, r *http.Request) {
	list, err := s.PaperAccounts(r.Context(), UserFrom(r.Context()).ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if list == nil {
		list = []PaperAccount{}
	}
	if s.PaperFees != nil {
		for i := range list {
			list[i].TakerFee, list[i].MakerFee, list[i].CustomFee = s.PaperFees(list[i].ID)
		}
	}
	writeJSON(w, 200, map[string]any{"accounts": list, "max": MaxPaperAccounts})
}

func (s *Service) hPaperNew(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name    string
		Initial string
	}
	if readJSON(w, r, &in) != nil {
		writeErr(w, ErrBadRequest)
		return
	}
	if in.Initial == "" {
		in.Initial = InitialPaper
	}
	a, err := s.NewPaperSized(r.Context(), UserFrom(r.Context()), in.Name, in.Initial)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, a)
}

func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, ErrNotFound
	}
	return id, nil
}

func (s *Service) hPaperRename(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	var in struct{ Name string }
	if err == nil && readJSON(w, r, &in) != nil {
		err = ErrBadRequest
	}
	if err == nil {
		err = s.RenamePaper(r.Context(), UserFrom(r.Context()), id, in.Name)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Service) hPaperDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err == nil {
		err = s.DeletePaper(r.Context(), UserFrom(r.Context()), id)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Service) hPaperReset(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	var in struct {
		Initial string `json:"initial"` // 空 = 不变（旧版前端还会带 trade_password，不用了，忽略）
	}
	if err == nil && readJSON(w, r, &in) != nil {
		err = ErrBadRequest
	}
	var a *PaperAccount
	if err == nil {
		// 先查归属：别人的账户一律 404，不暴露"存在但不是你的"，也不消耗二级密码次数
		if !s.OwnsPaper(r.Context(), UserFrom(r.Context()).ID, id) {
			err = ErrNotFound
		} else {
			a, err = s.ResetPaper(r.Context(), UserFrom(r.Context()), id, in.Initial)
		}
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, a)
}
