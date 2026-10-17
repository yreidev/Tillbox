-- +goose Up
ALTER TABLE orders ADD COLUMN is_test INTEGER NOT NULL DEFAULT 0;
CREATE INDEX orders_test_created ON orders (is_test, created_at);
CREATE TABLE test_receipts (
    order_id INTEGER PRIMARY KEY REFERENCES orders(id),
    deliveries INTEGER NOT NULL DEFAULT 1,
    first_at INTEGER NOT NULL,
    last_at INTEGER NOT NULL
);
CREATE TABLE archive_batches (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    filename TEXT NOT NULL UNIQUE,
    sha256 TEXT NOT NULL,
    records INTEGER NOT NULL,
    created_at INTEGER NOT NULL
);
CREATE INDEX notify_attempts_created ON notify_attempts (created_at);

-- +goose Down
DROP INDEX notify_attempts_created;
DROP TABLE archive_batches;
DROP TABLE test_receipts;
DROP INDEX orders_test_created;
ALTER TABLE orders DROP COLUMN is_test;
