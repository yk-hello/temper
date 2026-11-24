package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 谁能看到什么（12 的 3.13）：逐个接口，未登录、登录没开启、开启服务三种人都测。
func TestAccessByEndpoint(t *testing.T) {
	level := "anon"
	ok := func(string) (any, bool) { return map[string]string{"secret": "理由和证据"}, true }
	s := &Server{Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Strategies: func() any { return []string{"s1"} },
		Portfolio: func(_ string, full bool) (any, bool) {
			if full {
				return map[string]any{"positions": []string{"BTC"}}, true
			}
			return map[string]any{"positions": []string{}}, true
		},
		Performance: func(string) (any, bool) { return map[string]string{"closed": "没有理由"}, true },
		Decisions:   ok, Trades: ok, Calibration: ok,
		Signals: func(int) any { return []string{"signal"} },
		Access: func(*http.Request) (bool, bool) {
			return level != "anon", level == "open"
		},
	}
	srv := httptest.NewServer(s.routes())
	defer srv.Close()
	get := func(path string) (int, string) {
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	gated := []string{"/api/signals", "/api/strategies/s1/decisions", "/api/strategies/s1/trades", "/api/strategies/s1/calibration"}
	for _, want := range []struct {
		level string
		code  int
	}{{"anon", 401}, {"in", 403}, {"open", 200}} {
		level = want.level
		for _, p := range gated {
			if c, body := get(p); c != want.code {
				t.Fatalf("%s %s：%d，应 %d（%s）", want.level, p, c, want.code, body)
			}
		}
		// 公开的：谁都能看净值、汇总、已平仓交易
		for _, p := range []string{"/api/strategies", "/api/strategies/s1", "/api/strategies/s1/performance"} {
			if c, _ := get(p); c != 200 {
				t.Fatalf("%s %s：%d", want.level, p, c)
			}
		}
		// 还没平的持仓只给开启服务的人
		_, body := get("/api/strategies/s1")
		var v struct {
			Positions []string `json:"positions"`
		}
		_ = json.Unmarshal([]byte(body), &v)
		if (len(v.Positions) > 0) != (want.level == "open") {
			t.Fatalf("%s 看到的持仓：%s", want.level, body)
		}
	}
	// 没配置 Access 时默认关着
	s.Access = nil
	if c, _ := get("/api/signals"); c != 401 {
		t.Fatalf("默认应关着：%d", c)
	}
	if _, body := get("/api/strategies/s1"); strings.Contains(body, "BTC") {
		t.Fatalf("默认不给持仓：%s", body)
	}
}
