// Package binapi 和币安 U 本位合约一样的接口（D96 第 1 条，18 第 5.3 条）：把模拟盘包装成币安的样子，
// 现成的程序（自己写的、ccxt、Freqtrade、Hummingbot）只改一个地址、换一把这里发的模拟密钥，就跑在仿真盘上；
// 验证完把地址换回币安，就是实盘，代码不用改。
//
// 只对模拟账户：一把密钥绑一个模拟账户。下单走模拟盘同一个撮合引擎，纪律锁、全局停机、停用用户一样生效。
// 不仿的接口回明确的错误，不装作成功。
package binapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"qs/internal/auth"
)

const schema = `
create table if not exists binapi_keys (
  api_key    text primary key,
  user_id    bigint not null,
  account_id bigint not null,
  label      text not null default '',
  secret_enc bytea not null,
  created_at timestamptz not null default now(),
  revoked_at timestamptz
);
create index if not exists binapi_keys_user on binapi_keys (user_id) where revoked_at is null;
`

// MaxPerUser 每人最多几把（防滥用）
var MaxPerUser = 10 // 部署的人可以改：QS_LIMIT_API_KEYS（D103）

// Crypto 加解密（live.Store 的 Seal/Open，和交易所密钥同一把主密钥）。
type Crypto interface {
	Seal(plain string) ([]byte, error)
	Open(box []byte) (string, error)
	Enabled() bool
}

// Key 一把能用的密钥。
type Key struct {
	APIKey, Secret string
	User, Account  int64
}

var (
	ErrNoMaster = errors.New("NO_MASTER_KEY")
	ErrTooMany  = errors.New("BINAPI_LIMIT")
	ErrNotFound = errors.New("NOT_FOUND")
)

// Store 密钥表，能用的密钥缓存在内存里（每个请求都要查）。
type Store struct {
	Pool *pgxpool.Pool
	C    Crypto

	mu    sync.RWMutex
	cache map[string]Key
}

func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.Pool.Exec(ctx, schema); err != nil {
		return err
	}
	return s.reload(ctx)
}

func (s *Store) reload(ctx context.Context) error {
	rows, err := s.Pool.Query(ctx, `select api_key, user_id, account_id, secret_enc from binapi_keys where revoked_at is null`)
	if err != nil {
		return err
	}
	defer rows.Close()
	m := map[string]Key{}
	for rows.Next() {
		var k Key
		var box []byte
		if err := rows.Scan(&k.APIKey, &k.User, &k.Account, &box); err != nil {
			return err
		}
		if k.Secret, err = s.C.Open(box); err == nil {
			m[k.APIKey] = k
		}
	}
	s.mu.Lock()
	s.cache = m
	s.mu.Unlock()
	return rows.Err()
}

