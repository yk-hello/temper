"""验证策略的前向记录没有被改过（只用 Python 标准库）。

记录从策略页"下载前向记录"得到（接口 GET /api/strategies/<策略>/log），一行一条 JSON。
每一条的 chain = sha256(上一条的 chain + "\n" + id + "\n" + received_ms + "\n" + sha256(raw))，
第一条的"上一条"是空字符串。改了任何一条（内容、时间、顺序、删掉一条），从那一条往后都对不上。

用法：python verify_log.py my-strategy-qsig-log.jsonl
可选：再给一个你自己记下的 chain（比如上周截的最后一环），检查它还在链上：
      python verify_log.py my-strategy-qsig-log.jsonl 3fa9...
"""
import hashlib
import json
import sys


def sha256_hex(b: bytes) -> str:
    return hashlib.sha256(b).hexdigest()


def main() -> int:
    if len(sys.argv) < 2:
        print(__doc__)
        return 2
    path = sys.argv[1]
    want = sys.argv[2].lower() if len(sys.argv) > 2 else None
    prev, n, seen = "", 0, False
    with open(path, encoding="utf-8") as f:
        for line_no, line in enumerate(f, 1):
            if not line.strip():
                continue
            e = json.loads(line)
            if e["prev"] != prev:
                print(f"第 {line_no} 行（{e['id']}）：记的上一环和实际的上一环不一样，前面有记录被改过、删过或调过顺序")
                return 1
            raw_hash = sha256_hex(e["raw"].encode("utf-8"))
            chain = sha256_hex(f"{prev}\n{e['id']}\n{e['received_ms']}\n{raw_hash}".encode("utf-8"))
            if chain != e["chain"]:
                print(f"第 {line_no} 行（{e['id']}）：内容或时间和指纹对不上，这一条被改过")
                return 1
            if want and chain == want:
                seen = True
            prev, n = chain, n + 1
    print(f"通过：{n} 条，链完整。最后一环：{prev}")
    print("把最后一环记下来（发给别人、存到别处）。以后再导出时它还在链上，就说明这之前的记录没被改过。")
    if want and not seen:
        print(f"但是你给的 {want} 不在这条链上：之前的记录被改过，或者不是同一个策略的记录")
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
