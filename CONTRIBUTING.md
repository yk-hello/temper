# Contributing

English | [简体中文](CONTRIBUTING.zh-CN.md)

Questions, improvements and bug fixes are welcome. Below is how to get a development setup running and how to check your changes yourself.

## Ground rules first

A few principles of this project will not change. Please read them before proposing a feature, so no one's time is wasted:

- **No money handling**: no fees, revenue sharing, rebates, referral rewards, copy-trading or pooled funds; no withdrawals or transfers in the code.
- **No strategies**: no built-in strategies, signals, buy/sell suggestions or ratings. Example code only shows how to connect one.
- **No scraping**: data comes only from official public APIs, or from APIs whose keys the person running the install enters themselves.
- **Dependency licenses**: MIT, Apache-2.0, ISC, BSD, 0BSD and OFL (fonts) only. No GPL or AGPL. `python tools/dev/licenses.py` checks this.
- **Interface**: pure black background, white is the only accent color, red and green mean only up/down and profit/loss; labels say what will happen, not internal codes; no page scrolls as a whole.

## Layout

| Folder | Contents |
| --- | --- |
| `backend/` | Go backend. `cmd/qs` is the entry point; each package under `internal/` does one thing (`trading` paper-trading engine, `live` exchange connection, `follow` strategy following, `backtest` backtesting, `notify` notifications, …) |
| `web/` | React + TypeScript + Vite frontend. `src/pages` pages, `src/charts` hand-drawn charts, `src/i18n` Chinese and English |
| `docs/` | Documentation for users; `docs/protocol` is the machine-readable definition of the qsig/1 protocol |
| `tools/` | Check scripts (`e2e-*.mjs`, which really click through the interface with Playwright) and development scripts (`tools/dev`) |
| `compose.yaml` | For users: start everything with one command |
| `deploy/docker-compose.yml` | Development mode: the backend compiles and runs straight from source; after a code change, touch `backend/.reload` and it recompiles |

## Running it

Just using it: `docker compose up -d` (see the README).

Developing:

```bash
docker compose -f deploy/docker-compose.yml up -d    # database + backend (dev mode) + nginx, http://localhost:8080
cd web && npm ci && npm run dev                       # frontend dev server (hot reload), proxies to the local backend
```

After a backend change: `touch backend/.reload`, and the container recompiles and restarts (watch `docker compose -f deploy/docker-compose.yml logs -f backend`).

## Checking your changes

```bash
# Backend: formatting, static checks, tests
cd backend
gofmt -l ./cmd ./internal        # should print nothing
go vet ./...
go test ./...                    # tests that need no database

# Tests with a database: start a throwaway PostgreSQL. The tests empty the tables they use, so never point them at a database with data in it
docker run -d --rm --name qs-test-pg -e POSTGRES_USER=qs -e POSTGRES_PASSWORD=test -e POSTGRES_DB=qs_gotest -p 127.0.0.1:55433:5432 timescale/timescaledb:latest-pg16
QS_TEST_DB='postgres://qs:test@127.0.0.1:55433/qs_gotest?sslmode=disable' go test -p 1 ./...

# Frontend: type check and build
cd web
npx tsc --noEmit -p .
npm run build

# Third-party licenses
python tools/dev/licenses.py
```

On every push to `main` and every pull request, GitHub Actions runs the backend tests above (with the race detector, against a throwaway database) and the frontend type check and build (`.github/workflows/ci.yml`). Pushing a tag starting with `v` (such as `v0.1.0`) builds the backend and web Docker images and publishes them to the GitHub Container Registry of the account that owns this repository (`release.yml`). Changes are recorded in [CHANGELOG.md](CHANGELOG.md).

The interface check scripts are `tools/e2e-*.mjs`: the top of each script says what it checks and how to run it. They register accounts and place paper orders, so **run them only against a test install, never against one with real data**. `tools/dev/` holds the scripts the author uses on their own machine (Windows + Git Bash) to start a test environment and run the full check suite; adapt them to your own setup.

## Before you submit

- If you change behavior, add a test; if you change the interface, look at it at several window widths and at 125% and 150% scaling (many laptops run at 125%).
- Any new text in the interface needs both Chinese (`web/src/i18n/zh.ts`) and English (`en.ts`).
- Money calculations use Decimal and follow the exchange's rules; anything that differs from Binance must be stated in the interface or the docs.
- Never commit keys, `.env` files or real account details.

## Reporting security issues

Please contact the maintainer privately rather than opening a public issue.
