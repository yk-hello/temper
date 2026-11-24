package highlight

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound 没有这条，或者不是这个人的。
var ErrNotFound = errors.New("NOT_FOUND")

// 前一版（D88）有验真编号、邀请码、撤回和软删除，D90、D91 都去掉了：已经建好的表在这里改掉。
const schema = `
create table if not exists highlights (
  id       bigserial primary key,
  usr      bigint not null,
  account  bigint not null,
  trade_id text   not null,
  snap     jsonb  not null,
  created  bigint not null,
  unique (usr, account, trade_id)
);
alter table highlights drop column if exists token;
alter table highlights drop column if exists kind;
alter table highlights drop column if exists show_amount;
alter table highlights drop column if exists hidden;
do $$ begin
  if exists (select 1 from information_schema.columns where table_name = 'highlights' and column_name = 'deleted') then
    execute 'delete from highlights where deleted';
    execute 'alter table highlights drop column deleted';
  end if;
end $$;
create index if not exists highlights_u on highlights (usr, created desc);
drop table if exists invites;
drop table if exists invite_codes;
create table if not exists highlight_prefs (
  usr     bigint primary key,
  min_roe float8 not null
);
`

// Highlight 一条高光。
type Highlight struct {
	ID      int64 `json:"id"`
	Account int64 `json:"account"`
	Snap    Snap  `json:"snap"`
	Created int64 `json:"created"`
	User    int64 `json:"-"`
}

// Store 存在数据库里。
type Store struct{ pool *pgxpool.Pool }

// Attach 建表（旧表改成新结构）。
func (s *Store) Attach(ctx context.Context, p *pgxpool.Pool) error {
	if _, err := p.Exec(ctx, schema); err != nil {
		return err
	}
	s.pool = p
	return nil
}

// Ready 数据库接上了没有。
func (s *Store) Ready() bool { return s != nil && s.pool != nil }

// Add 记一条。同一笔交易只记一次（重复时返回 nil, nil）。
func (s *Store) Add(ctx context.Context, user, acct int64, sn Snap, now int64) (*Highlight, error) {
	js, err := json.Marshal(sn)
	if err != nil {
		return nil, err
	}
	h := &Highlight{Account: acct, Snap: sn, Created: now, User: user}
	err = s.pool.QueryRow(ctx, `insert into highlights (usr, account, trade_id, snap, created) values ($1,$2,$3,$4,$5)
		on conflict (usr, account, trade_id) do nothing returning id`, user, acct, sn.TradeID, js, now).Scan(&h.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return h, nil
}

func scan(row pgx.Row) (*Highlight, error) {
	var h Highlight
	var js []byte
	if err := row.Scan(&h.ID, &h.User, &h.Account, &js, &h.Created); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if err := json.Unmarshal(js, &h.Snap); err != nil {
		return nil, err
	}
	return &h, nil
}

const cols = `id, usr, account, snap, created`

// List 这个人的全部高光，新的在前。
func (s *Store) List(ctx context.Context, user int64) ([]Highlight, error) {
	rows, err := s.pool.Query(ctx, `select `+cols+` from highlights where usr=$1 order by created desc limit 1000`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Highlight{}
	for rows.Next() {
		h, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *h)
	}
	return out, rows.Err()
}

// Get 这个人的一条。
func (s *Store) Get(ctx context.Context, user, id int64) (*Highlight, error) {
	return scan(s.pool.QueryRow(ctx, `select `+cols+` from highlights where usr=$1 and id=$2`, user, id))
}

// Delete 删掉一条。
func (s *Store) Delete(ctx context.Context, user, id int64) error {
	t, err := s.pool.Exec(ctx, `delete from highlights where usr=$1 and id=$2`, user, id)
	if err != nil {
		return err
	}
	if t.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Min 这个人的门槛；没设过用默认值。
func (s *Store) Min(ctx context.Context, user int64) float64 {
	var v float64
	if err := s.pool.QueryRow(ctx, `select min_roe from highlight_prefs where usr=$1`, user).Scan(&v); err != nil {
		return DefaultMin
	}
	return ClampMin(v)
}

// SetMin 改门槛。
func (s *Store) SetMin(ctx context.Context, user int64, v float64) (float64, error) {
	v = ClampMin(v)
	_, err := s.pool.Exec(ctx, `insert into highlight_prefs (usr, min_roe) values ($1,$2) on conflict (usr) do update set min_roe=excluded.min_roe`, user, v)
	return v, err
}

// DeleteUser 删账户时一起删。
func (s *Store) DeleteUser(ctx context.Context, user int64) {
	if s.pool == nil {
		return
	}
	_, _ = s.pool.Exec(ctx, `delete from highlights where usr=$1`, user)
	_, _ = s.pool.Exec(ctx, `delete from highlight_prefs where usr=$1`, user)
}
