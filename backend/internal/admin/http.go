package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"qs/internal/auth"
)

// API "系统"页的接口（以前叫管理后台）。D104：一台装的东西是一个人自己用的，登录的账号都能用，没有角色；
// 不能管别的账号（改角色、停用、重置别人的密码这些都去掉了）。停机（往上调）不要二级密码：出事时要快。其余改动都要二级密码。
type API struct {
	S     *Store
	G     *Gate
	Users *auth.Service
	Log   *slog.Logger
	// CancelOpening 第二档停机：撤掉所有账户的开仓挂单（模拟盘合约、现货买单、交易所账户），返回撤了几张
	CancelOpening func() int
	// Strategies 所有策略的状态（qsig.Service.Status 的结果，原样给界面）
	Strategies func() any
	// Resume 恢复一个被暂停的策略；没有这个策略返回 false
	Resume func(id string) bool
	// Revoke 吊销一个自助建的策略的密钥（环境变量里配的不能在这里吊销）；没有返回 false
	Revoke func(ctx context.Context, id string) (bool, error)
	// Sys 系统设置（发信邮箱、AI 点评、Telegram、数据源密钥，settings.go）
	Sys *Sys
}

// Load 启动时把停机档位交给下单检查。
func (a *API) Load(ctx context.Context) error {
	h, err := a.S.Halt(ctx)
	if err != nil {
		return err
	}
	a.G.setLevel(h.Level)
	return nil
}

var (
	errForbidden = &auth.Err{HTTP: 403, Code: "FORBIDDEN"}
	errBad       = &auth.Err{HTTP: 400, Code: "BAD_REQUEST"}
	errNoStrat   = &auth.Err{HTTP: 404, Code: "NOT_FOUND"}
)

func write(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, err error) {
	var ae *auth.Err
	if errors.As(err, &ae) {
		write(w, ae.HTTP, map[string]string{"code": ae.Code})
		return
	}
	write(w, 500, map[string]string{"code": "INTERNAL_ERROR"})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<10))
	if err != nil || json.Unmarshal(b, v) != nil {
		write(w, 400, map[string]string{"code": "BAD_REQUEST"})
		return false
	}
	return true
}

// who 操作记录里写"谁做的"：账户名优先（D113 以后账号可以不绑邮箱，只认邮箱的话这一栏是空的）。
// 存在 admin_log.actor_email 这一列里（列名是老的，没改）。
func who(u *auth.User) string {
	return firstNonEmpty(u.Name, u.Email, u.Wallet)
}

func (a *API) Routes(mux *http.ServeMux) {
	starting := func(w http.ResponseWriter) bool {
		if a.S.Ready() {
			return false
		}
		write(w, 503, map[string]string{"code": "STARTING"})
		return true
	}
	mux.HandleFunc("GET /api/halt", func(w http.ResponseWriter, r *http.Request) {
		if !starting(w) {
			a.Users.Require(a.system)(w, r)
		}
	})
	adm := func(pattern string, fn func(w http.ResponseWriter, r *http.Request, u *auth.User)) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if starting(w) {
				return
			}
			a.Users.Require(func(w http.ResponseWriter, r *http.Request) {
				u := auth.UserFrom(r.Context())
				// 只有所有者（这台第一个注册的账号）能进"系统"页（D113，11-24 用户："不然别人从外面或者随便登录一个账号就看到服务器上所有的账号了"）
				if u == nil || !u.Owner {
					write(w, 403, map[string]string{"code": "FORBIDDEN"})
					return
				}
				fn(w, r, u)
			})(w, r)
		})
	}
	a.sysRoutes(adm)
	// 登录页用：能不能自己注册（不用登录）
	mux.HandleFunc("GET /api/registration", func(w http.ResponseWriter, r *http.Request) {
		if !starting(w) {
			write(w, 200, map[string]bool{"open": a.Users.RegistrationOpen(r.Context())})
		}
	})
	adm("GET /api/admin", a.overview)
	adm("GET /api/admin/log", a.log)
	adm("POST /api/admin/users", a.addUser)
	adm("POST /api/admin/registration", a.registration)
	adm("POST /api/admin/halt", a.halt)
	adm("POST /api/admin/strategies/{id}/resume", a.resume)
	adm("POST /api/admin/strategies/{id}/revoke", a.revoke)
}

