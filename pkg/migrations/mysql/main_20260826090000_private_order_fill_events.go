package mysql

import "github.com/c9s/rockhopper/v2"

func init() {
	AddStatementMigration("main", 20260826090000, "migrations/mysql/20260826090000_private_order_fill_events.sql", true,
		[]rockhopper.Statement{
			{Direction: rockhopper.DirectionUp, SQL: "CREATE TABLE `private_order_fill_events`\n(\n    `gid` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,\n    `schema_version` INT NOT NULL DEFAULT 1,\n    `event_type` VARCHAR(32) NOT NULL,\n    `observed_at` DATETIME(3) NOT NULL,\n    `exchange_at` DATETIME(3) NULL,\n    `production_version` VARCHAR(128) NOT NULL DEFAULT '',\n    `session` VARCHAR(64) NOT NULL DEFAULT '',\n    `exchange` VARCHAR(24) NOT NULL DEFAULT '',\n    `strategy` VARCHAR(128) NOT NULL DEFAULT '',\n    `strategy_instance_id` VARCHAR(128) NOT NULL DEFAULT '',\n    `symbol` VARCHAR(32) NOT NULL DEFAULT '',\n    `order_id` BIGINT UNSIGNED NOT NULL DEFAULT 0,\n    `client_order_id` VARCHAR(122) NOT NULL DEFAULT '',\n    `order_uuid` VARCHAR(64) NOT NULL DEFAULT '',\n    `trade_id` BIGINT UNSIGNED NOT NULL DEFAULT 0,\n    `side` VARCHAR(16) NOT NULL DEFAULT '',\n    `order_type` VARCHAR(32) NOT NULL DEFAULT '',\n    `time_in_force` VARCHAR(8) NOT NULL DEFAULT '',\n    `status` VARCHAR(32) NOT NULL DEFAULT '',\n    `is_working` BOOLEAN NOT NULL DEFAULT FALSE,\n    `price` VARCHAR(64) NOT NULL DEFAULT '',\n    `quantity` VARCHAR(64) NOT NULL DEFAULT '',\n    `average_price` VARCHAR(64) NOT NULL DEFAULT '',\n    `executed_quantity` VARCHAR(64) NOT NULL DEFAULT '',\n    `trade_price` VARCHAR(64) NOT NULL DEFAULT '',\n    `trade_quantity` VARCHAR(64) NOT NULL DEFAULT '',\n    `trade_quote_quantity` VARCHAR(64) NOT NULL DEFAULT '',\n    `fee` VARCHAR(64) NOT NULL DEFAULT '',\n    `fee_currency` VARCHAR(32) NOT NULL DEFAULT '',\n    `is_maker` BOOLEAN NOT NULL DEFAULT FALSE,\n    `is_buyer` BOOLEAN NOT NULL DEFAULT FALSE,\n    `submit_index` INT NOT NULL DEFAULT 0,\n    `submit_accepted` BOOLEAN NOT NULL DEFAULT FALSE,\n    `error` TEXT NOT NULL,\n    `cancel_reason` VARCHAR(128) NOT NULL DEFAULT '',\n    `cancel_accepted` BOOLEAN NOT NULL DEFAULT FALSE,\n    `cancel_error` TEXT NOT NULL,\n    `payload` TEXT NOT NULL,\n    PRIMARY KEY (`gid`)\n);\nCREATE INDEX `private_order_fill_events_observed` ON `private_order_fill_events` (`observed_at`);\nCREATE INDEX `private_order_fill_events_order` ON `private_order_fill_events` (`exchange`, `symbol`, `order_id`, `observed_at`);\nCREATE INDEX `private_order_fill_events_version` ON `private_order_fill_events` (`production_version`, `strategy`, `symbol`, `observed_at`);"},
		},
		[]rockhopper.Statement{
			{Direction: rockhopper.DirectionDown, SQL: "DROP TABLE `private_order_fill_events`;"},
		},
	)
}
