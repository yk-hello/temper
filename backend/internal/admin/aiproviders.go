package admin

// AI 复盘点评的厂商（D112，2026-11-23）。用户："主要就是自定义厂家，你可以参考 ccswitch 设计""其实只用做自定义厂商就好了"。
// 部署的人在"系统"页里加几家：名字、接口格式、地址、密钥、模型、思考程度；同一时间用其中一家，每家能单独测试。
//   - 和其他系统设置一样：只有所有者能看能改，保存、切换、删除要二级密码，密钥用主密钥加密存，界面只显示头尾几位
//   - 列表空着、或者选了"用配置文件"：用配置文件里的 AI_*（原来的写法照样能用）
//   - 原来网页里存的那一套（ai.base / ai.key / ai.model / ai.format）第一次启动时自动搬成列表里的第一家

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"qs/internal/auth"
)

// AIProvider 一家厂商。Key 在内存里是明文，存库时加密，不回给前端。
type AIProvider struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Format string `json:"format"` // openai = OpenAI 兼容接口；anthropic = Claude 自己的接口
	Base   string `json:"base"`   // anthropic 可以不填（官方地址）
	Model  string `json:"model"`  // anthropic 可以不填（默认模型）
	Effort string `json:"effort"` // 思考程度：空 = 默认（不传，厂商自己定）；none / low / medium / high / xhigh
	Key    string `json:"key,omitempty"`
}

// AITest 测试的结果。
type AITest struct {
	Model  string `json:"model"`
	Reply  string `json:"reply"`
	Tokens int    `json:"tokens"`
}

// 思考程度只收这几档（各家的翻译在 aireview 里）。
var aiEfforts = map[string]bool{"": true, "none": true, "low": true, "medium": true, "high": true, "xhigh": true}

const (
	aiStoreKey = "ai_providers"
	aiMax      = 20 // 最多几家
)

type aiStore struct {
	Active string       `json:"active"` // 用哪一家；空 = 用配置文件里的
	List   []AIProvider `json:"list"`
}

// AIActive 现在用的那一家（复制一份）；没有就是 nil（用配置文件里的）。
func (s *Sys) AIActive() *AIProvider {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.ai.List {
		if p.ID == s.ai.Active {
			c := p
			return &c
		}
	}
	return nil
}

// loadAI 读厂商列表；第一次时把原来网页里存的那一套搬进来（raw 是 Load 读到的原始设置，密钥还是加密的）。
// 返回 true 表示搬过了，调用的地方要把旧的 ai.* 从系统设置里去掉，免得删光以后又被搬回来。
func (s *Sys) loadAI(ctx context.Context, raw map[string]string) (bool, error) {
	var st aiStore
	found, err := s.S.get(ctx, aiStoreKey, &st)
	if err != nil {
		return false, err
	}
	for i := range st.List {
		if st.List[i].Key == "" {
			continue
		}
		plain, err := s.open(st.List[i].Key)
		if err != nil {
			st.List[i].Key = "" // 主密钥换过：这家要重新填密钥
			continue
		}
		st.List[i].Key = plain
	}
	moved := false
	if !found && raw["ai.key"] != "" {
		key, err := s.open(raw["ai.key"])
		if err == nil {
			p := AIProvider{ID: newAIID(), Format: "openai", Base: raw["ai.base"], Model: raw["ai.model"], Key: key}
			if raw["ai.format"] == "anthropic" {
				p.Format = "anthropic"
			}
			p.Name = defaultAIName(p)
			st = aiStore{Active: p.ID, List: []AIProvider{p}}
			if err := s.saveAI(ctx, st); err != nil {
				return false, err
			}
			moved = true
		}
	}
	s.mu.Lock()
	s.ai = st
	s.mu.Unlock()
	return moved, nil
}

func (s *Sys) saveAI(ctx context.Context, st aiStore) error {
	out := aiStore{Active: st.Active, List: make([]AIProvider, len(st.List))}
	for i, p := range st.List {
		if p.Key != "" {
			box, err := s.C.Seal(p.Key)
			if err != nil {
				return err
			}
			p.Key = "enc:" + base64.StdEncoding.EncodeToString(box)
		}
		out.List[i] = p
	}
	return s.S.put(ctx, aiStoreKey, out)
}

func newAIID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// defaultAIName 没起名字时：Claude，或者接口地址的域名。
func defaultAIName(p AIProvider) string {
	if p.Format == "anthropic" && (p.Base == "" || strings.Contains(p.Base, "anthropic.com")) {
		return "Claude"
	}
	if u, err := url.Parse(p.Base); err == nil && u.Host != "" {
		return strings.TrimPrefix(u.Host, "api.")
	}
	return "AI"
}

