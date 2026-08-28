# Relative-Hold risk component (screening stage)

## Hypothesis and null

The Fast joint action should be evaluated relative to a same-symbol Hold
baseline over a fixed one-hour label horizon. A positive fee-net excess return
should increase the scalar action utility, while tracking error and downside
beta above one should reduce it. The null is that the relative-Hold state adds
no incremental value after the existing whole-position terminal-wealth model.

This is an inventory-risk component, not a new direction, price, quantity, or
side-admission gate. It must be added at most once to the unified Fast
price/quantity objective after component replay.

## Causal contract

- `DecisionAt` is the final information cutoff for an action.
- `MaturedAt` must be at least `DecisionAt + Horizon`; labels arriving earlier
  are rejected.
- `StrategyReturn` and `HoldReturn` are same-symbol fee-net log returns using
  the next-observable executable BBO convention. A midpoint label is invalid
  for this component.
- Duplicate and out-of-order maturity timestamps are rejected.
- EWMA decay is computed from the actual elapsed maturity interval and the
  configured half-life. A long gap decays moments; the bounded CVaR shadow tail
  is cleared so stale losses cannot survive a reconnect.
- Overlapping labels are represented by effective sample size from the EWMA
  weights, not raw label count.

## Estimators

For each matured block, let

\[
e_t=r^{\rm strat}_t-r^{\rm hold}_t,
\qquad
\alpha_t=1-\exp\!\left(-\log 2\,{\Delta t\over h_{1/2}}\right).
\]

The implementation keeps exponentially weighted sufficient statistics for
`e_t`. The variance is shrunk toward the configured prior variance with

\[
\rho={N_{\rm eff}\over N_{\rm eff}+N_{\rm prior}}.
\]

For downside beta, only `HoldReturn < 0` labels update the bivariate moments:

\[
\beta^-={\operatorname{Cov}(r^{\rm strat},r^{\rm hold}\mid
r^{\rm hold}<0)\over
\operatorname{Var}(r^{\rm hold}\mid r^{\rm hold}<0)}.
\]

The scalar utility contribution per label hour is

\[
U_{\Delta}(w)=Ww\,\bar e
-{1\over2}Ww^2\lambda_{\rm TE}\sigma^2_{\Delta}
-{1\over2}Ww^2\lambda_{\beta}\sigma_{H,-}^2
  (\beta^- -\beta_0)_+^2.
\]

`w` is projected action notional divided by pair equity. CVaR at the configured
tail quantile is reported as `CVaRShadowLossJPY` only; it is intentionally not
part of the active utility before a causal paired replay.

## Screening status

The component and focused unit tests are complete. It is not connected to
`strategy.go`, live YAML, systemd, or a service binary. A same-symbol,
prequential replay must first compare it with the current joint Fast utility,
including fees, executable BBO labels, effective samples, block uncertainty,
drawdown, and turnover. Until that gate passes, the component remains a
diagnostic/shadow research artifact and cannot change orders.

## First matured ETHJPY replay

The first same-symbol replay used the current ETHJPY YAML and production
simulator from `2026-08-16T00:00:00Z` through `2026-08-18T00:00:00Z`. It loaded
causal warmup from `2026-08-15T17:30:00Z`, compacted BBO to one observation per
minute, used pair equity `6830.672313565` JPY, starting inventory
`0.01024405` ETH, and fixed visible queue multiplier `0`. No private fill
calibration or other ticker was used.

There were 47 eligible, matured, non-overlapping one-hour labels. The strategy
finished at `+50.0301` JPY versus `+49.3866` JPY for Hold, but the paired block
return difference was `-0.01934 bps` with standard error `0.14522 bps`; the
one-sided 95% lower bound was `-0.25822 bps` and the paired t-statistic was
`-0.133`. Twenty-two of 47 blocks were positive.

The final EWMA state had effective samples `17.18`, tracking error `1.0939 bps`,
downside effective samples `7.72`, downside beta `1.1117` (upper diagnostic
bound `1.1489`), and shadow downside CVaR `19.17 bps`. The old research report
used a fixed `N_eff >= 24` gate; that gate was not statistically compatible with
the configured six-hour half-life. It has been replaced by a dynamic precision
gate:

