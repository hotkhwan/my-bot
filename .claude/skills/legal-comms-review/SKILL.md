---
name: legal-comms-review
description: Use for ANNY legal/communications review before shipping user-facing copy, proof pages, subscription/access messaging, AI wording, or trading-related claims.
---

# Legal Communications Review

Use this skill for ANNY user-facing copy, public docs, proof pages, subscription
flows, social/landing wording, and any feature that could be read as financial
or investment advice.

## Required Sources

- `docs/legal/thai-sec-design-principles.md`
- `docs/branding/positioning.md`
- `docs/security/key-management.md`
- `docs/architecture/secret-model.md`

## Blockers

Block release if copy or behavior:

- Guarantees profit or implies guaranteed returns.
- Solicits investment by using returns as the hook.
- Invites copy-trading or following another user's trades.
- Implies ANNY holds, controls, or receives user funds.
- Presents AI as "AI says BUY" instead of an assessment the user controls.
- Hides losses, failed executions, rejected missions, or proof failures.
- Exposes private strategy logic, user identity, exchange account, API keys, or
  raw order payloads in public proof.

## Required Framing

- ANNY is an AI Trading Companion.
- Lead with risk, transparency, consistency, discipline.
- Use "AI Assessment", "confidence", and "suggested action" language.
- State that trading digital assets involves substantial risk.
- State that ANNY does not guarantee profits and is not financial advice.
- Public proof exposes hashes and `txHash` only.

## Output

Report:

1. Findings, sorted by severity, with file/section references.
2. Required wording changes.
3. Whether external legal review is required before production.
