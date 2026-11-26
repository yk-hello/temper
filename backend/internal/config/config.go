// Package config 读取环境变量配置。所有配置都有安全的默认值，方便本地一键启动。
package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	HTTPAddr       string        // 后端监听地址
	DatabaseURL    string        // 为空时跳过数据库（仅用于开发和测试）
	MarketSource   string        // "binance"（真实行情）或 "sim"（模拟行情，开发测试用）
	Symbols        []string      // 交易所代码，如 BTCUSDT
	PushInterval   time.Duration // 推给前端的合并间隔
	AllowedOrigins []string      // WebSocket 允许的来源
	BrandName      string        // 产品名（D110：Temper）
	QsigKeys       string        // 策略密钥："key_id:secret:strategy_id;..."（每个环境单独一套）
	DemoStrategy   bool          // 在后端内部跑一个随机信号的示例策略（demo-random），让界面有数据可看
	Hyperliquid    bool          // 同时接入 Hyperliquid 行情（链上数据、跨所对比）
	Onchain        bool          // 扫描 Hyperliquid 大额持仓（链上爆仓地图、动作流）
	OnchainSim     bool          // 测试环境：不连 Hyperliquid，用模拟持仓
	OnchainTop     int           // 按账户价值跟踪前多少个地址
	AuthDev        bool          // 账户开发模式：没有邮件服务，验证码打印到日志并在接口里返回
	AuthDevSet     bool          // AUTH_DEV 明写了：照它；没写：跟着有没有发信邮箱自动变（管理后台能配邮箱，2026-11-15）
	SignupOpen     bool          // 第一个账号以后还能不能自己注册（D104）。默认不能：放在云上时别人注册不进来；"系统"页里可以改
	AIKey          string        // AI 复盘点评走哪家（OpenAI 兼容接口）；配了就优先用它。不要打印
	AIBase         string        //
	AIModel        string        //
	AIFormat       string        // openai（默认）/ anthropic
	AIEffort       string        // 思考程度：空 = 默认；none / low / medium / high / xhigh（D112）
	DeepSeekKey    string        // AI 复盘点评（D85）；为空时不开。不要打印
	DeepSeekModel  string        //
	TelegramToken  string        // Telegram 通知（D85）；为空时不开。不要打印
	TelegramBase   string        // 测试环境指向假服务器，正式环境留空
	DeepSeekBase   string        // 同上
	SiteDomain     string        // 钱包登录消息里必须出现的网址（如 app.example.com）；为空时用浏览器访问的网址
	BinanceKey     string        // 币安 API 密钥（只开读取和交易，不开提现）：读真实的保证金分档；以后实盘也用。不要打印
	BinanceSecret  string        //
	BinanceBase    string        // 测试环境指向假服务器，正式环境留空
}

func Load() Config {
	return Config{
		HTTPAddr:       env("HTTP_ADDR", ":8000"),
		DatabaseURL:    env("DATABASE_URL", ""),
		MarketSource:   strings.ToLower(env("MARKET_SOURCE", "binance")),
		Symbols:        upper(list(env("SYMBOLS", "BTCUSDT,ETHUSDT,SOLUSDT,BNBUSDT,XRPUSDT,DOGEUSDT"))),
		PushInterval:   time.Duration(envInt("PUSH_INTERVAL_MS", 100)) * time.Millisecond, // 0.1 秒：和币安盘口最快的 100ms 一档对齐（11-26 用户："改成 0.1 秒"）
		AllowedOrigins: list(env("ALLOWED_ORIGINS", "localhost:*,127.0.0.1:*")),
		BrandName:      env("BRAND_NAME", "Temper"),
		QsigKeys:       env("QSIG_KEYS", ""),
		DemoStrategy:   env("DEMO_STRATEGY", "0") == "1", // 开源版默认关（D96：不提供策略）
		Hyperliquid:    env("HYPERLIQUID", "1") == "1",
		Onchain:        env("ONCHAIN", "1") == "1",
		OnchainSim:     env("ONCHAIN", "1") == "sim",
		OnchainTop:     envInt("ONCHAIN_TOP", 1500),
		AuthDev:        authDev(),
		AuthDevSet:     strings.TrimSpace(os.Getenv("AUTH_DEV")) != "",
		SignupOpen:     env("QS_SIGNUP", "closed") == "open",
		SiteDomain:     env("SITE_DOMAIN", ""),
		AIKey:          env("AI_API_KEY", ""),
		AIBase:         env("AI_BASE_URL", ""),
		AIModel:        env("AI_MODEL", ""),
		AIFormat:       env("AI_FORMAT", ""),
		AIEffort:       env("AI_EFFORT", ""),
		DeepSeekKey:    env("DEEPSEEK_API_KEY", ""),
		DeepSeekModel:  env("DEEPSEEK_MODEL", "deepseek-flash"),
		TelegramToken:  env("TELEGRAM_BOT_TOKEN", ""),
		TelegramBase:   env("TELEGRAM_BASE", ""),
		BinanceKey:     env("BINANCE_API_KEY", ""),
		BinanceSecret:  env("BINANCE_API_SECRET", ""),
		BinanceBase:    env("BINANCE_FAPI_BASE", ""),
		DeepSeekBase:   env("DEEPSEEK_BASE", ""),
	}
}

func env(k, def string) string {
	if v, ok := os.LookupEnv(k); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func envInt(k string, def int) int {
	if v, err := strconv.Atoi(env(k, "")); err == nil && v > 0 {
		return v
	}
	return def
}

func list(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func upper(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = strings.ToUpper(s)
	}
	return out
}

// authDev 开发模式（验证码显示在页面上、打在日志里）：AUTH_DEV 写了就照它；没写时，配了发信邮箱（SMTP_HOST）就关，没配就开。
func authDev() bool {
	if v := strings.TrimSpace(os.Getenv("AUTH_DEV")); v != "" {
		return v == "1"
	}
	return strings.TrimSpace(os.Getenv("SMTP_HOST")) == ""
}
