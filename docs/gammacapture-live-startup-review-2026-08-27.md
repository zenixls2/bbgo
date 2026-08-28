# GammaCapture Live Startup Trade and Market Data Review

**Review window:** startup `2026-08-27 19:48:18 JST` through the latest available ETHJPY capture at approximately `2026-08-27 23:34 JST`.

**Scope:** read-only live journal, ETHJPY BBO/trade capture, private order/fill ledger, and source-level reproduction. No live service, binary, YAML, or systemd unit was changed.

## Observed runtime

- Strategy unit remained `active/running`, `NRestarts=0`, started at `2026-08-27 19:48:18 JST`.
- Startup replay restored `4,891` BBO updates and `161` trade updates before live callbacks.
- Startup reconciled and canceled `2` owned stale orders.
- ETHJPY current-day capture after the warmup boundary contained `159,662` BBO rows through the observed endpoint.
- BBO spread: median approximately `0.025 bps`, P95 approximately `1.891 bps`, maximum approximately `14.687 bps`.
- `gap_before_ms`: P95 approximately `410 ms`, maximum approximately `15,383 ms`.

The gap marker is based on callback receive time. It is not an exchange event-time gap and must not be used as proof of market discontinuity without a separate event-time comparison.

## Private execution observations

The ledger contained `29` fills after the startup warmup boundary in the inspected window. These included both:

- passive maker fills (`GTC`, `isMaker=true`), and
- active/IOC fills (`IOC`, `isMaker` unset/false in the observed records).

Examples of the distinction:

- maker sell: order `1117405558`, `0.00642 ETH` at `397,992 JPY`, maker fee in JPY;
- IOC buy: order `1117407404`, `0.00081 ETH` at `398,072 JPY`, fee in ETH;
- IOC sell: order `1117422581`, `0.00081 ETH` at `398,230 JPY`, fee in JPY.

These populations must remain separate in calibration and PnL attribution. A public BBO touch is not evidence of a private fill.

## Confirmed implementation issue

At `22:42:39 JST`, the journal recorded the same `fillSequence=23` terminal-fill replacement deferral twenty times in the same second. Source inspection showed that the generation gate correctly prevented the normal BBO planner from submitting a competing replacement while the fill rebalance was pending; the defect was unbounded duplicate logging, not twenty quote submissions.

The source now rate-limits this diagnostic to one message per ten seconds and includes `stage` (`pre-cancel` or `post-cancel`). A regression test covers the rate limit.

## Model/data conclusions

1. The observed data is sufficiently active for monitoring, but the maximum receive-time gap and mixed time semantics require explicit gap labels in research manifests.
2. The private fill sample is small and heterogeneous. It cannot justify relaxing maker protection or treating all fills as passive maker evidence.
3. Several fills occurred after the BBO had moved from the order's original quote. This is expected for asynchronous execution records and demonstrates why calibration must use order submission BBO, exchange fill time, order type, and maker/taker status rather than nearest post-fill BBO alone.
4. The live logs show the causal pivot target changing inventory target ratios, while Fast drift was explicitly unhealthy and excluded from applied drift adjustment. No unvalidated drift correction was enabled by this review.
5. No model parameter, live YAML, systemd unit, or production binary was changed based only on these observations.

## Verification

The following source-level checks passed after the observability fix:

```text
go test ./pkg/strategy/gammacapture ./cmd/gammacapture-mm-research -count=1

go test -race ./pkg/strategy/gammacapture ./cmd/gammacapture-mm-research -count=1
```

The live service was not restarted, so the running process does not include this source change until a separately approved build/deployment occurs.
