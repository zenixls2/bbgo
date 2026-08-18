# GammaCapture downside e-process study

## Question

The 2026-08-03 ETHJPY stress replay showed that the QV-time inventory
controller reduced exposure only after a persistent decline was already under
way.  This study tested whether a distribution-free, time-uniform sequential
test could detect a statistically unusual executable-price drawdown early
enough to block long re-entry without fitting another direction classifier.

## Model

For an executable log-price path in quadratic-variation time, the continuous
local-martingale null is

    X_A = B_A.

For a fixed negative-drift alternative with magnitude `theta >= 0`, the
likelihood-ratio process is

    L_theta = exp(theta * X_A - theta^2 * A / 2),

where `X_A` is running-high minus current log price and `A` is pathwise
quadratic variation accumulated since that high.  Mixing `theta` over a
half-normal prior with scale `tau` gives the closed-form e-value

    E = 2 / (tau * sqrt(B))
        * exp(X_A^2 / (2*B))
        * Phi(X_A / sqrt(B)),
    B = A + 1/tau^2.

The implementation uses separate best-ask and best-bid paths and requires both
to cross the threshold.  It averages the predeclared 10/15/30-minute scale
e-values rather than selecting their maximum.  A single price jump contributes
large QV and is therefore penalized; repeated small same-direction moves can
accumulate evidence.  New highs reset drawdown evidence, and after an alarm a
new running-low recovery process controls release.

The threshold is `1/alpha`, with the configured `z=1.645` converted to the
one-sided Gaussian tail (`alpha ~= 0.05`, threshold `20.006`).  This is a
time-uniform evidence threshold under the local-martingale null, not a claim
that `E/(1+E)` is a universally calibrated market probability.  BBO dependence,
jumps, and microstructure also mean that empirical false-alarm validation is
still required.

## Synthetic verification

Unit tests verify:

- initial e-value normalization and one-jump QV penalty;
- alarm and recovery on persistent executable bid/ask moves;
- no alarm from spread widening alone;
- price-scale invariance;
- state reset across a market-data gap.

## ETHJPY result

The study used ETHJPY BBO from 2026-07-23 through 2026-08-08.  Outcomes were
competing executable first passages over 15- and 30-minute horizons.  A down
passage requires the future ask to fall far enough from the signal-time bid to
clear the complete 26-bps round-trip economic cost; the up event is symmetric.

| Horizon | Alarms | Down first | Up first | Unconditional down rate | Result |
|---|---:|---:|---:|---:|---|
| 15 minutes | 1 | 0 | 1 | 49.68% | reject |
| 30 minutes | 1 | 0 | 1 | 51.54% | reject |

The only alarm occurred at 2026-07-23 15:13 UTC and resolved up-first six
minutes later.  There was no alarm on 2026-08-03, including before the sharp
00:28 UTC fall.  The test is consequently too sparse and had the wrong observed
conditional outcome.  Its time-uniform type-I guarantee under an idealized null
does not supply useful downside power in this archive.

## Decision and interaction with HAR/Kalman

The original direction-classifier proposal remains rejected: the threshold is
not lowered and the e-process does not create an inventory target or gate
ordinary maker orders. It is now used narrowly as a noise/jump-robust active-
execution waiting-cost input only after the unchanged time-uniform alarm and
only when the Fast posterior has already produced the same-side target gap.
SELL uses executable bid/ask directly. BUY uses reciprocal executable BBO so
the original ask path remains the forecast side, then additionally requires no
active downside alarm and positive portfolio certainty equivalent. This
preserves the negative standalone-classification result without introducing a
second inventory controller.

HAR and Kalman solve different problems:

- side-specific online HAR can forecast the magnitude of future ask/bid
  variance directly and does not mathematically require Kalman filtering;
- HAR variance has no sign, so it cannot identify a downside continuation or
  choose a long/short inventory target;
- the inventory aim still needs a causal state model because noisy rolling
  direction estimates otherwise produce repeated entry/exit turnover;
- the tested immediate post-Kalman HAR denominator adjustment improved the
  early stress slice but worsened the complete 2026-08-03 replay through risk
  release and re-entry churn.

Therefore HAR remains a separately estimated risk input behind a disabled
research flag.  It must not bypass the directional posterior or the
proportional-cost no-trade region.

## References

- Corsi, *A Simple Approximate Long-Memory Model of Realized Volatility*,
  Journal of Financial Econometrics, DOI 10.1093/jjfinec/nbp001.
- Howard, Ramdas, McAuliffe, and Sekhon, *Time-uniform, nonparametric,
  nonasymptotic confidence sequences*, Annals of Statistics 49(2), 2021.
- Shin, Ramdas, and Rinaldo, *E-detectors: a nonparametric framework for
  sequential change detection*, 2022.
