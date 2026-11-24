"""均线交叉策略 → 回测用的信号表。

只用 Python 标准库。从币安公开接口取 1 小时 K 线，20 小时均线上穿 50 小时均线做多、下穿做空，
写成信号表（每行"时间,信号"，时间按 UTC），再用平台的回测跑：

    python backtest_ma_cross.py --symbol BTCUSDT --days 30 --out signals.csv
    cd backend && go run ./cmd/backtest -signals ../signals.csv -from 2026-10-01 -to 2026-10-31

也可以把 signals.csv 的内容贴到"复盘 → 回测"的信号表里。

这只是演示怎么接，不是能赚钱的策略。
"""

import argparse
import datetime as dt
import json
import urllib.request

API = "https://fapi.binance.com/fapi/v1/klines"


def klines(symbol: str, days: int) -> list[tuple[int, float]]:
    """最近 days 天的 1 小时 K 线：（收盘时间毫秒, 收盘价）。"""
    end = int(dt.datetime.now(dt.timezone.utc).timestamp() * 1000)
    start = end - days * 86_400_000 - 60 * 3_600_000  # 多取 60 小时给均线预热
    out: list[tuple[int, float]] = []
    while start < end:
        url = f"{API}?symbol={symbol}&interval=1h&startTime={start}&limit=1500"
        with urllib.request.urlopen(url, timeout=15) as r:
            rows = json.load(r)
        if not rows:
            break
        out += [(int(x[6]) + 1, float(x[4])) for x in rows]  # 收盘时间 + 1 毫秒 = 下一根的开盘
        start = int(rows[-1][0]) + 3_600_000
    return [x for x in out if x[0] <= end]


def sma(values: list[float], n: int, i: int) -> float | None:
    return sum(values[i - n + 1 : i + 1]) / n if i + 1 >= n else None


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--symbol", default="BTCUSDT")
    ap.add_argument("--days", type=int, default=30)
    ap.add_argument("--fast", type=int, default=20)
    ap.add_argument("--slow", type=int, default=50)
    ap.add_argument("--size", default="50%", help="每次开仓用可用资金的多少（名义金额）")
    ap.add_argument("--out", default="signals.csv")
    a = ap.parse_args()

    coin = a.symbol.removesuffix("USDT")
    bars = klines(a.symbol, a.days)
    # 回测从这一天（UTC 零点）开始；之前的 K 线只用来让均线有值，不出信号
    first = (bars[-1][0] // 86_400_000 - a.days) * 86_400_000
    closes = [c for _, c in bars]
    lines = ["time,signal"]
    side = 0  # 均线的方向：1 多，-1 空
    holding = False  # 回测里有没有仓位（预热期不开仓）
    for i, (t, _) in enumerate(bars):
        f, s = sma(closes, a.fast, i), sma(closes, a.slow, i)
        if f is None or s is None:
            continue
        want = 1 if f > s else -1
        if want == side:
            continue
        if t < first:  # 预热期：只记下方向，开始之后第一次交叉才出信号
            side = want
            continue
        when = dt.datetime.fromtimestamp(t / 1000, dt.timezone.utc).strftime("%Y-%m-%d %H:%M")
        if holding:
            lines.append(f"{when},平仓 {coin}")
        lines.append(f"{when},{'做多' if want == 1 else '做空'} {coin} {a.size}")
        side, holding = want, True
    with open(a.out, "w", encoding="utf-8") as fp:
        fp.write("\n".join(lines) + "\n")
    day = lambda ms: dt.datetime.fromtimestamp(ms / 1000, dt.timezone.utc).strftime("%Y-%m-%d")
    print(f"{len(lines) - 1} signals -> {a.out}")
    print(f"go run ./cmd/backtest -signals {a.out} -from {day(first)} -to {day(bars[-1][0] + 86_400_000)}")


if __name__ == "__main__":
    main()
