# GammaCapture candidate → production promotion decision

- Decision time: 2026-09-16T15:28:24+09:00
- Symbol/venue: ETHJPY / Binance
- Request: promote the GammaCapture `cadidate` to production, enable rebalancing, update config, and restart.
- Decision: **NO-GO; no production mutation performed**.

## Evidence

### Observed

- Production unit: `gammacapture-strategy.service`
- Unit file: `/home/zenixls2/.config/systemd/user/gammacapture-strategy.service`
- ExecStart uses `/home/zenixls2/src/bbgo/bin/bbgo` and `config/gammacapture-ethjpy.yaml`.
- Service remained `active/running`, `Result=success`, `MainPID=201957`, `NRestarts=2`, start `2026-09-16 06:50:03 JST`.
- Production config has `pivotRegimeTarget.enabled: false` at `config/gammacapture-ethjpy.yaml:231-233`.
- Candidate config changes that gate to `true` at `config/gammacapture-ethjpy-v21-candidate.yaml:242-244`, and also changes the production/ledger lineage and owner/lock namespace.
- Pre-existing production artifacts were unchanged during this request:
  - binary SHA-256: `68c9625c3b5e530e1eef37c962b40ab3162c1afb509554380e87c340193360fb`
  - production config SHA-256: `d1a205e3366a1c9e96ecc73cb5a97bf1eaed71cccde04337606d29ee1c4708b4`
  - candidate config SHA-256: `3c9d1bca5fac9344930ed3fc31e1ca7ede15c5ace7421b7e85d95af73bb9313f`
- `OpenCode` was used for a read-only five-lane promotion review. It performed no writes, build, API mutation, deployment, or restart. Its process exited successfully after the review lanes; because the CLI returned no final synthesis, only independently verified repository/workspace evidence is treated as authoritative here.

### Derived

- Enabling the candidate would authorize the causal pivot/continuation target owner and therefore alter strategic inventory-target/rebalance behavior; it is not a cosmetic config change.
- The current production rebalance audit reports `causalRegimeTargetEnabled=false` and `selectedInventoryTargetRatio=0.5` for 505/505 evaluations. The candidate setting is intended to change that behavior.
- Source validation and the candidate live-config contract are internally coherent, but correctness is not a promotion authorization.

### Estimated / research evidence

- `/home/zenixls2/workspace/gammacapture-regime-learning-validation-20260914/final-report.md:9-11` explicitly states: `Source-correctness gate: PASS. Research promotion: NO-GO. Production deployment: NO-GO.`
- The same report records failed/inconclusive economic gates for causal Kline, BOCPD45, regime persistence, and public-fill; it also records unresolved Relative-Hold preload lineage and startup maturity blockers.
- `/home/zenixls2/workspace/gammacapture-candidate-latest-5h-exit-audit-20260908.json:192-203` records candidate promotion `NO-GO` because private calibration failed, `queueMultiplier=0` was an assumption, candidate PnL was worse than baseline, and the IOC attempt had zero fills. Its fills are synthetic replay fills.
- `/home/zenixls2/workspace/gammacapture-learning-state-storage-decision-20260915-v2.md:1-7,103-118` says the approved storage architecture was not implemented/deployed and the current state/checkpoint/WAL semantics do not support an immediate full-readiness claim.
- `/home/zenixls2/workspace/gammacapture-production-readiness-20260916/public-shadow-20260915T160437Z/summary.json:11-19,35-45,73-89` shows infrastructure smoke acceptance only; public-fill was not ready/stale, fills were synthetic, Sharpe was unknown/incomplete, and real mutation calls were zero.

## Engineering validation performed

- `PATH=/usr/local/go/bin:$PATH go test ./pkg/strategy/gammacapture -count=1` — PASS.
- `PATH=/usr/local/go/bin:$PATH go test -race ./pkg/strategy/gammacapture -count=1` — PASS.
- `PATH=/usr/local/go/bin:$PATH go test ./cmd/gammacapture-mm-research -count=1` — PASS.
- `PATH=/usr/local/go/bin:$PATH go test ./... -count=1` — PASS.
- `PATH=/usr/local/go/bin:$PATH go test -race ./... -count=1` — PASS.
- `git diff --check` — PASS.

These are source correctness gates only; they do not establish alpha, fill fidelity, capacity, or deployment safety.

## Blockers and action

1. Explicit research and production decision is NO-GO.
2. Candidate private-fill calibration/queue evidence is insufficient; public/synthetic fills cannot substitute for private fills.
3. Candidate PnL was worse than baseline in the cited replay and the candidate’s late-decline exit claim was not live-proven.
4. Relative-Hold lineage/preload and startup maturity remain deployment blockers.
5. The worktree is intentionally dirty across a broad source surface; there is no isolated, reviewable production commit to deploy.
6. The required post-build deployed-artifact/runtime readback cannot be satisfied without first passing the promotion gate.

Therefore the production YAML was not replaced, the binary was not rebuilt/installed, no exchange or private order API was called, and `gammacapture-strategy.service` was not restarted. Rebalancing remains in the current safe production mode; the candidate target-owner/rebalance switch remains unpromoted.

## Smallest safe next step

Run a fresh, sealed, same-symbol execution replay with route-specific private-ledger calibration, current source/config checksums, an uncontaminated holdout, and explicit startup/mature-state readiness. Only after those gates pass should a separately authorized deployment create a backup, replace the binary/config atomically, restart the service, and verify deployed checksums plus runtime target-owner/rebalance telemetry.
