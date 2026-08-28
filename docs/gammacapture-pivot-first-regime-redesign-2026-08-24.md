# GammaCapture pivot-first regime redesign — 2026-08-24

## Decision

The previous 5-minute raw-score bucket is not a regime definition. It is only
a sampling cadence. The redesigned research component makes an economic
directional-change pivot the source of truth:

1. A leg starts only after price moves by the configured economic reversal
   threshold.
2. A pivot is confirmed only after the running extreme reverses by the same
   threshold.
3. The active state is the direction of the last confirmed pivot leg.
4. The target actuator uses the empirical remaining amplitude of completed
   same-direction legs, not `abs(rawTag) >= 0.35`.

The implementation consumes every compacted causal BBO event. The 5-minute
interval is used only to form evaluation anchors; it does not change the
pivot state.

## Mathematical actuator

For direction \(d_t\in\{-1,+1\}\), current leg amplitude \(A_t\), and the
causal mean amplitude of completed same-direction legs \(\bar A_{d_t}\),
define:

\[
\hat r_t = [\bar A_{d_t}-A_t]_+.
\]

After fee and risk allowance (c_t), the continuous size scale is:

\[
q_t = \operatorname{clip}\left(
  \rho_t\frac{[\hat r_t-c_t]_+}{\bar A_{d_t}},
  0,1
\right),
\]

where \(\rho_t=n_t/(n_t+n_0)\) shrinks the scale until enough completed
legs have been observed. The signed target shift is:

\[
\Delta w_t=d_t\,\kappa q_t.
\]

This is a continuous fee/risk-aware actuator. It can return zero without
declaring the whole regime model invalid, and it does not require a positive
chronological block before producing a bounded research signal.

## Causal replay

Data: ETHJPY BBO, 2026-07-23 through 2026-08-24. Training ends 2026-08-17,
validation ends 2026-08-22, and the untouched holdout is 2026-08-22 through
2026-08-24. The quote-action evaluation horizon is 15 minutes and the
fee/adverse-selection allowance is 20 bps.

The reversal candidates were predeclared as 20, 26, 35, 50, and 75 bps. The
26 bps candidate is the economic floor used in the earlier audit; it is not
selected from the holdout.

| Source | Reversal | Holdout active anchors | Weighted action value/anchor | Active action value |
| --- | ---: | ---: | ---: | ---: |
| Pivot-first | 20 bps | 106 | −0.14 bps | −16.89 bps |
| Pivot-first | 26 bps | 188 | −0.47 bps | −18.21 bps |
| Pivot-first | 35 bps | 245 | −0.99 bps | −15.60 bps |
| Pivot-first | 50 bps | 301 | −1.58 bps | −18.45 bps |
| Pivot-first | 75 bps | 442 | −3.23 bps | −19.63 bps |
| Previous raw tag 0.35 | — | 329 signals | −21.06 bps | −21.06 bps |

The weighted result improves mainly because the pivot actuator reduces size
when the remaining leg amplitude is not fee-positive. The active directional
leg value is still negative for every candidate, and no candidate has a
positive holdout block count. This is a risk/turnover improvement, not proof
of standalone alpha.

## Engineering integration boundary

The causal component is now wired into the strategy behind an explicit
`dynamicInventoryAim.pivotRegimeTarget.enabled` flag. When enabled, the live
BBO path observes every valid BBO event, bypasses the old time-bucketed
ML-tagged target adapter, and sends the resulting fee-aware continuous shift
through the existing inventory target actuator. It does not create a second
quote gate, side filter, order path, or Fast executable-price forecast. A
missing/invalid pivot state, gap reset, or non-fee-positive remaining leg
fails closed to the policy target.

The ETHJPY live profile explicitly keeps this flag disabled until private
fill/queue calibration demonstrates positive active-leg value. The production
code and config are therefore aligned: the repair is present and testable,
but the negative holdout result is not promoted as live alpha. The pure
component is:

- `pkg/strategy/gammacapture/pivot_regime.go`
- `cmd/gammacapture-mm-research/pivot_regime_study.go`

The next research step is not to tune another score threshold. It is to fit
side-specific remaining-amplitude and pivot-duration distributions, then
calibrate private-fill/queue execution before enabling the bounded shift in
the live profile.
