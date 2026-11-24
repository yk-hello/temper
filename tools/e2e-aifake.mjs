// AI 点评的完整链路，模型换成本机的假 Claude：回归里跑，不花钱、不要密钥（2026-11-18，用户："就算是真人碰不到的，你也要想到靠谱的办法去测试他"）。
// 真下单、平仓，在复盘页点"AI 点评"，看到点评；中英文、刷新读缓存、手机宽度都走一遍（流程就是 e2e-ai-real.mjs）。
// 假服务按 Claude 官方接口检查请求：POST …/v1/messages，带密钥（Authorization: Bearer 或 x-api-key）、anthropic-version，有 model、max_tokens、system、messages、
// 系统页设的思考程度（output_config.effort = low），不带 temperature，
// 少一样就回 400（页面上就出不来点评，检查会失败）。请求里出现交易笔记的内容就原样带回去，让"回复里没有笔记"那条检查抓到。
// 用法：node e2e-aifake.mjs <截图目录> <前端地址>。会注册账号、下单，不要对着 8080 跑。
import http from 'http'

const PORT = 2528
let calls = 0
const bad = []
const srv = http.createServer((req, res) => {
  let body = ''
  req.on('data', (c) => { body += c })
  req.on('end', () => {
    const fail = (why) => { bad.push(why); res.writeHead(400, { 'content-type': 'application/json' }); res.end(JSON.stringify({ type: 'error', error: { type: 'invalid_request_error', message: why } })) }
    if (req.method !== 'POST' || !/\/v1\/messages(\?|$)/.test(req.url ?? '')) return fail(`路径不对：${req.method} ${req.url}`)
    // 官方 Go SDK（v2）的 WithAPIKey 发 Authorization: Bearer；也认老的 x-api-key
    if (!req.headers['x-api-key'] && !/^Bearer sk-ant-fake-/.test(req.headers.authorization ?? '')) return fail('没带密钥（x-api-key 或 Authorization: Bearer）')
    if (!req.headers['anthropic-version']) return fail('没带 anthropic-version')
    let j
    try { j = JSON.parse(body) } catch { return fail('不是 JSON') }
    if (!j.model || !(j.max_tokens > 0) || !j.system || !Array.isArray(j.messages) || !j.messages.length) return fail(`缺字段：${Object.keys(j).join(',')}`)
    // 思考程度（D112）：厂商设的是"低"，要放在 output_config.effort 里；Claude 新模型不认 temperature
    if (j.output_config?.effort !== 'low') return fail(`思考程度没按设置传：${JSON.stringify(j.output_config ?? null)}`)
    if ('temperature' in j) return fail('带了 temperature')
    calls++
    const sys = typeof j.system === 'string' ? j.system : JSON.stringify(j.system)
    const user = JSON.stringify(j.messages)
    const en = /Reply with exactly three lines/.test(sys)
    let text = en
      ? 'Did well: A stop was set and the loss stayed near 1R.\nFix first: The stop sat only 0.8% from entry.\nRule for next time: Place the stop at least 1.5% from entry.'
      : '做对了：设了止损，亏损在 1R 左右。\n先改这个：止损离开仓价只有 0.8%，太近。\n下次的规则：止损放在开仓价 1.5% 以外。'
    const leak = user.match(/SECRETNOTE[0-9a-f]+/)
    if (leak) text += ' ' + leak[0]
    res.writeHead(200, { 'content-type': 'application/json' })
    res.end(JSON.stringify({ id: `msg_${calls}`, type: 'message', role: 'assistant', model: j.model, content: [{ type: 'text', text }], stop_reason: 'end_turn', stop_sequence: null, usage: { input_tokens: 300, output_tokens: 60 } }))
  })
})
await new Promise((ok, no) => { srv.once('error', no); srv.listen(PORT, '127.0.0.1', ok) })
srv.unref()
process.on('exit', () => {
  console.log(`假 Claude 收到 ${calls} 次合格的请求${bad.length ? `，${bad.length} 次不合格：${bad.slice(0, 3).join(' | ')}` : ''}`)
  if (bad.length) process.exitCode = 1
})
process.env.QS_E2E_AI_FAKE = `http://127.0.0.1:${PORT}`
await import('./e2e-ai-real.mjs')
