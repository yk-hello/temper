"""规则策略的空模板：定时取行情 → 你的规则算出目标仓位 → 用 qsig/1 发出去 → 发心跳。

只有"怎么接"，没有"怎么赚钱"：decide() 里返回的都是 0（空仓），换成你自己的规则。

    QS_BASE=http://localhost:8080 QS_KEY=... QS_SECRET=... QS_STRATEGY=my-strategy python template_rule.py

只用 Python 标准库；签名、发送、读回报用同目录的 qsclient.py（复制到一起就行）。
"""

import time

from qsclient import Client, QsigError

INSTRUMENTS = ["BTC-PERP", "ETH-PERP"]
EVERY = 300          # 多久判断一次（秒）
HEARTBEAT = 45       # 心跳间隔（秒），协议要求 30–60 秒一次，超过 180 秒算离线
VERSION = "my-rule-v1"  # 改了规则就改版本号：平台按版本分开算成绩


def decide(prices: dict[str, float]) -> dict[str, float]:
    """你的规则写在这里：返回每个品种的目标权重（目标名义价值 ÷ 组合净值，-1 到 1，正多负空，0 空仓）。"""
    return {inst: 0.0 for inst in prices}


def main() -> None:
    c = Client(version=VERSION)
    last_decide = 0.0
    while True:
        try:
            c.heartbeat()
            if time.time() - last_decide >= EVERY:
                last_decide = time.time()
                prices = {inst: c.last(inst) for inst in INSTRUMENTS}  # 决策那一刻重新取的原始价格
                weights = decide({k: float(v) for k, v in prices.items()})
                r = c.target(weights, reason="规则信号", reason_en="Rule signal", ref_prices=prices)
                print(r.get("status"), r.get("id"))
                # 想看执行结果（实际到了多少、均价、滑点）：print(c.wait(r["id"]))
        except QsigError as e:  # 平台拒了：原因在 e.body["code"]，常见的见 docs/qsig-1.md
            print("拒绝", e.status, e.body)
        except OSError as e:    # 网络断了：下一轮再试
            print("网络", e)
        time.sleep(HEARTBEAT)


if __name__ == "__main__":
    main()
