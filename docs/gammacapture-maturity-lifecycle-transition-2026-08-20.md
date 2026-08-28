# GammaCapture: path maturity, transition fallback, and horizon utility

## Scope

This change implements only items 2, 4, and 6 from the execution-model review.
The SELL minimum invariant, Bellman quote lease, and side-separated action-value
quantity controller (items 1, 3, and 5) are intentionally not changed because
they can increase turnover before a same-symbol holdout establishes a benefit.

## 2. Joint-path maturity

The old `EffectiveSamples <= 1` check was only a non-degeneracy check. It did
not distinguish a precise negative terminal-wealth estimate from a two-to-three
effective-sample estimate with a very wide confidence interval. Conversely, a
fixed six-sample threshold is not causal or reachable for overlapping 10/15/30
minute paths in a six-hour lookback.

`AssessJointPathMaturity` now uses the current weighted effective sample mass
and the largest side/target weighted variance. With (z=1.645),

\[
 h_t=z\sqrt{\max_j\{\widehat{\sigma}^2_j\}/N_{\rm eff}},\qquad
 r_t={h_t\over\max(c,|\widehat\mu_j|)},
\]

where (c=\text{maker fee}+\text{adverse selection}+\text{minimum edge})
and the maximum also includes a 1-bps numerical floor. A path is mature only
when (N_{\rm eff}>1), all moments are finite, and
`r_t <= pathMaturityMaxRelativeHalfWidth` (live value `1`).

An immature path returns `terminal path evidence is immature` and is
inconclusive; it does not trigger the authoritative no-order rejection. A
mature path may still return negative terminal certainty equivalent, which is
an economic rejection and remains authoritative. No raw six-sample gate was
added.

## 4. Multi-scale transition posterior

The existing one-minute Bayesian run-length model is now production-capable,
checkpointable, and duplicate-callback safe. Repeated BBO callbacks within the
same UTC minute are idempotent; a missing minute resets the causal segment.
It uses executable bid/ask returns, bipower variation, and a truncated
Normal-Inverse-Gamma predictive posterior.

Integration is deliberately one-way and precedence-ordered:

1. A ready, strictly-prequential BOCPD45 posterior remains authoritative.
2. The multi-scale posterior can supply a bounded direction fallback only when
   BOCPD45 is not ready.
3. It never owns quantity, price distance, a second admission gate, or Macro
   target selection.

The ETHJPY live YAML keeps this fallback disabled until a new same-symbol
component replay shows non-zero action diversity and positive holdout evidence.
This is consistent with the earlier rejection of price-only multi-scale
three-hour directional alpha; the implemented fallback is not a claim that
model predicts a long-horizon first-passage sign.

## 6. Lifecycle-aware horizon objective

Horizon selection now compares one objective rather than crossing score and
path utility separately:

\[
 U_H=S_H-z\,SE(S_H)-{C_{\rm replace}\over H}.
\]

`S_H` is the existing fee-net crossing/path score. The uncertainty term is a
confidence penalty. `C_replace` is the configured queue/opportunity replacement
cost from `QuoteLifecycleAction`; it contains no maker fee, so fees are not
double-counted. The result is exposed as
`SelectionScoreBpsPerHour`, while raw score and both penalties are logged
separately. The helper is applied once at the horizon-selector comparison point;
it is not duplicated in price, quantity, or lifecycle gates.

## Verification

Focused GammaCapture and research-runner tests pass. The bounded same-interval
component replay is recorded below; no service restart was performed by this
change.

## Adaptive decay factor and online (N_{\rm eff}) (2026-08-20)

The scale-only path weight

\[
 w_i=\exp\left(-\log(2)\,{\operatorname{age}_i\over\sqrt{HL}}\right)
\]

was replaced by a causal, data-derived time decay. After a horizon path has
matured, the model observes the absolute executable-BBO window return

\[
 x_i=\max\left(\left|10^4\log(Ask_{i,H}/Ask_{i,0})\right|,
                 \left|10^4\log(Bid_{i,H}/Bid_{i,0})\right|\right),
\]

and updates a lag-one persistence estimate with Welford sufficient statistics.
For the overlapping path renewal clock, the observed spacing is floored at
\(H\). The continuous half-life is then

\[
 h_t=\operatorname{clip}\left(-{\log(2)\,\max(H,\bar\Delta_t)
 \over \log(\hat\rho_t)},\;H,\;L\right),
 \qquad
 \phi_t(\Delta)=\exp\left(-\log(2){\Delta\over h_t}\right),
\]

where \(\hat\rho_t\) is the non-negative lag-one correlation of \(x_i\). A
cold start, zero persistence, or fewer than eight lagged pairs uses the
existing \(\sqrt{HL}\) value only as a temporary prior. This is not a new
symbol-specific fitted constant, and an outage resets the lag pair without
discarding matured statistics.

The weighted path count remains the authoritative current information mass:

\[
 N_{\rm eff,t}=\min\left(\sum_iw_i,
 { (\sum_iw_i)^2\over\sum_iw_i^2}\right).
\]

For stability diagnostics, the model also maintains an online EWMA
\(\bar N_t\) of already matured \(N_{\rm eff}\) values using the same
time-scaled \(\phi_t\). It is not a second hard gate. If current information
mass is below its own causal baseline, maturity uncertainty receives the
continuous surcharge

\[
 z\hat\sigma_t\left({1\over\sqrt{N_t}}-
 {1\over\sqrt{\bar N_t}}\right)_+,
\]

while \(N_t\) itself remains in the standard confidence width. The state is
checkpointed per 10m/15m/30m horizon; checkpoint version 10 intentionally
invalidates older state so startup replay cannot mix incompatible decay rules.

### Same-interval ETHJPY paired replay

The exact previous interval was replayed with the same 30-second BBO bucket,
cache, fees, balances, and queue setting: 2026-08-19 09:00:29Z through
2026-08-20 01:50:47Z (16.8466h, 2,010 BBO events, 55,307 public trades,
zero data gaps). The fixed-decay arm was run with the research-only
`--disable-adaptive-path-decay` override; the adaptive arm used the live YAML
`jointDistanceQuantity.adaptivePathDecay: true`.

| arm | net PnL (JPY) | hold (JPY) | excess (JPY) | fills (BUY/SELL) | round trips | maker fees (JPY) | max DD |
|---|---:|---:|---:|---:|---:|---:|---:|
| fixed \(\sqrt{HL}\) | 552.9530 | 548.6406 | +4.3124 | 89 (49/40) | 40 | 39.9012 | 2.1910% |
| adaptive persistence + Neff EWMA | 581.9575 | 548.6406 | +33.3169 | 80 (41/39) | 39 | 45.6418 | 2.1322% |

Mean path \(N_{\rm eff}\) rose from 3.315/2.630/1.721 to
8.086/5.899/2.971 for 10m/15m/30m. The corresponding mean adaptive
half-lives were about 4.77h/6h/6h, with lag correlations
0.978/0.985/0.994. Horizon scores themselves did not
change; the adaptive arm admitted more mature path estimates and changed
quote lifecycle decisions. This is a positive paired result on this interval,
not a promotion claim: the adaptive factor still requires untouched same-symbol
walk-forward blocks and block-bootstrap uncertainty before a production restart.
