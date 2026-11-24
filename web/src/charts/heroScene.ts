// 首页和行情"地形"页共用的 3D 场景：数据铺满、四周压平、镜头和雾。
// 这两处都是看气势用的（读数看频谱），人只能看到铺满屏幕、看不到边的山（用户 2026-11-09）。
import { H_SCALE, toHeights, Z_SPAN, type Terrain } from './terrain'
import { dirColors } from './scale'
import type { HeatHistory } from '../lib/heat'

export const HS = H_SCALE * 1.15 // 山比原来的地形高一点：只管好看
const hexRgb = (s: string) => [parseInt(s.slice(1, 3), 16) / 255, parseInt(s.slice(3, 5), 16) / 255, parseInt(s.slice(5, 7), 16) / 255]

/** 地形只铺在真正有数据的地方。两小时里只录到一段（刚启动、电脑睡过）时，没数据的时间是一片黑。
 *  没录到的时间去掉，把有数据的部分拉满；价格范围也收到挂单集中的地方，个别跳变不把范围撑大。
 *  数据本来就满的时候原样返回，鼠标读数还是原始值 */
export function fitToData(d: HeatHistory, c: Uint8Array): { d: HeatHistory; c: Uint8Array } {
  const { cols, rows } = d
  // 只有比中位数厚的挂单才会隆起成山（toHeights 把薄的压成 0），价格范围按这些算
  const hist = new Uint32Array(256)
  let nz = 0
  for (let i = 0; i < c.length; i++) if (c[i] > 0) { hist[c[i]]++; nz++ }
  let med = 1
  for (let v = 1, a = 0; v < 256; v++) { a += hist[v]; if (a >= nz / 2) { med = v; break } }
  const has: number[] = []
  const rowN = new Uint32Array(rows)
  let total = 0
  for (let q = 0; q < cols; q++) {
    let any = false
    for (let r = 0; r < rows; r++) {
      const v = c[r * cols + q]
      if (v > 0) any = true
      if (v > med) { rowN[r]++; total++ }
    }
    if (any) has.push(q)
  }
  if (has.length < 2 || !total) return { d, c }
  let lo = 0, hi = rows - 1, acc = 0
  while (lo < hi && acc + rowN[lo] < total * .02) acc += rowN[lo++]
  acc = 0
  while (hi > lo && acc + rowN[hi] < total * .02) acc += rowN[hi--]
  if (hi - lo < rows * .2) { // 太窄就是一条线，留够高度
    const pad = Math.ceil((rows * .2 - (hi - lo)) / 2)
    lo = Math.max(0, lo - pad); hi = Math.min(rows - 1, hi + pad)
  }
  // 数据基本完整就不动，真实行情平时都走这里：画面和鼠标读数都是原始值
  if (has.length >= cols * .98 && lo + (rows - 1 - hi) < rows * .3) return { d, c }
  // 第 tc 列对应有数据的列表里的第几个（可以是小数）
  const srcOf = (tc: number) => (tc / (cols - 1)) * (has.length - 1)
  const out = new Uint8Array(cols * rows)
  for (let tc = 0; tc < cols; tc++) {
    const s = srcOf(tc), i0 = Math.min(has.length - 2, Math.floor(s)), fx = s - i0
    const c0 = has[i0], c1 = has[i0 + 1]
    for (let tr = 0; tr < rows; tr++) {
      const sr = lo + (tr / (rows - 1)) * (hi - lo)
      const r0 = Math.min(rows - 1, Math.floor(sr)), r1 = Math.min(rows - 1, r0 + 1), fy = sr - r0
      const a = c[r0 * cols + c0] + (c[r1 * cols + c0] - c[r0 * cols + c0]) * fy
      const b = c[r0 * cols + c1] + (c[r1 * cols + c1] - c[r0 * cols + c1]) * fy
      out[tr * cols + tc] = Math.round(a + (b - a) * fx)
    }
  }
  const mids = new Array<number>(cols)
  const shift = d.shift ? new Array<number>(cols) : undefined
  for (let tc = 0; tc < cols; tc++) {
    if (shift && d.shift) { const s = srcOf(tc), i0 = Math.min(has.length - 2, Math.floor(s)); shift[tc] = d.shift[has[s - i0 > 0.5 ? i0 + 1 : i0]] }
  }
  for (let tc = 0; tc < cols; tc++) {
    const s = srcOf(tc), i0 = Math.min(has.length - 2, Math.floor(s)), fx = s - i0
    const m0 = d.mids[has[i0]], m1 = d.mids[has[i0 + 1]]
    mids[tc] = m0 > 0 && m1 > 0 ? m0 + (m1 - m0) * fx : m0 > 0 ? m0 : m1
  }
  // 成交点：时间换成新的列号。下面按 (t - t0) / dt 算列，所以 t0 = 0、dt = 1
  const trades = (d.trades ?? []).flatMap((x) => {
    const col = (x[0] - d.t0) / d.dt
    let a = 0, z = has.length - 1
    if (col < has[a] || col > has[z]) return []
    while (z - a > 1) { const m = (a + z) >> 1; if (has[m] <= col) a = m; else z = m }
    const pos = a + (col - has[a]) / Math.max(1, has[z] - has[a])
    return [[(pos / (has.length - 1)) * (cols - 1), x[1], x[2], x[3]] as typeof x]
  })
  return { d: { ...d, t0: 0, dt: 1, p0: d.p0 + lo * d.step, step: (d.step * (hi - lo)) / (rows - 1), mids, trades, shift }, c: out }
}

export interface Scene {
  d: HeatHistory // 拉伸以后的（鼠标读数按它算）
  c: Uint8Array
  heights: Float32Array
  line: Float32Array
  dots: Float32Array
  price: number
}

