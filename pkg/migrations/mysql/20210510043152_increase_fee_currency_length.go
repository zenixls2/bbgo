package mysql

import (
	"context"

	"github.com/c9s/rockhopper"
)

func init() {
	AddMigration(upIncreaseFeeCurrencyLength, downIncreaseFeeCurrencyLength)

}

func upIncreaseFeeCurrencyLength(ctx context.Context, tx rockhopper.SQLExecutor) (err error) {
	// This code is executed when the migration is applied.

	_, err = tx.ExecContext(ctx, "ALTER TABLE `trades`\nMODIFY COLUMN `fee_currency` VARCHAR(6) NOT NULL;")
	if err != nil {
		return err
	}

	return err
}

func downIncreaseFeeCurrencyLength(ctx context.Context, tx rockhopper.SQLExecutor) (err error) {
	// This code is executed when the migration is rolled back.

	_, err = tx.ExecContext(ctx, "ALTER TABLE `trades`\nMODIFY COLUMN `fee_currency` VARCHAR(4) NOT NULL;")
	if err != nil {
		return err
	}

	return err
}
