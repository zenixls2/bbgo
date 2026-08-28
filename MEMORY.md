# GammaCapture production memory

## Priority

The ETHJPY production strategy is currently losing money. Loss containment is
the first priority: preserving capital and restoring a safe, observable
decision path takes precedence over maximizing turnover, signal utilization,
or research novelty.

## Live-policy invariants

- A model that is not ready must not block quote submission or force a restart.
- New predictors may affect only the explicitly approved target scalar until
  paired walk-forward evidence promotes a broader actuator.
- Target changes must be bounded, uncertainty-shrunk, and constrained by the
  existing hard inventory band.
- Predictors must never directly set price, order quantity, cancellation, or
  side admission without a separate promotion decision.
- Startup preload must use only causal historical BBO data and must persist the
  model plus any open-bar/pending-label state across restart.
- Private-fill calibration is an input to protection only; its absence must
  not create a chicken-and-egg no-trade loop.
- Every production restart requires binary/config verification, healthy
  service status, journal inspection, and evidence that preload completed.

## Current production target owner

The causal pivot-regime CE target is the sole live inventory-target owner. It
uses BBO midpoint directional-change legs, a 26-bps reversal threshold, a soft
50% prior, hard 0%..100% capital bounds, and fee/adverse-selection-adjusted
switching cost. An exhausted but ready pivot is a zero-alpha no-trade state;
it must not revert the account toward 50%.

`PosteriorInventoryTarget`, the legacy `DynamicInventoryAim` actuator, the
regime-conditioned overlay, and `CausalKlinePivotLearner` are disabled in the
live ETHJPY configuration. Their implementations and research artifacts remain
available, but they must not compete with or overwrite the pivot CE target.

When probability-centered quantity projection is unavailable, the live
fallback is target-monotone: it may reduce the current target error but may
never place a quote that increases it. In particular, a 0% target cannot add
ETH and a 100% target cannot reduce ETH.
