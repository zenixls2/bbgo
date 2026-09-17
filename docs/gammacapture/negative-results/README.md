# GammaCapture negative-result archive

This directory preserves experiments and promotion decisions that were explicitly rejected or found non-viable. It is evidence, not a production policy or deployment authorization.

## Included records

- `2026-09-08-causal-target-route-fill-collapse.json`: maker-first/risk-reducing-IOC candidate failed executable BUY participation and round-trip stability across the checked windows.
- `2026-09-08-replay-calibration.json`: private maker-fill calibration and live/replay parity were not established; decision `NO-GO`.
- `2026-09-08-sell-only-hybrid-fill-collapse.json`: sell-only hybrid reduced activity and round trips through liquidation rather than a stable two-sided policy; decision `NO-GO`.
- `2026-09-14-regime-learning-validation.md`: causal Kline, BOCPD45, regime persistence, and public-fill economic gates failed; research and production promotion were `NO-GO`.
- `2026-09-16-promotion-no-go.md`: candidate target-owner/rebalance promotion was rejected because research, private-fill, holdout, state, and release-boundary gates were incomplete.

## Explicit exclusions

- The xmaker stale-hedge reservation record is excluded because it says `fixed-regression-verified` and explicitly directs that the fix be kept.
- Source changes, candidate YAML, live configuration, binaries, private-ledger data, order handoff data, deployment transcripts, and generated runtime artifacts are excluded. They remain uncommitted until separately reviewed and scoped.
- A `NO-GO` record does not prove that every related implementation is permanently invalid. It records the tested hypothesis, data scope, failed gate, and decision for the stated version and market regime.

## Provenance

The records were copied from the workspace evidence files used during the corresponding reviews. Their original source paths, experiment IDs, checksums, and applicability limits remain inside each record. No live order mutation, deployment, or service restart is authorized by this archive.
