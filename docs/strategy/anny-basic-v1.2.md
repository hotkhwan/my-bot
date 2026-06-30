# ANNY Basic v1.2

Status: policy, canonical indicators, and dual-timeframe paper adapter implemented.

ANNY Basic is ANNY's first versioned strategy model. It uses CDC Action Zone on
15-minute candles for direction and QQE confirmation, with 1-minute execution
alignment. The default symbol and plan are BTCUSDT and 15 minutes.

This is a model specification, not investment advice. The supplied 10 win and
10 loss cases are illustrative scenarios, not measured performance, a backtest,
or a promise of returns.

## Policy

- Long requires green CDC, QQE above 50, an upward QQE cross, and aligned 1m execution.
- Short requires red CDC, QQE below 50, a downward QQE cross, and aligned 1m execution.
- Stay in cash when signals disagree, entry is extended, volatility is abnormal,
  or the market is sideways.
- Stop at the +10 USDT illustrative target, after two consecutive losses, or at
  15 closed trades.
- Trades 1-3 are fast phase, 4-10 normal phase, and 11-15 defensive phase.
- Defensive phase caps model-requested leverage at 50x.
- The model specification permits up to 100x only with confirmed momentum.
  Platform, account, exchange-symbol, and user risk caps always take precedence.
- Rescue margin is policy-eligible only for a wick while the setup remains
  valid, at most twice, at most 10 USDT each, while preserving 20 USDT reserve.
  It never bypasses confirmation or execution safety gates.

The deterministic policy is implemented in `internal/strategy/annybasic`. It
consumes precomputed CDC/QQE observations and does not place orders.

## Canonical Indicator Parameters

- CDC Action Zone: EMA(12) and EMA(26) on 15m closes. Green transition means
  EMA12 crosses above EMA26; red transition means EMA12 crosses below EMA26.
- QQE: Wilder RSI(14), smoothed by EMA(5). RSI volatility is the absolute
  smoothed-RSI change, smoothed twice with EMA(27), then multiplied by 4.236.
  The QQE signal line is the resulting stateful trailing band.
- A CDC/QQE setup is eligible only on the first 1m candle after the confirming
  15m candle closes. One 15m crossover cannot trigger repeated entries.
- Execution alignment: EMA(5) above EMA(13) for long, below for short, on 1m.
- Momentum confirmation: current 1m volume is above its previous 20-bar mean
  and candle body is at least ATR(14).
- Extended entry: 1m close is more than 1.5 ATR(14) from the 15m EMA(12).
- Abnormal volatility: current 1m true range is greater than 3 ATR(14).
- Sideways: absolute 15m EMA12/EMA26 spread is below 0.1% of execution price.

Paper validation requests two independent candle series. Indicator decisions
use only 15m candles whose close time is at or before the current 1m candle
open, preventing look-ahead. Entry, SL/TP resolution, timeout, and fees use 1m
OHLC candles.

Because ANNY Basic setups are sparse, the dashboard paper assessment uses an
extended recent validation sample. `No launchable ANNY Basic setup` means
public Binance market data loaded successfully but no eligible setup was found
in that sample. The user-facing reason should name the dominant blocker, such
as market-condition filter, CDC/QQE non-alignment, indicator warmup, or AI side
filter. Market-data failures must surface as API errors, not as no-setup
results. A no-setup assessment must show entries needed by goal math,
launchable setups found, top blocker, and the next edit hint before Stage 3 /
mission launch is allowed.
The dashboard may offer one-tap fallback paper reassessment with Auto or RSI,
but that changes the strategy under assessment; it must not make an ANNY Basic
no-setup result launchable.

## Delivery Gates

1. Run fee-adjusted walk-forward paper tests and publish both gains and losses.
2. Paper assessment must report the entries needed by goal math, launchable
   setups found, top blocker, next edit hint, and the actual trades found in the
   validation window.
3. If a validation window has no launchable ANNY Basic setup, treat it as
   `edit plan`, not a launchable Mission or Flight Recorder result.
4. Integrate with Mission confirmation and Flight Recorder on dry-run/testnet.
5. Consider production only after Security and Legal review. Real trading stays
   disabled by default.
