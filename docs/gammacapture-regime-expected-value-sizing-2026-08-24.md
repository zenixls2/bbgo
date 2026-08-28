# GammaCapture regime-conditioned expected-value sizing

Date: 2026-08-24
Primary alpha type: quantity / position sizing
Status: rejected for promotion; replay-only component retained for further data collection

## Hypothesis and null

The hypothesis is that a fee-negative candidate should not be removed by a
binary terminal-value gate. The existing price, horizon, fill model, balance
limits, and inventory hard bounds remain fixed; a causal regime-conditioned
expected-value function determines the fraction of the candidate notional.
The fraction may continuously approach zero.

The null is that this quantity function has no incremental fee-net value over
the current fee-aware policy, and that any apparent improvement is explained
by no-trade behavior, synthetic fill uncertainty, or private-fill
miscalibration.

## Causal contract

- Prediction time: the current BBO decision timestamp.
- Features: completed zero-fee terminal-BBO path moments for the same quoted
  price and horizon, current regime reliability/direction, expected touch
  probabilities, and the current account state.
- Label maturity: only completed terminal paths are included by
  `JointPathPayoffStatistics`; no future path is used at the decision time.
- Integration point: final quantity after the existing quote/joint decision.
  The replay arm may restore the pre-joint base quote only when the ordinary
  terminal-value arm rejects it. It cannot change price, horizon, fill
  probability, hard balance capacity, or inventory bounds.
- Exchange convention: maker fee is charged once per expected touched side;
  adverse selection is charged per touched side; the configured
  `minimumNetEdgeBps` is represented as a half-edge turnover buffer per touch.

## Function

For a full candidate quantity, let `G` be the causal terminal-path expected
value after setting exchange fees, adverse selection, and turnover buffer to
zero. Let `n` be effective path samples, `n0` the prior sample mass, and `rho`
the regime reliability:

\[
  G_r = \rho\frac{n}{n+n_0}G.
\]

With expected touched notional `T`, maker fee `f`, adverse-selection allowance
`a`, and turnover buffer `e`, the full-size net value is:

\[
  A = G_r - T\frac{f+a+e/2}{10,000}.
\]

Uncertainty is charged through a bounded quadratic utility:

\[
  U(k)=kA-k^2R, \qquad
  R=\lambda\frac{(1+w|s|\rho)SE^2}{2W},
\]

where `k` is the quantity scale, `s` is the regime direction score, `SE` is
the causal gross-value standard error, `W` is pair equity, and `w` is the
directional-regime stress weight. The selected scale is:

\[
  k^*=\operatorname{clip}\left(\frac{A}{2R},0,1\right).
\]

Thus fee is part of the objective rather than a separate post-hoc filter. If
the expected value cannot pay fee and turnover, `k*` becomes zero. The venue
minimum-notional check remains an unavoidable final execution constraint.

## Same-symbol paired replay

Command:

```text
go run ./cmd/gammacapture-mm-research \
  --production-compare --regime-expected-value-sizing \
  --symbol ETHJPY --config config/gammacapture-ethjpy.yaml \
  --bbo-data data/gammacapture/ETHJPY \
  --replay-from 2026-08-22T00:00:00Z \
  --replay-to 2026-08-24T00:00:00Z \
  --pair-equity-jpy 6830.672313565 \
  --starting-base 0.01024405 \
  --queue-multiplier 0 \
  --actual-buy-fills 0 --actual-sell-fills 0 \
  --replay-cache-dir data/gammacapture/state/replay-cache \
  --replay-bbo-interval 10s
```

The scoring account is reset identically at the exact replay boundary for
baseline and candidate. The run used 16,907 BBO events, 88,529 aggregate-trade
events, no data gaps, and 47.997 active hours.

| Metric | Fee-aware baseline | Regime expected-value sizing |
|---|---:|---:|
| Quote refreshes | 401 | 0 |
| Full fills | 168 | 0 |
| Round trips | 77 | 0 |
| Maker fees (JPY) | 39.3441 | 0 |
| Net PnL (JPY) | -167.8151 | -84.9232 |
| Hold PnL (JPY) | -84.9232 | -84.9232 |
| Maximum drawdown | 4.1957% | 4.1252% |
| Sizing evaluations | — | 517 |
| Non-zero sizing decisions | — | 0 |
| Mean regime-shrunk gross value (JPY) | — | -0.0813 |
| Mean fee/turnover-adjusted value (JPY) | — | -0.4076 |

The candidate's apparent improvement over the baseline is entirely the result
of not trading. It is not incremental trading alpha. Private-fill calibration
also failed: actual private fills were supplied as 0/0 while the replay
calibration error was 166.

## Decision

`REJECT_NO_INCREMENTAL_VALUE` for this 48-hour window. The proposed structure
is mathematically and causally valid, but the observed gross regime value was
already negative before fees. Consequently, replacing the binary gate with a
continuous size function did not recover a positive trade region.

Do not integrate into live strategy or YAML. A longer same-symbol archive and
non-zero private-fill calibration are required before testing whether any
regime bucket has positive gross value and a stable non-zero size distribution.

## Files and verification

- Pure component: `pkg/strategy/gammacapture/regime_expected_value_sizing.go`
- Focused tests: `pkg/strategy/gammacapture/regime_expected_value_sizing_test.go`
- Replay-only integration: `cmd/gammacapture-mm-research/production_replay.go`
- CLI flag: `cmd/gammacapture-mm-research/main.go`

Verified with:

```text
go test ./pkg/strategy/gammacapture -run 'TestEvaluateRegimeExpectedValueSizing' -count=1
go test ./cmd/gammacapture-mm-research -count=1
```
