# Goal Adaptive Model Engine — PROPOSAL (pending review)

> Status: **PROPOSAL**, not yet Source of Truth. Owner of record: Codex (core
> trading engine). Trading-economics changes here must pass the **Legal Gate** and
> **Security Gate** before production. Codex should **not** implement Step 2/3
> directly until the blockers in §5 are resolved.

## 0. UX principle (applies to everything below)

**Minimal, Zoom-like. No new end-user settings unless strictly necessary.** The
default flow must "just work": pick a goal, assess, launch — like sending a Zoom
link and joining, no config. The adaptive model and its style are **chosen by the
model author / platform default**, never surfaced as knobs in the normal user
`/goal` flow. The two existing sliders (Capital risk %, Leverage use %) are the
*only* risk inputs a user sees; Step 2 must not add more. Model authoring (§3) is a
separate creator surface, not bolted onto the user flow.

## 1. Context & what already landed (Step 1)

The web `/goal` paper run exposed **Capital risk %** and **Leverage use %** sliders,
but the paper engine ignored them: per-trade reward/risk was fixed at 2% / 1% of
capital (RR 2:1), and the "launchable" gate required hitting the full `$target`
inside a short validation window — so realistic short windows (3–5 trades) almost
never produced the Next/launch button.

Step 1 (shipped on `develop`, paper-only, **no exchange path touched**):

- **Launch gate = RR-adjusted edge, not a lucky $-hit.** `launchable` = `trades >=
  minLaunchTrades(5)` AND realized PnL > 0 (positive realized expectancy, fees
  included). High-RR plans can launch below 50% win rate; a 2-trade fluke cannot.
- **Sliders size trades (interim, FIXED per trade).** `applyLeverageSizing` scales
  the per-trade bracket by Leverage use %, RR 2:1 preserved, clamped to 20% of
  capital and the drawdown budget. (Decimal math; no float64.)
- **Unlimited duration.** `duration:"unlimited"` (UI default) runs to target **or**
  drawdown over a long window.
- **Card transparency** (RR, Edge/trade, Launch ready) + relabel Update plan / Next →.

**Gap:** Step 1 sizes every trade equally. The target is *adaptive per-trade sizing
within a band*, with a *pluggable model style* chosen by the author.

## 2. Target: pluggable per-trade sizing (Step 2)

### 2.0 Hard rule: NO float64 in order/risk math

