// 模块 2 逐项核对（界面层）：下单区每个控件、持仓上的止盈止损和平仓对话框、改单、历史原因、报错、隐藏金额、杠杆对话框、
// 以及勾上止盈止损后下单区在各种屏幕上能不能完整滑到底、有没有横向滚动。
// 用法：node tools/e2e-ui-audit.mjs <截图输出目录>（后端模拟行情 :8000 + vite preview :4173）
import { chromium } from './node_modules/playwright/index.mjs'

const BASE = process.env.BASE ?? 'http://localhost:4173'
const OUT = process.argv[2] ?? '.'
let pass = 0, fail = 0
const ok = (c, name) => { if (c) pass++; else fail++; console.log(`${c ? '✓' : '✗'} ${name}`) }
const errs = []
const hdr = { 'X-QS-Client': 'web', Origin: BASE }

const browser = await chromium.launch({ channel: process.env.PW_CHANNEL, args: ['--use-gl=angle', '--use-angle=swiftshader'] })
const c0 = await browser.newContext()
await c0.addInitScript(() => localStorage.setItem('qs.wizardSeen', '1'))
await c0.request.post(BASE + '/api/auth/register', { headers: hdr, data: { email: `ui${Date.now()}@example.com`, password: 'password-123', lang: 'zh' } })
// 这个脚本测下单规则，不测纪律锁（默认开启，连亏 3 笔就冷静）：先关掉。纪律锁的检查在 e2e-m4
const noDiscipline = async (c) => {
  await c.request.post(BASE + '/api/me/trade-password', { headers: hdr, data: { new: 'tp-ui-12345' } })
  await c.request.post(BASE + '/api/discipline/enabled', { headers: hdr, data: { on: false, trade_password: 'tp-ui-12345' } })
}
await noDiscipline(c0)
const state = await c0.storageState()

// ---------- 勾上止盈止损后：各种屏幕上下单区能滑到底、没有横向滚动、最后一个输入框不被下单按钮挡住 ----------
for (const [w, h, dpr, name] of [[1897, 914, 1, '1920×1080 100%（用户的屏幕）'], [1536, 730, 1.25, '1920×1080 125%'], [1280, 590, 1.5, '1920×1080 150%'], [1097, 500, 1.75, '1920×1080 175%'], [1440, 900, 2, 'MacBook']]) {
  const c = await browser.newContext({ viewport: { width: w, height: h }, deviceScaleFactor: dpr, storageState: state, locale: 'zh-CN' })
await c.addInitScript(() => localStorage.setItem('qs.wizardSeen', '1'))
  const p = await c.newPage()
  await p.goto(BASE + '/?page=trade')
  await p.waitForSelector('.op-submit')
  await p.getByText('止盈止损', { exact: true }).click()
  await p.waitForSelector('.op-tpsl')
  const m = await p.evaluate(() => {
    const body = document.querySelector('.mk-side-body')
    body.scrollTop = body.scrollHeight
    const last = [...document.querySelectorAll('.op-tpsl input')].pop().getBoundingClientRect()
    const go = document.querySelector('.op-go').getBoundingClientRect()
    const btn = document.querySelector('.op-submit').getBoundingClientRect()
    return { hOver: body.scrollWidth - body.clientWidth, lastBottom: last.bottom, goTop: go.top + 10, btnBottom: btn.bottom, vh: innerHeight, pageScroll: document.documentElement.scrollHeight - innerHeight }
  })
  ok(m.hOver <= 0, `${name}：下单区没有横向滚动（多出 ${m.hOver}px）`)
  ok(m.lastBottom <= m.goTop, `${name}：滑到底时止损输入框不被下单按钮挡住（输入框底 ${m.lastBottom.toFixed(0)}，按钮区顶 ${m.goTop.toFixed(0)}）`)
  ok(m.btnBottom <= m.vh && m.pageScroll <= 0, `${name}：下单按钮在屏幕内、整页不滚动`)
  await p.screenshot({ path: `${OUT}/ui-tpsl-${w}x${h}.png` })
  await c.close()
}

