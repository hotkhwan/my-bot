# Project Memory

Persistent collaboration rules for ANNY:

- `develop` is the deploy branch. Never merge to `main` without the user's
  explicit instruction. Report the develop-to-main PR number first.
- Bump `internal/version/version.go` for every deployed release.
- Follow the source-of-truth documents linked from `docs/README.md`, including
  Legal and Security Gates.
- Keep real trading disabled by default. Current Fly deployment is testnet
  execution (`DRY_RUN=false`, `BINANCE_TESTNET=true`,
  `REAL_TRADING_ENABLED=false`).
- Entry execution is maker-first: post-only limit with a bounded wait, then
  market fallback for the unfilled remainder. SL/TP and urgent closes prioritize
  protection over maker fees.
- Paper planning must include estimated fees. Default assumptions are 0.02%
  maker entry and 0.04% taker exit; actual Binance fees depend on the user's
  VIP tier and discounts.
- Entry/exit plans keep separate controls for capital-risk percentage and
  leverage-use percentage. Capital risk must never be interpreted as leverage.
- Plan duration is distinct from execution timeframe. Dev supports a 15-minute
  plan on 1-minute candles; longer plans select 1m, 5m, 15m, or 1h execution
  candles automatically.
- A confirmed dev/testnet mission authorizes a timed close at plan expiry if
  TP/SL has not already closed it. The close is a **durable, Mongo-backed job**
  (`scheduled_closes` + a 30s poller, `internal/api/scheduled_close.go`), so it
  survives API restarts. It is persisted as `awaiting_entry` at prepare (keyed by
  the entry confirmation id) and only armed (`pending`) after the entry actually
  confirms — so a crash before the entry can never close an unrelated position.
- Public visitors do not self-register from the dashboard. They can submit an
  interest email, stored durably in MongoDB `interest_signups`.
- Early access flow is Interest → admin sends invite → account registration →
  member waits for final admin approval while `FREE_SUB_OPEN=false`.
- When early access is full, use `Close & waitlist`: send the capacity reply,
  retain the lead with `waitlisted` status, and contact them again at launch.
- Preserve user changes, including the plan documents moved into `docs/plan`.
- Use `.codex/skills/my-bot-dev/SKILL.md` as the Codex-native project workflow.
- After every commit or deploy, report the app version from
  `internal/version/version.go` plus the commit hash so testers know exactly
  which build is live.
