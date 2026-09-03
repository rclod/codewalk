-- Pending completion work, written in the same transaction as the order so a
-- crash between the two is impossible.
CREATE TABLE order_outbox (
    order_id   TEXT PRIMARY KEY,
    state      TEXT NOT NULL DEFAULT 'pending',
    attempts   INTEGER NOT NULL DEFAULT 0,
    last_error TEXT
);
