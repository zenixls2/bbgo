# GammaCapture alpha screening — 2026-08-16

This note records the causal screening performed before changing the live
ETHJPY policy.  All comparisons used the corrected next-BBO production replay,
zero queue multiplier, the same `0.01024405 ETH` starting inventory, and the
same-inventory hold benchmark.  No live YAML or userspace service was changed.

## Contracts and replay invariants

- The selected Fast horizon owns price, probability, quantity, and order-life
  clocks.  A model update alone does not rewrite an already-resting order's
  reference horizon.
- An early replacement is admissible only after the transport floor and only
  when an uncertainty-adjusted candidate beats the active order.
- A BBO crossing and an aggregate trade at the same timestamp may not fill the
  same replay order twice.  `TestProductionMakerCrossingAndAggregateTradeCannotDoubleFill`
  now locks this invariant.

## Rejected candidates

### Early statistical realignment

On the 2026-08-08 00:00–12:00Z component window, the experimental path made
12,095 early evaluations and accepted zero.  PnL, fills, and drawdown were
identical to control.  The modeled keep time was therefore not the binding
constraint in this sample.  The option remains research-only and is not wired
to live configuration.

### Globally changing the model clock from 5m to 1m

The earlier paired screen improved the fixed rise segment, but worsened mixed
PnL from `-38.4986` to `-40.9457 JPY`.  A global 1m clock is rejected.  Any
future clock change must be state-dependent and must be screened independently.

### Removing the joint terminal-wealth gate

Disabling the joint distance/quantity model raised range quote uptime to 100%,
but generated three SELL fills and reduced range PnL from `12.0675` to
`11.5308 JPY`.  The gate is protecting against fee-negative turnover; it must
not be replaced by an unconditional base-Fast fallback.

### Path-utility preliminary horizon selection

The path-utility selector changed quote lifetime and uptime but produced no
additional range fills and no PnL improvement.  It is not enabled in the live
profile.

### Joint horizon–distance–quantity selection

The optimizer already supported a Cartesian search over `(H, distance,
quantity)`, but both live and replay callers omitted `HorizonCandidates`; the
feature silently collapsed to the preliminary single horizon.  The callers now
provide configured horizons only when `jointHorizonSelection` is explicitly
enabled.  The live ETHJPY setting remains disabled because the causal replay
failed promotion:

| regime | joint-H result / hold (JPY) | fills BUY / SELL | current result (JPY) | decision |
| --- | ---: | ---: | ---: | --- |
| decline, 2026-08-03 00:00–08:00Z | -78.0400 / -80.0265 | 0 / 1 | -75.6685 | reject |
| rise, 2026-08-05 15:00–21:00Z | +69.0565 / +68.7376 | 2 / 0 | +68.7376 | local gain |
| range, 2026-08-08 00:00–12:00Z | +12.0675 / +12.0675 | 0 / 0 | +12.0675 | no effect |
| mixed, 2026-08-10 00:00–18:00Z | -42.1190 / -36.8376 | 2 / 1 | -38.4986 | reject |

The engineering wiring is retained so a future isolated model can be tested
honestly, but the switch stays off.  A candidate cannot be promoted from one
favorable rise result when decline and mixed both regress.

## Next admissible research target

The diagnostics show that every 10m/15m/30m crossing window can be statistically
healthy while the joint whole-position certainty equivalent remains non-positive.
The next candidate should therefore model competing stopping outcomes directly:
profitable complementary completion, adverse terminal inventory, and expiry.
It should output one fee-net terminal-wealth score per side and horizon, with a
zero-order action in the same candidate set.  It must first pass synthetic and
single-window tests before any four-regime replay or live setting change.

## Competing-path posterior study

> 2026-08-17 re-audit: the fixed non-overlapping evaluation grid below was not
> the quote renewal clock. The expiry-or-crossing event-clock rerun remains
> negative at both horizons (15m day-block delta `-0.0167 bps`, lower
> `-0.0382`; 30m `-0.0359`, lower `-0.0892`). The rejection remains, but only
> the corrected values in `gammacapture-alpha-reaudit-2026-08-17.md` are
> authoritative.

