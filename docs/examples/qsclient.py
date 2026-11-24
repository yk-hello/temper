"""qsig/1 的 Python 客户端（一个文件，只用标准库）。把它复制到你的策略旁边 import 就能用。

    from qsclient import Client
    c = Client()                       # 从环境变量读 QS_BASE、QS_KEY、QS_SECRET、QS_STRATEGY
    px = c.last("BTC-PERP")            # 下决策那一刻重新取的最新价
    r = c.target({"BTC-PERP": 0.2}, reason="均线金叉", confidence=0.6)
    print(r["status"], c.wait(r["id"]))  # 接受了吗；执行回报（实际到了多少、均价、滑点）
    c.heartbeat()                      # 每 30–60 秒一次；超过 180 秒没有判定离线

密钥在"账户 → 我的策略"里自己建（secret 只显示一次）。协议全文：docs/qsig-1.md。
我们不提供策略：这里只负责签名、发送和读回报，决策是你的。
"""

from __future__ import annotations

import hashlib
import hmac
import json
import os
import time
import urllib.error
import urllib.request
import uuid
from datetime import datetime, timedelta, timezone
from typing import Any


def iso(t: datetime) -> str:
    """UTC，精确到毫秒，以 Z 结尾（协议要求的时间格式）。"""
    return t.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.") + f"{t.microsecond // 1000:03d}Z"


def weight_str(w: float | str) -> str:
    """权重写成字符串，最多 4 位小数，-1 到 1。"""
    v = float(w)
    if not -1 <= v <= 1:
        raise ValueError(f"权重要在 -1 到 1 之间：{w}")
    s = f"{v:.4f}".rstrip("0").rstrip(".")
    return "0" if s in ("-0", "") else s


class QsigError(Exception):
    """平台拒绝了请求：code 是错误码（见 qsig-1.md 的错误码表），message 是说明。"""

    def __init__(self, status: int, body: dict):
        self.status, self.body = status, body
        self.code = body.get("code", "")
        super().__init__(f"{status} {self.code} {body.get('message', '')}")


class Client:
    def __init__(self, base: str | None = None, key: str | None = None, secret: str | None = None,
                 strategy: str | None = None, version: str = "v1", timeout: float = 15):
        self.base = (base or os.environ.get("QS_BASE", "http://localhost:8080")).rstrip("/")
        self.key = key or os.environ.get("QS_KEY", "")
        self.secret = secret or os.environ.get("QS_SECRET", "")
        self.strategy = strategy or os.environ.get("QS_STRATEGY", "")
        self.version = version  # 改了规则或提示词就改版本号：平台按版本分开算成绩
        self.timeout = timeout
        if not (self.key and self.secret and self.strategy):
            raise ValueError("缺少 QS_KEY、QS_SECRET 或 QS_STRATEGY")

    # ---------- 底层：签名和发送 ----------

    def call(self, method: str, path: str, body: bytes = b"") -> dict:
        """签名：HMAC-SHA256(secret, 毫秒时间戳 + 换行 + METHOD + 换行 + path + 换行 + body)，签的是实际发送的字节。"""
        ts = str(int(time.time() * 1000))
        sig = hmac.new(self.secret.encode(), f"{ts}\n{method}\n{path}\n".encode() + body, hashlib.sha256).hexdigest()
        req = urllib.request.Request(self.base + path, data=body or None, method=method, headers={
            "Content-Type": "application/json", "X-QS-Key": self.key, "X-QS-Timestamp": ts, "X-QS-Signature": sig,
        })
        try:
            with urllib.request.urlopen(req, timeout=self.timeout) as r:
                return json.load(r)
        except urllib.error.HTTPError as e:
            try:
                body_ = json.load(e)
            except ValueError:
                body_ = {"code": "HTTP_" + str(e.code)}
            raise QsigError(e.code, body_) from None

    def send(self, msg: dict) -> dict:
        return self.call("POST", "/v1/messages", json.dumps(msg, ensure_ascii=False, separators=(",", ":")).encode())

    def _base_msg(self, typ: str) -> dict:
        return {"protocol": "qsig/1", "type": typ, "id": str(uuid.uuid4()), "strategy_id": self.strategy,
                "strategy_version": self.version, "issued_at": iso(datetime.now(timezone.utc))}

    # ---------- 读 ----------

    def instruments(self) -> list[dict]:
        return self.call("GET", "/v1/instruments")["instruments"]

    def ticker(self, instrument: str) -> dict:
        return self.call("GET", f"/v1/ticker?instrument={instrument}")

    def last(self, instrument: str) -> str:
        """最新价（字符串，原样作为 ref_price 发回去）。"""
        return self.ticker(instrument)["last"]

    def portfolio(self) -> dict:
        """平台按你的信号跑的模型组合：现在的目标、仓位、净值。"""
        return self.call("GET", "/v1/portfolio")

    def performance(self) -> dict:
        return self.call("GET", "/v1/performance")

    def message(self, msg_id: str) -> dict:
        """一条消息的状态和执行回报。"""
        return self.call("GET", f"/v1/messages/{msg_id}")

    def wait(self, msg_id: str, timeout: float = 30) -> list[dict]:
        """等执行回报：每个品种实际到了多少、成交均价、滑点、手续费。超时返回目前拿到的。"""
        end, ex = time.time() + timeout, []
        while time.time() < end:
            ex = self.message(msg_id).get("executions") or []
            if ex and all(x.get("state") != "pending" for x in ex):
                return ex
            time.sleep(1)
        return ex

    # ---------- 发 ----------

    def target(self, weights: dict[str, float | str], *, reason: str = "", reason_en: str = "",
               confidence: float = 0.5, horizon_min: int = 240, valid_s: int = 60,
               data_as_of: datetime | None = None, mode: str = "partial", urgency: str = "normal",
               max_slippage_bps: int | None = None, stop_loss_pct: str | None = None,
               take_profit_pct: str | None = None, ref_prices: dict[str, str] | None = None,
               cost_usd: float | None = None, meta: dict | None = None) -> dict:
        """发目标仓位：weights = {品种: 目标名义价值 ÷ 组合净值}，带符号（正多负空，0 空仓）。

        - mode="partial" 只调整列出的品种（推荐）；"snapshot" 表示列出的就是完整持仓，没列出的全部平仓。
        - ref_prices 不给时，每个品种现取一次最新价（下决策那一刻的价格，平台拿它算滑点）。
        - data_as_of：这次判断用到的最新一条数据的时间，不给就当作现在。
        - cost_usd：这次推理花了多少钱（AI 策略），平台把它算进净收益。
        """
        if not weights:
            raise ValueError("weights 不能为空（防止程序出错时一条空列表误平全仓）；想全平用 halt 或逐个写 0")
        now = datetime.now(timezone.utc)
        msg = self._base_msg("signal")
        msg.update({"data_as_of": iso(data_as_of or now), "valid_until": iso(now + timedelta(seconds=valid_s)), "mode": mode})
        targets = []
        for inst, w in weights.items():
            t: dict[str, Any] = {
                "instrument": inst, "weight": weight_str(w),
                "confidence": confidence,  # 0–1：你对这条判断有多大把握（平台做信心校准：说 0.8 的是不是真的八成对）
                "horizon_min": horizon_min,  # 这条判断看多久（5 分钟到 7 天）
                "ref_price": (ref_prices or {}).get(inst) or self.last(inst), "ref_price_source": "binance_usdm_last",
                "rationale": {"summary": {"zh": reason or "策略信号", "en": reason_en or reason or "Strategy signal"}, "factors": [], "evidence": []},
            }
            ex: dict[str, Any] = {"urgency": urgency}
            if max_slippage_bps is not None:
                ex["max_slippage_bps"] = max_slippage_bps
            t["execution"] = ex
            exits = {k: v for k, v in (("stop_loss_pct", stop_loss_pct), ("take_profit_pct", take_profit_pct)) if v is not None}
            if exits:
                t["exits"] = exits
            targets.append(t)
        msg["targets"] = targets
        m = dict(meta or {})
        if cost_usd is not None:
            m["cost_usd"] = cost_usd
        if m:
            msg["meta"] = m
        return self.send(msg)

    def flat(self, instruments: list[str], reason: str = "平仓") -> dict:
        """把这些品种的目标设成 0。"""
        return self.target({i: 0 for i in instruments}, reason=reason)

    def heartbeat(self, status: str = "ok", note: str = "") -> dict:
        """每 30–60 秒一次。status：ok | degraded（数据或模型有问题，平台会提示跟随的人）| paused。"""
        msg = self._base_msg("heartbeat")
        msg["status"] = status
        if note:
            msg["note"] = note
        return self.send(msg)

    def halt(self, action: str = "pause", reason: str = "策略自己暂停") -> dict:
        """暂停：pause 不再加仓、持仓不动；flatten_all 全部平仓并暂停。之后只能由平台的管理员在管理后台恢复。"""
        msg = self._base_msg("halt")
        msg["action"] = action
        msg["reason"] = {"zh": reason or "策略自己暂停"}
        return self.send(msg)


if __name__ == "__main__":
    # 自检：读一个价、发一次心跳。用法：QS_BASE=... QS_KEY=... QS_SECRET=... QS_STRATEGY=... python qsclient.py
    c = Client()
    print("BTC-PERP 最新价", c.last("BTC-PERP"))
    print("心跳", c.heartbeat(note="qsclient 自检").get("status"))
