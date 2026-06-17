---
name: bot-architecture-review
description: Use when reviewing my-bot plans, PRs, or implementation readiness; checks scope against trading_bot_plan.md, trading-safety invariants, boundary interfaces, persistence, and rollout risk before implementation.
---

# Bot Architecture Review

Use this skill for review gates and focused implementation-readiness checks. Codex implements; this review decides if a slice is ready.

## Review Inputs

Read only the needed artifacts:
- `AGENT.md` (canonical) and `AGENTS.md`
- `CLAUDE.md` (review priorities)
- `trading_bot_plan.md` (approved scope, phases)
- `TRADING_BOT_REVIEW.md` (architecture context)
- relevant changed files or PR diff under `internal/`

## Review Bar

Approve when the plan is at least 80% ready and remaining gaps are cheaper to resolve during implementation. Block only issues that break architecture, trading safety, or execution. Mark as follow-up when a fix would take more than ~30 minutes and does not invalidate the current plan.

### Block on
- A path that could enable real trading by accident, or weaken the `REAL_TRADING_ENABLED` / `DRY_RUN` / `BINANCE_TESTNET` guard.
- Missing Telegram authorization on a command or callback; admin/allowlist/subscription boundary unclear.
- Order confirmation missing, non-idempotent callbacks, or non-atomic confirmation status transitions.
- Pending confirmations not persisted in MongoDB with TTL (memory-only), or restart-unsafe plans/alert state.
- Parser accepting opens without explicit `size`/`qty`, or `close` semantics wrong (100% vs partial).
- Exchange order path missing precision / min notional / leverage / margin-mode handling.
- External dependency (Binance, MongoDB, S3) not behind a mockable interface.
- Secrets exposed in logs, messages, repo, or config.

## Required Checks

### Boundaries and safety
- External services sit behind interfaces: `internal/exchange/binance`, `internal/storage/mongo`, `internal/storage/object`.
- Trading-safety invariants from `CLAUDE.md` hold for the touched surface.
- System-of-record for confirmations/plans/positions is clear and restart-safe.

### Scope
- Change stays inside the approved `trading_bot_plan.md` slice; no unrelated refactors.
- If reality differs from the plan, the plan is updated before close-out.

### Tests and rollout
- Validation matrix (see `bot-qa-validation`) covers the touched surface; parser changes have table-driven tests.
- Deploy/env/secret impact (see `bot-devops-release`) is understood and ordered.

## Output Format

Answer in this order:
1. Findings, sorted by severity, with `file:line`/section references.
2. Open questions / assumptions.
3. Short verdict — use one of:
   - `Plan requires revision before implementation.`
   - `Plan is ready to implement and validate.`
