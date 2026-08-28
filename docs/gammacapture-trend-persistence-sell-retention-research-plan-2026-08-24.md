# GammaCapture trend persistence and early-SELL retention plan

Date: 2026-08-24
Status: research and implementation plan; no live promotion
Owner: GammaCapture research / execution engineering

## 1. Objective

The immediate problem is that the strategy can sell a substantial amount near
the beginning of an upward move, then miss the remaining pivot-to-pivot
amplitude or buy back only after a delayed chase. The objective is to reduce
this early-SELL opportunity loss while preserving:

- positive fee-net Sharpe;
- lower maximum drawdown;
- lower return correlation with ETHJPY buy-and-hold price exposure;
- bounded inventory and exchange-compliant orders;
- causal, reproducible replay;
- private-fill calibration that is consistent with observed execution.

This plan treats the problem as two separate questions:

1. Is the regime/tagging process too reactive, so a persistent move is split
   into many short states?
2. Is the target/execution policy selling correctly according to its own
   neutral-inventory objective, but incorrectly for a persistent trend?

The second question must be answered even if the first question is negative.
The strategy must not be changed merely because a chart appears to show a
missed trend.

## 2. Current baseline and known failure modes

The current ETHJPY profile has `dynamicInventoryAim.enabled=true`, but
`dynamicInventoryAim.regimeConditionedTarget.enabled=false`. Therefore early
live SELLs are not directly caused by the regime-conditioned target adapter.
The active path uses the Fast target, FastDrift/BOCPD direction, marked
inventory control, probability-centered quantity, and the joint distance
optimizer.

The relevant implementation paths are:

- marked inventory is passed as `inventoryBase * mid` to the quantity target
  in [strategy.go:2188](/home/zenixls2/src/bbgo/pkg/strategy/gammacapture/strategy.go:2188);
- the neutral profile uses a 50% capital target and 0%/100% hard bounds in
  [gammacapture-ethjpy.yaml:348](/home/zenixls2/src/bbgo/config/gammacapture-ethjpy.yaml:348);
- an account above target produces SELL inventory actuation and moves the ask
  inward in [market_maker.go:2641](/home/zenixls2/src/bbgo/pkg/strategy/gammacapture/market_maker.go:2641);
- the BBO tag combines a horizon path-location term with a 30-second reversal
  term in [fast_drift.go:75](/home/zenixls2/src/bbgo/pkg/strategy/gammacapture/fast_drift.go:75);
- when FastDrift already moved the reservation price, the current quantity
  path explicitly clears `FastSellRestraint` to avoid double counting in
  [strategy.go:2206](/home/zenixls2/src/bbgo/pkg/strategy/gammacapture/strategy.go:2206);
- `preserveTwoSidedQuotes` can re-admit a minimum SELL after terminal-path
  rejection; the continuity floor does not assert a positive terminal-PnL
  lower bound, in [joint_quote_optimizer.go:1661](/home/zenixls2/src/bbgo/pkg/strategy/gammacapture/joint_quote_optimizer.go:1661).

These are hypotheses about causality, not yet a promotion decision. The
latest 48-hour baseline replay had more SELL than BUY fills and negative
fee-net excess over hold, while the new regime-EV arm had zero fills because
its gross value was negative. The zero-fill arm cannot be used to judge the
early-SELL mechanism.

## 3. Research contract

### 3.1 Null hypothesis

The current policy's early SELLs are economically correct risk reduction. A
trend-retention candidate does not improve fee-net terminal wealth after fees,
adverse selection, inventory risk, and realistic private fills.

The null is rejected only if the candidate improves the predeclared
side-timing and economic metrics on untouched chronological blocks without
creating an execution-calibration or drawdown failure.

### 3.2 Causal information set

At decision time \(t\), a candidate may use only:

- current and past executable BBO: ask for BUY-side price risk and bid for
  SELL-side liquidation risk;
- matured FastDrift, BOCPD45, volume, OFI, and path statistics;
- current balances, marked inventory, target, hard bounds, and active orders;
- private fill records whose timestamps are at or before \(t\);
- model state restored from a checkpoint that existed at \(t\).

It may not use the future pivot, future maximum excursion, later fill status,
or a later model decision to explain an earlier order. Every future outcome is
a label that matures after the prediction.

### 3.3 Two data sets must remain distinct

1. **Public-BBO research data:** useful for measuring price labels, tag
   stability, and hypothetical terminal wealth. It is not evidence of queue
   execution.