The follow-up implementation audit first confirmed that the existing path
model already labels mutually exclusive BUY-only, SELL-only, both-touch, and
no-touch paths.  The remaining testable difference was therefore the outcome
posterior: estimate one four-way Dirichlet distribution directly, or reconstruct
the four categories from independently smoothed BUY, SELL, and both-touch
marginals.

The standalone study uses non-overlapping predictions beginning at the actual
observed BBO timestamp.  Labels mature after the full 15m primary horizon (30m
is the sole predeclared sensitivity).  BUY-only terminal wealth liquidates at
the future executable bid, SELL-only terminal wealth reacquires at the future
executable ask, both-touch earns the quote-to-quote cycle, and actual 10bps
maker cost is charged once per fill.  No order or inventory state feeds back
into the study.

Two study bugs were found and corrected before the final result:

- deduplicating unchanged BBOs destroyed the timestamp needed to distinguish a
  flat live book from a data gap, so the study now preserves raw observation
  times;
- maturity was initially measured from the scheduled anchor and SELL-only used
  terminal bid.  Maturity now starts at the actual prediction BBO, subsequent
  predictions are non-overlapping, and SELL reacquisition uses terminal ask.

Final ETHJPY prequential results for 2026-08-03--2026-08-16 UTC:

| horizon | effective N | competing / marginal Brier | competing / marginal log loss | competing / marginal value MAE (bps) | incremental mean / simultaneous lower (bps) | positive daily blocks |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| 15m | 1,233 | 0.576857 / 0.577460 | 1.039045 / 1.080464 | 8.356829 / 8.336535 | -0.020295 / -0.029729 | 0 / 13 |
| 30m | 619 | 0.702369 / 0.704097 | 1.270089 / 1.353169 | 11.676059 / 11.662541 | -0.013517 / -0.026653 | 0 / 13 |

The direct four-way posterior improves categorical calibration but makes the
fee-net terminal-value prediction slightly worse in every chronological block.
The frozen gate returns `REJECT_UNSTABLE`; this component must not replace the
current estimator or alter `fastCandidateValue`.  The result also shows that
the previous negative joint-horizon replay cannot be attributed merely to
reconstructing competing outcome probabilities from marginals.

## One-sided conditional-payoff shrinkage study

> 2026-08-17 re-audit: training every causal minute remains correct, but scoring
> must use the expiry-or-crossing renewal clock. Equal-day paired deltas are
> `-1.0006 bps` at 15m and `-0.4963 bps` at 30m; both simultaneous lower bounds
> are negative. The rejection remains for the corrected reason.

The next frozen hypothesis kept the existing marginal outcome probabilities
and changed only the BUY-only/SELL-only conditional payoff mean.  The baseline
was the production-scale exponentially weighted arithmetic mean with half-life
`sqrt(H * 6h)`.  The candidate added one zero-value pseudo-observation,
`mu_EB = n_eff/(n_eff+1) * mu`, treating no new order as the sparse-data prior.
Known both-touch cycle value and no-touch zero value were not estimated.

An initial non-overlapping-only training implementation appeared to improve
15m MAE by `0.4579 bps` and 30m MAE by `1.3612 bps`.  That result was invalid:
production trains from one-minute overlapping completed windows with fractional
weight `1m/H`.  The study was corrected to train every minute with that weight,
while retaining non-overlapping H-spaced evaluation timestamps.

Final ETHJPY prequential results for 2026-08-03--2026-08-16 UTC:

| horizon | one-sided effective N | half-life | raw / shrunk MAE (bps) | incremental mean / simultaneous lower (bps) | positive daily blocks |
| --- | ---: | ---: | ---: | ---: | ---: |
| 15m | 464 | 1h13m29s | 11.714727 / 11.749444 | -0.034717 / -0.501365 | 6 / 13 |
| 30m | 330 | 1h43m55s | 13.547418 / 12.955315 | +0.592103 / -0.226718 | 8 / 13 |

The primary 15m estimator is slightly worse and the 30m sensitivity does not
clear its simultaneous confidence bound.  The gate returns `REJECT_UNSTABLE`.
The production-aligned rerun demonstrates why the earlier positive result must
not be promoted.  No conditional-payoff shrinkage input is integrated into the
strategy.

