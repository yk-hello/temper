package market

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// idleSink 记下连接状态的变化
type idleSink struct {
	mu     sync.Mutex
	st     SourceStatus
	events atomic.Int32
}

func (s *idleSink) Publish(Event) { s.events.Add(1) }
func (s *idleSink) UpdateStatus(f func(*SourceStatus)) {
	s.mu.Lock()
	f(&s.st)
	s.mu.Unlock()
}
func (s *idleSink) snap() (bool, int, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st.Connections["spot"], s.st.Reconnects, s.st.LastError
}

// 连接悄悄死掉（不报错、只是不再来数据）：Idle 时间内一条消息都没有就断开重连（11-28：原来要等 60 秒）
func TestBinanceIdleReconnect(t *testing.T) {
	var conns atomic.Int32
	var mu sync.Mutex
	var at []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		mu.Lock()
		at = append(at, time.Now())
		mu.Unlock()
		conns.Add(1)
		msg := `{"stream":"btcusdt@aggTrade","data":{"e":"aggTrade","E":1,"s":"BTCUSDT","p":"100","q":"1","T":1,"m":false}}`
		for i := 0; i < 3; i++ {
			_ = c.Write(r.Context(), websocket.MessageText, []byte(msg))
		}
		<-r.Context().Done() // 之后一声不吭，也不关连接
	}))
	defer srv.Close()
	s := &idleSink{st: SourceStatus{Connections: map[string]bool{}, Lag: map[string]int64{}}}
	var logs safeBuf
	b := &Binance{Symbols: []string{"BTCUSDT"}, Spot: true, BaseURL: "ws" + strings.TrimPrefix(srv.URL, "http"), Idle: 300 * time.Millisecond, Log: slog.New(slog.NewTextHandler(&logs, nil))}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Run(ctx, s)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && conns.Load() < 2 {
		time.Sleep(20 * time.Millisecond)
	}
	if conns.Load() < 2 {
		t.Fatalf("5 秒内没有重连（连了 %d 次）", conns.Load())
	}
	_, rec, _ := s.snap()
	if rec < 1 || !strings.Contains(logs.String(), "没有收到消息") {
		t.Fatalf("重连次数 %d，日志：%s", rec, logs.String())
	}
	mu.Lock()
	gap := at[1].Sub(at[0])
	mu.Unlock()
	if gap < 300*time.Millisecond || gap > 2500*time.Millisecond { // 300 毫秒没消息 + 第一次重连等 1 秒
		t.Fatalf("两次连接隔了 %v", gap)
	}
	if s.events.Load() < 3 {
		t.Fatalf("断之前的消息要照常收到：%d 条", s.events.Load())
	}
}

// 不设 Idle 时用默认的 10 秒
func TestBinanceIdleDefault(t *testing.T) {
	if idleDefault != 10*time.Second {
		t.Fatalf("默认 %v", idleDefault)
	}
}

// safeBuf 日志可能从几个线程同时写
type safeBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (w *safeBuf) Write(p []byte) (int, error) { w.mu.Lock(); defer w.mu.Unlock(); return w.b.Write(p) }
func (w *safeBuf) String() string              { w.mu.Lock(); defer w.mu.Unlock(); return w.b.String() }
