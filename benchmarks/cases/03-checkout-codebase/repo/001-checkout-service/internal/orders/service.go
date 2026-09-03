package orders

import (
	"example.com/checkout/internal/store"
)

// Service creates orders.
//
// Creation no longer authorises payment. It records the order and the work that
// still has to happen, and returns; the completion worker takes it from there.
type Service struct {
	store *store.Store
}

// NewService wires the order service.
func NewService(s *store.Store) *Service {
	return &Service{store: s}
}

// Create records a pending order together with its outbox entry.
func (s *Service) Create(id, customer string, amountCents int) error {
	return s.store.CreatePending(store.Order{
		ID:          id,
		Customer:    customer,
		AmountCents: amountCents,
	})
}
