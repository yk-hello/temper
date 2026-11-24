package main

// 系统设置接到各个组件（admin/settings.go，2026-11-15）：网页里改了哪一组，就把新值交给对应的组件，马上生效；
// "测试"按钮各做一件真的事：发一封测试邮件、让 AI 回一句、问 Telegram 机器人叫什么、试取一次数据。
// 出错的话不带密钥（错误里如果有地址，地址里的密钥换成 ***）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"qs/internal/admin"
	"qs/internal/aireview"
	"qs/internal/auth"
	"qs/internal/calendar"
	"qs/internal/config"
	"qs/internal/mailer"
	"qs/internal/news"
	"qs/internal/notify"
	"qs/internal/scan"
	"qs/internal/trading"
)

type sysDeps struct {
	cfg     config.Config
	mail    *mailer.Live
	reviews *aireview.Service
	tg      *notify.Telegram
	cal     *calendar.Service
	scanPub *scan.Public // 模拟行情时为空
	news    *news.Service
	specs   *trading.Specs
	log     *slog.Logger
}

func newSys(ctx context.Context, store *admin.Store, crypt admin.Crypt, d sysDeps) *admin.Sys {
	sys := &admin.Sys{S: store, C: crypt, TestAI: testAI(d.cfg), ListAI: func(ctx context.Context, p admin.AIProvider) ([]string, error) {
		ids, err := aireview.ListModels(ctx, p.Format, p.Key, p.Base, nil)
		if err != nil {
			return nil, friendly(hideKey(err, p.Key))
		}
		if len(ids) == 0 {
			return nil, errors.New("这家没有列出模型：直接手填模型名")
		}
		return ids, nil
	}}
	var lastBN string
	sys.Apply = func(group string) {
		switch group {
		case "mail":
			d.mail.Set(mailCfg(sys, d.cfg.BrandName), map[bool]string{true: "web", false: "env"}[sys.GroupFromWeb("mail")])
		case "ai":
			d.reviews.SetProvider(aiProvider(sys, d.cfg))
		case "tg":
			d.tg.SetToken(sys.Get("tg.token"))
		case "data":
			fred := sys.Get("fred.key")
			d.cal.SetFREDKey(fred)
			if d.scanPub != nil {
				d.scanPub.SetFREDKey(fred)
			}
			d.news.SetX(sys.Get("x.token"), splitAccounts(sys.Get("x.accounts")))
			// 币安只读密钥：用来读真实的保证金分档。换了才重新读
			if k, s := sys.Get("bn.key"), sys.Get("bn.secret"); k != "" && s != "" && k+s != lastBN {
				lastBN = k + s
				go func() {
					c, cancel := context.WithTimeout(ctx, 30*time.Second)
					defer cancel()
					if n, err := d.specs.FetchBrackets(c, d.cfg.BinanceBase, k, s); err != nil {
						d.log.Warn("保证金分档取不到", "err", hideKey(err, k, s))
					} else {
						d.log.Info("保证金分档已更新", "n", n)
					}
				}()
			}
		}
	}
	sys.Test = func(ctx context.Context, group string, u *auth.User) (string, error) {
		switch group {
		case "mail":
			if !d.mail.Enabled() {
				return "", errors.New("还没填齐：发信服务器、邮箱、授权码都要填")
			}
			if u.Email == "" {
				return "", errors.New("你的账号没有绑邮箱：先在账户页的\"账户名和邮箱\"里绑一个，测试邮件发到那里")
			}
			err := d.mail.Send(ctx, u.Email, "测试邮件：发信邮箱已经能用了",
				"这是一封测试邮件。\n\n收到它说明发信邮箱配好了：注册验证码、忘记密码、强平预警、对账告警这些都会从这个邮箱发出。\n在账户页的\"通知\"里可以选哪些事件发邮件。")
			if err != nil {
				return "", hideKey(err, sys.Get("mail.pass"))
			}
			return "已发到 " + maskEmail(u.Email) + "，去收件箱看一下（也看看垃圾邮件）", nil
		case "tg":
			tok := sys.Get("tg.token")
			if tok == "" {
				return "", errors.New("还没填机器人令牌")
			}
			base := d.cfg.TelegramBase
			if base == "" {
				base = "https://api.telegram.org"
			}
			var me struct {
				OK     bool `json:"ok"`
				Result struct {
					Username string `json:"username"`
				} `json:"result"`
				Description string `json:"description"`
			}
			if err := getJSON(ctx, base+"/bot"+tok+"/getMe", nil, &me); err != nil && me.Description == "" {
				return "", friendly(hideKey(err, tok))
			}
			if !me.OK || me.Result.Username == "" {
				return "", errors.New("Telegram 不认这个令牌：" + me.Description)
			}
			return "通了：机器人是 @" + me.Result.Username + "。用户在账户页的\"通知\"里绑定它", nil
		case "data":
			var lines []string
			if k := sys.Get("fred.key"); k != "" {
				var r struct {
					Seriess []struct {
						Title string `json:"title"`
					} `json:"seriess"`
					Error string `json:"error_message"`
				}
				q := url.Values{"series_id": {"DGS10"}, "api_key": {k}, "file_type": {"json"}}
				if err := getJSON(ctx, "https://api.stlouisfed.org/fred/series?"+q.Encode(), nil, &r); err != nil || len(r.Seriess) == 0 {
					lines = append(lines, "FRED：不通（"+errText(friendly(hideKey(err, k)), r.Error)+"）")
				} else {
					lines = append(lines, "FRED：通了（能取美债 10 年期等数据，经济日历会自动更新）")
				}
			}
			if t := sys.Get("x.token"); t != "" {
				acc := splitAccounts(sys.Get("x.accounts"))
				if len(acc) == 0 {
					lines = append(lines, "X：填了令牌，但还没填要看的账号")
				} else {
					var r struct {
						Data struct {
							ID string `json:"id"`
						} `json:"data"`
						Title string `json:"title"`
					}
					if err := getJSON(ctx, "https://api.x.com/2/users/by/username/"+url.PathEscape(acc[0]), map[string]string{"Authorization": "Bearer " + t}, &r); err != nil || r.Data.ID == "" {
						lines = append(lines, "X：不通（"+errText(friendly(hideKey(err, t)), r.Title)+"）")
					} else {
						lines = append(lines, "X：通了（@"+acc[0]+"）")
					}
				}
			}
			if k, s := sys.Get("bn.key"), sys.Get("bn.secret"); k != "" && s != "" {
				if n, err := d.specs.FetchBrackets(ctx, d.cfg.BinanceBase, k, s); err != nil {
					lines = append(lines, "币安只读密钥：不通（"+friendly(hideKey(err, k, s)).Error()+"）")
				} else {
					lines = append(lines, fmt.Sprintf("币安只读密钥：通了（读到 %d 个品种的真实保证金分档）", n))
				}
			}
			if len(lines) == 0 {
				return "", errors.New("这一组还一项都没填")
			}
			msg := strings.Join(lines, "；")
			if strings.Contains(msg, "不通") || strings.Contains(msg, "还没填") {
				return "", errors.New(msg)
			}
			return msg, nil
		}
		return "", errors.New("没有这一组")
	}
	return sys
}

