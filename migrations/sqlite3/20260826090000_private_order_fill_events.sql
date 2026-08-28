-- !txn
-- +up
CREATE TABLE `private_order_fill_events`
(
    `gid`                  INTEGER PRIMARY KEY AUTOINCREMENT,
    `schema_version`       INTEGER NOT NULL DEFAULT 1,
    `event_type`           VARCHAR(32) NOT NULL,
    `observed_at`          DATETIME(3) NOT NULL,
    `exchange_at`          DATETIME(3),
    `production_version`   VARCHAR(128) NOT NULL DEFAULT '',
    `session`              VARCHAR(64) NOT NULL DEFAULT '',
    `exchange`             VARCHAR(24) NOT NULL DEFAULT '',
    `strategy`             VARCHAR(128) NOT NULL DEFAULT '',
    `strategy_instance_id` VARCHAR(128) NOT NULL DEFAULT '',
    `symbol`               VARCHAR(32) NOT NULL DEFAULT '',
    `order_id`             INTEGER NOT NULL DEFAULT 0,
    `client_order_id`      VARCHAR(122) NOT NULL DEFAULT '',
    `order_uuid`           VARCHAR(64) NOT NULL DEFAULT '',
    `trade_id`             INTEGER NOT NULL DEFAULT 0,
    `side`                 VARCHAR(16) NOT NULL DEFAULT '',
    `order_type`           VARCHAR(32) NOT NULL DEFAULT '',
    `time_in_force`        VARCHAR(8) NOT NULL DEFAULT '',
    `status`               VARCHAR(32) NOT NULL DEFAULT '',
    `is_working`           BOOLEAN NOT NULL DEFAULT FALSE,
    `price`                VARCHAR(64) NOT NULL DEFAULT '',
    `quantity`             VARCHAR(64) NOT NULL DEFAULT '',
    `average_price`        VARCHAR(64) NOT NULL DEFAULT '',
    `executed_quantity`    VARCHAR(64) NOT NULL DEFAULT '',
    `trade_price`          VARCHAR(64) NOT NULL DEFAULT '',
    `trade_quantity`       VARCHAR(64) NOT NULL DEFAULT '',
    `trade_quote_quantity` VARCHAR(64) NOT NULL DEFAULT '',
    `fee`                  VARCHAR(64) NOT NULL DEFAULT '',
    `fee_currency`         VARCHAR(32) NOT NULL DEFAULT '',
    `is_maker`             BOOLEAN NOT NULL DEFAULT FALSE,
    `is_buyer`             BOOLEAN NOT NULL DEFAULT FALSE,
    `submit_index`         INTEGER NOT NULL DEFAULT 0,
    `submit_accepted`      BOOLEAN NOT NULL DEFAULT FALSE,
    `error`                TEXT NOT NULL DEFAULT '',
    `cancel_reason`        VARCHAR(128) NOT NULL DEFAULT '',
    `cancel_accepted`      BOOLEAN NOT NULL DEFAULT FALSE,
    `cancel_error`         TEXT NOT NULL DEFAULT '',
    `payload`              TEXT NOT NULL
);
CREATE INDEX `private_order_fill_events_observed` ON `private_order_fill_events` (`observed_at`);
CREATE INDEX `private_order_fill_events_order` ON `private_order_fill_events` (`exchange`, `symbol`, `order_id`, `observed_at`);
CREATE INDEX `private_order_fill_events_version` ON `private_order_fill_events` (`production_version`, `strategy`, `symbol`, `observed_at`);

-- +down
DROP TABLE IF EXISTS `private_order_fill_events`;
