// 管理后台"系统设置"（2026-11-15，用户："别人用不能也要当场写吧？""别人不能全靠AI配置吧，比如其他的AI复盘API填写"）：
// 所有者在网页里填发信邮箱、AI 复盘点评、Telegram、数据源密钥，不改配置文件、不重启。这里像所有者一样点一遍：
// 选服务商、填、保存（要二级密码）、测试（真的收到信）、密钥只显示头尾几位、测试失败的提示里不带密钥、清掉回到配置文件；
// 管理员（不是所有者）看不到这一页。
// 会注册账号：只能对着测试环境跑（第一个注册的人是所有者，所以要刚清空的测试库）。
// 用法：node tools/e2e-sysset.mjs <截图目录> [http://127.0.0.1:4173]
import { chromium } from 'playwright'
import net from 'node:net'
import http from 'node:http'

const OUT = process.argv[2] || '.'
const BASE = process.argv[3] || 'http://127.0.0.1:4173'
let pass = 0, fail = 0
const ok = (c, name) => { c ? pass++ : fail++; console.log(`${c ? '✓' : '✗'} ${name}`) }
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
const rnd = () => Math.random().toString(36).slice(2, 10)
const until = async (f, ms = 15000) => { const t0 = Date.now(); while (Date.now() - t0 < ms) { try { const v = await f(); if (v) return v } catch { /* 再试 */ } await sleep(200) } return null }

// ---------- 本机收信端（测试环境发信指向 127.0.0.1:2525） ----------
const inbox = []
const smtp = net.createServer((c) => {
  let buf = '', inData = false, to = ''
  const w = (s) => c.write(s + '\r\n')
  w('220 sink')
  c.on('error', () => {})
  c.on('data', (d) => {
    buf += d.toString('latin1')
    for (;;) {
      if (inData) {
        const end = buf.indexOf('\r\n.\r\n')
        if (end < 0) return
        const raw = buf.slice(0, end); buf = buf.slice(end + 5)
        const head = raw.slice(0, raw.indexOf('\r\n\r\n'))
        inbox.push({ to, head })
        inData = false; w('250 queued'); continue
      }
      const i = buf.indexOf('\r\n')
      if (i < 0) return
      const line = buf.slice(0, i); buf = buf.slice(i + 2)
      const up = line.toUpperCase()
      if (up.startsWith('RCPT TO:')) { to = line.slice(8).replace(/[<> ]/g, ''); w('250 ok') }
      else if (up === 'DATA') { inData = true; w('354 go') }
      else if (up === 'QUIT') { w('221 bye'); c.end(); return }
      else w('250 ok')
    }
  })
})
await new Promise((res, rej) => { smtp.once('error', rej); smtp.listen(2525, '127.0.0.1', res) })
// 假的 Claude 接口（Anthropic Messages API 的格式），看后端是不是按 Claude 的格式发
const claudeHits = []
const claude = http.createServer((req, res) => {
  let body = ''
  req.on('data', (d) => { body += d })
  req.on('end', () => {
    claudeHits.push({ path: req.url, auth: req.headers.authorization ?? '', body })
    res.setHeader('Content-Type', 'application/json')
    // 模型列表（11-24 "获取模型列表"）：按 Anthropic /v1/models 的格式回两个
    if (req.method === 'GET' && /^\/v1\/models/.test(req.url ?? '')) {
      return res.end(JSON.stringify({ data: [{ id: 'claude-sonnet-5-5', type: 'model', display_name: 'Sonnet', created_at: '2026-01-01T00:00:00Z' }, { id: 'claude-haiku-5-5', type: 'model', display_name: 'Haiku', created_at: '2026-01-01T00:00:00Z' }], has_more: false, first_id: 'a', last_id: 'b' }))
    }
    res.end(JSON.stringify({ id: 'msg_1', type: 'message', role: 'assistant', model: 'claude-opus-5-5', content: [{ type: 'text', text: '收到' }], stop_reason: 'end_turn', usage: { input_tokens: 20, output_tokens: 3 } }))
  })
})
await new Promise((res, rej) => { claude.once('error', rej); claude.listen(2527, '127.0.0.1', res) })

