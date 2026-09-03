package orders

import (
	"encoding/json"
	"net/http"

	"example.com/checkout/internal/store"
)

// Handler exposes order creation over HTTP.
type Handler struct {
	service *Service
}

// NewHandler builds the HTTP handler.
func NewHandler(s *Service) *Handler { return &Handler{service: s} }

type createRequest struct {
	ID          string `json:"id"`
	Customer    string `json:"customer"`
	AmountCents int    `json:"amount_cents"`
}

// Create handles POST /orders. A 202 response means the order was accepted and
// recorded, not that payment succeeded.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if err := h.service.Create(req.ID, req.Customer, req.AmountCents); err != nil {
		http.Error(w, "could not create order", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]string{"id": req.ID, "status": store.StatusPending})
}
