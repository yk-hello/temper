# 更新日志 / Changelog

格式参考 [Keep a Changelog](https://keepachangelog.com/)，版本号按 [语义化版本](https://semver.org/)。
Format follows Keep a Changelog; versions follow Semantic Versioning.

## [未发布 / Unreleased]

第一个公开版本 / First public version.

### 有什么 / Included

- 行情：币安 U 本位永续的实时盘口、成交、K 线；多图同屏；Hyperliquid 链上大额持仓和强平地图；跨所资金费和价差；市场页"今日"（关键价位、经济日历、异动、新闻标题）。
  Market data: Binance USDⓈ-M live book, trades and candles; multi-chart layout; Hyperliquid on-chain positions and liquidation map; cross-venue funding and spreads; a "Today" overview with key levels, economic calendar, movers and headlines.
- 模拟盘：逐条对齐币安的撮合、手续费、保证金分档、强平、条件单、资金费；分地区的下单延迟；算法单（分批挂单、追价、TWAP、冰山单、按成交量比例、跟踪止损、分批止盈）。
  Paper trading that follows Binance rules item by item, with regional latency simulation and algo orders (scale, chase, TWAP, iceberg, percent of volume, trailing stop, laddered take-profit).
- 回测：和模拟盘同一个撮合引擎，跑币安历史 1 分钟 K 线和资金费。
  Backtesting on the same matching engine with Binance 1-minute history and funding.
- 接策略：TradingView / Webhook、qsig/1 目标仓位协议（前向记录、验证报告）、和币安一样的接口；"只提醒，不下单"的调仓推送。
  Strategy connections: TradingView / webhooks, the qsig/1 target-position protocol (forward record, validation report), a Binance-compatible API, and notify-only rebalance alerts.
- 实盘：接自己的币安合约账户（只开交易权限，开了提现、划转的密钥拒绝保存），跟随引擎每个账户各自排队。
  Live trading on your own Binance futures account (trading-only keys; keys with withdrawal or transfer permission are refused); per-account follow queues.
- 复盘、纪律锁、团队（所有者、管理员、成员、全局停机、操作记录）、二次验证、通知（站内、Telegram、邮件、Webhook、Discord、Slack，每日复盘）、自定义快捷键。
  Review, discipline locks, teams (owner, admins, members, global halt, audit log), two-factor sign-in, notifications (in-app, Telegram, email, webhooks, Discord, Slack, daily recap), customizable shortcuts.
- 一条命令启动（`docker compose up -d`），不需要任何密钥；推 `v*` 标签时自动发布 Docker 镜像。
  One-command start with no keys required; Docker images are published automatically on `v*` tags.
