# GammaCapture Production Alpha vs Pivot Fit

**Sample date (JST):** 2026-08-28 00:00:00–00:35:45

**Instrument:** Binance ETHJPY

**Data sources:** production `market-maker quote evaluation` journal diagnostics and ETHJPY bookticker capture. No live configuration, binary, service, or state was modified.

## Method

- BBO reference price: `(bid + ask) / 2`.
- Pivot detector: the same economic directional-change interpretation used by `PivotRegimeFilter`:
  - reversal threshold: `26 bps`
  - maximum gap: `15 minutes`
  - event time: capture `received_at`
- The capture file is UTC-daily, so the JST date beginning at midnight is sampled from the tail of `ETHJPY-bookticker-2026-08-27.csv`, starting at `2026-08-27 15:00:00 UTC`.
- `pivotRegimeDirection` is interpreted as the direction of the **current active leg**, not the direction of the next reversal.
- To avoid counting repeated 10-second diagnostics from the same leg as independent observations, the fit table uses the latest production diagnostic before each completed leg's confirmation.

## Coverage

- BBO rows in the JST-day sample: `35,054`
- BBO range: `2026-08-27 15:00:00.213380 UTC` – `2026-08-27 15:35:45.867100 UTC`
- Confirmed 26-bps pivot events: `5`
- Production quote evaluations: `13`
- Complete, unambiguous production-to-completed-leg matches used for the deduplicated fit: `3` legs

The first few minutes contain a state-boundary ambiguity: the production filter was already seeded by observations before the JST-day sample, while a standalone replay beginning at midnight is not. Those observations are retained as diagnostics but excluded from the independent fit.

## Deduplicated fit

| Completed active leg | Production diagnostic | Pivot direction | Pivot expected amplitude | Realized point-to-point amplitude | Error |
|---|---:|---:|---:|---:|---:|
| Down | 00:16:25 JST | -1 | 52.623 bps | 43.821 bps | +8.802 bps |
| Up | 00:17:48 JST | +1 | 57.845 bps | 31.313 bps | +26.531 bps |
| Down | 00:21:40 JST | -1 | 52.463 bps | 43.302 bps | +9.161 bps |

`Error = production expected amplitude - realized completed-leg amplitude`.

### Aggregate geometry results

- Pivot-direction consistency of `pivotRegimeDirection` with the current active leg: **3/3 = 100%**.
- Mean predicted amplitude: **54.311 bps**.
- Mean realized amplitude: **39.479 bps**.
- Mean signed bias: **+14.832 bps**.
- MAE: **14.832 bps**.
- RMSE: **16.983 bps**.
- Mean absolute percentage error: **41.991%**.

The production pivot geometry therefore fits the **direction** of the active leg in this small sample, but overestimates completed-leg spacing materially. The overestimate is consistent across all three deduplicated legs, not caused by only one outlier.

## Alpha-direction comparison

Using the same three deduplicated observations:

| Signal | Direction fit to current pivot leg |
|---|---:|
| `pivotRegimeDirection` | 3/3 = 100% |
| `bocpd45Direction` sign | 3/3 = 100% |
| `fastDirection` sign | 1/3 = 33.3% |

The Fast direction values at the selected diagnostics were small and close to neutral. The pivot/BOCPD agreement should not be treated as independent predictive proof: the pivot direction is the state label produced by the same causal filter, and BOCPD is observed at the same live event stream. The result is evidence of contemporaneous directional alignment, not a validated out-of-sample alpha estimate.

## Interpretation

1. **Direction:** The production pivot state tracked the active directional leg during the unambiguous portion of the sample.
2. **Spacing:** The historical same-direction completed-leg mean used by the pivot model was too large for today's realized legs by about `14.8 bps` on average.
3. **Actuator effect:** When `RemainingAmplitudeBps` reached zero, the causal regime target was no longer applied. This is consistent with the implementation: a completed expected leg should not keep increasing directional inventory pressure after its estimated continuation is exhausted.
4. **Fast alpha:** Fast direction was not a reliable independent fit to the pivot leg in this sample (`1/3`). This supports keeping Fast as a separate evidence source rather than allowing it to override pivot geometry.
5. **Sample size:** Three independent completed legs are insufficient for production parameter changes, confidence intervals, or promotion claims.

## Decision

No production model or parameter change is justified by this sample alone. In particular, do not apply a permanent `14.8 bps` haircut from these three legs. The correct follow-up is a longer chronological sample with:

- one row per completed pivot leg;
- no overlap between calibration, score, and holdout intervals;
- explicit receive-time gap markers;
- executable-side and midpoint amplitudes reported separately;
- maker-only fill calibration kept separate from IOC execution;
- walk-forward estimation of amplitude shrinkage and directional accuracy.

A candidate amplitude shrinkage model may remain research/shadow-only until it passes that holdout and private-fill calibration gate.
