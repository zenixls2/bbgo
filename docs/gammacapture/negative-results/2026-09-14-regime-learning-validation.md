# GammaCapture learner/regime strengthening — final source report

## Question

Can GammaCapture’s enabled learners be made causally consistent across preload, checkpoint restore, runtime updates, data gaps, and account reconciliation without relaxing the existing freshness/risk/execution gates; and do the tested causal components improve out-of-sample executable value?

## Decision

**Source-correctness gate: PASS. Research promotion: NO-GO. Production deployment: NO-GO.**

The final source snapshot passes targeted, package, race, research-runner, and full-repository tests. However, causal Kline pivot, BOCPD45, regime persistence, and public-fill experiments fail their validation/economic gates. No Production YAML, live binary, service, collector, balance, or real order was changed.

## Hypothesis & mechanism

- **Hypothesized:** Unobserved transport intervals must form hard causal segment boundaries; otherwise the first recovery event can mature a delayed label over an unknown path.
- **Hypothesized:** Cold preload, checkpoint delta replay, and runtime learning must consume equivalent event semantics.
- **Hypothesized:** Relative-Hold history must join authenticated strategy-owned fill identity to the local availability clock; account reconciliation of unknown origin must reset the current label path.
- **Hypothesized:** A causal component is promotable only if it improves untouched, cost-aware executable-BBO value—not merely classification accuracy or performance relative to a worse diagnostic baseline.

## Assumptions

- ETHJPY/Binance, public BBO/trades for market evidence.
- Private-ledger records are used only for ownership and availability-clock reconciliation; public expected fills are not private fills.
- `Risk.MaxBookAge=5s` and `PublicFillCalibration.MaximumObservationGap=5s` are not relaxed.
- Existing hard capital, terminal-wealth, active-order, fee, minimum-notional, and planner rejection gates remain unchanged.
- Intentional dirty worktree at HEAD `b1606c4149c3dae6e924fbf732f2a07217e9035a`; no reset/clean.

## Data & lineage

- Capture root: `/home/zenixls2/src/bbgo/data/gammacapture/ETHJPY`
- Available capture span: 2026-07-23 through 2026-09-14.
- Causal component split:
  - train: 2026-08-22 to 2026-08-29 UTC
  - validation: 2026-08-29 to 2026-09-05 UTC
  - holdout: 2026-09-05 to 2026-09-12 UTC
  - label purge: 15 minutes where supported
- Public-fill replay: 54,745 BBO events and 27,898 aggregate trades; score interval 2026-09-08 08:56:36–13:56:36 UTC.
- Dataset replay-cache SHA-256: `af4d7fe08b4fc6eae8894975cde07a86694078ab556235c85e4d7273a82d2c91`.
- Final research binary SHA-256: `e137285ea04d40c3f5c5db077960a53fc35060f8778f4ae4bb973a094816d012`.
- Source/file checksums: `source-checksums.sha256`.

## Implemented source corrections

### Gap/recovery contract

- Added one coalesced runtime gap marker from the local 5-second stale-data boundary into the next real evidence update.
- Recovery now resets transient path state for crossing/intensity, horizon paths, downside/upside e-processes, BOCPD45, FastDrift, FastEvidence, asymmetric risk, Relative-Hold, public-fill, continuation, causal Kline, and causal directional-change pivot state.
- Matured sufficient statistics remain intact.
- Fill/account refreshes that reuse the last BBO do not consume or create market observations.

### Startup replay

- Cold rebuild, checkpoint delta replay, and runtime use raw timestamp-ordered BBO events.
- Startup capture gaps use the strictest declared availability budget: the minimum positive value among live BBO freshness, public-calibration observation gap, and the legacy horizon fallback. ETHJPY therefore uses 5 seconds.
- Startup gaps clear Relative-Hold anchors and every other unfinished delayed-label path before the recovery BBO is observed.
- Effective ETHJPY startup lookback remains at least 24 hours; elapsed history is not equated with learner maturity.

### Relative-Hold

- Exchange trades are reconciled against authenticated strategy-owned private-ledger identities.
- `ObservedAt`, not exchange event time, is the availability clock used in replay ordering.
- Missing, unmatched, or incomplete account history fails closed.
- Authoritative account sync that changes base inventory now clears the current Relative-Hold baseline and anchor, while retaining matured model statistics; the next real BBO starts a new causal path.

