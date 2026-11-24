package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"qs/internal/notify"
	"qs/internal/qsig"
	"qs/internal/trading"
)

// 18 第 8.4 条（D100 第 1、5 条）：
//   - 自己建的策略发了新的目标仓位：按通知设置提醒它的主人（只提醒，不下单；别人的策略不推）
//   - 每日复盘：UTC 零点过后，把昨天每个有动静的账户的盈亏、手续费、成交推给主人

// strategyPush 把一条被接受的信号变成给策略主人的提醒。ownerOf 返回 0 = 不是任何人自己建的（配置里的、示例的），不推。
func strategyPush(n *notify.Service, ownerOf func(ctx context.Context, id string) int64) func(qsig.Record) {
	return func(r qsig.Record) {
		if r.Type != "signal" || r.Status != "accepted" {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		owner := ownerOf(ctx, r.StrategyID)
		if owner == 0 {
			return
		}
		if p := strategyParams(r.StrategyID, r.Raw); p != nil {
			n.Push(ctx, owner, notify.KStrat, p)
		}
	}
}

// strategyParams 从信号原文里取要转达的内容：每个目标的品种、权重、信心（最多列 5 个），和第一条理由。
func strategyParams(id string, raw []byte) map[string]string {
	var m struct {
		Targets []struct {
			Instrument string  `json:"instrument"`
			Weight     string  `json:"weight"`
			Confidence float64 `json:"confidence"`
			Rationale  struct {
				Summary map[string]string `json:"summary"`
			} `json:"rationale"`
		} `json:"targets"`
	}
	if json.Unmarshal(raw, &m) != nil || len(m.Targets) == 0 {
		return nil
	}
	clip := func(s string) string {
		r := []rune(strings.TrimSpace(s))
		if len(r) > 120 {
			return string(r[:119]) + "…"
		}
		return string(r)
	}
	var list []string
	for i, t := range m.Targets {
		if i == 5 {
			break
		}
		list = append(list, t.Instrument+":"+t.Weight+":"+strconv.FormatFloat(t.Confidence, 'f', -1, 64))
	}
	p := map[string]string{"strategy": id, "targets": strings.Join(list, ",")}
	if extra := len(m.Targets) - len(list); extra > 0 {
		p["more"] = strconv.Itoa(extra)
	}
	if s := m.Targets[0].Rationale.Summary; s != nil {
		if v := clip(s["zh"]); v != "" {
			p["reason"] = v
		}
		if v := clip(s["en"]); v != "" {
			p["reason_en"] = v
		}
	}
	return p
}

// dayRecap 一个账户在 [from, to) 这段时间的流水。一张委托的平仓算一笔交易，赚亏按"已实现 − 这张委托的手续费"算。
// 没有成交也没有资金费时返回 nil（不发）。
func dayRecap(fills []trading.Fill, funds []trading.FundingRec, from, to int64) map[string]string {
	var realized, fees, funding decimal.Decimal
	nFills := 0
	type tr struct{ net decimal.Decimal }
	closed := map[string]*tr{}
	feeBy := map[string]decimal.Decimal{}
	for _, f := range fills {
		if f.Time < from || f.Time >= to {
			continue
		}
		nFills++
		realized = realized.Add(f.Realized)
		fees = fees.Add(f.Fee)
		feeBy[f.OrderID] = feeBy[f.OrderID].Add(f.Fee)
		if !f.Realized.IsZero() {
			if closed[f.OrderID] == nil {
				closed[f.OrderID] = &tr{}
			}
			closed[f.OrderID].net = closed[f.OrderID].net.Add(f.Realized)
		}
	}
	for _, x := range funds {
		if x.Time >= from && x.Time < to {
			funding = funding.Add(x.Amount)
		}
	}
	if nFills == 0 && funding.IsZero() {
		return nil
	}
	wins, losses := 0, 0
	for id, t := range closed {
		if t.net.Sub(feeBy[id]).Sign() > 0 {
			wins++
		} else {
			losses++
		}
	}
	return map[string]string{
		"closed": strconv.Itoa(len(closed)), "wins": strconv.Itoa(wins), "losses": strconv.Itoa(losses),
		"realized": realized.StringFixed(2), "fees": fees.StringFixed(2), "funding": funding.StringFixed(2),
		"net": realized.Sub(fees).Add(funding).StringFixed(2), "fills": strconv.Itoa(nFills),
	}
}

type recapAcct struct {
	User int64
	ID   int64
	Name string
}

// recapOnce 看现在该不该发昨天的复盘：UTC 零点过 5 分以后、而且昨天的还没发过，就发。发没发过记在库里（重启不重发，也不漏）。
// 返回发给了几个账户；-1 = 这次不用发（时间没到，或者发过了）。
func recapOnce(ctx context.Context, pool *pgxpool.Pool, n *notify.Service, accounts func(context.Context) []recapAcct,
	records func(acct int64) ([]trading.Fill, []trading.FundingRec, error), now time.Time) int {
	now = now.UTC()
	if now.Hour() == 0 && now.Minute() < 5 { // 等几分钟：跨零点的成交先落库
		return -1
	}
	end := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	day := end.AddDate(0, 0, -1)
	tag, err := pool.Exec(ctx, `insert into daily_recaps (day) values ($1) on conflict do nothing`, day.Format("2006-01-02"))
	if err != nil || tag.RowsAffected() == 0 {
		return -1
	}
	sent := 0
	for _, a := range accounts(ctx) {
		fills, funds, err := records(a.ID)
		if err != nil {
			continue
		}
		p := dayRecap(fills, funds, day.UnixMilli(), end.UnixMilli())
		if p == nil {
			continue
		}
		p["acct"], p["day"] = a.Name, day.Format("2006-01-02")
		n.Push(ctx, a.User, notify.KRecap, p)
		sent++
	}
	return sent
}

const recapSchema = `create table if not exists daily_recaps (day date primary key, sent_at timestamptz not null default now())`

// runRecaps 每分钟看一眼该不该发每日复盘。
func runRecaps(ctx context.Context, pool *pgxpool.Pool, n *notify.Service, accounts func(context.Context) []recapAcct,
	records func(acct int64) ([]trading.Fill, []trading.FundingRec, error), log *slog.Logger) {
	if _, err := pool.Exec(ctx, recapSchema); err != nil {
		log.Error("每日复盘：建表", "err", err)
		return
	}
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			if sent := recapOnce(ctx, pool, n, accounts, records, now); sent >= 0 {
				log.Info("每日复盘已发", "accounts", sent)
			}
		}
	}
}
