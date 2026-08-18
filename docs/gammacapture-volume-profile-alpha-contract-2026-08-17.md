# GammaCapture volume-profile transition alpha contract (2026-08-17)

## Frozen scope

- **Primary alpha type:** crossing/arrival-state conditioning.
- **Single integration point:** the historical-path weights used by Fast's
  joint terminal-payoff distribution.  The alpha must not add a second price
  skew, quantity multiplier, inventory target, or hard allow-side gate.
- **Baseline:** the current side-specific BBO drawdown/reversal/QV conditional
  joint-path model.
- **Candidate:** the same model with a compact, causal public-trade volume
  profile state added to its conditioning information.
- **Symbol/data:** ETHJPY only; recorded public aggregate trades and executable
  BBO, in their original event order.  No other ticker and no pre-training.

## Causal state available at quote time

The profile is exponentially aged and price-binned in log-price space.  It
retains only bounded sufficient statistics, not raw trades.  The candidate
state is continuous rather than a collection of hand-written regime gates:

1. signed distance from executable BBO reference to the profile POC;
2. local smoothed volume density relative to POC density;
3. signed aggressive-flow imbalance near the current price;
4. signed displacement from the volume-weighted profile centroid;
5. normalized position between the nearest lower and upper local
   high-volume nodes.

The side reflection is exact: price/flow coordinates change sign between BUY
and SELL, while density does not.  Learned path similarity therefore decides
whether POC entry historically implied rotation or whether a low-volume
corridor implied continuation; no manually weighted POC trading rule is
allowed.

## Outcome and statistical clock

This is not a short fixed-markout study.  For opening horizon \(H\), an order
may first touch during \([t,t+H]\); an unmatched leg then receives the current
Fast completion horizon \(H_c\) (the longest configured Fast window).  The
label matures only after \(t+H+H_c\), and values the path in executable terminal
wealth after actual modeled fees.  This also makes later BBO baselines part of
the completion path instead of pretending that the initial midpoint remains
the relevant mean.

- Primary: 15-minute opening window with 30-minute completion window.
- Sensitivity: 30-minute opening window with 30-minute completion window.
- Quote distance: the existing Fast candidate distance, not a POC-derived
  distance.
- Outcomes audited separately: completed rotation, BUY-only/down traversal,
  SELL-only/up traversal, and no touch; the primary score is paired fee-net
  terminal-wealth prediction loss.

### Per-window profile clock correction

The original experiment's fixed five-minute POC-entry difference was a clock
mismatch and is retired.  Each Fast opening window \(H\) owns a separate
profile and compares its current POC state with the state one full \(H\) ago.
Its profile observation range is estimated from mature, executable-BBO
first-passage latency rather than fixed to the payoff horizon:

\[
L_H(c)=\max\!\left\{Q_c(\tau_{H,BUY}),Q_c(\tau_{H,SELL})\right\},
\qquad L_H(c)\leftarrow H\left\lceil L_H(c)/H\right\rceil.
\]

Coverage \(c\) is a study parameter, not a production constant.  The primary
contract uses \(c=0.80\); any other value is a separately counted candidate
and must be chosen on a calibration interval before an untouched holdout.  If
either side fails to reach \(c\) within `horizonLookback`, that window is
insufficient and may not extrapolate with an independent-window assumption.

To retain bounded memory, the profile remains exponentially summarized.  Its
half-life is set to

\[
h_H=L_H(c)\frac{\log 2}{-\log(1-c)},
\]

so exactly fraction \(c\) of stationary exponential profile mass lies within
the empirically measured fill-latency range.  No raw trades or per-window event
queues are retained.

## Promotion gate

The standalone strictly-prequential candidate must beat the BBO-only baseline
on paired terminal-wealth error with a positive one-sided 95% lower confidence
bound.  Rotation Brier score, traversal-direction accuracy, effective sample
size, calibration and stability by day are mandatory diagnostics.  The 30m
sensitivity must not have a negative lower bound.  Otherwise the result is
`REJECT_UNSTABLE` and no strategy/config/service change is permitted.

## Resource contract

- Trade ingestion: amortized \(O(1)\).
- Snapshot/model-clock evaluation: \(O(B)\), no more than once per minute.
- Memory: \(O(B)\), with a fixed maximum bin count and no retained raw-trade
  queue.
- Lazy exponential decay avoids revisiting every bin for every trade.
- Full replay is prohibited before component correctness and standalone alpha
  screening pass.

## Superseded fixed-clock result

