# Multi-Trade Mission (Campaign Reuse) — Architecture Plan

> **Goal.** Turn the web dashboard **Mission** from *one entry → hold → timed close*
> into a **multi-trade campaign**: open → close → open → close, toward a profit
> target, within a plan window — matching what the paper engine already simulates.
> Testnet-only (real trading OFF). AI = ANNY Basic (not "AI says BUY"). Every trade
> keeps the same hard caps. Must pass Security + Legal gates before merge.
>
> Status: **planned, needs decisions + Legal sign-off before Codex.** Source of the
> reuse target: `internal/campaign/engine.go` (the open→close→open loop).

---

## Why "just reuse `campaignexec.Manager`" does NOT work (4 blockers)

| # | Blocker | Evidence | Impact |
|---|---|---|---|
| **F1** | `campaign.Engine` runs the **LLM advisor + price-only signal**, not ANNY Basic | `engine.go:72-91`; `campaignexec/signals.go:32` (returns `{Symbol,Price}` only); advisor = `app.go:540-602` (OpenAI/Anthropic) | Reusing as-is runs missions on the LLM, not the 15m CDC/QQE model the armed watcher uses (`mission.go:234-250`). Not equivalent. |
| **F2** | `campaignexec.ServicePlacer` uses `Confirm` (shared-executor fallback), not `ConfirmWithRequiredUserExecutor` | `placer.go:56` → `service.go:344-372`; armed path uses required-user at `armed_mission.go:581` | Key-scope regression — an auto trade could touch a shared/default executor instead of the user's own testnet key. |
| **F3** | No per-trade idempotency in the campaign path | `placer.go:52-56` uses plain `Prepare`; armed uses `PrepareWithIdempotencyKey` (`armed_mission.go:551`) | A retried `Decide→Place` after a crash can **double-open**. |
| **F4** | `campaignexec.Manager` is **in-memory only** and wired **only to Telegram** | `manager.go:60-61,113`; `telegram/handler.go:41,84`; never in `api.Server` | Web mission would not survive restart, and the dashboard can't even reach it. Violates restart-safety review bar. |
| **F5** | Engine loop has **no plan-window bound**; `MaxConsecutiveSkips:20` ends a quiet mission in ~20 min | `engine.go:62`; `manager.go:106` | A 1h/∞ window mission would die early in a quiet market. |

Extra correctness fact: the Engine never feeds `annybasic.State` back to the model,
which is why the stop rules (target / 2-consecutive-losses / 15-cap in
`model.go:75-83`) are **dead code on the live path** (`mission.go:249` passes
`State{RealizedPnL:0}` every call).

---

## Recommended: "A-prime" — drive `campaign.Engine` as a library from `api.Server`

Keep the Engine (clean DI loop = correct open→close→open primitive). Do **not** use
`campaignexec.Manager`/`ServicePlacer`. Add five owned pieces:

1. **Stateful ANNY Basic advisor** (`signals.Advisor`) — on each `Decide`, fetch 1m+15m
   candles (reuse `annyBasicLiveDecision`, `mission.go:234`), call `annybasic.Evaluate`
   with the **live accumulated** `annybasic.State`, map a side → full `signals.Decision`
   (Open/Side/Leverage/Entry/SL/TP/Size) reusing `missionBracket`/`missionSize`/
   `missionLeverageFor` so **caps hold on every re-entry**. `Decision.Stop` → signal
   campaign stop.
   - **State feedback loop = the #1 correctness seam:** after each closed trade the
     runner pushes realized PnL + win/loss into the advisor (`TradesClosed++`,
     `ConsecutiveLosses`, `RealizedPnLUSDT`) **before** the next `Decide`. This is what
     finally makes `model.go:75-83` risk stops live. Must be unit-tested first.
2. **Required-user, idempotency-keyed placer** (`campaign.OrderPlacer`) — uses
   `PrepareWithIdempotencyKey(userID, intent, "mission:<id>:trade:<n>")` +
   `ConfirmWithRequiredUserExecutor` (mirrors `armed_mission.go:551,581`). Never reuse
   `ServicePlacer` (F2/F3).
3. **Windowed runner** wrapping `Engine.Run` — `context.WithDeadline(planEnd)`, stop
   opening new trades after a late-window cutoff, disable/repurpose `MaxConsecutiveSkips`.
   Plan-end flush is **already covered per-trade**: every entry schedules + activates
   its own durable timed close (`scheduleTimedMissionClose`+`activateScheduledClose`,
   `scheduled_close.go:263,297`) exactly like the single-shot armed path
   (`armed_mission.go:570-586`), so the last open trade is flushed by its own close.
4. **Mongo-backed `CampaignMissionStore`** + boot rehydrate — mirror `ArmedMission`
   (`armed_mission.go:41-65,447-468`). Persist progress (`TradesClosed`,
   `RealizedPnLUSDT`, `ConsecutiveLosses`, `LastTradeIdempotencySeq`, `WindowExpiresAt`)
   so State rehydrates after restart; `ExpireStale` sweep for orphans.
5. **Per-trade safety** — re-assert `armedMissionRuntimeAllowed()` + `armedMissionTriggerAllowed`
   (active key + `allow`) **before every** Place; caps rebuilt each re-entry; duplicate
   safety from `orders` atomic `TakeForExecution` (`service.go:356`) + per-trade key.

**Rejected alternatives:** (B) extend the armed watcher into an in-goroutine loop
carrying State in memory → re-introduces the durability problem the scheduled-close
machinery was built to avoid; (A verbatim) route into `campaignexec.Manager` → fails
F1–F4.

