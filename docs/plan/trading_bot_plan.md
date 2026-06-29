# Trading Bot - Telegram MVP Plan

Status: reviewed and aligned for Go implementation.

## Overview

Build a private Telegram bot that controls Binance Futures through free-form text and inline buttons.

- Language: Go
- Telegram: `github.com/go-telegram/bot` or the chosen Go Telegram library in `go.mod`
- Exchange: Binance Futures through a Go client, preferably `github.com/adshao/go-binance/v2/futures`
- REST API: optional. If needed, use Fiber v3 (`github.com/gofiber/fiber/v3`)
- Database: MongoDB Atlas
- Object storage: S3-compatible cloud storage for generated files only
- Deployment: Docker on VPS, Fly.io, Railway, or similar

## Locked MVP Decisions

- Telegram mode: polling first. Use Fiber/webhook only when deployment needs a public webhook.
- Admin: owner Telegram user ID in `TELEGRAM_ADMIN_USER_ID`.
- Private MVP allowlist: comma-separated Telegram user IDs in `TELEGRAM_ALLOWED_USER_IDS`; if admin is omitted, the first allowlisted ID is treated as admin for backward compatibility.
- Multi-user SaaS direction: non-admin users must have an active subscription record before trading features are enabled.
- Position sizing: explicit sizing only in Phase 1. A trade must include `size <USDT>usdt` or `qty <baseQty>`.
- Margin mode: isolated by default.
- Confirmation: every exchange-changing action requires confirmation, including dry-run and testnet. This keeps UX, audit, and idempotency consistent.
- Parser scope: Phase 1 accepts exactly one TP. The domain model keeps `TakeProfits []decimal.Decimal` for future multiple TP support.

## Non-Negotiable Safety Rules

- Default to testnet and dry-run behavior until the user explicitly enables real trading.
- Never commit secrets, `.env`, API keys, private reports, or downloaded account exports.
- Only respond to allowlisted Telegram users.
- Require an explicit confirmation step before every exchange-changing action.
- Record every trade intent, confirmation, exchange request, and exchange response in an audit log.
- Tests must not call real Telegram, Binance, MongoDB Atlas, or S3 unless explicitly marked as integration tests.
- Handle exchange precision, minimum notional, rate limits, and idempotency before sending orders.
- Do not log API keys, secrets, full Telegram tokens, raw `.env` values, private account exports, or full callback tokens.

## Environment Variables

```env
# App
APP_ENV=local
LOG_LEVEL=info
DRY_RUN=true
REAL_TRADING_ENABLED=false
ORDER_SIZING_MODE=explicit
DEFAULT_MARGIN_MODE=isolated
MAX_LEVERAGE=20
CONFIRMATION_TTL_SECONDS=300

# Telegram
TELEGRAM_BOT_TOKEN=
TELEGRAM_ADMIN_USER_ID=
TELEGRAM_ALLOWED_USER_IDS=
TELEGRAM_MODE=polling
PUBLIC_WEBHOOK_URL=
TELEGRAM_POLLING_BACKOFF_MIN_SECONDS=1
TELEGRAM_POLLING_BACKOFF_MAX_SECONDS=30

# Optional HTTP/Fiber
HTTP_ENABLED=false
HTTP_ADDR=:8080

# Binance
BINANCE_API_KEY=
BINANCE_API_SECRET=
BINANCE_TESTNET=true
BINANCE_FUTURES_BASE_URL=https://demo-fapi.binance.com
BINANCE_REQUEST_TIMEOUT_SECONDS=10
EXCHANGE_INFO_CACHE_TTL_SECONDS=900

# TradingView webhook alerts
TRADINGVIEW_ENABLED=false
TRADINGVIEW_WEBHOOK_SECRET=

# AI advisor
AI_ENABLED=false
AI_PROVIDER=disabled
AI_API_KEY=
AI_BASE_URL=https://api.openai.com/v1
AI_MODEL=
AI_SYSTEM_PROMPT=
AI_REQUEST_TIMEOUT_SECONDS=20
AI_MIN_CONFIDENCE_PERCENT=70
AI_AUTOTRADE_ENABLED=false

# MongoDB Atlas
MONGODB_URI=
MONGODB_DATABASE=tradebot

# Stripe subscriptions
STRIPE_SECRET_KEY=
STRIPE_WEBHOOK_SECRET=
STRIPE_PRICE_ID=

# S3-compatible object storage
S3_ENDPOINT=
S3_REGION=
S3_BUCKET=
S3_ACCESS_KEY_ID=
S3_SECRET_ACCESS_KEY=
S3_FORCE_PATH_STYLE=false
```