### Public-fill/checkpoint

- Duplicate nonzero trade IDs are idempotent.
- Gaps discard queue and pending adverse labels.
- Checkpoint stores matured public-fill sufficient statistics but deliberately excludes active/terminal order maps and pending labels.

### Causal Kline study

- Split scoring uses `scoredTo = splitTo - labelHorizon`, with the boundary exclusive, so a prediction cannot mature in the next split.

## Validation design

- Tests were written RED-first for confirmed defects and then made GREEN.
- Chronological component studies separate train/validation/holdout where the runner supports it.
- BOCPD45 method selection was frozen before validation; holdout was left unopened after validation failure.
- Regime-persistence holdout was left unopened after validation failure.
- Causal Kline candidate selection used validation, but the runner emitted holdout results for both 3m and 5m candidates; this contaminates that holdout for future candidate selection and is recorded as a failure.
- No complete H5 multi-axis runner currently implements all regime cells, negative controls, nested selection, multiplicity correction, and a sealed holdout.

## Engineering results

| Gate | Exact scope | Result |
|---|---|---:|
| Learner regressions | 14 named tests, `count=100` | PASS |
| Learner regressions race | same 14 tests, `count=10` | PASS |
| GammaCapture package | `go test ./pkg/strategy/gammacapture -count=1` | PASS |
| GammaCapture package race | `go test -race ./pkg/strategy/gammacapture -count=1` | PASS |
| Research runner | `go test ./cmd/gammacapture-mm-research -count=1` | PASS |
| Research runner race | `go test -race ./cmd/gammacapture-mm-research -count=1` | PASS |
| Full repository | `go test ./... -count=1` | PASS |
| Full repository race | `go test -race ./... -count=1` | PASS |
| Formatting | `gofmt -d` on changed research files | PASS |
| Diff integrity | `git diff --check` | PASS |

These are engineering-health gates only; they do not establish alpha, fill fidelity, capacity, or deployment safety.

## Results & uncertainty

### Causal Kline pivot candidate

Validation selected 5m over the fixed 3m/5m set.

| Split | Candidate action | Diagnostic baseline | Incremental | Incremental one-sided 95% lower | Blocks |
|---|---:|---:|---:|---:|---:|
| Validation | -6.409 bps | -26.885 bps | +20.476 bps | +18.898 bps | 28/28 positive |
| Holdout | -6.030 bps | -24.795 bps | +18.765 bps | +15.866 bps | 27/28 positive |

**Estimated:** It is materially better than the old diagnostic baseline, but its absolute fee/cost-adjusted action value and lower bound remain negative. `promotionReady=false`; **NO-GO**.

### BOCPD45

Validation selected Platt calibration under the frozen Brier-skill rule:

- predictions: 12,270
- matured calibration samples: 12,727
- Brier: 0.250444
- prequential climatology Brier: 0.250042
- Brier skill: -0.1608%
- directional accuracy: 51.165% vs baseline 50.350%

**Estimated:** Slight accuracy lift does not compensate for worse probability calibration. Validation failed; holdout remained unopened. **NO-GO**.

### Regime persistence

Validation:

- eligible pairs: 2,016
- effective samples: 1,121.83
- raw action value: -12.354 bps
- filtered action value: -12.611 bps
- incremental: -0.257 ± 0.846 bps SE
- positive chronological blocks: 15/28

**Estimated:** Smoothing reduces state transitions but not fee-net executable value. Holdout remained unopened. **NO-GO**.

### Public-fill

Parsed final-source replay is schema-valid and deterministic against the prior source replay. A second foreground duplicate returned `exit_code=0`, was independently reparsed, and matched all predeclared deterministic economic/counter metrics exactly; wall-clock runtime fields were excluded from parity.

| Arm | Net PnL JPY | Max DD | Full fills | Round trips | Public expected quantity |
|---|---:|---:|---:|---:|---:|
| Baseline | -32.014 | 1.404% | 8 | 2 | 0 |
| Conservative | -59.914 | 2.350% | 0 | 0 | 0 |
| Central | -63.353 | 2.352% | 0 | 0 | 0.002332 ETH |
| Optimistic | -74.709 | 2.354% | 0 | 0 | 0.006478 ETH |

