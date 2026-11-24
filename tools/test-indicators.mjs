// K 线指标的手算样例（12 的 V.1、V.12）。node tools/test-indicators.mjs
import { bollinger, cvd, delta, ema, macd, rsi, sma, vwap } from '../web/src/lib/indicators.ts'

let fail = 0
const near = (name, got, want, tol = 1e-6) => {
  const ok = got != null && Math.abs(got - want) <= tol
  console.log(ok ? '  ✓' : '  ✗', name, ok ? '' : `得到 ${got}，应 ${want}`)
  if (!ok) fail++
}
const is = (name, cond) => { console.log(cond ? '  ✓' : '  ✗', name); if (!cond) fail++ }

// MA：(1+2+3)/3 = 2，(2+3+4)/3 = 3
let v = sma([1, 2, 3, 4], 3)
is('MA 不够时是空', v[0] === null && v[1] === null)
near('MA 第一个', v[2], 2)
near('MA 第二个', v[3], 3)

// EMA(3)：k = 0.5。第一个 = (1+2+3)/3 = 2；下一个 = 4×0.5 + 2×0.5 = 3；再下一个 = 10×0.5 + 3×0.5 = 6.5
v = ema([1, 2, 3, 4, 10], 3)
near('EMA 第一个', v[2], 2)
near('EMA 第二个', v[3], 3)
near('EMA 第三个', v[4], 6.5)

// 布林带(4, 2)：2、4、4、6 均值 4，总体标准差 √((4+0+0+4)/4) = √2
let b = bollinger([2, 4, 4, 6], 4, 2)
near('布林中轨', b.mid[3], 4)
near('布林上轨', b.up[3], 4 + 2 * Math.SQRT2)
near('布林下轨', b.lo[3], 4 - 2 * Math.SQRT2)

// RSI(3)：涨 1、涨 1、跌 1 → 平均涨 2/3，平均跌 1/3，RS = 2，RSI = 100 − 100/3 = 66.667
// 下一根涨 2：涨 = (2/3×2 + 2)/3 = 10/9，跌 = (1/3×2 + 0)/3 = 2/9，RS = 5，RSI = 100 − 100/6 = 83.333
v = rsi([10, 11, 12, 11, 13], 3)
near('RSI 第一个', v[3], 100 - 100 / 3)
near('RSI 第二个', v[4], 100 - 100 / 6)
near('一直涨 RSI = 100', rsi([1, 2, 3, 4], 3)[3], 100)
near('没波动 RSI = 50', rsi([5, 5, 5, 5], 3)[3], 50)

// MACD(2, 3, 2)：价格 1、2、3、4
// 快线 EMA2：k=2/3，1.5、2.5、3.5；慢线 EMA3：k=1/2，2、3
// DIF：第 3 根 2.5−2 = 0.5；第 4 根 3.5−3 = 0.5。DEA(2) = (0.5+0.5)/2 = 0.5，柱 = 0
let m = macd([1, 2, 3, 4], 2, 3, 2)
near('DIF', m.dif[2], 0.5)
near('DEA', m.dea[3], 0.5)
near('柱', m.hist[3], 0)

// VWAP：第一根 典型价 (12+8+10)/3 = 10、量 2；第二根 典型价 (22+18+20)/3 = 20、量 1
// (10×2 + 20×1) / 3 = 13.333；第二天重新算
const day = 86400000
const bars = [
  { t: day, h: 12, l: 8, c: 10, vb: 1, vs: 1 },
  { t: day + 60000, h: 22, l: 18, c: 20, vb: 1, vs: 0 },
  { t: 2 * day, h: 6, l: 4, c: 5, vb: 3, vs: 1 },
]
v = vwap(bars)
near('VWAP 第一根', v[0], 10)
near('VWAP 第二根', v[1], 40 / 3)
near('VWAP 跨天重算', v[2], 5)

// 主动买卖差和 CVD：0、+1、+2 → 累计 0、1、3
const d = delta(bars), c = cvd(bars)
near('主动买卖差', d[2], 2)
near('CVD', c[2], 3)

if (fail) { console.log(`失败 ${fail} 项`); process.exit(1) }
console.log('全部通过')
