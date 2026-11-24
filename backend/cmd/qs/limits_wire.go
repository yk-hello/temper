package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"

	"qs/internal/auth"
	"qs/internal/backtest"
	"qs/internal/binapi"
	"qs/internal/journal"
	"qs/internal/notify"
	"qs/internal/strategy"
)

// 部署的人可以改的上限（D103：用户说"顺便检查一下，还有没有不够开源的问题"）。
// 这些原来写死在代码里，开源出去后别人自己部署也改不了；现在都能用环境变量改，不填就是原来的默认值。
// 交易所自己的规则（委托数量上限、价格带、分档……）不在这里：那是"和币安一样"的一部分，不该让人改。
type limitDef struct {
	env  string
	key  string // /api/limits 里的名字
	get  func() int64
	set  func(int64)
	unit int64 // 环境变量里的单位换算成内部单位（截图按 MB 填）
}

var limitDefs = []limitDef{
	{"QS_LIMIT_PAPER_ACCOUNTS", "paper_accounts", func() int64 { return int64(auth.MaxPaperAccounts) }, func(v int64) { auth.MaxPaperAccounts = int(v) }, 1},
	{"QS_LIMIT_SIGNUP_PER_IP_HOUR", "signup_per_ip_hour", func() int64 { return int64(auth.SignupPerIPHour) }, func(v int64) { auth.SignupPerIPHour = int(v) }, 1},
	{"QS_LIMIT_BACKTEST_DAYS", "backtest_days", func() int64 { return backtest.MaxDays }, func(v int64) { backtest.MaxDays = v }, 1},
	{"QS_LIMIT_BACKTEST_INSTRUMENTS", "backtest_instruments", func() int64 { return int64(backtest.MaxInstruments) }, func(v int64) { backtest.MaxInstruments = int(v) }, 1},
	{"QS_LIMIT_BACKTEST_SIGNALS", "backtest_signals", func() int64 { return int64(backtest.MaxSignals) }, func(v int64) { backtest.MaxSignals = int(v) }, 1},
	{"QS_LIMIT_API_KEYS", "api_keys", func() int64 { return int64(binapi.MaxPerUser) }, func(v int64) { binapi.MaxPerUser = int(v) }, 1},
	{"QS_LIMIT_STRATEGIES", "strategies", func() int64 { return int64(strategy.MaxPerUser) }, func(v int64) { strategy.MaxPerUser = int(v) }, 1},
	{"QS_LIMIT_PRICE_ALERTS", "price_alerts", func() int64 { return int64(notify.MaxAlerts) }, func(v int64) { notify.MaxAlerts = int(v) }, 1},
	{"QS_LIMIT_RULES", "rules", func() int64 { return int64(notify.MaxRules) }, func(v int64) { notify.MaxRules = int(v) }, 1},
	{"QS_LIMIT_SHOT_MB", "shot_mb", func() int64 { return int64(journal.MaxShot) }, func(v int64) { journal.MaxShot = int(v) }, 1 << 20},
	{"QS_LIMIT_SHOTS_TOTAL_MB", "shots_total_mb", func() int64 { return journal.MaxShotsUser }, func(v int64) { journal.MaxShotsUser = v }, 1 << 20},
}

// applyLimits 启动时读环境变量；填错（不是正整数）就用默认值并记一条日志。
func applyLimits(log *slog.Logger) {
	for _, d := range limitDefs {
		s := strings.TrimSpace(os.Getenv(d.env))
		if s == "" {
			continue
		}
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil || v <= 0 {
			log.Warn("上限填得不对，用默认值", "env", d.env, "value", s, "default", d.get()/d.unit)
			continue
		}
		d.set(v * d.unit)
		log.Info("上限", "env", d.env, "value", v)
	}
}

// limitsRoute GET /api/limits：现在用的上限（界面的提示语按它写数字，改了环境变量提示也跟着对）。不要登录，只是几个数字。
func limitsRoute(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/limits", func(w http.ResponseWriter, _ *http.Request) {
		out := map[string]int64{}
		for _, d := range limitDefs {
			out[d.key] = d.get() / d.unit
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(out)
	})
}
