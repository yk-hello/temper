package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// 对外 Webhook（18 第 6.3 条）：每个人可以填一个地址，选中的事件 POST 一份 JSON 过去（自己的机器人、n8n、企业微信转发……）。
// 每次都签名：X-QS-Signature = HMAC-SHA256(secret, 时间戳 + "." + 请求体)，收的一方能验证确实是这里发的。
// 防止被拿来打内网（服务器可能在公网）：默认不发到本机、内网、链路本地地址（连接时按解析出来的地址检查，
// 改 DNS 也绕不过）；也不跟随跳转。自己部署在内网、确实要发到内网的，设 QS_WEBHOOK_PRIVATE=1。

// Hook 一个人的 Webhook。
type Hook struct {
	URL        string `json:"url"`
	Secret     string `json:"-"`
	LastAt     int64  `json:"last_at"`
	LastStatus int    `json:"last_status"` // HTTP 状态码；0 = 没发过或连不上
	LastErr    string `json:"last_err,omitempty"`
}

// Webhooks 发送器：后台一个协程一条条发，失败按 1、5、30 秒重试。
type Webhooks struct {
	AllowPrivate bool
	Client       *http.Client // 测试时换
	Retry        []time.Duration
	out          chan whJob
}

type whJob struct {
	user int64
	hook Hook
	body []byte
	kind string
}

var errPrivate = errors.New("不发到本机或内网地址（自己部署在内网要发的，设 QS_WEBHOOK_PRIVATE=1）")

func blocked(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsInterfaceLocalMulticast()
}

func (w *Webhooks) client() *http.Client {
	if w.Client != nil {
		return w.Client
	}
	d := &net.Dialer{Timeout: 8 * time.Second}
	if !w.AllowPrivate {
		d.Control = func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			if ip := net.ParseIP(host); ip == nil || blocked(ip) {
				return errPrivate
			}
			return nil
		}
	}
	tr := &http.Transport{DialContext: d.DialContext, TLSHandshakeTimeout: 8 * time.Second, ResponseHeaderTimeout: 10 * time.Second, Proxy: nil}
	return &http.Client{Transport: tr, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// CheckURL 保存前检查地址：只认 http、https，要有主机名，不超过 500 字。写的是 IP 时直接查是不是内网。
func (w *Webhooks) CheckURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(raw) > 500 || u.User != nil {
		return errors.New("BAD_URL")
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && blocked(ip) && !w.AllowPrivate {
		return errors.New("WEBHOOK_PRIVATE")
	}
	if !w.AllowPrivate && (u.Hostname() == "localhost" || strings.HasSuffix(u.Hostname(), ".localhost")) {
		return errors.New("WEBHOOK_PRIVATE")
	}
	return nil
}

func newSecret() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return "whsec_" + hex.EncodeToString(b)
}

// sign 签名：HMAC-SHA256(secret, 时间戳 + "." + 请求体)
func sign(secret, ts string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(ts + "."))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

// post 发一次，返回 HTTP 状态码（0 = 连不上）和错误说明。
// shape 聊天软件的进站 Webhook 有自己的格式（18 第 8.5 条）：认得出是 Discord、Slack 的地址，就把那句话按它们的格式发
// （照我们的 JSON 发过去对方会回 400）。别的地址照原样发。Discord 里不让文字触发 @everyone 这类提醒。
func shape(rawURL string, body []byte) []byte {
	u, err := url.Parse(rawURL)
	if err != nil {
		return body
	}
	var m struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(body, &m) != nil || m.Text == "" {
		return body
	}
	host := strings.ToLower(u.Hostname())
	switch {
	case (host == "discord.com" || host == "discordapp.com" || strings.HasSuffix(host, ".discord.com")) && strings.HasPrefix(u.Path, "/api/webhooks/"):
		text := []rune(m.Text)
		if len(text) > 1900 { // Discord 一条最多 2000 个字
			text = append(text[:1899], '…')
		}
		out, _ := json.Marshal(map[string]any{"content": string(text), "allowed_mentions": map[string]any{"parse": []string{}}})
		return out
	case host == "hooks.slack.com":
		out, _ := json.Marshal(map[string]string{"text": m.Text})
		return out
	}
	return body
}

func (w *Webhooks) post(ctx context.Context, h Hook, kind string, body []byte) (int, string) {
	body = shape(h.URL, body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.URL, bytes.NewReader(body))
	if err != nil {
		return 0, err.Error()
	}
	ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "qs-webhook/1")
	req.Header.Set("X-QS-Event", kind)
	req.Header.Set("X-QS-Timestamp", ts)
	req.Header.Set("X-QS-Signature", sign(h.Secret, ts, body))
	res, err := w.client().Do(req)
	if err != nil {
		var ne net.Error
		msg := err.Error()
		if errors.Is(err, errPrivate) {
			msg = errPrivate.Error()
		} else if errors.As(err, &ne) && ne.Timeout() {
			msg = "超时（15 秒）"
		}
		return 0, msg
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return res.StatusCode, fmt.Sprintf("对方回了 %d", res.StatusCode)
	}
	return res.StatusCode, ""
}

