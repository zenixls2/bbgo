# Causal Kline pivot replay — 2026-08-25

## Frozen replay

- Symbol: ETHJPY
- BBO data: `data/gammacapture/ETHJPY`
- Interval: 2026-07-25 00:00 UTC through 2026-08-24 00:00 UTC
- Train: through 2026-08-09
- Validation: 2026-08-09 through 2026-08-17
- Holdout: 2026-08-17 through 2026-08-24
- Primary horizon: 15 minutes
- Non-overlapping scoring anchor: 15 minutes
- Complete fee/adverse-selection/residual cost: 26 bps
- Chronological uncertainty blocks: 6 hours
- Candidates tested before scoring: 3m/3-bar and 5m/3-bar

Bars were built from the captured BBO mid stream. A bar became usable at the
first observed BBO after its close. The next bar confirmed the previous bar's
HIGH/LOW/NEUTRAL label. The model prediction was stored before that label
matured; model update happened only after maturity.

## Results

| interval | holdout eligible | accuracy | action mean | action lower bound | incremental vs existing pivot baseline | positive blocks |
|---|---:|---:|---:|---:|---:|---:|
| 3m | 598 | 65.72% | -7.15 bps | -8.42 bps | +20.61 bps | 28/28 |
| 5m | 560 | 62.14% | -6.75 bps | -8.13 bps | +18.70 bps | 28/28 |

The Kline learner contains predictive information relative to the current
event-driven directional-change baseline, but its own executable action is
still negative after the complete cost floor. The positive relative result is
therefore not sufficient evidence to move inventory or quote size in live
trading.

## Decision

`REJECT_NO_FEE_NET_VALUE` for production integration. The implementation is
kept as an isolated causal learner and replay tool. The live ETHJPY strategy
was not modified, restarted, or given a new target actuator.

The next valid experiment is a paired inventory-target component replay using
the learner's probability-weighted, bounded target shift. It must compare the
same starting inventory and executable terminal wealth against the current
target, include private-fill calibration, and require a positive fee-net
holdout lower bound before any live activation.
