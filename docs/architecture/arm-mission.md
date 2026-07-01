# Arm Mission — wait-for-setup live entry — GO-WITH-CONDITIONS (testnet pre-launch)

> Status: **GO-WITH-CONDITIONS** for testnet pre-launch engineering. Owner:
> Codex (live execution). External legal sign-off is still required before any
> prod / real-key promotion. Ship **testnet-only** behind the existing
> campaign-live gating.

## 0. Problem

Step 3 ("Launch & confirm") is **one-shot**: `runLiveMission` →
`handleMissionPrepare` ([internal/api/mission.go](../../internal/api/mission.go))
asks ANNY Basic for a setup *at the instant of the click*. If the model says "no
setup right now" (e.g. `execution not aligned`), nothing is staged and the user
dead-ends — even on a launch-ready plan. ANNY Basic is sparse by design (it stops
on 2 consecutive losses, caps at 15 trades, and filters most candles), so "no
setup right now" is common and correct, not a bug.

**Goal:** **Arm** a mission instead of dead-ending — Next → schedules a testnet
mission that waits for the next valid ANNY Basic setup inside the plan window and
auto-enters once, feeding the Live monitor.

## 1. Behaviour

- Next → on a **launch-ready** plan:
  - **Setup now** → unchanged: stage the confirmation immediately (current flow).
  - **No setup now** → **Arm** the mission: persist an armed record, show
    "🎯 Armed — waiting for an ANNY Basic setup (expires in <plan window>)" with a
    **Disarm** button.
- A watcher polls the live decision at the execution interval. On the **first**
  valid setup within the window → enter **once** (testnet), then schedule the
  timed close (reuse `scheduleTimedMissionClose`). Window elapses with no setup →
  **expire** quietly (no order), surfaced in the Flight Recorder.

## 2. Reuse what already exists

- `annyBasicLiveDecision` — the live setup check (unchanged).
- `orders.Prepare` + `orders.Confirm` — staging + atomic execution. **CORRECTION
  (security review):** `orders.Prepare` is **NOT idempotent** — it mints a fresh
  random id every call (`internal/orders/service.go`), so it can NOT be the
  single-entry guard. The single-entry guard is the **atomic `MarkTriggered`
  status transition** (`status=armed && expires_at>now`, conditional
  FindOneAndUpdate); only the caller that wins the claim may Prepare+Confirm. See
  §5.A.
- `scheduleTimedMissionClose` — the auto-close at plan end (already gated).
- The autonomous campaign (memory `autonomous-campaign-gating`, testnet-only) is the
  closest existing "watch & enter" machinery — the watcher should follow its model,
  not invent a parallel one.

## 3. Armed-mission record (persisted)

