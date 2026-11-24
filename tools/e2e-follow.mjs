// 跟随和权限的接口检查：未登录、登录没开启、开启服务三种人看到什么；开启跟随；
// 用价格注入造出最大亏损；重启后状态不变。
// 只能对着模拟行情的测试环境跑（注册账号、下单）：没有 /api/dev/sim-price 就直接退出，绝不碰 8080。
//   BASE=http://127.0.0.1:18000 node tools/e2e-follow.mjs --restart-cmd "powershell ... restart.ps1"
import { randomBytes } from 'node:crypto'
import { execSync } from 'node:child_process'

const BASE = process.env.BASE || 'http://127.0.0.1:18000'
const restartIdx = process.argv.indexOf('--restart-cmd')
const RESTART = restartIdx > 0 ? process.argv[restartIdx + 1] : ''
let pass = 0, fail = 0
const ok = (name, cond, extra = '') => {
  if (cond) { pass++; console.log('  ✓', name) } else { fail++; console.log('  ✗', name, extra) }
}
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

function client() {
  let cookie = ''
  return async (method, path, body) => {
    const res = await fetch(BASE + path, { method, headers: { 'X-QS-Client': 'web', 'Content-Type': 'application/json', ...(cookie ? { Cookie: cookie } : {}) }, body: body ? JSON.stringify(body) : undefined })
    const set = res.headers.get('set-cookie')
    if (set) cookie = set.split(';')[0]
    let json = null
    try { json = await res.json() } catch { /* 空回复 */ }
    return { status: res.status, json }
  }
}

const probe = await fetch(BASE + '/api/dev/sim-price', { method: 'POST', body: '{}' })
if (probe.status === 404) { console.error('这不是模拟行情的测试环境，退出'); process.exit(2) }

const anon = client()
const STRAT = 'demo-random'
console.log('未登录')
for (const p of ['/api/signals', `/api/strategies/${STRAT}/decisions`, `/api/strategies/${STRAT}/trades`, `/api/strategies/${STRAT}/calibration`, '/api/follows']) {
  ok(`${p} → 401`, (await anon('GET', p)).status === 401)
}
const pub = await anon('GET', `/api/strategies/${STRAT}`)
ok('净值公开，没有持仓', pub.status === 200 && pub.json.locked === true && pub.json.state.positions.length === 0, JSON.stringify(pub.json).slice(0, 200))
const perf = await anon('GET', `/api/strategies/${STRAT}/performance`)
ok('战绩公开', perf.status === 200 && 'return' in perf.json && 'closed' in perf.json)
ok('战绩里没有理由和账本', !/rationale|ledger|signals|message/.test(JSON.stringify(perf.json)))

console.log('登录但没开启服务')
const u = client()
const email = `f${randomBytes(6).toString('hex')}@example.com`
const reg = await u('POST', '/api/auth/register', { email, password: randomBytes(12).toString('hex') })
ok('注册', reg.status === 200, JSON.stringify(reg.json))
for (const p of ['/api/signals', `/api/strategies/${STRAT}/decisions`, `/api/strategies/${STRAT}/trades`, `/api/strategies/${STRAT}/calibration`]) {
  const r = await u('GET', p)
  ok(`${p} → 403`, r.status === 403 && r.json.code === 'FOLLOW_REQUIRED')
}
ok('还是看不到持仓', (await u('GET', `/api/strategies/${STRAT}`)).json.locked === true)

console.log('开启跟随')
ok('没确认不开', (await u('POST', '/api/follows', { strategy: STRAT, alloc: '5000' })).json.code === 'CONFIRM_REQUIRED')
// 模拟资金不限额（D103，11-16）：分配的比模拟账户可用的多，差的自动加进去（记成加钱）；试完停掉，下面接着用自动建的"策略跟随"账户
const pa1 = (await u('GET', '/api/paper-accounts')).json.accounts[0].id
const big = await u('POST', '/api/follows', { strategy: STRAT, account: pa1, alloc: '20000', confirm: true })
const pv = (await u('GET', `/api/trade/${pa1}`)).json
ok('超过可用资金：模拟账户自动补上', big.status === 200 && Number(pv.initial) >= 20000, `${big.status} ${big.json?.code ?? ''} 本金 ${pv.initial}`)
if (big.status === 200) await u('POST', `/api/follows/${big.json.id}/stop`, { mode: 'keep' })
const st = await u('POST', '/api/follows', { strategy: STRAT, alloc: '5000', max_loss: '0.005', confirm: true })
ok('开启', st.status === 200 && st.json.status === 'active', JSON.stringify(st.json))
const fid = st.json.id, acct = st.json.account
ok('默认 3 倍', st.json.params.max_leverage === 3)
const accts = await u('GET', '/api/paper-accounts')
ok('自动建了"策略跟随"账户', accts.json.accounts.some((a) => a.id === acct && a.name === '策略跟随'))
ok('第二次开说明原因', (await u('POST', '/api/follows', { strategy: STRAT, account: acct, alloc: '1000', confirm: true })).json.code === 'ACCOUNT_FOLLOWING')
for (const p of ['/api/signals', `/api/strategies/${STRAT}/decisions`, `/api/strategies/${STRAT}/trades`, `/api/strategies/${STRAT}/calibration`]) {
  ok(`${p} → 200`, (await u('GET', p)).status === 200)
}
ok('现在看得到持仓', (await u('GET', `/api/strategies/${STRAT}`)).json.locked === false)

