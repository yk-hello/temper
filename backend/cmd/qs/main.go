// qs 是平台后端的入口：行情接入（币安 + Hyperliquid）→ 盘口仓库 → 前端推送；
// qsig/1 信号接收 → 模型组合 → 模拟成交 → 账本 → 风控。
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"qs/internal/admin"
	"qs/internal/aireview"
	"qs/internal/algo"
	"qs/internal/api"
	"qs/internal/auth"
	"qs/internal/backtest"
	"qs/internal/binance"
	"qs/internal/binapi"
	"qs/internal/book"
	"qs/internal/bots"
	"qs/internal/calendar"
	"qs/internal/candles"
	"qs/internal/config"
	"qs/internal/db"
	"qs/internal/discipline"
	"qs/internal/fakesig"
	"qs/internal/flow"
	"qs/internal/follow"
	"qs/internal/heat"
	"qs/internal/highlight"
	"qs/internal/journal"
	"qs/internal/latency"
	"qs/internal/liqfeed"
	"qs/internal/live"
	"qs/internal/mailer"
	"qs/internal/market"
	"qs/internal/marketmap"
	"qs/internal/news"
	"qs/internal/notify"
	"qs/internal/onchain"
	"qs/internal/portfolio"
	"qs/internal/qsig"
	"qs/internal/ratelimit"
	"qs/internal/risk"
	"qs/internal/scan"
	sigs "qs/internal/signal"
	"qs/internal/spot"
	"qs/internal/store"
	"qs/internal/strategy"
	"qs/internal/system"
	"qs/internal/trading"
	"qs/internal/venues"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

