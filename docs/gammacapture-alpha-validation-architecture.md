# GammaCapture alpha validation architecture

## Purpose

New signals must be testable in a bounded, causal and repeatable loop without
turning every experiment into a full multi-arm production replay. The existing
event ordering, quote simulator, fill model and PnL accounting remain the
source of truth for component replay; this document only defines how a new
signal reaches that simulator.

## Three-stage execution

```text
immutable parsed-event cache
        |
        v
causal preload-only (models + matured labels, no orders/optimizer)
        |
        +--> private-fill calibration
        |       fail: mark absolute execution evidence uncalibrated
        |
        v
one baseline/candidate paired component replay
        |
        v
full strategy regression only after calibration passes
```

### 1. Dataset stage

`loadWarmReplayDatasetAtInterval` is the only data-loading path. Its cache is
keyed by symbol, interval, time range and file fingerprints. The loader keeps
the score range at the requested BBO interval, compacts warmup to one BBO per
second by default, and never loads future events beyond `scoreTo`.

Every report now carries a `preload` manifest with the three half-open event
ranges:

- `[simulationFrom, exactFrom)`: model warmup;
- `[exactFrom, scoreFrom)`: calibration/pre-score causal events;
- `[scoreFrom, scoreTo)`: scored events.

The manifest records BBO/trade counts, cache hit, interval and chronological
ordering. A corrupt or out-of-range cached dataset is rejected before any
policy simulation.

### 2. Calibration stage

`--validation-stage=auto` is the safe default. The existing queue candidates
are evaluated only on the calibration interval. If the predicted side counts
do not match confirmed private fills within the existing tolerance, the full
replay still runs. When no explicit queue assumption was supplied, it uses the
neutral queue prior (`1`) for every arm and records
`replayQueueSource=neutral-prior-uncalibrated`. Orders, fills, P&L and drawdown
remain useful for causal signal comparison, but absolute fill/P&L claims are
diagnostic only and cannot promote or tune a policy. This keeps calibration
from becoming a no-order bootstrap gate.

Use `--validation-stage=calibration` only when the explicit goal is to inspect
the queue-fit stage without running a score replay. The legacy
`--allow-uncalibrated-replay` option is retained for command compatibility;
uncalibrated full replay is now always diagnostic and must not be used as a
promotion or tuning result.

### 3. Component stage

After calibration, screen one primary alpha at a time. The baseline and
candidate consume the same immutable event arrays, same queue factor, same
score account and same next-observable-BBO fill semantics. The alpha may own
one scalar integration point only; it must not independently change price,
quantity and gating.

Warmup uses the existing simulator with `preloadOnly=true`. It updates causal
estimators and matures delayed labels, but skips synthetic orders, inventory
mutation, quote lifecycle decisions and the expensive optimizer until
`scoreFrom`. At the score boundary the account and score counters are reset,
while learned model state is retained.

There are two separate readiness concepts:

- **Execution readiness**: causal BBO/trade data, valid balances, venue filters,
  and an executable base quote. Failure here may block an order for a concrete
  exchange or safety reason.
- **Research readiness**: private-fill calibration, effective samples,
  chronological blocks, and confidence bounds. Failure here blocks promotion or
  parameter changes, not conservative base quoting or collection of new live
  ledger observations.

## Runtime safety

Production-policy research runs have a default wall-clock limit through
`--replay-max-runtime=10m`. A timeout terminates the research process without
emitting a partial result. This is a process-level guard so the existing
synchronous replay kernel does not need a broad context-cancellation rewrite.

For routine screening, use a compact interval and an explicit BBO interval,
for example:

```bash
go run ./cmd/gammacapture-mm-research \
  --production-compare \
  --validation-stage=auto \
  --replay-from 2026-08-25T00:00:00Z \
  --replay-to 2026-08-25T04:00:00Z \
  --replay-bbo-interval=10s \
  --replay-max-runtime=10m \
  --replay-cache-dir=data/gammacapture/state/replay-cache
```

Use a short calibration/component window first, then reserve a new
chronological holdout for acceptance. A long replay is a later regression
step, not the first test of a signal.

## Acceptance rules

The alpha-screening contract remains the statistical gate: causal timestamps,
matured labels, executable bid/ask outcomes, fee convention, effective sample
size, multiplicity-adjusted lower bound and positive chronological blocks. A
calibration failure is `INCONCLUSIVE`/diagnostic evidence, never a reason to
retune the queue factor or promote a synthetic result.

This design fixes the previous failure mode with a small surface-area change:
the parser/cache and production replay kernel are reused; only stage control,
preload auditing, an explicit neutral queue prior and a hard runtime boundary
are added.
