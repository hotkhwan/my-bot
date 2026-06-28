# ANNY — Project Docs (Source of Truth)

Team source of truth for principles that outlive any single conversation. Read before adding
user-facing or trading features.

- [legal/thai-sec-design-principles.md](legal/thai-sec-design-principles.md) — **Legal by
  Design**: forbidden language, what we must do, marketing wording, and the **Legal Gate**
  (5 questions every feature PR must answer).
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