// Lookup 认一把密钥。
func (s *Store) Lookup(apiKey string) (Key, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	k, ok := s.cache[apiKey]
	return k, ok
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Item 列表的一行（不含 secret）。
type Item struct {
	APIKey    string    `json:"api_key"`
	Account   int64     `json:"account"`
	Label     string    `json:"label"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Store) Mine(ctx context.Context, user int64) ([]Item, error) {
	rows, err := s.Pool.Query(ctx, `select api_key, account_id, label, created_at from binapi_keys where user_id = $1 and revoked_at is null order by created_at`, user)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Item, error) {
		var it Item
		err := r.Scan(&it.APIKey, &it.Account, &it.Label, &it.CreatedAt)
		return it, err
	})
}

// Create 发一把新密钥（64 位十六进制，和币安的长度一样），secret 只返回这一次。
func (s *Store) Create(ctx context.Context, user, account int64, label string) (*Key, error) {
	if !s.C.Enabled() {
		return nil, ErrNoMaster
	}
	var n int
	if err := s.Pool.QueryRow(ctx, `select count(*) from binapi_keys where user_id = $1 and revoked_at is null`, user).Scan(&n); err != nil {
		return nil, err
	}
	if n >= MaxPerUser {
		return nil, ErrTooMany
	}
	k := &Key{APIKey: randHex(32), Secret: randHex(32), User: user, Account: account}
	box, err := s.C.Seal(k.Secret)
	if err != nil {
		return nil, err
	}
	if _, err := s.Pool.Exec(ctx, `insert into binapi_keys (api_key, user_id, account_id, label, secret_enc) values ($1,$2,$3,$4,$5)`, k.APIKey, user, account, label, box); err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.cache == nil {
		s.cache = map[string]Key{}
	}
	s.cache[k.APIKey] = *k
	s.mu.Unlock()
	return k, nil
}

// Revoke 吊销（user = 0：不管是谁的，账户被删、用户被删时用）。
func (s *Store) Revoke(ctx context.Context, user int64, apiKey string) error {
	tag, err := s.Pool.Exec(ctx, `update binapi_keys set revoked_at = now() where api_key = $1 and ($2 = 0 or user_id = $2) and revoked_at is null`, apiKey, user)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	s.mu.Lock()
	delete(s.cache, apiKey)
	s.mu.Unlock()
	return nil
}

// RevokeAccount 账户归档（重置、删除）时把绑它的密钥都吊销。
func (s *Store) RevokeAccount(ctx context.Context, account int64) {
	if s.Pool == nil {
		return
	}
	_, _ = s.Pool.Exec(ctx, `update binapi_keys set revoked_at = now() where account_id = $1 and revoked_at is null`, account)
	s.mu.Lock()
	for k, v := range s.cache {
		if v.Account == account {
			delete(s.cache, k)
		}
	}
	s.mu.Unlock()
}

// ---------- 管理密钥的接口（登录后，账户页用） ----------

// KeysAPI 建、列、吊销。建和吊销要二级密码（密钥能在模拟账户上下单）。
type KeysAPI struct {
	S     *Store
	Users *auth.Service
	Ready func() bool
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, err error) {
	var ae *auth.Err
	switch {
	case errors.As(err, &ae):
		writeJSON(w, ae.HTTP, map[string]string{"code": ae.Code})
	case errors.Is(err, ErrNoMaster), errors.Is(err, ErrTooMany):
		writeJSON(w, 400, map[string]string{"code": err.Error()})
	case errors.Is(err, ErrNotFound):
		writeJSON(w, 404, map[string]string{"code": "NOT_FOUND"})
	default:
		writeJSON(w, 500, map[string]string{"code": "INTERNAL_ERROR"})
	}
}

func (a *KeysAPI) Routes(mux *http.ServeMux) {
	r := func(pattern string, fn func(w http.ResponseWriter, r *http.Request, u *auth.User)) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, req *http.Request) {
			if a.Ready != nil && !a.Ready() {
				writeJSON(w, 503, map[string]string{"code": "STARTING"})
				return
			}
			a.Users.Require(func(w http.ResponseWriter, req *http.Request) { fn(w, req, auth.UserFrom(req.Context())) })(w, req)
		})
	}
	r("GET /api/binapi-keys", func(w http.ResponseWriter, req *http.Request, u *auth.User) {
		items, err := a.S.Mine(req.Context(), u.ID)
		if err != nil {
			fail(w, err)
			return
		}
		if items == nil {
			items = []Item{}
		}
		writeJSON(w, 200, map[string]any{"enabled": a.S.C.Enabled(), "items": items})
	})
	r("POST /api/binapi-keys", func(w http.ResponseWriter, req *http.Request, u *auth.User) {
		var in struct {
			Account int64  `json:"account"`
			Label   string `json:"label"`
		}
		b, _ := io.ReadAll(http.MaxBytesReader(w, req.Body, 4<<10))
		if json.Unmarshal(b, &in) != nil || len([]rune(strings.TrimSpace(in.Label))) > 40 {
			writeJSON(w, 400, map[string]string{"code": "BAD_REQUEST"})
			return
		}
		if !a.Users.OwnsPaper(req.Context(), u.ID, in.Account) {
			writeJSON(w, 404, map[string]string{"code": "NOT_FOUND"})
			return
		}
		// 不要二级密码（D107）：这种密钥只能动模拟账户，钱是假的
		k, err := a.S.Create(req.Context(), u.ID, in.Account, strings.TrimSpace(in.Label))
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"api_key": k.APIKey, "secret": k.Secret, "account": k.Account})
	})
	r("POST /api/binapi-keys/{key}/revoke", func(w http.ResponseWriter, req *http.Request, u *auth.User) {
		// 删掉一把模拟账户的密钥：往安全的方向，不要二级密码（D107）
		if err := a.S.Revoke(req.Context(), u.ID, req.PathValue("key")); err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
}