func main() {
	if len(os.Args) > 1 { // 本机命令：重置二级密码、关二次验证（cli.go），不启动服务
		os.Exit(runCLI(os.Args[1:]))
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	// 发往币安的请求统一限速、收到 429 / 418 就退让：币安按 IP 限流，行情把 IP 撞封了，实盘也下不了单（11-17）
	binance.Default = &binance.Guard{Log: log}
	http.DefaultTransport = binance.Default
	cfg := config.Load()
	applyLimits(log) // 部署的人改的上限（QS_LIMIT_*，D103）
	log.Info("starting", "brand", cfg.BrandName, "market_source", cfg.MarketSource, "symbols", cfg.Symbols, "hyperliquid", cfg.Hyperliquid)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	instruments := make([]string, len(cfg.Symbols))
	for i, s := range cfg.Symbols {
		instruments[i] = market.CanonicalPerp(s)
	}

	// ---------- 行情 ----------
	hub := market.NewHub(instruments, cfg.PushInterval)
	go hub.Run(ctx)
	books := book.NewStore()
	primary := "binance-usdm"
	var src market.Source = &market.Binance{Symbols: cfg.Symbols, Log: log}
	var mapFetch marketmap.Fetcher = &marketmap.Binance{}
	var simMap *marketmap.Sim
	if cfg.MarketSource == "sim" {
		primary = "sim"
		sim := &market.Sim{Symbols: cfg.Symbols, Seed: uint64(time.Now().UnixNano()), BasePrice: marketmap.SimPrice}
		src = sim
		simMap = &marketmap.Sim{Symbols: cfg.Symbols, Price: sim.Price}
		mapFetch = simMap
	}
	bars := candles.New()
	// 流动性频谱：6 个重点币的完整盘口和录制（12 的 V.5–V.10）
	var heatTick func(string) float64
	var heatFeed heat.Feed = &heat.BinanceDepth{Instruments: instruments, Log: log}
	if simSrc, ok := src.(*market.Sim); ok {
		heatFeed = &heat.SimDepth{Instruments: instruments, Seed: uint64(time.Now().UnixNano()), Tick: func(i string) float64 { return heatTick(i) },
			Price: func(i string) (float64, bool) { return simSrc.Price(strings.TrimSuffix(i, "-PERP") + "USDT") }}
	}
	heats := heat.New(heatFeed, instruments, func(i string) float64 { return heatTick(i) }, log)
	// 订单流：持仓量拆解、多空比、爆仓流（12 的 V.12–V.14）
	var flowFetch flow.Fetcher = &flow.Binance{}
	if simSrc, ok := src.(*market.Sim); ok {
		flowFetch = &flow.Sim{Price: func(sym string) float64 { p, _ := simSrc.Price(sym); return p }}
	}
	flows := flow.New(flowFetch)
	meter := &market.Meter{}
	fan := &market.Fanout{Primary: primary, Hub: hub, Books: books, Meter: meter, OnEvent: func(ev market.Event) {
		if ev.Kind == market.KindTrade && ev.Venue == primary {
			bars.Trade(ev.Instrument, ev.Time, ev.Price, ev.Qty, ev.Side == market.Buy)
			if p, err := strconv.ParseFloat(ev.Price, 64); err == nil {
				if q, err := strconv.ParseFloat(ev.Qty, 64); err == nil {
					heats.OnTrade(ev.Instrument, ev.Time, p, q, ev.Side == market.Buy)
				}
			}
		}
	}}
	fan.OnLiquidation = func(ev market.Event) {
		p, _ := strconv.ParseFloat(ev.Price, 64)
		q, _ := strconv.ParseFloat(ev.Qty, 64)
		side := "short"
		if ev.Side == market.Sell { // 强平卖单 = 多单被强平
			side = "long"
		}
		flows.AddLiquidation(flow.Liq{T: ev.Time, Instrument: ev.Instrument, Side: side, Venue: "binance", P: p, Q: q, USD: p * q})
	}
	if simSrc, ok := src.(*market.Sim); ok {
		go flows.RunSim(ctx, instruments, func(i string) float64 { p, _ := simSrc.Price(strings.TrimSuffix(i, "-PERP") + "USDT"); return p })
	} else if !strings.EqualFold(os.Getenv("LIQ_FEEDS"), "off") {
		// Bybit、OKX 的全量强平（D102）：币安每个币每秒只推最大的一笔
		lf := &liqfeed.Feed{Inst: map[string]string{}, Log: log, OnLiq: func(l liqfeed.Liq) {
			flows.AddLiquidation(flow.Liq{T: l.T, Instrument: l.Instrument, Side: l.Side, Venue: l.Venue, P: l.P, Q: l.Q, USD: l.USD})
		}}
		for i, sym := range cfg.Symbols {
			lf.Inst[sym] = instruments[i]
		}
		go lf.Run(ctx)
	}
	go src.Run(ctx, fan)
	// 现货（D44）：和合约共用行情通道和盘口仓库，6 个重点币
	spotEng := spot.New(books, primary, log)
	var spotSyms []string
	for _, s := range spotEng.Specs {
		spotSyms = append(spotSyms, s.Base+"USDT")
	}
	sort.Strings(spotSyms)
	if simSrc, ok := src.(*market.Sim); ok {
		go (&market.SpotSim{Symbols: spotSyms, Price: simSrc.Price, Venue: primary, Seed: uint64(time.Now().UnixNano()), Decs: func(sym string) int {
			if s := spotEng.Specs[market.CanonicalSpot(sym)]; s != nil {
				return int(-s.Tick.Exponent())
			}
			return 2
		}}).Run(ctx, fan)
	} else {
		go (&market.Binance{Symbols: spotSyms, Spot: true, Log: log}).Run(ctx, fan)
	}
	if primary == "binance-usdm" {
		syms := map[string]string{}
		for i, sym := range cfg.Symbols {
			syms[instruments[i]] = sym
		}
		for _, sym := range spotSyms {
			syms[market.CanonicalSpot(sym)] = "spot:" + sym
		}
		bars.Remote = &candles.Remote{Symbols: syms} // 更早的历史 K 线从币安补
		go bars.RunHeal(ctx, 10*time.Second)         // 电脑睡眠、断网留下的窟窿也从币安补
		// 重启后补回最近约 25 小时的 1 分钟 K 线，图表不会是空的
		go func() {
			for i, sym := range cfg.Symbols {
				hist, err := candles.FetchBinance(ctx, sym, 1500)
				if err != nil {
					log.Warn("补 K 线失败（图表从现在开始画）", "symbol", sym, "err", err)
					continue
				}
				bars.Backfill(instruments[i], 60_000, hist)
			}
			log.Info("已补回历史 K 线", "symbols", len(cfg.Symbols))
		}()
	}
	if cfg.Hyperliquid {
		coins := make([]string, len(instruments))
		for i, in := range instruments {
			coins[i] = strings.TrimSuffix(in, "-PERP")
		}
		go (&market.Hyperliquid{Coins: coins, Log: log}).Run(ctx, fan)
	}

	// ---------- 交易核心 ----------
	// 有数据库时：等数据库就绪 → 建表 → 从数据库恢复账本、持仓、消息 → 才开始接收信号。
	// 恢复完成之前，/v1/ 一律回复 503，策略端会按规范重试。
	database := db.Connect(ctx, cfg.DatabaseURL, log)
	defer database.Close()

	specs := trading.NewSpecs(trading.DefaultSpecs())
	if primary == "binance-usdm" {
		go func() {
			if err := specs.FetchBinance(ctx); err != nil {
				log.Warn("读取币安品种规格失败，先用内置值", "err", err)
			} else {
				log.Info("已从币安读取品种规格（价格单位、数量单位、最小金额、委托上限、清算费率）")
			}
			// 配了币安密钥：读真实的保证金分档（没有密钥时 BTC 用官网页面读的完整分档，其他品种是估计值）
			if n, err := specs.FetchBrackets(ctx, cfg.BinanceBase, cfg.BinanceKey, cfg.BinanceSecret); err != nil {
				log.Warn("读取币安保证金分档失败，先用内置值", "err", err)
			} else if n > 0 {
				log.Info("已从币安读取保证金分档", "symbols", n)
			}
		}()
	}
	// 手动交易、AI 模型组合、跟随都走这一个引擎（D74）
	if simMap != nil {
		specs.AddCatalog(simMap.Universe(), marketmap.SimPrice)
	}
	heatTick = func(i string) float64 {
		if sp, ok := specs.Get(i); ok {
			v, _ := sp.Tick.Float64()
			return v
		}
		return 0
	}
	go heats.Run(ctx)
	trader := trading.New(books, primary, specs, log)
	// 全市场方块图（3.15）
	field := &marketmap.Service{F: mapFetch, Source: primary, Known: specs.Known, Focus: cfg.Symbols, Log: log}
	if primary != "sim" {
		// 币安的目录要等品种规格取回来才有：没取回来之前不过滤，等它好了就只留能交易的
		field.Known = func(sym string) bool { return specs.Known(sym) || !specs.HasCatalog() }
	}
	go field.Run(ctx)
	// 筛选器、异动、今日（盘前准备和市场总览）：方块图那份行情 + 币安和公开数据源的资金费、持仓量、日线、指数
	scanSvc := &scan.Service{Map: field.Current}
	var scanPub *scan.Public
	if primary == "binance-usdm" {
		scanPub = &scan.Public{FAPI: cfg.BinanceBase, FREDKey: strings.TrimSpace(os.Getenv("FRED_API_KEY"))}
		scanSvc.Src = scanPub
	}
	go scanSvc.Run(ctx)
	// 用户点开一个还没在收行情的币：开始收，能下单（2.31、3.15）。最多额外开这么多个，防止被人刷
	const maxExtraInstruments = 64
	var extraMu sync.Mutex
	extra := map[string]bool{}
	for _, in := range instruments {
		extra[in] = true
	}
	ensureInst := func(inst string) bool {
		extraMu.Lock()
		defer extraMu.Unlock()
		if extra[inst] {
			return true
		}
		if len(extra)-len(instruments) >= maxExtraInstruments || !specs.Enable(inst) {
			return false
		}
		sym := strings.TrimSuffix(inst, "-PERP") + "USDT"
		adder, ok := src.(interface{ Add(string) bool })
		if !ok || !adder.Add(sym) {
			return false
		}
		if bars.Remote != nil {
			bars.Remote.Add(inst, sym)
		}
		extra[inst] = true
		log.Info("开始收新品种的行情", "instrument", inst)
		return true
	}
	traderReady := make(chan struct{}) // 引擎重放完事件才能建模型组合
	var followers *follow.Manager      // 跟随层：模型组合的目标变了就通知它（下面建好）
	var st *store.Store
	var pmu sync.Mutex
	portfolios := map[string]*portfolio.Portfolio{}
	var restored struct {
		states map[string]json.RawMessage
		execs  map[string]json.RawMessage
	}
	memAcct := int64(0) // 没有数据库时系统账户的编号
	getPF := func(id string) *portfolio.Portfolio {
		pmu.Lock()
		defer pmu.Unlock()
		p, ok := portfolios[id]
		if !ok {
			var acct int64
			if st != nil {
				var err error
				if acct, err = st.AccountFor(ctx, id); err != nil {
					log.Error("strategy account", "strategy", id, "err", err)
					os.Exit(1)
				}
			} else {
				memAcct--
				acct = memAcct
			}
			p = portfolio.New(id, acct, primary, primary, books, trader)
			sid := id
			p.OnChange = func(c []portfolio.Change) { followers.Notify(sid, c) }
			if st != nil {
				key := portfolio.StoreKey(acct)
				var snap *portfolio.Snap
				if raw, ok := restored.states[key]; ok {
					snap = &portfolio.Snap{}
					_ = json.Unmarshal(raw, snap)
				}
				execs := map[string][]portfolio.Execution{}
				for mid, raw := range restored.execs {
					var list []portfolio.Execution
					if json.Unmarshal(raw, &list) == nil {
						execs[mid] = list
					}
				}
				var nav []portfolio.NAVPoint
				if pts, err := st.LoadNAV(ctx, key, 3650); err == nil {
					for _, pt := range pts {
						nav = append(nav, portfolio.NAVPoint{T: int64(pt[0]), NAV: pt[1]})
					}
				}
				p.Restore(snap, execs, nav)
				pid := id
				p.OnExecutions = func(msgID string, list []portfolio.Execution) {
					if err := st.SaveExecutions(msgID, pid, list); err != nil {
						log.Error("save executions", "err", err)
					}
				}
				p.OnNAV = func(t int64, nav float64) { _ = st.AppendNAV(key, t, nav) }
				p.OnState = func(sn portfolio.Snap) {
					if err := st.SaveState(key, sn); err != nil {
						log.Error("save state", "err", err)
					}
				}
			}
			portfolios[id] = p
		}
		return p
	}

	oiSource := &marketmap.OpenInterest{}
	lookupPF := func(id string) *portfolio.Portfolio {
		pmu.Lock()
		defer pmu.Unlock()
		return portfolios[id]
	}

	keys := parseKeys(cfg.QsigKeys)
	var demo *qsig.Key
	if cfg.DemoStrategy {
		var b [16]byte
		_, _ = rand.Read(b[:])
		demo = &qsig.Key{ID: "demo", Secret: hex.EncodeToString(b[:]), StrategyID: qsig.DemoStrategyID}
		keys = append(keys, *demo)
	}
	var ins []qsig.Instrument
	for _, in := range instruments {
		venues := []string{primary}
		if cfg.Hyperliquid {
			venues = append(venues, "hyperliquid")
		}
		ins = append(ins, qsig.Instrument{Instrument: in, Base: strings.TrimSuffix(in, "-PERP"), Quote: "USDT", Kind: "linear_perpetual", Venues: venues, Tradable: true})
	}
	svc, err := qsig.NewService(keys, ins, books, primary, getPF, log)
	if err != nil {
		log.Error("qsig init", "err", err)
		os.Exit(1)
	}
	var tradingReady atomic.Bool
	startTrading := func() {
		for _, k := range keys {
			getPF(k.StrategyID)
		}
		go func() {
			t := time.NewTicker(time.Second)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					pmu.Lock()
					list := make([]*portfolio.Portfolio, 0, len(portfolios))
					for _, p := range portfolios {
						list = append(list, p)
					}
					pmu.Unlock()
					for _, p := range list {
						p.Watch()
					}
				}
			}
		}()
		tradingReady.Store(true)
		log.Info("trading core ready", "persistent", st != nil, "strategies", len(keys))
	}
	if cfg.DatabaseURL == "" {
		log.Warn("没有配置数据库：账本和持仓只在内存里，重启就会清空")
		go trader.Run(ctx)
		startTrading()
	} else {
		go func() {
			select {
			case <-ctx.Done():
				return
			case <-database.Ready():
			}
			st = &store.Store{Pool: database.Pool}
			rctx, cancel := context.WithTimeout(ctx, 60*time.Second)
			defer cancel()
			err := st.Migrate(rctx)
			if err != nil {
				log.Error("migrate", "err", err)
				os.Exit(1)
			}
			restored.states, err = st.LoadStates(rctx)
			if err == nil {
				restored.execs, err = st.LoadExecutions(rctx, 30)
			}
			var msgs []store.Message
			if err == nil {
				msgs, err = st.LoadMessages(rctx, 30)
			}
			if err != nil {
				log.Error("restore", "err", err)
				os.Exit(1)
			}
			list := make([]qsig.Stored, 0, len(msgs))
			for _, m := range msgs {
				var h [32]byte
				copy(h[:], m.Hash)
				list = append(list, qsig.Stored{ID: m.ID, StrategyID: m.StrategyID, Type: m.Type, Status: m.Status, Code: m.Code, ReceivedAt: m.ReceivedAt, Body: m.Body, Hash: h, Reply: m.Reply})
			}
			svc.Load(list)
			if last, err := st.LastChains(rctx); err == nil {
				svc.SetChains(last) // 前向记录接着上次的最后一环
			} else {
				log.Error("qsig chains", "err", err)
			}
			svc.Persist = func(m qsig.Stored) error {
				return st.SaveMessage(store.Message{ID: m.ID, StrategyID: m.StrategyID, Type: m.Type, Status: m.Status, Code: m.Code, ReceivedAt: m.ReceivedAt, Body: m.Body, Hash: m.Hash[:], Reply: m.Reply, Prev: m.Prev, Chain: m.Chain})
			}
			log.Info("restored from database", "messages", len(msgs), "portfolios", len(restored.states))
			select {
			case <-ctx.Done():
				return
			case <-traderReady:
			}
			startTrading()
		}()
	}

	v1 := svc.Handler()
	front := &api.Server{Hub: hub, DBStatus: database.Status, BrandName: cfg.BrandName, AllowedOrigins: cfg.AllowedOrigins, Log: log, Started: time.Now(),
		Strategies: func() any {
			if !tradingReady.Load() {
				return []any{}
			}
			return svc.Status()
		},
		Portfolio: func(id string, full bool) (any, bool) {
			p := lookupPF(id)
			if p == nil || !tradingReady.Load() {
				return nil, false
			}
			st := p.State()
			if !full { // 没开启服务：只有净值和汇总，看不到还没平的仓位（3.13）
				st.Positions = []portfolio.PositionState{}
			}
			return map[string]any{"state": st, "nav_history": p.History(), "locked": !full}, true
		},
		Decisions: func(id string) (any, bool) {
			if lookupPF(id) == nil || !tradingReady.Load() {
				return nil, false
			}
			return svc.Decisions(id, 100), true
		},
		Trades: func(id string) (any, bool) {
			if lookupPF(id) == nil || !tradingReady.Load() {
				return nil, false
			}
			return svc.Trades(id), true
		},
		Calibration: func(id string) (any, bool) {
			if lookupPF(id) == nil || !tradingReady.Load() {
				return nil, false
			}
			return svc.Calibration(id), true
		},
		Ensure: ensureInst, MarketMap: field,
		OpenInterest: func(ctx context.Context, inst string) (float64, bool) {
			t, ok := books.Ticker(primary, inst)
			if !ok || t.Mark.Sign() <= 0 {
				return 0, false
			}
			mark, _ := t.Mark.Float64()
			if primary == "sim" { // 模拟行情：按品种算一个固定的数，界面上写"模拟行情"
				return 40_000 * mark * (1 + marketmap.SimPrice(inst)), true
			}
			v, err := oiSource.Value(ctx, strings.TrimSuffix(inst, "-PERP")+"USDT")
			if err != nil {
				return 0, false
			}
			return v * mark, true
		},
		Performance: func(id string) (any, bool) {
			p := lookupPF(id)
			if p == nil || !tradingReady.Load() {
				return nil, false
			}
			perf := p.Performance()
			perf.Followers, perf.FollowCapital = followers.Stats(id)
			return perf, true
		},
		Signals: func(n int) any { return svc.Recent(n) },
		Books:   books, Candles: bars, Primary: primary,
	}
	mux := http.NewServeMux()
	limitsRoute(mux)
	heats.Routes(mux)
	flows.Routes(mux)
	mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
		if !tradingReady.Load() {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Retry-After", "5")
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"status":"rejected","code":"INTERNAL_ERROR","message":"平台正在启动（恢复账本），请稍后重试"}` + "\n"))
			return
		}
		v1.ServeHTTP(w, r)
	})
	var chainScan *onchain.Scanner
	if cfg.Onchain || cfg.OnchainSim {
		sc := &onchain.Scanner{TopByValue: cfg.OnchainTop, Log: log}
		chainScan = sc
		price := func(coin string) float64 {
			for _, v := range []string{"hyperliquid", primary} {
				if t, ok := books.Ticker(v, coin+"-PERP"); ok && t.Mark.Sign() > 0 {
					f, _ := t.Mark.Float64()
					return f
				}
			}
			return 0
		}
		if cfg.OnchainSim {
			coins := make([]string, len(instruments))
			for i, in := range instruments {
				coins[i] = strings.TrimSuffix(in, "-PERP")
			}
			go sc.RunSim(ctx, coins, price)
		} else {
			go sc.Run(ctx)
		}
		mux.Handle("/api/onchain/", sc.Handler(price))
	}
	// ---------- 用户和账户（模块 1）、手动交易（模块 2）----------
	// 数据库连上 → 建表 → 重放交易事件 → 给没有引擎账户的模拟账户补开户 → 才开放接口。
	// 没有数据库时这些接口一直返回 503。
	users := auth.New(nil, log)
	users.DevMode = cfg.AuthDev
	users.DevAuto = !cfg.AuthDevSet // 没明写 AUTH_DEV：跟着有没有发信邮箱走（管理后台配上邮箱马上关）
	// 发信用部署的人自己的邮箱（D100）：管理后台"系统设置"里填，或者 deploy/.env 里写 SMTP_*；
	// 认证（验证码）和通知共用这一个，网页里改了马上生效（2026-11-15）
	mailLive := mailer.NewLive(mailer.FromEnv(cfg.BrandName))
	if m, _ := mailLive.Current(); m.Enabled() {
		log.Info("发信邮箱已配置", "host", m.Host, "from", m.From)
	}
	users.Mail = mailLive
	users.Domain = cfg.SiteDomain
	userGate := &auth.Gate{}
	users.Routes(mux, userGate)
	// 模块 7：权益（现在都挂内部套餐）、模拟账本（不接真钱）、通知、AI 点评（D85）
	notifier := notify.New(log)
	notifier.Mail = mailLive      // 通知也能发邮件（18 第 8.2 条）：默认只开和钱、安全有关的几种
	hlStore := &highlight.Store{} // 高光（D88）
	notifier.TG = &notify.Telegram{Token: cfg.TelegramToken, Base: cfg.TelegramBase, Log: log, S: notifier}
	// 对外 Webhook（18 第 6.3 条）：默认不发到本机和内网（服务器在公网时防止被拿来打内网）
	notifier.WH = &notify.Webhooks{AllowPrivate: os.Getenv("QS_WEBHOOK_PRIVATE") == "1"}
	notifier.Routes(mux, users, func(inst string) float64 {
		if t, ok := books.Ticker(primary, inst); ok {
			if v, _ := t.Mark.Float64(); v > 0 {
				return v
			}
			v, _ := t.Last.Float64()
			return v
		}
		return 0
	})
	go notifier.TG.Run(ctx)
	reviews := &aireview.Service{}
	reviews.Provider = envAIProvider(cfg) // 系统页里选了厂商的，读完系统设置后换成那一家（settings_wire.go）
	followers = follow.New(trader, users, lookupPF, log)
	// 纪律锁（D45、D80）：引擎下单前过它一遍；每次登录自动开启
	disc := discipline.New(trader, log)
	disc.AIManaged = followers.AIManaged
	// 实盘（D89）：用户接自己的币安账号。钱在用户自己的交易所账户里，系统不经手（D90）
	liveStore := live.NewStore(live.MasterFromEnv())
	liveDesks := &live.Desks{S: liveStore, Specs: specs, Books: books, Venue: primary, Log: log,
		URLs: liveURLs(ctx, books, primary, instruments, log)}
	disc.Trader = discMux{paper: trader, live: liveDesks}
	// 策略跟随也能驱动交易所账户（18 第 5.2 条）：按账户编号分到模拟盘或交易所；开启要二级密码，账户要先确认过"这是真钱"
	followers.Trader = trading.Mux{Paper: trader, Live: liveDesks}
	followers.TopUp = trader.Fund // 模拟资金不限额（D103）：分配的比可用的多时自动补上
	followers.LiveFills = liveDesks.SourceFills
	followers.LiveAcct = func(ctx context.Context, user, acct int64) (bool, bool) {
		if !liveStore.Ready() {
			return false, false
		}
		return liveStore.OwnedArmed(ctx, user, acct)
	}
	followers.TradePassword = users.CheckTradePassword
	// 自助建策略（D96）：用户自己拿 qsig/1 密钥，用同一把主密钥加密存；建好马上生效
	stratStore := &strategy.Store{C: liveStore, Reserved: func(id string) bool {
		for _, k := range keys {
			if k.StrategyID == id {
				return true
			}
		}
		return false
	}}
	// 交易所账户只能跟自己的策略（D99）：自己建的；环境变量里配的算部署的人（所有者）的。
	// 别人建的策略只能跟到模拟账户——别人的信号直接操作自己的真钱账户就是带单（D90 不做）。
	// 内置示例策略是随机信号：开着示例时可以接币安模拟交易和测试用的交易所，不能接真钱账户
	// 自己建的策略发了新目标：提醒它的主人（只提醒、不下单，18 第 8.4 条）。配置里的、示例的策略不是任何人自己建的，不推
	svc.OnRecord = strategyPush(notifier, func(ctx context.Context, id string) int64 {
		if stratStore.Pool == nil || stratStore.Reserved(id) || id == qsig.DemoStrategyID {
			return 0
		}
		owner, err := stratStore.Owner(ctx, id)
		if err != nil {
			return 0
		}
		return owner
	})
	followers.LiveStrategy = follow.LiveRule{DemoID: qsig.DemoStrategyID, DemoOn: cfg.DemoStrategy, Reserved: stratStore.Reserved, Env: liveStore.EnvOf,
		Owner: func(ctx context.Context, id string) (int64, error) {
			if stratStore.Pool == nil { // 数据库还没接上：一律不行
				return 0, nil
			}
			return stratStore.Owner(ctx, id)
		}}.Check
	var stratReady atomic.Bool
	stratOn := func(k strategy.Key) {
		svc.AddKey(qsig.Key{ID: k.KeyID, Secret: k.Secret, StrategyID: k.StrategyID})
		getPF(k.StrategyID)
	}
	(&strategy.API{S: stratStore, Users: users, Ready: stratReady.Load, OnKey: stratOn, OnRevoke: svc.RemoveKey}).Routes(mux)
	// 前向记录导出（D96 第 2 条）：一个策略收到的每条消息，串成链，下载后用 docs/examples/verify_log.py 自己验证没被改过
	mux.HandleFunc("GET /api/strategies/{id}/log", users.Require(func(w http.ResponseWriter, r *http.Request) {
		if st == nil {
			http.Error(w, `{"code":"STARTING"}`, 503)
			return
		}
		id := r.PathValue("id")
		w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+id+`-qsig-log.jsonl"`)
		w.Header().Set("Cache-Control", "no-store")
		if _, err := st.ExportChain(r.Context(), id, w); err != nil {
			log.Error("导出前向记录", "strategy", id, "err", err)
		}
	}))
	// 和币安一样的接口（D96 第 1 条，18 第 5.3 条）：模拟盘包装成币安 U 本位合约，现成的程序改个地址就能跑
	binapiStore := &binapi.Store{C: liveStore}
	var binapiReady atomic.Bool
	upstream := ""
	if primary == "binance-usdm" { // 这里不仿的公开行情（K 线等）转给币安；模拟行情时不转
		upstream = "https://fapi.binance.com"
		if cfg.BinanceBase != "" {
			upstream = strings.TrimRight(cfg.BinanceBase, "/")
		}
	}
	mux.Handle("/bapi/", &binapi.Server{Keys: binapiStore, E: trader, Books: books, Venue: primary, Upstream: upstream,
		Disabled: func(ctx context.Context, user int64) bool {
			_, err := users.UserByID(ctx, user) // 账号删了的密钥不能再用
			return err != nil
		}})
	(&binapi.KeysAPI{S: binapiStore, Users: users, Ready: binapiReady.Load}).Routes(mux)
	// "系统"页（D104）：全局停机。所有下单路径的预检最先过它
	adminStore := &admin.Store{}
	adminGate := &admin.Gate{}
	adminAPI := &admin.API{S: adminStore, G: adminGate, Users: users, Log: log,
		Strategies: func() any {
			if st := svc.Status(); st != nil {
				return st
			}
			return []any{}
		},
		Resume: svc.Resume,
		Revoke: func(ctx context.Context, id string) (bool, error) {
			old, err := stratStore.Revoke(ctx, 0, id)
			if errors.Is(err, strategy.ErrNotYour) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			svc.RemoveKey(old)
			return true, nil
		},
		CancelOpening: func() int {
			n := 0
			dm := discMux{paper: trader, live: liveDesks}
			for _, id := range disc.Accounts() {
				if c, err := dm.CancelOpening(id, "ADMIN_HALT"); err == nil {
					n += c
				}
				if !live.IsLive(id) {
					n += spotEng.CancelBuys(id, "ADMIN_HALT")
				}
			}
			return n
		}}
	adminAPI.Routes(mux)
	adminStore.SignupOpen = cfg.SignupOpen
	users.RegClosed = func(ctx context.Context) bool { return adminStore.RegMode(ctx) == "admin" }
	// 模拟盘的下单延迟（D94）：默认跟随这台服务器到币安的实测往返时间；用户可以选地区或关掉
	latMeter := &latency.Meter{}
	if primary == "binance-usdm" {
		base := cfg.BinanceBase
		if base == "" {
			base = "https://fapi.binance.com"
		}
		latMeter.URL = base + "/fapi/v1/time"
		go latMeter.Run(ctx)
	}
	latPrefs := &latency.Prefs{Owner: disc.UserOf}
	latModel := &latency.Model{Meter: latMeter, Mode: latPrefs.ForAccount}
	trader.Latency = latModel.Hop
	front.ExchangeRTT = func() (float64, bool) {
		rtt, _, ok := latMeter.Measured()
		return math.Round(float64(rtt)/float64(time.Millisecond)*10) / 10, ok
	}
	// 机器人（D41、D83）：现货和合约各一个通道
	high24 := func(inst string) decimal.Decimal {
		for _, x := range hub.Snapshot().Instruments {
			if x.Instrument == inst {
				v, _ := decimal.NewFromString(x.High24h)
				return v
			}
		}
		return decimal.Zero
	}
	botMgr := bots.New(&bots.SpotExec{E: spotEng, Books: books, Venue: primary, High: high24}, &bots.PerpExec{E: trader, Books: books, Venue: primary, High: high24}, log)
	botMgr.AIRunning = followers.AccountRunning
	botMgr.Locked = disc.LockErr
	spotEng.OnFill = func(acct int64, f spot.Fill, o spot.Order) {
		if o.Source == "manual" {
			botMgr.ManualAction(acct, f.Instrument)
		}
	}
	// 别的引擎要在机器人的币上下单：比优先级（D42、D83）
	trader.Guard = func(p trading.PreOrder) error {
		if err := adminGate.Check(p.Account); err != nil {
			return err
		}
		if p.Source == "follow" || p.Source == "ai" || p.Source == "signal" {
			engine := "ai"
			if p.Source == "signal" {
				engine = "signal"
			}
			if err := botMgr.Arbitrate(disc.UserOf(p.Account), p.Account, p.Instrument, engine); err != nil {
				return trading.NewReject("BOT_HAS_PRIORITY")
			}
		}
		return disc.Guard(p)
	}
	liveDesks.Guard = trader.Guard
	users.OnNewUser = disc.NewUser // 新用户的纪律锁默认关着（D107）
	// 新设备登录提醒（18 第 6.2 条）：站内、Telegram、Webhook 按这个人的开关发，默认都开
	users.OnNewDevice = func(user int64, device, ip string) {
		notifier.Push(context.Background(), user, notify.KLogin, map[string]string{"device": device, "ip": ip})
	}
	// 用登录密码重置了二级密码（D107）：交易所账户全部回到"未开启"（开新仓要用新的二级密码重新开启，减仓平仓照常），
	// 发一条安全提醒（和新设备登录同一类，默认开着）
	users.OnTradePwReset = func(user int64) {
		ctx := context.Background()
		if err := liveStore.DisarmAll(ctx, user); err != nil {
			log.Error("二级密码重置后暂停交易所账户失败", "user", user, "err", err)
		}
		notifier.Push(ctx, user, notify.KLogin, map[string]string{"event": "tp_reset"})
	}
	// 交易所账户对账发现对不上（18 第 6.4 条）：别处下的单、强平、钱包无故变化、对账一直失败
	liveDesks.OnAlert = func(user int64, params map[string]string) {
		notifier.Push(context.Background(), user, notify.KRecon, params)
	}
	users.OnPaperAccount = func(user, id int64, initial string) {
		trader.Open(id, decimal.RequireFromString(initial))
		disc.Register(user, id)
	}
	users.OnPaperReset = trader.CopyFees
	users.PaperFees = func(id int64) (string, string, bool) {
		t, m, c := trader.FeesOf(id)
		return t.String(), m.String(), c
	}
	front.Access = func(r *http.Request) (bool, bool) {
		if users.Pool == nil {
			return false, false
		}
		c, err := r.Cookie(auth.SessionCookie)
		if err != nil {
			return false, false
		}
		u, err := users.UserBySession(r.Context(), c.Value)
		if err != nil {
			return false, false
		}
		return true, followers.IsFollowing(u.ID)
	}
	users.OnPaperArchive = func(_, id int64) {
		binapiStore.RevokeAccount(context.Background(), id) // 绑这个账户的币安兼容密钥一起失效
		botMgr.AccountArchived(id)
		spotEng.Archive(id)
		trader.Archive(id)
		followers.AccountArchived(id)
		disc.Unregister(id)
	}
	users.OnUserDelete = func(ctx context.Context, user int64, ids []int64) error {
		botMgr.DeleteUser(ctx, user)
		notifier.DeleteUser(ctx, user)
		hlStore.DeleteUser(ctx, user)
		latPrefs.DeleteUser(ctx, user)
		if liveStore.Ready() {
			if accts, err := liveStore.Accounts(ctx, user); err == nil {
				for _, a := range accts {
					liveDesks.Stop(a.ID)
					disc.Unregister(a.ID)
				}
			}
			liveStore.DeleteUser(ctx, user)
		}
		if users.Pool == nil {
			return nil
		}
		tx, err := users.Pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		for _, q := range []string{
			`delete from trading_events where account = any($1)`,
			`delete from spot_events where account = any($1)`,
			`delete from algo_orders where account = any($1)`,
			`delete from signal_logs where hook in (select id from signal_hooks where account = any($1))`,
			`delete from signal_hooks where account = any($1)`,
			`delete from discipline_events where account = any($1)`,
			`delete from follow_events where follow in (select distinct follow from follow_events where (body->>'account')::bigint = any($1))`,
		} {
			if _, err := tx.Exec(ctx, q, ids); err != nil {
				return err
			}
		}
		// 去掉收费（D90）之前的库里还有 user_plans：有就一起删，新装的库没有这张表
		var plans *string
		if err := tx.QueryRow(ctx, `select to_regclass('user_plans')::text`).Scan(&plans); err == nil && plans != nil {
			if _, err := tx.Exec(ctx, `delete from user_plans where user_id = $1`, user); err != nil {
				return err
			}
		}
		for _, q := range []string{`delete from ai_reviews where user_id = $1`, `delete from journal_notes where usr = $1`, `delete from journal_shots where usr = $1`, `delete from journal_playbooks where usr = $1`, `delete from journal_order_playbook where usr = $1`} {
			if _, err := tx.Exec(ctx, q, user); err != nil {
				return err
			}
		}
		return tx.Commit(ctx)
	}
	botMgr.MaxBots = func(int64) int { return 20 } // 同时运行的机器人上限，只防滥用（D85 第 2 条）
	trader.OnManualFill = botMgr.ManualAction
	(&trading.API{Engine: trader, Users: users, Gate: userGate, OnManual: followers.ManualAction, Live: liveDesks,
		LiveOwns: func(ctx context.Context, user, acct int64) bool {
			return liveStore.Ready() && liveStore.Owns(ctx, user, acct)
		}}).Routes(mux)
	(&live.API{S: liveStore, D: liveDesks, Users: users, OnAccount: disc.Register, OnArchive: func(acct int64) {
		disc.Unregister(acct)
		followers.AccountArchived(acct) // 交易所账户停用了：上面的跟随停掉（和模拟账户重置一样，仓位不动）
	}}).Routes(mux)
	(&latency.API{M: latModel, P: latPrefs, Users: users}).Routes(mux)
	scanSvc.Routes(mux)
	// 回测（D95）：同一个撮合引擎跑历史 K 线；数据走币安公开接口（测试环境可以用 BINANCE_FAPI_BASE 指到假服务器）
	(&backtest.API{Src: &backtest.Binance{Base: cfg.BinanceBase}, Specs: specs, Users: users}).Routes(mux)
	botMgr.Routes(mux, users)
	algos := algo.New(trader, books, primary, log)
	algos.Locked = disc.LockErr
	algos.Routes(mux, users, followers.ManualAction)
	spotEng.Guard = func(acct int64, inst, side, source string, _ decimal.Decimal) error {
		if side == "buy" { // 现货里买是开仓，卖是减仓
			if err := adminGate.Check(acct); err != nil {
				return err
			}
		}
		if source == "signal" {
			if err := botMgr.Arbitrate(disc.UserOf(acct), acct, inst, "signal"); err != nil {
				return trading.NewReject("BOT_HAS_PRIORITY")
			}
		}
		return disc.SpotGuard(acct, inst, side)
	}
	// 用户自己的信号（D48）：TradingView 警报和 Webhook
	signals := sigs.New(&sigs.Engines{Perps: trader, Spots: spotEng, Live: liveDesks}, log)
	signals.Owns = func(ctx context.Context, user, acct int64) bool {
		if live.IsLive(acct) {
			return liveStore.Ready() && liveStore.Owns(ctx, user, acct)
		}
		return users.OwnsPaper(ctx, user, acct)
	}
	signals.RealMoney = live.IsLive // 给交易所账户建信号地址要二级密码（D107）
	signals.Known = func(inst string) bool {
		if spotEng.Specs[inst] != nil {
			return true
		}
		_, ok := trader.Specs.Get(inst)
		return ok
	}
	signals.Routes(mux, users)
	// 跨所对比（可视化 5、B）、经济日历（A）、系统控制台（11）
	vcoins := make([]string, len(instruments))
	for i, in := range instruments {
		vcoins[i] = strings.TrimSuffix(in, "-PERP")
	}
	vsvc := &venues.Service{Books: books, Primary: primary, Coins: vcoins, HL: cfg.Hyperliquid}
	// FRED_API_KEY：部署的人的免费密钥（圣路易斯联储），有了它经济日历全自动更新（D102）
	fredKey := strings.TrimSpace(os.Getenv("FRED_API_KEY"))
	cal := &calendar.Service{Bars: bars.Remote, Log: log, FREDKey: fredKey}
	// 新闻标题（18 第 8.6 条）：默认几家加密媒体的公开 RSS；X 只在部署的人填了自己的 token 时取
	newsSvc := news.FromEnv(vcoins, log)
	if simSrc, ok := src.(*market.Sim); ok {
		vsvc.Sim = simSrc.Price
		cal.Sim = true
		if os.Getenv("NEWS_FEEDS") == "" { // 模拟行情（离线、测试）默认不出去取新闻
			newsSvc.Feeds = nil
		}
		if os.Getenv("NEWS_NOTICE_BASE") == "" { // 交易所公告也一样（测试环境指到本机的假接口）
			newsSvc.Notices = false
		}
		mux.HandleFunc("POST /api/dev/news-poll", func(w http.ResponseWriter, r *http.Request) {
			newsSvc.PollOnce(r.Context(), true)
			w.WriteHeader(http.StatusNoContent)
		})
	}
	vsvc.Routes(mux)
	cal.Routes(mux)
	newsSvc.Routes(mux)
	go newsSvc.Run(ctx)
	// 系统设置（admin/settings.go）：发信邮箱、AI 复盘点评、Telegram、数据源密钥，管理后台改了马上生效
	sysSet := newSys(ctx, adminStore, liveStore, sysDeps{cfg: cfg, mail: mailLive, reviews: reviews, tg: notifier.TG, cal: cal, scanPub: scanPub, news: newsSvc, specs: specs, log: log})
	adminAPI.Sys = sysSet
	go vsvc.Run(ctx)
	sysSvc := &system.Service{Meter: meter, Hub: hub, Started: time.Now(), Heat: func() int { return len(heats.Instruments()) },
		Bots: botMgr.Running, Algos: algos.Active, Follows: followers.RunningCount, Signals: signals.Received.Load, Pool: func() *pgxpool.Pool {
			if database == nil {
				return nil
			}
			return database.Pool
		}}
	sysSvc.Polls = func() []system.Poll {
		out := []system.Poll{}
		st := vsvc.Polls()
		for _, k := range []string{"bybit", "okx"} {
			if v, ok := st[k]; ok {
				out = append(out, system.Poll{ID: k, Ms: v.Ms, OK: v.OK, At: v.At})
			}
		}
		if chainScan != nil {
			m := chainScan.LiqMap("BTC", 1, 0.01, 1)
			out = append(out, system.Poll{ID: "chain", OK: m.CycleEnd > 0, At: m.CycleEnd, N: m.Addresses})
		}
		return out
	}
	sysSvc.Routes(mux)
	(&spot.API{E: spotEng, Perps: trader, Users: users, Busy: followers.AccountRunning}).Routes(mux)
	(&follow.API{M: followers, Users: users, Gate: userGate}).Routes(mux)
	(&discipline.API{M: disc, Users: users, Gate: userGate}).Routes(mux)
	// 交易日志和复盘（模块 5）
	journalStore := &journal.Store{}
	jAPI := &journal.API{Users: users, Gate: userGate, Engine: trader, Store: journalStore, Review: reviews, Log: log,
		Spot: func(acct int64) []trading.Fill {
			list := spotEng.Fills(acct)
			in := make([]journal.SpotFill, 0, len(list))
			for _, f := range list {
				in = append(in, journal.SpotFill{Seq: f.Seq, Time: f.Time, OrderID: f.OrderID, Inst: f.Instrument, Side: f.Side, Qty: f.Qty, Px: f.Px, Fee: f.Fee, FeeUSDT: f.FeeUSDT, FeeBase: f.Side == "buy", Maker: f.Maker, Source: f.Source})
			}
			return journal.FromSpot(in, func(inst string) decimal.Decimal {
				if s := spotEng.Specs[inst]; s != nil {
					return s.Step
				}
				return decimal.Zero
			})
		},
		PerTrade: disc.PerTradeLimit,
		Live:     liveDesks,
		LiveAccount: func(ctx context.Context, user, acct int64) (*auth.PaperAccount, bool) {
			if !liveStore.Ready() || !liveStore.Owns(ctx, user, acct) {
				return nil, false
			}
			p := &auth.PaperAccount{ID: acct, Initial: "0"}
			if list, err := liveStore.Accounts(ctx, user); err == nil {
				for _, a := range list {
					if a.ID == acct {
						p.Name = a.Name
					}
				}
			}
			if v, err := liveDesks.View(acct); err == nil {
				p.Initial = v.Initial.String()
			}
			return p, true
		},
		Rejects: func(user, acct int64) []journal.Reject {
			evs, err := disc.History(user, acct)
			if err != nil {
				return nil
			}
			out := []journal.Reject{}
			for _, e := range evs {
				if e.Kind == "reject" {
					out = append(out, journal.Reject{T: e.Time, Inst: e.Inst, Rule: e.Rule})
				}
			}
			return out
		},
		// 价格路径：6 个重点币录到了就用逐笔成交，否则用 1 分钟 K 线，太长的用 5 分钟
		Path: func(ctx context.Context, inst string, from, to int64) ([]journal.Pt, string) {
			if ts := heats.Trades(ctx, inst, from, to); len(ts) > 0 {
				pts := make([]journal.Pt, len(ts))
				for i, x := range ts {
					pts[i] = journal.Pt{T: x.T, Hi: x.P, Lo: x.P, C: x.P}
				}
				return pts, "ticks"
			}
			iv, src := int64(60_000), "1m"
			if (to-from)/iv > 990 {
				iv, src = 300_000, "5m"
			}
			bars := bars.Query(ctx, inst, iv, 0, to+iv, int(min((to-from)/iv+2, 1000)))
			pts := make([]journal.Pt, 0, len(bars))
			for _, b := range bars {
				if b.T+iv > from && b.T < to {
					pts = append(pts, journal.Pt{T: b.T, Hi: b.H, Lo: b.L, C: b.C})
				}
			}
			return pts, src
		},
	}
	jAPI.Routes(mux)
	(&highlight.API{Users: users, Gate: userGate, S: hlStore, Scene: highlightScene(cal, chainScan, flows, heats, bars)}).Routes(mux)
	// 风险页：联动系数用 1 小时收盘价算
	closes := (&marketmap.Binance{}).Closes
	if simMap != nil {
		closes = simMap.Closes
	}
	(&risk.API{Spot: spotEng.Equity, BotOn: botMgr.BotOn, Users: users, Gate: userGate, Trader: trader, Betas: &risk.Betas{Closes: closes}, Manual: followers.ManualInsts, History: &risk.History{FAPI: cfg.BinanceBase}}).Routes(mux)
	verifyRoutes(mux, users, func() *store.Store { return st }, lookupPF, closes, log)
	if cfg.DatabaseURL == "" {
		go followers.Run(ctx)
		go disc.Run(ctx)
	}
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-database.Ready():
		}
		users.Pool = database.Pool
		mctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		// 最先接：重启后"今日"、筛选、异动马上用上一次的，不等后面恢复交易、补资金费这些联网的步骤（scan/warm.go）
		if err := scanSvc.Attach(mctx, database.Pool); err != nil {
			log.Error("筛选缓存表", "err", err)
		}
		if err := users.Migrate(mctx); err != nil {
			log.Error("auth migrate", "err", err)
			return
		}
		adminStore.Pool = database.Pool
		if err := adminStore.Migrate(mctx); err != nil {
			log.Error("admin migrate", "err", err)
			return
		}
		// 管理后台填过的系统设置：读回来交给各组件（解不开的项跳过，用配置文件里的）
		if err := sysSet.Load(mctx); err != nil {
			log.Warn("系统设置", "err", err)
		}
		for _, g := range []string{"mail", "ai", "tg", "data"} {
			sysSet.Apply(g)
		}
		if err := adminAPI.Load(mctx); err != nil {
			log.Error("admin load", "err", err)
			return
		}
		binapiStore.Pool = database.Pool
		if err := binapiStore.Migrate(mctx); err != nil {
			log.Error("binapi migrate", "err", err)
			return
		}
		binapiReady.Store(true)
		stratStore.Pool = database.Pool
		if err := stratStore.Migrate(mctx); err != nil {
			log.Error("strategy migrate", "err", err)
			return
		}
		// 等模型组合从库里恢复完（另一个协程）再接上自助建的策略，不然会建出一个空的覆盖掉
		go func() {
			for !tradingReady.Load() {
				select {
				case <-ctx.Done():
					return
				case <-time.After(200 * time.Millisecond):
				}
			}
			ks, err := stratStore.Active(context.Background())
			if err != nil {
				log.Error("strategy keys", "err", err)
				return
			}
			for _, k := range ks {
				stratOn(k)
			}
			stratReady.Store(true)
			if len(ks) > 0 {
				log.Info("自助建的策略已接上", "count", len(ks))
			}
		}()
		tst := &trading.Store{Pool: database.Pool}
		if err := tst.Migrate(mctx); err != nil {
			log.Error("trading migrate", "err", err)
			return
		}
		evs, err := tst.Load(mctx)
		if err == nil {
			trader.SetPersist(func(b []trading.Event) error {
				wctx, c := context.WithTimeout(context.Background(), 10*time.Second)
				defer c()
				return tst.Append(wctx, b)
			})
			err = trader.Restore(evs)
		}
		if err != nil {
			log.Error("trading restore", "err", err)
			return
		}
		go trader.RunPersist(ctx)
		papers, err := users.ActivePapers(mctx)
		if err != nil {
			log.Error("list paper accounts", "err", err)
			return
		}
		opened := 0
		for _, p := range papers {
			if !trader.Has(p.ID) {
				trader.Open(p.ID, decimal.RequireFromString(p.Initial))
				opened++
			}
		}
		// 后端没运行期间错过的资金费结算（电脑睡眠、关机）：从币安取真实记录补扣，按结算时刻去重
		if last := trader.LastEventTime(); primary == "binance-usdm" && last > 0 {
			now := time.Now().UnixMilli()
			from := max(last, now-30*24*3600*1000)
			total := 0
			for i, sym := range cfg.Symbols {
				recs, err := trading.FetchFundingHistory(mctx, "", sym, from, now)
				if err != nil {
					// 记下来稍后重试（按现在的持仓、只补到现在），不能就这么算了（11-18）
					log.Warn("补资金费：取币安结算记录失败，稍后重试", "symbol", sym, "err", err)
					trader.FundingGapAtBoot(instruments[i], from-1, now)
					continue
				}
				total += trader.CatchUpFunding(instruments[i], recs, last)
			}
			if total > 0 {
				log.Info("补上了后端停机期间的资金费", "count", total, "since", time.UnixMilli(last).Format(time.RFC3339))
			}
		}
		if primary == "binance-usdm" {
			trader.FundingHistory = func(ctx context.Context, inst string, from, to int64) ([]trading.FundingSettle, error) {
				return trading.FetchFundingHistory(ctx, "", strings.TrimSuffix(inst, "-PERP")+"USDT", from, to)
			}
			// 断流补算（审查第 4 条）：行情断了一段时，用这段的 1 分钟 K 线补查止损、止盈和强平
			trader.Bars1m = func(ctx context.Context, inst string, from, to int64, mark bool) ([]trading.Bar1m, error) {
				return trading.FetchBars1m(ctx, "", strings.TrimSuffix(inst, "-PERP")+"USDT", from, to, mark)
			}
			// 后端没运行的那段（电脑睡眠、关机）：从这个品种最后录到行情的那一分钟补算到现在，最多往回 24 小时
			boot := time.Now().UnixMilli()
			for _, inst := range trader.ActiveInstruments() {
				var last *int64
				if database.Pool.QueryRow(mctx, `select max(t) from heat_chunks where instrument=$1 and t < $2`, inst, boot).Scan(&last) == nil && last != nil {
					trader.AddGap(inst, max(*last+60_000, boot-24*3600_000), boot)
				}
			}
		}
		for _, inst := range trader.ActiveInstruments() {
			ensureInst(inst)
		}
		go trader.Run(ctx)
		// 交易所账户要在恢复跟随之前接上：跟随可能在交易所账户上（18 第 5.2 条），恢复时要撤它的挂单、记下成交序号
		if err := liveStore.Attach(mctx, database.Pool); err != nil {
			log.Error("实盘表", "err", err)
		} else if err := liveDesks.Start(ctx); err != nil {
			log.Error("实盘账户", "err", err)
		} else if accts, err := liveStore.Accounts(mctx, 0); err == nil {
			for _, a := range accts {
				disc.Register(a.User, a.ID)
			}
			if len(accts) > 0 {
				log.Info("实盘账户已接上", "accounts", len(accts))
			}
		}
		fst := &follow.Store{Pool: database.Pool}
		if err := fst.Migrate(mctx); err != nil {
			log.Error("follow migrate", "err", err)
			return
		}
		fevs, err := fst.Load(mctx)
		if err != nil {
			log.Error("follow restore", "err", err)
			return
		}
		followers.Persist = func(b []follow.Event) error {
			wctx, c := context.WithTimeout(context.Background(), 10*time.Second)
			defer c()
			return fst.Append(wctx, b)
		}
		followers.Restore(fevs)
		go followers.Run(ctx)
		if chainScan != nil {
			// 上次的扫描结果先用上，不用等第一轮扫完（V.20）
			if err := chainScan.Persist(ctx, database.Pool); err != nil {
				log.Error("onchain persist", "err", err)
			}
		}
		if err := spotEng.Attach(mctx, database.Pool); err != nil {
			log.Error("现货表", "err", err)
		}
		go spotEng.Run(ctx)
		if err := botMgr.Attach(mctx, database.Pool); err != nil {
			log.Error("机器人表", "err", err)
		}
		go botMgr.Run(ctx)
		if err := algos.Attach(mctx, database.Pool); err != nil {
			log.Error("算法单表", "err", err)
		}
		go algos.Run(ctx)
		if err := signals.Attach(mctx, database.Pool); err != nil {
			log.Error("信号表", "err", err)
		}
		if err := cal.Attach(mctx, database.Pool); err != nil {
			log.Error("经济日历表", "err", err)
		}
		go cal.Run(ctx)
		if err := heat.Migrate(mctx, database.Pool); err != nil {
			log.Error("heat migrate", "err", err)
		} else {
			heats.SetPool(database.Pool)
		}
		journalStore.Pool = database.Pool
		if err := journalStore.Migrate(mctx); err != nil {
			log.Error("journal migrate", "err", err)
			return
		}
		dst := &discipline.Store{Pool: database.Pool}
		if err := dst.Migrate(mctx); err != nil {
			log.Error("discipline migrate", "err", err)
			return
		}
		devs, err := dst.Load(mctx)
		if err != nil {
			log.Error("discipline load", "err", err)
			return
		}
		disc.Persist = func(b []discipline.Event) error {
			wctx, c := context.WithTimeout(context.Background(), 10*time.Second)
			defer c()
			return dst.Append(wctx, b)
		}
		disc.Restore(devs)
		for _, p := range papers {
			disc.Register(p.UserID, p.ID)
		}
		go disc.Run(ctx)
		close(traderReady)
		if err := reviews.Attach(mctx, database.Pool); err != nil {
			log.Error("AI 点评表", "err", err)
		}
		if err := notifier.Attach(mctx, database.Pool); err != nil {
			log.Error("通知表", "err", err)
		}
		notifier.StartWebhooks(ctx)
		// 每日复盘（18 第 8.4 条）：UTC 零点过后，把昨天有动静的账户（模拟账户和交易所账户）的流水推给主人
		go runRecaps(ctx, database.Pool, notifier, func(c context.Context) []recapAcct {
			var out []recapAcct
			if ps, err := users.ActivePapers(c); err == nil {
				for _, a := range ps {
					out = append(out, recapAcct{User: a.UserID, ID: a.ID, Name: a.Name})
				}
			}
			if liveStore.Ready() {
				if ls, err := liveStore.Accounts(c, 0); err == nil {
					for _, a := range ls {
						out = append(out, recapAcct{User: a.User, ID: a.ID, Name: a.Name})
					}
				}
			}
			return out
		}, func(acct int64) ([]trading.Fill, []trading.FundingRec, error) {
			if live.IsLive(acct) {
				f, fu, _, err := liveDesks.Records(acct)
				return f, fu, err
			}
			f, fu, _, err := trader.Records(acct)
			return f, fu, err
		}, log)
		if err := hlStore.Attach(mctx, database.Pool); err != nil {
			log.Error("高光表", "err", err)
		}
		if err := latPrefs.Attach(mctx, database.Pool); err != nil {
			log.Error("下单延迟设置表", "err", err)
		}
		userGate.Open()
		ns := notifySources(database.Pool, trader, disc, botMgr, followers, chainScan, cal, books, primary)
		ns.Metrics = scanSvc.Metrics           // 自定义提醒条件（18 第 10.4 条）
		notifier.RuleMetrics = scanSvc.Metrics // 加规则、恢复规则时按此刻的数字定起点
		go (&notify.Watcher{S: notifier, Src: ns}).Run(ctx)
		go (&highlight.Watcher{S: hlStore, Log: log, Src: highlightSources(database.Pool, trader, liveDesks, jAPI), Push: func(ctx context.Context, h *highlight.Highlight) {
			notifier.Push(ctx, h.User, notify.KHigh, highlightNotice(h))
		}}).Run(ctx)
		log.Info("账户和手动交易就绪", "trading_events", len(evs), "paper_accounts", len(papers), "newly_opened", opened)
		if cfg.AuthDev {
			log.Warn("账户：开发模式（没配发信邮箱，验证码显示在页面上、打在日志里；对外开放前配 SMTP_*，配了自动关）")
		}
	}()
	// 只有模拟行情才有：把价格直接设到某个值，测试环境用来造强平、止损这类场景（D75）。真实行情下这个接口不存在
	if sim, ok := src.(*market.Sim); ok {
		mux.HandleFunc("POST /api/dev/sim-price", func(w http.ResponseWriter, r *http.Request) {
			var in struct {
				Instrument string  `json:"instrument"`
				Price      float64 `json:"price"`
			}
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Price <= 0 {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			for i, sym := range cfg.Symbols {
				if instruments[i] == in.Instrument {
					sim.SetPrice(sym, in.Price)
					w.WriteHeader(http.StatusNoContent)
					return
				}
			}
			http.Error(w, "unknown instrument", http.StatusNotFound)
		})
	}
	mux.Handle("/", front.Handler())
	// 按来源限流（18 第 6.8 条）：每秒 30 个请求、最多攒 300 个；本机直连不限
	limited := (&ratelimit.Limiter{Rate: 30, Burst: 300, IP: auth.ClientIP}).Wrap(mux)
	srv := &http.Server{Addr: cfg.HTTPAddr, Handler: limited, ReadHeaderTimeout: 10 * time.Second}
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	if demo != nil {
		// 示例策略：等行情到了再开始发随机信号（只用于测试链路，界面上要标明）
		go func() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(15 * time.Second):
			}
			addr := cfg.HTTPAddr
			if strings.HasPrefix(addr, ":") {
				addr = "127.0.0.1" + addr
			}
			fakesig.Run(ctx, fakesig.Config{BaseURL: "http://" + addr, KeyID: demo.ID, Secret: demo.Secret, StrategyID: demo.StrategyID,
				Instruments: instruments[:min(3, len(instruments))], Every: 45 * time.Second, Log: log})
		}()
	}
	log.Info("listening", "addr", cfg.HTTPAddr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("server error", "err", err)
		os.Exit(1)
	}
	// 等处理中的请求结束，再把最后的事件写进数据库，不然关机前的成交会丢
	<-shutdownDone
	if n := trader.Drain(3 * time.Second); n > 0 {
		log.Error("关机时还有事件没写进数据库", "count", n)
	}
	disc.Flush()
	{
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		heats.Flush(c)
		if chainScan != nil {
			_ = chainScan.Save(c)
		}
		cancel()
	}
	if n := followers.Flush(); n > 0 {
		log.Error("关机时还有跟随事件没写进数据库", "count", n)
	}
	log.Info("stopped")
}

