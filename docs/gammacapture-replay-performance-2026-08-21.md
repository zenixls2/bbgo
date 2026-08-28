# GammaCapture replay performance (2026-08-21)

## Equivalence contract

Replay acceleration must preserve the complete causal policy result, not only
summary P&L. The benchmark therefore freezes the event archive, next-BBO fill
semantics, queue multiplier, fee model, starting account, preload boundary and
score interval. Acceptance requires byte-for-byte identical JSON, including
fills, equity curve, Hold, drawdown, markouts and diagnostics.

Benchmark command shape:

- symbol: `ETHJPY`;
- preload: `2026-08-20 10:48:35 JST` onward;
- scored interval: `2026-08-20 23:00:00` to `2026-08-21 00:00:00 JST`;
- BBO interval: one second;
- two-arm target action-value comparison; and
- fixed queue multiplier `0.25`.

## Profile findings

The original run took `7:53.94` wall time and `494.74` CPU seconds. Parsed data
loading was already cached and was not the bottleneck. The largest repeated
work was:

1. post-fill utility rebuilding completed horizon extrema and terminal paths;
2. conditional execution rebuilding the same current horizon state several
   times within one quote decision; and
3. `VolumeProfileState` rebuilding a conditional path only to retrieve the
   latest profile snapshot already stored on the current horizon point.

## Implemented changes

- Post-fill utility now consumes the existing causal `crossingExposures`
  rolling cache. The original raw-point implementation remains as a test-only
  reference and is compared across rising/falling paths, BUY/SELL sides,
  multiple timestamps and every candidate statistic.
- Horizon exposures retain exact `MinimumAsk` and `MaximumBid` values. This
  preserves the original floating-point touch comparison instead of replacing
  it with a nearly-equivalent logarithmic threshold.
- The latest conditional execution state is memoized per horizon within one
  accepted BBO and invalidated on every new or same-second replacement BBO.
- `VolumeProfileState` reads the latest point's causal profile in O(1).
- The two independent target-comparison arms run concurrently. They share only
  immutable input slices and retain separate models/accounts. This is limited
  to the paired mode whose global research providers are read-only.

## Measured result

| Version | Wall time | CPU time | Peak RSS |
|---|---:|---:|---:|
| Before | 7:53.94 | 494.74 s | 213 MiB |
| Cached scans, sequential arms | 3:12.88 | 203.21 s | 226 MiB |
| Cached scans, parallel arms | 2:01.33 | 236.25 s | 353 MiB |

The final wall-time speedup is `3.91x` (`74.4%` reduction). Total CPU falls
`52.2%`. Parallel arms trade about `140 MiB` extra peak memory for lower wall
time. The final JSON is byte-for-byte identical to the original result, and
the research package passes the Go race detector.

## Remaining preload work

The remaining profile is concentrated in genuine time-series work:
`JointPathPayoffStatistics`, adaptive path-decay estimation, incremental
crossing-exposure construction, conditional kernels and volume-profile
snapshots. Repeated experiments can be accelerated further with a versioned
research-only model-state checkpoint at the exact score boundary, keyed by
input file fingerprints, effective config, BBO interval and preload endpoint.
Such a checkpoint must include all sufficient statistics and pending matured
labels; it must never restore future state or a live account. Until complete
checkpoint round-trip equivalence is tested, the replay continues to perform
the causal preload rather than silently shortening it.
