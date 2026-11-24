// 模块 5 的检查（12 的 5.1–5.13）：真的下单、设止损、用价格注入打掉止损，再看复盘页。
// 会注册账号、下单，只能对着模拟行情的测试环境跑。
//   node tools/e2e-review.mjs <截图目录> <网址>
import { chromium } from 'playwright'
import { DEVICES } from './devices.mjs'

const OUT = process.argv[2] || '.'
const BASE = process.argv[3] || 'http://127.0.0.1:4173'
let pass = 0, fail = 0
const ok = (cond, name) => { if (cond) { pass++; console.log('✓', name) } else { fail++; console.log('✗', name) } }
const browser = await chromium.launch({ channel: process.env.PW_CHANNEL || 'msedge', args: ['--use-gl=angle', '--use-angle=swiftshader', '--enable-unsafe-swiftshader'] })
const newPage = async (opts = {}) => {
  const c = await browser.newContext({ viewport: { width: 1536, height: 730 }, deviceScaleFactor: 1.25, locale: 'zh-CN', timezoneId: 'Asia/Taipei', ...opts })
  await c.addInitScript(() => { localStorage.setItem('qs.wizardSeen', '1'); localStorage.setItem('qs.chartMode', 'candle') })
  const p = await c.newPage()
  p.errs = []
  p.on('pageerror', (e) => p.errs.push(String(e)))
  return p
}
const call = (p, method, path, body) => p.evaluate(async ({ method, path, body }) => {
  const r = await fetch(path, { method, headers: { 'X-QS-Client': 'web', 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined })
  const text = await r.text()
  let j = null
  try { j = JSON.parse(text) } catch { /* 不是 JSON */ }
  return { s: r.status, b: j, text }
}, { method, path, body })
const rand = () => Math.random().toString(36).slice(2)
const until = async (fn, ms = 8000) => {
  const t0 = Date.now()
  while (Date.now() - t0 < ms) {
    const v = await fn()
    if (v) return v
    await new Promise((r) => setTimeout(r, 200))
  }
  return null
}
const register = async (p, tag) => {
  await p.goto(BASE + '/?page=login')
  const r = await call(p, 'POST', '/api/auth/register', { email: `rv${tag}${Date.now()}${rand().slice(0, 4)}@example.com`, password: `rv-${rand()}-pass` })
  if (r.s >= 300) throw new Error('注册失败 ' + JSON.stringify(r))
  const tp = 'tp-' + rand()
  await call(p, 'POST', '/api/me/trade-password', { new: tp })
  await call(p, 'POST', '/api/discipline/enabled', { on: false, trade_password: tp })
  const list = await call(p, 'GET', '/api/paper-accounts')
  return (list.b.accounts ?? list.b)[0].id
}
const INST = 'BTC-PERP'
const price = async (p) => Number((await call(p, 'GET', '/api/market/snapshot')).b.instruments.find((x) => x.instrument === INST).last)
const order = (p, acct, side, qty, extra = {}) => call(p, 'POST', `/api/trade/${acct}/orders`, { instrument: INST, side, type: 'market', qty: String(qty), ...extra })
const inject = (p, px) => call(p, 'POST', '/api/dev/sim-price', { instrument: INST, price: px })
const flat = (p, acct) => until(async () => { const v = await call(p, 'GET', `/api/trade/${acct}`).catch(() => null); return v?.s === 200 && !(v.b.positions ?? []).some((x) => x.instrument === INST) })
const round1 = (v) => Math.round(v * 10) / 10

// 没登录
{
  const q = await newPage()
  await q.goto(BASE + '/?page=review')
  await q.waitForSelector('.rv-hint', { timeout: 10000 })
  ok(/登录/.test(await q.locator('.rv-hint').innerText()) && !/示例/.test(await q.locator('.lp').innerText()), '没登录时进回放练习，提示登录，没有示例数据')
  ok((await call(q, 'GET', '/api/journal/a/1/trades')).s === 401, '没登录不能取数据')
  await q.context().close()
}

const p = await newPage()
const A = await register(p, 'a')
await p.goto(BASE + '/?page=trade&i=BTC-PERP')
await p.waitForSelector('.mk-chart canvas')
const px0 = await until(() => price(p))

// 第 1 笔：多单，止损 2%，用注入的价格打掉 → stop_loss，R 约 −1
ok((await order(p, A, 'buy', 0.1)).s === 200, '第 1 笔开仓')
const sl = round1(px0 * 0.98)
ok((await call(p, 'POST', `/api/trade/${A}/tpsl`, { instrument: INST, sl: String(sl) })).s === 200, '设止损')
await new Promise((r) => setTimeout(r, 400))
await inject(p, round1(px0 * 0.97))
ok(await flat(p, A), '止损被打掉')
await inject(p, px0)
await new Promise((r) => setTimeout(r, 500))

// 第 2 笔：没设止损，手动平
const px2 = await price(p)
await order(p, A, 'buy', 0.1)
await new Promise((r) => setTimeout(r, 500))
await order(p, A, 'sell', 0.1, { reduce_only: true })
await flat(p, A)

// 第 3 笔：空单，止损往亏钱方向挪
await order(p, A, 'sell', 0.1)
await call(p, 'POST', `/api/trade/${A}/tpsl`, { instrument: INST, sl: String(round1(px2 * 1.02)) })
await new Promise((r) => setTimeout(r, 200))
await call(p, 'POST', `/api/trade/${A}/tpsl`, { instrument: INST, sl: String(round1(px2 * 1.04)) })
await new Promise((r) => setTimeout(r, 300))
await order(p, A, 'buy', 0.1, { reduce_only: true })
await flat(p, A)
await new Promise((r) => setTimeout(r, 400))

// 5.1、5.4 接口
const tr = await call(p, 'GET', `/api/journal/a/${A}/trades`)
ok(tr.s === 200 && tr.b.trades.length === 3, `日志有 3 笔（${tr.b?.trades?.length}）`)
const [t3, t2, t1] = tr.b.trades
ok(t1.closed_by === 'stop_loss' && t1.stop === sl, `第 1 笔是止损平的，初始止损对（${t1.closed_by}，${t1.stop}）`)
ok(t1.r != null && t1.r < -0.5 && t1.r > -2.5, `第 1 笔 R 算出来了（${t1.r}）`)
ok(Math.abs(t1.risk - Math.abs(t1.entry - t1.stop) * t1.qty) < 1e-6, '初始风险 = 开仓价到止损 × 数量')
ok(t1.gross - t1.fees + t1.funding - t1.net < 1e-6, '净收益 = 平仓盈亏 − 手续费 + 资金费')
ok(t1.legs.length >= 2 && t1.legs.every((l) => l.ref > 0), '成交带下单时的参考价')
ok(t1.slip_known === true, '滑点能算')
ok(t2.r === null && t2.violations.includes('no_stop'), `没设止损写无 R，标违规（${t2.violations}）`)
ok(t3.violations.includes('moved_stop'), `挪了止损标违规（${t3.violations}）`)
ok(t3.side === 'short', '第 3 笔是空单')
ok(t1.origin === 'manual', '来源是手动')

// 5.12 CSV
for (const k of ['trades', 'fills', 'funding']) {
  const r = await call(p, 'GET', `/api/journal/a/${A}/export?kind=${k}`)
  const bom = await p.evaluate(async (u) => { const b = new Uint8Array(await (await fetch(u)).arrayBuffer()); return [b[0], b[1], b[2]].join() }, `/api/journal/a/${A}/export?kind=${k}`)
  ok(r.s === 200 && bom === '239,187,191' && r.text.includes('\r\n'), `导出 ${k}（${r.text.split('\r\n').length - 2} 行）`)
}
ok((await call(p, 'GET', `/api/journal/a/${A}/export?kind=x`)).s === 400, '导出类型不对返回 400')

// 另一个人看不到
const q = await newPage()
const B = await register(q, 'b')
ok((await call(q, 'GET', `/api/journal/a/${A}/trades`)).s === 404, '别人看不到我的日志')
ok((await call(q, 'PUT', `/api/journal/a/${A}/trades/${t1.id}/note`, { note: 'x', tags: [] })).s === 404, '别人写不了我的笔记')

// 5.3 没建预案时下单区没有那一行
await p.goto(BASE + '/?page=trade&i=BTC-PERP')
await p.waitForSelector('.op')
await p.waitForTimeout(800)
ok(await p.locator('[data-op=playbook]').count() === 0, '没建预案时下单区没有多一行')

// 页面：交易日志
await p.goto(BASE + '/?page=review')
await p.waitForSelector('.rv-table', { timeout: 10000 })
await p.waitForTimeout(600)
ok(await p.locator('.rv-table tbody tr').count() === 3, '列表 3 笔')
ok(/无 R/.test(await p.locator('.rv-table').innerText()), '列表写无 R')
const read = await p.locator('.rv-read').first().innerText()
ok(/胜率/.test(read) && /期望值/.test(read) && /盈亏比/.test(read) && /利润因子/.test(read) && /平均 R/.test(read), `顶部统计：${read.replace(/\s+/g, ' ').slice(0, 70)}`)
ok(/样本太少/.test(read), '笔数太少写明')
await p.screenshot({ path: `${OUT}/rv-log.png` })
await p.locator('.rv-table tbody tr').last().click()
await p.waitForSelector('.rv-detail')
await p.waitForTimeout(250)
const det = await p.locator('.rv-detail').innerText()
ok(/初始止损/.test(det) && /初始风险/.test(det) && /净收益/.test(det) && /手续费/.test(det), '详情有止损、风险、费用拆解')
await p.screenshot({ path: `${OUT}/rv-detail.png` })

// 5.2 标签、笔记、情绪
await p.locator('.rv-note input.rv-in').first().fill('追高 重仓')
await p.locator('.rv-chips button').nth(2).click()
await p.locator('.rv-note textarea').fill('下次等回踩')
await p.locator('[data-rv=save]').click()
await until(async () => (await call(p, 'GET', `/api/journal/a/${A}/trades`)).b.trades.find((x) => x.id === t1.id)?.note?.note === '下次等回踩')
const saved = (await call(p, 'GET', `/api/journal/a/${A}/trades`)).b.trades.find((x) => x.id === t1.id).note
ok(saved.tags.length === 2 && saved.emotion === 'fomo', `笔记存下来了（${saved.tags}，${saved.emotion}）`)

// 截图：真 PNG、假图片、太大
const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==', 'base64')
await p.locator('.rv-shots input[type=file]').setInputFiles({ name: 'a.png', mimeType: 'image/png', buffer: png })
const shot = await until(async () => (await call(p, 'GET', `/api/journal/a/${A}/trades`)).b.trades.find((x) => x.id === t1.id)?.shots?.[0])
ok(shot && shot.mime === 'image/png', '截图传上去了')
const up = (body, type = 'image/png') => p.evaluate(async ({ a, id, body, type }) => {
  const r = await fetch(`/api/journal/a/${a}/trades/${id}/shots`, { method: 'POST', headers: { 'X-QS-Client': 'web', 'Content-Type': type }, body: new Uint8Array(body) })
  return r.status
}, { a: A, id: t1.id, body, type })
ok(await up([60, 115, 118, 103, 62]) === 415, '不是图片的不收（svg 也不收）')
ok(await up(new Array(5 * 1024 * 1024 + 10).fill(0)) === 413, '超过 5 MB 不收')
ok((await call(p, 'GET', `/api/journal/shots/${shot.id}`)).s === 200, '自己能看截图')
ok((await call(q, 'GET', `/api/journal/shots/${shot.id}`)).s === 404, '别人看不到截图')
ok((await call(q, 'DELETE', `/api/journal/shots/${shot.id}`)).s === 404, '别人删不了截图')
// 小 × 先问一句再删（11-17 走查：一点就删、删了找不回）
await p.locator('.rv-shot button').first().click()
const askDel = p.locator('.dlg', { hasText: '删除这张截图' })
ok(!!(await askDel.waitFor({ timeout: 5000 }).then(() => true).catch(() => false)), '删截图先确认')
ok(!!(await call(p, 'GET', `/api/journal/a/${A}/trades`)).b.trades.find((x) => x.id === t1.id)?.shots?.length, '确认前没删')
await askDel.locator('.btn.danger').click()
ok(!!(await until(async () => !(await call(p, 'GET', `/api/journal/a/${A}/trades`)).b.trades.find((x) => x.id === t1.id)?.shots?.length)), '确认后删掉了')

// 5.9 回放
await p.locator('[data-rv=replay]').click()
await p.waitForSelector('.rv-replay', { timeout: 8000 })
await p.waitForTimeout(1200)
const c0 = await p.locator('[data-rv=clock]').innerText()
await p.locator('[data-rv=play]').click()
await p.waitForTimeout(1500)
const c1 = await p.locator('[data-rv=clock]').innerText()
ok(c0 !== c1, `回放在走（${c0} → ${c1}）`)
await p.locator('[data-rv=play]').click()
await p.waitForTimeout(200)
const c2 = await p.locator('[data-rv=clock]').innerText()
await p.waitForTimeout(500)
ok(c2 === await p.locator('[data-rv=clock]').innerText(), '暂停后不走')
const tb = await p.locator('.rv-r-track').boundingBox()
await p.mouse.click(tb.x + tb.width * 0.6, tb.y + tb.height / 2)
await p.waitForTimeout(400)
ok(await p.locator('.rv-r-track i').count() >= 3, '进度条标出买卖点')
ok(await p.locator('.rv-r-cell').count() === 2 || /录制之前/.test(await p.locator('.rv-replay').innerText()), '有频谱，或者写明只有 K 线')
ok(/下次等回踩/.test(await p.locator('.rv-replay').innerText()), '回放里有笔记')
await p.screenshot({ path: `${OUT}/rv-replay.png` })
await p.keyboard.press('Escape')
await p.waitForTimeout(250)
ok(await p.locator('.rv-replay').count() === 0, 'Esc 关闭回放')

// 绩效
await p.locator('.rv-head [role=radio], .rv-head .seg button').filter({ hasText: '绩效' }).first().click()
await p.waitForSelector('.rv-perf')
await p.waitForTimeout(500)
ok(await p.locator('.rv-cal-grid i.up-bg, .rv-cal-grid i.down-bg').count() >= 1, '盈亏日历有格子')
ok(await p.locator('.rv-bar').count() === 8, 'R 分布 8 格')
ok(await p.locator('.rv-fall-row').count() >= 4, '成本瀑布')
const st = (await call(p, 'GET', `/api/journal/a/${A}/stats?tz=480`)).b.stats
const w = st.waterfall
ok(Math.abs(w.ideal - w.slip - w.fees + w.funding - w.net) < 0.01, `瀑布数字能加回去（${w.ideal} − ${w.slip} − ${w.fees} + ${w.funding} = ${w.net}）`)
ok(st.all.n === 3, '统计 3 笔')
ok(st.underwater.length === 3 && st.underwater.every((x) => x.dd <= 0), '水下图 3 个点')
ok(st.violations.no_stop?.n === 1 && st.violations.moved_stop?.n === 1, '违规统计对')
ok(['instrument', 'slot', 'weekday', 'playbook'].every((k) => st.groups[k]?.length > 0), '四种分组都有')
await p.screenshot({ path: `${OUT}/rv-perf.png` })

// MAE / MFE
await p.locator('.rv-head [role=radio], .rv-head .seg button').filter({ hasText: 'MAE' }).first().click()
await p.waitForSelector('.rv-mae canvas', { timeout: 10000 })
await p.waitForTimeout(1500)
const ex = (await call(p, 'GET', `/api/journal/a/${A}/excursions`)).b.items
ok(ex.length === 3 && ex.every((e) => e.source === 'ticks' || e.source === '1m' || e.source === '5m'), `MAE / MFE 写明用的哪种数据（${ex.map((e) => e.source)}）`)
const stopT = ex.find((e) => e.id === t1.id)
ok(stopT.mae_r >= 0.9, `被止损的单 MAE 至少到 1R（${stopT.mae_r}）`)
ok(/逐笔.*K 线/.test(await p.locator('.rv-mae .rv-read').innerText()), '界面写数据来源')
await p.screenshot({ path: `${OUT}/rv-mae.png` })

// 结论
await p.locator('.rv-head [role=radio], .rv-head .seg button').filter({ hasText: '结论' }).first().click()
await p.waitForSelector('.rv-find', { timeout: 15000 })
await p.waitForTimeout(400)
const fd = await p.locator('.rv-find').innerText()
ok(/没设止损/.test(fd) && /挪了止损/.test(fd), `结论写问题在哪：${fd.replace(/\s+/g, ' ').slice(0, 80)}`)
ok(/样本太少/.test(fd), '结论写样本太少')
const fds = (await call(p, 'GET', `/api/journal/a/${A}/findings`)).b.items
ok(fds.some((x) => x.code === 'stop_tighter' || x.code === 'tp_earlier'), `有反事实（${fds.map((x) => x.code)}）`)
await p.screenshot({ path: `${OUT}/rv-findings.png` })

// 5.3 预案
await p.locator('.rv-head [role=radio], .rv-head .seg button').filter({ hasText: '预案' }).first().click()
await p.waitForSelector('.rv-pb')
await p.locator('[data-rv=pb-new]').click()
await p.waitForSelector('[data-pbf=name]')
await p.locator('[data-pbf=name]').fill('回踩做多')
await p.locator('[data-pbf=entry]').fill('回踩 20 日均线不破')
await p.locator('[data-pbf=stop]').fill('前低下方')
await p.locator('[data-pbf=target]').fill('前高')
await p.locator('[data-rv=pb-save]').click()
await p.waitForSelector('[data-pb]', { timeout: 5000 })
ok(/回踩做多/.test(await p.locator('.rv-pb').innerText()), '建了预案')
await p.screenshot({ path: `${OUT}/rv-pb.png` })
const pid = (await call(p, 'GET', '/api/journal/playbooks')).b.playbooks[0].id
ok((await call(q, 'GET', '/api/journal/playbooks')).b.playbooks.length === 0, '别人看不到我的预案')
ok((await call(q, 'DELETE', `/api/journal/playbooks/${pid}`)).s === 404, '别人删不了我的预案')
ok((await call(p, 'POST', '/api/journal/playbooks', { name: '  ' })).s === 400, '名称不能空')

// 建了预案，下单区多一行；下单时选的预案记到这笔交易上
await p.goto(BASE + '/?page=trade&i=BTC-PERP')
await p.waitForSelector('[data-op=playbook]', { timeout: 8000 })
ok(true, '建了预案后下单区多一行')
await p.screenshot({ path: `${OUT}/rv-order-row.png` })
const o4 = await order(p, A, 'buy', 0.1)
await call(p, 'POST', `/api/journal/a/${A}/order-playbook`, { order_id: o4.b.id, playbook: pid })
await new Promise((r) => setTimeout(r, 300))
await order(p, A, 'sell', 0.1, { reduce_only: true })
await flat(p, A)
await new Promise((r) => setTimeout(r, 400))
const after = (await call(p, 'GET', `/api/journal/a/${A}/trades`)).b.trades[0]
ok(after.playbook === pid, `下单时选的预案记到交易上（${after.playbook}）`)
// 事后也能改
await call(p, 'PUT', `/api/journal/a/${A}/trades/${t2.id}/note`, { tags: [], note: '', emotion: '', playbook: pid })
const by = (await call(p, 'GET', `/api/journal/a/${A}/stats`)).b
ok(by.stats.groups.playbook.find((x) => x.key === String(pid))?.n === 2, '事后改预案，分组统计跟着变')
ok(by.playbook_names[pid] === '回踩做多', '统计里有预案名字')

// AI 的单默认不看（这里没有 AI 的单，查过滤条件）
ok((await call(p, 'GET', `/api/journal/a/${A}/trades?origin=ai`)).b.trades.length === 0, '切到 AI 没有手动的单')

// 超过每笔风险：纪律锁开着时，先开仓、后设一个太远的止损（默认每笔 1%，本金 1 万就是 100 美元）
{
  const pr = await newPage()
  const R0 = await register(pr, 'risk')
  await call(pr, 'POST', '/api/discipline/enabled', { on: true })
  const pxr = await price(pr)
  await order(pr, R0, 'buy', '0.05')
  const sl = await call(pr, 'POST', `/api/trade/${R0}/tpsl`, { instrument: INST, sl: String(round1(pxr * 0.95)) })
  await pr.waitForTimeout(500)
  await order(pr, R0, 'sell', '0.05', { reduce_only: true })
  await flat(pr, R0)
  const list = (await call(pr, 'GET', `/api/journal/a/${R0}/trades`)).b.trades
  const over = list.find((x) => x.violations.includes('over_risk'))
  ok(!!over, `止损太远、风险超过每笔上限：标"超过每笔风险"（设止损 ${sl.s}，${list[0]?.violations}）`)
  await pr.goto(BASE + '/?page=review')
  await pr.waitForSelector('.rv-table')
  await pr.locator('.rv-table tbody tr', { hasText: 'BTC' }).first().click()
  await pr.waitForSelector('.rv-detail')
  await pr.waitForTimeout(500)
  ok(/超过每笔风险/.test(await pr.locator('.rv-detail').innerText()), '明细里写着超过每笔风险')
  await pr.screenshot({ path: `${OUT}/rv-over-risk.png` })
  await pr.context().close()
}

// AI 的单：真的跟随一次，等它开仓，平掉，再看复盘
{
  const pa = await newPage()
  await register(pa, 'ai')
  const strats = (await call(pa, 'GET', '/api/strategies')).b
  const s0 = (strats.strategies ?? strats)[0]
  const sid = s0.strategy_id
  // 示例策略的信号是随机的：测试环境里别的脚本把模拟价格拉来拉去，它当天亏超 5% 就熔断（设计如此），不再开新仓。这种时候跳过，写明原因
  const tripped = s0.circuit_breaker || s0.halted
  if (tripped) console.log(`- 跳过 AI 复盘：示例策略 ${sid} 今天${s0.halted ? '已暂停' : '亏超 5% 已熔断'}，不会开新仓（测试环境状态，换一天或重建测试库再跑）`)
  const st = tripped ? { s: 0 } : await call(pa, 'POST', '/api/follows', { strategy: sid, confirm: true, alloc: '2000', max_loss: '0.2', max_leverage: 3, per_inst: '0.5', blacklist: [] })
  if (!tripped) ok(st.s === 200, `开启跟随（${st.s} ${st.b?.code ?? ''}）`)
  const fa = st.b?.account
  const opened = st.s === 200 && await until(async () => ((await call(pa, 'GET', `/api/trade/${fa}`)).b?.positions?.length ?? 0) > 0, 120_000)
  if (!tripped) ok(!!opened, 'AI 开了仓')
  if (opened) {
    await call(pa, 'POST', `/api/follows/${st.b.id}/stop`, { mode: 'close' })
    await until(async () => ((await call(pa, 'GET', `/api/trade/${fa}`)).b?.positions?.length ?? 1) === 0, 20_000)
    const ai = await until(async () => { const l = (await call(pa, 'GET', `/api/journal/a/${fa}/trades?origin=ai`)).b.trades; return l.length ? l : null }, 15_000)
    ok(!!ai, `AI 的单进了复盘（${ai?.length ?? 0} 笔）`)
    ok((await call(pa, 'GET', `/api/journal/a/${fa}/trades`)).b.trades.length === 0, '默认只看手动：AI 的单不混进来')
    await pa.goto(BASE + `/?page=review`)
    await pa.waitForSelector('.rv-log, .rv-out')
    await pa.waitForTimeout(800)
    await pa.screenshot({ path: `${OUT}/rv-ai-default.png` })
    const ori = pa.locator('[role=radio], .seg button').filter({ hasText: /^策略$/ }).first()
    await ori.click()
    await pa.waitForTimeout(600)
    ok(/没有策略跟随的交易/.test(await pa.locator('.rv-out').innerText()), '这个账户没有策略跟随的单：写明可以切账户')
    // 换账户只在顶栏（D73）
    await pa.locator('[data-sw]').click()
    await pa.locator(`[data-sw-item="${fa}"]`).click()
    await pa.waitForSelector('.rv-table')
    await pa.waitForTimeout(600)
    const rowsAi = await pa.locator('.rv-table tbody tr').count()
    ok(rowsAi > 0 && await pa.locator('.rv-table .rv-origin').count() >= 0, `切到 AI 跟随账户，看得到 AI 的单（${rowsAi} 笔）`)
    await pa.locator('.rv-table tbody tr').first().click()
    await pa.waitForSelector('.rv-detail')
    await pa.waitForTimeout(500)
    await pa.screenshot({ path: `${OUT}/rv-ai.png` })
    ok(pa.errs.length === 0, `AI 的单没有报错（${pa.errs[0] ?? ''}）`)
  }
  await pa.context().close()
}

// 5.11 练习：不影响任何账户。测试环境 K 线太少，用构造的 K 线
await p.route('**/api/candles?*', (route) => {
  const now = Date.now()
  const bars = Array.from({ length: 400 }, (_, i) => {
    const c = 70000 + Math.sin(i / 9) * 800 + i
    return { t: now - (400 - i) * 300_000, o: c - 20, h: c + 60, l: c - 60, c, vb: 5, vs: 4 }
  })
  route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(bars) })
})
const fillsBefore = (await call(p, 'GET', `/api/journal/a/${A}/export?kind=fills`)).text
await p.goto(BASE + '/?page=review&tab=practice')
await p.waitForSelector('.rv-prac canvas', { timeout: 10000 })
await p.waitForTimeout(800)
const clk0 = await p.locator('[data-rv=p-clock]').innerText()
await p.locator('[data-rv=p-next]').click()
await p.waitForTimeout(150)
ok(clk0 !== await p.locator('[data-rv=p-clock]').innerText(), '下一根能走')
await p.locator('[data-rv=p-long]').click()
await p.waitForSelector('[data-rv=p-float]')
await p.locator('[data-rv=p-play]').click()
await p.waitForTimeout(1300)
await p.locator('[data-rv=p-play]').click()
await p.locator('[data-rv=p-close]').click()
await p.waitForTimeout(200)
ok(await p.locator('[data-rv=p-n]').innerText() === '1', '练习单算一笔')
ok(/不进任何账户/.test(await p.locator('.rv-prac').innerText()), '写明不进任何账户')
await p.screenshot({ path: `${OUT}/rv-practice.png` })
ok(fillsBefore === (await call(p, 'GET', `/api/journal/a/${A}/export?kind=fills`)).text, '练习之后账户成交没变')
await p.unroute('**/api/candles?*')

