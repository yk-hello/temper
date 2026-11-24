// 快捷键自定义（18 第 9.3 条）：账户 → 交易偏好 → 改快捷键。改买入、放大图表的键，冲突和不合法的键要说清楚，
// 到交易页按新的键生效、旧的键不再生效；恢复默认。会注册一个测试账号：只能对着测试环境跑。
// 用法：node tools/e2e-hotkeys.mjs <截图目录> [http://127.0.0.1:4173]
import { chromium } from 'playwright'

const OUT = process.argv[2] || '.'
const BASE = process.argv[3] || 'http://127.0.0.1:4173'
let pass = 0, fail = 0
const ok = (cond, name) => { if (cond) { pass++; console.log('✓', name) } else { fail++; console.log('✗', name) } }
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
const rand = () => Math.random().toString(36).slice(2)

const browser = await chromium.launch({ channel: process.env.PW_CHANNEL || 'msedge' })
const c = await browser.newContext({ viewport: { width: 1536, height: 730 }, deviceScaleFactor: 1.25, locale: 'zh-CN' })
await c.addInitScript(() => { localStorage.setItem('qs.wizardSeen', '1') })
const p = await c.newPage()
const errs = []
p.on('pageerror', (e) => errs.push(String(e)))
await p.goto(BASE + '/?page=login')
const reg = await p.evaluate(async (body) => {
  const r = await fetch('/api/auth/register', { method: 'POST', headers: { 'X-QS-Client': 'web', 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
  return r.status
}, { email: `hk${Date.now()}${rand().slice(0, 4)}@example.com`, password: `hk-${rand()}-${rand()}`, lang: 'zh' })
ok(reg === 200 || reg === 201, `注册测试账号（${reg}）`)

await p.goto(BASE + '/?page=account')
const open = p.locator('[data-hk-open]')
await open.waitFor()
await open.scrollIntoViewIfNeeded()
const sub = async () => (await p.locator('.pf-row', { has: open }).locator('.pf-sub').innerText())
ok(/B 买入、S 卖出/.test(await sub()), '说明里写的是现在的键')
await open.click()
const dlg = p.locator('.dlg:has([data-hk-list])')
await dlg.waitFor()
ok((await dlg.locator('[data-hk]').count()) === 6, '六个能改的动作')
ok(/Enter 下单/.test(await dlg.innerText()), '写明哪些不能改')

// 买入改成 J
const buy = dlg.locator('[data-hk="buy"]')
await buy.click()
ok(/按一个键/.test(await buy.innerText()), '点了以后等按键')
await p.keyboard.press('j')
await sleep(150)
ok((await buy.innerText()).trim() === 'J', '买入改成 J')
// 卖出也想用 J：冲突
const sell = dlg.locator('[data-hk="sell"]')
await sell.click()
await p.keyboard.press('j')
await sleep(150)
ok(/已经是"买入/.test(await dlg.locator('.hk-err').innerText()), `冲突时说清楚（${await dlg.locator('.hk-err').innerText()}）`)
ok((await sell.innerText()).trim() === '按一个键…', '冲突的不改，还在等')
// 带 Ctrl：不合法
await p.keyboard.press('Control+x')
await sleep(150)
ok(/不带 Ctrl/.test(await dlg.locator('.hk-err').innerText()), '带 Ctrl 的不行')
await p.keyboard.press('Escape')
await sleep(300)
await open.click()
await dlg.waitFor()
// 等按键时按 H：是隐藏金额的键，冲突；而且不能顺手把金额隐藏了
const hiddenBefore = await p.evaluate(() => document.documentElement.dataset.private ?? '')
await dlg.locator('[data-hk="iv1"]').click()
await p.keyboard.press('h')
await sleep(150)
ok((await p.evaluate(() => document.documentElement.dataset.private ?? '')) === hiddenBefore, '按键被这里收下，不会顺手触发隐藏金额')
ok(/隐藏金额/.test(await dlg.locator('.hk-err').innerText()), '和隐藏金额冲突')
await dlg.locator('[data-hk="privacy"]').click()
await p.keyboard.press('k')
await sleep(150)
ok((await dlg.locator('[data-hk="privacy"]').innerText()).trim() === 'K', '隐藏金额改成 K')
// 放大图表改成 G
await dlg.locator('[data-hk="max"]').click()
await p.keyboard.press('g')
await sleep(150)
await dlg.screenshot({ path: `${OUT}/hk-01-dialog.png` })
await p.keyboard.press('Escape')
await sleep(300)
ok((await p.locator('.dlg-back').count()) === 0, 'Esc 关掉')
ok(/J 买入、S 卖出.*K 隐藏金额.*G 放大图表/.test(await sub()), `说明跟着变（${await sub()}）`)

// ---------- 交易页 ----------
await p.goto(BASE + '/?page=trade')
await p.locator('.op-side').first().waitFor()
await sleep(1500)
await p.locator('body').click({ position: { x: 5, y: 300 } }).catch(() => {})
await p.keyboard.press('s')
await sleep(200)
ok(await p.locator('.op-side.sell').count() === 1, 'S 还是卖出')
await p.keyboard.press('b')
await sleep(200)
ok(await p.locator('.op-side.sell').count() === 1, '旧的 B 不再是买入')
await p.keyboard.press('j')
await sleep(200)
ok(await p.locator('.op-side.buy').count() === 1, '新的 J 是买入')
ok(await p.evaluate(() => document.activeElement?.getAttribute('aria-label')) === '数量', '光标跳到数量')
await p.keyboard.press('Escape')
const maxBtn = p.locator('.mk-chart .icon-btn[aria-label^="放大图表"], .icon-btn[aria-label^="放大图表"]').first()
ok(/（G）/.test(await maxBtn.getAttribute('title') || ''), '放大按钮的提示写的是 G')
await p.keyboard.press('f')
await sleep(500)
ok((await p.locator('.icon-btn[aria-label^="恢复"]').count()) === 0, '旧的 F 不再放大')
await p.keyboard.press('g')
await sleep(600)
ok((await p.locator('.icon-btn[aria-label^="恢复"]').count()) === 1, '新的 G 放大图表')
await p.keyboard.press('Escape')
await sleep(500)

// 恢复默认
await p.goto(BASE + '/?page=account')
await open.waitFor()
await open.scrollIntoViewIfNeeded()
await open.click()
await dlg.waitFor()
await dlg.locator('[data-hk-reset]').click()
await sleep(150)
ok((await buy.innerText()).trim() === 'B' && (await dlg.locator('[data-hk="max"]').innerText()).trim() === 'F', '恢复默认')
await p.keyboard.press('Escape')
await sleep(300)
// 关掉快捷键：改键按钮也不能点
await p.locator('.pf-row', { has: open }).locator('.seg button', { hasText: '关' }).click()
ok(await open.isDisabled(), '关了快捷键，改键按钮不能点')
await p.locator('.pf-row', { has: open }).locator('.seg button', { hasText: '开' }).click()

// 英文
await p.locator('.seg button', { hasText: 'English' }).first().click()
await sleep(400)
ok(/B buy, S sell/.test(await sub()), `英文说明（${await sub()}）`)
await open.scrollIntoViewIfNeeded()
await open.click()
await dlg.waitFor()
await dlg.screenshot({ path: `${OUT}/hk-02-dialog-en.png` })
await p.keyboard.press('Escape')
await sleep(300)
await p.locator('.seg button', { hasText: '中文' }).first().click()

ok(errs.length === 0, `页面没有报错（${errs.slice(0, 2).join(' | ')}）`)
console.log(`\n${pass} 通过，${fail} 失败`)
await browser.close()
process.exit(fail ? 1 : 0)
