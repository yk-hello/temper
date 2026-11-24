// Package strategy 自助建策略（D96 第 2 条）：用户自己起策略名、拿 qsig/1 密钥，不用改环境变量、不用重启。
// 密钥用 QS_MASTER_KEY 加密存（和交易所密钥同一把主密钥）；签名要用原文，所以不能只存哈希。
// 我们不提供策略（D96 第 3 条）：这里只是"接进来"的入口。
package strategy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const schema = `
create table if not exists strategy_keys (
  key_id      text primary key,
  strategy_id text not null,
  user_id     bigint not null,
  name        text not null default '',
  secret_enc  bytea not null,
  created_at  timestamptz not null default now(),
  revoked_at  timestamptz
);
-- 一个策略同一时间只有一把能用的密钥（换密钥 = 旧的吊销、新的生效）
create unique index if not exists strategy_keys_live on strategy_keys (strategy_id) where revoked_at is null;
create index if not exists strategy_keys_user on strategy_keys (user_id);
`

var (
	ErrBadID   = errors.New("STRATEGY_BAD_ID")
	ErrTaken   = errors.New("STRATEGY_TAKEN")
	ErrNotYour = errors.New("NOT_FOUND")
	ErrNoKey   = errors.New("NO_MASTER_KEY") // 没配 QS_MASTER_KEY：和交易所密钥一样不能存
	ErrTooMany = errors.New("STRATEGY_LIMIT")
)

// MaxPerUser 每人最多几个策略（防滥用；小团队够用）
var MaxPerUser = 20 // 部署的人可以改：QS_LIMIT_STRATEGIES（D103）

var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{2,39}$`)

// Crypto 加解密（live.Store 的 Seal/Open）。
type Crypto interface {
	Seal(plain string) ([]byte, error)
	Open(box []byte) (string, error)
	Enabled() bool
}

// Key 一把能用的密钥（给 qsig 认签名）。
type Key struct {
	KeyID, Secret, StrategyID string
	UserID                    int64
}

// Item 我的策略列表的一行（不含密钥原文）。
type Item struct {
	StrategyID string    `json:"strategy_id"`
	Name       string    `json:"name"`
	KeyID      string    `json:"key_id"`
	CreatedAt  time.Time `json:"created_at"`
	RotatedAt  time.Time `json:"rotated_at"`
}

type Store struct {
	Pool *pgxpool.Pool
	C    Crypto
	// Reserved 环境变量里配的策略 id（别人不能再用）
	Reserved func(id string) bool
}

func (s *Store) Migrate(ctx context.Context) error {
	_, err := s.Pool.Exec(ctx, schema)
	return err
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Active 所有能用的密钥（启动时交给 qsig）。
func (s *Store) Active(ctx context.Context) ([]Key, error) {
	rows, err := s.Pool.Query(ctx, `select key_id, strategy_id, user_id, secret_enc from strategy_keys where revoked_at is null`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Key
	for rows.Next() {
		var k Key
		var box []byte
		if err := rows.Scan(&k.KeyID, &k.StrategyID, &k.UserID, &box); err != nil {
			return nil, err
		}
		if k.Secret, err = s.C.Open(box); err != nil {
			continue // 主密钥换了：这把解不开，跳过（用户换一把就行）
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// Mine 这个人的策略。
func (s *Store) Mine(ctx context.Context, user int64) ([]Item, error) {
	rows, err := s.Pool.Query(ctx, `
select strategy_id, name, key_id,
  (select min(created_at) from strategy_keys o where o.strategy_id = k.strategy_id and o.user_id = k.user_id), created_at
from strategy_keys k where user_id = $1 and revoked_at is null order by created_at`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Item{}
	for rows.Next() {
		var it Item
		if err := rows.Scan(&it.StrategyID, &it.Name, &it.KeyID, &it.CreatedAt, &it.RotatedAt); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// Owner 策略是谁建的（环境变量里配的返回 0）。
func (s *Store) Owner(ctx context.Context, id string) (int64, error) {
	var u int64
	err := s.Pool.QueryRow(ctx, `select user_id from strategy_keys where strategy_id = $1 order by created_at limit 1`, id).Scan(&u)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return u, err
}

// Create 新建策略：返回密钥原文（只这一次）。
func (s *Store) Create(ctx context.Context, user int64, id, name string) (*Key, error) {
	if !idRe.MatchString(id) || id == "demo-random" {
		return nil, ErrBadID
	}
	if !s.C.Enabled() {
		return nil, ErrNoKey
	}
	if s.Reserved != nil && s.Reserved(id) {
		return nil, ErrTaken
	}
	var n int
	if err := s.Pool.QueryRow(ctx, `select count(*) from strategy_keys where user_id = $1 and revoked_at is null`, user).Scan(&n); err != nil {
		return nil, err
	}
	if n >= MaxPerUser {
		return nil, ErrTooMany
	}
	// 策略名一旦用过就归这个人（吊销了也不让别人用：别人的跟随、记录还挂在这个名字上）
	if owner, err := s.Owner(ctx, id); err != nil {
		return nil, err
	} else if owner != 0 {
		return nil, ErrTaken
	}
	return s.issue(ctx, user, id, name)
}

func (s *Store) issue(ctx context.Context, user int64, id, name string) (*Key, error) {
	k := &Key{KeyID: "k-" + randHex(8), Secret: randHex(32), StrategyID: id, UserID: user}
	box, err := s.C.Seal(k.Secret)
	if err != nil {
		return nil, err
	}
	_, err = s.Pool.Exec(ctx, `insert into strategy_keys (key_id, strategy_id, user_id, name, secret_enc) values ($1,$2,$3,$4,$5)`, k.KeyID, id, user, name, box)
	if err != nil {
		return nil, ErrTaken // 唯一索引：同名同时建
	}
	return k, nil
}

// Rotate 换密钥：旧的吊销、新的生效，返回旧 key id 和新密钥。
func (s *Store) Rotate(ctx context.Context, user int64, id string) (string, *Key, error) {
	if !s.C.Enabled() {
		return "", nil, ErrNoKey
	}
	var old, name string
	err := s.Pool.QueryRow(ctx, `update strategy_keys set revoked_at = now() where strategy_id = $1 and user_id = $2 and revoked_at is null returning key_id, name`, id, user).Scan(&old, &name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, ErrNotYour
	}
	if err != nil {
		return "", nil, err
	}
	k, err := s.issue(ctx, user, id, name)
	return old, k, err
}

// Revoke 吊销（user = 0 表示管理员吊销任何人的）。返回吊销的 key id。
func (s *Store) Revoke(ctx context.Context, user int64, id string) (string, error) {
	var old string
	err := s.Pool.QueryRow(ctx, `update strategy_keys set revoked_at = now() where strategy_id = $1 and ($2 = 0 or user_id = $2) and revoked_at is null returning key_id`, id, user).Scan(&old)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotYour
	}
	return old, err
}
