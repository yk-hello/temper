# 策略用的数据从哪里来

> **本项目不提供策略。** 这一页只说常用的数据去哪里取、什么时候才拿得到，不说该用哪个、怎么用。

平台的 qsig/1 接口只给下单那一刻要用的价格。算指标要的历史数据，你的程序自己去数据源取。下面列的都是官方公开接口，或者要你自己申请密钥的官方接口，不爬网页。

取币安数据的小函数：[examples/marketdata.py](examples/marketdata.py)（只用标准库，不要密钥，`python marketdata.py` 自检）。

## 一、平台直接给的

签名方法见 [qsig-1.md](qsig-1.md)；[examples/qsclient.py](examples/qsclient.py) 已经包好。

| 怎么取 | 有什么 |
| --- | --- |
| `c.ticker("BTC-PERP")`（`GET /v1/ticker`） | 最新成交价、买一、卖一、标记价、资金费率、下次结算时间、时间戳 |
| `c.last("BTC-PERP")` | 只要最新成交价（字符串） |
| `c.instruments()`（`GET /v1/instruments`） | 能交易的品种 |
| `c.portfolio()`（`GET /v1/portfolio`） | 平台按你的信号跑出来的目标、仓位、净值，有没有熔断，有没有被平台平掉 |

发目标仓位时，`ref_price` 用 `c.last()` 当场取的价格。平台拿它算滑点；和平台的价格差超过 0.5% 会回一个警告，一般是价格取错了地方。

## 二、币安公开行情（不要密钥）

两条路，数据一样：

- **直接连币安**：合约 `https://fapi.binance.com`，现货 `https://api.binance.com`。
- **经平台转发**：`http://你的平台:8080/bapi/fapi/v1/klines?symbol=BTCUSDT&interval=1h`，路径和参数跟币安一样。K 线、24 小时统计、资金费历史、持仓量、成交记录能转发；`/futures/data` 下面的统计里只有持仓量历史能转发，多空比这些要直接连币安。部署的人把行情设成模拟（`MARKET_SOURCE=sim`）时不转发。

| 数据 | 接口 | 历史能取多久 | 要注意的 |
| --- | --- | --- | --- |
| 合约 K 线 | `/fapi/v1/klines` | 从上线起都有，一次最多 1500 根 | 最后一根通常还没收完，见第四节 |
| 标记价、指数价 K 线 | `/fapi/v1/markPriceKlines`、`/fapi/v1/indexPriceKlines` | 同上 | 强平、按标记价触发的止损看的是标记价，不是成交价 |
| 资金费率（已结算） | `/fapi/v1/fundingRate` | 全部 | 不是每个币都 8 小时结算一次，有的是 4 小时；结算间隔调过的币在 `/fapi/v1/fundingInfo` 里 |
| 资金费率（正在累计的这一期） | `/fapi/v1/premiumIndex` | 只有现在 | 结算前一直在变，以结算后 `fundingRate` 里的那条为准 |
| 持仓量 | 现在：`/fapi/v1/openInterest`；历史：`/futures/data/openInterestHist` | 历史只有最近 30 天 | 更早的币安直接返回 400（实测 31 天前就取不到）。要更长的历史，只能从现在起自己每期存一份 |
| 多空人数比、主动买卖比 | `/futures/data/globalLongShortAccountRatio`、`/futures/data/takerlongshortRatio` | 最近 30 天 | 不经平台转发，直接连币安 |
| 逐笔成交 | `/fapi/v1/aggTrades`；推送 `btcusdt@aggTrade`（常规线路） | 请求能往回翻 | 要实时的用推送 |
| 盘口 | `/fapi/v1/depth`；推送 `btcusdt@depth20@100ms`（高频线路） | 只有现在 | 历史盘口币安不给，要用就自己存 |
| 强平 | 推送 `btcusdt@forceOrder`（常规线路） | 只有推送 | 每个币每秒最多推一条，会少算 |
| 现货 K 线 | `https://api.binance.com/api/v3/klines` | 从上线起 | 现货和合约的价格不完全一样 |

推送分两条线路，名字用小写，几个名字用 `/` 连起来：

- 高频线路（盘口、最优买卖价）：`wss://fstream.binance.com/public/stream?streams=btcusdt@depth20@100ms`
- 常规线路（成交、标记价、24 小时统计、强平）：`wss://fstream.binance.com/market/stream?streams=btcusdt@aggTrade/btcusdt@markPrice@1s`

