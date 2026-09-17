# Rolling-terminal state-resolution ablation

Date: 2026-09-02 (Asia/Tokyo)

## Contract

This is a research-only direction/inventory-risk residual ablation.  The
existing point-feature executable-value forecast is the baseline and remains
the only policy owner.  The candidate may add one directional residual at the
continuation-CE integration point after promotion; it does not submit orders,
change inventory, or control a gate during this study.

The prediction clock remains one minute.  The primary and sensitivity labels
remain 15m and 30m executable-BBO outcomes:

```text
BUY  = 10000 * log(future_bid / current_ask) - fee
SELL = 10000 * log(current_bid / future_ask) - fee
```

The 30s variant changes only the completed public-flow state resolution.  The
15m and 30m feature windows remain 15m and 30m in wall-clock duration, which
means 30 and 60 states respectively.  Labels mature only after their horizon
and no future pivot state or private fill is used.

## Engineering changes

- Added an explicit bucket-size constructor while retaining the one-minute
  constructor as the compatibility path.
- Made bucket truncation, gap detection, contiguous-state validation, snapshot
  restore, and reset interval-aware.
- Persisted the state interval in public-flow and residual-model snapshots;
  mismatched 1m/30s checkpoints are rejected.
- Normalized return, spread-change, and observation activity features onto a
  one-minute coordinate.
- Converted the short EW half-life by wall-clock duration, so 3m remains 3m
  at both resolutions.
- Kept the one-minute prediction anchor and the executable label clock fixed.
- Corrected replay scoring so an unready candidate explicitly falls back to
  the frozen point baseline instead of being removed from the denominator.
  Common-ready metrics remain separately reported.

## ETHJPY paired replay

Replay interval: `2026-09-01T00:00:00Z` through
`2026-09-02T03:10:00Z`.  Both variants use the same BBO/trade archive,
1-second retained BBO stream, 6-hour blocks, 15m primary horizon, 30m
sensitivity horizon, and fee convention.

| State | Horizon | Eligible | Ready | Coverage | Common candidate MSE | Common point MSE | Full fallback-policy candidate MSE | Full fallback-policy point MSE | Full no-skill MSE |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 1m | 15m | 1,630 | 1,391 | 85.3% | 607.85 | 600.18 | 565.58 | 559.04 | 555.22 |
| 30s | 15m | 1,630 | 1,345 | 82.5% | 622.38 | 613.69 | 565.76 | 558.59 | 556.70 |
| 1m | 30m | 1,630 | 399 | 24.5% | 1,024.30 | 1,010.34 | 1,035.48 | 1,032.06 | 1,026.77 |
| 30s | 30m | 1,630 | 340 | 20.9% | 1,070.32 | 1,023.08 | 1,040.86 | 1,031.01 | 1,026.35 |

The MSE values are bps².  The full-policy rows include every eligible matured
anchor and use baseline fallback before candidate readiness.  Therefore the
coverage reduction cannot make the 30s candidate look better by deleting its
warm-up period.

Both candidates and baselines selected zero positive fee-net actions in this
interval.  Consequently action-level incremental SE is not identified; this
is reported as uncertainty, not as a zero-risk result.

## Decision

The 30s resolution variant is causal and engineering-valid, but it does not
beat either the 1m rolling candidate or the formal point-feature baseline.
The 15m result is worse on the common-ready set and slightly worse under the
full fallback policy.  The 30m result is weaker still and has insufficient
effective evidence for promotion.

Alpha gate: `INCONCLUSIVE_UNCERTAINTY` because the action delta has no
identified variance.  Forecast evidence is nevertheless negative relative to
the point baseline, so the state-resolution change is not promoted.

No production strategy, live YAML, checkpoint, service, or order behavior was
changed.  Replay artifacts:

- `data/gammacapture/research/ETHJPY-rolling-terminal-state-1m-v4-2026-09-02.json`
- `data/gammacapture/research/ETHJPY-rolling-terminal-state-30s-v4-normalized-2026-09-02.json`
- `data/gammacapture/research/ETHJPY-rolling-terminal-state-30s-v4-normalized-2026-09-02-gate.json`
