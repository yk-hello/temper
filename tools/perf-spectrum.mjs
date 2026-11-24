// 频谱性能测试：在 1080p 125% 的笔记本配置下连续滚轮缩放、拖动 4 秒，统计每帧重画耗时和实际帧间隔。
// 用法：node tools/perf-spectrum.mjs <网址，需带 ?page=showroom&perf=1>
import { chromium } from 'playwright'
const url = process.argv[2]
const browser = await chromium.launch({ args: ['--enable-webgl', '--use-gl=angle', '--use-angle=swiftshader', '--ignore-gpu-blocklist'] })
const page = await (await browser.newContext({ viewport: { width: 1536, height: 730 }, deviceScaleFactor: 1.25 })).newPage()
await page.goto(url, { waitUntil: 'networkidle' })
const load = await page.evaluate(() => performance.getEntriesByType('navigation')[0]?.duration)
const el = await page.$('.sr-spectrum .display-body')
await el.scrollIntoViewIfNeeded()
await page.waitForTimeout(800)
const box = await el.boundingBox()
await page.evaluate(() => {
  const w = window
  w.__frames = []
  let last = performance.now()
  const f = (t) => { w.__frames.push(t - last); last = t; if (w.__frames.length < 100000) requestAnimationFrame(f) }
  requestAnimationFrame(f)
  w.__qsPerf = {}
})
const cx = box.x + box.width * 0.5, cy = box.y + box.height * 0.45
await page.mouse.move(cx, cy)
const t0 = Date.now()
let i = 0
while (Date.now() - t0 < 2000) { await page.mouse.wheel(0, i++ % 20 < 10 ? -60 : 60); await page.waitForTimeout(16) }
await page.mouse.down()
for (let k = 0; k < 60; k++) { await page.mouse.move(cx - k * 4, cy + Math.sin(k / 6) * 30); await page.waitForTimeout(16) }
await page.mouse.up()
await page.waitForTimeout(600)
const r = await page.evaluate(() => {
  const q = (a, p) => { const s = [...a].sort((x, y) => x - y); return s.length ? s[Math.min(s.length - 1, Math.floor(p * s.length))] : 0 }
  const paint = window.__qsPerf.spectrum || [], fr = window.__frames.slice(5)
  return { paints: paint.length, paintP50: q(paint, 0.5), paintP95: q(paint, 0.95), paintMax: Math.max(...paint),
    frames: fr.length, frameP50: q(fr, 0.5), frameP95: q(fr, 0.95), slow: fr.filter((x) => x > 33).length }
})
console.log('页面加载', Math.round(load), 'ms')
console.log(`重画 ${r.paints} 次：中位 ${r.paintP50.toFixed(2)} ms，95% ${r.paintP95.toFixed(2)} ms，最慢 ${r.paintMax.toFixed(2)} ms`)
console.log(`帧间隔 ${r.frames} 帧：中位 ${r.frameP50.toFixed(1)} ms，95% ${r.frameP95.toFixed(1)} ms，超过 33 ms 的 ${r.slow} 帧`)
await browser.close()
