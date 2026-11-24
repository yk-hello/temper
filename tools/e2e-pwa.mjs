// 可安装应用（18 第 6.7 条）：清单、图标、后台脚本、断网时的说明页。只读，不注册账号，测试环境和 8080 都能跑。
// 用法：node tools/e2e-pwa.mjs <截图目录> [http://127.0.0.1:4173]
import { chromium } from 'playwright'

const OUT = process.argv[2] || '.'
const BASE = process.argv[3] || 'http://127.0.0.1:4173'
let pass = 0, fail = 0
const ok = (cond, name) => { if (cond) { pass++; console.log('✓', name) } else { fail++; console.log('✗', name) } }
const browser = await chromium.launch({ channel: process.env.PW_CHANNEL || 'msedge' })
const c = await browser.newContext({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 3, isMobile: true, hasTouch: true, locale: 'zh-CN' })
const p = await c.newPage()
const errs = []
p.on('pageerror', (e) => errs.push(String(e)))

await p.goto(BASE + '/')
const href = await p.locator('link[rel="manifest"]').getAttribute('href')
ok(href === '/manifest.webmanifest', '页面声明了清单')
const m = await p.evaluate(async (h) => { const r = await fetch(h); return { type: r.headers.get('content-type'), j: await r.json() } }, href)
ok(m.j.display === 'standalone' && m.j.start_url === '/' && m.j.icons.some((i) => i.sizes === '512x512' && i.purpose === 'maskable'), `清单：独立窗口、从首页开始、有可裁剪的 512 图标（${m.type}）`)
const icons = await p.evaluate(async (list) => Promise.all(list.map(async (i) => {
  const r = await fetch(i.src)
  const b = await createImageBitmap(await r.blob())
  return `${i.src} ${b.width}x${b.height} ${i.sizes}`
})), m.j.icons)
ok(icons.every((s) => { const [, got, want] = s.split(' '); return got === want }), `图标都能打开、尺寸对（${icons.join('；')}）`)
const reg = await p.evaluate(async () => {
  if (!('serviceWorker' in navigator)) return 'none'
  const r = await Promise.race([navigator.serviceWorker.ready, new Promise((res) => setTimeout(() => res(null), 8000))])
  return r ? r.active?.scriptURL ?? 'pending' : 'timeout'
})
ok(/\/sw\.js$/.test(reg), `后台脚本注册了（${reg}）`)
// 断网后打开：显示说明页，不是浏览器的错误页
await p.waitForTimeout(800)
await c.setOffline(true)
await p.goto(BASE + '/?page=trade').catch(() => {})
const body = await p.locator('body').innerText().catch(() => '')
ok(/连不上服务器/.test(body), `断网时打开显示"连不上服务器"（${body.slice(0, 30).replace(/\s+/g, ' ')}）`)
await p.screenshot({ path: `${OUT}/pwa-offline.png` })
await c.setOffline(false)
await p.goto(BASE + '/')
ok(!(await p.locator('body').innerText()).includes('连不上服务器'), '恢复网络后正常打开')
// 接口不经过后台脚本：断网时接口直接失败，不会拿到旧数据
const swCached = await p.evaluate(async () => (await caches.keys()).join(','))
ok(swCached === 'qs-offline-v1', `只缓存了说明页这一份（${swCached}）`)
ok(errs.length === 0, `没有页面报错（${errs.join(' | ').slice(0, 200)}）`)
await browser.close()
console.log(`\n${pass} 通过，${fail} 失败`)
process.exit(fail ? 1 : 0)