Data: ETHJPY public trades plus executable BBO, 2026-08-01 through
2026-08-16 inclusive.  Predictions were one minute apart, labels were released
only after the full opening-plus-completion clock, and uncertainty used daily
blocks because adjacent labels overlap.

| opening + completion | scored | baseline MAE | VP MAE | paired improvement | simultaneous 95% lower bound | improved days |
|---|---:|---:|---:|---:|---:|---:|
| 15m + 30m | 19,593 | 5.0300 bps | 5.4714 bps | -0.4414 bps | -0.7733 bps | 2 / 16 |
| 30m + 30m | 18,975 | 8.8048 bps | 9.2449 bps | -0.4401 bps | -0.8226 bps | 4 / 16 |

The direct hypotheses were also weak: correlation of continuous POC-entry
strength with later rotation was 0.0111 at 15m and -0.0098 at 30m; correlation
of low-density-corridor signed flow with later direction was 0.0181 and 0.0239.
Direction accuracy did not reach a useful level, and rotation Brier score
worsened.

Result: **`REJECT_UNSTABLE`**.  The bounded profile component and reproducible
research command are retained, but the profile is not connected to live path
weights, inventory, quote distance, or quantity, and no YAML/service change is
permitted by this experiment.  This prevents a plausible market narrative
with near-zero measured association from becoming another multiplicative
controller.

During audit, an early research-only warmup branch was found to release future
labels at prediction time.  It was removed: every sample now enters a pending
queue and updates both the BBO baseline and volume residual model only at its
declared maturity.  The volume learner is residual-only and has a second
strictly-prequential zero-intercept calibration layer, so it cannot silently
refit the baseline.

The component benchmark on the deployment-class Intel N100 was 265 ns/event,
0 B/op and 0 allocs/op for the representative trade-ingestion loop with a
one-minute snapshot every 60 events.  The observed profile stayed within its
hard 512-bin bound (mean about 455 bins).

## Corrected per-window fill-clock result

The fixed five-minute transition above was subsequently identified as a clock
mismatch.  A new experiment used 2026-08-01 through 2026-08-08 only to estimate
the primary \(c=0.80\) first-passage clock, and kept 2026-08-09 through
2026-08-16 untouched for scoring.

| Fast window | BUY \(Q_{.8}\) | SELL \(Q_{.8}\) | aligned POC range | EW half-life | holdout paired improvement | simultaneous lower bound | positive days |
|---|---:|---:|---:|---:|---:|---:|---:|
| 15m | 3h43m | 2h50m | 3h45m | 1h36m54s | -0.1102 bps | -0.1917 bps | 2 / 8 |
| 30m | 3h54m | 2h37m | 4h00m | 1h43m21s | -0.5030 bps | -1.2150 bps | 2 / 8 |

This confirms the clock criticism: POC entry must be measured over hours, not
five minutes.  It does not rescue the candidate alpha.  On the untouched half,
POC-entry/rotation correlation was -0.0170 and -0.0316; corridor-flow/direction
correlation was 0.0130 and 0.0109.  The primary 80% coverage is therefore still
`REJECT_UNSTABLE` and remains disconnected from production.

Coverage is exposed as `--volume-profile-fill-coverage`; 70%, 90%, or an
adaptively selected coverage may be studied later, but cannot be selected on
this already inspected holdout.  Such a comparison requires nested
calibration or newly collected same-symbol data and must count every attempted
coverage in its multiplicity correction.

## Revised probability-weighted future-mean contract

The preceding payoff scorer still gave re-based windows equal weight.  That is
not the target used by an order whose fill may occur several Fast windows
later.  The next standalone hypothesis is therefore classified as **price
offset**, not crossing/arrival:

- **Existing baseline:** the current single-window time-weighted BBO mean.
- **Single possible integration point after promotion:** the side-specific
  future executable-mean distribution consumed by the Fast inventory/price
  target.  It may not independently alter quantity or an allow-side gate.
- **Null:** conditioning that distribution on volume profile does not reduce
  strictly-prequential BUY-bid / SELL-ask future-mean error.

For every Fast window \(H\), candidate orders are re-based at the beginning of
each future window.  Let \(J_s\) be the index of the first re-based window in
which side \(s\) crosses, and estimate on matured calibration paths

\[
\pi_{s,j}=P(J_s=j), \qquad
K_H(c)=\min\left\{k:\sum_{j=0}^{k-1}\pi_{s,j}\ge c
\text{ for both sides}\right\}.
\]

If a BUY first fills in window \(j\), its relevant subsequent mark is the
time-weighted executable bid mean in window \(j+1\).  For SELL it is the
time-weighted executable ask reacquisition mean.  The two fee-net labels are