// 导航和英文
await p.goto(BASE + '/?page=review')
await p.waitForSelector('.rv-table')
ok(await p.locator('.nav a[aria-current=page]').innerText() === '复盘', '导航有复盘')
ok(p.errs.length === 0, `没有报错（${p.errs[0] ?? ''}）`)
await p.locator('.bar-lang button, .bar-lang [role=radio]').filter({ hasText: 'EN' }).first().click()
await p.waitForTimeout(600)
const enText = (await p.locator('.lp').innerText()).replace(/追高|重仓|下次等回踩|回踩做多|中/g, '')
ok(!/[一-鿿]/.test(enText), `英文没有中文：${(enText.match(/[一-鿿].{0,12}/) ?? [''])[0]}`)
await p.locator('.bar-lang button, .bar-lang [role=radio]').filter({ hasText: '中' }).first().click()

// 隐藏金额
await p.reload()
await p.waitForSelector('.rv-table')
await p.keyboard.press('h')
await p.waitForTimeout(300)
ok(!/[+−]\d+\.\d\d(?!R)/.test(await p.locator('.rv-table').innerText()), '按 H 隐藏金额（R 是比例，不算金额）')
await p.keyboard.press('h')

// 16 种设备
const state = await p.context().storageState()
for (const d of DEVICES) {
  const c = await browser.newContext({ storageState: state, viewport: { width: d.w, height: d.h }, deviceScaleFactor: d.dpr, isMobile: d.w < 500, hasTouch: d.w < 500, locale: 'zh-CN', timezoneId: 'Asia/Taipei' })
  const m = await c.newPage()
  const errs = []
  m.on('pageerror', (e) => errs.push(String(e)))
  await m.goto(BASE + '/?page=review')
  await m.waitForSelector('.rv-table', { timeout: 15000 })
  await m.waitForTimeout(500)
  const results = []
  for (const tab of ['log', 'perf', 'mae', 'findings', 'playbooks', 'practice']) {
    await m.goto(BASE + `/?page=review&tab=${tab}`)
    await m.waitForTimeout(500)
    results.push(await m.evaluate(() => [document.documentElement.scrollHeight - innerHeight, document.documentElement.scrollWidth - innerWidth]))
  }
  const worst = results.reduce((a, r) => [Math.max(a[0], r[0]), Math.max(a[1], r[1])], [0, 0])
  // 这两条是用眼睛看才发现的：绩效页的格子互相压住、手机上当前页被挤出导航
  await m.goto(BASE + '/?page=review&tab=perf')
  await m.waitForSelector('.rv-perf')
  await m.waitForTimeout(500)
  const lay = await m.evaluate(() => {
    const rs = [...document.querySelectorAll('.rv-cell')].map((e) => e.getBoundingClientRect())
    let overlap = 0
    for (let i = 0; i < rs.length; i++) for (let j = i + 1; j < rs.length; j++) {
      const a = rs[i], b = rs[j]
      if (a.left < b.right - 1 && b.left < a.right - 1 && a.top < b.bottom - 1 && b.top < a.bottom - 1) overlap++
    }
    const wf = document.querySelector('.rv-wf'), rows = wf.querySelectorAll('.rv-fall-row')
    const scroller = document.querySelector('.rv-perf')
    const scrolls = getComputedStyle(scroller).overflowY === 'auto'
    const cut = !scrolls && rows[rows.length - 1].getBoundingClientRect().bottom > wf.getBoundingClientRect().bottom + 1
    const nav = document.querySelector('.bar .nav')
    const user = document.querySelector('.bar-user')?.getBoundingClientRect()
    return { overlap, cut, navCut: nav.scrollWidth > nav.clientWidth + 1 || !user || user.right > innerWidth + 1 || user.width < 12 }
  })
  ok(lay.overlap === 0 && !lay.cut, `${d.name}：绩效页格子不互相压住，成本瀑布没被切掉（重叠 ${lay.overlap}，切掉 ${lay.cut}）`)
  ok(!lay.navCut, `${d.name}：导航 5 项和账户入口都看得见`)
  ok(worst[0] <= 0 && worst[1] <= 0 && errs.length === 0, `${d.name}：6 个标签都整页不滚，不溢出（${worst}）${errs[0] ?? ''}`)
  if (d.w === 920) {
    await m.goto(BASE + '/?page=review&tab=perf'); await m.waitForTimeout(800); await m.screenshot({ path: `${OUT}/rv-920-perf.png` })
    await m.goto(BASE + '/?page=review'); await m.waitForSelector('.rv-table'); await m.waitForTimeout(400); await m.screenshot({ path: `${OUT}/rv-920-log.png` })
  }
  await c.close()
}

console.log(`\n通过 ${pass}，失败 ${fail}`)
await browser.close()
process.exit(fail ? 1 : 0)
