// 最后一根 K 线跟着实时价走（11-26，用户："改成跟着动"；推送也改成 0.1 秒一次）：
// 原来 K 线每 2 秒才取一次，顶上的价格已经变了，最后一根还停在 2 秒前；现在每次推送（0.1 秒）都更新最后一根。
// 每 50 毫秒读一次顶上的价格和图上最后一根的收盘（图表外层的 data-last），看两者是不是一起变、对不对得上。
// 只读页面，不下单；对着测试环境跑。用法：node tools/e2e-livebar.mjs <截图目录> [http://127.0.0.1:4173]
import { chromium } from 'playwright'

const OUT = process.argv[2] || '.'
const BASE = process.argv[3] || 'http://127.0.0.1:4173'
let pass = 0, fail = 0
const ok = (cond, name) => { if (cond) { pass++; console.log('✓', name) } else { fail++; console.log('✗', name) } }

const browser = await chromium.launch({ channel: process.env.PW_CHANNEL || 'msedge' })
const c = await browser.newContext({ viewport: { width: 1536, height: 864 }, deviceScaleFactor: 1.25, locale: 'zh-CN' })
await c.addInitScript(() => { localStorage.setItem('qs.wizardSeen', '1') })
const p = await c.newPage()
const errs = []
p.on('pageerror', (e) => errs.push(String(e)))

await p.goto(BASE + '/?page=trade&i=BTC-PERP')
// 推送频率：页面打开后另连一条 /ws 数 5 秒
const r2 = await p.evaluate(async () => new Promise((res) => {
  const ws = new WebSocket(`${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/ws`)
  let n = 0, withAt = 0, t0 = 0
  ws.onmessage = (e) => {
    const d = JSON.parse(e.data)
    if (d.type !== 'snapshot') return
    if (!t0) t0 = performance.now()
    n++
    if (d.instruments.some((x) => x.last_at > 0)) withAt++
  }
  setTimeout(() => { ws.close(); res({ perSec: (n - 1) / ((performance.now() - t0) / 1000), n, withAt }) }, 5000)
}))
console.log(`  行情推送每秒 ${r2.perSec.toFixed(1)} 帧，带最后成交时间的 ${r2.withAt}/${r2.n}`)
ok(r2.perSec >= 8, '行情每秒推 8 帧以上（0.1 秒一次）')
ok(r2.withAt === r2.n, '每一帧都带最后一笔成交的时间（last_at）')

const price = '.mk-price .mk-last' // 顶上的大号最新价
await p.waitForSelector('.mk-chart .surface[data-last]', { timeout: 20000 })
await p.waitForSelector(price, { timeout: 20000 })

// 每 50 毫秒读一次，读 6 秒
const sample = async (label) => {
  const s = await p.evaluate(async (sel) => {
    const out = []
    const num = (t) => Number(String(t).replace(/[^0-9.\-]/g, ''))
    const t0 = performance.now()
    while (performance.now() - t0 < 6000) {
      const txt = document.querySelector(sel)?.textContent ?? ''
      const dec = (txt.split('.')[1] ?? '').replace(/[^0-9]/g, '').length // 顶上按交易所精度显示，图上存的是原始数
      const last = Number(document.querySelector('.mk-chart .surface')?.getAttribute('data-last'))
      out.push([num(txt), last, dec])
      await new Promise((r) => setTimeout(r, 50))
    }
    return out
  }, price)
  let changes = 0, same = 0
  for (let i = 0; i < s.length; i++) {
    if (i && s[i][1] !== s[i - 1][1]) changes++
    if (Math.abs(s[i][0] - s[i][1]) <= 0.5 * 10 ** -s[i][2] + 1e-9) same++ // 按顶上显示的小数位取整后一样
  }
  const topChanges = s.filter((x, i) => i && x[0] !== s[i - 1][0]).length
  console.log(`  ${label}：顶上的价格变了 ${topChanges} 次，最后一根收盘变了 ${changes} 次；${same}/${s.length} 个时刻两者一样`)
  return { changes, topChanges, same, n: s.length }
}

let r = await sample('1 分钟线')
ok(r.changes > 6, '1 分钟线：最后一根 6 秒里变了不止 3 次（原来 2 秒才取一次，最多变 3 次）')
ok(r.same / r.n >= 0.9, '1 分钟线：九成以上的时刻，最后一根的收盘和顶上的价格一样')
await p.screenshot({ path: `${OUT}/livebar-1m.png` })

await p.locator('button[data-tab="1s"]').first().click()
await p.waitForTimeout(1500)
r = await sample('1 秒线')
ok(r.changes > 6, '1 秒线：最后一根跟着变')
ok(r.same / r.n >= 0.9, '1 秒线：九成以上的时刻和顶上的价格一样')
await p.screenshot({ path: `${OUT}/livebar-1s.png` })

ok(!errs.length, '页面没有报错' + (errs.length ? '：' + errs.slice(0, 2).join('；') : ''))
await browser.close()
console.log(`\n${pass} 通过，${fail} 失败`)
process.exit(fail ? 1 : 0)
