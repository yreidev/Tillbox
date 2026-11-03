-- 经营码渠道只用支付宝正式网关，配置里不再存网关。原来选了沙箱的渠道停用：沙箱应用查不了真实账户的流水，
-- 要换成支付宝里“已上线”的正式应用再启用。

-- +goose Up
UPDATE channels SET enabled = FALSE
WHERE type = 'alipay_qr' AND config::jsonb ->> 'gateway' LIKE '%alipaydev.com%';
UPDATE channels SET config = (config::jsonb - 'gateway')::text
WHERE type = 'alipay_qr' AND config::jsonb -> 'gateway' IS NOT NULL;

-- +goose Down
-- 旧版本没有网关时用正式网关，不用改回。
