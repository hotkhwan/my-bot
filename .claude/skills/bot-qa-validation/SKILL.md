---
name: bot-qa-validation
description: Use when designing or reviewing my-bot validation plans, parser tests, exchange-safety checks, restart-safety, and tested-vs-untested evidence for trading-bot changes.
---

# Bot QA Validation

Use this skill at plan review and close-out.

## Validation Matrix

Cover only the surfaces touched by the task:

- **Parser (`internal/parser`):** table-driven cases for every grammar change — valid intents, invalid/ambiguous text, missing `size`/`qty` on opens, `close` 100% vs partial percentage. Output is typed and validated.
- **Exchange safety (`internal/exchange`, `internal/orders`):** precision rounding, min notional, leverage cap, margin mode, dry-run and testnet paths exercised before any live path.
- **Confirmation flow (`internal/telegram`, `internal/orders`):** duplicate-callback idempotency; status transitions are atomic; expiry via MongoDB TTL.
- **Authorization:** unauthorized user id is rejected on every command and callback; admin vs allowlist vs non-admin.
- **Persistence/restart safety:** pending confirmations, plans, and alert/scanner state survive a restart (`internal/storage/mongo`, `internal/plans`, `internal/scanner`, `internal/monitor`).
- **Storage (`internal/storage/object`):** S3 interactions are mocked; no live bucket needed for tests.
- **Deploy/env impact:** safety flags and required config behave as expected (see `bot-devops-release`).

## Evidence Rules

- Prefer tests that need no network; mock exchange, MongoDB, and S3.
- Prefer one batched validation pass near close-out.
- Report the exact commands run (`go build ./...`, `go test ./...`) and whether they passed.
- Report smoke paths tested and untested.
- If a test cannot run, explain the blocker and the residual trading risk.
