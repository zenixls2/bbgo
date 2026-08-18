# Asymmetric oscillation risk integration and fee-aware replay (2026-08-17)

The standalone alpha is integrated into the GammaCapture quote path as one
risk-aversion multiplier. It does not independently change side gates, quote
distance, quantity, or order lifetime. This keeps the existing Fast model
independent and makes the alpha's only production effect a change in the
inventory-risk penalty supplied to the unified quote optimizer.

## Online path

- `MarketMakerHorizonModel` derives causal bid-path net return, total
  variation, and up/down EWMA variance for the selected Fast horizon.
- `AsymmetricOscillationRiskModel` updates the matured terminal label before
  predicting the next multiplier; a data gap resets the pending label.
- A live decision applies `MacroInventory.RiskAversion *= multiplier` only when
  `enabled` and `shadowOnly` is false.
- The model state is included in the existing checkpoint (version 6), so a
  restart does not silently discard its prequential state.

## Configuration used for ETHJPY

```yaml
asymmetricOscillationRisk:
  enabled: true
  shadowOnly: false
  directionStrength: 0.6
  asymmetryWeight: 0.5
  minMultiplier: 0.65
  maxMultiplier: 1.75
  ewmaAlpha: 0.1
  priorVarianceBps2: 25
  minSamples: 12
```

## Paired production replay with fees

The replay uses the same ETHJPY BBO stream, legacy policy, queue multiplier,
and starting inventory in both arms. Binance's configured maker fee is charged
on every simulated fill. `fullLegacy` disables the alpha; `fullAsymmetricRisk`
enables the multiplier. The replay is diagnostic: the supplied calibration
fill counts are synthetic/zero, so it is not a statistical promotion gate.

| 6-hour window, 50/50 starting inventory | legacy | asymmetric risk |
|---|---:|---:|
| net P&L (JPY) | -8,606.48 | -8,653.37 |
| hold P&L (JPY) | -6,489.60 | -6,489.60 |
| full fills | 26 | 21 |
| fills/hour | 4.384 | 3.541 |
| 1-minute markout (bps) | 0.369 | 1.782 |
| 5-minute markout (bps) | 0.550 | 0.227 |
| 10-minute markout (bps) | -1.373 | -3.725 |
| maker fees (JPY) | 1,951.08 | 1,937.09 |
| maximum drawdown (%) | 1.136 | 1.149 |

The multiplier was ready for 16,420 of 17,080 observations, with mean
`1.114`, minimum `0.65`, and maximum `1.75`. In this window it reduced fills
and did not improve net P&L or maximum drawdown. This is consistent with the
standalone conclusion: the alpha is a tail-risk ordering signal, not a
fee-positive entry/exit signal. It is therefore live as a risk-only component,
but should not be described as a proven return improvement. Longer paired
holdout windows and real fill calibration are still required before tightening
or widening its bounds.

## Verification

```text
GOCACHE=/tmp/bbgo-gocache go test ./pkg/strategy/gammacapture ./cmd/gammacapture-mm-research
```

Both packages pass. The replay binary was rebuilt from the working tree before
the comparison.
