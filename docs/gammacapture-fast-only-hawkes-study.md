# ETHJPY fast-only / Hawkes study

The live ETHJPY profile now has `macroInventory.enabled: false`,
`noTradeRegion.enabled: false`, and Macro active execution disabled. The
long-window target, reversal lease, Macro IOC, and Macro BBO state are not part
of the quote decision. The inventory band remains centered at 50% risky asset
with 0%/100% hard capital bounds.

## Direction model

`HawkesDirectionModel` is an optional two-state marked Hawkes process. Public
buy/sell trades update exponentially decaying up/down excitation in O(1). The
direction supplied to the fast quote is

\[
d_t=(\lambda_\uparrow-\lambda_\downarrow)/(\lambda_\uparrow+\lambda_\downarrow),
\]

and the confidence is derived from event count and integrated intensity. The
45-second kernel is subcritical (`self=0.35`, `cross=0.05`). It is fused with
fast crossing direction only by their observed statistical confidence; no
fixed directional weight is used. Diagnostics expose both intensities.

## Current result

The first blind 12-hour ranging window (2026-07-23 18:00--2026-07-24 06:00
UTC, 6,808 JPY, exact 50/50 opening, queue=1, next-BBO, 10 bps maker fee)
does not pass the hold gate:

| Control | Net P&L | Maker fees | Full fills | Final risky weight |
|---|---:|---:|---:|---:|
| 50/50 hold | +6.42 JPY | 0 | — | 50.0% |
| fast, probability quantity + Hawkes | −11.02 JPY | 24.29 JPY | 11 | 10.9% |
| fast, probability quantity, no Hawkes | −9.16 JPY | 15.25 JPY | 6 | 25.5% |
| fast, staged quantity, no Hawkes | −2.00 JPY | 7.62 JPY | 4 | 37.9% |

The replay previously forced probability-centered quantity even when the
candidate configuration disabled it. That bypass is fixed; replay now uses
the candidate's `probabilityCenteredQuantity.enabled` and `shadowOnly` values.
The corrected staged-quantity control is materially better, but still does not
beat hold in the first window. The Hawkes fusion is currently rejected as a
production change because it worsens this control.

No live restart or binary deployment was performed. The next required change
is to solve fee-positive fast execution and realized inventory drift, then
rerun the six blind ETHJPY ranging windows. Macro rebalancing remains deferred.