// system 所有登录的人都要知道是不是停机了（顶上一条横幅）
func (a *API) system(w http.ResponseWriter, r *http.Request) {
	h, err := a.S.Halt(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	write(w, 200, map[string]any{"halt": h})
}

func (a *API) overview(w http.ResponseWriter, r *http.Request, u *auth.User) {
	ctx := r.Context()
	users, err := a.Users.Members(ctx)
	if err != nil {
		fail(w, err)
		return
	}
	h, err := a.S.Halt(ctx)
	if err != nil {
		fail(w, err)
		return
	}
	log, err := a.S.Entries(ctx, 0, 50)
	if err != nil {
		fail(w, err)
		return
	}
	var strats any = []any{}
	if a.Strategies != nil {
		strats = a.Strategies()
	}
	write(w, 200, map[string]any{"me": map[string]any{"id": u.ID}, "users": users, "registration": a.S.RegMode(ctx), "halt": h, "strategies": strats, "log": log})
}

func (a *API) log(w http.ResponseWriter, r *http.Request, _ *auth.User) {
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	l, err := a.S.Entries(r.Context(), before, 100)
	if err != nil {
		fail(w, err)
		return
	}
	write(w, 200, map[string]any{"log": l})
}

func (a *API) record(ctx context.Context, u *auth.User, action, target string, detail map[string]any) {
	if err := a.S.Log(ctx, u.ID, who(u), action, target, detail); err != nil && a.Log != nil {
		a.Log.Error("管理后台：写操作记录失败", "action", action, "err", err)
	}
}

func (a *API) addUser(w http.ResponseWriter, r *http.Request, u *auth.User) {
	var in struct {
		Name          string `json:"name"`  // 账户名（D113）
		Email         string `json:"email"` // 老写法，当账户名用
		Password      string `json:"password"`
		Lang          string `json:"lang"`
		TradePassword string `json:"trade_password"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if err := a.Users.CheckTradePassword(u, in.TradePassword); err != nil {
		fail(w, err)
		return
	}
	nu, err := a.Users.AddMember(r.Context(), firstNonEmpty(in.Name, in.Email), in.Password, in.Lang)
	if err != nil {
		fail(w, err)
		return
	}
	a.record(r.Context(), u, "user_add", firstNonEmpty(nu.Name, nu.Email), nil)
	write(w, 200, map[string]any{"id": nu.ID, "name": nu.Name, "email": nu.Email})
}

func (a *API) registration(w http.ResponseWriter, r *http.Request, u *auth.User) {
	var in struct {
		Mode          string `json:"mode"`
		TradePassword string `json:"trade_password"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if in.Mode != "open" && in.Mode != "admin" {
		fail(w, errBad)
		return
	}
	// 打开注册要二级密码（放进来的人和你权限一样）；关上不要：往安全的方向改不拦，和停机一样（11-16 走查）
	if in.Mode == "open" {
		if err := a.Users.CheckTradePassword(u, in.TradePassword); err != nil {
			fail(w, err)
			return
		}
	}
	if err := a.S.SetRegMode(r.Context(), in.Mode); err != nil {
		fail(w, err)
		return
	}
	a.record(r.Context(), u, "registration", in.Mode, nil)
	write(w, 200, map[string]bool{"ok": true})
}

// halt 全局停机。往上调（停）不要二级密码，出事时要快；往下调（恢复）要。
func (a *API) halt(w http.ResponseWriter, r *http.Request, u *auth.User) {
	var in struct {
		Level         int    `json:"level"`
		Reason        string `json:"reason"`
		TradePassword string `json:"trade_password"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if in.Level < HaltNone || in.Level > HaltCancel || len([]rune(in.Reason)) > 200 {
		fail(w, errBad)
		return
	}
	cur, err := a.S.Halt(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	if in.Level < cur.Level {
		if err := a.Users.CheckTradePassword(u, in.TradePassword); err != nil {
			fail(w, err)
			return
		}
	}
	h := Halt{Level: in.Level, Reason: in.Reason, At: time.Now().UnixMilli(), By: who(u)}
	if in.Level == HaltNone {
		h = Halt{}
	}
	if err := a.S.SetHalt(r.Context(), h); err != nil {
		fail(w, err)
		return
	}
	a.G.setLevel(in.Level) // 先挡住新单，再撤挂单：撤的过程中不会有新的开仓单进来
	n := 0
	if in.Level == HaltCancel && a.CancelOpening != nil {
		n = a.CancelOpening()
	}
	a.record(r.Context(), u, "halt", strconv.Itoa(in.Level), map[string]any{"from": cur.Level, "reason": in.Reason, "cancelled": n})
	write(w, 200, map[string]any{"halt": h, "cancelled": n})
}

func (a *API) resume(w http.ResponseWriter, r *http.Request, u *auth.User) {
	var in struct {
		TradePassword string `json:"trade_password"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if err := a.Users.CheckTradePassword(u, in.TradePassword); err != nil {
		fail(w, err)
		return
	}
	id := r.PathValue("id")
	if a.Resume == nil || !a.Resume(id) {
		fail(w, errNoStrat)
		return
	}
	a.record(r.Context(), u, "strategy_resume", id, nil)
	write(w, 200, map[string]bool{"ok": true})
}

// revoke 吊销策略的密钥：这个策略发的消息马上不认（已有仓位不动，跟随的人自己决定平不平）。
func (a *API) revoke(w http.ResponseWriter, r *http.Request, u *auth.User) {
	var in struct {
		TradePassword string `json:"trade_password"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if err := a.Users.CheckTradePassword(u, in.TradePassword); err != nil {
		fail(w, err)
		return
	}
	id := r.PathValue("id")
	ok := false
	if a.Revoke != nil {
		var err error
		if ok, err = a.Revoke(r.Context(), id); err != nil {
			fail(w, err)
			return
		}
	}
	if !ok {
		fail(w, &auth.Err{HTTP: 400, Code: "STRATEGY_ENV"})
		return
	}
	a.record(r.Context(), u, "strategy_revoke", id, nil)
	write(w, 200, map[string]bool{"ok": true})
}

// firstNonEmpty 第一个不空的。
func firstNonEmpty(v ...string) string {
	for _, x := range v {
		if strings.TrimSpace(x) != "" {
			return x
		}
	}
	return ""
}
