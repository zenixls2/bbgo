# GammaCapture Preload Sufficiency and Model Freshness Review

**Review time:** 2026-08-27 23:52 JST

**Scope:** ETHJPY live strategy, capture archive, persisted checkpoint/state, startup replay code, and model observation paths. No service restart, live configuration change, or deployment was performed.

## Executive conclusion

The host contains more than 30 calendar days of ETHJPY capture, but the live strategy does **not** use 30 days as its normal startup training window. The archive has 36 daily ETHJPY files from `2026-07-23` through `2026-08-27`; however:

- configured `aggTradeWarmup.lookback` is `6h`;
- `RequiredStartupWarmup()` is dominated by the causal pivot CE `startupWarmup: 24h`;
- when a compatible checkpoint exists, startup replays only the checkpoint delta;
- the current checkpoint is current and contains the rolling model state rather than a 30-day raw replay.

This is intentional for bounded rolling estimators, but it means **30 days of stored market data must not be interpreted as 30 days of model evidence**.

## Current preload and checkpoint state

The persisted ETHJPY state/checkpoint was updated through approximately `2026-08-27 23:48:37 JST`:

- checkpoint version: `16`;
- checkpoint `replayAfter`: `2026-08-27 23:48:37 JST`;
- slow model last observation: `23:48:37`;
- Fast models last observation: `23:48:37`;
- BBO horizon last second: `23:48:37`;
- horizon decision updated: `23:45:00`;
- BOCPD45 matured labels: `2,256`, last calibration fit: `23:48:05`;
- pivot regime last observation: `23:48:37`, completed leg samples: `52`;
- Relative-Hold matured labels: `29`, last matured label: `23:40:09`, state ready and not stale;
- private-fill calibration: `40` fills, with newer pending labels still maturing;
- volume profiles last observation: `23:48:37`.

The running process and capture were both advancing near the review time. There is no evidence that the active slow, Fast, BOCPD45, pivot, Relative-Hold, or volume-profile paths stopped updating.

## Models showing unhealthy/insufficient-like status

### 1. Fast Drift: unhealthy for validation, not because of missing 30-day data

Observed live decision:

- `fastDriftHealthy=false`;
- reason: `Fast drift does not beat the zero-drift forecast`;
- validation samples: `24`;
- prequential skill: approximately `-0.299`;
- validation gain: approximately `-386.05 bps^2`;
- validation probability: approximately `0.00648`;
- `fastDriftApplied=false`.

Source inspection confirms Fast Drift trains one non-overlapping label per horizon and trims samples to the configured `lookback`. It is not designed to consume all 36 days. The unhealthy result is a deliberate statistical rejection of the learned forecast, and the production path correctly falls back to zero drift. It is not evidence that the BBO callback has stopped.

**Decision:** no model correction. Do not force healthy status or increase its weight.

### 2. Causal pivot target: sometimes waiting, but not stale

The live logs showed both ready and waiting states. The waiting reason is:

```text
causal regime target waiting for pivot evidence:
causal pivot leg with empirical same-direction amplitude
```

The pivot filter uses `reversalBps: 26`, `maxGap: 15m`, and requires completed same-direction legs. It resets on a gap beyond `maxGap`, and a new segment must mature. This is a structural readiness condition, not a raw-data shortage. The checkpoint currently shows `52` completed leg samples and a ready/healthy decision at the latest persisted point.

**Decision:** design behavior is consistent with the configured pivot segmentation; gap frequency should still be monitored.

### 3. Joint/path maturity: effective sample size is low despite thousands of pairs

The checkpoint contains large path pair counts but effective sample sizes around `1.58` for the adaptive path-decay components. This is caused by time-decay weighting and the current short rolling windows; raw pair count and effective sample count are different estimands.

This is a material design concern if the operator expects 30 days of data to improve confidence. The current code intentionally uses bounded rolling state and does not treat raw pair count as equivalent to independent evidence. No production parameter should be changed until an explicit effective-sample policy is defined.

**Decision:** retain as a research/model-policy issue; do not claim that 30 days are being used.

### 4. Legacy Online Arrival: genuinely stale, but not a live model

The persisted compatibility field `state.onlineArrival` reports:

- last update around `2026-08-07`;
- old cell observations with `lastObserved` around `2026-08-07`.

Source search shows `OnlineArrival` is retained only for old YAML/checkpoint decoding. The active quote path does not observe or consult it; current executable-BBO horizon state is maintained by `MarketMakerHorizonModel`.

**Decision:** this is stale compatibility state, not a lagging production model. It should eventually be removed from persisted output through a versioned migration, but deleting it in-place would be unsafe while preserving existing live state.

### 5. Macro Inventory: no update because disabled

The checkpoint has zero `macro.lastObservation`, while the top-level state contains older macro timestamps. The current ETHJPY profile has `macroInventory.enabled: false`. The active quote path therefore does not require Macro Inventory readiness.

**Decision:** expected disabled-component behavior, not an update failure.

## Data archive versus active model evidence

The ETHJPY capture archive has 36 daily dates, from `2026-07-23` through `2026-08-27`, with BBO and trade files. That proves archive coverage, not that every model consumed all rows. Active models use distinct windows:

| Component | Active evidence boundary | Current status |
|---|---|---|
| Slow/Fast crossing | rolling state plus checkpoint delta | updated, healthy |
| BBO horizon | rolling horizon state/checkpoint | updated, decision at 23:45 |
| BOCPD45 | causal 45-second labels and calibration window | updated, ready |
| Pivot CE | completed pivot legs, `maxGap=15m` | updated, healthy at checkpoint |
| Relative-Hold | matured private-fill labels, 1h label horizon | ready, not stale |
| Fast Drift | one matured non-overlapping label per horizon, bounded lookback | unhealthy by forecast validation |
| Private-fill calibration | private fills plus 5m maturation | warming/partially ready |
| Online Arrival | compatibility only | stale but unused |
| Macro Inventory | disabled in live profile | intentionally unused |

## Findings requiring follow-up

1. Add a startup/effective-policy diagnostic that reports, per model:
   - archive coverage;
   - replay interval actually consumed;
   - checkpoint delta interval;
   - oldest/newest effective observation;
   - raw count and effective sample count;
   - last update and last maturity time;
   - reason for unhealthy/insufficient status.
2. Add a policy assertion distinguishing `archiveCoverageDays` from `modelEvidenceWindowDays`.
3. Add an explicit stale-state warning for compatibility-only `onlineArrival`, so operators do not mistake it for an active model.
4. Decide whether adaptive path-decay effective sample size around `1.58` is acceptable. If not, change the estimator policy and validate it with a chronological holdout; do not merely increase a displayed count.
5. Keep Fast Drift fail-closed while its prequential score is negative.

## Verification

The source/tests remained green after the preceding observability change:

```text
go test ./pkg/strategy/gammacapture ./cmd/gammacapture-mm-research -count=1

go test -race ./pkg/strategy/gammacapture ./cmd/gammacapture-mm-research -count=1
```

Both commands passed. The live service was not restarted.
