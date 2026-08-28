---
name: gammacapture-production-restart
description: Safely rebuild, deploy, restart, and verify the userspace GammaCapture production service before running an aligned replay or backtest.
---

# GammaCapture production restart

Use this skill only when the user explicitly authorizes a production restart or deployment. It covers the ETHJPY userspace service and must preserve unrelated dirty research changes.

## Fixed targets

- Unit: `gammacapture-strategy.service`
- Repository: `/home/zenixls2/src/bbgo`
- Binary: `/home/zenixls2/src/bbgo/bin/bbgo`
- Config: `/home/zenixls2/src/bbgo/config/gammacapture-ethjpy.yaml`
- Logs: `journalctl --user -u gammacapture-strategy.service`
- Build cache: use a task-specific writable `GOCACHE` under `/tmp`; never repurpose `HOME` or `CODEX_HOME`.

## Required sequence

1. Perform a read-only preflight: inspect `git status`, the unit's `ExecStart`, the current binary timestamp/hash, and the relevant config. Never reset, clean, or discard unrelated changes.
2. Run proportionate validation. For GammaCapture, at minimum run the strategy package tests and the relevant research-command tests.
3. Build to a temporary path. Verify that the artifact is a valid executable for the host architecture and record its hash. If the destination is running and direct copying reports `Text file busy`, replace it with an atomic rename of the temporary file; do not kill processes manually or overwrite broad paths. Preserve a recoverable pre-restart copy when practical.
4. Perform the service mutation only after explicit user authorization: `systemctl --user restart gammacapture-strategy.service`.
5. Verify `systemctl --user status` and recent journal output. Require an active/running service, the expected binary/config, no panic/fatal/permission errors, and evidence of startup preload, warmup, checkpoint, or capture restoration. A successful process start alone does not prove prefill completed.
6. Run the aligned replay/backtest only after a healthy restart. Use the current ETHJPY config and the causal next-BBO replay contract. Record interval, warmup, fees, queue/fill calibration, hold baseline, PnL, drawdown, fills, quote uptime, and warnings. Do not treat a short or uncalibrated sample as production promotion evidence.
7. Report the deployed binary hash, unit status, PID, prefill evidence, replay interval/command, metrics, and unresolved caveats.

## Safety invariants

- Loading this skill never implies permission to restart production.
- Never use live checkpoints as future labels in replay.
- Do not pass `--researchForceEntry` for performance claims.
- Do not repeatedly restart when systemd or journal access is unavailable; report the authority problem.
- Keep service operations and research conclusions separate: a restart verifies deployment, not strategy quality.
