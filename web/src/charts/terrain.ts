// 流动性地形（可视化 7）：盘口深度随时间变成 3D 地形，用等高线和点云渲染。价格是一道亮线在里面穿行。
// 直接用 WebGL2，不加依赖。x 是时间（右边是现在），z 是价格（远处价高，和频谱图上下方向一致），y 是挂单金额。
const MESH_VS = `#version 300 es
in vec2 a; in float h;
uniform mat4 vp; uniform float hs; uniform float lvl;
out float vH; out float vR; out vec3 vP;
void main() { float hh = min(h, lvl); vec3 p = vec3(a.x, hh * hs, a.y); vH = hh; vR = h; vP = p; gl_Position = vp * vec4(p, 1.0); }`

const MESH_FS = `#version 300 es
precision highp float;
in float vH; in float vR; in vec3 vP;
uniform vec3 eye; uniform float fog0; uniform float fog1; uniform float lvl;
uniform vec4 rip[6]; // x, z, 已过去的秒数, 强度
out vec4 o;
void main() {
  vec3 n = normalize(cross(dFdx(vP), dFdy(vP)));
  if (n.y < 0.0) n = -n;
  float light = clamp(dot(n, normalize(vec3(-0.55, 0.62, 0.45))), 0.0, 1.0);
  // 等高线：每 1/18 一条，每 3 条加粗一条
  float c = vH * 14.0;
  float f = abs(fract(c - 0.5) - 0.5) / max(fwidth(c), 1e-4);
  float line = (1.0 - min(f, 1.0)) * step(0.015, vH);
  float c3 = vH * 3.5;
  float f3 = abs(fract(c3 - 0.5) - 0.5) / max(fwidth(c3), 1e-4);
  float major = (1.0 - min(f3 * 0.7, 1.0)) * step(0.015, vH);
  // 陡坡上等高线挤在一起会糊成一片，淡一点，让山的明暗露出来
  float flat_ = smoothstep(0.03, 0.4, n.y);
  line *= 0.25 + 0.75 * flat_;
  major *= 0.45 + 0.55 * flat_;
  vec3 cool = vec3(0.36, 0.92, 0.92);
  vec3 base = mix(vec3(0.003, 0.012, 0.016), vec3(0.012, 0.15, 0.17), pow(vH, 0.95)) * (0.22 + 1.15 * pow(light, 1.4));
  vec3 col = base + cool * line * (0.07 + 0.75 * pow(vH, 0.7)) + cool * major * (0.12 + 1.1 * pow(vH, 0.7));
  col += vec3(0.82, 1.0, 1.0) * pow(vH, 4.2) * 0.8; // 只有山尖发白
  if (lvl < 0.999) { // 长出来的时候：先是一张测绘网格，正在升起的那一圈等高线最亮，还没长到的地方压暗
    vec2 gp = abs(fract(vP.xz * 16.0) - 0.5) / max(fwidth(vP.xz * 16.0), vec2(1e-4));
    float gridL = 1.0 - min(min(gp.x, gp.y), 1.0);
    col += cool * gridL * 0.16 * (1.0 - lvl);
    float f0 = abs(vR - lvl);
    float aa = max(fwidth(vR), 1e-4);
    col += vec3(0.7, 1.0, 1.0) * (1.0 - min(f0 / (aa * 1.6), 1.0)) * 1.4;
    col *= 1.0 - step(lvl, vR) * 0.55;
  }
  for (int i = 0; i < 6; i++) { // 大单成交：等高线荡开一圈
    if (rip[i].w <= 0.0) continue;
    float dd = length(vP.xz - rip[i].xy);
    float ra = rip[i].z * 0.5;
    float ring = exp(-pow((dd - ra) / (0.012 + ra * 0.05), 2.0)) * exp(-rip[i].z * 2.4) * rip[i].w;
    col += vec3(0.8, 1.0, 1.0) * ring * (0.25 + 0.75 * light);
  }
  float d = length(vP - eye);
  o = vec4(col * (1.0 - smoothstep(fog0, fog1, d)), 1.0);
}`

