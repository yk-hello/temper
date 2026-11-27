// 版本号写给人看（D119）：后端 /api/system 给的是 VERSION 文件里的原样，比如 0.1.0、0.1.0+dev
import { tr } from './i18n'

/** 0.1.0 → v0.1.0；0.1.0+dev（两次发布之间的代码）→ v0.1.0 之后的开发版 */
export function verText(v: string): string {
  const base = v.replace(/\+dev$/, '')
  return tr(v.endsWith('+dev') ? 'ver.dev' : 'ver.rel', { v: base })
}
