package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"qs/internal/notify"

	"github.com/shopspring/decimal"

	"qs/internal/trading"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// 18 第 8.4 条：每日复盘的算术。只算这一天里的；一张委托的平仓算一笔，赚亏扣掉它自己的手续费
func TestDayRecap(t *testing.T) {
	const day = int64(86_400_000)
	fills := []trading.Fill{
		{Time: 10, OrderID: "old", Realized: d("100"), Fee: d("1")},        // 前一天：不算
		{Time: day + 1, OrderID: "o1", Fee: d("0.5")},                      // 开仓
		{Time: day + 2, OrderID: "o2", Realized: d("6"), Fee: d("0.3")},    // 平仓，分两笔成交
		{Time: day + 3, OrderID: "o2", Realized: d("4"), Fee: d("0.2")},    //   合起来赚 10 − 0.5
		{Time: day + 4, OrderID: "o3", Realized: d("0.1"), Fee: d("0.4")},  // 毛利 0.1，扣手续费是亏的
		{Time: day + 5, OrderID: "o4", Realized: d("-3"), Fee: d("0.1")},   // 亏
		{Time: 2*day + 1, OrderID: "next", Realized: d("50"), Fee: d("1")}, // 后一天：不算
	}
	funds := []trading.FundingRec{{Time: day + 9, Amount: d("-0.25")}, {Time: 5, Amount: d("9")}}
	p := dayRecap(fills, funds, day, 2*day)
	want := map[string]string{"closed": "3", "wins": "1", "losses": "2", "realized": "7.10", "fees": "1.50", "funding": "-0.25", "net": "5.35", "fills": "5"}
	for k, v := range want {
		if p[k] != v {
			t.Errorf("%s = %s，应该是 %s", k, p[k], v)
		}
	}
	if dayRecap(fills, funds, 5*day, 6*day) != nil {
		t.Error("没有动静的一天不发")
	}
	if p := dayRecap(nil, []trading.FundingRec{{Time: day + 1, Amount: d("1.5")}}, day, 2*day); p == nil || p["fills"] != "0" || p["net"] != "1.50" {
		t.Errorf("只有资金费也发：%v", p)
	}
}

func TestStrategyParams(t *testing.T) {
	raw := []byte(`{"targets":[
	 {"instrument":"BTC-PERP","weight":"0.3","confidence":0.7,"rationale":{"summary":{"zh":"均线金叉","en":"MA cross"}}},
	 {"instrument":"ETH-PERP","weight":"-0.1","confidence":0.55,"rationale":{"summary":{"zh":"x"}}},
	 {"instrument":"A-PERP","weight":"0"},{"instrument":"B-PERP","weight":"0"},{"instrument":"C-PERP","weight":"0"},{"instrument":"D-PERP","weight":"0"}]}`)
	p := strategyParams("mine", raw)
	if p["strategy"] != "mine" || p["targets"] != "BTC-PERP:0.3:0.7,ETH-PERP:-0.1:0.55,A-PERP:0:0,B-PERP:0:0,C-PERP:0:0" || p["more"] != "1" || p["reason"] != "均线金叉" || p["reason_en"] != "MA cross" {
		t.Fatalf("%v", p)
	}
	if strategyParams("mine", []byte(`{"targets":[]}`)) != nil || strategyParams("mine", []byte(`not json`)) != nil {
		t.Fatal("没有目标或坏的消息不推")
	}
}

// 每日复盘的发送：零点刚过不发；过了 5 分发一次；同一天不重发；没动静的账户不发。需要数据库（QS_TEST_DB）。
func TestRecapOnce(t *testing.T) {
	dsn := os.Getenv("QS_TEST_DB")
	if dsn == "" {
		t.Skip("没有设置 QS_TEST_DB，跳过数据库测试")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `drop table if exists daily_recaps`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, recapSchema); err != nil {
		t.Fatal(err)
	}
	n := notify.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	day := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	accts := func(context.Context) []recapAcct {
		return []recapAcct{{User: 1, ID: 10, Name: "Paper 1"}, {User: 1, ID: 11, Name: "Paper 2"}, {User: 2, ID: 20, Name: "Paper 1"}}
	}
	recs := func(acct int64) ([]trading.Fill, []trading.FundingRec, error) {
		switch acct {
		case 10:
			return []trading.Fill{{Time: day.UnixMilli() + 1000, OrderID: "a", Realized: d("5"), Fee: d("1")}}, nil, nil
		case 20:
			return []trading.Fill{{Time: day.UnixMilli() - 1000, OrderID: "b", Realized: d("5"), Fee: d("1")}}, nil, nil // 前天的
		}
		return nil, nil, nil
	}
	next := day.AddDate(0, 0, 1)
	if got := recapOnce(ctx, pool, n, accts, recs, next.Add(2*time.Minute)); got != -1 {
		t.Fatalf("零点刚过 2 分钟不发：%d", got)
	}
	if got := recapOnce(ctx, pool, n, accts, recs, next.Add(6*time.Minute)); got != 1 {
		t.Fatalf("应该只发给有成交的那一个账户：%d", got)
	}
	if got := recapOnce(ctx, pool, n, accts, recs, next.Add(7*time.Minute)); got != -1 {
		t.Fatalf("同一天不重发：%d", got)
	}
	items, _, _ := n.List(ctx, 1, 10)
	if len(items) != 1 || items[0].Kind != notify.KRecap || items[0].Params["acct"] != "Paper 1" || items[0].Params["day"] != "2026-11-11" || items[0].Params["net"] != "4.00" {
		t.Fatalf("1 号收到的：%+v", items)
	}
	if items, _, _ := n.List(ctx, 2, 10); len(items) != 0 {
		t.Fatalf("2 号昨天没动静，不该收到：%+v", items)
	}
}