const PT_VS = `#version 300 es
in vec2 a; in float h;
uniform mat4 vp; uniform float hs; uniform float size; uniform float lvl;
out float vH; out vec3 vP;
void main() {
  float hh = min(h, lvl);
  vH = hh;
  vP = vec3(a.x, hh * hs + 0.004, a.y);
  gl_Position = vp * vec4(vP, 1.0);
  gl_PointSize = size * (0.7 + 1.1 * h) * clamp(2.2 / gl_Position.w, 0.5, 1.8);
}`

const PT_FS = `#version 300 es
precision highp float;
in float vH; in vec3 vP;
uniform vec3 eye; uniform float fog0; uniform float fog1;
out vec4 o;
void main() {
  float r = length(gl_PointCoord - 0.5) * 2.0;
  float a = smoothstep(1.0, 0.25, r) * smoothstep(0.06, 0.6, vH) * (1.0 - smoothstep(fog0, fog1, length(vP - eye)));
  o = vec4(mix(vec3(0.3, 0.92, 0.94), vec3(1.0), smoothstep(0.45, 0.9, vH)), a * 0.85);
}`

const LN_VS = `#version 300 es
in vec3 p;
uniform mat4 vp; uniform vec2 off;
out vec3 vP;
void main() { vP = p; gl_Position = vp * vec4(p, 1.0); gl_Position.xy += off * gl_Position.w; }`

const LN_FS = `#version 300 es
precision highp float;
uniform vec4 color; uniform vec3 eye; uniform float fog0; uniform float fog1;
in vec3 vP;
out vec4 o;
void main() { o = vec4(color.rgb, color.a * (1.0 - smoothstep(fog0, fog1, length(vP - eye)))); }`

const DOT_VS = `#version 300 es
in vec4 p; in vec4 c;
uniform mat4 vp; uniform float scale;
out vec4 vC; out vec3 vP;
void main() { vC = c; vP = p.xyz; gl_Position = vp * vec4(p.xyz, 1.0); gl_PointSize = p.w * scale; }`

const DOT_FS = `#version 300 es
precision highp float;
in vec4 vC; in vec3 vP;
uniform vec3 eye; uniform float fog0; uniform float fog1;
out vec4 o;
void main() {
  float r = length(gl_PointCoord - 0.5) * 2.0;
  o = vec4(vC.rgb, vC.a * smoothstep(1.0, 0.55, r) * (1.0 - smoothstep(fog0, fog1, length(vP - eye))));
}`

type M4 = Float32Array

function perspective(fovy: number, aspect: number, near: number, far: number): M4 {
  const f = 1 / Math.tan(fovy / 2), nf = 1 / (near - far)
  return new Float32Array([f / aspect, 0, 0, 0, 0, f, 0, 0, 0, 0, (far + near) * nf, -1, 0, 0, 2 * far * near * nf, 0])
}

function lookAt(e: number[], t: number[]): M4 {
  let zx = e[0] - t[0], zy = e[1] - t[1], zz = e[2] - t[2]
  let l = Math.hypot(zx, zy, zz); zx /= l; zy /= l; zz /= l
  let xx = zz, xz = -zx // up = (0,1,0) × z
  l = Math.hypot(xx, xz) || 1; xx /= l; xz /= l
  const yx = zy * xz, yy = zz * xx - zx * xz, yz = -zy * xx
  return new Float32Array([xx, yx, zx, 0, 0, yy, zy, 0, xz, yz, zz, 0, -(xx * e[0] + xz * e[2]), -(yx * e[0] + yy * e[1] + yz * e[2]), -(zx * e[0] + zy * e[1] + zz * e[2]), 1])
}

