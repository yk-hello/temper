# 和币安一样的接口（模拟盘）

已经有能对币安 U 本位合约下单的程序（自己写的、ccxt、Freqtrade、Hummingbot 等）：把地址换成你部署的平台、换一把这里发的模拟密钥，程序就跑在模拟盘上。规则和成交都按模拟盘（逐条对齐币安，见 [README](README.md)）。验证完把地址换回币安、换回币安的密钥，就是实盘，代码不用改。

## 拿密钥

"账户 → 币安兼容接口（模拟盘） → 新建密钥"：选一个模拟账户，输二级密码，拿到 API Key 和 Secret（Secret 只显示一次）。一把密钥只能在这个模拟账户上交易；要部署的人配了 `QS_MASTER_KEY`（密钥加密存）。

| | 币安 | 这里 |
| --- | --- | --- |
| 接口 | `https://fapi.binance.com` | `http://你的服务器:8080/bapi` |
| 推送 | `wss://fstream.binance.com` | `ws://你的服务器:8080/bapi` |

签名、参数、回复、错误码都和币安一样：`X-MBX-APIKEY` 头，查询串加请求体做 HMAC-SHA256，`timestamp` 和 `recvWindow`。

## ccxt

```python
import ccxt
ex = ccxt.binanceusdm({"apiKey": "...", "secret": "..."})
for k in ("fapiPublic", "fapiPublicV2", "fapiPublicV3", "fapiPrivate", "fapiPrivateV2", "fapiPrivateV3"):
    ex.urls["api"][k] = ex.urls["api"][k].replace("https://fapi.binance.com", "http://你的服务器:8080/bapi")
print(ex.fetch_balance()["USDT"])
```

如果你的 ccxt 版本在 `load_markets` 时去取币安现货的资产信息（`sapi` 开头的地址）报错，加 `"options": {"fetchCurrencies": False}`。

## 支持的接口

公开（不要密钥）：`ping`、`time`、`exchangeInfo`、`ticker/price`、`ticker/bookTicker`、`premiumIndex`、`depth`。K 线、24 小时统计、资金费历史、持仓量、成交记录这些公开行情原样转给币安（模拟行情时没有）。

要签名：

| 接口 | 说明 |
| --- | --- |
| `GET /fapi/v2/account`、`/fapi/v3/account`、`/fapi/v2/balance`、`/fapi/v3/balance` | 余额只有 USDT |
| `GET /fapi/v2/positionRisk`、`/fapi/v3/positionRisk`、`/fapi/v1/symbolConfig` | 单向持仓（BOTH） |
| `POST /fapi/v1/leverage`、`/fapi/v1/marginType` | |
| `GET/POST /fapi/v1/positionSide/dual` | 只支持单向持仓 |
| `GET /fapi/v1/commissionRate` | 这个模拟账户现在的费率（默认挂单 0.02%、吃单 0.05%；账户页可以改） |
| `POST /fapi/v1/order`、`/fapi/v1/order/test` | LIMIT（GTC/IOC/FOK/GTX/GTD，GTD 带 `goodTillDate`，至少晚 600 秒；也可以用 `priceMatch` 按盘口定价，不带 `price`）、MARKET；条件单认 `priceProtect`；STOP、STOP_MARKET、TAKE_PROFIT、TAKE_PROFIT_MARKET 也认（老写法，按条件单处理） |
| `GET/PUT/DELETE /fapi/v1/order`、`DELETE /fapi/v1/allOpenOrders`、`GET /fapi/v1/openOrders`、`/fapi/v1/allOrders` | 按 `orderId` 或 `origClientOrderId` |
| `POST/GET/DELETE /fapi/v1/algoOrder`、`GET /fapi/v1/openAlgoOrders`、`DELETE /fapi/v1/algoOpenOrders` | 条件单（币安 2025-12 起的写法） |
| `GET /fapi/v1/userTrades`、`/fapi/v1/income` | 成交、实现盈亏、手续费、资金费 |
| `POST/PUT/DELETE /fapi/v1/listenKey` | 账户推送：连 `ws://服务器/bapi/ws/<listenKey>` 或 `/bapi/private/ws/<listenKey>`，推 `ORDER_TRADE_UPDATE`、`ACCOUNT_UPDATE` |

没仿的接口回 `{"code": -5000, "msg": "... is not supported on this simulated venue"}`，不会装作成功。

## 和币安不一样的地方

- 整仓的止盈止损（`closePosition=true`）挂到仓位上：一个仓位一张止盈、一张止损，再下一张会替换前一张（币安允许多张）。
- 不支持：双向持仓、跟踪止损（`TRAILING_STOP_MARKET`）、批量下单（`batchOrders`）、多资产保证金。
- 客户端编号（`newClientOrderId`）只在内存里记着：服务器重启后要按 `orderId` 查。
- 平台自己的拦截（全局停机、停用、纪律锁、机器人优先）回 `-2010`，原因写在 `msg` 方括号里。拒单的 `msg` 都在最后带上平台的原因代码，比如 `[INSUFFICIENT_MARGIN]`。
- 下单频率限制和模拟盘一样（10 秒 300 张、1 分钟 1200 张），另外整个接口按来源每秒 30 个请求。
- 成交、延迟按模拟盘的规则（可以在"交易偏好"里选模拟下单延迟的地区）。
- 推送大约每 250 毫秒检查一次账户变化，比币安慢一点。
