// Package mailer 用部署的人自己的邮箱发信（D100 第 2 条）：填发信服务器、账号和授权码，不用另搭邮件服务。
// Gmail、Outlook、QQ、163 这些都能用（要在邮箱设置里开 SMTP、拿授权码，不是登录密码）。
//
// 465 端口直接走 TLS；587、25 端口先明文连上再升级（STARTTLS）。服务器不支持加密时拒绝发：
// 不能把账号密码明文发到网上。唯一的例外是本机（127.0.0.1 / localhost），给测试和本机的收信工具用。
package mailer

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config 发信设置。都从环境变量读（deploy/.env，不进仓库）。
type Config struct {
	Host     string // SMTP_HOST，例如 smtp.gmail.com
	Port     int    // SMTP_PORT：465（TLS）或 587（STARTTLS），默认 465
	User     string // SMTP_USER：登录发信服务器的账号，一般就是邮箱地址
	Password string // SMTP_PASSWORD：授权码
	From     string // MAIL_FROM：发件地址，不填用 SMTP_USER
	FromName string // MAIL_FROM_NAME：发件人显示的名字，不填用产品名
	Timeout  time.Duration
}

// FromEnv 读环境变量。没配 SMTP_HOST 就是没有邮件（Enabled 为 false）。
func FromEnv(brand string) Config {
	port, _ := strconv.Atoi(os.Getenv("SMTP_PORT"))
	if port == 0 {
		port = 465
	}
	c := Config{Host: strings.TrimSpace(os.Getenv("SMTP_HOST")), Port: port, User: strings.TrimSpace(os.Getenv("SMTP_USER")),
		Password: os.Getenv("SMTP_PASSWORD"), From: strings.TrimSpace(os.Getenv("MAIL_FROM")), FromName: strings.TrimSpace(os.Getenv("MAIL_FROM_NAME"))}
	if c.From == "" {
		c.From = c.User
	}
	if c.FromName == "" {
		c.FromName = brand
	}
	return c
}

// Enabled 配齐了才算有邮件：服务器、发件地址，还有授权码（本机测试用的收信工具可以不要）。
// 2026-11-15：原来只要填了服务器就算，授权码空着也会关掉开发模式，验证码却发不出去，新用户注册不了。
func (c Config) Enabled() bool {
	return c.Host != "" && c.From != "" && (c.Password != "" || local(c.Host))
}

var ErrOff = errors.New("MAIL_OFF")

func local(host string) bool { return host == "localhost" || host == "127.0.0.1" || host == "::1" }

// Send 发一封纯文本邮件。
func (c Config) Send(ctx context.Context, to, subject, body string) error {
	if !c.Enabled() {
		return ErrOff
	}
	if _, err := mail.ParseAddress(to); err != nil {
		return fmt.Errorf("收件地址不对：%w", err)
	}
	timeout := c.Timeout
	if timeout == 0 {
		timeout = 20 * time.Second
	}
	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	addr := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	dialer := &net.Dialer{Deadline: deadline}
	var conn net.Conn
	var err error
	tlsCfg := &tls.Config{ServerName: c.Host, MinVersion: tls.VersionTLS12}
	if c.Port == 465 {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, tlsCfg)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return dialErr(addr, err)
	}
	_ = conn.SetDeadline(deadline)
	cl, err := smtp.NewClient(conn, c.Host)
	if err != nil {
		conn.Close()
		return dialErr(addr, err)
	}
	defer cl.Close()
	if c.Port != 465 {
		if ok, _ := cl.Extension("STARTTLS"); ok {
			if err := cl.StartTLS(tlsCfg); err != nil {
				return fmt.Errorf("加密连接失败：%w", err)
			}
		} else if !local(c.Host) {
			return errors.New("发信服务器不支持加密（STARTTLS），不能明文发送账号密码")
		}
	}
	if c.User != "" && c.Password != "" {
		if ok, _ := cl.Extension("AUTH"); ok {
			if err := cl.Auth(smtp.PlainAuth("", c.User, c.Password, c.Host)); err != nil {
				return fmt.Errorf("发信账号或授权码不对：%w", err)
			}
		}
	}
	if err := cl.Mail(c.From); err != nil {
		return err
	}
	if err := cl.Rcpt(to); err != nil {
		return err
	}
	w, err := cl.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(c.message(to, subject, body)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return cl.Quit()
}

// message 拼一封 UTF-8 的纯文本邮件：标题按 RFC 2047 编码，正文 base64（每行 76 个字符）。
func (c Config) message(to, subject, body string) []byte {
	var b bytes.Buffer
	from := (&mail.Address{Name: c.FromName, Address: c.From}).String()
	id := make([]byte, 12)
	_, _ = rand.Read(id)
	domain := c.From[strings.LastIndex(c.From, "@")+1:]
	hdr := [][2]string{
		{"From", from},
		{"To", to},
		{"Subject", mime.QEncoding.Encode("utf-8", subject)},
		{"Date", time.Now().Format(time.RFC1123Z)},
		{"Message-ID", "<" + hex.EncodeToString(id) + "@" + domain + ">"},
		{"MIME-Version", "1.0"},
		{"Content-Type", "text/plain; charset=UTF-8"},
		{"Content-Transfer-Encoding", "base64"},
		{"Auto-Submitted", "auto-generated"}, // 系统自动发的，自动回复别回过来
	}
	for _, h := range hdr {
		b.WriteString(h[0] + ": " + h[1] + "\r\n")
	}
	b.WriteString("\r\n")
	enc := base64.StdEncoding.EncodeToString([]byte(strings.ReplaceAll(body, "\n", "\r\n")))
	for len(enc) > 76 {
		b.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc + "\r\n")
	return b.Bytes()
}

// dialErr 连接阶段的错误翻成大白话（测试按钮给不懂网络的人看）。
// 最常见的是"连上了又被掐断"（EOF / reset）：代理、VPN 或者网络运营商不放行发信端口 465、587，换哪家邮箱都一样。
func dialErr(addr string, err error) error {
	s := strings.ToLower(err.Error())
	switch {
	case errors.Is(err, io.EOF) || strings.Contains(s, "eof") || strings.Contains(s, "reset") || strings.Contains(s, "forcibly closed"):
		return fmt.Errorf("连上 %s 后被中途掐断：多半是代理、VPN 或网络不放行发信端口（465、587），换哪家邮箱都一样。在代理软件里把这两个端口设成直连，或换一个网络再试", addr)
	case strings.Contains(s, "no such host"):
		return fmt.Errorf("找不到发信服务器 %s：检查一下有没有拼错", addr)
	case strings.Contains(s, "timeout") || strings.Contains(s, "deadline"):
		return fmt.Errorf("连 %s 超时：这台电脑连不到那边，看看网络和代理", addr)
	case strings.Contains(s, "refused"):
		return fmt.Errorf("%s 拒绝连接：服务器地址或端口不对", addr)
	}
	return fmt.Errorf("连不上发信服务器 %s：%w", addr, err)
}
