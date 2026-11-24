// 部署的人可以改的上限（D103）：服务器上用环境变量 QS_LIMIT_* 改，启动时从 /api/limits 读。
// 提示语里的数字都从这里取，改了上限提示也跟着对。读不到时用默认值（和服务器的默认值一样）。
// 不要在这里引用 i18n：i18n 要用它，互相引用会转圈。

export const LIMITS: Record<string, number> = {
  paper_accounts: 10, signup_per_ip_hour: 10, backtest_days: 180, backtest_instruments: 5, backtest_signals: 20000,
  api_keys: 10, strategies: 20, price_alerts: 50, rules: 20, shot_mb: 5, shots_total_mb: 500,
}

export function loadLimits() {
  fetch('/api/limits')
    .then((r) => (r.ok ? r.json() : null))
    .then((v: Record<string, number> | null) => { if (v) for (const [k, n] of Object.entries(v)) if (Number.isFinite(n) && n > 0) LIMITS[k] = n })
    .catch(() => { /* 用默认值 */ })
}

/** 错误代码对应的上限数字（拼进提示语） */
export function limitParams(code: string): Record<string, string> | undefined {
  const n = (k: string) => String(LIMITS[k])
  switch (code) {
    case 'TOO_MANY_ACCOUNTS': return { max: n('paper_accounts') }
    case 'BT_BAD_RANGE': return { days: n('backtest_days') }
    case 'BT_TOO_MANY': return { inst: n('backtest_instruments'), signals: n('backtest_signals') }
    case 'STRATEGY_LIMIT': return { max: n('strategies') }
    case 'BINAPI_LIMIT': return { max: n('api_keys') }
    case 'TOO_MANY_ALERTS': return { max: n('price_alerts') }
    case 'TOO_MANY_RULES': return { max: n('rules') }
  }
  return undefined
}
