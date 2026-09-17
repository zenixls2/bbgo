# Rolling terminal-value continuation implementation results

Date: 2026-09-02 (Asia/Tokyo)

## Scope

The implementation follows the frozen contract and plan in:

- `docs/gammacapture-rolling-terminal-continuation-contract-2026-09-02.md`
- `docs/gammacapture-rolling-terminal-continuation-implementation-plan-2026-09-02.md`

The component is isolated from `strategy.go`, live YAML, orders, balances,
private fills, IOC, cancellation, and production checkpoints. It uses the
first observed executable BBO at or after each horizon. The replay interval is
2026-09-01 00:00 UTC through 2026-09-02 03:10 UTC, with derived preload from
2026-08-31 11:15 UTC. The preload is `15m lookback + 30m maximum horizon +
24 * 30m capacity bound`; it is not used as inferential N_eff.

## Implementation completed

- UTC rolling one-minute BBO/trade sufficient statistics with OFI, signed
  trade imbalance, spread, executable returns, duplicate handling, missing
  minute handling, and gap reset.
- Restart-safe snapshot/restore for both the in-progress minute and the
  rolling value model.
- Independent 15m and 30m pending-label queues and horizon heads.
- Causal running normalizer and exact side-reflected BUY/SELL feature path.
- Fee-net executable labels; no second fee deduction.
- Predictive variance from residual uncertainty and diagonal model leverage.
- Shared-denominator diagonal RLS update. This corrected a high-dimensional
  update error in the first implementation.
- Standalone scorer with progress logging, block diagnostics, and
  `alpha_gate.py` experiment manifest.

## Prequential replay result

| Horizon | Eligible | Coverage | Candidate BUY/SELL MSE | Baseline BUY/SELL MSE | Candidate actions | Residual N_eff | Gate |
|---|---:|---:|---:|---:|---:|---:|---|
| 15m primary | 1,630 | 68.4% | 593.96 / 593.39 | 550.62 / 550.53 | 0 | 116.25 | INCONCLUSIVE_UNCERTAINTY / no action variance |
| 30m sensitivity | 1,618 | 68.3% | 1,039.52 / 1,038.23 | 1,004.91 / 1,006.81 | 0 | 61.70 | sensitivity only |

The effective sample size is now calculated from candidate matured residual
autocorrelation, separately for BUY and SELL, and reported as the smaller
side. It is not derived from action deltas or raw count divided by horizon.

## Ex-post regime slices

These labels are descriptive only and are not model inputs:

| UTC block | Scenario | Net mid return | 15m candidate MSE vs baseline | 30m candidate MSE vs baseline |
|---|---|---:|---:|---:|
| 00:00–06:00 | up-high-vol | +79.1 bps | 301.4 vs 273.3 | 452.5 vs 444.1 |
| 06:00–12:00 | down-high-vol | -86.8 bps | 403.0 vs 363.5 | 908.1 vs 772.9 |
| 12:00–18:00 | down-high-vol | -159.3 bps | 681.9 vs 660.0 | 1,499.8 vs 1,448.6 |
| 18:00–24:00 | range-high-vol | -5.2 bps | 963.7 vs 872.4 | 1,194.4 vs 1,178.6 |
| 00:00–03:10 | range-high-vol | -32.4 bps | 642.9 vs 613.8 | 1,248.5 vs 1,359.7 |

The 15m candidate is worse than the no-skill expanding fee-net side mean in
the first four blocks. The final partial 30m block is better, but this is not
enough to overcome the primary result and is not an independent holdout.

## Release decision

`alpha_gate.py` returned `INCONCLUSIVE_UNCERTAINTY` for the primary 15m head:
the conservative action policy selected zero actions, so incremental standard
error is not identified. The candidate also has no positive chronological
blocks. No Stage 3 component replay or production integration is authorized by
the plan.

The next research task is model-quality work, not a live configuration change:
reduce the candidate's forecast loss and produce non-zero, stable action
coverage while preserving causal maturity and the same regime-independent
acceptance gate.