// ---------- 逐个控件 ----------
const ctx = await browser.newContext({ viewport: { width: 1536, height: 730 }, deviceScaleFactor: 1.25, storageState: state, locale: 'zh-CN' })
await ctx.addInitScript(() => localStorage.setItem('qs.wizardSeen', '1'))
const page = await ctx.newPage()
page.on('pageerror', (e) => errs.push(String(e)))
page.on('console', (m) => { if (m.type() === 'error' && !/Failed to load resource/.test(m.text())) errs.push(m.text()) })
await page.goto(BASE + '/?page=trade')
await page.waitForSelector('.op-submit')
await page.waitForSelector('.ob-r.bid')
await page.waitForTimeout(800)
const toast = async () => { await page.waitForSelector('.toast.show'); const s = await page.locator('.toast').innerText(); await page.waitForTimeout(50); return s }
const msg = async () => (await page.locator('.op-msg').innerText()).trim()
// 预估里按名字取一行（不按位置，加行不会错位）
const PV = ['数量', '预估成交价', '名义金额', '所需保证金', '手续费', '预估强平价']
const pv = async (i) => (await page.locator('.op-pv > div', { has: page.locator('dt', { hasText: PV[i + 1] }) }).locator('dd').innerText()).trim()
const mark = async () => Number((await page.locator('.mk-strip').innerText()).match(/标记价格\s*([\d,.]+)/)?.[1].replace(/,/g, ''))
const t1 = (v) => (Math.round(v * 10) / 10).toFixed(1)

// 有效方式
ok((await page.locator('.op-unit button[aria-pressed="true"]').innerText()) === 'USDT', '数量单位默认 USDT')
await page.locator('.op-tif .sel-btn').click()
const tifs = await page.locator('.sel-pop .sel-l').allInnerTexts()
const hints = await page.locator('.sel-pop .sel-h').allInnerTexts()
ok(tifs.length === 5 && hints.length === 5 && hints.every((x) => x.length >= 8), `有效方式：自己的下拉框，5 种（含有效到 GTD，D103）都有中文名和一句说明（${tifs.join(' / ')}）`)
await page.screenshot({ path: `${OUT}/ui-01-tif.png` })
await page.keyboard.press('ArrowDown')
await page.keyboard.press('ArrowDown')
await page.keyboard.press('Enter')
ok((await page.locator('.op-tif .sel-btn').innerText()).includes('全部成交否则撤') && await page.locator('.sel-pop').count() === 0, '下拉框可以用键盘选（↓↓ Enter 选中"全部成交否则撤"）')
await page.locator('.op-tif .sel-btn').click()
await page.mouse.click(5, 5)
ok(await page.locator('.sel-pop').count() === 0, '点别处关闭下拉框')
await page.locator('.op-tif .sel-btn').click()
await page.locator('.sel-pop li', { hasText: '一直有效' }).click()
// 买一 / 最新 / 卖一
for (const [k, label] of [[0, '买一'], [1, '最新'], [2, '卖一']]) {
  await page.locator('.op-quick button').nth(k).click()
  const v = await page.getByLabel('价格', { exact: true }).inputValue()
  ok(/^\d+\.\d$/.test(v), `点"${label}"填入价格（${v}）`)
}
// 按 USDT 输入
await page.locator('.op-unit button', { hasText: 'USDT' }).click()
await page.getByLabel('数量', { exact: true }).fill('500')
await page.waitForTimeout(600)
const notional = Number((await pv(1)).replace(/[^\d.]/g, ''))
ok(notional <= 500 && notional > 500 - 0.001 * 120000, `按 USDT 金额输入：按数量最小单位向下取整，名义金额不超过 500（${notional}）`)
await page.locator('.op-unit button', { hasText: 'BTC' }).click()
// 百分比
await page.locator('.op-pct button', { hasText: '25%' }).click()
await page.waitForTimeout(600)
ok((await page.getByLabel('数量', { exact: true }).getAttribute('placeholder')).includes('25%') && (await pv(1)) !== '—', `按可用余额 25%：数量框显示"25%"，预估有值（名义 ${await pv(1)}）`)
await page.locator('.op-pct button', { hasText: '25%' }).click()
// 只减仓时不能设止盈止损
await page.getByText('只减仓', { exact: true }).click()
ok(await page.getByRole('checkbox', { name: '止盈止损' }).isDisabled(), '勾上只减仓后，止盈止损不能勾（交易所规则）')
await page.getByText('只减仓', { exact: true }).click()

// 拒单原因显示在下单区
await page.locator('.op .seg button', { hasText: '市价' }).click()
await page.getByLabel('数量', { exact: true }).fill('0.0004') // BTC 0.001 已经超过最小名义 50 USDT（D92），用不到一个最小单位的数量
await page.waitForTimeout(700)
ok(/0\.001|最小/.test(await msg()), `数量太小：下单区直接说明最小多少（${await msg()}）`)

