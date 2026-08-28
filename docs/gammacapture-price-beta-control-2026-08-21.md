# GammaCapture price-beta control

## Objective

The existing ETHJPY maker path should retain positive risk-adjusted return and
low drawdown while reducing mechanical exposure to ETHJPY price. The existing
Relative-Hold scalar was not sufficient for this objective: it penalized
tracking error and downside beta, but its gross-order penalty did not change a
paired replay path.

For spot inventory (q_t), mid-price (S_t), and pair equity (W_t), the
first-order marked price beta is approximately

\[
\beta^{price}_t \simeq \frac{q_t S_t}{W_t}.
\]

The new research control caps the target of the existing
`DynamicInventoryAim`; it does not create a second side gate, price multiplier,
or independent order admission rule. `priceBetaTarget=0` is the null and
preserves the previous behavior exactly.

The Relative-Hold model was also extended with causal all-label bivariate EWMA
moments for strategy-vs-Hold beta. The total-beta utility remains opt-in and
is not the primary production control because a gross scalar cannot reliably
distinguish a de-risking SELL from an inventory-increasing BUY. Checkpoint
version 2 persists the additional sufficient statistics.

## Paired replay

The corrected production replay used the same ETHJPY archive, account, queue
multiplier, preload boundary, and 1-second compacted BBO stream:

- scored interval: `2026-08-20T10:49:49Z`–`2026-08-20T16:10:00Z`;
- pair equity: `7437.81601817 JPY`;
- starting base: `0.0205444 ETH`;
- queue multiplier: `0.25`;
- metrics: non-overlapping one-hour blocks, with Sharpe annualized from that
  block clock.

| price-beta target | net P&L JPY | max DD | annualized Sharpe | Hold correlation | Hold beta | mean risky weight |
|---:|---:|---:|---:|---:|---:|---:|
| null | 92.54 | 1.450% | 26.27 | 0.9883 | 0.539 | 0.536 |
| 0.35 | 94.50 | 1.120% | 32.37 | 0.9968 | 0.444 | 0.473 |
| 0.30 | 55.34 | 0.916% | 25.32 | 0.9872 | 0.342 | 0.337 |
| 0.20 | 43.20 | 0.615% | 27.93 | 0.9658 | 0.242 | 0.238 |

The `0.35` arm is the best short-window P&L/DD trade-off, but it does not
lower sample correlation. The `0.20` arm lowers correlation materially but
loses too much P&L. These are only five matured blocks and use a synthetic
public-BBO queue model, so none is production promotion evidence.

## Decision

The implementation and replay tooling are retained as a removable research
candidate. The live ETHJPY YAML remains unchanged and does not enable
`priceBetaTarget`. A longer untouched same-symbol walk-forward replay with
private-fill calibration is required before selecting a target or changing the
userspace service.

Focused tests passed:

```text
go test ./pkg/strategy/gammacapture ./cmd/gammacapture-mm-research
```
