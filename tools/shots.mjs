// 多屏幕比例截图（D34 验收矩阵）。
// 用法：node tools/shots.mjs <网址> <输出目录> [设备id,逗号分隔|all] [--full] [--sel=CSS选择器] [--wait=毫秒]
// 每张图按物理像素保存，文件名 = 设备 id。同时检查横向溢出和页面报错。
import { chromium } from 'playwright'
import fs from 'node:fs'
import { DEVICES } from './devices.mjs'

const [url, out, ids = 'all', ...rest] = process.argv.slice(2)
const full = rest.includes('--full')
const sel = rest.find((r) => r.startsWith('--sel='))?.slice(6)
const wait = Number(rest.find((r) => r.startsWith('--wait='))?.slice(7) ?? 1000)
const list = ids === 'all' ? DEVICES : DEVICES.filter((d) => ids.split(',').includes(d.id))
fs.mkdirSync(out, { recursive: true })
const browser = await chromium.launch({ args: ['--enable-webgl', '--use-gl=angle', '--use-angle=swiftshader', '--ignore-gpu-blocklist'] })
for (const d of list) {
  const ctx = await browser.newContext({ viewport: { width: d.w, height: d.h }, deviceScaleFactor: d.dpr, isMobile: d.w < 500, hasTouch: d.w < 500 })
  const page = await ctx.newPage()
  const errs = []
  page.on('pageerror', (e) => errs.push(String(e)))
  page.on('console', (m) => { if (m.type() === 'error') errs.push(m.text()) })
  await page.goto(url, { waitUntil: 'networkidle' })
  await page.evaluate(() => document.fonts.ready)
  await page.waitForTimeout(wait)
  const f = `${out}/${d.id}.png`
  if (sel) {
    const el = await page.$(sel)
    if (el) { await el.scrollIntoViewIfNeeded(); await page.waitForTimeout(250); await el.screenshot({ path: f }) }
  } else await page.screenshot({ path: f, fullPage: full })
  const [overflow, vscroll] = await page.evaluate(() => [document.documentElement.scrollWidth - document.documentElement.clientWidth, document.documentElement.scrollHeight - innerHeight])
  console.log(`${d.id.padEnd(16)} ${String(d.w).padStart(4)}×${String(d.h).padEnd(4)} @${d.dpr}  ${overflow > 0 ? `横向溢出 ${overflow}px ` : ''}${vscroll > 0 ? `整页可上下滚动 ${vscroll}px ` : ''}${errs.length ? '报错：' + errs.join(' | ').slice(0, 200) : 'ok'}`)
  await ctx.close()
}
await browser.close()
