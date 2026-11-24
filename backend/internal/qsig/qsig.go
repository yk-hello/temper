// Package qsig 是 qsig/1 信号协议的平台接收端（docs/04_信号协议规范.md）。
// 一条消息要么整体接受、要么整体拒绝；规则与对外的协议说明（docs/qsig-1.md）一致，不得修改。
package qsig

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/shopspring/decimal"
)

//go:embed schema.json
var schemaJSON []byte

const TimeLayout = "2006-01-02T15:04:05.000Z"

// Message 是三种消息的合集，只解析平台需要的字段；rationale、meta 原样保留给前端。
type Message struct {
	Protocol        string          `json:"protocol"`
	Type            string          `json:"type"`
	ID              string          `json:"id"`
	StrategyID      string          `json:"strategy_id"`
	StrategyVersion string          `json:"strategy_version"`
	IssuedAt        string          `json:"issued_at"`
	DataAsOf        string          `json:"data_as_of,omitempty"`
	ValidUntil      string          `json:"valid_until,omitempty"`
	Mode            string          `json:"mode,omitempty"`
	Targets         []TargetMsg     `json:"targets,omitempty"`
	Meta            json.RawMessage `json:"meta,omitempty"`
	Status          string          `json:"status,omitempty"` // heartbeat
	Note            string          `json:"note,omitempty"`
	Action          string          `json:"action,omitempty"` // halt
	Reason          *struct {
		Zh string `json:"zh"`
	} `json:"reason,omitempty"`
}

type TargetMsg struct {
	Instrument     string  `json:"instrument"`
	Weight         string  `json:"weight"`
	Confidence     float64 `json:"confidence"`
	HorizonMin     int     `json:"horizon_min"`
	RefPrice       string  `json:"ref_price"`
	RefPriceSource string  `json:"ref_price_source"`
	Execution      *struct {
		Urgency        string `json:"urgency"`
		MaxSlippageBps int    `json:"max_slippage_bps"`
	} `json:"execution,omitempty"`
	Exits *struct {
		StopLossPct   string `json:"stop_loss_pct"`
		TakeProfitPct string `json:"take_profit_pct"`
	} `json:"exits,omitempty"`
	Rationale json.RawMessage `json:"rationale"`
}

// Reply 是统一的回复体。
type Reply struct {
	Status     string         `json:"status"` // accepted | duplicate | rejected
	ID         string         `json:"id,omitempty"`
	ReceivedAt string         `json:"received_at"`
	Code       string         `json:"code,omitempty"`
	Message    string         `json:"message,omitempty"`
	Details    map[string]any `json:"details,omitempty"`
	Warnings   []Warning      `json:"warnings,omitempty"`
}

type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Error 是带 HTTP 状态和错误码的拒绝。
type Error struct {
	HTTP    int
	Code    string
	Msg     string
	Details map[string]any
}

func (e *Error) Error() string { return e.Code + ": " + e.Msg }

func reject(http int, code, msg string) *Error { return &Error{HTTP: http, Code: code, Msg: msg} }

// ---------- 格式校验 ----------

// 用绝对地址：相对路径会按当前目录解析，中文路径转义后就找不到了
const schemaURL = "https://qs.invalid/qsig-1.schema.json"

type validator struct {
	byType map[string]*jsonschema.Schema
}

func newValidator() (*validator, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaJSON))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(schemaURL, doc); err != nil {
		return nil, err
	}
	v := &validator{byType: map[string]*jsonschema.Schema{}}
	for _, t := range []string{"signal", "heartbeat", "halt"} {
		s, err := c.Compile(schemaURL + "#/$defs/" + t)
		if err != nil {
			return nil, err
		}
		v.byType[t] = s
	}
	return v, nil
}

// validate 按消息类型选对应的子 Schema 校验，返回第一个具体出错的位置（JSON Pointer）。
func (v *validator) validate(typ string, body []byte) *Error {
	s, ok := v.byType[typ]
	if !ok {
		e := reject(400, "SCHEMA_VIOLATION", "type 必须是 signal、heartbeat 或 halt")
		e.Details = map[string]any{"path": "/type"}
		return e
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err != nil {
		return reject(400, "INVALID_JSON", "不是合法的 JSON")
	}
	if err := s.Validate(inst); err != nil {
		path := "/"
		if ve, ok := err.(*jsonschema.ValidationError); ok {
			leaf := ve
			for len(leaf.Causes) > 0 {
				leaf = leaf.Causes[0]
			}
			if len(leaf.InstanceLocation) > 0 {
				path = "/" + strings.Join(leaf.InstanceLocation, "/")
			}
			e := reject(400, "SCHEMA_VIOLATION", leaf.Error())
			e.Details = map[string]any{"path": path}
			return e
		}
		e := reject(400, "SCHEMA_VIOLATION", err.Error())
		e.Details = map[string]any{"path": path}
		return e
	}
	return nil
}

// ---------- 签名 ----------

// Sign 计算签名：HMAC-SHA256(secret, timestamp + "\n" + METHOD + "\n" + path(含查询参数) + "\n" + body)，小写十六进制。
func Sign(secret string, ts int64, method, path string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(m, "%d\n%s\n%s\n", ts, strings.ToUpper(method), path)
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

func verify(secret string, ts int64, method, path string, body []byte, sig string) bool {
	want := Sign(secret, ts, method, path, body)
	return hmac.Equal([]byte(want), []byte(strings.ToLower(sig)))
}

func parseTime(s string) (time.Time, error) { return time.Parse(TimeLayout, s) }

func dec(s string) decimal.Decimal {
	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Zero
	}
	return d
}
