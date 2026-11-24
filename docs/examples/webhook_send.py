"""往平台的信号地址发一条信号（和 TradingView 警报一样）。

信号地址在"账户 → TradingView 警报和 Webhook"里新建，只在创建时显示一次。
绑在模拟账户上就在模拟盘成交，绑在交易所账户上就发到交易所（要先在账户页"开启下单"）。

    python webhook_send.py "http://localhost:8080/api/hook/tv/<密钥>" "做多 BTC 30%"
    python webhook_send.py "<地址>" '{"action":"close","symbol":"BTCUSDT"}'

返回 {"status":"ok","order":"..."}；被拒时 status 是 rejected，code 说明原因（界面的信号记录里有中文说明）。
"""

import json
import sys
import urllib.request


def send(url: str, text: str) -> dict:
    req = urllib.request.Request(url, data=text.encode("utf-8"), method="POST")
    with urllib.request.urlopen(req, timeout=15) as r:
        return json.load(r)


if __name__ == "__main__":
    if len(sys.argv) != 3:
        sys.exit(__doc__)
    print(send(sys.argv[1], sys.argv[2]))
