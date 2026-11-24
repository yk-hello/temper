// 显卡热力图：把"时间 × 价格"的挂单矩阵当成一张纹理，片元着色器负责取样和调色。
// 上百万个格子也只是一张纹理，缩放平移只改几个数，所以丝滑。
//
// 调色（D33：纯黑底配白色亮度）：
// - 黑点、白点由画面里实际的数据分布决定（自动对比度），放大、平移后依然层次分明
// - 弱挂单是带一点冷色的石墨灰，越厚越接近纯白，像科学成像而不是电视雪花
// - 放大到每档价格占 5 个物理像素以上时，格子之间留 1 个物理像素的暗刻线，像精密网格

const VS = `#version 300 es
in vec2 a;
out vec2 uv;
void main() { uv = a * 0.5 + 0.5; gl_Position = vec4(a, 0.0, 1.0); }`

const FS = `#version 300 es
precision highp float;
in vec2 uv;
out vec4 o;
uniform sampler2D tex;
uniform vec4 view;      // 可见范围（纹理坐标）：u0, v0, u1, v1
uniform vec3 tone;      // 黑点、白点（0–1）、伽马
uniform vec2 texSize;
uniform float tint;     // 冷色程度 0–1
uniform float lodMode;  // 1 = 只按纵向定多级纹理（实时频谱）
uniform float ramp;     // 1 = 青色配色（亮度仍然单调增加：黑、深青、亮青、白）
void main() {
  vec2 t = vec2(mix(view.x, view.z, uv.x), mix(view.y, view.w, uv.y));
  if (t.x < 0.0 || t.x > 1.0 || t.y < 0.0 || t.y > 1.0) { o = vec4(0.0, 0.0, 0.0, 1.0); return; }
  vec2 g = t * texSize;
  vec2 fp = fwidth(g);                         // 每个物理像素跨过多少格
  // 放大时取格子中心（边缘锐利）；缩小时用多级纹理平均（平移不闪）
  vec2 st = mix((floor(g) + 0.5) / texSize, t, step(vec2(1.0), fp));
  float v = lodMode > 0.5 ? textureLod(tex, st, log2(max(fp.y, 1.0))).r : textureGrad(tex, st, dFdx(t), dFdy(t)).r;
  float l = clamp((v - tone.x) / max(tone.y - tone.x, 1e-3), 0.0, 1.0);
  l = pow(l, tone.z);
  // 格间刻线：每档价格高度 ≥ 5 物理像素时，每档最下面 1 个物理像素压暗
  float rowPx = 1.0 / max(fp.y, 1e-4);
  if (rowPx >= 5.0) {
    float inRow = fract(g.y) * rowPx;          // 本格内从下往上第几个像素
    if (inRow < 1.0) l *= 0.35;
  }
  vec3 cool = vec3(0.78, 0.86, 1.0);
  vec3 c = l * mix(mix(vec3(1.0), cool, tint), vec3(1.0), smoothstep(0.35, 0.95, l));
  if (ramp > 0.5) {
    vec3 a = vec3(0.0, 0.13, 0.17), b = vec3(0.03, 0.56, 0.62), d = vec3(0.58, 0.96, 0.96);
    c = l < 0.3 ? mix(vec3(0.0), a, l / 0.3) : l < 0.68 ? mix(a, b, (l - 0.3) / 0.38) : l < 0.92 ? mix(b, d, (l - 0.68) / 0.24) : mix(d, vec3(1.0), (l - 0.92) / 0.08);
  }
  o = vec4(c, 1.0);
}`

export interface HeatView { u0: number; v0: number; u1: number; v1: number }
export interface Tone { black: number; white: number; gamma: number; tint: number; ramp?: number }

export interface HeatLayer { tex: WebGLTexture; cols: number; rows: number }

export class HeatGL {
  private gl: WebGL2RenderingContext
  private prog: WebGLProgram
  private tex: WebGLTexture
  private u: Record<string, WebGLUniformLocation | null> = {}
  private cols = 0
  private rows = 0

