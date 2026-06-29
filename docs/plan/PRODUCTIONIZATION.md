# Productionization design — auth, realtime, multi-user keys, autonomous trading

Companion to AI_TRADING_SYSTEM.md. Covers the remaining work to take the bot
from "components built" to "multi-user product". Grounded in the current code.
Entry point today is **Telegram first**; the web dashboard is secondary. Keep
the stack light — do not adopt heavy IAM before the bot needs it.

## Current state (what exists)

- `internal/users` — username/password (bcrypt) register/login. No sessions/JWT,
  no password reset, no RBAC enforcement. Minimal MVP auth.
- `internal/auth` — per-user Binance credential **encryption** (AES-256-GCM
  Keyring + CredentialService). **Crypto core only — not wired anywhere.** The
  executor still uses one global `BINANCE_API_KEY` from env.
- `internal/monitor` — **polls** positions every ~15s. No WebSocket / realtime.
- `internal/journal` — records trades on **manual close** only (SL/TP triggers
  on the exchange are not yet detected).

---

## 1. Auth — recommendation (do NOT start with Keycloak/Permify)

The bot's primary entry is Telegram, so identity should come from Telegram, not
a password the user invents and forgets (which is exactly what just happened).

**Recommended stack (light, standard, self-host, free):**

```
Telegram Login / Mini App  ->  verify initData hash (HMAC over BOT_TOKEN)
        -> Go API issues a short-lived JWT (HS256, our secret)
        -> Mongo: users / sessions / trading-permission state
        -> Casbin (only when permissions actually grow) for RBAC/ABAC
```

- **Telegram auth** is a real standard for Telegram apps: the client sends
  `initData`; the server verifies the HMAC-SHA256 signature using the bot token.
  No OAuth server, no password, nothing to reset. The web dashboard can use the
  Telegram Login Widget for the same.
- **JWT** issued by our Go API (claims: `sub`, `username`, `role`, `plan`).
  Keep permissions OUT of the JWT (they go stale) — store them in Mongo and
  check per request.
- **RBAC**: start with a `role` + a `permissions[]` array in Mongo and a Go
  middleware. Adopt **Casbin** (Go-native RBAC/ABAC) only when the matrix grows.

**Why not Keycloak / Permify yet:** they're built for multi-tenant orgs with
complex resource permissions (K-Lynx territory). For a Telegram-first trading
bot they add a service to run, a migration, and latency for no current benefit.
Revisit Keycloak (self-host OIDC) only if you add a real multi-org web product.

**Where rolling-our-own hurts at scale (be honest):** password reset, 2FA,
lockout, breach handling — all become yours. Telegram auth sidesteps most of
this by delegating identity to Telegram. The trading-specific gates below are
yours regardless of any auth provider.

**Trading permission state is domain logic, not auth** — keep it explicit:

```json
{ "role": "trader", "plan": "free",
  "liveTradingEnabled": false, "riskAccepted": true,
  "maxOrderUsd": 20, "maxDailyLossUsd": 50 }
```

Never grant live trading just because a user logged in. `liveTradingEnabled`
stays false until explicit opt-in + risk acceptance, on top of the existing
`REAL_TRADING_ENABLED` global gate.

**Immediate gap:** no password reset. Short term — let a user re-register or an
admin delete the Mongo `users` doc. Real fix — move to Telegram auth (no
password) per above.

---

## 2. Realtime (WebSocket) — we have none today

Numbers move on Binance/CoinGlass via **WebSocket**, not polling. For the bot,
the important stream is the **user-data stream** (account/position/order/fill
updates), plus market streams for prices.

```
Binance user-data stream (listenKey)   wss://fstream.binance.com/ws/<listenKey>
Binance market streams                 btcusdt@markPrice@1s, @bookTicker, @trade
        -> ONE Go realtime gateway (ingest once, normalise, throttle)
        -> fan out:  Telegram push (per user)   +   web (WS or SSE)
        -> per open position: live PnL / fill / SL-TP-trigger notifications
```

Rules:
- Do **not** let every web client or user connect to Binance directly — one Go
  gateway owns the connection, reconnect, and subscriptions.
- The **user-data stream** is what makes "each open position reports in
  realtime" work: a fill or an SL/TP trigger arrives as an event → push to
  Telegram and the web → and (see §4) record the close to the journal.
- Cache latest price in memory/Redis; broadcast a small normalised tick.

This also **solves §4** (SL/TP-close journaling) for free: the user-data stream
delivers ORDER_TRADE_UPDATE with realized PnL on close — no polling/income-history
matching needed.

---

## 3. Multi-user Binance API keys — onboarding flow

Each user trades with **their own** Binance key. The encryption core exists
(`internal/auth`); the endpoint and executor wiring do not.

**How a user gets a key (document this in the UI):**
1. Binance → Account → **API Management** → Create API → label it.
2. Enable **Futures**. **Do NOT enable Withdrawals.** Restrict to the server IP
   if possible.
3. Testnet/demo: get the key from `testnet.binancefuture.com` (or the demo env)
   instead — same flow, fake funds.

**Store flow (to build):**
```
POST /api/credentials  { apiKey, apiSecret, testnet }   (authenticated)
   -> auth.CredentialService.Store(userID, keys)         (AES-256-GCM seal)
   -> Mongo binance_credentials (sealed; never plaintext)
```

**Use flow (to build):** the order path must build a per-user executor from the
user's decrypted key instead of the single env key. Today `newTradingServices`
makes one global executor — it needs to become per-user (cache executors by
userID; `auth.CredentialService.Load(userID)` -> executor).

**Multi-user testing:** owner + partner each (1) Telegram-login, (2) paste their
own testnet key via /api/credentials, (3) trade on testnet → the journal/report
accumulate per user → real comparative statistics. `liveTradingEnabled=false`
and `REAL_TRADING_ENABLED=false` keep everyone on testnet until proven.

---

## 4. Final autonomous-trading pieces

### 4a. SL/TP-close journaling
Best done via the realtime user-data stream (§2): on ORDER_TRADE_UPDATE with a
close + realized PnL, record a `journal.Trade`. Polling fallback: the monitor
tracks open positions and, when one vanishes, queries `/fapi/v1/income`
(REALIZED_PNL) for the symbol — more fragile; prefer the stream.

### 4b. Real campaign Trader
`campaign.Engine` needs a real `Trader.Trade(decision) -> PnL` that: places the
order via the order service, then **waits for the position to resolve** (via the
user-data stream / journal), and returns the realized PnL. This closes the
autonomous loop. **Highest-risk piece** — build it last, validate on testnet
over many trades, keep `REAL_TRADING_ENABLED=false` until the journal shows a
sustained, fee-adjusted edge.

---

## Suggested order

1. Telegram auth + JWT + Mongo sessions (replaces fragile password login).
2. Per-user Binance keys: `/api/credentials` + per-user executor (wire
   `internal/auth`).
3. Realtime gateway (user-data stream) → Telegram + web push; this also feeds
   SL/TP-close journaling (4a).
4. Real campaign Trader (4b) — last, heavily testnet-validated.
