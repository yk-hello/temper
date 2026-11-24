// Package live 实盘：用户把自己的交易所账号接进来（D89）。现在接币安 U 本位合约。
//
// 规矩（D90）：
//   - 钱永远在用户自己的交易所账户里，系统不经手。代码里没有提现、转账；密钥开了提现或划转权限就拒绝保存。
//   - 密钥加密存（AES-256-GCM，主密钥 QS_MASTER_KEY 不进数据库），界面只显示头尾几位。
//   - 账户、仓位、委托以交易所为准：这里只是镜像，收到交易所的推送就重新读一遍，另外每 10 秒对一次账。
//   - 实盘账户的编号从 1 万亿开始，和模拟账户分开；交易页的接口按编号分到这里。
package live

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"qs/internal/trading"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Base 实盘账户编号从这里开始；小于它的是模拟账户。
const Base = trading.LiveBase

// IsLive 这个账户编号是不是实盘账户。
func IsLive(acct int64) bool { return acct >= Base }

// 错误码（界面按语言翻译）。
var (
	ErrNoMaster    = errors.New("LIVE_NO_MASTER_KEY") // 没配主密钥，不能保存交易所密钥
	ErrNotFound    = errors.New("NOT_FOUND")
	ErrWithdraw    = errors.New("KEY_HAS_WITHDRAW")     // 密钥开了提现或划转
	ErrNoFutures   = errors.New("KEY_NO_FUTURES")       // 密钥没开合约
	ErrDualSide    = errors.New("ACCOUNT_DUAL_SIDE")    // 账户是双向持仓模式
	ErrBadKey      = errors.New("KEY_REJECTED")         // 交易所不认这把密钥
	ErrTradePw     = errors.New("TRADE_PASSWORD_WRONG") // 二级密码不对
	ErrNotArmed    = errors.New("LIVE_NOT_ARMED")       // 还没确认"这是真钱"
	ErrUnsupported = errors.New("NOT_SUPPORTED_LIVE")   // 实盘不支持这个操作（比如撤单撤回）
)

const schema = `
create sequence if not exists live_account_ids start 1000000000000;
create table if not exists exchange_keys (
  id         bigserial primary key,
  usr        bigint not null,
  exchange   text   not null,
  env        text   not null,
  label      text   not null,
  key_hint   text   not null,
  key_enc    bytea  not null,
  secret_enc bytea  not null,
  perms      jsonb  not null default '{}',
  created    bigint not null,
  checked    bigint not null
);
create index if not exists exchange_keys_u on exchange_keys (usr);
create table if not exists live_accounts (
  id          bigint primary key default nextval('live_account_ids'),
  usr         bigint not null,
  key_id      bigint not null,
  name        text   not null,
  armed       boolean not null default false,
  created     bigint not null,
  archived_at bigint
);
create index if not exists live_accounts_u on live_accounts (usr) where archived_at is null;
create table if not exists live_fills (
  account   bigint not null,
  trade_id  bigint not null,
  order_id  bigint not null,
  client_id text   not null default '',
  symbol    text   not null,
  side      text   not null,
  qty       numeric not null,
  px        numeric not null,
  fee       numeric not null,
  fee_asset text   not null,
  realized  numeric not null,
  maker     boolean not null,
  t         bigint not null,
  primary key (account, symbol, trade_id)
);
create index if not exists live_fills_t on live_fills (account, t);
-- 下单前要不要弹确认（D96：默认要，可以关，关要二级密码）
alter table live_accounts add column if not exists confirm boolean not null default true;
alter table live_fills add column if not exists seq bigserial;
create table if not exists live_funding (
  account bigint not null,
  tran_id bigint not null,
  symbol  text   not null,
  amount  numeric not null,
  t       bigint not null,
  primary key (account, tran_id)
);
create table if not exists live_orders (
  account   bigint not null,
  id        text   not null,
  client_id text   not null,
  symbol    text   not null,
  side      text   not null,
  type      text   not null,
  price     numeric not null default 0,
  trigger   numeric not null default 0,
  qty       numeric not null default 0,
  filled    numeric not null default 0,
  avg_px    numeric not null default 0,
  status    text   not null,
  source    text   not null,
  reason    text   not null default '',
  created   bigint not null,
  updated   bigint not null,
  primary key (account, id)
);
create index if not exists live_orders_t on live_orders (account, created);
`

// Store 实盘的数据库表和密钥加解密。
type Store struct {
	pool   *pgxpool.Pool
	master []byte // 32 字节
}

// MasterFromEnv 加密交易所密钥、策略密钥用的主密钥（32 字节）。
//   - QS_MASTER_KEY（base64）配了就用它；写错了返回 nil（不悄悄换一把：换了以前存的密钥就解不开了）
//   - 没配时看 QS_MASTER_KEY_FILE：文件在就读；不在就生成一把写进去（一条命令启动时不用手工配，D100）。
//     文件坏了也返回 nil，不覆盖。这个文件要备份：丢了只能重新接入密钥。
//   - 都没有返回 nil：实盘和自助建策略关着，界面写明怎么开。
func MasterFromEnv() []byte {
	if v := strings.TrimSpace(os.Getenv("QS_MASTER_KEY")); v != "" {
		b, err := base64.StdEncoding.DecodeString(v)
		if err != nil || len(b) != 32 {
			return nil
		}
		return b
	}
	path := strings.TrimSpace(os.Getenv("QS_MASTER_KEY_FILE"))
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err == nil {
		b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
		if err != nil || len(b) != 32 {
			return nil
		}
		return b
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(b)+"\n"), 0o600); err != nil {
		return nil
	}
	return b
}

