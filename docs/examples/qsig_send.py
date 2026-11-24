"""用 qsig/1 协议给平台发一条目标仓位信号（程序化策略用）。

qsig/1 说的是"每个品种应该持有多少"（目标权重），不是买卖动作；平台负责算差额、下单、止损止盈。
协议全文：docs/qsig-1.md，机器可读的 Schema：docs/protocol/qsig-1.schema.json。

策略的密钥在"账户 → 我的策略"里自己建（secret 只显示一次）；也可以由部署平台的人写在环境变量 QSIG_KEYS 里（"key_id:secret:strategy_id"）。

    QS_BASE=http://localhost:8080 QS_KEY=my-key QS_SECRET=... QS_STRATEGY=my-strategy \\
        python qsig_send.py BTC-PERP 0.2

只用 Python 标准库。
"""

import hashlib
import hmac
import json
import os
import sys
import time
import urllib.error
import urllib.request
import uuid
from datetime import datetime, timedelta, timezone

BASE = os.environ.get("QS_BASE", "http://localhost:8080")
KEY = os.environ.get("QS_KEY", "")
SECRET = os.environ.get("QS_SECRET", "")
STRATEGY = os.environ.get("QS_STRATEGY", "")


def iso(t: datetime) -> str:
    """UTC，精确到毫秒，以 Z 结尾。"""
    return t.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.") + f"{t.microsecond // 1000:03d}Z"


def call(method: str, path: str, body: bytes = b"") -> tuple[int, dict]:
    """签名：timestamp \\n METHOD \\n path \\n body，HMAC-SHA256，小写十六进制。签的是实际发送的字节。"""
    ts = str(int(time.time() * 1000))
    msg = f"{ts}\n{method}\n{path}\n".encode() + body
    sig = hmac.new(SECRET.encode(), msg, hashlib.sha256).hexdigest()
    req = urllib.request.Request(BASE + path, data=body or None, method=method, headers={
        "Content-Type": "application/json", "X-QS-Key": KEY, "X-QS-Timestamp": ts, "X-QS-Signature": sig,
    })
    try:
        with urllib.request.urlopen(req, timeout=15) as r:
            return r.status, json.load(r)
    except urllib.error.HTTPError as e:
        return e.code, json.load(e)


def ticker(instrument: str) -> str:
    """下决策那一刻重新取的最新价（ref_price 要用原始市场价）。"""
    status, t = call("GET", f"/v1/ticker?instrument={instrument}")
    if status != 200:  # 密钥不对、被吊销、时钟差太多都在这里先暴露：说清楚，不要抛一堆 Python 报错
        sys.exit(f"{status} 取行情失败：{t.get('code', '')} {t.get('message', '')}")
    return t["last"]


def signal(instrument: str, weight: str) -> dict:
    now = datetime.now(timezone.utc)
    return {
        "protocol": "qsig/1",
        "type": "signal",
        "id": str(uuid.uuid4()),
        "strategy_id": STRATEGY,
        "strategy_version": "example-1",
        "issued_at": iso(now),
        "data_as_of": iso(now - timedelta(seconds=5)),
        "valid_until": iso(now + timedelta(seconds=60)),
        "mode": "partial",
        "targets": [{
            "instrument": instrument,
            "weight": weight,  # 目标仓位 ÷ 组合净值，带符号；字符串
            "confidence": 0.6,
            "horizon_min": 240,
            "ref_price": ticker(instrument),
            "ref_price_source": "binance_usdm_last",
            "execution": {"urgency": "normal", "max_slippage_bps": 20},
            "exits": {"stop_loss_pct": "0.02", "take_profit_pct": "0.05"},
            "rationale": {"summary": {"zh": "示例：演示怎么发信号", "en": "Example signal"}, "factors": [], "evidence": []},
        }],
    }


if __name__ == "__main__":
    if len(sys.argv) != 3 or not (KEY and SECRET and STRATEGY):
        sys.exit(__doc__)
    msg = signal(sys.argv[1], sys.argv[2])
    body = json.dumps(msg, ensure_ascii=False, separators=(",", ":")).encode()
    status, reply = call("POST", "/v1/messages", body)
    print(status, reply)
    if status == 200:
        time.sleep(3)
        print(call("GET", f"/v1/messages/{msg['id']}"))  # 执行回报：每个品种实际到了多少、成交均价、滑点
