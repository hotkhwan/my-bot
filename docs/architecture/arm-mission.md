# Arm Mission — wait-for-setup live entry — PROPOSAL (pending review)

> Status: **PROPOSAL**, not Source of Truth. Owner: Codex (live execution).
> This is a **live-trading** feature — it must pass the **Security Gate** and
> **Legal Gate** before any merge, and ship **testnet-only** behind the existing
> campaign-live gating. Codex should not implement until §5 is signed off.

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
- `orders.Prepare` — idempotent confirmation + TTL; the armed entry MUST go through
  it so a restart/double-trigger can't double-enter.
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
  idempotencyKey, triggeredConfirmationID?, createdAt
}  // TTL index on expiresAt
```

On boot, rehydrate `status=armed && now<expiresAt` and resume their watchers.

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

Plus, before any armed entry:

1. **Testnet-only.** Never arm/enter when `RealTradingEnabled` — real trading
   cannot be enabled accidentally. If config flips mid-window, the watcher aborts.
2. **Active Binance key** (testnet, Futures on, Withdrawals off) at trigger time.
3. **Authorization + subscription/daily limits** re-checked at trigger, not just at
   arm (`s.allow(..., "mission")`).
4. **Idempotency + single entry** via `orders.Prepare`; a restart re-arms the
   watcher but cannot double-enter.
5. **Restart safety** (Mongo persistence + rehydrate, §3).
6. **Disarm / expire** always available; expiry places no order.

### Legal Gate (`docs/legal/thai-sec-design-principles.md`)

| # | Question | Arm Mission risk |
|---|----------|------------------|
| 1 | Guaranteed/implied profit? | Copy must say "waits for a setup", never "auto-profit". |
| 2 | Soliciting investment with returns? | Low (testnet, user-armed, no funds solicited). |
| 3 | Copy-trading invite? | Low — it's the user's own armed plan, not following another. |
| 4 | Custody of funds? | None — testnet, user's own key. |
| 5 | Leads with profit/win-rate? | Lead with "armed / waiting / testnet", risk-first. |

➡️ Provisional: testnet-only arming is likely Gate-passable, but the **auto-entry**
framing needs legal confirmation. Mark **BLOCKED until Security + Legal sign-off**.

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
