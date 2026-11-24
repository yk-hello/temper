package admin

// 系统设置（2026-11-15，用户："别人用不能也要当场写吧？""别人不能全靠AI配置吧，比如其他的AI复盘API填写"）：
// 部署的人在管理后台填发信邮箱、AI 复盘点评、Telegram、数据源密钥，不用改配置文件、不用重启。
//   - 只有所有者能看能改（里面是整套系统的密钥），保存要二级密码，记操作记录（只记改了哪几项，不记值）
//   - 密钥用主密钥（QS_MASTER_KEY，和交易所密钥同一把）加密存进数据库，界面只显示头尾几位
//   - 网页里填的优先；没填的用配置文件（deploy/.env）里的，原来的写法照样能用
//   - 每一组有"测试"：发一封测试邮件、让 AI 回一句、查 Telegram 机器人名字、试取一次数据

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/mail"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"qs/internal/auth"
)

// Field 一项设置。Env 是配置文件里对应的变量（网页没填时用它）；几个名字用逗号隔开时，按顺序取第一个有值的。
type Field struct {
	Key    string `json:"key"`
	Group  string `json:"group"`
	Env    string `json:"env"`
	Secret bool   `json:"secret"`
}

// Fields 能在管理后台改的设置。分组：mail 发信邮箱、tg Telegram、data 数据源密钥。AI 复盘点评是一张厂商列表，单独存（aiproviders.go，D112）。
var Fields = []Field{
	{"mail.host", "mail", "SMTP_HOST", false},
	{"mail.port", "mail", "SMTP_PORT", false},
	{"mail.user", "mail", "SMTP_USER", false},
	{"mail.pass", "mail", "SMTP_PASSWORD", true},
	{"mail.from_name", "mail", "MAIL_FROM_NAME", false},
	{"tg.token", "tg", "TELEGRAM_BOT_TOKEN", true},
	{"fred.key", "data", "FRED_API_KEY", true},
	{"x.token", "data", "X_BEARER_TOKEN", true},
	{"x.accounts", "data", "X_ACCOUNTS", false},
	{"bn.key", "data", "BINANCE_API_KEY", true},
	{"bn.secret", "data", "BINANCE_API_SECRET", true},
}

func fieldOf(key string) (Field, bool) {
	for _, f := range Fields {
		if f.Key == key {
			return f, true
		}
	}
	return Field{}, false
}

// Crypt 加密（交易所密钥那一把主密钥）。
type Crypt interface {
	Enabled() bool
	Seal(plain string) ([]byte, error)
	Open(box []byte) (string, error)
}

// Sys 系统设置。Apply：某一组变了，main 里把新值交给对应的组件；Test：试一下这一组通不通。
type Sys struct {
	S     *Store
	C     Crypt
	Env   func(name string) string // 读配置文件里的变量（默认 os.Getenv，测试时换掉）
	Apply func(group string)
	Test  func(ctx context.Context, group string, u *auth.User) (string, error)
	// TestAI 试一家 AI 厂商（env = 试配置文件里的）
	TestAI func(ctx context.Context, p AIProvider, env bool) (AITest, error)
	// ListAI 问一家 AI 厂商有哪些模型
	ListAI func(ctx context.Context, p AIProvider) ([]string, error)

	mu  sync.RWMutex
	web map[string]string // 网页里填的（明文只在内存里）
	ai  aiStore           // AI 厂商列表（密钥明文只在内存里）
}

func (s *Sys) env(names string) string {
	for _, name := range strings.Split(names, ",") {
		v := ""
		if s.Env != nil {
			v = strings.TrimSpace(s.Env(name))
		} else {
			v = strings.TrimSpace(os.Getenv(name))
		}
		if v != "" {
			return v
		}
	}
	return ""
}

// Get 现在用的值：网页里填了用网页的，没填用配置文件的。
func (s *Sys) Get(key string) string {
	s.mu.RLock()
	v, ok := s.web[key]
	s.mu.RUnlock()
	if ok {
		return v
	}
	if f, ok := fieldOf(key); ok && f.Env != "" {
		return s.env(f.Env)
	}
	return ""
}

