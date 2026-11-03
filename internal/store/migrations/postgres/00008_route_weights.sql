-- 商户的一种支付方式可以选几个渠道，新订单按权重随机分：主键加上渠道，新增权重列。原有的每种方式一个渠道，权重记 1。

-- +goose Up
ALTER TABLE merchant_routes ADD COLUMN weight INTEGER NOT NULL DEFAULT 1;
ALTER TABLE merchant_routes DROP CONSTRAINT merchant_routes_pkey;
ALTER TABLE merchant_routes ADD PRIMARY KEY (merchant_id, pay_type, channel_id);

-- +goose Down
-- 退回每种方式一个渠道：留权重最大的，一样大时留编号小的。
DELETE FROM merchant_routes r
WHERE r.channel_id <> (
    SELECT x.channel_id FROM merchant_routes x
    WHERE x.merchant_id = r.merchant_id AND x.pay_type = r.pay_type
    ORDER BY x.weight DESC, x.channel_id LIMIT 1
);
ALTER TABLE merchant_routes DROP CONSTRAINT merchant_routes_pkey;
ALTER TABLE merchant_routes ADD PRIMARY KEY (merchant_id, pay_type);
ALTER TABLE merchant_routes DROP COLUMN weight;
