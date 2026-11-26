"""取币安公开行情的几个小函数（一个文件，只用标准库，不要密钥）。复制到你的策略旁边 import 就能用。

    from marketdata import klines, funding_history, open_interest
    bars = klines("BTCUSDT", "1h", 200)        # 只返回已经收完的 K 线，最后一根还在走的那根不要
    print(bars[-1]["close"], bars[-1]["close_time"])

默认直接连币安（https://fapi.binance.com）。想经过你部署的平台转发，设环境变量
QS_MARKET_BASE=http://localhost:8080/bapi（K 线、资金费、持仓量能转发；多空比不转发，总是直连币安）。

每个数据什么时候才拿得到、历史能取多久、请求额度怎么算：docs/market-data.md。
我们不提供策略：这里只负责把数据取回来，怎么用是你的事。
"""

from __future__ import annotations

import json
import os
import time
import urllib.parse
import urllib.request

FAPI = os.environ.get("QS_MARKET_BASE", "https://fapi.binance.com").rstrip("/")  # U 本位合约
FAPI_DIRECT = "https://fapi.binance.com"   # 平台不转发的（/futures/data 下面的统计）
SPOT = "https://api.binance.com"           # 现货


def get(base: str, path: str, **params) -> list | dict:
    """GET 一个公开接口。额度用到多少在响应头 X-MBX-USED-WEIGHT-1M 里（合约每个 IP 每分钟 2400）。"""
    url = base + path + ("?" + urllib.parse.urlencode(params) if params else "")
    with urllib.request.urlopen(url, timeout=15) as r:
        return json.load(r)


def _bars(rows: list, now_ms: int) -> list[dict]:
    out = []
    for k in rows:
        if k[6] >= now_ms:  # 收盘时间还没到：这根还在走，用它算出的指标会一直变（回测里用的都是收完的）
            continue
        out.append({"open_time": k[0], "close_time": k[6], "open": float(k[1]), "high": float(k[2]),
                    "low": float(k[3]), "close": float(k[4]), "volume": float(k[5]),
                    "taker_buy_volume": float(k[9]), "trades": k[8]})
    return out


def klines(symbol: str, interval: str, limit: int = 500) -> list[dict]:
    """合约 K 线，只要收完的。interval：1m 3m 5m 15m 30m 1h 2h 4h 6h 8h 12h 1d 3d 1w 1M。
    一次最多 1499 根（币安一次给 1500 根，去掉没收完的那根；再多就按时间分几次取）；时间都是毫秒，UTC。"""
    rows = get(FAPI, "/fapi/v1/klines", symbol=symbol, interval=interval, limit=min(limit + 1, 1500))
    return _bars(rows, int(time.time() * 1000))[-limit:]


def spot_klines(symbol: str, interval: str, limit: int = 500) -> list[dict]:
    """现货 K 线，只要收完的。"""
    rows = get(SPOT, "/api/v3/klines", symbol=symbol, interval=interval, limit=min(limit + 1, 1000))
    return _bars(rows, int(time.time() * 1000))[-limit:]


def funding_history(symbol: str, limit: int = 100) -> list[dict]:
    """已经结算的资金费率：[{time, rate}]。结算那一刻才定下来；不是每个币都是 8 小时一次。"""
    return [{"time": x["fundingTime"], "rate": float(x["fundingRate"])}
            for x in get(FAPI, "/fapi/v1/fundingRate", symbol=symbol, limit=limit)]


def premium(symbol: str) -> dict:
    """现在的标记价、指数价、正在累计的这一期资金费率（结算前一直在变）和下次结算时间。"""
    x = get(FAPI, "/fapi/v1/premiumIndex", symbol=symbol)
    return {"time": x["time"], "mark": float(x["markPrice"]), "index": float(x["indexPrice"]),
            "funding_rate": float(x["lastFundingRate"]), "next_funding_time": x["nextFundingTime"]}


def open_interest(symbol: str) -> dict:
    """现在的持仓量（张数按币算）。"""
    x = get(FAPI, "/fapi/v1/openInterest", symbol=symbol)
    return {"time": x["time"], "oi": float(x["openInterest"])}


def open_interest_hist(symbol: str, period: str = "1h", limit: int = 200) -> list[dict]:
    """持仓量历史：只有最近 30 天（更早的币安返回 400）。要更长的，从现在起自己每期存一份。
    period：5m 15m 30m 1h 2h 4h 6h 12h 1d；一次最多 500 条。"""
    return [{"time": x["timestamp"], "oi": float(x["sumOpenInterest"]), "oi_usd": float(x["sumOpenInterestValue"])}
            for x in get(FAPI, "/futures/data/openInterestHist", symbol=symbol, period=period, limit=limit)]


def long_short_ratio(symbol: str, period: str = "1h", limit: int = 200) -> list[dict]:
    """全市场多空人数比（多头账户占比 ÷ 空头账户占比）：只有最近 30 天，不经平台转发。"""
    return [{"time": x["timestamp"], "ratio": float(x["longShortRatio"]), "long": float(x["longAccount"])}
            for x in get(FAPI_DIRECT, "/futures/data/globalLongShortAccountRatio", symbol=symbol, period=period, limit=limit)]


def taker_ratio(symbol: str, period: str = "1h", limit: int = 200) -> list[dict]:
    """主动买入量 ÷ 主动卖出量：只有最近 30 天，不经平台转发。"""
    return [{"time": x["timestamp"], "ratio": float(x["buySellRatio"]), "buy": float(x["buyVol"]), "sell": float(x["sellVol"])}
            for x in get(FAPI_DIRECT, "/futures/data/takerlongshortRatio", symbol=symbol, period=period, limit=limit)]


if __name__ == "__main__":
    # 自检：每样取一点打出来。python marketdata.py
    def t(ms: int) -> str:
        return time.strftime("%Y-%m-%d %H:%M:%S", time.gmtime(ms / 1000)) + " UTC"

    b = klines("BTCUSDT", "1h", 3)
    print("合约 1 小时 K 线（收完的最后一根）", t(b[-1]["open_time"]), "收", b[-1]["close"])
    s = spot_klines("BTCUSDT", "1h", 1)
    print("现货 1 小时 K 线", t(s[-1]["open_time"]), "收", s[-1]["close"])
    f = funding_history("BTCUSDT", 1)[-1]
    print("上一次结算的资金费率", t(f["time"]), f["rate"])
    p = premium("BTCUSDT")
    print("标记价", p["mark"], "这一期资金费率（还在变）", p["funding_rate"], "下次结算", t(p["next_funding_time"]))
    print("持仓量", open_interest("BTCUSDT")["oi"])
    h = open_interest_hist("BTCUSDT", "1h", 2)
    print("持仓量历史（最近一期）", t(h[-1]["time"]), h[-1]["oi"])
    print("多空人数比（最近一期）", long_short_ratio("BTCUSDT", "1h", 1)[-1]["ratio"])
    print("主动买卖比（最近一期）", taker_ratio("BTCUSDT", "1h", 1)[-1]["ratio"])