2. **Private-fill calibration data:** immutable order submission, replacement,
   cancellation, partial-fill, and fill ledger. It is required for any claim
   about actual fill count, fee drag, or promotion.

The research report must state which data set supports every metric.

## 4. Mathematical definitions

### 4.1 Marked inventory and target error

Let \(q_t\) be base inventory, \(m_t\) the executable mid mark, \(C_t\) cash,
and \(W_t=C_t+q_tm_t\). The marked inventory weight is:

\[
w_t = \frac{q_tm_t}{W_t}.
\]

For a neutral policy target \(w_0\), a price increase changes \(w_t\) even if
the position is untouched:

\[
\frac{\partial w_t}{\partial \log m_t}=w_t(1-w_t)>0.
\]

This is the mathematical source of the possible early risk-reduction SELL.
The research must report both:

- **risk error:** \(w_t-w_0\);
- **trend-adjusted error:** \(w_t-w_t^*(z_t)\), where \(w_t^*\) is the
  candidate alpha/risk target.

A trend target is not allowed to weaken the hard inventory bounds.

### 4.2 Tag stability

For a signed regime score \(s_t\in[-1,1]\), define a neutral dead band
\(\delta_s\) and a raw state:

\[
R_t = \operatorname{sign}(s_t)\quad\text{if }|s_t|>\delta_s,
\qquad R_t=0\text{ otherwise}.
\]

For every contiguous run of \(R_t\), record:

- duration;
- absolute and signed amplitude during the run;
- number of sign changes per hour;
- time from the first tag to the first executable pivot;
- future same-sign survival at 1H, 2H, and 3H;
- future first opposite-pivot time.

The raw score and any filtered score must be reported separately. Filtering
must not hide a raw model instability.

### 4.3 Pivot-to-pivot outcome

Do not define a pivot label as the terminal return at one arbitrary horizon.
For a prediction at (t), define a causal first-passage outcome using
executable prices:

- upward barrier: future ask-side or mid-reference excursion reaches \(+a\);
- downward barrier: future bid-side or mid-reference excursion reaches \(-a\);
- continuation: the first same-direction barrier is reached before the
  opposite barrier and survives until horizon \(H\);
- amplitude: the log-price distance from the entry pivot to the first
  confirmed opposite pivot, with censoring when no opposite pivot occurs.

The pivot confirmation rule may use future data only to create the matured
label. It must not be used in the feature vector at prediction time. A second
label family should use a fixed terminal markout so that the study can
distinguish pivot-label error from horizon-label error.

### 4.4 Sell opportunity cost

For a passive SELL at quote \(A_t\), quantity \(q\), maker fee \(f\), and
terminal executable bid \(B_{t+H}\), the one-fill terminal wealth difference
against holding is approximately:

\[
\Delta W_{SELL}(H)
 = q\left[A_t(1-f)-B_{t+H}\right].
\]

In log-bps form, including adverse-selection allowance \(a\):

\[
v_{SELL,t}
 = E\left[\log\frac{A_t}{B_{t+H}}\cdot10^4-f_{bps}-a_{bps}
 \mid z_t,\;\text{SELL fills}\right].
\]

For a persistent rise this value should be negative. A SELL size function
should therefore shrink continuously as the evidence for continuation rises,
while still allowing risk-reducing inventory sales.

### 4.5 Proposed continuous retention state

Define:

- \(p_{surv,t}(H)=P(\text{same signed regime survives }H\mid z_t)\);
- \(\mu^+_t(H)=E[\text{future same-direction amplitude}\mid z_t]\);
- \(\sigma_t(H)\) as predictive uncertainty;
- \(c_t=f_{bps}+a_{bps}+e_{bps}\) as the all-in one-way economic cost;
- \(o_t=\operatorname{clip}((w_t-w_0)/B,0,1)\) as normalized overweight.

The first candidate retention score is:

\[
r^+_t = \operatorname{clip}\left(
p_{surv,t}(H)\frac{\mu^+_t(H)-c_t}{\sigma_t(H)+\epsilon},0,1\right).
\]

The candidate SELL allocation is:

\[
q^{sell}_t = q^{risk}_t +
q^{explore}_t(1-\lambda r^+_t),
\]

where \(q^{risk}_t\) is the continuous amount required by the risk target
after the trend buffer, and \(q^{explore}_t\) is the ordinary two-sided
exploration allocation. The exchange minimum remains a final lattice
constraint, not a reason to round a rejected continuous allocation upward.

The first implementation must also test the symmetric bearish case so that a
SELL-retention improvement is not an unbounded long-bias bug.