\[
Y_{BUY}=\sum_{j=0}^{K_H-1}\pi_{BUY,j}
\left[10^4\log\frac{\overline{Bid}_{j+1}}{q^{BUY}_j}-f\right],
\]

\[
Y_{SELL}=\sum_{j=0}^{K_H-1}\pi_{SELL,j}
\left[10^4\log\frac{q^{SELL}_j}{\overline{Ask}_{j+1}}-f\right].
\]

The probability masses are unconditional, so the unfilled tail contributes
zero rather than being normalized away.  Label maturity is
\(t+(K_H+1)H\).  A prediction may be emitted every minute, but that cadence is
not its outcome horizon.  The primary paired score is the average reduction in
absolute BUY and SELL label error; chronological blocks must be at least
\((K_H+1)H\).

### Event-clock correction

The one-minute grid is only a numerical integration grid. It is not an
eligible quote-decision clock. Starting from an actual decision anchor, the
next anchor is

\[
T_{n+1}=\min\{T_n+H,\;\tau^{BUY}_n,\;\tau^{SELL}_n\}^{+},
\]

where \(^{+}\) means the next observable BBO after a crossing/fill event. Both
quotes are re-based at every such anchor. The latency calibration and scored
observations must follow this renewal clock; timestamps between anchors remain
available only for first-passage detection, time-weighted means, and profile
sufficient statistics. Scoring every minute is invalid and all results above
that did so are superseded.

On each realized path, a side that fills is marked against the time-weighted
executable mean during the next complete \(H\) interval; an unfilled side is
zero. Averaging these causal Bernoulli outcomes prequentially performs the fill
probability weighting directly, without pretending that overlapping minute
anchors are independent or imposing equal weights on hypothetical windows.

#### Event-clock result

The corrected renewal clock reduced the untouched holdout from thousands of
overlapping minute anchors to 774 scored 15m lifecycle decisions and 558
scored 30m decisions.

| Fast window | 80% BUY / SELL latency | label maturity | baseline MAE | VP MAE | equal-day delta | simultaneous lower bound | positive days |
|---|---:|---:|---:|---:|---:|---:|---:|
| 15m | 1h30m / 1h15m | 1h45m | 9.1707 bps | 10.1619 bps | -1.0643 bps | -1.7347 bps | 2 / 8 |
| 30m | 1h30m / 1h30m | 2h00m | 12.0957 bps | 12.7109 bps | -0.5573 bps | -0.9983 bps | 1 / 8 |

Both sides worsened at both horizons. At 15m BUY MAE changed from 9.2799 to
10.4091 bps and SELL from 9.0615 to 9.9147 bps. At 30m BUY changed from
12.2342 to 13.2620 bps and SELL from 11.9572 to 12.1598 bps. The joint
side-symmetric alpha therefore remains `REJECT_UNSTABLE`. All earlier
every-minute gate values are retained only as a superseded audit trail and
must not be used for model selection.

The corrected figures above also repair a research implementation defect:
the small regression helper had treated response coordinates 1 and 2 as a
probability and direction and clamped them to `[0,1]` and `[-1,1]`. In this
experiment those coordinates are BUY and SELL terminal payoff in bps and must
remain unbounded. A regression test now protects the unit contract. The bug
made the prior reported VP loss look artificially small; it did not cause the
rejection.

### Revised future-mean result

Calibration remained 2026-08-01 through 2026-08-08 and scoring remained the
untouched 2026-08-09 through 2026-08-16 block. Re-based first-fill mass reached
80% after 2h for BUY and 1h30m for SELL at both Fast clocks. Therefore the 15m
label matured at 2h15m and the 30m label at 2h30m.

| Fast window | label horizon | baseline side-mean MAE | VP side-mean MAE | sample-weighted delta | equal-day delta | simultaneous lower bound | positive days |
|---|---:|---:|---:|---:|---:|---:|---:|
| 15m | 2h15m | 6.7318 bps | 7.3931 bps | -0.6613 bps | -0.3694 bps | -0.9009 bps | 2 / 8 |
| 30m | 2h30m | 11.5945 bps | 12.9634 bps | -1.3689 bps | -1.0672 bps | -1.9026 bps | 2 / 8 |

Both BUY and SELL errors worsened. POC-entry correlation with the corrected
probability-weighted mean was 0.0053 / 0.0210, and corridor correlation with
the side-differential return was -0.0004 / 0.0015. Thus the POC alpha remains
`REJECT_UNSTABLE` under the corrected label. This result does **not** reject
the probability-weighted multi-window BBO mean itself as a replacement for the
production single-window target; that is a separate baseline-model comparison
which must freeze Volume Profile off.