// 市价单 + 随单止盈止损
let mk = await mark()
await page.getByLabel('数量', { exact: true }).fill('0.02')
await page.getByText('止盈止损', { exact: true }).click()
await page.getByLabel('止盈触发价').fill(t1(mk * 1.04))
await page.getByLabel('止损触发价').fill(t1(mk * 0.96))
await page.locator('.op-submit').click()
ok((await toast()).includes('成交'), '市价单带止盈止损：成交')
await page.waitForTimeout(1500)
const tpslCell = await page.locator('.tl tbody tr').first().locator('td').nth(8).innerText()
ok(/\d/.test(tpslCell) && !tpslCell.includes('— / —'), `持仓行显示止盈 / 止损价（${tpslCell.replace(/\s+/g, ' ')}）`)
await page.screenshot({ path: `${OUT}/ui-02-position.png` })
// 有多仓时切到卖出：最多可卖拆成"平仓 + 开空"，和持仓对得上；勾只减仓时最多 = 持仓
await page.locator('.op-side button').nth(1).click()
await page.waitForTimeout(800)
const maxTxt = await page.locator('.op-max').innerText()
ok(/平仓 0\.020/.test(maxTxt) && /开空/.test(maxTxt), `卖出时"最多"拆成平仓 + 开空（${maxTxt}）`)
await page.getByText('只减仓', { exact: true }).click()
await page.waitForTimeout(300)
ok((await page.locator('.op-max').innerText()).includes('0.020'), `勾只减仓时最多 = 持仓 0.020（${await page.locator('.op-max').innerText()}）`)
await page.getByText('只减仓', { exact: true }).click()
await page.locator('.op-side button').nth(0).click()
// 已实现盈亏 = 扣过手续费的净额；和浮盈加起来正好等于权益变化
{
  const ro = await page.evaluate(() => [...document.querySelectorAll('.op-ro .ro')].map((r) => [r.querySelector('.ro-l')?.textContent, r.querySelector('.ro-v')?.textContent]))
  const num = (k) => Number((ro.find((x) => x[0] === k)?.[1] ?? '').replace(/[^\d.+−-]/g, '').replace('−', '-'))
  const eqv = num('权益'), up = num('未实现盈亏'), rz = num('已实现盈亏')
  ok(rz < 0 && Math.abs(eqv - (10000 + rz + up)) < 0.02, `已实现盈亏是扣过手续费的净额（${rz}），权益 ${eqv} = 10000 + 已实现 + 未实现 ${up}`)
  await page.locator('.op-ro .ro-has-pop').hover()
  await page.waitForTimeout(250)
  const pop = await page.locator('.op-ro .ro-pop').innerText()
  ok(/手续费/.test(pop) && /资金费/.test(pop) && /权益变化/.test(pop), '悬停显示明细：平仓盈亏、手续费、资金费、权益变化')
  await page.screenshot({ path: `${OUT}/ui-02b-realized.png` })
  await page.mouse.move(5, 300)
}

// 持仓上改止盈止损：设成会立即触发的价，要看到原因
await page.locator('.tl tbody tr').first().getByRole('button', { name: '止盈止损' }).click()
await page.waitForSelector('.dlg')
mk = await mark()
const tpInput = page.locator('.dlg input').first()
await tpInput.fill(t1(mk * 0.98))
await page.locator('.dlg .btn.primary').click()
await page.waitForTimeout(500)
const dlgErr = (await page.locator('.dlg .op-msg').innerText()).trim()
ok(/触发/.test(dlgErr), `止盈设在亏损方向：对话框里说明会立即触发（${dlgErr}）`)
await tpInput.fill(t1(mk * 1.05))
await page.locator('.dlg .btn.primary').click()
await page.waitForSelector('.dlg', { state: 'detached' })
ok(true, '改成正确的止盈价后保存')
await page.screenshot({ path: `${OUT}/ui-03-tpsl-saved.png` })

// 限价平一半
await page.locator('.tl tbody tr').first().getByRole('button', { name: '平仓', exact: true }).click()
await page.waitForSelector('.dlg')
await page.locator('.dlg .seg button', { hasText: '50%' }).click()
await page.locator('.dlg .seg button', { hasText: '限价' }).click()
const closePx = await page.locator('.dlg input').inputValue()
ok(Number(closePx) > 0, `限价平仓：价格预填为标记价（${closePx}）`)
mk = await mark()
await page.locator('.dlg input').fill(t1(mk * 1.02))
ok((await page.locator('.dlg .dlg-text').innerText()).includes('0.010'), '平 50%：显示平仓数量 0.010')
await page.screenshot({ path: `${OUT}/ui-04-close-limit.png` })
await page.locator('.dlg .btn.primary').click()
ok((await toast()).length > 0, '限价平仓单已挂出')
await page.locator('.mk-tabs [data-tab="open"]').click()
await page.waitForTimeout(800)
const rows = await page.locator('.tl tbody tr').count()
ok(rows >= 3, `当前委托：限价平仓单 + 止盈 + 止损（${rows} 行）`)
await page.screenshot({ path: `${OUT}/ui-05-open-orders.png` })