`ORDER_SIZING_MODE=explicit` is a forward-compatible enum. Phase 1 supports only `explicit`; future values may include `risk_percent` or `fixed_usdt` after the risk engine exists.

## Storage Policy

Use MongoDB Atlas for durable app data:

- Users and allowlist metadata
- Subscription records and Stripe customer/subscription IDs
- Encrypted per-user exchange credentials
- Parsed trade intents
- Orders, positions, fills, and plan assignments
- Audit events and confirmation records
- Bot session state
- Scanner signals and alert state

Use S3-compatible storage only for generated or uploaded files:

- Daily or weekly P&L reports
- Exported audit bundles
- Chart images or screenshots
- Backtest artifacts
- Large JSON/CSV exports

Do not use a local database file. Local disk is only for temporary files, build artifacts, and ignored logs.

## MongoDB Collections And Indexes

Use explicit repositories in `internal/storage/mongo`. Collection names are plural and snake_case.

| Collection | Purpose | Required indexes |
|---|---|---|
| `users` | Telegram users, role, status, and subscription metadata | unique `{telegram_user_id: 1}`, `{role: 1, status: 1}` |
| `interest_signups` | Product-interest emails; these are not user accounts | unique `{email: 1}` |
| `subscriptions` | Stripe customer/subscription state per user | unique `{stripe_customer_id: 1}`, unique `{stripe_subscription_id: 1}`, `{user_id: 1, status: 1}` |
| `exchange_credentials` | encrypted per-user Binance API credentials | unique `{user_id: 1, exchange: 1}`, `{status: 1, updated_at: -1}` |
| `audit_events` | user input, parser decisions, confirmations, exchange requests/responses | `{user_id: 1, created_at: -1}`, `{correlation_id: 1}` |
| `order_intents` | parsed and validated trade/close/status intents | `{user_id: 1, created_at: -1}`, `{status: 1, created_at: -1}`, `{intent_hash: 1}` |
| `confirmations` | pending and completed confirmation state | unique `{token_hash: 1}`, unique `{idempotency_key: 1}`, TTL `{expires_at: 1}` |
| `orders` | local order records mapped to exchange orders | unique `{client_order_id: 1}`, `{user_id: 1, created_at: -1}`, `{symbol: 1, status: 1}` |
| `fills` | exchange fills/trades | unique `{exchange_trade_id: 1}`, `{order_id: 1}` |
| `positions` | current and historical position snapshots | `{user_id: 1, status: 1}`, `{user_id: 1, symbol: 1, status: 1}`, `{plan_id: 1, status: 1}` |
| `plans` | plan metadata and strategy tags | unique `{user_id: 1, plan_id: 1}` |
| `signals` | scanner signals and decisions | `{symbol: 1, created_at: -1}`, `{status: 1, created_at: -1}` |
| `alert_states` | de-duplication state for alerts | unique `{user_id: 1, symbol: 1, rule_key: 1}` |
| `exchange_symbol_filters` | cached Binance symbol filters | unique `{symbol: 1}`, `{refreshed_at: -1}` |
| `job_locks` | monitor/scanner single-run locks when needed | unique `{job_name: 1}`, TTL `{expires_at: 1}` |

Confirmation documents must survive process restarts until expiry. Do not keep pending confirmations only in memory.
`job_locks` are mainly for multi-replica deployments so monitor/scanner jobs do not run twice at the same time.

