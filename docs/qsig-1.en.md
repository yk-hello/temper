# qsig/1 signal protocol

[中文](qsig-1.md) | English

The contract between a programmatic strategy and the platform: every time the strategy makes a decision, it sends one signal saying "how much of each instrument should be held"; the platform handles orders, stops and risk.
Machine-readable schema: [`protocol/qsig-1.schema.json`](protocol/qsig-1.schema.json); example: [`protocol/qsig-1.example.signal.json`](protocol/qsig-1.example.signal.json);
sending example: [`examples/qsig_send.py`](examples/qsig_send.py).

The protocol only ever adds optional fields; breaking changes will move to qsig/2.

## 1. Core semantics

- The strategy sends one signal per decision, containing a **target weight** per instrument (how much to hold), not buy/sell actions.
- `weight` = target position notional ÷ the strategy's model-portfolio equity, signed: positive long, negative short, 0 flat.
  - Per instrument |weight| ≤ 1
  - Sum of |weight| across instruments ≤ 2 (at most 2× gross leverage)
  - If a limit is exceeded the **whole message is rejected**; nothing is scaled down
- Sending the same target again = keep the position as is. The platform does **not** rebalance back to the target ratio as prices move.
- If the absolute difference between the new target and the actual position is < 0.01, nothing is traded (minimum adjustment).
- Version 1 supports perpetual futures only. Instruments use platform codes (`BTC-PERP` = stablecoin-margined linear perpetual), mapped to each exchange by the platform.
- `confidence` (0–1) is for display and user filtering only and **does not affect sizing**.

## 2. Message types

Fields shared by all messages: `protocol` (always "qsig/1"), `type`, `id` (UUID), `strategy_id`, `strategy_version`, `issued_at`.

### signal

| Field | Required | Meaning |
| --- | --- | --- |
| data_as_of | yes | Time of the newest data point used for this decision |
| valid_until | yes | After issued_at, at most 5 minutes later (60 seconds recommended). Not executed if already expired on arrival |
| mode | yes | `partial`: adjust only the listed instruments (recommended). `snapshot`: the list is the complete book; anything not listed is closed |
| targets | yes | 1–50 items, **never empty** (so a bug sending an empty list cannot close everything). To close all, send halt or write 0 for each |
| meta | no | Model, prompt version, latency, inference cost (cost_usd), etc. Display only |

Each item in targets:

| Field | Required | Meaning |
| --- | --- | --- |
| instrument | yes | e.g. `BTC-PERP`; must be in `/v1/instruments` |
| weight | yes | String, -1 to 1, at most 4 decimals |
| confidence | yes | Number, 0–1 |
| horizon_min | yes | Expected holding time in minutes, 5–10080 |
| ref_price | yes | String: the **raw** market price fetched at the moment of decision, without fees or slippage |
| ref_price_source | yes | Version 1: always `binance_usdm_last` (Binance USDⓈ-M perpetual last price) |
| execution | no | `urgency`: passive (maker only), normal (default), immediate (take liquidity now); `max_slippage_bps`: default 20 |
| exits | no | `stop_loss_pct`, `take_profit_pct`: fractions relative to the average entry price, as strings |
| rationale | yes | `summary.zh` (≤120 characters, required), `summary.en`; `factors` (≤12: key, label, score∈[-1,1]); `evidence` (≤10: kind, title, source, url, published_at, excerpt ≤200 characters) |

The rationale is shown in the decision feed on the strategies page.

### heartbeat
- Fields: `status` (ok / degraded / paused), optional `note`
- Send every 30–60 seconds
- After 180 seconds without one, the strategy is marked offline: positions stay, stops keep working, the operator is notified, and the UI shows "strategy offline"

### halt
- Fields: `action` (`flatten_all`: close everything and pause; `pause`: no new exposure, positions untouched), `reason.zh`
- After a halt **only the operator can resume manually**. Heartbeats never resume automatically, so a restarted program cannot resume by mistake.

## 3. Format rules

- All times are UTC with milliseconds and a trailing Z, e.g. `2026-11-03T08:30:12.345Z`.
- Numbers that affect order size (weight, ref_price, exits) are always strings.
- Ordering per instrument is by `issued_at`: a signal older than the decision already executed for that instrument is discarded if it arrives late (code STALE). **There is no separate sequence field.**
- A message is accepted or rejected as a whole.
- Unknown fields are ignored.
- The same id sent again with the same content is a duplicate (status: duplicate); the same id with different content is rejected (ID_CONFLICT).

## 4. Endpoints (implemented by the platform)

- All messages go to one endpoint: `POST /v1/messages`.
- Query endpoints (also signed):

