# GammaCapture VP alpha variants — standalone screening (2026-08-17)

## Scope

This study implements three removable variants behind one research-command
flag, `--alpha-variants-study`. They do not call order submission, balances,
strategy state, YAML, or systemd. Each model uses the same ETHJPY public BBO
and aggregate-trade archive, the same causal event clock, 15m primary horizon,
and 30m sensitivity horizon.

The archive from 2026-08-01 through 2026-08-16 has already been inspected in
earlier VP studies. It is retained for implementation and calibration checks,
not as an unseen promotion holdout. The 2026-08-17 capture is only a partial
day and is not yet sufficient for a new block-level acceptance test.

## Variants and single integration points

### C — maker/IOC lifecycle action value

The response is fee-net terminal executable wealth for four mutually exclusive
actions: maker BUY, IOC BUY, maker SELL, and IOC SELL. IOC is represented by
the same causal terminal outcome with the maker distance removed. The model
selects the action with the largest positive predicted value; zero is wait.
The baseline uses the current BBO state features. The candidate adds the frozen
nine-dimensional VP feature prefix. This is a maker/IOC execution alpha, not a
direction or inventory multiplier.

### A — Fast terminal-variance residual

The causal terminal mean is estimated first from BBO path features. The
baseline variance is a positive log-variance model; the candidate is nested as

\[
  v_{F+VP,t,H}=v_{F,t,H}\exp\{g_H(z^{VP}_t)\}.
\]

The response is the squared BUY/SELL executable-terminal residual, scored with
Gaussian QLIKE. VP cannot change the mean, quote distance, quantity, or gate
in this screen. The runner had an initial warmup defect in which the baseline
mean was not updated while cold; that was fixed before the reported replay.

### B — Macro side-HAR residual

BUY/ask and SELL/bid one-minute realized variance are kept separate. The
candidate is

\[
  v_{s,HAR+VP,t}=v_{s,HAR,t}\exp\{g_{s,H}(z^{VP}_t)\},
  \qquad s\in\{BUY,SELL\}.
\]

HAR is used only as a variance forecast; it cannot create signed expected
return. The response is side-average QLIKE improvement. Macro is currently
disabled in live ETHJPY configuration, so this variant remains research-only.

## ETHJPY results

The multiplicity-adjusted alpha manifests use six tried cells (three variants
and two horizons), 16 UTC day blocks, and a one-sided critical value of
2.39398. QLIKE values are dimensionless; the manifest preserves the common
screening field name and records the response unit explicitly.

| variant | horizon | effective samples | mean increment | SE | multiplicity-adjusted lower | result |
|---|---:|---:|---:|---:|---:|---|
| maker/IOC lifecycle | 15m | 1,834 | -0.2923 bps | 0.2217 | -0.8231 bps | reject |
| maker/IOC lifecycle | 30m | 1,374 | -0.0291 bps | 0.1251 | -0.3284 bps | reject |
| Fast terminal variance residual | 15m | 1,805 | -11.4038 QLIKE | 5.2995 | -24.09 | reject |
| Fast terminal variance residual | 30m | 1,340 | -4.8175 QLIKE | 1.5766 | -8.59 | reject |
| Macro HAR residual | 15m | 1,379 | +0.0312 QLIKE | 0.0628 | -0.1191 | reject |
| Macro HAR residual | 30m | 635 | +0.0708 QLIKE | 0.0914 | -0.1478 | reject |

The maker/IOC model changed 169 actions at 15m and 308 at 30m, but the
changes were negative on average. The Fast and Macro variants do not yet show
positive, stable lower bounds; the small positive Macro means are not evidence
of promotion.

## Gate decision

All three variants are `REJECT_NO_INCREMENTAL_VALUE` after multiplicity and
chronological block uncertainty. No component replay, strategy integration,
YAML change, binary build, or service restart is permitted from this study.

Reproduction:

```bash
go run ./cmd/gammacapture-mm-research \
  --alpha-variants-study --symbol ETHJPY \
  --bbo-data data/gammacapture \
  --config config/gammacapture-ethjpy.yaml \
  --replay-cache-dir data/gammacapture/state/replay-cache \
  --replay-from 2026-08-01T00:00:00Z \
  --replay-to 2026-08-17T00:00:00Z
```

The three gate manifests are:

- `gammacapture-alpha-maker-ioc-2026-08-17.json`
- `gammacapture-alpha-fast-variance-2026-08-17.json`
- `gammacapture-alpha-macro-har-2026-08-17.json`

Focused alpha tests, the full research-command package, and the
`pkg/strategy/gammacapture` package all pass. Removing `alpha_variants.go`, its
test, the research flag, and the three manifests restores the prior production
behavior.
