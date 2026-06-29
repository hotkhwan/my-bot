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
- Public visitors do not self-register from the dashboard. They can submit an
  interest email, stored durably in MongoDB `interest_signups`.
- Early access flow is Interest → admin sends invite → account registration →
  member waits for final admin approval while `FREE_SUB_OPEN=false`.
- Preserve user changes, including the plan documents moved into `docs/plan`.
- Use `.codex/skills/my-bot-dev/SKILL.md` as the Codex-native project workflow.
