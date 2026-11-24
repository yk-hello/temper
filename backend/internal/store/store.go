// Package store 把信号和模型组合的状态写进 PostgreSQL，重启后完整恢复（08 任务 4f）。
// 持仓和钱在 trading 引擎的事件表里（D74）。
//
//   - ledger_entries：搬引擎之前的旧账本。留着不删，不再读写
//   - strategy_accounts：每个策略在引擎里的系统账户（负数 id，和用户账户不冲突）
//   - qsig_messages：收到的每条消息和回复（审计、幂等：重启后同 id 重发仍判为重复）
//   - qsig_executions：每条信号的执行回报
//   - portfolio_state：组合里不能从成交推出的状态（目标、止损止盈、暂停、熔断等），每秒存一次
//   - nav_points：净值曲线（每分钟一个点）
package store

import (
	"context"
	"encoding/json"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	Pool *pgxpool.Pool
}

const schema = `
create table if not exists ledger_entries (
  seq        bigint primary key,
  portfolio  text not null,
  time       timestamptz not null,
  ref        text not null default '',
  memo       text not null,
  lines      jsonb not null,
  trade      jsonb
);
create index if not exists ledger_entries_portfolio on ledger_entries (portfolio, seq);

create sequence if not exists strategy_account_seq;
create table if not exists strategy_accounts (
  strategy_id text primary key,
  account_id  bigint not null unique,
  created_at  timestamptz not null default now()
);

create table if not exists qsig_messages (
  id           text primary key,
  strategy_id  text not null,
  type         text not null,
  status       text not null,
  code         text not null default '',
  received_at  timestamptz not null,
  body         jsonb not null,
  body_hash    bytea not null,
  reply        jsonb not null
);
create index if not exists qsig_messages_recent on qsig_messages (received_at desc);
-- 前向记录（D96）：收到的原始字节、上一环、这一环、写入顺序。之前的消息这几列是空的，链从加列之后开始
alter table qsig_messages add column if not exists raw bytea;
alter table qsig_messages add column if not exists prev_hash text;
alter table qsig_messages add column if not exists chain_hash text;
alter table qsig_messages add column if not exists seq bigserial;
create index if not exists qsig_messages_chain on qsig_messages (strategy_id, seq) where chain_hash is not null;

create table if not exists qsig_executions (
  message_id  text not null,
  strategy_id text not null,
  report      jsonb not null,
  updated_at  timestamptz not null,
  primary key (message_id)
);

create table if not exists portfolio_state (
  portfolio  text primary key,
  state      jsonb not null,
  updated_at timestamptz not null
);

create table if not exists nav_points (
  portfolio text not null,
  t         timestamptz not null,
  nav       double precision not null,
  primary key (portfolio, t)
);
`

func (s *Store) Migrate(ctx context.Context) error {
	_, err := s.Pool.Exec(ctx, schema)
	return err
}

// ---------- 账本 ----------

// AccountFor 这个策略在引擎里的系统账户；第一次用时分配一个。
func (s *Store) AccountFor(ctx context.Context, strategyID string) (int64, error) {
	if _, err := s.Pool.Exec(ctx, `insert into strategy_accounts (strategy_id, account_id) values ($1, -nextval('strategy_account_seq')) on conflict (strategy_id) do nothing`, strategyID); err != nil {
		return 0, err
	}
	var id int64
	err := s.Pool.QueryRow(ctx, `select account_id from strategy_accounts where strategy_id = $1`, strategyID).Scan(&id)
	return id, err
}

type Message struct {
	ID         string
	StrategyID string
	Type       string
	Status     string
	Code       string
	ReceivedAt time.Time
	Body       []byte
	Hash       []byte
	Reply      []byte
	Prev       string
	Chain      string
}

func (s *Store) SaveMessage(m Message) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := s.Pool.Exec(ctx, `insert into qsig_messages (id, strategy_id, type, status, code, received_at, body, body_hash, reply, raw, prev_hash, chain_hash)
		values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) on conflict (id) do nothing`,
		m.ID, m.StrategyID, m.Type, m.Status, m.Code, m.ReceivedAt, m.Body, m.Hash, m.Reply, m.Body, m.Prev, m.Chain)
	return err
}

// SignalRow 一条被接受的信号和它的执行回报（验证报告用）。
type SignalRow struct {
	ID         string
	ReceivedAt time.Time
	Body       []byte
	Report     []byte // qsig_executions.report，没有时是 nil
}

// AcceptedSignals 一个策略从 since 起被接受的全部信号，按时间先后。
func (s *Store) AcceptedSignals(ctx context.Context, strategyID string, since time.Time) ([]SignalRow, error) {
	rows, err := s.Pool.Query(ctx, `select m.id, m.received_at, m.body, e.report from qsig_messages m
		left join qsig_executions e on e.message_id = m.id
		where m.strategy_id = $1 and m.type = 'signal' and m.status = 'accepted' and m.received_at >= $2 order by m.received_at`, strategyID, since)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (SignalRow, error) {
		var x SignalRow
		err := r.Scan(&x.ID, &x.ReceivedAt, &x.Body, &x.Report)
		return x, err
	})
}

