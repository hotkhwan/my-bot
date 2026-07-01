# Mission Result Recording — SPEC for Codex (Roadmap Item A)

> Status: PROPOSAL. Owner: Codex (live execution). Testnet-only behind the existing
> campaign-live gate. Closes the Mission Zero item "Connect model decisions to
> Mission confirmation and Flight Recorder on testnet"
> ([ANNY_ROADMAP.md](../plan/ANNY_ROADMAP.md)). **Do AFTER Item B**
> ([paper-walkforward-validation.md](paper-walkforward-validation.md)). opBNB
> anchoring ([opbnb-anchor.md](opbnb-anchor.md)) is built ON TOP of this — it needs
> durable, complete completed-result records.
>
> **Decided:** capture TP/SL exits via a **reconciler poll** (not a websocket
> user-data stream); single **net `RealizedPnL`** per trade (no gross/fee split for
> Mission Zero); correlate entry↔exit by the **entry confirmation id**.

## The gap today (why the Flight Recorder is currently misleading)

- **Entry writes nothing.** `recordClosedTrade`
  ([internal/orders/service.go:415](../../internal/orders/service.go#L415))
  early-returns unless the intent is `IntentClose`, so a Mission ENTRY creates no
  journal row.
- **TP/SL exits are silent.** SL/TP are exchange-side algo orders placed at entry
  ([executor.go:191-219](../../internal/exchange/binance/executor.go#L191-L219),
  `closePosition:true`). When Binance fills one, the position closes **on the
  exchange with no `IntentClose`** passing through the order service → nothing is
  journaled. This is the DOMINANT exit path. The durable timed close then finds no
  matching position and records nothing either.
- **Decision context is dropped.** Even the one journaled path (durable timed close)
  writes only `Symbol/Side/PnL/Outcome` and leaves `Strategy/Models/Leverage/Entry/
  Exit/OpenedAt` empty ([service.go:427-437](../../internal/orders/service.go#L427-L437)),
  though the journal schema has those fields
  ([journal.go:42-63](../../internal/journal/journal.go#L42-L63)) and the mission
  knew them at prepare ([mission.go:177-187](../../internal/api/mission.go#L177-L187),
  [armed_mission.go:599-614](../../internal/api/armed_mission.go#L599-L614)).

⚠️ **Do NOT ship only the easy timed-close hook.** That records the minority of
missions (timed-out) and silently omits every TP/SL win and loss — a Flight Recorder
that hides losing missions, directly violating the transparency promise
([mission-zero-opbnb-testnet.md](../vision/mission-zero-opbnb-testnet.md)). The
reconciler is the load-bearing piece and must ship with this item.

## Design — 4 bounded slices

**A1. Open-write at entry confirm.** When a mission entry confirms (manual
[mission.go](../../internal/api/mission.go) path + armed
[armed_mission.go:588](../../internal/api/armed_mission.go#L588)), write a journal
row `status=open` carrying the decision context already in hand: strategy id+version,
side, entry, SL, TP, leverage, sizeUSDT, openedAt, and the **entry confirmation id**
as the correlation key. Add an `Open`/`Close` status to the journal or a nullable
`ClosedAt` to distinguish.

**A2. Persist decision context on close.** `recordClosedTrade` must copy
Strategy/Models/Leverage/Entry from the matching open row into the close, so the
recorded round-trip is complete ([service.go:427-437](../../internal/orders/service.go#L427-L437)).
Once populated, `missionReason` in the recorder renders correctly
([recorder.go](../../internal/api/recorder.go)).

**A3. TP/SL fill reconciler (poll) — the core piece.** A worker mirroring the
durable-close poller ([scheduled_close.go](../../internal/api/scheduled_close.go) +
[app/scheduled_closes.go](../../internal/app/scheduled_closes.go)):
- Source of truth: journal rows `status=open`.
- Poll ~30s (started in `Server.Run` like the close poller), gated by
  `armedMissionRuntimeAllowed` + `hasActiveKeyForSubject` (testnet-only).
- For each open row, read the user's positions
  (`PositionsWithRequiredUserExecutor`). If the position for that symbol **and side**
  is gone → the trade closed (TP or SL). **Side-match is mandatory** (same lesson as
  the H1 durable-close fix) so an unrelated same-symbol position is never mistaken
  for this mission's exit.
- On detected close, read realized PnL + exit price from Binance user trades
  since `openedAt` — a NEW exchange-boundary read (see below). Write the journal
  close: net `RealizedPnL`,
  exit price, closedAt, outcome (win/loss).
- Fidelity guard: a journal row must carry the entry quantity, and the reconciler
  only records after exit-side fills close that quantity. Commission/income rows
  alone are insufficient, and later same-symbol round-trips must be ignored by
  bounding attribution FIFO to the entry quantity.
- **Idempotency:** flip the journal row `open→closed` with an atomic conditional
  update (single-winner, like the scheduled-close claim) so two instances / two poll
  ticks never double-record. **Restart-safe:** state lives in the journal (Mongo).

**A4. Recorder shows real mission results.** No recorder change needed once A1–A3
populate the journal — its "real" feed
([recorder.go](../../internal/api/recorder.go)) stops being empty for missions and
renders model reason/confidence.

**A5. Legal/UI guard for result surfaces.** Every recorder/result surface must
label PnL by source (`paper`, `testnet`, or `exchange realized`/`live`) so a
simulation can never be mistaken for an exchange result. Copy must avoid
guaranteed/expected-return wording: no "guaranteed", "expected return",
"launch ready" as a profit claim, or "AI made $X". Prefer "paper simulation",
"testnet realized", "meets paper rules", and the product disclaimer wording from
`docs/legal/thai-sec-design-principles.md`.

## New exchange surface (flag for review)

The reconciler needs a **realized-PnL read** the executor doesn't expose today
(current close reads live `UnrealizedProfit`, [executor.go:363](../../internal/exchange/binance/executor.go#L363)).
Add an interface method at the exchange boundary (`UserTrades` for a symbol since
a timestamp, bounded by the entry quantity) with a mock, keeping exchange mockable
per the Go style skill. Keep it testnet-only via the existing gate.

## Correlation / identity (decided)

Reuse the **entry confirmation id** as the record correlation key (the durable close
already keys on it, [scheduled_close.go](../../internal/api/scheduled_close.go)) —
no new `missionId` field needed. Entry (A1) and exit (A3/A2) collapse into one
journal record via that id.

## Tests (no network)

- Entry confirm writes an open row with full decision context (strategy+version,
  entry, leverage) — both manual and armed paths.
- Reconciler: open position present → no close recorded; position gone (side-match) →
  one close recorded with realized PnL + outcome.
- Reconciler is single-winner (atomic open→closed) — no double-record across two
  ticks / two instances; restart re-reads open rows and still reconciles.
- Opposite-side same-symbol position does NOT trigger a false close (H1 lesson).
- Commission/income immediately after entry does NOT false-close a mission before
  an exit-side fill exists; multiple same-symbol round-trips after mission close
  do NOT leak into the mission PnL.
- Gate closed / no active testnet key → reconciler records nothing.
- Recorder renders a complete real mission record once the round-trip is journaled.
- Recorder/dashboard text preserves source labels (`paper`, `testnet`, `live`) and
  does not present simulated or testnet PnL as guaranteed/expected returns.

## Sequencing / dependency

B → **A (this)** → opBNB anchoring. opBNB
([opbnb-anchor.md](opbnb-anchor.md)) digests exactly the fields A now persists
(strategy+version, entry/exit, pnl, leverage, opened/closed, verdict); it cannot be
built until A produces complete records. Single net `RealizedPnL` is accepted for
Mission Zero — a gross/fee/net split can be added later if opBNB's proof page needs it.
