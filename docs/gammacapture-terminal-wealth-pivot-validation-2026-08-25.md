# Terminal wealth versus pivot regime validation

Date: 2026-08-26

Status: the target-relative CE correction is active in source. The live service
was not rebuilt or restarted; no live YAML or checkpoint was changed by this
task.

## Question

The terminal-wealth model is a profit-structure filter. The pivot regime is a
directional-change model whose label is the next completed pivot leg. The
study asks whether the two models estimate the same object, and whether the
terminal filter rejects a direction that later produces a positive
pivot-to-pivot executable outcome.

## Causal design

At every non-overlapping 15-minute anchor:

1. The pivot filter observes only the executable-BBO midpoint up to the
   anchor. A 26 bps reversal confirms a pivot only after the reversal is
   observed.
2. The terminal model observes only completed BBO paths before the anchor.
   Its one-sided score uses the configured 15-minute or 30-minute quote
   distance and `EvaluateTargetRelativePosition` with inventory equal to the
   target. This is a profit-only certainty-equivalent proxy for the live
   terminal filter, not a private-fill replay.
3. The matured label is the first subsequent confirmation of the currently
   active pivot direction. The return uses the executable BBO at the pivot
   extreme: BUY starts at ask and ends at bid; SELL starts at bid and ends at
   ask. The configured one-fill entry cost is 12 bps (10 bps maker fee plus 2
   bps adverse selection).
4. A separate fixed-horizon label is reported. It is not substituted for the
   pivot-to-pivot label.

The source code is [terminal_wealth_pivot_study.go](/home/zenixls2/src/bbgo/cmd/gammacapture-mm-research/terminal_wealth_pivot_study.go).
The command is enabled by `--terminal-wealth-pivot-study` in
[main.go](/home/zenixls2/src/bbgo/cmd/gammacapture-mm-research/main.go).

## Results

Full interval: 2026-07-24 through 2026-08-25, ETHJPY, 3,072 anchors, 1,682
confirmed pivots. The full run used the last executable BBO per 5 seconds for
runtime sensitivity; a 1-second model-clock replay was run independently on
the final 2026-08-23 through 2026-08-25 holdout.

| Model horizon | Split | Ready / resolved | Terminal mature | Terminal accepted | Pivot positive | Mean pivot when terminal rejected | Mean terminal CE |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 15m | full-run holdout, 8 days | 768 / 768 | 703 | 0 | 546 (71.1%) | +16.65 bps | −13.46 bps |
| 30m | full-run holdout, 8 days | 768 / 768 | 410 | 0 | 546 (71.1%) | +16.65 bps | −24.09 bps |
| 15m | 1s replay, final 2 days | 192 / 192 | 183 | 0 | 130 (67.7%) | +16.09 bps | −13.56 bps |
| 30m | 1s replay, final 2 days | 192 / 192 | 66 | 0 | 130 (67.7%) | +16.09 bps | −22.47 bps |

For the 5-second full-run holdout, all 32 six-hour blocks had positive mean
next-pivot outcome. This is a block-level stability result, not a claim that
the strategy would fill profitably: the study has no private queue position.
The 1-second sensitivity gives the same qualitative result, so the conflict
is not caused by coarse BBO retention.

The terminal sign classifier was effectively an always-reject classifier:
balanced accuracy was 0.50 and the false-negative count was 546 / 768 in both
15m and 30m holdout comparisons. The fixed 15m/30m markout was negative on
most samples, which is consistent with the terminal model's own negative
estimate; it is not the same label as the eventual pivot leg.

## Interpretation

There is no mathematical contradiction if the models own different
objectives:

- terminal wealth asks whether a passive one-fill action has positive
  fee/adverse-selection-adjusted wealth over a fixed short horizon;
- pivot regime asks whether the current directional leg eventually reaches
  its next confirmed extreme.

There is an economic decision conflict in the current architecture when the
terminal score is used as a universal gate. In this sample, the short-horizon
terminal filter rejects every pivot-direction action even though roughly
two-thirds to three-quarters of those pivot legs have positive eventual
executable value. The filter is therefore unsuitable as the sole gate for a
long-horizon pivot-following or inventory-adjustment action.

The result does **not** prove that every rejected order should be sent. The
positive pivot label can occur after a period of adverse markout, and it does
not contain queue fill probability. It proves that the current fixed-horizon
terminal score is not an accurate predictor of the next pivot-to-pivot
directional payoff.

## Inventory-adjustment implication

An inventory trim is not the same decision as an alpha trade. A SELL above
target during an upward pivot can have negative profit-only markout and still
be useful for reducing inventory variance. It should be evaluated by the
incremental target-relative utility

\[
\Delta CE(q)=CE(W_{t}+\text{candidate}(q), target)
           -CE(W_{t}, target),
\]

not by `CE(candidate) > 0` alone. The current study's synthetic 75/25 versus
50/50 sensitivity did not produce positive risk-relief CE in this interval;
that is evidence that the existing covariance estimate did not rescue this
case, not evidence that risk reduction is economically worthless for every
actual account state.

The safe structural correction is to keep two separate paths:

