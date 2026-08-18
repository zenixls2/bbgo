# GammaCapture rejected-alpha re-audit — 2026-08-17

## Why the earlier classifications were reopened

Several standalone studies trained from causally matured one-minute windows,
but their evaluation clock was a fixed non-overlapping grid. That is not the
strategy's action clock. A resting Fast quote is re-based only after its
selected horizon expires or after the first observable crossing event; the
crossing BBO belongs to the old order, and the next observable BBO is the first
possible replacement decision.

Training may still consume every causally matured minute with overlap weight
`1m/H`. Alpha scoring, confidence intervals, and action comparisons may not
pretend that every such training observation was an independently available
order decision. The corrected gate uses UTC days as paired independent blocks,
not the correlated lifecycle evaluations inside each day.

No live YAML, binary, service, or order path was changed by this re-audit.

## Corrected common experiment

- Symbol: ETHJPY only.
- Archive: 2026-08-03 00:00 through 2026-08-17 00:00 UTC.
- Fast horizons: predeclared 15m primary and 30m sensitivity.
- Quote distance: production-configured 15 bps.
- Fee: 10 bps for each realized maker fill.
- Training: one-minute causal completed paths with weight `1m/H` where the
  production model has that update law.
- Evaluation: expiry-or-first-crossing renewal clock.
- Label availability: prediction is recorded before the corresponding path is
  allowed to update the model.
- Uncertainty: equal-weight UTC-day paired mean and standard error; the two
  predeclared horizons use the existing simultaneous 95% lower bound.

## Recomputed candidates

| candidate | H | lifecycle evaluations | independent days | paired day mean | simultaneous lower | positive days | revised decision |
|---|---:|---:|---:|---:|---:|---:|---|
| direct four-way competing posterior vs reconstructed marginals | 15m | 1,852 | 14 | -0.0167 bps | -0.0382 bps | 0/14 | reject |
| direct four-way competing posterior vs reconstructed marginals | 30m | 1,370 | 14 | -0.0359 bps | -0.0892 bps | 0/14 | reject |
| one-sided payoff pseudo-count shrinkage | 15m | 850 one-sided | 14 | -1.0006 bps | -2.0582 bps | 5/14 | reject unstable |
| one-sided payoff pseudo-count shrinkage | 30m | 823 one-sided | 14 | -0.4963 bps | -1.5565 bps | 5/14 | reject unstable |
| side-specific depth-imbalance payoff regression | 15m | 1,852 | 14 | -0.0093 bps | -0.0235 bps | 3/14 | withdraw earlier component pass |
| side-specific depth-imbalance payoff regression | 30m | 1,370 | 14 | +0.0035 bps | -0.0071 bps | 9/14 | reject unstable |
| terminal-tail residual target | 15m | 149 complete minute paths | 14 | -0.2432 bps | -0.7408 bps | 4/14 | reject under old contract |
| terminal-tail residual target | 30m | 93 complete minute paths | 14 | -1.1128 bps | -2.1474 bps | 5/14 | reject under old contract |

The direct competing posterior still improves categorical Brier/log loss, but
it makes terminal-value MAE worse on every day. Better category calibration is
therefore not economic alpha by itself.

The 15m depth-imbalance result is the material correction. Its earlier fixed-
grid MAE improvement does not survive lifecycle sampling. At a zero fee-net
action threshold the conditional and unconditional models also select exactly
the same 270 actions, so the feature contributes zero action-level value in
this archive. Its narrow production wiring must not be cited as accepted
alpha; removing it is a separate production change and was not performed by
this research pass.

## Candidates whose status is not decided by this rerun

### Volume Profile / POC

The already-corrected event-clock experiment remains the authoritative result:
the joint symmetric candidate is rejected. At 15m, BUY-only MAE improved from
9.2588 to 9.0790 bps while SELL worsened from 9.0498 to 9.5245 bps. This is a
new side-specific hypothesis discovered on the inspected holdout, not a pass.
It may be frozen and tested only on data arriving after 2026-08-17; tuning or
promoting it on the same archive would be selection bias.

### Terminal target

The old terminal-tail candidate remains negative under its own contract, but
that contract is now obsolete: it uses one depth-weighted microprice path and
a final-five-minute target. The current model requires separate executable
BUY/SELL BBO paths and a post-fill probability-weighted mean over the actual
quote lifecycle, potentially spanning several Fast windows. Therefore this
result rejects only the old tail-residual alpha. It does not reject a newly
frozen side-specific post-fill terminal-target model.

### Production-replay ablations

Rejections produced by the next-BBO production replay did not assume a
one-minute action clock and remain valid for the tested code/configuration:

- globally changing the model clock from 5m to 1m;
- removing the joint terminal-wealth gate unconditionally;
- joint horizon/distance/quantity selection as then scaled;
- the one-cell symmetric horizon action selector;
- early statistical realignment when it accepted zero early replacements.

These are policy ablations, not timeless claims that their underlying feature
can never help. The old Hawkes replay, for example, used a retired Fast-only
quantity architecture. It remains a failed historical deployment candidate,
but a new Hawkes terminal-payoff feature would require a new frozen contract
and unseen data rather than inheriting that verdict.

### Standalone sequential and regime signals

- The downside e-process generated only one alarm and it resolved in the wrong
  direction. Its alarm clock is itself an observable event, so the minute-grid
  defect does not rescue it; the configured detector remains too sparse.
- The price-only multiscale BOCPD/regime models had negative Brier skill and
  worse log loss at 15m, 30m, 60m, and 180m. Those were prediction tests rather
  than hypothetical per-minute order decisions, so their directional-alpha
  rejection remains intact.
- HAR is a variance/risk forecast, not signed return alpha. Its rejected
  controller ablations cannot be reclassified by changing the quote clock.

## Resulting research queue

1. Keep all production-replay rejection decisions unchanged.
2. Treat 15m side-imbalance as unproven/zero-action, not promoted alpha.
3. Do not revive pseudo-count shrinkage or the direct competing posterior;
   their corrected day-block results are negative.
4. Freeze two genuinely new contracts for future unseen data only:
   side-specific BUY Volume Profile and side-specific post-fill terminal BBO
   mean. Neither may change production before a positive paired day-block lower
   bound and a non-zero action-level improvement.
