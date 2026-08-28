# GammaCapture replay performance review (2026-08-26)

## Scope

This review covers the research-only production replay and the
horizon-conditioned utility replay adapter. No live strategy code path, YAML
configuration, checkpoint, systemd unit, or userspace cron job was changed.

The paired replay used ETHJPY BBO/trade data, a four-hour causal warmup, a
two-hour score interval, 5-minute replay sampling, queue multiplier zero, and
the same account inputs for both arms.

## Findings

The main avoidable complexity was in conditional execution state construction.
Every newly completed horizon start rebuilt a rolling lookback with
`buildConditionalExecutionStates`, giving approximately:

```text
initialization: O(N)
incremental path before fix: O(N * W)
```

where `W` is the retained horizon lookback. The state slice also grew by one
element at a time with an exact-length allocation, adding repeated historical
copying and avoidable garbage collection.

The HCU replay adapter had a separate memory issue: it retained every BBO in a
map so a pivot timestamp could be looked up later. The map is now retired by a
continuation-lease retention queue. Pending labels remain bounded by the
continuation horizon and anchor spacing.

## Implemented changes

`conditionalExecutionStateBuilder` now carries the existing monotone extrema
deques, quadratic-variation sums, return queue, gap segment state, and causal
volume-profile snapshot forward. It preserves the original estimator
definition while changing the incremental work to:

```text
initialization: O(N)
new BBO point:   amortized O(1)
state storage:   geometric capacity growth, O(N) total copying
```

The bounded return queue is periodically compacted. A same-second BBO
replacement still uses the exact prior fallback because the current model
point is mutable until the next sampled second. History trimming can rebuild a
bounded retained segment; it does not read future observations.

The HCU adapter now retains only the BBO interval needed by its continuation
lease and compacts the timestamp queue. Its public-touch shadow labels and
private-fill calibration warning are unchanged.

## Profile evidence

The pre-change CPU profile of the paired two-hour replay showed:

| Component | Cumulative CPU |
|---|---:|
| `JointPathPayoffStatistics` | 11.26 s |
| `buildConditionalExecutionStates` | 8.82 s |
| `appendCompletedHorizonExposures` | 5.57 s |
| `adaptivePathDecaySnapshot` | 3.99 s |

After the rolling builder and geometric slice growth:

| Component | Cumulative CPU |
|---|---:|
| `JointPathPayoffStatistics` | 6.36 s |
| `adaptivePathDecaySnapshot` | 3.61 s |
| `RollingVolumeProfile.Snapshot` | 3.14 s |
| `conditionalExecutionKernel` | 3.11 s |

The controlled replay wall time was approximately 36.3 seconds before the
change and 22.3 seconds after it. CPU samples fell from 36.36 seconds to
21.46 seconds. These are replay-process measurements, not live latency
guarantees.

## Paired replay result

The final paired replay produced identical baseline and HCU strategy results:

| Metric | Legacy | HCU sizing-only |
|---|---:|---:|
| Net PnL (JPY) | -797.6324 | -797.6324 |
| Hold PnL (JPY) | -1265.9570 | -1265.9570 |
| Maximum drawdown (%) | 0.8029 | 0.8029 |
| Full fills | 6 | 6 |
| Round trips | 2 | 2 |
| Quote uptime (%) | 91.6667 | 91.6667 |

The HCU arm collected 13 anchors, 12 short labels, 5 continuation labels, and
5 public-touch labels, but did not reach its readiness threshold during this
short interval. Therefore it correctly made no quantity change. The replay
remains diagnostic only: the standalone HCU alpha gate was rejected and
private-fill calibration was unavailable.

## Verification

The following passed:

```text
go test ./pkg/strategy/gammacapture -count=1
go test ./cmd/gammacapture-mm-research -count=1
go test -race ./pkg/strategy/gammacapture ./cmd/gammacapture-mm-research -count=1
```

Additional tests compare the streaming builder with the original batch
definition across gaps and causal horizons, verify incremental exposure
equivalence, and enforce bounded HCU book retention.

## Remaining hotspots

The remaining work is genuine model computation rather than an obvious
accidental quadratic scan: joint terminal-path utility, adaptive path-decay
statistics, volume-profile snapshots, and conditional kernels. They were not
altered in this patch because changing their update clock or approximation
would require a separate byte-for-byte replay equivalence study.
