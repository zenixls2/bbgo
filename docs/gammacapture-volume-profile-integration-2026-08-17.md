# GammaCapture Volume Profile integration (2026-08-17)

## Scope

The existing `RollingVolumeProfile` component is now connected to the live
Fast terminal-payoff path as an optional, causal conditioning feature. It is
not a second quote controller, quantity multiplier, inventory gate, or Macro
replacement.

For each configured Fast horizon (currently 10m, 15m, and 30m), the model keeps
a bounded exponentially decayed public-trade profile. A BBO observation stores
the current profile snapshot in the corresponding causal horizon point. The
conditional execution kernel then adds the side-reflected POC, local density,
flow, centroid, and corridor-distance differences only when both the current
state and a matured historical state have enough effective trades:

\[
K(x,x')=\exp\left[-\frac12\left(\|z_4(x)-z_4(x')\|^2
 +\omega_V\|v_5(x)-v_5(x')\|^2\right)\right].
\]

If either profile is not ready, the kernel is exactly the previous four-feature
kernel. This preserves the old estimator during warmup and makes the new
component one-dimensional and reversible.

## Causality and boundedness

- Public trades update the profile only through `ObservePublicTrade`.
- A trade never rewrites an already observed BBO point; its state appears on a
  later BBO observation.
- Each profile uses lazy exponential decay, a hard bin cap, and O(1) trade
  ingestion. Snapshot work is bounded by the bin cap and occurs on the model
  clock, not on every trade.
- A capture gap resets the profiles so an outage is not interpreted as a
  continuous price path.
- At the time of the original integration the production YAML was disabled
  (`enabled: false`). It was later explicitly enabled for the ETHJPY live
  profile; the new asymmetric POC-risk flag remains opt-in and is not enabled
  in that live YAML.

## Verification

Unit tests cover side reflection, bounded/decaying bins, causal snapshot
propagation, kernel changes when both profiles are ready, and exact legacy
behavior when either profile is unavailable.

The ETHJPY production-state replay was run on the captured 2026-08-17
00:00--03:00 UTC interval with identical balances, queue multiplier, fees, and
exchange simulator. The VP candidate reached 168,367 ready profile-window
observations out of 179,475 (10m/15m/30m combined). The shorter first hour was
unchanged; the three-hour replay is long enough for the profile-weighted path
distribution to change the selected quote path:

| policy | fills | buy/sell | net P&L (JPY) | hold P&L (JPY) | 10m markout (bps) | max DD |
|---|---:|---:|---:|---:|---:|---:|
| asymmetric baseline | 4 | 3 / 1 | -677.15 | 6,112.00 | 11.35 | 19.25% |
| VP-conditioned candidate | 8 | 4 / 4 | 1,687.55 | 6,112.00 | 6.10 | 22.57% |

The candidate improves fee-adjusted P&L and markout relative to the asymmetric
baseline, but it still trails buy-and-hold on this rising interval and has a
larger maximum drawdown. This is evidence of useful conditioning, not a live
promotion criterion.

## Asymmetric POC-side risk screening

The follow-up candidate treats a POC above and below the current price as
different side risks. Let \(d=P_t-POC\), \(\rho\) be local profile density,
\(\sigma_V\) the profile scale, and \(p\) the adverse local-flow strength.
For BUY, adverse risk is present only when \(d<0\) and local flow is seller
dominated; for SELL it is present only when \(d>0\) and flow is buyer
dominated. With

\[
L=\min(|d|,\sigma_V)\rho,
\qquad E[\ell]=pL,
\qquad \operatorname{Var}(\ell)=p(1-p)L^2,
\]

the existing terminal-payoff side mean and variance are adjusted continuously.
The adjustment is not a hard gate and leaves crossing, price, and inventory
target estimators in their existing owners.

On the same ETHJPY 00:00--03:00 UTC replay, the variance-aware POC candidate
was worse as a promotion candidate:

| policy | fills | buy/sell | net P&L (JPY) | max DD (reported) | 10m markout (bps) |
|---|---:|---:|---:|---:|---:|
| VP-conditioned live policy | 7 | 4 / 3 | 1,688.07 | 0.2257031869% | 2.35 |
| VP + asymmetric POC risk | 8 | 4 / 4 | 1,687.56 | 0.2257031870% | 6.19 |

The reported drawdown reduction is numerically negligible while fee-adjusted
P&L, fills, and markout deteriorate. Therefore `asymmetricPOCRisk` is
implemented as a replay-only opt-in and remains **false in live configuration**.
The result rejects this first POC-risk form rather than silently promoting a
drawdown claim that the data does not support.

## Production replay warmup correction

The original production comparison loaded market data only from the evaluation
start and calculated `warmFrom` after loading. Filtering could not recover the
missing pre-start rows, so the first large orders reported zero joint path
samples and fell through to the ordinary notional fallback.

The corrected replay now loads the longest causal history plus the longest
path maturity before the earliest calibration/evaluation start. For the current
ETHJPY profile this is 6h lookback + 30m path maturity = 6h30m. If two-stage
continuation is enabled, the maturity term automatically includes the second
Fast horizon. Warmup BBO is compacted to one close per second, public trades
remain exact, and every BBO at or after the trading/calibration start remains
tick-exact. No orders or fills are allowed during warmup.

The corrected 2026-08-17 00:00--03:00 UTC replay loaded 7,925 warmup BBO rows
and 1,880 warmup public trades. Path state was ready for 35/36 evaluations at
10m and 15m, and 30/36 at 30m. The previous roughly 500k JPY fallback BUYs
disappeared; all fills were approximately 100--200 JPY.

| policy | fills | buy/sell | net P&L (JPY) | hold P&L (JPY) | 10m markout (bps) | max DD |
|---|---:|---:|---:|---:|---:|---:|
| VP-conditioned live policy | 8 | 4 / 4 | 6,113.14 | 6,112.00 | 3.36 | 0.20524% |
| VP + asymmetric POC risk | 6 | 3 / 3 | 6,112.67 | 6,112.00 | 2.97 | 0.20524% |

The warmup fix, rather than the POC penalty, removed the pathological inventory
overshoot and improved the live-policy result by about 4,425 JPY versus the
same three-hour replay without historical warmup. The POC-risk candidate still
does not improve drawdown materially and remains disabled.
