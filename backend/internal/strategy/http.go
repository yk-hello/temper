package strategy

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"qs/internal/auth"
)

// API 我的策略：建、换密钥、吊销。都要二级密码（密钥能驱动账户下单）。
type API struct {
	S     *Store
	Users *auth.Service
	// OnKey 新密钥生效（交给 qsig、建模型组合）；OnRevoke 吊销（qsig 马上不认）
	OnKey    func(k Key)
	OnRevoke func(keyID string)
	// Ready 数据库连上之前返回 503
	Ready func() bool
}

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
	for _, e := range []error{ErrBadID, ErrTaken, ErrNoKey, ErrTooMany} {
		if errors.Is(err, e) {
			write(w, 400, map[string]string{"code": e.Error()})
			return
		}
	}
	if errors.Is(err, ErrNotYour) {
		write(w, 404, map[string]string{"code": "NOT_FOUND"})
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

func (a *API) Routes(mux *http.ServeMux) {
	r := func(pattern string, fn func(w http.ResponseWriter, r *http.Request, u *auth.User)) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, req *http.Request) {
			if a.Ready != nil && !a.Ready() {
				write(w, 503, map[string]string{"code": "STARTING"})
				return
			}
			a.Users.Require(func(w http.ResponseWriter, req *http.Request) { fn(w, req, auth.UserFrom(req.Context())) })(w, req)
		})
	}
	r("GET /api/my-strategies", a.list)
	r("POST /api/my-strategies", a.create)
	r("POST /api/my-strategies/{id}/rotate", a.rotate)
	r("POST /api/my-strategies/{id}/revoke", a.revoke)
}

func (a *API) list(w http.ResponseWriter, r *http.Request, u *auth.User) {
	items, err := a.S.Mine(r.Context(), u.ID)
	if err != nil {
		fail(w, err)
		return
	}
	write(w, 200, map[string]any{"enabled": a.S.C.Enabled(), "items": items, "max": MaxPerUser})
}

type secretOut struct {
	StrategyID string `json:"strategy_id"`
	KeyID      string `json:"key_id"`
	Secret     string `json:"secret"` // 只在这一次返回
}

func (a *API) create(w http.ResponseWriter, r *http.Request, u *auth.User) {
	var in struct {
		StrategyID string `json:"strategy_id"`
		Name       string `json:"name"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	in.StrategyID, in.Name = strings.ToLower(strings.TrimSpace(in.StrategyID)), strings.TrimSpace(in.Name)
	if len([]rune(in.Name)) > 40 {
		fail(w, ErrBadID)
		return
	}
	// 新建不要二级密码（D107）：新策略没有人跟随，碰不到钱；把它接到交易所账户上跟随时要二级密码。
	// 换密钥、吊销仍然要：换掉你正在跟随的策略的密钥，就能借它往你的账户下单
	k, err := a.S.Create(r.Context(), u.ID, in.StrategyID, in.Name)
	if err != nil {
		fail(w, err)
		return
	}
	if a.OnKey != nil {
		a.OnKey(*k)
	}
	write(w, 200, secretOut{StrategyID: k.StrategyID, KeyID: k.KeyID, Secret: k.Secret})
}

func (a *API) rotate(w http.ResponseWriter, r *http.Request, u *auth.User) {
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
	old, k, err := a.S.Rotate(r.Context(), u.ID, r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	if a.OnRevoke != nil {
		a.OnRevoke(old)
	}
	if a.OnKey != nil {
		a.OnKey(*k)
	}
	write(w, 200, secretOut{StrategyID: k.StrategyID, KeyID: k.KeyID, Secret: k.Secret})
}

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
	old, err := a.S.Revoke(r.Context(), u.ID, r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	if a.OnRevoke != nil {
		a.OnRevoke(old)
	}
	write(w, 200, map[string]bool{"ok": true})
}
