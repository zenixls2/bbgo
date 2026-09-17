# Rolling terminal-value continuation correction results

Date: 2026-09-02 (Asia/Tokyo)

## Scope

This run executes the correction plan after the first rolling terminal-value
replay was weaker than its baseline.  It remains a research-only component:
`strategy.go`, the live ETHJPY YAML, orders, balances, private fills, IOC,
cancellation, and production checkpoints were not changed.

The replay uses ETHJPY from `2026-09-01T00:00:00Z` through
`2026-09-02T03:10:00Z`.  The derived preload starts at
`2026-08-31T11:00:00Z` and is based on the longest declared head:
`30m feature window + 30m horizon + 24 * 30m capacity bound`.

## Corrections implemented

- Replaced the high-dimensional lag-by-lag candidate with a 13-feature causal
  rolling residual model using fixed feature coordinates.
- Kept a formal prequential point-feature executable-value baseline and a
  no-skill expanding fee-net side mean.
- The primary candidate now fits only the directional residual. The common
  execution-cost residual is disabled for this ablation because the fee-net
  label already contains entry/exit spread and fee effects.
- Kept separate 15m/30m heads and queues with windows `[15, 30]`.
- Weighted each overlapping matured label by `min(1, 1/H_minutes)` in RLS,
  and separated mean-estimation uncertainty from predictive outcome variance.
- Readiness requires both the configured raw minimum and
  `N_eff >= max(24, 5 * 13) = 65` effective training labels.
- Corrected coverage to `ready anchors / eligible matured anchors`; warmup
  maturities are no longer included in the scored denominator.
- RLS covariance keeps signed off-diagonal terms, and replay preload/state
  history use the maximum declared window.

## Causal replay result

All anchors in the scored interval matured. Readiness is lower because the
effective-label rule is now applied to the candidate; this is a diagnostic
forecast comparison, not a claim of tradable PnL.

| Horizon | Window | Eligible | Matured | Ready | Coverage | Candidate BUY/SELL MSE | Point baseline BUY/SELL MSE | No-skill BUY/SELL MSE | N_eff | Actions |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 15m primary | 15m | 1,630 | 1,630 | 1,391 | 85.3% | 608.33 / 607.36 | 600.60 / 599.77 | 596.23 / 596.24 | 94.86 | 0 / 0 |
| 30m sensitivity | 30m | 1,630 | 1,630 | 399 | 24.5% | 1,022.22 / 1,026.38 | 1,009.24 / 1,011.43 | 1,000.93 / 1,003.79 | 17.36 | 0 / 0 |

The candidate remains worse than both baselines.  On the common ready-anchor
set, the primary average MSE is approximately 1.28% worse than the formal
point baseline and approximately 1.95% worse than no-skill. The 30m sensitivity
is based on only 399 ready anchors and has scored residual `N_eff=17.36`, below
the 65-label research threshold; it is not evidence for promotion. Every
chronological block has zero selected actions, so the alpha gate returns
`INCONCLUSIVE_UNCERTAINTY` rather than fabricating an incremental Sharpe or
P&L result.

## Release decision

`alpha_gate.py` result: `INCONCLUSIVE_UNCERTAINTY`.

The correction fixed the engineering/statistical attribution errors, but it
did not produce a candidate that beats the formal baseline.  No Stage 3 paired
component replay, production integration, restart, or live decision change is
authorized.  The next improvement must be a causal, prequential loss-reducing
change; lowering the action threshold or forcing trades would not be evidence
of improvement.

Artifacts:

- `data/gammacapture/research/ETHJPY-rolling-terminal-weighted-directional-v3-2026-09-02.json`
- `data/gammacapture/research/ETHJPY-rolling-terminal-weighted-directional-v3-2026-09-02-gate.json`
