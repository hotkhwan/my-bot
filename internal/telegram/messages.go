package telegram

const StartText = "🤖 ANNY is online.\n\nTap “Open ANNY” to launch the app right here in Telegram — every trade and result is logged in your Flight Recorder.\n\nDry-run and Binance testnet are on by default. Use /help for the command grammar, or /app to reopen the dashboard."

// OnboardText greets a not-yet-approved user and points them to the web app to
// register and request crew access (the admin approves from /pending).
const OnboardText = "👋 Welcome to ANNY — your Transparent AI Trading Companion.\n\nYou're not on the crew yet. Tap “Open ANNY” below, sign in, and press “Request access”. An admin will approve you, then every command unlocks here in Telegram."

const HelpText = `Phase 1 commands:

Open the app (Telegram Mini App):
/app          launch the ANNY dashboard inside Telegram

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

Profit goal — preview the plan (simulation, no real orders):
/goal profit 10 risk 50          (capital defaults to 100)
/goal profit 10 capital 100 risk 50 winrate 60

Autonomous campaign (testnet, gated):
/campaign start profit 10 risk 50 symbol BTC
/campaign stop

Admin:
/pending          list crew-access requests
/approve <id> [plan]  approve (optional free|captain|commander)
/reject <id>      reject / revoke a member
/tier <id> <plan> set plan: free|captain|commander
/makeadmin <id>   promote a member to admin

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
