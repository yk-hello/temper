package notify

// 自定义提醒条件（18 第 10.4 条，D102，借摩根士丹利 hobbes"规则让人在运行时自己配"的想法）。
// 规则只是数据：选一个币，最多 4 个条件（字段、高于或低于、一个数），"全部满足"或"任一满足"。不能写代码、不能执行任何东西。
// 条件从不满足变成满足时响一次，然后冷静一段时间（默认 1 小时）；选了"只提醒一次"的响完就停。只提醒，不下单。
// 数据用筛选器算好的那份（币安合约，全部品种）：价格、5 分钟 / 1 小时 / 24 小时涨跌、资金费、持仓量 1 / 24 小时变化、成交额倍数。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	KRule    = "rule_alert"
	MaxConds = 4 // 一条提醒最多几个条件（界面按这个设计，不开放修改）
)

// MaxRules 每人最多几条条件提醒；部署的人可以改：QS_LIMIT_RULES（D103）
var MaxRules = 20

// RuleFields 能用的字段。值的单位：px 是价格；chg5m、chg1h、chg24h、funding、oi1h、oi24h 是百分比（1 = 1%）；rv 是倍数。
var RuleFields = []string{"px", "chg5m", "chg1h", "chg24h", "funding", "oi1h", "oi24h", "rv"}

// Cond 一个条件。
type Cond struct {
	Field string  `json:"field"`
	Op    string  `json:"op"` // > 高于 | < 低于
	Value float64 `json:"value"`
}

// Rule 一条提醒规则。
type Rule struct {
	ID       int64  `json:"id"`
	User     int64  `json:"-"`
	Name     string `json:"name"`
	Inst     string `json:"instrument"`
	Join     string `json:"join"` // all | any
	Conds    []Cond `json:"conds"`
	Once     bool   `json:"once"`
	Cooldown int    `json:"cooldown"` // 分钟
	Created  int64  `json:"created"`
	LastHit  int64  `json:"last_hit"`
	Off      bool   `json:"off"`
	// Now 只在返回给界面时填：此刻已经满足（所以要等它先不满足、再满足才响）。存库时是空的，不写进去。
	// 11-17 走查：加了一条一加就满足的规则，界面只写"还没响过"，像是坏了。
	Now    *bool `json:"now,omitempty"`
	active bool  // 上一次看的时候满足不满足（只在从不满足变成满足时响）
}

// ErrRule 规则不对：Code 给界面翻译。
type ErrRule struct{ Code string }

func (e *ErrRule) Error() string { return e.Code }

func validField(f string) bool {
	for _, x := range RuleFields {
		if x == f {
			return true
		}
	}
	return false
}

// normalize 检查并补默认值。
func normalize(r *Rule) error {
	r.Name = strings.TrimSpace(r.Name)
	if utf8.RuneCountInString(r.Name) > 40 {
		return &ErrRule{"RULE_NAME_LONG"}
	}
	r.Inst = strings.TrimSpace(r.Inst)
	if r.Inst == "" || len(r.Inst) > 40 {
		return &ErrRule{"UNKNOWN_INSTRUMENT"}
	}
	if len(r.Conds) == 0 || len(r.Conds) > MaxConds {
		return &ErrRule{"RULE_CONDS"}
	}
	for _, c := range r.Conds {
		if !validField(c.Field) || (c.Op != ">" && c.Op != "<") || math.IsNaN(c.Value) || math.IsInf(c.Value, 0) {
			return &ErrRule{"RULE_BAD_COND"}
		}
		if c.Field == "px" && c.Value <= 0 {
			return &ErrRule{"RULE_BAD_COND"}
		}
	}
	if r.Join != "any" {
		r.Join = "all"
	}
	if r.Cooldown == 0 {
		r.Cooldown = 60
	}
	if r.Cooldown < 5 || r.Cooldown > 1440 {
		return &ErrRule{"RULE_COOLDOWN"}
	}
	return nil
}

// attachRules 建表、读回来。只在 Attach 里调用，那时 s.mu 已经锁着（这里不要再锁，会自己等自己，启动卡死）。
func (s *Service) attachRules(ctx context.Context) error {
	if s.Pool == nil {
		return nil
	}
	if _, err := s.Pool.Exec(ctx, `create table if not exists alert_rules (id bigserial primary key, user_id bigint not null, body jsonb not null, created bigint not null)`); err != nil {
		return err
	}
	rows, err := s.Pool.Query(ctx, `select id, user_id, body from alert_rules`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, user int64
		var raw []byte
		if rows.Scan(&id, &user, &raw) != nil {
			continue
		}
		r := &Rule{}
		if json.Unmarshal(raw, r) != nil {
			continue
		}
		r.ID, r.User = id, user
		r.active = true // 重启后第一次看：已经满足的不当成"刚变成满足"，免得重启就响一遍
		s.rules[id] = r
	}
	return rows.Err()
}

func (s *Service) saveRule(ctx context.Context, r *Rule) {
	if s.Pool == nil {
		return
	}
	raw, _ := json.Marshal(r)
	_, _ = s.Pool.Exec(ctx, `update alert_rules set body = $2 where id = $1`, r.ID, raw)
}

