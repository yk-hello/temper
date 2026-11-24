package mailer

import (
	"context"
	"sync"
)

// Live 可以随时换的发信设置（2026-11-15，用户："别人用不能也要当场写吧？"）：
// 部署的人在管理后台填好邮箱和授权码，马上生效，不用改配置文件、不用重启。认证（验证码）和通知共用这一个。
type Live struct {
	mu  sync.RWMutex
	c   Config
	src string // web = 管理后台填的；env = 配置文件（deploy/.env）；空 = 没配
}

// NewLive 启动时先用配置文件里的（有的话），数据库连上后再看管理后台有没有填过。
func NewLive(c Config) *Live {
	l := &Live{}
	if c.Enabled() {
		l.c, l.src = c, "env"
	} else {
		l.c = c
	}
	return l
}

// Set 换成新的设置；src 写明来源。
func (l *Live) Set(c Config, src string) {
	l.mu.Lock()
	l.c, l.src = c, src
	if !c.Enabled() {
		l.src = ""
	}
	l.mu.Unlock()
}

// Current 现在用的设置和来源。
func (l *Live) Current() (Config, string) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.c, l.src
}

// Enabled 现在能不能发信。
func (l *Live) Enabled() bool {
	c, _ := l.Current()
	return c.Enabled()
}

// Send 用现在的设置发一封。
func (l *Live) Send(ctx context.Context, to, subject, body string) error {
	c, _ := l.Current()
	return c.Send(ctx, to, subject, body)
}