// run 后台发送；done 记录最后一次结果（给界面看）。
func (w *Webhooks) run(ctx context.Context, done func(user int64, status int, errMsg string)) {
	retry := w.Retry
	if retry == nil {
		retry = []time.Duration{time.Second, 5 * time.Second, 30 * time.Second}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-w.out:
			var st int
			var msg string
			for i := 0; ; i++ {
				c, cancel := context.WithTimeout(ctx, 20*time.Second)
				st, msg = w.post(c, j.hook, j.kind, j.body)
				cancel()
				// 成功、对方说请求本身有问题（4xx）、不让发的地址：不再试
				if msg == "" || (st >= 400 && st < 500) || msg == errPrivate.Error() || i >= len(retry) {
					break
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(retry[i]):
				}
			}
			if done != nil {
				done(j.user, st, msg)
			}
		}
	}
}

func (w *Webhooks) enqueue(j whJob) {
	if w.out == nil {
		return
	}
	select {
	case w.out <- j:
	default: // 积压太多（对方一直不响应）：丢掉，不拖住别的通知
	}
}

// payload 发出去的内容：事件、时间、结构化参数，和按这个人的语言写好的一句话。
func payload(kind string, t int64, params map[string]string, text string) []byte {
	b, _ := json.Marshal(map[string]any{"event": kind, "time": t, "params": params, "text": text})
	return b
}

// StartWebhooks 起后台发送协程（数据库连上之后调用）。
func (s *Service) StartWebhooks(ctx context.Context) {
	if s.WH == nil {
		return
	}
	s.WH.out = make(chan whJob, 256)
	go s.WH.run(ctx, s.recordWebhook)
}

func (s *Service) recordWebhook(user int64, status int, errMsg string) {
	now := s.now()
	s.mu.Lock()
	if h := s.hooks[user]; h != nil {
		h.LastAt, h.LastStatus, h.LastErr = now, status, errMsg
	}
	s.mu.Unlock()
	if s.Pool != nil {
		_, _ = s.Pool.Exec(context.Background(), `update notify_webhooks set last_at = $2, last_status = $3, last_err = $4 where user_id = $1`, user, now, status, errMsg)
	}
}

// SetWebhook 设地址。newSecret = true 时换一个签名密钥（第一次设置总是新的）；返回密钥（界面只在这时显示）。
func (s *Service) SetWebhook(ctx context.Context, user int64, raw string, rotate bool) (string, error) {
	if s.WH == nil {
		return "", errors.New("WEBHOOK_OFF")
	}
	raw = strings.TrimSpace(raw)
	if err := s.WH.CheckURL(raw); err != nil {
		return "", err
	}
	s.mu.Lock()
	old := s.hooks[user]
	secret := ""
	if old != nil && !rotate {
		secret = old.Secret
	} else {
		secret = newSecret()
	}
	s.hooks[user] = &Hook{URL: raw, Secret: secret}
	s.mu.Unlock()
	if s.Pool != nil {
		if _, err := s.Pool.Exec(ctx, `insert into notify_webhooks (user_id, url, secret, created) values ($1,$2,$3,$4) on conflict (user_id) do update set url = excluded.url, secret = excluded.secret, last_at = 0, last_status = 0, last_err = ''`, user, raw, secret, s.now()); err != nil {
			return "", err
		}
	}
	if old != nil && !rotate {
		return "", nil // 只改地址：密钥不变，不再显示
	}
	return secret, nil
}

// DelWebhook 删掉（不再发）。
func (s *Service) DelWebhook(ctx context.Context, user int64) {
	s.mu.Lock()
	delete(s.hooks, user)
	s.mu.Unlock()
	if s.Pool != nil {
		_, _ = s.Pool.Exec(ctx, `delete from notify_webhooks where user_id = $1`, user)
	}
}

// TestWebhook 马上发一条测试消息（同步），返回对方的状态码和错误说明。
func (s *Service) TestWebhook(ctx context.Context, user int64) (int, string, error) {
	s.mu.Lock()
	h := s.hooks[user]
	s.mu.Unlock()
	if h == nil || s.WH == nil {
		return 0, "", errors.New("NOT_FOUND")
	}
	c, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	t := s.now()
	st, msg := s.WH.post(c, *h, "test", payload("test", t, map[string]string{}, Render(s.langOf(ctx, user), "test", nil)))
	s.recordWebhook(user, st, msg)
	return st, msg, nil
}
