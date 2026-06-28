# ANNY — Subscription & Founder Plan — Source of Truth

The launch / access model. Decided 2026-06-28. Not built yet (see "Build order"); this is the
target so we don't hack the DB later.

## Golden rule

**Give the Founder BADGE for life — NOT Commander for life.** Commander includes real AI +
real trading + real support cost; in 3 years AI could cost 10× more. A lifetime Commander grant
is an uncontrollable long-term cost. So:

- **Founder Badge** → lifetime (identity/loyalty; cheap; the thing people actually brag about).
- **Commander access** → time-boxed: **free until GA / ANNY v1.0**, then drops to **Captain
  Lifetime** for founders. Frame it as a **Founder's License** ("Founder 2026 ✓ · Commander free
  until ANNY v1.0").

## Access model (replaces FREE_SUB_OPEN)

`PRIVATE_BETA=true` (default) = **invite-only**. Flow: Landing → Request Access → Pending →
Approve → Commander (pioneer perk, already implemented in tierOfSubject). Set `PRIVATE_BETA=false`
at the public launch; approved users then fall back to their stored tier.

## Subscription model (target schema)

Don't store just a `plan` string. Store a structured subscription so campaigns/referrals/gifts/
beta-testers all fit without DB hacks:

```go
type Subscription struct {
    Plan          string    // crew | captain | commander
    Status        string    // active | expired | cancelled
    Source        string    // mission_zero | referral | gift | beta | purchase | ...
    FounderNumber int       // 0 = not a founder; else the badge number
    ExpiresAt     time.Time // zero = no expiry
}
```

Example: `{ "plan":"commander", "status":"active", "source":"mission_zero", "founderNumber":4, "expiresAt":"2027-12-31" }`.

`/api/me` + `tierOfSubject` consult `Status`/`ExpiresAt`: an expired Commander → Captain (founder)
or Crew (non-founder). The Flight Recorder / on-chain proof can carry a `modelVersionHash`
(see [secret-model.md](secret-model.md)) — never the formula.

## Launch phases (tech-flavoured caps, not "first 100")

| Phase | Count | Notes |
|-------|-------|-------|
| Core Team | 5 | founders #001–005 |
| **Alpha — "Mission Zero"** | **32** (or 64) | the real Commander grants; pilots/Captains |
| Closed Beta | 128 | invite |
| Open Beta | invite | |
| Public | free (Crew) | |

Keep **Commander free** to a small number (≈25–32) — Commander = real AI + real trading + real
support; 100 heavy users is unsupportable. The **Founder Badge** count can be larger than the
Commander-free count if desired (badge ≠ cost). "MISSION ZERO · 32 Pilots · closed" reads more
on-brand (Tech/Cyber) than "first 100".

## Profile (the "market killer")

Show identity, not promises: `Founder #004 · Joined 2026-06-28 · Mission 0 · 🟣 Founder · Access:
Commander (until v1.0)`. People will want the **badge** more than Commander itself.

## Build order (don't build expiry early)

1. ✅ `PRIVATE_BETA` + pioneer perk (approved → Commander during beta) — done.
2. ✅ Admin tools: `/pending` (with timestamps), `/approve <id> [plan]`, `/reject <id>`, web revoke.
3. ⏳ Near launch: `Subscription` struct + `FounderNumber` counter (cap Mission Zero at 32/64),
   `ExpiresAt` checks in `tierOfSubject`/`/api/me`, Founder badge on Profile, and the
   GA-downgrade (Commander → Captain-lifetime for founders) when `PRIVATE_BETA=false`.

Aligns with the Legal Gate ([../legal/thai-sec-design-principles.md](../legal/thai-sec-design-principles.md)):
the Founder offer is access/identity, never a guaranteed-return or solicitation.
