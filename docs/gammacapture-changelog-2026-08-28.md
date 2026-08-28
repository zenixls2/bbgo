# GammaCapture strategy and research changelog — 2026-08-28

## Scope

This release records the accumulated GammaCapture strategy, replay, execution,
private-ledger, and research changes after commit `680f4a042` (`Retire
DynamicInventoryAim from live quote path`). It intentionally excludes local
compiled binaries, captured market data, replay caches, checkpoints, and
credentials.

## Production policy

- Made the causal pivot-regime certainty-equivalent controller the sole live
  inventory-target owner. The configured 50% allocation is a soft prior rather
  than a safety target; 0% and 100% remain the hard capital boundaries.
- Added causal midpoint directional-change state, completed-leg moments,
  checkpoint restore, bounded 24-hour startup context, and next-pivot research
  labels. The live pivot reversal threshold remains 26 bps.
- Added a full-range target objective with predictive risk, prior shrinkage,
  and a one-way fee/adverse-selection L1 switching cost. Strong evidence can
  select the full admissible range without the legacy 20-point shift cap.
- Corrected the exhausted-pivot boundary. Zero remaining amplitude now enters
  the CE objective as zero alpha and preserves the current no-trade kink
  instead of making the target unavailable and falling back to 50%.
- Added a target-aware quantity fallback for degenerate or unavailable
  probability projection. It only reduces target error, fails closed below
  venue minimums, and preserves full target correction when the risk and
  balance capacities allow it. A flat target can no longer create a BUY and a
  full-long target can no longer create a SELL.
- Kept posterior inventory target, legacy DynamicInventoryAim actuation,
  regime-conditioned target, and causal Kline target disabled in production so
  they cannot overwrite the pivot CE owner.

## Execution and quote optimization

- Decoupled Fast maker-to-IOC execution from the posterior-target feature flag.
  The active target owner supplies target/evidence while the execution model
  independently compares passive wait loss, IOC cost, and portfolio CE.
- Added explicit target-execution evidence, reference-horizon maturity, visible
  depth, exchange percent-price bounds, residual maker gap, and target-relative
  CE diagnostics.
- Corrected terminal-wealth CE accounting to compare complete candidate and
  baseline wealth, including whole-position risk and target-relative terms,
  without using model readiness as a quote-admission gate.
- Strengthened joint distance/quantity fallback semantics: authoritative pair
  rejection may clear a side, while non-authoritative fallback retains only a
  target-restoring executable side. Probability-centered quantity remains the
  final live quantity owner.
- Added adaptive path decay, continuation, relative-Hold risk, conditional
  execution, lifecycle, and maturity diagnostics. Research components remain
  gated by their documented evidence and live configuration.
- Expanded quote, target, projection, IOC, private-fill, and order-submission
  logs so execution path, order type, time in force, target owner, fallback
  reason, and submitted notionals are auditable.

## Private order/fill evidence

- Added append-only framework database tables and migrations for authenticated
  submit intent/result, cancel request/result, order update, and fill events.
- Added an immutable `productionVersion` label to prevent calibration from
  mixing incompatible strategy deployments.
- Wired both general and fast order executors, exchange sessions, and the
  GammaCapture strategy ledger to record lifecycle evidence without changing
  order behavior.
- Preserved cumulative execution average price and normalized exchange order
  status/market percent-price metadata needed by private execution analysis.
- Added online private-fill calibration for touch-to-fill probability and
  adverse selection. Missing or immature private evidence remains diagnostic
  and cannot create a chicken-and-egg no-order gate.

## Replay and research infrastructure

- Added bounded validation stages in which calibration labels replay validity
  but only an explicitly requested calibration-only run stops before signal
  replay. Uncalibrated results cannot promote or tune production policy.
- Added causal preload/score manifests, deterministic parsed replay caches,
  compact BBO intervals, wall-clock limits, chronological validation, and
  replay performance improvements.
- Kept the execution contract at decision BBO `t`, earliest execution at the
  next observed BBO, first-passage passive evidence, immediate partial-fill
  balance updates, and a terminal executable Hold baseline.
- Added standalone and paired studies for causal pivot targets, pivot threshold
  and price basis, causal Kline labels, dynamic price beta, continuation Fast
  WFO, adaptive path decay, horizon-conditioned utility, relative-Hold risk,
  normal-flow pressure, regime persistence, terminal wealth, and fee-free or
  fee/risk-adjusted counterfactuals.
- Recorded negative, inconclusive, calibration-blocked, and research-only
  results in their dated Markdown/JSON artifacts instead of silently enabling
  them in the live strategy.

## Operations

- Added a production restart skill and operations runbook requiring preflight,
  focused tests, reproducible build, userspace service restart, startup prefill
  evidence, journal inspection, and aligned replay caveats.
- Added repository production memory emphasizing loss containment, non-blocking
  model warmup, causal preload, target ownership, and post-restart health.
- Updated tests that require live external APIs or a local listener to be
  explicitly opt-in, keeping normal offline verification deterministic.

## Verification and current evidence

- Focused GammaCapture and research-command package tests pass.
- The 2026-08-27 12:00–18:45 UTC ETHJPY component replay completed with 55,183
  preload BBO events and 4,666 scored BBO events in chronological order. Under
  the declared queue-1 assumption it produced 20 synthetic fills, strategy
  PnL of -3.97 JPY, and Hold PnL of -6.12 JPY.
- That replay did not pass private-fill queue calibration and is therefore an
  engineering regression result, not promotion or parameter-tuning evidence.
- The deployed service restored a ready pivot state with 64 completed-leg
  samples and submitted resting `LIMIT_MAKER` orders without an IOC or
  target-blind quantity fallback after restart.

## Known limitations

- The standalone causal target action study resets its synthetic current weight
  at each anchor and therefore does not replace stateful turnover or private
  fill replay.
- Private queue/fill calibration still needs more production-version-scoped
  observations before synthetic fill PnL can support promotion claims.
- Simultaneous probability-stratified multi-level orders and conditional
  time-to-fill survival modeling are research proposals only; this release
  continues to submit at most one resting maker order per side.
