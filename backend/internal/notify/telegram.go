package notify

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Telegram 发通知，收 /start 绑定。没配 token 时什么都不做。
// 用长轮询收消息，不需要公网地址，所以放在电脑上也能跑。
// 注意：token 在请求地址里，http 错误会把地址带出来，写日志前一定要去掉（见 safeErr）。
type Telegram struct {
	Token string
	Base  string // 默认 https://api.telegram.org，测试时换成本地
	HTTP  *http.Client
	Log   *slog.Logger
	S     *Service
	Retry []time.Duration // 发送失败后等多久再试，第一项是第一次

	mu      sync.Mutex
	bot     string
	codes   map[string]code
	out     chan outMsg
	changed chan struct{} // 令牌换了：关掉它，收消息的循环用新令牌重连
}

type code struct {
	user int64
	exp  time.Time
}

type outMsg struct {
	chat int64
	text string
}

// Enabled 配了 token 吗。
func (t *Telegram) Enabled() bool { return t != nil && t.token() != "" }

func (t *Telegram) token() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.Token
}

// SetToken 换机器人令牌（管理后台，2026-11-15）：马上停掉旧的收消息循环，用新的重连。空 = 关掉。
func (t *Telegram) SetToken(tok string) {
	t.mu.Lock()
	if tok == t.Token && t.changed != nil { // 没变：不打断正在收消息的连接
		t.mu.Unlock()
		return
	}
	t.Token, t.bot = tok, ""
	ch := t.changed
	t.changed = make(chan struct{})
	t.mu.Unlock()
	if ch != nil {
		close(ch)
	}
}

// Bot bot 的用户名。
func (t *Telegram) Bot() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.bot
}

// NewCode 绑定用的一次性代码，10 分钟有效。
func (t *Telegram) NewCode(user int64) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	c := hex.EncodeToString(b)
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.codes == nil {
		t.codes = map[string]code{}
	}
	now := time.Now()
	for k, v := range t.codes {
		if now.After(v.exp) {
			delete(t.codes, k)
		}
	}
	t.codes[c] = code{user: user, exp: now.Add(10 * time.Minute)}
	return c
}

func (t *Telegram) takeCode(c string) (int64, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	v, ok := t.codes[c]
	if !ok || time.Now().After(v.exp) {
		return 0, false
	}
	delete(t.codes, c)
	return v.user, true
}

// Send 排队发送，队列满了就丢（通知不能把交易拖慢）。
func (t *Telegram) Send(chat int64, text string) {
	if !t.Enabled() {
		return
	}
	t.mu.Lock()
	if t.out == nil {
		t.out = make(chan outMsg, 256)
	}
	ch := t.out
	t.mu.Unlock()
	select {
	case ch <- outMsg{chat, text}:
	default:
		if t.Log != nil {
			t.Log.Warn("Telegram 发送队列满了，丢掉一条")
		}
	}
}

// safeErr 去掉错误里的地址，地址里有 token。
func safeErr(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err.Error()
	}
	return err.Error()
}

