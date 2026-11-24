package live

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"qs/internal/auth"
	"qs/internal/binance"
)

// API 接交易所账号的接口。保存密钥、确认实盘、删除都要二级密码。
type API struct {
	S     *Store
	D     *Desks
	Users *auth.Service
	// Client 测试用：换成假的交易所
	Client func(env, key, secret string) *binance.Client
	// OnAccount 新建了实盘账户（纪律锁登记）；OnArchive 停用了
	OnAccount func(user, acct int64)
	OnArchive func(acct int64)
}

func (h *API) client(env, key, secret string) *binance.Client {
	if h.Client != nil {
		return h.Client(env, key, secret)
	}
	ep := h.D.URLs[env]
	return &binance.Client{FAPI: ep.FAPI, SAPI: ep.SAPI, WS: ep.WS, Key: key, Secret: secret}
}

// Perms 保存时查到的密钥权限（存下来给界面看）。
type Perms struct {
	Futures    bool   `json:"futures"`
	Withdraw   bool   `json:"withdraw"`
	Transfer   bool   `json:"transfer"`
	Universal  bool   `json:"universal_transfer"`
	IPRestrict bool   `json:"ip_restrict"`
	DualSide   bool   `json:"dual_side"`
	Wallet     string `json:"wallet"`
}

// Check 查这把密钥能不能用：能读合约账户、没开提现和划转、账户是单向持仓。
// 提现和划转的权限是硬拒（D90：系统不碰钱）；没绑 IP 只提醒不拒（测试网绑不了 IP）。
func Check(ctx context.Context, c *binance.Client, env string) (*Perms, error) {
	p := &Perms{}
	acc, err := c.Account(ctx)
	if err != nil {
		if code := binance.Code(err); code == -2015 || code == -2014 || code == -1022 {
			return nil, ErrBadKey
		}
		return nil, err
	}
	p.Wallet = string(acc.TotalWalletBalance)
	p.Futures = true
	if env != "testnet" { // 测试网没有 sapi（查不了提现权限）；测试网的钱是假的
		r, err := c.Restrictions(ctx)
		if err != nil {
			if code := binance.Code(err); code == -2015 || code == -2014 || code == -1022 {
				return nil, ErrBadKey
			}
			return nil, err
		}
		p.Futures, p.Withdraw, p.Transfer, p.Universal, p.IPRestrict = r.EnableFutures, r.EnableWithdrawals, r.EnableInternalTransfer, r.PermitsUniversalTransfer, r.IPRestrict
		if p.Withdraw || p.Transfer || p.Universal {
			return p, ErrWithdraw
		}
		if !p.Futures {
			return p, ErrNoFutures
		}
	}
	dual, err := c.DualSide(ctx)
	if err != nil {
		return nil, err
	}
	p.DualSide = dual
	if dual {
		return p, ErrDualSide
	}
	return p, nil
}

type liveErr struct {
	Code  string `json:"code"`
	Perms *Perms `json:"perms,omitempty"`
}

func write(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, err error, perms *Perms) {
	var ae *auth.Err
	if errors.As(err, &ae) {
		write(w, ae.HTTP, map[string]string{"code": ae.Code})
		return
	}
	code, st := "INTERNAL_ERROR", 500
	for _, e := range []error{ErrNoMaster, ErrNotFound, ErrWithdraw, ErrNoFutures, ErrDualSide, ErrBadKey, ErrNotArmed, ErrUnsupported} {
		if errors.Is(err, e) {
			code, st = e.Error(), 400
		}
	}
	if errors.Is(err, ErrNotFound) {
		st = 404
	}
	if code == "INTERNAL_ERROR" && binance.Code(err) != 0 {
		code, st = "EXCHANGE_ERROR", 502
	}
	write(w, st, liveErr{Code: code, Perms: perms})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<10))
	if err != nil || json.Unmarshal(b, v) != nil {
		write(w, 400, map[string]string{"code": "BAD_REQUEST"})
		return false
	}
	return true
}

func (h *API) Routes(mux *http.ServeMux) {
	r := func(pattern string, fn func(w http.ResponseWriter, r *http.Request, u *auth.User)) {
		mux.HandleFunc(pattern, h.Users.Require(func(w http.ResponseWriter, req *http.Request) {
			if !h.S.Ready() {
				write(w, 503, map[string]string{"code": "STARTING"})
				return
			}
			fn(w, req, auth.UserFrom(req.Context()))
		}))
	}
	r("GET /api/live", h.overview)
	r("POST /api/live/keys", h.addKey)
	r("DELETE /api/live/keys/{id}", h.deleteKey)
	r("POST /api/live/accounts", h.addAccount)
	r("POST /api/live/accounts/{id}/arm", h.arm)
	r("POST /api/live/accounts/{id}/confirm", h.confirm)
	r("DELETE /api/live/accounts/{id}", h.archive)
}

type accountOut struct {
	Account
	Status Status `json:"status"`
	Env    string `json:"env"`
}

// overview 这个人接了哪些密钥、有哪些实盘账户、连接状态；enabled = 服务器配了主密钥；envs = 能接哪些环境。
func (h *API) overview(w http.ResponseWriter, r *http.Request, u *auth.User) {
	keys, err := h.S.Keys(r.Context(), u.ID)
	if err != nil {
		fail(w, err, nil)
		return
	}
	accts, err := h.S.Accounts(r.Context(), u.ID)
	if err != nil {
		fail(w, err, nil)
		return
	}
	env := map[int64]string{}
	for _, k := range keys {
		env[k.ID] = k.Env
	}
	out := []accountOut{}
	for _, a := range accts {
		out = append(out, accountOut{Account: a, Status: h.D.Status(a.ID), Env: env[a.KeyID]})
	}
	envs := []string{}
	for _, e := range []string{"main", "testnet", "fake"} {
		if _, ok := h.D.URLs[e]; ok {
			envs = append(envs, e)
		}
	}
	write(w, 200, map[string]any{"enabled": h.S.Enabled(), "envs": envs, "keys": keys, "accounts": out})
}

