# GammaCapture inventory-neutral cycle allocation (2026-08-18)

## Scope

This change fixes terminal-path diagnostics and makes the existing unified Fast
quantity equation explicitly decompose into:

1. an expected-fill-neutral maker cycle; and
2. a one-sided target-restoring residual.

It does not introduce a new directional alpha, price offset, fee exception, or
post-quantity controller.

## Diagnostic defect

`jointQuotePathEffectiveSamples=0` did not necessarily mean that the terminal
path estimator lacked observations. A terminal rejection could evaluate more
than one effective path and then return either the continuity floor or the
probability-only bilateral baseline. Those returned decisions discarded the
evaluated sample and candidate counts.

The optimizer now records the largest effective path count as soon as a path is
evaluated and propagates only sample/candidate metadata through these fallback
returns. It does not propagate rejected prices, quantities, or utility claims.

## Quantity contract

For BUY/SELL notionals \(q_b,q_s\) and same-horizon touch probabilities
\(p_b,p_s\), the selected gross quote is split to satisfy, subject to executable
capacity,

\[
p_b q_b-p_s q_s=\Delta I^*.
\]

The realized allocation is decomposed as

\[
q_b=q_b^{cycle}+q_b^{target},\qquad
q_s=q_s^{cycle}+q_s^{target},
\]

where

\[
p_bq_b^{cycle}=p_sq_s^{cycle}
\]

and at most one of \(q_b^{target},q_s^{target}\) is non-zero. Capacity remains a
hard feasible-set ceiling. Promotion above the target-sufficient baseline still
requires the existing fee-net terminal-path utility; a positive nominal crossing
spread alone cannot override measured adverse markout.

## Screening result

- Primary type: quantity.
- Baseline: the previous closed-form probability-centered split.
- Integration point: `ProbabilityCenteredQuoteNotionals` only.
- Null: the refactor changes executable BUY/SELL quantities for the same gross,
  target and capacity.
- Result: null not observed; focused and package regressions retain the previous
  allocation while exposing cycle and target-restoring components.
- No PnL uplift is claimed because this is an algebraically equivalent ownership
  refactor, not an additional forecast.

## Verification

- BUY/SELL reflection symmetry.
- Expected-fill neutrality under asymmetric touch probabilities.
- Executable capacity clipping and realized correction reporting.
- Invalid/non-finite input fails closed.
- Full-risk promotion can use more than venue minima while leaving expected
  inventory at target.
- Continuity and baseline returns retain evaluated path/candidate diagnostics.
- The allocation was compared exactly with the retired inline formula over
  2,800 combinations: four gross notionals, seven desired-inventory changes,
  all 25 BUY/SELL touch-probability pairs, and four executable BUY intervals.
  Every BUY and SELL notional matched bit-for-bit.
- `go test ./pkg/strategy/gammacapture -count=1` passes.

The first event-replay attempt exposed a separate research-runner defect.
Quantity, post-fill, BOCPD quantity, and symmetric-horizon policy replays called
the explicit Macro-study warm-up helper.  It loaded `240h + 24h + 10m` even
though the live ETHJPY profile has `macroInventory.enabled: false`.  These
policy replays now use `MarketMakerConfig.RequiredStartupWarmup()`; explicit
Macro reversal/no-trade studies retain the long Macro history.  The ETHJPY
policy warm-up is consequently `6h30m`, exactly the configured six-hour
lookback plus two-stage path maturity, rather than `264h10m`.

With next-BBO fills, zero queue multiplier, `6830.672313565` JPY pair equity,
`0.01024405` ETH starting base, and a 5% drawdown stop, the 30-minute probe now
finishes in about 22 seconds instead of producing no report after six minutes
(at least a 16.6x improvement).  A cache-hit repeat produced identical PnL,
hold PnL, fills, drawdown, and refresh count.  The canonical eight-hour decline
replay then completed in 266.88 seconds: strategy `-84.5113` JPY versus hold
`-80.0265` JPY, excess `-4.4848` JPY, 8 BUY / 4 SELL fills, four round trips,
`1.3023` JPY fees, and `1.4063%` maximum drawdown.  It did not hit the 5% stop.
This result must not be compared as if it used the old runner's information
set: the old path incorrectly trained non-Macro models on eleven days solely
because dormant Macro parameters were present in YAML.

