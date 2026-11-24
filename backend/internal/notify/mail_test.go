package notify

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

type capMail struct {
	mu   sync.Mutex
	sent []string // "收件人|标题|正文"
}

func (m *capMail) Enabled() bool { return true }
func (m *capMail) Send(_ context.Context, to, subject, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, to+"|"+subject+"|"+body)
	return nil
}
func (m *capMail) n() int { m.mu.Lock(); defer m.mu.Unlock(); return len(m.sent) }

// 18 第 8.2 条：邮件通知。默认只开和钱、安全有关的；只发给验证过的邮箱；每人每小时最多 mailPerHour 封
func TestMailChannel(t *testing.T) {
	s, now := newSvc()
	m := &capMail{}
	s.Mail = m
	s.emailOf = func(u int64) string {
		if u == 1 {
			return "me@example.com"
		}
		return "" // 2 号没验证过邮箱
	}
	ctx := context.Background()
	if p := s.prefOf(1, KLiq); !p.ML {
		t.Fatal("强平预警默认发邮件")
	}
	if p := s.prefOf(1, KFill); p.ML {
		t.Fatal("成交默认不发邮件")
	}
	s.Push(ctx, 1, KLiq, map[string]string{"inst": "BTC-PERP", "px": "90000", "dist": "3"})
	s.Push(ctx, 1, KFill, map[string]string{"inst": "BTC-PERP", "side": "buy", "qty": "1", "px": "1"})
	s.Push(ctx, 2, KLiq, map[string]string{"inst": "BTC-PERP", "px": "90000", "dist": "3"})
	waitN := func(want int) {
		for i := 0; i < 100 && m.n() < want; i++ {
			time.Sleep(10 * time.Millisecond)
		}
		time.Sleep(30 * time.Millisecond)
		if m.n() != want {
			t.Fatalf("应该发 %d 封，发了 %d 封：%v", want, m.n(), m.sent)
		}
	}
	waitN(1)
	if got := m.sent[0]; !strings.HasPrefix(got, "me@example.com|强平预警") || !strings.Contains(got, "在那里可以关掉") {
		t.Fatalf("邮件：%s", got)
	}
	// 打开成交的邮件：发；一小时内最多 mailPerHour 封
	if err := s.SetPref(ctx, 1, KFill, Pref{Site: true, ML: true}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < mailPerHour+5; i++ {
		s.Push(ctx, 1, KFill, map[string]string{"inst": "ETH-PERP", "side": "sell", "qty": "1", "px": "1"})
	}
	waitN(mailPerHour)
	*now += 3600_000 // 过了一小时又能发
	s.Push(ctx, 1, KFill, map[string]string{"inst": "ETH-PERP", "side": "sell", "qty": "1", "px": "1"})
	waitN(mailPerHour + 1)
	// 没配发信邮箱：一封都不发
	s.Mail = nil
	s.Push(ctx, 1, KLiq, map[string]string{"inst": "BTC-PERP", "px": "90000", "dist": "3"})
	waitN(mailPerHour + 1)
}