## Side-specific BBO-imbalance payoff study

> 2026-08-17 correction: the earlier 15m component pass is withdrawn. On the
> quote renewal clock its equal-day delta is `-0.0093 bps` with lower bound
> `-0.0235`, only 3/14 positive days, and zero action disagreements. Existing
> narrow wiring was not changed by the research audit, but it must not be cited
> as statistically accepted alpha.

BUY-only and SELL-only payoff models were then trained separately with one
causal feature, top-of-book depth imbalance. BUY uses
`I=(Q_bid-Q_ask)/(Q_bid+Q_ask)` and SELL uses the reflected `-I`. Each side is
an EW ridge regression with unit prior precision; no bins, thresholds, fitted
half-life, or cross-ticker data are used. Outcome probabilities, known
both-touch cycle payoff, distance, fee, and clocks remain fixed.

Two attribution errors were corrected before accepting the standalone result:

- scoring only paths later known to be one-sided conditions on the future
  outcome and does not represent value available when an order is placed;
- comparing the decomposed candidate with a raw all-path EW mean changes both
  decomposition and imbalance at once. The final paired baseline uses the same
  marginal probabilities, known cycle value, and side-specific EW means; only
  the imbalance slope differs.

Final standalone results:

| horizon | effective N | baseline / conditional MAE (bps) | incremental mean / simultaneous lower (bps) | positive daily blocks |
| --- | ---: | ---: | ---: | ---: |
| 15m | 1,212 | 6.587945 / 6.560407 | +0.027538 / +0.009482 | 8 / 13 |
| 30m | 612 | 9.745866 / 9.745177 | +0.000689 / -0.021407 | 8 / 13 |

The frozen 15m primary study passes `PROMOTE_COMPONENT_REPLAY`; the 30m
sensitivity does not. A compact shadow action comparison then applied the
production identification floor, `makerFee * H / lookback` (`0.416667 bps` at
15m and `0.833333 bps` at 30m), while retaining the same first-passage labels.
The imbalance adjustment changed zero actions at either horizon. At 15m both
paths selected 109 actions; at 30m both selected five. Component incremental
value is therefore exactly zero.

This is a statistically identifiable calibration improvement but did not
change a component action under the then-current identification floor. At the
screening decision it was therefore not integrated. Multiplying it by an
arbitrary gain merely to cross the order lattice would invalidate the causal
contract and remains prohibited.

### Production integration and identification-floor correction

The original component replay used
`makerFeeBps * horizon / horizonLookback`, yielding `0.416667 bps` at 15m and
`0.833333 bps` at 30m. This is not a valid per-action economic threshold. Each
realized fill pays the full fee, which is already deducted in terminal path
wealth; an unfilled evaluation pays no fee. Dividing that fee by the number of
potential decisions in the lookback therefore neither represents execution
cost nor statistical uncertainty and double-counts cost when applied to the
fee-net posterior.

The corrected null action is zero fee-net, risk-adjusted incremental terminal
wealth. Candidate-search multiplicity remains controlled by paired confidence
bounds, and replacement churn remains controlled by the separate switching
cost. Replaying the standalone action comparison with a zero floor increased
the number of positive baseline actions (15m `109 -> 199`, 30m `5 -> 51`) but
the side-imbalance model still changed zero actions. Thus the floor was wrong,
but it was not the reason this small alpha failed to cross an action boundary.

At explicit operator request the statistically accepted 15m mechanism is
integrated narrowly into the 15m unified terminal-payoff estimator. The failed
30m sensitivity and untested 10m clock remain unconditional. Completed
BUY-only paths regress their terminal-liquidation payoff on start-book
imbalance; completed SELL-only paths use reflected imbalance. At decision time
only the corresponding one-sided payoff mean is adjusted by its empirical path
probability. Crossing probabilities, payoff variance, inventory target, quote
distance, and quantity remain unchanged, preventing the same depth observation
from becoming multiple independent controls. Missing live or historical depth
falls back exactly to the unconditional estimator. No fitted multiplier or
new YAML parameter is introduced.