// checkAI 检查一家填得对不对（只查明显的错）。
func checkAI(p AIProvider) string {
	switch {
	case p.Format != "openai" && p.Format != "anthropic":
		return "format"
	case utf8.RuneCountInString(p.Name) > 40:
		return "name"
	case p.Base != "" && !strings.HasPrefix(p.Base, "https://") && !strings.HasPrefix(p.Base, "http://"):
		return "base"
	case p.Format == "openai" && p.Base == "":
		return "base"
	case p.Format == "openai" && p.Model == "":
		return "model"
	case len(p.Model) > 120 || strings.ContainsAny(p.Model, " \t\n"):
		return "model"
	case !aiEfforts[p.Effort]:
		return "effort"
	case len(p.Base) > 400 || len(p.Key) > 4000:
		return "base"
	}
	return ""
}

func (a *API) aiRoutes(adm func(string, func(http.ResponseWriter, *http.Request, *auth.User))) {
	owner := func(fn func(http.ResponseWriter, *http.Request, *auth.User)) func(http.ResponseWriter, *http.Request, *auth.User) {
		return func(w http.ResponseWriter, r *http.Request, u *auth.User) {
			if a.Sys == nil {
				fail(w, errBad)
				return
			}
			fn(w, r, u)
		}
	}
	adm("GET /api/admin/ai", owner(a.aiList))
	adm("POST /api/admin/ai", owner(a.aiSave))
	adm("POST /api/admin/ai/use", owner(a.aiUse))
	adm("POST /api/admin/ai/delete", owner(a.aiDelete))
	adm("POST /api/admin/ai/test", owner(a.aiTest))
	adm("POST /api/admin/ai/models", owner(a.aiModels))
}

func (a *API) aiList(w http.ResponseWriter, _ *http.Request, _ *auth.User) {
	type row struct {
		AIProvider
		KeyHint string `json:"key_hint"`
	}
	s := a.Sys
	s.mu.RLock()
	list := make([]row, 0, len(s.ai.List))
	for _, p := range s.ai.List {
		h := ""
		if p.Key != "" {
			h = hint(p.Key)
		}
		p.Key = ""
		list = append(list, row{p, h})
	}
	active := s.ai.Active
	s.mu.RUnlock()
	// 配置文件里有没有配（列表空着、或者选了"用配置文件"时用它）
	env := map[string]string{}
	if s.env("AI_API_KEY") != "" || s.env("DEEPSEEK_API_KEY") != "" {
		env["format"] = "openai"
		if s.env("AI_FORMAT") == "anthropic" {
			env["format"] = "anthropic"
		}
		env["model"] = s.env("AI_MODEL")
		env["effort"] = s.env("AI_EFFORT")
	}
	write(w, 200, map[string]any{"active": active, "list": list, "env": env, "crypt": s.C != nil && s.C.Enabled()})
}