const browser = await chromium.launch({ channel: process.env.PW_CHANNEL || 'msedge' })
const mk = async () => {
  const c = await browser.newContext({ viewport: { width: 1536, height: 730 }, deviceScaleFactor: 1.25, locale: 'zh-CN' })
  await c.addInitScript(() => localStorage.setItem('qs.wizardSeen', '1'))
  const p = await c.newPage()
  const call = (method, path, body) => p.evaluate(async ({ method, path, body }) => { const r = await fetch(path, { method, headers: { 'X-QS-Client': 'web', 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined }); return { s: r.status, b: await r.json().catch(() => null) } }, { method, path, body })
  return { c, p, call }
}
const errs = []
const owner = await mk()
owner.p.on('pageerror', (e) => errs.push(String(e)))
const OWNER = `own${Date.now()}@example.com`
await owner.p.goto(BASE + '/?page=login')
await owner.call('POST', '/api/auth/register', { email: OWNER, password: `ow-${rnd()}-${rnd()}` })
const TP = 'tp-' + rnd()
await owner.call('POST', '/api/me/trade-password', { new: TP })
await owner.call('POST', '/api/me/bind-email', { email: OWNER }) // 注册只要账户名（D113）；测试邮件发到账户页绑定的邮箱
ok((await owner.call('GET', '/api/me')).b?.user?.role === undefined, '账号信息里没有角色（D104 独立使用）')

const p = owner.p
await p.goto(BASE + '/?page=admin')
await p.waitForSelector('[data-sys="mail"]', { timeout: 15000 })
ok(await p.locator('[data-sys]').count() === 4, '系统设置有四组：发信邮箱、AI 复盘点评、Telegram、数据源密钥')
ok(/配置文件/.test(await p.locator('[data-sys="mail"] [data-sys-src]').innerText()), '发信邮箱：现在用的是配置文件里的（测试环境写在环境变量里）')
await p.locator('[data-sys="mail"]').scrollIntoViewIfNeeded()
ok(/配置文件/.test(await p.locator('[data-sys="ai"] [data-sys-src]').innerText()), 'AI 点评：认出配置文件里的 DeepSeek（另一组变量名）')
await p.screenshot({ path: `${OUT}/sys-01-before.png` })

// 选服务商：Gmail 自动填服务器，给"去拿授权码"的链接
const pick = async (sec, label) => { await p.locator(`[data-sys="${sec}"] .sys-preset .sel-btn`).click(); await p.locator('.sel-pop li', { hasText: label }).first().click() }
await p.locator('[data-sys="mail"] .sys-preset .sel-btn').click()
ok(await p.locator('.sel-pop li', { hasText: 'Outlook' }).count() === 0, '发信邮箱没有 Outlook 选项（个人 Outlook 已经不支持授权码发信）')
await p.keyboard.press('Escape')
await pick('mail', 'QQ')
ok(/mail\.qq\.com/.test(await p.locator('[data-sys="mail"] [data-sys-help]').getAttribute('href') ?? ''), '选 QQ 邮箱：给出去拿授权码的链接')
await pick('mail', 'Gmail')
ok(/myaccount\.google\.com\/apppasswords/.test(await p.locator('[data-sys="mail"] [data-sys-help]').getAttribute('href') ?? ''), '选 Gmail：给出 Gmail 应用专用密码的链接')
// 用"其他"指到本机收信端（测试环境发不了真的 Gmail）
await pick('mail', '其他')
const mail = p.locator('[data-sys="mail"]')
await mail.locator('input[data-sys-key="mail.host"]').fill('127.0.0.1')
await mail.locator('input[data-sys-key="mail.port"]').fill('2525')
await mail.locator('input[data-sys-key="mail.user"]').fill('sender@example.com')
await mail.locator('input[data-sys-key="mail.pass"]').fill('abcd efgh ijkl mnop')
await mail.locator('input[data-sys-key="mail.from_name"]').fill('我的工作台')
await mail.locator('[data-sys-save]').click()
await p.waitForSelector('[data-admin-ask]')
ok(/马上生效/.test(await p.locator('[data-admin-ask]').innerText()), '保存前说明：马上生效、密钥加密存')
await p.locator('[data-admin-ask] input[type="password"]').fill(TP)
await p.locator('[data-admin-ask-ok]').click()
await p.waitForSelector('[data-admin-ask]', { state: 'detached' })
ok(await until(async () => /这里填的/.test(await mail.locator('[data-sys-src]').innerText())), '保存后：用的是网页里填的')
ok(await until(async () => /abcd…mnop/.test(await mail.locator('input[data-sys-key="mail.pass"]').getAttribute('placeholder') ?? '')), '授权码只显示头尾几位（空格去掉了）')
const before = inbox.length
await mail.locator('[data-sys-test]').click()
const msg = await until(async () => { const t = await mail.locator('[data-sys-msg]').innerText(); return t || null })
ok(/已发到 own/.test(msg ?? ''), `测试：页面写着发到了哪里（${msg}）`)
const got = await until(async () => inbox.slice(before).find((x) => x.to === OWNER))
ok(!!got, '收信端真的收到了测试邮件')
ok(/sender@example\.com/.test(got?.head ?? ''), '发件人是网页里填的邮箱（不是配置文件里的）')
await p.screenshot({ path: `${OUT}/sys-02-mail.png` })

// AI 复盘点评：自定义厂商列表（D112，11-23）。加一家连不上的：测试失败的提示里不带密钥、说大白话；第一家加进来就用它
await p.locator('[data-sys="ai"]').scrollIntoViewIfNeeded()
const ai = p.locator('[data-sys="ai"]')
const askPw = async () => {
  await p.locator('[data-admin-ask] input[type="password"]').fill(TP)
  await p.locator('[data-admin-ask-ok]').click()
  await p.waitForSelector('[data-admin-ask]', { state: 'detached' })
}
const form = ai.locator('[data-ai-form]')
await ai.locator('[data-ai-add]').click()
await form.locator('input[data-ai-f="name"]').fill('连不上的')
await form.locator('input[data-ai-f="base"]').fill('http://127.0.0.1:9')
await form.locator('input[data-ai-f="key"]').fill('sk-test-secret-123456')
await form.locator('input[data-ai-f="model"]').fill('m1')
await form.locator('.seg button[data-tab="high"]').click()
ok(await form.locator('.seg button[aria-pressed="true"]').innerText() === '高', '思考程度有六档，能选"高"')
await form.locator('[data-ai-ftest]').click()
const aiMsg = await until(async () => (await form.locator('[data-sys-msg]').innerText().catch(() => '')) || null, 30000)
ok(!!aiMsg && !/sk-test-secret/.test(aiMsg), `没存就能测试；失败提示里没有密钥（${(aiMsg ?? '').slice(0, 60)}）`)
ok(/连不上/.test(aiMsg ?? '') && !/dial tcp|connectex/.test(aiMsg ?? ''), 'AI 测试失败：说的是大白话"连不上"，不是网络错误原文')
await p.screenshot({ path: `${OUT}/sys-02b-ai-form.png` })
await form.locator('[data-ai-save]').click()
await askPw()
await ai.locator('[data-ai-row]').first().waitFor()
ok(/现在用：连不上的/.test(await ai.locator('[data-sys-src]').innerText()), '第一家加进来就用它')
// 再加一家 Claude 格式的，指到本机的假接口；模型不填（用 claude-opus-5-5），思考程度"低"
await ai.locator('[data-ai-add]').click()
await form.locator('input[data-ai-f="name"]').fill('本机 Claude')
await form.locator('.sys-sel .sel-btn').click()
await p.locator('.sel-pop li', { hasText: 'Anthropic' }).first().click()
ok(/claude-opus-5-5/.test(await form.locator('input[data-ai-f="model"]').getAttribute('placeholder') ?? ''), '选 Anthropic：模型名提示 claude-opus-5-5')
await form.locator('input[data-ai-f="base"]').fill('http://127.0.0.1:2527')
await form.locator('input[data-ai-f="key"]').fill('sk-ant-test-abcdef')
await form.locator('.seg button[data-tab="low"]').click()
// 获取模型列表：问这家有哪些模型，能搜，点一个填进模型名；再清空（不填 = 用默认的 claude-opus-5-5）
await form.locator('[data-ai-models]').click()
await form.locator('[data-ai-models-list] [data-ai-model]').first().waitFor({ timeout: 20000 })
ok(await form.locator('[data-ai-model]').count() === 2, '获取模型列表：列出这家的两个模型')
await form.locator('[data-ai-models-q]').fill('haiku')
ok(await form.locator('[data-ai-model]').count() === 1, '模型列表能搜')
await p.screenshot({ path: `${OUT}/sys-02d-ai-models.png` })
await form.locator('[data-ai-model="claude-haiku-5-5"]').click()
ok(await form.locator('input[data-ai-f="model"]').inputValue() === 'claude-haiku-5-5' && await form.locator('[data-ai-models-list]').count() === 0, '点一个就填进模型名，列表收起')
await form.locator('input[data-ai-f="model"]').fill('')
await form.locator('[data-ai-save]').click()
await askPw()
const claudeRow = ai.locator('[data-ai-row]', { hasText: '本机 Claude' })
await claudeRow.waitFor()
ok(await claudeRow.locator('[data-ai-on]').count() === 0 && /思考：低/.test(await claudeRow.innerText()), '加第二家不换正在用的；列表里写着思考程度')
// 切换到它
await claudeRow.locator('[data-ai-use]').click()
await askPw()
await until(async () => (await claudeRow.locator('[data-ai-on]').count()) === 1)
ok(/现在用：本机 Claude/.test(await ai.locator('[data-sys-src]').innerText()), '切换后写着现在用哪家')
await p.screenshot({ path: `${OUT}/sys-02c-ai-list.png` })
await claudeRow.locator('[data-ai-test]').click()
const cMsg = await until(async () => { const t = await claudeRow.locator('[data-sys-msg]').innerText().catch(() => ''); return /通了|不/.test(t) ? t : null }, 30000)
ok(/通了：claude-opus-5-5 回复「收到」/.test(cMsg ?? ''), `Claude 测试通了，没填模型用 claude-opus-5-5（${(cMsg ?? '').slice(0, 60)}）`)
const hit = claudeHits.at(-1)
let hb = {}
try { hb = JSON.parse(hit?.body ?? '{}') } catch { /* 下面会报 */ }
ok(hit?.path === '/v1/messages' && hit?.auth === 'Bearer sk-ant-test-abcdef' && hb.max_tokens > 0 && !('temperature' in hb), 'Claude 按它自己的格式发：/v1/messages、密钥在请求头、不带 temperature')
ok(hb.output_config?.effort === 'low', `思考程度"低"真的发出去了（${JSON.stringify(hb.output_config ?? null)}）`)
// 编辑第一家：密钥留空 = 不改，改模型
const firstRow = ai.locator('[data-ai-row]', { hasText: '连不上的' })
await firstRow.locator('[data-ai-edit]').click()
ok(/sk-t…3456/.test(await form.locator('input[data-ai-f="key"]').getAttribute('placeholder') ?? ''), '编辑时密钥只显示头尾几位')
await form.locator('input[data-ai-f="model"]').fill('m2')
await form.locator('[data-ai-save]').click()
await askPw()
await until(async () => /m2/.test(await firstRow.innerText()))
const list = (await owner.call('GET', '/api/admin/ai')).b
const one = list.list.find((x) => x.name === '连不上的')
ok(one?.model === 'm2' && one?.key_hint === 'sk-t…3456' && !JSON.stringify(list).includes('sk-test-secret') && !JSON.stringify(list).includes('sk-ant-test'), '改了模型、密钥留着；接口不回密钥')
// 删掉第一家
await firstRow.locator('[data-ai-del]').click()
await askPw()
await until(async () => (await ai.locator('[data-ai-row]', { hasText: '连不上的' }).count()) === 0)
ok((await owner.call('GET', '/api/admin/ai')).b.list.length === 1, '删掉一家')
// Telegram：令牌在地址里，失败提示要把它藏起来
const tg = p.locator('[data-sys="tg"]')
await tg.locator('input[data-sys-key="tg.token"]').fill('999:secret-token-xyz')
await tg.locator('[data-sys-save]').click()
await p.locator('[data-admin-ask] input[type="password"]').fill(TP)
await p.locator('[data-admin-ask-ok]').click()
await p.waitForSelector('[data-admin-ask]', { state: 'detached' })
await tg.locator('[data-sys-test]').click()
const tgMsg = await until(async () => (await tg.locator('[data-sys-msg]').innerText()) || null, 30000)
ok(!!tgMsg && !/secret-token/.test(tgMsg), `Telegram 测试失败：提示里没有令牌（${(tgMsg ?? '').slice(0, 60)}）`)
ok(/连不上/.test(tgMsg ?? ''), 'Telegram 测试失败：说的是大白话')
// 数据源：X 账号
const data = p.locator('[data-sys="data"]')
await data.locator('input[data-sys-key="x.accounts"]').fill('foo, @bar')
await data.locator('[data-sys-save]').click()
await p.locator('[data-admin-ask] input[type="password"]').fill(TP)
await p.locator('[data-admin-ask-ok]').click()
await p.waitForSelector('[data-admin-ask]', { state: 'detached' })
const st = (await owner.call('GET', '/api/admin/settings')).b
ok(st.fields.find((f) => f.key === 'x.accounts')?.value === 'foo, @bar', '数据源：X 账号存上了')
ok(!JSON.stringify(st).includes('sk-test-secret') && !JSON.stringify(st).includes('secret-token-xyz') && !JSON.stringify(st).includes('abcdefghijklmnop'), '接口里看不到任何完整的密钥')
await p.screenshot({ path: `${OUT}/sys-03-all.png`, fullPage: false })

// 清掉发信邮箱：回到配置文件的
await mail.scrollIntoViewIfNeeded()
await mail.locator('[data-sys-clear]').click()
await p.locator('[data-admin-ask] input[type="password"]').fill(TP)
await p.locator('[data-admin-ask-ok]').click()
await p.waitForSelector('[data-admin-ask]', { state: 'detached' })
ok(await until(async () => /配置文件/.test(await mail.locator('[data-sys-src]').innerText())), '清掉后：回到配置文件里的')

// D113（11-24）：只有所有者能看、能改系统设置；后注册的账号连接口都调不了（里面是整套系统的密钥）
const adm = await mk()
const ADM = `second${Date.now()}`
await adm.p.goto(BASE + '/?page=login')
await adm.call('POST', '/api/auth/register', { name: ADM, password: `ad-${rnd()}-${rnd()}` })
ok((await adm.call('GET', '/api/admin/settings')).s === 403 && (await adm.call('GET', '/api/admin/ai')).s === 403, '第二个账号调不了系统设置、AI 厂商的接口')
await adm.p.goto(BASE + '/?page=admin')
await adm.p.waitForSelector('[data-admin]', { timeout: 15000 })
ok(!!(await until(async () => (await adm.p.locator('[data-admin-forbidden]').count()) > 0)) && await adm.p.locator('[data-sys]').count() === 0, '第二个账号的"系统"页：只写明只有所有者能看')

// 手机
const m = await browser.newContext({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 3, isMobile: true, hasTouch: true, locale: 'zh-CN', storageState: await owner.c.storageState() })
const mp = await m.newPage()
mp.on('pageerror', (e) => errs.push(String(e)))
await mp.goto(BASE + '/?page=admin')
await mp.waitForSelector('[data-sys="mail"]', { timeout: 15000 })
await mp.locator('[data-sys="mail"]').scrollIntoViewIfNeeded()
await sleep(500)
const over = await mp.evaluate(() => document.documentElement.scrollWidth - innerWidth)
ok(over <= 1, `手机不横向滚动（${over}）`)
await mp.screenshot({ path: `${OUT}/sys-04-phone.png` })

ok(errs.length === 0, `页面没有报错（${errs.slice(0, 2).join(' | ')}）`)
console.log(`\n${pass} 通过，${fail} 失败`)
smtp.close()
claude.close()
await browser.close()
process.exit(fail ? 1 : 0)