  constructor(canvas: HTMLCanvasElement) {
    const gl = canvas.getContext('webgl2', { antialias: false, alpha: false, premultipliedAlpha: false, preserveDrawingBuffer: true })
    if (!gl) throw new Error('WebGL2 不可用')
    this.gl = gl
    const sh = (type: number, src: string) => {
      const s = gl.createShader(type)!
      gl.shaderSource(s, src); gl.compileShader(s)
      if (!gl.getShaderParameter(s, gl.COMPILE_STATUS)) throw new Error(gl.getShaderInfoLog(s) ?? 'shader')
      return s
    }
    const p = gl.createProgram()!
    gl.attachShader(p, sh(gl.VERTEX_SHADER, VS)); gl.attachShader(p, sh(gl.FRAGMENT_SHADER, FS))
    gl.linkProgram(p)
    if (!gl.getProgramParameter(p, gl.LINK_STATUS)) throw new Error(gl.getProgramInfoLog(p) ?? 'link')
    this.prog = p
    const buf = gl.createBuffer()
    gl.bindBuffer(gl.ARRAY_BUFFER, buf)
    gl.bufferData(gl.ARRAY_BUFFER, new Float32Array([-1, -1, 1, -1, -1, 1, 1, 1]), gl.STATIC_DRAW)
    const loc = gl.getAttribLocation(p, 'a')
    gl.enableVertexAttribArray(loc)
    gl.vertexAttribPointer(loc, 2, gl.FLOAT, false, 0, 0)
    this.tex = gl.createTexture()!
    gl.bindTexture(gl.TEXTURE_2D, this.tex)
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR_MIPMAP_LINEAR)
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.NEAREST)
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE)
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE)
    for (const n of ['view', 'tone', 'texSize', 'tint', 'lodMode', 'ramp']) this.u[n] = gl.getUniformLocation(p, n)
  }

  /** 再建一层（实时频谱：历史一层，最近几分钟一层），和默认那层共用同一个程序 */
  layer(): HeatLayer {
    const gl = this.gl
    const tex = gl.createTexture()!
    gl.bindTexture(gl.TEXTURE_2D, tex)
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR_MIPMAP_LINEAR)
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.NEAREST)
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE)
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE)
    return { tex, cols: 0, rows: 0 }
  }

  setLayer(l: HeatLayer, cols: number, rows: number, cells: Uint8Array) {
    const gl = this.gl
    l.cols = cols; l.rows = rows
    gl.bindTexture(gl.TEXTURE_2D, l.tex)
    gl.pixelStorei(gl.UNPACK_ALIGNMENT, 1)
    gl.texImage2D(gl.TEXTURE_2D, 0, gl.R8, cols, rows, 0, gl.RED, gl.UNSIGNED_BYTE, cells)
    gl.generateMipmap(gl.TEXTURE_2D)
  }

  /** 只改一列（每 100 毫秒一次）：不重传整张纹理 */
  setColumn(l: HeatLayer, col: number, cells: Uint8Array) {
    const gl = this.gl
    gl.bindTexture(gl.TEXTURE_2D, l.tex)
    gl.pixelStorei(gl.UNPACK_ALIGNMENT, 1)
    gl.texSubImage2D(gl.TEXTURE_2D, 0, col, 0, 1, l.rows, gl.RED, gl.UNSIGNED_BYTE, cells)
  }

  /** 缩小显示时用的多级纹理：贵，半秒一次就够 */
  mips(l: HeatLayer) {
    const gl = this.gl
    gl.bindTexture(gl.TEXTURE_2D, l.tex)
    gl.generateMipmap(gl.TEXTURE_2D)
  }

  clear() {
    const gl = this.gl
    gl.clearColor(0, 0, 0, 1)
    gl.clear(gl.COLOR_BUFFER_BIT)
  }

  /** 把一层画到 (x, y, w, h) 物理像素区域。不清屏，所以能叠好几层 */
  draw(l: HeatLayer, H: number, x: number, y: number, w: number, h: number, view: HeatView, tone: Tone) {
    if (!l.cols || !l.rows || w <= 0 || h <= 0) return
    const gl = this.gl
    gl.bindTexture(gl.TEXTURE_2D, l.tex)
    gl.viewport(x, H - y - h, w, h)
    gl.useProgram(this.prog)
    gl.uniform4f(this.u.view, view.u0, view.v0, view.u1, view.v1)
    gl.uniform3f(this.u.tone, tone.black, tone.white, tone.gamma)
    gl.uniform2f(this.u.texSize, l.cols, l.rows)
    gl.uniform1f(this.u.tint, tone.tint)
    gl.uniform1f(this.u.ramp, tone.ramp ?? 0)
    gl.uniform1f(this.u.lodMode, 1)
    gl.drawArrays(gl.TRIANGLE_STRIP, 0, 4)
  }

  /** cells：cols × rows，行优先（第 0 行 = 最低价） */
  setData(cols: number, rows: number, cells: Uint8Array) {
    const gl = this.gl
    this.cols = cols; this.rows = rows
    gl.bindTexture(gl.TEXTURE_2D, this.tex)
    gl.pixelStorei(gl.UNPACK_ALIGNMENT, 1)
    gl.texImage2D(gl.TEXTURE_2D, 0, gl.R8, cols, rows, 0, gl.RED, gl.UNSIGNED_BYTE, cells)
    gl.generateMipmap(gl.TEXTURE_2D)
  }

  /** 只画在 (x, y, w, h) 这块物理像素区域里（左上角为原点）；H = 画布物理高度 */
  render(H: number, x: number, y: number, w: number, h: number, view: HeatView, tone: Tone) {
    const gl = this.gl
    gl.clearColor(0, 0, 0, 1)
    gl.clear(gl.COLOR_BUFFER_BIT)
    gl.bindTexture(gl.TEXTURE_2D, this.tex)
    gl.viewport(x, H - y - h, w, h)
    gl.useProgram(this.prog)
    gl.uniform4f(this.u.view, view.u0, view.v0, view.u1, view.v1)
    gl.uniform3f(this.u.tone, tone.black, tone.white, tone.gamma)
    gl.uniform2f(this.u.texSize, this.cols, this.rows)
    gl.uniform1f(this.u.tint, tone.tint)
    gl.uniform1f(this.u.ramp, tone.ramp ?? 0)
    gl.uniform1f(this.u.lodMode, 0)
    gl.drawArrays(gl.TRIANGLE_STRIP, 0, 4)
  }
}

