package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

// httpDoer is the subset of http.Client used here, so tests can substitute a
// transport without a network.
type httpDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// transport carries the shared request/retry behaviour for HTTP providers.
type transport struct {
	provider string
	client   httpDoer
	apiKey   string
	// maxRetries bounds retries on rate limiting, overload and transport errors.
	maxRetries int
	// sleep is injectable so retry behaviour is testable without real delays.
	sleep func(time.Duration)
}

func newTransport(provider, apiKey string, timeout time.Duration) *transport {
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	return &transport{
		provider:   provider,
		client:     &http.Client{Timeout: timeout},
		apiKey:     apiKey,
		maxRetries: 4,
		sleep:      time.Sleep,
	}
}

// postJSON sends a JSON request and decodes a JSON response, retrying
// transient failures with exponential backoff.
func (t *transport) postJSON(ctx context.Context, url string, headers map[string]string, body any, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return &Error{Kind: ErrBadInput, Provider: t.provider, Message: err.Error()}
	}

	var lastErr error
	for attempt := 0; attempt <= t.maxRetries; attempt++ {
		if attempt > 0 {
			delay := time.Duration(math.Pow(2, float64(attempt-1))) * time.Second
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			t.sleep(delay)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if err != nil {
			return &Error{Kind: ErrBadInput, Provider: t.provider, Message: err.Error()}
		}
		req.Header.Set("content-type", "application/json")
		req.Header.Set("user-agent", "codewalk")
		for k, v := range headers {
			req.Header.Set(k, v)
		}

		resp, err := t.client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			lastErr = &Error{Kind: ErrTransport, Provider: t.provider, Message: t.scrub(err.Error())}
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		if readErr != nil {
			lastErr = &Error{Kind: ErrTransport, Provider: t.provider, Message: t.scrub(readErr.Error())}
			continue
		}
		if resp.StatusCode >= 400 {
			e := &Error{
				Kind:     classify(resp.StatusCode),
				Provider: t.provider,
				Status:   resp.StatusCode,
				Message:  t.scrub(extractAPIError(data)),
			}
			if e.Retryable() && attempt < t.maxRetries {
				lastErr = e
				continue
			}
			return e
		}
		if err := json.Unmarshal(data, out); err != nil {
			return &Error{Kind: ErrOther, Provider: t.provider, Message: fmt.Sprintf("could not parse response: %v", err)}
		}
		return nil
	}
	return lastErr
}

// scrub removes the API key from any text that may be surfaced to a user.
func (t *transport) scrub(s string) string {
	s = Redact(s, t.apiKey)
	if len(s) > 2000 {
		s = s[:2000] + "…"
	}
	return s
}

func classify(status int) ErrorKind {
	switch {
	case status == 401 || status == 403:
		return ErrAuth
	case status == 429:
		return ErrRateLimit
	case status == 529 || status == 503 || status == 502:
		return ErrOverload
	case status >= 500:
		return ErrOverload
	case status >= 400:
		return ErrBadInput
	}
	return ErrOther
}

// extractAPIError pulls a human-readable message out of the common provider
// error envelopes without assuming a specific schema.
func extractAPIError(data []byte) string {
	var envelope struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(data, &envelope); err == nil {
		if envelope.Error.Message != "" {
			return envelope.Error.Message
		}
		if envelope.Message != "" {
			return envelope.Message
		}
	}
	s := strings.TrimSpace(string(data))
	if len(s) > 500 {
		s = s[:500] + "…"
	}
	return s
}