## Idempotency And Confirmation TTL

Every parsed exchange-changing intent creates:

- an `order_intents` document with canonical `intent_hash`
- a `confirmations` document with an opaque callback token hash, `idempotency_key`, status, and `expires_at`
- an `audit_events` trail with a shared `correlation_id`

Rules:

- `CONFIRMATION_TTL_SECONDS` defaults to 300 seconds.
- Telegram callback data must contain only a compact confirmation ID/action/token reference, never full order details.
- Store only a hash of the callback token in MongoDB.
- `intent_hash` is for duplicate detection and operator/debug visibility; it is not the enforcement key. Enforcement uses unique `idempotency_key` and `client_order_id`.
- Duplicate confirmation callbacks with the same `idempotency_key` must return the previously recorded result.
- Client order IDs must be deterministic from the confirmation/execution identity, for example `tb_<shortConfirmationID>_<leg>`.
- Expired confirmations cannot execute orders; the bot should ask the user to send the command again.
- Cancel callbacks mark the confirmation as `cancelled` and are also idempotent.
- Confirmation status changes must use atomic conditional updates, for example MongoDB `findOneAndUpdate` or `updateOne` with expected current `status`, `token_hash`, and non-expired `expires_at`. Never read status first and update later in separate unsafe steps.

Confirmation statuses:

```text
pending -> confirmed -> executing -> executed
pending -> cancelled
pending -> expired
pending/confirmed/executing -> failed
```

## Exchange Filters, Precision, And Rate Limits

Before any order reaches Binance:

- Load symbol filters from Binance Futures `exchangeInfo`.
- Cache filters in memory and persist the latest copy in `exchange_symbol_filters`.
- Refresh filters on startup, when TTL expires, when a symbol is missing, or after a precision/filter error.
- Default `EXCHANGE_INFO_CACHE_TTL_SECONDS` is 900 seconds.
- Round price by tick size and quantity by step size.
- Validate minimum notional and exchange-specific leverage limits.
- Validate isolated margin mode unless the user explicitly changes config later.
- Reject execution when filters are unavailable.

Rate limit and retry rules:

- Telegram polling uses exponential backoff between `TELEGRAM_POLLING_BACKOFF_MIN_SECONDS` and `TELEGRAM_POLLING_BACKOFF_MAX_SECONDS`.
- Binance requests use context deadlines and request timeout from config.
- Binance retry is allowed only for safe reads and clearly idempotent writes.
- Track Binance rate-limit/weight response headers where the client exposes them.
- MongoDB connection should be established at startup and health-checked; transient write failures should fail the current action safely instead of silently losing audit data.

Entry execution is maker-first: submit a post-only `LIMIT` (`GTX`) at the planned
entry, wait for a short bounded window, cancel any remainder, then use `MARKET`
only for the unfilled quantity. Protective SL/TP and urgent closes remain taker
orders because avoiding an unprotected position takes priority over fee savings.

Planning contract:

- `capital_risk_pct` is the cumulative plan-loss ceiling as a percentage of
  allocated capital. It is never reused as leverage.
- `leverage_use_pct` is the percentage of the permitted leverage ceiling made
  available to the model; execution may choose less.
- `duration` is the plan's entry window, distinct from its automatically chosen
  execution candle interval. Supported durations are dev `15m`, `1h`, `2h`,
  `4h`, `8h`, `12h`, `24h`, `48h`, and `1w`.

## Logging And Sensitive Data Policy

Use structured logs with correlation IDs. Logs may include sanitized user ID, symbol, intent type, status, and order IDs.

Never log:

- `TELEGRAM_BOT_TOKEN`
- `BINANCE_API_KEY`
- `BINANCE_API_SECRET`
- `MONGODB_URI`
- S3 credentials
- raw `.env` contents
- full callback tokens
- private account exports

Audit events may store trading metadata and exchange response summaries, but secrets must be redacted before persistence.

## Project Structure