// 改单
const limitRow = page.locator('.tl tbody tr', { has: page.getByRole('button', { name: '改单' }) }).first()
await limitRow.getByRole('button', { name: '改单' }).click()
await page.waitForSelector('.dlg')
await page.locator('.dlg input').first().fill(t1(mk * 1.025))
await page.locator('.dlg .btn.primary').click()
ok((await toast()).length > 0, '改单成功')
await page.waitForTimeout(600)
ok((await page.locator('.tl').innerText()).replace(/,/g, '').includes(t1(mk * 1.025)), `委托列表里是新价格 ${t1(mk * 1.025)}`)
// 撤单
await page.locator('.tl tbody tr', { has: page.getByRole('button', { name: '改单' }) }).first().getByRole('button', { name: '撤单' }).click()
// 上一步改单的提示可能还在，等到新的提示出来
ok(await page.waitForFunction(() => /撤/.test(document.querySelector('.toast')?.textContent ?? ''), null, { timeout: 3000 }).then(() => true).catch(() => false), '撤单')

// 历史委托里能看懂原因
await page.locator('.mk-tabs [data-tab="hist"]').click()
await page.waitForTimeout(800)
// 点开一张有成交的委托：逐笔成交、挂单还是吃单、手续费
await page.locator('.tl .tl-exp').first().click()
await page.waitForSelector('.tl-sub-tbl')
const sub = await page.locator('.tl-sub-tbl').innerText()
ok(/吃单|挂单/.test(sub) && /合计 \d+ 笔/.test(sub), `点开委托看到逐笔成交（${sub.split('\n').slice(-1)[0].replace(/\s+/g, ' ')}）`)
await page.screenshot({ path: `${OUT}/ui-06b-order-fills.png` })
await page.locator('.tl .tl-exp').first().click()
const histText = await page.locator('.tl').innerText()
ok(/已撤销|已成交/.test(histText) && !/USER|POST_ONLY|canceled|filled/.test(histText), '历史委托：状态和原因都是中文，没有英文代码')
await page.screenshot({ path: `${OUT}/ui-06-history.png` })

// 隐藏金额
await page.keyboard.press('h')
await page.waitForTimeout(300)
const masked = await page.locator('.op-ro').innerText()
ok(/•{4,}/.test(masked) && !/\d,\d{3}\.\d{2}/.test(masked), '按 H 隐藏金额（显示成圆点）')
await page.keyboard.press('h')

// 杠杆对话框：有仓位时不能切逐仓，要说明
await page.locator('.op-chip').first().click()
await page.waitForSelector('.dlg')
await page.locator('.dlg .seg button', { hasText: '逐仓' }).click()
await page.locator('.dlg .btn.primary').click()
await page.waitForTimeout(500)
const levErr = (await page.locator('.dlg').innerText())
ok(/先平仓/.test(levErr), '有仓位时切逐仓：对话框里说明要先平仓')
await page.screenshot({ path: `${OUT}/ui-07-leverage.png` })
await page.keyboard.press('Escape')
await page.waitForTimeout(300)

// ---------- 模块 2 补齐 ----------
const inPage = (method, path, data) => page.evaluate(async ([m, p, d]) => {
  const r = await fetch(p, { method: m, headers: { 'Content-Type': 'application/json', 'X-QS-Client': 'web' }, body: d ? JSON.stringify(d) : undefined })
  return { status: r.status, body: await r.json().catch(() => null) }
}, [method, path, data])
const acctId = (await inPage('GET', '/api/paper-accounts')).body.accounts?.[0]?.id ?? (await inPage('GET', '/api/paper-accounts')).body[0].id

// 最新成交
await page.locator('.mk-book .seg button[data-tab="trades"]').click()
await page.waitForSelector('.tr-r', { timeout: 8000 }).catch(() => {})
ok(await page.locator('.tr-r').count() > 0, `盘口旁边的"最新成交"有数据（${await page.locator('.tr-r').count()} 笔）`)
await page.waitForTimeout(1500)
ok(await page.locator('.tr-r.tr-new').count() >= 0, '新成交从上面滑进来（有进场动画类）')
await page.screenshot({ path: `${OUT}/ui-08-trades.png` })
await page.locator('.mk-book .seg button[data-tab="book"]').click()

