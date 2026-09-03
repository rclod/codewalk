package store

import "database/sql"

// Order is a customer order as persisted.
type Order struct {
	ID          string
	Customer    string
	AmountCents int
	Status      string
}

// Order statuses. An order now moves through these over time rather than being
// created in its final state.
const (
	StatusPending    = "pending"
	StatusAuthorized = "authorized"
	StatusFailed     = "failed"
)

// Store persists orders and their pending completion work.
type Store struct {
	db *sql.DB
}

// New wraps a database handle.
func New(db *sql.DB) *Store { return &Store{db: db} }

// CreatePending writes the order and its outbox entry in one transaction, so
// an accepted order always has work queued for it.
func (s *Store) CreatePending(o Order) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(
		"INSERT INTO orders (id, customer, amount_cents, status) VALUES (?, ?, ?, ?)",
		o.ID, o.Customer, o.AmountCents, StatusPending,
	); err != nil {
		return err
	}
	if _, err := tx.Exec("INSERT INTO order_outbox (order_id, state) VALUES (?, 'pending')", o.ID); err != nil {
		return err
	}
	return tx.Commit()
}

// SetStatus records the outcome of completion.
func (s *Store) SetStatus(orderID, status string) error {
	_, err := s.db.Exec("UPDATE orders SET status = ? WHERE id = ?", status, orderID)
	return err
}

// Amount returns the amount to authorise for an order.
func (s *Store) Amount(orderID string) (int, error) {
	var amount int
	err := s.db.QueryRow("SELECT amount_cents FROM orders WHERE id = ?", orderID).Scan(&amount)
	return amount, err
}
