// 邮箱格式检查：不只说"格式不对"，而是指出具体哪里不对；常见邮箱漏了后缀时直接给出改好的写法。
// 后端只做最基本的检查（BAD_EMAIL），这里负责把话说清楚。

/** 常见邮箱服务：只写了 @ 后面的名字、漏了后缀时，用来猜完整域名 */
const PROVIDERS: Record<string, string> = {
  gmail: 'gmail.com', googlemail: 'googlemail.com', outlook: 'outlook.com', hotmail: 'hotmail.com', live: 'live.com',
  yahoo: 'yahoo.com', icloud: 'icloud.com', proton: 'proton.me', protonmail: 'protonmail.com', gmx: 'gmx.com',
  qq: 'qq.com', foxmail: 'foxmail.com', '163': '163.com', '126': '126.com', yeah: 'yeah.net', sina: 'sina.com', sohu: 'sohu.com', aliyun: 'aliyun.com',
}

export interface EmailIssue {
  /** 对应词条 err.<code> */
  code: string
  /** 建议改成的完整邮箱（EMAIL_SUGGEST 时有） */
  fix?: string
}

export function emailIssue(raw: string): EmailIssue | null {
  const s = raw.trim()
  if (!s) return { code: 'EMAIL_EMPTY' }
  if (/\s/.test(s)) return { code: 'EMAIL_SPACE' }
  const parts = s.split('@')
  if (parts.length === 1) return { code: 'EMAIL_NO_AT' }
  if (parts.length > 2) return { code: 'EMAIL_MULTI_AT' }
  const [name, dom] = parts
  if (!name) return { code: 'EMAIL_NO_NAME' }
  if (!dom) return { code: 'EMAIL_NO_DOMAIN' }
  const base = dom.replace(/\.+$/, '')
  if (!base.includes('.')) {
    const p = PROVIDERS[base.toLowerCase()]
    return p ? { code: 'EMAIL_SUGGEST', fix: `${name}@${p}` } : { code: 'EMAIL_NO_TLD' }
  }
  if (dom.startsWith('.') || dom.endsWith('.') || dom.includes('..') || !/^[^.]+(\.[^.]+)+$/.test(dom)) return { code: 'BAD_EMAIL' }
  return null
}