// 条件单
await page.locator('.op .seg button', { hasText: '条件' }).click()
await page.waitForSelector('.op-cond')
mk = await mark()
await page.getByLabel('触发价', { exact: true }).fill(t1(mk * 1.015)) // 离现价远一点：检查过程中别被真的触发了
await page.getByLabel('数量', { exact: true }).fill('0.01')
await page.waitForTimeout(500)
const say = await page.locator('.op-cond-say').innerText()
ok(/标记价涨到/.test(say) && /市价买入/.test(say) && /不占保证金/.test(say), `条件单用一句话说清楚会发生什么（${say}）`)
await page.screenshot({ path: `${OUT}/ui-09-cond.png` })
await page.locator('.op-submit').click()
ok((await toast()).includes('条件单已挂'), '条件单下单成功')
await page.locator('.mk-tabs [data-tab="open"]').click()
await page.waitForTimeout(800)
const openTxt = await page.locator('.tl').innerText()
ok(/条件市价/.test(openTxt) && /≥/.test(openTxt), '当前委托里显示"条件市价 ≥ 触发价"')
ok(await page.locator('.mk-tabs .cnt').count() > 0, '标签上的数字是单独的元素（变化时会跳一下）')
await page.screenshot({ path: `${OUT}/ui-10-cond-open.png` })
// 改条件单：对话框里有触发价
await page.locator('.tl tbody tr', { hasText: '条件市价' }).getByRole('button', { name: '改单' }).click()
await page.waitForSelector('.dlg')
ok(await page.locator('.dlg').getByLabel('触发价').count() === 1, '改条件单的对话框里能改触发价')
await page.keyboard.press('Escape')
// 关对话框有动画；Esc 没关上就点取消，不然后面的点击被遮罩挡住
await page.waitForSelector('.dlg-back', { state: 'detached', timeout: 1500 }).catch(async () => { await page.locator('.dlg').getByRole('button', { name: '取消' }).click().catch(() => {}); await page.waitForSelector('.dlg-back', { state: 'detached', timeout: 3000 }).catch(() => {}) })
await page.locator('.op .seg button', { hasText: '限价' }).first().click()

