package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"qs/internal/auth"
	"qs/internal/backtest"
	"qs/internal/portfolio"
	"qs/internal/store"
	"qs/internal/verify"
)

// verifyRoutes 策略的验证报告（D96 第 2 条，18 第 5.5 条）：GET /api/strategies/{id}/report。
// 价格用 1 小时收盘价（币安一次最多 1500 根，约 62 天；更早的部分对照组算不了，报告里写明）。
// 同一个策略 60 秒内重复打开用缓存，不反复去币安取数。
func verifyRoutes(mux *http.ServeMux, users *auth.Service, getStore func() *store.Store, lookupPF func(string) *portfolio.Portfolio,
	closes func(ctx context.Context, symbol string, n int) ([]float64, error), log *slog.Logger) {
	type cached struct {
		at  time.Time
		out []byte
	}
	var mu sync.Mutex
	cache := map[string]cached{}
	mux.HandleFunc("GET /api/strategies/{id}/report", users.Require(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		id := r.PathValue("id")
		pf := lookupPF(id)
		st := getStore()
		if pf == nil {
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"code":"NOT_FOUND"}`))
			return
		}
		if st == nil {
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"code":"STARTING"}`))
			return
		}
		mu.Lock()
		if c, ok := cache[id]; ok && time.Since(c.at) < time.Minute {
			mu.Unlock()
			_, _ = w.Write(c.out)
			return
		}
		mu.Unlock()
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		perf := pf.Performance()
		now := time.Now().UnixMilli()
		since := perf.Since
		if since == 0 {
			since = now
		}
		rows, err := st.AcceptedSignals(ctx, id, time.UnixMilli(since))
		if err != nil {
			log.Error("验证报告：读信号", "strategy", id, "err", err)
			w.WriteHeader(500)
			_, _ = w.Write([]byte(`{"code":"INTERNAL_ERROR"}`))
			return
		}
		sigs := make([]verify.Signal, 0, len(rows))
		for _, x := range rows {
			s, ok := verify.ParseSignal(x.ID, x.ReceivedAt.UnixMilli(), x.Body)
			if !ok {
				continue
			}
			var ex []portfolio.Execution
			if len(x.Report) > 0 && json.Unmarshal(x.Report, &ex) == nil {
				for _, e := range ex {
					if e.End > s.Done {
						s.Done = e.End
					}
				}
			}
			sigs = append(sigs, s)
		}
		const hour = int64(3600_000)
		n := int((now-since)/hour) + 26 // 动量要往前看 24 小时
		capped := n > 1500
		n = min(max(n, 48), 1500)
		prices := map[string]verify.Prices{}
		for _, inst := range verify.Instruments(perf, sigs) {
			cl, err := closes(ctx, strings.TrimSuffix(inst, "-PERP")+"USDT", n)
			if err != nil || len(cl) == 0 {
				continue
			}
			prices[inst] = verify.Prices{T0: now/hour*hour - int64(len(cl)-1)*hour, Closes: cl}
		}
		rep := verify.Build(verify.Input{Perf: perf, Signals: sigs, Prices: prices, Now: now, FeeRate: 0.0005})
		if capped {
			rep.Notes = append(rep.Notes, "PRICES_CAPPED")
		}
		out, _ := json.Marshal(rep)
		mu.Lock()
		cache[id] = cached{at: time.Now(), out: out}
		mu.Unlock()
		_, _ = w.Write(out)
	}))
	// 回测一致性（18 第 5.5 条）：策略收到的目标仓位导出成回测的信号表（每行"时间,{instrument, weight}"），
	// 回测页或验证报告拿去回测，和前向的结果对照。和前向记录一样，登录就能看（策略的公开验证材料）。
	mux.HandleFunc("GET /api/strategies/{id}/signal-table", users.Require(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		st := getStore()
		pf := lookupPF(id)
		if pf == nil || st == nil {
			w.WriteHeader(404)
			return
		}
		since := r.URL.Query().Get("since")
		from := pf.Performance().Since
		if v, err := strconv.ParseInt(since, 10, 64); err == nil && v > from {
			from = v
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		rows, err := st.AcceptedSignals(ctx, id, time.UnixMilli(from))
		if err != nil {
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+id+`-signals.csv"`)
		w.Header().Set("Cache-Control", "no-store")
		var sb strings.Builder
		sb.WriteString("# " + id + "：策略收到的目标仓位（qsig/1），时间是平台收到的时刻（UTC）。直接贴进回测页\n")
		n := 0
		for _, x := range rows {
			s, ok := verify.ParseSignal(x.ID, x.ReceivedAt.UnixMilli(), x.Body)
			if !ok {
				continue
			}
			for _, t := range s.Targets {
				if n >= backtest.MaxSignals {
					break
				}
				j, _ := json.Marshal(map[string]any{"instrument": t.Inst, "weight": t.Weight})
				sb.WriteString(time.UnixMilli(s.At).UTC().Format("2006-01-02T15:04:05Z") + "," + string(j) + "\n")
				n++
			}
		}
		_, _ = w.Write([]byte(sb.String()))
	}))
}
