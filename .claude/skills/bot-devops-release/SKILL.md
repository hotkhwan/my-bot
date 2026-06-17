---
name: bot-devops-release
description: Use for my-bot release readiness, Fly.io deploy, branch flow, env/secret handling, and develop-to-main PR checks before shipping the trading bot.
---

# Bot DevOps Release

Use this skill when a task changes deploy behavior or is ready for PR/release close-out.

## Checks

- Branch flow matches repo rules: `feature -> develop -> main` via PR; no direct commits to `main`.
- Build is green: `go build ./...` and the relevant `go test ./...` scope pass.
- Deploy config is consistent: `Dockerfile`, `fly.toml`, `.dockerignore`. The `app` name in `fly.toml` matches the Fly app.
- Telegram polling mode runs exactly one instance (getUpdates allows one poller); do not scale past 1. If switching to webhook, `HTTP_ENABLED=true`, `PUBLIC_WEBHOOK_URL` set, and an inbound service is configured.
- Trading-safety flags are correct for the target environment:
  - Concept/test: `DRY_RUN=true`, `BINANCE_TESTNET=true`, `REAL_TRADING_ENABLED=false`.
  - Live requires explicit, reviewed opt-in: `DRY_RUN=false`, `BINANCE_TESTNET=false`, `REAL_TRADING_ENABLED=true`, plus real `BINANCE_API_KEY`/`BINANCE_API_SECRET`.
- Secrets live in `fly secrets` (or the Fly dashboard), never in `fly.toml`, the repo, or logs: `TELEGRAM_BOT_TOKEN`, `MONGODB_URI`, `BINANCE_API_*`, `S3_*`, `STRIPE_*`, `AI_API_KEY`.
- Required config present so the bot boots: `TELEGRAM_BOT_TOKEN`, `MONGODB_URI`, and `TELEGRAM_ADMIN_USER_ID` or `TELEGRAM_ALLOWED_USER_IDS`.
- MongoDB (Atlas) is reachable and TTL indexes for pending confirmations are in place.
- Any new env flag or migration is documented and ordered.

## Output

Summarize release readiness, blockers, env/secret ordering, smoke evidence, the active trading-safety mode, and remaining rollback risk. Flag any change that could enable a real exchange path.
