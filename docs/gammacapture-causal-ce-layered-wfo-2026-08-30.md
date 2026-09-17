# Layered causal CE WFO — 2026-08-30

## Implementation

The research runner now exposes:

```text
--causal-regime-inventory-target-layered-wfo
```

It keeps three measurements separate while reusing the existing causal BBO
preload and pivot filter:

1. **Signal layer.** The future pivot extreme is used only as a label. It
   reports forecast remaining amplitude, realized continuation, signed error,
   absolute error, and positive six-hour error blocks. It does not report
   direction accuracy: the active leg direction and that leg's eventual
   extreme direction are mechanically aligned.
2. **Fixed-horizon CE layer.** `15m`, `30m`, and `60m` outcomes use the first
   BBO at or after `anchor + horizon`. The CE expected return is scaled by the
   causal expected remaining leg duration before the target optimizer runs.
3. **Stateful policy layer.** Candidate and legacy accounts carry base/quote,
   mark equity, current weight, target changes, turnover, costs, drawdown, and
   step returns across anchors. Rebalances execute at the captured BBO with
   the configured one-way cost. This is still an anchor-level execution
   approximation; it does not model queue position or private fills.

The stateful layer is the promotion-oriented diagnostic. It uses the same
train/validation/holdout boundaries and paired legacy path for both arms. The
selection gate remains train delta >= 0, validation delta >= 0, and validation
drawdown no worse than legacy. Holdout is reported but not used for selection.

## ETHJPY replay

Replay interval: `2026-07-23T00:00:00Z` through
`2026-08-30T00:00:00Z`; train ends `2026-08-11T00:00:00Z`; validation ends
`2026-08-20T12:00:00Z`; 15-minute anchors; 1-second retained BBO. The live
capture directory is append-only, so the retained BBO count can change by a
small amount between runs.

### Decision cost 12 bps, execution fee 10 bps

The CE switching-cost prior is 12 bps. The stateful account now charges only
the explicit 10 bps execution fee on the executed notional; the BBO ask/bid
already supplies the spread component. This avoids charging the same 12 bps as
an additional all-in cost on top of the executable BBO.

| Layer | Result |
|---|---:|
| Signal segmentations | 15 |
| Horizon/CE candidates | 720 |
| Stateful candidates passing train/validation gate | 0 |
| Stateful train-negative rejection | 573 |
| Stateful validation-negative rejection | 147 |

The best validation-delta candidate that was still rejected was approximately
`reversal=35 bps`, `horizon=15m`, `risk=0.005`, `prior=16`: train delta
`-671.26`, validation delta `-463.69`, validation DD `475.32` versus legacy
`185.29`, and holdout delta `-372.18` bps on the normalized account path.
Its validation turnover was `36.81` initial-equity turns and explicit fees were
`368.06` bps. The rejection is now an interpretable stateful policy result,
not an unexplained outcome-label failure.

### Removing only the stateful execution fee

Keeping the 12 bps CE decision-cost prior but setting only the stateful
execution fee to zero produced 18/720 candidates passing the train/validation
gate. The best eligible validation score was approximately
`reversal=20 bps`, `horizon=30m`, `risk=0.005`, `prior=8`:

- train delta: `+12.82 bps`;
- validation delta: `+405.05 bps`;
- validation DD: `139.99` versus legacy `140.53` bps;
- holdout delta: `+196.58 bps`.

This is not promotion evidence because the zero execution-fee assumption is
not the production fee model. It does show that the previous extra cash
charge was materially suppressing the policy and that the remaining 10 bps
explicit fee is economically binding. The next calibration step is therefore
private-fill/queue estimation of actual executed quantity and fee, not
weakening the WFO gate.

## Decision

The layered WFO is retained as a research diagnostic and executable-component
pre-screen. It is not wired to production enablement. A future candidate must
first survive this stateful paired replay, then the existing exact production
replay with private-fill calibration, before any live configuration change.