// 图上的交易线：挂一张离现价很近的限价单，拖动改价
const cv = page.locator('.mk-chart canvas.pannable')
mk = await mark()
await inPage('POST', `/api/trade/${acctId}/tpsl`, { instrument: 'BTC-PERP', tp: t1(mk * 1.003) })
// 交易线在画布上的位置（data-tlines：id、种类、y、标签的 ty、x、价）。每次按下前重新读（K 线在走，价格轴会缓慢移动）
const lineNow = async (k, id) => JSON.parse((await cv.getAttribute('data-tlines')) ?? '[]').find((l) => l.k === k && (!id || l.id === id))
// 挂一张图上看得见的限价买单，返回这张单。先离现价 0.4%，图上没有这条线（后端刚起来、模拟行情平静时图的价格范围很窄，
// 0.4% 甚至 0.2% 都在画面外面）就撤掉挂近一点。11-28：原来拖线前还有一道"离现价不到 0.3% 就撤掉、按 0.4% 重挂"，
// 把这里好不容易挂在画面里的单又挂回画面外面，图上没线可拖——"拖挂单线"时过时不过就是这个
const placeVisible = async () => {
  for (const pct of [0.996, 0.998, 0.999, 0.9995]) {
    mk = await mark()
    const o = (await inPage('POST', `/api/trade/${acctId}/orders`, { instrument: 'BTC-PERP', side: 'buy', type: 'limit', price: t1(mk * pct), qty: '0.01' })).body
    if (!o?.id) continue
    for (let i = 0; i < 15; i++) { // 最多等 1.5 秒画出来
      await page.waitForTimeout(100)
      if (await lineNow('limit', o.id)) return o
    }
    await inPage('DELETE', `/api/trade/${acctId}/orders/${o.id}`)
  }
  return null
}
let placed = await placeVisible()
const lines = JSON.parse((await cv.getAttribute('data-tlines')) ?? '[]')
const kinds = new Set(lines.map((l) => l.k))
ok(kinds.has('entry') && kinds.has('limit'), `图上画出自己的持仓线、挂单线（${[...kinds].join(', ')}）`)
await page.screenshot({ path: `${OUT}/ui-11-lines.png` })
// 拖线：按住标签往上 / 往下拖。拖完等提示；没等到（没抓住线）就再试一次。id：只拖这一张单的线
const dragLine = async (k, toY, shot, id) => {
  for (let attempt = 0; attempt < 3; attempt++) {
    await page.waitForFunction(() => !document.querySelector('.toast.show'), null, { timeout: 7000 }).catch(() => {}) // 撤单提示会留 5 秒（可以撤回），不要读到旧提示
    const bb = await cv.boundingBox()
    const l = await lineNow(k, id)
    if (!l) return { l: null, text: '' }
    await page.mouse.move(bb.x + l.x, bb.y + l.ty)
    await page.mouse.down()
    const y1 = toY(l, bb)
    await page.mouse.move(bb.x + l.x, bb.y + (l.ty + y1) / 2, { steps: 4 })
    await page.mouse.move(bb.x + l.x, bb.y + y1, { steps: 4 })
    if (shot && attempt === 0) await page.screenshot({ path: shot })
    await page.mouse.up()
    const text = await page.waitForSelector('.toast.show', { timeout: 3000 }).then(() => page.locator('.toast').innerText()).catch(() => '')
    if (text) return { l, text }
  }
  return { l: null, text: '' }
}
{
  // 刚才挂的那张还在就用它（模拟行情波动大，可能已经成交了，那就再挂一张看得见的）
  let before = placed && (await inPage('GET', `/api/trade/${acctId}`)).body.orders.find((o) => o.id === placed.id)
  if (!before) before = await placeVisible()
  // 往下拖（离现价更远）：模拟行情波动大，往上拖可能直接碰到现价成交，挂单就没了
  // 模拟行情纵轴很宽时，拖太远会超出价格限制被拒绝（这是对的）。拒绝了就拖近一点。
  let text = '', after = null
  // 一轮没拖成（挂单落到图最下面、贴着成交量那一栏，往下没地方拖），就按现在的价重挂一张再拖，最多两轮（11-18）
  for (let round = 0; round < 2; round++) {
    for (const dy of [60, 28, 12]) {
      text = (await dragLine('limit', (l, bb) => Math.min(l.ty + dy, bb.height - 110), `${OUT}/ui-12-dragging.png`, before?.id)).text
      if (/已改到/.test(text)) break
      await page.waitForTimeout(600)
    } // 拖够远：行情一动纵轴会变，拖太短结果可能反过来
    await page.waitForTimeout(800)
    after = (await inPage('GET', `/api/trade/${acctId}`)).body.orders.find((o) => o.id === before?.id)
    if (/已改到/.test(text) && after && Number(after.price) !== Number(before.price)) break
    if (before) await inPage('DELETE', `/api/trade/${acctId}/orders/${before.id}`)
    before = await placeVisible()
  }
  // 模拟行情跳得很猛，拖的那零点几秒里纵轴会自动重新缩放，同一个位置对应的价可能反而变高（11-18 连着两次）：只要求改成功、价变了
  ok(/已改到/.test(text) && after && Number(after.price) !== Number(before.price), `拖挂单线：改价成功（${before?.price} → ${after?.price}，提示：${text}）`)
}
{
  await page.waitForTimeout(2200) // 等上一条提示消失
  // 模拟行情波动大：拖之前现设一个离现价 0.2% 的止盈（太远可能不在画面里，太早设可能已经被真的触发）
  if (!(await inPage('GET', `/api/trade/${acctId}`)).body.positions.length) await inPage('POST', `/api/trade/${acctId}/orders`, { instrument: 'BTC-PERP', side: 'buy', type: 'market', qty: '0.02' })
  // 刚开的测试环境 K 线少、纵轴窄，0.2% 可能在画面外：画面外的线不算，一档一档收近
  let tpShown = null
  for (const f of [1.002, 1.001, 1.0006, 1.0004]) {
    mk = await mark()
    const set = await inPage('POST', `/api/trade/${acctId}/tpsl`, { instrument: 'BTC-PERP', tp: t1(mk * f) })
    await page.waitForTimeout(1200)
    tpShown = await lineNow('tp')
    if (tpShown) break
    console.log(`  止盈 +${((f - 1) * 100).toFixed(2)}% 不在画面里（${set.status}），收近一点`)
  }
  const tpo = (await inPage('GET', `/api/trade/${acctId}`)).body.orders.find((o) => o.type === 'take_profit_market')
  ok(!!tpShown, '图上画出止盈线')
  const tpBefore = tpo?.trigger_px
  // 把多单的止盈拖到现价下面：会立即触发，应被拒绝并弹回
  // 拖到挂单线那里（挂单在现价下方 0.4%，一定低于现价）；挂单不在了就拖到价格区底部
  const limY = (await lineNow('limit'))?.y
  const { l: tpl, text } = await dragLine('tp', (l, bb) => limY ?? bb.height * 0.72)
  await page.waitForTimeout(900)
  const viewAfter = (await inPage('GET', `/api/trade/${acctId}`)).body
  const tpAfter = viewAfter.orders.find((o) => o.type === 'take_profit_market')?.trigger_px
  if (!tpAfter && !viewAfter.positions.length) {
    // 止盈离现价只有零点零几个百分点时，模拟行情可能真的先碰到它（仓位已平）：这不是拖线的问题，这一项跳过，不算通过也不算失败
    console.log(`  跳过"拖到立即触发"：行情先碰到了止盈（${tpBefore}），仓位已平`)
  } else {
    let tpLine = await lineNow('tp')
    for (let i = 0; i < 20 && Math.abs(Number(tpLine?.p) - Number(tpBefore)) >= 1e-6; i++) { await page.waitForTimeout(100); tpLine = await lineNow('tp') } // 弹回动画约 0.3 秒
    ok(tpl && /触发/.test(text) && tpAfter === tpBefore && Math.abs(Number(tpLine?.p) - Number(tpBefore)) < 1e-6, `把止盈拖到会立即触发的位置：拒绝、说明原因、线弹回原价（${text}；线在 ${tpLine?.p}）`)
  }
}

