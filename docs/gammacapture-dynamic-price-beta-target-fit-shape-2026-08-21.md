# Dynamic price-beta target fit-shape and 1D ML screen (2026-08-21)

## Contract

- **Primary type:** inventory risk / target actuator.
- **Prediction time:** each compacted one-minute ETHJPY BBO observation,
  using only the preceding six hours.
- **Label:** executable future bid return over the frozen 15-minute horizon.
- **Effective samples:** 384 non-overlapping anchors; chronological OOS fit
  comparison uses 288 anchors after the first 96-anchor training block.
- **Null:** no fitted state-to-return relationship improves the constant mean
  out of sample; no production target change is authorized.

This screen evaluates the shape of the relationship between the causal z-score
and future executable-bid return. It does **not** estimate an optimal inventory
target: that requires fee-net marginal terminal wealth under actual inventory
and execution actions.

## Distribution

| causal z-score bin | n | mean future return (bps) | standard deviation (bps) |
|---|---:|---:|---:|
| z < -1 | 133 | 1.96 | 20.95 |
| -1 <= z < 0 | 61 | -0.40 | 29.18 |
| 0 <= z < 1 | 59 | 8.26 | 63.25 |
| 1 <= z < 2 | 31 | 0.21 | 14.70 |
| z >= 2 | 100 | 13.12 | 49.75 |

The conditional means are not reliably linear: the `1 <= z < 2` bucket falls
back near zero while `z >= 2` rises again. Dispersion is much larger than the
mean differences, especially in the neutral-to-positive region.

## Chronological out-of-sample comparison

The first 96 anchors train each model; the next three chronological blocks are
scored without refitting on their labels. The constant mean is the null.

| model | OOS RMSE (bps) | OOS MAE (bps) | OOS R2 | OOS correlation | sign accuracy | positive blocks |
|---|---:|---:|---:|---:|---:|---:|
| constant mean | 45.31 | 22.61 | -0.014 | -0.053 | 49.0% | 0/3 |
| binary active/inactive | 45.32 | **22.55** | -0.014 | 0.008 | **55.2%** | 1/3 |
| linear | 45.77 | 22.96 | -0.035 | -0.044 | 49.7% | 0/3 |
| quadratic | 46.07 | 22.80 | -0.048 | -0.143 | 50.0% | 0/3 |
| piecewise-linear | 46.15 | 22.87 | -0.052 | -0.139 | 50.3% | 0/3 |
| isotonic-increasing | **45.19** | 22.56 | **-0.008** | **0.055** | 53.1% | 1/3 |
| isotonic-decreasing | 47.34 | 23.13 | -0.107 | -0.399 | 47.9% | 1/3 |
| 1D kNN, k=8 | 46.18 | 23.52 | -0.053 | -0.018 | 55.2% | 1/3 |
| 1D kNN, k=16 | 45.77 | 22.98 | -0.034 | -0.017 | 48.3% | 1/3 |
| 1D kNN, k=32 | 45.29 | 22.56 | -0.013 | 0.042 | 50.7% | 1/3 |
| Gaussian kernel, h=0.5 | 46.23 | 23.01 | -0.056 | -0.110 | 50.7% | 0/3 |
| Gaussian kernel, h=1 | 46.15 | 22.80 | -0.051 | -0.134 | 51.4% | 0/3 |
| Gaussian kernel, h=2 | 46.24 | 22.64 | -0.056 | -0.231 | 50.3% | 0/3 |

Isotonic-increasing is numerically the best RMSE, followed by 1D kNN with
`k=32`, but its improvement over the constant mean is only about `0.12 bps`,
with one of three positive chronological blocks and negative OOS R2. The
advantage is not statistically or economically reliable. The linear model and
all Gaussian-kernel variants are worse than the constant mean.

The kNN and kernel models are strictly one-dimensional supervised estimators:

```text
z_t = causal feature at time t
y_t = executable future bid return at t + 15m
f_t(z_t) = fit only on labels matured before t
```

They estimate future return, not the optimal inventory target. A target still
requires maximizing a fee-net marginal utility such as

```text
U(beta) = predicted_return * inventory(beta)
          - risk_aversion * predicted_variance * inventory(beta)^2
          - execution_cost(beta)
```

Therefore even a better return predictor would not by itself justify lowering
`PriceBetaTarget`.

## Decision

```text
REJECT_UNSTABLE
```

The data do not support selecting a linear fit or deploying a one-dimensional
ML predictor. If a provisional research shape must be retained, isotonic or
large-k kNN is the least unstable diagnostic, while binary is the most
interpretable. None has production value. Do not tune the `0.20` floor or
connect the fitted shape to live `PriceBetaTarget`.

The next valid study must use the existing prequential terminal-return
forecast and the actual fee-net marginal inventory-wealth response. It should
compare target policies, not merely predict future price return, with paired
no-cap actions, drawdown, ETHJPY correlation, fill/partial-adjustment costs,
and block-bootstrap uncertainty.

## Verification

```text
GOCACHE=/tmp/bbgo-go-cache go test ./cmd/gammacapture-mm-research -run 'TestCompactBBOAtInterval' -count=1
GOCACHE=/tmp/bbgo-go-cache go run ./cmd/gammacapture-mm-research --dynamic-price-beta-target-study --symbol ETHJPY --bbo-data data/gammacapture/ETHJPY --replay-from 2026-08-17T00:00:00Z --replay-to 2026-08-21T00:00:00Z --dynamic-price-beta-target-horizon 15m --dynamic-price-beta-target-interval 1m
python3 /home/zenixls2/.codex/skills/gammacapture-alpha-screening/scripts/alpha_gate.py /tmp/dynamic-price-beta-target-fit-experiment.json
```

No live YAML, strategy integration, component replay, or service restart was
performed.
