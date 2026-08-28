# GammaCapture continuation and adaptive path-decay paired WFO

Date: 2026-08-26 JST
Symbol: ETHJPY
Score window: 2026-08-22 00:00Z through 2026-08-26 02:00Z
Blocks: chronological, 17 × 6 hours
Private-fill ledger: unavailable; both studies use public aggregate trade replay only.

## Contract

Each candidate was compared with a paired baseline on the same BBO/trade path,
starting balances, queue multiplier, causal warmup, and execution simulator.
The paired block statistic is candidate minus baseline. The lower 95% value is a
one-sided descriptive normal lower bound across chronological blocks; it is not a
claim of independent samples or a multiple-testing-adjusted p-value.

The studies are isolated replay harnesses. They do not edit the live YAML, deploy
a binary, or restart a service. The new preload-only path updates causal
estimators before the score boundary and skips quote/lifecycle work before that
boundary, so a shorter WFO sampling interval does not silently remove model
history.

## 1. Continuation posterior, Fast-only architecture

The baseline is the Fast quote/execution path with a fixed 50/50 inventory
target. The candidate keeps that path and enables only the causal continuation
posterior as the Macro no-trade inventory target. Trend excursion, legacy
continuation caps, active Macro IOC, downside-risk mode, hold protection, and
HAR variance control are disabled in the candidate. This explicit isolation was
added after the first harness pass showed that inherited controls could be
mistaken for continuation alpha.

| Metric | Baseline | Candidate | Candidate minus baseline |
|---|---:|---:|---:|
| Net PnL (JPY) | -190.0785 | -67.9762 | +122.1023 |
| Max drawdown | 4.3332% | 3.0610% | -1.2723 pp |
| Fills | 314 | 44 | -270 |
| Mean 6h block delta (JPY) | — | — | +7.3314 |
| One-sided lower 95% block delta (JPY) | — | — | +1.7766 |
| Positive delta blocks | — | — | 13/17 |
| Candidate excess versus hold, mean (JPY) | — | — | +1.1910 |
| Candidate excess versus hold, lower 95% (JPY) | — | — | -4.4998 |

Decision: **reject WFO**. The candidate now beats the paired Fast baseline with
a positive lower bound and lower drawdown, but it does not beat Hold with a
positive lower bound and its fill count falls 86%. This is not enough to call
continuation posterior alpha, and there is no private-fill calibration.

The first harness pass had inherited live hold-protection/HAR controls and
produced 23 fills; it is not used as evidence. The corrected isolated result
above is the source of truth.

### Prior shrinkage sensitivity

A fixed research sensitivity set `PriorStrength=0.20` was tested to pull the
continuation aim toward the 50/50 prior and reduce target noise. It did not
resolve the economic gate: candidate net -67.1754 JPY, DD 3.0610%, 41 fills,
mean block delta +7.3786 JPY, delta lower95 +1.8315 JPY, and hold-relative
lower95 -4.4446 JPY. The sensitivity is therefore also rejected; it is not a
parameter to promote from this same holdout.

## 2. Adaptive path decay

The baseline and candidate differ only in
`JointDistanceQuantity.AdaptivePathDecay`. This engineering screen used a 30s
BBO interval; an exact final replay would use 1s, but a negative paired result
does not justify deployment of the candidate.

| Metric | Fixed decay | Adaptive decay | Candidate minus baseline |
|---|---:|---:|---:|
| Net PnL (JPY) | -171.6818 | -185.5886 | -13.9068 |
| Max drawdown | 3.7933% | 4.1269% | +0.3336 pp |
| Fills | 334 | 317 | -17 |
| Mean 6h block delta (JPY) | — | — | -0.8525 |
| One-sided lower 95% block delta (JPY) | — | — | -3.7409 |
| Positive delta blocks | — | — | 8/17 |
| Candidate excess versus hold, mean (JPY) | — | — | -5.8259 |
| Candidate excess versus hold, lower 95% (JPY) | — | — | -10.1666 |

Decision: **reject WFO**. The candidate loses both on net PnL and drawdown in
the paired replay. The earlier single positive interval is not robust enough to
override this chronological test.

## 3. Engineering result

Target-relative CE unit and targeted integration tests pass, but this is a
formula/source result, not a production promotion result. The CE audit found the
corrected whole-position-minus-baseline variance formulation in source; the
integrated 48-hour replay still deteriorated versus the legacy arm and private
fill calibration failed. Therefore the CE change remains source/research-only.

No candidate in this document passed all of the required conditions:

- positive paired lower bound;
- no drawdown deterioration;
- no unexplained fill collapse;
- private-fill calibration; and
- untouched or final-resolution walk-forward confirmation.
