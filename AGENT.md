# AGENT.md

This repo is a Go Telegram trading bot for Binance Futures. Codex is the primary implementer. Claude is the primary reviewer and QA tester, and may help implement bounded slices when work is large.

## Session Startup Checklist

1. Read `AGENT.md`, `CLAUDE.md`, `docs/AGENT_MEMORY.md`, and `docs/plan/trading_bot_plan.md`.
2. Read `docs/plan/TRADING_BOT_REVIEW.md` when planning larger changes.
3. Check the current tree before editing. Preserve user changes.
4. Keep the stack aligned with Go, optional Fiber v3, MongoDB Atlas, and S3-compatible storage.
5. Keep real trading disabled by default.
6. Use `.codex/skills/my-bot-dev/SKILL.md` for the Codex development/release workflow.

## Core Direction

- Language: Go.
- HTTP framework: Fiber v3 only when REST endpoints, health checks, admin endpoints, or Telegram webhooks are needed.
- Telegram mode: polling first for MVP.
- Database: MongoDB Atlas.
- File/object storage: S3-compatible cloud storage for generated files only.
- Exchange: Binance Futures behind an exchange interface.
- Telegram: owner/admin comes from `TELEGRAM_ADMIN_USER_ID`; private-MVP allowlist can use `TELEGRAM_ALLOWED_USER_IDS`.
- Multi-user direction: non-admin users must be backed by MongoDB user/subscription state before trading access.

## Agent Roles

- Codex: implementer, architecture owner, code changes, tests, and integration cleanup.
- Claude: review and QA tester first. Claude should focus on correctness, risk, regressions, missing tests, security, and trading safety.
- Claude helper implementation: allowed for isolated work with clear file ownership, especially tests, docs, parser cases, or small services.

When work is split, each agent must state its owned files or responsibility. Agents must not revert unrelated changes made by others.

## Trading Safety Rules

- Default to `DRY_RUN=true`, `BINANCE_TESTNET=true`, and `REAL_TRADING_ENABLED=false`.
- Do not send live orders unless the user explicitly asks and config allows it.
- Require explicit confirmation for exchange-changing actions.
- Phase 1 requires explicit sizing with `size <amount>usdt` or `qty <amount>`.
- Default Binance margin mode is isolated.
- Make Telegram callback actions idempotent.
- Confirmation status transitions must use atomic conditional updates.
- Phase 1 confirmation buttons are `[Confirm] [Cancel]`; management buttons start in Phase 2.
- Persist audit events for user messages, parsed intents, confirmations, exchange requests, and exchange responses.
- Never log API secrets, Telegram tokens, raw `.env` files, or private account dumps.

## Go Style

- Keep packages small and named by responsibility: `parser`, `orders`, `telegram`, `exchange/binance`, `storage/mongo`.
- Prefer interfaces at boundaries: exchange, repository, object storage, clock, notifier.
- Keep handlers thin. Business logic belongs in services.
- Use `context.Context` for IO, exchange calls, database calls, and background workers.
- Return wrapped errors with useful context.
- Use table-driven tests for parser, risk validation, and service behavior.
- Do not use `float64` for order-critical math. Use a decimal type.
- Avoid global mutable state except process-level wiring in `cmd/tradebot/main.go`.

## Storage Rules

- MongoDB stores durable bot state: users, subscriptions, encrypted exchange credentials, audit events, order intents, orders, positions, fills, plans, signals, alert state, and job state.
- S3 stores file-like artifacts: report exports, audit bundles, chart images, backtest outputs, and large CSV/JSON exports.
- Local files are only for config templates, source code, ignored logs, and temporary artifacts.
- No local DB such as SQLite unless the user explicitly changes the architecture.

## Review And QA Expectations

Before considering work done:

- Run `go test ./...` when a Go module exists.
- Add or update tests for parser/risk/order behavior touched by the change.
- Verify real trading remains off by default.
- Verify no secrets are added to tracked files.
- Verify exchange, MongoDB, and S3 integrations can be mocked.
- Update docs when behavior, commands, env vars, or architecture changes.

## Implementation Priority

1. Go module, config, logger, `.env.example`, `.gitignore`.
2. Parser and domain types with tests.
3. Telegram auth, commands, and inline callback skeleton.
4. MongoDB audit, intent, and confirmation persistence with indexes.
5. Confirmation flow, TTL, and idempotency.
6. Exchange interface plus exchangeInfo filter cache and dry-run/testnet Binance adapter.
7. Position management, plans, scanner, alerts, and reports.
