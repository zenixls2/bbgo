-- +up
-- +begin
ALTER TABLE `trades`
MODIFY COLUMN `fee_currency` VARCHAR(6) NOT NULL;
-- +end

-- +down

-- +begin
ALTER TABLE `trades`
MODIFY COLUMN `fee_currency` VARCHAR(4) NOT NULL;
-- +end
