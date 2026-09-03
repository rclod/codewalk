package store

import "database/sql"

// Order is a customer order as persisted.
type Order struct {
	ID          string
	Customer    string
	AmountCents int
	Status      string
}

// Store persists orders.
type Store struct {
	db *sql.DB
}

// New wraps a database handle.
func New(db *sql.DB) *Store { return &Store{db: db} }

// InsertOrder writes a new order row.
func (s *Store) InsertOrder(o Order) error {
	_, err := s.db.Exec(
		"INSERT INTO orders (id, customer, amount_cents, status) VALUES (?, ?, ?, ?)",
		o.ID, o.Customer, o.AmountCents, o.Status,
	)
	return err
}