Per `AGENT.md` ("Do not use `float64` for order-critical math. Use a decimal
type."). The sizing API returns **integer basis points**; all `riskUSDT` / notional
/ bracket math uses `decimal.Decimal`. A conviction *proxy* may be computed in
float for ranking, but it MUST be quantized to int bps before it touches any
risk/order quantity. No float risk/qty crosses a package boundary.

### 2.1 Slider semantics — DO NOT redefine the source-of-truth contract

Per `docs/plan/trading_bot_plan.md` planning contract:

- `capital_risk_pct` = **cumulative plan-loss ceiling** (% of allocated capital).
  **Never reused as leverage, never a per-trade ceiling.** It bounds the *total*
  run via `remainingDrawdown = capital_risk_pct·capital − realizedLoss`.
- `leverage_use_pct` = % of the **permitted leverage ceiling** made available to
  the model; execution may choose less.

So the per-trade risk **band** is derived from `leverage_use_pct` (the leverage
budget), and is then **always clamped** — every trade, every time — by ALL of:

1. `remainingDrawdown` (so a run can never exceed its cumulative loss ceiling),
2. exchange **max leverage** for the symbol,
3. **liquidation-distance** check (stop must sit inside the liquidation price),
4. a **product hard cap** (absolute max per-trade risk bps).

If any clamp would push the band below its floor, the trade is **skipped**, not
forced.

### 2.2 Per-trade size band (bps) — floor never exceeds ceiling

Work entirely in integer bps of capital (`1% = 100 bps`):

```text
ceilingBps = leverageBudgetBps(leverage_use_pct)      // leverage budget → risk ceiling
floorBps   = min(ceilingBps, max(ABS_FLOOR_BPS, ceilingBps·FLOOR_RATIO))
            // min(ceiling, …) makes floor>ceiling IMPOSSIBLE even when ceiling is tiny
```

**Resolved constants (v1):**

- `leverageBudgetBps(pct) = pct × 4` — consistent with Step 1's `applyLeverageSizing`
  (25% → 100 bps = 1%, 50% → 200 bps, 100% → 400 bps risk ceiling).
- `ABS_FLOOR_BPS = 25` (0.25% absolute per-trade floor).
- `FLOOR_RATIO = 0.15` (floor ≈ 15% of ceiling).
- Worked example, leverage 50%: `ceiling = 200`, `floor = min(200, max(25, 30)) =
  30` → per-trade band **0.30%–2.00%** of capital.

This fixes the default-UI edge case: with Capital risk default 5 / min 1 and any
small leverage budget, `floorBps` collapses to `ceilingBps` instead of exceeding
it. Step 2 does **not** require raising slider min/default (keeps §0). All values
are integer bps; `riskUSDT` is `decimal` (§2.0).

### 2.3 Adaptive selection within the band (default: conviction + anti-martingale)

```text
convictionBps = floorBps + conviction × (ceilingBps − floorBps)   // int bps
streakAdj     = anti-martingale shrink toward floorBps as consecutiveLosses rises
riskBps       = clamp(convictionBps with streakAdj, floorBps, ceilingBps)
riskUSDT      = decimal(equityNow) × riskBps / 10_000            // decimal only
reward        = riskUSDT × RR ; then apply §2.1 clamps 1–4
```

- `conviction ∈ [0,1]` proxy: normalized momentum vs ATR at entry (`clamp(|ret| /
  ATR%, 0,1)`), optionally blended with the AI confidence already plumbed via
  `aiBias`. Quantized to bps before use (§2.0).
- First strong signal opens near ceiling; size decays toward floor after losses;
  a fresh strong signal scales back up. **Never increases size to chase a loss**
  (that is martingale — forbidden, §2.4).

### 2.4 Pluggable styles — keep the live model proprietary

Per `docs/architecture/secret-model.md` + memory `secret-model-architecture`: the
**paper** path stays conservative/public; the **live** "skill model" stays private.
Sizing is a parameter, not a hard-coded formula:

```go
type SizingStyle interface {
    // RiskBps returns the per-trade risk in integer basis points of capital,
    // already inside [floorBps, ceilingBps]. NO float64 leaves this call.
    RiskBps(in SizingInput) int64
    ID() string
}
```

- **User-selectable built-ins:** `conviction_antimartingale` (default),
  `conviction_only`, `fixed_fraction`.
- **`martingale`: NOT user-selectable. Test fixture only.** Per
  `docs/strategy/success-model-anny-basic.md` (rescue/averaging must be *limited*,
  not open martingale), it is excluded from any user/creator menu and any UX path;
  it exists only to assert the engine rejects it.
- The **default ANNY model** weighs styles by state/signal/equity — exact weights
  are a private-model param in the platform secret store, **not** in this doc.

### 2.5 Public paper vs live (LOCKED default — resolves old open question)

- **Public paper** uses **only** a public, conservative sizing style. It must not
  run the proprietary adaptive weights.
- **Live private model** uses the internal skill model and exposes **only the
  results and their hashes**, never the formula.

Codex must not flip this default without product + legal sign-off.

## 3. Epic: creator-defined models + on-chain signing — **BLOCKED until legal review**

Vision: authors pick/describe a sizing style + params → **simulate** on real
candles → **save** → **sign/anchor on opBNB** → optional **creation fee** later.
Ties to memory `anny-transparency-positioning`, `anny-v1-vision`.

### 3.1 Legal Gate — Step 3 is BLOCKED (not "review before ship")

`docs/legal/thai-sec-design-principles.md` answers (must be re-answered with legal
before ANY Step 3 build):

| # | Legal Gate question | Step 3 risk (provisional) |
|---|---|---|
| 1 | Guaranteed/implied profit? | **Risk** — model leaderboards/sim results must not imply guaranteed returns. |
| 2 | Soliciting investment with returns? | **HIGH risk** — a model marketplace + "use this model" + fees reads as solicitation. |
| 3 | Copy-trading that invites following? | **HIGH risk** — "follow this creator's model" is exactly the forbidden framing. |
| 4 | Taking custody of funds? | Creation fee flow must not custody trading capital. |
| 5 | Marketing leads with profit/signal/win-rate? | Must lead with risk/transparency/discipline. |

➡️ **Step 3 status: BLOCKED until legal review.** Do not spec implementation
details for Codex beyond the storage shape (§3.2) until 2 & 3 are cleared.

### 3.2 Creator-model storage (NOT the secret store)

Platform secrets hold **private ANNY params only**. A creator-saved model is a
separate **encrypted MongoDB document**, with: immutable `version` + content
`hash`, `ownerKey`, `visibility`, `createdAt`, and an `audit` trail. The opBNB
anchor stores the **hash/result digest only** — never params, secrets, or PII.

## 4. What stays out of scope / unchanged

- `capital_risk_pct` meaning is unchanged (cumulative ceiling). No source-of-truth
  edit required by Step 2.
- No new user-facing settings (see §0).

## 5. Blockers to clear before Codex implements (review outcome)

1. **Decimal/bps math** — `SizingStyle.RiskBps() int64`, all risk/notional in
   `decimal.Decimal`; no float64 in order-critical paths. *(addressed in §2.0/2.4)*
2. **floor-ceiling edge** — `floor = min(ceiling, max(ABS_FLOOR, ceiling·ratio))`;
   safe with current slider min=1/default=5. *(addressed in §2.2)*
3. **Step 3 Legal Gate** — 5-Q table added; Step 3 marked **BLOCKED until legal
   review**. *(addressed in §3.1)*
4. **martingale** removed from selectable styles (test-fixture only). *(§2.4)*
5. **creator-model storage** separated from secret store. *(§3.2)*
6. **public-paper default** locked to conservative sizing only. *(§2.5)*

## 6. Resolved decisions (were open questions)

1. **Band constants** — `leverageBudgetBps(pct)=pct×4`, `ABS_FLOOR_BPS=25`,
   `FLOOR_RATIO=0.15` (§2.2). Revisit only if backtests show the band too wide/narrow.
2. **Conviction proxy v1** — momentum-vs-volatility, strategy-agnostic:
   `convictionFloat = clamp(|return over lookback(14)| / (2 × ATR%), 0, 1)`,
   then **quantized to bps** before use; optionally blended with `aiBias`
   confidence when AI is on. A per-strategy strength score is a v2 refinement.
3. **Sizing base** — **fixed allocated capital** for v1 (matches Step 1, simplest to
   reason about and to keep `remainingDrawdown` exact). Equity-based compounding is
   deferred to v2 behind the same clamps (§2.1).

## 7. Handoff to Codex (Step 2 task)

**Scope (owned files):** `internal/campaign/paper.go` (per-trade sizing loop),
`internal/campaign/` new `sizing.go` (`SizingStyle` + built-ins), `internal/api/goal.go`
(wire style + clamps). **Do not** change the slider contract, `capital_risk_pct`
meaning, or add user-facing settings (§0).

**Build:**

1. `SizingStyle` interface returning `RiskBps() int64`; built-ins
   `conviction_antimartingale` (default), `conviction_only`, `fixed_fraction`.
   `martingale` is a test-only fixture, never registered in any user/creator menu.
2. Band math in integer bps (§2.2 constants); `riskUSDT`/notional in `decimal`.
3. Per-trade clamps applied every trade: `remainingDrawdown`, exchange max leverage,
   liquidation distance, product hard cap (§2.1). Skip the trade if the clamped band
   is empty — never force.
4. Public paper wires the **conservative** style only (§2.5).

**Tests required (no network):** table-driven `SizingStyle` cases; floor≤ceiling
invariant across slider range incl. min=1/default=5; anti-martingale shrinks after
consecutive losses and never grows to chase a loss; clamp caps risk at
`remainingDrawdown`; `martingale` rejected from the selectable registry; decimal
exactness (no float64 in risk/qty).

**Residual risk to flag on handoff:** any path that could reach a real exchange
account stays gated; Step 3 stays **BLOCKED until legal review** (§3.1).
