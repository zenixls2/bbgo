# Causal pivot-regime inventory target — 2026-08-27

## Decision

The fixed 50% inventory value is now treated as a soft prior in the new
causal pivot-regime target owner. It is not a safety target and it is not a
position cap. The target optimizer can select any value in the configured
hard capital interval, including 0% or 100%, when the causal regime evidence
and the fee/risk-adjusted objective support the boundary.

The feature is implemented behind
`dynamicInventoryAim.pivotRegimeTarget.causalCEEnabled`. Per explicit operator
request, the live ETHJPY YAML now enables this owner, disables the competing
posterior/Kline target owners, and keeps the legacy DynamicInventoryAim
actuator off. Private-fill/queue calibration is still an outstanding release
risk; enabling this feature does not imply that calibration has passed.

## Target execution ownership

Fast target execution, including marketable-limit IOC rebalancing, is an
execution actuator rather than a posterior-inventory-target actuator. It
consumes the executable forecast attached to the owner of the selected
target, in this order: causal pivot CE, dynamic inventory aim, then the legacy
posterior target. A target owner that does not expose an executable price
forecast remains passive-only; decoupling the IOC gate must not manufacture a
crossing signal from a target label alone.

Live diagnostics expose `fastTargetExecutionEvidenceSource` and
`fastTargetExecutionEvidenceReady` separately from
`posteriorInventoryTargetEnabled`. A disabled posterior target therefore no
longer falsely reports that IOC is disabled when another target owner is
ready.

## Mathematical contract

For risky inventory weight `w`, current weight `w_t`, and strategic prior
`w_0`, the target maximizes

```text
CE(w) = μ w - 1/2 γ σ² w² - 1/2 κ (w - w₀)² - c |w - w_t|
```

where:

- `μ` is the signed expected remaining pivot-leg return after reliability and
  effective-sample shrinkage;
- `σ²` is the causal same-direction completed-leg variance;
- `γ` is risk aversion;
- `κ` is prior strength;
- `c` is one-way maker fee plus adverse-selection cost.

The optimizer explicitly compares the current point, both stationary points,
and both hard bounds. The fee is an L1 switching cost inside the objective,
not a binary `expected value <= 0` order gate. This gives a genuine no-trade
region around the current inventory while allowing a strong regime to move to
the full admissible range.

The pivot filter observes every valid BBO midpoint (`BBO/2`), confirms a
directional change only after the configured reversal, and uses only completed
same-direction legs for the expectation and variance. Pivot state is now
included in model checkpoints and is replayed during startup prefill.
When the CE owner is enabled, startup requires a bounded 24-hour pivot context
so a short six-hour Fast horizon cannot leave the target owner cold after a
checkpoint rebuild; the replay still compacts BBO input to one observation per
second before updating models.

## Implementation

- `pkg/strategy/gammacapture/causal_regime_inventory_target.go`: pure CE target
  solver and pivot adapter.
- `pkg/strategy/gammacapture/pivot_regime.go`: completed-leg variance plus
  bounded snapshot/restore support.
- `pkg/strategy/gammacapture/strategy.go`: optional target-owner integration;
  causal CE target updates the actual inventory band and downstream projection.
- `pkg/strategy/gammacapture/model_checkpoint.go`: pivot state checkpoint,
  checkpoint version 16.
- `pkg/strategy/gammacapture/maker_startup_warmup.go`: midpoint pivot prefill
  on the same causal event clock as live quoting.
- `cmd/gammacapture-mm-research/causal_regime_inventory_target_study.go` and
  `production_replay.go`: isolated action screen and paired production replay.

The legacy `MaxShiftRatio` remains for the legacy pivot actuator. It is not
used by the new `causalCEEnabled` target owner, so the old ±20 percentage-point
cap cannot silently constrain the new path. Account, exchange, and configured
capital bounds remain hard.

## Evidence

### Long action-level walk-forward: ETHJPY, 2026-08-01..08-27

The candidate and legacy arms use identical causal pivot geometry and identical
next-confirmed-pivot outcomes. The candidate uses the CE target; legacy uses
the former ±20 percentage-point actuator.

| Split | Candidate mean net | Legacy mean net | Candidate delta | Candidate cumulative net | Candidate max DD | Positive blocks |
|---|---:|---:|---:|---:|---:|---:|
| Train | 3.558 bps | 0.330 bps | +3.229 bps | 1540.78 bps | 12.67 bps | 41/50 |
| Validation | 3.173 bps | 0.309 bps | +2.865 bps | 736.18 bps | 11.76 bps | 21/25 |
| Holdout | 4.386 bps | 0.488 bps | +3.898 bps | 986.78 bps | 9.51 bps | 26/26 |

These are executable next-pivot markouts, not private-fill PnL. They support
the direction of the change, but by themselves do not establish execution
safety.

### Full paired production replay: ETHJPY, 2026-08-20 12:00..08-27 00:00 UTC

Both arms used the same 5-second BBO replay, account seed, quote logic, and
explicit queue assumption `1`.

| Metric | Existing baseline | Causal pivot CE |
|---|---:|---:|
| Full fills | 617 | 511 |
| Buy / sell fills | 317 / 300 | 260 / 251 |
| Net PnL | 221.24 JPY | 237.58 JPY |
| Hold PnL | 359.58 JPY | 359.58 JPY |
| Maximum DD | 4.440% | 4.303% |
| BBO events | 105,546 | 105,546 |
| Data gaps | 0 | 0 |
| Early stop | false | false |

The candidate improved replay net PnL by 16.34 JPY and reduced measured DD by
0.137 percentage points, but both arms remained below hold. The candidate
evaluated 105,546 causal target states, with 76,901 ready/applied states, mean
target weight 34.84%, mean target delta -13.67 percentage points, and 22,784
full-flat target states. It selected no full-long state in this particular
down-biased sample; that is an observed market outcome, not an implementation
cap. Unit tests verify that sufficiently strong bullish evidence selects 100%.

The replay preload was chronological: 18,296 warmup BBO events from
2026-08-20 05:30 UTC followed by 105,546 score events. No private-fill labels
were used to manufacture the candidate target.

The first operator-directed live restart initially exposed that the former
six-hour warmup could leave the pivot state without enough completed
same-direction legs (`pivotRegimeReady=false`) even while Fast quoting was
healthy. The implementation now makes the CE owner's 24-hour startup history
an explicit bounded requirement and reports pivot readiness in the warmup log.

## Calibration and release status

The replay had only 2 actual private BUY fills and 1 actual private SELL fill;
the explicit queue-1 synthetic replay did not pass calibration (`calibration
absolute side error = 614`). Therefore the result is valid for implementation
and causal-path regression testing, but not for choosing a live queue model or
claiming execution safety.

Required follow-up after the operator-directed live enablement:

1. collect a larger production-version-scoped private order/fill ledger;
2. fit queue and touch-to-fill behavior by side and quote distance;
3. rerun the same paired replay with the calibrated queue and a holdout period;
4. require positive active-leg value, no material DD deterioration, and stable
   results across chronological blocks before treating the feature as
   calibration-approved.

## Verification

```text
GOCACHE=/tmp/bbgo-gocache go test ./pkg/strategy/gammacapture ./cmd/gammacapture-mm-research
```

The current test suite passes. The live YAML activation and service restart are
performed as part of the operator-directed release; post-restart health and
prefill evidence must still be checked in the deployment record.
