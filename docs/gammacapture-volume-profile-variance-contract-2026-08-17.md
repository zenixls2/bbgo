# GammaCapture Volume Profile conditional-variance contract (2026-08-17)

## Frozen hypothesis

- **Primary type:** inventory risk.
- **Mechanism:** profile density/corridor state may contain information about
  the conditional dispersion of executable terminal outcomes even though its
  directional mean alpha was rejected.
- **Null:** Volume Profile features do not improve strictly-prequential
  conditional-variance forecasts over an intercept-only exponentially weighted
  variance model.
- **Baseline:** the existing BBO-only terminal mean plus its causal unconditional
  residual variance.
- **Single possible integration point after promotion:**
  `InventoryDirectionalVarBps2`. The candidate cannot alter terminal mean,
  price offset, quantity, crossing probability, allow-side gates, or orders
  during screening.

## Causal response and clock

For each existing event-clock observation, first store the BBO-only terminal
mean predictions. When its executable BUY/SELL label matures, define

\[
e_B=Y_{BUY}-\widehat Y_{BUY},\qquad
e_S=Y_{SELL}-\widehat Y_{SELL},\qquad
e_D=\frac{e_B-e_S}{2}.
\]

The primary target is \(e_D^2\); BUY and SELL squared residuals are mandatory
symmetry diagnostics. The primary window is 15 minutes and the predeclared
sensitivity is 30 minutes. Fill-latency calibration, 80% lifecycle coverage,
label maturity, executable BBO convention, fee-net outcomes, 2026-08-01 through
2026-08-08 calibration, and 2026-08-09 through 2026-08-16 holdout remain frozen.

The variance learner predicts log variance to guarantee positivity. An
intercept-only EW ridge model is the baseline; the candidate uses the frozen
12-dimensional bounded VP state. Each has its own delayed EW log-bias
calibration. Prediction is stored first, and neither variance model nor its
calibrator may update until the associated terminal label matures. Missing
event-clock observations create no synthetic BBO or label.

The proper Gaussian quasi-likelihood score is

\[
L(v,e^2)=\log v+\frac{e^2}{v}.
\]

Daily blocks compare \(L(v_{base},e_D^2)-L(v_{VP},e_D^2)\). This score is
dimensionless and already conditions on the fee-net executable terminal
response; no trading fee or P&L is deducted a second time. Promotion requires
a positive multiplicity-adjusted one-sided lower bound at 15m, a non-negative
30m bound, at least five of eight positive days, finite predictions, and
reasonable calibration ratios on both sides. Canonical regime P&L is forbidden
unless this standalone gate first passes.

## Implementation audit and holdout result

Two implementation corrections were required before the result was accepted:

1. An initial log-bias calibration targeted geometric residual scale. QLIKE
   and inventory second moments require the arithmetic optimum
   \(s^*=E[e^2/\widetilde v]\), so it was replaced by a delayed EW QLIKE scale.
2. An initial candidate let the 12-dimensional VP model replace baseline
   variance. The frozen incremental hypothesis requires
   \(\log v^{candidate}=\log v^{BBO}+\widehat r^{VP}\), so VP was changed to a
   calibrated residual learner anchored to the same baseline.

Focused tests cover QLIKE optimality, 30-label calibration delay, finite
positive output under extreme log variance, and BUY/SELL reflection symmetry.
The corrected residual-on-baseline model produced:

| Fast window | samples | baseline QLIKE | VP QLIKE | equal-day improvement | simultaneous lower bound | positive days |
|---|---:|---:|---:|---:|---:|---:|
| 15m | 774 | 62.1519 | 12.6481 | +94.3191 | -90.7401 | 3 / 8 |
| 30m | 558 | 35.8895 | 22.8504 | +34.0395 | -33.0984 | 2 / 8 |

The positive sample mean is not stable evidence. A few catastrophic baseline
underpredictions dominate QLIKE, while the VP candidate overpredicts variance
for most observations. Directional actual/predicted calibration was 0.0756 at
15m and 0.0949 at 30m; side calibration was worse, and both 30m side QLIKE
increments were negative. Such a variance input would systematically suppress
capital utilization and quoting outside rare spikes.

Decision: **`REJECT_UNSTABLE`**. The reproducible standalone study is retained,
but VP is not connected to `InventoryDirectionalVarBps2`, quote distance,
quantity, live YAML, or service state. Canonical regime P&L replay is not
permitted because the Stage-2 gate failed.

Reproduction:

```bash
go run ./cmd/gammacapture-mm-research \
  --config config/gammacapture-ethjpy.yaml --symbol ETHJPY \
  --bbo-data data/gammacapture \
  --replay-cache-dir data/gammacapture/state/replay-cache \
  --replay-from 2026-08-01T00:00:00Z \
  --replay-to 2026-08-17T00:00:00Z \
  --volume-profile-variance-study --volume-profile-fill-coverage 0.8
```