---

## Phased plan (owned files per phase)

| Phase | Scope | Owned files | Risk |
|---|---|---|---|
| **0** | Stateful ANNY Basic advisor + State-feedback updater. **Pure, no network, no orders.** | `internal/api/mission_campaign_advisor.go` (or `strategy/annybasic/campaign_advisor.go`) | 🟢 unit-testable |
| **1** | Required-user idempotency placer + windowed runner (deadline + late cutoff). Reuse per-entry durable close. | `internal/api/mission_campaign_placer.go`, `mission_campaign_runner.go` | 🟠 touches live testnet order path |
| **2** | Mongo store + boot rehydrate + `ExpireStale`. | `internal/api/campaign_mission.go`, `internal/storage/mongo/campaign_mission_store.go`, wiring in `server.go` | 🟡 |
| **3** | API arm/status/disarm + UI roll-up + journal `CampaignID=missionID` + reconciler aggregation | `internal/api/*`, `internal/dashboard/dist/index.html`, `mission.go:196`/`armed_mission.go:613` | 🟡 |
| **4** | Legal + copy + docs. | copy at `armed_mission.go:389`, `mission.go:214`, dashboard copy, docs, memory | 🔴 Legal + Security gate |

## Test plan (no-network / table-driven / restart / duplicate / stop-rule)
1. Advisor stop-rule transitions (target / 2-loss / 15-cap) with real State — closes the `model.go:75-83` dead-code gap.
2. Idempotency key stable across retries, distinct per re-entry.
3. Gate re-asserted before every Place (fake placer counts gated calls).
4. Restart rehydration of State + window from Mongo mem-store.
5. Duplicate `Trade`/callback idempotent.
6. Window deadline + late cutoff; last open trade flushed by its own scheduled close.
7. Disarm mid-mission: no new entries, in-flight close still drains.

## Residual risks / real-exchange paths
- **Highest risk:** `Placer.Place → ConfirmWithRequiredUserExecutor` places live **testnet**
  orders N times/mission — must be gated by `armedMissionRuntimeAllowed()` before **every**
  re-entry, not just at arm. Flag for Security review.
- `annybasic.State` accumulation is the *only* thing making the model's risk stops live —
  a bug silently disables 2-loss/15-cap protection. Prioritize its test.
- `MaxConsecutiveSkips` must be disabled/repurposed for windowed missions (F5).

## Decisions (settled 2026-07-01)
1. **Advisor**: ✅ **ANNY Basic only** (matches armed path, avoids "AI says BUY" framing).
2. **Quota unit**: ✅ **one `mission` quota at arm-time**; still gate each re-entry on `allow` (a mid-mission tier downgrade halts new entries). Ties to `subscription-gated-limits`.
3. **MaxTrades / MaxDrawdown defaults**: reuse model's 15-cap + `capital_risk_pct` drawdown budget (default; tunable later).
4. **Late-window entry cutoff**: no new entries in the final 10% of the window (default; tunable in Phase 1).
5. **Target reached**: ✅ **flush the open position immediately** (lock the win) — the runner cancels the pending timed close and closes now.

## Progress (2026-07-01) — all phases landed on develop
- ✅ **Phase 0**: stateful `missionCampaignAdvisor` — model target / 2-loss / 15-cap stops fire from accumulated State
  (previously dead code). `mission_campaign_advisor.go` (+ tests).
- ✅ **Phase 1**: `missionCampaignPlacer` (required-user + per-trade idempotency `mission:<id>:trade:<n>` + durable
  per-trade close, gate re-asserted every entry), `recordingTrader` (State feedback), `missionCampaignRunner`
  (window deadline + late-window cutoff + paced signals). `mission_campaign_placer.go`, `mission_campaign_runner.go` (+ tests).
- ✅ **Phase 2**: durable `CampaignMissionStore` (mem + `mongoCampaignMissions` + `campaign_missions` collection/indexes);
  progress fields rehydrate State on restart. `campaign_mission.go`, `internal/app/campaign_mission_store.go` (+ tests).
- ✅ **Phase 3**: runtime assembly + boot rehydrate + `ExpireStale` (`campaign_mission_runtime.go`), endpoints
  `POST /api/mission/campaign/arm|disarm`, `GET /api/mission/campaign` (`campaign_mission_handlers.go`), dashboard
  multi-trade toggle + status/disarm + tier-cadence poll.
- ✅ **Phase 4**: Legal Gate reviewed the N-auto-entry consent copy — **PASS-WITH-EDITS** (testnet). Applied: reframed
  the profit target as a *stop condition* (not "toward target"), softened "authorizes the whole series" →
  "pre-authorizes this bounded, capped series", dropped "LIVE" from testnet copy, added substantial-risk + loss
  disclosure to all three long strings. **NEEDS-EXTERNAL-LEGAL-SIGNOFF before any real-trading enablement** — a single
  confirmation pre-authorizing a bounded series toward a profit target is the standing-authorization + profit-goal shape
  a regulator would scrutinize as managed-investment-like (same gate as [[arm-mission-feature]] real-trading block). Do
  not use these strings in front-page marketing without external review. Docs + memory updated.

### Known follow-ups (not blockers)
- The synchronous LiveTrader opens+awaits-close per trade, so at most one position is open at a time and it is always
  closed before re-evaluation — the "flush open position on target" decision is therefore a no-op in practice.
- `RealtimeResolver` subscribes after the order is placed (documented small race); a user-data WebSocket removes it.
- Per-tier caps (armed/campaign 3/user, quotas) remain hardcoded — see `subscription-gated-limits`.