1. profit/continuation quotes: require the terminal wealth posterior to be
   mature and fee-positive;
2. target repair: allow a continuous quantity only when the measured
   target-relative delta-CE is positive, with hard inventory bounds and a
   maximum risk budget. A negative profit-only score may be allowed only when
   the risk covariance benefit offsets it. If no positive delta-CE quantity
   exists, the model should not manufacture a passive order; an explicit
   risk-repair execution path must own that decision.

The next research step is to fit a pivot-conditioned survival mixture,
`CE_mix = p_survive * CE_long + (1-p_survive) * CE_short`, and to solve the
continuous quantity root of `DeltaCE(q) >= 0` separately for alpha and target
repair. This study does not promote that change to production.

## Delta-CE case check (original implementation)

The following results use the original implementation: its Kelly term was
target-relative, but its lower-confidence term was the standard error of the
incremental order payoff only. The formula audit below revises the
interpretation of these numbers.

The final two-day holdout was used for concrete examples. Only mature
terminal paths that were rejected by profit-only CE and later had a positive
next pivot outcome were retained. The default counterfactual cell was a
one-quote-notional order with inventory deviation equal to 25% of pair equity;
the grid also tested 0/25/50% inventory deviation and 0.5/1/2 quote notional.

| Horizon | Eligible false negatives | Mean target-relative improvement | SE | Rescued at default cell | Rescued anywhere in grid |
| --- | ---: | ---: | ---: | ---: | ---: |
| 15m | 26 | +0.0149 bps | 0.0015 bps | 0 / 26 | 0 / 26 |
| 30m | 12 | +0.0291 bps | 0.0049 bps | 0 / 12 | 0 / 12 |

On the full 8-day holdout from the 32-day 5-second replay, the result is
similar with enough chronological blocks for the early statistical gate:

| Horizon | Eligible false negatives | Mean improvement | SE | Positive blocks | Rescued anywhere in grid |
| --- | ---: | ---: | ---: | ---: | ---: |
| 15m | 500 | +0.0202 bps | 0.0020 bps | 32 / 32 | 0 / 500 |
| 30m | 290 | +0.0404 bps | 0.0071 bps | 29 / 29 | 0 / 290 |

Three representative 15m examples:

| Anchor (UTC) | Pivot direction | Next pivot outcome | Alpha CE | Risk-only CE at D=0 | Delta CE at D=25% |
| --- | ---: | ---: | ---: | ---: | ---: |
| 2026-08-24 23:15:00 | BUY | +28.34 bps | −10.29 bps | −8.495 bps | −8.490 bps |
| 2026-08-24 13:30:00 | SELL | +63.10 bps | −21.18 bps | −8.944 bps | −8.936 bps |
| 2026-08-24 12:00:01 | SELL | +60.14 bps | −21.46 bps | −9.028 bps | −9.020 bps |

Examples with the largest target-relative relief in the full holdout were:

| Anchor (UTC) | Horizon | Pivot outcome | Alpha CE | Risk-only CE at D=0 | Delta CE at D=25% | Improvement |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| 2026-08-17 23:45:03 | 15m | +89.93 bps | −4.95 bps | −1.0111 bps | −1.0110 bps | +0.0002 bps |
| 2026-08-19 21:30:04 | 15m | +16.90 bps | −15.10 bps | −86.594 bps | −86.241 bps | +0.353 bps |
| 2026-08-19 21:45:04 | 30m | +75.10 bps | −27.53 bps | −176.864 bps | −175.912 bps | +0.952 bps |

Even the largest improvement remains negative. The risk-relative adjustment
reduces the loss but does not authorize the order.

The original delta CE has the correct sign as a risk adjustment—it improves
the certainty equivalent slightly when the position is away from target—but
it does not cross zero. This conclusion applies only to the original
incremental-SE formula.

## CE formula audit: omitted baseline confidence difference

The current production function is
`JointPathPayoffStats.EvaluateTargetRelativePosition` in
[joint_path_payoff.go](/home/zenixls2/src/bbgo/pkg/strategy/gammacapture/joint_path_payoff.go).
Its quadratic risk term is already a difference:

\[
  \Delta V = V_{whole} - V_{baseline}.
\]

However, its lower-bound term is currently

\[
  \mu_\Delta - z\sqrt{V_\Delta/N},
\]

which is the lower bound of the *incremental payoff*, not the difference of
the candidate and no-order lower bounds. If CE is intended to mean
candidate-versus-no-order terminal CE, let \(Y\) be the existing
target-relative inventory wealth and \(X\) the candidate order payoff. The
consistent delta is

\[
\Delta CE = E[X]
 - z\left(\sqrt{\frac{Var(Y+X)}{N}}
          -\sqrt{\frac{Var(Y)}{N}}\right)
 - \frac{\gamma}{2W}\left(Var(Y+X)-Var(Y)\right).
\]

Therefore the missing correction to the current CE is

\[
z\left(\sqrt{\frac{Var(X)}{N}}
       -\sqrt{\frac{Var(Y+X)}{N}}
       +\sqrt{\frac{Var(Y)}{N}}\right).
\]

