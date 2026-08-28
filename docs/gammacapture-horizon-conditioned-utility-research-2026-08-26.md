# Horizon-conditioned continuation utility research

Date: 2026-08-26

Decision: **not ready for production**.

## Research question

The short terminal CE can reject a passive order because the first 15 minutes
have adverse selection, even when the current pivot leg later continues. The
candidate therefore combines:

1. short executable-BBO markout, including the 10 bps maker fee and 2 bps
   adverse-selection allowance;
2. conditional markout from the short maturity to the next confirmed pivot;
3. public-touch fill probability as a temporary arrival proxy; and
4. fill/no-fill mixture variance and target-relative inventory risk.

It is a quantity-only component. It cannot select direction, price, cancel
timing, or inventory aim.

## Decision function

For notional (q), fill probability (p), conditional combined value (m),
baseline variance (V_0), after-fill variance (V_1), and effective sample
size (N):

\[
E[\Delta W] = p m,
\]

\[
V_{mix}=(1-p)V_0+pV_1+p(1-p)m^2.
\]

The last term is arrival uncertainty; omitting it would overstate the value of
low-probability passive fills. The model-estimation variance is

\[
V_{mean}=p^2 SE(m)^2+m^2SE(p)^2.
\]

The lower confidence delta and risk-adjusted CE are

\[
L=E[\Delta W]-z\left(\sqrt{(V_{mix}/N)+V_{mean}}-sqrt{V_0/N}\right),
\]

\[
CE=L-\gamma\frac{V_{mix}-V_0}{2E_{pair}}.
\]

The selector chooses the highest positive CE among notional ratios
\(0.25,0.5,1,1.5\), with zero quantity as the implicit abstention. Unit tests
cover continuation offsetting a short loss, arrival variance, risk-reduction
repair, invalid inputs, and conservative candidate selection.

## Causal replay

The pivot filter observes every retained BBO event. At each 15-minute anchor,
the scorer predicts from the current causal pivot state and stores a pending
label. The short execution label updates only at the first BBO at or after
15 minutes. The continuation label uses the BBO at the pivot extreme but is
not used to update the estimator until the later pivot confirmation timestamp.
This prevents the common leakage of using a known future extreme before the
regime has confirmed.

The primary label is next confirmed pivot with a maximum six-hour confirmation
window. A fixed 60-minute continuation was tested as a neighboring sensitivity.
EWMA estimators use the configured six-hour horizon lookback; phase cells are
early/middle/late within the current causal leg and fall back to pooled side
data when sparse.

## Results

ETHJPY, 2026-07-24 through 2026-08-25, 5-second retained BBO, 15-minute
anchors:

| Split | Scored | Enhanced accepted | Mean realized bps/anchor | Positive 6h blocks | Block SE bps |
| --- | ---: | ---: | ---: | ---: | ---: |
| Train | 1,168 | 127 | +0.438 | 16/58 | 1.297 |
| Validation | 431 | 0 | 0.000 | 0/24 | not identified |
| Holdout | 702 | 118 | +0.502 | 12/32 | 1.707 |

The holdout point estimate is positive but is not statistically stable. With
three predeclared candidate variants, the alpha gate gives a one-sided lower
bound of **-3.132 bps**. The public-touch Brier score is 0.2274. The phase
conditioning did not improve the holdout selection relative to the pooled-side
variant, and the fixed 60-minute variant was negative on validation and empty
on holdout.

The short-only corrected CE baseline accepted zero candidates in this replay;
that is useful evidence that the continuation hypothesis changes the selection,
but not evidence that the selected orders are executable or profitable after
private queue effects.

## Engineering status

Implemented and tested:

- pure quantity utility in
  [horizon_conditioned_utility.go](/home/zenixls2/src/bbgo/pkg/strategy/gammacapture/horizon_conditioned_utility.go);
- causal standalone replay in
  [horizon_conditioned_utility_study.go](/home/zenixls2/src/bbgo/cmd/gammacapture-mm-research/horizon_conditioned_utility_study.go);
- delayed confirmation and paired-increment tests in
  [horizon_conditioned_utility_study_test.go](/home/zenixls2/src/bbgo/cmd/gammacapture-mm-research/horizon_conditioned_utility_study_test.go);
- alpha manifest in
  [gammacapture-horizon-conditioned-utility-gate-2026-08-26.json](/home/zenixls2/src/bbgo/docs/gammacapture-horizon-conditioned-utility-gate-2026-08-26.json).

Focused and full tests passed for `pkg/strategy/gammacapture` and
`cmd/gammacapture-mm-research`. No production strategy wiring, YAML,
checkpoint, service restart, or live order behavior was changed.

## Production decision and re-open conditions

Do not promote this component. Two independent reasons are sufficient:

1. the multiplicity-adjusted holdout lower bound is negative; and
2. private-fill calibration is unavailable (zero private fills).

The next valid experiment must capture immutable same-symbol order submission,
replacement, cancellation, partial-fill, and execution records with causal
BBO snapshots and quote distance. Then rerun the frozen scorer on a new
untouched holdout. Do not lower the CE threshold or retune the existing sample
to force a pass.
