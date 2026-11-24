package mailer

import (
	"bufio"
	"context"
	"encoding/base64"
	"mime"
	"net"
	"net/mail"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSMTP 本机的假发信服务器：不加密（只有本机允许），记下登录、发件人、收件人和信的内容。
type fakeSMTP struct {
	ln       net.Listener
	mu       sync.Mutex
	auth     string
	from, to string
	data     string
	starttls bool // 声称支持 STARTTLS（测"非本机不加密就拒绝"用不到）
}

func newFake(t *testing.T, host string) *fakeSMTP {
	ln, err := net.Listen("tcp", host+":0")
	if err != nil {
		t.Skip("开不了本机端口：", err)
	}
	f := &fakeSMTP{ln: ln}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakeSMTP) port() int { return f.ln.Addr().(*net.TCPAddr).Port }

func (f *fakeSMTP) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	w := func(s string) { _, _ = c.Write([]byte(s + "\r\n")) }
	w("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.TrimRight(line, "\r\n")
		up := strings.ToUpper(cmd)
		switch {
		case strings.HasPrefix(up, "EHLO"), strings.HasPrefix(up, "HELO"):
			w("250-fake")
			w("250 AUTH PLAIN")
		case strings.HasPrefix(up, "AUTH PLAIN"):
			raw, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(cmd[len("AUTH PLAIN"):]))
			f.mu.Lock()
			f.auth = string(raw)
			f.mu.Unlock()
			w("235 ok")
		case strings.HasPrefix(up, "MAIL FROM:"):
			f.mu.Lock()
			f.from = strings.Trim(cmd[len("MAIL FROM:"):], "<> ")
			f.mu.Unlock()
			w("250 ok")
		case strings.HasPrefix(up, "RCPT TO:"):
			f.mu.Lock()
			f.to = strings.Trim(cmd[len("RCPT TO:"):], "<> ")
			f.mu.Unlock()
			w("250 ok")
		case up == "DATA":
			w("354 go")
			var sb strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				sb.WriteString(l)
			}
			f.mu.Lock()
			f.data = sb.String()
			f.mu.Unlock()
			w("250 queued")
		case up == "QUIT":
			w("221 bye")
			return
		default:
			w("250 ok")
		}
	}
}

func TestSendPlainLocal(t *testing.T) {
	f := newFake(t, "127.0.0.1")
	c := Config{Host: "127.0.0.1", Port: f.port(), User: "me@example.com", Password: "app-code-123", From: "me@example.com", FromName: "QS 测试", Timeout: 5 * time.Second}
	if !c.Enabled() {
		t.Fatal("配了就该能用")
	}
	if err := c.Send(context.Background(), "you@example.com", "验证码 123456", "你的验证码是 123456。\n10 分钟内有效。"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.auth != "\x00me@example.com\x00app-code-123" || f.from != "me@example.com" || f.to != "you@example.com" {
		t.Fatalf("登录或收发件人：%q %q %q", f.auth, f.from, f.to)
	}
	msg, err := mail.ReadMessage(strings.NewReader(f.data))
	if err != nil {
		t.Fatal(err)
	}
	subj, _ := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	from, _ := mail.ParseAddress(msg.Header.Get("From"))
	body := make([]byte, 4096)
	n, _ := base64.NewDecoder(base64.StdEncoding, msg.Body).Read(body)
	if subj != "验证码 123456" || from.Name != "QS 测试" || !strings.Contains(string(body[:n]), "10 分钟内有效") {
		t.Fatalf("信的内容：%q %q %q", subj, from, string(body[:n]))
	}
	if msg.Header.Get("Content-Type") != "text/plain; charset=UTF-8" || msg.Header.Get("Message-ID") == "" {
		t.Fatalf("信头：%v", msg.Header)
	}
}

// 不是本机、服务器又不支持加密：不发（不能把授权码明文发到网上）
func TestRefusePlainRemote(t *testing.T) {
	f := newFake(t, "127.0.0.2") // 也是本机，但名字不是 localhost/127.0.0.1，当成"别处"
	c := Config{Host: "127.0.0.2", Port: f.port(), User: "me@example.com", Password: "secret", From: "me@example.com", Timeout: 5 * time.Second}
	err := c.Send(context.Background(), "you@example.com", "x", "y")
	if err == nil || !strings.Contains(err.Error(), "不支持加密") {
		t.Fatalf("应该拒绝：%v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.auth != "" {
		t.Fatal("授权码被明文发出去了")
	}
}

func TestOffAndBadAddress(t *testing.T) {
	if err := (Config{}).Send(context.Background(), "a@example.com", "x", "y"); err != ErrOff {
		t.Fatalf("没配：%v", err)
	}
	c := Config{Host: "127.0.0.1", Port: 1, From: "me@example.com"}
	if err := c.Send(context.Background(), "not-an-address", "x", "y"); err == nil || !strings.Contains(err.Error(), "收件地址") {
		t.Fatalf("坏地址：%v", err)
	}
}