function mul(a: M4, b: M4): M4 {
  const o = new Float32Array(16)
  for (let c = 0; c < 4; c++) for (let r = 0; r < 4; r++) o[c * 4 + r] = a[r] * b[c * 4] + a[4 + r] * b[c * 4 + 1] + a[8 + r] * b[c * 4 + 2] + a[12 + r] * b[c * 4 + 3]
  return o
}

export interface TerrainData {
  cols: number
  rows: number
  heights: Float32Array // rows × cols，0–1
  line: Float32Array // 价格线 x, y(相对 hs 之前的高度), z
  segs?: number[] // 价格线分几段画：每段起点的顶点序号。没有数据的时段断开，不连长线
  dots: Float32Array // x, h, z, 大小, r, g, b, a
}

export const Z_SPAN = 0.68
export const H_SCALE = 0.36

export class Terrain {
  private gl: WebGL2RenderingContext
  private pMesh: WebGLProgram
  private pPt: WebGLProgram
  private pLn: WebGLProgram
  private pDot: WebGLProgram
  private vaoMesh: WebGLVertexArrayObject
  private vaoLn: WebGLVertexArrayObject
  private vaoDot: WebGLVertexArrayObject
  private bXZ: WebGLBuffer
  private bH: WebGLBuffer
  private bIdx: WebGLBuffer
  private bLn: WebGLBuffer
  private bDot: WebGLBuffer
  private indices = 0
  private verts = 0
  private lineN = 0
  private lineSegs: number[] = [0]
  private dotN = 0
  private cols = 0
  private rows = 0
  vp: M4 = new Float32Array(16)
  eye = [0, 1, 2]
  private ray: { f: number[]; r: number[]; u: number[]; tan: number; aspect: number } | null = null

  constructor(canvas: HTMLCanvasElement) {
    const gl = canvas.getContext('webgl2', { antialias: true, alpha: false, premultipliedAlpha: false })
    if (!gl) throw new Error('WebGL2 不可用')
    this.gl = gl
    const prog = (vs: string, fs: string) => {
      const sh = (type: number, src: string) => {
        const s = gl.createShader(type)!
        gl.shaderSource(s, src); gl.compileShader(s)
        if (!gl.getShaderParameter(s, gl.COMPILE_STATUS)) throw new Error(gl.getShaderInfoLog(s) ?? 'shader')
        return s
      }
      const p = gl.createProgram()!
      gl.attachShader(p, sh(gl.VERTEX_SHADER, vs)); gl.attachShader(p, sh(gl.FRAGMENT_SHADER, fs))
      gl.linkProgram(p)
      if (!gl.getProgramParameter(p, gl.LINK_STATUS)) throw new Error(gl.getProgramInfoLog(p) ?? 'link')
      return p
    }
    this.pMesh = prog(MESH_VS, MESH_FS)
    this.pPt = prog(PT_VS, PT_FS)
    this.pLn = prog(LN_VS, LN_FS)
    this.pDot = prog(DOT_VS, DOT_FS)
    this.vaoMesh = gl.createVertexArray()!
    this.vaoLn = gl.createVertexArray()!
    this.vaoDot = gl.createVertexArray()!
    this.bXZ = gl.createBuffer()!
    this.bH = gl.createBuffer()!
    this.bIdx = gl.createBuffer()!
    this.bLn = gl.createBuffer()!
    this.bDot = gl.createBuffer()!
    gl.clearColor(0, 0, 0, 1)
  }