\[
SE_t=\sqrt{\widehat{\operatorname{Var}}(e_t)/N_{\rm eff,t}},\qquad
N_{\rm req,t}=\left(\frac{z\,\hat\sigma_t}{\max(\bar e_t,0)}\right)^2.
\]

For this replay, `SE=0.2639 bps`, the one-sided lower bound is `-0.4122 bps`,
and the implied `N_req=6689.9`; the gate is therefore
`INCONCLUSIVE_PRECISION`. The existing `MinimumEffectiveSamples=4` remains only
as a structural non-degeneracy requirement for moments, not as a production
promotion threshold. The small positive terminal JPY difference is also not
statistically distinguishable from zero.

This one-hour Relative-Hold label model is separate from the Fast 10m/15m/30m
path estimators. Those Fast estimators already learn a per-window persistence
half-life (the previously observed 10m value was about `4.77h`, subject to its
own causal sample). It must not be copied blindly into a one-hour equity-label
EWMA; the label process needs its own same-symbol persistence estimate.

### Relative-Hold (N_{\rm eff}) preload

Startup preload now includes the Relative-Hold structural effective-sample
requirement instead of assuming that the Fast lookback is sufficient. For an
EWMA label stream with horizon (H), half-life (h_{1/2}), and

\[
f=2^{-H/h_{1/2}},\qquad
N_{\rm eff}(n)=\frac{1+f}{1-f}\frac{1-f^n}{1+f^n},
\]

the loader finds the smallest integer (n\ge2) satisfying the larger of the
configured all-label and downside structural minima (N_{\rm eff}), then requires
`n * H + HorizonUpdateInterval` of causal history. The final preload is the
maximum of this requirement and the existing Fast/path/BOCPD/Macro warmup.
This only budgets the causal label clock: it cannot guarantee `DownsideReady`,
because the number of negative-Hold labels is path-dependent.
This is a structural readiness calculation; the online precision requirement
\(N_{\rm req}=(z\sigma/\max(\bar e,0))^2\) remains data-dependent and cannot be
known before replay. A compatible checkpoint can restore the accumulated
Relative-Hold sufficient statistics and bypass this label accumulation, while
the core Fast preload remains independently validated.

For the current ETHJPY study configuration (`H=1h`, `h1/2=6h`, minimum
`N_eff=4`, update interval `5m`), the required label count is `n=5`, giving
`N_eff=4.8709` and a Relative-Hold label warmup of `5h05m`. The existing core
requirement is `6h30m`, so the actual ETHJPY preload remains `6h30m`; the new
requirement is nevertheless enforced automatically if another symbol/config
has a shorter core warmup.

## Fast joint-optimizer integration replay

The scalar was then wired into `OptimizeUnifiedFastQuantity` through
`JointDistanceQuantityInput.RelativeHoldRisk`. It is added once in JPY/hour to
the candidate terminal-wealth objective; it is not copied into price, quantity,
or `allowBid`/`allowAsk` gates. Matured labels are updated only after the
one-hour outcome is observable. A fallback decision receives the same scalar
for objective diagnostics, without creating a second side controller.

The paired replay used identical ETHJPY data, fees, BBO convention, queue
multiplier, and initial state. The baseline had the scalar disabled; the
integrated arm enabled it with `TrackingErrorAversion=1` and
`DownsideBetaAversion=1`. After maturity, 165 joint decisions consumed the
scalar. Its accumulated per-decision utility contribution was
`-0.22997 JPY/hour` (this is an objective diagnostic, not realized P&L).

| arm | net P&L (JPY) | Hold (JPY) | excess vs Hold (JPY) | fills | scalar decisions |
|---|---:|---:|---:|---:|---:|
| baseline | 50.03015 | 49.38657 | 0.64358 | 34 | 0 |
| integrated | 50.03015 | 49.38657 | 0.64358 | 34 | 165 |

The paired delta is `0` JPY and `0` JPY versus Hold. This means the scalar was
causally evaluated inside the Fast optimizer but did not change the selected
orders on this sample; it is not evidence of an incremental trading benefit.
The block result remains `INCONCLUSIVE_PRECISION` (dynamic precision lower
bound `-0.4122 bps`, paired lower bound `-0.25822 bps`). The feature is
therefore not enabled in live YAML or strategy startup.

