package orders

import (
	"fmt"

	"example.com/checkout/internal/payments"
	"example.com/checkout/internal/store"
)

// Service creates orders.
type Service struct {
	store    *store.Store
	payments *payments.Client
}

// NewService wires the order service.
func NewService(s *store.Store, p *payments.Client) *Service {
	return &Service{store: s, payments: p}
}

// Create records an order and authorises payment for it. Both happen before
// this call returns, so a caller that receives no error knows the payment was
// accepted.
func (s *Service) Create(id, customer string, amountCents int) error {
	if err := s.payments.Authorize(id, amountCents); err != nil {
		return fmt.Errorf("authorize order %s: %w", id, err)
	}
	return s.store.InsertOrder(store.Order{
		ID:          id,
		Customer:    customer,
		AmountCents: amountCents,
		Status:      "authorized",
	})
}