// AddRule 加一条。
func (s *Service) AddRule(ctx context.Context, user int64, r Rule) (*Rule, error) {
	if err := normalize(&r); err != nil {
		return nil, err
	}
	s.mu.Lock()
	n := 0
	for _, x := range s.rules {
		if x.User == user {
			n++
		}
	}
	s.mu.Unlock()
	if n >= MaxRules {
		return nil, &ErrRule{"TOO_MANY_RULES"}
	}
	r.User, r.Created, r.LastHit, r.Off = user, s.now(), 0, false
	r.active = s.satisfiedNow(&r) // 加的时候已经满足的不响（不然一加就响，像是吓人）；不满足的，之后一满足就响
	if s.Pool != nil {
		raw, _ := json.Marshal(r)
		if err := s.Pool.QueryRow(ctx, `insert into alert_rules (user_id, body, created) values ($1, $2, $3) returning id`, user, raw, r.Created).Scan(&r.ID); err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	if s.Pool == nil {
		s.nextID++
		r.ID = s.nextID
	}
	cp := r
	s.rules[r.ID] = &cp
	s.mu.Unlock()
	now := r.active
	r.Now = &now
	return &r, nil
}

// Rules 一个人的全部规则。
func (s *Service) Rules(user int64) []Rule {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Rule{}
	for _, r := range s.rules {
		if r.User == user {
			cp := *r
			now := r.active
			cp.Now = &now
			out = append(out, cp)
		}
	}
	return out
}

// DelRule 删一条（只能删自己的）。
func (s *Service) DelRule(ctx context.Context, user, id int64) bool {
	s.mu.Lock()
	r := s.rules[id]
	if r == nil || r.User != user {
		s.mu.Unlock()
		return false
	}
	delete(s.rules, id)
	s.mu.Unlock()
	if s.Pool != nil {
		_, _ = s.Pool.Exec(ctx, `delete from alert_rules where id = $1 and user_id = $2`, id, user)
	}
	return true
}

// SetRuleOff 暂停或恢复。恢复时当成"已经满足"，等它重新变成满足才响。
func (s *Service) SetRuleOff(ctx context.Context, user, id int64, off bool) bool {
	s.mu.Lock()
	r := s.rules[id]
	if r == nil || r.User != user {
		s.mu.Unlock()
		return false
	}
	r.Off = off
	r.active = s.satisfiedNow(r) // 恢复时已经满足的不响，不满足的之后一满足就响
	cp := *r
	s.mu.Unlock()
	s.saveRule(ctx, &cp)
	return true
}

// satisfiedNow 这条规则此刻满不满足，作为"从不满足变成满足才响"的起点（D103 时修：原来一律当成满足，
// 加完规则马上碰上价格穿过线时，第一次检查看到"满足"会当成"加的时候就满足"，这一次就不响了）。
// 拿不到数字时当成满足（保守）。调用时可能已经拿着 s.mu，这里不加锁。
func (s *Service) satisfiedNow(r *Rule) bool {
	if s.RuleMetrics == nil {
		return true
	}
	m := s.RuleMetrics(r.Inst)
	if m == nil {
		return true
	}
	ok, _ := evalRule(r, m)
	return ok
}

// evalRule 算一条规则现在满不满足，以及每个条件此刻的值（取不到的条件算不满足）。
func evalRule(r *Rule, m map[string]float64) (bool, []float64) {
	vals := make([]float64, len(r.Conds))
	all, any := true, false
	for i, c := range r.Conds {
		v, ok := m[c.Field]
		vals[i] = v
		hit := ok && ((c.Op == ">" && v > c.Value) || (c.Op == "<" && v < c.Value))
		all = all && hit
		any = any || hit
	}
	if r.Join == "any" {
		return any, vals
	}
	return all, vals
}

// CheckRules 看一遍所有规则。metrics(品种) 给这个品种此刻的各项数字（单位见 RuleFields），取不到返回 nil。
func (s *Service) CheckRules(ctx context.Context, metrics func(inst string) map[string]float64) {
	type hit struct {
		r    Rule
		vals []float64
	}
	var hits []hit
	now := s.now()
	s.mu.Lock()
	for _, r := range s.rules {
		if r.Off {
			continue
		}
		m := metrics(r.Inst)
		if m == nil {
			continue
		}
		ok, vals := evalRule(r, m)
		if ok && !r.active && now-r.LastHit >= int64(r.Cooldown)*60_000 {
			r.LastHit = now
			if r.Once {
				r.Off = true
			}
			hits = append(hits, hit{*r, vals})
		}
		r.active = ok
	}
	s.mu.Unlock()
	for _, h := range hits {
		s.saveRule(ctx, &h.r)
		parts := make([]string, len(h.r.Conds))
		for i, c := range h.r.Conds {
			parts[i] = c.Field + c.Op + trimNum(c.Value) + "=" + sig6(h.vals[i])
		}
		s.Push(ctx, h.r.User, KRule, map[string]string{"inst": h.r.Inst, "name": h.r.Name, "conds": strings.Join(parts, ";"), "join": h.r.Join})
	}
}

// ruleFieldName 通知里的字段名。
var ruleFieldZh = map[string]string{"px": "价格", "chg5m": "5 分钟涨跌", "chg1h": "1 小时涨跌", "chg24h": "24 小时涨跌", "funding": "资金费", "oi1h": "持仓量 1 小时变化", "oi24h": "持仓量 24 小时变化", "rv": "成交额倍数"}
var ruleFieldEn = map[string]string{"px": "price", "chg5m": "5m change", "chg1h": "1h change", "chg24h": "24h change", "funding": "funding", "oi1h": "OI 1h change", "oi24h": "OI 24h change", "rv": "volume multiple"}

// ruleText 条件提醒的那句话：每个条件此刻的值和设的线。
func ruleText(en bool, coin string, p map[string]string) string {
	var parts []string
	for _, c := range strings.Split(p["conds"], ";") {
		eq := strings.SplitN(c, "=", 2)
		if len(eq) != 2 {
			continue
		}
		op := ">"
		if strings.Contains(eq[0], "<") {
			op = "<"
		}
		fv := strings.SplitN(eq[0], op, 2)
		if len(fv) != 2 {
			continue
		}
		field, line := fv[0], fv[1]
		now, _ := strconv.ParseFloat(eq[1], 64)
		unit := "%"
		var cur string
		switch field {
		case "px":
			unit, cur = "", strconv.FormatFloat(now, 'f', -1, 64)
		case "rv":
			unit, cur = "×", strconv.FormatFloat(now, 'f', 1, 64)
		case "funding":
			cur = strconv.FormatFloat(now, 'f', 4, 64)
		default:
			cur = strconv.FormatFloat(now, 'f', 2, 64)
		}
		if en {
			word := "above"
			if op == "<" {
				word = "below"
			}
			parts = append(parts, fmt.Sprintf("%s %s%s (%s %s%s)", ruleFieldEn[field], cur, unit, word, line, unit))
		} else {
			word := "高于"
			if op == "<" {
				word = "低于"
			}
			parts = append(parts, fmt.Sprintf("%s %s%s（%s %s%s）", ruleFieldZh[field], cur, unit, word, line, unit))
		}
	}
	name := p["name"]
	if en {
		if name == "" {
			name = "Condition alert"
		}
		return fmt.Sprintf("%s · %s: %s", name, coin, strings.Join(parts, "; "))
	}
	if name == "" {
		name = "条件提醒"
	}
	return fmt.Sprintf("%s · %s：%s", name, coin, strings.Join(parts, "；"))
}

// ruleRoutes 规则的增删查、暂停恢复。
func (s *Service) ruleRoutes(r func(pattern string, fn func(w http.ResponseWriter, q *http.Request, uid int64)), wj func(w http.ResponseWriter, code int, v any)) {
	r("GET /api/notify/rules", func(w http.ResponseWriter, _ *http.Request, uid int64) {
		wj(w, 200, map[string]any{"rules": s.Rules(uid), "fields": RuleFields, "max": MaxRules})
	})
	r("POST /api/notify/rules", func(w http.ResponseWriter, q *http.Request, uid int64) {
		var in Rule
		if err := json.NewDecoder(http.MaxBytesReader(w, q.Body, 8<<10)).Decode(&in); err != nil {
			wj(w, 400, map[string]string{"code": "BAD_REQUEST"})
			return
		}
		rule, err := s.AddRule(q.Context(), uid, in)
		var re *ErrRule
		if errors.As(err, &re) {
			wj(w, 422, map[string]string{"code": re.Code})
			return
		}
		if err != nil {
			wj(w, 500, map[string]string{"code": "INTERNAL_ERROR"})
			return
		}
		wj(w, 200, rule)
	})
	r("DELETE /api/notify/rules/{id}", func(w http.ResponseWriter, q *http.Request, uid int64) {
		id, _ := strconv.ParseInt(q.PathValue("id"), 10, 64)
		if !s.DelRule(q.Context(), uid, id) {
			wj(w, 404, map[string]string{"code": "NOT_FOUND"})
			return
		}
		wj(w, 200, map[string]bool{"ok": true})
	})
	r("POST /api/notify/rules/{id}/off", func(w http.ResponseWriter, q *http.Request, uid int64) {
		id, _ := strconv.ParseInt(q.PathValue("id"), 10, 64)
		var in struct {
			Off bool `json:"off"`
		}
		_ = json.NewDecoder(http.MaxBytesReader(w, q.Body, 1<<10)).Decode(&in)
		if !s.SetRuleOff(q.Context(), uid, id, in.Off) {
			wj(w, 404, map[string]string{"code": "NOT_FOUND"})
			return
		}
		wj(w, 200, map[string]bool{"ok": true})
	})
}

// sig6 此刻的值留 6 位有效数字（模拟行情、成交均价会有很长的小数），去掉末尾的 0。
func sig6(v float64) string {
	if v == 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return "0"
	}
	d := 5 - int(math.Floor(math.Log10(math.Abs(v))))
	d = max(0, min(d, 10))
	s := strconv.FormatFloat(v, 'f', d, 64)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}
