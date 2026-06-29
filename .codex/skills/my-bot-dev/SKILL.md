---
name: my-bot-dev
description: Codex workflow for implementing, reviewing, testing, releasing, and deploying ANNY/my-bot.
---

# ANNY Codex Development Skill

Read `AGENT.md`, `CLAUDE.md`, `docs/README.md`, and
`docs/AGENT_MEMORY.md` before substantial work. Treat all documents linked by
`docs/README.md` as source of truth.

For trading changes, enforce dry-run/testnet/live gates, explicit confirmation,
idempotency, isolated margin, exchange filters, and exact decimal math. Prefer
maker-first entry execution with a bounded wait and taker fallback; never delay
protective exits solely to save fees.

For dashboard changes, follow `.claude/skills/ux-ui-web/SKILL.md` and run its
JavaScript/ID checks plus Playwright where available. For Go changes, follow
`.claude/skills/go-trade-code-style/SKILL.md`.

Before release, run `go build ./...` and `go test ./...`, verify no secrets,
bump `internal/version/version.go`, commit to `develop`, and deploy Fly.io only
when authorized. Never commit directly to `main`. Open or identify the
develop-to-main PR, report its `#number`, and wait for the user to explicitly
order the merge.

Every user-facing or trading change must pass the Legal Gate in
`docs/legal/thai-sec-design-principles.md` and the Security Gate in
`.claude/skills/bot-security-review/SKILL.md`.
