// 最后一根 K 线跟着实时价走（web/src/lib/liveBar.ts）的手算样例。node tools/test-livebar.mjs
import { withLive } from '../web/src/lib/liveBar.ts'

let fail = 0
const is = (name, cond, extra = '') => { console.log(cond ? '  ✓' : '  ✗', name, cond ? '' : extra); if (!cond) fail++ }
const M = 60_000
const bar = (t, o, h, l, c) => ({ t, o, h, l, c, vb: 5, vs: 3 })
const T0 = 1_795_600_000_000 - (1_795_600_000_000 % M) // 某一分钟的开头

// 1. 同一根里：收盘跟着走，最高被推高
let ext = { current: null }
let bars = [bar(T0 - M, 100, 101, 99, 100.5), bar(T0, 100.5, 102, 100, 101)]
let out = withLive(bars, 103, T0 + 20_000, M, ext)
const L = (a) => a[a.length - 1]
is('同一根：收盘变成实时价', L(out).c === 103)
is('同一根：最高被推高到 103', L(out).h === 103)
is('同一根：开盘、最低、成交量不动', L(out).o === 100.5 && L(out).l === 100 && L(out).vb === 5)
is('同一根：前面的 K 线不动、原数组没被改', out[0] === bars[0] && L(bars).c === 101)
is('同一根：根数不变', out.length === 2)

// 2. 冲高又回落：高点不缩回去
out = withLive(bars, 101.5, T0 + 20_400, M, ext)
is('回落：收盘 101.5', L(out).c === 101.5)
is('回落：最高还是 103（上一次推送见过）', L(out).h === 103, `得到 ${L(out).h}`)
out = withLive(bars, 99.5, T0 + 20_800, M, ext)
is('跌破：最低变成 99.5', L(out).l === 99.5)

// 3. 时间进了下一根：先临时开一根，开盘用上一根的收盘
ext = { current: null }
bars = [bar(T0 - M, 100, 101, 99, 100.5), bar(T0, 100.5, 102, 100, 101)]
out = withLive(bars, 101.8, T0 + M + 300, M, ext)
is('进了下一根：多出一根', out.length === 3)
is('新的一根：时间对', L(out).t === T0 + M)
is('新的一根：开盘 = 上一根收盘 101', L(out).o === 101)
is('新的一根：收盘 101.8，最高 101.8，最低 101', L(out).c === 101.8 && L(out).h === 101.8 && L(out).l === 101)
is('新的一根：成交量先记 0', L(out).vb === 0 && L(out).vs === 0)
is('上一根不动', out[1] === bars[1])
out = withLive(bars, 100.7, T0 + M + 600, M, ext)
is('新的一根里回落：最高还是 101.8，最低 100.7', L(out).h === 101.8 && L(out).l === 100.7)

// 4. 真的 K 线取到了：换成真的，实时见过的高低不丢
const real = [...bars, bar(T0 + M, 101.1, 101.5, 100.9, 101.2)]
out = withLive(real, 101.3, T0 + M + 900, M, ext)
is('取到真的：根数还是 3', out.length === 3)
is('取到真的：开盘用真的 101.1', L(out).o === 101.1)
is('取到真的：最高取真的和实时见过的大者 101.8', L(out).h === 101.8)
is('取到真的：最低取小者 100.7', L(out).l === 100.7)
is('取到真的：收盘是最新实时价 101.3', L(out).c === 101.3)
is('取到真的：成交量用真的', L(out).vb === 5)

// 5. 不猜的情况：原样返回同一个数组
ext = { current: null }
is('推送比最后一根还旧：不动', withLive(bars, 105, T0 - M - 1, M, ext) === bars)
is('隔了不止一根（断线、睡醒）：不动', withLive(bars, 105, T0 + 2 * M + 1, M, ext) === bars)
is('价格无效：不动', withLive(bars, NaN, T0 + 1000, M, ext) === bars && withLive(bars, 0, T0 + 1000, M, ext) === bars)
is('时间无效：不动', withLive(bars, 101, 0, M, ext) === bars)
is('周期不认识：不动', withLive(bars, 101, T0 + 1000, 0, ext) === bars)
is('没有 K 线：不动', withLive([], 101, T0 + 1000, M, ext).length === 0)
ext = { current: null }
const same = [bar(T0, 100, 102, 99, 101)]
is('实时价没带来变化：原样返回（图不用重画）', withLive(same, 101, T0 + 1000, M, ext) === same)

// 6. 1 秒线：每秒一根，0.2 秒的推送落在对的那一秒
ext = { current: null }
const S = 1000, s0 = T0 + 30 * S
bars = [bar(s0, 100, 100, 100, 100)]
out = withLive(bars, 100.2, s0 + 400, S, ext)
is('1 秒线：同一秒更新', out.length === 1 && L(out).c === 100.2)
out = withLive(bars, 100.4, s0 + S + 200, S, ext)
is('1 秒线：下一秒开新的一根', out.length === 2 && L(out).t === s0 + S && L(out).o === 100)

console.log(fail ? `\n${fail} 项不对` : '\n全部通过')
process.exit(fail ? 1 : 0)
