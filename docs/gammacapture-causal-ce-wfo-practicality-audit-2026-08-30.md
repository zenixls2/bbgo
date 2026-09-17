# Causal CE WFO practicality audit — 2026-08-30

## Decision

The WFO is retained as a fast, research-only diagnostic pre-screen, but it is
removed as a production promotion gate. It cannot represent private fills,
queue position, fill latency, or the complete inventory/order lifecycle. Its
`selected` field therefore means “eligible for executable component replay,”
not “safe to deploy.”

## Validation performed

The audit found and corrected an outcome-timestamp defect. The old scorer used
the eventual pivot extreme (`PivotAt`) as the exit BBO even though that extreme
is only identified after the later reversal confirmation (`At`). During the
confirmation delay `PivotAt` can be earlier than the decision anchor. This
created a non-executable path and could make the target policy appear
fee-positive.

The corrected scorer uses the first observed executable BBO at or after the
next pivot confirmation. Regression tests cover a confirmation after the
anchor, a pivot extreme before the anchor, absent maturity, paired candidate /
legacy outcomes, and train-time comparison against legacy.

The second correction makes train and validation use the same paired baseline:
both require candidate delta versus legacy to be non-negative. The old train
check used candidate value versus 50% instead, which was inconsistent with the
validation objective.

## ETHJPY result

Command data: 2026-07-23 through 2026-08-30, 15-minute anchors, 1-second
retained BBO clock, 240 candidates. The capture directory is still being
written by production, so the retained-row count can move slightly between
runs; the rejection pattern is unchanged.

### Executable confirmation outcome

The default confirmation outcome is the honest markout for this target: it
starts at the decision anchor and ends at the first BBO at or after the next
confirmed reversal. It is deliberately harsher than the old label, because a
continuation target is held through the reversal-confirmation delay.

| Check | Result |
|---|---:|
| Retained BBO events | about 1.645 million |
| Parameter combinations | 240 |
| Candidates passing train / validation diagnostic | 0 |
| First rejection: negative train delta vs legacy | 240 |
| Best validation delta vs legacy | -1.1036 bps/anchor |
| Best holdout delta vs legacy | -1.7071 bps/anchor |

This corrected result is materially different from the old report because the
old `PivotAt` endpoint was not executable. It also exposes a second policy
problem: the WFO target is evaluated from a synthetic 50% current inventory at
every anchor, so repeated switching cost and drawdown are not a stateful order
path. The result is therefore a valid warning against the tested CE policy,
but not a standalone pivot-direction accuracy score.

### Prediction-only next-pivot label control

As a control experiment, the same causal states were scored at the next future
pivot extreme, only when that extreme was strictly after the anchor. This is
not an executable exit, so it cannot promote a live policy, but it isolates the
label-horizon effect:

| Check | Result |
|---|---:|
| Candidates passing | 0/240 |
| Best train paired delta | +12.6987 bps/anchor |
| Best validation paired delta | +13.3290 bps/anchor |
| Best validation DD vs legacy | 19.91 vs 2.74 bps |
| Best holdout paired delta | +12.6992 bps/anchor |

The positive control must not be called a directional hit-rate result: the
active leg direction and its eventual `PivotAt` direction are the same leg by
construction. It only isolates the label-horizon mismatch. The zero in the
executable WFO is caused by the CE's continuation-amplitude forecast being
marked after the reversal confirmation, plus the non-stateful 50% inventory
approximation. The production fee is not the sole cause either. With the same
single candidate and cost set to zero, train and validation deltas became
positive (+0.096 and +0.506 bps/anchor), but the validation DD gate still
rejected it (372.35 vs the legacy 45.99 bps). Thus the old gate was detecting
an unstable policy path rather than “no samples.”

## Operational rule

The WFO output must not disable or enable the live CE owner by itself. The
permitted sequence is:

1. use this WFO only to reject obviously weak segmentation / CE candidates;
2. run executable paired component replay with confirmation-time BBO and the
   real target/order lifecycle;
3. require same-symbol private-fill / queue calibration before promotion.

The live configuration comment now reflects this separation.
