package orders

import (
	"context"
	"errors"
	"log"
	"time"

	"example.com/checkout/internal/outbox"
	"example.com/checkout/internal/payments"
	"example.com/checkout/internal/store"
)

// Worker completes orders that the request path only recorded.
type Worker struct {
	outbox   *outbox.Store
	orders   *store.Store
	payments *payments.Client
	interval time.Duration
}

// NewWorker wires the completion worker.
func NewWorker(o *outbox.Store, s *store.Store, p *payments.Client, interval time.Duration) *Worker {
	return &Worker{outbox: o, orders: s, payments: p, interval: interval}
}

// Run polls for pending work until the context is cancelled.
func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.processBatch(); err != nil {
				log.Printf("outbox batch failed: %v", err)
			}
		}
	}
}

// processBatch claims pending entries and authorises each one.
func (w *Worker) processBatch() error {
	entries, err := w.outbox.Claim(20)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		w.complete(entry)
	}
	return nil
}

// complete authorises one order and records the outcome. A declined payment is
// a final state; a transport failure is left pending for the next poll.
func (w *Worker) complete(entry outbox.Entry) {
	amount, err := w.orders.Amount(entry.OrderID)
	if err != nil {
		_ = w.outbox.Fail(entry.OrderID, err.Error())
		return
	}
	err = w.payments.Authorize(entry.OrderID, amount)
	switch {
	case err == nil:
		_ = w.orders.SetStatus(entry.OrderID, store.StatusAuthorized)
		_ = w.outbox.Done(entry.OrderID)
	case errors.Is(err, payments.ErrDeclined):
		_ = w.orders.SetStatus(entry.OrderID, store.StatusFailed)
		_ = w.outbox.Done(entry.OrderID)
	default:
		_ = w.outbox.Fail(entry.OrderID, err.Error())
	}
}