/** 把一段历史变成可以画的场景：拉伸铺满，四周高度平滑降到零（屏幕里看不到切面），价格线跳变不拉长线 */
export function buildScene(d0: HeatHistory, c0: Uint8Array, maxDots = 1600): Scene {
  const { d, c } = fitToData(d0, c0)
  const { h: hs } = toHeights(c, d.cols, d.rows)
  // 四周的高度平滑降到零，不留切面（用户："屏幕内不要看到前后边界"）。右边是"现在"，只收一点
  const edge = (i: number, n: number, k = .07) => { const e = Math.min(i, n - 1 - i) / ((n - 1) * k); return e >= 1 ? 1 : e * e * (3 - 2 * e) }
  for (let r = 0; r < d.rows; r++) for (let q = 0; q < d.cols; q++) hs[r * d.cols + q] *= edge(r, d.rows) * edge(q, d.cols, q > d.cols / 2 ? .015 : .07)
  const rowOf = (p: number) => (p - d.p0) / d.step
  // 拼接过的数据每列有价格平移（后端 hero.go）：画的时候先减掉，读数时再加回来
  const sh = (i: number) => d.shift?.[Math.max(0, Math.min(d.cols - 1, Math.round(i)))] ?? 0
  const X = (i: number) => -1 + (2 * i) / (d.cols - 1)
  const Z = (i: number) => Z_SPAN * (1 - (2 * i) / (d.rows - 1))
  const hAt = (i: number, j: number) => hs[Math.max(0, Math.min(d.rows - 1, Math.round(j))) * d.cols + Math.max(0, Math.min(d.cols - 1, Math.round(i)))]
  const pts: number[] = []
  let eh = -1, price = 0, prevR = -1
  const maxJump = d.rows * .035 // 一列最多走这么远：价格跳变不拉一根长线，慢慢爬过去
  for (let i = 0; i < d.cols; i++) {
    const m = d.mids[i]
    if (!(m > 0)) continue
    let rr = rowOf(m - sh(i))
    if (rr < 0 || rr > d.rows - 1) continue
    if (prevR >= 0) rr = prevR + Math.max(-maxJump, Math.min(maxJump, rr - prevR))
    prevR = rr
    const g = hAt(i, rr)
    eh = eh < 0 ? g : eh + (g - eh) * 0.12
    pts.push(X(i), Math.max(eh, g) * HS + 0.022, Z(rr))
    price = m
  }
  const col = dirColors()
  const up = hexRgb(col.up), dn = hexRgb(col.down)
  const dots: number[] = []
  const tl = d.trades ?? []
  const stepT = Math.max(1, Math.floor(tl.length / maxDots))
  for (let i = 0; i < tl.length; i += stepT) {
    const [tt, p, usd, buy] = tl[i]
    const cc = (tt - d.t0) / d.dt, r = rowOf(p - sh(cc))
    if (cc < 0 || cc > d.cols - 1 || r < 0 || r > d.rows - 1) continue
    const k = buy ? up : dn
    dots.push(X(cc), hAt(cc, r) * HS + 0.016, Z(r), Math.min(8, Math.log10(Math.max(usd, 10)) * 0.9), k[0], k[1], k[2], 0.9)
  }
  return { d, c, heights: hs, line: new Float32Array(pts), dots: new Float32Array(dots), price }
}

export interface Cam { yaw: number; pitch: number; phase: number }

/** 镜头：低角度沿山脊慢慢走。
 *  这组数是算出来的：任何屏幕比例、镜头漂到任何位置，屏幕下边和左右都落在地形里（看不到前边界和侧边界），
 *  后边界交给雾。改之前先想清楚，不然边就露出来了 */
export function heroView(cam: Cam, asp: number) {
  // 带鱼屏不能再横着张开，张开就看到两边了
  const tanV0 = asp * .32 > .62 ? .62 / asp : .32
  // 竖屏（手机）：和横屏一样横着放（同一价位的挂单墙是一道道横向山脊，从近到远一层层排开，这是好看的来源），
  // 但屏幕窄，只看得到一小段时间：看的点往"现在"那头挪，挪到右边那条边刚好还被雾盖住为止，摆动也小一点
  if (asp < .8) {
    const yaw = cam.yaw + Math.sin(cam.phase) * .07
    const pitch = cam.pitch + Math.sin(cam.phase * .6 + 1) * .03
    const tgt = [.42 + Math.sin(cam.phase * .7) * .04, .05, -.05 - Math.cos(cam.phase * .5) * .03]
    return { yaw, pitch, dist: 1.35, tgt, tanV: tanV0, tanH: tanV0 * asp }
  }
  const yaw = cam.yaw + Math.sin(cam.phase) * .14
  const pitch = cam.pitch + Math.sin(cam.phase * .6 + 1) * .03
  const tgt = [Math.sin(cam.phase * .7) * .08, .05, -.05 - Math.cos(cam.phase * .5) * .03]
  const tanV = tanV0
  return { yaw, pitch, dist: 1.35, tgt, tanV, tanH: tanV * asp }
}

/** 拖动的范围有限：拖太远就把地形的边拖出来了 */
export const clampYaw = (y: number) => Math.max(.02, Math.min(.58, y))
export const clampPitch = (p: number) => Math.max(.5, Math.min(.95, p))

/** 雾在边界之前黑掉：屏幕里看到的边界都已经是黑的。返回想要的雾的终点，调用方慢慢跟，边界进出屏幕时不会闪 */
export const wantFog = (tr: Terrain, dist: number) => Math.max(dist * 1.12, Math.min(tr.edgeDist(), dist + 1.7))
