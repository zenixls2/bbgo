# GammaCapture engineering fixes — 2026-08-19

## Trigger

The 2026-08-18 18:00–2026-08-19 09:30 JST review found two execution hazards:

* a Dynamic Inventory Aim risk gradient was passed to Fast as if it were a
  price-return forecast.  This produced an IOC budget of roughly 1,101 bps;
* Binance rejected that IOC with `-1013 PERCENT_PRICE_BY_SIDE`, after the maker
  pair had already been cancelled.  A later IOC also used an implausibly wide
  limit.  Order logs showed the submitted limit rather than the actual fill.

## Changes

1. `DynamicInventoryAimDecision.ExecutionReturnBps` is now the only Dynamic Aim
   output consumed by Fast price execution.  It is the empirical-Bayes-shrunk
   directional return.  `NetReturnBps` remains the inventory-risk gradient for
   target selection and is explicitly not a markout forecast.
2. Fast active execution now computes a signed marginal inventory-variance
   penalty for both passive and rejected-maker paths.  It requires a positive
   one-sided certainty-equivalent lower bound after taker fee and return
   standard-error haircut.  The resulting bound caps the IOC impact budget.
   Risk-reducing impulses receive a negative variance penalty (a benefit).
3. Binance `PERCENT_PRICE_BY_SIDE` multipliers are retained in `types.Market`.
   Marketable IOC limits are clamped to the side-aware bounds using the current
   BBO midpoint as the causal reference, then tick-rounded.  The helper fails
   closed when the live touch itself is outside the admissible interval and
   logs whether a clamp occurred.
4. Binance order conversion and execution-report parsing now derive
   `AveragePrice` from cumulative quote quantity / executed quantity.  Order
   strings show `limit (avg-fill ...)` when those prices differ, so an IOC limit
   is not mistaken for its execution price.  The active-order update path keeps
   the average fill price.
5. Binance `-2011 Unknown order` during cancellation is treated as the benign
   fill/cancel race and logged at debug level; other cancellation failures keep
   their warning and retry behavior.

## Verification

Focused GammaCapture, Binance, bbgo, and types tests pass, including:

```text
go test ./pkg/strategy/gammacapture ./pkg/exchange/binance ./pkg/bbgo ./pkg/types
go test ./cmd/gammacapture-mm-research
go test ./pkg/...
```

The live systemd service was not restarted or replaced by this change. A new
binary must be built and reviewed before deployment; no credentials or runtime
state are part of the change.

## Test maintenance

The old `examples/max-withdraw` program referenced the removed MAX v2
`RestClient` API. It now uses `maxapi/v3.Client`, v3 withdrawal states, and the
v3 request constructors. Public Binance, public OKX, and Redis tests are now
explicit integration tests rather than unconditional network probes:

```text
TEST_BINANCE=1   # live Binance public/private API tests
TEST_OKEX=1      # live OKX public/private API tests
TEST_REDIS=1     # local Redis persistence test
TEST_LOCAL_NETWORK=1  # websocket tests that bind a local httptest listener
```

Without those flags, the default offline suite remains deterministic and does
not turn DNS or service availability into a code failure.

The complete offline suite passes with:

```bash
GOCACHE=/tmp/bbgo-gocache go test ./...
```

## Warm-up and bounded replay optimization

The replay path previously had two avoidable costs:

* a bounded replay could inherit the command's default calibration interval,
  silently loading history from July even when the requested replay began on
  2026-08-17; and
* every raw BBO row was parsed into memory before warm-up and evaluation
  compaction, even though the model only consumes the last causal state in a
  warm-up second and the requested evaluation bucket.

The loader now clamps the calibration interval to explicit replay bounds unless
the operator supplies an explicit calibration range.  It also performs
streaming BBO compaction while reading the CSV archive.  Warm-up retains one
last-state snapshot per second, evaluation uses the requested BBO interval, and
checkpoint delta replay remains exact (uncompacted).  The cache header includes
the interval so a 30-second replay cannot reuse an incompatible exact-event
cache.

The startup warm-up path applies the same one-second last-state accumulator and
ORs reconnect-gap markers across each bucket.  This reduces model updates while
preserving causal rolling-window state and gap detection.

For the ETHJPY replay from 2026-08-18 00:00 JST through 2026-08-19 13:07 JST,
the bounded warm-up began at 2026-08-17 17:30 JST, used 11,667 warm-up BBO
snapshots and 3,493 warm-up trades, and generated a 1.9 MB interval-specific
cache.  The full replay had no data gaps.  The baseline arm produced 36 fills
(18/18), net +11.68 JPY versus hold +11.19 JPY; the replay arm labelled
`horizon-touch` produced 27 fills (15/12), net +9.90 JPY versus hold +11.19
JPY.  The ETHJPY live YAML does not enable `HorizonTouchModel`, so this arm did
not load a HorizonTouch artifact and should be read as the current Fast path
with a replay-mode label, not as proof that HorizonTouch is live.  Thus the
loading fix removed historical-load waste, but the current risk/quote policy
still needs separate trading-performance work; it was not promoted based on
this replay.  Synthetic fills use public BBO/trades and a zero queue multiplier,
so they are not evidence of live calibration.

In the production comparison, `fullLegacy` is not a git branch or an old
binary.  It is a paired replay baseline built from the current YAML with only
`AsymmetricOscillationRisk` disabled and the legacy direction-fallback mode
selected.  Since the baseline inherits the current YAML, `VolumeProfile` remains
enabled in that arm; it is therefore not a clean pre-VP historical benchmark.

An isolated four-hour ETHJPY production replay was started with a 1-second BBO
bucket. Its warm cache reached about 514 MB before producing a report, so it
was stopped and its temporary cache removed. This is a replay-resource finding,
not a strategy result; the causal component tests above remain the promotion
evidence for this engineering fix.
