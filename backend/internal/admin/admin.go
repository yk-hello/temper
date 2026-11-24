// Package admin 管理后台（D89 第 5 条：几个人共用一套时用）：角色、停用、注册方式、全局停机、策略、操作记录。
// 不是大平台的运营后台：没有收费、没有资金操作（D90）。
package admin

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"qs/internal/trading"
)

// 全局停机的两档
const (
	HaltNone   = 0
	HaltOpen   = 1 // 停止开新仓：减仓、平仓照常
	HaltCancel = 2 // 停止开新仓，并撤掉所有开仓挂单
)

// Gate 下单前的全局检查。在撮合引擎的锁里调用：只读内存，不碰数据库，不回调引擎。
type Gate struct {
	halt atomic.Int32
}

// Check 开仓、加仓前调用（减仓和平仓不经过这里）。
func (g *Gate) Check(acct int64) error {
	if g == nil {
		return nil
	}
	if g.halt.Load() > HaltNone {
		return trading.NewReject("SYSTEM_HALTED")
	}
	return nil
}

// Level 现在的停机档位。
func (g *Gate) Level() int { return int(g.halt.Load()) }

func (g *Gate) setLevel(l int) { g.halt.Store(int32(l)) }

// ---------- 存储：设置和操作记录 ----------

const schema = `
create table if not exists admin_settings (
  key        text primary key,
  value      jsonb not null,
  updated_at timestamptz not null default now()
);
-- 操作记录只追加：改和删都会报错（导出看历史时不用担心被人改过）
create table if not exists admin_log (
  id          bigserial primary key,
  at          timestamptz not null default now(),
  actor       bigint not null,
  actor_email text not null default '',
  action      text not null,
  target      text not null default '',
  detail      jsonb not null default '{}'
);
create or replace function admin_log_append_only() returns trigger language plpgsql as $$
begin raise exception 'admin_log is append-only'; end $$;
create or replace trigger admin_log_no_change before update or delete on admin_log
  for each row execute function admin_log_append_only();
`

// Store 管理后台的数据。
type Store struct {
	// SignupOpen 没在"系统"页里设过注册方式时的默认值（配置 QS_SIGNUP=open）；默认关：第一个账号以后别人注册不进来
	SignupOpen bool
	Pool       *pgxpool.Pool
	mu         sync.Mutex
	reg        string // 注册方式缓存：open | admin
	ready      atomic.Bool
}

func (s *Store) Migrate(ctx context.Context) error {
	_, err := s.Pool.Exec(ctx, schema)
	if err == nil {
		s.ready.Store(true)
	}
	return err
}

// Ready 数据库连上、表建好了（之前接口返回 503）。
func (s *Store) Ready() bool { return s != nil && s.ready.Load() }

func (s *Store) get(ctx context.Context, key string, v any) (bool, error) {
	var raw []byte
	err := s.Pool.QueryRow(ctx, `select value from admin_settings where key = $1`, key).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal(raw, v)
}

func (s *Store) put(ctx context.Context, key string, v any) error {
	raw, _ := json.Marshal(v)
	_, err := s.Pool.Exec(ctx, `insert into admin_settings (key, value) values ($1,$2) on conflict (key) do update set value = excluded.value, updated_at = now()`, key, raw)
	return err
}

// RegMode 注册方式：open 谁都能注册；admin 只能管理员添加。默认 open（个人自己用时不用管）。
func (s *Store) RegMode(ctx context.Context) string {
	s.mu.Lock()
	if s.reg != "" {
		r := s.reg
		s.mu.Unlock()
		return r
	}
	s.mu.Unlock()
	var v struct {
		Mode string `json:"mode"`
	}
	if ok, err := s.get(ctx, "registration", &v); err != nil || !ok || (v.Mode != "admin" && v.Mode != "open") {
		v.Mode = "admin" // 没在"系统"页里设过：按配置（QS_SIGNUP），默认关
		if s.SignupOpen {
			v.Mode = "open"
		}
	}
	s.mu.Lock()
	s.reg = v.Mode
	s.mu.Unlock()
	return v.Mode
}

func (s *Store) SetRegMode(ctx context.Context, mode string) error {
	if err := s.put(ctx, "registration", map[string]string{"mode": mode}); err != nil {
		return err
	}
	s.mu.Lock()
	s.reg = mode
	s.mu.Unlock()
	return nil
}

// Halt 全局停机的状态。
type Halt struct {
	Level  int    `json:"level"`
	Reason string `json:"reason,omitempty"`
	At     int64  `json:"at,omitempty"`
	By     string `json:"by,omitempty"`
}

func (s *Store) Halt(ctx context.Context) (Halt, error) {
	var h Halt
	_, err := s.get(ctx, "halt", &h)
	return h, err
}

func (s *Store) SetHalt(ctx context.Context, h Halt) error { return s.put(ctx, "halt", h) }

// Entry 一条操作记录。
type Entry struct {
	ID         int64           `json:"id"`
	At         time.Time       `json:"at"`
	Actor      int64           `json:"actor"`
	ActorEmail string          `json:"actor_email"`
	Action     string          `json:"action"`
	Target     string          `json:"target"`
	Detail     json.RawMessage `json:"detail"`
}

// Log 记一条。detail 是结构化的，界面按语言拼文字。
func (s *Store) Log(ctx context.Context, actor int64, email, action, target string, detail map[string]any) error {
	if detail == nil {
		detail = map[string]any{}
	}
	raw, _ := json.Marshal(detail)
	_, err := s.Pool.Exec(ctx, `insert into admin_log (actor, actor_email, action, target, detail) values ($1,$2,$3,$4,$5)`, actor, email, action, target, raw)
	return err
}

// Entries 最近的记录，before > 0 时取比它早的（翻页）。
func (s *Store) Entries(ctx context.Context, before int64, n int) ([]Entry, error) {
	q := `select id, at, actor, actor_email, action, target, detail from admin_log where ($1 = 0 or id < $1) order by id desc limit $2`
	rows, err := s.Pool.Query(ctx, q, before, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.At, &e.Actor, &e.ActorEmail, &e.Action, &e.Target, &e.Detail); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
