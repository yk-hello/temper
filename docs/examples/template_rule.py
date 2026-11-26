"""规则策略的空模板：定时取 K 线 → 你的规则算出目标仓位 → 用 qsig/1 发出去 → 发心跳。

只有"怎么接"，没有"怎么赚钱"：decide() 里返回的都是 0（空仓），换成你自己的规则。

    QS_BASE=http://localhost:8080 QS_KEY=... QS_SECRET=... QS_STRATEGY=my-strategy python template_rule.py

只用 Python 标准库；同目录要有 qsclient.py（签名、发送、读回报）和 marketdata.py（取币安 K 线），复制到一起就行。
要一直跑、崩了自己起来：docs/running.md；还能取哪些数据：docs/market-data.md。
"""

import time
from datetime import datetime, timezone

from marketdata import klines
from qsclient import Client, QsigError

INSTRUMENTS = ["BTC-PERP", "ETH-PERP"]
INTERVAL = "1h"         # 用哪个周期的 K 线
BARS = 200              # 每次取多少根（只要收完的）
EVERY = 300             # 多久判断一次（秒）
HEARTBEAT = 45          # 心跳间隔（秒），协议要求 30–60 秒一次，超过 180 秒算离线
VERSION = "my-rule-v1"  # 改了规则就改版本号：平台按版本分开算成绩


def symbol(inst: str) -> str:
    """平台的 BTC-PERP 就是币安合约的 BTCUSDT。"""
    return inst.removesuffix("-PERP") + "USDT"


def decide(bars: dict[str, list[dict]]) -> dict[str, float]:
    """你的规则写在这里。bars：每个品种收完的 K 线，早的在前（字段见 marketdata.py：open_time、close、volume……）。
    返回每个品种的目标权重（目标名义价值 ÷ 组合净值，-1 到 1，正多负空，0 空仓）。"""
    return {inst: 0.0 for inst in bars}


def main() -> None:
    c = Client(version=VERSION)
    last_decide = 0.0
    while True:
        try:
            c.heartbeat()
            if time.time() - last_decide >= EVERY:
                last_decide = time.time()
                bars = {inst: klines(symbol(inst), INTERVAL, BARS) for inst in INSTRUMENTS}
                # data_as_of：用到的最新那条数据的时间，这里是最后一根收完的 K 线的收盘时间
                as_of = datetime.fromtimestamp(max(b[-1]["close_time"] for b in bars.values()) / 1000, timezone.utc)
                weights = decide(bars)
                prices = {inst: c.last(inst) for inst in INSTRUMENTS}  # 决策那一刻重新取的原始价格，平台拿它算滑点
                r = c.target(weights, reason="规则信号", reason_en="Rule signal", data_as_of=as_of, ref_prices=prices)
                print(r.get("status"), r.get("id"), flush=True)
                # 想看执行结果（实际到了多少、均价、滑点）：print(c.wait(r["id"]))
        except QsigError as e:  # 平台拒了：原因在 e.body["code"]，常见的见 docs/qsig-1.md
            print("拒绝", e.status, e.body, flush=True)
        except OSError as e:    # 网络断了、平台或币安暂时连不上：这一轮跳过，下一轮重新取、重新算、重新发
            print("网络", e, flush=True)
        time.sleep(HEARTBEAT)


if __name__ == "__main__":
    main()
