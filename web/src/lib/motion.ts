// 动效底座。原则（写进 05 规范）：
// 1. 连续：任何视图变化（缩放、换时间范围、复位）都是平滑过渡，不跳；手指松开后带一点惯性
// 2. 跟手：拖动时 1:1 跟随，没有延迟
// 3. 告诉你变了什么：选中指示条从旧位置滑到新位置；数字只亮变动的那几位
// 4. 克制：只在用户操作和数据变化时动，没有常驻动画；系统设置"减少动态效果"时全部关闭

const mq = typeof matchMedia !== 'undefined' ? matchMedia('(prefers-reduced-motion: reduce)') : null
export const reducedMotion = () => !!mq?.matches

/** 与帧率无关的指数逼近：每秒逼近 rate 的比例。返回新的值。 */
export function approach(cur: number, target: number, dtMs: number, rate = 16): number {
  if (reducedMotion()) return target
  const k = 1 - Math.exp((-rate * dtMs) / 1000)
  return cur + (target - cur) * k
}

/**
 * 一组数值一起逼近目标；返回是否还在动。
 * tol 是"足够接近就直接对齐"的绝对误差：一个数（所有键共用），或按键分别给。
 * 注意不能用相对误差：时间戳是 1.7×10¹² 这种大数，相对误差会让动画一开始就直接跳到终点。
 */
export function approachAll<T extends Record<string, number>>(cur: T, target: T, dtMs: number, rate = 16, tol: number | Partial<Record<keyof T, number>> = 1e-6): boolean {
  let moving = false
  for (const k in target) {
    const eps = typeof tol === 'number' ? tol : tol[k] ?? 1e-6
    const next = approach(cur[k], target[k], dtMs, rate)
    ;(cur as Record<string, number>)[k] = Math.abs(next - target[k]) <= eps ? target[k] : next
    if (cur[k] !== target[k]) moving = true
  }
  return moving
}

/** 惯性：松手时的速度（每毫秒）按指数衰减，返回从现在到停下的总位移 */
export function glide(velocityPerMs: number, decayMs = 280): number {
  if (reducedMotion()) return 0
  return velocityPerMs * decayMs
}
