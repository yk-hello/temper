package backtest

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"qs/internal/trading"
)

// ParseLines 读信号表：每行"时间,信号"。# 开头的行和空行跳过；第一行是表头也行。
//
//	2026-10-01 08:00,做多 BTC 30%
//	2026-10-01T12:00:00Z,{"action":"close","symbol":"BTCUSDT"}
//	1759305600000,{"instrument":"BTC-PERP","side":"buy","type":"limit","price":"60000","qty":"0.01","sl":"58000"}
//	2026-10-01 08:00,{"instrument":"BTC-PERP","weight":0.3}      目标仓位（qsig/1 的写法：权益的 30% 做多）
//
// 时间：毫秒或秒的时间戳、RFC 3339，或"年-月-日 时:分[:秒]"（按 UTC）。
// 信号：TradingView 警报的写法（一句话或 JSON），或者一张完整的委托（JSON 里有 instrument）。
func ParseLines(text string) ([]Signal, error) {
	var out []Signal
	for n, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		ts, msg, ok := strings.Cut(line, ",")
		if !ok {
			ts, msg, ok = strings.Cut(line, "\t")
		}
		if !ok {
			return nil, &LineError{Line: n + 1, Code: "BT_LINE_FORMAT"}
		}
		t, err := parseTime(strings.TrimSpace(ts))
		if err != nil {
			if len(out) == 0 && n == firstLine(text) { // 表头
				continue
			}
			return nil, &LineError{Line: n + 1, Code: "BT_LINE_TIME"}
		}
		msg = strings.Trim(strings.TrimSpace(msg), `"`)
		msg = strings.ReplaceAll(msg, `""`, `"`) // 表格软件导出的 CSV 把引号写成两个
		s := Signal{T: t}
		if strings.HasPrefix(msg, "{") && strings.Contains(msg, `"weight"`) {
			var tg Target
			if err := json.Unmarshal([]byte(msg), &tg); err != nil || tg.Instrument == "" {
				return nil, &LineError{Line: n + 1, Code: "BT_LINE_ORDER"}
			}
			s.Target = &tg
		} else if strings.HasPrefix(msg, "{") && strings.Contains(msg, `"instrument"`) {
			var o trading.OrderReq
			if err := json.Unmarshal([]byte(msg), &o); err != nil {
				return nil, &LineError{Line: n + 1, Code: "BT_LINE_ORDER"}
			}
			s.Order = &o
		} else {
			s.Text = msg
		}
		out = append(out, s)
		if len(out) > MaxSignals {
			return nil, ErrTooMany
		}
	}
	return out, nil
}

func firstLine(text string) int {
	for n, line := range strings.Split(text, "\n") {
		if l := strings.TrimSpace(line); l != "" && !strings.HasPrefix(l, "#") {
			return n
		}
	}
	return 0
}

func parseTime(s string) (int64, error) {
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		if n < 1e11 { // 秒
			return n * 1000, nil
		}
		return n, nil
	}
	for _, f := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02T15:04:05", "2006-01-02T15:04", "2006/01/02 15:04"} {
		if t, err := time.Parse(f, s); err == nil {
			return t.UnixMilli(), nil
		}
	}
	return 0, fmt.Errorf("time")
}

// LineError 信号表第几行有问题。
type LineError struct {
	Line int
	Code string
}

func (e *LineError) Error() string { return fmt.Sprintf("%s (line %d)", e.Code, e.Line) }
