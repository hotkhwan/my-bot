package telegram

const StartText = "Trade bot online.\n\nDry-run and Binance testnet are enabled by default. Use /help to see the command grammar."

const HelpText = `Phase 1 commands:

Open position:
long BTC 3x entry 67500 sl 65000 tp 72000 size 100usdt
short ETH 2x entry 3300 sl 3450 tp 3000 qty 0.05

Close:
close BTC
close BTC 50%
close all

Status:
/status
status

Market data (free Binance order-flow the AI reads):
/market BTC
/market ETHUSDT

Backtest a strategy on history (offline, no risk):
/backtest BTC

Phase 2 management:
be BTC
move sl BTC to be
trail BTC 0.5%
add BTC size 100usdt
add BTC qty 0.01

Rules:
- Every exchange-changing action will require [Confirm] [Cancel].
- Open orders must include entry, sl, tp, leverage, and size or qty.
- Phase 1 supports exactly one TP.
- Phase 2 management actions are accepted in dry-run; Binance execution for BE/trail/add stays blocked until exchange order tracking is complete.`

const StatusText = "No open positions."

const UnknownText = "Command not recognized yet. Use /help for the command grammar."