```text
trade-bot/
├── cmd/
│   └── tradebot/
│       └── main.go
├── internal/
│   ├── app/                 # wiring, lifecycle, dependency setup
│   ├── api/                 # optional Fiber v3 HTTP routes/webhooks
│   ├── config/              # env loading and validation
│   ├── domain/              # core types: OrderIntent, Position, Plan, Signal
│   ├── telegram/            # handlers, commands, callback buttons
│   ├── parser/              # free-form text parser
│   ├── exchange/
│   │   └── binance/         # Binance Futures adapter
│   ├── orders/              # order service and risk gates
│   ├── plans/               # plan 1/2/3 state and grouping
│   ├── scanner/             # market scanner and signal generation
│   ├── monitor/             # alert and position monitor loop
│   └── storage/
│       ├── mongo/           # MongoDB repositories
│       └── object/          # S3-compatible object storage client
├── .claude/
│   ├── agents/
│   └── skills/
├── AGENT.md
├── AGENTS.md
├── CLAUDE.md
├── TRADING_BOT_REVIEW.md
├── .env.example
└── .gitignore
```

## Phase 1 - Core Telegram MVP

Features:

- Auth: admin comes from `TELEGRAM_ADMIN_USER_ID`; optional private-MVP users come from `TELEGRAM_ALLOWED_USER_IDS`.
- Multi-user SaaS direction: non-admin users must pass subscription checks from MongoDB/Stripe before trading features are enabled.
- Commands: `/start`, `/help`, `/status`
- `/help` shows the frozen Phase 1 grammar and examples.
- `/status` and free-form `status` are equivalent read-only intents and do not require confirmation.
- Free-form parser for open, close, and status intents
- Open position intent: coin, side, entry, SL, TP, leverage, explicit size/qty
- Close position intent: close all, close symbol, close percentage
- Confirmation buttons after each parsed exchange-changing action: `[Confirm] [Cancel]`. `[Edit]` is optional later.
- Position-management buttons such as `[BE]`, `[Trail 0.5%]`, `[Close 50%]`, and `[Close All]` belong to Phase 2 after a position exists.
- Dry-run and testnet path before any live trading path
- MongoDB audit log for every user request and bot decision

Free-form examples:

```text
long BTC 3x entry 67500 sl 65000 tp 72000 size 100usdt
short ETH 2x entry 3300 sl 3450 tp 3000 qty 0.05
close BTC
close BTC 50%
close all
status
```

### Phase 1 Parser Grammar

Freeze this grammar before coding the parser. Anything outside this grammar should return a validation error plus a helpful Telegram message.

| Intent | Grammar | Notes |
|---|---|---|
| Open long/short | `<side> <symbol> <leverage> entry <price> sl <price> tp <price> <size>` | `side` is `long` or `short`; `leverage` is `<int>x`; exactly one TP in Phase 1 |
| Size by quote notional | `size <amount>usdt` | Example: `size 100usdt`; service calculates base quantity from entry and filters |
| Size by base quantity | `qty <amount>` | Example: `qty 0.05`; amount is base asset quantity |
| Close all | `close all` | Requires confirmation |
| Close symbol | `close <symbol>` | Requires confirmation; no percentage means close 100% |
| Close percentage | `close <symbol> <percent>%` | `percent` must be greater than 0 and at most 100 |
| Status | `status` | Read-only; no confirmation required |
| Slash status | `/status` | Equivalent to `status`; read-only; no confirmation required |
| Plan status | `plan <1|2|3> status` | Read-only; no confirmation required |
| Plan tag on open | append `plan <1|2|3>` | Optional in Phase 1; default plan can be empty |

Parser normalization:

- Symbols normalize to Binance Futures symbols, e.g. `BTC` becomes `BTCUSDT`.
- Prices, quantities, percentages, and leverage parse as decimal/integer values, never `float64`.
- `MAX_LEVERAGE` defaults to 20 before symbol-specific filters are loaded.
- Missing size/qty makes an open intent invalid.
- Multiple TPs are rejected in Phase 1 even though the domain type stores a slice.
- Market entries, risk-percent sizing, and scanner one-click sizing are deferred.