## Four-regime production-policy diagnostic

After the standalone rejection, the requested full-policy diagnostic was run
on the four already-inspected canonical ETHJPY regimes. This is diagnostic
evidence only, not a promotion set. Every run uses next-BBO execution,
`queueMultiplier=0`, the same 6,808 JPY starting equity and 0.01024405 ETH,
and 24 hours of causal warmup. No live YAML was changed.

The replay changes exactly one scalar before the existing posterior inventory
transform:

\[
\mu^{inventory}_{H,t}
=\frac{E_t[Y^{BUY}_{H}]-E_t[Y^{SELL}_{H}]}{2}.
\]

`terminal-only` estimates the two side-specific executable-BBO terminal
values. `VP + terminal` adds only the strictly-prequential calibrated Volume
Profile residual. Quote distance, crossing probability, variance, quantity,
and all existing gates remain identical to the control.

| regime (UTC) | control P&L / excess vs hold | terminal-only P&L / excess | VP + terminal P&L / excess | control / terminal / VP fills | max DD control / terminal / VP |
|---|---:|---:|---:|---:|---:|
| decline, Aug 3 00:00–08:00 | -75.86 / +4.17 JPY | -77.56 / +2.47 JPY | -115.38 / -35.35 JPY | 1 / 3 / 4 | 1.25% / 1.29% / 1.97% |
| rise, Aug 5 15:00–21:00 | +68.62 / -0.12 JPY | +72.28 / +3.55 JPY | +91.75 / +23.01 JPY | 1 / 3 / 4 | 0.30% / 0.32% / 0.44% |
| range, Aug 8 00:00–12:00 | +12.07 / 0.00 JPY | +12.07 / 0.00 JPY | +12.07 / 0.00 JPY | 0 / 0 / 0 | 0.12% / 0.12% / 0.12% |
| mixed, Aug 10 00:00–18:00 | -39.27 / -2.43 JPY | -39.27 / -2.43 JPY | -39.27 / -2.43 JPY | 2 / 2 / 2 | 1.33% / 1.33% / 1.33% |
| four-segment sum | -34.44 / +1.62 JPY | -32.48 / +3.58 JPY | -50.83 / -14.77 JPY | 4 / 8 / 10 | — |

The provider was active, not silently bypassed: terminal-only recorded up to
14,210 target overrides in a segment and the combined model up to 14,645.
The combined model's mean target changed from +9.75 bps in the rise segment to
+0.58 bps in the decline segment, where it should have remained defensive.
It consequently paid 6.33 JPY total taker fees across the four segments,
versus about 0.99 JPY for both control and terminal-only.

Decision: **do not integrate Volume Profile into the live strategy**. It
improves the selected rise path but catastrophically fails the decline path,
does not create any range capture, and leaves mixed unchanged. This agrees
with the negative strictly-prequential standalone confidence bounds. The
research-only replay hook and reproducible command remain for audit; production
strategy code, configuration, credentials, and services remain untouched.

## Rising-conditioned Volume Profile contract

The next predeclared candidate tests the narrower claim that the Volume
Profile residual has value only while the existing executable-BBO terminal
forecast is rising. It is a **price-offset** alpha, not a new regime controller.

- Baseline: the side-specific terminal-only target.
- Single integration point: the Volume Profile residual added to
  `InventoryDirectionalMeanBps`; quantity, distance, crossing, variance, and
  allow-side decisions remain unchanged.
- Null: rising-conditioned VP does not reduce strictly-prequential terminal
  payoff error or improve fee-net terminal wealth versus terminal-only.
- Primary horizon: 15 minutes; predeclared sensitivity: 30 minutes.
- Prediction clock and label maturity: the existing event-clock lifecycle and
  its probability-weighted executable-BBO terminal label.
- Fees: already included in the BUY and SELL terminal outcomes; no second fee
  deduction is permitted in the standalone score.

The rising state is deliberately parameter-free and causal:

\[
g_{H,t}=\mathbf 1\!\left\{
\widehat\mu^{BBO}_{H,t}
=\frac{\widehat Y^{BUY}_{H,t}-\widehat Y^{SELL}_{H,t}}{2}>0
\right\},
\]

\[
\widehat\mu^{gated}_{H,t}
=\widehat\mu^{BBO}_{H,t}
+g_{H,t}\left(
\widehat\mu^{VP+BBO}_{H,t}-\widehat\mu^{BBO}_{H,t}
\right).
\]

