package trading

import "time"

// travel 模拟一次请求在网上跑的时间（D94）。在拿引擎的锁之前调用：先睡到这张单"到交易所"再处理，
// 返回的函数在锁放开之后睡到"回报回来"。没设置 Latency 时什么都不做。
//
//	defer e.travel(acct)()
func (e *Engine) travel(acct int64) func() {
	if e.Latency == nil {
		return func() {}
	}
	exec, reply := e.Latency(acct)
	if exec > 0 {
		time.Sleep(exec)
	}
	return func() {
		if d := reply - exec; d > 0 {
			time.Sleep(d)
		}
	}
}

// 币安 U 本位合约每个账户的下单频率上限：10 秒 300 张、1 分钟 1200 张（撤单不算）。
const (
	rate10s = 300
	rate1m  = 1200
)

// rateCheck 下单频率超过交易所的限制就拒单（和币安的 -1015 一样）。持有 e.mu 时调用。
func (e *Engine) rateCheck(acct int64) error {
	if e.sent == nil {
		e.sent = map[int64][]int64{}
	}
	now := e.now()
	list := e.sent[acct]
	i := 0
	for i < len(list) && list[i] <= now-60_000 {
		i++
	}
	list = list[i:]
	n10 := 0
	for _, t := range list {
		if t > now-10_000 {
			n10++
		}
	}
	if n10 >= rate10s || len(list) >= rate1m {
		e.sent[acct] = list
		return rej("EXCHANGE_RATE_LIMIT", "n10", "300", "n60", "1200")
	}
	e.sent[acct] = append(list, now)
	return nil
}
