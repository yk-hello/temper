/** #rrggbb + 透明度 → rgba()。 */
export function alpha(hex: string, a: number): string {
  const m = hex.trim().replace('#', '')
  if (m.length !== 6) return `rgba(255,255,255,${a})`
  const n = parseInt(m, 16)
  return `rgba(${(n >> 16) & 255},${(n >> 8) & 255},${n & 255},${a})`
}
