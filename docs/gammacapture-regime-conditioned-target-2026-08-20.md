# Regime-conditioned inventory target (2026-08-20)

## Alpha contract

- **Name:** bounded regime-conditioned inventory target
- **Primary type:** inventory risk / target selection
- **Mechanism:** combine the validated Fast terminal side-BBO drift and
  calibrated BOCPD45 posterior into one bounded signed posterior, shrink it by the least
  effective sample count, and apply one clipped target shift.  The target is
  the only downstream output; price, quantity, and side gates remain owned by
  the existing Fast joint optimizer.
- **Null:** the combined posterior has no incremental information over the
  neutral policy target, so the target remains unchanged.
- **Existing baseline:** the configured neutral inventory target plus the
  existing DynamicInventoryAim target actuator.
- **Single integration point:** `DynamicInventoryAimDecision.AdjustedTargetRatio`.

## Causal clock and outcome

The component uses only snapshots available at the current BBO decision and
updates its posterior once per configured five-minute model bucket:

- Fast terminal-return probability
  \(P(r_H>0)=\Phi(\mu_H/\sigma_H)\), available only after strictly
  prequential side-BBO drift validation; and
- BOCPD45 calibrated up probability and matured-label count.

Horizon up/down crossing intensities were present in the first implementation
but were removed after review. They estimate passive quote arrival/touch
probability, not terminal-price direction. Feeding them into both quote
distance and inventory target duplicated the same first-passage evidence and
created a sell bias in the rising evening sample.

It does not create a fill label and does not access future BBO.  The terminal
wealth optimizer remains responsible for executable next-BBO outcomes, fees,
and final order admission.  Signals are combined once, rather than being
applied independently to price, quantity, and hard gates.

## Bounded rule

For each available source (i), let (p_i) be its up posterior and (w_i)
its confidence/evidence weight.  The implementation uses a normalized,
correlation-conservative logit aggregate:

\[
r_t=\tanh\left(\sum_i\frac{w_i}{1+\sum_jw_j}\operatorname{logit}(p_i)\right),
\qquad
\Delta I_t=\operatorname{clip}\left(
\kappa r_t\sqrt{\frac{N_{\rm eff}}{N_{\rm eff}+N_0}},
\pm\Delta_{\max}\right),
\]

\[
I_t^*=\operatorname{clip}(I_0^*+\Delta I_t,I_{\min},I_{\max}).
\]

`N_eff` is the minimum positive effective sample count among the participating
sources.  This avoids treating correlated BBO-derived signals as independent
observations.  Default bounds are `kappa=0.20`, `maxShiftRatio=0.20`, and
`priorEffectiveSamples=8`; the terminal-wealth gate still decides whether an
order is executable.

## Promotion checks

The focused tests cover neutral, monotone up/down, conflicting evidence,
sample shrinkage, hard bounds, invalid input, terminal-drift semantics,
five-minute sample-and-hold, and deterministic bounded output.
The component must pass these tests before live configuration or service
restart.  A full replay is a later component/regression check, not a tuning
step.

## Single action value: no stacking

The regime posterior changes only `I_t^*`.  Once a completed terminal-path
posterior is available, every `NONE`, `BUY`, `SELL`, or `BOTH` candidate is
compared with `NONE` by one target-relative action value:

\[
\Delta U_t(a)=\mathbb E[\Delta W_H(a)]-LPM_-(a)
-\frac{\gamma}{2W_t}
\left(\operatorname{Var}[W_H\mid a]-
\operatorname{Var}[W_H\mid\mathrm{NONE}]\right).
\]

Fees and executable-BBO markout are already included in
`E[Delta W_H(a)]`; the variance difference already includes covariance with
`I_t-I_t^*`.  Therefore a separate target-progress reward, target-side gate,
price skew, or quantity multiplier must not be added to a mature candidate.
This rule applies symmetrically: a profitable SELL is not vetoed merely because
the regime target is bullish, and a profitable BUY is not vetoed merely because
the target is bearish.  Each still must have positive posterior action value
and enough venue inventory/cash to satisfy exchange filters.

`TargetProgressContinuationValue` remains only as a data-gap prior when no
mature terminal path exists.  It is mutually exclusive with the mature
target-relative value, so inventory preference is counted exactly once.

## Evening replay result and production decision

The causal replay covers `2026-08-20 19:49:49` through `2026-08-21 01:10:00`
JST, with model preload beginning at `10:48:35`. At the scoring boundary the
replay now restores the actual live account (`0.0205444 ETH`, pair equity
`7437.81601817 JPY`) instead of allowing synthetic preload fills to alter the
starting inventory. This makes both arms' Hold P&L `+170.3747 JPY`, within
`0.13 JPY` of the observed online `+170.4980 JPY`.

With terminal-drift semantics and five-minute sample-and-hold, the candidate
produced `+99.0546 JPY` versus the retired stacked baseline's `+97.4293 JPY`,
but maximum drawdown worsened from `1.4410%` to `1.6426%`; fills changed from
`15 BUY / 13 SELL` to `15 BUY / 20 SELL`, farther from the observed online
`10 BUY / 12 SELL`; and 1m/5m markouts worsened. The synthetic execution
calibration therefore failed. The candidate is retained for research but
`regimeConditionedTarget.enabled` is **false** in the live ETHJPY YAML. No
production restart or promotion is justified by this result.

## Replay correction and theory revision (2026-08-21)

The previous target-action replay did not construct its arms explicitly. The
candidate inherited `DynamicInventoryAim` and
`regimeConditionedTarget` from the selected YAML. The default SOL research
configuration did not enable either field, so the reported comparison was not
guaranteed to be `single bounded target` versus `retired stacked target`.
That result is invalid for attributing the drawdown, fill, or markout change to
the regime-conditioned target.

The replay now constructs both arms in code: the baseline disables the dynamic
inventory actuator and enables only the retired stacked continuation; the
candidate enables `DynamicInventoryAim` with exactly one
`regimeConditionedTarget` actuator and disables stacked continuation. A unit
test asserts this contract even when the input YAML omits all target fields.

The theory is also narrowed. A regime posterior is a directional prior for the
inventory target, not an economic permission to carry more inventory. The
posterior may determine the sign and bounded size of a target shift only when
the same terminal-path risk gradient clears the one-way maker/adverse cost at
the configured confidence bound. Otherwise the target remains at the policy
target. This prevents a calibrated classification probability from becoming a
directional inventory bet without fee-net terminal-wealth evidence.

The corrected paired replay was then run on the same ETHJPY evening window
(`2026-08-20T10:49:49Z`--`16:10:00Z`, one-second BBO buckets, queue multiplier
`0.25`, actual score-account restore). The retired stacked arm produced
`82.2539 JPY` net P&L with `1.5508%` maximum drawdown and `16 BUY / 21 SELL`
completed orders. The single-target arm produced `79.7103 JPY`, `1.9666%`
maximum drawdown, and `10 BUY / 20 SELL`. Its 1-minute and 5-minute markouts
improved, while its 10-minute markout improved to `+3.82 bps` from `-2.60
bps`; that did not compensate for lower terminal P&L and higher drawdown.

Private-fill calibration still failed: without the userspace journal the
replay's synthetic side counts differed by 34 orders from the supplied
calibration counts. This result is therefore diagnostic, not a promotion
result. The candidate remains disabled and requires a fresh same-symbol
holdout with authenticated lifecycle calibration.
