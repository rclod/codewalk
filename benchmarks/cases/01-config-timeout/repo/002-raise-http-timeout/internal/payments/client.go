package payments

import (
	"net/http"
	"time"
)

// requestTimeout bounds a single call to the payments provider.
const requestTimeout = 30 * time.Second

// Client calls the external payments provider.
type Client struct {
	http *http.Client
	base string
}

// NewClient builds a client for the provider at baseURL.
func NewClient(baseURL string) *Client {
	return &Client{
		http: &http.Client{Timeout: requestTimeout},
		base: baseURL,
	}
}

// Authorize reserves funds for an order.
func (c *Client) Authorize(orderID string, amount int) error {
	req, err := http.NewRequest(http.MethodPost, c.base+"/authorizations", nil)
	if err != nil {
		return err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	return nil
}
