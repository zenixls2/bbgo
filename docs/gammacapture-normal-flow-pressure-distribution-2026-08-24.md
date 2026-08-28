# GammaCapture ordinary-flow sizing and distribution study

## Scope

This study addresses three observed problems: drawdown deterioration, adverse
selection deterioration, and missing private-fill calibration. It uses the
captured ETHJPY bookticker and trade data from 2026-08-22 through 2026-08-24.
The feature is a five-minute signed trade-notional imbalance and the label is
the next 15-minute BBO-mid log return. Anchors are non-overlapping at 15-minute
intervals. Every model is evaluated before its feature-distribution state is
updated, so the transformation is prequential and label-free.

## Candidate transformations

Let (x_t) be the signed notional imbalance and (n_t) the five-minute trade
count. The existing shrink is

\[
  s_t = \operatorname{clip}\left(x_t \frac{n_t}{n_t+20}, -0.35, 0.35\right),
\]

with activation at (|x_t n_t/(n_t+20)| \ge 0.10). The isolated candidates
keep this evidence shrink and output cap unchanged:

* `current`: (g(x)=x).
* `winsorized`: (g(x)=\operatorname{clip}(x,-0.25,0.25)), which reduces tail
  leverage without changing the side.
* `robust-tanh`: after the current observation is scored,
  (g(x)=\tanh((x-\mu_{t-1})/(2d_{t-1}))), where (mu) is an EWMA center and
  (d) is an EWMA absolute deviation with a 0.05 floor.
* `balanced-rank`: map (x) to its empirical mid-rank in the preceding 96
  anchors and then to (2F(x)-1).

The implementation is isolated in
[`normal_flow_pressure_distribution.go`](/home/zenixls2/src/bbgo/pkg/strategy/gammacapture/normal_flow_pressure_distribution.go)
and is not connected to live YAML or the production quote path.

## Public replay result

The replay contained 117,261 compacted BBO events, 89,940 trades and 191
anchors. The raw imbalance distribution had mean 0.034, standard deviation
0.351, p05 -0.516, median 0.009 and p95 0.616. Thus a Gaussian linear fit is
not justified by this sample; the distribution is bounded, heavy-tailed and
slightly positively shifted.

| Candidate | Active samples | Mean signed 15m move | SE | 95% lower bound | Hit rate | Signal/return corr. |
|---|---:|---:|---:|---:|---:|---:|
| current | 111 | +2.36 bps | 3.69 bps | -4.87 bps | 55.9% | 0.154 |
| winsorized | 111 | +2.36 bps | 3.69 bps | -4.87 bps | 55.9% | 0.122 |
| robust-tanh | 144 | -1.34 bps | 3.16 bps | -7.54 bps | 46.5% | 0.081 |
| balanced-rank | 155 | -1.24 bps | 3.01 bps | -7.15 bps | 48.4% | 0.086 |

The current and winsorized variants have the same sign decisions on this
sample; winsorization only reduces magnitude. The two adaptive transforms
center or re-rank the persistent flow and lose directional value in both daily
blocks.

The four-way alpha gate used adjusted alpha (0.05/4=0.0125). Current and
winsorized were rejected because their simultaneous net lower bound was
negative. Robust-tanh and balanced-rank were rejected as unstable because zero
of two chronological blocks was positive. Gate manifests are stored beside
this report under `docs/`.

## Quantity replay

As a separate replay-only quantity experiment, the ordinary-flow arm scales
both the absolute inventory-risk budget and equity risk-budget ratio by 0.5,
0.75 or 1.0. This avoids changing signal direction while testing whether
smaller tickets repair drawdown or markout.

On the six-hour sanity interval 2026-08-22 00:00–06:00 UTC with 10-second BBO
bucket, the public BBO replay had no data gaps, but private calibration was not
available: actual private fills were supplied as zero while the replay
generated 49 side-fill errors. Therefore these are diagnostic, not promotion
results.

| Risk scale | Net PnL JPY | Max drawdown | 1m markout | 5m markout | Round trips/day |
|---:|---:|---:|---:|---:|---:|
| baseline | -142.74 | 3.273% | -22.73 bps | -1.65 bps | 76.0 |
| 0.50 | -140.40 | 3.346% | -30.13 bps | -1.66 bps | 76.0 |
| 0.75 | -148.82 | 3.348% | -31.21 bps | -3.91 bps | 84.0 |
| 1.00 | -153.30 | 3.427% | -28.19 bps | -2.04 bps | 88.0 |

The smaller ticket did not improve maximum drawdown or one-minute adverse
selection. The apparent 0.5-scale PnL improvement is not enough to outweigh
the worse markout and failed private-fill calibration.

## Engineering decision

No new distribution transform or quantity scale is promoted to live. The
existing normal-flow repair remains a research candidate with its YAML flag
disabled. Before any promotion, capture a private fill ledger containing order
ID, side, quote price, displayed depth, queue estimate, placement/cancel times,
partial fills and post-fill 1m/5m/10m markouts. Calibrate the queue multiplier
on an untouched chronological segment, then rerun the exact paired replay with
the same account and fill calibration for every candidate.

The study command is:

```text
go run ./cmd/gammacapture-mm-research --normal-flow-pressure-distribution-study \
  --symbol ETHJPY --bbo-data data/gammacapture/ETHJPY \
  --replay-from 2026-08-22T00:00:00Z --replay-to 2026-08-24T00:00:00Z \
  --replay-cache-dir data/gammacapture/state/replay-cache \
  --replay-bbo-interval 1s
```

The quantity component runner accepts
`--normal-flow-pressure-risk-budget-scale` and remains replay-only.
