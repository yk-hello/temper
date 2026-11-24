// qsig-fake：独立运行的假信号发生器。
//
//	QS_URL=http://localhost:8080 QS_KEY=demo QS_SECRET=... QS_STRATEGY=demo-random go run ./cmd/qsig-fake
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"time"

	"qs/internal/fakesig"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	every, err := time.ParseDuration(getenv("QS_EVERY", "45s"))
	if err != nil {
		every = 45 * time.Second
	}
	cfg := fakesig.Config{BaseURL: getenv("QS_URL", "http://localhost:8080"), KeyID: getenv("QS_KEY", "demo"), Secret: getenv("QS_SECRET", ""),
		StrategyID: getenv("QS_STRATEGY", "demo-random"), Instruments: strings.Split(getenv("QS_INSTRUMENTS", "BTC-PERP,ETH-PERP,SOL-PERP"), ","),
		Every: every, Log: log}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	fakesig.Run(ctx, cfg)
}

func getenv(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
