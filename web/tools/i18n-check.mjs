// 双语词条检查（验收 1.9）：
//   1. 中英两边的键一一对应（TypeScript 已在编译时保证，这里再跑一遍，防止有人绕过类型）
//   2. 每条的占位符 {名字} 两边完全一致
//   3. 没有空词条；英文词条里不能混入中文（语言名"中文"除外）
//   4. 后端定义的每个错误码都有词条
// 用法：node --experimental-strip-types tools/i18n-check.mjs   （在 web/ 目录下）
import fs from 'node:fs'
import { zh } from '../src/i18n/zh.ts'
import { en } from '../src/i18n/en.ts'

const fails = []
const zk = Object.keys(zh), ek = Object.keys(en)
for (const k of zk) if (!(k in en)) fails.push(`英文缺少：${k}`)
for (const k of ek) if (!(k in zh)) fails.push(`中文缺少：${k}`)
const ph = (s) => [...s.matchAll(/\{(\w+)\}/g)].map((m) => m[1]).sort().join(',')
for (const k of zk) {
  if (!(k in en)) continue
  if (!zh[k].trim()) fails.push(`中文为空：${k}`)
  if (!en[k].trim()) fails.push(`英文为空：${k}`)
  if (ph(zh[k]) !== ph(en[k])) fails.push(`占位符不一致：${k}  中[${ph(zh[k])}] 英[${ph(en[k])}]`)
  if (k !== 'lang.zh' && /[一-鿿]/.test(en[k])) fails.push(`英文里有中文：${k}`)
}
// 后端错误码
const backend = new URL('../../backend/internal/auth/service.go', import.meta.url)
if (fs.existsSync(backend)) {
  const src = fs.readFileSync(backend, 'utf8')
  for (const m of src.matchAll(/=\s*e\(\d+,\s*"([A-Z_]+)"\)/g)) if (!(`err.${m[1]}` in zh)) fails.push(`后端错误码没有词条：${m[1]}`)
  for (const c of ['STARTING', 'INTERNAL_ERROR']) if (!(`err.${c}` in zh)) fails.push(`缺少错误码词条：${c}`)
}
// 交易引擎的拒单代码和撤销原因
const tdir = new URL('../../backend/internal/trading/', import.meta.url)
if (fs.existsSync(tdir)) {
  for (const f of fs.readdirSync(tdir).filter((f) => f.endsWith('.go') && !f.endsWith('_test.go'))) {
    const src = fs.readFileSync(new URL(f, tdir), 'utf8')
    for (const m of src.matchAll(/rej\("([A-Z_]+)"/g)) if (!(`err.${m[1]}` in zh)) fails.push(`交易拒单代码没有词条：${m[1]}（${f}）`)
    for (const m of src.matchAll(/(?:closeOrder\([^)]*?, St\w+, |expireRest\([^)]*?, )"([A-Z_]+)"/g)) if (!(`reason.${m[1]}` in zh)) fails.push(`撤销原因没有词条：${m[1]}（${f}）`)
    for (const m of src.matchAll(/"code": "([A-Z_]+)"/g)) if (!(`err.${m[1]}` in zh)) fails.push(`接口错误码没有词条：${m[1]}（${f}）`)
  }
}
if (fails.length) {
  console.error(fails.join('\n'))
  process.exit(1)
}
console.log(`双语词条检查通过：${zk.length} 条，键、占位符一一对应`)