  private grid(cols: number, rows: number) {
    const gl = this.gl
    const xz = new Float32Array(cols * rows * 2)
    for (let r = 0; r < rows; r++) for (let c = 0; c < cols; c++) {
      const i = (r * cols + c) * 2
      xz[i] = -1 + (2 * c) / Math.max(1, cols - 1)
      xz[i + 1] = Z_SPAN * (1 - (2 * r) / Math.max(1, rows - 1)) // 第 0 行价最低，离镜头最近
    }
    const idx = new Uint32Array((cols - 1) * (rows - 1) * 6)
    let k = 0
    for (let r = 0; r < rows - 1; r++) for (let c = 0; c < cols - 1; c++) {
      const a = r * cols + c, b = a + 1, d = a + cols, e = d + 1
      idx[k++] = a; idx[k++] = b; idx[k++] = d
      idx[k++] = b; idx[k++] = e; idx[k++] = d
    }
    gl.bindVertexArray(this.vaoMesh)
    gl.bindBuffer(gl.ARRAY_BUFFER, this.bXZ)
    gl.bufferData(gl.ARRAY_BUFFER, xz, gl.STATIC_DRAW)
    for (const p of [this.pMesh, this.pPt]) {
      const loc = gl.getAttribLocation(p, 'a')
      if (loc >= 0) { gl.enableVertexAttribArray(loc); gl.vertexAttribPointer(loc, 2, gl.FLOAT, false, 0, 0) }
    }
    gl.bindBuffer(gl.ARRAY_BUFFER, this.bH)
    gl.bufferData(gl.ARRAY_BUFFER, cols * rows * 4, gl.DYNAMIC_DRAW)
    for (const p of [this.pMesh, this.pPt]) {
      const loc = gl.getAttribLocation(p, 'h')
      if (loc >= 0) { gl.enableVertexAttribArray(loc); gl.vertexAttribPointer(loc, 1, gl.FLOAT, false, 0, 0) }
    }
    gl.bindBuffer(gl.ELEMENT_ARRAY_BUFFER, this.bIdx)
    gl.bufferData(gl.ELEMENT_ARRAY_BUFFER, idx, gl.STATIC_DRAW)
    gl.bindVertexArray(null)
    this.indices = idx.length
    this.verts = cols * rows
    this.cols = cols; this.rows = rows
  }

  setData(d: TerrainData) {
    const gl = this.gl
    if (d.cols !== this.cols || d.rows !== this.rows) this.grid(d.cols, d.rows)
    gl.bindBuffer(gl.ARRAY_BUFFER, this.bH)
    gl.bufferSubData(gl.ARRAY_BUFFER, 0, d.heights)
    gl.bindVertexArray(this.vaoLn)
    gl.bindBuffer(gl.ARRAY_BUFFER, this.bLn)
    gl.bufferData(gl.ARRAY_BUFFER, d.line, gl.DYNAMIC_DRAW)
    const lp = gl.getAttribLocation(this.pLn, 'p')
    gl.enableVertexAttribArray(lp); gl.vertexAttribPointer(lp, 3, gl.FLOAT, false, 0, 0)
    this.lineN = d.line.length / 3
    this.lineSegs = d.segs?.length ? d.segs : [0]
    gl.bindVertexArray(this.vaoDot)
    gl.bindBuffer(gl.ARRAY_BUFFER, this.bDot)
    gl.bufferData(gl.ARRAY_BUFFER, d.dots, gl.DYNAMIC_DRAW)
    const dp = gl.getAttribLocation(this.pDot, 'p'), dc = gl.getAttribLocation(this.pDot, 'c')
    gl.enableVertexAttribArray(dp); gl.vertexAttribPointer(dp, 4, gl.FLOAT, false, 32, 0)
    gl.enableVertexAttribArray(dc); gl.vertexAttribPointer(dc, 4, gl.FLOAT, false, 32, 16)
    this.dotN = d.dots.length / 8
    gl.bindVertexArray(null)
  }

