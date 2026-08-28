# GammaCapture price-beta target 0.20: private calibration and walk-forward

## Frozen contract

- **Primary type:** inventory risk / target actuator.
- **Hypothesis:** capping marked risky inventory at `priceBetaTarget=0.20`
  reduces ETHJPY price exposure and drawdown without destroying positive
  risk-adjusted return.
- **Null:** the existing DynamicInventoryAim target without a beta cap.
- **Single integration point:** `DynamicInventoryAimDecision.AdjustedTargetRatio`.
- **Causal label:** no future label is used by the cap; orders decided at
  `BBO[t]` execute no earlier than the next observed BBO.
- **Execution:** same ETHJPY executable-BBO replay, same maker/taker fee
  model, same starting account, same 1-second BBO compaction, and same
  public aggregate-trade fill proxy in both arms.

## Private fill calibration

The userspace journal was streamed directly into the research CLI (`--journal-data -`);
no private journal export was committed. The calibration cutoff was strictly
before the scoring interval:

- calibration: `2026-08-17T00:00:00Z`–`2026-08-20T10:49:49Z`;
- maker orders: `865` (`460 BUY`, `405 SELL`);
- confirmed maker fills: `160` (`68 BUY`, `92 SELL`);
- selected visible-BBO queue multiple: `0`;
- selected proxy precision/recall: `0.9195 / 1.0000`;
- public-cross side-count error: `14` orders.

The sample passes the minimum count floor, but the lifecycle calibration gate
fails because the public-cross proxy does not reproduce the confirmed BUY/SELL
counts within one order. Queue `0` is therefore a fixed diagnostic estimate,
not evidence that the exchange queue is actually zero and not a live promotion
parameter.

## Walk-forward replay

Scoring used the same account and fixed parameter after the calibration cutoff:

- walk-forward interval: `2026-08-20T10:49:49Z`–`2026-08-21T07:10:00Z`;
- pair equity: `7437.81601817 JPY`;
- starting base: `0.0205444 ETH`;
- queue proxy: `0`;
- target cap: `0.20`;
- four contiguous 5-hour diagnostic blocks, with the final block having zero
  fills and excluded from effective fill-block inference.

The original requested 5h interval was replayed separately as an exact paired
check:

| interval | arm | net P&L JPY | max DD | Sharpe | Hold corr. | Hold beta | fills |
|---|---|---:|---:|---:|---:|---:|---:|
| 2026-08-20 10:49:49–16:10Z | null | 90.12 | 1.360% | 24.84 | 0.9886 | 0.5459 | 37 |
| same | target 0.20 | 38.53 | 0.694% | 24.19 | 0.9690 | 0.2502 | 51 |

The continuous 20h replay, which avoids resetting account state at block
boundaries, gave:

| arm | net P&L JPY | excess vs Hold JPY | max DD | Sharpe | Hold corr. | Hold beta | fills |
|---|---:|---:|---:|---:|---:|---:|---:|
| null | 156.38 | -177.71 | 1.360% | 20.67 | 0.9891 | 0.5306 | 108 |
| target 0.20 | 65.08 | -269.01 | 0.694% | 19.81 | 0.9632 | 0.2318 | 129 |

The candidate reduced the continuous replay's correlation by `0.0259`, beta
by `0.2988`, and drawdown by `0.666` percentage points, while reducing net
P&L by `91.30 JPY` (`-122.75 bps`) and Sharpe by `0.86`. Its aggregate Sharpe
remained positive, but the three effective 5-hour blocks had only one positive
incremental P&L block; the candidate Sharpe was negative in two of those
blocks. The block-level mean incremental value was `-34.76 bps`, standard error
`30.11 bps`, with a descriptive one-sided 95% lower bound of `-84.29 bps`.

## Gate and decision

```json
{
  "name": "gammacapture-price-beta-target-020-private-calibrated-walkforward",
  "causal": true,
  "leakage_checks_passed": true,
  "effective_samples": 3.0,
  "minimum_effective_samples": 5.0,
  "incremental_mean_bps": -34.76,
  "incremental_standard_error_bps": 30.11,
  "incremental_cost_bps": 0,
  "risk_penalty_bps": 0,
  "candidate_tests": 1,
  "alpha": 0.05,
  "positive_blocks": 1,
  "total_effective_blocks": 3,
  "minimum_positive_blocks": 3,
  "gate": "INCONCLUSIVE_UNSTABLE_WALK_FORWARD",
  "promotion": "REJECT"
}
```

The target `0.20` is not promoted. It does achieve the desired direction for
correlation, beta, and drawdown, but fails the stronger requirement of stable
incremental value and positive block-level Sharpe. The live ETHJPY YAML and
userspace service remain unchanged. More same-symbol private fills should be
collected before testing a materially different target; do not tune `0.20`
again on this scoring sample.

Focused verification passed:

```text
go test ./pkg/strategy/gammacapture ./cmd/gammacapture-mm-research
git diff --check -- cmd/gammacapture-mm-research/order_lifecycle.go
```
