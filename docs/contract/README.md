# ANNY Feature Contracts

Status: handoff map for future Codex, Claude, and specialist-agent sessions.

Use this directory when a session needs to understand what a feature owns, where
to continue work, which tests matter, and which reviewer/skill should be used.
This is not a product promise and not financial advice.

## Update Rule

When a feature changes, update its contract in the same PR or commit:

- Status: `active`, `done-reference`, `blocked`, or `deferred`.
- Code surfaces: packages, commands, routes, config, and storage touched.
- Source docs: canonical docs that override chat memory.
- Next work: concrete follow-up, not broad ambition.
- Validation: exact tests or smoke checks expected.
- Gate: security, legal, architecture, QA, or release review required.

## Agent And Skill Routing

| Work type | Agent | Skill / source | Status |
|---|---|---|---|
| Codex implementation and release flow | Codex primary | `.codex/skills/my-bot-dev/SKILL.md` | Ready |
| Bounded Go backend work | `.claude/agents/go-backend-implementer.md` | `.claude/skills/bot-backend-implementation/SKILL.md`, `.claude/skills/go-trade-code-style/SKILL.md` | Ready |
| Review and QA | `.claude/agents/review-qa-tester.md` | `.claude/skills/bot-qa-validation/SKILL.md` | Ready |
| Security and trading-risk review | `.claude/agents/security-trading-risk-reviewer.md` | `.claude/skills/bot-security-review/SKILL.md` | Ready |
| Architecture review | `.claude/agents/architecture-planner.md` | `.claude/skills/bot-architecture-review/SKILL.md` | Ready |
| DevOps and release | Codex primary, reviewer may use release skill | `.claude/skills/bot-devops-release/SKILL.md` | Ready |
| UX / dashboard | Codex primary, UX reviewer as needed | `.claude/skills/ux-ui-web/SKILL.md` | Ready |
| Legal and communication | `.claude/agents/legal-comms-reviewer.md` | `.claude/skills/legal-comms-review/SKILL.md`, `docs/legal/thai-sec-design-principles.md` | Ready as internal gate; external legal counsel still required for production-risk changes |

## Cross-Cutting Release Contract

Source files:

- `.github/workflows/deploy.yml`
- `fly.toml`
- `internal/version/version.go`
- `docs/AGENT_MEMORY.md`

Current contract:

- `develop` auto-deploys to Fly testnet app `aliza-trading`.
- Public testnet URL is `https://aliza-trading.fly.dev/`.
- `main` must not deploy to the develop/testnet app.
- Production/mainnet may use Fly.io, but must be a separate app with separate
  secrets, database, domain, GitHub environment approval, and explicit
  production review.
- Every deployed release bumps `internal/version/version.go`.
- Every close-out reports the version and commit hash.

Validation:

- `go build ./...`
- `go test ./...`
- GitHub Actions deploy success for `develop`
- `GET https://aliza-trading.fly.dev/healthz` returns `{"status":"ok"}`

## Feature Contracts

| Feature | Status | Code surfaces | Source docs | Next work | Validation and gates |
|---|---|---|---|---|---|
| Telegram command MVP | active | `internal/telegram`, `internal/parser`, `internal/orders`, `internal/plans` | `docs/plan/trading_bot_plan.md` | Keep parser grammar frozen; extend only with tests and confirmation idempotency. | Parser/telegram/order tests; security + QA. |
| Auth and early access | active | `internal/api`, `internal/users`, `internal/auth`, `internal/interest`, `internal/app` | `docs/architecture/subscription-founder.md`, `docs/plan/PRODUCTIONIZATION.md` | Continue Telegram auth/JWT path; keep public self-registration disabled; admin approval remains required. | API auth tests; legal copy review; security review. |
| Per-user Binance credentials | active | `internal/auth`, `internal/api` credential routes, future executor wiring | `docs/security/key-management.md`, `docs/plan/PRODUCTIONIZATION.md` | Wire per-user executors from encrypted credentials; keep withdrawal keys impossible and real trading globally gated. | Credential tests; no secret logging; security review. |
| ANNY Basic v1.2 model | active | `internal/strategy/annybasic`, `internal/indicators`, `internal/backtest`, `internal/dashboard/dist/index.html` | `docs/strategy/anny-basic-v1.2.md`, `docs/strategy/success-model-anny-basic.md` | Complete fee-adjusted paper/walk-forward validation; connect only actionable assessments to mission confirmation and Flight Recorder on testnet. No launchable ANNY Basic setup means edit plan and must show entries needed, launchable setups found, top blocker, next edit hint, and safe Auto/RSI fallback reassessment actions without making ANNY Basic launchable. | Strategy/backtest tests; dashboard e2e; legal wording review. |
| Mission Zero transparency | active | `internal/transparency`, `internal/api/recorder.go`, future proof routes | `docs/vision/mission-zero-opbnb-testnet.md`, `docs/architecture/secret-model.md` | Build opBNB testnet anchor jobs and public `/proof/*` pages exposing hashes and `txHash` only. | Deterministic hash tests; proof sanitization; security + legal. |
| Flight Recorder and journal | active | `internal/journal`, `internal/api/recorder.go`, `internal/transparency` | `docs/vision/mission-zero-opbnb-testnet.md`, `docs/strategy/success-model-anny-basic.md` | Record SL/TP exchange-triggered closes through realtime user-data stream or safe polling fallback. | Journal/recorder tests; data leakage review. |
| Realtime gateway | active | `internal/realtime`, `internal/monitor`, future Binance user-data stream adapter | `docs/plan/PRODUCTIONIZATION.md` | Add Binance user-data stream ingestion; fan out normalized events to Telegram and SSE/web. | Reconnect tests/fakes; restart-safety review. |
| Autonomous campaign testnet | active | `internal/campaign`, `internal/campaignexec`, `internal/telegram`, `internal/journal` | `docs/plan/PRODUCTIONIZATION.md`, `docs/strategy/success-model-anny-basic.md` | Build real campaign Trader last; wait for realized PnL from journal; keep testnet-only until sustained evidence exists. | Campaign tests; security review; legal wording review. |
| TradingView and AI advisor | active | `internal/api` TradingView route, `internal/ai`, `internal/signals` | `docs/plan/TRADINGVIEW_AI.md`, `docs/branding/positioning.md` | Add concrete context providers only behind opt-in API keys; keep `AI_AUTOTRADE_ENABLED=false` by default. | Webhook-secret tests; provider fakes; security + legal. |
| Cloudflare edge policy | done-reference | `docs/architecture/cloudflare-edge-policy.md`, DNS/Cloudflare console | `docs/architecture/cloudflare-edge-policy.md` | Apply DNS/cache/security rules outside repo; never treat edge rules as trading security boundary. | Manual Cloudflare checklist; no credentials in repo. |
| Production/mainnet environment | deferred | future Fly app, future GitHub environment, future production domain | `docs/architecture/cloudflare-edge-policy.md`, `docs/security/key-management.md` | Create separate production Fly app and secrets only after legal/security/ops gates pass. | Release checklist; explicit user approval. |

## Handoff Checklist

Before handing work to another session or agent:

- Link the feature row above.
- Name owned files and changed behavior.
- State current version and commit hash.
- State tests run and tests not run.
- State whether any path can touch real exchange accounts.
- State which gate remains: architecture, security, QA, legal, or release.
