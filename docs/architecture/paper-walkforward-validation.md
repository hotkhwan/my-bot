# Fee-adjusted Paper + Walk-forward Validation — SPEC for Codex (Roadmap Item B)

> Status: PROPOSAL. Owner: Codex. Offline sim only — no live exchange, no new
> trading risk. Closes the Mission Zero gate "Complete fee-adjusted paper and
> walk-forward validation" ([ANNY_ROADMAP.md](../plan/ANNY_ROADMAP.md)).
> **Scope decided:** maker/taker fees only (NO funding); evidence reuses `GoalRun`.
> Do B before Item A ([mission-result-recording.md](mission-result-recording.md)).

## What already exists (reuse, do not rebuild)

The fee-adjusted ANNY Basic paper engine is already real in
[internal/campaign/paper.go](../../internal/campaign/paper.go) (`RunPaper`):
- Runs ANNY Basic over real Binance candles via the dual-timeframe adapter
  (`annybasic.ObserveAt`/`Evaluate`).
- Resolves each trade from **real intrabar highs/lows**, SL-before-TP pessimistic.
- Applies **separate maker-entry / taker-exit fees per leg** (default 0.02% / 0.04%).
- Produces win/loss/win-rate/expectancy; surfaced via `/goal`
  ([internal/api/goal.go](../../internal/api/goal.go) `summarize`) and persisted as
  `GoalRun` ([internal/app/goalruns.go](../../internal/app/goalruns.go)), which the
  Flight Recorder shows correctly labelled as paper
  ([internal/api/recorder.go](../../internal/api/recorder.go)).

So this item is **not a new engine** — it is a walk-forward wrapper + one missing
metric + an aggregate evidence record.

## What "walk-forward" means here

ANNY Basic is **rule-based, not parameter-fitted** — there is nothing to train. So
walk-forward here = **rolling out-of-sample folds**: slice the candle history into N
consecutive windows, run `RunPaper` on each fold independently, and report per-fold
+ aggregate metrics. This tests **temporal stability** (does the edge hold across
different market regimes, not just one lucky window), which is the validation the
roadmap wants — not optimization.

## Design

1. **Fold splitter** (new, in `internal/campaign`): given a candle series + fold
   count (or fold length in bars), produce N non-overlapping test windows. Each fold
   must include its own `WarmupBars` (`paper.go:48-49`) BEFORE the window start so
   ANNY Basic's 15m/1m dual-timeframe state is warm — the warmup must NOT straddle a
   fold boundary (look-ahead/leakage guard). Re-invoke `RunPaper` per fold.
2. **Max-drawdown in the paper path** (missing today — it lives only in
   `backtest.Result`): build the equity curve from the ordered per-trade net PnLs in
   `RunPaper` and track peak-to-trough. Add it to the paper result struct.
3. **Aggregate evidence** → reuse `GoalRun`: store per-fold {net PnL after fees,
   win-rate, expectancy, max-drawdown, trade count} + an aggregate row. No S3, no new
   collection (decided). Flight Recorder already renders `GoalRun` as paper evidence.

## Explicitly OUT of scope (decided)

- **Funding-rate cost** — not modelled. Fees stay maker/taker only.
- **S3 evidence bundle** — evidence stays in `GoalRun`.
- Parameter optimization / grid search — this is validation, not tuning.

## Tests (no network — the engine is deterministic, `paper.go:129`)

- Splitter produces N non-overlapping windows; each fold's warmup does not read
  candles from the previous fold's test window (leakage guard).
- Max-drawdown correct on a known equity curve (monotonic up → 0; a dip → the dip).
- Per-leg maker/taker fee still applied inside each fold (regression on `paper.go`).
- Aggregate evidence sums fold PnL and reports worst-fold drawdown.

## Legal framing (Legal Gate)

Output is **non-performance evidence**, never a returns claim. Keep copy led by
verifiability/discipline; results are "what the rules did on past candles, after
fees", not a forecast. Re-answer the 5-Q before any user-facing surfacing.
