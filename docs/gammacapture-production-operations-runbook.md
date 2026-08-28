# GammaCapture Production Operations Runbook

**Status:** operational control document; does not authorize a deployment or service restart.

**Scope:** userspace systemd collectors and the ETHJPY live GammaCapture strategy.

## 1. Current runtime topology

The user systemd manager runs five active units:

- `gammacapture-capture@BTCJPY.service`
- `gammacapture-capture@ETHJPY.service`
- `gammacapture-capture@SOLJPY.service`
- `gammacapture-capture@XRPJPY.service`
- `gammacapture-strategy.service`

The audited live strategy command is:

```text
/home/zenixls2/src/bbgo/bin/bbgo \
  --dotenv /home/zenixls2/src/bbgo/.env run \
  --config /home/zenixls2/src/bbgo/config/gammacapture-ethjpy.yaml
```

The units run under the user manager with `HOME=/home/zenixls2`, `Linger=yes`, and journal stdout/stderr. The strategy unit uses `Restart=on-failure`; collectors use `Restart=always` and `RestartSec=15`.

**Important:** units execute a mutable working-tree path and mutable binaries. A source edit or binary replacement can change the next restart without an immutable release boundary.

## 2. Pre-flight checks (read-only)

Run these before any restart, binary replacement, config change, or migration:

```bash
systemctl --user list-units 'gammacapture*' --all --no-pager
systemctl --user status gammacapture-strategy.service --no-pager
systemctl --user show gammacapture-strategy.service \
  -p ActiveState -p SubState -p Result -p NRestarts \
  -p ExecStart -p FragmentPath -p WorkingDirectory --no-pager

journalctl --user -u gammacapture-strategy.service -n 200 --no-pager
journalctl --user -u 'gammacapture-capture@*.service' -n 100 --no-pager

git -C /home/zenixls2/src/bbgo status --short --branch
git -C /home/zenixls2/src/bbgo diff --check
sha256sum /home/zenixls2/src/bbgo/bin/bbgo \
  /home/zenixls2/src/bbgo/bin/gammacapture-capture

du -sh /home/zenixls2/src/bbgo/data/gammacapture
```

Do not interpret `active (running)` as proof that the stream is healthy. Check recent journal timestamps, latest capture rows, BBO freshness, and account/order reconciliation separately.

## 3. Collector data boundaries

Collectors write public aggregate trades and book-ticker/BBO data as daily files with metadata and indexes. A collector gap warning is based on callback receive time. It is not proof of an exchange-event-time market outage.

The current paths are inconsistent:

- BTCJPY: `data/gammacapture/BTCJPY`
- ETHJPY: `data/gammacapture/ETHJPY`
- XRPJPY: `data/gammacapture/XRPJPY`
- SOLJPY: `data/gammacapture/live`

Do not merge or rename these paths while a collector is writing. Before changing a path, stop only the affected collector in an approved maintenance window, preserve the old directory, copy/verify data into a symbol-specific destination, and update the unit plus the replay manifest together. Never use an unverified mixed-symbol directory as a production warmup source.

Observed data volume was approximately 21 GB overall, including approximately 16 GB under `data/gammacapture/state`. No retention or rotation policy was present in the inspected units. Disk growth is therefore an operational risk, not a reason to delete data immediately.

## 4. Strategy safety invariants

The live strategy must remain fail-closed when any of these is true:

- configuration, market, account, or exchange-filter validation fails;
- required startup BBO history is absent or stale;
- owned stale-order cancellation cannot be verified;
- BBO is invalid, crossed, or missing required executable-side data;
- model readiness or data-quality requirements are not met;
- runtime state is `SUSPENDED` or `HALTED`;
- private-ledger or audit health is degraded beyond the configured policy.

A public BBO touch is not a private fill. A receive-time gap is not an exchange event-time gap. Neither may be silently converted into positive private execution evidence.

The maker quote admission gate must be checked both before and after acquiring the market-maker mutex. This protects against a BBO callback racing with suspend or emergency stop.

## 5. Incident handling

### 5.1 Strategy appears active but is stale

1. Do not restart first.
2. Inspect the last BBO, account-sync, order-update, and heartbeat journal timestamps.
3. Compare latest capture file modification time and last valid BBO row.
4. Check open strategy-owned orders through the exchange/account path.
5. If quote admission is not demonstrably safe, suspend or halt through the established operator control and verify cancellation; do not manually submit replacement orders.

### 5.2 Repeated Binance `-2010` maker rejection

This indicates the submitted limit would immediately match and take, commonly due to BBO movement between observation and submission. It is not evidence that a quote was accepted.

1. Preserve the journal interval and order IDs.
2. Confirm whether the rejection path cancels/replans safely and whether any owned order remains.
3. Check for repeated churn or a stale local BBO.
4. Treat the interval as execution-quality evidence requiring investigation; do not loosen maker-price protection or promote a fill model from it.

### 5.3 Collector websocket close or prolonged gap

1. Confirm whether the process reconnects and whether new rows resume.
2. Record both receive-time gap markers and exchange event timestamps where available.
3. Mark the interval as data-quality affected in research manifests.
4. Do not repair gaps by interpolating BBO or trades for execution claims.
5. If the latest BBO becomes stale beyond startup/live policy, the strategy must retain or cancel according to its fail-closed policy rather than assume continuity.

### 5.4 Restart or crash recovery

A restart requires verification of both local and exchange state:

1. Record current unit status, journal tail, binary hash, config hash, and owned order IDs.
2. Ensure the capture source for the required bounded warmup is present and fresh.
3. Verify model checkpoint identity and replay cursors.
4. Verify private-ledger integrity and sequence continuity.
5. Let startup reconcile only strategy-owned order IDs; unrelated Binance UI orders must remain untouched.
6. Confirm account balances and open orders after startup.
7. Confirm no quote is admitted until warmup/reconciliation gates pass.
8. Verify the first post-restart order decisions in the journal before declaring recovery complete.

## 6. Deployment boundary

No live deployment is justified solely by a positive replay or a code diff. A candidate requires:

- canonical replay using the same decision kernel and event-time contract as production;
- explicit data-quality and gap accounting;
- chronological WFO and an untouched holdout;
- reference, candidate, and ablation arms;
- action-level attribution for horizon, target, price, quantity, utility, risk, and fill assumptions;
- fee, drawdown, turnover, and hold-relative metrics;
- private-fill calibration where the claim concerns private execution;
- an immutable artifact with recorded checksum;
- a tested rollback artifact and a written maintenance window.

Until these conditions are met, research results remain research-only or shadow-only. Existing canary settings are not retroactively treated as approved promotions.

## 7. Credential and disk controls

The audited `.env` was mode `0644` while containing exchange and notification/database credentials. This should be tightened to owner-only permissions in an approved operations window and then verified with `stat`; do not print its contents. This runbook intentionally does not change permissions.

Before introducing retention, calculate the required replay/checkpoint/private-ledger retention period and archive/verify files first. Do not run broad deletion commands against `data/gammacapture/state` or capture roots.

## 8. Required evidence for an operational change

Every operational change record must include:

- exact unit and scope;
- pre-change unit state and order inventory;
- source/config/artifact checksums;
- reason and expected behavior;
- stop/rollback condition;
- post-change unit state;
- post-change journal evidence;
- post-change capture freshness and order/account reconciliation.

A successful `systemctl` command alone is not sufficient verification.