console.log('等 AI 开仓（示例策略每 45 秒一条信号）')
let pos = null
for (let i = 0; i < 90 && !pos; i++) {
  const v = await u('GET', `/api/trade/${acct}`)
  pos = v.json.positions?.[0] ?? null
  if (!pos) await sleep(2000)
}
ok('跟随单成交了', !!pos)
if (pos) {
  ok('逐仓 3 倍', pos.mode === 'isolated' && pos.leverage === 3, JSON.stringify(pos))
  const fills = await u('GET', `/api/trade/${acct}/history?kind=fills`)
  const f = fills.json.items?.[0]
  ok('来源标跟随', f?.source === 'follow', JSON.stringify(f))
  // 成交和执行记录更新之间隔着一次写库，等一下再查
  let detail = null, rec = null
  for (let i = 0; i < 15 && !rec; i++) {
    detail = await u('GET', `/api/follows/${fid}`)
    rec = detail.json.records.find((r) => r.state === 'filled')
    if (!rec) await sleep(200)
  }
  ok('有执行记录和跟随滑点字段', !!rec && 'slip_bps' in rec && 'model_avg' in rec, JSON.stringify(detail.json.records[0]))

  console.log('价格注入：亏到最大亏损')
  const px = Number(pos.mark) * (pos.side === 'long' ? 0.9 : 1.1)
  const inj = await fetch(BASE + '/api/dev/sim-price', { method: 'POST', body: JSON.stringify({ instrument: pos.instrument, price: px }) })
  ok('注入价格', inj.status < 300)
  let s = null
  for (let i = 0; i < 20; i++) {
    s = (await u('GET', `/api/follows/${fid}`)).json
    if (s.status === 'stopped') break
    await sleep(500)
  }
  ok('自动平仓并停止', s.status === 'stopped' && s.reason === 'max_loss', `${s.status} ${s.reason}`)
  const after = await u('GET', `/api/trade/${acct}`)
  ok('仓位平掉了', after.json.positions.length === 0, JSON.stringify(after.json.positions))
  ok('停了就不算开启服务', (await u('GET', '/api/signals')).status === 403)
}

console.log('重启')
const again = await u('POST', '/api/follows', { strategy: STRAT, account: acct, alloc: '3000', max_loss: '0.3', max_leverage: 5, per_inst: '0.6', blacklist: ['SOL-PERP'], confirm: true })
ok('停了可以再开', again.status === 200, JSON.stringify(again.json))
const fid2 = again.json.id
await u('POST', `/api/follows/${fid2}/pause`, {})
const before = (await u('GET', `/api/follows/${fid2}`)).json
let beforeAcct = (await u('GET', `/api/trade/${acct}`)).json
if (RESTART) {
  await sleep(1500) // 等事件写进数据库
  // 挂单是排队成交的，成交会陆续进来：重启前这一刻再记一次钱包，不然中间成交一笔就误报
  beforeAcct = (await u('GET', `/api/trade/${acct}`)).json
  await sleep(600)
  execSync(RESTART, { stdio: 'ignore' })
  let up = false
  for (let i = 0; i < 60 && !up; i++) {
    try { up = (await fetch(BASE + '/api/health')).ok && (await u('GET', `/api/follows/${fid2}`)).status === 200 } catch { /* 还没起来 */ }
    if (!up) await sleep(500)
  }
  const after = (await u('GET', `/api/follows/${fid2}`)).json
  const pick = (x) => JSON.stringify({ s: x.status, p: x.params, a: x.account, st: x.strategy, n: x.records?.length })
  ok('状态和参数不变', pick(before) === pick(after), `${pick(before)} / ${pick(after)}`)
  const afterAcct = (await u('GET', `/api/trade/${acct}`)).json
  ok('钱包不变', afterAcct.wallet === beforeAcct.wallet, `${beforeAcct.wallet} / ${afterAcct.wallet}`)
  ok('第一次跟随的记录还在', (await u('GET', '/api/follows')).json.follows.some((f) => f.id === fid)) // 开头试"自动补钱"那次也在列表里，所以不按条数数
}
await u('POST', `/api/follows/${fid2}/stop`, { mode: 'close' })

console.log(`\n通过 ${pass}，失败 ${fail}`)
process.exit(fail ? 1 : 0)
