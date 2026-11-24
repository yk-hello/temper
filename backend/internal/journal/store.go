package journal

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 截图上限（D81）：单张 5 MB，每人 500 MB，长期保留，只有本人能看。
// 部署的人可以改：QS_LIMIT_SHOT_MB、QS_LIMIT_SHOTS_TOTAL_MB（D103）
var (
	MaxShot            = 5 << 20
	MaxShotsUser int64 = 500 << 20
)

// 存不下时的错误。
var (
	ErrNotFound  = errors.New("NOT_FOUND")
	ErrShotQuota = errors.New("SHOT_QUOTA")
)

const schema = `
create table if not exists journal_notes (
  usr      bigint not null,
  account  bigint not null,
  trade_id text   not null,
  tags     text[] not null default '{}',
  note     text   not null default '',
  emotion  text   not null default '',
  playbook bigint not null default 0,
  updated  bigint not null,
  primary key (usr, account, trade_id)
);
create table if not exists journal_shots (
  id       bigserial primary key,
  usr      bigint not null,
  account  bigint not null,
  trade_id text   not null,
  mime     text   not null,
  size     int    not null,
  data     bytea  not null,
  created  bigint not null
);
create index if not exists journal_shots_i on journal_shots (usr, account, trade_id);
create table if not exists journal_playbooks (
  id      bigserial primary key,
  usr     bigint not null,
  name    text   not null,
  entry   text   not null default '',
  stop    text   not null default '',
  target  text   not null default '',
  deleted boolean not null default false,
  created bigint not null,
  updated bigint not null
);
create index if not exists journal_playbooks_i on journal_playbooks (usr);
create table if not exists journal_order_playbook (
  usr      bigint not null,
  account  bigint not null,
  order_id text   not null,
  playbook bigint not null,
  primary key (usr, account, order_id)
);
`

// Note 一笔交易的标签、笔记、情绪、预案。
type Note struct {
	Tags     []string `json:"tags"`
	Note     string   `json:"note"`
	Emotion  string   `json:"emotion"`
	Playbook int64    `json:"playbook"`
	Updated  int64    `json:"updated"`
}

// ShotMeta 一张截图的信息（不含内容）。
type ShotMeta struct {
	ID      int64  `json:"id"`
	Mime    string `json:"mime"`
	Size    int    `json:"size"`
	Created int64  `json:"created"`
}

// Playbook 预案。
type Playbook struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Entry   string `json:"entry"`
	Stop    string `json:"stop"`
	Target  string `json:"target"`
	Created int64  `json:"created"`
	Updated int64  `json:"updated"`
}

// Store 存标签、笔记、截图、预案。所有查询都带用户编号，别人的看不到。
type Store struct{ Pool *pgxpool.Pool }

// Migrate 建表。
func (s *Store) Migrate(ctx context.Context) error {
	_, err := s.Pool.Exec(ctx, schema)
	return err
}

func now() int64 { return time.Now().UnixMilli() }

// SaveNote 存笔记。
func (s *Store) SaveNote(ctx context.Context, user, acct int64, id string, n Note) error {
	if n.Tags == nil {
		n.Tags = []string{}
	}
	_, err := s.Pool.Exec(ctx, `insert into journal_notes (usr,account,trade_id,tags,note,emotion,playbook,updated) values ($1,$2,$3,$4,$5,$6,$7,$8)
		on conflict (usr,account,trade_id) do update set tags=excluded.tags, note=excluded.note, emotion=excluded.emotion, playbook=excluded.playbook, updated=excluded.updated`,
		user, acct, id, n.Tags, n.Note, n.Emotion, n.Playbook, now())
	return err
}

