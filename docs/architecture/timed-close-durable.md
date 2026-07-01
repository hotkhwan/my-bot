# Durable Timed Close (Mongo-backed) — SPEC for Codex (item ข)

> Status: PROPOSAL. Owner: Codex (live execution). Testnet-only behind the same
> campaign-live gating. Closes the last Phase B residual risk before any prod talk.

## Problem

`scheduleTimedMissionClose` ([internal/api/mission.go](../../internal/api/mission.go))
schedules the plan-end close as an **in-process goroutine timer**. If the API
restarts after an entry but before the timer fires, the timed close is **lost** —
the position then relies only on TP/SL and can sit open past its plan window. The
*entry* is restart-safe (armed mission persisted in Mongo); the *close* is not.

## Goal

Persist scheduled closes in Mongo; a poller rehydrates and fires due closes,
surviving restarts. Reuse the existing close logic and all the Phase B safety
guards.

## Design

**Collection `scheduled_closes`:**

```text
ScheduledClose {
  id, userKey/userID, symbol, dueAt,
  status: pending | executing | done | cancelled | skipped,
  confirmationID?, reason?, createdAt, updatedAt, purgeAt
}  // index: {status, dueAt}; TTL on purgeAt (not dueAt), ~90d after done
```

**On entry** (both manual `handleMissionPrepare` and armed `triggerArmedMission`):
replace the goroutine with a persisted row `{status: pending, dueAt: now +
planDuration(duration)}`. Keep the in-process timer too if you like as a
best-effort fast path, but the poller is the source of truth.

**Poller** (every ~30s, started in `Server.Run` like armed rehydrate):
1. Find `status=pending && dueAt<=now`.
2. For each, **atomically claim** `pending -> executing` (conditional
   FindOneAndUpdate) so two instances can't double-close.
3. Re-check the gating quad (`armedMissionRuntimeAllowed`) AND active **testnet**
   key (`hasActiveKeyForSubject`); if closed → `skipped`, no order.
4. `PositionsWithRequiredUserExecutor` → if no open position for the symbol →
   `done` (nothing to close).
5. Else `orders.Prepare` (close-all intent) → re-check gate → 
   `ConfirmWithRequiredUserExecutor` → `done`; on confirm error → leave a clear
   status + log (do not loop forever).

**Restart safety:** poller rehydrates from Mongo, so a missed timer fires on next
boot. **Idempotency:** the atomic `pending->executing` claim + close-all intent
(closing an already-closed position is a no-op) make double-runs safe.

## Reuse / hooks

- Close logic + gating: `scheduleTimedMissionClose` (mission.go) — port its body
  into the poller's per-row handler.
- Strict executor: `PositionsWithRequiredUserExecutor`,
  `ConfirmWithRequiredUserExecutor` (already exist).
- Pattern mirror: armed-mission persistence + rehydrate
  (`internal/api/armed_mission.go`, `internal/app/armed_missions.go`,
  `internal/storage/mongo/store.go`).

## Tests (no network)

claim is single-winner (no double close); restart fires a missed due close;
gate-closed → skipped no order; no-open-position → done; TTL on purgeAt keeps
audit; close-all idempotent.

## Out of scope / residual

Still testnet-only. Does not change entry logic. After this lands, both entry and
close are restart-safe — the remaining pre-prod blocker is external legal sign-off.
