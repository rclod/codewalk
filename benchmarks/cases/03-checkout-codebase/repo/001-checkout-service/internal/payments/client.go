package payments

import (
	"errors"
	"net/http"
	"time"
)

// ErrDeclined is returned when the provider refuses the authorisation.
var ErrDeclined = errors.New("payment declined")

// Client calls the external payments provider.
type Client struct {
	http *http.Client
	base string
}

// NewClient builds a client for the provider at baseURL.
func NewClient(baseURL string) *Client {
	return &Client{http: &http.Client{Timeout: 30 * time.Second}, base: baseURL}
}

// Authorize reserves funds for an order and reports whether the provider
// accepted the charge.
func (c *Client) Authorize(orderID string, amountCents int) error {
	req, err := http.NewRequest(http.MethodPost, c.base+"/authorizations", nil)
	if err != nil {
		return err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusPaymentRequired {
		return ErrDeclined
	}
	if res.StatusCode >= 400 {
		return errors.New("payments provider rejected the request")
	}
	return nil
}
