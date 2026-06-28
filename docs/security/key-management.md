# ANNY — Key & Custody Management — Source of Truth

**ANNY is non-custodial.** This is both a security stance and a legal one (see
[legal/thai-sec-design-principles.md](../legal/thai-sec-design-principles.md)).

## Custody — what ANNY must NEVER do

- ❌ Never hold user money or USDT.
- ❌ Never hold a user's withdrawal/private key.
- ❌ Never accept deposits "to trade on the user's behalf".

Trades execute on the **user's own Binance account, via the user's own API key**. The user
keeps control at all times.

## API keys

- The user creates a key on **demo.binance.com** (testnet) or their real account, and adds it
  in Settings. Tick only **Enable Reading + Enable Futures**; leave Withdrawals off.
  (Testnet REST host = `demo-fapi.binance.com`.)
- Keys are **encrypted at rest** (keyring / sealed) and decrypted only in memory to sign
  requests; never logged, never returned to the client.
- Per-user executors trade on each user's own key; testnet/real-trading gates are
  force-inherited so a user key cannot widen them.

## Secrets

- Platform secrets (AI key, JWT secret, encryption key, Mongo URI, **trailing/skill-model
  parameters**) live in **Fly secrets**, never committed. See
  [architecture/secret-model.md](../architecture/secret-model.md) for the secret-parameter list.
- Rotate any secret that was ever exposed before public launch.

## Future hardening (prod scale)

Move the proprietary skill model into a **separate private repo + private container +
internal-only pod** (`anny-skill-model`), reachable only by the execution engine over an
internal API that returns decisions, never formulas.
