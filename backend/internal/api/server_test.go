package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"qs/internal/market"
)

func newTestServer(t *testing.T) (*httptest.Server, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	hub := market.NewHub([]string{"BTC-PERP"}, 20*time.Millisecond)
	go hub.Run(ctx)
	go (&market.Sim{Symbols: []string{"BTCUSDT"}, Seed: 7, Tick: 5 * time.Millisecond}).Run(ctx, hub)
	s := &Server{Hub: hub, DBStatus: func() string { return "disabled" }, BrandName: "Temper",
		AllowedOrigins: []string{"*"}, Started: time.Now()}
	return httptest.NewServer(s.Handler()), cancel
}

func TestHealthAndWebSocket(t *testing.T) {
	ts, cancel := newTestServer(t)
	defer cancel()
	defer ts.Close()
	time.Sleep(100 * time.Millisecond)

	res, err := http.Get(ts.URL + "/api/health")
	if err != nil {
		t.Fatal(err)
	}
	var h health
	if err := json.NewDecoder(res.Body).Decode(&h); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if h.Status != "ok" || h.Brand != "Temper" || h.DB != "disabled" || !h.Market.Connected() {
		t.Fatalf("health: %+v", h)
	}

	ctx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelDial()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	frames := 0
	for frames < 3 {
		_, data, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("after %d frames: %v", frames, err)
		}
		var snap market.Snapshot
		if err := json.Unmarshal(data, &snap); err != nil || snap.Type != "snapshot" || len(snap.Instruments) != 1 {
			t.Fatalf("bad frame: %s", data)
		}
		frames++
	}
}
