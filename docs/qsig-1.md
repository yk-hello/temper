# qsig/1 信号协议

中文 | [English](qsig-1.en.md)

程序化策略和平台之间的约定：策略每做一次判断，就发一条"每个品种应该持有多少"的信号；平台负责下单、止损止盈和风控。
机器可读的 Schema：[`protocol/qsig-1.schema.json`](protocol/qsig-1.schema.json)；示例：[`protocol/qsig-1.example.signal.json`](protocol/qsig-1.example.signal.json)；
发送示例：[`examples/qsig_send.py`](examples/qsig_send.py)。

协议只新增可选字段；破坏性修改会升级到 qsig/2。

## 1. 核心语义

- 策略每做一次判断就发一条信号，内容是每个品种的**目标权重**（应该持有多少），不是买卖动作。
- `weight` = 目标仓位名义价值 ÷ 该策略模型组合的净值，带符号：正数做多，负数做空，0 空仓。
  - 单个品种 |weight| ≤ 1
  - 所有品种 |weight| 之和 ≤ 2（最多 2 倍总杠杆）
  - 超限时**整条消息拒绝**，不做缩放
- 重复发同一个目标 = 维持现状。平台**不会**因为价格涨跌自动把仓位调回目标比例。
- 新目标和当前实际仓位的差的绝对值 < 0.01 时不交易（最小调整阈值）。
- 第一版只支持永续合约。品种用平台统一代码（`BTC-PERP` 表示稳定币保证金的线性永续），平台映射到各交易所。
- `confidence`（0–1）只用于展示和用户筛选，**不参与仓位计算**。

## 2. 消息类型

所有消息共有的字段：`protocol`（固定 "qsig/1"）、`type`、`id`（UUID）、`strategy_id`、`strategy_version`、`issued_at`。

### signal

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| data_as_of | 是 | 这次判断用到的最新一条数据的时间 |
| valid_until | 是 | 晚于 issued_at，最多晚 5 分钟（建议 60 秒）。收到时已过期则不执行 |
| mode | 是 | `partial`：只调整列出的品种（推荐）。`snapshot`：列出的就是完整持仓，没列出的全部平仓 |
| targets | 是 | 1–50 项，**不能为空**（防止程序出错时一条空列表误平全仓）。想全平请发 halt 或逐个写 0 |
| meta | 否 | 模型、提示词版本、耗时、推理花费（cost_usd）等，只用于展示 |

targets 中每一项：

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| instrument | 是 | 如 `BTC-PERP`，必须在 `/v1/instruments` 列表里 |
| weight | 是 | 字符串，-1 到 1，最多 4 位小数 |
| confidence | 是 | 数字，0–1 |
| horizon_min | 是 | 预计持有多少分钟，5–10080 |
| ref_price | 是 | 字符串，决策瞬间重新取到的**原始**市场价，不扣手续费、不扣滑点 |
| ref_price_source | 是 | 第一版固定 `binance_usdm_last`（币安 U 本位永续最新成交价） |
| execution | 否 | `urgency`：passive（只挂单）、normal（默认）、immediate（立即吃单）；`max_slippage_bps`：默认 20 |
| exits | 否 | `stop_loss_pct`、`take_profit_pct`：相对入场均价的比例，字符串 |
| rationale | 是 | `summary.zh`（≤120 字，必填）、`summary.en`；`factors`（≤12 个：key、label、score∈[-1,1]）；`evidence`（≤10 条：kind、title、source、url、published_at、excerpt≤200 字） |

rationale 显示在"AI"页的决策流里。

### heartbeat
- 字段：`status`（ok / degraded / paused）、可选 `note`
- 每 30–60 秒发一次
- 超过 180 秒没收到（发出的信号也算一次），判定策略离线：持仓保持、止损照常、界面显示"策略离线"，并按通知设置提醒策略的主人和正在跟它的人；恢复时再提醒一次。从没发过心跳的策略不提醒

### halt
- 字段：`action`（`flatten_all`：全部平仓并暂停；`pause`：不再加仓，持仓不动）、`reason.zh`
- halt 之后**只能由部署的人手动恢复**。不允许心跳自动恢复，防止程序重启后误恢复。

## 3. 格式规则

- 时间一律 UTC，精确到毫秒，以 Z 结尾，如 `2026-11-03T08:30:12.345Z`。
- 影响下单数量的数字（weight、ref_price、exits）一律用字符串。
- 同一品种按 `issued_at` 判断先后：比该品种已执行的判断更早的信号，晚到了会被丢弃（错误码 STALE）。**没有单独的序号字段。**
- 一条消息要么整体接受，要么整体拒绝。
- 平台忽略不认识的字段。
- 同一个 id 重复发送且内容相同，按重复处理（status: duplicate）；同一个 id 内容不同，拒绝（ID_CONFLICT）。

## 4. 接口（平台实现）

- 所有消息走一个入口：`POST /v1/messages`。
- 查询接口（同样需要签名）：