// Source 这一项的值从哪来：web、env，或者空（没配）。
func (s *Sys) Source(key string) string {
	s.mu.RLock()
	_, ok := s.web[key]
	s.mu.RUnlock()
	if ok {
		return "web"
	}
	if f, ok := fieldOf(key); ok && f.Env != "" && s.env(f.Env) != "" {
		return "env"
	}
	return ""
}

// GroupFromWeb 这一组有没有在网页里填过（填过就整组用网页的，不和配置文件混着用）。
func (s *Sys) GroupFromWeb(group string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for k := range s.web {
		if strings.HasPrefix(k, group+".") || (group == "data" && (strings.HasPrefix(k, "fred.") || strings.HasPrefix(k, "x.") || strings.HasPrefix(k, "bn."))) {
			return true
		}
	}
	return false
}

const sysKey = "sys"

// Load 启动时读回网页里填过的（解不开的密钥跳过，记日志的事交给调用方）。
func (s *Sys) Load(ctx context.Context) error {
	raw := map[string]string{}
	if _, err := s.S.get(ctx, sysKey, &raw); err != nil {
		return err
	}
	web := map[string]string{}
	var bad []string
	for k, v := range raw {
		f, ok := fieldOf(k)
		if !ok {
			continue
		}
		if f.Secret {
			plain, err := s.open(v)
			if err != nil {
				bad = append(bad, k)
				continue
			}
			v = plain
		}
		web[k] = v
	}
	s.mu.Lock()
	s.web = web
	s.mu.Unlock()
	moved, err := s.loadAI(ctx, raw)
	if err != nil {
		return err
	}
	if moved { // 原来的 ai.* 已经搬进厂商列表：从系统设置里去掉
		if err := s.save(ctx, web); err != nil {
			return err
		}
	}
	if len(bad) > 0 {
		return errors.New("这几项解不开（主密钥换过？）：" + strings.Join(bad, "、"))
	}
	return nil
}

func (s *Sys) open(v string) (string, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(v, "enc:"))
	if err != nil {
		return "", err
	}
	return s.C.Open(b)
}

func (s *Sys) save(ctx context.Context, web map[string]string) error {
	raw := map[string]string{}
	for k, v := range web {
		f, _ := fieldOf(k)
		if f.Secret {
			box, err := s.C.Seal(v)
			if err != nil {
				return err
			}
			v = "enc:" + base64.StdEncoding.EncodeToString(box)
		}
		raw[k] = v
	}
	return s.S.put(ctx, sysKey, raw)
}

// hint 密钥只显示头尾几位。
func hint(v string) string {
	if len(v) <= 8 {
		return strings.Repeat("•", len(v))
	}
	return v[:4] + "…" + v[len(v)-4:]
}

var errSetting = &auth.Err{HTTP: 400, Code: "BAD_SETTING"}

// validate 检查一项填得对不对（只查明显的错）。
func validate(key, v string) error {
	switch key {
	case "mail.port":
		if p, err := strconv.Atoi(v); err != nil || (p != 25 && p != 465 && p != 587 && p != 2525) {
			return errSetting
		}
	case "mail.user":
		// 要写全：xx@gmail 这种少了 .com 的也拒（ParseAddress 会放过）
		a, err := mail.ParseAddress(v)
		if err != nil || a.Address != v || !strings.Contains(v[strings.LastIndexByte(v, '@')+1:], ".") {
			return errSetting
		}
	case "mail.host":
		if strings.ContainsAny(v, " /:") {
			return errSetting
		}
	}
	if len(v) > 4000 {
		return errSetting
	}
	return nil
}

var groupOf = map[string]string{"mail": "mail", "ai": "ai", "tg": "tg", "fred": "data", "x": "data", "bn": "data"}

