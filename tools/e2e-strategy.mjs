// 自助建策略（D96、18 第 5.1、5.4 条）走一遍：界面建策略、密钥只显示一次、用对外的示例脚本（Python）真的发信号、
// 下载前向记录并用对外的验证脚本验、换密钥后旧的不认、吊销后都不认、策略名不能给别人用。
// 会注册账号：只能对着测试环境跑（不要对着 8080）。要 Python 3。
// 用法：node tools/e2e-strategy.mjs <截图目录> [http://127.0.0.1:4173]
import { chromium } from 'playwright'
import { execFileSync } from 'node:child_process'
import { writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const OUT = resolve(process.argv[2] || '.')
const BASE = process.argv[3] || 'http://127.0.0.1:4173'
// 示例脚本在仓库的 docs/examples；工作副本里没有时用环境变量指过去
const EX = process.env.QS_EXAMPLES || join(dirname(fileURLToPath(import.meta.url)), '..', 'docs', 'public', 'examples')
let pass = 0, fail = 0
const ok = (cond, name) => { if (cond) { pass++; console.log('✓', name) } else { fail++; console.log('✗', name) } }
const browser = await chromium.launch({ channel: process.env.PW_CHANNEL || 'msedge' })
const rand = () => Math.random().toString(36).slice(2)
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
const until = async (fn, ms = 15000) => {
  const t0 = Date.now()
  while (Date.now() - t0 < ms) {
    const v = await fn().catch(() => null)
    if (v) return v
    await sleep(250)
  }
  return null
}
const newPage = async (opts = {}) => {
  const c = await browser.newContext({ viewport: { width: 1536, height: 730 }, deviceScaleFactor: 1.25, locale: 'zh-CN', acceptDownloads: true, ...opts })
  await c.addInitScript(() => { localStorage.setItem('qs.wizardSeen', '1') })
  const p = await c.newPage()
  p.errs = []
  p.on('pageerror', (e) => p.errs.push(String(e)))
  p.on('console', (m) => { if (m.type() === 'error' && !/Failed to load resource/.test(m.text())) p.errs.push(m.text()) })
  return p
}
const call = (p, method, path, body) => p.evaluate(async ({ method, path, body }) => {
  const r = await fetch(path, { method, headers: { 'X-QS-Client': 'web', 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined })
  let j = null
  try { j = await r.json() } catch { /* 不是 JSON */ }
  return { s: r.status, b: j }
}, { method, path, body })
const TP = 'tp-' + rand()
const register = async (p) => {
  await p.goto(BASE + '/?page=login')
  const r = await call(p, 'POST', '/api/auth/register', { email: `st${Date.now()}${rand().slice(0, 4)}@example.com`, password: `st-${rand()}-pass` })
  if (r.s !== 200) throw new Error('注册失败 ' + JSON.stringify(r))
  await call(p, 'POST', '/api/me/trade-password', { new: TP })
}
// 用对外的示例脚本发一条（和策略作者一样）。返回脚本打印的第一行（状态码和回复）
const send = (key, secret, sid, weight) => {
  try {
    return execFileSync('python', [join(EX, 'qsig_send.py'), 'BTC-PERP', weight], {
      env: { ...process.env, QS_BASE: BASE, QS_KEY: key, QS_SECRET: secret, QS_STRATEGY: sid, PYTHONIOENCODING: 'utf-8' }, encoding: 'utf-8', timeout: 30000,
    }).split('\n')[0]
  } catch (e) { return String(e.stdout || e.stderr || e.message).trim().split('\n').pop() } // 示例脚本出错时把原因写在最后一行
}

const p = await newPage()
await register(p)
await p.goto(BASE + '/?page=account')
const sec = p.locator('.ac-sec', { has: p.locator('h2', { hasText: '我的策略' }) })
await sec.waitFor()
await sec.scrollIntoViewIfNeeded()
await sec.locator('[data-mys-add]').click()
const form = p.locator('[data-mys-form]')
await form.waitFor()
// 策略名：只能小写字母、数字、短横线（输入时就过滤），太短按钮不能点
await form.locator('input').first().fill('AB')
ok(await form.locator('input').first().inputValue() === 'ab' && await form.locator('[data-mys-ok]').isDisabled(), '策略名自动转小写，太短不能提交')
const SID = `e2e-${rand().slice(0, 6)}`
await form.locator('input').first().fill(SID)
await form.locator('input').nth(1).fill('均线测试')
ok(await form.locator('input[type="password"]').count() === 0, '新建策略不要二级密码（D107：新策略没人跟随，碰不到钱）')
await form.locator('[data-mys-ok]').click()
const shown = p.locator('[data-mys-shown]')
await shown.waitFor()
const KEY = await shown.locator('[data-mys-secret="key"]').innerText()
const SECRET = await shown.locator('[data-mys-secret="secret"]').innerText()
ok(/^k-[0-9a-f]{16}$/.test(KEY) && /^[0-9a-f]{64}$/.test(SECRET), '建好后显示 key id 和 secret')
ok(/只显示这一次/.test(await shown.innerText()), '写明 secret 只显示一次')
await p.waitForTimeout(400) // 新建对话框关、密钥对话框开，等动画走完
await shot(p, 'st-01-secret', '.dlg:has([data-mys-shown])')
await shown.locator('[data-mys-done]').click()
ok(!!(await until(async () => (await sec.locator(`[data-mys-row="${SID}"]`).count()) > 0)), '列表里有这个策略')
ok(!(await sec.innerText()).includes(SECRET), '列表里看不到 secret')
await p.waitForTimeout(800) // 列表刷新完再截图（刷新时这一节会重画）
await shot(p, 'st-02-list', '.ac-sec:has(h2:text("我的策略"))').catch((e) => console.log('（截图没截上：' + String(e).slice(0, 60) + '）'))

// 用对外的示例脚本真的发信号：建好马上能用，不用重启
const r1 = send(KEY, SECRET, SID, '0.1')
ok(/^200 .*accepted/.test(r1), `示例脚本发的信号被接受（${r1.slice(0, 80)}）`)
const r2 = send(KEY, SECRET, SID, '0.15')
ok(/^200 /.test(r2), '再发一条')
const list = (await call(p, 'GET', '/api/strategies')).b
ok(Array.isArray(list) && list.some((x) => x.strategy_id === SID), '策略页的列表里有它')
// 18 第 8.4 条：自己的策略发了新目标，主人收到提醒（只提醒、不下单）；设置里写明
const sn = await until(async () => (await call(p, 'GET', '/api/notify')).b.items.filter((x) => x.kind === 'strategy_signal' && x.params.strategy === SID))
ok(!!sn && sn.length === 2 && /BTC-PERP:0\.15/.test(sn[0].params.targets), `策略每发一条，主人收到一条提醒（${sn?.length} 条，最新 ${sn?.[0]?.params.targets}）`)
await p.locator('.bar-bell-btn').click()
const bellText = (await until(async () => { const s = await p.locator('.bar-bell-pop').innerText(); return /发了新目标/.test(s) ? s : null })) ?? ''
ok(/你的策略 .+ 发了新目标：BTC 做多 15%/.test(bellText) && /平台只转达，没有替你下单/.test(bellText), '铃铛里写明：策略发了什么、平台只转达没有下单')
await shot(p, 'st-06-push', '.bar-bell-pop')
await p.keyboard.press('Escape')
ok(/只提醒，不下单/.test(await sec.locator('[data-mys-push]').innerText().catch(() => '')), '"我的策略"里写明调仓会提醒、在哪设置')

// 前向记录：策略页下载，用对外的验证脚本验
await p.goto(BASE + '/?page=ai')
await p.waitForSelector('.ai-head')
const sel = p.locator('.ai-title .sel-btn')
if (await sel.count()) {
  await sel.click()
  await p.locator('.sel-pop li', { hasText: SID }).click()
}
ok(!!(await until(async () => (await p.locator('.ai-title').innerText()).includes(SID))), '策略页切到这个策略')
ok((await p.locator('.nav a[aria-current="page"]').innerText()).trim() === '策略', '导航写"策略"（不再是"AI"）')
// 验证报告：11 项指标，每项写结果和门槛；门槛能改（存在这台浏览器里）
await p.locator('.ai-list .seg button', { hasText: '验证报告' }).click()
await p.waitForSelector('[data-vr]', { timeout: 30000 })
ok(await p.locator('[data-vr-row]').count() === 11, `验证报告有 11 项（${await p.locator('[data-vr-row]').count()}）`)
ok(/达到 \d+ 项，没达到 \d+ 项，数据不够 \d+ 项/.test(await p.locator('.vr-sum').innerText()), '顶上一句话：几项达到、几项没达到、几项数据不够')
ok(await p.locator('[data-vr-row="vr.days"]').getAttribute('data-vr-ok') === 'no', '刚接进来：前向时长没达到')
await p.locator('[data-vr-edit]').click()
await p.locator('[data-vr-th="days"]').fill('0')
ok(!!(await until(async () => (await p.locator('[data-vr-row="vr.days"]').getAttribute('data-vr-ok')) === 'ok')), '门槛改成 0 天：变成达到')
await p.locator('.ai-list').screenshot({ path: `${OUT}/st-04-report.png` })
await p.locator('[data-vr-ths] button').click() // 恢复默认
// 回测一致性（18 第 5.5 条）：收到的目标仓位导出成信号表，用同样的信号回测，和前向摆在一起
const tableText = await p.evaluate(async (sid) => (await fetch(`/api/strategies/${sid}/signal-table`)).text(), SID)
const tableRows = tableText.split('\n').filter((l) => l && !l.startsWith('#'))
ok(tableRows.length >= 2 && tableRows.every((l) => /^\d{4}-\d\d-\d\dT[\d:]+Z,\{"instrument":"[A-Z]+-PERP","weight":-?[\d.]+\}$/.test(l)), `信号表：每行"时间,目标仓位"（${tableRows[0]}）`)
await p.locator('[data-vr-bt-run]').click()
const btDone = await until(async () => (await p.locator('[data-vr-bt-result]').count()) > 0 || (await p.locator('[data-vr-bt] .warn-t').count()) > 0, 180000)
const btText = await p.locator('[data-vr-bt]').innerText()
ok(!!btDone && (await p.locator('[data-vr-bt-result]').count()) > 0, `用同样的信号回测：出结果（${btText.replace(/\s+/g, ' ').slice(-80)}）`)
ok(/收益[\s\S]*已平仓笔数[\s\S]*手续费/.test(btText) && !/NaN/.test(btText), '对照：收益、交易笔数、手续费，前向和回测摆在一起')
await shot(p, 'st-05-consistency', '[data-vr-bt]')
const dl = p.waitForEvent('download', { timeout: 15000 })
await p.locator('[data-ai-log]').click()
const file = join(OUT, `${SID}.jsonl`)
await (await dl).saveAs(file)
let out = ''
try { out = execFileSync('python', [join(EX, 'verify_log.py'), file], { encoding: 'utf-8', env: { ...process.env, PYTHONIOENCODING: 'utf-8' } }) } catch (e) { out = String(e.stdout || e.message) }
ok(/通过：2 条/.test(out), `验证脚本：链完整（${out.split('\n')[0]}）`)
const last = (out.match(/最后一环：([0-9a-f]{64})/) || [])[1]
// 改一条：验证脚本要指出来
const lines = (await import('node:fs')).readFileSync(file, 'utf-8').trim().split('\n')
const first = JSON.parse(lines[0])
first.raw = first.raw.replace('"0.1"', '"0.9"')
writeFileSync(file + '.bad', [JSON.stringify(first), ...lines.slice(1)].join('\n') + '\n')
try { out = execFileSync('python', [join(EX, 'verify_log.py'), file + '.bad'], { encoding: 'utf-8', env: { ...process.env, PYTHONIOENCODING: 'utf-8' } }) } catch (e) { out = String(e.stdout || e.message) }
ok(/被改过/.test(out), `改了第一条：验证脚本指出来（${out.split('\n')[0]}）`)
await shot(p, 'st-03-page', '.ai-head')

// 换密钥：旧的马上不认，新的能用；链接着往下走
await p.goto(BASE + '/?page=account')
await sec.waitFor()
await sec.locator(`[data-mys-row="${SID}"] [data-mys-rotate]`).click()
const ask = p.locator('[data-mys-ask]')
await ask.waitFor()
await ask.locator('input[type="password"]').fill(TP)
await ask.locator('[data-mys-ask-ok]').click()
await shown.waitFor()
const KEY2 = await shown.locator('[data-mys-secret="key"]').innerText()
const SECRET2 = await shown.locator('[data-mys-secret="secret"]').innerText()
await shown.locator('[data-mys-done]').click()
ok(KEY2 !== KEY && SECRET2 !== SECRET, '换出一把新密钥')
{ const o = send(KEY, SECRET, SID, '0.1'); ok(/^401 /.test(o), `旧密钥马上不认（${o.slice(0, 90)}）`) }
ok(/^200 /.test(send(KEY2, SECRET2, SID, '0.12')), '新密钥能用')
const txt = await p.evaluate(async (id) => (await fetch(`/api/strategies/${id}/log`)).text(), SID)
writeFileSync(file, txt)
try { out = execFileSync('python', [join(EX, 'verify_log.py'), file, last], { encoding: 'utf-8', env: { ...process.env, PYTHONIOENCODING: 'utf-8' } }) } catch (e) { out = String(e.stdout || e.message) }
ok(/通过：3 条/.test(out) && !/不在这条链上/.test(out), '换密钥后链接着走；上次记下的那一环还在链上')

// 对外的 Python 客户端 qsclient.py：发目标、等执行回报、心跳
const py = [
  'from qsclient import Client',
  'c = Client()',
  'r = c.target({"BTC-PERP": 0.12}, reason="e2e", confidence=0.6)',
  'ex = c.wait(r["id"], 20)',
  'print(r["status"], len(ex), ex[0].get("state") if ex else "-", c.heartbeat(note="e2e")["status"])',
].join('\n')
let pyOut = ''
try {
  pyOut = execFileSync('python', ['-c', py], { cwd: EX, encoding: 'utf-8', timeout: 60000,
    env: { ...process.env, QS_BASE: BASE, QS_KEY: KEY2, QS_SECRET: SECRET2, QS_STRATEGY: SID, PYTHONIOENCODING: 'utf-8' } }).trim()
} catch (e) { pyOut = String(e.stderr || e.stdout || e.message).trim().split('\n').pop() }
ok(/^accepted 1 \w+ accepted$/.test(pyOut), `qsclient.py：发目标、拿到执行回报、心跳（${pyOut}）`)

// 别人不能用这个策略名
const q = await newPage()
await register(q)
const taken = await call(q, 'POST', '/api/my-strategies', { strategy_id: SID, name: '', trade_password: TP })
ok(taken.b?.code === 'STRATEGY_TAKEN', `策略名不能给别人用（${taken.b?.code}）`)
const bad = await call(q, 'POST', '/api/my-strategies', { strategy_id: 'x', name: '', trade_password: TP })
ok(bad.b?.code === 'STRATEGY_BAD_ID', `策略名太短说清楚（${bad.b?.code}）`)
ok((await call(q, 'POST', `/api/my-strategies/${SID}/revoke`, { trade_password: TP })).s === 404, '别人吊销不了')

// 吊销：都不认
await sec.locator(`[data-mys-row="${SID}"] [data-mys-revoke]`).click()
await ask.waitFor()
ok(/全部被拒/.test(await ask.innerText()), '吊销前写清楚会发生什么')
await ask.locator('input[type="password"]').fill(TP)
await ask.locator('[data-mys-ask-ok]').click()
ok(!!(await until(async () => (await sec.locator(`[data-mys-row="${SID}"]`).count()) === 0)), '列表里没了')
{ const o = send(KEY2, SECRET2, SID, '0.1'); ok(/^401 /.test(o), `吊销后不认（${o.slice(0, 90)}）`) }

// 手机
const m = await newPage({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 3, isMobile: true, hasTouch: true })
await m.context().addCookies(await p.context().cookies())
await m.goto(BASE + '/?page=account')
await m.locator('.ac-sec', { has: m.locator('h2', { hasText: '我的策略' }) }).scrollIntoViewIfNeeded()
const over = await m.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)
ok(over <= 1, `手机宽度不横向滚动（多出 ${over}px）`)

ok([p, q, m].every((x) => x.errs.length === 0), `页面没有报错（${[p, q, m].flatMap((x) => x.errs).join(' | ').slice(0, 300)}）`)
await browser.close()
console.log(`\n${pass} 通过，${fail} 失败`)
process.exit(fail ? 1 : 0)

function shot(pg, name, el) { return (el ? pg.locator(el).first() : pg).screenshot({ path: `${OUT}/${name}.png` }) }
