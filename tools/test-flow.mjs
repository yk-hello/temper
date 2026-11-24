// 足迹图的手算样例（12 的 V.11）和持仓量摘要（V.13）。node tools/test-flow.mjs
import { mergeBar, nice125, niceUp125, oiSummary } from '../web/src/lib/flowCalc.ts'
import { practicePnl } from '../web/src/lib/journal.ts'

let fail = 0
const eq = (a, b, name) => {
  const ok = JSON.stringify(a) === JSON.stringify(b)
  if (!ok) { fail++; console.log('✗', name, JSON.stringify(a), '应是', JSON.stringify(b)) } else console.log('✓', name)
}

// 档位宽 10。每行：价位、主动买、主动卖
//   120:  b 9  s 1
//   110:  b 2  s 2
//   100:  b 1  s 9
// 合计买 12、卖 12，三档，每侧平均 4，噪声下限 0.4
// 120 买 9 ≥ 3 × 110 卖 2 → 买方失衡；120 卖 1，上一档没有买，1 < 3×0.4=1.2，不算
// 110 买 2 < 3 × 100 卖 9，不算；110 卖 2 < 3 × 120 买 9，不算
// 100 买 1，下一档没有卖，1 ≥ 1.2？不到，不算；100 卖 9 ≥ 3 × 110 买 2 → 卖方失衡
const bar = { t: 0, l: [{ p: 100, b: 1, s: 9 }, { p: 110, b: 2, s: 2 }, { p: 120, b: 9, s: 1 }] }
const d = mergeBar(bar, 10)
eq(d.levels.map((x) => [x.p, x.imb]), [[100, -1], [110, 0], [120, 1]], '失衡看斜对角')
eq([d.b, d.s, d.maxSide], [12, 12, 9], '合计和单格最大')
eq(d.poc, 100, '成交最多的价位：100 和 120 都是 10，取先到的 100')

// 合并到 20 一档：100、110 合成 100；120 单独
const m = mergeBar(bar, 20)
eq(m.levels.map((x) => [x.p, x.b, x.s]), [[100, 3, 11], [120, 9, 1]], '合并档位时买卖量加起来')
eq(m.poc, 100, '合并后成交最多的是 100（14）')

// 细档位不是显示档位的整数倍：按所在的档算
const n = mergeBar({ t: 0, l: [{ p: 104, b: 1, s: 0 }, { p: 106, b: 2, s: 0 }, { p: 110, b: 4, s: 0 }] }, 5)
eq(n.levels.map((x) => [x.p, x.b]), [[100, 1], [105, 2], [110, 4]], '价格落到不大于它的整数倍上')

// 只有一侧成交、零星几笔：不算失衡
const q = mergeBar({ t: 0, l: [{ p: 100, b: 0.1, s: 5 }, { p: 110, b: 5, s: 5 }] }, 10)
eq(q.levels[0].imb, 0, '一档既有买方又有卖方失衡时都不标，或者量太小不标')

eq([nice125(0.7), nice125(0.8), nice125(2.6), nice125(40), nice125(0)], [0.5, 1, 2, 50, 0], '档位取整到 1、2、5')

eq([niceUp125(0.7), niceUp125(1), niceUp125(1.4), niceUp125(2.6), niceUp125(40), niceUp125(0)], [1, 1, 2, 5, 50, 0], '显示档位只往宽里取')

// 持仓量摘要：最近 1 小时增加 1000、减少 200，基数 10000 - 800 = 9200
const h = 3600_000
const pts = [
  { t: 0, usd: 9000, delta: 999, dp: 1, kind: 'new_long' },            // 超过 1 小时，不算
  { t: 2 * h - 10, usd: 9500, delta: 1000, dp: -1, kind: 'new_short' },
  { t: 2 * h, usd: 10000, delta: -200, dp: 1, kind: 'short_cover' },
]
const s = oiSummary(pts, 2 * h + 1000, h)
eq(s.main, 'new_short', '主要是金额最大的那一种')
eq(Math.round(s.pct * 100) / 100, 8.7, '持仓量变化 = 800 / 9200')
eq(s.usd, 10000, '现在的持仓量')
eq(oiSummary([], 0, h), null, '没有数据')

// 练习单手算（5.11）：做多 1000 USDT，开仓 100，平仓 110，数量 10，毛利 100；手续费 (1000 + 1100) × 0.05% = 1.05
const pp = practicePnl('long', 100, 110, 1000)
eq([pp.gross, Math.round(pp.fee * 100) / 100, Math.round(pp.net * 100) / 100], [100, 1.05, 98.95], '练习单做多')
const ps = practicePnl('short', 100, 110, 1000)
eq([ps.gross, Math.round(ps.net * 100) / 100], [-100, -101.05], '练习单做空')

console.log(fail ? `失败 ${fail}` : '全部通过')
process.exit(fail ? 1 : 0)
