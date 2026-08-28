# Horizon-conditioned terminal utility research contract

Date: 2026-08-26

Status: the Stage 0 contract was frozen before replay. The implementation and
standalone replay are complete; the component is rejected for production and
does not authorize a live configuration change.

## Hypothesis

- Name: `horizon-conditioned-continuation-utility`
- Primary type: quantity / utility sizing
- Mechanism: a passive order can have negative 15m/30m markout while still
  having positive value after the current pivot leg continues. The decision
  therefore prices the short executable-BBO outcome and the conditional
  continuation value in one Bellman-style certainty equivalent. Inventory
  variance is measured against the no-order target-relative baseline.
- Null: adding continuation value and calibrated fill probability does not
  improve fee-net excess versus the corrected terminal CE baseline after
  uncertainty, inventory risk, and turnover costs.
- Existing baseline: the current corrected
  `JointPathPayoffDecision.CertaintyEquivalent`, with the existing price and
  direction decisions frozen.
- Single integration point: one non-negative order-notional scale in the
  quantity layer. The component must not select direction, price distance,
  cancellation, or inventory target.

## Causal clock and labels

- Prediction time: the current executable BBO and all model state available at
  that timestamp.
- Primary short horizon: 15 minutes, matching the live directional horizon.
- Primary continuation label: the next confirmed pivot leg in the current
  causal direction. Its maturity is the confirmation timestamp, while its
  endpoint is the executable BBO at the pivot extreme. A fixed 60-minute
  continuation is a predeclared neighboring sensitivity, not the primary
  target; the horizon is deliberately not forced to be short when the
  economic label is pivot-to-pivot.
- Short label maturity: the first observable BBO at or after prediction plus
  15 minutes. BUY uses entry ask and terminal bid; SELL uses entry bid and
  terminal ask.
- Continuation label: next-pivot executable wealth minus the matured short
  terminal wealth, conditional on the candidate fill. It is a continuation
  markout, not another entry trade, so the entry fee is charged exactly once
  in the short component. A pending label is updated only when the pivot
  confirmation is observed; a pivot extreme before confirmation is not used
  as a prediction-time feature.
- Update order: predict and store the pending state, then update only when the
  corresponding executable-BBO label has matured. No future pivot or terminal
  price may enter the prediction.
- Gap/reset: a missing or non-monotonic BBO segment invalidates the pending
  label and resets the local scorer; it is never imputed.
- Overlap: anchors are spaced by the primary short horizon. Effective sample
  size is reported by chronological blocks, not raw overlapping rows.

## Executable economics

- One-way maker fee: the configured maker fee, currently 10 bps in the ETHJPY
  profile.
- Adverse-selection allowance: the configured 2 bps allowance.
- The short conditional payoff is already fee/adverse-selection net. The
  continuation markout carries no second entry fee because it is the value of
  holding the already-filled inventory from 15m to 60m.
- The corrected target-relative confidence term is

  \[
  E[\Delta W] - z\left(\sqrt{V_{whole}/N}
                         -\sqrt{V_{baseline}/N}\right).
  \]

- If a private fill probability is unavailable, the study may use a public-BBO
  touch proxy only as a labelled sensitivity. It cannot pass the production
  gate without private queue/fill calibration.

## Acceptance metrics

The primary response is fee-net incremental utility versus the corrected
terminal CE baseline. Every report must include eligible/effective samples,
chronological block means and lower bounds, positive blocks, fill coverage,
turnover, maximum drawdown, Hold-relative excess, and BUY/SELL symmetry.

The component is not promotion-ready unless:

1. causal and label-order checks pass;
2. the primary next-confirmed-pivot result (with a maximum 6-hour label
   window) has a positive chronological-block lower bound;
3. the fixed 60-minute continuation sensitivity does not show a material sign
   reversal;
4. the value survives private-fill calibration and the same next-BBO replay
   contract; and
5. paired component replay improves excess versus Hold without increasing
   drawdown or relying on a fill-count increase.

Failure is recorded as `INCONCLUSIVE` when effective samples or private-fill
coverage are insufficient. Threshold relaxation alone is not a promotion
mechanism.

## Completed replay decision

The implementation is in [horizon_conditioned_utility.go](/home/zenixls2/src/bbgo/pkg/strategy/gammacapture/horizon_conditioned_utility.go),
and the standalone scorer is in
[horizon_conditioned_utility_study.go](/home/zenixls2/src/bbgo/cmd/gammacapture-mm-research/horizon_conditioned_utility_study.go).
The scorer keeps direction, price distance, cancellation, and inventory target
unchanged; it only compares quantity candidates. Labels update only at their
short-horizon or pivot-confirmation maturity.

The final ETHJPY replay covered 2026-07-24 through 2026-08-25 with 15-minute
anchors and 5-second retained BBO observations. The untouched 2026-08-17
through 2026-08-25 holdout produced:

- 702 scored anchors and 32 chronological six-hour blocks;
- 118 continuation candidates accepted, 61 public-touch outcomes;
- +0.502 bps mean fee/adverse-selection-net incremental value versus no
  action, with 12/32 positive blocks;
- 1.707 bps block standard error and a multiplicity-adjusted one-sided lower
  bound of -3.132 bps;
- public-touch Brier score 0.2274 and mean estimator effective sample 14.01
  per scored anchor;
- no historical private order/queue ledger, so private-fill calibration is
  `FAILED_NOT_AVAILABLE`.

The fixed 60-minute continuation sensitivity was also rejected: its validation
point estimate was approximately -0.52 bps and its holdout accepted no
candidate. The phase-conditioned next-pivot variant did not improve the
holdout over the pooled-side selection. The alpha manifest and deterministic
gate output are recorded in
[gammacapture-horizon-conditioned-utility-gate-2026-08-26.json](/home/zenixls2/src/bbgo/docs/gammacapture-horizon-conditioned-utility-gate-2026-08-26.json).

Decision: `REJECT_NO_INCREMENTAL_VALUE_AND_PRIVATE_FILL_NOT_AVAILABLE`. The
component remains research-only. Do not add it to strategy quantity sizing,
YAML, checkpoint, userspace service, or restart workflow. Re-open the gate only
after collecting a same-symbol private-fill ledger and a new untouched
chronological holdout; do not tune this sample to remove the negative lower
bound.
