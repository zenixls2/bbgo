# Public-flow pivot competing hazard study — 2026-09-01

## Scope

This is a research-only component. It does not modify the production YAML,
strategy wiring, live checkpoint, or systemd service.

The study uses ETHJPY BBO and public aggregate trades from
2026-07-24 00:00 UTC through 2026-09-01 00:00 UTC. A six-hour causal preload
is read before the scored interval. The preload seeds pivot history, the
causal run-length posterior, and flow intensity; predictions anchored before
the scored interval are excluded from all reported metrics. Labels mature only
after the frozen 3-minute or 5-minute horizon.

Primary settings:

- 30-second prediction anchor
- 26 bps pivot reversal label
- 15-minute maximum BBO gap reset
- 20 bps round-trip executable cost
- 3-minute primary horizon
- 5-minute neighboring-horizon sensitivity

## Model

For each 30-second bin (k), the model estimates a three-way conditional
hazard:

\[
P(J_k = c \mid T \ge k, x_t),
\qquad c \in \{\mathrm{no\ event},\mathrm{UP},\mathrm{DOWN}\}.
\]

The cumulative event probabilities are propagated through survival:

\[
F^{UP}_H = \sum_{k \le H} S_{k-1}p^{UP}_k,
\qquad
F^{DOWN}_H = \sum_{k \le H} S_{k-1}p^{DOWN}_k,
\]

\[
S_k = S_{k-1}p^{no\ event}_k.
\]

The online feature vector combines:

- verified pivot history and current-leg gradients;
- a bounded Bayesian run-length posterior over causal BBO flow pressure;
- exponentially decayed signed public-trade intensity;
- existing L1 OFI, queue imbalance, microprice, trade imbalance, and spread
  features.

The hazard update is strictly prequential. For a first-passage event in bin
(j), earlier bins receive `no event` and bin (j) receives its cause. A
fully censored horizon receives `no event` in every bin.

## Result

| Horizon | Event precision | Event coverage | Brier improvement | Incremental value | Positive blocks |
| --- | ---: | ---: | ---: | ---: | ---: |
| 3m primary | 51.02% | 2.49% | +0.0293 | -0.451 bps/block | 1/156 |
| 5m sensitivity | 55.45% | 7.08% | +0.0566 | -1.241 bps/block | 1/156 |

The 3m model produced 94,282 mature scored labels, including 5,719 UP and
5,801 DOWN first-passage labels. The 5m model had 8,850 UP and 8,933 DOWN
labels. The run-length and intensity components were ready throughout the
scored replay; the final full replay recorded 921,192 BBO observations and
563,596 public-flow events.

The component improves probability discrimination relative to the empirical
baseline, especially at 5m, but it does not reach the 60% point precision
target and its executable fee-net action value is negative. It therefore fails
the research gate and must not enter production.

## Engineering checks

- The loader includes the requested six-hour warm-up and loads through
  `To + max(horizon)` so final pending labels mature.
- BBO warm-up is compacted to one observation per second; public trades remain
  trade-level and are processed only up to the current prediction timestamp.
- A BBO gap greater than 15 minutes clears pending labels and causal path
  state, while retaining learned hazard coefficients.
- No ex-post pivot timestamp, future pivot direction, or future trade is used
  as a feature.
- The 3m full replay completed in about two minutes after compilation; the
  fixed 1-second data cache prevents model-setting changes from reparsing raw
  captures.

## Decision

`REJECT_UNSTABLE`. Keep the implementation research-only. The next valid
experiment is calibrated selective prediction or a duration-specific hazard
model with a predeclared acceptance coverage; do not lower the threshold or
turn on live actions merely to force a 60% point estimate.