  /** yaw、pitch 弧度；dist 镜头到中心的距离；target 看向哪；fov 视野 */
  setCamera(aspect: number, yaw: number, pitch: number, dist: number, target?: number[], fov = 0.72) {
    const t = target ?? [0, 0.05, 0.03]
    this.eye = [t[0] + dist * Math.cos(pitch) * Math.sin(yaw), t[1] + dist * Math.sin(pitch), t[2] + dist * Math.cos(pitch) * Math.cos(yaw)]
    this.vp = mul(perspective(fov, aspect, 0.05, 20), lookAt(this.eye, t))
    // 记下相机的方向，鼠标取点要用
    const e = this.eye
    let fx = t[0] - e[0], fy = t[1] - e[1], fz = t[2] - e[2]
    const fl = Math.hypot(fx, fy, fz); fx /= fl; fy /= fl; fz /= fl
    let rx = -fz, rz = fx
    const rl = Math.hypot(rx, rz) || 1; rx /= rl; rz /= rl
    const ux = -rz * fy, uy = rz * fx - rx * fz, uz = rx * fy
    this.ray = { f: [fx, fy, fz], r: [rx, 0, rz], u: [ux, uy, uz], tan: Math.tan(fov / 2), aspect }
  }

  /** 屏幕坐标（-1..1）对应地形上的哪一点：沿视线一步步走，碰到山就停。返回列和行 */
  pick(nx: number, ny: number, heights: Float32Array, cols: number, rows: number, hs = H_SCALE): { c: number; r: number } | null {
    const q = this.ray
    if (!q) return null
    const dx = q.f[0] + q.r[0] * nx * q.tan * q.aspect + q.u[0] * ny * q.tan
    const dy = q.f[1] + q.u[1] * ny * q.tan
    const dz = q.f[2] + q.r[2] * nx * q.tan * q.aspect + q.u[2] * ny * q.tan
    const e = this.eye
    for (let i = 8; i < 260; i++) {
      const t = i * 0.024
      const x = e[0] + dx * t, y = e[1] + dy * t, z = e[2] + dz * t
      if (y < 0) return null
      if (x < -1 || x > 1 || z < -Z_SPAN || z > Z_SPAN) continue
      const c = Math.round(((x + 1) / 2) * (cols - 1))
      const r = Math.round(((1 - z / Z_SPAN) / 2) * (rows - 1))
      if (y <= heights[r * cols + c] * hs) return { c, r }
    }
    return null
  }

  /** 世界坐标 → 屏幕（CSS 像素）。在镜头后面返回 null */
  project(x: number, y: number, z: number, w: number, h: number): [number, number] | null {
    const m = this.vp
    const cw = m[3] * x + m[7] * y + m[11] * z + m[15]
    if (cw <= 0.01) return null
    const cx = (m[0] * x + m[4] * y + m[8] * z + m[12]) / cw
    const cy = (m[1] * x + m[5] * y + m[9] * z + m[13]) / cw
    return [(cx * 0.5 + 0.5) * w, (0.5 - cy * 0.5) * h]
  }

  /** 地形的边界在屏幕里离镜头最近有多远。雾要在这个距离之前黑掉，才看不到边。屏幕里没有边时返回 Infinity */
  edgeDist(): number {
    const m = this.vp, e = this.eye
    let best = Infinity
    const test = (x: number, z: number) => {
      const cw = m[3] * x + m[11] * z + m[15]
      if (cw <= 0.01) return
      const cx = (m[0] * x + m[8] * z + m[12]) / cw, cy = (m[1] * x + m[9] * z + m[13]) / cw
      if (Math.abs(cx) > 1.02 || Math.abs(cy) > 1.02) return
      best = Math.min(best, Math.hypot(x - e[0], e[1], z - e[2]))
    }
    for (let i = 0; i <= 48; i++) {
      const k = -1 + i / 24
      test(k, Z_SPAN); test(k, -Z_SPAN); test(1, k * Z_SPAN); test(-1, k * Z_SPAN)
    }
    return best
  }

  /** 只更新高度（新旧快照之间过渡用） */
  setHeights(h: Float32Array) {
    const gl = this.gl
    gl.bindBuffer(gl.ARRAY_BUFFER, this.bH)
    gl.bufferSubData(gl.ARRAY_BUFFER, 0, h)
  }

