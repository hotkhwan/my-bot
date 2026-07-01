# opBNB Result Anchoring — SPEC for Codex (item ง)

> Status: PROPOSAL (design — needs wallet/infra + Legal sign-off before mainnet
> anchoring). Owner: Codex + product. Realises the "verifiable track record" moat
> from [[anny-v1-vision]] / [[anny-transparency-positioning]].

## Goal

After a mission/plan completes, anchor a **tamper-evident digest of the result**
on opBNB so anyone can later verify the outcome wasn't edited — a Flight Recorder
that is provable on-chain. **Results are public and verifiable; the model/method
stays secret** (see [[secret-model-architecture]]).

## What gets anchored — HASH ONLY, never secrets/PII

Anchor a hash (e.g. SHA-256) of a canonical result record:

```text
digest = hash(JSON{
  missionId, symbol, side, strategy+version,
  entryPrice, exitPrice, pnlUSDT, outcome, leverage,
  openedAt, closedAt, verdict
})
```

NEVER on-chain: API keys, model parameters/weights, wallet secrets, user PII, raw
order payloads. Only the digest (and optionally the public result fields the
digest commits to, served off-chain so anyone can recompute + match the hash).

## Design

**Collection `chain_anchors`:**

```text
ChainAnchor {
  id, recordRef (mission/run id), digest,
  status: pending | anchored | failed,
  batchRoot?, txHash?, anchoredAt, createdAt
}
```

**Batcher** (hourly, per the v1 "hourly opBNB anchor" plan): collect completed
results since the last anchor → build a **Merkle root** of their digests → submit
ONE opBNB tx carrying the root → store `txHash` + each leaf's proof. Hourly
batching keeps gas trivial and matches the vision doc.

**Verification (public proof page):** show each result's digest + its Merkle proof
+ the anchor `txHash` (opBNB explorer link). A visitor recomputes the digest from
the public result fields and checks it's in the anchored root → independent proof.

## Config / secrets / gating

- `OPBNB_ENABLED=false` default; `OPBNB_RPC_URL`, anchor **wallet key in secrets**
  (never in code/Mongo/on-chain).
- Start on opBNB **testnet**; promote to mainnet only after Legal + a funded wallet.
- Fallback: if opBNB is unavailable, OpenTimestamps (Bitcoin calendar) as a
  no-wallet alternative — same digest, different anchor.

## Legal Gate (`docs/legal/thai-sec-design-principles.md`)

Anchoring *verifiable results* is aligned (transparency, not profit). Conditions:
lead with verifiability/discipline, not returns; never anchor or display anything
implying guaranteed profit; no PII/secret on-chain; "proof of record", not a
performance ad. Re-answer the 5-Q before mainnet.

## Tests

digest is canonical/deterministic; no-PII/secret in the payload; Merkle root +
proof verify; idempotent anchor (a result anchored once isn't re-anchored);
graceful when OPBNB disabled (status stays pending, nothing breaks).

## Sequencing

Depends on having durable completed-result records (the journal / Flight Recorder
already stores trades; armed/timed-close items feed it). Build AFTER ข
(durable close) so "completed plan" is reliable, then anchor those completions.