New quote logs expose `quantityProjectionCycle*JPY`,
`quantityProjectionTargetRestoring*JPY`, `jointQuoteCycle*JPY`, and
`jointQuoteTargetRestoring*JPY`.

## Cycle profit hurdle versus target restoration

`minimumNetEdgeBps` is the reservation profit of matched oscillation notional;
it is not a realized cost of a one-sided inventory correction.  The terminal
path payoff already applied it only to the physically matched BUY/SELL amount,
but `targetRestoringFastContinuation` subsequently required the hypothetical
full cycle to clear the same hurdle before admitting a correction.  That gate
coupled two different decisions and could strand an account away from its
same-horizon target.

The continuation controller now sizes the correction with the one-way-cost
Bellman optimum

\[
q^*=g\left(\frac{g}{W}-c_{1\mathrm{way}}\right)_+,
\qquad g=|I-I^*|,
\]

and admits it using one-sided terminal payoff plus target-progress value.  It
pays maker/adverse cost and posterior whole-position risk exactly once, while
`minimumNetEdgeBps` remains a hard gate only for matched cycle quantity.  A
regression test raises the cycle hurdle to 1,000 bps: a supported
target-restoring SELL remains executable, while ordinary oscillation candidates
remain rejected.  The compact 2026-08-03 00:00--00:30 next-BBO replay is exactly
unchanged (one BUY, zero SELL, `-36.210904` JPY strategy PnL, `-0.797223` JPY
excess versus hold), demonstrating that the separation does not relax ordinary
cycle trading.

## Inward price candidates: ETHJPY gate screen and next research

The apparent conservatism of `$P_b/P_s$` was separated into two effects:

1. `$P_b$` and `$P_s$` are the submitted passive prices (historical replay
   reconstructs them from the start executable BBO and the candidate distance).
   They are not forecasts of the future price. The path estimator then uses
   first passage of executable ask for BUY and executable bid for SELL,
   followed by terminal executable wealth. A BBO touch is still an optimistic
   fill proxy when queue position/private fills are unavailable; it is not a
   reason to lower the statistical gate.
2. The inward branch requires a paired incremental value improvement for each
predeclared candidate `$d$`:

   \[
   \widehat{\Delta}_d-z_{\mathrm{FWER}}\,\widehat{SE}(\widehat{\Delta}_d)>0,
   \qquad
   z_{\mathrm{FWER}}=\Phi^{-1}\!\left(1-\frac{\alpha}{K-1}\right).
   \]

   With `$K=13$` and `$\alpha=0.05$`, this is approximately a 2.64-sigma
   one-sided bound. It is family-wise error control for searching several
   price levels, not a fee haircut.

### ETHJPY component screen

Using one-second executable-BBO points from 2026-08-17, 142 five-minute
decision anchors after the six-hour lookback were evaluated. The test used
10, 15 and 30 minute horizons, 15/20/30 bps base distances, and 1/3/5/7/10
bps inward concessions. Results were aggregated by anchor, not reused as
independent fills:

| horizon | effective samples/anchor | mean paired delta range | lower-bound result |
| --- | ---: | ---: | --- |
| 10m | 25.5--25.7 | approximately -0.82 to +0.11 bps | 0 / 142 supported |
| 15m | 17.2--17.4 | approximately -0.73 to +0.12 bps | 0 / 142 supported |
| 30m | 8.8--8.9 | approximately -1.41 to +0.69 bps | 0 / 142 supported |

The positive point estimates were small relative to their standard errors, so
all simultaneous lower bounds remained non-positive. The current failure to
select inward prices is therefore evidence of an unproven/weak incremental
alpha in this sample, not evidence that the `$P_s/P_b$` reconstruction is
mispriced. Relaxing the gate without a new outcome model would turn the
distance grid into a multiple-testing-driven adverse-selection selector.

### Literature-guided next model

The next candidate should be a state-dependent action-value model, screened as
a standalone price-offset component before any live integration:

\[
U_s(d\mid x)=p_s(d\mid x)\,m_s(d\mid x)
 -(1-p_s(d\mid x))\,C_{\rm wait}(x,H)
 -\lambda\,\Delta\operatorname{Var}(W),
\]

