-- 商户的一种支付方式可以选几个渠道，新订单按权重随机分：主键加上渠道，新增权重列。原有的每种方式一个渠道，权重记 1。
-- SQLite 改不了主键，建新表换掉。

-- +goose Up
CREATE TABLE merchant_routes_new (
    merchant_id INTEGER NOT NULL REFERENCES merchants (id),
    pay_type    TEXT    NOT NULL,
    channel_id  INTEGER NOT NULL,
    weight      INTEGER NOT NULL DEFAULT 1,
    PRIMARY KEY (merchant_id, pay_type, channel_id)
);
INSERT INTO merchant_routes_new (merchant_id, pay_type, channel_id, weight)
SELECT merchant_id, pay_type, channel_id, 1 FROM merchant_routes;
DROP TABLE merchant_routes;
ALTER TABLE merchant_routes_new RENAME TO merchant_routes;
CREATE INDEX merchant_routes_channel ON merchant_routes (channel_id);

-- +goose Down
-- 退回每种方式一个渠道：留权重最大的，一样大时留编号小的。
CREATE TABLE merchant_routes_old (
    merchant_id INTEGER NOT NULL REFERENCES merchants (id),
    pay_type    TEXT    NOT NULL,
    channel_id  INTEGER NOT NULL,
    PRIMARY KEY (merchant_id, pay_type)
);
INSERT INTO merchant_routes_old (merchant_id, pay_type, channel_id)
SELECT r.merchant_id, r.pay_type, r.channel_id FROM merchant_routes r
WHERE r.channel_id = (
    SELECT x.channel_id FROM merchant_routes x
    WHERE x.merchant_id = r.merchant_id AND x.pay_type = r.pay_type
    ORDER BY x.weight DESC, x.channel_id LIMIT 1
);
DROP TABLE merchant_routes;
ALTER TABLE merchant_routes_old RENAME TO merchant_routes;
CREATE INDEX merchant_routes_channel ON merchant_routes (channel_id);
