-- 时间一律存 Unix 毫秒，金额一律存分；0 表示"没有"。

-- +goose Up
CREATE TABLE admins (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    password_hash TEXT    NOT NULL,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
);

CREATE TABLE sessions (
    token_hash   TEXT    PRIMARY KEY,
    admin_id     INTEGER NOT NULL REFERENCES admins (id) ON DELETE CASCADE,
    ip           TEXT    NOT NULL DEFAULT '',
    user_agent   TEXT    NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL
);
CREATE INDEX sessions_expires_at ON sessions (expires_at);

CREATE TABLE settings (
    key        TEXT    PRIMARY KEY,
    value      TEXT    NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE merchants (
    id             INTEGER PRIMARY KEY,
    name           TEXT    NOT NULL,
    md5_key        TEXT    NOT NULL,
    rsa_public_key TEXT    NOT NULL DEFAULT '',
    key_type       INTEGER NOT NULL DEFAULT 0,
    domains        TEXT    NOT NULL DEFAULT '',
    enabled        INTEGER NOT NULL DEFAULT 1,
    remark         TEXT    NOT NULL DEFAULT '',
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL
);

CREATE TABLE orders (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    trade_no     TEXT    NOT NULL UNIQUE,
    merchant_id  INTEGER NOT NULL REFERENCES merchants (id),
    out_trade_no TEXT    NOT NULL,
    version      INTEGER NOT NULL,
    channel      TEXT    NOT NULL,
    pay_type     TEXT    NOT NULL,
    name         TEXT    NOT NULL,
    money        TEXT    NOT NULL,
    amount       INTEGER NOT NULL,
    pay_amount   INTEGER NOT NULL,
    notify_url   TEXT    NOT NULL,
    return_url   TEXT    NOT NULL DEFAULT '',
    param        TEXT    NOT NULL DEFAULT '',
    client_ip    TEXT    NOT NULL DEFAULT '',
    device       TEXT    NOT NULL DEFAULT '',
    status       TEXT    NOT NULL,
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL,
    ended_at     INTEGER NOT NULL DEFAULT 0,
    released_at  INTEGER NOT NULL,
    paid_at      INTEGER NOT NULL DEFAULT 0,
    paid_amount  INTEGER NOT NULL DEFAULT 0,
    txn_id       INTEGER NOT NULL DEFAULT 0,
    api_trade_no TEXT    NOT NULL DEFAULT '',
    buyer        TEXT    NOT NULL DEFAULT '',
    updated_at   INTEGER NOT NULL,
    UNIQUE (merchant_id, out_trade_no)
);
CREATE INDEX orders_pay_amount_released ON orders (pay_amount, released_at);
CREATE INDEX orders_status_expires ON orders (status, expires_at);
CREATE INDEX orders_created_at ON orders (created_at);
CREATE INDEX orders_merchant_created ON orders (merchant_id, created_at);

CREATE TABLE transactions (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    channel           TEXT    NOT NULL,
    external_id       TEXT    NOT NULL,
    direction         TEXT    NOT NULL,
    amount            INTEGER NOT NULL,
    occurred_at       INTEGER NOT NULL,
    alipay_order_no   TEXT    NOT NULL DEFAULT '',
    merchant_order_no TEXT    NOT NULL DEFAULT '',
    other_account     TEXT    NOT NULL DEFAULT '',
    memo              TEXT    NOT NULL DEFAULT '',
    biz_desc          TEXT    NOT NULL DEFAULT '',
    kind              TEXT    NOT NULL DEFAULT '',
    raw               TEXT    NOT NULL DEFAULT '',
    fingerprint       TEXT    NOT NULL,
    state             TEXT    NOT NULL,
    order_id          INTEGER NOT NULL DEFAULT 0,
    created_at        INTEGER NOT NULL,
    updated_at        INTEGER NOT NULL,
    UNIQUE (channel, external_id)
);
CREATE INDEX transactions_occurred_at ON transactions (occurred_at);
CREATE INDEX transactions_state ON transactions (state);
CREATE INDEX transactions_order_id ON transactions (order_id);

CREATE TABLE anomalies (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    kind           TEXT    NOT NULL,
    transaction_id INTEGER NOT NULL DEFAULT 0,
    order_id       INTEGER NOT NULL DEFAULT 0,
    detail         TEXT    NOT NULL DEFAULT '',
    status         TEXT    NOT NULL,
    created_at     INTEGER NOT NULL,
    resolved_at    INTEGER NOT NULL DEFAULT 0,
    resolved_by    TEXT    NOT NULL DEFAULT '',
    note           TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX anomalies_status_created ON anomalies (status, created_at);
CREATE INDEX anomalies_transaction ON anomalies (transaction_id);
CREATE INDEX anomalies_order ON anomalies (order_id);

CREATE TABLE notifications (
    order_id   INTEGER PRIMARY KEY REFERENCES orders (id),
    status     TEXT    NOT NULL,
    attempts   INTEGER NOT NULL DEFAULT 0,
    first_at   INTEGER NOT NULL DEFAULT 0,
    next_at    INTEGER NOT NULL,
    last_at    INTEGER NOT NULL DEFAULT 0,
    last_error TEXT    NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE INDEX notifications_status_next ON notifications (status, next_at);

CREATE TABLE notify_attempts (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    order_id    INTEGER NOT NULL REFERENCES orders (id),
    manual      INTEGER NOT NULL DEFAULT 0,
    url         TEXT    NOT NULL,
    http_status INTEGER NOT NULL DEFAULT 0,
    response    TEXT    NOT NULL DEFAULT '',
    error       TEXT    NOT NULL DEFAULT '',
    success     INTEGER NOT NULL DEFAULT 0,
    duration_ms INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL
);
CREATE INDEX notify_attempts_order ON notify_attempts (order_id, created_at);

CREATE TABLE audit_logs (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    actor      TEXT    NOT NULL,
    action     TEXT    NOT NULL,
    target     TEXT    NOT NULL DEFAULT '',
    detail     TEXT    NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
);
CREATE INDEX audit_logs_created ON audit_logs (created_at);

-- +goose Down
DROP TABLE audit_logs;
DROP TABLE notify_attempts;
DROP TABLE notifications;
DROP TABLE anomalies;
DROP TABLE transactions;
DROP TABLE orders;
DROP TABLE merchants;
DROP TABLE settings;
DROP TABLE sessions;
DROP TABLE admins;