where `$p_s$` is a state-conditioned touch/fill probability, `$m_s$` is the
fee- and terminal-markout-adjusted value conditional on a fill, and the
wait-cost term is the opportunity cost of retaining the current quote. A
candidate is eligible only when its absolute robust lower bound exceeds the
no-new-order baseline; an inward candidate need not beat the old quote by a
separate fragile test if its absolute action value is demonstrably positive.
The finite grid and predeclared comparison count remain, so the replacement
does not remove error control.

This direction follows [Cont--Kukanov's placement
problem](https://arxiv.org/abs/1210.1625), [Lokin--Yu's state-dependent
queueing fill probabilities](https://arxiv.org/abs/2403.02572) for deeper price
levels, and [Capponi--Figueroa-López--Yu's discrete-time model](https://arxiv.org/abs/2101.03086) combining price forecasts with simultaneous
buy/sell arrivals. For our data limitation, queue position is unavailable, so
public BBO touch must remain an upper-bound feature with a predeclared haircut
rather than a private-fill label.

Acceptance criteria for the next screen:

- strictly prequential state features (spread, executable-side QV, recent
  public trade intensity and depth imbalance when available);
- compare absolute robust action value against no-order and the current quote
  on ETHJPY only, with 10/15/30 minute horizons;
- report fill-proxy rate, terminal markout, fee-net value, lower confidence
  bound, and candidate-selection frequency separately;
- require positive holdout lower-bound utility and no increase in tail
  drawdown before changing the production gate.

## Multi-window price and arrival estimation

Extending the analysis beyond one selected Fast window is statistically valid,
but replacing `$P_b/P_s$` by a longer-window average is not. `$P_b$` and `$P_s$`
are submitted prices; what changes with `$k$` windows is the outcome horizon,
the cumulative first-passage probability, and the terminal executable wealth.

For a fixed quote held for `$T_k=kH$`, define side-specific first-passage times

\[
\tau_b=\inf\{u\ge0:A_{t+u}\le P_b\},\qquad
\tau_s=\inf\{u\ge0:B_{t+u}\ge P_s\}.
\]

The relevant objects are `$F_b(T_k)=P(\tau_b\le T_k\mid X_t)$`,
`$F_s(T_k)$`, their joint probability (not the product unless independence is
shown), and the conditional terminal-bid wealth after each touch. The robust
selection score should be

\[
\operatorname{LCB}_k
=\frac{\widehat E[\Delta W_{T_k}]
 -z\,\widehat{SE}(\widehat E[\Delta W_{T_k}])
 -\lambda\widehat{\operatorname{Var}}(W_{T_k})
 -C_{\rm stale}(T_k)}{T_k}.
\]

This is different from multiplying a one-window probability by `$k$`, or
assuming (1-(1-p)^k): first-passage events cluster, BUY/SELL arrivals are
dependent, and the price state changes after every window. Monte Carlo's wider
range as `$T$` grows is therefore a larger risk term, not automatic evidence of
larger expected profit.

There are two distinct policies:

1. **Fixed-order research horizon:** hold the same `$P_b/P_s$` for `$T_k$`,
   evaluate exact multi-window BBO paths, and include stale-quote/adverse
   selection cost. This requires retaining at least (T_k) history and must
   not be deployed as a 30-minute order lease.
2. **Renewal/continuation policy (recommended):** keep the current order only
   for its selected `$H$`. If it is unfilled, re-observe the state and solve
   the next window. The value is a Bellman continuation value

   \[
   V_n(x)=\max\{U_H(x),\;E[V_{n-1}(X_H)\mid\text{no fill} ]\},
   \]

   with a cancel/requote action at each boundary. This captures the chance
   that a sparse ETHJPY trade arrives several windows later without pretending
   that the original quote had a stationary 90-minute fill distribution.

The current implementation intentionally does not turn a 10/15/30-minute
Bernoulli estimate into a longer order lease. A safe extension should first
screen `$k=2,3$` continuation values on ETHJPY using non-overlapping,
strictly-prequential paths, then compare robust utility per hour, fill-proxy
rate, terminal markout, and tail drawdown. Only a positive holdout lower bound
can justify extending the horizon set or the order lifetime.