// Notes 这个账户的全部笔记。
func (s *Store) Notes(ctx context.Context, user, acct int64) (map[string]*Note, error) {
	rows, err := s.Pool.Query(ctx, `select trade_id,tags,note,emotion,playbook,updated from journal_notes where usr=$1 and account=$2`, user, acct)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*Note{}
	for rows.Next() {
		var id string
		n := &Note{}
		if err := rows.Scan(&id, &n.Tags, &n.Note, &n.Emotion, &n.Playbook, &n.Updated); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// AddShot 存一张截图。超过每人上限返回 ErrShotQuota。
func (s *Store) AddShot(ctx context.Context, user, acct int64, id, mime string, data []byte) (*ShotMeta, error) {
	var used int64
	if err := s.Pool.QueryRow(ctx, `select coalesce(sum(size),0) from journal_shots where usr=$1`, user).Scan(&used); err != nil {
		return nil, err
	}
	if used+int64(len(data)) > MaxShotsUser {
		return nil, ErrShotQuota
	}
	m := &ShotMeta{Mime: mime, Size: len(data), Created: now()}
	err := s.Pool.QueryRow(ctx, `insert into journal_shots (usr,account,trade_id,mime,size,data,created) values ($1,$2,$3,$4,$5,$6,$7) returning id`,
		user, acct, id, mime, len(data), data, m.Created).Scan(&m.ID)
	return m, err
}

// Shots 这个账户每笔交易的截图信息。
func (s *Store) Shots(ctx context.Context, user, acct int64) (map[string][]ShotMeta, error) {
	rows, err := s.Pool.Query(ctx, `select trade_id,id,mime,size,created from journal_shots where usr=$1 and account=$2 order by id`, user, acct)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]ShotMeta{}
	for rows.Next() {
		var id string
		var m ShotMeta
		if err := rows.Scan(&id, &m.ID, &m.Mime, &m.Size, &m.Created); err != nil {
			return nil, err
		}
		out[id] = append(out[id], m)
	}
	return out, rows.Err()
}

// Shot 取一张截图：只有本人能取。
func (s *Store) Shot(ctx context.Context, user, id int64) (mime string, data []byte, err error) {
	err = s.Pool.QueryRow(ctx, `select mime,data from journal_shots where id=$1 and usr=$2`, id, user).Scan(&mime, &data)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, ErrNotFound
	}
	return
}

// DeleteShot 删一张截图。
func (s *Store) DeleteShot(ctx context.Context, user, id int64) error {
	t, err := s.Pool.Exec(ctx, `delete from journal_shots where id=$1 and usr=$2`, id, user)
	if err == nil && t.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// ShotUsage 已用的截图空间。
func (s *Store) ShotUsage(ctx context.Context, user int64) (int64, error) {
	var used int64
	err := s.Pool.QueryRow(ctx, `select coalesce(sum(size),0) from journal_shots where usr=$1`, user).Scan(&used)
	return used, err
}

// Playbooks 这个人的预案（不含删掉的）。
func (s *Store) Playbooks(ctx context.Context, user int64) ([]Playbook, error) {
	rows, err := s.Pool.Query(ctx, `select id,name,entry,stop,target,created,updated from journal_playbooks where usr=$1 and not deleted order by id`, user)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Playbook, error) {
		var p Playbook
		err := r.Scan(&p.ID, &p.Name, &p.Entry, &p.Stop, &p.Target, &p.Created, &p.Updated)
		return p, err
	})
}

// SavePlaybook 新建（ID = 0）或修改。
func (s *Store) SavePlaybook(ctx context.Context, user int64, p Playbook) (*Playbook, error) {
	t := now()
	p.Updated = t
	if p.ID == 0 {
		p.Created = t
		err := s.Pool.QueryRow(ctx, `insert into journal_playbooks (usr,name,entry,stop,target,created,updated) values ($1,$2,$3,$4,$5,$6,$7) returning id`,
			user, p.Name, p.Entry, p.Stop, p.Target, t, t).Scan(&p.ID)
		return &p, err
	}
	r, err := s.Pool.Exec(ctx, `update journal_playbooks set name=$3, entry=$4, stop=$5, target=$6, updated=$7 where id=$1 and usr=$2 and not deleted`, p.ID, user, p.Name, p.Entry, p.Stop, p.Target, t)
	if err == nil && r.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return &p, err
}

// DeletePlaybook 删预案。已经用过它的交易统计里还写着它的名字，所以只做标记。
func (s *Store) DeletePlaybook(ctx context.Context, user, id int64) error {
	r, err := s.Pool.Exec(ctx, `update journal_playbooks set deleted=true where id=$1 and usr=$2`, id, user)
	if err == nil && r.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// AllPlaybookNames 编号 → 名字，包括删掉的。
func (s *Store) AllPlaybookNames(ctx context.Context, user int64) (map[int64]string, error) {
	rows, err := s.Pool.Query(ctx, `select id,name from journal_playbooks where usr=$1`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var n string
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// SetOrderPlaybook 下单时选的预案。
func (s *Store) SetOrderPlaybook(ctx context.Context, user, acct int64, order string, pb int64) error {
	_, err := s.Pool.Exec(ctx, `insert into journal_order_playbook (usr,account,order_id,playbook) values ($1,$2,$3,$4) on conflict (usr,account,order_id) do update set playbook=excluded.playbook`, user, acct, order, pb)
	return err
}

// OrderPlaybooks 委托 → 预案。
func (s *Store) OrderPlaybooks(ctx context.Context, user, acct int64) (map[string]int64, error) {
	rows, err := s.Pool.Query(ctx, `select order_id,playbook from journal_order_playbook where usr=$1 and account=$2`, user, acct)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var o string
		var p int64
		if err := rows.Scan(&o, &p); err != nil {
			return nil, err
		}
		out[o] = p
	}
	return out, rows.Err()
}