Zero is classified as not rising. No future canonical regime name, realized
return, P&L, manually selected bps threshold, or another ticker may enter the
gate. Predictions are stored before their labels mature; model and calibration
updates occur only at maturity. The canonical decline/rise/range/mixed replay
is diagnostic after standalone scoring and cannot promote this candidate.

### Rising-conditioned result

The same 2026-08-01 through 2026-08-08 calibration and 2026-08-09 through
2026-08-16 scoring split was retained. The gate reduced VP exposure to 393 of
774 scored 15m decisions and 269 of 558 scored 30m decisions, but did not make
the residual predictive:

| Fast window | terminal-only MAE | rising-only VP MAE | equal-day delta | simultaneous lower bound | positive days |
|---|---:|---:|---:|---:|---:|
| 15m | 9.1707 bps | 9.7094 bps | -0.6269 bps | -1.1572 bps | 2 / 8 |
| 30m | 12.0957 bps | 12.3452 bps | -0.1811 bps | -0.3762 bps | 2 / 8 |

The multiplicity-adjusted alpha gate returned `REJECT_UNSTABLE`: too few
chronological blocks had positive incremental value.

The requested canonical next-BBO diagnostic then compared all four policies.
Rising-only VP produced exactly the same fills, P&L, fees, and drawdown as
always-on VP in every segment:

| regime | terminal-only excess vs hold | VP always | VP rising-only | rising-only application rate |
|---|---:|---:|---:|---:|
| decline | +2.47 JPY | -35.35 JPY | -35.35 JPY | 35.50% |
| rise | +3.55 JPY | +23.01 JPY | +23.01 JPY | 95.74% |
| range | 0.00 JPY | 0.00 JPY | 0.00 JPY | 100.00% |
| mixed | -2.43 JPY | -2.43 JPY | -2.43 JPY | 0.00% |
| sum | +3.58 JPY | -14.77 JPY | -14.77 JPY | 55.49% overall |

This equality is not a bypass: in decline the gate blocked 64.50% of target
queries and changed the mean directional target from +0.58 to -1.98 bps. The
remaining locally positive forecasts nevertheless coincided with every
harmful VP-driven execution, so the order path was unchanged. A short-horizon
positive terminal forecast identifies local rebounds inside a decline; it is
not a classifier for the longer regime in which VP happened to work. The
rising-only candidate therefore remains research-only and must not be wired
into production or YAML.

### Conditional-training correction

The preceding gate had a model mismatch: it switched VP off at inference time,
but the VP residual and its calibration coefficient were still trained on all
rising and declining observations. The next frozen candidate is a rising
specialist rather than a globally trained residual behind a final gate.

At prediction time the causal terminal-only sign is stored with the pending
label. Only predictions tagged rising may update the specialist after label
maturity:

\[
\mathcal D^{rise}_{H,t}
=\left\{(x_u,Y_u):u<t,\;\widehat\mu^{BBO}_{H,u}>0,\;
Y_u\text{ matured by }t\right\}.
\]

\[
\widehat\mu^{specialist}_{H,t}
=\widehat\mu^{BBO}_{H,t}
+\mathbf 1\{\widehat\mu^{BBO}_{H,t}>0\}
\widehat r^{VP}_{H,t}(\mathcal D^{rise}_{H,t}).
\]

The tag may not be recomputed at maturity. A false local rebound inside a
decline is therefore not discarded: once its executable terminal outcome
matures, it supplies negative evidence to the rising specialist. Non-rising
predictions cannot dilute the specialist coefficients. The baseline, horizon,
fees, event clock, label maturity, holdout split, integration point, and two
predeclared horizons remain unchanged; only conditional training differs.

The correction passed synthetic causal tests but failed the unchanged real-data
holdout more severely:

| Fast window | terminal-only MAE | conditional specialist MAE | equal-day delta | simultaneous lower bound | positive days |
|---|---:|---:|---:|---:|---:|
| 15m | 9.1707 bps | 9.9160 bps | -0.8168 bps | -1.3606 bps | 0 / 8 |
| 30m | 12.0957 bps | 12.9556 bps | -1.3425 bps | -2.9754 bps | 1 / 8 |

This rejects the contamination explanation. Conditional training did not
reveal a stable VP direction effect among causally rising-tagged observations;
it removed offsetting samples and increased error. Per the alpha gate, no
canonical strategy replay, production integration, YAML change, or service
restart is permitted for this candidate. Further threshold or feature tuning
on the same inspected dates would be data snooping. A genuinely distinct
future hypothesis may test profile density as uncertainty/liquidity state,
where it can alter one variance or arrival input but cannot directly set
direction.
