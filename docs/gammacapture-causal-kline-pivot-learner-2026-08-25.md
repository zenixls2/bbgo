# Causal Kline pivot learner — 2026-08-25

## Purpose

The production `PivotRegimeFilter` is an event-driven directional-change
detector. It is not a Kline fractal and it is not the source of truth for a
learned pivot tag. This component adds the causal learning boundary needed for
a 3-minute Kline pivot experiment without changing production quote, cancel,
or inventory policy.

## Causal contract

- Primary type: direction/regime-tag feature.
- Baseline: the existing `PivotRegimeFilter` and a neutral/no-tag forecast.
- Prediction time: the close of the current completed Kline.
- Features: only the current and previous completed bars; the feature vector
  contains normalized return, multi-bar return, range, body, wick, and close
  location. No next-bar field is used.
- Label: three-bar fractal class for the current bar: HIGH, LOW, or NEUTRAL.
- Label maturity: the following Kline close. The previous bar is classified
  only after the current bar is available.
- Update order: mature the previous pending sample, update the online
  classifier, then emit the current-bar forecast and store its feature vector.
- Gap rule: a missing bar or explicit `GapBefore` clears the pending sample and
  feature path. Missing bars are never synthesized.
- Output: three probabilities plus a derived signed direction. The component
  does not submit orders, change price offsets, or cancel quotes.
- Future integration point: one inventory-target/quantity scalar only, after
  causal replay and private-fill calibration pass.

## Label geometry

For bars `t-1`, `t`, and `t+1`, a HIGH is confirmed when

```text
high[t] > high[t-1] && high[t] >= high[t+1]
```

and a LOW is confirmed when

```text
low[t] < low[t-1] && low[t] <= low[t+1]
```

The pivot timestamp is the middle bar's close time; the usable confirmation
timestamp is the next bar's close time. If both conditions hold in one bar,
the label is ambiguous and is skipped rather than resolved with hindsight.

## Restart and replay

`CausalKlinePivotSnapshot` stores the closed-bar history, classifier weights,
and the pending feature vector. A restart therefore preserves the exact
prediction-to-label pairing. Replay must process bars chronologically and must
not restore a live checkpoint into a research run.

## Current status

This is an isolated component with focused tests. It is not enabled by the
live ETHJPY YAML and is not wired into the quote lifecycle. The next permitted
step is a standalone same-symbol causal replay comparing 3m/3-bar, 3m/5-bar,
5m/3-bar, and the existing baseline with executable bid/ask, fees, block
stability, drawdown, adverse selection, and private-fill calibration.
