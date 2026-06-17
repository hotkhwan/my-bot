---
name: bot-backend-implementation
description: Use when implementing approved trading-bot backend slices in my-bot (module bottrade); keeps Go changes scoped to the approved plan, parser grammar, exchange safety, persistence, tests, and trading-safety rules.
---

# Bot Backend Implementation

Use only after the relevant slice in `trading_bot_plan.md` is approved, or the task is a clearly bounded lite change. Codex is the primary implementer; Claude may take bounded slices.

## Inputs

- `AGENT.md` (canonical instructions) and `AGENTS.md` (Codex entrypoint)
- `CLAUDE.md` (review stance and trading-safety priorities)
- `trading_bot_plan.md` (approved scope)
- `TRADING_BOT_REVIEW.md` (architecture context)
- `.claude/skills/go-trade-code-style/SKILL.md` and `.claude/skills/trading-bot/SKILL.md`
- existing package patterns under `internal/`

## Implementation Rules

- Keep scope inside the approved plan slice; name the files you own.
- Use existing repo patterns before adding abstractions; do not change unrelated files.
- Put external dependencies behind interfaces: `internal/exchange/binance`, `internal/storage/mongo`, `internal/storage/object`. Keep them mockable.
- Real trading stays disabled by default; never wire a live exchange path on by accident.
- Parser changes (`internal/parser`) keep output typed and validated; open intents require explicit `size <amount>usdt` or `qty <amount>`; `close <symbol>` is 100% close, partial needs a percentage.
- Handle Binance precision, min notional, leverage, and margin mode in `internal/exchange`/`internal/orders` before sending any order.
- Pending confirmations persist in MongoDB with TTL; status transitions use atomic conditional updates; Telegram callbacks are idempotent.
- Enforce Telegram authorization on every command and callback; admin from `TELEGRAM_ADMIN_USER_ID`, MVP allowlist from `TELEGRAM_ALLOWED_USER_IDS`.
- Never log or commit secrets.
- Avoid unrelated refactors and generated churn.

## Close-Out

- Run `go build ./...` and the relevant `go test ./...` scope.
- Add or extend table-driven tests for any parser grammar change.
- Prefer tests that need no network; mock exchange, MongoDB, and S3.
- Report tested vs untested areas, and flag any code path that could touch a real exchange account.
- Hand off per `CLAUDE.md` "Handoff To Codex": changed files, tests run + results, residual risks.
