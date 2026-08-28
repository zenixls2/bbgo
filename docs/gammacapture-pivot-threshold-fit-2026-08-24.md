# GammaCapture pivot-based threshold fit — 2026-08-24

This is a research-only study. It does not change `strategy.go`, the live YAML,
or the userspace service.

## Method

The existing raw regime score is recomputed causally every five minutes from
the 30-minute normalized slow return and the five-minute normalized fast
return:

`rawTag = 0.5 * (slowScore + fastScore)`.

For each anchor, the label is the first executable-BBO barrier reached within
three hours. The upward barrier uses `log(futureBid / currentAsk)` and the
downward barrier uses `-log(futureAsk / currentBid)`. A missing BBO path is
censored. The pivot scale is tested at 26 bps, the current economic scale of
20 bps round-trip cost plus 4 bps adverse selection plus 2 bps residual edge.

The training split fits a one-dimensional logistic model:

`P(correct pivot direction | |rawTag| = x) = sigmoid(a + b*x)`.

For a symmetric pivot of size `A` and cost `C`, the fee break-even probability
is:

`p_be = (A + C) / (2*A)`.

The fitted threshold is the smallest `x` whose fitted probability reaches
`p_be`. The final 2026-08-22 through 2026-08-24 interval is kept as an
untouched holdout.

## Results

Split: 2026-07-23 through 2026-08-24; training through 2026-08-17;
validation through 2026-08-22; holdout 2026-08-22 through 2026-08-24.

| Pivot scale | Training fitted slope | Fitted threshold | Break-even probability | Holdout result at 0.35 |
| ---: | ---: | ---: | ---: | ---: |
| 26 bps | −0.0059 | > 1.00 (none) | 88.46% | 48.94% precision, −20.55 bps |
| 50 bps | +0.0212 | > 1.00 (none) | 70.00% | 52.86% precision, −15.47 bps |

At the 26 bps economic pivot, the fitted probability at `|rawTag|=0.35`
was 49.52%, producing an expected net value of approximately −20.25 bps.
At the 50 bps pivot it was approximately 50.01%, still far below the 70%
required break-even probability.

The validation-selected threshold was 0.25 for the 50 bps experiment, but it
still had only 47.62% precision and −15.13 bps mean net pivot value. The
validation-selected threshold is therefore not promoted to production.

## Conclusion

The current data do **not** support `enterThreshold=0.35` as an economically
fee-positive pivot detector. Increasing the threshold does not create the
required probability separation; the score magnitude is almost flat or
slightly adverse with respect to the next executable pivot direction.

This does not prove that the raw score is useless for inventory control. It
does show that `0.35` must not be interpreted as a calibrated probability or
as a fee-cleared trade gate. If retained for research, it should be used only
as a bounded state-strength input to a continuous EV/risk sizing function.
The next model revision should fit pivot direction and remaining amplitude
separately, rather than treating the sign of the next first-passage pivot as
the whole target.
