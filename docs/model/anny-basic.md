# ANNY Basic — Model Card (v1.2)

> **Transparent by design.** This card explains *what the model does and how it is
> judged*, not a secret formula. ANNY Basic is the **public, conservative** paper
> strategy; the live "skill model" keeps its tuned parameters proprietary and
> exposes only verifiable results. Not financial advice. No guaranteed profit.

- **ID / version:** `anny_basic` · v1.2
- **Source of truth:** `internal/strategy/annybasic/` (model), `internal/campaign/paper.go`
  (paper engine), `internal/api/goal.go` (launch gate). Success criteria:
  `docs/strategy/success-model-anny-basic.md`.

---

## 1. What it is

A **risk-first** entry/stop policy for disciplined mission execution. It does not
place orders by itself — it proposes a side; the user confirms. It is intentionally
**selective**: it would rather take *no* trade than a low-quality one.

## 2. Entry logic (when ANNY Basic takes a side)

A side is only proposed when trend and momentum agree:

| Side | CDC Action Zone (15m) | QQE value | QQE cross |
|------|----------------------|-----------|-----------|
| **Long** | Green | > 50 | Cross up |
| **Short** | Red | < 50 | Cross down |

Anything else → **no trade** ("CDC and QQE are not aligned").

### No-trade gates (checked first, in fixed order)

1. **Abnormal volatility** → blocked
2. **Sideways market** → blocked
3. **Entry extended from trend** → blocked (don't chase)
4. **Execution not aligned** → blocked

These gates are *why setups are sparse* — most candles are filtered out on purpose.

## 3. Phases & leverage

Phase is derived from how many trades have closed this mission:

| Phase | Trades closed | Model leverage request |
|-------|---------------|------------------------|
| Fast | 0–2 | 50× (100× only if momentum confirmed) |
| Normal | 3–9 | 50× (100× only if momentum confirmed) |
| Defensive | 10+ | 50× (capped, no boost) |

The **platform/user leverage ceiling always wins** over the model's request
(`clampLeverage`). Leverage is never set by `capital_risk_pct`.

## 4. Campaign stops — why ANNY Basic makes *few* trades

The mission halts on the **first** of these (it does not "force" a trade count):

- 🎯 **Profit target reached** → stop as success
- ✋ **2 consecutive losses** → stop (no revenge trading / no open-ended martingale)
- ⏹ **15-trade hard cap** → stop

So a healthy ANNY Basic mission can legitimately be **2–4 trades**. That is by
design, not a bug.

## 5. Paper engine (how a /goal run is scored)

A `/goal` run is a **paper simulation on real Binance candles** — no orders, no
money. It resolves each trade from real intrabar highs/lows.

- **Reward : Risk = 2 : 1**, fixed (a 1:1 bracket is never traded).
- **Position size** comes from the **Leverage use %** slider (decimal math; risk
  per trade is clamped to 20% of capital and to the remaining drawdown budget).
- **`capital_risk_pct`** = the *cumulative* plan-loss ceiling only — never leverage.
- **Fees** (entry + exit) are deducted from every trade.
- **Edge / trade** = realized PnL ÷ trades (fees included) — the RR-adjusted edge.

## 6. "Launch ready" gate — what unlocks **Next →**

A paper result is **launch ready** when it shows a real edge, not a 1-trade fluke.
Two ways to qualify (both require **positive realized PnL** and **≥ 2 trades**):

| Path | Condition |
|------|-----------|
| **Target hit** | Verdict = `target_reached` → launch ready (even with just 2 trades — hitting the goal *is* the proof) |
| **Positive edge, no target** | Didn't reach target, but proved positive edge over **≥ 5 trades** |

> **There is no "must make 5 trades" rule.** The 5-trade floor applies *only* to runs
> that did **not** reach their target. A sparse ANNY Basic plan that hits its target
> in 2 winning trades is launch ready. (Launch to testnet still needs an active
> Binance key — the real-trading guard.)

Why win rate alone isn't the gate: at **RR 2:1** the break-even win rate is **33%**,
so a high-RR plan can be profitable below 50% — the gate uses *expectancy*, not WR.

## 7. Transparency boundary

| Public (verifiable) | Private (proprietary) |
|---------------------|------------------------|
| The mechanism above, every paper result, RR, edge, hashes/Flight Recorder | The live model's tuned parameters / weights (kept in secrets) |

## 8. Disclaimer

ANNY Basic is an educational, risk-first automation model. Results are simulated on
historical data and are **not** a prediction, signal, or guarantee of future profit.
The user always confirms execution. This is not financial advice.
