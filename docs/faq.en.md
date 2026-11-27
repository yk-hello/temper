# FAQ

[中文](faq.md) | English | [繁體中文](faq.zh-TW.md)

## Getting started

**The first start is slow.**
The first run builds the backend and the web app locally and downloads dependencies, usually 3–10 minutes depending on your connection. Later starts take seconds.

**http://localhost:8080 is blank or unreachable.**
Check that all three services are up:

```bash
docker compose ps
```

The `backend` row should say `healthy`. If not, read its log: `docker compose logs backend --tail 50`.

**Port 8080 is taken by another program.**
Create a `.env` with `QS_PORT=9090` (any free port), then `docker compose up -d`.

**The top bar keeps saying "connecting" and there is no market data.**
Most likely this machine cannot reach Binance (some regions and networks block it). Two options:

- Route Docker through your proxy (Docker Desktop → Settings → Resources → Proxies);
- Or explore with simulated prices: put `MARKET_SOURCE=sim` in `.env`. Simulated prices are random and only meant for trying the features.

If only spot pages say spot data is down while futures work, the spot stream address is blocked: put `BINANCE_SPOT_WS_URL=wss://data-stream.binance.vision:443` in `.env` and try again.

**It stopped working after the computer slept.**
On some machines Docker Desktop quits on sleep. Start Docker Desktop again; the services come back on their own and your data is intact.

## Accounts

**Do I need an email to sign up?**
No. Sign-up and sign-in use only an account name and a password, and you are signed in right after signing up. You can link an email on the Account page: with it you can recover a forgotten password and receive alerts by email. When outgoing mail is configured (`SMTP_*`, or on the System page), linking asks for a code sent to that address.

**I forgot my login password.**
Click "Forgot password?" on the sign-in page and enter your account name; a code is sent to the email linked to that account. Without a linked email, or if this install has no outgoing mail, run `docker compose exec backend qs reset-password your-account-name` on the computer that runs it; you get a new random password, then change it after signing in.

**I forgot my trade password.**
Account → Trade password → Change → "Forgot your trade password?": enter your sign-in password (plus the 6-digit code if two-step verification is on) and set a new one; no email needed. Afterwards every device is signed out, exchange accounts stop opening new positions (reducing, closing and TP/SL still work) until you turn them on again with the new password, and you get an alert. Accounts with only a wallet and no sign-in password: link an email and password under sign-in methods first, or use the local command below.

**I run this install myself, forgot my trade password and can't use my email.**
On the machine where it is deployed, run (use your email or user number):
```
docker compose exec backend qs reset-trade-password you@example.com
```
The trade password is cleared, every device is signed out and exchange accounts stop opening new positions; sign in, set a new one on the Account page (no old one needed) and turn your exchange accounts back on. If you lost the phone for two-factor sign-in, use `qs reset-2fa you@example.com` the same way. Only someone with access to that machine can run it; it cannot be done from the website.

**What is the trade password for?**
It guards against "someone already inside": a person at your signed-in computer, or yourself in a heated moment. Actions that can cost money need it: adding exchange keys, turning an exchange account on, creating a signal address for an exchange account, turning off order confirmation, turning off or loosening the discipline lock, deleting your account, changing System settings. Actions in the safe direction (halting, tightening rules) and paper-only actions do not. Since a forgotten trade password can be reset with your sign-in password, protection against someone who knows your sign-in password comes from two-step verification: turn it on before connecting real money. On your own computer, lock the screen when you step away.

**Is there an admin?**
No. An install is for one person: every account on it has the same rights and none can manage another. Global halt, system settings, the sign-up switch and the audit log are under the gear at the top right (the System page, also in the "?" menu); changes need the trade password. After the first account, sign-up is closed by default; add accounts under "System".

## Paper trading

**Can I trust the paper-trading numbers?**
The rules follow Binance USDⓈ-M perpetuals item by item: fees, margin tiers, liquidation and clearing fee, price and size filters, and funding at the real settlement times and rates. Where it differs from Binance, the interface and docs say so (for example, there are no other traders on paper, so the auto-deleverage queue is estimated).

