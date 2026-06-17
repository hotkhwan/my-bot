---
name: bot-security-review
description: Use for my-bot security review: real-trading guard, Telegram authorization, secret handling, exchange order safety, confirmation idempotency, and persistence safety before approving or shipping changes.
---

# Bot Security Review

Use this skill for security-sensitive plan, PR, or implementation review of the trading bot.

## Focus Areas

- **Real-trading guard:** real trading cannot be enabled by accident. `REAL_TRADING_ENABLED=true` must require `DRY_RUN=false` and `BINANCE_TESTNET=false` and real keys; the default path is dry-run + testnet.
- **Telegram authorization:** enforced on every command and callback. Admin from `TELEGRAM_ADMIN_USER_ID`; MVP allowlist from `TELEGRAM_ALLOWED_USER_IDS`. Non-admin trading access must eventually require active subscription state.
- **Confirmation flow:** order confirmation present; callbacks idempotent; duplicate Telegram callbacks cannot double-execute. Confirmation status transitions use atomic conditional updates.
- **Persistence safety:** pending confirmations stored in MongoDB with TTL (not only in memory); restart-safe for confirmations, plans, and alert/scanner state.
- **Exchange order safety:** Binance precision, min notional, leverage cap, and margin mode validated before any order; parser requires explicit `size <amount>usdt` or `qty <amount>` on opens; `close <symbol>` is 100%, partial needs a percentage.
- **Secret handling:** no secrets in logs, errors, Telegram messages, the repo, or `fly.toml`. Covers `TELEGRAM_BOT_TOKEN`, `BINANCE_API_*`, `MONGODB_URI`, `S3_*`, `STRIPE_*`, `AI_API_KEY`.
- **Inbound validation:** Telegram webhook secret, TradingView webhook secret (`TRADINGVIEW_WEBHOOK_SECRET`), Stripe webhook signature, and any external URL/file input are validated.
- **AI autotrade:** `AI_AUTOTRADE_ENABLED` may only act under dry-run or testnet in this phase; never silently place live orders.

## Review Output

Lead with concrete findings and `file:line` references. Classify each as blocker, follow-up, or note. Always flag any code path that could touch a real exchange account. Do not block on generic hardening unless the current change creates a real risk.
