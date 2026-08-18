# Asymmetric oscillation inventory risk (2026-08-17)

## Literature finding

The requested behavior has two separate theoretical bases, and they should not
be conflated:

1. Asymmetric-volatility models such as Nelson's EGARCH and Engle--Ng's news
   impact curve allow a negative return shock to change the next conditional
   variance more than a positive shock. Bekaert--Wu show that the return/
   volatility asymmetry also changes the risk premium and conditional
   covariance, so a market maker should not use one fixed inventory-risk
   coefficient in every path.
2. Guéant--Lehalle--Fernandez-Tapia solve the inventory-constrained HJB model
   with risk aversion and volatility explicitly in the quote policy. Fodra--
   Labadie extend this to a non-martingale reference price and directional bets:
   the inventory-risk-aversion parameter controls the PnL variance, skewness,
   kurtosis, and VaR trade-off while quotes become non-symmetric under a trend.

Crypto is not guaranteed to have the equity sign. A Bitcoin study reports
time-varying and sometimes *inverted* asymmetry, where positive returns raise
conditional volatility more than negative returns. Therefore ETHJPY must
estimate the sign online rather than permanently assuming an equity leverage
effect.

References: [Nelson (1991), EGARCH](https://web.pdx.edu/~crkl/readings/Nelson91.pdf),
[Engle--Ng (1993), news-impact curve](https://onlinelibrary.wiley.com/doi/10.1111/j.1540-6261.1993.tb05127.x),
[Bekaert--Wu (2000), asymmetric volatility and risk](https://academic.oup.com/rfs/article/13/1/1/1584172),
[Guéant--Lehalle--Fernandez-Tapia (2011), inventory-risk HJB](https://arxiv.org/abs/1105.3115),
[Fodra--Labadie (2012), directional market making with inventory risk](https://arxiv.org/abs/1206.4810),
and [time-varying Bitcoin asymmetry](https://pmc.ncbi.nlm.nih.gov/articles/PMC7850481/).

## Isolated model

For a selected Fast window, let \(R_t\) be the signed executable-BBO endpoint
return and \(TV_t=\sum_i|\Delta r_i|\) its total variation. The oscillation
score is

\[
O_t=1-\frac{|R_t|}{\max(TV_t,|R_t|)},\qquad
D_t=\tanh(R_t/s_t),\qquad S_t=O_tD_t.
\]

The online label is the future executable-bid terminal return. Matured labels
update separate EWMA second moments \(\hat\sigma^2_{+,t}\) and
\(\hat\sigma^2_{-,t}\), conditioned on the sign of \(S_t\). The estimated
asymmetry is

\[
A_t=\frac{\hat\sigma^2_{-,t}-\hat\sigma^2_{+,t}}
          {\hat\sigma^2_{-,t}+\hat\sigma^2_{+,t}},
\]

and the only promoted downstream input is a bounded risk-aversion multiplier:

\[
\frac{\lambda_t}{\lambda_0}
=\operatorname{clip}\left(
\exp\{-\eta S_t(1+wA_t)\},m_{\min},m_{\max}\right).
\]

With the usual downside-dominant asymmetry, \(S_t>0\) (oscillating upward)
lowers \(\lambda_t\), while \(S_t<0\) (oscillating downward) raises it. If the
online estimate is inverted, the response is attenuated and the sign remains
visible in diagnostics; it is not allowed to silently reverse the base path
ordering.

## Same-symbol causal study

The ETHJPY BBO archive was sampled at the last observation in each 5-minute
UTC bucket. A 15-minute past path produced the score and the next 15-minute
executable bid supplied the label. Effective samples use a non-overlapping
15-minute stride; no order, inventory feedback, or future BBO was used in the
feature.

| state | effective n | terminal bid return | mean minimum bid markout | downside semivariance |
|---|---:|---:|---:|---:|
| oscillating upward | 523 | -0.029 bps | -4.441 bps | 127.31 bps² |
| oscillating downward | 478 | -1.415 bps | -6.374 bps | 226.14 bps² |

Across 65 six-hour paired blocks, down-minus-up downside semivariance was
`86.87 bps²` with standard error `47.88 bps²` and 95% interval
`[-6.97, 180.72] bps²`. The ordering is economically meaningful but not yet a
fee-net PnL result; the interval crosses zero. The screening manifest therefore
returns `INCONCLUSIVE_UNCERTAINTY` and permits only component replay, not live
integration.

The isolated implementation and tests are in
`pkg/strategy/gammacapture/asymmetric_oscillation_risk.go` and
`asymmetric_oscillation_risk_test.go`. It does not modify `strategy.go`, live
YAML, or systemd units.