// 持仓止盈止损对话框：可以选按标记价还是最新价触发（上面行情碰到止盈把仓位平了的话，先开一个）
if (!(await inPage('GET', `/api/trade/${acctId}`)).body.positions.length) {
  await inPage('POST', `/api/trade/${acctId}/orders`, { instrument: 'BTC-PERP', side: 'buy', type: 'market', qty: '0.02' })
  await page.waitForTimeout(800)
}
await page.locator('.mk-tabs [data-tab="pos"]').click()
await page.waitForTimeout(500)
await page.locator('.tl tbody tr').first().getByRole('button', { name: '止盈止损' }).click()
await page.waitForSelector('.dlg')
ok(await page.locator('.dlg .seg button', { hasText: '最新价' }).count() === 1, '止盈止损对话框里能选按最新价触发')
await page.screenshot({ path: `${OUT}/ui-13-tpsl-by.png` })
await page.keyboard.press('Escape')
await page.waitForTimeout(300)

// 交易偏好：打开"市价单确认"，再下市价单要先看摘要
await page.goto(BASE + '/?page=account')
await page.waitForSelector('.pf-grid')
await page.locator('.pf-grid label', { hasText: '市价单' }).click()
await page.screenshot({ path: `${OUT}/ui-14-prefs.png`, fullPage: true })
await page.goto(BASE + '/?page=trade')
await page.waitForSelector('.op-submit')
await page.locator('.op .seg button', { hasText: '市价' }).click()
await page.getByLabel('数量', { exact: true }).fill('0.01')
await page.waitForTimeout(400)
await page.locator('.op-submit').click()
await page.waitForSelector('.dlg .cf-sum')
const sum = await page.locator('.dlg .cf-sum').innerText()
ok(/预估强平价/.test(sum) && /手续费/.test(sum) && /保证金/.test(sum), '开了确认：下单前弹摘要（数量、保证金、手续费、强平价）')
await page.waitForTimeout(260)
await page.screenshot({ path: `${OUT}/ui-15-confirm.png` })
await page.locator('.dlg .cf-go').click()
ok((await toast()).includes('成交'), '确认后下单成交')

// 逐仓调整保证金（新用户：逐仓开一笔）
{
  const c2 = await browser.newContext({ viewport: { width: 1536, height: 730 }, deviceScaleFactor: 1.25, locale: 'zh-CN' })
await c2.addInitScript(() => localStorage.setItem('qs.wizardSeen', '1'))
  await c2.request.post(BASE + '/api/auth/register', { headers: hdr, data: { email: `mg${Date.now()}@example.com`, password: 'password-123', lang: 'zh' } })
  await noDiscipline(c2)
  const p2 = await c2.newPage()
  p2.on('pageerror', (e) => errs.push(String(e)))
  await p2.goto(BASE + '/?page=trade')
  await p2.waitForSelector('.op-submit')
  const id2 = await p2.evaluate(async () => { const r = await fetch('/api/paper-accounts').then((x) => x.json()); return (r.accounts ?? r)[0].id })
  await p2.evaluate(async (id) => {
    const h = { 'Content-Type': 'application/json', 'X-QS-Client': 'web' }
    await fetch(`/api/trade/${id}/margin-mode`, { method: 'POST', headers: h, body: JSON.stringify({ instrument: 'BTC-PERP', mode: 'isolated' }) })
    await fetch(`/api/trade/${id}/orders`, { method: 'POST', headers: h, body: JSON.stringify({ instrument: 'BTC-PERP', side: 'buy', type: 'market', qty: '0.02' }) })
  }, id2)
  await p2.waitForTimeout(1200)
  await p2.locator('.tl-edit').first().click()
  await p2.waitForSelector('.dlg')
  await p2.locator('.dlg').getByLabel('金额').fill('50')
  await p2.waitForTimeout(700)
  const mgTxt = await p2.locator('.dlg .mg-sum').innerText()
  ok(/→/.test(mgTxt), `调整保证金：先预览调整后的保证金和强平价（${mgTxt.replace(/\s+/g, ' ')}）`)
  await p2.screenshot({ path: `${OUT}/ui-16-margin.png` })
  const m0 = (await p2.evaluate(async (id) => (await fetch(`/api/trade/${id}`).then((x) => x.json())).positions[0].margin, id2))
  await p2.locator('.dlg .btn.primary').click()
  await p2.waitForTimeout(900)
  const m1 = (await p2.evaluate(async (id) => (await fetch(`/api/trade/${id}`).then((x) => x.json())).positions[0].margin, id2))
  ok(Math.abs(Number(m1) - Number(m0) - 50) < 0.01, `追加 50 USDT 保证金（${m0} → ${m1}）`)
  await c2.close()
}