// NewStore master 为 nil 时只能读，不能存密钥。
func NewStore(master []byte) *Store { return &Store{master: master} }

// Attach 建表。
func (s *Store) Attach(ctx context.Context, p *pgxpool.Pool) error {
	if _, err := p.Exec(ctx, schema); err != nil {
		return err
	}
	s.pool = p
	return nil
}

func (s *Store) Ready() bool   { return s != nil && s.pool != nil }
func (s *Store) Enabled() bool { return len(s.master) == 32 }

func (s *Store) seal(plain string) ([]byte, error) {
	if !s.Enabled() {
		return nil, ErrNoMaster
	}
	blk, err := aes.NewCipher(s.master)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return g.Seal(nonce, nonce, []byte(plain), nil), nil
}

func (s *Store) open(box []byte) (string, error) {
	if !s.Enabled() {
		return "", ErrNoMaster
	}
	blk, err := aes.NewCipher(s.master)
	if err != nil {
		return "", err
	}
	g, err := cipher.NewGCM(blk)
	if err != nil {
		return "", err
	}
	if len(box) < g.NonceSize() {
		return "", errors.New("密文太短")
	}
	p, err := g.Open(nil, box[:g.NonceSize()], box[g.NonceSize():], nil)
	if err != nil {
		return "", err
	}
	return string(p), nil
}

// Seal、Open 别的模块（自助建的策略密钥）用同一把主密钥加密。
func (s *Store) Seal(plain string) ([]byte, error) { return s.seal(plain) }
func (s *Store) Open(box []byte) (string, error)   { return s.open(box) }

// Hint 密钥只显示头四位和尾四位。
func Hint(key string) string {
	if len(key) <= 10 {
		return "****"
	}
	return key[:4] + "…" + key[len(key)-4:]
}

// KeyInfo 给界面看的一把密钥（不含密钥本身）。
type KeyInfo struct {
	ID       int64  `json:"id"`
	Exchange string `json:"exchange"`
	Env      string `json:"env"` // main | testnet | fake
	Label    string `json:"label"`
	Hint     string `json:"hint"`
	Perms    string `json:"perms"`
	Created  int64  `json:"created"`
	Checked  int64  `json:"checked"`
}

// AddKey 加密存一把密钥（权限已经查过）。
func (s *Store) AddKey(ctx context.Context, user int64, exchange, env, label, key, secret, perms string) (*KeyInfo, error) {
	ke, err := s.seal(key)
	if err != nil {
		return nil, err
	}
	se, err := s.seal(secret)
	if err != nil {
		return nil, err
	}
	now := time.Now().UnixMilli()
	k := &KeyInfo{Exchange: exchange, Env: env, Label: label, Hint: Hint(key), Perms: perms, Created: now, Checked: now}
	err = s.pool.QueryRow(ctx, `insert into exchange_keys (usr, exchange, env, label, key_hint, key_enc, secret_enc, perms, created, checked)
		values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$9) returning id`, user, exchange, env, label, k.Hint, ke, se, perms, now).Scan(&k.ID)
	return k, err
}

// Keys 这个人的密钥。
func (s *Store) Keys(ctx context.Context, user int64) ([]KeyInfo, error) {
	rows, err := s.pool.Query(ctx, `select id, exchange, env, label, key_hint, perms::text, created, checked from exchange_keys where usr=$1 order by id`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []KeyInfo{}
	for rows.Next() {
		var k KeyInfo
		if err := rows.Scan(&k.ID, &k.Exchange, &k.Env, &k.Label, &k.Hint, &k.Perms, &k.Created, &k.Checked); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// secretOf 解密一把密钥（只在后端用，不出接口）。
func (s *Store) secretOf(ctx context.Context, keyID int64) (env, key, secret string, err error) {
	var ke, se []byte
	if err = s.pool.QueryRow(ctx, `select env, key_enc, secret_enc from exchange_keys where id=$1`, keyID).Scan(&env, &ke, &se); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			err = ErrNotFound
		}
		return
	}
	if key, err = s.open(ke); err != nil {
		return
	}
	secret, err = s.open(se)
	return
}

// DeleteKey 删密钥：用它的实盘账户一起停用。
func (s *Store) DeleteKey(ctx context.Context, user, id int64) error {
	t, err := s.pool.Exec(ctx, `delete from exchange_keys where usr=$1 and id=$2`, user, id)
	if err != nil {
		return err
	}
	if t.RowsAffected() == 0 {
		return ErrNotFound
	}
	_, err = s.pool.Exec(ctx, `update live_accounts set archived_at=$3 where usr=$1 and key_id=$2 and archived_at is null`, user, id, time.Now().UnixMilli())
	return err
}

// Account 一个实盘账户。
type Account struct {
	ID      int64  `json:"id"`
	User    int64  `json:"-"`
	KeyID   int64  `json:"key_id"`
	Name    string `json:"name"`
	Armed   bool   `json:"armed"`
	Confirm bool   `json:"confirm"` // 下单前弹确认（D96）
	Created int64  `json:"created"`
}

func (s *Store) AddAccount(ctx context.Context, user, keyID int64, name string) (*Account, error) {
	var ok bool
	if err := s.pool.QueryRow(ctx, `select exists (select 1 from exchange_keys where id=$1 and usr=$2)`, keyID, user).Scan(&ok); err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotFound
	}
	a := &Account{User: user, KeyID: keyID, Name: name, Created: time.Now().UnixMilli()}
	err := s.pool.QueryRow(ctx, `insert into live_accounts (usr, key_id, name, created) values ($1,$2,$3,$4) returning id`, user, keyID, name, a.Created).Scan(&a.ID)
	return a, err
}

