// 模拟账户的资金和手续费完全自由（D103）：金额不限、随时加减、重置不要二级密码还能换金额、每个账户自己的手续费率；
// 改了费率以后下单真的按新费率收；加进来的钱不算盈利；重置后费率带过去；可以回到默认。手机宽度不横向滚动。
// 会注册账号、下模拟单：只能对着测试环境跑。
// 用法：node tools/e2e-paperfunds.mjs <截图目录> [http://127.0.0.1:4173]
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
const call = (method, path, body) => p.evaluate(async ({ method, path, body }) => {
  const r = await fetch(path, { method, headers: { 'X-QS-Client': 'web', 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined })
  return { s: r.status, b: await r.json().catch(() => null) }
}, { method, path, body })
await p.goto(BASE + '/?page=login')
await call('POST', '/api/auth/register', { email: `pf${Date.now()}${rand().slice(0, 4)}@example.com`, password: `pf-${rand()}-${rand()}` })
const list = async () => (await call('GET', '/api/paper-accounts')).b.accounts
const addBtn = () => p.locator('.ac-under .btn.secondary:not([data-live-add])')
const rows = () => p.locator('[data-papers] tbody tr')

await p.goto(BASE + '/?page=account')
await p.waitForSelector('[data-papers] tbody tr')
// 1. 新建：金额不限（带逗号也认），0 不行
await p.locator('[data-paper-initial]').fill('2,500')
await addBtn().click()
await p.waitForFunction(() => document.querySelectorAll('[data-papers] tbody tr').length === 2)
let accts = await list()
const mine = accts.find((a) => Number(a.initial) === 2500)
ok(!!mine, '填 2,500 新建了 2,500 USDT 的账户')
await p.locator('[data-paper-initial]').fill('5,000,000,000')
await addBtn().click()
await p.waitForFunction(() => document.querySelectorAll('[data-papers] tbody tr').length === 3)
ok((await list()).some((a) => Number(a.initial) === 5e9), '金额不限：50 亿也能建')
await p.locator('[data-paper-initial]').fill('0')
await addBtn().click()
await p.waitForSelector('.form-err.on')
ok(/金额要大于 0/.test(await p.locator('.form-err.on').innerText()), '0 不行，说明原因')
ok((await list()).length === 3, '金额不对不新建')
await p.locator('[data-quick="100000"]').click()
ok(await p.locator('[data-paper-initial]').inputValue() === '100000', '快捷金额填进框里')
ok(/金额不限/.test(await p.locator('.ac-amt').innerText()), '金额框旁写着"金额不限"')
ok(/本金/.test(await p.locator('[data-papers] thead').innerText()), '列名是"本金"')
ok(/吃单 0\.05% · 挂单 0\.02%/.test(await p.locator(`[data-fee="${mine.id}"]`).innerText()), '默认费率写在表里：吃单 0.05% · 挂单 0.02%')
ok(await p.locator(`[data-fee="${mine.id}"]`).isVisible(), '费率在你的窗口宽度下看得见（不是被收起来的列）')
await p.locator('[data-papers]').scrollIntoViewIfNeeded()
await p.screenshot({ path: `${OUT}/pf-01-list.png` })

// 2. 改手续费
await p.locator(`[data-fee-edit="${mine.id}"]`).click()
await p.waitForSelector('[data-fee-form]')
await p.locator('[data-fee-form] input[data-fee-taker]').fill('5')
await p.locator('[data-fee-form] .btn.primary').click()
await p.waitForSelector('[data-fee-form] .form-err.on')
ok(/0% 到 1%/.test(await p.locator('[data-fee-form] .form-err.on').innerText()), '费率超过 1% 时说明范围')
await p.locator('[data-fee-form] input[data-fee-taker]').fill('0.04')
await p.locator('[data-fee-form] input[data-fee-maker]').fill('0.01')
await p.screenshot({ path: `${OUT}/pf-02-fees.png` })
await p.locator('[data-fee-form] .btn.primary').click()
await p.waitForSelector('[data-fee-form]', { state: 'detached' })
await sleep(300)
const feeTxt = await p.locator(`[data-fee="${mine.id}"]`).innerText()
ok(/吃单 0\.04% · 挂单 0\.01%/.test(feeTxt) && /自定/.test(feeTxt), `表里是新费率并标"自定"（${feeTxt.replace(/\s+/g, ' ')}）`)
await call('POST', `/api/trade/${mine.id}/orders`, { instrument: 'BTC-PERP', side: 'buy', type: 'market', qty: '0.01' })
await sleep(800)
const v = (await call('GET', `/api/trade/${mine.id}`)).b
const pos = v.positions?.[0]
const rate = pos ? Number(v.fees) / (Number(pos.qty) * Number(pos.entry)) : 0
ok(Math.abs(rate - 0.0004) < 0.00001, `成交按 0.04% 收手续费（${(rate * 100).toFixed(4)}%）`)
ok(v.custom_fee === true && Number(v.taker_fee) === 0.0004, '账户接口里是新费率')
const pv = (await call('POST', `/api/trade/${mine.id}/preview`, { instrument: 'BTC-PERP', side: 'buy', type: 'market', qty: '1' })).b
ok(pv && Math.abs(Number(pv.fee) / Number(pv.notional) - 0.0004) < 0.00001, '下单预览也按新费率算')

// 3. 加减资金：不用重置，加进来的钱不算盈利
const pnl = (x) => Number(x.equity) - Number(x.initial)
const before = (await call('GET', `/api/trade/${mine.id}`)).b
await p.locator(`[data-funds-edit="${mine.id}"]`).click()
await p.waitForSelector('[data-funds-form]')
await p.waitForFunction(() => /现在本金/.test(document.querySelector('[data-funds-form]')?.textContent ?? ''))
await p.locator('[data-funds-form] input[data-funds-amt]').fill('2,500')
await p.screenshot({ path: `${OUT}/pf-03-funds.png` })
await p.locator('[data-funds-form] .btn.primary').click()
await p.waitForSelector('[data-funds-form]', { state: 'detached' })
await sleep(300)
const added = (await call('GET', `/api/trade/${mine.id}`)).b
ok(Number(added.initial) === 5000, `加 2,500 后本金 5,000（${added.initial}）`)
ok(Math.abs(pnl(added) - pnl(before)) < 0.5, `加进来的钱不算盈利（前 ${pnl(before).toFixed(2)}，后 ${pnl(added).toFixed(2)}）`)
ok(/5,000/.test(await rows().filter({ hasText: mine.name }).innerText()), '表里本金变成 5,000')
// 拿走太多：说清最多能拿多少
await p.locator(`[data-funds-edit="${mine.id}"]`).click()
await p.waitForSelector('[data-funds-form]')
await p.locator('[data-funds-form] .seg button').nth(1).click()
await p.waitForFunction(() => /最多能拿走/.test(document.querySelector('[data-funds-form]')?.textContent ?? ''))
await p.locator('[data-funds-form] input[data-funds-amt]').fill('4999')
await p.locator('[data-funds-form] .btn.primary').click()
await p.waitForSelector('[data-funds-form] .fld.bad')
ok(/最多能拿走 [\d,.]+ USDT/.test(await p.locator('[data-funds-form] .fld.bad .fld-m').innerText()), '拿走太多：说清最多能拿多少')
await p.locator('[data-funds-form] input[data-funds-amt]').fill('1000')
await p.locator('[data-funds-form] .btn.primary').click()
await p.waitForSelector('[data-funds-form]', { state: 'detached' })
const taken = (await call('GET', `/api/trade/${mine.id}`)).b
ok(Number(taken.initial) === 4000 && Math.abs(pnl(taken) - pnl(before)) < 0.5, `拿走 1,000：本金 4,000，盈亏不变（${taken.initial}）`)

// 4. 重置：不要二级密码，可以换金额，费率带过去
await rows().filter({ hasText: mine.name }).getByRole('button', { name: '重置' }).click()
await p.waitForSelector('.dlg-back.on')
ok(await p.locator('.dlg input[type="password"]').count() === 0, '重置不用填二级密码')
ok(await p.locator('.dlg input[data-reset-initial]').inputValue() === '4000', '重置框里默认是现在的本金')
await p.locator('.dlg input[data-reset-initial]').fill('0')
await p.locator('.dlg .btn.danger').click()
await sleep(300)
ok(/金额要大于 0/.test(await p.locator('.dlg').innerText()), '重置金额是 0：在金额框下提示')
await p.locator('.dlg input[data-reset-initial]').fill('12,345.67')
await p.screenshot({ path: `${OUT}/pf-04-reset.png` })
await p.locator('.dlg .btn.danger').click()
await p.waitForSelector('.dlg-back', { state: 'detached' })
accts = await list()
const after = accts.find((a) => a.name === mine.name)
ok(after && after.id !== mine.id && Number(after.initial) === 12345.67, '重置后是新账户，12,345.67 USDT')
ok(after?.custom_fee && Number(after.taker_fee) === 0.0004, '重置后手续费设置带过去')
const v2 = (await call('GET', `/api/trade/${after.id}`)).b
ok(Number(v2.equity) === 12345.67 && (v2.positions ?? []).length === 0, '新账户权益 12,345.67、没有持仓')

// 5. 回到默认费率
await p.locator(`[data-fee-edit="${after.id}"]`).click()
await p.waitForSelector('[data-fee-default]')
await p.locator('[data-fee-default]').click()
await p.waitForSelector('[data-fee-form]', { state: 'detached' })
await sleep(300)
const defTxt = await p.locator(`[data-fee="${after.id}"]`).innerText()
ok(/吃单 0\.05% · 挂单 0\.02%/.test(defTxt) && !/自定/.test(defTxt), '"用默认费率"回到 0.05% / 0.02%')
ok((await call('POST', '/api/trade/1000000000001/funds', { amount: '100' })).s === 404, '不是自己的账户加不了钱')
const over = await p.evaluate(() => document.documentElement.scrollWidth - innerWidth)
ok(over <= 1, `不横向滚动（${over}）`)

// 手机
const m = await browser.newContext({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 3, isMobile: true, hasTouch: true, locale: 'zh-CN', storageState: await c.storageState() })
const mp = await m.newPage()
mp.on('pageerror', (e) => errs.push(String(e)))
await mp.goto(BASE + '/?page=account')
await mp.waitForSelector('[data-papers] tbody tr')
await mp.locator('[data-papers]').scrollIntoViewIfNeeded()
await sleep(500)
const mover = await mp.evaluate(() => document.documentElement.scrollWidth - innerWidth)
ok(mover <= 1, `手机不横向滚动（${mover}）`)
const cut = await mp.evaluate(() => {
  const wrap = document.querySelector('[data-papers]')?.closest('.ac-tbl-wrap')?.getBoundingClientRect()
  return [...document.querySelectorAll('[data-papers] tbody tr:first-child button')].filter((x) => x.getBoundingClientRect().right > (wrap?.right ?? 0) + 1).map((x) => x.textContent)
})
ok(cut.length === 0, `手机上每个按钮都在表里，没被切掉（${cut.join('、')}）`)
ok(await mp.locator(`[data-fee="${after.id}"]`).isVisible(), '手机上也看得见费率')
await mp.screenshot({ path: `${OUT}/pf-05-phone.png` })
await mp.locator(`[data-funds-edit="${after.id}"]`).click()
await mp.waitForSelector('[data-funds-form]')
await sleep(400)
await mp.screenshot({ path: `${OUT}/pf-06-phone-funds.png` })

// 删除模拟账户（D108）：先确认、删了列表少一个；只剩一个时没有删除按钮
await mp.close()
const nBefore = (await list()).length
const victim = (await list()).find((a) => a.id !== mine.id)
await p.goto(BASE + '/?page=account')
await p.waitForSelector('[data-papers] tbody tr')
await p.locator(`[data-paper-del="${victim.id}"]`).click()
const ask = p.locator('.dlg', { hasText: '删除「' })
ok(!!(await ask.waitFor({ timeout: 5000 }).then(() => true).catch(() => false)), '删除模拟账户先确认')
ok((await list()).length === nBefore, '确认前没删')
await ask.locator('.btn.danger').click()
await sleep(1200)
ok((await list()).length === nBefore - 1 && !(await list()).some((a) => a.id === victim.id), '确认后列表少了这一个')
// mine 前面重置过，编号换了：按现在的列表留第一个
const keep = (await list())[0]
for (const a of (await list()).slice(1)) await call('DELETE', `/api/paper-accounts/${a.id}`)
const last = await call('DELETE', `/api/paper-accounts/${keep.id}`)
ok(last.s === 400 && last.b.code === 'LAST_PAPER_ACCOUNT', `只剩一个时不能删（${last.s} ${last.b?.code}）`)
await p.reload()
await p.waitForSelector('[data-papers] tbody tr')
ok(await p.locator('[data-paper-del]').count() === 0, '只剩一个时不显示删除按钮')

ok(errs.length === 0, `页面没有报错（${errs.slice(0, 2).join(' | ')}）`)
console.log(`\n${pass} 通过，${fail} 失败`)
await browser.close()
process.exit(fail ? 1 : 0)