/** 自动对比度：在可见区域里均匀取样，黑点取中位数附近，白点取最亮的千分之几。 */
export function autoTone(cells: Uint8Array, cols: number, rows: number, c0: number, c1: number, r0: number, r1: number, blackQ = 0.04, whiteQ = 0.997, skipZero = false): { black: number; white: number } {
  c0 = Math.max(0, Math.floor(c0)); c1 = Math.min(cols - 1, Math.ceil(c1))
  r0 = Math.max(0, Math.floor(r0)); r1 = Math.min(rows - 1, Math.ceil(r1))
  const hist = new Uint32Array(256)
  const sc = Math.max(1, Math.floor((c1 - c0 + 1) / 160)), sr = Math.max(1, Math.floor((r1 - r0 + 1) / 160))
  let n = 0
  for (let y = r0; y <= r1; y += sr) for (let x = c0; x <= c1; x += sc) { hist[cells[y * cols + x]]++; n++ }
  if (!n) return { black: 0.3, white: 0.95 }
  const pct = (q: number) => {
    let acc = 0
    for (let i = 0; i < 256; i++) { acc += hist[i]; if (acc >= q * n) return i / 255 }
    return 1
  }
  if (skipZero) { n -= hist[0]; hist[0] = 0; if (n <= 0) return { black: 0.3, white: 0.95 } }
  const black = pct(blackQ)
  const white = Math.max(black + 0.1, pct(whiteQ))
  return { black, white }
}
