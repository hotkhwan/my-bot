# AI Trading System — Design (blueprint)

Status: design + Phase 1 in progress. This is the north star for the AI-driven
trading layer built on top of the existing manual command bot. Primary
implementer for heavy trading logic is Codex; Claude owns design, review/QA, and
bounded slices (per CLAUDE.md).

## Vision

The user states a goal in natural language ("ทุน 100 USDT ช่วยทำกำไร 10 USDT").
A **panel of AI models** (Claude + DeepSeek + Qwen …) analyses the market and
proposes a trade (direction, entry, SL, TP, confidence, rationale). A
**campaign engine** executes a sequence of trades toward the target — each trade
may use a *different* strategy — while a **position monitor** trails the
stop-loss to break-even and beyond. **Every trade is journaled**; **reports**
surface the real win-rate / PnL per strategy and per model so the system learns
which techniques actually work.

## Principles (non-negotiable)

1. **Measure before trusting.** Build the journal + report first. Profit is a
   *hypothesis to test*, not a promised outcome. Win-rate (70/30, 60/40, 50/50)
   is an assumption; the report proves or disproves it.
2. **Edge is unproven.** Short-term crypto direction is hard to predict and many
   strategies lose to fees + funding. The system must make this measurable.
3. **Capital preservation first.** Drawdown stops, trailing SL, and conservative
   default leverage. High leverage (e.g. 100x) is a *deliberate, conditional,
   short-duration, closely-watched* exception — never the default.
4. **Safety gated.** `REAL_TRADING_ENABLED=false` until proven on demo/testnet
   over many hundreds of trades. All existing exchange-safety guards stay.
5. **Strategy diversity + statistics.** Each trade may use a different technique;
   the journal's per-strategy win-rate decides which to use more.

## Components (→ existing code)

| Component | Exists | To build |
|---|---|---|
| AI advisor + **ensemble** | `internal/ai` (`Advisor.Decide`, OpenAI-compatible) | native Anthropic advisor; `EnsembleAdvisor` (panel vote); market context (TF, S/R) |
| Signal → order | `internal/signals` | wire to campaign |
| **Trade journal + report** | — | `internal/journal` (Phase 1, this slice) |
| **Campaign engine** | `internal/plans` (status only) | NL goal → trade plan → execute loop → stop rules |
| **Position monitor / trailing SL-BE** | `internal/monitor` (stub) | watcher; move SL to BE / lock profit; smart SL offset |
| Exchange executor | `internal/exchange/binance` (algo-order fixed) | cancel/modify SL for trailing; rollback on partial failure |
| Storage / report surface | Mongo (audit, signals) | journal collection; Telegram/dashboard reports |

## Multi-AI ensemble (yes — this is a first-class feature)

Each model is an `Advisor` (provider, base_url, key, model, weight). DeepSeek and
Qwen are OpenAI-compatible, so they reuse the existing OpenAI-compatible advisor
with different base URLs; Claude gets a native Anthropic advisor.

`EnsembleAdvisor` fans out to all advisors and aggregates their decisions:

- **majority vote** — trade the side most models agree on;
- **confidence-weighted** — weight each vote by the model's stated confidence and
  its historical accuracy (from the journal);
- **consensus-required** — only trade when ≥N models agree, otherwise *skip*
  (disagreement → no trade = capital preserved).

Per-model accuracy is tracked in the journal (`Trade.Models`), so high-accuracy
models earn more weight over time — the same statistics loop as per-strategy
win-rate.

## Phases

1. **Trade journal + report** — record every trade; aggregate win-rate, PnL,
   expectancy by strategy / model / symbol. *No exchange risk.* ← current slice
2. **Trailing SL / BE engine + rollback** — monitor open positions, move SL
   automatically; also fixes the dangling-order bug (entry placed, SL/TP failed).
3. **AI advisors** — native Anthropic + ensemble + market context.
4. **Campaign engine** — NL goal → multi-trade plan (count/sizing from target and
   assumed win-rate) → execute → stop on target / max-drawdown / max-trades.

## Phase 1 data model (`internal/journal`)

`Trade`: ID, UserID, CampaignID, ConfirmationID, Symbol, Side, **Strategy**,
**Models[]**, Leverage, Mode, Entry, Exit, StopLoss, TakeProfit, SizeUSDT,
Quantity, **PnLUSDT**, **Outcome** (open/win/loss/breakeven), OpenedAt, ClosedAt.

`Report`: Trades, Wins, Losses, Breakeven, Open, WinRate, TotalPnL, AvgWin,
AvgLoss, Expectancy, **ByStrategy**, **ByModel** (each: Trades/Wins/Losses/
WinRate/TotalPnL). All monetary math uses the exact `decimal` type.

## Safety notes for later phases

- Per-trade leverage cap is config-driven; 100x requires an explicit conditional
  flag, short max-hold, and active monitoring — defaults stay conservative.
- Campaign hard stops: max drawdown %, max trades, max wall-clock.
- Real trading stays disabled until the journal shows a sustained, fee-adjusted
  edge on demo/testnet.
