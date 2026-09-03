// Package outbox holds the work handed from the request path to the worker.
//
// The outbox row is the entire contract between the two: the request path only
// writes it, and the worker only reads it.
package outbox

import "database/sql"

// Entry is one piece of pending completion work.
type Entry struct {
	OrderID  string
	Attempts int
}

// Store reads and updates outbox entries.
type Store struct {
	db *sql.DB
}

// New wraps a database handle.
func New(db *sql.DB) *Store { return &Store{db: db} }

// Claim returns up to limit pending entries and marks them as in progress, so
// two workers do not process the same order.
func (s *Store) Claim(limit int) ([]Entry, error) {
	rows, err := s.db.Query(
		"UPDATE order_outbox SET state = 'claimed', attempts = attempts + 1 "+
			"WHERE order_id IN (SELECT order_id FROM order_outbox WHERE state = 'pending' LIMIT ?) "+
			"RETURNING order_id, attempts", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.OrderID, &e.Attempts); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// Done removes a completed entry.
func (s *Store) Done(orderID string) error {
	_, err := s.db.Exec("DELETE FROM order_outbox WHERE order_id = ?", orderID)
	return err
}

// Fail returns an entry to the pending state so the next poll retries it.
func (s *Store) Fail(orderID, reason string) error {
	_, err := s.db.Exec(
		"UPDATE order_outbox SET state = 'pending', last_error = ? WHERE order_id = ?", reason, orderID)
	return err
}
