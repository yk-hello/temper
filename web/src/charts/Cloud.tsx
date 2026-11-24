// 概率云的画法（3.19）：几千条路径叠成灰阶密度，亮 = 路径多。右边一条"最坏情况刻度"，
// 写清楚亏到哪里自动平仓、模拟里最差到哪。不发光，不用颜色铺底，红色只留给亏损那条线。
import { numLocale } from '../lib/tz'
import { useMemo } from 'react'
import { useSurface } from '../lib/gfx'
import { tr } from '../lib/i18n'
import type { CloudResult } from '../lib/cloud'
import { C, dirColors } from './scale'

export function Cloud({ cloud, alloc, maxLoss, days, priv }: { cloud: CloudResult | null; alloc: number; maxLoss: number; days: number; priv: boolean }) {
  // 密度只在云变的时候算一次：离屏画成图片，拖"分配金额"时只换刻度上的字
  const img = useMemo(() => {
    if (!cloud) return null
    const c = document.createElement('canvas')
    c.width = cloud.cols; c.height = cloud.rows
    const ctx = c.getContext('2d')!
    const data = ctx.createImageData(cloud.cols, cloud.rows)
    let mx = 1
    const sorted = Array.from(cloud.density).filter((x) => x > 0).sort((a, b) => a - b)
    if (sorted.length) mx = sorted[Math.floor(sorted.length * 0.985)] || 1
    for (let i = 0; i < cloud.density.length; i++) {
      const k = Math.min(1, cloud.density[i] / mx)
      const v = Math.round(Math.pow(k, 0.55) * 225)
      data.data[i * 4] = v; data.data[i * 4 + 1] = v; data.data[i * 4 + 2] = v + (v > 0 ? 3 : 0); data.data[i * 4 + 3] = 255
    }
    ctx.putImageData(data, 0, 0)
    return c
  }, [cloud])

  const { ref } = useSurface((g) => {
    if (!cloud || !img) return
    const money = (x: number) => priv ? '••••' : (x >= 0 ? '+' : '−') + Math.abs(x * alloc).toLocaleString(numLocale(), { maximumFractionDigits: 0 })
    const scaleW = Math.min(164, g.w * 0.36)
    const plot = { x0: 0, y0: 8, x1: g.w - scaleW, y1: g.h - 22 }
    const yOf = (v: number) => plot.y0 + ((cloud.hi - v) / (cloud.hi - cloud.lo)) * (plot.y1 - plot.y0)
    const xOf = (c: number) => plot.x0 + ((c + 0.5) / cloud.cols) * (plot.x1 - plot.x0)
    const { up, down } = dirColors()
    // 密度
    const ctx = g.ctx
    ctx.imageSmoothingEnabled = true
    ctx.imageSmoothingQuality = 'high'
    ctx.drawImage(img, g.X(plot.x0), g.Y(plot.y0), g.X(plot.x1) - g.X(plot.x0), g.Y(plot.y1) - g.Y(plot.y0))
    // 零线和最大亏损线
    g.hline(yOf(0), plot.x0, plot.x1, C.rule3)
    const stopY = yOf(-maxLoss)
    g.hline(stopY, plot.x0, plot.x1, down)
    // 5%、50%、95% 分位线
    const line = (arr: Float32Array, color: string, w: number) => {
      ctx.beginPath()
      for (let c = 0; c < cloud.cols; c++) {
        const x = g.X(xOf(c)), y = g.Y(yOf(arr[c]))
        if (c) ctx.lineTo(x, y); else ctx.moveTo(x, y)
      }
      ctx.lineWidth = g.px(w); ctx.strokeStyle = color; ctx.stroke()
    }
    line(cloud.p95, 'rgba(0,0,0,0.75)', 3)
    line(cloud.p5, 'rgba(0,0,0,0.75)', 3)
    line(cloud.p95, C.t2, 1)
    line(cloud.p5, C.t2, 1)
    line(cloud.p50, '#000', 3)
    line(cloud.p50, C.white, 1)
    // 时间轴
    g.hline(plot.y1, plot.x0, plot.x1, C.rule2)
    for (let i = 0; i <= 3; i++) {
      const x = plot.x0 + (i / 3) * (plot.x1 - plot.x0)
      g.vline(Math.min(x, plot.x1 - 1), plot.y1, plot.y1 + 5, C.rule3)
      const label = i === 0 ? tr('cloud.now') : tr('cloud.dayN', { n: Math.round((days * i) / 3) })
      g.text(label, i === 3 ? plot.x1 : x, plot.y1 + 17, { size: 11, color: C.t3, align: i === 0 ? 'left' : i === 3 ? 'right' : 'center' })
    }
    // 右边的最坏情况刻度
    const ax = plot.x1
    g.vline(ax, plot.y0, plot.y1, C.rule2)
    // 先从上往下排、互相让开；排到底放不下时再从下往上挤回来，最后一个标签不被裁掉
    // （11-17 走查：最大亏损线靠近 5% 分位时，"−1,000 亏到这里自动平仓"压在"−953"上、还被裁掉一半）
    const tags: { v: number; label: string; color: string; y: number }[] = [
      { v: cloud.p95[cloud.cols - 1], label: tr('cloud.p95'), color: up, y: 0 },
      { v: cloud.p50[cloud.cols - 1], label: tr('cloud.p50'), color: C.t1, y: 0 },
      { v: cloud.p5[cloud.cols - 1], label: tr('cloud.p5'), color: C.t2, y: 0 },
      { v: -maxLoss, label: tr('cloud.stop'), color: down, y: 0 },
    ]
    if (cloud.worst < -maxLoss - 0.0005) tags.push({ v: cloud.worst, label: tr('cloud.worst'), color: down, y: 0 })
    const gap = 30, top = plot.y0 + 8, bottom = g.h - 16
    let prev = top - gap
    for (const t of tags) { t.y = Math.max(prev + gap, yOf(t.v)); prev = t.y }
    let lim = bottom
    for (let i = tags.length - 1; i >= 0; i--) { tags[i].y = Math.min(tags[i].y, lim); lim = tags[i].y - gap }
    for (const t of tags) {
      g.hline(yOf(t.v), ax, ax + 6, t.color)
      g.text(money(t.v), ax + 10, t.y - 2, { size: 12, color: t.color, weight: 500, baseline: 'alphabetic' })
      g.text(t.label, ax + 10, t.y + 12, { size: 11, color: C.t3, baseline: 'alphabetic' })
    }
  }, [cloud, img, alloc, maxLoss, days, priv])

  return <div className="cloud"><canvas ref={ref} /></div>
}