All arms retain 3,447 public-data gaps. Public expected quantity is not a private fill, PnL bound, DD bound, or Sharpe bound. **NO-GO**.

## Robustness & failure records

1. **Gap-spanning labels** — first recovery BBO could mature unknown-path labels. Fixed by runtime/startup segment invalidation and regression tests.
2. **Cold/runtime event mismatch** — cold one-second compaction erased intrasecond crossings. Fixed by raw-event parity.
3. **Relative-Hold ownership/clock mismatch** — account-wide same-symbol history and mixed clocks could contaminate labels. Fixed by authenticated ownership join and local observed clock.
4. **Account reconciliation contamination** — unknown-origin balance changes could cross the Relative-Hold label horizon. Fixed by path reset while retaining matured statistics.
5. **Public-fill restart discontinuity** — matured evidence was lost. Fixed by bounded sufficient-stat checkpointing.
6. **Causal Kline holdout exposure** — both candidates were emitted on holdout. That holdout is now contaminated for future 3m/5m selection.
7. **Component economics** — causal Kline absolute value, BOCPD Brier skill, persistence incremental value, and public-fill economics all failed.
8. **H5 validation gap** — no runner combines all required causal regime axes, negative controls, nested selection/multiplicity control, and sealed holdout.

## Remaining blockers

- ETHJPY Relative-Hold is configured active rather than shadow-only even though the prior replay conclusion was inconclusive. Production YAML was intentionally not modified; this remains a deployment blocker.
- Startup reports learner readiness but does not require every auxiliary learner to be mature. Each learner must continue to fail closed or defer to baseline until its own readiness gate passes.
- A complete H5 runner is still required for volatility, activity, trend/range, change-point, liquidity, order-flow, spread/data health, inventory, causal pivot, and execution-lifecycle cells.
- Required negative controls remain: block permutation, placebo labels/horizons, time reversal, gap injection, and duplicate-event injection.
- Candidate search counts and FDR/FWER or equivalent family-wise controls must be recorded before any new sweep.
- A new sealed holdout is required for further causal Kline candidate selection.

## Trading/engineering implications

- Do not deploy this learner snapshot.
- Do not change the 5-second freshness boundary, terminal-wealth gate, hard capital limits, active-order barrier, fee floor, or minimum executable notional to manufacture readiness or fills.
- Keep public evidence, public expected quantity, private fills, and synthetic fills as separate populations.
- Engineering correctness reduces leakage risk but does not imply improved PnL.

## Knowledge graph updates

Suggested evidence chain:

```text
Hypothesis: observation gaps invalidate pending labels
  -> Tests: runtime/startup gap regressions
  -> Code: Strategy gap marker + learner ObserveGap/reset methods
  -> Result: targeted/package/race/full PASS
  -> Decision: source correctness accepted; deployment rejected

Hypothesis: causal components improve executable value
  -> Experiments: causal Kline / BOCPD45 / persistence / public-fill
  -> Results: four NO-GO records
  -> Decision: no Production promotion
```

Attach `source-checksums.sha256`, `test-summary.json`, component JSON reports, and this report as artifacts. Mark the causal-Kline 2026-09-05–2026-09-12 block as contaminated for future candidate selection.

## Production readback

At 2026-09-14T23:16:39+09:00:

```text
service=gammacapture-strategy.service
ActiveState=active
SubState=running
Result=success
MainPID=3394523
NRestarts=0
live binary SHA-256=68c9625c3b5e530e1eef37c962b40ab3162c1afb509554380e87c340193360fb
config SHA-256=d1a205e3366a1c9e96ecc73cb5a97bf1eaed71cccde04337606d29ee1c4708b4
```

The source-only learner changes are not present in that live binary.

## Next experiment

Implement one manifest-driven, same-symbol H5 runner before testing another learner or parameter grid:

1. derive regime thresholds from each train prefix only;
2. use non-overlapping chronological folds with label purging and embargo;
3. report every required regime cell plus sparse-cell failures;
4. add dependence-preserving negative controls;
5. record the entire candidate family and simultaneous uncertainty;
6. freeze one candidate before opening a newly reserved holdout;
7. only then run execution-faithful replay/shadow validation.
