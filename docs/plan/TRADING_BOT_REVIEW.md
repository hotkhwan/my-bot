# Trading Bot Plan Review

## Verdict

The original MVP shape is good: Telegram as the control surface, free-form text for speed, inline buttons for follow-up actions, and phased delivery from manual trading to scanner and alerts.

The main issue is stack mismatch. The original plan was Python, `python-telegram-bot`, `ccxt`, and `pandas-ta`. The current direction is Go, optional Fiber v3, MongoDB Atlas, and S3-compatible object storage. The plan has been realigned in `trading_bot_plan.md`.

## Key Changes

- Replace Python with Go for all application code.
- Replace `ccxt` with a Go Binance Futures adapter behind an interface.
- Use MongoDB Atlas for state, audit logs, orders, positions, plans, and signals.
- Use S3 only for generated/uploaded files, not normal bot state.
- Add safety gates before live exchange actions.
- Make parser output typed domain structs instead of loose maps.
- Add Claude/Codex role docs so implementer, reviewer, and QA work in one direction.

## Recommended Architecture Decisions

- Start with Telegram polling for the fastest MVP unless deployment needs webhooks.
- Use comma-separated Telegram user IDs so one owner and a small team are both supported.
- Require explicit `size <USDT>usdt` or `qty <baseQty>` in Phase 1; do not infer position size silently.
- Default Binance margin mode to isolated.
- Require confirmation for every exchange-changing action, even dry-run and testnet.
- Add Fiber v3 only for health endpoints, Telegram webhook mode, or admin/QA endpoints.
- Keep exchange code behind an interface so tests never call Binance.
- Keep MongoDB repositories behind interfaces so parser/order tests stay fast.
- Use exact decimal arithmetic for prices, quantities, and risk calculations.
- Store every important user action and exchange response as an audit event.

## Important Risks To Address Early

- Real trading safety: dry-run and testnet must remain default.
- Idempotency: repeated Telegram callback presses must not duplicate orders. Use MongoDB confirmation records, unique idempotency keys, and deterministic client order IDs.
- Precision: Binance symbol filters, tick size, step size, and min notional must be cached and applied before orders.
- Confirmation: pending confirmations belong in MongoDB with a TTL index, not only in memory.
- Secrets: no `.env`, API key, report export, account dump, full token, or callback secret should be committed or logged.
- Reliability: bot restart should not lose pending confirmations before TTL, active plans, or alert state.
- Parser scope: Phase 1 grammar should stay frozen and table-tested before exchange work begins.
- Confirmation state transitions should use atomic conditional MongoDB updates to prevent two workers from executing the same confirmation.
- `close <symbol>` means close 100%; partial close must include a percentage.

## Storage Recommendation

MongoDB Atlas is the primary store. Use it for all durable application data.

S3-compatible storage is useful, but only for file-like artifacts:

- P&L report exports
- Audit bundle exports
- Backtest reports
- Chart screenshots
- Large JSON/CSV exports

For the MVP, S3 can be optional until reports or uploaded files exist.

## Suggested First Implementation Batch

1. Create Go module and base folders.
2. Add config validation, logger, and `.env.example`.
3. Implement parser with table-driven tests.
4. Add Telegram auth and command skeleton.
5. Add MongoDB repository interfaces and collection/index setup for audit events, intents, and confirmations.
6. Implement confirmation flow, TTL, and idempotency before any exchange order.
7. Add exchange interface, exchangeInfo filter cache, and dry-run/testnet Binance adapter.

## Open Questions

Resolved defaults:

- Telegram runs as polling first.
- Allowed users are comma-separated user IDs.
- Phase 1 requires explicit sizing through `size <amount>usdt` or `qty <amount>`.
- Binance margin mode defaults to isolated.
- Every exchange-changing action requires confirmation.
- Phase 1 confirmation buttons are `[Confirm] [Cancel]`; management buttons start in Phase 2.