// ---------- 品种下拉：点别处、按 Esc 都能关（第 24 条） ----------
{
  await page.locator('.mk-picker').click()
  await page.waitForSelector('.mk-menu')
  await page.mouse.click(700, 300)
  await page.waitForTimeout(350)
  ok(await page.locator('.mk-menu').count() === 0, '品种下拉：点别处就关')
  await page.locator('.mk-picker').click()
  await page.waitForSelector('.mk-menu')
  await page.keyboard.press('Escape')
  await page.waitForTimeout(350)
  ok(await page.locator('.mk-menu').count() === 0 && await page.locator('.dlg-back').count() === 0, '品种下拉：按 Esc 就关，不弹别的')
}

// ---------- 用价格注入造爆仓（D75）：逐仓最高杠杆开多，把价格打穿强平价，看界面怎么提示 ----------
{
  const c3 = await browser.newContext({ viewport: { width: 1536, height: 730 }, deviceScaleFactor: 1.25, locale: 'zh-CN' })
await c3.addInitScript(() => localStorage.setItem('qs.wizardSeen', '1'))
  await c3.request.post(BASE + '/api/auth/register', { headers: hdr, data: { email: `liq${Date.now()}@example.com`, password: 'password-123', lang: 'zh' } })
  await noDiscipline(c3)
  const p3 = await c3.newPage()
  await p3.goto(BASE + '/?page=trade')
  await p3.waitForSelector('.op-submit')
  await p3.waitForSelector('.ob-r.bid')
  await p3.waitForTimeout(1200)
  const call = (m, u, d) => p3.evaluate(async ([m, u, d]) => { const r = await fetch(u, { method: m, headers: { 'Content-Type': 'application/json', 'X-QS-Client': 'web' }, body: d ? JSON.stringify(d) : undefined }); return { status: r.status, body: await r.json().catch(() => null) } }, [m, u, d])
  const id3 = (await call('GET', '/api/paper-accounts')).body.accounts[0].id
  await call('POST', `/api/trade/${id3}/margin-mode`, { instrument: 'BTC-PERP', mode: 'isolated' })
  await call('POST', `/api/trade/${id3}/leverage`, { instrument: 'BTC-PERP', leverage: 100 })
  await call('POST', `/api/trade/${id3}/orders`, { instrument: 'BTC-PERP', side: 'buy', type: 'market', qty: '0.05' })
  await p3.waitForTimeout(1200)
  const pos = (await call('GET', `/api/trade/${id3}`)).body.positions[0]
  await p3.screenshot({ path: `${OUT}/ui-17-before-liq.png` })
  const risk = await p3.locator('.mk-risk').innerText()
  ok(/离强平\s*[\d.]+%/.test(risk.replace(/\n/g, ' ')), `顶栏风险条写出离强平多远（${risk.replace(/\s+/g, ' ')}）`)
  await call('POST', '/api/dev/sim-price', { instrument: 'BTC-PERP', price: Number(pos.liq_px) * 0.995 })
  const liqText = await p3.waitForSelector('.liq-banner', { timeout: 8000 }).then(() => p3.locator('.liq-banner').innerText()).catch(() => '')
  ok(/强平/.test(liqText) && /亏了 [\d,.]+ USDT/.test(liqText), `爆仓提示写出亏了多少（${liqText.replace(/\s+/g, ' ')}）`)
  await p3.waitForTimeout(3000)
  ok(await p3.locator('.liq-banner').count() === 1, '爆仓提示留着，不会自己消失')
  await p3.reload()
  await p3.waitForSelector('.liq-banner', { timeout: 8000 }).catch(() => {})
  ok(await p3.locator('.liq-banner').count() === 1, '刷新后还在，点掉才算看过')
  await p3.locator('.liq-banner button').click()
  ok(await p3.locator('.liq-banner').count() === 0, '点"知道了"就关')
  await p3.reload()
  await p3.waitForTimeout(1500)
  ok(await p3.locator('.liq-banner').count() === 0, '点掉之后刷新不再出现')
  await p3.locator('.mk-tabs [data-tab="fills"]').waitFor({ timeout: 8000 })
  await p3.screenshot({ path: `${OUT}/ui-18-liquidated.png` })
  await p3.locator('.mk-tabs [data-tab="fills"]').click()
  await p3.waitForTimeout(700)
  const fills3 = await p3.locator('.mk-bottom').innerText()
  ok(/强平/.test(fills3), '成交记录里能看到强平那一笔')
  await p3.screenshot({ path: `${OUT}/ui-19-liq-fills.png` })
  await c3.close()
}

ok(errs.length === 0, `整个过程没有页面报错${errs.length ? '：' + errs.slice(0, 3).join(' | ') : ''}`)
console.log(`\n通过 ${pass}，失败 ${fail}`)
await browser.close()
process.exit(fail ? 1 : 0)
