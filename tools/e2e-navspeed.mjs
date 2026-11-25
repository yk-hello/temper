// 切换页面不卡（11-25，用户："每次打开切换页面都感觉像 PPT"）：
// 1. 点顶栏切页面不再播淡入淡出（视图过渡）；2. 打开后空闲时把所有页面的代码先下好，第一次进页面不用再下载；
// 3. 切回交易页，第一帧就有盘口（最近 15 秒的盘口留在内存里），不再先显示"等待盘口数据"。
// 会注册账号：只能对着测试环境跑。用法：node tools/e2e-navspeed.mjs <截图目录> [http://127.0.0.1:4173]
import { chromium } from 'playwright'

const OUT = process.argv[2] || '.'
const BASE = process.argv[3] || 'http://127.0.0.1:4173'
let pass = 0, fail = 0
const ok = (cond, name) => { if (cond) { pass++; console.log('✓', name) } else { fail++; console.log('✗', name) } }
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
const rand = () => Math.random().toString(36).slice(2)

const browser = await chromium.launch({ channel: process.env.PW_CHANNEL || 'msedge' })
const c = await browser.newContext({ viewport: { width: 1536, height: 864 }, deviceScaleFactor: 1.25, locale: 'zh-CN' })
await c.addInitScript(() => { localStorage.setItem('qs.wizardSeen', '1') })
const p = await c.newPage()
const errs = []
p.on('pageerror', (e) => errs.push(String(e)))
const code = []
p.on('request', (r) => { const u = new URL(r.url()); if (/\/assets\/.*\.js$/.test(u.pathname)) code.push(u.pathname) })

await p.goto(BASE + '/?page=login')
await p.evaluate(async (body) => {
  await fetch('/api/auth/register', { method: 'POST', headers: { 'X-QS-Client': 'web', 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
}, { name: `nav${Date.now()}${rand().slice(0, 4)}`, password: `nav-${rand()}-${rand()}` })
await p.goto(BASE + '/?page=trade')
await p.waitForSelector('.op-submit')
// 等盘口来、等空闲时把各页面的代码下完
await p.waitForFunction(() => document.querySelectorAll('.ob-r').length >= 10, null, { timeout: 20000 }).catch(() => {})
await sleep(5000)

// 每次切换：点之前挂一个逐帧检查，记下有没有视图过渡、新页面第一帧时盘口有没有数
const nav = async (pg, sel) => {
  await p.evaluate((sel) => {
    const m = (window.__nav = { vt: false, first: -1, bookAtFirst: -1 })
    const t0 = performance.now()
    const tick = () => {
      if (document.getAnimations().some((a) => String(a.effect?.pseudoElement || '').includes('view-transition'))) m.vt = true
      if (m.first < 0 && document.querySelector(sel)) {
        m.first = Math.round(performance.now() - t0)
        m.bookAtFirst = document.querySelectorAll('.ob-r').length
      }
      if (performance.now() - t0 < 1500) requestAnimationFrame(tick)
    }
    requestAnimationFrame(tick)
  }, sel)
  const before = code.length
  await p.locator(`.bar a[href="?page=${pg}"]`).first().click()
  await sleep(1600)
  return { ...(await p.evaluate(() => window.__nav)), newCode: code.length - before }
}

for (const [pg, sel] of [['review', '.lp'], ['about', '.ab'], ['risk', '.rk']]) {
  const r = await nav(pg, sel)
  ok(!r.vt, `切到${pg}：没有淡入淡出`)
  ok(r.first >= 0 && r.first < 800, `切到${pg}：页面出来了（${r.first} 毫秒）`)
  ok(r.newCode === 0, `切到${pg}：第一次进也不用再下载代码（下载了 ${r.newCode} 个）`)
  const back = await nav('trade', '.op-submit')
  ok(!back.vt, `从${pg}切回交易页：没有淡入淡出`)
  ok(back.bookAtFirst >= 10, `从${pg}切回交易页：第一帧就有盘口（${back.bookAtFirst} 行）`)
}
await p.screenshot({ path: `${OUT}/navspeed-trade.png` })
// 盘口太旧（超过 15 秒）不拿出来：离开 20 秒再回来，第一帧不该是旧盘口（等新数据）
{
  await nav('review', '.lp')
  await sleep(20000)
  const back = await nav('trade', '.op-submit')
  ok(back.bookAtFirst < 10, `离开 20 秒再回来：不显示旧盘口（第一帧 ${back.bookAtFirst} 行）`)
  const fresh = await p.waitForFunction(() => document.querySelectorAll('.ob-r').length >= 10, null, { timeout: 10000 }).then(() => true).catch(() => false)
  ok(fresh, '随后新盘口到了')
}
ok(errs.length === 0, `页面没有报错（${errs.join(' | ').slice(0, 200)}）`)
await browser.close()
console.log(`\n${pass} 通过，${fail} 失败`)
process.exit(fail ? 1 : 0)