  draw(W: number, H: number, dpr: number, dist: number, opt: { hs?: number; lvl?: number; lineK?: number; rip?: Float32Array; points?: boolean; dots?: boolean; fog?: [number, number]; lineCore?: number } = {}) {
    const hs = opt.hs ?? H_SCALE, lvl = opt.lvl ?? 1
    const rip = opt.rip ?? new Float32Array(24)
    const gl = this.gl
    gl.viewport(0, 0, W, H)
    gl.clear(gl.COLOR_BUFFER_BIT | gl.DEPTH_BUFFER_BIT)
    if (!this.indices) return
    gl.enable(gl.DEPTH_TEST)
    gl.depthFunc(gl.LEQUAL)
    gl.disable(gl.BLEND)
    gl.depthMask(true)
    // 地面和等高线
    gl.useProgram(this.pMesh)
    gl.uniformMatrix4fv(gl.getUniformLocation(this.pMesh, 'vp'), false, this.vp)
    gl.uniform1f(gl.getUniformLocation(this.pMesh, 'hs'), hs)
    gl.uniform1f(gl.getUniformLocation(this.pMesh, 'lvl'), lvl)
    gl.uniform4fv(gl.getUniformLocation(this.pMesh, 'rip'), rip)
    gl.uniform3fv(gl.getUniformLocation(this.pMesh, 'eye'), this.eye)
    const fog0 = opt.fog?.[0] ?? dist + 0.1, fog1 = opt.fog?.[1] ?? dist + 1.9
    gl.uniform1f(gl.getUniformLocation(this.pMesh, 'fog0'), fog0)
    gl.uniform1f(gl.getUniformLocation(this.pMesh, 'fog1'), fog1)
    const fogOn = (pr: WebGLProgram) => {
      gl.uniform3fv(gl.getUniformLocation(pr, 'eye'), this.eye)
      gl.uniform1f(gl.getUniformLocation(pr, 'fog0'), fog0)
      gl.uniform1f(gl.getUniformLocation(pr, 'fog1'), fog1)
    }
    gl.bindVertexArray(this.vaoMesh)
    gl.drawElements(gl.TRIANGLES, this.indices, gl.UNSIGNED_INT, 0)
    // 点云：叠加发光
    gl.enable(gl.BLEND)
    gl.blendFunc(gl.SRC_ALPHA, gl.ONE)
    gl.depthMask(false)
    gl.useProgram(this.pPt)
    gl.uniformMatrix4fv(gl.getUniformLocation(this.pPt, 'vp'), false, this.vp)
    fogOn(this.pPt)
    if (opt.points !== false) {
      gl.uniform1f(gl.getUniformLocation(this.pPt, 'hs'), hs)
      gl.uniform1f(gl.getUniformLocation(this.pPt, 'lvl'), lvl)
      gl.uniform1f(gl.getUniformLocation(this.pPt, 'size'), 1.5 * dpr)
      gl.bindVertexArray(this.vaoMesh)
      gl.drawArrays(gl.POINTS, 0, this.verts)
    }
    // 价格线：先不做深度判断画一遍淡的，被山挡住也看得见；再画实的
    gl.useProgram(this.pLn)
    gl.uniformMatrix4fv(gl.getUniformLocation(this.pLn, 'vp'), false, this.vp)
    fogOn(this.pLn)
    const uOff = gl.getUniformLocation(this.pLn, 'off'), uCol = gl.getUniformLocation(this.pLn, 'color')
    gl.bindVertexArray(this.vaoLn)
    const px = 2 / W, py = 2 / H
    const lineCount = Math.floor(this.lineN * (opt.lineK ?? 1))
    const strip = (dx: number, dy: number, a: number) => {
      gl.uniform2f(uOff, dx * px, dy * py)
      gl.uniform4f(uCol, 1, 1, 1, a)
      const sg = this.lineSegs
      for (let k = 0; k < sg.length; k++) {
        const b = Math.min(k + 1 < sg.length ? sg[k + 1] : this.lineN, lineCount)
        if (b - sg[k] >= 2) gl.drawArrays(gl.LINE_STRIP, sg[k], b - sg[k])
      }
    }
    gl.disable(gl.DEPTH_TEST)
    strip(0, 0, 0.3)
    gl.enable(gl.DEPTH_TEST)
    const w = Math.max(1, Math.round(dpr))
    for (let i = -2 * w; i <= 2 * w; i++) for (let j = -2 * w; j <= 2 * w; j++) {
      const rr = Math.hypot(i, j) / w
      if (rr > 2.2) continue
      strip(i, j, rr <= (opt.lineCore ?? 0.8) ? 1 : 0.14)
    }
    // 成交点和价格线末端
    gl.useProgram(this.pDot)
    gl.uniformMatrix4fv(gl.getUniformLocation(this.pDot, 'vp'), false, this.vp)
    fogOn(this.pDot)
    gl.uniform1f(gl.getUniformLocation(this.pDot, 'scale'), dpr)
    gl.bindVertexArray(this.vaoDot)
    if (opt.dots !== false) gl.drawArrays(gl.POINTS, 0, this.dotN)
    gl.bindVertexArray(null)
    gl.depthMask(true)
  }

