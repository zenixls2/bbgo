# GammaCapture online HAR variance-risk study

Date: 2026-08-07. Status: research-only; live ETHJPY configuration remains
disabled (`fastVarianceRiskEnabled: false` by omission/default).

## Question and model boundary

The study asked whether mixed-frequency, side-specific variance forecasts can
stop Macro inventory from re-entering too early during a falling-market
consolidation. BUY risk is estimated from executable ask log returns and SELL
risk from executable bid log returns. The online HAR model has no pretrained
artifact and predicts the next 15m/30m integrated variance from 15m, 30m and
long-window variance rates, downside semivariance share, and bipower-variation
jump share.

The Daniel--Moskowitz momentum-crash result supports state-dependent risk
scaling after market declines and in high-volatility states. It does not imply
that volatility forecasts identify the next price direction. Local ETHJPY
tests therefore kept expected return exclusively in the signed first-passage
crossing posterior and allowed HAR to enter only the risk denominator.

For base crossing variance `V_QV`, HAR variance `V_HAR`, directional expected
return `mu_QV`, strategic prior `w0`, risk aversion `gamma`, and prior strength
`lambda`, the tested controller was

```text
w* = (mu_QV + lambda*w0)
     / (gamma*max(V_QV, V_HAR) + lambda).
```

HAR never multiplies `mu_QV`; doing so would manufacture expected return from
risk. BUY/SELL HAR forecasts remain separate until their conservative maximum
enters this single Merton/no-trade controller.

## Standalone variance result

ETHJPY BBO, 2026-07-23 through 2026-08-08, non-overlapping causal labels:

| horizon | side | QLIKE skill | paired t | improved days | result |
|---|---|---:|---:|---:|---|
| 15m | ask/BUY | +24.49% | 3.55 | 15/15 | pass |
| 15m | bid/SELL | +24.23% | 3.59 | 15/15 | pass |
| 30m | ask/BUY | +16.78% | 2.96 | 14/14 | pass |
| 30m | bid/SELL | +16.56% | 3.07 | 14/14 | pass |
| 60m | both | about +10.6% | one side below 1.645 | -- | reject |
| 180m | both | about +18.6% | below 1.645 | -- | reject |

QLIKE is the relevant risk loss because it strongly penalizes variance
underprediction. Log-MSE was worse than the random-walk baseline, so HAR is not
a general-purpose level predictor and must not become a directional signal.

## Integration defects and ablations

The untouched bearish replay uses 2026-08-03 UTC, 6,808 JPY starting equity,
0.022940981226 ETH (100% risky), queue multiplier 1, next-BBO execution, and the
same maker/IOC simulator. QV-only produced -87.077043 JPY and 2.180158% maximum
drawdown.

| revision | integration | P&L JPY | max DD | conclusion |
|---|---|---:|---:|---|
| v1 | HAR risk also inflated direction measurement variance | -88.581098 | 2.203454% | reject; Kalman gain fell when response should accelerate |
| v2 | direction Jacobian uses base QV variance; full target still filtered | -87.070469 | 2.178510% | statistically correct, economically negligible (+0.0066 JPY) |
| v3 | filter base-QV target, apply HAR denominator immediately | -87.556624 | 2.189174% | reject; risk release caused re-entry churn |

The v1 bug is visible at 01:00 UTC: HAR raw aim was lower but its filtered aim
rose to 0.52664, versus QV-only 0.48308. Correcting the Jacobian in v2 lowered
the HAR filtered aim to 0.47911. In the 00:00--03:00 stress slice, v3 improved
P&L by 0.2933 JPY and maximum drawdown by 0.00516 percentage point, but the
full-day result reversed. v3 generated 9 maker BUY fills versus 5 for QV-only
and 35 Macro IOC fills versus 30. Immediate two-way release of the HAR
denominator therefore increased turnover and premature re-entry.

Artifacts:

- `/tmp/gamma-eth-2026-08-03-fast-variance-v1-vs-qv.json`
- `/tmp/gamma-eth-2026-08-03-fast-variance-v2-vs-qv.json`
- `/tmp/gamma-eth-2026-08-03-0000-0300-fast-variance-v3-vs-qv.json`
- `/tmp/gamma-eth-2026-08-03-fast-variance-v3-vs-qv.json`

## Can posterior hysteresis fix release churn?

A statistically defined three-state rule was evaluated before implementation:

```text
enter elevated risk: both executable sides have 95% lower log-ratio bound > 0
retain state:         posterior interval crosses zero
exit elevated risk:  both executable sides have 95% upper log-ratio bound < 0
```

Across all 15m anchors there were 58 joint elevated, 131 joint de-escalated,
and 1,168 ambiguous observations. However, on the complete 2026-08-03 bearish
day all 96 joint 15m anchors were ambiguous: zero elevated and zero
de-escalated. At 30m there were zero elevated, four de-escalated and 44
ambiguous anchors. A Bayesian HAR lease would therefore never enter on the
target failure day. Implementing it would be statistically indefensible.

Coverage artifacts:

- `/tmp/gamma-eth-har-risk-coverage-15m.json`
- `/tmp/gamma-eth-har-risk-coverage-30m.json`

## Decision and next blocker

HAR has real variance-forecasting skill but no demonstrated ability to identify
downtrend continuation in this local sample. It remains available only behind
the disabled research flag and must not be enabled live or checkpointed.

The remaining problem is a stopping-time problem: estimate the probability of
another downside first passage before a recovery barrier while the price is
consolidating below a recent high. A new model must be scored on that exact
event, separately from variance, before order/Macro integration. The current
archive contains too few independent severe drawdown episodes to fit a rare-
event continuation model without either additional history/cross-symbol data
or a distribution-free first-passage construction. No directional coefficient
should be inferred from HAR QLIKE skill.

## Primary references

- Corsi, *A Simple Approximate Long-Memory Model of Realized Volatility*,
  Journal of Financial Econometrics (HAR-RV), DOI 10.1093/jjfinec/nbp001.
- Barndorff-Nielsen and Shephard, *Power and Bipower Variation with Stochastic
  Volatility and Jumps* (bipower variation / jump-robust QV).
- Daniel and Moskowitz, *Momentum Crashes*, Journal of Financial Economics,
  2016, <https://www.kentdaniel.net/papers/published/jfe_16.pdf>.
- Cont, Kukanov and Stoikov, *The Price Impact of Order Book Events*,
  <https://arxiv.org/abs/1011.6402> (order-flow evidence is distinct from
  volatility and cannot be counted as an independent copy of the same signal).