旧地址 `wss://fstream.binance.com/ws/...` 和 `/stream` 币安在 2026-04-23 停用了，网上很多老例子还在用，连上去收不到数据。

### 请求额度

- 合约接口**每个 IP 每分钟 2400 权重**（`/fapi/v1/exchangeInfo` 的 `rateLimits` 里写着）。每次回复的响应头 `X-MBX-USED-WEIGHT-1M` 是这一分钟已经用掉多少。超了币安先回 429，还接着请求就会封 IP，从 2 分钟到 3 天不等。
- **策略和平台在同一台机器上时，共用这一个额度。** 实测平台自己一分钟就要用四百多。IP 被封以后，平台自己的行情也会断。
- 省额度的办法：K 线一次取够（一次取 1500 根只算 10 权重，分 1500 次一根一根取要 1500 权重）；只在新的一根收完时才取；要每秒都更新的数据用推送，不要一直轮询。

## 三、其他常用来源

平台自己的资讯和日历页用的就是下面这些。你的程序要用，自己去取：

| 数据 | 来源 | 要注意的 |
| --- | --- | --- |
| 新闻 | 各家官方 RSS：CoinDesk `https://www.coindesk.com/arc/outboundfeeds/rss/`、Cointelegraph `https://cointelegraph.com/rss`、Decrypt `https://decrypt.co/feed` | 用文章自己的发布时间；在信号的 `rationale.evidence` 里写上标题、链接、`published_at` |
| 交易所公告（上新、下架、维护） | Bybit `https://api.bybit.com/v5/announcements/index`、OKX `https://www.okx.com/api/v5/support/announcements` | 公告时间和生效时间是两回事，下架一般提前几天公告 |
| 宏观数据（利率、通胀、就业） | 美联储圣路易斯分行的 FRED（`https://api.stlouisfed.org`，免费申请密钥）；公布日程：美国经济分析局（BEA）的官方日历 | **会被修正**：第一次公布的数和后来修正的数不一样。回测要用当时公布的那个数，FRED 的姊妹库 ALFRED 存着每次公布的版本 |
| 大户仓位 | Hyperliquid 公开接口 `https://api.hyperliquid.xyz/info` | 只有 Hyperliquid 上的仓位 |
| X（推特） | X 的官方接口，要你自己的开发者密钥 | 按用量收费；只取你要关注的账号 |

## 四、每个数据"什么时候才知道"

回测好看、上线就不行，最常见的原因是用了当时还拿不到的数据。每个数据都要问一句：**它是在什么时候才能知道的？**

| 数据 | 什么时候才算数 |
| --- | --- |
| K 线 | 收盘时间（`close_time`）到了才算收完。没收完的那根，收盘价、最高、最低还在变；拿它算的指标在这一小时里会变好几次，回测里用的却都是收完的 |
| 资金费 | 结算那一刻（`fundingTime`）才定下来 |
| 持仓量历史、多空比 | 每期的数要等那一刻过去才出来，有时还要晚几分钟。回测时按你**实际能取到的时间**对齐，不要按数据上写的时间 |
| 新闻、公告 | 你的程序拿到它的时间可能比发布时间晚几分钟到几小时。回测里按"拿到的时间"算 |
| 宏观数据 | 公布时刻才知道，之后还可能被修正 |

写进信号的时候：`data_as_of` 填这次判断用到的**最新那一条数据**的时间（比如最后一根收完的 K 线的收盘时间）。平台会统计 `data_as_of` 到发出信号之间隔了多久，超过 10 分钟会提醒。

只用收完的 K 线（`marketdata.py` 的 `klines()` 已经这么做了）：

```python
import time
from datetime import datetime, timezone
from marketdata import klines
from qsclient import Client

c = Client(version="my-rule-v1")
bars = klines("BTCUSDT", "1h", 200)            # 只有收完的
as_of = datetime.fromtimestamp(bars[-1]["close_time"] / 1000, timezone.utc)
weight = 0.0                                   # 你的规则用 bars 算出来
c.target({"BTC-PERP": weight}, reason="规则信号", data_as_of=as_of)
```

## 五、品种代码对照

平台里叫 `BTC-PERP`，币安合约里叫 `BTCUSDT`：去掉 `-PERP`、加上 `USDT`。能交易哪些品种以 `c.instruments()` 为准。