  dispose() {
    this.gl.getExtension('WEBGL_lose_context')?.loseContext()
  }
}

/** 把后端的一张频谱矩阵变成高度：自动定黑点白点，价格方向轻微平滑，山看起来是连着的 */
export function toHeights(cells: Uint8Array, cols: number, rows: number): { h: Float32Array; black: number; white: number } {
  const hist = new Uint32Array(256)
  let n = 0
  for (let i = 0; i < cells.length; i++) if (cells[i] > 0) { hist[cells[i]]++; n++ }
  let black = 0, white = 255, acc = 0
  for (let v = 1; v < 256; v++) {
    acc += hist[v]
    if (black === 0 && acc >= n * 0.45) black = v
    if (acc >= n * 0.9985) { white = v; break }
  }
  if (white <= black) white = black + 1
  const raw = new Float32Array(cols * rows)
  for (let i = 0; i < raw.length; i++) {
    const v = cells[i]
    raw[i] = v <= black ? 0 : Math.pow(Math.min(1, (v - black) / (white - black)), 1.5)
  }
  // 墙在价格方向很窄，直接拉高是一堵堵立着的板。在价格方向摊开两遍、时间方向摊开一遍，才像山。
  const blurRows = (src: Float32Array) => {
    const out = new Float32Array(src.length)
    const w = [1, 4, 6, 4, 1]
    for (let r = 0; r < rows; r++) for (let c = 0; c < cols; c++) {
      let v = 0
      for (let k = -2; k <= 2; k++) v += src[Math.max(0, Math.min(rows - 1, r + k)) * cols + c] * w[k + 2]
      out[r * cols + c] = v / 16
    }
    return out
  }
  const once = blurRows(blurRows(raw))
  const h = new Float32Array(cols * rows)
  let top = 1e-3
  for (let r = 0; r < rows; r++) for (let c = 0; c < cols; c++) {
    const i = r * cols + c
    const lf = c > 0 ? once[i - 1] : once[i], rt = c + 1 < cols ? once[i + 1] : once[i]
    // 山顶保留一点原来的尖：不然最厚的墙被摊平，和普通的挂单差不多高
    h[i] = (once[i] * 2 + lf + rt) / 4 * 0.72 + raw[i] * 0.28
    if (h[i] > top) top = h[i]
  }
  // 按第 99.5% 那一档归一，不按最高的那一座：一堵特别厚的墙会把其他山都压矮、压暗（11-09 量过：最高 0.64，99.5% 才 0.45）
  const sorted = Float32Array.from(h).sort()
  top = Math.max(1e-3, Math.min(top, sorted[Math.floor((sorted.length - 1) * 0.995)]))
  for (let i = 0; i < h.length; i++) h[i] = Math.pow(Math.min(1, h[i] / top), 0.8)
  return { h, black, white }
}
