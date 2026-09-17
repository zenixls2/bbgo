# Baseline strengthening and rolling-terminal archive — 2026-09-02

## Decision

The rolling-terminal candidate is archived. A stronger baseline was screened
in two causal stages, but neither stage is approved to replace the production
causal pivot CE owner.

No production strategy wiring, live YAML, checkpoint, service, or order
behavior was changed.

## Candidate sequence

1. **Reflection-shared point baseline.** BUY and SELL use one shared ridge
   under side-reflected public-flow features. This reduces the parameter count
   while preserving the same fixed-horizon executable-BBO target.
2. **Pivot-state residual baseline.** The point forecast remains the offset;
   currently known causal pivot state/history can contribute only a shared
   residual. This prevents the pivot state from becoming a second independent
   target owner and guarantees a zero-residual fallback to the point baseline.

Both candidates use 24 hours of causal preload, 30-second anchors, first
observable BBO labels at 15m/30m maturity, a 12 bps executable label cost,
and 156 chronological six-hour blocks. BUY and SELL outcomes are learned from
every mature anchor; no selected-action-only training is used.

## Paired replay

Replay: ETHJPY `2026-07-24T00:00:00Z` through
`2026-09-01T00:00:00Z`, with data preload from
`2026-07-23T00:00:00Z`.

| Candidate | Horizon | BUY MSE vs point | SELL MSE vs point | Paired mean (bps/block) | 95% simultaneous lower | Positive blocks | Candidate actions / point actions |
|---|---:|---:|---:|---:|---:|---:|---:|
| Reflection-shared | 15m | −0.58 bps² | −0.48 bps² | −0.00268 | −0.00827 | 3/156 | 22 / 18 |
| Reflection-shared | 30m | −1.98 bps² | −1.77 bps² | +0.00745 | −0.00536 | 10/156 | 50 / 73 |
| Pivot-state residual | 15m | +4.49 bps² | +4.65 bps² | −0.02986 | −0.06410 | 9/156 | 389 / 18 |
| Pivot-state residual | 30m | +13.07 bps² | +13.25 bps² | −0.20374 | −0.38090 | 9/156 | 1,845 / 73 |

The reflection-shared candidate produces a small forecast MSE improvement, but
its lower bound remains negative and the action-level increment is not
identified as production value. The pivot-state residual creates many more
actions while worsening both MSE and paired value; it is rejected as
over-trading.

## Interpretation

The point baseline is already a strong shrinkage anchor for this public
fixed-horizon target. Adding a second state signal without a separately
validated target mapping does not improve it. The causal pivot CE remains the
only tested production target owner; replacing it would require a new exact
component replay with inventory, rebalancing cost, and private-fill
calibration. The current baseline screen does not justify that promotion.

## Artifacts

- `pkg/strategy/gammacapture/public_flow_pivot_symmetric_value.go`
- `pkg/strategy/gammacapture/public_flow_pivot_state_value.go`
- `cmd/gammacapture-mm-research/public_flow_pivot_symmetric_value_study.go`
- `data/gammacapture/research/ETHJPY-public-flow-pivot-symmetric-value-15m30m-12bps-2026-09-02.json`
- `data/gammacapture/research/ETHJPY-public-flow-pivot-state-residual-value-15m30m-12bps-2026-09-02.json`
- `docs/gammacapture-rolling-terminal-archive-2026-09-02.md`
