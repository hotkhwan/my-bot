# ANNY — Secret Model Architecture — Source of Truth

**Transparency of RESULTS, secrecy of METHOD.** Results are verifiable; the formula is not.

> Framing matters. Do **not** say "paper is negative but live is profitable, by design".
> Say:
> - **Public paper model = baseline simulation** (simple, well-known signals).
> - **Live execution = a private risk-management engine.**
> - **All live results are recorded and verifiable.**
>
> The secret lives in the **execution layer, not the signal layer** — EMA/RSI/MACD are
> guessable by anyone; the real edge is execution and risk management.

## Where the edge actually is (execution layer)

position sizing · risk cap · entry filter · trailing stop · break-even move · partial take
profit · cooldown · market-regime filter · latency / timing.

## Public vs Private

| Public Transparency (show) | Private Skill Model (never expose) |
|---|---|
| Mission result, PnL | exact parameters |
| entry / exit time | sizing formula |
| win / loss | trailing logic |
| drawdown | entry filters |
| hash / blockchain anchor | model weights, confidence threshold, prompts |

→ Anyone can **verify the results**; nobody can **copy the formula**.

## Secrets (values kept out of the repo, in Fly secrets / private config)

`TRAIL_ACTIVATE_PCT`, `TRAIL_GAP_PCT`, `BREAKEVEN_BUFFER_PCT`, `PARTIAL_TP_RATIO`,
`MAX_POSITION_RISK_PCT`, `CONFIDENCE_WEIGHT_MAP`, `REGIME_FILTER_PARAMS`, `COOLDOWN_SECONDS`,
`VOLATILITY_MULTIPLIER`.

Limitation: Fly secrets hide the **values**, but if the **logic** is in a public/shared repo,
someone reading the code can still infer the structure. To truly close the gap, split the
skill model out (below).

## Production: 3 services / 3 pods

| Service | Visibility | Responsibility |
|---|---|---|
| `anny-api` | public backend | Telegram / Mini App / user / subscription / mission UI |
| `anny-execution-engine` | private internal | order execution · position monitor · SL/TP · trailing · Binance |
| `anny-skill-model` | **most private** | proprietary strategy · sizing · confidence · risk model |

**Git:** separate **private repos** — `anny-api`, `anny-execution-engine`,
**`anny-skill-model` (most private, separate repo + private container + internal-only pod)**,
`anny-infra`. The skill model must **not** share a repo with the frontend/backend even if both
are private — future devs, contractors, investor DD, or partial open-sourcing make per-repo
permissions essential.

### Internal flow (decision result only — never the formula)

```
Telegram / Mini App → anny-api → (internal) anny-execution-engine → (internal) anny-skill-model
   → decision result only → Binance order → Flight Recorder → Merkle root → opBNB
```

`anny-skill-model` returns only:

```json
{ "action": "enterLong", "symbol": "BTCUSDT", "confidence": 82,
  "riskLevel": "low", "positionSize": "0.002", "reasonCode": "momentumConfirmed" }
```

or for management:

```json
{ "action": "moveStopLoss", "symbol": "BTCUSDT", "newStopPrice": "59555.00",
  "reasonCode": "profitProtection" }
```

**Never returns:** exact formula · weights · thresholds · secret params · prompt · model rules.

## What goes on-chain (opBNB) — hashes, never formulas

Store only proof of results:
`missionId, userIdHash, symbol, side, entryTime, exitTime, entryPrice, exitPrice, pnl, risk,
result, strategyPublicName, modelVersionHash, logHash, merkleRoot`.

`modelVersionHash` proves *which model version* a mission used **without revealing the source**.

Each skill-model deploy carries `gitCommitHash`, `modelVersion`, `dockerImageDigest`,
`configHash`. Flight Recorder records:

```json
{ "missionId": "m_4821", "modelVersionHash": "sha256:…",
  "executionVersionHash": "sha256:…", "configHash": "sha256:…", "resultHash": "sha256:…" }
```

→ proves "not edited after the fact" without opening the formula.

## Dev now (don't split yet — set the boundary)

Keep one repo but enforce boundaries:

```
my-bot/
  internal/api/         # public-facing
  internal/execution/   # execution engine (orders, monitor, trailing)
  internal/skillmodel/  # proprietary strategy — frontend must NEVER call this directly
```

When prod scale is real, lift `internal/skillmodel` out into its own **private repo /
service / internal-only pod**. Today: `internal/monitor` (trailing) + `internal/campaign`
(sizing) + `internal/signals` are the seeds of the skill model; params already come from
secrets.
