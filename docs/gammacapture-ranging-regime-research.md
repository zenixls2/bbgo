# ETHJPY ranging-regime replay

## Blind regime selection

This study uses ETHJPY only.  It does not use another ticker and does not read
strategy P&L while choosing windows.

From 2026-07-23 through 2026-08-07, the selector samples the indexed ETHJPY BBO
every 15 minutes, evaluates 12-hour windows on a six-hour grid, ranks retraced
path length, and then keeps at most six non-overlapping windows.  A candidate
must satisfy all of the following before any order simulation runs:

- absolute endpoint displacement no more than 25% of intrawindow log range;
- total path variation at least 1.5 times the range;
- at least two crossings of the geometric center outside a two-bps deadband;
- at least two reversals of returns whose magnitude exceeds three bps.

The six selected windows have efficiency ratios from 1.08% to 6.72%, 16--25
return reversals, and 597--997 bps of 15-minute path variation.  The selection
artifact is `/tmp/gamma-eth-range-windows-v1.json`.

## Replay controls

Every window starts from 6,808 JPY with exactly 50% ETH at its own opening BBO
mid and 50% JPY.  Both policies use the same next-BBO execution, visible queue
multiplier 1, 10-bps-per-side Binance fees, quantity projection, bounded Macro
IOC, and 240-hour causal warmup:

- `posterior`: complete same-ticker continuation posterior with QV fallback;
- `QV`: QV-only no-trade controller;
- `hold`: the unchanged 50/50 starting portfolio.

## Results

| Start UTC | Hold P&L | Posterior P&L | QV P&L | Posterior DD | QV DD | IOC posterior/QV |
|---|---:|---:|---:|---:|---:|---:|
| 07-23 18:00 | +6.42 | +1.00 | +1.00 | 0.671% | 0.671% | 10 / 10 |
| 07-28 06:00 | +30.82 | +18.23 | +6.15 | 0.987% | 1.018% | 8 / 15 |
| 07-28 18:00 | +11.94 | +12.88 | +3.59 | 1.196% | 1.298% | 2 / 19 |
| 07-29 12:00 | -15.84 | -12.70 | -25.02 | 1.456% | 1.301% | 6 / 20 |
| 07-30 18:00 | +8.85 | +9.24 | -8.71 | 0.832% | 1.077% | 4 / 23 |
| 08-04 12:00 | +0.69 | +0.45 | -1.85 | 0.582% | 0.733% | 7 / 10 |
| **sum** | **+42.89** | **+29.10** | **-24.84** | -- | -- | **37 / 97** |

Posterior minus QV is positive in five windows and tied in the one window where
the continuation posterior is unavailable.  The paired mean improvement is
8.9902 JPY per 12-hour window, paired t=3.271 with five degrees of freedom, and
a two-sided 95% t interval `[1.9253, 16.0551]` JPY.  Because the sample is only
six adjacent/non-overlapping market windows, this is evidence rather than a
claim of stable long-run alpha.

Posterior minus hold averages -2.2979 JPY per window, t=-0.973, with 95% interval
`[-8.3673, 3.7714]`.  It beats hold in three of six windows.  The data therefore
do not establish either superiority or inferiority to hold in a ranging regime.

Execution totals explain most of the improvement over QV:

| Metric | Posterior | QV |
|---|---:|---:|
| Net P&L | +29.10 JPY | -24.84 JPY |
| Fees | 14.65 JPY | 27.99 JPY |
| Macro IOC fills | 37 | 97 |
| Maker fills | 46 | 52 |
| Round trips | 35 | 63 |
| Target total variation | 3.667 | 3.618 |
| Mean max drawdown | 0.954% | 1.016% |
| Worst max drawdown | 1.456% | 1.301% |

The posterior target is not smoother than QV; its total variation is nearly
the same.  The no-trade region acts on fewer of those movements, cutting IOC by
61.9%, fees by 47.7%, and round trips by 44.4%.  It is therefore the predictive
distribution plus execution boundary, not simple target smoothing, that avoids
most QV churn.

## Engineering defect found by the range test

In the earliest window, continuation training data are unavailable for all 720
minutes.  The feature flag nevertheless marked the continuation mode applied
and skipped QV aim-measurement variance.  The supposedly inactive policy then
diverged from QV and lost 6.27 JPY instead of earning 1.00 JPY.

The controller now sets `ContinuationMixtureApplied` only after a healthy
posterior actually replaces the QV forecast.  If the posterior is unavailable,
all QV fallback fields, Kalman measurement variance, orders, fills, fees, P&L,
and drawdown are exactly equal.  The corrected replay ties QV at +1.0042 JPY.

This source fix is covered by an exact decision-structure unit test and the
production replay artifact
`/tmp/gamma-eth-range-20260723T18-v2-fallback-fix.json`.  The combined corrected
summary is `/tmp/gamma-eth-range-summary-v2.json`.

## Decision

Do not tune the live posterior from these six windows.  The current model is
materially better than QV-only in ranging data and does not show a statistically
resolved difference from hold.  The discovered fallback fix should be deployed
separately after explicit restart approval; no live setting was changed by this
study.
