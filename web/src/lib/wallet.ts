// 钱包登录（SIWE / EIP-4361，D52）。只签纯文字消息，不发交易、不要授权。
// 流程：向服务器要一次性随机码 → 钱包连接 → 拼标准登录消息 → personal_sign → 服务器校验。
import { keccak_256 } from '@noble/hashes/sha3'
import { api } from './session'

interface Eip1193 {
  request(args: { method: string; params?: unknown[] }): Promise<any>
}

function provider(): Eip1193 | undefined {
  return (window as any).ethereum as Eip1193 | undefined
}

export function hasWallet(): boolean {
  return !!provider()
}

/** EIP-55 大小写校验和地址（钱包会核对消息里的地址格式） */
export function toChecksum(addr: string): string {
  const a = addr.toLowerCase().replace(/^0x/, '')
  const h = Array.from(keccak_256(new TextEncoder().encode(a)), (b) => b.toString(16).padStart(2, '0')).join('')
  let out = '0x'
  for (let i = 0; i < a.length; i++) out += parseInt(h[i], 16) >= 8 ? a[i].toUpperCase() : a[i]
  return out
}

function utf8Hex(s: string) {
  return '0x' + Array.from(new TextEncoder().encode(s), (b) => b.toString(16).padStart(2, '0')).join('')
}

export class WalletError extends Error {
  constructor(public kind: 'none' | 'rejected' | 'failed', msg = '') { super(msg) }
}

export function buildMessage(o: { domain: string; origin: string; address: string; statement: string; chainId: number; nonce: string; issued: Date; ttlSec: number }) {
  return [
    `${o.domain} wants you to sign in with your Ethereum account:`,
    o.address,
    '',
    o.statement,
    '',
    `URI: ${o.origin}`,
    'Version: 1',
    `Chain ID: ${o.chainId}`,
    `Nonce: ${o.nonce}`,
    `Issued At: ${o.issued.toISOString()}`,
    `Expiration Time: ${new Date(o.issued.getTime() + o.ttlSec * 1000).toISOString()}`,
  ].join('\n')
}

/** 签名并提交。path：登录用 /api/auth/siwe/verify，绑定用 /api/me/bind-wallet */
export async function signWithWallet<T>(path: string, extra: Record<string, unknown> = {}, onWaiting?: () => void): Promise<T> {
  const eth = provider()
  if (!eth) throw new WalletError('none')
  let address: string
  let chainId = 1
  try {
    const accounts: string[] = await eth.request({ method: 'eth_requestAccounts' })
    if (!accounts?.length) throw new WalletError('failed', 'no account')
    address = toChecksum(accounts[0])
    try { chainId = parseInt(await eth.request({ method: 'eth_chainId' }), 16) || 1 } catch { /* 用 1 */ }
  } catch (e: any) {
    if (e instanceof WalletError) throw e
    throw e?.code === 4001 ? new WalletError('rejected') : new WalletError('failed', e?.message ?? String(e))
  }
  const n = await api<{ nonce: string; statement: string; ttl_seconds: number }>('GET', '/api/auth/siwe/nonce')
  const message = buildMessage({ domain: location.host, origin: location.origin, address, statement: n.statement, chainId, nonce: n.nonce, issued: new Date(), ttlSec: n.ttl_seconds })
  onWaiting?.()
  let signature: string
  try {
    signature = await eth.request({ method: 'personal_sign', params: [utf8Hex(message), address] })
  } catch (e: any) {
    throw e?.code === 4001 ? new WalletError('rejected') : new WalletError('failed', e?.message ?? String(e))
  }
  return api<T>('POST', path, { message, signature, ...extra })
}
