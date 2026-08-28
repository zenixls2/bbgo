# GammaCapture regime tagging implementation review — 2026-08-24

This is the implementation checkpoint for [the trend-persistence research plan](gammacapture-trend-persistence-sell-retention-research-plan-2026-08-24.md). It is research-only. No live YAML, `strategy.go`, service unit, or production binary was changed.

## What was implemented

`pkg/strategy/gammacapture/regime_persistence.go` adds an online `RegimePersistenceFilter` with four separate mechanisms:

1. A slow score is exponentially smoothed causally:

   `s̄_t = exp(-ln(2) Δt / h) s̄_{t-1} + (1 - exp(-ln(2) Δt / h)) s_t`.

2. Regime entry uses `|s̄| >= enterThreshold`; exit uses the narrower band `|s̄| <= exitThreshold`. The gap between the two thresholds prevents sign chatter.

3. A state change requires consecutive model-bucket confirmations and respects a minimum non-neutral state duration.

4. `FastReversalScore` cannot flip the slow state. It is reported as a separate conflict statistic:

   `conflict_t = max(0, -state_t × fastReversalScore_t)`.

The filter ignores duplicate buckets, resets after a causal data gap, clamps non-finite values, and has no access to future labels.

`cmd/gammacapture-mm-research --regime-persistence-study` compares the existing-style raw combined tag with the slow persistent tag using executable bid/ask markouts. A label uses the first observed BBO at or after maturity, with at most two sampling intervals of delay; it never fabricates a BBO at an unobserved timestamp.

The study now loads a causal warm-up of `feature memory + max(4×smoothing half-life, minimum state duration + confirmation memory)` and a forward tail of `horizon + 2×interval`. Warm-up observations seed the filter but are not scored as anchors; forward-tail observations can mature labels but are not scored as new anchors. The corrected 15-minute base run loaded 2026-08-21 22:24:59 through 2026-08-24 00:24:59 UTC, reported `forwardTailReady=true`, and produced 576 eligible pairs.

## ETHJPY result

Primary holdout: 2026-08-22 through 2026-08-24 UTC, 5-minute samples, 30-minute causal score/volatility windows, 15-minute forward executable markout, 20 bps round-trip cost.

| Arm | Signals | Regime transitions | Mean action value |
| --- | ---: | ---: | ---: |
| Raw combined tag | 329 | 275 | −11.90 bps |
| Slow persistent tag | 379 | 54 | −13.81 bps |

The corrected paired incremental value of the persistent arm was **−1.92 ± 2.25 bps** over 576 eligible pairs, with 4 of 8 chronological 6-hour blocks positive. The extra three pairs came from the previously omitted forward tail; the conclusion is unchanged: the simple slow-state replacement retains stale bullish states and does not yet improve the directional executable outcome.

A threshold/half-life check after the loading fix produced the following 2026-08-22 through 2026-08-24 holdout results:

| Variant | Incremental value | SE | Positive blocks |
| --- | ---: | ---: | ---: |
| Base: 15m / enter .35 / exit .15 | −1.92 bps | 2.25 bps | 4/8 |
| Half-life 10m | −4.32 bps | 2.81 bps | 4/8 |
| Half-life 30m | +1.55 bps | 2.05 bps | 7/8 |
| Enter .50 / exit .20 | +0.40 bps | 1.87 bps | 4/8 |
| Three confirmations | −2.21 bps | 2.19 bps | 4/8 |

The 30-minute variant also produced +2.63 bps over the descriptive 2026-08-17 through 2026-08-24 interval with 22/28 positive blocks, but that interval overlaps the parameter-selection windows and is not an untouched promotion result. The predeclared 30-minute neighboring horizon on the untouched 2026-08-22 through 2026-08-24 holdout was only +0.72 ± 2.80 bps with 4/8 positive blocks, so it is not independent confirmation.

## Effective sample calculation

The previous value of 8 was the number of 6-hour chronological stability blocks, not a calculated effective sample size. The study now estimates the paired-series effective sample using a predeclared Bartlett/HAC variance inflation factor:

`Var(mean) = γ₀ / N × [1 + 2 Σ wₖρₖ]`, with `wₖ = 1 − k/(L+1)` and `N_eff = N / max(1, VIF)`.

For the 15-minute base holdout, `N=576`, dependence window `L=12` lags (1 hour), `VIF=2.067`, and `N_eff=278.7`. The corrected HAC standard error is 2.25 bps. The 30-minute half-life arm has `N_eff=234.3`, VIF 2.458, and HAC standard error 2.05 bps.

The value 24 is not calculated from this dataset. It is a predeclared policy floor from the alpha gate, independent of the tagging result. Dynamic filtering changes the autocorrelation and therefore changes `N_eff`; it should not be used as a reason to lower the floor after seeing the holdout. If the floor is redesigned, it should be set before the next holdout using a power calculation such as:

`N_min = ((z_(1−α/m) + z_(1−β)) × σ_long-run / δ_min)²`,

where `δ_min` is the smallest economically meaningful incremental value and `σ_long-run` is estimated from training data only. Chronological blocks remain a separate stability test, not a substitute for `N_eff`.

The corrected alpha gate is recorded in [gammacapture-regime-persistence-gate-2026-08-24.json](gammacapture-regime-persistence-gate-2026-08-24.json). With HAC `N_eff=278.7`, it no longer fails for insufficient sample size; it returns `REJECT_UNSTABLE` because only 4 of 8 blocks are positive.

## Interpretation

The result separates two problems that were previously mixed:

- Persistence is an engineering/statistical improvement for reducing 275 → 54 state transitions.
- Persistence alone is not a validated sell-retention policy. Ignoring the fast reversal signal causes the tag to remain bullish through some adverse short-horizon moves.

Therefore the correct next model is a two-layer continuous action score, not a production state gate:

`action_t = persistentStateStrength_t × (1 - fastConflict_t)`.

The persistent state may control the slow inventory/SELL-retention tendency, while fast conflict only attenuates that tendency. This must be tested as a paired production-component replay with private-fill calibration; it must not be inferred from the current BBO markout study alone.

## Verification and promotion status

- Pure filter tests pass: confirmation, hysteresis, minimum duration, duplicate bucket, missing-data reset, non-finite bounds, reversal separation, and BUY/SELL symmetry.
- Standalone study tests pass: first-observable label maturity, executable-side spread effects, and paired-value accounting.
- HAC effective-sample calculation and threshold boundary tests pass.
- Alpha gate executed and returned `REJECT_UNSTABLE`.
- No live strategy integration was performed.

The component is ready for further research, not for live activation. The next evidence requirement is a longer rolling walk-forward with at least 24 independent chronological blocks plus private-fill/queue calibration before changing `regimeConditionedTarget`, `FastDrift`, or sell-retention behavior.