**Why did my market order fill a few hundred milliseconds late?**
By default paper trading simulates the real network latency from this computer to Binance, and fills against the order book at the moment the order arrives. Pick a region or turn it off in Account → Trading preferences.

**I lost all my paper money.**
In the paper accounts section of the Account page, "Funds" adds money or takes some out (the principal moves with it, so added money is not counted as profit), and "Reset" clears positions and starts again, with any amount you like; old records are archived. You can also open another account. There is no limit on the amount and no trade password is needed.

**Can I change paper trading fees?**
Yes. Click "Fees" next to a paper account and enter taker and maker rates as percentages (0 to 1%), for example your Binance VIP tier. Only future fills use them. The default is Binance's regular tier: 0.05% taker, 0.02% maker.

**Is a large position always fully liquidated?**
Not necessarily. As on Binance, open orders are cancelled first, then just enough of the position is closed to restore margin; only if that is not enough is the whole position closed. Small positions are usually closed in full because their maintenance margin rate is below the clearance fee rate.

## Strategies

**Do you provide strategies?**
No, and no trading advice either. This is a place to connect and validate your own strategies: see the [strategy author guide](strategy-guide.md) (in Chinese).

**How do I connect my strategy?**
Three ways, depending on what you already have ([details](strategies.md), in Chinese):

- TradingView alerts or anything that can send an HTTP request: use a webhook; one sentence is enough ("long BTC 30%").
  - TradingView strategies: give each order an `alert_message` in the strategy (e.g. `"long BTC 30%"`) and set the alert message to `{{strategy.order.alert_message}}`. The default strategy alert ("buy @ 0.01 filled on …") gives contracts, while orders here are a share of funds, so it is not accepted.
  - With a `BTCUSDT.P` symbol (TradingView's perpetual), buy / sell mean long / short on the perpetual; buy / sell without `.P` are spot.
- Your own program: send target positions with qsig/1, with a reason and confidence; the platform records them, scores them and produces a validation report.
- A program already written for Binance: point it at the Binance-compatible API here and it runs on paper.

**Can someone else's strategy trade my exchange account?**
No. An exchange account (real money) can only follow strategies you created yourself; other people's strategies can only be followed on paper accounts.

**Will backtests match paper trading?**
They share the same matching code, but backtests step through 1-minute candles while paper trading uses the live order book, so results differ. The "backtest consistency" section of the validation report backtests the signals your strategy actually sent and shows both side by side.

## Live trading

**Is live trading safe? Can my money be moved out?**
The software has no withdrawal or transfer code at all, and key permissions are checked when you save a key: keys with withdrawal or transfer permission are refused. Keys are stored encrypted in your own database. We also recommend an IP whitelist on the key at Binance, and trying a Binance demo-trading key first. See [Binance live trading](live.md) (in Chinese).

**What is the master key? Do I need to back it up?**
The key that encrypts exchange keys. If not configured, it is generated on first start and stored in the `qs-data` volume. **Back it up**:

```bash
docker compose cp backend:/data/master.key ./master.key.backup
```

If it is lost, stored exchange keys and strategy keys cannot be decrypted and must be added again.

**Can I run it on a public server for a few people?**
Yes, but first: set up HTTPS (a reverse proxy in front), configure outgoing mail, change the database password, and set `ALLOWED_ORIGINS` and `SITE_DOMAIN`. See "Security" in the README. If you host it and others add their keys, you are the custodian of those keys, and that responsibility is yours.

## Other

**Where is the data? How do I back it up?**
In two Docker volumes: `qs-db` (database) and `qs-data` (master key). Back up the database:

```bash
docker compose exec db pg_dump -U qs qs > backup.sql
```

**How do I remove everything?**
`docker compose down -v` deletes the volumes too (accounts, paper records and stored keys are gone for good).

**How do I upgrade?**
Pull the new code, then `docker compose up -d` (it rebuilds from the current code every time; about half a minute from cache when nothing changed). Database tables are migrated automatically.

**I found a problem or want a feature.**
See [CONTRIBUTING.md](../CONTRIBUTING.md). Please report security issues privately to the maintainers, not in public.
