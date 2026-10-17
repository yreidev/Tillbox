-- +goose Up
CREATE INDEX transactions_alipay_order ON transactions(channel, alipay_order_no);
CREATE TABLE assistance_cases (
 id BIGSERIAL PRIMARY KEY,
 dedupe_key TEXT NOT NULL UNIQUE,
 order_id BIGINT NOT NULL DEFAULT 0,
 transaction_id BIGINT NOT NULL DEFAULT 0,
 anomaly_id BIGINT NOT NULL DEFAULT 0,
 source TEXT NOT NULL,
 kind TEXT NOT NULL,
 payment_ref TEXT NOT NULL DEFAULT '',
 claim_amount BIGINT NOT NULL DEFAULT 0,
 paid_at BIGINT NOT NULL DEFAULT 0,
 message TEXT NOT NULL DEFAULT '',
 contact TEXT NOT NULL DEFAULT '',
 token_hash TEXT NOT NULL DEFAULT '',
 status TEXT NOT NULL DEFAULT 'pending',
 summary TEXT NOT NULL DEFAULT '',
 public_reply TEXT NOT NULL DEFAULT '',
 attempts BIGINT NOT NULL DEFAULT 0,
 next_at BIGINT NOT NULL,
 created_at BIGINT NOT NULL,
 updated_at BIGINT NOT NULL
);
CREATE UNIQUE INDEX assistance_token ON assistance_cases(token_hash) WHERE token_hash <> '';
CREATE INDEX assistance_due ON assistance_cases(status,next_at);
CREATE INDEX assistance_order ON assistance_cases(order_id);
CREATE INDEX assistance_ref ON assistance_cases(payment_ref);
CREATE TABLE assistance_actions (
 id BIGSERIAL PRIMARY KEY,
 case_id BIGINT NOT NULL REFERENCES assistance_cases(id),
 action TEXT NOT NULL,
 reason TEXT NOT NULL,
 result TEXT NOT NULL,
 created_at BIGINT NOT NULL
);
-- +goose Down
DROP TABLE assistance_actions;
DROP TABLE assistance_cases;
DROP INDEX transactions_alipay_order;
