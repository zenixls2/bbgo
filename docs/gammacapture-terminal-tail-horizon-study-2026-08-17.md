# GammaCapture terminal-tail and horizon-action study (2026-08-17)

## Frozen contracts

### Terminal-tail conditional target

- Name: `terminal-tail-conditional-target`.
- Primary type: inventory risk.
- Mechanism: the full-window price mean is a lagged target for inventory held
  at the horizon.  A geometric mean over the final `tau=min(5m,H/3)` reduces
  terminal-tick microstructure noise while remaining local to maturity.  A
  causal standardized deviation from the six-hour EW equilibrium lets an
  online ridge coefficient decide whether the same-symbol path is reverting
  or continuing; its sign is not fixed in advance.
- Null: the prequential conditional terminal-tail forecast does not reduce
  absolute error versus the existing EW full-window-mean forecast.
- Baseline: existing time-weighted depth-weighted full-window target return.
- Single integration point: `InventoryDirectionalMeanBps`; it may not alter
  crossing probabilities, order payoff, distance, or quantity directly.
- Prediction clock: one-minute anchors using only the latest observed ETHJPY
  BBO and causal six-hour equilibrium state.
- Primary horizon: 15m.  Sensitivity: 30m only.
- Label: log return from current depth-weighted BBO to the geometric mean of
  depth-weighted BBO observations in the final tail of the future window.
- Maturity: `predictionAt+H`; prediction is stored before any label update.
- Training: every minute with overlap weight `1m/H`; scoring every H.
- Gap rule: an anchor or future minute without an observed BBO within one
  minute is ineligible; model state resets across a connection-scale gap.
- Costs: none added because this predicts the inventory target distribution,
  not a trade payoff. Fees remain in the downstream action utility.

### Symmetric inventory-aware horizon action value

- Name: `symmetric-horizon-action-value`.
- Primary type: inventory risk.
- Mechanism: each horizon must be compared with the same causal account state
  and its own target distribution.  The score is the maximum fee-net,
  risk-adjusted certainty equivalent per hour over `{NO_ORDER, BUY, SELL,
  BOTH}` using one executable venue cell per feasible side.  This removes the
  existing BUY-only option asymmetry without a manually fitted side weight.
- Null: paired horizon/action decisions and fee-net replay value are no better
  than the current crossing-score plus marginal-BUY selector.
- Baseline: `scoreFastHorizonWithMarginalBuy`.
- Single integration point: `MarketMakerHorizonDecision.SelectionScoreBpsPerHour`.
- Action label and execution: the existing completed executable-BBO path
  utility, actual configured maker cost, and next-BBO replay contract.
- Inventory: current account-backed notional; candidate actions change it by
  their fill outcomes, including BUY/SELL covariance and target overshoot.
- Multiplicity: one family containing four actions times three horizons;
  existing path standard error and configured confidence z remain active.

No live YAML, service, binary, or order path may change until the corresponding
standalone and component gates pass.

## Results

> 2026-08-17 re-audit: the standalone results below used a fixed non-overlapping
> scoring grid. The corrected expiry-or-crossing clock remains negative: 15m
> equal-day delta `-0.2432 bps` (simultaneous lower `-0.7408`) and 30m
> `-1.1128 bps` (lower `-2.1474`). In addition, the single microprice/final-tail
> label is obsolete relative to the current side-specific executable-BBO,
> post-fill multi-window target. This rejects the old contract only.

### Terminal-tail conditional target: rejected

The standalone scorer used ETHJPY BBO from 2026-08-03 through 2026-08-16 UTC.
It trained every minute with weight `1m/H`, evaluated non-overlapping windows,
and updated labels only at `predictionAt+H`.  The first implementation audit
found that estimating the absolute terminal-tail return discarded the current
full-window baseline.  The final comparison therefore learned only the causal
residual correction to that baseline.  This correction also failed:

| horizon | effective N | full-window MAE | conditional tail MAE | incremental mean / simultaneous lower | positive days |
| --- | ---: | ---: | ---: | ---: | ---: |
| 15m | 1,178 | 9.490743 bps | 10.074480 bps | -0.583737 / -0.759571 bps | 0 / 13 |
| 30m | 589 | 14.070696 bps | 15.167186 bps | -1.096489 / -1.543008 bps | 1 / 13 |

Even the unconditional terminal-tail label was worse than the historical
full-window target.  The failure is therefore not merely the sign or strength
of the mean-reversion coefficient: on this sample, moving the inventory target
toward the final five minutes loses useful averaging information.  The frozen
gate rejects the candidate, so `InventoryDirectionalMeanBps` and the live YAML
remain unchanged.

### Symmetric horizon action value: component replay rejected

The isolated selector enumerates one venue cell for each feasible action in
`{NO_ORDER, BUY, SELL, BOTH}`.  It evaluates all actions from the same current
inventory, horizon-specific target, fee-net path moments, covariance and Kelly
risk penalty, then uses `max(0, CE)/H`.  Unit tests cover reflected BUY/SELL
states, bilateral value, a negative-value no-order decision, independent hard
bounds and common bps/hour units.

The implementation is reachable only through the existing research-disabled
`pathUtilityHorizonSelection` switch.  A compact paired next-BBO replay compared
it with the current marginal-BUY selector using the same starting inventory,
zero queue multiplier and configuration:

| regime | current PnL / hold | symmetric PnL / hold | current fills B/S | symmetric fills B/S | decision |
| --- | ---: | ---: | ---: | ---: | --- |
| decline, 2026-08-03 00:00-08:00Z | -78.0400 / -80.0265 | -80.0265 / -80.0265 | 0 / 1 | 0 / 0 | reject: removed useful SELL |
| rise, 2026-08-05 15:00-21:00Z | +69.1500 / +68.7376 | +68.6421 / +68.7376 | 1 / 0 | 1 / 0 | reject: fell below hold |
| range, 2026-08-08 00:00-12:00Z | +12.0675 / +12.0675 | +12.0675 / +12.0675 | 0 / 0 | 0 / 0 | no value |
| mixed, 2026-08-10 00:00-18:00Z | -42.5338 / -36.8376 | -38.9795 / -36.8376 | 2 / 1 | 1 / 0 | less loss, still below hold |

The symmetric objective improves mixed drawdown but suppresses the only useful
decline SELL and does not restore range turnover.  Its zero-order alternative
is too dominant when horizon selection sees only one venue cell while the
downstream optimizer can expose a larger risk-budgeted quantity.  This is a
model-scale mismatch, not evidence that SELL should be removed.  The switch
stays false and no service restart is warranted.

## Reproducibility

- Standalone model: `terminal_tail_target.go` and focused tests.
- Standalone scorer: `--terminal-tail-target-study`.
- Compact paired replay: `--symmetric-horizon-action-compare`.
- Gate manifest: `gammacapture-terminal-tail-target-experiment-2026-08-17.json`.
- Production config and service state were not modified.
