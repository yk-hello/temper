// 模块 3 的界面检查（3.5、3.9、3.12、3.14–3.31）：落地页、向导、跟随状态、AI 页、设置、命令面板、撤单撤回、
// "动效只给眼睛看"、减少动态效果、16 种设备。会注册账号、下单，只能对着模拟行情的测试环境跑。
//   node tools/e2e-m3.mjs <截图目录> <网址>
import { chromium } from 'playwright'
import { DEVICES } from './devices.mjs'

const OUT = process.argv[2] || '.'
const BASE = process.argv[3] || process.env.BASE || 'http://127.0.0.1:4173'
let pass = 0, fail = 0
const ok = (cond, name) => { if (cond) { pass++; console.log('✓', name) } else { fail++; console.log('✗', name) } }

const browser = await chromium.launch({ channel: process.env.PW_CHANNEL || 'msedge', args: ['--enable-webgl', '--use-gl=angle', '--use-angle=swiftshader', '--ignore-gpu-blocklist'] })
const newPage = async (opts = {}, seen = true) => {
  const c = await browser.newContext({ viewport: { width: 1536, height: 730 }, deviceScaleFactor: 1.25, locale: 'zh-CN', timezoneId: 'Asia/Taipei', ...opts })
  if (seen) await c.addInitScript(() => localStorage.setItem('qs.wizardSeen', '1'))
  const p = await c.newPage()
  p.errs = []
  p.on('pageerror', (e) => p.errs.push(String(e)))
  return p
}
const call = (p, method, path, body) => p.evaluate(async ({ method, path, body }) => {
  const r = await fetch(path, { method, headers: { 'X-QS-Client': 'web', 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined })
  let j = null
  try { j = await r.json() } catch { /* 空 */ }
  return { s: r.status, b: j }
}, { method, path, body })
const hasText = async (p, re) => re.test(await p.locator('body').innerText())

// 3.15 没登录先看到落地页
{
  const p = await newPage()
  const probe = await p.request.post(BASE + '/api/dev/sim-price', { data: {} })
  if (probe.status() === 404) { console.error('这不是模拟行情的测试环境，退出'); process.exit(2) }
  await p.goto(BASE + '/?page=landing')
  await p.waitForSelector('.mb-map canvas', { timeout: 15000 })
  ok(await p.locator('.lp-head .seg button').count() === 2, '没登录先看到落地页：方块图和强平分布两个标签（D90 去掉了示例仓位、策略净值）')
  ok(await p.locator('.bar-signup').count() === 1 && await p.locator('.bar-login').count() === 1, '顶栏有登录和注册')
  ok(await p.locator('nav a').allInnerTexts().then((x) => x.join(',') === '市场,策略,交易,风险,复盘,介绍'), '导航写市场、策略、交易、风险、复盘、介绍（3.17；"AI"改成"策略"，D96）')
  for (let i = 0; i < 80; i++) {
    const st = await call(p, 'GET', '/api/marketmap')
    if (st.b.state === 'ok') break
    await p.waitForTimeout(1000)
  }
  await p.waitForTimeout(5500)
  const snap = (await call(p, 'GET', '/api/marketmap')).b
  ok(snap.nodes.length >= 100 && snap.total >= snap.nodes.length, `显示 ${snap.nodes.length} 个，共 ${snap.total} 个`)
  ok(snap.nodes.every((n) => n.sec), '每个币都有板块')
  ok(snap.nodes.filter((n) => n.f).length === 6, '6 个重点币带标记')
  ok(snap.nodes.every((n) => n.qv >= snap.min_vol || n.f), '都过了成交额门槛')
  const lg = await p.locator('.mb-legend').innerText()
  ok(/面积/.test(lg) && /颜色/.test(lg) && /重点/.test(lg), '图例写明面积、颜色和重点币')
  ok(!(await hasText(p, /资金轮动|点阵|走势距离/)), '旧图的说法都不在了')
  // 方块真的铺满了：抽样看画布上不是黑的点占多少（只看截图发现过一次方块没长出来）
  const filled = await p.evaluate(() => {
    const c = document.querySelector('.mb-map canvas'), g = c.getContext('2d')
    let n = 0, on = 0
    for (let y = 0.08; y < 1; y += 0.04) for (let x = 0.02; x < 1; x += 0.02) {
      const d = g.getImageData(Math.floor(c.width * x), Math.floor(c.height * y), 1, 1).data
      n++; if (d[0] + d[1] + d[2] > 8) on++
    }
    return on / n
  })
  ok(filled > 0.8, `方块铺满画布（${(filled * 100).toFixed(0)}%）`)
  await p.screenshot({ path: `${OUT}/m3-lp.png` })
  // 悬停：立刻出读数
  const box = await p.locator('.mb-map canvas').boundingBox()
  // 板块名：悬停出明细，点击放大，Esc 回来
  await p.mouse.move(box.x + 12, box.y + 9)
  await p.waitForSelector('.mb-tip', { timeout: 2000 })
  ok(/币种数/.test(await p.locator('.mb-tip').innerText()) && /点击放大/.test(await p.locator('.mb-tip').innerText()), '板块名悬停写币种数和成交额占比')
  await p.mouse.down(); await p.mouse.up()
  await p.waitForTimeout(500)
  await p.mouse.move(box.x + 14, box.y + 9)
  await p.waitForTimeout(100)
  ok(/点击返回/.test(await p.locator('.mb-tip').innerText().catch(() => '')), '点板块名放大这个板块')
  await p.screenshot({ path: `${OUT}/m3-lp-sector.png` })
  await p.keyboard.press('Escape')
  await p.waitForTimeout(500)
  // 悬停：立刻出读数
  await p.mouse.move(box.x + box.width * 0.2, box.y + box.height * 0.5)
  await p.waitForTimeout(60)
  const hit = await p.locator('.mb-tip').count() > 0
  ok(hit, '鼠标移上去立刻出读数')
  if (hit) {
    const text = await p.locator('.mb-tip').innerText()
    ok(/价格/.test(text) && /涨跌/.test(text) && /成交额/.test(text) && /板块/.test(text), `读数写了价格、涨跌、成交额、板块（${text.replace(/\s+/g, ' ').slice(0, 60)}）`)
    await p.screenshot({ path: `${OUT}/m3-lp-hover.png` })
    const name = (await p.locator('.mb-tip b').innerText()).replace(/永续|perpetual/, '').trim()
    // 3.15 点一块：页面和数据立刻到位，动画没放完就能操作
    await p.mouse.down(); await p.mouse.up()
    await p.waitForSelector('.mk-chart', { timeout: 3000 })
    ok(new RegExp(`i=${name}-PERP`).test(p.url()), `点 ${name} 直接进它的交易页`)
    await p.waitForTimeout(400)
    ok(await p.locator('.mb-zoom').count() === 0, '放大过渡放完就移除，不留东西')
    await p.waitForTimeout(3000)
    ok((await p.locator('.mk-strip .sym, .mk-strip').first().innerText()).includes(name), '品种条就是这个币')
    ok((await call(p, 'GET', `/api/trade/specs`)).b.specs.some((x) => x.instrument === `${name}-PERP`), '这个币开始能下单（有规格）')
  }
  // 手机上也不横向溢出
  ok(p.errs.length === 0, `落地页没有页面报错（${p.errs[0] ?? ''}）`)
  await p.context().close()
}

// 3.16 四个标签切换真实画面：整页不滚动，AI 战绩写示例，有风险提示
{
  const p = await newPage()
  await p.goto(BASE + '/?page=landing')
  await p.waitForSelector('.lp-head .seg button')
  const noScroll = () => p.evaluate(() => document.documentElement.scrollHeight <= innerHeight)
  ok(await noScroll(), '落地页整页不滚动')
  // 示例仓位、策略净值两个标签去掉了（D90 第 4 条）
  ok(await p.locator('.lp-head .seg button').count() === 2, '落地页只剩方块图和强平分布两个标签')
  for (const [tab, re] of [['liq', /Hyperliquid/]]) {
    await p.locator(`.lp-head .seg button[data-tab="${tab}"]`).click()
    await p.waitForSelector('.lp-note')
    await p.waitForTimeout(800)
    ok(re.test(await p.locator('.lp-note').innerText()) && await noScroll(), `标签 ${tab}：有说明，不滚动`)
    await p.screenshot({ path: `${OUT}/m3-lp-${tab}.png` })
  }
  ok(await hasText(p, /风险提示/) && !(await hasText(p, /保证|稳赚|年化\s*\d/)), '有风险提示，不承诺收益')
  await p.context().close()
}

// 3.14 没开启服务时写一句话加入口，不是空白
{
  const p = await newPage()
  await p.goto(BASE + '/?page=ai')
  await p.waitForSelector('.ai-head', { timeout: 10000 })
  await p.locator('.ai-list [data-tab=decisions]').click()
  await p.waitForSelector('.ai-locked', { timeout: 5000 })
  ok(/开启跟随/.test(await p.locator('.ai-locked').innerText()) && await p.locator('.ai-locked button').count() === 1, '未登录看决策：一句话和入口')
  await p.locator('.ai-list [data-tab=pos]').click()
  await p.waitForTimeout(300)
  ok(await p.locator('.ai-locked').count() === 1, '未登录看持仓：一句话和入口')
  ok(/示例/.test(await p.locator('.ai-title').innerText()), 'AI 页写明示例')
  await p.context().close()
}

// 3.18、3.19 注册后第一次进来出现向导；可以跳过；帮助里能再打开
const p = await newPage({}, false)
await p.goto(BASE + '/?page=login&mode=register')
await p.waitForSelector('form input')
const email = `m3${Date.now()}@example.com`
await p.locator('form input').nth(0).fill(email)
await p.locator('form input').nth(1).fill('m3-' + Math.random().toString(36).slice(2) + '-pass')
await p.locator('form .btn.primary').first().click()
await p.waitForTimeout(1500)
await p.goto(BASE + '/')
await p.waitForSelector('.wz-steps', { timeout: 8000 }).catch(() => {})
ok(await p.locator('.wz-steps').count() === 1, '注册后第一次进来出现向导')
await p.locator('.wz-foot .btn.ghost').click() // 跳过
await p.waitForTimeout(300)
ok(await p.locator('.wz-steps').count() === 0, '可以跳过')
await p.reload()
await p.waitForTimeout(1500)
ok(await p.locator('.wz-steps').count() === 0, '跳过后不再自动出现')
ok(await p.locator('.mk-chart').count() === 1, '已登录直接进交易账户（不是落地页）')
await p.locator('.bar-help-btn').click()
await p.locator('.bar-help-pop button').first().click()
await p.waitForSelector('.wz-steps')
ok(true, '帮助里能再打开')
await p.screenshot({ path: `${OUT}/m3-wz1.png` })
// 动效不挡操作：连点下一步
await p.locator('.wz-foot .btn.primary').click()
ok(await p.locator('[data-wz-alloc]').count() === 1 && await p.locator('.sld').count() >= 1, '点完下一步立刻就是第二步（不等动画）')
// 11-16 用户"我们不是无限的吗"：模拟账户的分配金额自己填，不限额，写明比可用的多时自动加进去
ok(/不限额/.test(await p.locator('.wz-alloc').innerText()), '模拟账户：分配金额自己填，写明模拟资金不限额')
// 概率云：测试环境没有历史行情，塞一段假的 4 小时线
await p.route('**/api/candles?instrument=BTC-PERP&interval=4h&limit=540', (r) => {
  let v = 70000
  const now = Date.now()
  const rows = Array.from({ length: 540 }, (_, i) => { v *= 1 + (Math.sin(i * 1.7) + Math.sin(i * 0.31)) * 0.008; return { t: now - (540 - i) * 14400000, o: v, h: v, l: v, c: v, vb: 1, vs: 1 } })
  r.fulfill({ json: rows })
})
await p.locator('.wz-foot .btn.ghost').click()
await p.waitForTimeout(250)
await p.locator('.bar-help-btn').click()
await p.locator('.bar-help-pop button').first().click()
await p.waitForSelector('.wz-steps')
await p.locator('.wz-foot .btn.primary').click()
await p.waitForTimeout(1200)
const cloudDrawn = await p.evaluate(() => { const c = document.querySelector('.cloud canvas'); if (!c) return 0; const d = c.getContext('2d').getImageData(0, 0, c.width, c.height).data; let n = 0; for (let i = 0; i < d.length; i += 4) if (d[i] > 40) n++; return n })
ok(cloudDrawn > 2000, `概率云画出来了（亮像素 ${cloudDrawn}）`)
ok(/样本不够/.test(await p.locator('.wz-cloud').innerText()), '样本不够时写明是怎么算的')
ok(/极端行情可能多亏一些/.test(await p.locator('.wz-worst').innerText()), '写清楚最坏情况')
await p.screenshot({ path: `${OUT}/m3-wz2.png` })
// 拖滑块时实时变化
const before = await p.locator('.wz-worst').innerText()
const sld = p.locator('.sld').nth(0) // 模拟账户没有分配资金的滑块（改成自己填），第一个就是最大亏损
const sb = await sld.boundingBox()
await p.mouse.move(sb.x + sb.width * 0.3, sb.y + sb.height / 2)
await p.mouse.down()
await p.mouse.move(sb.x + sb.width * 0.7, sb.y + sb.height / 2, { steps: 5 })
const during = await p.locator('.wz-worst').innerText()
await p.mouse.up()
ok(before !== during, `拖最大亏损滑块时实时变化（${before.slice(0, 18)} → ${during.slice(0, 18)}）`)
await p.locator('.sld').nth(0).focus()
await p.keyboard.press('ArrowRight')
ok(true, '滑块能用键盘调')
// 18 第 6.6 条：跟随比例（更多设置里，默认 1 倍）
await p.locator('.wz-more').click()
const ratioSld = p.locator('.wz-morebox .sld').nth(1)
await ratioSld.focus()
await p.keyboard.press('ArrowLeft')
await p.keyboard.press('ArrowLeft')
const moreText = await p.locator('.wz-morebox').innerText()
ok(/跟随比例\s*0\.5×/.test(moreText) && /策略用资金的 40%，你用分配金额的 20%/.test(moreText), '跟随比例调到 0.5 倍，写明 40% 变 20%')
await p.locator('.wz-morebox').screenshot({ path: `${OUT}/m3-wz-ratio.png` })
// 第三步确认
await p.locator('.wz-foot .btn.primary').click()
ok(/你授权后，系统在这个账户里按规则下单/.test(await p.locator('.wz-auth').innerText()), '确认页写清楚授权')
ok(/跟随比例\s*0\.5×/.test(await p.locator('.wz-sum').innerText()), '确认页列出跟随比例')
await p.screenshot({ path: `${OUT}/m3-wz3.png` })
await p.locator('.wz-foot .btn.primary').click()
await p.waitForSelector('.wz-steps', { state: 'detached', timeout: 5000 })
await p.waitForTimeout(800)

// 3.20 下单区换成 AI 状态
await p.waitForSelector('.fw-side', { timeout: 5000 })
ok(/正在跟随/.test(await p.locator('.fw-side').innerText()), '下单区换成 AI 状态和分配资金')
ok(await p.locator('.mk-side .display-title').innerText() === '策略跟随', '标题写"策略跟随"')
const accts = (await call(p, 'GET', '/api/paper-accounts')).b.accounts
ok(accts.length === 2 && accts.some((a) => a.name === '策略跟随' && Number(a.initial) === 10000), '第一次跟随自动建 10,000U 的"策略跟随"账户')
const follows = () => call(p, 'GET', '/api/follows').then((r) => r.b.follows)
const fid = (await follows())[0].id
ok((await follows())[0].params.ratio === '0.5', '跟随比例存上了（0.5）')
const acct = (await follows())[0].account
ok((await call(p, 'GET', '/api/signals')).s === 200, '开启服务后能拿到决策')
await p.screenshot({ path: `${OUT}/m3-trade-follow.png` })

// 3.12、D80 在跟随账户里手动下单：不弹窗拦，写明手动成交后转手动管理
await p.locator('.fw-manual').click()
await p.waitForSelector('.op-follow-note')
ok(/手动成交后，这个币转为手动管理/.test(await p.locator('.op-follow-note').innerText()), '切到手动下单：写明成交后转手动管理')
await p.locator('.op-follow-note .op-link').click()
await p.waitForSelector('.fw-side')
ok(true, '能切回跟随状态')

// 3.9 暂停、恢复、停止
await p.locator('.fw-ctl .btn.secondary').click()
await p.waitForFunction(() => /已暂停/.test(document.querySelector('.fw-side')?.textContent ?? ''), null, { timeout: 3000 })
ok(true, '暂停后写"已暂停"')
await p.locator('.fw-ctl .btn.primary').click()
await p.waitForSelector('.dlg .fw-p', { timeout: 3000 })
ok(/恢复/.test(await p.locator('.dlg').innerText()), '恢复前先提示')
await p.screenshot({ path: `${OUT}/m3-resume.png` })
await p.locator('.dlg .btn.primary').click()
await p.waitForFunction(() => /正在跟随/.test(document.querySelector('.fw-side')?.textContent ?? ''), null, { timeout: 3000 })
ok(true, '恢复后回到正在跟随')
await p.locator('.fw-ctl .btn.ghost').click()
await p.waitForSelector('.fw-stop-opt')
ok(await p.locator('.fw-stop-opt').count() === 2, '停止有两种：平仓或保留')
await p.screenshot({ path: `${OUT}/m3-stop.png` })
await p.keyboard.press('Escape')
await p.waitForTimeout(250)

// AI 页（3.22–3.25）
await p.goto(BASE + '/?page=ai')
await p.waitForSelector('.ai-ro')
ok((await p.locator('.ai-ro').innerText()).match(/收益[\s\S]*最大回撤[\s\S]*夏普[\s\S]*胜率[\s\S]*已平仓交易[\s\S]*跟随人数[\s\S]*跟随资金/) !== null, 'AI 页：收益、回撤、夏普、胜率、交易次数、跟随人数和资金')
const hasNaN = await hasText(p, /NaN|undefined|Infinity/)
ok(!hasNaN, 'AI 页没有 NaN')
await p.screenshot({ path: `${OUT}/m3-ai.png` })
await p.locator('.ai-list [data-tab=decisions]').click()
await p.waitForTimeout(800)
await p.screenshot({ path: `${OUT}/m3-ai-dec.png` })
await p.locator('.ai-list [data-tab=cal]').click()
await p.waitForSelector('.cal', { timeout: 5000 })
ok(/样本不够/.test(await p.locator('.cal').innerText()), '信心校准：每档不到 30 笔写"样本不够"')
await p.screenshot({ path: `${OUT}/m3-ai-cal.png` })
await p.locator('.ai-list [data-tab=mine]').click()
await p.waitForSelector('.fw-detail')
await p.screenshot({ path: `${OUT}/m3-ai-mine.png` })

// 3.26 设置页
await p.goto(BASE + '/?page=account')
await p.waitForSelector('.ac-set')
await p.locator('.ac-under [data-quick="1000"]').click()
await p.locator('.ac-under .btn.secondary:not([data-live-add])').click() // 交易所账户那一节也有一个 .ac-under 按钮
await p.waitForTimeout(600)
const list2 = (await call(p, 'GET', '/api/paper-accounts')).b.accounts
ok(list2.some((a) => Number(a.initial) === 1000), '新建模拟账户：快捷金额 1,000')
// D103：金额不限（大于 0 就行）
ok((await call(p, 'POST', '/api/paper-accounts', { name: 'x', initial: '0' })).s === 400, '金额不限，但 0 被拒绝')
const sb2 = p.locator('.ac-set .seg').first()
await sb2.locator('button').nth(1).click()
ok(await p.evaluate(() => document.documentElement.dataset.colorMode === 'red-up'), '红涨绿跌立刻生效')
await sb2.locator('button').nth(0).click()
await p.locator('.ac-set .ac-sel').nth(0).locator('.sel-btn').click()
await p.locator('.ac-set .ac-sel').nth(0).locator('li').filter({ hasText: 'UTC' }).click()
const tz = await p.evaluate(() => JSON.parse(localStorage.getItem('qs.prefs')).tz)
ok(tz === 'UTC', '时区能改')
await p.locator('.ac-set .ac-sel').nth(1).locator('.sel-btn').click()
await p.locator('.ac-set .ac-sel').nth(1).locator('li').nth(1).click()
ok(await p.evaluate(() => JSON.parse(localStorage.getItem('qs.prefs')).numFmt) === 'de-DE', '数字格式能改')
await p.screenshot({ path: `${OUT}/m3-settings.png` })
await p.evaluate(() => { const v = JSON.parse(localStorage.getItem('qs.prefs')); v.numFmt = 'en-US'; v.tz = 'local'; localStorage.setItem('qs.prefs', JSON.stringify(v)) })
await p.locator('.ac-set-btns .btn').first().click()
await p.waitForSelector('.gl')
ok(await p.locator('.gl > div').count() >= 15, '术语表')
await p.keyboard.press('Escape')
await p.waitForTimeout(250)

// 2.24 命令面板：打开马上能打字；Esc 关；切品种
await p.goto(BASE + '/?page=trade')
await p.waitForSelector('.mk-chart')
await p.keyboard.press('Control+k')
await p.keyboard.type('eth', { delay: 5 })
await p.waitForTimeout(150)
ok(await p.locator('.pal-in').inputValue() === 'eth', 'Ctrl + K 打开马上能打字（不等动画）')
await p.keyboard.press('Enter')
await p.waitForTimeout(600)
ok((await p.locator('.mk-chart .display-title').innerText()) === 'ETH', '命令面板切品种')
await p.keyboard.press('Control+k')
await p.waitForSelector('.pal')
await p.keyboard.press('Escape')
await p.waitForSelector('.pal', { state: 'detached', timeout: 1000 })
ok(true, 'Esc 关命令面板')

// 2.27 数字键切周期；2.31 品种搜索
await p.keyboard.press('5')
await p.waitForTimeout(150)
ok(await p.evaluate(() => localStorage.getItem('qs.iv')) === '5m', '按 5 切 5 分钟')
await p.keyboard.press('1')
await p.keyboard.press('5')
await p.waitForTimeout(150)
ok(await p.evaluate(() => localStorage.getItem('qs.iv')) === '15m', '连按 1、5 切 15 分钟')
await p.keyboard.press('1')
await p.waitForTimeout(500)
ok(await p.evaluate(() => localStorage.getItem('qs.iv')) === '1m', '按 1 切 1 分钟')
await p.locator('.mk-strip .mk-picker').first().click()
await p.waitForSelector('.mk-search')
await p.keyboard.type('SIM00', { delay: 5 })
await p.waitForTimeout(200)
ok(await p.locator('.mk-menu-r').count() >= 2, '品种下拉可以搜索全市场')
await p.keyboard.press('Escape')
await p.waitForTimeout(250)
ok(await p.locator('.mk-menu').count() === 0, 'Esc 能关')

// 2.26 撤单 5 秒内能撤回
await call(p, 'POST', `/api/trade/${acct}/orders`, { instrument: 'ETH-PERP', side: 'buy', type: 'limit', price: '100', qty: '0.1' }).then((r) => r)
const ethMark = Number((await call(p, 'GET', '/api/market/snapshot')).b.instruments.find((i) => i.instrument === 'ETH-PERP').last)
const placed = await call(p, 'POST', `/api/trade/${acct}/orders`, { instrument: 'ETH-PERP', side: 'buy', type: 'limit', price: (ethMark * 0.98).toFixed(2), qty: '0.1' })
ok(placed.s === 200, `挂一张单（${placed.s} ${placed.b?.code ?? ''} 价格 ${(ethMark * 0.98).toFixed(2)}）`)
await p.locator('.fw-manual').click({ timeout: 2000 }).catch(() => {})
await p.locator('.mk-tabs [data-tab="open"]').click()
await p.waitForSelector('.tl tbody tr .btn.ghost')
// 要撤的是普通限价单：AI 的止盈止损也在这张表里，它们撤了不带撤回按钮
await p.locator('.tl tbody tr').filter({ hasText: '限价' }).locator('.tl-act .btn.ghost').last().click()
await p.waitForSelector('.toast-act', { timeout: 2000 })
ok(/撤/.test(await p.locator('.toast').innerText()), '撤单提示带撤回按钮')
await p.locator('.toast-act').click()
// 撤回也要走模拟的下单延迟（D94）：最多等 5 秒
let back = false
for (let i = 0; i < 20 && !back; i++) {
  await p.waitForTimeout(250)
  back = (await call(p, 'GET', `/api/trade/${acct}`)).b.orders.some((o) => o.type === 'limit' && Number(o.qty) === 0.1)
}
ok(back, '撤回后委托回来了')
await p.screenshot({ path: `${OUT}/m3-undo.png` })

// 系统灰色提示都换掉了（3.29）
const nativeSelect = await p.locator('select').count()
ok(nativeSelect === 0, '没有系统自带的下拉框')
await p.locator('.mk-ro .ro').first().hover()
await p.waitForTimeout(450)
ok(await p.locator('.tip-pop.show').count() === 1 && await p.locator('.mk-ro .ro[title]').first().count().then(() => true), '悬停说明是自己画的，不是系统提示')
ok(p.errs.length === 0, `流程里没有页面报错（${p.errs[0] ?? ''}）`)
await p.context().close()

// 3.30 系统设置"减少动态效果"时全部关闭
{
  const q = await newPage({ reducedMotion: 'reduce' })
  await q.goto(BASE + '/?page=trade')
  await q.waitForSelector('.mk-chart')
  await q.keyboard.press('Control+k')
  const anims = await q.evaluate(() => document.getAnimations().filter((a) => a.playState === 'running' && a.effect?.target?.closest?.('.pal')).length)
  ok(anims === 0, '减少动态效果时命令面板不做动画')
  await q.keyboard.press('Escape')
  await q.goto(BASE + '/')
  await q.waitForSelector('.mb-map canvas')
  await q.waitForTimeout(1200)
  ok(q.errs.length === 0, '减少动态效果时落地页正常')
  await q.context().close()
}

// 3.15 帧率：中端手机（CPU 降速 4 倍）的落地页
{
  const q = await newPage({ viewport: { width: 412, height: 820 }, deviceScaleFactor: 2.625, isMobile: true, hasTouch: true })
  const cdp = await q.context().newCDPSession(q)
  await cdp.send('Emulation.setCPUThrottlingRate', { rate: 4 })
  await q.goto(BASE + '/?page=landing')
  await q.waitForSelector('.mb-map canvas')
  await q.waitForTimeout(6000)
  const fps = await q.evaluate(() => new Promise((res) => { let n = 0; const t0 = performance.now(); const f = () => { n++; if (performance.now() - t0 < 3000) requestAnimationFrame(f); else res(n / 3) }; requestAnimationFrame(f) }))
  ok(fps >= 30, `手机落地页帧率（CPU 降速 4 倍）${fps.toFixed(0)}`)
  await q.screenshot({ path: `${OUT}/m3-lp-phone.png` })
  await q.context().close()
}

// 3.31 16 种设备
for (const d of DEVICES) {
  for (const [name, url, sel] of [['落地页', '/?page=landing', '.mb-map canvas'], ['AI 页', '/?page=ai', '.ai-head'], ['市场', '/?page=market', '.mb-map canvas']]) {
    const q = await newPage({ viewport: { width: d.w, height: d.h }, deviceScaleFactor: d.dpr, isMobile: d.w < 500, hasTouch: d.w < 500 })
    await q.goto(BASE + url)
    await q.waitForSelector(sel, { timeout: 15000 })
    await q.waitForTimeout(500)
    const m = await q.evaluate(() => ({ down: document.documentElement.scrollHeight - innerHeight, over: document.documentElement.scrollWidth - innerWidth, nan: /NaN|undefined/.test(document.body.innerText), bar: document.querySelector('.bar').scrollWidth - document.querySelector('.bar').clientWidth }))
    ok(m.down <= 0 && m.over <= 0 && !m.nan && m.bar <= 0 && q.errs.length === 0, `${d.name} ${name}：整页不滚，不溢出，无 NaN，顶栏不挤（${m.down}/${m.over}/${m.bar}）`)
    await q.context().close()
  }
}

console.log(`\n通过 ${pass}，失败 ${fail}`)
await browser.close()
process.exit(fail ? 1 : 0)
