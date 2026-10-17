-- 支付渠道：经营码和上游易支付都是渠道，商户各选一个。订单和流水的 channel 列改存渠道实例键 ch<编号>。

-- +goose Up
CREATE TABLE channels (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    type       TEXT    NOT NULL,
    name       TEXT    NOT NULL,
    enabled    INTEGER NOT NULL DEFAULT 0,
    config     TEXT    NOT NULL DEFAULT '{}',
    remark     TEXT    NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

ALTER TABLE merchants ADD COLUMN channel_id INTEGER NOT NULL DEFAULT 0;

-- 上游易支付预下单的结果，一个订单一条。
CREATE TABLE upstream_payments (
    order_id    INTEGER PRIMARY KEY REFERENCES orders (id),
    channel_id  INTEGER NOT NULL,
    pay_type    TEXT    NOT NULL,
    trade_no    TEXT    NOT NULL DEFAULT '',
    info_type   TEXT    NOT NULL DEFAULT '',
    info        TEXT    NOT NULL DEFAULT '',
    checked_at  INTEGER NOT NULL DEFAULT 0,
    check_error TEXT    NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

-- 金额池按渠道分配。
DROP INDEX orders_pay_amount_released;
CREATE INDEX orders_channel_pay_amount ON orders (channel, pay_amount, released_at);

-- 已有的支付宝设置（或经营码订单）变成 1 号经营码渠道，加密后的私钥原样沿用。
INSERT INTO channels (id, type, name, enabled, config, created_at, updated_at)
SELECT 1, 'alipay_qr', '支付宝经营码',
       COALESCE((SELECT json_extract(value, '$.enabled') FROM settings WHERE key = 'alipay'), 0),
       COALESCE((SELECT value FROM settings WHERE key = 'alipay'), '{}'),
       CAST(strftime('%s', 'now') AS INTEGER) * 1000, CAST(strftime('%s', 'now') AS INTEGER) * 1000
WHERE EXISTS (SELECT 1 FROM settings WHERE key = 'alipay')
   OR EXISTS (SELECT 1 FROM orders WHERE channel = 'alipay_qr')
   OR EXISTS (SELECT 1 FROM transactions WHERE channel = 'alipay_qr');

-- 本地演示库里的假渠道订单归到一个演示渠道。
INSERT INTO channels (id, type, name, enabled, config, created_at, updated_at)
SELECT COALESCE((SELECT MAX(id) FROM channels), 0) + 1, 'demo', '演示渠道', 1, '{}',
       CAST(strftime('%s', 'now') AS INTEGER) * 1000, CAST(strftime('%s', 'now') AS INTEGER) * 1000
WHERE EXISTS (SELECT 1 FROM orders WHERE channel = 'fake')
   OR EXISTS (SELECT 1 FROM transactions WHERE channel = 'fake');

UPDATE orders SET channel = 'ch1' WHERE channel = 'alipay_qr';
UPDATE transactions SET channel = 'ch1' WHERE channel = 'alipay_qr';
UPDATE orders SET channel = 'ch' || (SELECT MIN(id) FROM channels WHERE type = 'demo') WHERE channel = 'fake';
UPDATE transactions SET channel = 'ch' || (SELECT MIN(id) FROM channels WHERE type = 'demo') WHERE channel = 'fake';
UPDATE settings SET key = 'ledger_state:ch1'
WHERE key = 'ledger_state' AND EXISTS (SELECT 1 FROM channels WHERE id = 1 AND type = 'alipay_qr');
DELETE FROM settings WHERE key = 'ledger_state';
DELETE FROM settings WHERE key = 'alipay';
-- 商户优先用第一个启用的渠道（线上是启用的 1 号经营码渠道；演示库里经营码没启用时用演示渠道）。
UPDATE merchants SET channel_id = COALESCE((SELECT MIN(id) FROM channels WHERE enabled = 1), (SELECT MIN(id) FROM channels), 0);

-- +goose Down
INSERT INTO settings (key, value, updated_at)
SELECT 'alipay', json_set(config, '$.enabled', json(CASE enabled WHEN 1 THEN 'true' ELSE 'false' END)), updated_at
FROM channels WHERE id = 1 AND type = 'alipay_qr';
UPDATE settings SET key = 'ledger_state' WHERE key = 'ledger_state:ch1';
DELETE FROM settings WHERE key LIKE 'ledger_state:%';
UPDATE orders SET channel = 'fake' WHERE channel IN (SELECT 'ch' || id FROM channels WHERE type = 'demo');
UPDATE transactions SET channel = 'fake' WHERE channel IN (SELECT 'ch' || id FROM channels WHERE type = 'demo');
UPDATE orders SET channel = 'alipay_qr' WHERE channel LIKE 'ch%';
UPDATE transactions SET channel = 'alipay_qr' WHERE channel LIKE 'ch%';
DROP INDEX orders_channel_pay_amount;
CREATE INDEX orders_pay_amount_released ON orders (pay_amount, released_at);
DROP TABLE upstream_payments;
ALTER TABLE merchants DROP COLUMN channel_id;
DROP TABLE channels;
