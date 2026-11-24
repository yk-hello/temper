// 概率云的手算样例（12 的 3.19）。node tools/test-cloud.mjs
import { simulate, stepReturns } from '../web/src/lib/cloud.ts'

let fail = 0
const near = (name, got, want, tol = 1e-3) => {
  const ok = Math.abs(got - want) <= tol
  console.log(ok ? '  ✓' : '  ✗', name, ok ? '' : `得到 ${got}，应 ${want}`)
  if (!ok) fail++
}

// 每段固定 +1%，10 段：1.01^10 − 1 = 10.4622%，所有路径都一样
let r = simulate({ returns: [0.01], steps: 10, paths: 100, maxLoss: 0.2 })
near('固定涨：中位数', r.median, 1.01 ** 10 - 1)
near('固定涨：没有路径碰线', r.stopped, 0)
near('固定涨：最差也是这个数', r.worst, 0)
near('固定涨：最好', r.best, 1.01 ** 10 - 1)

// 每段固定 −5%，最大亏损 20%：0.95^4 = 0.8145 没到线，0.95^5 = 0.7738 碰线，之后平线。
// 最差 −22.62%，比设定的 20% 多亏一些：这正是界面上要写的"极端行情可能多亏一些"
r = simulate({ returns: [-0.05], steps: 20, paths: 100, maxLoss: 0.2 })
near('固定跌：全部碰线', r.stopped, 1)
near('固定跌：停在第 5 段', r.worst, 0.95 ** 5 - 1)
near('固定跌：之后不再变', r.median, 0.95 ** 5 - 1)

// 一半 +10%、一半 −10%，一段：中位数在两者之一，5% 分位是 −10%，95% 分位是 +10%
r = simulate({ returns: [0.1, -0.1], steps: 1, paths: 4000, maxLoss: 0.5, cols: 1 })
near('两点分布：5% 分位', r.p5[0], -0.1)
near('两点分布：95% 分位', r.p95[0], 0.1)
near('两点分布：没有碰线', r.stopped, 0)

// 密度：所有路径都算进格子里（纵轴范围包住 98% 以上的路径）
r = simulate({ returns: [0.02, -0.02, 0.01, -0.01], steps: 30, paths: 3000, maxLoss: 0.2 })
let total = 0
for (let i = 0; i < r.cols; i++) total += r.density[i * 0 + (r.rows - 1) * r.cols + i] * 0
let lastCol = 0
for (let row = 0; row < r.rows; row++) lastCol += r.density[row * r.cols + r.cols - 1]
near('密度：最后一列至少 97% 的路径落在图内', lastCol / 3000 >= 0.97 ? 1 : 0, 1)
near('同样的输入画出同样的云', simulate({ returns: [0.02, -0.02, 0.01, -0.01], steps: 30, paths: 3000, maxLoss: 0.2 }).median, r.median, 0)

// stepReturns：价格 100、110（第 1 段涨 10%）、缺一段（平）、99（第 3 段跌 10%）
const rets = stepReturns([{ t: 0, v: 100 }, { t: 10, v: 110 }, { t: 30, v: 99 }], 10)
near('分段收益：段数', rets.length, 3)
near('分段收益：第 1 段', rets[0], 0.1)
near('分段收益：第 2 段没有新价', rets[1], 0)
near('分段收益：第 3 段', rets[2], -0.1)

console.log(fail ? `失败 ${fail}` : '全部通过')
process.exit(fail ? 1 : 0)