func (a *API) sysRoutes(adm func(string, func(http.ResponseWriter, *http.Request, *auth.User))) {
	owner := func(fn func(http.ResponseWriter, *http.Request, *auth.User)) func(http.ResponseWriter, *http.Request, *auth.User) {
		return func(w http.ResponseWriter, r *http.Request, u *auth.User) {
			if a.Sys == nil {
				fail(w, errBad)
				return
			}
			fn(w, r, u)
		}
	}

	adm("GET /api/admin/settings", owner(a.settingsGet))
	adm("POST /api/admin/settings", owner(a.settingsSave))
	adm("POST /api/admin/settings/test", owner(a.settingsTest))
	a.aiRoutes(adm)
}

func (a *API) settingsGet(w http.ResponseWriter, _ *http.Request, _ *auth.User) {
	type row struct {
		Field
		Source string `json:"source"` // web / env / 空
		Value  string `json:"value"`  // 密钥只给头尾几位
	}
	out := make([]row, 0, len(Fields))
	for _, f := range Fields {
		v := a.Sys.Get(f.Key)
		if f.Secret && v != "" {
			v = hint(v)
		}
		out = append(out, row{f, a.Sys.Source(f.Key), v})
	}
	write(w, 200, map[string]any{"fields": out, "crypt": a.Sys.C != nil && a.Sys.C.Enabled()})
}

func (a *API) settingsSave(w http.ResponseWriter, r *http.Request, u *auth.User) {
	var in struct {
		Values        map[string]string `json:"values"` // 密钥空着 = 不改
		Clear         []string          `json:"clear"`  // 清掉网页里填的，回到配置文件里的（或者没有）
		TradePassword string            `json:"trade_password"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if err := a.Users.CheckTradePassword(u, in.TradePassword); err != nil {
		fail(w, err)
		return
	}
	if a.Sys.C == nil || !a.Sys.C.Enabled() {
		fail(w, &auth.Err{HTTP: 400, Code: "NO_MASTER_KEY"})
		return
	}
	a.Sys.mu.RLock()
	web := map[string]string{}
	for k, v := range a.Sys.web {
		web[k] = v
	}
	a.Sys.mu.RUnlock()
	groups := map[string]bool{}
	var keys []string
	for k, v := range in.Values {
		f, ok := fieldOf(k)
		if !ok {
			fail(w, errSetting)
			return
		}
		v = strings.TrimSpace(v)
		if f.Secret {
			v = strings.ReplaceAll(v, " ", "") // Gmail 的应用专用密码显示时带空格
			if v == "" {
				continue
			}
		}
		if v == "" {
			delete(web, k)
		} else {
			if err := validate(k, v); err != nil {
				write(w, 400, map[string]any{"code": "BAD_SETTING", "params": map[string]string{"key": k}})
				return
			}
			web[k] = v
		}
		groups[groupOf[strings.SplitN(k, ".", 2)[0]]] = true
		keys = append(keys, k)
	}
	for _, k := range in.Clear {
		if _, ok := fieldOf(k); ok {
			delete(web, k)
			groups[groupOf[strings.SplitN(k, ".", 2)[0]]] = true
			keys = append(keys, k)
		}
	}
	if err := a.Sys.save(r.Context(), web); err != nil {
		fail(w, err)
		return
	}
	a.Sys.mu.Lock()
	a.Sys.web = web
	a.Sys.mu.Unlock()
	for g := range groups {
		if a.Sys.Apply != nil {
			a.Sys.Apply(g)
		}
	}
	a.record(r.Context(), u, "settings", strings.Join(keys, ","), nil) // 只记改了哪几项，不记值
	write(w, 200, map[string]bool{"ok": true})
}

func (a *API) settingsTest(w http.ResponseWriter, r *http.Request, u *auth.User) {
	var in struct {
		Group string `json:"group"`
	}
	if !readJSON(w, r, &in) || a.Sys.Test == nil {
		fail(w, errBad)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	msg, err := a.Sys.Test(ctx, in.Group, u)
	if err != nil {
		write(w, 200, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	write(w, 200, map[string]any{"ok": true, "message": msg})
}
