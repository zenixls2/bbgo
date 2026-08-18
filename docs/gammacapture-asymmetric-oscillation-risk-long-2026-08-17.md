# Long-horizon asymmetric oscillation risk replay (2026-08-17)

This is a same-symbol standalone replay of the isolated risk alpha. It uses
ETHJPY BBO data from `2026-07-23 13:30 UTC` through `2026-08-17 07:25 UTC`.
Each sample uses the last BBO in a 5-minute bucket, a 15-minute causal past
path, and the next 15-minute executable-bid terminal/minimum markout. Samples
are non-overlapping at 15 minutes; no orders, inventory feedback, or future
labels enter the feature.

| state | effective n | mean multiplier | terminal bid return | minimum bid markout | downside semivariance |
|---|---:|---:|---:|---:|---:|
| oscillating upward | 768 | 0.861 | -0.179 bps | -5.078 bps | 161.42 bps² |
| oscillating downward | 743 | 1.167 | -1.230 bps | -7.061 bps | 305.51 bps² |

The model produces the requested ordering: upward oscillations receive a lower
risk-aversion multiplier, while downward oscillations receive a higher one.
The high-multiplier group has `287.32 bps²` downside semivariance versus
`167.94 bps²` for the low-multiplier group.

Across 99 six-hour paired blocks, down-minus-up downside semivariance is
`115.50 bps²`, standard error `51.00 bps²`, t-statistic `2.26`, and 95%
interval `[15.54, 215.46] bps²`. The terminal-return and minimum-markout
differences are not individually significant at the same block level; this is
evidence for a risk ordering, not a directional-return forecast.

The alpha therefore passes its standalone risk-ordering check but remains
`component-only`. A fee-net order/inventory component replay is still required
before connecting the multiplier to the production HJB/quote optimizer. No
live YAML, `strategy.go`, binary, or service was changed.

## Fee-net check

Using Binance's configured `10 bps` maker fee on entry and `10 bps` on exit,
the exact executable cycle was evaluated as

\[
10000\log\left(\frac{Bid_{t+15m}(1-0.001)}{Ask_t(1+0.001)}\right).
\]

Across 2,379 effective samples, the passive-hold terminal bid return averaged
`-0.146 bps`, while the fee-net cycle averaged `-20.225 bps`; only `8.53%` of
cycles were positive. The upward-oscillation and downward-oscillation cycle
means were `-20.259 bps` and `-21.365 bps`, respectively. The paired six-hour
down-minus-up fee-net difference was `-0.120 bps` with 95% interval
`[-1.910, 1.669] bps`.

Therefore this alpha is useful as a risk-state multiplier, but it is not itself
a fee-positive trading signal. The fee test is hold-dominant and does not
authorize order submission.