// LastChains 每个策略链的最后一环（启动时接上）。
func (s *Store) LastChains(ctx context.Context) (map[string]string, error) {
	rows, err := s.Pool.Query(ctx, `select distinct on (strategy_id) strategy_id, chain_hash from qsig_messages where chain_hash <> '' order by strategy_id, seq desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, h string
		if err := rows.Scan(&id, &h); err != nil {
			return nil, err
		}
		out[id] = h
	}
	return out, rows.Err()
}

// ChainEntry 导出的一行。raw 是收到的原始字节（按 UTF-8 文本给出）。
type ChainEntry struct {
	Seq        int64  `json:"seq"`
	ID         string `json:"id"`
	StrategyID string `json:"strategy_id"`
	Type       string `json:"type"`
	Status     string `json:"status"`
	Code       string `json:"code,omitempty"`
	ReceivedMs int64  `json:"received_ms"`
	Raw        string `json:"raw"`
	Prev       string `json:"prev"`
	Chain      string `json:"chain"`
}

// ExportChain 一个策略的整条链，按写入顺序，一行一条（JSON Lines）。
func (s *Store) ExportChain(ctx context.Context, strategyID string, w io.Writer) (int, error) {
	rows, err := s.Pool.Query(ctx, `select seq, id, strategy_id, type, status, code, received_at, raw, prev_hash, chain_hash
		from qsig_messages where strategy_id = $1 and chain_hash <> '' order by seq`, strategyID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	n := 0
	for rows.Next() {
		var e ChainEntry
		var at time.Time
		var raw []byte
		if err := rows.Scan(&e.Seq, &e.ID, &e.StrategyID, &e.Type, &e.Status, &e.Code, &at, &raw, &e.Prev, &e.Chain); err != nil {
			return n, err
		}
		e.ReceivedMs, e.Raw = at.UnixMilli(), string(raw)
		if err := enc.Encode(e); err != nil {
			return n, err
		}
		n++
	}
	return n, rows.Err()
}

// LoadMessages 读最近 days 天的消息（幂等判断和前端的决策流用）。
func (s *Store) LoadMessages(ctx context.Context, days int) ([]Message, error) {
	rows, err := s.Pool.Query(ctx, `select id, strategy_id, type, status, code, received_at, body, body_hash, reply
		from qsig_messages where received_at > now() - make_interval(days => $1) order by received_at`, days)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Message, error) {
		var m Message
		err := r.Scan(&m.ID, &m.StrategyID, &m.Type, &m.Status, &m.Code, &m.ReceivedAt, &m.Body, &m.Hash, &m.Reply)
		return m, err
	})
}

func (s *Store) SaveExecutions(msgID, strategyID string, report any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	b, _ := json.Marshal(report)
	_, err := s.Pool.Exec(ctx, `insert into qsig_executions (message_id, strategy_id, report, updated_at) values ($1,$2,$3,now())
		on conflict (message_id) do update set report = excluded.report, updated_at = now()`, msgID, strategyID, b)
	return err
}

func (s *Store) LoadExecutions(ctx context.Context, days int) (map[string]json.RawMessage, error) {
	rows, err := s.Pool.Query(ctx, `select message_id, report from qsig_executions where updated_at > now() - make_interval(days => $1)`, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]json.RawMessage{}
	for rows.Next() {
		var id string
		var b []byte
		if err := rows.Scan(&id, &b); err != nil {
			return nil, err
		}
		out[id] = b
	}
	return out, rows.Err()
}

// ---------- 组合状态与净值 ----------

func (s *Store) SaveState(portfolio string, state any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	b, _ := json.Marshal(state)
	_, err := s.Pool.Exec(ctx, `insert into portfolio_state (portfolio, state, updated_at) values ($1,$2,now())
		on conflict (portfolio) do update set state = excluded.state, updated_at = now()`, portfolio, b)
	return err
}

func (s *Store) LoadStates(ctx context.Context) (map[string]json.RawMessage, error) {
	rows, err := s.Pool.Query(ctx, `select portfolio, state from portfolio_state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]json.RawMessage{}
	for rows.Next() {
		var id string
		var b []byte
		if err := rows.Scan(&id, &b); err != nil {
			return nil, err
		}
		out[id] = b
	}
	return out, rows.Err()
}

func (s *Store) AppendNAV(portfolio string, t int64, nav float64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := s.Pool.Exec(ctx, `insert into nav_points (portfolio, t, nav) values ($1,$2,$3) on conflict do nothing`, portfolio, time.UnixMilli(t), nav)
	return err
}

func (s *Store) LoadNAV(ctx context.Context, portfolio string, days int) ([][2]float64, error) {
	rows, err := s.Pool.Query(ctx, `select t, nav from nav_points where portfolio = $1 and t > now() - make_interval(days => $2) order by t`, portfolio, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][2]float64
	for rows.Next() {
		var t time.Time
		var nav float64
		if err := rows.Scan(&t, &nav); err != nil {
			return nil, err
		}
		out = append(out, [2]float64{float64(t.UnixMilli()), nav})
	}
	return out, rows.Err()
}