func (t *Telegram) call(ctx context.Context, method string, body any, out any) error {
	base := t.Base
	if base == "" {
		base = "https://api.telegram.org"
	}
	hc := t.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 40 * time.Second}
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", base+"/bot"+t.token()+"/"+method, bytes.NewReader(b))
	if err != nil {
		return errors.New("bad request")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return errors.New(safeErr(err))
	}
	defer resp.Body.Close()
	var r struct {
		OK         bool            `json:"ok"`
		Result     json.RawMessage `json:"result"`
		Parameters struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if !r.OK {
		if r.Parameters.RetryAfter > 0 {
			select {
			case <-ctx.Done():
			case <-time.After(time.Duration(r.Parameters.RetryAfter) * time.Second):
			}
		}
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if out != nil {
		return json.Unmarshal(r.Result, out)
	}
	return nil
}

// deliver 发一条。网络断了就等一会儿再试，强平预警不能因为抖一下就丢了。
func (t *Telegram) deliver(ctx context.Context, m outMsg) {
	var err error
	for i, wait := range t.retry() {
		if i > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		err = t.call(c, "sendMessage", map[string]any{"chat_id": m.chat, "text": m.text}, nil)
		cancel()
		if err == nil {
			return
		}
		if t.Log != nil {
			t.Log.Warn("Telegram 发送失败", "try", i+1, "err", err)
		}
	}
}

func (t *Telegram) retry() []time.Duration {
	if t.Retry != nil {
		return t.Retry
	}
	return []time.Duration{0, 2 * time.Second, 5 * time.Second, 20 * time.Second, time.Minute}
}

// Run 一直跑：发送队列和收消息。
// Run 发消息和收消息。没有令牌时等着；管理后台填了或换了令牌就（重新）连上。
func (t *Telegram) Run(ctx context.Context) {
	t.mu.Lock()
	if t.out == nil {
		t.out = make(chan outMsg, 256)
	}
	if t.changed == nil {
		t.changed = make(chan struct{})
	}
	ch := t.out
	t.mu.Unlock()
	go func() {
		tick := time.NewTicker(50 * time.Millisecond) // Telegram 限制每秒约 30 条
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case m := <-ch:
				<-tick.C
				t.deliver(ctx, m)
			}
		}
	}()
	for ctx.Err() == nil {
		t.mu.Lock()
		changed, tok := t.changed, t.Token
		t.mu.Unlock()
		if tok == "" {
			select {
			case <-ctx.Done():
				return
			case <-changed:
				continue
			}
		}
		c, cancel := context.WithCancel(ctx)
		go func() {
			select {
			case <-changed:
				cancel()
			case <-c.Done():
			}
		}()
		t.poll(c)
		cancel()
	}
}

// poll 用现在的令牌问 bot 名字、收消息，直到 ctx 结束（令牌换了也会结束）。
func (t *Telegram) poll(ctx context.Context) {
	// 问 bot 叫什么，取不到就过一会儿再试（用户的网络经常断）
	for t.Bot() == "" {
		var me struct {
			Username string `json:"username"`
		}
		c, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := t.call(c, "getMe", map[string]any{}, &me)
		cancel()
		if err == nil && me.Username != "" {
			t.mu.Lock()
			t.bot = me.Username
			t.mu.Unlock()
			break
		}
		if t.Log != nil {
			t.Log.Warn("Telegram 连不上", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
		}
	}
	var offset int64
	down := false
	for ctx.Err() == nil {
		var ups []struct {
			ID      int64 `json:"update_id"`
			Message *struct {
				Text string `json:"text"`
				Chat struct {
					ID   int64  `json:"id"`
					Type string `json:"type"`
				} `json:"chat"`
			} `json:"message"`
		}
		c, cancel := context.WithTimeout(ctx, 35*time.Second)
		err := t.call(c, "getUpdates", map[string]any{"timeout": 25, "offset": offset, "allowed_updates": []string{"message"}}, &ups)
		cancel()
		if err != nil {
			if !down && t.Log != nil && ctx.Err() == nil { // 断网时只记第一次，不刷屏
				t.Log.Warn("Telegram 收消息失败", "err", err)
			}
			down = true
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}
		down = false
		for _, u := range ups {
			offset = u.ID + 1
			if u.Message != nil {
				t.handle(ctx, u.Message.Chat.ID, u.Message.Chat.Type, u.Message.Text)
			}
		}
	}
}

func (t *Telegram) handle(ctx context.Context, chat int64, kind, text string) {
	text = strings.TrimSpace(text)
	switch {
	case kind != "private":
		return // 通知里有账户信息，只发私聊
	case strings.HasPrefix(text, "/start "):
		user, ok := t.takeCode(strings.TrimSpace(strings.TrimPrefix(text, "/start ")))
		if !ok {
			t.Send(chat, "这个绑定链接已失效，请回到账户页重新生成。\nThis link has expired. Generate a new one on the Account page.")
			return
		}
		t.S.bind(ctx, user, chat)
		t.Send(chat, "绑定成功。发送 /stop 可以解除。\nLinked. Send /stop to unlink.")
	case text == "/stop":
		t.S.unbindChat(ctx, chat)
		t.Send(chat, "已解除绑定。\nUnlinked.")
	case text == "/start":
		t.Send(chat, "请在账户页点“绑定 Telegram”。\nUse “Link Telegram” on the Account page.")
	}
}
