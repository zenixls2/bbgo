# GammaCapture research queue audit

Date: 2026-08-26 JST
Data cutoff used for the new WFO: 2026-08-26 02:01Z
Scope: determine which research items are safe to integrate into the main
strategy after the continuation and adaptive path-decay paired studies.

## Promotion rule

An item is not production-ready merely because its code exists or one replay is
positive. It must have causal evidence, a paired chronological walk-forward,
fee/risk accounting, acceptable drawdown and activity, private-fill calibration,
and a clean untouched comparison. If those conditions are not met it remains
research-only or shadow-only.

## Queue disposition

| Item | Current source/config state | Evidence | Disposition |
|---|---|---|---|
| Continuation posterior, Fast-only | New isolated WFO harness; Macro remains disabled in live YAML | Corrected 17 blocks: mean delta +7.3314 JPY, lower 95% +1.7766, but hold-relative lower 95% -4.4998 and fills 314→44 | **Rejected; do not integrate** |
| Adaptive path decay | Present in source and current YAML | 17-block paired screen: mean delta -0.8525 JPY, lower 95% -3.7409; DD 3.79%→4.13% | **Rejected for promotion; retain for research/rollback review** |
| Target-relative CE | Formula fixed in source; targeted tests pass | CE audit: source changed, production not changed; integrated replay worsened; private fill failed | **Source-only; not deployed** |
| Side-specific BBO imbalance | Hardcoded 15m payoff branch remains in source | Re-audit withdrew prior promotion: lifecycle-clock delta -0.0093 bps, lower -0.0235, 3/14 positive days, no action disagreements | **Withdrawn; not accepted alpha** |
| Volume Profile / POC conditioning | ETHJPY YAML active and non-shadow | Broad evidence did not pass full alpha contract; asymmetric POC result not promoted | **Canary/under-validated; no promotion** |
| Relative hold risk | ETHJPY YAML active and non-shadow | Inconclusive precision, negative lower bound, no private-fill ledger | **Canary/under-validated; no promotion** |
| Asymmetric oscillation risk | Source exists; ETHJPY disabled | Synthetic diagnostic fills only; no return promotion | **Disabled; research-only** |
| Multiscale regime | Source fallback exists; config disabled | Research-only evidence; no accepted causal/executable gate | **Disabled; research-only** |
| Dynamic inventory aim / regime-conditioned sizing | Retired or disabled in live quote path | Prior studies did not establish robust next-pivot executable value | **Do not reintegrate** |
| Dynamic price-beta target | Research source/docs exist; not active | Short synthetic arm positive but hold correlation remained high and private calibration absent | **Rejected for promotion** |
| Normal-flow pressure distribution | Source/docs exist; config disabled | Small effective sample; robust/tanh and balanced-rank variants failed economic gate | **Rejected; disabled** |
| Regime expected-value sizing | Research replay only | No production-quality causal/private-fill evidence | **Replay-only** |
| Horizon-conditioned utility gate | Research source/docs; no production wiring | Simultaneous lower bound -3.1318 bps, 12/32 positive blocks, private fill failed | **Rejected; no wiring** |
| HAR / volatility conditioning | Research-only | QLIKE result did not translate into economic improvement; turnover/re-entry worsened | **Disabled; research-only** |
| Quote lifecycle candidate | Research path | Inconclusive and lacked action diversity | **No promotion** |
| Pivot/Kline regime and 0.35 threshold | Research source/docs; production disabled | Executable action mean -6.754 bps, lower -8.132; fee-net-value gate rejected | **Rejected; do not integrate** |

## Queue result

The live-promotion queue is **empty**. No item currently satisfies the complete
promotion rule, so there is no justified strategy merge, live YAML change, binary
deployment, or restart from this audit.

The active Volume Profile, RelativeHoldRisk, and adaptive path-decay settings
are existing configuration state, not newly approved research promotions. Their
status should be treated as under-validated canaries until private-fill data and
clean paired evidence exist. This audit intentionally does not silently mutate
those pre-existing user changes.

## Required next work

1. Capture a private order/fill ledger containing submission, cancel, queue
   position/assumption, partial fills, and maker/taker outcome.
2. Re-run continuation, adaptive decay, CE, Volume Profile, and RelativeHoldRisk
   with the same untouched chronology and the calibrated fill model.
3. Resolve the stale side-imbalance source branch with an explicit before/after
   replay before removing or promoting it.
4. Promote only a candidate whose block lower bound is positive and whose fill,
   drawdown, fee, and hold-relative metrics do not deteriorate.