| 接口 | 返回 |
| --- | --- |
| `GET /v1/instruments` | 可交易品种及元数据 |
| `GET /v1/ticker?instrument=BTC-PERP` | 最新价、买一卖一、标记价格、资金费率、下次结算时间、时间戳 |
| `GET /v1/portfolio` | 模型组合的净值和现金；每个品种的目标权重、实际权重、数量、入场价、标记价、浮动盈亏、目标设定时间；被平台平仓的原因（closed_by：stop_loss / risk / circuit_breaker / halt）；是否处于熔断 |
| `GET /v1/messages/{id}` | 状态（accepted / rejected / duplicate）和执行回报：每个品种的原权重、目标权重、实际达到的权重、执行状态、成交均价、相对 ref_price 的滑点（bps）、手续费、开始和结束时间 |
| `GET /v1/performance?window=7d\|30d\|all` | 绩效指标 |

执行状态（state）的取值：

- `filled`：全部成交
- `partial`：部分成交
- `skipped_price_moved`：执行前价格已偏离 ref_price 超过 max_slippage_bps，不执行
- `skipped_no_change`：目标和之前相同，维持现状
- `skipped_min_adjust`：调整幅度小于 0.01，不执行

### 签名
- 请求头：
  - `X-QS-Key`：策略的 key id
  - `X-QS-Timestamp`：毫秒时间戳
  - `X-QS-Signature`：HMAC-SHA256 签名，小写十六进制
- 待签名字符串：`timestamp + "\n" + METHOD + "\n" + path（含查询参数）+ "\n" + body`。GET 请求的 body 为空字符串。**签名基于实际发送的原始字节。**
- 请求头时间与平台时间相差超过 30 秒拒绝（TIMESTAMP_SKEW）。
- 每个环境（测试、生产）单独一套密钥。密钥可以自助建（"账户 → 我的策略"，secret 只显示一次，可以换、可以吊销），也可以由部署的人写在环境变量 `QSIG_KEYS` 里。strategy_id 必须与 key 绑定。
- 收到的每条消息都进该策略的前向记录（哈希链），见 [strategies.md](strategies.md) 的"前向记录"。

### 回复与错误码

回复体统一为：`{status, id, received_at, code?, message?, details?, warnings?}`。

| HTTP | code | 含义 | 是否重试 |
| --- | --- | --- | --- |
| 200 | — | accepted 或 duplicate | — |
| 400 | INVALID_JSON / SCHEMA_VIOLATION（details.path）/ UNSUPPORTED_PROTOCOL | 格式问题 | 否 |
| 401 | AUTH_FAILED / TIMESTAMP_SKEW | 签名错误或时钟不准 | 否 |
| 403 | STRATEGY_MISMATCH | strategy_id 与 key 不符 | 否 |
| 409 | ID_CONFLICT | 同一 id 内容不同 | 否 |
| 422 | EXPIRED / STALE / UNKNOWN_INSTRUMENT / INSTRUMENT_NOT_TRADABLE / WEIGHT_LIMIT_EXCEEDED / CIRCUIT_BREAKER / STRATEGY_PAUSED | 业务规则拒绝 | 否 |
| 429 | RATE_LIMITED | 每个策略每分钟最多 30 条信号 | 是，退避重试 |
| 5xx | INTERNAL_ERROR | 平台故障 | 是 |

- **CIRCUIT_BREAKER 和 STRATEGY_PAUSED 只拒绝加仓类信号。** 减仓和平仓信号任何时候都接受。
- **warnings**（接受但提醒）：
  - REF_PRICE_DEVIATION：收到时 ref_price 与平台价格偏差超过 0.5%，可能是价格来源取错了
  - LONG_LATENCY：issued_at − data_as_of 超过 600 秒
- **重试规则**：网络超时、429 和 5xx 时，用同一个 id、同样的内容重发，签名头重新计算；过了 valid_until 就放弃。

## 5. 平台执行规则

- 模型组合初始净值 10,000 USDT。在真实行情里模拟交易：按真实盘口逐档成交，吃单手续费 0.05%、挂单 0.02%，资金费按交易所实际结算。
- 执行前检查：当前价格偏离 ref_price 超过 max_slippage_bps，就不执行（skipped_price_moved）。成交过程中均价不能超过这个上限，超出的剩余部分撤单。
- 三种 urgency 的执行方式：
  - passive：在最优价挂单，60 秒或信号过期仍未成交就放弃
  - normal：先挂限价单，剩余部分在滑点上限内转为吃单
  - immediate：在滑点上限内直接吃单
- 止损止盈：用标记价格监控。触发后吃单平仓，该品种目标置为 0，并记录 closed_by。
- horizon 到期但没有新信号：第一版不自动平仓，只在界面和组合状态里标记"过期"。
- 单日（UTC）亏损超过 5% 熔断：当天剩余时间只接受减仓和平仓。