// aiSave 加一家或改一家。密钥空着 = 不改（新加的必须填）。第一家加进来时直接用它。
func (a *API) aiSave(w http.ResponseWriter, r *http.Request, u *auth.User) {
	var in struct {
		AIProvider
		Key           string `json:"key"`
		TradePassword string `json:"trade_password"`
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
	p := in.AIProvider
	p.Name, p.Base, p.Model = strings.TrimSpace(p.Name), strings.TrimRight(strings.TrimSpace(p.Base), "/"), strings.TrimSpace(p.Model)
	p.Key = strings.ReplaceAll(strings.TrimSpace(in.Key), " ", "")
	s := a.Sys
	s.mu.Lock()
	st := aiStore{Active: s.ai.Active, List: append([]AIProvider(nil), s.ai.List...)}
	s.mu.Unlock()
	idx := -1
	for i, x := range st.List {
		if x.ID == p.ID && p.ID != "" {
			idx = i
		}
	}
	if idx >= 0 && p.Key == "" {
		p.Key = st.List[idx].Key
	}
	if p.Key == "" {
		write(w, 400, map[string]any{"code": "AI_NO_KEY"})
		return
	}
	if p.Name == "" {
		p.Name = defaultAIName(p)
	}
	if bad := checkAI(p); bad != "" {
		write(w, 400, map[string]any{"code": "BAD_AI", "params": map[string]string{"key": bad}})
		return
	}
	if idx >= 0 {
		st.List[idx] = p
	} else {
		if len(st.List) >= aiMax {
			fail(w, &auth.Err{HTTP: 400, Code: "TOO_MANY"})
			return
		}
		p.ID = newAIID()
		st.List = append(st.List, p)
		if len(st.List) == 1 {
			st.Active = p.ID
		}
	}
	if !a.aiCommit(w, r, u, st, "ai.save:"+p.ID) {
		return
	}
	write(w, 200, map[string]any{"ok": true, "id": p.ID})
}

// aiUse 切换用哪一家。id 空 = 用配置文件里的。
func (a *API) aiUse(w http.ResponseWriter, r *http.Request, u *auth.User) {
	var in struct {
		ID            string `json:"id"`
		TradePassword string `json:"trade_password"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if err := a.Users.CheckTradePassword(u, in.TradePassword); err != nil {
		fail(w, err)
		return
	}
	s := a.Sys
	s.mu.Lock()
	st := aiStore{Active: in.ID, List: append([]AIProvider(nil), s.ai.List...)}
	s.mu.Unlock()
	ok := in.ID == ""
	for _, p := range st.List {
		if p.ID == in.ID {
			ok = true
		}
	}
	if !ok {
		fail(w, &auth.Err{HTTP: 404, Code: "NOT_FOUND"})
		return
	}
	if !a.aiCommit(w, r, u, st, "ai.use:"+in.ID) {
		return
	}
	write(w, 200, map[string]bool{"ok": true})
}

// aiDelete 删一家。删的是正在用的，换成剩下的第一家（一家不剩就用配置文件里的）。
func (a *API) aiDelete(w http.ResponseWriter, r *http.Request, u *auth.User) {
	var in struct {
		ID            string `json:"id"`
		TradePassword string `json:"trade_password"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if err := a.Users.CheckTradePassword(u, in.TradePassword); err != nil {
		fail(w, err)
		return
	}
	s := a.Sys
	s.mu.Lock()
	st := aiStore{Active: s.ai.Active}
	for _, p := range s.ai.List {
		if p.ID != in.ID {
			st.List = append(st.List, p)
		}
	}
	n := len(s.ai.List)
	s.mu.Unlock()
	if len(st.List) == n {
		fail(w, &auth.Err{HTTP: 404, Code: "NOT_FOUND"})
		return
	}
	if st.Active == in.ID {
		st.Active = ""
		if len(st.List) > 0 {
			st.Active = st.List[0].ID
		}
	}
	if !a.aiCommit(w, r, u, st, "ai.delete:"+in.ID) {
		return
	}
	write(w, 200, map[string]bool{"ok": true})
}

// aiCommit 存库、换上、让 AI 点评马上用新的、记操作记录（只记做了什么，不记密钥）。
func (a *API) aiCommit(w http.ResponseWriter, r *http.Request, u *auth.User, st aiStore, what string) bool {
	s := a.Sys
	if err := s.saveAI(r.Context(), st); err != nil {
		fail(w, err)
		return false
	}
	s.mu.Lock()
	s.ai = st
	s.mu.Unlock()
	if s.Apply != nil {
		s.Apply("ai")
	}
	a.record(r.Context(), u, "settings", what, nil)
	return true
}

// aiDraft 测试、获取模型列表用的那一家：可以是已经存的（只给 id），也可以是还没存的草稿（密钥空着就用存的那家的）。
func (a *API) aiDraft(w http.ResponseWriter, r *http.Request) (AIProvider, bool, bool) {
	var in struct {
		AIProvider
		Key string `json:"key"`
		Env bool   `json:"env"` // 配置文件里的
	}
	if !readJSON(w, r, &in) {
		return AIProvider{}, false, false
	}
	s := a.Sys
	p := in.AIProvider
	p.Base, p.Model = strings.TrimRight(strings.TrimSpace(p.Base), "/"), strings.TrimSpace(p.Model)
	p.Key = strings.ReplaceAll(strings.TrimSpace(in.Key), " ", "")
	if p.ID != "" && p.Key == "" {
		s.mu.RLock()
		for _, x := range s.ai.List {
			if x.ID == p.ID {
				p.Key = x.Key
				if p.Format == "" { // 只给了 id：整家拿存的
					p = x
				}
			}
		}
		s.mu.RUnlock()
	}
	return p, in.Env, true
}

// aiTest 让它回一句。
func (a *API) aiTest(w http.ResponseWriter, r *http.Request, _ *auth.User) {
	if a.Sys.TestAI == nil {
		fail(w, errBad)
		return
	}
	p, env, ok := a.aiDraft(w, r)
	if !ok {
		return
	}
	if !env {
		if p.Key == "" {
			write(w, 200, map[string]any{"ok": false, "code": "AI_NO_KEY"})
			return
		}
		if bad := checkAI(p); bad != "" {
			write(w, 200, map[string]any{"ok": false, "code": "BAD_AI", "params": map[string]string{"key": bad}})
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	res, err := a.Sys.TestAI(ctx, p, env)
	if err != nil {
		write(w, 200, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	write(w, 200, map[string]any{"ok": true, "model": res.Model, "reply": res.Reply, "tokens": res.Tokens})
}

// aiModels 问这家有哪些模型，给表单里挑（11-24）。只要格式、地址（OpenAI 兼容要填）、密钥，模型名不用填。
func (a *API) aiModels(w http.ResponseWriter, r *http.Request, _ *auth.User) {
	if a.Sys.ListAI == nil {
		fail(w, errBad)
		return
	}
	p, _, ok := a.aiDraft(w, r)
	if !ok {
		return
	}
	if p.Key == "" {
		write(w, 200, map[string]any{"ok": false, "code": "AI_NO_KEY"})
		return
	}
	if p.Model == "" {
		p.Model = "x" // 模型名还没选：只查格式和地址
	}
	if bad := checkAI(p); bad != "" {
		write(w, 200, map[string]any{"ok": false, "code": "BAD_AI", "params": map[string]string{"key": bad}})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	ids, err := a.Sys.ListAI(ctx, p)
	if err != nil {
		write(w, 200, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	write(w, 200, map[string]any{"ok": true, "models": ids})
}
