# Dynamic price-beta target: chase-state screening (2026-08-21)

## Contract

- **Primary type:** inventory risk / target actuator.
- **Baseline:** current `DynamicInventoryAim` with no `PriceBetaTarget` cap.
- **Single integration point:** `DynamicInventoryAimConfig.PriceBetaTarget`,
  which may later feed `AdjustedTargetRatio`.
- **Null:** `PriceBetaTarget=0`, meaning no additional cap.
- **Causal state:** current inventory ratio, policy target, hard inventory
  bounds, and the existing same-symbol executable-BBO inventory-return
  estimate and predictive variance.
- **Primary hypothesis:** a low beta cap is useful only when the account is
  already overweight relative to policy and positive executable-return
  evidence indicates a chase state; it should stay inactive in range and
  decline states, where the existing economic risk gradient remains in charge.

## Dynamic rule

Let (z_t=widehat{mu}_t/widehat{sigma}_t), after empirical-Bayes
shrinkage, and let (o_t) be the normalized amount by which current inventory
is above the policy target. The component uses

[
c_t=
operatorname{clip}left({z_t-z_0over z_1-z_0},0,1ight)o_t,
qquad
eta^{max}_t=eta^{hard}_{max}
-c_t(eta^{hard}_{max}-eta^{chase}).
]

The predeclared defaults are (z_0=1), (z_1=2), and
(eta^{chase}=0.20). A non-positive forecast, insufficient evidence, or
inventory at/below policy target returns the null cap. This is intentionally
not a generic bearish-regime cap: downside de-risking remains the existing
fee/risk-gradient decision.

The component is isolated in
`pkg/strategy/gammacapture/dynamic_price_beta_target.go`; it does not call
order submission, mutate balances, or alter price/quantity/gates.

## Standalone causal screen

The screen used ETHJPY BBO only, with no orders or inventory feedback:

- interval: `2026-08-17T00:00:00Z`–`2026-08-21T00:00:00Z`;
- six-hour causal history;
- one-minute BBO sampling;
- primary 15-minute horizon;
- 384 non-overlapping effective anchors;
- hypothetical state: current inventory ratio `0.80`, policy target `0.50`.

The cap activated on 131 anchors. Their future executable-bid return averaged
`+10.07 bps`, versus `+2.86 bps` on inactive anchors; 73 active anchors were
positive and 58 negative. The mean active cap was `0.573`, because the rule
ramps between the hard maximum and `0.20` according to overweight exposure.
The simplified full-target exposure-reduction opportunity-cost proxy was
`+2.29 bps` before fees, spread, partial adjustment, and queue effects.

This does not support promotion. It suggests that the causal feature used for
the first screen is detecting continuation more than harmful chase/reversal.
The standalone gate is therefore:

```text
INCONCLUSIVE_STANDALONE_BEHAVIOR_SCREEN
```

No component replay, live YAML change, or service restart follows from this
screen. The next valid study must replace the past-return proxy with the
existing prequential terminal-return forecast and score the actual cap action
against the no-cap policy using executable-BBO markout, fees, partial
adjustment, drawdown, and block uncertainty.

Focused verification:

```text
go test ./pkg/strategy/gammacapture -run TestDynamicPriceBetaTarget -count=1
go test ./cmd/gammacapture-mm-research -run 'TestCompactBBOAtInterval' -count=1
```
