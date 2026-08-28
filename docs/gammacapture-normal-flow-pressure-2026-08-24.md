# GammaCapture ordinary-flow blind spot and replay repair

Date: 2026-08-24 UTC
Symbol: ETHJPY
Status: implemented as a gated research candidate; live YAML remains disabled

## Data and causal boundary

The complete UTC days 2026-08-22 and 2026-08-23 were loaded from the local
capture archive:

- `ETHJPY-bookticker-2026-08-22.csv`: 1,110,441 BBO rows;
- `ETHJPY-bookticker-2026-08-23.csv`: 843,559 BBO rows;
- captured trades: 43,491 and 45,047 rows respectively;
- Binance aggregate-trade downloads: 43,490 and 45,047 normalized rows; and
- Binance 1-minute klines: 1,440 rows per day, retained as a diagnostic
  cross-check rather than as the execution price source.

The production replay used the captured BBO and public trades, a one-second
BBO bucket, a 6h30m causal warmup beginning at 2026-08-21 17:30 UTC, and the
scored interval 2026-08-22 00:00 UTC through 2026-08-24 00:00 UTC. It processed
115,807 compacted BBO events and 88,537 trade events with zero data gaps.
The queue multiplier was fixed at zero, matching the prior private-calibration
replay convention. No private fills were available for these two days, so the
fill calibration gate is deliberately false.

## Blind spot

`VolumeBalance.Signal` is non-zero only in its high-volume shock/rebalancing
state. On the recent diagnostic sample, the ordinary signed public flow was
directional even though that shock state was inactive. A 5-minute signed trade
imbalance split produced approximately:

| Condition | Anchors | Mean next 15m bid return |
|---|---:|---:|
| imbalance >= 0.05 | 87 | +2.62 bps |
| imbalance <= -0.05 | 78 | -5.98 bps |
| difference | | +8.60 bps |

This is a research observation, not an executable fill claim; the production
replay below is the execution test.

## Repair

The candidate adds an auxiliary fallback only when the existing shock signal is
zero. With (I_t) equal to the signed 5-minute trade imbalance and (n_t)
the observed trade count, it uses

\[
 w_t = \frac{n_t}{n_t+n_0}, \qquad
 s_t = \operatorname{clip}(w_t I_t,-w_{max},w_{max}).
\]

The current research defaults are (n_0=20), (n_t\ge20),
\(|I_t|\ge0.10), and (w_{max}=0.35). The signal is an auxiliary input to
the existing joint quote model: it is not a side admission gate, inventory
target, or second quantity controller. Existing OFI/volume disagreement
suppression still overrides it.

The live strategy and the production replay now use the same pure evaluator.
This also fixed an engineering gap discovered during testing: the replay had
been rebuilding `VolumeSignal` independently and initially omitted the new
fallback, producing identical baseline and candidate arms. The replay now
injects the fallback at the same single integration point as live code.

## Paired production replay

Both arms used the same BBO/trade stream, warmup, account, queue model, fees,
and legacy replay policy. The candidate differed only by enabling the bounded
ordinary-flow fallback in memory.

| Metric | Baseline | Normal-flow candidate | Change |
|---|---:|---:|---:|
| Net PnL (JPY) | -156.08 | -146.77 | +9.31 |
| Hold PnL (JPY) | -58.00 | -96.39 | -38.39 |
| Excess vs hold (JPY) | -98.07 | -50.37 | +47.70 |
| Maximum drawdown | 3.900% | 4.033% | +0.133 pp |
| Full fills | 181 | 194 | +13 |
| Round trips | 83 | 90 | +7 |
| Maker fees (JPY) | 44.68 | 37.26 | -7.42 |
| 1m markout | -2.58 bps | -6.28 bps | -3.70 bps |
| 5m markout | +0.09 bps | -2.47 bps | -2.56 bps |
| 10m markout | +0.81 bps | -1.72 bps | -2.53 bps |

The candidate improves raw PnL and excess versus the model's hold reference,
but it remains loss-making, increases maximum drawdown, and makes markout more
adverse at every measured horizon. The replay cannot establish positive
Sharpe or private-queue robustness because the two-day private-fill calibration
is unavailable; therefore the promotion decision is:

**Do not enable in live YAML.**

This is a successful engineering repair of the silent-flow path and a failed
alpha-promotion attempt. The candidate remains available for further screening
with private fills and a longer untouched holdout. The conservative fallback is
configured in `config/gammacapture-ethjpy.yaml` with `enabled: false`.

## Verification

Focused unit tests cover shrinkage, hard bounds, insufficient evidence, and sign
symmetry. The GammaCapture strategy and production-replay packages pass:

```text
go test ./pkg/strategy/gammacapture ./cmd/gammacapture-mm-research -count=1
```
