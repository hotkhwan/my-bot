# TradingView And AI Advisor

This bot uses TradingView as a signal source through webhook alerts. It does not use TradingView as a market-data REST API.

TradingView alert target:

```text
POST /tradingview/webhook
```

Example alert body:

```json
{
  "secret": "change-me",
  "source": "tradingview",
  "symbol": "BTCUSDT",
  "interval": "15m",
  "price": "67500",
  "strategy": "rsi_macd_volume",
  "action_hint": "evaluate",
  "side_hint": "long",
  "message": "RSI oversold with MACD cross",
  "indicators": {
    "rsi14": 28.4,
    "macd_hist": 12.5,
    "volume_change_percent": 180
  }
}
```

Common TradingView placeholders also work when mapped like this:

```json
{
  "secret": "change-me",
  "ticker": "{{ticker}}",
  "timeframe": "{{interval}}",
  "close": "{{close}}",
  "strategy": "my_strategy",
  "action": "{{strategy.order.action}}",
  "message": "{{strategy.order.comment}}"
}
```

Required local config for webhook intake:

```env
HTTP_ENABLED=true
HTTP_ADDR=:8080
TRADINGVIEW_ENABLED=true
TRADINGVIEW_WEBHOOK_SECRET=change-me
```

AI advisor is disabled by default. To use an OpenAI-compatible chat completions API:

```env
AI_ENABLED=true
AI_PROVIDER=openai_compatible
AI_API_KEY=
AI_BASE_URL=https://api.openai.com/v1
AI_MODEL=
AI_MIN_CONFIDENCE_PERCENT=70
AI_AUTOTRADE_ENABLED=false
```

## Multi-source context enrichment

The advisor can decide on the raw TradingView signal alone, or on the signal plus
context gathered from several external sources first. The flow mirrors a typical
research stack:

| Category (`internal/ai`) | Example sources | What it adds |
| --- | --- | --- |
| `narrative` | Grok, Perplexity, ChatGPT + web | news, sentiment, X/Twitter alpha |
| `onchain` | Nansen, Arkham | whale / smart-money accumulation |
| `orderflow` | CryptoQuant, CoinGlass | open interest, funding, exchange flows |
| `risk` | internal checks | exposure, anomaly, false-signal guards |

How it works:

- Each source implements `ai.ContextProvider` (`Name`, `Category`, `Enrich`).
- `ai.NewAggregator` fans out to all providers **concurrently**, each under its own
  `ProviderTimeout`. A slow or failing provider is logged and skipped, never
  failing the whole decision (favours fewer false signals over blocking on one
  unavailable source).
- The merged `MarketContext` is rendered into the LLM prompt ahead of the signal,
  and the model returns a `confidence_percent` (Bull Score) used by the existing
  confidence gate.

Wiring is opt-in: pass an `Enricher` to `ai.OpenAICompatibleConfig`. When it is
nil (the default today), behaviour is unchanged — the advisor decides from the
raw signal. Concrete provider integrations (with their own API keys and config)
are a follow-up; the framework, aggregation, and tests live in
`internal/ai/context.go`.

Safety gates:

- `AI_AUTOTRADE_ENABLED=false` means webhook signals can produce decisions but will not execute automatically.
- If `AI_AUTOTRADE_ENABLED=true`, the bot can auto-confirm decisions only through the existing order service.
- Real Binance live execution is still not wired in this phase. Use dry-run or Binance testnet.
- TradingView webhook secrets must not be placed in public Pine scripts or shared screenshots.
