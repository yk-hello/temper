package auth

import (
	"errors"
	"strings"
	"time"
)

// SIWE（EIP-4361）登录消息：
//
//	example.com wants you to sign in with your Ethereum account:
//	0xAbC...
//
//	<声明>
//
//	URI: https://example.com
//	Version: 1
//	Chain ID: 1
//	Nonce: abcdef12
//	Issued At: 2026-11-04T10:00:00Z
//	Expiration Time: 2026-11-04T10:05:00Z
type SiweMessage struct {
	Domain    string
	Address   string
	Statement string
	URI       string
	Version   string
	ChainID   string
	Nonce     string
	IssuedAt  time.Time
	ExpiresAt time.Time
}

const siweHeader = " wants you to sign in with your Ethereum account:"

// SiweStatement 是我们要求用户签的声明，明确写出"不是交易、不会转账"。
const SiweStatement = "登录 Temper。这只是登录确认，不是交易，不会动用你的资产。 Sign in to Temper. This is not a transaction and will not move any funds."

func ParseSiwe(msg string) (SiweMessage, error) {
	var m SiweMessage
	lines := strings.Split(strings.ReplaceAll(msg, "\r\n", "\n"), "\n")
	if len(lines) < 8 || !strings.HasSuffix(lines[0], siweHeader) {
		return m, errors.New("不是标准的登录消息")
	}
	m.Domain = strings.TrimSuffix(lines[0], siweHeader)
	m.Address = strings.ToLower(strings.TrimSpace(lines[1]))
	if !strings.HasPrefix(m.Address, "0x") || len(m.Address) != 42 {
		return m, errors.New("地址格式不对")
	}
	i := 2
	if i < len(lines) && lines[i] == "" {
		i++
	}
	if i < len(lines) && !strings.Contains(lines[i], ": ") {
		m.Statement = lines[i]
		i++
		if i < len(lines) && lines[i] == "" {
			i++
		}
	}
	for ; i < len(lines); i++ {
		k, v, ok := strings.Cut(lines[i], ": ")
		if !ok {
			continue
		}
		switch k {
		case "URI":
			m.URI = v
		case "Version":
			m.Version = v
		case "Chain ID":
			m.ChainID = v
		case "Nonce":
			m.Nonce = v
		case "Issued At":
			m.IssuedAt, _ = time.Parse(time.RFC3339, v)
		case "Expiration Time":
			m.ExpiresAt, _ = time.Parse(time.RFC3339, v)
		}
	}
	if m.Version != "1" || m.Nonce == "" || m.IssuedAt.IsZero() {
		return m, errors.New("登录消息缺少字段")
	}
	return m, nil
}
