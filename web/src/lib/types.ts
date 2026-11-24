// 与后端 internal/market 的 JSON 一一对应
export type Side = 'buy' | 'sell'

export interface InstrumentState {
  instrument: string
  symbol: string
  venue: string
  last: string
  last_side?: Side
  bid: string
  bid_qty: string
  ask: string
  ask_qty: string
  mark: string
  funding_rate: string
  next_funding_time: number
  change_24h_pct: string
  quote_volume_24h: string
  high_24h: string
  low_24h: string
  trades: number
  updated_at: number
}

export interface SourceStatus {
  name: string
  connections: Record<string, boolean>
  reconnects: number
  last_error?: string
  last_message_at: number
  /** 每条线路的积压（毫秒），超过 2 秒算行情延迟 */
  lag_ms?: Record<string, number>
}

export interface Snapshot {
  type: 'snapshot'
  ts: number
  status: SourceStatus
  instruments: InstrumentState[]
}

export interface Point {
  t: number
  v: number
}
