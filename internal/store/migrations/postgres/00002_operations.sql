-- +goose Up
ALTER TABLE orders ADD COLUMN is_test BOOLEAN NOT NULL DEFAULT FALSE;
CREATE INDEX orders_test_created ON orders (is_test, created_at);
CREATE TABLE test_receipts (
    order_id BIGINT PRIMARY KEY REFERENCES orders(id),
    deliveries BIGINT NOT NULL DEFAULT 1,
    first_at BIGINT NOT NULL,
    last_at BIGINT NOT NULL
);
CREATE TABLE archive_batches (
    id BIGSERIAL PRIMARY KEY,
    filename TEXT NOT NULL UNIQUE,
    sha256 TEXT NOT NULL,
    records BIGINT NOT NULL,
    created_at BIGINT NOT NULL
);
CREATE INDEX notify_attempts_created ON notify_attempts (created_at);

-- +goose Down
DROP INDEX notify_attempts_created;
DROP TABLE archive_batches;
DROP TABLE test_receipts;
DROP INDEX orders_test_created;
ALTER TABLE orders DROP COLUMN is_test;
