# Rolling-terminal candidate archive — 2026-09-02

The rolling-terminal continuation candidate is temporarily archived. The
source and replay artifacts remain in place so historical results can be
reproduced, but the candidate is not a production owner and must not be used
to replace the causal pivot CE target.

## Reason

The 1m and 30s state-resolution variants failed to beat the formal point
feature baseline on the primary 15-minute executable-BBO forecast. The 30s
variant also weakened the 30-minute sensitivity. Its action-level variance
was not identified because neither arm selected a positive fee-net action;
that is uncertainty, not evidence of safety.

## Preserved artifacts

- `cmd/gammacapture-mm-research/rolling_terminal_continuation_study.go`
- `pkg/strategy/gammacapture/rolling_terminal_value.go`
- `pkg/strategy/gammacapture/rolling_terminal_residual.go`
- `pkg/strategy/gammacapture/public_flow_minute_window.go`
- `docs/gammacapture-rolling-terminal-continuation-contract-2026-09-02.md`
- `docs/gammacapture-rolling-terminal-continuation-results-2026-09-02.md`
- `docs/gammacapture-rolling-terminal-continuation-correction-results-2026-09-02.md`
- `docs/gammacapture-rolling-terminal-state-resolution-2026-09-02.md`

The new baseline work is isolated from these files. No live YAML, strategy
wiring, checkpoint, service, or order behavior is changed by this archive.