Per CLAUDE.md ("pending confirmations stored in MongoDB with TTL, not only in
memory" + "verify restart safety"). The current `timedMissions` is an **in-memory
goroutine** (`s.timedMissions.Store`, `scheduleTimedMissionClose`) — **not restart
safe**. Armed missions must persist:

```text
ArmedMission {
  id, userKey/userID, symbol, strategy, side?(nil until trigger),
  capitalUSDT, leverageUsePct, durationWindow,
  armedAt, expiresAt, status: armed|triggered|expired|disarmed,
  idempotencyKey, triggeredConfirmationID?, purgeAt?, createdAt
}
```

On boot, rehydrate `status=armed && now<expiresAt` and resume their watchers.
`expiresAt` is the entry window. TTL must use `purgeAt`, not `expiresAt`:
armed/expired/disarmed records set `purgeAt = expiresAt + retention` (currently
90d), while triggered records leave `purgeAt` unset so testnet-entry audit is
kept.

## 4. Safety model — arming = bounded pre-authorization

Arming is the user's consent to enter **once**, **on testnet**, **within the plan
window**, at **capped leverage** (`missionMaxLeverage`), with the plan's size — not
open-ended discretion. One entry per armed mission; auto-closed at window end.

## 5. Security + Legal Gate (BLOCKERS — sign off before build)

Reuse the exact gating quad from `scheduleTimedMissionClose`
([mission.go:245](../../internal/api/mission.go#L245)) on **every** watcher tick and
at entry time — re-checked, not cached:

```text
cfg.App.CampaignLiveEnabled && cfg.Binance.Testnet &&
!cfg.App.RealTradingEnabled && !cfg.App.DryRun
```

### 5.A Security Gate — verdict: GO-WITH-CONDITIONS (3 HIGH are HARD merge blockers)

Phase A scaffolding is a sound base. Phase B's auto-entry MUST add these guards
(reviewed 2026; hook sites in code):

**HIGH (hard blockers):**

1. **Single-entry = claim-first via `MarkTriggered`, NOT `orders.Prepare`.**
   `orders.Prepare` mints a fresh random id per call → never collides → two ticks =
   two live entries. Reorder `checkArmedMission`: (a) `MarkTriggered(...)` atomic
   claim → abort if `!changed`; (b) only the winner calls `orders.Prepare` +
   `orders.Confirm`; (c) write the real `confirmation.ID` back onto the record
   (new store method / second transition — today the hook passes `""`).
   **Write order: MarkTriggered (persisted) → Prepare → Confirm**, so a crash never
   re-enters on rehydrate. Also thread `mission.IdempotencyKey` ("armed:"+id) into
   the confirmation so the unique index is a real backstop.
2. **Testnet-scoped key check.** `hasActiveKeyForSubject` must require
   `Active && Testnet` (`ProfileInfo.Testnet` exists); **reject, do not fall back**
   if the executor lookup fails or yields a non-testnet executor — else a mainnet
   active key routes real money under a testnet global config.
3. **Re-check the quad AND testnet-key immediately before `orders.Confirm`**, inside
   the claimed critical section (the candles+decision round-trip can take seconds and
   config can flip). Abort if either is now false.

**MEDIUM:**

4. Rebuild the full order-safety envelope at entry: `missionLeverageFor` +
   `DecisionToIntent(_, missionMaxLeverage)`, `missionBracket`, `missionSize`
   (cap `missionMaxSizeUSDT`); schedule `scheduleTimedMissionClose` after Confirm.
5. Move/clarify `usage.Incr("mission")` — increment at trigger (winning claim), not
   only at arm; keep the `s.allow(...)` re-check at trigger.
6. Disarm/expire vs in-flight trigger: `MarkTriggered.changed==true` is the point of
   no return; `handleDisarmMission` must report "already triggered" for a non-armed
   row; a rehydrate must reconcile "triggered but no confirmation id" rather than
   re-enter.

**Restart safety:** rehydrate re-watches only `status=armed`; combined with write
order (1) this prevents double-enter — the single most important Phase B rule.

### 5.B Legal Gate — verdict: GO-WITH-CONDITIONS (testnet-only, pre-launch)

5 questions all **No** (non-custodial, user's own key, testnet, risk-first), EXCEPT
Q4 framing: unattended auto-entry (no per-trade human Confirm) needs explicit
disclosure. Copy fixes required (land WITH Phase B, not before):

1. **Arm copy** (`armed_mission.go:317`) — remove "Phase A records the setup only;
   no order is placed" (false under Phase B). Replace with explicit consent: one
   testnet entry on the user's own key, side chosen by ANNY Basic, capped
   size/leverage, auto-closed, **no setup guaranteed**, not financial advice.
2. **Retire "ANNY manages the protective stop"** (`mission.go:203`) → mechanical
   wording ("a protective stop + timed close are attached to this testnet entry").
3. **UI armed copy** (`index.html` `setArmedState`, ~1349/1530) — disclose that
   arming auto-places one testnet entry and the window can expire with no order.
4. Surface expired-no-order and losing auto-entries in the Flight Recorder (no
   hidden missions).

➡️ **External legal sign-off required before any real-key/prod promotion.** Testnet
pre-launch engineering may proceed once 5.A (HIGH 1-3) + 5.B copy are done; the
campaign-live testnet quad stays the outer fence. **Do not promote to prod/real-key
without human legal sign-off.**

## 6. UX (minimal — Zoom-like, no new settings)

Reuse Step 3's area: replace the dead-end message with an **Armed** state
(waiting + expiry + Disarm). No new form fields. On trigger, the entry appears in
the existing Live monitor (Step 4).

## 7. Handoff to Codex

**Owned:** `internal/api/mission.go` (arm path + watcher), new
`internal/api/armed_mission.go` (record + store iface), `internal/storage/mongo/`
(persistence + TTL), `internal/dashboard/dist/index.html` (Armed/Disarm UI).
**Tests (no network):** arm-when-no-setup; trigger-on-first-setup; expire-no-order;
gating quad blocks arm/entry; idempotency (no double-enter on restart); disarm;
restart rehydration. **Residual risk:** every code path that could reach a real
exchange account stays gated + flagged; this is the first user-armed live-entry
path — review hard.
