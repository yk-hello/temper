package liqfeed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// 连接悄悄死掉（不报错、只是不来数据）：readIdle 到时间返回错误，外面就会重连（11-28 前会一直等下去）
func TestReadIdle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		_ = c.Write(r.Context(), websocket.MessageText, []byte("pong"))
		<-r.Context().Done() // 之后一声不吭
	}))
	defer srv.Close()
	ctx := context.Background()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	if raw, err := readIdle(ctx, c, 300*time.Millisecond); err != nil || string(raw) != "pong" {
		t.Fatalf("第一条：%q %v", raw, err)
	}
	t0 := time.Now()
	_, err = readIdle(ctx, c, 300*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "没有收到消息") || time.Since(t0) > 2*time.Second {
		t.Fatalf("没消息时要按时返回错误：%v（%v）", err, time.Since(t0))
	}
}
