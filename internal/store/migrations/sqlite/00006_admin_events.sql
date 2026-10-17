-- 发给管理员的业务消息（收款到账、付款人申诉）的待发队列。规则见 docs/reliability.md。

-- +goose Up
-- 同一笔订单或同一个申诉只排一次。status：pending 待发、sent 已发、skipped 没开或对象已不在。
CREATE TABLE admin_events (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    kind       TEXT    NOT NULL,
    ref_id     INTEGER NOT NULL,
    status     TEXT    NOT NULL DEFAULT 'pending',
    created_at INTEGER NOT NULL,
    done_at    INTEGER NOT NULL DEFAULT 0,
    UNIQUE (kind, ref_id)
);
CREATE INDEX admin_events_status ON admin_events (status, id);

-- +goose Down
DROP TABLE admin_events;
