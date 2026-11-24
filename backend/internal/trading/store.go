package trading

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store 把事件存进数据库（只追加）。重启时整本重放即可恢复全部状态（验收 2.7、2.12）。
type Store struct{ Pool *pgxpool.Pool }

const storeSchema = `
create table if not exists trading_events (
  seq     bigint primary key,
  account bigint not null,
  t       bigint not null,
  kind    text   not null,
  body    jsonb  not null
);
create index if not exists trading_events_account on trading_events (account, seq);
`

func (s *Store) Migrate(ctx context.Context) error {
	_, err := s.Pool.Exec(ctx, storeSchema)
	return err
}

// Append 按顺序写入；重试时已写过的跳过（按 seq 去重）。
func (s *Store) Append(ctx context.Context, evs []Event) error {
	b := &pgx.Batch{}
	for _, ev := range evs {
		body, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		b.Queue(`insert into trading_events (seq, account, t, kind, body) values ($1,$2,$3,$4,$5) on conflict (seq) do nothing`, ev.Seq, ev.Account, ev.Time, ev.Kind, body)
	}
	return s.Pool.SendBatch(ctx, b).Close()
}

func (s *Store) Load(ctx context.Context) ([]Event, error) {
	rows, err := s.Pool.Query(ctx, `select body from trading_events order by seq`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Event, error) {
		var raw []byte
		var ev Event
		if err := r.Scan(&raw); err != nil {
			return ev, err
		}
		err := json.Unmarshal(raw, &ev)
		return ev, err
	})
}
