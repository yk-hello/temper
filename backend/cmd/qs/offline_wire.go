package main

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"qs/internal/notify"
	"qs/internal/qsig"
)

// 策略掉线提醒（D117）：最后一次心跳（发出的信号也算）超过 180 秒，和策略页显示"策略离线"是同一条线，
// 按通知设置提醒策略的主人和正在跟它的人；恢复时再提醒一次，写明中间离线了多久。
//   - 主人：自己建的策略是建的人；部署时配置里写的策略是部署的人（所有者）；示例策略不提醒
//   - 从没发过心跳的不提醒：还没开始跑，谈不上掉线
//   - 平台刚启动时先等 180 秒：平台停着的那段时间本来就收不到心跳，不算策略掉线
//   - 同一次掉线只提醒一次：提醒过的记在库里，平台重启以后不重发

const offlineSchema = `create table if not exists strategy_offline (strategy_id text primary key, last_hb bigint not null, alerted_at bigint not null)`

// offlineEvery 多久看一次。提醒最晚在判定离线后这么久发出。
const offlineEvery = 15 * time.Second

type offlineWatch struct {
	Pool      *pgxpool.Pool // 没有时只记在内存（测试）
	N         *notify.Service
	Status    func() []qsig.StrategyStatus
	Owner     func(ctx context.Context, id string) int64 // 0 = 没有主人
	Followers func(id string) []int64                    // 正在跟的人
	Log       *slog.Logger

	started int64            // 第一次检查的时间（毫秒）
	alerted map[string]int64 // 策略 → 发离线提醒时它的最后一次心跳（毫秒）；恢复后删掉
}

// load 建表，读回提醒过、还没恢复的。
func (w *offlineWatch) load(ctx context.Context) error {
	w.alerted = map[string]int64{}
	if w.Pool == nil {
		return nil
	}
	if _, err := w.Pool.Exec(ctx, offlineSchema); err != nil {
		return err
	}
	rows, err := w.Pool.Query(ctx, `select strategy_id, last_hb from strategy_offline`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var hb int64
		if rows.Scan(&id, &hb) == nil {
			w.alerted[id] = hb
		}
	}
	return rows.Err()
}

// tick 看一遍所有策略。now 是毫秒。
func (w *offlineWatch) tick(ctx context.Context, now int64) {
	if w.alerted == nil {
		w.alerted = map[string]int64{}
	}
	if w.started == 0 {
		w.started = now
	}
	limit := qsig.OfflineAfter.Milliseconds()
	seen := map[string]bool{}
	for _, st := range w.Status() {
		id, hb := st.StrategyID, st.LastHeartbeat
		seen[id] = true
		if st.Sample || hb == 0 {
			continue
		}
		prev, was := w.alerted[id]
		switch {
		case now-hb > limit && now-max(hb, w.started) > limit && (!was || prev != hb):
			w.send(ctx, id, "off", now-hb)
			w.mark(ctx, id, hb, now)
		case now-hb <= limit && was && hb > prev:
			w.send(ctx, id, "on", hb-prev)
			w.clear(ctx, id)
		}
	}
	for id := range w.alerted {
		if !seen[id] { // 策略删掉了（密钥都吊销了）：不再提醒，也不发"恢复"
			w.clear(ctx, id)
		}
	}
}

// send 发给主人和正在跟的人。主人自己也在跟的话只发一条（按主人的写法）。
func (w *offlineWatch) send(ctx context.Context, id, state string, gap int64) {
	mins := strconv.FormatInt(gap/60_000, 10)
	push := func(user int64, role string) {
		w.N.Push(ctx, user, notify.KOff, map[string]string{"strategy": id, "state": state, "role": role, "mins": mins})
	}
	owner := w.Owner(ctx, id)
	if owner != 0 {
		push(owner, "own")
	}
	n := 0
	for _, u := range w.Followers(id) {
		if u != owner {
			push(u, "follow")
			n++
		}
	}
	if w.Log != nil {
		w.Log.Info("策略掉线提醒", "strategy", id, "state", state, "minutes", mins, "owner", owner != 0, "followers", n)
	}
}

func (w *offlineWatch) mark(ctx context.Context, id string, hb, now int64) {
	w.alerted[id] = hb
	if w.Pool != nil {
		if _, err := w.Pool.Exec(ctx, `insert into strategy_offline (strategy_id, last_hb, alerted_at) values ($1, $2, $3)
			on conflict (strategy_id) do update set last_hb = excluded.last_hb, alerted_at = excluded.alerted_at`, id, hb, now); err != nil && w.Log != nil {
			w.Log.Warn("策略掉线提醒：写库", "err", err)
		}
	}
}

func (w *offlineWatch) clear(ctx context.Context, id string) {
	delete(w.alerted, id)
	if w.Pool != nil {
		_, _ = w.Pool.Exec(ctx, `delete from strategy_offline where strategy_id = $1`, id)
	}
}

// run 每 15 秒看一次。ready 之前（信号记录还没从库里读回来，最后一次心跳不准）不看。
func (w *offlineWatch) run(ctx context.Context, ready func() bool) {
	if err := w.load(ctx); err != nil {
		if w.Log != nil {
			w.Log.Error("策略掉线提醒：建表", "err", err)
		}
		return
	}
	t := time.NewTicker(offlineEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			if ready() {
				w.tick(ctx, now.UnixMilli())
			}
		}
	}
}
