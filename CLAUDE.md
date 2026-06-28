# CLAUDE.md

Claude's main role in this repo is review and QA. Claude may help implement small, bounded slices when the user or Codex needs parallel work, but Codex remains the primary implementer.

## Read First

Before working:

1. Read `AGENT.md`.
2. Read `trading_bot_plan.md`.
3. Read `TRADING_BOT_REVIEW.md` for architecture context.
4. Read `docs/` (Source of Truth): `docs/legal/thai-sec-design-principles.md`, `docs/architecture/secret-model.md`, `docs/branding/positioning.md`, `docs/security/key-management.md`.
5. Use the relevant skill under `.claude/skills/`.

## Legal Gate

Every user-facing or trading feature PR must pass the **Legal Gate** in `docs/legal/thai-sec-design-principles.md` (alongside the Security Gate). Answer the 5 questions; if any is "yes" or "unsure", it needs legal review before production — do not ship straight to prod. Forbidden in copy/UI/marketing: guaranteed profit, soliciting investment with returns, copy-trading invites, taking custody of funds. Marketing leads with risk/transparency/consistency/discipline, not profit/signal/win-rate. Frame AI as "AI Assessment · Confidence · Suggested Action (user confirms)", never "AI says BUY". Keep the formula secret (execution layer), expose only verifiable results.

## Review Stance

Lead with findings, ordered by severity. Focus on bugs, trading risk, security, missing tests, and behavior regressions. Keep summaries short and put them after findings.

Review priorities:

- Real trading cannot be enabled accidentally.
- Telegram user authorization is enforced on every command and callback.
- Admin access comes from `TELEGRAM_ADMIN_USER_ID`; non-admin trading access must eventually require active subscription state.
- Order confirmation and callback idempotency are present.
- Pending confirmations are stored in MongoDB with TTL, not only in memory.
- Confirmation status transitions use atomic conditional updates.
- Phase 1 parser requires explicit `size <amount>usdt` or `qty <amount>`.
- `close <symbol>` means close 100%; partial close requires a percentage.
- Parser output is typed, validated, and tested.
- Binance precision, min notional, leverage, and margin mode are handled before orders.
- Exchange, MongoDB, and S3 are mockable.
- Secrets are not logged or committed.

## QA Expectations

- Prefer tests that do not require network access.
- Add table-driven cases for every parser grammar change.
- Verify restart safety for pending confirmations, plans, and alert state.
- Test duplicate Telegram callbacks.
- Test invalid or ambiguous trade text.
- Test missing size/qty on open intents.
- Test dry-run and testnet paths before any live path.

## Implementation Help Rules

When helping implement:

- Keep scope narrow and name owned files.
- Do not change unrelated files.
- Follow Go style in `.claude/skills/go-trade-code-style/SKILL.md`.
- Follow trading-bot domain rules in `.claude/skills/trading-bot/SKILL.md`.
- Prefer interfaces at external boundaries.
- Update docs when commands, env vars, or behavior change.

## Handoff To Codex

When handing work back:

- List changed files.
- List tests run and results.
- State residual risks or manual checks.
- Flag any code path that could touch real exchange accounts.
