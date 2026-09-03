CREATE TABLE orders (
    id           TEXT PRIMARY KEY,
    customer     TEXT NOT NULL,
    amount_cents INTEGER NOT NULL,
    status       TEXT NOT NULL
);
