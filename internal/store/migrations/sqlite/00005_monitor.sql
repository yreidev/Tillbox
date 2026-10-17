-- 监控和告警：告警记录、接收告警的 Telegram 会话。规则见 docs/reliability.md。

-- +goose Up
-- 同一个去重键同时只有一条进行中的告警（resolved_at = 0）。
CREATE TABLE alerts (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    key          TEXT    NOT NULL,
    level        TEXT    NOT NULL,
    title        TEXT    NOT NULL,
    detail       TEXT    NOT NULL DEFAULT '',
    link         TEXT    NOT NULL DEFAULT '',
    count        INTEGER NOT NULL DEFAULT 0,
    started_at   INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    resolved_at  INTEGER NOT NULL DEFAULT 0,
    notified_at  INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX alerts_open_key ON alerts (key) WHERE resolved_at = 0;
CREATE INDEX alerts_resolved_at ON alerts (resolved_at);

CREATE TABLE telegram_chats (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    chat_id    INTEGER NOT NULL UNIQUE,
    name       TEXT    NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    sent_at    INTEGER NOT NULL DEFAULT 0,
    error_at   INTEGER NOT NULL DEFAULT 0,
    error      TEXT    NOT NULL DEFAULT ''
);

-- +goose Down
DROP TABLE telegram_chats;
DROP TABLE alerts;
DELETE FROM settings WHERE key IN ('telegram', 'monitor_state') OR key LIKE 'confirm_state:%';
