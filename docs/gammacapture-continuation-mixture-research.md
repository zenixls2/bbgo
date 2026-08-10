# GammaCapture single-ticker continuation posterior

## Objective and invariant

The Macro controller should avoid premature long exposure when a falling
ETHJPY path pauses briefly and then continues lower.  It must reduce loss and
drawdown relative to holding ETH through falling stress paths without using a
second symbol.

Only ETHJPY executable BBO, public ETHJPY trades, and causally closed ETHJPY
model history are inputs.  No other ticker is a feature, training source,
prior, calibration source, or runtime dependency.

## Estimators rejected before integration

1. A QV-time half-normal e-process emitted only one alarm from 2026-07-23
   through 2026-08-08.  It was wrong and did not signal before the 2026-08-03
   fall.
2. A standalone nearest-neighbor consolidation classifier had Brier skill of
   -17.53% at 15 minutes and -11.85% at 30 minutes.  A Gaussian-prior Bayesian
   logistic version remained negative at -10.70% and -13.16%.

They are retained only as negative research evidence and do not affect orders.

## Accepted predictive law

For the current short-window ETHJPY path, causally matured, non-overlapping
historical analogs have three mutually exclusive outcomes over the shortest
Macro forecast horizon:

- `up`: buy at the current ask and a later bid clears complete round-trip cost;
- `down`: sell at the current bid and a later ask clears the symmetric cost;
- `censor`: neither executable passage occurs before the horizon.

With counts `(n_up,n_down,n_0)` and a symmetric `Dirichlet(1,1,1)` prior,

    p_j = (n_j + 1) / (n + 3),     j in {up, down, censor}.

Let positive executable excursions be `X_up` and `X_down`.  A censored path
has return zero.  The complete posterior-predictive moments are

    mu_C = p_up E[X_up] - p_down E[X_down],

    V_C  = p_up E[X_up^2] + p_down E[X_down^2] - mu_C^2.

When one direction has no observed magnitude, its symmetric Dirichlet
pseudo-event receives the pooled magnitude of both resolved directions.  It
does not silently become a zero-loss event.  Mean-estimation variance combines
the analytic Dirichlet linear-functional variance with conditional magnitude
mean variance.

The important correction on 2026-08-07 is that `mu_C` and `V_C` already carry
the censor probability.  The earlier candidate incorrectly multiplied them
again by `1-p_censor` while blending with QV.  Resolved-event mass is not a
posterior probability that the continuation model is correct.  That operation
double-counted quiet-path uncertainty and had no valid model-selection
interpretation.

The deployed structure is therefore model selection, not an independence or
precision mixture:

    healthy continuation posterior -> use (mu_C, V_C)
    otherwise                       -> use the same-ticker QV crossing law

The two estimators share ETHJPY history, so treating them as independent would
manufacture precision.  A healthy continuation law passes through exactly one
regularized Merton target,

    w* = clip((mu_C + kappa w_prior) / (gamma V_C + kappa), w_min, w_max),

one posterior-uncertainty Kalman state, and one proportional-cost no-trade
region.  The legacy continuation hard cap and long-window trend target remain
off.  A continuation posterior may own the target when sparse QV crossings are
`DEGRADED`; missing QV sign evidence no longer disables a complete, causally
matured continuation law.

## ETHJPY production-replay evidence

All executions use next-BBO semantics, queue multiplier 1, Binance's 10 bps
per-side fees, executable bid/ask prices, the production quantity/IOC code,
and no other ticker.  Starting balances must be feasible spot balances.  The
replay now rejects `startingBase * openingMid > pairEquity` instead of silently
clamping JPY to zero while allowing the hold benchmark to borrow JPY.

| ETHJPY interval | Hold P&L / max DD | Posterior P&L / max DD | Hold-relative result |
|---|---:|---:|---:|
| 2026-08-03 UTC | -85.3175 JPY / 3.4578% | -60.0231 JPY / 1.9618% | +25.2944 JPY |
| 2026-07-31 UTC, exact 100% ETH start | -300.8958 JPY / 5.6686% | -155.1990 JPY / 2.9212% | +145.6968 JPY |
| 2026-08-05 13:45--00:00 UTC, rising control | +146.4093 JPY / 1.0973% | +72.1176 JPY / 0.5456% | -74.2918 JPY |

The requested falling-path objective passes on two independent ETHJPY days.
The rising control remains profitable and halves drawdown, but gives up part of
the trend.  This is the observed opportunity cost of avoiding early exposure;
it must be monitored rather than hidden by tuning on the rising interval.

Artifacts:

- `/tmp/gamma-eth-2026-08-03-continuation-posterior-v2.json`
- `/tmp/gamma-eth-2026-07-31-continuation-posterior-v2-corrected.json`
- `/tmp/gamma-eth-2026-08-05-rise-continuation-posterior-v2.json`

The earlier `/tmp/gamma-eth-2026-07-31-continuation-posterior-v2.json` used an
infeasible starting base value and must not be cited.

## Live deployment

`config/gammacapture-ethjpy.yaml` enables `continuationMixtureEnabled: true`.
Despite the compatibility name, the implementation now selects the complete
continuation posterior; it does not apply the invalid mixture weight.

At the 2026-08-07 userspace restart, checkpoint warmup restored a healthy model
before two stale orders were cancelled.  The first live posterior forecast was
-35.77 bps with aim 42.82% and a 40.55%--45.08% no-trade band.  Current risky
weight was 46.35%, so the bounded Macro executor sold 0.00074 ETH and then
submitted both a maker BUY and maker SELL.  Account synchronization matched
exchange balances, `fastHealth` was healthy, and no restart/error loop appeared.

The prior binary is preserved as
`bin/bbgo.pre-continuation-posterior-v2-20260807`.

Monitor hold-relative equity, maximum drawdown, continuation up/down/censor
probabilities, forecast mean and variance, target/band, IOC fee share, and
re-entry delay.  Rollback is `continuationMixtureEnabled: false` plus restoring
the preserved binary; the persisted target mode resets once so incompatible
Kalman state is not reused.
