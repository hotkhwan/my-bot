# ANNY Plan Index

Status: planning index. This file separates active plans from completed
reference decisions so future sessions do not re-open solved questions.

## Active Plans

| File | Status | Use now | Next action |
|---|---|---|---|
| `ANNY_ROADMAP.md` | active | Mission Zero / Mission One roadmap | Continue open Mission Zero milestones: fee-adjusted validation, Mission confirmation + Flight Recorder on testnet, restart-safe timed jobs. |
| `PRODUCTIONIZATION.md` | active backlog | Auth, realtime, per-user keys, autonomous trading | Implement in order: Telegram auth/JWT, per-user Binance credentials, realtime user-data gateway, real campaign Trader last. |
| `TRADINGVIEW_AI.md` | active/reference | TradingView webhook and AI advisor contract | Add concrete context providers only when provider keys/config are ready; keep AI autotrade off by default. |

## Done / Reference Plans

| File | Status | Why it stays |
|---|---|---|
| `trading_bot_plan.md` | done-reference baseline | Canonical MVP architecture and safety rules. Consult before backend work; do not treat every old phase bullet as current open work. |
| `TRADING_BOT_REVIEW.md` | done-reference review | Historical architecture review that resolved stack and safety defaults. Consult for rationale. |

## Current Open Work Pointers

Detailed feature ownership and "where to continue" notes live in
[`../contract/README.md`](../contract/README.md).

Use status `done-reference` instead of moving old planning files away. The
history is useful, but active implementation should follow the contracts,
roadmap, and current source-of-truth docs.
