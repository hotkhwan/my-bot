# ANNY — Legal by Design (Thai SEC) — Source of Truth

> Goal is **not** "avoid the SEC". The goal is to **design the product from day one so it
> does not fall into the elements of a licensed business, and does not communicate in a
> way that breaks the law.** These are different things. Legal is a moat for ANNY, as
> much as the AI/technical edge.

This document is the **Source of Truth** for what features are allowed, what needs legal
review, and what marketing language is forbidden. Every Pull Request that adds a
user-facing feature must pass the **Legal Gate** (below) before merge.

---

## Level 1 — Absolutely forbidden ❌

1. **No guaranteed profit.** Never use, or imply, language like: *Guaranteed Profit, 100%
   Win, Never Lose, Daily 5%, Monthly 30%, AI that always wins* — including phrasings that
   mean the same thing without the word "guarantee".
2. **No soliciting investment with returns.** No *Join now / Yesterday +18% / Subscribe now
   / AI made $2,400 / You can earn too.* Advertising that uses returns as the hook is a
   high-priority concern for regulators.
3. **No copy-trading that invites following.** No *Follow John's Portfolio / Copy this trade
   / Copy our signals.* If ever added, legal must review first.
4. **No taking custody of client funds.** No *Transfer USDT to us / We'll trade for you.*
   That changes the business model entirely (and requires a licence).

## Level 2 — What we should do ✅

- **User uses their own API key.** ANNY never holds money, USDT, or private keys. Trades go
  through the user's own Binance account directly. (Non-custodial by design — see
  [security/key-management.md](../security/key-management.md).)
- **User confirms / opts in.** AI recommendation → *Execute?* Auto-trade is **enabled by the
  user**, explicit opt-in.
- **Risk profile.** Before real trading, ask the user's risk level (Conservative / Balanced /
  Aggressive) and store it.
- **Risk limits.** e.g. Max loss 2%, Max daily loss 5%, Stop trading after 3 losses. ANNY
  emphasises **"helping reduce risk"** over **"helping make profit"**.

---

## Marketing language

Shift the vocabulary **away from** Profit / Signal / Win-rate **toward** Risk / Transparency /
Consistency / Discipline.

| Don't show | Show instead |
|---|---|
| Win Rate 92% | Average Risk 1.2% · Average Hold Time 42 min · Max Drawdown 3.1% |
| "AI says BUY" | "AI Assessment · Confidence 82% · Suggested Action: BUY" |
| BUY BUY BUY feed | Mission · Reason · Risk · Result |

**Required disclaimers (every page / footer):**

> ANNY provides analytical tools and execution automation. It does not guarantee profits and
> should not be considered financial or investment advice. Trading digital assets involves
> substantial risk.

Short forms used in-product:
- Not financial advice.
- Not investment solicitation.
- Past performance does not guarantee future results.
- All missions include both profit and loss records.

**Positioning:** not "AI Trading Bot" — **"AI Trading Companion — Helping you manage risk and
execute strategies. Not financial advice."** See [branding/positioning.md](../branding/positioning.md).

---

## The Legal Gate (every feature PR must answer)

1. Does this feature make ANNY **hold or control** the user's assets?
2. Is there any text that could be read as **guaranteeing returns or soliciting investment**?
3. Does the user **still control their own account and API key**?
4. Are the **risks and limitations disclosed** sufficiently?
5. If this feature were on the **front page**, would a layperson mistake ANNY for a
   **managed-investment / fund-management** service?

**If any answer is "yes" or "unsure" → legal review required before production.** Do not ship
straight to prod. This is a stronger development culture than fixing problems after the fact.

> Pairs with the existing **Security Gate** (see CLAUDE.md / `security-trading-risk-reviewer`).
> Legal Gate + Security Gate both run before merge on user-facing/trading features.
