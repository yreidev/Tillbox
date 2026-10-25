-- 商户按支付方式选渠道：每种方式一行，没有这一行的方式就是没开。以前商户只选一个渠道（merchants.channel_id），
-- 升级时按那个渠道开放的方式换算：经营码和演示渠道是支付宝，第三方易支付是配置里勾选的方式。

-- +goose Up
CREATE TABLE merchant_routes (
    merchant_id BIGINT NOT NULL REFERENCES merchants (id),
    pay_type    TEXT   NOT NULL,
    channel_id  BIGINT NOT NULL,
    PRIMARY KEY (merchant_id, pay_type)
);
CREATE INDEX merchant_routes_channel ON merchant_routes (channel_id);

INSERT INTO merchant_routes (merchant_id, pay_type, channel_id)
SELECT m.id, 'alipay', c.id FROM merchants m JOIN channels c ON c.id = m.channel_id
WHERE c.type IN ('alipay_qr', 'demo');
INSERT INTO merchant_routes (merchant_id, pay_type, channel_id)
SELECT DISTINCT m.id, t.value, c.id
FROM merchants m JOIN channels c ON c.id = m.channel_id
CROSS JOIN LATERAL jsonb_array_elements_text(
    CASE WHEN jsonb_typeof(c.config::jsonb -> 'types') = 'array' THEN c.config::jsonb -> 'types' ELSE '[]'::jsonb END
) AS t (value)
WHERE c.type = 'epay';

ALTER TABLE merchants DROP COLUMN channel_id;

-- +goose Down
ALTER TABLE merchants ADD COLUMN channel_id BIGINT NOT NULL DEFAULT 0;
-- 退回只选一个渠道：优先用支付宝走的渠道。
UPDATE merchants SET channel_id = COALESCE((
    SELECT channel_id FROM merchant_routes r WHERE r.merchant_id = merchants.id
    ORDER BY r.pay_type <> 'alipay', r.channel_id LIMIT 1
), 0);
DROP TABLE merchant_routes;
