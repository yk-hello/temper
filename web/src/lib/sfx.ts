// 高光视频的音效（`05` 第 7 节：不配乐，只用自己设计的音效）。全部现场合成，没有任何外部音频文件，也就没有版权问题。
// 时间点由 charts/film.ts 的 sfxPlan 给：开仓一声，每根 K 线一声很轻的滴答（音高跟着价格走，赚得越多越高），
// 最深浮亏处一声低音，平仓一下重击，金光扫过一道亮音，最后一声钟。

export const SR = 48000

export interface SfxPlan {
  entry: number
  candles: { t: number; k: number }[] // k：0–1，越赚钱越高
  low: number | null
  exit: number
  sweep: number
  rows: number
}

export async function synth(duration: number, p: SfxPlan): Promise<AudioBuffer> {
  const ctx = new OfflineAudioContext(2, Math.ceil(SR * duration), SR)
  const out = ctx.createGain()
  out.gain.value = 0.9
  out.connect(ctx.destination)
  // 一点房间感：合成一段衰减的噪声当混响
  const rev = ctx.createConvolver()
  const ir = ctx.createBuffer(2, SR * 2, SR)
  for (let ch = 0; ch < 2; ch++) {
    const d = ir.getChannelData(ch)
    for (let i = 0; i < d.length; i++) d[i] = (Math.random() * 2 - 1) * Math.pow(1 - i / d.length, 3.5)
  }
  rev.buffer = ir
  const wet = ctx.createGain()
  wet.gain.value = 0.28
  rev.connect(wet).connect(out)
  const send = (n: AudioNode, dry = 1) => { const g = ctx.createGain(); g.gain.value = dry; n.connect(g).connect(out); n.connect(rev) }
  const tone = (t: number, f: number, a: number, dur: number, type: OscillatorType = 'sine', dry = 1) => {
    if (t < 0 || t > duration - 0.02) return
    const o = ctx.createOscillator(); o.type = type; o.frequency.value = f
    const g = ctx.createGain(); g.gain.setValueAtTime(0, t); g.gain.linearRampToValueAtTime(a, t + 0.005); g.gain.exponentialRampToValueAtTime(0.0005, t + dur)
    o.connect(g); send(g, dry); o.start(t); o.stop(Math.min(duration, t + dur + 0.02))
  }
  const noise = (t: number, dur: number, a: number, f0: number, f1: number, q = 0.8) => {
    const b = ctx.createBuffer(1, Math.ceil(SR * dur), SR)
    const d = b.getChannelData(0)
    for (let i = 0; i < d.length; i++) d[i] = Math.random() * 2 - 1
    const s = ctx.createBufferSource(); s.buffer = b
    const f = ctx.createBiquadFilter(); f.type = 'bandpass'; f.Q.value = q
    f.frequency.setValueAtTime(f0, t); f.frequency.exponentialRampToValueAtTime(f1, t + dur)
    const g = ctx.createGain(); g.gain.setValueAtTime(0, t); g.gain.linearRampToValueAtTime(a, t + dur * 0.15); g.gain.exponentialRampToValueAtTime(0.0005, t + dur)
    s.connect(f).connect(g); send(g, 1); s.start(t); s.stop(t + dur)
  }

  // 开仓：一声干净的短音
  tone(p.entry, 880, 0.12, 0.35)
  tone(p.entry, 1320, 0.05, 0.25)
  // 每根 K 线：很轻的滴答，音高在一个五声音阶里跟着价格走
  const scale = [0, 2, 4, 7, 9, 12, 14, 16, 19, 21, 24]
  for (const c of p.candles) {
    const step = scale[Math.round(c.k * (scale.length - 1))]
    tone(c.t - 0.03, 440 * Math.pow(2, step / 12), 0.035, 0.12, 'triangle', 0.7)
  }
  // 最深浮亏：一声沉下去的低音
  if (p.low !== null) {
    const t = p.low
    const o = ctx.createOscillator(); o.type = 'sine'
    o.frequency.setValueAtTime(110, t); o.frequency.exponentialRampToValueAtTime(55, t + 0.6)
    const g = ctx.createGain(); g.gain.setValueAtTime(0, t); g.gain.linearRampToValueAtTime(0.28, t + 0.02); g.gain.exponentialRampToValueAtTime(0.0005, t + 0.8)
    o.connect(g); send(g, 1); o.start(t); o.stop(t + 0.85)
  }
  // 平仓：一下重击（低频冲击加一小段噪声）
  {
    const t = p.exit
    const o = ctx.createOscillator(); o.type = 'sine'
    o.frequency.setValueAtTime(140, t); o.frequency.exponentialRampToValueAtTime(42, t + 0.35)
    const g = ctx.createGain(); g.gain.setValueAtTime(0, t); g.gain.linearRampToValueAtTime(0.75, t + 0.004); g.gain.exponentialRampToValueAtTime(0.0005, t + 0.9)
    o.connect(g); send(g, 1); o.start(t); o.stop(t + 0.95)
    noise(t, 0.18, 0.22, 2400, 600, 0.6)
  }
  // 金光扫过：一道上扬的亮音
  noise(p.sweep, 0.7, 0.05, 1800, 7000, 2.5)
  tone(p.sweep + 0.05, 1568, 0.04, 0.9, 'sine', 0.5)
  // 收尾：钟声（几个不成整数倍的泛音，像金属）
  const t = p.rows
  for (const [f0, a0, dur] of [[523.25, 0.14, 2.6], [1046.5, 0.06, 2], [1567.98, 0.04, 1.6], [2637, 0.02, 1], [785.5, 0.04, 2.2]] as [number, number, number][]) {
    tone(t, f0, a0, dur)
  }
  return ctx.startRendering()
}