// Accounts 所有没停用的实盘账户（user = 0 时是全部，启动时用）。
func (s *Store) Accounts(ctx context.Context, user int64) ([]Account, error) {
	q, args := `select id, usr, key_id, name, armed, confirm, created from live_accounts where archived_at is null order by id`, []any{}
	if user > 0 {
		q, args = `select id, usr, key_id, name, armed, confirm, created from live_accounts where archived_at is null and usr=$1 order by id`, []any{user}
	}
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Account{}
	for rows.Next() {
		var a Account
		if err := rows.Scan(&a.ID, &a.User, &a.KeyID, &a.Name, &a.Armed, &a.Confirm, &a.Created); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) Owns(ctx context.Context, user, acct int64) bool {
	var ok bool
	_ = s.pool.QueryRow(ctx, `select exists (select 1 from live_accounts where id=$1 and usr=$2 and archived_at is null)`, acct, user).Scan(&ok)
	return ok
}

// EnvOf 交易所账户接的是哪个环境：main（真钱）、testnet（币安模拟交易）、fake（测试用）。查不到返回空。
func (s *Store) EnvOf(ctx context.Context, acct int64) string {
	if s.pool == nil {
		return ""
	}
	var env string
	_ = s.pool.QueryRow(ctx, `select k.env from live_accounts a join exchange_keys k on k.id = a.key_id where a.id = $1`, acct).Scan(&env)
	return env
}

// OwnedArmed 是不是这个人的账户、确认过"这是真钱"没有（策略跟随开启前查）。
func (s *Store) OwnedArmed(ctx context.Context, user, acct int64) (owned, armed bool) {
	err := s.pool.QueryRow(ctx, `select true, armed from live_accounts where id=$1 and usr=$2 and archived_at is null`, acct, user).Scan(&owned, &armed)
	if err != nil {
		return false, false
	}
	return
}

// DisarmAll 这个人的交易所账户全部回到"未开启"：开新仓要用二级密码重新开启，减仓、平仓、止盈止损照常
// （D107：二级密码被重置以后）。
func (s *Store) DisarmAll(ctx context.Context, user int64) error {
	_, err := s.pool.Exec(ctx, `update live_accounts set armed=false where usr=$1 and archived_at is null`, user)
	return err
}

func (s *Store) Arm(ctx context.Context, user, acct int64) error {
	t, err := s.pool.Exec(ctx, `update live_accounts set armed=true where id=$1 and usr=$2 and archived_at is null`, acct, user)
	if err == nil && t.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// SetConfirm 下单前要不要弹确认（关要二级密码，在接口那边查）。
func (s *Store) SetConfirm(ctx context.Context, user, acct int64, on bool) error {
	t, err := s.pool.Exec(ctx, `update live_accounts set confirm=$3 where id=$1 and usr=$2 and archived_at is null`, acct, user, on)
	if err == nil && t.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

func (s *Store) Archive(ctx context.Context, user, acct int64) error {
	t, err := s.pool.Exec(ctx, `update live_accounts set archived_at=$3 where id=$1 and usr=$2 and archived_at is null`, acct, user, time.Now().UnixMilli())
	if err == nil && t.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// DeleteUser 删账户时：密钥、实盘账户和它们的本地记录一起删（交易所那边的东西不动）。
func (s *Store) DeleteUser(ctx context.Context, user int64) {
	if s.pool == nil {
		return
	}
	_, _ = s.pool.Exec(ctx, `delete from live_fills where account in (select id from live_accounts where usr=$1)`, user)
	_, _ = s.pool.Exec(ctx, `delete from live_funding where account in (select id from live_accounts where usr=$1)`, user)
	_, _ = s.pool.Exec(ctx, `delete from live_orders where account in (select id from live_accounts where usr=$1)`, user)
	_, _ = s.pool.Exec(ctx, `delete from live_accounts where usr=$1`, user)
	_, _ = s.pool.Exec(ctx, `delete from exchange_keys where usr=$1`, user)
}

func (s *Store) String() string { return fmt.Sprintf("live.Store(enabled=%v)", s.Enabled()) }