// mailCfg 发信设置：每一项网页里填了用网页的，没填用配置文件的。
func mailCfg(sys *admin.Sys, brand string) mailer.Config {
	port, _ := strconv.Atoi(sys.Get("mail.port"))
	if port == 0 {
		port = 465
	}
	user := sys.Get("mail.user")
	from := strings.TrimSpace(os.Getenv("MAIL_FROM"))
	if from == "" || sys.GroupFromWeb("mail") {
		from = user
	}
	name := sys.Get("mail.from_name")
	if name == "" {
		name = brand
	}
	return mailer.Config{Host: sys.Get("mail.host"), Port: port, User: user, Password: sys.Get("mail.pass"), From: from, FromName: name}
}

// aiProvider AI 复盘点评用哪家：系统页的厂商列表里选了哪家就用哪家（D112）；没选就用配置文件里的。
func aiProvider(sys *admin.Sys, cfg config.Config) aireview.Provider {
	if p := sys.AIActive(); p != nil {
		return aireview.Build(p.Format, p.Key, p.Base, p.Model, p.Effort)
	}
	return envAIProvider(cfg)
}

// envAIProvider 配置文件里的：AI_FORMAT=anthropic 用 Claude 自己的接口（地址、模型可以不填）；
// 否则 AI_BASE_URL、AI_API_KEY、AI_MODEL 三项填齐的 OpenAI 兼容接口；再没有就看 DeepSeek 那几项；都没有就关掉。
func envAIProvider(cfg config.Config) aireview.Provider {
	switch {
	case cfg.AIFormat == "anthropic" && cfg.AIKey != "":
		return aireview.Build("anthropic", cfg.AIKey, cfg.AIBase, cfg.AIModel, cfg.AIEffort)
	case cfg.AIKey != "" && cfg.AIBase != "" && cfg.AIModel != "":
		return aireview.Build("openai", cfg.AIKey, cfg.AIBase, cfg.AIModel, cfg.AIEffort)
	case cfg.DeepSeekKey != "":
		return aireview.DeepSeek(cfg.DeepSeekKey, cfg.DeepSeekModel, cfg.DeepSeekBase)
	}
	return nil
}