### Increasing mature labels without leakage

- For a one-hour label, advance a non-overlapping anchor only after the next
  executable-BBO point at or after `anchor+1h`; monotonic prices still produce
  valid labels even when no quote fills.
- To reduce the initial four-label warm-up, restore a same-symbol checkpoint
  containing strategy and Hold equity sufficient statistics, or run a causal
  pre-roll replay before the scored interval. Market-data preload alone cannot
  invent prior strategy equity labels.
- More frequent five-minute anchors are possible, but labels overlap. Their
  raw count must be reduced by the overlap/renewal factor (or HAC variance); it
  must not be treated as independent `N_eff`.
- In a monotonic market, bilateral gamma-cycle arrivals can genuinely be near
  zero. The correct behavior is to use one-sided terminal/risk-reducing labels
  and wait-loss/IOC logic where supported, not to manufacture completed cycles.

Full machine-readable output is in
`docs/gammacapture-relative-hold-risk-replay-2026-08-20.json`. The next
permitted study is a frozen, longer same-symbol walk-forward block or a
predeclared half-life/aversion sensitivity; no live restart follows from this
result.

## Causal preload, `scoreFrom`, and checkpoint lifecycle

The replay now has two explicit clocks:

1. `preloadFrom` is a shadow replay boundary. The same production simulator
   observes BBO/trades, creates synthetic fills, records strategy and Hold
   equity, and feeds only labels whose one-hour outcome has matured. The
   integrated arm keeps Relative-Hold `ShadowOnly` during this phase so the
   paired policy cannot use its own unscored warmup decisions.
2. `scoreFrom` is the requested `--replay-from`. At its first observable BBO,
   preload orders are cancelled, score counters and drawdown peak are reset,
   and the current account is marked as the scored initial state. Model
   sufficient statistics, delayed labels, balances, and cumulative fee ledger
   continue. The model is therefore not frozen at the start of scoring: labels
   that mature after `scoreFrom` still update the same model before later quote
   decisions.

An optional `--relative-hold-risk-checkpoint PATH` stores a 0600 atomic JSON
file. It contains the Relative-Hold EWMA weights/moments, downside bivariate
moments, bounded tail, last matured label, model configuration fingerprint, and
the replay cursor. A subsequent interval at the cursor or later loads it and
skips already-matured labels; a future/incompatible cursor is treated as a
cache miss. The checkpoint deliberately contains no credentials, orders, or
balances. Core Fast model warmup still uses the normal same-symbol market-data
preload; this boundary prevents a Relative-Hold checkpoint from silently
pretending to restore unrelated Fast state.

Focused tests cover label feedback before `scoreFrom`, score-boundary reset,
model checkpoint round-trip/config mismatch, atomic checkpoint I/O, and
post-checkpoint continuation. The short ETHJPY replay confirmed
`checkpointLoaded=true` on a contiguous later interval and retained matured
labels while leaving baseline/integrated P&L paired.

## Five-day same-symbol factor replay (2026-08-15 through 2026-08-20)

The requested production-style comparison was shortened to five UTC days:

- symbol: `ETHJPY`;
- scored interval: `2026-08-15T00:00:00Z`–`2026-08-20T00:00:00Z`;
- causal warmup: `2026-08-14T17:30:00Z`;
- executable-BBO replay sampling: 5 minutes;
- queue multiplier: `0` (a fill-model comparison, not private-fill calibration).

The five-day factor arms produced the same path under this sparse executable
fill sample. The comparison was run with the same causal warmup/preload for
every full arm; the queue-calibration arm remains a separate short replay:

| arm | net P&L (JPY) | Hold (JPY) | excess (JPY) | fills (buy/sell) | max drawdown | maker fee (JPY) |
|---|---:|---:|---:|---:|---:|---:|
| Legacy | 779.09384 | 760.12321 | 18.97063 | 3 (2/1) | 0.1700% | 0.30 |
| Horizon touch | 779.09384 | 760.12321 | 18.97063 | 3 (2/1) | 0.1700% | 0.30 |
| Volume profile | 779.09384 | 760.12321 | 18.97063 | 3 (2/1) | 0.1700% | 0.30 |
| VP + POC risk | 779.09384 | 760.12321 | 18.97063 | 3 (2/1) | 0.1700% | 0.30 |
| Acquisition reset | 779.09384 | 760.12321 | 18.97063 | 3 (2/1) | 0.1700% | 0.30 |
| Asymmetric risk | 779.09384 | 760.12321 | 18.97063 | 3 (2/1) | 0.1700% | 0.30 |
| Relative-Hold arm | 779.09384 | 760.12321 | 18.97063 | 3 (2/1) | 0.1700% | 0.30 |

Relative-Hold matured five labels (`N_eff=4.8709`) in the scored block and
the integrated replay accumulated `N_eff=10.3776` after causal continuation.
Its paired estimate was `+0.2301 bps` with standard error `0.6288 bps`,
one-sided 95% lower bound `−0.8042 bps`, and `t=0.366`; the gate remains
`INCONCLUSIVE_PRECISION`. The baseline and integrated arms had zero P&L
difference, so there is no evidence of an incremental trading effect yet.

Calibration also failed for the five-day factor comparison (`absolute side
error=5` against the supplied synthetic 4-buy/4-sell calibration). Since no
private fills are available, these fills are not evidence for tuning or live
promotion. The result is useful as a consistency check: none of the tested
factors changed a quote or fill on this interval. No YAML or live service was
changed as a consequence.

### Why the five-day fill count is low

Yes, the 5-minute replay aggregation is a major contributor. The scored
replay retained exactly 1,440 BBO snapshots (one per 5-minute bucket), while
the raw ETHJPY bookticker archive contains about 1.89 million rows for
2026-08-15 through 2026-08-19 alone. `readBBOFilesCompacted` keeps only the
last BBO in each bucket; the simulator then activates/requotes on that
retained snapshot. A bid or ask that touched the order between two retained
snapshots is invisible and cannot fill. Public trades were not reduced to
5-minute buckets, so this also creates an intentionally conservative,
asymmetric replay input: dense trades versus a sparse quote path.

The observed three fills are therefore not evidence that the live strategy
would only trade three times in five days. They are a lower-resolution replay
result. Exact-BBO replay (or a smaller interval such as 1 second for the
scored window) is required before using fill cadence or factor differences for
production conclusions; it is more expensive and should be run after a
focused interval/cache check.

## Live canary activation (2026-08-20 19:17 JST)

At the user's request, the exact five-day replay parameters were enabled for
the ETHJPY userspace strategy:

```yaml
relativeHoldRisk:
  enabled: true
  shadowOnly: false
  horizon: 1h
  halfLife: 6h
  minimumEffectiveSamples: 4
  minimumDownsideEffectiveSamples: 4
  priorEffectiveSamples: 4
  targetDownsideBeta: 1
  confidenceZ: 1.645
  tailQuantile: 0.95
  maxTailSamples: 256
  trackingErrorAversion: 1
  downsideBetaAversion: 1
```

The live binary was rebuilt from the current source and the previous binary
was retained as `bin/bbgo.pre-relative-hold-20260820`. A JSON duration decoding
compatibility fix was required because BBGO's config bridge serializes YAML
durations as strings while this research model's public fields use
`time.Duration`.

The first implementation exposed an important lifecycle gap: startup replay
replayed BBO/trades for the Fast models but did not replay the strategy equity
path, so it could report thousands of BBO updates with
`relativeHoldMaturedLabels=0`. The research checkpoint previously used for
this purpose was also not suitable for live because it was produced from a
coarser aggregated-BBO replay.

The live preload now queries same-symbol Binance private fills over the causal
`preloadFrom` interval, rolls current balances backwards to the interval start,
replays those fills forward, and marks Strategy/Hold wealth on the same
one-second causal BBO accumulator used by startup model warmup. A balance
reconciliation check rejects incomplete history, manual transfers, or another
strategy's fills rather than manufacturing labels. The resulting
`ModelCheckpoint` is marked `live-private-fills/1s-causal-bbo`; checkpoints with
an aggregated/research source are rejected and regenerated on the next startup.
After `scoreFrom`/startup, the normal executable-bid equity bridge continues
the same non-overlapping one-hour label clock.
