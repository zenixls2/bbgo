# GammaCapture private order/fill ledger

## Purpose

The framework now records authenticated order and trade observations in the append-only `private_order_fill_events` table. This is an audit and calibration dataset only. It does not alter quote prices, quantities, inventory aims, or any risk gate.

The table complements the existing upserted `orders` and `trades` tables. Upserts are convenient for the current state; this ledger preserves every observed lifecycle transition, submit/cancel boundary, and private fill so queue and adverse-selection studies do not reconstruct history from logs.

## Production identity

Every row contains `production_version`. Set it explicitly in the deployment configuration and change it whenever the production strategy/configuration bundle changes:

```yaml
environment:
  productionVersion: gammacapture-ethjpy-2026-08-26-v1

sync:
  userDataStream:
    privateOrderFillLedger: true
```

The ETHJPY profile has these settings in [config/gammacapture-ethjpy.yaml](/home/zenixls2/src/bbgo/config/gammacapture-ethjpy.yaml). The database still follows BBGO's normal `database` configuration or `DB_DRIVER`/`DB_DSN`/`SQLITE3_DSN` environment variables; no database is opened when those are absent.

## Event contract

The framework emits:

- `order_submit_intent` and `order_submit_result` at the order executor boundary;
- `cancel_request` and `cancel_result` around executor cancellation;
- `order_update` for each authenticated private order update;
- `fill` for each authenticated private trade update.

`payload` retains the original order/trade object. Scalar columns contain exact fixed-point values as strings, avoiding float rounding. `observed_at` is the local callback clock; `exchange_at` is the venue timestamp when available. `strategy` and `strategy_instance_id` are present on executor-originated events. Raw stream events can have empty strategy fields and should be joined to the submit result by exchange, symbol, and order ID when attribution is required.

The migration is [20260826090000_private_order_fill_events.sql](/home/zenixls2/src/bbgo/migrations/sqlite3/20260826090000_private_order_fill_events.sql) for SQLite and the corresponding MySQL migration. BBGO upgrades the table through its normal migration path.

## Calibration queries

Always select one production version, symbol, and strategy/client-order namespace before calculating fill rates:

```sql
SELECT event_type, COUNT(*) AS n
FROM private_order_fill_events
WHERE production_version = 'gammacapture-ethjpy-2026-08-26-v1'
  AND symbol = 'ETHJPY'
  AND client_order_id LIKE 'gcmm-%'
GROUP BY event_type
ORDER BY event_type;
```

Private fill attribution should use `fill` rows, not a public cross alone:

```sql
SELECT observed_at, exchange_at, order_id, trade_id, side,
       trade_price, trade_quantity, fee, fee_currency, is_maker
FROM private_order_fill_events
WHERE production_version = 'gammacapture-ethjpy-2026-08-26-v1'
  AND symbol = 'ETHJPY'
  AND event_type = 'fill'
  AND client_order_id LIKE 'gcmm-%'
ORDER BY observed_at, gid;
```

For queue/adverse-selection work, join these rows to the captured BBO using `exchange_at` when present and `observed_at` as the transport-latency fallback. A private fill is never inferred from the public tape.

## Operational rules

The ledger is observation-only and storage failures are logged without changing an order decision. The DB path is used for all strategy types; GammaCapture additionally retains its BBO-enriched JSONL boundary ledger while the calibration tooling is migrated. The JSONL is tagged with the same production version when the environment provides one.

No historical private fills can be recovered into this table from an absent ledger. Collection starts after the process has loaded the migration and registered the user-data callbacks. Do not promote a queue model until the selected version has at least 30 maker fills, including at least 10 per side, and the existing precision/recall gate passes on a chronological holdout.