// testAI 系统页"测试"一家 AI 厂商：让它回一句。错误里不带密钥；选了思考程度又被拒（400），提示可能是这一档不支持。
func testAI(cfg config.Config) func(ctx context.Context, p admin.AIProvider, env bool) (admin.AITest, error) {
	return func(ctx context.Context, p admin.AIProvider, env bool) (admin.AITest, error) {
		prov, effort := aireview.Build(p.Format, p.Key, p.Base, p.Model, p.Effort), p.Effort
		if env {
			prov, effort = envAIProvider(cfg), cfg.AIEffort
			if prov == nil {
				return admin.AITest{}, errors.New("配置文件里没有配 AI 复盘点评")
			}
		}
		text, tokens, model, err := prov.Complete(ctx, "你是连接测试。只回复两个字：收到", "测试")
		if err != nil {
			e := friendly(hideKey(err, p.Key, cfg.AIKey, cfg.DeepSeekKey))
			if effort != "" && strings.Contains(err.Error(), "400") {
				e = errors.New(e.Error() + "。这个模型可能不支持所选的思考程度：改成\"默认\"再试")
			}
			return admin.AITest{}, e
		}
		return admin.AITest{Model: model, Reply: strings.TrimSpace(firstRunes(text, 20)), Tokens: tokens}, nil
	}
}

func splitAccounts(s string) []string {
	var out []string
	for _, a := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '，' || r == ' ' || r == '\n' }) {
		if a = strings.TrimPrefix(strings.TrimSpace(a), "@"); a != "" {
			out = append(out, a)
		}
	}
	return out
}

func getJSON(ctx context.Context, u string, hdr map[string]string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	_ = json.Unmarshal(b, out)
	if res.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d", res.StatusCode)
	}
	return nil
}

// friendly 把常见的网络和接口错误翻成大白话（测试按钮的提示给不懂技术的人看）。
func friendly(err error) error {
	if err == nil {
		return nil
	}
	s := err.Error()
	l := strings.ToLower(s)
	switch {
	case strings.Contains(l, "no such host"):
		return errors.New("找不到这个地址：检查一下有没有拼错")
	case strings.Contains(l, "dial tcp") || strings.Contains(l, "connectex") || strings.Contains(l, "connection refused") || strings.Contains(l, "timeout") || strings.Contains(l, "deadline exceeded"):
		return errors.New("连不上：地址不对，或者这台电脑连不到那边的网络（要代理的话，看看代理开着没有）")
	case strings.Contains(l, "401") || strings.Contains(l, "403") || strings.Contains(l, "unauthorized") || strings.Contains(l, "invalid api key") || strings.Contains(l, "authentication"):
		return errors.New("密钥不对：对方拒绝了（复制时有没有少几位、多了空格）")
	case strings.Contains(l, "404"):
		return errors.New("地址或模型名不对：对方说找不到（404）")
	case strings.Contains(l, "429"):
		return errors.New("请求太频繁，或者这个账号的额度用完了（429）")
	case strings.Contains(l, "529") || strings.Contains(l, "503"):
		return errors.New("对方服务太忙，过一会儿再试")
	case strings.Contains(l, "refused"):
		return errors.New("模型拒绝回答这一次的内容，换一个模型试试")
	}
	return err
}

// hideKey 错误信息里出现密钥的地方换成 ***。
func hideKey(err error, keys ...string) error {
	if err == nil {
		return nil
	}
	s := err.Error()
	for _, k := range keys {
		if len(k) >= 4 {
			s = strings.ReplaceAll(s, k, "***")
		}
	}
	return errors.New(s)
}

func errText(err error, detail string) string {
	if detail != "" {
		return detail
	}
	if err != nil {
		return err.Error()
	}
	return "没有返回数据"
}

func maskEmail(e string) string {
	at := strings.IndexByte(e, '@')
	if at <= 1 {
		return e
	}
	n := 3
	if at < n {
		n = at
	}
	return e[:n] + "***" + e[at:]
}

func firstRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return string(r)
}
