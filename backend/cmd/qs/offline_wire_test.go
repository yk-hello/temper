package main

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"qs/internal/notify"
	"qs/internal/qsig"
)

// 策略掉线提醒（D117）
func TestOfflineWatch(t *testing.T) {
	ctx := context.Background()
	const min = int64(60_000)
	n := notify.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	hb := map[string]int64{}
	var follows []int64
	w := &offlineWatch{N: n,
		Status: func() []qsig.StrategyStatus {
			out := []qsig.StrategyStatus{{StrategyID: qsig.DemoStrategyID, Sample: true, LastHeartbeat: 1}, {StrategyID: "never"}}
			for id, v := range hb {
				out = append(out, qsig.StrategyStatus{StrategyID: id, LastHeartbeat: v})
			}
			return out
		},
		Owner: func(_ context.Context, id string) int64 {
			if id == "mine" {
				return 1
			}
			return 0
		},
		Followers: func(id string) []int64 { return follows },
	}
	got := func(user int64) []map[string]string {
		items, _, _ := n.List(ctx, user, 50)
		var out []map[string]string
		for i := len(items) - 1; i >= 0; i-- {
			if items[i].Kind == notify.KOff {
				out = append(out, items[i].Params)
			}
		}
		return out
	}

	// 平台刚启动：最后一次心跳是 10 分钟前（平台停着的时候），先等 3 分钟
	t0 := 1_000 * min
	hb["mine"] = t0 - 10*min
	follows = []int64{1, 2, 3}
	w.tick(ctx, t0)
	w.tick(ctx, t0+2*min)
	if len(got(1)) != 0 {
		t.Fatal("平台刚启动的 3 分钟里不提醒")
	}
	// 启动以后 3 分钟还没收到：提醒主人一条（他自己也在跟，只发主人的那条）和跟着的人各一条；示例策略、从没发过心跳的不提醒
	w.tick(ctx, t0+3*min+1)
	if g := got(1); len(g) != 1 || g[0]["role"] != "own" || g[0]["state"] != "off" || g[0]["mins"] != "13" || g[0]["strategy"] != "mine" {
		t.Fatalf("主人：%v", g)
	}
	if g := got(2); len(g) != 1 || g[0]["role"] != "follow" {
		t.Fatalf("跟着的人：%v", g)
	}
	// 一直没恢复：不再提醒
	w.tick(ctx, t0+30*min)
	if len(got(1)) != 1 {
		t.Fatal("同一次掉线只提醒一次")
	}
	// 平台重启（换一个 watcher，读回"提醒过了"）：还是不重发
	w2 := &offlineWatch{N: n, Status: w.Status, Owner: w.Owner, Followers: w.Followers, alerted: map[string]int64{"mine": hb["mine"]}}
	w2.tick(ctx, t0+40*min)
	w2.tick(ctx, t0+45*min)
	if len(got(1)) != 1 {
		t.Fatal("平台重启以后不重发")
	}
	// 恢复：主人和跟着的人各一条，写明中间离线了多久；3 号不跟了，不发
	follows = []int64{2}
	hb["mine"] = t0 + 50*min
	w2.tick(ctx, t0+50*min+10_000)
	if g := got(1); len(g) != 2 || g[1]["state"] != "on" || g[1]["mins"] != "60" {
		t.Fatalf("恢复：%v", g)
	}
	if len(got(2)) != 2 || len(got(3)) != 1 {
		t.Fatalf("恢复只发给现在还在跟的人：%v %v", got(2), got(3))
	}
	if len(w2.alerted) != 0 {
		t.Fatal("恢复以后清掉")
	}

	// 正常跑着：心跳 2 分钟前，不提醒
	hb["mine"] = t0 + 60*min
	w2.tick(ctx, t0+62*min)
	if len(got(1)) != 2 {
		t.Fatal("3 分钟以内不算离线")
	}
	// 又掉线：再提醒一次（这是新的一次掉线）
	w2.tick(ctx, t0+63*min+15_000)
	if g := got(1); len(g) != 3 || g[2]["state"] != "off" || g[2]["mins"] != "3" {
		t.Fatalf("第二次掉线：%v", got(1))
	}
	// 策略删掉了：清掉记录，不发"恢复"
	delete(hb, "mine")
	w2.tick(ctx, t0+70*min)
	if len(w2.alerted) != 0 || len(got(1)) != 3 {
		t.Fatal("删掉的策略不再管")
	}
}
