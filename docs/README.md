# ANNY — Project Docs (Source of Truth)

Team source of truth for principles that outlive any single conversation. Read before adding
user-facing or trading features.

- [AGENT_MEMORY.md](AGENT_MEMORY.md) — persistent release, merge, fee, and collaboration decisions.
- [legal/thai-sec-design-principles.md](legal/thai-sec-design-principles.md) — **Legal by
  Design**: forbidden language, what we must do, marketing wording, and the **Legal Gate**
  (5 questions every feature PR must answer).
- [architecture/cloudflare-edge-policy.md](architecture/cloudflare-edge-policy.md) - **Cloudflare
  edge policy**: DNS, SSL/TLS, edge security, cache, traffic policy, and proof-page
  exposure rules for `joinanny.com`.
- [contract/README.md](contract/README.md) - **Feature contracts**: feature ownership,
  next work, validation gates, and agent/skill routing for future sessions.
- [architecture/subscription-founder.md](architecture/subscription-founder.md) — **Launch / Founder
  plan**: PRIVATE_BETA invite-only; Founder Badge for life but Commander time-boxed (free until GA
  → Captain lifetime); `Subscription` schema; Mission Zero caps (32/64/128).
- [architecture/secret-model.md](architecture/secret-model.md) — **Secret model**: transparent
  results / secret method; public-paper vs private-execution; 3-service split
  (`anny-api` / `anny-execution-engine` / `anny-skill-model`); decision-only internal API;
  `modelVersionHash`; opBNB stores hashes, never formulas.
- [branding/positioning.md](branding/positioning.md) — **Positioning**: "AI Trading
  Companion"; risk-first vocabulary; AI-assessment wording; disclaimers.
- [security/key-management.md](security/key-management.md) — **Non-custodial**: user holds key,
  USDT, private key; secrets in Fly; future private skill-model container.

Two gates run before merge on user-facing/trading features: the **Security Gate**
(`security-trading-risk-reviewer`) and the **Legal Gate** (legal doc above). See CLAUDE.md.

Strategy and roadmap:

- [plan/README.md](plan/README.md) - active vs done-reference planning index.
- [strategy/anny-basic-v1.2.md](strategy/anny-basic-v1.2.md) - first versioned
  ANNY strategy model and its delivery gates.
- [strategy/success-model-anny-basic.md](strategy/success-model-anny-basic.md) - Mission Zero
  success criteria for risk-first, recorder-first, transparency-first ANNY Basic validation.
- [vision/mission-zero-opbnb-testnet.md](vision/mission-zero-opbnb-testnet.md) - opBNB testnet
  transparency layer for public hashes and txHash-only proof exposure.
- [plan/ANNY_ROADMAP.md](plan/ANNY_ROADMAP.md) - Mission Zero and Mission One
  roadmap, including current model delivery status.