// notifySources 通知要看的东西。都是读内存，账户列表读一次库。
func notifySources(pool *pgxpool.Pool, trader *trading.Engine, disc *discipline.Manager, botMgr *bots.Manager, followers *follow.Manager, chain *onchain.Scanner, cal *calendar.Service, books *book.Store, primary string) notify.Sources {
	f64 := func(d decimal.Decimal) float64 { v, _ := d.Float64(); return v }
	src := notify.Sources{
		Accounts: func(ctx context.Context) []notify.AcctRef {
			rows, err := pool.Query(ctx, `select id, user_id, name from paper_accounts where archived_at is null`)
			if err != nil {
				return nil
			}
			defer rows.Close()
			out := []notify.AcctRef{}
			for rows.Next() {
				var a notify.AcctRef
				if rows.Scan(&a.ID, &a.User, &a.Name) == nil {
					out = append(out, a)
				}
			}
			return out
		},
		Snap: func(acct int64) (*notify.Snap, bool) {
			v, err := trader.View(acct)
			if err != nil {
				return nil, false
			}
			s := &notify.Snap{Ratio: f64(v.MarginRatio), Rule: disc.LockRule(acct)}
			s.Locked = s.Rule != ""
			for _, p := range v.Positions {
				s.Positions = append(s.Positions, notify.PosSnap{Inst: p.Instrument, Mark: f64(p.Mark), Liq: f64(p.LiqPx)})
			}
			for _, n := range v.Notices {
				ns := notify.NoticeSnap{Seq: n.Seq, Kind: n.Kind, Inst: n.Instrument, Px: f64(n.Px), Loss: f64(n.Loss)}
				if n.Kind == "liquidation" && n.Reason == "partial" {
					ns.Partial = n.Qty.String()
				}
				s.Notices = append(s.Notices, ns)
			}
			return s, true
		},
		FillSeq: trader.FillSeq,
		Fills: func(acct, after int64) []notify.FillSnap {
			list := trader.FillsAfter(acct, after)
			out := make([]notify.FillSnap, len(list))
			for i, f := range list {
				out[i] = notify.FillSnap{Seq: f.Seq, Inst: f.Instrument, Side: f.Side, Qty: f64(f.Qty), Px: f64(f.Px), Liq: f.Liq}
			}
			return out
		},
		Bots: func(user int64) []notify.BotSnap {
			list := botMgr.List(user)
			out := make([]notify.BotSnap, len(list))
			for i, b := range list {
				out[i] = notify.BotSnap{ID: b.ID, Inst: b.Params.Instrument, Reason: b.Reason, Paused: b.Status == bots.StPaused}
			}
			return out
		},
		Follows: func(user int64) []notify.FollowSnap {
			list := followers.List(&auth.User{ID: user})
			out := make([]notify.FollowSnap, len(list))
			for i, f := range list {
				out[i] = notify.FollowSnap{ID: f.ID, Stopped: f.Status == follow.StStopped, Reason: f.Reason}
			}
			return out
		},
		Price: func(inst string) float64 {
			if t, ok := books.Ticker(primary, inst); ok {
				if v := f64(t.Mark); v > 0 {
					return v
				}
				return f64(t.Last)
			}
			return 0
		},
		Econ: func() []notify.EconSnap {
			list, _ := cal.List()
			out := make([]notify.EconSnap, len(list))
			for i, e := range list {
				out[i] = notify.EconSnap{T: e.T, Key: e.Key}
			}
			return out
		},
	}
	if chain != nil {
		src.Whales = func() []notify.WhaleSnap {
			list := chain.Events("", notify.WhaleMin, 30)
			out := make([]notify.WhaleSnap, len(list))
			for i, e := range list {
				out[i] = notify.WhaleSnap{T: e.Time, Coin: e.Coin, Kind: e.Kind, Value: e.Value}
			}
			return out
		}
	}
	return src
}

// parseKeys 解析 "key_id:secret:strategy_id;key_id2:secret2:strategy_id2"
func parseKeys(s string) []qsig.Key {
	var out []qsig.Key
	for _, part := range strings.Split(s, ";") {
		f := strings.Split(strings.TrimSpace(part), ":")
		if len(f) == 3 && f[0] != "" && f[1] != "" && f[2] != "" {
			out = append(out, qsig.Key{ID: f[0], Secret: f[1], StrategyID: f[2]})
		}
	}
	return out
}
