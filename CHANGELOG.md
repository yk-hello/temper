# 更新日志 / Changelog

格式参考 [Keep a Changelog](https://keepachangelog.com/)，版本号按 [语义化版本](https://semver.org/)。
Format follows Keep a Changelog; versions follow Semantic Versioning.

每个版本在 GitHub 上打一个 `vX.Y.Z` 标签。怎么升级、固定用某个版本、退回上一个版本，见常见问题的"升级和退回"（[简体中文](docs/faq.md)、[English](docs/faq.en.md)）。升级不会丢数据、账号和 `.env` 里的设置。
Each release is tagged `vX.Y.Z` on GitHub. To upgrade, stay on a version or go back to the previous one, see "Upgrading and rolling back" in the FAQ. Upgrades keep your data, accounts and `.env` settings.

## [未发布 / Unreleased]

- 网页镜像 `qs-web` 的打包（`npm ci`、`vite build`）只在构建机器自己的架构上跑一次，再放进各架构的 nginx 镜像。v0.1.0 发布时 arm64 那一路在模拟环境里打包，6 小时没跑完被 GitHub 停掉，所以 v0.1.0 只有后端镜像 `qs-backend`，没有 `qs-web`；从源码构建（`docker compose up -d`）不受影响。
  The web image `qs-web` now runs its build step (`npm ci`, `vite build`) once on the build machine's own architecture and copies the result into each architecture's nginx image. For v0.1.0 the arm64 build ran under emulation and was stopped by GitHub after 6 hours, so v0.1.0 has the backend image `qs-backend` but no `qs-web`; building from source (`docker compose up -d`) is unaffected.

## [0.1.0] - 2026-11-28

第一个正式版本。之前没有版本号，到今天为止的全部内容算 0.1.0。
First tagged release. Until now the project had no version numbers; everything up to today is 0.1.0.

### 有什么 / Included

- 行情：币安 U 本位永续的实时盘口、成交、K 线；多图同屏；Hyperliquid 链上大额持仓和强平地图；跨所资金费和价差；市场页"今日"（关键价位、经济日历、异动、新闻标题）。
  Market data: Binance USDⓈ-M live book, trades and candles; multi-chart layout; Hyperliquid on-chain positions and liquidation map; cross-venue funding and spreads; a "Today" overview with key levels, economic calendar, movers and headlines.
- 模拟盘：逐条对齐币安的撮合、手续费、保证金分档、强平、条件单、资金费；分地区的下单延迟；算法单（分批挂单、追价、TWAP、冰山单、按成交量比例、跟踪止损、分批止盈）。
  Paper trading that follows Binance rules item by item, with regional latency simulation and algo orders (scale, chase, TWAP, iceberg, percent of volume, trailing stop, laddered take-profit).
- 回测：和模拟盘同一个撮合引擎，跑币安历史 1 分钟 K 线和资金费。
  Backtesting on the same matching engine with Binance 1-minute history and funding.
- 接策略：TradingView / Webhook、qsig/1 目标仓位协议（前向记录、验证报告）、和币安一样的接口；"只提醒，不下单"的调仓推送；策略掉线提醒。
  Strategy connections: TradingView / webhooks, the qsig/1 target-position protocol (forward record, validation report), a Binance-compatible API, notify-only rebalance alerts and strategy offline alerts.
- 实盘：接自己的币安合约账户（只开交易权限，开了提现、划转的密钥拒绝保存），跟随引擎每个账户各自排队。
  Live trading on your own Binance futures account (trading-only keys; keys with withdrawal or transfer permission are refused); per-account follow queues.
- 复盘、纪律锁、"系统"页（只有第一个注册的账号能进：全局停机、系统设置、这台的账号、操作记录）、二次验证、通知（站内、Telegram、邮件、Webhook、Discord、Slack，每日复盘）、自定义快捷键。
  Review, discipline locks, a System page for the first account only (global halt, settings, accounts on this install, audit log), two-factor sign-in, notifications (in-app, Telegram, email, webhooks, Discord, Slack, daily recap), customizable shortcuts.
- 一条命令启动（`docker compose up -d`），不需要任何密钥；推 `v*` 标签时自动构建 Docker 镜像。
  One-command start with no keys required; Docker images are built automatically on `v*` tags.

### 最近的改动 / Recent changes

- 策略掉线提醒：策略 3 分钟没有心跳，提醒它的主人和正在跟它的人，恢复时再提醒一次。默认开，在"账户 → 通知 → 策略离线"里开关。
  Strategy offline alerts: when a strategy sends no heartbeat for 3 minutes, its owner and everyone following it get a notification, and another when it is back. On by default; switch it under Account → Notifications → Strategy offline.
- 登录、注册完回到刚才那一页，交易页还是原来的品种。
  Signing in or signing up returns you to the page that sent you there, with the same symbol on the trade page.
- 现货行情改用 `wss://stream.binance.com:443`（9443 有的网络会拦）；要换地址，设 `BINANCE_SPOT_WS_URL`，比如 `wss://data-stream.binance.vision:443`。合约不受影响；只有现货断了时，现货页面单独提示。
  Spot market data now streams from `wss://stream.binance.com:443` (9443 is blocked on some networks); set `BINANCE_SPOT_WS_URL` to use another address, such as `wss://data-stream.binance.vision:443`. Futures are unaffected; when only spot data is down, spot pages say so.
- 手机交易页：下单、盘口、自选、账户四个大按钮，持仓、委托、历史在"账户"下面一排。
  Mobile trade page: Trade, Book, Watchlist and Account are four large buttons; positions, orders and history sit in a second row under Account.
- 地址栏跟着页面变：换了品种、图表样式、标签，刷新、分享链接都还是现在这个。
  The address bar follows the symbol, chart type and tab you switch to, so refreshing or sharing the link keeps them.
- "系统"页、介绍页和 `/api/health` 里能看到版本号。
  The version is shown on the System page, the About page and in `/api/health`.
- 介绍页的盘口柱状图：几格并成一根柱子时改成加总（原来取最大那一格，电脑上平均少两成、手机上只显示四成左右）；大单成交的亮线按真实金额画（原来放大了约 5.5 倍）；悬停和两边最厚挂单的读数按整根柱子算。
  About page order-book bars: when several price steps share one bar, the bar now shows their total (it used to show the largest step, about 20% low on desktop and 60% low on phones); large-trade spikes are drawn at their real size (they were about 5.5× too tall); hover and wall readings use the whole bar.
- 频谱页：离现价最近的大单不再从价格尺上省掉（原来会和顶上"上方大单 / 下方大单"对不上）。
  Heatmap: the big orders nearest the price are no longer dropped from the price scale (they used to disagree with the "Above / Below" readout at the top).
- 副图（持仓量变化、主动买卖差、CVD 等）的数字写法统一：1000 以下按大小留位数，不再写成 −452.07。
  Indicator panes (open interest change, delta, CVD and others) format numbers consistently: below 1,000 the decimals follow the size, no more −452.07.
- 交易页阶梯线：原来放周期按钮的位置写明"逐笔 · 最近 5 分钟"（阶梯线画的是逐笔成交，没有周期可选）。
  Trade page step line: where the interval buttons sit, it now says "Every trade · last 5 min" (the step line draws individual trades, so there is no interval to pick).
- 频谱页的时间按钮"24时"改成"1日"（英文"24h"改成"1D"），和 K 线一致。
  Heatmap span "24h" now reads "1D", matching the candle intervals.
- 行情连接断了发现得更快：币安行情 10 秒没有消息就重连（原来 60 秒，网络断一下价格会在页面上停一分钟）；完整盘口 10 秒（原来 15 秒）、Hyperliquid 20 秒（原来 90 秒）；Bybit、OKX 强平推送原来不设时限，现在 50、60 秒没有消息（连 ping 的回应都没有）就重连。
  Dead connections are noticed sooner: Binance market streams reconnect after 10 s without a message (was 60 s, so a network blip froze prices for a minute); the full order book after 10 s (was 15 s), Hyperliquid after 20 s (was 90 s); Bybit and OKX liquidation streams had no limit and now reconnect after 50 and 60 s without even a ping reply.
- 阶梯线：两个点之间超过 5 秒没有收到成交的那一段画成暗的虚线，悬停写明多少秒（原来画成和平时一样的实线，像价格没动）。
  Step line: stretches of more than 5 s without a trade are drawn as a dim dashed line with the duration on hover (they used to look like a flat price).

### 升级 / Upgrading

不用改任何配置。升级前可以先备份（常见问题："数据在哪？怎么备份？"）。
No configuration changes are needed. You can back up first (FAQ: "Where is the data? How do I back it up?").