It is zero at target (\(Var(Y)=0\)), but can be materially positive for a
risk-reducing inventory repair. The research command now calculates both
forms without changing production behavior in
[terminal_wealth_pivot_study.go](/home/zenixls2/src/bbgo/cmd/gammacapture-mm-research/terminal_wealth_pivot_study.go).

The full 5-second replay gives the following paired result. “Eligible” means
mature terminal paths rejected by profit-only CE and later followed by a
positive next-pivot executable outcome; it is a diagnostic false-negative set,
not an unbiased deployment sample.

| Horizon | Eligible | Original relief | Corrected relief | Mean missing correction | Corrected accepted at default cell | Corrected rescue anywhere |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| 15m | 500 | +0.0202 bps | +11.2591 bps | +11.2389 bps | 64 / 500 | 65 / 500 |
| 30m | 290 | +0.0404 bps | +19.7758 bps | +19.7354 bps | 93 / 290 | 93 / 290 |

All 32 available 15m blocks and 29 available 30m blocks had positive default
relief under both calculations. On the independent final two-day 1-second
sensitivity, the corrected default-cell acceptance counts were 3 / 26 for 15m
and 8 / 12 for 30m, versus 0 under the original formula.

This confirms a formula-level downward shift in the current CE when the action
reduces baseline inventory uncertainty. It does not by itself authorize a
production change: the corrected term may be too generous if the intended
objective is specifically an uncertainty penalty on incremental order payoff;
the objective contract must first choose between those two definitions. In
addition, this screen still has synthetic inventory states and public-BBO
markouts, with no private queue/fill calibration. The next safe step is a
shadow component replay with private-fill calibration and a predeclared
accepted/rescued gate before changing the production function.

## Implementation and integrated replay

The tested CE revision used the target-relative lower-bound difference in
[joint_path_payoff.go](/home/zenixls2/src/bbgo/pkg/strategy/gammacapture/joint_path_payoff.go):

\[
  LowerPnL = E[\Delta W]
    - z\left(SE_{whole}-SE_{baseline}\right),
  \qquad CE = LowerPnL - \frac{\gamma}{2W}\Delta V.
\]

The implementation exposes baseline/whole/incremental SE fields and preserves
the incremental `StdErrorJPY` field for diagnostics and legacy callers. The
current source uses the corrected whole-minus-baseline lower bound; the
integrated replay numbers below are historical engineering results from the
same formula, not a live deployment result.

The complete 32-day terminal/pivot replay after the implementation produced
the same fixed old-formula false-negative cohort and the corrected acceptance
counts above: 64/500 at the 15m default cell and 93/290 at 30m. This confirms
the integrated function is using the intended new term rather than changing
the sample selection.

An additional 48-hour integrated production-policy replay was run with
ETHJPY, 10-second BBO buckets, queue multiplier zero, and no private-fill
calibration:

| Arm | Fills | Net PnL JPY | Hold PnL JPY | Excess vs hold | Max drawdown | Quote refreshes |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| current CE / legacy policy | 172 | −136.75 | −99.03 | −37.72 | 4.094% | 371 |
| configured horizon-touch arm | 150 | −149.73 | −94.56 | −55.17 | 4.225% | 363 |

Private-fill calibration failed, so this integrated replay was only an
engineering behavior check. It was not promoted, and the tested CE did not
make the overall strategy profitable versus Hold in this interval.

The two-day case-study gate is recorded in
[gammacapture-terminal-wealth-delta-ce-experiment-2026-08-25.json](/home/zenixls2/src/bbgo/docs/gammacapture-terminal-wealth-delta-ce-experiment-2026-08-25.json)
and returns `INCONCLUSIVE_SAMPLE`: the two-day case study has only two
effective six-hour blocks, below the predeclared 24-block minimum.

The full-holdout manifest is recorded in
[gammacapture-terminal-wealth-delta-ce-experiment-full-2026-08-25.json](/home/zenixls2/src/bbgo/docs/gammacapture-terminal-wealth-delta-ce-experiment-full-2026-08-25.json).
Its generic early gate returns `PROMOTE_COMPONENT_REPLAY` because the mean
block improvement is positive. This only permits a narrowly isolated shadow
component replay under the original formula; its original decision-level
rescue rate is zero, so it does not authorize production promotion or gate
relaxation. The formula-audit results are recorded separately in
[gammacapture-terminal-wealth-ce-formula-audit-2026-08-26.json](/home/zenixls2/src/bbgo/docs/gammacapture-terminal-wealth-ce-formula-audit-2026-08-26.json).

## Reproduction

```text
go run ./cmd/gammacapture-mm-research \
  --terminal-wealth-pivot-study \
  --config config/gammacapture-ethjpy.yaml \
  --symbol ETHJPY --bbo-data data/gammacapture \
  --replay-from 2026-07-24T00:00:00Z \
  --replay-to 2026-08-25T00:00:00Z \
  --terminal-wealth-pivot-horizons 15m,30m \
  --terminal-wealth-pivot-bbo-interval 5s
```

The 1-second sensitivity uses the same command over 2026-08-17 through
2026-08-25 with `--terminal-wealth-pivot-bbo-interval 1s`.