func (h *API) addKey(w http.ResponseWriter, r *http.Request, u *auth.User) {
	var in struct {
		Env           string `json:"env"`
		Label         string `json:"label"`
		Key           string `json:"key"`
		Secret        string `json:"secret"`
		TradePassword string `json:"trade_password"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	in.Key, in.Secret, in.Label = strings.TrimSpace(in.Key), strings.TrimSpace(in.Secret), strings.TrimSpace(in.Label)
	if _, ok := h.D.URLs[in.Env]; !ok || len(in.Key) < 16 || len(in.Key) > 128 || len(in.Secret) < 16 || len(in.Secret) > 128 || len([]rune(in.Label)) > 40 {
		write(w, 400, map[string]string{"code": "BAD_REQUEST"})
		return
	}
	if !h.S.Enabled() {
		fail(w, ErrNoMaster, nil)
		return
	}
	if err := h.Users.CheckTradePassword(u, in.TradePassword); err != nil {
		fail(w, err, nil)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	p, err := Check(ctx, h.client(in.Env, in.Key, in.Secret), in.Env)
	if err != nil {
		fail(w, err, p)
		return
	}
	pj, _ := json.Marshal(p)
	if in.Label == "" {
		in.Label = "Binance"
	}
	k, err := h.S.AddKey(ctx, u.ID, "binance", in.Env, in.Label, in.Key, in.Secret, string(pj))
	if err != nil {
		fail(w, err, nil)
		return
	}
	write(w, 200, map[string]any{"key": k, "perms": p})
}

func pathID(r *http.Request) (int64, bool) {
	n, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return n, err == nil && n > 0
}

func (h *API) deleteKey(w http.ResponseWriter, r *http.Request, u *auth.User) {
	id, ok := pathID(r)
	var in struct {
		TradePassword string `json:"trade_password"`
	}
	if !ok || !readJSON(w, r, &in) {
		return
	}
	if err := h.Users.CheckTradePassword(u, in.TradePassword); err != nil {
		fail(w, err, nil)
		return
	}
	accts, _ := h.S.Accounts(r.Context(), u.ID)
	if err := h.S.DeleteKey(r.Context(), u.ID, id); err != nil {
		fail(w, err, nil)
		return
	}
	for _, a := range accts {
		if a.KeyID == id {
			h.D.Stop(a.ID)
			if h.OnArchive != nil {
				h.OnArchive(a.ID)
			}
		}
	}
	write(w, 200, map[string]bool{"ok": true})
}

func (h *API) addAccount(w http.ResponseWriter, r *http.Request, u *auth.User) {
	var in struct {
		KeyID int64  `json:"key_id"`
		Name  string `json:"name"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len([]rune(in.Name)) > 24 {
		write(w, 400, map[string]string{"code": "BAD_REQUEST"})
		return
	}
	// 一把密钥只对应一个实盘账户：两个账户共用一个交易所账户，仓位会互相打架
	accts, _ := h.S.Accounts(r.Context(), u.ID)
	for _, a := range accts {
		if a.KeyID == in.KeyID {
			write(w, 409, map[string]string{"code": "KEY_IN_USE"})
			return
		}
	}
	a, err := h.S.AddAccount(r.Context(), u.ID, in.KeyID, in.Name)
	if err != nil {
		fail(w, err, nil)
		return
	}
	h.D.launch(*a)
	if h.OnAccount != nil {
		h.OnAccount(u.ID, a.ID)
	}
	write(w, 200, a)
}

// arm 用户确认"这是真钱，下单会真的成交"之后才能下单。
func (h *API) arm(w http.ResponseWriter, r *http.Request, u *auth.User) {
	id, ok := pathID(r)
	var in struct {
		TradePassword string `json:"trade_password"`
	}
	if !ok || !readJSON(w, r, &in) {
		return
	}
	if err := h.Users.CheckTradePassword(u, in.TradePassword); err != nil {
		fail(w, err, nil)
		return
	}
	if err := h.S.Arm(r.Context(), u.ID, id); err != nil {
		fail(w, err, nil)
		return
	}
	write(w, 200, map[string]bool{"ok": true})
}

// confirm 下单前要不要弹确认：打开随时可以，关掉要二级密码（D96）。
func (h *API) confirm(w http.ResponseWriter, r *http.Request, u *auth.User) {
	id, ok := pathID(r)
	var in struct {
		On            bool   `json:"on"`
		TradePassword string `json:"trade_password"`
	}
	if !ok || !readJSON(w, r, &in) {
		return
	}
	if !in.On {
		if err := h.Users.CheckTradePassword(u, in.TradePassword); err != nil {
			fail(w, err, nil)
			return
		}
	}
	if err := h.S.SetConfirm(r.Context(), u.ID, id, in.On); err != nil {
		fail(w, err, nil)
		return
	}
	write(w, 200, map[string]bool{"ok": true})
}

// archive 停用：只是这里不再管它，交易所那边的仓位和委托不动（界面会先提醒）。
func (h *API) archive(w http.ResponseWriter, r *http.Request, u *auth.User) {
	id, ok := pathID(r)
	if !ok {
		write(w, 404, map[string]string{"code": "NOT_FOUND"})
		return
	}
	if err := h.S.Archive(r.Context(), u.ID, id); err != nil {
		fail(w, err, nil)
		return
	}
	h.D.Stop(id)
	if h.OnArchive != nil {
		h.OnArchive(id)
	}
	write(w, 200, map[string]bool{"ok": true})
}
