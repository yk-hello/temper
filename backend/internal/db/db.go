// Package db 管理数据库连接。阶段 0 只做连通检查，表结构从阶段 3 开始加。
package db

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type DB struct {
	Pool   *pgxpool.Pool // 连上之后才不为空；用 Ready() 等待
	status atomic.Value  // string
	ready  chan struct{}
}

// Ready 连上数据库时关闭。没有配置数据库时永远不会关闭。
func (d *DB) Ready() <-chan struct{} { return d.ready }

// Connect 在后台不断重试连接，直到成功或 ctx 取消。不会阻塞启动：
// docker compose 里数据库可能比后端晚几秒就绪。
func Connect(ctx context.Context, url string, log *slog.Logger) *DB {
	d := &DB{ready: make(chan struct{})}
	if url == "" {
		d.status.Store("disabled")
		return d
	}
	d.status.Store("connecting")
	go func() {
		wait := time.Second
		for ctx.Err() == nil {
			pool, err := pgxpool.New(ctx, url)
			if err == nil {
				pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				err = pool.Ping(pingCtx)
				cancel()
				if err == nil {
					d.Pool = pool
					d.status.Store("ok")
					log.Info("database connected")
					close(d.ready)
					return
				}
				pool.Close()
			}
			d.status.Store("error: " + err.Error())
			log.Warn("database not ready, retrying", "err", err, "wait", wait)
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			if wait < 10*time.Second {
				wait *= 2
			}
		}
	}()
	return d
}

// Status 返回 "ok"、"disabled"、"connecting" 或 "error: ..."。
func (d *DB) Status() string {
	if v, ok := d.status.Load().(string); ok {
		return v
	}
	return "unknown"
}

func (d *DB) Close() {
	if d.Pool != nil {
		d.Pool.Close()
	}
}