## 5. Workstream W0: freeze and reproduce the baseline

### Tasks

1. Freeze the current Git commit, config checksum, model checkpoint checksum,
   data manifest, and Go toolchain version.
2. Re-run the current production replay with exact scoring boundaries and a
   separate warmup boundary.
3. Save the baseline JSON report, fill ledger, and quote-origin ledger.
4. Verify that all baseline fields are deterministic when replayed twice.

### Required baseline outputs

- PnL, excess over hold, Sharpe, maximum drawdown, downside deviation;
- ETHJPY return correlation and beta;
- BUY/SELL quote count, fill count, notional, fee, and average lifetime;
- 1m/5m/10m SELL markout and post-SELL maximum adverse continuation;
- current marked inventory, policy target, selected target, and hard bounds;
- `FastDriftApplied`, `FastDriftMeanBps`, direction, BOCPD probability;
- `FastSellRestraint`, projected SELL notional, and continuity-floor reason;
- private-fill calibration error by side and by quote distance.

### Completion condition

Two identical baseline runs have byte-equivalent metrics apart from runtime
metadata, and every missing field is documented rather than silently treated
as zero.

## 6. Workstream W1: causal early-SELL attribution

Add a research-only attribution table keyed by each quote submission and fill.
The table must preserve the immutable quote-origin state. Existing quote-origin
support should be reused; do not join a later model snapshot onto an earlier
fill.

### Required attribution fields

```text
timestamp, order_id, side, quote_price, quote_distance_bps,
mid, executable_bid, executable_ask,
inventory_base, marked_inventory_jpy, policy_target_jpy,
selected_target_jpy, hard_min_jpy, hard_max_jpy,
fast_direction, direction_coverage, fast_drift_applied,
fast_drift_mean_bps, fast_drift_strength,
bocpd45_direction, bocpd45_change_probability,
fast_sell_restraint, sell_fill_probability,
projected_sell_notional, continuity_floor_applied,
fill_time, fill_quantity, fee, terminal_markout_1m,
terminal_markout_5m, terminal_markout_10m,
future_max_rise_before_drawdown, pivot_to_pivot_amplitude
```

### Primary attribution classifications

Every early SELL must be assigned to one or more mutually auditable causes:

1. marked-inventory risk correction;
2. ordinary two-sided exploration;
3. FastDrift reservation-price adjustment;
4. BOCPD/directional state change;
5. joint continuity floor;
6. post-fill completion or target-restoring continuation;
7. exchange/account constraint;
8. unknown or missing diagnostic.

The unknown category must be non-zero if instrumentation is incomplete.

### Completion condition

At least 95% of SELL fills in the baseline can be attributed to a concrete
causal path. Otherwise do not tune any model parameter.

## 7. Workstream W2: tag and regime audit

### Experiments

Run the following in a standalone research scorer before touching live code:

1. raw Fast direction only;
2. raw BBO state tag;
3. `pathLocation` only;
4. `shortReversal` only;
5. BOCPD45 only;
6. current weighted combination;
7. separated slow regime plus fast reversal risk modifier.

For each arm, use the same historical BBO and the same matured labels.

### Measurements

- median and p10/p90 state duration;
- state transitions per hour;
- fraction of sign changes inside a known upward pivot-to-pivot leg;
- persistence \(P(R_{t+H}=R_t)\);
- amplitude retention ratio;
- calibration curve by score decile;
- Brier score, log loss, and calibration slope/intercept;
- conditional SELL opportunity cost by state duration bin;
- performance split by 5m, 10m, 15m, 30m horizons.

### Important control

The 45-second BOCPD horizon must not be interpreted as a 5–15 minute
economic regime. If it is retained, it should be treated as a microstructure
shock/reversal feature. A slower regime state must be estimated separately.

## 8. Workstream W3: pivot and persistence models

Test models in increasing complexity and stop when a simpler model is adequate.

### Candidate A: persistence filter

Use a causal exponentially smoothed score:

\[
\tilde s_t=\alpha s_t+(1-\alpha)\tilde s_{t-1}.
\]

Use hysteresis thresholds \(\delta_{enter}>\delta_{exit}\) and a minimum
state lease. The lease is measured in model buckets, not BBO events. Start
with a predeclared small grid such as 1, 2, and 3 five-minute buckets; do not
select the best value on the final holdout.

### Candidate B: semi-Markov duration model

Estimate:

\[
P(\tau>u\mid z_t, R_t),
\]

