"""大模型（AI）策略的空模板：取数据 → 拼提示词 → 调你自己用的模型服务 → 检查输出 → 用 qsig/1 发出去。

只有"怎么接"，没有"怎么赚钱"：ask_model() 是空的，接你自己用的模型服务；提示词也要你自己写。
理由、信心、推理花费随信号一起发，平台会展示、做信心校准、把推理花费算进净收益。

    QS_BASE=http://localhost:8080 QS_KEY=... QS_SECRET=... QS_STRATEGY=my-ai python template_ai.py

只用 Python 标准库；签名、发送、读回报用同目录的 qsclient.py（复制到一起就行）。
要 K 线、资金费、持仓量这些，用同目录的 marketdata.py（见 docs/market-data.md）。
要一直跑、崩了自己起来：docs/running.md。
"""

import json
import os
import threading
import time
from datetime import datetime, timezone

from qsclient import Client, QsigError, iso

INSTRUMENTS = ["BTC-PERP", "ETH-PERP"]
MAX_WEIGHT = 0.3       # 风控写在你自己的程序里：单个品种最多 30%，不让模型的输出直接决定下多大
MAX_TOTAL = 1.0        # 所有品种合计最多 100%（协议上限是 200%）
EVERY = 300            # 多久判断一次（秒）；判断之间照常发心跳
LOG_DIR = "ai_log"     # 每次判断的原始记录：数据、提示词、模型输出、发出的目标、平台回复，至少留 90 天
VERSION = "my-ai-v1"   # 换模型、改提示词都要改版本号：平台按版本分开算成绩


def collect(c: Client, now: datetime) -> dict:
    """取这次判断用到的数据。记下每份数据的时间：data_as_of 要写其中最新的那个，不能用到"之后"的数据。"""
    prices = {inst: c.last(inst) for inst in INSTRUMENTS}
    return {"as_of": now, "prices": prices}


def build_prompt(data: dict) -> str:
    """你的提示词。要求模型只输出 JSON，例如：
    {"targets": [{"instrument": "BTC-PERP", "weight": 0.1, "confidence": 0.6, "summary": "……"}], "cost_usd": 0.02}
    """
    return "……"


def ask_model(prompt: str) -> str:
    """调你自己用的模型服务，返回模型的原始文本输出。这里故意是空的。
    记得给请求设超时（比如 120 秒）：模型服务卡住时，宁可这一次不判断。"""
    raise NotImplementedError("接你自己的模型服务")


def check(raw: str) -> tuple[dict[str, float], float, str, float] | None:
    """检查模型输出：格式不对、品种不认识、超出范围的，一律不发（宁可空过一次）。
    返回（目标权重、平均信心、一句理由、推理花费）。"""
    try:
        out = json.loads(raw)
    except json.JSONDecodeError:
        return None
    weights, confs, notes = {}, [], []
    for t in out.get("targets", []):
        inst, w, c = t.get("instrument"), float(t.get("weight", 0)), float(t.get("confidence", 0))
        if inst not in INSTRUMENTS or not 0 <= c <= 1:
            return None
        weights[inst] = max(-MAX_WEIGHT, min(MAX_WEIGHT, w))
        confs.append(c)
        notes.append(str(t.get("summary", ""))[:120])
    if not weights or sum(abs(w) for w in weights.values()) > MAX_TOTAL:
        return None
    return weights, sum(confs) / len(confs), "；".join(n for n in notes if n) or "模型判断", float(out.get("cost_usd", 0))


def keep_alive(c: Client) -> None:
    """心跳单独一个线程：问一次模型可能要几十秒到几分钟，和判断放在一个循环里的话，卡住时心跳也停了，超过 180 秒就算离线。"""
    while True:
        try:
            c.heartbeat()
        except (QsigError, OSError) as e:
            print("心跳", e, flush=True)
        time.sleep(45)


def main() -> None:
    os.makedirs(LOG_DIR, exist_ok=True)
    c = Client(version=VERSION)
    threading.Thread(target=keep_alive, args=(c,), daemon=True).start()
    last = 0.0
    while True:
        if time.time() - last >= EVERY:
            last = time.time()
            now = datetime.now(timezone.utc)
            record: dict = {"t": iso(now)}
            try:
                data = collect(c, now)
                prompt = build_prompt(data)
                raw = ask_model(prompt)
                record.update({"data": data["prices"], "prompt": prompt, "raw": raw})
                got = check(raw)
                if got:
                    weights, conf, reason, cost = got
                    r = c.target(weights, reason=reason, confidence=conf, data_as_of=data["as_of"], ref_prices=data["prices"],
                                 cost_usd=cost, meta={"model": "你的模型名和版本"})
                    record.update({"sent": weights, "reply": r})
            except QsigError as e:
                record["reject"] = {"status": e.status, "body": e.body}
            except OSError as e:
                record["error"] = str(e)
            with open(os.path.join(LOG_DIR, now.strftime("%Y%m%d") + ".jsonl"), "a", encoding="utf-8") as f:
                f.write(json.dumps(record, ensure_ascii=False, default=str) + "\n")
        time.sleep(5)


if __name__ == "__main__":
    main()
