// 这台设备跑不跑得动首页的 3D 地形（D85 第 22 条）。高：完整；中：降精度；低：介绍页。
// 分两步：先看硬条件，再在开场里实测约 0.4 秒。结果记在这台设备上，下次不用再测。用户可以手动切换。
export type Tier = 'high' | 'mid' | 'low'

const KEY = 'qs.tier2', FORCE = 'qs.tierForce'

function renderer(): { name: string; soft: boolean } | null {
  try {
    const c = document.createElement('canvas')
    const gl = c.getContext('webgl2')
    if (!gl) return null
    const d = gl.getExtension('WEBGL_debug_renderer_info')
    const name = d ? String(gl.getParameter(d.UNMASKED_RENDERER_WEBGL)) : ''
    gl.getExtension('WEBGL_lose_context')?.loseContext()
    return { name, soft: /swiftshader|llvmpipe|software|basic render/i.test(name) }
  } catch { return null }
}

export function forcedTier(): Tier | null {
  try { const v = localStorage.getItem(FORCE); return v === 'high' || v === 'mid' || v === 'low' ? v : null } catch { return null }
}
export function forceTier(t: Tier | null) {
  try { if (t) localStorage.setItem(FORCE, t); else localStorage.removeItem(FORCE) } catch { /* 无痕模式 */ }
}

/** 不用测就能定的情况。返回 null 表示要实测。 */
export function quickTier(): Tier | null {
  const f = forcedTier()
  if (f) return f
  const r = renderer()
  if (!r || r.soft) return 'low'
  const nav = navigator as Navigator & { connection?: { saveData?: boolean }; deviceMemory?: number }
  if (nav.connection?.saveData) return 'low'
  if ((nav.hardwareConcurrency || 8) <= 2 || (nav.deviceMemory || 8) <= 2) return 'low'
  try {
    const v = JSON.parse(localStorage.getItem(KEY) ?? 'null')
    if (v && v.r === r.name && (v.t === 'high' || v.t === 'mid' || v.t === 'low')) return v.t
  } catch { /* 没记过 */ }
  return null
}

export function rememberTier(t: Tier) {
  try { localStorage.setItem(KEY, JSON.stringify({ r: renderer()?.name ?? '', t })) } catch { /* 无痕模式 */ }
}

/** 实测的结果换算成档位。门槛要等集成显卡和手机测过再定，现在是保守估计。 */
export function tierOf(avgFrameMs: number): Tier {
  return avgFrameMs <= 22 ? 'high' : avgFrameMs <= 42 ? 'mid' : 'low'
}
