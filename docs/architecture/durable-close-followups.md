# Durable Close + Arm Mission — Review Follow-ups (SPEC for Codex)

> Status: REVIEW OUTPUT. Owner: Codex (live execution). Scope reviewed:
> `da491d1..HEAD` (v0.9.42→0.9.48) — Arm Mission (wait-for-setup entry) and durable
> timed close (Mongo-backed `scheduled_closes`). Everything below is testnet-only
> behind the existing campaign-live gate; none of it can reach mainnet as written.

## Verdict

Core machinery is sound. Verified by review + adversarial trace and by tests:
atomic `pending→executing` claim is single-winner; armed-mission trigger is
single-winner (`MarkTriggered` + `armed:<id>` idempotency key); duplicate-confirm is
safe; the gating quad (`armedMissionRuntimeAllowed` + `hasActiveKeyForSubject`,
testnet-only) is re-checked before claim, after prepare, and before confirm; the
removed in-process timer leaves no double-scheduling. No double-entry,
double-close, or mainnet-leak path was found.

Two HIGH items and three MEDIUM items remain. **H1 is fixed in this branch**; the
rest are handed to Codex.

## H1 — Timed close could flatten an unrelated same-symbol position — FIXED HERE

`internal/api/scheduled_close.go`. The plan-end close used `CloseIntent{All:true}`
(close 100% of the symbol) guarded only by a symbol match, ignoring side. Trace:
mission LONG BTCUSDT (15m) → TP closes it at minute 5 → user manually opens a SHORT
BTCUSDT at minute 8 → at minute 15 the poller saw an open BTCUSDT position and would
close the user's unrelated SHORT.

Fix applied: `ScheduledClose` now carries `Side` (captured at schedule time from the
mission/decision side, both the manual and armed paths). `scheduledCloseHasOpenPosition`
requires the open position's side to match; a mismatch is treated as "no matching
open position" → `done`, no order. Empty side (legacy rows) falls back to symbol-only
match so in-flight rows drain safely. Test: `TestScheduledCloseSkipsOppositeSidePosition`.

**Residual for Codex:** in hedge mode a user could hold BOTH the mission side and an
opposite unrelated side at once; `All:true` would still close both. Full fix =
reduce-only by the recorded side and (ideally) the recorded entry amount, instead of
close-all-symbol. Testnet default is one-way mode, so the shipped guard closes the
real-world case; the hedge-mode refinement is lower priority.

## H2 — Executed entry confirmation is TTL-purged before recovery — FOR CODEX

`internal/storage/mongo/confirmations.go` (`Complete`/`Fail`) +
`internal/storage/mongo/store.go` (`expires_at_ttl`, `SetExpireAfterSeconds(0)`) +
`internal/api/scheduled_close.go` (`reconcileAwaitingCloses`).

`expires_at = createdAt + ConfirmationTTL` (~5 min) and the confirmations collection
has a TTL index on `expires_at`. `Complete` flips status→executed but does NOT bump
`expires_at`, so Mongo purges even executed confirmations ~5 min after prepare. If a
crash happens between confirm-success and `activateScheduledClose`, and recovery does
not run within that window, `ConfirmationStatus` returns `ok=false` (purged); the
reconciler falls to its default branch and, after the 30-min awaiting TTL, **cancels
the close of a genuinely-executed entry** — the exact "blind time-based cancel" the
design says it avoids. (Mitigation: the position still has TP/SL; the failure mode is
"outlives its plan window", i.e. the original problem this feature exists to solve.)

**Fix options (pick one):**
1. On `Complete`/`Fail`, extend `expires_at` to `now + retention` so terminal
   confirmations survive long enough to reconcile. Simplest, but changes retention for
   ALL confirmations — check nothing else depends on the 5-min purge.
2. Record the entry's terminal outcome on the `ScheduledClose` row at activation time
   so recovery never depends on the confirmation still existing. Narrower blast radius.

**Test to add:** purge/delete the entry confirmation, then assert an executed close is
NOT cancelled by the reconciler.

## M1 — Copy promises a timed close that isn't created when the runtime gate is off

`internal/api/mission.go` (`handleMissionPrepare`) + `scheduleTimedMissionClose`
(early-returns `(zero, nil)` when `armedMissionRuntimeAllowed()` is false). The prepare
handler is gated by `approved`/`hasActiveKey`/`allow` but NOT by the runtime gate, so
with the gate off no close row is persisted yet the response still says "a timed close
at the end of the plan … timed close are attached." Fix: when the gate is off, drop the
timed-close promise from the copy (or refuse to stage), and distinguish "gate off → no
close expected" from "persist failed".

## M2 — Interactive mission confirm uses the fallback executor, not the required one

`internal/api/command.go` (`handleConfirm` → `s.orders.Confirm` → `executorForUser`,
which falls back to the shared executor when no per-user executor resolves). The
automated paths correctly use `ConfirmWithRequiredUserExecutor`; this user-confirmed
mission entry does not, and `hasActiveKey` is only checked at prepare. A transient
per-user lookup failure at confirm could route a live testnet order to the shared key.
Fix: route mission confirms through `ConfirmWithRequiredUserExecutor`, or re-check
`hasActiveKey` at confirm and fail closed on provider error.

## M3 — Mongo scheduled-close layer had zero test coverage — PARTIALLY CLOSED

Every scheduled-close test ran against `memScheduledCloses`; the prod
`internal/app/scheduled_closes.go` (Mongo filters, pipeline, transitions) was
unexercised, so a filter/operator bug would ship green. Added no-network tests
(`internal/app/scheduled_closes_test.go`): `TestScheduledCloseClaimFilterBranches`
(guards the single-winner claim precondition — pending-due OR stale-executing) and
`TestScheduledCloseBSONRoundTrip` (bson tags, omitempty, Side/PurgeAt).

**Residual for Codex:** true `FindOneAndUpdate` atomicity (two instances racing one
row) can only be proven against a real Mongo. The repo has no integration harness
(`internal/storage/mongo` tests are pure serialization). Decide whether to add one
(testcontainers / ephemeral mongod) and, if so, port the single-winner and
restart-fires-missed-due cases onto the Mongo store. Also still missing: a boot/poller
restart-path test, and the gate-flip-between-prepare-and-confirm case
(`scheduled_close.go` post-prepare re-check).

## LOW

- `reconcileAwaitingCloses` activates via `time.Now()`, so a crash-recovered close
  starts its window at recovery time, not the original entry time — the plan window can
  effectively restart. Confirm intended.
- `usage.Incr(..., "mission")` counts on both arm and trigger (double-counts toward the
  daily limit). Accounting only, not trading-safety.
- `sortScheduledCloses` (mem insertion sort) is unasserted for ≥3 rows; awaiting rows
  carry a zero-value `DueAt` (BSON epoch, `<= now`) kept out of `ListDue` by the status
  check alone — safe today, fragile.