Parser output should become a typed Go value, not a loose map:

```go
type SizeKind int

const (
    SizeUnknown SizeKind = iota
    SizeUSDT
    SizeQty
)

type OrderSize struct {
    Kind   SizeKind
    Amount decimal.Decimal
}

type OrderIntent struct {
    Symbol   string
    Side     Side
    Leverage int
    Entry    decimal.Decimal
    StopLoss decimal.Decimal
    TakeProfits []decimal.Decimal
    Size     OrderSize
    PlanID   string
}
```

Use an exact decimal library for prices and sizes. Do not use `float64` for order-critical math.

## Phase 2 - Position Management

- Move SL to breakeven: `be BTC` or `move sl btc to be`
- Trailing stop: `trail BTC 0.5%`
- Partial close: `close BTC 50%`
- Scale in: `add BTC size 100usdt` or `add BTC qty 0.01`
- Auto-suggest breakeven when unrealized profit reaches 1R
- Store plan, risk, and audit metadata in MongoDB

Inline buttons after open position:

```text
[BE] [Trail 0.5%] [Close 50%] [Close All]
```

Inline buttons after `/status`:

```text
[BTC +244 USDT] [ETH +117 USDT]
```

Pressing a symbol button should show details and action buttons.

## Phase 3 - Plans and Multi-Position

- Plan system: `plan 1`, `plan 2`, `plan 3`
- `plan 1 status`: show positions in a plan
- `close plan 2`: close all positions in a plan
- Optional strategy metadata per plan: scalp, swing, hedge

Examples:

```text
long SOL 4x entry 148 sl 142 tp 158 size 100usdt plan 1
short AVAX 2x entry 38.2 sl 40 tp 34 qty 1.5 plan 3
close plan 1
```

## Phase 4 - Scanner and Signals

- `/scan`: scan Binance Futures markets
- Initial signals: RSI overbought/oversold, volume spike, MACD cross
- Send suggestion with inline `[Trade this]` button
- Scheduled scan every hour
- Store signals in MongoDB and generated reports in S3 only when needed

Example:

```text
Signal found:

BNB/USDT LONG setup
Entry zone: 580-585
SL: 565 | TP: 620
RSI(14): 28.4 | Volume: +180%

[Open BNB Long] [Skip]
```

## Phase 5 - Alerts and Monitoring

- Alert when SL distance is under 0.5%
- Alert when TP is hit partially
- Daily P&L summary at configured local time
- Position monitor loop every 30 seconds
- Persist alert state to avoid duplicate alerts after restart

## Optional Fiber v3 API

Add Fiber v3 only when one of these is needed:

- Telegram webhook receiver
- Health/readiness endpoints for deployment
- Admin or QA endpoints for local testing
- Manual trigger endpoints for scanner or monitor

Suggested routes:

```text
GET  /healthz
GET  /readyz
POST /telegram/webhook
POST /admin/scan
```

Keep HTTP handlers thin. They should call application services instead of containing trading logic.

## MVP Order of Work

1. Initialize Go module, config loader, logger, and `.env.example`
2. Telegram auth and `/start`, `/help`, `/status`
3. Free-form parser with table-driven tests
4. Domain types and order intent confirmation flow
5. MongoDB repositories and indexes for audit events, intents, and confirmations
6. Confirmation TTL and idempotency flow
7. Exchange interface, exchangeInfo filter cache, and dry-run/testnet Binance adapter
8. Open/close position flow in testnet or dry-run
9. Inline buttons for common follow-up actions
10. Position status with P&L
11. Phase 2 management actions
12. Phase 3 plan grouping
13. Phase 4 scanner
14. Phase 5 alerts and reports

## Review Gates

Before any merge or handoff:

- `go test ./...` passes
- Parser edge cases are covered
- Exchange adapter can be mocked
- Real trading remains disabled by default
- No secrets or `.env` files are tracked
- MongoDB and S3 calls are behind interfaces
- Claude review/QA checks trading safety, tests, and regressions