with a duration-dependent hazard. The output is survival probability, not an
independent buy/sell gate. This is preferable to forcing all regimes to have
the same geometric duration.

### Candidate C: amplitude-conditioned target

Estimate conditional amplitude and uncertainty by state, duration, and
current path location. Use robust quantiles or a censored model because
unfinished trends are right-censored. The model must distinguish:

- a state that is correct but has small remaining amplitude;
- a state that is wrong;
- a state that is correct but terminates before the trading horizon.

### Candidate D: separated target and execution controls

Use the slow persistence state only for the trend-adjusted inventory target.
Use the 30-second reversal and BOCPD45 only for quote distance, adverse
selection, or survival uncertainty. Do not let the same short signal both
flip the target and change the quote side.

## 9. Workstream W4: sell-retention and target implementation

All candidates must first be pure functions with no account or exchange calls.

### Proposed research files

Create isolated components such as:

- `pkg/strategy/gammacapture/regime_persistence.go`;
- `pkg/strategy/gammacapture/pivot_label.go`;
- `pkg/strategy/gammacapture/sell_retention.go`;
- `cmd/gammacapture-mm-research/trend_persistence_sell_study.go`.

The exact names may change, but the separation is mandatory:

- label construction must not submit orders;
- persistence must not own inventory or price;
- sell retention must return a continuous quantity scale and diagnostics;
- the replay adapter must be the only place that combines the component with
  existing strategy state.

### Pure sell-retention API

The function should accept:

```text
currentMarkedWeight
policyTargetWeight
hardMinWeight, hardMaxWeight
directionScore
survivalProbability
amplitudeMean, amplitudeStdDev
forecastHorizon
makerFeeBps, adverseSelectionBps, turnoverBps
currentSellCapacity
venueMinimumSellNotional
```

It should return:

```text
continuousSellScale
riskReducingSellNotional
explorationSellNotional
trendRetentionScore
opportunityCostBps
confidenceLowerBps
reason
```

The output must be bounded, finite, side-symmetric, and deterministic.

### Required invariants

- positive bullish continuation cannot increase SELL quantity;
- bearish continuation cannot reduce necessary risk-reducing SELL;
- a neutral signal reproduces the baseline allocation;
- all quantities remain within balance, hard inventory, and exchange limits;
- changing an unfilled target does not create an execution fee;
- maker fee is charged once per expected fill and never twice through a
  downstream overlay;
- a missing model output fails closed to the baseline, not to a zero or an
  oversized order;
- the candidate cannot modify quote price, horizon, or fill probability in
  the first sizing experiment.

## 10. Workstream W5: replay and walk-forward protocol

### Arms

Every experiment must include at least:

1. current baseline;
2. slow persistence only;
3. persistence plus trend-adjusted target;
4. persistence plus sell-retention sizing;
5. full candidate with all approved components.

The first comparison must hold price, horizon, queue model, fee, balances,
hard bounds, and private-fill inputs constant.

### Time split

Use chronological blocks:

```text
training / model-fit block
calibration block
walk-forward validation block
untouched final holdout block
```

Apply an embargo at least as long as the maximum forecast, continuation, and
label maturity horizon. Overlapping observations must not be split across
train and test without purging.

The 48-hour replay is an engineering diagnostic only. Promotion evidence
requires a longer same-symbol archive containing multiple rise, decline, and
range episodes. The exact final holdout dates must be frozen before looking at
candidate results.

### Private-fill calibration

For every replay arm, compare predicted and observed side fills using the
actual private ledger:

- predicted fill probability by quote-distance bin;
- predicted versus observed BUY/SELL fills;
- partial-fill quantity error;
- order-lifetime error;
- cancellation/replacement timing error;
- fee total error;
- calibration error by regime and by side.

Do not use `queue-multiplier=0` as private-fill calibration. That is a public
next-BBO counterfactual and must be labelled as such.

## 11. Statistical evaluation and promotion gates

### Primary metrics

- fee-net excess PnL versus hold;
- annualized or block Sharpe, with uncertainty interval;
- maximum drawdown and recovery time;
- ETHJPY return correlation and beta;
- SELL opportunity-cost markout at 1m/5m/10m;
- post-SELL missed upside before the first adverse drawdown;
- pivot-to-pivot amplitude capture ratio;
- BUY/SELL notional and fill balance;
- private-fill calibration error.

### Statistical method

Use day/episode/block bootstrap, not row-wise bootstrap, because BBO paths and
overlapping labels are dependent. Report paired baseline-candidate
differences on identical timestamps and identical private-fill records.