| Endpoint | Returns |
| --- | --- |
| `GET /v1/instruments` | Tradable instruments and metadata |
| `GET /v1/ticker?instrument=BTC-PERP` | Last price, best bid/ask, mark price, funding rate, next settlement time, timestamp |
| `GET /v1/portfolio` | Model-portfolio equity and cash; per instrument: target weight, actual weight, quantity, entry price, mark price, unrealized P&L, time the target was set; why the platform closed a position (closed_by: stop_loss / risk / circuit_breaker / halt); whether the circuit breaker is active |
| `GET /v1/messages/{id}` | Status (accepted / rejected / duplicate) and execution report: per instrument the previous weight, target weight, achieved weight, execution state, average fill price, slippage vs ref_price (bps), fees, start and end time |
| `GET /v1/performance?window=7d\|30d\|all` | Performance metrics |

Execution `state` values:

- `filled`: fully filled
- `partial`: partially filled
- `skipped_price_moved`: before execution the price had moved more than max_slippage_bps from ref_price; not executed
- `skipped_no_change`: same target as before; position kept
- `skipped_min_adjust`: adjustment smaller than 0.01; not executed

### Signing
- Headers:
  - `X-QS-Key`: the strategy's key id
  - `X-QS-Timestamp`: timestamp in milliseconds
  - `X-QS-Signature`: HMAC-SHA256 signature, lowercase hex
- String to sign: `timestamp + "\n" + METHOD + "\n" + path (with query string) + "\n" + body`. For GET the body is the empty string. **Sign the exact raw bytes you send.**
- Requests whose timestamp differs from platform time by more than 30 seconds are rejected (TIMESTAMP_SKEW).
- Use separate keys per environment (test, production). Keys can be created self-service (Account → My strategies; the secret is shown once and can be rotated or revoked) or set by the operator in the `QSIG_KEYS` environment variable. strategy_id must match the key.
- Every message received goes into the strategy's forward record (a hash chain); see "forward record" in [strategies.md](strategies.md) (in Chinese).

### Replies and error codes

Reply body: `{status, id, received_at, code?, message?, details?, warnings?}`.

| HTTP | code | Meaning | Retry? |
| --- | --- | --- | --- |
| 200 | — | accepted or duplicate | — |
| 400 | INVALID_JSON / SCHEMA_VIOLATION (details.path) / UNSUPPORTED_PROTOCOL | Format problem | No |
| 401 | AUTH_FAILED / TIMESTAMP_SKEW | Bad signature or clock off | No |
| 403 | STRATEGY_MISMATCH | strategy_id does not match the key | No |
| 409 | ID_CONFLICT | Same id, different content | No |
| 422 | EXPIRED / STALE / UNKNOWN_INSTRUMENT / INSTRUMENT_NOT_TRADABLE / WEIGHT_LIMIT_EXCEEDED / CIRCUIT_BREAKER / STRATEGY_PAUSED | Rejected by a business rule | No |
| 429 | RATE_LIMITED | At most 30 signals per strategy per minute | Yes, with backoff |
| 5xx | INTERNAL_ERROR | Platform failure | Yes |

- **CIRCUIT_BREAKER and STRATEGY_PAUSED reject only signals that add exposure.** Signals that reduce or close are always accepted.
- **warnings** (accepted, with a note):
  - REF_PRICE_DEVIATION: ref_price differs from the platform price by more than 0.5% on arrival; the price source may be wrong
  - LONG_LATENCY: issued_at − data_as_of is more than 600 seconds
- **Retries**: on network timeouts, 429 and 5xx, resend with the same id and the same content, recomputing the signature headers; give up once valid_until has passed.

## 5. Execution rules

- The model portfolio starts at 10,000 USDT and is simulated against the live market: fills walk the real order book level by level, taker fee 0.05%, maker fee 0.02%, funding at the exchange's actual settlements.
- Pre-trade check: if the current price is more than max_slippage_bps from ref_price, nothing is executed (skipped_price_moved). During execution the average price may not exceed that limit; any remainder beyond it is cancelled.
- The three urgencies:
  - passive: post at the best price; give up if not filled within 60 seconds or by expiry
  - normal: post a limit order first, then take the remainder within the slippage limit
  - immediate: take liquidity right away within the slippage limit
- Stops and take-profits are monitored on the mark price. When triggered, the position is closed with a market order, the instrument's target becomes 0, and closed_by is recorded.
- horizon expires without a new signal: version 1 does not close automatically; the UI and portfolio state mark it "expired".
- Daily (UTC) loss above 5% trips the circuit breaker: for the rest of the day only reducing and closing signals are accepted.
