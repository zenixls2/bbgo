# GammaCapture pivot price basis audit — 2026-08-25

## Question

Determine whether the current pivot estimate is based on a BBO midpoint or an
executable BBO side, and whether the price basis explains cases where the
predicted pivot time and direction are correct but the resulting inventory
action loses money.

This is a research-only diagnostic. It does not change `strategy.go`, the live
YAML, the service binary, or the quote policy.

## Current implementation

The research pivot implementations use the BBO midpoint:

- `PivotRegimeFilter` receives `ReferencePrice = (bid + ask) / 2`.
- `CausalKlinePivotLearner` builds each Kline's OHLC from the BBO midpoint.
- The raw regime threshold study also normalizes midpoint returns.

The pivot event's stored extreme is therefore a midpoint extreme. It is not an
executable entry or liquidation price.

The current production strategy no longer calls `DynamicInventoryAim` or its
pivot adapter. Its inventory notional and target are still marked with the
midpoint, but execution risk is side-specific:

- BUY entry / fill path uses the ask and liquidation mark uses the future bid.
- SELL entry / fill path uses the bid and reacquisition mark uses the future ask.
- Maker touch distance and quote construction use the actual BBO, not the
  midpoint alone.

Thus the live target mark is a portfolio accounting reference, while the
research pivot is a structural signal. Neither should be interpreted as a
guaranteed executable pivot price.

## Causal price-basis diagnostic

Data: ETHJPY BBO, 2026-08-22 through 2026-08-24, last observed BBO per second,
15-minute non-overlapping anchors. For an upward action the executable return
is `log(futureBid / currentAsk)`; for a downward action it is
`log(currentBid / futureAsk)`. The 26 bps cost floor is not included in the
raw basis comparison.

| diagnostic | result |
|---|---:|
| anchors with non-zero midpoint direction | 123 |
| upward midpoint directions | 74 |
| upward midpoint direction but executable return <= 0 | 7 |
| downward midpoint directions | 49 |
| downward midpoint direction but executable return <= 0 | 7 |
| midpoint direction but executable return <= 0 | 14 / 123 (11.4%) |
| median current+future half-spread basis, upward | 0.026 bps |
| median current+future half-spread basis, downward | 0.026 bps |
| 90th percentile basis, upward | 0.554 bps |
| 90th percentile basis, downward | 0.730 bps |

For a 26 bps first-passage test over the same period, midpoint and executable
barriers were:

| direction | midpoint hits | executable hits | midpoint-only hits |
|---|---:|---:|---:|
| upward | 56 | 54 | 2 |
| downward | 52 | 51 | 1 |

Only 3 of 191 barrier observations were midpoint-only. The midpoint can create
a false executable pivot, but the observed ETHJPY spread is generally too small
to explain the roughly 15–26 bps losses by itself.

## Why the action still loses

The causal 3-minute Kline replay illustrates the distinction. On the recent
holdout, 3-minute predictions had 61.90% directional accuracy, but the
executable action mean was −0.93 bps after the 26 bps cost floor, with a lower
bound of −3.03 bps. The longer frozen holdout was also negative: the 3m and 5m
variants had action means −7.15 and −6.75 bps respectively, despite predictive
directional information.

The mathematical issue is:

```text
correct direction != positive executable cycle value
```

For an upward inventory action, the relevant value is approximately

```text
log(Bid[t+H] / Ask[t]) - fee - adverse-selection - risk penalty
```

not `log(Mid[t+H] / Mid[t])`. A midpoint pivot can be directionally correct
while the excursion is too small, the confirmation arrives too late, or the
spread/adverse move consumes the entire gross return.

There is a second, more important geometric problem in the old pivot actuator:
the pivot filter confirms a reversal only after the midpoint has moved by the
reversal threshold from the running extreme. At confirmation, the strategy is
already reacting to a completed reversal, then estimates remaining amplitude
from completed midpoint legs. That remaining-amplitude estimate does not model
entry-side price, exit-side price, pivot confirmation delay, or the probability
that the remaining excursion is large enough to clear the full cycle cost.

## Conclusion

The answer is:

1. Current research pivot calculation uses midpoint (`BBO / 2`), not a single
   BBO side.
2. This is a genuine price-basis mismatch for an executable inventory action,
   but it is a secondary loss source for ETHJPY at the measured spread.
3. The dominant failure is amplitude/value calibration: a correct pivot
   direction and timing do not imply a positive executable, fee-net cycle.
4. The pivot threshold and midpoint leg amplitude must not be promoted as a
   direct quote or inventory actuator. The existing Kline pivot screen remains
   rejected for fee-net action value.

## Next research contract

Keep midpoint only as a neutral structural feature, then compare two
side-specific variants in a standalone causal replay:

- upward/BUY pivot geometry from ask-side bars, with future bid liquidation;
- downward/SELL pivot geometry from bid-side bars, with future ask reacquisition.

Fit remaining amplitude and confirmation delay separately by side. Score a
continuous target shift only when its lower fee-net value bound is positive:

```text
EV_buy  = p_buy * E[log(Bid_T / Ask_0)] - fees - adverse - risk
EV_sell = p_sell * E[log(Bid_0 / Ask_T)] - fees - adverse - risk
```

The candidate must beat the current posterior-inventory target on the same
starting inventory, use private-fill calibration, and pass positive holdout
lower bounds before any component replay or production integration.
