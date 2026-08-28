# GammaCapture fee-free counterfactual

## Question

The null is: after removing explicit exchange trading fees, the current
strategy still does not have positive excess wealth versus buy-and-hold. The
primary benchmark is the same starting quote/base account marked at the
terminal executable BBO. The replay uses the captured ETHJPY BBO/trade stream,
2026-08-22 00:00 UTC through 2026-08-24 00:00 UTC, 10-second BBO buckets, the
same causal warmup and queue multiplier zero.

For signed trades (u_j), where (u_j>0) is a buy and (p_j) is its execution
price, the exact strategy-minus-hold decomposition is

\[
  W_T^{strategy}-W_T^{hold}
  = \sum_j u_j(S_T-p_j)-F_T,
\]

where (S_T) is the terminal mark and (F_T) is explicit exchange fees. The
first term includes spread capture, timing, adverse selection and inventory
path effects. Adverse selection must not be subtracted again if it is already
measured through terminal wealth; it is a diagnostic decomposition, not a
second accounting charge.

## Two counterfactuals

1. `accountingFeeFreeGross`: preserve the fee-aware quote policy and remove
   recorded maker/taker fees algebraically from the same realized path. This
   isolates fee drag but does not recompute a fee-free equity drawdown path.
2. `policyFeeFree`: set maker/taker fee to numerical epsilon and remove the
   explicit maker-fee component from the minimum half-spread. Adverse selection
   and the configured minimum net edge remain. This tests a policy that would
   actually react to a zero-fee venue.

## Two-day replay

| Policy | PnL JPY | Hold PnL JPY | Excess vs hold | Max DD | Round trips/day | 1m markout | 5m markout | 10m markout |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| fee-aware baseline | -167.82 | -84.92 | -82.89 | 4.196% | 38.5 | -12.80 bps | -3.61 bps | -8.08 bps |
| accounting fee-free | -128.47 | -84.92 | -43.55 | same path DD* | 38.5 | -12.80 bps | -3.61 bps | -8.08 bps |
| policy fee-free | -157.97 | -84.92 | -73.04 | 3.973% | 103.5 | -5.28 bps | -3.88 bps | -3.48 bps |

\* The accounting-only row removes fees from PnL attribution but deliberately
does not relabel the fee-aware equity path's drawdown.

The fee-free accounting result remains below hold by 43.55 JPY. Removing the
fee floor increases fills from 168 to 437 and round trips/day from 38.5 to
103.5, but PnL remains below hold by 73.04 JPY. The 1-minute and 10-minute
markouts improve, while the 5-minute markout remains negative. This is
evidence that the dominant problem is not just fee drag; it is the conditional
quality of fills, inventory path and adverse selection.

Private-fill calibration is still unavailable. With actual private fills set
to zero, the two-day replay has 166 absolute side-fill errors, so this result
is a public-data counterfactual and not a promotion result.

## Where fees should enter

There are three distinct layers:

1. **Signal estimation:** estimate gross executable return or order-flow
   information without embedding the current fee schedule in the feature
   label. This answers whether the signal has information.
2. **Action and portfolio control:** apply fee, spread, rebate, adverse
   selection, impact, queue uncertainty and inventory risk to the action value.
   This answers whether quoting or trading is worthwhile now.
3. **Acceptance and replay accounting:** subtract realized fees exactly and
   compare net wealth, excess versus hold, Sharpe, drawdown and markout.

The GammaCapture implementation already applies fees in layers 2 and 3: the
minimum half-spread, fee-adjusted horizon edge, quote net-mark edge and replay
fill accounting. Its raw BBO/trade evidence remains separate. The important
research correction is therefore not “remove fees from the strategy”; it is to
maintain a fee-free gross diagnostic beside the fee-net policy objective, so a
failed strategy can be diagnosed as signal failure, adverse selection, or fee
failure rather than all three being mixed together.

The replay-only entry point is:

```text
go run ./cmd/gammacapture-mm-research --fee-free-counterfactual \
  --symbol ETHJPY --bbo-data data/gammacapture/ETHJPY \
  --replay-from 2026-08-22T00:00:00Z --replay-to 2026-08-24T00:00:00Z \
  --pair-equity-jpy 6830.672313565 --starting-base 0.01024405 \
  --queue-multiplier 0 --actual-buy-fills 0 --actual-sell-fills 0 \
  --replay-cache-dir data/gammacapture/state/replay-cache \
  --replay-bbo-interval 10s
```