Use a predeclared candidate list and account for multiple comparisons. A
single best result from many tag thresholds is not promotion evidence.

### Promotion requirements

A candidate may proceed only if all are true:

- no causal or checkpoint leakage is found;
- focused unit, replay, and determinism tests pass;
- private-fill calibration is available and does not materially worsen by
  side or distance bucket;
- fee-net Sharpe remains positive on the untouched holdout;
- excess PnL is non-inferior to baseline and preferably positive after costs;
- maximum drawdown is non-inferior to baseline within the predeclared
  tolerance;
- early-SELL opportunity cost and missed-upside metrics improve with a paired
  confidence interval that excludes a practically harmful deterioration;
- ETHJPY correlation does not increase beyond the predeclared tolerance;
- the candidate does not rely on the continuity floor to hide a negative
  terminal-wealth result;
- the improvement survives at least one rise, one decline, and one range
  block.

If any required metric is unavailable, the decision is `REJECT_INCOMPLETE_DATA`,
not success.

## 12. Testing and engineering checklist

### Pure-function tests

Add tests for:

- monotonic bullish survival reducing SELL scale;
- monotonic bearish survival preserving or increasing risk reduction;
- neutral symmetry under BUY/SELL reflection;
- duration lease and hysteresis around both thresholds;
- amplitude censoring and no-future-feature construction;
- zero, NaN, Inf, negative, and missing inputs;
- hard inventory and venue minimum boundaries;
- fee charged once;
- no double application when FastDrift already changed quote price;
- deterministic JSON serialization.

### Replay tests

- identical input produces identical quote/fill sequence;
- warmup observations do not enter scoring PnL;
- exact scoring boundary is respected;
- gaps reset only the causal state that requires reset;
- pending labels mature at the first observable BBO at or after expiry;
- private fills are applied only at their actual timestamps;
- model checkpoint restore produces the same next decision;
- baseline arm remains numerically unchanged when candidate is disabled.

### Operational safeguards

- keep all new candidate switches off in the live YAML;
- use a separate research report namespace and checkpoint version;
- log `not-ready` reasons distinctly from economic rejection;
- preserve quote-origin state for every submitted order;
- add a kill switch that returns to baseline sizing without altering balances;
- never silently fall back from a missing private-fill ledger to synthetic fills;
- never overwrite the baseline artifact with a candidate artifact.

## 13. Rollback and staged deployment

### Stage 0: offline only

Pure functions, standalone labels, and public-BBO replay. No live strategy
change.

### Stage 1: shadow diagnostics

Run the candidate beside the baseline and log its proposed target, sell scale,
and reason. The candidate must not alter orders. Compare live observations
against the same causal replay.

### Stage 2: paper or private-fill replay

Use a frozen config and actual private-fill ledger. Confirm queue and partial
fill calibration before any order mutation.

### Stage 3: canary

Only after promotion gates pass, enable the candidate for one symbol and one
small risk allocation. Keep baseline and candidate metrics in separate
namespaces and monitor early-SELL, drawdown, correlation, and calibration.

### Immediate rollback triggers

- private-fill error becomes materially worse;
- any hard inventory violation;
- non-finite or unbounded quantity;
- SELL quantity increases during a strongly persistent bullish state;
- positive Sharpe disappears on the rolling canary block;
- drawdown exceeds the frozen risk limit;
- candidate and replay quote-origin fields diverge;
- any evidence of future-label or checkpoint leakage.

Rollback means disabling the candidate switch and restoring the last known
baseline config/checkpoint. It does not mean deleting research artifacts or
rewriting the baseline report.

## 14. Execution order and completion checklist

1. Freeze and reproduce baseline.
2. Add early-SELL attribution and missing diagnostic fields.
3. Produce tag duration, survival, amplitude, and pivot-matching report.
4. Decide whether the dominant issue is tag instability, target semantics,
   quantity fallback, or a combination.
5. Implement pure persistence and pivot-label components.
6. Implement the continuous sell-retention function.
7. Run unit tests and deterministic public-BBO replay.
8. Run chronological walk-forward with frozen candidate list.
9. Run private-fill calibration on non-zero observed fills.
10. Evaluate paired PnL, Sharpe, drawdown, correlation, markout, and
    pivot-to-pivot capture.
11. Promote only if every required gate passes.
12. Otherwise record the rejection reason and retain the candidate for the
    next data window without changing production.

The first concrete coding task is therefore **W1 causal early-SELL
attribution**, not tuning the regime lookback or changing the live target.
